package stats_test

import (
	"database/sql"
	"maps"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The career-time boards (§4.1, docs/events.md): "fastest time from game start
// to X", where game start means the start of one KSA save and the clock is the
// envelope's own sim_t.

// otherCareer is a second save belonging to the same player.
const otherCareer = "testcareer000002"

// thirdCareer is a third save belonging to the same player.
const thirdCareer = "testcareer000003"

// wantRow asserts a stat row exactly, context and seq included — the context is
// where the career a record was set in is recorded, so these boards need it.
func wantRow(t *testing.T, got map[string]row, key string, expect row) {
	t.Helper()
	if got[key] != expect {
		t.Errorf("%s = %v, want %v", key, got[key], expect)
	}
}

// careerRow reads one row of the `career` table.
type careerRow struct {
	maxSimT  float64
	rewound  bool
	firstSeq int64
	lastSeq  int64
	ordinal  int64
}

func readCareers(t *testing.T, proj *store.Projections) map[string]careerRow {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT career, max_sim_t, rewound, first_seq, last_seq, ordinal FROM career WHERE player_id = 1 ORDER BY career`)
	if err != nil {
		t.Fatalf("read career: %v", err)
	}
	defer rows.Close()

	out := map[string]careerRow{}
	for rows.Next() {
		var (
			career  string
			r       careerRow
			rewound int64
		)
		if err := rows.Scan(&career, &r.maxSimT, &rewound, &r.firstSeq, &r.lastSeq, &r.ordinal); err != nil {
			t.Fatalf("scan career: %v", err)
		}
		r.rewound = rewound != 0
		out[career] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read career: %v", err)
	}
	return out
}

type careerOrdinalKey struct {
	player int64
	career string
}

func readCareerOrdinals(t *testing.T, proj *store.Projections) map[careerOrdinalKey]int64 {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, career, ordinal FROM career ORDER BY player_id, first_seq`)
	if err != nil {
		t.Fatalf("read career ordinals: %v", err)
	}
	defer rows.Close()

	out := map[careerOrdinalKey]int64{}
	for rows.Next() {
		var key careerOrdinalKey
		var ordinal int64
		if err := rows.Scan(&key.player, &key.career, &ordinal); err != nil {
			t.Fatalf("scan career ordinal: %v", err)
		}
		out[key] = ordinal
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read career ordinals: %v", err)
	}
	return out
}

// --- fastest_to_orbit ---------------------------------------------------------

func TestFastestToOrbitTakesTheEarliestAchievedOrbit(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 0},
		{flight: f, typ: "flight.started", payload: stats.FlightStarted{VehicleName: "A", Body: "earth", CrewCount: 1}, simT: 10},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "escaped", Body: "earth"}, simT: 120},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth", ApM: 320000, PeM: 295000}, simT: 190},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "luna"}, simT: 90000},
	})
	if got["1/fastest_to_orbit"].Value != 190_000 {
		t.Errorf("fastest_to_orbit = %v, want 190000 ms — the first orbit of the career, not the last",
			got["1/fastest_to_orbit"].Value)
	}
	wantRow(t, got, "1/fastest_to_orbit", row{
		Value:   190_000,
		Seq:     4,
		Context: `{"body":"earth","career":"testcareer000001","flight":"` + ids.String(f) + `"}`,
	})
}

// The minimum is taken across the player's careers, because a leaderboard wants
// the player's best run. Which career it came from is in the context.
func TestFastestToOrbitTakesTheBestCareer(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	got := fold(t, []input{
		{flight: f1, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 4000},
		{flight: f2, career: otherCareer, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 260},
	})
	wantRow(t, got, "1/fastest_to_orbit", row{
		Value:   260_000,
		Seq:     2,
		Context: `{"body":"earth","career":"` + otherCareer + `","flight":"` + ids.String(f2) + `"}`,
	})
}

// A slower run must not overwrite a faster one, and an equal one must not steal
// the rank — the tie rule is the same as every record board's, just mirrored.
func TestFastestToOrbitKeepsTheEarliestClaimOnATie(t *testing.T) {
	f1, f2, f3 := flightN(1), flightN(2), flightN(3)
	got := fold(t, []input{
		{flight: f1, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 300},
		{flight: f2, career: otherCareer, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 900},
		{flight: f3, career: otherCareer, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 300},
	})
	if got["1/fastest_to_orbit"].Value != 300_000 || got["1/fastest_to_orbit"].Seq != 1 {
		t.Errorf("fastest_to_orbit = %v at seq %d, want 300000 ms at seq 1 — an equal time keeps the earlier claim",
			got["1/fastest_to_orbit"].Value, got["1/fastest_to_orbit"].Seq)
	}
}

func TestFastestToOrbitIgnoresFlaggedFlightsAndMissingClocks(t *testing.T) {
	flagged, noClock, noCareer := flightN(1), flightN(2), flightN(3)
	got := fold(t, []input{
		// A teleport to orbit is not a fast ascent.
		{flight: flagged, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}, simT: 5},
		{flight: flagged, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 6},
		// Absent is not zero: a missing clock reading scored as 0 would be an
		// unbeatable record.
		{flight: noClock, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, noSimT: true},
		// No career means sim_t has no origin, so it is not a career time.
		{flight: noCareer, noCareer: true, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 7},
	})
	if _, on := got["1/fastest_to_orbit"]; on {
		t.Errorf("fastest_to_orbit = %v, want the board empty", got["1/fastest_to_orbit"])
	}
	// The unflagged orbits still counted towards the plain counter, which proves
	// the exclusions above are specific to the career-time board.
	if got["1/orbits_achieved"].Value != 2 {
		t.Errorf("orbits_achieved = %v, want 2", got["1/orbits_achieved"].Value)
	}
}

// --- fastest_to_<body> --------------------------------------------------------

func TestFastestToBodyScoresWhateverBodyTheEventNamed(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "earth", ToBody: "luna"}, simT: 300_000},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "luna", ToBody: "sol"}, simT: 400_000},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "sol", ToBody: "mars"}, simT: 21_000_000},
		// A second, later arrival at a body already reached must not replace the
		// first — the board is "how long it took you", not "when you last went".
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "mars", ToBody: "luna"}, simT: 40_000_000},
	}, 0, false)

	got := readStats(t, proj)
	for stat, wantValue := range map[string]float64{
		"1/fastest_to_luna": 300_000_000,
		"1/fastest_to_sol":  400_000_000,
		"1/fastest_to_mars": 21_000_000_000, // ms
		"1/soi_bodies":      3,
	} {
		if got[stat].Value != wantValue {
			t.Errorf("%s = %v, want %v", stat, got[stat].Value, wantValue)
		}
	}

	// The arrival times are also written to player_body, for every body,
	// including any whose name could not be a stat key at all.
	var lunaFirst sql.NullFloat64
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT first_sim_t FROM player_body WHERE player_id = 1 AND kind = 'soi' AND body = 'luna'`).
		Scan(&lunaFirst); err != nil {
		t.Fatal(err)
	}
	if !lunaFirst.Valid || lunaFirst.Float64 != 300_000 {
		t.Errorf("player_body.first_sim_t for luna = %v, want 300000", lunaFirst)
	}
}

// A body nobody has ever typed into this repository gets a board, because the
// board comes from the data. This is the whole point of the family: KSA's system
// is content, and a compiled-in list would silently deny a board to whoever got
// somewhere new first.
func TestFastestToBodyMintsABoardForABodyNoListMentions(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "earth", ToBody: "zephyria_prime"}, simT: 500},
	}, 0, false)

	got := readStats(t, proj)
	if got["1/fastest_to_zephyria_prime"].Value != 500_000 {
		t.Errorf("fastest_to_zephyria_prime = %v, want 500000 ms — a body is a board because the data said so",
			got["1/fastest_to_zephyria_prime"].Value)
	}
	if got["1/soi_bodies"].Value != 1 {
		t.Errorf("soi_bodies = %v, want 1", got["1/soi_bodies"].Value)
	}

	// And its metadata is derived from the key, with no lookup table anywhere.
	b, ok := stats.Describe("fastest_to_zephyria_prime")
	if !ok || b.Title != "Fastest to Zephyria Prime" || b.Unit != "ms" || !b.Ascending || !b.Career {
		t.Errorf("Describe = %+v (ok=%v), want an ascending career board titled from the key", b, ok)
	}
}

// A name that cannot be half of a stat key keeps every other consequence it had
// — the visit, the count, the arrival time — and simply gets no board. That is
// protocol hygiene (a stat key is a URL path segment), not a claim about which
// bodies exist.
func TestFastestToBodySkipsANameThatCannotBeAStatKey(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "earth", ToBody: "planet x/../../etc"}, simT: 500},
	}, 0, false)

	got := readStats(t, proj)
	for stat := range got {
		if stat != "1/soi_bodies" {
			t.Errorf("a malformed body name produced the stat %q", stat)
		}
	}
	if got["1/soi_bodies"].Value != 1 {
		t.Errorf("soi_bodies = %v, want 1 — the visit still happened", got["1/soi_bodies"].Value)
	}
	var first sql.NullFloat64
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT first_sim_t FROM player_body WHERE player_id = 1 AND kind = 'soi' AND body = 'planet x/../../etc'`).
		Scan(&first); err != nil {
		t.Fatal(err)
	}
	if !first.Valid || first.Float64 != 500 {
		t.Errorf("first_sim_t for an unkeyable body = %v, want it recorded anyway", first)
	}
}

// A body literally called "orbit" must not land on `fastest_to_orbit`: a fixed
// board's key is reserved, or two different questions would share one row.
func TestFastestToBodyNeverCollidesWithAFixedBoard(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "earth", ToBody: "orbit"}, simT: 500},
	}, 0, false)

	if got := readStats(t, proj); got["1/fastest_to_orbit"].Value != 0 {
		t.Errorf("a body named %q scored on fastest_to_orbit: %v", "orbit", got["1/fastest_to_orbit"])
	}
}

func TestBoardDirectionAndUnitsAreDerivedFromTheKey(t *testing.T) {
	for stat, want := range map[string]stats.Board{
		"fastest_to_orbit": {Stat: "fastest_to_orbit", Title: "Fastest to Orbit", Unit: "ms", Ascending: true, Career: true},
		"fastest_to_luna":  {Stat: "fastest_to_luna", Title: "Fastest to Luna", Unit: "ms", Ascending: true, Career: true},
		"fastest_to_kerbin_ii": {
			Stat: "fastest_to_kerbin_ii", Title: "Fastest to Kerbin Ii", Unit: "ms", Ascending: true, Career: true,
		},
		"rud_total":         {Stat: "rud_total", Title: "Rapid Unscheduled Disassemblies", Unit: "RUDs"},
		"rud_ground_impact": {Stat: "rud_ground_impact", Title: "RUDs — Ground Impact", Unit: "RUDs"},
		"rud_kraken":        {Stat: "rud_kraken", Title: "RUDs — Kraken", Unit: "RUDs"},
	} {
		got, ok := stats.Describe(stat)
		if !ok || got != want {
			t.Errorf("Describe(%q) = %+v (ok=%v), want %+v", stat, got, ok, want)
		}
	}
	for _, stat := range []string{"not_a_board", "fastest_to_", "fastest_to_Luna", "rud_a b", "fastest_to__x"} {
		if b, ok := stats.Describe(stat); ok {
			t.Errorf("Describe(%q) = %+v, want it rejected", stat, b)
		}
	}
}

// --- careers and the rewind mark ----------------------------------------------

func TestCareerOrdinalsAreAssignedInFirstSeenOrder(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{career: otherCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 10},
		{player: bob, career: thirdCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 20},
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 30},
		{career: otherCareer, flight: flightN(1), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 40},
		{career: thirdCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 50},
	}, 0, false)

	got := readCareerOrdinals(t, proj)
	want := map[careerOrdinalKey]int64{
		{player: alice, career: otherCareer}:   1,
		{player: alice, career: defaultCareer}: 2,
		{player: alice, career: thirdCareer}:   3,
		{player: bob, career: thirdCareer}:     1,
	}
	if !maps.Equal(got, want) {
		t.Errorf("career ordinals = %v, want %v", got, want)
	}
}

func TestCareerOrdinalSurvivesLaterEvents(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 10},
		{career: otherCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 20},
	}, 0, false)
	apply(t, proj, []input{
		{career: otherCareer, flight: flightN(1), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 30},
		{flight: flightN(2), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 40},
	}, 2, false)

	got := readCareerOrdinals(t, proj)
	if got[careerOrdinalKey{player: alice, career: defaultCareer}] != 1 {
		t.Errorf("first career ordinal = %d, want 1", got[careerOrdinalKey{player: alice, career: defaultCareer}])
	}
	if got[careerOrdinalKey{player: alice, career: otherCareer}] != 2 {
		t.Errorf("second career ordinal = %d, want 2", got[careerOrdinalKey{player: alice, career: otherCareer}])
	}
}

func TestBatchSizeDoesNotChangeOrdinals(t *testing.T) {
	in := []input{
		{career: otherCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 10},
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 20},
		{career: otherCareer, flight: flightN(1), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 30},
		{career: thirdCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 40},
		{flight: flightN(2), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 50},
	}

	var want map[careerOrdinalKey]int64
	for _, batchSize := range []int{1, 2, 1000} {
		proj := testutil.MemProjections(t)
		for start := 0; start < len(in); start += batchSize {
			end := min(start+batchSize, len(in))
			apply(t, proj, in[start:end], int64(start), false)
		}
		got := readCareerOrdinals(t, proj)
		if want == nil {
			want = got
			continue
		}
		if !maps.Equal(got, want) {
			t.Errorf("batch size %d ordinals = %v, want %v", batchSize, got, want)
		}
	}
}

func TestRebuildReproducesOrdinals(t *testing.T) {
	in := []input{
		{career: otherCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 10},
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 20},
		{career: otherCareer, flight: flightN(1), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 30},
		{career: thirdCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 40},
	}

	incremental := testutil.MemProjections(t)
	apply(t, incremental, in, 0, false)
	rebuilt := testutil.MemProjections(t)
	apply(t, rebuilt, in, 0, true)

	want := readCareerOrdinals(t, incremental)
	if got := readCareerOrdinals(t, rebuilt); !maps.Equal(got, want) {
		t.Errorf("rebuilt career ordinals = %v, want %v", got, want)
	}
}

func TestCareerTracksItsHighWaterMark(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 0},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 400},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}, simT: 120},
		{flight: f, career: otherCareer, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 9},
	}, 0, false)

	careers := readCareers(t, proj)
	if got := careers[defaultCareer]; got.maxSimT != 400 || got.rewound {
		t.Errorf("career %s = %+v, want max_sim_t 400 and no mark", defaultCareer, got)
	}
	if got := careers[otherCareer]; got.maxSimT != 9 || got.rewound {
		t.Errorf("career %s = %+v, want max_sim_t 9 and no mark", otherCareer, got)
	}
}

func TestCareerTracksEveryAttributedEventAsActivity(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 100},
		// Flagged activity does not score, but it is still activity in this save.
		{flight: flightN(1), typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}, noSimT: true},
	}, 0, false)

	got := readCareers(t, proj)[defaultCareer]
	if got.firstSeq != 1 || got.lastSeq != 2 {
		t.Errorf("career seq range = %d..%d, want 1..2", got.firstSeq, got.lastSeq)
	}
	if got.maxSimT != 100 {
		t.Errorf("max_sim_t = %v, want 100 — a clockless event is activity, not a clock reading", got.maxSimT)
	}
	if got := readStats(t, proj); len(got) != 0 {
		t.Errorf("the activity fixture unexpectedly scored: %v", got)
	}
}

func TestCareerLastSeqIsStableAcrossBatchBoundaries(t *testing.T) {
	in := []input{
		{flight: flightN(1), typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 500},
		// A lower clock must still advance last_seq, while max_sim_t stays put.
		{flight: flightN(1), typ: "telemetry.window", payload: stats.TelemetryWindow{T0Sim: 400, T1Sim: 450, Body: "earth"}, simT: 450},
		{flight: flightN(1), typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "console"}, noSimT: true},
	}

	oneBatch := testutil.MemProjections(t)
	apply(t, oneBatch, in, 0, false)

	separateBatches := testutil.MemProjections(t)
	for i := range in {
		apply(t, separateBatches, in[i:i+1], int64(i), false)
	}

	want := readCareers(t, oneBatch)[defaultCareer]
	got := readCareers(t, separateBatches)[defaultCareer]
	if got != want {
		t.Errorf("career differs across batch boundaries: got %+v, want %+v", got, want)
	}
	if got.firstSeq != 1 || got.lastSeq != 3 || got.maxSimT != 500 {
		t.Errorf("career = %+v, want first_seq 1, last_seq 3, max_sim_t 500", got)
	}
}

// The rewind rule, in full: a session.started whose sim_t is below the career's
// high-water mark means an earlier save of that career was loaded.
func TestBackwardsClockMarksTheCareerRewound(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 0},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 900},
		// The player quits and loads the same save again, from later on: forward,
		// so no mark.
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 1200},
		// Now they load an earlier save of the same career, and beat their time.
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 300},
		{flight: flightN(2), typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 480},
	}, 0, false)

	careers := readCareers(t, proj)
	if !careers[defaultCareer].rewound {
		t.Error("a session.started below the career's high-water mark must mark the career rewound")
	}
	if careers[defaultCareer].maxSimT != 1200 {
		t.Errorf("max_sim_t = %v, want 1200 — the mark must not rewind the high-water mark",
			careers[defaultCareer].maxSimT)
	}

	// The mark changes nothing about the score. This is the honest limitation
	// stated in docs/events.md: catlog cannot tell save-scumming from ordinary
	// reloading, and does not try. The faster time stands, and the career says
	// its clock went backwards.
	got := readStats(t, proj)
	if got["1/fastest_to_orbit"].Value != 480_000 {
		t.Errorf("fastest_to_orbit = %v, want 480000 ms — the mark must not exclude the run",
			got["1/fastest_to_orbit"].Value)
	}
}

// Two saves interleaved in one log is the ordinary case the mark must never fire
// on: an honest player with two careers is not rewinding either of them.
func TestTwoCareersInterleavedAreNotARewind(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 5000},
		{flight: flightN(1), typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 5100},
		// Loads a completely different save, whose clock is much earlier.
		{career: otherCareer, typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 40},
		{flight: flightN(2), career: otherCareer, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 220},
		// And back to the first one, from where they left it.
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 5200},
	}, 0, false)

	careers := readCareers(t, proj)
	for career, got := range careers {
		if got.rewound {
			t.Errorf("career %s was marked rewound; switching saves is not a rewind", career)
		}
	}
	if got := readStats(t, proj)["1/fastest_to_orbit"].Value; got != 220_000 {
		t.Errorf("fastest_to_orbit = %v, want 220000 ms — the min is across careers", got)
	}
}

// A clock that dips *within* a session is emission order, not a rewind: a
// telemetry window closes with the sim time of its end, and Flush drains pending
// impacts after the frame loop has stopped. Only session.started can mark.
func TestBackwardsClockWithinASessionIsNotARewind(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 0},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 600},
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{T0Sim: 540, T1Sim: 570, Body: "earth"}, simT: 570},
	}, 0, false)

	if readCareers(t, proj)[defaultCareer].rewound {
		t.Error("an out-of-order event inside one session must not mark the career")
	}
}

// A career-less event still folds; it just contributes nothing to a career.
func TestEventsWithoutACareerDoNotCreateOne(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: flightN(1), noCareer: true, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}, simT: 5},
	}, 0, false)

	if got := readCareers(t, proj); len(got) != 0 {
		t.Errorf("careers = %v, want none", got)
	}
	if got := readStats(t, proj)["1/stagings"].Value; got != 1 {
		t.Errorf("stagings = %v, want 1 — a career-less event still folds everywhere else", got)
	}
}

// A rebuild reproduces careers and their marks exactly: the state folds run
// alone on the first pass, so the second pass scores against a complete table.
func TestRebuildReproducesCareersAndMarks(t *testing.T) {
	f := flightN(1)
	in := []input{
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 0},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, simT: 800},
		{typ: "session.started", payload: stats.SessionStarted{ModVer: "0.1.0"}, simT: 90},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "earth", ToBody: "luna"}, simT: 500},
	}

	incremental := testutil.MemProjections(t)
	apply(t, incremental, in, 0, false)
	rebuilt := testutil.MemProjections(t)
	apply(t, rebuilt, in, 0, true)

	a, b := readCareers(t, incremental), readCareers(t, rebuilt)
	if len(a) != len(b) || a[defaultCareer] != b[defaultCareer] {
		t.Errorf("careers differ: incremental %v, rebuilt %v", a, b)
	}
	if !a[defaultCareer].rewound {
		t.Error("the rewind must survive a rebuild")
	}
	sa, sb := readStats(t, incremental), readStats(t, rebuilt)
	for _, stat := range []string{"1/fastest_to_orbit", "1/fastest_to_luna"} {
		if sa[stat] != sb[stat] {
			t.Errorf("%s: incremental %v, rebuilt %v", stat, sa[stat], sb[stat])
		}
	}
}
