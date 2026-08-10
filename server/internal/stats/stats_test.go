package stats_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The golden tests below all have the same shape: a sequence of §4.2 events in,
// the exact `player_stat` rows out. They go through [stats.Decode] rather than
// building a stats.Event literal, so the payload structs and their JSON tags are
// covered too — a renamed tag would otherwise silently stop a board scoring.

const (
	alice int64 = 1
	bob   int64 = 2
)

// input is one event in a golden sequence.
type input struct {
	player int64
	flight ids.ID // ids.Zero for session/roster events
	typ    string
	// career is the §4.1 career id. Empty means [defaultCareer]; the one test
	// that needs "no career at all" says so with noCareer.
	career   string
	noCareer bool
	payload  any
	simT     float64
	noSimT   bool
	// recvMS overrides the server receive stamp. Zero means the default ladder.
	// The rolling-window tests set it because a bucket is a function of exactly
	// this value and of nothing else.
	recvMS int64
}

// defaultCareer is the career every golden event belongs to unless it says
// otherwise. Sixteen lowercase Crockford characters, like the wire form.
const defaultCareer = "testcareer000001"

// row is a `player_stat` row as a golden test cares about it.
type row struct {
	Value   float64
	Seq     int64
	Context string
}

func (r row) String() string {
	return fmt.Sprintf("{value:%v seq:%d context:%s}", r.Value, r.Seq, r.Context)
}

// flightN returns a stable flight ULID for a golden test.
func flightN(n byte) ids.ID {
	var id ids.ID
	id[0], id[15] = 0x01, n
	return id
}

// fold applies every registered fold to the sequence, one event per seq starting
// at 1, and returns the resulting player_stat rows keyed "player/stat".
func fold(t *testing.T, in []input) map[string]row {
	t.Helper()
	proj := testutil.MemProjections(t)
	apply(t, proj, in, 0, false)
	return readStats(t, proj)
}

// apply folds a sequence into an open projections database.
//
// refined mirrors what a rebuild does (§5.6): a first pass that applies only the
// flight-state fold and indexes every kitten.kia, then a second that scores the
// boards against a flight_state already complete for the whole history. That
// two-pass shape is the whole reason a rebuild can answer questions the
// incremental path cannot, so a test of the refinements has to reproduce it.
func apply(t *testing.T, proj *store.Projections, in []input, base int64, refined bool) {
	t.Helper()
	ctx := t.Context()

	run := func(folds []stats.Fold, batch func(*sql.Tx) *stats.Batch) {
		t.Helper()
		err := proj.WithWriteTx(ctx, func(tx *sql.Tx) error {
			b := batch(tx)
			for i, e := range in {
				ev := decode(t, e, base+int64(i)+1)
				for _, f := range folds {
					if err := f.Apply(ctx, b, ev); err != nil {
						return fmt.Errorf("fold %s on %s: %w", f.Name(), e.typ, err)
					}
				}
			}
			return b.Flush(ctx)
		})
		if err != nil {
			t.Fatalf("apply folds: %v", err)
		}
	}

	newBatch := func(tx *sql.Tx) *stats.Batch { return stats.NewBatch(tx, stats.BatchOptions{}) }
	if !refined {
		run(stats.Folds(), newBatch)
		return
	}

	kia := map[ids.ID][]float64{}
	for _, e := range in {
		if e.typ == "kitten.kia" && e.flight != ids.Zero && !e.noSimT {
			kia[e.flight] = append(kia[e.flight], e.simT)
		}
	}
	run(stats.StateFolds(), newBatch)
	// SecondPassFolds, not BoardFolds: it is what the rebuild's second pass
	// actually applies, so the census is folded here too and a refined run stays
	// comparable to an incremental one row for row.
	run(stats.SecondPassFolds(), func(tx *sql.Tx) *stats.Batch {
		return stats.NewRefinedBatch(tx, kia, stats.BatchOptions{})
	})
}

func decode(t *testing.T, e input, seq int64) stats.Event {
	t.Helper()
	raw, err := json.Marshal(e.payload)
	if err != nil {
		t.Fatalf("marshal %s payload: %v", e.typ, err)
	}
	if e.payload == nil {
		raw = json.RawMessage("{}")
	}
	player := e.player
	if player == 0 {
		player = alice
	}
	career := e.career
	if career == "" && !e.noCareer {
		career = defaultCareer
	}
	se := store.StoredEvent{
		Seq:      seq,
		PlayerID: player,
		RecvTime: recvOr(e.recvMS, 1_700_000_000_000+seq),
		Event: store.Event{
			ID:        flightN(byte(seq)),
			FlightID:  e.flight,
			SessionID: flightN(0xff),
			Career:    career,
			Type:      e.typ,
			Ver:       1,
			SimTime:   sql.NullFloat64{Float64: e.simT, Valid: !e.noSimT},
			WallTime:  1_700_000_000_000,
			Payload:   raw,
		},
	}
	ev, err := stats.Decode(se, nil)
	if err != nil {
		t.Fatalf("decode %s: %v", e.typ, err)
	}
	return ev
}

func readStats(t *testing.T, proj *store.Projections) map[string]row {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat ORDER BY player_id, stat`)
	if err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	defer rows.Close()

	out := map[string]row{}
	for rows.Next() {
		var (
			player int64
			stat   string
			r      row
			cx     sql.NullString
		)
		if err := rows.Scan(&player, &stat, &r.Value, &cx, &r.Seq); err != nil {
			t.Fatalf("scan player_stat: %v", err)
		}
		r.Context = cx.String
		out[fmt.Sprintf("%d/%s", player, stat)] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	return out
}

// want asserts the exact set of stat keys and their values, ignoring context and
// seq unless the test asks for them with wantRow.
func want(t *testing.T, got map[string]row, expect map[string]float64) {
	t.Helper()
	gotKeys := slices.Sorted(maps.Keys(got))
	wantKeys := slices.Sorted(maps.Keys(expect))
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("stat keys:\n got %v\nwant %v", gotKeys, wantKeys)
	}
	for k, v := range expect {
		if got[k].Value != v {
			t.Errorf("%s = %v, want %v", k, got[k].Value, v)
		}
	}
}

// --- biggest_lithobrake_survived ---------------------------------------------

func TestLithobrakeRecordAndItsExclusions(t *testing.T) {
	f1, f2, f3, f4 := flightN(1), flightN(2), flightN(3), flightN(4)
	got := fold(t, []input{
		{flight: f1, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 1}},
		// Qualifies.
		{flight: f1, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 214, EnergyJ: 4.8e7, Survived: true, Body: "duna", CrewCount: 1,
		}},
		// Does not: the vehicle was destroyed.
		{flight: f2, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 900, Survived: false, Body: "duna", CrewCount: 1,
		}},
		// Does not: nobody aboard.
		{flight: f3, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 900, Survived: true, Body: "duna", CrewCount: 0,
		}},
		// Does not: falling over on the pad is not a lithobrake.
		{flight: f4, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 900, Survived: true, LaunchPad: true, Body: "kerbin", CrewCount: 2,
		}},
	})
	want(t, got, map[string]float64{
		"1/biggest_lithobrake_survived": 214,
		// The same impact, ranked by energy instead — one eligibility rule, two
		// boards (survivedImpact).
		"1/biggest_impact_energy": 4.8e7,
		// f1's flight.started put one kitten on the pad.
		"1/biggest_crew": 1,
	})

	wantCx := `{"body":"duna","energy_j":48000000,"flight":"` + ids.String(flightN(1)) + `","speed_ms":214}`
	if cx := got["1/biggest_lithobrake_survived"].Context; cx != wantCx {
		t.Errorf("context = %s", cx)
	}
	if cx := got["1/biggest_impact_energy"].Context; cx != wantCx {
		t.Errorf("energy board context = %s, want the same blob as the speed board", cx)
	}
}

func TestRecordTieKeepsTheEarliestSeq(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	impact := stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}
	got := fold(t, []input{
		{flight: f1, typ: "vehicle.impact", payload: impact},
		{flight: f2, typ: "vehicle.impact", payload: impact}, // identical value, later seq
	})
	r := got["1/biggest_lithobrake_survived"]
	if r.Value != 214 {
		t.Fatalf("value = %v, want 214", r.Value)
	}
	if r.Seq != 1 {
		t.Errorf("updated_seq = %d, want 1: an equal value must not displace the earlier claim", r.Seq)
	}
	if wantCx := `"flight":"` + ids.String(f1) + `"`; !contains(r.Context, wantCx) {
		t.Errorf("context = %s, want it to still name the first flight", r.Context)
	}
}

func TestRecordIsReplacedOnlyByAStrictlyLargerValue(t *testing.T) {
	f1, f2, f3 := flightN(1), flightN(2), flightN(3)
	got := fold(t, []input{
		{flight: f1, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}},
		{flight: f2, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 100, Survived: true, Body: "duna", CrewCount: 1}},
		{flight: f3, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 301, Survived: true, Body: "eve", CrewCount: 1}},
	})
	r := got["1/biggest_lithobrake_survived"]
	if r.Value != 301 || r.Seq != 3 {
		t.Fatalf("got %s, want value 301 at seq 3", r)
	}
}

// --- flag exclusion ----------------------------------------------------------

func TestFlaggedFlightScoresNothing(t *testing.T) {
	clean, dirty := flightN(1), flightN(2)
	got := fold(t, []input{
		{flight: clean, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}},
		// The flag lands before the flight's scoring events, which is the case
		// the incremental fold can get right on its own.
		{flight: dirty, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport", Detail: "moved 4.2e6 m"}},
		{flight: dirty, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 9000, Survived: true, Body: "duna", CrewCount: 1}},
		{flight: dirty, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "collision", SpeedMs: 20}},
		{flight: dirty, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}},
		{flight: dirty, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "duna"}},
		{flight: dirty, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "ike"}},
		{flight: dirty, typ: "vehicle.docked", payload: stats.VehicleDock{}},
		{flight: dirty, typ: "kitten.tumble", payload: stats.KittenTumble{Kid: "k1", SpeedMs: 9}},
		{flight: dirty, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 4}},
		{flight: dirty, typ: "telemetry.window", payload: window(500, 900, ptr(88.0))},
	})
	want(t, got, map[string]float64{"1/biggest_lithobrake_survived": 214})
}

func TestTuningFlagExcludesTheTumbleCounter(t *testing.T) {
	// docs/events.md: the game's debug window live-edits the tumble speed gate,
	// which is the sole classifier for kitten.tumble. The flag only protects the
	// board if a *counter* honours it, which is why catlog applies the exclusion
	// to counters and not only to records.
	dirty := flightN(1)
	got := fold(t, []input{
		{flight: dirty, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "tuning", Detail: "TumbleSpeedGate 6.5 → 0.1"}},
		{flight: dirty, typ: "kitten.tumble", payload: stats.KittenTumble{Kid: "k1", SpeedMs: 0.2}},
		{flight: dirty, typ: "kitten.tumble", payload: stats.KittenTumble{Kid: "k1", SpeedMs: 0.2}},
	})
	want(t, got, map[string]float64{})
}

func TestFlagBits(t *testing.T) {
	for flag, bit := range map[string]int64{
		"teleport":      stats.FlagTeleport,
		"refuel":        stats.FlagRefuel,
		"resource_edit": stats.FlagResourceEdit,
		"console":       stats.FlagConsole,
		"tuning":        stats.FlagTuning,
	} {
		if got := stats.FlagBit(flag); got != bit {
			t.Errorf("FlagBit(%q) = %d, want %d", flag, got, bit)
		}
	}
	// An unrecognised flag from a newer mod must still taint the flight: failing
	// open would make every future flag a scoring loophole.
	if got := stats.FlagBit("warp_drive"); got != stats.FlagOther {
		t.Errorf("FlagBit(unknown) = %d, want FlagOther (%d)", got, stats.FlagOther)
	}
	unflagged := stats.FlightState{}
	if unflagged.Flagged() {
		t.Error("a flight with no bits set reports flagged")
	}
}

func TestUnknownFlagStillExcludes(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "orbital_mind_control"}},
		{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 500, Survived: true, CrewCount: 1}},
	})
	want(t, got, map[string]float64{})
}

// --- peak_g and the speed boards ---------------------------------------------

func TestPeakGIgnoresAnAbsentReading(t *testing.T) {
	// docs/ksa-integration.md: StructuralLoad is only written under full
	// physics, so the mod omits peak_g rather than reporting zero. A fold that
	// treated the omission as a real 0 would put a fake minimum on the board.
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "telemetry.window", payload: window(100, 200, nil)},
		{flight: f, typ: "telemetry.window", payload: window(150, 250, ptr(0.0))},
	})
	want(t, got, map[string]float64{
		"1/fastest_surface_speed": 150,
		"1/fastest_orbital_speed": 250,
	})
}

func TestSpeedBoardsComeFromTelemetryWindows(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	seedCareerSet(t, proj, defaultCareer, testSystem, 1)
	apply(t, proj, []input{
		{flight: f, typ: "telemetry.window", payload: window(2410, 7820, ptr(4.2))},
		{flight: f, typ: "telemetry.window", payload: window(640, 9450, ptr(6.8))},
		// roster.snapshot.fastest_ms is ecliptic-frame (~30 km/s standing still
		// on Earth) and must never reach a speed board.
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 1000, FastestMs: 29812},
		}}},
	}, 0, false)
	got := readStats(t, proj)
	want(t, got, map[string]float64{
		"1/fastest_surface_speed": 2410,
		"1/fastest_orbital_speed": 9450,
		"1/peak_g_survived":       6.8,
		"1/distance_travelled":    1000,
		"1/top_kitten_distance":   1000,
	})
}

// --- counters ----------------------------------------------------------------

func TestRUDCountersTotalAndPerCause(t *testing.T) {
	f := flightN(1)
	// The §4.2 causes plus one the enum does not contain. A cause a newer game
	// or mod introduces gets its own board rather than vanishing into the total:
	// the per-cause boards are a family whose keys come from the data, not an
	// allow-list this build keeps.
	causes := []string{
		"ground_impact", "ocean_impact", "collision",
		"excessive_g_force", "aerodynamic_forces", "hydrodynamic_forces",
		"kraken",
	}
	in := []input{}
	expect := map[string]float64{"1/rud_total": float64(len(causes)) + 1}
	for _, cause := range causes {
		in = append(in, input{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: cause, SpeedMs: 100}})
		expect["1/rud_"+cause] = 1
	}
	// A cause that cannot be half of a stat key still counts towards the total
	// and nothing else.
	in = append(in, input{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "who knows", SpeedMs: 100}})

	want(t, fold(t, in), expect)
}

func TestCounterTieBreakIsWhoReachedTheNumberFirst(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	got := fold(t, []input{
		{player: alice, flight: f1, typ: "vehicle.staging", payload: stats.VehicleStaging{}},
		{player: bob, flight: f2, typ: "vehicle.staging", payload: stats.VehicleStaging{}},
		{player: alice, flight: f1, typ: "vehicle.staging", payload: stats.VehicleStaging{}},
		{player: bob, flight: f2, typ: "vehicle.staging", payload: stats.VehicleStaging{}},
	})
	a, b := got["1/stagings"], got["2/stagings"]
	if a.Value != 2 || b.Value != 2 {
		t.Fatalf("stagings: alice %v, bob %v — want 2 each", a.Value, b.Value)
	}
	if a.Seq >= b.Seq {
		t.Errorf("alice reached 2 at seq %d, bob at seq %d: alice must sort first", a.Seq, b.Seq)
	}
}

func TestOrbitsCountOnlyAchieved(t *testing.T) {
	f := flightN(1)
	want(t, fold(t, []input{
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "kerbin", ApM: 320000, PeM: 295000}},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "escaped", Body: "kerbin"}},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "mun"}},
	}), map[string]float64{
		"1/orbits_achieved": 2,
		// The first achieved orbit is also this career's fastest-to-orbit; the
		// input has no simT, so it lands at 0.
		"1/fastest_to_orbit": 0,
		// The shape boards read the same events. `ecc` and `inc_deg` are 0 here,
		// which is what an unreadable orbit looks like, so neither
		// roundest_orbit nor steepest_orbit may score.
		"1/highest_apoapsis": 320000,
		"1/lowest_orbit":     295000,
	})
}

func TestSOIBodiesCountsDistinctDestinations(t *testing.T) {
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"}},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "mun", ToBody: "kerbin"}},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"}}, // repeat
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "kerbin", ToBody: "duna"}},
	}, 0, false)
	// Three distinct destinations, and — because the per-body boards come from
	// the data rather than a list — a career-time board for each of them. These
	// are `catlog.sim`'s body names, which the stock game does not ship: that is
	// exactly the case the old allow-list got wrong.
	want(t, readStats(t, proj), map[string]float64{
		"1/soi_bodies":        3,
		"1/fastest_to_mun":    0,
		"1/fastest_to_kerbin": 0,
		"1/fastest_to_duna":   0,
	})

	var bodies int
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM player_body WHERE player_id = 1 AND kind = 'soi'`).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if bodies != 3 {
		t.Errorf("player_body rows = %d, want 3", bodies)
	}
}

func TestKittensRecoveredSumsOnlyRecoveredFlights(t *testing.T) {
	f1, f2, f3 := flightN(1), flightN(2), flightN(3)
	want(t, fold(t, []input{
		{flight: f1, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 3}},
		{flight: f2, typ: "flight.ended", payload: stats.FlightEnded{Reason: "destroyed", CrewCount: 2}},
		{flight: f3, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 1}},
	}), map[string]float64{
		"1/kittens_recovered": 4,
		// The sum is 4; the biggest single trip home is 3.
		"1/biggest_recovery": 3,
	})
}

func TestDistanceTravelledSumsPerKittenMaxima(t *testing.T) {
	proj := testutil.MemProjections(t)
	seedCareerSet(t, proj, defaultCareer, testSystem, 1)
	apply(t, proj, []input{
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 1000, Missions: 1},
			{Kid: "k2", Name: "Nimbus", TravelledM: 500, Missions: 1},
		}}},
		// A later snapshot advances one kitten and — as happens after a save
		// reload — regresses the other. Totals may never rewind.
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 2500, Missions: 2},
			{Kid: "k2", Name: "Nimbus", TravelledM: 100, Missions: 1},
		}}},
	}, 0, false)
	want(t, readStats(t, proj), map[string]float64{
		"1/distance_travelled":  3000,
		"1/top_kitten_distance": 2500,
		"1/top_kitten_missions": 2,
	})

	var travelled float64
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT travelled_m FROM kitten WHERE player_id = 1 AND kid = 'k2'`).Scan(&travelled); err != nil {
		t.Fatal(err)
	}
	if travelled != 500 {
		t.Errorf("kitten k2 travelled_m = %v, want 500 (max, never rewound)", travelled)
	}
}

// --- flight_state ------------------------------------------------------------

func TestFlightStateIsBuiltForEveryFlightBearingEvent(t *testing.T) {
	f := flightN(7)
	engines := 4
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 2, EngineCount: &engines}},
		{flight: f, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "refuel"}},
		{flight: f, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "console"}},
		{flight: f, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 2}},
	}, 0, false)

	var (
		flags  int64
		reason string
		crew   int64
		body   string
		engine int64
	)
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT flags, ended_reason, crew, body, engine_count FROM flight_state WHERE flight_id = ?`,
		ids.Bytes(f)).Scan(&flags, &reason, &crew, &body, &engine); err != nil {
		t.Fatalf("read flight_state: %v", err)
	}
	if wantFlags := stats.FlagRefuel | stats.FlagConsole; flags != wantFlags {
		t.Errorf("flags = %d (%v), want %d", flags, stats.FlagNames(flags), wantFlags)
	}
	if reason != "recovered" || crew != 2 || body != "duna" || engine != 4 {
		t.Errorf("flight_state = {%q, crew %d, body %q, engines %d}", reason, crew, body, engine)
	}
}

func TestFlightEngineCountSurvivesSameBatchAndFlushReload(t *testing.T) {
	zero := 0
	positive := 4
	cases := []struct {
		flight ids.ID
		name   string
		value  *int
	}{
		{flight: flightN(8), name: "absent"},
		{flight: flightN(9), name: "zero", value: &zero},
		{flight: flightN(10), name: "positive", value: &positive},
	}
	proj := testutil.MemProjections(t)

	if err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		batch := stats.NewBatch(tx, stats.BatchOptions{})
		for i, tc := range cases {
			ev := decode(t, input{flight: tc.flight, typ: "flight.started", payload: stats.FlightStarted{
				Body: "earth", EngineCount: tc.value,
			}}, int64(i+1))
			if err := stats.FlightFold().Apply(t.Context(), batch, ev); err != nil {
				return err
			}
			assertEngineCount(t, batch, tc.flight, tc.name, tc.value, "same-batch")
		}
		return batch.Flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}

	if err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		batch := stats.NewBatch(tx, stats.BatchOptions{})
		for _, tc := range cases {
			assertEngineCount(t, batch, tc.flight, tc.name, tc.value, "reloaded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	counts, err := proj.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counts.FlightState != int64(len(cases)) {
		t.Errorf("flight_state census = %d, want %d", counts.FlightState, len(cases))
	}
}

func assertEngineCount(t *testing.T, batch *stats.Batch, flight ids.ID, name string, want *int, phase string) {
	t.Helper()
	state, ok, err := batch.Flight(t.Context(), flight)
	if err != nil || !ok {
		t.Fatalf("%s %s Flight = ok %v, err %v", phase, name, ok, err)
	}
	if want == nil {
		if state.EngineCount.Valid {
			t.Errorf("%s %s engine_count = %+v, want absent", phase, name, state.EngineCount)
		}
		return
	}
	if !state.EngineCount.Valid || state.EngineCount.Int64 != int64(*want) {
		t.Errorf("%s %s engine_count = %+v, want %d", phase, name, state.EngineCount, *want)
	}
}

func TestFlagArrivingBeforeFlightStartedIsNotLost(t *testing.T) {
	// A batch can be split so a flight.flagged is folded before the
	// flight.started it belongs to. The flag must survive that.
	f := flightN(9)
	got := fold(t, []input{
		{flight: f, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}},
		{flight: f, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 1}},
		{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 500, Survived: true, CrewCount: 1}},
	})
	want(t, got, map[string]float64{})
}

// --- rebuild refinements -----------------------------------------------------

func TestRefinedPassAppliesTheKIAWindow(t *testing.T) {
	f := flightN(1)
	in := []input{
		{flight: f, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 1}, simT: 100},
		{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}, simT: 200},
		{flight: f, typ: "kitten.kia", payload: stats.KittenKIA{Kid: "k1", Name: "Comet", Context: "manual_destroy"}, simT: 201},
		{flight: f, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 0}, simT: 210},
	}

	incremental := testutil.MemProjections(t)
	apply(t, incremental, in, 0, false)
	if got := readStats(t, incremental)["1/biggest_lithobrake_survived"].Value; got != 214 {
		t.Fatalf("incremental lithobrake = %v, want 214 (it cannot see the kia yet)", got)
	}

	refined := testutil.MemProjections(t)
	apply(t, refined, in, 0, true)
	if _, ok := readStats(t, refined)["1/biggest_lithobrake_survived"]; ok {
		t.Error("the refined pass scored a lithobrake with a kitten.kia 1 s away (§4.2 ±2 s window)")
	}
}

func TestKIAWindowOnlyReachesTwoSeconds(t *testing.T) {
	f := flightN(1)
	in := []input{
		{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}, simT: 200},
		{flight: f, typ: "kitten.kia", payload: stats.KittenKIA{Kid: "k1", Context: "manual_destroy"}, simT: 202.5},
		{flight: f, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 1}, simT: 210},
	}
	refined := testutil.MemProjections(t)
	apply(t, refined, in, 0, true)
	if got := readStats(t, refined)["1/biggest_lithobrake_survived"].Value; got != 214 {
		t.Errorf("lithobrake = %v, want 214: a kia 2.5 s away is outside the window", got)
	}
}

func TestRefinedPeakGRequiresARecoveredFlight(t *testing.T) {
	survived, lost := flightN(1), flightN(2)
	in := []input{
		{flight: survived, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 1}},
		{flight: survived, typ: "telemetry.window", payload: window(100, 200, ptr(6.0))},
		{flight: survived, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 1}},
		{flight: lost, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 1}},
		{flight: lost, typ: "telemetry.window", payload: window(100, 200, ptr(19.0))},
		{flight: lost, typ: "flight.ended", payload: stats.FlightEnded{Reason: "destroyed", CrewCount: 0}},
	}

	incremental := testutil.MemProjections(t)
	apply(t, incremental, in, 0, false)
	if got := readStats(t, incremental)["1/peak_g_survived"].Value; got != 19 {
		t.Fatalf("incremental peak_g = %v, want 19 (the simpler §5.6 rule)", got)
	}

	refined := testutil.MemProjections(t)
	apply(t, refined, in, 0, true)
	if got := readStats(t, refined)["1/peak_g_survived"].Value; got != 6 {
		t.Errorf("refined peak_g = %v, want 6: only recovered flights count", got)
	}
}

// --- feed --------------------------------------------------------------------

func TestFeedSummaries(t *testing.T) {
	f := flightN(1)
	cases := []struct {
		in   input
		want string
	}{
		{input{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 214, Survived: true, Body: "duna", CrewCount: 1}},
			"whiskers lithobraked at 214 m/s on duna — and survived"},
		{input{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "excessive_g_force", SpeedMs: 1204, Body: "eve"}},
			"whiskers lost a vehicle to excessive g-force on eve at 1204 m/s"},
		{input{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved", Body: "kerbin", ApM: 320000, PeM: 295000}},
			"whiskers made orbit around kerbin (320 km × 295 km)"},
		{input{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"}},
			"whiskers entered mun's sphere of influence"},
		{input{flight: f, typ: "kitten.tumble", payload: stats.KittenTumble{Kid: "k1", Name: "Bramble", SpeedMs: 8.9, Body: "mun", From: "future-mode"}},
			"whiskers's kitten Bramble took a tumble at 8.9 m/s on mun"},
		{input{flight: f, typ: "kitten.kia", payload: stats.KittenKIA{Kid: "k1", Name: "Bramble", Context: "manual_destroy"}},
			"whiskers said goodbye to kitten Bramble"},
		{input{flight: f, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 3}},
			"whiskers brought 3 kittens home safely"},
		{input{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "mun", VerticalSpeedMs: 1.4, HorizontalSpeedMs: 0.3, CrewCount: 2, Survived: true}},
			"whiskers landed on mun at 1.4 m/s with 2 kittens aboard"},
		{input{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "duna", VerticalSpeedMs: 240, HorizontalSpeedMs: 4, Survived: true}},
			"whiskers landed on duna at 240 m/s"},
	}

	proj := testutil.MemProjections(t)
	err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := stats.NewBatch(tx, stats.BatchOptions{})
		for i, c := range cases {
			got, ok, err := stats.Summarize(t.Context(), decode(t, c.in, int64(i)+1), "whiskers", b)
			if err != nil {
				return err
			}
			if !ok {
				t.Errorf("%s produced no feed line", c.in.typ)
				continue
			}
			if got != c.want {
				t.Errorf("%s:\n got %q\nwant %q", c.in.typ, got, c.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFeedSkipsNonEventsAndFlaggedFlights(t *testing.T) {
	dirty := flightN(2)
	proj := testutil.MemProjections(t)
	err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := stats.NewBatch(tx, stats.BatchOptions{})
		ctx := t.Context()

		// A flight.ended that was not a recovery is not news.
		if _, ok, err := stats.Summarize(ctx, decode(t, input{flight: flightN(1), typ: "flight.ended",
			payload: stats.FlightEnded{Reason: "despawned"}}, 1), "whiskers", b); err != nil || ok {
			t.Errorf("despawned flight produced a feed line (err %v)", err)
		}
		// A landing the vehicle did not survive is a crash, and the vehicle.rud
		// beside it already announces that. One moment, one feed line.
		if _, ok, err := stats.Summarize(ctx, decode(t, input{flight: flightN(1), typ: "vehicle.landed",
			payload: stats.VehicleLanded{Body: "eve", VerticalSpeedMs: 0.4, CrewCount: 3}}, 5),
			"whiskers", b); err != nil || ok {
			t.Errorf("a landing nobody survived produced a feed line (err %v)", err)
		}
		// A player with no handle produces nothing — the feed is public.
		if _, ok, err := stats.Summarize(ctx, decode(t, input{flight: flightN(1), typ: "vehicle.soi",
			payload: stats.VehicleSOI{ToBody: "mun"}}, 2), "", b); err != nil || ok {
			t.Errorf("a handle-less player produced a feed line (err %v)", err)
		}

		flagFold := stats.FlightFold()
		if err := flagFold.Apply(ctx, b, decode(t, input{flight: dirty, typ: "flight.flagged",
			payload: stats.FlightFlagged{Flag: "teleport"}}, 3)); err != nil {
			return err
		}
		if _, ok, err := stats.Summarize(ctx, decode(t, input{flight: dirty, typ: "vehicle.soi",
			payload: stats.VehicleSOI{ToBody: "mun"}}, 4), "whiskers", b); err != nil || ok {
			t.Errorf("a flagged flight reached the feed (err %v)", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- board metadata ----------------------------------------------------------

func TestBoardMetadataCoversEveryStatAFoldWrites(t *testing.T) {
	// Every board key a fold can produce must describe, or the read API would
	// hold a row it can neither title nor serve.
	// The four boards whose unit is deliberately empty: an eccentricity, and
	// three counts of a thing the title already names. Everything else must
	// carry a label, or a value column would be a bare number nobody can read.
	unitless := map[string]bool{
		stats.StatRoundestOrbit: true,
		stats.StatMostParts:     true,
		stats.StatMostStages:    true,
		stats.StatBiggestStack:  true,
	}
	declared := map[string]bool{}
	for _, b := range stats.FixedBoards() {
		if declared[b.Stat] {
			t.Errorf("duplicate board %q", b.Stat)
		}
		if b.Title == "" {
			t.Errorf("board %q has no title", b.Stat)
		}
		if b.Unit == "" && !unitless[b.Stat] {
			t.Errorf("board %q has no unit", b.Stat)
		}
		declared[b.Stat] = true
	}

	fixed := []string{
		stats.StatBiggestLithobrakeSurvived, stats.StatPeakGSurvived,
		stats.StatFastestSurfaceSpeed, stats.StatFastestOrbitalSpeed,
		stats.StatKittenTumbles, stats.StatRUDTotal, stats.StatOrbitsAchieved,
		stats.StatSOIBodies, stats.StatDockings, stats.StatStagings,
		stats.StatKittensRecovered, stats.StatDistanceTravelled,
		stats.StatFastestToOrbit,
		stats.StatMaxQSurvived, stats.StatBiggestImpactEnergy,
		stats.StatFastestEntry, stats.StatHighestAltitude,
		stats.StatHighestApoapsis, stats.StatLowestOrbit,
		stats.StatRoundestOrbit, stats.StatSteepestOrbit,
		stats.StatSoftestTouchdown, stats.StatHeaviestLaunch,
		stats.StatMostParts, stats.StatBiggestCrew,
		stats.StatBiggestRecovery, stats.StatMostStages,
		stats.StatLongestEVA, stats.StatLandedBodies,
		stats.StatSplashdowns, stats.StatEVAs, stats.StatFlameouts,
		stats.StatEngineIgnitions, stats.StatTopKittenDistance,
		stats.StatTopKittenMissions,
		stats.StatHeaviestToOrbit, stats.StatSoftestLanding, stats.StatLandings,
		stats.StatLowestPass, stats.StatBiggestStack,
		stats.StatCareerPlaytime, stats.StatPlaySessions,
	}
	for _, stat := range fixed {
		if !declared[stat] {
			t.Errorf("fold writes %q but no fixed board declares it", stat)
		}
	}
	if len(declared) != len(fixed) {
		t.Errorf("%d fixed boards declared, %d expected", len(declared), len(fixed))
	}

	// The two families: whatever key the fold builds, Describe must name it.
	for _, body := range []string{"luna", "zephyria", "kerbin-ii", "mod.newworld"} {
		stat, ok := stats.FastestToStat(body)
		if !ok {
			t.Fatalf("FastestToStat(%q) refused a well-formed name", body)
		}
		if b, known := stats.Describe(stat); !known || b.Unit != "ms" {
			t.Errorf("fold writes %q but Describe says %+v (known=%v)", stat, b, known)
		}
	}
	for _, cause := range []string{"ground_impact", "kraken", "orbital_decay"} {
		stat, ok := stats.RUDStat(cause)
		if !ok {
			t.Fatalf("RUDStat(%q) refused a well-formed cause", cause)
		}
		if b, known := stats.Describe(stat); !known || b.Unit != "RUDs" {
			t.Errorf("fold writes %q but Describe says %+v (known=%v)", stat, b, known)
		}
	}
	if _, ok := stats.Describe("not_a_board"); ok {
		t.Error("Describe accepted an unknown stat")
	}
	if _, ok := stats.Known("not_a_board", 99); ok {
		t.Error("Known accepted an unknown stat")
	}
}

// The board index is assembled from the data: a fixed board always, a family
// board once enough players are on it, in a display order that is a function of
// the keys alone — so a rebuild that reproduces player_stat reproduces the
// published board list too.
func TestCatalogPublishesFamilyBoardsOnceEnoughPlayersAreOnThem(t *testing.T) {
	counts := map[string]int64{
		stats.StatKittenTumbles: 4,
		"rud_ground_impact":     3,
		"rud_kraken":            1,
		"fastest_to_luna":       2,
		"fastest_to_zephyria":   1,
		"not_a_board_at_all":    9,
	}

	got := []string{}
	for _, b := range stats.Catalog(counts, 2) {
		got = append(got, b.Stat)
	}
	has := func(stat string) bool { return slices.Contains(got, stat) }

	for _, stat := range []string{"rud_ground_impact", "fastest_to_luna"} {
		if !has(stat) {
			t.Errorf("%q has two players and was not listed: %v", stat, got)
		}
	}
	for _, stat := range []string{"rud_kraken", "fastest_to_zephyria", "not_a_board_at_all"} {
		if has(stat) {
			t.Errorf("%q was listed: %v", stat, got)
		}
	}
	if len(stats.FixedBoards()) != len(got)-2 {
		t.Errorf("catalog = %v; every fixed board is listed whether or not anyone is on it", got)
	}
	// Family members sit under the fixed board they belong with.
	if i, j := slices.Index(got, stats.StatRUDTotal), slices.Index(got, "rud_ground_impact"); j != i+1 {
		t.Errorf("rud_ground_impact is at %d, want right after rud_total at %d: %v", j, i, got)
	}
	if i, j := slices.Index(got, stats.StatFastestToOrbit), slices.Index(got, "fastest_to_luna"); j != i+1 {
		t.Errorf("fastest_to_luna is at %d, want right after fastest_to_orbit at %d: %v", j, i, got)
	}
	// The order is a pure function of the counts map, whose iteration order Go
	// deliberately randomises — so running it twice is a real test.
	again := []string{}
	for _, b := range stats.Catalog(counts, 2) {
		again = append(again, b.Stat)
	}
	if !slices.Equal(got, again) {
		t.Errorf("catalog is not deterministic:\n %v\n %v", got, again)
	}

	// Lowering the threshold publishes history that was already recorded; it
	// never has to go and collect it.
	all := []string{}
	for _, b := range stats.Catalog(counts, 1) {
		all = append(all, b.Stat)
	}
	if !slices.Contains(all, "fastest_to_zephyria") || !slices.Contains(all, "rud_kraken") {
		t.Errorf("min 1 = %v, want the single-entrant boards listed", all)
	}
}

func TestCareerNativeBoardsAreLastWithoutDisplacingDynamicFamilies(t *testing.T) {
	fixed := stats.FixedBoards()
	if len(fixed) < 2 || fixed[len(fixed)-2].Stat != stats.StatCareerPlaytime || fixed[len(fixed)-1].Stat != stats.StatPlaySessions {
		t.Fatalf("last fixed boards = %v, want career_playtime then play_sessions", fixed[max(0, len(fixed)-2):])
	}

	var got []string
	for _, b := range stats.Catalog(map[string]int64{"fastest_to_luna": 2}, 2) {
		got = append(got, b.Stat)
	}
	orbit := slices.Index(got, stats.StatFastestToOrbit)
	if orbit < 0 || orbit+3 >= len(got) {
		t.Fatalf("catalog is missing the career tail: %v", got)
	}
	want := []string{stats.StatFastestToOrbit, "fastest_to_luna", stats.StatCareerPlaytime, stats.StatPlaySessions}
	if !slices.Equal(got[orbit:orbit+4], want) {
		t.Errorf("catalog career tail = %v, want %v", got[orbit:orbit+4], want)
	}
}

// A board page is servable for a stat somebody actually holds, listed or not: a
// profile row has to link somewhere, and hiding a player's own achievement from
// them is not what the threshold is for.
func TestKnownServesABoardTheIndexIsStillHoldingBack(t *testing.T) {
	if _, ok := stats.Known(stats.StatDockings, 0); !ok {
		t.Error("an empty fixed board must still be servable")
	}
	if _, ok := stats.Known("fastest_to_zephyria", 1); !ok {
		t.Error("a family board with one player must be servable")
	}
	if _, ok := stats.Known("fastest_to_zephyria", 0); ok {
		t.Error("a family board nobody is on must 404")
	}
}

func TestFoldOrderPutsStateFoldsFirst(t *testing.T) {
	all := stats.Folds()
	state := stats.StateFolds()
	if len(all) != len(stats.SecondPassFolds())+len(state) {
		t.Fatalf("Folds() has %d entries, StateFolds() %d, SecondPassFolds() %d",
			len(all), len(state), len(stats.SecondPassFolds()))
	}
	// The rebuild's second pass and the incremental loop's tail must be the same
	// list. If they drift, a rebuilt projections.db stops matching the
	// incremental one — which is the one property the rebuild exists to give.
	if len(stats.SecondPassFolds()) != len(stats.BoardFolds())+len(stats.LogFolds()) {
		t.Errorf("SecondPassFolds() is not BoardFolds() plus LogFolds()")
	}
	// Every board fold reads flight_state for the flag exclusion, and the
	// career-time boards need the career row to exist, so the state folds have
	// to run first. system is first among them so the same batch sees a
	// system.discovered binding before any scoped board write.
	for i, f := range state {
		if all[i].Name() != f.Name() {
			t.Errorf("fold %d is %q, want %q", i, all[i].Name(), f.Name())
		}
	}
	if state[0].Name() != "system" || state[1].Name() != stats.FlightFold().Name() {
		t.Errorf("state fold prefix = %q, %q; want system, flight", state[0].Name(), state[1].Name())
	}
}

// --- payload decoding --------------------------------------------------------

func TestUnknownPayloadKeysSurviveDecoding(t *testing.T) {
	// §4.1 preserves unknown payload keys, and the row stores the payload
	// verbatim, so a newer mod's extra field must decode rather than fail.
	se := store.StoredEvent{
		Seq: 1, PlayerID: alice, RecvTime: 1,
		Event: store.Event{
			ID: flightN(1), FlightID: flightN(2), SessionID: flightN(3),
			Type: "vehicle.impact", Ver: 1, WallTime: 1,
			Payload: json.RawMessage(`{"speed_ms":214,"survived":true,"crew_count":1,"tail_number":"KX-9"}`),
		},
	}
	ev, err := stats.Decode(se, nil)
	if err != nil {
		t.Fatalf("decode with an unknown key: %v", err)
	}
	if !contains(string(ev.Raw), "tail_number") {
		t.Error("Raw lost the unknown key")
	}
	p, ok := ev.Payload.(stats.VehicleImpact)
	if !ok || p.SpeedMs != 214 {
		t.Errorf("payload = %#v", ev.Payload)
	}
}

func TestDecodeRejectsAMalformedPayload(t *testing.T) {
	se := store.StoredEvent{
		Seq: 1, PlayerID: alice, RecvTime: 1,
		Event: store.Event{
			ID: flightN(1), SessionID: flightN(3), Type: "vehicle.impact", Ver: 1, WallTime: 1,
			Payload: json.RawMessage(`{"speed_ms":"fast"}`),
		},
	}
	if _, err := stats.Decode(se, nil); err == nil {
		t.Error("a payload with the wrong field type decoded cleanly")
	}
}

// --- helpers -----------------------------------------------------------------

func window(surfaceMax, orbitalMax float64, peakG *float64) stats.TelemetryWindow {
	return stats.TelemetryWindow{
		T0Sim: 0, T1Sim: 30, N: 60, Body: "duna",
		SurfaceSpeedMs: stats.Agg{Max: surfaceMax},
		OrbitalSpeedMs: stats.Agg{Max: orbitalMax},
		PeakG:          peakG,
	}
}

func ptr[T any](v T) *T { return &v }

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// recvOr picks an input's explicit receive stamp, or the default ladder.
func recvOr(explicit, fallback int64) int64 {
	if explicit != 0 {
		return explicit
	}
	return fallback
}
