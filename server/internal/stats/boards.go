package stats

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// The §5.6 launch boards. Every stat key catlog serves is one of these
// constants; nothing constructs a stat key from user input.
const (
	StatBiggestLithobrakeSurvived = "biggest_lithobrake_survived"
	StatPeakGSurvived             = "peak_g_survived"
	StatFastestSurfaceSpeed       = "fastest_surface_speed"
	StatFastestOrbitalSpeed       = "fastest_orbital_speed"
	StatKittenTumbles             = "kitten_tumbles"
	StatRUDTotal                  = "rud_total"
	StatOrbitsAchieved            = "orbits_achieved"
	StatSOIBodies                 = "soi_bodies"
	StatDockings                  = "dockings"
	StatStagings                  = "stagings"
	StatKittensRecovered          = "kittens_recovered"
	StatDistanceTravelled         = "distance_travelled"
	StatFastestToOrbit            = "fastest_to_orbit"
)

// TimedBodies is the allow-list of celestial bodies that get a "fastest career
// time to reach" board, in system order. It is the stock KSA body set for build
// 2026.8.5.5168, lowercased: `Content/Core/Astronomicals.xml` declares exactly
// these as permanent members of the system (`StellarBody`, `PlanetaryBody`,
// `AtmosphericBody`, `MinorBody`).
//
// It is an allow-list for the same reason [RUDCauses] is: `vehicle.soi.to_body`
// is an opaque string from the client, and a board key built from client text
// would let anyone mint a leaderboard — and a million of them (§6, "stat keys
// are compile-time constants"). A body outside this set still lands in
// `player_body` and still counts towards `soi_bodies`; it simply gets no board
// of its own.
//
// Comets (`PeriodicComet`, `InterstellarComet` in the same file) are left out on
// purpose: they are the transient half of the system, and the line "the bodies
// KSA ships as permanent members" is one a reader can check. Adding them, or a
// body a future build introduces, is one entry here plus a rebuild — the
// per-body arrival *times* are recorded for every body regardless, in
// `player_body.first_sim_t`, so nothing is lost in the meantime.
var TimedBodies = []string{
	"sol",
	"mercury",
	"venus",
	"earth",
	"luna",
	"mars",
	"phobos",
	"deimos",
	"jupiter",
	"saturn",
	"uranus",
}

// FastestToStat is the per-body board key for a [TimedBodies] entry.
func FastestToStat(body string) string { return "fastest_to_" + body }

// RUDCauses is the §4.2 `vehicle.rud.cause` enum, in the order the per-cause
// boards are listed. A cause outside this set counts towards `rud_total` only —
// a newer mod must not be able to mint a leaderboard key.
var RUDCauses = []string{
	"ground_impact",
	"ocean_impact",
	"collision",
	"excessive_g_force",
	"aerodynamic_forces",
	"hydrodynamic_forces",
}

// RUDStat is the per-cause board key for a §4.2 cause.
func RUDStat(cause string) string { return "rud_" + cause }

// Board is the metadata `GET /v1/leaderboards` publishes for one stat (§4.8).
type Board struct {
	// Stat is the key `/v1/leaderboards/{stat}` takes.
	Stat string `json:"stat"`
	// Title is the human name; the web UI renders it verbatim.
	Title string `json:"title"`
	// Unit labels the value column. Not a conversion factor: values are always
	// in the unit the event carried (metres, m/s, g, seconds) or a plain count.
	Unit string `json:"unit"`
	// Ascending reports that a *smaller* value ranks higher. True for every
	// "fastest career time to X" board and false for everything else, and it is
	// published so a client never has to guess which way a board reads.
	Ascending bool `json:"ascending"`
	// Career is true when the value is a career-relative time and the row's
	// context therefore carries `career` (and may be qualified by the career's
	// rewind mark). See career.go.
	Career bool `json:"career"`
}

// boards is the §5.6 table, in display order: the three "how did you survive
// that" records first, then the speed records, then the counters.
var boards = func() []Board {
	rec := func(stat, title, unit string) Board { return Board{Stat: stat, Title: title, Unit: unit} }
	fastest := func(stat, title string) Board {
		return Board{Stat: stat, Title: title, Unit: "s", Ascending: true, Career: true}
	}

	out := []Board{
		rec(StatBiggestLithobrakeSurvived, "Biggest Lithobrake Survived", "m/s"),
		rec(StatPeakGSurvived, "Peak G Survived", "g"),
		rec(StatFastestSurfaceSpeed, "Fastest Surface Speed", "m/s"),
		rec(StatFastestOrbitalSpeed, "Fastest Orbital Speed", "m/s"),
		rec(StatKittenTumbles, "Kitten Tumbles", "tumbles"),
		rec(StatRUDTotal, "Rapid Unscheduled Disassemblies", "RUDs"),
	}
	for _, cause := range RUDCauses {
		out = append(out, rec(RUDStat(cause), "RUDs — "+causeTitle(cause), "RUDs"))
	}
	out = append(out,
		rec(StatOrbitsAchieved, "Orbits Achieved", "orbits"),
		rec(StatSOIBodies, "Bodies Visited", "bodies"),
		rec(StatDockings, "Dockings", "dockings"),
		rec(StatStagings, "Stagings", "stagings"),
		rec(StatKittensRecovered, "Kittens Recovered", "kittens"),
		rec(StatDistanceTravelled, "Distance Travelled", "m"),
	)

	// The career-time boards last, as their own block: they are the only ones
	// where the smallest number wins.
	out = append(out, fastest(StatFastestToOrbit, "Fastest to Orbit"))
	for _, body := range TimedBodies {
		out = append(out, fastest(FastestToStat(body), "Fastest to "+bodyTitle(body)))
	}
	return out
}()

var boardByStat = func() map[string]Board {
	m := make(map[string]Board, len(boards))
	for _, b := range boards {
		m[b.Stat] = b
	}
	return m
}()

// Boards returns the board metadata in display order (§4.8 `/v1/leaderboards`).
func Boards() []Board { return slices.Clone(boards) }

// BoardFor looks a board up by stat key, reporting false for an unknown key —
// which is how the read API turns a typo'd URL into a 404 rather than an empty
// board.
func BoardFor(stat string) (Board, bool) {
	b, ok := boardByStat[stat]
	return b, ok
}

// bodyTitle capitalizes a [TimedBodies] entry for display. The wire form is
// lowercase and opaque (§4.2); this is presentation only.
func bodyTitle(body string) string {
	if body == "" {
		return body
	}
	return string(body[0]-32) + body[1:]
}

func causeTitle(cause string) string {
	switch cause {
	case "ground_impact":
		return "Ground Impact"
	case "ocean_impact":
		return "Ocean Impact"
	case "collision":
		return "Collision"
	case "excessive_g_force":
		return "Excessive G-Force"
	case "aerodynamic_forces":
		return "Aerodynamic Forces"
	case "hydrodynamic_forces":
		return "Hydrodynamic Forces"
	default:
		return cause
	}
}

// --- record folds ------------------------------------------------------------

// lithobrakeFold implements `biggest_lithobrake_survived` (§5.6): the fastest
// `vehicle.impact` the crew walked away from.
//
// §4.2's BEST-GUESS (D11) rule is `survived && crew_count ≥ 1 && !launch_pad`
// plus "no kitten.kia for the same flight within ±2.0 s". The last clause needs
// events that arrive after the impact, so the incremental path accepts the
// impact as-is and a rebuild applies the window (§5.6).
type lithobrakeFold struct{}

func (lithobrakeFold) Name() string { return StatBiggestLithobrakeSurvived }

func (lithobrakeFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleImpact](ev)
	if !ok || !p.Survived || p.LaunchPad || p.CrewCount < 1 || p.SpeedMs <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	if fs.Refined() && ev.HasSimTime && fs.KIANear(ev.FlightID, ev.SimTime) {
		// A kitten died within the window: whatever this was, it was not
		// "survived with crew" (§4.2).
		return nil
	}
	return putRecord(ctx, tx, ev.PlayerID, StatBiggestLithobrakeSurvived, p.SpeedMs, map[string]any{
		"body":     p.Body,
		"flight":   ids.String(ev.FlightID),
		"energy_j": p.EnergyJ,
	}, ev.Seq)
}

// peakGFold implements `peak_g_survived` (§5.6): the largest `telemetry.window`
// peak_g the flight lived through.
//
// The incremental path takes the simpler rule §5.6 sanctions — any window of an
// unflagged flight — because a window is folded long before its flight ends. A
// rebuild adds the condition §5.6 actually wants, `ended_reason == 'recovered'`,
// which it can evaluate because its first pass built flight_state for the whole
// history before any board was scored.
type peakGFold struct{}

func (peakGFold) Name() string { return StatPeakGSurvived }

func (peakGFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok {
		return nil
	}
	// Absent, not zero: StructuralLoad is only written under full physics, so a
	// missing peak_g means "no reading this window" and must not score
	// (docs/ksa-integration.md).
	if p.PeakG == nil || *p.PeakG <= 0 {
		return nil
	}
	st, found, err := fs.Flight(ctx, ev.FlightID)
	if err != nil {
		return err
	}
	if found && st.Flagged() {
		return nil
	}
	if fs.Refined() && !(found && st.Recovered()) {
		return nil
	}
	return putRecord(ctx, tx, ev.PlayerID, StatPeakGSurvived, *p.PeakG, map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"t1_sim": p.T1Sim,
	}, ev.Seq)
}

// speedFold implements `fastest_surface_speed` and `fastest_orbital_speed`
// (§5.6): the max of a `telemetry.window` aggregate.
//
// Deliberately *not* sourced from roster.snapshot.fastest_ms, which is the
// game's ecliptic-frame FastestSpeed and reads ~30 km/s standing still on Earth
// (docs/ksa-integration.md). Do not "improve" this.
type speedFold struct {
	stat    string
	surface bool
}

func (f speedFold) Name() string { return f.stat }

func (f speedFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok {
		return nil
	}
	value := p.OrbitalSpeedMs.Max
	if f.surface {
		value = p.SurfaceSpeedMs.Max
	}
	if value <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, tx, ev.PlayerID, f.stat, value, map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"t1_sim": p.T1Sim,
	}, ev.Seq)
}

// --- counter folds -----------------------------------------------------------

// countFold implements the boards that are simply "how many of this event":
// `kitten_tumbles`, `dockings`, `stagings`.
type countFold struct {
	stat      string
	eventType string
}

func (f countFold) Name() string { return f.stat }

func (f countFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	if ev.Type != f.eventType {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, tx, ev.PlayerID, f.stat, 1, ev.Seq)
}

// rudFold implements `rud_total` and the six `rud_<cause>` boards (§5.6).
type rudFold struct{}

func (rudFold) Name() string { return StatRUDTotal }

func (rudFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleRUD](ev)
	if !ok {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	if err := addCount(ctx, tx, ev.PlayerID, StatRUDTotal, 1, ev.Seq); err != nil {
		return err
	}
	// An unknown cause counts towards the total and nothing else: the per-cause
	// boards are a fixed enum, not a namespace the wire can extend.
	if !slices.Contains(RUDCauses, p.Cause) {
		return nil
	}
	return addCount(ctx, tx, ev.PlayerID, RUDStat(p.Cause), 1, ev.Seq)
}

// orbitsFold implements `orbits_achieved` (§5.6).
type orbitsFold struct{}

func (orbitsFold) Name() string { return StatOrbitsAchieved }

func (orbitsFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, tx, ev.PlayerID, StatOrbitsAchieved, 1, ev.Seq)
}

// soiFold implements `soi_bodies` (§5.6): the number of distinct bodies whose
// sphere of influence the player has entered, materialized in `player_body`.
//
// The count is advanced by one only when the INSERT OR IGNORE actually inserts,
// so the board never needs a `count(*)` and stays correct under replay.
type soiFold struct{}

func (soiFold) Name() string { return StatSOIBodies }

func (soiFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO player_body (player_id, kind, body, first_seq) VALUES (?, 'soi', ?, ?)`,
		ev.PlayerID, p.ToBody, ev.Seq)
	if err != nil {
		return fmt.Errorf("stats: record soi body %q: %w", p.ToBody, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("stats: record soi body %q: %w", p.ToBody, err)
	}
	if n == 0 {
		return nil // already visited
	}
	return addCount(ctx, tx, ev.PlayerID, StatSOIBodies, 1, ev.Seq)
}

// recoveredFold implements `kittens_recovered` (§5.6): the sum of
// `flight.ended.crew_count` over flights that ended `recovered`.
type recoveredFold struct{}

func (recoveredFold) Name() string { return StatKittensRecovered }

func (recoveredFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[FlightEnded](ev)
	if !ok || p.Reason != "recovered" || p.CrewCount < 1 {
		return nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, tx, ev.PlayerID, StatKittensRecovered, float64(p.CrewCount), ev.Seq)
}

// distanceFold implements `distance_travelled` (§5.6): the sum, over a player's
// kittens, of the furthest each has ever travelled.
//
// roster.snapshot carries running totals rather than deltas, so every column is
// folded with max() — a snapshot that arrives out of order, or a save reloaded
// from an earlier point, can only ever fail to advance a total, never rewind it.
// The event has no flight (§4.1 sends flight: null), so there is no flag to
// check.
type distanceFold struct{}

func (distanceFold) Name() string { return StatDistanceTravelled }

func (distanceFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, _ FlightStateReader) error {
	p, ok := payloadOf[RosterSnapshot](ev)
	if !ok || len(p.Kittens) == 0 {
		return nil
	}
	for _, k := range p.Kittens {
		if k.Kid == "" {
			continue
		}
		kia := 0
		if k.KIA {
			kia = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kitten (player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (player_id, kid) DO UPDATE SET
			   name = excluded.name,
			   travelled_m = max(kitten.travelled_m, excluded.travelled_m),
			   fastest_ms = max(kitten.fastest_ms, excluded.fastest_ms),
			   missions = max(kitten.missions, excluded.missions),
			   mission_time_s = max(kitten.mission_time_s, excluded.mission_time_s),
			   kia = max(kitten.kia, excluded.kia),
			   updated_seq = excluded.updated_seq`,
			ev.PlayerID, k.Kid, k.Name, k.TravelledM, k.FastestMs, k.Missions, k.MissionTimeS, kia, ev.Seq); err != nil {
			return fmt.Errorf("stats: upsert kitten %q: %w", k.Kid, err)
		}
	}

	var total sql.NullFloat64
	if err := tx.QueryRowContext(ctx,
		`SELECT sum(travelled_m) FROM kitten WHERE player_id = ?`, ev.PlayerID).Scan(&total); err != nil {
		return fmt.Errorf("stats: sum kitten distance: %w", err)
	}
	if !total.Valid || total.Float64 <= 0 {
		return nil
	}
	return setValue(ctx, tx, ev.PlayerID, StatDistanceTravelled, total.Float64, ev.Seq)
}

// --- career-time folds --------------------------------------------------------
//
// Both boards below are the same rule with a different milestone:
//
//	the smallest sim_t at which an unflagged flight of this player reached the
//	milestone, where sim_t is seconds since that career began.
//
// Three conditions, each a one-liner:
//
//   - the event must carry a career (§4.1). Without one, sim_t is a number with
//     no origin and cannot be a career time; the event is skipped rather than
//     guessed at.
//   - the event must carry a sim_t, and it must be ≥ 0. Absent is not zero — a
//     missing clock reading scored as 0 would be an unbeatable record.
//   - the flight must be unflagged, exactly like every other board (§5.6). A
//     teleport to orbit is not a fast ascent.
//
// The minimum is taken per player, not per career, which is what a leaderboard
// wants: your best career is the one that represents you. Which career it was is
// recorded in the row's context, and the read API qualifies it with that
// career's rewind mark (career.go).
//
// No "first in the career" bookkeeping is needed to make that correct. Within a
// career the clock only moves forward, so the earliest arrival *is* the minimum;
// the only way a later arrival can undercut an earlier one is an earlier save
// being loaded, which is precisely the case the rewind mark exists to state.

// careerTime reports the career-relative time an event happened at, and whether
// the event is eligible to set one of the boards above.
func careerTime(ctx context.Context, ev Event, fs FlightStateReader) (float64, bool, error) {
	if !ev.HasCareer() || !ev.HasSimTime || ev.SimTime < 0 {
		return 0, false, nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return 0, false, err
	}
	return ev.SimTime, true, nil
}

// toOrbitFold implements `fastest_to_orbit`: how long into a career the player
// first put something into a stable orbit around anything.
type toOrbitFold struct{}

func (toOrbitFold) Name() string { return StatFastestToOrbit }

func (toOrbitFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" {
		return nil
	}
	t, ok, err := careerTime(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}
	return putBest(ctx, tx, ev.PlayerID, StatFastestToOrbit, t, map[string]any{
		"career": ev.Career,
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
	}, ev.Seq)
}

// toBodyFold implements the `fastest_to_<body>` family: how long into a career
// the player first entered each body's sphere of influence.
//
// It writes `player_body.first_sim_t` for *every* body — including ones with no
// board — so that adding a body to [TimedBodies] later is a rebuild rather than
// a data-loss discovery.
type toBodyFold struct{}

func (toBodyFold) Name() string { return "fastest_to_body" }

func (toBodyFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	t, ok, err := careerTime(ctx, ev, fs)
	if err != nil || !ok {
		return err
	}

	// soiFold owns the row's existence; this only ever lowers its time. It runs
	// after soiFold (fold order in fold.go), so the row is already there. The
	// coalesce covers a row soiFold inserted on a career-less event, which has
	// no time yet — min() over NULL is NULL in SQLite.
	if _, err := tx.ExecContext(ctx,
		`UPDATE player_body SET first_sim_t = min(coalesce(first_sim_t, ?), ?)
		 WHERE player_id = ? AND kind = 'soi' AND body = ?`,
		t, t, ev.PlayerID, p.ToBody); err != nil {
		return fmt.Errorf("stats: record arrival time at %q: %w", p.ToBody, err)
	}

	if !slices.Contains(TimedBodies, p.ToBody) {
		// A body outside the stock set counts towards `soi_bodies` and keeps its
		// arrival time, but it does not get to mint a board (§6).
		return nil
	}
	return putBest(ctx, tx, ev.PlayerID, FastestToStat(p.ToBody), t, map[string]any{
		"career": ev.Career,
		"from":   p.FromBody,
		"flight": ids.String(ev.FlightID),
	}, ev.Seq)
}
