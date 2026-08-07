import { cn } from '../cn.ts';
import { standing } from './standing.ts';

/**
 * A rank with its denominator, and a bar for how far up the board it is.
 *
 * `#3` on its own says nothing: third of four is not third of four thousand. The
 * profile endpoint publishes `players` for exactly this, so the SPA renders
 * `#3 of 41`.
 *
 * The two numbers do not count the same population — see `standing`, which is
 * where that is dealt with.
 */

/** Top 3 gets the accent, the top tenth gets full contrast, the rest is muted. */
function toneFor(rank: number, players: number): string {
  if (rank <= 3) return 'text-accent-text';
  if (standing(rank, players) >= 90) return 'text-fg';
  return 'text-fg-muted';
}

export function Rank(props: {
  readonly rank: number;
  readonly players: number;
  readonly className?: string;
}) {
  const share = standing(props.rank, props.players);
  return (
    <span className={cn('inline-flex items-center gap-2', props.className)}>
      <span
        className={cn('text-sm font-semibold tabular-nums', toneFor(props.rank, props.players))}
      >
        #{props.rank}
      </span>
      <span className="text-fg-muted text-xs tabular-nums">of {props.players}</span>
      <span
        // A bar, not a widget: it restates the two numbers next to it and adds
        // nothing a screen reader has not already been told.
        aria-hidden
        className="bg-panel-sunken border-border h-1.5 w-16 shrink-0 overflow-hidden rounded-full border"
      >
        <span
          className={cn('block h-full', props.rank <= 3 ? 'bg-accent' : 'bg-border-strong')}
          style={{ width: `${String(share)}%` }}
        />
      </span>
    </span>
  );
}
