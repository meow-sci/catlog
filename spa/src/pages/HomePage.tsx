import { ChevronRight } from 'lucide-react';
import { getBoard } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import { FeedPanel } from '../ui/FeedPanel.tsx';
import { Empty, Failure, Loading, Panel, PanelHeader } from '../ui/kit.tsx';

/**
 * The three boards the front page previews: one record, one speed record and one
 * counter, so a first-time visitor sees what all three kinds of board look like
 * without clicking. Matches the server-rendered site's own selection.
 */
const FEATURED = ['biggest_lithobrake_survived', 'fastest_orbital_speed', 'rud_total'] as const;

const FEATURED_ROWS = 3;

export function HomePage() {
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
          {FEATURED.map((stat) => (
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
        <BoardTable unit={board.data.unit} rows={board.data.rows} showContext={false} />
      )}
    </Panel>
  );
}
