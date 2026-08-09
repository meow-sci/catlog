package stats_test

import (
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The golden tests for the boards added after the launch thirteen. They share
// stats_test.go's harness — [fold], [apply], [want] — so a board here is
// asserted exactly the way a board there is: §4.2 events in, the exact
// `player_stat` rows out, through [stats.Decode] so the payload tags are
// covered too.

// --- the orbit shape boards ---------------------------------------------------

func TestOrbitShapeBoardsRankTheOrbitFourWays(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 320000, PeM: 295000, Ecc: 0.0021, IncDeg: 51.6}},
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "mun", ApM: 90000, PeM: 12000, Ecc: 0.4, IncDeg: 88.9}},
		// `escaped` is not an orbit anybody reached, so none of the four may
		// take these figures however good they look.
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "escaped", Body: "kerbin", ApM: 9e9, PeM: 1, Ecc: 0.00001, IncDeg: 179}},
	})
	want(t, got, map[string]float64{
		"1/orbits_achieved":  2,
		"1/fastest_to_orbit": 0,
		"1/highest_apoapsis": 320000,
		// Ascending: the *lowest* periapsis and the *roundest* orbit win, which
		// is the opposite of every other record board on this event.
		"1/lowest_orbit":   12000,
		"1/roundest_orbit": 0.0021,
		"1/steepest_orbit": 88.9,
	})

	// One context blob for all four, so the four rows of one orbit describe the
	// same orbit rather than four slices of it.
	wantCx := `{"ap_m":320000,"body":"kerbin","ecc":0.0021,"flight":"` +
		ids.String(f) + `","inc_deg":51.6,"pe_m":295000}`
	if cx := got["1/highest_apoapsis"].Context; cx != wantCx {
		t.Errorf("context = %s\n   want %s", cx, wantCx)
	}
}

func TestOrbitShapeBoardsRefuseAnUnreadableOrbit(t *testing.T) {
	// `ap_m` is written 0.0 unless the conic is Bound; `ecc` and `inc_deg` read
	// back 0 when nothing wrote them; `pe_m` is computed unconditionally and is
	// legitimately negative for a periapsis underground
	// (docs/ksa-integration.md). None of those is a record — and a 0
	// eccentricity on an *ascending* board would be an unbeatable one.
	f := flightN(1)
	want(t, fold(t, []input{
		{flight: f, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 0, PeM: -4000, Ecc: 0, IncDeg: 0}},
	}), map[string]float64{
		"1/orbits_achieved":  1,
		"1/fastest_to_orbit": 0,
	})
}

// --- the telemetry.window boards ----------------------------------------------

func TestHighestAltitudeAndMaxQ(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 0, T1Sim: 30, N: 60, Body: "kerbin",
			AltM:           stats.Agg{Max: 71000},
			SurfaceSpeedMs: stats.Agg{Max: 2410},
			MaxQPa:         ptr(41200.0),
		}},
		// A window with no structural-load reading at all. The altitude still
		// scores — a position is always sampled — and max_q must not move, the
		// same omit-don't-zero rule peak_g obeys.
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 30, T1Sim: 60, N: 60, Body: "kerbin",
			AltM: stats.Agg{Max: 240000},
		}},
		// And an explicit zero, which is what an unwritten StructuralLoad looks
		// like once it has been serialised by something that does not omit.
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 60, T1Sim: 90, N: 60, Body: "kerbin",
			AltM:   stats.Agg{Max: 100},
			MaxQPa: ptr(0.0),
		}},
	})
	want(t, got, map[string]float64{
		"1/highest_altitude":      240000,
		"1/fastest_surface_speed": 2410,
		"1/max_q_survived":        41200,
	})
	if cx := got["1/max_q_survived"].Context; cx != `{"body":"kerbin","flight":"`+ids.String(f)+`","t1_sim":30}` {
		t.Errorf("context = %s", cx)
	}
}

func TestRefinedMaxQRequiresARecoveredFlight(t *testing.T) {
	// max_q_survived is peak_g_survived's twin, refinement included: only a
	// rebuild knows how the flight ended, so incrementally the biggest reading
	// wins and after a rebuild only a recovered flight's does.
	survived, lost := flightN(1), flightN(2)
	in := []input{
		{flight: survived, typ: "flight.started", payload: stats.FlightStarted{Body: "kerbin", CrewCount: 1}},
		{flight: survived, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T1Sim: 30, Body: "kerbin", MaxQPa: ptr(28000.0)}},
		{flight: survived, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 1}},
		{flight: lost, typ: "flight.started", payload: stats.FlightStarted{Body: "kerbin", CrewCount: 1}},
		{flight: lost, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T1Sim: 30, Body: "kerbin", MaxQPa: ptr(91000.0)}},
		{flight: lost, typ: "flight.ended", payload: stats.FlightEnded{Reason: "destroyed"}},
	}

	incremental := testutil.MemProjections(t)
	apply(t, incremental, in, 0, false)
	if got := readStats(t, incremental)["1/max_q_survived"].Value; got != 91000 {
		t.Fatalf("incremental max_q = %v, want 91000 (the simpler rule)", got)
	}

	refined := testutil.MemProjections(t)
	apply(t, refined, in, 0, true)
	if got := readStats(t, refined)["1/max_q_survived"].Value; got != 28000 {
		t.Errorf("refined max_q = %v, want 28000: only recovered flights count", got)
	}
}

// --- fastest_entry -------------------------------------------------------------

func TestFastestEntryCountsOnlyArrivals(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{
			Dir: "entered", Body: "eve", SpeedMs: 3200, DynPressurePa: 88000}},
		// Leaving an atmosphere fast is an ascent, which the speed boards
		// already rank; it is not an entry however quick it was.
		{flight: f, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{
			Dir: "exited", Body: "eve", SpeedMs: 9999, DynPressurePa: 4}},
		{flight: f, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{
			Dir: "entered", Body: "kerbin", SpeedMs: 2100, DynPressurePa: 41000}},
	})
	want(t, got, map[string]float64{"1/fastest_entry": 3200})
	if cx := got["1/fastest_entry"].Context; cx != `{"body":"eve","dyn_pressure_pa":88000,"flight":"`+ids.String(f)+`"}` {
		t.Errorf("context = %s", cx)
	}
}

// --- biggest_impact_energy ------------------------------------------------------

func TestImpactEnergyShareTheLithobrakeEligibility(t *testing.T) {
	f1, f2, f3 := flightN(1), flightN(2), flightN(3)
	want(t, fold(t, []input{
		// Qualifies on both boards: the same crash, two rankings.
		{flight: f1, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 214, EnergyJ: 4.8e7, Survived: true, Body: "duna", CrewCount: 1}},
		// A heavier bang that nobody walked away from.
		{flight: f2, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 300, EnergyJ: 9e11, Survived: false, Body: "duna", CrewCount: 3}},
		// A heavier bang on the pad, which is not a lithobrake and not a bang
		// survived either.
		{flight: f3, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 12, EnergyJ: 9e11, Survived: true, LaunchPad: true, Body: "kerbin", CrewCount: 3}},
	}), map[string]float64{
		"1/biggest_lithobrake_survived": 214,
		"1/biggest_impact_energy":       4.8e7,
	})
}

func TestRefinedKIAWindowDropsTheEnergyBoardToo(t *testing.T) {
	f := flightN(1)
	in := []input{
		{flight: f, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 214, EnergyJ: 4.8e7, Survived: true, Body: "duna", CrewCount: 1}, simT: 200},
		{flight: f, typ: "kitten.kia", payload: stats.KittenKIA{Kid: "k1", Context: "manual_destroy"}, simT: 201},
	}
	refined := testutil.MemProjections(t)
	apply(t, refined, in, 0, true)
	if _, ok := readStats(t, refined)["1/biggest_impact_energy"]; ok {
		t.Error("the energy board kept a crash the ±2 s kia window disqualified; the two impact boards must agree")
	}
}

// --- the flight.started boards --------------------------------------------------

func TestLaunchBoardsMeasureWhatWasOnThePad(t *testing.T) {
	f1, f2, f3 := flightN(1), flightN(2), flightN(3)
	got := fold(t, []input{
		{flight: f1, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Whisker VII", Body: "kerbin", MassKg: 412000, PartCount: 214, CrewCount: 3}},
		{flight: f2, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Probe", Body: "kerbin", MassKg: 900, PartCount: 640, CrewCount: 0}},
		// An unreadable vehicle reports zeros rather than omitting the keys, so
		// a zero here is a failed read and not an empty rocket.
		{flight: f3, typ: "flight.started", payload: stats.FlightStarted{Body: "unknown"}},
	})
	want(t, got, map[string]float64{
		"1/heaviest_launch": 412000,
		"1/most_parts":      640,
		"1/biggest_crew":    3,
	})
	wantCx := `{"body":"kerbin","crew_count":3,"flight":"` + ids.String(f1) +
		`","mass_kg":412000,"part_count":214,"vehicle":"Whisker VII"}`
	if cx := got["1/heaviest_launch"].Context; cx != wantCx {
		t.Errorf("context = %s\n   want %s", cx, wantCx)
	}
}

// --- biggest_recovery and most_stages -------------------------------------------

func TestBiggestRecoveryIsTheSingleBestTripHome(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	got := fold(t, []input{
		{flight: f1, typ: "flight.started", payload: stats.FlightStarted{Body: "mun", CrewCount: 5}},
		{flight: f1, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 5}},
		{flight: f2, typ: "flight.started", payload: stats.FlightStarted{Body: "duna", CrewCount: 9}},
		// Lost, so it is neither a recovery nor a record.
		{flight: f2, typ: "flight.ended", payload: stats.FlightEnded{Reason: "destroyed", CrewCount: 9}},
	})
	want(t, got, map[string]float64{
		"1/kittens_recovered": 5,
		"1/biggest_recovery":  5,
		// The nine that did not come back were still nine on the pad: the two
		// boards measure different halves of a flight.
		"1/biggest_crew": 9,
	})
}

func TestMostStagesCountsStagesFiredNotTheIndex(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "flight.started", payload: stats.FlightStarted{Body: "kerbin", CrewCount: 1}},
		// stage_index is zero-based and read in the postfix, so firing the first
		// stage is index 0 and *one* stage fired.
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 0}},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 4}},
	})
	want(t, got, map[string]float64{
		"1/stagings":     3,
		"1/most_stages":  5,
		"1/biggest_crew": 1,
	})
	if cx := got["1/most_stages"].Context; cx != `{"body":"kerbin","flight":"`+ids.String(f)+`"}` {
		t.Errorf("context = %s: the body comes off flight_state, the payload has none", cx)
	}
}

// --- the spacewalk and engine boards ---------------------------------------------

func TestSpacewalkAndEngineBoards(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "kitten.eva_start", payload: stats.KittenEvaStart{Kid: "k1", Name: "Bramble"}},
		// §4.1 sends kitten.eva_end with flight: null, asymmetrically with the
		// start — so this event has no flight at all and is scoreable anyway.
		{typ: "kitten.eva_end", payload: stats.KittenEvaEnd{Kid: "k1", Name: "Bramble", DurationS: 412.5}},
		{flight: f, typ: "kitten.eva_start", payload: stats.KittenEvaStart{Kid: "k2", Name: "Nimbus"}},
		// duration_s is 0.0 when the EVA vehicle's launch time was never
		// readable, which is not a spacewalk anybody can claim.
		{typ: "kitten.eva_end", payload: stats.KittenEvaEnd{Kid: "k2", Name: "Nimbus"}},
		{flight: f, typ: "engine.ignition", payload: stats.Engine{Engine: "lv-909", Count: 2}},
		{flight: f, typ: "engine.ignition", payload: stats.Engine{Engine: "lv-909", Count: 1}},
		// engine.shutdown is the falling half of the same edge pair and counts
		// nothing: a board for it would count every burn twice.
		{flight: f, typ: "engine.shutdown", payload: stats.Engine{Engine: "lv-909", Count: 1}},
		{flight: f, typ: "engine.flameout", payload: stats.Engine{Engine: "lv-909", Count: 1}},
	})
	want(t, got, map[string]float64{
		"1/evas":             2,
		"1/longest_eva":      412.5,
		"1/engine_ignitions": 2,
		"1/flameouts":        1,
	})
	if cx := got["1/longest_eva"].Context; cx != `{"kitten":"Bramble"}` {
		t.Errorf("context = %s: eva_end carries no flight, so the kitten is the row's identity", cx)
	}
}

// --- the vehicle.situation boards -------------------------------------------------

func TestSituationBoards(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		// A capsule under parachutes: an arrival in water, an arrival at kerbin,
		// and a touchdown.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "floating", Body: "kerbin", SurfaceSpeedMs: 6.2}},
		// A boat crossing the on-rails boundary is not a second splashdown and
		// not a touchdown: it never left the water.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "floating", To: "sailing", Body: "kerbin", SurfaceSpeedMs: 0.4}},
		// A landing on another body.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "maneuvering", To: "landed", Body: "mun", SurfaceSpeedMs: 1.1}},
		// A rover coming to a stop is surface-to-surface: mun is already
		// counted, and 0.05 m/s is not a touchdown.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "rolling", To: "landed", Body: "mun", SurfaceSpeedMs: 0.05}},
		// A ninth situation a future build adds reports no contact and scores
		// nothing at all.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "maneuvering", To: "hovering", Body: "duna", SurfaceSpeedMs: 0.01}},
		// Arriving *from* a situation nobody could read is still an arrival at
		// duna — but it is not a measured touchdown, so the ascending board must
		// refuse it however small the number is.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "unknown", To: "landed", Body: "duna", SurfaceSpeedMs: 0.02}},
	})
	want(t, got, map[string]float64{
		"1/splashdowns":       1,
		"1/landed_bodies":     3,
		"1/softest_touchdown": 1.1,
	})
	if cx := got["1/softest_touchdown"].Context; cx != `{"altitude_m":0,"body":"mun","flight":"`+
		ids.String(f)+`","from":"maneuvering","to":"landed"}` {
		t.Errorf("context = %s", cx)
	}
}

func TestSplashdownRequiresPureOceanContact(t *testing.T) {
	// `dragging` and `bottomed` touch terrain as well as water: a hull on a
	// shoreline is an arrival at the body, not a splashdown.
	f := flightN(1)
	want(t, fold(t, []input{
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "dragging", Body: "kerbin", SurfaceSpeedMs: 3}},
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "bottomed", Body: "kerbin", SurfaceSpeedMs: 2}},
	}), map[string]float64{
		"1/landed_bodies":     1,
		"1/softest_touchdown": 2,
	})
}

func TestLandedAndVisitedBodiesAreSeparateSets(t *testing.T) {
	// player_body is keyed (player_id, kind, body), so reaching a body and
	// landing on it are two rows and two counters. Flying past mun without
	// landing must not put it on landed_bodies.
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"}},
		{flight: f, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "mun", ToBody: "duna"}},
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "landed", Body: "duna", SurfaceSpeedMs: 4}},
		// The same landing again is not a second body.
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "landed", Body: "duna", SurfaceSpeedMs: 9}},
	}, 0, false)
	want(t, readStats(t, proj), map[string]float64{
		"1/soi_bodies":        2,
		"1/fastest_to_mun":    0,
		"1/fastest_to_duna":   0,
		"1/landed_bodies":     1,
		"1/softest_touchdown": 4,
	})

	var rows int
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM player_body WHERE player_id = 1 AND kind = 'landed'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("player_body kind='landed' rows = %d, want 1", rows)
	}
}

// --- the per-kitten record boards ---------------------------------------------------

func TestKittenRecordBoardsBreakTiesDeterministically(t *testing.T) {
	// The winning kitten's *name* goes in the context, and Go randomises map
	// iteration, so without a tie-break on `kid` a rebuild would produce
	// different bytes from the incremental fold of the same log.
	in := []input{{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
		{Kid: "k2", Name: "Nimbus", TravelledM: 4200, Missions: 3},
		{Kid: "k1", Name: "Comet", TravelledM: 4200, Missions: 3},
	}}}}

	first := fold(t, in)["1/top_kitten_distance"].Context
	for range 8 {
		if cx := fold(t, in)["1/top_kitten_distance"].Context; cx != first {
			t.Fatalf("context = %s, the first run said %s: the tie-break is not deterministic", cx, first)
		}
	}
	if first != `{"kitten":"Comet"}` {
		t.Errorf("context = %s, want the lowest kid to hold the tie", first)
	}
}

func TestKittenRecordBoardsTakeTheBestSingleKitten(t *testing.T) {
	got := fold(t, []input{
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 4200, Missions: 2},
			{Kid: "k2", Name: "Nimbus", TravelledM: 990, Missions: 11},
		}}},
	})
	want(t, got, map[string]float64{
		"1/distance_travelled":  5190,
		"1/top_kitten_distance": 4200,
		"1/top_kitten_missions": 11,
	})
	if cx := got["1/top_kitten_missions"].Context; cx != `{"kitten":"Nimbus"}` {
		t.Errorf("context = %s: the furthest and the most-flown need not be the same cat", cx)
	}
}

// --- the flag exclusion, on everything new ---------------------------------------------

func TestFlaggedFlightExcludesTheNewBoards(t *testing.T) {
	// Every board that folds a flight-bearing event inherits `scoreable`. This
	// is the same assertion TestFlaggedFlightScoresNothing makes for the launch
	// thirteen, over the event types added since.
	dirty := flightN(1)
	want(t, fold(t, []input{
		{flight: dirty, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}},
		{flight: dirty, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Cheat", Body: "kerbin", MassKg: 9e9, PartCount: 9000, CrewCount: 90}},
		{flight: dirty, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 9e9, PeM: 1, Ecc: 0.00001, IncDeg: 179}},
		{flight: dirty, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{
			Dir: "entered", Body: "eve", SpeedMs: 9999}},
		{flight: dirty, typ: "vehicle.impact", payload: stats.VehicleImpact{
			SpeedMs: 9000, EnergyJ: 9e12, Survived: true, Body: "duna", CrewCount: 1}},
		{flight: dirty, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "floating", Body: "kerbin", SurfaceSpeedMs: 0.001}},
		{flight: dirty, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 40}},
		{flight: dirty, typ: "kitten.eva_start", payload: stats.KittenEvaStart{Kid: "k1", Name: "Cheat"}},
		{flight: dirty, typ: "engine.ignition", payload: stats.Engine{Engine: "lv-909", Count: 1}},
		{flight: dirty, typ: "engine.flameout", payload: stats.Engine{Engine: "lv-909", Count: 1}},
		{flight: dirty, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T1Sim: 30, Body: "kerbin", AltM: stats.Agg{Max: 9e9}, MaxQPa: ptr(9e9)}},
		{flight: dirty, typ: "flight.ended", payload: stats.FlightEnded{Reason: "recovered", CrewCount: 90}},
	}), map[string]float64{})
}

// --- payload decoding -----------------------------------------------------------------

func TestTheOnceUndecodedTypesNowDecode(t *testing.T) {
	// Six §4.2 types had no decoder at all. The payloads here are wire-shaped
	// maps rather than the structs, so the JSON tags are what is under test —
	// a renamed tag would silently stop a board scoring.
	for _, c := range []struct {
		typ  string
		wire map[string]any
		want any
	}{
		{"vehicle.atmosphere",
			map[string]any{"dir": "entered", "body": "eve", "speed_ms": 3200, "dyn_pressure_pa": 88000},
			stats.VehicleAtmosphere{Dir: "entered", Body: "eve", SpeedMs: 3200, DynPressurePa: 88000}},
		{"engine.ignition", map[string]any{"engine": "lv-909", "count": 2},
			stats.Engine{Engine: "lv-909", Count: 2}},
		{"engine.shutdown", map[string]any{"engine": "lv-909", "count": 2},
			stats.Engine{Engine: "lv-909", Count: 2}},
		{"engine.flameout", map[string]any{"engine": "lv-909", "count": 1},
			stats.Engine{Engine: "lv-909", Count: 1}},
		{"kitten.eva_start", map[string]any{"kid": "k1", "name": "Bramble"},
			stats.KittenEvaStart{Kid: "k1", Name: "Bramble"}},
		{"kitten.eva_end", map[string]any{"kid": "k1", "name": "Bramble", "duration_s": 412.5},
			stats.KittenEvaEnd{Kid: "k1", Name: "Bramble", DurationS: 412.5}},
	} {
		ev := decode(t, input{flight: flightN(1), typ: c.typ, payload: c.wire}, 1)
		if ev.Payload != c.want {
			t.Errorf("%s decoded to %#v, want %#v", c.typ, ev.Payload, c.want)
		}
	}
}
