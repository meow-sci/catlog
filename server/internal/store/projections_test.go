package store_test

import (
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func seedScopedProjectionRows(t *testing.T, p *store.Projections) {
	t.Helper()
	statements := []string{
		`INSERT INTO system
		 (hash, system_id, name, slug, home_body, body_count, reported_complete, first_seq)
		 VALUES
		 ('hash-a', 'Sol', 'Alpha System', 'alpha-system', 'earth', 2, 1, 5),
		 ('hash-b', 'Dense', 'Beta System', 'beta-system', 'beta', 2, 1, 6),
		 ('hash-c', 'Empty', 'Gamma System', 'gamma-system', 'gamma', 0, 0, 7)`,
		`INSERT INTO system_body
		 (hash, body, name, class, kind, rank, radius_m, mass_kg, soi_m, atmo_m, ocean_m,
		  angvel, axis_x, axis_y, axis_z, ccf_to_cce_t0_x, ccf_to_cce_t0_y,
		  ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq)
		 VALUES
		 ('hash-a', 'earth', 'Earth', 'Planet', 'planet', 1, 1, 2, 3, 4, 5,
		  6, 0, 0, 1, 0, 0, 0, 1, 8),
		 ('hash-a', 'sol', 'Sol', 'Star', 'star', 0, 7, 8, 9, 0, 0,
		  10, 0, 1, 0, 0, 0, 0, 1, 9),
		 ('hash-b', 'beta', 'Beta', 'Star', 'star', 0, 11, 12, 13, 0, 0,
		  14, 1, 0, 0, 0, 0, 0, 1, 10)`,
		`INSERT INTO career
		 (player_id, career, max_sim_t, rewound, first_seq, ordinal, last_seq, system, system_changed)
		 VALUES
		 (1, 'career-two', 22.5, 1, 20, 2, 29, 'hash-a', 1),
		 (1, 'career-one', 11.5, 0, 10, 1, 19, 'hash-a', 0),
		 (1, 'career-three', 33.5, 0, 30, 3, 39, 'hash-b', 0),
		 (2, 'career-four', 44.5, 0, 40, 1, 49, 'hash-a', 0),
		 (3, 'career-five', 55.5, 1, 50, 1, 59, 'hash-b', 0)`,
		`INSERT INTO career_stat
		 (player_id, career, system, stat, value, context, updated_seq)
		 VALUES
		 (1, 'career-one', 'hash-a', 'score', 100, '{"save":1}', 70),
		 (1, 'career-two', 'hash-a', 'score', 100, '{"save":2}', 70),
		 (1, 'career-three', 'hash-b', 'score', 130, '{"save":3}', 65),
		 (2, 'career-four', 'hash-a', 'score', 100, '{"save":4}', 70),
		 (3, 'career-five', 'hash-b', 'score', 80, '{"save":5}', 90),
		 (1, 'career-one', 'hash-a', 'altitude', 42, '{"unit":"m"}', 71)`,
		`INSERT INTO system_stat
		 (player_id, system, stat, value, context, updated_seq)
		 VALUES
		 (1, 'hash-a', 'score', 100, '{"system":"a1"}', 70),
		 (2, 'hash-a', 'score', 100, '{"system":"a2"}', 70),
		 (1, 'hash-b', 'score', 130, '{"system":"b1"}', 65),
		 (3, 'hash-b', 'score', 80, '{"system":"b3"}', 90),
		 (1, 'hash-a', 'altitude', 42, '{"unit":"m"}', 71)`,
	}
	for _, statement := range statements {
		if _, err := p.Writer().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seed scoped projection rows: %v", err)
		}
	}
}

func scopedProjections(t *testing.T) *store.Projections {
	t.Helper()
	p := testutil.MemProjections(t)
	seedScopedProjectionRows(t, p)
	return p
}

func TestCareerProjectionReads(t *testing.T) {
	p := scopedProjections(t)

	desc, err := p.CareerLeaderboard(t.Context(), "score", "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []struct {
		playerID int64
		career   string
	}{
		{1, "career-three"},
		{1, "career-one"},
		{1, "career-two"},
		{2, "career-four"},
		{3, "career-five"},
	}
	if len(desc) != len(wantOrder) {
		t.Fatalf("descending rows = %d, want %d", len(desc), len(wantOrder))
	}
	for i, want := range wantOrder {
		if desc[i].PlayerID != want.playerID || desc[i].Career != want.career {
			t.Errorf("descending row %d = (%d, %q), want (%d, %q)",
				i, desc[i].PlayerID, desc[i].Career, want.playerID, want.career)
		}
	}
	if got := desc[1]; got.System != "hash-a" || got.Ordinal != 1 || got.Stat != "score" ||
		got.Value != 100 || string(got.Context) != `{"save":1}` || got.UpdatedSeq != 70 {
		t.Errorf("career row fields = %+v context=%s", got, got.Context)
	}

	asc, err := p.CareerLeaderboard(t.Context(), "score", "", true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{asc[0].Career, asc[1].Career, asc[2].Career, asc[3].Career, asc[4].Career}; !reflect.DeepEqual(got, []string{"career-five", "career-one", "career-two", "career-four", "career-three"}) {
		t.Errorf("ascending order = %v", got)
	}

	page, err := p.CareerLeaderboard(t.Context(), "score", "", false, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page[0].Career, page[1].Career}; !reflect.DeepEqual(got, []string{"career-one", "career-two"}) {
		t.Errorf("career page = %v", got)
	}
	filtered, err := p.CareerLeaderboard(t.Context(), "score", "hash-a", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{filtered[0].Career, filtered[1].Career, filtered[2].Career}; !reflect.DeepEqual(got, []string{"career-one", "career-two", "career-four"}) {
		t.Errorf("system-filtered careers = %v", got)
	}

	stats, err := p.CareerStatsForPlayer(t.Context(), 1, "career-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Stat != "altitude" || stats[1].Stat != "score" ||
		stats[0].Ordinal != 1 || stats[0].System != "hash-a" {
		t.Errorf("career stats = %+v", stats)
	}

	// Rank correction must see both saves owned by one hidden player. Returning
	// one player-level summary would lose one of these tied rows.
	hidden, err := p.CareerStatsForPlayers(t.Context(), "score", "hash-a", []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{hidden[0].Career, hidden[1].Career}; !reflect.DeepEqual(got, []string{"career-one", "career-two"}) {
		t.Errorf("hidden player's save rows = %v", got)
	}

	aheadDesc, err := p.CareerStatAhead(t.Context(), "score", "", 100, 70, false)
	if err != nil {
		t.Fatal(err)
	}
	aheadAsc, err := p.CareerStatAhead(t.Context(), "score", "", 100, 70, true)
	if err != nil {
		t.Fatal(err)
	}
	if aheadDesc != 1 || aheadAsc != 1 {
		t.Errorf("career rows ahead = desc %d asc %d, want 1 and 1", aheadDesc, aheadAsc)
	}
	filteredAhead, err := p.CareerStatAhead(t.Context(), "score", "hash-a", 90, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if filteredAhead != 3 {
		t.Errorf("career rows ahead in hash-a = %d, want 3", filteredAhead)
	}
	allEntrants, err := p.CareerStatEntrants(t.Context(), "score", "")
	if err != nil {
		t.Fatal(err)
	}
	filteredEntrants, err := p.CareerStatEntrants(t.Context(), "score", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if allEntrants != 5 || filteredEntrants != 3 {
		t.Errorf("career entrants = all %d filtered %d, want 5 and 3 saves", allEntrants, filteredEntrants)
	}
}

func TestCareerRowsAndOrdinalLookup(t *testing.T) {
	p := scopedProjections(t)
	rows, err := p.PlayerCareers(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{rows[0].Career, rows[1].Career, rows[2].Career}; !reflect.DeepEqual(got, []string{"career-one", "career-two", "career-three"}) {
		t.Fatalf("career ordinal order = %v", got)
	}
	want := store.CareerRow{
		PlayerID:      1,
		Career:        "career-two",
		Ordinal:       2,
		System:        "hash-a",
		SystemChanged: true,
		MaxSimT:       22.5,
		Rewound:       true,
		FirstSeq:      20,
		LastSeq:       29,
	}
	if rows[1] != want {
		t.Errorf("career row = %+v, want %+v", rows[1], want)
	}
	found, ok, err := p.CareerByOrdinal(t.Context(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found != want {
		t.Errorf("career ordinal 2 = %+v, %v, want %+v, true", found, ok, want)
	}
	missing, ok, err := p.CareerByOrdinal(t.Context(), 1, 99)
	if err != nil {
		t.Fatal(err)
	}
	if ok || missing != (store.CareerRow{}) {
		t.Errorf("missing career = %+v, %v", missing, ok)
	}
}

func TestSystemProjectionReads(t *testing.T) {
	p := scopedProjections(t)

	desc, err := p.SystemLeaderboard(t.Context(), "score", "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []struct {
		playerID int64
		system   string
	}{
		{1, "hash-b"}, {1, "hash-a"}, {2, "hash-a"}, {3, "hash-b"},
	}
	for i, want := range wantOrder {
		if desc[i].PlayerID != want.playerID || desc[i].System != want.system {
			t.Errorf("descending system row %d = (%d, %q), want (%d, %q)",
				i, desc[i].PlayerID, desc[i].System, want.playerID, want.system)
		}
	}
	if got := desc[1]; got.Stat != "score" || got.Value != 100 ||
		string(got.Context) != `{"system":"a1"}` || got.UpdatedSeq != 70 {
		t.Errorf("system row fields = %+v context=%s", got, got.Context)
	}

	asc, err := p.SystemLeaderboard(t.Context(), "score", "", true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{asc[0].System, asc[1].System, asc[2].System, asc[3].System}; !reflect.DeepEqual(got, []string{"hash-b", "hash-a", "hash-a", "hash-b"}) {
		t.Errorf("ascending system order = %v", got)
	}
	page, err := p.SystemLeaderboard(t.Context(), "score", "", false, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].PlayerID != 1 || page[0].System != "hash-a" ||
		page[1].PlayerID != 2 || page[1].System != "hash-a" {
		t.Errorf("system page = %+v", page)
	}
	filtered, err := p.SystemLeaderboard(t.Context(), "score", "hash-a", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].PlayerID != 1 || filtered[1].PlayerID != 2 {
		t.Errorf("filtered system rows = %+v", filtered)
	}

	stats, err := p.SystemStatsForPlayer(t.Context(), 1, "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Stat != "altitude" || stats[1].Stat != "score" ||
		stats[1].System != "hash-a" {
		t.Errorf("player system stats = %+v", stats)
	}
	aheadDesc, err := p.SystemStatAhead(t.Context(), "score", "", 100, 70, false)
	if err != nil {
		t.Fatal(err)
	}
	aheadAsc, err := p.SystemStatAhead(t.Context(), "score", "", 100, 70, true)
	if err != nil {
		t.Fatal(err)
	}
	if aheadDesc != 1 || aheadAsc != 1 {
		t.Errorf("system rows ahead = desc %d asc %d, want 1 and 1", aheadDesc, aheadAsc)
	}
	filteredAhead, err := p.SystemStatAhead(t.Context(), "score", "hash-a", 90, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if filteredAhead != 2 {
		t.Errorf("system rows ahead in hash-a = %d, want 2", filteredAhead)
	}
	allEntrants, err := p.SystemStatEntrants(t.Context(), "score", "")
	if err != nil {
		t.Fatal(err)
	}
	filteredEntrants, err := p.SystemStatEntrants(t.Context(), "score", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if allEntrants != 4 || filteredEntrants != 2 {
		t.Errorf("system entrants = all %d filtered %d, want 4 and 2 (player, system) pairs",
			allEntrants, filteredEntrants)
	}
}

func TestSystemLeaderboardFinalTieIsPlayerThenSystem(t *testing.T) {
	p := scopedProjections(t)
	if _, err := p.Writer().ExecContext(t.Context(),
		`INSERT INTO system_stat (player_id, system, stat, value, updated_seq)
		 VALUES (1, 'hash-c', 'score', 100, 70)`); err != nil {
		t.Fatal(err)
	}
	rows, err := p.SystemLeaderboard(t.Context(), "score", "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]struct {
		playerID int64
		system   string
	}, 0, 3)
	for _, row := range rows[1:4] {
		got = append(got, struct {
			playerID int64
			system   string
		}{row.PlayerID, row.System})
	}
	want := []struct {
		playerID int64
		system   string
	}{{1, "hash-a"}, {1, "hash-c"}, {2, "hash-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system tie order = %+v, want %+v", got, want)
	}
}

func TestPlayerSystemsJoinsMetadataAndEffectiveCompleteness(t *testing.T) {
	p := scopedProjections(t)
	rows, err := p.PlayerSystems(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("player systems = %+v, want two distinct systems", rows)
	}
	byHash := make(map[string]store.SystemRow, len(rows))
	for _, row := range rows {
		byHash[row.Hash] = row
	}
	if got := byHash["hash-a"]; got.SystemID != "Sol" || got.Name != "Alpha System" ||
		got.Slug != "alpha-system" || got.HomeBody != "earth" || got.BodyCount != 2 ||
		!got.Complete || got.FirstSeq != 5 {
		t.Errorf("complete system metadata = %+v", got)
	}
	if got := byHash["hash-b"]; got.SystemID != "Dense" || got.Name != "Beta System" ||
		got.Slug != "beta-system" || got.HomeBody != "beta" || got.BodyCount != 2 ||
		got.Complete || got.FirstSeq != 6 {
		t.Errorf("effectively incomplete system metadata = %+v", got)
	}
}
