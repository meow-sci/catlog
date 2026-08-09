# Agent Behaviorial Instructions

You MUST obey these to change your behavior

- Keep responses focused, brief, and concise. Keep disclaimers and caveats short, and spend most of the response on the main answer. When asked to explain something, give a high-level summary unless an in-depth explanation is specifically requested.
- Before your first tool call, say in one sentence what you're about to do. While working, give a brief update only when you find something important or change direction. When you finish, lead with the outcome: your first sentence should answer "what happened" or "what did you find," with supporting detail after it for readers who want it.
- Match the length of written documents to what the task needs: cover the substance, but do not pad with filler sections, redundant summaries, or boilerplate.
- Deliver what was asked, at the scope intended. Make routine judgment calls yourself, and check in only when different readings of the request would lead to materially different work. If the request seems mistaken or a better approach exists, say so in a sentence and continue with the task as asked rather than quietly narrowing, widening, or transforming it. Finish the whole task, and stop short of actions that are clearly beyond what was asked.
- Delegate to a subagent only for large tasks that are genuinely independent and parallelizable, such as a wide multi-file investigation. Do not delegate work you can finish yourself in a handful of tool calls, and do not use subagents to verify or double-check your own work. If one subagent can complete the task, use one rather than several, and keep spawn counts low.
- Only correct an earlier statement when the error would change the user's code, conclusions, or decisions. State corrections plainly and briefly, then continue the task. For slips that change nothing for the user, make the fix and move on without noting it.

# Agent Tone

<tone_preference>
Keep outputs reasonably concise.
</tone_preference>

---

# The catlog repository

Passive telemetry from Kitten Space Agency → community leaderboards. A KSA mod detects gameplay
events client-side, spools them, and ships signed compressed batches to a small Go server that
stores an immutable log, folds it into leaderboards, and serves them.

## Documentation constitution — NON-NEGOTIABLE

**catlog's documentation is load-bearing.** It is the specification two independent implementations
(Go and C#) are built against, and the next reader — human or agent — will trust it over the code.
A change that leaves a document wrong is an **incomplete change**, not a change plus a follow-up.

### The rules

1. **Update the affected document in the same commit as the code.** Never "later", never a TODO.
   Use the table below to find which one.
2. **Every change gets a dated entry in [`docs/DECISIONS.md`](docs/DECISIONS.md)** — right area, next
   free `<AREA>-NNN`, and it must say **why**, not only what. A decision without its reasoning gets
   re-litigated within the year; that is the entire purpose of the file.
3. **Never leave a document describing something that is gone.** Delete the passage or mark it
   *superseded* with a pointer to what replaced it. A stale paragraph is worse than a missing one,
   because it is believed.
4. **Never mint a new `§` number.** The `§N` numbering is a frozen citation space inherited from a
   deleted plan, mapped in
   [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#5-the--section-index). New material gets a document
   and a heading; cite it by name.
5. **Contract changes carry a version bump.** An event or payload change bumps that event's `ver`;
   an endpoint shape change bumps its `ver`; a credential-file change bumps `format`.
6. **Read [`docs/CONSTITUTION.md`](docs/CONSTITUTION.md) before making a design decision.** Most of
   the time, the principle is *why* the code looks the way it does. If a principle seems to be in the
   way, that is the thing to discuss — not to route around.
7. **Do not re-litigate a locked decision (D1–D22) or a recorded one** without reading its entry
   first. Nearly every one exists because the obvious alternative was tried, measured or reasoned
   through, and lost.
8. **The event/projection reference is [`docs/event-details.md`](docs/event-details.md), and it does
   not move alone.** See the rule immediately below — it is not optional and it has no deferred form.

### The event & projection reference — `docs/event-details.md` + `docs-site/`

**[`docs/event-details.md`](docs/event-details.md) is THE reference for the event catalog and the
read projections.** One entry per event type — wire identity, every payload key, the game object the
value is read off, the Harmony patch point or poll that produces it, whether it is event-driven or
passive, its thresholds and debounces, and the boards it moves. One entry per projection — the events
that feed it, the fold, the eligibility rules, the units. `docs/events.md` remains the wire
*specification*; `event-details.md` is the joined, maintained view, and it is the one to update
first.

**`docs-site/` MUST be updated in tandem, in the same commit.** The Astro/Starlight site under
[`docs-site/`](docs-site/) is the player-facing half of exactly the same information: same events,
same projections, same rules, written for someone who plays Kitten Space Agency rather than someone
who builds it. It is published at <https://meow.science.fail/catlog/> and players will trust it.

**A commit that changes an event, a payload field, a detector, a game read, a fold, a board, an
eligibility rule or a unit and updates only one of the two is an incomplete change.** There is no
"docs-site follow-up" — the follow-up never happens, and the site is what players read.

| You touched… | `docs/event-details.md` | `docs-site/` |
|---|---|---|
| a payload field, a `ver`, an emission rule | that event's section | its family page + `src/data/events.ts` |
| where a value is read from in the game | that event's **Game source** block | its "Where it comes from" prose, in player terms |
| event-driven ↔ passive, a threshold, a debounce | that event's **Classification** block | the event's `trigger` in `src/data/events.ts` |
| a new event type | a new section + the registry table | `src/data/events.ts` + the right family page |
| a board's fold, eligibility, unit or title | **Boards** | `leaderboards/catalog.mdx` + `src/data/boards.ts` |
| a rebuild-vs-incremental divergence | **Rebuild ≠ incremental** | `leaderboards/eligibility.mdx` |

The site's prose explains *where in the game* a number comes from **in player-relatable terms** — "the
number your navball shows as surface speed", not "`Vehicle.GetSurfaceSpeed()`". Code identifiers,
file paths and patch points belong in `event-details.md` and nowhere on the site.

### What to update when

| If you change… | Update |
|---|---|
| An event, envelope field or payload | `docs/events.md` **+ bump `ver`** — **and `docs/event-details.md` + `docs-site/`** |
| An HTTP endpoint, status, header, error code | `docs/ingest-api.md` |
| The credential file's shape | `docs/credential.md` **+ bump `format`** |
| Handle rules, `user_key`, moderation semantics | `docs/identity.md` |
| A Go package's role, the schema, config keys, admin routes | `docs/server.md` |
| A fold, a board key, an eligibility rule, a projection | **`docs/event-details.md` + `docs-site/`** |
| A detector's game source, threshold, or event-vs-passive nature | **`docs/event-details.md` + `docs-site/`** |
| Anything a player reads about what catlog records | **`docs-site/`** (see the rule above) |
| A detector rule, the outbox, the shipper, a KSA patch point | `docs/mod.md` (+ `docs/ksa-integration.md` if a patch point moved) |
| The container images, nginx, the compose project, Ansible | `docs/operations.md` |
| A Make target, a build flag, a test mode | `DEVELOPMENT.md` |
| Anything a visitor to the website would notice | `README.md` |
| Repo layout, ports, a new top-level directory | `docs/ARCHITECTURE.md` |
| An integrity check | `docs/integrity-audit.md`, against Constitution §8's five tests |
| Either frontend's visual or interaction design | `docs/ui-design.md` |
| Something now unbuilt, blocked, or refused | `docs/ROADMAP.md` |

**Always, additionally:** `docs/DECISIONS.md`.

## Where to look first

| Question | Go here |
|---|---|
| What is this, how does it fit together | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| What does `§4.5.3` in this comment mean | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#5-the--section-index) |
| Why is it like this | [`docs/DECISIONS.md`](docs/DECISIONS.md) — grouped by area, searchable |
| What are we optimising for | [`docs/CONSTITUTION.md`](docs/CONSTITUTION.md) |
| How do I build/run/test X | [`DEVELOPMENT.md`](DEVELOPMENT.md) |
| What's the wire contract | `docs/{events,ingest-api,identity,credential}.md` |
| What does event X contain, where does it come from, what board does it move | [`docs/event-details.md`](docs/event-details.md) |
| What do players get told about all that | [`docs-site/`](docs-site/) — the published site |
| What's not built / never will be | [`docs/ROADMAP.md`](docs/ROADMAP.md) |

## Layout

| Path | What | Doc |
|---|---|---|
| `server/` | Go 1.26. `catlogd`, `catlogctl`, `mockidp`. Logic in `internal/*`. | `docs/server.md` |
| `mod/` | .NET 10. `catlog.lib` (KSA-free core), `catlog` (the game mod), `catlog.sim`, `catlog.loadgen`, 2 test projects. | `docs/mod.md` |
| `site/` | Datastar site's CSS/JS + the Playwright suite. **HTML templates live in `server/internal/web/templates/`.** | `docs/ui-design.md` |
| `spa/` | Standalone React reader over the public read API. Own lockfile and deployment. | `spa/README.md` |
| `contracts/testdata/` | Deterministic cross-language conformance vectors, consumed by both suites. | `docs/ingest-api.md` |
| `infra/` | Dockerfiles, the compose project, nginx config, Ansible roles and playbooks. | `docs/operations.md` |
| `docs-site/` | Astro 7 + Starlight + React. The **player-facing** docs site, published to GitHub Pages at `/catlog/`. Own lockfile, own deployment. | `docs-site/README.md` |
| `docs/`, `scripts/`, `data/` | Specification; helper scripts; git-ignored runtime state. | — |

## Conventions

- **pnpm only.** Never `npm`, `npx`, `yarn`.
- **Conventional commits**: `feat(server):`, `fix(mod):`, `perf(loadgen):`.
- **Go**: stdlib `net/http`, no framework, no ORM. Fold *writes* live in `stats/`, projection *reads*
  in `store/`. Every package needs a non-HTTP entry point — a package whose only interface is its mux
  forces a new seam on every later consumer.
- **C#**: `ImplicitUsings` disabled (every `using` explicit), nullable enabled,
  `TreatWarningsAsErrors`, immutable records, per-subsystem dead-latch error handling. **`catlog.lib`
  must never reference KSA** — a guard test enforces it.
- **TypeScript (`spa/`, `docs-site/`)**: React Compiler is on, so the Rules of React are mandatory and
  hand-written `useMemo`/`useCallback`/`memo` are **forbidden** (a manual memo makes the compiler bail
  out of the whole component). Anything that navigates is an `<a href>`.
- **Every change keeps `make test` green** and adds tests for what it changed.

## Things that will bite you

- **One process per database file.** Turso takes an exclusive whole-file lock that excludes other
  *processes* entirely, readers included. A `make dev` and a `make e2e` cannot coexist; an IDE
  database tool on `data/events.db` stops the server from starting. Use `make db-snapshot`.
- **`catlogctl` never opens a database.** It is an HTTP client for the loopback admin API. That is
  why every stateful verb is an admin route.
- **`_ms` is metres per second in payload keys** (`speed_ms`, `fastest_ms`) **while the board unit
  string `"ms"` is milliseconds.** Only `unitForKey` knows the difference.
- **`server/internal/units` is the single definition of a formatted catlog number**, and
  `spa/src/ui/units.ts` is a port of it. A rule change is three edits in one commit: `units.go`, its
  test table, and the port.
- **There is no allow-list of celestial bodies.** `body` is opaque to the server; `fastest_to_<body>`
  and `rud_<cause>` boards exist because a name appeared in the data. Never add a fixed list, and
  never assume a fixed board list in a client.
- **Rebuild ≠ incremental**, by design, whenever history holds a late flag, a scuttled kitten, or a
  flight that did not end recovered. That is D22, not a bug.
- **The mod's 30-second ship floor is a hard constant** enforced at three layers and measured against
  an injected clock the shipped assembly cannot reach. Three tests hold that shut. Do not "simplify"
  it.
- **Never add response compression to `/v1/ingest` or `/v1/feed/sse`** — the first is hashed byte for
  byte, the second is a stream.
- **Anti-cheat is capped by Constitution §8's five tests.** If a proposed check fails any of them, it
  is out of scope: do not build it, and flag it in `docs/integrity-audit.md` if it already exists.
