import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { StatsResponse } from '../api/types.ts';
import { stubFetch } from '../test/http.ts';
import { StatsPage } from './StatsPage.tsx';

/**
 * The stats-of-stats screen.
 *
 * Same shape as the other page tests: the real data path through a stubbed
 * `fetch`, because the interesting failures live in the seam between the JSON
 * and the render.
 */

const STATS: StatsResponse = {
  generated: 1_800_000_500_000,
  events: {
    total: 1234567,
    types: [
      {
        type: 'telemetry.window',
        count: 1000000,
        share: 0.81,
        first: 1_700_000_000_000,
        last: 1_800_000_000_000,
      },
      { type: 'vehicle.rud', count: 234567, share: 0.19, first: 1_700_000_000_000 },
    ],
    windows: [
      {
        period: 'daily',
        bucket: '2026-08-08',
        count: 4200,
        types: [{ type: 'telemetry.window', count: 4000, share: 0.952 }],
      },
      { period: 'weekly', bucket: '2026-W32', count: 31000, types: [] },
      { period: 'monthly', bucket: '2026-08', count: 120000, types: [] },
      { period: 'yearly', bucket: '2026', count: 900000, types: [] },
    ],
    first: 1_700_000_000_000,
    last: 1_800_000_000_000,
    days: 250,
    per_day: 4938.268,
    busiest: { period: 'daily', bucket: '2026-03-02', count: 98765 },
    daily: [
      { period: 'daily', bucket: '2026-08-07', count: 3100 },
      { period: 'daily', bucket: '2026-08-08', count: 4200 },
    ],
  },
  collection: {
    boards: 27,
    placements: 812,
    types: 2,
    handles: 41,
    scoring_players: 38,
    flights: 5123,
    flagged_flights: 12,
    careers: 88,
    rewound_careers: 3,
    kittens: 240,
    bodies: 9,
    feed_rows: 500,
    log_head: 1234570,
    projected: 1234567,
    lag: 3,
  },
};

describe('the stats page', () => {
  it('reads the whole page from one request', async () => {
    const fetchMock = stubFetch([{ path: '/v1/stats', body: STATS }]);
    render(<StatsPage />);
    // The server assembles and memoises the whole answer, so composing this
    // page out of several reads would buy nothing and could show two halves of
    // two different views of the database.
    await screen.findByText('Events logged');
    // Two cells carry it: the headline total, and the projector's cursor —
    // which is the point of publishing both.
    expect(screen.getAllByText('1,234,567')).toHaveLength(2);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('shows every rolling window, named and dated', async () => {
    stubFetch([{ path: '/v1/stats', body: STATS }]);
    render(<StatsPage />);
    await screen.findByText('Events logged');

    // Labelled for a reader — the API's key is a duration ("weekly"), what a
    // reader wants is the window.
    for (const label of ['Today', 'This week', 'This month', 'This year']) {
      expect(screen.getByText(label)).toBeTruthy();
    }
    // And the bucket beside it, because "today" is the *server's* UTC day and
    // printing which one is the only honest way to say so.
    expect(screen.getByText('2026-08-08')).toBeTruthy();
    expect(screen.getByText('2026-W32')).toBeTruthy();
  });

  it('states the projector lag rather than pretending the page is live', async () => {
    stubFetch([{ path: '/v1/stats', body: STATS }]);
    render(<StatsPage />);
    await screen.findByText('Events logged');

    // Everything on this page is a projection. The gap between the log head and
    // the cursor is why a figure here can disagree with one on a board page,
    // and this row is where a reader finds that out.
    expect(screen.getByText('Projector lag')).toBeTruthy();
    expect(screen.getByText('1,234,570')).toBeTruthy();
  });

  it('is about the collection, so nobody is named on it', async () => {
    stubFetch([{ path: '/v1/stats', body: STATS }]);
    render(<StatsPage />);
    await screen.findByText('Events logged');
    expect(screen.queryByText(/whiskers/i)).toBeNull();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('shows the server error rather than a page of zeroes', async () => {
    stubFetch([
      { path: '/v1/stats', status: 500, body: { error: 'internal', detail: 'could not count' } },
    ]);
    render(<StatsPage />);
    // §9.3: the server's own detail, not a friendlier replacement — when this is
    // a CORS refusal or a stopped server, that string is the whole diagnosis.
    expect(await screen.findByText(/could not count/)).toBeTruthy();
    expect(screen.queryByText('Events logged')).toBeNull();
  });

  it('renders an empty collection without inventing one', async () => {
    stubFetch([
      {
        path: '/v1/stats',
        body: {
          generated: 1_800_000_500_000,
          events: { total: 0, types: [], windows: [], days: 0, per_day: 0, daily: [] },
          collection: { ...STATS.collection, boards: 0, placements: 0, types: 0, handles: 0 },
        } satisfies StatsResponse,
      },
    ]);
    render(<StatsPage />);
    // The morning of launch: zeroes where there are zeroes, an em dash where
    // there is no answer at all, and no chart for a series with nothing in it.
    expect(await screen.findByText('Events logged')).toBeTruthy();
    expect(screen.getByText('No day has had one yet.')).toBeTruthy();
    expect(screen.queryByText('Events per day', { selector: 'h2' })).toBeNull();
  });
});
