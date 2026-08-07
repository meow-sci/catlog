import { cn } from '../cn.ts';
import { exactValue, formatValue } from '../units.ts';
import { RewoundMark } from './Pill.tsx';

/**
 * A number, rendered by `ui/units.ts` and carrying everything a reader or a test
 * needs to get back to the figure underneath it (§4.4).
 *
 * - the **unit is inside the string**, because that is the contract two
 *   implementations can be held to;
 * - `title` is the exact figure, which is how a reader recovers the digits
 *   `48 MJ` hides;
 * - `data-value` is the exact float **as the server sent it**. That is not
 *   decoration: a test that reconstructs a number by stripping non-digits out of
 *   rendered text breaks the moment a career-time board renders `5m 13s`, and
 *   the smoke script reads this attribute instead;
 * - `tabular-nums` and `whitespace-nowrap`, because this only ever appears in a
 *   table cell or a stat block — never in prose (§3).
 */
export function Value(props: {
  readonly value: number;
  readonly unit: string;
  readonly rewound?: boolean | undefined;
  readonly className?: string;
}) {
  return (
    <span
      title={exactValue(props.value, props.unit)}
      data-value={props.value}
      className={cn('tabular-nums whitespace-nowrap', props.className)}
    >
      {formatValue(props.value, props.unit)}
      {props.rewound === true && <RewoundMark />}
    </span>
  );
}
