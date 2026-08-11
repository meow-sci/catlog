package readapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

func seedDetailedCareer(
	t *testing.T, f *fixture, playerID int64, career, system string, ordinal int64,
	changed, rewound bool, maxSimT float64, firstSeq, lastSeq int64,
) {
	t.Helper()
	f.projWrite(`INSERT INTO career
		(player_id, career, ordinal, system, system_changed, max_sim_t, rewound, first_seq, last_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		playerID, career, ordinal, system, changed, maxSimT, rewound, firstSeq, lastSeq)
}

func errorDetail(t *testing.T, recBody []byte) string {
	t.Helper()
	var out struct {
		Code   string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recBody, &out); err != nil {
		t.Fatalf("error response is not JSON: %s", recBody)
	}
	if out.Code != "not_found" {
		t.Errorf("error = %q, want not_found", out.Code)
	}
	return out.Detail
}

func TestSavesListUsesCareerActivityAndOmitsFalseAndUnknownSystemFields(t *testing.T) {
	f := newFixture(t)
	player := f.player("alpha")
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 3, 1, 1)

	firstOne := f.event(player, 1)
	lastOne := f.event(player, 2)
	firstTwo := f.event(player, 3)
	lastTwo := f.event(player, 4)
	const rawOne = "raw-career-no-boards-NEVER-PUBLISH"
	const rawTwo = "raw-career-system-NEVER-PUBLISH"
	seedDetailedCareer(t, f, player, rawOne, "", 1, false, false, 12.5, firstOne, lastOne)
	seedDetailedCareer(t, f, player, rawTwo, "hash-sol", 2, true, true, 45.25, firstTwo, lastTwo)
	seedCareerBoardRow(t, f, player, rawTwo, "hash-sol", stats.StatStagings, 12, 5)
	// A stale board row still counts as a career_stat row in the list, even
	// though the detail endpoint cannot safely invent metadata for it.
	seedCareerBoardRow(t, f, player, rawTwo, "hash-sol", "retired_board_key", 3, 6)

	rec := f.get("/v1/players/ALPHA/saves")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(rawOne)) || bytes.Contains(rec.Body.Bytes(), []byte(rawTwo)) {
		t.Fatalf("raw career escaped in response: %s", rec.Body)
	}
	got := decode[readapi.SavesResponse](t, rec)
	if got.Handle != "alpha" || len(got.Saves) != 2 {
		t.Fatalf("response = %+v", got)
	}
	one, two := got.Saves[0], got.Saves[1]
	if one.Save != 1 || len(one.SaveID) != readapi.LabelLen || one.PlaytimeMS != 12_500 || one.Boards != 0 {
		t.Errorf("save one = %+v", one)
	}
	if one.First != 1_800_000_000_000+firstOne || one.Last != 1_800_000_000_000+lastOne {
		t.Errorf("no-board save activity = (%d, %d), want event recv times", one.First, one.Last)
	}
	if one.System != nil || one.SystemChanged || one.Rewound {
		t.Errorf("save one optional state = %+v", one)
	}
	if two.Save != 2 || len(two.SaveID) != readapi.LabelLen || two.SaveID == one.SaveID ||
		two.PlaytimeMS != 45_250 || two.First != 1_800_000_000_000+firstTwo ||
		two.Last != 1_800_000_000_000+lastTwo || !two.SystemChanged || !two.Rewound || two.Boards != 2 {
		t.Errorf("save two = %+v", two)
	}
	wantSystem := &readapi.SystemRef{Hash: "hash-sol", Name: "Solar System", Slug: "solar-system"}
	if two.System == nil || *two.System != *wantSystem {
		t.Errorf("system = %+v, want %+v", two.System, wantSystem)
	}

	var wire struct {
		Saves []map[string]json.RawMessage `json:"saves"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"system", "system_changed", "rewound"} {
		if _, present := wire.Saves[0][key]; present {
			t.Errorf("false/unknown %q was serialized on save one: %s", key, rec.Body)
		}
	}
	for _, key := range []string{"system", "system_changed", "rewound"} {
		if _, present := wire.Saves[1][key]; !present {
			t.Errorf("true/known %q omitted from save two: %s", key, rec.Body)
		}
	}
}

func TestSaveDetailRanksEveryHiddenCareerWithExactTieRules(t *testing.T) {
	f := newFixture(t)
	target := f.player("alpha")
	visible := f.player("visible")
	hidden := f.player("hidden")
	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 3, 1, 1)

	const targetCareer = "raw-target-career-NEVER-PUBLISH"
	first, last := f.event(target, 1), f.event(target, 2)
	seedDetailedCareer(t, f, target, targetCareer, "hash-sol", 1, true, true, 8.75, first, last)
	for i, career := range []string{"hidden-career-one", "hidden-career-two"} {
		seq := f.event(hidden, 10+i)
		seedDetailedCareer(t, f, hidden, career, "hash-sol", int64(i+1), false, false, 1, seq, seq)
	}
	visibleSeq := f.event(visible, 20)
	seedDetailedCareer(t, f, visible, "visible-career", "hash-sol", 1, false, false, 1, visibleSeq, visibleSeq)

	// Earlier equal values are ahead. Both hidden saves must be removed from
	// rank, while entrants remains the raw number of save rows.
	seedCareerBoardRow(t, f, hidden, "hidden-career-one", "hash-sol", stats.StatStagings, 150, 30)
	seedCareerBoardRow(t, f, hidden, "hidden-career-two", "hash-sol", stats.StatStagings, 100, 31)
	seedCareerBoardRow(t, f, visible, "visible-career", "hash-sol", stats.StatStagings, 100, 32)
	seedCareerBoardRow(t, f, hidden, "hidden-career-one", "hash-sol", stats.StatFastestToOrbit, 10, 33)
	seedCareerBoardRow(t, f, hidden, "hidden-career-two", "hash-sol", stats.StatFastestToOrbit, 50, 34)
	seedCareerBoardRow(t, f, visible, "visible-career", "hash-sol", stats.StatFastestToOrbit, 50, 35)
	seedCareerBoardRow(t, f, target, targetCareer, "hash-sol", stats.StatStagings, 100, 40)
	seedCareerBoardRow(t, f, target, targetCareer, "hash-sol", stats.StatFastestToOrbit, 50, 41)
	f.ban("hidden")

	rec := f.get("/v1/players/alpha/saves/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), targetCareer) || strings.Contains(rec.Body.String(), "hidden-career") {
		t.Fatalf("raw career escaped in response: %s", rec.Body)
	}
	got := decode[readapi.SaveResponse](t, rec)
	if got.Handle != "alpha" || got.Save != 1 || len(got.SaveID) != readapi.LabelLen ||
		got.System == nil || got.System.Name != "Solar System" || !got.SystemChanged || !got.Rewound || got.PlaytimeMS != 8_750 {
		t.Fatalf("save metadata = %+v", got)
	}
	if len(got.Stats) != 2 || got.Stats[0].Stat != stats.StatFastestToOrbit || got.Stats[1].Stat != stats.StatStagings {
		t.Fatalf("ordered stats = %+v", got.Stats)
	}
	for _, row := range got.Stats {
		if row.Rank != 2 || row.Entrants != 4 || row.Updated == 0 {
			t.Errorf("%s rank metadata = %+v", row.Stat, row)
		}
		var context map[string]any
		if err := json.Unmarshal(row.Context, &context); err != nil {
			t.Fatalf("%s context: %v", row.Stat, err)
		}
		if context["career"] != got.SaveID || context["body"] != "earth" {
			t.Errorf("%s redacted context = %v", row.Stat, context)
		}
	}
	if !got.Stats[0].Ascending || got.Stats[0].Title != "Fastest to Orbit" || got.Stats[0].Unit != "ms" || got.Stats[0].Value != 50 {
		t.Errorf("ascending stat = %+v", got.Stats[0])
	}
	if got.Stats[1].Ascending || got.Stats[1].Title != "Stagings" || got.Stats[1].Unit != "stagings" || got.Stats[1].Value != 100 {
		t.Errorf("descending stat = %+v", got.Stats[1])
	}
}

func TestSaveDetailOmitsFalseFieldsAndUnknownBoards(t *testing.T) {
	f := newFixture(t)
	player := f.player("plain")
	seq := f.event(player, 1)
	seedDetailedCareer(t, f, player, "plain-raw-career", "", 1, false, false, 0, seq, seq)
	seedCareerBoardRow(t, f, player, "plain-raw-career", "", "retired_board_key", 1, 2)

	rec := f.get("/v1/players/plain/saves/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decode[readapi.SaveResponse](t, rec)
	if got.System != nil || got.SystemChanged || got.Rewound || got.Stats == nil || len(got.Stats) != 0 {
		t.Fatalf("response = %+v", got)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"system", "system_changed", "rewound"} {
		if _, present := wire[key]; present {
			t.Errorf("false/unknown %q was serialized: %s", key, rec.Body)
		}
	}
}

func TestSaveRoutesMakePrivacyStatusesIndistinguishableAndValidateOrdinal(t *testing.T) {
	f := newFixture(t)
	known := f.player("known")
	seq := f.event(known, 1)
	seedDetailedCareer(t, f, known, "known-career", "", 1, false, false, 0, seq, seq)
	f.player("banned")
	f.ban("banned")
	f.player("retired")
	if err := f.events.RetireHandle(t.Context(), nil, "retired", "purged", 123); err != nil {
		t.Fatal(err)
	}
	if err := f.dir.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, handle := range []string{"unknown", "banned", "retired"} {
		for _, suffix := range []string{"/saves", "/saves/1", "/saves/not-a-number"} {
			rec := f.get("/v1/players/" + handle + suffix)
			if rec.Code != http.StatusNotFound || errorDetail(t, rec.Body.Bytes()) != "no such player" {
				t.Errorf("%s%s = %d %s", handle, suffix, rec.Code, rec.Body)
			}
		}
	}
	for _, ordinal := range []string{"0", "-1", "banana", "2", "999999999999999999999999"} {
		rec := f.get("/v1/players/known/saves/" + ordinal)
		if rec.Code != http.StatusNotFound || errorDetail(t, rec.Body.Bytes()) != "catlog has no such save for this player" {
			t.Errorf("ordinal %q = %d %s", ordinal, rec.Code, rec.Body)
		}
	}
}

func TestSavesListIsAnEmptyArrayAndSaveRoutesCarryCORSOnSuccess(t *testing.T) {
	f := newFixture(t, func(deps *readapi.Deps) { deps.AllowedOrigins = []string{readerOrigin} })
	player := f.player("empty")
	rec := do(t, f.mux, http.MethodGet, "/v1/players/empty/saves", readerOrigin)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != readerOrigin ||
		rec.Header().Get("Cache-Control") != readapi.CacheControl || rec.Body.String() != "{\"handle\":\"empty\",\"saves\":[]}\n" {
		t.Fatalf("empty saves response = %d %s, headers = %v", rec.Code, rec.Body, rec.Header())
	}
	seq := f.event(player, 1)
	seedDetailedCareer(t, f, player, "empty-career", "", 1, false, false, 0, seq, seq)
	rec = do(t, f.mux, http.MethodGet, "/v1/players/empty/saves/1", readerOrigin)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != readerOrigin ||
		rec.Header().Get("Cache-Control") != readapi.CacheControl {
		t.Fatalf("save response = %d %s, headers = %v", rec.Code, rec.Body, rec.Header())
	}
}
