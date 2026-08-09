package readapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// fixture is a read API over hand-written projections: it writes player_stat
// rows directly rather than folding events, so a paging or filtering bug cannot
// hide behind a fold bug.
type fixture struct {
	t      *testing.T
	events *store.Events
	proj   *store.Projections
	dir    *directory.Directory
	mux    *http.ServeMux

	byHandle map[string]int64
	// eventN keeps hand-written events' derived ids distinct from each other.
	eventN int
}

// live is the smallest thing satisfying readapi.Projections. In catlogd this is
// *projector.Live, which additionally holds the rebuild swap's read lock.
type live struct{ p *store.Projections }

func (l live) With(fn func(*store.Projections) error) error { return fn(l.p) }

func (l live) WriteGen() int64 { return l.p.WriteGen() }

func newFixture(t *testing.T, opts ...func(*readapi.Deps)) *fixture {
	t.Helper()
	f := &fixture{
		t:        t,
		events:   testutil.MemEvents(t),
		proj:     testutil.MemProjections(t),
		byHandle: map[string]int64{},
	}
	f.dir = directory.New(f.events)
	if err := f.dir.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps := readapi.Deps{
		Projections: live{f.proj},
		Events:      f.events,
		Directory:   f.dir,
		Log:         testutil.DiscardLogger(),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	srv, err := readapi.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	f.mux = http.NewServeMux()
	srv.Register(f.mux)
	return f
}

// player creates a player, claims a handle and reloads the directory.
func (f *fixture) player(handle string) int64 {
	f.t.Helper()
	keys := testutil.Keys(f.t)
	id, err := f.events.EnsurePlayer(f.t.Context(), nil, keys.UserKey("dev", handle), "dev", 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.events.ClaimHandle(f.t.Context(), id, handle, 7_000_000+id); err != nil {
		f.t.Fatal(err)
	}
	if err := f.dir.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	f.byHandle[handle] = id
	return id
}

func (f *fixture) ban(handle string) {
	f.t.Helper()
	if err := f.events.SetBan(f.t.Context(), nil, f.byHandle[handle], 9_999_999, "cheating"); err != nil {
		f.t.Fatal(err)
	}
	if err := f.dir.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
}

// event writes one event row so a stat's updated_seq has a real recv_time to
// resolve against — the §4.8 `updated` field.
func (f *fixture) event(playerID int64, n int) int64 {
	f.t.Helper()
	var id ids.ID
	id[0] = byte(playerID)
	id[15] = byte(n)
	if _, _, err := f.events.InsertEvents(f.t.Context(), nil, playerID, []store.Event{{
		ID: id, SessionID: id, Type: "vehicle.impact", Ver: 1,
		WallTime: 1, Payload: json.RawMessage(`{}`),
	}}); err != nil {
		f.t.Fatal(err)
	}
	var seq int64
	if err := f.events.Reader().QueryRowContext(f.t.Context(),
		`SELECT seq FROM event WHERE player_id = ? ORDER BY seq DESC LIMIT 1`, playerID).Scan(&seq); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.events.Writer().ExecContext(f.t.Context(),
		`UPDATE event SET recv_time = ? WHERE seq = ?`, 1_800_000_000_000+seq, seq); err != nil {
		f.t.Fatal(err)
	}
	return seq
}

// projWrite writes to projections.db the way the projector does — through a
// write transaction, not the bare handle. The distinction is load-bearing: the
// board census cache is keyed on the projections write generation, which only a
// committed transaction moves, so a fixture that wrote around it would be
// invisible to every read that follows.
func (f *fixture) projWrite(query string, args ...any) {
	f.t.Helper()
	if err := f.proj.WithWriteTx(f.t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.t.Context(), query, args...)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// stat writes a player_stat row at a real event seq.
func (f *fixture) stat(playerID int64, stat string, value float64, n int) {
	f.t.Helper()
	seq := f.event(playerID, n)
	f.projWrite(`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, ?, ?)`,
		playerID, stat, value, `{"body":"duna"}`, seq)
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	// §4.8: every public read response carries this header, verbatim.
	if got := rec.Header().Get("Cache-Control"); got != readapi.CacheControl {
		f.t.Errorf("GET %s Cache-Control = %q, want %q", path, got, readapi.CacheControl)
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body)
	}
	return out
}

// --- /v1/leaderboards --------------------------------------------------------

func TestBoardsListsEveryBoardEvenTheEmptyOnes(t *testing.T) {
	f := newFixture(t)
	p := f.player("whiskers")
	f.stat(p, stats.StatKittenTumbles, 12, 1)

	rec := f.get("/v1/leaderboards")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := decode[readapi.BoardsResponse](t, rec)
	if len(got.Boards) != len(stats.FixedBoards()) {
		t.Fatalf("%d boards, want %d — an empty board is still a board", len(got.Boards), len(stats.FixedBoards()))
	}
	// Four boards carry no unit on purpose — an eccentricity and three counts of
	// a thing the title already names — so the index must publish the empty
	// string rather than invent a label. stats.FixedBoards owns that list.
	unitless := map[string]bool{
		stats.StatRoundestOrbit: true,
		stats.StatMostParts:     true,
		stats.StatMostStages:    true,
		stats.StatBiggestStack:  true,
	}
	for _, b := range got.Boards {
		if b.Title == "" {
			t.Errorf("board %q has no title", b.Stat)
		}
		if b.Unit == "" && !unitless[b.Stat] {
			t.Errorf("board %q has no unit", b.Stat)
		}
		switch b.Stat {
		case stats.StatKittenTumbles:
			if b.Count != 1 {
				t.Errorf("kitten_tumbles count = %d, want 1", b.Count)
			}
		default:
			if b.Count != 0 {
				t.Errorf("board %q count = %d, want 0", b.Stat, b.Count)
			}
		}
	}
}

// A board whose key came out of the event stream appears in the index the moment
// a second player is on it, and is served — but not listed — before that.
func TestBoardsListsADataDrivenBoardOnceEnoughPlayersAreOnIt(t *testing.T) {
	f := newFixture(t)
	one := f.player("first_there")
	f.stat(one, "fastest_to_zephyria", 900, 1)

	listed := func() []string {
		f.t.Helper()
		out := []string{}
		for _, b := range decode[readapi.BoardsResponse](t, f.get("/v1/leaderboards")).Boards {
			out = append(out, b.Stat)
		}
		return out
	}

	if slices.Contains(listed(), "fastest_to_zephyria") {
		t.Error("a board with one player was listed; the threshold is the whole mitigation")
	}
	// Served all the same: the player's own profile row has to link somewhere.
	page := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/fastest_to_zephyria"))
	if page.Title != "Fastest to Zephyria" || !page.Ascending || len(page.Rows) != 1 {
		t.Errorf("board page = %+v, want one row on an ascending board titled from the key", page)
	}
	if rec := f.get("/v1/leaderboards/fastest_to_nobody_went_there"); rec.Code != http.StatusNotFound {
		t.Errorf("a family key nobody is on = %d, want 404", rec.Code)
	}

	two := f.player("second_there")
	f.stat(two, "fastest_to_zephyria", 400, 2)
	all := listed()
	if !slices.Contains(all, "fastest_to_zephyria") {
		t.Errorf("boards = %v, want fastest_to_zephyria listed once two players are on it", all)
	}
	// And it lands under the fixed career-time board it belongs with.
	if i, j := slices.Index(all, stats.StatFastestToOrbit), slices.Index(all, "fastest_to_zephyria"); j != i+1 {
		t.Errorf("boards = %v, want fastest_to_zephyria right after fastest_to_orbit", all)
	}

	// The profile shows the row either way, and titles it from the key.
	profile := decode[readapi.PlayerResponse](t, f.get("/v1/players/first_there"))
	if len(profile.Stats) != 1 || profile.Stats[0].Title != "Fastest to Zephyria" {
		t.Errorf("profile = %+v, want the row titled from the key", profile.Stats)
	}
}

// --- /v1/leaderboards/{stat} -------------------------------------------------

func TestBoardRanksAndTieBreak(t *testing.T) {
	f := newFixture(t)
	first := f.player("first_to_ten")
	second := f.player("second_to_ten")
	top := f.player("top_dog")

	f.stat(first, stats.StatStagings, 10, 1)  // earlier seq
	f.stat(second, stats.StatStagings, 10, 2) // same value, later seq
	f.stat(top, stats.StatStagings, 40, 3)

	got := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings"))
	if len(got.Rows) != 3 {
		t.Fatalf("%d rows, want 3", len(got.Rows))
	}
	wantOrder := []string{"top_dog", "first_to_ten", "second_to_ten"}
	for i, want := range wantOrder {
		if got.Rows[i].Handle != want {
			t.Errorf("row %d = %q, want %q (ties keep the earliest updated_seq)", i, got.Rows[i].Handle, want)
		}
		if got.Rows[i].Rank != i+1 {
			t.Errorf("row %d rank = %d, want %d", i, got.Rows[i].Rank, i+1)
		}
		if got.Rows[i].Updated == 0 {
			t.Errorf("row %d has no `updated` timestamp", i)
		}
	}
	if got.Title == "" || got.Unit == "" {
		t.Errorf("board metadata missing from the response: %+v", got)
	}
}

func TestBoardPagination(t *testing.T) {
	f := newFixture(t)
	for i := range 25 {
		p := f.player(fmt.Sprintf("player_%02d", i))
		f.stat(p, stats.StatStagings, float64(100-i), i)
	}

	page1 := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?limit=10"))
	if len(page1.Rows) != 10 || page1.Rows[0].Rank != 1 || page1.Rows[9].Rank != 10 {
		t.Fatalf("first page: %d rows, ranks %d..%d", len(page1.Rows), page1.Rows[0].Rank, page1.Rows[9].Rank)
	}
	if page1.Rows[0].Handle != "player_00" {
		t.Errorf("first row = %q, want player_00 (highest value)", page1.Rows[0].Handle)
	}

	page3 := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?limit=10&offset=20"))
	if len(page3.Rows) != 5 {
		t.Fatalf("last page has %d rows, want 5", len(page3.Rows))
	}
	if page3.Rows[0].Rank != 21 || page3.Rows[0].Handle != "player_20" {
		t.Errorf("last page starts at rank %d (%s), want 21 (player_20)", page3.Rows[0].Rank, page3.Rows[0].Handle)
	}

	// Past the end is an empty page, not an error.
	beyond := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?limit=10&offset=500"))
	if len(beyond.Rows) != 0 {
		t.Errorf("offset past the end returned %d rows", len(beyond.Rows))
	}
}

func TestLimitIsClampedToTheSection48Ceiling(t *testing.T) {
	f := newFixture(t)
	p := f.player("whiskers")
	f.stat(p, stats.StatStagings, 1, 1)

	for _, tc := range []struct {
		query, name string
		want        int
	}{
		{"?limit=500", "over the ceiling", readapi.MaxLimit},
		{"?limit=0", "zero", 1},
		{"?limit=-4", "negative", 1},
		{"", "absent", readapi.DefaultLimit},
	} {
		got := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings"+tc.query))
		if got.Limit != tc.want {
			t.Errorf("limit %s: echoed %d, want %d", tc.name, got.Limit, tc.want)
		}
	}

	rec := f.get("/v1/leaderboards/stagings?limit=banana")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a non-numeric limit returned %d, want 400", rec.Code)
	}
}

func TestUnknownBoardIs404(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/v1/leaderboards/not_a_board")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != "not_found" {
		t.Errorf("body = %s", rec.Body)
	}
}

// --- banned players ----------------------------------------------------------

func TestBannedPlayersAreFilteredFromBoardsAndRanks(t *testing.T) {
	f := newFixture(t)
	cheat := f.player("cheater")
	honest := f.player("honest")
	third := f.player("third")

	f.stat(cheat, stats.StatStagings, 999, 1)
	f.stat(honest, stats.StatStagings, 50, 2)
	f.stat(third, stats.StatStagings, 10, 3)

	before := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings"))
	if len(before.Rows) != 3 || before.Rows[0].Handle != "cheater" {
		t.Fatalf("before the ban: %+v", before.Rows)
	}
	beforeProfile := decode[readapi.PlayerResponse](t, f.get("/v1/players/honest"))
	if beforeProfile.Stats[0].Rank != 2 {
		t.Fatalf("honest starts at rank %d, want 2", beforeProfile.Stats[0].Rank)
	}

	f.ban("cheater")

	after := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings"))
	if len(after.Rows) != 2 {
		t.Fatalf("%d rows after the ban, want 2", len(after.Rows))
	}
	if after.Rows[0].Handle != "honest" || after.Rows[0].Rank != 1 {
		t.Errorf("top row = %s at rank %d, want honest at 1", after.Rows[0].Handle, after.Rows[0].Rank)
	}
	if after.Rows[1].Rank != 2 {
		t.Errorf("second row rank = %d, want 2: a ban must close the gap it leaves", after.Rows[1].Rank)
	}

	// The profile rank has to agree with the board it appears on, which is why
	// the rank subtracts the banned players ahead rather than counting rows.
	profile := decode[readapi.PlayerResponse](t, f.get("/v1/players/honest"))
	if profile.Stats[0].Rank != 1 {
		t.Errorf("honest's profile rank = %d, want 1", profile.Stats[0].Rank)
	}

	if rec := f.get("/v1/players/cheater"); rec.Code != http.StatusNotFound {
		t.Errorf("a banned player's profile returned %d, want 404", rec.Code)
	}
}

// countingLive wraps the projections handle and counts the queries that reach
// it, which is how the census cache's behavior is observable.
type countingLive struct {
	p     *store.Projections
	calls int
}

func (l *countingLive) With(fn func(*store.Projections) error) error {
	l.calls++
	return fn(l.p)
}

func (l *countingLive) WriteGen() int64 { return l.p.WriteGen() }

// TestStatCountsAreCached pins the board-census cache: repeated index reads are
// served from memory, and a write to projections.db invalidates it — which is
// what keeps the unit above safe for every test that writes a row and re-reads.
//
// The key is the projections write generation, not the head of the event log.
// Those come apart in the window that matters: ingest stops, the head stops
// moving, and the fold is still running. Keyed on the head, a read landing
// there cached a half-folded census and served it for the whole TTL — a board
// index that under-reports for ten seconds right after a load run.
func TestStatCountsAreCached(t *testing.T) {
	f := newFixture(t)
	whiskers := f.player("whiskers")
	f.stat(whiskers, stats.StatStagings, 5, 1)

	counting := &countingLive{p: f.proj}
	srv, err := readapi.New(readapi.Deps{
		Projections: counting,
		Events:      f.events,
		Directory:   f.dir,
		Log:         testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := srv.BoardList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after := counting.calls
	if after == 0 {
		t.Fatal("the first index read never queried the projections")
	}

	second, err := srv.BoardList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counting.calls != after {
		t.Errorf("a repeated index read queried again (%d → %d calls); the census must be cached", after, counting.calls)
	}
	if len(second.Boards) != len(first.Boards) {
		t.Errorf("cached index = %d boards, first read = %d", len(second.Boards), len(first.Boards))
	}

	// An event that has not been folded yet changes no board, so it does not
	// invalidate anything: the census is a statement about projections.db.
	f.event(whiskers, 2)
	if _, err := srv.BoardList(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counting.calls != after {
		t.Errorf("an unfolded event recounted the census (%d → %d calls); "+
			"the census counts projections, not the log", after, counting.calls)
	}

	// The fold landing is what must drop the cache, TTL or no TTL.
	f.stat(whiskers, stats.StatOrbitsAchieved, 3, 3)
	third, err := srv.BoardList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counting.calls == after {
		t.Error("the index read after a fold was served stale; a projections write must invalidate the census")
	}
	count := func(r readapi.BoardsResponse, stat string) int64 {
		for _, b := range r.Boards {
			if b.Stat == stat {
				return b.Count
			}
		}
		return -1
	}
	if before, now := count(first, stats.StatOrbitsAchieved), count(third, stats.StatOrbitsAchieved); now != before+1 {
		t.Errorf("%s count = %d after the fold, want %d — the recount must be fresh, not just a recount",
			stats.StatOrbitsAchieved, now, before+1)
	}
}

// TestSmallPageSurvivesABannedFirstFetch pins the sized first read of
// visibleRows: a small limit reads a small first page, and when that page turns
// out to be all banned players the scan escalates until the request is honest.
func TestSmallPageSurvivesABannedFirstFetch(t *testing.T) {
	f := newFixture(t)
	// 25 banned players hold the top of the board — more than a limit=2
	// request's slacked first page — with two honest players below them.
	for i := range 27 {
		handle := fmt.Sprintf("player_%02d", i)
		p := f.player(handle)
		f.stat(p, stats.StatStagings, float64(100-i), i)
		if i < 25 {
			f.ban(handle)
		}
	}

	got := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?limit=2"))
	if len(got.Rows) != 2 {
		t.Fatalf("%d rows, want 2 despite the whole first fetch being banned", len(got.Rows))
	}
	if got.Rows[0].Handle != "player_25" || got.Rows[0].Rank != 1 {
		t.Errorf("top row = %q rank %d, want player_25 at 1", got.Rows[0].Handle, got.Rows[0].Rank)
	}
	if got.Rows[1].Handle != "player_26" || got.Rows[1].Rank != 2 {
		t.Errorf("second row = %q rank %d, want player_26 at 2", got.Rows[1].Handle, got.Rows[1].Rank)
	}
}

func TestBannedPlayersDoNotShortenAPage(t *testing.T) {
	// The filter runs in Go, so a page has to over-fetch until it is full.
	f := newFixture(t)
	for i := range 30 {
		handle := fmt.Sprintf("player_%02d", i)
		p := f.player(handle)
		f.stat(p, stats.StatStagings, float64(100-i), i)
		if i%2 == 0 {
			f.ban(handle)
		}
	}

	got := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?limit=10"))
	if len(got.Rows) != 10 {
		t.Fatalf("%d rows, want a full page of 10 despite half the board being banned", len(got.Rows))
	}
	for i, row := range got.Rows {
		if row.Rank != i+1 {
			t.Errorf("row %d rank = %d, want %d", i, row.Rank, i+1)
		}
		if row.Handle != fmt.Sprintf("player_%02d", 2*i+1) {
			t.Errorf("row %d = %q, want player_%02d", i, row.Handle, 2*i+1)
		}
	}
}

// --- /v1/players/{handle} ----------------------------------------------------

func TestPlayerProfile(t *testing.T) {
	f := newFixture(t)
	p := f.player("Whiskers_Prime")
	f.stat(p, stats.StatBiggestLithobrakeSurvived, 214, 1)
	f.stat(p, stats.StatRUDTotal, 6, 2)

	rec := f.get("/v1/players/whiskers_prime") // §4.7: lookup is case-insensitive
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[readapi.PlayerResponse](t, rec)
	if got.Handle != "Whiskers_Prime" {
		t.Errorf("handle = %q, want the display casing", got.Handle)
	}
	if got.Since == 0 {
		t.Error("`since` is missing")
	}
	if len(got.Stats) != 2 {
		t.Fatalf("%d stats, want 2: %+v", len(got.Stats), got.Stats)
	}
	for _, s := range got.Stats {
		if s.Rank != 1 || s.Title == "" || s.Unit == "" || s.Updated == 0 {
			t.Errorf("stat %+v is incomplete", s)
		}
		// A profile page shows a rank next to a value, so it needs the board's
		// direction and its population without fetching the board index too.
		if s.Players != 1 {
			t.Errorf("stat %s players = %d, want 1", s.Stat, s.Players)
		}
		if board, _ := stats.Describe(s.Stat); s.Ascending != board.Ascending {
			t.Errorf("stat %s ascending = %v, want %v", s.Stat, s.Ascending, board.Ascending)
		}
		if string(s.Context) != `{"body":"duna"}` {
			t.Errorf("stat %s context = %s", s.Stat, s.Context)
		}
	}
}

func TestUnknownPlayerIs404(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/v1/players/nobody_at_all")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestProfileDropsStatsThisBuildNoLongerPublishes(t *testing.T) {
	// A board removed between releases leaves rows in projections.db until the
	// next rebuild. The profile must not offer a link the board endpoint 404s.
	f := newFixture(t)
	p := f.player("whiskers")
	f.stat(p, "retired_board", 3, 1)
	f.stat(p, stats.StatRUDTotal, 1, 2)

	got := decode[readapi.PlayerResponse](t, f.get("/v1/players/whiskers"))
	if len(got.Stats) != 1 || got.Stats[0].Stat != stats.StatRUDTotal {
		t.Errorf("stats = %+v, want only the boards this build declares", got.Stats)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := readapi.New(readapi.Deps{}); err == nil {
		t.Error("New accepted empty deps")
	}
}
