import { describe, expect, it } from 'vitest';
import { BoardsPage } from '../pages/BoardsPage.tsx';
import { BoardTable } from '../ui/BoardTable.tsx';

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
 * The check is a string match on the compiled function body. `_c(n)` allocates
 * the cache and `Symbol.for("react.memo_cache_sentinel")` marks an unfilled
 * slot; both only exist in compiler output. Vitest transforms through the same
 * `vite.config.ts` the build uses, so if this passes, the build is compiling too.
 */
describe('React Compiler', () => {
  it.each([
    ['BoardsPage', BoardsPage],
    ['BoardTable', BoardTable],
  ])('compiled %s (auto-memoization is on)', (_name, component) => {
    const source = component.toString();
    expect(source).toContain('react.memo_cache_sentinel');
  });

  it("nothing hand-writes memoization — that is the compiler's job now", () => {
    // A guard on the source, not the output: if someone reaches for useMemo,
    // preserve-manual-memoization forces the compiler to bail out of that
    // component entirely, which is worse than the memo they were trying to add.
    const source = BoardTable.toString();
    expect(source).not.toContain('useMemo');
  });
});
