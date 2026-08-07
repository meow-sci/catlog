# catlog — Initial Implementation Plan

Status: **authoritative implementation spec** (2026-08-06).
Inputs: [plans/INITIAL_OUTLINE_PLAN.md](plans/INITIAL_OUTLINE_PLAN.md), [plans/CATLOG_PROPOSALS.md](plans/CATLOG_PROPOSALS.md), plus owner decisions (captured in §1).

Precedence: **this document > CATLOG_PROPOSALS.md > INITIAL_OUTLINE_PLAN.md**. If this document is silent, consult the proposals; do not re-open decisions listed in §1.

---

## 0. How implementation agents must use this document

- Work is cut into **work packages** (§12, WP0–WP10). Each WP lists: goal, dependencies, context to load, exact tasks/files, and a **Definition of Done** (commands that must pass). Execute a WP end-to-end; do not partially start several.
- All thinking has been done up front. If something appears ambiguous, first re-read §4 (contracts) and §1 (locked decisions) — the answer is almost always there. Only genuinely new information (e.g. a library API changed) justifies deviating; when you deviate, record it in `docs/DECISIONS.md` with one line of rationale.
- **Never call external network services** during development or tests: no R2, no Turso Cloud, no real Discord/Google/GitHub. Everything runs locally (mock IdP included). Downloading build dependencies (go modules, NuGet, pnpm packages, docker images) is allowed.
- Machine facts (verified 2026-08-06): macOS arm64, `go1.26.5`, dotnet SDKs `9.0.307` + `10.0.100`, node `v24.18.0`, pnpm `11.12.0`. Docker Desktop is installed but the daemon is **not always running** — docker-dependent tests must degrade to a clear skip (§9.4).
- Package manager for anything Node: **pnpm exclusively** (`pnpm add`, `pnpm exec`, `pnpm dlx`). Never `npm`/`npx`/`yarn`.
- Relevant local skills to load per-WP (listed in each WP): `turso-db`, `harmony`, `ksa`, `mod-impl`, `tomlyn`, `imgui`, `imgui-design`.

---

## 1. Locked decisions (do not re-litigate)

| # | Decision | Detail |
|---|---|---|
| D1 | Hosting | DigitalOcean VPS VM, **owner-managed**. We produce deploy assets (§11) but never provision anything. |
| D2 | 100% local dev | Entire system (mod-side, server, site, IdPs) runs and is tested locally. External services are design targets only. |
| D3 | Backend language | **Go** (1.26.x). Single binary `catlogd` + admin CLI `catlogctl` + `mockidp`. Module `github.com/meow-sci/catlog/server`. |
| D4 | Database | **Turso embedded, first-class, no shims/abstraction layers.** Driver `turso.tech/database/tursogo` via `database/sql`. Two files: `events.db`, `projections.db`. The mod's local outbox uses `Microsoft.Data.Sqlite` (no Turso C# SDK exists — this is the mod's private spool, not the server DB). |
| D5 | Auth to backend APIs | **JWS**, two-token proof-of-possession scheme: long-lived **license JWS** (server-signed) + per-batch **proof JWS** (client-signed), both ES256/P-256, exactly §4.5. No bearer stopgap. |
| D6 | Reverse proxy | **nginx** (considered Caddy/Traefik; rejected — owner expertise wins, nginx feature set is sufficient). Config in `infra/nginx/`. |
| D7 | nginx testing | **testcontainers-go** drives real nginx containers in Go integration tests (build tag `docker`). |
| D8 | Archive | **R2 design-only.** `archive.Store` Go interface with a filesystem implementation used in dev/tests; R2 implementation is a documented future task, never called in dev. Only the **raw event log** is archived — projections are rebuildable and never archived. |
| D9 | Handles | US-ASCII, ≤150 chars, regex in §4.7. **Globally unique (case-insensitive). First claim wins and is permanent for that account. Never recycled** — on ban or account deletion the handle is retired forever (blocks impersonation of prior owners). |
| D10 | IdPs | Discord, Google, GitHub — all three at launch. Locally simulated by `mockidp`. No auto-merge of accounts across IdPs. |
| D11 | Crew survival | Best guess per decomp: **physics RUDs do not kill crew** (only manual destroy calls `KillCrew`). Rules in §4.2 (`vehicle.impact.survived`, lithobrake board). Marked `BEST-GUESS` for in-game verification later. |
| D12 | Repo layout | Top-level `mod/` (C#), `server/` (Go), `site/` (assets + e2e), plus `contracts/`, `docs/`, `infra/` (§2). |
| D13 | Mod testability | KSA-free core: `mod/catlog.lib` has **zero** KSA/Brutal references (enforced by a guard test). All API-interfacing code (outbox, shipper, signer, detector) lives there and is integration-tested against the real Go server without KSA. `mod/catlog.sim` console harness simulates gameplay end-to-end. |
| D14 | Site testing | Playwright via pnpm for e2e. Server-rendered UI with **datastar**; `site/` owns static assets + e2e tests; HTML templates live in the Go server (they are server code). |
| D15 | Passive telemetry | Windowed client-side, 30 s windows (`telemetry.window`), 2 Hz sampling. No raw sample firehose. |
| D16 | License TTL | 180 days. Reissue is the ban/deny-list touchpoint. |
| D17 | user_key | `HMAC-SHA256(pepper, "<idp>:" + <stable-subject>)`, 32 bytes. No email anywhere in the system — never requested from any IdP. |
| D18 | Wire format | NDJSON + Brotli (`Content-Encoding: br`), snake_case JSON. |
| D19 | Event IDs | ULIDs minted client-side; `(player, event_id)` unique index gives idempotent union-merge convergence. |
| D20 | Backend HTTP | Go stdlib `net/http` (1.22+ pattern routing). No web framework. CSRF via Go 1.25 `http.CrossOriginProtection` + SameSite cookies. |
| D21 | Go JOSE library | `github.com/go-jose/go-jose/v4` for JWS/JWK/RFC-7638 thumbprints server-side. Mod implements compact JWS with BCL only. |
| D22 | Projections | Incremental fold + cheap full rebuild as the correctness backstop (nightly in prod, on-demand via `catlogctl`). Late `flight.flagged` events are healed by rebuild (§5.6). |

---

## 2. Repository layout

Create exactly this tree (files appear in their WPs):

```
catlog/
├── INITIAL_IMPL_PLAN.md            (this file)
├── README.md                       WP0: one-screen overview + quickstart
├── Makefile                        WP0: root orchestration (§9.1)
├── .gitignore                      WP0
├── plans/                          (existing)
├── docs/
│   ├── DECISIONS.md                WP0: running log of deviations/decisions
│   ├── events.md                   WP0: event taxonomy (extracted from §4.2)
│   ├── ingest-api.md               WP0: extracted from §4.3–§4.5, §4.9
│   ├── credential.md               WP0: extracted from §4.6
│   ├── identity.md                 WP0: extracted from §4.7
│   └── r2-archive-design.md        WP10: R2 layout + migration path (design only)
├── contracts/
│   └── testdata/                   WP2: cross-language conformance vectors (§4.10)
├── server/                         Go module github.com/meow-sci/catlog/server
│   ├── go.mod
│   ├── cmd/catlogd/main.go
│   ├── cmd/catlogctl/main.go
│   ├── cmd/mockidp/main.go
│   └── internal/...                (§5.2)
├── mod/
│   ├── Directory.Build.props       WP6 (copied/adapted from unscience)
│   ├── catlog.slnx
│   ├── catlog.lib/                 KSA-free core (MeowSci.Catlog.Lib)
│   ├── catlog/                     the actual KSA mod (MeowSci.Catlog) — WP8
│   ├── catlog.sim/                 console gameplay simulator (MeowSci.Catlog.Sim)
│   ├── catlog.lib.tests/           xunit unit tests
│   └── catlog.integration.tests/   xunit vs real local server
├── site/
│   ├── package.json                pnpm; packageManager pinned
│   ├── assets/                     css/js sources (incl. WebCrypto keygen)
│   ├── scripts/build.mjs           esbuild bundle + vendor copy → site/dist
│   ├── dist/                       build output (gitignored)
│   └── e2e/                        playwright specs + playwright.config.ts
└── infra/
    ├── nginx/
    │   ├── dev.conf                full config, §6.1
    │   └── prod.conf.example       §6.2
    ├── compose.yaml                optional local nginx (§9.3)
    ├── systemd/
    │   ├── catlogd.service
    │   ├── catlog-nightly.service  (rebuild + archive)
    │   └── catlog-nightly.timer
    └── deploy/deploy.sh            rsync-based, §11
```

`.gitignore` must cover: `server/bin/`, `data/`, `site/dist/`, `site/node_modules/`, `mod/**/bin/`, `mod/**/obj/`, `*.db`, `*.db-wal`, `contracts/testdata/tmp/`, `.DS_Store`.

---

## 3. Fixed local ports & paths

| Thing | Value |
|---|---|
| `catlogd` public HTTP | `127.0.0.1:8080` |
| `catlogd` admin HTTP (localhost only, never proxied) | `127.0.0.1:6060` |
| `mockidp` | `127.0.0.1:9090` |
| nginx (dev, optional/docker) | `127.0.0.1:8081` |
| Server data dir (dev) | `./data/` → `data/events.db`, `data/projections.db`, `data/keys/`, `data/archive/` |
| Playwright baseURL | `http://127.0.0.1:8080` (direct to Go; nginx tested separately) |
| Dev issuer / htu base | `http://127.0.0.1:8080` |

---

## 4. Shared contracts (normative)

Everything in this section is the single source of truth for both the C# mod and the Go server. WP0 extracts it verbatim into `docs/`. Changing anything here requires bumping `ver` on the affected event or endpoint and a line in `docs/DECISIONS.md`.

### 4.1 Event envelope

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

### 4.2 Event taxonomy (launch set, all `ver: 1`)

Aggregate object `agg` = `{"min": f, "max": f, "mean": f, "last": f}`.
`body` = lowercase celestial body name string (opaque to server). `situation` = lowercased KSA enum name, opaque to server (known values incl. `landed`, `rolling`, `floating`, `sailing`, `dragging`, `bottomed`, plus airborne states — treat as open set).
Kitten identity: `kid` = lowercase Crockford base32 of the first 10 bytes of `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)` (16 chars); `name` = roster display name sanitized to printable US-ASCII, max 32 chars (moderation surface — purge path covers it).

| type | payload |
|---|---|
| `session.started` | `{"mod_ver": "0.1.0", "game_build": "2026.7.3.4826", "install": "<ulid>"}` |
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

### 4.3 Wire format & limits

- Batch = NDJSON (one envelope per line, `\n` separated, UTF-8, no BOM), compressed with **Brotli**; request header `Content-Encoding: br`, `Content-Type: application/x-ndjson`.
- Events within a batch are ordered by outbox append order (oldest first). A batch never mixes streams.
- Limits (server-enforced; mirror in mod constants):

| Limit | Value | Violation |
|---|---|---|
| Max compressed body | 1 MiB | `413 too_large` |
| Max decompressed | 8 MiB | `413 too_large` |
| Max events/batch | 2000 | `413 too_large` |
| Max single event line | 16 KiB | `400 malformed_batch` |
| Clock skew (proof `iat`) | ±300 s | `401 clock_skew` |
| Per-credential rate | token bucket: 1 batch / 2 s, burst 5 | `429` + `Retry-After` |

### 4.4 Ingest HTTP API

```
POST /v1/ingest
Content-Type: application/x-ndjson
Content-Encoding: br
X-Catlog-License: <compact license JWS>
X-Catlog-Proof:   <compact proof JWS>
<brotli(NDJSON)>
```

Responses (JSON body; `Date` header always present — the mod uses it for clock sync):

| Status | Body | Meaning |
|---|---|---|
| 200 | `{"accepted": n, "deduped": n}` | Stored (some rows may be duplicate event_ids). |
| 200 | `{"accepted": 0, "deduped": n, "replay": true}` | Whole-batch replay short-circuit (batch_id seen). |
| 400 | `{"error": "malformed_batch", "detail": s}` | Undecodable body / bad envelope / unknown type. |
| 401 | `{"error": <auth code §4.9>, "server_time": unix_ms}` | Auth failure. `clock_skew` includes `server_time`. |
| 409 | `{"error": "stream_fork"}` | seq conflict (§4.5.3). Mod recovery: mint new stream. |
| 413 | `{"error": "too_large"}` | Over limits. Mod recovery: halve batch size, retry. |
| 415 | `{"error": "unsupported_encoding"}` | Missing/wrong Content-Encoding. |
| 429 | `{"error": "rate_limited"}` + `Retry-After` | Back off. |

Public read endpoints in §4.8. Health: `GET /healthz` → `200 {"ok": true}` (no auth, no DB write).

### 4.5 Auth: license + proof JWS (ES256 only)

All JWS are **compact serialization**, alg allow-list `{ES256}` (reject anything else — no `none`, no RSA). Base64url without padding. .NET note: `ECDsa.SignData(..., SHA256)` already emits the r‖s IEEE-P1363 format JWS requires — no DER conversion.

#### 4.5.1 License JWS (server-signed, issued by dashboard/admin)

Header: `{"alg": "ES256", "kid": "catlog-<yyyymm>", "typ": "catlog-license+jwt"}`
Claims:

```jsonc
{
  "iss": "<issuer base URL, e.g. http://127.0.0.1:8080>",
  "sub": "<base64url(32-byte user_key)>",
  "handle": "whiskers_prime",
  "cnf": { "jkt": "<RFC 7638 SHA-256 thumbprint of client public JWK>" },
  "iat": 1770000000,
  "exp": 1785552000,            // iat + 180 days
  "jti": "lic_<ulid>",
  "ver": 1
}
```

Server signing key: P-256, PEM at `data/keys/license-signing.pem` (created by `catlogctl keygen`). Public JWKS served at `GET /.well-known/catlog-jwks.json` (`{"keys":[...]}`, each with `kid`). Rotation = add a key with new `kid`, keep old until all licenses expire.

#### 4.5.2 Proof JWS (client-signed, one per batch)

Header: `{"alg": "ES256", "typ": "catlog-proof+jwt", "jwk": {<public EC JWK: kty,crv,x,y only>}}`
Claims:

```jsonc
{
  "jti": "<batch_id ulid>",      // doubles as the batch id for replay short-circuit
  "iat": 1770000000,             // client time corrected by server-Date offset
  "htm": "POST",
  "htu": "<configured ingest URL, e.g. http://127.0.0.1:8080/v1/ingest>",
  "bh":  "<base64url(SHA-256(raw request body bytes AS SENT, i.e. post-brotli))>",
  "sid": "<stream ulid>",        // stream = one outbox instance epoch
  "seq": 42,                     // 1-based, strictly monotonic per (jkt, sid)
  "ph":  "<base64url(SHA-256(previous batch's body bytes))>"   // OMITTED when seq == 1
}
```

`htu` is compared against the server config list `accepted_htu` (string equality, no normalization) — dev config lists the dev URL, prod lists the public URL.

#### 4.5.3 Server verification order (cheapest first — implement exactly this order)

1. Both headers present; each JWS ≤ 4 KiB; compact format parses.
2. License header: `alg == ES256`, known `kid` → verify signature with server key (cache parsed licenses by SHA-256 of the raw JWS string, LRU 10k).
3. License claims: `iss` matches config; `exp` not passed; `ver == 1`.
4. Deny-list: `sub` not banned, `cnf.jkt` not revoked (in-memory set, §5.8).
5. DB: credential row for `jkt` exists, not revoked, matches `handle` + player (this also catches deleted accounts).
6. Proof header: `alg == ES256`, embedded `jwk` is P-256; **thumbprint(jwk) == license `cnf.jkt`** — else `401 proof_invalid`.
7. Proof signature verifies with embedded jwk.
8. `htm == "POST"`, `htu ∈ accepted_htu`, `|iat - now| ≤ 300 s` (else `clock_skew`).
9. Rate limit token bucket keyed `jkt` (§4.3) — before reading the body.
10. Read body (enforce size caps while reading). `bh == b64u(sha256(body))` — else `proof_invalid`.
11. Batch replay: row exists in `ingest_batch(player, jti)` → `200 replay` short-circuit, stop.
12. Stream check against `stream_state(player, sid)`: no row → require `seq == 1` and no `ph` (else `409`). Row exists → `seq == last_seq + 1 && ph == last_bh` accepted; `seq <= last_seq` → `409 stream_fork`; `seq > last_seq + 1` → accept but set `gap` marker in stream_state (telemetry is loss-tolerant; forensics only).
13. Decompress (cap 8 MiB), parse NDJSON, validate envelopes, txn: insert events (`INSERT OR IGNORE` on `(player_id,event_id)`), upsert `stream_state`, insert `ingest_batch`, commit.

Mod-side failure handling: `401 clock_skew` → recompute offset from `Date` header, re-sign, retry once; `409` → mint new `sid`, reset `seq=1`, continue (old chain abandoned); `413` → halve batch event cap (floor 50), retry; `429`/`5xx`/network → exponential backoff 1 s·2ⁿ + full jitter, cap 5 min, batches coalesce.

#### 4.5.4 Sessions & CSRF (website only — unrelated to ingest)

Cookie `catlog_sess` (prod: `__Host-catlog_sess`): value `b64u(user_key) + "." + exp_unix + "." + b64u(HMAC-SHA256(session_key, user_key_bytes || exp))`; TTL 7 days; `HttpOnly; SameSite=Lax; Path=/` (+`Secure` in prod). `session_key` = 32 random bytes at `data/keys/session.key`. CSRF: wrap mutating routes with Go 1.25 `http.CrossOriginProtection`. OAuth `state`: 32 random bytes, stored in a short-lived cookie, compared on callback.

### 4.6 Credential file (what the player downloads / the sim uses)

`catlog-credential.json` — assembled **client-side** (browser or `catlogctl issue`); the private key never reaches the server in the browser flow:

```jsonc
{
  "format": 1,
  "handle": "whiskers_prime",
  "license": "<compact license JWS>",
  "private_key_pem": "-----BEGIN PRIVATE KEY-----\n...(PKCS#8 EC P-256)...\n-----END PRIVATE KEY-----\n"
}
```

Mod default location: `<KSA user dir>/mods/catlog/credential.json`; sim/tests take a path argument. Loader must: parse license (unverified decode) to display handle/expiry, compute jkt from the private key's public part, and **refuse to start shipping if jkt ≠ license `cnf.jkt`**.

### 4.7 Identity, user_key, handles

`user_key = HMAC-SHA256(pepper, subject_string)`, where `subject_string` is:

| IdP | subject_string | Flow | Scopes | Account-age gate |
|---|---|---|---|---|
| Discord | `"discord:" + snowflake id` | OAuth2 code (no OIDC) → `GET /api/users/@me` | `identify` only | snowflake age ≥ 30 days (`(id>>22)+1420070400000`) |
| Google | `"google:" + id_token sub` | OIDC code flow; verify `id_token` against issuer JWKS | `openid` only | none (quotas only) |
| GitHub | `"github:" + numeric user id` | OAuth2 code → `GET /user` | none (default) | `created_at` ≥ 30 days |

`pepper` = 32 random bytes at `data/keys/pepper.key` (created by `catlogctl keygen`). Never in the DB, never logged. **Email is never requested from any IdP.** Discard IdP tokens immediately after reading the subject.

Handle rules (D9):

- Regex: `^[A-Za-z0-9]([A-Za-z0-9._-]{0,148}[A-Za-z0-9])?$` (1–150 chars, US-ASCII alnum + `._-`, must start/end alnum).
- Uniqueness: case-insensitive (`handle_lc` column, unique index). Original casing preserved for display.
- Reserved list (rejected at claim): `admin, administrator, catlog, api, root, system, mod, moderator, staff, official, support, help, www` + configurable extras.
- Immutable: no rename. New handle = new claim (subject to quota).
- **Never recycled**: ban or account deletion moves `handle_lc` into `retired_handle` permanently; claim checks consult both tables.
- Quotas per account: ≤ 5 live handles; ≤ 3 license issuances per 24 h (covers new + reissue).

Ban/delete: purge = `DELETE` all events/batches/stream_state for player, delete credentials + handles (retiring handle_lc), delete archive prefix (fs store), keep tombstone `{user_key, reason, at}` + revoked jkts in deny-list. Projections heal on next rebuild; fast path filters banned players in read queries.

### 4.8 Read API (public, CDN-cacheable JSON)

All responses `Cache-Control: public, s-maxage=30, stale-while-revalidate=300` except SSE.

- `GET /v1/leaderboards` → `{"boards": [{"stat": "biggest_lithobrake_survived", "title": s, "unit": "m/s", "count": n}]}`
- `GET /v1/leaderboards/{stat}?limit=50&offset=0` (limit ≤ 200) → `{"stat": s, "rows": [{"rank": 1, "handle": s, "value": f, "context": {…}, "updated": unix_ms}]}`
- `GET /v1/players/{handle}` → `{"handle": s, "since": unix_ms, "stats": [{"stat": s, "value": f, "rank": n, "context": {…}}]}` (404 if unknown/banned)
- `GET /v1/feed/sse` → datastar SSE stream of recent-activity fragments (no cache)
- `GET /.well-known/catlog-jwks.json`, `GET /.well-known/catlog-denylist.json` (§5.8)

Site HTML routes (§5.7): `/`, `/boards`, `/boards/{stat}`, `/p/{handle}`, `/login`, `/auth/{idp}/start`, `/auth/{idp}/callback`, `/dashboard`, `/docs/{install,privacy,api}`. Dashboard JSON API (session-auth’d, CSRF-protected): `GET /api/me`, `GET /api/handles`, `POST /api/handles` `{handle, jwk}` → `{license}`, `POST /api/handles/{handle}/reissue` `{jwk}` → `{license}`, `POST /api/handles/{handle}/revoke`, `POST /api/me/delete`, `POST /api/logout`.

### 4.9 Error code registry

`bad_request, malformed_batch, unsupported_encoding, license_invalid, license_expired, license_revoked, proof_invalid, clock_skew, banned, stream_fork, rate_limited, too_large, not_found, handle_taken, handle_invalid, handle_reserved, quota_exceeded, account_too_new, internal`. JSON shape everywhere: `{"error": code, "detail"?: s, "server_time"?: ms}`.

### 4.10 Cross-language conformance vectors — `contracts/testdata/`

Generated deterministically by `catlogctl testvectors generate contracts/testdata` (fixed keys, fixed timestamps — no randomness; regeneration is byte-identical). Consumed by **both** Go and C# test suites; this is what guarantees mod↔server interop without KSA.

Files:

```
keys/server-signing.pem, keys/server-jwks.json
keys/client-p256.pem, keys/client-pub.jwk.json, keys/client.jkt.txt
license/license-valid.jws, license-expired.jws, license-claims.json
batches/batch-001.ndjson, batch-001.br, batch-001.bh.txt
proofs/proof-001.jws (valid, seq=1), proof-002.jws (seq=2, correct ph),
       proof-bad-bh.jws, proof-wrong-key.jws, proof-stale-iat.jws
expected/verify-results.json   // map file → {ok: bool, error: code}
```

C# tests must: verify Go-produced license + proofs (signature + claims), reproduce `batch-001.bh.txt` from `batch-001.br`, reproduce `client.jkt.txt` from the JWK (RFC 7638), and produce proofs that the Go verifier accepts (exercised again live in WP7). Signatures are randomized (both runtimes), so tests verify rather than byte-compare.

---

## 5. Server (Go) design

### 5.1 Module & dependencies

`server/go.mod`: `module github.com/meow-sci/catlog/server`, `go 1.26`. Dependencies (add with `go get <pkg>@latest` and record resolved versions in `docs/DECISIONS.md`):

| Package | Use |
|---|---|
| `turso.tech/database/tursogo` | DB driver (registers `database/sql` driver name `"turso"`; pure Go via purego FFI, no CGO) |
| `github.com/go-jose/go-jose/v4` | JWS/JWK/thumbprints |
| `github.com/oklog/ulid/v2` | ULIDs |
| `github.com/andybalholm/brotli` | ingest body decompression |
| `github.com/klauspost/compress/zstd` | archive chunks |
| `github.com/BurntSushi/toml` | config |
| `github.com/starfederation/datastar-go` | datastar SSE SDK (if the import path differs at `go get` time, use the current first-party Go SDK from the datastar org and note it) |
| `github.com/testcontainers/testcontainers-go` | nginx tests (test-only, build tag `docker`) |

Everything else stdlib: `net/http` (pattern routing + `CrossOriginProtection`), `crypto/{ecdsa,elliptic,hmac,sha256,rand}`, `log/slog`, `encoding/json`, `expvar`, `net/http/pprof`.

### 5.2 Package layout

```
server/internal/
  config/    Config struct + TOML load + env override (CATLOG_*)
  ids/       ULID mint/parse helpers (string26 ↔ [16]byte)
  cjws/      thin wrappers over go-jose: SignES256, VerifyES256, Thumbprint, ParseCompactUnverified
  keys/      load/create pepper, session key, signing key; JWKS assembly
  store/     Open (events / projections), migrations (embedded SQL), typed queries; NO ORM
  authz/     license issue/verify, proof verify chain (§4.5.3 exactly), deny-list, token buckets
  identity/  IdP client flows (discord/google/github) against configurable base URLs; user_key derivation; sessions
  ingest/    body read/caps, brotli, NDJSON decode, envelope validation, write pipeline
  projector/ checkpoint loop, fold registry, rebuild, feed broadcaster
  stats/     per-board fold implementations + board metadata (§5.6)
  readapi/   /v1/* JSON handlers + caching headers
  web/       html/template (go:embed), datastar SSE handlers, dashboard handlers
  adminapi/  localhost admin mux :6060 (§5.9)
  archive/   Store interface + fs impl + chunk writer (§5.10)
  testutil/  spin up in-memory stores, mint test credentials, golden helpers
```

`cmd/catlogd`: wire everything, run public mux :8080 + admin mux :6060, graceful shutdown (drain writer, close DBs). `cmd/catlogctl`: thin HTTP client for the admin API + local key utilities (never opens the DB — see §5.4 one-process rule). `cmd/mockidp`: §5.8.1.

### 5.3 Config (`server/catlogd.toml`, env overrides `CATLOG_SECTION_KEY`)

```toml
[server]   listen = "127.0.0.1:8080"; admin_listen = "127.0.0.1:6060"
           base_url = "http://127.0.0.1:8080"      # issuer AND htu base
           static_dir = "../site/dist"              # served at /static/ in dev; empty = disabled (nginx serves it in prod)
[data]     dir = "./data"
[ingest]   accepted_htu = ["http://127.0.0.1:8080/v1/ingest"]
           max_body_bytes = 1048576; max_events = 2000
[auth]     license_ttl_days = 180; handle_quota = 5; issuance_per_day = 3; min_account_age_days = 30
[idp.discord] auth_url=""; token_url=""; api_base=""; client_id=""; client_secret=""
[idp.google]  issuer=""; auth_url=""; token_url=""; jwks_url=""; client_id=""; client_secret=""
[idp.github]  auth_url=""; token_url=""; api_base=""; client_id=""; client_secret=""
[limits]   ratelimit_per_jkt_per_s = 0.5; ratelimit_burst = 5
```

Dev config (`server/catlogd.dev.toml`, committed) points all IdP URLs at `http://127.0.0.1:9090/{discord,google,github}/...` with client_id/secret `dev`/`dev`.

### 5.4 Storage — Turso discipline & DDL

Turso rules that shape this code (from the turso-db skill — treat as hard constraints): **one process per DB file** (hence catlogctl → admin API, never direct file access; never open a live DB with `tursodb` shell — copy the file first); WAL is the default journal mode (set nothing); **no `VACUUM`**; no `WITHOUT ROWID`; avoid `STRICT`; avoid expression indexes (materialize `handle_lc` instead); UTF-8 only; MVCC/encryption experimental — **do not enable**. FK clauses are documentation only (no `PRAGMA foreign_keys`).

Connections: per DB file, one `*sql.DB` for writes with `SetMaxOpenConns(1)`, one for reads with `SetMaxOpenConns(4)`. All writes to a file go through its single writer goroutine (§5.5) or the admin mutex — never concurrent write txns.

Migrations: embedded `store/migrations/events/NNNN_*.sql` + `.../projections/NNNN_*.sql`, applied in order at startup inside a txn each; version table `schema_version(v INTEGER NOT NULL)`.

`events.db`:

```sql
CREATE TABLE player (
  player_id  INTEGER PRIMARY KEY,
  user_key   BLOB NOT NULL UNIQUE,          -- 32 B
  idp        TEXT NOT NULL,                 -- 'discord'|'google'|'github'
  created_at INTEGER NOT NULL,              -- unix ms
  banned_at  INTEGER, ban_reason TEXT
);
CREATE TABLE handle (
  handle     TEXT PRIMARY KEY,
  handle_lc  TEXT NOT NULL UNIQUE,
  player_id  INTEGER NOT NULL REFERENCES player(player_id),
  created_at INTEGER NOT NULL
);
CREATE TABLE retired_handle (handle_lc TEXT PRIMARY KEY, reason TEXT NOT NULL, retired_at INTEGER NOT NULL);
CREATE TABLE credential (
  jkt        TEXT PRIMARY KEY,              -- b64u thumbprint
  player_id  INTEGER NOT NULL, handle TEXT NOT NULL,
  license_jti TEXT NOT NULL,
  issued_at  INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER
);
CREATE INDEX cred_player ON credential(player_id);
CREATE TABLE event (
  seq       INTEGER PRIMARY KEY,            -- rowid: server-local total order = projector cursor
  event_id  BLOB NOT NULL,                  -- 16 B ULID
  player_id INTEGER NOT NULL,
  flight_id BLOB, session_id BLOB,          -- 16 B ULIDs, flight nullable
  type      TEXT NOT NULL, ver INTEGER NOT NULL DEFAULT 1,
  sim_time  REAL, wall_time INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  payload   TEXT NOT NULL                   -- JSON
);
CREATE UNIQUE INDEX ev_dedup ON event(player_id, event_id);
CREATE INDEX ev_player ON event(player_id, seq);
CREATE TABLE ingest_batch (
  player_id INTEGER NOT NULL, batch_id TEXT NOT NULL,   -- proof jti
  n_events INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  PRIMARY KEY (player_id, batch_id)
);
CREATE TABLE stream_state (
  player_id INTEGER NOT NULL, sid BLOB NOT NULL, jkt TEXT NOT NULL,
  last_seq INTEGER NOT NULL, last_bh TEXT NOT NULL, gap INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (player_id, sid)
);
CREATE TABLE tombstone (user_key BLOB PRIMARY KEY, reason TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE archive_cursor (id INTEGER PRIMARY KEY CHECK (id = 1), last_seq INTEGER NOT NULL);
```

`projections.db`:

```sql
CREATE TABLE proj_checkpoint (projection TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE player_stat (
  player_id INTEGER NOT NULL, stat TEXT NOT NULL,
  value REAL NOT NULL, context TEXT,        -- JSON: body, flight, sim_t, etc.
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, stat)
);
CREATE INDEX stat_rank ON player_stat(stat, value);
CREATE TABLE flight_state (
  flight_id BLOB PRIMARY KEY, player_id INTEGER NOT NULL,
  flags INTEGER NOT NULL DEFAULT 0,         -- bit0 teleport, bit1 refuel, bit2 resource_edit, bit3 console
  ended_reason TEXT, crew INTEGER, body TEXT, started_seq INTEGER NOT NULL
);
CREATE INDEX fs_player ON flight_state(player_id);
CREATE TABLE player_body (player_id INTEGER NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, first_seq INTEGER NOT NULL, PRIMARY KEY (player_id, kind, body));
CREATE TABLE kitten (
  player_id INTEGER NOT NULL, kid TEXT NOT NULL, name TEXT NOT NULL,
  travelled_m REAL NOT NULL DEFAULT 0, fastest_ms REAL NOT NULL DEFAULT 0,
  missions INTEGER NOT NULL DEFAULT 0, mission_time_s REAL NOT NULL DEFAULT 0,
  kia INTEGER NOT NULL DEFAULT 0, updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, kid)
);
CREATE TABLE feed (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, handle TEXT NOT NULL, type TEXT NOT NULL, summary TEXT NOT NULL);
```

Cross-file joins are impossible (two DBs) — resolve `player_id → handle` in Go with an in-memory map (loaded at start; invalidated on handle create/revoke/ban via a callback from the identity code). Feed table capped at 500 rows (`DELETE FROM feed WHERE id <= max(id) - 500` after insert).

### 5.5 Ingest pipeline

Handler (`ingest`): runs §4.5.3 steps 1–10 inline (no DB writes), then submits a `WriteJob{player, batchID, sid, jkt, seq, bh, events []DecodedEvent, reply chan WriteResult}` to a **bounded channel (cap 256)**; a single writer goroutine per events.db owns steps 11–13: begins txn, batch short-circuit check, `INSERT OR IGNORE` per event (prepared stmt), upsert stream_state, insert ingest_batch, commit, replies `{accepted, deduped, replay}`, then notifies the projector (`chan struct{}` non-blocking send). Channel full → `503` + `Retry-After: 5` (backpressure). Handler timeout 30 s.

### 5.6 Projector & launch boards

Projector goroutine: on notify or 1 s ticker, read `event` rows `seq > checkpoint` (batches of 1000, read handle on events.db), decode, apply every registered fold, write all projection updates + `proj_checkpoint` in **one projections.db txn**, then push feed rows to the SSE broadcaster. All folds share one checkpoint (`projection = 'all'`).

Fold interface (in `stats`): `type Fold interface { Apply(tx *sql.Tx, ev DecodedEvent, fs FlightStateReader) error }` — folds may read/update `flight_state` first (it is itself a fold, applied before the others).

Launch boards (stat key → source → fold rule; all "record" folds skip events whose flight has any flag bit set, and tie-break keeps the earliest `updated_seq`):

| stat | rule |
|---|---|
| `biggest_lithobrake_survived` | `vehicle.impact` where survived && !launch_pad && crew_count ≥ 1 → max `speed_ms`. Context: `{body, flight, energy_j}`. (KIA-window check per §4.2 done at rebuild time; incremental path accepts the impact as-is — rebuild heals.) |
| `peak_g_survived` | `telemetry.window.peak_g` max, only counted when `flight_state.ended_reason == 'recovered'` at fold time — incremental under-count healed by rebuild; acceptable (D22). Simpler alternative implemented first: max `peak_g` over windows of **unflagged** flights, context notes flight; refine at rebuild. |
| `fastest_surface_speed` | max `telemetry.window.surface_speed_ms.max` |
| `fastest_orbital_speed` | max `telemetry.window.orbital_speed_ms.max` |
| `kitten_tumbles` | count `kitten.tumble` |
| `rud_total` and `rud_<cause>` (6) | count `vehicle.rud` (+ per cause) |
| `orbits_achieved` | count `vehicle.orbit` phase=achieved |
| `soi_bodies` | distinct `to_body` via `player_body(kind='soi')`; value = count |
| `dockings` | count `vehicle.docked` |
| `stagings` | count `vehicle.staging` |
| `kittens_recovered` | sum `flight.ended.crew_count` where reason=recovered |
| `distance_travelled` | from `roster.snapshot`: upsert per-kitten max `travelled_m`; value = sum over kittens |

Feed summaries: template per type (e.g. `"{handle} lithobraked at 214 m/s on duna — and survived"`), only for: impact(survived), rud, orbit achieved, soi, tumble, kia, flight recovered.

**Rebuild** (admin-triggered + nightly): build into `projections.rebuild.db` from seq 0 (same folds + the KIA-window refinement pass), close live proj DB, `os.Rename` swap, reopen, resume. Read API holds an RWMutex around the proj handle swap.

### 5.7 Web UI (datastar SSR)

Templates: `web/templates/*.gohtml` via `go:embed` — `layout`, `home`, `boards`, `board`, `profile`, `login`, `dashboard`, `docs_*`. Static assets served by nginx in prod, by Go (`/static/` → `config.static_dir`) in dev. CSS: `@picocss/pico` (vendored via pnpm build) + `site/assets/css/catlog.css`. JS: vendored datastar + `site/assets/js/keygen.js` (dashboard only).

- `/` — top-3 of three featured boards + live feed panel: `<div id="feed" data-on-load="@get('/v1/feed/sse')">`; the SSE handler (datastar SDK) patches new feed items in.
- `/boards/{stat}` — table rendered from `player_stat` (limit 100), `s-maxage=30`.
- `/p/{handle}` — profile stats + ranks.
- `/dashboard` — session-gated; lists handles + credential metadata (jkt, issued, expires, revoked); "New handle" wizard.

Wizard (`keygen.js`, plain ES module — no framework):
1. `crypto.subtle.generateKey({name:"ECDSA", namedCurve:"P-256"}, true, ["sign"])`
2. `exportKey("jwk", pub)` → strip to `{kty,crv,x,y}` → `POST /api/handles {handle, jwk}` (datastar action or plain fetch with CSRF-safe same-origin).
3. On `{license}`: `exportKey("pkcs8", priv)` → PEM-wrap → assemble credential JSON (§4.6) → `Blob` download `catlog-credential.json`. Show "this file cannot be re-downloaded" + jkt fingerprint.
4. Errors surfaced inline: `handle_taken`, `handle_invalid`, `handle_reserved`, `quota_exceeded`, `account_too_new`.

### 5.8 Identity endpoints & deny-list

`identity`: one generic OAuth2-code engine parameterized per IdP (§4.7 table), all URLs from config. Google path additionally fetches JWKS (cached) and verifies `id_token` (iss, aud, exp, sig) — extract `sub`. After deriving `user_key`: upsert player (banned → show "account banned" page, no session), set session cookie, redirect `/dashboard`.

Deny-list: in-memory `{subs: set, jkts: set}` loaded from events.db (tombstones + revoked credentials) at start and refreshed on every mutation (ban/revoke call a hook). Published as signed JWS at `/.well-known/catlog-denylist.json`: payload `{"ver": n, "updated_at": ms, "banned_subs": [b64u...], "revoked_jkts": [...]}` — regenerated on mutation; future multi-node pullers poll it (single-node now: in-process set is authoritative).

#### 5.8.1 `mockidp` (the reason dev is 100% local)

Single binary, port 9090, config `server/mockidp.toml` (committed) defining test users:

```toml
[[user]] id = "discord-snowflake-100000000000000000"  # aged snowflake
         label = "Whiskers (Discord, old account)"
[[user]] id = "discord-snowflake-NEW"                  # minted-now snowflake → account_too_new
[[user]] google_sub = "g-user-1"; label = "Mittens (Google)"
[[user]] github_id = 4242; label = "Clawdia (GitHub)"
```

Implements, with the **exact response shapes of the real providers** (fields we read only):

- `/discord/oauth/authorize` → HTML page listing users with "Login as" buttons → redirects with `code`; `/discord/oauth/token` (form POST, checks client_id/secret) → `{access_token}`; `/discord/api/users/@me` → `{"id": "<snowflake>"}`. Snowflakes computed so the age gate is exercised both ways.
- `/google/authorize`, `/google/token` → includes `id_token` **really signed** (mockidp mints its own P-256 key) with `{iss: "http://127.0.0.1:9090/google", aud: client_id, sub, exp}`; `/google/jwks` serves the JWKS. (catlogd's google verifier reads `jwks_url` from config — no discovery doc needed.)
- `/github/login/oauth/authorize`, `/github/login/oauth/access_token`, `/github/user` → `{"id": 4242, "created_at": "2020-01-01T00:00:00Z"}`.

The authorize pages carry stable DOM ids (`#login-as-<label-slug>`) for playwright.

### 5.9 Admin API (`127.0.0.1:6060`, no auth — bound to loopback; never proxied by nginx) & `catlogctl`

| Endpoint | catlogctl verb | Behavior |
|---|---|---|
| `POST /admin/issue {handle, jwk?}` | `catlogctl issue --handle X [--out dir]` | If no jwk: CLI generates P-256 locally, sends pub jwk, writes complete `catlog-credential.json` to `--out`. Creates a synthetic player `user_key = HMAC(pepper, "dev:"+handle)` if needed. **Dev/test only path.** |
| `POST /admin/ban {handle|sub, reason}` / `POST /admin/unban` | `ban` / `unban` | Sets banned_at, revokes credentials, retires handle(s), refreshes deny-list, purges on `--purge`. |
| `POST /admin/purge {sub}` | `purge` | Full data deletion incl. archive prefix + tombstone. |
| `POST /admin/projections/rebuild` | `rebuild` | §5.6 rebuild+swap; returns stats. |
| `POST /admin/archive/run` | `archive` | §5.10. |
| `POST /admin/backup {dest}` | `backup` | Quiesce writer (mutex), copy `events.db` + `-wal` to dest, resume. |
| `POST /admin/seed` | `seed` | Inserts the deterministic demo dataset (3 players `demo_ace`/`demo_tumbler`/`demo_crasher`, one of each record type) for UI dev. |
| `POST /admin/denylist/publish` | `denylist` | Regenerate signed deny-list. |
| `GET /admin/stats` | `stats` | counters (events, players, queue depth, checkpoint lag). |
| (local, no server) | `keygen` | Create `data/keys/{license-signing.pem, session.key, pepper.key}` if missing. |
| (local, no server) | `testvectors generate <dir>` | §4.10. |

Also on 6060: `net/http/pprof`, `expvar` counters (`ingest_accepted`, `ingest_deduped`, `ingest_rejected_<code>`, `projector_lag_seq`, `sse_clients`).

### 5.10 Archiver (fs now, R2 later)

```go
type Store interface {
    Put(ctx, key string, r io.Reader) error   // immutable write
    List(ctx, prefix string) ([]string, error)
    Delete(ctx, prefix string) error          // recursive, for purge
}
```

`fsStore` roots at `data/archive/`. Key layout (identical for R2 later): `players/<b64u(user_key)>/chunks/<firstseq>-<lastseq>.ndjson.zst` + `players/<sub>/manifest.json` (chunk list + counts). Archive run: read events `seq > archive_cursor` (cap 100k/run), group by player, append one zstd NDJSON chunk per player per run (envelope + `recv_time` line format identical to wire NDJSON plus `"_seq"` field), update manifests, advance cursor in events.db. Archiving **copies** (never deletes) — local retention pruning is a later, separate decision. `docs/r2-archive-design.md` (WP10) documents the R2 impl: S3-compatible API, aws-sdk-go-v2, credentials via env, lifecycle = none (immutable), purge = prefix delete — design only, zero code calling R2.

### 5.11 Logging

`log/slog` JSON to stdout. Every ingest rejection logs one line `{code, jkt?, ip, detail}` at WARN, rate-limited per jkt (1/min) to keep hostile traffic from log-flooding. Never log: pepper, keys, license/proof contents, user_key raw bytes (log b64u prefix ≤ 8 chars).

---

## 6. nginx

### 6.1 `infra/nginx/dev.conf` (complete file — WP9 writes exactly this, templated `$STATIC_ROOT` and `$UPSTREAM`)

```nginx
worker_processes 1;
events { worker_connections 1024; }
http {
  include mime.types;
  default_type application/octet-stream;
  sendfile on;
  server_tokens off;

  limit_req_zone $binary_remote_addr zone=ingest_ip:10m rate=10r/s;
  limit_req_status 429;

  upstream catlogd { server $UPSTREAM; keepalive 16; }

  server {
    listen 8081;
    client_max_body_size 2m;

    location = /v1/ingest {
      limit_req zone=ingest_ip burst=20 nodelay;
      proxy_pass http://catlogd;
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header Host $host;
      # body is brotli-compressed application/x-ndjson — pass through untouched
      proxy_request_buffering on;
    }

    location = /v1/feed/sse {
      proxy_pass http://catlogd;
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_buffering off;
      proxy_cache off;
      proxy_read_timeout 1h;
      add_header X-Accel-Buffering no;
    }

    location /static/ {
      alias $STATIC_ROOT/;      # site/dist
      expires 1h;
      gzip on; gzip_types text/css application/javascript;
    }

    location /admin/ { return 403; }   # belt & suspenders; admin binds loopback:6060 anyway

    location / {
      proxy_pass http://catlogd;
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header Host $host;
      gzip on; gzip_types text/html application/json text/css application/javascript;
    }
  }
}
```

### 6.2 `prod.conf.example`

Same shape plus: `listen 443 ssl http2` + certbot-managed certs, `server_name catlog.<domain>`, HTTP→HTTPS redirect server block, `limit_req` zones sized for real traffic, static served from `/var/lib/catlog/site/`, proxy to `127.0.0.1:8080`. TLS is owner-managed (D1) — the example documents, doesn't automate.

### 6.3 testcontainers-go tests (`server/internal/nginxproxy/nginx_test.go`, build tag `docker`)

Fixture: start `httptest`-style catlogd (real handlers, in-memory DBs) on a host port; run `nginx:stable-alpine` container with `dev.conf` mounted (templated `$UPSTREAM = host.testcontainers.internal:<port>`) using `testcontainers.WithHostPortAccess(port)`; map container 8081. Tests:

1. Ingest round-trip through proxy succeeds (200, correct accepted count) — proves header + brotli body passthrough.
2. `X-Forwarded-For` reaches the handler.
3. 3 MiB body → nginx 413 (never reaches Go).
4. Burst 40 rapid requests → some 429 from `limit_req`.
5. SSE: connect `/v1/feed/sse`, trigger a feed event, assert the frame arrives < 1 s (proves `proxy_buffering off`).
6. `/admin/` → 403.

Skip logic: `testcontainers` provider probe fails → `t.Skip("docker unavailable")`. Run via `make test-nginx` (`go test -tags docker ./internal/nginxproxy/`).

---

## 7. Mod (.NET) design

### 7.1 Projects & csproj demarcation (D13)

`mod/Directory.Build.props`: copy `unscience/Directory.Build.props` (net10.0, LangVersion 13, Nullable enable, TreatWarningsAsErrors, ImplicitUsings disable, KSAFolder/KSAUserDir properties with env override) — adjust `SelectedDistModDir` identically.

| Project | AssemblyName | References | KSA? |
|---|---|---|---|
| `catlog.lib` | `MeowSci.Catlog.Lib` | NuGet: `Microsoft.Data.Sqlite` (9.x), `Ulid` (Cysharp), `Tomlyn` | **NONE — enforced** |
| `catlog` | `MeowSci.Catlog` | ProjectRef catlog.lib; NuGet `Lib.Harmony`; KSA/Brutal dlls via `$(KSAFolder)` with `Condition="Exists(...)"` `Private=false` (pattern: `unscience/steely-eyed-missile-kitten/steely-eyed-missile-kitten.csproj`) | yes (build requires KSA install) |
| `catlog.sim` | `MeowSci.Catlog.Sim` | ProjectRef catlog.lib. `OutputType=Exe` | no |
| `catlog.lib.tests` | — | xunit + catlog.lib | no |
| `catlog.integration.tests` | — | xunit + catlog.lib | no |

Guard test (in `catlog.lib.tests`): reflect over `typeof(EventEnvelope).Assembly.GetReferencedAssemblies()` and assert none start with `KSA`, `Brutal`, `0Harmony` — the demarcation is CI-enforced, not aspirational.

`mod/catlog.slnx` lists all five; `dotnet build mod/catlog.slnx` on a KSA-less machine must succeed for everything except `catlog` (that project no-ops its KSA refs when the folder is missing and will fail to compile — therefore **exclude `catlog` from the slnx**; build it directly via `dotnet build mod/catlog` on a KSA machine. The slnx is the local/CI surface.)

### 7.2 `catlog.lib` internals

House style: gatOS/unscience conventions — `System.Text.Json` with `JsonNamingPolicy.SnakeCaseLower`, immutable records, no ImplicitUsings, nullable enabled, per-subsystem dead-latch error handling (one ERROR log then disable for session; never throw across the host boundary).

```
catlog.lib/
  Events/     EventEnvelope.cs        record; ToNdjsonLine(); STJ context (source-gen optional)
              Payloads.cs             one record per §4.2 type; EventTypes registry (type→ver)
              GameSignal.cs           abstract record + one sealed record per Harmony-origin signal
                                      (Rud, Impact, Splash, Staging, Dock, Undock, EvaStart, EvaEnd,
                                       Tumble, Kia, Recovered, Created, Removed, SessionLoaded,
                                       Flagged, RosterSample)
  Telemetry/  TelemetrySnapshot.cs    record: vehicle_id, vehicle_name, sim_t, wall_ms, body,
                                      situation, altitude_m, atmo_height_m, surface_speed_ms,
                                      orbital_speed_ms, accel_ms2, peak_g, dyn_pressure_pa, mass_kg,
                                      ecc, ap_m, pe_m, inc_deg, parent_body_id, crew_count
              Sanitize.cs             NaN/Inf scrub (copy pattern from gatOS)
  Detect/     EventDetector.cs        stateless prev/curr comparator per vehicle: situation diff,
                                      atmosphere hysteresis (±2% of atmo height), orbit achieved
                                      (ecc<1 && pe_alt > atmo_height + 1000), orbit escaped (ecc≥1),
                                      SOI (parent diff); debounce 2.0 s sim-time per (vehicle, kind)
              WindowAccumulator.cs    30 s sim-time min/max/mean/last folds → telemetry.window
              FlightTracker.cs        (vehicle_id, launch_game_time) → flight ULID; session ULID;
                                      flags accumulation; signal→envelope mapping (EventFactory)
              ImpactCorrelator.cs     impact + same-frame destruction pairing → survived flag
                                      (holds impacts one tick; destruction seen → survived=false)
  Outbox/     OutboxDb.cs             Microsoft.Data.Sqlite; SQLitePCL.Batteries_V2.Init() at open
              (DDL: outbox_event(id INTEGER PRIMARY KEY, event_id TEXT UNIQUE, kind INT/*0 passive,1 event*/,
                     created_ms INT, body TEXT);
                    shipper_state(k TEXT PRIMARY KEY, v TEXT)  -- sid, seq, last_bh, clock_offset_ms)
              Append(batch), NextBatch(maxN, maxBytes), MarkShipped(lastRowId),
              Prune(capBytes: drop oldest kind=0 first, never kind=1)
  Ship/       BatchShipper.cs         async loop; triggers: ≥64 pending or oldest ≥15 s;
                                      brotli-compress NDJSON, mint batch ULID, seq++/ph chain,
                                      sign proof, POST, handle §4.5.3 mod-side recovery table;
                                      IShipperClock + HttpMessageHandler injectable for tests
  Auth/       Credential.cs           load/validate §4.6 file; jkt check
              Jws.cs                  compact JWS ES256 sign/verify (BCL only: ECDsa, ~60 lines)
              Jwk.cs                  EC JWK export + RFC 7638 thumbprint (canonical {"crv","kty","x","y"})
  Config/     ModConfig.cs            Tomlyn (tomlyn skill); atomic temp+move save; load-never-throws;
                                      fields: enabled, ingest_url, credential_path, sample_hz=2,
                                      window_s=30, outbox_cap_mb=50, log_level
  Util/       Ulid helpers, Base64Url, SnapshotStore.cs   ← copy from
              /Users/asherwin/repos/meow-sci/gatOS/gatOS.SimFs/Snapshots/SnapshotStore.cs (57 lines)
```

Threading contract: game thread produces `TelemetrySnapshot[]` + `GameSignal`s → `SnapshotStore` swap → detector/window/outbox run on a worker; shipper is its own task woken by outbox flush. In sim/tests, "game thread" is the scenario runner. Passive-sample cadence 2 Hz enforced by drop-not-backfill (`SampleClock` pattern).

### 7.3 `catlog.sim` — gameplay simulator

`dotnet run --project mod/catlog.sim -- --scenario <name> --server http://127.0.0.1:8080 --credential path/to/catlog-credential.json [--list] [--assert] [--speed 100]`

Scenarios are **C# classes** (`IScenario { string Name; IEnumerable<SimStep> Steps(); void Assert(ReadApiClient api, string handle); }`); `SimStep` = `At(simT)` + snapshot set and/or signals. The runner feeds steps through the real detector→outbox→shipper against the real server (`--speed` scales wall-clock pacing; outbox in a temp dir). `--assert` polls the read API (10 s timeout) and asserts expected leaderboard values.

Canonical scenarios (each asserts its boards):

1. `hop-lithobrake` — suborbital hop, hard survivable impact at 62 m/s with 2 crew → `biggest_lithobrake_survived=62`, `kittens_recovered=2`.
2. `orbit-and-back` — launch, atmosphere exit, orbit achieved, SOI stays, deorbit, recovery → `orbits_achieved=1`.
3. `rud-sampler` — six flights, one RUD per cause → `rud_total=6`, each `rud_<cause>=1`.
4. `tumbleweed` — EVA kitten, 3 tumbles → `kitten_tumbles=3`.
5. `cheater` — teleport-flagged flight with absurd 9000 m/s survived impact → asserts it does **not** appear on the board.
6. `soak` — 30 min compressed play, ~2k events incl. windows → asserts counts and that dedup=0.

### 7.4 `catlog` (game project — WP8, requires a KSA machine)

Implements only: `Mod.cs` lifecycle (StarMap entry, config load, status ImGui window via `imgui`/`imgui-design` skills), `Patcher.cs` with the verified patch table from CATLOG_PROPOSALS §1.3 (copy the table into code comments; `AccessTools` + null-check + patch-time log; never patch worker-thread detect methods), `VehicleTelemetry.cs` (all KSA reads → `TelemetrySnapshot`, per-vehicle try/catch), `KsaAnchor.cs` attribute on every KSA touch, `mod.toml` (copy shape from SEMK). Everything else is calls into `catlog.lib`. Load skills `ksa`, `harmony`, `mod-impl` before writing this code. Acceptance is build-on-KSA-machine + manual smoke checklist (`docs/DECISIONS.md` records results); no automated tests here by design — that's what the lib/sim split buys us.

### 7.5 Mod test suites

`catlog.lib.tests` (unit; `dotnet test`, no network):
- Detector: situation transitions, debounce, atmosphere hysteresis both edges, orbit achieve/escape incl. NaN ecc, SOI change; golden scenario snapshots → expected envelope sequences.
- WindowAccumulator: agg math vs hand-computed; window boundary at exactly 30 s.
- ImpactCorrelator: impact+destruction same frame → survived=false; impact alone → true.
- Outbox: append/drain ordering, prune drops passive first, crash-recovery (reopen mid-batch), state round-trip.
- Jws/Jwk: sign→verify round-trip; **conformance against `contracts/testdata`** (§4.10): verify Go license & proofs, reproduce bh + jkt values.
- Shipper (fake `HttpMessageHandler`): batch trigger thresholds, seq/ph chain across batches, 401 clock_skew resync-once, 409 → new sid + seq=1, 413 halving, backoff schedule (virtualized clock), offline accumulation.
- Assembly guard test (§7.1).

`catlog.integration.tests` (`dotnet test` with env `CATLOG_SERVER_URL` set, else spawns the server): fixture starts `server/bin/catlogd` with a temp data dir + dev config on a random port, runs `catlogctl issue` for a test handle, then: ship→200/accepted; re-ship same batch→replay; tamper body→401 proof_invalid; skew clock→recovers; revoke credential (`catlogctl`) → 401 license_revoked; oversize→413→halving succeeds. Run via `make test-integration` (which builds the Go binaries first).

---

## 8. Site (`site/`)

- `package.json`: `"packageManager": "pnpm@11.12.0"`; deps: `@playwright/test`, `esbuild`, `@picocss/pico`, datastar npm package (resolve current name — datastar's published package under the starfederation org; note the resolved name/version in `docs/DECISIONS.md`).
- `scripts/build.mjs`: esbuild-bundle `assets/js/*.js` → `dist/js/`, copy pico css + datastar dist file → `dist/vendor/`, copy `assets/css/` → `dist/css/`. `pnpm build` runs it. No dev server, no framework — the Go server renders HTML.
- `e2e/playwright.config.ts`: `webServer: [ {command: "make -C .. server-run-test-env", url: "http://127.0.0.1:8080/healthz"}, {command: "make -C .. mockidp-run", url: "http://127.0.0.1:9090/healthz"} ]`, project chromium only, `baseURL http://127.0.0.1:8080`. Test-env server uses a throwaway data dir seeded via `/admin/seed`.
- Specs:
  1. `auth.spec.ts` — Discord login-as aged user → dashboard; brand-new snowflake → `account_too_new` message; Google + GitHub paths land on dashboard.
  2. `handle.spec.ts` — wizard: claim `e2e_whiskers`, intercept download, parse credential JSON, structural checks (license decodes, `cnf.jkt` matches a locally recomputed RFC-7638 thumbprint of the generated key via page `crypto.subtle`); duplicate claim → `handle_taken`; reserved word → `handle_reserved`; 151-char / non-ASCII → `handle_invalid`; case-collision (`E2E_WHISKERS`) → `handle_taken`.
  3. `boards.spec.ts` — seeded data renders: boards index, lithobrake board rows ranked, profile page, 404 page for unknown handle.
  4. `feed.spec.ts` — open home, POST a seed event via admin API, assert feed item appears via SSE without reload.
  5. `lifecycle.spec.ts` — revoke handle from dashboard (jkt disappears from list); delete-my-data → logged out, profile 404, handle **not** reclaimable by a second account (retired).
- WebCrypto works on `http://127.0.0.1` (secure context) — no TLS needed for e2e.

Full-stack proof (`make e2e-full`, script not playwright): clean data dir → start catlogd+mockidp → `catlogctl issue --handle sim_ace` → run sim `hop-lithobrake --assert` → `curl` board JSON asserts `sim_ace` at rank 1 → playwright `boards.spec.ts` against the same instance.

---

## 9. Local development & orchestration

### 9.1 Root `Makefile` targets (all paths relative to repo root; every WP keeps these green)

```
bootstrap        go mod download; dotnet restore mod/catlog.slnx; pnpm -C site install
build            server-build + mod-build + site-build
server-build     cd server && go build -o bin/ ./cmd/...
mod-build        dotnet build mod/catlog.slnx -c Release
site-build       pnpm -C site build
test             server-test + mod-test           (no docker, no network)
server-test      cd server && go test ./...
mod-test         dotnet test mod/catlog.lib.tests
test-integration server-build + go test -tags integration ./... + dotnet test mod/catlog.integration.tests
test-nginx       server-build + go test -tags docker ./internal/nginxproxy/   (skips w/o docker)
e2e              site-build + pnpm -C site exec playwright test
e2e-full         §8 script (scripts/e2e-full.sh)
sim              dotnet run --project mod/catlog.sim -- --scenario $(SCENARIO) --server http://127.0.0.1:8080 --credential $(CRED)
dev              runs catlogd (dev config) + mockidp in foreground (trap-kill both); expects `make keys` once
keys             server/bin/catlogctl keygen
seed             server/bin/catlogctl seed
testvectors      server/bin/catlogctl testvectors generate contracts/testdata
```

### 9.2 Dev loop

`make keys && make dev` → visit `http://127.0.0.1:8080`, log in via mockidp buttons, claim handle, download credential, `make sim SCENARIO=hop-lithobrake CRED=~/Downloads/catlog-credential.json`, watch the board + live feed update.

### 9.3 Optional local nginx: `infra/compose.yaml` runs `nginx:stable-alpine` on 8081 with `dev.conf` (`$UPSTREAM=host.docker.internal:8080`, static mounted from `site/dist`). Not required for any test except its own smoke check.

### 9.4 Docker-dependent tests

Only the nginx suite needs docker. It must self-skip with a clear message when the daemon is unreachable, and `make test-nginx` prints how to start Docker Desktop. Nothing in `make test` touches docker.

---

## 10. Definition of the "simulate everything locally" guarantee (D2)

The following external things are simulated, and nothing else may exist:

| Real thing | Local stand-in |
|---|---|
| Discord/Google/GitHub | `mockidp` (exact response shapes) |
| R2 | `archive/fsStore` (identical key layout) |
| KSA game runtime | `catlog.sim` scenarios feeding the real lib pipeline |
| Cloudflare CDN | nothing (Go sets correct `Cache-Control`; CDN is transparent) |
| DO VPS + TLS | nginx dev.conf (HTTP) + prod.conf.example (documented) |
| Turso Cloud offsite sync | not used in dev; `catlogctl backup` covers dev durability |

---

## 11. Deployment assets (owner-managed VPS — we only produce files)

- `infra/systemd/catlogd.service`: `User=catlog`, `WorkingDirectory=/var/lib/catlog`, `ExecStart=/usr/local/bin/catlogd -config /etc/catlog/catlogd.toml`, `Restart=on-failure`, hardening (`ProtectSystem=strict`, `ReadWritePaths=/var/lib/catlog`, `NoNewPrivileges=yes`).
- `infra/systemd/catlog-nightly.{service,timer}`: 04:30 UTC → `catlogctl rebuild && catlogctl archive && catlogctl backup /var/backups/catlog/$(date +%F)`.
- `infra/deploy/deploy.sh`: build linux/amd64 (`GOOS=linux GOARCH=amd64 go build`), `pnpm -C site build`, rsync binary + `site/dist` + configs to the VPS, `systemctl restart catlogd`. Idempotent, `--dry-run` flag.
- `prod.conf.example` per §6.2. DNS/TLS/firewall/Cloudflare: owner's runbook, out of scope.

---

## 12. Work packages

Dependency graph: `WP0 → WP1 → {WP2, WP3} → WP4 → WP5`; `WP0 → WP6 → WP7 (needs WP2)`; `WP8` after WP7 (and a KSA machine); `WP9` after WP2; `WP10` after WP4. WP5 needs WP3+WP4. Independent WPs may run in parallel.

Every WP ends with: `make test` green, new tests added for its code, `docs/DECISIONS.md` appended if anything deviated, and a conventional commit (`feat(server): …`, `feat(mod): …` — see `git-commit` skill).

---

**WP0 — Scaffolding & contracts** *(no deps)*
Create §2 tree (empty projects that build), root Makefile (§9.1, stub targets fine where the WP hasn't landed), `.gitignore`, README quickstart, extract §4 into `docs/{events,ingest-api,credential,identity}.md` verbatim, `docs/DECISIONS.md` seeded with §1 table. `server/go.mod` + `cmd/catlogd` hello-server on 8080 with `/healthz`; `mod/catlog.slnx` with empty lib/sim/test projects building on this mac; `site/package.json` + build.mjs producing `dist/`.
**DoD**: `make bootstrap build test` all green on a clean checkout; `curl :8080/healthz` → `{"ok":true}`.

**WP1 — Server foundation: config, keys, store, migrations** *(WP0)*
Load skill `turso-db` first. Implement `config`, `ids`, `cjws`, `keys`, `store` (both DDLs §5.4, migration runner, open discipline incl. MaxOpenConns rules), `testutil` (fresh temp-dir DBs — note `:memory:` is fine for throwaway unit stores but migrations tests must also run against real files), `catlogctl keygen`. Unit tests: migration idempotence, ULID round-trip, JWS sign/verify/thumbprint against known RFC 7638 vector (`{"crv":"P-256",...}` example from the RFC), event insert + dedup index behavior, handle claim incl. retired/case-collision paths.
**DoD**: `make server-test`; a `go vet ./...` clean tree.

**WP2 — Ingest path + conformance vectors** *(WP1)*
Implement `authz` (full §4.5.3 chain — table-driven tests per step, each with the exact error code), token buckets, `ingest` (§5.5 pipeline incl. bounded-channel backpressure test), `/v1/ingest` handler, `catlogctl testvectors generate` (§4.10) and commit the generated `contracts/testdata/`. expvar counters. Integration test (`-tags integration`): boot full binary on a random port, mint credential via admin issue (implement `/admin/issue` + `catlogctl issue` here), ship a golden batch with a Go-side test shipper, verify rows + replay + fork + skew + oversize behaviors end-to-end.
**DoD**: `make server-test test-integration testvectors` green; `contracts/testdata` committed and regeneration is byte-identical.

**WP3 — Identity: mockidp, IdP flows, sessions, issuance, deny-list, admin** *(WP1)*
Implement `mockidp` (§5.8.1 + `/healthz`), `identity` (3 IdP flows, user_key, sessions, CSRF, account-age gates), handle claim rules (§4.7 total: regex, reserved, retired, quotas), license issuance (`POST /api/handles`, reissue, revoke), delete-my-data, ban/purge + tombstones + deny-list publish, remaining `catlogctl` verbs (§5.9), `/.well-known/*`. Unit tests: user_key derivation vectors, snowflake age math, handle validation matrix (valid/151 chars/non-ASCII/reserved/case-dup/retired), quota enforcement, session cookie forge-rejection. Integration: full OAuth dance against mockidp with Go's http client (cookie jar), all three IdPs; ban → ingest 401 → unban.
**DoD**: `make test test-integration` green.

**WP4 — Projector, boards, read API, seed** *(WP2)*
Implement `projector`, `stats` (all §5.6 boards), rebuild+swap, feed cap, `readapi` (§4.8 with cache headers), `/admin/seed` + `/admin/projections/rebuild` + `/admin/stats`. Tests: per-fold golden tests (input event sequence → expected player_stat rows, incl. flag-exclusion and tie-break), checkpoint resume mid-stream, rebuild equals incremental for unflagged histories, rebuild heals a late-flag case that incremental missed, leaderboard pagination, banned-player filtering.
**DoD**: `make server-test test-integration`; seeded instance serves correct board JSON.

**WP5 — Web UI + e2e** *(WP3, WP4)*
Implement `web` (§5.7 all pages, datastar SSE feed), `keygen.js`, docs pages (install/privacy/api — privacy states "we never receive your email"), site build pipeline, playwright suite (§8, all 5 specs), `make e2e`, `make e2e-full` script.
**DoD**: `make e2e` and `make e2e-full` green locally (chromium via `pnpm exec playwright install chromium`).

**WP6 — Mod core (`catlog.lib`) + unit tests** *(WP0; §4.10 vectors from WP2 to finish conformance tests)*
Load skill `tomlyn`. Implement §7.2 in full + `catlog.lib.tests` per §7.5. Copy `SnapshotStore.cs` from the verified gatOS path. Keep every §4 constant in one `Wire.cs` static class mirroring §4.3 limits.
**DoD**: `make mod-test` green on this mac (no KSA, no network); guard test proves zero KSA refs.

**WP7 — Simulator + mod↔server integration** *(WP2, WP6)*
Implement `catlog.sim` (§7.3 runner + 6 scenarios + ReadApiClient) and `catlog.integration.tests` (§7.5 fixture + cases). Wire `make sim`, `make test-integration` (both stacks), `make e2e-full` sim leg.
**DoD**: `make test-integration` green; `hop-lithobrake --assert`, `cheater --assert`, `soak --assert` pass against a fresh local server.

**WP8 — Game mod (`catlog` project)** *(WP7 + machine with KSA install)*
Load skills `ksa`, `harmony`, `mod-impl`, `imgui`, `imgui-design`. Implement §7.4 using the patch table verified in CATLOG_PROPOSALS §1.3 (decomp build 2026.7.3.4826; re-verify anchors against the current decomp at `ksa-game-assemblies/current/decomp` before coding — game builds move). Status window: collection on/off, queue depth, last ship result, handle + expiry.
**DoD**: builds on the KSA machine; manual smoke checklist executed and recorded in `docs/DECISIONS.md` (liftoff, orbit, RUD each cause obtainable, tumble, ship-to-local-server over LAN or same machine).

**WP9 — nginx configs + testcontainers suite + deploy assets** *(WP2)*
Write §6.1/§6.2 configs, §6.3 test suite, `infra/compose.yaml`, systemd units + timer, `deploy.sh`.
**DoD**: `make test-nginx` green with docker running (and clean-skips without); compose smoke: site reachable through :8081.

**WP10 — Archiver (fs) + R2 design doc** *(WP4)*
Implement `archive` (§5.10: Store, fsStore, chunk writer, manifests, cursor, purge hook wired into WP3's purge path), `catlogctl archive`, restore tool (`catlogctl archive-restore <dir>` → replays chunks into a fresh events.db via admin — enables the DR story). Write `docs/r2-archive-design.md`. Tests: archive run determinism, manifest correctness, purge deletes prefix, restore round-trips (restore→rebuild→same player_stat rows), cursor resume.
**DoD**: `make server-test` green; restore round-trip test proves DR path.

---

## 13. Risks & watch items (carry into DECISIONS.md as they resolve)

1. **Turso beta**: no VACUUM (purge leaves free pages — acceptable; monitor file size in `/admin/stats`), driver behavior under `database/sql` (verify prepared-stmt + txn semantics early in WP1 with a smoke test), purego FFI on linux/amd64 for deploy builds (verify cross-compile actually runs on a linux box/docker before first deploy — note: cross-compiled FFI libs may need the linux artifact; test in WP9 with a docker run).
2. **datastar Go SDK / npm package names** may have moved — resolve at `go get`/`pnpm add` time, record versions.
3. **`http.CrossOriginProtection`** is Go 1.25+ — present in 1.26.5; if API differs, fall back to Origin/Sec-Fetch-Site checking middleware (10 lines).
4. **Crew survival semantics** are BEST-GUESS (D11) — WP8 smoke must test a physics RUD with crew and record the observed roster outcome; adjust §4.2 rule + projections via rebuild if wrong.
5. **KSA build drift** vs the verified patch table — WP8 re-verifies anchors; `[KsaAnchor]` + patch-time logging localize breaks.
6. **ES256 in WebCrypto vs JOSE**: WebCrypto ECDSA signatures are already r‖s — no issue; but `exportKey("jwk")` includes `d` on private export — ensure only the **public** JWK ever leaves the page.
7. **nginx `limit_req` behind Cloudflare later**: per-IP zones see CF IPs in prod — the prod config must use `CF-Connecting-IP` (documented in prod.conf.example comments) once Cloudflare fronts it. Dev unaffected.
