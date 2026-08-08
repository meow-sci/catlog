import { ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { Button, Disclosure, DisclosurePanel, Heading } from 'react-aria-components';
import { cn } from '../cn.ts';

/**
 * A disclosure: the full `context` blob on a board row, the pretty-printed
 * payload on a raw event.
 *
 * React Aria's `Disclosure` rather than `<details>` — it wires `aria-expanded`
 * and `aria-controls` between a real button and the panel, keeps the panel in
 * the accessibility tree only while it is open, and does not inherit `<summary>`
 * 's list-marker and focus quirks.
 *
 * `Heading` wraps the trigger so the disclosure is a landmark a screen reader
 * can jump between when a table has thirty of them.
 */
export function Details(props: {
  readonly summary: ReactNode;
  readonly children: ReactNode;
  readonly className?: string;
  readonly headingLevel?: 3 | 4;
}) {
  return (
    <Disclosure className={cn('min-w-0', props.className)}>
      <Heading level={props.headingLevel ?? 4}>
        <Button
          slot="trigger"
          className="text-fg-muted data-hovered:text-fg group inline-flex cursor-pointer items-center gap-1 rounded-sm text-sm transition-colors duration-150"
        >
          <ChevronRight
            aria-hidden
            className="size-3.5 transition-transform duration-150 group-data-expanded:rotate-90"
          />
          {props.summary}
        </Button>
      </Heading>
      <DisclosurePanel className="pt-2">{props.children}</DisclosurePanel>
    </Disclosure>
  );
}

/**
 * A JSON blob, pretty-printed.
 *
 * **Raw** on purpose in the event log: this is the view where a reader wants
 * `7799`, not `7 799 m/s` — the inverse of a default table, and deliberate. The
 * formatted reading is available as a `title` on the keys that have a unit (see
 * `PayloadValues`), so nothing is lost either way.
 */
export function Json(props: { readonly value: unknown }) {
  return (
    <pre className="bg-panel-sunken border-border text-fg-muted max-h-80 overflow-auto rounded-md border p-3 font-mono text-xs leading-snug">
      {JSON.stringify(props.value, null, 2)}
    </pre>
  );
}
