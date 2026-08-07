# catlog — Proposals

Companion to [INITIAL_OUTLINE_PLAN.md](INITIAL_OUTLINE_PLAN.md). Produced from four parallel research threads (2026-08):

1. **KSA decompiled sources** (`ksa-game-assemblies/current/decomp`, build 2026.7.3.4826) — what events/telemetry are actually detectable and the exact patch points.
2. **gatOS + unscience** — proven runtime-readable KSA surface, threading/marshaling patterns, and existing code catlog should copy.
3. **Identity/auth** — Discord OAuth2 verified against current docs, credential-format tradeoffs, privacy critique of the email-hash key.
4. **Backend/DB/hosting** — Turso capabilities from the local skill + current docs, event-store design, verified Aug-2026 pricing.

Organized as seven proposals (one per area of concern), a phased roadmap, and open decisions. Each proposal has a primary recommendation; alternatives are noted inline.

---

## Research verdicts that change the initial plan

Three ideas in the outline don't survive contact with the research; flagging them up front since everything below builds on the revised versions:

| Outline idea | Verdict | Replacement |
|---|---|---|
| PK = `sha256(lowercase(email))` | **Reject.** Email space is dictionary-enumerable → the hash is reversible from a DB leak, and *anyone* can compute a target's key and probe the API ("does this person play KSA?"). Worse: player changes email at the IdP → new key → **ban silently evaporates**; recycled emails inherit bans. | `user_key = HMAC-SHA256(pepper, "discord:" + snowflake)`. Server-held pepper blocks outsider computation; Discord snowflakes are stable and never recycled, so bans stick to the account. Email is **never received at all** (don't even request the `email` scope). See Proposal 3. |
| X.509 client certs + mTLS at ingest | **Reject.** Cloudflare only supports bring-your-own-CA mTLS on Enterprise plans (verified) — a catlog mini-CA can't terminate at the edge on a small budget, and CA ops (custody, CRL/OCSP, reissue churn) is heavy for one person. mTLS also authenticates the channel, not the payload — no per-batch audit trail. | Client-generated P-256 keypair + server-signed proof-of-possession license JWS (DPoP-style). Plain HTTPS + headers → CDN-indifferent, stateless verification, per-batch non-repudiation. See Proposal 3. |
| CDN well-known index + immutable global chunk log as the read-distribution path | **Demote.** The website needs *projections*, not the raw log — making consumers replay an ever-growing log is strictly worse than serving a 50 KB precomputed JSON. Fatal conflict: globally interleaved immutable chunks make "trivially purge a banned player" require re-compacting every chunk they ever touched. | CDN-cached read API/pages (`s-maxage` + stale-while-revalidate on Cloudflare free) as the serving path. Immutable chunks survive as a **per-player** cold archive on R2 (purge = delete one prefix) for DR, open-data export, and disk capping. See Proposals 5–6. |

Also confirmed (good news): Discord is OAuth2-only — no OIDC/id_token — so the login flow is code-exchange + `/users/@me`, strictly server-side. And the "conflict-free per-player log" intuition in the outline is exactly right — with client-minted event ULIDs and a unique `(player, event_id)` index, multi-server convergence is a set union; no CRDT machinery needed.

---

## Proposal 1 — The game mod

### 1.1 Shape

A **standalone StarMap mod** (`catlog` + `catlog.lib` two-project split, .NET 10 / C# 13 like the sibling repos). Do **not** depend on gatOS at runtime — it drags a QEMU/VM-sized mod as a prerequisite and exports no small telemetry contract; its value is patterns, not binaries. Copy, don't import.

The direct ancestor is **`unscience/steely-eyed-missile-kitten`** (~2,000 lines): passive multi-vehicle sampling, debounced edge-detection events, SQLite event buffering. catlog's mod is essentially SEMK with the ImGui/mission layers swapped for an HTTP shipper.

```
catlog.lib/
  Telemetry/     VehicleTelemetry.cs      ← ALL KSA reads co-located here (SEMK pattern)
                 TelemetrySnapshot.cs     ← immutable record per vehicle per sample
  Events/        EventDetector.cs         ← stateless prev/curr comparator + debounce
                 FlightTracker.cs         ← flight/session identity (§1.4)
  Outbox/        OutboxDb.cs              ← SQLite write-ahead spool (§1.5)
  Shipper/       BatchShipper.cs          ← HTTP client, sign + POST + backoff (§1.5)
  Auth/          Credential.cs            ← license JWS + P-256 key load, ES256 signer
catlog/
  Mod.cs, Patcher.cs, mod.toml            ← lifecycle + Harmony patches (§1.3)
```

Disciplines copied from gatOS (they exist because they were earned):

- **KSA types in exactly one folder** (`Telemetry/` + `Patcher.cs`); everything else game-free and unit-testable.
- `[KsaAnchor]`-style attribute on every KSA touch so a decomp/game bump fails the build *at* the coupling site.
- Per-vehicle try/catch in the sample loop (a mid-teardown vehicle must not kill the tick); one-log-then-**dead-latch** for whole subsystems (a broken shipper disables itself for the session, never spams, never crashes a frame).
- `SnapshotStore` (volatile-swap immutable snapshot + `WaitForNextAsync`) to hand data from the game thread to the shipper thread with zero locks — copy the 57-line file from `gatOS.SimFs/Snapshots/SnapshotStore.cs`.
- `SampleClock` drop-not-backfill rate limiting; NaN/Inf scrubbing (`Sanitize.cs`); the `GC.GetAllocatedBytesForCurrentThread()` alloc tripwire; `PerfStat` alloc-free timing surfaced in a small status window.
- `HotkeyGuard` from ksa-abstractions if the mod grows any ImGui text input.
- TOML config via Tomlyn (snake_case, atomic temp+move writes, load-never-throws), data under `Documents/My Games/Kitten Space Agency/mods/catlog/`.

### 1.2 Sampling loop

- All game reads on the game thread in `[StarMapBeforeGui]`; passive channels sampled at **2 Hz default** for all vehicles (SEMK cadence) with a configurable burst rate (up to ~10 Hz) for the controlled vehicle only.
- Event detection is **poll + snapshot-diff** wherever possible (cheap, drift-proof) and Harmony **only** where polling can't see the moment (destruction, impacts, staging, docking — §1.3).
- Idle gate: no credential configured, or player opted out → the sampler does zero work.
- High-rate G/impact context: a small preallocated struct ring buffer (geeforce pattern — `GForceRecorder` keeps 40 Hz body-frame g with O(1) running stats) for the controlled vehicle, so record-class events can attach a short high-frequency trace (§4.4) without allocating in steady state.

### 1.3 Event detection — patch points (verified against decomp build 2026.7.3.4826)

Polled (snapshot-diff on `Vehicle.Situation` + derived flags, 2 s debounce — SEMK's `EventDetector` ruleset):

| Event | Detection |
|---|---|
| Liftoff / Landed / Splashdown | `Situation` transitions (`Landed`, `Rolling`, `Floating`, `Sailing`, contact flags via `SituationEx`). Note: current build has **8** situation values (`Dragging`, `Bottomed` are new vs the skill docs) — use the `SituationEx` helpers, not exhaustive switches. |
| Atmosphere entered/exited | derived: `GetBarometricAltitude()` vs `Parent.GetAtmosphereReference().Physical.Height` |
| Stable orbit / orbit escaped | periapsis-altitude crosses atmosphere height with `Eccentricity < 1` / ecc crosses ≥ 1 (NaN-guard everything; Ap/Pe are radii) |
| SOI change | `vehicle.Parent.Id` diff |
| Engine ignition/shutdown/flameout | `Parts.RocketNozzles` state: `ActivatedThisFrame`/`DeactivatedThisFrame`; flameout = deactivation while controller `IsActive` |
| Kitten tumble ("did NOT land on feet") | `((KittenEva)v).LocomotionState.Mode` — count transitions **into `LocomotionMode.Tumbling`**. The game itself classifies this: terrain contact at tangential speed ≥ 6.5 m/s ⇒ Tumbling; feet-first goes Airborne→Grounded directly. Public property, no reflection. |

Harmony-patched (all game-thread methods; resolve via `AccessTools` with null-check + patch-time logging, since decomp drift is real):

| Event | Patch | Notes |
|---|---|---|
| **RUD with cause** | prefix `Universe.DestroyVehicleFromEvent(Vehicle, VehicleDestructionEvent)` (static, public) | `VehicleDestructionCause` has **6 causes** — `GroundImpact, OceanImpact, Collision, ExcessiveGForce, AerodynamicForces, HydrodynamicForces` — richer than the plan hoped. Event carries `PeakGLoad`, `PeakDynamicPressure`; vehicle still intact in the prefix → read speed, position, `Crew`, mass at the moment of death. |
| **Lithobrake** | postfix `GroundImpactEvent.Apply(Vehicle)` | Carries `ImpactVelocity` (m/s closing speed), `ImpactKineticEnergy`, `IsLaunchPad`. Fires for *every* hard ground contact, destructive or not, and **impacts apply before destructions in the same frame batch** (verified in `VehicleUpdateTask.ApplyRenderEventsToVehicles`) → "survived lithobrake at N m/s" = impact postfix with no destruction prefix for the same vehicle Id that frame. Reject records when `IsImpactFxSuppressed()` (5 s post-teleport) or `IsLaunchPad`. Water analog: `WaterSplashEvent` (`ImpactKineticEnergy`; v ≈ √(2E/m)). |
| Any vehicle removal | postfix `Universe.DestroyVehicle(Vehicle)` (static) | Catch-all: RUD, manual destroy, recovery. |
| Recovery (flight end) | postfix `Vehicle.Recover()` | Manual-destroy-vs-recover distinguished by `VehicleDestroyData.Recovered`. |
| Kitten KIA | postfix `Vehicle.KillCrew` + roster diff `KittenRosterEntryData.Kia` | Surprising verified behavior: the **physics RUD path does NOT kill crew** (`EndAllCrewMissions`, no KIA) — only manual destroy calls `KillCrew`. Verify in-game before building "kitten survived" logic on it. |
| Vehicle created (launch/split/load) | postfix `CelestialSystem.Register(Astronomical)` filter `is Vehicle` | Single choke point for all creation paths. Deregister = removal choke point. |
| Staging | postfix `SequenceList.ActivateNextSequence(Vehicle)` | |
| Docking / undocking | postfix `DockingPort.Dock` / `DockingPort.Undock` | Dock merges vehicles (identity churn — §1.4). |
| EVA start / end | postfix private `EVADoor.CreateKittenEva` (fallback: `Register` filtered `is KittenEva`); board = postfix `Vehicle.AddCrew`/`AddCrewToFirstAvailableSeat` | EVA vehicle `Id` **is the kitten's roster name**. |
| Session boundaries | postfix `Universe.DeserializeSave`, `Universe.LoadSystem` | On load: all vehicles recreated, **sim time can jump backwards**. Never hold `Vehicle` refs across a load; track by Id. |
| Assisted-flight flags | postfix `Vehicle.Teleport`, `TeleportToLocation`, `RefillConsumables`, `InputEvents.VehicleResourcesChangeData` | Not blocked — just stamped onto the flight so leaderboards can exclude assisted runs (§4.4). |

**Never patch worker-thread methods** (`DetectStructuralFailure`, `DetectTerrainContact`, `DetectDockingEvent`) — always hook the game-thread Apply/Destroy side.

Free leaderboard fodder needing zero detection work: the game already keeps per-kitten `TravelledMeters`, `FastestSpeed`, `MissionCount`, `TotalMissionTime`, `Kia` in `Universe.KittenRoster` — snapshot it periodically.

### 1.4 Flight & session identity

KSA has **no player/account/save GUID anywhere** (verified). The mod mints its own:

- `install_id` — ULID minted once into the mod's config.
- `session_id` — ULID minted per save-load boundary (`DeserializeSave` postfix) and game start.
- `flight_id` — ULID minted per `(vehicle.Id, Vehicle.LaunchGameTime)` pair; `LaunchGameTime` is set at construction, survives save/load and is inherited by split children, so it delimits a "flight" well. Docking merges and splits are recorded as events linking the involved flight_ids rather than trying to force one continuous identity.
- Every event carries both `sim_time` (`Universe.GetElapsedSimTime()`) and wall time — sim time is not monotonic across loads.

### 1.5 Outbox & shipper (the one genuinely new piece)

Neither gatOS nor unscience has an outbound HTTP client — this is new code, but small:

- **Write-ahead outbox**: local SQLite (`Microsoft.Data.Sqlite`; call `SQLitePCL.Batteries_V2.Init()` explicitly — required in the mod environment; SEMK's `EventDatabase`/`EventWriter` are the template). Events accumulate in a locked list on the game thread, flushed to the outbox every ~5 s off-thread. The outbox is the offline buffer: play without internet, ship later.
- **Shipper thread**: wakes on outbox flush (SnapshotStore-style signal), drains up to N events into a batch (target: one batch per 15–60 s of active play, or on buffer-size threshold), assigns `batch_id` (ULID) + per-stream monotonic `seq` + previous-batch hash, signs the proof JWS (§3.4), POSTs with `HttpClient`. On 2xx: delete shipped rows. On 429/5xx/timeouts: exponential backoff with jitter, batches coalesce (telemetry is loss-tolerant by design; the outbox caps at a configurable size and drops oldest passive samples first, never events).
- Compression: `Content-Encoding: br` via in-box `BrotliStream` (zstd would drag a native dependency into the mod; Brotli is BCL).
- Clock skew: read the server `Date` header, keep a computed offset, sign `iat` with corrected time; on a `clock_skew` 401, resync and retry once.
- Consent & privacy posture: telemetry ships **only** when a credential file is present and enabled in config; a status window shows what's being collected, queue depth, last ship result.

---

## Proposal 2 — Event taxonomy & wire format

### 2.1 Envelope

Every event:

```jsonc
{
  "id":     "01J9...",          // ULID, minted client-side (dedup key server-side)
  "type":   "vehicle.rud",      // namespaced, lowercase
  "ver":    1,                  // payload schema version (upcasting server-side)
  "flight": "01J9...",          // flight_id (nullable for session/roster events)
  "session":"01J9...",
  "sim_t":  12345.678,          // Universe sim seconds
  "wall_t": 1770000000123,      // client unix ms (untrusted)
  "payload":{ ... }
}
```

### 2.2 Initial event types

| Type | Payload highlights |
|---|---|
| `flight.started` | vehicle id/name, home body, mass, part count, crew count |
| `flight.ended` | reason: recovered / destroyed / despawned |
| `vehicle.situation` | from→to situation, body, altitude, speeds |
| `vehicle.soi` | from→to body |
| `vehicle.atmosphere` | entered/exited, body, dynamic pressure peak |
| `vehicle.orbit` | achieved/escaped, Ap/Pe/ecc/inc at the moment |
| `vehicle.rud` | **cause (6-value enum), PeakGLoad, PeakDynamicPressure**, speed, altitude, body, crew aboard |
| `vehicle.impact` | ImpactVelocity, kinetic energy, survived: bool, body, IsLaunchPad |
| `vehicle.staging` / `vehicle.docked` / `vehicle.undocked` | stage index / other flight_id |
| `engine.ignition` / `engine.flameout` | engine template, count |
| `kitten.eva_start` / `kitten.eva_end` | kitten name-hash |
| `kitten.tumble` | ground speed at tumble, body — *the* "didn't land on feet" counter |
| `kitten.kia` | context |
| `roster.snapshot` | per-kitten TravelledMeters / FastestSpeed / MissionCount / TotalMissionTime (periodic, low cadence) |
| `flight.flagged` | teleport / refuel / console assist markers |
| `telemetry.window` | §2.3 |

Contract lives in `docs/events.md` in the repo — the C# mod and the Go projector must agree on every `(type, ver)`; upcasters are registered per pair server-side, stored events are never rewritten.

### 2.3 Passive channels: window client-side (the single biggest cost lever)

Raw 1 Hz acceleration/velocity from every player would 10×+ the event volume (~10 M+/day vs ~0.5–2 M/day) and is the difference between "$5/mo forever" and per-row-billing pain on any cloud alternative. **Decide this early; recommendation: window in the mod.**

`telemetry.window` (one per vehicle per 30 s of active flight): min/max/mean/last for speed (surface/orbital), altitude, accel magnitude, peak G (from the game's own EMA-filtered `StructuralLoad.PeakGLoad`), dynamic pressure, mass, plus sample count. Record-class detail comes from the ring-buffer trace attached only to record candidates (§4.4), not from the firehose.

### 2.4 Wire format

MVP: **NDJSON + Brotli** (System.Text.Json snake_case, gatOS house style; in-box on both ends; debuggable with curl). The envelope is small; Brotli erases most of JSON's verbosity. Revisit MessagePack/CBOR only if measurements say the bytes matter — server storage can transcode independently of the wire (§5.2).

---

## Proposal 3 — Identity, auth & credentials

### 3.1 Login (website)

Discord **OAuth2 authorization-code flow, strictly server-side** (confidential client + `state`; there is no Discord OIDC/id_token — verified against current docs):

1. Browser → `/login` → Discord authorize with `scope=identify` **only**. Don't request `email` — the cleanest "never store email" is *never receive email*. (This also moots the outline's email-hash requirement — there is no email anywhere in the system.)
2. Server exchanges the code, calls `/users/@me`, reads the snowflake `id`. Discard both tokens immediately — catlog needs Discord for the seconds it takes to learn the snowflake.
3. `user_key = HMAC-SHA256(pepper, "discord:" + id)` — pepper is 32 random bytes in the server's secret store, never in the DB. The `discord:` prefix namespaces future IdPs (`google:`+`sub` is the natural second — real OIDC; GitHub workable third, key on numeric id).
4. Normal short-lived signed session cookie for the site.

Free anti-abuse signal with zero PII: the snowflake encodes account-creation time (`(id >> 22) + 1420070400000`) — gate credential issuance on account age (e.g. ≥ 30 days).

Multiple IdPs per human: **don't auto-merge** (no email → no auto-merge primitive, and that's fine). Explicit account-linking flow later if demanded.

### 3.2 User keying, bans, deletion

- `user_key` is the internal partition/ban/delete anchor; the **handle is the only public identity** (unchanged from the outline).
- Ban/delete: purge all events + aggregates + archive chunks by `user_key`, keep a minimal tombstone `{user_key, reason, banned_at}` + revoked credential thumbprints in a deny-list. The tombstone holds no recoverable PII (peppered HMAC of a pseudonymous ID) and is a defensible fraud-prevention retention.
- Because the key derives from the stable IdP subject, the banned player who logs back in with the same Discord account **re-derives the same key → still banned** — the property the email hash could never deliver. Evasion requires a fresh aged Discord account (account-age gate + quotas raise that cost).

### 3.3 Credential: client keypair + PoP license (recommended)

DPoP-style, minus the OAuth server — catlog is the issuer:

1. Dashboard "new handle": **browser WebCrypto generates a P-256 keypair client-side**; only the public JWK is POSTed with the desired handle. The private key never crosses the wire at all — strictly stronger than "we don't retain it".
2. Server enforces quotas (e.g. ≤ 5 handles/user, ≤ 3 issuances/day), deny-list, handle rules (unique, reserved words, no banned-handle reuse), then signs a compact **license JWS** and stores only `{user_key, handle, jkt, iat, exp}`:

```jsonc
// header {"alg":"ES256","kid":"catlog-2026a","typ":"catlog-license+jwt"}
{
  "iss": "https://catlog.gg",
  "sub": "u_...",                          // user_key, base64url
  "handle": "whiskers_prime",
  "cnf": { "jkt": "NzbLs..." },            // RFC 7638 thumbprint of the client JWK
  "iat": 1770000000, "exp": 1785552000,    // ~180-day TTL; reissue = ban touchpoint
  "jti": "lic_01JD...", "ver": 1
}
```

3. Browser assembles `catlog-credential.json` `{handle, license, private_key (PKCS#8 PEM)}` — downloaded once, never reconstructable; player drops it in the mod config dir.

Crypto ground truth: **ES256/P-256 is fully in-box** in .NET (`ECDsa.Create(ECCurve.NamedCurves.nistP256)`; .NET's ECDSA output is already the JWS `r||s` wire format — no DER conversion). A compact-JWS signer is ~40 lines of BCL-only code, zero NuGets in the mod. **Ed25519 is NOT in the BCL until .NET 11** (dotnet/runtime#63174, api-approved) — keep `alg` agility, adopt later if desired.

Rejected alternatives: bearer JWT (the token *is* the secret, retransmitted every request; savings over PoP are marginal — acceptable only as an MVP stopgap with the same claim shape); X.509+mTLS (see verdicts table).

### 3.4 Ingest authentication

Raw compressed body + two headers; the batch is **not** wrapped in a JWS (base64url would inflate it ~33% and kill streaming compression):

```
POST /v1/ingest
Content-Encoding: br
X-Catlog-License: <compact license JWS>
X-Catlog-Proof:   <compact proof JWS>
<brotli(NDJSON event batch)>
```

Proof JWS (header carries the public `jwk`): `{jti, iat, htm, htu, bh = b64u(SHA-256(body bytes)), seq, ph = b64u(SHA-256(previous batch body))}`.

The `seq`/`ph` **hash chain per (handle, stream)** is the piece that quietly implements the outline's "distributed log" idea: uploads become a tamper-evident append-only log; duplicate `seq` → idempotent drop; gaps detectable; a *fork* (two different batches at one `seq`) is a high-signal indicator of credential theft.

Server verification, in cheap-first order: parse license → `kid` → verify with server key (cache by hash) → deny-list check (`sub`, `jkt`) → `iat` ± 300 s + `jti` unseen in window → verify proof with its embedded JWK → thumbprint(jwk) == `cnf.jkt` → alg allow-list `{ES256}` only → `htm`/`htu`/`bh` match → rate-limit by `jkt` → dedup `(sub, stream, seq)` → verify chain. Fully stateless except the tiny deny-list + replay window. Server signing keys published as a `kid`-tagged JWKS on the CDN; rotate by adding a `kid`.

---

## Proposal 4 — Ingestion API & leaderboard integrity

### 4.1 Endpoint contract

- `POST /v1/ingest` — batch, as above. Responses: `200 {accepted, deduped}`, `401 {error: clock_skew, server_time}`, `429 + Retry-After`, `413` (batch too large). Server `Date` header always present (mod clock sync).
- Idempotency at two grains: `(player, batch_id)` short-circuit (retried batch → 200 without re-parse) and `(player, event_id)` per-row `INSERT OR IGNORE`.
- Limits at the handler edge: max batch bytes, min batch interval per credential, max events/batch.

### 4.2 Rate limiting on a tiny budget

Pre-auth: Cloudflare free WAF/rate rules per-IP; invalid-signature counters → temp IP block. Post-auth: in-memory token bucket per `jkt` (single-node = trivial). Verification cost is the DoS surface — the cheap checks run before any ECDSA; ~2 verifies/request is still tens of thousands/s/core.

### 4.3 Anti-forgery: layered skepticism, no anti-cheat theater

The client is attacker-controlled; signatures prove *who*, never *that it's true*. Don't attempt client attestation (snake oil, burns solo-dev time). Layers:

1. **Server-side derivation** — every leaderboard value is *computed from the event stream*, never accepted as a claimed stat. "Biggest lithobrake" = `vehicle.impact.ImpactVelocity` where survived, cross-checked against the flight's preceding telemetry windows.
2. **Physics plausibility** — catlog knows KSA's constants: kinematic continuity between windows (∫a·dt ≈ Δv), teleport-scale position jumps, telemetry continuing after a RUD, orbit events with no ascent history. Cheap per-batch checks; flag, don't silently trust.
3. **Assisted-flight exclusion** — the mod's own `flight.flagged` events (teleport/refuel/console) exclude runs from record boards. An attacker can suppress these — which is what layers 2/4/5 are for; honest players just get correct categorization.
4. **Quarantine pipeline** — records land `pending`; inside the plausibility envelope → auto-publish; top-N candidates hold for review. Record-class claims require the attached high-frequency ring-buffer trace (§1.2) for offline re-derivation (Trackmania/speedrun.com replay-file precedent). Shadow-ban mode: quarantined users see their own data, nobody else does.
5. **Statistical outliers** — per-metric robust z-scores; suspicion multipliers for new accounts / first-upload-is-a-world-record.
6. **Quotas + community reporting** feeding the same quarantine queue.

### 4.4 Ban propagation

Deny-list = banned `user_key`s ∪ revoked `jkt`s + reason codes — a few KB at any realistic scale. Publish as a signed versioned well-known file on the CDN, polled by ingest; 180-day license `exp` is the backstop (every reissue re-checks the deny-list against the IdP-derived key).

---

## Proposal 5 — Data platform (event store, projections, Turso)

### 5.1 Engine strategy

Turso (the Rust rewrite) is **beta** —   explicitly say so — with hard constraints that shape the design: **one process per DB file** (no multi-process access, unlike vanilla SQLite WAL), no `VACUUM` (breaks `VACUUM INTO` backups), MVCC/`BEGIN CONCURRENT` experimental, encryption experimental (rotation = full rewrite), a synced DB must never be touched by any other tool, and **no C#/.NET SDK** (mod ships HTTP regardless — no change there).

Recommendation honoring the "try Turso" preference while containing the risk: **embedded Turso in-process behind a small repository interface**, with a one-line swap to vanilla SQLite (`modernc.org/sqlite`, pure Go) if beta bites. The one-process rule conveniently *mandates* the monolith we want anyway (§6.1). Skip Turso encryption-at-rest — LUKS on the VPS is the boring correct answer.

### 5.2 Event store DDL

(Amended from research to use the Proposal-3 identity model — no email hash anywhere.)

```sql
PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000; PRAGMA wal_autocheckpoint = 4000;

CREATE TABLE player (
  player_id   INTEGER PRIMARY KEY,
  user_key    BLOB NOT NULL UNIQUE,       -- 32 B HMAC-SHA256(pepper, "discord:"+snowflake)
  created_at  INTEGER NOT NULL,
  banned_at   INTEGER
);
CREATE TABLE handle (
  handle      TEXT PRIMARY KEY,           -- the only public identity
  player_id   INTEGER NOT NULL REFERENCES player(player_id),
  jkt         TEXT NOT NULL,              -- current credential thumbprint
  issued_at   INTEGER NOT NULL, expires_at INTEGER NOT NULL,
  revoked_at  INTEGER
);
CREATE TABLE event (
  seq        INTEGER PRIMARY KEY,         -- rowid: server-local total order = projector cursor
  event_id   BLOB NOT NULL,               -- 16 B ULID from the mod
  player_id  INTEGER NOT NULL,
  flight_id  BLOB, session_id BLOB,
  type       TEXT NOT NULL,
  ver        INTEGER NOT NULL DEFAULT 1,
  sim_time   REAL,
  wall_time  INTEGER NOT NULL,            -- client, untrusted
  recv_time  INTEGER NOT NULL,            -- server, trusted
  payload    TEXT NOT NULL                -- JSON (MVP; measure before switching to MessagePack BLOB)
);
CREATE UNIQUE INDEX ev_dedup      ON event(player_id, event_id);
CREATE INDEX        ev_player_seq ON event(player_id, seq);
CREATE TABLE ingest_batch (                -- whole-batch retry short-circuit
  player_id INTEGER NOT NULL, batch_id BLOB NOT NULL,
  n_events INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  PRIMARY KEY (player_id, batch_id)
);
```

One HTTP batch = one transaction (verify → batch short-circuit → per-row `INSERT OR IGNORE` → commit). Batched WAL inserts run 10k–100k rows/s even on one shared vCPU; peak need is ~2k/s worst case — 10–25× headroom. Avoid `STRICT` and `WITHOUT ROWID` (Turso compatibility).

### 5.3 Projections

Separate `projections.db` (own file → could even be a second process later, per the one-process rule):

```sql
CREATE TABLE proj_checkpoint (projection TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE player_stat (
  player_id INTEGER NOT NULL, stat TEXT NOT NULL,
  value REAL NOT NULL, context TEXT,          -- JSON: body, flight, sim_time of the record
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, stat)
);
CREATE INDEX stat_rank ON player_stat(stat, value DESC);
-- top-N = pure index scan joined to handle, filtered banned_at IS NULL
CREATE TABLE stat_daily (day INTEGER, stat TEXT, player_id INTEGER, value REAL,
  PRIMARY KEY (stat, day, player_id));
```

The projector is a goroutine tailing `event.seq` from its checkpoint — a pure fold. **Rebuild is routine, not scary**: fresh file, replay from 0 at 100–300k events/s (≈ minutes for years of data), atomic rename swap. That's the event-sourcing payoff: projections stay simple and get redesigned freely; new leaderboard idea = new fold + replay.

Upcasting: registry keyed `(type, ver)`, chain `v1→v2→…` applied at decode; stored events immutable forever.

Ban/delete path: `DELETE FROM event WHERE player_id = ?` + handles + R2 prefix + tombstone; fast path filters `banned_at` in reads, periodic full rebuild is the clean path.

### 5.4 Convergence & backups

The schema is **multi-master-safe by construction** (client ULIDs + unique index → union convergence, exactly the outline's insight) — but run **one writer**; there is no load-based reason for a second node at this scale, and the property stays free forever.

Backups: Turso Cloud sync-push as offsite replica ($0–5/mo tier), or brief write-pause + file copy cron. **Never point Litestream at a synced Turso DB** (corrupts sync state), and no `VACUUM INTO` on Turso.

---

## Proposal 6 — Hosting, distribution & website

### 6.1 Primary architecture: one Go binary, one cheap VPS, free CDN

```
KSA + catlog mod ──brotli NDJSON batch, license+proof JWS──▶ Cloudflare (free proxy/WAF/cache)
                                                                    │
                                                          VPS ($4–9/mo, Hetzner CX/CAX or DO)
                                                          └── catlog (single Go binary)
                                                               ├─ ingest handler → bounded chan → single writer → events.db (embedded Turso)
                                                               ├─ projector goroutine → projections.db
                                                               ├─ read API + datastar site (SSE for live updates)
                                                               └─ archiver → R2: per-player cold chunks + deny-list + JWKS  (<$1/mo)
Browser ◀─ CDN-cached pages/JSON (s-maxage=30, stale-while-revalidate=300)
Browser ◀─ SSE live feed (cache-bypassed)
Offsite durability: turso sync push → Turso Cloud ($0–5/mo)
```

- **Go over Rust**: datastar's flagship first-party SDK is Go (v1.0 stable, SSE-based, no build toolchain); `net/http` + goroutines is the canonical shape for SSE + a single-writer channel; single static binary; solo-dev iteration speed. Rust is the fallback if living closer to the Turso engine during beta ever matters. Don't split languages.
- **Writes need no geo-distribution** — telemetry is async, batched, retryable; +150 ms to one region is invisible. Reads get the global story from CDN-cached projection responses. A leaderboard site has no case for multi-region writes; per-player-log isolation keeps that door open forever anyway.
- R2 archive (later phase): per-player immutable chunk prefixes (`/players/{handle}/chunks/…`), zstd MessagePack, `Cache-Control: immutable`; caps VPS disk, doubles as open-data export and DR, purge-compatible (delete one prefix). Explicitly *not* the read path.

### 6.2 Cost (verified Aug 2026)

| Item | Monthly |
|---|---|
| Hetzner CX22 / CAX11 (or DO $6 droplet) | ~$4–9 |
| Cloudflare free (proxy, WAF, cache, rate rules) | $0 |
| R2 archive | <$1 |
| Turso Cloud offsite replica | $0–5 |
| **Total** | **~$5–15** |

Alternative (Cloudflare-native: Workers + D1) only if zero-server-admin ever outweighs cost predictability: $5 base but **per-row-written billing with 3× index amplification** couples cost directly to telemetry granularity ($5 → $135/mo if the mod over-samples), can't run Turso, SSE is awkward under Worker CPU limits, and the backend becomes TypeScript. Ranked second.

### 6.3 Website

- **datastar** (v1.0, ~11 KB, hypermedia + SSE) per the outline preference; Go SDK is first-party. Server-rendered leaderboards from `player_stat` (pure index scans), CDN-cached; a live "recent events" ticker via SSE fed from projector notifications (SSE endpoints bypass cache).
- Pages: global leaderboards per stat (biggest survived lithobrake, kitten tumble counts, RUD tallies by cause, fastest speeds, distance travelled, most kittens safely recovered…); per-handle profile (their records + flight history); recent-activity feed; docs (mod install, credential setup, privacy statement: "we never receive your email").
- Dashboard (session-auth'd): handle list with credential metadata (`jkt`, issued/expires — nothing secret), new-handle wizard (WebCrypto keygen in-browser → one-time `catlog-credential.json` download), reissue, revoke, delete-my-data button.

---

## Proposal 7 — Leaderboard & stat catalog (launch set)

Directly implementable from verified game surfaces:

| Board | Source |
|---|---|
| 🏆 Biggest lithobrake (survived) | `vehicle.impact` where `survived` — `GroundImpactEvent.ImpactVelocity`, no same-frame destruction, not launch-pad, not teleport-flagged |
| 🙀 Cats don't always land on their feet | count of `kitten.tumble` (`LocomotionMode.Tumbling` entries ≥ 6.5 m/s) |
| 💥 RUD tallies, by cause | `vehicle.rud` — 6-cause enum gives six sub-boards for free (GroundImpact / OceanImpact / Collision / ExcessiveGForce / AerodynamicForces / HydrodynamicForces) |
| 🌡️ Highest peak G survived | `StructuralLoad.PeakGLoad` from windows/RUD context |
| 🚀 Fastest surface speed / orbital speed | telemetry windows |
| 🛰️ SOI collector | distinct bodies per player from `vehicle.soi` |
| 🌍 First-to / most orbits, most dockings, most stagings | respective events |
| 🐱 Kitten career stats | roster snapshots: distance, fastest, mission count/time; KIA memorial wall |
| ⛑️ Most kittens recovered safely | `flight.ended: recovered` × crew |

---

## Phased roadmap

1. **Phase 0 — contracts.** `docs/events.md` (event taxonomy + versions), credential file format, ingest API spec. Small, but everything hangs off it.
2. **Phase 1 — mod MVP.** Sampler + detector (poll-based events only) + outbox + shipper against a dev ingest stub. Validate patch points against the live game (esp. the crew-survival question and `DestroyVehicleFromEvent`).
3. **Phase 2 — backend MVP.** Go monolith: ingest (full verification path) + event store + one projection (lithobrake board) + minimal datastar page. Deploy on VPS + Cloudflare. Bearer-stopgap auth acceptable here **only** if it keeps the license claim shape.
4. **Phase 3 — identity.** Discord login, handle dashboard, WebCrypto issuance, deny-list, delete-my-data. Swap stopgap → PoP proofs.
5. **Phase 4 — integrity + polish.** Harmony event set, ring-buffer traces on record candidates, quarantine pipeline, remaining boards, live SSE feed.
6. **Phase 5 — archive & openness.** R2 per-player chunks, open-data export, offsite sync, public API docs.

## Open decisions

1. **Passive-channel granularity** (§2.3) — windowed 30 s (recommended) vs raw 1 Hz. The single biggest cost/complexity lever; affects mod, wire, storage.
2. **Turso vs vanilla SQLite at launch** — repository interface makes it a one-liner, but pick the launch default (recommendation: Turso, per stated preference, with the escape hatch tested in CI).
3. **License TTL** — 180 d recommended; shorter (30–90 d) shrinks deny-list retention at the cost of player reissue friction.
4. **Handle policy** — length/charset, impersonation deny-list, whether handles are renameable (recommendation: immutable; new handle = new credential).
5. **Second IdP timing** — Google is the natural add (real OIDC); ship Discord-only first?
6. **Crew-survival semantics** — verify in-game that physics RUDs really leave kittens alive (decomp says yes, surprising); the lithobrake "science/kitten survived" rule depends on it.
7. **Where the repo splits** — mod (C#) and backend (Go) in this repo as `/mod` + `/server`, or separate repos.
