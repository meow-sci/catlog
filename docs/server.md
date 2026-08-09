# catlog server design (`server/`)

Owns **§5.1–§5.11**. The wire contracts it implements are in [ingest-api.md](ingest-api.md),
[events.md](events.md), [identity.md](identity.md) and [credential.md](credential.md); the reasons
behind everything below are in [DECISIONS.md](DECISIONS.md), areas `STORE-*`, `INGEST-*`, `IDENT-*`,
`PROJ-*` and `ARCH-*`.

Go module `github.com/meow-sci/catlog/server`, Go 1.26. Three binaries:

| Binary | What |
|---|---|
| `catlogd` | The server. Public mux on `:8080`, admin mux on `:6060`. |
| `catlogctl` | Admin CLI. An HTTP client for the admin API, plus two local-only verbs. **Never opens a database.** |
| `mockidp` | The local stand-in for Discord, Google and GitHub. Development only; never deployed, never proxied. |

## §5.1 Dependencies

Deliberately few, and every one is pinned. No web framework, no ORM, no cloud SDK.

| Package | Use |
|---|---|
| `turso.tech/database/tursogo` | The database driver, via `database/sql`. Pure Go through purego FFI. |
| `github.com/go-jose/go-jose/v4` | JWS, JWK, RFC 7638 thumbprints. |
| `github.com/oklog/ulid/v2` | ULIDs. |
| `github.com/andybalholm/brotli` | Ingest body decompression. |
| `github.com/klauspost/compress` | zstd for archive chunks and for payload compression. |
| `github.com/BurntSushi/toml` | Configuration. |
| `github.com/starfederation/datastar-go/datastar` | The SSE SDK for the server-rendered site. Import the `/datastar` subpackage — the module root has no importable package. |
| `github.com/testcontainers/testcontainers-go` | The nginx suite only, behind `//go:build docker`. |

Everything else is standard library: `net/http` (pattern routing and `CrossOriginProtection`),
`crypto/*`, `log/slog`, `encoding/json`, `expvar`, `net/http/pprof`.

**Two standing rules.** `tursogo`'s Go driver is generated from a spec that ships inside the module,
so every bump of it — or of `purego` underneath it — needs a behaviour re-probe, not just a green
build. And because the only importer of the docker-tagged dependencies sits behind a build tag,
`go get` records them as indirect; run `go mod tidy` (which considers all tags) after touching
`go.mod`.

## §5.2 Packages

```
server/
  cmd/catlogd/      wire everything; public mux :8080 + admin mux :6060; graceful shutdown
  cmd/catlogctl/    admin API client + keygen + testvectors
  cmd/mockidp/      §5.8.1
  integration/      the -tags integration suite: builds and runs the real binaries
  internal/
    config/     TOML load + CATLOG_* environment override + validation refusals
    ids/        ULID mint/parse (26-char string ↔ 16 bytes)
    clock/      the one clock catlogd reads; movable in development only
    cjws/       thin go-jose wrappers: SignES256, VerifyES256, Thumbprint, deterministic signing
    keys/       pepper, session key, license signing key; JWKS assembly; secret hygiene
    store/      Open, embedded migrations, typed queries. No ORM.
    authz/      license issue/verify, the §4.5.3 chain, deny-list, token buckets
    identity/   three IdP flows, user_key derivation, sessions, handles, moderation
    ingest/     caps, brotli, NDJSON decode, envelope validation, the write pipeline
    projector/  the fold loop, checkpoint, rebuild, upcasters, the feed broadcaster
    stats/      one fold per board + board metadata + the batching accumulator
    directory/  the in-memory player_id → handle map (the cross-file join)
    readapi/    every /v1/* JSON handler, CORS, privacy redaction, cache headers
    web/        html/template + datastar handlers for the server-rendered site
    adminapi/   the loopback-only admin mux
    archive/    Store interface + filesystem implementation + chunks and manifests
    units/      the single definition of what a catlog number looks like
    seed/       the deterministic demo dataset
    testvectors/ the §4.10 generator
    testutil/   throwaway stores, test credentials
    nginxproxy/ the docker-tagged nginx suite (§6.3)
```

Two package-level rules worth knowing before adding code:

- **Fold *writes* live in `stats`; projection *reads* live in `store`.** The `ON CONFLICT … WHERE
  excluded.value > player_stat.value` clause **is** the rule "ties keep the earliest claimant", so it
  belongs beside the board's meaning, not in the query layer.
- **Every package needs a non-HTTP entry point.** `identity` and `readapi` were each written with
  only a mux as their interface, and every later consumer — the server-rendered site, the compare
  endpoint — had to grow a new seam to reach them. A package whose only interface is its handler
  forces that on everyone who comes next.

## §5.3 Configuration

`server/catlogd.dev.toml` is committed and documented in place; production supplies the same keys
from the environment. Any value is overridable as `CATLOG_<SECTION>_<KEY>`.

`catlogd` takes four flags: `-config`, `-listen`, `-admin-listen`, `-version`, and one more that
exists for the container:

**`catlogd -healthcheck`** performs a single `GET /healthz` against this server's own listen address
and exits 0 or 1. It is the image's `HEALTHCHECK`, because the production runtime is a Docker
Hardened Image with no shell and no HTTP client — `HEALTHCHECK CMD curl …` is not available, and a
second binary shipped to make one request would be a second thing to build, stamp and keep in sync.

It **opens no database**, permanently and on purpose: tursogo's exclusive whole-file lock means a
probe that opened `events.db` would fail exactly when the server was healthy and succeed only when it
was not — an inversion that would restart a working server every fifteen seconds. It also rewrites a
wildcard bind address (`0.0.0.0`, `::`) to loopback, because those are bind addresses rather than
destinations and the container listens on one. The response body is compared byte for byte, not
merely for a 2xx: a proxy answering 200 with something else is not this server.

| Section | Key points |
|---|---|
| `[server]` | `listen`, `admin_listen`, `base_url` (the license `iss` **and** the proof `htu` base), `static_dir` (empty in prod — nginx serves it), `clock_control`, `max_stream_clients` |
| `[data]` | `dir`, `checkpoint_interval_s`, `compress_payloads` |
| `[ingest]` | `accepted_htu`, `max_body_bytes`, `max_events`, `max_inflight` |
| `[auth]` | `license_ttl_days`, `handle_quota`, `issuance_per_day`, `min_account_age_days`, `reserved_handles` |
| `[idp.*]` | Discord, Google, GitHub endpoints and credentials |
| `[limits]` | `ratelimit_per_jkt_per_s`, `ratelimit_burst`, `ratelimit_disabled` |
| `[boards]` | `min_players` — how many distinct players a *family* board needs before it is listed |
| `[projector]` | `batch_size`, `flush_rows`, `tick_s`, `decoders` |
| `[cors]` | `allowed_origins` — exact `scheme://host[:port]`, no wildcards |

**`Config.Validate` refuses rather than warns.** Two development-only settings — `clock_control` and
`ratelimit_disabled` — make catlogd **fail to start** when combined with an `https://` base URL, and
log a WARN naming the base URL for as long as they are on. A malformed CORS origin (a wildcard, a
trailing slash) is also a refusal, because such an entry silently never matches. The principle is
that a control which is absent cannot be half-on: `POST /admin/clock` is left *unmounted* rather than
mounted-and-refusing, and `ratelimit_disabled` removes the rate-limit step from the chain rather than
configuring an enormous rate.

## §5.4 Storage

Two files, `events.db` and `projections.db`, and they **cannot be joined** — `player_id → handle` is
resolved in Go through `internal/directory`, an in-memory map reloaded by every path that writes a
handle row.

### Turso discipline — hard constraints

- **One process per file.** The lock excludes other *processes*, not other handles in the same
  process. This is why `catlogctl` is an HTTP client, why `make e2e` uses its own data directory, and
  why deploys must fully stop the old process — no rolling or blue-green overlap is possible.
- **Two `*sql.DB` per file**: a writer with `SetMaxOpenConns(1)` and a reader with `SetMaxOpenConns(4)`.
  All writes go through the single writer goroutine or the admin mutex.
- **The WAL never auto-checkpoints.** Left alone the `-wal` grows for the life of the process while
  the main file stays near-empty. Every open database runs `PRAGMA wal_checkpoint(TRUNCATE)` on
  `[data] checkpoint_interval_s` and again at close. Backups must capture the `-wal` or checkpoint
  first.
- **No `VACUUM`, no `WITHOUT ROWID`, no `STRICT`, no expression indexes, no recursive CTEs.** The
  first two are available behind an experimental DSN flag and are deliberately not enabled; `WITH
  RECURSIVE` is unimplemented and no flag enables it, so projections, boards and the archive scan
  must all stay flat.
- **Constraint violations are indistinguishable.** The driver collapses every one onto a single
  sentinel with no extended result code, so dedup and upserts use `INSERT OR IGNORE` /
  `ON CONFLICT DO UPDATE` everywhere and derive their counts from `RowsAffected()`, never from error
  inspection.

### Schema

Migrations are embedded SQL under `store/migrations/{events,projections}/NNNN_*.sql`, applied in
order, one `Exec` per file inside its own transaction, recorded in
`schema_version(v, name, applied_at)`.

**`events.db`** — the immutable log and the identity state:

| Table | Holds |
|---|---|
| `player` | `player_id`, `user_key` (32 B, unique), `idp`, `created_at`, ban state |
| `handle`, `retired_handle` | Live handles (with a `handle_lc` unique index) and permanently retired ones |
| `credential` | One row per key thumbprint: `jkt`, player, handle, license `jti`, issue/expiry/revocation |
| `event` | `seq` (rowid — the server-local total order and the projector cursor), `event_id`, player, flight, session, `career`, type, `ver`, `sim_time`, `wall_time`, `recv_time`, `payload` |
| `ingest_batch` | `(player_id, batch_id)` — the replay short-circuit |
| `stream_state` | Per `(player, sid)`: `last_seq`, `last_bh`, `gap` |
| `tombstone`, `archive_cursor` | Purge records; the archiver's position |

`CREATE UNIQUE INDEX ev_dedup ON event(player_id, event_id)` is the dedup guarantee, and
`ev_player(player_id, seq)` keeps its `seq` column because the planner needs it.

**Payloads are zstd-compressed against a trained dictionary** (`compress_payloads`, migration 0003) —
measured **3.25×** on the payload column, because a majority of the dominant payload type is
byte-identical across events. Rows written either way stay readable, so the switch is always safe.

**`projections.db`** — everything rebuildable:

| Table | Holds |
|---|---|
| `proj_checkpoint` | One shared cursor for every fold |
| `player_stat` | `(player_id, stat) → value, context, updated_seq` — every board row |
| `flight_state` | Per flight: `flags` bitfield, `ended_reason`, crew, body |
| `player_body` | Distinct bodies per player and `kind` — `'soi'` (entered) and `'landed'` (touched down) — plus first-arrival times, which only `'soi'` rows carry |
| `kitten` | Per-kitten totals folded from `roster.snapshot` |
| `feed` | The activity feed, capped at 500 rows |
| `event_census` | One row per `(type, period, bucket)` — what makes `GET /v1/stats` affordable |

`flight_state.flags` is bit0 teleport, bit1 refuel, bit2 resource_edit, bit3 console, bit4 tuning,
bit5 other. **An unrecognised flag value sets bit5** — failing open would make every future flag a
scoring loophole for as long as the server lagged the mod.

## §5.5 Ingest pipeline

The handler runs §4.5.3 steps 1–10 inline with **no database writes**, decompresses and decodes the
body, then submits a `WriteJob` to a bounded channel. A **single writer goroutine** owns steps 11–13:
begin transaction, replay check, batched multi-row inserts, upsert `stream_state`, insert
`ingest_batch`, commit, reply, notify the projector.

Three bounds, and each answers `503` + `Retry-After: 5` rather than queueing forever:

- the write channel's capacity,
- `[ingest] max_inflight` — how many requests may hold an expanded body in memory at once, which is
  the knob that actually sizes peak ingest memory,
- a 30-second handler deadline.

Two consequences worth knowing. **A replayed batch still pays for its own decompression and envelope
validation** before the short-circuit is discovered — that is the trade that keeps the single writer
doing nothing but transaction work. And **cheap checks genuinely run first**: step 1 does its own
structural parse rather than going through the JWS parser, and the ordering is asserted directly by
counting ECDSA verifications, because it is a denial-of-service property rather than a style
preference.

## §5.6 Projector and the boards

One goroutine. On the writer's notification (or a fallback ticker), it reads `event` rows past the
checkpoint in batches of `[projector] batch_size`, decodes payloads across `decoders` goroutines,
applies every fold, and writes all projection updates **and** the checkpoint in one transaction —
then pushes feed rows to the SSE broadcaster.

**`stats.Batch` is why it is fast.** The projector used to issue about twenty-one SQL statements per
event; folding is dominated by per-statement cost, so a batch now folds into a read-through cache and
write-back accumulator, merges repeated writes to the same board, and flushes the survivors as a
handful of multi-row statements. That took the fold from ~3,300 to ~29,000 events/s, which is enough
to keep pace with ingest in real time. `TestBatchSizeDoesNotChangeTheProjection` is the test that
holds it honest: the projection may not depend on where the batch boundaries fell.

### The boards

Forty fixed keys, in publish order — which is the order `FixedBoards()` returns and therefore
the order `GET /v1/leaderboards` lists them, grouped by kind rather than by source event:

- **records** — `biggest_lithobrake_survived`, `peak_g_survived`, `max_q_survived`,
  `biggest_impact_energy`, `fastest_surface_speed`, `fastest_orbital_speed`, `fastest_entry`,
  `highest_altitude`, `lowest_pass`, `highest_apoapsis`, `lowest_orbit`, `roundest_orbit`,
  `steepest_orbit`, `softest_touchdown`, `softest_landing`, `heaviest_launch`, `heaviest_to_orbit`,
  `most_parts`, `biggest_stack`, `biggest_crew`, `biggest_recovery`, `most_stages`, `longest_eva`;
- **counters** — `kitten_tumbles`, `rud_total`, `orbits_achieved`, `soi_bodies`, `landed_bodies`,
  `landings`, `dockings`, `stagings`, `splashdowns`, `evas`, `flameouts`, `engine_ignitions`,
  `kittens_recovered`;
- **derived totals and per-kitten records** — `distance_travelled`, `top_kitten_distance`,
  `top_kitten_missions`;
- **career time** — `fastest_to_orbit`.

`docs/event-details.md` carries the canonical table: title, unit, direction, source event and fold
kind for every one of them, plus the eligibility rule board by board. Four of them
(`roundest_orbit`, `most_parts`, `most_stages`, `biggest_stack`) have an **empty** unit on purpose —
an eccentricity is dimensionless, and a bare count of a thing the title already names does not need
the word twice. `units.ForKey("stage_count")` falls through to `""`, which is correct and is why the
wire-v2 wave needed no `units` change and therefore no `spa/src/ui/units.ts` port edit.

**The five wire-v2 boards, and the pairing that justifies each one.** Every one of them exists
because it answers a question its nearest neighbour cannot:

| board | source | not the same as | because |
|---|---|---|---|
| `heaviest_to_orbit` | `vehicle.orbit.mass_kg` | `heaviest_launch` | what left the pad includes the propellant spent getting off it; what is still there at the milestone is the payload. Paired, the only honest efficiency-shaped number reachable without reading propellant |
| `softest_landing` | `vehicle.landed.vertical_speed_ms` | `softest_touchdown` | that board ranks the *whole* velocity relative to the ground. A rover at 8 m/s across a plain and a lander at 8 m/s straight down are the same number there |
| `landings` | `vehicle.landed`, counted | `landed_bodies` | worlds versus arrivals. `landed_bodies` still reads `vehicle.situation` and stays the sole writer of `player_body kind='landed'` |
| `lowest_pass` | `telemetry.window.radar_alt_m.min` | `highest_altitude` inverted | `alt_m` is barometric, so a low run down a canyon reads as high. This is the terrain-relative reading |
| `biggest_stack` | `flight.started.stage_count` | `most_stages` | built versus *fired*. A five-stage rocket that RUDs on stage two scores 5 here and 2 there |

`heaviest_to_orbit` requires `phase == "achieved"`; `softest_landing` and `landings` share one
`survivedLanding` gate so the two agree about which arrivals happened; `lowest_pass` refuses an
**absent** aggregate before it looks at a number. Each of the three that reads a newly-added key gates
`> 0`, which is what keeps a `ver` 1 history — where the key does not exist — off the board.
`heaviest_to_orbit` uses `> 0` rather than `ver >= 2` on purpose: the two are the same predicate, and
`> 0` matches every other gate in `boards.go` so no reader has to know that one board consults the
envelope.

**Newly decoded in `stats/payload.go`.** `FlightStarted` gained `kids []string`, `stage_count int`
and `lat`/`lon *float64`; `FlightEnded` gained `kids`, `body string` and `lat`/`lon`;
`VehicleSituation` gained `radar_alt_m *float64`; `VehicleOrbit` gained `mass_kg float64`;
`VehicleRUD` and `VehicleImpact` gained `lat`/`lon`; `TelemetryWindow` gained `radar_alt_m *Agg` and
`warp_max float64`; and `VehicleLanded` is a new struct. **Every optional key is a pointer**,
following the `peak_g` precedent — absent is `nil`, an explicit 0 is a pointer to 0 — because a
zeroed latitude is the equator and a zeroed radar altitude is the ground. `FlightStarted` and
`FlightEnded` are no longer comparable structs, since they hold a slice; nothing compared them with
`==` and nothing should.

Most of those fields are decoded and read by no fold, deliberately: the immutable log is the product
and a board for each of them is a separate decision. The one with an obvious consumer is
`flight.ended.body` — `flight_state.body` still comes only from `flight.started`, so a flight whose
start was never folded still has an empty body although its end now carries one.

**Two families are not fixed:** `rud_<cause>` and `fastest_to_<body>` take their second half from the
event stream, because KSA's celestial systems are content that mods extend and `body` is opaque to
the server. There is no allow-list of places. What a name must satisfy is only that it can *be* a
stat key — lowercase, `[a-z0-9]` then `[a-z0-9._-]`, at most 40 characters, because a stat key is a
URL path segment. That is protocol hygiene, not a list of approved worlds.

Such a board is **listed** by `GET /v1/leaderboards` once `[boards] min_players` distinct players
hold a value on it (default 2). Below that it still exists, is still served at its own URL, and still
shows on the profile of whoever is on it. The threshold gates the *index* only — it stops one
modified client filling the public list with invented place names, and a leaderboard with one entrant
is not a leaderboard. Nothing is lost while waiting: the per-player value is recorded for every body
and cause regardless, so lowering the threshold publishes history that is already there.

**Rules every fold obeys:** flagged flights score nothing (all of them, not only the record boards —
a counter board on a cheated flight is just as wrong); ties keep the earliest `updated_seq`; and
`ascending` boards exist, where the smallest value wins. Ascending is **no longer only** a career-time
property: `lowest_orbit`, `roundest_orbit`, `softest_touchdown`, `softest_landing` and `lowest_pass`
are ascending records, which is why each of them refuses a zero — on a board where small wins, a zero
from a value the game never wrote is an unbeatable record nobody flew. On the two newest that is
literal rather than theoretical: 0 m/s of descent is an unreadable state-vector decomposition, and
0 m of ground clearance is where every vehicle sits on the pad.

**Two rules the wire-v2 boards do *not* get, and must not.** There is no plausibility check on a
landing — a one-metre hop is a landing, and filtering on "was that real" infers intent from data
shape, which Constitution §8 forbids. And `telemetry.window.warp_max` is descriptive only: it may
inform a reader, weight or annotate a value, but it must never reject or disqualify a record, and it
is not a cheat signal.

**The flag exclusion is structural, not universal.** `scoreable` passes every event that carries no
flight, and the wire sends `flight: null` for `roster.snapshot` and `kitten.eva_end`. So
`distance_travelled`, `top_kitten_distance`, `top_kitten_missions` and `longest_eva` have no flag
exclusion at all, and `evas` has one only when the EVA signal carried a vehicle id. That is a property
of the source events; the fix would be on the mod side.

**Rolling windows** are a dimension of a board, not a board. Daily / weekly / monthly / yearly
buckets hang off the four write helpers, so they compose with the dynamic families for free. Buckets
derive from the event's `recv_time`, never from the wall clock — which is what makes a rebuild
reproduce them.

### Rebuild — the correctness backstop

Admin-triggered and nightly. It builds into a fresh file from seq 0 and swaps it in, and it is **two
passes**: pass 1 folds flight state and collects the `kitten.kia` index; pass 2 folds the boards
against a flight state that is already complete for all of history.

That is what heals the things the incremental path cannot know at fold time: a `flight.flagged` that
arrives *after* its flight scored, the ±2 s KIA window on the two impact boards, and the
`ended_reason == 'recovered'` condition on the two structural-load boards. So **rebuild ≠ incremental
whenever a history contains a late flag, a scuttled kitten, or a flight that did not end recovered** —
that is the point of the design, not a defect. Feed rows are a fourth divergence: they resolve the
handle from the live directory at fold time and pass 2 re-renders them, so a player banned since is
absent from a rebuilt feed and present in an incremental one.

**A rebuild is also the migration whenever a build gains a decoder or a fold.** Events already in
`events.db` that no fold read produce board rows on the next rebuild and never on the incremental
path, because the incremental path has already passed them. The board expansion is exactly that case:
until a server rebuilds, the boards fed by the newly-decoded types are short by their whole history.

**The wire-v2 boards are the *other* case, and the two are easy to confuse.** They read keys that
were never sent, not keys that were sent and ignored, so a `ver` 1 payload replayed through the new
decoders yields the same 0 or the same absence and every gate refuses it; `vehicle.landed` was
rejected outright at ingest until this build, so it has no history at all. **All five start empty and
fill from the first wire-v2 batch.** A rebuild recovers exactly one thing here: events a `ver` 2 mod
shipped to a server whose projector still folded `ver` 1, which were skipped as a future version and
are sitting in the log unfolded. `launchFold`'s context blob also gained `stage_count`, so rows
written before this build keep the five-key shape until they are beaten or the projection is rebuilt;
both paths produce the six-key shape, so this is not a divergence.

The swap keeps the old file until the reopen succeeds: close → delete the stale `-wal`/`-shm` →
rename live to `.old` → rename the rebuild in → reopen → delete `.old`, restoring `.old` on failure.

**An event the projector cannot decode is skipped and the checkpoint still advances** — one event
from a newer mod must never wedge every projection behind it. The skip log is deduplicated per
`(type, ver)` and carries no payload, because payloads are player-supplied and unbounded.
`projector.Upcasters` holds **nine** entries, and every one of them is the **identity**.
`kitten.tumble` and `kitten.kia` are `ver: 2` because both gained a non-null `flight` — an envelope
change, with the payload bytes unmoved. The seven wire-v2 types are `ver: 2` for the mirror-image
reason: each `ver` 2 payload is its `ver` 1 payload *plus* keys, in that order, with nothing renamed,
retyped, re-unitted or removed, so a `ver` 1 event still folds correctly and simply says less. That
is what let all seven move in one commit. The prediction the empty registry was built on has now held
twice — a version bump is a `Register` line, not a migration — and the entries are still required,
since `Apply` refuses to fold a row it cannot bring to the current version.

The identity is only *safe* because each new key's absence is refused **downstream** rather than read
as a zero, which is where the three `> 0` gates above earn their keep. `vehicle.landed` gets no entry
at all: it is new at `ver: 1`, which is already `CurrentVer`.

`currentVer` must equal the mod's `EventTypes.Versions` exactly, or a newer mod's events are skipped
as a future version until this build catches up and a rebuild runs (PROJ-092). `knownTypes` must
equal the mod's registry name for name and index for index — 23 entries since `vehicle.landed` was
added between `vehicle.impact` and `vehicle.staging`. Until a type is in that list the server answers
`400 malformed_batch` for the **whole batch**, so a wire-v2 mod shipping to a wire-v1 server loses
everything, not just landings: the mod change and the server change have to merge together (PROJ-093).

## §5.7 The server-rendered site

`html/template` via `go:embed`, with datastar for interactivity. Routes: `/`, `/boards`,
`/boards/{stat}`, `/p/{handle}`, `/p/{handle}/events`, `/events`, `/stats`, `/search`, `/compare`,
`/login`, `/dashboard`, `/docs/{install,privacy,api}`, and a catch-all 404.

It renders the **same numbers** the read API publishes by calling `readapi`'s exported query methods —
a second implementation would have been a second place for a banned player to reach a public surface.

The feed is rendered server-side and then replaced by the stream, and the list carries
`data-source="ssr"|"sse"` so a test can tell an open stream from a page whose module never loaded.
The credential wizard generates its key pair in the browser and **only ever transmits the public
half**; the private key is exported once, straight into the download.

Design detail is in [ui-design.md](ui-design.md).

## §5.8 Identity, sessions and the deny-list

One generic OAuth2 code engine parameterised per provider, all URLs from configuration. Google
additionally verifies the `id_token` against the issuer's JWKS — cached, and refetched on a `kid`
miss as well as on expiry, so a provider rotating keys recovers with no restart. An IdP's `id_token`
may be RS256 or ES256; catlog's *own* JWS allow-list stays exactly `{ES256}`.

The deny-list is an in-memory `{subs, jkts}` loaded from the database at start and refreshed on every
mutation, published as a signed compact JWS at `/.well-known/catlog-denylist.json` and verifiable
against `/.well-known/catlog-jwks.json`.

Moderation semantics, which are easy to get subtly wrong:

- **Ban** retires the `handle_lc` but keeps the `handle` row, so an unban can hand it back. It
  revokes credentials with one timestamp, and **unban restores exactly those** — a credential the
  player revoked themselves stays revoked.
- **A banned player is *absent* from the directory, not marked in it.** Every read surface therefore
  fails closed by construction, and `GET /v1/players/{handle}` answers 404 identically for unknown,
  retired and banned handles — distinguishing them would make the endpoint a ban oracle. Ranks
  subtract banned players who outrank the profile, so a ban closes the gap rather than leaving a hole.
- **A purged account cannot log in again**: tombstones are read as banned subjects, so the login path
  refuses rather than minting a session whose licenses ingest would reject. Delete-my-data is a purge.
- At ingest, a ban surfaces as `banned` and a bare revoke as `license_revoked`.

### §5.8.1 `mockidp`

Serves Discord, Google and GitHub on `:9090` with the exact response shapes catlog reads, from a
committed cast in `server/mockidp.toml`. Each `[[user]]` becomes a "Login as …" button with a stable
DOM id. Snowflakes and `created_at` values are computed so the 30-day account-age gate is exercised
**both ways**.

**It rejects any authorize request carrying an email scope.** catlog never requests one, and a mock
that happily granted it would let the rule rot into a comment.

`POST /generate` mints synthetic subjects for the load harness. The committed cast and its DOM ids
are byte-for-byte untouched by it — there is a test that diffs all four consent pages before and
after generating thirty accounts and fails if a single byte moved.

## §5.9 Admin API and `catlogctl`

Bound to loopback, unauthenticated, **never proxied** — and the mux additionally refuses any
non-loopback peer, so a one-line typo in `admin_listen` fails closed instead of opening credential
issuance to the world.

| Route | `catlogctl` | Does |
|---|---|---|
| `POST /admin/issue` | `issue` | Mint a credential. The CLI generates the key locally and sends only the public JWK. |
| `POST /admin/ban` / `unban` / `purge` | same | Moderation, including archive prefix deletion |
| `POST /admin/projections/rebuild` | `rebuild` | §5.6 rebuild and swap |
| `POST /admin/archive/run` / `restore` | `archive` / `archive-restore` | §5.10 |
| `POST /admin/backup` | `backup` | Quiesce the writer, copy `events.db` **and its `-wal`** |
| `POST /admin/seed` | `seed` | The deterministic demo dataset |
| `POST /admin/events` | — | Insert events directly. The dev-loop tool: push one, watch the feed. |
| `POST /admin/clock` | — | Move the server's notion of now. Development only, mounted only when enabled. |
| `POST /admin/denylist/publish` | `denylist` | Regenerate the signed deny-list |
| `GET /admin/stats` | `stats` | Counters, both file sizes, both WAL sizes, projector lag |
| — | `keygen`, `testvectors` | Local only; touch no server |

Also on `:6060`: `net/http/pprof` and `expvar`. **All `ingest_rejected_<code>` counters are published
at init**, including codes ingest cannot produce, so a dashboard never has to guess whether a missing
variable means "zero" or "not wired".

`GET /admin/stats` is the deterministic "has everything landed" primitive: the projector has caught
up when `lag_seq == 0` **and** `checkpoint_seq == events.max_seq`. Both halves are load-bearing — on
an empty log the lag is zero because there is nothing to do, and a checkpoint at the head cannot on
its own distinguish "caught up" from "the fold loop is not running". Nothing in the test harnesses
sleeps and hopes.

## §5.10 Archiver

```go
type Store interface {
    Put(ctx, key string, r io.Reader) error
    List(ctx, prefix string) ([]string, error)
    Delete(ctx, prefix string) error
}
```

`fsStore` roots at `data/archive/`, using the key layout an R2 bucket would use, so the migration is
`rclone copy` and one new file with four methods. Reads are a separate optional `Getter` interface —
a store that cannot read can still be archived to, and says so when asked to restore.

A run reads events past `archive_cursor`, groups by player, and appends one zstd NDJSON chunk per
player per run. **The chunk line is the wire envelope plus `_seq` and `_recv`**, both underscored
because they are not envelope fields; `_recv` is load-bearing, since without it a restored event gets
a new `recv_time` and the restored log is no longer the log that was archived.

**Determinism is bought with two encoder settings** — default speed and `WithEncoderConcurrency(1)`,
because a concurrent encoder's block boundaries land in the output — plus ordering players by
`player_id` rather than map iteration.

**The cursor moves last**, so a crash mid-run leaves it where it was and the retry writes the same
keys with the same contents. Restore preserves `seq` and `player_id` (the disaster-recovery promise
is that a rebuild over a restored log produces *the same* projections, not merely equivalent ones),
verifies every chunk's SHA-256, length, event count, declared range and ordering **before writing a
row**, and refuses a conflict rather than merging two histories.

Restore brings back the event log and the `player` rows and nothing else — handles, credentials and
bans are identity state and are not archived. The runbook is therefore "restore the backup, then
replay any archive newer than it". [r2-archive-design.md](r2-archive-design.md) documents the R2
implementation, which is **designed and not built**: no cloud SDK is a dependency, and none will be.

## §5.11 Logging and secret hygiene

`log/slog` JSON to stdout. Every ingest rejection logs one WARN line, rate-limited per thumbprint so
hostile traffic cannot flood the log.

**Secret hygiene is enforced by types, not by discipline.** `keys.Set` and `keys.UserKey` implement
both `slog.LogValuer` and `fmt.Stringer`, so neither structured logging nor a stray `%v` can emit the
pepper, the session key or a full `user_key` — a `user_key` always renders as its ≤8-character
base64url prefix. `keys` also **refuses to load a secret that is group- or world-readable** (a hard
error, not a warning) and writes every secret `O_EXCL` at `0600` under a `0700` directory: a leaked
pepper is unrecoverable, because it would let an attacker link every `user_key` back to an IdP
subject.

## Response compression is the proxy's job

`catlogd` emits uncompressed responses and will keep doing so — no middleware, no framework. Both
nginx configs carry `gzip on` for the page and `/static/` locations, and a CDN compresses again at
the edge. So a dev browser's network tab showing uncompressed JSON is expected, not a bug; and a
deployment with no compressing proxy in front should not be the origin a cross-origin reader is
pointed at.

**Two locations must never gain compression, anywhere:** `/v1/feed/sse`, because buffering defeats
streaming, and `/v1/ingest`, whose request body is hashed and verified byte for byte.
