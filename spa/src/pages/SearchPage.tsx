import { Search, Users, X } from 'lucide-react';
import { useState } from 'react';
import { Checkbox, CheckboxGroup, Input, SearchField } from 'react-aria-components';
import { MAX_COMPARE_HANDLES, MIN_QUERY_LENGTH } from '../api/client.ts';
import { hrefFor, navigate } from '../state/router.ts';
import {
  Empty,
  Failure,
  LinkButton,
  Loading,
  Panel,
  PanelFooter,
  PanelHeader,
} from '../ui/kit/index.ts';
import { useHandleSuggestions } from '../ui/useHandleSuggestions.ts';

/** How many results the page asks for. The dropdown asks for fewer. */
const PAGE_LIMIT = 50;

/**
 * Handle search as a real, linkable page — not only the header overlay.
 *
 * Results are checkable, and the check marks build a comparison: "find my
 * friends, then put us side by side" is one flow rather than two, and it is the
 * flow the whole comparison feature exists for.
 *
 * Two rules from the endpoint show through and both are deliberate:
 *
 *  - **Nothing is requested below two characters.** The server answers 400, not
 *    an empty 200, so a box that fired on the first keystroke would produce an
 *    error on every search. The page says what is missing instead.
 *  - **`truncated` means narrow the query, not load more.** There is no offset:
 *    "a paged search over a live directory is a promise this cannot keep." So
 *    there is no pager here, and there should never be one.
 */
export function SearchPage(props: { readonly q: string }) {
  const [query, setQuery] = useState(props.q);
  const [picked, setPicked] = useState<string[]>([]);
  const result = useHandleSuggestions(query, PAGE_LIMIT);
  const typed = query.trim();
  const handles = result.status === 'ready' ? result.data.handles : [];

  return (
    <div className="space-y-5">
      <header className="max-w-[65ch]">
        <h1>Find a handle</h1>
        <p className="text-fg-muted mt-1">
          Handles that match, closest first. Tick a few to line them up side by side.
        </p>
      </header>

      <SearchField
        aria-label="Search handles"
        value={query}
        onChange={setQuery}
        onSubmit={(value) => {
          // The URL is what makes a search shareable, so submitting writes it —
          // and an exact hit goes straight to the profile, because a results
          // page with one row on it is a wasted click.
          const exact = handles.find((h) => h.toLowerCase() === value.trim().toLowerCase());
          if (exact !== undefined) navigate({ name: 'player', handle: exact });
          else navigate({ name: 'search', q: value.trim() });
        }}
        className="border-border bg-panel-sunken data-focus-within:border-border-strong flex max-w-md items-center gap-2 rounded-md border px-2"
      >
        <Search aria-hidden className="text-fg-subtle size-4 shrink-0" />
        <Input
          // The one input on a page whose entire purpose is this input. The
          // rule exists for forms that steal focus from the content around
          // them; there is no content around this one.
          // oxlint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          placeholder="whiskers"
          className="text-fg placeholder:text-fg-subtle min-h-9 w-full bg-transparent py-1 text-base outline-none"
        />
        {query !== '' && (
          <button
            type="button"
            aria-label="Clear"
            onClick={() => {
              setQuery('');
            }}
            className="text-fg-subtle hover:text-fg cursor-pointer"
          >
            <X aria-hidden className="size-4" />
          </button>
        )}
      </SearchField>

      <Panel>
        <PanelHeader
          title="Matches"
          aside={handles.length > 0 ? `${String(handles.length)} shown` : undefined}
        />
        {typed.length > 0 && typed.length < MIN_QUERY_LENGTH && (
          <Empty>Two characters is the shortest search catlog will run.</Empty>
        )}
        {typed.length === 0 && <Empty>Type a handle, or part of one.</Empty>}
        {result.status === 'loading' && <Loading label="Searching…" />}
        {result.status === 'error' && <Failure what="search for handles" error={result.error} />}
        {result.status === 'ready' &&
          handles.length === 0 &&
          typed.length >= MIN_QUERY_LENGTH && (
            // "match", not "start with": the endpoint is prefix-first and *then*
            // substring, so a hit can be anywhere in a handle.
            <Empty>No handles match {typed}.</Empty>
          )}
        {handles.length > 0 && (
          <CheckboxGroup
            aria-label="Handles to compare"
            value={picked}
            onChange={setPicked}
            className="divide-border divide-y"
          >
            {handles.map((handle) => (
              <div key={handle} className="flex items-center gap-3 px-3 py-2">
                <Checkbox
                  value={handle}
                  aria-label={`Compare ${handle}`}
                  isDisabled={picked.length >= MAX_COMPARE_HANDLES && !picked.includes(handle)}
                  className="group flex cursor-pointer items-center gap-2 data-disabled:cursor-not-allowed data-disabled:opacity-40"
                >
                  <span className="border-border-strong group-data-selected:bg-accent group-data-selected:border-accent flex size-4 shrink-0 items-center justify-center rounded-sm border transition-colors duration-150">
                    <span className="text-accent-fg hidden text-xs leading-none group-data-selected:block">
                      ✓
                    </span>
                  </span>
                </Checkbox>
                <a
                  href={hrefFor({ name: 'player', handle })}
                  className="text-fg hover:text-accent-text font-medium"
                >
                  {handle}
                </a>
              </div>
            ))}
          </CheckboxGroup>
        )}
        {result.status === 'ready' && result.data.truncated === true && (
          <p className="border-border text-fg-muted border-t px-3 py-2 text-sm">
            More handles match. Try a longer query.
          </p>
        )}
        {picked.length > 0 && (
          <PanelFooter>
            <span className="tabular-nums">
              {picked.length} selected
              {picked.length >= MAX_COMPARE_HANDLES && ' — that is the most'}
            </span>
            <LinkButton variant="primary" href={hrefFor({ name: 'compare', handles: picked })}>
              <Users aria-hidden className="size-3.5" />
              Compare these
            </LinkButton>
          </PanelFooter>
        )}
      </Panel>
    </div>
  );
}
