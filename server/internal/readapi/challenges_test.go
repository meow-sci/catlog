package readapi_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

const challengeOpenMS int64 = 1_786_320_000_000

func challengeFixture(t *testing.T, now int64) *fixture {
	t.Helper()
	return newFixture(t, func(d *readapi.Deps) {
		d.Now = func() time.Time { return time.UnixMilli(now) }
	})
}

func seedChallengeRow(t *testing.T, f *fixture, player int64, career, system, challenge string, value float64, context any, n int) int64 {
	t.Helper()
	seq := f.event(player, n)
	f.projWrite(`INSERT INTO challenge_stat
		(player_id, career, system, challenge, value, context, updated_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, player, career, system, challenge, value, context, seq)
	return seq
}

func TestChallengesCatalogueUsesServerClockAndRawEntrantCounts(t *testing.T) {
	f := challengeFixture(t, challengeOpenMS)
	a, b := f.player("alpha"), f.player("bravo")
	seedChallengeRow(t, f, a, "", "", "tumbleweek", 4, nil, 1)
	seedChallengeRow(t, f, b, "", "", "tumbleweek", 8, nil, 2)
	f.ban("bravo")

	rec := f.get("/v1/challenges")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[readapi.ChallengesResponse](t, rec)
	defs := stats.Challenges()
	if got.Now != challengeOpenMS || len(got.Challenges) != len(defs) {
		t.Fatalf("catalogue = %+v", got)
	}
	for i, def := range defs {
		row := got.Challenges[i]
		if row.Challenge != def.Key || row.Title != def.Title || row.Blurb != def.Blurb ||
			row.Unit != def.Unit || row.Ascending != def.Ascending || row.Scope != def.Scope ||
			row.Opens != def.Opens || row.Closes != def.Closes || row.State != "open" {
			t.Errorf("challenge[%d] = %+v, definition = %+v", i, row, def)
		}
		want := int64(0)
		if def.Key == "tumbleweek" {
			want = 2 // raw rows include the hidden player
		}
		if row.Entrants != want {
			t.Errorf("%s entrants = %d, want %d", def.Key, row.Entrants, want)
		}
	}
}

func TestChallengeDetailPlayerScopeClosesRanksAroundHiddenRows(t *testing.T) {
	f := challengeFixture(t, challengeOpenMS)
	hidden, alpha, bravo := f.player("hidden"), f.player("alpha"), f.player("bravo")
	seedChallengeRow(t, f, hidden, "", "", "tumbleweek", 99, nil, 1)
	seqA := seedChallengeRow(t, f, alpha, "", "", "tumbleweek", 12,
		`{"career":"private","body":"mun"}`, 2)
	seedChallengeRow(t, f, bravo, "", "", "tumbleweek", 8, nil, 3)
	f.ban("hidden")

	rec := f.get("/v1/challenges/tumbleweek?limit=1&offset=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[readapi.ChallengeResponse](t, rec)
	if got.State != "open" || got.Entrants != 3 || got.Limit != 1 || got.Offset != 0 || len(got.Rows) != 1 {
		t.Fatalf("response = %+v", got)
	}
	row := got.Rows[0]
	if row.Rank != 1 || row.Handle != "alpha" || row.Value != 12 || row.Updated != 1_800_000_000_000+seqA {
		t.Errorf("row = %+v", row)
	}
	if row.Save != 0 || row.SaveID != "" || row.System != nil || row.Rewound {
		t.Errorf("player row leaked another scope: %+v", row)
	}
	if strings.Contains(string(row.Context), "private") || !strings.Contains(string(row.Context), "mun") {
		t.Errorf("context was not redacted/preserved: %s", row.Context)
	}

	second := decode[readapi.ChallengeResponse](t, f.get("/v1/challenges/tumbleweek?limit=1&offset=1"))
	if len(second.Rows) != 1 || second.Rows[0].Rank != 2 || second.Rows[0].Handle != "bravo" {
		t.Errorf("second visible page = %+v", second.Rows)
	}
}

func TestChallengeDetailCareerAndSystemFieldsAreScopeSpecific(t *testing.T) {
	f := challengeFixture(t, challengeOpenMS)
	p := f.player("pilot")
	seedSystem(t, f, "hash-sol", "sol", "Solar System", "solar-system", 0, 1, 1)
	careerSeq := f.event(p, 1)
	seedDetailedCareer(t, f, p, "raw-career", "hash-sol", 7, false, true, 20, careerSeq, careerSeq)
	careerUpdated := seedChallengeRow(t, f, p, "raw-career", "hash-sol", "speedrun_orbit", 1500,
		`{"career":"raw-career","body":"earth"}`, 2)
	systemUpdated := seedChallengeRow(t, f, p, "", "hash-sol", "heavy_lift_week", 42000,
		`{"career":"raw-career","mass_kg":42000}`, 3)

	careerRec := f.get("/v1/challenges/speedrun_orbit")
	career := decode[readapi.ChallengeResponse](t, careerRec)
	if careerRec.Code != http.StatusOK || len(career.Rows) != 1 {
		t.Fatalf("career response = %d %+v", careerRec.Code, career)
	}
	cr := career.Rows[0]
	if cr.Save != 7 || len(cr.SaveID) != readapi.LabelLen || !cr.Rewound || cr.System != nil ||
		cr.Updated != 1_800_000_000_000+careerUpdated || strings.Contains(careerRec.Body.String(), "raw-career") {
		t.Errorf("career row = %+v; wire=%s", cr, careerRec.Body)
	}

	systemRec := f.get("/v1/challenges/heavy_lift_week")
	system := decode[readapi.ChallengeResponse](t, systemRec)
	if systemRec.Code != http.StatusOK || len(system.Rows) != 1 {
		t.Fatalf("system response = %d %+v", systemRec.Code, system)
	}
	sr := system.Rows[0]
	wantSystem := &readapi.SystemRef{Hash: "hash-sol", Name: "Solar System", Slug: "solar-system"}
	if !reflect.DeepEqual(sr.System, wantSystem) || sr.Save != 0 || sr.SaveID != "" || sr.Rewound ||
		sr.Updated != 1_800_000_000_000+systemUpdated || strings.Contains(systemRec.Body.String(), "raw-career") {
		t.Errorf("system row = %+v; wire=%s", sr, systemRec.Body)
	}

	for name, body := range map[string][]byte{"career": careerRec.Body.Bytes(), "system": systemRec.Body.Bytes()} {
		var wire struct {
			Rows []map[string]json.RawMessage `json:"rows"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if name == "career" {
			if _, ok := wire.Rows[0]["system"]; ok {
				t.Errorf("career row serialized system: %s", body)
			}
		} else {
			for _, key := range []string{"save", "save_id", "rewound"} {
				if _, ok := wire.Rows[0][key]; ok {
					t.Errorf("system row serialized %s: %s", key, body)
				}
			}
		}
	}
}

func TestCareerChallengeDropsEveryHiddenSaveAndClosesRank(t *testing.T) {
	f := challengeFixture(t, challengeOpenMS)
	hidden, visible := f.player("hidden"), f.player("visible")
	seqH1, seqH2, seqV := f.event(hidden, 1), f.event(hidden, 2), f.event(visible, 3)
	seedDetailedCareer(t, f, hidden, "hidden-one", "", 1, false, false, 0, seqH1, seqH1)
	seedDetailedCareer(t, f, hidden, "hidden-two", "", 2, false, false, 0, seqH2, seqH2)
	seedDetailedCareer(t, f, visible, "visible-save", "", 4, false, false, 0, seqV, seqV)
	f.projWrite(`INSERT INTO challenge_stat
		(player_id, career, system, challenge, value, updated_seq) VALUES
		(?, 'hidden-one', '', 'speedrun_orbit', 1, ?),
		(?, 'hidden-two', '', 'speedrun_orbit', 2, ?),
		(?, 'visible-save', '', 'speedrun_orbit', 3, ?)`, hidden, seqH1, hidden, seqH2, visible, seqV)
	f.ban("hidden")

	got := decode[readapi.ChallengeResponse](t, f.get("/v1/challenges/speedrun_orbit"))
	if got.Entrants != 3 || len(got.Rows) != 1 || got.Rows[0].Rank != 1 || got.Rows[0].Handle != "visible" {
		t.Fatalf("response = %+v", got)
	}
}

func TestChallengeClosedStillServesRowsAndValidationAndErrors(t *testing.T) {
	f := challengeFixture(t, 1_786_924_800_000)
	p := f.player("pilot")
	seedChallengeRow(t, f, p, "", "", "tumbleweek", 1, nil, 1)
	got := decode[readapi.ChallengeResponse](t, f.get("/v1/challenges/tumbleweek"))
	if got.State != "closed" || len(got.Rows) != 1 {
		t.Errorf("closed challenge = %+v", got)
	}
	if rec := f.get("/v1/challenges/no_such_challenge"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown status = %d: %s", rec.Code, rec.Body)
	}
	if rec := f.get("/v1/challenges/tumbleweek?limit=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad paging status = %d: %s", rec.Code, rec.Body)
	}

	fail := newFixture(t, func(d *readapi.Deps) { d.Projections = failedProjections{} })
	for _, path := range []string{"/v1/challenges", "/v1/challenges/tumbleweek"} {
		if rec := fail.get(path); rec.Code != http.StatusInternalServerError {
			t.Errorf("GET %s status = %d: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestChallengeWireTypesNeverExposeRawCareer(t *testing.T) {
	b, err := json.Marshal(readapi.ChallengeResponse{Rows: []readapi.ChallengeRow{{SaveID: "public"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"career"`) {
		t.Fatalf("public challenge type carries a career field: %s", b)
	}
}
