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

/**
 * Installs a `fetch` that answers the given routes and rejects everything else.
 *
 * Returns the mock so a test can assert on call counts.
 */
export function stubFetch(routes: readonly StubbedRoute[]) {
  const mock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const route = routes.find((r) => url.includes(r.path));
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
