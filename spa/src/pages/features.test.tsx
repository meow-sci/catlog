import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { CompareResponse, EventsResponse, PlayerResponse } from '../api/types.ts';
import { $me, ME_KEY, setMe } from '../state/me.ts';
import { stubFetch } from '../test/http.ts';
import { standing } from '../ui/kit/index.ts';
import { YourStanding } from '../ui/YourStanding.tsx';
import { ComparePage } from './ComparePage.tsx';
import { PlayerEventsPage } from './PlayerEventsPage.tsx';
import { PlayerPage } from './PlayerPage.tsx';
import { SearchPage } from './SearchPage.tsx';

/**
 * The features this redesign added: search, comparison, the raw event log, and
 * the "me" handle.
 *
 * Every assertion here is a *rule* from the API rather than a preference — the
 * two-character search floor, `found: false` as a column, absent ≠ zero, and
 * paging until the cursor is gone. They are the rules the two frontends have to
 * agree on, and each one is invisible until it is wrong.
 */

describe('handle search', () => {
  it('sends no request below the server’s two-character floor', async () => {
    // `MinQueryLen` is 2 and a shorter query is a **400**, not an empty 200. A
    // box that fired on the first keystroke would therefore error on every
    // single search. The right fix is not to render the error — it is not to
    // send the request.
    const mock = stubFetch([{ path: '/v1/players', body: { query: 'x', limit: 50, handles: [] } }]);
    const user = userEvent.setup();
    render(<SearchPage q="" />);

    await user.type(screen.getByRole('searchbox'), 'w');
    expect(await screen.findByText(/shortest search catlog will run/)).toBeTruthy();
    expect(mock).not.toHaveBeenCalled();
  });

  it('says handles "match", because the endpoint is prefix-then-substring', async () => {
    stubFetch([{ path: '/v1/players?q=zz', body: { query: 'zz', limit: 50, handles: [] } }]);
    render(<SearchPage q="zz" />);

    // Not "no handles start with": a hit can be anywhere in a handle, so an
    // empty state that promised prefixes would be describing a different API.
    expect(await screen.findByText('No handles match zz.')).toBeTruthy();
  });

  it('renders matches as profile links and builds a comparison from the ticked ones', async () => {
    stubFetch([
      {
        path: '/v1/players?q=demo',
        body: {
          query: 'demo',
          limit: 50,
          handles: ['demo_ace', 'demo_crasher', 'demo_tumbler'],
          truncated: true,
        },
      },
    ]);
    const user = userEvent.setup();
    render(<SearchPage q="demo" />);

    expect(await screen.findByRole('link', { name: 'demo_ace' })).toBeTruthy();
    // `truncated` means narrow the query, not load more: there is no offset on
    // this endpoint, and there is deliberately no pager here.
    expect(screen.getByText('More handles match. Try a longer query.')).toBeTruthy();
    expect(screen.queryByRole('link', { name: /Next/ })).toBeNull();

    await user.click(screen.getByRole('checkbox', { name: 'Compare demo_ace' }));
    await user.click(screen.getByRole('checkbox', { name: 'Compare demo_tumbler' }));

    expect(screen.getByRole('link', { name: /Compare these/ }).getAttribute('href')).toBe(
      '/compare?handles=demo_ace,demo_tumbler',
    );
  });
});

describe('comparison', () => {
  const COMPARE: CompareResponse = {
    handles: [
      { handle: 'demo_ace', found: true, since: 1_700_000_000_000 },
      { handle: 'demo_crasher', found: true, since: 1_700_000_000_000 },
      { handle: 'ghost', found: false },
    ],
    boards: [
      {
        stat: 'biggest_lithobrake_survived',
        title: 'Biggest Lithobrake Survived',
        unit: 'm/s',
        ascending: false,
        players: 41,
        rows: [
          {
            handle: 'demo_crasher',
            value: 214,
            rank: 1,
            updated: 1_800_000_000_000,
          },
        ],
      },
      {
        stat: 'fastest_to_orbit',
        title: 'Fastest to Orbit',
        unit: 's',
        ascending: true,
        players: 400,
        rows: [
          { handle: 'demo_ace', value: 313, rank: 7, updated: 1_800_000_000_000 },
          { handle: 'demo_crasher', value: 3661, rank: 200, updated: 1_800_000_000_000 },
        ],
      },
    ],
  };

  const render3 = () => {
    stubFetch([{ path: '/v1/compare', body: COMPARE }]);
    return render(<ComparePage handles={['demo_ace', 'demo_crasher', 'ghost']} />);
  };

  it('gives an unknown handle a column rather than dropping it', async () => {
    render3();
    // Silently dropping it lets a typo look like a defeat — and it reveals no
    // more than asking for that one profile already does, since unknown,
    // retired and banned are one answer everywhere.
    expect(await screen.findByText('no such player')).toBeTruthy();
    expect(screen.getAllByText('ghost').length).toBeGreaterThan(0);
  });

  it('renders an absent player as absent, not as zero', async () => {
    const { container } = render3();
    await screen.findByRole('link', { name: 'Biggest Lithobrake Survived' });

    const row = container.querySelector('tr[data-stat="biggest_lithobrake_survived"]');
    // demo_ace is not on this board. `—`, with the reason on hover — never `0`,
    // which is a claim the folds never made.
    expect(row?.textContent).toContain('—');
    expect(row?.querySelector('[title="not on this board"]')).toBeTruthy();
    expect(row?.textContent).not.toContain('0 m/s');
  });

  it('picks the best cell from `ascending`, never by inference', async () => {
    const { container } = render3();
    await screen.findByRole('link', { name: 'Fastest to Orbit' });

    // Lowest wins on a career-time board: 313 s beats 3661 s, so the *smaller*
    // number is the marked one. A table that assumed bigger-is-better would mark
    // the wrong player and look completely plausible doing it.
    const row = container.querySelector('tr[data-stat="fastest_to_orbit"]');
    const best = row?.querySelector('.text-accent-text');
    expect(best?.textContent).toContain('5m 13s');
    expect(row?.textContent).toContain('1h 01m');
  });

  it('labels the rank as the world rank, not the rank among these handles', async () => {
    render3();
    await screen.findByRole('link', { name: 'Fastest to Orbit' });
    // "3rd in the world", not "2nd of your friends".
    expect(screen.getByText('#7 in the world')).toBeTruthy();
    expect(screen.getByText('#200 in the world')).toBeTruthy();
  });

  it('asks for an empty comparison rather than nothing when nobody is picked', async () => {
    const mock = stubFetch([
      { path: '/v1/compare', body: { handles: [], boards: [] } as CompareResponse },
    ]);
    render(<ComparePage handles={[]} />);

    expect(await screen.findByText(/Nobody to compare yet/)).toBeTruthy();
    // An empty list is a valid, empty comparison — the endpoint says so — which
    // is exactly what a picker with nobody in it should request.
    await waitFor(() => {
      expect(mock).toHaveBeenCalled();
    });
  });
});

describe('the raw event log', () => {
  const page1: EventsResponse = {
    handle: 'demo_ace',
    limit: 50,
    // **A short page with a cursor is not the end of the log.** One event and a
    // `next` is exactly what a filtered page that hit the server's scan bound
    // looks like.
    next: '41822',
    events: [
      {
        seq: 41823,
        id: '01J9VA',
        type: 'vehicle.impact',
        ver: 1,
        session: '01J9VS',
        flight: '01J9VF',
        career: 'b7k2q9x4m0nrt3vz',
        sim_t: 1832.5,
        recv: 1_800_000_000_000,
        payload: { speed_ms: 7799, energy_j: 48_000_000, survived: true, unheard_of_key: 'kept' },
      },
    ],
  };

  const page2: EventsResponse = {
    handle: 'demo_ace',
    limit: 50,
    events: [
      {
        seq: 41822,
        id: '01J9VB',
        type: 'session.started',
        ver: 1,
        session: '01J9VS',
        recv: 1_799_000_000_000,
        payload: { mod_ver: '0.1.0' },
      },
    ],
  };

  it('shows raw numbers with the formatted reading beside them — the inverse of a board', async () => {
    stubFetch([{ path: '/v1/players/demo_ace/events', body: page1 }]);
    const user = userEvent.setup();
    render(<PlayerEventsPage handle="demo_ace" type="" />);

    await screen.findByText('vehicle.impact');
    await user.click(screen.getByRole('button', { name: 'Payload' }));

    // This is the view where a reader wants `7799`, not `7 799 m/s`.
    expect(screen.getByText('7799')).toBeTruthy();
    expect(screen.getByText('(7 799 m/s)')).toBeTruthy();
    // A key this build has never heard of is still rendered: catlog preserves
    // them, and a raw view that dropped them would be lying about what it
    // recorded.
    expect(screen.getByText('unheard_of_key')).toBeTruthy();
  });

  it('renders sim_t as a duration, because that is what a career clock is', async () => {
    stubFetch([{ path: '/v1/players/demo_ace/events', body: page1 }]);
    render(<PlayerEventsPage handle="demo_ace" type="" />);
    // 1832.5 seconds into the career.
    expect(await screen.findByText('30m 32s')).toBeTruthy();
  });

  it('keeps paging while a cursor is present, even after a short page', async () => {
    const mock = stubFetch([
      { path: 'before=41822', body: page2 },
      { path: '/v1/players/demo_ace/events', body: page1 },
    ]);
    const user = userEvent.setup();
    render(<PlayerEventsPage handle="demo_ace" type="" />);

    await screen.findByText('vehicle.impact');
    // One event and a cursor: a client that stopped on a short page would
    // silently truncate somebody's history.
    const more = screen.getByRole('button', { name: 'Load older events' });
    await user.click(more);

    // `getAllBy`: the type filter's own option list names every type too.
    expect((await screen.findAllByText('session.started')).length).toBeGreaterThan(0);
    expect(mock.mock.calls.some((c) => String(c[0]).includes('before=41822'))).toBe(true);
    // The second page has no cursor, so now it really is the end.
    expect(await screen.findByText('That is the whole log.')).toBeTruthy();
  });

  it('never shows a hashed user identifier, whatever the payload carries', async () => {
    stubFetch([{ path: '/v1/players/demo_ace/events', body: page1 }]);
    const { container } = render(<PlayerEventsPage handle="demo_ace" type="" />);
    await screen.findByText('vehicle.impact');

    // Redaction is the server's job and cannot be done in a frontend — but the
    // standing rule is worth a tripwire on the side that renders.
    for (const forbidden of ['user_key', 'install', 'wall_t']) {
      expect(container.innerHTML).not.toContain(forbidden);
    }
  });

  it('answers a 404 the same way every other surface does', async () => {
    stubFetch([
      {
        path: '/v1/players/nobody/events',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);
    render(<PlayerEventsPage handle="nobody" type="" />);

    expect(await screen.findByRole('heading', { name: 'Nothing here' })).toBeTruthy();
    for (const leak of [/banned/i, /retired/i]) {
      expect(screen.queryByText(leak)).toBeNull();
    }
  });
});

describe('the "me" handle', () => {
  const PLAYER: PlayerResponse = {
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
    ],
  };

  it('survives a reload, because it is one plain localStorage key', async () => {
    stubFetch([{ path: '/v1/players/demo_ace', body: PLAYER }]);
    const user = userEvent.setup();
    render(<PlayerPage handle="demo_ace" />);

    await user.click(await screen.findByRole('button', { name: 'This is me' }));

    // One key, no JSON envelope, the handle in display casing — so a curious
    // user can read it and clear it.
    expect(window.localStorage.getItem(ME_KEY)).toBe('demo_ace');
    expect($me.get()).toBe('demo_ace');
    expect(screen.getByRole('button', { name: 'This is you' })).toBeTruthy();
  });

  it('is never sent to catlog as an identifier', async () => {
    setMe('demo_ace');
    const mock = stubFetch([{ path: '/v1/players/demo_ace', body: PLAYER }]);
    render(<YourStanding />);
    await screen.findByText('Orbits Achieved');

    // The handle appears in the *path* of the profile it asks for, and nowhere
    // else: not as a query parameter on a cached URL, not as a header, not as a
    // credential. `credentials: 'omit'` is load-bearing and stays.
    for (const call of mock.mock.calls) {
      const init = call[1] as RequestInit | undefined;
      expect(init?.credentials).toBe('omit');
      expect(String(call[0])).not.toContain('?');
    }
  });

  it('repeats the API’s silence for a handle that no longer resolves, and never auto-clears', async () => {
    setMe('demo_ace');
    stubFetch([
      {
        path: '/v1/players/demo_ace',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);
    const user = userEvent.setup();
    render(<YourStanding />);

    // Not banned, not deleted, not retired, not renamed. The API answers 404 for
    // all of those identically on purpose.
    expect(await screen.findByText(/no public profile for/)).toBeTruthy();
    for (const leak of [/banned/i, /deleted/i, /retired/i]) {
      expect(screen.queryByText(leak)).toBeNull();
    }
    // The stored value is the user's data: a 404 during an incident or a
    // reversed moderation action must not silently erase it.
    expect(window.localStorage.getItem(ME_KEY)).toBe('demo_ace');

    await user.click(screen.getByRole('button', { name: 'Keep it' }));
    expect(window.localStorage.getItem(ME_KEY)).toBe('demo_ace');
    expect(screen.queryByText(/no public profile for/)).toBeNull();
  });

  it('shows nothing at all when the request failed rather than 404ed', async () => {
    setMe('demo_ace');
    stubFetch([{ path: '/v1/players/demo_ace', status: 503, body: { error: 'internal' } }]);
    const { container } = render(<YourStanding />);

    // Offline, DNS, a refused CORS preflight, a 5xx — none of them is news about
    // a handle, so the panel is absent rather than alarming.
    await waitFor(() => {
      expect(container.textContent).toBe('');
    });
  });

  it('forgets the handle only when asked', async () => {
    setMe('demo_ace');
    stubFetch([
      {
        path: '/v1/players/demo_ace',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);
    const user = userEvent.setup();
    render(<YourStanding />);

    await screen.findByText(/no public profile for/);
    await user.click(screen.getByRole('button', { name: 'Forget it' }));
    expect(window.localStorage.getItem(ME_KEY)).toBeNull();
    expect($me.get()).toBeNull();
  });
});

describe('standing', () => {
  it('never exceeds 100 %, because rank and the denominator count different populations', () => {
    // Rank is ban-filtered; `players` counts rows including banned players. So a
    // rank can be better than the denominator implies, and the arithmetic has to
    // clamp rather than produce 104 %.
    expect(standing(1, 41)).toBe(100);
    expect(standing(1, 1)).toBe(100);
    // A rank better than the population it is measured against still clamps.
    expect(standing(1, 0)).toBe(0);
    expect(standing(0, 10)).toBe(0);
    expect(standing(21, 40)).toBe(50);
    expect(standing(400, 400)).toBe(0);
  });
});
