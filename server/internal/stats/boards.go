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
)

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
	// in the unit the event carried (metres, m/s, g) or a plain count.
	Unit string `json:"unit"`
}

// boards is the §5.6 table, in display order: the three "how did you survive
// that" records first, then the speed records, then the counters.
var boards = func() []Board {
	out := []Board{
		{StatBiggestLithobrakeSurvived, "Biggest Lithobrake Survived", "m/s"},
		{StatPeakGSurvived, "Peak G Survived", "g"},
		{StatFastestSurfaceSpeed, "Fastest Surface Speed", "m/s"},
		{StatFastestOrbitalSpeed, "Fastest Orbital Speed", "m/s"},
		{StatKittenTumbles, "Kitten Tumbles", "tumbles"},
		{StatRUDTotal, "Rapid Unscheduled Disassemblies", "RUDs"},
	}
	for _, cause := range RUDCauses {
		out = append(out, Board{RUDStat(cause), "RUDs — " + causeTitle(cause), "RUDs"})
	}
	return append(out,
		Board{StatOrbitsAchieved, "Orbits Achieved", "orbits"},
		Board{StatSOIBodies, "Bodies Visited", "bodies"},
		Board{StatDockings, "Dockings", "dockings"},
		Board{StatStagings, "Stagings", "stagings"},
		Board{StatKittensRecovered, "Kittens Recovered", "kittens"},
		Board{StatDistanceTravelled, "Distance Travelled", "m"},
	)
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
