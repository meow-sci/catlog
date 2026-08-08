import { describe, expect, it, vi } from 'vitest';
import { stubFetch } from '../test/http.ts';
import { ApiError, apiGet, getBoard, getBoards, getPlayer } from './client.ts';

describe('apiGet', () => {
  it('decodes a 200 JSON body', async () => {
    stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);
    await expect(getBoards()).resolves.toEqual({ boards: [] });
  });

  it('turns the §4.9 error body into an ApiError carrying code and detail', async () => {
    stubFetch([
      {
        path: '/v1/players/ghost',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);

    const error = await getPlayer('ghost').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 404, code: 'not_found', message: 'no such player' });
    // The one place the 404-means-content decision lives.
    expect((error as ApiError).notFound).toBe(true);
  });

  it('survives a non-2xx with a body that is not JSON at all', async () => {
    // What an upstream proxy actually sends when catlogd is down.
    stubFetch([
      {
        path: '/v1/leaderboards',
        status: 502,
        body: '<html><body>502 Bad Gateway</body></html>',
        contentType: 'text/html',
      },
    ]);

    const error = (await getBoards().catch((e: unknown) => e)) as ApiError;
    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(502);
    expect(error.code).toBe('http_502');
    expect(error.notFound).toBe(false);
  });

  it('rejects a 200 whose body is not JSON rather than letting the parse error escape', async () => {
    stubFetch([{ path: '/v1/leaderboards', body: 'not json at all', contentType: 'text/plain' }]);

    const error = (await getBoards().catch((e: unknown) => e)) as ApiError;
    expect(error).toBeInstanceOf(ApiError);
    expect(error.code).toBe('bad_response');
  });

  it('reports a transport failure as status 0 — a browser never says why', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))),
    );

    const error = (await apiGet('/v1/leaderboards').catch((e: unknown) => e)) as ApiError;
    expect(error.status).toBe(0);
    expect(error.code).toBe('network');
  });

  it('never sends credentials to the read API', async () => {
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);
    await getBoards();
    expect(mock.mock.calls[0]?.[1]).toMatchObject({ credentials: 'omit' });
  });

  it('never renders out of the browser HTTP cache', async () => {
    // §4.8's `stale-while-revalidate=300` is for the shared cache in front of
    // the origin. A browser honours it too, and with no `max-age` that made
    // every response stale on arrival — the SPA rendered one revision behind the
    // server for up to five minutes, while the revalidation request went out
    // anyway. Measured: no reduction in origin requests, just older data.
    // Dropping this option silently reintroduces that.
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);
    await getBoards();
    expect(mock.mock.calls[0]?.[1]).toMatchObject({ cache: 'no-cache' });
  });

  it('encodes paging and path parameters', async () => {
    const mock = stubFetch([
      {
        path: 'limit=10&offset=20',
        body: { stat: 'x', title: '', unit: '', limit: 10, offset: 20, rows: [] },
      },
    ]);
    await getBoard('rud_ground_impact', 10, 20);
    expect(String(mock.mock.calls[0]?.[0])).toContain(
      '/v1/leaderboards/rud_ground_impact?limit=10&offset=20',
    );
  });

  it('escapes a handle that would otherwise change the path', async () => {
    const mock = stubFetch([{ path: '%2F..%2Fadmin', body: {} }]);
    await getPlayer('/../admin').catch(() => undefined);
    expect(String(mock.mock.calls[0]?.[0])).toContain('/v1/players/%2F..%2Fadmin');
  });
});
