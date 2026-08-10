package readapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func seedScopedCareer(
	t *testing.T, f *fixture, playerID int64, career, system string, ordinal int64, rewound bool, seq int,
) {
	t.Helper()
	f.projWrite(`INSERT INTO career
		(player_id, career, max_sim_t, rewound, first_seq, ordinal, last_seq, system)
		VALUES (?, ?, 100, ?, ?, ?, ?, ?)`, playerID, career, rewound, seq, ordinal, seq, system)
}

func seedCareerBoardRow(
	t *testing.T, f *fixture, playerID int64, career, system, stat string, value float64, n int,
) {
	t.Helper()
	seq := f.event(playerID, n)
	context := `{"career":"` + career + `","body":"earth"}`
	f.projWrite(`INSERT INTO career_stat
		(player_id, career, system, stat, value, context, updated_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, playerID, career, system, stat, value, context, seq)
}

func seedSystemBoardRow(
	t *testing.T, f *fixture, playerID int64, system, stat string, value float64, n int,
) {
	t.Helper()
	seq := f.event(playerID, n)
	f.projWrite(`INSERT INTO system_stat
		(player_id, system, stat, value, context, updated_seq)
		VALUES (?, ?, ?, ?, ?, ?)`, playerID, system, stat, value, `{"body":"earth"}`, seq)
}

func TestBoardIndexPublishesScopesAndAuthoritativeBodyMetadata(t *testing.T) {
	f := newFixture(t)
	first := f.player("first")
	second := f.player("second")
	f.stat(first, "fastest_to_luna", 100, 1)
	f.stat(second, "fastest_to_luna", 200, 2)
	f.stat(first, "rud_kraken", 1, 3)
	f.stat(second, "rud_kraken", 1, 4)

	got := decode[readapi.BoardsResponse](t, f.get("/v1/leaderboards"))
	byStat := make(map[string]readapi.BoardSummary, len(got.Boards))
	for _, board := range got.Boards {
		byStat[board.Stat] = board
		if !slices.Equal(board.Scopes, stats.Scopes()) {
			t.Errorf("%s scopes = %v, want %v", board.Stat, board.Scopes, stats.Scopes())
		}
	}
	if !byStat["fastest_to_luna"].BodyDerived {
		t.Error("fastest_to_luna is not marked body-derived")
	}
	if byStat["rud_kraken"].BodyDerived || byStat[stats.StatSOIBodies].BodyDerived {
		t.Error("non-body-keyed board was marked body-derived")
	}
	if board, ok := stats.Describe("fastest_to_future_world"); !ok || !board.BodyDerived {
		t.Errorf("family metadata did not survive Describe: %+v, %v", board, ok)
	}
}

func TestAbsentScopeIsExactlyPlayerScopeAndHasNoScopedFields(t *testing.T) {
	f := newFixture(t)
	p := f.player("whiskers")
	f.stat(p, stats.StatStagings, 7, 1)

	implicit := f.get("/v1/leaderboards/stagings")
	explicit := f.get("/v1/leaderboards/stagings?scope=player")
	if implicit.Code != http.StatusOK || explicit.Code != http.StatusOK || implicit.Body.String() != explicit.Body.String() {
		t.Fatalf("implicit player response differs from explicit:\n%s\n%s", implicit.Body, explicit.Body)
	}
	got := decode[readapi.BoardResponse](t, implicit)
	if got.Scope != stats.ScopePlayer || len(got.Rows) != 1 {
		t.Fatalf("player board = %+v", got)
	}
	var raw struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(implicit.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"save", "save_id", "system"} {
		if _, exists := raw.Rows[0][key]; exists {
			t.Errorf("player row unexpectedly published %q: %s", key, implicit.Body)
		}
	}
}

func TestScopeValidationAndSystemResolutionOrder(t *testing.T) {
	f := newFixture(t)
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 0, 1, 1)

	tests := []struct {
		path   string
		status int
		detail string
	}{
		{"/v1/leaderboards/stagings?scope=nope&system=missing", 400, "scope must be one of player, career, system"},
		{"/v1/leaderboards/stagings?scope=career&period=weekly&at=bad&system=missing", 400, "career scope has no time windows"},
		{"/v1/leaderboards/stagings?scope=system&period=daily&system=missing", 400, "system scope has no time windows"},
		{"/v1/leaderboards/stagings?system=solar-system", 400, "system filtering needs scope=system or scope=career"},
		{"/v1/leaderboards/stagings?scope=career&system=missing", 404, "catlog has never seen a system by that name"},
		{"/v1/leaderboards/stagings?scope=system&system=missing", 404, "catlog has never seen a system by that name"},
	}
	for _, tc := range tests {
		rec := f.get(tc.path)
		var body struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		if rec.Code != tc.status || body.Detail != tc.detail {
			t.Errorf("GET %s = %d %q, want %d %q", tc.path, rec.Code, body.Detail, tc.status, tc.detail)
		}
	}
}

func TestCareerScopeRanksSavesAndNeverPublishesRawCareer(t *testing.T) {
	f := newFixture(t)
	hidden := f.player("hidden")
	alpha := f.player("alpha")
	beta := f.player("beta")
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 0, 1, 1)
	seedSystem(t, f, "hash-alt", "Alt", "Alternate", "alternate", 0, 1, 2)

	const (
		hiddenOne = "hiddencareer001"
		hiddenTwo = "hiddencareer002"
		alphaOne  = "alphacareer0001"
		alphaTwo  = "alphacareer0002"
		betaOne   = "betacareer00001"
	)
	seedScopedCareer(t, f, hidden, hiddenOne, "hash-sol", 1, false, 1)
	seedScopedCareer(t, f, hidden, hiddenTwo, "hash-sol", 2, false, 2)
	seedScopedCareer(t, f, alpha, alphaOne, "hash-sol", 3, true, 3)
	seedScopedCareer(t, f, alpha, alphaTwo, "hash-alt", 1, false, 4)
	seedScopedCareer(t, f, beta, betaOne, "hash-sol", 1, false, 5)
	seedCareerBoardRow(t, f, hidden, hiddenOne, "hash-sol", stats.StatStagings, 120, 1)
	seedCareerBoardRow(t, f, hidden, hiddenTwo, "hash-sol", stats.StatStagings, 110, 2)
	seedCareerBoardRow(t, f, alpha, alphaOne, "hash-sol", stats.StatStagings, 100, 3)
	seedCareerBoardRow(t, f, alpha, alphaTwo, "hash-alt", stats.StatStagings, 90, 4)
	seedCareerBoardRow(t, f, beta, betaOne, "hash-sol", stats.StatStagings, 80, 5)
	// The index denominator remains the all-time player board: players, not
	// saves, and ban-inclusive just as it was before scopes existed.
	f.stat(hidden, stats.StatStagings, 120, 10)
	f.stat(alpha, stats.StatStagings, 100, 11)
	f.stat(beta, stats.StatStagings, 80, 12)
	f.ban("hidden")

	rec := f.get("/v1/leaderboards/stagings?scope=career")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[readapi.BoardResponse](t, rec)
	if got.Scope != stats.ScopeCareer || got.Period != stats.PeriodAllTime || len(got.Rows) != 3 {
		t.Fatalf("career response = %+v", got)
	}
	wantHandles := []string{"alpha", "alpha", "beta"}
	wantSaves := []int64{3, 1, 1}
	wantHashes := []string{"hash-sol", "hash-alt", "hash-sol"}
	for i, row := range got.Rows {
		if row.Handle != wantHandles[i] || row.Rank != i+1 || row.Save != wantSaves[i] ||
			row.System == nil || row.System.Hash != wantHashes[i] || row.System.Name == "" || row.System.Slug == "" {
			t.Errorf("career row %d = %+v", i, row)
		}
	}
	if !got.Rows[0].Rewound {
		t.Error("rewound mark was not read from the career row on a non-career-time board")
	}
	if got.Rows[0].SaveID != readapi.Label(alpha, "career", alphaOne) || got.Rows[0].SaveID == alphaOne {
		t.Errorf("save_id = %q", got.Rows[0].SaveID)
	}
	var context struct {
		Career string `json:"career"`
	}
	if err := json.Unmarshal(got.Rows[0].Context, &context); err != nil || context.Career != got.Rows[0].SaveID {
		t.Errorf("redacted context = %s, want career %q", got.Rows[0].Context, got.Rows[0].SaveID)
	}
	var rawCareerPage struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawCareerPage); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"save", "save_id", "system"} {
		if _, exists := rawCareerPage.Rows[0][key]; !exists {
			t.Errorf("career row omitted required %q: %s", key, rec.Body)
		}
	}
	for _, rawCareer := range []string{hiddenOne, hiddenTwo, alphaOne, alphaTwo, betaOne} {
		if strings.Contains(rec.Body.String(), rawCareer) {
			t.Errorf("raw career %q leaked in %s", rawCareer, rec.Body)
		}
	}
	index := decode[readapi.BoardsResponse](t, f.get("/v1/leaderboards"))
	for _, board := range index.Boards {
		if board.Stat == stats.StatStagings && board.Count != 3 {
			t.Errorf("index count = %d, want 3 players (raw and ban-inclusive), not 5 saves", board.Count)
		}
	}

	bySlug := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?scope=career&system=solar-system"))
	byHash := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?scope=career&system=hash-sol"))
	if len(bySlug.Rows) != 2 || len(byHash.Rows) != 2 || bySlug.Rows[0].SaveID != byHash.Rows[0].SaveID {
		t.Errorf("slug/hash career filtering differs: %+v / %+v", bySlug.Rows, byHash.Rows)
	}
}

func TestSystemScopeRanksPlayerSystemPairsAndBatchesMetadata(t *testing.T) {
	f := newFixture(t)
	hidden := f.player("hidden")
	alpha := f.player("alpha")
	beta := f.player("beta")
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 0, 1, 1)
	seedSystem(t, f, "hash-alt", "Alt", "Alternate", "alternate", 0, 1, 2)
	seedSystemBoardRow(t, f, hidden, "hash-sol", stats.StatStagings, 120, 1)
	seedSystemBoardRow(t, f, hidden, "hash-alt", stats.StatStagings, 110, 2)
	seedSystemBoardRow(t, f, alpha, "hash-sol", stats.StatStagings, 100, 3)
	seedSystemBoardRow(t, f, alpha, "hash-alt", stats.StatStagings, 90, 4)
	seedSystemBoardRow(t, f, beta, "hash-sol", stats.StatStagings, 80, 5)
	f.ban("hidden")

	rec := f.get("/v1/leaderboards/stagings?scope=system")
	got := decode[readapi.BoardResponse](t, rec)
	if got.Scope != stats.ScopeSystem || len(got.Rows) != 3 {
		t.Fatalf("system response = %+v", got)
	}
	wantHandles := []string{"alpha", "alpha", "beta"}
	wantHashes := []string{"hash-sol", "hash-alt", "hash-sol"}
	for i, row := range got.Rows {
		if row.Handle != wantHandles[i] || row.Rank != i+1 || row.System == nil ||
			row.System.Hash != wantHashes[i] || row.Save != 0 || row.SaveID != "" {
			t.Errorf("system row %d = %+v", i, row)
		}
	}
	if got.Rows[0].System.Name != "Solar System" || got.Rows[0].System.Slug != "solar-system" ||
		got.Rows[1].System.Name != "Alternate" || got.Rows[1].System.Slug != "alternate" {
		t.Errorf("system labels = %+v / %+v", got.Rows[0].System, got.Rows[1].System)
	}
	var rawSystemPage struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawSystemPage); err != nil {
		t.Fatal(err)
	}
	if _, exists := rawSystemPage.Rows[0]["system"]; !exists {
		t.Errorf("system row omitted system: %s", rec.Body)
	}
	for _, key := range []string{"save", "save_id", "rewound"} {
		if _, exists := rawSystemPage.Rows[0][key]; exists {
			t.Errorf("system row unexpectedly published %q: %s", key, rec.Body)
		}
	}
	filtered := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?scope=system&system=solar-system"))
	filteredByHash := decode[readapi.BoardResponse](t, f.get("/v1/leaderboards/stagings?scope=system&system=hash-sol"))
	if len(filtered.Rows) != 2 || filtered.Rows[0].Handle != "alpha" || filtered.Rows[0].Rank != 1 ||
		filtered.Rows[1].Handle != "beta" || filtered.Rows[1].Rank != 2 {
		t.Errorf("filtered system rows = %+v", filtered.Rows)
	}
	if len(filteredByHash.Rows) != 2 || filteredByHash.Rows[0].Handle != filtered.Rows[0].Handle ||
		filteredByHash.Rows[1].Handle != filtered.Rows[1].Handle {
		t.Errorf("slug/hash system filtering differs: %+v / %+v", filtered.Rows, filteredByHash.Rows)
	}

	counting := &countingLive{p: f.proj}
	srv, err := readapi.New(readapi.Deps{
		Projections: counting, Events: f.events, Directory: f.dir, Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	page, known, err := srv.Board(t.Context(), stats.StatStagings, stats.PeriodAllTime, "",
		stats.ScopeSystem, "", 50, 0)
	if err != nil || !known || len(page.Rows) != 3 {
		t.Fatalf("direct system board = %+v, %v, %v", page, known, err)
	}
	if counting.calls != 2 {
		t.Errorf("projection query groups = %d, want 2 (one row page + one system metadata batch)", counting.calls)
	}
}
