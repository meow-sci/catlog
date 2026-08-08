# catlog mod design (`mod/`)

Owns **§7.1–§7.5**. The wire contracts it implements are in [events.md](events.md),
[ingest-api.md](ingest-api.md) and [credential.md](credential.md); the game surfaces it reads are in
[ksa-integration.md](ksa-integration.md); the reasons are in [DECISIONS.md](DECISIONS.md), areas
`MOD-*` and `LOAD-*`.

.NET 10, `net10.0`, nullable enabled, `TreatWarningsAsErrors`, `ImplicitUsings` **disabled** — every
`using` is explicit.

## §7.1 The projects, and the demarcation that makes them testable

| Project | Assembly | References | KSA? |
|---|---|---|---|
| `catlog.lib` | `MeowSci.Catlog.Lib` | `Microsoft.Data.Sqlite`, `Ulid`, `Tomlyn` | **None — enforced** |
| `catlog` | `MeowSci.Catlog` | `catlog.lib`, `Lib.Harmony`, the KSA/Brutal assemblies | yes |
| `catlog.sim` | `MeowSci.Catlog.Sim` | `catlog.lib` | no |
| `catlog.loadgen` | `MeowSci.Catlog.Loadgen` | `catlog.lib`, `catlog.sim` | no |
| `catlog.lib.tests` | — | xunit + `catlog.lib` | no |
| `catlog.integration.tests` | — | xunit + `catlog.lib` | no |

**The whole design rests on one line: `catlog.lib` has zero KSA references, and a guard test proves
it.** It reflects over the assembly's referenced assemblies and asserts none begin with `KSA`,
`Brutal` or `0Harmony` — and further, that the only non-`System.*` references are the three expected
NuGet packages. Every interesting piece of behaviour — detection, spooling, signing, shipping,
recovery — therefore lives somewhere it can be unit-tested and integration-tested without the game
existing.

`mod/Directory.Build.props` resolves the game's reference assemblies through a ladder (`$KSA_DLL_DIR`
→ a sibling `ksa-game-assemblies/current/dll/` checkout → the per-OS install), so the game project
compiles on a machine with no KSA install. That is a deliberate trade with a stated cost: **a machine
with none of the three cannot build the solution at all.** The alternative — leaving the game project
out of the solution — means the one project coupled to KSA is the one project CI never compiles, and
a decomp drop would break it silently until someone opened the game.

Test framework is xunit, with the sibling repos' structural conventions: mirrored directory layout,
one sealed class per type, because-messages on assertions, and an assembly-wide log silencer.

## §7.2 `catlog.lib` — the core

```
catlog.lib/
  Wire.cs         every §4.3/§4.5 constant in one place, including the hard reporting floor
  Events/         EventEnvelope · Payloads (one record per §4.2 type) · EventTypes registry
                  GameSignal + one sealed record per Harmony-origin signal
  Telemetry/      TelemetrySnapshot · TelemetryFrame · GameBridge (the game-thread seam)
                  SituationInfo (the verified packed-bitfield table) · Sanitize (NaN/Inf scrub)
  Detect/         EventDetector · WindowAccumulator · FlightTracker · ImpactCorrelator
                  EventPipeline (composes them — this is what the game and the harnesses drive)
  Outbox/         OutboxDb — Microsoft.Data.Sqlite; outbox_event + shipper_state
  Ship/           BatchShipper · ProofSigner · BrotliCodec · IShipperClock · BackoffPolicy
  Auth/           Credential (§4.6 load + jkt check) · Jws · Jwk (RFC 7638)
  Config/         ModConfig — Tomlyn, atomic save, load-never-throws
  Util/           ULIDs, base64url, SnapshotStore, ModLog, SubsystemHealth, PerfStat
```

House style throughout: immutable records, `System.Text.Json` with snake_case, and **per-subsystem
dead-latch error handling** — a subsystem that faults logs once and disables itself for the session
rather than spamming or throwing across the host boundary.

### The game-thread seam

`GameBridge` has two halves, and the split is the important part:

- **`Frames`** is a latest-wins `SnapshotStore`. Correct for passive telemetry: a dropped sample
  costs resolution, and the detector compares previous against current.
- **`Signals`** is an unbounded, lossless, FIFO channel. Correct for discrete events: a dropped RUD
  or impact is a permanently lost leaderboard entry.

**Frame boundaries travel in-band** as a `FrameBoundarySignal`, because channel order is the only
thing that still carries "these happened in the same frame" once signals leave the game thread — and
the impact correlator needs exactly that.

**There is no zeroed fallback snapshot, anywhere.** A per-vehicle read failure means the vehicle is
*absent* from that frame, logged once. Manufacturing a zeroed snapshot would produce phantom SOI
changes and phantom orbit-achieved edges, and those score.

### Detection rules

- **Latched edges, not raw diffs.** Debounce rate-limits without losing a transition: a suppressed
  change is re-detected on the next sample and reported *from* the last state that actually reached
  the wire.
- **Atmosphere is a Schmitt trigger** (enter below 0.98× the atmosphere height, exit above 1.02×) — a
  bare threshold plus debounce still alternates.
- **A backwards jump in `sim_t` rebaselines** the vehicle instead of diffing across it. That is a save
  load.
- **Orbit class comes from the game's own predicates**, never from NaN-sniffing an apsis: a hyperbolic
  apoapsis is *negative*, not NaN. `ap_m`/`pe_m` are altitudes above the parent's mean radius, not
  from-centre radii, and the snapshot fields are named to make that unmissable.
- **Telemetry windows are half-open in sim time** — a window opened at `t0` covers `t0 ≤ t < t0+30`,
  and a flight ending flushes its partial window before `flight.ended`, so the seconds before a RUD
  are not discarded.
- **`peak_g` and `max_q_pa` are omitted, never zeroed**, when the frame carried no reading. The game
  writes that struct only under full physics, so an all-zero reading means "no data this step" — and
  reporting zero would corrupt the board with fake minima.
- **An impact is held one full frame.** All impacts land before all physics destructions in a frame,
  but a *manual* destroy lands in the game's later input-apply pass — so an impact seen in frame N is
  resolved at the end of frame N+1, and a destruction in either frame flips `survived` to false.
  Ending a flight also resolves that vehicle's outstanding impacts immediately, because the verdict
  cannot change once the flight is over.

### The outbox

SQLite, in the player's own data directory. Two tables: `outbox_event` and `shipper_state` (`sid`,
`seq`, `last_bh`, `clock_offset_ms`, `last_request_ms`).

**Each envelope's serialized line is stored verbatim**, so the bytes the server hashes are the bytes
the detector produced. **Only `telemetry.window` is droppable** (kind 0); everything else is kind 1,
including `roster.snapshot`, which is periodic but carries totals that feed boards. Pruning deletes
oldest kind-0 rows until the cap is met and stops when only kind-1 rows remain.

Nothing leaves the outbox until the server answers `200`. That is what makes the shipper's
unload-time flush an optimisation rather than a correctness requirement.

### The shipper, and the 30-second floor

`BatchShipper` composes NDJSON, brotli-compresses, mints a batch ULID, signs the ES256 proof over the
body hash and the `sid`/`seq`/`ph` chain, POSTs, and implements the recovery table in
[ingest-api.md](ingest-api.md).

Two things beyond the wire contract:

**The batch id is minted once per batch *body* and reused for every retry of those bytes.** A fresh
`jti` on an unchanged `seq` misses the replay short-circuit and earns a `409` instead. The pairing is
persisted, so a game crash mid-ship replays cleanly rather than forking.

**A hard minimum of 30 seconds between requests, unreachable from `catlog.toml`.** The threat model
is stated so its scope is not argued about later: *the attacker is a player editing a text file.*
`ship_interval_s = 1` used to be a one-line edit that turned a stock install into a firehose. Someone
who recompiles the assembly can do anything and always will be able to; that is explicitly not what
this defends against. The floor closes the **easy** path.

`Wire.MinShipIntervalSeconds = 30.0` is a `const` (pinned as `IsLiteral` by a test, so it is baked
into every call site), and it is enforced in three places because clamping the config alone only
closes the path you thought of:

1. `ModConfig.Normalize` clamps `ship_interval_s` up to it, so the number a player reads is the
   number the mod honours.
2. `BatchShipper.ShouldShip` reports "not due" inside the window, checked *before* either trigger, so
   the count trigger cannot open it early — 10,000 buffered events still ship one batch per window.
3. `SendAsync` refuses at the point of transmission, stamping the window as it goes.

Only (3) is the guarantee; the others are courtesies. **It refuses rather than waits** — a concealed
30-second block inside a method the game thread can reach is a shutdown hang. Every retry is floored
too, including `409` and `413`, at `max(backoff, 30 s)`; a `Retry-After` is honoured when it asks for
longer and floored when it asks for shorter. The cost is recovery latency and nothing else.

The stamp is **persisted**, so restarting the game does not reset the window. The comparison uses raw
clock time and never the server-learned offset, because that offset is attacker-adjacent input and a
hostile `Date` header must not be able to buy a shorter window.

The floor is measured against an **injected** `IShipperClock`, which is what lets the simulator prove
a 30-second property in milliseconds. That seam is unreachable from anything a player can edit: the
shipped mod constructs its shipper in exactly one place and *omits* the clock argument, so the safe
thing is the parameter's default. Three tests hold it shut, two of which read `mod/catlog`'s own
sources (comments stripped) and assert the shipped assembly never names the clock type and reads no
environment variable or command line at all.

The one exemption is `FinalShip`, the courtesy flush at game unload: exactly one attempt, run on the
thread pool, waited on for at most **2 seconds**, then cancelled and abandoned — because a hung
connection must never hold the game open. It is exempt because it fires at most once per session, and
abusing it means actually quitting and relaunching KSA. The exempt request is still stamped, so it
buys one batch on the way out rather than a reset.

## §7.4 `catlog` — the game project

The only code that touches KSA, and deliberately thin: everything else is a call into `catlog.lib`.

| File | What |
|---|---|
| `Mod.cs` | Lifecycle, config load, the status window (F10) |
| `Patcher.cs` | The Harmony patches, each carrying its `ksa-integration.md` table row as a comment |
| `VehicleTelemetry.cs` | **Every** KSA read, each with a `[KsaAnchor]` |
| `PolledSignals.cs` | The 2 Hz poll, vehicle tracking, the roster diff |
| `CatlogRuntime.cs`, `ModPaths.cs`, `KsaAnchor.cs` | Wiring, paths, the anchor attribute |

**Every KSA read carries a `[KsaAnchor]`** naming the `file:line` it was verified against and the
units gotcha it embodies — inclination is radians, apsides are radii from body centre, situation is a
packed bitfield, structural load is all-zero off full physics, crew is seats not occupants, the
roster's fastest speed is ecliptic-frame. Anchors marked high churn risk are the surfaces that are
one build old, and are the first things to re-verify on a decomp drop.

Patch points that were chosen carefully, because the obvious target was wrong:

- **`flight.flagged: teleport` hooks the player-command path, not `Vehicle.Teleport`.** The latter has
  three callers and only one is cheating — normal EVA egress teleports a kitten, and an editor
  decouple teleports the split vehicle. Flagging there would quietly exclude ordinary play from every
  board.
- **`flight.started` comes from polling, not from the registration hook**, where the vehicle is
  half-built and every read throws. A vehicle is registered the first time catlog *sees* it, which
  also closes the hole where a vehicle created and destroyed inside one sample interval would emit
  events against a flight the server can never join.
- **`flight.ended` has one emitter**, the true removal choke point, with the *reason* decided by
  intent flags the earlier patches set. A silent-removal safety net closes any tracked vehicle that
  vanished without one, so a flight never leaks open.
- **`kitten.kia` is emitted by roster diff**, with the manual-destroy patch only supplying context.
  One emit path means no dedup problem, and it still catches a KIA arriving by a route a future build
  adds. The first roster read is a baseline that emits nothing, so loading a save full of dead kittens
  does not replay their deaths.
- **Engine events are whole-vehicle, not per-engine**, using the two globals the game already
  publishes. The consequence is recorded rather than hidden: a vehicle that shuts down one of two
  engine groups reports nothing until the last one stops.

Runtime state lives in the player's KSA user directory — `catlog.toml`, `outbox.db`,
`install-id.txt`, the credential — **never beside the installed DLLs**, because a mod update replaces
the install folder and the player's spool, settings and credential must survive it.

**Acceptance is a manual smoke checklist, by design**; there are no automated tests here. That is
what the lib/sim split buys. The checklist is in [ROADMAP.md](ROADMAP.md) and has **not yet been
run** — see there for what that means.

## §7.3 `catlog.sim` — the deterministic acceptance harness

```sh
make sim                                                       # list scenarios
make sim SCENARIO=hop-lithobrake CRED=<path> ASSERT=1 SPEED=100
```

Scenarios are C# classes producing `SimStep`s — sim-time-stamped snapshot sets and signals — fed
through the **real** detector, outbox, signer and shipper against a **real** server. Six of them:
`hop-lithobrake`, `orbit-and-back`, `rud-sampler`, `tumbleweed`, `cheater`, `soak`.

`cheater` is the one that earns its place: it flies two flights, one flagged *before* its scoring
events and one flagged ~60 s *after*. Only the second tests the rebuild backstop — a scenario that
always flagged first would pass with the rebuild path completely broken.

Assertions are **baseline-relative** (a record board must end at `max(baseline, expected)`, a counter
at `baseline + delta`), captured after a projector wait, so every scenario is re-runnable against a
database that already holds data. Nothing sleeps and hopes: the wait is on `GET /admin/stats` until
the projector's lag is zero *and* its checkpoint equals the log head.

`--speed` is sim seconds per wall second; unpaced is the default, because the assertions do not care
about pacing.

## `catlog.loadgen` — the volume harness

A sibling project, and `catlog.sim` deliberately does not know it exists. The dependency runs one way
only: adding a `--random` mode to the simulator would have put its six exactly-asserted scenarios one
refactor away from being a statement about a dice roll.

It does **not** fabricate envelopes. Like the simulator it emits only telemetry snapshots and game
signals, and the real detector, accumulator, correlator, outbox, signer and shipper do their real
jobs — a hand-authored batch posted at `/v1/ingest` would test the Go server and nothing else.
Players are provisioned through the real OAuth flow against `mockidp`, so catlogd runs its real code
exchange, real token verification, real `user_key` derivation and real handle quotas.

**Play is invented as a career.** Capability is gated on accumulated in-game time and nothing else:
players arrive with time already on the clock and progress from pad tests and hops through suborbital
lobs, orbit, rendezvous and docking, transfers, landings, and probes to the outer system. Fleet size
grows the same way. Failure is career-appropriate — beginners lose vehicles on the pad and at max-Q,
veterans on final approach and at a docking port — so the RUD causes appear in proportions that match
what the player was attempting. Everything is flown around the solar system KSA actually ships, with
radii, masses and atmosphere heights taken from the game's own data, so orbital speeds are derived
rather than invented and EVAs only happen where a kitten could stand.

**Reproducibility is per-player, not global.** There is no shared RNG: player *i* draws from a pure
function of `(seed, i)`, in a fixed order, on one thread, so nothing about scheduling or server
latency can reach a draw. It is SplitMix64 rather than `System.Random`, whose seeded stream is a
runtime detail that has already changed once. The proof is a printed digest that deliberately
excludes ULIDs, because ids are minted fresh every run by design. `--seed` namespaces the *gameplay*;
`--namespace` namespaces the *identities*, so the same seed re-runs against a database that already
holds the last run's players.

**Cadence and floor are separate, and conflating them cost a whole run before it was noticed.** The
harness decides when a batch is *due* on sim time (`--ship-age`) and leaves *whether it may go*
entirely to the shipper; when the floor refuses, the clock is wound by the remainder and not one
millisecond more. The floor itself is not weakened — `catlog.loadgen` is a console assembly no player
installs, and the guard tests over `mod/catlog`'s sources are unaffected by it.

Running it, and how to make the numbers mean something, is in
[../DEVELOPMENT.md](../DEVELOPMENT.md#5-load-testing--make-loadgen).

## §7.5 Test suites

**`catlog.lib.tests`** — unit, no network, no game. Detector transitions and debounce, atmosphere
hysteresis both edges, orbit achieve and escape, window boundary arithmetic, impact correlation,
outbox ordering and pruning and crash recovery, JWS/JWK round-trips **and conformance against
`contracts/testdata`**, the shipper's whole recovery table on a virtual clock, the clock-seam guards,
and the assembly guard.

**`catlog.integration.tests`** — against a real `catlogd` on a random port with a throwaway data
directory: acceptance, replay, body tampering, clock-skew recovery, revocation, and the `413` halving
ladder. The oversize case runs its own server with a constrained event cap, because at the shipped
2,000-event limit the mod's *local* pre-check fires first and no server `413` is ever seen.

Both fixtures **reap stray child processes** and fail an otherwise-passing run if one survived. A
`catlogd` that outlives its test holds the exclusive database lock and shuts every later process out —
a silent, compounding failure that always gets blamed on something else.
