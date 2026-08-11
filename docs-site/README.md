# catlog docs site

The **player-facing** documentation for catlog: what the Kitten Space Agency mod
records, and how those records become leaderboards. Astro + Starlight, published
to GitHub Pages at <https://meow.science.fail/catlog/>.

Not to be confused with `site/` in the repository root, which holds the datastar
frontend's assets and Playwright suite.

## The rule

This site is one half of catlog's event and projection reference.
**[`docs/event-details.md`](../docs/event-details.md) is the other half, and it is
the primary one.** A commit that changes an event, a payload field, a detector, a
game reading, a fold, a board or an eligibility rule updates **both**, in that
commit. See `AGENTS.md`, `docs/CONSTITUTION.md` §9.1, and `DOCS-003` in
`docs/DECISIONS.md`.

Code identifiers, file paths and patch points belong in `event-details.md` and
nowhere on this site. The site explains where a number comes from in terms a
player can check.

## Layout

```
src/
  content/docs/          the pages
    start/               what catlog is, how a flight becomes a score, identity, privacy
    events/              the event catalog, one page per group + a filterable index
    leaderboards/        how boards work, every board, every exclusion rule
    reference/           glossary, units, this rule restated for maintainers
  components/
    EventBrowser.tsx     the filterable event index (React)
    EventDetail.astro    renders one event from src/data/events.ts
    BoardDetail.astro    renders one board from src/data/boards.ts
  data/
    events.ts            typed mirror of the event catalog — DERIVED DATA
    boards.ts            typed mirror of the board catalog — DERIVED DATA
```

The two data modules are the single source for each event's and each board's
player-facing prose, so a change lands in one place and every page that mentions
it follows.

## Commands

pnpm only — never npm, npx or yarn.

| Command                             | What                                                                            |
| ----------------------------------- | ------------------------------------------------------------------------------- |
| `pnpm install`                      | Install dependencies                                                            |
| `pnpm dev`                          | Dev server on `localhost:4321`                                                  |
| `pnpm build`                        | Build to `./dist/`. Also type-checks frontmatter and Starlight component props. |
| `pnpm preview`                      | Serve the built site                                                            |
| `pnpm check`                        | lint + format check + build — what CI runs                                      |
| `pnpm lint` / `pnpm lint:fix`       | oxlint                                                                          |
| `pnpm format` / `pnpm format:check` | oxfmt                                                                           |

## Notes

- **`base` is `/catlog/`.** Every internal link carries it: `/catlog/events/browse/`,
  with a trailing slash.
- **`start/privacy.mdx` is the only page that tells the privacy story.** Every other
  page links to it rather than restating what catlog does and does not know — a
  reader who came for the boards should not read the guarantee on every page. See
  `DOCS-013` in `docs/DECISIONS.md`.
- **React Compiler is on.** Hand-written `useMemo` / `useCallback` / `memo` are
  forbidden — a manual memo makes the compiler bail out of the whole component.
- Deployed by `.github/workflows/docs-site.yml`, which only fires on pushes
  touching `docs-site/**`.
