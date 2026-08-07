import { useStore } from '@nanostores/react';
import { getPlayer } from '../api/client.ts';
import { useResource } from '../api/useResource.ts';
import { $me, $meDismissed, clearMe, dismissMeNotice } from '../state/me.ts';
import { ALLTIME, hrefFor, PAGE_SIZE } from '../state/router.ts';
import { $now, formatAgo } from './format.ts';
import { Button, Loading, Panel, PanelBody, PanelHeader, Rank, Value } from './kit/index.ts';

/** How many of a player's best placements the front page shows. */
const BEST = 3;

/**
 * The front page's personal card: where the viewer stands, above the fold.
 *
 * Absent — not empty, not a spinner — when there is no "me" handle. A
 * personalised panel that renders itself as a placeholder is worse than one that
 * is simply not there (§7.1).
 */
export function YourStanding() {
  const me = useStore($me);
  const dismissed = useStore($meDismissed);
  const now = useStore($now);
  const player = useResource(me === null ? null : `player:${me}`, (signal) =>
    getPlayer(me ?? '', signal),
  );

  if (me === null) return null;

  if (player.status === 'error') {
    // §7.1, and the distinction is the whole of it:
    //
    //  - a **404** means catlog has no public profile for this handle. Unknown,
    //    retired and banned are one answer on purpose, so the notice repeats the
    //    API's silence and guesses at nothing. It never auto-clears: the stored
    //    value is the user's data, and a 404 during an incident, a rebuild, or a
    //    moderation action that gets reversed must not silently erase it.
    //  - **anything else**, including `status === 0` — offline, DNS, a refused
    //    CORS preflight — shows nothing at all. A network blip is not news about
    //    a handle.
    if (!player.error.notFound) return null;
    if (dismissed.includes(me.toLowerCase())) return null;
    return (
      <Panel>
        <PanelHeader title="Your standing" />
        <PanelBody className="space-y-3">
          <p className="text-fg">
            catlog has no public profile for <span className="font-medium">{me}</span> any more.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              onPress={() => {
                dismissMeNotice(me);
              }}
            >
              Keep it
            </Button>
            <Button variant="ghost" onPress={clearMe}>
              Forget it
            </Button>
          </div>
        </PanelBody>
      </Panel>
    );
  }

  if (player.status === 'loading') {
    return (
      <Panel>
        <PanelHeader title="Your standing" />
        <Loading label="Loading your standing…" />
      </Panel>
    );
  }
  if (player.status !== 'ready') return null;

  const stats = player.data.stats;
  // "Best" is by how far up the board the row sits, not by rank alone: #4 of 5
  // is not a better story than #12 of 900.
  const best = [...stats]
    .sort((a, b) => (a.rank - 1) / Math.max(a.players, 1) - (b.rank - 1) / Math.max(b.players, 1))
    .slice(0, BEST);
  const latest = stats.reduce((newest, s) => Math.max(newest, s.updated), 0);

  return (
    <Panel>
      <PanelHeader
        title="Your standing"
        aside={
          <a
            href={hrefFor({ name: 'player', handle: player.data.handle })}
            className="text-accent-text hover:underline"
          >
            {player.data.handle}
          </a>
        }
      />
      {best.length === 0 ? (
        <PanelBody>
          <p className="text-fg-muted text-sm">You are not on any board yet. Fly something.</p>
        </PanelBody>
      ) : (
        <ul className="divide-border divide-y">
          {best.map((stat) => (
            <li key={stat.stat} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2">
              <a
                href={hrefFor({
                  name: 'board',
                  stat: stat.stat,
                  // Straight to the page this rank is on, so "see where I sit"
                  // is one click and needs no new endpoint.
                  offset: Math.floor((stat.rank - 1) / PAGE_SIZE) * PAGE_SIZE,
                  period: ALLTIME,
                })}
                className="text-fg hover:text-accent-text min-w-0 flex-1 font-medium"
              >
                {stat.title}
              </a>
              <Rank rank={stat.rank} players={stat.players} />
              <Value
                value={stat.value}
                unit={stat.unit}
                rewound={stat.rewound}
                className="text-fg w-28 text-right font-medium"
              />
            </li>
          ))}
        </ul>
      )}
      {latest > 0 && (
        <p className="border-border text-fg-muted border-t px-3 py-2 text-sm">
          Last record {formatAgo(latest, now)}.
        </p>
      )}
    </Panel>
  );
}
