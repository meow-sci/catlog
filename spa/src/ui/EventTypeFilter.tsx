import { ChevronDown } from 'lucide-react';
import type { Key } from 'react-aria-components';
import { Label, ListBox, ListBoxItem, Popover, Select, SelectValue } from 'react-aria-components';
import { Button } from './kit/index.ts';

/** The value meaning "no `?type=` filter". */
export const ALL_TYPES = '';

/**
 * The event-type filter, shared by the per-handle and global raw logs.
 *
 * `onChange` navigates rather than setting state at both call sites: the
 * filter lives in the URL, so a filtered log is a pasteable link and the back
 * button undoes a filter change the way it undoes a page change.
 */
export function TypeFilter(props: {
  readonly types: readonly string[];
  readonly value: string;
  readonly onChange: (value: string) => void;
}) {
  const items = [
    { id: ALL_TYPES, label: 'Every type' },
    ...props.types.map((t) => ({ id: t, label: t })),
  ];
  return (
    <Select
      selectedKey={props.value}
      onSelectionChange={(key: Key | null) => {
        props.onChange(key === null ? ALL_TYPES : String(key));
      }}
      className="flex flex-wrap items-center gap-2"
    >
      <Label className="text-fg-muted text-sm">Show</Label>
      <Button className="min-w-56 justify-between">
        <SelectValue />
        {/* An icon rather than ▾ U+25BE: that codepoint is in no subset of
            the Inter package, so it would render from a fallback face. */}
        <ChevronDown aria-hidden className="text-fg-subtle size-4" />
      </Button>
      <Popover className="bg-panel-raised border-border shadow-popover max-h-80 w-(--trigger-width) overflow-auto rounded-lg border py-1">
        <ListBox items={items}>
          {(item: { id: string; label: string }) => (
            <ListBoxItem
              id={item.id}
              textValue={item.label}
              className="data-focused:bg-wash-hover text-fg cursor-pointer px-3 py-1.5 text-base outline-none"
            >
              {item.label}
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </Select>
  );
}
