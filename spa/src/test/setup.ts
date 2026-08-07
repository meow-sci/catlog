import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';

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
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
