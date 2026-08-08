import { Search } from 'lucide-react';
import { useRef, useState } from 'react';
import { cn } from './cn.ts';
import type { HandleComboBox } from './kit/HandleComboBox.tsx';

/**
 * The header's handle search, deferred.
 *
 * The real thing is [HandleComboBox] — React Aria's `ComboBox`, with the
 * `ListBox`, the `Popover` and the overlay machinery under them. It is also on
 * every page, which put all of that into the chunk every visitor downloads to
 * see a leaderboard they will never search from. So the shell renders this
 * instead: a plain, accessible `<input type="search">` in a form whose submit
 * is a real `/search?q=` navigation, weighing nothing.
 *
 * The first sign of interest — focus, or a pointer passing over it — starts the
 * dynamic import, and once the module is in hand the combo box takes the plain
 * input's place. The swap preserves what the visitor was doing: the letters
 * they typed carry over (`defaultQuery`) and focus is restored with the caret
 * at the end (`autoFocus`) — but only if the input was still focused when the
 * module arrived, because a hover-triggered upgrade must not steal focus from
 * wherever the keyboard actually is.
 *
 * Until the upgrade lands (or if the chunk fails to load — the upgrade is an
 * enhancement, not a dependency), the plain form keeps working: Enter searches.
 * What is lost in that window is suggestions, which nothing can need within a
 * hundred milliseconds of first touching an empty box.
 */
/**
 * The one shared, deduped load of the combo box module.
 *
 * Module-level rather than inside the component for two reasons: every
 * `HeaderSearch` (and every focus of one) shares a single request, and — the
 * hard constraint — a dynamic `import()` expression inside a component is
 * syntax React Compiler cannot analyse, so its presence silently bails the
 * whole component out of memoization (`reactCompiler.test.ts` is what catches
 * that). On failure the slot is cleared so a later focus can try again.
 */
let comboBoxLoad: Promise<typeof HandleComboBox> | null = null;

function loadComboBox(): Promise<typeof HandleComboBox> {
  comboBoxLoad ??= import('./kit/HandleComboBox.tsx').then(
    (mod) => mod.HandleComboBox,
    (cause: unknown) => {
      comboBoxLoad = null;
      throw cause;
    },
  );
  return comboBoxLoad;
}

export function HeaderSearch(props: {
  readonly label: string;
  readonly placeholder?: string;
  /** A handle was picked from the suggestion list (only possible once upgraded). */
  readonly onCommit: (handle: string) => void;
  /** Enter was pressed on free text. */
  readonly onSubmitQuery: (query: string) => void;
  readonly clearOnCommit?: boolean;
  readonly className?: string;
}) {
  const [query, setQuery] = useState('');
  const [full, setFull] = useState<{
    readonly ComboBox: typeof HandleComboBox;
    readonly refocus: boolean;
  } | null>(null);
  const input = useRef<HTMLInputElement>(null);

  const upgrade = () => {
    loadComboBox().then(
      (ComboBox) => {
        setFull({
          ComboBox,
          // Decided at arrival time, not request time: a hover can start the
          // load and a click elsewhere can move focus before it finishes.
          refocus: input.current !== null && input.current === document.activeElement,
        });
      },
      () => {
        // The chunk did not load — offline, most likely. The plain form below
        // still searches, and the next focus retries.
      },
    );
  };

  if (full !== null) {
    const { ComboBox } = full;
    return (
      <ComboBox
        label={props.label}
        placeholder={props.placeholder ?? 'Find a handle'}
        className={props.className ?? ''}
        onCommit={props.onCommit}
        onSubmitQuery={props.onSubmitQuery}
        clearOnCommit={props.clearOnCommit ?? false}
        defaultQuery={query}
        restoreFocus={full.refocus}
      />
    );
  }

  return (
    <search className={cn('min-w-0', props.className)}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          const typed = query.trim();
          if (typed === '') return;
          props.onSubmitQuery(typed);
        }}
      >
        {/* The same wrapper and tokens as the combo box's own input, so the
            upgrade is invisible: nothing moves, nothing restyles. */}
        <div className="border-border bg-panel-sunken focus-within:border-border-strong flex min-w-0 items-center gap-2 rounded-md border px-2 transition-colors duration-150">
          <Search aria-hidden className="text-fg-subtle size-4 shrink-0" />
          <input
            ref={input}
            type="search"
            aria-label={props.label}
            placeholder={props.placeholder ?? 'Find a handle'}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
            }}
            onFocus={upgrade}
            onPointerEnter={upgrade}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
            spellCheck={false}
            className="text-fg placeholder:text-fg-subtle min-h-8 w-full min-w-0 bg-transparent py-1 text-base outline-none"
          />
        </div>
      </form>
    </search>
  );
}
