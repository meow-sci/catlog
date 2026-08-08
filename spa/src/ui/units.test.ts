import { describe, expect, it } from 'vitest';
import { CONFORMANCE, LABEL_CONFORMANCE } from './units.conformance.ts';
import {
  CANONICAL_LOCALE,
  DEGREES,
  exactValue,
  formatNumber,
  formatValue,
  GS,
  JOULES,
  KILOGRAMS,
  METRES,
  METRES_SEC,
  METRES_SEC2,
  PASCALS,
  SECONDS,
  unitForKey,
  unitLabel,
  unitMeasured,
} from './units.ts';

/**
 * The whole point of `units.ts`.
 *
 * `units.Conformance` is the contract the Go implementation in
 * `server/internal/units` is held to by its own `units_test.go`; this asserts
 * the TypeScript half of the same table. A failure here means one of two things
 * happened: either a rule changed — in which case the Go package comment, its
 * table and this copy all move together — or a rule broke, in which case
 * catlog's two frontends have just started disagreeing about what a record says.
 */
describe('the shared conformance table', () => {
  it.each(CONFORMANCE.map((row) => [row.value, row.unit, row.want] as const))(
    'Format(%p, %p) = %p',
    (value, unit, want) => {
      // Pinned to the canonical locale: the table is the cross-language
      // contract, and a table that read the machine's locale would pass or fail
      // depending on whose machine ran it.
      expect(formatValue(value, unit, CANONICAL_LOCALE)).toBe(want);
    },
  );

  it('reproduces every row the Go table carries', () => {
    // A guard on the transcription itself: rows get deleted by accident during a
    // merge far more easily than they get changed.
    expect(CONFORMANCE).toHaveLength(43);
  });
});

describe('the shared label table', () => {
  it.each(LABEL_CONFORMANCE.map((row) => [row.unit, row.label, row.measured] as const))(
    'Label(%p) = %p, Measured = %p',
    (unit, label, measured) => {
      expect(unitLabel(unit)).toBe(label);
      expect(unitMeasured(unit)).toBe(measured);
    },
  );

  it('reproduces every row the Go table carries', () => {
    expect(LABEL_CONFORMANCE).toHaveLength(16);
  });

  it('names a unit only when every cell in the column ends in it', () => {
    // The whole rule, checked rather than asserted: render a value in the unit
    // and look at what the string ends with. An SI prefix goes *before* the
    // symbol — "1.82 Mm" still ends in "m" — which is why a scaled column keeps
    // its base symbol and a duration column cannot.
    for (const row of LABEL_CONFORMANCE) {
      if (row.unit === '') continue;
      for (const value of [0.45, 37.5, 313, 3661, 90_000, 1234.5, 1.82e6]) {
        const cell = formatValue(value, row.unit);
        expect(cell.endsWith(row.label)).toBe(row.label !== 'Time');
      }
    }
  });

  it('is the fix for a duration column headed "ms"', () => {
    // The defect, spelled out. A career-time board publishes `unit: "ms"` and
    // renders these three strings; not one of them says "ms".
    expect(formatValue(37_500, 'ms')).toBe('37.5 s');
    expect(formatValue(37_380_000, 'ms')).toBe('10h 23m');
    expect(formatValue(2.1e10, 'ms')).toBe('243d 01h');
    expect(unitLabel('ms')).toBe('Time');
    // And the storage unit is not lost — it moves into the prose above the board.
    expect(unitMeasured('ms')).toBe('ms, shown as a duration');
  });
});

describe('grouping', () => {
  it("is the reader's, not a separator catlog picked", () => {
    // The defect this replaced: every reader on earth got a U+202F narrow
    // no-break space between the groups. The joke is that U+202F is not a bad
    // separator — it is fr-FR's, which is the last assertion here — it was just
    // being shown to the other several hundred locales as well.
    expect(formatValue(1234567, '', CANONICAL_LOCALE)).toBe('1,234,567');
    expect(formatValue(1234567, '', 'de-DE')).toBe('1.234.567');
    expect(formatValue(1234567, '', 'en-GB')).toBe('1,234,567');
    expect(formatValue(1234567, '', 'fr-FR')).toBe('1\u202F234\u202F567');
  });

  it('is grouping, not a character swap', () => {
    // Why this cannot be "replace one separator with another" on the rendered
    // string: two locales below disagree about where the groups *are*, not
    // about what goes between them.
    expect(formatNumber(1234567, 'en-IN')).toBe('12,34,567');
    // es-ES leaves a bare four-digit number ungrouped.
    expect(formatNumber(1234, 'es-ES')).toBe('1234');
    expect(formatNumber(1234, CANONICAL_LOCALE)).toBe('1,234');
  });

  it('localises the decimal separator with it', () => {
    expect(formatValue(1820000, 'm', CANONICAL_LOCALE)).toBe('1.82 Mm');
    expect(formatValue(1820000, 'm', 'de-DE')).toBe('1,82 Mm');
    // Rule 5's sub-minute forms are still one number, so they localise too.
    expect(formatValue(37500, 'ms', 'de-DE')).toBe('37,5 s');
    // Its two-component forms are not, and must not acquire a separator.
    expect(formatValue(2.1e7, 's', 'de-DE')).toBe('243d 01h');
  });

  it('keeps the trailing-zero trim that rule 2 asks for', () => {
    // `minimumFractionDigits: 0`, checked: Intl's default of 3 maximum / 0
    // minimum is not what this needs, and a minimum of two would render 1.8
    // as "1.80".
    expect(formatNumber(1.8, CANONICAL_LOCALE)).toBe('1.8');
    expect(formatNumber(1.8, 'de-DE')).toBe('1,8');
    expect(formatNumber(0.002, CANONICAL_LOCALE)).toBe('0.002');
  });

  it('never renders a negative zero', () => {
    // A value that rounds away to nothing keeps no sign: negative zero is a
    // rounding artefact, never a fact about a flight.
    expect(formatNumber(-0, CANONICAL_LOCALE)).toBe('0');
    expect(formatNumber(-1e-9, CANONICAL_LOCALE)).toBe('0');
    expect(formatNumber(-1e-9, 'de-DE')).toBe('0');
    // A value that survives the rounding keeps it.
    expect(formatNumber(-0.004, CANONICAL_LOCALE)).toBe('-0.004');
  });
});

describe('unitForKey', () => {
  it.each([
    // The trap: `_ms` is metres per second in a payload, but the board unit
    // string "ms" is milliseconds. Only this function knows the difference.
    ['speed_ms', METRES_SEC],
    ['surface_speed_ms', METRES_SEC],
    ['fastest_ms', METRES_SEC],
    ['accel_ms2', METRES_SEC2],
    ['altitude_m', METRES],
    ['ap_m', METRES],
    ['travelled_m', METRES],
    ['energy_j', JOULES],
    ['peak_q_pa', PASCALS],
    ['max_q_pa', PASCALS],
    ['dyn_pressure_pa', PASCALS],
    ['duration_s', SECONDS],
    ['mission_time_s', SECONDS],
    ['t1_sim', SECONDS],
    ['sim_t', SECONDS],
    ['mass_kg', KILOGRAMS],
    ['inc_deg', DEGREES],
    ['peak_g', GS],
    // Unrecognised keys get no unit rather than a wrong one.
    ['body', ''],
    ['career', ''],
    ['ecc', ''],
    ['n', ''],
  ])('maps %p to %p', (key, want) => {
    expect(unitForKey(key)).toBe(want);
  });

  it('renders a roster speed as a speed, not as half a minute', () => {
    // The bug this table exists to prevent, spelled out: 30 km/s is an ecliptic
    // -frame roster speed, and a renderer that read `_ms` as milliseconds would
    // print "30 s".
    expect(formatValue(30000, unitForKey('fastest_ms'), CANONICAL_LOCALE)).toBe('30,000 m/s');
    expect(formatValue(30000, 'ms', CANONICAL_LOCALE)).toBe('30 s');
  });
});

describe('every unit a board can publish', () => {
  it.each([
    'm/s',
    'g',
    'tumbles',
    'RUDs',
    'orbits',
    'bodies',
    'dockings',
    'stagings',
    'kittens',
    'm',
    'ms',
  ])('renders %p without falling through to something that looks like a bug', (unit) => {
    const got = formatValue(1234.5, unit);
    expect(got).not.toBe('');
    expect(got.endsWith(' ')).toBe(false);
  });

  it('renders a label this build has never heard of', () => {
    // A board added later gets "12 whatevers", not a blank cell.
    expect(formatValue(12, 'whatevers')).toBe('12 whatevers');
  });
});

describe('exactValue', () => {
  it('keeps the digits the rendered form hides', () => {
    expect(formatValue(48_000_000, 'J')).toBe('48 MJ');
    expect(exactValue(48_000_000, 'J')).toBe('48000000 J');
    expect(exactValue(7799, '')).toBe('7799');
  });

  it('says so rather than putting NaN in a tooltip', () => {
    expect(exactValue(Number.NaN, 'm')).toBe('not a number');
  });
});
