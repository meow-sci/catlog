# catlog events — envelope & taxonomy

Origin: [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md) §4.1–§4.2, extracted verbatim.

> Everything in this document is the single source of truth for both the C# mod and the Go
> server. Changing anything here requires bumping `ver` on the affected event and a line in
> [DECISIONS.md](DECISIONS.md).

## Event envelope

One event = one JSON object (one NDJSON line). snake_case keys. Unknown envelope keys are rejected; unknown **payload** keys are preserved (forward compat).

```jsonc
{
  "id":      "01J9V5M3E8Z0FAKEULID26CHR",  // ULID, client-minted, dedup key
  "type":    "vehicle.rud",                 // namespaced, lowercase, [a-z0-9_.]
  "ver":     1,                             // payload schema version, int ≥1
  "flight":  "01J9V5M3E8...",               // flight_id ULID; null for session/roster events
  "session": "01J9V5M3E8...",               // session_id ULID, never null
  "sim_t":   12345.678,                     // Universe sim seconds (float); may jump backwards across loads
  "wall_t":  1770000000123,                 // client unix ms (untrusted)
  "payload": { }                            // per-type object, may be {}
}
```

Validation (server): `id` parses as ULID; `type` matches known registry or event is stored with `flagged` marker in payload? — **No**: unknown `type` → the whole batch is rejected `400 malformed_batch` (the mod and server ship together; unknown types mean version skew, surface it loudly). `ver` unknown-but-higher → accept and store (projector skips what it can't decode, logs once).

## Event taxonomy (launch set, all `ver: 1`)

Aggregate object `agg` = `{"min": f, "max": f, "mean": f, "last": f}`.
`body` = lowercase celestial body name string (opaque to server). `situation` = lowercased KSA enum name, opaque to server (known values incl. `landed`, `rolling`, `floating`, `sailing`, `dragging`, `bottomed`, plus airborne states — treat as open set).
Kitten identity: `kid` = lowercase Crockford base32 of the first 10 bytes of `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` (16 chars); `name` = roster display name sanitized to printable US-ASCII, max 32 chars (moderation surface — purge path covers it).

| type | payload |
|---|---|
| `session.started` | `{"mod_ver": "0.1.0", "game_build": "2026.8.5.5168", "install": "<ulid>"}` |
| `flight.started` | `{"vehicle_name": s(≤64 ascii), "body": s, "mass_kg": f, "part_count": i, "crew_count": i}` |
| `flight.ended` | `{"reason": "recovered"\|"destroyed"\|"despawned", "crew_count": i}` |
| `vehicle.situation` | `{"from": s, "to": s, "body": s, "altitude_m": f, "surface_speed_ms": f, "orbital_speed_ms": f}` |
| `vehicle.atmosphere` | `{"dir": "entered"\|"exited", "body": s, "speed_ms": f, "dyn_pressure_pa": f}` |
| `vehicle.orbit` | `{"phase": "achieved"\|"escaped", "body": s, "ap_m": f, "pe_m": f, "ecc": f, "inc_deg": f}` |
| `vehicle.soi` | `{"from_body": s, "to_body": s}` |
| `vehicle.rud` | `{"cause": "ground_impact"\|"ocean_impact"\|"collision"\|"excessive_g_force"\|"aerodynamic_forces"\|"hydrodynamic_forces", "peak_g": f, "peak_q_pa": f, "speed_ms": f, "altitude_m": f, "body": s, "crew_count": i}` |
| `vehicle.impact` | `{"speed_ms": f, "energy_j": f, "survived": b, "launch_pad": b, "body": s, "crew_count": i}` — `survived` = no destruction of same vehicle in same frame (mod-computed, §7.2) |
| `vehicle.staging` | `{"stage_index": i}` |
| `vehicle.docked` / `vehicle.undocked` | `{"other_flight": "<ulid>"}` |
| `engine.ignition` / `engine.shutdown` / `engine.flameout` | `{"engine": s(template name), "count": i}` |
| `kitten.eva_start` | `{"kid": s, "name": s}` |
| `kitten.eva_end` | `{"kid": s, "name": s, "duration_s": f}` |
| `kitten.tumble` | `{"kid": s, "name": s, "speed_ms": f, "body": s}` |
| `kitten.kia` | `{"kid": s, "name": s, "context": "rud"\|"manual_destroy"\|"unknown"}` |
| `roster.snapshot` | `{"kittens": [{"kid": s, "name": s, "travelled_m": f, "fastest_ms": f, "missions": i, "mission_time_s": f, "kia": b}]}` — every 10 min of play, and on session end |
| `flight.flagged` | `{"flag": "teleport"\|"refuel"\|"resource_edit"\|"console", "detail": s}` |
| `telemetry.window` | `{"t0_sim": f, "t1_sim": f, "n": i, "body": s, "alt_m": agg, "surface_speed_ms": agg, "orbital_speed_ms": agg, "accel_ms2": agg, "peak_g": f, "max_q_pa": f, "mass_kg_last": f}` — one per vehicle per 30 s sim-time of active flight |

`BEST-GUESS (D11)` crew-survival semantics used by projections: a lithobrake counts as *survived with crew* iff `vehicle.impact.survived == true && crew_count ≥ 1 && launch_pad == false` and no `kitten.kia` event exists for the same flight with `sim_t` within ±2.0 s of the impact. Revisit after in-game verification of `KillCrew` behavior.
