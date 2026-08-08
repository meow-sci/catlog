import { vi } from 'vitest';

/**
 * Routes stubbed `fetch` calls by URL suffix.
 *
 * Matching on a suffix rather than the whole URL keeps the fixtures independent
 * of `VITE_CATLOG_API_BASE`, which differs between a local run and CI.
 */
export interface StubbedRoute {
  /**
   * Matched as a substring of the request URL, query string included. Routes are
   * tried in order, so put the more specific one first.
   */
  readonly path: string;
  readonly status?: number;
  /** The response body. Objects are JSON-encoded; strings are sent verbatim. */
  readonly body: unknown;
  /** Overrides the content type — used to test a non-JSON body. */
  readonly contentType?: string;
}

/** What a real `fetch` rejects with when its signal fires. */
function abortError(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException('The operation was aborted.', 'AbortError');
}

/**
 * Installs a `fetch` that answers the given routes and rejects everything else.
 *
 * **It honours `init.signal`**, because a stub that does not is a stub that
 * cannot reproduce anything about cancellation. One did not, and the cost was a
 * real bug the whole suite was structurally blind to: an aborted request still
 * resolved 200 here, so `useResource` reusing an already-aborted entry looked
 * perfectly healthy in tests and hung forever in a browser. See the StrictMode
 * cases in `useResource.test.ts`.
 *
 * The signal is checked twice, matching the real thing: once on entry (an
 * already-aborted signal never reaches the network) and once after yielding, so
 * a caller that aborts synchronously — which is exactly what React's StrictMode
 * mount/cleanup/mount does — sees a rejection rather than an answer.
 *
 * Returns the mock so a test can assert on call counts.
 */
export function stubFetch(routes: readonly StubbedRoute[]) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const signal = init?.signal ?? null;
    // Read through a call, not a narrowed property: the whole point is that the
    // answer changes between the two checks, which is exactly what control-flow
    // narrowing would optimise away.
    const aborted = () => signal !== null && signal.aborted;
    if (aborted()) throw abortError(signal as AbortSignal);

    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const route = routes.find((r) => url.includes(r.path));

    // Yield once before answering: a synchronous abort by the caller must land
    // before the response does, as it would against a real network.
    await Promise.resolve();
    if (aborted()) throw abortError(signal as AbortSignal);

    if (route === undefined) {
      return new Response('not stubbed', { status: 599 });
    }
    const contentType = route.contentType ?? 'application/json';
    const body = typeof route.body === 'string' ? route.body : JSON.stringify(route.body);
    return new Response(body, {
      status: route.status ?? 200,
      headers: { 'Content-Type': contentType },
    });
  });
  vi.stubGlobal('fetch', mock);
  return mock;
}
