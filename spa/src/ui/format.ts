import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * Time, displayed.
 *
 * **Numbers do not live here.** `ui/units.ts` is the port of
 * `server/internal/units` and is the only thing allowed to render a value; this
 * file is instants and durations-since, which are a different problem with a
 * different rule.
 *
 * Everything here is a pure function of its arguments — no `Date.now()`, no
 * locale sniffing at call time — because these are called from render, and the
 * Rules of React require render to be idempotent. The one genuinely
 * time-dependent thing the UI wants (relative timestamps) is a store instead,
 * below.
 */

/**
 * Fixed UTC, never the viewer's locale.
 *
 * "A leaderboard is a shared artefact, and localising it would make two people
 * describing the same row disagree." The server-rendered site writes
 * `2026-08-07 14:32 UTC` and this writes `7 Aug 2026, 14:32 UTC` — two
 * renderings of the same fixed instant, both locale-independent, and §10
 * sanctions the difference. What is *not* sanctioned is a friendly local
 * timestamp on either.
 */
const timestamp = new Intl.DateTimeFormat('en-GB', {
  dateStyle: 'medium',
  timeStyle: 'short',
  timeZone: 'UTC',
});

/** A unix-ms instant as a fixed UTC timestamp. */
export function formatInstant(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return 'unknown';
  return timestamp.format(new Date(ms)) + ' UTC';
}

/**
 * A unix-ms instant as a fixed UTC calendar day — `2026-08-07`.
 *
 * The same string `web.formatDate` writes, and the same string a census bucket
 * key already is, so a "first seen" column and a bucket label read identically.
 * ISO rather than the viewer's date order for the reason at the top of this
 * file: these are days of a shared log, not days in the reader's week.
 */
export function formatDay(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—';
  return new Date(ms).toISOString().slice(0, 10);
}

/** A unix-ms instant as an ISO string, for a `<time datetime>` attribute. */
export function isoInstant(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '';
  return new Date(ms).toISOString();
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
 * produce a different output, which is exactly what React Compiler's
 * memoization assumes cannot happen. Reading the clock from a store moves the
 * impurity to one place with a lifecycle: the interval runs only while something
 * displays a timestamp, and stops when nothing does.
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
