import { titleize } from '../context.ts';
import { formatValue, unitForKey } from '../units.ts';

/**
 * The allow-list behind an event-log row's one-line payload summary.
 *
 * # An allow-list, for the same reason `CONTEXT_KEYS` is one
 *
 * An unrecognised type or key is **counted, not shown**: the payload is
 * free-form server-preserved JSON, so a deny-list here would let a key a newer
 * mod version introduces leak into a public table merely because nobody
 * remembered to exclude it. A row's summary may only ever contain values this
 * file has decided are safe and legible; everything else is one click away in
 * the full-payload view, which is *supposed* to show everything.
 *
 * # What is deliberately off the lists
 *
 * - **`install`** — dropped by the server (`readapi/privacy.go`) and must never
 *   be re-invited by a frontend list, even though its absence makes the entry
 *   here look redundant.
 * - **`kid`** — a hashed kitten identity token: not a privacy hazard (it is
 *   already per-player salted), simply sixteen characters of noise where the
 *   kitten's `name` says it better.
 * - **`other_flight`** and every other ULID — they mean nothing to a reader
 *   and eat the row. Same call as hiding `flight` from a board's detail cell.
 *
 * Numbers render through the `unitForKey`/`formatValue` path every other
 * generic renderer uses. Strings are titleized only for the enum-ish keys
 * ([TITLEIZED_KEYS]) — `titleize` splits on dots, so passing `mod_ver`
 * through it would print `0.1.0` as `0 1 0`. Only actual scalars render: an
 * allow-listed key that arrives holding an object renders nothing.
 */
export const SUMMARY_KEYS: Readonly<Record<string, readonly string[]>> = {
  'session.started': ['mod_ver', 'game_build'],
  'flight.started': ['vehicle_name', 'body', 'mass_kg', 'crew_count'],
  'flight.ended': ['reason', 'crew_count'],
  'flight.flagged': ['flag'],
  'vehicle.situation': ['from', 'to', 'body'],
  'vehicle.atmosphere': ['dir', 'body', 'speed_ms'],
  'vehicle.orbit': ['phase', 'body', 'ap_m', 'pe_m'],
  'vehicle.soi': ['from_body', 'to_body'],
  'vehicle.rud': ['cause', 'body', 'speed_ms', 'peak_g'],
  'vehicle.impact': ['body', 'speed_ms', 'energy_j', 'survived'],
  'vehicle.staging': ['stage_index'],
  'vehicle.docked': [],
  'vehicle.undocked': [],
  'engine.ignition': ['engine', 'count'],
  'engine.shutdown': ['engine', 'count'],
  'engine.flameout': ['engine', 'count'],
  'kitten.eva_start': ['name'],
  'kitten.eva_end': ['name', 'duration_s'],
  'kitten.tumble': ['name', 'speed_ms', 'body'],
  'kitten.kia': ['name', 'context'],
  'roster.snapshot': [],
  'telemetry.window': ['body', 'n', 'peak_g', 'max_q_pa'],
};

/**
 * The string-valued keys that hold a lowercase body / enum name and read
 * better titleized: `kerbin` -> `Kerbin`, `ground_impact` -> `Ground Impact`.
 * Free-text and version-shaped strings (`vehicle_name`, `mod_ver`, `name`)
 * are deliberately not here — they render verbatim.
 */
const TITLEIZED_KEYS: ReadonlySet<string> = new Set([
  'body',
  'from',
  'to',
  'from_body',
  'to_body',
  'cause',
  'reason',
  'dir',
  'phase',
  'context',
  'flag',
]);

/** One summary value, rendered — the defensive little sibling of `context.scalar`. */
function summaryValue(key: string, value: string | number | boolean): string {
  if (typeof value === 'number') {
    return Number.isFinite(value) ? formatValue(value, unitForKey(key)) : String(value);
  }
  if (typeof value === 'boolean') return String(value);
  return TITLEIZED_KEYS.has(key) ? titleize(value) : value;
}

export interface SummaryPair {
  readonly key: string;
  readonly value: string;
}

/**
 * The allow-listed pairs of one event's payload, in list order. Empty for an
 * unknown type, a summary-less type, or a payload holding none of its keys —
 * all of which the component renders as a field count instead.
 */
export function summarizeEvent(type: string, payload: unknown): readonly SummaryPair[] {
  if (typeof payload !== 'object' || payload === null) return [];
  const keys = SUMMARY_KEYS[type];
  if (keys === undefined) return [];
  const blob = payload as Record<string, unknown>;
  const out: SummaryPair[] = [];
  for (const key of keys) {
    if (!Object.hasOwn(blob, key)) continue;
    const value = blob[key];
    // Scalars only. An allow-listed key is a claim about a key *and its
    // documented shape*; a key that arrives holding an object is a shape this
    // build has never seen, and gets the unknown treatment.
    if (typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') {
      continue;
    }
    out.push({ key, value: summaryValue(key, value) });
  }
  return out;
}

/** How many top-level keys a payload holds — the fallback summary. */
export function payloadFieldCount(payload: unknown): number {
  if (typeof payload !== 'object' || payload === null) return 0;
  return Object.keys(payload).length;
}
