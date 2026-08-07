import { useStore } from '@nanostores/react';
import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react';
import { getBoard, getBoards, getPlayer } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { $me, isMe } from '../state/me.ts';
import { ALLTIME, hrefFor, PAGE_SIZE } from '../state/router.ts';
import { BoardTable } from '../ui/BoardTable.tsx';
import {
  DisabledLinkButton,
  Empty,
  Failure,
  LinkButton,
  Loading,
  Panel,
  PanelFooter,
  PanelHeader,
  PeriodTabs,
  Pill,
} from '../ui/kit/index.ts';
import { formatNumber } from '../ui/units.ts';

/**
 * One leaderboard, paged, over a chosen window.
 *
 * The offset **and the period** live in the URL, not in component state, so a
 * page of a board is a link somebody can send and the back button steps through
 * both. The period selector is the cheapest thing on the site that makes a
 * leaderboard worth revisiting: it turns a static all-time ranking into "what
 * happened this week".
 */
export function BoardPage(props: {
  readonly stat: string;
  readonly offset: number;
  readonly period: string;
}) {
  const { stat, offset, period } = props;
  const board = useResource(`board:${stat}:${period}:${String(offset)}`, (signal) =>
    getBoard(stat, PAGE_SIZE, offset, period, signal),
  );
  // The windows come from the board index, which publishes `periods` per board,
  // rather than from a list in this file — the same reason the index itself is
  // not a constant here. It is the request the header and the front page already
  // made, and it is `s-maxage=30` at the CDN. A family board too small to be
  // listed borrows the set from a board that is: the server returns the same
  // windows for every board, so the fallback states a fact rather than a guess.
  const index = useResource('boards', getBoards);
  const periods =
    index.status === 'ready'
      ? (index.data.boards.find((b) => b.stat === stat)?.periods ??
        index.data.boards[0]?.periods ??
        [])
      : [];

  // A page that came back full is very likely not the last one; §4.8 publishes
  // no total, so "there is probably more" is the honest thing to render.
  const rows = board.status === 'ready' ? board.data.rows : [];
  const hasNext = board.status === 'ready' && rows.length >= board.data.limit;
  const hasPrev = offset > 0;

  return (
    <div className="space-y-5">
      <div>
        <a
          href={hrefFor({ name: 'boards' })}
          className="text-fg-muted hover:text-fg inline-flex items-center gap-1 text-sm"
        >
          <ArrowLeft aria-hidden className="size-3.5" />
          All boards
        </a>
        <h1 id="board-title" data-stat={stat} className="mt-2">
          {board.status === 'ready' ? board.data.title : stat}
        </h1>
        {/* Which way the board reads comes from the server, per board. The
            career-time boards rank the smallest value first, and a reader that
            assumed otherwise would present the fastest ascent as the worst. */}
        {board.status === 'ready' && (
          <p
            id="board-direction"
            data-ascending={board.data.ascending}
            className="text-fg-muted mt-1 text-sm"
          >
            Measured in {board.data.unit === '' ? 'plain counts' : board.data.unit}.{' '}
            {board.data.ascending ? 'Lowest wins.' : 'Highest wins.'}
          </p>
        )}
      </div>

      {periods.length > 0 && (
        <div className="flex flex-wrap items-center gap-3">
          <PeriodTabs
            label="Window"
            selected={period}
            periods={periods}
            hrefFor={(p) => hrefFor({ name: 'board', stat, offset: 0, period: p })}
            labelFor={(p) => (p === ALLTIME ? 'all time' : p)}
          />
          {board.status === 'ready' &&
            board.data.bucket !== undefined &&
            board.data.bucket !== '' && (
              <Pill title="The window these rows cover, in UTC">{board.data.bucket}</Pill>
            )}
        </div>
      )}

      <Panel>
        <PanelHeader
          title="Standings"
          aside={
            rows.length > 0
              ? `ranks ${formatNumber(offset + 1)}–${formatNumber(offset + rows.length)}`
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
              : period === ALLTIME
                ? 'Nobody is on this board yet.'
                : 'Nobody has scored in this window yet.'}
          </Empty>
        )}
        {board.status === 'ready' && rows.length > 0 && (
          <BoardTable unit={board.data.unit} ascending={board.data.ascending} rows={rows} />
        )}
        {board.status === 'ready' && (
          <YouAreHere
            stat={stat}
            period={period}
            offset={offset}
            rows={rows.map((r) => r.handle)}
          />
        )}
      </Panel>

      {(hasPrev || hasNext) && (
        <nav aria-label="Pagination" className="flex items-center justify-between">
          {hasPrev ? (
            <LinkButton
              href={hrefFor({
                name: 'board',
                stat,
                offset: Math.max(0, offset - PAGE_SIZE),
                period,
              })}
            >
              <ChevronLeft aria-hidden className="size-3.5" />
              Previous
            </LinkButton>
          ) : (
            <DisabledLinkButton>
              <ChevronLeft aria-hidden className="size-3.5" />
              Previous
            </DisabledLinkButton>
          )}
          {hasNext ? (
            <LinkButton href={hrefFor({ name: 'board', stat, offset: offset + PAGE_SIZE, period })}>
              Next
              <ChevronRight aria-hidden className="size-3.5" />
            </LinkButton>
          ) : (
            <DisabledLinkButton>
              Next
              <ChevronRight aria-hidden className="size-3.5" />
            </DisabledLinkButton>
          )}
        </nav>
      )}
    </div>
  );
}

/**
 * `You: #147`, at the foot of the table, linking to the page you are actually on.
 *
 * The rank comes from the profile endpoint, which already publishes it, and the
 * page is arithmetic: `offset = floor((rank - 1) / PAGE_SIZE) * PAGE_SIZE`. No
 * new endpoint, no `?around=` parameter.
 *
 * **All-time only, and that is a gap rather than a choice.** `player_stat` is
 * the only table the profile endpoint reads, so the rank it publishes is the
 * all-time one. Showing it beside a weekly board would be a *wrong* number
 * rather than a missing one, so the strip simply does not appear on a windowed
 * view.
 */
function YouAreHere(props: {
  readonly stat: string;
  readonly period: string;
  readonly offset: number;
  readonly rows: readonly string[];
}) {
  const me = useStore($me);
  const onThisPage = props.rows.some((handle) => isMe(handle, me));
  const player = useResource(
    me === null || props.period !== ALLTIME || onThisPage ? null : `player:${me}`,
    (signal) => getPlayer(me ?? '', signal),
  );

  if (player.status !== 'ready') return null;
  const mine = player.data.stats.find((s) => s.stat === props.stat);
  if (mine === undefined) return null;
  const page = Math.floor((mine.rank - 1) / PAGE_SIZE) * PAGE_SIZE;
  if (page === props.offset) return null;

  return (
    <PanelFooter>
      <span>
        You are <span className="text-accent-text font-semibold tabular-nums">#{mine.rank}</span> of{' '}
        <span className="tabular-nums">{mine.players}</span> here.
      </span>
      <a
        href={hrefFor({ name: 'board', stat: props.stat, offset: page, period: ALLTIME })}
        className="text-accent-text hover:underline"
      >
        Go to your row
      </a>
    </PanelFooter>
  );
}
