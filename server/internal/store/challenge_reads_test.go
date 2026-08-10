package store_test

import (
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func challengeProjections(t *testing.T) *store.Projections {
	t.Helper()
	p := testutil.MemProjections(t)
	_, err := p.Writer().ExecContext(t.Context(), `
		INSERT INTO challenge_stat
		(player_id, career, system, challenge, value, context, updated_seq) VALUES
		(1, '', '', 'tumbleweek', 5, NULL, 50),
		(2, '', '', 'tumbleweek', 10, '{}', 40),
		(3, '', '', 'tumbleweek', 10, '{"kid":"three"}', 40),

		(1, 'career-b', 'system-b', 'speedrun_orbit', 100, NULL, 20),
		(1, 'career-a', 'system-a', 'speedrun_orbit', 100, '{"body":"home-a"}', 20),
		(2, 'career-c', 'system-a', 'speedrun_orbit', 90, '[]', 10),

		(1, '', 'system-b', 'heavy_lift_week', 200, '{"body":"home-b"}', 30),
		(1, '', 'system-a', 'heavy_lift_week', 200, '{"body":"home-a"}', 30),
		(2, '', 'system-a', 'heavy_lift_week', 300, NULL, 31)`)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChallengeLeaderboardFieldsScopesOrderingAndPaging(t *testing.T) {
	p := challengeProjections(t)

	player, err := p.ChallengeLeaderboard(t.Context(), "tumbleweek", "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int64{player[0].PlayerID, player[1].PlayerID, player[2].PlayerID}; !reflect.DeepEqual(got, []int64{2, 3, 1}) {
		t.Errorf("player-scope order = %v", got)
	}
	if got, want := player[2], (store.ChallengeRow{
		PlayerID: 1, Career: "", System: "", Challenge: "tumbleweek",
		Value: 5, Context: nil, UpdatedSeq: 50,
	}); !reflect.DeepEqual(got, want) {
		t.Errorf("player row fields = %+v", got)
	}
	if player[0].Context == nil || string(player[0].Context) != `{}` {
		t.Errorf("JSON context was not preserved: %#v", player[0].Context)
	}

	career, err := p.ChallengeLeaderboard(t.Context(), "speedrun_orbit", "", true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{career[0].Career, career[1].Career, career[2].Career}; !reflect.DeepEqual(got, []string{"career-c", "career-a", "career-b"}) {
		t.Errorf("career-scope ascending/tie order = %v", got)
	}
	if got := career[0]; got.PlayerID != 2 || got.System != "system-a" || got.Challenge != "speedrun_orbit" ||
		got.Value != 90 || string(got.Context) != `[]` || got.UpdatedSeq != 10 {
		t.Errorf("career row fields = %+v context=%s", got, got.Context)
	}

	system, err := p.ChallengeLeaderboard(t.Context(), "heavy_lift_week", "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{system[0].System, system[1].System, system[2].System}; !reflect.DeepEqual(got, []string{"system-a", "system-a", "system-b"}) {
		t.Errorf("unfiltered system-scope order = %v", got)
	}
	if system[0].PlayerID != 2 || system[1].PlayerID != 1 || system[2].PlayerID != 1 {
		t.Errorf("system-scope player tie order = %+v", system)
	}

	filtered, err := p.ChallengeLeaderboard(t.Context(), "heavy_lift_week", "system-a", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].PlayerID != 2 || filtered[1].PlayerID != 1 {
		t.Errorf("exact system filter = %+v", filtered)
	}
	page, err := p.ChallengeLeaderboard(t.Context(), "heavy_lift_week", "", false, 1, -9)
	if err != nil || len(page) != 1 || page[0].PlayerID != 2 {
		t.Errorf("negative-offset page = %+v, %v", page, err)
	}
	page, err = p.ChallengeLeaderboard(t.Context(), "heavy_lift_week", "", false, 1, 1)
	if err != nil || len(page) != 1 || page[0].PlayerID != 1 || page[0].System != "system-a" {
		t.Errorf("offset page = %+v, %v", page, err)
	}
	if rows, err := p.ChallengeLeaderboard(t.Context(), "heavy_lift_week", "", false, 0, 0); err != nil || rows != nil {
		t.Errorf("non-positive limit = %+v, %v; want nil,nil", rows, err)
	}
}

func TestChallengeAheadAndEntrantsMatchScopedRowSemantics(t *testing.T) {
	p := challengeProjections(t)
	tests := []struct {
		name              string
		challenge, system string
		value             float64
		seq               int64
		asc               bool
		wantAhead         int64
		wantEntrants      int64
	}{
		{"player descending", "tumbleweek", "", 10, 40, false, 0, 3},
		{"player earlier tie", "tumbleweek", "", 10, 41, false, 2, 3},
		{"career ascending", "speedrun_orbit", "", 100, 20, true, 1, 3},
		{"career filtered", "speedrun_orbit", "system-a", 100, 20, true, 1, 2},
		{"system descending all", "heavy_lift_week", "", 200, 30, false, 1, 3},
		{"system descending filtered", "heavy_lift_week", "system-a", 200, 30, false, 1, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ahead, err := p.ChallengeAhead(t.Context(), tc.challenge, tc.system, tc.value, tc.seq, tc.asc)
			if err != nil || ahead != tc.wantAhead {
				t.Errorf("ahead = %d, %v; want %d", ahead, err, tc.wantAhead)
			}
			entrants, err := p.ChallengeEntrants(t.Context(), tc.challenge, tc.system)
			if err != nil || entrants != tc.wantEntrants {
				t.Errorf("entrants = %d, %v; want %d raw scoped rows", entrants, err, tc.wantEntrants)
			}
		})
	}
	if ahead, err := p.ChallengeAhead(t.Context(), "missing", "", 0, 0, false); err != nil || ahead != 0 {
		t.Errorf("missing challenge ahead = %d, %v", ahead, err)
	}
	if entrants, err := p.ChallengeEntrants(t.Context(), "missing", ""); err != nil || entrants != 0 {
		t.Errorf("missing challenge entrants = %d, %v", entrants, err)
	}
}

func TestChallengesForPlayerReturnsEveryRawScopeInCanonicalOrder(t *testing.T) {
	p := challengeProjections(t)
	rows, err := p.ChallengesForPlayer(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ challenge, career, system string }{
		{"heavy_lift_week", "", "system-a"},
		{"heavy_lift_week", "", "system-b"},
		{"speedrun_orbit", "career-a", "system-a"},
		{"speedrun_orbit", "career-b", "system-b"},
		{"tumbleweek", "", ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("player challenge rows = %+v", rows)
	}
	for i, expected := range want {
		if rows[i].Challenge != expected.challenge || rows[i].Career != expected.career || rows[i].System != expected.system {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], expected)
		}
	}
	if rows[0].PlayerID != 1 || rows[0].Value != 200 || string(rows[0].Context) != `{"body":"home-a"}` || rows[0].UpdatedSeq != 30 {
		t.Errorf("raw player row fields = %+v context=%s", rows[0], rows[0].Context)
	}
	missing, err := p.ChallengesForPlayer(t.Context(), 99)
	if err != nil || len(missing) != 0 {
		t.Errorf("missing player rows = %+v, %v", missing, err)
	}
}

func TestChallengeReadsReportStorageErrors(t *testing.T) {
	p := testutil.MemProjections(t)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChallengeLeaderboard(t.Context(), "x", "", false, 1, 0); err == nil {
		t.Error("ChallengeLeaderboard succeeded after close")
	}
	if _, err := p.ChallengeAhead(t.Context(), "x", "", 1, 1, false); err == nil {
		t.Error("ChallengeAhead succeeded after close")
	}
	if _, err := p.ChallengeEntrants(t.Context(), "x", ""); err == nil {
		t.Error("ChallengeEntrants succeeded after close")
	}
	if _, err := p.ChallengesForPlayer(t.Context(), 1); err == nil {
		t.Error("ChallengesForPlayer succeeded after close")
	}
}
