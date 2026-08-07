package units_test

import (
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/units"
)

// TestConformance is the whole point of the package: the table in [units.Conformance]
// is the contract the TypeScript implementation in `spa/` has to reproduce, and
// this asserts the Go half of it.
//
// A failure here means one of two things happened. Either a rule changed — in
// which case the package comment, this table and the SPA's copy all move
// together — or a rule broke, in which case the two frontends have just started
// disagreeing about what a record says.
func TestConformance(t *testing.T) {
	for _, tc := range units.Conformance {
		got := units.Format(tc.Value, tc.Unit)
		if got != tc.Want {
			t.Errorf("Format(%v, %q) = %q, want %q", tc.Value, tc.Unit, got, tc.Want)
		}
	}
}

// The separator is U+202F, not an ASCII space: it does not wrap and it does not
// widen the column. A plain space here would be invisible in a diff and visible
// on every page.
func TestGroupSeparatorIsANarrowNoBreakSpace(t *testing.T) {
	got := units.Format(1234567, "")
	if !strings.Contains(got, " ") {
		t.Errorf("Format(1234567) = %q, want U+202F between the groups", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("Format(1234567) = %q, want no ASCII space", got)
	}
}

func TestForKeyReadsTheSuffix(t *testing.T) {
	for key, want := range map[string]string{
		// The trap: `_ms` is metres per second in a payload, but the board unit
		// string "ms" is milliseconds. Only this function knows the difference.
		"speed_ms":         units.MetresSec,
		"surface_speed_ms": units.MetresSec,
		"fastest_ms":       units.MetresSec,
		"accel_ms2":        units.MetresSec2,
		"altitude_m":       units.Metres,
		"ap_m":             units.Metres,
		"travelled_m":      units.Metres,
		"energy_j":         units.Joules,
		"peak_q_pa":        units.Pascals,
		"max_q_pa":         units.Pascals,
		"dyn_pressure_pa":  units.Pascals,
		"duration_s":       units.Seconds,
		"mission_time_s":   units.Seconds,
		"t1_sim":           units.Seconds,
		"sim_t":            units.Seconds,
		"mass_kg":          units.Kilograms,
		"inc_deg":          units.Degrees,
		"peak_g":           units.Gs,
		// Unrecognised keys get no unit rather than a wrong one.
		"body":   "",
		"career": "",
		"ecc":    "",
		"n":      "",
	} {
		if got := units.ForKey(key); got != want {
			t.Errorf("ForKey(%q) = %q, want %q", key, got, want)
		}
	}
}

// Every unit any board publishes must render without falling through to
// something that looks like a bug — a board added later with a new label gets
// "12 whatevers", never "12 " or a panic.
func TestEveryBoardUnitRenders(t *testing.T) {
	for _, unit := range []string{"m/s", "g", "tumbles", "RUDs", "orbits", "bodies", "dockings", "stagings", "kittens", "m", "ms"} {
		got := units.Format(1234.5, unit)
		if got == "" || strings.HasSuffix(got, " ") {
			t.Errorf("Format(1234.5, %q) = %q", unit, got)
		}
	}
}
