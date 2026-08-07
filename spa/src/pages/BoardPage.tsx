import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { getBoard } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor, PAGE_SIZE } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import { cn } from '../ui/cn.ts';
import { Empty, Failure, Loading, Panel, PanelHeader } from '../ui/kit.tsx';

const pagerButton =
  'inline-flex items-center gap-1 rounded-md border border-ink-800 bg-ink-850 px-3 py-1.5 ' +
  'text-xs font-medium text-ink-200 transition-colors';

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
          <PagerLink
            href={hrefFor({ name: 'board', stat, offset: Math.max(0, offset - PAGE_SIZE) })}
            isEnabled={hasPrev}
          >
            <ChevronLeft aria-hidden className="size-3.5" />
            Previous
          </PagerLink>
          <PagerLink
            href={hrefFor({ name: 'board', stat, offset: offset + PAGE_SIZE })}
            isEnabled={hasNext}
          >
            Next
            <ChevronRight aria-hidden className="size-3.5" />
          </PagerLink>
        </nav>
      )}
    </div>
  );
}

/**
 * One step of the pager.
 *
 * An `<a href>` and not a button: a page of a board is a place, so it must be
 * middle-clickable, cmd-clickable and copyable like every other link here. The
 * unavailable direction renders as a `<span aria-disabled>` rather than a dead
 * link — there is no URL to offer, and a link to nowhere is worse than no link.
 */
function PagerLink(props: {
  readonly href: string;
  readonly isEnabled: boolean;
  readonly children: ReactNode;
}) {
  if (!props.isEnabled) {
    return (
      <span aria-disabled className={cn(pagerButton, 'cursor-not-allowed opacity-40')}>
        {props.children}
      </span>
    );
  }
  return (
    <a href={props.href} className={cn(pagerButton, 'hover:bg-ink-800')}>
      {props.children}
    </a>
  );
}
