package stats

import (
	"context"
	"fmt"
)

// BadgeFolds is intentionally empty until F5 activates the complete starter
// catalogue atomically. F4 supplies and tests the reusable fold shapes without
// creating an arbitrary partial public award set.
func BadgeFolds() []Fold { return nil }

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
