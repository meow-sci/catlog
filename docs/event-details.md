# Event details — the technical event & projection reference

**This is the canonical, 1:1 technical reference for every event catlog produces and every
projection catlog derives from them.** It is the document a maintainer reads before touching a
detector, a payload, a fold, or a board, and the document a maintainer updates *in the same commit*
as that change.

It has a mandatory companion. The player-facing site under [`docs-site/`](../docs-site/) is the same
information rewritten for people who play the game rather than build it. **The two move together.**
See [Maintenance contract](#maintenance-contract) below, and the rule as stated in
[`CLAUDE.md`](../CLAUDE.md) and [`docs/CONSTITUTION.md`](CONSTITUTION.md).

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
- [The event catalog](#the-event-catalog) — 22 sections
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
| Configurable range | `sample_hz` clamped to **[0.1, 20]** | `ModConfig.Normalize`, `Config/ModConfig.cs:224` |
| Telemetry window | **30.0 sim seconds** (`window_s` clamped [5, 300]) | `Wire.TelemetryWindowSeconds`, `Wire.cs:133`; `ModConfig.cs:225` |
| Detector debounce | **2.0 sim seconds** per (vehicle, `DetectKind`) | `Wire.DetectorDebounceSeconds`, `Wire.cs:136` |
| Atmosphere hysteresis | **±2 %** of atmosphere height | `Wire.AtmosphereHysteresis`, `Wire.cs:139` |
| Orbit-achieved margin | **1000 m** above atmosphere top | `Wire.OrbitAchievedMarginM`, `Wire.cs:142` |
| Roster poll interval | **600 sim seconds** | `PolledSignals.RosterIntervalSeconds`, `mod/catlog/PolledSignals.cs:30` |
| Manual-destroy → KIA attribution window | **2.0 sim seconds** | `PolledSignals.ManualDestroyWindowSeconds`, `:36` |
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
- `kind` comes from `EventTypes.KindOf` (`Events/EventTypes.cs:141-142`). **`telemetry.window` is the
  only kind-0 type**; the other 21 are kind 1, explicitly including `roster.snapshot` because it
  carries totals that move boards (`EventTypes.cs:136-139`).
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
| `type` | string | required | `:18-19`, set from the `EventTypes` constant at the call site | Namespaced lowercase `[a-z0-9_.]`. Must be one of the 22 registry names or the **whole batch** is rejected (`decode.go:223-227`, `ingest/types.go:16-39`). |
| `ver` | int | always emitted | `:22-23`, from `EventTypes.VersionOf(type)` at `:95` | **All 22 types are `ver: 1`** (`EventTypes.cs:89-113`). Server requires present and ≥ 1 (`decode.go:228-233`); unknown-but-higher is accepted and stored. |
| `flight` | string \| null | **key always present** | `:29-30`; no `JsonIgnore` (`Util/CatlogJson.cs:16-21`) | ULID when non-null; validated as a ULID when present (`decode.go:239-244`). Null on `session.started`, `roster.snapshot`, `kitten.eva_end`, `kitten.tumble`, `kitten.kia`. |
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

### `body` — fully open, opaque to the server

No allow-list anywhere. Value is `IParentBody.Id.ToLowerInvariant()` (`VehicleTelemetry.BodyName`,
`:267-271`, anchor `KSA/IObjectId.cs:5` / `KSA/Astronomical.cs:96`), or the literal `"unknown"` on a
failed read. To *get a board* a name must additionally satisfy the stat-suffix shape: lowercase,
first char `[a-z0-9]`, rest `[a-z0-9._-]`, ≤ 40 chars (`stats/boards.go:185-200`).

### Small literal sets

`vehicle.atmosphere.dir` ∈ `{entered, exited}` (`Detect/EventDetector.cs:313,326`);
`vehicle.orbit.phase` ∈ `{achieved, escaped}` (`:373,401`). Both are literals at the detector call
sites, not enums.

`EngineEventKind { Ignition, Shutdown, Flameout }` (`GameSignal.cs:41-55`) selects the event type via
`EventTypes.TypeOf` (`:193-198`), default → `engine.flameout`.

---

## Signal → event dispatch

`EventPipeline.Dispatch` (`Detect/EventPipeline.cs:180-297`) is the single switch. Signal records are
in `Events/GameSignal.cs`.

| `GameSignal` subtype | Raised by | Produces |
|---|---|---|
| `FrameBoundarySignal` (`:138`) | `GameBridge.EndFrame` ← `CatlogRuntime.Tick` (`:366`) | drains `ImpactCorrelator.EndFrame()` → 0..n `vehicle.impact` (`EventPipeline.cs:184-187`) |
| `SessionLoadedSignal` (`:151`) | `CatlogRuntime.OnSessionBoundary` (`:298-300`) ← `Patcher.SessionBoundaryPostfix` (`:691-701`) | `session.started` + full pipeline reset |
| `VehicleCreatedSignal` (`:171`) | `PolledSignals.Track` (`:94-103`) | `flight.started` + replayed session-wide `flight.flagged` |
| `VehicleRemovedSignal` (`:188`) | `Patcher.DisposePrefix` (`:537-538`); `PolledSignals.Prune` (`:228`) | `flight.ended` + drained impacts + flushed window |
| `VehicleRecoveredSignal` (`:200`) | **nothing in the shipped mod** — sim/tests only | `flight.ended` with `reason: recovered` |
| `RudSignal` (`:217`) | `Patcher.DestroyVehicleFromEventPrefix` (`:393-405`) | `vehicle.rud` + marks the correlator |
| `ImpactSignal` (`:238`) | `Patcher.GroundImpactApplyPostfix` (`:430-440`) | held by the correlator; later `vehicle.impact` |
| `SplashSignal` (`:259`) | `Patcher.WaterSplashApplyPostfix` (`:469-471`) | converted to an `ImpactSignal` with `launch_pad=false` (`ImpactCorrelator.cs:52-60`) → `vehicle.impact` |
| `StagingSignal` (`:273`) | `Patcher.ActivateNextSequencePostfix` (`:578`) | `vehicle.staging` |
| `DockSignal` (`:281`) | `Patcher.DockPostfix` (`:602`) | `vehicle.docked` |
| `UndockSignal` (`:289`) | `Patcher.UndockPostfix` (`:625`) | `vehicle.undocked` |
| `EngineSignal` (`:299`) | `PolledSignals.PollVehicle` (`:174-190`) | `engine.ignition` / `engine.shutdown` / `engine.flameout` |
| `EvaStartSignal` (`:312`) | `Patcher.CreateKittenEvaPostfix` (`:650`) | `kitten.eva_start` |
| `EvaEndSignal` (`:320`) | `Patcher.DisposePrefix`, KittenEva branch (`:530-535`) | `kitten.eva_end` |
| `TumbleSignal` (`:329`) | `PolledSignals.PollVehicle` (`:165-169`) | `kitten.tumble` |
| `KiaSignal` (`:337`) | `PolledSignals.PollRoster` roster diff (`:269-273`) | `kitten.kia` |
| `FlaggedSignal` (`:346`) | `Patcher.Flag` (`:754`), `Patcher.UniverseDestroyPrefix` (`:678-683`), `PolledSignals.CheckTuning` (`:242-248`) | `flight.flagged` (1..n) |
| `RosterSampleSignal` (`:357`) | `PolledSignals.PollRoster` (`:282`), `PolledSignals.EmitRoster` (`:148`) | `roster.snapshot` |

Frame-derived, no signal: `EventDetector.Observe` on the published `TelemetryFrame` produces
`vehicle.situation`, `vehicle.atmosphere`, `vehicle.orbit`, `vehicle.soi`
(`EventPipeline.ProcessFrame`, `:104-105`); `WindowAccumulator` produces `telemetry.window`
(`:107-110`).

An unknown signal subtype is **ignored with a debug log**, never thrown — signals arrive from Harmony
patch bodies and must never kill the worker (`EventPipeline.cs:292-295`).

---

## The registry

`mod/catlog.lib/Events/EventTypes.cs:18-81` (names), `:89-113` (versions). Server mirror
`server/internal/ingest/types.go:16-39`. **The two lists agree exactly** — 22 names, same spelling,
same order.

| # | `type` | `ver` | outbox kind | Trigger | Feeds |
|---|---|---|---|---|---|
| 1 | `session.started` | 1 | 1 | event | `career` (rewind mark) |
| 2 | `flight.started` | 1 | 1 | polled-discovery | `flight_state` |
| 3 | `flight.ended` | 1 | 1 | event (+ passive net) | `flight_state`, `kittens_recovered`, feed |
| 4 | `flight.flagged` | 1 | 1 | event (4 of 5) / passive (`tuning`) | `flight_state` → **excludes everything** |
| 5 | `vehicle.situation` | 1 | 1 | passive | — |
| 6 | `vehicle.atmosphere` | 1 | 1 | passive | — (stored, unfolded) |
| 7 | `vehicle.orbit` | 1 | 1 | passive | `orbits_achieved`, `fastest_to_orbit`, feed |
| 8 | `vehicle.soi` | 1 | 1 | passive | `soi_bodies`, `fastest_to_<body>`, `player_body`, feed |
| 9 | `vehicle.rud` | 1 | 1 | event | `rud_total`, `rud_<cause>`, feed |
| 10 | `vehicle.impact` | 1 | 1 | event (1-frame hold) | `biggest_lithobrake_survived`, feed |
| 11 | `vehicle.staging` | 1 | 1 | event | `stagings` |
| 12 | `vehicle.docked` | 1 | 1 | event | `dockings` |
| 13 | `vehicle.undocked` | 1 | 1 | event | — (decoded, counts nothing) |
| 14 | `engine.ignition` | 1 | 1 | passive | — (stored, unfolded) |
| 15 | `engine.shutdown` | 1 | 1 | passive | — (stored, unfolded) |
| 16 | `engine.flameout` | 1 | 1 | passive | — (stored, unfolded) |
| 17 | `kitten.eva_start` | 1 | 1 | event | — (stored, unfolded) |
| 18 | `kitten.eva_end` | 1 | 1 | event | — (stored, unfolded) |
| 19 | `kitten.tumble` | 1 | 1 | passive | `kitten_tumbles`, feed |
| 20 | `kitten.kia` | 1 | 1 | passive | lithobrake KIA window (rebuild), feed |
| 21 | `roster.snapshot` | 1 | **1** | passive (+1 event) | `distance_travelled`, `kitten` |
| 22 | `telemetry.window` | 1 | **0** | passive | `peak_g_survived`, `fastest_surface_speed`, `fastest_orbital_speed` |

Every event additionally lands in `event_census` (10 rows: own type + total, × 5 periods).

---

## The event catalog

Each entry has the same eight blocks: **Wire**, **Payload**, **Detector**, **Game source**,
**Classification**, **Dedup / ordering**, **Server**, **Vectors**. "Classification" is the answer to
*is this event-driven or passive telemetry* — the distinction the player-facing site surfaces as
"something happened" vs "sampled in the background".

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

**Vectors.** `contracts/testdata/batches/batch-001.ndjson` line 1.

---

### `flight.started`

**Wire.** `"flight.started"` (`EventTypes.cs:21`), `ver` 1, kind 1. `flight` = the newly minted (or
re-resolved) flight ULID.

**Payload** — `FlightStartedPayload`, `Payloads.cs:34-39`

| Key | Type | Units | Source |
|---|---|---|---|
| `vehicle_name` | string | — | `Ids.SanitizeVehicleName(created.VehicleName)` (`EventPipeline.cs:324`). The signal's `VehicleName` **is the vehicle id** — KSA has no separate display name (`PolledSignals.cs:98`). |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` (`PolledSignals.cs:99`) → lowercase `IParentBody.Id`, or `"unknown"`. |
| `mass_kg` | number | kg | `VehicleTelemetry.MassKg` ← `Vehicle.TotalMass` (a **float**, `KSA/Vehicle.cs:551`), `Sanitize.Finite`d. 0 when unreadable. |
| `part_count` | int | count | `Vehicle.Parts.Count` (`KSA/PartTree.cs:89`). 0 when unreadable. |
| `crew_count` | int | count | **Occupied** seats, not seat count: iterates `Vehicle.Crew` (`ReadOnlySpan<IVASeat>`, `KSA/Vehicle.cs:373`) counting `seat.AssignedKittenHash != KeyHash.Zero` (`VehicleTelemetry.cs:327-334`). **A `KittenEva` always returns 1** (`:324-325`). |

`LaunchGameTime` rides on the signal (`GameSignal.cs:180`) but is **not** on the payload — it is half
of the flight identity only.

**Detector.** `EventPipeline.OnVehicleCreated` (`:317-341`). Resolves the flight with
`Tracker.FlightFor(created.VehicleId, created.LaunchGameTime)` (`:319`), emits `flight.started`, then
**replays every session-wide flag onto the new flight** as `flight.flagged` with detail
`"session-wide flag"` (`:332-339`).

**Game source — polled, not patched (deliberate).** `PolledSignals.Track` (`mod/catlog/PolledSignals.cs:87-105`)
raises `VehicleCreatedSignal` the first time catlog *sees* a vehicle id. Two call sites:

1. the 2 Hz sample pass — `PolledSignals.Poll` → `Track` (`:129`), over
   `VehicleTelemetry.CollectVehicles`, which walks `Universe.CurrentSystem.All.UnsafeAsList()`
   type-testing for `Vehicle` (`VehicleTelemetry.cs:575-593`; `KittenEva : Vehicle`, so EVA kittens
   are included);
2. **ahead of any vehicle-scoped Harmony signal**, via `Patcher.Track` (`Patcher.cs:764-775`), which
   drains the resulting signals into the bridge *before* the signal it was called for.

Why not patch the registration hook: `CelestialSystem.Register` sees a half-built vehicle where every
read throws (B6), and a vehicle created and destroyed inside one 0.5 s sample interval would
otherwise emit a RUD against a flight with no `flight.started` (`docs/mod.md:189-192`).

**Classification.** **PASSIVE-DISCOVERED, event-shaped.** The trigger is "catlog saw a vehicle id it
has not seen", evaluated at 2 Hz and also on demand from patch bodies. No debounce; the
`_vehicles.ContainsKey(id)` check (`PolledSignals.cs:90`) is the once-only latch.

**Dedup / ordering.** One per `(vehicle_id, LaunchGameTime)`. Because `FlightFor` learns a NaN→real
`LaunchGameTime` in place (`FlightTracker.cs:102-103`), a flight ULID minted earlier by an EVA start
or a flag is adopted rather than replaced.

**Server.** `flightFold` → `StartFlight(crew_count, body, seq)` (`stats/flight.go:108-113`), creating
the `flight_state` row every board consults.

**Vectors.** `batch-001.ndjson` line 2.

---

### `flight.ended`

**Wire.** `"flight.ended"` (`EventTypes.cs:24`), `ver` 1, kind 1. `flight` = the flight being closed.

**Payload** — `FlightEndedPayload`, `Payloads.cs:44-46`

| Key | Type | Values |
|---|---|---|
| `reason` | string | `"recovered"` \| `"destroyed"` \| `"despawned"` (`EventTypes.cs:161-165`) |
| `crew_count` | int | occupied seats at the moment it ended (`Patcher.cs:538`). **0** on the silent-removal safety-net path (`PolledSignals.cs:228`), indistinguishable on the wire from a genuinely empty vehicle. |

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
`stats/boards.go:527-541`) and the feed (`stats/feed.go:78-84`). `flight_state.ended_reason` is what
`peak_g_survived`'s rebuild refinement tests (`Recovered()`, `stats/flight.go:88`).

**Vectors.** `batch-001.ndjson` line 5.

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
(`EventPipeline.cs:47,356`).

**Server — this is the exclusion mechanism.** `flightFold` ORs `FlagBit(flag)` into
`flight_state.flags` (`stats/flight.go:120-125`). Bits: 0 `teleport`, 1 `refuel`, 2 `resource_edit`,
3 `console`, 4 `tuning`, **5 `other`** for an unrecognised value (`:29,34-49`, PROJ-002) — failing
open would make every future flag a scoring loophole. `scoreable` (`stats/fold.go:205-220`) then
suppresses **every board**, the feed, and the raw event views for that flight. The one exception is
`distance_travelled`, whose source event carries no flight at all.

**Vectors.** None.

---

### `vehicle.situation`

**Wire.** `"vehicle.situation"` (`EventTypes.cs:30`), `ver` 1, kind 1. Detector kind
`DetectKind.Situation = 0` (`EventDetector.cs:11-12`).

**Payload** — `VehicleSituationPayload`, `Payloads.cs:62-68`

| Key | Type | Units | Source |
|---|---|---|---|
| `from` | string | — | `state.ReportedSituation` — the situation **last actually reported on the wire**, not the previous sample (`EventDetector.cs:241`) |
| `to` | string | — | `curr.Situation` (`:242`) |
| `body` | string | — | `curr.Body` |
| `altitude_m` | number | metres above the parent's **mean radius** | `Vehicle.GetBarometricAltitude()` (`KSA/Vehicle.cs:2840-2843` = `PositionCci.Length() - Parent.MeanRadius`). **Not terrain-relative** — that is `GetRadarAltitude()` at `:2845`. |
| `surface_speed_ms` | number | m/s | `Vehicle.GetSurfaceSpeed()` (`KSA/Vehicle.cs:2759`). **Never `NavBallData.Speed`** — that is frame-dependent on the player's navball mode (`VehicleTelemetry.cs:436-437`). |
| `orbital_speed_ms` | number | m/s | `Vehicle.OrbitalSpeed` (`KSA/Vehicle.cs:581` = `GetVelocityCci().Length()`), body-centred inertial |

**Detector** — `EventDetector.CheckSituation` (`:222-252`):

```
if (baseline || ReportedSituation is null) { ReportedSituation = curr.Situation; return; }  // seeds, emits nothing
if (ReportedSituation == curr.Situation) return;
if (!CanFire(Situation, curr.SimT)) return;                                                 // 2 s debounce
emit; ReportedSituation = curr.Situation; MarkFired(...)
```

The edge is taken off the **latch**, not the raw previous snapshot — that is what makes the 2 s
debounce rate-limiting rather than lossy: a suppressed transition is re-detected on the next sample
and reported *from* the last state that reached the wire (`:121-127`).

**Game source.** `Vehicle.Situation` (`KSA/Vehicle.cs:533`, `=> _props.Situation`), enum
`KSA/Situation.cs:3-13`, mapped by `VehicleTelemetry.SituationName` (`:895-906`) inside
`VehicleTelemetry.Sample` (`:148`), sampled at **2 Hz** from `CatlogRuntime.SamplePass` (`:488`).

**Classification.** **PASSIVE** (2 Hz poll → worker prev/curr comparator). Gate: 2.0 sim-second
debounce per (vehicle, kind); baseline emits nothing. A backwards `sim_t` jump calls
`VehicleDetectState.Rebaseline()` (`EventDetector.cs:208-209, 96-105`), dropping every latch and
timer — a save was loaded.

**Server.** Decoded (`stats/payload.go`) but no fold reads it.

**Vectors.** None.

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

**Server.** **Not decoded** — `server/internal/stats/payload.go:220-222` explicitly lists
`vehicle.atmosphere` among the types no board reads. Stored, never folded.

**Vectors.** None.

---

### `vehicle.orbit`

**Wire.** `"vehicle.orbit"` (`EventTypes.cs:36`), `ver` 1, kind 1. Detector kinds
`OrbitAchieved = 3`, `OrbitEscaped = 4` (`EventDetector.cs:20-24`), independent debounce timers.

**Payload** — `VehicleOrbitPayload`, `Payloads.cs:88-94`, built `EventDetector.cs:406-419`

| Key | Type | Units | Source / gotcha |
|---|---|---|---|
| `phase` | string | — | literal `"achieved"` (`:373`) / `"escaped"` (`:401`) |
| `body` | string | — | `curr.Body` |
| `ap_m` | number | **metres of ALTITUDE above the parent's mean radius** | `Sanitize.RadiusToAltitude(orbit.Apoapsis, parent.MeanRadius)` **only when `OrbitClass.Bound`, else 0.0** (`VehicleTelemetry.cs:162-164`). The game's `Orbit.Apoapsis` (`KSA/Orbit.cs:1168`) is a **radius from body centre**, and is **negative** on a hyperbola / NaN on a parabola (B4). |
| `pe_m` | number | metres of altitude | `Sanitize.RadiusToAltitude(orbit.Periapsis, meanRadius)`, computed **unconditionally** (`:165`) — so it can legitimately be negative. |
| `ecc` | number | — | `orbit.Eccentricity` (`KSA/Orbit.cs:1154`), `Sanitize.Finite`d |
| `inc_deg` | number | **degrees** | `orbit.Inclination * (180/π)` (`VehicleTelemetry.cs:166`). **The game stores radians** (`KSA/Orbit.cs:1160`). |

**Detector — two independent rules.**

`CheckOrbitAchieved` (`EventDetector.cs:344-376`):

```
safeAltitude = curr.AtmoHeightM + 1000        // Wire.OrbitAchievedMarginM
above        = curr.IsBoundOrbit && curr.PeAltM > safeAltitude
baseline: OrbitAchieved = above               (emits nothing)
rising edge only; falling back below the bar re-arms silently (:360-366)
2 s debounce on DetectKind.OrbitAchieved
```

On an airless body `AtmoHeightM == 0`, so the 1000 m margin alone is the bar (`:347-348`).

`CheckOrbitEscaped` (`:378-404`): edge on `IsBoundOrbit` going **true → false**; regaining a bound
orbit re-arms silently.

**No NaN sniffing anywhere.** `TelemetrySnapshot.IsBoundOrbit` (`Telemetry/TelemetrySnapshot.cs:151-156`)
uses the `OrbitClass` the game project supplied, falling back to finite `ecc < 1` only for callers
(simulator, hand-built fixtures) that have no classifier.

**Game source.** `VehicleTelemetry.ClassifyOrbit` (`:192-199`) calls the game's own predicates in
this order — **parabolic first, because `ecc == 1` is the knife-edge**: `Orbit.IsParabolic()`
(`KSA/Orbit.cs:1757`), `Orbit.IsHyperbolic()` (`:1763`), `Orbit.IsBound()` (`:1775`). The result is
carried on the snapshot as `OrbitClass` (`TelemetrySnapshot.cs:15-28,112`) precisely because
`catlog.lib` must stay KSA-free.

Guard before any orbit read: `VehicleTelemetry.IsReadable` (`:92-105`) checks
`vehicle.FlightPlan.Patches.Count > 0`, because `Vehicle.Parent => Orbit.Parent => Patch =>
FlightPlan.Patches[0]` **throws `ArgumentOutOfRangeException`** on an uninitialised vehicle rather
than returning null (B6, `KSA/FlightPlan.cs:64`). Sampled at 2 Hz.

**Classification.** **PASSIVE.** Threshold `pe_alt > atmo_height + 1000 m`; conic class from the
game; 2 s debounce per phase; baseline seeds silently.

**Server.** `orbitsFold` counts `phase == "achieved"` on an unflagged flight into `orbits_achieved`
(`stats/boards.go:484-498`); `toOrbitFold` takes the same events into `fastest_to_orbit`
(`:632-650`); the feed renders `"{h} made orbit around {body} ({ap} × {pe})"` (`stats/feed.go:53`).
`escaped` counts nothing anywhere.

**Vectors.** None.

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

**Server.** Two folds, in this order (the order matters):

1. `soiFold` (`stats/boards.go:505-523`) — `b.AddBody(playerID, "soi", to_body, seq)` reports whether
   the `player_body` row was **new**; only then `addCount(soi_bodies, 1)`. No `count(*)`, correct
   under replay (PROJ-011).
2. `toBodyFold` (`:659-693`) — **always** lowers `player_body.first_sim_t` (seconds) for that body,
   regardless of whether a board key can be built (`:673-679`), then `putBest(fastest_to_<body>, …)`
   when `FastestToStat(to_body)` succeeds.

`soiFold` must precede `toBodyFold` because `LowerBodyTime` is a no-op if the row does not exist
(`batch.go:538-540`). Body names are **never** validated against a list: a `to_body` that cannot be a
stat key still counts towards `soi_bodies` and still records `first_sim_t`.

**Vectors.** None.

---

### `vehicle.rud`

**Wire.** `"vehicle.rud"` (`EventTypes.cs:42`), `ver` 1, kind 1.

**Payload** — `VehicleRudPayload`, `Payloads.cs:111-118`, built `EventPipeline.cs:211-218`

| Key | Type | Units | Source |
|---|---|---|---|
| `cause` | string | — | `EventTypes.ToWire(rud.Cause)` ← `VehicleTelemetry.MapCause(destructionEvent.Cause)` (`Patcher.cs:397`) |
| `peak_g` | number | g | `VehicleDestructionEvent.PeakGLoad`, a **`float` `required`** field (`Patcher.cs:398`) |
| `peak_q_pa` | number | Pa | `VehicleDestructionEvent.PeakDynamicPressure` (`:399`) |
| `speed_ms` | number | m/s | `Vehicle.GetSurfaceSpeed()` (`:400`) |
| `altitude_m` | number | m above mean radius | `GetBarometricAltitude()` (`:401`) |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` (`:402`) |
| `crew_count` | int | count | `VehicleTelemetry.CrewCount(vehicle)` (`:405`). **Per D11 all of them survive** — the physics destruction path calls `EndAllCrewMissions` and never `KillCrew` (`:403-404`). |

`peak_g` / `peak_q_pa` here are **not** the nullable `StructuralLoad`-derived telemetry values. They
come off the destruction event itself and land in non-nullable payload fields, so a **0 is emitted
rather than the key omitted** — the opposite of `telemetry.window`'s rule.

**Detector.** `EventPipeline.Dispatch`, `RudSignal` case (`:207-219`). **Order is load-bearing**:
`_correlator.Destroyed(rud.VehicleId)` is called *first* (`:210`) so an impact recorded earlier in
the same frame resolves to `survived = false`.

**Game source.** A **prefix** on `Universe.DestroyVehicleFromEvent(Vehicle, VehicleDestructionEvent)`
— `KSA/Universe.cs:1699`, `public static`, installed `Patcher.cs:132-134`, body `:376-411`.

- **Prefix, not postfix**: the vehicle is fully intact at prefix time, so speed/altitude/crew/mass
  reads are valid (`Patcher.cs:129-130`).
- Guard `if (vehicle.IsDisposed) return;` mirrors the game's own early return at
  `KSA/Universe.cs:1701`, so a second call is a no-op rather than a duplicate RUD (`:383-386`).
- This is the game-thread **apply-side counterpart** of the worker-thread
  `VehicleUpdateTask.DetectStructuralFailure` (`KSA/VehicleUpdateTask.cs:481`), which must **never**
  be patched (`Patcher.cs:18-27`).

**Classification.** **EVENT-DRIVEN.** No debounce, no threshold.

**Dedup.** The `IsDisposed` guard plus the `Destroying` set (`Patcher.cs:392`), which also drives the
subsequent `flight.ended` reason.

**Server.** `rudFold` (`stats/boards.go:455-481`): on an unflagged flight, `addCount(rud_total, 1)`,
then `addCount(rud_<cause>, 1)` when `RUDStat(cause)` yields a legal key. A cause that cannot be a
stat key (empty, > 40 chars, bad charset, or colliding with a fixed key) contributes to `rud_total`
**only**. Feed: `"{h} lost a vehicle to {causePhrase} on {body} at {speed} m/s"`.

**Vectors.** None.

---

### `vehicle.impact`

**Wire.** `"vehicle.impact"` (`EventTypes.cs:45`), `ver` 1, kind 1.

**Payload** — `VehicleImpactPayload`, `Payloads.cs:127-133`, built `EventFactory.cs:69-75`

| Key | Type | Units | Source |
|---|---|---|---|
| `speed_ms` | number | m/s | **Ground:** `GroundImpactEvent.ImpactVelocity` — the **closing NORMAL speed**, not total speed (`Patcher.cs:434-436`; `KSA/GroundImpactEvent.cs:9`, new in 5168 r5162, computed `KSA/ConstraintSim.cs:726-738`). **Splash:** a reconstructed `√(2E/m)` scalar, 0 when mass or energy is 0 (`Patcher.cs:465-467`) — `WaterSplashEvent` carries no velocity. **The two are indistinguishable on the wire.** |
| `energy_j` | number | J | `GroundImpactEvent.ImpactKineticEnergy` (`KSA/GroundImpactEvent.cs:7`) / `WaterSplashEvent.ImpactKineticEnergy` (`KSA/WaterSplashEvent.cs:5`) |
| `survived` | bool | — | **mod-computed** by `ImpactCorrelator` — see below |
| `launch_pad` | bool | — | `GroundImpactEvent.IsLaunchPad` (`KSA/GroundImpactEvent.cs:19`, `Patcher.cs:438`). **Always `false` for a splash** (`ImpactCorrelator.cs:48-59`). |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` |
| `crew_count` | int | count | `VehicleTelemetry.CrewCount(vehicle)` |

**Detector** — `ImpactCorrelator` (`Detect/ImpactCorrelator.cs`). Two lists, `_pending` (this frame)
and `_held` (last frame) — `:37-38`.

- `Impact(signal)` appends to `_pending` (`:45`).
- `Splash(signal)` converts to an `ImpactSignal` with `LaunchPad: false` and appends (`:52-60`).
- `Destroyed(vehicleId)` marks **both** lists (`:67-72`).
- `EndFrame()` resolves `_held`, promotes `_pending` → `_held`, clears `_pending` (`:79-85`), called
  from the `FrameBoundarySignal` case (`EventPipeline.cs:184-186`).
- `DrainFor(vehicleId)` resolves one vehicle's outstanding impacts immediately when its flight ends
  (`:98-111`); `Drain()` resolves everything at session end (`:118-129`).
- Verdict: `Survived = !Destroyed` (`:159-167`).

**The rule, exactly:** an impact seen in frame *N* is resolved at the end of frame *N+1*; a
destruction of that vehicle in frame *N* **or** *N+1* flips `survived` to false
(`ImpactCorrelator.cs:24-29`). The extra frame exists because the game applies **every** impact and
splash for a frame before **any** physics destruction (`KSA/VehicleUpdateTask.cs:410-453`), but a
*manual* destroy lands later still, in `InputEvents.ApplyInputEvents` (`KSA/Program.cs:1918`, six
lines after `Universe.ApplyVehicleSolvers` at `:1912`).

**Flight attribution has two modes.** `EventFactory.FromResolvedImpact(tracker, impact)` (`:49-50`)
*mints* a flight if needed; the explicit-flight overload (`:61-75`) does not. `EventPipeline.Flush`
uses **peek** semantics and **drops** an impact whose flight already ended, with a debug log, rather
than inventing a phantom flight with no `flight.started` (`EventPipeline.cs:160-171`).

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

**Server.** `lithobrakeFold` (`stats/boards.go:331-354`) → `biggest_lithobrake_survived`. Eligibility
`Survived && !LaunchPad && CrewCount >= 1 && SpeedMs > 0`, then `scoreable`, then — **rebuild only**
— no `kitten.kia` for the same flight within ±2.0 s of `ev.SimTime`. Value is raw `speed_ms`, no
conversion. Feed: `"{h} lithobraked at {speed} m/s on {body} — and survived"`.

**Vectors.** `batch-001.ndjson` line 4 (`survived: true`, `launch_pad: false`, `body: "duna"`).

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

**Server.** `countFold{stagings, "vehicle.staging"}` — +1 per event on an unflagged flight.
`stage_index` is decoded (`stats/payload.go:105-107`) and **unused**.

**Vectors.** None.

---

### `vehicle.docked`

**Wire.** `"vehicle.docked"` (`EventTypes.cs:51`), `ver` 1, kind 1.

**Payload** — `VehicleDockPayload`, `Payloads.cs:142-143`

| Key | Type | Source |
|---|---|---|
| `other_flight` | string \| **null** | `Tracker.PeekFlight(dock.OtherVehicleId)` (`EventPipeline.cs:237`) — **peek, never mint**, so a vehicle with no open flight yields the literal `"other_flight":null`. The Go struct is a plain `string` (`stats/payload.go:111`), so null decodes to `""`. |

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

**Server.** `countFold{dockings, "vehicle.docked"}` — +1 on an unflagged flight.

**Vectors.** None.

---

### `vehicle.undocked`

**Wire.** `"vehicle.undocked"` (`EventTypes.cs:54`), `ver` 1, kind 1.

**Payload.** The same record as `vehicle.docked`. `other_flight` = `Tracker.PeekFlight(undock.OtherVehicleId)`
where "other" is the **vehicle that split off** (`GameSignal.cs:288`, `EventPipeline.cs:242`).

**Detector.** `EventPipeline.Dispatch`, `UndockSignal` case (`:240-242`).

**Game source.** A **postfix** on `DockingPort.Undock(Vehicle oldVehicle, out PoseChange
combinedToSplit)` — `KSA/DockingPort.cs:460`, installed `Patcher.cs:207-209`, body `:610-631`. The
body is `oldVehicle.Split(...)`; the caller is `KSA/InputEvents.cs:384`. `__result is null` → return.
The split vehicle is `Track`ed — and so gets a `flight.started` — before the undock event is raised
(`:621`).

**Classification.** **EVENT-DRIVEN.**

**Server.** Decoded (`stats/payload.go:201`) but **counts nothing**. There is no `undockings` board.

**Vectors.** None.

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

**Server.** **Not decoded** (`stats/payload.go:220-222`). Stored, never folded.

**Vectors.** None.

---

### `engine.shutdown`

**Wire.** `"engine.shutdown"` (`EventTypes.cs:60`), `ver` 1, kind 1.

**Payload.** `EnginePayload` as above. `count` = `Math.Max(1, state.EngineCount)` — the count from
the **previous** observation, since the engines are now off (`PolledSignals.cs:180`).

**Detector / game source / classification.** Identical to `engine.ignition`; the falling edge of
`IsAnyEngineActive`.

**Server.** Not decoded. **Vectors.** None.

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

**Server.** Not decoded. **Vectors.** None.

---

### `kitten.eva_start`

**Wire.** `"kitten.eva_start"` (`EventTypes.cs:66`), `ver` 1, kind 1. `flight` =
`Tracker.FlightFor(eva.VehicleId)` when the signal carries a vehicle id, else `null`
(`EventPipeline.cs:252-253`). **`FlightFor` mints**, so this can create the EVA vehicle's flight ULID
before its `flight.started` exists — the one documented exception to the ordering invariant, see
[Known drift](#known-drift).

**Payload** — `KittenEvaStartPayload`, `Payloads.cs:155-157`

| Key | Type | Source |
|---|---|---|
| `kid` | string (16 Crockford) | `Ids.KittenId(installId, kittenName)` (`EventPipeline.cs:410`). **Relabelled per player by `Redact` before publication.** |
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

**Server.** Not decoded (`stats/payload.go:220-222`). **Vectors.** None.

---

### `kitten.eva_end`

**Wire.** `"kitten.eva_end"` (`EventTypes.cs:69`), `ver` 1, kind 1. `flight` = **explicitly `null`**
(`EventPipeline.cs:260`) — asymmetric with `kitten.eva_start`.

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

**Server.** Not decoded. **Vectors.** None.

---

### `kitten.tumble`

**Wire.** `"kitten.tumble"` (`EventTypes.cs:72`), `ver` 1, kind 1. `flight` = **null**
(`EventPipeline.cs:267`), even though a tumble always belongs to a specific `KittenEva` vehicle that
has an open flight.

**Payload** — `KittenTumblePayload`, `Payloads.cs:173-177`

| Key | Type | Units | Source |
|---|---|---|---|
| `kid` | string | — | `Ids.KittenId(installId, tumble.KittenName)` |
| `name` | string | — | `Ids.SanitizeName(...)`. `KittenName` is the EVA vehicle's id, i.e. the roster name (`PolledSignals.cs:167`). |
| `speed_ms` | number | m/s (tangential ground speed) | `VehicleTelemetry.GroundSpeedMs(vehicle)` (`:168`) |
| `body` | string | — | `VehicleTelemetry.BodyOf(vehicle)` |

**Detector** — `PolledSignals.PollVehicle` (`:162-169`):

```
if (now.Locomotion == LocomotionMode.Tumbling && state.Locomotion != LocomotionMode.Tumbling)
    emit TumbleSignal
```

**Transitions INTO `Tumbling` only** — a tumble ends `Tumbling → Rightening → Grounded`, so counting
transitions *out* would double-count via `Rightening`.

**Game source.**

- `VehicleTelemetry.LocomotionMode(vehicle)` (`:631-641`) → `KittenEva.LocomotionState.Mode`
  (`KSA/KittenEva.cs:20`, `KSA/LocomotionState.cs:5`, `KSA/LocomotionMode.cs:3`). `LocomotionState`
  is a get-only property returning a **struct copy**, so no reflection and no aliasing. **Churn risk
  High — the whole locomotion subsystem is new in 5168.** Six values: `Mmu, Grounded, Airborne,
  Tumbling, Rightening, Ladder`.
- `VehicleTelemetry.GroundSpeedMs` (`:655-665`) → `KittenEva.LocomotionState.GroundSpeed`
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
baseline seed emits nothing.

**Server.** `countFold{kitten_tumbles, "kitten.tumble"}` — +1 per event on an unflagged flight. **No
payload field is read at all**; the event type alone is the signal. Feed: `"{h}'s kitten {name} took
a tumble at {speed} m/s on {body}"`.

**Vectors.** None.

---

### `kitten.kia`

**Wire.** `"kitten.kia"` (`EventTypes.cs:75`), `ver` 1, kind 1. `flight` = **null**
(`EventPipeline.cs:275`).

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
- Context: a **prefix** on `Vehicle.KillCrew()` — `KSA/Vehicle.cs:2796`, installed `Patcher.cs:180-182`,
  body `:546-565`, calling `runtime.NoteManualDestroy(simT)`. `KillCrew` has **exactly one caller**,
  `KSA/InputEvents.cs:515`, guarded by `if (!Recovered)` — i.e. exclusively the player-initiated
  destroy path. It is therefore a **player-intent marker, not a fatality signal**. The physics RUD
  path calls `EndAllCrewMissions` and never touches `Kia`.
- Polled at **2 Hz**.

**Classification.** **PASSIVE** (2 Hz roster diff). Gates: baseline emits nothing; rising edge only;
a 2.0 sim-second proximity window decides `context`.

**Dedup.** The `_kia` dictionary keyed by roster name is the latch; since `Kia` is never reset by the
game, the rising edge can fire at most once per kitten per session.

**Server.** No board counts it directly. It feeds **rebuild pass 1**, which builds
`kia map[flightID][]simT` from `kitten.kia` events that carry a flight and a sim time
(`projector/rebuild.go:155-157`) — and that map is what disqualifies a
`biggest_lithobrake_survived` claim within ±2.0 s. Feed: `"{h} said goodbye to kitten {name}"`.

**Vectors.** None.

---

### `roster.snapshot`

**Wire.** `"roster.snapshot"` (`EventTypes.cs:78`), `ver` 1, **kind 1 (scoring, never pruned)** —
called out explicitly at `EventTypes.cs:136-139` because it carries kitten totals that move boards.
`flight` = **null** (`EventPipeline.cs:287`).

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

**Server.** `distanceFold` (`stats/boards.go:551-577`) is the **sole board fold with no flag
exclusion** — this event carries no flight, so `scoreable` returns true unconditionally, and it
cannot be otherwise (PROJ-001). Every kitten row with a non-empty `kid` is upserted via
`b.UpsertKitten`, and **every running total merges with `max()`** (`batch.go:637-642`): a snapshot
arriving out of order, or an earlier save reloaded, can fail to advance a total but never rewind one.
`distance_travelled` = `Σ max(travelled_m)` over the player's kittens, written with `setValue`.

**Vectors.** None.

---

### `telemetry.window`

**Wire.** `"telemetry.window"` (`EventTypes.cs:81`), `ver` 1, **the only kind-0 (passive, droppable)
type** (`EventTypes.cs:141-142`). `flight` = `tracker.FlightFor(window.VehicleId)`.
**`sim_t` = `window.Payload.T1Sim`**, the sim time of the window's *last sample*, not the emission
instant (`EventFactory.FromWindow`, `:32-40`) — which is why in-session emission is slightly out of
order by design.

**Payload** — `TelemetryWindowPayload`, `Payloads.cs:228-247`. `agg` = `{"min", "max", "mean", "last"}`
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
| `peak_g` | number | g | **YES — omitted entirely** when no sample carried a reading | `[JsonIgnore(WhenWritingNull)]`, `Payloads.cs:238`; fold `WindowAccumulator.cs:140-141` |
| `max_q_pa` | number | Pa | **YES — omitted** under the same rule | `Payloads.cs:241`; `WindowAccumulator.cs:142-143` |
| `mass_kg_last` | number | kg | no | mass at the **end** of the window |

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
(`:129-180`), one per live vehicle per due tick, from `CatlogRuntime.SamplePass` (`:484-490`). **A
vehicle whose read throws is omitted from the frame, never zero-filled** (`:174-179`) — a zeroed
snapshot fed to a prev/curr comparator manufactures phantom SOI changes (`body → ""`) and phantom
orbit-achieved edges (`ecc → 0`), and both of those score.

**Classification.** **PASSIVE** — this is the archetype. 2 Hz sampling, 30 sim-second aggregation
window, no debounce.

**Server.** Three boards read it: `peak_g_survived` (`*p.PeakG > 0`, a **`*float64`** so absent ≠
zero), `fastest_surface_speed` (`surface_speed_ms.max`) and `fastest_orbital_speed`
(`orbital_speed_ms.max`).

**Vectors.** `batch-001.ndjson` line 3 — the only vector exercising `agg` objects and the (present)
`peak_g` / `max_q_pa` keys. `n: 60`, `t0_sim: 100.5`, `t1_sim: 130.5` — exactly a 30 s window at 2 Hz.

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

**Fold order** (`stats/fold.go:66-116`):

```
flight_state → career → biggest_lithobrake_survived → peak_g_survived → fastest_surface_speed
→ fastest_orbital_speed → kitten_tumbles → rud_total(+rud_<cause>) → orbits_achieved → soi_bodies
→ dockings → stagings → kittens_recovered → distance_travelled → fastest_to_orbit
→ fastest_to_body → census
```

Order matters in exactly two places: `flightFold` must precede every board (the flag check), and
`soiFold` must precede `toBodyFold` (row existence for `LowerBodyTime`).

### `stats.Batch` — the write-back accumulator

`stats/batch.go:60`. In-memory read-through caches (`flights`, `careers`, `bodies`, `kittens`,
`values`) plus per-`statKind` write accumulators, flushed as multi-row statements
(`DefaultFlushRows = 500`). Flush order is fixed and key-sorted (`:950-961`) so a rebuild is
byte-comparable to the incremental result.

| kind | rule | `player_stat` guard | `player_stat_period` guard |
|---|---|---|---|
| `kindRecord` | strictly larger wins | `WHERE excluded.value > player_stat.value` (`:797-799`) | `WHERE excluded.value > …` (`:811-813`) |
| `kindBest` | strictly smaller wins | `WHERE excluded.value < …` (`:800-802`) | `WHERE excluded.value < …` (`:814-816`) |
| `kindCount` | `value = value + excluded.value` (`:803-804`) | same (`:817-819`) | |
| `kindSet` | replace outright, `WHERE excluded.value <> player_stat.value` (`:805-807`) | **no period form** — `setValue` writes its *delta* through `periodAdd` instead (`fold.go:180-182`) | |

**Tie semantics.** Because record/best replace only on a **strict** inequality, an equal value leaves
the earlier `updated_seq`: *whoever got there first keeps the rank* (`stats/doc.go:30-33`).

### The two projection tables

**`player_stat`** — `migrations/projections/0001_init.sql:20-26`:
`player_stat(player_id, stat, value REAL, context TEXT /*JSON*/, updated_seq, PRIMARY KEY(player_id, stat))`
+ `INDEX stat_rank(stat, value)`. Because the PK is `(player_id, stat)`, `count(*) GROUP BY stat`
**is** the distinct-player count — no `DISTINCT` needed (PROJ-034).

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

---

## Boards

`stats/boards.go`. `Board` is `{Stat, Title, Unit, Ascending, Career}` (`:30-46`). **`Unit` is a
label, never a conversion factor.** `Career` marks a value that is a career-relative time and whose
row context carries `career`.

### The 13 fixed boards, in display order

`stats/boards.go:55-73`:

| # | key | Title | Unit | Asc | Career | Source event | Fold kind |
|---|---|---|---|---|---|---|---|
| 1 | `biggest_lithobrake_survived` | Biggest Lithobrake Survived | `m/s` | no | no | `vehicle.impact` | record (max) |
| 2 | `peak_g_survived` | Peak G Survived | `g` | no | no | `telemetry.window` | record (max) |
| 3 | `fastest_surface_speed` | Fastest Surface Speed | `m/s` | no | no | `telemetry.window` | record (max) |
| 4 | `fastest_orbital_speed` | Fastest Orbital Speed | `m/s` | no | no | `telemetry.window` | record (max) |
| 5 | `kitten_tumbles` | Kitten Tumbles | `tumbles` | no | no | `kitten.tumble` | count |
| 6 | `rud_total` | Rapid Unscheduled Disassemblies | `RUDs` | no | no | `vehicle.rud` | count |
| 7 | `orbits_achieved` | Orbits Achieved | `orbits` | no | no | `vehicle.orbit` | count |
| 8 | `soi_bodies` | Bodies Visited | `bodies` | no | no | `vehicle.soi` | count (set-backed) |
| 9 | `dockings` | Dockings | `dockings` | no | no | `vehicle.docked` | count |
| 10 | `stagings` | Stagings | `stagings` | no | no | `vehicle.staging` | count |
| 11 | `kittens_recovered` | Kittens Recovered | `kittens` | no | no | `flight.ended` | count (+crew) |
| 12 | `distance_travelled` | Distance Travelled | `m` | no | no | `roster.snapshot` | set (derived total) |
| 13 | `fastest_to_orbit` | Fastest to Orbit | `ms` | **yes** | **yes** | `vehicle.orbit` | best (min) |

### The two dynamic families

`stats/boards.go:136-148`. **There is no allow-list.** A key exists because a name appeared in the
data.

| prefix | listed under | Title | Unit | Asc | Career |
|---|---|---|---|---|---|
| `rud_` | `rud_total` | `"RUDs — " + titleize(cause)` | `RUDs` | no | no |
| `fastest_to_` | `fastest_to_orbit` | `"Fastest to " + titleize(body)` | `ms` | **yes** | **yes** |

Key construction: `FastestToStat(body)` / `RUDStat(cause)` → `familyStat(prefix, value)` (`:152-172`).
`statSuffix` (`:185-200`) lowercases, then requires `[a-z0-9]` first and `[a-z0-9._-]` thereafter,
length ≤ `MaxStatSuffixLen = 40`. A key that would collide with a **fixed** key is refused
(`:165-170`) — a body literally named `orbit` cannot land on `fastest_to_orbit`. **A rejected name
keeps every other consequence**: it still counts towards `soi_bodies` / `rud_total` and still records
`player_body.first_sim_t`.

`titleize` (`:207-220`) splits on `_ - .` and capitalises each word — derived, never a lookup table
(PROJ-036). `Describe(stat)` (`:228-246`) is a **pure function of the key**. `Known(stat, players)`
(`:256-265`): a fixed board is always servable; a family board is servable once `players > 0`.
`Catalog(counts, minPlayers)` (`:279-311`) lists fixed boards always, in table order, and family
members with `count >= minPlayers` sorted by key, inserted directly under their parent.
`DefaultMinPlayers = 2`, config key `[boards] min_players`.

### Fold detail, board by board

**`biggest_lithobrake_survived`** — `lithobrakeFold`, `:331-354`. Reads `vehicle.impact`
{`speed_ms`, `energy_j`, `survived`, `launch_pad`, `body`, `crew_count`}. Eligibility:
`Survived && !LaunchPad && CrewCount >= 1 && SpeedMs > 0`, then `scoreable`, then — **rebuild only**
— no `kitten.kia` for the same flight within ±2.0 s of `ev.SimTime` (`KIAWindowSeconds = 2.0`,
`fold.go:61`). Value: raw `speed_ms` in m/s, no conversion, rounding or clamping. Context
`{"body", "flight", "energy_j"}`.

**`peak_g_survived`** — `peakGFold`, `:364-394`. Reads `telemetry.window.peak_g`, a **`*float64`**
(`stats/payload.go:162`) — absent ≠ zero. Eligibility: `p.PeakG != nil && *p.PeakG > 0`, flight
unflagged, and **rebuild only** `st.Recovered()` i.e. `flight_state.ended_reason == "recovered"`.
Value `*p.PeakG` in `g`. Context `{"body", "flight", "t1_sim"}`; `t1_sim` is **seconds**.

**`fastest_surface_speed` / `fastest_orbital_speed`** — `speedFold{stat, surface}`, `:402-430`,
registered twice. Reads `telemetry.window.surface_speed_ms.max` or `orbital_speed_ms.max`.
Eligibility: `value > 0`, flight unflagged. No rebuild refinement. Context
`{"body", "flight", "t1_sim"}`. **Explicit anti-pattern**: these must **never** be sourced from
`roster.snapshot.fastest_ms`, which is the game's ecliptic-frame `FastestSpeed` and reads ~30 km/s
standing still on Earth (`:398-401`).

**The counter boards** — `kitten_tumbles`, `dockings`, `stagings`, `orbits_achieved`, `rud_total`,
`rud_<cause>`, `kittens_recovered`, `soi_bodies` — all use `addCount`, whose `context` argument is
`nil`, so `player_stat.context` is SQL NULL and `BoardRow.Context` is omitted from JSON. Their
`updated_seq` becomes the seq at which the counter reached its current value, so the tie-break is
*whoever got to N first*.

- `kitten_tumbles`, `dockings`, `stagings`: `countFold` on the event type alone.
- `orbits_achieved`: `vehicle.orbit` with `phase == "achieved"` only; `escaped` counts nothing.
- `rud_total` / `rud_<cause>`: see [`vehicle.rud`](#vehiclerud).
- `kittens_recovered`: `flight.ended` with `reason == "recovered" && crew_count >= 1` →
  `addCount(+float64(CrewCount))`. **It adds the crew count, not 1.**
- `soi_bodies`: `b.AddBody(...)` reports whether the `player_body` row was new; only then +1. No
  `count(*)`, correct under replay (PROJ-011).

**`distance_travelled`** — `distanceFold`, `:551-577`, the only `kindSet` board and the only board
with **no flag exclusion** (its source event carries no flight). Value = `Σ max(travelled_m)` over the
player's kittens (`batch.go:651 KittenDistance`), written with `setValue` when `> 0`. Unit `m`,
SI-scaled at render (`1.82 Mm`). `setValue` reads the previous value first so the window contribution
is the **increase**. The `kindSet` guard `WHERE excluded.value <> player_stat.value` means a
recomputation of the same total leaves `updated_seq` alone.

**The career-time boards** — `fastest_to_orbit` and the `fastest_to_<body>` family. The shared rule
(`:579-604`) is *the smallest `sim_t` at which an unflagged flight of this player reached the
milestone*, taken **per player, not per career**. `careerTime` (`:608-617`) requires all of:
`ev.HasCareer()`; `ev.HasSimTime && ev.SimTime >= 0` (absent is not zero); and `scoreable`.

`careerMillis(seconds) = seconds * 1000` (`:628`). **This is the other half of the `_ms` trap**: the
board *unit string* `"ms"` is milliseconds, whereas a payload key ending `_ms` is metres per second
(`units/units.go:424-432`). `sim_t` stays **seconds** on the wire and in `player_body.first_sim_t`;
only the projection value is converted (PROJ-029 / PROJ-047).

### The rewind mark

Written by `careerFold` into `career.rewound`, **never** into `player_stat.context` (PROJ-026).
Resolved at read time by `RewoundCareers(playerID, careers)` — one query per distinct player on a
board page, and none at all for a board whose `Board.Career` is false. Surfaces as
`BoardRow.Rewound` / `PlayerRow.Rewound` / `CompareRow.Rewound`, emitted only when true. The `career`
value used for the join is read from the row's own context *before* redaction, and the context is
then relabelled per player by `Redact`. **The mark excludes nothing and scores nothing.**

### Number formatting

`server/internal/units` is **the single definition of a formatted catlog number**; `spa/src/ui/units.ts`
is a port of it, and `Conformance` / `LabelConformance` (`units.go:468-572`) are the shared tables. A
rule change is three edits in one commit: `units.go`, its test table, and the port.

- Three significant figures: `decimals = clamp(2 - floor(log10|x|), 0, 6)`; round on the magnitude,
  re-apply sign, trim trailing zeros, group in threes with a canonical `,`.
- `m`, `J`, `Pa` scale by SI prefix; no sub-unit prefixes. **`m/s` never scales.**
- `s` and `ms` render as a **duration** — `ms` divides by 1000 first, then the ladder
  (`450 ms` / `37.5 s` / `5m 13s` / `1h 01m` / `243d 01h` / `1y 5d`; a year is 365 days flat).
- Any other unit (including `g` and every counter label) is three-sig-figs + `" " + unit`.
- `ForKey` (`:433-459`) is the only thing that knows `_ms` = m/s while the board unit `"ms"` =
  milliseconds. Longest suffix first, so `_ms2` is not read as `_ms`. Exact keys `sim_t` / `t0_sim` /
  `t1_sim` → seconds; `ecc`, `n`, `part_count`, `crew_count`, `missions`, `stage_index` → unitless.

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

### `flight_state`

`0001_init.sql:34-39`: `flight_state(flight_id BLOB PK, player_id, flags INTEGER DEFAULT 0,
ended_reason, crew, body, started_seq)`.

Flag bits (`stats/flight.go:12-30`): 0 `teleport`, 1 `refuel`, 2 `resource_edit`, 3 `console`,
4 `tuning`, **5 `other`** for an unrecognised value (PROJ-002).

`flightFold` (`:92-127`) runs first in every list. **Every** flight-bearing event creates the row
(`EnsureFlight`), not only `flight.started` — a batch may fold `flight.flagged` before the
`flight.started` it belongs to.

Consumers: `scoreable` (`stats/fold.go:205-220`) — events with **no flight** are scoreable, a missing
row is treated as unflagged, otherwise `st.Flags == 0`; `Recovered()` for `peak_g_survived`'s rebuild
refinement; `Projections.FlaggedFlights` so the raw-event read views drop every row belonging to a
flagged flight (PROJ-051).

### `career`

`0002_career.sql:9-21`: `career(player_id, career TEXT, max_sim_t REAL DEFAULT 0,
rewound INTEGER DEFAULT 0, first_seq, PRIMARY KEY(player_id, career))`. `careerFold`
(`stats/career.go:60-81`) — no `career` key → nothing; `career` but no `sim_t` → `EnsureCareer` only;
`session.started` → `MarkRewound` **before** advancing, and only when the career already exists and
`max_sim_t > sim_t`; then `AdvanceCareer` raises the high-water mark.

### `player_body`

`player_body(player_id, kind, body, first_seq, first_sim_t, PRIMARY KEY(player_id, kind, body))`.
`kind` is `'soi'` today. `first_sim_t` is **seconds**, NULL when the arrival event carried no career
or no clock. Recorded for **every** body, including ones with no board of their own. Read surface is
aggregate counts only — there is no per-body endpoint; the `fastest_to_<body>` board is the readable
form, in ms.

### `kitten`

`kitten(player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq,
PRIMARY KEY(player_id, kid))`. Written only by `distanceFold` → `UpsertKitten`; every numeric column
merges with `max()`, `name` is overwritten with the latest. `fastest_ms` here is the game's
**ecliptic-frame** `FastestSpeed` and must never become a speed board. Read surface: a count only.

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
| `kitten.tumble` | — | `"{h}'s kitten {name} took a tumble at {speed} m/s on {body}"` |
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
| A flagged flight scores nothing — every board, including counters | `scoreable` → `flight_state.flags == 0` | `stats/fold.go:205-220`; PROJ-001 |
| `distance_travelled` is exempt (its event carries no flight) | `!ev.HasFlight()` → true | `stats/fold.go:206-208` |
| An **unknown** flag value still excludes | `FlagOther`, bit 5 | `stats/flight.go:29,34-48`; PROJ-002 |
| Launch-pad impacts never score | `!LaunchPad` | `stats/boards.go:337` |
| Crewless impacts never score | `CrewCount >= 1` | `stats/boards.go:337` |
| An impact within 5 s of a teleport is not recorded at all | `Vehicle.IsImpactFxSuppressed()` | `Patcher.cs:423-424,455-456` |
| An impact whose vehicle died in frame *N* or *N+1* is `survived: false` | `ImpactCorrelator` | `ImpactCorrelator.cs:24-29` |
| A manual destroy also flips `survived` | `EndFlight` tells the correlator first | `EventPipeline.cs:379-380` |
| A lithobrake within ±2 s of a `kitten.kia` (rebuild only) | `b.KIANear` | `stats/boards.go:344-348` |
| `peak_g_survived` requires the flight ended `recovered` (rebuild only) | `st.Recovered()` | `stats/boards.go:386-388` |
| Absent `peak_g` ≠ 0 g | `*float64` + omit-don't-zero on the wire | `stats/payload.go:162`; `Payloads.cs:238` |
| Banned players are invisible on every read surface | **absent from the in-memory directory**, so no handle resolves | PROJ-007 |
| Banned rows are still counted in `count` / `players` | unfiltered row counts, by design | PROJ-008 |
| Rank compensates for banned rows ahead | `StatAhead - StatsForPlayers(banned)` | `readapi.go:446-471` |
| Unknown / retired / banned handle → one 404 | not a ban oracle | `readapi.go:428-431` |
| A feed line needs a handle **and** an unflagged flight | `Summarize` | `stats/feed.go:21-31` |
| Raw event views drop flagged flights and handle-less players | `FlaggedFlights` + the directory | PROJ-051 |
| Install-derived identifiers are never published raw | `Redact` / `Label` | `readapi/privacy.go` |
| A family board is withheld from the public index below `min_players` | listing rule only — the value is still stored, the board is still served, the profile still shows it | PROJ-034 / PROJ-035 |
| Purge deletes from `events.db`; projections follow only on rebuild | `tombstone` | `migrations/events/0001_init.sql:82` |

---

## Rebuild ≠ incremental

`projector/rebuild.go:71`. A rebuild builds into `projections.rebuild.db` from seq 0, then atomically
swaps (the old file is kept as `<path>.old` until reopen succeeds — PROJ-012).

- **Pass 1** (`:139`) applies `StateFolds()` only (`flight_state`, `career`) over the whole log and
  builds `kia map[flightID][]simT` from `kitten.kia` events carrying a flight and a sim time.
- **Pass 2** (`:170`) uses `stats.NewRefinedBatch(tx, kia, …)`, applies `SecondPassFolds()` (boards +
  census) against a `flight_state` already complete for all history, and re-renders feed rows.
- Nothing is broadcast from a rebuild.

Refinement is carried on the `FlightStateReader`: `Refined()` is false incrementally, and `KIANear`
always answers false then. **This is D22, not a bug.** The divergences, exhaustively:

1. **A late `flight.flagged`.** Incrementally, events folded before the flag arrived already scored;
   a rebuild sees the completed `flight_state` on pass 2 and drops them all (PROJ-004).
2. **The `biggest_lithobrake_survived` ±2.0 s KIA window** — applied only when `b.Refined()`.
3. **`peak_g_survived`'s `ended_reason == 'recovered'`** — applied only when `b.Refined()`. This is
   the **broadest** divergence: every `destroyed` / `despawned` / still-open flight loses its
   `peak_g` row on rebuild.
4. **Feed rows.** `feedRow` resolves the handle from the *live* directory at fold time, and rebuild
   pass 2 re-renders them. A player banned since the events were folded therefore keeps feed rows
   incrementally (until the 500-row cap ages them out) but produces none on rebuild.
5. **Undecodable events.** A build that gained a decoder folds on rebuild what it skipped before.

Things that deliberately **do not** diverge: rolling-window buckets (derived from `ev.RecvTime`, never
the wall clock — PROJ-043), retention trims (gated on `ev.Seq % 512`), and the census.

---

## Conformance coverage

`contracts/testdata/` is generated by `catlogctl testvectors generate <dir>` and consumed by
`mod/catlog.lib.tests/Conformance/ContractVectorTests.cs`.

| File | Pins |
|---|---|
| `batches/batch-001.ndjson` | 5 envelopes, one line each |
| `batches/batch-001.br` | the Brotli body as sent |
| `batches/batch-001.bh.txt` | `mPBWDmLPC5QrbKto5gJrxmJ3v1tln9l3UGRw0n4ZBHM` — base64url SHA-256 of the compressed body |
| `keys/*`, `license/*`, `proofs/*`, `expected/verify-results.json` | the credential / JWS layer, not events |

**Covered by a vector: 5 of 22** — `session.started`, `flight.started`, `telemetry.window`,
`vehicle.impact`, `flight.ended`.

**Uncovered: 17** — `flight.flagged`, `vehicle.situation`, `vehicle.atmosphere`, `vehicle.orbit`,
`vehicle.soi`, `vehicle.rud`, `vehicle.staging`, `vehicle.docked`, `vehicle.undocked`,
`engine.ignition`, `engine.shutdown`, `engine.flameout`, `kitten.eva_start`, `kitten.eva_end`,
`kitten.tumble`, `kitten.kia`, `roster.snapshot`.

Vector-level assertions that apply to every line regardless: every `type` is in the registry, every
`id` parses as a ULID, the `flight` key is always present, `session` is non-empty, and each line is
within `Wire.MaxEventLineBytes` (`ContractVectorTests.cs:145-164`).

---

## Known drift

Recorded here rather than silently fixed, so the next pass has a work list. Each item is either a
document that disagrees with the code, or behaviour no document states. **Nothing here is a claim
that the code is wrong.**

### Documents that disagree with the code

1. **`docs/events.md:81` — `vehicle.impact.survived`.** Says "no destruction in the **same frame**".
   The code holds an impact for **one full frame**: frame *N*'s impact resolves at the end of frame
   *N+1*, and a destruction in either frame flips the verdict. `docs/mod.md:96-100` states it
   correctly.
2. **`docs/events.md:83` — `other_flight` is typed as a ULID.** It is nullable and `"other_flight":null`
   is a legal emitted shape. The Go struct is a plain `string`, so null silently decodes to `""`.
3. **`docs/events.md:18` — `flight` is "null for session/roster events".** `kitten.eva_end`,
   `kitten.tumble` and `kitten.kia` also emit null, while `kitten.eva_start` emits non-null.
4. **`docs/events.md:89` — `roster.snapshot` "every 10 min of play".** The 600-second interval is
   compared against **sim** time, so under time warp snapshots come far more often in wall time. And
   "on session end" means **process unload only**; a save-load boundary emits no closing roster.
5. **`docs/events.md:91` — `telemetry.window` "one per vehicle per 30 s".** There are four close
   paths; three of them produce a short window (`n < 60`).
6. **`docs/events.md:64` — the `situation` list is missing `"unknown"`**, which is emittable.
7. **`docs/ingest-api.md:277` — the career-board value is "seconds since the career began".** The
   fold multiplies by 1000 and the unit string is `"ms"` (PROJ-047). The same stale phrase appears in
   `store/projections.go:76`, `stats/fold.go:133-134` and `spa/src/api/types.ts:36`.
8. **`docs/ingest-api.md:217-218` — response shapes are missing published fields**: `periods` on the
   board index, and `title` / `unit` / `period` / `bucket` / `limit` / `offset` on the board page.
9. **`docs/ARCHITECTURE.md:51-52` and `docs/CONSTITUTION.md:70-72` still claim stat keys are
   compile-time constants and enums are allow-lists.** Superseded by PROJ-033 / PROJ-037;
   `docs/integrity-audit.md:54` has already been corrected, these two have not.
10. **`stats/fold.go:165-166` says `setValue` serves `soi_bodies`.** `soiFold` uses `addCount`
    (PROJ-011); `setValue` has exactly one caller, `distanceFold`.
11. **`docs/server.md:252-266` lists three sources of rebuild≠incremental divergence.** Feed-row
    handle resolution is a fourth.
12. **`docs/server.md:224-226` lists the fixed board keys but not their titles or units**, so that
    canonical table existed only in `stats/boards.go` until this document.

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
23. **Six event types are stored but read by nothing**: `vehicle.atmosphere`, all three `engine.*`,
    and both `kitten.eva_*`. All are kind 1, so they are never pruned from the outbox and always cost
    batch slots. `vehicle.situation` and `vehicle.undocked` are decoded but fold into nothing.
24. **The conformance vector is not byte-representative of mod output.** It is Go-generated and
    alphabetises payload keys; the C# mod emits declaration order. Harmless for `bh`, which hashes
    whatever bytes are actually sent, but nothing says so.
25. **`Board.Career` carries a `json:"career"` tag but is exposed in no response struct.** Clients
    infer "career board" from `ascending` + `unit == "ms"` + the presence of `rewound`.
