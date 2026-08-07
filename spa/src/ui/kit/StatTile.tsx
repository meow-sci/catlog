import type { ReactNode } from 'react';
import { cn } from '../cn.ts';

/**
 * One global figure: a label, a number, and a line saying what it counts.
 *
 * The value is `--text-lg` (18 px) — the largest anything gets outside an `h1` —
 * and `tabular-nums`, because tiles sit in a row and a row of numbers that do
 * not line up is worse than no row.
 */
export function StatTile(props: {
  readonly label: string;
  readonly value: ReactNode;
  readonly note?: ReactNode;
  readonly className?: string;
}) {
  return (
    <div
      className={cn(
        'border-border bg-panel shadow-panel flex flex-col gap-0.5 rounded-lg border px-3 py-2',
        props.className,
      )}
    >
      <span className="text-fg-muted text-xs font-semibold tracking-[0.04em] uppercase">
        {props.label}
      </span>
      <span className="text-fg text-lg font-semibold tabular-nums">{props.value}</span>
      {props.note !== undefined && <span className="text-fg-muted text-xs">{props.note}</span>}
    </div>
  );
}
