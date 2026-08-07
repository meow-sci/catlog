import { useStore } from '@nanostores/react';
import { ArrowLeft, SearchX } from 'lucide-react';
import { getPlayer } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { hrefFor } from '../state/router.ts';
import { $now, exactValue, formatAgo, formatInstant, formatValue } from '../ui/format.ts';
import { Empty, Failure, Loading, Panel, PanelHeader, Pill } from '../ui/kit.tsx';

/** One player's placements across every board. */
export function PlayerPage(props: { readonly handle: string }) {
  const { handle } = props;
  const player = useResource(`player:${handle}`, (signal) => getPlayer(handle, signal));
  const now = useStore($now);

  // §4.8 answers 404 for unknown, retired and banned handles identically, on
  // purpose — telling them apart would make this endpoint a ban oracle. So the
  // UI says exactly what the API said and no more.
  if (player.status === 'error' && player.error.notFound) {
    return (
      <div className="space-y-6">
        <BackLink />
        <Panel className="flex flex-col items-center gap-3 px-4 py-16 text-center">
          <SearchX aria-hidden className="text-ink-700 size-8" />
          <h1 className="text-ink-50 text-xl font-semibold">No such player</h1>
          <p className="text-ink-400 max-w-md text-sm">
            catlog has no public profile for{' '}
            <span className="text-ink-200 font-mono">{handle}</span>.
          </p>
        </Panel>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <BackLink />
      <header>
        <h1 className="text-ink-50 font-mono text-2xl font-semibold">{handle}</h1>
        {player.status === 'ready' && (
          <p className="text-ink-400 mt-1 text-sm">
            Handle claimed {formatInstant(player.data.since)}
          </p>
        )}
      </header>

      <Panel>
        <PanelHeader
          title="Placements"
          aside={
            player.status === 'ready'
              ? `${String(player.data.stats.length)} board${player.data.stats.length === 1 ? '' : 's'}`
              : undefined
          }
        />
        {player.status === 'loading' && <Loading label="Loading profile…" />}
        {player.status === 'error' && <Failure what="load this profile" error={player.error} />}
        {player.status === 'ready' && player.data.stats.length === 0 && (
          <Empty>This player is not on any board yet.</Empty>
        )}
        {player.status === 'ready' && player.data.stats.length > 0 && (
          <ul className="divide-ink-850 divide-y">
            {player.data.stats.map((stat) => (
              <li key={stat.stat} className="flex items-center gap-4 px-4 py-3">
                <span className="min-w-0 flex-1">
                  <a
                    href={hrefFor({ name: 'board', stat: stat.stat, offset: 0 })}
                    className="text-ink-50 hover:text-flare-400 block font-medium"
                  >
                    {stat.title}
                  </a>
                  <span className="text-ink-400 block text-xs">
                    updated {formatAgo(stat.updated, now)}
                  </span>
                </span>
                <Pill className="shrink-0">rank #{stat.rank}</Pill>
                <span
                  className="text-flare-400 w-32 shrink-0 text-right font-mono text-sm tabular-nums"
                  title={exactValue(stat.value, stat.unit)}
                >
                  {formatValue(stat.value, stat.unit)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}

function BackLink() {
  return (
    <a
      href={hrefFor({ name: 'boards' })}
      className="text-ink-400 hover:text-ink-200 inline-flex items-center gap-1 text-xs"
    >
      <ArrowLeft aria-hidden className="size-3.5" />
      All boards
    </a>
  );
}
