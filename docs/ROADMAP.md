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
- [ ] **Teleport does not false-flag.** Go EVA, and separately do an editor decouple. Neither may
      produce `flight.flagged: teleport`. Then teleport from the console or the Set Orbit window:
      that one **must** flag.
- [ ] **Tumble.** Send a kitten downhill fast enough to trip the 6.5 m/s gate. One `kitten.tumble`
      per tumble — the `Tumbling → Rightening → Grounded` settle must **not** produce a second.
- [ ] **Tuning flag.** Open the game's Kitten Locomotion Tuning window and change the tumble gate. One
      session-wide `flight.flagged: tuning`, and every open flight is tainted.
- [ ] **Staging, docking, EVA.** Stage → `vehicle.staging` with the right index. Dock and undock →
      `vehicle.docked` / `vehicle.undocked` with the other flight's ULID resolved. EVA out and back →
      `kitten.eva_start` / `kitten.eva_end` with a sane duration.
- [ ] **Save/load is a clean boundary.** Load a save mid-session: a new `session.started`, no spurious
      orbit or SOI events for vehicles that were already there, and the session ULID changes.
- [ ] **Ship to a local server.** `make dev`, claim a handle, point `credential_path` at the download,
      restart the game. The window shows the handle and expiry; *Last ship* goes green with the
      **server's** accepted/deduped counts; the board shows the flight. Kill the server mid-session —
      the retry ladder shows and the queue grows, nothing is lost — then restart it and watch the
      backlog drain.
- [ ] **Clean unload.** Quit from the menu. The last events are shipped or spooled, `outbox.db` leaves
      no `-wal` behind, and the next launch continues the same `sid`/`seq` chain with no
      `409 stream_fork` on the first batch.
- [ ] **Cost.** With Diagnostics open, confirm the sample stays well under a millisecond with a dozen
      vehicles in the system, and *Read faults* stays at 0. Constitution §3 — the mod is a guest.

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

### Locking `vehicle.rud` along with the other five

Considered and refused. The five types that cannot be switched off are the ones whose absence makes a
number *better* than it was; `vehicle.rud` only hides how often a player exploded, and the
`vehicle.impact.survived` verdict — the one that actually scores — is computed client-side before the
filter ever sees the envelope, so it stays honest either way. Vanity-hiding is a preference, and
Constitution §8's proportionality is the reason the list stops where it does. See MOD-072.

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
