import { useEffect, useState } from 'react';
import { MAX_QUERY_LENGTH, MIN_QUERY_LENGTH, searchHandles } from '../api/client.ts';
import type { SearchResponse } from '../api/types.ts';
import { type Resource, useResource } from '../api/useResource.ts';

/** How long the search box waits before asking the server. */
export const SEARCH_DEBOUNCE_MS = 250;

/** How many suggestions a dropdown shows. The results *page* asks for more. */
export const SUGGESTION_LIMIT = 8;

/**
 * `value`, but only after it has stopped changing for `delay` milliseconds.
 *
 * The `setState` runs inside a `setTimeout` scheduled by an effect, not in the
 * effect body — so this is not the cascading-render pattern
 * `react-hooks/set-state-in-effect` exists to catch, and the cleanup cancels the
 * pending timer on every keystroke.
 */
export function useDebounced<T>(value: T, delay: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => {
      setSettled(value);
    }, delay);
    return () => {
      clearTimeout(timer);
    };
  }, [value, delay]);
  return settled;
}

/**
 * Handle suggestions for `raw`, debounced.
 *
 * Two behaviours here are the API's rules rather than taste, and getting either
 * wrong is visible on the very first keystroke:
 *
 *  1. **No request below two characters.** `MinQueryLen` is 2 and a shorter
 *     query is a `400`, not an empty `200`. So a one-character box has *no
 *     suggestions yet* — it does not have an error. The guard is expressed as a
 *     `null` resource key, which means no request is made at all rather than one
 *     being made and its failure swallowed.
 *  2. **`truncated` means "narrow your query", not "load more".** There is no
 *     offset on this endpoint, deliberately: a paged search over a live
 *     directory is a promise the server cannot keep.
 *
 * A new keystroke aborts the request in flight, because `useResource` keys on
 * the query and tears down the previous effect.
 */
export function useHandleSuggestions(
  raw: string,
  limit = SUGGESTION_LIMIT,
): Resource<SearchResponse> {
  const query = raw.trim().slice(0, MAX_QUERY_LENGTH);
  const debounced = useDebounced(query, SEARCH_DEBOUNCE_MS);
  return useResource(
    debounced.length >= MIN_QUERY_LENGTH ? `search:${String(limit)}:${debounced}` : null,
    (signal) => searchHandles(debounced, limit, signal),
  );
}
