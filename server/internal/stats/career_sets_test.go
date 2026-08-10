package stats_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

const (
	testSystem  = "testsystem000001"
	otherSystem = "testsystem000002"
)

func seedCareerSet(t *testing.T, p *store.Projections, career, system string, ordinal int) {
	t.Helper()
	_, err := p.Writer().ExecContext(t.Context(),
		`INSERT INTO career (player_id, career, first_seq, last_seq, ordinal, system)
		 VALUES (1, ?, 0, 0, ?, ?)`, career, ordinal, system)
	if err != nil {
		t.Fatalf("seed career %s: %v", career, err)
	}
}

func readSetScopedValue(t *testing.T, p *store.Projections, table, scope, stat string) (float64, string) {
	t.Helper()
	column := "career"
	if table == "system_stat" {
		column = "system"
	}
	var value float64
	var context string
	q := fmt.Sprintf(`SELECT value, coalesce(context,'') FROM %s WHERE player_id = 1 AND %s = ? AND stat = ?`, table, column)
	if err := p.Reader().QueryRowContext(t.Context(), q, scope, stat).Scan(&value, &context); err != nil {
		t.Fatalf("read %s %s/%s: %v", table, scope, stat, err)
	}
	return value, context
}

func dumpCareerSetTable(t *testing.T, p *store.Projections, table string) []string {
	t.Helper()
	query := `SELECT player_id, career, system, kind, body, first_seq, coalesce(first_sim_t,-1)
		FROM career_body ORDER BY player_id, career, kind, body`
	if table == "career_kitten" {
		query = `SELECT player_id, career, system, kid, name, travelled_m, fastest_ms,
			missions, mission_time_s, kia, updated_seq
			FROM career_kitten ORDER BY player_id, career, kid`
	}
	rows, err := p.Reader().QueryContext(t.Context(), query)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out = append(out, fmt.Sprint(values...))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return out
}

func TestLifetimeBodySetsAreUnchangedByCareerSiblings(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	apply(t, p, []input{
		{career: defaultCareer, flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		{career: defaultCareer, flight: flightN(1), typ: "vehicle.situation", payload: stats.VehicleSituation{To: "landed", Body: "luna"}},
		{career: otherCareer, flight: flightN(2), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		{career: otherCareer, flight: flightN(2), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "mars"}},
		{career: otherCareer, flight: flightN(2), typ: "vehicle.situation", payload: stats.VehicleSituation{To: "landed", Body: "mars"}},
	}, 0, false)

	got := readStats(t, p)
	if got["1/soi_bodies"].Value != 2 || got["1/landed_bodies"].Value != 2 {
		t.Errorf("lifetime body unions = soi %v, landed %v; want 2, 2",
			got["1/soi_bodies"].Value, got["1/landed_bodies"].Value)
	}
}

func TestCareerSetBoardsArePerSave(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	apply(t, p, []input{
		{career: defaultCareer, flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		{career: defaultCareer, flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "mars"}},
		{career: otherCareer, flight: flightN(2), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
	}, 0, false)

	if got := readStats(t, p)["1/soi_bodies"].Value; got != 2 {
		t.Errorf("lifetime soi_bodies = %v, want 2", got)
	}
	if got, _ := readSetScopedValue(t, p, "career_stat", defaultCareer, stats.StatSOIBodies); got != 2 {
		t.Errorf("first save soi_bodies = %v, want 2", got)
	}
	if got, _ := readSetScopedValue(t, p, "career_stat", otherCareer, stats.StatSOIBodies); got != 1 {
		t.Errorf("second save soi_bodies = %v, want 1", got)
	}
	if got, _ := readSetScopedValue(t, p, "system_stat", testSystem, stats.StatSOIBodies); got != 2 {
		t.Errorf("system union soi_bodies = %v, want 2", got)
	}
}

func TestKittenRowsSplitPerSave(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	apply(t, p, []input{
		{career: defaultCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 100}}}},
		{career: otherCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 200}}}},
	}, 0, false)

	var lifetime, scoped int
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM kitten`).Scan(&lifetime); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM career_kitten`).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if lifetime != 1 || scoped != 2 {
		t.Errorf("kitten rows = lifetime %d, per-save %d; want 1, 2", lifetime, scoped)
	}
}

func TestKittenTopsComeFromTheirOwnScope(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	seedCareerSet(t, p, thirdCareer, otherSystem, 3)
	apply(t, p, []input{
		{career: thirdCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "life", Name: "Lifetime", TravelledM: 1000, Missions: 10}}}},
		{career: defaultCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "save", Name: "Save", TravelledM: 800, Missions: 8}}}},
		{career: otherCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "system", Name: "System", TravelledM: 900, Missions: 9}}}},
	}, 0, false)

	player := readStats(t, p)
	for stat, want := range map[string]struct {
		player float64
		career float64
		system float64
	}{
		stats.StatTopKittenDistance: {player: 1000, career: 800, system: 900},
		stats.StatTopKittenMissions: {player: 10, career: 8, system: 9},
	} {
		got := player["1/"+stat]
		wantContext := `{"kitten":"Lifetime"}`
		if got.Value != want.player || got.Context != wantContext {
			t.Errorf("player %s = %v, want %v with %s", stat, got, want.player, wantContext)
		}
		cv, cc := readSetScopedValue(t, p, "career_stat", defaultCareer, stat)
		if cv != want.career || cc != `{"kitten":"Save"}` {
			t.Errorf("career %s = %v %s, want %v Save", stat, cv, cc, want.career)
		}
		sv, sc := readSetScopedValue(t, p, "system_stat", testSystem, stat)
		if sv != want.system || sc != `{"kitten":"System"}` {
			t.Errorf("system %s = %v %s, want %v System", stat, sv, sc, want.system)
		}
	}
}

func TestDistanceTravelledSumsAcrossCareersEvenWhenKidRepeats(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	apply(t, p, []input{
		{career: defaultCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 100}}}},
		{career: otherCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 200}}}},
	}, 0, false)
	if got := readStats(t, p)["1/"+stats.StatDistanceTravelled].Value; got != 300 {
		t.Errorf("distance_travelled = %v, want 300 across the two saves", got)
	}
}

func TestARosterSnapshotWithNoCareerWritesNoCareerKittenRow(t *testing.T) {
	p := testutil.MemProjections(t)
	apply(t, p, []input{{noCareer: true, typ: "roster.snapshot", payload: stats.RosterSnapshot{
		Kittens: []stats.RosterKitten{{Kid: "k1", Name: "Mittens", TravelledM: 10}},
	}}}, 0, false)
	var n int
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM career_kitten`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("career_kitten rows = %d, want 0", n)
	}
}

func TestUnknownSystemWritesNoCareerSetRows(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, "", 1)
	apply(t, p, []input{
		{flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "k1", Name: "Mittens", TravelledM: 10}}}},
	}, 0, false)
	for _, table := range []string{"career_body", "career_kitten"} {
		var n int
		if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s rows = %d, want 0 while system is unknown", table, n)
		}
	}
}

func TestRebuildEqualsIncrementalForTheCareerSetTables(t *testing.T) {
	in := []input{
		{career: defaultCareer, flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}, simT: 50},
		{career: otherCareer, flight: flightN(2), typ: "vehicle.situation", payload: stats.VehicleSituation{To: "landed", Body: "mars"}, simT: 60},
		{career: defaultCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 100}}}},
		{career: otherCareer, typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "same", Name: "Mittens", TravelledM: 200}}}},
	}
	build := func(refined bool) *store.Projections {
		p := testutil.MemProjections(t)
		seedCareerSet(t, p, defaultCareer, testSystem, 1)
		seedCareerSet(t, p, otherCareer, testSystem, 2)
		apply(t, p, in, 0, refined)
		return p
	}
	incremental, rebuilt := build(false), build(true)
	for _, table := range []string{"career_body", "career_kitten"} {
		want, got := dumpCareerSetTable(t, incremental, table), dumpCareerSetTable(t, rebuilt, table)
		if !slices.Equal(want, got) {
			t.Errorf("%s differs: incremental %v, rebuilt %v", table, want, got)
		}
	}
}
