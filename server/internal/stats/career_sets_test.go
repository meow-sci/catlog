package stats_test

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

type setScopeRow struct {
	value   float64
	context string
	seq     int64
}

func readSetScopeRows(t *testing.T, p *store.Projections, table, scopeColumn string) map[string]setScopeRow {
	t.Helper()
	rows, err := p.Reader().QueryContext(t.Context(), fmt.Sprintf(
		`SELECT %s, stat, value, context, updated_seq FROM %s WHERE player_id = 1 ORDER BY %s, stat`,
		scopeColumn, table, scopeColumn))
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]setScopeRow{}
	for rows.Next() {
		var scope, stat string
		var value float64
		var context sql.NullString
		var seq int64
		if err := rows.Scan(&scope, &stat, &value, &context, &seq); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out[scope+"/"+stat] = setScopeRow{value: value, context: context.String, seq: seq}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s rows: %v", table, err)
	}
	return out
}

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

func TestUnknownSystemKeepsSprintCareerFactsButNoSystemRows(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, "", 1)
	apply(t, p, []input{
		{flight: flightN(1), typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{{Kid: "k1", Name: "Mittens", TravelledM: 10}}}},
	}, 0, false)
	var bodies, kittens int
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM career_body`).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM career_kitten`).Scan(&kittens); err != nil {
		t.Fatal(err)
	}
	if bodies != 1 || kittens != 0 {
		t.Errorf("unknown-system career sets = bodies %d, kittens %d; want sprint SOI 1, roster 0", bodies, kittens)
	}
	player := readStats(t, p)
	career := readSetScopeRows(t, p, "career_stat", "career")
	systems := readSetScopeRows(t, p, "system_stat", "system")
	for _, stat := range []string{stats.StatBodiesBy1Y, stats.StatBodiesBy10Y} {
		if got := player["1/"+stat].Value; got != 1 {
			t.Errorf("unknown-system player %s = %v, want 1", stat, got)
		}
		if got := career[defaultCareer+"/"+stat].value; got != 1 {
			t.Errorf("unknown-system career %s = %v, want 1", stat, got)
		}
		for key := range systems {
			if strings.HasSuffix(key, "/"+stat) {
				t.Errorf("unknown system wrote %s row %q", stat, key)
			}
		}
	}
}

func TestBodySprintsUseBestSaveAndInclusiveThresholds(t *testing.T) {
	const unknownCareer = "testcareer000004"
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	seedCareerSet(t, p, thirdCareer, otherSystem, 3)
	seedCareerSet(t, p, unknownCareer, "", 4)

	year := float64(stats.SprintYearSeconds)
	tenYears := 10 * year
	cleanA, cleanB, cleanC := flightN(40), flightN(41), flightN(42)
	flagged := flightN(43)
	apply(t, p, []input{
		// Exact boundaries are included; just-over values are excluded from that
		// sprint while remaining eligible for a later threshold.
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: year, payload: stats.VehicleSOI{ToBody: "boundary-1y"}},
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: year + 1, payload: stats.VehicleSOI{ToBody: "over-1y"}},
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: tenYears, payload: stats.VehicleSOI{ToBody: "boundary-10y"}},
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: tenYears + 1, payload: stats.VehicleSOI{ToBody: "over-10y"}},
		// An unkeyable opaque body still belongs to the save-backed set even
		// though it cannot form a fastest_to_<body> stat key.
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: 10, payload: stats.VehicleSOI{ToBody: "bad/body"}},
		// This body starts just outside one year and crosses the boundary on a
		// later replay of an earlier save clock.
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: year + 2, payload: stats.VehicleSOI{ToBody: "rewound"}},
		// Empty destination is not an entered SOI; absent and negative clocks are
		// not silently interpreted as zero.
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: 1, payload: stats.VehicleSOI{}},
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", noSimT: true, payload: stats.VehicleSOI{ToBody: "no-clock"}},
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: -1, payload: stats.VehicleSOI{ToBody: "negative-clock"}},
		{career: defaultCareer, flight: flagged, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}},
		{career: defaultCareer, flight: flagged, typ: "vehicle.soi", simT: 1, payload: stats.VehicleSOI{ToBody: "flagged"}},
	}, 0, false)

	// A second transaction exercises read-through from flushed career_body and
	// proves a repeated body lowers first_sim_t across the inclusive boundary.
	apply(t, p, []input{
		{career: defaultCareer, flight: cleanA, typ: "vehicle.soi", simT: year - 1, payload: stats.VehicleSOI{ToBody: "rewound"}},
		// Same body in another save is distinct there. The first system's answer
		// is the best one save (3), never a five-body union across these careers.
		{career: otherCareer, flight: cleanB, typ: "vehicle.soi", simT: 1, payload: stats.VehicleSOI{ToBody: "boundary-1y"}},
		{career: otherCareer, flight: cleanB, typ: "vehicle.soi", simT: 2, payload: stats.VehicleSOI{ToBody: "b-two"}},
		{career: otherCareer, flight: cleanB, typ: "vehicle.soi", simT: 3, payload: stats.VehicleSOI{ToBody: "b-three"}},
		// A different system has the best one-year save and therefore represents
		// the player lifetime maximum without merging system sets.
		{career: thirdCareer, flight: cleanC, typ: "vehicle.soi", simT: 1, payload: stats.VehicleSOI{ToBody: "c-one"}},
		{career: thirdCareer, flight: cleanC, typ: "vehicle.soi", simT: 2, payload: stats.VehicleSOI{ToBody: "c-two"}},
		{career: thirdCareer, flight: cleanC, typ: "vehicle.soi", simT: 3, payload: stats.VehicleSOI{ToBody: "c-three"}},
		{career: thirdCareer, flight: cleanC, typ: "vehicle.soi", simT: 4, payload: stats.VehicleSOI{ToBody: "c-four"}},
		// Unknown system still advances its save and lifetime candidates, but
		// cannot create a comparable system row.
		{career: unknownCareer, flight: flightN(44), typ: "vehicle.soi", simT: 1, payload: stats.VehicleSOI{ToBody: "u-one"}},
		{career: unknownCareer, flight: flightN(44), typ: "vehicle.soi", simT: 2, payload: stats.VehicleSOI{ToBody: "u-two"}},
	}, 11, false)

	player := readStats(t, p)
	careerRows := readSetScopeRows(t, p, "career_stat", "career")
	systemRows := readSetScopeRows(t, p, "system_stat", "system")
	for stat, want := range map[string]struct {
		player, careerA, careerB, careerC, unknown, systemA, systemC float64
		playerSeq                                                    int64
	}{
		stats.StatBodiesBy1Y:  {4, 3, 3, 4, 2, 3, 4, 19},
		stats.StatBodiesBy10Y: {5, 5, 3, 4, 2, 5, 4, 6},
	} {
		if got := player["1/"+stat]; got.Value != want.player || got.Context != "" || got.Seq != want.playerSeq {
			t.Errorf("player %s = %+v, want %v with NULL context at seq %d", stat, got, want.player, want.playerSeq)
		}
		for career, value := range map[string]float64{
			defaultCareer: want.careerA, otherCareer: want.careerB,
			thirdCareer: want.careerC, unknownCareer: want.unknown,
		} {
			if got := careerRows[career+"/"+stat]; got.value != value || got.context != "" {
				t.Errorf("career %s %s = %+v, want %v with NULL context", career, stat, got, value)
			}
		}
		if got := systemRows[testSystem+"/"+stat]; got.value != want.systemA || got.context != "" {
			t.Errorf("system A %s = %+v, want %v with NULL context", stat, got, want.systemA)
		}
		if got := systemRows[otherSystem+"/"+stat]; got.value != want.systemC || got.context != "" {
			t.Errorf("system C %s = %+v, want %v with NULL context", stat, got, want.systemC)
		}
		var systemCount int
		for key := range systemRows {
			if strings.HasSuffix(key, "/"+stat) {
				systemCount++
			}
		}
		if systemCount != 2 {
			t.Errorf("%s system rows = %d in %v, want only known systems", stat, systemCount, systemRows)
		}
	}

	for _, table := range []string{"player_stat", "career_stat", "system_stat"} {
		var nonNull int
		query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE stat IN (?, ?) AND context IS NOT NULL`, table)
		if err := p.Reader().QueryRowContext(t.Context(), query, stats.StatBodiesBy1Y, stats.StatBodiesBy10Y).Scan(&nonNull); err != nil {
			t.Fatalf("read %s sprint contexts: %v", table, err)
		}
		if nonNull != 0 {
			t.Errorf("%s has %d non-NULL sprint contexts, want zero", table, nonNull)
		}
	}
}

func TestKittensToOrbitAndBackRequiresEveryPredicate(t *testing.T) {
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	valid, noOrbit, destroyed := flightN(20), flightN(21), flightN(22)
	empty, flagged, wrongPhase, outOfOrder := flightN(23), flightN(24), flightN(25), flightN(26)
	apply(t, p, []input{
		{career: defaultCareer, flight: valid, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		// Duplicate payload entries are iterated verbatim and collapse only at
		// the career_body primary key.
		{career: defaultCareer, flight: valid, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"first", "first", "second"}}},
		{career: defaultCareer, flight: noOrbit, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"no-orbit"}}},
		{career: defaultCareer, flight: destroyed, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: defaultCareer, flight: destroyed, typ: "flight.ended", payload: stats.FlightEnded{Reason: "destroyed", Kids: []string{"destroyed"}}},
		{career: defaultCareer, flight: empty, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: defaultCareer, flight: empty, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered"}},
		{career: defaultCareer, flight: flagged, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}},
		{career: defaultCareer, flight: flagged, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: defaultCareer, flight: flagged, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"flagged"}}},
		{career: defaultCareer, flight: wrongPhase, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "lost"}},
		{career: defaultCareer, flight: wrongPhase, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"wrong-phase"}}},
		// A later orbit fact cannot retro-award a recovery already processed.
		{career: defaultCareer, flight: outOfOrder, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"too-early"}}},
		{career: defaultCareer, flight: outOfOrder, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
	}, 0, false)

	player := readStats(t, p)["1/"+stats.StatKittensToOrbitAndBack]
	if player.Value != 2 || player.Context != "" || player.Seq != 2 {
		t.Errorf("player orbit kittens = %+v, want value 2, NULL context, seq 2", player)
	}
	career := readSetScopeRows(t, p, "career_stat", "career")[defaultCareer+"/"+stats.StatKittensToOrbitAndBack]
	system := readSetScopeRows(t, p, "system_stat", "system")[testSystem+"/"+stats.StatKittensToOrbitAndBack]
	for label, row := range map[string]setScopeRow{"career": career, "system": system} {
		if row.value != 2 || row.context != "" || row.seq != 2 {
			t.Errorf("%s orbit kittens = %+v, want value 2, NULL context, seq 2", label, row)
		}
	}
	var careerMembers, lifetimeMembers int
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM career_body WHERE kind = 'orbit_kid'`).Scan(&careerMembers); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM player_body WHERE kind = 'orbit_kid'`).Scan(&lifetimeMembers); err != nil {
		t.Fatal(err)
	}
	if careerMembers != 2 || lifetimeMembers != 0 {
		t.Errorf("orbit_kid rows = career %d, player %d; want 2, 0", careerMembers, lifetimeMembers)
	}
}

func TestKittensToOrbitAndBackCountsCareerRowsIndependentlyByScope(t *testing.T) {
	const unknownCareer = "testcareer000004"
	p := testutil.MemProjections(t)
	seedCareerSet(t, p, defaultCareer, testSystem, 1)
	seedCareerSet(t, p, otherCareer, testSystem, 2)
	seedCareerSet(t, p, thirdCareer, otherSystem, 3)
	seedCareerSet(t, p, unknownCareer, "", 4)

	first, second, third, unknown := flightN(30), flightN(31), flightN(32), flightN(33)
	apply(t, p, []input{
		{career: defaultCareer, flight: first, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: defaultCareer, flight: first, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"same", "alpha", "alpha"}}},
		{career: otherCareer, flight: second, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: otherCareer, flight: second, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"same", "beta"}}},
		{career: thirdCareer, flight: third, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: thirdCareer, flight: third, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"same"}}},
	}, 0, false)

	// A separate batch forces the backing set and prior stat values through SQL
	// before the unknown-system save extends the lifetime and career totals.
	apply(t, p, []input{
		{career: unknownCareer, flight: unknown, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}},
		{career: unknownCareer, flight: unknown, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", Kids: []string{"unknown-system"}}},
	}, 6, false)

	player := readStats(t, p)["1/"+stats.StatKittensToOrbitAndBack]
	if player.Value != 6 || player.Context != "" || player.Seq != 8 {
		t.Errorf("lifetime orbit kittens = %+v, want 6 with NULL context at seq 8", player)
	}
	careerRows := readSetScopeRows(t, p, "career_stat", "career")
	for career, want := range map[string]float64{
		defaultCareer: 2, otherCareer: 2, thirdCareer: 1, unknownCareer: 1,
	} {
		row := careerRows[career+"/"+stats.StatKittensToOrbitAndBack]
		if row.value != want || row.context != "" {
			t.Errorf("career %s orbit kittens = %+v, want %v with NULL context", career, row, want)
		}
	}
	systemRows := readSetScopeRows(t, p, "system_stat", "system")
	if row := systemRows[testSystem+"/"+stats.StatKittensToOrbitAndBack]; row.value != 4 || row.context != "" || row.seq != 4 {
		t.Errorf("same-system orbit kittens = %+v, want 4 with NULL context at seq 4", row)
	}
	if row := systemRows[otherSystem+"/"+stats.StatKittensToOrbitAndBack]; row.value != 1 || row.context != "" || row.seq != 6 {
		t.Errorf("other-system orbit kittens = %+v, want 1 with NULL context at seq 6", row)
	}
	var orbitSystemRows int
	for key := range systemRows {
		if strings.HasSuffix(key, "/"+stats.StatKittensToOrbitAndBack) {
			orbitSystemRows++
		}
	}
	if orbitSystemRows != 2 {
		t.Errorf("orbit-kitten system rows = %d in %v, want only the two known systems", orbitSystemRows, systemRows)
	}

	var members, repeated, playerMembers int
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM career_body WHERE kind = 'orbit_kid'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM career_body WHERE kind = 'orbit_kid' AND body = 'same'`).Scan(&repeated); err != nil {
		t.Fatal(err)
	}
	if err := p.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM player_body WHERE kind = 'orbit_kid'`).Scan(&playerMembers); err != nil {
		t.Fatal(err)
	}
	if members != 6 || repeated != 3 || playerMembers != 0 {
		t.Errorf("set rows = total %d, repeated kid %d, player rows %d; want 6, 3, 0", members, repeated, playerMembers)
	}
	for _, table := range []string{"player_stat", "career_stat", "system_stat"} {
		var nonNull int
		query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE stat = ? AND context IS NOT NULL`, table)
		if err := p.Reader().QueryRowContext(t.Context(), query, stats.StatKittensToOrbitAndBack).Scan(&nonNull); err != nil {
			t.Fatalf("read %s contexts: %v", table, err)
		}
		if nonNull != 0 {
			t.Errorf("%s has %d non-NULL orbit-kitten contexts, want zero", table, nonNull)
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
