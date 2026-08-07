import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { BoardResponse, BoardsResponse, PlayerResponse } from '../api/types.ts';
import { stubFetch } from '../test/http.ts';
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

const BOARDS: BoardsResponse = {
  boards: [
    {
      stat: 'biggest_lithobrake_survived',
      title: 'Biggest Lithobrake Survived',
      unit: 'm/s',
      count: 1,
    },
    { stat: 'orbits_achieved', title: 'Orbits Achieved', unit: 'orbits', count: 1 },
    { stat: 'dockings', title: 'Dockings', unit: 'dockings', count: 0 },
  ],
};

const LITHOBRAKE: BoardResponse = {
  stat: 'biggest_lithobrake_survived',
  title: 'Biggest Lithobrake Survived',
  unit: 'm/s',
  limit: 50,
  offset: 0,
  rows: [
    {
      rank: 1,
      handle: 'demo_crasher',
      value: 214,
      context: { body: 'kerbin' },
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

    // Each one links to its own board route.
    const link = screen.getByRole('link', { name: /Biggest Lithobrake Survived/ });
    expect(link.getAttribute('href')).toBe('#/boards/biggest_lithobrake_survived');
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
  it('renders the rows in rank order with their values', async () => {
    stubFetch([{ path: '/v1/leaderboards/biggest_lithobrake_survived', body: LITHOBRAKE }]);
    render(<BoardPage stat="biggest_lithobrake_survived" offset={0} />);

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
      '#/p/demo_crasher',
    );
  });

  it('says the board is empty rather than showing a broken table', async () => {
    stubFetch([
      {
        path: '/v1/leaderboards/dockings',
        body: { ...LITHOBRAKE, stat: 'dockings', title: 'Dockings', rows: [] },
      },
    ]);
    render(<BoardPage stat="dockings" offset={0} />);

    expect(await screen.findByText('Nobody is on this board yet.')).toBeTruthy();
  });

  it('asks the server for the requested page', async () => {
    const mock = stubFetch([{ path: 'offset=50', body: { ...LITHOBRAKE, offset: 50, rows: [] } }]);
    render(<BoardPage stat="biggest_lithobrake_survived" offset={50} />);

    await screen.findByText(/nothing on this page/);
    expect(String(mock.mock.calls[0]?.[0])).toContain('offset=50');
  });
});

describe('PlayerPage', () => {
  it('renders a profile with each placement and its rank', async () => {
    const player: PlayerResponse = {
      handle: 'demo_ace',
      since: 1_700_000_000_000,
      stats: [
        {
          stat: 'orbits_achieved',
          title: 'Orbits Achieved',
          unit: 'orbits',
          value: 2,
          rank: 1,
          updated: 1_800_000_000_000,
        },
      ],
    };
    stubFetch([{ path: '/v1/players/demo_ace', body: player }]);
    render(<PlayerPage handle="demo_ace" />);

    expect(await screen.findByRole('heading', { name: 'demo_ace' })).toBeTruthy();
    expect(screen.getByText('Orbits Achieved')).toBeTruthy();
    expect(screen.getByText('rank #1')).toBeTruthy();
    expect(screen.getByText('2 orbits')).toBeTruthy();
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

    expect(await screen.findByRole('heading', { name: 'No such player' })).toBeTruthy();
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
    expect(screen.queryByText('No such player')).toBeNull();
  });
});
