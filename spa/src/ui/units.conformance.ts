import { GS, JOULES, METRES, METRES_SEC, MILLIS, PASCALS, SECONDS } from './units.ts';

/**
 * `units.Conformance`, transcribed.
 *
 * This is the cross-language table from `server/internal/units/units.go`. The Go
 * half is asserted by `units_test.go`; this half is asserted by `units.test.ts`.
 * Every row must be present and every row must match — a divergence here means
 * the same record now reads differently on catlog's two frontends.
 *
 * It is a hand transcription because `units.Conformance` is a Go `var`, not a
 * JSON file. The Go package comment contemplates a `catlogctl` sub-command that
 * would emit it and remove the transcription; until that exists, **a rule change
 * is three edits in one commit** — `units.go`, `units_test.go`'s table, and this
 * file plus `units.ts`.
 *
 * The separator in the expectations below is U+202F, not a space. It looks like
 * one. It is not one, and `units.test.ts` asserts that too.
 */
export interface ConformanceRow {
  readonly value: number;
  readonly unit: string;
  readonly want: string;
}

export const CONFORMANCE: readonly ConformanceRow[] = [
  // The five the read-API work package was asked to pin. Note what they show:
  // an orbital speed stays in m/s and gets grouped, a career time becomes a
  // duration, an impact energy takes an SI prefix, a transfer becomes a
  // two-component duration.
  { value: 62, unit: METRES_SEC, want: '62 m/s' },
  { value: 7799, unit: METRES_SEC, want: '7\u202F799 m/s' },
  { value: 37500, unit: MILLIS, want: '37.5 s' },
  { value: 48000000, unit: JOULES, want: '48 MJ' },
  { value: 2.1e7, unit: SECONDS, want: '243d 01h' },

  // Three significant figures, trailing zeros trimmed, groups separated.
  { value: 0, unit: '', want: '0' },
  { value: 0.002, unit: '', want: '0.002' },
  { value: 0.5, unit: '', want: '0.5' },
  { value: 4.25, unit: '', want: '4.25' },
  { value: 62, unit: '', want: '62' },
  { value: 214, unit: '', want: '214' },
  { value: 7799, unit: '', want: '7\u202F799' },
  { value: 1234567, unit: '', want: '1\u202F234\u202F567' },
  { value: -214.4, unit: '', want: '-214' },

  // Length scales; speed does not.
  { value: 999, unit: METRES, want: '999 m' },
  { value: 1500, unit: METRES, want: '1.5 km' },
  { value: 1820000, unit: METRES, want: '1.82 Mm' },
  { value: 1.5e9, unit: METRES, want: '1.5 Gm' },
  { value: 4.2e12, unit: METRES, want: '4.2 Tm' },
  { value: 0.5, unit: METRES, want: '0.5 m' },
  { value: 2410, unit: METRES_SEC, want: '2\u202F410 m/s' },

  // Energy and pressure.
  { value: 9.9e9, unit: JOULES, want: '9.9 GJ' },
  { value: 48750, unit: PASCALS, want: '48.8 kPa' },
  { value: 101325, unit: PASCALS, want: '101 kPa' },
  { value: 21000, unit: PASCALS, want: '21 kPa' },

  // Durations, every rung of the ladder.
  { value: 0, unit: SECONDS, want: '0 s' },
  { value: 0.45, unit: SECONDS, want: '450 ms' },
  { value: 37.5, unit: SECONDS, want: '37.5 s' },
  { value: 59.9, unit: SECONDS, want: '59.9 s' },
  { value: 60, unit: SECONDS, want: '1m 00s' },
  { value: 313, unit: SECONDS, want: '5m 13s' },
  { value: 3661, unit: SECONDS, want: '1h 01m' },
  { value: 86399, unit: SECONDS, want: '23h 59m' },
  { value: 90000, unit: SECONDS, want: '1d 01h' },
  { value: 31536000, unit: SECONDS, want: '1y 0d' },
  { value: 31968000, unit: SECONDS, want: '1y 5d' },
  { value: 312500, unit: MILLIS, want: '5m 12s' },
  { value: -3661, unit: SECONDS, want: '-1h 01m' },

  // Everything else is the number plus the label.
  { value: 9.6, unit: GS, want: '9.6 g' },
  { value: 6, unit: 'RUDs', want: '6 RUDs' },
  { value: 12, unit: 'tumbles', want: '12 tumbles' },
  { value: Number.NaN, unit: METRES, want: '—' },
  { value: Number.POSITIVE_INFINITY, unit: 'RUDs', want: '—' },
];
