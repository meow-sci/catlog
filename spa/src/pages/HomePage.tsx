import { ChevronRight } from 'lucide-react';
import { getBoard, getBoards } from '../api/client.ts';
import type { BoardsResponse } from '../api/types.ts';
import { useResource } from '../api/useResource.ts';
import { ALLTIME, hrefFor } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import { pickFeatured } from '../ui/featured.ts';
import { FeedPanel } from '../ui/FeedPanel.tsx';
import { Empty, Failure, Loading, Panel, PanelHeader, StatTile } from '../ui/kit/index.ts';
import { formatNumber } from '../ui/units.ts';
import { YourStanding } from '../ui/YourStanding.tsx';

const FEATURED_ROWS = 3;

/**
 * Where am I, and what is happening?
 *
 * In order: the global figures, the viewer's own standing if they have claimed a
 * handle, three featured boards, and the live feed. A first-time visitor sees
 * what catlog is; a returning one sees themselves first.
 *
 * The boards it previews are chosen from the ones the server actually lists
 * (`../ui/featured.ts`) rather than named here, because the board index changes
 * at runtime: two families take their keys from the event stream, so a page
 * pinned to three fixed names could be pinned to a board that does not exist.
 */
export function HomePage() {
  const boards = useResource('boards', getBoards);
  const featured =
    boards.status === 'ready' ? pickFeatured(boards.data.boards.map((b) => b.stat)) : [];

  return (
    <div className="space-y-6">
      <header className="max-w-[65ch] space-y-2">
        <h1 id="home-title">Leaderboards for things that went wrong</h1>
        <p className="text-fg-muted">
          catlog watches your Kitten Space Agency flights and keeps score. Lithobrakes you walked
          away from, speeds nobody should reach, and every rapid unscheduled disassembly along the
          way.
        </p>
      </header>

      {boards.status === 'ready' && <GlobalTiles data={boards.data} />}

      <YourStanding />

      <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <div className="space-y-6">
          <div id="featured-boards" className="space-y-6">
            {boards.status === 'loading' && (
              <Panel>
                <Loading label="Loading boards…" />
              </Panel>
            )}
            {boards.status === 'error' && (
              <Panel>
                <Failure what="load the board list" error={boards.error} />
              </Panel>
            )}
            {featured.map((stat) => (
              <FeaturedBoard key={stat} stat={stat} />
            ))}
          </div>
          <a
            href={hrefFor({ name: 'boards' })}
            className="text-accent-text inline-flex items-center gap-1 text-sm font-medium hover:underline"
          >
            See every board
            <ChevronRight aria-hidden className="size-4" />
          </a>
        </div>
        <FeedPanel />
      </div>
    </div>
  );
}

/**
 * The global figures, assembled from `GET /v1/leaderboards` and nothing else.
 *
 * There is no `/v1/stats` endpoint and this deliberately does not ask for one:
 * the board index already answers "how many boards, how populated, which is the
 * busiest", it is one request the page was making anyway, and it is the one
 * summary whose cost is obviously bounded.
 */
function GlobalTiles(props: { readonly data: BoardsResponse }) {
  const { boards } = props.data;
  const placements = boards.reduce((sum, b) => sum + b.count, 0);
  const busiest = boards.reduce<(typeof boards)[number] | null>(
    (best, b) => (best === null || b.count > best.count ? b : best),
    null,
  );

  return (
    <div className="grid gap-3 sm:grid-cols-3">
      <StatTile label="Boards" value={formatNumber(boards.length)} note="Empty ones included." />
      <StatTile
        label="Placements"
        value={formatNumber(placements)}
        note="Rows held across every board."
      />
      <StatTile
        label="Busiest board"
        value={busiest === null || busiest.count === 0 ? '—' : formatNumber(busiest.count)}
        note={busiest === null || busiest.count === 0 ? 'Nobody has scored yet.' : busiest.title}
      />
    </div>
  );
}

function FeaturedBoard(props: { readonly stat: string }) {
  const { stat } = props;
  const board = useResource(`featured:${stat}`, (signal) =>
    getBoard(stat, FEATURED_ROWS, 0, ALLTIME, signal),
  );

  return (
    <Panel className="featured-board" stat={stat}>
      <PanelHeader
        title={board.status === 'ready' ? board.data.title : stat}
        aside={
          <a
            href={hrefFor({ name: 'board', stat, offset: 0, period: ALLTIME })}
            className="text-accent-text hover:underline"
          >
            full board
          </a>
        }
      />
      {board.status === 'loading' && <Loading label="Loading…" />}
      {board.status === 'error' && <Failure what="load this board" error={board.error} />}
      {board.status === 'ready' && board.data.rows.length === 0 && (
        <Empty>Nobody is on this board yet.</Empty>
      )}
      {board.status === 'ready' && board.data.rows.length > 0 && (
        <BoardTable
          unit={board.data.unit}
          ascending={board.data.ascending}
          rows={board.data.rows}
          showContext={false}
          compact
        />
      )}
    </Panel>
  );
}
