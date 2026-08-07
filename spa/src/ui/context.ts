import { formatValue, unitForKey } from './units.ts';

/**
 * Titlecase for a body name or a RUD cause: `luna` → `Luna`,
 * `ground_impact` → `Ground Impact`.
 *
 * A port of `stats.titleize`, which is what generates the board titles. Body and
 * vehicle names arrive lowercase from the game and sit next to those titles, so
 * they should read the same way.
 *
 * **Do not re-titlecase a board title** — the server already did, and a second
 * pass over one it generated differently would silently disagree with the URL.
 */
export function titleize(suffix: string): string {
  const words = suffix.split(/[_\-.]+/u).filter((w) => w !== '');
  if (words.length === 0) return suffix;
  return words.map((w) => w[0]!.toUpperCase() + w.slice(1)).join(' ');
}

/**
 * The display allow-list for a board row's detail column.
 *
 * An unrecognised key is **hidden**, not shown, and that is the whole point of
 * an allow-list rather than a deny-list here: the fold layer can add a context
 * key without a frontend release, and a new internal id cannot leak into a
 * public table merely because nobody remembered to exclude it.
 *
 * What is deliberately off it, and where it went instead:
 *
 * - **`flight`** — a client-minted ULID. It means nothing to a reader and eats
 *   the widest column in the table. Not a privacy hazard (there is no install in
 *   it); it is simply clutter. Visible in the row's Details disclosure and in
 *   the raw event log.
 * - **`career`** — already **relabelled per player** by the server
 *   (`readapi/privacy.go`), because the raw value is derived from the mod's
 *   install id and would otherwise link one person's two handles. It is hidden
 *   here as well, for a different reason: a 16-character token is not a fact a
 *   reader wants in a table.
 *
 * The order is the order it reads best in, and it matches
 * `web/templates.go`'s `contextKeys` exactly — the two frontends must render the
 * same detail cell.
 */
export const CONTEXT_KEYS = ['body', 'from', 'energy_j', 't1_sim'] as const;

export interface ContextPair {
  /** The key with its underscores opened out — `energy j`, `t1 sim`. */
  readonly key: string;
  readonly value: string;
}

/**
 * Decodes a fold's `context` blob for display, keeping only [CONTEXT_KEYS].
 *
 * The values are rendered through `units.ForKey` + `units.Format`, which is what
 * turns `energy_j: 48000000` into `48 MJ` and `t1_sim: 313` into `5m 13s`. The
 * **key** is what says which unit the number is in — there is nothing in the
 * value itself that does.
 */
export function describeContext(context: unknown): ContextPair[] {
  if (typeof context !== 'object' || context === null) return [];
  const blob = context as Record<string, unknown>;
  const out: ContextPair[] = [];
  for (const key of CONTEXT_KEYS) {
    if (!Object.hasOwn(blob, key)) continue;
    out.push({ key: key.replaceAll('_', ' '), value: scalar(key, blob[key]) });
  }
  return out;
}

/** One context or payload value, rendered for a table cell. */
export function scalar(key: string, value: unknown): string {
  if (value === null || value === undefined) return '—';
  if (typeof value === 'string') return titleize(value);
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'number') return formatValue(value, unitForKey(key));
  return JSON.stringify(value);
}

/** Whether a `context` blob has anything at all worth a Details disclosure. */
export function hasContext(context: unknown): boolean {
  return typeof context === 'object' && context !== null && Object.keys(context).length > 0;
}
