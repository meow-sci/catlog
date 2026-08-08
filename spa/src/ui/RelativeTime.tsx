import { useStore } from '@nanostores/react';
import { $now, formatAgo, formatInstant, isoInstant } from './format.ts';

/**
 * "4m ago", as a leaf.
 *
 * This component subscribes to [$now] *itself*, and that placement is the whole
 * point: the 30-second tick re-renders exactly these `<time>` elements. When a
 * page read `$now` at the top and passed `now` into a table, the tick landed in
 * the RAC `TableBody` `dependencies` array and rebuilt every row of every table
 * twice a minute to change a few words.
 *
 * The absolute reading rides along for free: `dateTime` for machines, `title`
 * for a hover — the fixed-UTC instant, never the viewer's locale (§10).
 */
export function RelativeTime(props: { readonly at: number; readonly className?: string }) {
  const now = useStore($now);
  return (
    <time
      dateTime={isoInstant(props.at)}
      title={formatInstant(props.at)}
      className={props.className}
    >
      {formatAgo(props.at, now)}
    </time>
  );
}
