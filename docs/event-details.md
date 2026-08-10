# Event details — the technical event & projection reference

**This is the canonical, 1:1 technical reference for every event catlog produces and every
projection catlog derives from them.** It is the document a maintainer reads before touching a
detector, a payload, a fold, or a board, and the document a maintainer updates *in the same commit*
as that change.

It has a mandatory companion. The player-facing site under [`docs-site/`](../docs-site/) is the same
information rewritten for people who play the game rather than build it. **The two move together.**
See [Maintenance contract](#maintenance-contract) below, and the rule as stated in
[`AGENTS.md`](../AGENTS.md) and [`docs/CONSTITUTION.md`](CONSTITUTION.md).

Related, narrower documents that remain authoritative for their own slice:
[`events.md`](events.md) (the wire contract as a specification),
[`ingest-api.md`](ingest-api.md) (HTTP), [`mod.md`](mod.md) (mod internals),
[`ksa-integration.md`](ksa-integration.md) (patch points and game-build churn),
[`server.md`](server.md) (Go packages and schema). This file is the *joined* view: one row per
event, from the game object it is read off, through the detector, to the board it moves.

Reference build: KSA **2026.8.5.5168**, decompiled sources at
`ksa-game-assemblies/current/decomp/KSA/`. Every `KSA/…` citation below is into that tree.

---

## Maintenance contract

**`docs/event-details.md` is the primary reference.** Every change to an event, a payload, a
detector, a game read, a fold, a board key, an eligibility rule, or a unit lands here first and in
the same commit as the code.

**`docs-site/` is not optional.** The same change lands in the player-facing site in the same
commit, in player language. A commit that updates one and not the other is an incomplete change.

| You changed… | Update here | And in `docs-site/` |
|---|---|---|
| An event's payload, `ver`, or emission rule | [The event catalog](#the-event-catalog) | the family page under `src/content/docs/events/` **and** `src/data/events.ts` |
| Where a value is read from in the game | that event's **Game source** block | that event's "Where it comes from" prose |
| Event-driven ↔ passive, a threshold, a debounce | that event's **Classification** block | the event's `trigger` in `src/data/events.ts` |
| A new event type | a new `##` section + the [registry table](#the-registry) | `src/data/events.ts` + the right family page |
| A board's fold, eligibility, unit or title | [Boards](#boards) | `src/content/docs/leaderboards/catalog.mdx` + `src/data/boards.ts` |
| A new board or board family | [Boards](#boards) + [Suppression](#suppression-and-eligibility-matrix) | both of the above |
| A rebuild-vs-incremental divergence | [Rebuild ≠ incremental](#rebuild--incremental) | `leaderboards/eligibility.mdx` |
| Whether a type can be switched off in `catlog.toml` | the [registry table](#the-registry) + [Turning a type off](#turning-a-type-off--the-events-table) | `src/data/events.ts` + `start/turning-things-off.mdx` |

`docs-site/src/data/events.ts` and `docs-site/src/data/boards.ts` are the machine-readable mirror of
this file's tables. They are *derived* data: this document wins any disagreement, and the fix is to
correct the mirror.

---

## Contents

- [Pipeline](#pipeline)
- [The envelope](#the-envelope)
- [Identifiers and sanitisation](#identifiers-and-sanitisation)
- [Enum vocabularies](#enum-vocabularies)
- [Signal → event dispatch](#signal--event-dispatch)
- [The registry](#the-registry)
- [The event catalog](#the-event-catalog) — 25 sections
- [Projections](#projections)
- [Boards](#boards)
- [State projections](#state-projections)
- [Suppression and eligibility matrix](#suppression-and-eligibility-matrix)
- [Rebuild ≠ incremental](#rebuild--incremental)
- [Conformance coverage](#conformance-coverage)
- [Known drift](#known-drift)

---

## Pipeline

```
game thread                    worker task                 wire            server
───────────                    ───────────                 ────            ──────
Harmony patch body ─┐
                    ├─► GameBridge.Signals (Channel, lossless, FIFO)
PolledSignals.Poll ─┘         │
                              ├─► EventPipeline.Dispatch ─► EventEnvelope ─► OutboxDb
VehicleTelemetry.Sample ──────┤        │                                        │
   (2 Hz, latest-wins)        │        ├─ EventDetector  (prev/curr edges)      │
   GameBridge.Frames ─────────┘        └─ WindowAccumulator (30 s folds)        │
                                                                           NDJSON+br
                                                                                ▼
                                                      POST /v1/ingest ─► events.db (immutable)
                                                                                │
                                                                        projector.Step
                                                                                │
                                                                    stats.Batch folds
                                                                                ▼
                                                                    projections.db ─► read API
```

**Two transports out of the game thread, deliberately not merged**
(`mod/catlog.lib/Telemetry/GameBridge.cs:16-36`):

- **`GameBridge.Frames`** — a latest-wins `SnapshotStore` of `TelemetryFrame` (`GameBridge.cs:67`,
  published `:94-99`). Lossy on purpose: a dropped frame costs sample resolution only. **Only
  passive telemetry rides here.**
- **`GameBridge.Signals`** — an unbounded, lossless, FIFO `Channel<GameSignal>` (`GameBridge.cs:52-59`,
  `:70`, write `:106-127`). Every discrete/scoring occurrence rides here. A refused write increments
  `SignalsDropped` and logs once (`:117-124`).
- **Frame boundaries travel in-band** as `FrameBoundarySignal` (`GameBridge.cs:137-138`): channel
  order is the only surviving carrier of "these happened in the same frame", which is exactly what
  `ImpactCorrelator` needs.

Game-thread driver: `Mod.OnBeforeUi(double dt)` under `[StarMapBeforeGui]` (`mod/catlog/Mod.cs:71-76`)
→ `CatlogRuntime.Tick(dt)` (`CatlogRuntime.cs:335-367`). `Tick` calls `SampleClock.Tick(dt)`
(`:349`); on a due tick it runs `SamplePass` (`:471-497`) and — **unconditionally, on every frame
including unsampled ones** — `_bridge.EndFrame(simT, wallMs)` (`:366`).

`SamplePass` order is load-bearing (`CatlogRuntime.cs:475-496`): **signals first, then the frame**, so
`flight.started` always precedes that flight's first `telemetry.window`.

Worker loop `CatlogRuntime.RunWorkerAsync` (`:499-550`) drains signals and, on each
`FrameBoundarySignal`, consumes the latest published frame if its `Sequence` advanced (`:515-523`).
Envelopes go to `OutboxDb.Append` in emission order (`:552-569`).

### Cadence and constants

| Knob | Value | Where |
|---|---|---|
| Passive sample rate | **2.0 Hz** default | `Wire.DefaultSampleHz`, `mod/catlog.lib/Wire.cs:130` |
| Configurable range | `sample_hz` clamped to **[0.1, 20]** | `ModConfig.Normalize`, `Config/ModConfig.cs:310` |
| Telemetry window | **30.0 sim seconds** (`window_s` clamped [5, 300]) | `Wire.TelemetryWindowSeconds`, `Wire.cs:133`; `ModConfig.cs:311` |
| Detector debounce | **2.0 sim seconds** per (vehicle, `DetectKind`) | `Wire.DetectorDebounceSeconds`, `Wire.cs:136` |
| Atmosphere hysteresis | **±2 %** of atmosphere height | `Wire.AtmosphereHysteresis`, `Wire.cs:139` |
| Orbit-achieved margin | **1000 m** above atmosphere top | `Wire.OrbitAchievedMarginM`, `Wire.cs:142` |
| Roster poll interval | **600 sim seconds** | `PolledSignals.RosterIntervalSeconds`, `mod/catlog/PolledSignals.cs:30` |
| Manual-destroy → KIA attribution window | **2.0 sim seconds** | `PolledSignals.ManualDestroyWindowSeconds`, `:36` |
| Crew-kill → KIA *flight* attribution window | **2.0 sim seconds** | `EventPipeline.CrewKillWindowSeconds`, `mod/catlog.lib/Detect/EventPipeline.cs:79` |
| Ship floor | **30 s**, enforced at three layers | `Wire.MinShipIntervalSeconds`, `Wire.cs:237` |

`SampleClock` (`Telemetry/SampleClock.cs:47-60`) fires **at most once per `Tick`** and zeroes the
accumulator when the backlog exceeds one interval — missed intervals after a hitch are **dropped,
not back-filled**.

### Outbox and batch

`mod/catlog.lib/Outbox/OutboxDb.cs`. SQLite, WAL, `synchronous=NORMAL`, `busy_timeout=3000`
(`:125-128`), schema version 1 (`:66`), DDL `:68-82`:

```sql
CREATE TABLE outbox_event (
    id         INTEGER PRIMARY KEY,     -- append order; the only ordering that exists
    event_id   TEXT    NOT NULL UNIQUE, -- the envelope's ULID; makes re-append idempotent
    kind       INTEGER NOT NULL,        -- 0 passive (droppable), 1 scoring (never pruned)
    created_ms INTEGER NOT NULL,        -- the envelope's wall_t
    body       TEXT    NOT NULL         -- the NDJSON line, verbatim
);
CREATE INDEX idx_outbox_kind_id ON outbox_event(kind, id);
CREATE TABLE shipper_state (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
```

- `Append` uses `INSERT OR IGNORE` in one transaction (`:172-199`) — a retried append is idempotent.
- `kind` comes from `EventTypes.KindOf` (`Events/EventTypes.cs:181-182`). **`telemetry.window` is the
  only kind-0 type**; the other 24 are kind 1, explicitly including `roster.snapshot` because it
  carries totals that move boards (`EventTypes.cs:176-179`).
- `Prune` deletes oldest kind-0 rows until the cap is met, and stops when only kind-1 rows remain.
  It tracks the running total across the deletes rather than re-measuring the whole table after each
  one — the re-measure made pruning quadratic in the number of rows dropped, which mattered exactly
  when the outbox was already under pressure.
- `NextBatch` reads oldest-first `ORDER BY id` (`:213`). Rows are deleted **only** on a `200`
  (`MarkShipped`, `:244-254`).

`OutboxBatch.ToNdjson()` (`:32-42`) emits LF-separated lines **with a trailing LF**. That byte
sequence is Brotli-compressed and its SHA-256 becomes the proof's `bh`.

| Limit | Value | Where |
|---|---|---|
| compressed body | 1 MiB → `413 too_large` | `Wire.MaxCompressedBodyBytes`, `Wire.cs:19`; `ingest/decode.go:44` |
| decompressed NDJSON | 8 MiB → `413` | `Wire.MaxDecompressedBytes`, `Wire.cs:22` |
| events per batch | 2000 → `413` | `Wire.MaxEventsPerBatch`, `Wire.cs:25` |
| one NDJSON line | 16 KiB → `400 malformed_batch` | `Wire.MaxEventLineBytes`, `Wire.cs:28` |
| default per-batch cap | 500, halved on `413`, floor 50 | `Wire.DefaultBatchEventCap`/`MinBatchEventCap`, `Wire.cs:250,253` |
| ship cadence | oldest ≥ 60 s, or ≥ 500 pending; hard 30 s floor | `Wire.cs:169,175,237` |

Framing enforced server-side (`ingest/decode.go:150-186`): LF only (CRLF rejected), no interior blank
lines, one complete JSON value per line, optional single trailing newline. Transport:
`POST /v1/ingest`, `Content-Type: application/x-ndjson`, `Content-Encoding: br`, headers
`X-Catlog-License` and `X-Catlog-Proof` (`Wire.cs:39-51`).

**There is no per-event sequence number on the wire.** Ordering is emission order out of
`EventPipeline`, preserved by `OutboxDb.id` and by `NextBatch`'s `ORDER BY id`.
`TelemetryFrame.Sequence` and `FrameBoundarySignal.Sequence` are internal diagnostics and are never
serialised. The only wire sequence is per batch: the proof's `sid` (stream ULID) + `seq` (1-based,
strictly monotonic per `(jkt, sid)`) + `ph` (previous body hash, omitted at `seq == 1`) —
`Ship/ProofSigner.cs:20-31`.

**Idempotency** is the server's `(player, event_id)` uniqueness (D19). The mod does **not** derive
event ids from content — every `EventEnvelope.Create` mints a fresh random ULID
(`Events/EventEnvelope.cs:93`) — so a logical occurrence detected twice produces two rows. The
guards are structural: latched edges + debounce in the detector, baseline-emits-nothing seeding,
`IsDisposed` guards in patch bodies (`Patcher.cs:385-386`), flag dedup per `(flight, flag)`
(`FlightTracker.AddFlag`, `:131-137`), and `INSERT OR IGNORE` on `event_id`.

---

## The envelope

One event = one JSON object = one NDJSON line. `mod/catlog.lib/Events/EventEnvelope.cs:11-103`. Every
property carries an explicit `[JsonPropertyName]` so a C# rename cannot silently move the wire
(`:6-10`).

**The wire field naming the event type is `type`** (a JSON string). There is no `kind` or `event`
field on the wire; `kind` exists **only** as a local SQLite column in the mod's outbox.

| Key | JSON type | Optional | Source | Constraints |
|---|---|---|---|---|
| `id` | string | required | `EventEnvelope.cs:14-15`, minted `:93` via `Ids.NewUlid()` (`Util/Ids.cs:21`) | 26-char ULID. Server: `ids.Parse` or `400 malformed_batch` (`ingest/decode.go:219-222`). Dedup key is `(player, event_id)` (D19). |
| `type` | string | required | `:18-19`, set from the `EventTypes` constant at the call site | Namespaced lowercase `[a-z0-9_.]`. Must be one of the 25 registry names or the **whole batch** is rejected (`decode.go:223-227`, `ingest/types.go`). |
| `ver` | int | always emitted | `:22-23`, from `EventTypes.VersionOf(type)` at `:95` | **Every registry type is `ver: 1`** (`EventTypes.cs`). Server requires present and ≥ 1 (`decode.go:228-233`); unknown-but-higher is accepted and stored. |
| `flight` | string \| null | **key always present** | `:29-30`; no `JsonIgnore` (`Util/CatlogJson.cs:16-21`) | ULID when non-null; validated as a ULID when present (`decode.go:239-244`). Always null on `system.discovered`, `system.body`, `session.started`, `roster.snapshot` and `kitten.eva_end`; **conditionally** null on `kitten.tumble` and `kitten.kia`, which name a flight whenever the mod can resolve one (see those entries). |
| `session` | string | required | `:33-34` | ULID. Minted by the `FlightTracker` ctor (`Detect/FlightTracker.cs:45`) and re-minted at every save-load boundary (`FlightTracker.NewSession`, `:71-78`). |
| `career` | string | required | `:41-42` | Exactly **16 lowercase Crockford base32** chars (`0-9 a-z` minus `i l o u`). Alphabet `Ids.Crockford` (`Ids.cs:14`), validator `Ids.IsHash16` (`:71-82`), server `validCareer` (`decode.go:283+`). |
| `sim_t` | number | optional server-side, always emitted | `:50-51` | Universe sim seconds since this career began, from `Universe.GetElapsedSeconds()` (`VehicleTelemetry.SimTimeSeconds`, `:478-488`, anchor `KSA/Universe.cs:2103`). Always finite (`Sanitize.Finite`). |
| `wall_t` | int (unix ms) | required | `:54-55` | `DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()`. **Untrusted by the server** — `recv_time` is stamped at insert. Missing → `400`. |
| `payload` | object | required (`{}` allowed) | `:62-63` | Stored **verbatim**; unknown *payload* keys are preserved for forward compat — the exact opposite of the envelope rule. |

**The envelope key set is closed.** The Go decoder uses `dec.DisallowUnknownFields()`
(`decode.go:165`): any unknown envelope key rejects the whole batch.

### Serialisation canonicalisation

`mod/catlog.lib/Util/CatlogJson.cs:26-31` is the single `JsonSerializerOptions` in catlog:

- `PropertyNamingPolicy = SnakeCaseLower` — belt-and-braces; every wire property already carries an
  explicit `[JsonPropertyName]`, so the policy never actually decides a key name.
- `Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping`, `WriteIndented = false`.
- **No `DefaultIgnoreCondition`.** Omission is opted into per property, in exactly three places:
  `telemetry.window.peak_g`, `telemetry.window.max_q_pa` (`Events/Payloads.cs:238,241`) and the
  proof's `ph` (`Ship/ProofSigner.cs:28-31`). Everything else, including `"flight": null`, is always
  written.
- **Key order = C# declaration order.** Not a normative contract — `bh` hashes the actual bytes —
  but see [Known drift](#known-drift) about the Go-generated conformance vector.

`EventEnvelope.ToNdjsonLine()` (`:71`) produces one line with **no trailing newline**; the outbox
stores that string verbatim (`OutboxDb.cs:196`) so the bytes the server hashes are the bytes the
detector produced.

Non-finite doubles can never reach the wire: `Telemetry/Sanitize.cs:19-26` scrubs NaN/±∞ to 0 at the
**capture** boundary. JSON has no NaN literal; a bare `NaN` token would earn `400 malformed_batch`.

---

## Identifiers and sanitisation

| Identifier | Derivation | Where |
|---|---|---|
| `id` (event) | fresh ULID per envelope | `Ids.NewUlid()`, `Ids.cs:21`; `EventEnvelope.cs:93` |
| `session` | fresh ULID per session; re-minted on every save-load | `FlightTracker.cs:45`, `:74` |
| `flight` | fresh ULID per `(vehicle_id, LaunchGameTime)` | `FlightTracker.FlightFor`, `:92-111`. A differing non-NaN `LaunchGameTime` retires the open flight and mints a new one (`:96-110`). `LaunchGameTime` ← `Vehicle.LaunchGameTime` (`VehicleTelemetry.cs:461-471`, anchor `KSA/Vehicle.cs:162`: set at ctor `:1313`, restored from save `:922`, inherited by split children `:1543`) — which is what makes a save reload re-use the same flight rather than mint a second. |
| `career` | `crockford32_lower(SHA-256("catlog-career:" + install_id + ":" + save_key)[0..10])` → 16 chars | `Ids.CareerId`, `:65-66`; `Hash16`, `:88-108`. `save_key` = `"save:" + GameSave.Id` once saved (`VehicleTelemetry.AdoptSaveCareer`, `:536-549`) or `"new:" + fresh ULID` for an unsaved game (`:560-562`). Install-salted, so the server never learns a save's name. |
| `install` | ULID persisted per machine | `ModPaths.LoadOrCreateInstallId()`, `CatlogRuntime.cs:167` |
| `kid` (kitten) | `crockford32_lower(SHA-256("catlog-kitten:" + install_id + ":" + roster_name)[0..10])` → 16 chars | `Ids.KittenId`, `:44-45` |
| `sid` / `seq` (batch stream) | ULID stream id + 1-based monotonic sequence in `shipper_state` | `Wire.StateKeys.StreamId`/`Seq`, `Wire.cs:270,273` |

`install` and the raw `career`/`kid` never reach a public response: `readapi/privacy.go` drops
`install`/`install_id` outright and relabels `career`/`kid` **per player** as 16 Crockford chars
derived from `SHA-256("catlog-public-label:" + kind + ':' + int64BE(playerID) + ':' + value)`
(`privacy.go:114-132`).

### Name sanitisation

| Field | Rule | Where |
|---|---|---|
| `flight.started.vehicle_name` | printable US-ASCII `0x20–0x7E`, **≤ 64 chars**, trimmed; empty → `"vehicle"` | `Ids.SanitizeVehicleName`, `:124`; `SanitizeAscii`, `:126-143` |
| every kitten `name` | printable US-ASCII, **≤ 32 chars**, trimmed; empty → `"kitten"` | `Ids.SanitizeName`, `:116` |
| `body` | `IParentBody.Id` lowercased; empty/unreadable → **`"unknown"`** | `VehicleTelemetry.BodyName`, `:267-271`; `BodyOf`, `:276-286` |
| `situation` | exhaustive switch over KSA's 8-value `Situation` enum → fixed lowercase names; anything else → **`"unknown"`** | `VehicleTelemetry.SituationName`, `:895-906`; library-side `SituationInfo.Normalize`, `Telemetry/SituationInfo.cs:93-94` |

The sanitisers truncate **before** trimming, so a 64th character that is a space is dropped by the
trim (`Ids.cs:131-142`).

---

## Enum vocabularies

Wire-string mappings live in `mod/catlog.lib/Events/EventTypes.cs`; the C# enums in
`Events/GameSignal.cs`.

### `vehicle.rud.cause` — 6 shipped values

`ground_impact`, `ocean_impact`, `collision`, `excessive_g_force`, `aerodynamic_forces`,
`hydrodynamic_forces`.

Enum `RudCause` (`GameSignal.cs:19-38`); `EventTypes.ToWire(RudCause)` (`:147-156`) is a total switch
with **unknown → `"collision"`**. Game source `VehicleDestructionCause` (`KSA/VehicleDestructionCause.cs:3-11`),
mapped by `VehicleTelemetry.MapCause` (`:828-837`) — total switch, a new game value lands on
`GroundImpact` and logs a warning (`:908-914`).

**This is not an allow-list server-side.** The six are fixture data (PROJ-033); a future cause gets
its own `rud_<cause>` board rather than disappearing.

### `flight.ended.reason` — 3 values

`recovered`, `destroyed`, `despawned`. `FlightEndReason` (`GameSignal.cs:6-16`),
`EventTypes.ToWire` (`:161-165`), default → `despawned`.

### `flight.flagged.flag` — 5 values

`teleport`, `refuel`, `resource_edit`, `console`, `tuning`. `FlightFlag` (`GameSignal.cs:71-92`),
`EventTypes.ToWire` (`:171-178`), **default → `"tuning"`**. Server-side an unrecognised value sets
`FlagOther` (bit 5) rather than being ignored — `stats/flight.go:29,34-48`, PROJ-002.

### `kitten.kia.context` — 3 values

`rud`, `manual_destroy`, `unknown`. `KiaContext` (`GameSignal.cs:58-68`), `EventTypes.ToWire`
(`:183-188`), default → `unknown`. Per D11 the `rud` arm **never fires in the shipped mod** —
nothing raises `KiaContext.Rud`.

### `vehicle.situation.from` / `.to` — open set, 9 emitted values

`maneuvering`, `freefall`, `rolling`, `landed`, `sailing`, `floating`, `dragging`, `bottomed`, plus
**`unknown`** for anything a future build adds. `VehicleTelemetry.SituationName` (`:895-906`), anchor
`KSA/Situation.cs:3-13`.

The game enum is a **packed bitfield**: `value = (SurfaceContact << 1) | onRailsBit`
(`KSA/SituationEx.cs:56,62`). `catlog.lib` re-derives contact and rails bits from the *names* with
zero KSA references (`SituationInfo.Table`, `Telemetry/SituationInfo.cs:42-53`):

| situation | SurfaceContact | on rails | bitfield |
|---|---|---|---|
| `maneuvering` | None | no | 0 |
| `freefall` | None | yes | 1 |
| `rolling` | Terrain | no | 2 |
| `landed` | Terrain | yes | 3 |
| `sailing` | Ocean | no | 4 |
| `floating` | Ocean | yes | 5 |
| `dragging` | Terrain+Ocean | no | 6 |
| `bottomed` | Terrain+Ocean | yes | 7 |

Every lookup is total; unknown → no contact, off rails (`SituationInfo.cs:58-88`). There is
deliberately no exhaustive `switch` over this set anywhere in catlog.

**The server now keeps the same eight rows** in `server/internal/stats/situation.go` — the contact
column only, because no board reads the rails bit. Three boards (`softest_touchdown`,
`landed_bodies`, `splashdowns`) decide what a transition *means* from it, so it could not stay on the
mod side alone. **It is catlog's one two-sided table**: see [Number formatting](#number-formatting)
for the discipline that governs it.

The server's table also draws a distinction the mod's does not need. `knownSituation(name)` is not
`contactOf(name) == contactNone`: `maneuvering` and `freefall` are *known* to be off the ground,
whereas `"unknown"` only means the read failed. `softest_touchdown` requires the first and refuses
the second, because a touchdown measured from a situation nobody could read is not a measurement —
and on an ascending board it would be a record.

### `body` — fully open, opaque to the server

No allow-list anywhere. Value is `IParentBody.Id.ToLowerInvariant()` (`VehicleTelemetry.BodyName`,
`:267-271`, anchor `KSA/IObjectId.cs:5` / `KSA/Astronomical.cs:96`), or the literal `"unknown"` on a
failed read. To *get a board* a name must additionally satisfy the stat-suffix shape: lowercase,
first char `[a-z0-9]`, rest `[a-z0-9._-]`, ≤ 40 chars (`stats/boards.go:241-256`).

### Small literal sets

`vehicle.atmosphere.dir` ∈ `{entered, exited}` (`Detect/EventDetector.cs:313,326`);
`vehicle.orbit.phase` ∈ `{achieved, escaped}` (`:373,401`). Both are literals at the detector call
sites, not enums.

`EngineEventKind { Ignition, Shutdown, Flameout }` (`GameSignal.cs:41-55`) selects the event type via
`EventTypes.TypeOf` (`:233-238`), default → `engine.flameout`.

---

## Signal → event dispatch

`EventPipeline.Dispatch` (`Detect/EventPipeline.cs:199-316`) is the single switch. Signal records are
in `Events/GameSignal.cs`.

| `GameSignal` subtype | Raised by | Produces |
|---|---|---|
| `FrameBoundarySignal` (`:138`) | `GameBridge.EndFrame` ← `CatlogRuntime.Tick` (`:366`) | drains `ImpactCorrelator.EndFrame()` → 0..n `vehicle.impact` (`EventPipeline.cs:203-206`) |
| `SessionLoadedSignal` (`:151`) | `CatlogRuntime.OnSessionBoundary` (`:298-300`) ← `Patcher.SessionBoundaryPostfix` (`:691-701`) | `session.started` + full pipeline reset |
| `VehicleCreatedSignal` (`:186-200`) | `PolledSignals.Track` (`:90-114`) | `flight.started` + replayed session-wide `flight.flagged` |
| `VehicleRemovedSignal` (`:188`) | `Patcher.DisposePrefix` (`:537-538`); `PolledSignals.Prune` (`:228`) | `flight.ended` + drained impacts + flushed window |
| `VehicleRecoveredSignal` (`:200`) | **nothing in the shipped mod** — sim/tests only | `flight.ended` with `reason: recovered` |
| `RudSignal` (`:262-274`) | `Patcher.DestroyVehicleFromEventPrefix` (`:393-431`) | `vehicle.rud` + marks the correlator |
| `ImpactSignal` (`:238`) | `Patcher.GroundImpactApplyPostfix` (`:430-440`) | held by the correlator; later `vehicle.impact` |
| `SplashSignal` (`:259`) | `Patcher.WaterSplashApplyPostfix` (`:469-471`) | converted to an `ImpactSignal` with `launch_pad=false` (`ImpactCorrelator.cs:52-60`) → `vehicle.impact` |
| `StagingSignal` (`:273`) | `Patcher.ActivateNextSequencePostfix` (`:578`) | `vehicle.staging` |
| `DockSignal` (`:281`) | `Patcher.DockPostfix` (`:602`) | `vehicle.docked` |
| `UndockSignal` (`:289`) | `Patcher.UndockPostfix` (`:625`) | `vehicle.undocked` |
| `EngineSignal` (`:299`) | `PolledSignals.PollVehicle` (`:174-190`) | `engine.ignition` / `engine.shutdown` / `engine.flameout` |
| `EvaStartSignal` (`:312`) | `Patcher.CreateKittenEvaPostfix` (`:650`) | `kitten.eva_start` |
| `EvaEndSignal` (`:320`) | `Patcher.DisposePrefix`, KittenEva branch (`:530-535`) | `kitten.eva_end` |
| `TumbleSignal` (`:388-390`) | `PolledSignals.PollVehicle` (`:176-180`) | `kitten.tumble`, on the tumbling kitten's own EVA flight, carrying the previous mode through `LocomotionModeName.FromGameName` (`Events/LocomotionModeName.cs:14-23`) |
| `CrewKilledSignal` (`:347`) | `Patcher.KillCrewPrefix` (`:546-576`) | **no event** — it carries the seat read that lets the next `kitten.kia` name a flight |
| `KiaSignal` (`:358`) | `PolledSignals.PollRoster` roster diff (`:277-281`) | `kitten.kia` |
| `FlaggedSignal` (`:346`) | `Patcher.Flag` (`:754`), `Patcher.UniverseDestroyPrefix` (`:678-683`), `PolledSignals.CheckTuning` (`:242-248`) | `flight.flagged` (1..n) |
| `RosterSampleSignal` (`:357`) | `PolledSignals.PollRoster` (`:282`), `PolledSignals.EmitRoster` (`:148`) | `roster.snapshot` |

Frame-derived, no signal: `EventDetector.Observe` on the published `TelemetryFrame` produces
`vehicle.situation`, `vehicle.atmosphere`, `vehicle.orbit`, `vehicle.soi`
(`EventPipeline.ProcessFrame`, `:104-105`); `WindowAccumulator` produces `telemetry.window`
(`:107-110`).

`vehicle.landed` is frame-derived too, but does not reach the outbox from `ProcessFrame`. The
detector emits a `DetectKind.Landing` carrying a `LandingObservation`, and `ProcessFrame` hands it
to `ImpactCorrelator.Landed` instead of `Add` (`EventPipeline.cs:147-149`); the envelope is minted
later, from whichever drain settles the verdict — the `FrameBoundarySignal` case, `DrainFor` at
flight end, or `Drain` at session end. It is the only event type whose value is decided on the
worker but whose *emission* waits on a frame boundary.

An unknown signal subtype is **ignored with a debug log**, never thrown — signals arrive from Harmony
patch bodies and must never kill the worker (`EventPipeline.cs:311-314`).

---

## The registry

`mod/catlog.lib/Events/EventTypes.cs` (names), its `Versions` map (versions). Server mirror
`server/internal/ingest/types.go`'s `knownTypes` for the names, `projector/upcast.go`'s `CurrentVer` +
`currentVer` for the versions. **The two lists agree exactly** — 25 names, same spelling, same
order — and the two version maps must too: a type the mod stamps at a version the server does not
fold is skipped as a future version, which is silent data loss for that type until the server catches
up and a rebuild runs.

**Every type is at `ver` 1.** There is one shape of every event, so `projector.currentVer` is empty
and `projector.Upcasters` has nothing registered (PROJ-100).

| # | `type` | `ver` | outbox kind | Disableable? | Trigger | Feeds |
|---|---|---|---|---|---|---|
| 1 | `session.started` | 1 | 1 | **no — locked** | event | `career` (rewind mark) |
| 2 | `system.discovered` | 1 | **1** | **no — locked** | event (session boundary) | `system`; one-time career→system binding |
| 3 | `system.body` | 1 | **1** | yes | event (system-load survey) | immutable `system_body` catalogue |
| 4 | `flight.started` | 1 | 1 | **no — locked** | polled-discovery | `flight_state` launch facts, `heaviest_launch`, `most_parts`, `biggest_crew`, `biggest_stack` |
| 5 | `flight.ended` | 1 | 1 | **no — locked** | event (+ passive net) | `flight_state`, `kittens_recovered`, `biggest_recovery`, `kittens_to_orbit_and_back`, feed |
| 6 | `flight.flagged` | 1 | 1 | **no — locked** | event (4 of 5) / passive (`tuning`) | `flight_state` → **excludes everything** |
| 7 | `vehicle.situation` | 1 | 1 | yes | passive | `softest_touchdown`, `landed_bodies`, `splashdowns`, `player_body`, `career_body` |
| 8 | `vehicle.atmosphere` | 1 | 1 | yes | passive | `flight_state` space milestone, `fastest_entry` |
| 9 | `vehicle.orbit` | 1 | 1 | yes | passive | `flight_state` orbit milestone, `orbits_achieved`, `highest_apoapsis`, `lowest_orbit`, `roundest_orbit`, `steepest_orbit`, `heaviest_to_orbit`, `fastest_to_orbit`, feed |
| 10 | `vehicle.soi` | 1 | 1 | yes | passive | conditional `flight_state` other-SOI milestone, `soi_bodies`, `fastest_to_<body>`, `bodies_by_1y`, `bodies_by_10y`, `player_body`, `career_body`, feed |
| 11 | `vehicle.rud` | 1 | 1 | yes | event | `rud_total`, `rud_<cause>`, `parts_lost`, `biggest_parts_lost`, `biggest_crew_wreck`, `kittens_wrecked`, feed |
| 12 | `vehicle.impact` | 1 | 1 | yes | event (1-frame hold) | `biggest_lithobrake_survived`, `biggest_impact_energy`, feed |
| 13 | `vehicle.landed` | 1 | 1 | yes | passive (1-frame hold) | conditional `flight_state` landed milestone, `softest_landing`, `landings`, feed |
| 14 | `vehicle.staging` | 1 | 1 | yes | event | `stagings`, `most_stages` |
| 15 | `vehicle.docked` | 1 | 1 | yes | event | `flight_state` docked milestone, `dockings` |
| 16 | `vehicle.undocked` | 1 | 1 | yes | event | — (decoded, counts nothing) |
| 17 | `engine.ignition` | 1 | 1 | yes | passive | `engine_ignitions` |
| 18 | `engine.shutdown` | 1 | 1 | yes | passive | — (decoded, counts nothing) |
| 19 | `engine.flameout` | 1 | 1 | yes | passive | `flameouts` |
| 20 | `kitten.eva_start` | 1 | 1 | yes | event | `evas` |
| 21 | `kitten.eva_end` | 1 | 1 | yes | event | `longest_eva` |
| 22 | `kitten.tumble` | 1 | 1 | yes | passive | `kitten_tumbles`, `botched_landings`, `tumbles_on_<body>`, feed |
| 23 | `kitten.kia` | 1 | 1 | **no — locked** | passive | the impact-board KIA window (rebuild), feed |
| 24 | `roster.snapshot` | 1 | **1** | yes | passive (+1 event) | `distance_travelled`, `top_kitten_distance`, `top_kitten_missions`, `kitten`, `career_kitten` |
| 25 | `telemetry.window` | 1 | **0** | yes | passive | `peak_g_survived`, `max_q_survived`, `fastest_surface_speed`, `fastest_orbital_speed`, `highest_altitude`, `lowest_pass` |

`vehicle.landed` is **not** in `AlwaysReported` — a player may switch it off like any other
non-spine type — and it is `KindEvent`, the default for everything except `telemetry.window`.

Every event additionally lands in `event_census` (10 rows: own type + total, × 5 periods).
Every flight-bearing event also ensures `flight_state` exists and may supply that row's first
nonempty career, whether or not this table lists a more specific flight-state effect.

### Turning a type off — the `[events]` table

`catlog.toml` has an `[events]` table keyed by the wire type name. An **absent key means enabled**,
so the table is empty on a fresh install; the full list ships commented out in the file's header with
the boards each type feeds (`ModConfig.Header`, held in step with `EventTypes.All` by
`TheHeaderDocumentsEveryRegisteredEventType`). Enforcement is two layers — `ModConfig.Normalize` →
`NormalizeEvents`, so the file the player reads back is the truth, and `EventTypeFilter.Create`, so a
hand-built `EventPipelineOptions` cannot express it either — and the filter is *applied* at
`EventPipeline.Add`, which is late on purpose: every detector, tracker, correlator and window
mutation has already happened, so a suppressed type cannot rewind state and cannot change what the
other types say. `EventTypes.AlwaysReported` is the six locked types marked above. MOD-072 and
PROJ-108.

**Nothing here is a wire change.** A batch may always legally omit any type; only an *unknown* type
is rejected (`400 malformed_batch`). The server cannot tell a player who flew nothing from a player
who switched a type off, and does not try.

What a player gives up per type, the nineteen that can be switched off:

| Type off | Boards that stop moving | Other consequence |
|---|---|---|
| `system.body` | none directly | the mandatory `system.discovered` header reports `complete: false`; no catalogue, everywhere badge or future 3D system view can be complete, no body rows or durable marker are written, and a later enabled session may retry |
| `vehicle.situation` | `softest_touchdown`, `landed_bodies`, `splashdowns` | `player_body` and `career_body` stop updating from situation changes. **`vehicle.landed` still fires** — the filter is applied at the pipeline funnel, after detection, so suppressing one of the pair does not suppress the other |
| `vehicle.atmosphere` | `fastest_entry` | — |
| `vehicle.orbit` | `orbits_achieved`, `fastest_to_orbit`, `highest_apoapsis`, `lowest_orbit`, `roundest_orbit`, `steepest_orbit`, `heaviest_to_orbit` | feed rows |
| `vehicle.soi` | `soi_bodies`, `bodies_by_1y`, `bodies_by_10y`, and the whole `fastest_to_<body>` family | `player_body`, `career_body`, feed rows |
| `vehicle.rud` | `rud_total`, every `rud_<cause>` board, `parts_lost`, `biggest_parts_lost`, `biggest_crew_wreck`, `kittens_wrecked` | RUD, lost-vehicle-size and aboard-lost-vehicle projections stop moving. `vehicle.impact.survived` is unaffected — the correlator computes it before `Add` |
| `vehicle.impact` | `biggest_lithobrake_survived`, `biggest_impact_energy` | feed rows |
| `vehicle.landed` | `softest_landing`, `landings` | feed rows. **`landed_bodies` is unaffected** — it reads `vehicle.situation`, which is where the same edge is also reported |
| `vehicle.staging` | `stagings`, `most_stages` | — |
| `vehicle.docked` | `dockings` | — |
| `vehicle.undocked` | none | genuinely unread by any fold today |
| `engine.ignition` | `engine_ignitions` | — |
| `engine.shutdown` | none | genuinely unread by any fold today |
| `engine.flameout` | `flameouts` | — |
| `kitten.eva_start` | `evas` | — |
| `kitten.eva_end` | `longest_eva` | — |
| `kitten.tumble` | `kitten_tumbles`, `botched_landings`, every `tumbles_on_<body>` board | feed rows |
| `roster.snapshot` | `distance_travelled`, `top_kitten_distance`, `top_kitten_missions` | the `kitten` and `career_kitten` tables stop existing for that player. This is why it is `KindEvent` and never pruned |
| `telemetry.window` | `peak_g_survived`, `max_q_survived`, `highest_altitude`, `lowest_pass`, `fastest_surface_speed`, `fastest_orbital_speed` | The **only** `KindPassive` type, so it is the only thing `OutboxDb.Prune` may drop: switching it off leaves a full outbox nothing droppable. Also the highest-volume type, and therefore the most attractive knob. There is no fallback source — `boards.go` explicitly refuses to substitute `roster.snapshot.fastest_ms` |

Two notes that follow from the folds rather than from this table. `scoreable()` treats an event with
**no** flight as scoreable, so the `roster.snapshot`-sourced boards are never touched by the flag
gate at all — turning that type off is the only way to lose them. And every type feeds
`event_census` → `GET /v1/stats`, so disabling any of them makes that player's census under-report;
that is expected and harmless.

---

## The event catalog

Each entry has the same eight blocks: **Wire**, **Payload**, **Detector**, **Game source**,
**Classification**, **Dedup / ordering**, **Server**, **Vectors**. "Classification" is the answer to
*is this event-driven or passive telemetry* — the distinction the player-facing site surfaces as
"something happened" vs "sampled in the background".

---

### `system.discovered`

**Wire.** `"system.discovered"` (`EventTypes.SystemDiscovered`), `ver` 1, outbox kind 1.
`flight` = **null**. `session` and `career` are the freshly established identities for the session
boundary. It is in `AlwaysReported`: without it the server cannot attribute the save to the system
whose bodies and names give its records meaning.

**Payload** — `SystemDiscoveredPayload`

| Key | JSON | Optional? | Meaning |
|---|---|---|---|
| `system` | string | no | Lowercase SHA-256 identity of the canonical raw survey; identical content hashes identically across installs. |
| `id` | string | no | Raw `CelestialSystem.Id`; not lowercased or sanitised. |
| `name` | string | no | Matching `SystemInfo.DisplayName.Value`, printable-US-ASCII sanitised and capped at 64 characters; raw system id is the fallback. |
| `home` | string | no | `HomeBody.Id` through the same canonical lowercase body-name conversion as flight events. |
| `bodies` | integer | no | Count of the materialised `All.OfType<IParentBody>()` snapshot, excluding template vehicles. |
| `complete` | boolean | no | True only when every body row in that count accompanies this header. False means no body list in this boundary — disabled, capped, invalid, or already durably sent — never an empty system. |

**Detector.** `SystemSurvey.Capture` runs once from the `Universe.LoadSystem` postfix and caches a
plain immutable `SystemSnapshot`. There is deliberately no separate survey signal: the snapshot is
carried by `SessionLoadedSignal`, and the session-boundary path resets/mints identities, creates this
header and any body events, then creates `session.started`. The startup and save-load paths use that
same boundary. A null `Universe.CurrentSystem` produces no phantom system/session pair; the later
system-load postfix establishes the boundary.

`complete` is false and no `system.body` rows are made when body reporting is disabled, the filtered
count exceeds `Wire.MaxSystemBodies` (5,000), any other required scalar/axis member is non-finite, or
an orientation quaternion is non-finite or cannot be normalised. There is no truncated or partially
plausible catalogue.

**Game source.** The loaded system is `Universe.CurrentSystem` (`KSA/Universe.cs:92`); id, home and
the mixed live collection are `CelestialSystem.Id`, `HomeBody` and `All`
(`KSA/CelestialSystem.cs:55-61`). Bodies are exactly `All.OfType<IParentBody>()`: `Celestial` and
`StellarBody` implement that interface while `Vehicle` does not (`KSA/Celestial.cs:23`,
`KSA/StellarBody.cs:12`, `KSA/Vehicle.cs:27`). Display metadata is the exact ordinal id match in
`SelectSystem.Systems`, reading `SystemInfo.DisplayName.Value` (`KSA/SelectSystem.cs:18`,
`KSA/SystemInfo.cs:10-11,29`, `KSA/StringReference.cs:9`), with raw id fallback. The complete source
inventory and hash encoding are normative in [ksa-integration.md](ksa-integration.md#system-survey-and-stable-identity).

**Classification.** Event-driven at a session boundary, not passive telemetry. The survey reads KSA
once on the game thread at a load boundary and hands the worker only immutable catlog records; it is
never in the steady 2 Hz vehicle loop.

**Dedup / ordering.** Exactly one header per session, before every body row and before
`session.started`. A header is emitted even after that career/system's body catalogue is durably
marked sent. On a first complete report the outbox transaction appends header → every body →
session, then sets `survey:<career>:<system_hash>`. On a marked report it appends a
`complete: false` header → session; the earlier atomic catalogue is already durable.
Disabled, capped and invalid reports set `complete: false`, append no bodies and never set the
marker, so a later session can retry.

**Server.** The ingest registry preserves the payload verbatim and `systemFold` first-writes the
`system` identity/header, assigns its stable slug, and binds the career to the hash once. Repeated
matching headers may only promote `reported_complete` false→true; identity conflicts retain the
first row. Effective completeness additionally requires the stored body-row count to equal
`bodies`. The event moves no leaderboard directly.

**Vectors.** `batch-001.ndjson` line 1 is a complete header followed by body rows; line 5 is a
`complete: false` header with no body rows. Registry-coverage tests require this type at its current
version, and payload round-trip tests pin every required key.

---

### `system.body`

**Wire.** `"system.body"` (`EventTypes.SystemBody`), `ver` 1, outbox kind 1. `flight` = **null**.
It is configurable, but never droppable once appended: a catalogue is immutable state, not passive
telemetry.

**Payload** — `SystemBodyPayload`

| Key | JSON | Optional? | Meaning |
|---|---|---|---|
| `system` | string | no | Same system hash as the preceding header; makes row order semantically irrelevant. |
| `body` | string | no | Canonical lowercase body-name join key. |
| `name` | string | no | Printable-ASCII sanitised raw `Astronomical.Id`; KSA exposes no separate body display-name field. |
| `class` | string | no | Concrete runtime `Astronomical.Class`; opaque, **open set**, never server-inferred. |
| `kind` | string | no | One of `star`, `planet`, `moon`, `minor`, `other`; the fixed semantic mapping below. |
| `rank` | integer | no | Depth from this body's own root. Multiple roots are valid. |
| `parent` | string | yes | Canonical lowercase direct-parent key; absent for every root. |
| `radius_m` | number | no | Mean radius from centre, metres. |
| `mass_kg` | number | no | Mass, kilograms. `mu` is omitted because KSA derives it as mass × `6.6743E-11`. |
| `soi_m` | number | no | Sphere-of-influence radius, metres; a root star's `+Inf` is the sole representational exception and is sent as `0`. |
| `atmo_m` | number | no | Atmosphere height, metres; zero for airless bodies. |
| `ocean_m` | number | no | Ocean level above mean radius, metres; zero when absent. |
| `angvel` | number | no | Signed angular velocity, radians/second; negative is retrograde. |
| `axis` | object `{x,y,z}` | no | Finite rotation axis in the body-centred ecliptic frame. |
| `ccf_to_cce_t0` | object `{x,y,z,w}` | no | Finite normalised body-fixed→body-centred-ecliptic orientation at career time zero. |
| `sma_m`, `ecc`, `inc_deg`, `lan_deg`, `argp_deg`, `t_pe` | numbers | group | Six-value orbital-shape group: all present or all absent. Always absent for roots and absent as a group if any member is non-finite. Angles are degrees; `t_pe` is absolute career-clock time. |
| `period_s` | number | yes | Orbital period in seconds; independently absent when non-finite, including unbound conics. |

The quaternion is normalised and sign-canonicalised: the first non-zero component in `w,x,y,z`
order is positive, and negative zero becomes positive zero. If it cannot be normalised the whole
survey is incomplete. `body`, `parent` and the header's `home` use precisely the same normaliser.

**Detector.** The body records are the materialised immutable result of the same system-load survey
as `system.discovered`. They are included only for an unmarked `(career, system hash)` whose survey
is complete and whose `system.body` setting is enabled. There is no per-frame detector and no
separate signal.

**Game source.** Each row comes from an `IParentBody` that is also an `Astronomical`. The exact
field-by-field inventory is in [ksa-integration.md](ksa-integration.md#exact-source-inventory):
`MeanRadius`, `Mass`, `SphereOfInfluence`, atmosphere/ocean references, angular velocity,
`GetRotationAxisCce()`, `GetCcf2Cce(SimTime.Zero)`, and non-root `Orbit` elements. The semantic kind
mapping is exact: `StellarBody` → `star`; `PlanetaryBody`, `TerrestrialBody` or `AtmosphericBody`
with a direct stellar parent → `planet`, and with a non-stellar parent → `moon`; `MinorBody`,
`Asteroid`, `Comet`, `PeriodicComet` or `InterstellarComet` → `minor`; every future class → `other`.

**Classification.** Event-driven catalogue capture at system load. It is not sampled telemetry.
KSA's celestial elements are immutable: `OrbitData` is a readonly struct of readonly fields assigned
by its constructor, the per-frame worker recomputes state vectors rather than elements, and
celestials are not serialised into the save. Therefore one body survey per `(career, system hash)`
is the complete answer for that career, not an approximation that can become stale.

**Dedup / ordering.** The client orders the immutable snapshot by raw body id ordinal for hashing
and stable emission. Every row follows its `system.discovered` header and precedes
`session.started`. The durable marker is committed only in the same transaction after every row is
in the outbox. A crash may cause a resend but may never cause a mark-without-rows loss. A missing
marker after local state loss also resends safely.

**Server.** The typed decoder uses pointers for `parent`, all six shape members and `period_s`, so
absence cannot collapse into zero. `systemFold` inserts the row first-write under `(hash, body)` and
ignores later duplicates, including differing ones; it does not invent a placeholder header when a
body arrives first. `class` remains opaque with no allow-list. No leaderboard reads the event
directly; it supplies catalogue/everywhere state and future 3D placement.

**Vectors.** `batch-001.ndjson` line 2 is a root with absent parent and absent orbital group; line 3
is a bound orbiting body with all six orbital values and finite period; line 4 is an unbound body
with all six orbital values and `period_s` absent. All three carry finite normalised orientations.
Survey unit tests separately pin quaternion identity, `q`/`-q`, 180-degree `w = 0`
canonicalisation and non-finite period omission; the payload round-trip check pins the optional-key
sets.

---

### `session.started`

**Wire.** `"session.started"` (`EventTypes.cs:18`), `ver` 1 (`:91`), outbox kind 1.
`flight` = **null**. `session` = the freshly minted session ULID.

**Payload** — `SessionStartedPayload`, `Events/Payloads.cs:23-26`

| Key | Type | Optional | Source |
|---|---|---|---|
| `mod_ver` | string | no | `CatlogRuntime.ModVersion`, from `AssemblyInformationalVersionAttribute` with the `+sha` suffix stripped (`CatlogRuntime.cs:642-660`); `"0.0.0"` on failure. |
| `game_build` | string | no | `VehicleTelemetry.GameBuild()` (`:60-77`) — `VersionInfo.Current.VersionString` with the leading `v` stripped, e.g. `"2026.8.5.5168"`; **`"unknown"`** if the read throws. Anchor `KSA/VersionInfo.cs:115`, format `:143`. |
| `install` | string (ULID) | no | `EventPipelineOptions.InstallId` (`EventPipeline.cs:10`) ← `ModPaths.LoadOrCreateInstallId()`. Stable per machine; also the salt for `kid` and `career`. **Dropped by `Redact` before any public response.** |

**Detector — two distinct paths.**

1. **Process start**, once, before anything else: `EventPipeline.SessionStarted(simT, wallMs)`
   (`:85-93`) from `CatlogRuntime.Start` (`:453`). Uses `_options.ModVersion`/`GameBuild`.
2. **Save-load / new-game boundary**: `EventPipeline.OnSessionLoaded` (`:300-315`) via
   `SessionLoadedSignal`. It **resets the whole pipeline first** — new `EventDetector`, new
   `WindowAccumulator`, new `ImpactCorrelator`, clears live vehicles and session flags, then
   `Tracker.NewSession(loaded.CareerId)` (`:305-310`) — and only then emits. Uses the signal's own
   `ModVersion`/`GameBuild`.

**Game source.** Trigger patches, both → `Patcher.SessionBoundaryPostfix` (`Patcher.cs:691-701`):

- **postfix** on `Universe.DeserializeSave(UniverseData)` — `KSA/Universe.cs:2140`, installed
  `Patcher.cs:272-274`. Runs *after* `CurrentSystem.DestroyAllVehicles()`; a true teardown+rebuild
  boundary; swaps `KittenRoster` at `KSA/Universe.cs:2178`.
- **postfix** on `Universe.LoadSystem(string id)` — `KSA/Universe.cs:167`, installed
  `Patcher.cs:282-285`. Sole caller is the boot path `KSA/Program.cs:965`.

Career adoption happens **before** the boundary: a **prefix** on `UncompressedSave.Load()`
(`KSA/UncompressedSave.cs:45`, installed `Patcher.cs:298-300` → `VehicleTelemetry.AdoptSaveCareer`)
— a prefix, not a postfix, because `Load()` itself calls `Universe.DeserializeSave` at
`KSA/UncompressedSave.cs:57`. A **postfix** on `UncompressedSave.Make(string)` (`:104`, installed
`Patcher.cs:309-311`) carries the career through a first save / "save as". A **prefix** on
`Universe.LoadSystem` → `VehicleTelemetry.BeginUnsavedCareer()` (`Patcher.cs:704-714`).

**Classification.** **EVENT-DRIVEN** (process start + a discrete state transition). No hysteresis, no
debounce.

**Dedup / ordering.** One per session by construction. A session start followed by a save load
legitimately produces two `session.started` events with *different* `session` ULIDs
(`CatlogRuntime.cs:450-453`).

**Server.** `careerFold` (`stats/career.go:60-81`) treats `session.started` as the **only** moment a
career rewind can be detected: `MarkRewound(playerID, career, simT)` fires **before** advancing the
high-water mark, and only when the career already exists and `max_sim_t > sim_t`
(`batch.go:394-405`). Comparing only at session boundaries is why the rule needs no epsilon
(`career.go:32-38`). **The mark excludes nothing and scores nothing.**

**Vectors.** `contracts/testdata/batches/batch-001.ndjson` line 6 — the always-null `flight`.

---

### `flight.started`

**Wire.** `"flight.started"` (`EventTypes.cs`), `ver` 1, kind 1. `flight` = the newly minted (or
re-resolved) flight ULID.

**Payload** — `FlightStartedPayload`, `Payloads.cs:63-100`

| Key | Type | Units | Source |
|---|---|---|---|
| `vehicle_name` | string | — | `Ids.SanitizeVehicleName(created.VehicleName)` (`EventPipeline.cs:471`). The signal's `VehicleName` **is the vehicle id** — KSA has no separate display name (`PolledSignals.cs:101`). |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` (`PolledSignals.cs:102`) → lowercase `IParentBody.Id`, or `"unknown"`. |
| `mass_kg` | number | kg | `VehicleTelemetry.MassKg` (`PolledSignals.cs:103`, helper `VehicleTelemetry.cs:742-750`) ← `Vehicle.TotalMass` (a **float**, `KSA/Vehicle.cs:551`), `Sanitize.Finite`d. 0 when unreadable. |
| `part_count` | int | count | `VehicleTelemetry.PartCount` (`PolledSignals.cs:104`) → `Vehicle.Parts.Count` (`KSA/PartTree.cs:89`). 0 when unreadable. |
| `crew_count` | int | count | **Occupied** seats, not seat count: `VehicleTelemetry.CrewCount` (`PolledSignals.cs:105`, helper `VehicleTelemetry.cs:642-661`) iterates `Vehicle.Crew` (`ReadOnlySpan<IVASeat>`, `KSA/Vehicle.cs:373`) counting `seat.AssignedKittenHash != KeyHash.Zero`. **A `KittenEva` always returns 1**. |
| `kids` | array of string | — | `VehicleTelemetry.CrewNames` (`PolledSignals.cs:110`, helper `VehicleTelemetry.cs:672-694`) — the same seat walk as `crew_count`, resolved through `Universe.KittenRoster.Find(KeyHash)`, then hashed to a 16-char `kid` per §4.7. In seat order. **Always present**; `[]` when uncrewed, so a reader never has to tell "nobody aboard" from "the mod did not say". Read **once per vehicle on first sight**, in `PolledSignals.Track`, not per tick. |
| `stage_count` | int | count | `VehicleTelemetry.StageCount` (`PolledSignals.cs:111`, helper `VehicleTelemetry.cs:492-500`) → `Vehicle.Parts.SequenceList.Count` (`KSA/PartTree.cs:29`, `KSA/SequenceList.cs:99`). `0` when unreadable, which is a real value here rather than a lie — a vehicle genuinely can have no sequences. **Churn risk High**: `SequenceList` was very nearly rewritten in 5168. |
| `engine_count` | int | count | **OPTIONAL — absent when unreadable.** `VehicleTelemetry.EngineCount` (`PolledSignals.cs:112`, helper `VehicleTelemetry.cs:715-734`) → `Vehicle.Parts.Modules.Get<EngineController>().Length`: installed rocket-engine controllers, active or not. Present `0` is the meaningful fact that none were installed when this flight began. `[KsaAnchor]` churn risk Medium (`KSA/ModuleList.cs:164`). |
| `lat` | number | degrees | **OPTIONAL — the key is absent when unreadable.** `VehicleTelemetry.Latitude` (`PolledSignals.cs:113`) → `Celestial.GetLatitudeFromCce(Vehicle.GetPositionCce())` (`KSA/Celestial.cs:698`, `KSA/Vehicle.cs:2414`), already in degrees. Requires `Orbit.Parent is Celestial` — the method is declared on `Celestial`, not on `IParentBody`, so the type test is mandatory rather than defensive. |
| `lon` | number | degrees | **OPTIONAL.** `VehicleTelemetry.Longitude` (`PolledSignals.cs:114`) → `Celestial.GetLongitudeFromCce` (`KSA/Celestial.cs:733`), same rule. |

`LaunchGameTime` rides on the signal (`GameSignal.cs:195`) but is **not** on the payload — it is half
of the flight identity only.

**Why `engine_count` is optional while `stage_count` is not.** A present engine count of 0 is the
fact this field exists to preserve: no engine was installed at flight start. Turning a failed read
into 0 would make “unknown” indistinguishable from that fact, so failure omits the key. A zero stage
count is already both a legal vehicle and the fallback the existing `biggest_stack` gate refuses.
Latitude and longitude are omitted for the analogous reason that 0 is a real place — the equator or
prime meridian — rather than a missing answer. MOD-078 and MOD-082.

**Detector.** `EventPipeline.OnVehicleCreated` (`:464-493`). Resolves the flight with
`Tracker.FlightFor(created.VehicleId, created.LaunchGameTime)` (`:466`), emits `flight.started`, then
**replays every session-wide flag onto the new flight** as `flight.flagged` with detail
`"session-wide flag"` (`:482-490`). `EngineCount` passes through unchanged at `:478`, preserving
null separately from 0.

**Game source — polled, not patched (deliberate).** `PolledSignals.Track` (`mod/catlog/PolledSignals.cs:90-114`)
raises `VehicleCreatedSignal` the first time catlog *sees* a vehicle id. Two call sites:

1. the 2 Hz sample pass — `PolledSignals.Poll` → `Track` (`:140`), over
   `VehicleTelemetry.CollectVehicles`, which walks `Universe.CurrentSystem.All.UnsafeAsList()`
   type-testing for `Vehicle` (`VehicleTelemetry.cs:918-930`; `KittenEva : Vehicle`, so EVA kittens
   are included);
2. **ahead of any vehicle-scoped Harmony signal**, via `Patcher.Track` (`Patcher.cs:826-838`), which
   drains the resulting signals into the bridge *before* the signal it was called for.

Why not patch the registration hook: `CelestialSystem.Register` sees a half-built vehicle where every
read throws (B6), and a vehicle created and destroyed inside one 0.5 s sample interval would
otherwise emit a RUD against a flight with no `flight.started` (`docs/mod.md:273-276`).

`engine_count` is read in that same once-per-vehicle tracking step. It counts
`EngineController` modules, not rocket cores or nozzles: their controller interface can instead be
an RCS thruster controller, so counting either list would falsely call attitude thrusters engines.
The count says what was installed at this boundary, not what later produced velocity. RCS,
decoupler springs and docking pushoff can accelerate an engine-less vehicle; if a vehicle sheds its
engines, the continuing piece is a new vehicle and therefore a new flight with its own count.

**Classification.** **PASSIVE-DISCOVERED, event-shaped.** The trigger is "catlog saw a vehicle id it
has not seen", evaluated at 2 Hz and also on demand from patch bodies. No debounce; the
`_vehicles.ContainsKey(id)` check (`PolledSignals.cs:90`) is the once-only latch.

**Dedup / ordering.** One per `(vehicle_id, LaunchGameTime)`. Because `FlightFor` learns a NaN→real
`LaunchGameTime` in place (`FlightTracker.cs:102-103`), a flight ULID minted earlier by an EVA start
or a flag is adopted rather than replaced.

**Server.** `FlightStarted.EngineCount` is a Go `*int` (`stats/payload.go:48-58`). `flightFold`
passes it with `part_count`, `mass_kg` and the event sequence to `StartFlight`
(`stats/flight.go:131-138`), which persists the exact launch facts in `flight_state`: migration
`0009_flight_engine_count.sql` owns nullable `engine_count`, and migration
`0010_flight_facts.sql` owns nullable `part_count` / `launch_mass_kg` plus the milestone and career
columns. This creates
the `flight_state` row every board consults. `launchFold` (`stats/boards.go`), registered **four**
times, then takes the same payload onto `heaviest_launch` (`mass_kg`), `most_parts` (`part_count`),
`biggest_crew` (`crew_count`) and `biggest_stack` (`stage_count`). Each is gated
`> 0`, and for the three integer fields that gate **is** §4.2's `>= 1`: all four values are written
as 0 rather than omitted when the read failed, so a zero is an unreadable vehicle and not an empty
one. The gate matters most on `stage_count`, the highest-risk read of the four, which is why the board reads
a value rather than the envelope (PROJ-094). One shared context, seven keys —
`{"body", "flight", "vehicle", "mass_kg", "part_count", "crew_count", "stage_count"}` — so the four
rows of one launch describe the same vehicle rather than four partial views of it.

No current fold scores or copies `engine_count` into a board context. It is retained in
`flight_state` for the later challenge projection. A missing `flight.started` or an absent field
remains SQL `NULL`; explicit 0 remains 0 (`stats/batch.go:315-330`).

`started_seq` is the sequence of the actual start event, and future composite consumers use
`FlightState.HasStartFactAt(candidate.seq, fact.Valid)`: the start must exist, must not be later than
the candidate, and the required nullable fact must be present. This deliberately declines an early
candidate that incremental folding could not have joined, even though rebuild pass 1 eventually
knows the completed flight row.

`kids`, `lat` and `lon` are decoded (`[]string`, `*float64`, `*float64`) and read by no fold. The
first fold that reads `kids` must not treat a nil slice as "uncrewed": nil is a `ver` 1 row, `[]` is
an uncrewed one.

**Vectors.** `batch-001.ndjson` line 7 is crewed with `kids` populated, `stage_count` 3 and
`lat` / `lon` present, while optional `engine_count` is absent. Line 22 is an uncrewed probe with
`kids: []`, `stage_count: 0`, explicit `engine_count: 0` and absent `lat` / `lon`. The pair pins
unknown separately from a real “no engines” zero.

---

### `flight.ended`

**Wire.** `"flight.ended"` (`EventTypes.cs`), `ver` 1, kind 1. `flight` = the flight being closed.

**Payload** — `FlightEndedPayload`, `Payloads.cs:72-83`

| Key | Type | Values |
|---|---|---|
| `reason` | string | `"recovered"` \| `"destroyed"` \| `"despawned"` (`EventTypes.ToWire(FlightEndReason)`) |
| `crew_count` | int | occupied seats at the moment it ended (`Patcher.cs:538`). **0** on the silent-removal safety-net path (`PolledSignals.cs:228`), indistinguishable on the wire from a genuinely empty vehicle. |
| `kids` | array of string | Who was aboard at the end, in seat order. Always present; `[]` when nobody was, and `[]` on the safety-net path for the same reason `crew_count` is 0 there. |
| `body` | string | Lowercase parent body, or the literal `"unknown"`. |
| `lat` / `lon` | number | **OPTIONAL** — degrees, absent when unreadable. |

**Why the event gained a `body` at all.** Without one a landing site is unplaceable: the flight's last
`telemetry.window` may be a whole window old and the vehicle may have changed SOI since. The reads are
real on every ordinary path, because the removal hook is a **prefix** — the vehicle is fully intact
and its orbit, parent and seats are all readable, and that is the last instant at which they are.

**The one path that cannot read them** is `PolledSignals.Prune`, the silent-removal safety net, where
there is no vehicle object left to ask. It sends `body: "unknown"`, `kids: []` and omits the
position. The last-sampled body and position are deliberately **not** cached to fill it in: an id can
vanish because it was renamed or merged into another vehicle, and "where it was half a second ago" is
a different claim from "where it ended". MOD-079.

`"unknown"` is an ordinary member of the open `body` set, not a sentinel the server may reject — but
it is a name, so there must be no `landed_on_unknown` board, and it is excluded wherever a real body
is required.

**Detector** — `EventPipeline.EndFlight` (`:366-405`). The order inside is load-bearing:

1. if `reason == Destroyed`, tell the correlator (`:379-380`) — a *manual* destroy has no
   `RudSignal`, so without this a player could scuttle after every hard landing to bank a `survived`
   record;
2. resolve that vehicle's outstanding impacts **while the flight id is still live** (`:390-391`) →
   emits `vehicle.impact` **before** `flight.ended`;
3. flush the partial telemetry window (`:394-396`) → emits `telemetry.window` **before**
   `flight.ended`;
4. emit `flight.ended` (`:397-400`);
5. `Tracker.EndFlight` + `_detector.Forget` + `_windows.Forget` (`:402-404`).

**Game source — one emitter at the single removal choke point.** A **prefix** on
`Vehicle.Dispose(bool endMission)` — `KSA/Vehicle.cs:3510` (deregisters at `:3520`), installed
`Patcher.cs:169-171`, body `:496-544`. The parameterless `Dispose()` delegates to this overload, so
patching the `bool` one catches every path exactly once (B12).

The *reason* is decided by intent flags earlier patches set:

| Reason | Decided by |
|---|---|
| `recovered` | the `Recovering` set — a **prefix** on `Vehicle.Recover()` (`KSA/Vehicle.cs:2765`, installed `Patcher.cs:159-161`, body `:479-494`) |
| `destroyed` | the `Destroying` set — **prefixes** on `Universe.DestroyVehicleFromEvent` (`Patcher.cs:392`) and `Vehicle.KillCrew()` (`:555`) |
| `despawned` | neither — docking merge, EVA board, save teardown (`Patcher.cs:520-523`) |

Guard: `if (!runtime.Forget(id)) return;` (`Patcher.cs:513`) — only a flight catlog actually opened
is closed, so no `flight.ended` is invented for a vehicle catlog never saw.

**Second emitter — the silent-removal safety net.** `PolledSignals.Prune` (`:210-230`) raises
`VehicleRemovedSignal(..., Despawned, crewCount: 0)` for anything still tracked but absent from the
live roster: a `CelestialSystem.Rename` (deregister→rename→register, which is not a dispose) or a
missed docking merge. Better than leaking an open flight.

**Classification.** **EVENT-DRIVEN** (Harmony prefix), with a **PASSIVE** 2 Hz reconciliation net.

**Dedup / ordering.** `runtime.Forget(id)` (`CatlogRuntime.cs:284` → `PolledSignals.cs:113`) is the
once-only latch shared by both emitters.

**Server.** `flightFold` → `EndFlight(reason)` (`stats/flight.go:114-119`). Feeds
`kittens_recovered` (`reason == "recovered" && crew_count >= 1` → `addCount(+crew_count)`,
`stats/boards.go:1010-1026`), `biggest_recovery` (`recoveryFold`, `:726-754` — the same eligibility,
but `putRecord` of the single largest `crew_count` rather than a running sum) and the feed
(`stats/feed.go:78-84`). The two recovery boards are deliberately different achievements: forty solo
recoveries and one nine-seat station crew return are the same number on `kittens_recovered` and very
different on `biggest_recovery`. Although `flight.ended` now carries `body`, `biggest_recovery` keeps
its established context source from `flight_state` via `flightBody` — free, because `scoreable` has
already loaded that row into the batch cache. The recovered event's ordered `kids` list now also
feeds `kittens_to_orbit_and_back`; its exact set fold is documented below.

`flight_state.ended_reason` is what the rebuild refinement of `peak_g_survived` **and**
`max_q_survived` tests (`Recovered()`, `stats/flight.go:88`).

**`flight.ended.body` is decoded and unused.** `flight_state.body` still comes from `flight.started`
via `flightFold`, so a flight whose `flight.started` was never folded still has an empty body even
though its `flight.ended` now carries one. Reading it would be a rebuild-only improvement and is out
of scope; `lat` and `lon` are likewise decoded and read by nothing. `kids` is authoritative input to
`kittens_to_orbit_and_back`.

**Vectors.** `batch-001.ndjson` lines 27 (`recovered`, crew and position), 28 (the safety net: `despawned`, `crew_count` 0, `kids` `[]`, `body: "unknown"`, no position) and 32 (`destroyed`).

---

### `flight.flagged`

**Wire.** `"flight.flagged"` (`EventTypes.cs:27`), `ver` 1, kind 1. `flight` = the flagged flight —
note `EventPipeline.Vehicle(...)` uses the **minting** `Tracker.FlightFor(vehicleId)` (`:407-408`),
and `FlightTracker.AddFlag` does the same via `Register` (`FlightTracker.cs:131-137,154-159`), so a
flag can create a flight that has no `flight.started`.

**Payload** — `FlightFlaggedPayload`, `Payloads.cs:51-53`

| Key | Type | Values |
|---|---|---|
| `flag` | string | `teleport` \| `refuel` \| `resource_edit` \| `console` \| `tuning` |
| `detail` | string | free text; see the table below |

**Detector** — `EventPipeline.OnFlagged` (`:343-364`).

- **Vehicle-scoped** (`FlaggedSignal.VehicleId != null`): `Tracker.AddFlag` dedupes per
  `(flight, flag)` and returns false on a repeat, so twenty consecutive teleport frames emit exactly
  one event (`:347-350`; `FlightTracker.cs:24-28`).
- **Session-wide** (`VehicleId == null`): added to `_sessionFlags`, then one event per currently open
  flight (`:355-363`), **and** replayed onto every flight started later (`:330-339`) with detail
  `"session-wide flag"` rather than the original text.

**Game source — five distinct producers.**

| flag | Patch point (KSA) | Kind | Body | `detail` |
|---|---|---|---|---|
| `teleport` | `InputEvents.TeleportInputData.Apply()` — `KSA/InputEvents.cs:295` | **prefix** (`Patcher.cs:240-242`) | `:658-663`; `TeleportInputData` is a **struct**, so `__instance` is by-ref | `"the vehicle was teleported by a player command"` |
| `refuel` | `Vehicle.RefillConsumables()` — `KSA/Vehicle.cs:2981` | **prefix** (`:250-252`) | `:665-666` | `"Vehicle.RefillConsumables was called"` |
| `resource_edit` | `Vehicle.DepleteConsumables()` — `KSA/Vehicle.cs:2988` | **prefix** (`:254-256`) | `:668-669` | `"Vehicle.DepleteConsumables was called"` |
| `console` | `Universe.Destroy(string id)`, the `[TerminalAction("destroy")]` — `KSA/Universe.cs:1107` | **prefix** (`:263-265`) | `Patcher.UniverseDestroyPrefix`, `:671-689` | ``"the terminal `destroy` command was used on this vehicle"`` |
| `tuning` | none — **polled** | 2 Hz | `PolledSignals.CheckTuning`, `:232-249` | `"KittenLocomotionTuning.Current.TumbleSpeedGate is <x>, stock is 6.5"` |

**Why `teleport` hooks `TeleportInputData.Apply` and not `Vehicle.Teleport`** (recorded verbatim at
`Patcher.cs:229-237`): `Vehicle.Teleport` (`KSA/Vehicle.cs:2031`) has three callers and only one is
cheating — `KSA/EVADoor.cs:158` teleports a kitten as part of *normal* EVA egress and
`KSA/VehicleEditor.cs:2193` teleports the split vehicle on an editor decouple. Flagging there would
exclude ordinary play from every board. `TeleportInputData.Apply` is the player-command path only,
and both producers (`Vehicle.TeleportToLocation` `KSA/Vehicle.cs:3920`, the Set Orbit debug window
`:4724`) funnel through it.

**Why `tuning` exists.** It reads `KittenLocomotionTuning.Current.TumbleSpeedGate`
(`VehicleTelemetry.cs:607-617`, anchor `KSA/KittenLocomotionTuning.cs:33,59,77`, **churn risk High,
new in 5168**) and compares to `StockTumbleSpeedGate = 6.5f` (`:40`). `Current` is a mutable public
static that the game's own `KittenTuningWindow` live-edits by ref via `ImGui.DragFloat`. Without this
flag the `kitten_tumbles` board is trivially forgeable. Latched once per session by `_tuningFlagged`
(`PolledSignals.cs:234,241`).

**Classification.** `teleport`/`refuel`/`resource_edit`/`console`: **EVENT-DRIVEN** (Harmony
prefixes). `tuning`: **PASSIVE** (2 Hz poll), gated by an exact float inequality with a
once-per-session latch.

**Dedup.** Per `(flight, flag)` via a `HashSet<FlightFlag>` on the `FlightRecord`
(`FlightTracker.cs:167`); session-wide flags additionally via `_sessionFlags`
(`EventPipeline.cs:55,375`).

**Server — this is the exclusion mechanism.** `flightFold` ORs `FlagBit(flag)` into
`flight_state.flags` (`stats/flight.go:120-125`). Bits: 0 `teleport`, 1 `refuel`, 2 `resource_edit`,
3 `console`, 4 `tuning`, **5 `other`** for an unrecognised value (`:29,34-49`, PROJ-002) — failing
open would make every future flag a scoring loophole. `scoreable` (`stats/fold.go:205-220`) then
suppresses **every board**, the feed, and the raw event views for that flight. The one exception is
`distance_travelled`, whose source event carries no flight at all.

**Vectors.** `batch-001.ndjson` line 29 — a `tuning` flag on a flight that has no `flight.started`.

---

### `vehicle.situation`

**Wire.** `"vehicle.situation"` (`EventTypes.cs`), `ver` 1, kind 1. Detector kind
`DetectKind.Situation = 0` (`EventDetector.cs:11-12`).

**Payload** — `VehicleSituationPayload`, `Payloads.cs:103-113`

| Key | Type | Units | Source |
|---|---|---|---|
| `from` | string | — | `state.ReportedSituation` — the situation **last actually reported on the wire**, not the previous sample (`EventDetector.cs:241`) |
| `to` | string | — | `curr.Situation` (`:242`) |
| `body` | string | — | `curr.Body` |
| `altitude_m` | number | metres above the parent's **mean radius** | `Vehicle.GetBarometricAltitude()` (`KSA/Vehicle.cs:2840-2843` = `PositionCci.Length() - Parent.MeanRadius`). **Not terrain-relative** — that is `GetRadarAltitude()` at `:2845`. |
| `surface_speed_ms` | number | m/s | `Vehicle.GetSurfaceSpeed()` (`KSA/Vehicle.cs:2759`). **Never `NavBallData.Speed`** — that is frame-dependent on the player's navball mode (`VehicleTelemetry.cs:436-437`). |
| `orbital_speed_ms` | number | m/s | `Vehicle.OrbitalSpeed` (`KSA/Vehicle.cs:581` = `GetVelocityCci().Length()`), body-centred inertial |
| `radar_alt_m` | number | metres above **the terrain or ocean surface directly beneath** | **OPTIONAL — absent when unreadable.** The companion to `altitude_m` and deliberately *not* `Vehicle.GetRadarAltitude()`; see [ksa-integration.md](ksa-integration.md) §1 for why, and for the three guards that each yield *absent* rather than 0. |

**Detector** — `EventDetector.CheckSituation` (`:236-304`):

```
if (baseline || ReportedSituation is null) { ReportedSituation = curr.Situation; return; }  // seeds, emits nothing
if (ReportedSituation == curr.Situation) return;
if (!CanFire(Situation, curr.SimT)) return;                                                 // 2 s debounce
emit; ReportedSituation = curr.Situation; MarkFired(...)
```

The edge is taken off the **latch**, not the raw previous snapshot — that is what makes the 2 s
debounce rate-limiting rather than lossy: a suppressed transition is re-detected on the next sample
and reported *from* the last state that reached the wire (`:121-127`).

**`vehicle.landed` is emitted from inside this same gate**, immediately after `vehicle.situation`,
whenever the transition is a touchdown. One detector, two events, one debounce — see
[`vehicle.landed`](#vehiclelanded).

**Game source.** `Vehicle.Situation` (`KSA/Vehicle.cs:533`, `=> _props.Situation`), enum
`KSA/Situation.cs:3-13`, mapped by `VehicleTelemetry.SituationName` (`:895-906`) inside
`VehicleTelemetry.Sample` (`:148`), sampled at **2 Hz** from `CatlogRuntime.SamplePass` (`:488`).

**Classification.** **PASSIVE** (2 Hz poll → worker prev/curr comparator). Gate: 2.0 sim-second
debounce per (vehicle, kind); baseline emits nothing. A backwards `sim_t` jump calls
`VehicleDetectState.Rebaseline()` (`EventDetector.cs:208-209, 96-105`), dropping every latch and
timer — a save was loaded.

**Server.** Three boards, all of them reading the transition through
[`stats/situation.go`](#vehiclesituationfrom--to--open-set-9-emitted-values)'s contact table rather
than switching on the names:

1. `touchdownFold` (`stats/boards.go:820-857`) → `softest_touchdown`, a `putBest` (min) of
   `surface_speed_ms`. `to` must have surface contact, `from` must be a **known** contact-free
   situation (`freefall` or `maneuvering`), and the speed must be `> 0`. Requiring `from` to be known
   and not merely contact-free is what separates a landing from an unreadable transition; requiring
   it to be contact-free is what keeps `rolling` → `landed` as a rover stops, or `landed` →
   `dragging` on a slope, off a board they would otherwise own at almost zero speed. Context
   `{"body", "flight", "from", "to", "altitude_m"}`.
2. `landedBodiesFold` (`:952-982`) → `landed_bodies`, the count of distinct bodies with **any**
   surface contact — terrain, ocean or both, because splashing down on a body is arriving at it.
   Set-backed exactly like `soi_bodies`: `b.AddBody(playerID, "landed", body, seq)` reports whether
   the `player_body` row was new and only then is the counter advanced, so there is no `count(*)` and
   it stays correct under replay (PROJ-011). Requires a non-empty `body`.
3. `splashdownFold` (`:984-1008`) → `splashdowns`. `to` must be **pure** ocean contact (`sailing` or
   `floating`); `dragging` and `bottomed` touch terrain too and are a hull on a shoreline rather than
   a capsule under a parachute. `from` must be contact-free, which is what makes this an *arrival* —
   without it a boat crossing the `sailing` ↔ `floating` boundary as it goes on and off rails would
   count a splashdown every time, and the 2 s debounce only rate-limits that rather than stopping it.

`orbital_speed_ms` and `radar_alt_m` are decoded and read by nothing. The terrain-relative altitude
that *does* score is the aggregate on `telemetry.window`, which `lowest_pass` reads — one situation
change is not a pass over anything.

**`landed_bodies` reads this event rather than `vehicle.landed`**, and that is a decision rather
than an oversight (PROJ-097). See [Boards](#fold-detail-board-by-board).

**Vectors.** `batch-001.ndjson` line 8 — `radar_alt_m` present.

---

### `vehicle.atmosphere`

**Wire.** `"vehicle.atmosphere"` (`EventTypes.cs:33`), `ver` 1, kind 1. Detector kinds
`AtmosphereEntered = 1`, `AtmosphereExited = 2` (`EventDetector.cs:14-17`) — **they debounce
independently.**

**Payload** — `VehicleAtmospherePayload`, `Payloads.cs:75-79`, built `EventDetector.cs:331-342`

| Key | Type | Units | Source |
|---|---|---|---|
| `dir` | string | — | literal `"entered"` (`:313`) or `"exited"` (`:326`) |
| `body` | string | — | `curr.Body` |
| `speed_ms` | number | m/s | `curr.SurfaceSpeedMs` — surface-relative, **not** orbital |
| `dyn_pressure_pa` | number | Pa | `curr.DynPressurePa` |

**Detector** — `EventDetector.CheckAtmosphere` (`:286-329`), a **Schmitt trigger**:

```
atmoHeight    = curr.AtmoHeightM;  hasAtmosphere = atmoHeight > 0
baseline seed: InAtmosphere = hasAtmosphere && curr.AltitudeM < atmoHeight     (emits nothing)
if !inside:  require hasAtmosphere && curr.AltitudeM <  atmoHeight * (1 - 0.02)  → "entered"
if  inside:  exit when !hasAtmosphere || curr.AltitudeM > atmoHeight * (1 + 0.02) → "exited"
```

The hysteresis band is read off the **latch**, which is the whole point of a Schmitt trigger — a bare
threshold plus the 2 s debounce only rate-limits flapping, it does not suppress it (`:300-303`).
Changing to an airless parent counts as an exit (`:319-320`).

**Game source.** `AtmoHeightM` = `VehicleTelemetry.AtmosphereHeightM(parent)` (`:296-307`):
`IParentBody.GetAtmosphereReference()?.Physical.Height.InMeters()`; **0 when airless**. Anchors
`KSA/IParentBody.cs:57`, `KSA/AtmosphereReference.cs:11`, `KSA/PhysicalAtmosphereReference.cs:23`,
`KSA/DistanceReference.cs:148`. The whole chain is nullable, `Physical` is a **field** and `Height` a
**class**, not a double.

`DynPressurePa` = `PhysicalAtmosphereReference.GetDynamicPressure(vehicle)` — a **static helper**
(`KSA/PhysicalAtmosphereReference.cs:66`); there is **no `Vehicle.DynamicPressure` property**
(`VehicleTelemetry.cs:127-128`, read at `:157`). Sampled at 2 Hz.

**Classification.** **PASSIVE.** ±2 % hysteresis band; 2.0 s debounce per direction; baseline seeds
silently.

**Server.** Decoded as `stats.VehicleAtmosphere` (`stats/payload.go:71-76`). `entryFold`
(`stats/boards.go:601-629`) → `fastest_entry`, the max `speed_ms` of a `dir == "entered"` event on an
unflagged flight, gated `> 0`. **`exited` is ignored**: leaving an atmosphere fast is an ascent, and
the speed boards already rank that; entering one fast is the part that usually ends in
`rud_aerodynamic_forces`. The speed is surface-relative, which is the right frame for an entry — what
matters is the air the vehicle is hitting, not the body's inertial motion — and it is therefore
directly comparable with the lithobrake and RUD speeds. Context
`{"body", "flight", "dyn_pressure_pa"}`.

Independently of scoring, `flightFold` sets set-only `MilestoneSpace` when `dir == "exited"`. An
`entered` event does not set it; no later event clears it.

**Vectors.** `batch-001.ndjson` line 12.

---

### `vehicle.orbit`

**Wire.** `"vehicle.orbit"` (`EventTypes.cs`), `ver` 1, kind 1. Detector kinds
`OrbitAchieved = 3`, `OrbitEscaped = 4` (`EventDetector.cs:20-24`), independent debounce timers.

**Payload** — `VehicleOrbitPayload`, `Payloads.cs:167-196`

| Key | Type | Units | Source / gotcha |
|---|---|---|---|
| `phase` | string | — | literal `"achieved"` (`EventDetector.cs:442`) / `"escaped"` (`:470`) |
| `body` | string | — | `curr.Body` |
| `ap_m` | number | **metres of ALTITUDE above the parent's mean radius** | `Sanitize.RadiusToAltitude(orbit.Apoapsis, parent.MeanRadius)` **only when `OrbitClass.Bound`, else 0.0** (`VehicleTelemetry.cs:195-200`). The game's `Orbit.Apoapsis` (`KSA/Orbit.cs:1168`) is a **radius from body centre**, and is **negative** on a hyperbola / NaN on a parabola (B4). |
| `pe_m` | number | metres of altitude | `Sanitize.RadiusToAltitude(orbit.Periapsis, meanRadius)`, computed **unconditionally** (`:201`) — so it can legitimately be negative. |
| `ecc` | number | — | `orbit.Eccentricity` (`KSA/Orbit.cs:1154`), `Sanitize.Finite`d |
| `inc_deg` | number | **degrees** | `orbit.Inclination * (180/π)` (`VehicleTelemetry.cs:202`). **The game stores radians** (`KSA/Orbit.cs:1160`). |
| `sma_m` | number | m | `Orbit.SemiMajorAxis` (`KSA/Orbit.cs:1156`), `Sanitize.Finite`d. It is positive for a bound ellipse, negative for a hyperbola and zero when the game's value is non-finite (including a parabolic `+Inf`). |
| `lan_deg` | number | degrees | `Orbit.LongitudeOfAscendingNode` (`KSA/Orbit.cs:1162`) converted radians→degrees and `Sanitize.Finite`d. |
| `argp_deg` | number | degrees | `Orbit.ArgumentOfPeriapsis` (`KSA/Orbit.cs:1164`) converted radians→degrees and `Sanitize.Finite`d. |
| `t_pe` | number | game seconds | `Orbit.TimeAtPeriapsis.Seconds()` (`KSA/Orbit.cs:1152`; `KSA/SimTime.cs:67-70`), the absolute periapsis time on the same game clock as `sim_t`, not a countdown. Non-finite becomes 0. |
| `period_s` | number | s | `Orbit.Period` (`KSA/Orbit.cs:1170`) on a bound orbit. Hyperbolic and parabolic trajectories carry 0 because they have no repeating period; any non-finite bound reading also becomes 0. |
| `mass_kg` | number | kg | `Vehicle.TotalMass` at the instant the milestone fired — the same read `flight.started.mass_kg` takes, sampled again. Non-optional; `0` when unreadable. |

**Detector — two independent rules.**

`CheckOrbitAchieved` (`EventDetector.cs:413-445`):

```
safeAltitude = curr.AtmoHeightM + 1000        // Wire.OrbitAchievedMarginM
above        = curr.IsBoundOrbit && curr.PeAltM > safeAltitude
baseline: OrbitAchieved = above               (emits nothing)
rising edge only; falling back below the bar re-arms silently (:429-434)
2 s debounce on DetectKind.OrbitAchieved
```

On an airless body `AtmoHeightM == 0`, so the 1000 m margin alone is the bar (`:420-421`).

`CheckOrbitEscaped` (`:447-473`): edge on `IsBoundOrbit` going **true → false**; regaining a bound
orbit re-arms silently.

**No NaN sniffing anywhere.** `TelemetrySnapshot.IsBoundOrbit` (`Telemetry/TelemetrySnapshot.cs:216-225`)
uses the `OrbitClass` the game project supplied, falling back to finite `ecc < 1` only for callers
(simulator, hand-built fixtures) that have no classifier.

The same 2 Hz snapshot carries all five new element values through both detector arms unchanged.
They do not participate in either threshold or debounce: `CheckOrbitAchieved` and
`CheckOrbitEscaped` decide only *when* to emit, and the payload captures the current snapshot. Each
member is non-optional to match the existing orbit numbers. A non-finite scalar is sanitised to 0;
an exception while sampling the orbit rejects the whole vehicle sample, so no partly invented
milestone is emitted. `PeriodS` additionally uses the game-supplied conic class and is fixed at 0
for every unbound path rather than forwarding KSA's `NaN`.

**Game source.** `VehicleTelemetry.ClassifyOrbit` (`:231-248`) calls the game's own predicates in
this order — **parabolic first, because `ecc == 1` is the knife-edge**: `Orbit.IsParabolic()`
(`KSA/Orbit.cs:1757`), `Orbit.IsHyperbolic()` (`:1763`), `Orbit.IsBound()` (`:1775`). The result is
carried on the snapshot as `OrbitClass` (`TelemetrySnapshot.cs:15-28,126-127`) precisely because
`catlog.lib` must stay KSA-free.

The element reads are direct `Orbit` properties: `SemiMajorAxis` is a `double` in metres;
`LongitudeOfAscendingNode` and `ArgumentOfPeriapsis` are `double` radians converted to degrees;
`TimeAtPeriapsis` is a `SimTime` converted with `Seconds()`; and `Period` is a `double` in seconds
(`KSA/Orbit.cs:1152-1170`). KSA computes `Period = NaN` for both parabolic and hyperbolic conics
(`KSA/OrbitData.cs:35-75`), which is why the mod's bound/unbound decision precedes sanitisation.
The exact read and conversion path is `VehicleTelemetry.Sample` (`:142-220`), into init-only
snapshot fields (`TelemetrySnapshot.cs:111-124`), then `EventDetector.Orbit` (`:475-494`).

Guard before any orbit read: `VehicleTelemetry.IsReadable` (`:108-132`) checks
`vehicle.FlightPlan.Patches.Count > 0`, because `Vehicle.Parent => Orbit.Parent => Patch =>
FlightPlan.Patches[0]` **throws `ArgumentOutOfRangeException`** on an uninitialised vehicle rather
than returning null (B6, `KSA/FlightPlan.cs:64`). Sampled at 2 Hz.

**Classification.** **PASSIVE.** Threshold `pe_alt > atmo_height + 1000 m`; conic class from the
game; 2 s debounce per phase; baseline seeds silently. The five new element values are descriptive
only and change none of those rules.

**Server.** `orbitsFold` counts `phase == "achieved"` on an unflagged flight into `orbits_achieved`
(`stats/boards.go:910-925`); `toOrbitFold` takes the same events into `fastest_to_orbit`
(`:1160-1180`); the feed renders `"{h} made orbit around {body} ({ap} × {pe})"` (`stats/feed.go:53`).
`escaped` counts nothing anywhere.

`flightFold` also ORs set-only `MilestoneOrbit` for every decoded `phase == "achieved"`. This raw
historical fact does **not** require launch facts and remains set even when the orbit event preceded
`flight.started`; only a future composite predicate that needs a launch fact applies the
`started_seq <= candidate.seq` rule. `phase == "escaped"` does not set or clear the bit.

**No current server fold reads `sma_m`, `lan_deg`, `argp_deg`, `t_pe` or `period_s`.** They are
decoded and retained in the immutable event payload for drawing and later derived uses, but they do
not enter a board context, ranking predicate, score or feed sentence. Adding them therefore changes
no board formula and creates no board.

`orbitMassFold` takes the same `phase == "achieved"` events onto `heaviest_to_orbit`, gated
`mass_kg > 0`. It is a separate type rather than a fifth `orbitRecordFold` because it does not take
the shared shape blob: this board ranks the *vehicle*, and its context is
`{"body", "flight", "ap_m", "pe_m"}` — the apsides are what say where it got to.

`orbitRecordFold` (`:631-686`) is registered **four times** over the same `phase == "achieved"`
events — one type rather than four, for the reason `speedFold` is registered twice: the eligibility
is identical and only the field and the direction differ.

| board | field | direction |
|---|---|---|
| `highest_apoapsis` | `ap_m` | record (max) |
| `lowest_orbit` | `pe_m` | **best (min)** |
| `roundest_orbit` | `ecc` | **best (min)** |
| `steepest_orbit` | `inc_deg` | record (max) |

All four are gated `value > 0`, and that gate is load-bearing rather than defensive: `ap_m` is
written 0.0 whenever the conic is not `Bound`; an `ecc` or `inc_deg` of exactly 0 is what a failed or
unwritten read leaves behind; and `pe_m` is computed unconditionally, so it is legitimately negative
for an orbit whose periapsis is underground. On the two **ascending** boards a zero would be an
unbeatable record nobody flew, which is why `roundest_orbit` refuses a perfectly circular-looking 0
rather than crowning it. All four share one context `{"body", "flight", "ap_m", "pe_m", "ecc",
"inc_deg"}` — a reader looking at a periapsis wants the apoapsis beside it, and one context shape
means the four rows of one orbit are the same blob.

**Vectors.** `batch-001.ndjson` line 14 — carries `mass_kg` and all five non-optional orbital
element fields: `sma_m: 6557100.375`, `lan_deg: 72.25`, `argp_deg: 14.75`, `t_pe: 160.125` and
the bound `period_s: 5420.5`.

---

### `vehicle.soi`

**Wire.** `"vehicle.soi"` (`EventTypes.cs:39`), `ver` 1, kind 1. Detector kind `SoiChange = 5`.

**Payload** — `VehicleSoiPayload`, `Payloads.cs:99-101`

| Key | Type | Source |
|---|---|---|
| `from_body` | string | `state.Previous?.Body ?? state.ReportedParentBodyId` (`EventDetector.cs:273`) |
| `to_body` | string | `curr.Body` (`:278`) |

**Detector** — `EventDetector.CheckSoi` (`:254-284`):

```
if (string.IsNullOrEmpty(curr.ParentBodyId)) return;   // a blank id means the READ FAILED,
                                                       // not that the vehicle left every SOI (:257-259)
baseline / blank latch: adopt and return                (emits nothing)
if (ReportedParentBodyId == curr.ParentBodyId) return;
2 s debounce on DetectKind.SoiChange
emit; ReportedParentBodyId = curr.ParentBodyId
```

**Game source.** `TelemetrySnapshot.ParentBodyId` defaults to `Body` when the game project does not
distinguish them (`TelemetrySnapshot.cs:78-82`) — and the shipped mod sets **both** to
`BodyName(parent)` (`VehicleTelemetry.cs:147,155`), so SOI detection is effectively a
lowercase-body-name diff. `IParentBody.Id` comes from `IObjectId` (`KSA/IObjectId.cs:5`, impl
`KSA/Astronomical.cs:96`) — it is **not** declared on `IParentBody` (`VehicleTelemetry.cs:266`).
Sampled at 2 Hz.

**Classification.** **PASSIVE**, 2 s debounce, blank-guarded, baseline seeds silently.

**Server.** Three folds, in this order (the order matters):

1. `soiFold` (`stats/boards.go:1210-1258`) — `b.AddBody(playerID, "soi", to_body, seq)` reports whether
   the `player_body` row was **new**; only then `addCount(soi_bodies, 1)`. No `count(*)`, correct
   under replay (PROJ-011).
2. `toBodyFold` (`:1690-1737`) — after the shared career/clock/flag gate, ensures a save-local
   `'soi'` member even when the system is unknown (`:1711-1722`), then **always** lowers both
   lifetime and save `first_sim_t` values (seconds), regardless of whether a board key can be
   built. It finally `putBest(fastest_to_<body>, …)` when `FastestToStat(to_body)` succeeds.
3. `bodySprintFold` (`:1739-1791`, stable name `body_sprints`, registered `fold.go:144`) runs after
   those lowered times. For each inclusive threshold it writes the current save's distinct-body
   count, the best one-save count across the player, and — when the current save has a known
   system — the best one-save count among that player's saves in that system. All three contexts
   are SQL NULL.

`soiFold` must precede `toBodyFold` because the latter lowers the rows' time, and `toBodyFold` must
precede `bodySprintFold` so the same event's earlier arrival is visible in the recomputed count.
Body names are **never** validated against a list: a `to_body` that cannot be a stat key still
counts towards `soi_bodies`, still records `first_sim_t`, and still counts in both sprint boards.

Before those board folds, `flightFold` may OR set-only `MilestoneOtherSOI`, but only when all three
facts are known at the SOI event: nonempty `to_body`, an actual `flight.started` with
`started_seq <= event.seq` and nonempty launch `body`, and `to_body != launch body`
(`stats/flight.go:160-170`). If the SOI arrives before the start, the bit stays clear and the later
start does **not** retro-award it. This is a conservative incomplete/out-of-order-log rule, not a
body allow-list.

**Vectors.** `batch-001.ndjson` line 16.

---

### `vehicle.rud`

**Wire.** `"vehicle.rud"` (`EventTypes.cs`), `ver` 1, kind 1.

**Payload** — `VehicleRudPayload`, `Payloads.cs:199-213`

| Key | Type | Units | Source |
|---|---|---|---|
| `cause` | string | — | `EventTypes.ToWire(rud.Cause)` ← `VehicleTelemetry.MapCause(destructionEvent.Cause)` (`Patcher.cs:418`) |
| `peak_g` | number | g | `VehicleDestructionEvent.PeakGLoad`, a **`float` `required`** field (`Patcher.cs:419`) |
| `peak_q_pa` | number | Pa | `VehicleDestructionEvent.PeakDynamicPressure` (`:420`) |
| `speed_ms` | number | m/s | `VehicleTelemetry.SurfaceSpeedMs(vehicle)` → `Vehicle.GetSurfaceSpeed()` (`:421`) |
| `altitude_m` | number | m above mean radius | `VehicleTelemetry.AltitudeM(vehicle)` → `GetBarometricAltitude()` (`:422`) |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` (`:423`) |
| `crew_count` | int | count | `VehicleTelemetry.CrewCount(vehicle)` (`:426`), read from the intact whole vehicle. It is an occupancy fact, not a death count. |
| `part_count` | int | count | `VehicleTelemetry.PartCount(vehicle)` (`:427`) → `Vehicle.Parts.Count` (`VehicleTelemetry.cs:703-711`), read in the destruction prefix while the whole vehicle is still intact. Non-optional; `0` when the read fails. |
| `lat` / `lon` | number | degrees | **OPTIONAL — absent when unreadable.** Read in the same prefix, while the vehicle is still intact. Decoded as `*float64` and read by no fold; they are there so the log can say *where* a vehicle was lost. |

`peak_g` / `peak_q_pa` here are **not** the nullable `StructuralLoad`-derived telemetry values. They
come off the destruction event itself and land in non-nullable payload fields, so a **0 is emitted
rather than the key omitted** — the opposite of `telemetry.window`'s rule.

**Detector.** `Patcher.DestroyVehicleFromEventPrefix` (`Patcher.cs:393-431`) reads `PartCount` into
`RudSignal` beside the other intact-vehicle facts; `EventPipeline.Dispatch`, `RudSignal` case
(`EventPipeline.cs:288-303`), copies it into `VehicleRudPayload`. **Order is load-bearing**:
`_correlator.Destroyed(rud.VehicleId)` is called *first* (`:291`) so an impact recorded earlier in
the same frame resolves to `survived = false`.

**Game source.** A **prefix** on `Universe.DestroyVehicleFromEvent(Vehicle, VehicleDestructionEvent)`
— `KSA/Universe.cs:1699`, `public static`, installed `Patcher.cs:132-134`, body `:393-431`.

- **Prefix, not postfix**: the vehicle is fully intact at prefix time, so speed, altitude, crew and
  `Vehicle.Parts.Count` are valid. The game subsequently ends the crew missions and disposes the
  vehicle, tearing down the object this count describes.
- Guard `if (vehicle.IsDisposed) return;` mirrors the game's own early return at
  `KSA/Universe.cs:1701`, so a second call is a no-op rather than a duplicate RUD
  (`Patcher.cs:400-403`).
- This is the game-thread **apply-side counterpart** of the worker-thread
  `VehicleUpdateTask.DetectStructuralFailure` (`KSA/VehicleUpdateTask.cs:481`), which must **never**
  be patched (`Patcher.cs:18-27`).
- KSA exposes the destruction of the **whole vehicle** here. It does not expose a trustworthy event
  for each individual part that explodes or breaks off, so `part_count` is the size of the intact
  vehicle at its RUD boundary, not a count of separately observed part losses.
- Build 5168 confirms the crew interpretation at source. Physics structural failure creates a
  `VehicleDestructionEvent` (`KSA/VehicleUpdateTask.cs:481-509`), whose apply path calls
  `Universe.DestroyVehicleFromEvent` (`KSA/VehicleDestructionEvent.cs:11-14`; `Universe.cs:1699-1733`).
  That reaches `DestroyVehicle`, which calls `Vehicle.EndAllCrewMissions`
  (`Universe.cs:1736-1743`; `Vehicle.cs:806-829`) and never `KillCrew`. The separate player destroy
  path calls `Vehicle.KillCrew` when it was not recovery (`KSA/InputEvents.cs:487-528`), and
  `KittenRosterEntryData.Kill` is what sets `Kia = true` (`KSA/Vehicle.cs:2796-2811`;
  `KittenRosterEntryData.cs:96-109`). Therefore these boards can state only who was aboard a
  physics RUD; they cannot state deaths, casualties, crashes or explosions.

**Classification.** **EVENT-DRIVEN.** No debounce, no threshold.

**Dedup.** The `IsDisposed` guard plus the `Destroying` set (`Patcher.cs:392`), which also drives the
subsequent `flight.ended` reason.

**Server.** `rudPartsFold` (`stats/boards.go:1128-1183`, stable name `rud_parts_crew` at
`:1131-1134`) replaces the E2 `rud_parts` identity and makes one eligibility decision for all six
outputs. On an unflagged flight it writes `rud_total` and a legal `rud_<cause>` as before, then reads the same decoded
`VehicleRUD.PartCount` once. Only a value `> 0` adds that many parts to `parts_lost` and offers the
same value to max-record `biggest_parts_lost`. Both use the shared scoped helpers, so player, career
and system values move together. The additive total has SQL NULL context; the largest-single-loss
record carries exactly `{"body", "cause", "flight"}`. Zero is the read-failure fallback and is a
no-op on both part boards. Independently, `crew_count >= 1` adds the full count to `kittens_wrecked`
with SQL NULL context and offers it to max-record `biggest_crew_wreck`, whose exact context is
`{"body", "cause", "flight"}`. A cause that cannot be a stat key still contributes to `rud_total`
and every qualifying part or crew board; cause-key validity, part readability and crew occupancy
are independent branches. The new stable fold name changes `BuildID`, queuing a historical rebuild
that backfills both appended crew boards without a `BuildVersion` bump. Feed:
`"{h} lost a vehicle to {causePhrase} on {body} at {speed} m/s"`.

**Vectors.** `batch-001.ndjson` line 30 — `part_count: 31`, `lat` / `lon` **absent**, and
`peak_g` / `peak_q_pa` written as numbers rather than omitted.

---

### `vehicle.impact`

**Wire.** `"vehicle.impact"` (`EventTypes.cs`), `ver` 1, kind 1.

**Payload** — `VehicleImpactPayload`, `Payloads.cs:187-200`

| Key | Type | Units | Source |
|---|---|---|---|
| `speed_ms` | number | m/s | **Ground:** `GroundImpactEvent.ImpactVelocity` — the **closing NORMAL speed**, not total speed (`Patcher.cs:434-436`; `KSA/GroundImpactEvent.cs:9`, new in 5168 r5162, computed `KSA/ConstraintSim.cs:726-738`). **Splash:** a reconstructed `√(2E/m)` scalar, 0 when mass or energy is 0 (`Patcher.cs:465-467`) — `WaterSplashEvent` carries no velocity. **The two are indistinguishable on the wire.** |
| `energy_j` | number | J | `GroundImpactEvent.ImpactKineticEnergy` (`KSA/GroundImpactEvent.cs:7`) / `WaterSplashEvent.ImpactKineticEnergy` (`KSA/WaterSplashEvent.cs:5`) |
| `survived` | bool | — | **mod-computed** by `ImpactCorrelator` — see below |
| `launch_pad` | bool | — | `GroundImpactEvent.IsLaunchPad` (`KSA/GroundImpactEvent.cs:19`, `Patcher.cs:438`). **Always `false` for a splash** (`ImpactCorrelator.cs:48-59`). |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` |
| `crew_count` | int | count | `VehicleTelemetry.CrewCount(vehicle)` |
| `lat` / `lon` | number | degrees | **OPTIONAL — absent when unreadable.** Read in the impact/splash postfix. Decoded and read by no fold. |

**Detector** — `ImpactCorrelator` (`Detect/ImpactCorrelator.cs`). One generic two-slot `Hold<,>` —
`_pending` (this frame) and `_held` (last frame) — instantiated **twice**: once for impacts and once
for landings.

- `Impact(signal)` appends to the impact hold.
- `Splash(signal)` converts to an `ImpactSignal` with `LaunchPad: false` and appends.
- `Landed(observation)` appends to the landing hold — see [`vehicle.landed`](#vehiclelanded).
- `Destroyed(vehicleId)` marks **both slots of both holds**.
- `EndFrame()` resolves `_held`, promotes `_pending` → `_held`, clears `_pending`, called from the
  `FrameBoundarySignal` case (`EventPipeline.cs:203-205`).
- `DrainFor(vehicleId)` resolves one vehicle's outstanding observations immediately when its flight
  ends; `Drain()` resolves everything at session end.
- Verdict: `Survived = !Destroyed`.

**All three drains return a `Verdicts` record struct carrying both kinds** — impacts and landings —
because one advance of the hold settles both. Splitting them into two methods would mean two calls,
and a caller that made only one of them would strand the other kind forever. MOD-077.

**The rule, exactly:** an impact seen in frame *N* is resolved at the end of frame *N+1*; a
destruction of that vehicle in frame *N* **or** *N+1* flips `survived` to false
(`ImpactCorrelator.cs:24-29`). The extra frame exists because the game applies **every** impact and
splash for a frame before **any** physics destruction (`KSA/VehicleUpdateTask.cs:410-453`), but a
*manual* destroy lands later still, in `InputEvents.ApplyInputEvents` (`KSA/Program.cs:1918`, six
lines after `Universe.ApplyVehicleSolvers` at `:1912`).

**Flight attribution has two modes.** `EventFactory.FromResolvedImpact(tracker, impact)` (`:49-50`)
*mints* a flight if needed; the explicit-flight overload (`:61-75`) does not. `EventPipeline.Flush`
uses **peek** semantics and **drops** an impact whose flight already ended, with a debug log, rather
than inventing a phantom flight with no `flight.started` (`EventPipeline.cs:179-190`).

**Game source.**

| Signal | Patch | Kind | Where |
|---|---|---|---|
| `ImpactSignal` | `GroundImpactEvent.Apply(Vehicle)` — `KSA/GroundImpactEvent.cs:21` | **postfix** | installed `Patcher.cs:142-144`, body `:413-446` |
| `SplashSignal` | `WaterSplashEvent.Apply(Vehicle)` — `KSA/WaterSplashEvent.cs:13` | **postfix** | installed `:150-152`, body `:448-477` |
| frame boundary | `Universe.ApplyVehicleSolvers()` — `KSA/Universe.cs:1653` | **postfix** | installed `:123-125`; the body is a bare counter (`:364-374`) used only as a heartbeat — catlog closes its frame in `[StarMapBeforeGui]`, which is *after* `ApplyInputEvents` and therefore also covers manual destroys |

Both apply-side hooks are the game-thread counterparts of worker-thread detectors that must never be
patched: `ConstraintSim.DetectTerrainContact` (`KSA/ConstraintSim.cs:705`) and
`VehicleUpdateTask.DetectWaterSplash` (`KSA/VehicleUpdateTask.cs:455`).

**Suppression gate.** Both postfixes early-return on `VehicleTelemetry.IsImpactSuppressed(vehicle)`
(`Patcher.cs:423-424`, `:455-456`) ← `Vehicle.IsImpactFxSuppressed()` (`KSA/Vehicle.cs:5271-5274` =
`Program.GetPlayerTime() - _lastTeleportTime < 5.0`). The game still **applies** the impact when FX
are suppressed — only the visuals are skipped — so the postfix fires either way and must consult this
itself. An impact within 5 s of a teleport is not a real lithobrake.

**Classification.** **EVENT-DRIVEN** (Harmony postfix), with a **one-full-frame hold** before the
verdict is final. No debounce; the only rate limiting is the game's own inside
`ConstraintSim.DetectTerrainContact`.

**Server.** Two boards, one crash read two ways. `survivedImpact` (`stats/boards.go:389-403`) is the
eligibility both share, and they share it because they must agree about which crashes count:
`Survived && !LaunchPad && CrewCount >= 1`, then `scoreable`, then — **rebuild only** — no
`kitten.kia` for the same flight within ±2.0 s of `ev.SimTime`. That last clause depends on
`kitten.kia` naming a flight (MOD-073).

- `lithobrakeFold` (`:416-432`) → `biggest_lithobrake_survived`, max `speed_ms`, gated `> 0`.
- `impactEnergyFold` (`:442-456`) → `biggest_impact_energy`, max `energy_j`, gated `> 0`.

**Not a duplicate.** `speed_ms` is a closing *normal* speed for a ground impact and a reconstructed
`√(2E/m)` scalar for a splash, so it says nothing about how much vehicle was moving; `energy_j` is
the game's own `ImpactKineticEnergy` and therefore ranks a heavy lander touching down hard above a
probe hitting fast. Both take the same context `{"body", "flight", "speed_ms", "energy_j"}`
(`impactContext`, `:407-414`) — whichever figure is not this board's value is the one a reader wants
next to it, and one blob means the two rows of one crash agree.

Feed: `"{h} lithobraked at {speed} m/s on {body} — and survived"`.

**Vectors.** `batch-001.ndjson` line 25 (`survived: true`, `launch_pad: false`, `body: "duna"`, `speed_ms: 214.5`, `lat` / `lon` present).

---

### `vehicle.landed`

**Wire.** `"vehicle.landed"` (`EventTypes.cs`), `ver` 1, kind 1. `flight` is
**always non-null**: a landing is minted only against a flight the tracker can already name. Detector
kind `DetectKind.Landing = 6` (`EventDetector.cs:40`), which exists for legibility only —
`CanFire` is never called with it.

**Why a type and not four more keys on `vehicle.situation`**, which reports the same transition:
`survived` cannot be known at detection time. It takes a full frame of destruction hold, and a
situation change is emitted immediately — so folding it in would mean either stalling every situation
change by a frame or storing a `survived` that a later event has to correct, in a log catlog never
revises. MOD-075.

**Payload** — `VehicleLandedPayload`, `Payloads.cs:226-240`, built `EventFactory.FromResolvedLanding`
(`:93-109`)

| Key | Type | Units | Optional | Source |
|---|---|---|---|---|
| `body` | string | — | no | `curr.Body` on the detecting sample |
| `vertical_speed_ms` | number | m/s, **positive downwards** | no | `VehicleTelemetry.VerticalSpeedMs`. `0` when unreadable. |
| `horizontal_speed_ms` | number | m/s, always ≥ 0 | no | `VehicleTelemetry.HorizontalSpeedMs`. `0` when unreadable. |
| `crew_count` | int | count | no | the same occupied-seat walk every other event uses |
| `survived` | bool | — | no | **mod-computed by `ImpactCorrelator`**, the same one-full-frame hold `vehicle.impact.survived` goes through |
| `radar_alt_m` | number | m above the surface beneath | **YES — omitted** | `curr.RadarAltM`. **Not expected to be 0**: detection is at 2 Hz, so the sample is up to 0.5 s after contact and the vehicle is still settling. |
| `lat` / `lon` | number | degrees | **YES — omitted** | `curr.Lat` / `curr.Lon` |

**Neither speed exists on the game object.** KSA publishes `GetSurfaceSpeed()`, a magnitude, and
nothing else, so `VehicleTelemetry.SurfaceVelocity` reconstructs the vector from the same two terms
that method uses — `v_surface = v_cci − ω × r` (`KSA/Vehicle.cs:2759-2763`) — and splits it into a
radial and a tangential component. The radial one is negated so that a landing reads *positive*,
which is the sign a player means by "came down at 4 m/s". It is a cached field read and a cross
product; `NavBallData.Speed` is frame-dependent on the player's navball mode and must never be used
for a recorded number.

**Detector — the surface-contact half of the situation edge, and nothing else.**
`EventDetector.CheckSituation` (`:236-304`) emits `vehicle.situation` and then, when
`IsTouchdown(from, to)` (`:317-321`), a second `DetectedEvent` off the same transition:

```
IsTouchdown(from, to) = IsKnown(from) && IsKnown(to)
                        && !HasSurfaceContact(from) && HasSurfaceContact(to)
```

Contact-free is `freefall` and `maneuvering`; contact is `landed`, `rolling`, `sailing`, `floating`,
`dragging`, `bottomed`. **Both sides must be *known***, so a ninth situation added by a future build
cannot be mistaken for flight — the same rule `softest_touchdown` takes, for the same reason.

**Classification.** **PASSIVE** (2 Hz poll → worker prev/curr comparator), with a **one-full-frame
hold** before `survived` is final. It carries **no debounce of its own**: it is emitted inside the
situation rule's `CanFire` gate and never marks a timer. That is deliberate in both directions
(MOD-076):

- a craft chattering between `freefall` and `landed` at 2 Hz — a bouncing lander, a rover on rough
  ground — would otherwise mint a landing every 500 ms, each one a record;
- a landing suppressed by the debounce is **not lost**, because `ReportedSituation` is only advanced
  when the pair actually fires, so the edge is still pending on the next sample and both events are
  emitted then, off the same `from`;
- and a timer of its own could fire on a transition whose `vehicle.situation` was suppressed, leaving
  a `vehicle.landed` in the log with nothing beside it to explain it.

Baseline emits nothing, so a vehicle **already on the ground when a save loads does not "land"** —
the first sample only seeds `ReportedSituation`. A backwards `sim_t` jump rebaselines the whole
state, dropping every latch and timer.

**Dedup / ordering.** The `ReportedSituation` latch is the dedup, shared with `vehicle.situation`;
the two are always emitted as a pair, situation first. A landing detected while processing frame *N*
settles at the end of frame *N+2* rather than *N+1*: it enters the hold one step later than an impact
raised *inside* frame *N*, because the worker processes a frame's telemetry just after that frame's
boundary signal has already been consumed. That is strictly the safer direction and is not
special-cased.

**Flight attribution uses peek semantics**, like an outstanding impact: `EventPipeline.Flush` drops a
landing whose flight already ended, with a debug log, rather than minting a phantom flight with no
`flight.started` (`:225-236`).

**Server.** Two boards, `softest_landing` and `landings`, sharing one eligibility in
`survivedLanding` (`stats/boards.go`) because a board about touching down *gently* and a board about
touching down *at all* must agree about which arrivals happened: `survived`, then `scoreable`.

- `softestLandingFold` → `softest_landing`, a `putBest` (min) of `vertical_speed_ms`, gated `> 0`.
  Context `{"body", "flight", "horizontal_speed_ms", "crew_count"}`.
- `landingsFold` → `landings`, `addCount(1)`. **No speed gate** — a landing at any rate is a landing.

**`survived` is taken, never re-derived.** It has already been through the destruction hold, so a
fold must not reconstruct it from a nearby `vehicle.rud` or `flight.ended`. Unlike `survivedImpact`
there is **no crew requirement and no ±2 s KIA window**: those exist because D11's rule is about a
*crew* surviving a lithobrake, and landing a probe is landing. Unlike `survivedLoad` there is **no
rebuild-only refinement**, so both boards fold identically incrementally and on rebuild.

**No plausibility rule, and there must not be one.** A one-metre hop is a landing. Filtering on "was
that a *real* landing" infers intent from data shape, which Constitution §8 forbids. PROJ-096.

Feed: `"{h} landed on {body} at {n} m/s with {k} kittens aboard"`, or without the crew clause at
`crew_count` 0. **A landing the vehicle did not survive produces no feed line at all** — that is a
crash, and the `vehicle.rud` emitted beside it already announces it.

`radar_alt_m`, `lat` and `lon` are decoded (`*float64`) and read by no fold.

Independently of those boards, `flightFold` ORs set-only `MilestoneLanded` only when the decoded
payload has `survived: true`. An unsurvived landing does not set it, and nothing clears a prior bit.

**Vectors.** `batch-001.ndjson` line 26 — `lat` / `lon` present, `radar_alt_m` **absent**.

---

### `vehicle.staging`

**Wire.** `"vehicle.staging"` (`EventTypes.cs:48`), `ver` 1, kind 1.

**Payload** — `VehicleStagingPayload`, `Payloads.cs:137-138`

| Key | Type | Source |
|---|---|---|
| `stage_index` | int | `SequenceList.ActiveSequence` read in the **postfix**, so it is the newly-activated stage (`Patcher.cs:578`). Zero-based (`Payloads.cs:136`). |

**Detector.** `EventPipeline.Dispatch`, `StagingSignal` case (`:230-232`) → the `Vehicle(...)` helper.

**Game source.** A **postfix** on `SequenceList.ActivateNextSequence(Vehicle vehicle)` —
`KSA/SequenceList.cs:135`, installed `Patcher.cs:189-191`, body `:567-584`. The only call site in the
whole game is `KSA/Vehicle.cs:3342`, behind the stage key.

**Classification.** **EVENT-DRIVEN.** No debounce, no threshold.

**Server.** Two boards. `countFold{stagings, "vehicle.staging"}` — +1 per event on an unflagged
flight. `stagesFold` (`stats/boards.go:756-785`) → `most_stages`, a record of **`stage_index + 1`**:
the index is zero-based and is read in the postfix, so it names the sequence that just became active,
and `+1` is "how many stages have fired", the number a player would say out loud. There is **no
`> 0` gate**, for the same reason — firing stage 0 is one staging event and is one stage. `body`
comes from `flight_state` (`flightBody`), because this payload carries none.

**Vectors.** `batch-001.ndjson` line 9.

---

### `vehicle.docked`

**Wire.** `"vehicle.docked"` (`EventTypes.cs:51`), `ver` 1, kind 1.

**Payload** — `VehicleDockPayload`, `Payloads.cs:142-143`

| Key | Type | Source |
|---|---|---|
| `other_flight` | string \| **null** | `Tracker.PeekFlight(dock.OtherVehicleId)` (`EventPipeline.cs:256`) — **peek, never mint**, so a vehicle with no open flight yields the literal `"other_flight":null`. The Go struct is a plain `string` (`stats/payload.go:127`), so null decodes to `""`. |

**Detector.** `EventPipeline.Dispatch`, `DockSignal` case (`:235-238`). The event is attributed to
`dock.VehicleId`, the `thisVehicle` side.

**Game source.** A **postfix** on `DockingPort.Dock(Vehicle thisVehicle, Vehicle otherVehicle,
DockingPort otherVehicleDockingPort, out PoseChange consumedToCombined)` — `KSA/DockingPort.cs:422`,
installed `Patcher.cs:200-202`, body `:586-608`.

- `__result is null` means the port was already docked — **nothing happened, return** (`:593-595`).
- Both vehicles go through `Patcher.Track` first (`:597-598`), so their `flight.started` events
  precede this one.
- **Why not `DockingEvent.Apply`**: that covers only *physics*-initiated docking; player-commanded
  dock/undock goes through `InputEvents` instead, and `DockingEvent` is additionally suppressed when
  a destruction is pending the same frame (`KSA/VehicleUpdateTask.cs:415-416`).
  `ConstraintSim.DetectDockingEvent` (`KSA/ConstraintSim.cs:751`) is a worker-thread detector and must
  not be patched.

**Classification.** **EVENT-DRIVEN.**

**Server.** `countFold{dockings, "vehicle.docked"}` — +1 on an unflagged flight. Independently,
`flightFold` ORs set-only `MilestoneDocked` for every successfully decoded `vehicle.docked`; no
payload value further qualifies it and nothing clears it.

**Vectors.** `batch-001.ndjson` line 17 — `"other_flight":null`, the taxonomy's one in-payload null.

---

### `vehicle.undocked`

**Wire.** `"vehicle.undocked"` (`EventTypes.cs:54`), `ver` 1, kind 1.

**Payload.** The same record as `vehicle.docked`. `other_flight` = `Tracker.PeekFlight(undock.OtherVehicleId)`
where "other" is the **vehicle that split off** (`GameSignal.cs:288`, `EventPipeline.cs:261`).

**Detector.** `EventPipeline.Dispatch`, `UndockSignal` case (`:240-242`).

**Game source.** A **postfix** on `DockingPort.Undock(Vehicle oldVehicle, out PoseChange
combinedToSplit)` — `KSA/DockingPort.cs:460`, installed `Patcher.cs:207-209`, body `:610-631`. The
body is `oldVehicle.Split(...)`; the caller is `KSA/InputEvents.cs:384`. `__result is null` → return.
The split vehicle is `Track`ed — and so gets a `flight.started` — before the undock event is raised
(`:621`).

**Classification.** **EVENT-DRIVEN.**

**Server.** Decoded (`stats/payload.go:126-128`) but **counts nothing**. There is no `undockings` board.

**Vectors.** `batch-001.ndjson` line 23 — `other_flight` a ULID, the counterpart to the docked line's null.

---

### `engine.ignition`

**Wire.** `"engine.ignition"` (`EventTypes.cs:57`), `ver` 1, kind 1. Selected via
`EventTypes.TypeOf(EngineEventKind.Ignition)`.

**Payload** — `EnginePayload`, `Payloads.cs:148-150`

| Key | Type | Source |
|---|---|---|
| `engine` | string | template id of the **first active** engine controller found, or the literal `"unknown"` when none could be read (`PolledSignals.cs:179`) |
| `count` | int | `Math.Max(1, count)` where `count` is the number of active `EngineController`s (`:180`) |

**Detector** — `PolledSignals.PollVehicle` (`:171-181`), an edge on the whole-vehicle boolean
`EngineActive`:

```
if (now.EngineActive != state.EngineActive)
    emit Ignition  when now.EngineActive == true
    emit Shutdown  when now.EngineActive == false
```

Per-vehicle state is seeded silently by `PolledSignals.Track` → `Observe` (`:93,195-203`), so a
vehicle already burning at save-load does not fire a spurious ignition.

**Game source — whole-vehicle, not per-engine (deliberate).**

- `VehicleTelemetry.IsAnyEngineActive(vehicle)` ← `Vehicle.IsAnyEngineActive()` (`KSA/Vehicle.cs:6030`,
  churn risk Medium) — reads `EngineControllerGlobalState.IsAnyActive` off the vehicle's
  `ModuleStateList` (`VehicleTelemetry.cs:737-753`).
- `VehicleTelemetry.ActiveEngines(vehicle)` (`:796-818`) walks
  `vehicle.Parts.Modules.Get<EngineController>()` (a `Span<T>` — **never store it across frames**,
  `KSA/PartTree.cs:34`), counting `IsActive` (`KSA/EngineController.cs:36`) and taking the first
  `TemplateId` (`KSA/ModuleBase.cs:29`).
- Polled at **2 Hz** (`PolledSignals.cs:133`).

Consequence, recorded rather than hidden (`docs/mod.md:200-202`): a vehicle that shuts down one of
two engine groups reports nothing until the last one stops.

**Classification.** **PASSIVE** (2 Hz poll, per-vehicle boolean edge). No debounce beyond the sample
interval; the baseline seed is the only gate.

**Server.** Decoded as `stats.Engine` (`stats/payload.go:139-142`) — one Go type for all three wire
names, because the payload is one type in the mod too. `countFold{engine_ignitions,
"engine.ignition"}` — +1 per event on an unflagged flight. Neither payload field is read: the fold
keys on the event type, and `engine` / `count` are whole-vehicle readings that would rank a
two-engine-group vehicle oddly (see the game source above).

**Vectors.** `batch-001.ndjson` line 10 (`shutdown` is line 13, `flameout` line 24 — the three share this payload).

---

### `engine.shutdown`

**Wire.** `"engine.shutdown"` (`EventTypes.cs:60`), `ver` 1, kind 1.

**Payload.** `EnginePayload` as above. `count` = `Math.Max(1, state.EngineCount)` — the count from
the **previous** observation, since the engines are now off (`PolledSignals.cs:180`).

**Detector / game source / classification.** Identical to `engine.ignition`; the falling edge of
`IsAnyEngineActive`.

**Server.** Decoded as `stats.Engine`; **no fold reads it**. Deliberate: an ignition is a decision and
a flameout is a failure, whereas a shutdown is the unremarkable other half of every burn, and
counting it would be counting `engine_ignitions` twice with a lag. **Vectors.** `batch-001.ndjson` line 13.

---

### `engine.flameout`

**Wire.** `"engine.flameout"` (`EventTypes.cs:63`), `ver` 1, kind 1.

**Payload.** `EnginePayload`; `count` = `Math.Max(1, count)` of currently-active controllers
(`PolledSignals.cs:189`).

**Detector** — `PolledSignals.PollVehicle` (`:182-190`), in the `else if` branch, i.e. **only when
the active edge did not fire this tick**:

```
else if (now.EngineActive && state.EnginePropellant && !now.EnginePropellant)
    emit Flameout
```

**Game source — synthesised; the game has no flameout concept.** `docs/ksa-integration.md` B3
records zero hits for `flameout`, `starved` or `ResourceAvailable` anywhere in the codebase. The
game's own predicate is `IsActive && !IsPropellantAvailable` (`KSA/EngineController.cs:60`),
reconstructed here at whole-vehicle granularity from two globals:

- `Vehicle.IsAnyEngineActive()` — `KSA/Vehicle.cs:6030`
- `Vehicle.IsAnyEnginePropellantAvailable()` — `KSA/Vehicle.cs:6131` (`VehicleTelemetry.cs:771-781`,
  churn risk Medium)

The per-engine `IsPropellantAvailable` lives on the parallel `EngineControllerState` span, which
`PartTree` does not expose as a named `StateList` — hence the whole-vehicle globals (`:792-795`).
Polled at 2 Hz.

**Classification.** **PASSIVE** (2 Hz poll, two-boolean edge).

**Server.** `countFold{flameouts, "engine.flameout"}` → `flameouts` ("Ran Dry"), +1 per event on an
unflagged flight. **Vectors.** `batch-001.ndjson` line 24.

---

### `kitten.eva_start`

**Wire.** `"kitten.eva_start"` (`EventTypes.cs:66`), `ver` 1, kind 1. `flight` =
`Tracker.FlightFor(eva.VehicleId)` when the signal carries a vehicle id, else `null`
(`EventPipeline.cs:271-272`). **`FlightFor` mints**, so this can create the EVA vehicle's flight ULID
before its `flight.started` exists — the one documented exception to the ordering invariant, see
[Known drift](#known-drift).

**Payload** — `KittenEvaStartPayload`, `Payloads.cs:155-157`

| Key | Type | Source |
|---|---|---|
| `kid` | string (16 Crockford) | `Ids.KittenId(installId, kittenName)` (`EventPipeline.cs:428`). **Relabelled per player by `Redact` before publication.** |
| `name` | string | `Ids.SanitizeName(eva.KittenName)` — ≤ 32 printable ASCII, fallback `"kitten"` |

**Detector.** `EventPipeline.Dispatch`, `EvaStartSignal` case (`:250-255`).

**Game source.** A **postfix** on the **private instance** method
`EVADoor.CreateKittenEva(Vehicle, IVASeat, KittenRosterEntryData)` — `KSA/EVADoor.cs:133`, installed
via `AccessTools.Method(typeof(EVADoor), "CreateKittenEva")` at `Patcher.cs:218-220`, body `:633-656`.

- **B1 name collision**: `KittenEva.CreateKittenEva(CelestialSystem, VehicleTemplate, IParentBody,
  string)` is a different, `public static` method — the scenario/template spawn path whose `id` is
  **not** guaranteed to be a roster name. The declaring type in the `AccessTools` call is what
  disambiguates.
- `__result is null` means the door could not produce an EVA (no backpack part) — no egress happened,
  return (`:640-642`).
- The name comes from the `rosterEntry.Name` **argument** (`:646`); empty → return.
- The vehicle id comes from `VehicleTelemetry.IdOf(__result)` (`:650`) — a `KittenEva`'s `Vehicle.Id`
  **is** the kitten's roster name (`KSA/EVADoor.cs:142` → `KSA/Astronomical.cs:153`).

**Classification.** **EVENT-DRIVEN.**

**Server.** Decoded as `stats.KittenEvaStart` (`stats/payload.go:145-148`).
`countFold{evas, "kitten.eva_start"}` → `evas` ("Spacewalks"), +1 per event. **The flag exclusion
applies only when the EVA signal carried a vehicle id** — `flight` is
`Tracker.FlightFor(eva.VehicleId)` when there is one and `null` otherwise, and `scoreable` passes
every flightless event. That is a property of the source event, not a decision about the board.
**Vectors.** `batch-001.ndjson` line 18 — the EVA-minted `flight`, distinct from the vehicle's.

---

### `kitten.eva_end`

**Wire.** `"kitten.eva_end"` (`EventTypes.cs:69`), `ver` 1, kind 1. `flight` = **explicitly `null`**
(`EventPipeline.cs:279`) — asymmetric with `kitten.eva_start`.

**Payload** — `KittenEvaEndPayload`, `Payloads.cs:163-166`

| Key | Type | Units | Source |
|---|---|---|---|
| `kid` | string | — | `Ids.KittenId(installId, name)` |
| `name` | string | — | `Ids.SanitizeName(...)` |
| `duration_s` | number | **sim seconds** | `max(0, simT - Vehicle.LaunchGameTime)`; **0.0 when `LaunchGameTime` is NaN** (`Patcher.cs:532-533`), indistinguishable from an instantaneous EVA. |

**Detector.** `EventPipeline.Dispatch`, `EvaEndSignal` case (`:258-262`).

**Game source.** Raised inside the **prefix** on `Vehicle.Dispose(bool)` (`Patcher.cs:496-544`), in
the `VehicleTelemetry.IsKitten(__instance)` branch (`:530-535`). A `KittenEva`'s `Vehicle.Id` is the
kitten's roster name, so its disposal *is* the end of that kitten's EVA. `IsKitten` is a plain
`is KittenEva` test (`VehicleTelemetry.cs:732`, `KSA/KittenEva.cs:8`).

The `EvaEndSignal` is raised **before** the `VehicleRemovedSignal` for the same vehicle (`:534` vs
`:537`), so `kitten.eva_end` precedes that EVA vehicle's `flight.ended`.

**Classification.** **EVENT-DRIVEN.**

**Server.** Decoded as `stats.KittenEvaEnd` (`stats/payload.go:155-159`). `evaDurationFold`
(`stats/boards.go:787-818`) → `longest_eva`, the max `duration_s` in seconds, gated **`> 0`** because
0.0 is what an unreadable `LaunchGameTime` leaves behind and is indistinguishable on the wire from an
EVA that ended in the frame it began.

Because `flight` is explicitly `null` here, **this board has no flag exclusion at all** — `scoreable`
passes every flightless event, and there is no flight to name in the context either, so the context
is `{"kitten"}`. The fold adds a `flight` key when the event carries one, which the shipped mod never
does; it is there so a future build that fills the key in gets the link rather than a silently
missing one.

**Vectors.** `batch-001.ndjson` line 21 — `flight` explicitly null.

---

### `kitten.tumble`

**Wire.** `"kitten.tumble"` (`EventTypes.cs:83`), `ver` **1**, kind 1. `flight` = **the tumbling
kitten's own EVA flight** (`EventPipeline.cs:352-370`). A `KittenEva`'s `Vehicle.Id` *is* the roster
name, so `tumble.KittenName` is already the vehicle id and `Tracker.PeekFlight(tumble.KittenName)`
resolves it; `PolledSignals.Track` has registered that vehicle into the same `into` list ahead of the
`TumbleSignal`, so the flight is open by the time the pipeline sees it.

**Peek, not `FlightFor`.** Minting here would attach the tumble to a flight ULID that has no
`flight.started` and never will — the phantom flight `Flush`'s peek semantics (MOD-059) exist to
prevent — and a tumble is exactly the event a phantom would be minted for, since it can arrive from a
vehicle whose id could not be read. So `flight` is still **null** when the kitten has no open flight.
A null tumble scores exactly as every tumble did at `ver` 1; an invented flight poisons a join
permanently.

**Payload** — `KittenTumblePayload`, `Payloads.cs:319-330`

| Key | Type | Units | Source |
|---|---|---|---|
| `kid` | string | — | `Ids.KittenId(installId, tumble.KittenName)` |
| `name` | string | — | `Ids.SanitizeName(...)`. `KittenName` is the EVA vehicle's id, i.e. the roster name (`PolledSignals.cs:178-180`). |
| `from` | string (open set) | — | The previous cached `KittenEva.LocomotionState.Mode`, mapped by `LocomotionModeName.FromGameName` (`LocomotionModeName.cs:14-23`). Today's six known modes become lowercase; null or an unseen value becomes the honest fallback `"unknown"`. The server must not allow-list them. |
| `speed_ms` | number | m/s (tangential ground speed) | `VehicleTelemetry.GroundSpeedMs(vehicle)` (`PolledSignals.cs:179`) |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` |

**Detector** — `PolledSignals.PollVehicle` (`:163-181`):

```
previous = state.Locomotion
if (now.Locomotion == LocomotionMode.Tumbling && previous != LocomotionMode.Tumbling)
    emit TumbleSignal(from = lowercased(previous), or "unknown")
```

**Transitions INTO `Tumbling` only** — a tumble ends `Tumbling → Rightening → Grounded`, so counting
transitions *out* would double-count via `Rightening`. `Airborne → Tumbling` means the kitten failed
to land on its feet; `Grounded → Tumbling` means it tripped while already on the ground. The normal
recovery path is `Tumbling → Rightening → Grounded`.

**The game may report more than one tumble during one cartwheel.** If a tumbling kitten remains off
the ground beyond stock `TumbleAirborneExitTime = 0.5 s`, KSA changes it from `Tumbling` to
`Airborne`. A later bounce can therefore produce another `Airborne → Tumbling` edge. Catlog records
those state-machine edges as they occur; it does not smooth or merge them, and some events from one
visually continuous cartwheel can consequently carry `from: "airborne"`.

**Game source.**

- `VehicleTelemetry.LocomotionMode(vehicle)` (`:963-984`) → `KittenEva.LocomotionState.Mode`
  (`KSA/KittenEva.cs:20`, `KSA/LocomotionState.cs:5`, `KSA/LocomotionMode.cs:3`). `LocomotionState`
  is a get-only property returning a **struct copy**, so no reflection and no aliasing. **Churn risk
  High — the whole locomotion subsystem is new in 5168.** Six values: `Mmu, Grounded, Airborne,
  Tumbling, Rightening, Ladder`.
- The `from` value is the previous sample's cached mode in `PolledSignals.VehicleState`, not a second
  KSA read. The conversion is total and preserves the wire's open-set rule with `"unknown"` as its
  default rather than throwing on a future enum value.
- `VehicleTelemetry.GroundSpeedMs` (`:986-1008`) → `KittenEva.LocomotionState.GroundSpeed`
  (`KSA/LocomotionState.cs:13`). The game's own classifier uses `LocomotionFacts.TangentialSpeedPhys`
  (`KSA/KittenLocomotion.cs:30`, computed `KSA/VehicleUpdateTask.cs:1154`), which is *not* exposed on
  the state struct; `GroundSpeed` is the closest published quantity.
- Polled at **2 Hz**.

**The game-side threshold** producing `Tumbling` is
`facts.TerrainContact && facts.TangentialSpeedPhys >= tuning.TumbleSpeedGate`
(`KSA/KittenLocomotion.cs:30-33`), stock gate **6.5 m/s** (raised from 5.5 in r5131). That gate is a
mutable public static the game's own debug window live-edits — which is why any deviation raises
`flight.flagged: tuning`.

**Classification.** **PASSIVE** (2 Hz poll, enum edge). Gate: transitions into `Tumbling` only;
baseline seed emits nothing. This is an edge reporter, not a physical-fall deduplicator: the stock
0.5 s airborne exit described above can make one rough cartwheel cross the edge repeatedly.

**Server.** One `tumbleFold` (`stats/boards.go:1079-1115`) handles the three related counters after
the shared flag check. Its stable fold name is `tumble_split`, replacing the former generic
`kitten_tumbles` fold identity so `BuildID` queues a rebuild that backfills the two new projections.
Every decoded tumble adds 1 to the unchanged `kitten_tumbles` total. Exact
`from == "airborne"` also adds
1 to `botched_landings`; `"grounded"`, `"unknown"` and every future value in this open set simply do
not match. When `TumblesOnStat(body)` can form a safe dynamic key, every tumble origin also adds 1
to `tumbles_on_<body>`. An unkeyable body loses only that family write: the all-body total and the
airborne-only counter still move. All three writes use `addCount`, so they fan out to player, career
and system scope and carry no row context. That "on an unflagged flight"
depends entirely on the envelope's `flight`: `scoreable` passes any event with no flight, so a
flightless tumble could never inherit the `tuning` flag raised on its flight, and a player who
lowered the tumble speed gate — the entire definition of a tumble, live-editable in the game's own
debug window — would score normally.
Feed: `"{h}'s kitten {name} took a tumble at {speed} m/s on {body}"`.

**Vectors.** `batch-001.ndjson` lines 19 and 20 — both have a non-null `flight`; line 19 carries
`from: "airborne"` for a botched landing, while line 20 carries `from: "grounded"` for a trip. The
pair pins both currently interpreted sides of the open-set discriminator cross-language without
turning them into a closed enum.

---

### `kitten.kia`

**Wire.** `"kitten.kia"` (`EventTypes.cs:77`), `ver` **2**, kind 1. `flight` = the flight the
kitten died on **when the mod can prove one**, else null (`EventPipeline.FlightForKia`, `:458-470`).

**Payload** — `KittenKiaPayload`, `Payloads.cs:183-186`

| Key | Type | Values |
|---|---|---|
| `kid` | string | `Ids.KittenId(installId, kittenName)` |
| `name` | string | `Ids.SanitizeName(...)` |
| `context` | string | `"manual_destroy"` when a `KillCrew` was noted within **2.0 sim seconds**, else `"unknown"` (`PolledSignals.cs:257,273`). **`"rud"` is never emitted by the shipped mod.** |

**Detector — a roster diff with one emit path** (`PolledSignals.PollRoster`, `:251-283`):

```
manualDestroyNearby = simT - _lastManualDestroySimT <= 2.0
foreach kitten in roster:
    wasKia = _kia[name] (default false)
    _kia[name] = kitten.Kia
    if (!_rosterSeeded || wasKia || !kitten.Kia) continue     // rising edge only, after a baseline
    emit KiaSignal(name, manualDestroyNearby ? ManualDestroy : Unknown)
_rosterSeeded = true
```

The **first roster read is a baseline**: loading a save that already contains KIA kittens must not
replay their deaths (`:264-266`). `_kia` and `_rosterSeeded` are cleared by `PolledSignals.Reset()`
at every save-load boundary.

**Game source.**

- Roster: `Universe.KittenRoster.Kittens` (`KSA/Universe.cs:94`, `KSA/KittenRosterData.cs:13`) read
  through `VehicleTelemetry.SampleRoster()` (`:679-705`). **Never cached across a load** — the whole
  `KittenRosterData` object is swapped on save-load (`KSA/Universe.cs:2178`) and new game (`:176`)
  (B8), so it is re-resolved every call.
- `Kia` flag: `KittenRosterEntryData.Kia` (`KSA/KittenRosterEntryData.cs:29`, `[XmlAttribute("KIA")]`).
  Written in **exactly one place**: `Kia = true` at `:108` inside `Kill(bool hasLaunched)` (`:96`).
  **Never reset to false.**
- Context **and flight**: a **prefix** on `Vehicle.KillCrew()` — `KSA/Vehicle.cs:2796`, installed
  `Patcher.cs:180-182`, body `:546-576`, calling `runtime.NoteManualDestroy(simT)` for the context
  and raising a `CrewKilledSignal` with the seat read for the flight (see below). `KillCrew` has
  **exactly one caller**, `KSA/InputEvents.cs:515`, guarded by `if (!Recovered)` — i.e. exclusively the player-initiated
  destroy path. It is therefore a **player-intent marker, not a fatality signal**. The physics RUD
  path calls `EndAllCrewMissions` and never touches `Kia`.
- Polled at **2 Hz**.

**Classification.** **PASSIVE** (2 Hz roster diff). Gates: baseline emits nothing; rising edge only;
a 2.0 sim-second proximity window decides `context`.

**Dedup.** The `_kia` dictionary keyed by roster name is the latch; since `Kia` is never reset by the
game, the rising edge can fire at most once per kitten per session.

**Flight attribution — exact or absent, never inferred** (`EventPipeline.cs:403-470`). Two paths
produce a flight:

1. **A `CrewKilledSignal` for that kitten within `CrewKillWindowSeconds` (2.0 sim seconds).**
   `Patcher.KillCrewPrefix` (`:546-576`) raises it from `Vehicle.KillCrew` — the only writer of the
   roster's `Kia` flag in the whole build (D11, [ksa-integration.md](ksa-integration.md) §4) — after
   registering the vehicle through `Track`, carrying `VehicleTelemetry.CrewNames(__instance)`
   (`:412`). That is the last moment the seats are readable and the flight is still open;
   `Vehicle.Dispose` follows in the same frame and the roster diff a tick later sees a name and
   nothing else. `OnCrewKilled` remembers `(kitten → flight, simT)` and evicts entries older than the
   window. **This is the path that fires in a real game**, because every KIA the current build can
   produce comes through `KillCrew`.
2. **The kitten is outside right now** — `_evaVehicles` (built from the `eva_start`/`eva_end` pair,
   not by matching the name against the tracker, because a player can name a *rocket* after a kitten
   and that lookup would then void the rocket's flight) says she is on EVA, and her EVA vehicle's
   flight is open.

It stays **null**, deliberately, when: no `KillCrew` was seen and she was not outside (a future build
that sets `Kia` by some other route); the seat read yielded no name for her (`CrewNames` swallows an
unreadable roster and skips a seat whose `AssignedKittenHash` does not resolve); the crew kill
happened on a vehicle with no open flight, which would otherwise name a flight the server has no
`flight.started` for; more than 2.0 sim seconds separate the crew kill from the diff that noticed the
death (the poll runs at 2 Hz, so this needs a stall or a time-warp jump); or the session was reloaded
between the two (`_crewKills` and `_evaVehicles` are cleared at the save-load boundary). A wrong
attribution would disqualify an innocent flight's impact record and cannot be appealed; a null costs
one disqualification that should have happened. See MOD-073.

**Server.** No board counts it directly. It feeds **rebuild pass 1**, which builds
`kia map[flightID][]simT` from `kitten.kia` events that carry a flight and a sim time
(`projector/rebuild.go:163-165`) — and that map is what disqualifies a
`biggest_lithobrake_survived` **and** a `biggest_impact_energy` claim within ±2.0 s. A KIA that
names no flight is absent from that map, deliberately: there is no key a null-flight KIA could be
indexed under that is not a guess (MOD-073). Feed: `"{h} said goodbye to kitten {name}"`.

**Vectors.** `batch-001.ndjson` line 31 — a non-null `flight`, `context: "manual_destroy"`.

---

### `roster.snapshot`

**Wire.** `"roster.snapshot"` (`EventTypes.cs:78`), `ver` 1, **kind 1 (scoring, never pruned)** —
called out explicitly at `EventTypes.cs:176-179` because it carries kitten totals that move boards.
`flight` = **null** (`EventPipeline.cs:306`).

**Payload** — `RosterSnapshotPayload`, `Payloads.cs:211-212`; rows `RosterKittenPayload`, `:196-203`;
built by `EventFactory.RosterPayload` (`:81-97`). Shape: `{"kittens": [ {…}, {…} ]}`.

| Row key | Type | Units | Source (KSA) |
|---|---|---|---|
| `kid` | string (16 Crockford) | — | `Ids.KittenId(installId, k.Name)` |
| `name` | string | — | `Ids.SanitizeName(k.Name)` — ≤ 32 printable ASCII |
| `travelled_m` | number | m | `KittenRosterEntryData.TravelledMeters.InMeters()` (`VehicleTelemetry.cs:691`). **`DistanceReference` is a CLASS**, not a double (B5). |
| `fastest_ms` | number | m/s | `KittenRosterEntryData.FastestSpeed.InMeters()` (`:692`). **ECLIPTIC-FRAME** — it includes the parent body's orbital motion, so it reads ~30 km/s standing still on Earth (B5). Recorded for completeness only; **must never become a speed board**. |
| `missions` | int | count | `KittenRosterEntryData.MissionCount` (`KSA/KittenRosterEntryData.cs:26`). Counts aborted pre-launch missions too. |
| `mission_time_s` | number | s | `KittenRosterEntryData.TotalMissionTime.GetSeconds()` — a `SimTimeReference` **class** (`:23`). **Only banks at mission boundaries, so it is stale mid-mission** (B5). |
| `kia` | bool | — | `KittenRosterEntryData.Kia` (`:29`) |

Roster entries with an empty `Name` are **skipped** (`VehicleTelemetry.cs:687-688`).

**Detector.** `EventPipeline.Dispatch`, `RosterSampleSignal` case (`:285-289`).

**Game source — two emission paths.**

1. **Periodic**: `PolledSignals.PollRoster` fires when `simT - _lastRosterSimT >= 600.0` **sim**
   seconds. Under time warp these arrive far more often in wall time.

   **Two cadences, two reads.** The KIA diff below runs on *every* 2 Hz tick and reads the roster
   through an allocation-free scan (`VehicleTelemetry.SampleRosterKia`, name and KIA flag only) — a
   death noticed ten minutes late would be attributed past the manual-destroy window and get the
   wrong cause. The `roster.snapshot` **payload** is built only on the tick it is about to be
   emitted. Rebuilding the full payload every tick and discarding it 1,199 times out of 1,200 was
   the largest per-tick allocation on the game thread (MOD-071). An empty roster skips the tick.
2. **Session end**: `PolledSignals.EmitRoster` (`:144-150`) from `CatlogRuntime.Dispose` **before**
   the signal channel is completed (`CatlogRuntime.cs:378-392`). This is **process unload only** — a
   save-load boundary calls `PolledSignals.Reset()` and emits **no** closing roster for the session
   that just ended.

**Classification.** **PASSIVE** (2 Hz poll gated by a 600 sim-second interval), plus one EVENT-DRIVEN
emission at process unload.

**Server.** `distanceFold` (`stats/boards.go:1028-1092`) writes **three** boards, and none of them
has a flag exclusion — this event carries no flight, so `scoreable` returns true unconditionally, and
it cannot be otherwise (PROJ-001). Every kitten row with a non-empty `kid` is upserted into the
lifetime `kitten` projection via `b.UpsertKitten`, and into `career_kitten` via
`b.UpsertCareerKitten` when the event has a career whose celestial system is known. **Every running
total merges with `max()` within its row**: a snapshot arriving out of order, or an earlier point in
the same save reloaded, can fail to advance a total but never rewind one.

`kid` is `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` and deliberately contains no
career. KSA's roster belongs to `UniverseData` — the save — so two saves can each contain a different
kitten named Mittens that shares one `kid`. The lifetime `kitten` row consequently answers "what is
the largest total ever seen for this install-and-name identity", while `career_kitten` keeps the two
save-local cats separate. The distinction is why the total-distance board reads the career rows.

| board | value |
|---|---|
| `distance_travelled` | `Σ career_kitten.travelled_m` over the saves in the selected scope (`setValue` / scoped set writers) |
| `top_kitten_distance` | the largest single `max(travelled_m)` in the selected scope (`putRecord`, gated `> 0`) |
| `top_kitten_missions` | the largest single `max(missions)` in the selected scope (`putRecord`, gated `> 0`) |

The two per-kitten boards are "who is your best cat" where `distance_travelled` is "how good is your
whole roster", and they are folded here because this is the only fold that writes the `kitten` and
`career_kitten` tables. Both take context `{"kitten"}`, the winner's display name.

The lifetime `Batch.KittenTops` **breaks ties on `kid`**. Career ties use `kid`; system ties use
`(career, kid)`, because the same `kid` can name separate cats in separate saves. These are not
niceties: the winner's name lands in the row's context, Go randomises map iteration order, and a
rebuild has to reproduce the incremental context byte for byte.

**Both inherit `distance_travelled`'s exemption from the flag exclusion**, so a kitten who did all
her travelling on a teleported flight still holds the record. Not fixable server-side — the fix would
be the mod attributing roster totals to the flights that earned them. Recorded rather than papered
over.

`fastest_ms` is still read by nothing, deliberately: it is the game's ecliptic-frame `FastestSpeed`
and must never become a speed board.

**Vectors.** `batch-001.ndjson` line 33 — two kitten rows, one `kia`, and the always-null `flight`.

---

### `telemetry.window`

**Wire.** `"telemetry.window"` (`EventTypes.cs`), `ver` 1, **the only kind-0 (passive, droppable)
type**. `flight` = `tracker.FlightFor(window.VehicleId)`.
**`sim_t` = `window.Payload.T1Sim`**, the sim time of the window's *last sample*, not the emission
instant (`EventFactory.FromWindow`, `:32-40`) — which is why in-session emission is slightly out of
order by design.

**Payload** — `TelemetryWindowPayload`, `Payloads.cs:407-429`. `agg` = `{"min", "max", "mean", "last"}`
(`Agg`, `:13-17`); an empty fold yields `Agg(0,0,0,0)`.

| Key | Type | Units | Optional | Source |
|---|---|---|---|---|
| `t0_sim` | number | sim s | no | sim time of the **first** sample |
| `t1_sim` | number | sim s | no | sim time of the **last** sample |
| `n` | int | count | no | samples folded |
| `body` | string | — | no | body at the **end** of the window |
| `alt_m` | agg | m above mean radius | no | `Vehicle.GetBarometricAltitude()` |
| `surface_speed_ms` | agg | m/s | no | `Vehicle.GetSurfaceSpeed()` (`KSA/Vehicle.cs:2759`) |
| `orbital_speed_ms` | agg | m/s | no | `Vehicle.OrbitalSpeed` (`KSA/Vehicle.cs:581`) |
| `accel_ms2` | agg | m/s² | no | `Vehicle.AccelerationBody.Length()` (`KSA/Vehicle.cs:557`, a `double3`) |
| `peak_g` | number | g | **YES — omitted entirely** when no sample carried a reading | `[JsonIgnore(WhenWritingNull)]`, `Payloads.cs:416-418`; fold `WindowAccumulator.cs:148-149` |
| `max_q_pa` | number | Pa | **YES — omitted** under the same rule | `Payloads.cs:419-421`; `WindowAccumulator.cs:150-151` |
| `mass_kg_last` | number | kg | no | mass at the **end** of the window |
| `radar_alt_m` | **agg** | m above the surface beneath | **YES — omitted entirely** when no sample carried a reading | `Payloads.cs:423-425`. Folded over **only** the samples that had a terrain reading — the `peak_g` rule, applied to an aggregate. |
| `warp_max` | number | × | no | The highest `Universe.SimulationSpeed` (`KSA/Universe.cs:100`) seen in the window. **`1` is real time, and `1` is also the unreadable fallback — never `0`**, because an unreadable warp is not a stopped clock. |
| `state` | object `{pos,vel}` of `{x,y,z}` numbers | `pos`: m; `vel`: m/s | **YES — whole object omitted** unless all six numbers are finite and belong to `body` | State at the **last sample**, in the parent-body-centred inertial (CCI) frame; `TelemetrySnapshot.State` → `WindowAccumulator._state` → `TelemetryWindowPayload.State` (`TelemetrySnapshot.cs:126-130`; `WindowAccumulator.cs:157-180`; `Payloads.cs:403-429`). |

**`state` is one atomic, last-sample reading, not an aggregate.** A mean position has no useful
physical meaning, and position without velocity can only be interpolated; the pair can be
propagated even when a neighbouring passive window was pruned. Every sample assignment, including
`null`, replaces the prior `_state` (`WindowAccumulator.cs:157-162`), just as the sample replaces
`body` and `mass_kg_last`. Thus a failed read or an SOI/body change at the end of a window clears an
older valid state rather than mislabelling old-parent coordinates with the new `body`. One
non-finite/unreadable component omits the whole object: no component is zero-filled and no origin
fallback is emitted.

**`radar_alt_m`'s population is not `n`.** `n` remains the total sample count; the aggregate is
folded only over the samples that carried a reading, and the key is absent altogether when none did —
which is every window spent in orbit, where there is no terrain below to read. Nothing may
reconstruct a count from it, and a decoder must read it as an optional object rather than an `agg`
that happens to be zeroed. This is the `peak_g` rule and it is stated separately here because it is
the first *aggregate* to take it.

**Why `warp_max` is on the payload at all.** A window samples at 2 Hz on the **wall** clock but spans
30 **sim** seconds, so under time warp its aggregates are drawn from a handful of samples rather than
the nominal 60, and nothing else in the payload says so. It is **descriptive only**: under
Constitution §8 it may inform a reader, weight or annotate a value; it must not reject or disqualify
a record, and it is not a cheat signal. PROJ-098.

**Why `peak_g` / `max_q_pa` are omitted rather than zeroed.** `TelemetrySnapshot.PeakG`/`MaxQPa` are
`double?`. `VehicleTelemetry.PeakG` (`:220-234`) reads `ref readonly Vehicle.StructuralLoad`
(`KSA/Vehicle.cs:531`) and returns **null** when `load.MaxGLoad <= 0` or `PeakGLoad` is non-finite.
`StructuralLoad` is written **only inside `ApplyFullPhysics`** (`KSA/VehicleUpdateTask.cs:492-497`)
and reset to `default` every prepared step (`KSA/VehicleUpdateState.cs:287`), so **an all-zero struct
means "no data this step"** (on rails, or in freefall), not "zero g" (B10). `MaxGLoad` is the
discriminator: `VehicleStructuralLimits.EffectiveMaxGLoad` floors at 5, so it is always ≥ 5 when the
struct was written and exactly 0 when it was not. `MaxDynamicPressure` is the hard-coded 200 kPa
limit set beside `PeakDynamicPressure`, so it is the same discriminator. **Churn risk High —
`StructuralLoad` is new in 5168.** Reporting 0 would fill the peak-g board with fake minima.

**Detector** — `WindowAccumulator` (`Detect/WindowAccumulator.cs`). Windows are **half-open in sim
time**: a window opened at `t0` covers `t0 ≤ t < t0 + windowSeconds`. A sample at exactly
`t0 + window` **closes** the window and becomes the first sample of the next one (`:19-22`, `Add`
`:52-77`), so a 2 Hz stream over 30 s produces windows of **60 samples** and no sample lands in two
windows. `Add` handles a backwards `sim_t` jump by **discarding** the partial window and starting over
(`:60-66`) — it spans two timelines and its mean would be meaningless.

**Four close paths**, not one:

| Path | Where | Emission |
|---|---|---|
| a sample crosses the boundary | `Add`, `:68-73`, driven from `EventPipeline.ProcessFrame` `:107-110` | the normal case, `n == 60` |
| a flight ends | `Flush(vehicleId)` from `EventPipeline.EndFlight` `:394-396` | partial window **before** `flight.ended`, so the seconds before a RUD are kept |
| a vehicle vanished without a removal signal | `EventPipeline.PruneStaleVehicles` `:412-430` | partial window |
| session end | `FlushAll()` from `EventPipeline.Flush` `:173-175` | every open window, in **no particular order** |

`Forget(vehicleId)` (`:108`) discards a partial window **without** emitting — used after a flight has
already been closed.

**Game source.** The whole snapshot is built by `VehicleTelemetry.Sample(vehicle, simT, wallMs)`
(`:160-231`), one per live vehicle per due tick, from `CatlogRuntime.SamplePass` (`:490-510`). **A
vehicle whose read throws is omitted from the frame, never zero-filled** (`VehicleTelemetry.cs:225-230`;
`CatlogRuntime.cs:502-508`) — a zeroed
snapshot fed to a prev/curr comparator manufactures phantom SOI changes (`body → ""`) and phantom
orbit-achieved edges (`ecc → 0`), and both of those score.

`VehicleTelemetry.StateOf` (`:233-272`) reads `Orbit.StateVectors` by ref, then
`StateVectors.PositionCci` and `VelocityCci`. The game declares that ref-read property at
`KSA/Orbit.cs:1150`, the readonly fields at `KSA/StateVectors.cs:6-14`, and transforms both vectors
through `Orb2ParentCci` in `Orbit.GetStateVectorsAt` (`KSA/Orbit.cs:2107-2113`). The result is raw game
metres and metres per second relative to `Orbit.Parent` in that parent's centred inertial frame.
The method re-reads and normalises `Orbit.Parent` and requires it to equal the snapshot `body` before
accepting all six finite components; any read failure returns `null` while retaining the rest of the
sample.

**Classification.** **PASSIVE** — this is the archetype. 2 Hz sampling, 30 sim-second aggregation
window, no debounce. It is the registry's only `KindPassive` event and the outbox's only prunable
record. Putting path state here means it is deliberately shed before any discrete gameplay event
when the local spool is under pressure.

**Server.** Six boards read it.

| board | field | eligibility |
|---|---|---|
| `peak_g_survived` | `peak_g` | `survivedLoad` |
| `max_q_survived` | `max_q_pa` | `survivedLoad` |
| `fastest_surface_speed` | `surface_speed_ms.max` | `> 0`, unflagged |
| `fastest_orbital_speed` | `orbital_speed_ms.max` | `> 0`, unflagged |
| `highest_altitude` | `alt_m.max` | `> 0`, unflagged |
| `lowest_pass` | `radar_alt_m.min` | present **and** `> 0`, unflagged |

`survivedLoad` (`stats/boards.go:474-489`) is the eligibility the first two share, and they share it
because they are the same reading: both come off `Vehicle.StructuralLoad`, both are **`*float64`** so
absent ≠ zero, and both are boards about living through something. It requires a non-nil reading
`> 0`, an unflagged flight, and — **rebuild only** — `flight_state.ended_reason == "recovered"`. Peak
g is how hard the airframe was squeezed; max q is how hard the air was pushing, and an ascent profile
can be brutal on one and gentle on the other.

`highest_altitude` (`altitudeFold`, `:575-599`) deliberately takes the **plain** flag exclusion and
not `survivedLoad`'s recovered-flight rule: an altitude is a position, always sampled and always
meaningful, and a probe that never came back still got there. It is barometric, so a mountaintop
landing scores its elevation and a low pass over a canyon does not.

All six share `windowContext` (`:493-499`) — `{"body", "flight", "t1_sim"}`, `t1_sim` in **seconds**.
`state`, `accel_ms2`, `mass_kg_last`, `n`, `t0_sim`, `warp_max` and every aggregate member that is not
`alt_m.max` / `surface_speed_ms.max` / `orbital_speed_ms.max` / `radar_alt_m.min` are decoded and
read by no fold. `state` is future visualisation material only; it changes no projection, board or
feed context.

**`warp_max` decodes as `0` when a payload does not carry it, not as `1`.** The intended default is
`1` — a stopped clock is not a legal warp — but Go's zero value for the field is `0` and nothing reads
it today, so this is currently invisible. If a future reader wants it, **`0` must be treated as
"absent" at the read site**. PROJ-098.

**Vectors.** `batch-001.ndjson` lines 11 and 15 — the pair that pins the `agg` objects and the
omit-don't-zero optionals from both sides. Line 11 is an ascent with `peak_g`, `max_q_pa`,
the `radar_alt_m` aggregate all **present**, `warp_max: 1`, `n: 60`, `t0_sim: 100.5`, `t1_sim: 130.5`
and a complete finite `state` — exactly a 30 s window at 2 Hz with the atomic state-present case.
Line 15 is the same type coasting at `warp_max: 1000` with all four optional readings **absent** and
`n: 3`, which makes both the warp short-window and state-absence cases testable.

---

## Projections

Everything below is derived from `events.db` and lands in `projections.db`. **The two files cannot be
joined** (`store/projections.go:57-60`): `player_id → handle` is resolved in Go from an in-memory
directory, and `updated_seq → recv_time` by `store/directory.go:62` (`Events.RecvTimes`, chunked at
200 seqs).

### Ingest → log

- `INSERT OR IGNORE` against `UNIQUE INDEX ev_dedup ON event(player_id, event_id)`
  (`migrations/events/0001_init.sql:61`), performed by `store/events.go:434 InsertEvents` (chunked
  multi-row, `EventInsertChunk = 500`). Returns `(accepted, deduped)`.
- Whole-batch replay short-circuit: `ingest_batch(player_id, batch_id)` where `batch_id` is the
  proof's `jti` (`0001_init.sql:65-69`; `store/events.go:651 BatchSeen`, `:670 InsertBatch`).
- Per-stream hash chain `stream_state` (`0001_init.sql:73-78`) — forensic only; a gap is recorded,
  not rejected.
- `recv_time` is stamped server-side at insert (`store/events.go:445`), **never** from the client's
  `wall_time`.

### Projector loop (incremental)

`server/internal/projector/projector.go`:

1. `Step` takes `applyMu`, reads the shared checkpoint (`store.AllProjections = "all"`), then
   `events.EventsSince(ctx, after, batchSize)`.
2. Payload decode fans out across `Decoders` goroutines (`projector.go:290 decodeAll`).
3. One `projections.db` write transaction (`:292 WithWriteTx`): `stats.NewBatch(tx, …)`; for each
   decoded event apply **all** folds in order (`:303 applyFolds`); render a feed row (`:306`);
   `b.Flush(ctx)`; insert feed rows + `CapFeed(500)`; `SetCheckpoint` **in the same transaction**.
4. After commit: publish feed rows to the SSE broadcaster and the whole raw batch (decode failures
   included) to the raw broadcaster.
5. **Undecodable events are skipped and the checkpoint still advances** (`:298-302`, `:395`;
   PROJ-014).

**Fold order** (`stats/fold.go`) — the state folds, then the stable board-fold sequence, then the
census. A combined source fold emits its append-only board outputs at the source's existing
position; fixed-board publish order remains the separate table above:

```
system → flight_state → career
→ biggest_lithobrake_survived → peak_g_survived → max_q_survived → biggest_impact_energy
→ fastest_surface_speed → fastest_orbital_speed → fastest_entry → highest_altitude
→ highest_apoapsis → lowest_orbit → roundest_orbit → steepest_orbit → softest_touchdown
→ heaviest_launch → most_parts → biggest_crew → biggest_recovery → most_stages → longest_eva
→ kitten_tumbles(+botched_landings, +tumbles_on_<body>)
→ rud_total(+rud_<cause>, +parts_lost, +biggest_parts_lost, +biggest_crew_wreck, +kittens_wrecked)
→ orbits_achieved → soi_bodies → landed_bodies
→ dockings → stagings → splashdowns → evas → flameouts → engine_ignitions → kittens_recovered
→ distance_travelled(+top_kitten_distance, +top_kitten_missions) → fastest_to_orbit
→ fastest_to_body → career_playtime → play_sessions → kittens_to_orbit_and_back → body_sprints → census
```

Order matters in three places: `systemFold` must precede `careerFold` and every board because a
same-batch discovery binds the career before its session and scores are folded; `flightFold` must
precede every board (the flag check); and `soiFold` → `toBodyFold` → `bodySprintFold` establishes,
lowers and then counts the save-local SOI times. Everything else is listed in
board-metadata order purely so the two lists read the same way; no two board folds write the same
`(player_id, stat)`.

### `stats.Batch` — the write-back accumulator

`stats/batch.go`. In-memory read-through caches (`systems`, system bodies, `flights`, `careers`,
`bodies`, `kittens`, `values`, career values, career systems and badge awards) plus per-`statKind`
write accumulators for player, career, system and period rows, flushed as multi-row statements
(`DefaultFlushRows = 500`). Flush order is fixed and every widened key is sorted before writing, so
a rebuild is byte-comparable to the incremental result.

| kind | rule | `player_stat` guard | `player_stat_period` guard |
|---|---|---|---|
| `kindRecord` | strictly larger wins | `WHERE excluded.value > player_stat.value` (`:830-832`) | `WHERE excluded.value > …` (`:844-846`) |
| `kindBest` | strictly smaller wins | `WHERE excluded.value < …` (`:833-835`) | `WHERE excluded.value < …` (`:847-849`) |
| `kindCount` | `value = value + excluded.value` (`:836-837`) | same (`:850-851`) | |
| `kindSet` | replace outright, `WHERE excluded.value <> player_stat.value` (`:838-840`) | **no period form** — `setValue` writes its *delta* through `periodAdd` instead (`fold.go:200-202`) | |

**Tie semantics.** Because record/best replace only on a **strict** inequality, an equal value leaves
the earlier `updated_seq`: *whoever got there first keeps the rank* (`stats/doc.go:30-33`).

### The board projection tables

**`player_stat`** — `migrations/projections/0001_init.sql:20-26`:
`player_stat(player_id, stat, value REAL, context TEXT /*JSON*/, updated_seq, PRIMARY KEY(player_id, stat))`
+ `INDEX stat_rank(stat, value)`. Because the PK is `(player_id, stat)`, `count(*) GROUP BY stat`
**is** the distinct-player count — no `DISTINCT` needed (PROJ-034).

**`career_stat`** — `0006_career_scope.sql`: one row per `(player_id, career, stat)`, with the
career's `system` denormalised beside `value`, JSON `context` and `updated_seq`. It ranks saves rather
than players. **`system_stat`** uses `(player_id, system, stat)` and ranks one player's result in one
celestial system. Both carry the same strict record/best tie rules and additive counter rule as
`player_stat`; neither carries a period column.

`putRecord`, `putBest` and `addCount` write the player row, then fan the same contribution into the
career and known-system rows through one shared helper. This is intentionally universal: all fixed
boards, all three dynamic families and any future ordinary board get the scopes without a registry.
An event with no career still moves only the player row. A career whose system is not yet known gets
its career row and no system row.

`kindSet` does **not** use that fan-out. A derived player total, one save's total and the union across
all saves in a system are different queries; `setValue`, `setCareerValue` and `setSystemValue` accept
those separately so a lifetime total can never be copied into a row labelled as one save.

**`player_stat_period`** — `0003_period.sql:38-50`:
`player_stat_period(player_id, stat, period, bucket, value REAL, context, updated_seq,
PRIMARY KEY(player_id, stat, period, bucket))`.

Periods: `alltime` (which *is* `player_stat`, never stored here), `daily`, `weekly`, `monthly`,
`yearly` (`stats/period.go:12-18`). Bucket keys are UTC, always: `daily 2006-01-02`,
`weekly %04d-W%02d` from `t.ISOWeek()`, `monthly 2006-01`, `yearly 2006`. All sort chronologically as
plain text, which is what makes retention a `bucket < ?` delete and `?at=` validation a shape check.

Every board value fans out into the windows through one of four helpers (`stats/periodwrite.go:39/47/58`):
`putRecord → periodRecord`, `putBest → periodBest`, `addCount → periodAdd`, and `setValue →
periodAdd` with `value - prev` **only when positive** — so "distance travelled *this month*" is an
increase, not a lifetime figure wearing a monthly label (PROJ-044). An event with `RecvTime <= 0`
writes **no** windows at all; the all-time board still has it.

Retention (`stats/period.go:56-61`): daily 90, weekly 53, monthly 36, yearly 20 buckets. The cutoff
walks calendar steps back from the event's own `recv_time`, and trimming fires when
`ev.Seq % 512 == 0`, inside the projector transaction.

A **period is a dimension of a board, not a board** (PROJ-042): the board index publishes
`periods: stats.Periods()` per board. `[boards] min_players` is evaluated **only on the all-time
board**; a published board's windows are published with it, and `?period=weekly` may legitimately be
empty (PROJ-045).

### The badge award projection table

Migration `0011_badges.sql` creates
`badge_award(player_id, career, badge, system, first_career, earned_seq, earned_at,
earned_sim_t, context, PRIMARY KEY(player_id, career, badge))`. The empty career sentinel is the
lifetime award; a nonempty career is an award earned independently by that save. The same badge may
therefore have one lifetime row and one row for each save that qualifies. A lifetime row records the
system and `first_career` in which the **current projection** first awards it. A per-save row records
that save's system and leaves `first_career` empty because its primary-key career already supplies
the provenance (`migrations/projections/0011_badges.sql:6-20`).

The three read-order indexes are `badge_system(system, badge, earned_seq)`,
`badge_holders(badge, earned_seq)` and `badge_by_career(player_id, career, earned_seq)`. The metadata
registry is documented below.

The store reads preserve every award field and keep raw save identity below the read-API boundary.
`BadgesForPlayer(player, career)` filters the career exactly, including the empty lifetime sentinel.
An unfiltered holder list/count likewise uses only lifetime rows. A system-filtered holder query
cannot filter lifetime provenance: a player may first earn the badge in one system and earn it again
in a later save in another. It therefore considers nonempty per-save rows in the requested system,
selects one deterministic earliest row per player by `earned_seq` then raw-career tie-break, and
orders the resulting holders by `earned_seq` then `player_id`. Counts use the identical population,
so saves never inflate the holder denominator. `BadgeCounts` groups lifetime rows only
(`store/projections.go`, merit-badge reads).

The only public F6 use is the collection census: `GET /v1/stats` adds `collection.badges`, the number
of distinct keys with a lifetime holder, and `collection.badge_awards`, the total lifetime plus
per-save award rows. They share the response's `(WriteGen, 10 s TTL)` memoization. Individual badge
and player-award endpoints remain absent until G1.

Within one projection build, an award is first-write via `INSERT ... ON CONFLICT DO NOTHING`.
`earned_seq` is the first qualifying projector sequence, `earned_at` is that event's server
`recv_time` in Unix milliseconds — never client `wall_t` — and nullable `earned_sim_t` is the event's
career clock in seconds when present. `context` is nullable projector-authored JSON with the same
shape and public promise as `player_stat.context`: recursive career/kitten relabelling applies before
publication, and default tables show only the established `body`, `from`, `energy_j` and `t1_sim`
allow-list. It is never an unfiltered client payload.

“Once” means the earliest row in the current projection, not an irrevocable entitlement. There is
no `revoked` state. Rebuilding from seq 0 creates a fresh `badge_award`, so current folds and final
flight state may omit an award formerly present, while a newly added rule may discover one in old
history. This is the same rebuild-authoritative correction model as the boards, without a second
historical eligibility engine.

The rows are player-owned. Withholding a player's events and rebuilding removes every award;
restoring those events at their original sequence numbers and rebuilding can restore them with the
same first-award ordering. Purging their events makes the removal permanent. No moderation path
enumerates this table: structural log exclusion plus rebuild supplies Constitution §7's totality,
while shared `system` and `system_body` rows remain catalogue facts rather than player awards
(STORE-019).

### Badge accumulator and dual-scope writer

`stats.Batch` keys pending awards by `(player_id, career, badge)` and retains the complete candidate:
system, lifetime `first_career`, `earned_seq`, `earned_at`, nullable `earned_sim_t` and encoded
context. `putBadge` is first-write at both layers. In memory, a new candidate replaces an existing
pending candidate only when its sequence is lower, and replaces the **whole** value so timestamp,
career time, system, first-save provenance and context still belong to that earliest event. At SQL
flush, `INSERT ... ON CONFLICT DO NOTHING` preserves any row already written by an earlier batch.
The two rules together make projection output independent of projector and flush batch size; SQL
cannot recover the earliest candidate if the pending map already overwrote it. A row loaded from
SQL is immutable in the cache (`stats/batch.go:1545-1610`).

`award` is the shared two-scope helper. It encodes context once and resolves the event career's
known system once. It always offers the lifetime key `(player, '', badge)`, putting the event career
in `first_career`; when the event has a career it also offers the independent per-save key and leaves
`first_career` empty. Both candidates keep the same event sequence, server receive timestamp,
nullable career clock and context. `HasSimTime` preserves the distinction between SQL NULL and a
real clock reading of zero. If context encoding fails, neither scope is offered. Missing career
identity still permits the lifetime candidate; missing system identity is retained honestly as the
empty system rather than suppressing either scope.

`award` deliberately performs no `scoreable` check. The registered badge fold owns its predicate and
eligibility because flightless events have no flight state to gate, while every flight-bearing fold
must use the existing final-state rule. It also performs no registry check: a concrete fold supplies
its compile-time or validated family key (`stats/fold.go:296-313`).

`HasBadge(player_id, career, badge)` is a composite-badge read-through: it checks the pending map
before querying `badge_award`, caching both existence and absence, so a later event in the same
projector batch observes an award not yet flushed to SQL. `flushBadges` sorts by player id, career
and badge, writes bounded nine-column multi-row statements, and runs in `Batch.Flush` immediately
after career-stat rows and before system-stat rows. A conflict is a successful no-op, not an update.
The entries survive the flush as a read-through cache with `pending` cleared, matching the other
Batch caches (`stats/batch.go:1564-1638,1873-1884,1977-1985`).

### Badge catalogue registry

The registry contains **35 fixed badges in five ordered groups plus three exploration-family
patterns**. Metadata is a pure function of the key: `DescribeBadge` does not consult stored awards,
and a valid family key can therefore be titled even when that exact body name was never compiled
into the server. `FixedBadges` retains the order below. `BadgeCatalog` inserts eligible family
members after `been_to_everything`, in declared family order (`reached_`, `orbited_`, `landed_on_`)
and key order within each family, before continuing with the kitten group. Group order is
`first-steps`, `flight`, `survival`, `exploration`, `kittens`. Tier is presentational only. Earning
a higher tier never removes a lower one (`stats/badges.go:57-138,196-230`).

| # | key | Title | Blurb | Group | Tier | Derivation |
|---|---|---|---|---|---:|---|
| 1 | `first_flight` | Off The Pad | Your first flight. | `first-steps` | — | first `flight.started` |
| 2 | `first_stage` | Separation | You let go of something on purpose. | `first-steps` | — | first `vehicle.staging` |
| 3 | `first_space` | Above The Air | You left the atmosphere. | `first-steps` | — | first `vehicle.atmosphere` with `dir == "exited"` |
| 4 | `first_orbit` | Around We Go | You made orbit. | `first-steps` | — | first `vehicle.orbit` with `phase == "achieved"` |
| 5 | `first_landing` | Wheels Down | You put something down and it survived. | `first-steps` | — | first surviving `vehicle.landed` |
| 6 | `first_recovery` | Home Again | You recovered a vehicle. | `first-steps` | — | first `flight.ended` with `reason == "recovered"` |
| 7 | `first_eva` | Outside | A kitten went out. | `first-steps` | — | first `kitten.eva_start` |
| 8 | `first_dock` | Well Met | Two of your vehicles became one. | `first-steps` | — | first `vehicle.docked` |
| 9 | `first_rud` | It Happens | You lost a vehicle. Everyone does. | `first-steps` | — | first `vehicle.rud` |
| 10 | `crewed_orbit` | Passengers | You brought company into orbit. | `flight` | — | achieved orbit on a flight whose start crew was at least 1 |
| 11 | `orbit_and_back` | Round Trip | You made orbit and brought the vehicle home. | `flight` | — | recovered flight whose first achieved-orbit sequence is strictly earlier than the recovery event |
| 12 | `docked_after_orbit` | Rendezvous | You docked after making orbit. | `flight` | — | docking whose flight's first achieved-orbit sequence is strictly earlier; does not claim docking occurred in orbit |
| 13 | `coaster` | Along For The Ride | You reached another sphere without an engine. | `flight` | — | SOI arrival on a flight with known `engine_count == 0` |
| 14 | `heavy_lifter` | Heavy Lifter | You put a notably heavy payload into orbit. | `flight` | — | `heaviest_to_orbit >= 20,000` |
| 15 | `big_stack` | Tall Order | You built a stack with ambitions. | `flight` | — | `biggest_stack >= 5` |
| 16 | `many_parts` | Kit Bash | You assembled a hundred parts into one vehicle. | `flight` | — | `most_parts >= 100` |
| 17 | `well_lit` | Well Lit | You have lit rather a lot of engines. | `flight` | — | `engine_ignitions >= 100` |
| 18 | `lithobraker` | Lithobraker | You survived an enthusiastic arrival. | `survival` | 1 | `biggest_lithobrake_survived >= 50` |
| 19 | `ground_truth` | Ground Truth | You survived an even more enthusiastic arrival. | `survival` | 2 | `biggest_lithobrake_survived >= 100` |
| 20 | `pressed` | Pressed | You remained attached through ten g. | `survival` | — | `peak_g_survived >= 10` |
| 21 | `feather` | Feather | You landed with unusual restraint. | `survival` | — | `0 < softest_landing <= 0.5` |
| 22 | `canyon_run` | Canyon Run | You passed within a hundred metres of the ground. | `survival` | — | `0 < lowest_pass <= 100` |
| 23 | `old_hand` | Old Hand | You have landed often enough to look practised. | `survival` | — | `landings >= 25` |
| 24 | `wanderer` | Wanderer | You reached three worlds. | `exploration` | 1 | `soi_bodies >= 3` |
| 25 | `voyager` | Voyager | You reached five worlds. | `exploration` | 2 | `soi_bodies >= 5` |
| 26 | `grand_tour` | Grand Tour | You reached eight worlds. | `exploration` | 3 | `soi_bodies >= 8` |
| 27 | `groundskeeper` | Groundskeeper | You landed on three worlds. | `exploration` | — | `landed_bodies >= 3` |
| 28 | `been_to_every_planet` | Every World | You visited every planet in this system. | `exploration` | — | **inactive until F7**; entered every `kind == "planet"` body in the save's effectively complete system catalogue |
| 29 | `been_to_everything` | Nothing Left | You visited everything in this system. | `exploration` | — | **inactive until F7**; entered every body in the save's effectively complete system catalogue |
| 30 | `not_on_their_feet` | Not On Their Feet | A kitten failed to land on their feet. | `kittens` | — | first `kitten.tumble` with `from == "airborne"` |
| 31 | `persistently_upside_down` | Persistently Upside Down | Your kittens have tumbled fifty times. | `kittens` | — | `kitten_tumbles >= 50` |
| 32 | `crowded_capsule` | Crowded Capsule | You brought four kittens home at once. | `kittens` | — | `biggest_recovery >= 4` |
| 33 | `spacewalker` | Spacewalker | Your kittens have taken ten spacewalks. | `kittens` | — | `evas >= 10` |
| 34 | `the_long_walk` | The Long Walk | A kitten spent an hour outside. | `kittens` | — | `longest_eva >= 3,600` |
| 35 | `ferry_service` | Ferry Service | You brought ten kittens to orbit and home. | `kittens` | — | `kittens_to_orbit_and_back >= 10` |

| Family key | Title derivation | Blurb derivation | Group | Derivation |
|---|---|---|---|---|
| `reached_<body>` | `"Reached " + titleize(body)` | `"You reached " + titleize(body) + "."` | `exploration` | `vehicle.soi.to_body` |
| `orbited_<body>` | `"Orbited " + titleize(body)` | `"You made orbit around " + titleize(body) + "."` | `exploration` | achieved `vehicle.orbit.body` |
| `landed_on_<body>` | `"Landed on " + titleize(body)` | `"You landed on " + titleize(body) + "."` | `exploration` | surviving `vehicle.landed.body` |

All three call the board registry's exact `statSuffix` rule: lowercase; first character
`[a-z0-9]`; remaining characters `[a-z0-9._-]`; value-derived suffix at most 40 characters.
`familyBadge` also rejects a fixed-key collision (`stats/badges.go:140-159`). `titleize` is shared
too, so board and badge titles cannot disagree about the same opaque game name. There is no
celestial-body allow-list.

Every fixed badge is always catalogued. A dynamic family member is catalogued only when at least
`[boards] min_players` distinct players hold it; this reuses the board-family community-publication
threshold rather than creating a second knob. A caller value below 1 uses
`DefaultMinPlayers == 2`. The threshold affects listing only, not whether a valid key can be
described or stored. `KnownBadge` accepts every fixed key and a valid family key with at least one
holder for direct lookup; the stricter catalogue gate is separate
(`stats/badges.go:161-230`).

F5 activates every predicate expressible with F4's four shapes: **33 fixed badges plus all three
dynamic family folds**. The two fixed subset badges `been_to_every_planet` and
`been_to_everything` remain registered metadata but deliberately have no fold until F7 implements
the effectively-complete-catalogue comparison; they cannot currently produce awards.
`SecondPassFolds` is ordered
`BoardFolds → BadgeFolds → LogFolds`, so threshold shapes read the post-write player and career board
values through `Batch`. Event, composite and family shapes offer their first qualifying event to the
shared two-scope `award` helper. Every concrete fold name contains its fixed badge key or stable
family name, so adding or removing one changes `BuildID` and reconsiders immutable history.

Composite predicates read the completed `flight_state` row instead of correlating events inside a
fold. Launch-fact composites use `HasStartFactAt`. The two orbit-order composites additionally
require `0 < first_orbit_seq < candidate.seq`; the milestone bit alone is insufficient because a
rebuild's first pass has already seen future events. An orbit before docking or recovery qualifies,
an orbit after it does not, and equal sequence numbers do not manufacture an order. Family
predicates derive keys through `statSuffix`; an unkeyable body skips only its family badge and still
counts toward fixed set and threshold badges. Every flight-bearing shape uses the existing
final-state `scoreable` rule.

The active folds are in the exact catalogue/group order shown above, with the three family folds in
their declared order at the exploration slot where F7's two subset folds will later sit before them.
All fixed awards use SQL NULL context: their badge key, earning sequence and documented predicate
already identify the achievement, so copying arbitrary payload or a transient board winner into
context would add data without meaning. A family award has exactly one context key,
`{"body": <opaque game name>}`, because the derived key is normalised and the original reported name
is the only additional fact its future detail surface needs.

---

## Boards

`stats/boards.go`. `Board` is `{Stat, Title, Unit, Ascending, Career}` (`:52-69`). **`Unit` is a
label, never a conversion factor.** `Career` marks a value that is a career-relative time and whose
row context carries `career`.

**Every board has player, career and system scope.** Scope is a ranking dimension, not a second board
key: player scope ranks players, career scope ranks `(player, save)` pairs, and system scope ranks
`(player, celestial system)` pairs. This applies to all 50 fixed rows below and all three dynamic
families, with no opt-out list. `Career` in the table is unrelated: it says the board's *value* is a
time measured from the start of a career.

### The 50 fixed boards, in display order

`stats/boards.go`. Display order **is** publish order — it is the order `FixedBoards()`
returns and therefore the order `GET /v1/leaderboards` lists — and it is grouped by kind rather than
by source: the "how did you survive that" records first, then the speed and shape records, then what
was on the pad, then the counters and roster totals, then the career-time and save-native boards.
The Phase E boards append after the original 42 so existing positions remain stable. The three
dynamic families slot under `kitten_tumbles`, `rud_total` and `fastest_to_orbit`.

| # | key | Title | Unit | Asc | Career | Source event | Fold kind |
|---|---|---|---|---|---|---|---|
| 1 | `biggest_lithobrake_survived` | Biggest Lithobrake Survived | `m/s` | no | no | `vehicle.impact` | record (max) |
| 2 | `peak_g_survived` | Peak G Survived | `g` | no | no | `telemetry.window` | record (max) |
| 3 | `max_q_survived` | Max Q Survived | `Pa` | no | no | `telemetry.window` | record (max) |
| 4 | `biggest_impact_energy` | Biggest Bang Survived | `J` | no | no | `vehicle.impact` | record (max) |
| 5 | `fastest_surface_speed` | Fastest Surface Speed | `m/s` | no | no | `telemetry.window` | record (max) |
| 6 | `fastest_orbital_speed` | Fastest Orbital Speed | `m/s` | no | no | `telemetry.window` | record (max) |
| 7 | `fastest_entry` | Fastest Atmospheric Entry | `m/s` | no | no | `vehicle.atmosphere` | record (max) |
| 8 | `highest_altitude` | Highest Altitude | `m` | no | no | `telemetry.window` | record (max) |
| 9 | `lowest_pass` | Lowest Pass | `m` | **yes** | no | `telemetry.window` | best (min) |
| 10 | `highest_apoapsis` | Highest Apoapsis | `m` | no | no | `vehicle.orbit` | record (max) |
| 11 | `lowest_orbit` | Lowest Stable Orbit | `m` | **yes** | no | `vehicle.orbit` | best (min) |
| 12 | `roundest_orbit` | Roundest Orbit | *(empty)* | **yes** | no | `vehicle.orbit` | best (min) |
| 13 | `steepest_orbit` | Most Inclined Orbit | `deg` | no | no | `vehicle.orbit` | record (max) |
| 14 | `softest_touchdown` | Softest Touchdown | `m/s` | **yes** | no | `vehicle.situation` | best (min) |
| 15 | `softest_landing` | Softest Landing | `m/s` | **yes** | no | `vehicle.landed` | best (min) |
| 16 | `heaviest_launch` | Heaviest Launch | `kg` | no | no | `flight.started` | record (max) |
| 17 | `heaviest_to_orbit` | Heaviest Payload To Orbit | `kg` | no | no | `vehicle.orbit` | record (max) |
| 18 | `most_parts` | Most Parts | *(empty)* | no | no | `flight.started` | record (max) |
| 19 | `biggest_stack` | Most Stages Built | *(empty)* | no | no | `flight.started` | record (max) |
| 20 | `biggest_crew` | Biggest Crew | `kittens` | no | no | `flight.started` | record (max) |
| 21 | `biggest_recovery` | Most Kittens Home At Once | `kittens` | no | no | `flight.ended` | record (max) |
| 22 | `most_stages` | Most Stages | *(empty)* | no | no | `vehicle.staging` | record (max) |
| 23 | `longest_eva` | Longest Spacewalk | `s` | no | no | `kitten.eva_end` | record (max) |
| 24 | `kitten_tumbles` | Kitten Tumbles | `tumbles` | no | no | `kitten.tumble` | count |
| 25 | `rud_total` | Rapid Unscheduled Disassemblies | `RUDs` | no | no | `vehicle.rud` | count |
| 26 | `orbits_achieved` | Orbits Achieved | `orbits` | no | no | `vehicle.orbit` | count |
| 27 | `soi_bodies` | Bodies Visited | `bodies` | no | no | `vehicle.soi` | count (set-backed) |
| 28 | `landed_bodies` | Bodies Landed On | `bodies` | no | no | `vehicle.situation` | count (set-backed) |
| 29 | `landings` | Landings | `landings` | no | no | `vehicle.landed` | count |
| 30 | `dockings` | Dockings | `dockings` | no | no | `vehicle.docked` | count |
| 31 | `stagings` | Stagings | `stagings` | no | no | `vehicle.staging` | count |
| 32 | `splashdowns` | Splashdowns | `splashdowns` | no | no | `vehicle.situation` | count |
| 33 | `evas` | Spacewalks | `EVAs` | no | no | `kitten.eva_start` | count |
| 34 | `flameouts` | Ran Dry | `flameouts` | no | no | `engine.flameout` | count |
| 35 | `engine_ignitions` | Engines Lit | `ignitions` | no | no | `engine.ignition` | count |
| 36 | `kittens_recovered` | Kittens Recovered | `kittens` | no | no | `flight.ended` | count (+crew) |
| 37 | `distance_travelled` | Distance Travelled | `m` | no | no | `roster.snapshot` | set (derived total) |
| 38 | `top_kitten_distance` | Furthest-Travelled Kitten | `m` | no | no | `roster.snapshot` | record (max) |
| 39 | `top_kitten_missions` | Most Missions Flown | `missions` | no | no | `roster.snapshot` | record (max) |
| 40 | `fastest_to_orbit` | Fastest to Orbit | `ms` | **yes** | **yes** | `vehicle.orbit` | best (min) |
| 41 | `career_playtime` | Longest Save | `ms` | no | **yes** | any event carrying `career` + `sim_t` | record (max) |
| 42 | `play_sessions` | Play Sessions | `sessions` | no | no | `session.started` | count |
| 43 | `botched_landings` | Did Not Land On Their Feet | `tumbles` | no | no | `kitten.tumble` | count (`from == "airborne"`) |
| 44 | `parts_lost` | Parts In Lost Vehicles | `parts` | no | no | `vehicle.rud` | count (+`part_count`) |
| 45 | `biggest_parts_lost` | Biggest Vehicle Lost | `parts` | no | no | `vehicle.rud` | record (max) |
| 46 | `kittens_to_orbit_and_back` | Kittens To Orbit And Home | `kittens` | no | no | `flight.ended` | count (set-backed) |
| 47 | `biggest_crew_wreck` | Most Kittens Aboard A Lost Vehicle | `kittens` | no | no | `vehicle.rud` | record (max) |
| 48 | `kittens_wrecked` | Kittens Aboard Lost Vehicles | `kittens` | no | no | `vehicle.rud` | count (+`crew_count`) |
| 49 | `bodies_by_1y` | Worlds In The First Year | `bodies` | no | no | `vehicle.soi` | set-derived best save |
| 50 | `bodies_by_10y` | Worlds In Ten Years | `bodies` | no | no | `vehicle.soi` | set-derived best save |

The final five appended constants are at `stats/boards.go:22-26`; their fixed metadata is at
`stats/boards.go:161-165`.

**Four boards carry an empty `Unit` on purpose** — `roundest_orbit` (an eccentricity is
dimensionless) and `most_parts` / `most_stages` / `biggest_stack` (bare counts of a thing the title
already names). `units.Split("")` renders the number alone, and inventing a label like `parts` would
put the word on the page twice. Two tests that asserted "every board has a unit" allow exactly these
four (`stats_test.go`, `readapi_test.go`) rather than being deleted, so a *fifth* unitless board is
still a test failure and a decision somebody has to make. `units.ForKey("stage_count")` returns `""`
by falling through, which is correct and needed no units change at all.

**Five boards have no flag exclusion, and one has it only sometimes.** For the roster and EVA
boards that is a property of the source event rather than a choice: `scoreable` passes every event
carrying no flight, and §4.1 sends `flight: null` for `roster.snapshot` and for `kitten.eva_end`.
`career_playtime` is deliberately different: a save's duration is not a feat, so a flagged flight
inside it does not erase the time for which the save was played.

| board | flag exclusion |
|---|---|
| `distance_travelled`, `top_kitten_distance`, `top_kitten_missions` | **none** — `roster.snapshot` carries no flight |
| `longest_eva` | **none** — `kitten.eva_end` is `flight: null`, asymmetrically with `kitten.eva_start` |
| `career_playtime` | **none** — a duration is not a feat; it records the positive career clock directly |
| `evas` | **only when the EVA signal carried a vehicle id** (`kitten.eva_start`) |

### The three dynamic families

`stats/boards.go`. **There is no allow-list.** A key exists because a name appeared in the
data. All three families have the same player, career and system scopes as every fixed board; a dynamic
key receives all three on the event that creates it, with no registration step.

| prefix | listed under | Title | Unit | Asc | Career |
|---|---|---|---|---|---|
| `tumbles_on_` | `kitten_tumbles` | `"Tumbles on " + titleize(body)` | `tumbles` | no | no |
| `rud_` | `rud_total` | `"RUDs — " + titleize(cause)` | `RUDs` | no | no |
| `fastest_to_` | `fastest_to_orbit` | `"Fastest to " + titleize(body)` | `ms` | **yes** | **yes** |

Key construction: `TumblesOnStat(body)` (`stats/boards.go:246-248`) / `FastestToStat(body)` /
`RUDStat(cause)` → `familyStat(prefix, value)`.
`statSuffix` (`:241-256`) lowercases, then requires `[a-z0-9]` first and `[a-z0-9._-]` thereafter,
length ≤ `MaxStatSuffixLen = 40`. A key that would collide with a **fixed** key is refused
— a body literally named `orbit` cannot land on `fastest_to_orbit`. **A rejected name keeps every
other consequence**: it still counts towards `kitten_tumbles`, `soi_bodies` or `rud_total`, as
applicable, and an SOI arrival still records `player_body.first_sim_t`.

`titleize` (`:263-276`) splits on `_ - .` and capitalises each word — derived, never a lookup table
(PROJ-036). `Describe(stat)` (`:284-302`) is a **pure function of the key**. `Known(stat, players)`
(`:312-321`): a fixed board is always servable; a family board is servable once `players > 0`.
`Catalog(counts, minPlayers)` (`:335-367`) lists fixed boards always, in table order, and family
members with `count >= minPlayers` sorted by key, inserted directly under their parent.
`DefaultMinPlayers = 2`, config key `[boards] min_players`.

### Fold detail, board by board

**The two impact boards** — `biggest_lithobrake_survived` (`lithobrakeFold`, `:416-432`) and
`biggest_impact_energy` (`impactEnergyFold`, `:442-456`). One crash, two readings, **one shared
eligibility** in `survivedImpact` (`:389-403`) because they must agree about which crashes count:
`Survived && !LaunchPad && CrewCount >= 1`, then `scoreable`, then — **rebuild only** — no
`kitten.kia` for the same flight within ±2.0 s of `ev.SimTime` (`KIAWindowSeconds = 2.0`,
`fold.go:61`). Each board additionally gates its own value `> 0`. Values are raw `speed_ms` (m/s) and
raw `energy_j` (J), no conversion, rounding or clamping. One shared context
`{"body", "flight", "speed_ms", "energy_j"}` (`impactContext`, `:407-414`).

They are not duplicates: `speed_ms` is a closing *normal* speed for a ground impact and a
reconstructed `√(2E/m)` scalar for a splash, while `energy_j` is the game's own
`ImpactKineticEnergy` — so the energy board ranks a heavy lander touching down hard above a light
probe hitting fast, and the speed board does the opposite.

**The two structural-load boards** — `peak_g_survived` (`peakGFold`, `:501-517`) and `max_q_survived`
(`maxQFold`, `:519-541`). Both read a **`*float64`** off `telemetry.window` (`stats/payload.go:209-210`)
— absent ≠ zero — and both go through `survivedLoad` (`:474-489`): non-nil and `> 0`, flight
unflagged, and **rebuild only** `st.Recovered()` i.e. `flight_state.ended_reason == "recovered"`.
Values `*p.PeakG` in `g` and `*p.MaxQPa` in `Pa`. Context `{"body", "flight", "t1_sim"}`; `t1_sim` is
**seconds**. Peak g is how hard the airframe was squeezed, max q is how hard the air was pushing, and
an ascent profile can be brutal on one and gentle on the other.

**`fastest_surface_speed` / `fastest_orbital_speed`** — `speedFold{stat, surface}`, `:543-573`,
registered twice. Reads `telemetry.window.surface_speed_ms.max` or `orbital_speed_ms.max`.
Eligibility: `value > 0`, flight unflagged. No rebuild refinement. Context
`{"body", "flight", "t1_sim"}`. **Explicit anti-pattern**: these must **never** be sourced from
`roster.snapshot.fastest_ms`, which is the game's ecliptic-frame `FastestSpeed` and reads ~30 km/s
standing still on Earth (`:546-548`).

**`highest_altitude`** — `altitudeFold`, `:575-599`. Max `alt_m.max`, gated `> 0`, flight unflagged.
It takes the plain flag exclusion rather than `survivedLoad`'s recovered-flight rule, and the
distinction is the point: a structural-load reading is only written under full physics, whereas an
altitude is a position — always sampled, always meaningful — and a probe that never came back still
got there. Barometric, not radar (`PositionCci.Length() - Parent.MeanRadius`), so a mountaintop
landing scores its elevation and a low pass over a canyon does not.

**`lowest_pass`** — `lowestPassFold`. `putBest` (min) of `telemetry.window.radar_alt_m.min`. The
counterpart of `highest_altitude` and deliberately the *other* altitude: `alt_m` is barometric —
above the parent's mean radius — so a low pass down a canyon reads as *high* and a mountaintop hover
reads as *low*. This is the terrain-relative reading, so it is not `highest_altitude` inverted.

Two gates, both load-bearing. **An absent aggregate never scores**: a window spent in orbit has no
terrain below it and the mod omits the key entirely rather than folding zeros, so the fold refuses
`nil` before it looks at a number. And the minimum must be **strictly positive**, because this board
is ascending and 0 is exactly what a vehicle sitting on the ground reads — an unbeatable record every
flight would tie on its way to the pad (PROJ-088). **A landing is not a pass**; `softest_landing` is
the board for arriving.

Refinement: none. Like `highest_altitude` and unlike `peak_g_survived` / `max_q_survived`, it takes
the plain flag exclusion rather than `survivedLoad`'s recovered-flight rule — a position is always
sampled, and a probe that never came back still flew that low. Context `windowContext`.

**`fastest_entry`** — `entryFold`, `:601-629`. Max `speed_ms` of a `vehicle.atmosphere` with
`dir == "entered"`, gated `> 0`, flight unflagged. `exited` is ignored on purpose: leaving an
atmosphere fast is an ascent the speed boards already rank. Context
`{"body", "flight", "dyn_pressure_pa"}`.

**The four orbit-shape boards** — `orbitRecordFold`, `:631-686`, registered four times over
`vehicle.orbit` with `phase == "achieved"`: `highest_apoapsis` (`ap_m`, max), `lowest_orbit` (`pe_m`,
**min**), `roundest_orbit` (`ecc`, **min**) and `steepest_orbit` (`inc_deg`, max). All four gate
`value > 0`, all four share the context `{"body", "flight", "ap_m", "pe_m", "ecc", "inc_deg"}`.

The `> 0` gate is doing real work rather than being defensive. `ap_m` is written 0.0 whenever the
conic is not `Bound`; an `ecc` or `inc_deg` of exactly 0 is what a failed or unwritten read leaves
behind; `pe_m` is computed unconditionally and is legitimately negative for a periapsis underground.
On the two **ascending** boards a zero is not merely wrong, it is an *unbeatable record nobody flew* —
which is why `roundest_orbit` must refuse a perfectly circular-looking 0 rather than crown it.

**`heaviest_to_orbit`** — `orbitMassFold`. `putRecord` of `vehicle.orbit.mass_kg`, gated
`phase == "achieved"` and `mass_kg > 0`. The heaviest thing a player has ever put into a stable orbit
around anything; `escaped` is excluded exactly as it is on the four shape boards, because an escape
is not an orbit anybody reached.

**Not `heaviest_launch` twice, and the pair is the point.** What left the pad includes the propellant
that will be spent getting off it; what is still there when the milestone fires is the payload.
Together the two are the only honest efficiency-shaped number reachable **without reading
propellant** — which is why the mod put `mass_kg` on `vehicle.orbit` rather than letting a reader diff
a launch mass against a telemetry window that may be half a window stale. PROJ-093.

**Zero rule.** `mass_kg` is written as 0 when the read failed, and 0 kg is not a payload, so the
board gates on `> 0` exactly as the four launch boards do — a gate on the value rather than on the
envelope, which is what keeps every board in `boards.go` reading the same way (PROJ-094). Context `{"body", "flight", "ap_m", "pe_m"}` — not
the shared four-board shape blob, because this board ranks the *vehicle* and the apsides are what say
where it got to.

**`softest_touchdown`** — `touchdownFold`, `:820-857`, a `putBest` (min) of
`vehicle.situation.surface_speed_ms`, gated `> 0`. Two conditions make it a landing rather than a
bump: `to` must have surface contact, and `from` must be a **known** contact-free situation
(`freefall` or `maneuvering`). Known matters separately from contact-free: `"unknown"` also reports
no contact, and a touchdown measured from a state nobody could read is not a measurement — on an
ascending board it would be a record. It also excludes the transitions that would otherwise dominate:
`rolling` → `landed` as a rover stops, `landed` → `dragging` on a slope. Context
`{"body", "flight", "from", "to", "altitude_m"}`.

**`softest_landing` and `landings`** — `softestLandingFold` and `landingsFold`, over
`vehicle.landed`, with **one shared eligibility** in `survivedLanding`: a board about touching down
gently and a board about touching down at all must agree about which arrivals happened. It is
`survived`, then `scoreable`, and nothing else.

`survived` is the mod's answer and the only one taken — it has been through the same one-full-frame
destruction hold as `vehicle.impact.survived`, so a touchdown the vehicle did not walk away from is a
crash and `vehicle.rud` / `biggest_impact_energy` are where a crash belongs. Unlike `survivedImpact`
there is **no crew requirement and no ±2 s KIA window**: those exist because D11's rule is about a
*crew* surviving a lithobrake, and landing a probe is landing (`crew_count` rides in the context for
a reader who cares). Unlike `survivedLoad` there is **no rebuild-only refinement**, so both boards
fold identically incrementally and on rebuild.

- `softest_landing` is a `putBest` (min) of `vertical_speed_ms`, which is positive downwards, so
  smaller is softer. It must therefore refuse an exact 0 — what an unreadable state-vector
  decomposition leaves behind, and an unbeatable record on an ascending board (PROJ-088). A genuine
  touchdown is never exactly 0 m/s: the detector samples at 2 Hz and the vehicle is still settling.
  Context `{"body", "flight", "horizontal_speed_ms", "crew_count"}`.
  **Not `softest_touchdown` twice**: that board ranks `surface_speed_ms`, the *whole* velocity
  relative to the ground. A rover arriving at 8 m/s across a plain and a lander arriving at 8 m/s
  straight down are the same number there and very different flying; this one is the vertical
  component alone, which is the one a pilot is actually managing.
- `landings` is `addCount(1)` with **no speed gate** — a landing at any rate is a landing. It is
  deliberately **not** a `countFold`: that type counts every event of a type, and this one owes the
  same `survived` gate its sibling takes. It counts landings, not worlds; `landed_bodies` is the
  set-backed board for "how many worlds", it still reads `vehicle.situation`, and the two never
  double-count (PROJ-097).

**The four launch boards** — `launchFold{stat, value}`, registered four times over `flight.started`:
`heaviest_launch` (`mass_kg`), `most_parts` (`part_count`), `biggest_crew` (`crew_count`) and
`biggest_stack` (`stage_count`). All gated `> 0`, which for the three integer fields **is** §4.2's
`>= 1`; all four fields are written as 0 rather than omitted when the read failed, so a zero is an
unreadable vehicle rather than an empty one. One shared context, now six keys —
`{"body", "flight", "vehicle", "mass_kg", "part_count", "crew_count", "stage_count"}`.

`biggest_stack` is where the gate earns its keep: `stage_count` is the highest-risk KSA read of the
four (it walks `Vehicle.Parts.SequenceList`, which was nearly rewritten in 5168) and a `ver` 1 row
carries no stage count at all. Both fall out through the same door. **It is not `most_stages`**:
that board is the highest `stage_index + 1` a vehicle ever *fired*, off `vehicle.staging`; this one
is how many were *built*, off the pad. A five-stage rocket that RUDs on stage two scores 5 here and 2
there.

**`biggest_recovery`** — `recoveryFold`, `:726-754`. `flight.ended` with `reason == "recovered" &&
crew_count >= 1`, `putRecord` of `crew_count`. The counterpart of `kittens_recovered`, which sums:
forty solo recoveries and one nine-seat crew return are equal there and very different here. Its
established `body` context still comes from `flight_state` (`flightBody`) even though the payload now
carries one — free, because `scoreable` has already cached that row.

**`kittens_to_orbit_and_back`** — `kittensToOrbitFold` (`boards.go:1351-1411`; registered at
`fold.go:143`) reads `flight.ended` only when
`reason == "recovered"` and the ordered `kids` list is nonempty, applies `scoreable`, then requires
the joined `flight_state` to carry `MilestoneOrbit` and
`0 < first_orbit_seq < flight.ended.seq`. The sequence comparison prevents a rebuild's completed
first pass from treating an orbit recorded only after recovery as a prior achievement. Each
recovery-time kitten is inserted as a
save-local `career_body(kind='orbit_kid', body=kid)` member. A repeated `(career, kid)` is a no-op;
the same kid label in another save is a second member. On every new member the fold independently
recomputes the player total across all `(career, kid)` rows, that save's total, and — only when the
career has a known system — the total across that player's saves in the same system. It writes the
three derived values with `setValue`, `setCareerValue` and `setSystemValue`; every row context is
SQL NULL. The save-local insert is `AddCareerSetMember` (`batch.go:795-819`); its three count reads
are `CareerBodyCount`, `CareerSetCount` and `SystemCareerSetCount` (`batch.go:843-895`).

The fold iterates `kids` in wire order, never through a map. Current contexts are empty and the
final counts are set-valued, but preserving insertion order keeps replay deterministic and prevents
future provenance from acquiring Go-map order. “Back” means a KSA recovery: the game permits that
only on the system's home body, at rest and in contact. The crew list at recovery is authoritative,
so a kitten who boarded after orbit counts and one who transferred away before recovery does not.

**`most_stages`** — `stagesFold`, `:756-785`. `putRecord` of **`stage_index + 1`**: the index is
zero-based and read in the postfix, so `+1` is "how many stages have fired". **No `> 0` gate**, for
that same reason — firing stage 0 is one staging event and one stage. `body` from `flight_state`.

**`longest_eva`** — `evaDurationFold`, `:787-818`. Max `kitten.eva_end.duration_s` in seconds, gated
**`> 0`** because 0.0 is what an unreadable `LaunchGameTime` leaves behind and is indistinguishable on
the wire from an EVA that ended in the frame it began. The event is `flight: null`, so there is
nothing for the flag exclusion to check and nothing to name: context is `{"kitten"}`, plus a `flight`
key only if a future build starts attributing the event.

**The lost-vehicle size and occupancy boards** — `parts_lost`, `biggest_parts_lost`,
`biggest_crew_wreck` and `kittens_wrecked` share `rudPartsFold` (`boards.go:1128-1183`) and its one
`scoreable` result. `part_count <= 0` moves neither part board: zero is the intact-vehicle
read-failure fallback. A positive value is added in full to `parts_lost`, whose
context is SQL NULL, and offered unchanged to max-record `biggest_parts_lost`, whose byte-stable
context is `{"body", "cause", "flight"}`. `addCount` and `putRecord` fan the same contribution into
player, career and known-system scopes. The counter tie is whoever reached the total first; the
record changes only on a strictly larger vehicle and an equal size keeps the earlier claimant.

Independently, `crew_count < 1` moves neither crew board. A positive count is added in full to
`kittens_wrecked`, whose context is SQL NULL, and offered unchanged to max-record
`biggest_crew_wreck`, whose context is the same exact `{"body", "cause", "flight"}` object as the
largest-vehicle record. This counts kittens aboard a whole vehicle at its physics RUD. It does not
claim they died, count individual bodies, or classify the loss as a crash or explosion. The normal
physics path ends their missions; a player scuttle is the separate `KillCrew` / `kitten.kia` path.

**The counter boards** — `kitten_tumbles`, `botched_landings`, `tumbles_on_<body>`, `parts_lost`,
`kittens_wrecked`, `dockings`, `stagings`, `evas`, `flameouts`, `engine_ignitions`,
`orbits_achieved`, `rud_total`, `rud_<cause>`, `kittens_recovered`, `soi_bodies`, `landed_bodies`,
`landings`, `splashdowns` — all use
`addCount`, whose `context` argument is `nil`, so
`player_stat.context` is SQL NULL and `BoardRow.Context` is omitted from JSON. Their `updated_seq`
becomes the seq at which the counter reached its current value, so the tie-break is *whoever got to N
first*.

The five additional orbital elements (`sma_m`, `lan_deg`, `argp_deg`, `t_pe`, `period_s`) are not
read by `orbitRecordFold`, `orbitMassFold`, `orbitsFold` or `toOrbitFold`, and are not copied into
their contexts. They are recorded facts only; every orbit board above keeps the same field,
eligibility and tie rule.

- `kitten_tumbles`, `botched_landings`, `tumbles_on_<body>`: `tumbleFold` (`:1079-1115`, stable name
  `tumble_split`) decodes the payload once. The total does not inspect `from`: failed landings,
  grounded trips and repeated bounce edges all still increment it. Only exact `"airborne"`
  increments `botched_landings`; the open set is
  otherwise opaque. Every valid body key increments its family board regardless of `from`, while an
  unkeyable body still increments the total and, when airborne, `botched_landings`.
- `dockings`, `stagings`, `evas`, `flameouts`, `engine_ignitions`: `countFold` on the event type.
  `engine.shutdown` counts nothing — a shutdown is the unremarkable other half of every burn, and
  counting it would be counting `engine_ignitions` twice with a lag.
- `orbits_achieved` (`:910-925`): `vehicle.orbit` with `phase == "achieved"` only; `escaped` counts
  nothing.
- `rud_total`, `rud_<cause>`, `parts_lost`, `biggest_parts_lost`, `biggest_crew_wreck`,
  `kittens_wrecked`: `rudPartsFold` (stable name `rud_parts_crew`) applies `scoreable` once; see the
  lost-vehicle detail above.
- `kittens_recovered` (`:1010-1026`): `flight.ended` with `reason == "recovered" && crew_count >= 1`
  → `addCount(+float64(CrewCount))`. **It adds the crew count, not 1.**
- `soi_bodies` (`:927-950`) and `landed_bodies` (`:952-982`): `b.AddBody(...)` reports whether the
  lifetime `player_body` row was new; `b.AddCareerBody(...)` independently reports whether the
  save-local `career_body` row was new. Player scope is the lifetime union, career scope is one
  save's set, and system scope is the union across that player's saves with the same system
  identity. The separate tables keep the two novelty signals independent and replay-stable
  (PROJ-011 / PROJ-106).
  `landed_bodies` writes `kind = 'landed'` and counts **any** surface contact — terrain, ocean or
  both — because splashing down on a body is arriving at it. It **stays on `vehicle.situation`** now
  that `vehicle.landed` exists (PROJ-097): the landing fires only on the contact-free → contact edge,
  while this board asks whether the player has anything *on* a surface, so a vehicle already on the
  ground when a save loads and a rover going `rolling` → `landed` both reach it through the situation
  and through nothing else. Moving the source would also empty every existing row on the next
  rebuild. `landings` and `softest_landing` never touch `player_body`, so `landedBodiesFold` remains
  the sole writer of `kind = 'landed'` and a touchdown advances the counter through exactly one path.
- `splashdowns` (`:984-1008`): `to` must be **pure** ocean contact (`sailing` / `floating`), and
  `from` must be contact-free. `dragging` and `bottomed` touch terrain as well and are a hull on a
  shoreline. The `from` gate is what makes this an *arrival*: without it a boat crossing the
  `sailing` ↔ `floating` boundary as it goes on and off rails would count a splashdown forever, and
  the 2 s situation debounce only rate-limits that rather than stopping it.

**`distance_travelled`** — `distanceFold`, `:1028-1092`, the only `kindSet` board. Player value =
`Σ career_kitten.travelled_m` across all of the player's saves; career value sums one save, and
system value sums all saves carrying that system identity. This deliberately counts two different
cats with the same roster name in two saves twice, rather than collapsing them through the
career-free `kid` and keeping only the larger total. It is written when `> 0`; unit `m`, SI-scaled at
render (`1.82 Mm`). `setValue` reads the previous value first so the window contribution is the
**increase**. The `kindSet` guard
`WHERE excluded.value <> player_stat.value` means a recomputation of the same total leaves
`updated_seq` alone.

**`top_kitten_distance` / `top_kitten_missions`** — folded by the same `distanceFold`, because it is
the only fold that writes the kitten projections. `Batch.KittenTops` returns the lifetime leader of
each column; `CareerKittenTops` and `SystemKittenTops` compute their own scoped candidates rather
than copying that winner. Each board is gated `> 0`, context `{"kitten"}`. Lifetime and career ties
are broken on `kid`, and system ties on `(career, kid)`, rather than left to Go's randomised map
order: the winner's *name* lands in the context and a rebuild has to reproduce the incremental bytes
exactly. **All three of these boards, and `longest_eva`, have no flag exclusion** — their source
events carry no flight. That is not fixable here; the fix would be the mod attributing roster totals
to the flights that earned them.

**The career-time boards** — `fastest_to_orbit` and the `fastest_to_<body>` family. The shared rule
(`:1109-1134`) is *the smallest `sim_t` at which an unflagged flight of this player reached the
milestone*, taken **per player, not per career**. `careerTime` (`:1136-1147`) requires all of:
`ev.HasCareer()`; `ev.HasSimTime && ev.SimTime >= 0` (absent is not zero); and `scoreable`.

`careerMillis(seconds) = seconds * 1000` (`:1158`). **This is the other half of the `_ms` trap**: the
board *unit string* `"ms"` is milliseconds, whereas a payload key ending `_ms` is metres per second
(`units/units.go:424-432`). `sim_t` stays **seconds** on the wire and in `player_body.first_sim_t`;
only the projection value is converted (PROJ-029 / PROJ-047).

**The two world-sprint boards** — `bodies_by_1y` and `bodies_by_10y` are written together by
`bodySprintFold` (`boards.go:1739-1791`, stable name `body_sprints`). They use
`SprintYearSeconds = 365 * 24 * 3600` (`:67-69`): exactly 31,536,000 seconds and 315,360,000 seconds.
Both boundaries are inclusive. This “year” is catlog's flat duration unit, matching the duration
ladder in `server/internal/units`; it is not any celestial body's orbital period and requires no
game or system-catalogue read.

The fold runs on every `vehicle.soi` with a nonempty destination, career, nonnegative `sim_t` and a
scoreable flight. It does not wait for a new set member: `toBodyFold` may lower an existing
`career_body.first_sim_t` after a rewind, and that lower time can move a body across a threshold.
`CareerBodyCountBefore` counts distinct qualifying bodies in the current save;
`BodyCountBefore` takes the maximum of those per-save counts for player scope; and
`SystemBodyCountBefore` takes the maximum one-save count among that player's saves bound to the
current system (`batch.go:857-917`). Neither broader scope unions early bodies from different saves.
The three values are computed independently through `setCareerValue`, `setValue` and, only when the
system is known, `setSystemValue`; all contexts are SQL NULL. An unknown system therefore omits only
the system row while the save and player results still move. The `kindSet` merge updates a row only
when its value changes, so an equal best from another save preserves the earlier `updated_seq`.

### The rewind mark

Written by `careerFold` into `career.rewound`, **never** into `player_stat.context` (PROJ-026).
Resolved at read time by `RewoundCareers(playerID, careers)` — one query per distinct player on a
board page, and none at all for a board whose `Board.Career` is false. Surfaces as
`BoardRow.Rewound` / `PlayerRow.Rewound` / `CompareRow.Rewound`, emitted only when true. The `career`
value used for the join is read from the row's own context *before* redaction, and the context is
then relabelled per player by `Redact`. **The mark excludes nothing and scores nothing.**

### Number formatting

`server/internal/units` is **the single definition of a formatted catlog number**, and `Conformance`
/ `LabelConformance` (`units.go:468-572`) are the tables that pin it. A rule change is two edits in
one commit: the rule in `units.go`, and the row in `Conformance` that fixes its output. The read API
publishes raw numbers, so anything rendering them elsewhere — a community dashboard over
`GET /v1/…` — reproduces these rules from those tables rather than from prose.

- Three significant figures: `decimals = clamp(2 - floor(log10|x|), 0, 6)`; round on the magnitude,
  re-apply sign, trim trailing zeros, group in threes with a canonical `,`.
- `m`, `J`, `Pa` scale by SI prefix; no sub-unit prefixes. **`m/s` never scales.**
- `s` and `ms` render as a **duration** — `ms` divides by 1000 first, then the ladder
  (`450 ms` / `37.5 s` / `5m 13s` / `1h 01m` / `243d 01h` / `1y 5d`; a year is 365 days flat).
- Any other unit (including `g` and every counter label) is three-sig-figs + `" " + unit`.
- `ForKey` (`:433-459`) is the only thing that knows `_ms` = m/s while the board unit `"ms"` =
  milliseconds. Longest suffix first, so `_ms2` is not read as `_ms`. Exact keys `sim_t` / `t0_sim` /
  `t1_sim` → seconds; `ecc`, `n`, `part_count`, `crew_count`, `missions`, `stage_index` → unitless.
  Every context key the boards emit already resolves — `ap_m` / `pe_m` / `altitude_m` → `m`,
  `inc_deg` → `deg`, `dyn_pressure_pa` → `Pa`, `duration_s` → `s`, `mass_kg` → `kg`, and
  `vehicle` / `kitten` / `from` / `to` → unitless by falling off the end.

**catlog has exactly one two-sided table, and it is edited in pairs.** A row changed on one side and
not the other is a silent divergence between two implementations that are supposed to agree, so it
has no "later".

| Table | Sides | What disagreement would do |
|---|---|---|
| Situation → surface contact | `server/internal/stats/situation.go` ↔ `mod/catlog.lib/Telemetry/SituationInfo.cs` | a board lands on one side of a landing and not the other — `landed_bodies`, `splashdowns` and `softest_touchdown` all decide what a transition *means* from this table |

The server's copy carries the contact column only; the on-rails bit is not ported because no board
reads it. Both copies are total by construction — an unknown name reports no contact rather than
guessing — which is what keeps a ninth situation from a future build off a board instead of onto
one.

### Read paths

| Endpoint | Handler | Shape |
|---|---|---|
| `GET /v1/leaderboards` | `readapi.go:223` → `query.go:82 BoardList` | `{boards:[{stat,title,unit,ascending,count,periods}], min_players}`. `count` is the **unfiltered** row count, banned players included (PROJ-008). |
| `GET /v1/leaderboards/{stat}` | `readapi.go:271` → `query.go:117 Board` | `{stat,title,unit,ascending,period,bucket?,limit,offset,rows[]}`; rows `{rank,handle,value,context?,updated,rewound?}`. 404 when the key is neither fixed nor a family board anybody holds. |
| `GET /v1/players/{handle}` | `readapi.go:425` → `query.go:262` | Every row the player holds, listed board or not. A stat this build cannot `Describe` is dropped. Unknown / retired / banned handles are one 404 — deliberately not a ban oracle (PROJ-007). |
| `GET /v1/compare?handles=` | `compare.go:109` | N ≤ 8 profiles pivoted board-first, display order from `Catalog(counts, 1)`. An absent player is absent, not zero. |
| `GET /v1/players?q=` | `search.go:70` | Scans the **in-memory directory only** — no database at all. |
| `GET /v1/feed`, `/v1/feed/stream`, `/v1/feed/sse` | `readapi/feed.go`, `web` | see [`feed`](#feed) |
| `GET /v1/stats` | `stats.go:182` | see [`event_census`](#event_census) |

Ordering on both board tables: value (direction from `Board.Ascending`), then `updated_seq ASC`, then
`player_id ASC`. Ranks are **positional over visible rows** (`Rank: offset + i + 1`). Banned and
handle-less players are dropped in Go by over-fetch-and-drop (`readapi.go:306 visibleRows`, scan
bounded by `maxScan = 5000`), so **a ban closes the gap rather than leaving a hole**. Profile rank is
`ahead - hiddenAhead + 1`, re-applying the same `better || (equal && earlier seq)` comparison in Go so
a profile can never contradict a board page. Paging: `limit ∈ [1,200]` (default 50), `offset ≥ 0`;
an over-large limit is clamped, not rejected (PROJ-009).

Every public read response carries `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`
— including 400s and 404s.

The server-rendered site (`server/internal/web/pages.go`) calls `readapi`'s exported query methods
directly, precisely so there is no second place a banned player can reach a public surface.

---

## State projections

### `system`

`system(hash PRIMARY KEY, system_id, name, slug UNIQUE, home_body, body_count,
reported_complete, first_seq)` (migration 0008). It is created only by `system.discovered`; an
orphan `system.body` never creates a placeholder whose unknown name would force a mutable slug.

Identity is immutable first-write. On a repeated header, `system_id`, `name`, `home_body` and
`body_count` are compared. A difference retains the original row and logs a structured warning with
the hash, current seq and first seq; an exact match may only perform
`reported_complete = reported_complete OR incoming_complete`. Thus false→true supports a player
enabling body reporting later, while a routine later false header cannot erase a catalogue already
received. Neither path rewrites `slug` or `first_seq`. These comparisons protect deterministic
projection state; they are not client plausibility checks and exclude nobody.

The public/consumer meaning of complete is never the header bit alone:

```text
effective_complete = reported_complete
                     AND count(system_body WHERE hash = system.hash) == body_count
```

That count is what prevents an interrupted 3,215-row catalogue from looking complete before its
last row. A zero body count can be effectively complete only when it was actually reported complete
and has zero rows; false always means unknown rather than empty.

`slug` is assigned from the sanitised display name with a dedicated, deterministic ASCII rule:
lowercase `A-Z`, retain `a-z0-9`, map each run of every other byte to one `-`, trim hyphens and cap
the base at 48 bytes. Empty falls back to the first eight hash characters. Different hashes that
collide receive `-2`, `-3`, … in ascending `first_seq` order, so rebuild and every projector batch
size agree. This does not reuse or weaken `statSuffix`: system display names may contain spaces and parentheses,
while stat keys remain protocol identifiers.

### `system_body`

`system_body(hash, body, name, class, kind, rank, parent, radius_m, mass_kg, soi_m, atmo_m,
ocean_m, angvel, axis_x, axis_y, axis_z, sma_m, ecc, inc_deg, lan_deg, argp_deg, t_pe,
period_s, ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq,
PRIMARY KEY(hash, body))` (migration 0008), with `(hash, kind, body)` index.

Rows are immutable first-write via `INSERT … ON CONFLICT DO NOTHING`; a differing duplicate keeps
the original. `class` is the game's opaque word and has **no allow-list**. The six orbital-shape
columns are null as a group on a root or invalid group; `period_s` is independently nullable;
`parent` is null for every root. Physical/orientation values are stored exactly as reported and no
orbital value is derived from another.

There is intentionally no foreign key to `system`. A body carries its own hash and may cross a
projector batch boundary before its header; `systemFold` stores it without inventing header fields.
List/detail readers hide such orphan rows until `system.discovered` creates the real system.

### `flight_state`

`0001_init.sql:34-39` creates `flight_state(flight_id BLOB PK, player_id, flags INTEGER DEFAULT 0,
ended_reason, crew, body, started_seq)`. Migration `0009_flight_engine_count.sql` adds nullable
`engine_count INTEGER`: SQL `NULL` means `flight.started` was not folded or its game read failed;
present 0 means no rocket engine was installed when the flight began. Migration
`0010_flight_facts.sql` then adds `milestones INTEGER NOT NULL DEFAULT 0`, nullable
`part_count INTEGER`, nullable `launch_mass_kg REAL`, and `career TEXT NOT NULL DEFAULT ''`.
Migration 0010 does not re-add `engine_count`; 0009 remains its sole owner.
Migration `0012_flight_orbit_seq.sql` adds `first_orbit_seq INTEGER NOT NULL DEFAULT 0`, the earliest
positive sequence of an achieved-orbit event on the flight.

Flag bits (`stats/flight.go:12-30`): 0 `teleport`, 1 `refuel`, 2 `resource_edit`, 3 `console`,
4 `tuning`, **5 `other`** for an unrecognised value (PROJ-002).

`flightFold` (`stats/flight.go:114-182`) runs between `systemFold` and `careerFold` in
`StateFolds`. **Every** flight-bearing event
creates the row (`EnsureFlight`), not only `flight.started` — a batch may fold `flight.flagged` before the
`flight.started` it belongs to. The row retains the first nonempty event career and never replaces it
with empty. On `flight.started`, `StartFlight` writes crew, body, `started_seq`, nullable
`engine_count`, nullable persisted `part_count`, and nullable persisted `launch_mass_kg`; in the
current v1 payload the latter two are present even when their numeric failed-read fallback is 0.

Milestone bits are achievements, not exclusions: 0 orbit achieved (`vehicle.orbit phase=achieved`),
1 space (`vehicle.atmosphere dir=exited`), 2 other SOI, 3 survived landing, 4 docked. Every write is
`milestones |= bit`; no path clears one. Other SOI alone requires a known launch body from an actual
start no later than the SOI event and a distinct nonempty destination. If the SOI was early, the bit
is deliberately never retro-awarded. Orbit remains a raw set-only milestone even when its event was
early. The orbit fold also lowers `first_orbit_seq` to the earliest achieved-orbit sequence, so a
composite can distinguish a prior orbit from one the rebuild first pass learned from the future.
`kittens_to_orbit_and_back` reads the orbit state, as do F5's active orbit composites.

`FlightState.HasStartFactAt(candidateSeq, factValid)` is that normative join predicate:
`StartedSeq > 0 && StartedSeq <= candidateSeq && factValid`. A rebuild's first pass may know a later
start, but a composite candidate is refused when the incremental projector could not have known its
required fact at that sequence. The set-only folds and this conservative comparison make replay
deterministic without fabricating historical knowledge.

The in-memory flight-id map is a read-through/write-back cache for all thirteen columns; the key is
the flight id and `flightEntry` carries the other twelve. Its SQL `SELECT`, `FlightState` conversion
and sorted multi-row flush preserve the exact order through
`engine_count, milestones, part_count, launch_mass_kg, career, first_orbit_seq`; the flush uses 13 placeholders and
updates every mutable column on conflict. Pending reads therefore see facts and ORed milestones from
earlier events in the same batch before anything reaches SQL.

Consumers: `scoreable` (`stats/fold.go:226-241`) — events with **no flight** are scoreable, a missing
row is treated as unflagged, otherwise `st.Flags == 0`; `Recovered()` for the rebuild refinement on
`peak_g_survived` and `max_q_survived`; `Body` for the record boards whose own payload carries none
(`flightBody` — `biggest_recovery`, `most_stages`); `Projections.FlaggedFlights` so the raw-event read
views drop every row belonging to a flagged flight (PROJ-051).

### `career`

`career(player_id, career TEXT, max_sim_t REAL DEFAULT 0, rewound INTEGER DEFAULT 0, first_seq,
ordinal, last_seq, system, system_changed, PRIMARY KEY(player_id, career))`. `careerFold`
(`stats/career.go:60-81`) — no `career` key → nothing; `career` but no `sim_t` → `EnsureCareer` only;
`session.started` → `MarkRewound` **before** advancing, and only when the career already exists and
`max_sim_t > sim_t`; then `AdvanceCareer` raises the high-water mark.

`systemFold` runs before `careerFold` and binds `career.system` from the first
`system.discovered`, once. A later discovery for the same career with a different hash leaves that
first system in place and sets `system_changed = 1`. Like `rewound`, the mark is **non-punitive**:
it excludes nothing, changes no score and makes no claim about intent. It only qualifies a
system-scoped comparison when the content definition changed under one save.

### `player_body`

`player_body(player_id, kind, body, first_seq, first_sim_t, PRIMARY KEY(player_id, kind, body))`.

**`kind` has two values.** `'soi'` (written by `soiFold` from `vehicle.soi`) and `'landed'` (written
by `landedBodiesFold` from `vehicle.situation`, any surface contact). The `(player_id, kind, body)`
primary key already allowed for a second kind, so this needed no migration. Only `'soi'` rows are
ever passed to `LowerBodyTime`, so a `'landed'` row's `first_sim_t` is **always NULL** — there is no
`fastest_landing_on_<body>` family and none is proposed.

`first_sim_t` is **seconds**, NULL when the arrival event carried no career or no clock. Recorded for
**every** body, including ones with no board of their own. Read surface is aggregate counts only —
there is no per-body endpoint; the `fastest_to_<body>` board is the readable form, in ms. The admin
`SELECT count(DISTINCT body) FROM player_body` is unaffected in practice, since landing on a body
implies having entered its SOI.

### `career_body`

`career_body(player_id, career, system, kind, body, first_seq, first_sim_t,
PRIMARY KEY(player_id, career, kind, body))`. It is a save-local set table. **`kind` has three
implemented values:** `'soi'` and `'landed'` store a celestial body from qualifying
`vehicle.soi` / `vehicle.situation`; `'orbit_kid'` stores a recovered kitten id in the existing
`body` column. That name now reads generically as “the set member”; renaming it would change no
semantics and require a needless schema migration.

`'orbit_kid'` is deliberately **not** written to `player_body`. Kitten ids omit career, so identical
roster names in two saves must remain two `(career, kid)` members rather than collapse into one
lifetime row. `system` is denormalised from the career. An unknown system still permits the
player-wide and save-local counts; only the system-scoped board write is absent. For an
`'orbit_kid'` member, `first_seq` is the recovery event that first added it and `first_sim_t` remains
NULL.

For `'soi'`, `soiFold` still uses the known-system `AddCareerBody` path for the scoped
`soi_bodies` set. Separately, `toBodyFold` ensures a save-local timed member through
`AddCareerSetMember`, so an unknown system does not erase a meaningful save/player sprint arrival;
only `bodies_by_1y` / `bodies_by_10y` system scope is absent.

Its primary key makes novelty local to one save. `soi_bodies` and `landed_bodies` therefore advance
independently at player and career scope, while system scope counts `DISTINCT body` across all
matching career rows. `first_sim_t` has the same seconds/NULL meaning as on `player_body`, and is
lowered only for `'soi'` rows so `fastest_to_<body>` can use the correct per-save arrival.
The same save-local arrival times feed `bodies_by_1y` and `bodies_by_10y`: each career counts its
distinct bodies at or before the exact threshold, while player and system scope keep the maximum
one-career result rather than unioning bodies across careers.
`kittens_to_orbit_and_back` instead counts distinct `'orbit_kid'` members independently across all
`(career, kid)` rows for player scope, one career for save scope, and all known-system careers for
system scope. `player_body` remains unchanged with only `'soi'` and `'landed'`.

### `kitten`

`kitten(player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq,
PRIMARY KEY(player_id, kid))`. Written only by `distanceFold` → `UpsertKitten`; every numeric column
merges with `max()`, `name` is overwritten with the latest. `fastest_ms` here is the game's
**ecliptic-frame** `FastestSpeed` and must never become a speed board.

Read surface: `Batch.KittenTops` reads `travelled_m` and `missions` for the player-scope
`top_kitten_distance` and `top_kitten_missions`. `fastest_ms`, `mission_time_s` and `kia` are stored
and read by nothing.

### `career_kitten`

`career_kitten(player_id, career, system, kid, name, travelled_m, fastest_ms, missions,
mission_time_s, kia, updated_seq, PRIMARY KEY(player_id, career, kid))`. It is written alongside
`kitten` from `roster.snapshot` when the event's career and system are known. Numeric totals merge
with `max()` within the save-local row and the latest name wins, matching `kitten` without merging
separate saves.

This table is the source for `distance_travelled`: player scope sums every career row, career scope
sums that save, and system scope sums the rows for all saves with that system identity. It also
supplies the scoped candidates for `top_kitten_distance` and `top_kitten_missions`; system ties are
ordered by `(career, kid)`. The career dimension is essential because `kid` itself is not
save-scoped, as described under [`roster.snapshot`](#rostersnapshot).

### `feed`

`feed(id INTEGER PRIMARY KEY, at INTEGER, handle TEXT, type TEXT, summary TEXT)`, capped at
`FeedCap = 500`. `at` is `ev.RecvTime` — the **server** stamp, never `wall_t` (PROJ-010).

Three suppression rules, in order (`projector.go:373-383`, `stats/feed.go:21-31`):

1. the player has **no handle in the directory** (banned, purged, or never claimed) → no row;
2. the flight is **flagged** → no row (the same `scoreable`);
3. the event type is not one of the seven feed types → no row.

| type | extra predicate | sentence |
|---|---|---|
| `vehicle.impact` | `survived && !launch_pad && crew_count >= 1` | `"{h} lithobraked at {speed} m/s on {body} — and survived"` |
| `vehicle.rud` | — | `"{h} lost a vehicle to {causePhrase} on {body} at {speed} m/s"` |
| `vehicle.orbit` | `phase == "achieved"` | `"{h} made orbit around {body} ({ap} × {pe})"` |
| `vehicle.soi` | `to_body != ""` | `"{h} entered {body}'s sphere of influence"` |
| `kitten.tumble` | —; `from` does not alter the current feed sentence | `"{h}'s kitten {name} took a tumble at {speed} m/s on {body}"` |
| `kitten.kia` | — | `"{h} said goodbye to kitten {name}"` |
| `flight.ended` | `reason == "recovered"` | `"{h} brought {n} kittens home safely"`, or `"{h} recovered a vehicle"` when `crew_count < 1` |

Rendering helpers (`feed.go:93-181`): unknown cause → underscores→spaces, empty → "an unexplained
disassembly"; empty body → "somewhere"; empty kitten name → "a kitten"; `sanitize` strips control
chars and caps at 32; `num` is whole above 100 and one decimal below, NaN/Inf → "?"; `altitude`
renders ≥ 1000 m as km. **This is a separate number renderer from `internal/units` and does not
follow its rules.**

The stream **never replays history**; a reconnecting client re-reads the snapshot.

### `event_census`

`event_census(type, period, bucket, n, first_seq, last_seq, first_at, last_at,
PRIMARY KEY(type, period, bucket))`. `type = ''` (`stats.CensusAllTypes`) is the **stored total across
every type** — a point lookup rather than a group-by, and it stays honest for a type this build cannot
name. `period ∈ {alltime, daily, weekly, monthly, yearly}`; `bucket = ''` for `alltime`. **No
retention.**

`censusFold` (`stats/census.go:26-48`) writes ten rows per event: `{own type, ''} × 5 periods`. It
obeys **none** of the board rules — no flag exclusion, no handle requirement, no tie-break
(PROJ-082). An event with `RecvTime <= 0` lands only in the two all-time rows. `n` accumulates,
`first_seq`/`first_at` take the min, `last_seq`/`last_at` the max.

Surfaced by `GET /v1/stats`: `events.total/first/last` from the stored `''` row (never a sum),
`events.types` with `share = count/total`, `events.windows` (the four current buckets broken down by
type), `events.days`, `events.per_day`, `events.busiest`, `events.daily` (90 buckets), and
`collection.*`. Memoised on `(WriteGen, 10 s TTL)`.

### `proj_checkpoint`

**One shared cursor** for every fold, `projection = 'all'`, written in the same transaction as the
fold writes. Surfaced as `collection.projected` / `collection.lag` and `projector.checkpoint_seq` /
`lag_seq`.

---

## Suppression and eligibility matrix

| Suppression | Mechanism | Where |
|---|---|---|
| A type the player switched off in `[events]` never leaves the machine — the server sees an absence, not a suppression | `EventTypeFilter.IsEnabled`, applied at the pipeline's single funnel; six types cannot be switched off at all | `Detect/EventPipeline.cs`; MOD-072, PROJ-108 |
| A flagged flight scores nothing — every board, including counters | `scoreable` → `flight_state.flags == 0` | `stats/fold.go:205-220`; PROJ-001 |
| The `roster.snapshot` and flightless-`kitten.*` boards are exempt — `distance_travelled`, `top_kitten_distance`, `top_kitten_missions`, `longest_eva`, and `evas` whenever the EVA signal carried no vehicle id | `!ev.HasFlight()` → true | `stats/fold.go:226-228` |
| An **unknown** flag value still excludes | `FlagOther`, bit 5 | `stats/flight.go:29,34-48`; PROJ-002 |
| A `tuning`-flagged flight's tumbles do not count on the total, botched-landing counter or per-body family — the exclusion the flag exists for, which works only because `kitten.tumble` names a flight | `scoreable` once before all three `tumbleFold` writes | `stats/boards.go`; MOD-073 |
| A tumble is a botched landing only when the open-set discriminator is exactly `"airborne"`; `"grounded"`, `"unknown"` and future values remain ordinary tumbles | exact `From == "airborne"` | `tumbleFold` |
| A body that cannot form a safe dynamic stat still moves `kitten_tumbles` and, when airborne, `botched_landings`; only `tumbles_on_<body>` is absent | `TumblesOnStat` / `familyStat` refuses the family key after the fixed writes | `stats/boards.go` |
| An unreadable whole-vehicle part count moves the RUD total and cause family but neither part board | `PartCount <= 0` returns after the existing RUD writes | `rudPartsFold`; PROJ-088 |
| A crewless RUD moves the RUD and any positive-part boards but neither aboard-lost-vehicle board | `CrewCount < 1` skips only the two crew writes | `rudPartsFold` |
| A cause that cannot form a family key still moves every independently qualifying part and crew board | `RUDStat` failure skips only the cause-family write | `rudPartsFold` |
| `kittens_to_orbit_and_back` requires a recovered ending, at least one recovery-time kitten, an unflagged flight and an achieved orbit strictly earlier than recovery | payload predicates → `scoreable` → `flight_state.milestones & MilestoneOrbit` → `0 < first_orbit_seq < event.seq` | `kittensToOrbitFold` |
| An unknown career system still moves the player and save `kittens_to_orbit_and_back` sets but produces no system-scoped row | `SystemCareerSetCount` returns `known=false`; the first two independent counts were already written | `stats/batch.go` |
| A world sprint requires a nonempty destination, career, present nonnegative clock and unflagged flight | payload gate → `careerTime` → `scoreable` | `bodySprintFold` |
| A world first reached exactly at 31,536,000 s or 315,360,000 s counts; a later arrival does not | `first_sim_t <= threshold` | `CareerBodyCountBefore`, `BodyCountBefore`, `SystemBodyCountBefore` |
| A repeated SOI may lower an existing arrival across a sprint threshold after a rewind | `toBodyFold` lowers before `bodySprintFold` recomputes on every qualifying SOI | `BoardFolds` order |
| An unknown career system still moves save and player sprint results but produces no system-scoped row | `SystemBodyCountBefore` returns `ok=false` after the first two independent writes | `bodySprintFold` |
| Launch-pad impacts never score — on **both** impact boards | `!LaunchPad` | `stats/boards.go:390` |
| Crewless impacts never score — on **both** impact boards | `CrewCount >= 1` | `stats/boards.go:390` |
| An impact within 5 s of a teleport is not recorded at all | `Vehicle.IsImpactFxSuppressed()` | `Patcher.cs:423-424,455-456` |
| An impact whose vehicle died in frame *N* or *N+1* is `survived: false` | `ImpactCorrelator` | `ImpactCorrelator.cs:24-29` |
| A manual destroy also flips `survived` | `EndFlight` tells the correlator first | `EventPipeline.cs:398-399` |
| An impact within ±2 s of a `kitten.kia` **that named a flight** (rebuild only) — on **both** impact boards | `b.KIANear`, via the shared `survivedImpact`; the index is fed only by flight-bearing KIAs, and the mod attributes one only when it can prove it | `stats/boards.go:397-401`; `projector/rebuild.go:163`; MOD-073 |
| `peak_g_survived` **and** `max_q_survived` require the flight ended `recovered` (rebuild only) | `st.Recovered()`, via the shared `survivedLoad` | `stats/boards.go:485-487` |
| Absent `peak_g` / `max_q_pa` ≠ 0 | `*float64` + omit-don't-zero on the wire | `stats/payload.go`; `Payloads.cs:416-421` |
| Absent `lat` / `lon` / `radar_alt_m` ≠ 0 — a zeroed latitude is the equator and a zeroed radar altitude is the ground | `*float64` / `*Agg` + omit-don't-zero on the wire | `stats/payload.go`; MOD-078 |
| An unwritten orbit figure does not count — `ap_m == 0` (conic not `Bound`), `ecc == 0` or `inc_deg == 0` (unread) | `value > 0` on all four shape boards | `stats/boards.go:663-665` |
| A touchdown *from* an unreadable situation does not count — `"unknown"` is refused, not merely treated as contact-free | `knownSituation(from)` | `stats/situation.go`; `stats/boards.go:843` |
| A splashdown *from* a situation already touching a surface does not count — this is what stops an on/off-rails boat counting forever | `!hasSurfaceContact(from)` | `stats/boards.go:1000` |
| A `dragging` or `bottomed` arrival is not a splashdown — those touch terrain too | `contactOf(to) == contactOcean` | `stats/boards.go:1000` |
| A zero-length EVA does not count — `duration_s == 0.0` is an unreadable launch time | `DurationS > 0` | `stats/boards.go:803` |
| A zero-mass, zero-part, crewless or stage-less launch does not count | `> 0` on all **four** launch boards | `launchFold` |
| An unreadable orbital mass does not count — `mass_kg` is written as 0 when the read failed | `mass_kg > 0` on `heaviest_to_orbit` | `orbitMassFold`; PROJ-094 |
| A window with **no** terrain reading never scores — the aggregate is absent, not zeroed | `RadarAltM == nil` refused before the value is read | `lowestPassFold`; PROJ-095 |
| A terrain minimum of 0 does not count — that is a vehicle on the ground, and on an ascending board an unbeatable record | `RadarAltM.Min > 0` | `lowestPassFold`; PROJ-088 |
| A landing the vehicle did not walk away from scores nothing — on **both** landing boards | `survived`, via the shared `survivedLanding`; taken from the mod's one-full-frame hold, never re-derived | `survivedLanding`; PROJ-096 |
| A 0 m/s descent rate does not count | `vertical_speed_ms > 0` on `softest_landing` only — `landings` has no speed gate | `softestLandingFold`; PROJ-088 |
| A landing the vehicle did not survive produces **no feed line** — the `vehicle.rud` beside it already says so | `!p.Survived` → no summary | `stats/feed.go:44-49` |
| A bouncing lander cannot mint a landing every 500 ms | `vehicle.landed` shares the situation rule's 2 s debounce and marks no timer of its own | `EventDetector.CheckSituation`; MOD-076 |
| **Not** suppressed: a one-metre hop is a landing, and `warp_max` disqualifies nothing | Constitution §8 — neither infers intent from data shape | PROJ-096 / PROJ-098 |
| Banned players are invisible on every read surface | **absent from the in-memory directory**, so no handle resolves | PROJ-007 |
| Banned rows are still counted in `count` / `players` | unfiltered row counts, by design | PROJ-008 |
| Rank compensates for banned rows ahead | `StatAhead - StatsForPlayers(banned)` | `readapi.go:446-471` |
| Unknown / retired / banned handle → one 404 | not a ban oracle | `readapi.go:428-431` |
| A feed line needs a handle **and** an unflagged flight | `Summarize` | `stats/feed.go:21-31` |
| Raw event views drop flagged flights and handle-less players | `FlaggedFlights` + the directory | PROJ-051 |
| Install-derived identifiers are never published raw | `Redact` / `Label` | `readapi/privacy.go` |
| A family board is withheld from the public index below `min_players` | listing rule only — the value is still stored, the board is still served, the profile still shows it | PROJ-034 / PROJ-035 |
| Purge deletes from `events.db`; projections follow only on rebuild | `tombstone` | `migrations/events/0001_init.sql:82` |
| A shadowbanned player's events are **not in the log at all** — moved to `shadowban_event`, so no fold ever sees them; projections follow on rebuild, the handle directory hides them meanwhile | `shadowban`, `shadowban_event` | `migrations/events/0005_shadowban.sql` |

---

## Rebuild ≠ incremental

`projector/rebuild.go:71`. A rebuild builds into `projections.rebuild.db` from seq 0, then atomically
swaps (the old file is kept as `<path>.old` until reopen succeeds — PROJ-012).

- **Pass 1** (`:139`) applies `StateFolds()` only (`flight_state`, `career`) over the whole log and
  builds `kia map[flightID][]simT` from `kitten.kia` events carrying a flight and a sim time (`:163`).
  A KIA the mod could not attribute carries no flight and is **not** indexed — there is no key for it
  that is not a guess, and a guess voids an innocent flight's impact record.
- **Pass 2** (`:170`) uses `stats.NewRefinedBatch(tx, kia, …)`, applies `SecondPassFolds()` (boards +
  census) against a `flight_state` already complete for all history, and re-renders feed rows.
- Nothing is broadcast from a rebuild.

Refinement is carried on the `FlightStateReader`: `Refined()` is false incrementally, and `KIANear`
always answers false then. **This is D22, not a bug.** The divergences, exhaustively:

1. **A late `flight.flagged`.** Incrementally, events folded before the flag arrived already scored;
   a rebuild sees the completed `flight_state` on pass 2 and drops them all (PROJ-004).
2. **The ±2.0 s KIA window on the two impact boards** — `biggest_lithobrake_survived` and
   `biggest_impact_energy`, which share `survivedImpact` and therefore share the divergence. Applied
   only when `b.Refined()`. It is a real divergence **because `kitten.kia` names a flight**: an
   index keyed by flight over events that carried none would always be empty, and the rebuild would
   agree with the incremental path by accident. It fires on a scuttle with crew aboard, on the
   attributable deaths only.
3. **`ended_reason == 'recovered'` on the two structural-load boards** — `peak_g_survived` and
   `max_q_survived`, which share `survivedLoad`. Applied only when `b.Refined()`. This is still the
   **broadest** divergence: every `destroyed` / `despawned` / still-open flight loses both rows on
   rebuild.
4. **Feed rows.** `feedRow` resolves the handle from the *live* directory at fold time, and rebuild
   pass 2 re-renders them. A player banned or shadowbanned since the events were folded therefore
   keeps feed rows incrementally (until the 500-row cap ages them out) but produces none on rebuild.
5. **Undecodable events.** A build that gained a decoder, a fold or a board folds on rebuild what it
   skipped or ignored before. Incrementally the new fold only sees events arriving after the upgrade,
   because the checkpoint has already passed everything older; a rebuild replays the log from seq 0
   and the board fills from history. **That is the whole of it — there is no backfill script and none
   is needed.** The one thing a rebuild cannot invent is a field the wire never carried: a fold
   reading a key no stored payload holds decodes the same zero or the same absence on both paths, and
   the `> 0` gates refuse it either way.
6. **A shadow ban applied since the last rebuild.** The withheld events are gone from `event`, so the
   rebuild cannot see them and every board row they earned disappears. Incrementally those rows
   survive, because the cursor only moves forward and cannot take back what it already scored. This
   is why every shadow-ban verb queues a rebuild, and why the handle directory hides the account in
   the meantime. `unshadowban` is the same divergence in reverse — the events return **at their
   original seq**, so the rebuild reproduces the original values *and* the original `updated_seq`
   tie-break, which is what decides who holds a record when two players reach the same number.

### The build stamp — the divergence that used to be invisible

`proj_build` (projections migration 0005) records the fold-set identity that produced the file:
`stats.BuildID` over the projections schema version, every registered fold's name in order, and
`stats.BuildVersion`. At startup the projector compares it to its own.

Divergence 5 above is the reason. A deploy that adds or changes a board leaves the live file holding
the *old* definition, and folding forward mixes the two: the new board fills with events from the
deploy onwards, which is indistinguishable from a board nobody has scored on. So a mismatched stamp
**suspends the fold loop** and starts a rebuild (`[projector] auto_rebuild`, on by default). Boards go
stale for the length of the rebuild and are never wrong, and a board this deploy added reads empty
rather than short-by-history.

Fold names catch a board added, removed or renamed. **`stats.BuildVersion` is a hand-bumped constant
and catches what they cannot: a fold whose name did not change and whose meaning did** — a new
threshold, a changed unit, a widened eligibility rule, a different tie-break. Bumping it belongs in
the same commit as the change, exactly like an event's `ver`.

Things that deliberately **do not** diverge: rolling-window buckets (derived from `ev.RecvTime`, never
the wall clock — PROJ-043), retention trims (gated on `ev.Seq % 512`), and the census. Nor do the
newest boards add a sixth divergence — none of `heaviest_to_orbit`, `softest_landing`, `landings`,
`lowest_pass` or `biggest_stack` calls `b.Refined()` or
`b.KIANear`, and none derives anything from the wall clock or from map order:
`heaviest_to_orbit` / `biggest_stack` / `lowest_pass` are pure record-and-best folds over one
payload, `landings` uses `addCount` so its tie-break is "whoever reached N first" under replay, and
`softest_landing` uses `putBest`, whose strictly-smaller rule is replay-stable. `landed_bodies` uses `AddBody`'s row-novelty report, so it is
replay-correct the same way `soi_bodies` is; and the two per-kitten record boards break ties on `kid`
rather than on Go map order, so a rebuild reproduces the incremental `context` byte for byte.

---

## Conformance coverage

`contracts/testdata/` is generated by `catlogctl testvectors generate <dir>` and consumed by
`mod/catlog.lib.tests/Conformance/ContractVectorTests.cs`.

| File | Pins |
|---|---|
| `batches/batch-001.ndjson` | 33 envelopes, one line each; SHA-256 `51396327a7e8f7a89dbc5ee048811a96efb75d2bd8bf7a5e4961c7bf8112fd06` |
| `batches/batch-001.br` | the Brotli body as sent |
| `batches/batch-001.bh.txt` | `4l3WGOl7mWLE46sA2uy5vv3_704N5YrxKGSA34MSVnU` — base64url SHA-256 of the compressed body |
| `keys/*`, `license/*`, `proofs/*`, `expected/verify-results.json` | the credential / JWS layer, not events |

**Covered by a vector: 25 of 25.** Every registered type appears at least once, at the `ver` the
registry stamps for it today, and `Batch001_CoversEveryRegisteredType` fails the moment a type is
added to `EventTypes` without a line — so this section cannot go stale.

The line count is 33 rather than 25 because reaching the registry count was never the point: the set
exists to pin payload *shapes*, and seven shape families need more than one line to say what they have
to say.

| type | lines | why more than once |
|---|---|---|
| `system.discovered` | 1, 5 | line 1 is complete and precedes its catalogue; line 5 is `complete: false` and has no body rows. |
| `system.body` | 2, 3, 4 | line 2 is a root with `parent` and all six orbital-shape keys absent; line 3 is a bound body with `parent`, all six shape keys and finite period present; line 4 is an unbound body with the six shape keys present and `period_s` absent. All three carry finite normalised orientations. |
| `telemetry.window` | 11, 15 | line 11 carries `peak_g`, `max_q_pa`, the `radar_alt_m` aggregate and a complete finite `state`, with `n` 60 and `warp_max` 1; line 15 is the same type on rails at 1000× warp with all four **absent** and `n` 3. The pair pins atomic state presence/absence as well as omit-don't-zero. |
| `flight.started` | 7, 22 | line 7 is crewed with `kids` populated, `stage_count` 3 and `lat` / `lon` present, while `engine_count` is **absent**; line 22 is an uncrewed probe with explicit `engine_count: 0`, `kids` `[]`, `stage_count` 0 and `lat` / `lon` absent. The pair separates unknown from meaningful zero. |
| `flight.ended` | 27, 28, 32 | `recovered` with crew and a position; the silent-removal safety net (`despawned`, `crew_count` 0, `kids` `[]`, `body: "unknown"`, no position); and `destroyed`. |
| `vehicle.docked` / `vehicle.undocked` | 17, 23 | `other_flight` **null** and `other_flight` a ULID — the one in-payload `null` in the taxonomy. |
| `kitten.tumble` | 19, 20 | line 19 is `from: "airborne"`, a botched landing; line 20 is `from: "grounded"`, a trip. The pair pins both currently interpreted members while `from` remains an open-set string. |

Between them the lines pin an array field (`kids`, `roster.snapshot.kittens`), an optional present
**and** absent for the same field on the same type, a nested object (`agg`), a nested *optional*
object (`telemetry.window`'s `radar_alt_m`), a nested optional state object with two nested vectors,
an in-payload `null`, and all three envelope `flight`
cases — always non-null, always null, and conditionally null (`kitten.tumble`, `kitten.kia`, both
naming a flight). Lines 19 and 20 additionally pin `kitten.tumble.from` on both the airborne
failed-landing and grounded-trip sides without changing the current count fold. Line 14 pins all five
non-optional `vehicle.orbit` element keys and a finite bound period;
the separate system-body rows continue to pin the catalogue's different optional-period convention.

Vector-level assertions that apply to every line regardless: every `type` is in the registry, every
`id` parses as a ULID, the `flight` key is always present and is `null` or a ULID, `session` is
non-empty, `career` is 16 Crockford characters, `payload` is an object, each line is within
`Wire.MaxEventLineBytes` (`Batch001_IsAllKnownEnvelopeTypes`), and **`ver` equals
`EventTypes.VersionOf(type)`** (`Batch001_StampsTheRegistrysCurrentVersion`). The last of these is
the drift check the whole layer exists for — the generator hard-codes `ver` per line, so without it a
version bump on either side would leave the fixtures pinning a shape nobody emits while both suites
stay green. The Go half is `projector.TestGoldenBatchIsAtTheCurrentVersions`, which asserts the same
against `projector.currentVer` and additionally that every type that map overrides appears in a line.

Beyond the envelope, `Batch001_PayloadsRoundTripThroughTheirRecords` deserialises every `payload`
into its `Payloads.cs` record and re-serialises it with `CatlogJson.Options`, then compares key sets
and values. It is the only assertion in either suite that looks inside a payload, and it is what
makes a dropped or invented key a test failure rather than a silent divergence — and what makes the
present/absent pairs above load-bearing rather than decorative. Key *order* is deliberately not
compared: the Go generator builds payloads from `map[string]any` so `encoding/json` emits them
alphabetically, the mod emits declaration order, `bh` hashes the bytes each side actually produced,
and neither order is normative. See [Known drift](#known-drift).

---

## Known drift

Recorded here rather than silently fixed, so the next pass has a work list. Each item is either a
document that disagrees with the code, or behaviour no document states. **Nothing here is a claim
that the code is wrong.**

### Documents that disagree with the code

1. **Fixed (2026-08-09).** `docs/events.md` said `vehicle.impact.survived` meant "no destruction in
   the **same frame**". The code holds an impact for **one full frame** — frame *N*'s impact resolves
   at the end of frame *N+1*, and a destruction in either frame flips the verdict — and the taxonomy
   table now says so, alongside the note that `vehicle.landed.survived` goes through the same hold.
2. **`docs/events.md:83` — `other_flight` is typed as a ULID.** It is nullable and `"other_flight":null`
   is a legal emitted shape. The Go struct is a plain `string`, so null silently decodes to `""`.
3. **Fixed.** `docs/events.md`'s envelope comment said `flight` is "null for session/roster
   events"; `kitten.eva_end` also emits null, `kitten.eva_start` emits non-null, and `kitten.tumble`
   / `kitten.kia` emit null only when the mod cannot resolve a flight (MOD-073). The comment now
   reads "null when the event names no flight" and the two conditional cases are spelled out in both
   documents.
4. **`docs/events.md:89` — `roster.snapshot` "every 10 min of play".** The 600-second interval is
   compared against **sim** time, so under time warp snapshots come far more often in wall time. And
   "on session end" means **process unload only**; a save-load boundary emits no closing roster.
5. **`docs/events.md` — `telemetry.window` "one per vehicle per 30 s".** There are four close
   paths; three of them produce a short window (`n < 60`). **Now compounded**: under time warp a
   window still spans 30 *sim* seconds but is sampled at 2 Hz *wall*, so `n` can be far below 60 with
   no close-path involved at all. `warp_max` is what says so, and nothing on the site or in
   `events.md` connects the two.
6. **`docs/events.md:64` — the `situation` list is missing `"unknown"`**, which is emittable. The
   server now carries its own copy of the eight real names
   (`stats/situation.go`) and treats everything else, `"unknown"` included, as no surface contact.
7. **`docs/ingest-api.md:277` — the career-board value is "seconds since the career began".** The
   fold multiplies by 1000 and the unit string is `"ms"` (PROJ-047). The same stale phrase appears in
   `store/projections.go:76` and `stats/fold.go:133-134`.
8. **`docs/ingest-api.md:217-218` — response shapes are missing published fields**: `periods` on the
   board index, and `title` / `unit` / `period` / `bucket` / `limit` / `offset` on the board page.
9. **`docs/ARCHITECTURE.md:51-52` and `docs/CONSTITUTION.md:70-72` still claim stat keys are
   compile-time constants and enums are allow-lists.** Superseded by PROJ-033 / PROJ-037;
   `docs/integrity-audit.md:54` has already been corrected, these two have not.
10. **`stats/fold.go:185-186` says `setValue` serves `soi_bodies`.** `soiFold` uses `addCount`
    (PROJ-011); `setValue` has exactly one caller, `distanceFold`.
11. **Fixed.** `docs/server.md`'s rebuild section listed three sources of divergence; feed-row handle
    resolution is a fourth and is now named there, as is the newly-live decoder case.
12. **Fixed.** `docs/server.md` listed the fixed board keys with no titles or units; it now groups
    them by fold kind and points here for the canonical table, which is the right split — a full
    board table in two documents is a table that goes stale in one of them.

### Behaviour no document states

13. **`kitten.eva_start` can precede its own `flight.started`.** `Patcher.CreateKittenEvaPostfix`
    raises the signal directly, bypassing `Patcher.Track`, and `Tracker.FlightFor` mints. The EVA
    vehicle's `flight.started` arrives at the next 2 Hz tick and reuses the same ULID. This is the
    one exception to the ordering invariant `PolledSignals.cs:70-81` exists to guarantee.
14. **`VehicleRecoveredSignal` is dead code in the shipped mod** — defined and handled, raised only
    by `catlog.sim` and the test suites.
15. **`"unknown"` is an emittable value for `body`, `situation` and `engine`.** For `body` that means
    a real `fastest_to_unknown` board once two players hit it.
16. **`vehicle.rud.peak_g` / `peak_q_pa` are never omitted** — a different quantity from
    `telemetry.window`'s `StructuralLoad`-derived values, and easy to conflate with the
    omit-don't-zero rule.
17. **`vehicle.impact.speed_ms` means two different things** — closing *normal* speed for a ground
    impact, a reconstructed `√(2E/m)` scalar for a splash. Indistinguishable on the wire.
18. **Session-wide `flight.flagged` replays lose their detail text** — a flight started after the
    flag gets `"session-wide flag"` instead of the original message.
19. **`flight.flagged` can mint a flight for a vehicle that has no `flight.started`.** The `console`
    flag is the realistic case: the terminal argument string is passed as the vehicle id without
    confirming it names a tracked vehicle.
20. **`flight.ended.crew_count` is `0` on the safety-net path** regardless of who was aboard.
21. **`kitten.eva_end.duration_s` is `0.0` when `LaunchGameTime` is unreadable.**
22. **`crew_count` for a `KittenEva` is hard-coded to 1** — consistent with `Vehicle.SeatCount`, but
    worth stating since crew survival is a scoring input (D11).
23. **Superseded.** The six types that were stored but read by nothing — `vehicle.atmosphere`, all
    three `engine.*`, both `kitten.eva_*` — are decoded and folded as of the board expansion, as is
    `vehicle.situation`. What is left is smaller and deliberate: `vehicle.undocked` and
    `vehicle.docked.other_flight` fold into nothing; `engine.shutdown` decodes and counts nothing;
    `engine.*.engine` / `.count`, `kitten.eva_*.kid`, `vehicle.situation.orbital_speed_ms`,
    `vehicle.rud.peak_g` / `.peak_q_pa` / `.altitude_m` / `.crew_count`, `roster.snapshot`'s
    `fastest_ms` (deliberately — ecliptic frame) / `.mission_time_s` / `.kia`, `telemetry.window`'s
    `accel_ms2` / `mass_kg_last` / `n` / `t0_sim` and every aggregate member no board takes, and
    `session.started.*` are all decoded and read by no fold.

    **The list is long and every entry on it is deliberate**, including the spatial keys:
    `kids` on both flight events, every `lat` / `lon`, `flight.ended.body`,
    `vehicle.situation.radar_alt_m`, `telemetry.window.warp_max`, and `vehicle.landed`'s
    `radar_alt_m` / `lat` / `lon` are decoded and read by nothing. They are recorded because the
    immutable log is the product; a board for each of them is a separate decision.
    `flight.ended.body` is the one with an obvious consumer — `flight_state.body` still comes only
    from `flight.started`, so a flight whose start was never folded has an empty body although its
    end now carries one. Reading it would be a rebuild-only improvement.
24. **The conformance vector is not byte-representative of mod output.** It is Go-generated and
    alphabetises payload keys; the C# mod emits declaration order. Harmless for `bh`, which hashes
    whatever bytes are actually sent, and no longer unstated — [Conformance
    coverage](#conformance-coverage) says so, and the payload round-trip test deliberately compares
    key *sets* rather than order. The divergence itself stands: neither order is normative, and
    making one of them so would buy nothing.
25. **`Board.Career` carries a `json:"career"` tag but is exposed in no response struct.** Clients
    infer "career board" from `ascending` + `unit == "ms"` + the presence of `rewound`.
26. **Fixed (2026-08-09).** The ±2 s KIA window and the `tuning` flag were both inert, because
    `kitten.tumble` and `kitten.kia` named no flight and neither the rebuild's KIA index nor
    `scoreable`'s flag gate can act on an event that names one. Both mechanisms passed their tests,
    which constructed the events *with* a flight the mod did not send — **a guard whose test data is
    shaped differently from real data is not a guard.** The fix was on the mod side: both events
    attribute a flight. `batch-001.ndjson` lines 19, 20 and 31 pin that envelope shape cross-language.
    See MOD-073.

27. **Fixed (2026-08-09).** The conformance vectors covered five types on five lines, so nothing in
    `contracts/testdata/` pinned the omit-don't-zero rule for `lat` / `lon` / `radar_alt_m` across
    the two implementations. The fixture set grew again with the final-v1 discriminator pairs and is now 33 lines
    covering all 25 registered types, and
    two assertions stop it drifting again: `ver` must equal the registry's current version for the
    type, and every registered type must appear in a line. See
    [Conformance coverage](#conformance-coverage) and INGEST-025.

28. **`telemetry.window.warp_max` decodes as `0` when a payload omits it, not as `1`.** The intended
    default is `1` (a stopped clock is not a legal warp) but the Go zero value for the field is `0`.
    Nothing reads it today, so it is invisible; the first reader must treat `0` as "absent" at the
    read site. Recorded in a comment on the field and in PROJ-098.

29. **`vehicle.landed` emits from a frame boundary, not from `ProcessFrame`.** It is detected on the
    worker like every other frame-derived event, but its envelope is minted by whichever correlator
    drain settles the verdict, which is one frame later than an impact raised inside the same frame.
    No document other than this one's catalog entry states the asymmetry, and it is the only event
    type whose value and whose emission come from different places.
