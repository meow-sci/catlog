import { AlertTriangle, Loader2 } from 'lucide-react';
import type { ReactNode } from 'react';
import type { ApiError } from '../../api/client.ts';
import { cn } from '../cn.ts';

/**
 * The loading state.
 *
 * `<output>` rather than a `<div role="status">`: it carries the same implicit
 * live-region semantics natively, and the text label next to the spinner is what
 * makes it mean anything to a screen reader — a bare spinning icon is
 * decoration.
 *
 * §9.2 rule 7: **empty states get the whimsy; loading states get nothing.**
 * "Loading boards…" is correct and complete. A spinner with a joke is a spinner
 * you read twice.
 */
export function Loading(props: { readonly label: string; readonly className?: string }) {
  return (
    <output
      className={cn('text-fg-muted flex items-center gap-2 px-4 py-8 text-sm', props.className)}
    >
      <Loader2 aria-hidden className="size-4 animate-spin" />
      {props.label}
    </output>
  );
}

/**
 * A read that could not be completed.
 *
 * It shows the server's own `detail` and status rather than a generic apology,
 * and that is load-bearing (§9.3): when this is a CORS refusal or a stopped
 * server, the difference between "failed to fetch" and a 500 is the entire
 * diagnosis. Never replace a diagnosable message with something friendlier.
 */
export function Failure(props: {
  readonly what: string;
  readonly error: ApiError;
  readonly className?: string;
}) {
  return (
    <div role="alert" className={cn('flex items-start gap-3 px-4 py-8 text-sm', props.className)}>
      <AlertTriangle aria-hidden className="text-danger mt-0.5 size-4 shrink-0" />
      <div>
        <p className="text-fg">Could not {props.what}.</p>
        <p className="text-fg-muted mt-1 font-mono text-xs">
          {props.error.status > 0 ? `${String(props.error.status)} ` : ''}
          {props.error.code}: {props.error.message}
        </p>
      </div>
    </div>
  );
}

/**
 * An empty result that is not an error — and the best place on the site for the
 * writing. "Nobody has scored here yet." "Nothing has happened yet. Fly
 * something."
 */
export function Empty(props: { readonly children: ReactNode; readonly className?: string }) {
  return <p className={cn('text-fg-muted px-4 py-8 text-sm', props.className)}>{props.children}</p>;
}
