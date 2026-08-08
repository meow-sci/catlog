import { ArrowUp, Radio } from 'lucide-react';
import { cn } from './cn.ts';
import { Button, ToggleButton } from './kit/index.ts';
import type { TailStatus } from './useEventTail.ts';

/**
 * The live-tail controls both event-log pages share: the toggle, the
 * connection status, and the unobtrusive "N new events" flush button that
 * appears while arrivals are buffered (paused-while-scrolled).
 *
 * The 429 at the server's subscriber cap surfaces here as `unavailable` with
 * retry messaging — a status line, never an error page: the paginated log
 * underneath is still perfectly readable.
 */
export function TailControls(props: {
  readonly enabled: boolean;
  readonly onToggle: (enabled: boolean) => void;
  readonly status: TailStatus;
  readonly pending: number;
  /** Flush the buffered rows and put the reader back at the head. */
  readonly onShowNew: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <ToggleButton isSelected={props.enabled} onChange={props.onToggle}>
        <Radio aria-hidden className="size-3.5" />
        Live tail
      </ToggleButton>
      {props.enabled && <TailStatusText status={props.status} />}
      {props.pending > 0 && (
        <Button variant="primary" onPress={props.onShowNew} className="min-h-0 px-2.5 py-1">
          <ArrowUp aria-hidden className="size-3.5" />
          {props.pending} new event{props.pending === 1 ? '' : 's'}
        </Button>
      )}
    </div>
  );
}

const STATUS_TEXT: Record<TailStatus, string> = {
  off: '',
  connecting: 'connecting…',
  live: 'live',
  reconnecting: 'connection lost — reconnecting…',
  // The subscriber-cap 429, most likely. Retried automatically; say so.
  unavailable: 'stream is full or unreachable — retrying…',
};

function TailStatusText(props: { readonly status: TailStatus }) {
  if (props.status === 'off') return null;
  return (
    <output
      className={cn(
        'flex items-center gap-1.5 text-sm',
        props.status === 'live' ? 'text-fg-muted' : 'text-fg-subtle',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'size-2 rounded-full',
          props.status === 'live' ? 'bg-accent' : 'bg-border-strong',
        )}
      />
      {STATUS_TEXT[props.status]}
    </output>
  );
}
