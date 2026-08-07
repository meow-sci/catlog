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
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly data: T }
  | { readonly status: 'error'; readonly error: ApiError };

const LOADING: Resource<never> = { status: 'loading' };

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
 * The returned value is derived during render — `LOADING` until the resolved
 * result's key matches the requested one — so there is no `setState` in an
 * effect and no flash of stale data when `key` changes.
 */
export function useResource<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>,
): Resource<T> {
  const [settled, setSettled] = useState<{ key: string; value: Resource<T> } | null>(null);

  const latest = useRef(load);
  useEffect(() => {
    latest.current = load;
  });

  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    latest.current(controller.signal).then(
      (data) => {
        if (live) setSettled({ key, value: { status: 'ready', data } });
      },
      (cause: unknown) => {
        // An abort is this effect's own cleanup, not a failure to show.
        if (live && !controller.signal.aborted) {
          setSettled({ key, value: { status: 'error', error: asApiError(cause) } });
        }
      },
    );
    return () => {
      live = false;
      controller.abort();
    };
  }, [key]);

  return settled?.key === key ? settled.value : LOADING;
}
