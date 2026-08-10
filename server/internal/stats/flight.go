package stats

import (
	"context"
	"database/sql"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// The flight_state.flags bitfield (§5.4, extended by the docs/events.md
// amendment). Any bit set excludes the flight from every board.
const (
	FlagTeleport     int64 = 1 << 0
	FlagRefuel       int64 = 1 << 1
	FlagResourceEdit int64 = 1 << 2
	FlagConsole      int64 = 1 << 3
	// FlagTuning marks a flight during which the game's debug window was
	// live-editing KittenLocomotionTuning.Current.TumbleSpeedGate — the sole
	// classifier for kitten.tumble. It is the reason the flag exclusion has to
	// cover counter boards and not only record boards: `kitten_tumbles` is a
	// counter, and without the exclusion this flag would protect nothing
	// (docs/events.md, docs/DECISIONS.md).
	FlagTuning int64 = 1 << 4
	// FlagOther is set for a `flight.flagged` value this build does not know.
	// Not in §5.4: an unrecognised flag is a newer mod telling us something is
	// wrong with this flight, and the only safe reading of "something is wrong"
	// is to exclude it. Failing open would make every future flag a scoring
	// loophole until the server caught up.
	FlagOther int64 = 1 << 5
)

// The flight_state.milestones bitfield. Milestones are historical facts: once
// observed they are never cleared.
const (
	MilestoneOrbit    int64 = 1 << 0
	MilestoneSpace    int64 = 1 << 1
	MilestoneOtherSOI int64 = 1 << 2
	MilestoneLanded   int64 = 1 << 3
	MilestoneDocked   int64 = 1 << 4
)

// FlagBit maps a §4.2 `flight.flagged.flag` value onto its bit, returning
// [FlagOther] for anything it does not recognise.
func FlagBit(flag string) int64 {
	switch flag {
	case "teleport":
		return FlagTeleport
	case "refuel":
		return FlagRefuel
	case "resource_edit":
		return FlagResourceEdit
	case "console":
		return FlagConsole
	case "tuning":
		return FlagTuning
	default:
		return FlagOther
	}
}

// FlagNames renders a flags bitfield for logs and for /admin/stats.
func FlagNames(flags int64) []string {
	var out []string
	for _, f := range []struct {
		bit  int64
		name string
	}{
		{FlagTeleport, "teleport"},
		{FlagRefuel, "refuel"},
		{FlagResourceEdit, "resource_edit"},
		{FlagConsole, "console"},
		{FlagTuning, "tuning"},
		{FlagOther, "other"},
	} {
		if flags&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// FlightState is a row of `flight_state` (§5.4).
type FlightState struct {
	FlightID     ids.ID
	PlayerID     int64
	Flags        int64
	EndedReason  string
	Crew         sql.NullInt64
	Body         string
	StartedSeq   int64
	EngineCount  sql.NullInt64
	Milestones   int64
	PartCount    sql.NullInt64
	LaunchMassKg sql.NullFloat64
	Career       string
}

// Flagged reports whether any flag bit is set.
func (f FlightState) Flagged() bool { return f.Flags != 0 }

// Recovered reports whether the flight ended with the vehicle recovered — the
// condition `peak_g_survived` applies during a rebuild (§5.6).
func (f FlightState) Recovered() bool { return f.EndedReason == "recovered" }

// HasStartFactAt reports whether a nullable flight.started fact is usable by a
// composite consumer at candidateSeq. The start must have actually been seen,
// must not occur after the candidate event, and the required fact must be
// present. This ordering rule keeps incremental projection and rebuild honest
// when an earlier event arrives before flight.started.
func (f FlightState) HasStartFactAt(candidateSeq int64, factValid bool) bool {
	return f.StartedSeq > 0 && f.StartedSeq <= candidateSeq && factValid
}

// flightFold maintains `flight_state` (§5.6). It is applied before every other
// fold, because every other fold asks it whether the flight has been flagged.
type flightFold struct{}

func (flightFold) Name() string { return "flight_state" }

func (flightFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	if !ev.HasFlight() {
		return nil
	}
	// Every flight-bearing event creates the row, not just flight.started: a
	// batch can be split so that a flight.flagged is folded before the
	// flight.started it belongs to has arrived, and the flag must not be lost.
	if err := b.EnsureFlight(ctx, ev.FlightID, ev.PlayerID, ev.Career); err != nil {
		return err
	}

	switch ev.Type {
	case "flight.started":
		p, ok := payloadOf[FlightStarted](ev)
		if !ok {
			return nil
		}
		return b.StartFlight(ctx, ev.FlightID, p.CrewCount, p.Body, p.EngineCount, p.PartCount, p.MassKg, ev.Seq)
	case "flight.ended":
		p, ok := payloadOf[FlightEnded](ev)
		if !ok {
			return nil
		}
		return b.EndFlight(ctx, ev.FlightID, p.Reason)
	case "flight.flagged":
		p, ok := payloadOf[FlightFlagged](ev)
		if !ok {
			return nil
		}
		return b.FlagFlight(ctx, ev.FlightID, FlagBit(p.Flag))
	case "vehicle.orbit":
		p, ok := payloadOf[VehicleOrbit](ev)
		if ok && p.Phase == "achieved" {
			return b.MarkFlightMilestone(ctx, ev.FlightID, MilestoneOrbit)
		}
	case "vehicle.atmosphere":
		p, ok := payloadOf[VehicleAtmosphere](ev)
		if ok && p.Dir == "exited" {
			return b.MarkFlightMilestone(ctx, ev.FlightID, MilestoneSpace)
		}
	case "vehicle.soi":
		p, ok := payloadOf[VehicleSOI](ev)
		if !ok || p.ToBody == "" {
			return nil
		}
		state, found, err := b.Flight(ctx, ev.FlightID)
		if err != nil || !found {
			return err
		}
		if state.HasStartFactAt(ev.Seq, state.Body != "") && p.ToBody != state.Body {
			return b.MarkFlightMilestone(ctx, ev.FlightID, MilestoneOtherSOI)
		}
	case "vehicle.landed":
		p, ok := payloadOf[VehicleLanded](ev)
		if ok && p.Survived {
			return b.MarkFlightMilestone(ctx, ev.FlightID, MilestoneLanded)
		}
	case "vehicle.docked":
		if _, ok := payloadOf[VehicleDock](ev); ok {
			return b.MarkFlightMilestone(ctx, ev.FlightID, MilestoneDocked)
		}
	}
	return nil
}
