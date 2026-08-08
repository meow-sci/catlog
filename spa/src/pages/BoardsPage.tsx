import { getBoards } from '../api/client.ts';
import type { BoardSummary } from '../api/types.ts';
import { useResource } from '../api/useResource.ts';
import { ALLTIME, hrefFor } from '../state/router.ts';
import {
  DataCell,
  DataRow,
  DataTable,
  Failure,
  HeadCell,
  HeadRow,
  Loading,
  Panel,
  TableRows,
} from '../ui/kit/index.ts';
import { formatNumber, unitLabel } from '../ui/units.ts';

/**
 * Every board catlog is currently publishing: which exist, how populated, and
 * which way each one reads.
 *
 * The list comes from the API rather than a constant here, and that is
 * load-bearing rather than tidy: two board families (`fastest_to_<body>`,
 * `rud_<cause>`) take their keys from the event stream, because KSA's celestial
 * systems are game content that mods extend and the server treats a body name as
 * opaque. The set of boards therefore changes while the server is running, and
 * nothing in this file may assume its size or its membership.
 *
 * The stat key is not a column. It is in the URL and in `data-stat`, where a
 * test or a curious reader can find it; the title says it better.
 */
export function BoardsPage() {
  const boards = useResource('boards', getBoards);

  return (
    <div className="space-y-6">
      <header className="max-w-[65ch]">
        <h1 id="boards-title">Leaderboards</h1>
        <p className="text-fg-muted mt-1">
          Every board catlog publishes. A board with no entries is still a board.
        </p>
      </header>

      <Panel>
        {boards.status === 'loading' && <Loading label="Loading boards…" />}
        {boards.status === 'error' && <Failure what="load the board list" error={boards.error} />}
        {boards.status === 'ready' && (
          <DataTable aria-label="Leaderboards" id="boards-index">
            <HeadRow>
              <HeadCell id="title" isRowHeader>
                Board
              </HeadCell>
              <HeadCell id="unit">Measured in</HeadCell>
              <HeadCell id="count" align="end">
                Players
              </HeadCell>
            </HeadRow>
            <TableRows
              items={boards.data.boards.map((b) => ({ ...b, id: b.stat }))}
              renderEmptyState={() => (
                <p className="text-fg-muted px-4 py-8 text-sm">
                  No boards yet. Fly something, and give catlog something to rank.
                </p>
              )}
            >
              {(board: BoardSummary & { id: string }) => (
                <DataRow id={board.stat} className="boards-row" data-stat={board.stat}>
                  {/* A link in the cell, not `href` on the Row. React Aria will
                      happily make a whole row a link, but it does it with press
                      handling on a `<tr>` rather than an anchor — which the
                      router's one delegated listener cannot see, and which
                      cannot be middle-clicked or copied. */}
                  <DataCell className="font-medium">
                    <a
                      href={hrefFor({
                        name: 'board',
                        stat: board.stat,
                        offset: 0,
                        period: ALLTIME,
                      })}
                      className="text-fg hover:text-accent-text"
                    >
                      {board.title}
                    </a>
                  </DataCell>
                  {/* The display label, not the storage unit: this column has to
                      agree with the header on the board page it links to. */}
                  <DataCell className="text-fg-muted unit text-sm">
                    {unitLabel(board.unit)} <span aria-hidden>{board.ascending ? '↑' : '↓'}</span>
                    <span className="sr-only">
                      {board.ascending ? ' — lowest wins' : ' — highest wins'}
                    </span>
                  </DataCell>
                  <DataCell align="end" className="text-fg-muted text-sm">
                    {board.count === 0 ? 'empty' : formatNumber(board.count)}
                  </DataCell>
                </DataRow>
              )}
            </TableRows>
          </DataTable>
        )}
      </Panel>

      {/* Why a place somebody has been to may not be here yet. The number is the
          server's, not this file's — a client that hard-coded it would be wrong
          the first time the threshold moved. */}
      {boards.status === 'ready' && boards.data.min_players > 1 && (
        <p id="boards-note" className="text-fg-muted max-w-[65ch] text-sm">
          Boards for places, and for ways a vehicle came apart, come from what players actually did
          rather than from a list inside the server. One appears here once at least{' '}
          {boards.data.min_players} different players are on it. Before that it still exists, and
          still shows on the profile of whoever is on it — but a leaderboard with a single entrant
          is not a leaderboard.
        </p>
      )}
    </div>
  );
}
