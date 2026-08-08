/**
 * catlog's numbers, rendered the way a person reads them.
 *
 * **This is a port of `server/internal/units/units.go`, and that file is the
 * authority.** The two implementations must agree *character for character* or
 * the same record reads differently depending on which of catlog's two frontends
 * you opened. The rules are restated below so this file is readable on its own;
 * when it and `units.go` disagree, `units.go` wins and this file is the bug.
 *
 * What pins them together is `units.Conformance` — a Go `var` asserted by
 * `units_test.go` and transcribed into `units.conformance.ts`, asserted by
 * `units.test.ts`. **A rule change is three edits in one commit**: `units.go`,
 * its table, and this port.
 *
 * # The rules
 *
 *  1. Not finite (NaN, ±Inf) → `—`. Nothing in catlog may put a bare NaN on a
 *     public page.
 *
 *  2. **Three significant figures, trailing zeros trimmed.** Decimals are
 *     `clamp(2 - floor(log10 |x|), 0, 6)`; the value is rounded to that many,
 *     trailing zeros and any trailing `.` are removed, and the result is
 *     grouped by `Intl.NumberFormat` **in the reader's locale**. Zero renders `0`.
 *
 *     Grouping is the one rule that is not a constant. A leaderboard is a shared
 *     artefact and its *timestamps* are therefore fixed UTC (`ui/format.ts`) —
 *     but a thousands separator is not a fact about the record, it is a fact
 *     about the reader, and a U+202F between the groups was a third thing that
 *     is nobody's convention. So the digits, the significant figures and the SI
 *     prefix are decided here, and the *separators and group sizes* are decided
 *     by Intl — which knows that en-IN writes `12,34,567` and that es-ES leaves
 *     `1234` alone, neither of which swapping one character can produce.
 *
 *     `units.go` renders the same values with a **canonical** en-US grouping,
 *     because a cached HTML response cannot know a locale; the server-rendered
 *     site re-renders it in the browser from `units.Split`. So the conformance
 *     table below is pinned to {@link CANONICAL_LOCALE}, and that is the locale
 *     in which the two frontends must still agree character for character.
 *
 *     Rounding is defined **on the magnitude** — `round(|x| · 10^d) / 10^d`,
 *     halves up, sign re-applied afterwards. That is spelled out rather than
 *     left to `Number.toFixed` because `toFixed` and Go's
 *     `strconv.FormatFloat` resolve a tie differently, while `Math.round` and
 *     `math.Round` agree on a non-negative input. So the dance below is not
 *     ceremony: calling `toFixed` on the raw value is the bug this avoids.
 *
 *  3. **Length (`m`), energy (`J`) and pressure (`Pa`) scale by SI prefix** —
 *     the largest of 1, k, M, G, T whose scaled magnitude is at least 1. There
 *     are no prefixes below the base unit: `0.5 J` is `0.5 J`, never `500 mJ`.
 *
 *  4. **Speed (`m/s`) never scales.** m/s is the prompt a KSA player reads
 *     directly, every speed board is in m/s, and a per-value scale would put
 *     `7.8 km/s` and `998 m/s` in the same column. `7,799 m/s` stays that.
 *
 *  5. **Time (`s`, `ms`) becomes a human duration**, largest two units that fit:
 *     under a second, milliseconds; under a minute, seconds at three significant
 *     figures; above, `5m 13s`, `1h 01m`, `243d 01h`, `1y 5d` — trailing
 *     component zero-padded to two digits except days inside a year, whole
 *     seconds truncated rather than rounded so `1h 00m` never appears for
 *     something that has not reached an hour. A year is 365 days flat: this is a
 *     duration, not a calendar.
 *
 *  6. Anything else — `g`, and the counter boards' labels (`RUDs`, `tumbles`,
 *     `bodies`…) — is rule 2 plus a space plus the unit verbatim. An empty unit
 *     is rule 2 alone. That is what makes a board added later with a label this
 *     build has never seen render `12 whatevers` rather than something that
 *     looks like a bug.
 *
 *  7. **A column header names the unit only when every cell in the column ends
 *     in it.** {@link unitLabel} is that rule. Rules 3, 4 and 6 all render
 *     `value + symbol` — `1.82 Mm`, `7,799 m/s`, `6 RUDs` — so the symbol labels
 *     the column and the header shows it verbatim, in its own case. Rule 5 does
 *     not: a column of durations reads `37.5 s`, `10h 23m`, `243d 01h`, and no
 *     cell in it says `ms`. Its header therefore names the **quantity** —
 *     `Time`. {@link unitMeasured} is the same distinction in prose, for
 *     "Measured in …", and it keeps the storage unit the header drops.
 */

/** Canonical unit ids — exactly the strings `stats.Board.Unit` carries, plus the two that only appear inside a fold's `context` blob. */
export const METRES = 'm';
export const METRES_SEC = 'm/s';
export const SECONDS = 's';
export const MILLIS = 'ms';
export const JOULES = 'J';
export const PASCALS = 'Pa';
export const GS = 'g';
export const KILOGRAMS = 'kg';
export const DEGREES = 'deg';
export const METRES_SEC2 = 'm/s2';

/**
 * The locale the cross-frontend conformance table is written in.
 *
 * Every formatter below takes an optional `locale` and defaults it to
 * `undefined`, which is Intl for "whatever this browser is" — and that default
 * is the feature. This constant is what the tests pin instead, and it is the
 * same grouping `units.Format` bakes into the server-rendered fallback, so
 * "the two frontends agree" stays a statement somebody can check.
 */
export const CANONICAL_LOCALE = 'en-US';

/** What a value that is not a number renders as. Never `NaN`, never `0`, never blank. */
export const NOT_A_NUMBER = '—';

const MAX_DECIMALS = 6;

/** The ladder rule 3 walks, largest first. */
const SI_PREFIXES: readonly (readonly [step: number, prefix: string])[] = [
  [1e12, 'T'],
  [1e9, 'G'],
  [1e6, 'M'],
  [1e3, 'k'],
  [1, ''],
];

/**
 * Renders `value` in `unit` — one string, number and unit together.
 *
 * There is no `{ number, unit }` split, and that is the contract: the *string*
 * is what two implementations can be held to.
 *
 * `unit` is a canonical id above or a count label — whatever the board the value
 * came off publishes. An unrecognised unit is appended verbatim, which is what
 * makes the counter boards work without a table of their labels.
 */
export function formatValue(value: number, unit: string, locale?: string): string {
  if (!Number.isFinite(value)) return NOT_A_NUMBER;
  switch (unit) {
    case METRES:
    case JOULES:
    case PASCALS:
      return scaleSI(value, unit, locale);
    case SECONDS:
      return formatDuration(value, locale);
    case MILLIS:
      return formatDuration(value / 1000, locale);
    case '':
      return formatNumber(value, locale);
    default:
      return formatNumber(value, locale) + ' ' + unit;
  }
}

/**
 * Rule 7: the column header for a column of values in `unit`.
 *
 * The value cells carry their own units, so the header is not there to repeat
 * them — it is there to say what the column *is*. For every unit whose rendered
 * form ends in the unit itself (rules 3, 4 and 6) that is the unit, verbatim: a
 * length column mixes `999 m` and `1.82 Mm` and `m` names both, a counter board
 * mixes `6 RUDs` and `12 RUDs` and `RUDs` names both. A duration column (rule 5)
 * is the one place that breaks, because `243d 01h` contains no `ms` and
 * `10h 23m` contains no `s`, so the header names the quantity instead.
 *
 * The returned string is a label to render **as it is**. Do not uppercase it:
 * `M/S` is not a unit, `PA` is not a unit, and `RUDS` is not how catlog writes
 * that word — so the header cell that carries this is the one cell exempted from
 * the uppercasing every other header gets, in both frontends.
 */
export function unitLabel(unit: string): string {
  switch (unit) {
    case SECONDS:
    case MILLIS:
      return 'Time';
    // No unit at all: nothing to name but the column's job.
    case '':
      return 'Value';
    default:
      return unit;
  }
}

/**
 * Rule 7 in prose — the noun phrase for a sentence like "Measured in ___.",
 * which both frontends put above a board.
 *
 * It differs from {@link unitLabel} in two ways, both deliberate. It is lower
 * case, because it lands mid-sentence. And for a duration it keeps the storage
 * unit rather than replacing it: `ms, shown as a duration` is the one place a
 * reader is told that the API publishes milliseconds, which is what makes
 * `data-value` and the `title` on every cell legible instead of mysterious.
 */
export function unitMeasured(unit: string): string {
  switch (unit) {
    case SECONDS:
    case MILLIS:
      return unit + ', shown as a duration';
    case '':
      return 'plain counts';
    default:
      return unit;
  }
}

/**
 * Rule 2 on its own: three significant figures, trailing zeros trimmed,
 * thousands grouped in `locale`. What a bare count renders as.
 *
 * `locale` defaults to the reader's. Pass one only to pin the output — the
 * conformance table does, with {@link CANONICAL_LOCALE}.
 */
export function formatNumber(value: number, locale?: string): string {
  if (!Number.isFinite(value)) return NOT_A_NUMBER;
  // Two steps, and the order is the whole of rule 2: *this file* decides how
  // many digits there are, and Intl decides how they are written. Handing the
  // raw value to Intl with `maximumSignificantDigits: 3` would look equivalent
  // and is not — it rounds on its own terms rather than on the magnitude, so Go
  // and the browser would part company at a tie.
  const body = trimZeros(fixed(value, decimals(value)));
  return grouped(Number(body), fractionDigits(body), locale);
}

/** How many decimals a rendered body actually shows, after {@link trimZeros}. */
function fractionDigits(body: string): number {
  const dot = body.indexOf('.');
  return dot === -1 ? 0 : body.length - dot - 1;
}

/** Rule 3: the largest prefix whose scaled magnitude is at least 1, then rule 2. */
function scaleSI(value: number, unit: string, locale?: string): string {
  const magnitude = Math.abs(value);
  for (const [step, prefix] of SI_PREFIXES) {
    if (magnitude >= step) return formatNumber(value / step, locale) + ' ' + prefix + unit;
  }
  // Below the base unit: no sub-unit prefixes (rule 3).
  return formatNumber(value, locale) + ' ' + unit;
}

/** Rule 5: seconds rendered as a human duration. */
export function formatDuration(seconds: number, locale?: string): string {
  if (!Number.isFinite(seconds)) return NOT_A_NUMBER;
  let sign = '';
  let magnitude = seconds;
  if (magnitude < 0) {
    sign = '-';
    magnitude = -magnitude;
  }
  if (magnitude === 0) return formatNumber(0, locale) + ' s';
  // The signed value goes to formatNumber rather than a '-' being glued on
  // afterwards: a locale does not have to write a minus the way this file does.
  if (magnitude < 1) return formatNumber(seconds * 1000, locale) + ' ms';
  if (magnitude < 60) return formatNumber(seconds, locale) + ' s';

  // Whole seconds from here down: a two-component duration has no use for a
  // fraction of its smaller unit, and truncating (rather than rounding) is what
  // keeps "1h 00m" from appearing for something that has not reached an hour.
  const total = Math.trunc(magnitude);
  if (total < 3600) return sign + pair(Math.trunc(total / 60), 'm', total % 60, 's', true);
  if (total < 86_400) {
    return sign + pair(Math.trunc(total / 3600), 'h', Math.trunc((total % 3600) / 60), 'm', true);
  }
  if (total < 365 * 86_400) {
    return (
      sign + pair(Math.trunc(total / 86_400), 'd', Math.trunc((total % 86_400) / 3600), 'h', true)
    );
  }
  const days = Math.trunc(total / 86_400);
  return sign + pair(Math.trunc(days / 365), 'y', days % 365, 'd', false);
}

/**
 * The two-component form. `pad` zero-fills the trailing component to two digits,
 * which lines durations up in a column; the days-inside-a-year case passes
 * false, because `1y 005d` reads worse than `1y 5d`.
 */
function pair(a: number, aUnit: string, b: number, bUnit: string, pad: boolean): string {
  const trailing = pad && b < 10 ? '0' + String(b) : String(b);
  return String(a) + aUnit + ' ' + trailing + bUnit;
}

/** Rule 2's `clamp(2 - floor(log10 |x|), 0, 6)`. */
function decimals(value: number): number {
  const magnitude = Math.abs(value);
  if (magnitude === 0) return 0;
  const d = 2 - Math.floor(Math.log10(magnitude));
  return Math.min(Math.max(d, 0), MAX_DECIMALS);
}

/**
 * Rounds on the magnitude and re-applies the sign, so Go and JavaScript resolve
 * a tie the same way (rule 2).
 *
 * `toFixed` is called only *after* the rounding, on a value that is already the
 * nearest double to a multiple of 10^-d — so there is no tie left for it to
 * resolve differently from `strconv.FormatFloat`.
 */
function fixed(value: number, d: number): string {
  let sign = '';
  let magnitude = value;
  // `Object.is` is what distinguishes -0 from 0; `<` does not, and Go's
  // math.Signbit does. Rule 2 says the sign is re-applied, so it has to be read
  // the same way on both sides.
  if (value < 0 || Object.is(value, -0)) {
    sign = '-';
    magnitude = -value;
  }
  const scale = 10 ** d;
  const rounded = Math.round(magnitude * scale) / scale;
  // toFixed switches to exponential notation at 1e21 and Go's 'f' verb never
  // does. Nothing catlog measures gets there — decimals() has already clamped d
  // to 0 long before — but a garbage value must not render as "4.2e+21".
  const digits = rounded >= 1e21 ? BigInt(rounded).toString() : rounded.toFixed(d);
  const out = sign + digits;
  // Negative zero is a rounding artefact, never a fact about a flight.
  if (out === '-0' || (out.startsWith('-0.') && /^[-0.]*$/.test(out))) return out.slice(1);
  return out;
}

/** Removes a trailing fraction of zeros, and the point with it. */
function trimZeros(s: string): string {
  if (!s.includes('.')) return s;
  const trimmed = s.replace(/0+$/, '');
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

/**
 * Rule 2's last step: `n`, shown to exactly `decimals` places, written the way
 * `locale` writes numbers.
 *
 * `minimumFractionDigits: 0` is what stops the trim being undone — the caller
 * has already decided that 1.8200 is `1.82`, and a minimum of two would put the
 * zeros back on a value that happened to land on 1.8.
 *
 * Formatters are cached because constructing one is the expensive part of Intl
 * — it resolves the locale and builds the pattern — and this is called once per
 * table cell.
 */
const FORMATTERS = new Map<string, Intl.NumberFormat>();

function grouped(n: number, decimals: number, locale: string | undefined): string {
  const key = `${locale ?? ''}\u0000${String(decimals)}`;
  let formatter = FORMATTERS.get(key);
  if (formatter === undefined) {
    // `useGrouping` is left at its default, which is `auto` — the locale's own
    // `minimumGroupingDigits`. Forcing it on would render es-ES's 1234 as
    // "1.234", which that locale deliberately does not write.
    formatter = new Intl.NumberFormat(locale, {
      minimumFractionDigits: 0,
      maximumFractionDigits: decimals,
    });
    FORMATTERS.set(key, formatter);
  }
  // Negative zero is a rounding artefact, never a fact about a flight. `fixed`
  // has already dropped the sign from the string; this stops a -0 arriving
  // from a caller that did its own arithmetic.
  return formatter.format(Object.is(n, -0) ? 0 : n);
}

/**
 * The unit a payload or `context` key's value is in, so a generic renderer — a
 * board row's detail, the raw-event view — can format a blob it has no schema
 * for.
 *
 * Suffix-driven and deliberately **total**: a key it does not recognise gets no
 * unit rather than a wrong one.
 *
 * **The trap this exists for:** `_ms` means **metres per second** in every
 * payload catlog records (`speed_ms`, `surface_speed_ms`, `fastest_ms`) while
 * the board unit string `"ms"` means **milliseconds**. A renderer that keys on
 * the suffix pattern without this table shows a 30 km/s ecliptic-frame roster
 * speed as a 30-second duration. `_ms2` is matched before `_ms` for the same
 * class of reason.
 */
export function unitForKey(key: string): string {
  const k = key.toLowerCase();
  switch (k) {
    case 'sim_t':
    case 't0_sim':
    case 't1_sim':
      return SECONDS;
    case 'ecc':
    case 'n':
    case 'part_count':
    case 'crew_count':
    case 'missions':
    case 'stage_index':
      return '';
  }
  // Longest suffix first: "_ms2" must not be read as "_ms".
  const suffixes: readonly (readonly [suffix: string, unit: string])[] = [
    ['_ms2', METRES_SEC2],
    ['_ms', METRES_SEC],
    ['_pa', PASCALS],
    ['_kg', KILOGRAMS],
    ['_deg', DEGREES],
    ['_sim', SECONDS],
    ['_j', JOULES],
    ['_m', METRES],
    ['_s', SECONDS],
    ['_g', GS],
  ];
  for (const [suffix, unit] of suffixes) {
    if (k.endsWith(suffix)) return unit;
  }
  return '';
}

/**
 * The exact figure, for a `title`.
 *
 * §4.4: every formatted value carries one, because it is how a reader recovers
 * the digits `48 MJ` hides.
 */
export function exactValue(value: number, unit: string): string {
  if (!Number.isFinite(value)) return 'not a number';
  return unit === '' ? String(value) : `${String(value)} ${unit}`;
}
