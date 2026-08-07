import { useStore } from '@nanostores/react';
import type { BoardRow } from '../api/types.ts';
import { $me, isMe } from '../state/me.ts';
import { hrefFor } from '../state/router.ts';
import { describeContext, hasContext } from './context.ts';
import { $now, formatAgo, isoInstant } from './format.ts';
import {
  DataCell,
  DataRow,
  DataTable,
  Details,
  HeadCell,
  HeadRow,
  Json,
  TableRows,
  Value,
} from './kit/index.ts';
import { unitLabel } from './units.ts';

/**
 * A leaderboard.
 *
 * # The default row, and what is not in it
 *
 * Rank, handle, value with its unit, the human-meaningful context, and when.
 * That is the whole of it. The `flight` ULID is out — a client-minted id that
 * means nothing to a reader and eats the widest column — and so is `career`,
 * which the server has already relabelled per player and which is still a
 * 16-character token nobody wants in a table. Both stay one disclosure away in
 * **Details**, which shows the blob exactly as the API sent it. That is safe
 * because what the API sent is already post-redaction: there is nothing further
 * to strip client-side, and there is no client-side redaction to get wrong.
 *
 * # Direction
 *
 * `ascending` is published by the server per board and is **never inferred**
 * here. The career-time boards are seconds since a career began, and a table
 * that presented the fastest ascent as though it were the worst one would be a
 * wrong answer rather than a styling gap. It is stated in words as well as
 * marked, because an arrow is not a sentence.
 */
export function BoardTable(props: {
  readonly unit: string;
  readonly rows: readonly BoardRow[];
  readonly ascending: boolean;
  readonly showContext?: boolean;
  /** Row rank → the board's own offset, so a compact preview can hide the "when" column. */
  readonly compact?: boolean;
}) {
  const now = useStore($now);
  const me = useStore($me);
  const compact = props.compact === true;
  const anyContext = props.showContext !== false && props.rows.some((r) => hasContext(r.context));
  const direction = props.ascending ? 'lowest first' : 'highest first';

  return (
    <DataTable aria-label="Leaderboard">
      <HeadRow>
        <HeadCell id="rank" align="end" className="w-14">
          #
        </HeadCell>
        <HeadCell id="handle" isRowHeader>
          Player
        </HeadCell>
        {/* `normal-case tracking-normal` undoes the header row's uppercasing for
            this one cell, and only this one: "M/S" is not a unit, "PA" is not a
            unit, and "RUDS" is not how catlog writes that word. The datastar
            site does the same with `thead th.value`. */}
        <HeadCell id="value" align="end" className="normal-case tracking-normal">
          {/* The label says what the column *is*, not what each row says. It is
              the unit wherever a cell ends in that unit — a length column
              legitimately mixes `999 m` and `1.82 Mm` and `m` names both — and
              the quantity where no cell does: a career-time column renders
              `5m 13s` and `243d 01h`, so it is headed "Time" rather than the
              "ms" the API publishes. */}
          <span title={`Ranked ${direction}`}>
            {unitLabel(props.unit)} <span aria-hidden>{props.ascending ? '↑' : '↓'}</span>
            <span className="sr-only"> — ranked {direction}</span>
          </span>
        </HeadCell>
        {anyContext && (
          <HeadCell id="context" className="hidden sm:table-cell">
            Where
          </HeadCell>
        )}
        {!compact && (
          <HeadCell id="updated" align="end" className="hidden md:table-cell">
            Updated
          </HeadCell>
        )}
      </HeadRow>
      <TableRows items={props.rows} dependencies={[now, anyContext, compact, me]}>
        {(row: BoardRow) => (
          <DataRow
            id={`${String(row.rank)}:${row.handle}`}
            isMe={isMe(row.handle, me)}
            className="board-row"
            data-rank={row.rank}
            data-handle={row.handle}
          >
            <DataCell align="end" className="text-fg-muted rank">
              {row.rank}
            </DataCell>
            <DataCell>
              <a
                href={hrefFor({ name: 'player', handle: row.handle })}
                className="text-fg hover:text-accent-text font-medium"
              >
                {row.handle}
              </a>
              {isMe(row.handle, me) && <span className="text-accent-text ml-2 text-xs">you</span>}
            </DataCell>
            <DataCell align="end" className="value text-fg font-medium" data-value={row.value}>
              <Value value={row.value} unit={props.unit} rewound={row.rewound} />
            </DataCell>
            {anyContext && (
              <DataCell className="context text-fg-muted hidden text-sm sm:table-cell">
                <ContextCell context={row.context} />
              </DataCell>
            )}
            {!compact && (
              <DataCell align="end" className="text-fg-muted hidden text-sm md:table-cell">
                <time dateTime={isoInstant(row.updated)}>{formatAgo(row.updated, now)}</time>
              </DataCell>
            )}
          </DataRow>
        )}
      </TableRows>
    </DataTable>
  );
}

/**
 * The allow-listed half of a row's `context`, with the rest one click away.
 *
 * The disclosure shows the blob as the API sent it — which is already
 * post-redaction, so nothing here has to know what a hazard looks like.
 */
function ContextCell(props: { readonly context: unknown }) {
  const pairs = describeContext(props.context);
  if (!hasContext(props.context)) return null;
  return (
    <div className="flex flex-col items-start gap-1">
      {pairs.length > 0 && (
        <span className="context-pairs flex flex-wrap gap-x-3 gap-y-0.5">
          {pairs.map((pair) => (
            <span key={pair.key}>
              <span className="text-fg-subtle">{pair.key} </span>
              <span className="text-fg-muted tabular-nums">{pair.value}</span>
            </span>
          ))}
        </span>
      )}
      <Details summary="Details">
        <Json value={props.context} />
      </Details>
    </div>
  );
}
