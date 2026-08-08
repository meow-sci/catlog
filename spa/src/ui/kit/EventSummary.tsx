import { payloadFieldCount, summarizeEvent } from './summarize.ts';

/**
 * The one-line payload summary an event-log row carries.
 *
 * A thin renderer over `summarize.ts`, which owns the allow-list and the
 * reasoning (an unknown type or key renders as a field count, never as its
 * values — the same defensive posture as `CONTEXT_KEYS`). Split into two
 * files so this one exports only a component, per the fast-refresh rule.
 */
export function EventSummary(props: { readonly type: string; readonly payload: unknown }) {
  const pairs = summarizeEvent(props.type, props.payload);
  if (pairs.length === 0) {
    const count = payloadFieldCount(props.payload);
    return (
      <span className="text-fg-subtle text-sm">
        {count === 0 ? '—' : `${String(count)} field${count === 1 ? '' : 's'}`}
      </span>
    );
  }
  return (
    <span className="text-fg-muted truncate text-sm">
      {pairs.map((pair, i) => (
        <span key={pair.key} className="whitespace-nowrap">
          {i > 0 && <span className="text-fg-subtle"> · </span>}
          <span className="text-fg-subtle">{pair.key.replaceAll('_', ' ')} </span>
          <span className="text-fg">{pair.value}</span>
        </span>
      ))}
    </span>
  );
}
