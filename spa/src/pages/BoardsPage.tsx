import { ChevronRight } from 'lucide-react';
import { getBoards } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor } from '../state/router.ts';
import { Failure, Loading, Panel, Pill } from '../ui/kit.tsx';

/**
 * Every board catlog is currently publishing.
 *
 * The list comes from the API rather than a constant here, and that is now
 * load-bearing rather than tidy: two board families (`fastest_to_<body>`,
 * `rud_<cause>`) take their keys from the event stream, because KSA's celestial
 * systems are game content that mods extend and the server treats a body name as
 * opaque. The set of boards therefore changes while the server is running, and
 * nothing in this file may assume its size or its membership.
 */
export function BoardsPage() {
  const boards = useResource('boards', getBoards);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-ink-50 text-2xl font-semibold">Leaderboards</h1>
        <p className="text-ink-400 mt-1 text-sm">
          Every board catlog publishes. A board with no entries is still a board.
        </p>
      </header>

      <Panel>
        {boards.status === 'loading' && <Loading label="Loading boards…" />}
        {boards.status === 'error' && <Failure what="load the board list" error={boards.error} />}
        {boards.status === 'ready' && (
          <ul className="divide-ink-850 divide-y">
            {boards.data.boards.map((board) => (
              <li key={board.stat}>
                <a
                  href={hrefFor({ name: 'board', stat: board.stat, offset: 0 })}
                  className="hover:bg-ink-850 group flex items-center gap-4 px-4 py-3 transition-colors"
                >
                  <span className="min-w-0 flex-1">
                    <span className="text-ink-50 group-hover:text-flare-400 block font-medium">
                      {board.title}
                    </span>
                    <span className="text-ink-400 block font-mono text-xs">{board.stat}</span>
                  </span>
                  <Pill title={board.ascending ? 'Lowest value wins' : 'Highest value wins'}>
                    {board.unit} <span aria-hidden>{board.ascending ? '↑' : '↓'}</span>
                  </Pill>
                  <span className="text-ink-400 w-20 text-right font-mono text-xs tabular-nums">
                    {board.count === 0
                      ? 'empty'
                      : `${String(board.count)} ${board.count === 1 ? 'entry' : 'entries'}`}
                  </span>
                  <ChevronRight aria-hidden className="text-ink-700 size-4 shrink-0" />
                </a>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      {/* Why a place somebody has been to may not be here yet. The number is the
          server's, not this file's. */}
      {boards.status === 'ready' && boards.data.min_players > 1 && (
        <p className="text-ink-400 text-xs">
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
