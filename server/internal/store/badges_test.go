package store_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func badgeProjections(t *testing.T) *store.Projections {
	t.Helper()
	p := testutil.MemProjections(t)
	rows := []struct {
		playerID                     int64
		career, badge, system, first string
		seq, at                      int64
		sim                          any
		context                      any
	}{
		{1, "", "first_flight", "sys-a", "save-a", 5, 1005, nil, nil},
		{1, "", "orbit", "sys-a", "save-a", 10, 1010, nil, nil},
		{1, "save-a", "orbit", "sys-a", "", 10, 1010, 10.5, `{"body":"A"}`},
		// Player 1 earned the same badge later in sys-b. Equal sequences pin the
		// raw-career tie-break used to select one deterministic save.
		{1, "z-save", "orbit", "sys-b", "", 20, 1020, 20.5, `{"body":"late"}`},
		{1, "a-save", "orbit", "sys-b", "", 20, 1021, 21.5, `{"body":"chosen"}`},
		{2, "", "orbit", "sys-b", "save-two", 15, 1015, 15.5, `{"body":"B"}`},
		{2, "save-two", "orbit", "sys-b", "", 15, 1015, 15.5, `{"body":"B"}`},
		{2, "save-three", "orbit", "sys-b", "", 17, 1017, 17.5, `{"body":"B2"}`},
		{3, "", "save_only_control", "sys-a", "save-three", 40, 1040, nil, nil},
		{3, "save-three", "save_only", "sys-a", "", 41, 1041, nil, nil},
		{4, "", "orbit", "sys-a", "save-four", 10, 1011, nil, nil},
	}
	for _, row := range rows {
		if _, err := p.Writer().ExecContext(t.Context(),
			`INSERT INTO badge_award
			 (player_id, career, badge, system, first_career, earned_seq, earned_at, earned_sim_t, context)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.playerID, row.career, row.badge, row.system, row.first,
			row.seq, row.at, row.sim, row.context,
		); err != nil {
			t.Fatalf("seed badge award: %v", err)
		}
	}
	return p
}

func TestBadgesForPlayerUsesExactScopeAndScansEveryField(t *testing.T) {
	p := badgeProjections(t)
	lifetime, err := p.BadgesForPlayer(t.Context(), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := badgeKeys(lifetime); !slices.Equal(got, []string{"first_flight", "orbit"}) {
		t.Fatalf("lifetime badges = %v", got)
	}
	row := lifetime[1]
	if row.PlayerID != 1 || row.Career != "" || row.Badge != "orbit" ||
		row.System != "sys-a" || row.FirstCareer != "save-a" ||
		row.EarnedSeq != 10 || row.EarnedAt != 1010 || row.EarnedSimT.Valid || row.Context != nil {
		t.Errorf("lifetime row = %+v context=%s", row, row.Context)
	}

	save, err := p.BadgesForPlayer(t.Context(), 1, "a-save")
	if err != nil {
		t.Fatal(err)
	}
	if len(save) != 1 {
		t.Fatalf("save badges = %+v", save)
	}
	row = save[0]
	if row.Career != "a-save" || row.FirstCareer != "" ||
		row.EarnedSimT != (sql.NullFloat64{Float64: 21.5, Valid: true}) ||
		string(row.Context) != `{"body":"chosen"}` {
		t.Errorf("save row = %+v context=%s", row, row.Context)
	}
	missing, err := p.BadgesForPlayer(t.Context(), 99, "")
	if err != nil || len(missing) != 0 {
		t.Errorf("missing player rows = %+v, err %v", missing, err)
	}
}

func TestBadgeHoldersOrderingPagingAndSystemDeduplication(t *testing.T) {
	p := badgeProjections(t)
	lifetime, err := p.BadgeHolders(t.Context(), "orbit", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := badgePlayers(lifetime); !slices.Equal(got, []int64{1, 4, 2}) {
		t.Fatalf("lifetime holder order = %v", got)
	}
	page, err := p.BadgeHolders(t.Context(), "orbit", "", 1, 1)
	if err != nil || len(page) != 1 || page[0].PlayerID != 4 {
		t.Fatalf("lifetime page = %+v, err %v", page, err)
	}

	system, err := p.BadgeHolders(t.Context(), "orbit", "sys-b", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := badgePlayers(system); !slices.Equal(got, []int64{2, 1}) {
		t.Fatalf("system holder order = %v", got)
	}
	// Player 1's lifetime row points at sys-a, but the sys-b query must find
	// their later per-save award and choose a-save over the equal-seq z-save.
	if system[1].Career != "a-save" || system[1].System != "sys-b" ||
		system[1].FirstCareer != "" || string(system[1].Context) != `{"body":"chosen"}` {
		t.Errorf("deduplicated system row = %+v context=%s", system[1], system[1].Context)
	}

	for _, tc := range []struct {
		system string
		want   int64
	}{{"", 3}, {"sys-b", 2}, {"missing", 0}} {
		count, err := p.BadgeHolderCount(t.Context(), "orbit", tc.system)
		if err != nil || count != tc.want {
			t.Errorf("holder count system %q = %d, err %v; want %d", tc.system, count, err, tc.want)
		}
		rows, err := p.BadgeHolders(t.Context(), "orbit", tc.system, 100, -5)
		if err != nil || int64(len(rows)) != count {
			t.Errorf("holder denominator system %q = %d rows/%d count, err %v", tc.system, len(rows), count, err)
		}
	}
	empty, err := p.BadgeHolders(t.Context(), "orbit", "", 0, 0)
	if err != nil || empty != nil {
		t.Errorf("zero-limit holders = %+v, err %v", empty, err)
	}
}

func TestBadgeCountsUsesLifetimeRowsOnly(t *testing.T) {
	p := badgeProjections(t)
	counts, err := p.BadgeCounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"first_flight": 1, "orbit": 3, "save_only_control": 1}
	if len(counts) != len(want) {
		t.Fatalf("badge counts = %v, want %v", counts, want)
	}
	for badge, n := range want {
		if counts[badge] != n {
			t.Errorf("badge %q count = %d, want %d", badge, counts[badge], n)
		}
	}
	if _, ok := counts["save_only"]; ok {
		t.Error("per-save-only badge entered the lifetime census")
	}
}

func badgeKeys(rows []store.BadgeRow) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].Badge
	}
	return out
}

func badgePlayers(rows []store.BadgeRow) []int64 {
	out := make([]int64, len(rows))
	for i := range rows {
		out[i] = rows[i].PlayerID
	}
	return out
}
