import { ArrowLeft, SearchX } from 'lucide-react';
import { useState } from 'react';
import type { Key } from 'react-aria-components';
import { Label, ListBox, ListBoxItem, Popover, Select, SelectValue } from 'react-aria-components';
import type { EventRow } from '../api/types.ts';
import { hrefFor } from '../state/router.ts';
import { formatInstant, isoInstant } from '../ui/format.ts';
import {
  Button,
  DataRow,
  DataTable,
  DenseCell,
  Details,
  Empty,
  Failure,
  HeadCell,
  HeadRow,
  Json,
  Loading,
  Panel,
  PanelFooter,
  PanelHeader,
  TableRows,
  Value,
} from '../ui/kit/index.ts';
import { KNOWN_EVENT_TYPES, useEventLog } from '../ui/useEventLog.ts';
import { formatValue, SECONDS, unitForKey } from '../ui/units.ts';

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
 * # Numbers here are raw, and that is the inverse of every other table
 *
 * A reader on this page wants `7799`, not `7 799 m/s`. So payload values are
 * printed as the API sent them, with the *formatted* reading in the `title`.
 * Unknown payload keys are rendered as well: catlog preserves them, and a raw
 * view that dropped them would be lying about what it recorded.
 */
export function PlayerEventsPage(props: { readonly handle: string }) {
  const { handle } = props;
  const [type, setType] = useState<string>(ALL_TYPES);
  const log = useEventLog(handle, type);

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

  // The documented taxonomy, plus anything the loaded pages actually contain —
  // so a type a newer mod version emits is filterable the moment one appears.
  const seen = new Set(log.events.map((e) => e.type));
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

      <TypeFilter types={types} value={type} onChange={setType} />

      <Panel>
        <PanelHeader
          title="Event log"
          aside={
            log.events.length > 0
              ? `${String(log.events.length)} event${log.events.length === 1 ? '' : 's'} loaded`
              : undefined
          }
        />
        {log.status === 'loading' && <Loading label="Loading the log…" />}
        {log.status === 'error' && log.error !== null && (
          <Failure what="read the event log" error={log.error} />
        )}
        {log.status === 'ready' && log.events.length === 0 && (
          <Empty>
            {type === ALL_TYPES
              ? 'Nothing has happened yet. Fly something.'
              : `No ${type} events on this page of the log.`}
          </Empty>
        )}
        {log.events.length > 0 && <EventTable events={log.events} />}
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
        {log.status === 'ready' && log.next === null && log.events.length > 0 && (
          <PanelFooter>
            <span>That is the whole log.</span>
          </PanelFooter>
        )}
      </Panel>
    </div>
  );
}

function TypeFilter(props: {
  readonly types: readonly string[];
  readonly value: string;
  readonly onChange: (value: string) => void;
}) {
  const items = [
    { id: ALL_TYPES, label: 'Every type' },
    ...props.types.map((t) => ({ id: t, label: t })),
  ];
  return (
    <Select
      selectedKey={props.value}
      onSelectionChange={(key: Key | null) => {
        props.onChange(key === null ? ALL_TYPES : String(key));
      }}
      className="flex flex-wrap items-center gap-2"
    >
      <Label className="text-fg-muted text-sm">Show</Label>
      <Button className="min-w-56 justify-between">
        <SelectValue />
        <span aria-hidden className="text-fg-subtle">
          ▾
        </span>
      </Button>
      <Popover className="bg-panel-raised border-border shadow-popover max-h-80 w-(--trigger-width) overflow-auto rounded-lg border py-1">
        <ListBox items={items}>
          {(item: { id: string; label: string }) => (
            <ListBoxItem
              id={item.id}
              textValue={item.label}
              className="data-focused:bg-wash-hover text-fg cursor-pointer px-3 py-1.5 text-base outline-none"
            >
              {item.label}
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </Select>
  );
}

function EventTable(props: { readonly events: readonly EventRow[] }) {
  return (
    <DataTable aria-label="Event log">
      <HeadRow>
        <HeadCell id="recv">When</HeadCell>
        <HeadCell id="type" isRowHeader>
          Type
        </HeadCell>
        <HeadCell id="sim" align="end">
          Career clock
        </HeadCell>
        <HeadCell id="payload">Payload</HeadCell>
      </HeadRow>
      <TableRows items={props.events.map((e) => ({ ...e, id: e.id }))}>
        {(event: EventRow & { id: string }) => (
          <DataRow id={event.id} data-type={event.type} className="event-row">
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

function EventDetails(props: { readonly event: EventRow }) {
  const { event } = props;
  return (
    <Details summary="Payload">
      <div className="space-y-3">
        <PayloadValues payload={event.payload} />
        <Json value={event.payload} />
        <dl className="text-fg-muted grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-xs">
          <dt>event</dt>
          <dd className="break-all">{event.id}</dd>
          {event.session !== undefined && (
            <>
              <dt>session</dt>
              <dd className="break-all">{event.session}</dd>
            </>
          )}
          {event.flight !== undefined && (
            <>
              <dt>flight</dt>
              <dd className="break-all">{event.flight}</dd>
            </>
          )}
          {event.career !== undefined && event.career !== '' && (
            <>
              <dt title="Relabelled per player, so it groups your own records and matches nobody else's">
                career
              </dt>
              <dd className="break-all">{event.career}</dd>
            </>
          )}
        </dl>
      </div>
    </Details>
  );
}

/**
 * Top-level payload values, **raw**, with the formatted reading as a `title`.
 *
 * The unit comes from the key — `speed_ms` is metres per second, `duration_s` is
 * seconds — because there is nothing in the value that says. An unrecognised key
 * gets no unit rather than a wrong one, and is still shown.
 */
function PayloadValues(props: { readonly payload: unknown }) {
  if (typeof props.payload !== 'object' || props.payload === null) return null;
  const entries = Object.entries(props.payload as Record<string, unknown>);
  if (entries.length === 0) return null;
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm">
      {entries.map(([key, value]) => {
        const unit = unitForKey(key);
        const readable =
          typeof value === 'number' && Number.isFinite(value)
            ? formatValue(value, unit)
            : undefined;
        return (
          <div key={key} className="col-span-2 grid grid-cols-subgrid">
            <dt className="text-fg-subtle font-mono">{key}</dt>
            <dd className="text-fg font-mono tabular-nums" title={readable}>
              {typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value)}
              {readable !== undefined && readable !== String(value) && (
                <span className="text-fg-muted ml-2 font-sans">({readable})</span>
              )}
            </dd>
          </div>
        );
      })}
    </dl>
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
