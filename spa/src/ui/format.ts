import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * Display formatting.
 *
 * Everything here is a pure function of its arguments — no `Date.now()`, no
 * locale sniffing at call time — because these are called from render, and the
 * Rules of React require render to be idempotent. The one genuinely
 * time-dependent thing the UI wants (relative timestamps) is a store instead,
 * below.
 */

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });
const plain = new Intl.NumberFormat('en', { maximumFractionDigits: 2 });
const timestamp = new Intl.DateTimeFormat('en-GB', {
  dateStyle: 'medium',
  timeStyle: 'short',
  timeZone: 'UTC',
});

/**
 * A leaderboard value, with its unit.
 *
 * Counters get no decimals; measurements keep two. Large values compact to
 * `1.2M` so a `distance_travelled` row does not shove the rest of the table off
 * a phone screen — the exact figure stays in the `title` attribute.
 */
export function formatValue(value: number, unit: string): string {
  if (!Number.isFinite(value)) return '—';
  const magnitude = Math.abs(value);
  const number = magnitude >= 100_000 ? compact.format(value) : plain.format(value);
  return unit === '' ? number : `${number} ${unit}`;
}

/** The exact value, for a tooltip. */
export function exactValue(value: number, unit: string): string {
  return unit === '' ? String(value) : `${String(value)} ${unit}`;
}

/** A unix-ms instant as a fixed UTC timestamp. */
export function formatInstant(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return 'unknown';
  return timestamp.format(new Date(ms)) + ' UTC';
}

/**
 * A unix-ms instant relative to `now`, e.g. `4m ago`.
 *
 * `now` is a parameter rather than a `Date.now()` call precisely so this stays
 * pure and testable; components get it from [$now].
 */
export function formatAgo(ms: number, now: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return 'unknown';
  const seconds = Math.max(0, Math.round((now - ms) / 1000));
  if (seconds < 45) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${String(minutes)}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${String(hours)}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${String(days)}d ago`;
  return formatInstant(ms);
}

/** How often [$now] ticks. Coarse: nothing on screen is finer than a minute. */
const TICK_MS = 30_000;

/**
 * The current time, as a store.
 *
 * A component may not call `Date.now()` during render — the same inputs would
 * produce a different output, which is exactly what React Compiler's memoization
 * assumes cannot happen. Reading the clock from a store moves the impurity to
 * one place with a lifecycle: the interval runs only while something displays a
 * timestamp, and stops when nothing does.
 */
export const $now: ReadableAtom<number> = (() => {
  const store = atom(Date.now());
  onMount(store, () => {
    const timer = setInterval(() => {
      store.set(Date.now());
    }, TICK_MS);
    return () => {
      clearInterval(timer);
    };
  });
  return store;
})();
