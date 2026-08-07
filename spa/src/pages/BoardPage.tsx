import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react';
import { Button } from 'react-aria-components';
import { getBoard } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor, navigate, PAGE_SIZE } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import { Empty, Failure, Loading, Panel, PanelHeader } from '../ui/kit.tsx';

const pagerButton =
  'inline-flex items-center gap-1 rounded-md border border-ink-800 bg-ink-850 px-3 py-1.5 ' +
  'text-xs font-medium text-ink-200 transition-colors data-hovered:bg-ink-800 ' +
  'data-disabled:cursor-not-allowed data-disabled:opacity-40';

/**
 * One leaderboard, paged.
 *
 * The offset lives in the URL, not in component state, so a page of a board is a
 * link somebody can send — and so the back button steps through pages. That is
 * the same property the server-rendered site gets from `?offset=`; here it costs
 * a route parameter and a `navigate` call.
 */
export function BoardPage(props: { readonly stat: string; readonly offset: number }) {
  const { stat, offset } = props;
  const board = useResource(`board:${stat}:${String(offset)}`, (signal) =>
    getBoard(stat, PAGE_SIZE, offset, signal),
  );

  // A page that came back full is very likely not the last one; §4.8 publishes
  // no total, so "there is probably more" is the honest thing to render.
  const rows = board.status === 'ready' ? board.data.rows : [];
  const hasNext = board.status === 'ready' && rows.length >= board.data.limit;
  const hasPrev = offset > 0;

  return (
    <div className="space-y-6">
      <div>
        <a
          href={hrefFor({ name: 'boards' })}
          className="text-ink-400 hover:text-ink-200 inline-flex items-center gap-1 text-xs"
        >
          <ArrowLeft aria-hidden className="size-3.5" />
          All boards
        </a>
        <h1 className="text-ink-50 mt-2 text-2xl font-semibold">
          {board.status === 'ready' ? board.data.title : stat}
        </h1>
        <p className="text-ink-400 mt-1 font-mono text-xs">{stat}</p>
      </div>

      <Panel>
        <PanelHeader
          title="Standings"
          aside={
            rows.length > 0
              ? `ranks ${String(offset + 1)}–${String(offset + rows.length)}`
              : undefined
          }
        />
        {board.status === 'loading' && <Loading label="Loading standings…" />}
        {board.status === 'error' && (
          <Failure
            what={board.error.notFound ? `find a board called ${stat}` : 'load this board'}
            error={board.error}
          />
        )}
        {board.status === 'ready' && rows.length === 0 && (
          <Empty>
            {offset > 0
              ? 'There is nothing on this page — try going back.'
              : 'Nobody is on this board yet.'}
          </Empty>
        )}
        {board.status === 'ready' && rows.length > 0 && (
          <BoardTable unit={board.data.unit} rows={rows} />
        )}
      </Panel>

      {(hasPrev || hasNext) && (
        <nav aria-label="Pagination" className="flex items-center justify-between">
          <Button
            className={pagerButton}
            isDisabled={!hasPrev}
            onPress={() => {
              navigate({ name: 'board', stat, offset: Math.max(0, offset - PAGE_SIZE) });
            }}
          >
            <ChevronLeft aria-hidden className="size-3.5" />
            Previous
          </Button>
          <Button
            className={pagerButton}
            isDisabled={!hasNext}
            onPress={() => {
              navigate({ name: 'board', stat, offset: offset + PAGE_SIZE });
            }}
          >
            Next
            <ChevronRight aria-hidden className="size-3.5" />
          </Button>
        </nav>
      )}
    </div>
  );
}
