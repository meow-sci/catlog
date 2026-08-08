package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
	"github.com/meow-sci/catlog/server/internal/web"
)

// --- fixture -------------------------------------------------------------------

// fakeRead answers the page handlers with fixed data. The point of these tests
// is the rendering, not the queries — those are readapi's, and its own suite
// covers them.
type fakeRead struct {
	boards readapi.BoardsResponse
	board  map[string]readapi.BoardResponse
	player map[string]readapi.PlayerResponse
	events map[string]readapi.EventsResponse
	global readapi.EventsResponse
	// players maps a player id to its handle for PublicEvents; an id off the
	// map is a handle-less player, which PublicEvents must drop.
	players map[int64]string
	handles []string
	err     error
	// lastPeriod and lastOffset record what the board page actually asked for,
	// so a test can assert that `?period=` and `?offset=` reach the read layer
	// rather than merely appearing in the URL.
	lastPeriod string
	lastOffset int
}

func (f *fakeRead) BoardList(context.Context) (readapi.BoardsResponse, error) {
	return f.boards, f.err
}

func (f *fakeRead) Board(_ context.Context, stat, period, _ string, limit, offset int) (readapi.BoardResponse, bool, error) {
	f.lastPeriod, f.lastOffset = period, offset
	if f.err != nil {
		return readapi.BoardResponse{}, true, f.err
	}
	b, ok := f.board[stat]
	if !ok {
		return readapi.BoardResponse{}, false, nil
	}
	b.Limit, b.Offset, b.Period = limit, offset, period
	if offset > 0 {
		// Page two of a one-page board is empty, which is what makes the pager
		// assertions mean something.
		b.Rows = nil
	}
	return b, true, nil
}

func (f *fakeRead) Player(_ context.Context, handle string) (readapi.PlayerResponse, bool, error) {
	if f.err != nil {
		return readapi.PlayerResponse{}, true, f.err
	}
	p, ok := f.player[handle]
	return p, ok, nil
}

func (f *fakeRead) PlayerEvents(_ context.Context, handle, typ string, _ int64, limit int) (readapi.EventsResponse, bool, error) {
	if f.err != nil {
		return readapi.EventsResponse{}, true, f.err
	}
	e, ok := f.events[handle]
	if !ok {
		return readapi.EventsResponse{}, false, nil
	}
	e.Limit, e.Type = limit, typ
	if typ != "" {
		kept := e.Events[:0:0]
		for _, ev := range e.Events {
			if ev.Type == typ {
				kept = append(kept, ev)
			}
		}
		e.Events = kept
	}
	return e, true, nil
}

func (f *fakeRead) GlobalEvents(_ context.Context, typ string, _ int64, limit int) (readapi.EventsResponse, error) {
	if f.err != nil {
		return readapi.EventsResponse{}, f.err
	}
	out := f.global
	out.Limit, out.Type = limit, typ
	if typ != "" {
		kept := out.Events[:0:0]
		for _, ev := range out.Events {
			if ev.Type == typ {
				kept = append(kept, ev)
			}
		}
		out.Events = kept
	}
	return out, nil
}

// PublicEvents mirrors the readapi seam's contract shallowly: handle-less
// players dropped, every surviving row named. The real redaction is readapi's
// and is tested there; these tests assert the web handler renders only what
// this seam returned.
func (f *fakeRead) PublicEvents(_ context.Context, batch []store.StoredEvent) ([]readapi.EventRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	rows := make([]readapi.EventRow, 0, len(batch))
	for _, ev := range batch {
		handle, ok := f.players[ev.PlayerID]
		if !ok {
			continue
		}
		row := readapi.EventRow{
			Seq: ev.Seq, Handle: handle, Type: ev.Type, Ver: ev.Ver,
			Career: ev.Career, Recv: ev.RecvTime, Payload: ev.Payload,
		}
		if ev.SimTime.Valid {
			t := ev.SimTime.Float64
			row.SimT = &t
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (f *fakeRead) Search(q string, limit int) readapi.SearchResponse {
	out := readapi.SearchResponse{Query: q, Limit: limit, Handles: []string{}}
	for _, h := range f.handles {
		if strings.Contains(h, strings.ToLower(q)) {
			out.Handles = append(out.Handles, h)
		}
	}
	return out
}

func (f *fakeRead) Compare(ctx context.Context, handles []string) (readapi.CompareResponse, error) {
	if f.err != nil {
		return readapi.CompareResponse{}, f.err
	}
	out := readapi.CompareResponse{Handles: []readapi.ComparePlayer{}, Boards: []readapi.CompareBoard{}}
	byStat := map[string]*readapi.CompareBoard{}
	var order []string
	for _, h := range handles {
		p, ok, err := f.Player(ctx, h)
		if err != nil {
			return readapi.CompareResponse{}, err
		}
		if !ok {
			out.Handles = append(out.Handles, readapi.ComparePlayer{Handle: h})
			continue
		}
		out.Handles = append(out.Handles, readapi.ComparePlayer{Handle: p.Handle, Found: true, Since: p.Since})
		for _, row := range p.Stats {
			b, seen := byStat[row.Stat]
			if !seen {
				b = &readapi.CompareBoard{
					Stat: row.Stat, Title: row.Title, Unit: row.Unit,
					Ascending: row.Ascending, Players: row.Players,
				}
				byStat[row.Stat] = b
				order = append(order, row.Stat)
			}
			b.Rows = append(b.Rows, readapi.CompareRow{
				Handle: p.Handle, Value: row.Value, Rank: row.Rank,
				Context: row.Context, Updated: row.Updated,
			})
		}
	}
	for _, stat := range order {
		out.Boards = append(out.Boards, *byStat[stat])
	}
	return out, nil
}

// fakeAccounts stands in for identity.Server on the dashboard.
type fakeAccounts struct {
	data identity.DashboardData
	err  error
}

func (f *fakeAccounts) LoadDashboard(context.Context, keys.UserKey) (identity.DashboardData, error) {
	return f.data, f.err
}

type fixture struct {
	srv         *web.Server
	mux         *http.ServeMux
	read        *fakeRead
	accounts    *fakeAccounts
	sessions    *identity.Sessions
	broadcaster *projector.Broadcaster
	raw         *projector.RawBroadcaster
	projections *store.Projections
	userKey     keys.UserKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	proj := testutil.MemProjections(t)
	live := projector.NewLive(proj)
	sessions, err := identity.NewSessions(make([]byte, 32), false)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}

	read := &fakeRead{
		boards: readapi.BoardsResponse{MinPlayers: 2, Boards: []readapi.BoardSummary{
			{Stat: stats.StatBiggestLithobrakeSurvived, Title: "Biggest Lithobrake Survived", Unit: "m/s", Count: 2},
			{Stat: stats.StatRUDTotal, Title: "Rapid Unscheduled Disassemblies", Unit: "RUDs", Count: 1},
			// A board whose key came out of the event stream: no constant in the
			// server mentions this place, and the index must render it anyway.
			{Stat: "fastest_to_zephyria", Title: "Fastest to Zephyria", Unit: "s", Ascending: true, Count: 2},
		}},
		board:   map[string]readapi.BoardResponse{},
		handles: []string{"demo_crasher", "demo_ace"},
		player: map[string]readapi.PlayerResponse{
			"demo_crasher": {
				Handle: "demo_crasher", Since: 1767225600000,
				Stats: []readapi.PlayerRow{{
					Stat: stats.StatBiggestLithobrakeSurvived, Title: "Biggest Lithobrake Survived",
					Unit: "m/s", Value: 214, Rank: 1, Players: 41, Updated: 1767225600000,
					// `flight` and `career` are on the blob on purpose: the
					// display allow-list has to drop them from the visible pairs
					// while the Details disclosure still shows what the API sent.
					Context: json.RawMessage(`{"body":"duna","energy_j":48000000,"flight":"01J9VFLIGHT","career":"b7k2q9x4m0nrt3vz"}`),
				}},
			},
			"demo_ace": {
				Handle: "demo_ace", Since: 1767225600000,
				Stats: []readapi.PlayerRow{{
					Stat: stats.StatBiggestLithobrakeSurvived, Title: "Biggest Lithobrake Survived",
					Unit: "m/s", Value: 62, Rank: 2, Players: 41, Updated: 1767225600000,
				}, {
					// A career-time board: milliseconds in, a duration out. It
					// is the case that breaks any test reading a number out of
					// the rendered text.
					Stat: "fastest_to_zephyria", Title: "Fastest to Zephyria",
					Unit: "ms", Value: 313000, Rank: 1, Players: 2, Ascending: true,
					Updated: 1767225600000,
				}},
			},
		},
		events: map[string]readapi.EventsResponse{
			"demo_crasher": {
				Handle: "demo_crasher", Next: "41822",
				Events: []readapi.EventRow{
					{
						Seq: 41824, ID: "01J9VEVENT1", Type: "vehicle.impact", Ver: 1,
						Session: "01J9VSESSION", Flight: "01J9VFLIGHT",
						Career: "b7k2q9x4m0nrt3vz", SimT: ptr(1832.5), Recv: 1767225600000,
						Payload: json.RawMessage(`{"speed_ms":214,"body":"duna","survived":true}`),
					},
					{
						Seq: 41823, ID: "01J9VEVENT2", Type: "vehicle.rud", Ver: 1, Recv: 1767225500000,
						Payload: json.RawMessage(`{"cause":"ground_impact"}`),
					},
				},
			},
		},
		// The global log: demo_crasher's page interleaved with another player,
		// every row naming its handle — what GlobalEvents publishes.
		global: readapi.EventsResponse{
			Next: "41822",
			Events: []readapi.EventRow{
				{
					Seq: 41824, Handle: "demo_crasher", ID: "01J9VEVENT1", Type: "vehicle.impact", Ver: 1,
					Session: "01J9VSESSION", Flight: "01J9VFLIGHT",
					Career: "b7k2q9x4m0nrt3vz", SimT: ptr(1832.5), Recv: 1767225600000,
					Payload: json.RawMessage(`{"speed_ms":214,"body":"duna","survived":true}`),
				},
				{
					Seq: 41823, Handle: "demo_ace", ID: "01J9VEVENT3", Type: "vehicle.orbit", Ver: 1, Recv: 1767225550000,
					Payload: json.RawMessage(`{"body":"luna"}`),
				},
			},
		},
		players: map[int64]string{1: "demo_crasher", 2: "demo_ace"},
	}
	for _, stat := range web.FeaturedBoards {
		read.board[stat] = readapi.BoardResponse{Stat: stat, Title: "Board " + stat, Unit: "m/s"}
	}
	read.board[stats.StatBiggestLithobrakeSurvived] = readapi.BoardResponse{
		Stat: stats.StatBiggestLithobrakeSurvived, Title: "Biggest Lithobrake Survived", Unit: "m/s",
		Rows: []readapi.BoardRow{
			{Rank: 1, Handle: "demo_crasher", Value: 214, Updated: 1767225600000, Context: json.RawMessage(`{"body":"duna"}`)},
			{Rank: 2, Handle: "demo_ace", Value: 62, Updated: 1767225600000},
		},
	}

	accounts := &fakeAccounts{}
	broadcaster := projector.NewBroadcaster()
	raw := projector.NewRawBroadcaster()

	srv, err := web.New(web.Deps{
		Config:      testutil.Config(t),
		Read:        read,
		Projections: live,
		Feed:        broadcaster,
		Raw:         raw,
		Sessions:    sessions,
		Accounts:    accounts,
		Log:         testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	return &fixture{
		srv: srv, mux: mux, read: read, accounts: accounts, sessions: sessions,
		broadcaster: broadcaster, raw: raw, projections: proj,
		userKey: keys.UserKey{1, 2, 3},
	}
}

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, httptest.NewRequest(http.MethodGet, path, nil))
}

func (f *fixture) do(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

// signedIn returns a request carrying a valid session cookie.
func (f *fixture) signedIn(t *testing.T, path string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	f.sessions.Issue(w, f.userKey)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func mustContain(t *testing.T, body, want, what string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("%s: response does not contain %q", what, want)
	}
}

func mustNotContain(t *testing.T, body, unwanted, what string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Errorf("%s: response contains %q and should not", what, unwanted)
	}
}

func ptr[T any](v T) *T { return &v }

// --- pages ------------------------------------------------------------------------

func TestEveryPageRenders(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 200, []string{`id="home-title"`, `id="featured-boards"`, `id="feed-panel"`, `<ul id="feed"`}},
		{"/boards", 200, []string{
			`id="boards-index"`, `data-stat="rud_total"`, "Biggest Lithobrake Survived",
			// The index is whatever the server listed, including a board no
			// constant in this repository names.
			`data-stat="fastest_to_zephyria"`, "Fastest to Zephyria", `id="boards-note"`,
		}},
		{"/boards/biggest_lithobrake_survived", 200, []string{
			`id="board-title"`, `data-handle="demo_crasher"`, `data-rank="2"`,
			`id="board-periods"`, `data-period="weekly"`, `id="board-range"`,
		}},
		{"/boards/no_such_board", 404, []string{`id="not-found"`}},
		{"/boards/biggest_lithobrake_survived?period=nope", 404, []string{`id="not-found"`}},
		{"/p/demo_crasher", 200, []string{`id="profile-handle"`, `data-stat="biggest_lithobrake_survived"`, "#1"}},
		{"/p/nobody", 404, []string{`id="not-found"`}},
		{"/p/demo_crasher/events", 200, []string{
			`id="events-log"`, `data-type="vehicle.impact"`, `id="events-older"`,
		}},
		{"/p/nobody/events", 404, []string{`id="not-found"`}},
		{"/search", 200, []string{`id="search-idle"`}},
		{"/search?q=a", 200, []string{`id="search-short"`}},
		{"/search?q=demo", 200, []string{`id="search-results"`, `data-handle="demo_ace"`}},
		{"/search?q=zzzz", 200, []string{`id="search-empty"`}},
		{"/compare", 200, []string{`id="compare-empty"`}},
		{"/compare?handles=demo_crasher,demo_ace", 200, []string{
			`id="compare-table"`, `data-handle="demo_crasher"`, `data-handle="demo_ace"`,
		}},
		{"/login", 200, []string{`id="login-discord"`, `id="login-google"`, `id="login-github"`}},
		{"/docs/install", 200, []string{`id="docs-title"`, "credential.json"}},
		{"/docs/privacy", 200, []string{`id="privacy-no-email"`}},
		{"/docs/api", 200, []string{`id="docs-api-endpoints"`, "/v1/feed/sse"}},
		{"/docs/nope", 404, []string{`id="not-found"`}},
		{"/nothing/here", 404, []string{`id="not-found"`}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := f.get(t, tc.path)
			if w.Code != tc.status {
				t.Fatalf("GET %s = %d, want %d\n%s", tc.path, w.Code, tc.status, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s content-type = %q", tc.path, ct)
			}
			body := w.Body.String()
			// Every page is a complete document with the layout around it.
			mustContain(t, body, "<!doctype html>", tc.path)
			mustContain(t, body, `src="/static/vendor/datastar.js"`, tc.path)
			for _, want := range tc.want {
				mustContain(t, body, want, tc.path)
			}
		})
	}
}

// A board page must render rows in the order the read API returned them: the
// rank column is positional over *visible* rows, so re-sorting here would make
// the page disagree with /v1/leaderboards/{stat}.
func TestBoardRowsKeepTheirOrderAndRanks(t *testing.T) {
	f := newFixture(t)
	body := f.get(t, "/boards/biggest_lithobrake_survived").Body.String()

	first := strings.Index(body, `data-handle="demo_crasher"`)
	second := strings.Index(body, `data-handle="demo_ace"`)
	switch {
	case first < 0 || second < 0:
		t.Fatalf("both handles should be on the page:\n%s", body)
	case first > second:
		t.Errorf("rank 1 (demo_crasher) renders after rank 2 (demo_ace)")
	}
}

// Every value cell carries the exact figure as `data-value` alongside the
// rendered string, and the rendered string comes from `units`.
//
// The pair is the point. A career-time board publishes milliseconds and renders
// "5m 13s"; anything downstream that wants the number — a test, a chart, a
// reader hovering for the digits behind "48 MJ" — reads the attribute, and
// reconstructing it out of the text is the thing that stops working silently.
func TestValueCellsCarryBothTheRenderedAndTheExactFigure(t *testing.T) {
	f := newFixture(t)
	body := f.get(t, "/p/demo_ace").Body.String()

	// A speed: grouped, never scaled — the same number a KSA player reads.
	mustContain(t, body, `data-value="62" title="62 m/s"`, "profile")
	mustContain(t, body, "62 m/s", "profile")

	// A career time: milliseconds in, a two-component duration out. The exact
	// figure stays the milliseconds the API published.
	mustContain(t, body, `data-value="313000" title="313000 ms"`, "profile")
	mustContain(t, body, "5m 13s", "profile")
}

// The Detail column is an allow-list, so a fold can add a context key without a
// frontend release and an internal id cannot appear in a public table merely
// because nobody remembered to exclude it.
func TestBoardDetailShowsTheAllowListAndHidesTheRest(t *testing.T) {
	f := newFixture(t)
	body := f.get(t, "/p/demo_crasher").Body.String()

	// `body` is on the list, and `energy_j` renders through the unit table.
	mustContain(t, body, `<span class="ctx-key">body</span> <span class="ctx-value">Duna</span>`, "profile")
	mustContain(t, body, `<span class="ctx-key">energy j</span> <span class="ctx-value">48 MJ</span>`, "profile")

	// `flight` and `career` are not pairs. They are still inside the disclosure,
	// which shows the blob as the API sent it — already post-redaction.
	mustNotContain(t, body, `<span class="ctx-key">flight</span>`, "profile")
	mustNotContain(t, body, `<span class="ctx-key">career</span>`, "profile")
	mustContain(t, body, "01J9VFLIGHT", "profile details disclosure")
}

// A window is a dimension of a board, and `?period=` has to reach the read layer
// rather than merely decorate the URL.
func TestBoardPassesTheWindowAndTheOffsetThrough(t *testing.T) {
	f := newFixture(t)

	f.get(t, "/boards/biggest_lithobrake_survived?period=weekly&offset=100")
	if f.read.lastPeriod != stats.PeriodWeekly {
		t.Errorf("period reaching Read = %q, want weekly", f.read.lastPeriod)
	}
	if f.read.lastOffset != 100 {
		t.Errorf("offset reaching Read = %d, want 100", f.read.lastOffset)
	}

	f.get(t, "/boards/biggest_lithobrake_survived")
	if f.read.lastPeriod != stats.PeriodAllTime {
		t.Errorf("default period = %q, want alltime", f.read.lastPeriod)
	}
}

// The comparison marks the best cell from the board's published direction. It
// must never be inferred: `fastest_to_*` ranks the smallest value first, and a
// guess would crown the slowest ascent on exactly the boards where it matters.
func TestCompareMarksTheBestCellByTheBoardsDirection(t *testing.T) {
	f := newFixture(t)
	body := f.get(t, "/compare?handles=demo_crasher,demo_ace,ghost").Body.String()

	// Descending board: 214 beats 62.
	mustContain(t, body, `class="value best" data-value="214"`, "compare")
	mustNotContain(t, body, `class="value best" data-value="62"`, "compare")

	// A handle nobody has is a column, not an omission — silently dropping it
	// would let a typo look like a defeat.
	mustContain(t, body, `data-handle="ghost"`, "compare")
	mustContain(t, body, "no such player", "compare")

	// A board only one of them is on shows a gap, not a zero.
	mustContain(t, body, `class="absent" title="not on this board"`, "compare")
}

// The comparison set is the URL, so `?add=` merges and redirects rather than
// rendering something the address bar disagrees with.
func TestCompareAddRedirectsToTheCanonicalURL(t *testing.T) {
	f := newFixture(t)
	w := f.get(t, "/compare?handles=demo_crasher&add=demo_ace")
	if w.Code != http.StatusFound {
		t.Fatalf("GET /compare?add= = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/compare?handles=demo_crasher%2Cdemo_ace" {
		t.Errorf("Location = %q", got)
	}
}

// The header search box is a plain GET form and the suggestion endpoint is an
// enhancement over it. Below the API's two-character floor the page must not ask
// at all — and must not show an error for not having asked.
func TestSearchSuggestIsADatastarPatchAndStaysQuietBelowTwoCharacters(t *testing.T) {
	f := newFixture(t)

	w := f.get(t, "/search/suggest?q=demo")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("suggest content-type = %q", ct)
	}
	body := w.Body.String()
	mustContain(t, body, "event: datastar-patch-elements", "suggest")
	mustContain(t, body, `id="search-suggest"`, "suggest")
	mustContain(t, body, "demo_ace", "suggest")

	// One character: an empty list, never an error.
	short := f.get(t, "/search/suggest?q=d").Body.String()
	mustContain(t, short, `id="search-suggest"`, "short suggest")
	mustNotContain(t, short, "demo_ace", "short suggest")
	mustNotContain(t, short, "at least 2 characters", "short suggest")
}

// §4.8's cache header belongs on the public pages — they are the same public
// facts the JSON API publishes — and must never appear on a page that depends on
// a session.
func TestCacheHeaders(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/", "/boards", "/boards/biggest_lithobrake_survived", "/p/demo_crasher", "/docs/privacy"} {
		if got := f.get(t, path).Header().Get("Cache-Control"); got != readapi.CacheControl {
			t.Errorf("GET %s Cache-Control = %q, want %q", path, got, readapi.CacheControl)
		}
	}
	if got := f.get(t, "/login").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET /login Cache-Control = %q, want no-store", got)
	}

	// 404s are never publicly cacheable: the catch-all matches every URL nobody
	// thought of, and each distinct miss would occupy its own CDN entry.
	if got := f.get(t, "/no/such/page").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("404 Cache-Control = %q, want no-store", got)
	}

	// The /docs index redirect is as cacheable as its fixed target.
	if w := f.get(t, "/docs"); w.Code != http.StatusFound {
		t.Errorf("GET /docs = %d, want 302", w.Code)
	} else if got := w.Header().Get("Cache-Control"); got != readapi.CacheControl {
		t.Errorf("GET /docs Cache-Control = %q, want %q", got, readapi.CacheControl)
	}

	// The SSE feed is the one public route that must not be cached at all.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, web.FeedPath, nil)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	f.mux.ServeHTTP(w, r.WithContext(ctx))
	if got := w.Header().Get("Cache-Control"); got == readapi.CacheControl {
		t.Errorf("the SSE feed carries the read-API cache header: %q", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("GET %s content-type = %q", web.FeedPath, got)
	}
}

// --- dashboard -----------------------------------------------------------------------

func TestDashboardRedirectsWithoutASession(t *testing.T) {
	f := newFixture(t)
	w := f.get(t, "/dashboard")
	if w.Code != http.StatusFound {
		t.Fatalf("GET /dashboard = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/login?next=%2Fdashboard" {
		t.Errorf("Location = %q", got)
	}
}

// A session whose account has been banned or purged since the cookie was minted
// must be cleared, not looped: without the Set-Cookie the browser bounces
// between /dashboard and /login forever.
func TestDashboardClearsASessionForAGoneAccount(t *testing.T) {
	f := newFixture(t)
	f.accounts.err = identity.ErrNoAccount

	w := f.do(t, f.signedIn(t, "/dashboard"))
	if w.Code != http.StatusFound {
		t.Fatalf("GET /dashboard = %d, want 302", w.Code)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == f.sessions.CookieName() && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("the session cookie was not cleared: %v", w.Result().Cookies())
	}
}

func TestDashboardRendersHandlesAndNeverTheUserKey(t *testing.T) {
	f := newFixture(t)
	const sub = "THIS-IS-THE-USER-KEY-AND-MUST-NOT-BE-RENDERED"
	f.accounts.data = identity.DashboardData{
		Me: identity.MeResponse{
			Sub: sub, IdP: "discord", Since: 1767225600000,
			Handles: 1, HandleQuota: 5, Issuances24h: 1, IssuanceQuota: 3, LicenseTTLDays: 180,
		},
		Handles: []identity.HandleView{
			{Handle: "e2e_whiskers", Since: 1767225600000, Credentials: []identity.CredentialView{
				{JKT: "live-jkt", LicenseJTI: "lic_1", IssuedAt: 1767225600000, ExpiresAt: 1782777600000},
				{JKT: "dead-jkt", LicenseJTI: "lic_0", IssuedAt: 1767225600000, Revoked: true, RevokedAt: 1767225600000},
			}},
			{Handle: "retired_one", Since: 1767225600000, Retired: true},
		},
	}

	w := f.do(t, f.signedIn(t, "/dashboard"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard = %d, want 200\n%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// The `sub` is the user_key. §5.11 keeps it out of logs; a page is worse.
	if strings.Contains(body, sub) {
		t.Error("the dashboard rendered the user_key")
	}
	mustContain(t, body, `data-handle="e2e_whiskers"`, "dashboard")
	mustContain(t, body, `data-jkt="live-jkt"`, "dashboard")
	// A revoked credential is still listed — §5.7 asks for the `revoked` field —
	// but under a different attribute, so "the jkt disappears from the list"
	// stays a true statement about a revoke.
	mustContain(t, body, `data-revoked-jkt="dead-jkt"`, "dashboard")
	if strings.Contains(body, `data-jkt="dead-jkt"`) {
		t.Error("a revoked credential is still listed as live")
	}
	mustContain(t, body, `class="handle handle-retired panel" data-handle="retired_one"`, "dashboard")
	mustContain(t, body, `id="wizard"`, "dashboard")
	mustContain(t, body, `src="/static/js/keygen.js"`, "dashboard")

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("dashboard Cache-Control = %q, want no-store", got)
	}
}

func TestDashboardDisablesTheWizardAtQuota(t *testing.T) {
	f := newFixture(t)
	f.accounts.data = identity.DashboardData{
		Me: identity.MeResponse{IdP: "github", Handles: 5, HandleQuota: 5, Issuances24h: 3, IssuanceQuota: 3},
	}
	body := f.do(t, f.signedIn(t, "/dashboard")).Body.String()
	mustContain(t, body, `data-can-claim="false"`, "wizard")
	mustContain(t, body, `data-can-issue="false"`, "wizard")
	mustContain(t, body, `<button id="wizard-submit" disabled>`, "wizard")
}

// --- the login-failure page ------------------------------------------------------------

// The contract WP3 established and site/e2e/auth.spec.ts asserts.
func TestAuthErrorKeepsItsDOMContract(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/discord/callback", nil)

	if !f.srv.AuthError(w, r, http.StatusForbidden, "account_too_new", "this account is 3 days old") {
		t.Fatal("AuthError declined to render")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="auth-error" data-error="account_too_new"`,
		`id="auth-error-code"`,
		`id="auth-error-detail"`,
		`id="auth-error-retry"`,
		"account_too_new",
		"this account is 3 days old",
	} {
		mustContain(t, body, want, "auth error")
	}
}

// --- the SSE feed --------------------------------------------------------------------

// Priming, then one live row, then the cap.
func TestFeedPrimesThenStreams(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	for i := 1; i <= 3; i++ {
		if _, err := f.projections.InsertFeed(ctx, nil, store.FeedRow{
			At: 1767225600000, Handle: "demo_ace", Type: "vehicle.rud",
			Summary: "demo_ace lost a vehicle " + string(rune('a'+i-1)),
		}); err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, web.FeedPath, nil).WithContext(reqCtx)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{}, 64)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.mux.ServeHTTP(w, r)
	}()

	// The prime.
	waitFor(t, w, func(body string) bool { return strings.Contains(body, `<ul id="feed"`) })
	if got := w.body(); !strings.Contains(got, "event: datastar-patch-elements") {
		t.Fatalf("the prime is not a datastar patch:\n%s", got)
	}
	// Primed rows are not "arrived": the flash is scoped to data-arrived, and a
	// (re)connect that animated thirty old rows would claim they were new.
	mustNotContain(t, w.body(), "data-arrived", "prime")

	// A live row. Publish only reaches a subscriber that already exists, which
	// is why the handler subscribes before it primes.
	f.broadcaster.Publish([]store.FeedRow{{
		ID: 99, At: 1767225600001, Handle: "demo_crasher", Type: "vehicle.impact",
		Summary: "demo_crasher lithobraked at 214 m/s on duna — and survived",
	}})
	waitFor(t, w, func(body string) bool { return strings.Contains(body, "lithobraked at 214") })

	body := w.body()
	mustContain(t, body, "data: selector #feed", "live patch")
	mustContain(t, body, "data: mode prepend", "live patch")
	mustContain(t, body, `id="feed-item-99"`, "live patch")
	// Only the live per-row patch is marked arrived, and the summary's leading
	// handle is rendered as a profile link with the rest left as prose.
	mustContain(t, body, "data-arrived", "live patch")
	mustContain(t, body, `<a href="/p/demo_crasher">demo_crasher</a> lithobraked at 214`, "live patch")

	// A replayed row (id at or below the high-water mark) must not be patched in
	// twice: the prime and the live channel overlap by design.
	before := strings.Count(w.body(), `id="feed-item-99"`)
	f.broadcaster.Publish([]store.FeedRow{{ID: 99, At: 1, Handle: "demo_crasher", Type: "vehicle.impact", Summary: "duplicate"}})
	f.broadcaster.Publish([]store.FeedRow{{ID: 100, At: 2, Handle: "demo_ace", Type: "vehicle.rud", Summary: "a later line"}})
	waitFor(t, w, func(b string) bool { return strings.Contains(b, "a later line") })
	if after := strings.Count(w.body(), `id="feed-item-99"`); after != before {
		t.Errorf("a replayed feed row was patched in again (%d → %d)", before, after)
	}
	if strings.Contains(w.body(), "duplicate") {
		t.Error("a replayed feed row reached the page")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the SSE handler did not return after the client disconnected")
	}
	if f.broadcaster.Clients() != 0 {
		t.Errorf("the subscriber was not cancelled: %d left", f.broadcaster.Clients())
	}
}

func TestFeedSurvivesAnEmptyFeedTable(t *testing.T) {
	f := newFixture(t)
	reqCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, web.FeedPath, nil).WithContext(reqCtx)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{}, 8)}

	go f.mux.ServeHTTP(w, r)
	waitFor(t, w, func(b string) bool { return strings.Contains(b, `class="feed-empty"`) })
}

// --- helpers ---------------------------------------------------------------------------

// flushRecorder is a ResponseRecorder that is safe to read while the handler is
// still writing, and that signals every flush.
type flushRecorder struct {
	*httptest.ResponseRecorder
	mu    sync.Mutex
	wrote chan struct{}
}

func (f *flushRecorder) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.ResponseRecorder.Write(b)
	select {
	case f.wrote <- struct{}{}:
	default:
	}
	return n, err
}

func (f *flushRecorder) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ResponseRecorder.Flush()
}

func (f *flushRecorder) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Body.String()
}

func waitFor(t *testing.T, w *flushRecorder, ok func(string) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if ok(w.body()) {
			return
		}
		select {
		case <-w.wrote:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for the stream; got:\n%s", w.body())
		}
	}
}

// --- the raw event pages ---------------------------------------------------------------

// The global page renders rows with their handles, the chip row, the handle
// filter notice, and the pager — and carries the public cache header like every
// other public page.
func TestGlobalEventsPageRenders(t *testing.T) {
	f := newFixture(t)

	body := f.get(t, "/events").Body.String()
	mustContain(t, body, `id="events-log"`, "events page")
	mustContain(t, body, `id="events-body" data-source="ssr"`, "events page")
	mustContain(t, body, `id="event-row-41824"`, "events page")
	mustContain(t, body, `data-seq="41824"`, "events page")
	// Every row names its player as a profile link.
	mustContain(t, body, `<a href="/p/demo_crasher">demo_crasher</a>`, "events page")
	mustContain(t, body, `<a href="/p/demo_ace">demo_ace</a>`, "events page")
	// The received column is a machine-readable instant plus the display text.
	mustContain(t, body, `<time datetime="2026-01-01T00:00:00Z">2026-01-01 00:00 UTC</time>`, "events page")
	// The career clock renders through the unit table, raw figure in the title.
	mustContain(t, body, `title="1832.5 s"`, "events page")
	mustContain(t, body, "30m 32s", "events page")
	// Chips: the taxonomy union, with the page's own types present.
	mustContain(t, body, `id="events-types"`, "events page")
	mustContain(t, body, `data-type="vehicle.orbit"`, "events page")
	// Pager: newest is disabled on page one, older links by cursor.
	mustContain(t, body, `<span id="events-newest" aria-disabled="true">`, "events page")
	mustContain(t, body, `href="/events?before=41822" id="events-older"`, "events page")
	// The nav names the page.
	mustContain(t, body, `id="nav-events" aria-current="page"`, "events page")

	if got := f.get(t, "/events").Header().Get("Cache-Control"); got != readapi.CacheControl {
		t.Errorf("GET /events Cache-Control = %q, want %q", got, readapi.CacheControl)
	}
}

// `?handle=` narrows the global page to one player through the same PlayerEvents
// call the per-handle page uses — same rows, same 404 for unknown, retired and
// banned — and renders as a notice with a way out.
func TestGlobalEventsHandleFilter(t *testing.T) {
	f := newFixture(t)

	body := f.get(t, "/events?handle=demo_crasher").Body.String()
	mustContain(t, body, `id="events-handle-filter"`, "handle filter")
	mustContain(t, body, `data-handle="demo_crasher"`, "handle filter")
	mustContain(t, body, `id="events-handle-clear" href="/events"`, "handle filter")
	// The per-player envelope names the handle once; the page stamps it onto
	// every row so the shared partial still renders the link.
	mustContain(t, body, `<a href="/p/demo_crasher">demo_crasher</a>`, "handle filter")
	// The chip URLs carry the handle filter through.
	mustContain(t, body, `href="/events?handle=demo_crasher&amp;type=vehicle.impact"`, "handle filter")

	w := f.get(t, "/events?handle=nobody")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /events?handle=nobody = %d, want 404", w.Code)
	}

	if w := f.get(t, "/events?before=not-a-cursor"); w.Code != http.StatusNotFound {
		t.Errorf("GET /events?before=not-a-cursor = %d, want 404", w.Code)
	}
}

// eventRowRx pulls every rendered log row out of a page, disclosure included.
var eventRowRx = regexp.MustCompile(`(?s)<tr class="event-row".*?</tr>`)

// The per-handle page and the global page render rows through one partial, so
// the same event is byte-identical on both — the feed-item discipline. This is
// the assertion that catches the two views drifting apart cell by cell.
func TestEventsPagesShareOneRowPartial(t *testing.T) {
	f := newFixture(t)

	global := eventRowRx.FindAllString(f.get(t, "/events").Body.String(), -1)
	perHandle := eventRowRx.FindAllString(f.get(t, "/p/demo_crasher/events").Body.String(), -1)

	if len(global) == 0 || len(perHandle) == 0 {
		t.Fatalf("no rows extracted: global %d, per-handle %d", len(global), len(perHandle))
	}
	// Seq 41824 is on both pages; the two renderings must be identical bytes.
	find := func(rows []string, id string) string {
		for _, row := range rows {
			if strings.Contains(row, id) {
				return row
			}
		}
		return ""
	}
	g, p := find(global, `id="event-row-41824"`), find(perHandle, `id="event-row-41824"`)
	switch {
	case g == "" || p == "":
		t.Fatalf("seq 41824 missing: global %q, per-handle %q", g, p)
	case g != p:
		t.Errorf("the two pages render the same row differently:\nglobal:     %s\nper-handle: %s", g, p)
	}
}

// The live tail exists only on page one: a page read at a cursor is historical,
// and its markup must not open a stream that prepends today's rows above last
// week's.
func TestEventsLiveTailOnlyOnPageOne(t *testing.T) {
	f := newFixture(t)

	one := f.get(t, "/events").Body.String()
	mustContain(t, one, `id="events-tail"`, "page one")
	mustContain(t, one, `data-init="@get('/v1/events/sse', {requestCancellation: 'cleanup'})"`, "page one")
	mustContain(t, one, `id="events-live"`, "page one")

	// The tail carries the page's own filter into the stream URL.
	filtered := f.get(t, "/events?type=vehicle.impact&handle=demo_crasher").Body.String()
	mustContain(t, filtered,
		`data-init="@get('/v1/events/sse?handle=demo_crasher&amp;type=vehicle.impact', {requestCancellation: 'cleanup'})"`,
		"filtered page")

	deep := f.get(t, "/events?before=41822").Body.String()
	mustNotContain(t, deep, `id="events-tail"`, "deep page")
	mustNotContain(t, deep, `id="events-live"`, "deep page")

	// The per-handle page tails too, scoped to its handle.
	perHandle := f.get(t, "/p/demo_crasher/events").Body.String()
	mustContain(t, perHandle,
		`data-init="@get('/v1/events/sse?handle=demo_crasher', {requestCancellation: 'cleanup'})"`,
		"per-handle page")
	perHandleDeep := f.get(t, "/p/demo_crasher/events?before=41822").Body.String()
	mustNotContain(t, perHandleDeep, `id="events-tail"`, "deep per-handle page")
}

// --- the events SSE tail ---------------------------------------------------------------

// Subscribe-then-prime, one live row, the arrival mark, the seq dedupe and the
// DOM cap — handleFeed's contract, on the raw log.
func TestEventsSSEPrimesThenStreams(t *testing.T) {
	f := newFixture(t)

	reqCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, web.EventsSSEPath, nil).WithContext(reqCtx)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{}, 64)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.mux.ServeHTTP(w, r)
	}()

	// The prime replaces the whole tbody, marked as the stream's copy.
	waitFor(t, w, func(body string) bool { return strings.Contains(body, `id="events-body" data-source="sse"`) })
	mustContain(t, w.body(), "event: datastar-patch-elements", "prime")
	mustNotContain(t, w.body(), "data-arrived", "prime")

	// A live row: through the seam, above the high-water mark.
	f.raw.Publish([]store.StoredEvent{{
		Seq: 41900, PlayerID: 2, RecvTime: 1767225700000,
		Event: store.Event{Type: "vehicle.rud", Ver: 1, Payload: json.RawMessage(`{"cause":"tumble"}`)},
	}})
	waitFor(t, w, func(body string) bool { return strings.Contains(body, `id="event-row-41900"`) })

	body := w.body()
	mustContain(t, body, "data: selector #events-body", "live patch")
	mustContain(t, body, "data: mode prepend", "live patch")
	mustContain(t, body, "data-arrived", "live patch")
	mustContain(t, body, `<a href="/p/demo_ace">demo_ace</a>`, "live patch")

	// A replayed seq (at or below the high-water mark) is never patched twice,
	// and a handle-less player's row never reaches the page at all.
	before := strings.Count(w.body(), `id="event-row-41900"`)
	f.raw.Publish([]store.StoredEvent{
		{Seq: 41900, PlayerID: 2, Event: store.Event{Type: "vehicle.rud", Ver: 1, Payload: json.RawMessage(`{"cause":"duplicate"}`)}},
		{Seq: 41901, PlayerID: 99, Event: store.Event{Type: "vehicle.rud", Ver: 1, Payload: json.RawMessage(`{"cause":"nameless"}`)}},
		{Seq: 41902, PlayerID: 1, Event: store.Event{Type: "vehicle.orbit", Ver: 1, Payload: json.RawMessage(`{"body":"sentinel"}`)}},
	})
	waitFor(t, w, func(b string) bool { return strings.Contains(b, "sentinel") })
	if after := strings.Count(w.body(), `id="event-row-41900"`); after != before {
		t.Errorf("a replayed row was patched in again (%d → %d)", before, after)
	}
	mustNotContain(t, w.body(), "duplicate", "replayed row")
	mustNotContain(t, w.body(), "nameless", "handle-less row")

	// The DOM cap: push the page past EventRows and the oldest row is removed
	// by the id the row partial stamped.
	burst := make([]store.StoredEvent, 0, web.EventRows)
	for i := range web.EventRows {
		burst = append(burst, store.StoredEvent{
			Seq: int64(42000 + i), PlayerID: 1,
			Event: store.Event{Type: "vehicle.rud", Ver: 1, Payload: json.RawMessage(`{}`)},
		})
	}
	f.raw.Publish(burst)
	waitFor(t, w, func(b string) bool { return strings.Contains(b, "data: mode remove") })
	mustContain(t, w.body(), "data: selector #event-row-41823", "trim")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the SSE handler did not return after the client disconnected")
	}
	if f.raw.Clients() != 0 {
		t.Errorf("the raw subscriber was not cancelled: %d left", f.raw.Clients())
	}
}

// `?type=` and `?handle=` scope the tail to the page's filter: the prime reads
// the filtered page and live rows are matched per subscriber.
func TestEventsSSEHonoursTheFilters(t *testing.T) {
	f := newFixture(t)

	reqCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet,
		web.EventsSSEPath+"?type=vehicle.impact&handle=demo_crasher", nil).WithContext(reqCtx)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{}, 64)}
	go f.mux.ServeHTTP(w, r)

	// The prime is the filtered page: demo_crasher's impact, nobody else's rows.
	waitFor(t, w, func(body string) bool { return strings.Contains(body, `data-source="sse"`) })
	mustContain(t, w.body(), `id="event-row-41824"`, "filtered prime")
	mustNotContain(t, w.body(), `id="event-row-41823"`, "filtered prime")

	// Wrong type, wrong player, then a match — only the match is patched in.
	f.raw.Publish([]store.StoredEvent{
		{Seq: 41903, PlayerID: 1, Event: store.Event{Type: "vehicle.rud", Ver: 1, Payload: json.RawMessage(`{"cause":"wrong_type"}`)}},
		{Seq: 41904, PlayerID: 2, Event: store.Event{Type: "vehicle.impact", Ver: 1, Payload: json.RawMessage(`{"body":"wrong_player"}`)}},
		{Seq: 41905, PlayerID: 1, Event: store.Event{Type: "vehicle.impact", Ver: 1, Payload: json.RawMessage(`{"body":"match"}`)}},
	})
	waitFor(t, w, func(b string) bool { return strings.Contains(b, "match") })
	mustNotContain(t, w.body(), "wrong_type", "type filter")
	mustNotContain(t, w.body(), "wrong_player", "handle filter")
	mustContain(t, w.body(), "data-arrived", "filtered live patch")
}

// The events tail is the other public route that must never be cached.
func TestEventsSSEIsNeverCached(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, web.EventsSSEPath, nil)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	f.mux.ServeHTTP(w, r.WithContext(ctx))
	if got := w.Header().Get("Cache-Control"); got == readapi.CacheControl {
		t.Errorf("the events SSE tail carries the read-API cache header: %q", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("GET %s content-type = %q", web.EventsSSEPath, got)
	}
}
