import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { BoardResponse, BoardsResponse, PlayerResponse } from '../api/types.ts';
import { setMe } from '../state/me.ts';
import { ALLTIME } from '../state/router.ts';
import { stubFetch } from '../test/http.ts';
import { pickFeatured } from '../ui/featured.ts';
import { BoardPage } from './BoardPage.tsx';
import { BoardsPage } from './BoardsPage.tsx';
import { PlayerPage } from './PlayerPage.tsx';

/**
 * Page-level rendering tests.
 *
 * They exercise the real data path — `useResource` → the real API client → a
 * stubbed `fetch` — rather than injecting fake props, because the interesting
 * failures are in the seam between the fetch and the render, not in the JSX.
 *
 * The fixtures are the seeded demo dataset (`POST /admin/seed`), so a test that
 * passes here describes something a human can go and look at.
 */

const PERIODS = [ALLTIME, 'daily', 'weekly', 'monthly', 'yearly'];

const BOARDS: BoardsResponse = {
  min_players: 2,
  boards: [
    {
      stat: 'biggest_lithobrake_survived',
      title: 'Biggest Lithobrake Survived',
      unit: 'm/s',
      ascending: false,
      count: 1,
      periods: PERIODS,
    },
    {
      stat: 'orbits_achieved',
      title: 'Orbits Achieved',
      unit: 'orbits',
      ascending: false,
      count: 1,
      periods: PERIODS,
    },
    {
      stat: 'dockings',
      title: 'Dockings',
      unit: 'dockings',
      ascending: false,
      count: 0,
      periods: PERIODS,
    },
    // A board whose key came out of the event stream. Nothing in the SPA names
    // this place; it is here because players went there.
    {
      stat: 'fastest_to_zephyria',
      title: 'Fastest to Zephyria',
      unit: 's',
      ascending: true,
      count: 2,
      periods: PERIODS,
    },
  ],
};

const LITHOBRAKE: BoardResponse = {
  stat: 'biggest_lithobrake_survived',
  title: 'Biggest Lithobrake Survived',
  unit: 'm/s',
  ascending: false,
  period: ALLTIME,
  limit: 50,
  offset: 0,
  rows: [
    {
      rank: 1,
      handle: 'demo_crasher',
      value: 214,
      context: { body: 'kerbin', flight: '01J9VZZZ', energy_j: 48_000_000 },
      updated: 1_800_000_000_000,
    },
    { rank: 2, handle: 'demo_tumbler', value: 88, updated: 1_800_000_000_000 },
  ],
};

describe('BoardsPage', () => {
  it('renders every board the API lists, empty ones included', async () => {
    stubFetch([{ path: '/v1/leaderboards', body: BOARDS }]);
    render(<BoardsPage />);

    expect(await screen.findByText('Biggest Lithobrake Survived')).toBeTruthy();
    expect(screen.getByText('Orbits Achieved')).toBeTruthy();
    // A board nobody is on is still a board (§4.8).
    expect(screen.getByText('Dockings')).toBeTruthy();
    expect(screen.getByText('empty')).toBeTruthy();

    // Each one links to its own board route — a real path, so it survives being
    // copied out of the address bar and pasted somewhere else.
    const link = screen.getByRole('link', { name: /Biggest Lithobrake Survived/ });
    expect(link.getAttribute('href')).toBe('/boards/biggest_lithobrake_survived');
  });

  it('keeps the stat key out of the table and in the markup', async () => {
    stubFetch([{ path: '/v1/leaderboards', body: BOARDS }]);
    const { container } = render(<BoardsPage />);
    await screen.findByText('Biggest Lithobrake Survived');

    // The title says it better than `rud_total` does. The key is still in the
    // URL and in `data-stat`, where a test or a curious reader can find it.
    expect(screen.queryByText('biggest_lithobrake_survived')).toBeNull();
    expect(container.querySelector('tr.boards-row[data-stat="orbits_achieved"]')).toBeTruthy();
  });

  it('renders a board whose key came out of the event stream, and says why one may be missing', async () => {
    stubFetch([{ path: '/v1/leaderboards', body: BOARDS }]);
    render(<BoardsPage />);

    // No constant in this app names Zephyria: the index is the server's answer,
    // not a list here, and the page must not assume its size or membership.
    expect(await screen.findByText('Fastest to Zephyria')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Fastest to Zephyria/ }).getAttribute('href')).toBe(
      '/boards/fastest_to_zephyria',
    );
    // The threshold is explained with the server's number, not one hard-coded.
    expect(screen.getByText(/at least 2 different players/)).toBeTruthy();
  });

  it('shows the server error rather than an empty list when the read fails', async () => {
    stubFetch([
      { path: '/v1/leaderboards', status: 500, body: { error: 'internal', detail: 'boom' } },
    ]);
    render(<BoardsPage />);

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Could not load the board list');
    expect(alert.textContent).toContain('internal');
  });
});

describe('BoardPage', () => {
  const boardRoutes = (board: BoardResponse) => [
    { path: `/v1/leaderboards/${board.stat}`, body: board },
    { path: '/v1/leaderboards', body: BOARDS },
  ];

  it('renders the rows in rank order with their values', async () => {
    stubFetch(boardRoutes(LITHOBRAKE));
    render(<BoardPage stat="biggest_lithobrake_survived" offset={0} period={ALLTIME} />);

    expect(await screen.findByRole('link', { name: 'demo_crasher' })).toBeTruthy();

    const rows = screen.getAllByRole('row');
    // One header row, then the data rows in the order the API sent them.
    expect(rows).toHaveLength(3);
    expect(rows[1]?.textContent).toContain('1');
    expect(rows[1]?.textContent).toContain('demo_crasher');
    expect(rows[1]?.textContent).toContain('214 m/s');
    expect(rows[2]?.textContent).toContain('demo_tumbler');
    expect(rows[2]?.textContent).toContain('88 m/s');

    // The handle links to the profile route.
    expect(screen.getByRole('link', { name: 'demo_crasher' }).getAttribute('href')).toBe(
      '/p/demo_crasher',
    );
  });

  it('carries the exact float on every value cell', async () => {
    // Not decoration: a test that reconstructed the number by stripping
    // non-digits out of the rendered text would break the moment a career-time
    // board rendered `5m 13s`. The smoke script reads this attribute.
    stubFetch(boardRoutes(LITHOBRAKE));
    const { container } = render(
      <BoardPage stat="biggest_lithobrake_survived" offset={0} period={ALLTIME} />,
    );
    await screen.findByRole('link', { name: 'demo_crasher' });

    const cell = container.querySelector('tr.board-row[data-rank="1"] td.value');
    expect(cell?.getAttribute('data-value')).toBe('214');
  });

  it('hides the flight id and keeps it one disclosure away', async () => {
    stubFetch(boardRoutes(LITHOBRAKE));
    const { container } = render(
      <BoardPage stat="biggest_lithobrake_survived" offset={0} period={ALLTIME} />,
    );
    await screen.findByRole('link', { name: 'demo_crasher' });

    // `flight` is a client-minted ULID: meaningless to a reader and the widest
    // column in the table. Off the allow-list, so it is not in the visible row…
    const pairs = container.querySelector('tr.board-row[data-rank="1"] .context-pairs');
    expect(pairs?.textContent).not.toContain('01J9VZZZ');
    // …but the row's Details disclosure still offers the blob as the API sent
    // it, which is safe precisely because what the API sent is already
    // post-redaction.
    expect(screen.getAllByRole('button', { name: 'Details' }).length).toBeGreaterThan(0);
    // The allow-listed keys are rendered, through the unit renderer.
    expect(pairs?.textContent).toContain('Kerbin');
    expect(pairs?.textContent).toContain('48 MJ');
  });

  it('pages with links, not click handlers, so a page of a board can be opened in a tab', async () => {
    stubFetch(
      boardRoutes({
        ...LITHOBRAKE,
        // A full page (limit rows returned) is what makes "next" available.
        limit: 2,
        offset: 50,
      }),
    );
    render(<BoardPage stat="biggest_lithobrake_survived" offset={50} period={ALLTIME} />);
    await screen.findByRole('link', { name: 'demo_crasher' });

    expect(screen.getByRole('link', { name: /Previous/ }).getAttribute('href')).toBe(
      '/boards/biggest_lithobrake_survived',
    );
    expect(screen.getByRole('link', { name: /Next/ }).getAttribute('href')).toBe(
      '/boards/biggest_lithobrake_survived?offset=100',
    );
  });

  it('offers the windows the server publishes, as links', async () => {
    stubFetch(boardRoutes(LITHOBRAKE));
    render(<BoardPage stat="biggest_lithobrake_survived" offset={0} period={ALLTIME} />);
    await screen.findByRole('link', { name: 'demo_crasher' });

    // A period is a place, so each tab is a real link: `?period=weekly` is what
    // makes "how did this week go" something you can send somebody.
    const weekly = screen.getByRole('tab', { name: 'weekly' });
    expect(weekly.getAttribute('href')).toBe('/boards/biggest_lithobrake_survived?period=weekly');
    // `alltime` is the default and stays out of the URL.
    expect(screen.getByRole('tab', { name: 'all time' }).getAttribute('href')).toBe(
      '/boards/biggest_lithobrake_survived',
    );
  });

  it('asks the server for the window it is showing, and names the bucket', async () => {
    stubFetch(boardRoutes({ ...LITHOBRAKE, period: 'weekly', bucket: '2026-W32' }));
    const mock = stubFetch([
      { path: 'period=weekly', body: { ...LITHOBRAKE, period: 'weekly', bucket: '2026-W32' } },
      { path: '/v1/leaderboards', body: BOARDS },
    ]);
    render(<BoardPage stat="biggest_lithobrake_survived" offset={0} period="weekly" />);

    expect(await screen.findByText('2026-W32')).toBeTruthy();
    expect(mock.mock.calls.some((c) => String(c[0]).includes('period=weekly'))).toBe(true);
  });

  it('says the board is empty rather than showing a broken table', async () => {
    stubFetch([
      {
        path: '/v1/leaderboards/dockings',
        body: { ...LITHOBRAKE, stat: 'dockings', title: 'Dockings', rows: [] },
      },
      { path: '/v1/leaderboards', body: BOARDS },
    ]);
    render(<BoardPage stat="dockings" offset={0} period={ALLTIME} />);

    expect(await screen.findByText('Nobody is on this board yet.')).toBeTruthy();
  });

  it('asks the server for the requested page', async () => {
    const mock = stubFetch([
      { path: 'offset=50', body: { ...LITHOBRAKE, offset: 50, rows: [] } },
      { path: '/v1/leaderboards', body: BOARDS },
    ]);
    render(<BoardPage stat="biggest_lithobrake_survived" offset={50} period={ALLTIME} />);

    await screen.findByText(/nothing on this page/);
    expect(String(mock.mock.calls[0]?.[0])).toContain('offset=50');
  });

  // A career-time board ranks the smallest value first. The server publishes
  // which way each board reads; presenting a "fastest to" board as though a
  // bigger number were better is a wrong answer, not a styling gap.
  it('says which way the board reads, from the server and not from the key', async () => {
    stubFetch([
      {
        path: '/v1/leaderboards/fastest_to_zephyria',
        body: {
          stat: 'fastest_to_zephyria',
          title: 'Fastest to Zephyria',
          unit: 's',
          ascending: true,
          period: ALLTIME,
          limit: 50,
          offset: 0,
          rows: [
            { rank: 1, handle: 'demo_ace', value: 50, updated: 1_800_000_000_000 },
            {
              rank: 2,
              handle: 'demo_crasher',
              value: 537.5,
              updated: 1_800_000_000_000,
              rewound: true,
            },
          ],
        },
      },
      { path: '/v1/leaderboards', body: BOARDS },
    ]);
    render(<BoardPage stat="fastest_to_zephyria" offset={0} period={ALLTIME} />);

    expect(await screen.findByRole('link', { name: 'demo_ace' })).toBeTruthy();
    expect(screen.getByText(/Lowest wins\./)).toBeTruthy();
    // Seconds are a duration, not a bare number: 537.5 s is 8m 57s.
    expect(screen.getByText('8m 57s')).toBeTruthy();
    expect(screen.getByText('50 s')).toBeTruthy();
    // And the rewind qualifier reaches the row it qualifies.
    expect(screen.getByText(/career rewound/)).toBeTruthy();
  });

  it('marks the viewer’s own row', async () => {
    setMe('demo_tumbler');
    stubFetch(boardRoutes(LITHOBRAKE));
    const { container } = render(
      <BoardPage stat="biggest_lithobrake_survived" offset={0} period={ALLTIME} />,
    );
    await screen.findByRole('link', { name: 'demo_crasher' });

    const mine = container.querySelector('tr.board-row[data-handle="demo_tumbler"]');
    expect(mine?.className).toContain('bg-wash-selected');
    expect(
      container.querySelector('tr.board-row[data-handle="demo_crasher"]')?.className,
    ).not.toContain('bg-wash-selected');
  });
});

describe('PlayerPage', () => {
  const player: PlayerResponse = {
    handle: 'demo_ace',
    since: 1_700_000_000_000,
    stats: [
      {
        stat: 'orbits_achieved',
        title: 'Orbits Achieved',
        unit: 'orbits',
        value: 2,
        ascending: false,
        rank: 3,
        players: 41,
        updated: 1_800_000_000_000,
      },
      {
        stat: 'fastest_to_orbit',
        title: 'Fastest to Orbit',
        unit: 's',
        value: 37.5,
        ascending: true,
        rank: 120,
        players: 400,
        updated: 1_800_000_000_000,
      },
    ],
  };

  it('renders a profile with each placement, its rank and its denominator', async () => {
    stubFetch([{ path: '/v1/players/demo_ace', body: player }]);
    render(<PlayerPage handle="demo_ace" />);

    expect(await screen.findByRole('heading', { name: 'demo_ace' })).toBeTruthy();
    // React Aria builds a table's collection in a second pass, so the rows are
    // awaited rather than queried straight after the heading.
    expect(await screen.findByText('Orbits Achieved')).toBeTruthy();
    // `#3` on its own says nothing: third of four is not third of four thousand.
    expect(screen.getByText('#3')).toBeTruthy();
    expect(screen.getByText('of 41')).toBeTruthy();
    expect(screen.getByText('2 orbits')).toBeTruthy();
    // Seconds are a duration on a profile too.
    expect(screen.getByText('37.5 s')).toBeTruthy();
    // Direction is published per row, so a "fastest to" board is not presented
    // backwards.
    expect(screen.getAllByText('Lowest wins.').length).toBe(1);
  });

  it('links each placement to the page of the board containing that rank', async () => {
    stubFetch([{ path: '/v1/players/demo_ace', body: player }]);
    render(<PlayerPage handle="demo_ace" />);
    await screen.findByText('Fastest to Orbit');

    // rank 120 with a 50-row page is offset 100 — arithmetic, not an endpoint.
    expect(screen.getByRole('link', { name: 'Fastest to Orbit' }).getAttribute('href')).toBe(
      '/boards/fastest_to_orbit?offset=100',
    );
    expect(screen.getByRole('link', { name: 'Orbits Achieved' }).getAttribute('href')).toBe(
      '/boards/orbits_achieved',
    );
  });

  it('offers the raw event log and a comparison seeded with this handle', async () => {
    stubFetch([{ path: '/v1/players/demo_ace', body: player }]);
    render(<PlayerPage handle="demo_ace" />);
    await screen.findByRole('heading', { name: 'demo_ace' });

    expect(screen.getByRole('link', { name: /Raw events/ }).getAttribute('href')).toBe(
      '/p/demo_ace/events',
    );
    expect(screen.getByRole('link', { name: /Compare/ }).getAttribute('href')).toBe(
      '/compare?handles=demo_ace',
    );
  });

  it('renders the not-found state for a 404, without guessing why', async () => {
    // §4.8 answers 404 identically for unknown, retired and banned handles. The
    // UI must not imply which one this is.
    stubFetch([
      {
        path: '/v1/players/nobody',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);
    render(<PlayerPage handle="nobody" />);

    expect(await screen.findByRole('heading', { name: 'Nothing here' })).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    for (const leak of [/banned/i, /retired/i, /suspend/i]) {
      expect(screen.queryByText(leak)).toBeNull();
    }
  });

  it('shows a real failure as an error, not as a missing player', async () => {
    stubFetch([
      { path: '/v1/players/demo_ace', status: 500, body: { error: 'internal', detail: 'boom' } },
    ]);
    render(<PlayerPage handle="demo_ace" />);

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Could not load this profile');
    expect(screen.queryByText('Nothing here')).toBeNull();
  });
});

describe('pickFeatured', () => {
  // The front page's choice of previews is a preference, not a contract. The
  // server-rendered site makes the same choice independently; the two are
  // separate applications sharing an HTTP contract and nothing else. What keeps
  // this from rotting is that the preference is filtered against what the server
  // actually publishes.
  it('prefers the named boards, in their stated order', () => {
    expect(pickFeatured(['dockings', 'rud_total', 'biggest_lithobrake_survived'])).toEqual([
      'biggest_lithobrake_survived',
      'rud_total',
      'dockings',
    ]);
  });

  it('falls back to whatever the server does publish', () => {
    expect(pickFeatured(['fastest_to_luna', 'soi_bodies', 'stagings', 'dockings'])).toEqual([
      'fastest_to_luna',
      'soi_bodies',
      'stagings',
    ]);
  });

  it('never asks for a board the server did not list', () => {
    expect(pickFeatured([])).toEqual([]);
    expect(pickFeatured(['rud_total'])).toEqual(['rud_total']);
  });
});
