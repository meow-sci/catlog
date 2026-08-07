package stats

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// The §5.6 launch boards: one per fold, every key a compile-time constant.
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

// fixedBoards is the §5.6 table, in display order: the "how did you survive
// that" records first, then the speed records, then the counters, then the one
// career-time board whose key is a constant.
//
// Every entry here is a board because a fold with that name exists, so the list
// is a property of the build. The two *families* below are not: their keys come
// out of the data.
var fixedBoards = func() []Board {
	rec := func(stat, title, unit string) Board { return Board{Stat: stat, Title: title, Unit: unit} }

	return []Board{
		rec(StatBiggestLithobrakeSurvived, "Biggest Lithobrake Survived", "m/s"),
		rec(StatPeakGSurvived, "Peak G Survived", "g"),
		rec(StatFastestSurfaceSpeed, "Fastest Surface Speed", "m/s"),
		rec(StatFastestOrbitalSpeed, "Fastest Orbital Speed", "m/s"),
		rec(StatKittenTumbles, "Kitten Tumbles", "tumbles"),
		rec(StatRUDTotal, "Rapid Unscheduled Disassemblies", "RUDs"),
		rec(StatOrbitsAchieved, "Orbits Achieved", "orbits"),
		rec(StatSOIBodies, "Bodies Visited", "bodies"),
		rec(StatDockings, "Dockings", "dockings"),
		rec(StatStagings, "Stagings", "stagings"),
		rec(StatKittensRecovered, "Kittens Recovered", "kittens"),
		rec(StatDistanceTravelled, "Distance Travelled", "m"),
		{Stat: StatFastestToOrbit, Title: "Fastest to Orbit", Unit: "ms", Ascending: true, Career: true},
	}
}()

var fixedByStat = func() map[string]Board {
	m := make(map[string]Board, len(fixedBoards))
	for _, b := range fixedBoards {
		m[b.Stat] = b
	}
	return m
}()

// FixedBoards returns the boards every build publishes whether or not anybody is
// on them, in display order.
func FixedBoards() []Board { return slices.Clone(fixedBoards) }

// --- the dynamic board families ------------------------------------------------
//
// Two board keys are not constants. `fastest_to_<body>` and `rud_<cause>` take
// their second half from the event stream — a celestial body, a destruction
// cause — and catlog holds no list of either.
//
// It held one, and that was wrong. KSA's celestial systems are hand-authored
// content that ships as data and that mods extend or replace, and docs/events.md
// has always said `body` is "opaque to server"; a compiled-in list of bodies is
// guaranteed to be wrong for somebody, and wrong *silently* — a player who
// reaches a body we never heard of simply gets no board. The same argument
// applies to `cause`: a destruction cause a future build adds would count
// towards `rud_total` and vanish from the per-cause boards.
//
// So a family board exists because a value appeared in the data. Two rules stop
// that from being a way to mint leaderboards, and neither is a model of what a
// player "ought" to be able to do (docs/CONSTITUTION.md §8):
//
//   - the value must be able to *be* half of a stat key: see [statSuffix]. That
//     is protocol hygiene — a stat key is a URL path segment — and not an
//     opinion about which bodies are real.
//   - a family board is *listed* only once [DefaultMinPlayers] distinct players
//     hold a value on it. A leaderboard with one entrant is not a leaderboard,
//     and one modified client cannot fill the public index on its own.
//
// The threshold is a listing rule and never data loss: the per-player value is
// written for every body and every cause regardless, so lowering it publishes
// history that was already there rather than starting to collect it.

// DefaultMinPlayers is how many distinct players a family board needs before
// `GET /v1/leaderboards` lists it. Configurable as `[boards] min_players`.
const DefaultMinPlayers = 2

// MaxStatSuffixLen bounds the content-derived half of a family stat key. Long
// enough for any name a body could plausibly carry, short enough that the key
// stays a comfortable URL path segment.
const MaxStatSuffixLen = 40

// family is one dynamic board family: a stat-key prefix whose members come from
// the data, listed after the fixed board they belong with.
type family struct {
	// prefix is the stat-key prefix, trailing underscore included.
	prefix string
	// after is the fixed board this family's members are listed under.
	after string
	// board derives one member's metadata from its stat key and suffix.
	board func(stat, suffix string) Board
}

var families = []family{{
	prefix: "rud_",
	after:  StatRUDTotal,
	board: func(stat, cause string) Board {
		return Board{Stat: stat, Title: "RUDs — " + titleize(cause), Unit: "RUDs"}
	},
}, {
	prefix: "fastest_to_",
	after:  StatFastestToOrbit,
	board: func(stat, body string) Board {
		return Board{Stat: stat, Title: "Fastest to " + titleize(body), Unit: "ms", Ascending: true, Career: true}
	},
}}

// FastestToStat is the career-time board key for a body, reporting false when
// the body's name cannot be half of a stat key.
func FastestToStat(body string) (string, bool) { return familyStat("fastest_to_", body) }

// RUDStat is the per-cause board key for a §4.2 cause, reporting false when the
// cause cannot be half of a stat key.
func RUDStat(cause string) (string, bool) { return familyStat("rud_", cause) }

// familyStat builds a family stat key out of a value the wire carried.
func familyStat(prefix, value string) (string, bool) {
	suffix, ok := statSuffix(value)
	if !ok {
		return "", false
	}
	stat := prefix + suffix
	if _, fixed := fixedByStat[stat]; fixed {
		// A body literally named "orbit", or a cause named "total", would
		// otherwise land on a fixed board and merge with it. The value keeps
		// every other consequence it had; it just does not get this key.
		return "", false
	}
	return stat, true
}

// statSuffix normalises a wire value into the half of a stat key it can be, or
// reports that it cannot be one.
//
// Lowercased because §4.2 says these values already are, so folding case can
// only merge two spellings of one name. Then `[a-z0-9]` followed by
// `[a-z0-9._-]`, bounded by [MaxStatSuffixLen], because a stat key is a URL path
// segment and an entry in a public index. That is the whole rule, and it is
// protocol hygiene rather than an allow-list: a value that fails it keeps every
// other consequence it had — it still lands in `player_body`, still counts
// towards `soi_bodies` or `rud_total`, still keeps its arrival time. It just
// gets no board of its own.
func statSuffix(v string) (string, bool) {
	if v == "" || len(v) > MaxStatSuffixLen {
		return "", false
	}
	out := strings.ToLower(v)
	for i := range len(out) {
		c := out[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return "", false
		}
	}
	return out, true
}

// titleize renders the content half of a stat key as a display name: "luna" →
// "Luna", "ground_impact" → "Ground Impact".
//
// Derived, never looked up. A table of pretty names is a table of the bodies we
// happen to have heard of, which is the thing this file no longer keeps.
func titleize(suffix string) string {
	words := strings.FieldsFunc(suffix, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	if len(words) == 0 {
		return suffix
	}
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// Describe derives a board's metadata from its stat key, reporting false for a
// key that is neither a fixed board nor a well-formed member of a family.
//
// It reads nothing: a board's title, unit and direction are a pure function of
// its key. That is what lets a profile row, a board page and a rebuilt
// projection agree about a body nobody had heard of when this was written.
func Describe(stat string) (Board, bool) {
	if b, ok := fixedByStat[stat]; ok {
		return b, true
	}
	for _, f := range families {
		suffix, cut := strings.CutPrefix(stat, f.prefix)
		if !cut {
			continue
		}
		// The key must be exactly what familyStat would have produced, so that
		// one board never has two spellings.
		norm, ok := statSuffix(suffix)
		if !ok || norm != suffix {
			return Board{}, false
		}
		return f.board(stat, suffix), true
	}
	return Board{}, false
}

// Known reports the metadata for a stat a server may serve a board page for.
// players is how many players hold a value on it (`player_stat` has one row per
// player per stat, so that is a row count).
//
// A fixed board is always servable — an empty board is still a board. A family
// board is servable once anybody at all is on it, which is what makes the link
// on a profile row resolve. Listing it publicly is the stricter question
// [Catalog] answers.
func Known(stat string, players int64) (Board, bool) {
	b, ok := Describe(stat)
	if !ok {
		return Board{}, false
	}
	if _, fixed := fixedByStat[stat]; fixed {
		return b, true
	}
	return b, players > 0
}

// Catalog is the board list a server holding these per-board player counts
// publishes, in display order: every fixed board whether or not anyone is on it,
// plus each family's members that at least minPlayers distinct players have
// reached, listed under the fixed board they belong with.
//
// counts is `player_stat` grouped by stat. Because (player_id, stat) is that
// table's primary key, the count *is* the number of distinct players on the
// board — the threshold needs no second query and no `DISTINCT`.
//
// Family members are ordered by key. Any other order would need to know
// something about bodies (which one is nearer the star), which is exactly the
// knowledge that does not belong here.
func Catalog(counts map[string]int64, minPlayers int) []Board {
	if minPlayers < 1 {
		minPlayers = DefaultMinPlayers
	}
	members := map[string][]Board{}
	for stat, n := range counts {
		if n < int64(minPlayers) {
			continue
		}
		if _, fixed := fixedByStat[stat]; fixed {
			continue
		}
		f, ok := familyOf(stat)
		if !ok {
			continue // a board this build no longer publishes; awaiting a rebuild
		}
		b, ok := Describe(stat)
		if !ok {
			continue
		}
		members[f.after] = append(members[f.after], b)
	}
	for _, group := range members {
		slices.SortFunc(group, func(a, b Board) int { return strings.Compare(a.Stat, b.Stat) })
	}

	out := make([]Board, 0, len(fixedBoards)+len(counts))
	for _, b := range fixedBoards {
		out = append(out, b)
		out = append(out, members[b.Stat]...)
	}
	return out
}

func familyOf(stat string) (family, bool) {
	for _, f := range families {
		if strings.HasPrefix(stat, f.prefix) {
			return f, true
		}
	}
	return family{}, false
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
	return putRecord(ctx, tx, ev, StatBiggestLithobrakeSurvived, p.SpeedMs, map[string]any{
		"body":     p.Body,
		"flight":   ids.String(ev.FlightID),
		"energy_j": p.EnergyJ,
	})
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
	return putRecord(ctx, tx, ev, StatPeakGSurvived, *p.PeakG, map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"t1_sim": p.T1Sim,
	})
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
	return putRecord(ctx, tx, ev, f.stat, value, map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"t1_sim": p.T1Sim,
	})
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
	return addCount(ctx, tx, ev, f.stat, 1)
}

// rudFold implements `rud_total` and the `rud_<cause>` family (§5.6).
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
	if err := addCount(ctx, tx, ev, StatRUDTotal, 1); err != nil {
		return err
	}
	// The per-cause board comes from the cause the event carried, not from a
	// list of the causes this build happens to know: a cause a newer game or mod
	// introduces gets its own board rather than disappearing into the total.
	// Only a cause that cannot be half of a stat key counts towards `rud_total`
	// alone.
	stat, ok := RUDStat(p.Cause)
	if !ok {
		return nil
	}
	return addCount(ctx, tx, ev, stat, 1)
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
	return addCount(ctx, tx, ev, StatOrbitsAchieved, 1)
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
	return addCount(ctx, tx, ev, StatSOIBodies, 1)
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
	return addCount(ctx, tx, ev, StatKittensRecovered, float64(p.CrewCount))
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
	return setValue(ctx, tx, ev, StatDistanceTravelled, total.Float64)
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

// careerMillis converts a career-relative time from the seconds the wire
// carries to the milliseconds the boards publish.
//
// `sim_t` stays seconds on the wire and in `player_body.first_sim_t`: stored
// events are immutable and there is no envelope-level upcaster, so changing the
// unit of a logged field would strand every event already written in a unit
// nothing could identify (docs/DECISIONS.md, WP-CLOCK). A projection has no such
// problem — it is rebuildable by definition — so the conversion happens here, at
// the one place a career time becomes a board value.
func careerMillis(seconds float64) float64 { return seconds * 1000 }

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
	return putBest(ctx, tx, ev, StatFastestToOrbit, careerMillis(t), map[string]any{
		"career": ev.Career,
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
	})
}

// toBodyFold implements the `fastest_to_<body>` family: how long into a career
// the player first entered each body's sphere of influence.
//
// The body comes from the event and nothing checks it against a list — that is
// the point of the family (see the commentary above [DefaultMinPlayers]). It
// also writes `player_body.first_sim_t` for *every* body, including one whose
// name cannot be a stat key at all, so no arrival time is ever lost.
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

	stat, ok := FastestToStat(p.ToBody)
	if !ok {
		// A name that cannot be half of a stat key still counts towards
		// `soi_bodies` and still keeps its arrival time above; it just has no
		// board of its own.
		return nil
	}
	return putBest(ctx, tx, ev, stat, careerMillis(t), map[string]any{
		"career": ev.Career,
		"from":   p.FromBody,
		"flight": ids.String(ev.FlightID),
	})
}
