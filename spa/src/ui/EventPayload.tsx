import type { EventRow } from '../api/types.ts';
import { Details, Json } from './kit/index.ts';
import { formatValue, unitForKey } from './units.ts';

/**
 * The full-payload view of one raw event, shared by the per-handle log and the
 * global log (lifted out of `PlayerEventsPage`, per review).
 *
 * # Numbers here are raw, and that is the inverse of every other table
 *
 * A reader on this view wants `7799`, not `7 799 m/s`. So payload values are
 * printed as the API sent them, with the *formatted* reading beside them.
 * Unknown payload keys are rendered as well: catlog preserves them, and a raw
 * view that dropped them would be lying about what it recorded. (The one-line
 * *summary* column is the opposite — an allow-list; see `kit/EventSummary`.)
 */

/** The payload and identifiers of one event — the body of both pages' detail view. */
export function EventPayloadBody(props: { readonly event: EventRow }) {
  const { event } = props;
  return (
    <div className="space-y-3">
      <PayloadValues payload={event.payload} />
      <Json value={event.payload} />
      <dl className="text-fg-muted grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-xs">
        <dt title="The server-assigned position in the stored log">seq</dt>
        <dd className="break-all">{event.seq}</dd>
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
  );
}

/** [EventPayloadBody] behind a disclosure — the per-row form the tables use. */
export function EventDetails(props: { readonly event: EventRow }) {
  return (
    <Details summary="Payload">
      <EventPayloadBody event={props.event} />
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
export function PayloadValues(props: { readonly payload: unknown }) {
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
