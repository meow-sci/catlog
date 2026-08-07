import type { ReactNode } from 'react';
import { cn } from '../cn.ts';

/**
 * The one container shape the whole app uses: 1 px border, 8 px radius,
 * `--color-panel`, `--shadow-panel`. There is no second card style, and adding
 * one is how a design system starts to look like three design systems.
 */
export function Panel(props: {
  readonly id?: string;
  readonly className?: string;
  readonly children: ReactNode;
  /**
   * Which board this panel is about.
   *
   * A named prop rather than a spread of arbitrary attributes: it is the one
   * hook the front page's preview panels need, and letting a container accept
   * any `data-*` is how internal ids end up on public markup by accident.
   */
  readonly stat?: string;
}) {
  return (
    <section
      id={props.id}
      data-stat={props.stat}
      className={cn('border-border bg-panel shadow-panel rounded-lg border', props.className)}
    >
      {props.children}
    </section>
  );
}

/**
 * A panel's heading strip.
 *
 * `--text-sm`/600 uppercase with `+0.04em` tracking — a **label**, not a
 * heading. §2.2's whole complaint about the old type scale was headings that
 * outsized the tables beneath them, and a panel title is the commonest way that
 * creeps back in.
 */
export function PanelHeader(props: {
  readonly title: ReactNode;
  readonly aside?: ReactNode;
  readonly className?: string;
}) {
  return (
    <div
      className={cn(
        'border-border flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2 border-b px-3 py-2',
        props.className,
      )}
    >
      <h2 className="text-fg-muted text-sm font-semibold tracking-[0.04em] uppercase">
        {props.title}
      </h2>
      {props.aside !== undefined && (
        <div className="text-fg-muted text-sm tabular-nums">{props.aside}</div>
      )}
    </div>
  );
}

/** Ordinary padded panel content, for anything that is not a table or a list. */
export function PanelBody(props: { readonly className?: string; readonly children: ReactNode }) {
  return <div className={cn('p-4', props.className)}>{props.children}</div>;
}

/** A strip below a panel's content — pagers, "you are here" markers, counts. */
export function PanelFooter(props: { readonly className?: string; readonly children: ReactNode }) {
  return (
    <div
      className={cn(
        'border-border bg-panel-sunken text-fg-muted flex flex-wrap items-center justify-between gap-3 border-t px-3 py-2 text-sm',
        props.className,
      )}
    >
      {props.children}
    </div>
  );
}
