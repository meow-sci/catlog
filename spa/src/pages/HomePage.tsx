import { ChevronRight } from 'lucide-react';
import { getBoard, getBoards } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import { pickFeatured } from '../ui/featured.ts';
import { FeedPanel } from '../ui/FeedPanel.tsx';
import { Empty, Failure, Loading, Panel, PanelHeader } from '../ui/kit.tsx';

const FEATURED_ROWS = 3;

/**
 * The front page.
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
    <div className="space-y-8">
      <header className="space-y-2">
        <h1 className="text-ink-50 text-3xl font-semibold tracking-tight">
          Leaderboards for things that went wrong
        </h1>
        <p className="text-ink-400 max-w-2xl text-sm">
          catlog records what Kitten Space Agency flights actually did — the lithobrakes survived,
          the speeds reached, the disassemblies. This is the React reader for the same public API
          the main site is built on.
        </p>
      </header>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <div className="space-y-6">
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
          <a
            href={hrefFor({ name: 'boards' })}
            className="text-flare-400 hover:text-flare-500 inline-flex items-center gap-1 text-sm font-medium"
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

function FeaturedBoard(props: { readonly stat: string }) {
  const { stat } = props;
  const board = useResource(`featured:${stat}`, (signal) =>
    getBoard(stat, FEATURED_ROWS, 0, signal),
  );

  return (
    <Panel>
      <PanelHeader
        title={board.status === 'ready' ? board.data.title : stat}
        aside={
          <a
            href={hrefFor({ name: 'board', stat, offset: 0 })}
            className="hover:text-flare-400 inline-flex items-center gap-1"
          >
            full board
            <ChevronRight aria-hidden className="size-3.5" />
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
        />
      )}
    </Panel>
  );
}
