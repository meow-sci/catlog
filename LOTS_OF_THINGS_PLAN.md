# LOTS_OF_THINGS_PLAN.md

**Per-save leaderboards, merit badges, weekly challenges, and the boards the community asked for.**

Status: **plan only — nothing here is implemented.** Written 2026-08-09.
Author: planning session. Audience: the implementing agents who will execute the phases below.

---

## 0. How to use this document

This plan is written to be executed by coding agents, one task at a time, in the dependency order
in Appendix G. **Do not use the document's page order as the execution order:** the required first
slice is A1–A6 → C1–C4 → A7–A9 → B. Every task
carries: the files it touches, the code it should write (or a close sketch of it), the tests it must
add, the documents it must update in the **same commit**, and an acceptance check.

**Read these first, once, before starting any task:**

| Read | Why |
|---|---|
| [`AGENTS.md`](AGENTS.md) | The documentation constitution. It is not advisory. |
| [`docs/CONSTITUTION.md`](docs/CONSTITUTION.md) | §5 (rebuildable), §6 (derived never claimed), §8 (anti-cheat cap), §9.1 (the two-halves doc rule). |
| [`docs/event-details.md`](docs/event-details.md) §§ *Projections*, *Boards*, *Rebuild ≠ incremental* | How a fold actually works today. |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) `PROJ-024`, `PROJ-026`, `PROJ-033`–`PROJ-037`, `PROJ-042`–`PROJ-047`, `PROJ-049`, `PROJ-088`, `PROJ-096`, `PROJ-099` | Every one of these constrains something below. |

**Non-negotiables that apply to every task in this plan.** A task that breaks one of these is not
done, however green the tests are:

1. **`make test` stays green.** Every task adds tests for what it changed.
2. **Rebuild == incremental outside the recorded D22 refinements.** Any new fold derives its
   bucket/window/scope from `ev.RecvTime`, `ev.Seq`, `ev.Career` or the payload — **never** from
   `time.Now()`, Go map iteration order or anything outside the event. Extend
   `projector.TestRebuildEqualsIncrementalForAnUnflaggedHistory` to every new table and separately
   pin each intentionally inherited late-flag/KIA/recovery divergence (see A8, F4 and H3).
3. **Docs move in the same commit.** `docs/event-details.md` **and** `docs-site/` for anything that
   touches an event, a payload field, a fold, a board, an eligibility rule or a unit. Plus a dated
   `docs/DECISIONS.md` entry in the right area with the next free number, saying **why**.
4. **Never mint a new `§` number.** New material gets a document and a heading.
5. **This plan is greenfield and every event remains `ver: 1`.** There is no launched contract,
   stored production data, previous-binary deployment, compatibility path or `stats.BuildVersion`
   bump to preserve. New and changed payloads define their final v1 shape, conformance vectors are
   regenerated to that shape, and the final deployment starts with fresh databases. This is the
   owner's explicit direction and the case `PROJ-100` records. The repository's post-launch version
   rules still apply to later work, not to tasks in this plan.
6. **Build the final schema, not an upgrade path.** Use the next migration number so the existing
   migration runner and exact-DDL tests construct the final fresh database, but add no backfills,
   upcasters, compatibility transforms or previous-schema acceptance tests. The sibling-table
   design in Task A5 remains because its lifetime/per-save meanings are cleanly distinct, not
   because a live database forces it.
7. **No celestial-body allow-list, ever.** Not for boards, not for badges, not for challenges. See
   §3.7 below — this is the single most likely way an implementing agent will get this wrong.
8. **Career ids are never published raw.** `readapi/privacy.go`'s per-player relabelling is mandatory
   on every new surface that carries one. See §3.5.

---

## 1. The question that started this: yes, we already have a per-game-save identifier

**`career`.** It is on the envelope of **every** event, it is **never null**, and the server already
validates it (`server/internal/ingest/decode.go`: exactly 16 lowercase Crockford base32 characters,
anything else rejects the whole batch).

```jsonc
{
  "id": "01J9V5M3E8Z0FAKEULID26CHR",
  "type": "vehicle.rud",
  "ver": 1,
  "flight": "01J9V5M3E8...",
  "session": "01J9V5M3E8...",
  "career": "b7k2q9x4m0nrt3vz",   // ← one KSA save, played over time
  "sim_t": 12345.678,             // ← seconds since THIS career's game started
  "wall_t": 1770000000123,
  "payload": { }
}
```

Everything the request asks for follows from four properties that are already true and already
tested:

- **A career *is* one save, across quit and resume.** The mod derives it as
  `crockford32(SHA-256("catlog-career:" + install_id + ":" + save_key))[0..10]`, where `save_key` is
  the KSA save name, learned by patching `UncompressedSave.Load` and `UncompressedSave.Make`. Closing
  and reopening the game and loading the same save yields the same `career`. **One career is many
  sessions** — `session` is minted at every save-load boundary. (`docs/events.md`, "`career` and
  `sim_t` — the career clock".)
- **`sim_t` is the career's own clock**, in seconds since that save's game began, and it survives
  quitting because KSA serialises it into the save as `UniverseData.GameTime`. So "how long has this
  save been played" is a number we already receive on every event. (PROJ-019.)
- **`projections.db` already has a `career` table** —
  `career(player_id, career, max_sim_t, rewound, first_seq)` — maintained by `stats/career.go`'s
  `careerFold`. `max_sim_t` **is** "how long this save has been played", already folded, already
  rebuilt-from-log correct.
- **`career` is already carried on `stats.Event`** (`stats/event.go`: `Career string`, `HasCareer()`),
  so every existing fold can see it today without a single wire or storage change.

**What is missing is only the ranking dimension.** `player_stat`'s primary key is
`(player_id, stat)`. Today a career-derived value like `fastest_to_orbit` is deliberately reduced
*across* careers — "the minimum is taken per player, not per career; your best career is the one
that represents you" (`stats/boards.go`, PROJ-024). To rank saves against each other we need
`(player_id, career, stat)`, which is a new table, exactly as `player_stat_period` was a new table
when a *period* became a dimension of a board (`0003_period.sql`, PROJ-042).

That is Phase A. Everything else in this plan builds on it.

---

## 2. What is being built

Five things, in dependency order:

| # | Thing | Shape | Phases |
|---|---|---|---|
| 1 | **Per-save (career) leaderboards** | A second scope on every existing board, plus 2 career-native boards. Ranks `(player, save)` pairs. | A, B |
| 2 | **Celestial systems as a first-class dimension** | KSA's system is replaceable XML content. The mod hashes it and reports its bodies; the server ranks *within* a system, names it in the UI, and can answer "have you been everywhere". | C |
| 3 | **New boards from the community ideas** | Mostly free from data already on the wire; five existing event types gain their final-v1 fields. | D, E |
| 4 | **Merit badges** | A once-only award in two independently earned scopes: lifetime and per-save, always labelled with the system. | F, G |
| 5 | **Weekly challenges** | A curated rule over a named, explicitly-dated window. | H, I |

And a documentation audit that closes all of it (Phase J). Every implementation task still owns
its affected docs and next-free decision entry in the same commit; Phase J never creates deferred
documentation for earlier code.

**Every save-scoped stat carries `(handle, save, system)`.** A save belongs to exactly one system, so
the system is determined by it and displayed by friendly name. A genuinely cross-system
player-scoped challenge may deliberately have no system label; H1 prevents attaching a
misleading last-contributor system.

### 2.1 The community ideas, and what each one actually needs

Verbatim from the request, with a verdict. **Six of the nine need no new wire data at all.**

| # | Community idea | Verdict | Where it lands |
|---|---|---|---|
| 1 | "Biggest lithobrake record (fastest impact where science/kitten survived)" | **Already exists** — `biggest_lithobrake_survived` requires `survived && crew_count ≥ 1 && !launch_pad`. It is board #1 in the catalog. The gap is *discoverability*, not data. | Phase G/H surfacing; new career scope in Phase A |
| 2 | "Most times a kitten did NOT land on their feet" | **Already exists** — `kitten_tumbles`, folded from `kitten.tumble`. New: a `tumbles_on_<body>` dynamic family (the payload already carries `body`) and a "clumsiest kitten" per-kitten record. | Task E1, E2 |
| 3 | "Most kittens to orbit and back" | **Derivable, no wire change.** Needs a `milestones` bitfield on `flight_state` (did this flight reach orbit) + the `kids` array already on `flight.ended`. | Task D3, E3 |
| 4 | "Most parts exploded/destroyed" | **Needs one new payload field** — `vehicle.rud.part_count`. Parts at destruction are not derivable from `flight.started.part_count` because staging changes it. | Task D1, E4 |
| 5 | "Get to Jupiter with no engines" | **Needs one new payload field** — `flight.started.engine_count` — then it is a badge/challenge over `vehicle.soi` joined to `flight_state`. | Task D2, F5, H4 |
| 6 | "Most SOIs within 10 years" | **Free.** `vehicle.soi` + envelope `sim_t`, career-scoped. A "year" is `units`' flat 365-day year — the *server* must never learn a body's orbital period, though the mod now reports it (§3.16). | Task E5 |
| 7 | "Most kittens KIA in a single crash" | **Free, with an honesty caveat.** `vehicle.rud.crew_count` already ships. Per D11 the crew *survive* a physics RUD, so the honest board is "most kittens aboard a vehicle that was lost", not "killed". | Task E6 |
| 8 | "Personal achievements to unlock — 'managed to get into orbit'" | **This is the badge system.** | Phases F, G |
| 9 | "Visited every planet in the solar system" | **Built — and correctly, for any system.** The mod reports the body list the *game* loaded, so the server still holds no list of its own and the badge is right for stock KSA, for a content patch that adds Neptune, and for a total-conversion mod, on the day each ships. Tiered count badges ship beside it. | Phase C, §3.7, Task F4 |

---

## 3. Design decisions, with the reasoning

Every subsection here becomes a dated `docs/DECISIONS.md` entry in Phase J (numbers are allocated in
Task J1). They are written out in full because the *why* is the part that stops them being
re-litigated, and because an implementing agent that does not understand the why will implement the
wrong thing.

### 3.1 A career is a **scope** — a second dimension of a board, not a new set of boards

`GET /v1/leaderboards` stays **one row per board**. Each row gains
`scopes: ["player","career"]`. `?scope=career` on a board URL selects the per-save ranking;
an absent `?scope=` means `player`. The response grows new fields, so semantic default behaviour —
not byte identity with today's pre-launch response — is the contract.

*Why:* this is PROJ-042's argument, applied a second time, and it applies harder here. Listing
`landings@career` as its own index entry multiplies the index by two — and because
`fastest_to_<body>` and `rud_<cause>` take their keys from the event stream, that multiplier applies
to a list with **no upper bound**. It would also force `stats.Describe` (which derives a board's
title, unit and direction from its key **alone**), `stats.Catalog` (which groups families by key
prefix) and the exact-match `/v1/leaderboards/{stat}` lookup to start parsing a compound key.
`site/e2e/boards.spec.ts` asserts the rendered index equals the API's exactly; a shape that grows
multiplicatively breaks that the day somebody reaches somewhere new.

**Storage is a new table, never an encoded key.** `career_stat(player_id, career, stat, …)`. The
argument against `stat = "landings@<career>"` is already written down in
`0003_period.sql`'s own header and has not changed.

### 3.2 Every board gets career scope, with no opt-out list

All 40 fixed boards, both dynamic families, and every board added later.

*Why:* the property that made periods maintainable is that they hang off the four write helpers and
therefore compose with the dynamic families **for free, with no registry to update and nothing to
enumerate** (PROJ-044, `TestDynamicBoardsGetTheirWindowsForFree`). An opt-out list *is* a registry,
and it would be a registry that has to be extended every time a board is added, by someone who has
to decide a question nobody is asking. If a board is meaningless per save, it is equally meaningless
to the reader, who simply will not look at it. The cost of being wrong in this direction is one
uninteresting row; the cost of being wrong in the other is a board somebody wanted that silently
does not exist.

### 3.3 Career boards have **no period dimension**, and that is a refusal not an omission

`?scope=career&period=weekly` is a `400 bad_request`. `?scope=career` is always all-time.

*Why:* two reasons that agree. **(a) Row count.** `player_stat_period` is already
players × boards × buckets; crossing it with careers makes it players × boards × buckets × careers,
and the bucket count is bounded only by retention while the career count is bounded by nothing.
Constitution §2 has an opinion about that table. **(b) It means nothing.** A career already *is* a
time scope — a contiguous stretch of one player's game — so "this save's landings, in week 32" is a
window over a window, and the number it produces answers no question anybody has.

### 3.4 A career board ranks `(player, save)` pairs, and one player may hold several rows

No dedup to "the player's best save".

*Why:* the question the board answers is literally "which *save* has the most landings", and a
player with five saves genuinely has five saves. Their best one sorts to the top on its merits and
the others sort below on theirs; that is an honest ranking, not a player farming the board. Deduping
would also make `rank` disagree between the board page and the profile, which is the one thing
`readapi`'s rank arithmetic exists to prevent.

### 3.5 A save is published as an **ordinal** for humans and a **relabelled id** for machines

Public rows carry `save: 3` (the player's third save, by first-seen order) and
`save_id: "<16 chars>"` (the per-player relabel from `readapi/privacy.go`). The **raw** `career`
value never reaches a public surface.

*Why the relabel:* PROJ-049. `career` is `SHA-256("catlog-career:" + install_id + ":" + save_key)`
and one install is one *machine*, so publishing it raw links two accounts belonging to one person —
which catlog deliberately makes impossible to tell from outside (Constitution §1). This was a live
leak once already, found and closed in `readapi/privacy.go`; a per-career board that skipped the
relabel would re-open it at ten times the surface area. **The relabel is not optional and not a
detail.**

*Why an ordinal as well:* the relabelled id is a 16-character opaque token, and the whole point of
these boards is that a player recognises their own save. The server cannot help by naming it — it
never learns the save's name, on purpose, because the install-id salt is what stops the server
learning what a player called their game. An ordinal is the most a reader can be given that costs
nothing: it is a function of that player's own event order, it groups nothing across accounts, and
it is stable under rebuild because it is assigned in ascending `first_seq`.

*URL form:* `/p/{handle}/saves/{ordinal}` — `/p/whiskers/saves/2`. Ordinals, not tokens, in URLs.

### 3.6 A badge is once-only within the current projection and rebuild-authoritative

One table, `badge_award`, with `career = ''` meaning **the lifetime (player) award** and
`career = '<id>'` meaning **the per-save award**. The same badge is awarded independently in both
scopes.

**Every row also carries the system**, denormalised, even though a save determines its system and the
value is therefore derivable by a join. Three reasons, and the first is the one that matters:

- **A reader has to see it without asking for it.** "Grand Tour" earned in stock Sol and "Grand Tour"
  earned in a twelve-planet conversion are different achievements, and a badge page that made you
  click through to find out which is a badge page that misleads. The system's *friendly name* is on
  every badge, every board row and every save, always.
- The lifetime row (`career = ''`) has no career to join through, so without the column it could not
  be labelled at all. It records the career **and** system it was **first earned in** — which is the
  honest answer to "where did the current projection first award this"; a rebuild may change it
  when earlier eligibility changes (§3.6).
- It makes "which badges have I earned in this system" one predicate rather than a join.

There is no *third* row per system. A system's badges are the union of its saves' badges, which is a
query, not a projection — and unlike the career/lifetime pair, the two would never disagree.

*Why one table with a sentinel rather than two tables:* `event_census` already uses exactly this
trick (`type = ''` is the stored total across every type, `bucket = ''` is all-time) and it is
correct here for the same reason — the two scopes have identical columns and identical rules, and
splitting them would mean two flushes, two dumpers, two read paths and two places to forget
something. `''` is safe as a sentinel because ingest rejects any `career` that is not exactly 16
Crockford characters, so the empty string can never be a real career.

*And why §3.12 rejects a sentinel for `player_body`, which looks like the same question and is not.*
There, the sentinel would have gone into a table that **already exists and already means one thing**,
to let it mean two — while widening a primary key that Turso cannot widen in place (§5.2). Here the
table is new, both scopes have the same columns and the same merge rule, and nothing is being
retrofitted. The test is whether the sentinel *adds* a meaning to a table or *is* the table's
meaning; `event_census` passes it, `badge_award` passes it, a widened `player_body` did not.

*Why once-only:* a badge is a *milestone*, and its interesting property is **when the current
projection first says you got it**. `INSERT … ON CONFLICT DO NOTHING` retains that first qualifying
seq inside one build.

**A rebuild is authoritative.** Badge folds read the same board/state projections the rest of the
server uses. A later `flight.flagged`, a KIA/recovery refinement, or a changed/removed badge fold can
therefore make a badge disappear when projections are rebuilt; adding a badge can discover it from
old events. That is intended. There is no `revoked` column, tombstone, event-time progress table,
`first_flagged_seq` or special badge eligibility path. The public API reports what the current code
projects from the immutable log, exactly as a leaderboard does. Adding or removing a badge means
adding/removing its registry entry **and** fold together; the fold-name change alters `BuildID` and
queues the rebuild that discovers or removes its rows. Document this plainly: “once-only” means one
earliest row in the current projection, not an irrevocable entitlement.

### 3.7 "Visited every planet" is built — and the list comes from the game, never from the server

The badge exists, but its exact meaning has one **owner decision gate** before delegation. The set it
checks against comes from the `system.body` events the mod sends after reading the game's own loaded
system (§3.16). Tier badges on the *count* of distinct bodies —
`wanderer` (3), `voyager` (5), `grand_tour` (8) — ship beside it, because they are a different and
also-interesting question.

**“Planet/world” has one explicit mod-side meaning.** Runtime `Astronomical.Class` is **not**
`"Planet"`: stock objects report concrete strings such as `PlanetaryBody`, `TerrestrialBody`,
`AtmosphericBody`, `MinorBody`, `Asteroid`, `Comet` and `StellarBody`. Emit both the concrete class
and this exhaustive normalised `kind`:

| `kind` | Rule |
|---|---|
| `star` | concrete class `StellarBody` |
| `planet` | concrete class `PlanetaryBody`, `TerrestrialBody` or `AtmosphericBody` **and its direct parent is any `StellarBody`** |
| `moon` | one of those same three concrete classes with a non-stellar parent |
| `minor` | `MinorBody`, `Asteroid`, `Comet`, `PeriodicComet` or `InterstellarComet` |
| `other` | any future/unknown concrete class |

This works for a modded multiple-star hierarchy; a stellar parent need not itself be a root. “Every
World” uses only `kind == "planet"`, excluding minor bodies/comets and moons. Preserve `class`
separately, test every row plus an unknown class, and record the table verbatim in C2, F7,
`docs/event-details.md` and player-facing copy. The Go server never infers kind from a name.

*Why this does not violate PROJ-033.* That decision deleted the celestial-body **allow-list** — a
list of body names compiled into the server. This adds no such list. The server learns what bodies
exist the same way it learns that a body exists at all: because it appeared in the event stream. The
difference from a `fastest_to_<body>` board is only that the mod now also reports the bodies a player
has *not* been to yet, which is exactly the missing fact that made the badge unanswerable before.

The result is correct in four cases a hardcoded list gets wrong, and this is the whole argument:

- **stock KSA ships four systems, not one.** `SolSystem.xml` (`Id: Sol`, the default) has **54
  celestial bodies**; `SolSystemDense.xml` (`SolDense`) has **3,215**; `EarthSystem.xml` (`SolLite`)
  has **three** — Sol, Earth, Luna; `EarthOnly.xml` (`Test`) has **two**. A player can pick any of
  them at launch. "Every planet" is already four different answers before a single mod is installed.
- **the next content patch**, on the day it ships, with no catlog change;
- **a system mod**, which registers itself simply by listing an XML file in its `mod.toml`;
- **a hand-edited file**, where "every planet" means something different for one player.

**The rule that survives, unchanged, and is still the most likely mistake in this plan:** the
*server* holds no list of bodies. If a task tempts you to write
`var planets = []string{"mercury", "venus", …}`, stop — you are re-adding `TimedBodies`
(PROJ-025 → PROJ-033). The list lives in the log, per system, because the game said so.

**And the badge is per-system, necessarily.** "Been everywhere" in a 200-body total conversion is not
"been everywhere" in stock, so the award is scoped to the system it was earned in and displayed with
that system's name.

### 3.8 A challenge is a **compile-time rule over an explicitly-dated window**, not runtime state

`server/internal/stats/challenges.go` holds a Go slice of challenge definitions. Each carries a key,
a title, a blurb, `Opens`/`Closes` as explicit unix-ms constants, a scope, a fold kind, and a
predicate. Adding a challenge is a commit and a deploy.

*Why not an admin API or a database table:* projections must be rebuildable, and
`TestRebuildEqualsIncremental…` must hold. If a challenge's definition lives in mutable runtime
state, a rebuild replays history against **today's** definitions while the incremental projection was
built against **yesterday's**, and the two disagree by construction. Constitution §5 makes the log
the only durable thing; a challenge definition is not in the log, so it has to be in the artifact.
It also fails Constitution §2's "no new machinery" instinct: an authoring UI, a validation layer, and
a migration for the definition table, to serve one person writing one challenge a week.

*Why not a TOML file with a predicate vocabulary:* it has the same rebuild property as the Go
registry (edit it, rebuild, history changes) so it buys no correctness — it buys "no deploy", at the
cost of inventing a mini-language. The request is explicitly for **arbitrary** rules, and arbitrary
means a Go func. The file *is* the interface, exactly as `catlog.toml`'s `[events]` table is
(ROADMAP.md, "An in-game editor for the `[events]` table").

*Why it is still safe to add a challenge whose window is in the past:* it is. A rebuild folds it and
the board fills from history — this is PROJ-090's "a new decoder or fold becomes retroactive by
rebuild, and no backfill script is written", applied unchanged.

### 3.9 A challenge's window is measured on `recv_time`, and the limitation is stated not engineered around

An event counts for a challenge when `Opens ≤ ev.RecvTime < Closes`.

*Why:* PROJ-043, verbatim and unchanged. The projector's rebuild replays history; a window taken
from `time.Now()` would file a two-year-old event in this week's challenge. `ev.RecvTime` is the
server's own authoritative stamp and is already in hand inside every fold. `wall_t` is the untrusted
client clock and is not a candidate.

*The honest limitation, which the player-facing site must state:* if you play offline and your
outbox does not drain until after the challenge closes, those flights do not count. There is no
grace period, because a grace period is a tuned threshold and the one-look test would fail. The site
says: *"your flights have to reach catlog before the deadline — if you play offline, get back online
before it closes."*

### 3.10 The whole wire change, in one table

Two new event types and five existing v1 payloads extended to their final pre-launch shape. **All
types remain `ver: 1`; there are no upcasters or compatibility payloads in this plan.**

| Type | Change | Serves |
|---|---|---|
| `system.discovered` | **new**, `ver 1`, locked on | §3.15–3.17: system identity and its friendly name |
| `system.body` | **new**, `ver 1`, configurable | the body set — "visited every planet", and the 3D contract; disabling it makes the catalogue incomplete |
| `flight.started` | existing `ver 1`: `+engine_count` | "reach Jupiter with no engines" |
| `vehicle.rud` | existing `ver 1`: `+part_count` | "most parts destroyed" |
| `kitten.tumble` | existing `ver 1`: `+from` | "did not land on their feet", as actually asked for |
| `vehicle.orbit` | existing `ver 1`: `+sma_m`, `lan_deg`, `argp_deg`, `t_pe`, `period_s` | §3.20 — draw the orbit a milestone happened on |
| `telemetry.window` | existing `ver 1`: `+state` (position **and** velocity, optional) | §3.20 — draw where the vessel was |

Everything else the community asked for is derived server-side from data already shipping.

*The discipline applied at every candidate field* — Constitution §3, the mod is a guest, and every
field is bandwidth, outbox rows and game-thread reads a player did not ask to pay for:

1. **Can the server derive it by joining `flight_state`?** If yes, it is not a wire change. That is
   why `vehicle.soi` does **not** gain `engine_count` and `vehicle.orbit` does **not** gain `kids`.
2. **Can it be derived from the career?** If yes, it is not an envelope field. That is why the
   **system is not on the envelope** despite being needed by almost every new board — a career has
   exactly one system, so the server joins it once (§3.15).
3. **Is it irreducible?** Parts at the moment of destruction cannot be recovered from
   `flight.started.part_count`, because staging changes it in between. Engines installed at launch
   are implied by nothing on the wire. A body's orbit is knowable only from the game.

*The one field that cost real thought* is `telemetry.window.state`, and §3.20 rule 2 is its
justification: it is the largest single wire cost here, and it is on the only droppable type on
purpose.

*Why these two are genuinely irreducible:* parts at the moment of destruction cannot be recovered
from `flight.started.part_count` because staging changes it between the two, and engines installed at
launch are not implied by any field currently on the wire.

### 3.11 `flight_state` gains derived columns, because a flight is the natural join key

`flight_state` gains `milestones INTEGER NOT NULL DEFAULT 0` (a bitfield) and
`engine_count INTEGER`, `part_count INTEGER`, `launch_mass_kg REAL` (from `flight.started`).

*Why:* the table already exists, is already written by `flightFold` for **every** flight-bearing
event, is already read by four boards for exactly this reason (`flightBody` supplies the body to
`biggest_recovery` and `most_stages`, whose own payloads carry none), and is already complete for
all history on a rebuild's second pass. Adding "what kind of vehicle was this, and what has it
achieved" to it is the same move, and it is what keeps the wire small (§3.10).

### 3.12 The two set-backed tables gain **career siblings**, and the originals are not touched

`player_body` and `kitten` keep their exact current shape, contents, primary keys and meaning. Two
new tables sit beside them: `career_body(player_id, career, kind, body, …)` and
`career_kitten(player_id, career, kid, …)`, written by the same folds on the same events.

*Why siblings rather than widened lifetime tables:* the lifetime and per-save sets answer different
questions, use different novelty signals and need different deterministic identities. Keeping them
separate makes those meanings explicit in the final fresh schema:

- **No sentinel.** An earlier draft used `career = ''` rows inside a widened `player_body` to keep the
  lifetime novelty count honest. That works, but it makes one table mean two things and makes every
  future reader ask which rows they are counting. Two tables, two questions, no ambiguity.
- **The novelty signals stay separate by construction.** `soi_bodies` increments from `AddBody`'s
  row-novelty report against `player_body`; the per-save board increments from the equivalent report
  against `career_body`. Neither can double-count the other, because they are different rows in
  different tables.

*The cost, stated:* a body reached in two saves stores three rows rather than two, and a kitten who
appears in two saves stores three rather than one. That is a projection, it is bounded by
players × saves × bodies, and it buys unambiguous lifetime and per-save semantics.

### 3.13 `kid` is not save-scoped, and the lifetime `distance_travelled` quirk is documented, not fixed

`kid` is `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` — it contains **no career**.
KSA's roster lives in `UniverseData`, which is the save, so a kitten called Mittens in save A and a
kitten called Mittens in save B are two different cats that collapse to one `kid`. The lifetime
`kitten` table merges them under `max()`, so `distance_travelled` reports the *furthest that name ever
got in any one save* rather than the total across saves.

`career_kitten` splits them, and the final greenfield lifetime board must sum those per-save rows.
Task A5.4 is therefore **mandatory**: `distance_travelled` becomes the total across saves, even when
two saves reuse the same roster name. There is no published data and no `BuildVersion` bump or
compatibility branch. Document the final meaning rather than preserving the pre-launch quirk.

### 3.14 Badge families reuse `[boards] min_players`, and challenges do not need it

A dynamic badge family member (`orbited_<body>`) is listed in the public badge index once
`min_players` distinct players hold it. Fixed badges are always listed. Challenge keys are curated
and always listed.

*Why:* PROJ-034's argument transfers unchanged — a badge for a place a single modified client
invented should not be able to fill the public index, and one holder is not a community achievement.
Reusing the existing config key rather than adding `[badges] min_players` is deliberate: it is one
threshold answering one question ("is this a thing more than one person has done"), and two knobs
that always get set to the same value are one knob with extra steps.


### 3.15 A **system** is a first-class dimension, and it is determined by the career

KSA's celestial system is **content, not code**: it is XML that ships with the game, that a content
patch changes, that a mod replaces, and that a player can hand-edit. Two players who both reach
something called `luna` may not have reached the same object, and ranking them against each other is
not merely uninteresting — it is **wrong**.

Two properties make this tractable, and both come from the game:

1. **A system cannot change during a session, and cannot change within a career.** It is chosen when
   the game loads a system and is fixed for the life of that save.
2. **It is fully readable at runtime.** The loaded system enumerates its bodies, their parents, their
   orbital elements and their classification, all derived by the game rather than guessed by us.

So the system is a **function of the career** — which is what keeps the wire cost at zero per event.
`career` is already on every envelope; the system is not, and must not be. The server learns
`career → system` once, from the discovery events (§3.16), and stores it on the `career` row.

**What if it changes anyway?** A player can edit the XML and reload the same save. The rule is the
one PROJ-023 already set for the clock running backwards: **mark the career, state the limitation,
build no machine.** The career keeps the **first** system it was seen in; a later, different hash
sets `system_changed` on that career. The mark excludes nothing and scores nothing — it qualifies a
number, exactly as `rewound` does, and for the same reason: a per-system comparison only means
something if the system held still.

### 3.16 The mod computes the hash and reports the bodies; the server still holds no list

The mod does one relatively expensive computation at session start and ships the result:

- a **system hash** over the system's name and its ordered bodies with a stable per-body quantity,
- and one event per body carrying that body's identity, classification, parent and orbital elements.

*Why the mod and not the server:* only the mod can see the loaded content. The server cannot know
what XML a player is running, and the whole point of PROJ-033 is that it must not try. This keeps the
existing invariant exactly: **the server holds no list of bodies** — it learns which bodies exist the
same way it learns anything, because an event said so.

*Why a hash rather than the system's name:* names are not unique and not stable. Two players can both
be running something called "Sol" with different contents; one player can edit a body's orbit and
keep the name. The hash is the identity; the name is the label. Publishing only the name would silently
merge two different systems, which is the failure this whole section exists to prevent.

*Why the name is in the hash anyway:* two systems with identical bodies but different names are, to a
player, two different things — a total conversion that happens to reuse stock orbits should not
collapse into stock. The name is cheap to include and its absence would be surprising.

**What the hash must be computed from** is a *stability* question, not a taste question, and Task C1
specifies it exactly. The standard it has to meet: **two players running byte-identical content must
produce the same hash, on different machines, on different runs, forever.** That rules out anything
float-precision-sensitive, anything that depends on enumeration order the game does not guarantee,
and anything mutable at runtime.

**The hash is exact catalogue identity, not a coarse fingerprint.** It includes both raw system id
and display name and every stable field represented by the final survey, including the complete
rotation/orientation and orbital set. Two catalogues that would publish different `system.body`
rows must hash differently. Task C1 pins the complete field order and canonical encoding; C3 still
retains first-write on an impossible/corrupt same-hash conflict rather than mutating shared data.

### 3.17 Two event types, not one chunked payload

`system.discovered` carries the header — hash, name, home body and body count; its envelope carries
the boundary's current `sim_t`. `system.body` carries **one body per event**.

*Why not one event with a `bodies[]` array:* `Wire.MaxEventLineBytes` is 16 KiB and a single line
over the cap wedges the outbox behind it. **This is not a hypothetical.** Stock KSA ships
`SolSystemDense.xml` with **3,215 celestial bodies** — at roughly 280 bytes of JSON each that is
about 900 KB in one line, fifty-six times the cap, in a file the player selects from the launcher.
Even the default `Sol` system is **54 bodies**, ~15 KB, which is already over. The alternatives were a **cap** (silently
wrong data, which is against the whole ethos) or **chunking** (an assembly protocol, a part index, a
"did every part arrive" question, and a new failure mode).

One event per thing is how catlog models everything else, it has no cap, no truncation flag and no
assembly step, and the events are **order-independent** — each carries its own system hash, so a
rebuild folding them in any order produces the same table.

*The cost, and why it is not what it first looks like:* the body set is sent **once per
`(career, system)`**, not once per session — the mod records that it has done so in the outbox's own
`shipper_state` table, which survives a restart (Task C2.3). The header event still goes every
session, because that is what binds a career to its system and it is one small event.

A re-send after a fresh outbox is harmless: every `system.body` is an immutable first-write keyed
`(hash, body)`, so folding the same survey twice is a no-op and a conflicting duplicate cannot
rewrite shared catalogue data. **That property is what makes the optimisation safe to have**, and
it is worth identical/conflicting duplicate tests rather than a comment.

### 3.18 System is the **third board scope**, and body boards say so

`stats.Scopes()` becomes `["player", "career", "system"]`, and `system_stat(player_id, system, stat, …)`
joins `player_stat` and `career_stat`. `?scope=system` ranks **(player, system) pairs**.

*Why a scope and not a filter:* "who is the best in this system" is a *ranking*, and a ranking needs
its own rows — a filter over `player_stat` cannot produce one, because `player_stat`'s key is
`(player_id, stat)` and a player who has played two systems has already had them merged into one row
by the time a filter could run.

*What happens to the player-scope body boards, honestly stated:* they still merge systems, because
that is what a lifetime board *is* — "the fastest you have ever reached anything called luna". The
site says exactly that and points the reader at `?scope=system` for the comparable question. This is
the same shape as every other honest-limitation note in catlog: state it, do not engineer around it.

`?system=<slug>` is a **filter**, valid on `scope=career` and `scope=system` (both carry the column)
and a `400` on `scope=player` with a message naming the scope that can answer — the same
"say which of the two it is" discipline `?period=` already follows.

### 3.19 A system's public identity is a hash, a name, and a slug — and unlike a career, it may be published raw

| Field | What it is | Where it is used |
|---|---|---|
| `hash` | The mod's stable identity for the content | Storage key, API key, joins |
| `name` | What the game calls it | **Every display surface, always** |
| `slug` | `name` through C3.4's dedicated ASCII slugger, with a `-2`, `-3` … suffix when two distinct systems share a name, assigned in first-seen order | URLs: `/boards/fastest_to_luna?system=sol` |

**The hash is not a deanonymisation hazard and must not be relabelled.** `career`, `kid` and
`install` are derived from the mod's install id, which is one *machine* and therefore one *person*,
which is why `readapi/privacy.go` relabels them per player (PROJ-049). A system hash is derived from
**public game content**: every player running stock KSA produces the same one. Relabelling it would
break the only thing it is for — grouping players who are in the same system.

*The residual, stated rather than hidden:* a player running a **bespoke** system produces a hash only
they have, so two accounts belonging to one person playing that system are correlatable. That is the
same class of soft correlator PROJ-050 already accepts and documents for kitten names and vehicle
names — content the player chose, which cannot be redacted without deleting the thing the view exists
to show. It goes in the same list.

*Why the slug and not the hash in URLs:* a URL is a thing people paste to each other. The collision
suffix is assigned deterministically by first-seen order, so it is stable and reproducible under
rebuild — the same trick as career ordinals (§3.5).

### 3.20 The data contract for a future 3D view — what ships, and what is deliberately not built

The owner wants it to be **possible later** to render a rough but honest 3D view of a system and a
vessel's path through it, in a browser, from catlog's own data. **Nothing in this plan builds that.**
What this plan does is make sure the log will contain enough, because a log cannot be back-filled and
a decision to record less is the one decision that cannot be undone (Constitution §5).

The contract is a completeness requirement on three payloads, subject to one owner decision:

| To draw | Needs | Where it comes from |
|---|---|---|
| The system, at any sim time | Per body: parent, the full Keplerian element set, the physical size, and the rotation axis/rate; exact ground tracks also require rotation phase at a stated epoch | `system.body` |
| A vessel's orbit at a milestone | The full element set, not just apoapsis and periapsis | `vehicle.orbit` |
| A vessel's path over time | A state vector — position **and** velocity — at a known sim time, relative to a named parent | `telemetry.window` |
| Where a thing happened | Body + latitude + longitude + altitude | already on `vehicle.rud`, `vehicle.impact`, `vehicle.landed`, `flight.started`, `flight.ended` |

**Three facts about KSA make this genuinely achievable rather than approximate**, all verified at
build 2026.8.5.5168 and all worth recording because they are what the contract rests on:

1. **A celestial's orbital elements never change.** `OrbitData` is a `readonly struct` of `readonly`
   fields assigned once in the body's constructor; the per-frame worker recomputes only state
   vectors; celestials are not serialised into the save at all; and the one setter that exists has
   zero call sites. So the motion is **pure two-body analytic** — no perturbation, no n-body, no
   drift — and one survey per career is the complete answer for that career's whole life.
2. **`TimeAtPeriapsis` is an absolute time on the same clock as `sim_t`.** Not an offset from the
   reading. So a consumer needs no knowledge of when the survey was taken.
3. **There are no barycentres.** Pluto and Charon are modelled as Pluto orbiting the star and Charon
   orbiting Pluto. A renderer must **not** invent a mass-weighted common centre — it would disagree
   with the game.

Two rules a task must not violate:

1. **Elements are recorded, never derived.** catlog ships what the game says and computes nothing
   orbital, on either side. The moment the server starts deriving positions it owns a physics model
   it cannot verify, and PROJ-099's *record it, never validate it* line is crossed.
2. **The vessel state vector lives on `telemetry.window`, and that is deliberate.** It is the single
   largest wire cost in this plan. It goes there because that type is windowed at 30 sim-seconds
   rather than per frame, because the window is already the "here is what this vehicle was doing"
   record, and — the decisive reason — because `telemetry.window` is the **only `KindPassive` type**,
   the only thing the outbox may drop under pressure. Visualization data is exactly what *should* be
   shed first when a player's spool is full, and putting it anywhere else would make it undroppable.

**Exact body-fixed placement is part of the contract.** Axis plus angular velocity does not define
prime-meridian phase, so `system.body` also carries a finite, normalised orientation-at-zero
quaternion read from `IParentBody.GetCcf2Cce(SimTime.Zero)`. Storage and the systems API preserve all
four components. Together they can place latitude/longitude events and ground tracks exactly in the
game's body-fixed frame; omission or a non-finite quaternion makes the catalogue incomplete.

What is **not** being recorded, so nobody adds it speculatively: no per-frame vessel positions, no
vessel attitude, no camera state, no terrain or mesh data, no textures or colours, and no derived
trajectory. The body-orientation quaternion is included solely because exact surface placement is
part of the chosen contract.
If a renderer later wants smoother paths it can interpolate, or a post-launch decision can lower the
window under the normal contract-version rules.

**Also surveyed, readable, and deliberately not shipped now** — Saturn's ring geometry, per-body
colours, named surface locations (Earth alone has some forty cities and mountains with latitudes and
longitudes), atmospheric scale height and sea-level density, and the terrain height envelope. Every
one is visual polish on a view that does not exist yet, and none of them changes what catlog *records
about a flight*. They are listed in Appendix D with their symbols so that whoever eventually builds
the renderer starts from a survey rather than doing one.

---

## 4. Constitution check

Written out because Constitution §8 is the principle most likely to be quietly violated by a feature
that sounds like fun, and because §9 requires the reasoning to exist before the code does.

| Principle | This plan |
|---|---|
| **§1** no email, handle is the only identity | Untouched. New surfaces publish handle + save ordinal + relabelled save id. §3.5. |
| **§2** cheap enough to forget about | Nine new tables (`career_stat`, `system_stat`, `career_body`, `career_kitten`, `system`, `system_body`, `badge_award`, `challenge_stat`, `challenge_member`), all bounded: the scoped ones by players × saves/systems × boards or challenges, and the system catalogue by *distinct systems* × bodies — one row set shared by everyone running stock KSA. **Career and system scopes are explicitly denied a period dimension** (§3.3) precisely to keep another multiplier from existing. No new process, job or service. |
| **§3** the mod is a guest | Two new v1 event types and five final-v1 payload extensions (§3.10). The system events are one cached survey emitted at a session boundary; the body rows are once per catalogue identity. The community fields are read at a patch point or poll the mod already uses. The two costs that needed arguing are the system enumeration (§3.16) and `telemetry.window.state` (the largest wire cost here, placed on the **only droppable type** on purpose, §3.20). Everything else is derived server-side. |
| **§4** everything runs locally | No new external anything. Challenges are compile-time; badges are folds; seed data covers all three (Tasks B7, G5, I5). |
| **§5** the log is immutable, everything else rebuilds | Every new table is a projection and the equivalence snapshot covers every table. The final deployment starts from a fresh database, but rebuild-from-log correctness remains mandatory because that is the production recovery model. |
| **§6** every number is derived, never claimed | Badge keys and challenge keys are compile-time constants; dynamic badge families use the same `statSuffix` protocol-hygiene rule as boards (PROJ-037). Nothing on the wire is a badge, a challenge score or a rank. **The system events are the one place the mod reports something the server cannot check**, and they are reports of *game content*, not of achievement — the same category as `game_build`. A modified client can invent a one-planet system and mint the everywhere badge; §8 already accepts that class of forgery, and the tier badges are no better protected. Stated in §3.16 rather than defended against. |
| **§7** moderation is trivial and total | Every new table is `player_id`-keyed and rebuilt from the log, so a purge and a shadow ban both clear it by the same route `player_stat` uses. `STORE-018` made the exclusion **structural** — a withheld player's events are not in the log at all — so a projection added later inherits it without knowing the feature exists. **Task A9 proves this rather than assuming it.** Note §7 was amended by that work to cover the shadow-ban verb explicitly. |
| **§8** anti-cheat is proportionate | **Nothing in this plan is an integrity check.** No badge, board or challenge infers cheating from data shape. A challenge does **not** exclude a rewound career (the mark still excludes nothing and scores nothing); the resolved §3.6 badge rule governs later corrections without adding a plausibility check. §8 was amended by the shadow-ban work to distinguish shadow-banning-as-moderation (a named human decision, built) from shadow-banning-as-anti-cheat (a machine inference, still forbidden); nothing here is either. |
| **§9 / §9.1** documentation is part of the system | Phase J is not optional and is not a follow-up. Every phase carries its own doc tasks inline; Phase J is the sweep that proves nothing was missed. |

**What this plan refuses,** recorded in `docs/ROADMAP.md` under *Deliberately not built* (Task J3) so
nobody re-argues it: a **server-side** list of celestial bodies (§3.7 — the list lives in the log
because the game reported it); a `kittens_scuttled` board (§8's consequence test); payload-versus-
booster mass and Δv (the game's own data is UI-gated and untrustworthy); and **building the 3D view**
— this plan only guarantees the data for it exists (§3.20).

---

## 5. The shadow-ban / rebuild work this plan sits on top of

**That work is finished.** This section records the seams the implementation uses. It does **not**
create an upgrade requirement for this greenfield plan; the final run starts with fresh databases.

Four pieces landed:

| Piece | What it is |
|---|---|
| `events/0004_event_seq.sql` + `store/events.go` | An explicit forward-only `event_seq` allocator. SQLite's rowid hands out `max+1`, which reuses a number whose row was deleted — and rows *are* deleted, by a purge and by a shadow ban's move. A reused seq sits behind the projector checkpoint and the archive cursor, so the re-issued event would be stored and then never folded and never archived, silently. (`STORE-017`.) |
| `events/0005_shadowban.sql` + `store/shadowban.go` | `shadowban` / `shadowban_event`, mirroring `event` column for column **including `seq`**. A withheld player's rows are **moved out of the log**, not filtered at read time. Restore is the move in reverse, at the original sequence numbers — so a restore reproduces the same boards *and* the same "who got there first" tie-break. (`STORE-018`, `IDENT-016`–`018`.) |
| `projector/job.go` | The rebuild is now **detached, observable and coalescing**: `POST /admin/projections/rebuild` answers `202` with a job, `GET` the same path reports phase / events scanned / head, and a request arriving mid-rebuild queues exactly one more pass. It no longer holds the admin mutex. (`PROJ-103`.) |
| `projections/0005_build.sql` + `stats/build.go` | **`proj_build`, `BuildID` and `BuildVersion`** — see below. (`PROJ-101`, `PROJ-102`.) |

### 5.1 `stats.BuildID` — read this before writing any fold

`BuildID` hashes the projections schema version, **the ordered names of every registered fold**, and
the hand-bumped constant `stats.BuildVersion`. It is stamped into `projections.db`, and at startup
the projector compares the file's stamp to its own.

| The live file | What happens |
|---|---|
| Stamp matches | Normal operation |
| Stamp differs, checkpoint is 0 | Folding forward from zero *is* a full build. Stamped, loop runs |
| Stamp differs, file holds history | **The fold loop is suspended** and a rebuild starts (`[projector] auto_rebuild`, default `true`). The old file keeps serving |
| Stamp differs, in-memory database | Logs loudly and carries on. Tests only |

**Two consequences implementers must preserve:**

1. **Fold names are stable and unique.** `BuildID` hashes ordered names, so badge and challenge fold
   names must include their key; adding a registry entry must change `BuildID`. Validate duplicate
   names and test this property. It also remains the post-launch mechanism by which a new fold asks
   for history to be replayed.
2. **No task in this plan bumps `stats.BuildVersion`.** `distance_travelled` and every other formula
   are being defined before launch against a fresh projection. The normal post-launch rule in
   `PROJ-102` remains true for later work, but implementing agents must not add compatibility
   machinery to this plan.

### 5.2 ★ The migrations construct one final fresh schema

Use sequential projection migrations because that is how every test and database constructor builds
the schema. There is no legacy database to transform and no acceptance criterion involving an older
binary or schema. The planned migrations happen to be additive (`CREATE TABLE`, `CREATE INDEX`,
`ALTER TABLE … ADD COLUMN`), and the sibling tables remain the clearest final design, but do not add
backfills, compatibility copies, destructive-migration workarounds or live-serving rationale.

### 5.3 Rules for every task in this plan

1. **`events.db` migrations `0004` and `0005` are taken.** No task here needs one.
2. **`projections.db`: `0005` is taken.** This plan uses `0006`–`0011`. Run
   `ls server/internal/store/migrations/projections/` before creating one — **verify, do not trust
   this plan**, which was already wrong about this once.
3. **Never reserve decision numbers in this plan.** At this audit the maxima are `PROJ-103`,
   `STORE-018`, `IDENT-018`, `OPS-035`, `MOD-080`, `UI-057`, `DOCS-004`, but every implementation
   task rechecks its area and takes the next free number in that same commit. Phase J audits this;
   it does not allocate the decisions after the fact.
4. **`server/internal/stats/` is untouched except for the new `build.go`.** Fold registration is
   exactly what this plan assumes: `Folds()`, `StateFolds()`, `SecondPassFolds()`, `BoardFolds()`,
   the four write helpers, `Batch`, every board. Nothing in Phases A–I has to be re-derived.
5. **`store/projections.go` gained one hunk and changed nothing existing.** `StatRow`, `Leaderboard`,
   `LeaderboardPeriod`, `StatAhead`, `StatPlayers`, `PlayerStats`, `RewoundCareers` and `Counts` are
   byte-identical to before. Tasks A7, F6 and H5 append to the end of that file and will not conflict.
6. **These files *were* rewritten — read them before touching them:** `store/events.go`,
   `store/store.go`, `store/identity.go`, `store/archive.go`, `store/directory.go`,
   `projector/projector.go`, `projector/rebuild.go`, `adminapi/projections.go`,
   `adminapi/identity.go`, `config/config.go`, `cmd/catlogd/main.go`, `cmd/catlogctl/projections.go`.
   H2 makes one narrow, explicit edit to `cmd/catlogd/main.go` to wire fail-fast challenge
   validation; no other task edits these files.
7. **Do not restructure `projector.Step`, `projector/rebuild.go` or `projector/job.go`.** New folds
   are *additive*: new files in `stats/`, appended entries in `BoardFolds()` or a new fold-list
   function. The one unavoidable edit to `projector/` is adding the new tables to the
   rebuild-equivalence snapshot (§5.4) — keep it to that.
8. **The rebuild verb changed.** `POST /admin/projections/rebuild` now returns `202` plus a job;
   `{"wait": true}` blocks for scripts; `GET` the same path reports progress;
   `catlogctl rebuild [-detach|-status]` is the CLI. Anything in this plan or in a test that assumed
   a synchronous rebuild needs the new shape (Task J5).
9. **Constitution §7 and §8 were amended** to distinguish shadow-banning-as-moderation (built, a
   human's decision about a named account) from shadow-banning-as-anti-cheat (still forbidden, a
   machine's inference from statistics). Nothing in this plan is either, but §4's compliance table
   was updated to match.
10. **`Rebuild ≠ incremental` gained a sixth divergence** (a shadow ban applied since the last
   rebuild) and a new *"The build stamp"* section in `docs/event-details.md`. Task J2's sweep must
   extend those, not overwrite them.
11. **This work helps ours, three times over.** A durable never-reused `seq` strengthens the
   `updated_seq` tie-break every new projection inherits; a structural shadowban means the new tables
   need no moderation wiring (confirm in Task A9); and `BuildID` means the new boards backfill
   themselves.

### 5.4 ★ Three hand-maintained lists every new projection table must be added to

Nothing in the compiler will remind you, and this plan adds nine tables.

| List | Where | What happens if you forget |
|---|---|---|
| The expected-DDL fixture | `server/internal/store/store_test.go` — `TestMigrationsCreateTheFullDDL`'s expected projections **table** list *and* its **index** list | A test failure, immediately. This is the friendly one. |
| The rebuild-equivalence snapshot | `server/internal/projector/projector_test.go` — the `snapshot` struct and `rig.snapshot()`'s per-table `dump(...)` calls | **Silent.** `TestRebuildEqualsIncrementalForAnUnflaggedHistory` passes while proving nothing about your table. This is the dangerous one, and it is why Task A8 exists as a task rather than a footnote. |
| The projection census | `server/internal/store/projections.go` — `Counts`; it has ten queries before this plan and gains one table count for each new table behind `RebuildResult` and `GET /admin/stats` | Silent under-reporting in the admin census. Cosmetic, but it is the number an operator uses to confirm a rebuild did what they expected. |

This plan adds **nine** tables: `career_stat`, `system_stat`, `career_body`, `career_kitten`
(Phase A), `system`, `system_body` (Phase C), `badge_award` (Phase F), and `challenge_stat` plus
`challenge_member` (Phase H).

`store.AllProjections` is still the single shared checkpoint key `"all"` — **not** a list, and it does
not need extending.

### 5.5 Two behaviours that will bite a test rig

1. **`Projector.Step` returns early when the build stamp does not match.** `Suspended()` is checked
   *before* `applyMu` is taken, so a rig that drives `Step`/`Drain` against a projections database
   stamped by a different fold set does **nothing at all**, silently, and the checkpoint never moves.
   A fresh in-memory database has checkpoint 0 and is therefore stamped-and-run rather than
   suspended, so ordinary `stats` and `projector` tests are unaffected — but a test that deliberately
   stamps a foreign build must expect it. `newRig(t, opts ...func(*projector.Options))` already takes
   functional options, so `func(o *projector.Options) { o.AutoRebuild = … }` needs no test-helper
   change.
2. **The schema version participates in `BuildID`.** Preserve that fact and its tests. This plan's
   release gate creates a fresh final database, so it does not stage or verify intermediate
   migration-only deployments.

---

## Phase A — career scope, server core

**Goal:** every board is rankable per save. No read surface changes yet; this phase ends with
`career_stat` correct, rebuildable, and proven equal under rebuild.

**Before starting:** read §5 in full. This phase is applied on top of the completed shadow-ban /
rebuild work. Confirm that work is committed before branching, and re-run
`ls server/internal/store/migrations/projections/`.

---

### Task A1 — the `career_stat` and `system_stat` tables, the career ordinal, and the career's system

**Files:** one new migration; `server/internal/store/store_test.go`;
`server/internal/projector/projector_test.go`; `server/internal/store/projections.go`;
`docs/server.md`; `docs/DECISIONS.md`.

1. `ls server/internal/store/migrations/projections/` and take the next free number. This task
   uses `0006`; **if it is taken, use the next one and shift every later migration in this plan.**

2. Create `server/internal/store/migrations/projections/0006_career_scope.sql`:

```sql
-- projections.db 0006 — a board, ranked per save and per celestial system.
--
-- A career is one KSA save played over time (§4.1 `career`). `player_stat` ranks
-- players; this table ranks (player, save) pairs on the same board keys, with the
-- same four merge rules and the same tie-break.
--
-- Why a table and not a compound stat key ("landings@<career>"): the argument is
-- in 0003_period.sql's header and has not changed. `stats.Describe` derives a
-- board's title, unit and direction from its key ALONE, and `stats.Catalog`
-- groups families by key prefix — both would have to start parsing. A scope is a
-- dimension of a board, so it is a column.
--
-- Why there is no period column here: a career already IS a time scope, and
-- players x boards x buckets x careers is a row count Constitution §2 has an
-- opinion about. `?scope=career&period=weekly` is a 400, deliberately.
--
-- Rebuildable from seq 0 like everything else in this file (D22).
CREATE TABLE career_stat (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,           -- 16 lowercase Crockford base32 chars, never ''
  -- The system this save is playing in, denormalised from `career` so that
  -- `?system=` is a predicate rather than a join (§3.18). '' until the career's
  -- `system.discovered` has been folded.
  system      TEXT NOT NULL DEFAULT '',
  stat        TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,                    -- JSON, same shape as player_stat.context
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, stat)
);
CREATE INDEX career_stat_system ON career_stat(stat, system, value, updated_seq);

-- The ranking index. `updated_seq` is in it because it is the tie-break, so the
-- ordering the read path asks for is covered end to end.
CREATE INDEX career_stat_rank ON career_stat(stat, value, updated_seq);

-- A per-player sequence number for a save, assigned in ascending first_seq order
-- and stable under rebuild.
--
-- It exists because the career id itself can never be published raw (it is
-- derived from the mod's install id, so it would link one person's two accounts
-- — see readapi/privacy.go and PROJ-049) and its per-player relabel is an opaque
-- 16-character token. A reader has to be able to recognise their own save. The
-- server deliberately never learns what the player called it, so an ordinal is
-- the most that can honestly be offered: "your third save".
--
-- 0 means "not yet assigned", which no row keeps after its first fold.
ALTER TABLE career ADD COLUMN ordinal INTEGER NOT NULL DEFAULT 0;
-- The last event seen for this save, whether or not it scored. Save pages use
-- this rather than max(board.updated_seq), which misses non-scoring and flagged
-- activity and fails entirely for a save with no board row.
ALTER TABLE career ADD COLUMN last_seq INTEGER NOT NULL DEFAULT 0;

-- --- the system scope ------------------------------------------------------
--
-- A celestial system is replaceable XML content (§3.15), so two players who both
-- reached something called `luna` may not have reached the same object. Ranking
-- them together is wrong, not merely uninteresting, so a system is the third
-- board scope and gets rows of its own.
--
-- The hash comes from the mod (§3.16). It is NOT install-derived and is
-- therefore NOT a deanonymisation hazard: every player running stock KSA
-- produces the same one, which is the entire point. It must never go through
-- readapi/privacy.go's per-player relabelling — that would break the only thing
-- it is for (§3.19).
CREATE TABLE system_stat (
  player_id   INTEGER NOT NULL,
  system      TEXT NOT NULL,           -- the system hash; never ''
  stat        TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, system, stat)
);
CREATE INDEX system_stat_rank ON system_stat(stat, value, updated_seq);

-- The career learns which system it is playing in, once.
--
-- A system cannot change within a career, so this is written from the first
-- `system.discovered` for that career and never overwritten. `system_changed`
-- is the provenance mark for the case a player edits the XML and reloads: it
-- excludes nothing and scores nothing, exactly like `rewound` (§3.15, PROJ-023).
ALTER TABLE career ADD COLUMN system         TEXT NOT NULL DEFAULT '';
ALTER TABLE career ADD COLUMN system_changed INTEGER NOT NULL DEFAULT 0;
```

**`career_stat` carries `system` as a column** so `?system=` is a predicate rather than a join. It is
denormalised from the `career` row on purpose, and it is written **at fold time from
`Batch.CareerSystem`** — never back-filled. In the final contract `system.discovered` precedes the
first session/scoring event, so a normal career never writes `''`; an unknown system is only a
defensive/out-of-contract case and is skipped consistently by incremental folding and rebuild.

`careerFold`/`AdvanceCareer` must set `last_seq = ev.Seq` for **every** event carrying that career,
including events that do not score and flagged events; first insert sets both `first_seq` and
`last_seq`. Add ordering/batch-size tests.

**The three hand-maintained lists move in this task, not A8:** add both tables/indexes and the new
schema version to the exact-DDL fixture; add empty ordered dumps to the projector snapshot; add both
tables to `Counts`. A migration that postpones any of these edits cannot keep `make test` green or
prove rebuild equivalence.

**Docs/decision in this commit:** final DDL and table roles in `docs/server.md`, plus the next-free
`PROJ` decision explaining career/system scopes and `last_seq` as all-activity provenance.

**Acceptance:** `make test` green; exact DDL/schema version, census and empty-table snapshot all pin
the final shape. No runtime board behaviour changes yet.

---

### Task A2 — `stats`: the scope vocabulary and the `Batch` accumulator

**Files:** new `server/internal/stats/scope.go`; edit `server/internal/stats/batch.go`.

**A2.1 — `server/internal/stats/scope.go`** (new file). Model it on `period.go`:

```go
package stats

// The scopes a board can be ranked in.
//
// `player` is `player_stat` and is what every existing URL means. `career` is
// `career_stat`, which ranks (player, save) pairs: one row per save, so a player
// with five saves legitimately holds five rows on a board and their best one
// sorts to the top on its merits. `system` is `system_stat`, which ranks
// (player, celestial system) pairs — the scope a body board actually wants,
// because a lifetime board for a *name* merges systems by construction.
//
// A scope is a **dimension of a board, not a board** — the same argument periods
// settled (PROJ-042). `GET /v1/leaderboards` stays one row per board and each row
// publishes the scopes it can be read in; `?scope=` selects one.
const (
	ScopePlayer = "player"
	ScopeCareer = "career"
	// ScopeSystem ranks (player, celestial system) pairs. It is the only
	// COMPARABLE scope for anything derived from a body name: KSA's system is
	// replaceable XML content, so two players who both reached something called
	// `luna` may not have reached the same object (§3.15, §3.18).
	ScopeSystem = "system"
)

// Scopes returns every value `?scope=` accepts, `player` first.
func Scopes() []string { return []string{ScopePlayer, ScopeCareer, ScopeSystem} }

// ValidScope reports whether s is a scope the API serves. The empty string is
// `player`, so an absent parameter keeps the semantic default.
func ValidScope(s string) (string, bool) {
	if s == "" {
		return ScopePlayer, true
	}
	for _, known := range Scopes() {
		if s == known {
			return s, true
		}
	}
	return "", false
}
```

**A2.2 — `batch.go`.** Add a fifth write accumulator beside the `player_stat` and
`player_stat_period` ones. Follow the existing `putStat` / `putPeriod` / `flushStats` /
`flushPeriods` shapes exactly — same `statKind` merge rules, same `pendingStat.merge`, same
`DefaultFlushRows` chunking.

```go
// careerStatKey is the primary key of career_stat. `system` is a written column,
// not part of the key — a save cannot change systems (§3.15), so it can never
// split a row in two.
type careerStatKey struct {
	playerID int64
	career   string
	stat     string
}

// systemStatKey is the primary key of system_stat.
type systemStatKey struct {
	playerID int64
	system   string
	stat     string
}

// putScoped records a record/best/count board write in EVERY scope beyond
// `player`, so a fold never has to name them and a scope added later composes
// for free.
//
// **Not for kindSet.** A derived total is a function of another table, and the
// per-save and per-system answers are different queries — see [setCareerValue]
// and [setSystemValue], which the three set-backed folds call explicitly.
//
// It is one function rather than one call per scope because the two scopes are
// not independent: `system` is a property of `career`, read through
// b.CareerSystem, and doing that lookup once per write rather than twice is the
// difference between a cached read and two.
//
//	career : keyed (player, career, stat); skipped when the event carries none
//	system : keyed (player, system, stat);  skipped when the career's system is
//	         not yet known. The final mod emits `system.discovered` before the
//	         first session/scoring event; this branch is defensive, not a
//	         pre-launch compatibility path or a documented divergence.
func (b *Batch) putScoped(ctx context.Context, kind statKind, ev Event, stat string, value float64, cx any) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putCareerStat(kind, ev, system, stat, value, cx)
	if system != "" {
		b.putSystemStat(kind, ev, system, stat, value, cx)
	}
	return nil
}

// CareerSystem reads the system a save is playing in, answering from this
// batch's own pending writes first. "" means not yet known.
func (b *Batch) CareerSystem(ctx context.Context, playerID int64, career string) (string, error)

// putCareerStat records a board write in the career scope.
//
// A no-op when the event carries no career: `sim_t` and every per-save number are
// only meaningful inside one, and a row keyed on "" would be every save at once.
// Stored events written before the `career` key existed decode to "" and are
// simply absent here — the all-time board still has them.
func (b *Batch) putCareerStat(kind statKind, ev Event, system, stat string, value float64, cx any) {
	if ev.Career == "" {
		return
	}
	// ... same body as putStat, keyed by careerStatKey, into b.careerStats[kind].
	// `system` rides along as a written column, not part of the key.
}

// putSystemStat is the same, keyed by (playerID, system, stat).
func (b *Batch) putSystemStat(kind statKind, ev Event, system, stat string, value float64, cx any) {}
```

The flush statement, mirroring `statFlush` (`batch.go:829-852`) with the PK widened:

```go
var careerStatFlush = [numStatKinds]string{
	kindRecord: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value > career_stat.value`,
	kindBest: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value < career_stat.value`,
	kindCount: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = career_stat.value + excluded.value, updated_seq = excluded.updated_seq`,
	kindSet: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value <> career_stat.value`,
}
```

**Add `flushCareerStats` to `Flush`'s fixed order** (`batch.go:983-993`), immediately after
`flushStats` and before `flushPeriods`:

```go
flushFlights, flushCareers, flushBodies, flushKittens, flushStats, flushCareerStats, flushSystemStats, flushPeriods, flushCensus
```

**The keys must be sorted before writing**, exactly as the existing flushes do — a rebuild has to be
byte-comparable to the incremental result, and Go map iteration order is not.

Also add a read-through accessor for the value, mirroring `StatValue`:

```go
// CareerStatValue reads a career-scoped board value, answering from this batch's
// own pending writes first so two reads in one batch see each other exactly as
// they did when each was its own statement.
func (b *Batch) CareerStatValue(ctx context.Context, playerID int64, career, stat string) (float64, error)
```

**Tests** (`server/internal/stats/`): add `career_scope_test.go` with a `readCareerStats` dumper in
the style of `readPeriods` (`period_test.go:27-53`), keyed `"<player>/<career>/<stat>"`.

**Acceptance:** `make test` green. Nothing writes to `career_stat` yet.

---

### Task A3 — fan the write helpers out into the career scope

**Files:** `server/internal/stats/fold.go`.

This is the whole of §3.2: three of the four helpers gain **one line**, and every board — fixed,
dynamic, and every board added after this — gets its career scope with no registry to update.

```go
func putBest(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindBest, ev.PlayerID, stat, value, cx, ev.Seq)
	if err := b.putScoped(ctx, kindBest, ev, stat, value, cx); err != nil {   // NEW
		return err
	}
	return periodBest(ctx, b, ev, stat, value, cx)
}

func putRecord(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindRecord, ev.PlayerID, stat, value, cx, ev.Seq)
	if err := b.putScoped(ctx, kindRecord, ev, stat, value, cx); err != nil {  // NEW
		return err
	}
	return periodRecord(ctx, b, ev, stat, value, cx)
}

func addCount(ctx context.Context, b *Batch, ev Event, stat string, delta float64) error {
	b.putStat(kindCount, ev.PlayerID, stat, delta, nil, ev.Seq)
	if err := b.putScoped(ctx, kindCount, ev, stat, delta, nil); err != nil {  // NEW
		return err
	}
	return periodAdd(ctx, b, ev, stat, delta)
}
```

**`setValue` gets NO fan-out, and that is deliberate.** It writes a *derived total* read from
another table (`soi_bodies` counts `player_body`, `distance_travelled` sums `kitten`), and the
player-scope total is not the career-scope total. Mirroring it would write the lifetime figure into
every save's row, silently. Instead add an explicit sibling:

```go
// setCareerValue writes a derived total in the career scope.
//
// Separate from [setValue] rather than folded into it because a derived total is
// a function of another table, and the per-save figure is a different query from
// the lifetime one. A fan-out here would write the lifetime number into a row
// labelled with one save — wrong, and wrong invisibly. The three folds that use
// setValue each compute their own career figure and call this beside it.
//
// There is no period form: `setValue`'s window write is an increase read from the
// previous value, and a career scope has no windows (see 0006_career_scope.sql).
func setCareerValue(ctx context.Context, b *Batch, ev Event, stat string, value float64) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putCareerStat(kindSet, ev, system, stat, value, nil)
	return nil
}

// setSystemValue is its system-scoped twin, and it takes a SEPARATE value.
//
// This is the same trap [setValue] has, one level down: a system's derived total
// is not one save's. "Bodies visited in the Sol system" is the union across every
// save you have played there, so it is its own query — and mirroring the career
// figure into the system row would write one save's number under a label that
// claims all of them. The three set-backed folds compute both.
func setSystemValue(ctx context.Context, b *Batch, ev Event, stat string, value float64) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil || system == "" {
		return err
	}
	b.putSystemStat(kindSet, ev, system, stat, value, nil)
	return nil
}
```

Add a doc note to `fold.go`'s helper block header naming the asymmetry, so the next reader does not
"fix" it.

**Tests** (`stats/career_scope_test.go`):

1. `TestEveryBoardKindWritesItsCareerScope` — one record, one best, one counter board, folded with a
   career; assert the `career_stat` row exists with the same value, context and `updated_seq` as the
   `player_stat` row.
2. `TestCareerScopeIsPerSaveNotPerPlayer` — the same player, two careers, three landings in career A
   and one in career B. Assert `player_stat` `landings` = 4, `career_stat` A = 3, B = 1.
3. `TestAnEventWithNoCareerWritesNoCareerRow` — `{noCareer: true}` input; `player_stat` moves,
   `career_stat` is empty.
4. `TestCareerScopeTieKeepsTheEarlierSeq` — two equal record values in one career; the earlier
   `updated_seq` survives.
5. `TestDynamicBoardsGetTheirCareerScopeForFree` — a `vehicle.rud` with a novel cause and a
   `vehicle.soi` to a novel body; assert `career_stat` rows exist for `rud_<cause>` and
   `fastest_to_<body>` with no registration anywhere. **This is the PROJ-044 property, restated for
   scopes, and it is the test that stops somebody adding an opt-out list later.**
6. `TestFlaggedFlightScoresNothingInEitherScope`.

**Acceptance:** `make test` green; `career_stat` populated for every board.

**Docs/decision in this commit:** update every board's scopes in `docs/event-details.md` and
`docs-site/src/data/boards.ts` together, plus the next-free `PROJ` decision for universal scope and
the no-opt-out rule. Task B6 adds explanatory pages; it does not postpone this contract update.

---

### Task A4 — career ordinals

**Files:** `server/internal/stats/batch.go`, `server/internal/stats/career.go`.

`EnsureCareer` / `AdvanceCareer` currently insert the `career` row. They must now assign
`ordinal = <this player's highest ordinal so far> + 1` on **first insert only**, never on update.

```go
// nextOrdinal is the sequence number the next new save of this player takes.
//
// Read once per player per batch from the table, then advanced in memory, so a
// batch that first sees three of a player's saves numbers them 1, 2, 3 in seq
// order rather than three times 1.
//
// Replay-stable by construction: careers are first seen in ascending seq order on
// both the incremental and the rebuild path, so the same save gets the same
// number every time. That is what lets the ordinal be a URL segment.
func (b *Batch) nextOrdinal(ctx context.Context, playerID int64) (int64, error) {
	if n, ok := b.careerOrdinals[playerID]; ok {
		b.careerOrdinals[playerID] = n + 1
		return n + 1, nil
	}
	var high int64
	err := b.tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(ordinal), 0) FROM career WHERE player_id = ?`, playerID).Scan(&high)
	if err != nil {
		return 0, fmt.Errorf("stats: read career ordinal high-water for %d: %w", playerID, err)
	}
	b.careerOrdinals[playerID] = high + 1
	return high + 1, nil
}
```

The insert becomes `INSERT ... ON CONFLICT (player_id, career) DO UPDATE SET ...` **without**
touching `ordinal`, so it is written once and never moves.

Add to `CareerState` in `stats/career.go`:

```go
	// Ordinal is this save's per-player sequence number, 1-based, assigned in
	// first-seen order. It is what a public surface shows in place of the career
	// id, which can never be published raw (PROJ-049).
	Ordinal int64
```

**Tests:**
- `TestCareerOrdinalsAreAssignedInFirstSeenOrder` — three careers interleaved across one batch;
  assert 1, 2, 3 by `first_seq`.
- `TestCareerOrdinalSurvivesLaterEvents` — more events in career 1 after career 2 appeared; career 1
  keeps ordinal 1.
- `TestBatchSizeDoesNotChangeOrdinals` — fold the same input at `batchSize` 1, 2 and 1000; identical
  ordinals. (Mirror `TestBatchSizeDoesNotChangeTheProjection`.)
- `TestRebuildReproducesOrdinals`.

---

### Task A5 — the two career sibling tables: `career_body` and `career_kitten`

**Files:** a new migration; `server/internal/stats/batch.go`; `server/internal/stats/boards.go`;
the exact-DDL fixture, projector snapshot and projection census; `docs/event-details.md`;
`docs-site/src/data/boards.ts`; `docs/DECISIONS.md`.

This is §3.12 and §3.13. **Read both before starting.** The sibling tables are the final-schema
design because lifetime and per-save sets have different identities and novelty semantics. There is
no legacy-data preservation or `stats.BuildVersion` work in this task.

**A5.1 — migration `0007_career_sets.sql`.** Purely additive: two `CREATE TABLE`s and two indexes.

```sql
-- projections.db 0007 — the per-save halves of the two set-backed projections.
--
-- The siblings answer different final-schema questions:
--
--   player_body  — "which worlds has this PLAYER been to"   (lifetime, unchanged)
--   career_body  — "which worlds has this SAVE been to"
--   kitten       — "what has this kitten done"               (lifetime, unchanged)
--   career_kitten— "what has this kitten done IN THIS SAVE"
--
-- The last pair matters because `kid` is
-- SHA-256("catlog-kitten:" + install_id + ":" + roster_name) and carries no
-- career, while KSA's roster lives in UniverseData — which is the save. A cat
-- called Mittens in save A and a cat called Mittens in save B are two different
-- cats sharing one kid. `kitten` merges them under max(), which is the lifetime
-- answer; `career_kitten` keeps them apart, which is the per-save one.
--
-- Novelty is what drives the two counter boards, and keeping the tables separate
-- is what keeps the two novelty signals from contaminating each other: the
-- lifetime board increments when a row is new in `player_body`, the per-save one
-- when a row is new in `career_body`, and neither can see the other's rows.
CREATE TABLE career_body (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,          -- 16 Crockford chars; never ''
  -- Denormalised from `career`, so the system-scoped set counts are one
  -- `count(DISTINCT body) … WHERE system = ?` rather than a join (§3.18).
  system      TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,          -- 'soi' | 'landed' | 'orbit_kid' (Task E3)
  body        TEXT NOT NULL,
  first_seq   INTEGER NOT NULL,
  first_sim_t REAL,                   -- seconds; NULL when the arrival carried no clock
  PRIMARY KEY (player_id, career, kind, body)
);
CREATE INDEX career_body_system ON career_body(player_id, system, kind, body);

CREATE TABLE career_kitten (
  player_id      INTEGER NOT NULL,
  career         TEXT NOT NULL,
  system         TEXT NOT NULL DEFAULT '',   -- denormalised, as on career_body
  kid            TEXT NOT NULL,
  name           TEXT NOT NULL,
  travelled_m    REAL NOT NULL DEFAULT 0,
  fastest_ms     REAL NOT NULL DEFAULT 0,   -- ecliptic-frame; must never become a speed board
  missions       INTEGER NOT NULL DEFAULT 0,
  mission_time_s REAL NOT NULL DEFAULT 0,
  kia            INTEGER NOT NULL DEFAULT 0,
  updated_seq    INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, kid)
);
CREATE INDEX career_kitten_player ON career_kitten(player_id, career);
CREATE INDEX career_kitten_system ON career_kitten(player_id, system);
```

**A5.2 — `Batch` methods.** Every existing method keeps its signature and its behaviour. Add a
sibling for each, and a second cache map keyed by `(playerID, career, …)`:

| Existing (unchanged) | New sibling |
|---|---|
| `AddBody(ctx, playerID, kind, body, seq) (bool, error)` | `AddCareerBody(ctx, ev, kind, body) (bool, error)` — no-op returning `false` when `ev.Career == ""` |
| `LowerBodyTime(ctx, playerID, kind, body, t) error` | `LowerCareerBodyTime(ctx, ev, kind, body, t) error` |
| `BodyCount(ctx, playerID, kind) (int64, error)` | `CareerBodyCount(ctx, playerID, career, kind) (int64, error)` **and** `SystemBodyCount(ctx, ev, kind) (int64, bool, error)` — `count(DISTINCT body)` over `career_body` for that player and system; `ok` is false when the system is unknown |
| `UpsertKitten(ctx, playerID, k, seq) error` | `UpsertCareerKitten(ctx, ev, k) error` — writes nothing when `ev.Career == ""`, because a roster reading that cannot be placed in a save belongs to no save |
| `KittenDistance(ctx, playerID) (float64, error)` | `CareerKittenDistance(ctx, playerID, career) (float64, error)` **and** `SystemKittenDistance(ctx, playerID, system) (float64, error)` |
| `KittenTops(ctx, playerID) (travelled, missions KittenTop, error)` | `CareerKittenTops(ctx, playerID, career) (…)` **and** `SystemKittenTops(ctx, playerID, system) (…)`; a system candidate is identified/tied by `(career, kid)`, not `kid` alone, because the same `kid` may occur in several saves |

`flushCareerBodies` and `flushCareerKittens` join `Flush`'s fixed order immediately after
`flushBodies` and `flushKittens` respectively, key-sorted like every other flush so a rebuild is
byte-comparable to the incremental result.

**Both new tables carry `system`, written from `Batch.CareerSystem` at insert.** They are set tables,
so a row is written once and never updated. The final pipeline guarantees discovery before any
qualifying body/roster event; if the system is unavailable, do not insert the scoped row. Incremental
and rebuild therefore make the same defensive choice—there is no pre-launch late-system divergence.

**The `KittenTops` tie-break stays total, never Go map order** (`batch.go:679`): lifetime by `kid`,
career by `kid`, and system by `(career, kid)`. `distanceFold` must compute the player, career and
system top rows independently and write each from its own query. Do **not** pass a lifetime
`KittenTop` through generic `putRecord` fan-out: that copies a kitten from another save/system into
the active scoped row. Add a player-only record helper (or explicitly suppress fan-out) for these
two boards, then call `putCareerStat`/`putSystemStat` with their own contexts.

**A5.3 — the three set-backed folds** in `boards.go`. Each keeps everything it does today and adds a
career arm:

```go
// soiFold — sketch. The same shape applies to landedBodiesFold and distanceFold.
//
// The lifetime SOI/landing halves keep their existing semantics. Distance is the
// deliberate greenfield correction in A5.4 and sums career_kitten.
func (soiFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[VehicleSOI](ev)
	if !ok || p.ToBody == "" {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}

	// --- lifetime: exactly as before ---------------------------------------
	isNew, err := b.AddBody(ctx, ev.PlayerID, "soi", p.ToBody, ev.Seq)
	if err != nil {
		return err
	}
	if isNew {
		n, err := b.BodyCount(ctx, ev.PlayerID, "soi")
		if err != nil {
			return err
		}
		if err := setValue(ctx, b, ev, StatSOIBodies, float64(n)); err != nil {
			return err
		}
	}

	// --- per save: new, and a no-op when the event carries no career --------
	newForCareer, err := b.AddCareerBody(ctx, ev, "soi", p.ToBody)
	if err != nil || !newForCareer {
		return err
	}
	n, err := b.CareerBodyCount(ctx, ev.PlayerID, ev.Career, "soi")
	if err != nil {
		return err
	}
	if err := setCareerValue(ctx, b, ev, StatSOIBodies, float64(n)); err != nil {
		return err
	}

	// --- per system: the UNION across this player's saves in that system ----
	// A separate query, never the career figure mirrored (see setSystemValue).
	sn, ok, err := b.SystemBodyCount(ctx, ev, "soi")
	if err != nil || !ok {
		return err
	}
	return setSystemValue(ctx, b, ev, StatSOIBodies, float64(sn))
}
```

`toBodyFold` additionally calls `LowerCareerBodyTime` beside its existing `LowerBodyTime`, so the
`fastest_to_<body>` career-scoped value has a per-save arrival time to read.

**Tests:**
- `TestLifetimeBodySetsAreUnchangedByCareerSiblings` — fold a history with two careers and assert
  `soi_bodies` and `landed_bodies` keep their lifetime union semantics. Distance is covered by the
  corrected final-schema test below, not by a pre-launch compatibility fixture.
- `TestCareerSetBoardsArePerSave` — the same body reached in two careers counts once per career and
  once lifetime.
- `TestKittenRowsSplitPerSave` — the same `kid` in two careers keeps two `career_kitten` rows and one
  `kitten` row.
- `TestKittenTopsComeFromTheirOwnScope` — the lifetime winner, save winner and system winner are
  three different `(career,kid)` candidates; assert every value and context names the right one.
- `TestDistanceTravelledSumsAcrossCareersEvenWhenKidRepeats` — two saves reuse a roster name and the
  lifetime total is the sum of both `career_kitten.travelled_m` rows.
- `TestARosterSnapshotWithNoCareerWritesNoCareerKittenRow`.
- `TestRebuildEqualsIncrementalForTheCareerSetTables`.

**A5.4 — mandatory: define the final lifetime `distance_travelled` correctly.**

Query `SELECT COALESCE(SUM(travelled_m),0) FROM career_kitten WHERE player_id = ?`. The old
pre-launch implementation merged same-named kittens across saves under `max()` and is not a contract
to preserve. Update the fold, both documentation halves and the next-free decision entry with the
final total-across-saves meaning. No version or compatibility work.

**The three hand-maintained lists move in this task:** exact tables/indexes/schema version,
equivalence snapshot dumps, and `Counts` for both new tables.

**Docs in this task's commit:** `docs/event-details.md` state-projection entries, the
`kid`-is-not-save-scoped note and corrected distance fold; the matching board data/site prose; and
the next-free decision entry. Do **not** document `orbit_kid` until Task E3 implements it.

---

### Task A6 — the two career-native boards

**Files:** `server/internal/stats/boards.go`, `server/internal/stats/fold.go`.

Two boards whose *subject* is the save itself. Both are ordinary boards that exist in both scopes —
at player scope `career_playtime` is "your longest-running save", at career scope it is "how long
this save has been played".

Add to the `StatXxx` constants and to `fixedBoards` (`boards.go:90-139`), **at the end of the table
so display order for every existing board is unchanged**:

| key | Title | Unit | Asc | Career | Fold kind | Source |
|---|---|---|---|---|---|---|
| `career_playtime` | `Longest Save` | `ms` | no | **yes** | record (max) | any event carrying `career` + `sim_t` |
| `play_sessions` | `Play Sessions` | `sessions` | no | no | count | `session.started` |

**`career_playtime` reads the event, never the `career` table.** This is the single most important
detail in the task:

```go
// careerPlaytimeFold records how far the career's own clock has run.
//
// It folds `ev.SimTime` directly and NEVER reads career.max_sim_t, even though
// that column holds the same number. careerFold is a state fold: on a rebuild it
// runs in pass 1, so by pass 2 the table already holds the career's FINAL
// high-water mark and every event would read it. The value would agree with the
// incremental result and the `updated_seq` would not — the rebuild would stamp
// the career's first event, the incremental path its last raising one. That is a
// rebuild-versus-incremental divergence for no benefit: max(sim_t) over the
// career's events IS max_sim_t, and putRecord's strictly-larger rule computes it
// replay-stably.
//
// There is no flag gate. A duration is not a feat: a teleport-flagged flight is
// one flight inside a save, and the save was still played for as long as it was
// played. This joins the four boards that already carry no flag exclusion for
// reasons of their own (see the Boards table in docs/event-details.md).
type careerPlaytimeFold struct{}

func (careerPlaytimeFold) Name() string { return StatCareerPlaytime }

func (careerPlaytimeFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	if !ev.HasCareer() || !ev.HasSimTime || ev.SimTime <= 0 {
		return nil
	}
	return putRecord(ctx, b, ev, StatCareerPlaytime, careerMillis(ev.SimTime), map[string]any{
		"career": ev.Career,
	})
}
```

`play_sessions` counts the initial session as well as later loads; it is not a resume count. It is
one line in `BoardFolds()`:
`countFold{stat: StatPlaySessions, eventType: "session.started"}`.

Append both to `BoardFolds()` in board-metadata order (i.e. at the end).

**`careerMillis` already exists** (`boards.go`, PROJ-047) — reuse it, do not re-derive. `Unit: "ms"`
means milliseconds and `units` renders it as a duration ladder (`5m 13s`, `243d 01h`); a career of
any length renders correctly with no units change.

**Tests:** golden value + tie-break + `TestCareerPlaytimeIsTheMaxSimTNotTheCareerTableReading`
(fold incrementally and refined, assert **identical `updated_seq`**) + `TestPlaySessionsCountsLoads`.

**Docs in this commit:** `docs/event-details.md` Boards table (two new rows, the count goes
40 → 42), `docs-site/src/data/boards.ts` (two entries), `docs-site/src/content/docs/leaderboards/catalog.mdx`
(two `<BoardDetail>` blocks in a new `## The save itself` section).

---

### Task A7 — the store reads

**Files:** `server/internal/store/projections.go` **only** (§5.3 rule 4 — do not touch `events.go`,
`store.go`, `identity.go`, `archive.go`, all of which the shadow-ban work rewrote). **Append the new
methods at the end of the file** rather than interleaving them among the existing ones.

Add, each mirroring its `player_stat` sibling exactly, including the canonical ordering
`ORDER BY value DESC|ASC, updated_seq ASC, player_id ASC, career ASC`:

```go
// CareerStatRow is a career_stat row. It is StatRow plus the save it belongs to.
type CareerStatRow struct {
	PlayerID   int64
	Career     string
	System     string
	Ordinal    int64
	Stat       string
	Value      float64
	Context    json.RawMessage
	UpdatedSeq int64
}

// system is "" for no filter. When set it is a `WHERE system = ?` predicate,
// which is why career_stat carries the column (§3.18).
func (p *Projections) CareerLeaderboard(ctx context.Context, stat, system string, asc bool, limit, offset int) ([]CareerStatRow, error)
func (p *Projections) CareerStatsForPlayer(ctx context.Context, playerID int64, career string) ([]CareerStatRow, error)
// Return EVERY matching save row for each id. One hidden player can own several
// rows ahead of a visible save; a player-level summary cannot correct the rank.
func (p *Projections) CareerStatsForPlayers(ctx context.Context, stat, system string, playerIDs []int64) ([]CareerStatRow, error)
func (p *Projections) CareerStatAhead(ctx context.Context, stat, system string, value float64, seq int64, asc bool) (int64, error)
func (p *Projections) CareerStatEntrants(ctx context.Context, stat, system string) (int64, error) // SAVES, not players

// Store-owned query type. store must never import stats: stats already imports
// store, so returning stats.CareerState would create an import cycle.
type CareerRow struct {
	PlayerID      int64
	Career        string
	Ordinal       int64
	System        string
	SystemChanged bool
	MaxSimT       float64
	Rewound       bool
	FirstSeq      int64
	LastSeq       int64
}
func (p *Projections) PlayerCareers(ctx context.Context, playerID int64) ([]CareerRow, error) // ordinal order
func (p *Projections) CareerByOrdinal(ctx context.Context, playerID int64, ordinal int64) (CareerRow, bool, error)

// --- the system scope ---
func (p *Projections) SystemLeaderboard(ctx context.Context, stat, system string, asc bool, limit, offset int) ([]SystemStatRow, error)
func (p *Projections) SystemStatsForPlayer(ctx context.Context, playerID int64, system string) ([]SystemStatRow, error)
func (p *Projections) SystemStatAhead(ctx context.Context, stat, system string, value float64, seq int64, asc bool) (int64, error)
func (p *Projections) SystemStatEntrants(ctx context.Context, stat, system string) (int64, error) // (player, system) PAIRS
type SystemRow struct { /* exact system columns from C3; store-owned, never stats.SystemState */ }
func (p *Projections) PlayerSystems(ctx context.Context, playerID int64) ([]SystemRow, error)
```

`SystemStatRow` is `CareerStatRow` with `System` in place of `Career`. Do **not** try to share one
struct across the two by making the field a union — the read paths are separate and a shared
"scope key" field would make every call site ask which it is.

Spell every `SELECT` column and `Scan` destination in the same order; tests must pin career id,
ordinal, system, `system_changed`, first/last seq, max sim time, rewound, stat value/context and
updated seq. The read API maps these store-owned rows into its own response types.

**Note the difference from `StatPlayers`:** `career_stat`'s PK is `(player_id, career, stat)` and
`system_stat`'s is `(player_id, system, stat)`, so `count(*) GROUP BY stat` counts **saves** and
**(player, system) pairs** respectively — not distinct players. Name both methods `…Entrants` so
nobody reads them as player counts, and say so in their doc comments: PROJ-034's "the PK *is* the
distinct-player count" note does **not** transfer to either.

**`[boards] min_players` is still evaluated only on the all-time player board**, for every scope.
That is PROJ-045's argument reused: a threshold that varied per scope would make the *index* vary by
scope, and a board that appeared under `?scope=system` but not `?scope=player` would be
indistinguishable from a bug.

**Tests:** `server/internal/store/projections_test.go` — ordering, paging, ties, and that
`CareerStatEntrants` counts saves.

---

### Task A8 — prove rebuild == incremental for every new table

**Files:** `server/internal/projector/projector_test.go`.

This is §5.4 applied. **The shadow-ban work did not touch `projector_test.go`**, so its current shape
is exactly what this plan assumes:

```go
type snapshot struct {
	Stats   []string
	Flights []string
	Bodies  []string
	Kittens []string
	Periods []string
	Feed    []string
	Cursor  int64
}
```

with `rig.snapshot()` dumping one ordered `SELECT` per table.

The migration tasks have already added their exact DDL and census entries. Here, finish the broad
equivalence harness: add `Careers`, `CareerStats`, `SystemStats`, `CareerBodies`, `CareerKittens` and
the existing-but-currently-omitted `Census` (`event_census`) fields plus ordered `dump(...)` calls.
The `Careers` dump must carry `ordinal`, `system`, `system_changed`, `first_seq`, `last_seq`,
`max_sim_t` and `rewound`. Confirm the general equivalence test actually diffs every field.

Add `TestRebuildEqualsIncrementalForCareerScope` with a history that exercises: two careers for one
player, an interleaved third, a flight flagged **before** any scoring candidate in one of them (so
this is not D22's intentional late-flag divergence), a career with no `sim_t` on some events, and
events with no career at all.

There is **no pre-feature-history divergence to support**: production starts with a fresh database
and the final mod emits discovery before the first session event. Add
`TestSystemDiscoveryPrecedesFirstScoreInFinalPipeline` and a rebuild-equivalence history beginning
with discovery. Do not add a compatibility fixture for histories produced by a binary that never
launched.

**Acceptance:** `make test` green, including `-run Rebuild`.

---

### Task A9 — moderation and the shadowban interaction

**Files:** read-only investigation, then a test.

Confirm that a purge and a shadow ban clear every new **player-owned** table that exists at this
point: `career_stat`, `system_stat`, `career_body` and `career_kitten`. Shared `system` and
`system_body` catalogue rows are intentionally not player-owned and may remain. Tasks F1 and H1
extend the same structural test when badge/challenge tables are introduced.

**The expected answer is "nothing to do", and the point of the task is to prove it rather than assume
it.** `STORE-018` made the shadow ban structural: the player's rows are *moved out of* `event` into
`shadowban_event`, so a rebuild produces projections that never saw them, and `PurgePlayer` deletes
from both tables. Every table in this plan is a projection rebuilt from seq 0, so all five empty out
for free — **provided nothing in the shadow-ban or purge path enumerates projection tables by
name.** Do not name future tables in this task's assertion.

**What to actually do:**
1. `git log --oneline -20`, then read `server/internal/store/shadowban.go`,
   `server/internal/adminapi/shadowban.go` and `server/internal/projector/job.go`.
2. Grep for any list of projection table names (`player_stat`, `flight_state`, `career`, …). If one
   exists, add every table implemented through C3, including `system_stat`.
3. Extend `server/internal/projector/shadowban_test.go` (it exists) to assert the new tables empty
   out for that player after the rebuild, and do the same for the purge test.
4. If nothing enumerates tables by name, write that finding into this task's next-free decision
   entry —
   "the new tables need no moderation wiring, because the exclusion is structural and every one of
   them is rebuilt from the log" — rather than leaving it unstated. A reader six months from now
   should not have to re-derive it.

---

## Phase B — career scope on the read API, app site, and player docs

**Goal:** a player can open a board and rank saves, and open their own list of saves.

---

### Task B1 — `?scope=` on the board endpoint, `scopes` in the index

**Files:** `server/internal/readapi/readapi.go`, `query.go`, `docs/ingest-api.md`.

**B1.1 — `BoardSummary` gains `Scopes []string \`json:"scopes"\`**, filled from `stats.Scopes()`.
Every board publishes all three, because every board has all three (§3.2).

It also gains **`BodyDerived bool \`json:"body_derived,omitempty"\``** — true for the
`fastest_to_` family and any future family whose
key is built from a body name. It is not a rule the server enforces; it is a **hint to a client**
that `?scope=player` merges systems on this board and `?scope=system` is the comparable question
(§3.18). Add this as authoritative metadata on `stats.Board`/the family catalogue and copy it into
the response; do not duplicate prefix detection in `readapi`.

**B1.2 — `handleBoard` parses `?scope=`** beside `?period=`, in this order, and **refuses the
combination**:

```go
	scope, ok := stats.ValidScope(r.URL.Query().Get("scope"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"scope must be one of "+strings.Join(stats.Scopes(), ", "))
		return
	}
	if scope != stats.ScopePlayer && period != stats.PeriodAllTime {
		// A career already is a time scope. Crossing it with a rolling window
		// would be a window over a window, and the row count is
		// players x boards x buckets x careers — see 0006_career_scope.sql.
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			scope+" scope has no time windows")
		return
	}

	// ?system= narrows to one celestial system. It is a predicate on a column
	// that only the two non-player scopes carry, and `player_stat` cannot answer
	// it even in principle: its key is (player_id, stat), so a player who has
	// played two systems was already merged into one row (§3.18). Say which
	// scope can answer rather than serving a silently wrong page.
	system := r.URL.Query().Get("system")
	if system != "" && scope == stats.ScopePlayer {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"system filtering needs scope=system or scope=career")
		return
	}
	if system != "" {
		hash, ok := s.systemBySlug(r.Context(), system)   // slug or hash, both accepted
		if !ok {
			s.writeError(w, http.StatusNotFound, authz.CodeNotFound,
				"catlog has never seen a system by that name")
			return
		}
		system = hash
	}
```

**B1.3 — `BoardResponse` gains `Scope string \`json:"scope"\``** and `BoardRow` gains two optional
fields:

```go
type BoardRow struct {
	Rank int    `json:"rank"`
	Handle string `json:"handle"`
	// Save is the player's own ordinal for the save this row belongs to — "their
	// third save". Present only on scope=career rows.
	Save int64 `json:"save,omitempty"`
	// SaveID is the per-player relabel of the §4.1 career key (readapi/privacy.go).
	// It groups one player's rows and links nothing across accounts. The RAW career
	// value is never published — see PROJ-049.
	SaveID string `json:"save_id,omitempty"`
	// System is on EVERY row in every scope that can carry it, because two rows
	// are only comparable if they are in the same system and a reader has to see
	// that without asking (§3.15). Unlike SaveID it is published RAW: a system
	// hash comes from public game content, not from the install id, and
	// relabelling it would break the only thing it is for (§3.19).
	System  *SystemRef      `json:"system,omitempty"`
	Value   float64         `json:"value"`
	Context json.RawMessage `json:"context,omitempty"`
	Updated int64           `json:"updated"`
	Rewound bool            `json:"rewound,omitempty"`
}

// SystemRef is how a celestial system appears anywhere in the read API. Always
// all three: the hash is the join key, the name is what a human reads, and the
// slug is what a URL carries.
type SystemRef struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}
```

**`SystemRef` is resolved once per page, not once per row.** The distinct systems on a page are a
handful; look them up in one query and share the value, the same way `rewound` is resolved with one
query per distinct player.

**B1.4 — `Server.Board` takes `scope`** and branches to `visibleCareerRows`, a copy of
`visibleRows` (`readapi.go:306-358`) over `CareerLeaderboard` with the same over-fetch-and-drop ban
filter, the same `maxScan = 5000` and the same `scanPage = 256`. **Do not** invent a second ban
filter; if the two functions can share their scan loop with a row-source closure, do that instead.

`Rewound` on a career row is read from the row's **own** career, so it applies to every board in
career scope, not only the `Board.Career` ones. Widen `rewound()` accordingly.

Factor the over-fetch/drop/positional-rank loop behind a typed row-source callback while doing this.
The current `visibleRows` is hard-coded to `player_stat`; badge and challenge holder pages later
need the identical visibility semantics without copying the algorithm. Keep entrants/holder totals
consistent with existing boards (the raw denominator remains ban-inclusive; visible positional
ranks close gaps on rendered/API pages) and pin that policy in tests and docs.

**There is no read-API version bump.** Document the final pre-launch shape in
`docs/ingest-api.md` §4.8 (both endpoint rows) and take the next-free decision number in this commit.

**Tests:** `readapi_test.go` — scope round-trip, the 400 on `scope=career&period=weekly`, ban
filtering in career scope, ordering, `save`/`save_id` present in career scope and **absent** in
player scope, and a redaction test asserting the raw career never appears.

---

### Task B2 — the two saves endpoints

**Files:** new `server/internal/readapi/saves.go`; register in `readapi.go`; `docs/ingest-api.md`.

```
GET /v1/players/{handle}/saves
  -> {"handle": s,
      "saves": [{"save": 1, "save_id": s,
                 "system": {"hash": s, "name": s, "slug": s},   // absent if never learned
                 "system_changed"?: true,
                 "playtime_ms": f, "first": unix_ms, "last": unix_ms,
                 "rewound"?: true, "boards": n}]}

GET /v1/players/{handle}/saves/{ordinal}
  -> {"handle": s, "save": 1, "save_id": s,
      "system": {"hash": s, "name": s, "slug": s}, "system_changed"?: true,
      "playtime_ms": f, "rewound"?: true,
      "stats": [{"stat", "title", "unit", "value", "ascending", "rank", "entrants",
                 "context"?, "updated"}]}
```

**Every save carries its system, by friendly name.** A save with no system yet — one played entirely
before this shipped, and not opened since — omits the key rather than sending an empty object;
`"system": null` and `"system": {}` would both invite a client to render a blank chip.
`system_changed` is the §3.15 provenance mark and, like `rewound`, is emitted only when true and
**excludes nothing**.

Rules, each mirroring an existing one:
- **Unknown / retired / banned handle → one 404**, the same answer, deliberately not a ban oracle
  (PROJ-007). Reuse the existing handle resolution in `readapi.go:428-431`.
- An ordinal with no career → 404 with distinct copy ("catlog has no such save for this player"),
  the same "which of the two is it" discipline `handleBoard` uses for windows.
- `first` / `last` come from `Events.RecvTimes` on `career.first_seq` / `career.last_seq` — never
  max board `updated_seq`, which misses non-scoring/flagged activity and saves with no board rows.
  projections.db and events.db **cannot be joined**, so resolve the distinct seqs in one events-db
  call just as board rows resolve `updated` (PROJ-010).
- `rank` is `CareerStatAhead(...) - hiddenAhead + 1`, re-applying the `better || (equal && earlier seq)`
  comparison in Go so a save's page can never contradict the board page. Use
  `CareerStatsForPlayers`: one banned player may own several career rows ahead, and **every** such
  row must be subtracted with the exact same value/tie comparator. A player-level lookup is wrong.
- `entrants` is `CareerStatEntrants` — **saves, not players**; label it that way in the response and
  on the page.
- Every response carries `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`, including
  the 404s. Route through `s.public(mux, ...)` so CORS is attached by the one place that attaches it.

Badge counts do not exist yet and are deliberately absent here. Task G2 adds them to save responses
and pages in the same commit as the badge projection/read path; Phase B must not ship placeholder
data sourced from a later phase.

---

### Task B3 — privacy: relabel, never publish, and pin it with a test

**Files:** `server/internal/readapi/privacy.go`, `privacy_test.go` / `redaction_test.go`.

`Label(playerID, kindCareer, career)` already exists and already produces the 16-character
per-player relabel. Every new surface uses it:

- `BoardRow.SaveID`
- `SavesResponse.Saves[].SaveID`
- `SaveResponse.SaveID`
- badge and challenge rows (Phases G, I)

**Extend `redaction_test.go` by naming what a regression would leak** — the existing test's spirit.
Add a table-driven test that walks the JSON of every new endpoint and fails if any string equals a
known raw career id. Make it generic over responses so Phases G and H inherit it:

```go
// TestNoPublicResponseCarriesARawCareer marshals every public response shape
// built from a fixture whose career is a known sentinel, and fails if that
// sentinel appears anywhere in the bytes. It is deliberately a blunt instrument:
// PROJ-049 was a live leak that survived review because the field was three
// levels down in a context blob, and a rule keyed on field names cannot see a
// field somebody adds tomorrow.
```

---

### Task B4 — the board page learns about scope

**Files:** `server/internal/web/web.go` (the `Read` interface + route), `pages.go`,
`templates/board.gohtml`, `templates/partials.gohtml`, `site/assets/css/catlog.css`,
`docs/ui-design.md`.

1. Widen `Read` with the new `Board(ctx, stat, scope, period, bucket string, limit, offset int)`
   signature and the two saves methods. **Never query `store` from `web`** — `web.go:4-11` says why,
   and the ban filter lives behind that seam.
   Parse and validate `scope`, `system`, `period`, `at`, `limit` and `offset` in `handleBoard`,
   rejecting invalid combinations with 400. Precompute every chip and pager URL from a copied
   `url.Values` so changing one dimension preserves all other applicable parameters. The current
   templates reconstruct URLs from only period/offset; retaining that code would silently reset a
   career board to player scope on pagination.
2. `board.gohtml` gains a **scope chip row** directly above the existing period chips, built exactly
   like them (`.chip`, `.selected`, `aria-current="page"`, an `<a href>` — anything that navigates is
   a link):

```gohtml
<nav class="periods" id="board-scopes" aria-label="Ranking">
  {{- range $b.Scopes}}
  <a class="chip{{if eq . $b.Scope}} selected{{end}}" data-scope="{{.}}"
     href="{{statPath $b.Stat}}{{if ne . "player"}}?scope={{.}}{{end}}"
     {{if eq . $b.Scope}}aria-current="page"{{end}}">{{scopeLabel .}}</a>
  {{- end}}
</nav>
```

   Add `scopeLabel` to `templateFuncs`: `player` → `Players`, `career` → `Saves`, `system` →
   `Systems`, falling back to the
   raw key so a future scope renders its own name (the `periodLabels` pattern, `pages.go:143-158`).

3. **When `scope=career`, the period chips are not rendered** and a one-line note says why:
   *"A save is already a period, so these boards have no time windows."* Sentence case, no
   exclamation mark, British spelling (`docs/ui-design.md` §9.2).
4. `board-table` gains two optional columns between Handle and the value:
   - **Save**, in career scope: `<a href="/p/{handle}/saves/{save}">Save {{.Save}}</a>`
   - **System**, in career **and** system scope: the friendly **name**, linking to
     `/systems/{{.System.Slug}}` after C7 exists. Never a bare `?system=` link: player scope rejects
     it and `/boards?system=` is not a route. Never the hash — the hash appears in the API and
     nowhere a person reads (§3.19).

   On a **body-derived** board in player scope (`body_derived` from Task B1.1), render a one-line
   note above the table instead of a column: *"This board ranks a **name**. If two celestial systems
   both have a Luna, both are here — see the same board [by system](?scope=system)."* Sentence case,
   British spelling, no exclamation mark.
5. Every value cell keeps `data-value="<the exact float>"` — the e2e suite reads that attribute and
   **never** the rendered text (`docs/ui-design.md` §4.4; a career board renders `5m 13s`, and
   stripping non-digits gives `513`, which sorts, so a text-based assertion passes while asserting
   nothing).
6. Add the new DOM ids (`board-scopes`, `board-scope-note`, `td.save`) to `docs/ui-design.md` §11's
   do-not-break list **in this commit**.

---

### Task B5 — the two saves pages

**Files:** `templates/saves.gohtml`, `templates/save.gohtml`, `templates.go` (`pageTemplates`),
`pages.go` (two handlers), `web.go` (two routes), `templates/profile.gohtml`, `web_test.go`,
`docs/ui-design.md`.

Routes, registered above the `GET /` catch-all:

```go
	mux.HandleFunc("GET /p/{handle}/saves", s.handleSaves)
	mux.HandleFunc("GET /p/{handle}/saves/{ordinal}", s.handleSave)
```

`/p/{handle}/saves` — one `.panel` with a table: Save · **System** · Played · First seen · Last seen ·
Boards. Task G2 adds the Badges column when badge data exists. `Played` is
`{{numUnit .PlaytimeMs "ms"}}` so it renders through the duration ladder.
**System** is the friendly name, linking to `/systems/{slug}`; a save with no system yet renders
the em dash `—`, which is the site's existing "no value" glyph, never `NaN`, `0` or blank. Empty
state: *"No saves recorded yet."*

`/p/{handle}/saves/{ordinal}` — the profile table, scoped. Reuse `board-table`'s cell partials
(`value-cell`, `context-cell`) rather than writing new ones. Header line:
*"Save 2 · Sol · played 4d 06h · #3 of 41 saves on Landings"* — note **saves**, not players (Task B2),
and the system named between the two. The rewound dagger and its exact tooltip come from `value-cell`
unchanged; `system_changed` gets its own mark beside it with the tooltip *"The celestial system this
save is in changed. Per-system comparisons before and after are not comparing the same worlds."*

Add a `Saves` link to `profile.gohtml`'s button row beside `Compare` and `Raw events`.

**Do not add a top-level nav entry.** Saves hang off a profile; the header nav is five links and
`docs/ui-design.md` §5.1's page inventory is a table somebody maintains — add the two rows to that
table instead.

**Tests:** two rows in the `TestEveryPageRenders` table in `web_test.go:421` plus fakes on
`fakeRead`; every case already asserts `<!doctype html>` and the datastar module.

---

### Task B6 — the docs-site half

**Files:** `docs-site/src/data/boards.ts`, `docs-site/src/content/docs/leaderboards/catalog.mdx`,
`docs-site/src/content/docs/leaderboards/index.mdx`, a new
`docs-site/src/content/docs/leaderboards/saves.mdx`, `docs-site/astro.config.mjs`,
`docs-site/src/components/BoardDetail.astro`.

**This is not a follow-up commit.** Constitution §9.1 — it lands with the code.

1. **`boards.ts`**: add `scopes: ("player" | "career" | "system")[]` to the `Board` interface with a doc
   comment distinguishing it from the existing `career: boolean` (which means "the *value* is a
   career-relative time" and is a completely different thing — the report from the frontend survey
   flagged this as the most confusable pair on the site). Add the two new boards
   (`career_playtime`, `play_sessions`). Render the scopes as a pill in `BoardDetail.astro` beside
   the existing `career time` pill.

2. **`catalog.mdx` — one paragraph must be rewritten.** It currently says, of the career-time boards:

   > The time is taken **per player**, not per career. Your best across all your careers is your
   > entry.

   That is still true *of the player-scope board*, and it is now only half the story. Replace with a
   passage that says both, in player language, and links to the new page:

   > The time is taken **per player** on the ordinary board: your best across all your saves is your
   > entry. Every board can also be read **per save** — see [Saves](/catlog/leaderboards/saves/) —
   > where each of your saves ranks on its own.

3. **New page `leaderboards/saves.mdx`** (`sidebar.order: 4`, add
   `{ label: "Per-save boards", slug: "leaderboards/saves" }` to the `Leaderboards` group in
   `astro.config.mjs`). It must cover, in player terms and with no code identifiers anywhere:
   - **What a save is to catlog**: one KSA game, played over as many sittings as you like. Closing
     the game and coming back to the same save is the same save. Starting a new game is a new one.
   - **What the ordinal is** and why it is a number rather than the name you gave it: *"catlog never
     learns what you called your save — the name is hashed on your own machine before anything is
     sent, so the server could not tell you even if it wanted to. Your saves are numbered in the
     order catlog first saw them."*
   - **One player can hold several rows** on a per-save board, and why that is the honest answer.
   - **Per-save boards have no time windows**, and why: a save is already a stretch of time.
   - **The rewind mark still excludes nothing.**
   - The two limits already in `docs/events.md`: deleting a save and starting a new game with the
     same name re-uses the identity, and a game that has never been saved gets a fresh one each time
     you start.

4. **`leaderboards/index.mdx`**: the `## Time windows` section says *"a period is a **view of a
   board**, not a separate board"*. Add the parallel sentence for scope so the two read as one idea.

5. **Link, do not re-explain, the empty-board behaviour.** `leaderboards/eligibility.mdx` already has
   a section *"A new board starts empty, not half full"*, added by the shadow-ban work, explaining
   that a deploy which adds a board suspends updates and recomputes rather than half-filling it.
   Every new page in this plan (saves, badges, challenges) will make a reader ask why their new board
   is empty on day one — **link to that section rather than writing a second copy of it.** Two
   explanations of one mechanism drift; this is the same rule `boards.ts` is under.

**Acceptance:** `cd docs-site && pnpm check` passes. Note `BoardDetail.astro` throws on an unknown
`stat`, so a board in a page but not in `boards.ts` is a **build failure** — which is the point.

---

### Task B7 — seed, sim and e2e

**Files:** `server/internal/seed/seed.go`, `mod/catlog.sim/Scenarios/`, `site/e2e/`.

1. **Seed.** `seed.go` already has `newCareer(n, simT)` and `seedCareer(handle, n)`. Give at least
   two demo players **two careers each**, with enough events that a per-save board has more than one
   row — otherwise `/boards/landings?scope=career` renders an empty demo and the e2e suite pins
   nothing. This is PROJ-040's lesson ("the demo dataset gained a second entrant on every family
   board, because otherwise the demo would show none of them") applied to scopes.
2. **`catlog.sim`.** `EventPipelineOptions.CareerId` is a trailing optional defaulting to a stable
   per-install career (PROJ-028), so existing scenarios keep working untouched. Add **one** scenario
   — `two-saves` — that runs the same short flight profile under two different career ids and
   asserts, via `ReadApiClient`, that the per-save board shows two rows and the player board one.

   **`ReadApiClient` needs a per-save assertion helper.** It has `ExpectRecord` / `ExpectCounter`
   against `GET /v1/players/{handle}`; add the career-scope equivalent against
   `/v1/leaderboards/{stat}?scope=career`. Note the shadow-ban work already rewrote
   `ReadApiClient.Rebuild()` for the detached endpoint — it now posts `{"wait":true,"reason":…}` and
   reads a nested `result` object — so **do not** reintroduce the old synchronous shape.
3. **`site/e2e/boards.spec.ts`** — a new test:
   - `/boards/landings?scope=career` renders the scope chips with `career` selected;
   - the Save column links to `/p/{handle}/saves/{n}`;
   - values read from `data-value` are sorted in the published direction;
   - the rendered rows equal `GET /v1/leaderboards/landings?scope=career` row for row;
   - `/boards/landings?scope=career&period=weekly` returns 400, and the page renders the
     "no such window" style 404/400 copy rather than an empty board;
   - **no assertion on the number of boards** — PROJ-039 (`toHaveCount(30)` was the assertion that
     said a client could assume a fixed board list).
   Before adding this test, repair the existing `FIXED_BOARDS` fixture: it currently names only a
   subset of the fixed catalogue. Generate it from an authoritative test export or explicitly list
   every fixed key from `stats.fixedBoards`; keep dynamic families separate. The assertion remains
   “every fixed key is exposed”, never “there are exactly N boards”.
4. **`site/e2e/`** — a new `saves.spec.ts` for the two pages.

**Acceptance:** `make test` green, `make e2e` green, `cd docs-site && pnpm check` green.

---

## Phase C — celestial systems: identity, the body catalogue, and the friendly name

**Goal:** every save knows which celestial system it is in; every system has a stable identity, a
friendly name and a URL slug; and the log contains enough about each system's bodies to answer
"have you been everywhere" and — later, if anyone wants it — to draw the thing.

**Read §3.15–§3.20 before starting.** Six decisions shape every line of this phase, and the two that
an implementer is most likely to get wrong are: **the server never holds a list of bodies** (§3.7),
and **the system hash is published raw while a career id never is** (§3.19).

**Phase C and Phase D are one commit stream in the mod** — both edit the final-v1 registry/payload
surface, regenerate `contracts/testdata`, and touch shared configuration/docs. Do C, then D, on one
branch; no event version changes.

---

### Task C1 — the system survey and the hash

**Files:** a new `mod/catlog/SystemSurvey.cs`, `mod/catlog.lib/Util/Ids.cs`,
`mod/catlog.lib/Telemetry/SystemSnapshot.cs`, `docs/ksa-integration.md`, `docs/mod.md`, and the
next-free `docs/DECISIONS.md` entry. Keep raw hash inputs separate from canonical wire values.

**Every symbol below was verified against `ksa-game-assemblies/current/decomp/KSA/` at build
2026.8.5.5168.** Record each with a `[KsaAnchor]` anyway — the anchors are what make the next build
bump a mechanical re-check rather than an investigation.

#### C1.1 — enumerate the bodies

```csharp
// Universe.CurrentSystem            KSA/Universe.cs:92
// CelestialSystem.Id                KSA/CelestialSystem.cs:61   → "Sol" | "SolDense" | "SolLite" | "Test" | a mod's
// CelestialSystem.All               KSA/CelestialSystem.cs:57   → LookupCollection<Astronomical>
// CelestialSystem.Count             KSA/CelestialSystem.cs:59   → ALL registered Astronomical,
//                                                               including vehicles; NEVER body count
// CelestialSystem.HomeBody          KSA/CelestialSystem.cs:55
foreach (IParentBody body in Universe.CurrentSystem.All.OfType<IParentBody>()) { … }
```

**`OfType<IParentBody>()` is exactly the celestial bodies and nothing else**, and that precision is
worth understanding rather than copying: `Celestial` and `StellarBody` implement `IParentBody`;
`Vehicle` does not (`KSA/Vehicle.cs:27`). The five template vehicles that stock content registers
**live in the same collection**, so any looser filter would put `Gemini7` in the star chart.

Materialise this filtered enumeration into a plain snapshot list on the game thread, sort by raw Id
ordinal, and use **that list's count** for `BodyCount`, `MaxSystemBodies`, completeness and tests.
`CelestialSystem.Count` is `_all.Count` and includes registered vehicles, so it is wrong for every
one of those uses.

`IParentBody` does not expose `Class`; each enumerated instance is also an `Astronomical`, so cast to
that base and let virtual dispatch return the concrete runtime class (`PlanetaryBody`,
`TerrestrialBody`, etc.). Preserve it as opaque `class`; the fixed semantic mapping in
§3.7 is a separate `kind` value.

**Model a forest.** `CelestialSystem` creates a tree for every template body whose parent is null;
mod systems may have multiple roots, and `GetWorldSun()` merely chooses the first star. Retain every
parentless body, compute `rank` as depth from that body's own root, and derive ordered `roots[]` from
body rows. There is no singular `root_body` field, invariant or display-root selection rule. Tests
cover two roots and a nested body under each.

For the required orientation, call `body.GetCcf2Cce(SimTime.Zero)` on the `IParentBody`; it returns
`Brutal.Numerics.doubleQuat` with public `X/Y/Z/W` fields. This already composes body-fixed → inertial
spin phase with inertial → body-centred-ecliptic orientation (`Celestial.cs:575-578`), so a consumer
does not have to reconstruct the missing azimuth from `axis`. Add anchors for that method and the
four fields and test the identity quaternion returned by `StellarBody`.

`LookupCollection<T>.TypeFilter<T2>` is a **`ref struct`** (`KSA/LookupCollection.cs:12`) that
exposes `GetEnumerator`/`MoveNext`/`Current`, so `foreach` compiles with **no LINQ and no
allocation** — and it wraps a `Span`, so it must not outlive the frame and must not be held across a
register/deregister.

#### C1.2 — ★ the ordering hazard, which is the one that would have broken this

**`CelestialSystem.All`'s order is not stable, in the same session, with the same content.**
`LookupCollection.Deregister` is a **swap-remove** (`KSA/LookupCollection.cs:148-161`), and
`Universe.DeserializeSave` calls `CurrentSystem.DestroyAllVehicles()` on **every save load**
(`KSA/Universe.cs:2152`) — which deregisters the five template vehicles and pulls five *celestials*
down into the vacated slots. Sandbox mode strips those vehicles entirely; an in-game rename
deregisters and re-appends.

So: **sort by `Id`, ordinal, ascending, before hashing anything.** An order-dependent hash would flip
between "hashed at boot" and "hashed after loading a save" and split one system into two. Put that
sentence in the code, not just here.

#### C1.3 — the hash input, and why every field earns its place

The old tab/newline formula is deleted: ids are content and can contain separators, text formatting
is locale-sensitive unless specified, and its small field set collides for catalogues whose stored
3D rows differ.

Define `SystemHashInput` in this exact logical order:

1. raw `CelestialSystem.Id`, raw `SystemInfo.DisplayName.Value`, raw home-body id and body count;
2. for every body sorted by raw id ordinal: raw id, optional raw parent id, concrete `class`, §3.7
   `kind`, and forest depth/rank;
3. `radius_m`, `mass_kg`, `soi_m`, `atmo_m`, `ocean_m`, `angvel`, all three `axis` components and
   all four finite normalised `ccf_to_cce_t0` quaternion components;
4. one presence byte for the six-value orbital group followed, when present, by `sma_m`, `ecc`,
   `inc_deg`, `lan_deg`, `argp_deg`, `t_pe`; then an independent period-presence byte and
   `period_s` when present.

Do not hash sanitised display/body strings, `complete`, or an envelope time: they are derived
presentation/health/session values, not content. Encode the sequence with the ASCII domain prefix
`catlog-system-v1`, canonical UTF-8 **length-prefixed strings**, explicit presence bytes and
fixed-width integers. Every string is `u32 byte-length + strict UTF-8 bytes`; body count/rank are
big-endian `i32`; optional parent/orbit/period use a single `0`/`1` byte before any value. Encode
finite doubles as canonical IEEE-754 binary64 bits in big-endian order,
normalising `-0` to `+0`. Encode `+Inf`, `-Inf` and all NaNs with three distinct canonical tags so
identity remains deterministic even when C2 marks the wire catalogue incomplete; never feed
platform NaN payload bits directly into the hash. Hash the bytes with SHA-256, take the first ten
bytes, and Crockford-lowercase them to 16 characters. Pin a complete known vector, reordering,
culture (`fr-FR`), `-0`, NaN/Inf and separator-bearing-id tests.

The source inventory must distinguish authored/template values from runtime-derived values. The
chosen rule intentionally hashes the canonical surveyed output—even the game-derived axis,
orientation and period—because a different published row must have a different identity. Pin those
values on multiple supported runtimes. Machine-dependent terrain sampling, mutable state vectors,
positions and cosmetic textures/colours remain excluded because `system.body` does not publish
them. Record every inclusion/exclusion with its source and reason in `docs/ksa-integration.md`; the
numbered list above is normative.

**No install-id salt**, unlike `Ids.CareerId` and `Ids.KittenId`. The hash must be *identical* for
every player running the same content — that is the entire feature (§3.19). Put that in the doc
comment, because it looks like an omission.

`Ids.SystemId(SystemHashInput)` lives in **`catlog.lib`** and takes no KSA type, so it is testable
without the game. `SystemSnapshot` contains canonical wire values and a separate hash input; do not
force raw identity fields and lowercased join keys into one record.

#### C1.4 — when to read it, and the silent failure to guard

**Read at a POSTFIX on `Universe.LoadSystem(string)`** (`KSA/Universe.cs:167`). `CurrentSystem` is
assigned at `:174`, *after* the constructor returns, so a prefix would read the previous system or
null. `LoadSystem` runs **once per game launch** — a save load goes through `Universe.DeserializeSave`
and does **not** reload the system — so this is one enumeration per launch, not per session.
**Cache the survey and re-emit the cached copy at each session boundary** (Task C2.3); the cost
question in C2.4 then barely exists.

At startup, `StarMap.AllModsLoaded` currently runs before KSA's default `LoadSystem`, so the postfix
does catch the initial load. Also handle `Universe.CurrentSystem` if startup ordering changes and a
system is already present; never fabricate a survey from null.

**The silent failure:** `CelestialSystem`'s constructor **swallows per-root exceptions**
(`KSA/CelestialSystem.cs:139-153`), so a modded body that throws takes its subtree with it, the
system still loads, and `CurrentSystem` looks complete. **Hash and report what is actually
registered** — the materialised `IParentBody` enumeration is the truth, the template and
`CelestialSystem.Count` are not. A
partially-loaded system produces its own honest hash rather than pretending to be the intact one.

#### C1.5 — the snapshot type

`SystemSnapshot` and `SystemBodySnapshot` are plain immutable records in **`catlog.lib`**
(`Telemetry/`), filled by the game project. `catlog.lib` must never see a KSA type and the assembly
guard test enforces it. The cache holds raw/hash inputs separately from lowercased wire join keys;
session/career/sim/wall timestamps are supplied only when events are emitted, never cached in the
survey.

---

### Task C2 — the two new event types

**Files:** `mod/catlog.lib/Events/EventTypes.cs`, `Payloads.cs`, `GameSignal.cs`,
`Detect/EventPipeline.cs`, `Config/ModConfig.cs`, `mod/catlog/SystemSurvey.cs`, `Patcher.cs`,
`CatlogRuntime.cs`, `mod/catlog.lib/Events/Wire.cs`, `mod/catlog.lib/Storage/OutboxDb.cs` and their
tests; `server/internal/ingest/types.go`; typed Go payload/decode cases; conformance generator and
vectors; `docs/events.md`; both event-detail/site halves; next-free decision entry. Appendix E is the
full end-to-end checklist for a new type — follow its greenfield branch.

**C2 is one indivisible vertical contract commit.** Registry, locked configuration, mod payload,
server acceptance/decode, vectors and both documentation halves land together and leave
`make test` green (`INGEST-025`). Do not delegate “mod event” and “server support” separately: an
unknown type rejects its entire batch.

**C2.1 — the registry.** Two constants and two `Versions` entries at `1`.
`system.discovered` joins `AlwaysReportedTypes` because career→system attribution is load-bearing;
the locked set changes from five to **six**. `system.body` remains configurable: disabling it gives
up catalogue/everywhere/3D functionality but cannot improve a score because effective completeness
fails. Update `NoTomlKeyCanTurnOffTheSpine`, its exact-name test and the header sentence together.
The `[events]` example marks only `system.discovered` `# locked on`.

When `system.body` is disabled, Runtime emits `system.discovered` with `complete: false`, emits no
body rows and does **not** write the durable survey marker; enabling it later permits the next
session boundary to send the catalogue. The cap or an invalid required numeric has the same
header/body outcome for a different documented reason.

**C2.2 — the payloads.**

```csharp
// system.discovered — one per session, before session.started.
public sealed record SystemDiscoveredPayload(
    [property: JsonPropertyName("system")]   string System,      // the hash
    [property: JsonPropertyName("id")]       string Id,          // CelestialSystem.Id — "Sol", "SolDense", a mod's
    [property: JsonPropertyName("name")]     string Name,        // SystemInfo.DisplayName, sanitised, <= 64 ASCII
    [property: JsonPropertyName("home")]     string HomeBody,    // canonical lowercase BodyName key
    [property: JsonPropertyName("bodies")]   int    BodyCount,   // materialised IParentBody snapshot count
    // False whenever the body list was NOT sent: system.body is disabled, the
    // count exceeds MaxSystemBodies, or a required numeric is invalid. The set
    // is unknown rather than empty; everywhere badges award nothing.
    [property: JsonPropertyName("complete")] bool   Complete);

// system.body — one per celestial, in any order.
public sealed record SystemBodyPayload(
    [property: JsonPropertyName("system")]   string  System,     // the hash, so order does not matter
    [property: JsonPropertyName("body")]     string  Body,       // canonical lowercase BodyName key
    [property: JsonPropertyName("name")]     string  Name,       // sanitised raw Id; KSA has no body display-name field
    [property: JsonPropertyName("class")]    string  Class,      // concrete runtime class, opaque
    [property: JsonPropertyName("kind")]     string  Kind,       // §3.7's fixed normalised semantic kind
    [property: JsonPropertyName("rank")]     int     Rank,       // depth from the root
    [property: JsonPropertyName("parent")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    string? Parent,                                              // canonical key; absent for root
    // --- physical -----------------------------------------------------------
    [property: JsonPropertyName("radius_m")] double  RadiusM,        // mean radius, metres from centre
    [property: JsonPropertyName("mass_kg")]  double  MassKg,
    [property: JsonPropertyName("soi_m")]    double  SoiM,           // +Inf on the star — send it as 0
    [property: JsonPropertyName("atmo_m")]   double  AtmoM,          // 0 when airless
    [property: JsonPropertyName("ocean_m")]  double  OceanM,         // ocean level above mean radius; 0 when none
    // Rotation: what a renderer needs to place a ground track or a marker.
    // `axis` is the rotation axis in the body-centred ECLIPTIC frame, which is
    // the unambiguous form — tilt and azimuth separately would need the reader
    // to know the convention.
    [property: JsonPropertyName("angvel")]   double  AngVelRadS,     // signed: negative is retrograde
    [property: JsonPropertyName("axis")]     Vec3    AxisCce,
    [property: JsonPropertyName("ccf_to_cce_t0")] Quat CcfToCceT0, // finite normalised x/y/z/w
    // --- six-value orbital-shape group; every key absent on the root ---------
    [property: JsonPropertyName("sma_m"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? SmaM,
    [property: JsonPropertyName("ecc"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? Ecc,
    [property: JsonPropertyName("inc_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? IncDeg,
    [property: JsonPropertyName("lan_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? LanDeg,
    [property: JsonPropertyName("argp_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? ArgpDeg,
    [property: JsonPropertyName("t_pe"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? TPe,
    // Independently absent for an unbound conic (the game reports NaN).
    [property: JsonPropertyName("period_s"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? PeriodS);
```

`Quat` is a four-double immutable wire record (`x`, `y`, `z`, `w`), with a matching typed Go struct;
it is not encoded as an anonymous array whose component order a weaker agent could reverse.
Normalise it, then canonicalise the equivalent `q`/`-q` representations: the first non-zero
component in `w,x,y,z` order must be positive, and every `-0` becomes `+0`. The hash consumes this
same canonical quaternion. Pin identity, a normal quaternion, its negation and a 180-degree (`w=0`)
case.

`SystemInfo.DisplayName` is launcher metadata, not a `CelestialSystem` property. Resolve the loaded
system id through the public system-selection metadata and fall back to the raw id if no entry is
found; sanitise once. A body's raw `Id` is its only display name.

**Join-key rule:** `body`, `parent` and `home` go through the exact canonical lowercase
normalisation used by `VehicleTelemetry.BodyName`. Raw KSA ids remain in `SystemHashInput`, not in
these join columns. Without this rule stock `Earth` catalogue rows cannot join event body `earth`.

**`mu` is deliberately not sent.** It is `Mass * 6.6743E-11` — a default interface method on
`IParentBody` (`KSA/IParentBody.cs:15`) — so `mass_kg` carries it exactly and a consumer multiplies by
the same constant. Sending both would be two numbers that can disagree.

Rules that are not negotiable:

- **The six shape keys are optional as a group** and are all absent on the root body, which has
  no orbit. Absent, never `0` — a zero eccentricity is a real circle and a zero inclination is a real
  equatorial orbit (the omit-don't-zero rule, MOD-078).
- **`period_s` is `NaN` for an unbound orbit** in the game (`KSA/OrbitData.cs:73`) — three stock
  interstellar comets hit it. JSON has no NaN: **omit the key** in that case, and say so, because a
  decoder that reads it into a plain `float64` would otherwise get `0` and draw a stationary comet.
- **Angles are degrees on the wire**, converted at the read like `vehicle.orbit.inc_deg` already is.
  The game stores radians; do the conversion once, in the mod, and say so in the anchor.
- **`t_pe` is an ABSOLUTE time in the game's own base** — the same clock `sim_t` is measured in, with
  its zero at the start of the career — **not an offset from the reading.** Verified:
  `GetElapsedTimeSincePeriapsis(t) => t - TimeAtPeriapsis` (`KSA/Orbit.cs:1332-1340`), and stock XML
  carries negative values for bodies whose last periapsis preceded t=0. This is what makes §3.20's
  "the system at any sim time" answerable **without** knowing when the survey was taken.
- **A celestial's elements never change** — `OrbitData` is a `readonly struct` of `readonly` fields
  assigned once in the constructor, the per-frame worker recomputes only state vectors, and celestials
  are not serialised into the save at all. So one survey per career is not an approximation; it is the
  complete answer for that career's whole life. Put that sentence in `event-details.md`: it is the
  reason this event can be emitted once instead of sampled.
- **`class` is the game's own string, opaque to the server**, exactly like `body` and `situation`.
  There is no allow-list. `kind` is §3.7's explicit semantic mapping; the server never
  guesses from `class` or body names.
- **Every emitted numeric is finite.** Treat the root's documented `soi_m = +Inf` as the sole
  representational exception and emit `0` for it. If any other required physical scalar or any axis
  component is non-finite, mark the header `complete: false` and emit **no** body rows; do not publish
  a partly plausible immutable catalogue. If any of the six orbital-shape values is non-finite,
  omit that whole group for the body. Omit `period_s` unless finite. Never zero one bad vector
  component. The whole survey is incomplete unless all four orientation components are finite and
  the quaternion normalises. Add NaN and ±Inf serialization tests
  asserting the exact header/body outcome and key absence.

**C2.3 — the emission point, and the once-per-career rule.**

*Survey* in the `Universe.LoadSystem` postfix and cache the immutable snapshot. Do **not** add a
separate `SystemSurveySignal`: the current session seam cannot order it correctly because
`EventPipeline.OnSessionLoaded` itself calls `Tracker.NewSession()` and immediately creates
`session.started`.

Instead, add `SystemSnapshot?` to `SessionLoadedSignal`. In one `OnSessionLoaded` order: reset and
call `Tracker.NewSession()` so the new ids exist; emit `system.discovered` and any body rows; then
emit `session.started`. Refactor `CatlogRuntime.Start()` to use the same boundary path rather than
directly appending `session.started`. Split the callbacks: the `LoadSystem` postfix captures the
survey and establishes a boundary; the `DeserializeSave` postfix reuses the cached survey for the
loaded save's boundary. If `CurrentSystem` is null, emit no phantom system/session pair; the later
`LoadSystem` postfix establishes it. Tests assert the new session/career ids and exact
`system.discovered` → all bodies → `session.started` order on startup and save load.

`EventPipeline` does not own `OutboxDb`; `CatlogRuntime` does. Runtime reads
`survey:<career>:<hash>`, tells the pipeline whether body envelopes are needed, and always leaves the
header enabled. When body rows are needed, Runtime appends the complete ordered header/body/session
batch and advances the marker in the same `AppendAndSetState` transaction; a marked survey emits a
header/session batch with no bodies. Tests cover marked/header-only, unmarked/body-inclusive, and an
append failure that leaves the marker unset. A disabled `system.body` test asserts
`complete:false`, no body rows and no marker; re-enable and cross another session boundary to prove
the catalogue is then sent.

**Send the body events only once per `(career, system_hash)`.** The header
(`system.discovered`) goes every session — it is one small event and it is what binds the career to
the system. The body events do not: `OutboxDb`'s `shipper_state` stores
`survey:<career>:<hash>` and persists across game restarts. At stock `Sol` that turns
54 events per save load into 54 once; at `SolDense` it turns **3,215** per save load into 3,215 once,
which is the difference between affordable and rude.

If the mod cannot tell (a fresh outbox, a new career), it re-sends — and the server folds it
idempotently, because every `system.body` is immutable first-write keyed `(hash, body)`. **Re-sending must always
be correct**, and the test for that is a fold of the same survey twice producing a byte-identical
table.

**Crash rule:** commit the marker only after every catalogue event is in the outbox. Prefer an
`OutboxDb.AppendAndSetState` transaction covering the event rows and marker; append-then-mark is an
acceptable fallback because its crash window merely resends. Mark-then-append is forbidden because
a crash permanently loses the catalogue. Add reopen and injected-failure tests.

**The cap.** `Wire.MaxSystemBodies = 5000`. Above it, emit the header with `complete: false` and
**no body events at all** — never a truncated list, because a partial set would make the everywhere
badges wrong in the one direction that matters (awarding them to somebody who has not been
everywhere). `SolDense`'s 3,215 sits comfortably under it; the cap exists for an adversarial or
generated system, and it is a documented refusal rather than silent truncation.

**Read on the game thread**, hand the worker a plain immutable record. `catlog.lib` must never see a
KSA type and the assembly guard test enforces it. If the system reads as null, **emit nothing** — a
missing system is a known state (`''`) and a hash of nothing is not recoverable.

**C2.4 — cost.** One enumeration per system load, on the game thread at a load boundary rather than
inside the steady frame loop. Measure it with the status
window's diagnostics open, at `Sol` and at `SolDense`, and record both numbers in `docs/mod.md`:
`SolDense` is 3,215 bodies and is the honest worst case somebody will actually run.

---

### Task C3 — the server: two projections and a state fold

**Files:** a new migration `0008_systems.sql`, `server/internal/stats/system.go`, `fold.go`,
`server/internal/store/projections.go`; exact-DDL fixture; projector snapshot; census;
`docs/server.md`; `docs/event-details.md`; docs-site system/event data; next-free decision entry.

**C3.1 — contract precondition.** C2 already added `knownTypes`, typed payloads/decode, vectors and
wire docs in lockstep. Verify those tests before adding projections; do not duplicate or defer them.

**C3.2 — the migration** (additive: two `CREATE TABLE`s, three indexes):

```sql
-- projections.db 0008 — the celestial systems players are playing in.
--
-- catlog holds NO list of celestial bodies (PROJ-033), and this does not change
-- that. These two tables are a projection of what the mod REPORTED the game had
-- loaded — the same way `player_body` is a projection of where somebody went.
-- The difference, and the reason the "visited everywhere" badge became possible,
-- is that this one also records the bodies a player has NOT been to.
CREATE TABLE system (
  hash        TEXT PRIMARY KEY,
  -- The game's own id for the system: "Sol", "SolDense", "SolLite", "Test", or
  -- whatever a mod registered. NOT unique — two mods can both ship a system
  -- called Sol with different contents, which is exactly why `hash` is the key.
  system_id   TEXT NOT NULL,
  name        TEXT NOT NULL,          -- the display name, for humans
  -- The URL form: `name` through the stat-suffix alphabet, with a -2, -3 …
  -- suffix when two DISTINCT systems share a name. Assigned in ascending
  -- first_seq order, so it is stable and a rebuild reproduces it (§3.19).
  slug        TEXT NOT NULL,
  home_body   TEXT NOT NULL,
  body_count  INTEGER NOT NULL DEFAULT 0,   -- as the mod reported it
  -- 0 when the mod did not send the body list (disabled, over
  -- Wire.MaxSystemBodies, or invalid required numeric). The set is UNKNOWN,
  -- not empty; everywhere badges award nothing (Task F7 rule 1).
  reported_complete INTEGER NOT NULL DEFAULT 0,
  first_seq   INTEGER NOT NULL
);
CREATE UNIQUE INDEX system_slug ON system(slug);

CREATE TABLE system_body (
  hash     TEXT NOT NULL,
  body     TEXT NOT NULL,
  name     TEXT NOT NULL,
  class    TEXT NOT NULL,          -- the game's own word; opaque, no allow-list
  kind     TEXT NOT NULL,          -- §3.7's normalised semantic kind
  rank     INTEGER NOT NULL,
  parent   TEXT,                   -- NULL for the root
  radius_m REAL NOT NULL DEFAULT 0,   -- mean radius, metres from centre
  mass_kg  REAL NOT NULL DEFAULT 0,   -- mu = mass_kg * 6.6743e-11; not stored twice
  soi_m    REAL NOT NULL DEFAULT 0,   -- 0 on the star, whose SOI is +Inf in the game
  atmo_m   REAL NOT NULL DEFAULT 0,
  ocean_m  REAL NOT NULL DEFAULT 0,
  angvel   REAL NOT NULL DEFAULT 0,   -- rad/s, signed; negative is retrograde
  axis_x   REAL NOT NULL DEFAULT 0,   -- rotation axis, body-centred ecliptic frame
  axis_y   REAL NOT NULL DEFAULT 0,
  axis_z   REAL NOT NULL DEFAULT 0,
  -- The Keplerian set, NULL as a group on the root body. Stored exactly as the
  -- mod sent them and NEVER derived from each other: catlog computes nothing
  -- orbital, on either side (§3.20 rule 1).
  sma_m    REAL, ecc REAL, inc_deg REAL, lan_deg REAL, argp_deg REAL,
  t_pe     REAL, period_s REAL,
  ccf_to_cce_t0_x REAL NOT NULL,
  ccf_to_cce_t0_y REAL NOT NULL,
  ccf_to_cce_t0_z REAL NOT NULL,
  ccf_to_cce_t0_w REAL NOT NULL,
  first_seq   INTEGER NOT NULL,
  PRIMARY KEY (hash, body)
);
CREATE INDEX system_body_kind ON system_body(hash, kind, body);
CREATE INDEX career_system ON career(system, player_id);
```

**C3.3 — `systemFold`, and it is a STATE fold.** Add it to `StateFolds()` **first**, before
`flightFold` and `careerFold`:

```go
// StateFolds returns the folds that maintain the tables the boards read.
//
// systemFold is FIRST. A board fold reads the career's system through
// Batch.CareerSystem, and the discovery event arrives before session.started in
// the same batch — so the system has to be recorded before careerFold advances
// the clock on that very event. The final mod guarantees discovery first; this
// fold order makes the same guarantee true inside a buffered batch and keeps
// incremental folding equal to rebuild.
func StateFolds() []Fold { return []Fold{systemFold{}, flightFold{}, careerFold{}} }
```

`systemFold` handles both types and is **order-independent between them**, because `system.body`
carries its own hash and has no foreign key. A body row may be written before its header; do **not**
create a placeholder `system` row because `name`/unique `slug` are unknown and a placeholder slug
would need undocumented mutation/conflict rules. Create/slug the system only on
`system.discovered`; list/detail APIs hide orphan body rows. Write an across-batches body-before-
header test.

System **identity fields** and body rows are immutable first-write. On a repeated header, compare
`system_id`, `name`, `home_body` and `body_count`: if any differ, retain the first row and record/log
the conflict; if they match, the sole allowed update is
`reported_complete = reported_complete OR incoming_complete`. This monotone false→true promotion is
required when a player first disables `system.body` and later enables it. It never rewrites identity,
slug or `first_seq`, and a later false cannot erase a completed catalogue. `system.body` itself uses
`INSERT ... ON CONFLICT DO NOTHING`; a differing duplicate retains the original. Add identical
idempotence, differing-retains-first, false→true promotion and true→false non-regression tests across
batch boundaries. This is deterministic projection integrity, not a client plausibility/anti-cheat
check.

Header `reported_complete` means only “the mod intended to send all rows.” The effective catalogue
is complete iff it is true **and** `count(system_body for hash) == body_count`. Expose that effective
boolean in the API and require it in F7, so an interrupted 3,215-row append cannot award early. Test
the complete disabled→enabled path: first header false/no rows, later header true/all rows, effective
complete only after the last row.

It also sets the career's system:

```go
// On system.discovered, bind the career to the system — ONCE.
//
// A system cannot change within a career (§3.15). If a later discovery event for
// the same career reports a different hash — a player edited the XML and
// reloaded — the FIRST one stands and `system_changed` is set. The mark excludes
// nothing and scores nothing; it qualifies a per-system comparison exactly as
// `rewound` qualifies a career time (PROJ-023).
```

**C3.4 — the slug.** Do not reuse `statSuffix`: it validates protocol keys and rejects spaces and
parentheses, so stock display names such as “Solar System (Dense)” would all fall back to hashes.
Add a dedicated ASCII `systemSlug`: the mod already sanitises `name` to ASCII, so lowercase `A-Z`,
retain `a-z0-9`, map each run of every other byte to one hyphen, trim hyphens and cap the base length;
empty falls back to the first eight hash characters. No Unicode normalisation library or
version-dependent Unicode table participates. Then append `-2`, `-3` … for a distinct hash collision,
assigned in ascending `first_seq`. Do not weaken `statSuffix`. Pin actual stock names, punctuation,
empty/non-ASCII-defensive input, length cap and collision order at three batch sizes.

**Tests:** golden folds for both types; out-of-order bodies; a second discovery for the same career
with a different hash sets `system_changed` and does not move `career.system`; two distinct systems
with the same display name get the normalised base and `-2` **in first-seen order** at three batch sizes;
rebuild equality.

This migration task updates all three hand-maintained lists immediately: exact tables/indexes and
schema version, ordered snapshot dumps, and `Counts`. It also lands the state-projection docs,
player-facing system explanation and next-free decision entry; C6 is conformance/document audit,
not deferred primary documentation.

---

### Task C4 — the system read surface

**Files:** new `server/internal/readapi/systems.go`; `readapi.go`; `docs/ingest-api.md`.

```
GET /v1/systems
  -> {"systems": [{"hash": s, "system_id": s, "name": s, "slug": s, "home_body": s,
                   "bodies": n, "complete": b, "players": n, "careers": n}]}

GET /v1/systems/{slug}
  -> {"hash": s, "system_id": s, "name": s, "slug": s, "home_body": s, "roots": [s, …],
      "players": n, "careers": n,
      "complete": b,
      "bodies": [{"body": s, "name": s, "class": s, "kind": s, "rank": n, "parent"?: s,
                  "radius_m": f, "mass_kg": f, "soi_m": f, "atmo_m": f, "ocean_m": f,
                  "angvel": f, "axis": {"x": f, "y": f, "z": f},
                  "sma_m"?: f, "ecc"?: f, "inc_deg"?: f, "lan_deg"?: f,
                  "argp_deg"?: f, "t_pe"?: f, "period_s"?: f,
                  "ccf_to_cce_t0": {"x": f, "y": f, "z": f, "w": f}}]}
```

- Accepts a **slug or a hash** in the path segment, so an API consumer holding a hash needs no lookup.
- `players` / `careers` are counts over the `career` table, not `system_stat`, so a system somebody
  loaded and never scored in still lists.
- `roots` is every `system_body` with no parent, in canonical body order. Do not assume exactly one;
  each body's `rank` is depth from its own root.
- `complete` is the effective value `reported_complete && actual body rows == body_count`, never the
  header bit alone. All numeric response values are finite because C2/C3 enforce that invariant.
- **This is the endpoint a future 3D view reads.** Its required orientation-at-zero quaternion plus
  axis/rate supports exact body-fixed placement and ground tracks (§3.20). It is
  deliberately a **complete dump** rather than a paged one — a system is bounded, a renderer needs all
  of it, and paging a thing that is always fetched whole is machinery for nobody. Say so in
  `docs/ingest-api.md`.
- **It is the one response in catlog that can be large.** `SolDense` is 3,215 bodies, roughly a
  megabyte of JSON. That is acceptable because it is immutable, cacheable and requested by a renderer
  rather than a page — but it is worth stating, because every other catlog response is bounded by
  `limit ≤ 200`.
- Cache headers as everywhere else. A system is close to immutable, so this is the most cacheable
  response catlog serves — do not be tempted to give it a longer `s-maxage` than the §4.8 discipline;
  one number for every public response is worth more than one route's efficiency.

---

### Task C5 — the friendly name reaches every surface that already exists

**Files:** `server/internal/readapi/query.go`, `compare.go`, `events.go`, `stats.go`.

`SystemRef` (Task B1.3) is resolved once per page and attached to: board rows in the career and system
scopes, save rows, comparison rows, and the `GET /v1/players/{handle}` profile. `GET /v1/stats`'s
`collection` census gains `systems` and `system_bodies`.

**The raw hash is published and must NOT go through `Redact`.** Add a case to
`readapi/privacy_test.go`'s table asserting that a system hash **survives** redaction unchanged, so a
future well-meaning change that adds it to the relabel list fails a test that explains why (§3.19).

---

### Task C6 — vectors, docs, and the fixtures

**Audit only:** C2 owns the event contracts/vectors/docs and C3 owns projection docs. Re-run the
checks below and repair omissions in the owning commit before merge; C6 must not be the first place
the contract becomes coherent.

1. **`server/internal/testvectors/testvectors.go` + `make testvectors`.** The batch gains a
   `system.discovered` line and **at least two** `system.body` lines: one **root** body with the
   six shape keys **absent**, and one orbiting body with them **present** plus finite period. Every
   body line carries a finite normalised `ccf_to_cce_t0`. Add an unbound body with period absent and
   a second `system.discovered complete:false` with no body rows to pin the disabled/declined shape.
   That present/
   absent pair is what makes `Batch001_PayloadsRoundTripThroughTheirRecords` prove anything about the
   optional group.
2. `docs/events.md` — two new rows in the taxonomy, the `class` vocabulary marked **open set**, and
   the type count.
3. `docs/event-details.md` — two new `##` sections with all eight blocks, two registry rows, and new
   **State projections** entries for `system` and `system_body`.
4. `docs/ksa-integration.md` — a section for the system survey with every `file:line`.
5. `docs-site/src/data/events.ts` + a new `events/systems.mdx` family page in the sidebar. In player
   terms: *"catlog looks at the solar system your save is in once, when you load it, and writes down
   what is in it — the worlds, how big they are and where they orbit. That is how a leaderboard for
   'Luna' knows which Luna you mean, and how catlog can tell whether you have been everywhere."*
6. `docs/mod.md` — the survey's cost measurement (Task C2.4).

---
### Task C7 — the systems pages in the app and player docs

**Files:** `templates/systems.gohtml`, `templates/system.gohtml`, `templates.go`, `pages.go`,
`web.go`, `layout.gohtml`, `web_test.go`, `docs/ui-design.md`;
`docs-site/src/content/docs/leaderboards/systems.mdx`, `docs-site/astro.config.mjs`.

```go
	mux.HandleFunc("GET /systems", s.handleSystems)
	mux.HandleFunc("GET /systems/{slug}", s.handleSystem)
```

**No new top-level nav entry.** The header budget is spent (Task I2 takes it to seven with Badges and
Challenges). Systems are reached from the board scope chips, from a save's System column, and from a
link in the `/boards` index header — *"catlog is tracking N celestial systems"* — which is where a
reader is when the question occurs to them.

`/systems` — one `.panel`, a table of Name · Bodies · Players · Saves, ordered by player count
descending. Empty state: *"No systems recorded yet."*

`/systems/{slug}` — the header line (name, home body, N bodies, N players) and **one table of bodies**:
Name · Class · Parent · Radius · Sphere of influence · Semi-major axis · Period. All through
`units.Format`, all `tabular-nums`, all with `data-value` carrying the exact float. Sort by `rank`
then `sma_m` so the tree reads outward from the star. The orbital angles are **not** shown — they are
in the API for a renderer, and a table of six angles is a table nobody reads.

**The one design decision on this page:** it is a *reference* page, not a leaderboard. It gets no
ranks, no bars and no accent fills. It looks like `/docs/api` looks, and it exists so a player can
answer "which Luna is that" and "what is the game actually simulating".

**docs-site — `leaderboards/systems.mdx`**, in the `Leaderboards` sidebar group. In player language,
covering:

- **What a system is to catlog**: the set of worlds your game loaded. It comes from files that ship
  with Kitten Space Agency, and a mod — or you, with a text editor — can change it.
- **Why the boards care**: *"If you and I are both playing the game as it ships, we are in the same
  system and our Luna records are the same race. If you have installed a system mod, your Luna may be
  a completely different world. catlog can tell, so it does not pretend otherwise."*
- **How catlog tells**: once, when you load a save, it writes down the worlds and where they orbit and
  makes a fingerprint of that. **The fingerprint is not about you** — everyone playing the stock game
  produces the same one, which is exactly what lets it group you together.
- **A save never changes system.** If you edit the files and reload, catlog marks the save and keeps
  ranking it; it does not delete anything and does not accuse you of anything.
- **What this means for badges**: "Every World" is per system, and the page shows which.
- The honest note that a **custom** system is unusual enough to be recognisable, which is the same
  trade a memorable handle makes — link the identity page.

---

## Phase D — final v1 wire shape: community fields, orbital elements, and `flight_state`

**Goal:** the three readings the community ideas need that cannot be derived, the element fields
§3.20's data contract requires, and the `flight_state` columns that stop us needing any more.

| Task | Type | Change |
|---|---|---|
| D1 | `vehicle.rud` | final `ver 1`: `+part_count` |
| D2 | `flight.started` | final `ver 1`: `+engine_count` |
| D3 | `kitten.tumble` | final `ver 1`: `+from` |
| **D3b** | `vehicle.orbit` | final `ver 1`: `+sma_m`, `+lan_deg`, `+argp_deg`, `+t_pe`, `+period_s` |
| **D3c** | `telemetry.window` | final `ver 1`: `+state` — position **and** velocity, optional |
| D4 | server | five final-v1 payload structs/decode paths; no upcasters |
| D5 | `flight_state` | the milestone bitfield and the four fact columns |
| D6 | vectors + docs | |

**Delegation/commit boundary:** D1, D2, D3, D3b and D3c are each an indivisible vertical contract
slice. Each task includes its mod field/read, matching Go payload/decode, vector present/absent
cases, mod/server tests, `docs/events.md`, `docs/event-details.md`, docs-site event data/family prose
and next-free decision entry. D4 and D6 are audits over those five completed slices, not later
commits that make an earlier broken contract whole. Every slice leaves `make test` green.

**Payload means "what the vehicle weighed", loosely.** The owner has settled that: KSA has no payload
concept and cannot be given one (below), so wherever this plan says "payload" it means the vehicle's
current total mass, and the site says that in one sentence rather than avoiding the word.

**This phase was scoped against the game.** Every claim below was verified against the decompiled
KSA build `2026.8.5.5168` before this plan was written. The findings that shaped it:

| Question | Answer |
|---|---|
| Is per-part destruction observable? | **No.** An exhaustive identifier sweep of the decomp finds no `DestroyPart`, no `OnPartDestroyed`, no `RemovePart`, no `Explo*`/`Sever*`/`Shatter*`/`Fracture*`. Destruction is one whole-vehicle test in `VehicleUpdateTask.DetectStructuralFailure`. |
| Then how do we count parts lost? | `vehicle.Parts.Count`, read in a **prefix** on `Universe.DestroyVehicleFromEvent` — **which catlog already patches, as a prefix, and already reads crew from**. The vehicle is fully intact there. |
| Do physics RUDs kill crew? | **No — re-confirmed at source in 5168.** `Universe.DestroyVehicle` calls `EndAllCrewMissions()`, which calls `EndMission()`, never `Kill()`. `Kia = true` is written in exactly one place, reachable only from the player-initiated destroy path. D11 stands. |
| Is engine count readable? | **Yes.** `vehicle.Parts.Modules.Get<EngineController>()`, whose `.Length` is the installed engine count. catlog already calls `Modules.Get<EngineController>()` in `VehicleTelemetry.ActiveEngines`. |
| Can we tell payload mass from booster mass? | **No.** KSA has no payload concept — no `IsPayload`, no `PayloadMass`. `SequencePerformance` has exactly the per-stage split that would be needed and is **refreshed in flight only while the player has one of two specific windows open**, so it holds stale editor data the rest of the time. Wet/dry **is** available (`TotalMass` / `InertMass` / `PropellantMass`). |
| Is there a "landed on their feet" success state? | **Yes.** `LocomotionMode` is `{Mmu, Grounded, Airborne, Tumbling, Rightening, Ladder}`. A touchdown below the tumble gate goes `Airborne → Grounded`; the same touchdown above it goes to `Tumbling`. **The previous mode is the whole answer, and catlog's poller already holds it.** |
| Can a vessel's position be recorded? | **Yes** — `Orbit.StateVectors` exposes a body-centred inertial position and velocity, relative to `Orbit.Parent`. ★ Confirm the exact frame, units and numeric type against the survey in Task C1 before writing D3c. |

---

### Task D1 — final-v1 `vehicle.rud` gains `part_count`

**Community idea #4.** The only honest reading of "most parts destroyed" that KSA supports.

**Mod files, in order** (the full end-to-end checklist for a payload field is Appendix E):

1. `mod/catlog.lib/Events/EventTypes.cs` — keep `[VehicleRud] = 1`; update the v1 payload shape only.
2. `mod/catlog.lib/Events/Payloads.cs` — add to `VehicleRudPayload`, **after `CrewCount` and before
   the optional `Lat`/`Lon`** so the optionals stay last:
   `[property: JsonPropertyName("part_count")] int PartCount,`
   Not optional: 0 is a real reading only if the vehicle had no parts, which cannot happen, and the
   existing `> 0` gate on the board covers a failed read. (Contrast `lat`/`lon`, where 0 is the
   equator.)
3. `mod/catlog.lib/Events/GameSignal.cs` — `RudSignal` gains `int PartCount`.
4. `mod/catlog.lib/Detect/EventPipeline.cs` — the `case RudSignal rud:` arm passes it through.
5. `mod/catlog/VehicleTelemetry.cs` — `PartCount(Vehicle)` **already exists** (`Vehicle.Parts.Count`,
   `[KsaAnchor]` risk Low). Reuse it; no new anchor.
6. `mod/catlog/Patcher.cs` — `DestroyVehicleFromEventPrefix` fills `PartCount:
   VehicleTelemetry.PartCount(vehicle)`. **It is already a prefix and already reads
   `CrewCount(vehicle)` there**, so this is one argument on an existing call. Add to that patch's
   comment: *"read in the prefix because `Universe.DestroyVehicle` → `EndAllCrewMissions` zeroes the
   seats and `Dispose` follows in the same frame; the vehicle is only intact here."*

**Server/vector/docs:** complete this field's D4/D6 slice in this same task and commit.

---

### Task D2 — final-v1 `flight.started` gains `engine_count`

**Community idea #5** ("get to jupiter with no engines").

1. `EventTypes.cs` — keep `[FlightStarted] = 1`.
2. `Payloads.cs` —
   `[property: JsonPropertyName("engine_count"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] int? EngineCount,`
   placed after `StageCount` and before `Lat`/`Lon`. The Go payload uses `*int`; absent is unknown,
   while present `0` is the meaningful “no engines” fact.
3. `GameSignal.cs` — `VehicleCreatedSignal` gains `int? EngineCount`.
4. `EventPipeline.cs` — the `VehicleCreatedSignal` arm passes it through.
5. `mod/catlog/VehicleTelemetry.cs` — **new** read, with a `[KsaAnchor]`:

```csharp
    /// <summary>
    /// How many rocket engines are installed on the vehicle, active or not.
    /// </summary>
    /// <remarks>
    /// Counts <c>EngineController</c> modules, not <c>RocketCores</c> or
    /// <c>RocketNozzles</c>: a <c>RocketCore</c>'s controller may be a
    /// <c>ThrusterController</c> instead, so those two lists include RCS
    /// thrusters and would report a probe with attitude control as having
    /// engines.
    /// </remarks>
    [KsaAnchor(
        "Vehicle.Parts.Modules.Get<EngineController>()",
        SourceFile = "KSA/ModuleList.cs:164",
        Verified = "2026-08-09", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Medium,
        Notes = "Modules.HasAny<EngineController>() is the cheaper predicate; we want the count.")]
    public static int? EngineCount(Vehicle vehicle)
    {
        try { return vehicle.Parts.Modules.Get<EngineController>().Length; }
        catch (Exception ex) { Faults.Note(ex); return null; }
    }
```

6. `mod/catlog/PolledSignals.cs` — `Track` fills `EngineCount: VehicleTelemetry.EngineCount(vehicle)`
   when it builds `VehicleCreatedSignal`.

**Never turn a failed read into `0`.** Both `coaster` and `coasting_class` require
`engine_count != nil && *engine_count == 0`; unknown earns nothing. This task's vectors carry one
explicit zero and one absent key, and its tests cover a real zero, a positive count and an injected
read failure/absent JSON value through C#, Go decode and `flight_state`.

**The honest limitation, which the site must state (Task G3/H3):** *"no engines" means no engine was
installed when the flight began.* RCS thrusters, decoupler springs and docking-port pushoff all
impart velocity and are not engines; and a vehicle can shed its engines in transit, at which point
the piece that continues is a **new flight** with its own count. Both are stated, neither is
engineered around — inferring "did they really coast" from data shape is the thing Constitution §8
forbids.

---

### Task D3 — final-v1 `kitten.tumble` gains `from`

**Community idea #2**, and this is the field that makes it the stat that was actually asked for.

The request was *"a stat of most amount of times a kitten did in fact NOT land on their feet (while
doing EVA on a planet)"*. Today `kitten_tumbles` counts **every** entry into the tumbling state,
including a kitten who trips while running along flat ground — which is a different and less
interesting thing. The game distinguishes them and catlog is already holding the answer:

- `LocomotionMode` is `{Mmu, Grounded, Airborne, Tumbling, Rightening, Ladder}`.
- A touchdown **below** the tumble speed gate goes `Airborne → Grounded` — landed on their feet.
- The same touchdown **above** it goes `Airborne → Tumbling` — **did not land on their feet.**
- Tripping while running is `Grounded → Tumbling`.
- Recovery is `Tumbling → Rightening → Grounded`, which is why catlog counts transitions *into*
  `Tumbling` and never out of it.

`mod/catlog/PolledSignals.cs` already keeps the previous mode in `VehicleState.Locomotion` and
already compares it (`now.Locomotion == Tumbling && state.Locomotion != Tumbling`). **The previous
value is in hand and is being thrown away.**

1. `EventTypes.cs` — keep `[KittenTumble] = 1`.
2. `Payloads.cs` — `[property: JsonPropertyName("from")] string From,` on `KittenTumblePayload`.
3. `GameSignal.cs` — `TumbleSignal` gains `string From`.
4. `PolledSignals.cs` — pass `state.Locomotion`, lowercased, through a **total** mapper; an
   unreadable or previously-unseen mode becomes `"unknown"`, never a guess. Mirror `SituationName`'s exhaustive
   switch in `VehicleTelemetry.cs:1258-1269`.
5. `EventPipeline.cs` — the `TumbleSignal` arm passes it through.

**`from` is an open set, opaque to the server**, exactly like `situation` and `body`: KSA declares
`Ladder` with no producer today and a future build may add a mode. Do not write an allow-list. The
board in Task E1 tests for the one value it cares about and treats everything else as "not a botched
landing".

**One caveat to carry into `docs/event-details.md`:** a violently tumbling kitten that leaves the
ground for longer than `TumbleAirborneExitTime` (0.5 s stock) goes `Tumbling → Airborne` and
re-enters `Tumbling` on the next bounce — so one cartwheel can produce several tumbles, some of them
`from: "airborne"`. That is the game's own state machine and catlog reports it rather than smoothing
it.

---

### Task D3b — final-v1 `vehicle.orbit` gains the rest of the Keplerian set

**§3.20's data contract.** `vehicle.orbit` already carries apoapsis, periapsis, eccentricity and
inclination — enough to *describe* an orbit, not enough to *draw* one. Five more fields close it;
record `sma_m` rather than asking a consumer to derive it:

```csharp
    [property: JsonPropertyName("sma_m")]    double SmaM,
    [property: JsonPropertyName("lan_deg")]  double LanDeg,    // longitude of the ascending node
    [property: JsonPropertyName("argp_deg")] double ArgpDeg,   // argument of periapsis
    [property: JsonPropertyName("t_pe")]     double TPe,       // time at periapsis, game seconds
    [property: JsonPropertyName("period_s")] double PeriodS,   // 0 on an unbound trajectory
```

Placed after `inc_deg` and before `mass_kg`. **Not optional**, matching the existing four: `ap_m` and
`pe_m` are already emitted as `0` when the read fails and the four shape boards gate on `> 0`
(PROJ-094). Keep that convention — introducing an optional here would make one payload follow two
rules.

Degrees, converted in the mod like `inc_deg` already is. `period_s` is `0` for a hyperbolic or
parabolic trajectory, which `OrbitClass` already distinguishes; a consumer reads `0` as "not a closed
orbit" and the existing `IsBoundOrbit` logic is where that is decided.

**Exact implementation route:** add init-only `SmaM`, `LanDeg`, `ArgpDeg`, `TPe` and `PeriodS`
properties to `TelemetrySnapshot`; fill them in `VehicleTelemetry.Sample` from
`orbit.SemiMajorAxis`, `LongitudeOfAscendingNode`, `ArgumentOfPeriapsis`,
`TimeAtPeriapsis.Seconds()` and `Period`, with radians→degrees and the finite/unbound policy above;
then pass them through `EventDetector` into `VehicleOrbitPayload`. Update `TestData`, `SimVehicle`,
detector tests and vectors. A payload-only edit with no Snapshot→Sample→Detector path is incomplete.

**No board reads these.** They are recorded, not scored — say so in `docs/event-details.md`'s entry
and in `boards.ts`'s prose, so nobody looks for the leaderboard.

---

### Task D3c — final-v1 `telemetry.window` gains `state`

**The largest single wire cost in this plan, and the one place §3.20 rule 2 has to be read before
writing code.**

```csharp
public sealed record Vec3(
    [property: JsonPropertyName("x")] double X,
    [property: JsonPropertyName("y")] double Y,
    [property: JsonPropertyName("z")] double Z);

// A body-centred inertial state vector at the moment the window closed.
public sealed record StateVec(
    [property: JsonPropertyName("pos")] Vec3 Pos,     // metres, relative to `body`
    [property: JsonPropertyName("vel")] Vec3 Vel);    // metres per second
```

added to `TelemetryWindowPayload` as an **optional**, with the full omit-don't-zero treatment —
`StateVec?` **and** `[property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]` — like
`radar_alt_m`'s aggregate:

```csharp
    [property: JsonPropertyName("state")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    StateVec? State,
```

**Why position *and* velocity, when position alone is smaller.** A position sequence can only be
interpolated; a state vector can be **propagated**. At 30 sim-seconds under warp the samples are far
apart in orbit, and a spline through them is visibly wrong near periapsis. Six numbers make every
window independently sufficient — which is also what makes a dropped window harmless rather than a
hole in the path.

**Why it is on this type.** `telemetry.window` is the **only `KindPassive`** type and therefore the
only thing `OutboxDb.Prune` may drop when a player's spool fills. Visualisation data is exactly what
*should* be shed first under pressure. On any other type it would be undroppable. That is the
decisive argument and it belongs in the `DECISIONS.md` entry.

**Implementation:** `WindowAccumulator.VehicleWindow` keeps the **last** sample's state, exactly as
it already keeps `_massKgLast` and `_body`, and `Close()` emits it. It is not an aggregate — a mean
position is meaningless. The value is read into `TelemetrySnapshot` as a new **init-only** nullable
property, filled in `VehicleTelemetry.Sample`, absent when the read fails.

**The accessor/frame is resolved:**

```csharp
ref readonly StateVectors sv = ref vehicle.Orbit.StateVectors;
double3 pos = sv.PositionCci;
double3 vel = sv.VelocityCci;
```

`Orbit.GetStateVectorsAt` transforms orbital vectors through `Orb2ParentCci`; these are metres and
metres/second in the parent-body-centred inertial frame, relative to `vehicle.Orbit.Parent`. Record
those exact symbols and source lines in `[KsaAnchor]`/`docs/ksa-integration.md`. Populate a nullable
whole `StateVec` only when all six components are finite and the snapshot `Body` names that same
parent; otherwise omit `state`. Never zero one failed component or emit an origin vector. Pin the
TelemetrySnapshot → VehicleTelemetry.Sample → WindowAccumulator(last sample) → payload path with
present/absent and body-change tests.

**Size, measured not guessed.** Before merging, generate a realistic batch with `catlog.loadgen` and
compare the compressed body size against the same batch without `state`. Record the number in
`docs/mod.md`. If the growth is worse than about a third, come back and reconsider — a documented
measurement is what makes that a decision rather than a regret.

---

### Task D4 — audit the server halves of the five final-v1 payloads

**Files:** read/repair `server/internal/stats/payload.go` and payload/decode tests from D1–D3c.

Add the fields to `VehicleRUD`, `FlightStarted`, `KittenTumble`, `VehicleOrbit` and
`TelemetryWindow` with exact `json:` tags and decode cases. `state` is a `*StateVec` so absence stays
distinct from a legitimate origin vector. Add finite/presence tests for all five payloads.

**Do not edit `projector/upcast.go`, `currentVer`, `Upcasters` or event versions.** There is no old
v1 production shape to accept. `EventTypesTests.RegistryHasExactlyTheLaunchSet` continues asserting
every type is version 1; regenerated final-shape vectors are the cross-language contract.

---

### Task D5 — `flight_state` learns what the vehicle was and what it achieved

**Files:** a new projections migration (`0009_flight_facts.sql` — **re-check the number**),
`server/internal/stats/flight.go`, `server/internal/stats/batch.go`, exact-DDL/schema fixture,
projector snapshot, `docs/server.md`, both event/projection doc halves and next-free decision entry.

This is §3.11: the join key that keeps the wire small. The migration spells every final column.

```sql
-- projections.db 0009 — what a flight was, and what it got done.
--
-- flight_state is already written by flightFold for EVERY flight-bearing event
-- and is already read by four boards whose own payloads carry no body
-- (`flightBody`). These columns extend the same idea: a badge or a board that
-- needs "what kind of vehicle was this" or "did this flight ever reach orbit"
-- joins the flight instead of asking the mod to repeat itself on every event.
--
-- That is Constitution §3 doing real work. `vehicle.soi` does NOT gain
-- `engine_count` and `vehicle.orbit` does NOT gain `kids`, because both are
-- answerable from here.
ALTER TABLE flight_state ADD COLUMN milestones     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE flight_state ADD COLUMN engine_count   INTEGER;   -- NULL: flight.started not seen/unreadable
ALTER TABLE flight_state ADD COLUMN part_count     INTEGER;
ALTER TABLE flight_state ADD COLUMN launch_mass_kg REAL;
ALTER TABLE flight_state ADD COLUMN career         TEXT NOT NULL DEFAULT '';
```

`career` on `flight_state` is what lets a badge earned on a flight be attributed to a save when the
awarding event is `flight.ended` — it is written by `flightFold` from the first event that carries
one, and never overwritten.

**Milestone bits** in `stats/flight.go`, beside the existing flag bits:

```go
// Milestone bits on flight_state.milestones. These record what a flight
// ACHIEVED, and are deliberately a separate column from `flags`, which records
// what disqualifies it. Nothing here ever excludes anything.
const (
	MilestoneOrbit    = 1 << 0 // vehicle.orbit phase=achieved
	MilestoneSpace    = 1 << 1 // vehicle.atmosphere dir=exited
	MilestoneOtherSOI = 1 << 2 // vehicle.soi into a body other than the launch body
	MilestoneLanded   = 1 << 3 // vehicle.landed, survived
	MilestoneDocked   = 1 << 4 // vehicle.docked
)
```

`flightFold` sets them; nothing clears them. Set-only bits are replay-stable by construction.

This is not only a migration edit. Update every hand-maintained flight-cache seam in one task:
`FlightState`; the in-memory `flightEntry`; its `SELECT`/scan order; `EnsureFlight`/`StartFlight`;
and the flush `INSERT`, placeholder count and conflict update. `flight.started` supplies
`engine_count`, `part_count` and `launch_mass_kg`; the first flight-bearing event supplies `career`,
which is never overwritten by empty. Each milestone uses the exact positive predicate shown above.
Badge folds use the existing final-state `scoreable` path, like boards. Do not add
`first_flagged_seq` or `scoreableAt`: §3.6 deliberately makes rebuild correction authoritative.

**Fact-order rule:** a composite predicate may use start facts only when the existing
`FlightState.StartedSeq` names an actual `flight.started` and `StartedSeq <= candidate.Seq` (the
required nullable fact is also valid). If `vehicle.orbit`/`vehicle.soi` arrived first, deliberately
decline that composite candidate; do not let rebuild's completed first pass award something the
incremental path could not know. This conservative case is an out-of-order/incomplete log, not the
normal final mod order. `MilestoneOtherSOI` likewise requires the launch body already known and
`to_body != launch_body`; never mark “other” while launch is unknown and never retro-award it later.

**Tests:** each bit's positive and negative predicate; flush/reload of every new column;
`TestFlightMilestonesAccumulate`, `TestFlightFactsSurviveAnOutOfOrderBatch` (a
`vehicle.orbit` folded before its `flight.started` — the batch may legitimately do that, and
`EnsureFlight` already creates the row for any flight-bearing event), three batch sizes and rebuild
equivalence. Add an out-of-order composite-badge test proving both paths decline the early candidate.
Update exact DDL/schema in this migration task; the snapshot already dumps the widened flight row
and must include every new column.

---

### Task D6 — audit conformance vectors and final-v1 documentation

Every D1–D3c slice already updated these. This task verifies the combined catalogue and repairs an
omission in the owning slice before merge.

**Files:** `server/internal/testvectors/testvectors.go`, `contracts/testdata/**`, `docs/events.md`,
`docs/event-details.md`, `docs-site/src/data/events.ts`, `docs-site/src/content/docs/events/*.mdx`.

1. **Regenerate:** `make testvectors` (`catlogctl testvectors generate contracts/testdata`). Before
   that, edit `testvectors.go` so the batch:
   - keeps **all five types at `ver: 1`**;
   - carries RUD `part_count`, `flight.started.engine_count` present as explicit `0` on one line and
     absent on another, all five orbit additions, and telemetry state present on one line and absent
     on another;
   - **carries `kitten.tumble` twice** — once `from: "airborne"` (a botched landing) and once
     `from: "grounded"` (a trip). The vector set's whole job is to pin payload *shapes*, and a
     value-discriminated board needs both sides pinned. This mirrors why `flight.started` and
     `telemetry.window` each already appear twice.
2. `docs/events.md` — document all five final-v1 payload shapes; the “every one at `ver: 1`” sentence
   remains true. Add the `from` vocabulary note beside `situation`, marked **open set**.
3. `docs/event-details.md` — update all five event sections (Wire, Payload, Detector, Game source,
   Classification), including exact KSA source/accessor and finite/absence rules.
4. `docs-site/src/data/events.ts` — keep `ver: 1` on all five entries and add every field in player
   language. For `from`: *"What the kitten was doing immediately before: `airborne` means they were
   in the air, so this was a landing that did not go well."*
5. Update the kittens, vehicle and telemetry/orbit player pages in the same task. No payload change
   is complete with only the kitten prose updated.

**Mod tests that will fail until updated** (they are supposed to):
- `EventTypesTests.RegistryHasExactlyTheLaunchSet` stays an all-v1 assertion and therefore catches
  any accidental bump.
- `ContractVectorTests.Batch001_StampsTheRegistrysCurrentVersion` and
  `Batch001_PayloadsRoundTripThroughTheirRecords`.
- `TestData.Snapshot` / the signal factories gain defaulted parameters.

---

## Phase E — the boards the community asked for

Every board here is `putRecord` / `putBest` / `addCount` / set-backed. The first three helpers fan
out to all three scopes; set-backed boards must compute player, career and system values explicitly.
None needs a registry entry beyond `fixedBoards` and `BoardFolds()`.

The full catalog with units, directions and exclusions is **Appendix A**; the tasks below are the
implementation notes that are not obvious from it.

---

### Task E1 — the tumble split: `botched_landings` and the `tumbles_on_<body>` family

**Files:** `server/internal/stats/boards.go`, `fold.go`.

1. **`botched_landings`** (count, `landings`… no — unit `tumbles`): `kitten.tumble` where
   `from == "airborne"`. Title: **"Did Not Land On Their Feet"**. `kitten_tumbles` keeps counting
   everything and is unchanged, so no existing number moves.

```go
// botchedLandingFold counts the tumbles that were landings.
//
// `from` is the kitten's locomotion mode immediately before the tumble, and the
// game's own state machine is what makes this a real distinction rather than a
// heuristic: a touchdown below the tumble speed gate goes airborne -> grounded
// (landed on their feet), and the same touchdown above it goes airborne ->
// tumbling. A trip while running is grounded -> tumbling and is not this board.
//
// `from` is an OPEN SET, opaque to the server. This tests for the one value it
// cares about; everything else, including a mode a future game build introduces
// and the literal "unknown", is simply not a botched landing.
```

2. **`tumbles_on_<body>` dynamic family.** `kitten.tumble` already carries `body`. Register a third
   entry in `families` (`boards.go:193-238`), listed under `kitten_tumbles`:

```go
	{
		prefix: "tumbles_on_", after: StatKittenTumbles,
		board: func(stat, body string) Board {
			return Board{Stat: stat, Title: "Tumbles on " + titleize(body), Unit: "tumbles"}
		},
	},
```

Metadata alone does not write a stat. Add `TumblesOnStat(body)` and replace the generic
`countFold{StatKittenTumbles}` with a tumble fold that writes the unchanged total plus the valid
family key through `addCount`, giving all three scopes. Mirror `rudFold`'s total+family shape.

   `familyStat` already refuses a suffix that collides with a fixed key and already validates the
   alphabet. **A body whose name cannot form a key still counts towards `kitten_tumbles`** — that
   invariant is `familyStat`'s, not yours, and it must not be re-implemented.

**Tests:** valid body writes total and family in all scopes; an **unkeyable** body still writes the
total and no family. Do not use body `landings` as the negative case:
`tumbles_on_landings` is a valid non-colliding full key.

---

### Task E2 — `parts_lost` and `biggest_parts_lost`

**Files:** `boards.go`, `fold.go`.

From `vehicle.rud.part_count` (Task D1). Two boards from one reading, the `rud_total`/`rud_<cause>`
shape:

- **`parts_lost`** — title **"Parts In Lost Vehicles"**, count, unit `parts`, adds `part_count` per
  RUD. A RUD is not necessarily an explosion, so neither title nor copy claims it is.
- **`biggest_parts_lost`** — record (max). "The largest vehicle you have lost in one go."

Both gate `part_count > 0` (PROJ-088: a zero is an unread value and on a counter it is a silent
no-op, on a record it is meaningless) and both go through `scoreable`.

**The title must not overclaim.** KSA has no per-part destruction; this is *the size of the vehicle
that was lost*, not a count of parts that individually exploded. `docs/event-details.md` says
the technical version, `docs-site` says the player version: *"KSA does not blow parts off one at a
time — a vehicle either survives or it does not. This is how big the thing was when it stopped
existing."*

---

### Task E3 — `kittens_to_orbit_and_back`

**Files:** `boards.go`, `batch.go` (a new `player_set` kind), `fold.go`.

**Community idea #3.** Set-backed, per kitten, using the machinery `soi_bodies` already uses.

**The rule, stated once:** a kitten counts when she is aboard a flight that **reached orbit** and
that flight later **ended recovered** with her still aboard.

```go
// kittensToOrbitFold — sketch.
//
// Reads flight_state.milestones (Task D5) rather than correlating a
// vehicle.orbit event with a flight.ended one, because the two can be arbitrarily
// far apart in the log and a projection may not hold state across events that a
// rebuild would not reproduce in the same order.
func (kittensToOrbitFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	p, ok := payloadOf[FlightEnded](ev)
	if !ok || p.Reason != "recovered" || len(p.Kids) == 0 {
		return nil
	}
	ok, err := scoreable(ctx, ev, b)
	if err != nil || !ok {
		return err
	}
	st, found, err := b.Flight(ctx, ev.FlightID)
	if err != nil || !found || st.Milestones&MilestoneOrbit == 0 {
		return err
	}
	// One row per kitten in player/career sets; derive all three board scopes.
	// Iterate p.Kids IN ORDER, never a map, so a rebuild reproduces the context
	// byte for byte.
	...
}
```

**Storage.** Reuse `career_body`'s set shape: add `kind = 'orbit_kid'`, with the `kid` in the `body`
column. Do **not** write this kind to `player_body`. `kid` omits career (§3.13), so the same roster
name in two saves denotes two different cats but would collapse in the lifetime table. Count
`career_body` rows by `(career, kid)`: all careers for the lifetime board, one career for save scope,
and all careers with the resolved system for system scope. **No migration is needed** because the
table's shape does not change.

On each new member, compute three independent totals from `career_body`: all `(career,kid)` rows for
the player; this save's rows; and all rows across this player's saves in the resolved system.
Write them with `setValue`, `setCareerValue` and `setSystemValue`. Never mirror one count with
`putRecord`. Test repeated kitten ids across two saves and two systems and assert the lifetime count
is two, not one.

**Rename nothing.** `body` holding a `kid` reads oddly, and renaming the column would touch four
documents and buy nothing — the column is "the set member" and always was. Say so in a comment and in
`docs/event-details.md`'s `career_body` section, where `kind` now has three values; `player_body`
remains unchanged.

**Two caveats, both stated and neither engineered around:**
- **"Back" means recovered.** KSA only offers recovery on the system's home body, at rest, in
  contact (`Vehicle.CanRecover`). A crew that lands on the Moon and stays has not come back.
- **A kitten who boarded *after* orbit** and rode home is counted, and a kitten who was aboard at
  orbit but transferred away before recovery is not. Crew move between vehicles by EVA, boarding and
  docking, and catlog resolves the question at the two ends rather than tracking seats continuously.
  This is the cheap answer and it is the honest one; say so in `docs/event-details.md`.

---

### Task E4 — `biggest_crew_wreck` and `kittens_wrecked`

**Files:** `boards.go`, `fold.go`. **No wire change** — `vehicle.rud.crew_count` already ships.

**Community idea #7**, delivered under a title that is true.

D11 is re-confirmed at source in build 5168: a **physics** RUD ends crew missions without killing
them, while the player-initiated destroy path can mark crew KIA. `vehicle.rud` covers lost vehicles,
not only physics crashes, so these boards state only the fact the payload actually proves: how many
kittens were aboard.

- **`biggest_crew_wreck`** — record (max) on `crew_count`, gated `>= 1` and `scoreable`.
  Title: **"Most Kittens Aboard A Lost Vehicle"**.
- **`kittens_wrecked`** — count, adds `crew_count` per RUD. Title: **"Kittens Aboard Lost
  Vehicles"**. It makes no unsupported claim about their fate.

The site copy states: *"This is not a body count. It is how many kittens were aboard when a vehicle
was lost. Physics accidents normally send them home; a player-initiated scuttle is a different game
path, so catlog does not claim what happened to them here."*

A separate `kittens_scuttled` counter from `kitten.kia` is **not** built. `kitten.kia` signals a
deliberate scuttle with crew aboard, and a public board ranking that is a durable public consequence
attached to a person for a choice the game offers — Constitution §8's consequence test. Record the
refusal in `docs/ROADMAP.md` (Task J3).

---

### Task E5 — the sim-time sprint boards: `bodies_by_1y`, `bodies_by_10y`

**Files:** `boards.go`, `fold.go`. **No wire change, no new game read.**

**Community idea #6** ("most SOIs within 10 years"). Career-native by nature and free from Phase A.

- Value: the number of **distinct** bodies whose SOI was first entered within the threshold in one
  save. Add batch-aware `BodyCountBefore`, `CareerBodyCountBefore` and `SystemBodyCountBefore`
  accessors over `first_sim_t`. Career scope is the current save's count; player scope is the
  **maximum complete save result**, not a union of early arrivals across different saves; system
  scope is the maximum save result among that player's saves in the system. `player_body` cannot
  answer this: it collapses saves and retains only a lifetime first encounter.
- Thresholds: `SprintYearSeconds = 365 * 24 * 3600` — **a flat 365-day year, matching
  `server/internal/units`' duration ladder**, which is already what a catlog `ms` value renders as.
  `bodies_by_1y` = 31 536 000 s, `bodies_by_10y` = 315 360 000 s.

**This is the second most likely place to get §3.7 wrong.** A "year" here is a unit of catlog's own
number formatting, **not** a body's orbital period. Reading Earth's period out of the game to define
a year would put a celestial-body fact into the server, which is precisely what PROJ-033 removed.
Put that sentence in the fold's doc comment.

These are scope-specific derived totals, not generic `kindRecord` fan-out. After `toBodyFold` lowers
the current save's `career_body.first_sim_t`, recompute the affected career result and the player/
system maxima and write through `setCareerValue`, `setValue` and `setSystemValue`. Run on every
qualifying SOI, not only a new body: a rewind can lower an existing row across the threshold. Tests
cover that rewind, multiple saves/systems, repeated body names and batch sizes 1/large.

---

### Task E6 — the documentation for Phase E

**In the same commits as the code above, not after.**

- `docs/event-details.md` — a **Boards** table row per new board (the count moves again; state the
  final total: **50 fixed boards**), a **Fold detail** entry each, rows in the **Suppression and eligibility matrix** for
  every new gate, and `career_body`'s third `kind` (`player_body` is unchanged).
- `docs-site/src/data/boards.ts` — one entry per board with `what` / `how` / `excluded` written for a
  player, plus the new `tumbles_on_<body>` family in `BOARD_FAMILIES`.
- `docs-site/src/content/docs/leaderboards/catalog.mdx` — `<BoardDetail>` blocks in the right
  sections, with the three honesty asides above (`parts_lost` is not per-part; `biggest_crew_wreck`
  is not a body count; "no engines" does not mean "no propulsion").
- `docs-site/src/content/docs/events/kittens.mdx` and `vehicle.mdx` — the new fields.

---

## Phase F — merit badges, server side

**Goal:** a timestamped, once-only award in the **current projection**, independently at **player**
scope and **per-save** scope, from the events already in the log. Rebuild correction is
authoritative (§3.6).

**The model, in one paragraph.** A badge is a named predicate over the event stream. When an event
first satisfies it in one projection build, a row records the seq, server receive time and career
clock. It never expires or downgrades inside that build. A rebuild can omit the row when current
folds/final state no longer award it, and can discover a newly added badge from old history.

---

### Task F1 — the `badge_award` table

**Files:** a new projections migration (`0010_badges.sql` — **re-check the number**), exact-DDL
fixture, projector snapshot, projection census, `docs/server.md`, `docs/event-details.md`, new
`docs-site/src/content/docs/badges/index.mdx`, moderation test and next-free decision entry.

```sql
-- projections.db 0010 — merit badges.
--
-- A badge is a milestone: the interesting property is WHEN YOU FIRST GOT IT, so
-- the write is INSERT ... ON CONFLICT DO NOTHING and the row never changes
-- inside a projection build. A rebuild is authoritative and may omit it under
-- the current folds/final flight state (§3.6).
--
-- ONE TABLE, TWO SCOPES. career = '' is the LIFETIME award; career = '<id>' is the
-- per-save award, and the same badge is earned independently in both. The two
-- scopes have identical columns and identical rules, so splitting them would mean
-- two flushes, two dumpers, two read paths and two places to forget something.
-- '' is safe as a sentinel because ingest rejects any career that is not exactly
-- 16 Crockford characters — the same trick event_census uses for `type = ''` and
-- `bucket = ''`.
--
-- No `revoked` column, and there must not be one. A badge row is current
-- projection output, so removal happens only by rebuilding without the row—not
-- by accumulating punitive state. Constitution §8's consequence test also
-- applies: nothing here may ever treat a player differently because of history.
CREATE TABLE badge_award (
  player_id    INTEGER NOT NULL,
  career       TEXT NOT NULL,        -- '' = lifetime; otherwise the save it was earned in
  badge        TEXT NOT NULL,        -- a fixed key, or a family key built from event data
  -- The celestial system it was earned in, denormalised (§3.6). On a per-save
  -- row this is that save's system. On the LIFETIME row ('' career) it is the
  -- system of the save the CURRENT PROJECTION first awards it in. A rebuild may
  -- change this when earlier eligibility changes (§3.6).
  system       TEXT NOT NULL DEFAULT '',
  -- Which save the lifetime row was first earned in. '' on a per-save row,
  -- where `career` already says it.
  first_career TEXT NOT NULL DEFAULT '',
  earned_seq   INTEGER NOT NULL,     -- the projector cursor: the tie-break, and the sort key
  earned_at    INTEGER NOT NULL,     -- unix ms, the SERVER receive stamp — never wall_t
  earned_sim_t REAL,                 -- career clock in seconds; NULL when the event carried none
  context      TEXT,                 -- JSON; the same shape and the same allow-list as player_stat.context
  PRIMARY KEY (player_id, career, badge)
);

-- "which badges has anyone earned in this system", and the per-system holder
-- counts the badge index publishes.
CREATE INDEX badge_system ON badge_award(system, badge, earned_seq);

-- "who has this badge, earliest first" and "how many hold it" from one index.
CREATE INDEX badge_holders ON badge_award(badge, earned_seq);
-- "everything this save earned, in the order it was earned".
CREATE INDEX badge_by_career ON badge_award(player_id, career, earned_seq);
```

In this same task add the table/indexes/schema version to the DDL fixture, its ordered dump to the
equivalence snapshot, its count to `Counts`, and its player-owned rows to purge/shadowban coverage.
Document the projection and rebuild-authoritative/two-scope semantics now; G adds read surfaces, not deferred
fold documentation.

---

### Task F2 — the badge registry

**Files:** new `server/internal/stats/badges.go`; `docs/event-details.md`; the badge index prose;
next-free decision entry.

Model it **exactly** on `boards.go`: compile-time constants, a fixed table, dynamic families built
from event data, a `Describe` that is a pure function of the key, and a `Catalog` gated on
`min_players`. If you find yourself designing something new here, stop and re-read `boards.go` —
every problem this file has, that one has already solved.

```go
// Badge is what a badge is, and it is derived from its key alone — the same
// property `stats.Board` has, and for the same reason: a family badge for a place
// nobody has ever typed here arrives fully described (PROJ-036).
type Badge struct {
	Key   string `json:"badge"`
	Title string `json:"title"`
	// Blurb is one sentence, player-facing, in catlog's voice: dry, British
	// spelling, sentence case, no exclamation marks (docs/ui-design.md §9.2).
	Blurb string `json:"blurb"`
	// Group orders the catalogue and nothing else.
	Group string `json:"group"` // "first-steps" | "flight" | "survival" | "exploration" | "kittens"
	// Tier is 0 for a standalone badge, 1..n for a ladder (Wanderer -> Voyager ->
	// Grand Tour). Purely presentational: a higher tier does not supersede a lower
	// one and never removes it.
	Tier int `json:"tier,omitempty"`
}

func FixedBadges() []Badge
func BadgeFamilies() []badgeFamily          // orbited_<body>, landed_on_<body>, reached_<body>
func OrbitedBadge(body string) (string, bool)   // familyStat's twin — REUSE statSuffix
func DescribeBadge(key string) (Badge, bool)
func KnownBadge(key string, holders int64) (Badge, bool)
func BadgeCatalog(counts map[string]int64, minPlayers int) []Badge
```

**Reuse `statSuffix` and `titleize` verbatim.** Do not copy them; export them from `boards.go` if
they are unexported and the compiler complains. The alphabet rule (lowercase, `[a-z0-9]` first, then
`[a-z0-9._-]`, ≤ 40 chars) exists because the key is a **URL path segment** (PROJ-037), and a badge
key is one too.

**`min_players` for badge families** is the existing `[boards] min_players` (§3.14). Fixed badges are
always listed.

---

### Task F3 — the `Batch` accumulator and the `award` helper

**Files:** `server/internal/stats/batch.go`, `server/internal/stats/fold.go`.

```go
// award records a badge in BOTH scopes: the lifetime award and, when the event
// carries a career, the per-save one.
//
// Once-only. The flush is INSERT ... ON CONFLICT DO NOTHING, so the first event
// that satisfies the predicate is the one that keeps the row, and every later one
// is free. That is the same "whoever got there first keeps it" rule the record
// boards use, spelled the simplest way it can be spelled.
//
// It does NOT go through scoreable() itself — a badge fold decides its own
// eligibility, because flightless events have nothing to gate on. Every
// flight-bearing badge uses the existing final-state scoreable() path; rebuild
// correction is authoritative (§3.6).
func award(ctx context.Context, b *Batch, ev Event, badge string, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putBadge(ev.PlayerID, "", badge, system, ev.Career, ev, cx)
	if ev.Career != "" {
		b.putBadge(ev.PlayerID, ev.Career, badge, system, "", ev, cx)
	}
	return nil
}
```

`flushBadges` goes into `Flush`'s fixed order after `flushCareerStats`, key-sorted like every other
flush. `putBadge` is a first-write merge inside the pending map too: if the same composite key is
offered several times before one flush, retain the candidate with the lowest `earned_seq` and its
matching timestamps/context. SQL `ON CONFLICT DO NOTHING` cannot recover the earliest candidate if
the map already overwrote it. Test three candidates in one unflushed batch and at several flush
sizes. Add `Batch.HasBadge(ctx, playerID, career, badge)` as a read-through for the composite badges
that need to know.

---

### Task F4 — the four badge fold shapes

**Files:** new `server/internal/stats/badgefolds.go`; one line in `fold.go`;
`docs/event-details.md`; the badge index's calculation/eligibility prose; next-free decision entry.

Add `BadgeFolds() []Fold` and append it inside `SecondPassFolds()` **after** `BoardFolds()` and
**before** `LogFolds()`:

```go
func SecondPassFolds() []Fold {
	return append(append(BoardFolds(), BadgeFolds()...), LogFolds()...)
}
```

**The order is load-bearing.** Threshold badges read a counter's post-write board value, so every
board fold must have run first. They intentionally inherit that board's final-state eligibility and
rebuild refinements (§3.6); do not add independent badge progress.

Every concrete fold has a stable unique name containing the badge key, for example
`badge:first_orbit` or `badge-family:orbited`. Validate `SecondPassFolds()` names for duplicates.
Adding a fixed badge/family fold must change `BuildID`; add a test because retroactive rebuild
discovery hashes names, not registry data. Test removal too: deleting a registry+fold entry changes
`BuildID`, and rebuilding omits its former awards.

Four shapes, and every badge in Appendix B is one of them:

**1. Event badges** — one event satisfies the predicate outright.

```go
// eventBadge awards on the first event of a type that passes `when`.
type eventBadge struct {
	badge string
	typ   string
	when  func(ev Event) bool           // nil means "any event of this type"
	cx    func(ev Event) map[string]any // nil means no context
}
```

**2. Threshold badges** — a counter crossed a number.

```go
// thresholdBadge awards when a board's value first reaches n.
//
// It reads the value AFTER the board folds have written it, which is why
// BadgeFolds runs after BoardFolds. It reads through the Batch, so it sees this
// batch's own pending writes exactly as a one-statement-at-a-time path would —
// A rebuild may read a corrected value and therefore remove the award; that is
// the documented rebuild-authoritative semantic (§3.6).
//
// It reads the CAREER-scoped value for the career award and the PLAYER-scoped
// value for the lifetime one. Those are genuinely different questions ("ten
// landings in this save" is not "ten landings ever"), so this shape does not use
// `award` — it calls putBadge for each scope with its own predicate.
type thresholdBadge struct {
	badge string
	stat  string
	n     float64
}
```

**3. Composite badges** — need flight state. Read `flight_state.milestones` (Task D5); never
correlate two events by holding state in the fold, which a rebuild is not guaranteed to reproduce.

**4. Family badges** — the key is built from event data.

```go
// orbitedBodyBadge awards `orbited_<body>` from vehicle.orbit phase=achieved.
//
// THERE IS NO LIST OF BODIES. The key comes from the event, through the same
// statSuffix validation a board key goes through, and the badge is described from
// the key alone. A body whose name cannot form a key gets no badge and still
// counts towards every tier badge — the same rule familyStat already enforces for
// fastest_to_<body> (PROJ-037).
```

**Tests** (`stats/badges_test.go`), with a `readBadges` dumper keyed
`"<player>/<career>/<badge>"`:

1. `TestABadgeIsAwardedOnceAndKeepsTheFirstSeq` — the predicate satisfied three times; one row,
   the earliest `earned_seq`.
2. `TestABadgeIsAwardedInBothScopes` — one event; a `career=''` row and a `career=<id>` row.
3. `TestThePerSaveBadgeIsPerSave` — earned in career A; career B has no row and the lifetime row is
   unchanged.
4. `TestAThresholdBadgeReadsThePostWriteValue` — the tenth landing awards on the tenth event, not
   the eleventh.
5. `TestAThresholdBadgeIsPerScope` — six landings in save A and six in save B awards the lifetime
   ten-badge but neither save's.
6. `TestAFlaggedBeforeCandidateEarnsNoBadge` and
   `TestAFlagAfterCandidateIsRemovedByRebuild`, plus KIA/recovery threshold cases pinning the
   documented rebuild-authoritative correction.
7. `TestABadgeForAnUnkeyableBodyIsSkippedButStillCountsTowardsTiers`.
8. `TestUnrefinedBadgeHistoryRebuildsIdentically` — at three different batch sizes, because the
   threshold shape must not acquire a batch-boundary divergence (PROJ-060). The late
   flag/KIA/recovery cases in test 6 intentionally assert the existing D22 divergence instead.

Each catalogue/predicate commit updates `docs/event-details.md`, badge data/player prose and the
next-free decision entry with the fold. Do not defer eligibility documentation to G or J.

---

### Task F5 — the starter badge catalogue

**Files:** `server/internal/stats/badges.go`, `badgefolds.go`;
`docs/event-details.md`; new `docs-site/src/data/badges.ts`,
`docs-site/src/components/BadgeDetail.astro`, and
`docs-site/src/content/docs/badges/catalog.mdx`; badge index prose; next-free decision entry.

**The full catalogue is Appendix B.** It is **35 fixed badges** across five groups plus three dynamic
family patterns, and every one is derivable from events already on the wire plus Phase C's system
catalogue, Phase D's fields and Phase E's boards. Implement it verbatim after resolving §3.7; the
appendix gives key, title, blurb, group, tier, shape and predicate for each.

**The exploration group has two halves and they answer different questions**, so ship both:

- **the tier ladder** — `wanderer` (3 bodies) → `voyager` (5) → `grand_tour` (8), threshold badges on
  `soi_bodies`. *"How much have you got around?"* Works with no system data at all, which is why it
  is here rather than folded into the everywhere badges.
- **the everywhere badges** — Task F7. *"Is there anything left?"* Needs Phase C.

**The constraint to re-read before writing any of it:** §3.7. The *server* holds no list of bodies.
The everywhere badges compare against the list the **game reported** for that save's system; the tier
badges compare against a number. Neither compiles a body name into Go, and neither may start to.

The evidence, since somebody will want to argue the tier thresholds. Build 2026.8.5.5168 ships
**four** selectable systems, and the default one (`Sol`) has **54 celestial bodies**: 1 star,
8 atmospheric bodies, 13 planetary bodies, 24 minor bodies, 7 comets and an asteroid — Neptune,
Pluto, Titan and the Galilean moons among them. `SolDense` has **3,215**. `SolLite` has **three**.

So the tiers are **not** fractions of "the solar system" — there is no such fixed thing. They are
round numbers that mean something to a player: three worlds is a competent programme, five is a
serious one, eight is most of the interesting ones in the default system. Say that in the blurbs
rather than implying a denominator.

---

### Task F6 — store reads and the census

**Files:** `server/internal/store/projections.go`, `server/internal/readapi/stats.go`.

```go
type BadgeRow struct {
	PlayerID   int64
	Career     string
	Badge      string
	System     string
	FirstCareer string
	EarnedSeq  int64
	EarnedAt   int64
	EarnedSimT sql.NullFloat64
	Context    json.RawMessage
}

func (p *Projections) BadgesForPlayer(ctx context.Context, playerID int64, career string) ([]BadgeRow, error)
func (p *Projections) BadgeHolders(ctx context.Context, badge, system string, limit, offset int) ([]BadgeRow, error)
func (p *Projections) BadgeCounts(ctx context.Context) (map[string]int64, error)                             // career='' only
func (p *Projections) BadgeHolderCount(ctx context.Context, badge, system string) (int64, error)
```

With `system == ""`, holders/count use `career=''` lifetime rows: one current first-award row per player. With
a system filter, query per-save rows in that system and select **one deterministic earliest row per
player** (`earned_seq`, then career tie-break), counting distinct players. The lifetime PK can retain
only the current first-award system, so filtering lifetime rows would omit players who later earned the badge
elsewhere. Never count every save as a holder.

Select/scan `system` and `first_career` on every applicable query. The read API resolves
`first_career` to ordinal and per-player relabel and never publishes the raw value. Resolve badge
endpoint slug→hash before passing the optional system filter into both holders and count so the
reported denominator matches the rows.

Add `badges` and `badge_awards` to `GET /v1/stats`'s `collection` census, memoised on
`(WriteGen, 10 s TTL)` like the rest (PROJ-083).

---

### Task F7 — the everywhere badges

**Community idea #9, built.** Phase C did the hard part; this is the fold.

`system_body` says which bodies exist in a system, its concrete class and §3.7's
normalised kind.
`career_body` says which of them a save has reached. The badge is the subset test, and catlog holds
no list of bodies at any point in it (§3.7).

Two **fixed subset badges** (not dynamic families):

| key | Title | Awarded when |
|---|---|---|
| `been_to_every_planet` | Every World | every `system_body` whose normalised `kind == "planet"` has a `career_body` `'soi'` row |
| `been_to_everything` | Nothing Left | the same for **every** body of every class, including every parentless root |

```go
// everywhereFold — sketch.
//
// Runs on every qualifying vehicle.soi after soiFold has written the arrival.
// `soiFold`'s local `newForCareer` is not shared across folds; the indexed
// anti-join and once-only award make a repeated check safe and deterministic.
//
// THERE IS NO LIST OF BODY NAMES HERE. `kind` is the explicit semantic mapping
// selected in §3.7 and emitted by the mod; concrete `class` remains opaque.
func (everywhereFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	// ... resolve the career's system; bail if unknown
	// missing := b.BodiesNotVisited(ctx, ev, class)   // one indexed anti-join
	// if missing == 0 { award(...) }
}
```

**`Batch.BodiesNotVisited(ctx, ev, kind)` must be read-through aware.** A plain SQL anti-join cannot
see the current batch's buffered `career_body` insertion, so the final body would award one event
late or never. Merge pending `careerBodies` into the stored membership view (or expose a batch-aware
member/count primitive), then evaluate the indexed subset over one system. Test the last missing
body at batch size 1 and a large batch.

**Four rules this fold must obey, each of which is a way to get it subtly wrong:**

1. **Only an effectively complete catalogue can award:** header `reported_complete` is true,
   actual row count equals `body_count`, and the selected subset has at least one row. This prevents
   early award while thousands of body events are still arriving and prevents a vacuous Every World
   on a system with no selected planets. `been_to_everything` likewise requires a non-empty complete
   catalogue.
2. **The award is per save and lifetime, like every badge** (§3.6), and both rows carry the system.
   "Every World" in stock Sol and "Every World" in a twelve-planet conversion are different
   achievements and the badge page must say which.
3. A content patch that adds Neptune creates a different system hash, so the old event history and
   its old-system award remain a separate question. As with every badge, a rebuild may still omit
   that award under current folds/final eligibility (§3.6); there is no explicit revocation path.
4. Apply final-state `scoreable` first, like every flight-bearing board. A later flag may remove the
   badge on rebuild (§3.6).

**The forgery, stated rather than defended against:** a modified client can report a one-planet system
and mint the badge. Constitution §8 accepts that class of thing explicitly, and the tier ladder is no
better protected — an invented SOI transition is just as cheap. It goes in the `DECISIONS.md` entry,
not into a check.

**Tests:** award on the last missing body and not the second-to-last at multiple batch sizes; no
award for missing header, `reported_complete=false`, row count short of `body_count`, or an empty
selected kind; an unknown concrete class still follows its explicit `kind`; both scopes written;
unrefined-history rebuild equality plus the §3.6 late-flag removal case, including §3.7's exact
class/parent-to-kind mapping.

---

## Phase G — badges on the read API, app site, and player docs

### Task G1 — the badge endpoints

**Files:** new `server/internal/readapi/badges.go`; register in `readapi.go`; `docs/ingest-api.md`.

```
GET /v1/badges
  -> {"min_players": n,
      "badges": [{"badge": s, "title": s, "blurb": s, "group": s, "tier"?: n, "holders": n}]}

GET /v1/badges/{badge}?system=<slug>
  -> {"badge": s, "title": s, "blurb": s, "group": s, "holders": n, "limit": n, "offset": n,
      "rows": [{"rank": n, "handle": s, "save": n, "save_id": s,
                "system": {"hash": s, "name": s, "slug": s},
                "earned": unix_ms, "sim_t"?: f, "context"?: {…}}]}
      -- ordered by earned_seq ASC: first to earn it is first.

GET /v1/players/{handle}/badges
  -> {"handle": s, "earned": [ …badge rows… ], "unearned": [ …badge summaries… ]}

GET /v1/players/{handle}/saves/{ordinal}/badges
  -> the same shape, for one save.
```

Rules, all inherited:
- Ban filtering through the same over-fetch-and-drop used by `visibleRows` — **never a second
  filter**.
- Unknown / retired / banned handle → one 404 (PROJ-007). Unknown badge key → 404 unless
  `DescribeBadge` can describe it and somebody holds it (the `Known(stat, players)` rule).
- `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`, including the 404s.
- `context` goes through `Redact` before it is published, and any career in it is relabelled
  (§3.5). **A badge row's `career` field is never published raw**, in either scope.
- Lifetime `/players/{handle}/badges` lists only unearned **fixed** badges; a player may have several
  systems and there is no single bounded “their system” checklist. The save endpoint additionally
  lists unearned `orbited_` / `landed_on_` / `reached_` keys for that save's own **effectively
  complete** system catalogue. Take them from `system_body`, not from every family key ever seen.
- `?system=<slug>` filters a badge's holders to one system, which is the only way to compare holders
  of `been_to_every_planet` meaningfully. This view is derived from per-save awards, distinct by
  player with the earliest earning save as representative; the single lifetime row remains
  the current first award and cannot represent later-system achievements.

There is no read-API version bump. Record the four final pre-launch shapes and visibility/denominator
semantics in `docs/ingest-api.md` §4.8.

---

### Task G2 — the badge pages

**Files:** `templates/badges.gohtml`, `templates/badge.gohtml`, `templates/player_badges.gohtml`;
`templates.go`; `pages.go`; `web.go`; `templates/profile.gohtml`; `templates/layout.gohtml`;
`web_test.go`; `docs/ui-design.md`.

Routes:

```go
	mux.HandleFunc("GET /badges", s.handleBadges)
	mux.HandleFunc("GET /badges/{badge}", s.handleBadge)
	mux.HandleFunc("GET /p/{handle}/badges", s.handlePlayerBadges)
	mux.HandleFunc("GET /p/{handle}/saves/{ordinal}/badges", s.handleSaveBadges)
```

**Add `Badges` to the header nav** in `layout.gohtml` (`id="nav-badges"`, the `aria-current`
conditional) — it is a top-level thing a visitor should find, unlike saves. That makes the nav six
links; add the rows to `docs/ui-design.md` §5.1's page inventory in the same commit.

**Design rules that are not negotiable** (`docs/ui-design.md`, all quoted from the spec):
- **One container shape.** A badge tile is a `.panel`, or a `.tiles` grid cell. **There is no second
  card style**, and this is exactly the feature that will tempt somebody to invent one.
- **No new colour.** Earned uses `--color-accent` as a **fill**, never as text —
  `--color-accent-text` carries any accent-coloured word. Unearned is `--color-fg-muted`, not a new
  grey.
- **No new motion.** The site has exactly two animations (the feed-arrival flash and a 150 ms hover
  colour transition), both wrapped in `prefers-reduced-motion`. A badge does not sparkle, pulse, or
  animate on award.
- **No emoji, no icons that carry meaning alone.** Badge identity is its title. If an icon is added
  later it is decorative and `aria-hidden`.
- Earned dates are **fixed UTC** (`2026-08-07 14:32 UTC`), never localised — a leaderboard is a
  shared artefact.
- **Every earned badge shows the system it was earned in, by name**, and the save
  (`Save 2 · Sol`), linking to `/p/{handle}/saves/{n}` and `/systems/{slug}`. On the
  badge's own holder page the system is a **column**, because "Every World" in a twelve-planet
  conversion is not the same achievement and a reader comparing holders has to see that (§3.6). A
  badge earned in a save with no known system renders the em dash, never a blank chip.
- `tabular-nums` on every number; `data-value` on anything a test might read.
- Empty state, matching the existing voice: **"No badges yet. Fly something."**
- **Tone:** dry, understated, affectionate about failure; British spelling; sentence case for
  everything we write; no exclamation marks; never invent a fact for a joke.

Add a `Badges` link to `profile.gohtml`'s button row, and a badge count to the save page from
Task B5; this is also where its Badges column appears, because the data source now exists.

---

### Task G3 — the docs-site half

**Audit/files:** the badge data, component, index and catalogue created in F1/F5;
`docs-site/astro.config.mjs`. G3 adds the navigation/read-surface links and audits player copy
against the implemented endpoint; it does not defer creation of the behavior documentation.

1. **`badges.ts`** — mirror `boards.ts` exactly: a doc comment saying **DERIVED DATA** and that
   `docs/event-details.md` wins any disagreement; an exported interface; a `BADGES` array; a
   `BADGE_FAMILIES` array; a `badgeByKey()` lookup.

```ts
export interface MeritBadge {
  badge: string;
  title: string;
  group: "first-steps" | "flight" | "survival" | "exploration" | "kittens";
  tier?: number;
  /** One line: what you did to get it. */
  what: string;
  /** Exactly what catlog looked at. */
  how: string;
  /** Anything that stops it being awarded. */
  excluded: string[];
  /** Event types that can award it. */
  from: string[];
  family?: { pattern: string; from: string };
}
```

2. **`BadgeDetail.astro`** — copy `BoardDetail.astro`, including its **fail-fast**
   `if (!badge) throw new Error(...)`, which is what makes a badge named in a page but missing from
   the data module a **build failure**. Import `./detail.css`; **name no hex value** — every colour
   goes through a Starlight token (`custom.css` maps them onto the app palette).
   `catalog.mdx` imports `BADGES`/`BADGE_FAMILIES` and renders every fixed entry and family pattern
   through the component at build time; no hand-maintained second list and no React island.

3. **New sidebar section** in `astro.config.mjs`, after `Leaderboards`:

```js
        {
          label: "Merit badges",
          items: [
            { label: "How badges work", slug: "badges" },
            { label: "Every badge", slug: "badges/catalog" },
          ],
        },
```

4. **`badges/index.mdx`** must say, in player terms:
   - A badge is once-only in the current projection. A rebuild can remove one after a later
     disqualifying fact or a catalogue/rule change, and can discover a newly added badge from old
     history. There is no level or score.
   - A qualifying history projects each badge **twice**: once at lifetime scope and once in the save
     where it first qualified. A new save starts with no per-save rows of its own; the lifetime row
     remains while the current projection still awards it.
   - Anyone can look at anyone's badges — they are as public as a leaderboard row.
   - **How Every World avoids a hardcoded list**, in the player's own terms: the save reports the
     complete catalogue its game loaded; the badge compares its visits with that catalogue. catlog
     compiles no list of body names, so a game update or system mod supplies its own denominator.
     Use the resolved §3.7 wording and say that incomplete catalogues award nothing.
   - A flagged flight earns nothing, linking to `leaderboards/eligibility`.

---

### Task G4 — seed and e2e for badges

- **Seed:** demo players earn a representative spread — at least one fixed badge, one family badge,
  one tier badge, and at least one badge held by **two** players so the family gate (`min_players`)
  publishes something.
- **e2e:** new `site/e2e/badges.spec.ts` — the catalogue lists what `GET /v1/badges` lists (row for
  row, **no count assertion** — PROJ-039), a badge page ranks holders earliest-first, a player's page
  separates earned from unearned, and a save's badge page is a subset of the player's.

---

## Phase H — weekly challenges, server side

**Goal:** a curated rule, over an explicitly-dated window, ranked like a board.

**Read §3.8 and §3.9 before starting.** The two decisions that shape every line of this phase are
that a challenge is **compile-time** and that its window is measured on **`ev.RecvTime`**.

---

### Task H1 — `challenge_stat` and the distinct-member set

**Files:** a new projections migration (`0011_challenges.sql` — **re-check the number**), exact-DDL
fixture, projector snapshot, census, moderation test, `docs/server.md`, `docs/event-details.md`, new
`docs-site/src/content/docs/challenges/index.mdx`, and next-free decision entry.

```sql
-- projections.db 0011 — time-boxed challenges.
--
-- A challenge is a board with a curated rule and an explicit start and end date.
-- Its definition lives in server/internal/stats/challenges.go — in the deployed
-- ARTIFACT, never in mutable runtime state — because a projection has to be
-- rebuildable and rebuild has to equal incremental. A definition that could
-- change while events were being folded would make a rebuild replay history
-- against today's rules while the incremental projection was built against
-- yesterday's, and the two would disagree by construction.
--
-- Not folded into player_stat_period: that table's `period` is a calendar window
-- with a retention horizon that DELETES aged-out buckets, and a challenge's rows
-- have to outlive their week — the archive of past challenges is the point.
-- Overloading it would make the retention delete a landmine.
--
-- Scope sentinels:
--   player: career='', system=''
--   career: career='<id>', system='<hash>'
--   system: career='', system='<hash>'
CREATE TABLE challenge_stat (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,
  challenge   TEXT NOT NULL,
  -- Empty for a genuinely cross-system player aggregate. Non-empty for career
  -- and system scope; never label a mixed aggregate with its last contributor.
  system      TEXT NOT NULL DEFAULT '',
  value       REAL NOT NULL,
  context     TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, system, challenge)
);

CREATE INDEX challenge_rank ON challenge_stat(challenge, system, value, updated_seq);

-- Batch-aware distinct members for set-valued challenge rules such as
-- coasting_class. career/system use the same sentinels as challenge_stat.
CREATE TABLE challenge_member (
  player_id INTEGER NOT NULL,
  career    TEXT NOT NULL,
  system    TEXT NOT NULL,
  challenge TEXT NOT NULL,
  member    TEXT NOT NULL,
  first_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, system, challenge, member)
);
CREATE INDEX challenge_member_count
  ON challenge_member(challenge, player_id, career, system);
```

**No retention.** A closed challenge keeps its rows forever; the archive of past weeks is a feature.
Row count is entrants × challenges plus qualifying distinct members, growing at the rate the owner
writes challenges and the bounded members their rules admit.

Both tables are added to all three hand-maintained lists and structural moderation coverage in this
task. `challenge_member` must have pending-row read-through operations so results do not depend on
flush/batch boundaries.

**Comparison-sensitive challenges use system scope.** `heavy_lift_week`, `coasting_class` and
`feather_touch` are `ScopeSystem`, ranking `(player, system)` rows. `speedrun_orbit` is
`ScopeCareer`. Genuine global `tumbleweek`/`full_house` rows are `ScopePlayer` with `system=''` and
publish no system label. Never label a mixed player aggregate with its latest contributor.

---

### Task H2 — the challenge registry

**Files:** new `server/internal/stats/challenges.go`; `server/cmd/catlogd/main.go` (startup
validation wiring); unit and exact startup-failure tests; `docs/event-details.md`; new
`docs-site/src/data/challenges.ts` and `docs-site/src/components/ChallengeDetail.astro`; challenge
index prose; next-free decision entry.

```go
// A challenge is a board with a curated rule and a start and end date.
//
// This file IS the interface, the way catlog.toml's [events] table is the mod's
// (see docs/ROADMAP.md, "An in-game editor for the [events] table"). Adding a
// challenge is: append a Challenge literal here, write its fold, add its player-
// facing copy to docs-site/src/data/challenges.ts, commit, deploy. There is no
// admin API and no definition table, and §3.8 of LOTS_OF_THINGS_PLAN.md says why
// at length.
//
// Adding a challenge whose window is already in the past is legal and works: a
// rebuild folds it and its board fills from history. That is PROJ-090's "a new
// fold becomes retroactive by rebuild, and no backfill script is written",
// unchanged.
type Challenge struct {
	Key   string `json:"challenge"`
	Title string `json:"title"`
	// Blurb is the rule, in one or two sentences, in the player's words.
	Blurb string `json:"blurb"`
	// Opens and Closes are unix MILLISECONDS, UTC, half-open [Opens, Closes).
	// They are compared against ev.RecvTime — the server's own receive stamp,
	// never time.Now() and never the client's wall_t. PROJ-043.
	Opens  int64 `json:"opens"`
	Closes int64 `json:"closes"`
	// Unit and Ascending mean exactly what they mean on a Board.
	Unit      string `json:"unit"`
	Ascending bool   `json:"ascending"`
	// Scope is stats.ScopePlayer, ScopeCareer or ScopeSystem (§H1).
	Scope string `json:"scope"`
}

func Challenges() []Challenge
func ChallengeByKey(key string) (Challenge, bool)
// Open reports whether now falls in the window. Used ONLY by the read API for
// presentation; a fold never asks, because a fold is told by the event.
func (c Challenge) Open(nowMS int64) bool
// InWindow is the fold's gate.
func (c Challenge) InWindow(recvMS int64) bool { return recvMS >= c.Opens && recvMS < c.Closes }
```

**Validation at startup, not at fold time.** Add `ValidateChallenges() error`, called from
`catlogd`'s wiring, refusing: a duplicate key, a key that fails `statSuffix`, `Opens <= 0`,
`Closes <= Opens`, an unknown `Scope`, duplicate fold names, and a key that collides with a board
key. A bad challenge should stop the server starting, not produce a quietly wrong board. Add
`TestEveryShippedChallengeIsValid`.

---

### Task H3 — the fold shape

**Files:** new `server/internal/stats/challengefolds.go`; one line in `fold.go`;
`docs/event-details.md`; challenge eligibility/window prose; next-free decision entry.

Add `ChallengeFolds() []Fold` and append it inside `SecondPassFolds()` after `BadgeFolds()`.

```go
// challengeFold is one challenge's rule.
//
// The window gate comes first and is the same in every one of them, so it lives
// here rather than in each `value` func:
//
//	if ev.RecvTime <= 0 || !c.InWindow(ev.RecvTime) { return nil }
//
// An event with RecvTime <= 0 is in no challenge, which is the same answer
// periods give: a row whose window nobody can determine belongs in no window.
type challengeFold struct {
	c    Challenge
	kind statKind
	// value returns the contribution and whether this event contributes at all.
	// It is where the arbitrary part of "arbitrary rules" lives.
	value func(ctx context.Context, b *Batch, ev Event) (float64, map[string]any, bool, error)
}
```

and the three write helpers, mirroring `putRecord` / `putBest` / `addCount` exactly, including the
strict-inequality tie rule:

```go
func putChallenge(ctx context.Context, b *Batch, ev Event, c Challenge, kind statKind, value float64, cx map[string]any) error
```

`Scope` decides the composite row key: player writes `career=''`, `system=''`; career writes the
event career and its resolved system; system writes `career=''` and the resolved system. Career/
system scope is a no-op if its identity is unavailable.

Every concrete fold name is `challenge:<key>`, stable and unique. `ChallengeFolds()` contains one
name per registry definition, validation rejects duplicates, and a test proves adding a definition
changes `BuildID`; retroactive past challenges depend on that fact.

**Every challenge fold that reads a flight-bearing event calls `scoreable` first.** A flagged flight
scores nothing, on a challenge exactly as on a board.

**Tests:**
- `TestAnEventBeforeTheWindowScoresNothing` / `...After...` / `...OnTheOpeningInstant...` (inclusive)
  / `...OnTheClosingInstant...` (exclusive).
- `TestAnEventWithNoRecvTimeIsInNoChallenge`.
- `TestChallengeTieKeepsTheEarlierSeq`.
- `TestUnrefinedChallengeHistoryRebuildsIdentically` — fold a history spanning a challenge window
  at several batch sizes, rebuild and diff.
- `TestLateFlagRemovesAChallengeContributionOnRebuild` — pins the inherited D22 correction rather
  than falsely claiming universal equivalence.
- `TestAChallengeAddedAfterTheFactFillsFromHistoryOnRebuild` — pins §3.8's retroactivity claim.

---

### Task H4 — six starter challenges

**Files:** `challenges.go`, `challengefolds.go`; `docs/event-details.md`;
`docs-site/src/data/challenges.ts`; new
`docs-site/src/content/docs/challenges/archive.mdx`; challenge index prose; next-free decision entry.

**The full rules are Appendix C.** Each shipped literal also sets its own explicit UTC Unix-ms
`Opens`/`Closes`; there is no global challenge calendar or runtime-relative window. They prove the
mechanics across every fold kind and all scopes and are worked examples for later challenges:

| key | Title | Kind | Scope | Reads |
|---|---|---|---|---|
| `heavy_lift_week` | Heavy Lift Week | record | **system** | `vehicle.orbit` `mass_kg` on that event career's system home body |
| `speedrun_orbit` | From Scratch To Orbit | best (asc) | career | `vehicle.orbit` `sim_t` |
| `tumbleweek` | Tumbleweek | count | player | `kitten.tumble` |
| `coasting_class` | Coasting Class | record | **system** | `vehicle.soi` joined to known `flight_state.engine_count == 0`, distinct `(system, body)` members in-window |
| `feather_touch` | Feather Touch | best (asc) | **system** | `vehicle.landed` `vertical_speed_ms > 0`, away from that system's home body |
| `full_house` | Full House | record | player | `flight.ended` `reason=recovered`, `crew_count` |

**Two naming rules that are load-bearing:**

- **"Payload" is fine as a word, and it means the vehicle's current total mass.** The owner has
  settled that. KSA genuinely has no payload concept — no `IsPayload`, no `PayloadMass`, and the
  per-stage `SequencePerformance` split is refreshed in flight only while one of two specific windows
  is open, so it holds stale editor data otherwise — but the loose sense is the one players use.
  So the challenge may say payload, **provided the definition is on the page**: it is what the whole
  vehicle weighed at the instant orbit was achieved, propellant included. Both documents say that
  sentence; neither implies catlog can separate the cargo from the rocket.
- **`coasting_class` is not called "no propulsion".** It means no *engine* was installed when the
  flight began. RCS thrusters, decoupler springs and docking-port pushoff all impart velocity, and
  the site says so (Task D2).

**Do not hardcode `"earth"` or add a challenge-level home-body literal.** Where a rule says home,
resolve `ev.Career → career.system → system.home_body` and compare the event's canonical body key.
That is the only rule valid across replaceable systems. The worked heavy-lift predicate must include
this gate, and Feather Touch uses the inverse. Missing system/home means no contribution.

`coasting_class` must not reuse `career_body`: that table includes events outside the challenge and
engine-powered visits, and an old row can suppress a later qualifying visit. Insert a canonical
member such as `<system>\x00<body>` into `challenge_member` only after the recv-time, scoreable and
known-`engine_count == 0` gates; use its read-through novelty/count to update the score. Test an earlier
powered visit, later qualifying visit, two systems with the same body name, batch sizes and rebuild.

---

### Task H5 — store reads

**Files:** `server/internal/store/projections.go`.

```go
type ChallengeRow struct { PlayerID int64; Career, System, Challenge string; Value float64; Context json.RawMessage; UpdatedSeq int64 }
func (p *Projections) ChallengeLeaderboard(ctx context.Context, challenge, system string, asc bool, limit, offset int) ([]ChallengeRow, error)
func (p *Projections) ChallengeAhead(ctx context.Context, challenge, system string, value float64, seq int64, asc bool) (int64, error)
func (p *Projections) ChallengeEntrants(ctx context.Context, challenge, system string) (int64, error)
func (p *Projections) ChallengesForPlayer(ctx context.Context, playerID int64) ([]ChallengeRow, error)
```

Use a store-owned challenge row: career and system sentinels encode three scopes and are not
identical to `CareerStatRow`'s save ordinal/join shape. Canonical ordering and optional system filter
match every board.

---

## Phase I — challenges on the read API, app site, and player docs

### Task I1 — the challenge endpoints

**Files:** new `server/internal/readapi/challenges.go`; `docs/ingest-api.md`.

```
GET /v1/challenges
  -> {"now": unix_ms,
      "challenges": [{"challenge": s, "title": s, "blurb": s, "unit": s, "ascending": b,
                      "scope": s, "opens": unix_ms, "closes": unix_ms,
                      "state": "upcoming"|"open"|"closed", "entrants": n}]}

GET /v1/challenges/{challenge}
  -> {…the same fields…, "limit": n, "offset": n,
      "rows": [{"rank": n, "handle": s, "save"?: n, "save_id"?: s,
                "system"?: {"hash": s, "name": s, "slug": s},
                "value": f, "context"?: {…}, "updated": unix_ms, "rewound"?: true}]}
```

- `state` is derived from **the server clock** (`s.deps.Now()`), never the browser's — the same rule
  `?period=` without `?at=` already follows.
- Ordered newest-window-first in the index, with open challenges before upcoming before closed.
- Rows go through B1's factored row-source visibility helper and the same `Redact`; do not copy the
  player-stat-specific `visibleRows`. `save`/`save_id` appear only on career scope; system scope
  carries `SystemRef`; player scope carries neither unless its definition is constrained to one.
- **A closed challenge is served exactly as an open one.** Its rows are the archive, and the archive
  is a feature.
- Cache headers as everywhere else. Note the index's `state` flips at a known instant and
  `s-maxage=30` means a CDN can be up to 30 seconds stale about it — which is fine and worth a
  comment, because somebody will notice.

---

### Task I2 — the challenge pages

**Files:** `templates/challenges.gohtml`, `templates/challenge.gohtml`; `templates.go`; `pages.go`;
`web.go`; `layout.gohtml`; `home.gohtml`; `web_test.go`; `docs/ui-design.md`.

```go
	mux.HandleFunc("GET /challenges", s.handleChallenges)
	mux.HandleFunc("GET /challenges/{challenge}", s.handleChallenge)
```

- **Nav gains `Challenges`.** With Badges from Task G2 that is seven header links, which is the most
  the header should carry — say so in `docs/ui-design.md` §5.1 so the next feature knows the budget
  is spent.
- `/challenges` renders three groups — **Open now**, **Coming up**, **Finished** — each a `.panel`
  with the standard table. Empty states: *"Nothing running just now."* / *"Nothing scheduled yet."* /
  *"Nothing has finished yet."*
- The **home page** gains a compact panel for the currently-open challenge, reusing `board-table`
  with `Compact: true` — the partial already takes that flag.
- Closing time renders as **fixed UTC**, with a plain relative hint beside it (*"closes in 3 days"*)
  computed **server-side** from `s.deps.Now()`. No client-side clock; `intl.js` handles numbers, not
  dates.
- The deadline sentence, in catlog's voice, on every open challenge page:
  *"Your flights have to reach catlog before it closes. If you play offline, get back online in
  time."* That is §3.9's honest limitation, stated rather than engineered around.

---

### Task I3 — the docs-site half

**Audit/files:** the challenge data, component, index and archive created in H1/H2/H4;
`docs-site/astro.config.mjs`. I3 adds navigation/read-surface links and audits the copy against the
implemented endpoint; it does not defer the behavioral documentation.

`challenges/index.mdx` must cover:
- What a challenge is: a leaderboard with a rule and a deadline. It does not affect the ordinary
  boards, and nothing about it is a rank or a score attached to you.
- **How the deadline is measured**, and the offline caveat.
- Windows are **UTC**, always, so everyone's week is the same week.
- The same eligibility rules as everything else — a flagged flight scores nothing.
- A challenge that has closed **keeps its results**; nothing is deleted.

`challenges/archive.mdx` is the page that explains that past challenges stay readable, and links to
the live index. **Do not hand-maintain a list of past challenges in MDX** — it would go stale
immediately. `challenges.ts` mirrors the shipped registry, and the rule from `boards.ts` applies
unchanged: it is DERIVED DATA and `docs/event-details.md` wins.

Define a typed `Challenge` interface mirroring the Go fields, a `CHALLENGES` array, lookup helpers,
and a fail-fast `ChallengeDetail.astro` modelled on `BoardDetail.astro`. The index/archive MDX imports
and maps the typed data at build time; no React island is needed. Every task runs `pnpm check`.

---

### Task I4 — seed and e2e for challenges

- **Seed:** define one challenge in the seed's own window whose demo events fall inside it, and one
  already closed, so `/challenges` renders two of its three groups and the archive is exercised.
  Before `/admin/seed`, post a fixed `at_ms` to `/admin/clock`; choose fixed challenge windows that
  contain that instant. `/admin/seed` uses `s.deps.Now()` and the current test environment does not
  move it automatically. Never define a seed challenge relative to `time.Now()`.
- **e2e:** `site/e2e/challenges.spec.ts` — the index groups by state, an open challenge ranks, a
  closed one still serves its rows, the home-page panel matches `/v1/challenges`, and values are read
  from `data-value`.

---

## Phase J — the documentation sweep and the release gate

Phases A–I each carry their own doc updates, in their own commits, because
Constitution §9 has no deferred form. **This phase is the sweep that proves nothing was missed**, and
the place the reasoning gets written down once rather than eight times.

---

### Task J1 — `docs/DECISIONS.md`

**Audit only.** Every implementation task already took the next free number in the right area and
landed its reasoning with its code/docs. Re-scan `docs/DECISIONS.md` and verify coverage for:
career/system scopes and privacy; sibling set meanings and corrected lifetime distance; system
survey/hash/identity and the resolved owner choices; final-v1 payload reads; new board semantics;
badge permanence/catalogue/everywhere rule; challenge compile-time windows/scopes/distinct sets;
structural moderation; visibility denominators; and navigation/system-display rules.

Never copy numbers from this plan. At audit time the old draft already collided with `MOD-080` and
`UI-045`/`UI-046`; current maxima must be re-read immediately before each implementation commit.
Missing reasoning is fixed in the **owning task's commit before merge**, not assigned retroactively
in Phase J.

---

### Task J2 — `docs/event-details.md`

The single largest doc change, and the one the next reader will trust over the code.

- **Contents** — new sections for **Career scope**, **Celestial systems**, **Badges** and
  **Challenges**.
- **The registry table** — two new type rows; every type remains `ver: 1`, so preserve and verify
  that sentence in both reference documents.
- **Boards** — every new board's row, the new dynamic family, and the final section title
  **“The 50 fixed boards”** (40 today + 2 in A6 + 8 in E).
- **Fold detail, board by board** — an entry per new board.
- **Projection tables** — verify all nine new tables, including `challenge_member`, with merge
  rules and guards. Do not retain the old “becomes five” count.
- **State projections** — `career` gains `ordinal`, `system` and `system_changed`; `career_body`,
  `career_kitten`, `system` and `system_body` are new and get entries of their own;
  `career_body` gains a third `kind`; `flight_state` gains `milestones` and the final fact columns.
  `player_body` and `kitten` are **unchanged** — say so, since a reader will
  expect them to have moved.
- **Suppression and eligibility matrix** — a row per new gate. This table is how a reader answers
  "why did my thing not count", and an ungated new board is a support question.
- **Rebuild ≠ incremental** — do not invent a pre-launch late-system class. Challenges and badges
  inherit applicable existing late-flag/refinement divergences; a badge may disappear when the
  authoritative rebuild no longer awards it (§3.6). Verify deterministic cases: recv-time windows,
  career ids, first-seq ordinals/slugs, direct sim-time playtime, self-identifying catalogue events,
  and batch-aware pending sets. Add a numbered divergence only if implementation evidence finds a
  genuinely new class.
- **Conformance coverage** — the new vector lines and what each pins.
- **Known drift** — clear anything this work fixed; add anything it knowingly left.

---

### Task J3 — `docs/ROADMAP.md`

Under *Deliberately not built*, with reasoning:

1. **A server-side list of celestial bodies** (§3.7). The everywhere badges are *built*, and the
   entry has to say precisely what is still refused: catlog compiles no body name into Go. The list
   is in the log, per system, because the game reported it. Update the existing PROJ-033 area to
   point here so the two read as one position rather than a reversal.
1b. **Building the 3D view** (§3.20). The *data* for it is now guaranteed; the renderer is not
   planned, not scoped and not owed. Record what exists (`GET /v1/systems/{slug}`, the element fields
   on `vehicle.orbit`, `telemetry.window.state`) so whoever picks it up starts from the contract
   rather than from a survey.
2. **A `kittens_scuttled` board** (Task E4) — Constitution §8's consequence test.
3. **Payload-versus-booster mass** — extend the existing *Propellant, Δv and any efficiency board*
   entry: `SequencePerformance` has exactly the per-stage split needed and is refreshed in flight
   only while one of two windows is open, so it holds stale editor data the rest of the time. It is
   not a cost question; the data is not trustworthy.
4. **Δv as a vehicle statistic** — same entry. `NavBallData.DeltaV` is the *active stage only* and is
   gated on the same two windows; `ThrustWeightRatio` is a HUD number using surface gravity and
   current throttle, not a vehicle capability. Integrating `KinematicMeasurements.DeltaVelocityCci`
   is the honest form if it is ever wanted, and PROJ-099's *record it, never validate it* rule would
   have to be written down first.
5. **Reentry-heat boards** — KSA has **no thermal simulation**. The only part-path `Temperature` is
   an FX value; there is no part heat and no overheat.
6. **Anything from Appendix D** that gets asked for later — it is listed there with its evidence so
   the next survey is a lookup.

Under *Open, unblocked* — and in the §1 "the mod has never run inside KSA" checklist, in the same
style as the existing rows — add the in-game verification items this plan creates:

- `engine_count` is positive on a rocket with engines, **is** explicit 0 on a probe with only RCS,
  and is absent rather than 0 when the KSA read fails;
- `part_count` on a RUD matches what the vehicle actually had;
- `kitten.tumble.from` reads `airborne` on a botched landing and `grounded` on a trip;
- **the cached system survey emits at each session boundary before `session.started`**, and reports the stock body
  count with no missing bodies and no duplicates;
- **the same save loaded twice produces the same system hash**, and a hand-edited `Astronomicals.xml`
  produces a different one and sets `system_changed`;
- `vehicle.orbit`'s five new elements are finite on a real orbit and `period_s` is 0 on an escape;
- `telemetry.window.state` is present in flight and **absent** where the read fails, never `{0,0,0}`;
- the measured cost of the survey and of the `state` read, against Constitution §3 (Tasks C2.4,
  D3c).

---

### Task J4 — everything else

| Document | Change |
|---|---|
| `docs/events.md` | Two new v1 type rows, five final-v1 payload additions, `from`/`class` as open sets, `kind` as the resolved semantic mapping, and type count 23 → 25; all remain `ver: 1` |
| `docs/ingest-api.md` | §4.8 final pre-launch shapes: `?scope=`/`?system=`, scopes/body-derived metadata, `SystemRef`, two saves, two systems, four badge and two challenge endpoints; no API version bump |
| `docs/server.md` | Its §5.6 folds and all **nine** new projection tables; §5.4 final DDL; §5.3 only if config changed. These are `server.md`'s headings, not new frozen `§` citations. |
| `docs/mod.md` | §7.2 detection rules for `kitten.tumble.from`; §7.4 for the two new reads |
| `docs/ksa-integration.md` | Sections for `EngineController` enumeration, `LocomotionMode`, **the celestial-system survey** (enumeration, the element set, the safe read point) and **the vehicle state vector** (frame, units, accessor) — each with `file:line` against build 2026.8.5.5168 and a `[KsaAnchor]` risk rating. This is the largest addition to that document since it was written |
| `docs/ui-design.md` | Its §5.1 inventory for **ten route patterns** (2 saves + 2 systems + 4 badges + 2 challenges), §11 DOM ids, nav budget and the system-name rule |
| `docs/ARCHITECTURE.md` | Only if a new top-level directory appears. **Never mint a new `§` number.** |
| `DEVELOPMENT.md` | Any new Make target or test mode |
| `README.md` | Badges and challenges are things a visitor to the website notices |
| `docs/integrity-audit.md` | **Only if** something here turns out to be an integrity check. Nothing in this plan is; if that changes, it goes here against Constitution §8's five tests |

---

### Task J5 — the release gate

Run, in order, and paste the actual output into the final commit message or the PR body — not a
summary of it:

```
make test              # Go + C#, everything
make test-integration  # real mod/server cross-language contract path
make e2e               # the Playwright suite against a real server
make e2e-full          # mod payloads through the full end-to-end surface
cd docs-site && pnpm check     # oxlint + oxfmt + astro build
make testvectors && git diff --exit-code contracts/testdata   # vectors are current
```

Then, by hand:

1. **Create a fresh final database, ingest representative history, then force a rebuild and diff
   it.** `catlogctl rebuild` (it watches; `-detach` returns immediately, `-status` reports phase and
   progress) then compare rebuilt projections with the captured incremental state. The test covers
   this in miniature; do it once for real. `POST /admin/projections/rebuild` now
   answers `202` with a job rather than blocking, and `{"wait": true}` is the blocking form for
   scripts (`PROJ-103`).
2. **Prove the fresh schema is exact.** Inspect the migration DDL test/census/snapshot output for all
   nine tables and every new index/column. There is deliberately no previous-binary or old-database
   deployment test.
3. Open every new page at a viewport of 1280×900 **and** at 380 px, and confirm no page scrolls
   horizontally — wide tables scroll inside their `.table-wrap`, never the body (UI-042).
4. Open `/boards/landings?scope=career` with **no data** and confirm the empty state, not a broken
   table.
5. Confirm no public JSON response anywhere contains a raw 16-character career id (Task B3's test
   does this; look at one response by eye as well) — **and confirm the system hash *is* there**,
   unrelabelled, which is the opposite requirement on a value of the same shape (§3.19).
6. **Fetch `GET /v1/systems/{slug}` and check the resolved §3.20 contract:** effective completeness
   is true; every non-root body has the six orbital-shape keys; unbound period is absent; parents
   resolve; every parentless root is listed in `roots` and every body belongs to one tree. Under
   verify the required orientation-at-zero quaternion and an exact ground marker.
7. Re-read `AGENTS.md`'s update table and tick every row this work touched.

---

## Appendix A — the new boards, in full

Everything here goes in `stats.fixedBoards` (append at the end so existing display order is
untouched), in `docs/event-details.md`'s Boards table, and in `docs-site/src/data/boards.ts`.
`Career` in this table means the existing `Board.Career` flag — *the value is a career-relative
time* — not the new scope, which every board has.

| key | Title | Unit | Asc | Career | Fold | Source | Gate |
|---|---|---|---|---|---|---|---|
| `career_playtime` | Longest Save | `ms` | no | **yes** | record | any event with `career` + `sim_t` | `sim_t > 0`. **No flag gate** — a duration is not a feat |
| `play_sessions` | Play Sessions | `sessions` | no | no | count | `session.started` | — |
| `botched_landings` | Did Not Land On Their Feet | `tumbles` | no | no | count | `kitten.tumble` | `from == "airborne"`, `scoreable` |
| `parts_lost` | Parts In Lost Vehicles | `parts` | no | no | count (+`part_count`) | `vehicle.rud` | `part_count > 0`, `scoreable` |
| `biggest_parts_lost` | Biggest Vehicle Lost | `parts` | no | no | record | `vehicle.rud` | `part_count > 0`, `scoreable` |
| `kittens_to_orbit_and_back` | Kittens To Orbit And Home | `kittens` | no | no | count (set-backed) | `flight.ended` | `reason == "recovered"`, `milestones & orbit`, `scoreable` |
| `biggest_crew_wreck` | Most Kittens Aboard A Lost Vehicle | `kittens` | no | no | record | `vehicle.rud` | `crew_count >= 1`, `scoreable` |
| `kittens_wrecked` | Kittens Aboard Lost Vehicles | `kittens` | no | no | count (+`crew_count`) | `vehicle.rud` | `crew_count >= 1`, `scoreable` |
| `bodies_by_1y` | Worlds In The First Year | `bodies` | no | no | set-derived best save per scope | `vehicle.soi` | `career_body.first_sim_t < 31 536 000`, career + clock, `scoreable` |
| `bodies_by_10y` | Worlds In Ten Years | `bodies` | no | no | set-derived best save per scope | `vehicle.soi` | `career_body.first_sim_t < 315 360 000`, career + clock, `scoreable` |

**New dynamic family**, registered in `families` under `kitten_tumbles`:

| prefix | Title | Unit | Asc | Key from |
|---|---|---|---|---|
| `tumbles_on_` | `"Tumbles on " + titleize(body)` | `tumbles` | no | `kitten.tumble.body` |

**No new units.** `sessions`, `parts`, `tumbles`, `kittens`, `bodies` are counter labels, and
`units.Split` renders an unrecognised unit as `three-sig-figs + " " + unit` with `units.Label`
returning it verbatim — which is how `RUDs`, `landings` and `missions` already work with no
registration. `career_playtime` uses `ms`, which already renders as a duration ladder. **Nothing in
`units.go` changes, and therefore nothing in `units.Conformance` changes.** If that turns out to be
wrong, the rule is two edits in one commit: the rule in `units.go`, and the row in `Conformance` that
pins it.

---

## Appendix B — the badge catalogue, in full

**Thirty-five fixed badges plus three dynamic family patterns.** Shape is one of the four in Task F4,
plus the fixed subset shape Task F7 adds. Every predicate is satisfiable from final-v1 events plus
the system catalogue and Phase E projections.

Every flight-bearing badge uses the existing final-state `scoreable` rule. The catalogue does not
repeat that gate per row; §3.6 documents why a later disqualifying fact can remove an award on
rebuild.

### Group `first-steps` — event badges

| key | Title | Blurb | Predicate |
|---|---|---|---|
| `first_flight` | Off The Pad | Your first flight. | first `flight.started` |
| `first_stage` | Separation | You let go of something on purpose. | first `vehicle.staging` |
| `first_space` | Above The Air | You left the atmosphere. | first `vehicle.atmosphere` `dir == "exited"` |
| `first_orbit` | Around We Go | You made orbit. | first `vehicle.orbit` `phase == "achieved"` |
| `first_landing` | Wheels Down | You put something down and it survived. | first `vehicle.landed` `survived` |
| `first_recovery` | Home Again | You recovered a vehicle. | first `flight.ended` `reason == "recovered"` |
| `first_eva` | Outside | A kitten went out. | first `kitten.eva_start` |
| `first_dock` | Well Met | Two of your vehicles became one. | first `vehicle.docked` |
| `first_rud` | It Happens | You lost a vehicle. Everyone does. | first `vehicle.rud` |

### Group `flight`

| key | Title | Shape | Predicate |
|---|---|---|---|
| `crewed_orbit` | Passengers | composite | `vehicle.orbit achieved` on a flight that **began** with `flight_state.crew >= 1` |
| `orbit_and_back` | Round Trip | composite | `flight.ended reason=recovered` on a flight with `milestones & MilestoneOrbit` |
| `docked_after_orbit` | Rendezvous | composite | `vehicle.docked` after that flight had previously set `MilestoneOrbit`; does not claim the docking itself occurred in orbit |
| `coaster` | Along For The Ride | composite | `vehicle.soi` on a flight whose `flight_state.engine_count` is known and equals 0 |
| `heavy_lifter` | Heavy Lifter | threshold | `heaviest_to_orbit >= 20 000` |
| `big_stack` | Tall Order | threshold | `biggest_stack >= 5` |
| `many_parts` | Kit Bash | threshold | `most_parts >= 100` |
| `well_lit` | Well Lit | threshold | `engine_ignitions >= 100` |

`coaster` is the "get to Jupiter with no engines" badge, generalised to any SOI arrival so it is not
a body allow-list. `reached_<body>` (below) is what says *where*.

### Group `survival`

| key | Title | Shape | Predicate |
|---|---|---|---|
| `lithobraker` | Lithobraker | threshold | `biggest_lithobrake_survived >= 50` |
| `ground_truth` | Ground Truth (tier 2) | threshold | `biggest_lithobrake_survived >= 100` |
| `pressed` | Pressed | threshold | `peak_g_survived >= 10` |
| `feather` | Feather | threshold, **below** | `0 < softest_landing <= 0.5` |
| `canyon_run` | Canyon Run | threshold, **below** | `0 < lowest_pass <= 100` |
| `old_hand` | Old Hand | threshold | `landings >= 25` |

The two **below** rows are why `thresholdBadge` needs a `Below bool`: an ascending board is satisfied
by being *small*, and both must also refuse `0` — PROJ-088, because a zero from an unread value would
be an unbeatable record and would hand the badge to everyone.

### Group `exploration`

| key | Title | Shape | Predicate |
|---|---|---|---|
| `wanderer` | Wanderer (tier 1) | threshold | `soi_bodies >= 3` |
| `voyager` | Voyager (tier 2) | threshold | `soi_bodies >= 5` |
| `grand_tour` | Grand Tour (tier 3) | threshold | `soi_bodies >= 8` |
| `groundskeeper` | Groundskeeper | threshold | `landed_bodies >= 3` |
| `been_to_every_planet` | Every World | **subset** (Task F7) | every `kind == "planet"` body in this save's effectively complete system has been entered |
| `been_to_everything` | Nothing Left | **subset** (Task F7) | the same for every body of every class |
| `reached_<body>` | `"Reached " + titleize(body)` | **family** | `vehicle.soi` `to_body` |
| `orbited_<body>` | `"Orbited " + titleize(body)` | **family** | `vehicle.orbit` `phase == "achieved"`, `body` |
| `landed_on_<body>` | `"Landed on " + titleize(body)` | **family** | `vehicle.landed` `survived`, `body` |

**The tiers and the everywhere badges are different questions and both ship** (Task F5). A higher tier
never removes a lower one and the everywhere badge never removes a tier; they are all held at once.
**Every one of these is labelled with its system** on every surface, because "Every World" means
something different in each (§3.6).

### Group `kittens`

| key | Title | Shape | Predicate |
|---|---|---|---|
| `not_on_their_feet` | Not On Their Feet | event | first `kitten.tumble` `from == "airborne"` |
| `persistently_upside_down` | Persistently Upside Down | threshold | `kitten_tumbles >= 50` |
| `crowded_capsule` | Crowded Capsule | threshold | `biggest_recovery >= 4` |
| `spacewalker` | Spacewalker | threshold | `evas >= 10` |
| `the_long_walk` | The Long Walk | threshold | `longest_eva >= 3600` |
| `ferry_service` | Ferry Service | threshold | `kittens_to_orbit_and_back >= 10` |

**Blurbs are the site's voice, not the code's** (`docs/ui-design.md` §9.2): dry, understated,
affectionate about failure, **British spelling**, sentence case, **no exclamation marks, no emoji**,
and never a number played for a joke.

---

## Appendix C — the six starter challenges, in full

Every challenge sets an explicit UTC half-open `[Opens, Closes)` window in its own Go literal. The
fold compares those epochs with `ev.RecvTime`, the catlogd server's receive timestamp assigned when
the event arrived—never client `wall_t`, simulation time or `time.Now()` during replay. There is no
shared calendar and no owner gate on dates: setting/changing the literal is part of defining that
challenge. The constants below are a fixture/example of the required representation, with human UTC
dates in comments so a reader can check the milliseconds:

```go
const (
	// 2026-08-10T00:00:00Z .. 2026-08-17T00:00:00Z
	week33Opens  int64 = 1_786_320_000_000
	week33Closes int64 = 1_786_924_800_000
)
```

| key | Title | Kind | Scope | Unit | Asc | The rule |
|---|---|---|---|---|---|---|
| `heavy_lift_week` | Heavy Lift Week | record | **system** | `kg` | no | The heaviest payload to orbit over that system's home body — whole-vehicle mass at achievement |
| `speedrun_orbit` | From Scratch To Orbit | best | **career** | `ms` | **yes** | The shortest career time at which a save reached orbit |
| `tumbleweek` | Tumbleweek | count | player | `tumbles` | no | The most kitten tumbles |
| `coasting_class` | Coasting Class | record | **system** | `bodies` | no | The most distinct worlds reached in-window on flights that launched with no engine installed |
| `feather_touch` | Feather Touch | best | **system** | `m/s` | **yes** | The gentlest surviving landing away from that system's home body |
| `full_house` | Full House | record | player | `kittens` | no | The most kittens brought home in one piece at once |

A worked example, complete enough to copy:

```go
// heavy_lift_week — the heaviest thing anybody puts in orbit this week.
//
// "Payload" here is the loose sense players use: the whole vehicle's mass at the
// instant orbit was achieved, propellant included. KSA has no payload concept and
// catlog cannot separate cargo from booster — the per-stage data that would allow
// it is refreshed in flight only while one of two specific windows is open — so
// the blurb states the definition rather than the word implying a precision that
// does not exist.
{
	Key: "heavy_lift_week", Title: "Heavy Lift Week",
	Blurb: "Get the heaviest payload you can into orbit. The number is what the whole " +
		"vehicle weighed the moment it got there, propellant included — catlog cannot " +
		"tell the cargo from the rocket, and does not try.",
	Opens: week33Opens, Closes: week33Closes,
	Unit: "kg", Ascending: false, Scope: ScopeSystem,
}
```

```go
// its fold
challengeFold{
	c: mustChallenge("heavy_lift_week"), kind: kindRecord,
	value: func(ctx context.Context, b *Batch, ev Event) (float64, map[string]any, bool, error) {
		p, ok := payloadOf[VehicleOrbit](ev)
		if !ok || p.Phase != "achieved" || p.MassKg <= 0 {
			return 0, nil, false, nil   // mass_kg is written as 0 when the read failed — PROJ-094
		}
		ok, err := scoreable(ctx, ev, b)
		if err != nil || !ok {
			return 0, nil, false, err
		}
		home, known, err := b.CareerHomeBody(ctx, ev.PlayerID, ev.Career)
		if err != nil || !known || p.Body != home {
			return 0, nil, false, err
		}
		return p.MassKg, map[string]any{
			"body":   p.Body,
			"flight": ids.String(ev.FlightID),
		}, true, nil
	},
}
```

`speedrun_orbit` is the career-scoped one and exists to prove that path: it is `kindBest` on
`careerMillis(ev.SimTime)`, gated on `ev.HasCareer() && ev.HasSimTime`, `Scope: ScopeCareer`, so its
rows carry `save` and `save_id`. Its blurb has to say what it rewards without prescribing it:
*"Start a save and get to orbit. The clock is the game clock, counted from the beginning of that
save."*

`coasting_class` is the one that needs `flight_state`: require
`engine_count != nil && *engine_count == 0`, then insert the qualifying canonical `(system,body)`
member into `challenge_member` and count that read-through set. It must not use `career_body` or a Go
set held in the fold.

---

## Appendix D — surveyed, readable, and deliberately not built now

Everything below was verified against the decompiled build `2026.8.5.5168` while this plan was
written. **None of it is in scope.** It is recorded so the next survey is a lookup rather than a
re-derivation, and so nobody has to argue about what KSA does and does not expose.

### Readable with no new Harmony patch — a poll away

| Read | Symbol | What it would be good for |
|---|---|---|
| Whole-vehicle fuel fraction | `Vehicle.Parts.TotalFillFraction` | One float 0..1, recomputed every physics step for every vehicle. **The cheapest unread value in the game.** "Least fuel left at orbit insertion" |
| Dry / propellant mass | `Vehicle.InertMass`, `Vehicle.PropellantMass` | `heaviest_dry_to_orbit`. Note PROJ-099 governs propellant: *record it, never validate it*, and that rule has to be written down before it is recorded |
| Vehicle dimensions | `Vehicle.BoundingSphereRadiusBody`, `BoundingBoxHalfExtentsAsmb` | "Tallest rocket to orbit". Already computed — use the accessors, not `PartTree.ComputeBoundingBoxAsmb()` |
| Structural-load **fraction** | `StructuralLoad.GLoadFraction`, `DynamicPressureFraction` | "Closest call" — how near the limit you got, where the limit is the game's own and varies with vehicle size. Strictly better than absolute `peak_g`, and the game even ships the alert thresholds |
| Vehicle region | `Vehicle.VehicleRegion` (`Surface`/`LowOrbit`/`HighOrbit`) | A free, game-blessed altitude classification, serialized into the save |
| Part-module inventory | `Modules.HasAny<T>()`, `PartTree.{DockingPorts,Decouplers,SolarPanels,Batteries,Tanks}` | "Most docking ports on one stack" |
| Battery charge | `BatteryState.Charge` | "Survived the eclipse" |
| Δv actually expended | `KinematicMeasurements.DeltaVelocityCci`, integrated | The **honest** Δv metric, unlike `NavBallData.DeltaV` |
| Target / rendezvous | `Vehicle.Target`, `TargetPart` | "Closest approach" |
| Control point | `Vehicle.ControlPart`, `Ctrl2Body` | New in 5168 and **mandatory** to normalise any attitude metric, since the player can move the control frame |
| Recoverability | `Vehicle.CanRecover` | One line for "is this a recoverable landing", which catlog re-derives today |

### Celestial and system data, readable, not shipped

Surveyed while designing Phase C. All verified at build 2026.8.5.5168.

| Read | Symbol | What it would be for |
|---|---|---|
| **Ring geometry** | `body.BodyTemplate.RingsReference` — `InnerRadius`, `OuterRadius` (metres), `Inclination`, `LongitudeOfAscendingNode` | Stock: **Saturn only**. Four numbers, and the single highest visual payoff item for a 3D view |
| **Named surface locations** | `CelestialTemplate.Locations` — `City` / `Landmark` / `Crater` / `Mountain`, each with latitude and longitude | Earth alone ships ~40. Free labels on a globe |
| **Atmosphere profile** | `PhysicalAtmosphereReference.SeaLevelPressure` / `SeaLevelDensity` / `ScaleHeight` | Enough to render an exponential atmosphere shell client-side |
| **Body and star colours** | `CelestialTemplate.ColorRgb`, `StellarBodyTemplate.ColorRgb` / `LightColorRgb`, `Celestial.OrbitColor`, `SoiColor` | The game's own palette, so a rendered view matches |
| **Terrain height envelope** | `Astronomical.MaxTerrainRadius` / `MinTerrainRadius` (authored, safe) | Relief. **Never** the `…Approx` variants — they sample 16,384 points across `Environment.ProcessorCount` threads and are machine-dependent |
| **Galactic plane** | `CelestialSystem.GalacticPlane` (`float4x4`) | Star-field orientation; would make a rendered sky match the game's |
| **The system id in a save** | `universeData.CelestialSystems[0].Id.Id`, readable in a `Universe.DeserializeSave` prefix; the game itself ignores it | Would let the mod detect "this save was made in a different system" before loading |

### Real Harmony patch points, unused

| Target | Why it is interesting |
|---|---|
| `Decoupler.Decouple(Vehicle)` | Public instance, game thread, returns the new `Vehicle`. Finer-grained than `vehicle.staging` — a piece **actually came off**, and the child's `Parts.Count` is how big it was. The closest thing KSA has to "parts jettisoned" |
| `KittenEva.UpdateFromTaskResults(...)` | Game-thread, `public override`, where `_locomotionState` is assigned. Would give exact tumble/jump/airborne timings instead of a 2 Hz poll. **Do not patch `KittenLocomotion.Advance`/`DeriveMode` — they run on a worker thread** |
| `CelestialSystem.Rename(Vehicle, string)` | Deregister → rename → register. This is the known "renaming a vehicle mid-flight splits the flight in two" limitation in `docs/ROADMAP.md`, and this is where a fix would go |

### Confirmed absences — do not go looking again

| Topic | Finding |
|---|---|
| Per-part destruction | **Does not exist.** No `DestroyPart`, no `OnPartDestroyed`, no `Explo*`/`Sever*`/`Shatter*`/`Fracture*`. Destruction is one whole-vehicle test |
| Science | **Does not exist.** Zero hits for `science` or `experiment` anywhere |
| Achievements / contracts / milestones | **Do not exist** in the game |
| Comms / antenna / signal | **Do not exist** |
| Part temperature / reentry heating | **No thermal simulation.** The only part-path `Temperature` is an FX value driven by frost and emissivity timings |
| Aerodynamic detail | Vehicle aero is **one number**: dynamic pressure. No angle of attack, no drag coefficient, no lift |
| Payload vs booster | **No payload concept.** `SequencePerformance` has the split and is UI-gated (see Task J3). catlog uses "payload" in the loose sense — total vehicle mass — and says so wherever it does |
| Barycentres | **The concept does not exist.** No `Barycent*` symbol anywhere. Pluto orbits the star and Charon orbits Pluto |
| A system or body version field | **Does not exist.** `SystemTemplate` has `Id`, `GalacticPlane` and `Bodies`; there is no version, revision, checksum or date. This is why Phase C hashes the content |
| A body display name | **Does not exist.** The `Id` *is* the name |
| More than one system loaded at once | **No.** `UniverseData.CelestialSystems` is plural but serialisation writes exactly one and deserialisation reads index 0 |
| Δv as a vehicle statistic | `NavBallData.DeltaV` is the **active stage only** and is refreshed only while one of two windows is open |
| EVA distance walked | No EVA-only counter. `KittenRosterEntryData.TravelledMeters` is credited for **every** vehicle a kitten rides, in the **ecliptic** frame — dominated by heliocentric motion. Never present it as distance walked |

---

## Appendix E — checklist: adding an event type or a payload field

Derived from a full survey of the mod. **Follow it in order.** Steps marked ★ are the ones whose
omission fails silently rather than loudly.

### A new payload field on an existing type

**Greenfield branch for this plan:** edit the final `ver: 1` shape in place. Do not add version
bumps, upcasters, compatibility fixtures or previous-shape tests. The normal post-launch contract
rule applies only after real data exists.

1. `mod/catlog.lib/Events/EventTypes.cs` — keep `Versions[type] == 1` and pin it in tests.
2. `mod/catlog.lib/Events/Payloads.cs` — add the member. Optional fields need **both** `T?` **and**
   `[property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]` ★ — the shared
   `JsonSerializerOptions` deliberately has no global null-ignore, so nullability alone does nothing.
3. `mod/catlog.lib/Events/GameSignal.cs` — the signal record, for an event-driven type.
4. `mod/catlog.lib/Telemetry/TelemetrySnapshot.cs` — an **init-only property**, never a positional
   parameter, for a polled type.
5. `mod/catlog.lib/Detect/{EventPipeline,EventDetector,WindowAccumulator}.cs` — pass it through.
6. `mod/catlog/VehicleTelemetry.cs` — the KSA read, `try`/`catch`, with a `[KsaAnchor]` naming
   `Member`, `SourceFile` (`file:line`), `Verified`, `GameVersion` and `Risk`.
7. `mod/catlog/{Patcher,PolledSignals}.cs` — fill it at the patch point or the poll.
8. `mod/catlog.lib.tests/TestData.cs` — a **defaulted** parameter, so no existing call site breaks.
9. `mod/catlog.lib.tests/Events/EventTypesTests.cs` — the `ver` assertion.
10. `mod/catlog.sim/SimVehicle.cs` — set it in `Sample`, if a scenario should exercise it. Note
    `RadarAltM`, `Lat`, `Lon`, `VerticalSpeedMs`, `HorizontalSpeedMs` and `WarpFactor` are **not** set
    there today, so scenarios never exercise them.
11. `server/internal/projector/upcast.go` — **no change in this plan**; `currentVer` stays 1 and the
    upcaster registry stays empty.
12. `server/internal/stats/payload.go` — the Go struct field and its `json:` tag.
13. `server/internal/testvectors/testvectors.go` + `make testvectors` — and for an **optional** field,
    carry it **present on one line and absent on another** ★, which is what makes
    `Batch001_PayloadsRoundTripThroughTheirRecords` prove anything.
14. Docs: `docs/events.md`, `docs/event-details.md`, `docs-site/src/data/events.ts`, the family page,
    `docs/DECISIONS.md`.

### A new event type — everything above, plus

15. `EventTypes.cs` — the `public const string`, the `Versions` entry, and `AlwaysReported` only when
    omission defeats an exclusion **or a recorded decision makes the event mandatory attribution**.
    This plan adds only `system.discovered` under the second rule; `system.body` stays configurable.
16. `EventTypes.KindOf` — a new type is `KindEvent` and therefore **never pruned from the outbox**.
    Only `telemetry.window` is `KindPassive`. Consider the outbox pressure before adding a chatty
    type.
17. `mod/catlog.lib/Config/ModConfig.cs` — a line in the commented `[events]` block in the `Header`
    const, with the boards it feeds, and `# locked on` iff it is always-reported. ★ **Test-enforced**
    by `TheHeaderDocumentsEveryRegisteredEventType`.
18. `mod/catlog.lib.tests/Config/EventGateTests.cs` — `AlwaysReported` changes five → six: update
    the exact locked-name set and its header sentence together; only `system.discovered` is added.
19. `mod/catlog.lib.tests/Conformance/ContractVectorTests.cs` — the `PayloadTypes` row ★, or
    `Batch001_PayloadsRoundTripThroughTheirRecords` throws `KeyNotFoundException`.
20. `EventTypesTests.RegistryHasExactlyTheLaunchSet` — the count (23 today).
21. ★ `server/internal/ingest/types.go` — `knownTypes`. **Without it the whole batch is rejected
    `400 malformed_batch`.**
22. `server/internal/stats/payload.go` — a `case` in `decodePayload`.
23. `docs/event-details.md` — a new `##` section with all eight blocks, **and** a row in the registry
    table whose header currently says "23 names" (this plan takes it to **25**).

**Line budget:** `Wire.MaxEventLineBytes` is 16 KiB and a single line over the cap wedges the outbox
behind it. `Wire.MaxEventsPerBatch` is 2000.

---

## Appendix F — checklist: adding a board

0. **Give every new fold a stable unique name.** In this greenfield plan, changed formulas define the
   fresh final projection and do not bump `stats.BuildVersion`. After launch, the normal `PROJ-102`
   rule applies to meaning changes under an unchanged name.
1. `server/internal/stats/boards.go` — a `StatXxx` constant, a `Board` row in `fixedBoards`
   (**append**, so existing display order is untouched), and the fold type.
2. `server/internal/stats/fold.go` — one entry in `BoardFolds()`, in board-metadata order.
   **Order is part of `BuildID`**, so moving an existing entry is a real change, not a tidy-up.
3. Choose the write helper: `putRecord` (max) · `putBest` (min) · `addCount` (counter) · `setValue` +
   `setCareerValue` + `setSystemValue` (derived total). **Do not write to the batch directly** — the
   first three helpers are what give a board its rolling windows and **both** extra scopes for free
   (`putScoped`). The derived-total trio is deliberately three explicit calls, because a per-save and
   a per-system total are different queries and mirroring one into the other writes a number under a
   label that claims something else.
4. Gate it: `scoreable` for anything flight-bearing, plus a `> 0` value gate on any reading that
   decodes to `0` when the mod could not read it (PROJ-088, PROJ-094).
5. Tests in `stats/`: golden value + `updated_seq` + **byte-exact `Context`**; the tie rule; the flag
   exclusion; rebuild-equals-incremental; a `Describe` metadata assertion; and — if the board is
   derived from a **body name** — that it is marked `body_derived` so the site puts the merge-systems
   note on it (Task B1.1).
6. `docs/event-details.md`: the Boards table row, a **Fold detail** entry, and a
   **Suppression and eligibility matrix** row per gate.
7. `docs-site/src/data/boards.ts` and a `<BoardDetail>` in `leaderboards/catalog.mdx`. ★
   `BoardDetail.astro` **throws** on an unknown stat, so a board in a page but not in the data module
   is a build failure — which is the point.
8. `docs/DECISIONS.md`.
9. **Units:** nothing to do unless the unit needs scaling or duration treatment. An unrecognised unit
   already renders as `three-sig-figs + " " + unit`. If `units.go` changes, the row in
   `units.Conformance` changes **in the same commit**.
10. **Never assert a board count** in a test or a spec (PROJ-039).
11. **If the board needs a new projection table**: construct the final fresh schema in the next
    migration and add the table to **all three**
    hand-maintained lists in §5.4 (`projector_test.go`'s `snapshot`, `store_test.go`'s expected DDL,
    and `store/projections.go`'s `Counts`). The first is silent when forgotten.

---

## Appendix G — sequencing, and what can run in parallel

```
A1–A6 ──> C1–C4 ──> A7–A9 ──> B
                │
                └──> D ──> E ──> F ──> G
                       │          └──────┐
                       └──> H ──> I      │
                                        ▼
                              everything ──> J
```

- **All owner gates are resolved in the contract above.** Implement exact system identity, a forest,
  required orientation, §3.7's planet mapping, configurable `system.body`, system-scoped comparison
  challenges and rebuild-authoritative badges. Challenge definitions carry explicit UTC epochs.
  Do not reintroduce the rejected alternatives in implementation tasks.
- **The document's first executable slice is A1–A6 → C1–C4 → A7–A9 → B.** A7/B name system
  store/read types that C creates; running page order would not compile. A8's final ordering test
  likewise requires C's event/fold.
- **C then D are one serial mod/contract stream.** Both touch event registries, config header,
  payloads and vectors. All changed/new types remain v1; each contract task is a vertical commit.
- **E needs A, C and D.** Its derived scopes need system binding and its fields/flight facts come
  from D.
- **F needs A + C + D + E.** Everywhere needs the catalogue/normalised kind; coaster needs engine
  facts; Ferry Service needs E's board. **H needs A + C + D** for scopes, dynamic home bodies and
  engine facts. F and H may run in parallel only after those prerequisites.
- **G needs F and the B/C surfaces; I needs H and the B/C surfaces.**
- **B, G and I all edit `layout.gohtml`, `templates.go`, `web.go` and `docs/ui-design.md`.** Run them
  in series, or expect conflicts in exactly those four files.
- **J is last and is an audit/release gate.** It does not supply deferred docs or decisions.

**Before every phase:** rebase on `main`, re-read §5, and re-check
`ls server/internal/store/migrations/projections/` for the next free migration number.

**One vertical commit per task** is the right granularity, each with its docs/decision in it. A task that
touches an event, a payload field, a fold, a board, an eligibility rule or a unit and updates only
`docs/event-details.md` **or** only `docs-site/` is an incomplete change and should not be committed.
