package stats

import (
	"context"
	"fmt"
)

// BadgeFolds returns the active starter catalogue in display order.
func BadgeFolds() []Fold {
	return []Fold{
		// First steps.
		eventBadge{badge: BadgeFirstFlight, typ: "flight.started"},
		eventBadge{badge: BadgeFirstStage, typ: "vehicle.staging"},
		eventBadge{badge: BadgeFirstSpace, typ: "vehicle.atmosphere", when: atmosphereExited},
		eventBadge{badge: BadgeFirstOrbit, typ: "vehicle.orbit", when: orbitAchieved},
		eventBadge{badge: BadgeFirstLanding, typ: "vehicle.landed", when: landingSurvived},
		eventBadge{badge: BadgeFirstRecovery, typ: "flight.ended", when: flightRecovered},
		eventBadge{badge: BadgeFirstEVA, typ: "kitten.eva_start"},
		eventBadge{badge: BadgeFirstDock, typ: "vehicle.docked"},
		eventBadge{badge: BadgeFirstRUD, typ: "vehicle.rud"},

		// Flight.
		compositeBadge{badge: BadgeCrewedOrbit, typ: "vehicle.orbit", when: crewedOrbitCandidate},
		compositeBadge{badge: BadgeOrbitAndBack, typ: "flight.ended", when: orbitAndBackCandidate},
		compositeBadge{badge: BadgeDockedAfterOrbit, typ: "vehicle.docked", when: dockedAfterOrbitCandidate},
		compositeBadge{badge: BadgeCoaster, typ: "vehicle.soi", when: coasterCandidate},
		thresholdBadge{badge: BadgeHeavyLifter, stat: StatHeaviestToOrbit, n: 20_000},
		thresholdBadge{badge: BadgeBigStack, stat: StatBiggestStack, n: 5},
		thresholdBadge{badge: BadgeManyParts, stat: StatMostParts, n: 100},
		thresholdBadge{badge: BadgeWellLit, stat: StatEngineIgnitions, n: 100},

		// Survival.
		thresholdBadge{badge: BadgeLithobraker, stat: StatBiggestLithobrakeSurvived, n: 50},
		thresholdBadge{badge: BadgeGroundTruth, stat: StatBiggestLithobrakeSurvived, n: 100},
		thresholdBadge{badge: BadgePressed, stat: StatPeakGSurvived, n: 10},
		thresholdBadge{badge: BadgeFeather, stat: StatSoftestLanding, n: 0.5, below: true},
		thresholdBadge{badge: BadgeCanyonRun, stat: StatLowestPass, n: 100, below: true},
		thresholdBadge{badge: BadgeOldHand, stat: StatLandings, n: 25},

		// Exploration.
		thresholdBadge{badge: BadgeWanderer, stat: StatSOIBodies, n: 3},
		thresholdBadge{badge: BadgeVoyager, stat: StatSOIBodies, n: 5},
		thresholdBadge{badge: BadgeGrandTour, stat: StatSOIBodies, n: 8},
		thresholdBadge{badge: BadgeGroundskeeper, stat: StatLandedBodies, n: 3},
		everywhereBadge{badge: BadgeBeenToEveryPlanet, kind: "planet"},
		everywhereBadge{badge: BadgeBeenToEverything},
		reachedBodyBadge{},
		orbitedBodyBadge{},
		landedOnBodyBadge{},

		// Kittens.
		eventBadge{badge: BadgeNotOnTheirFeet, typ: "kitten.tumble", when: tumbleFromAirborne},
		thresholdBadge{badge: BadgePersistentlyUpsideDown, stat: StatKittenTumbles, n: 50},
		thresholdBadge{badge: BadgeCrowdedCapsule, stat: StatBiggestRecovery, n: 4},
		thresholdBadge{badge: BadgeSpacewalker, stat: StatEVAs, n: 10},
		thresholdBadge{badge: BadgeTheLongWalk, stat: StatLongestEVA, n: 3_600},
		thresholdBadge{badge: BadgeFerryService, stat: StatKittensToOrbitAndBack, n: 10},
	}
}

func atmosphereExited(ev Event) bool {
	p, ok := payloadOf[VehicleAtmosphere](ev)
	return ok && p.Dir == "exited"
}

func orbitAchieved(ev Event) bool {
	p, ok := payloadOf[VehicleOrbit](ev)
	return ok && p.Phase == "achieved"
}

func landingSurvived(ev Event) bool {
	p, ok := payloadOf[VehicleLanded](ev)
	return ok && p.Survived
}

func flightRecovered(ev Event) bool {
	p, ok := payloadOf[FlightEnded](ev)
	return ok && p.Reason == "recovered"
}

func tumbleFromAirborne(ev Event) bool {
	p, ok := payloadOf[KittenTumble](ev)
	return ok && p.From == "airborne"
}

// eventBadge awards on the first event of typ that satisfies when.
type eventBadge struct {
	badge string
	typ   string
	when  func(Event) bool
	cx    func(Event) map[string]any
}

func (f eventBadge) Name() string { return "badge:" + f.badge }

func (f eventBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	if ev.Type != f.typ || (f.when != nil && !f.when(ev)) {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	var cx map[string]any
	if f.cx != nil {
		cx = f.cx(ev)
	}
	return award(ctx, b, ev, f.badge, cx)
}

// thresholdBadge reads board values after BoardFolds has handled the candidate
// event. Lifetime and save thresholds are independent; a lifetime total may
// span saves without awarding either save.
type thresholdBadge struct {
	badge string
	stat  string
	n     float64
	below bool
}

func (f thresholdBadge) Name() string { return "badge:" + f.badge }

func (f thresholdBadge) met(v float64) bool {
	if f.below {
		return v > 0 && v <= f.n
	}
	return v >= f.n
}

func (f thresholdBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	lifetime, err := b.StatValue(ctx, ev.PlayerID, f.stat)
	if err != nil {
		return err
	}
	if f.met(lifetime) {
		b.putBadge(ev.PlayerID, "", f.badge, system, ev.Career, ev, nil)
	}
	if ev.Career == "" {
		return nil
	}
	save, err := b.CareerStatValue(ctx, ev.PlayerID, ev.Career, f.stat)
	if err != nil {
		return err
	}
	if f.met(save) {
		b.putBadge(ev.PlayerID, ev.Career, f.badge, system, "", ev, nil)
	}
	return nil
}

// compositeBadge joins one candidate event to the completed flight_state row.
// Its predicate must use HasStartFactAt for nullable flight.started facts.
type compositeBadge struct {
	badge string
	typ   string
	when  func(Event, FlightState) bool
	cx    func(Event, FlightState) map[string]any
}

func (f compositeBadge) Name() string { return "badge:" + f.badge }

func (f compositeBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	if ev.Type != f.typ {
		return nil
	}
	state, found, err := b.Flight(ctx, ev.FlightID)
	if err != nil || !found || state.Flagged() || f.when == nil || !f.when(ev, state) {
		return err
	}
	var cx map[string]any
	if f.cx != nil {
		cx = f.cx(ev, state)
	}
	return award(ctx, b, ev, f.badge, cx)
}

func crewedOrbitCandidate(ev Event, state FlightState) bool {
	p, ok := payloadOf[VehicleOrbit](ev)
	return ok && p.Phase == "achieved" && state.HasStartFactAt(ev.Seq, state.Crew.Valid) && state.Crew.Int64 >= 1
}

func orbitAndBackCandidate(ev Event, state FlightState) bool {
	p, ok := payloadOf[FlightEnded](ev)
	return ok && p.Reason == "recovered" && state.Milestones&MilestoneOrbit != 0 && state.FirstOrbitSeq > 0 && state.FirstOrbitSeq < ev.Seq
}

func dockedAfterOrbitCandidate(ev Event, state FlightState) bool {
	_, ok := payloadOf[VehicleDock](ev)
	return ok && state.Milestones&MilestoneOrbit != 0 && state.FirstOrbitSeq > 0 && state.FirstOrbitSeq < ev.Seq
}

func coasterCandidate(ev Event, state FlightState) bool {
	p, ok := payloadOf[VehicleSOI](ev)
	return ok && p.ToBody != "" && state.HasStartFactAt(ev.Seq, state.EngineCount.Valid) && state.EngineCount.Int64 == 0
}

type reachedBodyBadge struct{}
type orbitedBodyBadge struct{}
type landedOnBodyBadge struct{}

// everywhereBadge is a fixed subset predicate over the game-reported system
// catalogue. kind is the emitted normalized kind; empty selects every body.
type everywhereBadge struct {
	badge string
	kind  string
}

func (f everywhereBadge) Name() string { return "badge:" + f.badge }

func (f everywhereBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	lifetime, err := b.HasBadge(ctx, ev.PlayerID, "", f.badge)
	if err != nil {
		return err
	}
	save, err := b.HasBadge(ctx, ev.PlayerID, ev.Career, f.badge)
	if err != nil || (lifetime && save) {
		return err
	}
	missing, ready, err := b.BodiesNotVisited(ctx, ev, f.kind)
	if err != nil || !ready || missing != 0 {
		return err
	}
	return award(ctx, b, ev, f.badge, nil)
}

func (reachedBodyBadge) Name() string  { return "badge-family:reached" }
func (orbitedBodyBadge) Name() string  { return "badge-family:orbited" }
func (landedOnBodyBadge) Name() string { return "badge-family:landed_on" }

func (reachedBodyBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	return awardBodyBadge(ctx, b, ev, p.ToBody, ReachedBadge)
}

func (orbitedBodyBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleOrbit](ev)
	if !ok || p.Phase != "achieved" || p.Body == "" {
		return nil
	}
	return awardBodyBadge(ctx, b, ev, p.Body, OrbitedBadge)
}

func (landedOnBodyBadge) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleLanded](ev)
	if !ok || !p.Survived || p.Body == "" {
		return nil
	}
	return awardBodyBadge(ctx, b, ev, p.Body, LandedOnBadge)
}

func awardBodyBadge(ctx context.Context, b *Batch, ev Event, body string, key func(string) (string, bool)) error {
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	badge, valid := key(body)
	if !valid {
		return nil
	}
	return award(ctx, b, ev, badge, map[string]any{"body": body})
}

func validateFoldNames(folds []Fold) error {
	seen := make(map[string]bool, len(folds))
	for _, fold := range folds {
		name := fold.Name()
		if name == "" || seen[name] {
			return fmt.Errorf("stats: duplicate or empty fold name %q", name)
		}
		seen[name] = true
	}
	return nil
}
