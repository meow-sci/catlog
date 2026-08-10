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
    seed/       deterministic event histories for populated demo boards and badges
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
| `[projector]` | `batch_size`, `flush_rows`, `tick_s`, `decoders`, `auto_rebuild` |
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
| `event` | `seq` (the server-local total order and the projector cursor), `event_id`, player, flight, session, `career`, type, `ver`, `sim_time`, `wall_time`, `recv_time`, `payload` |
| `event_seq` | The explicit `seq` allocator (migration 0004) |
| `shadowban`, `shadowban_event` | The shadow-ban roster and the withheld half of the log (migration 0005) |
| `ingest_batch` | `(player_id, batch_id)` — the replay short-circuit |
| `stream_state` | Per `(player, sid)`: `last_seq`, `last_bh`, `gap` |
| `tombstone`, `archive_cursor` | Purge records; the archiver's position |

`CREATE UNIQUE INDEX ev_dedup ON event(player_id, event_id)` is the dedup guarantee, and
`ev_player(player_id, seq)` keeps its `seq` column because the planner needs it.

**`event.seq` is allocated explicitly, not by the rowid** (migration 0004). SQLite hands out
`max(rowid) + 1`, which reuses a number whose row was deleted — and rows *are* deleted, by a purge
(§4.7) and by a shadow ban's move (below). A reused seq is already behind the projector checkpoint
and the archive cursor, so the re-issued event would be stored and then never folded onto a board and
never archived, silently. `event_seq.next_seq` only moves forward; `InsertEvents` reserves a run of
numbers inside the caller's transaction, `RestoreEvents` lifts the floor past anything it inserted at
an explicit seq, and `OpenEvents` reconciles the allocator against both event tables at every start.
Gaps are expected: every reader scans `seq > cursor` and none assumes the column is dense.

**Shadow bans move rows rather than filtering them** (migration 0005). `shadowban_event` mirrors
`event` column for column, **including `seq`**, so withholding a player is one `INSERT..SELECT` plus a
`DELETE` and lifting the ban is the same in reverse — at the original sequence numbers, which is what
makes a restore reproduce the same boards down to the "who got there first" tie-break. The exclusion
is structural rather than a predicate: every public surface reads either `event` or a projection
folded from it, so a withheld player is absent from both by construction and a read path added later
inherits that without knowing the feature exists. `PurgePlayer` deletes from both tables, because an
account deletion that left a copy of the log behind would be a privacy failure.

**Payloads are zstd-compressed against a trained dictionary** (`compress_payloads`, migration 0003) —
measured **3.25×** on the payload column, because a majority of the dominant payload type is
byte-identical across events. Rows written either way stay readable, so the switch is always safe.

**`projections.db`** — everything rebuildable:

| Table | Holds |
|---|---|
| `proj_checkpoint` | One shared cursor for every fold |
| `proj_build` | The build stamp: which binary's fold set produced this file (migration 0005) |
| `player_stat` | `(player_id, stat) → value, context, updated_seq` — every board row |
| `career_stat` | `(player_id, career, stat) → system, value, context, updated_seq` — the same board keys ranked per save; `system` is denormalised so filtering does not require a join (migration 0006) |
| `system_stat` | `(player_id, system, stat) → value, context, updated_seq` — board rows ranked within a celestial-system identity (migration 0006) |
| `flight_state` | Per flight: exclusion flags, ending/launch facts, first nonempty career, set-only achievement milestones, and the earliest achieved-orbit sequence. Migration 0009 owns nullable `engine_count`; migration 0010 adds the retained facts/milestones; migration 0012 adds `first_orbit_seq` |
| `career` | Per save: sim-time high-water and rewind mark, first/last event seq, public ordinal, first celestial-system identity and non-punitive `system_changed` provenance mark; `last_seq` advances on every attributed event, including non-scoring and flagged activity |
| `player_body` | Distinct bodies per player and `kind` — `'soi'` (entered) and `'landed'` (touched down) — plus first-arrival times, which only `'soi'` rows carry |
| `career_body` | Distinct members per save and `kind`, with the career's system identity denormalised; SOI `first_sim_t` supports per-save arrival sprints, and its novelty signal is independent of `player_body` (migration 0007) |
| `kitten` | Per-kitten totals folded from `roster.snapshot` |
| `career_kitten` | Per-save kitten totals folded from `roster.snapshot`, with system identity denormalised; unlike `kitten`, it does not merge same-named kittens from different saves (migration 0007) |
| `system` | One immutable first-seen identity/header per celestial-system hash: raw system id, display name, stable URL slug, home body, declared body count, monotone reported-complete bit and first seq (migration 0008) |
| `system_body` | One immutable first-seen catalogue row per `(hash, body)`, including opaque game class, semantic kind, forest topology, physical values, orientation and optional orbital elements (migration 0008) |
| `badge_award` | Current-projection merit-badge awards at lifetime and per-save scope, with first-award sequence, server receive time, optional career time and provenance (migration 0011) |
| `challenge_stat` | Retained ranked values for explicitly dated challenges at player, save or system scope (migration 0013); H1 establishes storage only, before challenge definitions and folds |
| `challenge_member` | Retained distinct facts used by set-valued challenge rules, with first sequence and the same scope sentinels as `challenge_stat` (migration 0013) |
| `feed` | The activity feed, capped at 500 rows |
| `event_census` | One row per `(type, period, bucket)` — what makes `GET /v1/stats` affordable |

`career_stat` has primary key `(player_id, career, stat)`, rank index
`(stat, value, updated_seq)`, and system-filter index `(stat, system, value, updated_seq)`.
`system_stat` has primary key `(player_id, system, stat)` and rank index
`(stat, value, updated_seq)`. Neither table carries a period: a save is already a time scope, and
crossing careers with rolling buckets would add an unbounded fourth storage dimension. Both tables
are empty until their board folds land; migration 0006 establishes the final schema first.

`career_body` has primary key `(player_id, career, kind, body)` and an index on
`(player_id, system, kind, body)` for distinct-member system unions. Its implemented kinds are
`'soi'`, `'landed'` and `'orbit_kid'`; the last stores the kitten id in the existing `body` member
column and is never written to `player_body`. `first_sim_t` is populated only for SOI arrivals.
Adding `'orbit_kid'` changes no schema: it reuses the generic member column and migration 0007
table unchanged.
For `kind='soi'`, `first_sim_t` is the earliest observed arrival in that save. The world-sprint
projections count rows at or before their flat duration threshold per save, then retain the best
single-save result at player and known-system scope; they never union early arrivals from several
saves. A timed SOI member may carry an empty system so the save and player sprint results survive
missing system identity; only the system-scoped result is omitted.
`career_kitten` has primary key
`(player_id, career, kid)`, plus indexes on `(player_id, career)` and `(player_id, system)`. Both
tables denormalise `system` from the career, and both are rebuildable additions from migration 0007.

Migration `0011_badges.sql` adds
`badge_award(player_id, career, badge, system, first_career, earned_seq, earned_at,
earned_sim_t, context, PRIMARY KEY(player_id, career, badge))`. It has indexes
`badge_system(system, badge, earned_seq)`, `badge_holders(badge, earned_seq)` and
`badge_by_career(player_id, career, earned_seq)` (`0011_badges.sql:6-20`). The empty career is the
lifetime scope; a nonempty career is that save's independent award. A lifetime row retains the
system and save in which the current projection first awarded it. Per-save rows retain their system
and leave `first_career` empty because `career` already identifies the save. Migration 0011 advances
the projection schema to version 11, and `ProjectionCounts.BadgeAward` includes the table in the
admin projection census (`store/projections.go:716,754`).

The store read seam exposes complete `BadgeRow` values, including `system`, lifetime
`first_career`, nullable `earned_sim_t` and nullable JSON context. `BadgesForPlayer` always applies
an exact career filter: the empty sentinel means lifetime, not every scope. Unfiltered holder reads
use only those lifetime rows. A system-filtered holder read instead ranks nonempty per-save rows in
that system by `(earned_seq, career)`, keeps one row per player, and then orders players by
`(earned_seq, player_id)`. This includes a player whose lifetime first award belongs to another
system without counting several qualifying saves as several holders. `BadgeHolderCount` uses the
same population, and `BadgeCounts` groups lifetime rows only
(`store/projections.go`, merit-badge reads).

`GET /v1/stats` reports `collection.badges` as the number of badge keys with at least one lifetime
holder and `collection.badge_awards` as all current lifetime plus per-save award rows. Both are part
of the existing whole-response cache keyed by projection `WriteGen` with a 10-second TTL
(`readapi/stats.go`). `readapi/badges.go` uses the same store seams for the four public badge routes:
the stable/gated catalogue, lifetime or system-filtered distinct-player holder pages, and lifetime
or exact-save checklists. It resolves raw save provenance to ordinals and per-player labels before
building any response and passes every context through the shared recursive redactor.

The HTML saves index needs all per-save badge counts at once. `BadgeCountsByCareer` groups one
player's non-lifetime award rows in one store query; `readapi.SaveBadgeCounts` resolves those private
career keys against the player's saves and returns only ordinal-to-count pairs. The web package uses
that narrow non-HTTP seam instead of issuing one checklist read per save, and never receives a raw
career key (`store/projections.go`, `readapi/badges.go`, `web/pages.go`).

Migration `0013_challenges.sql` adds the challenge projection foundation. `challenge_stat` is keyed
by `(player_id, career, system, challenge)` and ranked through
`challenge_rank(challenge, system, value, updated_seq)`. `challenge_member` is keyed by
`(player_id, career, system, challenge, member)` and indexed by
`challenge_member_count(challenge, player_id, career, system)`. Player scope uses empty career and
system; save scope uses its career and bound system; system scope uses empty career and its system.
Those non-NULL sentinels prevent a mixed player aggregate from being labelled with whichever system
contributed last.

Challenge rows have no retention. They do not reuse `player_stat_period`, whose calendar buckets
are deliberately aged out: the archive of a closed challenge is part of the feature. Definitions
will be compiled into the server so an incremental fold and a later rebuild cannot silently apply
different mutable rules. H1 does not yet define or fold any challenge. Its batch foundation only
loads one scoped `challenge_member` set on demand, merges pending additions, and flushes new members
in deterministic key order, making future set-valued folds independent of projector batch size.
Both challenge tables are player-owned structural projections for moderation/rebuild purposes.
`ProjectionCounts` and the cached public collection census expose their current row counts as
`challenge_stat`/`challenge_member` and `challenge_stats`/`challenge_members`, respectively.

Awards are insert-once inside one projection build: the first qualifying event keeps its
`earned_seq`, matching server-side `recv_time` in `earned_at`, nullable event career clock in
`earned_sim_t`, and nullable JSON `context`. `earned_at` never trusts the client's wall clock.
Context is projector-authored and promises the same shape and public treatment as
`player_stat.context`: recursive career/kitten relabelling happens before publication, and the
default display allow-list remains exactly `body`, `from`, `energy_j` and `t1_sim`; it is not
arbitrary client JSON. There is no revocation column or accumulated punishment. A rebuild creates a
fresh table from the live immutable log and is authoritative: current folds and final state may omit
an award that an earlier build contained, or discover one in old history.

`badge_award` is player-owned projection output and therefore needs no moderation-specific delete
list. A shadow ban structurally removes the player's events from the live log; the queued rebuild
drops their awards, and restoring the events at their original sequence numbers plus rebuilding
restores the same eligible awards. A purge removes the source events permanently, so rebuilding
cannot recreate the rows. This is the STORE-019 rule; shared `system` and `system_body` catalogue
facts remain governed by their content, not by whichever player first reported them.

`system` is keyed by the content hash, **not** by `system_id` or display name: two mods may both
call different content `Sol`. Its unique `slug` is ASCII-only and rebuild-stable. Lowercase ASCII
letters and digits survive; each run of every other byte becomes one hyphen; leading/trailing
hyphens are removed and the base is capped at 48 bytes. An empty base falls back to the first eight
hash characters. Distinct hashes with the same base receive `-2`, `-3`, … in ascending `first_seq`
order. This is deliberately separate from `statSuffix`, whose protocol-key alphabet must not be
weakened to accommodate human display names such as `Solar System (Dense)`.

`system_body` has primary key `(hash, body)` and index `(hash, kind, body)`. `class` is KSA's own
opaque string and has **no allow-list**. The six orbital-shape columns are nullable as a group;
`period_s` is independently nullable; `parent` is null on every root. Orientation is stored as four
required quaternion columns, not recomputed. Neither table has a foreign key between them: a body
may arrive in an earlier projector batch than its header, and creating a placeholder system would
invent a name and slug that later need mutation rules. Orphan body rows remain internal until the
real header arrives.

Migration 0008 also adds `career_system(system, player_id)`, the covered path for counting players
and saves attached to a system without consulting score rows. A save that loaded a system but never
scored still belongs to that system.

Both tables are immutable first-write projections. A repeated matching header may only promote
`reported_complete` from 0 to 1; a later false cannot erase it. Conflicting identity fields retain
the first row and emit a structured warning naming the hash, current seq and first seq. Duplicate
body rows use `ON CONFLICT DO NOTHING`, so a differing replay also retains the first row. This is
deterministic projection integrity, not a plausibility or anti-cheat check.

The completeness exposed to readers is **effective completeness**:

```text
reported_complete == 1 AND count(system_body WHERE hash = system.hash) == body_count
```

The header bit alone is insufficient: an interrupted large catalogue must not award an everywhere
result before its last row. Conversely, a later false header cannot regress a catalogue that was
previously received completely.

`flight_state.flags` is bit0 teleport, bit1 refuel, bit2 resource_edit, bit3 console, bit4 tuning,
bit5 other. **An unrecognised flag value sets bit5** — failing open would make every future flag a
scoring loophole for as long as the server lagged the mod.

Migration `0009_flight_engine_count.sql` adds nullable `flight_state.engine_count`. The
`flight.started` fold writes the decoded `*int` without collapsing it: SQL `NULL` means the start
event has not been folded or its KSA read was absent, explicit 0 means the vehicle began that flight
with no installed rocket engine, and a positive value is the installed count. No current board fold
reads the column; retaining the launch fact now is what lets a later challenge remain a projection
of the immutable log rather than an inference from subsequent motion.

Migration `0010_flight_facts.sql` adds `milestones INTEGER NOT NULL DEFAULT 0`, nullable
`part_count INTEGER`, nullable `launch_mass_kg REAL`, and `career TEXT NOT NULL DEFAULT ''`.
`engine_count` is deliberately not repeated there: migration 0009 already owns it. On a decoded
`flight.started`, `StartFlight` records crew, body, the exact absent/0/positive engine count, part
count, launch mass and `started_seq`. Until that event is observed the three launch-fact columns are
SQL `NULL` and `started_seq` is 0. `EnsureFlight` runs for every flight-bearing event and retains the
first nonempty career; neither a later empty value nor a later event replaces it.

`milestones` records set-only historical facts, separate from exclusion `flags`: bit 0 orbit
achieved, bit 1 atmosphere exited, bit 2 entered an SOI other than the known launch body, bit 3
survived a landing, bit 4 docked. `MarkFlightMilestone` only ORs a bit, so neither incremental fold
order nor a rebuild can clear an observed achievement. `MilestoneOtherSOI` is conservative: the SOI
event may set it only when a real `flight.started` has already supplied a nonempty launch body at
`started_seq <= event.seq`, and `to_body` differs. An early SOI is never retroactively upgraded when
the start arrives. The raw orbit bit is different: any decoded `phase == "achieved"` sets it even if
the start event is later; start ordering applies only when a consumer needs a start fact.

Migration `0012_flight_orbit_seq.sql` adds `first_orbit_seq INTEGER NOT NULL DEFAULT 0`. An achieved
orbit sets the milestone bit and lowers this column to that flight's earliest positive event
sequence. The bit answers whether orbit ever happened; the sequence answers whether it happened
strictly before a later candidate. Keeping both prevents the rebuild's completed first pass from
making a docking or recovery that precedes orbit look eligible.

The batch cache keys each `flightEntry` by flight id and carries the other twelve columns in the
entry. Its read-through `SELECT`, `FlightState`, sorted dirty-id flush and 13-placeholder
`INSERT … ON CONFLICT DO UPDATE` use the same order: `player_id, flags, ended_reason, crew, body,
started_seq, engine_count, milestones, part_count, launch_mass_kg, career, first_orbit_seq` after
`flight_id`. This is
load-bearing: pending events in one
projector batch must see the same accumulated facts and milestone bits that a subsequent batch reads
from SQL. `FlightState.HasStartFactAt(candidateSeq, factValid)` is the shared fact-order predicate:
an actual start exists, its sequence is not later than the candidate, and the nullable fact needed by
that consumer is present.

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
event; folding is dominated by per-statement cost, so a batch now folds into read-through caches and
write-back accumulators, merges repeated board and badge writes, and flushes the survivors as a
handful of multi-row statements. That took the fold from ~3,300 to ~29,000 events/s, which is enough
to keep pace with ingest in real time. `TestBatchSizeDoesNotChangeTheProjection` is the test that
holds it honest: the projection may not depend on where the batch boundaries fell.

Badge writes use a composite `(player_id, career, badge)` pending key. The pending value keeps all
first-award provenance together and is replaced only by a lower `earned_seq`; `HasBadge` reads that
map before SQL and caches both presence and absence so same-batch composite logic sees unflushed
awards without repeated queries. A row loaded from SQL is immutable in the cache. `flushBadges` runs
immediately after `flushCareerStats` and before `flushSystemStats`, sorts by player id, career and
badge, chunks nine-column rows by the Batch flush-row bound and uses
`INSERT ... ON CONFLICT DO NOTHING`. The in-memory lowest-sequence merge and SQL no-op are both
required: the former preserves the earliest candidate offered inside one flush window, while the
latter preserves the row an earlier flush already committed. Cache entries survive a flush with
their pending marker cleared (`stats/batch.go:1545-1638,1873-1884`).

Threshold folds read the effective post-board value through `StatValue` and `CareerStatValue`.
Those caches retain whether the stored row exists as well as its numeric value, merge pending count,
record, best and set writes over that baseline, then update the cached effective value as later
writes arrive. The existence bit matters for ascending boards: an absent row is not a real zero that
can defeat the first positive best value. A threshold therefore fires on the event that
crosses it whether that event shares a large projector batch with earlier contributions or follows
a flush/reload boundary.

The shared `award` helper writes one lifetime candidate and, when a career exists, one independent
per-save candidate with identical sequence, server receive time, nullable simulation time and
context. `HasSimTime` distinguishes an absent clock from a real zero, and a context-encoding failure
writes neither scope. It resolves the career's system once; the lifetime row retains that career as provenance,
whereas the save row already carries it in its key. The helper does not decide eligibility; each
registered concrete fold supplies its compile-time or validated family key and owns the established
flight/board eligibility rule (`stats/fold.go:296-313`). F5 registers 33 fixed folds and three
dynamic family folds. F7 adds the two fixed effectively-complete-system subset folds, making all 35
fixed badges active.

`Batch.BodiesNotVisited` resolves the save's bound system, requires a reported-complete header whose
declared `body_count` exactly equals the effective catalogue row count, and refuses an empty selected
subset. Its per-system body→kind cache merges unflushed `system_body` inserts, while the existing
per-player career-body cache already includes unflushed SOI membership. The final missing arrival
therefore satisfies the subset in the same large batch just as it does after a flush. Kind
`"planet"` selects the game-emitted normalized planet rows; empty kind selects every body, including
parentless roots and opaque future classes. No server body list or concrete-class mapping exists
(`stats/batch.go`, `stats/system.go`, `stats/badgefolds.go`).

State folds run `systemFold → flightFold → careerFold` before every board. `systemFold` is first
because `system.discovered` precedes `session.started` in the same client boundary and board folds
read the career's system through the Batch cache. Recording the system and binding the career before
the career clock advances makes the buffered incremental path match a rebuild. `system.body` is
order-independent with its header because every row carries the hash and the schema has no foreign
key.

On the first `system.discovered` for `(player, career)`, the career is bound to that hash once. A
later different hash leaves `career.system` unchanged and sets `career.system_changed = 1`. The mark
excludes nothing and scores nothing; it qualifies system-scoped comparisons exactly as `rewound`
qualifies a career clock. It is provenance for a system definition that changed under one save, not
an inference about why it changed.

### The boards

Fifty fixed keys, in publish order — which is the order `FixedBoards()` returns and therefore
the order `GET /v1/leaderboards` lists them, grouped by kind rather than by source event:

- **records** — `biggest_lithobrake_survived`, `peak_g_survived`, `max_q_survived`,
  `biggest_impact_energy`, `fastest_surface_speed`, `fastest_orbital_speed`, `fastest_entry`,
  `highest_altitude`, `lowest_pass`, `highest_apoapsis`, `lowest_orbit`, `roundest_orbit`,
  `steepest_orbit`, `softest_touchdown`, `softest_landing`, `heaviest_launch`, `heaviest_to_orbit`,
  `most_parts`, `biggest_stack`, `biggest_crew`, `biggest_recovery`, `most_stages`, `longest_eva`;
- **counters** — `kitten_tumbles`, `rud_total`, `orbits_achieved`, `soi_bodies`, `landed_bodies`,
  `landings`, `dockings`, `stagings`, `splashdowns`, `evas`, `flameouts`, `engine_ignitions`,
  `kittens_recovered`, then append-only `botched_landings`, `parts_lost`,
  `kittens_to_orbit_and_back` and `kittens_wrecked`;
- **derived totals and per-kitten records** — `distance_travelled`, `top_kitten_distance`,
  `top_kitten_missions`;
- **career time and save-native boards** — `fastest_to_orbit`, `career_playtime`, `play_sessions`;
- **append-only records and best-save results** — `biggest_parts_lost`, `biggest_crew_wreck`,
  `bodies_by_1y`, `bodies_by_10y`.

`docs/event-details.md` carries the canonical table: title, unit, direction, source event and fold
kind for every one of them, plus the eligibility rule board by board. Four of them
(`roundest_orbit`, `most_parts`, `most_stages`, `biggest_stack`) have an **empty** unit on purpose —
an eccentricity is dimensionless, and a bare count of a thing the title already names does not need
the word twice. `units.ForKey("stage_count")` falls through to `""`, which is correct.

**Five boards sit next to a near neighbour, and each pairing is deliberate.** Every one of them
exists because it answers a question its neighbour cannot:

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

**Newly decoded in `stats/payload.go`.** `FlightStarted` carries `kids []string`, `stage_count int`,
nullable `engine_count *int` and `lat`/`lon *float64`; `FlightEnded` gained `kids`, `body string` and `lat`/`lon`;
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

**Two rules the landing and warp boards do *not* get, and must not.** There is no plausibility check on a
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

**A rebuild is also what makes a new fold retroactive.** Events already in `events.db` that no fold
read produce board rows on the next rebuild and never on the incremental path, because the
incremental path has already passed them. Adding a board is therefore a rebuild, not a backfill
script — and only a rebuild, because nothing else re-reads history.

The swap keeps the old file until the reopen succeeds: close → delete the stale `-wal`/`-shm` →
rename live to `.old` → rename the rebuild in → reopen → delete `.old`, restoring `.old` on failure.
**Reads are never interrupted**: the old file answers every query until the rename, and the rename is
same-filesystem and atomic. That is the whole cutover — there is no second process and no second copy
of the log, because one process per database file (§5.4) forbids both.

### Rebuild is a routine operation

It is not only the nightly backstop. It is how a new board gets the history that preceded it, how a
shadowbanned player's records leave the boards, and how a changed fold is applied to everything it
should always have applied to. Three properties follow, and they are in `projector/job.go`:

- **It runs detached.** `POST /admin/projections/rebuild` answers `202` with a job, and
  `GET /admin/projections/rebuild` reports the phase, the events scanned and the head. At production
  size a rebuild is minutes, and no HTTP request, admin write lock or operator should be held for
  that. `{"wait": true}` exists for scripts that need the finished result in one call.
- **A request that arrives during one is queued, not lost.** A shadow ban applied while a rebuild is
  scanning is invisible to that rebuild — the scan already read those rows — so the file swapped in
  would still hold their records. The job runs one more pass instead.
- **The rebuild does not hold the §5.4 admin mutex.** It used to, which made it exclude bans, backups
  and the deny-list for its whole duration. The projections database is serialized by the projector's
  own `applyMu`; nothing else needed a lock.

### The build stamp — how a deploy that changes the boards heals itself

`projections.db` is a cache of the log, and a deploy can change what that cache is *supposed* to
contain. The projector's cursor only moves forward, so a board added today fills up with events from
today onwards while every event that preceded the deploy is missing from it — a board that is
populated, plausible and **wrong**, and indistinguishable from a board nobody has scored on yet.

`proj_build` records the fold-set identity that produced the file: `stats.BuildID` over the
projections schema version, the ordered names of every registered fold, and `stats.BuildVersion`. The
first two catch a board added, removed or renamed; the third is a hand-bumped constant and catches the
case they cannot see — **a fold whose name is unchanged and whose meaning changed**. Bumping it is the
same discipline as bumping an event's `ver`: same commit as the change, no exceptions.

At startup the projector compares the stamp to its own fold set:

| The live file | What happens |
|---|---|
| Stamp matches | Normal operation. |
| Stamp differs, checkpoint is 0 | The file holds nothing, so folding forward from zero *is* a full build. It is stamped and the loop runs. |
| Stamp differs, file holds history | **The fold loop is suspended** and a rebuild starts (`auto_rebuild`, on by default). The old file keeps answering reads unchanged, and the rebuilt one is swapped in when it is ready. |
| Stamp differs, in-memory database | Nothing to swap, so it logs loudly and carries on. Tests only. |

Suspending is the point. While it is suspended, boards are **stale but never wrong**, and a board this
deploy added reads *empty* rather than short-by-history. `GET /admin/stats` reports
`projector.build.stale` and `projector.rebuild.suspended`, and `catlogctl rebuild -status` says so in
words.

**Projections migrations must be additive.** The live file is migrated in place at open so its old
boards stay readable while the rebuild runs; a destructive migration would damage the file that is
still serving. A change that needs the old shape gone gets it for free, because the rebuild creates a
fresh database from every migration.

**An event the projector cannot decode is skipped and the checkpoint still advances** — one event
from a newer mod must never wedge every projection behind it. The skip log is deduplicated per
`(type, ver)` and carries no payload, because payloads are player-supplied and unbounded.
`projector.Upcasters` is **empty**, and stays empty until a payload shape actually changes: every
§4.2 type is at `ver: 1`, so there is nothing to convert. The registry exists so that the first bump
is a `Register` line rather than a migration project (PROJ-100).

`currentVer` must equal the mod's `EventTypes.Versions` exactly, or a newer mod's events are skipped
as a future version until this build catches up and a rebuild runs. `knownTypes` must equal the mod's
registry name for name and index for index — 23 entries. Until a type is in that list the server
answers `400 malformed_batch` for the **whole batch**, so a mod that emits a type its server does not
know loses everything in the batch, not just the new type: the mod change and the server change have
to merge together (PROJ-093).

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
| `POST /admin/shadowban` / `unshadowban` | same | Withhold a player's log without telling them; give it back |
| `POST /admin/shadowban/delete` | `shadowban-delete` | Destroy the withheld events permanently. Irreversible; needs `-yes` |
| `GET /admin/shadowban` | `shadowban-list` | The roster, its reasons and how much is withheld — the review queue |
| `GET /admin/shadowban/events` | `shadowban-review` | Read the withheld events themselves, unredacted |
| `POST /admin/projections/rebuild` | `rebuild` | Start a §5.6 rebuild and swap. `202` + a job; `{"wait": true}` blocks |
| `GET /admin/projections/rebuild` | `rebuild -status` | Phase, events scanned, head, and whether the loop is suspended |
| `POST /admin/archive/run` / `restore` | `archive` / `archive-restore` | §5.10 |
| `POST /admin/backup` | `backup` | Quiesce the writer, copy `events.db` **and its `-wal`** |
| `POST /admin/seed` | `seed` | Deterministic demo histories covering boards plus fixed, tier and publishable family badges |
| `POST /admin/events` | — | Insert events directly. The dev-loop tool: push one, watch the feed. |
| `POST /admin/clock` | — | Move the server's notion of now. Development only, mounted only when enabled. |
| `POST /admin/denylist/publish` | `denylist` | Regenerate the signed deny-list |
| `GET /admin/stats` | `stats` | Counters, both file sizes, both WAL sizes, projector lag, the build stamp, the rebuild job |
| — | `keygen`, `testvectors` | Local only; touch no server |

Also on `:6060`: `net/http/pprof` and `expvar`. **All `ingest_rejected_<code>` counters are published
at init**, including codes ingest cannot produce, so a dashboard never has to guess whether a missing
variable means "zero" or "not wired".

`GET /admin/shadowban/events` is the one place in catlog where an **unredacted** payload leaves the
database. The §4.8 views redact `install` and relabel `career`/`kid` per player; this endpoint cannot,
because its entire purpose is letting a human read what an account actually sent and decide whether it
should be restored or destroyed. It is loopback-only like every other admin route.

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
