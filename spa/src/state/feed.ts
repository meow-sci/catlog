import { atom, onMount, type ReadableAtom } from 'nanostores';
import { feedStreamUrl, getFeed } from '../api/client.ts';
import type { FeedRow } from '../api/types.ts';

/**
 * The live activity feed.
 *
 * # Why this is a store and not a hook
 *
 * It is one connection shared by every component that shows the feed, it must
 * survive route changes, and it has a lifecycle (open, reconnect, close) that
 * has nothing to do with any particular render. nanostores' mount/disabled modes
 * express exactly that: the EventSource opens when the first component
 * subscribes and closes a second after the last one unsubscribes, with no
 * component owning the teardown.
 *
 * # Why it is not the datastar stream
 *
 * catlog already streams this data at `GET /v1/feed/sse`, and the
 * server-rendered site consumes it with no client code at all. That stream
 * carries *HTML* — datastar `PatchElements` frames addressed to DOM ids the Go
 * templates own — so a React client could only use it by scraping data back out
 * of another view's markup. `GET /v1/feed/stream` publishes the same rows as
 * JSON instead (server/internal/readapi/feed.go).
 */

/** How many rows the panel holds. Matches the server-rendered site's own panel. */
export const FEED_LIMIT = 30;

export interface FeedState {
  readonly rows: readonly FeedRow[];
  /**
   * `connecting` until the first snapshot lands, then `live` while the stream is
   * open, `offline` once it has dropped and is retrying, `error` if even the
   * snapshot could not be read.
   */
  readonly status: 'connecting' | 'live' | 'offline' | 'error';
}

const INITIAL: FeedState = { rows: [], status: 'connecting' };

/**
 * Merges new rows into the panel: newest first, deduped by id, capped.
 *
 * Pure, and exported so it can be tested without a socket. Dedup is by id
 * because feed ids are a monotonic INTEGER PRIMARY KEY (§5.4) — a row that has
 * been seen once can never legitimately arrive with different content.
 */
export function mergeFeed(
  existing: readonly FeedRow[],
  incoming: readonly FeedRow[],
): readonly FeedRow[] {
  const byId = new Map<number, FeedRow>();
  for (const row of [...incoming, ...existing]) {
    if (!byId.has(row.id)) byId.set(row.id, row);
  }
  return [...byId.values()].sort((a, b) => b.id - a.id).slice(0, FEED_LIMIT);
}

/**
 * Drops the leading handle from a feed summary.
 *
 * The server composes summaries as complete sentences — "demo_ace made orbit
 * around mun" — because the server-rendered panel shows nothing else. This panel
 * renders the handle as its own link, so the prefix would appear twice and eat
 * the width the actual news needs. The possessive form ("demo_ace's kitten …")
 * is left alone: removing it would leave a sentence with no subject.
 */
export function withoutHandle(summary: string, handle: string): string {
  const prefix = handle + ' ';
  return summary.startsWith(prefix) ? summary.slice(prefix.length) : summary;
}

export const $feed: ReadableAtom<FeedState> = (() => {
  const store = atom<FeedState>(INITIAL);

  onMount(store, () => {
    const controller = new AbortController();
    let source: EventSource | undefined;
    let live = true;

    /**
     * (Re)reads the snapshot.
     *
     * Called on mount and again whenever the stream (re)opens. The stream
     * publishes only new rows — it has no replay — so the snapshot is how a
     * reconnecting client recovers whatever it missed while disconnected. Two
     * calls can overlap; `mergeFeed` is idempotent by id, so that is harmless.
     */
    const snapshot = () => {
      getFeed(FEED_LIMIT, controller.signal).then(
        (res) => {
          if (!live) return;
          store.set({
            rows: mergeFeed(store.get().rows, res.rows),
            status: source === undefined ? 'offline' : store.get().status,
          });
        },
        () => {
          if (!live) return;
          // Only a failure with nothing on screen is worth reporting as an
          // error; otherwise the panel keeps showing what it has.
          if (store.get().rows.length === 0) store.set({ rows: [], status: 'error' });
        },
      );
    };

    snapshot();

    // No EventSource in a non-browser runtime (or a test that did not stub it):
    // the snapshot above still populated the panel, so degrade to static rather
    // than throw.
    if (typeof EventSource !== 'undefined') {
      source = new EventSource(feedStreamUrl());
      source.addEventListener('open', () => {
        if (!live) return;
        store.set({ rows: store.get().rows, status: 'live' });
        // A reconnect means rows were missed; re-read them.
        snapshot();
      });
      source.addEventListener('feed', (event: MessageEvent<string>) => {
        if (!live) return;
        let row: FeedRow;
        try {
          row = JSON.parse(event.data) as FeedRow;
        } catch {
          return; // a truncated frame; the next one will be fine
        }
        store.set({ rows: mergeFeed(store.get().rows, [row]), status: 'live' });
      });
      source.addEventListener('error', () => {
        if (!live) return;
        // EventSource reconnects on its own (the server sends `retry:`), so this
        // is a status change, not a teardown.
        store.set({ rows: store.get().rows, status: 'offline' });
      });
    }

    return () => {
      live = false;
      controller.abort();
      source?.close();
      store.set(INITIAL);
    };
  });

  return store;
})();
