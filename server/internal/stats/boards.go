package stats

import (
	"context"
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

	StatMaxQSurvived        = "max_q_survived"
	StatBiggestImpactEnergy = "biggest_impact_energy"
	StatFastestEntry        = "fastest_entry"
	StatHighestAltitude     = "highest_altitude"
	StatHighestApoapsis     = "highest_apoapsis"
	StatLowestOrbit         = "lowest_orbit"
	StatRoundestOrbit       = "roundest_orbit"
	StatSteepestOrbit       = "steepest_orbit"
	StatSoftestTouchdown    = "softest_touchdown"
	StatHeaviestLaunch      = "heaviest_launch"
	StatMostParts           = "most_parts"
	StatBiggestCrew         = "biggest_crew"
	StatBiggestRecovery     = "biggest_recovery"
	StatMostStages          = "most_stages"
	StatLongestEVA          = "longest_eva"
	StatLandedBodies        = "landed_bodies"
	StatSplashdowns         = "splashdowns"
	StatEVAs                = "evas"
	StatFlameouts           = "flameouts"
	StatEngineIgnitions     = "engine_ignitions"
	StatTopKittenDistance   = "top_kitten_distance"
	StatTopKittenMissions   = "top_kitten_missions"

	// The wire-v2 boards. Three read keys that did not exist before
	// (`mass_kg` on an orbit, `stage_count` on a launch, `radar_alt_m` on a
	// window) and two read the event wire v2 added.
	StatHeaviestToOrbit = "heaviest_to_orbit"
	StatSoftestLanding  = "softest_landing"
	StatLandings        = "landings"
	StatLowestPass      = "lowest_pass"
	StatBiggestStack    = "biggest_stack"
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
// that" records first, then the speed and shape records, then what was on the
// pad, then the counters, then the one career-time board whose key is a
// constant.
//
// Every entry here is a board because a fold with that name exists, so the list
// is a property of the build. The two *families* below are not: their keys come
// out of the data.
//
// Four boards have an **empty unit** — `roundest_orbit` (an eccentricity),
// `most_parts`, `most_stages` and `biggest_stack` (bare counts of a thing whose
// name is already in the title). An empty unit is a real answer here rather
// than a missing one: units.Split renders it as the number alone, and inventing
// a label like "parts" would put it on the page twice.
var fixedBoards = func() []Board {
	rec := func(stat, title, unit string) Board { return Board{Stat: stat, Title: title, Unit: unit} }
	// best is rec's mirror: a board where a *smaller* value ranks higher.
	best := func(stat, title, unit string) Board {
		return Board{Stat: stat, Title: title, Unit: unit, Ascending: true}
	}

	return []Board{
		rec(StatBiggestLithobrakeSurvived, "Biggest Lithobrake Survived", "m/s"),
		rec(StatPeakGSurvived, "Peak G Survived", "g"),
		rec(StatMaxQSurvived, "Max Q Survived", "Pa"),
		rec(StatBiggestImpactEnergy, "Biggest Bang Survived", "J"),
		rec(StatFastestSurfaceSpeed, "Fastest Surface Speed", "m/s"),
		rec(StatFastestOrbitalSpeed, "Fastest Orbital Speed", "m/s"),
		rec(StatFastestEntry, "Fastest Atmospheric Entry", "m/s"),
		rec(StatHighestAltitude, "Highest Altitude", "m"),
		best(StatLowestPass, "Lowest Pass", "m"),
		rec(StatHighestApoapsis, "Highest Apoapsis", "m"),
		best(StatLowestOrbit, "Lowest Stable Orbit", "m"),
		best(StatRoundestOrbit, "Roundest Orbit", ""),
		rec(StatSteepestOrbit, "Most Inclined Orbit", "deg"),
		best(StatSoftestTouchdown, "Softest Touchdown", "m/s"),
		best(StatSoftestLanding, "Softest Landing", "m/s"),
		rec(StatHeaviestLaunch, "Heaviest Launch", "kg"),
		rec(StatHeaviestToOrbit, "Heaviest Payload To Orbit", "kg"),
		rec(StatMostParts, "Most Parts", ""),
		rec(StatBiggestStack, "Most Stages Built", ""),
		rec(StatBiggestCrew, "Biggest Crew", "kittens"),
		rec(StatBiggestRecovery, "Most Kittens Home At Once", "kittens"),
		rec(StatMostStages, "Most Stages", ""),
		rec(StatLongestEVA, "Longest Spacewalk", "s"),
		rec(StatKittenTumbles, "Kitten Tumbles", "tumbles"),
		rec(StatRUDTotal, "Rapid Unscheduled Disassemblies", "RUDs"),
		rec(StatOrbitsAchieved, "Orbits Achieved", "orbits"),
		rec(StatSOIBodies, "Bodies Visited", "bodies"),
		rec(StatLandedBodies, "Bodies Landed On", "bodies"),
		rec(StatLandings, "Landings", "landings"),
		rec(StatDockings, "Dockings", "dockings"),
		rec(StatStagings, "Stagings", "stagings"),
		rec(StatSplashdowns, "Splashdowns", "splashdowns"),
		rec(StatEVAs, "Spacewalks", "EVAs"),
		rec(StatFlameouts, "Ran Dry", "flameouts"),
		rec(StatEngineIgnitions, "Engines Lit", "ignitions"),
		rec(StatKittensRecovered, "Kittens Recovered", "kittens"),
		rec(StatDistanceTravelled, "Distance Travelled", "m"),
		rec(StatTopKittenDistance, "Furthest-Travelled Kitten", "m"),
		rec(StatTopKittenMissions, "Most Missions Flown", "missions"),
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

// survivedImpact reports whether a `vehicle.impact` may score, which is the
// whole eligibility rule `biggest_lithobrake_survived` and
// `biggest_impact_energy` share — they are the same crash read two ways, and
// they must agree about which crashes count.
//
// §4.2's BEST-GUESS (D11) rule is `survived && crew_count ≥ 1 && !launch_pad`
// plus "no kitten.kia for the same flight within ±2.0 s". The last clause needs
// events that arrive after the impact, so the incremental path accepts the
// impact as-is and a rebuild applies the window (§5.6).
func survivedImpact(ctx context.Context, b *Batch, ev Event, p VehicleImpact) (bool, error) {
	if !p.Survived || p.LaunchPad || p.CrewCount < 1 {
		return false, nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return false, err
	}
	if b.Refined() && ev.HasSimTime && b.KIANear(ev.FlightID, ev.SimTime) {
		// A kitten died within the window: whatever this was, it was not
		// "survived with crew" (§4.2).
		return false, nil
	}
	return true, nil
}

// impactContext is the shared context of both impact boards: whichever figure
// is not this board's value is the one a reader wants next to it.
func impactContext(ev Event, p VehicleImpact) map[string]any {
	return map[string]any{
		"body":     p.Body,
		"flight":   ids.String(ev.FlightID),
		"speed_ms": p.SpeedMs,
		"energy_j": p.EnergyJ,
	}
}

// lithobrakeFold implements `biggest_lithobrake_survived` (§5.6): the fastest
// `vehicle.impact` the crew walked away from.
type lithobrakeFold struct{}

func (lithobrakeFold) Name() string { return StatBiggestLithobrakeSurvived }

func (lithobrakeFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleImpact](ev)
	if !ok || p.SpeedMs <= 0 {
		return nil
	}
	ok, err := survivedImpact(ctx, b, ev, p)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatBiggestLithobrakeSurvived, p.SpeedMs, impactContext(ev, p))
}

// impactEnergyFold implements `biggest_impact_energy`: the same survived crash
// measured in joules rather than in metres per second.
//
// Not a duplicate of the board above. `speed_ms` is a **closing normal speed**
// for a ground impact and a reconstructed √(2E/m) scalar for a splash, so it
// says nothing about how much vehicle was moving; `energy_j` is the game's own
// ImpactKineticEnergy and therefore ranks a heavy lander touching down hard
// above a probe hitting fast (docs/ksa-integration.md).
type impactEnergyFold struct{}

func (impactEnergyFold) Name() string { return StatBiggestImpactEnergy }

func (impactEnergyFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleImpact](ev)
	if !ok || p.EnergyJ <= 0 {
		return nil
	}
	ok, err := survivedImpact(ctx, b, ev, p)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatBiggestImpactEnergy, p.EnergyJ, impactContext(ev, p))
}

// survivedLoad reports whether a `telemetry.window` structural-load reading may
// score. It is the eligibility `peak_g_survived` and `max_q_survived` share,
// and they share it because they are the same reading: both come off
// `Vehicle.StructuralLoad`, both are `*float64`, and both are boards about
// living through something.
//
// Absent is not zero. StructuralLoad is only written under full physics, so a
// missing reading means "no data this window" and must not score — a nil
// treated as a real 0 would fill an ascending-looking record board with fake
// minima (docs/ksa-integration.md).
//
// The incremental path takes the simpler rule §5.6 sanctions — any window of an
// unflagged flight — because a window is folded long before its flight ends. A
// rebuild adds the condition §5.6 actually wants, `ended_reason == 'recovered'`,
// which it can evaluate because its first pass built flight_state for the whole
// history before any board was scored.
func survivedLoad(ctx context.Context, b *Batch, ev Event, reading *float64) (bool, error) {
	if reading == nil || *reading <= 0 {
		return false, nil
	}
	st, found, err := b.Flight(ctx, ev.FlightID)
	if err != nil {
		return false, err
	}
	if found && st.Flagged() {
		return false, nil
	}
	if b.Refined() && !(found && st.Recovered()) {
		return false, nil
	}
	return true, nil
}

// windowContext is the shared context of every board sourced from a
// `telemetry.window`: which body, which flight, and when the window closed.
func windowContext(ev Event, p TelemetryWindow) map[string]any {
	return map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"t1_sim": p.T1Sim,
	}
}

// peakGFold implements `peak_g_survived` (§5.6): the largest `telemetry.window`
// peak_g the flight lived through.
type peakGFold struct{}

func (peakGFold) Name() string { return StatPeakGSurvived }

func (peakGFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok {
		return nil
	}
	ok, err := survivedLoad(ctx, b, ev, p.PeakG)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatPeakGSurvived, *p.PeakG, windowContext(ev, p))
}

// maxQFold implements `max_q_survived`: the largest dynamic pressure a flight
// came home from, in pascals.
//
// The g board's twin in every respect, [survivedLoad] included — same source
// struct, same omit-don't-zero rule on the wire, same rebuild-only requirement
// that the flight ended `recovered`. Peak g is how hard the airframe was
// squeezed; max q is how hard the air was pushing, and an ascent profile can be
// brutal on one and gentle on the other.
type maxQFold struct{}

func (maxQFold) Name() string { return StatMaxQSurvived }

func (maxQFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok {
		return nil
	}
	ok, err := survivedLoad(ctx, b, ev, p.MaxQPa)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatMaxQSurvived, *p.MaxQPa, windowContext(ev, p))
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

func (f speedFold) Apply(ctx context.Context, b *Batch, ev Event) error {
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
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, f.stat, value, windowContext(ev, p))
}

// altitudeFold implements `highest_altitude`: the highest a vehicle got above
// its parent's mean radius.
//
// Barometric altitude, not radar altitude — `alt_m` is
// `PositionCci.Length() - Parent.MeanRadius`, so a mountaintop landing scores
// its elevation and a low pass over a canyon does not
// (docs/ksa-integration.md). It takes the plain flag exclusion rather than
// [survivedLoad]'s recovered-flight rule: an altitude is a position, always
// sampled and always meaningful, and a probe that never came back still got
// there.
type altitudeFold struct{}

func (altitudeFold) Name() string { return StatHighestAltitude }

func (altitudeFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok || p.AltM.Max <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatHighestAltitude, p.AltM.Max, windowContext(ev, p))
}

// lowestPassFold implements `lowest_pass`: the closest a vehicle came to the
// ground without ending up on it.
//
// The counterpart of `highest_altitude`, and deliberately the *other* altitude.
// `alt_m` is barometric — above the parent's mean radius — so a low pass down a
// canyon reads as high and a mountaintop hover reads as low; `radar_alt_m` is
// the terrain-relative reading the mod folds over only the samples that had one.
//
// Two gates, both load-bearing. **An absent aggregate never scores**: a window
// spent in orbit has no terrain below it and the mod omits the key entirely
// rather than sending zeros, the peak_g rule. And the minimum must be strictly
// positive, because this board is ascending and a 0 is what a vehicle sitting on
// the ground reads — an unbeatable record that every flight would tie on its way
// to the pad (PROJ-088). A landing is not a pass; `softest_landing` is the board
// for arriving.
type lowestPassFold struct{}

func (lowestPassFold) Name() string { return StatLowestPass }

func (lowestPassFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[TelemetryWindow](ev)
	if !ok || p.RadarAltM == nil || p.RadarAltM.Min <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putBest(ctx, b, ev, StatLowestPass, p.RadarAltM.Min, windowContext(ev, p))
}

// entryFold implements `fastest_entry`: the fastest a vehicle was moving as it
// crossed into an atmosphere.
//
// `exited` is ignored. Leaving an atmosphere fast is an ascent, which the speed
// boards already rank; entering one fast is the part that usually ends in
// `rud_aerodynamic_forces`, and that is the board.
//
// The speed is surface-relative (`curr.SurfaceSpeedMs`), which is the right
// frame for an entry: what matters is the air the vehicle is hitting, not the
// body's inertial motion.
type entryFold struct{}

func (entryFold) Name() string { return StatFastestEntry }

func (entryFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleAtmosphere](ev)
	if !ok || p.Dir != "entered" || p.SpeedMs <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putRecord(ctx, b, ev, StatFastestEntry, p.SpeedMs, map[string]any{
		"body":            p.Body,
		"flight":          ids.String(ev.FlightID),
		"dyn_pressure_pa": p.DynPressurePa,
	})
}

// orbitRecordFold implements the four boards that describe the *shape* of an
// orbit a player actually reached: `highest_apoapsis`, `lowest_orbit`,
// `roundest_orbit` and `steepest_orbit`.
//
// One type registered four times rather than four types, for the reason
// speedFold is registered twice: the eligibility is identical and only the
// field and the direction differ.
//
// Every value is gated on `> 0`, and that gate is doing real work rather than
// being defensive. `ap_m` is written as 0.0 whenever the conic is not Bound;
// an `ecc` or `inc_deg` of exactly 0 is what a failed or unwritten read leaves
// behind; and `pe_m` is computed unconditionally, so it can legitimately be
// negative for an orbit whose periapsis is underground (docs/ksa-integration.md).
// On the two ascending boards a zero would be an unbeatable record that nobody
// flew, which is why `roundest_orbit` must refuse a perfectly circular-looking
// 0 rather than crown it.
type orbitRecordFold struct {
	stat string
	// best makes this a min-record: rounder and lower are smaller numbers.
	best bool
	// value picks this board's field out of the orbit.
	value func(VehicleOrbit) float64
}

func (f orbitRecordFold) Name() string { return f.stat }

func (f orbitRecordFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" {
		return nil
	}
	value := f.value(p)
	if value <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	// The whole orbit, on all four boards. A reader looking at a periapsis
	// wants the apoapsis beside it, and carrying one context shape means the
	// four rows of one orbit are the same blob rather than four partial views
	// of it.
	cx := map[string]any{
		"body":    p.Body,
		"flight":  ids.String(ev.FlightID),
		"ap_m":    p.ApM,
		"pe_m":    p.PeM,
		"ecc":     p.Ecc,
		"inc_deg": p.IncDeg,
	}
	if f.best {
		return putBest(ctx, b, ev, f.stat, value, cx)
	}
	return putRecord(ctx, b, ev, f.stat, value, cx)
}

// orbitMassFold implements `heaviest_to_orbit`: the heaviest thing a player has
// ever put into a stable orbit around anything.
//
// Not the same claim as `heaviest_launch`, and the pair is the point. What left
// the pad includes the propellant that will be spent getting off it; what is
// still there when the milestone fires is the payload. Paired, the two are the
// only honest efficiency-shaped number reachable without reading propellant,
// which is why the mod added `mass_kg` to `vehicle.orbit` rather than letting a
// reader diff a launch mass against a telemetry window that may be half a
// window stale.
//
// `escaped` is excluded exactly as it is on the four shape boards: an escape is
// not an orbit anybody reached. The `> 0` gate is what keeps ver 1 history off
// the board — `mass_kg` did not exist before wire v2, so every stored orbit
// older than the bump decodes as 0, and 0 kg is not a payload.
type orbitMassFold struct{}

func (orbitMassFold) Name() string { return StatHeaviestToOrbit }

func (orbitMassFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" || p.MassKg <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	// The orbit it reached, not the whole shape blob: this board ranks the
	// vehicle, and the apsides are what say where it got to.
	return putRecord(ctx, b, ev, StatHeaviestToOrbit, p.MassKg, map[string]any{
		"body":   p.Body,
		"flight": ids.String(ev.FlightID),
		"ap_m":   p.ApM,
		"pe_m":   p.PeM,
	})
}

// launchFold implements the four boards about what was on the pad:
// `heaviest_launch`, `most_parts`, `biggest_crew` and `biggest_stack`.
//
// All four read `flight.started`, all four are gated on `> 0`, and for the
// three integer fields that gate *is* §4.2's `>= 1`. Zero is what every one of
// them reports when the read failed — mass, part count, crew count and stage
// count are all written as 0 rather than omitted — so a zero is an unreadable
// vehicle, not an empty one. `stage_count` is the highest-risk read of the four
// (it walks `Vehicle.Parts.SequenceList`), which makes the gate matter most
// there and is also why a ver 1 row, which carries no stage count at all, falls
// out through the same door.
type launchFold struct {
	stat  string
	value func(FlightStarted) float64
}

func (f launchFold) Name() string { return f.stat }

func (f launchFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[FlightStarted](ev)
	if !ok {
		return nil
	}
	value := f.value(p)
	if value <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	// One blob for all four, for the reason orbitRecordFold carries one: a
	// reader looking at the heaviest launch wants to know how many parts and
	// stages it took, and four rows describing one vehicle should be the same
	// bytes rather than four partial views of it.
	return putRecord(ctx, b, ev, f.stat, value, map[string]any{
		"body":        p.Body,
		"flight":      ids.String(ev.FlightID),
		"vehicle":     p.VehicleName,
		"mass_kg":     p.MassKg,
		"part_count":  p.PartCount,
		"crew_count":  p.CrewCount,
		"stage_count": p.StageCount,
	})
}

// recoveryFold implements `biggest_recovery`: the most kittens brought home by
// one flight.
//
// The counterpart of `kittens_recovered`, which sums crew over every recovered
// flight. This one is the single best trip, which is a different achievement:
// forty solo recoveries and one nine-seat station crew return are the same
// number on that board and very different on this one.
type recoveryFold struct{}

func (recoveryFold) Name() string { return StatBiggestRecovery }

func (recoveryFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[FlightEnded](ev)
	if !ok || p.Reason != "recovered" || p.CrewCount < 1 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	body, err := flightBody(ctx, b, ev)
	if err != nil {
		return err
	}
	return putRecord(ctx, b, ev, StatBiggestRecovery, float64(p.CrewCount), map[string]any{
		"body":   body,
		"flight": ids.String(ev.FlightID),
	})
}

// stagesFold implements `most_stages`: the highest stage number a vehicle ever
// reached.
//
// `stage_index` is zero-based and is read in the postfix, so it is the sequence
// that just became active — the value is therefore `stage_index + 1`, which is
// "how many stages have fired", the number a player would say out loud. There
// is no `> 0` gate for the same reason: firing stage 0 is one staging event and
// counts as one stage.
type stagesFold struct{}

func (stagesFold) Name() string { return StatMostStages }

func (stagesFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleStaging](ev)
	if !ok {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	body, err := flightBody(ctx, b, ev)
	if err != nil {
		return err
	}
	return putRecord(ctx, b, ev, StatMostStages, float64(p.StageIndex+1), map[string]any{
		"body":   body,
		"flight": ids.String(ev.FlightID),
	})
}

// evaDurationFold implements `longest_eva`: the longest single spacewalk.
//
// `duration_s` is 0.0 when the EVA vehicle's launch time was never readable,
// which is indistinguishable on the wire from an EVA that ended in the frame it
// began — hence the strict `> 0`, which costs nothing real and keeps a failed
// read off the board.
//
// §4.1 sends `kitten.eva_end` with **flight: null**, asymmetrically with
// `kitten.eva_start`. So there is nothing here for the flag exclusion to check
// (scoreable passes every flightless event) and no flight to name in the
// context; the kitten is what identifies the row.
type evaDurationFold struct{}

func (evaDurationFold) Name() string { return StatLongestEVA }

func (evaDurationFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[KittenEvaEnd](ev)
	if !ok || p.DurationS <= 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	cx := map[string]any{"kitten": p.Name}
	if ev.HasFlight() {
		// Not reachable from the shipped mod; here so a future build that fills
		// the key in gets the link rather than a silently missing one.
		cx["flight"] = ids.String(ev.FlightID)
	}
	return putRecord(ctx, b, ev, StatLongestEVA, p.DurationS, cx)
}

// touchdownFold implements `softest_touchdown`: the gentlest arrival on a
// surface.
//
// Two conditions make this a landing rather than a bump. The destination must
// be a surface-contact situation, and the *origin* must be one of the two
// situations that are known to be off the ground — `freefall` or `maneuvering`.
// Requiring the origin to be **known** and not merely contact-free is the whole
// difference between a landing and an unreadable transition: `"unknown"` also
// reports no contact, and a touchdown measured from a state nobody could read
// is not a measurement (situation.go).
//
// It also excludes the transitions that would otherwise dominate the board:
// `rolling` → `landed` as a rover stops, or `landed` → `dragging` on a slope,
// are surface-to-surface at almost zero speed and are not touchdowns.
type touchdownFold struct{}

func (touchdownFold) Name() string { return StatSoftestTouchdown }

func (touchdownFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSituation](ev)
	if !ok || !hasSurfaceContact(p.To) || p.SurfaceSpeedMs <= 0 {
		return nil
	}
	if !knownSituation(p.From) || hasSurfaceContact(p.From) {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putBest(ctx, b, ev, StatSoftestTouchdown, p.SurfaceSpeedMs, map[string]any{
		"body":       p.Body,
		"flight":     ids.String(ev.FlightID),
		"from":       p.From,
		"to":         p.To,
		"altitude_m": p.AltitudeM,
	})
}

// survivedLanding reports whether a `vehicle.landed` may score, which is the
// whole eligibility `softest_landing` and `landings` share — a board about
// touching down gently and a board about touching down at all must agree about
// which arrivals happened.
//
// `survived` is the mod's answer and the only one taken: it has been through
// the same one-full-frame destruction hold as `vehicle.impact.survived`, so a
// touchdown the vehicle did not walk away from is a crash, and `vehicle.rud`
// and `biggest_impact_energy` are where a crash belongs.
//
// Unlike [survivedImpact] there is no crew requirement and no ±2 s KIA window.
// Those exist because §4.2's D11 rule is about a *crew* surviving a lithobrake;
// landing a probe is landing, and `crew_count` rides in the context for a reader
// who cares. And unlike [survivedLoad] there is no rebuild-only refinement, so
// these two boards fold identically incrementally and on rebuild.
func survivedLanding(ctx context.Context, b *Batch, ev Event, p VehicleLanded) (bool, error) {
	if !p.Survived {
		return false, nil
	}
	return scoreable(ctx, ev, b)
}

// softestLandingFold implements `softest_landing`: the gentlest touchdown, by
// descent rate.
//
// Not a duplicate of `softest_touchdown`, which ranks the same moment by
// `surface_speed_ms` — the *whole* velocity relative to the ground. A rover
// arriving at 8 m/s across a plain and a lander arriving at 8 m/s straight down
// are the same number there and very different flying; this board is the
// vertical component alone, which is the one a pilot is actually managing.
//
// `vertical_speed_ms` is positive downwards, so smaller is softer and the board
// is ascending — and therefore must refuse a 0, which is what an unreadable
// state-vector decomposition leaves behind and would be an unbeatable record
// (PROJ-088). A genuine touchdown is never exactly 0 m/s: the detector samples
// at 2 Hz and the vehicle is still settling.
type softestLandingFold struct{}

func (softestLandingFold) Name() string { return StatSoftestLanding }

func (softestLandingFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleLanded](ev)
	if !ok || p.VerticalSpeedMs <= 0 {
		return nil
	}
	ok, err := survivedLanding(ctx, b, ev, p)
	if err != nil || !ok {
		return err
	}
	return putBest(ctx, b, ev, StatSoftestLanding, p.VerticalSpeedMs, map[string]any{
		"body":                p.Body,
		"flight":              ids.String(ev.FlightID),
		"horizontal_speed_ms": p.HorizontalSpeedMs,
		"crew_count":          p.CrewCount,
	})
}

// landingsFold implements `landings`: how many times a player has put something
// down and had it survive.
//
// Not a [countFold], because that one counts every event of a type and this one
// owes the same `survived` gate its sibling board takes. A landing is one edge —
// the mod emits `vehicle.landed` only on contact-free → surface contact, sharing
// the situation rule's 2 s debounce — so a bouncing lander cannot inflate this
// by alternating at 2 Hz, and nothing here needs to guess at that.
//
// It counts landings, not bodies: `landed_bodies` is the set-backed board for
// "how many worlds", and the two are deliberately different questions.
type landingsFold struct{}

func (landingsFold) Name() string { return StatLandings }

func (landingsFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleLanded](ev)
	if !ok {
		return nil
	}
	ok, err := survivedLanding(ctx, b, ev, p)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, b, ev, StatLandings, 1)
}

// --- counter folds -----------------------------------------------------------

// countFold implements the boards that are simply "how many of this event":
// `kitten_tumbles`, `dockings`, `stagings`.
type countFold struct {
	stat      string
	eventType string
}

func (f countFold) Name() string { return f.stat }

func (f countFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	if ev.Type != f.eventType {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, b, ev, f.stat, 1)
}

// rudFold implements `rud_total` and the `rud_<cause>` family (§5.6).
type rudFold struct{}

func (rudFold) Name() string { return StatRUDTotal }

func (rudFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleRUD](ev)
	if !ok {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	if err := addCount(ctx, b, ev, StatRUDTotal, 1); err != nil {
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
	return addCount(ctx, b, ev, stat, 1)
}

// orbitsFold implements `orbits_achieved` (§5.6).
type orbitsFold struct{}

func (orbitsFold) Name() string { return StatOrbitsAchieved }

func (orbitsFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, b, ev, StatOrbitsAchieved, 1)
}

// soiFold implements `soi_bodies` (§5.6): the number of distinct bodies whose
// sphere of influence the player has entered, materialized in `player_body`.
//
// The count is advanced by one only when the INSERT OR IGNORE actually inserts,
// so the board never needs a `count(*)` and stays correct under replay.
type soiFold struct{}

func (soiFold) Name() string { return StatSOIBodies }

func (soiFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	added, err := b.AddBody(ctx, ev.PlayerID, "soi", p.ToBody, ev.Seq)
	if err != nil || !added {
		return err // an err, or a body already visited
	}
	return addCount(ctx, b, ev, StatSOIBodies, 1)
}

// landedBodiesFold implements `landed_bodies`: how many distinct bodies the
// player has put something down on.
//
// The set-backed shape of `soi_bodies`, and for the same reason (PROJ-011):
// `AddBody` reports whether the `player_body` row was new, so the counter
// advances only on a new row and the board never needs a `count(*)` and stays
// correct under replay. It writes `kind = 'landed'` alongside soiFold's
// `'soi'`, which the table's (player_id, kind, body) key already allows for.
//
// "Landed on" is any surface contact — terrain, ocean or both — because
// splashing down on a body is arriving at it. `splashdowns` is the board that
// distinguishes them.
//
// **It stays on `vehicle.situation` now that `vehicle.landed` exists**, and
// that is a decision rather than an oversight. Three reasons, in order of
// weight. (1) `vehicle.landed` fires only on the contact-free → contact edge,
// while this board asks whether the player has anything *on* a surface: a
// vehicle already on the ground when a save loads never produces a landing
// event, and a rover that then goes `rolling` → `landed` would put its body on
// the board through the situation and through nothing else. (2) Every
// `landed_bodies` row in every existing log was written from a
// `vehicle.situation`, and no `vehicle.landed` exists before wire v2 — moving
// the source would silently empty the board on the next rebuild, which is data
// loss dressed as a refactor. (3) The two events come off the *same* detection,
// so switching would buy no new edges; it would only lose the ones above.
//
// The corollary is the rule against double counting: `landings` and
// `softest_landing` read `vehicle.landed` and never touch `player_body`, so a
// touchdown advances this counter through exactly one path.
type landedBodiesFold struct{}

func (landedBodiesFold) Name() string { return StatLandedBodies }

func (landedBodiesFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSituation](ev)
	if !ok || p.Body == "" || !hasSurfaceContact(p.To) {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	added, err := b.AddBody(ctx, ev.PlayerID, "landed", p.Body, ev.Seq)
	if err != nil || !added {
		return err // an err, or a body already landed on
	}
	return addCount(ctx, b, ev, StatLandedBodies, 1)
}

// splashdownFold implements `splashdowns`: arrivals in water.
//
// `to` must be pure ocean contact — `sailing` or `floating` — rather than any
// ocean contact, because `dragging` and `bottomed` touch terrain as well and
// are a hull on a shoreline rather than a capsule under a parachute.
//
// `from` must be contact-free, which is what makes this an *arrival*. Without
// it a boat bobbing across the `sailing` ↔ `floating` boundary as it goes on
// and off rails would count a splashdown every time, and the 2 s situation
// debounce only rate-limits that — it does not stop it.
type splashdownFold struct{}

func (splashdownFold) Name() string { return StatSplashdowns }

func (splashdownFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSituation](ev)
	if !ok || contactOf(p.To) != contactOcean || hasSurfaceContact(p.From) {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, b, ev, StatSplashdowns, 1)
}

// recoveredFold implements `kittens_recovered` (§5.6): the sum of
// `flight.ended.crew_count` over flights that ended `recovered`.
type recoveredFold struct{}

func (recoveredFold) Name() string { return StatKittensRecovered }

func (recoveredFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[FlightEnded](ev)
	if !ok || p.Reason != "recovered" || p.CrewCount < 1 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return addCount(ctx, b, ev, StatKittensRecovered, float64(p.CrewCount))
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

func (distanceFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[RosterSnapshot](ev)
	if !ok || len(p.Kittens) == 0 {
		return nil
	}
	for _, k := range p.Kittens {
		if k.Kid == "" {
			continue
		}
		if err := b.UpsertKitten(ctx, ev.PlayerID, k, ev.Seq); err != nil {
			return err
		}
	}

	// Two per-kitten records off the same roster: the furthest one kitten has
	// ever gone, and the most missions one kitten has ever flown. Both are
	// "who is your best cat", where `distance_travelled` is "how good is your
	// whole roster", and they are folded here because this is the only fold
	// that has ever written the `kitten` table.
	//
	// **They inherit distance_travelled's exemption from the flag exclusion**,
	// because roster.snapshot carries no flight (§4.1 sends flight: null) and
	// scoreable passes every flightless event. So a kitten who did all her
	// travelling on a teleported flight still holds the record. That is a
	// property of the source event, not a decision about these boards, and it
	// is not fixable here — the fix would be the mod attributing roster totals
	// to the flights that earned them. Recorded rather than papered over.
	travelled, missions, err := b.KittenTops(ctx, ev.PlayerID)
	if err != nil {
		return err
	}
	if travelled.Value > 0 {
		if err := putRecord(ctx, b, ev, StatTopKittenDistance, travelled.Value,
			map[string]any{"kitten": travelled.Name}); err != nil {
			return err
		}
	}
	if missions.Value > 0 {
		if err := putRecord(ctx, b, ev, StatTopKittenMissions, missions.Value,
			map[string]any{"kitten": missions.Name}); err != nil {
			return err
		}
	}

	total, err := b.KittenDistance(ctx, ev.PlayerID)
	if err != nil {
		return err
	}
	if total <= 0 {
		return nil
	}
	return setValue(ctx, b, ev, StatDistanceTravelled, total)
}

// flightBody is the body a flight's `flight.started` reported, for the record
// boards whose own payload carries none.
//
// It costs nothing: every caller has already been through scoreable, which
// loaded the same flight_state row into the batch's cache. An empty string is
// the honest answer for a flight whose `flight.started` has not been folded yet
// — a batch can be split — and is what the payload itself would have said.
func flightBody(ctx context.Context, b *Batch, ev Event) (string, error) {
	st, ok, err := b.Flight(ctx, ev.FlightID)
	if err != nil || !ok {
		return "", err
	}
	return st.Body, nil
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
func careerTime(ctx context.Context, ev Event, b *Batch) (float64, bool, error) {
	if !ev.HasCareer() || !ev.HasSimTime || ev.SimTime < 0 {
		return 0, false, nil
	}
	ok, err := scoreable(ctx, ev, b)
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

func (toOrbitFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" {
		return nil
	}
	t, ok, err := careerTime(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	return putBest(ctx, b, ev, StatFastestToOrbit, careerMillis(t), map[string]any{
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

func (toBodyFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	t, ok, err := careerTime(ctx, ev, b)
	if err != nil || !ok {
		return err
	}

	// soiFold owns the row's existence; this only ever lowers its time. It runs
	// after soiFold (fold order in fold.go), so the row is already there. The
	// coalesce covers a row soiFold inserted on a career-less event, which has
	// no time yet — min() over NULL is NULL in SQLite.
	if err := b.LowerBodyTime(ctx, ev.PlayerID, "soi", p.ToBody, t); err != nil {
		return err
	}

	stat, ok := FastestToStat(p.ToBody)
	if !ok {
		// A name that cannot be half of a stat key still counts towards
		// `soi_bodies` and still keeps its arrival time above; it just has no
		// board of its own.
		return nil
	}
	return putBest(ctx, b, ev, stat, careerMillis(t), map[string]any{
		"career": ev.Career,
		"from":   p.FromBody,
		"flight": ids.String(ev.FlightID),
	})
}
