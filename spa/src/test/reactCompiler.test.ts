import { describe, expect, it } from 'vitest';
import { BoardsPage } from '../pages/BoardsPage.tsx';
import { ComparePage } from '../pages/ComparePage.tsx';
import { PlayerEventsPage } from '../pages/PlayerEventsPage.tsx';
import { SearchPage } from '../pages/SearchPage.tsx';
import { BoardTable } from '../ui/BoardTable.tsx';
import { HandleComboBox } from '../ui/kit/HandleComboBox.tsx';
import { DataTable } from '../ui/kit/index.ts';
import { YourStanding } from '../ui/YourStanding.tsx';

/**
 * Proof that React Compiler is actually wired up.
 *
 * This is not paranoia. The compiler runs through `@rolldown/plugin-babel`
 * (`@vitejs/plugin-react` 6 dropped its inline `babel` option), and every way it
 * can fail to run is **silent**: a missing `@babel/core` peer, a preset filter
 * that stops matching, a plugin ordering change. The app keeps working — it just
 * quietly loses every memoization, and the first symptom is a profiler session
 * six months later.
 *
 * The check is a string match on the compiled function body, and there are two
 * of them because the compiler emits two different things:
 *
 *  - **`_c(n)`** — the memo cache allocation. Every compiled component has one,
 *    so this is the signal that a component was compiled *at all*.
 *  - **`Symbol.for("react.memo_cache_sentinel")`** — the marker for a slot that
 *    has never been filled. It only appears where the compiler hoisted something
 *    with no dependency to compare against, so a component can be perfectly
 *    compiled and not contain it.
 *
 * Vitest transforms through the same `vite.config.ts` the build uses, so if this
 * passes, the build is compiling too.
 */

/**
 * `_c(9)` after bundling, `(0, __vite_ssr_import_0__.c)(9)` under vitest's SSR
 * transform. Both are the same call to `react/compiler-runtime`.
 */
const ALLOCATES_MEMO_CACHE = /(?:\b_c|\.c\))\(\d+\)/;

describe('React Compiler', () => {
  // A spread across a page, a table, a kit component and the two screens that
  // are loaded lazily, because the compiler bails out **per component**: one
  // component violating the Rules of React loses its own memoization silently
  // and nothing else's, so a single sample would prove very little.
  it.each([
    ['BoardsPage', BoardsPage],
    ['BoardTable', BoardTable],
    ['DataTable', DataTable],
    ['HandleComboBox', HandleComboBox],
    ['YourStanding', YourStanding],
    ['ComparePage', ComparePage],
    ['SearchPage', SearchPage],
    ['PlayerEventsPage', PlayerEventsPage],
  ])('compiled %s (auto-memoization is on)', (_name, component) => {
    expect(component.toString()).toMatch(ALLOCATES_MEMO_CACHE);
  });

  it.each([
    ['BoardsPage', BoardsPage],
    ['BoardTable', BoardTable],
  ])('%s carries the memo-cache sentinel', (_name, component) => {
    expect(component.toString()).toContain('react.memo_cache_sentinel');
  });

  it("nothing hand-writes memoization — that is the compiler's job now", () => {
    // A guard on the source, not the output: if someone reaches for useMemo,
    // preserve-manual-memoization forces the compiler to bail out of that
    // component entirely, which is worse than the memo they were trying to add.
    for (const component of [BoardTable, ComparePage, HandleComboBox, YourStanding]) {
      const source = component.toString();
      expect(source).not.toContain('useMemo');
      expect(source).not.toContain('useCallback');
    }
  });
});
