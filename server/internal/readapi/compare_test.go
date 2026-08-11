package readapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// boardOf finds one board in a comparison.
func boardOf(t *testing.T, cmp readapi.CompareResponse, stat string) readapi.CompareBoard {
	t.Helper()
	for _, b := range cmp.Boards {
		if b.Stat == stat {
			return b
		}
	}
	t.Fatalf("no %s board in the comparison: %+v", stat, cmp.Boards)
	return readapi.CompareBoard{}
}

func TestCompareLaysThreeHandlesSideBySide(t *testing.T) {
	f := newFixture(t)
	ace, tumbler, crasher := f.player("ace"), f.player("tumbler"), f.player("crasher")

	f.stat(ace, stats.StatStagings, 40, 1)
	f.stat(tumbler, stats.StatStagings, 10, 2)
	f.stat(crasher, stats.StatStagings, 25, 3)
	// Only two of the three are on this one.
	f.stat(ace, stats.StatKittenTumbles, 3, 4)
	f.stat(crasher, stats.StatKittenTumbles, 9, 5)

	cmp := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles=ace,tumbler,crasher"))

	// Column order is request order, so a client can zip its header row against
	// each board's rows without sorting anything.
	var handles []string
	for _, h := range cmp.Handles {
		if !h.Found || h.Since == 0 {
			t.Errorf("handle %+v", h)
		}
		handles = append(handles, h.Handle)
	}
	if !slices.Equal(handles, []string{"ace", "tumbler", "crasher"}) {
		t.Errorf("handles = %v, want request order", handles)
	}

	stagings := boardOf(t, cmp, stats.StatStagings)
	if stagings.Title == "" || stagings.Unit == "" || stagings.Ascending {
		t.Errorf("board metadata missing or wrong: %+v", stagings)
	}
	if stagings.Players != 3 {
		t.Errorf("stagings players = %d, want 3", stagings.Players)
	}
	// Ranks are positions on the whole board, not among the compared handles.
	want := map[string]int{"ace": 1, "crasher": 2, "tumbler": 3}
	for _, row := range stagings.Rows {
		if row.Rank != want[row.Handle] {
			t.Errorf("%s rank = %d, want %d (rank is global, not among the compared)", row.Handle, row.Rank, want[row.Handle])
		}
		if row.Updated == 0 {
			t.Errorf("%s has no updated timestamp", row.Handle)
		}
	}

	// A board only some of them are on carries only the rows that exist: absent
	// is absent, not zero, and the UI renders the gap.
	tumbles := boardOf(t, cmp, stats.StatKittenTumbles)
	if len(tumbles.Rows) != 2 {
		t.Fatalf("kitten_tumbles rows = %+v, want the two who are on it", tumbles.Rows)
	}
	for _, row := range tumbles.Rows {
		if row.Handle == "tumbler" {
			t.Error("a player who is not on the board was given a row")
		}
	}

	// A board nobody in the comparison is on is not in the comparison at all.
	for _, b := range cmp.Boards {
		if b.Stat == stats.StatRUDTotal {
			t.Error("an empty board was included; a comparison is not the board index")
		}
	}
}

func TestCompareIsConsistentWithTheProfileEndpoint(t *testing.T) {
	// The comparison is the profile assembly pivoted, and the point of that is
	// that the numbers cannot drift apart.
	f := newFixture(t)
	a, b := f.player("ace"), f.player("rival")
	f.stat(a, stats.StatStagings, 40, 1)
	f.stat(b, stats.StatStagings, 10, 2)
	f.ban("rival")
	f.player("third")
	f.stat(f.byHandle["third"], stats.StatStagings, 5, 3)

	cmp := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles=ace,third"))
	for _, row := range boardOf(t, cmp, stats.StatStagings).Rows {
		profile := decode[readapi.PlayerResponse](t, f.get("/v1/players/"+row.Handle))
		for _, p := range profile.Stats {
			if p.Stat != stats.StatStagings {
				continue
			}
			if p.Rank != row.Rank || p.Value != row.Value || p.Players != boardOf(t, cmp, stats.StatStagings).Players {
				t.Errorf("%s: comparison says rank %d value %v, profile says rank %d value %v",
					row.Handle, row.Rank, row.Value, p.Rank, p.Value)
			}
		}
	}
}

func TestCompareAttachesSystemsInOneResponseLevelBatch(t *testing.T) {
	f := newFixture(t)
	ace, rival := f.player("ace"), f.player("rival")
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 0, 1, 1)
	seedSystem(t, f, "hash-alt", "Alt", "Alternate", "alternate", 0, 1, 2)
	aceSeq, rivalSeq := f.event(ace, 1), f.event(rival, 1)
	seedDetailedCareer(t, f, ace, "ace-winning-career", "hash-sol", 1, false, false, 1, aceSeq, aceSeq)
	seedDetailedCareer(t, f, rival, "rival-winning-career", "hash-alt", 1, false, false, 1, rivalSeq, rivalSeq)
	f.statContext(ace, stats.StatStagings, 4, `{"career":"ace-winning-career"}`)
	f.statContext(rival, stats.StatStagings, 3, `{"career":"rival-winning-career"}`)

	counting := &countingLive{p: f.proj}
	srv, err := readapi.New(readapi.Deps{
		Projections: counting, Events: f.events, Directory: f.dir, Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := srv.Compare(t.Context(), []string{"ace", "rival"})
	if err != nil {
		t.Fatal(err)
	}
	rows := boardOf(t, cmp, stats.StatStagings).Rows
	if len(rows) != 2 || rows[0].System == nil || rows[0].System.Name != "Solar System" ||
		rows[1].System == nil || rows[1].System.Name != "Alternate" {
		t.Fatalf("comparison systems = %+v", rows)
	}
	// stat census + (stats and rank inputs per player) + one career-binding
	// group + one system-header metadata group. In particular, adding the second
	// handle does not add a second system-header read.
	if counting.calls != 7 {
		t.Errorf("projection query groups = %d, want 7 with one response-level metadata batch", counting.calls)
	}
	raw, err := json.Marshal(cmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, career := range []string{"ace-winning-career", "rival-winning-career"} {
		if strings.Contains(string(raw), career) {
			t.Errorf("comparison published raw career %q: %s", career, raw)
		}
	}
}

func TestCompareTreatsAMissingHandleTheSameWayTheProfileDoes(t *testing.T) {
	// Unknown, retired and banned are deliberately indistinguishable (§4.8), and
	// a comparison must not become the oracle the profile endpoint refuses to be.
	f := newFixture(t)
	f.player("ace")
	f.player("was_banned")
	f.ban("was_banned")
	f.stat(f.byHandle["ace"], stats.StatStagings, 40, 1)

	cmp := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles=ace,was_banned,never_existed"))
	if len(cmp.Handles) != 3 {
		t.Fatalf("handles = %+v, want all three echoed", cmp.Handles)
	}
	for _, h := range cmp.Handles[1:] {
		if h.Found || h.Since != 0 {
			t.Errorf("%+v: a missing handle must carry nothing but its name", h)
		}
	}
	if cmp.Handles[1].Found != cmp.Handles[2].Found {
		t.Error("banned and unknown gave different answers; this endpoint is now a ban oracle")
	}
	// The comparison still works for whoever is left.
	if len(boardOf(t, cmp, stats.StatStagings).Rows) != 1 {
		t.Error("one missing handle emptied the comparison")
	}
}

func TestCompareCapsAndDeduplicatesItsInput(t *testing.T) {
	f := newFixture(t)
	var asked []string
	for i := range readapi.MaxCompareHandles + 4 {
		handle := fmt.Sprintf("player_%02d", i)
		f.player(handle)
		f.stat(f.byHandle[handle], stats.StatStagings, float64(100-i), i)
		asked = append(asked, handle)
	}

	cmp := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles="+strings.Join(asked, ",")))
	if len(cmp.Handles) != readapi.MaxCompareHandles {
		t.Errorf("%d handles came back, want the cap of %d", len(cmp.Handles), readapi.MaxCompareHandles)
	}
	// The effective list is echoed, so a client that asked for too many can see
	// which ones it actually got rather than guessing.
	if cmp.Handles[0].Handle != "player_00" {
		t.Errorf("kept %q first, want the first requested", cmp.Handles[0].Handle)
	}

	// A handle repeated (in any casing) is one column, not two.
	dup := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles=player_00,PLAYER_00,player_01"))
	if len(dup.Handles) != 2 {
		t.Errorf("handles = %+v, want the repeat collapsed", dup.Handles)
	}

	// Repeating the parameter is the same request as one comma-separated list.
	split := decode[readapi.CompareResponse](t, f.get("/v1/compare?handles=player_00&handles=player_01"))
	if len(split.Handles) != 2 {
		t.Errorf("handles = %+v, want both", split.Handles)
	}
}

func TestCompareWithNobodyIsAnEmptyComparison(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/v1/compare")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a UI with nobody selected asks the same URL", rec.Code)
	}
	got := decode[readapi.CompareResponse](t, rec)
	if len(got.Handles) != 0 || len(got.Boards) != 0 {
		t.Errorf("got %+v", got)
	}
}
