import { renderHook, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
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

  it('recovers when StrictMode aborts the first mount and immediately remounts', async () => {
    // The regression: StrictMode runs mount -> cleanup -> mount. The cleanup
    // drops refs to 0 and aborts the in-flight request; the remount then found
    // that same entry still in the map, unsettled, and subscribed to it. Its
    // promise was already rejected with the abort, and the abort branch is
    // deliberately silent (an abort is a consumer's own cleanup, not a failure
    // to show) — so the hook sat on `loading` forever and never re-requested.
    //
    // In the browser this was every page that loads through `useResource`
    // stuck on "Loading boards…", while the SSE feed — which does not use this
    // hook — worked, which is what made it look like a server problem.
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);

    const { result } = renderHook(() => useResource('boards', boards), { wrapper: StrictMode });

    await waitFor(() => {
      expect(result.current.status).toBe('ready');
    });
    expect(mock.mock.calls.length).toBeGreaterThanOrEqual(1);
  });

  it('does not hand an aborted request to a new consumer', async () => {
    const mock = stubFetch([{ path: '/v1/leaderboards', body: { boards: [] } }]);

    // Last consumer out aborts the in-flight request...
    const first = renderHook(() => useResource('boards', boards));
    first.unmount();

    // ...so the next consumer must start its own, not adopt the dead one.
    const second = renderHook(() => useResource('boards', boards));
    await waitFor(() => {
      expect(second.result.current.status).toBe('ready');
    });
    // Two calls, not one: the aborted request is not an answer to reuse.
    expect(mock).toHaveBeenCalledTimes(2);
  });

  it('stays idle on a null key and asks for nothing', () => {
    const mock = stubFetch([]);
    const { result } = renderHook(() => useResource(null, boards));
    expect(result.current.status).toBe('idle');
    expect(mock).not.toHaveBeenCalled();
  });
});
