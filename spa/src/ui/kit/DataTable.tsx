import {
  Cell,
  type CellProps,
  Column,
  type ColumnProps,
  Row,
  type RowProps,
  Table,
  TableBody,
  type TableBodyProps,
  TableHeader,
  type TableHeaderProps,
  type TableProps,
} from 'react-aria-components';
import { cn } from '../cn.ts';

/**
 * The table.
 *
 * React Aria's `Table` rather than a bare `<table>`, and it renders exactly the
 * elements a bare one would — `<table><thead><tr><th>…</th></tr></thead>` — on
 * top of a real grid widget: arrow-key navigation between cells, a proper row
 * header for screen readers, and focus that survives the rows changing under it
 * during pagination. Rendering that by hand is where a hand-rolled leaderboard
 * quietly stops being usable without a mouse.
 *
 * Wrapped here rather than used raw in five pages so the boards, the comparison
 * and the raw event log are visibly one component with one set of tokens, and so
 * a change to row density is one edit.
 *
 * `className` is narrowed to a string. React Aria also accepts a function of the
 * render state, which is a genuinely useful escape hatch and exactly the kind of
 * thing that, used in five places, turns a kit back into scattered primitives.
 */
type Styled<P> = Omit<P, 'className'> & { readonly className?: string };

/**
 * A table scrolls **inside itself** rather than making the page scroll
 * sideways (§11) — a comparison of eight handles is wider than a phone and must
 * not take the rest of the page with it.
 *
 * The scroll lives on a wrapper rather than on the `<table>` itself. Putting
 * `display: block` on a table does make `overflow-x` apply, but it also stops
 * the table filling its container: the anonymous table box inside a block box is
 * sized to its content, so a three-column board ends up half the width of the
 * panel around it. The wrapper gets the same behaviour with none of that, and
 * §10 sanctions the two frontends differing in markup where they agree in
 * behaviour.
 */
export function DataTable(props: Styled<TableProps> & { readonly id?: string }) {
  const { className, ...rest } = props;
  return (
    <div className="w-full max-w-full overflow-x-auto">
      <Table
        {...rest}
        // Nothing on any table is selectable: these are records, not a work queue.
        selectionMode={rest.selectionMode ?? 'none'}
        className={cn('w-full border-collapse text-base', className)}
      />
    </div>
  );
}

export function HeadRow<T extends object>(props: Styled<TableHeaderProps<T>>) {
  const { className, ...rest } = props;
  return (
    <TableHeader
      {...rest}
      className={cn(
        'text-fg-muted border-border border-b text-sm font-semibold tracking-[0.04em] uppercase',
        className,
      )}
    />
  );
}

/**
 * A header cell.
 *
 * `align="end"` is the value columns: right-aligned, `tabular-nums`, `nowrap`.
 * The unit lives in the header as a *label of what the column is* while each
 * cell still carries its own rendered unit — not redundant for the scaling
 * quantities, since a length column legitimately mixes `999 m` and `1.82 Mm`.
 */
export function HeadCell(props: Styled<ColumnProps> & { readonly align?: 'start' | 'end' }) {
  const { className, align = 'start', ...rest } = props;
  return (
    <Column
      {...rest}
      className={cn(
        'px-3 py-2 font-semibold',
        align === 'end' ? 'text-right tabular-nums' : 'text-left',
        className,
      )}
    />
  );
}

export function TableRows<T extends object>(props: Styled<TableBodyProps<T>>) {
  const { className, ...rest } = props;
  return <TableBody {...rest} className={cn('divide-border divide-y', className)} />;
}

/**
 * One row.
 *
 * `isMe` paints the viewer's own row with `--color-wash-selected` and a left
 * accent rule — the same two signals the datastar site gets from
 * `tr[data-handle="…"]` and a `data-me` attribute on `<html>`.
 */
export function DataRow<T extends object>(
  props: Styled<RowProps<T>> & { readonly isMe?: boolean },
) {
  const { className, isMe = false, ...rest } = props;
  return (
    <Row
      {...rest}
      className={cn(
        'data-hovered:bg-wash-hover data-focus-visible:bg-wash-hover transition-colors duration-150',
        isMe && 'bg-wash-selected border-accent-text border-l-2',
        className,
      )}
    />
  );
}

export function DataCell(props: Styled<CellProps> & { readonly align?: 'start' | 'end' }) {
  const { className, align = 'start', ...rest } = props;
  return (
    <Cell
      {...rest}
      className={cn(
        'px-3 py-2 align-baseline',
        align === 'end' && 'text-right whitespace-nowrap tabular-nums',
        className,
      )}
    />
  );
}

/** The denser row padding the raw event log uses (`--row-py-dense`). */
export function DenseCell(props: Styled<CellProps> & { readonly align?: 'start' | 'end' }) {
  const { className, ...rest } = props;
  return <DataCell {...rest} className={cn('py-1', className)} />;
}
