import { AlertTriangle, Loader2 } from 'lucide-react';
import type { ReactNode } from 'react';
import type { ApiError } from '../api/client.ts';
import { cn } from './cn.ts';

/** A bordered card. The one container shape the whole app uses. */
export function Panel(props: { readonly className?: string; readonly children: ReactNode }) {
  return (
    <section
      className={cn(
        'border-ink-800 bg-ink-900/60 rounded-lg border shadow-sm shadow-black/20',
        props.className,
      )}
    >
      {props.children}
    </section>
  );
}

/** A panel's heading strip, optionally with something aligned to the right. */
export function PanelHeader(props: {
  readonly title: ReactNode;
  readonly aside?: ReactNode;
  readonly className?: string;
}) {
  return (
    <div
      className={cn(
        'border-ink-800 flex items-baseline justify-between gap-4 border-b px-4 py-3',
        props.className,
      )}
    >
      <h2 className="text-ink-50 text-sm font-semibold tracking-wide uppercase">{props.title}</h2>
      {props.aside !== undefined && <div className="text-ink-400 text-xs">{props.aside}</div>}
    </div>
  );
}

/** A small labelled chip: units, counts, statuses. */
export function Pill(props: {
  readonly children: ReactNode;
  readonly className?: string;
  readonly title?: string;
}) {
  return (
    <span
      title={props.title}
      className={cn(
        'bg-ink-850 text-ink-400 border-ink-800 rounded-full border px-2 py-0.5 font-mono text-[0.7rem]',
        props.className,
      )}
    >
      {props.children}
    </span>
  );
}

/**
 * The loading state.
 *
 * `<output>` rather than a `<div role="status">`: it carries the same implicit
 * live-region semantics natively, and the text label next to the spinner is what
 * makes it mean anything to a screen reader — a bare spinning icon is decoration.
 */
export function Loading(props: { readonly label: string }) {
  return (
    <output className="text-ink-400 flex items-center gap-2 px-4 py-8 text-sm">
      <Loader2 aria-hidden className="size-4 animate-spin" />
      {props.label}
    </output>
  );
}

/**
 * The failure state for a read that could not be completed.
 *
 * It shows the server's own `detail` (§4.9) rather than a generic apology: when
 * this is a CORS refusal or a stopped server, the difference between "failed to
 * fetch" and a 500 is the entire diagnosis.
 */
export function Failure(props: { readonly what: string; readonly error: ApiError }) {
  return (
    <div role="alert" className="flex items-start gap-3 px-4 py-8 text-sm">
      <AlertTriangle aria-hidden className="text-flare-500 mt-0.5 size-4 shrink-0" />
      <div>
        <p className="text-ink-200">Could not {props.what}.</p>
        <p className="text-ink-400 mt-1 font-mono text-xs">
          {props.error.status > 0 ? `${String(props.error.status)} ` : ''}
          {props.error.code}: {props.error.message}
        </p>
      </div>
    </div>
  );
}

/** An empty result that is not an error. */
export function Empty(props: { readonly children: ReactNode }) {
  return <p className="text-ink-400 px-4 py-8 text-sm">{props.children}</p>;
}
