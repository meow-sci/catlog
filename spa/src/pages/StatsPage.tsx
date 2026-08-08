import { getStats } from '../api/client.ts';
import type { CollectionStats, EventStats, TypeCount, WindowStats } from '../api/types.ts';
import { useResource } from '../api/useResource.ts';
import { formatDay, formatInstant } from '../ui/format.ts';
import {
  DataCell,
  DataRow,
  DataTable,
  Failure,
  HeadCell,
  HeadRow,
  Loading,
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  StatTile,
  TableRows,
} from '../ui/kit/index.ts';
import { formatNumber, formatValue } from '../ui/units.ts';

/**
 * Stats of stats.
 *
 * Every other screen here is about a player. This one is about the collection —
 * how many events catlog is holding, of what kinds, arriving how fast, since
 * when, and how much has been derived from them. Data for nerds about the data
 * collected by nerds, and deliberately not a leaderboard: no records, no
 * ranking, nobody's handle.
 *
 * One request for the whole page. `GET /v1/stats` assembles and memoises the
 * entire answer server-side, so there is nothing here to compose out of several
 * reads and no way for two halves of the page to describe two different views
 * of the database.
 */
export function StatsPage() {
  const stats = useResource('stats', getStats);

  return (
    <div className="space-y-6" id="stats-page">
      <header className="max-w-[65ch]">
        <h1 id="stats-title">Stats of stats</h1>
        <p className="text-fg-muted mt-1">
          How much catlog is holding, what kind, and how fast it is arriving. Data for nerds, about
          the data collected by nerds — nothing here is a record and nobody is ranked.
        </p>
      </header>

      {stats.status === 'loading' && (
        <Panel>
          <Loading label="Counting…" />
        </Panel>
      )}
      {stats.status === 'error' && (
        <Panel>
          <Failure what="read the collection stats" error={stats.error} />
        </Panel>
      )}
      {stats.status === 'ready' && (
        <>
          <Headline events={stats.data.events} types={stats.data.collection.types} />
          <Windows windows={stats.data.events.windows} />
          <Daily events={stats.data.events} />
          <Types events={stats.data.events} />
          <Collection collection={stats.data.collection} generated={stats.data.generated} />
        </>
      )}
    </div>
  );
}

/** The four headline figures. */
function Headline(props: { readonly events: EventStats; readonly types: number }) {
  const { events } = props;
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4" id="stats-tiles">
      <StatTile
        label="Events logged"
        value={formatNumber(events.total)}
        note={`Across ${formatNumber(props.types)} event types.`}
      />
      <StatTile
        label="Events per day"
        value={formatNumber(events.per_day)}
        note={`Over ${formatNumber(events.days)} days that saw one.`}
      />
      <StatTile
        label="Busiest day"
        value={events.busiest === undefined ? '—' : formatNumber(events.busiest.count)}
        note={events.busiest?.bucket ?? 'No day has had one yet.'}
      />
      <StatTile
        label="Watching since"
        value={events.first === undefined ? '—' : formatDay(events.first)}
        note={
          events.last === undefined
            ? 'Nothing has arrived yet.'
            : `Newest ${formatDay(events.last)}.`
        }
      />
    </div>
  );
}

/**
 * The four rolling windows, each broken down by type.
 *
 * The bucket is shown because the window is the one the *server's* clock is in:
 * a reader in Auckland looking at "today" is looking at a UTC day, and the only
 * honest way to say so is to print which one.
 */
function Windows(props: { readonly windows: readonly WindowStats[] }) {
  return (
    <section id="stats-windows" className="space-y-3">
      <h2 className="text-fg-muted text-sm font-semibold tracking-[0.04em] uppercase">Lately</h2>
      <div className="grid gap-3 sm:grid-cols-2">
        {props.windows.map((w) => (
          <Panel key={w.period} className="window-card" id={`window-${w.period}`}>
            <PanelHeader title={PERIOD_LABELS[w.period] ?? w.period} aside={w.bucket} />
            <PanelBody className="space-y-3">
              <p className="text-fg text-xl font-semibold tabular-nums">
                {formatNumber(w.count)} <span className="text-fg-muted text-sm">events</span>
              </p>
              {w.types.length === 0 ? (
                <p className="text-fg-muted text-sm">Nothing yet in this window.</p>
              ) : (
                <TypeBars types={w.types} max={w.count} />
              )}
            </PanelBody>
          </Panel>
        ))}
      </div>
    </section>
  );
}

/**
 * How a window is written for a reader.
 *
 * The API's keys are durations ("weekly"); what a reader wants to see is the
 * window ("This week"). Mirrors `web.periodLabels`, fallback included, so a
 * window the API adds renders its own key rather than nothing.
 */
const PERIOD_LABELS: Readonly<Record<string, string>> = {
  alltime: 'All time',
  daily: 'Today',
  weekly: 'This week',
  monthly: 'This month',
  yearly: 'This year',
};

/**
 * A type breakdown as labelled bars.
 *
 * `aria-hidden` on the bar: it is a third rendering of a number the row already
 * states twice, and a screen reader announcing a meter as well would read the
 * same fact three times.
 */
function TypeBars(props: { readonly types: readonly TypeCount[]; readonly max: number }) {
  return (
    <ul className="space-y-1.5">
      {props.types.map((t) => (
        <li
          key={t.type}
          className="grid grid-cols-[minmax(6rem,10rem)_1fr_auto] items-center gap-2"
        >
          <code className="text-fg-muted truncate text-xs">{t.type}</code>
          <span aria-hidden className="bg-panel-sunken h-2 overflow-hidden rounded-sm">
            <i className="bg-accent block h-full" style={{ width: barWidth(t.count, props.max) }} />
          </span>
          <span className="text-fg-muted text-xs tabular-nums">
            {formatNumber(t.count)} · {formatValue(t.share * 100, '%')}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * One bar's length as a CSS width.
 *
 * Floored at 1 % for any non-zero count, because a chart whose smallest bar is
 * invisible lies about which days had events: on ninety days with one busy
 * afternoon in them, everything else would round away.
 */
function barWidth(n: number, max: number): string {
  if (n <= 0 || max <= 0) return '0%';
  return `${String(Math.min(Math.max((n / max) * 100, 1), 100))}%`;
}

/**
 * The daily series.
 *
 * Only days that carry an event are here — a day catlog was switched off is not
 * a zero, and drawing it as one would be a claim about a quiet day that nobody
 * made. The columns are fixed-width and the strip scrolls rather than
 * compressing, so ninety days on a phone is a scroll and not ninety hairlines.
 */
function Daily(props: { readonly events: EventStats }) {
  const { daily } = props.events;
  if (daily.length === 0) return null;
  const max = daily.reduce((m, d) => Math.max(m, d.count), 0);

  return (
    <Panel id="stats-daily">
      <PanelHeader title="Events per day" aside={`${formatNumber(daily.length)} days`} />
      <PanelBody>
        {/* A list of bars is not an <img>, but `role="img"` is what collapses
            ninety <li> into one announcement instead of ninety. The rule's
            suggestion would replace the chart with an element that cannot have
            children, so it is turned off for exactly this element. */}
        {/* oxlint-disable jsx-a11y/prefer-tag-over-role */}
        <ol
          aria-label="Daily event counts, oldest first"
          className="flex h-24 items-end gap-0.5 overflow-x-auto"
        >
          {daily.map((d) => (
            <li
              key={d.bucket}
              data-bucket={d.bucket}
              data-value={d.count}
              title={`${d.bucket}: ${formatNumber(d.count)}`}
              className="flex h-full w-2 shrink-0 items-end"
            >
              <i
                className="bg-accent block min-h-0.5 w-full rounded-t-sm"
                style={{ height: barWidth(d.count, max) }}
              />
            </li>
          ))}
        </ol>
        {/* oxlint-enable jsx-a11y/prefer-tag-over-role */}
      </PanelBody>
    </Panel>
  );
}

/** Every type catlog has ever received, most-counted first. */
function Types(props: { readonly events: EventStats }) {
  const { types } = props.events;
  const max = types.reduce((m, t) => Math.max(m, t.count), 0);

  return (
    <Panel id="stats-types">
      <PanelHeader title="Every event type" />
      <DataTable aria-label="Event types">
        <HeadRow>
          <HeadCell id="type" isRowHeader>
            Type
          </HeadCell>
          <HeadCell id="count" align="end">
            Events
          </HeadCell>
          <HeadCell id="share">Share</HeadCell>
          <HeadCell id="first">First seen</HeadCell>
          <HeadCell id="last">Last seen</HeadCell>
        </HeadRow>
        <TableRows
          items={types.map((t) => ({ ...t, id: t.type }))}
          renderEmptyState={() => (
            <p className="text-fg-muted px-4 py-8 text-sm">Nothing has been logged yet.</p>
          )}
        >
          {(t: TypeCount & { id: string }) => (
            <DataRow id={t.type} data-type={t.type}>
              <DataCell>
                <code className="text-xs">{t.type}</code>
              </DataCell>
              <DataCell align="end" className="tabular-nums">
                {formatNumber(t.count)}
              </DataCell>
              <DataCell className="min-w-32">
                <span aria-hidden className="bg-panel-sunken block h-2 overflow-hidden rounded-sm">
                  <i className="bg-accent block h-full" style={{ width: barWidth(t.count, max) }} />
                </span>
                <span className="text-fg-muted text-xs tabular-nums">
                  {formatValue(t.share * 100, '%')}
                </span>
              </DataCell>
              <DataCell className="text-fg-muted text-sm">
                {t.first === undefined ? '—' : formatDay(t.first)}
              </DataCell>
              <DataCell className="text-fg-muted text-sm">
                {t.last === undefined ? '—' : formatDay(t.last)}
              </DataCell>
            </DataRow>
          )}
        </TableRows>
      </DataTable>
    </Panel>
  );
}

/**
 * What catlog has made of the log.
 *
 * The last three rows are what makes the rest honest: everything on this page is
 * a projection, and a projection is only as current as its cursor. A figure here
 * disagreeing with a figure on a board page is a lag, and this is where a reader
 * finds that out.
 */
const CENSUS_ROWS: readonly {
  readonly key: keyof CollectionStats;
  readonly label: string;
  readonly note: string;
}[] = [
  { key: 'boards', label: 'Leaderboards published', note: 'Empty ones included.' },
  {
    key: 'placements',
    label: 'Placements held',
    note: 'One player on one board, counted once per board.',
  },
  { key: 'handles', label: 'Handles claimed', note: 'Retired and banned ones are not counted.' },
  {
    key: 'scoring_players',
    label: 'Players on a board',
    note: 'Of those, the ones who have scored anywhere.',
  },
  { key: 'flights', label: 'Flights', note: 'Every flight catlog has seen an event from.' },
  {
    key: 'flagged_flights',
    label: 'Flagged flights',
    note: 'Teleport, refuel, resource edit, console or tuning. Excluded from every board.',
  },
  { key: 'careers', label: 'Careers', note: 'One KSA save, played over time.' },
  {
    key: 'rewound_careers',
    label: 'Rewound careers',
    note: 'An earlier save was loaded. It qualifies a time and nothing else.',
  },
  { key: 'kittens', label: 'Kittens', note: 'Every kitten anybody has flown.' },
  {
    key: 'bodies',
    label: 'Bodies reached',
    note: 'catlog keeps no list of these. Players went there.',
  },
  {
    key: 'feed_rows',
    label: 'Feed lines held',
    note: 'Capped, so this is a fact about the cap once catlog is busy.',
  },
  { key: 'log_head', label: 'Log head', note: 'The newest sequence number in the event log.' },
  { key: 'projected', label: 'Projected through', note: 'How far the projector has folded.' },
  {
    key: 'lag',
    label: 'Projector lag',
    note: 'Events the numbers above have not caught up with yet.',
  },
];

function Collection(props: { readonly collection: CollectionStats; readonly generated: number }) {
  return (
    <Panel id="stats-collection">
      <PanelHeader title="What catlog has made of it" />
      <DataTable aria-label="Collection census">
        <HeadRow>
          <HeadCell id="what" isRowHeader>
            What
          </HeadCell>
          <HeadCell id="n" align="end">
            Count
          </HeadCell>
          <HeadCell id="note">Meaning</HeadCell>
        </HeadRow>
        <TableRows items={CENSUS_ROWS.map((r) => ({ ...r, id: r.key }))}>
          {(row: (typeof CENSUS_ROWS)[number] & { id: string }) => (
            <DataRow id={row.key} data-census={row.key}>
              <DataCell className="font-medium">{row.label}</DataCell>
              <DataCell align="end" className="tabular-nums">
                {formatNumber(props.collection[row.key])}
              </DataCell>
              <DataCell className="text-fg-muted text-sm">{row.note}</DataCell>
            </DataRow>
          )}
        </TableRows>
      </DataTable>
      <PanelFooter>
        <span>Assembled {formatInstant(props.generated)}.</span>
      </PanelFooter>
    </Panel>
  );
}
