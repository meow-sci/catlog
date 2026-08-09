package stats

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Summarize renders the §5.6 feed line for an event, reporting false when the
// event is not one of the eight feed types, when the flight was flagged, or when
// the player has no public handle.
//
// Flagged flights are excluded for the same reason they are excluded from the
// boards: the feed is the most visible surface catlog has, and a flight that
// cannot score should not be celebrated on the front page either.
//
// The caller supplies the handle because projections.db cannot join to
// events.db (§5.4) — player_id → handle is resolved in Go.
func Summarize(ctx context.Context, ev Event, handle string, fs FlightStateReader) (string, bool, error) {
	if handle == "" {
		return "", false, nil
	}
	ok, err := scoreable(ctx, ev, fs)
	if err != nil || !ok {
		return "", false, err
	}
	line, ok := summary(ev, handle)
	return line, ok, nil
}

func summary(ev Event, handle string) (string, bool) {
	switch ev.Type {
	case "vehicle.impact":
		p, ok := payloadOf[VehicleImpact](ev)
		if !ok || !p.Survived || p.LaunchPad || p.CrewCount < 1 {
			return "", false
		}
		return fmt.Sprintf("%s lithobraked at %s m/s on %s — and survived", handle, num(p.SpeedMs), place(p.Body)), true

	case "vehicle.landed":
		p, ok := payloadOf[VehicleLanded](ev)
		if !ok || !p.Survived {
			// A touchdown the vehicle did not walk away from is a crash, and the
			// `vehicle.rud` emitted beside it already says so. Announcing both
			// would put one moment on the feed twice, in two moods.
			return "", false
		}
		if p.CrewCount < 1 {
			return fmt.Sprintf("%s landed on %s at %s m/s", handle, place(p.Body), num(p.VerticalSpeedMs)), true
		}
		return fmt.Sprintf("%s landed on %s at %s m/s with %s aboard",
			handle, place(p.Body), num(p.VerticalSpeedMs), kittens(p.CrewCount)), true

	case "vehicle.rud":
		p, ok := payloadOf[VehicleRUD](ev)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s lost a vehicle to %s on %s at %s m/s",
			handle, causePhrase(p.Cause), place(p.Body), num(p.SpeedMs)), true

	case "vehicle.orbit":
		p, ok := payloadOf[VehicleOrbit](ev)
		if !ok || p.Phase != "achieved" {
			return "", false
		}
		return fmt.Sprintf("%s made orbit around %s (%s × %s)",
			handle, place(p.Body), altitude(p.ApM), altitude(p.PeM)), true

	case "vehicle.soi":
		p, ok := payloadOf[VehicleSOI](ev)
		if !ok || p.ToBody == "" {
			return "", false
		}
		return fmt.Sprintf("%s entered %s's sphere of influence", handle, place(p.ToBody)), true

	case "kitten.tumble":
		p, ok := payloadOf[KittenTumble](ev)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s's kitten %s took a tumble at %s m/s on %s",
			handle, kittenName(p.Name), num(p.SpeedMs), place(p.Body)), true

	case "kitten.kia":
		p, ok := payloadOf[KittenKIA](ev)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s said goodbye to kitten %s", handle, kittenName(p.Name)), true

	case "flight.ended":
		p, ok := payloadOf[FlightEnded](ev)
		if !ok || p.Reason != "recovered" {
			return "", false
		}
		if p.CrewCount < 1 {
			return fmt.Sprintf("%s recovered a vehicle", handle), true
		}
		return fmt.Sprintf("%s brought %s home safely", handle, kittens(p.CrewCount)), true
	}
	return "", false
}

func causePhrase(cause string) string {
	switch cause {
	case "ground_impact":
		return "a ground impact"
	case "ocean_impact":
		return "an ocean impact"
	case "collision":
		return "a collision"
	case "excessive_g_force":
		return "excessive g-force"
	case "aerodynamic_forces":
		return "aerodynamic forces"
	case "hydrodynamic_forces":
		return "hydrodynamic forces"
	case "":
		return "an unexplained disassembly"
	default:
		return strings.ReplaceAll(cause, "_", " ")
	}
}

// place renders a body name. Bodies are opaque lowercase strings on the wire
// (§4.2) and are shown as they arrive, minus anything that could break a line.
func place(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "somewhere"
	}
	return sanitize(body, 32)
}

func kittenName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "a kitten"
	}
	return sanitize(name, 32)
}

// sanitize keeps the feed printable. Names are a moderation surface (§4.2), so
// control characters and newlines never reach a summary.
func sanitize(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= max {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "?"
	}
	return out
}

func kittens(n int) string {
	if n == 1 {
		return "1 kitten"
	}
	return strconv.Itoa(n) + " kittens"
}

// num renders a measurement: whole numbers above 100, one decimal below, and
// never scientific notation or a NaN leaking into a sentence.
func num(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "?"
	}
	if math.Abs(v) >= 100 {
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

// altitude renders an apsis. §4.2's ap_m/pe_m are altitudes above the parent's
// mean radius (docs/ksa-integration.md), so "120 km" means 120 km up, not
// 120 km from the centre of the world.
func altitude(m float64) string {
	if math.IsNaN(m) || math.IsInf(m, 0) {
		return "?"
	}
	if math.Abs(m) >= 1000 {
		return num(m/1000) + " km"
	}
	return num(m) + " m"
}
