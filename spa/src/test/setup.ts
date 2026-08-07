import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';
import { clearMe } from '../state/me.ts';

/**
 * Test setup.
 *
 * Two invariants for the whole suite:
 *
 *  1. **Nothing reaches the network.** `fetch` is replaced with a stub that
 *     fails loudly, so a test that forgets to arrange a response gets a named
 *     error rather than a real request to 127.0.0.1:8080 — or, worse, a pass
 *     that only works on a machine with catlogd running.
 *  2. **Every render is torn down.** React roots left mounted keep nanostores
 *     subscriptions alive, and a lazy store that never reaches disabled mode
 *     leaks its interval into the next test.
 */
beforeEach(() => {
  // 3. **No test inherits another's "me" handle or theme.** Both are
  //    localStorage keys read by lazy nanostores that stay mounted for a second
  //    after the last subscriber goes away, so a leftover value is a
  //    cross-test dependency that only shows up when the files run in a
  //    different order.
  window.localStorage.clear();
  // The atom itself has to be reset too: a lazy nanostore keeps its value for a
  // second after the last subscriber unsubscribes, so clearing storage alone
  // leaves the previous test's handle in memory.
  clearMe();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      throw new Error(`unstubbed fetch: ${String(input)}`);
    }),
  );
  // EventSource exists in happy-dom but would try to open a connection. The feed
  // store degrades gracefully when it is absent, which is what the tests want.
  vi.stubGlobal('EventSource', undefined);
});

afterEach(() => {
  cleanup();
  clearMe();
  window.localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
