# Integrity audit

**Date:** 2026-08-07 · **Against:** [CONSTITUTION.md](CONSTITUTION.md) §8 (anti-cheat
proportionality) · **Scope:** every integrity mechanism that exists in code today, plus the ones
that are proposed and not built.

**Amended 2026-08-09** for M10, the `[events]` table's locked types — the only mechanism added
since. **This audit changed no code.** Everything below classified *Borderline* or *Too far* is flagged
for the owner's decision, per the principle's own instruction.

## Headline

**Nothing in catlog today is "too far".** The implemented anti-cheat surface is, in total, roughly
150 lines: five flags raised at the exact game call sites that cheat, one bitfield, one `WHERE
flags = 0` guard, and the fact that every board is folded from events rather than submitted. That
is a proportionate amount of machinery for a hobby leaderboard, and the ratio is right — the most
valuable mechanism (§6 of the constitution: boards are derived, never claimed) costs nothing,
because it falls out of event sourcing.

Three items are flagged as **Borderline**, and only one of them is really about the code:

1. **The stream hash chain** (`sid`/`seq`/`ph`) — the code is fine and should stay, but the
   *claim* the plan makes for it is not delivered by any line in the repo. Docs fix, not a code
   fix.
2. **The rebuild's ±2 s KIA window** — cheap, but its trigger almost never fires and it now
   overlaps a mod-side check that arrived later.
3. **`peak_g_survived`'s recovered-flight requirement** — not really an anti-cheat rule at all,
   but it is the one board whose number can legitimately drop overnight, which will eventually be
   mistaken for one.

The larger finding is forward-looking: `the original proposals (now removed; see [DECISIONS.md](DECISIONS.md))` §4.3 proposes a **six-layer**
anti-forgery scheme. Two layers are built (and are the good ones). **Four are not, and under this
principle they should never be.** That is worth settling now, before someone reads the proposals
document as a to-do list.

---

## Verdict table

| # | Mechanism | Where | Verdict | Recommendation |
|---|---|---|---|---|
| **Mod side** ||||
| M1 | `flight.flagged: teleport` — prefix on `InputEvents.TeleportInputData.Apply` | `mod/catlog/Patcher.cs:240`, `:629` | **In scope** | Keep |
| M2 | `flight.flagged: refuel` / `resource_edit` — prefixes on `Vehicle.RefillConsumables` / `DepleteConsumables` | `mod/catlog/Patcher.cs:250`, `:636` | **In scope** | Keep |
| M3 | `flight.flagged: console` — prefix on the terminal `destroy` command | `mod/catlog/Patcher.cs:263`, `:642` | **In scope** | Keep |
| M4 | `flight.flagged: tuning` — `TumbleSpeedGate != 6.5f` | `mod/catlog/PolledSignals.cs:232`, `mod/catlog/VehicleTelemetry.cs:34`, `:522` | **In scope** | Keep — this is the principle's own worked example |
| M5 | Session-wide flags replayed onto flights started later | `mod/catlog.lib/Detect/EventPipeline.cs:319`, `:332` | **In scope** | Keep |
| M6 | Flag dedup per `(flight, flag)` | `mod/catlog.lib/Detect/FlightTracker.cs:110` | **In scope** | Keep |
| M7 | Impacts inside the game's 5 s post-teleport FX-suppression window are dropped | `mod/catlog/Patcher.cs:394`, `:426`; `VehicleTelemetry.cs:777` | **In scope** | Keep |
| M8 | `vehicle.impact.survived` frame correlation, incl. manual-destroy flipping the verdict | `mod/catlog.lib/Detect/ImpactCorrelator.cs` (170 lines) | **In scope** | Keep |
| M9 | NaN/Inf scrubbing on every game read | `mod/catlog/VehicleTelemetry.cs` (`Sanitize.Finite`, ~20 call sites) | **In scope** | Keep |
| M10 | `catlog.toml`'s `[events]` table refuses to switch off `session.started`, `flight.started`, `flight.ended`, `flight.flagged`, `kitten.kia` (2026-08-09) | `mod/catlog.lib/Events/EventTypes.cs` (`AlwaysReported`), `Config/ModConfig.cs` (`NormalizeEvents`), `Events/EventTypeFilter.cs` (`Create`) | **In scope** | Keep |
| **Server side** ||||
| S1 | `flight_state.flags` and the `flags = 0` exclusion applied by every fold | `server/internal/stats/flight.go:99`, `server/internal/stats/fold.go:159` | **In scope** | Keep — highest value per line in the repo |
| S2 | `FlagOther` (bit 5): an unrecognised flag value excludes the flight | `server/internal/stats/flight.go:32`, `:37` | **In scope** | Keep |
| S3 | Boards derived from events; fixed stat keys are constants; a data-driven board (`fastest_to_<body>`, `rud_<cause>`) is listed only once ≥2 distinct players are on it | `server/internal/stats/boards.go` (`fixedBoards`, `statSuffix`, `Catalog`) | **In scope** | Keep — the allow-lists this row used to cite are gone; see below |
| S4 | `seq` is a server-local rowid; `wall_t` is untrusted and the feed uses `recv_time` | `server/internal/stats/event.go:15` | **In scope** | Keep |
| S5 | Rebuild pass 1/2 — a late `flight.flagged` retroactively unscoring its flight | `server/internal/projector/rebuild.go:71`, `:139`, `:169` | **In scope** | Keep |
| S6 | `/admin/stats` reports `flagged_flights` | `server/internal/store/projections.go:280` | **In scope** | Keep |
| S7 | Rebuild refinement — ±2 s `kitten.kia` window on `biggest_lithobrake_survived` | `server/internal/stats/fold.go:44`, `flight.go:183`, `boards.go:139` | **Borderline** | Keep as-is; see F2 |
| S8 | Rebuild refinement — `peak_g_survived` requires `ended_reason == 'recovered'` | `server/internal/stats/boards.go:181` | **Borderline** | Keep; document the overnight change; see F3 |
| S9 | Stream hash chain `sid`/`seq`/`ph`, `409 stream_fork`, sticky `gap` | `server/internal/authz/authz.go:425`, `server/internal/ingest/writer.go:174`, `server/internal/store/events.go:559` | **Borderline** | Keep the code, fix the claim; see F1 |

Nothing classified **Too far**.

---

## In scope — the detail worth having

These need no decision. They are recorded so the next person does not have to re-derive why each
one is fine.

**The five flags (M1–M4, S1).** Every one is a Harmony hook on the exact game call the player used
to cheat, or a single comparison against a value KSA ships. None of them infers anything. The
teleport hook in particular is a good example of the principle working: `Vehicle.Teleport` was
rejected as a target *because* normal EVA egress and editor decouples call it, i.e. the naive
version would have failed the honest-player test and quietly excluded ordinary play from every
board (`DECISIONS.md`, WP8). The chosen target, `TeleportInputData.Apply`, has exactly two
producers and both are player commands.

**The `tuning` flag (M4).** Confirming the owner's prior: **in scope**, unambiguously. It is one
float equality against `6.5f`, held in one named constant with a `[KsaAnchor]` on the reader, and
it exists because the game itself ships a debug window that live-edits the sole classifier for
`kitten.tumble`. "Assume KSA game default settings" is precisely what this is; without it the
tumble board is not hard to forge, it is trivial. The session-wide taint (M5) is the correct scope
for it, since the edited value is a process-global.

**The flag exclusion covering every fold (S1).** §5.6 of the plan only asked for it on *record*
boards. Applying it to counters as well is what makes the `tuning` flag mean anything at all —
`kitten_tumbles` is a counter. This is 15 lines (`scoreable`) and it is the mechanism the whole
scheme rests on.

**`FlagOther` (S2).** One `default:` branch: a flag value this server build does not recognise sets
bit 5 and excludes the flight. Fail-closed, and it means a newer mod is never a scoring loophole
while the server catches up. Two lines.

**Boards are derived (S3, S4).** This was layer 1 of the original six-layer integrity proposal, and it is the layer that was implemented — correctly. Four of the other five are settled as never-to-be-built ([ROADMAP.md](ROADMAP.md)).
Every *value* is computed by folding events and none is ever accepted as a submitted stat; `seq` is
the events.db rowid, tie-breaks use it, and the feed timestamps with `recv_time` rather than the
client's `wall_t`. This costs nothing extra — it is just what event sourcing looks like — and it
is doing more real work than every flag combined.

The one thing that has changed since this audit was first written is **which stat keys exist**. Two
board families, `fastest_to_<body>` and `rud_<cause>`, used to be gated by compiled-in lists — the
eleven stock KSA bodies, the six shipped RUD causes — on the reasoning that a key built from client
text would let anyone mint a leaderboard. That reasoning was half right and the remedy was wrong.
KSA's celestial systems are hand-authored content that mods extend, `docs/events.md` has always
said `body` is opaque to the server, and a compiled-in list denies a board to whoever gets somewhere
new *silently*. The lists are gone. What replaced them is one clause: such a board is **listed** by
`GET /v1/leaderboards` once at least **2 distinct players** hold a value on it.

Against the five-part test that clause passes cleanly. It compares against nothing about the game
(1); it is one sentence and one comparison in one place (2); it adds no table, no stage, no job and
no accumulated state — the count it reads is the one the index query already computes (3); it
cannot punish an honest player, because their value is recorded either way and appears on their own
profile and at the board's own URL regardless (4); and its only effect is on what a public list
contains (5). It is also correct on its own merits, independent of abuse: a leaderboard with one
entrant is not a leaderboard. Lowering or raising it publishes or unpublishes history that is
already in the projection, so it is a tunable rather than a data decision.

**The impact correlator (M8).** The largest single piece of code in this audit at 170 lines, and
still in scope. It is not pattern-hunting: it is the *definition* of `survived`, derived from
verified game frame ordering (impacts are applied before physics destructions; manual destroys land
later, in the input-apply pass). It also closed a genuine free-record hole — before the WP8 fix, a
player could scuttle after any hard landing and bank a "survived" record — with a one-line call,
not with a heuristic.

**Post-teleport impact suppression (M7).** Reads the game's own `Vehicle.IsImpactFxSuppressed()`
predicate. This is the cheapest possible form of the principle: the game already knows the answer,
so ask it.

**The `[events]` locked list (M10, added 2026-08-09).** The mod now lets a player switch individual
event types off in `catlog.toml`, which is a **new player-controlled surface that can suppress
events** and therefore belongs in this audit. Five types refuse the setting, and a `false` on any of
them is dropped with a warning naming the key.

Against the five-part test, taking the refusal itself as the mechanism under audit:

1. **Stock-data test.** It compares against catlog's own wire contract and nothing else — a set of
   five type names from the registry, tested with a set membership. It models nothing about what a
   player ought to be able to achieve, and reads no game value at all.
2. **One-look test.** One `HashSet` with the reason for each of the five written beside it, read by
   both enforcement points and by the test that asserts the list is exactly those five.
3. **No-new-machinery test.** No table, no stage, no job, no accumulated state. It is a membership
   test on a string, evaluated per envelope.
4. **Honest-player test.** An honest player cannot trip it, because tripping it requires typing
   `"flight.flagged" = false` into a file — and even then the consequence is a log line and the mod
   continuing to behave the way a stock install does. Nothing is excluded and nothing is scored
   differently.
5. **Consequence test.** Its only effect is that a setting is not applied. It queues no work for a
   human, scores no suspicion, and treats no player differently because of history.

**The distinction that matters here, stated so it is not lost: this is a refusal to accept a
configuration, not an inference about a player.** Nothing about the person is being judged, guessed
at, or recorded. The mod does not notice that the key was present, does not report it, does not flag
the flight and does not remember it next session; the file is rewritten without the key and the log
says why. That is the same category as clamping `ship_interval_s` up to the floor (MOD-065) — the
mod declining to do something on the player's behalf — and it is emphatically not the category §8
governs, which is machinery that tries to infer cheating from data.

**The suppression point is late on purpose, and that is an integrity property.** The filter is
applied at `EventPipeline.Add`, after the detector, the flight tracker, the impact correlator and the
window accumulator have all advanced. Dropping an envelope therefore rewinds no state, so a type a
player switched off cannot change what the *other* types say — `vehicle.rud` off does not make a
fatal impact report `survived: true`, which is pinned by a test. Filtering earlier, in the dispatch
switch, would have taken that bookkeeping with it and made suppression of one type quietly corrupt
another.

**`vehicle.rud` was considered for the locked list and left out.** Switching it off hides how often a
player exploded; it cannot make any number better than it was, because the `survived` verdict is
computed before the filter runs. Locking it would have been machinery bought for vanity rather than
for integrity, which is what §8 exists to refuse. Recorded in MOD-072 and in
[ROADMAP.md](ROADMAP.md).

---

## Flagged for the owner's decision

### F1 — The stream hash chain sells more than it delivers (S9) · **Borderline**

**What it does.** Every batch's proof JWS carries `sid` (a stream ULID), `seq` (1-based, per
stream) and `ph` (SHA-256 of the previous batch's body). The server requires `seq == 1` with no
`ph` for a new stream, requires `ph == last_bh` for the next batch, answers `409 stream_fork` if a
`seq` is reused, and accepts a skipped `seq` while setting a sticky `gap` flag on the stream row.

**What it costs.** Small: ~30 lines in `ingest/writer.go`, four structural checks in
`authz/proofClaims`, one table with six columns, plus the mod's matching state. It is also locked
by D5, written into `docs/ingest-api.md`, mirrored in `catlog.lib`, and pinned by the
cross-language conformance vectors in `contracts/testdata/`.

**What it actually buys.** Less than the plan claims, on three counts:

- **It is not load-bearing for dedup.** Idempotency comes from `(player, event_id)` unique index
  and the `(player, jti)` batch replay short-circuit at step 11. Both work with the chain deleted.
- **It is not load-bearing for ordering.** The projector's cursor is the server-local `seq` rowid.
  Batch `seq` orders nothing downstream.
- **It is not a credential-theft signal in practice.** `the original proposals (now removed; see [DECISIONS.md](DECISIONS.md)):234` describes a
  fork as "a high-signal indicator of credential theft". But a fork is not recorded anywhere — no
  row, no log line, no per-player counter — and the mod's *documented* recovery (§4.5.3) is to mint
  a fresh `sid` and reset `seq = 1`. So a thief pays one 409 and continues. The `gap` column is
  worse: it is written by the ingest writer and **read by nothing in the entire codebase** — no
  admin surface, no log, no query. It is write-only forensics for a forensic process that does not
  exist.

What it *does* buy, honestly stated: it makes a batch reordered or replayed out of position a loud
409 instead of a silent accept, which is a real (if minor) protocol-hygiene property, and it makes
the ingest stream self-describing enough to debug a confused outbox.

**Recommendation: keep the code, fix the claim.** Removing it would mean amending a locked
decision, changing the wire contract, regenerating the conformance vectors and touching the mod —
a large change to delete something cheap. But it should be described as what it is (ordering
hygiene and debuggability) and not as tamper-evidence or theft detection, because that framing is
what would justify building the layers in F5 on top of it. Two concrete follow-ups for the owner
to pick from, both optional:

- *(docs, recommended)* Reword the `seq`/`ph` rationale in `docs/ingest-api.md` to drop
  "tamper-evident" and "credential theft".
- *(code, owner's call)* Either surface `gap` in `/admin/stats` beside `flagged_flights`, or drop
  the column at the next schema change. A column nothing reads is a promise nothing keeps.

### F2 — The ±2 s KIA window is nearly a no-op, and now overlaps a mod-side check (S7) · **Borderline**

**What it does.** During a rebuild, pass 1 indexes every `kitten.kia` by `(flight, sim_t)`; pass 2
refuses a `biggest_lithobrake_survived` record if a KIA landed within ±2 s of the impact. It is
§4.2's stated crew-survival rule.

**What it costs.** Genuinely small — an `if` in pass 1, a linear scan in `Flights.KIANear`, an
in-memory `map[flight][]float64`, and a `RebuildResult.KIAFlights` counter. The two-pass rebuild it
rides on is needed anyway, for late-flag healing (S5) and for `peak_g` (S8).

**What it actually buys.** Very little, for a reason the project itself discovered after the rule
was written: `Kia = true` is set in exactly one place in the game, reachable only from
`Vehicle.KillCrew()`, whose sole caller is the player-initiated destroy path (`DECISIONS.md`,
2026-08-06). So `kitten.kia` is a *deliberate scuttling* signal, not an impact-fatality signal —
the project's own note says the check "almost never fires". And the scuttle-after-a-lithobrake case
it was meant to catch is now caught earlier and better by the impact correlator (M8), which flips
`survived` to false for a manual destroy in the impact's own frame or the next.

The residual gap it still covers is narrow: a player who survives an impact, keeps flying, and
scuttles between roughly one frame and two seconds later.

**Recommendation: keep it.** It is cheap, it is the documented §4.2 contract, and it will become
meaningful again if a future KSA build ever kills crew on a physics RUD (D11 is still marked
`BEST-GUESS` pending in-game verification). Flagged only so the owner knows it is the redundant
half of a pair, and so that if the rebuild ever needs simplifying this is the first thing to
delete. **Do not** extend it — widening the window, or making it consider kittens on other
flights, would fail the honest-player test.

### F3 — `peak_g_survived` means two different things before and after a rebuild (S8) · **Borderline**

**What it does.** Incrementally, the board takes the max `peak_g` over any window of an unflagged
flight. At rebuild, it additionally requires the flight to have ended `recovered`.

**What it costs.** One line of code. The real cost is conceptual: the incremental and rebuilt
answers deliberately disagree, so a player's `peak_g_survived` can *drop* after the nightly
rebuild, with no cheating involved and no explanation on the page. Every other board is monotone.

**What it actually buys.** The board means "G you walked away from" rather than "G you
experienced", which is the more interesting record and the one §5.6 specifies.

**Recommendation: keep it, and note it where it shows.** This is a board *definition*, not an
anti-cheat rule, and it is not what the principle is aimed at — flagged because it is the one place
in the system where a leaderboard number legitimately goes down overnight, and the first support
question about it will be "did someone cheat?". Cheapest fix if it ever bites: score `peak_g` only
at `flight.ended`, so both paths agree. That is a code change and is the owner's call, not this
audit's.

---

### F4 — The career rewind mark is a fact about the clock, not an accusation (2026-08-07) · **Borderline**

**What it does.** `careerFold` keeps `career.max_sim_t` per `(player, career)` and sets
`career.rewound` when a `session.started` for that career arrives with a lower `sim_t`. That is
exactly "an earlier save of this career was loaded". The read API surfaces it as
`"rewound": true` on that career's rows on the career-time boards.

**Against the five-part test, honestly.** It passes 1 (it compares against catlog's own wire
contract — KSA's own clock is monotone within a save unless an earlier one is loaded, verified in
`docs/ksa-integration.md` §5b), 2 (one sentence, one place), and 5 (its only effect is a boolean
next to a number: nothing is excluded, nothing is scored, no human is queued, no history
accumulates against a *player*). It fails 3 on a literal reading — it adds a table and a
high-water mark that accumulates across events — and 4 on a literal reading, because an honest
player who reloads an earlier save does set it.

**Why it is here rather than deleted.** §8 governs *integrity checks*: mechanisms that try to
infer cheating. This one does not try, and could not — save-scumming and ordinary reloading are the
same event and catlog says so in `docs/events.md` in as many words. What it does is qualify a
derived number: "seconds from game start to orbit" only means anything if the clock ran forward,
so when it did not, the board says so. It is the same category as `telemetry.window.peak_g` being
*absent* rather than zero — provenance on a measurement, not suspicion about a person. Note also
that the *career grouping* it sits on is what stops the mark firing on an honest player with two
saves; without it, every save switch would look like a rewind, and rule 4 would be failed for
real.

**Recommendation: keep it, and delete it if the owner disagrees.** Removing it is a small,
self-contained change — drop `rewound` from the `career` table and the two read-API sites, and the
boards keep working unchanged. The career grouping itself is load-bearing and must stay: without
it the career-time boards cannot be defined at all.

---

## Not governed by this principle

Listed explicitly so a future reader does not cite §8 as a reason to delete them. None of these
exist to stop stat manipulation.

| Mechanism | Exists for |
|---|---|
| License + proof JWS chain, cheap-first verification order, `Verifier.Stats()` counters | Authentication and DoS resistance — an unauthenticated stranger must not be able to buy an ECDSA verify with garbage |
| Body size caps, decompression cap, max events/batch, strict NDJSON framing, known-type allow-list | Protocol hygiene; turns silent truncation into a loud rejection |
| The shape a body name or a RUD cause must have to become a stat key (`[a-z0-9]` then `[a-z0-9._-]`, ≤40 chars) | Protocol hygiene; a stat key is a URL path segment. It is not a judgement about which bodies are real — a name that fails it keeps every other consequence it had |
| Token bucket per `jkt`, nginx `limit_req`/`limit_conn` zones | Rate limiting / DoS |
| Deny-list, bans, purges, tombstones, retired handles | Moderation (constitution §7) |
| Handle quota (5), issuance quota (3/day), `min_account_age_days` (30) | Handle squatting and ban evasion — identity abuse, not stat manipulation. Each is one comparison |
| Archive chunk SHA-256 + length + count + seq-range verification on restore | Disaster recovery; protects the log from bit rot and from us |
| `keys` refusing group/world-readable secrets; `slog.LogValuer` on secret types | Secret hygiene (constitution §1) |

---

## Proposed but not implemented

`the original proposals (now removed; see [DECISIONS.md](DECISIONS.md))` §4.3 proposes six layers of "layered skepticism". Two are built. **The
constitution's §8 test settles the other four, and the answer is no** — recording that here means
nobody has to re-derive it, and nobody builds one on a quiet afternoon.

| Layer | Status | Verdict under §8 |
|---|---|---|
| 1. Server-side derivation — every value computed from the event stream | **Built** (S3, S4) | In scope, and the most valuable thing in the system |
| 3. Assisted-flight exclusion via the mod's own `flight.flagged` | **Built** (M1–M4, S1) | In scope |
| 2. Physics plausibility — kinematic continuity `∫a·dt ≈ Δv` between windows, teleport-scale position jumps, telemetry continuing after a RUD, orbit events with no ascent history | Not built | **Do not build.** Fails the stock-data test (it models what a player *ought* to be able to achieve, not what KSA ships), fails the honest-player test (every one of these needs a tolerance tuned against real play — save loads, time warp, docking, physics jitter), and fails no-new-machinery (cross-window state per flight) |
| 4. Quarantine pipeline — records land `pending`, top-N held for review, record claims require an attached high-frequency ring-buffer trace, shadow-ban mode | Not built | **Do not build.** Fails the consequence test outright — it queues work for a human, and there is exactly one human. The ring-buffer trace also contradicts D15 (no raw sample firehose) and constitution §3 (the mod must not cost the player frames) |
| 5. Statistical outliers — per-metric robust z-scores, suspicion multipliers for new accounts and for first-upload-is-a-world-record | Not built | **Do not build.** This is the literal thing the principle names — "esoteric patterns". It also punishes exactly the honest player who installs catlog and immediately does something spectacular, which is the best day this project can have |
| 6. Quotas + community reporting feeding the quarantine queue | Partly built, differently | Quotas exist and are identity abuse controls, not anti-cheat (see above). **Do not build** a report queue as an anti-cheat pipeline. A "something's wrong here" mail link into ordinary moderation (§7) is fine and is not this |

One further proposal, from `docs/ksa-integration.md` §7 (the `KittenTuningWindow` row): *"snapshot
the whole `KittenLocomotionTuning.Current` (or a hash of it) alongside any kitten-locomotion
record."* Only `TumbleSpeedGate` is compared today, which is correct — it is the only field that
classifies anything catlog scores. If a kitten-distance or jump board is ever added, the right
move is **one more equality check** against the stock value of the field that classifies *it*, not
a hash of the whole struct. A hash would fail the one-look test (nobody can tell from a differing
hash what changed, or whether it mattered) and the honest-player test (a future build changing an
unrelated default would flag every session on the planet).

---

## Method

Read in full: `server/internal/stats/` (fold, flight, boards, event), `server/internal/projector/`
(projector, rebuild), `server/internal/authz/` (authz, claims, ratelimit, denylist),
`server/internal/ingest/` (writer, types), `server/internal/store/events.go` stream-state section,
`mod/catlog.lib/Detect/`, `mod/catlog/Patcher.cs`, `mod/catlog/PolledSignals.cs`,
`mod/catlog/VehicleTelemetry.cs`. Searched the whole tree for the vocabulary of the unbuilt layers
(`z-score`, `quarantine`, `suspicion`, `plausib*`, `anomal*`, `outlier`, `heuristic`) — the only
hits are the word "plausible" in test fixtures and a `cheater` simulator scenario,
which is a *test* of the flag exclusion rather than a mechanism.

Line references are to the tree at the time of writing and will drift; the file and symbol names
are the durable part.
