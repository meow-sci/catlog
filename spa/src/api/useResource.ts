import { useEffect, useRef, useState } from 'react';
import { ApiError, asApiError } from './client.ts';

/**
 * The state of one read-API request.
 *
 * A discriminated union rather than `{ data, error, loading }`: it makes the
 * impossible states (loading *and* errored, ready with no data) unrepresentable,
 * and it makes every page's render a `switch` the type checker completes.
 */
export type Resource<T> =
  /**
   * There is nothing to ask for yet — a search box with one character in it, a
   * comparison with nobody selected. Distinct from `loading` because §7.1's rule
   * is that an absent thing is *absent*, not empty-with-a-spinner.
   */
  | { readonly status: 'idle' }
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly data: T }
  | { readonly status: 'error'; readonly error: ApiError };

const LOADING: Resource<never> = { status: 'loading' };
const IDLE: Resource<never> = { status: 'idle' };

/**
 * How long a settled response may be reused, matching the server's own
 * freshness window (`s-maxage=30` on every read-API response, §4.8).
 *
 * The server's `Cache-Control` is `s-maxage` only — an instruction to a shared
 * cache, which a browser deliberately ignores — so without this map the client
 * re-fetches on every navigation something a CDN already answered. The split of
 * labour: the server still owns *what* is fresh (this constant only mirrors its
 * window), the client merely stops asking the same question twice within it.
 */
const TTL_MS = 30_000;

interface Entry {
  readonly promise: Promise<unknown>;
  readonly controller: AbortController;
  /** Consumers currently subscribed. The last one out aborts an in-flight request. */
  refs: number;
  /** When the request started — the conservative end of the freshness window. */
  readonly at: number;
  settled: boolean;
}

/**
 * One entry per resource key: the in-flight promise (so two components asking
 * for `boards` in the same frame share one request) and, once resolved, the
 * answer for [TTL_MS]. Failures are evicted on rejection so the next mount
 * retries rather than replaying a stale error.
 */
const entries = new Map<string, Entry>();

/** Empties the cache. For tests, which stub `fetch` per case and must not share answers. */
export function clearResourceCache(): void {
  entries.clear();
}

/** The cached entry for `key`, or a freshly started request if there is none worth reusing. */
function acquire(key: string, load: (signal: AbortSignal) => Promise<unknown>): Entry {
  const hit = entries.get(key);
  // An unsettled entry is reused at any age: it is still the freshest possible
  // answer, and starting a second identical request would be the bug.
  if (hit !== undefined && (!hit.settled || Date.now() - hit.at < TTL_MS)) return hit;
  const controller = new AbortController();
  const entry: Entry = {
    promise: load(controller.signal),
    controller,
    refs: 0,
    at: Date.now(),
    settled: false,
  };
  entry.promise.then(
    () => {
      entry.settled = true;
    },
    () => {
      // Errors (and aborts) are not cached. Guarded, so an old failure cannot
      // evict a newer entry that already replaced this one.
      if (entries.get(key) === entry) entries.delete(key);
    },
  );
  entries.set(key, entry);
  return entry;
}

/**
 * Fetches `load()` whenever `key` changes.
 *
 * `key` is the cache key of the request — `board:rud_total:50`, `player:demo_ace`
 * — and is the effect's only dependency. `load` is expected to be a fresh
 * closure on every render (it captures the same values `key` encodes), so it is
 * held in a ref that an effect refreshes rather than listed as a dependency: a
 * dependency on a function identity that changes every render would refetch on
 * every render.
 *
 * This is the "latest ref" pattern, and it is the one place in the app that
 * needs an escape hatch. It stays inside this hook so no page has to know about
 * it, and it obeys the Rules of React the compiler depends on: the ref is only
 * ever touched from effects, never during render.
 *
 * A `null` key means **there is nothing to ask for** — a search box below the
 * server's two-character minimum, a comparison with an empty handle list. No
 * request is made and the resource stays `idle`. That is the mechanism behind
 * "the UI must not fire a request below two characters": the guard is a key, not
 * a condition around a hook, so the hooks below stay unconditional.
 *
 * Requests are shared and briefly remembered through [entries]: same key, same
 * answer, for the server's own 30-second freshness window. Abort semantics
 * survive the sharing — a consumer unmounting merely stops listening, and it is
 * the *last* consumer of a still-flying request that aborts it.
 *
 * The returned value is derived during render — `LOADING` until the resolved
 * result's key matches the requested one — so there is no `setState` in an
 * effect and no flash of stale data when `key` changes. The cache is touched
 * only from effects, never during render, which is what keeps components pure
 * under React Compiler's assumptions; a cache hit therefore costs one microtask
 * rather than one network round trip.
 */
export function useResource<T>(
  key: string | null,
  load: (signal: AbortSignal) => Promise<T>,
): Resource<T> {
  const [settled, setSettled] = useState<{ key: string; value: Resource<T> } | null>(null);

  const latest = useRef(load);
  useEffect(() => {
    latest.current = load;
  });

  useEffect(() => {
    if (key === null) return;
    const entry = acquire(key, latest.current);
    entry.refs += 1;
    let live = true;
    entry.promise.then(
      (data) => {
        if (live) setSettled({ key, value: { status: 'ready', data: data as T } });
      },
      (cause: unknown) => {
        // An abort is a consumer's own cleanup, not a failure to show.
        if (live && !entry.controller.signal.aborted) {
          setSettled({ key, value: { status: 'error', error: asApiError(cause) } });
        }
      },
    );
    return () => {
      live = false;
      entry.refs -= 1;
      if (entry.refs === 0 && !entry.settled) entry.controller.abort();
    };
  }, [key]);

  if (key === null) return IDLE;
  return settled?.key === key ? settled.value : LOADING;
}
