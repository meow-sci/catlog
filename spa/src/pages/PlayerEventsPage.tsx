import { ArrowLeft, SearchX } from 'lucide-react';
import { useEffect, useState } from 'react';
import { getPlayerEvents } from '../api/client.ts';
import type { EventRow } from '../api/types.ts';
import { hrefFor, navigate } from '../state/router.ts';
import { EventDetails } from '../ui/EventPayload.tsx';
import { TypeFilter } from '../ui/EventTypeFilter.tsx';
import { formatInstant, isoInstant } from '../ui/format.ts';
import {
  Button,
  DataRow,
  DataTable,
  DenseCell,
  Empty,
  Failure,
  HeadCell,
  HeadRow,
  Loading,
  Panel,
  PanelFooter,
  PanelHeader,
  TableRows,
  Value,
} from '../ui/kit/index.ts';
import { TailControls } from '../ui/TailControls.tsx';
import { KNOWN_EVENT_TYPES, mergeHead, useEventLog } from '../ui/useEventLog.ts';
import { useEventTail } from '../ui/useEventTail.ts';
import { SECONDS } from '../ui/units.ts';

const ALL_TYPES = '';

/**
 * The raw event log for one handle.
 *
 * # Why catlog publishes this
 *
 * Every other surface is a *derived* number — a fold's opinion about a history
 * nobody outside the server can see. This is the history, and it is what makes
 * "your record is 214 m/s" checkable rather than merely asserted.
 *
 * # What it does not show, and where that is enforced
 *
 * `install` is dropped and `career` and `kid` are relabelled per player, by the
 * **server** (`readapi/privacy.go`), because publishing them would link two
 * handles belonging to one person — the one thing the handle-only identity model
 * exists to prevent. The client never sees the raw values, which is the point:
 * redaction is not something a frontend can implement, and this page therefore
 * cannot get it wrong. `wall_t` is omitted server-side for the same class of
 * reason. `user_key` has never existed on any of these responses.
 *
 * Flights flagged as cheated are excluded server-side too — the promise on the
 * privacy page is that they "score nothing and never appear publicly", and the
 * endpoint keeps it by filtering rather than by marking, since a browsable
 * public record of whose flights were flagged is exactly the durable public
 * consequence the constitution forbids.
 *
 * # The filter lives in the URL
 *
 * `?type=` rather than component state, so a filtered log is a pasteable link
 * and the back button undoes a filter change the way it undoes a page change.
 *
 * # The live tail
 *
 * The same stream the global log tails, narrowed server-side to this handle.
 * Off by default here: a profile's log is usually read as history, and a table
 * that moves under a reader who came to check one number is a cost, not a
 * feature. The toggle is one press away.
 */
export function PlayerEventsPage(props: { readonly handle: string; readonly type: string }) {
  const { handle, type } = props;
  const log = useEventLog(`events:${handle}:${type}`, (before, signal) =>
    getPlayerEvents(handle, { type, before }, signal),
  );

  // Off by default; see the file comment.
  const [tailOn, setTailOn] = useState(false);
  const atTop = useWindowAtTop();
  const tail = useEventTail({
    enabled: tailOn && log.status === 'ready',
    type,
    handle,
    // Pause-while-scrolled: a reader partway down the log must not have the
    // rows walk out from under the cursor.
    paused: !atTop,
    onLive: log.refresh,
  });

  if (log.status === 'error' && log.error?.notFound === true) {
    return (
      <div className="space-y-5">
        <BackLink handle={handle} />
        <Panel className="flex flex-col items-center gap-3 px-4 py-16 text-center">
          <SearchX aria-hidden className="text-fg-subtle size-8" />
          <h1>Nothing here</h1>
          <p className="text-fg-muted max-w-[65ch]">
            catlog has no public profile for <span className="text-fg font-medium">{handle}</span>.
          </p>
        </Panel>
      </div>
    );
  }

  // Stream arrivals merged over the paged log, deduped by seq — the tail's
  // copy of an overlapping row wins.
  const events = tail.rows.length === 0 ? log.events : mergeHead(log.events, tail.rows);

  // The documented taxonomy, plus anything the loaded pages actually contain —
  // so a type a newer mod version emits is filterable the moment one appears.
  const seen = new Set(events.map((e) => e.type));
  const types = [...new Set([...KNOWN_EVENT_TYPES, ...seen])].sort((a, b) => a.localeCompare(b));

  return (
    <div className="space-y-5">
      <BackLink handle={handle} />
      <header className="max-w-[65ch]">
        <h1>
          Everything <span className="font-mono">{handle}</span> reported
        </h1>
        <p className="text-fg-muted mt-1">
          The events themselves, newest first, as catlog received them. Every board on this site is
          an opinion about this list.
        </p>
      </header>

      <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
        <TypeFilter
          types={types}
          value={type}
          onChange={(next) => {
            navigate({ name: 'playerEvents', handle, type: next });
          }}
        />
        <TailControls
          enabled={tailOn}
          onToggle={setTailOn}
          status={tail.status}
          pending={tail.pending}
          onShowNew={() => {
            tail.flush();
            window.scrollTo({ top: 0, behavior: 'instant' });
          }}
        />
      </div>

      <Panel>
        <PanelHeader
          title="Event log"
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
            {type === ALL_TYPES
              ? 'Nothing has happened yet. Fly something.'
              : `No ${type} events on this page of the log.`}
          </Empty>
        )}
        {events.length > 0 && <EventTable events={events} />}
        {/*
         * **Page until `next` is absent, never until a page comes back short.**
         * A filtered page that hit the server's scan bound is short *and* has a
         * cursor, and looks exactly like the end of a log. Getting this wrong
         * silently truncates somebody's history, which is the one failure this
         * page cannot have.
         */}
        {log.next !== null && (
          <PanelFooter>
            <span>
              There is more log below this. A short page is not the end of it — the server stops
              scanning before it stops finding.
            </span>
            <Button onPress={log.loadMore} isDisabled={log.isLoadingMore}>
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
    </div>
  );
}

/**
 * Whether the window is scrolled to (near) the top — what "at the head of the
 * log" means on this page, where the document itself scrolls. The threshold is
 * a little slack so the sticky header's height does not count as "away".
 */
function useWindowAtTop(): boolean {
  const [atTop, setAtTop] = useState(true);
  useEffect(() => {
    const read = () => {
      setAtTop(window.scrollY < 80);
    };
    read();
    window.addEventListener('scroll', read, { passive: true });
    return () => {
      window.removeEventListener('scroll', read);
    };
  }, []);
  return atTop;
}

function EventTable(props: { readonly events: readonly EventRow[] }) {
  return (
    <DataTable aria-label="Event log">
      <HeadRow>
        <HeadCell id="seq" align="end">
          Seq
        </HeadCell>
        <HeadCell id="recv">When</HeadCell>
        <HeadCell id="type" isRowHeader>
          Type
        </HeadCell>
        <HeadCell id="sim" align="end">
          Career clock
        </HeadCell>
        <HeadCell id="payload">Payload</HeadCell>
      </HeadRow>
      <TableRows items={props.events}>
        {(event: EventRow) => (
          <DataRow id={event.seq} data-type={event.type} className="event-row">
            <DenseCell align="end" className="text-fg-subtle text-sm">
              {event.seq}
            </DenseCell>
            <DenseCell className="text-fg-muted text-sm whitespace-nowrap tabular-nums">
              <time dateTime={isoInstant(event.recv)}>{formatInstant(event.recv)}</time>
            </DenseCell>
            <DenseCell className="text-fg font-mono text-sm">{event.type}</DenseCell>
            <DenseCell align="end" className="text-fg-muted text-sm">
              {/* `sim_t` is seconds since this career's game started, so it
                  reads as a duration: 1832.5 is 30m 32s, not "1832.5". */}
              {event.sim_t === undefined ? (
                <span title="This event carried no clock reading">—</span>
              ) : (
                <Value value={event.sim_t} unit={SECONDS} />
              )}
            </DenseCell>
            <DenseCell>
              <EventDetails event={event} />
            </DenseCell>
          </DataRow>
        )}
      </TableRows>
    </DataTable>
  );
}

function BackLink(props: { readonly handle: string }) {
  return (
    <a
      href={hrefFor({ name: 'player', handle: props.handle })}
      className="text-fg-muted hover:text-fg inline-flex items-center gap-1 text-sm"
    >
      <ArrowLeft aria-hidden className="size-3.5" />
      {props.handle}
    </a>
  );
}
