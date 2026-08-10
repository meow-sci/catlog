# catlog events — envelope & taxonomy

Owns **§4.1–§4.2**.

> **Normative.** This document is the single source of truth for both the C# mod and the Go server.
> **Changing an event or a payload field requires bumping that event's `ver`.** Anything that changes here needs a dated entry in [DECISIONS.md](DECISIONS.md) saying why,
> in the same commit — see [ARCHITECTURE.md](ARCHITECTURE.md#7-keeping-the-documentation-true).

## Event envelope

One event = one JSON object (one NDJSON line). snake_case keys. Unknown envelope keys are rejected; unknown **payload** keys are preserved (forward compat).

```jsonc
{
  "id":      "01J9V5M3E8Z0FAKEULID26CHR",  // ULID, client-minted, dedup key
  "type":    "vehicle.rud",                 // namespaced, lowercase, [a-z0-9_.]
  "ver":     1,                             // event schema version, int ≥1 — see the taxonomy below
  "flight":  "01J9V5M3E8...",               // flight_id ULID; null when the event names no flight
  "session": "01J9V5M3E8...",               // session_id ULID, never null
  "career":  "b7k2q9x4m0nrt3vz",            // career id, 16 chars, never null — see below
  "sim_t":   12345.678,                     // seconds since this career's game started (float)
  "wall_t":  1770000000123,                 // client unix ms (untrusted)
  "payload": { }                            // per-type object, may be {}
}
```

Validation (server): `id` parses as ULID; `type` matches known registry or event is stored with `flagged` marker in payload? — **No**: unknown `type` → the whole batch is rejected `400 malformed_batch` (the mod and server ship together; unknown types mean version skew, surface it loudly). `ver` unknown-but-higher → accept and store (projector skips what it can't decode, logs once). `career` must be exactly 16 lowercase Crockford base32 characters (`0-9 a-z` minus `i l o u`); anything else rejects the batch.

### `career` and `sim_t` — the career clock

**A career is one KSA save played over time.** It is the unit `sim_t` is measured in, and the unit the time-to-milestone boards rank.

- `sim_t` is the game's `Universe.GetElapsedSimTime()`. KSA starts a new game at **exactly 0** and serialises the clock into the save (`UniverseData.GameTime`, restored by `Universe.DeserializeSave`), so `sim_t` **is** "seconds since this career's game started" and it survives quitting the game. There is no second clock and no per-career origin on the wire, because none is needed. Evidence with `file:line` in [ksa-integration.md](ksa-integration.md) §5.
- `session` is minted at every save-**load** boundary, so **one career is many sessions**. Two events are comparable in time when they share a `(player, career)`, and never otherwise: two different saves interleave freely in one player's log and their `sim_t` values have nothing to do with each other.
- `career` is opaque to the server: 16 characters, no meaning, no ordering. A client must keep it **stable for the lifetime of one save** and **different between saves**.

**How the catlog mod derives it** (normative for the shipped mod, not for the wire — any client may choose its own scheme as long as the two rules above hold):

```
career = crockford32_lower(SHA-256("catlog-career:" + install_id + ":" + save_key)[0..10])
```

where `save_key` is `"save:" + <KSA save name>` once the career has a save, and `"new:" + <a fresh ULID>` for a game that has not been saved yet. The mod learns the save name by patching `UncompressedSave.Load` (adopt on load, before the session boundary) and `UncompressedSave.Make` (adopt on write, so a career that began unsaved keeps its identity through its first save, and a "save as" carries the career with it). The install-id salt means the server never learns what a player called a save, and two players who both call one `apollo` do not collide.

**Why a save name and not something better:** KSA has no career, save or player identifier of any kind. The save root `UniverseData` has exactly four fields — `GameTime`, `Camera`, `CelestialSystems`, `KittenRoster` — and none is an id, a GUID, a seed or a creation stamp; `SaveMetaData.created` looks like an anchor and is re-stamped on every overwrite. Verified against build 2026.8.5.5168; citations in [ksa-integration.md](ksa-integration.md) §5.

### The clock going backwards

`sim_t` moves backwards within a career only when an **earlier save of that career was loaded**. The server states that and does nothing else about it:

> **Rewind rule.** A career is marked *rewound* when a `session.started` for it arrives carrying a `sim_t` lower than the highest `sim_t` already seen in that career. The mark is permanent, career-wide, and appears as `"rewound": true` on that career's rows on the career-time boards.

The mark **excludes nothing and scores nothing**. The row is ranked normally and the player is treated no differently; it qualifies a number, in the same way an absent `peak_g` is absent rather than zero. Comparing only at session boundaries is what keeps the rule threshold-free — inside a session, event emission is slightly out of order by design (a telemetry window closes with the sim time of its *end*), so "any decrease" would need a tuned epsilon, and a save load has no such ambiguity.

**The honest limitation, stated:** *catlog cannot tell save-scumming from ordinary reloading, and does not try.* Reloading before a tricky burn and reloading to retry a milestone look identical from here, and both are trivially available to everyone. Reaching a milestone faster after a reload still counts. Three further limits fall out of the anchor being a save name:

- deleting a save and starting a new game under the same name re-uses the career id, so the new game's clock reads as a rewind of the old one;
- a career that has never been saved gets a fresh id at every game start, and its events are unlinked from the save it is later written to only for the part before that first save;
- if the mod cannot read the save name at all, the career stays whatever it was and the mark simply never fires.

## Event taxonomy (25 types, every one at `ver: 1`)

Aggregate object `agg` = `{"min": f, "max": f, "mean": f, "last": f}`.
`body` = lowercase celestial body name string (opaque to server). `situation` = lowercased KSA enum name, opaque to server (known values incl. `landed`, `rolling`, `floating`, `sailing`, `dragging`, `bottomed`, plus airborne states — treat as open set).
`vec3` = `{"x": f, "y": f, "z": f}`. `quat` = `{"x": f, "y": f, "z": f, "w": f}`.

**"Opaque to server" is load-bearing, and the stats layer honours it.** KSA's celestial systems are hand-authored content that ships as data and that mods extend or replace, so the server holds no list of bodies: the `fastest_to_<body>` boards come into existence because a body appeared in the event stream, and their titles are derived from the name. The same now goes for `vehicle.rud.cause` — the six values in the table below are the ones the game ships today, not an allow-list, and a cause a future build introduces gets its own `rud_<cause>` board rather than disappearing into `rud_total`. The only thing a name has to satisfy is that it can *be* a stat key: lowercase, starting `[a-z0-9]`, then `[a-z0-9._-]`, at most 40 characters — because a stat key is a URL path segment. A name that cannot still counts towards `soi_bodies` / `rud_total` and still keeps its arrival time; it simply gets no board.

**Why a board for somewhere new may not appear immediately.** Such a board is *listed* by `GET /v1/leaderboards` once at least **2 distinct players** hold a value on it (configurable, `[boards] min_players`). Before that it still exists, is still served at its own URL, and still shows on the profile of whoever is on it — a leaderboard with a single entrant is not a leaderboard, and the threshold is also what stops one modified client filling the public index with invented place names. Nothing is lost while waiting: the per-player value is recorded for **every** body and every cause regardless, so a body sitting at one player is published the moment a second player gets there, and changing the threshold publishes history that is already in the projection.
A trailing `?` on a type — `f?`, `agg?` — marks an **optional** key: when the value is unreadable the key is **absent from the object entirely**, never `null` and never `0`. See "Optional keys" below.

Kitten identity: `kid` = lowercase Crockford base32 of the first 10 bytes of `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` (16 chars); `name` = roster display name sanitized to printable US-ASCII, max 32 chars (moderation surface — purge path covers it).

| type | payload |
|---|---|
| `session.started` | `{"mod_ver": "0.1.0", "game_build": "2026.8.5.5168", "install": "<ulid>"}` |
| `system.discovered` | `{"system": s(hash), "id": s, "name": s(≤64 ascii), "home": s, "bodies": i, "complete": b}` |
| `system.body` | `{"system": s(hash), "body": s, "name": s, "class": s(open set), "kind": "star"\|"planet"\|"moon"\|"minor"\|"other", "rank": i, "parent": s?, "radius_m": f, "mass_kg": f, "soi_m": f, "atmo_m": f, "ocean_m": f, "angvel": f, "axis": vec3, "ccf_to_cce_t0": quat, "sma_m": f?, "ecc": f?, "inc_deg": f?, "lan_deg": f?, "argp_deg": f?, "t_pe": f?, "period_s": f?}` |
| `flight.started` | `{"vehicle_name": s(≤64 ascii), "body": s, "mass_kg": f, "part_count": i, "crew_count": i, "kids": [s], "stage_count": i, "lat": f?, "lon": f?}` |
| `flight.ended` | `{"reason": "recovered"\|"destroyed"\|"despawned", "crew_count": i, "kids": [s], "body": s, "lat": f?, "lon": f?}` — `body` may be the literal `"unknown"` |
| `vehicle.situation` | `{"from": s, "to": s, "body": s, "altitude_m": f, "surface_speed_ms": f, "orbital_speed_ms": f, "radar_alt_m": f?}` |
| `vehicle.atmosphere` | `{"dir": "entered"\|"exited", "body": s, "speed_ms": f, "dyn_pressure_pa": f}` |
| `vehicle.orbit` | `{"phase": "achieved"\|"escaped", "body": s, "ap_m": f, "pe_m": f, "ecc": f, "inc_deg": f, "mass_kg": f}` — `mass_kg` is the mass at the instant the milestone fired |
| `vehicle.soi` | `{"from_body": s, "to_body": s}` |
| `vehicle.rud` | `{"cause": "ground_impact"\|"ocean_impact"\|"collision"\|"excessive_g_force"\|"aerodynamic_forces"\|"hydrodynamic_forces", "peak_g": f, "peak_q_pa": f, "speed_ms": f, "altitude_m": f, "body": s, "crew_count": i, "lat": f?, "lon": f?}` |
| `vehicle.impact` | `{"speed_ms": f, "energy_j": f, "survived": b, "launch_pad": b, "body": s, "crew_count": i, "lat": f?, "lon": f?}` — `survived` = no destruction of the same vehicle in that frame **or the next** (mod-computed, §7.2) |
| `vehicle.landed` | `{"body": s, "vertical_speed_ms": f, "horizontal_speed_ms": f, "crew_count": i, "survived": b, "radar_alt_m": f?, "lat": f?, "lon": f?}` — `vertical_speed_ms` is **positive downwards**; `survived` is the same one-full-frame hold as `vehicle.impact` |
| `vehicle.staging` | `{"stage_index": i}` |
| `vehicle.docked` / `vehicle.undocked` | `{"other_flight": "<ulid>"}` |
| `engine.ignition` / `engine.shutdown` / `engine.flameout` | `{"engine": s(template name), "count": i}` |
| `kitten.eva_start` | `{"kid": s, "name": s}` |
| `kitten.eva_end` | `{"kid": s, "name": s, "duration_s": f}` |
| `kitten.tumble` | `{"kid": s, "name": s, "speed_ms": f, "body": s}` |
| `kitten.kia` | `{"kid": s, "name": s, "context": "rud"\|"manual_destroy"\|"unknown"}` |
| `roster.snapshot` | `{"kittens": [{"kid": s, "name": s, "travelled_m": f, "fastest_ms": f, "missions": i, "mission_time_s": f, "kia": b}]}` — every 10 min of play, and on session end |
| `flight.flagged` | `{"flag": "teleport"\|"refuel"\|"resource_edit"\|"console"\|"tuning", "detail": s}` |
| `telemetry.window` | `{"t0_sim": f, "t1_sim": f, "n": i, "body": s, "alt_m": agg, "surface_speed_ms": agg, "orbital_speed_ms": agg, "accel_ms2": agg, "peak_g": f?, "max_q_pa": f?, "mass_kg_last": f, "radar_alt_m": agg?, "warp_max": f}` — one per vehicle per 30 s sim-time of active flight |

### Payload rules a decoder has to get right

**The system catalogue is complete-or-declared-incomplete.** `system.discovered` is emitted before
`session.started` at every session boundary and binds the career to the system hash. Its `id` is the
raw `CelestialSystem.Id`; `name` is the matching system-selection display name, sanitised to at most
64 printable ASCII characters, or the raw id when no exact ordinal metadata match exists. `home`,
`body` and `parent` use the same canonical lowercase body-name normalisation as flight events.
`bodies` is the number of materialised celestial bodies, not a template count.

`complete` is true only when the complete body list accompanies this header: `system.body` reporting
is enabled, the body count is at most 5,000, every body has valid required physical and orientation
values, and this career/system is not already durably marked sent. Otherwise the header carries
`complete: false` and no `system.body` rows are emitted. Disabled, capped and invalid surveys write
no durable marker, so turning body reporting back on or fixing the content allows a later session to
retry. A marked survey needs no retry because its catalogue was already appended atomically. The
header is always reported. Body rows are reported at most once per `(career, system hash)`, but may
be safely resent after local state loss. A successful first report is appended in this order —
header, all body rows, then `session.started` — before its marker is committed. A marked report is
header then `session.started`. The list is never truncated.

**`system.body` describes immutable authored celestial data, not live state.** `class` is the
concrete runtime class name and is an **open set**; a server must not validate it against today's
game classes. `kind` is the mod's fixed semantic mapping: `StellarBody` is `star`; planetary,
terrestrial and atmospheric bodies directly under a star are `planet` and those under any other
body are `moon`; minor bodies, asteroids and all comet classes are `minor`; an unknown class is
`other`. `rank` is depth from that body's root, and `parent` is absent on a root. A root body's
infinite sphere of influence is represented as `soi_m: 0`; every emitted number is otherwise finite.
`mu` is absent by design because it is derived exactly from `mass_kg` and the game's gravitational
constant.

The six orbital-shape keys — `sma_m`, `ecc`, `inc_deg`, `lan_deg`, `argp_deg`, `t_pe` — are a group:
all six are present, or all six are absent. They are absent for roots and whenever any member is not
finite. Angles are degrees. `t_pe` is the absolute periapsis time in the same career clock as
`sim_t`, not an offset from the survey. `period_s` is independent and is absent when the game reports
a non-finite period, including an unbound orbit. Zero remains a real value and is never used for
absence. `ccf_to_cce_t0` is finite and normalised; because `q` and `-q` are the same rotation, its
first non-zero component in `w,x,y,z` order is positive, and every negative zero is written as
positive zero.

**An unreadable value never scores.** Three keys read as a defined fallback rather than being
omitted, and every board that reads one gates it, because the fallback is indistinguishable from a
real reading of the same number:

| key | reads as when the mod could not say | what stops it scoring |
|---|---|---|
| `vehicle.orbit.mass_kg` | `0` | `heaviest_to_orbit` requires `> 0` |
| `flight.started.stage_count` | `0` | `biggest_stack` requires `> 0` |
| `telemetry.window.radar_alt_m` | absent | `lowest_pass` refuses the absent aggregate, then requires `min > 0` |

**Optional keys — omit, never zero.** `lat`, `lon` and `radar_alt_m` (on `vehicle.situation`,
`vehicle.landed` and as an aggregate on `telemetry.window`) join `peak_g` / `max_q_pa` under the
omit-don't-zero rule, and for the same reason stated the other way round: **a zero is a real
reading**. Latitude 0 is the equator, longitude 0 is a meridian, and a radar altitude of 0 is the
ground. Writing 0 for "could not read" produces a *wrong* record rather than a missing one, so the
key is left out of the object and a decoder must read these into an optional, never a plain float.
`vehicle.rud`'s `peak_g` / `peak_q_pa` remain the exception: they come off the destruction event
itself, are non-nullable, and are emitted as 0.

**`kids` is always present and always an array**, possibly empty — an uncrewed flight sends `[]`, not
a missing key, so a reader never has to tell "nobody aboard" from "the mod did not say". It carries
one 16-character `kid` per kitten aboard, in seat order, under the same per-player relabelling every
other `kid` gets.

**`flight.ended.body` may be the literal `"unknown"`.** The event carries a body because a landing
site is otherwise unplaceable — the flight's last `telemetry.window` may be a whole window old and
the vehicle may have changed SOI since. Every ordinary end path reads a real body, because
the mod hooks the single removal choke point *before* the vehicle is torn down. The one path that
cannot is the poll's silent-removal safety net, where there is no vehicle object left to ask; it
sends `"unknown"`, matching the `crew_count: 0` that path already reports. `body` is an open set with
no allow-list, so `"unknown"` is an ordinary member of it — but there must be no `landed_on_unknown`
board, and it is excluded wherever a real body is required.

**`telemetry.window.radar_alt_m` has a different population from `n`.** It is folded over *only* the
samples that carried a reading — the `peak_g` rule — and is absent entirely when none did, which is
every window spent in orbit. `n` remains the total sample count. Nothing may reconstruct a count from
the aggregate.

**`warp_max` is descriptive only** — the highest simulation speed seen in the window, `1` for real
time and never `0`. Under Constitution §8 it may inform, weight or annotate a reading; it must not
reject or disqualify a record, and it is not a cheat signal. A window samples at 2 Hz *wall* clock
but spans 30 *sim* seconds, so under warp its aggregates are drawn from a handful of samples rather
than the nominal 60, and nothing else in the payload says so.

**`vehicle.landed`** is the transition into a surface-contact situation (`landed`, `rolling`,
`sailing`, `floating`, `dragging`, `bottomed`) from a contact-free one (`freefall`, `maneuvering`),
both sides required to be *known* situations. It is detected on the **same edge** `vehicle.situation`
already detects — one detector, two events emitted off one transition, in that order — and it
inherits that rule's 2 s debounce rather than carrying a timer of its own. `survived` goes through
the **same one-full-frame hold** as `vehicle.impact.survived`, so scuttling after a bad landing
cannot bank a record; it is authoritative and must never be re-derived from a nearby `vehicle.rud` or
`flight.ended`. `vertical_speed_ms` is **positive downwards** (a soft touchdown is a small positive
number) and `horizontal_speed_ms` is a magnitude, always ≥ 0.

**There is no plausibility rule on a landing, and there must not be one.** A one-metre hop is a
landing. Filtering on "was that a *real* landing" infers intent from data shape, which
Constitution §8 forbids.

**`flight` on the two kitten scoring events.** `kitten.tumble` and `kitten.kia` name the flight they belong to whenever the mod can resolve one. The envelope field is load-bearing on both, not decorative:

- **`kitten.tumble`** carries the tumbling kitten's own EVA flight (a kitten outside *is* a vehicle whose id is her roster name). Only a flight-bearing event can inherit its flight's flags, so without it a `tuning`-flagged session's tumbles would score anyway, which is exactly what the flag was built to prevent. It is `null` when the kitten has no open flight — the mod resolves an existing flight and never mints one, because a minted flight would have no `flight.started` and would poison the join permanently.
- **`kitten.kia`** carries the flight the kitten died on, and **only when the mod can prove one**: a crew read taken inside the game's kill-the-crew call within 2.0 sim seconds of the roster diff, or the kitten's own EVA flight if she was outside. Otherwise it stays `null`, deliberately — a guessed flight would disqualify an innocent flight's `biggest_lithobrake_survived` / `biggest_impact_energy` record under the ±2 s rule below, and that is the one outcome the rule must never produce. See [event-details.md](event-details.md#kittenkia) for the exhaustive null cases.

`BEST-GUESS (D11)` crew-survival semantics used by projections: a lithobrake counts as *survived with crew* iff `vehicle.impact.survived == true && crew_count ≥ 1 && launch_pad == false` and no `kitten.kia` event exists for the same flight with `sim_t` within ±2.0 s of the impact. Revisit after in-game verification of `KillCrew` behavior.

> **Decomp verification (2026-08-06, build 2026.8.5.5168).** The D11 guess is **confirmed at source level**: `Kia = true` is written in exactly one place, reachable only from `Vehicle.KillCrew()`, whose only caller is the player-initiated destroy path (guarded by `if (!Recovered)`). The physics RUD path calls `EndAllCrewMissions` and never touches it. A `kitten.kia` event therefore signals *deliberate scuttling*, not a fatality from an impact. The rule above stays as written — the `kitten.kia` proximity check simply fires rarely, on scuttles with crew aboard rather than on every fatal crash. Full evidence, with file:line citations, in [ksa-integration.md](ksa-integration.md) §4. In-game confirmation is still required before the rule is treated as settled (WP8).

**Payload caveats established by the same verification** (see [ksa-integration.md](ksa-integration.md)):

- `telemetry.window.peak_g` / `max_q_pa` come from `Vehicle.StructuralLoad`, which is **only written under full physics** and reset each prepared step. An all-zero reading means *no data this step* (on-rails or freefall), not *zero g* — the mod must omit rather than report zero.
- `roster.snapshot.fastest_ms` is the game's own `FastestSpeed`, which is **ecliptic-frame** (it includes the parent body's orbital motion, so it reads ~30 km/s on Earth). It is recorded for completeness and must not be surfaced as a vehicle speed record; the speed boards derive from `telemetry.window` instead.
- `vehicle.orbit.ap_m` / `pe_m` are **altitudes above the parent body's mean radius**, in metres — *not* the game's from-centre radii. §4.2 left this ambiguous; altitudes are the only reading under which the orbit-achieved rule (`pe_alt > atmo_height + 1000`) makes sense. The mod's snapshot fields are named `ApAltM`/`PeAltM` to make it unmissable, and the game project subtracts `Parent.MeanRadius` itself.
- `flight.flagged.flag` includes `"tuning"`: the game ships a debug window that live-edits `KittenLocomotionTuning.Current.TumbleSpeedGate` (default `6.5`), which would otherwise make the `kitten_tumbles` board trivially forgeable. The mod emits this flag whenever the active tuning differs from stock, and `detail` carries what changed.
