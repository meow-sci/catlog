package units_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/units"
)

// TestConformance is the whole point of the package: [units.Conformance] is the
// written contract for what a catlog number looks like, and this asserts that
// the code still meets it.
//
// A failure here means one of two things happened. Either a rule changed — in
// which case the package comment and this table move together — or a rule
// broke, in which case a record has just started reading differently than the
// contract says it should.
func TestConformance(t *testing.T) {
	for _, tc := range units.Conformance {
		got := units.Format(tc.Value, tc.Unit)
		if got != tc.Want {
			t.Errorf("Format(%v, %q) = %q, want %q", tc.Value, tc.Unit, got, tc.Want)
		}
	}
}

// TestLabelConformance is the same contract for rule 7. A column of durations
// under a header reading "ms" was the defect this table exists to stop coming
// back: no cell in that column says "ms", so the header names the quantity.
func TestLabelConformance(t *testing.T) {
	for _, tc := range units.LabelConformance {
		if got := units.Label(tc.Unit); got != tc.Label {
			t.Errorf("Label(%q) = %q, want %q", tc.Unit, got, tc.Label)
		}
		if got := units.Measured(tc.Unit); got != tc.Measured {
			t.Errorf("Measured(%q) = %q, want %q", tc.Unit, got, tc.Measured)
		}
	}
}

// A header may only name a unit that a reader will actually see at the end of
// every cell beneath it. That is the whole rule, and it is checkable: render a
// value in the unit and look at what the string ends with.
func TestLabelNamesOnlyAUnitEveryCellCarries(t *testing.T) {
	for _, tc := range units.LabelConformance {
		if tc.Unit == "" {
			continue
		}
		for _, v := range []float64{0.45, 37.5, 313, 3661, 90000, 1234.5, 1.82e6} {
			cell := units.Format(v, tc.Unit)
			// An SI prefix goes *before* the symbol — "1.82 Mm" still ends in
			// "m" — which is exactly why a scaled column can keep its base
			// symbol in the header and a duration column cannot.
			named := strings.HasSuffix(cell, tc.Label)
			if tc.Label == "Time" {
				if named {
					t.Errorf("Label(%q) = %q but Format(%v, %q) = %q ends in it: the header should name the unit", tc.Unit, tc.Label, v, tc.Unit, cell)
				}
				continue
			}
			if !named {
				t.Errorf("Label(%q) = %q but Format(%v, %q) = %q does not end in it", tc.Unit, tc.Label, v, tc.Unit, cell)
			}
		}
	}
}

// The canonical separators are en-US's, and they are the *fallback* — the form
// a reader with no JavaScript keeps. The site re-renders them through
// Intl.NumberFormat, so what this asserts is that the fallback is a pair of
// characters people actually write, and not the U+202F narrow no-break space
// this used to bake in for everybody.
func TestCanonicalSeparators(t *testing.T) {
	got := units.Format(1234567, "")
	if got != "1,234,567" {
		t.Errorf("Format(1234567) = %q, want %q", got, "1,234,567")
	}
	if strings.ContainsAny(got, " \u00a0\u202f") {
		t.Errorf("Format(1234567) = %q, want no space of any width between the groups", got)
	}
	if got := units.Format(48750, units.Pascals); got != "48.8 kPa" {
		t.Errorf("Format(48750, Pa) = %q, want %q", got, "48.8 kPa")
	}
}

// Split is what the server-rendered site hands Intl.NumberFormat, so every
// invariant a browser relies on is asserted here rather than in JavaScript:
// Head+Tail is Format exactly, Number is the *scaled* value the reader sees,
// and Decimals is the post-trim count — a pre-trim count would put "1.820" back
// on the page.
func TestSplitDescribesWhatTheBrowserReRenders(t *testing.T) {
	for _, tc := range []struct {
		value    float64
		unit     string
		head     string
		tail     string
		number   float64
		decimals int
		isNumber bool
	}{
		{1820000, units.Metres, "1.82", " Mm", 1.82, 2, true},
		{7799, units.MetresSec, "7,799", " m/s", 7799, 0, true},
		{1234567, "", "1,234,567", "", 1234567, 0, true},
		{48000000, units.Joules, "48", " MJ", 48, 0, true},
		{6, "RUDs", "6", " RUDs", 6, 0, true},
		{-214.4, "", "-214", "", -214, 0, true},
		{0.002, "", "0.002", "", 0.002, 3, true},
		// Rule 5 under a minute is still one number, so it localises.
		{37500, units.Millis, "37.5", " s", 37.5, 1, true},
		{450, units.Millis, "450", " ms", 450, 0, true},
		{0, units.Seconds, "0", " s", 0, 0, true},
		// A two-component duration is not: it is two numbers and a layout, and
		// no component can reach a thousand.
		{2.1e7, units.Seconds, "243d 01h", "", 0, 0, false},
		{-3661, units.Seconds, "-1h 01m", "", 0, 0, false},
		// Neither is the not-a-number.
		{math.NaN(), units.Metres, "—", "", 0, 0, false},
	} {
		p := units.Split(tc.value, tc.unit)
		if p.Head != tc.head || p.Tail != tc.tail {
			t.Errorf("Split(%v, %q) = %q + %q, want %q + %q", tc.value, tc.unit, p.Head, p.Tail, tc.head, tc.tail)
		}
		if got := p.Head + p.Tail; got != units.Format(tc.value, tc.unit) {
			t.Errorf("Split(%v, %q) reassembles to %q, but Format says %q", tc.value, tc.unit, got, units.Format(tc.value, tc.unit))
		}
		if p.IsNumber != tc.isNumber {
			t.Errorf("Split(%v, %q).IsNumber = %v, want %v", tc.value, tc.unit, p.IsNumber, tc.isNumber)
		}
		if !tc.isNumber {
			continue
		}
		if p.Number != tc.number {
			t.Errorf("Split(%v, %q).Number = %v, want %v", tc.value, tc.unit, p.Number, tc.number)
		}
		if p.Decimals != tc.decimals {
			t.Errorf("Split(%v, %q).Decimals = %d, want %d", tc.value, tc.unit, p.Decimals, tc.decimals)
		}
	}
}

// The number a browser re-renders from must round-trip to the text a reader
// without one sees. If it did not, the two would disagree in the last digit
// depending on whether a script ran.
func TestSplitNumberRoundTripsToItsOwnText(t *testing.T) {
	for _, tc := range units.Conformance {
		p := units.Split(tc.Value, tc.Unit)
		if !p.IsNumber {
			continue
		}
		want := strings.ReplaceAll(p.Head, units.GroupSeparator, "")
		got := strconv.FormatFloat(p.Number, 'f', p.Decimals, 64)
		if got != want {
			t.Errorf("Split(%v, %q): Number %v at %d decimals is %q, but Head is %q",
				tc.Value, tc.Unit, p.Number, p.Decimals, got, want)
		}
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
