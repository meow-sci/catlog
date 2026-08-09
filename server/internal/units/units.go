// Package units renders catlog's numbers the way a person reads them.
//
// # Why this is a package and not a template function
//
// The JSON API publishes raw numbers in the unit the event carried — metres,
// metres per second, milliseconds, joules — because that is the honest wire
// contract and because a formatted string is not a number a client can sort.
// `server/internal/web` turns those numbers into text for the site, and any
// client reading the API has to turn them into text for itself. The rules for
// doing that are therefore neither a template's business nor a client's: they
// live here, written down, so that one record does not read differently
// depending on who rendered it.
//
// The rules are below, with a worked table. [Conformance] is that table as
// data: it is asserted by units_test.go and it is what anyone reimplementing
// the formatting against the public API should reproduce verbatim. **A rule
// change is two edits in one commit — the rule here, and the row in
// [Conformance] that pins it.**
//
// # The rules
//
//  1. A value that is not finite (NaN, ±Inf) renders as "—". Nothing else in
//     catlog is allowed to put a bare NaN on a public page.
//
//  2. **Three significant figures, trailing zeros trimmed.** For a magnitude x,
//     the number of decimals is `clamp(2 - floor(log10 |x|), 0, 6)`; the value
//     is rounded to that many decimals, trailing zeros and any trailing "." are
//     removed, and the integer part is grouped in threes. Zero renders as "0".
//
//     **Grouping is a rendering of the reader's locale, and this package cannot
//     know it.** A Go process serves one HTML response to a shared cache
//     (§4.8's `s-maxage=30`), so the separator baked in here is a *canonical*
//     one — [GroupSeparator] and [DecimalSeparator], which are en-US's — and the
//     browser re-renders it. The site does that through `Intl.NumberFormat`,
//     over the number and precision this package publishes in [Split], which is
//     why that function exists and why [Format] is only ever the fallback a
//     reader with no JavaScript sees. Everything below therefore describes the canonical
//     form; the *placement* of the groups is Intl's business, and it differs by
//     more than a character — en-IN groups 12,34,567 and es-ES leaves 1234
//     alone.
//
//     Rounding is defined on the magnitude — `round(|x| · 10^d) / 10^d` with
//     halves going up — and the sign is re-applied afterwards. Go's math.Round
//     and JavaScript's Math.round both do exactly that on a non-negative input,
//     which is the whole reason it is specified this way rather than left to
//     strconv.FormatFloat and Number.toFixed, which disagree at ties.
//
//  3. **Length (`m`), energy (`J`) and pressure (`Pa`) scale by SI prefix**:
//     the largest of 1, k (10³), M (10⁶), G (10⁹), T (10¹²) whose scaled
//     magnitude is at least 1. There are no prefixes below the base unit —
//     0.5 J is "0.5 J", never "500 mJ" — because catlog has no use for them.
//
//  4. **Speed (`m/s`) never scales.** The prompt for a KSA player is m/s: every
//     speed board is in m/s, orbital velocity is a number this audience reads
//     directly, and a per-value scale would put "7.8 km/s" and "998 m/s" in the
//     same leaderboard column. So 7,799 m/s stays 7,799 m/s.
//
//  5. **Time (`s`, `ms`) becomes a human duration**, in the largest two units
//     that fit. Under a second it is milliseconds; under a minute it is seconds
//     to three significant figures; above that it is a two-component form —
//     "5m 13s", "1h 01m", "243d 01h", "1y 5d" — where the trailing component is
//     zero-padded to two digits except for days inside a year. A year is 365
//     days flat; this is a duration, not a calendar.
//
//  6. Any other unit — `g`, and the count units the counter boards carry
//     (`RUDs`, `tumbles`, `bodies`…) — is rule 2 followed by a space and the
//     unit verbatim. An empty unit is rule 2 alone.
//
//  7. **A column header names the unit only when every cell in the column ends
//     in it.** [Label] is that rule. Rules 3, 4 and 6 all render `value + symbol`
//     — `1.82 Mm`, `7,799 m/s`, `6 RUDs` — so the symbol labels the column and
//     the header shows it verbatim, in its own case. Rule 5 does not: a column of
//     durations reads `37.5 s`, `10h 23m`, `243d 01h`, and no cell in it says
//     "ms". Its header therefore names the **quantity** — `Time` — because a
//     header reading `ms` over `243d 01h` is a statement about catlog's storage
//     that a reader has no way to check and no reason to want. [Measured] is the
//     same distinction in prose, for a sentence like "Measured in …", and it
//     keeps the storage unit visible where [Label] drops it.
//
// # Worked examples
//
//	Format(62, "m/s")        →  "62 m/s"
//	Format(7799, "m/s")      →  "7,799 m/s"      (canonical; the browser localises it)
//	Format(37500, "ms")      →  "37.5 s"
//	Format(48000000, "J")    →  "48 MJ"
//	Format(2.1e7, "s")       →  "243d 01h"
//	Format(214, "m/s")       →  "214 m/s"
//	Format(1820000, "m")     →  "1.82 Mm"
//	Format(4.8e7, "J")       →  "48 MJ"
//	Format(48750, "Pa")      →  "48.8 kPa"
//	Format(9.6, "g")         →  "9.6 g"
//	Format(6, "RUDs")        →  "6 RUDs"
//	Format(math.NaN(), "m")  →  "—"
//
//	Label("m/s")             →  "m/s"
//	Label("ms")              →  "Time"
//	Label("RUDs")            →  "RUDs"
//	Measured("ms")           →  "ms, shown as a duration"
//
// The full list, including the edge cases, is [Conformance]; the header labels
// are [LabelConformance].
package units

import (
	"math"
	"strconv"
	"strings"
)

// Canonical unit ids. These are exactly the strings [stats.Board.Unit] carries,
// plus the two that only appear inside a fold's `context` blob (J, Pa), so a
// caller never has to translate before calling [Format].
const (
	Metres     = "m"
	MetresSec  = "m/s"
	Seconds    = "s"
	Millis     = "ms"
	Joules     = "J"
	Pascals    = "Pa"
	Gs         = "g"
	Kilograms  = "kg"
	Degrees    = "deg"
	MetresSec2 = "m/s2"

	// GroupSeparator and DecimalSeparator are the **canonical** separators —
	// en-US's — that [Format] renders with.
	//
	// They are not a claim about how a reader writes numbers. They are what a
	// cached, locale-blind HTML response can honestly say; the site replaces
	// them with the reader's own through `Intl.NumberFormat`, re-rendering the
	// element [Split] describes. A reader with JavaScript off keeps these, which
	// is why they are the conventional pair and not a sentinel nobody writes.
	GroupSeparator   = ","
	DecimalSeparator = "."

	notANumber  = "—"
	maxDecimals = 6
)

// siPrefixes is the ladder rules 3 walks, largest first.
var siPrefixes = []struct {
	step   float64
	prefix string
}{
	{1e12, "T"},
	{1e9, "G"},
	{1e6, "M"},
	{1e3, "k"},
	{1, ""},
}

// Format renders v in unit, following the rules in the package comment.
//
// unit is a canonical id (the constants above) or a count label — whatever
// [stats.Board.Unit] holds for the board the value came off. An unrecognised
// unit is appended verbatim, which is what makes the counter boards work
// without a table of their labels.
func Format(v float64, unit string) string {
	p := Split(v, unit)
	return p.Head + p.Tail
}

// Parts is [Format]'s answer taken apart, so a browser can put it back together
// in the reader's locale.
//
// It exists for exactly one caller — the server-rendered site's number element,
// which ships `Head + Tail` as text for a reader with no JavaScript and
// `Number`/`Decimals` as attributes for `Intl.NumberFormat` to re-render from.
// Splitting rather than re-parsing is what keeps the browser from having to
// know that "1.82 Mm" is a number and " Mm" is not, or that "243d 01h" is
// neither.
type Parts struct {
	// Head is the canonical rendering of the number, grouped with
	// [GroupSeparator] — "1.82", "7,799", "243d 01h", "—".
	Head string
	// Tail is what follows it, leading space included: " Mm", " m/s", " RUDs",
	// or "" for a bare count.
	Tail string
	// Number is the value Head renders — the *scaled* one, so 1 820 000 m has a
	// Number of 1.82 and a Tail of " Mm". Meaningless unless IsNumber.
	Number float64
	// Decimals is how many decimal places Head actually shows, after the
	// trailing zeros of rule 2 came off. It is `maximumFractionDigits` for the
	// browser, and passing the pre-trim count instead would put "1.820" back on
	// the page.
	Decimals int
	// IsNumber reports that Head is a single number and may be re-rendered.
	//
	// False for "—" and for rule 5's two-component durations: "1h 01m" is two
	// numbers and a layout, the zero-padding is load-bearing, and neither
	// component can reach a thousand — so there is nothing there for a locale to
	// have an opinion about.
	IsNumber bool
}

// Split renders v in unit and reports the pieces; see [Parts].
func Split(v float64, unit string) Parts {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Parts{Head: notANumber}
	}
	switch unit {
	case Metres, Joules, Pascals:
		return scaleSI(v, unit)
	case Seconds:
		return durationParts(v)
	case Millis:
		return durationParts(v / 1000)
	case "":
		return numberParts(v, "")
	default:
		return numberParts(v, " "+unit)
	}
}

// numberParts is rule 2, kept as pieces.
//
// The decimal count it reports is read back off the rendered string rather than
// taken from [decimals], because [trimZeros] has already run: rule 2 renders
// 1.8200 as "1.82", and telling a browser to show two decimals of 1.82 is the
// same string while telling it to show four is not.
func numberParts(v float64, tail string) Parts {
	body := trimZeros(fixed(v, decimals(v)))
	shown := 0
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		shown = len(body) - dot - 1
	}
	// Parsed back rather than carried through, so the attribute a browser
	// re-renders from is exactly the number the fallback text says — never the
	// unrounded input, which would let the two disagree in the last digit.
	n, err := strconv.ParseFloat(body, 64)
	if err != nil {
		n = v
	}
	return Parts{Head: group(body), Tail: tail, Number: n, Decimals: shown, IsNumber: true}
}

// Label is rule 7: the column header for a column of values in unit.
//
// The value cells carry their own units, so the header is not there to repeat
// them — it is there to say what the column *is*. For every unit whose rendered
// form ends in the unit itself (rules 3, 4 and 6) that is the unit, verbatim: a
// length column mixes "999 m" and "1.82 Mm" and `m` names both, a counter board
// mixes "6 RUDs" and "12 RUDs" and `RUDs` names both. A duration column
// (rule 5) is the one place that breaks, because "243d 01h" contains no `ms` and
// "10h 23m" contains no `s`, so the header names the quantity instead.
//
// The returned string is a label to render **as it is**. Do not uppercase it:
// "M/S" is not a unit, "PA" is not a unit, and "RUDS" is not how catlog writes
// that word. The site exempts this one header cell from the uppercasing every
// other header gets, and that is why.
func Label(unit string) string {
	switch unit {
	case Seconds, Millis:
		return "Time"
	case "":
		// No unit at all: nothing to name but the column's job.
		return "Value"
	default:
		return unit
	}
}

// Measured is rule 7 in prose — the noun phrase for a sentence like
// "Measured in ___.", which goes above a board.
//
// It differs from [Label] in two ways, both deliberate. It is lower case,
// because it lands mid-sentence. And for a duration it keeps the storage unit
// rather than replacing it: "ms, shown as a duration" is the one place a reader
// is told that the API publishes milliseconds, which is what makes `data-value`
// and the `title` on every cell legible instead of mysterious.
func Measured(unit string) string {
	switch unit {
	case Seconds, Millis:
		return unit + ", shown as a duration"
	case "":
		return "plain counts"
	default:
		return unit
	}
}

// Number is rule 2 on its own: three significant figures, trailing zeros
// trimmed, thousands grouped. It is what a bare count renders as.
func Number(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return notANumber
	}
	return numberParts(v, "").Head
}

// scaleSI is rule 3: pick the largest prefix whose scaled magnitude is at
// least 1, then render the scaled value with rule 2.
func scaleSI(v float64, unit string) Parts {
	a := math.Abs(v)
	for _, p := range siPrefixes {
		if a >= p.step {
			return numberParts(v/p.step, " "+p.prefix+unit)
		}
	}
	// Below the base unit: no sub-unit prefixes (rule 3).
	return numberParts(v, " "+unit)
}

// Duration is rule 5: seconds rendered as a human duration.
func Duration(seconds float64) string {
	p := durationParts(seconds)
	return p.Head + p.Tail
}

// durationParts is [Duration] kept as pieces; see [Parts].
func durationParts(seconds float64) Parts {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return Parts{Head: notANumber}
	}
	sign := ""
	magnitude := seconds
	if magnitude < 0 {
		sign, magnitude = "-", -magnitude
	}
	switch {
	case magnitude == 0:
		return numberParts(0, " s")
	case magnitude < 1:
		// The signed value goes in, so the minus is the number's rather than a
		// prefix glued onto it — a browser re-rendering the attribute has to
		// produce the whole string, and a locale may not write a minus the way
		// this file does.
		return numberParts(seconds*1000, " ms")
	case magnitude < 60:
		return numberParts(seconds, " s")
	}

	// Whole seconds from here down: a two-component duration has no use for a
	// fraction of its smaller unit, and truncating (rather than rounding) keeps
	// "1h 00m" from appearing for something that has not reached an hour.
	total := int64(magnitude)
	var head string
	switch {
	case total < 3600:
		head = pair(total/60, "m", total%60, "s", true)
	case total < 86400:
		head = pair(total/3600, "h", total%3600/60, "m", true)
	case total < 365*86400:
		head = pair(total/86400, "d", total%86400/3600, "h", true)
	default:
		days := total / 86400
		head = pair(days/365, "y", days%365, "d", false)
	}
	return Parts{Head: sign + head}
}

// pair renders the two-component form. pad zero-fills the trailing component to
// two digits, which lines durations up in a column; the days-inside-a-year case
// passes false because "1y 005d" reads worse than "1y 5d".
func pair(a int64, au string, b int64, bu string, pad bool) string {
	trailing := strconv.FormatInt(b, 10)
	if pad && b < 10 {
		trailing = "0" + trailing
	}
	return strconv.FormatInt(a, 10) + au + " " + trailing + bu
}

// decimals is rule 2's `clamp(2 - floor(log10 |x|), 0, 6)`.
func decimals(v float64) int {
	a := math.Abs(v)
	if a == 0 {
		return 0
	}
	d := 2 - int(math.Floor(math.Log10(a)))
	return min(max(d, 0), maxDecimals)
}

// fixed rounds on the magnitude and re-applies the sign, so Go and JavaScript
// resolve a tie the same way (rule 2).
func fixed(v float64, d int) string {
	sign := ""
	a := v
	if math.Signbit(v) {
		sign, a = "-", -v
	}
	p := math.Pow(10, float64(d))
	r := math.Round(a*p) / p
	out := sign + strconv.FormatFloat(r, 'f', d, 64)
	if out == "-0" || strings.HasPrefix(out, "-0.") && strings.Trim(out, "-0.") == "" {
		// Negative zero is a rounding artefact, never a fact about a flight.
		return strings.TrimPrefix(out, "-")
	}
	return out
}

// trimZeros removes a trailing fraction of zeros, and the point with it.
func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// group inserts [GroupSeparator] between thousands in the integer part. It is
// the canonical grouping only — see the note on that constant.
func group(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(GroupSeparator)
		}
		b.WriteRune(r)
	}
	out := sign + b.String()
	if hasFrac {
		out += DecimalSeparator + frac
	}
	return out
}

// ForKey maps a payload or `context` key to the unit its value is in, so a
// generic renderer (the "Detail" column of a board row, a raw-event view) can
// format a blob it has no schema for.
//
// It is suffix-driven and deliberately total: a key it does not recognise gets
// no unit rather than a wrong one. The trap it exists to avoid is that `_ms`
// means **metres per second** in every §4.2 payload (`speed_ms`, `fastest_ms`)
// while the board unit string "ms" means **milliseconds** — the two are
// different alphabets and only this function knows it.
func ForKey(key string) string {
	k := strings.ToLower(key)
	switch k {
	case "sim_t", "t0_sim", "t1_sim":
		return Seconds
	case "ecc", "n", "part_count", "crew_count", "missions", "stage_index":
		return ""
	}
	// Longest suffix first: "_ms2" must not be read as "_ms".
	for _, s := range []struct{ suffix, unit string }{
		{"_ms2", MetresSec2},
		{"_ms", MetresSec},
		{"_pa", Pascals},
		{"_kg", Kilograms},
		{"_deg", Degrees},
		{"_sim", Seconds},
		{"_j", Joules},
		{"_m", Metres},
		{"_s", Seconds},
		{"_g", Gs},
	} {
		if strings.HasSuffix(k, s.suffix) {
			return s.unit
		}
	}
	return ""
}

// Conformance is the worked table the rules above describe, as data. It is
// asserted by units_test.go, which is why it is exported: the tests live in
// units_test, outside the package.
//
// It is also the citable form of the contract — for docs, and for anyone
// rendering these numbers from the public API themselves — so a future
// `catlogctl` sub-command could emit it as JSON without inventing a second
// list. **Add a row here first when a rule changes.**
var Conformance = []struct {
	Value float64
	Unit  string
	Want  string
}{
	// The five the read-API work package was asked to pin.
	{62, MetresSec, "62 m/s"},
	{7799, MetresSec, "7,799 m/s"},
	{37500, Millis, "37.5 s"},
	{48000000, Joules, "48 MJ"},
	{2.1e7, Seconds, "243d 01h"},

	// Three significant figures, trailing zeros trimmed, groups separated.
	{0, "", "0"},
	{0.002, "", "0.002"},
	{0.5, "", "0.5"},
	{4.25, "", "4.25"},
	{62, "", "62"},
	{214, "", "214"},
	{7799, "", "7,799"},
	{1234567, "", "1,234,567"},
	{-214.4, "", "-214"},

	// Length scales; speed does not.
	{999, Metres, "999 m"},
	{1500, Metres, "1.5 km"},
	{1820000, Metres, "1.82 Mm"},
	{1.5e9, Metres, "1.5 Gm"},
	{4.2e12, Metres, "4.2 Tm"},
	{0.5, Metres, "0.5 m"},
	{2410, MetresSec, "2,410 m/s"},

	// Energy and pressure.
	{9.9e9, Joules, "9.9 GJ"},
	{48750, Pascals, "48.8 kPa"},
	{101325, Pascals, "101 kPa"},
	{21000, Pascals, "21 kPa"},

	// Durations, every rung of the ladder.
	{0, Seconds, "0 s"},
	{0.45, Seconds, "450 ms"},
	{37.5, Seconds, "37.5 s"},
	{59.9, Seconds, "59.9 s"},
	{60, Seconds, "1m 00s"},
	{313, Seconds, "5m 13s"},
	{3661, Seconds, "1h 01m"},
	{86399, Seconds, "23h 59m"},
	{90000, Seconds, "1d 01h"},
	{31536000, Seconds, "1y 0d"},
	{31968000, Seconds, "1y 5d"},
	{312500, Millis, "5m 12s"},
	{-3661, Seconds, "-1h 01m"},

	// Everything else is the number plus the label.
	{9.6, Gs, "9.6 g"},
	{6, "RUDs", "6 RUDs"},
	{12, "tumbles", "12 tumbles"},
	{math.NaN(), Metres, "—"},
	{math.Inf(1), "RUDs", "—"},
}

// LabelConformance is [Conformance] for rule 7: every unit a board can publish,
// the column header it gets from [Label], and the prose form [Measured] gives it.
//
// It is a second table rather than three more columns on the first because the
// two answer different questions — one is per *value*, one is per *unit* — and
// because a header label has no value to be right about.
//
// The same standing rule applies: **add a row here first when a rule
// changes.**
var LabelConformance = []struct {
	Unit string
	// Label is the column header — [Label]'s answer.
	Label string
	// Measured is the noun phrase for "Measured in ___." — [Measured]'s answer.
	Measured string
}{
	// Rules 3, 4 and 6: the rendered cell ends in the unit, so the header is the
	// unit. Nothing here is title-cased, because none of it is a word.
	{MetresSec, "m/s", "m/s"},
	{Metres, "m", "m"},
	{Gs, "g", "g"},
	{Joules, "J", "J"},
	{Pascals, "Pa", "Pa"},

	// Rule 5 is the exception: a duration column says "243d 01h", never "ms".
	{Seconds, "Time", "s, shown as a duration"},
	{Millis, "Time", "ms, shown as a duration"},

	// The counter boards' labels are the name of the thing counted, which is
	// exactly what a header wants, and they are written the way the API writes
	// them — "RUDs", not "RUDS".
	{"RUDs", "RUDs", "RUDs"},
	{"tumbles", "tumbles", "tumbles"},
	{"orbits", "orbits", "orbits"},
	{"bodies", "bodies", "bodies"},
	{"dockings", "dockings", "dockings"},
	{"stagings", "stagings", "stagings"},
	{"kittens", "kittens", "kittens"},

	// A board added later with a label this build has never seen, and the
	// defensive case of no unit at all.
	{"whatevers", "whatevers", "whatevers"},
	{"", "Value", "plain counts"},
}
