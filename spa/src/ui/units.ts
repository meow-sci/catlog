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
 *     trailing zeros and any trailing `.` are removed, and the integer part is
 *     grouped in threes with U+202F NARROW NO-BREAK SPACE. Zero renders `0`.
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
 *     `7.8 km/s` and `998 m/s` in the same column. `7 799 m/s` stays that.
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
 * U+202F NARROW NO-BREAK SPACE.
 *
 * Not an ASCII space: it does not wrap and it does not widen the column. A plain
 * space here would be invisible in a diff and visible on every page.
 */
export const GROUP_SEPARATOR = ' ';

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
export function formatValue(value: number, unit: string): string {
  if (!Number.isFinite(value)) return NOT_A_NUMBER;
  switch (unit) {
    case METRES:
    case JOULES:
    case PASCALS:
      return scaleSI(value, unit);
    case SECONDS:
      return formatDuration(value);
    case MILLIS:
      return formatDuration(value / 1000);
    case '':
      return formatNumber(value);
    default:
      return formatNumber(value) + ' ' + unit;
  }
}

/**
 * Rule 2 on its own: three significant figures, trailing zeros trimmed,
 * thousands grouped. What a bare count renders as.
 */
export function formatNumber(value: number): string {
  if (!Number.isFinite(value)) return NOT_A_NUMBER;
  return group(trimZeros(fixed(value, decimals(value))));
}

/** Rule 3: the largest prefix whose scaled magnitude is at least 1, then rule 2. */
function scaleSI(value: number, unit: string): string {
  const magnitude = Math.abs(value);
  for (const [step, prefix] of SI_PREFIXES) {
    if (magnitude >= step) return formatNumber(value / step) + ' ' + prefix + unit;
  }
  // Below the base unit: no sub-unit prefixes (rule 3).
  return formatNumber(value) + ' ' + unit;
}

/** Rule 5: seconds rendered as a human duration. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds)) return NOT_A_NUMBER;
  let sign = '';
  let magnitude = seconds;
  if (magnitude < 0) {
    sign = '-';
    magnitude = -magnitude;
  }
  if (magnitude === 0) return '0 s';
  if (magnitude < 1) return sign + formatNumber(magnitude * 1000) + ' ms';
  if (magnitude < 60) return sign + formatNumber(magnitude) + ' s';

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

/** Inserts U+202F between thousands in the integer part. */
function group(s: string): string {
  let sign = '';
  let body = s;
  if (body.startsWith('-')) {
    sign = '-';
    body = body.slice(1);
  }
  const dot = body.indexOf('.');
  const intPart = dot === -1 ? body : body.slice(0, dot);
  let out = '';
  for (let i = 0; i < intPart.length; i++) {
    if (i > 0 && (intPart.length - i) % 3 === 0) out += GROUP_SEPARATOR;
    out += intPart[i];
  }
  return sign + out + (dot === -1 ? '' : body.slice(dot));
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
