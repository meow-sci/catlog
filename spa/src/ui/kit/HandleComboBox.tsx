import { Search } from 'lucide-react';
import { useState } from 'react';
import { ComboBox, Input, type Key, ListBox, ListBoxItem, Popover } from 'react-aria-components';
import { MIN_QUERY_LENGTH } from '../../api/client.ts';
import { cn } from '../cn.ts';
import { SUGGESTION_LIMIT, useHandleSuggestions } from '../useHandleSuggestions.ts';

/**
 * Handle search, as a React Aria `ComboBox`.
 *
 * This is the argument for React Aria in one component: arrow keys, Home/End,
 * Escape, the `aria-activedescendant` wiring between the input and the listbox,
 * the popover's placement and its focus containment, and the announcement of how
 * many results appeared — all of it comes free and all of it is what a
 * hand-rolled suggestion box gets wrong.
 *
 * `allowsCustomValue` because a handle you typed but did not pick is still a
 * search worth running. `menuTrigger="input"` because the list should appear as
 * you type rather than needing a button.
 *
 * **Server-side filtering.** The endpoint matches prefix-first then substring,
 * in that order, and that ordering is the useful part of the answer. So the
 * combo box is told not to filter again — [KEEP_EVERYTHING] — and renders
 * exactly what came back, in the order it came back.
 */

/** ComboBox filters client-side by default; the server has already done it, better. */
const KEEP_EVERYTHING = () => true;

interface Suggestion {
  readonly id: string;
  readonly handle: string;
}

export function HandleComboBox(props: {
  readonly label: string;
  readonly placeholder?: string;
  /** A handle was picked from the list. */
  readonly onCommit: (handle: string) => void;
  /** Enter was pressed on free text. Omit to treat Enter as "pick nothing". */
  readonly onSubmitQuery?: (query: string) => void;
  /** Cleared after a commit — what a "add another handle" picker wants. */
  readonly clearOnCommit?: boolean;
  /**
   * What the box starts out holding. `HeaderSearch` mounts this component in
   * place of a plain input the visitor has already typed into, and the letters
   * they typed must survive the swap.
   */
  readonly defaultQuery?: string;
  /** Focus the input on mount — the other half of surviving that swap. */
  readonly restoreFocus?: boolean;
  readonly className?: string;
  readonly id?: string;
}) {
  const [query, setQuery] = useState(props.defaultQuery ?? '');
  const result = useHandleSuggestions(query);
  const handles = result.status === 'ready' ? result.data.handles : [];
  const items: Suggestion[] = handles.map((handle) => ({ id: handle, handle }));
  const truncated = result.status === 'ready' && result.data.truncated === true;
  const tooShort = query.trim().length > 0 && query.trim().length < MIN_QUERY_LENGTH;

  const commit = (handle: string) => {
    props.onCommit(handle);
    if (props.clearOnCommit === true) setQuery('');
  };

  return (
    <search className={cn('min-w-0', props.className)}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          const typed = query.trim();
          if (typed === '') return;
          // An exact match on Enter goes straight to that profile: if you knew the
          // handle, a results page with one row on it is a wasted click.
          const exact = handles.find((h) => h.toLowerCase() === typed.toLowerCase());
          if (exact !== undefined) {
            commit(exact);
            return;
          }
          props.onSubmitQuery?.(typed);
        }}
      >
        <ComboBox
          aria-label={props.label}
          allowsCustomValue
          allowsEmptyCollection
          menuTrigger="input"
          defaultFilter={KEEP_EVERYTHING}
          inputValue={query}
          onInputChange={setQuery}
          items={items}
          selectedKey={null}
          onSelectionChange={(key: Key | null) => {
            if (key !== null) commit(String(key));
          }}
          className="w-full"
        >
          <div className="border-border bg-panel-sunken data-focus-within:border-border-strong flex min-w-0 items-center gap-2 rounded-md border px-2 transition-colors duration-150">
            <Search aria-hidden className="text-fg-subtle size-4 shrink-0" />
            <Input
              id={props.id}
              // A callback rather than the `autoFocus` attribute: the swap this
              // supports happens well after page load, when the browser may not
              // honour late `autofocus`, and the caret belongs after what was
              // already typed rather than in front of it.
              ref={(el) => {
                if (el !== null && props.restoreFocus === true) {
                  el.focus();
                  el.setSelectionRange(el.value.length, el.value.length);
                }
              }}
              placeholder={props.placeholder ?? 'Find a handle'}
              className="text-fg placeholder:text-fg-subtle min-h-8 w-full min-w-0 bg-transparent py-1 text-base outline-none"
            />
          </div>
          <Popover className="bg-panel-raised border-border shadow-popover w-(--trigger-width) overflow-auto rounded-lg border py-1">
            <ListBox
              items={items}
              renderEmptyState={() => (
                <p className="text-fg-muted px-3 py-2 text-sm">
                  {tooShort
                    ? 'Two characters is the shortest search catlog will run.'
                    : result.status === 'loading'
                      ? 'Looking…'
                      : query.trim() === ''
                        ? 'Type a handle.'
                        : // "match", not "start with": the endpoint is prefix-first
                          // *then* substring, so a hit can be in the middle.
                          `No handles match ${query.trim()}.`}
                </p>
              )}
            >
              {(item: Suggestion) => (
                <ListBoxItem
                  id={item.id}
                  textValue={item.handle}
                  className="data-focused:bg-wash-hover data-focused:text-fg text-fg cursor-pointer px-3 py-1.5 text-base outline-none"
                >
                  {item.handle}
                </ListBoxItem>
              )}
            </ListBox>
            {truncated && (
              <p className="text-fg-muted border-border mt-1 border-t px-3 py-1.5 text-xs">
                More handles match. Try a longer query.
              </p>
            )}
          </Popover>
        </ComboBox>
        {truncated && (
          <p className="sr-only">More than {SUGGESTION_LIMIT} handles match. Try a longer query.</p>
        )}
      </form>
    </search>
  );
}
