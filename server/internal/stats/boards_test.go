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
			VehicleName: "Whisker VII", Body: "kerbin", MassKg: 412000, PartCount: 214,
			CrewCount: 3, StageCount: 3}},
		{flight: f2, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Probe", Body: "kerbin", MassKg: 900, PartCount: 640, CrewCount: 0, StageCount: 5}},
		// An unreadable vehicle reports zeros rather than omitting the keys, so
		// a zero here is a failed read and not an empty rocket.
		{flight: f3, typ: "flight.started", payload: stats.FlightStarted{Body: "unknown"}},
	})
	want(t, got, map[string]float64{
		"1/heaviest_launch": 412000,
		"1/most_parts":      640,
		"1/biggest_crew":    3,
		// The tallest stack is not the heaviest rocket: `biggest_stack` ranks the
		// probe that flew on five sequences over the crewed vehicle's three.
		"1/biggest_stack": 5,
	})
	wantCx := `{"body":"kerbin","crew_count":3,"flight":"` + ids.String(f1) +
		`","mass_kg":412000,"part_count":214,"stage_count":3,"vehicle":"Whisker VII"}`
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
		"1/bodies_by_1y":      2,
		"1/bodies_by_10y":     2,
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
	proj := testutil.MemProjections(t)
	seedCareerSet(t, proj, defaultCareer, testSystem, 1)
	apply(t, proj, []input{
		{typ: "roster.snapshot", payload: stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 4200, Missions: 2},
			{Kid: "k2", Name: "Nimbus", TravelledM: 990, Missions: 11},
		}}},
	}, 0, false)
	got := readStats(t, proj)
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
			VehicleName: "Cheat", Body: "kerbin", MassKg: 9e9, PartCount: 9000, CrewCount: 90,
			StageCount: 900}},
		{flight: dirty, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 9e9, PeM: 1, Ecc: 0.00001, IncDeg: 179, MassKg: 9e9}},
		{flight: dirty, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "mun", VerticalSpeedMs: 0.001, HorizontalSpeedMs: 0, CrewCount: 9, Survived: true}},
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
			T1Sim: 30, Body: "kerbin", AltM: stats.Agg{Max: 9e9}, MaxQPa: ptr(9e9),
			RadarAltM: &stats.Agg{Min: 0.001, Max: 4, Mean: 2, Last: 1}}},
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

// --- the landing, altitude and mass boards ---------------------------------------

func TestLandingBoardsRankTheDescentRate(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "mun", VerticalSpeedMs: 1.4, HorizontalSpeedMs: 0.3, CrewCount: 2, Survived: true}},
		// Harder, but still a landing: the counter takes it and the record does not.
		{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "duna", VerticalSpeedMs: 9.8, HorizontalSpeedMs: 12, CrewCount: 1, Survived: true}},
		// A touchdown nobody walked away from is a crash, and `survived` is the
		// mod's answer rather than something inferred from how gentle it looks.
		{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "eve", VerticalSpeedMs: 0.2, HorizontalSpeedMs: 1, CrewCount: 3, Survived: false}},
		// An unreadable descent decomposition reads 0, which on an ascending board
		// would be an unbeatable record — but it is still a landing that happened.
		{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "mun", VerticalSpeedMs: 0, HorizontalSpeedMs: 4, CrewCount: 0, Survived: true}},
	})
	want(t, got, map[string]float64{
		"1/landings":        3,
		"1/softest_landing": 1.4,
	})
	wantCx := `{"body":"mun","crew_count":2,"flight":"` + ids.String(f) + `","horizontal_speed_ms":0.3}`
	if cx := got["1/softest_landing"].Context; cx != wantCx {
		t.Errorf("context = %s\n   want %s", cx, wantCx)
	}
}

func TestLandingBoardsAreNotLandedBodies(t *testing.T) {
	// `landed_bodies` stays on `vehicle.situation`, and the two events are
	// emitted from the same detection — so a real landing arrives as a pair and
	// must advance each board exactly once.
	f := flightN(1)
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{flight: f, typ: "vehicle.situation", payload: stats.VehicleSituation{
			From: "freefall", To: "landed", Body: "mun", SurfaceSpeedMs: 1.5}},
		{flight: f, typ: "vehicle.landed", payload: stats.VehicleLanded{
			Body: "mun", VerticalSpeedMs: 1.4, HorizontalSpeedMs: 0.5, CrewCount: 1, Survived: true}},
	}, 0, false)
	want(t, readStats(t, proj), map[string]float64{
		"1/landed_bodies":     1,
		"1/softest_touchdown": 1.5,
		"1/landings":          1,
		"1/softest_landing":   1.4,
	})

	// Only the situation writes player_body; the landing folds never touch it,
	// which is what stops the pair counting the body twice.
	var rows int
	if err := proj.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM player_body WHERE player_id = 1 AND kind = 'landed'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("player_body kind='landed' rows = %d, want 1", rows)
	}
}

func TestLowestPassNeedsARadarReading(t *testing.T) {
	f := flightN(1)
	got := fold(t, []input{
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 0, T1Sim: 30, N: 60, Body: "mun",
			AltM:      stats.Agg{Max: 12000},
			RadarAltM: &stats.Agg{Min: 340, Max: 12000, Mean: 4000, Last: 900}}},
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 30, T1Sim: 60, N: 60, Body: "mun",
			AltM:      stats.Agg{Max: 9000},
			RadarAltM: &stats.Agg{Min: 85, Max: 9000, Mean: 2000, Last: 120}}},
		// A window spent in orbit has no terrain below it, so the mod omits the
		// aggregate entirely rather than folding zeros into it. The altitude board
		// still scores — a position is always sampled.
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 60, T1Sim: 90, N: 60, Body: "mun", AltM: stats.Agg{Max: 240000}}},
		// And a window that ended on the ground: 0 is where a landed vehicle sits,
		// and on an ascending board that is a record nobody can beat (PROJ-088).
		{flight: f, typ: "telemetry.window", payload: stats.TelemetryWindow{
			T0Sim: 90, T1Sim: 120, N: 60, Body: "mun",
			AltM:      stats.Agg{Max: 400},
			RadarAltM: &stats.Agg{Min: 0, Max: 400, Mean: 90, Last: 0}}},
	})
	want(t, got, map[string]float64{
		"1/highest_altitude": 240000,
		"1/lowest_pass":      85,
	})
	if cx := got["1/lowest_pass"].Context; cx != `{"body":"mun","flight":"`+ids.String(f)+`","t1_sim":60}` {
		t.Errorf("context = %s", cx)
	}
}

func TestHeaviestToOrbitRanksThePayloadNotTheRocket(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	got := fold(t, []input{
		{flight: f1, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Heirloom", Body: "kerbin", MassKg: 412000, PartCount: 214,
			CrewCount: 3, StageCount: 3}},
		{flight: f1, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 320000, PeM: 295000,
			Ecc: 0.0021, IncDeg: 51.6, MassKg: 18400}},
		// A much lighter rocket that got more of itself up there — which is the
		// whole reason this is not `heaviest_launch` read twice.
		{flight: f2, typ: "flight.started", payload: stats.FlightStarted{
			VehicleName: "Lean", Body: "kerbin", MassKg: 90000, PartCount: 40,
			CrewCount: 0, StageCount: 2}},
		{flight: f2, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "kerbin", ApM: 200000, PeM: 190000,
			Ecc: 0.001, IncDeg: 5, MassKg: 22100}},
		// An escape is not an orbit anybody reached, however heavy.
		{flight: f2, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "escaped", Body: "kerbin", MassKg: 9e6}},
		// An unreadable mass reads 0, and 0 kg is not a payload.
		{flight: f2, typ: "vehicle.orbit", payload: stats.VehicleOrbit{
			Phase: "achieved", Body: "mun", ApM: 10000, PeM: 9000, Ecc: 0.05, IncDeg: 2}},
	})
	want(t, got, map[string]float64{
		"1/heaviest_launch":   412000,
		"1/most_parts":        214,
		"1/biggest_crew":      3,
		"1/biggest_stack":     3,
		"1/orbits_achieved":   3,
		"1/fastest_to_orbit":  0,
		"1/highest_apoapsis":  320000,
		"1/lowest_orbit":      9000,
		"1/roundest_orbit":    0.001,
		"1/steepest_orbit":    51.6,
		"1/heaviest_to_orbit": 22100,
	})
	wantCx := `{"ap_m":200000,"body":"kerbin","flight":"` + ids.String(f2) + `","pe_m":190000}`
	if cx := got["1/heaviest_to_orbit"].Context; cx != wantCx {
		t.Errorf("context = %s\n   want %s", cx, wantCx)
	}
}

func TestVehicleLandedKeepsAbsentApartFromZero(t *testing.T) {
	// A wire-shaped map rather than the struct, so the JSON tags are what is
	// under test. lat/lon/radar_alt_m are omitted when the read failed — never
	// null, never 0 — and 0 is a real place for all three, so the decoder has to
	// keep "the mod could not say" apart from "at the equator, on the ground".
	full := decode(t, input{flight: flightN(1), typ: "vehicle.landed", payload: map[string]any{
		"body": "mun", "vertical_speed_ms": 1.4, "horizontal_speed_ms": 0.3,
		"crew_count": 2, "survived": true, "radar_alt_m": 0, "lat": 0, "lon": 0,
	}}, 1)
	p, ok := full.Payload.(stats.VehicleLanded)
	if !ok {
		t.Fatalf("payload = %#v", full.Payload)
	}
	if p.Body != "mun" || p.VerticalSpeedMs != 1.4 || p.HorizontalSpeedMs != 0.3 ||
		p.CrewCount != 2 || !p.Survived {
		t.Errorf("payload = %#v", p)
	}
	for name, got := range map[string]*float64{"radar_alt_m": p.RadarAltM, "lat": p.Lat, "lon": p.Lon} {
		if got == nil || *got != 0 {
			t.Errorf("%s = %v, want a pointer to 0: an explicit zero is a reading", name, got)
		}
	}

	bare := decode(t, input{flight: flightN(1), typ: "vehicle.landed", payload: map[string]any{
		"body": "mun", "vertical_speed_ms": 1.4, "horizontal_speed_ms": 0.3,
		"crew_count": 2, "survived": true,
	}}, 2)
	q := bare.Payload.(stats.VehicleLanded)
	if q.RadarAltM != nil || q.Lat != nil || q.Lon != nil {
		t.Errorf("absent keys decoded to %v/%v/%v, want nil", q.RadarAltM, q.Lat, q.Lon)
	}
}
