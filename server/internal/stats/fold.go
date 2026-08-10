package stats

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// Fold is one projection rule (§5.6). The projector applies every registered
// fold to every event of a batch inside one projections.db transaction.
//
// §5.6 writes the signature as `Apply(tx *sql.Tx, ev DecodedEvent, fs
// FlightStateReader) error`. Three departures, all recorded in
// docs/DECISIONS.md: a context, because every database call underneath takes one
// and a rebuild must be cancellable; Name, because "which fold failed" is the
// only useful thing to put in the log line when one does; and a [Batch] in place
// of the raw transaction and the separate reader.
//
// The Batch is where the performance is. A fold that writes straight to the
// transaction issues one tursogo statement per write and one per read, and at
// ~15 µs each that was the entire cost of a projection. A Batch answers reads
// from cache, merges repeated writes to one key, and flushes the survivors as
// multi-row statements — while being the same rules, spelled in Go. It is also
// the FlightStateReader, so a fold has one thing to talk to rather than two
// that have to agree.
type Fold interface {
	// Name identifies the fold in logs and in /admin/stats.
	Name() string
	// Apply records this fold's contribution for one event. Returning an error
	// aborts the whole batch: a fold that cannot write must not be allowed to
	// advance the checkpoint past the event it dropped.
	Apply(ctx context.Context, b *Batch, ev Event) error
}

// FlightStateReader is what a fold may ask about the flight an event belongs to.
// [Batch] implements it, and is the only thing that does; it stays an interface
// because [Summarize] takes one and reads nothing else, which keeps the feed
// renderer honest about how little it is allowed to touch.
//
// The first method is §5.6's; the other two carry the rebuild-only refinements.
// They live on the same interface because the refinement is a property of the
// pass, not of the event: during incremental folding Refined is false and
// KIANear always answers false, so the same fold code produces the optimistic
// incremental answer and the exact rebuild answer without branching anywhere
// else.
type FlightStateReader interface {
	// Flight reads flight_state for a flight, reporting false when no row
	// exists yet.
	Flight(ctx context.Context, id ids.ID) (FlightState, bool, error)
	// Refined reports whether the caller is a rebuild, where flight_state is
	// already complete for the entire history (§5.6).
	Refined() bool
	// KIANear reports whether a kitten.kia landed in this flight within ±2.0 s
	// of simT (§4.2's crew-survival window). Always false outside a rebuild.
	KIANear(flight ids.ID, simT float64) bool
}

// KIAWindowSeconds is the ±window of §4.2's crew-survival rule.
const KIAWindowSeconds = 2.0

// Folds returns every fold in application order: the state folds first (§5.6),
// then everything the second pass applies. The order of the boards among
// themselves does not matter — no two write the same (player_id, stat).
func Folds() []Fold {
	return append(StateFolds(), SecondPassFolds()...)
}

// SecondPassFolds returns every fold that runs once flight_state and career are
// complete: boards, badges, challenges, then the census.
//
// It exists so that "what a rebuild's second pass applies" and "what the
// incremental loop applies after the state folds" are one list rather than two
// that have to be kept level. A fold that ran in one and not the other would
// make a rebuilt projections.db disagree with the incremental one, which is the
// one property the rebuild exists to guarantee.
func SecondPassFolds() []Fold {
	return secondPassFolds(BoardFolds(), BadgeFolds(), ChallengeFolds(), LogFolds())
}

func secondPassFolds(boards, badges, challenges, logs []Fold) []Fold {
	folds := append(append(append(boards, badges...), challenges...), logs...)
	if err := validateFoldNames(folds); err != nil {
		panic(err)
	}
	return folds
}

// LogFolds returns the folds that describe the log itself rather than the
// players in it — currently just the event census behind `GET /v1/stats`.
//
// Separate from [BoardFolds] because it obeys none of the board rules: no flag
// exclusion, no handle requirement, no tie-break. See census.go.
func LogFolds() []Fold { return []Fold{censusFold{}} }

// StateFolds returns the folds that maintain the tables the boards read.
// systemFold is first because later folds read the career binding through the
// same Batch, including when discovery and scoring events share one batch.
func StateFolds() []Fold { return []Fold{systemFold{}, flightFold{}, careerFold{}} }

// FlightFold returns the flight_state fold, which every board fold depends on.
func FlightFold() Fold { return flightFold{} }

// BoardFolds returns every leaderboard fold, in board-metadata order.
//
// A rebuild applies these on its second pass, after [StateFolds] alone has built
// a complete flight_state and career table on the first (§5.6).
func BoardFolds() []Fold {
	return []Fold{
		lithobrakeFold{},
		peakGFold{},
		maxQFold{},
		impactEnergyFold{},
		speedFold{stat: StatFastestSurfaceSpeed, surface: true},
		speedFold{stat: StatFastestOrbitalSpeed},
		entryFold{},
		altitudeFold{},
		lowestPassFold{},
		orbitRecordFold{stat: StatHighestApoapsis, value: func(p VehicleOrbit) float64 { return p.ApM }},
		orbitRecordFold{stat: StatLowestOrbit, best: true, value: func(p VehicleOrbit) float64 { return p.PeM }},
		orbitRecordFold{stat: StatRoundestOrbit, best: true, value: func(p VehicleOrbit) float64 { return p.Ecc }},
		orbitRecordFold{stat: StatSteepestOrbit, value: func(p VehicleOrbit) float64 { return p.IncDeg }},
		orbitMassFold{},
		touchdownFold{},
		softestLandingFold{},
		launchFold{stat: StatHeaviestLaunch, value: func(p FlightStarted) float64 { return p.MassKg }},
		launchFold{stat: StatMostParts, value: func(p FlightStarted) float64 { return float64(p.PartCount) }},
		launchFold{stat: StatBiggestCrew, value: func(p FlightStarted) float64 { return float64(p.CrewCount) }},
		launchFold{stat: StatBiggestStack, value: func(p FlightStarted) float64 { return float64(p.StageCount) }},
		recoveryFold{},
		stagesFold{},
		evaDurationFold{},
		tumbleFold{},
		rudPartsFold{},
		orbitsFold{},
		soiFold{},
		landedBodiesFold{},
		landingsFold{},
		countFold{stat: StatDockings, eventType: "vehicle.docked"},
		countFold{stat: StatStagings, eventType: "vehicle.staging"},
		splashdownFold{},
		countFold{stat: StatEVAs, eventType: "kitten.eva_start"},
		countFold{stat: StatFlameouts, eventType: "engine.flameout"},
		countFold{stat: StatEngineIgnitions, eventType: "engine.ignition"},
		recoveredFold{},
		// distanceFold also writes top_kitten_distance and top_kitten_missions.
		distanceFold{},
		toOrbitFold{},
		toBodyFold{},
		careerPlaytimeFold{},
		countFold{stat: StatPlaySessions, eventType: "session.started"},
		kittensToOrbitFold{},
		bodySprintFold{},
	}
}

// --- shared write helpers ----------------------------------------------------
//
// These are the shapes every board write takes. Each records the write in the
// batch under its merge rule; the SQL that settles it against whatever is
// already in the table lives in batch.go, one statement per rule per flush.
//
// The rules are still the rules. A record board is replaced only by a strictly
// *larger* value, in memory and again in the flushed `ON CONFLICT` guard, so an
// equal value leaves the earlier `updated_seq` — and therefore the earlier
// claimant's rank — untouched (§5.6).
//
// Record, best and count writes fan out to every scope because the same event
// contribution has the same meaning in each. A set write does not: it is a
// derived total read from another table, and the player, career and system
// totals are different queries. Those folds call [setValue], [setCareerValue]
// and [setSystemValue] with independently computed values.

// putBest writes a min-record board: the row is replaced only by a strictly
// *smaller* value. It is the exact mirror of [putRecord], including the tie
// rule.
//
// This is what every "fastest career time to X" board is: the value is seconds
// since the career began, and lower is better.
func putBest(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindBest, ev.PlayerID, stat, value, cx, ev.Seq)
	if err := b.putScoped(ctx, kindBest, ev, stat, value, cx); err != nil {
		return err
	}
	return periodBest(ctx, b, ev, stat, value, cx)
}

// putRecord writes a record (max) board.
func putRecord(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindRecord, ev.PlayerID, stat, value, cx, ev.Seq)
	if err := b.putScoped(ctx, kindRecord, ev, stat, value, cx); err != nil {
		return err
	}
	return periodRecord(ctx, b, ev, stat, value, cx)
}

// putPlayerRecord writes only the lifetime row and its period rows. Set-backed
// folds use it when each scope has a different winning source row and therefore
// must not fan one lifetime context into the career and system scopes.
func putPlayerRecord(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindRecord, ev.PlayerID, stat, value, cx, ev.Seq)
	return periodRecord(ctx, b, ev, stat, value, cx)
}

// addCount advances a counter board. updated_seq becomes the seq at which the
// counter reached its new value, which is what makes the tie-break "whoever got
// to N first" rather than "whoever was written last" — and is why the batch
// carries the last contributing seq alongside the summed delta.
func addCount(ctx context.Context, b *Batch, ev Event, stat string, delta float64) error {
	b.putStat(kindCount, ev.PlayerID, stat, delta, nil, ev.Seq)
	if err := b.putScoped(ctx, kindCount, ev, stat, delta, nil); err != nil {
		return err
	}
	return periodAdd(ctx, b, ev, stat, delta)
}

// setValue writes a derived total, replacing whatever was there. Used by
// set-backed and derived-total boards whose value is a function of another
// table rather than an accumulation.
func setValue(ctx context.Context, b *Batch, ev Event, stat string, value float64) error {
	// A derived total's *window* value is what it grew by inside that window —
	// "distance travelled this month", not "lifetime distance as of this
	// month", which would be a cumulative number wearing a monthly label. That
	// needs the previous total, so it is read before the write; the batch
	// answers from its own pending writes, so two snapshots in one batch see
	// each other exactly as they did when each was its own statement. A rebuild
	// replays the same events in the same seq order and therefore reads the
	// same previous value, which is what keeps rebuild == incremental.
	prev, err := b.StatValue(ctx, ev.PlayerID, stat)
	if err != nil {
		return err
	}
	b.putStat(kindSet, ev.PlayerID, stat, value, nil, ev.Seq)
	if delta := value - prev; delta > 0 {
		return periodAdd(ctx, b, ev, stat, delta)
	}
	return nil
}

// setCareerValue writes a derived total in the career scope.
//
// Separate from [setValue] rather than folded into it because a derived total is
// a function of another table, and the per-save figure is a different query from
// the lifetime one. A fan-out here would write the lifetime number into a row
// labelled with one save — wrong, and wrong invisibly. Each fold that uses
// setValue computes its own career figure and calls this beside it.
//
// There is no period form: setValue's window write is an increase read from the
// previous value, and a career scope has no windows (see 0006_career_scope.sql).
func setCareerValue(ctx context.Context, b *Batch, ev Event, stat string, value float64) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putCareerStat(kindSet, ev, system, stat, value, nil)
	return nil
}

// setSystemValue is its system-scoped twin, and it takes a separate value.
//
// A system's derived total is not one save's. "Bodies visited in the Sol
// system" is the union across every save played there, so it is its own query;
// mirroring the career figure would label one save's number as all of them.
func setSystemValue(ctx context.Context, b *Batch, ev Event, stat string, value float64) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil || system == "" {
		return err
	}
	b.putSystemStat(kindSet, ev, system, stat, value, nil)
	return nil
}

// encodeContext renders a stat's context column. encoding/json sorts map keys,
// so the same context always produces the same bytes — which is what lets a
// rebuild be compared to the incremental result column for column.
func encodeContext(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("stats: encode context: %w", err)
	}
	return string(b), nil
}

// award records the first badge satisfaction in lifetime scope and, when the
// event has a career, in that save's scope. Badge folds own eligibility; this
// helper deliberately performs no scoreable or registry check.
func award(ctx context.Context, b *Batch, ev Event, badge string, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putBadge(ev.PlayerID, "", badge, system, ev.Career, ev, cx)
	if ev.Career != "" {
		b.putBadge(ev.PlayerID, ev.Career, badge, system, "", ev, cx)
	}
	return nil
}

// scoreable reports whether an event may contribute to a board: it must belong
// to a flight, and that flight must carry no flag bit (§5.6).
//
// Events with no flight (session.started, roster.snapshot) are scoreable — there
// is no flight to have been flagged, and `distance_travelled` folds one of them.
func scoreable(ctx context.Context, ev Event, fs FlightStateReader) (bool, error) {
	if !ev.HasFlight() {
		return true, nil
	}
	st, ok, err := fs.Flight(ctx, ev.FlightID)
	if err != nil {
		return false, err
	}
	if !ok {
		// No flight_state row means no flight.flagged has ever been seen for it.
		// The row is created by FlightFold for every event carrying a flight, so
		// in practice this only happens if FlightFold was not applied.
		return true, nil
	}
	return st.Flags == 0, nil
}
