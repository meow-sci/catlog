import { renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { stubFetch } from '../test/http.ts';
import { apiGet } from './client.ts';
import { useResource } from './useResource.ts';

/**
 * The client-side half of the caching split (see `client.ts`): the server's
 * `s-maxage=30` is aimed at a shared cache and ignored by browsers, so
 * `useResource` keeps its own memory of the same window. These pin the three
 * behaviours that memory adds — reuse, in-flight dedupe, and *not* remembering
 * failures — plus the abort semantics that had to survive the sharing.
 */
describe('useResource', () => {
  const boards = (signal: AbortSignal) => apiGet<{ boards: [] }>('/v1/leaderboards', signal);

  it('answers a repeated key from memory within the freshness window', async () => {
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);

    const first = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(first.result.current.status).toBe('ready');
    });
    const calls = mock.mock.calls.length;
    first.unmount();

    // A second consumer, later but inside the window: same answer, no request.
    const second = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(second.result.current.status).toBe('ready');
    });
    expect(mock.mock.calls.length).toBe(calls);
  });

  it('dedupes concurrent consumers of one key into one request', async () => {
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);

    const a = renderHook(() => useResource('boards', boards));
    const b = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(a.result.current.status).toBe('ready');
      expect(b.result.current.status).toBe('ready');
    });
    // StrictMode being off in tests, concurrent mounts share one fetch.
    expect(mock).toHaveBeenCalledTimes(1);
  });

  it('does not remember a failure — the next mount retries', async () => {
    const failing = stubFetch([
      { path: '/v1/leaderboards', status: 500, body: { error: 'internal' } },
    ]);
    const first = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(first.result.current.status).toBe('error');
    });
    expect(failing).toHaveBeenCalledTimes(1);
    first.unmount();

    const healed = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);
    const second = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(second.result.current.status).toBe('ready');
    });
    expect(healed).toHaveBeenCalledTimes(1);
  });

  it('aborts an in-flight request when its last consumer unmounts', async () => {
    let aborted: AbortSignal | undefined;
    const never = (signal: AbortSignal) => {
      aborted = signal;
      return new Promise<never>(() => {
        // Never settles: the abort is the only way out.
      });
    };

    const a = renderHook(() => useResource('slow', never));
    const b = renderHook(() => useResource('slow', never));
    expect(aborted?.aborted).toBe(false);

    // The first consumer leaving must NOT abort — the second is still waiting.
    a.unmount();
    expect(aborted?.aborted).toBe(false);

    b.unmount();
    expect(aborted?.aborted).toBe(true);
  });

  it('stays idle on a null key and asks for nothing', () => {
    const mock = stubFetch([]);
    const { result } = renderHook(() => useResource(null, boards));
    expect(result.current.status).toBe('idle');
    expect(mock).not.toHaveBeenCalled();
  });
});
