package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/units"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// pageTemplates are the §5.7 pages. Each file defines `content` (and may define
// `title`); `layout.gohtml` defines the document around it.
//
// A page is its own [template.Template] rather than one big set, because
// html/template has no way to invoke a template chosen at run time: cloning the
// layout per page is what lets each page override `content` by name.
var pageTemplates = []string{
	"home",
	"boards",
	"board",
	"profile",
	"login",
	"dashboard",
	"notfound",
	"autherror",
	"docs_install",
	"docs_privacy",
	"docs_api",
}

type templateSet struct {
	pages map[string]*template.Template
	// feed renders the SSE fragments. It is the *same* partials file the pages
	// use, so a feed line patched in over SSE is byte-identical to one rendered
	// into the initial page (§5.7).
	feed *template.Template
}

func parseTemplates() (*templateSet, error) {
	set := &templateSet{pages: make(map[string]*template.Template, len(pageTemplates))}
	for _, name := range pageTemplates {
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(templateFS,
			"templates/layout.gohtml", "templates/partials.gohtml", "templates/"+name+".gohtml")
		if err != nil {
			return nil, fmt.Errorf("web: parse template %s: %w", name, err)
		}
		if t.Lookup("layout") == nil || t.Lookup("content") == nil {
			return nil, fmt.Errorf("web: template %s defines no content block", name)
		}
		set.pages[name] = t
	}
	feed, err := template.New("feed").Funcs(templateFuncs).ParseFS(templateFS, "templates/partials.gohtml")
	if err != nil {
		return nil, fmt.Errorf("web: parse feed partials: %w", err)
	}
	set.feed = feed
	return set, nil
}

// page is what every template is executed with.
type page struct {
	// Title goes in <title> and is the page's <h1> where one is wanted.
	Title string
	// Nav marks the active navigation entry ("home", "boards", "docs", "account").
	Nav string
	// SignedIn switches the navigation between "Sign in" and "Dashboard". It is
	// the presence of a well-formed session cookie, not proof the account still
	// exists — the dashboard itself re-checks (§4.5.4).
	SignedIn bool
	// Scripts are extra ES modules to load; only the dashboard has any.
	Scripts []string
	// BaseURL is the deployment's public URL, quoted in the docs pages so the
	// install instructions match the server the reader is looking at.
	BaseURL string
	// Data is the page's own payload.
	Data any
}

// render executes a page into a buffer and only then writes it.
//
// The buffer is the point: a template error halfway through would otherwise have
// already sent a 200 and half a document, which is indistinguishable from a
// network failure at the other end.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, cache string, p page) {
	t, ok := s.tpl.pages[name]
	if !ok {
		s.deps.Log.Error("no such template", "template", name, "path", r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.SignedIn = s.signedIn(r)
	p.BaseURL = s.deps.Config.Server.BaseURL

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.deps.Log.Error("rendering a page failed", "template", name, "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cache)
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.deps.Log.Debug("page write failed", "path", r.URL.Path, "err", err)
	}
}

// fragment renders one of the shared partials — what the SSE feed patches in.
func (s *Server) fragment(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := s.tpl.feed.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// signedIn reports whether the request carries a session cookie this server
// signed. It never touches the database: the navigation does not need to be
// right about a banned account, and the pages that do re-check.
func (s *Server) signedIn(r *http.Request) bool {
	_, err := s.deps.Sessions.From(r)
	return err == nil
}

// --- template functions ---------------------------------------------------------

// Number formatting is [units], not a second implementation of it: the SPA
// renders the same values from the same JSON, and the two frontends showing one
// record differently is exactly the failure that package exists to prevent.
// Read its comment before changing what a number looks like here.
var templateFuncs = template.FuncMap{
	// value is the bare number — thousands grouped, three significant figures —
	// for a column whose header already carries the unit.
	"value": units.Number,
	// unit is the number with its unit applied: metres scaled to km/Mm, a
	// career time in milliseconds rendered as a duration, a count labelled. For
	// a cell that has to stand on its own.
	"unit":       units.Format,
	"datetime":   formatDateTime,
	"date":       formatDate,
	"ctx":        contextPairs,
	"pct":        percent,
	"feedID":     feedItemID,
	"statPath":   func(stat string) string { return "/boards/" + stat },
	"playerPath": func(handle string) string { return "/p/" + handle },
	"dict":       dict,
	"add":        func(a, b int) int { return a + b },
}

// formatDateTime renders a unix-ms timestamp in UTC. Everything catlog stores is
// unix ms and every page shows UTC: a leaderboard is a shared artefact, and
// localising it would make two people describing the same row disagree.
func formatDateTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

func formatDate(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

func percent(n, of int) int {
	if of <= 0 {
		return 0
	}
	return int(math.Round(float64(n) / float64(of) * 100))
}

// pair is one decoded entry of a board row's `context` blob.
type pair struct {
	Key   string
	Value string
}

// contextPairs decodes a fold's `context` JSON for display.
//
// The blob is written by the folds, not by players, but its *values* come from
// event payloads, which are player-supplied (§4.2 names are a moderation
// surface). It is therefore decoded into strings and rendered through
// html/template's escaping rather than being emitted as JSON into the page.
func contextPairs(raw json.RawMessage) []pair {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]pair, 0, len(m))
	for k, v := range m {
		// The key is what says which unit the number is in — `energy_j` is
		// joules, `speed_ms` is metres per second — so the label is stripped of
		// its underscores for display and handed to units.ForKey for meaning.
		out = append(out, pair{Key: strings.ReplaceAll(k, "_", " "), Value: scalar(k, v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func scalar(key string, v any) string {
	switch t := v.(type) {
	case nil:
		return "—"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return units.Format(t, units.ForKey(key))
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "—"
		}
		return string(b)
	}
}

// dict builds a map inside a template, so a partial can take more than one
// argument.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd argument count %d", len(kv))
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is not a string", i)
		}
		m[k] = kv[i+1]
	}
	return m, nil
}
