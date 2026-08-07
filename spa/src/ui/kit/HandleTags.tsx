import { X } from 'lucide-react';
import { Button, type Key, Tag, TagGroup, TagList } from 'react-aria-components';

/**
 * The compared handles, as removable chips.
 *
 * React Aria's `TagGroup`: the chips are a focusable list, arrow keys move
 * between them, Backspace and Delete remove the focused one, and each remove
 * button is labelled with the handle it removes rather than a bare "remove"
 * eight times over.
 *
 * A chip whose player could not be found is still a chip. `found: false` is a
 * **column, not an omission** — silently dropping it lets a typo look like a
 * defeat — so it is marked here as well as in the table.
 */
export function HandleTags(props: {
  readonly label: string;
  readonly handles: readonly { readonly handle: string; readonly found: boolean }[];
  readonly onRemove: (handle: string) => void;
}) {
  return (
    <TagGroup
      aria-label={props.label}
      onRemove={(keys: Set<Key>) => {
        for (const key of keys) props.onRemove(String(key));
      }}
    >
      <TagList
        items={props.handles.map((h) => ({ id: h.handle, ...h }))}
        className="flex flex-wrap gap-2"
        renderEmptyState={() => (
          <p className="text-fg-muted text-sm">Nobody yet. Add a handle to compare.</p>
        )}
      >
        {(item: { id: string; handle: string; found: boolean }) => (
          <Tag
            id={item.id}
            textValue={item.handle}
            className={
              item.found
                ? 'border-accent-text/40 bg-wash-selected text-fg inline-flex items-center gap-1 rounded-full border py-0.5 pr-1 pl-3 text-sm'
                : 'border-border bg-panel-sunken text-fg-muted inline-flex items-center gap-1 rounded-full border py-0.5 pr-1 pl-3 text-sm line-through'
            }
          >
            {item.handle}
            <Button
              slot="remove"
              aria-label={`Remove ${item.handle}`}
              className="data-hovered:bg-wash-press text-fg-muted data-hovered:text-fg flex size-5 cursor-pointer items-center justify-center rounded-full transition-colors duration-150"
            >
              <X aria-hidden className="size-3.5" />
            </Button>
          </Tag>
        )}
      </TagList>
    </TagGroup>
  );
}
