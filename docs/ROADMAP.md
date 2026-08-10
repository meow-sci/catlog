# catlog roadmap

What is not built, what is blocked, and what is deliberately never going to be built.

Everything that *is* built is described in [ARCHITECTURE.md](ARCHITECTURE.md) and decided in
[DECISIONS.md](DECISIONS.md). This file holds only the open edges.

---

## 1. Blocked on the game — the mod has never run inside KSA

**This is the single largest gap and it should be read first.** `mod/catlog` compiles against the
real `KSA.dll` and deploys, but **no line of it has executed inside the game**. Every in-game
behaviour below is unverified. The lib/sim split means the interesting code — detection, spooling,
signing, shipping, recovery — is exercised heavily without the game; what is untested is the layer
that reads KSA and the Harmony patches that drive it.

Run this checklist on a machine with KSA, then replace each item with a dated result.

- [ ] **Mod loads.** Start KSA with `catlog` in the mods folder. The console reports N Harmony
      patches applied with **no unresolved targets**. Press **F10**: the status window opens and
      reports the game build, the install ULID, and all subsystems nominal. Any "patch target does
      not exist in this game build" line is a [ksa-integration.md](ksa-integration.md) regression —
      record which one.
- [ ] **Liftoff.** Launch a crewed vehicle. Within ~1 s the *Vehicles* row is non-zero and *Recorded*
      climbs. Expect `flight.started`, then `vehicle.situation` on the landed→freefall edge and
      `vehicle.atmosphere: exited` on the way up.
- [ ] **Orbit.** Circularise above the atmosphere. Exactly one `vehicle.orbit: achieved` — the rule
      is `pe_alt > atmo_height + 1000`, and `pe_alt` must be an **altitude**, not a radius. A `pe_m`
      in the millions means the `MeanRadius` subtraction regressed. Warp a few orbits: no repeats.
- [ ] **Each RUD cause, one flight per cause.** `ground_impact` (fly into terrain), `ocean_impact`
      (into water), `collision` (into another vehicle), `excessive_g_force` (hard burn or aggressive
      re-entry), `aerodynamic_forces` (dive at low altitude), `hydrodynamic_forces` (fast water
      entry). Each must produce one `vehicle.rud` with the right cause, and **crew must come back
      alive** (D11) — check the roster afterwards for no new KIA.
- [ ] **Survivable lithobrake.** Land hard, survive, recover: `vehicle.impact` with `survived: true`,
      the right crew count, `launch_pad: false`. Then repeat and **abandon** the vehicle immediately
      after the impact instead of recovering — `survived` must be `false`. This is the manual-destroy
      fix, and it has never run in-game.
- [ ] **Launch-pad impacts are excluded.** A hard set-down on the launch pad records
      `launch_pad: true`, and scores nothing.
- [ ] **Landing.** Set down gently and stay intact: exactly **one** `vehicle.landed` beside the
      `vehicle.situation` for the same edge, `survived: true`, `vertical_speed_ms` a small **positive**
      number (positive is *downwards*) and `radar_alt_m` near but not exactly 0. Then bounce a lander
      so it alternates `freefall`/`landed` for several seconds — the 2 s shared debounce must hold it
      to one record per 2 s and **never** one per sample. Then land and immediately scuttle:
      `survived` must be `false`, and **no feed line** may appear.
- [ ] **Nothing lands at save-load.** Load a save with a vehicle already sitting on the ground. No
      `vehicle.landed` at all — the baseline seeds the latch and emits nothing.
- [ ] **The new spatial reads resolve.** `lat` / `lon` present and sane on a launch, a landing and a
      RUD on a `Celestial`; `radar_alt_m` **absent** on a `telemetry.window` spent in orbit and
      present near the surface; `warp_max` reading 1 in real time and the warp factor under warp.
      Any of these arriving as `0` rather than absent is an omit-don't-zero regression (MOD-078).
- [ ] **`stage_count` is not 0.** The highest-churn read of the wave. A three-stage rocket reports 3
      on `flight.started`; a 0 means `SequenceList` moved in this build.
- [ ] **Construction facts survive the KSA read.** A rocket with installed engines reports a
      positive `flight.started.engine_count`; a probe with RCS but no rocket engine reports an
      explicit 0; and an induced read failure omits the key rather than claiming zero. Destroy a
      known vehicle through a physics RUD and confirm `vehicle.rud.part_count` is the number of
      parts the intact vehicle held at that boundary.
- [ ] **Teleport does not false-flag.** Go EVA, and separately do an editor decouple. Neither may
      produce `flight.flagged: teleport`. Then teleport from the console or the Set Orbit window:
      that one **must** flag.
- [ ] **Tumble.** Send a kitten downhill fast enough to trip the 6.5 m/s gate. One `kitten.tumble`
      per tumble — the `Tumbling → Rightening → Grounded` settle must **not** produce a second.
      Confirm `from: "airborne"` on a botched landing and `from: "grounded"` on a trip.
- [ ] **Tuning flag.** Open the game's Kitten Locomotion Tuning window and change the tumble gate. One
      session-wide `flight.flagged: tuning`, and every open flight is tainted.
- [ ] **Staging, docking, EVA.** Stage → `vehicle.staging` with the right index. Dock and undock →
      `vehicle.docked` / `vehicle.undocked` with the other flight's ULID resolved. EVA out and back →
      `kitten.eva_start` / `kitten.eva_end` with a sane duration.
- [ ] **Save/load is a clean boundary.** Load a save mid-session: a new `session.started`, no spurious
      orbit or SOI events for vehicles that were already there, and the session ULID changes.
- [ ] **The cached system survey is complete and ordered.** At every session boundary,
      `system.discovered` arrives before `session.started`, and its header reports the stock body
      count. On the first unmarked boundary, the emitted body rows have no missing bodies or
      duplicate ids. Loading the same save twice produces the same system hash. Hand-editing
      `Astronomicals.xml` produces a different hash on the next load and sets `system_changed` on
      the career binding.
- [ ] **Orbit elements are real game values.** On a bound orbit, `sma_m`, `lan_deg`, `argp_deg`,
      `t_pe` and `period_s` are finite and agree with the game's orbit. On an escape trajectory,
      `period_s` is exactly 0 rather than NaN or a fabricated period.
- [ ] **The telemetry state vector is atomic.** A window in flight carries a finite six-number
      `state` in the documented parent-body-centred inertial frame. Where the KSA read is made to
      fail, the whole object is absent — never a partial object and never a `{0,0,0}` position.
- [ ] **Ship to a local server.** `make dev`, claim a handle, point `credential_path` at the download,
      restart the game. The window shows the handle and expiry; *Last ship* goes green with the
      **server's** accepted/deduped counts; the board shows the flight. Kill the server mid-session —
      the retry ladder shows and the queue grows, nothing is lost — then restart it and watch the
      backlog drain.
- [ ] **Clean unload.** Quit from the menu. The last events are shipped or spooled, `outbox.db` leaves
      no `-wal` behind, and the next launch continues the same `sid`/`seq` chain with no
      `409 stream_fork` on the first batch.
- [ ] **Cost.** With Diagnostics open, confirm the sample stays well under a millisecond with a dozen
      vehicles in the system, and *Read faults* stays at 0. Measure the one-time system survey and
      the per-sample state-vector read separately as well as the whole sample. Constitution §3 — the
      mod is a guest.

### Two things this checklist settles

**D11 (crew survival) is confirmed at source level and stays `BEST-GUESS` until it is confirmed
in-game.** `Kia = true` is written in exactly one place, reachable only from the player-initiated
destroy path; the physics RUD path never touches it. So `kitten.kia` signals *deliberate scuttling*,
not an impact fatality — which means the KIA-proximity clause on the lithobrake board almost never
fires. If the in-game result differs, [events.md](events.md)'s rule changes and a rebuild heals the
boards.

**Known limitation: renaming a vehicle mid-flight splits the flight in two.** A rename is deregister →
rename → register with no disposal, so the old id closes as `despawned` and the new one starts a fresh
flight. The result is two honest flights rather than a corrupt one. The fix needs a rename-aware
tracker key, which is not worth inventing before the checklist above has run.

---

## 2. Open, unblocked

**Running the Linux artifact has never been verified.** The cross-compile is proven — the ELF, its
interpreter and its three `DT_NEEDED` entries are all measured — but no catlog binary has *executed*
on Linux, because this machine has no usable docker daemon. First deploy or a `docker run` closes it.
See [operations.md](operations.md).

**The nginx configs have never been through `nginx -t`.** Structurally checked only. The first VPS
install must validate before reloading.

**R2 is designed and not built.** [r2-archive-design.md](r2-archive-design.md) specifies it fully:
S3-compatible API, credentials from the environment, path-style addressing, no lifecycle rules and no
versioning (chunks are immutable, and versioning would preserve exactly the data a purge deletes). No
cloud SDK is a dependency, and none is added until the day it is built. The migration is `rclone copy`
because the key layout is already the bucket layout.

**`projector.Upcasters` is empty.** Every event type is `ver: 1`, so there is nothing to upcast. The
registry exists now so that the first payload version bump is a registration rather than a migration,
because stored events are immutable forever.

**Engine events are whole-vehicle, not per-engine.** A vehicle that shuts down one of two engine
groups reports nothing until the last one stops. Per-engine granularity is reachable but costs a
four-type-argument generic that would need re-verifying against every game build — a later change with
a real price, recorded rather than hidden.

**`tursogo` and `purego` are pinned, and every bump needs a behaviour re-probe.** The driver is
generated from a spec that ships inside the module, and a test-only dependency already forced a purego
bump onto the database's FFI path once. A green build is not sufficient evidence. If the pairing ever
breaks, the escape hatch is to move `internal/nginxproxy` into its own Go module so a test-only
dependency stops constraining the server's.

---

## 3. Deliberately not built

These are settled. They are listed so nobody has to re-argue them.

### Anti-cheat beyond the stock-data test

Constitution §8 defines five tests a proposed integrity check must all pass. Named and out of scope:
physics-plausibility envelopes, quarantine or pending-record pipelines, replay traces attached to
record claims, robust z-scores and statistical outlier detection, suspicion multipliers or reputation
scores, shadow-banning, community-report queues, and client attestation of any kind.

**"Shadow-banning" there means the anti-cheat sense** — a machine inferring a cheater from the shape
of their data and silently voiding them. The **moderation** verb of the same name is built and is not
this: an administrator names an account, for abuse or decency, and its log is withheld rather than
deleted so the call can be reviewed and reversed. Constitution §7 owns it and §8 exempts it
explicitly. See [identity.md](identity.md).

The client is attacker-controlled and always will be. Signatures prove who sent something, never that
it is true. A determined person can put a fake number on a leaderboard about a cat game, and the
complexity budget is better spent elsewhere.

Anything already in the code that fails those five tests is listed in
[integrity-audit.md](integrity-audit.md) for the owner to decide on.

### An in-game editor for the `[events]` table

`catlog.toml` lets a player switch individual event types off, and the status window **reports** what
is off — `N off in catlog.toml: <names>` — but it does not edit it. That is deliberate rather than
unfinished. The window is read-only rows and one checkbox by design, and it has no text input, which
is the stated reason it needs no `HotkeyGuard` (MOD-051); a toggle per switchable type would mean
persisting from the game thread, re-deriving the pipeline's filter live mid-session, and deciding
what happens to a half-open telemetry window when its type is switched off underneath it. The file is
the interface, the header documents every key, and the window's job is to make sure a setting a
player made months ago is never invisible. If it is ever built, it must go through
`ModConfig.Normalize` and `EventTypeFilter.Create` like every other path — the two-layer refusal in
MOD-072 is not something a UI gets to bypass.

### Locking `vehicle.rud` along with the other six

Considered and refused. Five of the six locked types are the score/integrity spine whose absence
makes a number *better* than it was; `system.discovered` is separately locked because suppressing it
destroys career-to-system attribution. `vehicle.rud` only hides how often a player exploded, and the
`vehicle.impact.survived` verdict — the one that actually scores — is computed client-side before the
filter ever sees the envelope, so it stays honest either way. Vanity-hiding is a preference, and
Constitution §8's proportionality is the reason the score/integrity list stops where it does. See
MOD-072 and PROJ-108.

### A `kittens_scuttled` leaderboard

Refused. `kitten.kia` identifies the deliberate player-destroy path with crew aboard, so turning it
into a durable public ranking would attach a public consequence to a person for using an action the
game offers. That fails Constitution §8's consequence test. The shipped lost-vehicle crew boards
instead describe only the kittens aboard at a physics RUD and make no claim about deaths; they do
not provide a softer route to the same scuttle ranking. See PROJ-118.

### A server-side catalogue of celestial-body names

Refused. Catlog compiles no body name into Go: KSA content and mods define an open set, and a server
list would silently become wrong for somebody. The immutable log instead carries each game's
reported catalogue under its content-derived system hash. The Every World and Nothing Left badges
use that per-system catalogue only when its header is effectively complete; they do not turn it into
a global allow-list. Dynamic board and badge families likewise derive their keys from emitted names
and apply only protocol-safe key syntax. This is the same position as PROJ-033, extended from board
keys to the system-aware projections that now exist.

### A 3D celestial-system and flight-path view

Not planned, not scoped and not owed. The data foundation is intentionally present:
`GET /v1/systems/{slug}` exposes the surveyed forest, `vehicle.orbit` carries the complete milestone
element set, and `telemetry.window.state` carries parent-body-centred position and velocity samples.
Those facts make a renderer possible; they do not choose a scene graph, propagation model, camera,
time control or browser performance budget. A future renderer should start from those contracts
rather than repeat the KSA survey, but the existence of drawable data is not a promise to build one.

### Propellant, Δv and any efficiency board that judges a claim

Refused for now, and the reason is Constitution §8 rather than cost. `Vehicle.PropellantMass` sits
beside the mass catlog already reads twice, so this is cheap — but the moment the log carries both a
Δv figure and the orbit a vehicle reached, the difference between them is computable and the next
reasonable-sounding proposal is to reject records where the two do not reconcile. That check needs a
tolerance nobody can defend (aerobraking, gravity assists, staging losses, drag, an engine the mod
could not read), and its false positives land on **honest players doing something clever**.

What shipped instead is the honest version of the same appetite: `vehicle.orbit.mass_kg` beside
`flight.started.mass_kg`, and the `heaviest_to_orbit` / `heaviest_launch` pair. They rank absolute
whole-vehicle mass at two observable boundaries for comparison; neither claims a payload split or
computes a ratio. A future decision may record propellant — but it has to write *record it, never
validate it* down first, the way `warp_max` did. PROJ-099.

KSA does not expose a durable payload-versus-booster boundary. `SequencePerformance` contains the
per-stage split that could approximate one, but it is refreshed in flight only while one of two UI
windows is open and otherwise retains stale editor data. Refusing that split is therefore about the
trustworthiness of the observation, not implementation cost. Likewise `NavBallData.DeltaV` is only
the active stage and is refreshed behind the same UI gates, while `ThrustWeightRatio` is a HUD value
using surface gravity and current throttle rather than a vehicle capability. If expended Δv is ever
wanted, integrating `KinematicMeasurements.DeltaVelocityCci` is the honest source, and PROJ-099's
*record it, never validate it* rule must be recorded for that use before the payload changes.

### Reentry-heat boards

Impossible with the current game model. KSA has no thermal simulation, part heat or overheat state;
the only part-path `Temperature` is a visual-effects value driven by frost and emissivity timing.
Ranking it would present an FX control as vehicle heating, so there is no missing detector to add.

### Other surveyed KSA possibilities

Appendix D of [the implementation plan](../LOTS_OF_THINGS_PLAN.md#appendix-d--surveyed-readable-and-deliberately-not-built-now)
records the remaining source survey so a future request begins with evidence rather than another
decompilation pass. It covers cheap polled reads (fuel fraction, dimensions, structural-load
fractions, region, module inventory, battery, target and control point), optional celestial
rendering data, three viable but unused Harmony patch points, and confirmed absences such as
science, contracts, comms, per-part destruction, aerodynamic detail and EVA-only distance. None is
promised work; each still needs its own event/projection, cost, privacy and Constitution review.

### A plausibility rule on what counts as a "real" landing

A one-metre hop is a landing, and catlog says so on the player-facing site rather than filtering it
out. Every version of the filter — a minimum fall height, a minimum time airborne, a minimum
horizontal distance — infers **intent** from data shape, excludes something real (a hop test, a rover
cresting a ridge, an aborted hover) and excludes no determined faker, who hops from higher. PROJ-096.

### Save-scum detection

catlog cannot tell save-scumming from ordinary reloading, and **does not try**. Reloading before a
tricky burn and reloading to retry a milestone look identical, and both are trivially available to
everyone. A career whose clock went backwards is *marked* — the mark excludes nothing, scores nothing,
and ranks the row normally. It qualifies a number the way an absent reading is absent rather than
zero.

### Response compression in Go

`catlogd` has no compression middleware and gains none. It is the reverse proxy's job, and both nginx
configs already do it. See [server.md](server.md).

### Anything that scales cost with attention

No managed services, no per-request cloud bill, no framework, no ORM. Constitution §2: the way a hobby
project dies is not a crash, it is a monthly invoice that makes the owner turn it off.

---

## 4. Where new work goes

There is no work-package sequence any more — the build order was a property of writing the thing the
first time, and it is history. New work is: read [CONSTITUTION.md](CONSTITUTION.md), make the change,
keep `make test` green, update the documents named in
[ARCHITECTURE.md](ARCHITECTURE.md#7-keeping-the-documentation-true), and add a dated entry to
[DECISIONS.md](DECISIONS.md) saying why.
