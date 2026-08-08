import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { EventRow } from '../api/types.ts';
import { mergeTail, mergeTailBatch, TAIL_RETRY_MS, useEventTail } from './useEventTail.ts';

function row(seq: number, type = 'vehicle.impact'): EventRow {
  return {
    seq,
    handle: 'demo_ace',
    id: `01J${String(seq)}`,
    type,
    ver: 1,
    recv: 1_800_000_000_000 + seq,
    payload: {},
  };
}

describe('mergeTail', () => {
  it('prepends the monotonic case without re-sorting', () => {
    expect(mergeTail([row(2), row(1)], row(3)).map((r) => r.seq)).toEqual([3, 2, 1]);
  });

  it('dedupes by seq, preferring the incoming copy in place', () => {
    const out = mergeTail([row(3), row(2)], { ...row(2), type: 'fresh' });
    expect(out.map((r) => r.seq)).toEqual([3, 2]);
    expect(out[1]?.type).toBe('fresh');
  });

  it('slots an out-of-order arrival where it belongs', () => {
    expect(mergeTail([row(4), row(2)], row(3)).map((r) => r.seq)).toEqual([4, 3, 2]);
  });

  it('caps by dropping the oldest', () => {
    expect(mergeTail([row(3), row(2), row(1)], row(4), 3).map((r) => r.seq)).toEqual([4, 3, 2]);
  });
});

describe('mergeTailBatch', () => {
  it('merges a buffered batch: deduped, newest first, capped', () => {
    const out = mergeTailBatch([row(3), row(1)], [row(4), row(3), row(2)], 3);
    expect(out.map((r) => r.seq)).toEqual([4, 3, 2]);
  });
});

/**
 * A hand-rolled EventSource double.
 *
 * The feed's own tests run with EventSource stubbed *out* (its store degrades
 * to a static snapshot), so there was nothing there to reuse: the tail's whole
 * job is the socket lifecycle, and these tests need to drive open, frames,
 * drops and refusals by hand.
 */
class FakeEventSource extends EventTarget {
  static CONNECTING = 0 as const;
  static OPEN = 1 as const;
  static CLOSED = 2 as const;
  static instances: FakeEventSource[] = [];

  readonly url: string;
  readyState: 0 | 1 | 2 = 0;
  closed = false;

  constructor(url: string) {
    super();
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  open() {
    this.readyState = FakeEventSource.OPEN;
    this.dispatchEvent(new Event('open'));
  }

  frame(r: EventRow) {
    this.dispatchEvent(new MessageEvent('raw', { data: JSON.stringify(r) }));
  }

  /** A dropped connection: the browser retries on its own. */
  drop() {
    this.readyState = FakeEventSource.CONNECTING;
    this.dispatchEvent(new Event('error'));
  }

  /** A refused connection (a non-200 — the subscriber-cap 429): fatal. */
  refuse() {
    this.readyState = FakeEventSource.CLOSED;
    this.dispatchEvent(new Event('error'));
  }
}

function install() {
  FakeEventSource.instances = [];
  vi.stubGlobal('EventSource', FakeEventSource);
}

const latest = () => FakeEventSource.instances.at(-1)!;

afterEach(() => {
  vi.useRealTimers();
});

describe('useEventTail', () => {
  it('reports off while disabled and opens nothing', () => {
    install();
    const { result } = renderHook(() => useEventTail({ enabled: false, type: '', paused: false }));
    expect(result.current.status).toBe('off');
    expect(FakeEventSource.instances).toHaveLength(0);
  });

  it('carries the page filters to the stream URL', () => {
    install();
    renderHook(() =>
      useEventTail({ enabled: true, type: 'vehicle.rud', handle: 'demo_ace', paused: false }),
    );
    expect(latest().url).toContain('/v1/events/stream?');
    expect(latest().url).toContain('type=vehicle.rud');
    expect(latest().url).toContain('handle=demo_ace');
  });

  it('goes connecting -> live and prepends frames deduped by seq', async () => {
    install();
    const { result } = renderHook(() => useEventTail({ enabled: true, type: '', paused: false }));
    expect(result.current.status).toBe('connecting');

    act(() => {
      latest().open();
      latest().frame(row(1));
      latest().frame(row(2));
      latest().frame(row(2)); // a duplicate keeps its position
    });
    await waitFor(() => {
      expect(result.current.status).toBe('live');
    });
    expect(result.current.rows.map((r) => r.seq)).toEqual([2, 1]);
  });

  it('buffers while paused behind a pending count, and flush releases it', async () => {
    install();
    const { result } = renderHook(
      ({ paused }) => useEventTail({ enabled: true, type: '', paused }),
      { initialProps: { paused: true } },
    );
    act(() => {
      latest().open();
      latest().frame(row(1));
      latest().frame(row(2));
    });
    await waitFor(() => {
      expect(result.current.pending).toBe(2);
    });
    // Nothing moved on screen: that is the whole point of the pause.
    expect(result.current.rows).toHaveLength(0);

    act(() => {
      result.current.flush();
    });
    expect(result.current.pending).toBe(0);
    expect(result.current.rows.map((r) => r.seq)).toEqual([2, 1]);
  });

  it('auto-flushes when unpaused — the reader came back to the head', async () => {
    install();
    const { result, rerender } = renderHook(
      ({ paused }) => useEventTail({ enabled: true, type: '', paused }),
      { initialProps: { paused: true } },
    );
    act(() => {
      latest().open();
      latest().frame(row(7));
    });
    await waitFor(() => {
      expect(result.current.pending).toBe(1);
    });

    rerender({ paused: false });
    await waitFor(() => {
      expect(result.current.pending).toBe(0);
    });
    expect(result.current.rows.map((r) => r.seq)).toEqual([7]);
  });

  it('reports a drop as reconnecting — the browser retries on its own', async () => {
    install();
    const { result } = renderHook(() => useEventTail({ enabled: true, type: '', paused: false }));
    act(() => {
      latest().open();
      latest().drop();
    });
    await waitFor(() => {
      expect(result.current.status).toBe('reconnecting');
    });
  });

  it('reports a refusal (the 429 subscriber cap) as unavailable and retries itself', async () => {
    vi.useFakeTimers();
    install();
    const { result } = renderHook(() => useEventTail({ enabled: true, type: '', paused: false }));
    act(() => {
      latest().refuse();
    });
    expect(result.current.status).toBe('unavailable');
    expect(FakeEventSource.instances).toHaveLength(1);

    // The retry cadence is the server's own `retry:` value.
    act(() => {
      vi.advanceTimersByTime(TAIL_RETRY_MS);
    });
    expect(FakeEventSource.instances).toHaveLength(2);
    act(() => {
      latest().open();
    });
    expect(result.current.status).toBe('live');
  });

  it('re-reads the snapshot on a reopen, never on the first open', async () => {
    install();
    const onLive = vi.fn();
    renderHook(() => useEventTail({ enabled: true, type: '', paused: false, onLive }));
    act(() => {
      latest().open();
    });
    // The first open is the initial connect; the pager's head fetch is that
    // snapshot. Calling refresh here would double every page load's requests.
    expect(onLive).not.toHaveBeenCalled();

    act(() => {
      latest().drop();
      latest().open();
    });
    expect(onLive).toHaveBeenCalledTimes(1);
  });

  it('closes the socket and clears on unmount', () => {
    install();
    const { unmount } = renderHook(() => useEventTail({ enabled: true, type: '', paused: false }));
    const source = latest();
    unmount();
    expect(source.closed).toBe(true);
  });

  it('reopens with the new filter when it changes', async () => {
    install();
    const { result, rerender } = renderHook(
      ({ type }) => useEventTail({ enabled: true, type, paused: false }),
      { initialProps: { type: '' } },
    );
    act(() => {
      latest().open();
      latest().frame(row(1));
    });
    await waitFor(() => {
      expect(result.current.rows).toHaveLength(1);
    });

    rerender({ type: 'vehicle.rud' });
    // A different filter is a different log: old rows must not leak into it.
    expect(result.current.rows).toHaveLength(0);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(latest().url).toContain('type=vehicle.rud');
    expect(FakeEventSource.instances[0]?.closed).toBe(true);
  });
});
