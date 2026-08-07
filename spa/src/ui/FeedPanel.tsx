import { useStore } from '@nanostores/react';
import { Radio, RadioReceiver } from 'lucide-react';
import { $feed, withoutHandle } from '../state/feed.ts';
import { hrefFor } from '../state/router.ts';
import { $now, formatAgo } from './format.ts';
import { Empty, Loading, Panel, PanelHeader } from './kit.tsx';

const STATUS_LABEL: Record<string, string> = {
  connecting: 'connecting',
  live: 'live',
  offline: 'reconnecting',
  error: 'unavailable',
};

/**
 * The activity feed.
 *
 * Subscribing to `$feed` is the whole of the wiring: the store owns the
 * EventSource and opens it on the first subscriber, so mounting this component
 * anywhere — twice, even — costs one connection and no lifecycle code here.
 */
export function FeedPanel() {
  const feed = useStore($feed);
  const now = useStore($now);
  const isLive = feed.status === 'live';

  return (
    <Panel>
      <PanelHeader
        title="Live activity"
        aside={
          <span className="inline-flex items-center gap-1.5">
            {isLive ? (
              <Radio aria-hidden className="text-flare-400 size-3.5 animate-pulse" />
            ) : (
              <RadioReceiver aria-hidden className="size-3.5" />
            )}
            <span className={isLive ? 'text-flare-400' : undefined}>
              {STATUS_LABEL[feed.status] ?? feed.status}
            </span>
          </span>
        }
      />
      {feed.status === 'connecting' && feed.rows.length === 0 && <Loading label="Connecting…" />}
      {feed.status === 'error' && feed.rows.length === 0 && (
        <Empty>The activity feed is unavailable right now.</Empty>
      )}
      {feed.rows.length > 0 && (
        // aria-live so a screen reader hears new lines without the focus moving.
        // `polite`, not `assertive`: a leaderboard event is never urgent.
        <ul className="divide-ink-850 divide-y" aria-live="polite" aria-relevant="additions">
          {feed.rows.map((row) => (
            <li key={row.id} className="px-4 py-2 text-sm">
              <div className="flex items-baseline justify-between gap-3">
                <a
                  href={hrefFor({ name: 'player', handle: row.handle })}
                  className="text-ink-50 hover:text-flare-400 font-medium"
                >
                  {row.handle}
                </a>
                <time
                  dateTime={new Date(row.at).toISOString()}
                  className="text-ink-400 shrink-0 font-mono text-xs"
                >
                  {formatAgo(row.at, now)}
                </time>
              </div>
              <p className="text-ink-200 mt-0.5 text-[0.8rem] leading-snug">
                {withoutHandle(row.summary, row.handle)}
              </p>
            </li>
          ))}
        </ul>
      )}
      {feed.rows.length === 0 && feed.status !== 'connecting' && feed.status !== 'error' && (
        <Empty>Nothing has happened yet.</Empty>
      )}
    </Panel>
  );
}
