import { X } from 'lucide-react';
import { useState } from 'react';
import { Cell, Column, Row, Table, TableBody, TableHeader, type Key } from 'react-aria-components';
import { TableLayout, Virtualizer } from 'react-aria-components/Virtualizer';
import { getEvents } from '../api/client.ts';
import type { EventRow } from '../api/types.ts';
import { hrefFor, navigate } from '../state/router.ts';
import { cn } from '../ui/cn.ts';
import { EventPayloadBody } from '../ui/EventPayload.tsx';
import { TypeFilter } from '../ui/EventTypeFilter.tsx';
import { formatInstant, isoInstant } from '../ui/format.ts';
import { HandleComboBox } from '../ui/kit/HandleComboBox.tsx';
import { EventSummary } from '../ui/kit/EventSummary.tsx';
import {
  Button,
  Empty,
  Failure,
  Loading,
  Panel,
  PanelFooter,
  PanelHeader,
  Pill,
  Value,
} from '../ui/kit/index.ts';
import { TailControls } from '../ui/TailControls.tsx';
import { KNOWN_EVENT_TYPES, mergeHead, useEventLog } from '../ui/useEventLog.ts';
import { useEventTail } from '../ui/useEventTail.ts';
import { SECONDS } from '../ui/units.ts';

/**
 * The global raw event log: every player mixed together, newest first, with a
 * live tail — the whole site's source material, watchable as it arrives.
 *
 * # Virtualized, and why the rows are uniform
 *
 * This is the one table with no natural bound — the log grows as long as
 * anyone flies — so rows render through React Aria's `Virtualizer` +
 * `TableLayout`, imported *in this lazy chunk only*: nothing on the critical
 * path pays for them. The layout is given a fixed `rowHeight`, which is what
 * keeps the virtualizer trivial — and it is why the payload column is a
 * one-line **allow-list summary** (`EventSummary`) rather than a disclosure:
 * an in-row expansion re-measuring under the scroller is exactly the fight the
 * master–detail pattern avoids. Clicking (or Enter on) a row selects it, and
 * the full payload renders in a panel below the table, reusing the same
 * machinery as the per-handle log.
 *
 * # The live tail's defaults
 *
 * On, while the view is at the head of the log — this page's resting state is
 * "watch the telemetry come in". Paging back turns it off: a reader walking
 * into history has said what they are doing, and the head moving under them
 * would be noise. The toggle always wins once touched. While the reader is
 * scrolled away from the top (or the tail is paused), arrivals buffer behind
 * an unobtrusive "N new events" button rather than moving the table.
 *
 * # Filters are URL state
 *
 * `?type=` and `?handle=` — a filtered view of the firehose is a link somebody
 * can paste, and the back button undoes a filter change.
 */
export function EventsPage(props: { readonly type: string; readonly handle: string }) {
  const { type, handle } = props;
  const log = useEventLog(`log:${type}:${handle}`, (before, signal) =>
    getEvents({ type, handle, before }, signal),
  );

  // `null` until the reader touches the toggle; the default is "on while at
  // the head", and paging back is what leaving the head means.
  const [tailPref, setTailPref] = useState<boolean | null>(null);
  const [pagedBack, setPagedBack] = useState(false);
  const tailOn = tailPref ?? !pagedBack;

  const [atTop, setAtTop] = useState(true);

  const tail = useEventTail({
    enabled: tailOn && log.status === 'ready',
    type,
    handle,
    paused: !atTop,
    onLive: log.refresh,
  });

  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);

  if (log.status === 'error' && log.error?.notFound === true) {
    // The one 404 this page can produce: an unknown, retired or banned
    // `?handle=` — the same one answer for all three as everywhere else.
    return (
      <div className="space-y-5">
        <EventsHeader />
        <Panel className="flex flex-col items-center gap-3 px-4 py-16 text-center">
          <h1>Nothing here</h1>
          <p className="text-fg-muted max-w-[65ch]">
            catlog has no public profile for <span className="text-fg font-medium">{handle}</span>.
          </p>
          <Button
            onPress={() => {
              navigate({ name: 'events', type, handle: '' });
            }}
          >
            Show everybody instead
          </Button>
        </Panel>
      </div>
    );
  }

  // Stream arrivals merged over the paged log, deduped by seq — the tail's
  // copy of an overlapping row wins.
  const events = tail.rows.length === 0 ? log.events : mergeHead(log.events, tail.rows);
  const selected = selectedSeq === null ? undefined : events.find((e) => e.seq === selectedSeq);

  const seen = new Set(events.map((e) => e.type));
  const types = [...new Set([...KNOWN_EVENT_TYPES, ...seen])].sort((a, b) => a.localeCompare(b));

  return (
    <div className="space-y-5">
      <EventsHeader />

      <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
        <TypeFilter
          types={types}
          value={type}
          onChange={(next) => {
            navigate({ name: 'events', type: next, handle });
          }}
        />
        <div className="flex items-center gap-2">
          {handle === '' ? (
            <HandleComboBox
              label="Filter by handle"
              placeholder="Filter by handle"
              className="w-48"
              onCommit={(picked) => {
                navigate({ name: 'events', type, handle: picked });
              }}
              clearOnCommit
            />
          ) : (
            <Pill className="flex items-center gap-1">
              <span className="text-fg font-mono">{handle}</span>
              <Button
                variant="ghost"
                aria-label={`Stop filtering by ${handle}`}
                onPress={() => {
                  navigate({ name: 'events', type, handle: '' });
                }}
                className="size-5 min-h-0 rounded-full p-0"
              >
                <X aria-hidden className="size-3.5" />
              </Button>
            </Pill>
          )}
        </div>
        <TailControls
          enabled={tailOn}
          onToggle={setTailPref}
          status={tail.status}
          pending={tail.pending}
          onShowNew={() => {
            tail.flush();
            // The grid element is its own scroll container; found by id in
            // this event handler rather than held in a ref, which the
            // react-hooks refs lint refuses to see pass through a prop.
            document
              .getElementById(RAW_LOG_TABLE_ID)
              ?.querySelector('[role="grid"]')
              ?.scrollTo({ top: 0, behavior: 'instant' });
            setAtTop(true);
          }}
        />
      </div>

      <Panel>
        <PanelHeader
          title="Raw event log"
          aside={
            events.length > 0
              ? `${String(events.length)} event${events.length === 1 ? '' : 's'} loaded`
              : undefined
          }
        />
        {log.status === 'loading' && <Loading label="Loading the log…" />}
        {log.status === 'error' && log.error !== null && (
          <Failure what="read the event log" error={log.error} />
        )}
        {log.status === 'ready' && events.length === 0 && (
          <Empty>
            {type === '' && handle === ''
              ? 'Nothing has happened yet. Fly something.'
              : 'No events match this filter yet.'}
          </Empty>
        )}
        {events.length > 0 && (
          <EventsTable
            events={events}
            selectedSeq={selectedSeq}
            onScrollTop={setAtTop}
            onSelect={(seq) => {
              setSelectedSeq((prev) => (prev === seq ? null : seq));
            }}
          />
        )}
        {/*
         * **Page until `next` is absent, never until a page comes back short.**
         * A filtered page that hit the server's scan bound is short *and* has a
         * cursor, and looks exactly like the end of a log.
         */}
        {log.next !== null && (
          <PanelFooter>
            <span>
              There is more log below this. A short page is not the end of it — the server stops
              scanning before it stops finding.
            </span>
            <Button
              onPress={() => {
                // Walking into history: the head is no longer where the reader
                // is, so the tail's default flips off (the toggle still wins).
                setPagedBack(true);
                log.loadMore();
              }}
              isDisabled={log.isLoadingMore}
            >
              {log.isLoadingMore ? 'Loading…' : 'Load older events'}
            </Button>
          </PanelFooter>
        )}
        {log.trimmed > 0 && (
          <PanelFooter>
            <span>
              Keeping the page light: the {log.trimmed} newest loaded event
              {log.trimmed === 1 ? '' : 's'} slid off the top as you paged back. Reload to start
              from the head again.
            </span>
          </PanelFooter>
        )}
        {log.status === 'ready' && log.next === null && log.events.length > 0 && (
          <PanelFooter>
            <span>That is the whole log.</span>
          </PanelFooter>
        )}
      </Panel>

      {selected !== undefined && (
        <Panel id="event-detail">
          <PanelHeader
            title={`Event ${String(selected.seq)} — ${selected.type}`}
            aside={
              <Button
                variant="ghost"
                aria-label="Close event detail"
                onPress={() => {
                  setSelectedSeq(null);
                }}
                className="size-6 min-h-0 p-0"
              >
                <X aria-hidden className="size-4" />
              </Button>
            }
          />
          <div className="px-3 py-3">
            <EventPayloadBody event={selected} />
          </div>
        </Panel>
      )}
    </div>
  );
}

function EventsHeader() {
  return (
    <header className="max-w-[65ch]">
      <h1>The raw event log</h1>
      <p className="text-fg-muted mt-1">
        Everything everybody reported, newest first, as catlog received it. Every board on this site
        is an opinion about this list. Click a row for its full payload.
      </p>
    </header>
  );
}

/** The fixed row height the virtualizer lays rows out on, px. */
const ROW_HEIGHT = 36;

/** The id of the wrapper around the scrollable grid — how "N new events" finds it. */
const RAW_LOG_TABLE_ID = 'raw-event-log';

/**
 * The virtualized table.
 *
 * Raw React Aria components rather than the kit's `DataTable`: the virtualizer
 * needs the `<table>` itself to be the fixed-height scroll container, where
 * the kit wraps its table in an overflow div and lets it grow. The cell and
 * header classes are the kit's, so the two tables read as one component.
 */
function EventsTable(props: {
  readonly events: readonly EventRow[];
  readonly selectedSeq: number | null;
  readonly onScrollTop: (atTop: boolean) => void;
  readonly onSelect: (seq: number) => void;
}) {
  return (
    <div id={RAW_LOG_TABLE_ID}>
      <Virtualizer
        layout={TableLayout}
        layoutOptions={{ rowHeight: ROW_HEIGHT, headingHeight: 36 }}
      >
        <Table
          aria-label="Raw event log"
          selectionMode="none"
          onRowAction={(key: Key) => {
            props.onSelect(Number(key));
          }}
          onScroll={(event) => {
            props.onScrollTop((event.target as HTMLElement).scrollTop < ROW_HEIGHT);
          }}
          className="h-[60vh] min-h-80 w-full overflow-auto text-base"
        >
          <TableHeader className="bg-panel text-fg-muted border-border sticky top-0 z-1 border-b text-sm font-semibold tracking-[0.04em] uppercase">
            <Column id="seq" width={90} className="px-3 py-2 text-right font-semibold tabular-nums">
              Seq
            </Column>
            <Column id="recv" width={185} className="px-3 py-2 text-left font-semibold">
              When
            </Column>
            <Column id="handle" width={150} className="px-3 py-2 text-left font-semibold">
              Handle
            </Column>
            <Column id="type" isRowHeader width={170} className="px-3 py-2 text-left font-semibold">
              Type
            </Column>
            <Column
              id="sim"
              width={110}
              className="px-3 py-2 text-right font-semibold tabular-nums"
            >
              Career clock
            </Column>
            <Column id="summary" minWidth={220} className="px-3 py-2 text-left font-semibold">
              Payload
            </Column>
          </TableHeader>
          <TableBody items={props.events}>
            {(event: EventRow) => (
              <Row
                id={event.seq}
                data-type={event.type}
                className={cn(
                  'data-hovered:bg-wash-hover data-focus-visible:bg-wash-hover border-border cursor-pointer border-b transition-colors duration-150',
                  props.selectedSeq === event.seq && 'bg-wash-selected',
                )}
              >
                <Cell className="text-fg-subtle px-3 py-1 text-right text-sm whitespace-nowrap tabular-nums">
                  {event.seq}
                </Cell>
                <Cell className="text-fg-muted px-3 py-1 text-sm whitespace-nowrap tabular-nums">
                  <time dateTime={isoInstant(event.recv)}>{formatInstant(event.recv)}</time>
                </Cell>
                <Cell className="px-3 py-1 text-sm whitespace-nowrap">
                  {event.handle === undefined || event.handle === '' ? (
                    <span className="text-fg-subtle">—</span>
                  ) : (
                    <a
                      href={hrefFor({ name: 'player', handle: event.handle })}
                      className="text-accent-text hover:underline"
                    >
                      {event.handle}
                    </a>
                  )}
                </Cell>
                <Cell className="text-fg px-3 py-1 font-mono text-sm whitespace-nowrap">
                  {event.type}
                </Cell>
                <Cell className="text-fg-muted px-3 py-1 text-right text-sm whitespace-nowrap tabular-nums">
                  {event.sim_t === undefined ? (
                    <span title="This event carried no clock reading">—</span>
                  ) : (
                    <Value value={event.sim_t} unit={SECONDS} />
                  )}
                </Cell>
                <Cell className="max-w-0 truncate px-3 py-1">
                  <EventSummary type={event.type} payload={event.payload} />
                </Cell>
              </Row>
            )}
          </TableBody>
        </Table>
      </Virtualizer>
    </div>
  );
}
