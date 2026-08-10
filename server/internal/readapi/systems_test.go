package readapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/store"
)

type failedProjections struct{}

func (failedProjections) With(func(*store.Projections) error) error {
	return errors.New("projection unavailable")
}

func (failedProjections) WriteGen() int64 { return 0 }

func seedSystem(t *testing.T, f *fixture, hash, id, name, slug string, bodies, complete, seq int) {
	t.Helper()
	f.projWrite(`INSERT INTO system
		(hash, system_id, name, slug, home_body, body_count, reported_complete, first_seq)
		VALUES (?, ?, ?, ?, 'home', ?, ?, ?)`, hash, id, name, slug, bodies, complete, seq)
}

func seedCareer(t *testing.T, f *fixture, playerID int64, career, system string, seq int) {
	t.Helper()
	f.projWrite(`INSERT INTO career (player_id, career, first_seq, last_seq, ordinal, system)
		VALUES (?, ?, ?, ?, 1, ?)`, playerID, career, seq, seq, system)
}

func seedRoot(t *testing.T, f *fixture, hash, body, name string, rank, seq int) {
	t.Helper()
	f.projWrite(`INSERT INTO system_body
		(hash, body, name, class, kind, rank, radius_m, mass_kg, soi_m, atmo_m, ocean_m,
		 angvel, axis_x, axis_y, axis_z, ccf_to_cce_t0_x, ccf_to_cce_t0_y,
		 ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq)
		VALUES (?, ?, ?, 'Star', 'star', ?, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 0, 0, 1, ?)`,
		hash, body, name, rank, seq)
}

func seedOrbitingBody(t *testing.T, f *fixture, hash string) {
	t.Helper()
	f.projWrite(`INSERT INTO system_body
		(hash, body, name, class, kind, rank, parent, radius_m, mass_kg, soi_m, atmo_m,
		 ocean_m, angvel, axis_x, axis_y, axis_z, sma_m, ecc, inc_deg, lan_deg, argp_deg,
		 t_pe, period_s, ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z,
		 ccf_to_cce_t0_w, first_seq)
		VALUES (?, 'beta-child', 'Beta', 'Planet', 'planet', 1, 'alpha-root',
		 11, 12, 13, 14, 15, -16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26,
		 27, 28, 29, 30, 2)`, hash)
}

func TestSystemsListsHeadersInFirstSeenOrderWithCareerCounts(t *testing.T) {
	f := newFixture(t)
	first := f.player("first")
	second := f.player("second")
	hidden := f.player("hidden")

	seedSystem(t, f, "hash-a", "A", "Alpha", "alpha", 2, 1, 20)
	seedRoot(t, f, "hash-a", "alpha-root", "Alpha Root", 0, 1) // one row missing: effectively incomplete
	seedSystem(t, f, "hash-b", "B", "Beta", "beta", 0, 1, 10)
	seedCareer(t, f, first, "career-a1", "hash-a", 1)
	seedCareer(t, f, first, "career-a2", "hash-a", 2)
	seedCareer(t, f, second, "career-a3", "hash-a", 3)
	seedCareer(t, f, hidden, "career-hidden", "hash-a", 4)
	seedCareer(t, f, first, "career-b1", "hash-b", 5)
	f.ban("hidden")

	rec := f.get("/v1/systems")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[readapi.SystemsResponse](t, rec)
	want := []readapi.SystemSummary{
		{Hash: "hash-b", SystemID: "B", Name: "Beta", Slug: "beta", HomeBody: "home", Complete: true, Players: 1, Careers: 1},
		{Hash: "hash-a", SystemID: "A", Name: "Alpha", Slug: "alpha", HomeBody: "home", Bodies: 2, Complete: false, Players: 2, Careers: 3},
	}
	if !reflect.DeepEqual(got.Systems, want) {
		t.Fatalf("systems = %+v, want %+v", got.Systems, want)
	}
}

func TestSystemsEmptyListIsJSONArray(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/v1/systems")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"systems\":[]}\n" {
		t.Fatalf("empty response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSystemDetailBySlugAndHashIsCanonicalAndComplete(t *testing.T) {
	f := newFixture(t)
	p := f.player("whiskers")
	seedSystem(t, f, "hash-a", "Sol", "Solar System", "solar-system", 3, 1, 1)
	seedRoot(t, f, "hash-a", "zeta-root", "Zeta Root", 0, 3)
	seedOrbitingBody(t, f, "hash-a")
	seedRoot(t, f, "hash-a", "alpha-root", "Alpha Root", 0, 1)
	seedCareer(t, f, p, "career-one", "hash-a", 1)
	seedCareer(t, f, p, "career-two", "hash-a", 2)

	for _, path := range []string{"/v1/systems/solar-system", "/v1/systems/hash-a"} {
		rec := f.get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		got := decode[readapi.SystemDetail](t, rec)
		if got.Hash != "hash-a" || got.SystemID != "Sol" || got.Name != "Solar System" ||
			got.Slug != "solar-system" || got.HomeBody != "home" || !got.Complete ||
			got.Players != 1 || got.Careers != 2 {
			t.Fatalf("header/counts = %+v", got)
		}
		if !reflect.DeepEqual(got.Roots, []string{"alpha-root", "zeta-root"}) {
			t.Errorf("roots = %v", got.Roots)
		}
		if len(got.Bodies) != 3 || got.Bodies[0].Body != "alpha-root" ||
			got.Bodies[1].Body != "beta-child" || got.Bodies[2].Body != "zeta-root" {
			t.Fatalf("body order = %+v", got.Bodies)
		}
		child := got.Bodies[1]
		if child.Parent == nil || *child.Parent != "alpha-root" || child.SmaM == nil || *child.SmaM != 20 ||
			child.PeriodS == nil || *child.PeriodS != 26 || child.Axis != (readapi.Vector3{X: 17, Y: 18, Z: 19}) ||
			child.CcfToCceT0 != (readapi.Quaternion{X: 27, Y: 28, Z: 29, W: 30}) {
			t.Fatalf("orbiting body = %+v", child)
		}

		var raw struct {
			Bodies []map[string]json.RawMessage `json:"bodies"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"parent", "sma_m", "ecc", "inc_deg", "lan_deg", "argp_deg", "t_pe", "period_s"} {
			if _, exists := raw.Bodies[0][key]; exists {
				t.Errorf("root JSON unexpectedly contains %q", key)
			}
		}
	}
}

func TestSystemLookupPrefersExactHashOverAnotherSystemsSlug(t *testing.T) {
	f := newFixture(t)
	seedSystem(t, f, "hash-a", "A", "Alpha", "hash-b", 0, 1, 1)
	seedSystem(t, f, "hash-b", "B", "Beta", "beta", 0, 1, 2)
	got := decode[readapi.SystemDetail](t, f.get("/v1/systems/hash-b"))
	if got.Hash != "hash-b" || got.Name != "Beta" {
		t.Fatalf("lookup returned %+v", got)
	}
}

func TestUnknownSystemIsCached404(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/v1/systems/unknown")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "not_found" || body.Detail != "system not found" {
		t.Fatalf("body = %+v", body)
	}
}

func TestOrphanSystemBodiesAreHiddenFromBothRoutes(t *testing.T) {
	f := newFixture(t)
	seedRoot(t, f, "orphan-hash", "orphan-root", "Orphan", 0, 1)
	list := decode[readapi.SystemsResponse](t, f.get("/v1/systems"))
	if len(list.Systems) != 0 {
		t.Fatalf("orphan appeared in list: %+v", list.Systems)
	}
	if rec := f.get("/v1/systems/orphan-hash"); rec.Code != http.StatusNotFound {
		t.Fatalf("orphan detail status = %d", rec.Code)
	}
}

func TestSystemProjectionFailureUsesStandardInternalError(t *testing.T) {
	f := newFixture(t, func(deps *readapi.Deps) { deps.Projections = failedProjections{} })
	for _, path := range []string{"/v1/systems", "/v1/systems/sol"} {
		rec := f.get(path)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s status = %d", path, rec.Code)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error != "internal" {
			t.Fatalf("GET %s body = %s", path, rec.Body.String())
		}
	}
}
