import { useStore } from '@nanostores/react';
import { getCompare } from '../api/client.ts';
import { MAX_COMPARE_HANDLES } from '../api/client.ts';
import type { CompareBoard, CompareRow } from '../api/types.ts';
import { useResource } from '../api/useResource.ts';
import { $me } from '../state/me.ts';
import { ALLTIME, hrefFor, navigate, PAGE_SIZE } from '../state/router.ts';
import {
  DataCell,
  DataRow,
  DataTable,
  Empty,
  Failure,
  HandleComboBox,
  HeadCell,
  HeadRow,
  Loading,
  Panel,
  PanelBody,
  PanelHeader,
  TableRows,
  Value,
} from '../ui/kit/index.ts';
import { HandleTags } from '../ui/kit/HandleTags.tsx';
import { unitLabel } from '../ui/units.ts';

/**
 * Up to eight handles, side by side, across every board any of them is on.
 *
 * # The URL is the state
 *
 * Every change writes `?handles=`, so the comparison is a link you can paste
 * into a Discord channel — which is the actual social act being designed for,
 * and the reason none of this lives in component state.
 *
 * # One request, not N
 *
 * `GET /v1/compare` rather than N profile reads. N answers can disagree: a
 * projection commit between the first and the last shows one player's new record
 * next to another's stale rank. One request reads them all against one view of
 * the projections, so the table is internally consistent.
 *
 * # The four rules the table has to respect
 *
 *  - **`found: false` is a column, not an omission.** It is headed with the
 *    string that was asked for and marked "no such player". Silently dropping it
 *    lets a typo look like a defeat — and it reveals nothing that asking for that
 *    one profile does not already reveal, since unknown, retired and banned are
 *    one answer everywhere.
 *  - **Absent is absent, not zero.** A board only some of them are on shows `—`
 *    for the others, the same rule the folds follow for a missing `peak_g`.
 *  - **`rank` is the world rank**, not the rank among the compared handles:
 *    "3rd in the world", not "2nd of your friends". Labelled so.
 *  - **The best cell is decided by `ascending`**, never inferred — which is the
 *    whole reason the server publishes it per board.
 */
export function ComparePage(props: { readonly handles: readonly string[] }) {
  const { handles } = props;
  const me = useStore($me);
  const compare = useResource(`compare:${handles.join(',')}`, (signal) =>
    getCompare(handles, signal),
  );

  const go = (next: readonly string[]) => {
    navigate({ name: 'compare', handles: next.slice(0, MAX_COMPARE_HANDLES) });
  };

  // The effective list comes back from the server — extras over the cap are
  // dropped there rather than rejected — so the chips render what was actually
  // compared, not what was asked for.
  const columns =
    compare.status === 'ready'
      ? compare.data.handles
      : handles.map((h) => ({ handle: h, found: true }));
  const full = handles.length >= MAX_COMPARE_HANDLES;

  return (
    <div className="space-y-5">
      <header className="max-w-[65ch]">
        <h1>Side by side</h1>
        <p className="text-fg-muted mt-1">
          Up to {MAX_COMPARE_HANDLES} handles, on every board any of them has reached. The link in
          your address bar is the comparison — send it to the people in it.
        </p>
      </header>

      <Panel>
        <PanelHeader
          title="Comparing"
          aside={
            me !== null && !handles.some((h) => h.toLowerCase() === me.toLowerCase()) ? (
              <button
                type="button"
                className="text-accent-text cursor-pointer hover:underline"
                onClick={() => {
                  go([me, ...handles]);
                }}
              >
                Add yourself
              </button>
            ) : undefined
          }
        />
        <PanelBody className="space-y-3">
          <HandleTags
            label="Compared handles"
            handles={columns}
            onRemove={(handle) => {
              go(handles.filter((h) => h.toLowerCase() !== handle.toLowerCase()));
            }}
          />
          {full ? (
            <p className="text-fg-muted text-sm">
              That is {MAX_COMPARE_HANDLES} — the most a side-by-side table stays readable at.
              Remove one to add another.
            </p>
          ) : (
            <HandleComboBox
              label="Add a handle to the comparison"
              placeholder="Add a handle"
              className="max-w-xs"
              clearOnCommit
              onCommit={(handle) => {
                go([...handles, handle]);
              }}
            />
          )}
        </PanelBody>
      </Panel>

      {handles.length === 0 && (
        <Panel>
          <Empty>Nobody to compare yet. Add a handle, or two, or eight.</Empty>
        </Panel>
      )}

      {handles.length > 0 && (
        <Panel>
          <PanelHeader
            title="Boards"
            aside={
              compare.status === 'ready'
                ? `${String(compare.data.boards.length)} board${compare.data.boards.length === 1 ? '' : 's'}`
                : undefined
            }
          />
          {compare.status === 'loading' && <Loading label="Loading the comparison…" />}
          {compare.status === 'error' && (
            <Failure what="compare these players" error={compare.error} />
          )}
          {compare.status === 'ready' && compare.data.boards.length === 0 && (
            <Empty>
              {compare.data.handles.every((h) => !h.found)
                ? 'None of those handles is a player catlog knows about.'
                : 'None of them is on a board yet.'}
            </Empty>
          )}
          {compare.status === 'ready' && compare.data.boards.length > 0 && (
            <CompareTable boards={compare.data.boards} columns={compare.data.handles} me={me} />
          )}
        </Panel>
      )}
    </div>
  );
}

function CompareTable(props: {
  readonly boards: readonly CompareBoard[];
  readonly columns: readonly { readonly handle: string; readonly found: boolean }[];
  readonly me: string | null;
}) {
  return (
    <DataTable aria-label="Comparison">
      <HeadRow>
        <HeadCell id="board" isRowHeader className="min-w-48">
          Board
        </HeadCell>
        {props.columns.map((column) => (
          <HeadCell key={column.handle} id={column.handle} align="end">
            {column.found ? (
              <a
                href={hrefFor({ name: 'player', handle: column.handle })}
                className="text-fg hover:text-accent-text font-mono normal-case"
              >
                {column.handle}
              </a>
            ) : (
              <span className="text-fg-muted font-mono normal-case">
                {column.handle}
                <span className="text-fg-subtle block text-xs font-normal">no such player</span>
              </span>
            )}
          </HeadCell>
        ))}
      </HeadRow>
      <TableRows items={props.boards.map((b) => ({ ...b, id: b.stat }))}>
        {(board: CompareBoard & { id: string }) => (
          <DataRow id={board.stat} data-stat={board.stat}>
            <DataCell>
              <a
                href={hrefFor({ name: 'board', stat: board.stat, offset: 0, period: ALLTIME })}
                className="text-fg hover:text-accent-text font-medium"
              >
                {board.title}
              </a>
              {/* One row is one board, so the row header carries what a column
                  header carries elsewhere: the label of what these cells hold. */}
              <span className="text-fg-muted block text-sm">
                {unitLabel(board.unit)} · {board.ascending ? 'lowest wins' : 'highest wins'} ·{' '}
                {board.players} on the board
              </span>
            </DataCell>
            {props.columns.map((column) => (
              <CompareCell
                key={column.handle}
                board={board}
                handle={column.handle}
                best={bestHandle(board)}
              />
            ))}
          </DataRow>
        )}
      </TableRows>
    </DataTable>
  );
}

/** The best of the compared values on one board, decided by `ascending` — never inferred. */
function bestHandle(board: CompareBoard): string | null {
  let best: CompareRow | null = null;
  for (const row of board.rows) {
    if (best === null || (board.ascending ? row.value < best.value : row.value > best.value)) {
      best = row;
    }
  }
  return best?.handle ?? null;
}

function CompareCell(props: {
  readonly board: CompareBoard;
  readonly handle: string;
  readonly best: string | null;
}) {
  const row = props.board.rows.find((r) => r.handle === props.handle);
  if (row === undefined) {
    return (
      <DataCell align="end" className="text-fg-subtle">
        <span title="not on this board">—</span>
      </DataCell>
    );
  }
  const isBest = props.best === props.handle;
  return (
    <DataCell align="end" data-value={row.value} className="value">
      <span className={isBest ? 'text-accent-text font-semibold' : 'text-fg'}>
        <Value value={row.value} unit={props.board.unit} rewound={row.rewound} />
        {isBest && <span className="sr-only"> — best of these</span>}
      </span>
      <a
        href={hrefFor({
          name: 'board',
          stat: props.board.stat,
          offset: Math.floor((row.rank - 1) / PAGE_SIZE) * PAGE_SIZE,
          period: ALLTIME,
        })}
        className="text-fg-muted hover:text-accent-text block text-xs tabular-nums"
        title="Rank on the whole board, not among the compared handles"
      >
        #{row.rank} in the world
      </a>
    </DataCell>
  );
}
