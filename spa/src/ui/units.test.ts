import { describe, expect, it } from 'vitest';
import { CONFORMANCE } from './units.conformance.ts';
import {
  DEGREES,
  exactValue,
  formatValue,
  GS,
  GROUP_SEPARATOR,
  JOULES,
  KILOGRAMS,
  METRES,
  METRES_SEC,
  METRES_SEC2,
  PASCALS,
  SECONDS,
  unitForKey,
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
      expect(formatValue(value, unit)).toBe(want);
    },
  );

  it('reproduces every row the Go table carries', () => {
    // A guard on the transcription itself: rows get deleted by accident during a
    // merge far more easily than they get changed.
    expect(CONFORMANCE).toHaveLength(43);
  });
});

describe('the group separator', () => {
  it('is U+202F and not an ASCII space', () => {
    // A plain space would be invisible in a diff and visible on every page: it
    // wraps, and it is wider.
    expect(GROUP_SEPARATOR).toBe('\u202F');
    expect(formatValue(1234567, '')).toContain('\u202F');
    expect(formatValue(1234567, '')).not.toContain(' ');
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
    expect(formatValue(30000, unitForKey('fastest_ms'))).toBe('30\u202F000 m/s');
    expect(formatValue(30000, 'ms')).toBe('30 s');
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
