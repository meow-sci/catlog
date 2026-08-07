import { useStore } from '@nanostores/react';
import { Radio, RadioReceiver } from 'lucide-react';
import { $feed, withoutHandle } from '../state/feed.ts';
import { hrefFor } from '../state/router.ts';
import { cn } from './cn.ts';
import { $now, formatAgo, isoInstant } from './format.ts';
import { Empty, Loading, Panel, PanelHeader } from './kit/index.ts';

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
 * EventSource and opens it on the first subscriber, so mounting this anywhere —
 * twice, even — costs one connection and no lifecycle code here.
 *
 * The summaries are **prose**, so they get no tabular figures: "lithobraked at
 * 214 m/s" is a sentence, and tabular digits inside a sentence look like a
 * ransom note. The timestamps beside them are not prose and do (§3).
 */
export function FeedPanel() {
  const feed = useStore($feed);
  const now = useStore($now);
  const isLive = feed.status === 'live';

  return (
    <Panel id="feed-panel">
      <PanelHeader
        title="Live activity"
        aside={
          <span className="inline-flex items-center gap-1.5">
            {isLive ? (
              <Radio aria-hidden className="text-accent-text size-3.5 animate-pulse" />
            ) : (
              <RadioReceiver aria-hidden className="size-3.5" />
            )}
            <span className={isLive ? 'text-accent-text' : undefined}>
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
        <ul
          id="feed"
          data-source={isLive ? 'sse' : 'snapshot'}
          data-count={feed.rows.length}
          className="divide-border divide-y"
          aria-live="polite"
          aria-relevant="additions"
        >
          {feed.rows.map((row) => (
            <li
              key={row.id}
              className={cn(
                'feed-item px-3 py-2 text-sm',
                // One of exactly two things that move on this site, and it is
                // wrapped in `prefers-reduced-motion` in index.css.
                feed.arrived.includes(row.id) && 'catlog-arrive',
              )}
              data-feed-id={row.id}
              data-type={row.type}
            >
              <div className="flex items-baseline justify-between gap-3">
                <a
                  href={hrefFor({ name: 'player', handle: row.handle })}
                  className="text-fg hover:text-accent-text font-medium"
                >
                  {row.handle}
                </a>
                <time
                  dateTime={isoInstant(row.at)}
                  className="text-fg-muted shrink-0 text-xs tabular-nums"
                >
                  {formatAgo(row.at, now)}
                </time>
              </div>
              {/* Prose: no tabular figures. `withoutHandle` drops the leading
                  handle the server composed, because it is already a link above
                  — the server writes complete sentences because the
                  server-rendered panel shows nothing else. */}
              <p className="text-fg-muted mt-0.5 leading-snug">
                {withoutHandle(row.summary, row.handle)}
              </p>
            </li>
          ))}
        </ul>
      )}
      {feed.rows.length === 0 && feed.status !== 'connecting' && feed.status !== 'error' && (
        <Empty>Nothing has happened yet. Fly something.</Empty>
      )}
    </Panel>
  );
}
