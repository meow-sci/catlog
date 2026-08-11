package stats_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

const (
	solHash   = "01kittensol"
	denseHash = "02kittendense"
)

func discovered(hash, id, name string, bodies int, complete bool) input {
	return input{typ: "system.discovered", payload: stats.SystemDiscovered{
		System: hash, ID: id, Name: name, Home: "earth", Bodies: bodies, Complete: complete,
	}}
}

func catalogueBody(hash, body, name string, rank int, parent *string) input {
	return input{typ: "system.body", payload: stats.SystemBody{
		System: hash, Body: body, Name: name, Class: "FutureModClass", Kind: "other",
		Rank: rank, Parent: parent, RadiusM: 1, MassKg: 2, SoiM: 3, AtmoM: 4,
		OceanM: 5, AngVel: 6, Axis: stats.Vec3{X: 0, Y: 1, Z: 0},
		CcfToCceT0: stats.Quat{W: 1},
	}}
}

func strptr(v string) *string     { return &v }
func floatptr(v float64) *float64 { return &v }

func applyChunks(t *testing.T, p *store.Projections, history []input, chunk int, refined bool) {
	t.Helper()
	for start := 0; start < len(history); start += chunk {
		end := min(start+chunk, len(history))
		apply(t, p, history[start:end], int64(start), refined)
	}
}

func dumpSystemTables(t *testing.T, p *store.Projections) ([]string, []string) {
	t.Helper()
	systems := dumpRows(t, p, `SELECT hash, system_id, name, slug, home_body, body_count, reported_complete, first_seq FROM system ORDER BY hash`)
	bodies := dumpRows(t, p, `SELECT hash, body, name, class, kind, rank, coalesce(parent,''), radius_m, mass_kg, soi_m, atmo_m, ocean_m, angvel, axis_x, axis_y, axis_z, coalesce(sma_m,-1), coalesce(ecc,-1), coalesce(inc_deg,-1), coalesce(lan_deg,-1), coalesce(argp_deg,-1), coalesce(t_pe,-1), coalesce(period_s,-1), ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq FROM system_body ORDER BY hash, body`)
	return systems, bodies
}

func dumpRows(t *testing.T, p *store.Projections, query string) []string {
	t.Helper()
	rows, err := p.Reader().QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(cells))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSystemBodyMayPrecedeHeaderAcrossBatches(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{catalogueBody(solHash, "sol", "Sol", 0, nil)}, 0, false)

	if systems, bodies := dumpSystemTables(t, p); len(systems) != 0 || len(bodies) != 1 {
		t.Fatalf("before header: systems=%v bodies=%v", systems, bodies)
	}
	apply(t, p, []input{discovered(solHash, "Sol", "Sol", 1, true)}, 1, false)
	r, ok, err := p.SystemBySlugOrHash(t.Context(), solHash)
	if err != nil || !ok || !r.Complete {
		t.Fatalf("system after header = %+v, %v, %v", r, ok, err)
	}
	var bound string
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT system FROM career WHERE player_id = 1 AND career = ?`, defaultCareer).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != solHash {
		t.Fatalf("career system = %q", bound)
	}
}

func TestSystemFirstWriteAndMonotoneCompletenessAcrossBatches(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{discovered(solHash, "Sol", "Sol", 1, false)}, 0, false)
	wantSystems, _ := dumpSystemTables(t, p)

	apply(t, p, []input{discovered(solHash, "Wrong", "Wrong", 99, true)}, 1, false)
	gotSystems, _ := dumpSystemTables(t, p)
	if !reflect.DeepEqual(gotSystems, wantSystems) {
		t.Fatalf("conflicting header mutated first write:\nwant %v\ngot  %v", wantSystems, gotSystems)
	}

	apply(t, p, []input{discovered(solHash, "Sol", "Sol", 1, true)}, 2, false)
	apply(t, p, []input{discovered(solHash, "Sol", "Sol", 1, false)}, 3, false)
	var complete int
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT reported_complete FROM system WHERE hash = ?`, solHash).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete != 1 {
		t.Fatalf("reported_complete = %d, want monotone 1", complete)
	}
}

func TestSystemBodyDuplicateRetainsFirstByteForByte(t *testing.T) {
	p := testutil.MemProjections(t)
	first := catalogueBody(solHash, "sol", "First", 0, nil)
	apply(t, p, []input{first}, 0, false)
	_, want := dumpSystemTables(t, p)
	different := catalogueBody(solHash, "sol", "Second", 9, strptr("invented"))
	apply(t, p, []input{different}, 1, false)
	_, got := dumpSystemTables(t, p)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body conflict mutated row:\nwant %v\ngot  %v", want, got)
	}
}

func TestSystemBodyGoldenAllColumns(t *testing.T) {
	p := testutil.MemProjections(t)
	body := catalogueBody(solHash, "earth", "Earth", 1, strptr("sol"))
	body.payload = stats.SystemBody{
		System: solHash, Body: "earth", Name: "Earth", Class: "Planet", Kind: "planet",
		Rank: 1, Parent: strptr("sol"), RadiusM: 11, MassKg: 12, SoiM: 13,
		AtmoM: 14, OceanM: 15, AngVel: -16, Axis: stats.Vec3{X: 17, Y: 18, Z: 19},
		SmaM: floatptr(20), Ecc: floatptr(21), IncDeg: floatptr(22),
		LanDeg: floatptr(23), ArgpDeg: floatptr(24), TPe: floatptr(25), PeriodS: floatptr(26),
		CcfToCceT0: stats.Quat{X: 27, Y: 28, Z: 29, W: 30},
	}
	apply(t, p, []input{discovered(solHash, "Sol", "Solar System", 1, true), body}, 0, false)

	system, ok, err := p.SystemBySlugOrHash(t.Context(), "solar-system")
	if err != nil || !ok {
		t.Fatalf("system = %+v, %v, %v", system, ok, err)
	}
	wantSystem := store.SystemRow{
		Hash: solHash, SystemID: "Sol", Name: "Solar System", Slug: "solar-system",
		HomeBody: "earth", BodyCount: 1, Complete: true, FirstSeq: 1,
	}
	if system != wantSystem {
		t.Fatalf("system = %+v, want %+v", system, wantSystem)
	}

	bodies, err := p.SystemBodies(t.Context(), solHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("bodies = %+v", bodies)
	}
	got := bodies[0]
	if got.Hash != solHash || got.Body != "earth" || got.Name != "Earth" ||
		got.Class != "Planet" || got.Kind != "planet" || got.Rank != 1 ||
		!got.Parent.Valid || got.Parent.String != "sol" || got.RadiusM != 11 ||
		got.MassKg != 12 || got.SoiM != 13 || got.AtmoM != 14 || got.OceanM != 15 ||
		got.AngVel != -16 || got.AxisX != 17 || got.AxisY != 18 || got.AxisZ != 19 ||
		!got.SmaM.Valid || got.SmaM.Float64 != 20 || !got.Ecc.Valid || got.Ecc.Float64 != 21 ||
		!got.IncDeg.Valid || got.IncDeg.Float64 != 22 || !got.LanDeg.Valid || got.LanDeg.Float64 != 23 ||
		!got.ArgpDeg.Valid || got.ArgpDeg.Float64 != 24 || !got.TPe.Valid || got.TPe.Float64 != 25 ||
		!got.PeriodS.Valid || got.PeriodS.Float64 != 26 || got.QuatX != 27 || got.QuatY != 28 ||
		got.QuatZ != 29 || got.QuatW != 30 || got.FirstSeq != 2 {
		t.Fatalf("body row = %+v", got)
	}
}

func TestEffectiveCompletenessRequiresEveryDeclaredBody(t *testing.T) {
	p := testutil.MemProjections(t)
	history := []input{
		discovered(solHash, "Sol", "Sol", 2, false),
		discovered(solHash, "Sol", "Sol", 2, true),
		catalogueBody(solHash, "sol", "Sol", 0, nil),
		catalogueBody(solHash, "earth", "Earth", 1, strptr("sol")),
	}
	for i, ev := range history {
		apply(t, p, []input{ev}, int64(i), false)
		r, ok, err := p.SystemBySlugOrHash(t.Context(), solHash)
		if err != nil || !ok {
			t.Fatalf("step %d system: %+v %v %v", i, r, ok, err)
		}
		if r.Complete != (i == len(history)-1) {
			t.Fatalf("step %d complete = %v", i, r.Complete)
		}
	}
}

func TestCareerSystemFirstWinsAndChangeOnlyMarks(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{
		discovered(solHash, "Sol", "Sol", 0, true),
		discovered(denseHash, "Dense", "Dense", 0, true),
	}, 0, false)
	var system string
	var changed int
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT system, system_changed FROM career WHERE player_id = 1 AND career = ?`, defaultCareer).
		Scan(&system, &changed); err != nil {
		t.Fatal(err)
	}
	if system != solHash || changed != 1 {
		t.Fatalf("career binding = %q changed=%d", system, changed)
	}
}

func TestSystemSlugVocabularyAndCollisionOrderAcrossBatchSizes(t *testing.T) {
	history := []input{
		discovered("hash-a", "A", "Solar System (Dense)", 0, true),
		discovered("hash-b", "B", "Solar System (Dense)", 0, true),
		discovered("hash-c", "C", "Solar System (Dense)", 0, true),
	}
	for _, chunk := range []int{1, 2, 100} {
		p := testutil.MemProjections(t)
		applyChunks(t, p, history, chunk, false)
		rows, err := p.Systems(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got := []string{rows[0].Slug, rows[1].Slug, rows[2].Slug}
		want := []string{"solar-system-dense", "solar-system-dense-2", "solar-system-dense-3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chunk %d slugs = %v", chunk, got)
		}
	}
}

func TestDiscoveryBindsScopedRowsWithinSameBatch(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{
		discovered(solHash, "Sol", "Sol", 0, true),
		{typ: "session.started", payload: stats.SessionStarted{}},
	}, 0, false)
	var careerSystem, systemStat string
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT system FROM career_stat WHERE player_id = 1 AND career = ? AND stat = 'play_sessions'`, defaultCareer).Scan(&careerSystem); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT system FROM system_stat WHERE player_id = 1 AND stat = 'play_sessions'`).Scan(&systemStat); err != nil {
		t.Fatal(err)
	}
	if careerSystem != solHash || systemStat != solHash {
		t.Fatalf("scoped systems = %q, %q", careerSystem, systemStat)
	}
}

func TestSystemProjectionIncrementalEqualsRebuildAndReplayIsIdempotent(t *testing.T) {
	history := []input{
		discovered(solHash, "Sol", "Sol", 2, true),
		catalogueBody(solHash, "sol", "Sol", 0, nil),
		catalogueBody(solHash, "earth", "Earth", 1, strptr("sol")),
	}
	incremental := testutil.MemProjections(t)
	applyChunks(t, incremental, history, 1, false)
	wantSystems, wantBodies := dumpSystemTables(t, incremental)
	apply(t, incremental, history, int64(len(history)), false)
	gotSystems, gotBodies := dumpSystemTables(t, incremental)
	if !reflect.DeepEqual(gotSystems, wantSystems) || !reflect.DeepEqual(gotBodies, wantBodies) {
		t.Fatal("replaying an identical survey changed the catalogue tables")
	}

	rebuilt := testutil.MemProjections(t)
	apply(t, rebuilt, history, 0, true)
	rebuiltSystems, rebuiltBodies := dumpSystemTables(t, rebuilt)
	if !reflect.DeepEqual(rebuiltSystems, wantSystems) || !reflect.DeepEqual(rebuiltBodies, wantBodies) {
		t.Fatalf("rebuild differs:\ninc=%v %v\nreb=%v %v", wantSystems, wantBodies, rebuiltSystems, rebuiltBodies)
	}
}

func TestSystemBodyClassAndKindAreOpaque(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{catalogueBody(solHash, "mod-world", "Mod World", 0, nil)}, 0, false)
	var class, kind string
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT class, kind FROM system_body WHERE hash = ? AND body = 'mod-world'`, solHash).Scan(&class, &kind); err != nil {
		t.Fatal(err)
	}
	if class != "FutureModClass" || kind != "other" {
		t.Fatalf("opaque values = %q/%q", class, kind)
	}
}
