package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/readapi"
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
	"badges",
	"badge",
	"player_badges",
	"systems",
	"system",
	"profile",
	"saves",
	"save",
	"events",
	"events_all",
	"stats",
	"search",
	"compare",
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
	// Search pre-fills the header search box, so a results page shows what was
	// asked for rather than an empty field.
	Search string
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

// Number formatting is [units], not a second implementation of it: the same
// values reach a reader as JSON from the read API, and the page and the API
// disagreeing about one record is exactly the failure that package exists to
// prevent. Read its comment before changing what a number looks like here.
var templateFuncs = template.FuncMap{
	// value is the number with its unit applied: metres scaled to km/Mm, a
	// career time in milliseconds rendered as a duration, a count labelled.
	//
	// Every value cell on the site calls this, header unit or not. That is not
	// redundant for the scaling quantities — a length column legitimately mixes
	// "999 m" and "1.82 Mm" — and it buys column-independence on the profile,
	// comparison and tile surfaces, where there is no header to carry the unit.
	"value": units.Format,
	// num and numUnit are `number` and `value` as the element `intl.js`
	// re-renders in the reader's locale — see [numberHTML]. Every *text*
	// position uses these; the plain string functions stay for the attribute
	// positions (`title`, `aria-label`), where a span cannot go.
	"num":     num,
	"numUnit": numUnit,
	// unitLabel is the column header for a value column (rule 7). It is the unit
	// itself wherever a cell ends in that unit, and the name of the quantity
	// where it does not — a career-time column reads "243d 01h", so its header
	// says "Time" rather than "ms". Rendered as returned: `thead th.value` turns
	// the uppercasing off, because "M/S" is not a unit and "RUDS" is not a word
	// catlog writes.
	"unitLabel": units.Label,
	// measured is the same fact mid-sentence, under "Measured in ___." — and it
	// keeps the storage unit that unitLabel drops, so the page still says
	// somewhere that the API publishes milliseconds.
	"measured": units.Measured,
	// number is rule 2 alone, for a count that is not in any unit at all.
	"number": number,
	// exact is the figure as the API sent it, for `data-value` and `title`. It
	// is what a reader uses to recover the digits "48 MJ" hides, and what the
	// e2e suite reads instead of trying to strip non-digits out of "5m 13s".
	"exact":       exactValue,
	"exactUnit":   exactWithUnit,
	"datetime":    formatDateTime,
	"date":        formatDate,
	"iso":         isoInstant,
	"trimHandle":  trimHandle,
	"feedItem":    newFeedItem,
	"ctx":         contextPairs,
	"blob":        prettyJSON,
	"standing":    standing,
	"periodLabel": periodLabel,
	"scopeLabel":  scopeLabel,
	"barWidth":    barWidth,
	"percent":     percent,
	"rankClass":   rankClass,
	"pageOffset":  pageOffset,
	"titleize":    titleize,
	"feedID":      feedItemID,
	"eventItem":   newEventItem,
	"eventRowID":  eventRowID,
	"statPath":    func(stat string) string { return "/boards/" + stat },
	"badgePath":   func(badge string) string { return "/badges/" + badge },
	"playerPath":  func(handle string) string { return "/p/" + handle },
	"eventsPath":  func(handle string) string { return "/p/" + handle + "/events" },
	"comparePath": comparePath,
	"query":       url.Values.Encode,
	"dict":        dict,
	"add":         func(a, b int) int { return a + b },
	"sub":         func(a, b int) int { return a - b },
}

// --- localisable numbers ---------------------------------------------------

// numUnit renders a value and its unit as the element a browser re-renders.
func numUnit(v float64, unit string) template.HTML { return numberHTML(units.Split(v, unit)) }

// num is [number] as that same element.
func num(v any) template.HTML {
	f, ok := asFloat(v)
	if !ok {
		return template.HTML(notANumber)
	}
	return numberHTML(units.Split(f, ""))
}

// notANumber is what a value of a kind this build cannot widen renders as. It
// matches units' own, because a hole in a column should look the same wherever
// it came from.
const notANumber = "&#8212;"

// numberHTML wraps a rendered number in the element `intl.js` re-renders it
// from (site/assets/js/intl.js).
//
// # Why a browser finishes a number the server started
//
// Every public page is served `s-maxage=30` to a shared cache (§4.8), so one
// response goes to everybody: there is no locale available here to render in,
// exactly as there is no handle available to personalise with (see me.js). The
// separator a reader expects is nonetheless theirs — 1,234,567 in Cambridge MA,
// 1.234.567 in Berlin, 12,34,567 in Bengaluru — and only the browser knows
// which. So the server renders `units`' canonical form as the text, publishes
// the number and its precision as attributes, and `Intl.NumberFormat` finishes
// the job on arrival.
//
// Attributes rather than a re-parse of the text, because the text is not always
// a number: "1.82 Mm" is a number and a suffix, "243d 01h" is neither, and a
// browser given only the string would have to reimplement `units` to tell them
// apart. [units.Split] has already done that, and a two-component duration
// comes back with IsNumber false and is left exactly as it is.
//
// A reader with no JavaScript keeps the canonical form, which is a conventional
// one rather than the U+202F this used to show everybody.
func numberHTML(p units.Parts) template.HTML {
	tail := template.HTMLEscapeString(p.Tail)
	if !p.IsNumber {
		return template.HTML(template.HTMLEscapeString(p.Head) + tail)
	}
	var b strings.Builder
	b.WriteString(`<span class="n" data-n="`)
	// 'f' with -1 precision: the shortest decimal that round-trips, never an
	// exponent. `Number()` on the other side has to read this back as the same
	// double the text was rendered from.
	b.WriteString(strconv.FormatFloat(p.Number, 'f', -1, 64))
	b.WriteString(`" data-d="`)
	b.WriteString(strconv.Itoa(p.Decimals))
	b.WriteString(`">`)
	b.WriteString(template.HTMLEscapeString(p.Head))
	b.WriteString(`</span>`)
	b.WriteString(tail)
	return template.HTML(b.String())
}

// asFloat widens the numeric kinds these pages carry. text/template does not
// convert between them, and the counts on a page are variously `int` (how many
// boards), `int64` (a board's population) and `float64` (a value off a row).
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	default:
		return 0, false
	}
}

// number groups a count for display.
//
// It takes `any` because text/template does not convert between numeric kinds,
// and the counts on these pages are variously `int` (how many boards), `int64`
// (a board's population) and `float64` (a value off a row). One entry point that
// widens them is less error-prone than three template functions that differ only
// in a signature.
func number(v any) string {
	f, ok := asFloat(v)
	if !ok {
		return "—"
	}
	return units.Number(f)
}

// exactValue renders a float the way the JSON API published it: no grouping, no
// significant-figure trim, no unit. It is the machine-readable half of every
// value cell (`data-value`).
func exactValue(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// exactWithUnit is the `title` on a formatted value: the undecorated figure and
// the unit the event carried, so hovering "48 MJ" says "48000000 J".
func exactWithUnit(v float64, unit string) string {
	s := exactValue(v)
	if s == "" {
		return "not a number"
	}
	if unit == "" {
		return s
	}
	return s + " " + unit
}

// pageOffset is the offset of the board page a rank falls on, so a profile row
// can link to "where I actually sit" without a new endpoint.
func pageOffset(rank int) int {
	if rank < 1 {
		return 0
	}
	return (rank - 1) / BoardRows * BoardRows
}

// rankClass grades a placement for the profile table. Top three get the accent,
// the top tenth gets full-strength text, everyone else is muted — a ranking, not
// a verdict.
func rankClass(rank int, players int64) string {
	switch {
	case rank <= 3:
		return "rank-top"
	case players > 0 && float64(rank) <= float64(players)/10:
		return "rank-high"
	default:
		return "rank-rest"
	}
}

// comparePath builds `/compare?handles=…` out of any mixture of handles and
// lists of handles, so a template can write `comparePath .With .Handle` to mean
// "the set I am carrying, plus this one".
//
// The result goes back through [readapi.SplitHandles], which deduplicates and
// caps at eight. That makes "+ compare" idempotent on a handle already in the
// set, and it means the link a template offers can never ask for more handles
// than the endpoint will answer with — the cap is applied where the URL is
// built rather than silently by the server afterwards.
func comparePath(parts ...any) string {
	var handles []string
	for _, p := range parts {
		switch t := p.(type) {
		case string:
			if t != "" {
				handles = append(handles, t)
			}
		case []string:
			handles = append(handles, t...)
		}
	}
	handles = readapi.SplitHandles(strings.Join(handles, ","))
	if len(handles) == 0 {
		return "/compare"
	}
	return "/compare?handles=" + url.QueryEscape(strings.Join(handles, ","))
}

// titleize renders a fold's lowercase content word as a display name — "luna" →
// "Luna", "ground_impact" → "Ground Impact" — matching stats.titleize, which the
// server already applied to the board titles themselves. Board titles must not
// be run through this a second time; context *values* must.
func titleize(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for i, w := range words {
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

// prettyJSON re-indents a stored blob for a disclosure.
//
// It is decoded and re-encoded rather than printed, so a malformed or hostile
// blob cannot end a <script> or open a tag: html/template escapes the result as
// text, and what it escapes is bytes encoding/json produced.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(out)
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

// isoInstant renders a unix-ms timestamp as an RFC 3339 / ISO-8601 instant, for
// machine-readable attributes — a `<time datetime>` must carry a valid datetime
// string, and the display form ("2026-08-07 14:32 UTC") is not one. Returns ""
// for an instant that does not exist rather than inventing an epoch.
func isoInstant(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// trimHandle strips a leading "<handle> " off a feed summary, returning ""
// when the handle is not the prefix. stats.Summarize composes every feed line
// handle-first, which is what lets the `feed-item` template render the handle
// as a profile link and the rest of the sentence as prose — with the whole
// sentence as the fallback when the shape ever changes.
func trimHandle(summary, handle string) string {
	rest, ok := strings.CutPrefix(summary, handle+" ")
	if !ok {
		return ""
	}
	return rest
}

// standing is how full a profile row's rank bar is: 100 for first place, falling
// towards 0 at the back of the field. It reads the way a reader expects a bar to
// read — more is better — which the raw percentile does not, since rank 1 of 41
// is a *2 %* percentile and a 2 %-full bar next to a first place would be
// nonsense.
//
// The clamp is load-bearing rather than defensive: `rank` is filtered of banned
// players and `players` is not, so a rank can legitimately be better than the
// denominator implies and the naive ratio can leave the interval. A bar 104 %
// wide would be a visible lie about arithmetic nobody would think to question.
func standing(rank int, players int64) int {
	if players <= 0 || rank < 1 {
		return 0
	}
	behind := int(math.Round((1 - float64(rank-1)/float64(players)) * 100))
	return min(max(behind, 0), 100)
}

// pair is one decoded entry of a board row's `context` blob.
//
// Value is pre-rendered HTML because a numeric entry is [numberHTML]'s element
// rather than a string — the detail chips localise like every other number on
// the page. Everything that is not a number goes through
// [template.HTMLEscapeString] on the way in: these values come from event
// payloads, which are player-supplied.
type pair struct {
	Key   string
	Value template.HTML
}

// contextKeys is the display allow-list for the Detail column: the
// human-meaningful half of a fold's `context` blob, in the order it reads best.
//
// An unrecognised key is **hidden**, not shown. That is the whole point of an
// allow-list here rather than a deny-list: the fold layer can add a context key
// without a frontend release, and a new internal id cannot leak into a public
// table merely because nobody remembered to exclude it.
//
// What is deliberately off it: `flight` — a client-minted ULID that means
// nothing to a reader and eats the widest column — and `career`, which the
// server has already relabelled per player (readapi/privacy.go) and which is
// still a 16-character token no reader wants in a table. Both remain visible in
// the row's Details disclosure and in the raw event log, which is what those
// surfaces are for.
var contextKeys = []string{"body", "from", "energy_j", "t1_sim"}

// contextPairs decodes a fold's `context` JSON for display, keeping only
// [contextKeys].
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
	out := make([]pair, 0, len(contextKeys))
	for _, k := range contextKeys {
		v, ok := m[k]
		if !ok {
			continue
		}
		// The key is what says which unit the number is in — `energy_j` is
		// joules, `t1_sim` is seconds — so the label is stripped of its
		// underscores for display and handed to units.ForKey for meaning.
		out = append(out, pair{Key: strings.ReplaceAll(k, "_", " "), Value: scalar(k, v)})
	}
	return out
}

func scalar(key string, v any) template.HTML {
	switch t := v.(type) {
	case nil:
		return template.HTML(notANumber)
	case string:
		// Body and vehicle names arrive lowercase from the game; the board
		// titles the server generates are already title case, so the values
		// beside them should read the same way.
		return template.HTML(template.HTMLEscapeString(titleize(t)))
	case bool:
		return template.HTML(strconv.FormatBool(t))
	case float64:
		return numUnit(t, units.ForKey(key))
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return template.HTML(notANumber)
		}
		return template.HTML(template.HTMLEscapeString(string(b)))
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
