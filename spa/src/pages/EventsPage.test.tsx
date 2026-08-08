import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import type { EventsResponse } from '../api/types.ts';
import { stubFetch } from '../test/http.ts';
import { EventsPage } from './EventsPage.tsx';

/**
 * The global raw log, through the real data path — `useEventLog` → the real
 * client → a stubbed fetch — like every other page test.
 *
 * The table is virtualized, and a virtualizer only renders what fits its
 * viewport — which happy-dom reports as zero-sized. The prototype getters
 * below give every element a plausible laid-out size so the virtualizer has a
 * viewport to fill. Restored after the file; nothing else in the suite
 * measures layout.
 */
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', {
    configurable: true,
    get() {
      return 800;
    },
  });
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true,
    get() {
      return 1200;
    },
  });
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get() {
      return 800;
    },
  });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get() {
      return 1200;
    },
  });
});

afterAll(() => {
  for (const name of ['clientHeight', 'clientWidth', 'offsetHeight', 'offsetWidth']) {
    // eslint-style dynamic delete is fine here: these were test-added getters.
    delete (HTMLElement.prototype as unknown as Record<string, unknown>)[name];
  }
});

const PAGE1: EventsResponse = {
  limit: 50,
  // A short page WITH a cursor: the scan-cap shape, not the end of the log.
  next: '90',
  events: [
    {
      seq: 92,
      handle: 'demo_ace',
      id: '01J9VA',
      type: 'vehicle.impact',
      ver: 1,
      session: '01J9VS',
      flight: '01J9VF',
      career: 'b7k2q9x4m0nrt3vz',
      sim_t: 1832.5,
      recv: 1_800_000_000_000,
      payload: {
        body: 'kerbin',
        speed_ms: 7799,
        energy_j: 48_000_000,
        survived: true,
        unheard_of_key: 'kept',
      },
    },
    {
      seq: 91,
      handle: 'demo_tumbler',
      id: '01J9VB',
      type: 'mystery.type',
      ver: 1,
      recv: 1_799_999_000_000,
      payload: { alpha: 1, beta: 2, secret_value: 'must not render' },
    },
  ],
};

const PAGE2: EventsResponse = {
  limit: 50,
  events: [
    {
      seq: 90,
      handle: 'demo_ace',
      id: '01J9VC',
      type: 'session.started',
      ver: 1,
      recv: 1_799_000_000_000,
      payload: { mod_ver: '0.1.0' },
    },
  ],
};

describe('EventsPage', () => {
  // Queries for row content are scoped to the grid: the type filter's hidden
  // native <select> also names every event type, so an unscoped getByText
  // would match twice.
  const grid = () => within(screen.getByRole('grid', { name: 'Raw event log' }));

  it('renders the global log with seq, handle links and allow-list summaries', async () => {
    stubFetch([{ path: '/v1/events', body: PAGE1 }]);
    render(<EventsPage type="" handle="" />);

    await screen.findByRole('grid', { name: 'Raw event log' });
    expect(await grid().findByText('vehicle.impact')).toBeTruthy();
    // Every row names its player, as a link to the profile.
    const link = grid().getByRole('link', { name: 'demo_ace' });
    expect(link.getAttribute('href')).toBe('/p/demo_ace');
    // seq is a column now — it is what the stream and the snapshot merge by.
    expect(grid().getByText('92')).toBeTruthy();
    // A known type summarises through the allow-list…
    expect(grid().getByText('Kerbin')).toBeTruthy();
    // …an unknown type renders a count, never its values.
    expect(grid().getByText('3 fields')).toBeTruthy();
    expect(document.body.innerHTML).not.toContain('must not render');
  });

  it('opens the full payload as master-detail on row action', async () => {
    stubFetch([{ path: '/v1/events', body: PAGE1 }]);
    const user = userEvent.setup();
    render(<EventsPage type="" handle="" />);

    await screen.findByRole('grid', { name: 'Raw event log' });
    await user.click(await grid().findByText('vehicle.impact'));

    // The detail panel reuses the shared payload machinery: raw values with
    // the formatted reading beside them, unknown keys included.
    expect(await screen.findByText('7799')).toBeTruthy();
    expect(screen.getByText('unheard_of_key')).toBeTruthy();
    // And the identifiers, seq included.
    expect(screen.getByText('01J9VA')).toBeTruthy();
  });

  it('keeps paging while a cursor is present, even after a short page', async () => {
    const mock = stubFetch([
      { path: 'before=90', body: PAGE2 },
      { path: '/v1/events', body: PAGE1 },
    ]);
    const user = userEvent.setup();
    render(<EventsPage type="" handle="" />);

    await screen.findByRole('grid', { name: 'Raw event log' });
    await user.click(screen.getByRole('button', { name: 'Load older events' }));

    expect(await grid().findByText('session.started')).toBeTruthy();
    expect(mock.mock.calls.some((c) => String(c[0]).includes('before=90'))).toBe(true);
    expect(await screen.findByText('That is the whole log.')).toBeTruthy();
  });

  it('shows the live-tail status without an EventSource, as unavailable with retry messaging', async () => {
    // The suite runs with EventSource stubbed out (test setup), so the tail —
    // which defaults ON at the head of the global log — degrades to its
    // `unavailable` status line. Crucially this is a status, not an error page:
    // the paginated log still renders underneath it.
    stubFetch([{ path: '/v1/events', body: PAGE1 }]);
    render(<EventsPage type="" handle="" />);

    await screen.findByRole('grid', { name: 'Raw event log' });
    expect(grid().getByText('vehicle.impact')).toBeTruthy();
    expect(screen.getByText(/stream is full or unreachable/)).toBeTruthy();
  });

  it('answers an unknown ?handle= with the one 404 answer and a way out', async () => {
    stubFetch([
      {
        path: '/v1/events',
        status: 404,
        body: { error: 'not_found', detail: 'no such player' },
      },
    ]);
    render(<EventsPage type="" handle="nobody" />);

    expect(await screen.findByRole('heading', { name: 'Nothing here' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Show everybody instead' })).toBeTruthy();
    for (const leak of [/banned/i, /retired/i]) {
      expect(screen.queryByText(leak)).toBeNull();
    }
  });

  it('shows the server error rather than an empty table when the read fails', async () => {
    stubFetch([{ path: '/v1/events', status: 500, body: { error: 'internal', detail: 'boom' } }]);
    render(<EventsPage type="" handle="" />);

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Could not read the event log');
    expect(alert.textContent).toContain('internal');
  });

  it('says something friendly when the log is empty', async () => {
    stubFetch([{ path: '/v1/events', body: { limit: 50, events: [] } }]);
    render(<EventsPage type="" handle="" />);
    expect(await screen.findByText('Nothing has happened yet. Fly something.')).toBeTruthy();
  });
});
