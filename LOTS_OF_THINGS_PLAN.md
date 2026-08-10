# LOTS_OF_THINGS_PLAN.md

**Per-save leaderboards, merit badges, weekly challenges, and the boards the community asked for.**

Status: **plan only — nothing here is implemented.** Written 2026-08-09.
Author: planning session. Audience: the implementing agents who will execute the phases below.

---

## 0. How to use this document

This plan is written to be executed by coding agents, one task at a time, in order. Every task
carries: the files it touches, the code it should write (or a close sketch of it), the tests it must
add, the documents it must update in the **same commit**, and an acceptance check.

**Read these first, once, before starting any task:**

| Read | Why |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | The documentation constitution. It is not advisory. |
| [`docs/CONSTITUTION.md`](docs/CONSTITUTION.md) | §5 (rebuildable), §6 (derived never claimed), §8 (anti-cheat cap), §9.1 (the two-halves doc rule). |
| [`docs/event-details.md`](docs/event-details.md) §§ *Projections*, *Boards*, *Rebuild ≠ incremental* | How a fold actually works today. |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) `PROJ-024`, `PROJ-026`, `PROJ-033`–`PROJ-037`, `PROJ-042`–`PROJ-047`, `PROJ-049`, `PROJ-088`, `PROJ-096`, `PROJ-099` | Every one of these constrains something below. |

**Non-negotiables that apply to every task in this plan.** A task that breaks one of these is not
done, however green the tests are:

1. **`make test` stays green.** Every task adds tests for what it changed.
2. **Rebuild == incremental.** Any new fold derives its bucket/window/scope from `ev.RecvTime`,
   `ev.Seq`, `ev.Career` or the payload — **never** from `time.Now()`, never from Go map iteration
   order, never from anything outside the event. `projector.TestRebuildEqualsIncrementalForAnUnflaggedHistory`
   must be extended to cover every new table (see Task A8).
3. **Docs move in the same commit.** `docs/event-details.md` **and** `docs-site/` for anything that
   touches an event, a payload field, a fold, a board, an eligibility rule or a unit. Plus a dated
   `docs/DECISIONS.md` entry in the right area with the next free number, saying **why**.
4. **Never mint a new `§` number.** New material gets a document and a heading.
5. **A contract change carries a version bump.** Event/payload → that event's `ver`. Endpoint shape →
   its `ver`. Credential file → `format`. **And a fold whose *meaning* changed without its name
   changing → `stats.BuildVersion`** — see §5.1, which explains why and names the one task in this
   plan that could owe one.
6. **Projections migrations are additive only.** No `DROP TABLE`, no `DROP COLUMN`, no table
   recreation under `migrations/projections/` — the live file is migrated in place while it is still
   serving reads. See §5.2. This one is new since the shadow-ban work and it already forced a
   redesign of Task A5.
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

Four things, in dependency order:

| # | Thing | Shape | Phases |
|---|---|---|---|
| 1 | **Per-save (career) leaderboards** | A second scope on every existing board, plus 3 career-native boards. Ranks `(player, save)` pairs. | A, B |
| 2 | **New boards from the community ideas** | Mostly free from data already on the wire; three new payload fields. | C, D |
| 3 | **Merit badges** | A permanent, once-only award. Two independent projections: lifetime and per-save. | E, F |
| 4 | **Weekly challenges** | A curated rule over a named, explicitly-dated window. | G, H |

And a documentation sweep that closes all of it (Phase I).

### 2.1 The community ideas, and what each one actually needs

Verbatim from the request, with a verdict. **Six of the nine need no new wire data at all.**

| # | Community idea | Verdict | Where it lands |
|---|---|---|---|
| 1 | "Biggest lithobrake record (fastest impact where science/kitten survived)" | **Already exists** — `biggest_lithobrake_survived` requires `survived && crew_count ≥ 1 && !launch_pad`. It is board #1 in the catalog. The gap is *discoverability*, not data. | Phase F/H surfacing; new career scope in Phase A |
| 2 | "Most times a kitten did NOT land on their feet" | **Already exists** — `kitten_tumbles`, folded from `kitten.tumble`. New: a `tumbles_on_<body>` dynamic family (the payload already carries `body`) and a "clumsiest kitten" per-kitten record. | Task D1, D2 |
| 3 | "Most kittens to orbit and back" | **Derivable, no wire change.** Needs a `milestones` bitfield on `flight_state` (did this flight reach orbit) + the `kids` array already on `flight.ended`. | Task C3, D3 |
| 4 | "Most parts exploded/destroyed" | **Needs one new payload field** — `vehicle.rud.part_count`. Parts at destruction are not derivable from `flight.started.part_count` because staging changes it. | Task C1, D4 |
| 5 | "Get to Jupiter with no engines" | **Needs one new payload field** — `flight.started.engine_count` — then it is a badge/challenge over `vehicle.soi` joined to `flight_state`. | Task C2, E5, G4 |
| 6 | "Most SOIs within 10 years" | **Free.** `vehicle.soi` + envelope `sim_t`, career-scoped. A "year" is `units`' flat 365-day year — catlog must not learn a body's orbital period. | Task D5 |
| 7 | "Most kittens KIA in a single crash" | **Free, with an honesty caveat.** `vehicle.rud.crew_count` already ships. Per D11 the crew *survive* a physics RUD, so the honest board is "most kittens aboard a vehicle that was lost", not "killed". | Task D6 |
| 8 | "Personal achievements to unlock — 'managed to get into orbit'" | **This is the badge system.** | Phases E, F |
| 9 | "Visited every planet in the solar system" | **Refused as literally stated, delivered as tiers.** catlog has no list of what "every" is, by design (PROJ-033). Delivered as `soi_bodies ≥ N` tiered badges. | §3.7, Task E4 |

---

## 3. Design decisions, with the reasoning

Every subsection here becomes a dated `docs/DECISIONS.md` entry in Phase I (numbers are allocated in
Task I1). They are written out in full because the *why* is the part that stops them being
re-litigated, and because an implementing agent that does not understand the why will implement the
wrong thing.

### 3.1 A career is a **scope** — a second dimension of a board, not a new set of boards

`GET /v1/leaderboards` stays **one row per board**. Each row gains
`scopes: ["player","career"]`. `?scope=career` on a board URL selects the per-save ranking;
an absent `?scope=` means `player` and every existing URL, cache entry and assertion is
byte-identical.

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

### 3.6 A badge is a permanent, once-only, timestamped award, and there are two independent scopes

One table, `badge_award`, with `career = ''` meaning **the lifetime (player) award** and
`career = '<id>'` meaning **the per-save award**. The same badge is awarded independently in both
scopes.

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

*Why once-only:* a badge is a *milestone*, and its interesting property is **when you first got it**.
`INSERT … ON CONFLICT DO NOTHING` gives that for free and is replay-stable: a rebuild replays the
same seqs in the same order and therefore awards at the same event.

### 3.7 There is no "visited every planet" badge, and there must not be one

The community asked for it. The honest catlog answer is a **tier ladder on the count of distinct
bodies**: `wanderer` (3 bodies), `voyager` (5), `grand_tour` (8), and so on.

*Why:* PROJ-033 deleted the celestial-body allow-list on purpose. KSA's celestial systems are
hand-authored content that ships as data and that mods extend or replace, so the server holds no
list of bodies at all — `fastest_to_<body>` boards come into existence *because a body appeared in
the event stream*. A badge for "every planet" requires the server to know what "every" is, which
means re-introducing the exact list PROJ-033 removed, and it would be **wrong for every player
running a system mod** and stale the day KSA ships a new body. A count threshold is checkable, is
stable across content changes, and says something true.

**This is the most likely mistake in this plan.** If a task tempts you to write
`var planets = []string{"mercury", "venus", …}`, stop: you are re-adding `TimedBodies`, which was
already tried and already deleted (PROJ-025 → PROJ-033).

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

### 3.10 Three new payload fields, and no more

`flight.started` → **`ver: 2`**, gains `engine_count: i`.
`vehicle.rud` → **`ver: 2`**, gains `part_count: i`.

Everything else the community asked for is derived on the server from data already shipping.

*Why so few:* Constitution §3 — the mod is a guest in someone else's game, and every field is
bandwidth, outbox rows and game-thread reads that a player did not ask to pay for. The discipline
applied at every candidate field was: *can the server derive this by joining `flight_state`?* If yes,
it is not a wire change. That is why `vehicle.soi` does **not** gain `engine_count` (join the flight)
and why `vehicle.orbit` does **not** gain `kids` (the flight's crew at recovery is already on
`flight.ended`, and `flight_state` can carry "this flight reached orbit" in one integer column).

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

*Why not add `career` to the existing primary keys:* **because projections migrations are now
additive-only** (§5.2, `PROJ-101`). The live `projections.db` is migrated in place at open so its
boards keep answering reads while the rebuild runs, and SQLite cannot widen a primary key without
recreating the table — which would damage the file that is still serving. That rules out the obvious
design.

*Why the result is better anyway,* and would have been the right answer even without the constraint:

- **No published number moves.** `soi_bodies`, `landed_bodies`, `distance_travelled`,
  `top_kitten_distance` and `top_kitten_missions` all read the tables they read today, unchanged, so
  there is nothing to re-explain to a player and **no `stats.BuildVersion` bump owed** (§5.1).
- **No sentinel.** An earlier draft used `career = ''` rows inside a widened `player_body` to keep the
  lifetime novelty count honest. That works, but it makes one table mean two things and makes every
  future reader ask which rows they are counting. Two tables, two questions, no ambiguity.
- **The novelty signals stay separate by construction.** `soi_bodies` increments from `AddBody`'s
  row-novelty report against `player_body`; the per-save board increments from the equivalent report
  against `career_body`. Neither can double-count the other, because they are different rows in
  different tables.

*The cost, stated:* a body reached in two saves stores three rows rather than two, and a kitten who
appears in two saves stores three rather than one. That is a projection, it is bounded by
players × saves × bodies, and it is the price of the additive constraint.

### 3.13 `kid` is not save-scoped, and the lifetime `distance_travelled` quirk is documented, not fixed

`kid` is `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` — it contains **no career**.
KSA's roster lives in `UniverseData`, which is the save, so a kitten called Mittens in save A and a
kitten called Mittens in save B are two different cats that collapse to one `kid`. The lifetime
`kitten` table merges them under `max()`, so `distance_travelled` reports the *furthest that name ever
got in any one save* rather than the total across saves.

**The per-save boards are correct** — `career_kitten` splits them — and the lifetime board keeps the
behaviour it has always had. Fixing the lifetime number is a one-line change once `career_kitten`
exists (sum over it instead), but it moves a published number and therefore owes a `BuildVersion`
bump, so it is **optional sub-task A5.4** and the owner's call rather than something this plan
smuggles in. Either way the quirk gets written down in `docs/event-details.md`, because right now it
is behaviour no document states.

### 3.14 Badge families reuse `[boards] min_players`, and challenges do not need it

A dynamic badge family member (`orbited_<body>`) is listed in the public badge index once
`min_players` distinct players hold it. Fixed badges are always listed. Challenge keys are curated
and always listed.

*Why:* PROJ-034's argument transfers unchanged — a badge for a place a single modified client
invented should not be able to fill the public index, and one holder is not a community achievement.
Reusing the existing config key rather than adding `[badges] min_players` is deliberate: it is one
threshold answering one question ("is this a thing more than one person has done"), and two knobs
that always get set to the same value are one knob with extra steps.

---

## 4. Constitution check

Written out because Constitution §8 is the principle most likely to be quietly violated by a feature
that sounds like fun, and because §9 requires the reasoning to exist before the code does.

| Principle | This plan |
|---|---|
| **§1** no email, handle is the only identity | Untouched. New surfaces publish handle + save ordinal + relabelled save id. §3.5. |
| **§2** cheap enough to forget about | Five new tables (`career_stat`, `career_body`, `career_kitten`, `badge_award`, `challenge_stat`), all bounded: players × saves × boards, players × saves × bodies, players × saves × kittens, players × (saves+1) × badges, players × challenges. **Career scope is explicitly denied a period dimension** (§3.3) precisely to keep the multiplicative one from existing. No new process, no new job, no new service. |
| **§3** the mod is a guest | **Three new payload fields, total** — two ints and one string. All three are read at a patch point or a poll the mod is already inside; one of them (`kitten.tumble.from`) is a value the poller already holds and throws away. No new Harmony patch, no new sampling. Everything else is derived server-side. §3.10. |
| **§4** everything runs locally | No new external anything. Challenges are compile-time; badges are folds; seed data covers all three (Tasks B7, F5, H5). |
| **§5** the log is immutable, everything else rebuilds | Every new table is a projection, rebuilt from seq 0. `TestRebuildEqualsIncremental…` is extended to all of them (Task A8) rather than left covering the old ones only. Adding a fold now also changes `stats.BuildID`, so a deploy suspends the fold loop and rebuilds itself (§5.1) — the new boards fill from history with no operator step. |
| **§6** every number is derived, never claimed | Badge keys and challenge keys are compile-time constants; dynamic badge families use the same `statSuffix` protocol-hygiene rule as boards (PROJ-037). Nothing on the wire is a badge, a challenge score or a rank. |
| **§7** moderation is trivial and total | Every new table is `player_id`-keyed and rebuilt from the log, so a purge and a shadow ban both clear it by the same route `player_stat` uses. `STORE-018` made the exclusion **structural** — a withheld player's events are not in the log at all — so a projection added later inherits it without knowing the feature exists. **Task A9 proves this rather than assuming it.** Note §7 was amended by that work to cover the shadow-ban verb explicitly. |
| **§8** anti-cheat is proportionate | **Nothing in this plan is an integrity check.** No badge, board or challenge infers cheating from data shape. Two places were checked and deliberately left alone: a challenge does **not** exclude a rewound career (the mark still excludes nothing and scores nothing), and no badge is ever revoked. §8 was amended by the shadow-ban work to distinguish shadow-banning-as-moderation (a named human decision, built) from shadow-banning-as-anti-cheat (a machine inference, still forbidden); nothing here is either. |
| **§9 / §9.1** documentation is part of the system | Phase I is not optional and is not a follow-up. Every phase carries its own doc tasks inline; Phase I is the sweep that proves nothing was missed. |

**The one thing this plan refuses:** "visited every planet" as literally stated (§3.7). It is
recorded in `docs/ROADMAP.md` under *Deliberately not built* in Task I3, so nobody re-argues it.

---

## 5. The shadow-ban / rebuild work this plan sits on top of

**That work is finished.** It is staged in the working tree this plan will be applied to, across
~50 files. This section is what it changed *for us* — it is not optional background, because it
introduces one hard constraint that makes an earlier draft of Task A5 illegal, and one mechanism that
makes every phase here better.

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

**Two consequences this plan depends on:**

1. **Every board, badge and challenge added here fills from history by itself.** A new fold changes
   the registry, which changes `BuildID`, which suspends the loop and rebuilds. PROJ-090's "a new
   fold becomes retroactive by rebuild, and no backfill script is written" — now with the server
   noticing rather than a human. A board added by a deploy reads **empty** until the rebuild lands,
   never short-by-history, and that is the point of suspending.
2. **★ A task that changes what an existing fold *means* without renaming it MUST bump
   `stats.BuildVersion` in the same commit.** `BuildID` hashes fold *names*, so added / removed /
   renamed is caught free; a changed threshold, unit, eligibility rule, tie-break or formula is not.
   Same discipline as an event's `ver` (`PROJ-102`). The cost of forgetting is a board quietly short
   of history until the next nightly rebuild.

   **As rewritten, no task in this plan requires a `BuildVersion` bump** — Task A5 was restructured
   precisely so that it does not (§5.2). The one place it becomes owed is **optional sub-task A5.4**,
   which is flagged as such. If you deviate from this plan in a way that moves a number an existing
   fold already produced, bump it.

### 5.2 ★ Projections migrations are now additive-only

> *"The live file is migrated in place at open so its existing boards stay readable while the rebuild
> runs; a destructive migration would damage the file that is still serving."* — `PROJ-101`,
> restated in `docs/server.md`.

**No `DROP TABLE`, no `DROP COLUMN`, no table recreation, in `migrations/projections/`.** Only
`CREATE TABLE`, `CREATE INDEX` and `ALTER TABLE … ADD COLUMN`.

This is a real constraint, not a style note: SQLite cannot widen a primary key in place, so the
obvious way to make an existing table career-aware — recreate it — is now forbidden. **Task A5 was
rewritten around this**, and the result is better than the original: new tables alongside the old
ones, every existing published number untouched, no sentinel-value trickery, and no `BuildVersion`
bump. Every other migration in this plan (A1, C5, E1, G1) was already additive; **verify yours before
writing it.**

Nothing is lost by the constraint. A change that genuinely needs the old shape gone gets it from the
rebuild, which creates a fresh database from every migration.

### 5.3 Rules for every task in this plan

1. **`events.db` migrations `0004` and `0005` are taken.** No task here needs one.
2. **`projections.db`: `0005` is taken.** This plan uses `0006`–`0010`. Run
   `ls server/internal/store/migrations/projections/` before creating one — **verify, do not trust
   this plan**, which was already wrong about this once.
3. **Decision numbers taken:** `PROJ` to 103, `STORE` to 018, `IDENT` to 018, `OPS` to 035. Task I1
   starts at **`PROJ-104`**. `MOD` is at 079, `UI` at 044, `DOCS` at 004.
4. **`server/internal/stats/` is untouched except for the new `build.go`.** Fold registration is
   exactly what this plan assumes: `Folds()`, `StateFolds()`, `SecondPassFolds()`, `BoardFolds()`,
   the four write helpers, `Batch`, every board. Nothing in Phases A–H has to be re-derived.
5. **`store/projections.go` gained one hunk and changed nothing existing.** `StatRow`, `Leaderboard`,
   `LeaderboardPeriod`, `StatAhead`, `StatPlayers`, `PlayerStats`, `RewoundCareers` and `Counts` are
   byte-identical to before. Tasks A7, E6 and G5 append to the end of that file and will not conflict.
6. **These files *were* rewritten — read them before touching them:** `store/events.go`,
   `store/store.go`, `store/identity.go`, `store/archive.go`, `store/directory.go`,
   `projector/projector.go`, `projector/rebuild.go`, `adminapi/projections.go`,
   `adminapi/identity.go`, `config/config.go`, `cmd/catlogd/main.go`, `cmd/catlogctl/projections.go`.
   No task in this plan needs to edit any of them.
7. **Do not restructure `projector.Step`, `projector/rebuild.go` or `projector/job.go`.** New folds
   are *additive*: new files in `stats/`, appended entries in `BoardFolds()` or a new fold-list
   function. The one unavoidable edit to `projector/` is adding the new tables to the
   rebuild-equivalence snapshot (§5.4) — keep it to that.
8. **The rebuild verb changed.** `POST /admin/projections/rebuild` now returns `202` plus a job;
   `{"wait": true}` blocks for scripts; `GET` the same path reports progress;
   `catlogctl rebuild [-detach|-status]` is the CLI. Anything in this plan or in a test that assumed
   a synchronous rebuild needs the new shape (Task I5).
9. **Constitution §7 and §8 were amended** to distinguish shadow-banning-as-moderation (built, a
   human's decision about a named account) from shadow-banning-as-anti-cheat (still forbidden, a
   machine's inference from statistics). Nothing in this plan is either, but §4's compliance table
   was updated to match.
10. **`Rebuild ≠ incremental` gained a sixth divergence** (a shadow ban applied since the last
   rebuild) and a new *"The build stamp"* section in `docs/event-details.md`. Task I2's sweep must
   extend those, not overwrite them.
11. **This work helps ours, three times over.** A durable never-reused `seq` strengthens the
   `updated_seq` tie-break every new projection inherits; a structural shadowban means the new tables
   need no moderation wiring (confirm in Task A9); and `BuildID` means the new boards backfill
   themselves.

### 5.4 ★ Three hand-maintained lists every new projection table must be added to

Nothing in the compiler will remind you. This plan adds **five** tables (`career_stat`,
`career_body`, `career_kitten`, `badge_award`, `challenge_stat`), so each of these is touched five
times.

| List | Where | What happens if you forget |
|---|---|---|
| The expected-DDL fixture | `server/internal/store/store_test.go` — `TestMigrationsCreateTheFullDDL`'s expected projections **table** list *and* its **index** list | A test failure, immediately. This is the friendly one. |
| The rebuild-equivalence snapshot | `server/internal/projector/projector_test.go` — the `snapshot` struct and `rig.snapshot()`'s per-table `dump(...)` calls | **Silent.** `TestRebuildEqualsIncrementalForAnUnflaggedHistory` passes while proving nothing about your table. This is the dangerous one, and it is why Task A8 exists as a task rather than a footnote. |
| The projection census | `server/internal/store/projections.go` — `Counts`, the ten `count(*)` queries behind `RebuildResult` and `GET /admin/stats` | Silent under-reporting in the admin census. Cosmetic, but it is the number an operator uses to confirm a rebuild did what they expected. |

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
2. **Bumping the projections schema version is itself a `BuildID` change.** It is hashed input
   alongside the fold names, so **every migration this plan adds triggers a rebuild on deploy even
   before its folds do.** That is desirable — it is what makes the new boards fill from history —
   but it means there is no such thing as a "migration-only, no rebuild" phase here. Say so in each
   phase's release notes.

---

## Phase A — career scope, server core

**Goal:** every board is rankable per save. No read surface changes yet; this phase ends with
`career_stat` correct, rebuildable, and proven equal under rebuild.

**Before starting:** read §5 in full. This phase is applied **on top of the staged shadow-ban /
rebuild work**, which is where the additive-only migration rule and the `BuildID` mechanism come
from. Confirm that work is committed (or at least staged and stable) before branching, and re-run
`ls server/internal/store/migrations/projections/`.

---

### Task A1 — the `career_stat` table and the career ordinal

**Files:** one new migration.

1. `ls server/internal/store/migrations/projections/` and take the next free number. This task
   uses `0006`; **if it is taken, use the next one and shift every later migration in this plan.**

2. Create `server/internal/store/migrations/projections/0006_career_scope.sql`:

```sql
-- projections.db 0005 — a board, ranked per save.
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
  stat        TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,                    -- JSON, same shape as player_stat.context
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, stat)
);

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
```

**Acceptance:** `make test` green (migrations are applied by every test that opens a projections DB;
a syntax error fails immediately). No behaviour change yet.

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
// sorts to the top on its merits.
//
// A scope is a **dimension of a board, not a board** — the same argument periods
// settled (PROJ-042). `GET /v1/leaderboards` stays one row per board and each row
// publishes the scopes it can be read in; `?scope=` selects one.
const (
	ScopePlayer = "player"
	ScopeCareer = "career"
)

// Scopes returns every value `?scope=` accepts, `player` first.
func Scopes() []string { return []string{ScopePlayer, ScopeCareer} }

// ValidScope reports whether s is a scope the API serves. The empty string is
// `player`, so an absent parameter means what it always meant and every existing
// URL, cache entry and assertion stays byte-identical.
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
// careerStatKey is the primary key of career_stat.
type careerStatKey struct {
	playerID int64
	career   string
	stat     string
}

// putCareerStat records a board write in the career scope.
//
// A no-op when the event carries no career: `sim_t` and every per-save number are
// only meaningful inside one, and a row keyed on "" would be every save at once.
// Stored events written before the `career` key existed decode to "" and are
// simply absent here — the all-time board still has them.
func (b *Batch) putCareerStat(kind statKind, ev Event, stat string, value float64, cx any) {
	if ev.Career == "" {
		return
	}
	// ... same body as putStat, keyed by careerStatKey, into b.careerStats[kind]
}
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
flushFlights, flushCareers, flushBodies, flushKittens, flushStats, flushCareerStats, flushPeriods, flushCensus
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
	b.putCareerStat(kindBest, ev, stat, value, cx)   // NEW
	return periodBest(ctx, b, ev, stat, value, cx)
}

func putRecord(ctx context.Context, b *Batch, ev Event, stat string, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putStat(kindRecord, ev.PlayerID, stat, value, cx, ev.Seq)
	b.putCareerStat(kindRecord, ev, stat, value, cx)  // NEW
	return periodRecord(ctx, b, ev, stat, value, cx)
}

func addCount(ctx context.Context, b *Batch, ev Event, stat string, delta float64) error {
	b.putStat(kindCount, ev.PlayerID, stat, delta, nil, ev.Seq)
	b.putCareerStat(kindCount, ev, stat, delta, nil)  // NEW
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
	b.putCareerStat(kindSet, ev, stat, value, nil)
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

**Files:** a new migration; `server/internal/stats/batch.go`; `server/internal/stats/boards.go`.

This is §3.12 and §3.13. **Read both before starting**, and read §5.2 — this task was rewritten
because the shadow-ban work made projections migrations additive-only, and the earlier draft
(recreate `player_body` and `kitten` with a wider primary key) is now illegal.

**No existing table is altered. No existing number moves. No `stats.BuildVersion` bump is owed.**
`player_body` and `kitten` keep their exact current shape, contents and meaning; the lifetime boards
that read them are untouched. The per-save scope gets two new tables of its own.

**A5.1 — migration `0007_career_sets.sql`.** Purely additive: two `CREATE TABLE`s and two indexes.

```sql
-- projections.db 0007 — the per-save halves of the two set-backed projections.
--
-- ADDITIVE ONLY, and that is a hard constraint rather than a preference: the live
-- projections.db is migrated in place at open so its existing boards keep
-- answering reads while a rebuild runs (PROJ-101), so a migration that dropped or
-- recreated a table would damage the file that is still serving. SQLite cannot
-- widen a primary key in place, so `player_body` and `kitten` cannot learn about
-- careers — they get siblings instead.
--
-- The siblings answer a different question, which is why this is honest rather
-- than a workaround:
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
  kind        TEXT NOT NULL,          -- 'soi' | 'landed' | 'orbit_kid' (Task D3)
  body        TEXT NOT NULL,
  first_seq   INTEGER NOT NULL,
  first_sim_t REAL,                   -- seconds; NULL when the arrival carried no clock
  PRIMARY KEY (player_id, career, kind, body)
);

CREATE TABLE career_kitten (
  player_id      INTEGER NOT NULL,
  career         TEXT NOT NULL,
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
```

**A5.2 — `Batch` methods.** Every existing method keeps its signature and its behaviour. Add a
sibling for each, and a second cache map keyed by `(playerID, career, …)`:

| Existing (unchanged) | New sibling |
|---|---|
| `AddBody(ctx, playerID, kind, body, seq) (bool, error)` | `AddCareerBody(ctx, ev, kind, body) (bool, error)` — no-op returning `false` when `ev.Career == ""` |
| `LowerBodyTime(ctx, playerID, kind, body, t) error` | `LowerCareerBodyTime(ctx, ev, kind, body, t) error` |
| `BodyCount(ctx, playerID, kind) (int64, error)` | `CareerBodyCount(ctx, playerID, career, kind) (int64, error)` |
| `UpsertKitten(ctx, playerID, k, seq) error` | `UpsertCareerKitten(ctx, ev, k) error` — writes nothing when `ev.Career == ""`, because a roster reading that cannot be placed in a save belongs to no save |
| `KittenDistance(ctx, playerID) (float64, error)` | `CareerKittenDistance(ctx, playerID, career) (float64, error)` |
| `KittenTops(ctx, playerID) (travelled, missions KittenTop, error)` | `CareerKittenTops(ctx, playerID, career) (…)` |

`flushCareerBodies` and `flushCareerKittens` join `Flush`'s fixed order immediately after
`flushBodies` and `flushKittens` respectively, key-sorted like every other flush so a rebuild is
byte-comparable to the incremental result.

**The `KittenTops` tie-break stays on `kid`, never Go map order** (`batch.go:679`). The career
sibling breaks ties on `(career, kid)` so it is still total.

**A5.3 — the three set-backed folds** in `boards.go`. Each keeps everything it does today and adds a
career arm:

```go
// soiFold — sketch. The same shape applies to landedBodiesFold and distanceFold.
//
// The lifetime half is UNCHANGED, line for line. Do not refactor it while you are
// here: it is what keeps `soi_bodies` producing the number it produces today, and
// therefore what keeps this task free of a stats.BuildVersion bump.
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
	return setCareerValue(ctx, b, ev, StatSOIBodies, float64(n))
}
```

`toBodyFold` additionally calls `LowerCareerBodyTime` beside its existing `LowerBodyTime`, so the
`fastest_to_<body>` career-scoped value has a per-save arrival time to read.

**Tests:**
- `TestLifetimeSetBoardsAreUnchangedByCareerSiblings` — fold a history with two careers and assert
  `soi_bodies`, `landed_bodies`, `distance_travelled`, `top_kitten_distance` and
  `top_kitten_missions` produce **byte-identical** `player_stat` rows (value, `updated_seq` and
  `context`) to the same history folded before this task. **Write this first.** It is the whole
  safety property of the additive design, and it is what says no `BuildVersion` bump is owed.
- `TestCareerSetBoardsArePerSave` — the same body reached in two careers counts once per career and
  once lifetime.
- `TestKittenRowsSplitPerSave` — the same `kid` in two careers keeps two `career_kitten` rows and one
  `kitten` row.
- `TestARosterSnapshotWithNoCareerWritesNoCareerKittenRow`.
- `TestRebuildEqualsIncrementalForTheCareerSetTables`.

**A5.4 — OPTIONAL, the owner's call: correct the lifetime `distance_travelled`.**

**Do not do this without being asked.** It is written out because it is now nearly free and because
leaving it undocumented would be worse than either choice.

Today `distance_travelled` sums, over `kitten`, a `travelled_m` that was merged with `max()` across
saves — so it reports *the furthest any one name ever got in a single save*, not the total across
saves. With `career_kitten` in place, the correct number is one query change:
`SELECT COALESCE(SUM(travelled_m),0) FROM career_kitten WHERE player_id = ?`.

If taken, it is a fold whose **name is unchanged and whose meaning changed**, so it carries:
1. **a `stats.BuildVersion` bump in the same commit** (§5.1, `PROJ-102`);
2. a `DECISIONS.md` entry saying the number goes **up** for anyone who reused kitten names, and why
   that is the right number;
3. an update to `distance_travelled`'s `how` line in `docs-site/src/data/boards.ts` and its entry in
   `docs/event-details.md`.

If **not** taken, the quirk still gets written into `docs/event-details.md`'s `kitten` section — it is
currently behaviour no document states, which is exactly what the *Known drift* section exists for.

**Docs in this task's commit:** `docs/event-details.md` — a **State projections** entry for each new
table, the `kid`-is-not-save-scoped note in the `kitten` section, and the third `kind` value on
`career_body` once Task D3 lands.

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
| `play_sessions` | `Times Resumed` | `sessions` | no | no | count | `session.started` |

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

`play_sessions` is one line in `BoardFolds()`:
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
	Stat       string
	Value      float64
	Context    json.RawMessage
	UpdatedSeq int64
}

func (p *Projections) CareerLeaderboard(ctx context.Context, stat string, asc bool, limit, offset int) ([]CareerStatRow, error)
func (p *Projections) CareerStatsForPlayer(ctx context.Context, playerID int64, career string) ([]CareerStatRow, error)
func (p *Projections) CareerStatAhead(ctx context.Context, stat string, value float64, seq int64, asc bool) (int64, error)
func (p *Projections) CareerStatEntrants(ctx context.Context, stat string) (int64, error)   // row count, for "#3 of 41"
func (p *Projections) PlayerCareers(ctx context.Context, playerID int64) ([]CareerState, error) // ordered by ordinal
func (p *Projections) CareerByOrdinal(ctx context.Context, playerID int64, ordinal int64) (CareerState, bool, error)
func (p *Projections) CareerStatCounts(ctx context.Context, stat string) (int64, error)
```

`PlayerCareers` returns `stats.CareerState` including `Ordinal`, `MaxSimT`, `Rewound`, `FirstSeq`.

**Note the difference from `StatPlayers`:** `career_stat`'s PK is `(player_id, career, stat)`, so
`count(*) GROUP BY stat` counts **saves**, not distinct players. Name the method
`CareerStatEntrants` so nobody reads it as a player count, and say so in its doc comment — PROJ-034's
"the PK *is* the distinct-player count" note does **not** transfer.

**Tests:** `server/internal/store/projections_test.go` — ordering, paging, ties, and that
`CareerStatEntrants` counts saves.

---

### Task A8 — prove rebuild == incremental for every new table

**Files:** `server/internal/projector/projector_test.go`, `server/internal/store/store_test.go`,
`server/internal/store/projections.go`.

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

**Do all three, in this task:**

1. **`projector_test.go`** — add `CareerStats`, `CareerBodies` and `CareerKittens` fields plus their
   `dump(...)` calls, and widen the `career` dump to carry `ordinal`. Then confirm
   `TestRebuildEqualsIncrementalForAnUnflaggedHistory` diffs them.
2. **`store_test.go`** — add `career_stat`, `career_body`, `career_kitten` to
   `TestMigrationsCreateTheFullDDL`'s expected projections **table** list, and `career_stat_rank` /
   `career_kitten_player` to its expected **index** list. Bump the expected projections schema
   version.
3. **`store/projections.go` → `Counts`** — add the three tables' `count(*)` so the admin census and
   `RebuildResult` report them.

Only the first is silent when forgotten, and that is precisely why it is first: a new projection
table absent from the snapshot is a table the equivalence guarantee **skips without failing**. Every
later phase in this plan adds another one, so leave the snapshot in a state where adding a table is
obviously three edits rather than one.

Add `TestRebuildEqualsIncrementalForCareerScope` with a history that exercises: two careers for one
player, an interleaved third, a flagged flight in one of them, a career with no `sim_t` on some
events, and events with no career at all.

**Acceptance:** `make test` green, including `-run Rebuild`.

---

### Task A9 — moderation and the shadowban interaction

**Files:** read-only investigation, then a test.

Confirm that a purge and a shadow ban both clear `career_stat`, `career_body`, `career_kitten`,
`badge_award` and `challenge_stat` by the same route `player_stat` uses.

**The expected answer is "nothing to do", and the point of the task is to prove it rather than assume
it.** `STORE-018` made the shadow ban structural: the player's rows are *moved out of* `event` into
`shadowban_event`, so a rebuild produces projections that never saw them, and `PurgePlayer` deletes
from both tables. Every table in this plan is a projection rebuilt from seq 0, so all five empty out
for free — **provided nothing in the shadow-ban or purge path enumerates projection tables by
name.**

**What to actually do:**
1. `git log --oneline -20`, then read `server/internal/store/shadowban.go`,
   `server/internal/adminapi/shadowban.go` and `server/internal/projector/job.go`.
2. Grep for any list of projection table names (`player_stat`, `flight_state`, `career`, …). If one
   exists, add `career_stat`, `career_body`, `career_kitten`, `badge_award` and `challenge_stat` to
   it.
3. Extend `server/internal/projector/shadowban_test.go` (it exists) to assert the new tables empty
   out for that player after the rebuild, and do the same for the purge test.
4. If nothing enumerates tables by name, **write that finding into the Phase I decision entry** —
   "the new tables need no moderation wiring, because the exclusion is structural and every one of
   them is rebuilt from the log" — rather than leaving it unstated. A reader six months from now
   should not have to re-derive it.

---

## Phase B — career scope on the read API and both frontends

**Goal:** a player can open a board and rank saves, and open their own list of saves.

---

### Task B1 — `?scope=` on the board endpoint, `scopes` in the index

**Files:** `server/internal/readapi/readapi.go`, `query.go`, `docs/ingest-api.md`.

**B1.1 — `BoardSummary` gains `Scopes []string \`json:"scopes"\`**, filled from `stats.Scopes()`.
Every board publishes both, because every board has both (§3.2).

**B1.2 — `handleBoard` parses `?scope=`** beside `?period=`, in this order, and **refuses the
combination**:

```go
	scope, ok := stats.ValidScope(r.URL.Query().Get("scope"))
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "bad_request",
			"scope must be one of "+strings.Join(stats.Scopes(), ", "))
		return
	}
	if scope == stats.ScopeCareer && period != stats.PeriodAllTime {
		// A career already is a time scope. Crossing it with a rolling window
		// would be a window over a window, and the row count is
		// players x boards x buckets x careers — see 0006_career_scope.sql.
		s.fail(w, r, http.StatusBadRequest, "bad_request",
			"scope=career has no time windows: a save is already a period")
		return
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
	SaveID  string          `json:"save_id,omitempty"`
	Value   float64         `json:"value"`
	Context json.RawMessage `json:"context,omitempty"`
	Updated int64           `json:"updated"`
	Rewound bool            `json:"rewound,omitempty"`
}
```

**B1.4 — `Server.Board` takes `scope`** and branches to `visibleCareerRows`, a copy of
`visibleRows` (`readapi.go:306-358`) over `CareerLeaderboard` with the same over-fetch-and-drop ban
filter, the same `maxScan = 5000` and the same `scanPage = 256`. **Do not** invent a second ban
filter; if the two functions can share their scan loop with a row-source closure, do that instead.

`Rewound` on a career row is read from the row's **own** career, so it applies to every board in
career scope, not only the `Board.Career` ones. Widen `rewound()` accordingly.

**B1.5 — bump the endpoint's `ver`** per Constitution §9 and record the shape in
`docs/ingest-api.md` §4.8 (both the `/v1/leaderboards` line and the `/v1/leaderboards/{stat}` line).

**Tests:** `readapi_test.go` — scope round-trip, the 400 on `scope=career&period=weekly`, ban
filtering in career scope, ordering, `save`/`save_id` present in career scope and **absent** in
player scope, and a redaction test asserting the raw career never appears.

---

### Task B2 — the two saves endpoints

**Files:** new `server/internal/readapi/saves.go`; register in `readapi.go`; `docs/ingest-api.md`.

```
GET /v1/players/{handle}/saves
  -> {"handle": s,
      "saves": [{"save": 1, "save_id": s, "playtime_ms": f, "first": unix_ms,
                 "last": unix_ms, "rewound"?: true, "boards": n}]}

GET /v1/players/{handle}/saves/{ordinal}
  -> {"handle": s, "save": 1, "save_id": s, "playtime_ms": f, "rewound"?: true,
      "stats": [{"stat", "title", "unit", "value", "ascending", "rank", "entrants",
                 "context"?, "updated"}]}
```

Rules, each mirroring an existing one:
- **Unknown / retired / banned handle → one 404**, the same answer, deliberately not a ban oracle
  (PROJ-007). Reuse the existing handle resolution in `readapi.go:428-431`.
- An ordinal with no career → 404 with distinct copy ("catlog has no such save for this player"),
  the same "which of the two is it" discipline `handleBoard` uses for windows.
- `first` / `last` come from `Events.RecvTimes` on `first_seq` / the max `updated_seq`, the same way
  board rows resolve `updated` (PROJ-010) — projections.db and events.db **cannot be joined**.
- `rank` is `CareerStatAhead(...) - hiddenAhead + 1`, re-applying the `better || (equal && earlier seq)`
  comparison in Go so a save's page can never contradict the board page.
- `entrants` is `CareerStatEntrants` — **saves, not players**; label it that way in the response and
  on the page.
- Every response carries `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`, including
  the 404s. Route through `s.public(mux, ...)` so CORS is attached by the one place that attaches it.

---

### Task B3 — privacy: relabel, never publish, and pin it with a test

**Files:** `server/internal/readapi/privacy.go`, `privacy_test.go` / `redaction_test.go`.

`Label(playerID, kindCareer, career)` already exists and already produces the 16-character
per-player relabel. Every new surface uses it:

- `BoardRow.SaveID`
- `SavesResponse.Saves[].SaveID`
- `SaveResponse.SaveID`
- badge and challenge rows (Phases F, H)

**Extend `redaction_test.go` by naming what a regression would leak** — the existing test's spirit.
Add a table-driven test that walks the JSON of every new endpoint and fails if any string equals a
known raw career id. Make it generic over responses so Phases F and H inherit it:

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

   Add `scopeLabel` to `templateFuncs`: `player` → `Players`, `career` → `Saves`, falling back to the
   raw key so a future scope renders its own name (the `periodLabels` pattern, `pages.go:143-158`).

3. **When `scope=career`, the period chips are not rendered** and a one-line note says why:
   *"A save is already a period, so these boards have no time windows."* Sentence case, no
   exclamation mark, British spelling (`docs/ui-design.md` §9.2).
4. `board-table` gains an optional **Save** column between Handle and the value, rendered only in
   career scope: `<a href="/p/{handle}/saves/{save}">Save {{.Save}}</a>`.
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

`/p/{handle}/saves` — one `.panel` with a table: Save · Played · First seen · Last seen · Boards.
`Played` is `{{numUnit .PlaytimeMs "ms"}}` so it renders through the duration ladder. Empty state:
*"No saves recorded yet."*

`/p/{handle}/saves/{ordinal}` — the profile table, scoped. Reuse `board-table`'s cell partials
(`value-cell`, `context-cell`) rather than writing new ones. Header line:
*"Save 2 · played 4d 06h · #3 of 41 saves on Landings"* — note **saves**, not players (Task B2).
The rewound dagger and its exact tooltip come from `value-cell` unchanged.

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

1. **`boards.ts`**: add `scopes: ("player" | "career")[]` to the `Board` interface with a doc
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
4. **`site/e2e/`** — a new `saves.spec.ts` for the two pages.

**Acceptance:** `make test` green, `make e2e` green, `cd docs-site && pnpm check` green.

---

## Phase C — wire v2: three payload fields, and what `flight_state` learns

**Goal:** the three readings the community ideas need that cannot be derived, and the `flight_state`
columns that stop us needing any more.

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

---

### Task C1 — `vehicle.rud` → `ver 2`, gains `part_count`

**Community idea #4.** The only honest reading of "most parts destroyed" that KSA supports.

**Mod files, in order** (the full end-to-end checklist for a payload field is Appendix E):

1. `mod/catlog.lib/Events/EventTypes.cs` — `[VehicleRud] = 2` in the `Versions` dictionary.
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

**Server files:** see Task C4.

---

### Task C2 — `flight.started` → `ver 2`, gains `engine_count`

**Community idea #5** ("get to jupiter with no engines").

1. `EventTypes.cs` — `[FlightStarted] = 2`.
2. `Payloads.cs` — `[property: JsonPropertyName("engine_count")] int EngineCount,` placed after
   `StageCount` and before `Lat`/`Lon`.
3. `GameSignal.cs` — `VehicleCreatedSignal` gains `int EngineCount`.
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
        Member = "Vehicle.Parts.Modules.Get<EngineController>()",
        SourceFile = "KSA/ModuleList.cs:164",
        Verified = "2026-08-09", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Medium,
        Notes = "Modules.HasAny<EngineController>() is the cheaper predicate; we want the count.")]
    public static int EngineCount(Vehicle vehicle)
    {
        try { return vehicle.Parts.Modules.Get<EngineController>().Length; }
        catch (Exception ex) { Faults.Note(ex); return 0; }
    }
```

6. `mod/catlog/PolledSignals.cs` — `Track` fills `EngineCount: VehicleTelemetry.EngineCount(vehicle)`
   when it builds `VehicleCreatedSignal`.

**The honest limitation, which the site must state (Task F3/H3):** *"no engines" means no engine was
installed when the flight began.* RCS thrusters, decoupler springs and docking-port pushoff all
impart velocity and are not engines; and a vehicle can shed its engines in transit, at which point
the piece that continues is a **new flight** with its own count. Both are stated, neither is
engineered around — inferring "did they really coast" from data shape is the thing Constitution §8
forbids.

---

### Task C3 — `kitten.tumble` → `ver 2`, gains `from`

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

1. `EventTypes.cs` — `[KittenTumble] = 2`.
2. `Payloads.cs` — `[property: JsonPropertyName("from")] string From,` on `KittenTumblePayload`.
3. `GameSignal.cs` — `TumbleSignal` gains `string From`.
4. `PolledSignals.cs` — pass `state.Locomotion`, lowercased, through a **total** mapper; an
   unreadable or previously-unseen mode becomes `"unknown"`, never a guess. Mirror `SituationName`'s exhaustive
   switch in `VehicleTelemetry.cs:1258-1269`.
5. `EventPipeline.cs` — the `TumbleSignal` arm passes it through.

**`from` is an open set, opaque to the server**, exactly like `situation` and `body`: KSA declares
`Ladder` with no producer today and a future build may add a mode. Do not write an allow-list. The
board in Task D1 tests for the one value it cares about and treats everything else as "not a botched
landing".

**One caveat to carry into `docs/event-details.md`:** a violently tumbling kitten that leaves the
ground for longer than `TumbleAirborneExitTime` (0.5 s stock) goes `Tumbling → Airborne` and
re-enters `Tumbling` on the next bounce — so one cartwheel can produce several tumbles, some of them
`from: "airborne"`. That is the game's own state machine and catlog reports it rather than smoothing
it.

---

### Task C4 — the server half of the three bumps

**This task is what stops the three bumps being silent data loss.** A `ver` the mod stamps and the
server does not know is skipped by the projector as a future version, with one log line.

**Files:**

1. `server/internal/projector/upcast.go` — `currentVer` gains
   `{"vehicle.rud": 2, "flight.started": 2, "kitten.tumble": 2}`, and `CurrentVer` follows.
2. `server/internal/projector/upcast.go` — **register three upcasters, `(type, 1) → 2`.** Every one
   is the identity plus a default:
   - `vehicle.rud` v1 → v2: `part_count` absent → decodes as `0`, and every board reading it gates
     on `> 0`. **Write the upcaster anyway, as a no-op with a comment**, so the registry is a
     complete record of every shape that has existed. `projector.Upcasters` has shipped empty since
     day one (PROJ-015) precisely so that the first bump is a registration rather than a migration —
     this is that moment.
   - Same for the other two.
3. `server/internal/stats/payload.go` — add the three fields to `VehicleRUD`, `FlightStarted` and
   `KittenTumble` with their `json:` tags.
4. `server/internal/ingest/types.go` — **no change**; no new type names.

**Tests:**
- `projector.TestGoldenBatchIsAtTheCurrentVersions` will fail until the vectors are regenerated
  (Task C6) — that is the drift check working.
- Add `TestAVersionOneRudDecodesWithNoPartCount` and its two siblings: a stored `ver: 1` payload
  still folds, and the boards that read the new field decline it.

---

### Task C5 — `flight_state` learns what the vehicle was and what it achieved

**Files:** a new projections migration (`0008_flight_facts.sql` — **re-check the number**),
`server/internal/stats/flight.go`, `server/internal/stats/batch.go`.

This is §3.11: the join key that keeps the wire small. Four columns and a bitfield.

```sql
-- projections.db 0007 — what a flight was, and what it got done.
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
ALTER TABLE flight_state ADD COLUMN engine_count   INTEGER;   -- NULL: flight.started not seen, or ver 1
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

**Tests:** `TestFlightMilestonesAccumulate`, `TestFlightFactsSurviveAnOutOfOrderBatch` (a
`vehicle.orbit` folded before its `flight.started` — the batch may legitimately do that, and
`EnsureFlight` already creates the row for any flight-bearing event), and rebuild equivalence.

---

### Task C6 — conformance vectors, and the documents the bumps touch

**Files:** `server/internal/testvectors/testvectors.go`, `contracts/testdata/**`, `docs/events.md`,
`docs/event-details.md`, `docs-site/src/data/events.ts`, `docs-site/src/content/docs/events/*.mdx`.

1. **Regenerate:** `make testvectors` (`catlogctl testvectors generate contracts/testdata`). Before
   that, edit `testvectors.go` so the batch:
   - stamps the three new versions (it hard-codes `ver` per line);
   - carries the three new fields;
   - **carries `kitten.tumble` twice** — once `from: "airborne"` (a botched landing) and once
     `from: "grounded"` (a trip). The vector set's whole job is to pin payload *shapes*, and a
     value-discriminated board needs both sides pinned. This mirrors why `flight.started` and
     `telemetry.window` each already appear twice.
2. `docs/events.md` — the taxonomy table's three rows, the `ver` column, and the "23 types, every one
   at `ver: 1`" heading, which is now false. Add the `from` vocabulary note beside the existing
   `situation` one, marked **open set**.
3. `docs/event-details.md` — the three event sections (Wire, Payload, Detector, Game source,
   Classification blocks), the registry table's `ver` column, and the sentence *"Every type is at
   `ver` 1"* under **The registry**, which must be rewritten rather than left.
4. `docs-site/src/data/events.ts` — `ver: 2` on the three entries and one `EventField` each, in
   player language. For `from`: *"What the kitten was doing immediately before: `airborne` means they
   were in the air, so this was a landing that did not go well."*
5. `docs-site/src/content/docs/events/kittens.mdx` — the prose for the new distinction. This is the
   page a player reads to understand the board in Task D1, and it is the more important half.

**Mod tests that will fail until updated** (they are supposed to):
- `EventTypesTests.RegistryHasExactlyTheLaunchSet` — `Assert.All(… Assert.Equal(1, VersionOf(type)))`.
  Change it to assert **the registry's declared version per type**, from a table in the test, so it
  keeps catching an *accidental* bump.
- `ContractVectorTests.Batch001_StampsTheRegistrysCurrentVersion` and
  `Batch001_PayloadsRoundTripThroughTheirRecords`.
- `TestData.Snapshot` / the signal factories gain defaulted parameters.

---

## Phase D — the boards the community asked for

Every board here is `putRecord` / `putBest` / `addCount` / set-backed, so **each one gets its career
scope for free** from Phase A. None needs a registry entry beyond `fixedBoards` and `BoardFolds()`.

The full catalog with units, directions and exclusions is **Appendix A**; the tasks below are the
implementation notes that are not obvious from it.

---

### Task D1 — the tumble split: `botched_landings` and the `tumbles_on_<body>` family

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

   `familyStat` already refuses a suffix that collides with a fixed key and already validates the
   alphabet. **A body whose name cannot form a key still counts towards `kitten_tumbles`** — that
   invariant is `familyStat`'s, not yours, and it must not be re-implemented.

**Test:** `TestABodyNamedAfterAFixedBoardGetsNoTumbleBoardButStillCounts`.

---

### Task D2 — `parts_lost` and `biggest_parts_lost`

**Files:** `boards.go`, `fold.go`.

From `vehicle.rud.part_count` (Task C1). Two boards from one reading, the `rud_total`/`rud_<cause>`
shape:

- **`parts_lost`** — count, unit `parts`, adds `part_count` per RUD. "How many parts you have lost,
  in total, to explosions."
- **`biggest_parts_lost`** — record (max). "The largest vehicle you have lost in one go."

Both gate `part_count > 0` (PROJ-088: a zero is an unread value and on a counter it is a silent
no-op, on a record it is meaningless) and both go through `scoreable`.

**The title must not overclaim.** KSA has no per-part destruction; this is *the size of the vehicle
that was destroyed*, not a count of parts that individually exploded. `docs/event-details.md` says
the technical version, `docs-site` says the player version: *"KSA does not blow parts off one at a
time — a vehicle either survives or it does not. This is how big the thing was when it stopped
existing."*

---

### Task D3 — `kittens_to_orbit_and_back`

**Files:** `boards.go`, `batch.go` (a new `player_set` kind), `fold.go`.

**Community idea #3.** Set-backed, per kitten, using the machinery `soi_bodies` already uses.

**The rule, stated once:** a kitten counts when she is aboard a flight that **reached orbit** and
that flight later **ended recovered** with her still aboard.

```go
// kittensToOrbitFold — sketch.
//
// Reads flight_state.milestones (Task C5) rather than correlating a
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
	// One row per kitten, both scopes, novelty-reported — the AddBody shape.
	// Iterate p.Kids IN ORDER, never a map, so a rebuild reproduces the context
	// byte for byte.
	...
}
```

**Storage.** Reuse the existing set shape rather than adding a fifth table: `player_body` and
`career_body` are both `(…, kind, body, …)` and `kind` is already a discriminator. Add
`kind = 'orbit_kid'`, with the `kid` in the `body` column, to **both** — `player_body` for the
lifetime board and `career_body` for its per-save scope, exactly as `soiFold` does after Task A5.
**No migration is needed**, since neither table's shape changes.

**Rename nothing.** `body` holding a `kid` reads oddly, and renaming the column would touch four
documents and buy nothing — the column is "the set member" and always was. Say so in a comment and in
`docs/event-details.md`'s `player_body` / `career_body` sections, where `kind` now has three values.

**Two caveats, both stated and neither engineered around:**
- **"Back" means recovered.** KSA only offers recovery on the system's home body, at rest, in
  contact (`Vehicle.CanRecover`). A crew that lands on the Moon and stays has not come back.
- **A kitten who boarded *after* orbit** and rode home is counted, and a kitten who was aboard at
  orbit but transferred away before recovery is not. Crew move between vehicles by EVA, boarding and
  docking, and catlog resolves the question at the two ends rather than tracking seats continuously.
  This is the cheap answer and it is the honest one; say so in `docs/event-details.md`.

---

### Task D4 — `biggest_crew_wreck` and `kittens_wrecked`

**Files:** `boards.go`, `fold.go`. **No wire change** — `vehicle.rud.crew_count` already ships.

**Community idea #7**, delivered under a title that is true.

D11 is re-confirmed at source in build 5168: `Universe.DestroyVehicle` calls `EndAllCrewMissions()`,
which calls `EndMission()`; `Kia = true` is written in exactly one place, reachable only from the
player-initiated destroy path. **A physics RUD returns the whole crew alive.** So there is no such
thing as "kittens KIA in a crash", and a board with that title would be a lie catlog told about its
own data.

- **`biggest_crew_wreck`** — record (max) on `crew_count`, gated `>= 1` and `scoreable`.
  Title: **"Most Kittens Aboard A Lost Vehicle"**.
- **`kittens_wrecked`** — count, adds `crew_count` per RUD. Title: **"Kittens Walked Away From A
  Wreck"** — because that is literally what happened to them.

The site copy must carry the joke *and* the fact: *"Kitten Space Agency's kittens are indestructible
in a crash — the game ends their mission and sends them home. So this is not a body count. It is how
many of them were aboard when the vehicle stopped existing, and they all walked away from it."*

A separate `kittens_scuttled` counter from `kitten.kia` is **not** built. `kitten.kia` signals a
deliberate scuttle with crew aboard, and a public board ranking that is a durable public consequence
attached to a person for a choice the game offers — Constitution §8's consequence test. Record the
refusal in `docs/ROADMAP.md` (Task I3).

---

### Task D5 — the sim-time sprint boards: `bodies_by_1y`, `bodies_by_10y`

**Files:** `boards.go`, `fold.go`. **No wire change, no new game read.**

**Community idea #6** ("most SOIs within 10 years"). Career-native by nature and free from Phase A.

- Value: the number of **distinct** bodies whose SOI the player entered at a `sim_t` below the
  threshold. Set-backed like `soi_bodies`, reading `first_sim_t` from `player_body` (lifetime) and
  `career_body` (per save).
- Thresholds: `SprintYearSeconds = 365 * 24 * 3600` — **a flat 365-day year, matching
  `server/internal/units`' duration ladder**, which is already what a catlog `ms` value renders as.
  `bodies_by_1y` = 31 536 000 s, `bodies_by_10y` = 315 360 000 s.

**This is the second most likely place to get §3.7 wrong.** A "year" here is a unit of catlog's own
number formatting, **not** a body's orbital period. Reading Earth's period out of the game to define
a year would put a celestial-body fact into the server, which is precisely what PROJ-033 removed.
Put that sentence in the fold's doc comment.

Both boards are `kindRecord` on the count (so they only ever go up), gated on
`ev.HasCareer() && ev.HasSimTime`, and both are much more interesting at `?scope=career` than at
player scope — which the site should say rather than the code enforcing.

---

### Task D6 — the documentation for Phase D

**In the same commits as the code above, not after.**

- `docs/event-details.md` — a **Boards** table row per new board (the count moves again; state the
  new total), a **Fold detail** entry each, rows in the **Suppression and eligibility matrix** for
  every new gate, and the third `kind` on `player_body` / `career_body`.
- `docs-site/src/data/boards.ts` — one entry per board with `what` / `how` / `excluded` written for a
  player, plus the new `tumbles_on_<body>` family in `BOARD_FAMILIES`.
- `docs-site/src/content/docs/leaderboards/catalog.mdx` — `<BoardDetail>` blocks in the right
  sections, with the three honesty asides above (`parts_lost` is not per-part; `biggest_crew_wreck`
  is not a body count; "no engines" does not mean "no propulsion").
- `docs-site/src/content/docs/events/kittens.mdx` and `vehicle.mdx` — the new fields.

---

## Phase E — merit badges, server side

**Goal:** a permanent, timestamped, once-only award, projected independently at **player** scope and
at **per-save** scope, from the events already in the log.

**The model, in one paragraph.** A badge is a named predicate over the event stream. When an event
first satisfies it, a row is written with the seq, the wall time and the career clock at which it
happened, and nothing ever changes that row. There is no revocation, no expiry and no downgrade —
a badge records that a thing happened, and it did.

---

### Task E1 — the `badge_award` table

**Files:** a new projections migration (`0009_badges.sql` — **re-check the number**).

```sql
-- projections.db 0008 — merit badges.
--
-- A badge is a milestone: the interesting property is WHEN YOU FIRST GOT IT, so
-- the write is INSERT ... ON CONFLICT DO NOTHING and the row never changes. That
-- is also what makes it replay-stable — a rebuild replays the same seqs in the
-- same order and therefore awards at the same event.
--
-- ONE TABLE, TWO SCOPES. career = '' is the LIFETIME award; career = '<id>' is the
-- per-save award, and the same badge is earned independently in both. The two
-- scopes have identical columns and identical rules, so splitting them would mean
-- two flushes, two dumpers, two read paths and two places to forget something.
-- '' is safe as a sentinel because ingest rejects any career that is not exactly
-- 16 Crockford characters — the same trick event_census uses for `type = ''` and
-- `bucket = ''`.
--
-- No `revoked` column, and there must not be one. A badge is not a rank and not a
-- claim about a player; it is a record that an event happened. Constitution §8's
-- consequence test also applies: nothing here may ever treat a player differently
-- because of accumulated history.
CREATE TABLE badge_award (
  player_id    INTEGER NOT NULL,
  career       TEXT NOT NULL,        -- '' = lifetime; otherwise the save it was earned in
  badge        TEXT NOT NULL,        -- a fixed key, or a family key built from event data
  earned_seq   INTEGER NOT NULL,     -- the projector cursor: the tie-break, and the sort key
  earned_at    INTEGER NOT NULL,     -- unix ms, the SERVER receive stamp — never wall_t
  earned_sim_t REAL,                 -- career clock in seconds; NULL when the event carried none
  context      TEXT,                 -- JSON; the same shape and the same allow-list as player_stat.context
  PRIMARY KEY (player_id, career, badge)
);

-- "who has this badge, earliest first" and "how many hold it" from one index.
CREATE INDEX badge_holders ON badge_award(badge, earned_seq);
-- "everything this save earned, in the order it was earned".
CREATE INDEX badge_by_career ON badge_award(player_id, career, earned_seq);
```

---

### Task E2 — the badge registry

**Files:** new `server/internal/stats/badges.go`.

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

### Task E3 — the `Batch` accumulator and the `award` helper

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
// eligibility, because a few of them (the ones sourced from flightless events)
// have nothing to gate on. Every badge fold that reads a flight-bearing event
// MUST call scoreable() first; the checklist in Appendix B says which.
func award(ctx context.Context, b *Batch, ev Event, badge string, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	b.putBadge(ev.PlayerID, "", badge, ev, cx)
	if ev.Career != "" {
		b.putBadge(ev.PlayerID, ev.Career, badge, ev, cx)
	}
	return nil
}
```

`flushBadges` goes into `Flush`'s fixed order after `flushCareerStats`, key-sorted like every other
flush. Add `Batch.HasBadge(ctx, playerID, career, badge)` as a read-through for the composite badges
that need to know.

---

### Task E4 — the four badge fold shapes

**Files:** new `server/internal/stats/badgefolds.go`; one line in `fold.go`.

Add `BadgeFolds() []Fold` and append it inside `SecondPassFolds()` **after** `BoardFolds()` and
**before** `LogFolds()`:

```go
func SecondPassFolds() []Fold {
	return append(append(BoardFolds(), BadgeFolds()...), LogFolds()...)
}
```

**The order is load-bearing.** Threshold badges read a counter's *post-write* value from the batch,
so every board fold must have run for that event first.

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
// and therefore a rebuild, replaying the same events in the same seq order, reads
// the same value and awards at the same event.
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

**3. Composite badges** — need flight state. Read `flight_state.milestones` (Task C5); never
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
6. `TestAFlaggedFlightEarnsNoBadge`.
7. `TestABadgeForAnUnkeyableBodyIsSkippedButStillCountsTowardsTiers`.
8. `TestRebuildEqualsIncrementalForBadges` — **including at three different batch sizes**, because
   the threshold shape is the one that could depend on batch boundaries and must not (PROJ-060).

---

### Task E5 — the starter badge catalogue

**Files:** `server/internal/stats/badges.go`, `badgefolds.go`.

**The full catalogue is Appendix B.** It is 30 badges across five groups, and every one is derivable
from events already on the wire plus Phase C's three fields. Implement it verbatim; the appendix
gives key, title, blurb, group, tier, shape and predicate for each.

**The one design constraint to re-read before writing it:** §3.7. There is no `visited_every_planet`
badge and there must not be one. The exploration ladder is
`wanderer` (3 bodies) → `voyager` (5) → `grand_tour` (8), threshold badges on `soi_bodies`.

The evidence, since somebody will want to argue: the shipped `Astronomicals.xml` in build
2026.8.5.5168 contains **11 bodies** — Sol, Mercury, Venus, Earth, Luna, Mars, Phobos, Deimos,
Jupiter, Saturn, Uranus — plus seven comets. **There is no Neptune, no Titan and no Galilean moon.**
"Every planet in the solar system" evaluated against today's build is a *seven-planet* set that will
change on the next content patch and is already wrong for anyone running a system mod. A count
threshold is checkable, survives content changes, and says something true.

An alternative that would make "everything this build ships" answerable **is** available and is
written up as **Task E7**, which is optional and explicitly the owner's call.

---

### Task E6 — store reads and the census

**Files:** `server/internal/store/projections.go`, `server/internal/readapi/stats.go`.

```go
type BadgeRow struct {
	PlayerID   int64
	Career     string
	Badge      string
	EarnedSeq  int64
	EarnedAt   int64
	EarnedSimT sql.NullFloat64
	Context    json.RawMessage
}

func (p *Projections) BadgesForPlayer(ctx context.Context, playerID int64, career string) ([]BadgeRow, error)
func (p *Projections) BadgeHolders(ctx context.Context, badge string, limit, offset int) ([]BadgeRow, error) // career='' only, earned_seq ASC
func (p *Projections) BadgeCounts(ctx context.Context) (map[string]int64, error)                             // career='' only
func (p *Projections) BadgeHolderCount(ctx context.Context, badge string) (int64, error)
```

**Every count is over `career = ''` rows only.** A count over all rows would count a player once per
save that earned it, and "how many people have this" is the question being asked. Say so in the doc
comments; it is the single easiest thing to get wrong here.

Add `badges` and `badge_awards` to `GET /v1/stats`'s `collection` census, memoised on
`(WriteGen, 10 s TTL)` like the rest (PROJ-083).

---

### Task E7 — OPTIONAL: "you have been everywhere this game has"

**Do not implement this without the owner saying so.** It is written out because the community asked
for "visited every planet in the solar system" and it is the only honest way to answer that question.
It costs a new event type, and the plan's default answer is the tier ladder in Task E5.

**The idea.** The server must never hold a list of bodies (§3.7), but **the game can be asked at
runtime** what is in the system it loaded: `Universe.CurrentSystem.All` enumerates every
`Astronomical`, and each `Celestial` publishes `Class` (`"Planet"` / `"Moon"` / `"Star"`) and `Rank`
(depth from the root), all derived by the game rather than by us. So the mod reports the catalogue,
the server folds "have you been to all of it", and **no list lives in catlog at all** — the badge is
correct for stock KSA, for a content patch that adds Neptune, and for a total-conversion system mod,
on the day each ships.

**What it would take:**
- A new event type `system.catalog`, `ver 1`, `AlwaysReported: false`, `KindEvent`, one per session:
  `{"system": s, "bodies": [{"id": s, "class": s, "rank": i}]}`. Emitted from the session boundary,
  **not** from `session.started` — `Universe.CurrentSystem` may not be populated at the moment the
  session ULID is minted, so it needs its own poll-driven emission on the first tick where the
  system is readable.
- A `system_body(player_id, career, body, class, rank)` projection.
- Badges `been_everywhere_planets` / `been_everywhere` folded as "the set of bodies you have entered
  the SOI of covers every body of class `Planet` this session reported".

**The trade-off, stated:** it is trivially forgeable — a modified client reports a one-planet system
and mints the badge. Under Constitution §8 that is *acceptable* ("we accept that a determined person
can put a fake number on a leaderboard about a cat game") and the tier ladder is no better in this
respect, since SOI transitions to invented bodies are equally forgeable. The real cost is
Constitution §2: a new event type, a new table, a new detector poll and a fourth thing to keep in
step across two implementations and two documents — for one badge.

**If it is built,** everything in Appendix E applies (it is a new event type), and it gets its own
`DECISIONS.md` entry recording *why the list lives in the game rather than the server*, because that
is the whole argument.

---

## Phase F — badges on the read API and both frontends

### Task F1 — the badge endpoints

**Files:** new `server/internal/readapi/badges.go`; register in `readapi.go`; `docs/ingest-api.md`.

```
GET /v1/badges
  -> {"min_players": n,
      "badges": [{"badge": s, "title": s, "blurb": s, "group": s, "tier"?: n, "holders": n}]}

GET /v1/badges/{badge}
  -> {"badge": s, "title": s, "blurb": s, "group": s, "holders": n, "limit": n, "offset": n,
      "rows": [{"rank": n, "handle": s, "earned": unix_ms, "sim_t"?: f, "context"?: {…}}]}
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
- `unearned` lists only **fixed** badges. Listing every unearned family badge would be a list of
  every place anyone has ever been, per player, and it grows without bound.

**Bump the read API `ver`** and record the four shapes in `docs/ingest-api.md` §4.8.

---

### Task F2 — the badge pages

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
- `tabular-nums` on every number; `data-value` on anything a test might read.
- Empty state, matching the existing voice: **"No badges yet. Fly something."**
- **Tone:** dry, understated, affectionate about failure; British spelling; sentence case for
  everything we write; no exclamation marks; never invent a fact for a joke.

Add a `Badges` link to `profile.gohtml`'s button row, and a badge count to the save page from
Task B5.

---

### Task F3 — the docs-site half

**Files:** new `docs-site/src/data/badges.ts`; new `docs-site/src/components/BadgeDetail.astro`;
new `docs-site/src/content/docs/badges/index.mdx` and `docs-site/src/content/docs/badges/catalog.mdx`;
`docs-site/astro.config.mjs`.

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
   - A badge is permanent. **It is never taken away**, and there is no level or score attached to it.
   - You earn each badge **twice**: once for good, and once in the save you did it in. A new save
     starts with none of the per-save ones and all of your lifetime ones intact.
   - Anyone can look at anyone's badges — they are as public as a leaderboard row.
   - **Why there is no "visited every planet" badge**, in the player's own terms: *"catlog has no
     list of worlds anywhere in it. Leaderboards for places exist because somebody went there, and
     badges work the same way — which means catlog genuinely does not know how many planets there are
     supposed to be, and would be wrong the moment the game added one or you installed a mod that
     did. So the exploration badges count worlds instead of naming them."*
   - A flagged flight earns nothing, linking to `leaderboards/eligibility`.

---

### Task F4 — seed and e2e for badges

- **Seed:** demo players earn a representative spread — at least one fixed badge, one family badge,
  one tier badge, and at least one badge held by **two** players so the family gate (`min_players`)
  publishes something.
- **e2e:** new `site/e2e/badges.spec.ts` — the catalogue lists what `GET /v1/badges` lists (row for
  row, **no count assertion** — PROJ-039), a badge page ranks holders earliest-first, a player's page
  separates earned from unearned, and a save's badge page is a subset of the player's.

---

## Phase G — weekly challenges, server side

**Goal:** a curated rule, over an explicitly-dated window, ranked like a board.

**Read §3.8 and §3.9 before starting.** The two decisions that shape every line of this phase are
that a challenge is **compile-time** and that its window is measured on **`ev.RecvTime`**.

---

### Task G1 — the `challenge_stat` table

**Files:** a new projections migration (`0010_challenges.sql` — **re-check the number**).

```sql
-- projections.db 0009 — time-boxed challenges.
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
-- career = '' is a player-scoped challenge; career = '<id>' scopes one to a single
-- save, the same sentinel badge_award uses.
CREATE TABLE challenge_stat (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,
  challenge   TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, challenge)
);

CREATE INDEX challenge_rank ON challenge_stat(challenge, value, updated_seq);
```

**No retention.** A closed challenge keeps its rows forever; the archive of past weeks is a feature.
Row count is players × challenges, which grows at the rate the owner writes challenges.

---

### Task G2 — the challenge registry

**Files:** new `server/internal/stats/challenges.go`.

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
	// Scope is stats.ScopePlayer or stats.ScopeCareer.
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
`catlogd`'s wiring, refusing: a duplicate key, a key that fails `statSuffix`, `Closes <= Opens`, an
unknown `Scope`, and a key that collides with a board key. A bad challenge should stop the server
starting, not produce a quietly wrong board. Add `TestEveryShippedChallengeIsValid`.

---

### Task G3 — the fold shape

**Files:** new `server/internal/stats/challengefolds.go`; one line in `fold.go`.

Add `ChallengeFolds() []Fold` and append it inside `SecondPassFolds()` after `BadgeFolds()`.

```go
// challengeFold is one challenge's rule.
//
// The window gate comes first and is the same in every one of them, so it lives
// here rather than in each `value` func:
//
//	if !c.InWindow(ev.RecvTime) { return nil }
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

`Scope` decides the row's `career`: `ScopePlayer` writes `career = ''`, `ScopeCareer` writes
`ev.Career` and is a no-op when the event carries none.

**Every challenge fold that reads a flight-bearing event calls `scoreable` first.** A flagged flight
scores nothing, on a challenge exactly as on a board.

**Tests:**
- `TestAnEventBeforeTheWindowScoresNothing` / `...After...` / `...OnTheOpeningInstant...` (inclusive)
  / `...OnTheClosingInstant...` (exclusive).
- `TestAnEventWithNoRecvTimeIsInNoChallenge`.
- `TestChallengeTieKeepsTheEarlierSeq`.
- `TestRebuildEqualsIncrementalForChallenges` — the important one. Fold a history spanning a
  challenge window, rebuild, diff.
- `TestAChallengeAddedAfterTheFactFillsFromHistoryOnRebuild` — pins §3.8's retroactivity claim.

---

### Task G4 — six starter challenges

**Files:** `challenges.go`, `challengefolds.go`.

**The full definitions are Appendix C.** They exist to prove the mechanics across every fold kind and
both scopes, and to be the worked examples the owner copies when writing week seven:

| key | Title | Kind | Scope | Reads |
|---|---|---|---|---|
| `heavy_lift_week` | Heavy Lift Week | record | player | `vehicle.orbit` `mass_kg` on the home body |
| `speedrun_orbit` | From Scratch To Orbit | best (asc) | career | `vehicle.orbit` `sim_t` |
| `tumbleweek` | Tumbleweek | count | player | `kitten.tumble` |
| `coasting_class` | Coasting Class | record | player | `vehicle.soi` joined to `flight_state.engine_count == 0` |
| `feather_touch` | Feather Touch | best (asc) | player | `vehicle.landed` `vertical_speed_ms > 0`, away from the home body |
| `full_house` | Full House | record | player | `flight.ended` `reason=recovered`, `crew_count` |

**Two naming rules that are load-bearing:**

- **`heavy_lift_week` is not called "payload".** KSA has no payload concept — verified: no
  `IsPayload`, no `PayloadMass`, and the per-stage `SequencePerformance` data that would allow a
  booster/upper-stage split is refreshed in flight **only while the player has one of two specific
  windows open**, so it holds stale editor data the rest of the time. It is total mass at the moment
  orbit was achieved, and both documents say so.
- **`coasting_class` is not called "no propulsion".** It means no *engine* was installed when the
  flight began. RCS thrusters, decoupler springs and docking-port pushoff all impart velocity, and
  the site says so (Task C2).

**Do not hardcode `"earth"`.** Where a challenge means "the home body", the honest server-side form
is a challenge-level `Body string` field on the definition, set by whoever writes the challenge, with
a comment that KSA's home body is `CelestialSystem.HomeBody` and is a property of the loaded system
rather than a constant. A challenge naming a body is a **curated key**, not a server allow-list —
that distinction is the whole of §3.7 and is worth a sentence in the file.

---

### Task G5 — store reads

**Files:** `server/internal/store/projections.go`.

```go
func (p *Projections) ChallengeLeaderboard(ctx context.Context, challenge string, asc bool, limit, offset int) ([]CareerStatRow, error)
func (p *Projections) ChallengeAhead(ctx context.Context, challenge string, value float64, seq int64, asc bool) (int64, error)
func (p *Projections) ChallengeEntrants(ctx context.Context, challenge string) (int64, error)
func (p *Projections) ChallengesForPlayer(ctx context.Context, playerID int64) ([]CareerStatRow, error)
```

Reuse `CareerStatRow` — the columns are identical and inventing a second identical struct is two
places to change a tag. Canonical ordering, same as every board.

---

## Phase H — challenges on the read API and both frontends

### Task H1 — the challenge endpoints

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
                "value": f, "context"?: {…}, "updated": unix_ms, "rewound"?: true}]}
```

- `state` is derived from **the server clock** (`s.deps.Now()`), never the browser's — the same rule
  `?period=` without `?at=` already follows.
- Ordered newest-window-first in the index, with open challenges before upcoming before closed.
- Rows go through the same ban filter and the same `Redact`; `save`/`save_id` appear only on
  career-scoped challenges.
- **A closed challenge is served exactly as an open one.** Its rows are the archive, and the archive
  is a feature.
- Cache headers as everywhere else. Note the index's `state` flips at a known instant and
  `s-maxage=30` means a CDN can be up to 30 seconds stale about it — which is fine and worth a
  comment, because somebody will notice.

---

### Task H2 — the challenge pages

**Files:** `templates/challenges.gohtml`, `templates/challenge.gohtml`; `templates.go`; `pages.go`;
`web.go`; `layout.gohtml`; `home.gohtml`; `web_test.go`; `docs/ui-design.md`.

```go
	mux.HandleFunc("GET /challenges", s.handleChallenges)
	mux.HandleFunc("GET /challenges/{challenge}", s.handleChallenge)
```

- **Nav gains `Challenges`.** With Badges from Task F2 that is seven header links, which is the most
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

### Task H3 — the docs-site half

**Files:** new `docs-site/src/data/challenges.ts`; new `docs-site/src/components/ChallengeDetail.astro`;
new `docs-site/src/content/docs/challenges/index.mdx` and `.../challenges/archive.mdx`;
`docs-site/astro.config.mjs`.

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

---

### Task H4 — seed and e2e for challenges

- **Seed:** define one challenge in the seed's own window whose demo events fall inside it, and one
  already closed, so `/challenges` renders two of its three groups and the archive is exercised.
  Because a challenge window is measured on `recv_time`, and the seed controls the injected clock
  (WP-CLOCK, PROJ-030), this is deterministic — **do not** define a seed challenge relative to
  `time.Now()`.
- **e2e:** `site/e2e/challenges.spec.ts` — the index groups by state, an open challenge ranks, a
  closed one still serves its rows, the home-page panel matches `/v1/challenges`, and values are read
  from `data-value`.

---

## Phase I — the documentation sweep and the release gate

Phases A–H each carry their own doc updates, in their own commits, because
Constitution §9 has no deferred form. **This phase is the sweep that proves nothing was missed**, and
the place the reasoning gets written down once rather than eight times.

---

### Task I1 — `docs/DECISIONS.md`

**The shadow-ban work took `PROJ-101`–`103`, `STORE-017`–`018`, `IDENT-016`–`018` and `OPS-035`, so
this plan starts at `PROJ-104`.** Look up the current highest number in each area before writing —
the numbers below are the intent, not a reservation. Each entry is dated, and each says **why**, not
only what; a decision without its reasoning gets re-litigated within the year, which is the entire
purpose of the file.

| Entry | Subject | Area |
|---|---|---|
| `PROJ-104` | A career is a scope — a dimension of a board, not a set of boards (§3.1) | PROJ |
| `PROJ-105` | Every board gets career scope, with no opt-out list (§3.2) | PROJ |
| `PROJ-106` | Career boards have no period dimension, and that is a refusal (§3.3) | PROJ |
| `PROJ-107` | A career board ranks (player, save) pairs; one player may hold several rows (§3.4) | PROJ |
| `PROJ-108` | A save publishes as an ordinal for humans and a relabelled id for machines (§3.5) | PROJ |
| `PROJ-109` | `career_body` / `career_kitten` are **siblings** rather than a widened primary key — forced by `PROJ-101`'s additive-only rule, and better for three reasons that would have applied anyway (§3.12) | PROJ |
| `PROJ-110` | `career_playtime` folds `sim_t` and never reads `career.max_sim_t`, because a state fold is already complete on a rebuild's second pass and the `updated_seq` would diverge (Task A6) | PROJ |
| `PROJ-111` | A badge is permanent, once-only, and projected at two scopes from one table (§3.6) | PROJ |
| `PROJ-112` | There is no "visited every planet" badge; tiers instead, and the build-5168 evidence (§3.7) | PROJ |
| `PROJ-113` | Badge families reuse `[boards] min_players` rather than gaining a knob (§3.14) | PROJ |
| `PROJ-114` | A challenge is a compile-time rule over an explicit window; not an admin API, not a DSL (§3.8) | PROJ |
| `PROJ-115` | A challenge window is `recv_time`, and the offline limitation is stated not engineered around (§3.9) | PROJ |
| `PROJ-116` | `challenge_stat` is its own table rather than a `player_stat_period` overload, because that table's retention **deletes** and a challenge's rows must outlive their week (Task G1) | PROJ |
| `PROJ-117` | Moderation needed no wiring for the five new tables, and exactly why — `STORE-018` made the exclusion structural (Task A9's finding) | PROJ |
| `PROJ-118` | *(only if A5.4 is taken)* The lifetime `distance_travelled` correction, and the `stats.BuildVersion` bump it carried | PROJ |
| `MOD-080` | Three payload fields and no more; what was derivable from `flight_state` instead (§3.10, §3.11) | MOD |
| `MOD-081` | `kitten.tumble.from` — the game's own state machine distinguishes a botched landing from a trip (Task C3) | MOD |
| `MOD-082` | Parts destroyed is not readable in KSA; `Parts.Count` at the RUD prefix is the honest proxy (Task D2) | MOD |
| `MOD-083` | D11 re-confirmed at source in 5168; the board is named for what actually happens (Task D4) | MOD |
| `UI-045` | Nav budget: seven header links, and what that costs the next feature (Task H2) | UI |
| `DOCS-005` | `badges.ts` and `challenges.ts` join `events.ts` and `boards.ts` under the DERIVED DATA rule | DOCS |

Look up the current highest number in each area before writing.

---

### Task I2 — `docs/event-details.md`

The single largest doc change, and the one the next reader will trust over the code.

- **Contents** — new sections for **Career scope**, **Badges** and **Challenges**.
- **The registry table** — three `ver` bumps, and the sentence *"Every type is at `ver` 1"* under
  **The registry** must be **rewritten, not left**. Same for `docs/events.md`'s heading
  *"23 types, every one at `ver: 1`"*.
- **Boards** — every new board's row, the new dynamic family, and an updated count in the section
  title (it says "The 40 fixed boards" today).
- **Fold detail, board by board** — an entry per new board.
- **The two projection tables** — becomes five. Document `career_stat`, `badge_award` and
  `challenge_stat` with their merge rules and their guards, in the same table shape.
- **State projections** — `career` gains `ordinal`; `career_body` and `career_kitten` are new and get
  entries of their own; `player_body` / `career_body` gain a third `kind`; `flight_state` gains
  `milestones` and the four fact columns. `player_body` and `kitten` are otherwise **unchanged** —
  say so, since a reader will expect them to have moved.
- **Suppression and eligibility matrix** — a row per new gate. This table is how a reader answers
  "why did my thing not count", and an ungated new board is a support question.
- **Rebuild ≠ incremental** — state explicitly that the new folds add **no** sixth divergence, and
  why for each: badges are once-only inserts keyed on first occurrence; challenge windows derive
  from `ev.RecvTime`; career scope derives from `ev.Career`; ordinals derive from `first_seq` order;
  `career_playtime` folds `sim_t` rather than reading a state table (Task A6). **If any of them does
  diverge, it goes in the numbered list instead — an honest sixth divergence beats a false claim.**
- **Conformance coverage** — the new vector lines and what each pins.
- **Known drift** — clear anything this work fixed; add anything it knowingly left.

---

### Task I3 — `docs/ROADMAP.md`

Under *Deliberately not built*, with reasoning:

1. **"Visited every planet" as a fixed list** (§3.7) — and a pointer to Task E7 as the version that
   would be honest, marked as the owner's call.
2. **A `kittens_scuttled` board** (Task D4) — Constitution §8's consequence test.
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

Under *Open, unblocked*, add the three in-game verification items Phase C creates:
`engine_count` is not 0 on a rocket with engines; `part_count` on a RUD matches the vehicle;
`kitten.tumble.from` reads `airborne` on a botched landing and `grounded` on a trip. Add them to the
§1 checklist in the same style as the existing rows.

---

### Task I4 — everything else

| Document | Change |
|---|---|
| `docs/events.md` | Three payload rows, three `ver` bumps, the `from` vocabulary as an **open set**, and the "every one at `ver: 1`" heading |
| `docs/ingest-api.md` | §4.8: `?scope=`, `scopes` on the index, the two saves endpoints, four badge endpoints, two challenge endpoints, and the read API `ver` bump |
| `docs/server.md` | Its **§5.6**: the new folds and the five new projection tables, added to the projections-table list there; its **§5.4**: the new DDL; its **§5.3** if any config key was added. (Those are `server.md`'s own section numbers, not this plan's.) |
| `docs/mod.md` | §7.2 detection rules for `kitten.tumble.from`; §7.4 for the two new reads |
| `docs/ksa-integration.md` | A section for `EngineController` enumeration and one for `LocomotionMode`, each with `file:line` against build 2026.8.5.5168 and a `[KsaAnchor]` risk rating |
| `docs/ui-design.md` | §5.1 page inventory (six new pages), §11 do-not-break DOM ids, the nav budget note |
| `docs/ARCHITECTURE.md` | Only if a new top-level directory appears. **Never mint a new `§` number.** |
| `DEVELOPMENT.md` | Any new Make target or test mode |
| `README.md` | Badges and challenges are things a visitor to the website notices |
| `docs/integrity-audit.md` | **Only if** something here turns out to be an integrity check. Nothing in this plan is; if that changes, it goes here against Constitution §8's five tests |

---

### Task I5 — the release gate

Run, in order, and paste the actual output into the final commit message or the PR body — not a
summary of it:

```
make test              # Go + C#, everything
make e2e               # the Playwright suite against a real server
cd docs-site && pnpm check     # oxlint + oxfmt + astro build
make testvectors && git diff --exit-code contracts/testdata   # vectors are current
```

Then, by hand:

1. **Force a rebuild and diff it.** `catlogctl rebuild` (it watches; `-detach` returns immediately,
   `-status` reports phase and progress) on a database with a representative history, then diff the
   rebuilt projections against the incremental ones. `TestRebuildEqualsIncremental…` covers this in
   miniature; do it once for real. **Note the verb changed** — `POST /admin/projections/rebuild` now
   answers `202` with a job rather than blocking, and `{"wait": true}` is the blocking form for
   scripts (`PROJ-103`).
2. **Prove the build stamp did its job.** Deploy this work onto a database built by the *previous*
   binary and confirm, without touching anything: `catlogctl stats` reports
   `projector.build.stale` and `projector.rebuild.suspended`; the old boards keep answering reads;
   the new boards read **empty** rather than short-of-history; and once `auto_rebuild` finishes,
   every new board is full and `stale` is false. This is the mechanism `PROJ-101` exists for and
   this plan leans on it in every phase — verify it once rather than assuming it.
3. Open every new page at a viewport of 1280×900 **and** at 380 px, and confirm no page scrolls
   horizontally — wide tables scroll inside their `.table-wrap`, never the body (UI-042).
4. Open `/boards/landings?scope=career` with **no data** and confirm the empty state, not a broken
   table.
5. Confirm no public JSON response anywhere contains a raw 16-character career id (Task B3's test
   does this; look at one response by eye as well).
6. Re-read `CLAUDE.md`'s update table and tick every row this work touched.

---

## Appendix A — the new boards, in full

Everything here goes in `stats.fixedBoards` (append at the end so existing display order is
untouched), in `docs/event-details.md`'s Boards table, and in `docs-site/src/data/boards.ts`.
`Career` in this table means the existing `Board.Career` flag — *the value is a career-relative
time* — not the new scope, which every board has.

| key | Title | Unit | Asc | Career | Fold | Source | Gate |
|---|---|---|---|---|---|---|---|
| `career_playtime` | Longest Save | `ms` | no | **yes** | record | any event with `career` + `sim_t` | `sim_t > 0`. **No flag gate** — a duration is not a feat |
| `play_sessions` | Times Resumed | `sessions` | no | no | count | `session.started` | — |
| `botched_landings` | Did Not Land On Their Feet | `tumbles` | no | no | count | `kitten.tumble` | `from == "airborne"`, `scoreable` |
| `parts_lost` | Parts Lost To Explosions | `parts` | no | no | count (+`part_count`) | `vehicle.rud` | `part_count > 0`, `scoreable` |
| `biggest_parts_lost` | Biggest Vehicle Lost | `parts` | no | no | record | `vehicle.rud` | `part_count > 0`, `scoreable` |
| `kittens_to_orbit_and_back` | Kittens To Orbit And Home | `kittens` | no | no | count (set-backed) | `flight.ended` | `reason == "recovered"`, `milestones & orbit`, `scoreable` |
| `biggest_crew_wreck` | Most Kittens Aboard A Lost Vehicle | `kittens` | no | no | record | `vehicle.rud` | `crew_count >= 1`, `scoreable` |
| `kittens_wrecked` | Kittens Walked Away From A Wreck | `kittens` | no | no | count (+`crew_count`) | `vehicle.rud` | `crew_count >= 1`, `scoreable` |
| `bodies_by_1y` | Worlds In The First Year | `bodies` | no | no | record (of a count) | `vehicle.soi` | `sim_t < 31 536 000`, career + clock, `scoreable` |
| `bodies_by_10y` | Worlds In Ten Years | `bodies` | no | no | record (of a count) | `vehicle.soi` | `sim_t < 315 360 000`, career + clock, `scoreable` |

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

Thirty badges. **Shape** is one of the four in Task E4. Every predicate below is satisfiable from
events already on the wire plus Phase C's three fields.

Every flight-bearing badge calls `scoreable` first — that is not repeated per row.

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
| `crewed_orbit` | Passengers | composite | `vehicle.orbit achieved` on a flight whose `flight_state.crew >= 1` |
| `orbit_and_back` | Round Trip | composite | `flight.ended reason=recovered` on a flight with `milestones & MilestoneOrbit` |
| `docked_in_orbit` | Rendezvous | composite | `vehicle.docked` on a flight with `milestones & MilestoneOrbit` |
| `coaster` | Along For The Ride | composite | `vehicle.soi` on a flight whose `flight_state.engine_count == 0` |
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
| `reached_<body>` | `"Reached " + titleize(body)` | **family** | `vehicle.soi` `to_body` |
| `orbited_<body>` | `"Orbited " + titleize(body)` | **family** | `vehicle.orbit` `phase == "achieved"`, `body` |
| `landed_on_<body>` | `"Landed on " + titleize(body)` | **family** | `vehicle.landed` `survived`, `body` |

**The three tiers are the answer to "visited every planet", and the reason is §3.7.** A higher tier
never removes a lower one; they are all held at once.

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

Dates below are placeholders — **the owner sets the real ones**. They are written as UTC instants and
committed as unix-millisecond literals with the human date in a trailing comment, so a reader can
check them:

```go
const (
	// 2026-08-10T00:00:00Z .. 2026-08-17T00:00:00Z
	week33Opens  int64 = 1_786_579_200_000
	week33Closes int64 = 1_787_184_000_000
)
```

| key | Title | Kind | Scope | Unit | Asc | The rule |
|---|---|---|---|---|---|---|
| `heavy_lift_week` | Heavy Lift Week | record | player | `kg` | no | The heaviest vehicle to reach orbit, total mass at the moment orbit was achieved |
| `speedrun_orbit` | From Scratch To Orbit | best | **career** | `ms` | **yes** | The shortest career time at which a save reached orbit |
| `tumbleweek` | Tumbleweek | count | player | `tumbles` | no | The most kitten tumbles |
| `coasting_class` | Coasting Class | record | player | `bodies` | no | The most worlds reached on flights that launched with no engine installed |
| `feather_touch` | Feather Touch | best | player | `m/s` | **yes** | The gentlest surviving landing away from home |
| `full_house` | Full House | record | player | `kittens` | no | The most kittens brought home in one piece at once |

A worked example, complete enough to copy:

```go
// heavy_lift_week — the heaviest thing anybody puts in orbit this week.
//
// NOT called "payload", and that is deliberate: KSA has no payload concept, and
// the per-stage data that would allow a booster/upper-stage split is refreshed in
// flight only while the player has one of two specific windows open, so it holds
// stale editor data the rest of the time. This is total mass at the instant orbit
// was achieved, and both documents say so.
{
	Key: "heavy_lift_week", Title: "Heavy Lift Week",
	Blurb: "Put the heaviest thing you can into orbit. The number is what the whole " +
		"vehicle weighed the moment it got there, propellant included.",
	Opens: week33Opens, Closes: week33Closes,
	Unit: "kg", Ascending: false, Scope: ScopePlayer,
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

`coasting_class` is the one that needs `flight_state`: read the flight, require
`engine_count != nil && *engine_count == 0`, then count distinct `to_body` values inside the window
using the `career_body` set shape — **not** a Go set held in the fold, which a rebuild is not
obliged to reproduce in the same order.

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
| Orbital period | `Orbit.Period`, `SemiMajorAxis`, `LongitudeOfAscendingNode`, `ArgumentOfPeriapsis` | `Period` is unread today — "longest orbital period" |
| Δv actually expended | `KinematicMeasurements.DeltaVelocityCci`, integrated | The **honest** Δv metric, unlike `NavBallData.DeltaV` |
| Target / rendezvous | `Vehicle.Target`, `TargetPart` | "Closest approach" |
| Control point | `Vehicle.ControlPart`, `Ctrl2Body` | New in 5168 and **mandatory** to normalise any attitude metric, since the player can move the control frame |
| Recoverability | `Vehicle.CanRecover` | One line for "is this a recoverable landing", which catlog re-derives today |

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
| Payload vs booster | **No payload concept.** `SequencePerformance` has the split and is UI-gated (see Task I3) |
| Δv as a vehicle statistic | `NavBallData.DeltaV` is the **active stage only** and is refreshed only while one of two windows is open |
| EVA distance walked | No EVA-only counter. `KittenRosterEntryData.TravelledMeters` is credited for **every** vehicle a kitten rides, in the **ecliptic** frame — dominated by heliocentric motion. Never present it as distance walked |

---

## Appendix E — checklist: adding an event type or a payload field

Derived from a full survey of the mod. **Follow it in order.** Steps marked ★ are the ones whose
omission fails silently rather than loudly.

### A new payload field on an existing type

1. `mod/catlog.lib/Events/EventTypes.cs` — **bump `Versions[type]`** ★ (Constitution §9).
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
11. ★ `server/internal/projector/upcast.go` — `currentVer` **and** an upcaster for every intermediate
    version. **A `ver` the server does not know is skipped as a future version — silent data loss for
    that type until the server catches up and a rebuild runs.**
12. `server/internal/stats/payload.go` — the Go struct field and its `json:` tag.
13. `server/internal/testvectors/testvectors.go` + `make testvectors` — and for an **optional** field,
    carry it **present on one line and absent on another** ★, which is what makes
    `Batch001_PayloadsRoundTripThroughTheirRecords` prove anything.
14. Docs: `docs/events.md`, `docs/event-details.md`, `docs-site/src/data/events.ts`, the family page,
    `docs/DECISIONS.md`.

### A new event type — everything above, plus

15. `EventTypes.cs` — the `public const string`, the `Versions` entry, and `AlwaysReported` **only**
    if its absence would make a number *better* than it was.
16. `EventTypes.KindOf` — a new type is `KindEvent` and therefore **never pruned from the outbox**.
    Only `telemetry.window` is `KindPassive`. Consider the outbox pressure before adding a chatty
    type.
17. `mod/catlog.lib/Config/ModConfig.cs` — a line in the commented `[events]` block in the `Header`
    const, with the boards it feeds, and `# locked on` iff it is always-reported. ★ **Test-enforced**
    by `TheHeaderDocumentsEveryRegisteredEventType`.
18. `mod/catlog.lib.tests/Config/EventGateTests.cs` — only if `AlwaysReported` changed; it pins the
    list to five names and the header sentence *"Five types cannot be switched off"* is asserted
    verbatim.
19. `mod/catlog.lib.tests/Conformance/ContractVectorTests.cs` — the `PayloadTypes` row ★, or
    `Batch001_PayloadsRoundTripThroughTheirRecords` throws `KeyNotFoundException`.
20. `EventTypesTests.RegistryHasExactlyTheLaunchSet` — the count (23 today).
21. ★ `server/internal/ingest/types.go` — `knownTypes`. **Without it the whole batch is rejected
    `400 malformed_batch`.**
22. `server/internal/stats/payload.go` — a `case` in `decodePayload`.
23. `docs/event-details.md` — a new `##` section with all eight blocks, **and** a row in the registry
    table whose header currently says "23 names".

**Line budget:** `Wire.MaxEventLineBytes` is 16 KiB and a single line over the cap wedges the outbox
behind it. `Wire.MaxEventsPerBatch` is 2000.

---

## Appendix F — checklist: adding a board

0. **Is it a new fold, or a changed one?** A new fold changes `stats.BuildID` for free and the
   projection rebuilds itself on deploy. **A change to an existing fold's *meaning* under an
   unchanged name does not** — that one owes a `stats.BuildVersion` bump in the same commit
   (`PROJ-102`, §5.1).
1. `server/internal/stats/boards.go` — a `StatXxx` constant, a `Board` row in `fixedBoards`
   (**append**, so existing display order is untouched), and the fold type.
2. `server/internal/stats/fold.go` — one entry in `BoardFolds()`, in board-metadata order.
   **Order is part of `BuildID`**, so moving an existing entry is a real change, not a tidy-up.
3. Choose the write helper: `putRecord` (max) · `putBest` (min) · `addCount` (counter) · `setValue` +
   `setCareerValue` (derived total). **Do not write to the batch directly** — the helpers are what
   give the board its rolling windows and its career scope for free.
4. Gate it: `scoreable` for anything flight-bearing, plus a `> 0` value gate on any reading that
   decodes to `0` when the mod could not read it (PROJ-088, PROJ-094).
5. Tests in `stats/`: golden value + `updated_seq` + **byte-exact `Context`**; the tie rule; the flag
   exclusion; rebuild-equals-incremental; and a `Describe` metadata assertion.
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
11. **If the board needs a new projection table**: its migration must be **additive only** — no
    `DROP`, no recreation (§5.2, `PROJ-101`) — and the table must be added to **all three**
    hand-maintained lists in §5.4 (`projector_test.go`'s `snapshot`, `store_test.go`'s expected DDL,
    and `store/projections.go`'s `Counts`). The first is silent when forgotten.

---

## Appendix G — sequencing, and what can run in parallel

```
A (career core) ──┬──> B (career surfaces)
                  │
                  ├──> E (badges server) ──> F (badge surfaces)
                  │
                  └──> G (challenges server) ──> H (challenge surfaces)

C (wire v2) ──> D (new boards) ──┘   (D's boards inherit career scope from A)

                                         all ──> I (docs sweep + release gate)
```

- **A is the only hard prerequisite.** Everything else assumes `career_stat` and the scope
  vocabulary exist.
- **Every phase's deploy rebuilds itself.** Each one adds folds, which changes `stats.BuildID`, which
  suspends the fold loop and runs a rebuild (`auto_rebuild`, on by default). So a phase can ship
  without an operator step — but expect the boards to be **stale for the length of the rebuild** and
  the phase's own new boards to read **empty** until it lands. That is the designed behaviour
  (`PROJ-101`), not a bug, and it is worth saying in the release notes for each phase.
- **C is independent of A** and can start immediately in parallel — it touches `mod/` plus
  `projector/upcast.go` and `stats/payload.go`, none of which Phase A edits.
- **D needs C** (two of its boards read the new fields) and **should land after A** so its boards get
  career scope without a second pass.
- **E/G are independent of each other** and both only need A. F/H likewise.
- **B, F and H all edit `layout.gohtml`, `templates.go`, `web.go` and `docs/ui-design.md`.** Run them
  in series, or expect conflicts in exactly those four files.
- **I is last**, and it is a real phase with real work, not a formality.

**Before every phase:** rebase on `main`, re-read §5, and re-check
`ls server/internal/store/migrations/projections/` for the next free migration number.

**One commit per task** is the right granularity, each with its doc updates in it. A task that
touches an event, a payload field, a fold, a board, an eligibility rule or a unit and updates only
`docs/event-details.md` **or** only `docs-site/` is an incomplete change and should not be committed.
