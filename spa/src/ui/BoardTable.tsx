import { useStore } from '@nanostores/react';
import { Cell, Column, Row, Table, TableBody, TableHeader } from 'react-aria-components';
import type { BoardRow } from '../api/types.ts';
import { hrefFor } from '../state/router.ts';
import { $now, exactValue, formatAgo, formatValue } from './format.ts';

/**
 * A leaderboard as a table.
 *
 * React Aria's `Table` rather than a bare `<table>`: it is a real grid widget —
 * arrow-key navigation between cells, a proper row header for screen readers,
 * focus management that survives the rows changing under it during pagination.
 * Rendering that by hand is where a hand-rolled leaderboard quietly stops being
 * usable without a mouse.
 */
export function BoardTable(props: {
  readonly unit: string;
  readonly rows: readonly BoardRow[];
  /**
   * The smallest value ranks first.
   *
   * Published by the server per board (§4.8) and never inferred here: the
   * career-time boards are seconds since a career began, and a table that
   * presented the fastest ascent as though it were the worst one would be a
   * wrong answer rather than a styling gap.
   */
  readonly ascending: boolean;
  readonly showContext?: boolean;
}) {
  const now = useStore($now);
  const anyContext = props.showContext !== false && props.rows.some((r) => r.context !== undefined);
  const direction = props.ascending ? 'lowest first' : 'highest first';

  return (
    <Table aria-label="Leaderboard" className="w-full border-collapse text-sm" selectionMode="none">
      <TableHeader className="text-ink-400 text-xs tracking-wide uppercase">
        <Column id="rank" className="w-14 px-4 py-2 text-right font-medium">
          #
        </Column>
        <Column id="handle" isRowHeader className="px-4 py-2 text-left font-medium">
          Player
        </Column>
        <Column id="value" className="px-4 py-2 text-right font-medium">
          <span title={`Ranked ${direction}`}>
            {props.unit === '' ? 'Value' : props.unit}{' '}
            <span aria-hidden>{props.ascending ? '↑' : '↓'}</span>
            <span className="sr-only"> — ranked {direction}</span>
          </span>
        </Column>
        {anyContext && (
          <Column id="context" className="hidden px-4 py-2 text-left font-medium sm:table-cell">
            Where
          </Column>
        )}
        <Column id="updated" className="hidden px-4 py-2 text-right font-medium md:table-cell">
          Updated
        </Column>
      </TableHeader>
      <TableBody
        items={props.rows}
        dependencies={[now, anyContext]}
        className="divide-ink-850 divide-y"
      >
        {(row: BoardRow) => (
          <Row
            id={`${String(row.rank)}:${row.handle}`}
            className="data-focus-visible:bg-ink-850 data-hovered:bg-ink-900 transition-colors"
          >
            <Cell className="text-ink-400 px-4 py-2 text-right font-mono tabular-nums">
              {row.rank}
            </Cell>
            <Cell className="px-4 py-2">
              <a
                href={hrefFor({ name: 'player', handle: row.handle })}
                className="text-ink-50 hover:text-flare-400 font-medium"
              >
                {row.handle}
              </a>
            </Cell>
            <Cell className="text-flare-400 px-4 py-2 text-right font-mono tabular-nums">
              {/* The compacted value is what fits; the exact one is one hover away. */}
              <span title={exactValue(row.value, props.unit)}>
                {formatValue(row.value, props.unit)}
              </span>
              {/* A career whose save was rewound qualifies the number and does
                  nothing else: the row is ranked normally (§4.1). */}
              {row.rewound === true && (
                <span
                  className="text-ink-400 ml-1 cursor-help"
                  title="An earlier save of this career was loaded, so its clock did not only run forwards."
                >
                  <span aria-hidden>†</span>
                  <span className="sr-only"> (career rewound)</span>
                </span>
              )}
            </Cell>
            {anyContext && (
              <Cell className="text-ink-400 hidden px-4 py-2 font-mono text-xs sm:table-cell">
                {describeContext(row.context)}
              </Cell>
            )}
            <Cell className="text-ink-400 hidden px-4 py-2 text-right text-xs whitespace-nowrap md:table-cell">
              {formatAgo(row.updated, now)}
            </Cell>
          </Row>
        )}
      </TableBody>
    </Table>
  );
}

/**
 * Renders the interesting part of a row's `context`.
 *
 * `context` is documented as free-form, board-specific JSON, so this reads the
 * one key that means the same thing on every board that has it (`body`) and
 * otherwise says nothing. Guessing at the rest would be inventing a schema the
 * server does not promise.
 */
function describeContext(context: unknown): string {
  if (typeof context !== 'object' || context === null) return '';
  const body = (context as { body?: unknown }).body;
  return typeof body === 'string' ? body : '';
}
