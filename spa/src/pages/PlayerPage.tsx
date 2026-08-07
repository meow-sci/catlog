import { useStore } from '@nanostores/react';
import { ArrowLeft, ScrollText, SearchX, Users } from 'lucide-react';
import { getPlayer } from '../api/client.ts';
import type { PlayerStat } from '../api/types.ts';
import { useResource } from '../api/useResource.ts';
import { $me, clearMe, isMe, setMe } from '../state/me.ts';
import { ALLTIME, hrefFor, PAGE_SIZE } from '../state/router.ts';
import { describeContext, hasContext } from '../ui/context.ts';
import { $now, formatAgo, formatInstant } from '../ui/format.ts';
import {
  DataCell,
  DataRow,
  DataTable,
  Details,
  Empty,
  Failure,
  HeadCell,
  HeadRow,
  Json,
  LinkButton,
  Loading,
  Panel,
  PanelHeader,
  Rank,
  TableRows,
  ToggleButton,
  Value,
} from '../ui/kit/index.ts';

/**
 * One player: every placement, every rank *and its denominator*, and a way to
 * start comparing.
 */
export function PlayerPage(props: { readonly handle: string }) {
  const { handle } = props;
  const player = useResource(`player:${handle}`, (signal) => getPlayer(handle, signal));
  const me = useStore($me);
  const now = useStore($now);
  const mine = isMe(handle, me);

  // §4.8 answers 404 for unknown, retired and banned handles identically, on
  // purpose — telling them apart would make this endpoint a ban oracle. So the
  // UI says exactly what the API said and no more. A funnier line that implied a
  // ban would be a lie.
  if (player.status === 'error' && player.error.notFound) {
    return (
      <div className="space-y-5">
        <BackLink />
        <Panel className="flex flex-col items-center gap-3 px-4 py-16 text-center">
          <SearchX aria-hidden className="text-fg-subtle size-8" />
          <h1>Nothing here</h1>
          <p className="text-fg-muted max-w-[65ch]">
            catlog has no public profile for <span className="text-fg font-medium">{handle}</span>.
          </p>
        </Panel>
      </div>
    );
  }

  const stats = player.status === 'ready' ? player.data.stats : [];

  return (
    <div className="space-y-5">
      <BackLink />
      <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div>
          <h1 id="profile-handle" data-handle={handle}>
            {handle}
          </h1>
          {player.status === 'ready' && (
            <p className="text-fg-muted mt-1 text-sm">
              Handle claimed {formatInstant(player.data.since)}.
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/*
           * "This is me" is the whole of the personalisation story: no account,
           * no session, one localStorage key. Every public read response is
           * `s-maxage=30` to a shared cache, so there is no server-rendered
           * personalisation available to either frontend even in principle.
           */}
          <ToggleButton
            isSelected={mine}
            onChange={(selected) => {
              if (selected) setMe(handle);
              else clearMe();
            }}
            variant={mine ? 'primary' : 'secondary'}
          >
            {mine ? 'This is you' : 'This is me'}
          </ToggleButton>
          <LinkButton href={hrefFor({ name: 'compare', handles: compareWith(handle, me) })}>
            <Users aria-hidden className="size-3.5" />
            Compare
          </LinkButton>
          <LinkButton href={hrefFor({ name: 'playerEvents', handle })} variant="ghost">
            <ScrollText aria-hidden className="size-3.5" />
            Raw events
          </LinkButton>
        </div>
      </header>

      <Panel>
        <PanelHeader
          title="Placements"
          aside={
            player.status === 'ready'
              ? `${String(stats.length)} board${stats.length === 1 ? '' : 's'}`
              : undefined
          }
        />
        {player.status === 'loading' && <Loading label="Loading profile…" />}
        {player.status === 'error' && <Failure what="load this profile" error={player.error} />}
        {player.status === 'ready' && stats.length === 0 && (
          <Empty>This player is not on any board yet.</Empty>
        )}
        {player.status === 'ready' && stats.length > 0 && (
          <DataTable aria-label={`Placements for ${handle}`} id="profile-stats">
            <HeadRow>
              <HeadCell id="board" isRowHeader>
                Board
              </HeadCell>
              <HeadCell id="rank">Rank</HeadCell>
              <HeadCell id="value" align="end">
                Value
              </HeadCell>
              <HeadCell id="updated" align="end" className="hidden md:table-cell">
                Updated
              </HeadCell>
            </HeadRow>
            <TableRows items={stats.map((s) => ({ ...s, id: s.stat }))} dependencies={[now]}>
              {(stat: PlayerStat & { id: string }) => (
                <DataRow id={stat.stat} data-stat={stat.stat} data-rank={stat.rank}>
                  <DataCell>
                    <a
                      // Straight to the page containing this rank, so "see where
                      // I sit" is one click and needs no new endpoint.
                      href={hrefFor({
                        name: 'board',
                        stat: stat.stat,
                        offset: Math.floor((stat.rank - 1) / PAGE_SIZE) * PAGE_SIZE,
                        period: ALLTIME,
                      })}
                      className="text-fg hover:text-accent-text font-medium"
                    >
                      {stat.title}
                    </a>
                    <span className="text-fg-muted block text-sm">
                      {stat.ascending ? 'Lowest wins.' : 'Highest wins.'}
                    </span>
                    {hasContext(stat.context) && <ContextDetails context={stat.context} />}
                  </DataCell>
                  <DataCell>
                    <Rank rank={stat.rank} players={stat.players} />
                  </DataCell>
                  <DataCell
                    align="end"
                    className="value text-fg font-medium"
                    data-value={stat.value}
                  >
                    <Value value={stat.value} unit={stat.unit} rewound={stat.rewound} />
                  </DataCell>
                  <DataCell align="end" className="text-fg-muted hidden text-sm md:table-cell">
                    {formatAgo(stat.updated, now)}
                  </DataCell>
                </DataRow>
              )}
            </TableRows>
          </DataTable>
        )}
      </Panel>

      {player.status === 'ready' && stats.length > 0 && (
        <p className="text-fg-muted max-w-[65ch] text-sm">
          Ranks skip nobody: a banned account is removed from the board rather than leaving a hole
          in the numbering. The "of" figure counts every row a board holds, so a rank can be better
          than it implies and never worse.
        </p>
      )}
    </div>
  );
}

/** The handles a "Compare" button should start from: this one, and yours if it is not this one. */
function compareWith(handle: string, me: string | null): string[] {
  if (me === null || isMe(handle, me)) return [handle];
  return [me, handle];
}

function ContextDetails(props: { readonly context: unknown }) {
  const pairs = describeContext(props.context);
  return (
    <div className="mt-1">
      {pairs.length > 0 && (
        <span className="text-fg-muted flex flex-wrap gap-x-3 text-sm">
          {pairs.map((pair) => (
            <span key={pair.key}>
              <span className="text-fg-subtle">{pair.key} </span>
              <span className="tabular-nums">{pair.value}</span>
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

function BackLink() {
  return (
    <a
      href={hrefFor({ name: 'boards' })}
      className="text-fg-muted hover:text-fg inline-flex items-center gap-1 text-sm"
    >
      <ArrowLeft aria-hidden className="size-3.5" />
      All boards
    </a>
  );
}
