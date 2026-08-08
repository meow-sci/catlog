import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { EventRow, EventsResponse } from '../api/types.ts';
import { appendPage, EVENT_ROWS_CAP, mergeHead, useEventLog } from './useEventLog.ts';

/**
 * The generalized pager, exercised directly.
 *
 * The page-level tests (`features.test.tsx`) already prove the pager through a
 * real page and a stubbed fetch; these pin the parts the generalization added —
 * the keyed fetchPage seam, the accumulation cap, and the head merge — where a
 * page test would be all setup.
 */

function row(seq: number, type = 'vehicle.impact'): EventRow {
  return { seq, id: `01J${String(seq)}`, type, ver: 1, recv: 1_800_000_000_000 + seq, payload: {} };
}

function page(events: readonly EventRow[], next?: string): EventsResponse {
  return { limit: 50, events: [...events], ...(next === undefined ? {} : { next }) };
}

describe('appendPage', () => {
  it('appends under the cap without touching anything', () => {
    const out = appendPage([row(3), row(2)], [row(1)], 10);
    expect(out.events.map((e) => e.seq)).toEqual([3, 2, 1]);
    expect(out.dropped).toBe(0);
  });

  it('trims from the head — the newest end — once the cap is hit', () => {
    // Newest first: [5,4,3] + [2,1] over a cap of 4 keeps the *older* window,
    // because the reader who clicked "load more" is walking into history.
    const out = appendPage([row(5), row(4), row(3)], [row(2), row(1)], 4);
    expect(out.events.map((e) => e.seq)).toEqual([4, 3, 2, 1]);
    expect(out.dropped).toBe(1);
  });

  it('defaults to the documented cap', () => {
    const many = Array.from({ length: EVENT_ROWS_CAP }, (_, i) => row(EVENT_ROWS_CAP - i));
    const out = appendPage(many, [row(0)]);
    expect(out.events).toHaveLength(EVENT_ROWS_CAP);
    expect(out.dropped).toBe(1);
  });
});

describe('mergeHead', () => {
  it('dedupes by seq, newest first, preferring the incoming copy', () => {
    const stale = { ...row(2), type: 'stale' };
    const fresh = { ...row(2), type: 'fresh' };
    const out = mergeHead([row(3), stale], [row(4), fresh]);
    expect(out.map((e) => e.seq)).toEqual([4, 3, 2]);
    expect(out[2]?.type).toBe('fresh');
  });
});

describe('useEventLog', () => {
  it('pages through the keyed fetchPage seam, stopping only when next is absent', async () => {
    const fetchPage = vi.fn((before: string | undefined) =>
      before === undefined
        ? Promise.resolve(page([row(3)], '3'))
        : Promise.resolve(page([row(2), row(1)])),
    );
    const { result } = renderHook(() => useEventLog('k', fetchPage));

    await waitFor(() => {
      expect(result.current.status).toBe('ready');
    });
    // **A short page with a cursor is not the end.** One row and a `next` is
    // exactly what a filtered page that hit the scan bound looks like.
    expect(result.current.events).toHaveLength(1);
    expect(result.current.next).toBe('3');

    act(() => {
      result.current.loadMore();
    });
    await waitFor(() => {
      expect(result.current.next).toBeNull();
    });
    expect(fetchPage).toHaveBeenLastCalledWith('3', expect.any(AbortSignal));
    expect(result.current.events.map((e) => e.seq)).toEqual([3, 2, 1]);
    expect(result.current.trimmed).toBe(0);
  });

  it('resets the accumulation when the key changes', async () => {
    const fetchPage = vi.fn(() => Promise.resolve(page([row(9)])));
    const { result, rerender } = renderHook(({ k }) => useEventLog(k, fetchPage), {
      initialProps: { k: 'a' },
    });
    await waitFor(() => {
      expect(result.current.status).toBe('ready');
    });

    rerender({ k: 'b' });
    // Loading again, not showing the old key's rows under the new key's name.
    expect(result.current.status).toBe('loading');
    await waitFor(() => {
      expect(result.current.status).toBe('ready');
    });
    expect(fetchPage).toHaveBeenCalledTimes(2);
  });

  it('refresh merges the re-read head by seq and leaves the cursor alone', async () => {
    let head: EventsResponse = page([row(2), row(1)], '1');
    const fetchPage = vi.fn((before: string | undefined) =>
      Promise.resolve(before === undefined ? head : page([])),
    );
    const { result } = renderHook(() => useEventLog('k', fetchPage));
    await waitFor(() => {
      expect(result.current.status).toBe('ready');
    });

    // Two new rows arrived at the head while the stream was down.
    head = page([row(4), row(3), row(2)], '2');
    act(() => {
      result.current.refresh();
    });
    await waitFor(() => {
      expect(result.current.events.map((e) => e.seq)).toEqual([4, 3, 2, 1]);
    });
    // The accumulated cursor still points where the reader had paged to — the
    // head page's own cursor must not rewind it.
    expect(result.current.next).toBe('1');
  });
});
