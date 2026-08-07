import { cleanStores, keepMount } from 'nanostores';
import { describe, expect, it, vi } from 'vitest';
import type { FeedRow } from '../api/types.ts';
import { stubFetch } from '../test/http.ts';
import { $feed, FEED_LIMIT, mergeFeed, withoutHandle } from './feed.ts';

const row = (id: number, summary = `event ${String(id)}`): FeedRow => ({
  id,
  at: 1_800_000_000_000 + id,
  handle: 'demo_crasher',
  type: 'vehicle.impact',
  summary,
});

describe('mergeFeed', () => {
  it('keeps the newest first', () => {
    expect(mergeFeed([row(1)], [row(3), row(2)]).map((r) => r.id)).toEqual([3, 2, 1]);
  });

  it('dedupes by id, so a snapshot after a reconnect cannot double up', () => {
    const merged = mergeFeed([row(2), row(1)], [row(3), row(2), row(1)]);
    expect(merged.map((r) => r.id)).toEqual([3, 2, 1]);
  });

  it('prefers the incoming copy of a duplicate id', () => {
    const merged = mergeFeed([row(1, 'stale')], [row(1, 'fresh')]);
    expect(merged[0]?.summary).toBe('fresh');
  });

  it('caps the panel', () => {
    const many = Array.from({ length: FEED_LIMIT + 20 }, (_, i) => row(i));
    expect(mergeFeed([], many)).toHaveLength(FEED_LIMIT);
    expect(mergeFeed([], many)[0]?.id).toBe(FEED_LIMIT + 19);
  });
});

describe('withoutHandle', () => {
  it('drops the handle the panel already renders as a link', () => {
    expect(withoutHandle('demo_ace made orbit around mun', 'demo_ace')).toBe(
      'made orbit around mun',
    );
  });

  it('leaves the possessive form alone — stripping it would remove the subject', () => {
    const summary = "demo_tumbler's kitten Bramble took a tumble at 7.2 m/s on mun";
    expect(withoutHandle(summary, 'demo_tumbler')).toBe(summary);
  });

  it('leaves a summary that does not start with the handle', () => {
    expect(withoutHandle('a thing happened', 'demo_ace')).toBe('a thing happened');
  });
});

describe('$feed', () => {
  it('loads the snapshot when it is first subscribed to, and not before', async () => {
    const mock = stubFetch([{ path: '/v1/feed?limit=30', body: { limit: 30, rows: [row(9)] } }]);

    // Lazy: nothing is subscribed, so nothing has been fetched.
    expect(mock).not.toHaveBeenCalled();

    keepMount($feed);
    await vi.waitFor(() => {
      expect($feed.get().rows).toHaveLength(1);
    });
    expect($feed.get().rows[0]?.id).toBe(9);
    // EventSource is stubbed out in the test setup, so the store degrades to the
    // snapshot rather than throwing.
    expect($feed.get().status).toBe('offline');

    cleanStores($feed);
  });

  it('reports an error only when it has nothing to show', async () => {
    stubFetch([{ path: '/v1/feed', status: 500, body: { error: 'internal' } }]);
    keepMount($feed);
    await vi.waitFor(() => {
      expect($feed.get().status).toBe('error');
    });
    cleanStores($feed);
  });
});
