import type { ReactNode } from 'react';
import { cn } from '../cn.ts';

/**
 * A small labelled chip: units, counts, statuses, board direction.
 *
 * `tabular-nums` because a pill almost always holds a number and pills sit in
 * columns; `slashed-zero` because the ones that hold a token — a relabelled
 * career id, a bucket name — are Crockford base32 in a sans face, where `0` and
 * `O` are otherwise a coin toss (§3).
 */
export function Pill(props: {
  readonly children: ReactNode;
  readonly className?: string;
  readonly title?: string;
  readonly tone?: 'neutral' | 'accent';
}) {
  return (
    <span
      title={props.title}
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs whitespace-nowrap tabular-nums',
        props.tone === 'accent'
          ? 'border-accent-text/40 text-accent-text bg-wash-selected'
          : 'border-border bg-panel-sunken text-fg-muted',
        props.className,
      )}
    >
      {props.children}
    </span>
  );
}

/**
 * A token rendered in the sans face — a relabelled career id, a bucket name.
 *
 * `slashed-zero` for the reason §3 gives: inside `<code>` the mono font already
 * distinguishes `0` from `O`, and outside it nothing does.
 */
export function Token(props: { readonly children: ReactNode; readonly title?: string }) {
  return (
    <span title={props.title} className="text-fg-muted text-xs tabular-nums slashed-zero">
      {props.children}
    </span>
  );
}

/**
 * The rewind dagger.
 *
 * It qualifies a number and does nothing else: the row ranks normally and the
 * player is treated no differently (§4.1). The tooltip is the exact sentence
 * both frontends use, and the `sr-only` text is what makes the mark mean
 * anything to a reader who cannot see a dagger.
 */
export function RewoundMark() {
  return (
    <span
      className="text-fg-muted ml-1 cursor-help"
      title="An earlier save of this career was loaded, so its clock did not only run forwards."
    >
      <span aria-hidden>†</span>
      <span className="sr-only"> (career rewound)</span>
    </span>
  );
}
