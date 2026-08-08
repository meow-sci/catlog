# catlog decisions

**This is the record of what catlog chose and why.** Do not re-litigate anything here without
reading it first — nearly every entry exists because the obvious alternative was tried, measured, or
reasoned through and lost.

[CONSTITUTION.md](CONSTITUTION.md) sits above this file: it says what catlog *optimises for*, this
says what it *decided*. Make a new decision by checking it against the constitution, then recording
it here.

**Format.** Decisions are grouped by area and numbered `<AREA>-NNN` within it. Every entry keeps the
rationale it was written with — the "why" is the part that stops a decision being re-opened in six
months. An entry that stopped being true is marked *superseded* and kept, because the reason it was
replaced is itself worth not re-discovering.

**Adding one.** Append to the right area with the next free number, date it, and say why — not only
what. If it changes a contract in [events.md](events.md), [ingest-api.md](ingest-api.md),
[identity.md](identity.md) or [credential.md](credential.md), change that document in the same
commit. See [ARCHITECTURE.md](ARCHITECTURE.md#7-keeping-the-documentation-true) for the full rule.

## Contents

- **[Repository, toolchain & dependencies](#repository-toolchain--dependencies)** — `REPO-*`, 25 entries
- **[Storage — Turso, schema & compression](#storage--turso-schema--compression)** — `STORE-*`, 16 entries
- **[Ingest, auth & the conformance vectors](#ingest-auth--the-conformance-vectors)** — `INGEST-*`, 24 entries
- **[Identity, handles & moderation](#identity-handles--moderation)** — `IDENT-*`, 15 entries
- **[Projector, boards & the read API](#projector-boards--the-read-api)** — `PROJ-*`, 84 entries
- **[Archive & restore](#archive--restore)** — `ARCH-*`, 13 entries
- **[The two frontends](#the-two-frontends)** — `UI-*`, 55 entries
- **[The mod and its KSA-free core](#the-mod-and-its-ksa-free-core)** — `MOD-*`, 69 entries
- **[The load harness](#the-load-harness)** — `LOAD-*`, 26 entries
- **[nginx, systemd & deployment](#nginx-systemd--deployment)** — `OPS-*`, 17 entries
- **[Documentation](#documentation)** — `DOCS-*`, 2 entries

---

## Locked decisions (do not re-litigate)

The founding choices. Every one still holds; everything below refines them.

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
| D12 | Repo layout | Top-level `mod/` (C#), `server/` (Go), `site/` (datastar assets + e2e), `spa/` (React reader), plus `contracts/`, `docs/`, `infra/`, `scripts/` (§2 — see [ARCHITECTURE.md](ARCHITECTURE.md)). |
| D13 | Mod testability | KSA-free core: `mod/catlog.lib` has **zero** KSA/Brutal references (enforced by a guard test). All API-interfacing code (outbox, shipper, signer, detector) lives there and is integration-tested against the real Go server without KSA. `mod/catlog.sim` console harness simulates gameplay end-to-end. |
| D14 | Site testing | Playwright via pnpm for e2e. Server-rendered UI with **datastar**; `site/` owns static assets + e2e tests; HTML templates live in the Go server (they are server code). |
| D15 | Passive telemetry | Windowed client-side, 30 s windows (`telemetry.window`), 2 Hz sampling. No raw sample firehose. |
| D16 | License TTL | 180 days. Reissue is the ban/deny-list touchpoint. |
| D17 | user_key | `HMAC-SHA256(pepper, "<idp>:" + <stable-subject>)`, 32 bytes. No email anywhere in the system — never requested from any IdP. |
| D18 | Wire format | NDJSON + Brotli (`Content-Encoding: br`), snake_case JSON. |
| D19 | Event IDs | ULIDs minted client-side; `(player, event_id)` unique index gives idempotent union-merge convergence. |
| D20 | Backend HTTP | Go stdlib `net/http` (1.22+ pattern routing). No web framework. CSRF via Go 1.25 `http.CrossOriginProtection` + SameSite cookies. |
| D21 | Go JOSE library | `github.com/go-jose/go-jose/v4` for JWS/JWK/RFC-7638 thumbprints server-side. Mod implements compact JWS with BCL only. |

---

## Repository, toolchain & dependencies

The build, the pinned versions, and the rules that keep four toolchains buildable on one laptop.

### REPO-001 — A1: `mod/Directory.Build.props` resolves the KSA assemblies dynamically

*Accepted · 2026-08-06 · WP0.*

A1: `mod/Directory.Build.props` resolves the KSA assemblies dynamically instead of assuming the Windows game install the plan (§7.1, via `unscience/Directory.Build.props`) assumed. Ladder copied from `gatOS/Directory.Build.props`, first match wins: `$(KSA_DLL_DIR)` → sibling `../../ksa-game-assemblies/current/dll/` → `C:\Program Files\Kitten Space Agency\` → `$(HOME)/repos/meow-sci/ksa-game-assemblies/current/dll/` — so the game project builds on this macOS dev machine, which has no game install. Verified: it resolves to the sibling checkout (37 assemblies incl. `KSA.dll`, `Brutal.*.dll`, `Planet.*.dll`).

### REPO-002 — A1 (cont.): `KSAUserDir` on non-Windows is `$(HOME)/Documents/My Games/Kitten Space Agency/`

*Accepted · 2026-08-06 · WP0.*

A1 (cont.): `KSAUserDir` on non-Windows is `$(HOME)/Documents/My Games/Kitten Space Agency/` (gatOS points it at its own mods checkout instead) and the dist override env var is `CATLOG_DIST_DIR` — catlog needs the real per-OS KSA user dir, not a repo-local one.

### REPO-003 — A1 (cont.): `ImplicitUsings` is `disable`

*Accepted · 2026-08-06 · WP0.*

A1 (cont.): `ImplicitUsings` is `disable`, unlike gatOS which enables it — plan §7.2 house style; every using in `mod/` is explicit.

### REPO-004 — A2: `mod/catlog/` is not permanently excluded from `mod/catlog.slnx`

*Accepted · 2026-08-06 · WP0.*

Plan §7.1 excludes it because a KSA-less machine cannot build it; A1 makes the DLLs resolve here, so WP8 adds `catlog/catlog.csproj` to the solution rather than building it out-of-band. The slnx carries a comment saying so.

### REPO-005 — A3: the current game build is `2026.8.5.5168`

*Accepted · 2026-08-06 · WP0.*

A3: the current game build is `2026.8.5.5168` (`ksa-game-assemblies/current/version.json`), not `2026.7.3.4826` as the plan states. `docs/events.md` uses the new value in the `session.started` example. Consequence: the `CATLOG_PROPOSALS.md` §1.3 patch table was verified against the **older** decomp and must be re-verified against `ksa-game-assemblies/current/decomp` in WP8 before any Harmony patch is written.

### REPO-006 — A4: nothing in WP0 depends on docker

*Accepted · 2026-08-06 · WP0.*

A4: nothing in WP0 depends on docker (the daemon is down on this machine). `make test-nginx` is a stub until WP9 and `make test` never touches docker, per §9.4.

### REPO-007 — `.gitignore` adds `*.db-shm` alongside the §2 list — SQLite/Turso writes a shared-memory file next to every WAL, and leaving it untracked-but-unignored dirties every status check

*Accepted · 2026-08-06 · WP0.*

`.gitignore` adds `*.db-shm` alongside the §2 list — SQLite/Turso writes a shared-memory file next to every WAL, and leaving it untracked-but-unignored dirties every status check.

### REPO-008 — Test framework for `mod/` is **xunit 2.9.3** + `xunit.runner.visualstudio` 3.1.4 + `Microsoft.NET.Test.Sdk` 17.14.1 (the versions the .NET 10.0.100 SDK's own `dotnet new xunit` template pins)

*Accepted · 2026-08-06 · WP0.*

Note the sibling gatOS repo uses NUnit; the plan (§7.1) specifies xunit for catlog, so catlog does not follow gatOS here.

### REPO-009 — Site dev dependencies pinned exactly (no `^`): `esbuild` 0.28.1, `@picocss/pico` 2.1.1, `@playwright/test` 1.62.1

*Accepted · 2026-08-06 · WP0.*

`packageManager` is `pnpm@11.20.0` per §8.

### REPO-010 — The **datastar npm package is deliberately unresolved.** `site/scripts/build.mjs` carries a `TODO(WP5)` where its browser bundle joins the vendor copy list; WP5 resolves the real package name under the starfederation org at `pnpm add` time and records name + version here

*Accepted · 2026-08-06 · WP0.*

Same applies to the Go SDK (`github.com/starfederation/datastar-go`) at WP1/WP5 `go get` time.

### REPO-011 — pnpm 11 no longer reads the `pnpm` field in `package.json`; build-script approval moved to `site/pnpm-workspace.yaml` (`allowBuilds: esbuild: true`)

*Accepted · 2026-08-06 · WP0.*

Without it a clean `pnpm install` exits 1 with `ERR_PNPM_IGNORED_BUILDS` and `make bootstrap` fails.

### REPO-012 — `server/go.mod` has **zero dependencies** so far — none of the §5.1 packages are imported yet, and an unused `require` block would not survive `go mod tidy`

*Accepted · 2026-08-06 · WP0.*

WP1 adds them with `go get <pkg>@latest` and records every resolved version here, as §5.1 requires.

### REPO-013 — `server/internal/` contains exactly the §5.2 packages (each with a `doc.go`)

*Accepted · 2026-08-06 · WP0.*

`internal/nginxproxy/` from §6.3 is **not** pre-created — WP9 creates it with its test file, since a package holding only a build-tagged test file would otherwise be empty.

### REPO-014 — The playwright config in §8 references `make -C

*Accepted · 2026-08-06 · WP0.*

server-run-test-env` and `make -C .. mockidp-run`, which are not in the §9.1 target list. They are **not** in the WP0 Makefile; WP5 adds them together with `e2e`.

### REPO-015 — KSA detection surfaces re-verified against build `2026.8.5.5168`; result committed as [ksa-integration.md](ksa-integration.md)

*Accepted · 2026-08-06 · orchestrator.*

KSA detection surfaces re-verified against build `2026.8.5.5168`; result committed as [ksa-integration.md](ksa-integration.md) — a drop-in replacement for `plans/CATLOG_PROPOSALS.md` §1.3, with per-row VERIFIED/CHANGED/GONE status and `file:line` citations. WP6 and WP8 use that document, **not** the proposals table. Note the previous decomp tree on disk is `2026.8.3.5117`; the plan's `2026.7.3.4826` does not exist locally, so several claims it made were newly confirmed by this pass rather than re-confirmed.

### REPO-016 — D11 (crew survival) is confirmed at source level, and stays `BEST-GUESS` pending in-game proof

*Accepted · 2026-08-06 · orchestrator.*

D11 (crew survival) is confirmed at source level, and stays `BEST-GUESS` pending in-game proof. `Kia = true` is written in exactly one place, reachable only from `Vehicle.KillCrew()`, whose sole caller is the player-initiated destroy path; the physics RUD path calls `EndAllCrewMissions` and never touches it. Consequence: `kitten.kia` is a *deliberate scuttling* signal, not an impact fatality signal. The §4.2 rule is unchanged — its `kitten.kia` proximity check simply almost never fires. WP8 must still verify in-game (§13.4).

### REPO-017 — Contract amendment (no `ver` bump): `flight.flagged.flag` gains the value `"tuning"`

*Accepted · 2026-08-06 · orchestrator.*

Contract amendment (no `ver` bump): `flight.flagged.flag` gains the value `"tuning"`, and `flight_state.flags` (§5.4 projections) gains **bit4 = tuning**. The game ships a debug window that live-edits `KittenLocomotionTuning.Current.TumbleSpeedGate` (stock `6.5` m/s), which is the sole classifier for `kitten.tumble` — without this flag the tumble board is trivially forgeable. Amending `ver: 1` in place rather than bumping is correct here because no `ver: 1` event has ever been emitted or stored; the plan's bump-on-change rule (§4) protects shipped data, and there is none.

### REPO-018 — `telemetry.window.peak_g`/`max_q_pa` must be omitted, not zeroed, when unavailable

*Accepted · 2026-08-06 · orchestrator.*

`telemetry.window.peak_g`/`max_q_pa` must be omitted, not zeroed, when unavailable. `Vehicle.StructuralLoad` is new in 5168 and is written only under full physics (reset every prepared step), so an all-zero reading means "no data this step" for an on-rails or freefall vehicle. Reporting zero would corrupt the `peak_g_survived` board with fake minima.

### REPO-019 — `roster.snapshot.fastest_ms` is ecliptic-frame

*Accepted · 2026-08-06 · orchestrator.*

`roster.snapshot.fastest_ms` is ecliptic-frame (the game's `FastestSpeed` includes the parent body's orbital motion — ~30 km/s baseline on Earth). Recorded for completeness, never surfaced as a speed record; the speed boards derive from `telemetry.window` as §5.6 already specifies.

### REPO-020 — Detection-rule corrections that do not change the wire contract

*Accepted · 2026-08-06 · orchestrator.*

Detection-rule corrections that do not change the wire contract, for WP6/WP8: there is no flameout concept in the game (`engine.flameout` is derived from `IsActive && !IsPropellantAvailable`); hyperbolic apoapsis is **negative, not NaN**, so orbit classification branches on `Orbit.IsHyperbolic()/IsParabolic()/IsBound()` rather than NaN-sniffing; `vehicle.Parent` **throws** on an uninitialized vehicle rather than returning null; the vehicle-removal choke point is `Vehicle.Dispose(bool)`, not `Deregister`; and "survived" must be resolved at an `ApplyVehicleSolvers` postfix, because manual destroys land later in the same frame. Details and citations in [ksa-integration.md](ksa-integration.md) §0.

### REPO-021 — datastar's browser bundle is vendored into the repo, not installed from npm

*Accepted · 2026-08-06 · orchestrator.*

Verified: the npm package named `datastar` is an unrelated GoDaddy library, and `@starfederation/datastar` was abandoned at `1.0.0-beta.11` (Mar 2025) and never became v1 — the v1.x bundle ships only from the project's git repo. `site/assets/vendor/datastar.js` is **v1.0.2**, 34,083 bytes, `sha384-SnyFlWTdFL3c8+9/1WsPuMFBq6AQOGC1LmS9upY4YkM3En3wZr5q2UvydHaMgOVG` (fetched once from `cdn.jsdelivr.net/gh/starfederation/datastar@v1.0.2/bundles/datastar.js`; both the byte count and the hash were reproduced locally). Committing it keeps `make site-build` hermetic, which D2 requires, and removes a CDN from the build path. The `TODO(WP5)` in `site/scripts/build.mjs` is resolved.

### REPO-022 — The datastar Go SDK import path is `github.com/starfederation/datastar-go/datastar`

*Accepted · 2026-08-06 · orchestrator.*

The datastar Go SDK import path is `github.com/starfederation/datastar-go/datastar` — the module root is not importable, the API lives in the `/datastar` subpackage. Module **v1.2.2**. Its `go.sum` lists two CGO backends (`google/brotli/go/cbrotli`, `valyala/gozstd`) which look alarming but are **not in the build graph** — `CGO_ENABLED=0 GOOS=linux go build` still produces a statically linked binary, because the graph uses pure-Go `andybalholm/brotli` and `klauspost/compress/zstd`. SDK and bundle versions diverge legitimately (1.2.2 vs 1.0.2); do not try to align them.

### REPO-023 — Deploy constraint discovered ahead of WP9: `catlogd` cannot ship on `scratch` or `distroless/static`

*Accepted · 2026-08-06 · orchestrator.*

Deploy constraint discovered ahead of WP9: `catlogd` cannot ship on `scratch` or `distroless/static`. `tursogo` is CGO-free but purego's `fakecgo` still emits a dynamically-linked ELF with a glibc interpreter even at `CGO_ENABLED=0`, and the driver extracts a native `.so` to disk at startup (so a read-only or `noexec` temp dir breaks it — `TURSO_GO_CACHE_DIR` overrides the location). Alpine needs `-tags musl`. A glibc base (Debian/Ubuntu, which the target DO droplet is) is the boring correct answer. Related: the exclusive whole-file DB lock means **deploys must fully stop the old process before starting the new one** — no rolling or blue-green overlap. `windows/arm64` compiles but fails at runtime.

### REPO-024 — Response compression is the reverse proxy's job; `catlogd` emits uncompressed responses and will keep doing so

*Accepted · 2026-08-07 · orchestrator.*

Made explicit after the bare-Go dev server was observed serving uncompressed HTML/JSON. The Go server has no compression middleware and gains none: D20 keeps it stdlib-only with no framework, and both `infra/nginx/dev.conf` and `infra/nginx/prod.conf.example` already carry `gzip on` (`text/html`, `application/json`, `text/css`, `application/javascript`, plus `gzip_vary on` in prod) for the page and `/static/` locations, with Cloudflare compressing again at the edge. **Consequences, accepted deliberately:** (1) `make dev` talks straight to Go on `:8080` with no nginx in the path, so responses in a dev browser's network tab are uncompressed — that is expected, not a bug; (2) a deployment with no compressing proxy in front serves uncompressed JSON and HTML, so `spa/` — which is hosted separately and cross-origin — must be pointed at a proxied origin rather than a bare `catlogd`. **Two locations must never gain compression, in nginx or anywhere else:** `/v1/feed/sse`, because gzip buffering defeats streaming and the SSE contract depends on a frame arriving in under a second (§6.3), and `/v1/ingest`, whose request body is hashed and verified byte for byte (`prod.conf.example` carries a standing "never add gunzip/brotli filters or sub_filter here" comment). Unrelated and unaffected: the **inbound** Brotli on `/v1/ingest` request bodies (D18), which is the mod's wire format and has nothing to do with response encoding.

### REPO-025 — The root `Makefile` drives `spa/` too, and `make dev` brings up all three servers

*Accepted · 2026-08-08 · docs.*

`spa/` was deliberately absent from every Make target, on the grounds that it is independently built and independently deployed. That reasoning was right about the *builds* and wrong about the *entry point*: it meant a contributor had to know that a second frontend existed, find its README, and start it by hand — and the two frontends exist precisely so they can be compared, which is hard when only one of them is running. So `bootstrap`, `build`, `test` and `clean` now cover it (`spa-build`, `spa-test`, `spa-check`), and `make dev` runs vite alongside catlogd and mockidp.

**Nothing about the independence changed, and that is checkable rather than asserted.** Every `spa-*` target is a one-line `pnpm -C spa …` that behaves identically typed by hand; `spa/` keeps its own lockfile, its own toolchain, its own CI workflow and its own deployment; and it still needs no Go or .NET toolchain to install, lint, test or build. What the Makefile provides is a place to find the commands, not a coupling — and `make dev-server` exists for the cases that genuinely want catlogd and mockidp alone (`make loadgen`, and anything driving the e2e suites).

**Vite is bound to `127.0.0.1` explicitly** (`SPA_HOST`), because vite otherwise binds `localhost`, which resolves to `::1` first on macOS — that would make the reader the one component in the repo not reachable at the 127.0.0.1 address every other config, doc and allow-list entry uses. **`CATLOG_DEV_API` is threaded from `SERVER_URL`**, so vite's `/v1` proxy follows an overridden server address instead of silently proxying to the default.

**`make dev` is same-origin and therefore cannot test CORS — deliberately.** Vite proxies `/v1` to catlogd, which is the point (UI-030: development must not depend on the allow-list being right). `make spa-preview` is the other half: the built bundle on its own origin at `:4173`, cross-origin against catlogd, which is the shape a real deployment has and the only local target that exercises `[cors] allowed_origins` for real. Both ports were already in the dev config's allow-list.

**`spa-deps` is a guard, not a build step.** It fails with the fix in the message when `spa/node_modules` is missing, because vite's own failure would otherwise scroll past inside a three-process `make dev` and look like the server broke.

---

## Storage — Turso, schema & compression

Everything that follows from an embedded database that takes an exclusive whole-file lock.

### STORE-001 — Go dependency versions resolved and pinned

*Accepted · 2026-08-07 · WP1.*

Go dependency versions resolved and pinned (§5.1 requires recording these). Direct: `turso.tech/database/tursogo` **v0.7.2**, `github.com/go-jose/go-jose/v4` **v4.1.4**, `github.com/oklog/ulid/v2` **v2.1.2**, `github.com/BurntSushi/toml` **v1.6.0**. Indirect, pulled by tursogo: `github.com/tursodatabase/turso-go-platform-libs` **v0.7.2**, `github.com/ebitengine/purego` **v0.9.1**, `golang.org/x/sys` **v0.38.0**. The remaining §5.1 packages (`andybalholm/brotli`, `klauspost/compress`, `starfederation/datastar-go`, `testcontainers-go`) are **deliberately not added yet** — nothing in WP1 imports them and `go mod tidy` would drop them; WP2/WP5/WP9 add them and record versions here. Note `tursogo` is pinned exactly, not `@latest`: it has 100+ tags, mostly `-pre.N`, and its Go driver is LLM-generated from a spec shipped inside the module, so every bump needs a behaviour re-probe.

### STORE-002 — §5.4's two-`*sql.DB`-handles-per-file arrangement WORKS — implemented as specified

*Accepted · 2026-08-07 · WP1.*

The open question was whether tursogo's exclusive whole-file lock would make a writer handle (`SetMaxOpenConns(1)`) and a reader handle (`SetMaxOpenConns(4)`) on the same path deadlock within one process. It does not: the lock excludes *other processes*, not other handles in the same process. Evidence, all in `store_test.go`: `TestTwoHandlesOnOneFile` opens both handles, reads on the reader while a write transaction is open on the writer, and then runs 1 writer goroutine × 200 inserts against 4 reader goroutines × 200 queries with a 90 s deadlock guard — zero errors. The single-writer-goroutine discipline of §5.5 still stands; `SetMaxOpenConns(1)` is a backstop, not a substitute.

### STORE-003 — The one-process-per-file rule is now a test, not a comment

*Accepted · 2026-08-07 · WP1.*

The one-process-per-file rule is now a test, not a comment. `TestSecondProcessIsLockedOut` re-executes the test binary as a second process and asserts it cannot even `SELECT` from a live database (`Locking error: … File is locked by another process`). Consequences already baked into the code: `catlogctl` never opens a database file (§5.9), and — for WP9 — **rolling/blue-green deploys are impossible**; the old catlogd must fully exit before the new one starts, so plan for a brief hard downtime window in `deploy.sh` and the systemd unit.

### STORE-004 — A1: explicit WAL checkpointing, deviating from §5.4's "WAL is the default; set nothing"

*Accepted · 2026-08-07 · WP1.*

A1: explicit WAL checkpointing, deviating from §5.4's "WAL is the default; set nothing". tursogo's WAL never auto-checkpoints: `PRAGMA wal_autocheckpoint` cannot be read, *setting* it returns no error and has no effect, and `db.Close()` does not checkpoint. Left alone the `-wal` file grows for the life of the process while the main `.db` stays near-empty. `store.DB` therefore runs `PRAGMA wal_checkpoint(TRUNCATE)` on a timer (`[data] checkpoint_interval_s`, default 60 s) and once more in `Close()`. Measured in `TestWALGrowsUntilCheckpointed`: after 4000 inserts `db=4096 B wal=762232 B`; after one checkpoint `db=724992 B wal=0 B`. Two operational consequences: backups must capture the `-wal` alongside the `.db` (or checkpoint first — `catlogctl backup` in WP10 must do this), and startup WAL-replay time is bounded by the interval.

### STORE-005 — A2: `[data] checkpoint_interval_s` added to the §5.3 config

*Accepted · 2026-08-07 · WP1.*

A2: `[data] checkpoint_interval_s` added to the §5.3 config, defaulting to 60. Not in the plan, but the checkpoint timer above needs a knob and it belongs with the other storage settings. 0 disables the timer; shutdown still checkpoints. Env override `CATLOG_DATA_CHECKPOINT_INTERVAL_S`.

### STORE-006 — Correction to §13.1: `VACUUM` is not permanently unavailable in Turso

*Accepted · 2026-08-07 · WP1.*

Correction to §13.1: `VACUUM` is not permanently unavailable in Turso. tursogo v0.7.2 supports both `VACUUM` and `WITHOUT ROWID` behind a DSN flag (`?experimental=vacuum,without_rowid`); the local `turso-db` skill's "No vacuum / No WITHOUT ROWID" rules are stale for this version. **We do not enable either** — §5.4 forbids both, unknown `experimental=` values are silently ignored (a typo fails open with no error), and the feature is not worth the risk on a beta engine. Recorded because §13.1 reasons from "no VACUUM, so purge leaves free pages, monitor file size": that mitigation stands, but the escape hatch exists if free-page growth ever becomes a real problem. `store.DB.FileSize()` exposes the number `/admin/stats` needs (WP4).

### STORE-007 — `WITH RECURSIVE` is unimplemented in tursogo and no flag enables it

*Accepted · 2026-08-07 · WP1.*

Verified that nothing in either DDL or any WP1 query needs it. Carrying forward as a design constraint: **no recursive CTEs anywhere in catlog** — projections, leaderboards and the archive scan must all stay flat. `modernc.org/sqlite` remains the theoretical escape hatch but D4 forbids an abstraction layer, so treat this as permanent.

### STORE-008 — A3: `schema_version` is `(v INTEGER NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`

*Accepted · 2026-08-07 · WP1.*

A3: `schema_version` is `(v INTEGER NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`, a superset of §5.4's `schema_version(v INTEGER NOT NULL)`. Without the primary key nothing prevents a version being recorded twice, which would make the idempotence guarantee unverifiable; `name` and `applied_at` make a migration incident legible after the fact. `TestMigrationsAreIdempotent` opens the same real file three times and asserts the row set never changes.

### STORE-009 — Migration files are applied **one `Exec` per file inside its own transaction** — tursogo splits a multi-statement `Exec` itself, and it does so inside a transaction as well as outside (verified)

*Accepted · 2026-08-07 · WP1.*

Related gotcha the runner guards against: an `Exec` containing only comments fails with `turso: API misuse: got null pointer`, so `loadMigrations` rejects a comment-only migration file at load time rather than at apply time.

### STORE-010 — A4: the active license signing key's `kid` is stored in a `data/keys/license-signing.kid` sidecar

*Accepted · 2026-08-07 · WP1.*

A4: the active license signing key's `kid` is stored in a `data/keys/license-signing.kid` sidecar. §4.5.1 specifies `kid = "catlog-<yyyymm>"` but not where the month is remembered; deriving it from the PEM's mtime at every start would silently rotate the `kid` whenever the file is touched, invalidating live licenses. The sidecar is written at creation. Rotation (§4.5.1 "add a key with a new kid, keep the old until all licenses expire") is implemented by dropping retired keys in as `license-signing-<kid>.pem`: they are loaded **verify-only**, appear in the JWKS and resolve via `keys.Set.SigningKeyByKID`, and never sign.

### STORE-011 — A5: `keys` refuses to load a secret that is group- or world-readable

*Accepted · 2026-08-07 · WP1.*

A5: `keys` refuses to load a secret that is group- or world-readable (mode `& 0o077 != 0` is a hard error, not a warning) and writes every secret `O_EXCL` at `0600` under a `0700` directory. Not in the plan; a leaked pepper is unrecoverable (D17 — it would let an attacker link every `user_key` back to an IdP subject), and `O_EXCL` means losing a concurrent `keygen` race cannot destroy an existing key.

### STORE-012 — §5.11 secret hygiene is enforced by types, not by discipline

*Accepted · 2026-08-07 · WP1.*

§5.11 secret hygiene is enforced by types, not by discipline. `keys.Set` and `keys.UserKey` implement `slog.LogValuer` **and** `fmt.Stringer`, so neither structured logging nor a stray `%v` can emit the pepper, the session key or a full `user_key` — a `user_key` always renders as its ≤8-char b64u prefix. `TestSecretsNeverRender` asserts this against a real JSON handler.

### STORE-013 — Dedup and upserts use `INSERT OR IGNORE` / `ON CONFLICT DO UPDATE` **everywhere**, never error inspection

*Accepted · 2026-08-07 · WP1.*

tursogo collapses every constraint violation onto a single `ErrTursoConstraint` sentinel with no extended result code, so UNIQUE and NOT NULL are indistinguishable without brittle substring matching. This is why `ClaimHandle` reports `ErrHandleTaken` from `RowsAffected() == 0` rather than from an error, and why `InsertEvents` derives its accepted/deduped counts the same way.

### STORE-014 — Payloads are per-event zstd against a shared trained dictionary, because 54% of the dominant payload was byte-identical key names

*Accepted · 2026-08-08 · WP-STORE-ZSTD.*

Measured on the production-shaped log: `telemetry.window` is 83.8% of rows and 63% of the file, and a 16 KB dictionary (trained with `zstd --train`, cover, on 5,149 payloads stratified across all 22 types; provenance in the source) captures exactly the repeated structure a 490 B payload cannot self-describe. Migration `0003`: append-only `payload_dict` (dictionaries are never mutated, so any historical row stays decodable forever) + `event.enc` (0 = legacy JSON text, 1 = zstd+dict). **Old rows are never rewritten** — with no VACUUM, a lazy migration is the only honest one; the projected −46% is realized on new writes and fully on the next restore-into-fresh-DB, which `RestoreEvents` also compresses for exactly that reason. Write path falls back to enc=0 verbatim if compression fails or does not strictly shrink — an event is never lost to its own compression. Decode lives at the single `scanStoredEvents` seam, so projector, readapi and the archive see byte-identical JSON and the archive's deterministic chunks are unchanged (asserted by test against the real Archiver). `[data] compress_payloads` (default true) is the escape hatch; reads always handle both encodings.

### STORE-015 — Measured 3.25×, not the analysis's 4.50×, and the delta is the encoder, recorded so nobody re-litigates it

*Accepted · 2026-08-08 · WP-STORE-ZSTD.*

Measured 3.25×, not the analysis's 4.50×, and the delta is the encoder, recorded so nobody re-litigates it. 50,223 real payloads: 21.6 MB → 6.66 MB through klauspost (payload −69%, projected file −46%); the reference zstd CLI with the same dictionary reaches 3.74× — klauspost's dictionary encoding is simply weaker than libzstd's. Knobs measured and rejected: CRC off buys 1.5 pp and drops the only per-payload corruption check; higher levels buy ≤3% and SpeedBest is worse *and* 40× slower with dictionaries. Shipped: level 3 + CRC. `BenchmarkDrain` got ~3.6% *faster* (3.41 → 3.28 s) — smaller blobs save more pager I/O than DecodeAll costs.

### STORE-016 — `ev_player` keeps its `seq` column, and this is the second time a slim-index premise died on tursogo's planner

*Accepted · 2026-08-08 · WP-STORE-ZSTD.*

On classic SQLite the implicit rowid tail makes `(player_id)` equivalent to `(player_id, seq)`; tursogo does not implement the rowid-tail seek — the cursor page plans `SeekLE key=[player_id]` plus a per-row `IdxRowId→Ge→Prev` filter, so page N of a busy player's history would walk N pages of index. 4 B/row is not worth quadratic paging. `TestEvPlayerIndexPlans` pins the two-column seek so an engine upgrade can revisit. Also corrected in this pass: every comment claiming tursogo *lacks* VACUUM/WITHOUT ROWID now says they are unused **by policy (§5.4), not capability**. Deferred with intent, for the next restore-carried migration: positional payload encoding (lossless −43% of payload), column interning, and 16 KiB pages — all want a fresh database, and restore-from-archive is the sanctioned carrier; local retention pruning stays gated on the §5.10 restore drill being rehearsed.

---

## Ingest, auth & the conformance vectors

The §4.5.3 verification chain, the idempotency contract, and how the cross-language vectors stay byte-identical.

### INGEST-001 — Dependency added: `github.com/andybalholm/brotli` v1.2.2

*Accepted · 2026-08-07 · WP2.*

Dependency added: `github.com/andybalholm/brotli` v1.2.2 (§5.1's ingest decompression). It is the only new module WP2 needs; `klauspost/compress`, `datastar-go` and `testcontainers-go` remain deliberately unadded until WP10/WP5/WP9 import them.

### INGEST-002 — §4.5.3 step 1 does its own structural parse instead of calling `cjws.ParseCompactUnverified`

*Accepted · 2026-08-07 · WP2.*

§4.5.3 step 1 does its own structural parse instead of calling `cjws.ParseCompactUnverified`. `cjws`'s parser enforces the `{ES256}` allow-list, so routing step 1 through it would attribute an `alg` failure to step 1 rather than to steps 2/6, and would allocate a go-jose object before anything has been authenticated. `authz.splitCompact` therefore does size + three-base64url-segments + header-is-JSON, and go-jose is not touched until step 2. The ordering is a DoS property, so it is asserted directly: `Verifier.Stats()` counts ECDSA verifications and `TestCheapChecksRunBeforeSignatureVerification` pins "step 1 costs zero, an expired license costs one, only a valid request costs two".

### INGEST-003 — Per-step §4.9 code assignments, where §4.5.3 named a step but not a code

*Accepted · 2026-08-07 · WP2.*

Step 1: a missing/oversize/unparseable license is `license_invalid`, the same for the proof is `proof_invalid` — both 401, because step 1 is part of the auth chain and §4.4 maps auth failures to 401 (not `bad_request`, and not `too_large`: the §4.3 `too_large` limits are all about the *body*). Step 2 and step 3's `iss`/`ver`/malformed-claims cases: `license_invalid`; step 3 expiry: `license_expired`. Step 4: banned `sub` → `banned`, revoked `jkt` → `license_revoked`. Step 5: missing credential row, handle mismatch or `sub`↔player mismatch → `license_invalid`; a revoked row → `license_revoked`; a banned player → `banned`. Steps 6, 7 and step 8's `htm`/`htu`/structural failures → `proof_invalid`; step 8 skew → `clock_skew`. Step 9 → `rate_limited` (+`Retry-After`). Step 10 → `proof_invalid`. Step 12 → `stream_fork`. Step 13 → `malformed_batch` or `too_large`.

### INGEST-004 — `server_time` is emitted on every 401, not only on `clock_skew`

*Accepted · 2026-08-07 · WP2.*

`server_time` is emitted on every 401, not only on `clock_skew`. §4.4's table gives the 401 row the body `{"error": <auth code>, "server_time": unix_ms}` and then notes that `clock_skew` includes it; sending it on all of them is the superset, costs nothing, and means the mod can correct its offset from any auth failure rather than only the one that names the problem.

### INGEST-005 — Only `Content-Encoding` is enforced on the ingest request; `Content-Type` is not

*Accepted · 2026-08-07 · WP2.*

Only `Content-Encoding` is enforced on the ingest request; `Content-Type` is not. §4.4 shows both headers and §4.9 has exactly one code for the pair (`unsupported_encoding`, 415). The body's framing is fully validated anyway (brotli, then NDJSON, then envelopes), so rejecting on a proxy-rewritten or absent `Content-Type` would add false negatives without adding a single real check. `Content-Encoding: br` is required exactly, case-insensitively, with no stacked encodings.

### INGEST-006 — The §5.5 backpressure 503 carries `{"error": "internal", "detail": "ingest queue is full"}` and `Retry-After: 5`

*Accepted · 2026-08-07 · WP2.*

The §5.5 backpressure 503 carries `{"error": "internal", "detail": "ingest queue is full"}` and `Retry-After: 5`. §5.5 fixes the status and the header but not a body code, and §4.9 has no "busy" code. `internal` is the honest one from the registry (the request was fine; the server could not take it), and the mod's documented `5xx` path — exponential backoff with coalescing — is already the right reaction. The same shape is used when the writer misses the handler's 30 s deadline.

### INGEST-007 — The decompress/parse half of §4.5.3 step 13 runs in the handler, before the queue; only the transaction runs in the writer

*Accepted · 2026-08-07 · WP2.*

The decompress/parse half of §4.5.3 step 13 runs in the handler, before the queue; only the transaction runs in the writer. §5.5 is explicit that `WriteJob` carries `events []DecodedEvent`, which forces decoding to precede submission. Consequence worth knowing: a replayed batch (step 11) still pays for its own decompression and envelope validation before the short-circuit is discovered. That is the plan's trade — it keeps the single writer goroutine doing nothing but I/O-bound transaction work, which is what makes one writer enough.

### INGEST-008 — `cjws` gains `SignES256Deterministic` (RFC 6979) — this is how §4.10's byte-identical regeneration is achieved

*Accepted · 2026-08-07 · WP2.*

ECDSA is randomized, so the vectors would otherwise change on every run. Go 1.24+ implements FIPS 186-5 / RFC 6979 deterministic ECDSA and `(*ecdsa.PrivateKey).Sign(nil, digest, crypto.SHA256)` selects it; the new function converts that DER signature to the r‖s form JWS needs and builds the protected header itself, in a fixed member order (`alg`, `kid`, `typ`, `jwk`), because the header is covered by the signature. `TestRFC6979Vector` pins it to RFC 6979 Appendix A.2.5 rather than to "it was stable twice", and a companion assertion proves the ordinary randomized `SignES256` is still randomized. Production signing is unchanged: only the vector generator uses the deterministic path.

### INGEST-009 — Everything else in the vectors is deterministic by construction

*Accepted · 2026-08-07 · WP2.*

Everything else in the vectors is deterministic by construction: the three P-256 keys are PEM constants in `internal/testvectors`, all times derive from `reference_time = 1770000000`, ULIDs are built from a fixed timestamp plus ten SHA-256-derived entropy bytes (never minted), every claim set is a Go struct so `encoding/json` fixes the member order, and brotli is pinned to quality 5 / LGWin 22 with the module version pinned in `go.mod`. Two independent regenerations and a regeneration over the committed tree are asserted by `TestGenerateIsByteIdentical` and `TestCommittedVectorsAreCurrent`, so an edit to the generator that is not committed fails `make server-test`.

### INGEST-010 — `expected/verify-results.json` carries scalars beside §4.10's file→result map

*Accepted · 2026-08-07 · WP2.*

The map is the `files` member; `reference_time`, `issuer`, `htu`, `handle`, `jkt`, `steps` and `note` sit next to it. Without `reference_time` the set is unusable — the valid license expires 2026-07-31, so *every* consumer would see it as expired at any real clock — and §4.10 fixes the file list, not the file's internal shape. `steps: "1-10"` records that `ok` covers the credential checks only: `proof-002.jws` is `ok` even though a server with no `stream_state` row answers it 409 (step 12 is state, not cryptography).

### INGEST-011 — `license/license-claims.json` holds the exact signed payload bytes plus one trailing newline

*Accepted · 2026-08-07 · WP2.*

`license/license-claims.json` holds the exact signed payload bytes plus one trailing newline, not a pretty-printed rendering, so a consumer can compare it directly against `b64u_decode(license-valid.jws.split('.')[1])`. Every other text vector (`*.jws`, `*.txt`) likewise ends in a single `\n` that consumers must trim; `batch-001.br` is binary and has none, and `batch-001.ndjson`'s final newline is part of the NDJSON framing.

### INGEST-012 — Two packages beyond §5.2: `internal/testvectors` and `server/integration`

*Accepted · 2026-08-07 · WP2.*

Two packages beyond §5.2: `internal/testvectors` and `server/integration`. §5.9 lists `testvectors generate` as a local `catlogctl` verb but §5.2 gives it no home; putting the generator in `cmd/catlogctl` would make the reproducibility tests (which need to generate into temp directories and compare trees) live in `package main`. `server/integration` holds the `-tags integration` suite that builds and runs the real binaries; it carries an untagged `doc.go` so `go build./...` and `go vet./...` still see a package.

### INGEST-013 — `store.Events.BannedUserKeys` added

*Accepted · 2026-08-07 · WP2.*

`store.Events.BannedUserKeys` added so the §5.8 deny-list can be rebuilt from the database at start: §5.8 says "tombstones + revoked credentials", but a *banned* player is neither until they are purged, and step 4 has to catch them before the step-5 query. Bans are therefore in the in-memory set as well as on the player row.

### INGEST-014 — `testutil.Config` now sets `server.listen` and `server.admin_listen` to `127.0.0.1:0`

*Accepted · 2026-08-07 · WP2.*

`testutil.Config` now sets `server.listen` and `server.admin_listen` to `127.0.0.1:0`. §3's fixed 8080/6060 belong to the developer's own catlogd; a test that bound them failed whenever one was running, and WP2 is the first work package to start the admin listener.

### INGEST-015 — `POST /admin/issue` accepts an optional `jwk` and, when it is absent, generates the key pair server-side and returns `private_key_pem`

*Accepted · 2026-08-07 · WP2.*

`POST /admin/issue` accepts an optional `jwk` and, when it is absent, generates the key pair server-side and returns `private_key_pem`. §5.9 writes `{handle, jwk?}` and says the *CLI* generates the key; `catlogctl issue` does exactly that, so the private half never leaves the machine on the normal path. The server-side branch exists so the endpoint is usable from `curl` in dev, and it is defensible only because of the next entry. It is dev/test only and has no bearing on the §4.8 dashboard flow, where the key is generated in the browser.

### INGEST-016 — The admin mux refuses any non-loopback peer, in addition to binding loopback

*Accepted · 2026-08-07 · WP2.*

The admin mux refuses any non-loopback peer, in addition to binding loopback. §5.9 makes it unauthenticated on the grounds that it is loopback-only and never proxied; a one-line typo (`admin_listen = "0.0.0.0:6060"`) would otherwise turn credential issuance into an open endpoint. With the guard, a misconfigured address fails closed.

### INGEST-017 — `/admin/issue` validates the §4.7 handle *format* only

*Accepted · 2026-08-07 · WP2.*

The reserved list, the ≤5-live-handles quota, the ≤3-issuances-per-day quota and the retired-handle rules are WP3's, and duplicating them in the dev path would create two places to drift. What is enforced here: the regex, "the handle must not belong to another player", and one credential per key (a reissue means a new key pair, which is also what the browser wizard does).

### INGEST-018 — expvar counters beyond §5.9's list

*Accepted · 2026-08-07 · WP2.*

expvar counters beyond §5.9's list: `ingest_batches`, `ingest_replayed`, `ingest_rejected` (the total beside the per-code ones) and `ingest_queue_depth`. The queue depth is the important one — §5.5's bounded channel is invisible until it is already full and answering 503. All `ingest_rejected_<code>` counters for every §4.9 code are published at init, including codes ingest cannot produce, so a dashboard never has to guess whether a missing variable means "zero" or "not wired".

### INGEST-019 — NDJSON framing is strict

*Accepted · 2026-08-07 · WP2.*

NDJSON framing is strict: an empty batch, an interior blank line, CRLF line endings and trailing content after an envelope are all `400 malformed_batch`. §4.1 only says one event per line; being strict costs the mod nothing (it writes the frames) and turns a whole class of silent truncation into a loud rejection. A single trailing `\n` is the normal case and is not an empty line.

### INGEST-020 — The ingest handler sets `Date` explicitly rather than leaving it to `net/http`

*Accepted · 2026-08-07 · WP2.*

The ingest handler sets `Date` explicitly rather than leaving it to `net/http`. §4.4 requires it on every response and the mod's `clock_skew` recovery depends on it; setting it in the handler means it is present on paths that never reach the standard response writer, and makes it assertable from `httptest.ResponseRecorder`.

### INGEST-021 — The vectors' license `sub` is `SHA-256("catlog-testvectors-user")`, not a pepper-derived `user_key`

*Accepted · 2026-08-07 · WP2.*

D17 derives `user_key` from the pepper, and the vectors have no pepper (nor should they ship one). What a verifier checks about `sub` is that it is 32 bytes of base64url, which this satisfies; the C# side never derives it.

### INGEST-022 — The rate limiter's bucket map is swept, not expired

*Accepted · 2026-08-07 · WP2.*

At 50k live buckets it drops every bucket that has refilled to its burst ceiling — which is lossless rather than approximate, because a full bucket is indistinguishable from one that was never created. The §5.11 rejection-log throttle uses the same trick at 10k keys.

### INGEST-023 — Idempotency is now a written contract in `docs/ingest-api.md`, not only a set of tests

*Accepted · 2026-08-07 · WP2.*

New "Idempotency contract" section states the key at both levels — `(player_id, event_id)` for events, `(player_id, batch_id)` for batches, client-minted ULID + server-derived player in each — how the server derives the player half (step 5, credential row by `cnf.jkt`, cross-checked against the license `sub`'s `user_key`; nothing in the request body participates), and a table of exactly which retries are safe. Verified end to end rather than assumed: a hostile client cannot write into another player's namespace because §4.1 rejects **unknown envelope keys**, so there is no identity field to forge and none can be added; unknown *payload* keys are preserved verbatim but never read as identity. New suites `server/internal/ingest/idempotency_test.go` (forged envelope keys for `player_id`/`player`/`handle`/`sub`/`user_key`/`jkt` → 400; a payload naming another player changes no attribution; the same event id under two credentials is two rows; batch ids scoped per player; six identical requests store six events; overlap reported as `{accepted:2, deduped:2}`; store closed and reopened from disk still deduplicates) and `server/integration/idempotency_test.go` (the same against the real `catlogd` binary over HTTP, **including a full process restart mid-suite** — `server.restart()` was added to the fixture for it). Existing coverage was cited rather than duplicated where it already sufficed: `TestInsertEventsDedup` for the union merge and the cross-player case at store level, `TestBatchReplayShortCircuit` for `(player, batch_id)` scoping, `TestIngestDedupAndReplay` for the two-shipment case.

### INGEST-024 — The stream chain's documented justification is now honest, and `stream_state.gap` is finally read

*Accepted · 2026-08-07 · WP2.*

The stream chain's documented justification is now honest, and `stream_state.gap` is finally read. `plans/CATLOG_PROPOSALS.md:234` framed a fork as "a high-signal indicator of credential theft"; nothing in the repo delivers that — a fork is not counted per player, not alerted on, not retained, and the documented client recovery is to mint a new `sid` and carry on, so a thief pays exactly one `409`. The chain is also not load-bearing for dedup (that is the `ev_dedup` index plus the batch replay short-circuit) or for ordering (that is the server-local `event.seq` rowid). `docs/ingest-api.md` now says so explicitly under "What the stream chain actually buys", listing what it *does* provide — gap visibility, ordering hygiene, debuggability — and what it does not. The chain stays: D5, the wire contract, the mod mirror and the §4.10 conformance vectors all pin it. Separately, `gap` was written on every commit (stickily, via `max()`) and read by **nothing**, which made the chain's cost real and its one genuine benefit unrealised. New `store.Events.StreamCensus` counts streams, gapped streams and distinct gapped players; `GET /admin/stats` gained a `streams` object and `catlogctl stats` a `streams` line. `gapped_players` is what separates "one client churning through streams" from "everyone is losing batches". Tests: `TestStreamCensus` (store) and `TestStreamGapIsVisibleInAdminStats` (integration, real binary, incl. the sticky-marker case).

---

## Identity, handles & moderation

OAuth against three providers, `user_key` derivation, sessions, and what ban / unban / purge actually do.

### IDENT-001 — A-WP3-1: the §4.5.4 session MAC covers `user_key_bytes || decimal-ASCII(exp)`

*Accepted · 2026-08-07 · WP3.*

A-WP3-1: the §4.5.4 session MAC covers `user_key_bytes || decimal-ASCII(exp)`. §4.5.4 writes `HMAC-SHA256(session_key, user_key_bytes || exp)` without pinning an integer encoding. The cookie already carries `exp` as decimal text, so the MAC covers exactly the bytes presented — no second representation to get wrong, and verification needs no re-encoding step. Pinned by `TestSessionCookieShape`, which recomputes the MAC from the spec rather than from the code.

### IDENT-002 — A-WP3-2: `/.well-known/catlog-denylist.json` serves a compact JWS with `Content-Type: application/jose`, not a JSON object

*Accepted · 2026-08-07 · WP3.*

A-WP3-2: `/.well-known/catlog-denylist.json` serves a compact JWS with `Content-Type: application/jose`, not a JSON object. §5.8 says "published as signed JWS" and §4.8 fixes the `.json` path; the two cannot both be honoured literally. The signature is the load-bearing half (a future second node authenticates a ban list it pulls over HTTP), so the body is the JWS and the path keeps its documented spelling. The payload is exactly §5.8's `{ver, updated_at, banned_subs, revoked_jkts}`, and it verifies against a key published at `/.well-known/catlog-jwks.json`. Header `typ` is `catlog-denylist+jwt`, distinct from license and proof.

### IDENT-003 — A-WP3-3: a ban retires the handle_lc but keeps the `handle` row; only a purge deletes it

*Accepted · 2026-08-07 · WP3.*

A-WP3-3: a ban retires the handle_lc but keeps the `handle` row; only a purge deletes it. §5.9 says ban "retires handle(s)" and §12 WP3 requires "ban → ingest 401 → unban restores" — reconciled by splitting retirement (a `retired_handle` row, which blocks *anyone else*, D9) from ownership (the `handle` row, which survives so an unban can hand it back). New store query `MarkHandleRetired` does the first without the second; `RetireHandle` (WP1) still does both and is the purge path.

### IDENT-004 — A-WP3-4: unban restores exactly the credentials that ban revoked, selected by the ban's timestamp

*Accepted · 2026-08-07 · WP3.*

A-WP3-4: unban restores exactly the credentials that ban revoked, selected by the ban's timestamp. `Ban` stamps one `revoked_at` on every credential it revokes and `Unban` clears `revoked_at = banned_at` (`store.UnrevokeCredentialsAt`), so a credential the player revoked from the dashboard, or one an earlier ban revoked, stays revoked. The ban's timestamp is stepped forward past any existing revocation on that player so the selector cannot collide (a frozen test clock makes a collision certain rather than merely possible).

### IDENT-005 — A-WP3-5: a purged account cannot log in again

*Accepted · 2026-08-07 · WP3.*

A-WP3-5: a purged account cannot log in again. §4.7's purge keeps a tombstone, and `authz.DenyList.LoadFrom` reads tombstones as banned subjects — so the account is refused at §4.5.3 step 4 forever. Allowing the login would only mint sessions whose licenses ingest rejects as `banned`, so `/auth/{idp}/callback` consults the same deny-list and shows the "account banned or deleted" response with no session. Applies to `POST /api/me/delete` too: delete-my-data is a purge.

### IDENT-006 — A-WP3-6: reissue revokes the handle's previous live credentials

*Accepted · 2026-08-07 · WP3.*

D16 makes reissue "the ban/deny-list touchpoint"; §4.8 does not say what happens to the credential being replaced. It is revoked in both halves (row + in-memory set), because the reason a player reissues is that the old credential file is lost or compromised. `POST /api/handles/{handle}/revoke` keeps the handle (D9: immutable, permanent) and only kills the credentials.

### IDENT-007 — A-WP3-7: an IdP `id_token` may be RS256 or ES256

*Accepted · 2026-08-07 · WP3.*

A-WP3-7: an IdP `id_token` may be RS256 or ES256. catlog's own JWS allow-list stays exactly `{ES256}` (§4.5); this is somebody else's signature. Real Google signs with RS256, and `mockidp` signs with ES256 so the JWKS-fetch path is genuinely exercised in dev. Still an allow-list — `none` and HMAC confusion remain impossible. The JWKS cache refetches on a `kid` miss (floored at 10 s) as well as on expiry, so a provider rotating keys — including mockidp minting a new one at every start — recovers without a catlogd restart.

### IDENT-008 — A-WP3-8: `[auth] reserved_handles` is the §4.7 "+ configurable extras" knob

*Accepted · 2026-08-07 · WP3.*

A-WP3-8: `[auth] reserved_handles` is the §4.7 "+ configurable extras" knob, added to `config.Auth`. Extras are *added* to the built-in list, never substituted for it, and matched case-insensitively. `server/catlogd.dev.toml` carries an empty list to document the knob.

### IDENT-009 — A-WP3-9: `mockidp` rejects any authorize request carrying an email scope (400 `invalid_scope`)

*Accepted · 2026-08-07 · WP3.*

D17 says catlog never requests an email; a mock provider that happily granted one would let the rule rot into a comment. The mock enforces it, so the guarantee is tested rather than asserted. `mockidp` also mints its Google signing key fresh at every start and persists nothing.

### IDENT-010 — A-WP3-10: `catlogctl backup` landed in WP3 rather than WP10

*Accepted · 2026-08-07 · WP3.*

A-WP3-10: `catlogctl backup` landed in WP3 rather than WP10. §5.9 lists it next to the other admin verbs and it needs nothing from the archiver. "Quiesce the writer" is implemented as a write transaction on events.db — the single writer connection (§5.4) is what actually excludes the ingest writer goroutine; the admin mutex alone would not. The `-wal` sidecar is copied alongside the main file because the Turso WAL never auto-checkpoints (WP1). `projections.db` is not backed up: it is rebuildable by design (D8).

### IDENT-011 — A-WP3-11: the login-failure page is a hard-coded minimal HTML document, not a template

*Accepted · 2026-08-07 · WP3.*

WP5 owns `web/templates/`; the OAuth callback is a top-level navigation and must answer a browser before then. The contract WP5 and its playwright suite consume is `#auth-error[data-error="<§4.9 code>"]` plus `#auth-error-code` / `#auth-error-detail`; a request with `Accept: application/json` gets the §4.9 body instead.

### IDENT-012 — A-WP3-12 (defect fix, found by the WP7 simulator): every path that creates a handle now reloads the in-memory directory (§5.4)

*Accepted · 2026-08-07 · WP3.*

A-WP3-12 (defect fix, found by the WP7 simulator): every path that creates a handle now reloads the in-memory directory (§5.4). `internal/directory` is loaded once at start and, before this, was reloaded only by `POST /admin/seed` — so a handle claimed against a *running* catlogd (`catlogctl issue`, and the dashboard's `POST /api/handles`) was unknown to the read path until a restart. The events folded correctly the whole time and every board row for that player was silently invisible: `GET /v1/players/{handle}` 404'd and `readapi.visibleRows` dropped the player as "holding no handle yet". `adminapi.Server.reloadDirectory` is now called from `POST /admin/issue` (a four-line additive edit to WP2's `issue.go`), and `identity.Server.reloadDirectory` from claim, reissue, revoke and delete — uniformly, including the paths that cannot change the map, because "every write reloads" is checkable by reading the call sites while "the writes that change handle rows reload" is a judgement each new call site has to make again. Ban/unban/purge already reloaded via `Moderator.refresh`. Regressions: `adminapi.TestIssueMakesTheHandleResolvableImmediately` (verified to fail with the reload removed) and `integration.TestRuntimeHandleIsVisibleWithoutARestart`, which covers both creation paths and asserts the player reaches a leaderboard, not just their profile.

### IDENT-013 — A-WP3-13: a ban surfaces at ingest as `banned`, a bare revoke as `license_revoked`

*Accepted · 2026-08-07 · WP3.*

A-WP3-13: a ban surfaces at ingest as `banned`, a bare revoke as `license_revoked`. §4.5.3 step 4 checks `sub` before `jkt`, and a ban writes both, so `banned` is the code the chain produces for a banned account — not `license_revoked`. Revoking a credential from the dashboard leaves the sub clean and yields `license_revoked`, with the account, its handle and its profile untouched (D9: revoking is not banning). Both are pinned: `integration.TestBanBlocksIngestAndUnbanRestoresIt` and `integration.TestRevokeWithoutABanIsLicenseRevoked`.

### IDENT-014 — A-WP3-14: `integration.TestRuntimeHandleIsVisibleWithoutARestart` asserts a named board with a literal value, not "whatever the profile lists first"

*Accepted · 2026-08-07 · WP3.*

An earlier draft waited for any stat and then checked the board it named, which made the test's meaning depend on which fold happened to sort first — it would have silently started asserting a different board the day `contracts/testdata/batches/batch-001.ndjson` or the fold registry changed. It now waits for `biggest_lithobrake_survived == 214.5`, which is what that batch's single `vehicle.impact` (survived, 2 crew, off the pad, duna) deterministically produces and is §5.6's own worked example. It additionally asserts the read-API invariant the ambiguity had hidden: **every stat a profile reports must also place that player on the corresponding board, at the same value.** Investigated and refuted the suspicion that `/v1/players/{handle}` could report a stat with no `player_stat` row — `readapi.handlePlayer` lists only real rows for that player, filtered through `stats.BoardFor`, so it can under-report a board this build has dropped but can never invent one.

### IDENT-015 — A-WP3-15: the integration fixtures now reap stray child processes (`integration/main_test.go`)

*Accepted · 2026-08-07 · WP3.*

A `catlogd` that outlives its test holds tursogo's exclusive whole-file lock (§5.4), which shuts every later process out of that database entirely — a silent, compounding failure that gets blamed on something else. Every child a fixture starts is registered, and `TestMain` kills and reaps whatever survives `m.Run()`, **failing an otherwise-passing run** so a leak cannot hide; a SIGINT handler does the same on Ctrl-C. `server.stop()` and `mockidp.stop()` now deregister unconditionally and `Wait()` after a `Kill()` (an unwaited child stays a zombie and may not release the lock), and the graceful window dropped from 30 s to 15 s so a wedged fixture is never itself the reason a run times out. Verified by removing the fixture's `t.Cleanup` and watching the run flip from PASS to FAIL with `killing stray catlogd (pid …)`. Not covered: `go test -timeout` firing, which panics the binary from Go's own watchdog and runs no cleanup or TestMain code at all.

---

## Projector, boards & the read API

The fold, the rebuild backstop, the board families, the rolling windows, and every public read surface.

### PROJ-001 — The flag exclusion covers every fold, not only the "record" folds §5.6 names

*Accepted · 2026-08-07 · WP4.*

The flag exclusion covers every fold, not only the "record" folds §5.6 names. §5.6 says "all *record* folds skip events whose flight has any flag bit set", which would leave `kitten_tumbles`, `rud_*`, `orbits_achieved`, `soi_bodies`, `dockings`, `stagings` and `kittens_recovered` scoring on a cheated flight. That reading also makes the `tuning` flag pointless: [events.md](events.md) added it precisely because the game's debug window live-edits `KittenLocomotionTuning.Current.TumbleSpeedGate`, the sole classifier for `kitten.tumble` — a **counter** board. `docs/` wins over the plan text, so every fold that attributes an event to a flight honours the flags. `distance_travelled` is the sole exception and cannot be otherwise: `roster.snapshot` carries `flight: null` (§4.1), so there is no flight to have been flagged.

### PROJ-002 — `flight_state.flags` gains bit5 = `other`

*Accepted · 2026-08-07 · WP4.*

`flight_state.flags` gains bit5 = `other`, beyond §5.4's four bits and the `tuning` bit5→bit4 the WP0 amendment added (final layout: bit0 teleport, bit1 refuel, bit2 resource_edit, bit3 console, bit4 tuning, bit5 other). An unrecognised `flight.flagged.flag` value sets it. Failing open — treating an unknown flag as no flag — would make every future flag value a scoring loophole for as long as the server lagged the mod, and "a newer mod says something is wrong with this flight" is not a statement to ignore.

### PROJ-003 — The `stats.Fold` signature is `Apply(ctx, tx, ev, fs)`, not §5.6's `Apply(tx, ev, fs)`, and folds also carry `Name()`

*Accepted · 2026-08-07 · WP4.*

Every database call underneath takes a context and a rebuild has to be cancellable; `Name()` is what makes "which fold failed at which seq" a usable log line rather than an anonymous SQL error. `FlightStateReader` likewise gains two methods beyond §5.6's `Flight`: `Refined()` and `KIANear(flight, simT)`. They live there because a fold has exactly three parameters and the refinement is a property of the *pass*, not of the event — during incremental folding `Refined()` is false and `KIANear` always answers false, so one body of fold code produces both the optimistic incremental answer and the exact rebuild answer with no branching anywhere else.

### PROJ-004 — The rebuild is two passes over the log, not one

*Accepted · 2026-08-07 · WP4.*

The rebuild is two passes over the log, not one. §5.6 says "same folds + the KIA-window refinement pass"; the shape that actually delivers it is pass 1 = flight-state fold only, collecting a `flight → []kitten.kia sim_t` index, then pass 2 = board folds against a `flight_state` already complete for the entire history. That single change is what heals **all three** things the incremental path cannot know at fold time: a `flight.flagged` that arrives after its flight already scored, the §4.2 ±2 s KIA window on `biggest_lithobrake_survived`, and §5.6's `ended_reason == 'recovered'` condition on `peak_g_survived`. Consequence worth stating plainly: **rebuild ≠ incremental whenever a history contains a late flag, a scuttled kitten, or a flight that did not end recovered** — that is the point of D22, not a defect. `TestRebuildEqualsIncrementalForAnUnflaggedHistory` therefore fixes a history with none of those three and compares every projection table column for column.

### PROJ-005 — Fold *writes* live in `stats`, projection *reads* live in `store`

*Accepted · 2026-08-07 · WP4.*

Fold *writes* live in `stats`, projection *reads* live in `store`. §5.2 gives `store` "typed queries"; putting `INSERT … ON CONFLICT … WHERE excluded.value > player_stat.value` there would separate the interesting half of every board from its meaning, because that WHERE clause **is** the rule "ties keep the earliest updated_seq". The read side (leaderboard pages, profiles, ranks, feed, the census) stays in `store/projections.go`, where it is shared with the read API and sits next to the schema.

### PROJ-006 — Two packages beyond §5.2: `internal/directory` and `internal/seed`

*Accepted · 2026-08-07 · WP4.*

Two packages beyond §5.2: `internal/directory` and `internal/seed`. §5.4 requires a Go-side `player_id → handle` map because the two database files cannot be joined; that map is also where banned players are filtered out of every read surface at once (§4.7's "fast path filters banned players"), so it is a real component with its own tests rather than a field on something else. `internal/seed` holds the §5.9 demo dataset; keeping ~200 lines of fixture data out of `adminapi` mattered here because WP3 was editing that package concurrently. Precedent: WP2 added `internal/testvectors` for the same reason.

### PROJ-007 — A banned player is *absent* from the directory, not marked in it

*Accepted · 2026-08-07 · WP4.*

One consequence is that every read surface fails closed by construction: no handle resolves, no board row renders, no feed line names them. The other is that `GET /v1/players/{handle}` answers 404 identically for unknown, retired and banned handles — distinguishing them would make the endpoint a ban oracle. Ranks stay consistent with the board page by subtracting the banned players that outrank the profile (`StatAhead` minus `StatsForPlayers(banned)`), so a ban closes the gap it leaves rather than leaving a hole in the numbering.

### PROJ-008 — `GET /v1/leaderboards` lists every board even when nobody is on it, and its `count` is unfiltered row count

*Accepted · 2026-08-07 · WP4.*

The set of boards is a property of the build, not of the data — a UI that discovers boards from this endpoint must not lose one because nobody has scored yet — and an exactly-filtered count would require reading and filtering the whole board on every request for a number that only ever means "this board has entries".

### PROJ-009 — `limit` is clamped to §4.8's 200 rather than rejected; a non-numeric `limit` is `400 bad_request`

*Accepted · 2026-08-07 · WP4.*

This is a CDN-cached public endpoint and clamping keeps one cache entry per (stat, limit, offset) instead of splitting it between a 400 and a 200. `Cache-Control: public, s-maxage=30, stale-while-revalidate=300` is set on **every** read-API response including the 404s: an unknown handle is as stable a public fact as a board, and it is exactly the response an enumeration scraper would otherwise force through uncached.

### PROJ-010 — §4.8's board-row `updated` (unix ms) is resolved from events.db, not stored in projections.db

*Accepted · 2026-08-07 · WP4.*

§4.8's board-row `updated` (unix ms) is resolved from events.db, not stored in projections.db. `player_stat` records *which* event set a record (`updated_seq`); the timestamp already exists as `event.recv_time`, and duplicating it into the other file would create an invariant a rebuild has to keep honest. `store.Events.RecvTimes` does one keyed lookup per page (chunked at 200 seqs). Related: the feed timestamps with `recv_time`, never the client's `wall_t` — §4.1 says `wall_t` is untrusted, and a feed ordered by it could be pinned to the top forever.

### PROJ-011 — `soi_bodies` advances by one only when its `INSERT OR IGNORE` into `player_body` actually inserts

*Accepted · 2026-08-07 · WP4.*

`soi_bodies` advances by one only when its `INSERT OR IGNORE` into `player_body` actually inserts, rather than re-running a `count(*)`. Same answer, no aggregate per event, and correct under replay. `distance_travelled` cannot use that trick (it is a sum of per-kitten maxima that any snapshot may raise), so it recomputes `sum(travelled_m)` per `roster.snapshot` — which is every ~10 minutes of play, not per frame.

### PROJ-012 — The rebuild swap keeps the old file as `<path>.old` until the reopen succeeds

*Accepted · 2026-08-07 · WP4.*

The rebuild swap keeps the old file as `<path>.old` until the reopen succeeds. §5.6 says "close the live handle, `os.Rename`, reopen". Done literally, a failure at the reopen step leaves the process with no projections database and nothing to fall back to. The sequence is: close → delete the live `-wal`/`-shm` (stale WAL frames must never be replayed onto a file that was swapped underneath them) → rename live to `.old` → rename the rebuild in → reopen → delete `.old`; a failed rename restores `.old` and reopens it. `store.DB.Close` already checkpoints `TRUNCATE`, so the sidecars are provably empty before they are removed.

### PROJ-013 — catlogd's shutdown closes the *live* projections handle, not the one it opened

*Accepted · 2026-08-07 · WP4.*

After a rebuild those are different objects on the same path, and closing the stale one would leak the real file lock — which, given §5.4's one-process rule, means the next catlogd cannot start. `projector.Live.Close()` exists for exactly this, and the defer that calls it is registered *before* the one that stops the fold loop so LIFO stops the writer first.

### PROJ-014 — An event the projector cannot decode is skipped and the checkpoint still advances

*Accepted · 2026-08-07 · WP4.*

An event the projector cannot decode is skipped and the checkpoint still advances. §4.1 says the projector "skips what it can't decode, logs once"; the load-bearing half is that skipping must not stall the cursor, or one event from a newer mod would wedge every projection behind it forever. The skip log is deduplicated per `(type, ver)` and deliberately carries no payload — payloads are player-supplied and unbounded (§5.11).

### PROJ-015 — `projector.Upcasters` ships empty and is exercised only by tests

*Accepted · 2026-08-07 · WP4.*

Every §4.2 type is `ver: 1`, so there is nothing to upcast; the registry exists now so the first payload version bump is a registration rather than a migration, because stored events are immutable forever and nothing may rewrite events.db. `ver > current` is `ErrFutureVersion` (skip + log), and a declared bump with no upcaster is `ErrNoUpcaster` — a loud programming error rather than silent data loss.

### PROJ-016 — expvar counters beyond §5.9's `projector_lag_seq` and `sse_clients`

*Accepted · 2026-08-07 · WP4.*

expvar counters beyond §5.9's `projector_lag_seq` and `sse_clients`: `projector_checkpoint_seq` (lag alone cannot tell "caught up" from "not running" — both read zero on an empty log), `projector_skipped` and `projector_rebuilds`. `GET /admin/stats` additionally reports both database file sizes and WAL sizes, which is the number §13.1 asks to be watched now that a purge cannot reclaim pages.

### PROJ-017 — The feed broadcaster drops batches for a subscriber whose buffer (8 batches) is full, rather than blocking

*Accepted · 2026-08-07 · WP4.*

The feed is a "what is happening right now" panel; back-pressuring the projector would stall every projection write behind one wedged browser tab. WP5's SSE handler primes a new subscriber from `store.Projections.RecentFeed` and then follows the live channel, so a dropped batch costs a gap, not a permanent divergence.

### PROJ-018 — The demo dataset emits its `flight.flagged` *before* the flagged flight's scoring events

*Accepted · 2026-08-07 · WP4.*

The seeded database is meant to be the canonical answer for UI development and for WP7's literal assertions, so the incremental fold of it must already equal what a rebuild produces (asserted by `TestSeedIsWhatARebuildProduces`). The late-flag case that the incremental path genuinely gets wrong is covered by `TestRebuildHealsALateFlag` instead, where it belongs. Seeded values are fixed and assertable: `demo_crasher` 214 m/s lithobrake (§5.6's own example) and one RUD of each of the six causes; `demo_ace` 9450 m/s orbital, 2410 m/s surface, 6.8 g, 2 orbits, 1 docking, 3 stagings, 3 kittens recovered, 4 210 000 m travelled; `demo_tumbler` 4 tumbles, 2 SOI bodies, 930 000 m travelled. The 999 m/s impact and 99.9 g window on `demo_crasher`'s flagged flight score nothing, which is the point of including them.

### PROJ-019 — `sim_t` already is the career clock, so the contract needed one new key and no new time field

*Accepted · 2026-08-07 · WP-CAREER.*

The owner asked for "fastest time since game start to orbit, different planets etc". Re-verified against the current decomp (build 2026.8.5.5168): `Universe.GetElapsedSimTime()` reads `_lastSimStep.NextTime` (`KSA/Universe.cs:2108`), a new game leaves it at `default(SimStep)` = **exactly 0.0** (nothing in the static ctor `:2337-2351` or `LoadSystem` `:167-179` touches it), it is **written to the save** as `UniverseData.GameTime` (`KSA/UniverseData.cs:43`) and **restored on load** (`KSA/Universe.cs:2160-2167`). `SimTime` is a `readonly struct` over one `double` of seconds (`KSA/SimTime.cs:6-8`). So `sim_t` *is* "seconds since this save's game started" and persists across quitting — an explicit `game_t` would have been a second copy of the same number. Full evidence in `docs/ksa-integration.md` §5b. Two caveats recorded there: the save round-trips the clock through a seven-component `TimeSpanReference` whose attributes are rounded to 4 decimals by `NaNFilteringXmlWriter` (±5e-5 s, irrelevant at leaderboard resolution), and a save with a missing `<GameTime/>` loads as t=0 with no error.

### PROJ-020 — `plans/CATLOG_PROPOSALS.md` §1.4's "KSA has no player/account/save GUID anywhere (verified)" is CONFIRMED against 5168 — it is one of the few claims in that document that survived re-checking

*Accepted · 2026-08-07 · WP-CAREER.*

The save root `UniverseData` has exactly four fields (`GameTime`, `Camera`, `CelestialSystems`, `KittenRoster` — `KSA/UniverseData.cs:10-20`); none is an id, GUID, seed, creation stamp or name. `rg -i "career|campaign"` over `KSA/` returns zero. `rg "Seed"` returns only terrain-noise seeds, because the system is a hand-authored XML template and not generated. Three near-misses were checked and rejected: `SaveMetaData.Created` (`KSA/SaveMetaData.cs:16-17`) is re-stamped on every write because an overwrite is `Delete(); Make(Id);` (`KSA/UncompressedSave.cs:85-89`), so it is a "last written" time wearing a "created" label; `CelestialSystemData.Id` is the system template name and is written but never read back (`KSA/CelestialSystem.cs:612-754` ignores it); the 17 procedurally-named starting kittens (`KSA/KittenRosterData.cs:29-47`) are an accidental fingerprint that mutates as kittens are created and die.

### PROJ-021 — The `career` envelope key: `ver: 1` amended in place, not bumped

*Accepted · 2026-08-07 · WP-CAREER.*

Nothing has ever been emitted to a real player, so the precedent set earlier in this file applies. `docs/events.md` §4.1 gains one required key, `career` — 16 lowercase Crockford base32 characters, never null, opaque to the server — and the server rejects a malformed or missing one as `400 malformed_batch` like any other envelope error. **Why the envelope and not `session.started`'s payload,** which was the other sanctioned option: every identity field catlog has (`id`, `flight`, `session`) is already on the envelope and a third-party implementer should find the whole identity model in one place; a stored event stays self-describing, so the archive, the admin event browser and any fold need no join and no ordering assumption (§5 — the log is the only irreplaceable thing, so it should carry its own meaning); and server-side it is strictly *fewer* pieces — one nullable column and `ev.Career`, versus a new projections table, a new reader method on the fold interface, a per-event lookup, and a dependency on `session.started` having been folded first. The cost is ~17 bytes per stored row and nothing on the wire, where Brotli collapses the repetition.

### PROJ-022 — A career is one KSA save, anchored on the save's own name, because that is the only stable per-save string the game has

*Accepted · 2026-08-07 · WP-CAREER.*

A career is one KSA save, anchored on the save's own name, because that is the only stable per-save string the game has. `GameSave.Id` (`KSA/GameSave.cs:13`) is the folder under `Documents/My Games/Kitten Space Agency/saves`. The mod derives `career = crockford32_lower(SHA-256("catlog-career:" + install_id + ":" + save_key)[0..10])` — the same construction as `kid`, so it is one more call rather than new machinery, and the install-id salt means the server never learns what a player called a save and two players' `apollo` saves do not collide. `save_key` is `"save:" + <name>`, or `"new:" + <fresh ULID>` for a game that has not been saved yet. **Two new Harmony patches, and the prefix/postfix choice is load-bearing:** `UncompressedSave.Load` gets a **prefix** (`KSA/UncompressedSave.cs:45`, instance, zero args — `__instance.Id` is the save) because `Load()` calls `Universe.DeserializeSave` itself at `:57`, so catlog's existing `SessionBoundaryPostfix` fires *inside* it and a postfix would stamp the first session after every load with the previous career's id; `UncompressedSave.Make(string)` gets a **postfix** (`:104`, the single write path — terminal `save`, the UI popup and `Overwrite()` all land there) so a career that began unsaved keeps its identity through its first save and a "save as" carries the career with it. Rejected: `Universe.DeserializeSave` carries no name/path/stream at all; `GameSaves.Selected` (`KSA/GameSaves.cs:125`) tracks *UI selection*, is stale after a terminal `load`, and can dangle across `GameSaves.Refresh()`; `UniverseData.LoadFrom` fires once per save at application start, not once at load.

### PROJ-023 — Backwards clock: mark the career, state the limitation, build no machine

*Accepted · 2026-08-07 · WP-CAREER.*

The rule, in one sentence: *a career is marked rewound when a `session.started` for it arrives carrying a `sim_t` lower than the highest `sim_t` already seen in that career* — which is exactly "an earlier save of this career was loaded", because a save load is the only thing that mints a session. It surfaces as `"rewound": true` on that career's career-time rows and does **nothing else**: no exclusion, no score, no queue, and the faster time still stands. Comparing only at session boundaries is what makes it threshold-free — inside a session the mod's emission order is deliberately a little lossy (a telemetry window closes with the sim time of its *end*, `Flush` drains pending impacts after the frame loop stops), so a naive "any decrease" test would need an epsilon tuned to the window length, and §8's honest-player test rules that out. **The career grouping is what makes the mark defensible at all:** without it, an honest player switching between two saves trips it every time. Recorded as F4 in `docs/integrity-audit.md` with the five-part test applied honestly — it fails rules 3 and 4 on a literal reading, and is kept because §8 governs mechanisms that try to *infer cheating*, which this does not and could not. **The honest limitation, stated on the boards' own contract page:** catlog cannot tell save-scumming from ordinary reloading and does not try; deleting a save and starting a new game under the same name re-uses the career id and reads as a rewind; a copied save folder is a new career.

### PROJ-024 — Two new board families, and the first ascending boards catlog has

*Accepted · 2026-08-07 · WP-CAREER.*

Two new board families, and the first ascending boards catlog has. `fastest_to_orbit` (the smallest `sim_t` at which an unflagged flight reached `vehicle.orbit phase=achieved`) and `fastest_to_<body>` (the same for `vehicle.soi to_body`), both in seconds, both `min` rather than `max`. Three conditions, each a one-liner: the event must carry a career (without one, `sim_t` has no origin), it must carry a `sim_t` ≥ 0 (absent is not zero — a missing reading scored as 0 would be an unbeatable record), and the flight must be unflagged like every other board (a teleport to orbit is not a fast ascent). The minimum is taken **per player, not per career** — your best career is the one that represents you — and the career it came from is in the row's `context`. No "first in the career" bookkeeping is needed for that to be right: within a career the clock only moves forward, so the earliest arrival *is* the minimum, and the one exception is the rewind the mark already names. `putBest` mirrors `putRecord` exactly, including the tie rule: an equal time keeps the earlier `updated_seq`, so whoever got there first keeps the rank.

### PROJ-025 — `TimedBodies` is an allow-list of the stock KSA bodies, for the same reason `RUDCauses` is

*Superseded · 2026-08-07 · WP-CAREER — superseded by **PROJ-033** — a body board now exists because a body appeared in the data.*

`TimedBodies` is an allow-list of the stock KSA bodies, for the same reason `RUDCauses` is. `vehicle.soi.to_body` is an opaque client string, so building a stat key from it would let anyone mint a leaderboard — and a million of them (§6: "stat keys are compile-time constants"). The list is the eleven permanent members of the shipped system, lowercased, read off `Content/Core/Astronomicals.xml` (`StellarBody`/`PlanetaryBody`/`AtmosphericBody`/`MinorBody`): sol, mercury, venus, earth, luna, mars, phobos, deimos, jupiter, saturn, uranus. Comets (`PeriodicComet`, `InterstellarComet` in the same file) are deliberately out — "the bodies KSA ships as permanent members" is a line a reader can check. **Nothing is lost by being conservative:** `toBodyFold` writes `player_body.first_sim_t` for *every* body it sees, including ones with no board, so widening the list later (or a future build adding a body) is one entry plus a rebuild rather than a data-loss discovery.

### PROJ-026 — The rewind mark is resolved at read time, not written into `player_stat.context`

*Accepted · 2026-08-07 · WP-CAREER.*

A career can be rewound long *after* a record was set, so a mark baked into the context at fold time would be stale until the next rebuild — the same incremental-versus-refined split that F3 already flags as the one place a number legitimately changes overnight. Instead the board row records its `career` and `readapi` joins `career.rewound` for the page (one query per distinct player on the page, and none at all for a board whose values are not career times). That makes the mark exact at every moment, keeps the projection free of a value that is not a function of the events folded so far, and costs the read path one bounded query.

### PROJ-027 — `Board` gained `ascending` and `career`, and `Leaderboard`/`StatAhead` gained a direction

*Accepted · 2026-08-07 · WP-CAREER.*

`Board` gained `ascending` and `career`, and `Leaderboard`/`StatAhead` gained a direction. `ORDER BY value DESC` is wrong for a board where the smallest number wins, and there is no honest way to fake it (storing `-sim_t` would put a negative number on the page). The direction is published in `GET /v1/leaderboards` and `GET /v1/leaderboards/{stat}` so a client never has to infer it from the stat key, and `rank()`'s hidden-player arithmetic flips with it so a profile's rank cannot contradict the board page it appears on.

### PROJ-028 — Two smaller notes

*Accepted · 2026-08-07 · WP-CAREER.*

Two smaller notes. (1) `RestoreEvents` was silently dropping the new column — caught by `TestRestoreRoundTripsAndRebuildsIdenticalProjections`, which compared 33 original `player_stat` rows against 29 restored ones. It is the archive round-trip test earning its keep: the chunk codec and `InsertEvents` had both been updated and the restore path had not, and nothing else in the suite would have noticed. (2) `EventPipelineOptions.CareerId` is a **trailing optional** parameter defaulting to a stable per-install career, so every existing caller — `catlog.loadgen`, the conformance tests — keeps compiling and keeps meaning something sensible: a harness with no concept of a KSA save has exactly one career, forever.

### PROJ-029 — `sim_t` stays seconds on the wire; the derived board values become milliseconds. The rejected option is recorded here because "why isn't `sim_t` in ms?" is a question someone will ask again

*Accepted · 2026-08-07 · WP-CLOCK.*

The owner asked for game time "in milliseconds for our storage", and the literal reading — change `sim_t` to an int64 count of milliseconds — was rejected on one argument that settles it before any other is needed: **stored events are immutable (Constitution §5) and there is no envelope-level upcaster.** `projector/upcast.go` is keyed `(type, ver)` and rewrites *payloads*; the envelope's own fields have no migration path at all. So a unit change would leave every event already in the log holding seconds, with nothing in the system able to tell which unit a given row is in — retroactively lossy in a way no later fix could repair, which is exactly the failure mode §5 exists to prevent. The supporting arguments only pile on: `sim_t` in seconds is entangled with `Wire.TelemetryWindowSeconds`, `DetectorDebounceSeconds`, the ±2.0 s `KIANear` crew-survival window, `telemetry.window`'s `t0_sim`/`t1_sim`, `career.max_sim_t` and `player_body.first_sim_t`, all of which would have to move together; seconds is the game's native unit (`Universe.GetElapsedSeconds()`); and a float64 carries millisecond resolution comfortably for a career of any plausible length. **What was adopted instead:** express the *derived* values in milliseconds — `player_stat.value` is a REAL and every projection is rebuildable, so it is a fold change plus `Unit: "ms"` plus a rebuild. Reversible, and it touches no log. The general rule this is an instance of: when the question is "what unit should this be in", prefer changing the projection over changing the event, because one is a rebuild and the other is forever.

### PROJ-030 — catlogd now reads one clock, and a development build can move it

*Accepted · 2026-08-07 · WP-CLOCK.*

Every timestamp catlog treats as authoritative is server-generated — an event's `recv_time`, a license's `iat`/`exp`, a session's expiry, the `Date` header a client resynchronises against — and the client's `wall_t` is carried but never trusted (§4.1). That is the right design, and it has a consequence that only becomes visible once rolling daily/weekly/monthly/yearly aggregates are on the table: **the server's clock is the only thing that decides which day, week, month or year a leaderboard row belongs to**, so a rolling yearly board cannot be tested without waiting a year. `internal/clock` is now that one clock, threaded from `cmd/catlogd` into the store (`Options.Now` → `DB.nowMillis`, which is what stamps `recv_time` — previously a package-level `time.Now()` that no seam could reach), the ingest writer, the verifier (`SetClock`, which re-points the rate limiter with it), and the `Now` fields `adminapi`, `identity` and `archive` already had and nobody was filling in. Its doc comment on `authz.Verifier.SetClock` changed from "Tests only" to what is now true. **One clock is deliberately left on real wall time:** `identity/google.go`'s `id_token` expiry check, because that token was minted by another process on real time and lives for minutes — an offset clock would reject every perfectly good Google login. There is a comment at the site saying so, because it looks exactly like an oversight.

### PROJ-031 — Four independent things keep clock control out of production, and two of them are refusals rather than warnings

*Accepted · 2026-08-07 · WP-CLOCK.*

Four independent things keep clock control out of production, and two of them are refusals rather than warnings. `[server] clock_control` defaults to false; `Config.Validate` **refuses to start** when it is true alongside an `https://` base URL, on the grounds that a deployment reachable over TLS is not a laptop; `clock.Clock` itself returns `ErrNotControllable` unless it was built controllable, so even a caller that reached the handler gets nothing; and `catlogd` only calls `RegisterClock` when the flag is on, so on a normal server `POST /admin/clock` does not exist and answers 404 rather than 403. The route lives on the loopback-only admin mux like every other §5.9 route, is registered through its own `Register…` entry point per the established idiom, and logs a WARN at start-up and on every move — moving the clock is not a thing that should be discoverable only by reading a database afterwards. The offset is bounded to ±10 years so a typo fails loudly.

### PROJ-032 — The sharp edge of a movable clock is now four tests rather than a note

*Accepted · 2026-08-07 · WP-CLOCK.*

The sharp edge of a movable clock is now four tests rather than a note. §4.5.3 checks license expiry at **step 3** and proof skew at **step 8**. A server whose clock has jumped forward past a license's 180-day TTL therefore answers `license_expired`, **not** the `clock_skew` a reader would expect from "the clocks disagree" — and `clock_skew` is the only 401 the mod recovers from, so everything else latches the shipper dead for the session. The practical consequence for any harness simulating months or years: it does not get slow, it stops, unless it reissues licenses (180 days) and sessions (7 days) as it advances. `authz/clockjump_test.go` pins all of it — the ordering when both conditions are true at once, that a small jump inside the license lifetime is still recoverable `clock_skew`, that reissuing at the new clock restores service, and that the token bucket refills rather than wedging across a jump (it measures absolute deltas, so a forward jump clamps every bucket to full instead of stalling one).

### PROJ-033 — The celestial-body allow-list is gone; a body board exists because a body appeared in the data. This supersedes the `TimedBodies` entry above

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

That entry justified an eleven-name list read off `Content/Core/Astronomicals.xml` on the grounds that `vehicle.soi.to_body` is client text and a key built from client text would let anyone mint a leaderboard. The premise was right and the remedy was wrong. KSA's celestial systems are **hand-authored content that ships as data and that mods extend or replace**, `docs/events.md` has said from the beginning that `body` is "opaque to server", and a compiled-in list is guaranteed to be wrong for somebody — silently, because a player who reaches a body we never heard of simply gets no board and no error. `stats.TimedBodies` and `stats.RUDCauses` are deleted. `fastest_to_<body>` and `rud_<cause>` are now **families**: the fold builds the key from the value the event carried, and the same argument applies to causes — a destruction cause a future build adds gets its own board instead of vanishing into `rud_total`. The six §4.2 causes survive only as *fixture data* in `internal/seed`, which is what they always were.

### PROJ-034 — A family board is listed once at least two distinct players are on it, and that one clause is the whole of the mitigation (owner's call)

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

Removing the allow-list naively lets a modified client invent ten thousand place names and fill the public index with them. The answer is `[boards] min_players`, default **2**, applied to every family: `GET /v1/leaderboards` lists a data-driven board only when that many distinct players hold a value on it. Against CONSTITUTION §8's five-part test it passes cleanly — it models nothing about the game, it is one comparison in one place, it adds no table and no pipeline stage and no accumulated state (the count is the one the index query already computes), it cannot punish an honest player, and its only effect is on the contents of a list. It is also correct on its own merits: **a leaderboard with one entrant is not a leaderboard.** The count is free because `player_stat`'s primary key is `(player_id, stat)`, so `count(*) GROUP BY stat` *is* the distinct-player count — no `DISTINCT`, no second query. Default rather than opt-in on purpose: the protection is the behaviour out of the box.

### PROJ-035 — The threshold gates the index, not the projector, and not a board's own URL

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

Two consequences, both deliberate. (1) `toBodyFold` and `rudFold` write the per-player value for **every** body and every cause regardless of publication, so the threshold is a display rule that can be turned down to publish history already in the projection, never a decision to stop collecting. A body sitting at one player is published the moment a second player arrives — no rebuild, no migration. (2) A board with one entrant is still *served* at `/v1/leaderboards/{stat}` and still shows on that player's profile. Gating the page as well would give every such profile row a link to a 404 and would hide a player's own achievement from them until somebody else repeated it, while buying nothing: reaching it requires already knowing the exact key, which is not a way to fill anything. "Published" means "in the index", and the index is the surface the mitigation is for.

### PROJ-036 — Board metadata is derived from the key, so a board for a place nobody has ever typed here arrives fully described

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

Board metadata is derived from the key, so a board for a place nobody has ever typed here arrives fully described. `stats.Describe(stat)` is a pure function of the stat key: a fixed key looks up its constant row, a family key yields title, unit and direction from its own text. `titleize` splits on `_ -.` and capitalises — `luna` → "Fastest to Luna", `ground_impact` → "RUDs — Ground Impact", `kerbin_ii` → "Fastest to Kerbin Ii". **No table of pretty names**, deliberately: a lookup table of nice titles is a list of the bodies we happen to have heard of, which is the thing this change removed. The one cost is cosmetic and accepted — "Excessive G Force" rather than the hand-written "Excessive G-Force". `stats.Catalog(counts, minPlayers)` assembles the index: fixed boards always, family members sorted by key beneath the fixed board they belong with (`rud_*` under `rud_total`, `fastest_to_*` under `fastest_to_orbit`). Ordering by key rather than by, say, distance from the star is the point — system order is knowledge this layer no longer has.

### PROJ-037 — What a name must be to become a stat key is protocol hygiene, not an allow-list

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

What a name must be to become a stat key is protocol hygiene, not an allow-list. `statSuffix` lowercases (§4.2 says these values already are, so folding case can only merge two spellings of one name) and then requires `[a-z0-9]` followed by `[a-z0-9._-]`, at most 40 characters — because a stat key is a URL path segment and an entry in a public index. A name that fails keeps **every other consequence it had**: the visit still counts towards `soi_bodies`, the cause still counts towards `rud_total`, `player_body.first_sim_t` still records the arrival. Fixed keys are additionally reserved, so a body literally named `orbit` cannot land on `fastest_to_orbit` and merge two different questions into one row. This belongs in the "not anti-cheat" list of integrity-audit.md, next to the body-size caps.

### PROJ-038 — A rebuild reproduces the published board set for free, because publication is read-side

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

D22 makes the full rebuild the correctness backstop, and dynamically created buckets have to survive it identically. They do, without any new machinery: the projector writes `player_stat` rows and nothing else, and the board list is computed from those rows at request time by `stats.Catalog`, whose ordering is a pure function of the keys. Reproduce `player_stat` and the index is reproduced by construction — there is no separate registry of boards to drift, and no arrival-order dependence to get wrong. Verified live: an invented body's board survives `POST /admin/projections/rebuild` byte for byte.

### PROJ-039 — Neither frontend may assume a fixed board list, and `toHaveCount(30)` was the assertion that said it could

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

The datastar site already rendered `BoardList`, so it needed only a note explaining the threshold and a `data-ascending` marker. The React SPA's board index was already dynamic, but its home page pinned three board keys; that list is now a *preference* filtered against what the server publishes (`spa/src/ui/featured.ts`), so being wrong costs a different choice of preview rather than an error panel. `site/e2e/boards.spec.ts` no longer counts rows. It asserts three things instead: every board whose key is a compile-time constant is present **by name** (which is what catches a board that silently stopped being published), the rendered `data-stat` list equals `GET /v1/leaderboards` exactly (which catches the template dropping or inventing one), and the published set is strictly larger than the fixed set and contains a member of each family (so the comparison is not two copies of the same list). A count that every new body invalidates is a count that gets bumped without being read.

### PROJ-040 — The demo dataset gained a second entrant on every family board, because otherwise the demo would show none of them

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

The demo dataset gained a second entrant on every family board, because otherwise the demo would show none of them. `internal/seed`'s stated purpose is that the three demo players between them set a record on every launch board; under the publication threshold a family board with one entrant is invisible, so "every launch board" stopped being true the moment the allow-list went. `demo_tumbler` now flies one RUD per §4.2 cause and `demo_crasher` gains a second, slower career over the same stock-body chain `demo_ace` flies. `demo_ace` still owns every per-body board — its second career starts at `sim_t` 0 and `demo_crasher`'s at 500 — and no previously asserted seeded value moved.

### PROJ-041 — `GET /v1/leaderboards` publishes `min_players`

*Accepted · 2026-08-07 · WP-DYNBOARDS.*

Without it the index is inexplicable: a player who has been somewhere new and sees no board deserves to be told it needs a second visitor rather than to file a bug. Both frontends render the explanation from the server's number rather than a constant of their own, and the e2e asserts the page and the JSON agree about it.

### PROJ-042 — A period is a dimension of a board, not a board

*Accepted · 2026-08-07 · WP-PERIOD.*

A period is a dimension of a board, not a board. `GET /v1/leaderboards` stays one row per board and each row gains `periods: ["alltime","daily","weekly","monthly","yearly"]`; `?period=` on the board URL selects one, `?at=` names a window, and an absent `?period=` still means all-time so every existing URL, cache entry and assertion is byte-identical. The alternative — listing `rud_total@weekly` as its own entry — multiplies the index by five, and since `fastest_to_<body>` and `rud_<cause>` take their keys from the event stream that multiplier applies to a list with no upper bound. It would also have forced `stats.Describe` (which derives a board's title, unit and direction from its key alone), `stats.Catalog` (which groups families by key prefix) and the exact-match `/v1/leaderboards/{stat}` lookup to start parsing a compound key. The two frontends both render the index as a flat list, and `site/e2e/boards.spec.ts` asserts the rendered index equals the API's exactly — a shape that grows multiplicatively would have broken that on the day a player reached somewhere new.

### PROJ-043 — Buckets are derived from the event's `recv_time` and never from the wall clock, and that is load-bearing rather than tidy

*Accepted · 2026-08-07 · WP-PERIOD.*

The projector's rebuild replays history; a bucket taken from `time.Now()` would file a two-year-old event in this morning's window, so a rebuilt `projections.db` would disagree with the incrementally-built one and `TestRebuildEqualsIncrementalForAnUnflaggedHistory` would — correctly — fail. `stats.Event.RecvTime` was already in hand inside every fold, and the WP-CLOCK work is what makes it the server's own authoritative stamp rather than anything a client sent. `TestRebuildEqualsIncrementalForPeriods` pins the same guarantee for the new table. An event with no receive stamp writes no windows at all, which is the honest answer: a row whose window nobody can determine belongs in no window, and the all-time board still has it.

### PROJ-044 — The windows hang off the four write helpers, so they compose with the dynamic board families for free

*Accepted · 2026-08-07 · WP-PERIOD.*

Every board value catlog computes passes through `putBest`, `putRecord`, `addCount` or `setValue`, so that is where the period writes went. The consequence is the one the design needed: a `fastest_to_<body>` board that comes into existence the moment a second player reaches somewhere new gets its daily, weekly, monthly and yearly windows on the very same event, with no registry to update and nothing to enumerate — `TestDynamicBoardsGetTheirWindowsForFree` is that property. The helpers now take the `Event` rather than a `playerID`/`seq` pair, which is a smaller signature and puts the receive stamp where it is needed. Semantics per kind: records keep the largest value achieved *inside* the window, best/ascending boards the smallest, counters accumulate their deltas — a counter's weekly value is **not** recoverable from a running lifetime total, which is the whole reason the table exists rather than a query over `player_stat`. `setValue`'s derived totals (`distance_travelled`) contribute their *increase*, read from the previous row before the write, so "distance this month" is distance travelled in that month and not a lifetime figure wearing a monthly label.

### PROJ-045 — `[boards] min_players` stays a single global threshold evaluated on the all-time board, and a published board's windows are published with it

*Accepted · 2026-08-07 · WP-PERIOD.*

Two players all-time is genuinely not two players this week, so the alternative was real: gate each window separately. It was rejected because per-period membership makes the *index* vary by period, which reintroduces exactly the multiplicative growth the previous decision exists to avoid — the index would have to describe which windows currently clear the bar, and that answer changes every midnight. A board that vanished from the index on Mondays and returned on Fridays would also be indistinguishable from a bug. So the threshold answers "is this board a leaderboard at all", once, and `?period=weekly` on a published board may legitimately return few rows or none. An empty window is honest; a disappearing board is not. A rebuild reproduces the published set either way, because it is a function of `player_stat` row counts and those are unchanged.

### PROJ-046 — Retention is a `bucket < cutoff` string comparison, trimmed in-transaction, on a cadence gated by the event's sequence number

*Accepted · 2026-08-07 · WP-PERIOD.*

Every bucket format (`2026-08-07`, `2026-W32`, `2026-08`, `2026`) sorts chronologically as plain text, which is chosen so that ageing out a window needs no date arithmetic in SQL — tursogo has no recursive CTEs and the weekly key is an ISO week-numbering year that SQLite could not compute anyway. The trim runs inside the projector's transaction, like the feed cap, because there is no `VACUUM` to tidy up after a deletion that happened elsewhere. It fires every 512 events, gated on `ev.Seq % 512`, and that gate is deliberately the sequence number rather than a timer or a sampled draw: seq is the projector's cursor, so a rebuild trims at exactly the same points and rebuild-equals-incremental survives. Horizons are 90 daily, 53 weekly, 36 monthly and 20 yearly buckets — Constitution §2 has an opinion about a table whose row count is players × boards × buckets and whose bucket count otherwise grows forever. It is a listing horizon and not data loss: the log is untouched and a rebuild reconstructs whatever the current numbers say to keep.

### PROJ-047 — The career boards now publish milliseconds, as agreed

*Accepted · 2026-08-07 · WP-PERIOD.*

The career boards now publish milliseconds, as agreed. `Unit` is `"ms"` and the conversion is a single `careerMillis` at the point a career time becomes a board value. `sim_t` stays seconds on the wire and `player_body.first_sim_t` stays seconds in the store — the conversion deliberately does not happen inside `careerTime`, or that column's unit would have changed silently along with it. This is the projection-not-the-log rule from WP-CLOCK being applied: it is a fold change plus a rebuild, it touches no event, and it is reversible. `catlog.sim`'s `orbit-and-back` scenario asserts the new value (190 000 ms), and the seed's expectations moved with it.

### PROJ-048 — Four public read endpoints added for the two frontends' three journeys (own stats / global stats / compare with friends), all under the §4.8 cache discipline

*Accepted · 2026-08-07 · WP-READ.*

Four public read endpoints added for the two frontends' three journeys (own stats / global stats / compare with friends), all under the §4.8 cache discipline. `GET /v1/players?q=` (handle search), `GET /v1/players/{handle}/events` (raw event browsing), `GET /v1/compare?handles=a,b,c` (N-handle comparison), and two fields on the existing `GET /v1/players/{handle}` rows — `ascending` and `players` — so a profile can render "#3 of 41" and say which way the board reads without also fetching the board index. Shapes are in [ingest-api.md](ingest-api.md) §4.8. Nothing new in `Deps`, so `catlogd`'s wiring is unchanged; every route goes through `readapi.Server.Register`, which is the only thing that attaches the cross-origin headers (readapi/cors.go), and every response carries `Cache-Control: public, s-maxage=30, stale-while-revalidate=300` including the 400s and 404s.

### PROJ-049 — `install`, `kid` and `career` are the deanonymisation hazard the read API had to solve before it could publish raw events, and `career` was already leaking

*Accepted · 2026-08-07 · WP-READ.*

All three are derived from the mod's install id (docs/events.md): `install` *is* it, `kid` is `SHA-256("catlog-kitten:" + install_id + ":" + roster_name)`, and `career` is `SHA-256("catlog-career:" + install_id + ":" + save_key)`. One install is one *machine*, so it is one *person* rather than one account — and catlog deliberately lets one person hold two accounts with no way to tell from outside (§1). Publishing any of the three raw links those accounts to each other. **`career` was the live one**: the career-time folds copy the §4.1 envelope key into `player_stat.context`, and `/v1/players/{handle}` and `/v1/leaderboards/fastest_to_*` have been publishing it verbatim, so two accounts on one machine playing one save have been publicly correlatable. Resolution, in `readapi/privacy.go`: `install` (and `install_id`) is **dropped** — a per-player relabelling of it would group nothing while still looking like a token to interpret; `career` and `kid` are **relabelled per player** as `crockford32(SHA-256("catlog-public-label:" + kind + ":" + player_id + ":" + value))[0..16]`, the same 16-character shape as the value they replace, so they still group one player's rows ("these records came from one save") and no longer link two. The rules are keyed **by field name at any depth**, not by event type, because §4.1 preserves unknown payload keys — that covers `roster.snapshot.kittens[].kid` with no special case and a future event carrying `install` before anybody notices. Unkeyed rather than HMAC'd with the pepper: inverting a label needs the underlying value, and the only party who already has it is the person who owns the install; the pepper would close that last case and is not worth giving `readapi` a dependency on the key set for. Fast path: a blob mentioning none of the three names is passed through as the bytes the fold wrote, so an ordinary board row costs nothing.

### PROJ-050 — Everything else in the §4.2 taxonomy was checked for cross-handle correlation and deliberately published

*Accepted · 2026-08-07 · WP-READ.*

Everything else in the §4.2 taxonomy was checked for cross-handle correlation and deliberately published. `session`/`flight`/`id`/`other_flight` are per-occurrence ULIDs minted fresh by the client with no install in them; `sid` never leaves `stream_state`; `game_build`/`mod_ver` are shared by everybody. Two residuals are stated rather than hidden: (1) **kitten and vehicle names** are the same across a person's two accounts if they name things the same way — a soft correlator no redaction can remove without deleting the content the raw view exists to show, and the same exposure a player accepts by picking a recognisable handle; (2) **receive times** correlate anything shipped at the same moment, which the activity feed has published per handle since §5.6. One field is omitted for a smaller reason: the envelope's **`wall_t`** is the untrusted client clock, adds nothing next to the server's `recv`, and its offset from `recv` is a per-machine constant. `user_key` appears in no read-API response and never has; `readapi/redaction_test.go` pins all of it by naming what a regression would leak, in the same spirit as the CORS boundary test.

### PROJ-051 — The public raw-event view excludes flagged flights, and that is the only §8-compatible reading of the flags

*Accepted · 2026-08-07 · WP-READ.*

The public raw-event view excludes flagged flights, and that is the only §8-compatible reading of the flags. `GET /v1/players/{handle}/events` reads events.db directly, where nothing records that a flight was flagged, so without a filter it would publish every event of one — contradicting the promise on `/docs/privacy` that a flagged flight "scores nothing and never appears publicly", which the boards keep for free (the folds never write a row). It is also what Constitution §8's consequence test requires: a flag's only effect may be to exclude a flight from the boards, and it "never treats a player differently because of accumulated history" — a browsable public record of which flights were flagged is exactly a durable public consequence attached to a person, and the flags are `teleport`, `refuel`, `resource_edit`, `console`, `tuning`, so such a page would publicly label somebody who nudged a debug window. **Cost, since §5.4 forbids the join:** one primary-key lookup per *distinct flight on the page* against `flight_state` (`store.Projections.FlaggedFlights`, chunked at 200 like `Events.RecvTimes`) — no scan, no new projector state, and no measurable change to the page. The alternative of having the projector maintain an in-memory flagged set was not needed; revisit only if a page ever carries hundreds of distinct flights.

### PROJ-052 — The `/docs/privacy` promise about flagged flights is now half true, knowingly

*Accepted · 2026-08-07 · WP-READ.*

It says they are "stored and **shown to you**, but score nothing and never appear publicly". The second half now holds everywhere. The first does not: there is no session-authed view in which a player sees their own flagged flights — the dashboard shows handles, credentials and quota, and the public event endpoint cannot show them because it takes no credentials at all (readapi/cors.go: these routes never emit `Access-Control-Allow-Credentials` and never see a cookie). Two ways to close it, for whoever owns it next: **(a)** build the own-data view — a session-authed `GET /api/me/flights` or similar, rendered on the dashboard, showing the player their own flagged flights and why; or **(b)** soften that half of the sentence to match what catlog does. (a) is the better promise and the more work. The privacy copy is deliberately **not** edited here: the two frontend work packages own that text, and a promise is not something to change in passing.

### PROJ-053 — Handle search is a scan of the in-memory directory, not a query

*Accepted · 2026-08-07 · WP-READ.*

Handle search is a scan of the in-memory directory, not a query. `directory.Directory.Search` walks the same `handle → player` map every board page already resolves through, so it needs no ban filter (a banned player is absent from it by construction), no index SQL cannot provide anyway, and no round trip. Prefix matches sort before substring matches, each group by the lowercase handle, and the **whole** match set is collected before truncating — stopping early would hand back whichever matches Go's randomised map iteration reached first, and a CDN would then cache one arbitrary subset per query. Bounds: `q` shorter than 2 characters or longer than §4.7's 150-character handle cap is `400 bad_request` (cacheable, so a client hammering a malformed query is answered by the CDN); `limit` defaults to 20, clamps to 50; `truncated` is reported and there is no offset, because a paged search over a live directory is a promise this cannot keep.

### PROJ-054 — A comparison is N profiles pivoted, capped at 8

*Accepted · 2026-08-07 · WP-READ.*

A comparison is N profiles pivoted, capped at 8. `GET /v1/compare` calls exactly the code behind `GET /v1/players/{handle}` for each handle and pivots the result board-first, so the rank arithmetic that discounts banned players and the redaction of install-derived keys exist once — a second implementation of either would eventually drift, and the drift would be a wrong rank or a leak. The `player_stat` census is read once per request rather than once per handle. The cap is 8 because a side-by-side table stops being readable well before that and because a comparison's cost is N × (one rank query per board its player is on); over the cap the extras are **dropped** rather than 400'd, and the echoed `handles` array is the authoritative column order. An unknown, retired or banned handle comes back `{"handle": s, "found": false}` — one answer for all three, revealing exactly what `GET /v1/players/{handle}` already reveals by 404ing all three, and failing the whole comparison because one of five friends deleted their account would be worse for everyone and no better for them. A board is included when at least one compared handle is on it and carries only the rows that exist: **absent is absent, not zero**, the same rule the folds follow for a missing `peak_g`. `min_players` does not apply, for the same reason it does not apply to a profile.

### PROJ-055 — Event paging is by cursor, and the cursor is opaque

*Accepted · 2026-08-07 · WP-READ.*

Event paging is by cursor, and the cursor is opaque. `next` → `?before=` over `ev_player (player_id, seq)`, newest first, because the log grows at the head and an offset would drift under a reader whenever the player shipped between two requests. The `?type=` filter runs in Go over pages rather than in SQL — there is no index on `type`, so a SQL `type = ?` would let one request walk a whole history looking for something that is not there — with a 5 000-row scan bound, which means **a short page carrying a cursor is not the end of the log** and a client must page until `next` is absent. Same over-fetch-and-drop shape `visibleRows` already uses for bans, and the flagged-flight exclusion rides in the same loop.

### PROJ-056 — `server/internal/units` is the single definition of what a catlog number looks like, and the SPA must reproduce it

*Accepted · 2026-08-07 · WP-READ.*

The API publishes raw numbers in the unit the event carried, so formatting happens once per frontend and the two must agree character for character or the same record reads differently depending on which site you opened. The rules: non-finite → `—`; **three significant figures** (`decimals = clamp(2 - floor(log10 |x|), 0, 6)`) with trailing zeros trimmed and thousands grouped by U+202F; rounding defined on the magnitude as `round(|x| · 10^d) / 10^d` with halves up and the sign re-applied, **specifically** so Go's `math.Round` and JavaScript's `Math.round` cannot disagree at a tie the way `strconv.FormatFloat` and `Number.toFixed` do; `m`/`J`/`Pa` scale by SI prefix (1, k, M, G, T; nothing below the base unit); **`m/s` never scales**, because every speed board is in m/s, this audience reads 7 799 m/s directly, and a per-value scale would mix units inside one leaderboard column; `s`/`ms` become two-component durations (`450 ms`, `37.5 s`, `5m 13s`, `1h 01m`, `243d 01h`, `1y 5d`, a year being a flat 365 days). `units.ForKey` maps a payload key to its unit and exists mainly to hold one trap: **`_ms` is metres per second in every §4.2 payload while the board unit string `"ms"` is milliseconds.** `units.Conformance` is the shared table, asserted by `units_test.go`; **the TypeScript implementation needs its own copy of that list, and neither may change a rule without changing the other.** `web/templates.go`'s `value` and `unit` funcs now delegate here rather than carrying a second implementation.

### PROJ-057 — The projector was slow because it issued about twenty-one SQL statements per event, and for no other reason — measured before anything was changed

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

The projector was slow because it issued about twenty-one SQL statements per event, and for no other reason — measured before anything was changed. `BenchmarkDrain` (`server/internal/projector/bench_test.go`) folds a 100,050-event synthetic log shaped like a load-harness run: 25 players, telemetry windows dominant. It took **38.5 s**, which is the 2,600 events/s WP-LOADGEN-FAST measured against a live server, reproduced in a unit test that runs in a minute. The CPU profile is a red herring and worth recording as one: 78% of samples are `runtime.pthread_cond_signal` under `newproc`, which reads like a scheduler pathology, and `GOMAXPROCS=1` moves the wall clock by 8%. The number that actually explains it came from micro-benchmarking the driver: **one tursogo statement costs 14–18 µs** end to end (`~10 µs of it inside the engine, via cgo`), against ~5 µs per row when the same rows arrive in one multi-row `VALUES` list. 21 × 18 µs = 385 µs, and 385 µs × 100,000 = 38.5 s. Everything else — decoding, the folds themselves, the scheduler noise — was rounding error against that.

### PROJ-058 — Where the twenty-one went, since the shape is the whole argument for the fix

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

For one `telemetry.window`, the type that dominates a real log: 1 `INSERT OR IGNORE` into `flight_state`, 1 upsert into `career`, then four board folds each asking `flight_state` whether the flight was flagged (**4 identical SELECTs**, one per fold, plus one more from the feed renderer), and three board values each fanning out into `player_stat` **and its four rolling windows** (**15 writes**). Three kinds of waste, in rising order of size: reads repeated within one event, writes to a key that a later event in the same batch will overwrite anyway, and one statement per row where the row count per key was never the point.

### PROJ-059 — The fix is `stats.Batch`: a read-through cache and write-back accumulator that a batch folds into, flushed as a handful of multi-row statements

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

The fix is `stats.Batch`: a read-through cache and write-back accumulator that a batch folds into, flushed as a handful of multi-row statements. `Fold.Apply` takes a `*Batch` instead of a `*sql.Tx` and a separate `FlightStateReader` — the Batch is the reader too, so a fold has one thing to talk to. `flight_state`, `career`, `player_body` and `kitten` become caches loaded on first touch and written back when dirty; `player_stat` and `player_stat_period` become four and three pending maps, one per merge rule, flushed as one statement per rule. **Every merge rule is the in-memory spelling of the `ON CONFLICT` guard it replaces, tie-breaks included**: a record board merges on strictly `>` because `WHERE excluded.value > player_stat.value` did, which is what keeps "whoever got there first keeps the rank" true. `setValue` merges only on a *changed* value, because its guard was `WHERE excluded.value <> player_stat.value` and merging unconditionally would have handed `updated_seq` to a later event that recomputed the same total. Result: **38.5 s → 3.46 s, an 11× improvement**, 2,600 → 28,900 events/s. A million events folds in ~35 s rather than ~385 s, which changes WP-LOADGEN-FAST's "36 s to store and five minutes to fold" into 36 s and 35 s.

### PROJ-060 — The projection cannot depend on where the batch boundaries fell, and `TestBatchSizeDoesNotChangeTheProjection` is the test that says so

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

It folds the same history at `BatchSize` 1, 2, 3, 17 and 1000 and diffs every projection table. At one event per batch nothing merges and every write settles in SQL exactly as it did before `Batch` existed; at a thousand, a player's whole history collapses in memory first. This is the test that catches the failure mode the golden-value tests cannot: a record board that merged on `>=` still holds the right *value* and quietly hands the rank to the later claimant — verified by making that exact change and watching two sub-tests fail. It is also why `player_stat_period` was added to the projector test's `snapshot()`, which had never covered the rolling windows at all, and why the test rig now stamps `recv_time` from a **fixed** clock: `recv_time` is an input to the projection (it is what the windows bucket by), so a wall clock made two rigs folding one history produce two different tables.

### PROJ-061 — The retention trim flushes the buffered window writes before it deletes, and runs once per seq rather than once per board write

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

The retention trim flushes the buffered window writes before it deletes, and runs once per seq rather than once per board write. `trimPeriods` fires on `seq % 512` from inside `eachPeriod`, so an event that wrote three boards ran the same four `DELETE`s three times; collapsing that is free. The flush before it is not cosmetic: a window write the batch is still holding is a row the one-statement-at-a-time path would already have written, and therefore a row the delete could already have reached. It takes a batch spanning more than the retention horizon for that to matter — which is to say a rebuild replaying a history with quiet stretches in it, the one pass whose answer has to match the incremental one.

### PROJ-062 — Parallelism is not the answer here, and the measurement says so plainly

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

Turso has one writer, and the ~5 µs per row that survives batching is real work inside the engine rather than overhead a second goroutine could overlap with. The one genuinely parallel stage is decoding payloads — pure `encoding/json` over bytes the database already handed us, touching no shared state — so it fans out across cores (`projector/decode.go`, `DefaultDecoders` = cores − 1, inline below 64 events, skip logging still serial so "logs once" still names the first event by seq). **It is worth about 1%**: `decoders` 1, 4 and 13 fold the benchmark in 3.66 / 3.61 / 3.69 s. It is kept because it is correct, free, and scales with payload size in a way §4.2's payloads do not yet exercise — not because it is load-bearing. Anyone reaching for goroutines to make this faster should read this line first.

### PROJ-063 — `batch_size` is the projector's memory knob and is left at §5.6's 1000 on purpose

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

Bigger batches merge more: 1000 → 2000 → 5000 → 10000 folds the benchmark in 3.60 / 3.23 / 2.88 / 2.77 s, so ten times the batch buys 20% — and holds ten times as many decoded payloads while it does. Peak live heap for a 100,050-event drain at the default is **4.9 MB**, which is what makes catlogd comfortable on a 1 GB VM; the new `[projector]` config section says so in its comment, and raising it is the supported way to trade that back for speed on a machine with memory to spare.

### PROJ-064 — Two allocation fixes on the read path, and one non-fix worth recording

*Accepted · 2026-08-07 · WP-PROJ-FAST.*

Two allocation fixes on the read path, and one non-fix worth recording. `scanStoredEvents` scanned each payload into a `string` and then converted it to a `json.RawMessage` — **two copies of every event payload in the log**, on the one function every fold and every rebuild pass runs over every row; it scans into a `[]byte` now, which is the one copy `database/sql` requires anyway, and pre-sizes its result to the caller's `LIMIT` instead of growing a slice of ~150-byte structs from nil. `Bucket` was recomputing four `time.Format` strings per board write per event; a single-entry memo on the receive time removes ~1.2 M string allocations per 100k events. Together: 13.5 → 12.7 kB allocated per event. **The non-fix**: `stats.Event.Raw` looks like it holds a second copy of the payload and does not — it is a slice header aliasing the same array `store.StoredEvent.Payload` already points at, and the batch's `evs` slice keeps that array alive regardless, so dropping the field would save 24 bytes per event and free nothing. The remaining ~392 allocations per event are almost entirely inside tursogo and purego's reflect-based FFI trampoline (`reflect.unsafe_New` alone is 34% of allocated objects), which is the driver's to fix and not ours.

### PROJ-065 — Event inserts are batched multi-row statements, because per-statement overhead was the ingest write path

*Accepted · 2026-08-08 · WP-CHEAP.*

Event inserts are batched multi-row statements, because per-statement overhead was the ingest write path. `store.Events.InsertEvents` now emits `INSERT OR IGNORE … VALUES (…),(…)` in chunks of 500, the same pattern `InsertFeedRows` and `stats.Batch.write` already proved. Per-row `RowsAffected` was only ever consumed as the two aggregates the §4.4 response carries, and SQLite treats an intra-statement duplicate exactly like a stored one, so `accepted`/`deduped` cannot drift — verified against tursogo before relying on it, pinned by `TestInsertEventsDedupsWithinOneBatch` / `TestInsertEventsAcrossChunks`. Measured: a 500-event batch inserts in 5.6 ms, down from 11.5 ms (~2×), and the win is per-statement overhead so it holds on file-backed stores.

### PROJ-066 — Ingest memory is bounded by an in-flight gate, not by hope

*Accepted · 2026-08-08 · WP-CHEAP.*

New `[ingest] max_inflight` (default 4×GOMAXPROCS): a non-blocking semaphore taken after authz and before the body is read, answering the existing 503 + `Retry-After: 5` when full. The §4.3 caps bound one request at ~9 MiB (1 MiB body + 8 MiB decompressed); this bounds how many such requests exist at once, which is what actually sizes peak RSS on a 1 GB box. `QueueDepth` 256 → 64 (every queued job's handler is blocked in `Await` behind the gate, so the queue cannot legitimately exceed in-flight count). The NDJSON decode no longer copies the batch — line scan over the raw `[]byte`, one `json.Decoder` per batch using `InputOffset()` against each line's range, all framing rules and error wording unchanged (the whole existing rejection matrix passes unmodified) — and brotli readers are pooled.

### PROJ-067 — The read path stops recounting the world: the board census and profile ranks were the per-page cost

*Accepted · 2026-08-08 · WP-CHEAP.*

The read path stops recounting the world: the board census and profile ranks were the per-page cost. `statCounts` (a full `player_stat` group-by, previously run on every BoardList/Player/Compare) is cached ~10 s **keyed by `events.MaxSeq`** — new events are the only thing that (after a fold) changes the census, so the head moving invalidates immediately and an idle server serves it for free (**the key was wrong; superseded by WP-STALE-CENSUS below**); the mutex is held across the recount so concurrent misses wait for one query. Profile ranks collapsed from one `StatAhead` per board (× 8 handles on `/v1/compare`) to two statements per profile via `StatAheadForPlayer`, a correlated count whose tie rule is byte-for-byte `StatAhead`'s, pinned by an equivalence test. `visibleRows` sizes its first read to the request (a 3-row featured board reads ≤28 rows, not 256). **Deliberately skipped:** adding `max-age` to `readapi.CacheControl` — the exact header string is contract in docs/ingest-api.md; prod nginx gained a commented `proxy_cache` micro-cache example instead, same effect without touching the wire.

### PROJ-068 — Streams and hot loops stop repeating identical work per consumer

*Accepted · 2026-08-08 · WP-CHEAP.*

Streams and hot loops stop repeating identical work per consumer. `/v1/feed/stream` marshals and SSE-frames each row once per broadcast through a `feedHub` (one upstream subscription, shared bytes to every subscriber); the web feed renders its HTML fragment once per row the same way. Authz step 5's credential read is memoized per jkt, invalidated by the deny-list version counter (audited: every ban/unban/purge/revoke path mutates the deny-list) plus a 60 s TTL for out-of-band DB edits; only successes are cached. `/admin/stats`' counting half is cached 30 s keyed by `MaxSeq` — both C# consumers were checked and stay exact because shipping events moves the key (**that audit missed the consumer that reads the count after a *purge*; superseded by WP-STALE-CENSUS below**). The projector's idle tick went 1 s → 5 s (`[projector] tick_s`; the ingest notify channel covers real arrivals) and a zero-read step skips the lag query because lag is provably 0.

### PROJ-069 — `stat_rank` stays narrow, and the plan says why

*Accepted · 2026-08-08 · WP-CHEAP.*

EXPLAIN under tursogo 0.7.2 shows `USE SORTER FOR ORDER BY` identically for the current, widened, and DESC-annotated indexes — this planner does not satisfy ORDER BY from index order beyond the equality prefix, and the sorter only sorts one board's rows. A wider index would buy write amplification and nothing else; re-check when the planner grows ORDER BY-from-index. Deployment knobs shipped commented: `GOMEMLIMIT=700MiB` + `GOGC=300` and `MemoryHigh`/`MemoryMax` in the systemd unit (ordered so the runtime reacts before systemd reclaims before the OOM kill), and `[archive] max_events_per_run` became a TOML knob.

### PROJ-070 — `GET /v1/events` is the whole log, newest first, and `?handle=` is a filter that delegates rather than a second implementation

*Accepted · 2026-08-08 · WP-RAW-EVENTS.*

`GET /v1/events` is the whole log, newest first, and `?handle=` is a filter that delegates rather than a second implementation. `EventRow` gained `seq` (the cursor value was already the seq; now it is honest) and `handle,omitempty` — the per-handle endpoint omits the per-row handle (its envelope names it once), the global view always fills it. The global scan reuses the per-player over-fetch-and-drop loop generalized with a keep-predicate: rows whose player holds no handle in the Directory (banned/purged/unclaimed) and flagged-flight rows are dropped, bounded by `maxEventScan`, so a short page WITH a cursor stays normal and "page until `next` is absent" carries over verbatim. `?handle=` resolves through `Directory.Lookup` and 404s with the same one-answer-for-three shape as the profile endpoint — anything else would have been the ban oracle `/v1/players/{handle}/events` refuses to be.

### PROJ-071 — The live stream publishes what the log stores, not what the projector understood

*Accepted · 2026-08-08 · WP-RAW-EVENTS.*

A `projector.RawBroadcaster` publishes each batch from `Step`'s post-commit path before and regardless of per-event decode — an event the fold skips (`logSkipOnce`) still reaches the stream, so the paginated log and the live stream cannot diverge, pinned by a test where an undecodable event streams while `Skipped=1`. Rebuild mirrors the feed exactly: `Rebuild` publishes nothing (it writes a scratch DB checkpointed at head), so a rebuild never re-streams the log. Redaction happens once at publish in readapi's `eventsHub` (salt = the row's own player_id — never the reader's, never one salt per page), handle-less and flagged rows drop at publish (fail closed on flag-read errors), each row is marshalled and framed once and fanned out as shared bytes; per-subscriber `?type=`/`?handle=` are two string compares before the write. Stream subscribers are capped (`[server] max_stream_clients`, default 64, both hubs) answering `429 rate_limited` — 503 means "server busy", this means "you have enough streams".

### PROJ-072 — One redaction seam serves both transports

*Accepted · 2026-08-08 · WP-RAW-EVENTS.*

The web (datastar) stream and the JSON stream render through the same exported `readapi.PublicEvents` — the eventsHub's drop/redact step — so the two surfaces cannot disagree about what is public. The site's `/events` page and the per-handle page render rows through one shared `event-row` partial (SSR, SSE prime, and live patches alike), asserted byte-identical by test. The datastar live tail pauses by *closing* the stream: `@get(url, {requestCancellation:'cleanup'})` registers the abort with datastar's per-attribute cleanup, so removing `data-init` aborts and restoring it re-primes — verified against the vendored v1.0.2 bundle; the default `'auto'` does not abort on cleanup, which is why the option is explicit. Paused means closed; resumed means re-primed; no buffering pretence.

### PROJ-073 — The SPA's stream pages choose uniform rows and honest URLs

*Accepted · 2026-08-08 · WP-RAW-EVENTS.*

The global `/events` page virtualizes with RAC `Virtualizer` + `TableLayout` (imported only in the lazy chunk) and uses master-detail rather than expandable rows — an in-row disclosure re-measuring under the scroller fights scroll anchoring and at-top detection, and at-top is what drives the tail pause. The payload summary is an allow-list per event type rendering only known scalar keys through the unit formatter; unknown types and keys render as a field count, never values — the CONTEXT_KEYS posture, so a new fold key cannot leak into a public table by omission. `type` and `handle` live in the URL on both event pages (back undoes a filter); the live-tail toggle deliberately does not — it is a function of view state, and a `?tail=` in a shared link would impose the sharer's transient toggle on the reader. Both pagers cap accumulation at 1000 rows (trimming the newest end, visibly reported) and the tail buffers at 500; a paused tail buffers behind an "N new events" button. The subscriber-cap 429 (invisible to EventSource) surfaces as `unavailable` with a 5 s self-retry, never an error page.

### PROJ-074 — `GET /admin/stats` under-reported `events.total` for 30 s after any purge, and that is what failed catlog.loadgen's zero-loss invariant

*Accepted · 2026-08-08 · WP-STALE-CENSUS.*

WP-CHEAP keyed the census cache on `events.max_seq`, reasoning that only new events change the count. Only new events *increase* it: `PurgePlayer` (§4.7 — a ban with purge, and every delete-my-data) removes a player's rows **without moving the head**, so the cache key did not change and the pre-purge count was served for the whole TTL. The load harness reads `events.total` as the baseline for its most important check, and its own moderation phase purges accounts at the end of every run — so the *second* run against a database was reported as having silently lost exactly as many events as the first run's purges had removed. Reproduced at 1,058,811 events (`expected 1058811, got 1054660`, short by the 4,151 rows two delete-my-data accounts had taken with them) and confirmed directly: purge 4,477 events, `total` unchanged, correct 32 s later.

### PROJ-075 — The fix is to count write transactions, because whatever changes a census had to commit one

*Accepted · 2026-08-08 · WP-STALE-CENSUS.*

New `store.DB.WriteGen()` — a monotonic counter incremented on every committed `WithWriteTx`, and on every autocommit statement through the new `DB.autocommit()` [Querier] that all sixteen nil-`Querier` fallbacks now use instead of the bare `Writer()` handle. `/admin/stats` keys its census on (events gen, projections gen); the TTL stays as a backstop for what changes without a write of ours, not as the correctness argument. A sequence number can only ever answer "has anything been appended"; a commit counter answers "has anything changed", which is the question a cache actually has.

### PROJ-076 — The read path's board census had the same defect in the other direction, and it is in the load-test path too

*Accepted · 2026-08-08 · WP-STALE-CENSUS.*

The read path's board census had the same defect in the other direction, and it is in the load-test path too. `readapi.statCounts` counts `player_stat` in projections.db but was keyed on `events.MaxSeq`. Those come apart exactly when ingest stops: the head freezes while the fold is still running, so a read landing in that window cached a half-folded census and served it for the full 10 s — `GET /v1/leaderboards` under-reporting its boards right after a load run, which is when the harness re-discovers boards to check that every player is visible on one. It is now keyed on `projector.Live.WriteGen()`, which the fold moves and nothing else does. `Live` carries a `base` across the rebuild swap so the counter cannot restart on a fresh handle and let a rebuilt database read as one already counted.

### PROJ-077 — Two tests were pinning the bug and now pin the contract

*Accepted · 2026-08-08 · WP-STALE-CENSUS.*

Two tests were pinning the bug and now pin the contract. `TestAdminStatsCensusIsCached` asserted that a ban "may be served stale — but only within the TTL"; it now asserts the ban and a purge both show immediately, and proves the cache is still a cache by poking a value no query could produce into it and reading it back. `TestStatCountsAreCached` asserted that a new event invalidates the board census; it now asserts the opposite — an unfolded event changes no board — and that the fold is what invalidates, checking the recounted number rather than only that a recount happened. The readapi fixtures write projections through `WithWriteTx` like the projector does, rather than around it via `Writer()`. Verified end to end: six consecutive `make loadgen PLAYERS=550 … ASSERT=1` runs against one database grown to 5.3 M events, all 14 invariants green, where the second run had failed before.

### PROJ-078 — The U+202F group separator is gone; grouping is `Intl.NumberFormat` in the reader's locale, on both frontends

*Accepted · 2026-08-08 · WP-CENSUS.*

The U+202F group separator is gone; grouping is `Intl.NumberFormat` in the reader's locale, on both frontends. `units` used a narrow no-break space between thousands, chosen because it does not wrap or widen a column. It is also, for practically every reader, not a thousands separator: en-* writes `1,234,567`, de-* writes `1.234.567`, de-CH writes an apostrophe, and the one locale U+202F is actually right for is fr-FR — which is to say catlog was showing several hundred locales one locale's answer. The SPA now calls `Intl.NumberFormat` directly (`formatValue(value, unit, locale?)`, defaulting to the browser's). **A separator swap would not have been enough**, which is why this is a re-render rather than a `replaceAll`: en-IN groups `12,34,567` and es-ES leaves `1234` alone, so the *group sizes* differ, not just the character between them. `useGrouping` is left at Intl's `auto` for exactly that reason, and `minimumFractionDigits: 0` preserves rule 2's trailing-zero trim.

### PROJ-079 — The server-rendered site cannot know a locale, so it renders a canonical one and the browser finishes the job

*Accepted · 2026-08-08 · WP-CENSUS.*

Every public page ships `Cache-Control: public, s-maxage=30` to a shared cache: there is no locale available at render time, in the same sense and for the same reason there is no handle available to personalise with (see `me.js`). `Vary: Accept-Language` was rejected — it is a high-cardinality header and would shred the CDN cache the whole §4.8 design exists to fill — and so was pulling in `golang.org/x/text`, which would only have moved the same wrong guess server-side. Instead `units.Format` renders `GroupSeparator = ","` / `DecimalSeparator = "."` (en-US, a conventional pair rather than a sentinel nobody writes, so a reader with JavaScript off gets something normal), new `units.Split` publishes the **scaled** number and its post-trim precision as `Parts`, the `num`/`numUnit` template functions emit `<span class="n" data-n="1.82" data-d="2">1.82</span> Mm`, and `site/assets/js/intl.js` re-renders those through `Intl.NumberFormat`. Attributes rather than re-parsing the text, because the text is not always a number — `1.82 Mm` is a number and a suffix, `243d 01h` is neither, and `Split` has already made that distinction so the browser does not have to reimplement `units`. A `MutationObserver` covers the feed lines, event rows and search suggestions datastar patches in over SSE; hooking datastar's own events would tie this to a vendored bundle's API for no benefit. The plain `value`/`number` functions stay for the attribute positions (`title`, `aria-label`) where a span cannot go.

### PROJ-080 — The cross-language conformance table is now pinned to a locale rather than to a separator

*Accepted · 2026-08-08 · WP-CENSUS.*

The cross-language conformance table is now pinned to a locale rather than to a separator. `units.Conformance` and its TypeScript transcription assert the canonical (en-US) rendering, which is exactly what `units.Format` produces and what `formatValue(…, CANONICAL_LOCALE)` produces — so "the two frontends agree character for character" is still a statement somebody can check, now qualified by "for the same locale". Two new Go tests cover the seam the browser depends on: `Split` reassembles to `Format` exactly, and its `Number` at its `Decimals` round-trips to its own text, so a reader with a script and a reader without one can never see different digits.

### PROJ-081 — `event_census` is a projection, which is what makes a public `/v1/stats` affordable

*Accepted · 2026-08-08 · WP-CENSUS.*

The front page's tiles carried a comment saying the honest summary stops at what `GET /v1/leaderboards` already computes, "until somebody decides a public `/v1/stats` is worth its unbounded half". This is that decision, and the unbounded half is gone: one row per `(type, period, bucket)` where the empty type is the total, folded O(1) per event, so "how many events, by type, by window" is a handful of indexed reads instead of a scan of a 657 MB table with a date function per row. The total is a **stored row rather than a sum of the types**, so it stays right for a type this build cannot name. Unlike `player_stat_period` there is no retention: that table is players × boards × buckets and grows in three dimensions, this is types × buckets — under ten thousand rows a year — and "the busiest day catlog has ever had" is not a question a 90-day horizon can answer.

### PROJ-082 — The census fold obeys none of the board rules, and that is deliberate

*Accepted · 2026-08-08 · WP-CENSUS.*

No flag exclusion, no handle requirement, no tie-break: a flagged flight's telemetry is still telemetry catlog is storing, and hiding it here would make the census disagree with the log it is a census of. It therefore lives in a new `stats.LogFolds()` rather than in `BoardFolds()`, and both the incremental loop and the rebuild's second pass now take `stats.SecondPassFolds()` — one list instead of two that have to be kept level, since a fold that ran in one and not the other is precisely how a rebuilt projections.db stops matching the incremental one.

### PROJ-083 — `GET /v1/stats` is memoised like the board census, on `WriteGen` plus a 10 s TTL

*Accepted · 2026-08-08 · WP-CENSUS.*

The assembly is about twenty small queries, which is more than a public page should pay per request; the whole response is cached behind the same key WP-STALE-CENSUS established, so it invalidates on a commit rather than on a proxy for one. The reads all happen inside one `Projections.With`, so the page describes one view of the database rather than a handful taken either side of a rebuild swap. `log_head`, `projected` and `lag` are published because everything else on the page is a projection, and a figure here disagreeing with a figure on a board page should be diagnosable as a cursor rather than a bug.

### PROJ-084 — Both frontends ship the same page at parity: `/stats`, in the navigation, on both

*Accepted · 2026-08-08 · WP-CENSUS.*

Headline tiles, the four rolling windows each broken down by type, a 90-day daily series, every event type with its share, and the collection census. Deliberately not a leaderboard — no records, no ranking, nobody's handle — and the SPA's copy is a lazy chunk for the same reason Compare and Events are: it is reached on purpose rather than landed on, and the front page should not carry it. The daily series draws only days that carried an event, because a day catlog was switched off is not a zero anybody measured.

---

## Archive & restore

The filesystem archiver, the manifest, restore verification, and the R2 design that is deliberately not built.

### ARCH-001 — Dependency added: `github.com/klauspost/compress` v1.19.2

*Accepted · 2026-08-07 · WP10.*

Dependency added: `github.com/klauspost/compress` v1.19.2 (§5.1's `zstd` for archive chunks). It was already present as an indirect dependency at v1.18.5 (pulled by `datastar-go`); WP10 promotes it to direct and pins the verified version. **No AWS/S3/cloud SDK was added, and none will be until R2 stops being design-only (D8).**

### ARCH-002 — The chunk line is the §4.1 wire envelope plus `_seq` *and* `_recv`

*Accepted · 2026-08-07 · WP10.*

The chunk line is the §4.1 wire envelope plus `_seq` *and* `_recv`. §5.10 says "envelope + `recv_time` line format identical to wire NDJSON plus `"_seq"` field", which names two server-local values but only spells one of them. Both are carried, both underscore-prefixed: they are *not* envelope fields (a mod handed this line would reject them as unknown envelope keys, §4.1), and the prefix keeps the format honest about which half is the wire contract. `_recv` is load-bearing rather than decorative — without it a restored event gets a new `recv_time` and the restored log is no longer the log that was archived.

### ARCH-003 — A restore preserves `seq` and `player_id`

*Accepted · 2026-08-07 · WP10.*

Both are server-local rowids that nothing outside the process sees, so re-minting them would look harmless — except `player_stat` is keyed by `player_id` and carries `updated_seq`, and the DR promise is that a rebuild over a restored log produces *the same* projections rather than merely equivalent ones. `store.RestoreEvents` therefore inserts at an explicit `seq`, and `store.RestorePlayer` at an explicit `player_id`; the manifest carries `player_id`, `idp` and `created_at` because they are the three `player` columns a chunk cannot supply. Both refuse a conflict (`ErrSeqConflict`, `ErrPlayerConflict`) rather than merging two histories — `INSERT OR IGNORE` alone cannot tell "already here" from "that rowid is someone else's", and silently dropping an event during a recovery is the one failure mode this path must not have.

### ARCH-004 — `archive.Getter` is a fourth method, kept out of `Store` on purpose

*Accepted · 2026-08-07 · WP10.*

`archive.Getter` is a fourth method, kept out of `Store` on purpose. §5.10's interface is `Put`/`List`/`Delete` and stays exactly that. But a manifest is a document the archiver appends to, and a restore has to read chunks back, so reads had to exist somewhere: `Getter` is a separate optional interface that `fsStore` implements and an S3-compatible store would too (`GetObject` is no less fundamental than `PutObject`). A store that does not implement it can still be archived to; it says so plainly when asked to restore. Recorded because it is an addition to a normative interface, small as it is.

### ARCH-005 — Determinism is bought with two encoder settings

*Accepted · 2026-08-07 · WP10.*

Determinism is bought with two encoder settings. `zstd.SpeedDefault` + `WithEncoderConcurrency(1)`: a concurrent encoder splits the input into blocks by goroutine scheduling, and those block boundaries land in the output, so the same events would compress to different bytes on a busy machine. Player order within a run is by `player_id` rather than map iteration, for the same reason. The determinism test rewinds the cursor and re-runs rather than rebuilding the log, because rebuilding it would change `recv_time` — that test would then be asserting that two different logs compress alike, which is not the property anyone wants.

### ARCH-006 — The cursor moves last, and a retry is idempotent by construction

*Accepted · 2026-08-07 · WP10.*

A run writes every chunk and manifest, then advances `archive_cursor`. A crash in between leaves the cursor where it was, so the next run re-reads the same events and writes *the same keys with the same contents* — which is why the chunk key encodes its seq range rather than a timestamp, and why `Manifest.addChunk` replaces a same-key entry instead of appending a duplicate.

### ARCH-007 — A restore advances the archive cursor to the restored head

*Accepted · 2026-08-07 · WP10.*

The log it just replayed came out of the archive, so it is by definition already archived; leaving the cursor at zero would make the next `catlogctl archive` copy the whole history again under different chunk boundaries, leaving both sets in the manifest. Consequence worth knowing: a restored server's first archive pass is a no-op, and that is correct.

### ARCH-008 — `POST /admin/archive/restore` is a route §5.9's table does not list

*Accepted · 2026-08-07 · WP10.*

`POST /admin/archive/restore` is a route §5.9's table does not list. §12 WP10 asks for `catlogctl archive-restore <dir>` "via admin", and it has to be an admin route for the reason every other stateful verb is: tursogo's exclusive whole-file lock means `catlogctl` can never open a database itself (§5.4). `<dir>` is therefore a path on the **server's** filesystem, exactly like `POST /admin/backup`'s `dest`; catlogctl resolves it to an absolute path before sending so it means the same thing on both sides.

### ARCH-009 — Restore brings back the event log and the `player` rows, and nothing else

*Accepted · 2026-08-07 · WP10.*

Handles, credentials, bans and tombstones are identity state and are not archived (D8: only the raw log is). A server restored from an archive alone therefore serves the boards' *data* but resolves no handles — `catlogctl backup`'s copy of `events.db` is the other half of the recovery, and the DR runbook is "restore the backup, then replay any archive newer than it". The round-trip test asserts this explicitly rather than leaving it as a surprise.

### ARCH-010 — A restore verifies before it writes a row

*Accepted · 2026-08-07 · WP10.*

A restore verifies before it writes a row: the stored SHA-256 and byte length of every chunk, the event count, the declared seq range, seq ordering within the chunk, and that no chunk on disk is missing from the manifest (or present but unlisted). This runs at the exact moment nobody is positioned to notice a truncated or swapped chunk, so a partial replay reported as success would turn one disaster into two. Five tamper cases are tested.

### ARCH-011 — The purge seam is satisfied structurally, not by import

*Accepted · 2026-08-07 · WP10.*

The purge seam is satisfied structurally, not by import. `archive.Archiver.DeletePlayerArchive(ctx, sub)` matches `identity.ArchivePurger` without either package importing the other; `cmd/catlogd` passes the archiver as `identity.Deps.Archive`. WP3's existing spy test is kept (it proves the call happens) and a second test wires the *real* filesystem store with real chunks on disk (it proves the call deletes the prefix an archive run actually wrote), so the two halves of the §5.10 key layout cannot drift apart unnoticed. An end-to-end integration test purges one of three seeded players on a live server and watches exactly two objects — and the now-empty directory — leave the disk.

### ARCH-012 — `fsStore.Put` writes to a temporary file and renames

*Accepted · 2026-08-07 · WP10.*

Chunks are immutable but manifests are rewritten every run, and a half-written manifest would fail every future restore. The temporaries are named `.tmp-*` and skipped by `List`, so a crashed write never becomes a key. On R2 this disappears: `PutObject` is already atomic.

### ARCH-013 — R2 is documented, not built

*Accepted · 2026-08-07 · WP10.*

R2 is documented, not built (`docs/r2-archive-design.md`): S3-compatible API via `aws-sdk-go-v2` (**not added as a dependency**), credentials from `CATLOG_ARCHIVE_R2_*` environment variables only, path-style addressing, region `auto`, **no lifecycle rules and no versioning** — chunks are immutable so there is nothing to expire, and versioning would preserve exactly the data a purge deletes. Purge is a prefix delete. The migration is `rclone copy data/archive/ r2:<bucket>/` because the key layout is already the bucket layout; the only new code is one file with four methods.

---

## The two frontends

The server-rendered datastar site and the React reader. Same data, two UI patterns, kept side by side so they can be compared.

### UI-001 — Dependency added: `github.com/starfederation/datastar-go` v1.2.2 — imported as the `/datastar` subpackage, not the module root

*Accepted · 2026-08-07 · WP5.*

Dependency added: `github.com/starfederation/datastar-go` v1.2.2 — imported as the `/datastar` subpackage, not the module root. `import "github.com/starfederation/datastar-go/datastar"`; the module root has no importable package. It pulls in `github.com/CAFxX/httpcompression` v0.0.9, `github.com/valyala/bytebufferpool` v1.0.0 and `github.com/santhosh-tekuri/jsonschema/v6` as indirects; the CGO backends that appear in `go.sum` (`google/brotli/go/cbrotli`, `valyala/gozstd`) are optional contrib and are **not** in the build graph. The browser bundle stays vendored at v1.0.2 (WP0's entry). **SDK v1.2.2 and bundle v1.0.2 diverge legitimately** — they are independently versioned and the contract between them is the wire format (`datastar-patch-elements` / `datastar-patch-signals` with `selector`/`mode`/`elements` datalines), which `make e2e` now exercises in a real browser rather than by inspection.

### UI-002 — §5.7's `data-on-load="@get('/v1/feed/sse')"` is `data-init` in datastar v1.0.2

*Accepted · 2026-08-07 · WP5.*

The attribute no longer exists: the bundle's plugin list is `attr bind class computed effect indicator init json-signals on on-intersect on-interval on-signal-patch ref show signals style text`, and `data-on-load` would be parsed by the generic `data-on-<event>` plugin as a listener for a DOM `load` event that a `<div>` never fires — **silently doing nothing**, which is the worst possible failure mode for a feed. `#feed-panel` carries `data-init` instead. The `@get`/`@post`/… actions are unchanged (verified in the bundle: `Te("get","GET",!1)`).

### UI-003 — A-WP5-1: `POST /admin/events` added to the §5.9 admin API

*Accepted · 2026-08-07 · WP5.*

A-WP5-1: `POST /admin/events` added to the §5.9 admin API. §8's `feed.spec.ts` is specified as "POST a seed event via admin API", and no existing admin route can produce a *new* feed row on demand — `POST /admin/seed` is idempotent by construction (its event ids are derived from fixed strings, so the second call inserts nothing and publishes nothing, which is exactly the property WP4 wanted). The new route takes `{handle, events: [<§4.1 envelope>…]}`, mints the `id`/`session`/`wall_t` a human should not have to write, validates `type` against the same §4.2 registry `/v1/ingest` uses, inserts under the §5.4 admin mutex and drains the projector before answering. It skips the §4.5.3 auth chain and nothing else; the mux is loopback-only and never proxied (§3). It is also the dev-loop tool the plan implies and did not provide: push one event, watch the feed. Rejected alternative: rebuilding the license+proof+brotli client in TypeScript inside the e2e suite — a second implementation of the mod's most security-sensitive code, maintained by nobody, to test something `make e2e-full` and the ingest integration tests already cover for real.

### UI-004 — A-WP5-2: `server/catlogd.dev.toml` sets `static_dir = "site/dist"`, not §5.3's literal `"../site/dist"`

*Accepted · 2026-08-07 · WP5.*

The path is resolved against the working directory, and every dev entry point — `make dev`, `make e2e`'s `server-run-test-env`, `scripts/e2e-full.sh` — runs catlogd from the repo root, where `../site/dist` points outside the repository. §5.3's spelling is written from `server/`'s point of view. `[data] dir = "./data"` in the same file was already repo-root-relative, so this makes the two agree.

### UI-005 — A-WP5-3: the SSE feed's `<ul id="feed">` carries `data-source="ssr"|"sse"`, and the home page renders the feed server-side rather than shipping it empty

*Accepted · 2026-08-07 · WP5.*

A-WP5-3: the SSE feed's `<ul id="feed">` carries `data-source="ssr"|"sse"`, and the home page renders the feed server-side rather than shipping it empty. §5.7 shows an empty `<div id="feed">` that the stream fills. Two problems with the literal reading: the front page is blank for anyone whose browser never runs the datastar module, and — the load-bearing one — there is then **no way for a test to tell an open stream from a page whose module never loaded**, because both show the same rows. The server now renders the current feed with `data-source="ssr"` and the stream's prime replaces the whole list with an identical one marked `"sse"`; `feed.spec.ts` waits on that attribute before pushing its event, so "the line arrived over SSE" is a fact rather than a hope. The panel wrapper (`#feed-panel`) rather than the list carries `data-init`, so patching the list never disturbs the element the connection is bound to.

### UI-006 — A-WP5-4: a revoked credential moves to `data-revoked-jkt` instead of keeping `data-jkt`

*Accepted · 2026-08-07 · WP5.*

A-WP5-4: a revoked credential moves to `data-revoked-jkt` instead of keeping `data-jkt`. §5.7 asks the dashboard to list credential metadata "(jkt, issued, expires, revoked)" and §8's lifecycle spec asserts that after a revoke "the jkt disappears from the list". Both are honoured by rendering live credentials as `.credential[data-jkt]` and revoked ones as `.credential-revoked[data-revoked-jkt]`: the jkt genuinely leaves the live list, and the history is still visible, which matters because a player looking at a broken install needs to see *that* their credential was revoked, not merely that it is absent.

### UI-007 — A-WP5-5: destructive dashboard buttons arm on the first click and act on the second; there is no `window.confirm`

*Accepted · 2026-08-07 · WP5.*

A native confirm is browser chrome — invisible to the page, auto-dismissed by automation, and unstyleable. Arming puts the state in the DOM (`data-armed="true"`, and the label changes), so the e2e suite asserts the same thing a user sees. Applies to revoke, reissue and delete-my-data. After a successful revoke the page **reloads** rather than re-rendering the list in JS: the handle list is server-rendered, and one renderer is better than two that must agree.

### UI-008 — A-WP5-6: `readapi` grew exported query methods (`BoardList`, `Board`, `Player`, `ClampPaging`) in a new `query.go`, and its HTTP handlers now delegate to them

*Accepted · 2026-08-07 · WP5.*

Package `web` renders the same numbers `/v1/leaderboards/*` publishes, and the interesting part of assembling them is `visibleRows`' over-fetch-and-drop pass against the directory (§5.4) plus the rank arithmetic that keeps a profile consistent with the board page. A second implementation in `web` would have been a second place for a banned player to reach a public surface — and the more visible of the two. Nothing in the JSON changed; `readapi`'s own suite passes untouched.

### UI-009 — A-WP5-7: two additive seams in `internal/identity` — `Server.LoadDashboard` and `Server.SetErrorPage`

*Accepted · 2026-08-07 · WP5.*

A-WP5-7: two additive seams in `internal/identity` — `Server.LoadDashboard` and `Server.SetErrorPage`. `web` cannot import identity's unexported query helpers and identity cannot import `web` (it would be a cycle), so: `LoadDashboard(ctx, user_key)` returns the §4.8 `MeResponse` + `[]HandleView` the dashboard renders, reusing exactly the code `GET /api/me` and `GET /api/handles` serve, so the page cannot drift from the API; `SetErrorPage` installs WP5's login-failure **template** over WP3's hard-coded fallback (A-WP3-11), which stays in place for a catlogd wired without a web UI and keeps identity's own tests green. The DOM contract is unchanged and now pinned from both sides — `web.TestAuthErrorKeepsItsDOMContract` and `auth.spec.ts`. New sentinel `identity.ErrNoAccount` covers "banned or purged since the cookie was minted" as one error, because distinguishing them on a page reachable without signing in would leak moderation state.

### UI-010 — A-WP5-8: the playwright config lives at `site/e2e/playwright.config.ts` as §8 says, so the webServer commands are `make -C ../..`, not §8's `make -C ..`

*Accepted · 2026-08-07 · WP5.*

Playwright resolves a webServer's working directory against the config file's directory, and the config is two levels below the repo root. The suite is invoked as `pnpm -C site exec playwright test --config e2e/playwright.config.ts` (also `pnpm -C site run e2e`). `CATLOG_E2E_EXTERNAL=1` drops the webServer block entirely so `scripts/e2e-full.sh` can run `boards.spec.ts` against the instance the simulator just flew into — the whole point of that leg. `workers: 1`: the suite drives one stateful server where handle claims, bans and deletions are global facts.

### UI-011 — A-WP5-9: `make e2e-full` seeds the demo dataset *between* the rank-1 assertion and playwright

*Accepted · 2026-08-07 · WP5.*

A-WP5-9: `make e2e-full` seeds the demo dataset *between* the rank-1 assertion and playwright. §8 orders it "curl the board JSON asserting `sim_ace` at rank 1 → run `boards.spec.ts`". Those two want different databases: `hop-lithobrake` sets `biggest_lithobrake_survived = 62`, which is only rank 1 on an otherwise-empty board, while `demo_crasher`'s seeded 214 would outrank it — and `boards.spec.ts` asserts the seeded values by literal. So the script asserts rank 1 on the clean instance and seeds immediately afterwards. Consequence worth stating: **`boards.spec.ts` requires a seeded instance in both worlds**, which is why `server-run-test-env` seeds at startup too.

### UI-012 — A-WP5-10: `make e2e` runs on its own data directory (`data-e2e/`), wiped at every start, and `reuseExistingServer: false`

*Accepted · 2026-08-07 · WP5.*

A-WP5-10: `make e2e` runs on its own data directory (`data-e2e/`), wiped at every start, and `reuseExistingServer: false`. tursogo's exclusive whole-file lock (§5.4) means the e2e server cannot share `./data` with a `make dev`; wiping rather than reusing is what makes the seeded fixture — and therefore `boards.spec.ts`'s literal values and `handle.spec.ts`'s permanent `e2e_whiskers` claim — deterministic. The trade-off is real and is documented in the README: a `make dev` holding :8080 makes `make e2e` fail to bind rather than silently testing the wrong database.

### UI-013 — A-WP5-11: `keygen.js` never sends the private key, and the suite proves it rather than asserting it

*Accepted · 2026-08-07 · WP5.*

The module exports the public half only (`exportKey("jwk", keyPair.publicKey)` — never the pair, never the private key), copies it into a **new object with exactly `{kty,crv,x,y}`** (a whitelist, so a future WebCrypto member cannot leak by default), and runs `assertPublicOnly()` immediately before serialising. `postJSON` is the only `fetch` in the file and the private key is not in its scope. The private half is exported once, as PKCS#8, straight into the `Blob`. `handle.spec.ts` intercepts the actual `POST /api/handles` body and asserts its keys are exactly `["handle","jwk"]` / `["crv","kty","x","y"]`, that no `d` is present and that the string contains no `PRIVATE KEY` — and then recomputes the RFC 7638 thumbprint from the **downloaded** PEM inside the page and checks it equals the license's `cnf.jkt`, which proves the downloaded key really is the one the license was bound to (§4.6). The server-side refusal of a `d`-bearing JWK is asserted too, as defence in depth.

### UI-014 — A-WP5-12: `/login` and the 404 page

*Accepted · 2026-08-07 · WP5.*

A-WP5-12: `/login` and the 404 page. §4.8 lists `/login` without saying what is on it; it is the three §4.7 providers with the scope each one is asked for, plus the D17 statement, and `?next=` is sanitised to a same-site absolute path so it can never be an open redirector. `GET /` is registered last as a catch-all that renders a real 404 page (`#not-found`) — Go's pattern router only reaches it when nothing more specific matched, so the ingest, read, identity and static routes all still win. `/docs` (bare) redirects to `/docs/install`.

### UI-015 — Design feedback on the WP3/WP4 interfaces, recorded because it is the kind of thing that gets forgotten

*Accepted · 2026-08-07 · WP5.*

Design feedback on the WP3/WP4 interfaces, recorded because it is the kind of thing that gets forgotten. (a) `projector.Broadcaster.Subscribe` returning a plain `<-chan []store.FeedRow` with **no high-water mark** means the "subscribe, then prime, then dedupe" order is a rule the caller has to know and cannot be reminded of by the type; a `Subscribe() (rows <-chan []FeedRow, since int64, cancel func())` would have made the correct order the only compilable one. (b) `identity.Sessions.From` gives a `user_key` and nothing else, so every page that needs the account behind a cookie needed a new seam in `identity` — the session layer is one lookup short of being usable on its own. (c) `identity`'s JSON API is complete and correct but had **no non-HTTP entry point at all**; every response shape existed only at the far end of a `http.HandlerFunc`. Adding `LoadDashboard` took ten minutes, but a package whose only interface is its mux forces that on every future consumer. (d) `readapi` had the same shape and the same fix. None of these are defects — they are what "implement §4.8" produces when the only named consumer is HTTP — but a WP whose whole job is a second consumer pays for all of them at once.

### UI-016 — A-WP5-13: `scripts/e2e-full.sh` rebuilds `server/bin/*` if any binary is missing, rather than trusting its Make dependency

*Accepted · 2026-08-07 · WP5.*

Observed during this WP: a concurrent `make clean` removed `server/bin/` between `make e2e-full`'s `server-build` prerequisite and the script's use of `catlogctl`, three steps in and after a database had already been created. The script is also meant to be runnable on its own. The guard is one `[[ -x ]]` loop and turns a confusing mid-run failure into a rebuild.

### UI-017 — A second frontend, `spa/`: a Vite + React SPA on GitHub Pages, alongside — not instead of — the datastar site

*Accepted · 2026-08-07 · WP-SPA.*

Two UI patterns over one API, kept side by side so they can be compared. `site/` + `server/internal/web/` stay exactly as they are and remain the canonical site: they own the session, the dashboard, the credential wizard, real URLs and SEO. `spa/` is **read-only** and anonymous — four §4.8 endpoints, no login, no `/api/*`. Stack matched to `flexo` and verified at install time: React 19.2.8, Vite 8.2.0, `@vitejs/plugin-react` 6.0.5, React Compiler via `babel-plugin-react-compiler` 1.0.0 through `@rolldown/plugin-babel` 0.2.3 (plugin-react 6 dropped its inline `babel` option, so the compiler runs from `reactCompilerPreset()`), Tailwind 4.3.3, nanostores 1.4.2, react-aria-components 1.20.0, oxlint 1.77.0 + oxfmt 0.62.0, vitest 4.1.10 + happy-dom. pnpm exclusively. `make spa-build` / `spa-dev` / `spa-test` / `spa-lint`; `spa-build` and `spa-test` are wired into the root `build` and `test` the same way `site-build` already is, and `bootstrap` installs it.

### UI-018 — Hash routing, not a `404.html` fallback

*Superseded · 2026-08-07 · WP-SPA — superseded by **UI-022** — the SPA moved to HTML5 History routing over real paths.*

GitHub Pages has no rewrites, so a SPA deep link is served either by a `404.html` copy of `index.html` or by the fragment. The `404.html` trick answers every deep link with a real HTTP **404** — browsers render it, but caches, uptime checks and any CDN that intercepts 404s do not — and it has to be kept in sync with `base`. The fragment is never sent to the server: `/catlog/#/boards/rud_total` is one 200 for `index.html` from any host at any base path, with nothing to enable in repository settings. The cost is pretty URLs and SEO, and `site/` already owns both — which is exactly why this trade is available here and would not be for a lone SPA.

### UI-019 — A-SPA-1: `GET /v1/feed` and `GET /v1/feed/stream` added to the §4.8 read API (JSON), leaving `GET /v1/feed/sse` untouched

*Accepted · 2026-08-07 · WP-SPA.*

The datastar stream is a *rendering* transport, not a data transport: its frames are `PatchElements` payloads carrying rendered `feed-item` HTML addressed to DOM ids the Go templates own. That is why the server-rendered site gets its live feed for free, and it is why a React client cannot consume it — doing so would mean scraping data back out of another view's markup and breaking the next time a template changed. The new routes publish the same `store.FeedRow` values as JSON: `/v1/feed` is the snapshot (first paint, and the fallback when a stream cannot be held open), `/v1/feed/stream` is `text/event-stream` with one `event: feed` frame per new row. The stream deliberately has **no replay** — a reconnecting client re-reads the snapshot, rather than the server growing a `Last-Event-ID` cursor over a table that is capped at 500 and can lose rows out from under it. `Deps.Feed` is optional, so a Server built without a broadcaster simply has no stream route. Rejected: polling only (the live feed is the whole point of the comparison), and parsing the datastar HTML (couples two frontends that exist to be independent).

### UI-020 — CORS is attached to the read API's routes and to nothing else, and the allow-list is configuration

*Accepted · 2026-08-07 · WP-SPA.*

New `[cors] allowed_origins` (`CATLOG_CORS_ALLOWED_ORIGINS`), defaulting to the four loopback origins Vite's dev and preview servers can appear as — never a deployment URL. The middleware lives in `readapi` and is applied by `readapi.Server.Register`, so the boundary is mechanical: `/api/*` and `/auth/*` (cookie-authenticated), `/v1/ingest` (§4.5.3, on the /v1 prefix but not a read route) and the loopback admin mux never see it. No response ever carries `Access-Control-Allow-Credentials` — these are anonymous public facts, so there is no per-user answer to leak and no reason to let a cookie ride along. `Vary: Origin` is set unconditionally, including on responses with no allow header, because §4.8 ships `s-maxage=30` and a shared cache without the Vary would turn a narrow allow-list into an accidental wildcard. Origins are exact `scheme://host[:port]` matches; `*`, a trailing slash, a path or a non-http scheme are **startup** failures, since all of them fail silently at request time. `TestCORSCoversTheReadAPIAndNothingElse` in `server/cmd/catlogd/` boots the real wiring and asserts the header on the four read routes and its absence on eleven others including `/v1/feed/sse`.

### UI-021 — `@babel/core` is a peer dependency of `@rolldown/plugin-babel`, and every way React Compiler can fail to run is silent

*Accepted · 2026-08-07 · WP-SPA.*

Without it the plugin does nothing, the app still works, and every memoization is quietly gone. pnpm's auto-install-peers supplies it, but `spa/src/test/reactCompiler.test.ts` asserts on the compiled function body (`Symbol.for("react.memo_cache_sentinel")`) so a regression is loud. Verified at build time too: every one of the 19 components in an unminified bundle allocates a cache — `BoardPage` 46 slots, `PlayerPage` 39, `BoardTable` 27.

### UI-022 — SUPERSEDES the 2026-08-07 "Hash routing, not a `404.html` fallback" entry: the SPA now uses HTML5 History routing over real paths, and the fragment is gone

*Accepted · 2026-08-07 · WP-SPA.*

Owner decision, verbatim: *"No. no hash routing. These sites will be hosted on different web hosts on different domains, if at all concurrently. I don't want hash routing, I want html5 style SPA routing with regular paths."* The original rationale did not fail on its own terms — the `404.html` trick really does answer deep links with HTTP 404, and the fragment really is one 200 from any host — it failed on its **premise**. It traded pretty URLs away on the grounds that "`site/` already owns both, this frontend is the second view of the same data, not the canonical one". That is only true if the two frontends are one deployment. They are not: they are separate applications on separate hosts and separate domains that may not even run concurrently, so the SPA is the canonical view of catlog for whoever reaches it and has to own real URLs. The entry's own closing sentence — *"a lone SPA with no SSR sibling should take the `404.html` route and the real URLs"* — describes this deployment; it just did not know that yet. What replaced it: `spa/src/state/router.ts` parses `location.pathname + location.search`, navigates with `history.pushState`/`replaceState`, and re-reads on `popstate` (pushState fires no event, so `navigate` sets the store itself — the one piece of bookkeeping the History API does not do). Routes are `/`, `/boards`, `/boards/:stat?offset=`, `/p/:handle`, unknown paths render the existing not-found screen. **Links are plain `<a href>` elements** intercepted by one delegated `click` listener on `document`; it defers to the browser for a modifier key, a non-primary button, `target`, `download`, `rel="external"`, a cross-origin URL, a fragment, a path outside the deployment base, or an already-`defaultPrevented` event — so cmd/ctrl-click, shift-click, middle-click and right-click → "open in new tab" all still work, and the board pager became two links instead of two `onPress` buttons for the same reason. Navigation resets scroll and moves focus to `<main tabindex="-1">`, with `history.scrollRestoration = 'manual'` so the browser does not fight it against rows that have not been fetched yet. **The base path is threaded, never assumed**: `import.meta.env.BASE_URL` → `BASE_PATH`, stripped when reading a location and prepended when writing a link, with `SPA_BASE` still switching the whole app to a subpath; `vite.config.ts` now defaults `base` to `/` (its own domain) instead of `/catlog/`, and `index.html`'s favicon moved from `./favicon.svg` to `/favicon.svg` because a relative URL resolves against the *route* once deep links stop being fragments. The static-host cost the old entry was right about is paid by generating it: a `writeBundle` plugin copies `dist/index.html` to `dist/404.html` every build (never committed — a checked-in duplicate drifts silently and keeps "working" while serving a stale bundle), and `spa/README.md` carries the one-line equivalent for nginx, Netlify, Cloudflare Pages, Vercel and S3+CloudFront since the hosting is not settled. Verified in real chromium at **both** bases — `/` and `/catlog/`, against a seeded catlogd, on `vite preview` and on `pnpm dev`: a cold browser context sent straight to `/boards/biggest_lithobrake_survived` renders `demo_crasher` / `214 m/s` with no visit to the home page (HTTP 200, no redirect); three clicked links then three `goBack`s and three `goForward`s land on the right path with the right content each time; and a ctrl-click on an in-app link leaves the current page where it was and opens a real second tab.

### UI-023 — The two frontends are fully decoupled builds: the root `Makefile` owns the Go server, the .NET mod and the datastar site, and nothing else

*Superseded · 2026-08-07 · WP-SPA — superseded in part by **REPO-025** — the root `Makefile` now drives `spa/` too. The *builds* stay independent; only the entry point is shared.*

Owner decision: *"The SPA and datastar/go apps should be completely independent. Separate folders, separate tech stacks, separate builds, etc. Use the native pnpm tooling like flexo does for the SPA site."* `spa/` already had its own folder and its own `pnpm-workspace.yaml`; the coupling that remained was the Makefile, which installed, built, tested and linted it. All of it is gone — `grep -n spa Makefile` returns nothing. `bootstrap` no longer runs `pnpm -C spa install`, `build` is `server-build mod-build site-build`, `test` is `server-test mod-test`, `clean` no longer removes `spa/dist`, and the `spa-build` / `spa-dev` / `spa-test` / `spa-lint` targets and their `.PHONY` entries are deleted. (One unrelated word had to change: the `test-integration` comment said the mod leg "spawns" catlogd, which the grep matches.) `make e2e` and `make e2e-full` are untouched — they exercise the datastar site, which stays wired. The SPA is now driven only by pnpm from inside `spa/`, with **flexo's script shape**: `dev`, `build`, `preview`, `typecheck`, `lint`, `lint:fix`, `fmt`, `fmt:check`, `test`, `smoke`, plus an aggregate `check`. `lint` no longer secretly means "typecheck and format-check too" — it is `oxlint` and only `oxlint`, so a red build names the tool that failed; `.github/workflows/spa-pages.yml` runs the four gates individually and still uses pnpm, never `make`. New `spa/README.md` documents the whole thing standalone: what it is, the commands, `VITE_CATLOG_API_BASE` and the dev proxy, the routing/base-path story, and the deep-link fallback each static host needs. Verified there is no build-time coupling in either direction: `spa/tsconfig*.json` include only `src` and `vite.config.ts`, `vite.config.ts` reads nothing outside `spa/`, no source or config file references `server/`, `site/`, `mod/`, `infra/`, `../..` or `make`, there is no root `package.json` or root `pnpm-workspace.yaml` that could pull it into a workspace, and `pnpm install && pnpm typecheck && pnpm lint && pnpm fmt:check && pnpm test && pnpm build` all pass with no Go or .NET toolchain involved. The only remaining reference to the server is a comment in `scripts/smoke.mjs` saying how to start one, which is a *runtime* dependency of a browser smoke test and exactly right. **What deliberately stays coupled, because it is a published contract and not an accident:** the read API's CORS allow-list (`[cors] allowed_origins`) and the `GET /v1/feed` + `/v1/feed/stream` JSON endpoints added by A-SPA-1. Two independently deployed things talking over HTTP is the seam we want; removing it would just mean the SPA could not read anything. Not changed, and flagged rather than silently edited: `server/catlogd.dev.toml`'s CORS comment still mentions `make spa-dev`, which no longer exists — it belongs to `server/`, which this work package does not own.

### UI-024 — `@picocss/pico` is gone, and `site/assets/css/catlog.css` now carries the whole stylesheet

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

Pico's default type scale — an `h1` at 2× body, an `hgroup` subtitle outsizing the tables beneath it — was the entirety of the "the CSS is huge" complaint, and none of it was ours. Measured: **before**, `pico.min.css` ~83 kB plus 193 lines (~3.5 kB) of ours; **after**, one file of 1 411 lines / **29.5 kB**, of which 23.6 kB survives comment-stripping. That is roughly a third of the bytes and all of it chosen. It is dropped from `site/package.json`, from `vendorFiles` in `site/scripts/build.mjs` and from `layout.gohtml`'s `<link>`; the reset, the type scale, both themes and the form controls are hand-written against the token names in `docs/ui-design.md` §2, so the React reader's Tailwind `@theme` and this file declare the same names with the same values.

### UI-025 — The fontsource package is `@fontsource-variable/inter`, pinned at `5.3.0`, and only `files/inter-latin-wght-normal.woff2` (48 kB) is vendored

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The fontsource package is `@fontsource-variable/inter`, pinned at `5.3.0`, and only `files/inter-latin-wght-normal.woff2` (48 kB) is vendored. `docs/ui-design.md` §2.4 flagged the name as unresolved; it resolves as guessed. `scripts/build.mjs` copies that one file to `dist/fonts/` through `require.resolve`, for the same reason the pico entry used it — a version bump cannot silently leave a stale path behind — and `@font-face` is declared in `catlog.css` against `/static/fonts/`, not copied from the package's own CSS, so the `src:` URL matches where the build actually put the file. The build stays hermetic (D2): an e2e test asserts that loading the front page issues **zero** requests off 127.0.0.1, that the woff2 is served from this origin, and that `document.fonts.check("400 16px 'Inter Variable'")` is true.

### UI-026 — The latin subset is sufficient, and it was verified against the package's `unicode.json` rather than assumed — with one correction to the specification

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

Every dynamic string catlog renders is ASCII by construction, and of the punctuation we write ourselves, `—` U+2014, `†` U+2020, `·` U+00B7, `×` U+00D7, `↑` U+2191, `↓` U+2193 and the U+202F group separator `units.Format` uses are **all** inside the latin `unicode-range` (U+2020 and U+202F via `U+2000-206F`). **`→` U+2192 is in no Inter subset at all** — not latin, not latin-ext — so §2.4's list is wrong about it. Every "Full board →" is now a `.more` class whose `::after` is `›` U+203A, which is covered.

### UI-027 — datastar v1.0.2 separates a plugin from its key with a colon: `data-on:input`, not `data-on-input`

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The hyphenated form is **silently ignored** — no console error, no `pageerror`, no listener, an attribute that looks exactly right and does nothing. Bisected on a static probe page: on one page `data-on-click` never fired and `data-on:click` did. This cost most of an afternoon and is the same class of finding as WP5's `data-on-load` → `data-init`; the plugin list in that entry is accurate, the *syntax* for reaching a keyed plugin was not written down. `data-init` and `data-text` take no key and are unaffected. Modifiers attach after the key as documented (`data-on:input__debounce.250ms` works, ternaries and `@get(…)` inside the expression work).

### UI-028 — Every value cell carries `data-value` (the float as the API sent it) and `title` (that float plus its unit) beside the rendered string, and the e2e suite reads the attribute

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

Every value cell carries `data-value` (the float as the API sent it) and `title` (that float plus its unit) beside the rendered string, and the e2e suite reads the attribute. `boards.spec.ts` reconstructed numbers with `textContent.replace(/[^\d.-]/g,'')`, which on the career boards this redesign renders as durations turns "5m 13s" into 513 and "1h 01m" into 101 — values that still *sort*, so the assertion would have gone on passing while asserting nothing. That is a test that fails by succeeding, and moving it onto `data-value` was not optional. Changed assertions, all in the same commit as the renderer: the `fastest_to_luna` ordering check reads `dataset.value` and additionally asserts the cells really do render as durations; the lithobrake top row asserts `data-value="214"` and the text `214 m/s`; the profile rows assert `data-value`; the flagged-flight exclusions assert `td.value[data-value="999"]` rather than the text "999", which is both stricter and no longer matches a grouped "1 999".

### UI-029 — The Detail column is a display allow-list — `body`, `from`, `energy_j`, `t1_sim` — and everything else, including a key this build has never seen, is hidden

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

A deny-list would mean a fold adding a context key ships it into a public table by default. `flight` goes because it is a client-minted ULID that means nothing to a reader and eats the widest column; `career` goes because a 16-character token is not a fact anybody wants in a table, even after `readapi/privacy.go` has relabelled it per player. Both are still in the row's **Details** disclosure, which prints the blob as the API sent it — already post-redaction, so there is nothing further to strip — and both are in the raw event view, which is what that page is for.

### UI-030 — The `/docs/privacy` flagged-flight sentence is now honest, and the change is (b) of the two options WP-READ left open

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

It said flagged flights were "stored and **shown to you**, but score nothing and never appear publicly". The second half holds everywhere; the first never did. (a) — building a session-authed own-data view — is the better promise, and it is out of this work package's reach: it needs a `flight_state`-by-player query in `internal/store`, a new session-authed handler and a dashboard section, and this package owns `web/` and `site/`. It is also not obviously the right feature: Constitution §8 permits a flag's *only* effect to be that a flight does not score, and a page listing which of somebody's flights tripped `teleport` or `console` is a durable record attached to a person even when only they can read it. So the sentence now says what catlog does — flagged flights are stored, score nothing, and appear nowhere public "not on a board, not in the live feed, and not in an event log, including your own", followed by why. `docs/install`'s "they are visible to you" was the same half-truth in the same words and is fixed identically. **The promise is now true as written; if (a) is ever built, this sentence is what has to change back.**

### UI-031 — The raw event view is linked from the profile and from the dashboard, because the open question §6.2 gates it on is already closed in code

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The raw event view is linked from the profile and from the dashboard, because the open question §6.2 gates it on is already closed in code. `readapi.Server.scanEvents` excludes flagged flights per page and `flaggedFlights` documents why; the endpoint therefore cannot contradict the privacy page, and `docs/ui-design.md`'s "neither frontend should link it from a public page" was written against the possibility that it might. It links.

### UI-032 — `web.Read` gained `PlayerEvents`, `Search` and `Compare`; nothing reaches around it

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

That interface is the seam keeping the ban filter and the redaction to one implementation each, so widening it deliberately is the whole point of its existing. The one change outside `web/` is `readapi.splitHandles` → **`readapi.SplitHandles`**: `/compare?handles=` is parsed on both the JSON and the HTML side, and two surfaces disagreeing about which eight of nine handles survive the cap is a bug nobody would find twice. `comparePath` runs its output back through it, so "+ compare" on a handle already in the set is idempotent and a template can never build a link asking for more handles than the endpoint will answer.

### UI-033 — `/boards/{stat}` grew the period selector and a real pager, which is most of "see the global stats"

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

`/boards/{stat}` grew the period selector and a real pager, which is most of "see the global stats". `?period=` has been supported by `Board` since WP-PERIOD and was used by neither frontend; a static all-time ranking is a page you visit once. The chips are labelled by window rather than by duration — "This week", not "weekly" — because that is the question being asked. An unknown period is a 404 that says it was the *window* that was wrong, not the board. The pager infers "there is more" from a full page rather than from a count: `BoardResponse` publishes no total by design, and a full last page merely costs the reader one click to an empty one.

### UI-034 — Personalisation is `site/assets/js/me.js`, ~250 lines, and it is forced rather than chosen

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

Every public page carries `s-maxage=30`, so the response is not ours to personalise — the handle lives in `localStorage['catlog:me']`, is never sent to catlog as an identifier, and drives the header chip, the profile toggle, row highlighting, the front page's "Your standing" panel and the off-page "You: #147" strip (whose offset is computed from the rank the profile already carries, which is why `?around=` was never needed). Two rules it implements literally: it **never auto-clears** — a 404 can be a reversed moderation action or a rebuild, and the stored value is the user's data, so the notice offers *Keep it* and *Forget it* — and it **distinguishes a 404 from a failure**, showing nothing at all when the fetch fails, so a dropped connection never accuses anybody of not existing. The copy for a real 404 repeats the API's own silence: "catlog has no public profile for X any more", never banned, deleted, retired or renamed. The client-JS budget for the site is now datastar (34 kB, vendored), `keygen.js` on the dashboard, the inline `<head>` theme script, and this. No component framework: staying lean is this frontend's half of the D14 bake-off, and React Aria belongs to the other half.

### UI-035 — In the comparison, a tie marks every tied cell and a board only one compared handle is on marks none

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The counter boards tie constantly — two players with one ocean-impact RUD each — and marking whichever row arrived first presents an arbitrary choice as a fact. A single entrant is not "the best": there is nobody to be better than, and the mark would read as a claim about the board rather than about the comparison. Best is decided by the board's published `ascending` and never inferred, which an e2e test pins on `fastest_to_luna` by asserting the marked cell is the row-wise **minimum**.

### UI-036 — The profile's rank bar fills with standing, not with percentile

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The profile's rank bar fills with standing, not with percentile. `#1 of 41` is a 2 % percentile, and a 2 %-full bar beside a first place is nonsense — a bar reads as "more is better" whatever the label says. It is `100 − (rank−1)/players`, clamped, and the clamp is load-bearing rather than defensive: rank is filtered of banned players and the denominator is not, so the raw ratio can leave the interval and a bar 104 % wide would be a visible lie about arithmetic nobody would think to question.

### UI-037 — e2e went from 32 to 46 tests; the 32 still assert what they asserted

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

The additions are `site/e2e/reader.spec.ts`, one describe block per journey plus units, fonts and theme. The DOM contract in §11 of the design document is intact — `#home-title`, `#featured-boards .featured-board[data-stat]`, `tr.board-row[data-rank][data-handle]` with `td.value`/`td.context`, `#boards-index tr.boards-row[data-stat]`, `#boards-note`, `#board-title[data-stat]`, `#board-direction[data-ascending]`, `#profile-handle[data-handle]`, `#profile-stats tr[data-stat][data-rank]`, `#feed-panel`, `#feed[data-source][data-count]`, `li.feed-item[data-feed-id][data-type]`, the `#not-found-*`, `#auth-error-*` and `#wizard-*` sets, `#quota-*`, `.credential[data-jkt]`, `#logout`, `#delete-account` all keep their names — and `feed-list`/`feed-item` remain one partial shared by the page and the SSE handler, with `data-source="ssr|sse"` untouched. Two Go assertions in `web_test.go` changed because the markup genuinely did: the retired-handle card is now `class="handle handle-retired panel"`, and the board fixture returns no rows past offset 0 so the pager has something true to assert.

### UI-038 — Correction to the CSS figures above, measured after the last round of fixes:

*Accepted · 2026-08-07 · WP-UI-DATASTAR.*

Correction to the CSS figures above, measured after the last round of fixes: `site/assets/css/catlog.css` is **1 428 lines / 30 121 bytes**, **23 864 bytes** with comments stripped. The comparison is unchanged — it replaces ~83 kB of minified pico plus 193 lines of ours — but the exact numbers are these.

### UI-039 — Inter is `@fontsource-variable/inter@5.3.0`, and the SPA declares its own `@font-face` rather than importing the package's CSS

*Accepted · 2026-08-07 · WP-SPA-UI.*

The ⚠ FLAG in `docs/ui-design.md` §2.4 asked for the package name and entry path to be resolved at install time and recorded: the package name is right, but **there is no `latin.css` entry**. 5.3.0 ships `index.css`, `wght.css`, `standard.css` and `opsz.css`, and every one of them declares *all* subsets — cyrillic, cyrillic-ext, greek, greek-ext, vietnamese, latin-ext, latin — so importing any of them emits six `.woff2` files into `dist/` where five are never fetched. `spa/src/index.css` therefore declares one `@font-face` against `@fontsource-variable/inter/files/inter-latin-wght-normal.woff2` (48 kB, weight axis only, no optical-size axis) with the package's own latin `unicode-range` copied verbatim, and Vite fingerprints and rewrites the URL. **Both frontends must use this same package at this same major version**, and the datastar site should copy the same file rather than the package's CSS for the same reason. Verified by rendering, not by trusting the range: every non-ASCII glyph catlog draws — `—` U+2014, `†` U+2020, `↑↓` U+2191/2193, `·` U+00B7, `×` U+00D7, `→` U+2192 and the U+202F group separator — falls inside `U+0000-00FF` or `U+2000-206F`.

### UI-040 — The TypeScript port of `units.Format` is pinned to the Go original by 46 765 generated vectors, not only by the 43-row `units.Conformance` table

*Accepted · 2026-08-07 · WP-SPA-UI.*

The committed test (`spa/src/ui/units.test.ts`) asserts the transcribed table, because that is the artefact the two implementations share. But a 43-row table cannot prove the *rounding* rule, which is the part most likely to diverge silently — so the port was additionally checked, once, against the real `units.go` compiled and run over: every row of `Conformance`; every exact power of ten from 1e-9 to 1e15 and its neighbourhood (the one place `math.Log10` and `Math.log10` can disagree about `floor`); every ties case where half-up and half-even differ; every duration boundary; and 40 000 pseudo-random magnitudes across 24 decades, over all 14 unit strings. **Zero mismatches.** Recorded because the check is worth repeating and the conclusion is worth trusting: `Math.round(|x|·10^d)/10^d` followed by `toFixed(d)` on the *already-rounded* value reproduces Go exactly, and calling `toFixed` on the raw value does not.

### UI-041 — §6.2's ⚠ FLAG about flagged flights is resolved, and it was resolved in the API rather than the UI

*Accepted · 2026-08-07 · WP-SPA-UI.*

§6.2's ⚠ FLAG about flagged flights is resolved, and it was resolved in the API rather than the UI. `readapi/events.go`'s `flaggedFlights` excludes the events of a flagged flight from the public log entirely, so `docs_privacy.gohtml`'s "flights flagged as cheated … never appear publicly" is true as shipped. The endpoint's own comment gives the argument, and it is stronger than "publish and mark": a browsable public record of whose flights were flagged is precisely the durable public consequence attached to a person that Constitution §8 forbids, and the flags are `teleport`, `refuel`, `resource_edit`, `console` and `tuning`. **So the raw-event view is linked from the profile page, not gated** — `docs/ui-design.md` §6.2's "neither frontend should link it" was written before that code landed and no longer applies. `EventRow.flagged` is not needed and should not be added.

### UI-042 — The SPA's tables scroll inside a wrapper `<div>`, not by putting `display: block` on the `<table>`

*Accepted · 2026-08-07 · WP-SPA-UI.*

The SPA's tables scroll inside a wrapper `<div>`, not by putting `display: block` on the `<table>`. §11 requires the behaviour — a comparison of eight handles must not make the page scroll sideways — and `display: block` does deliver it, but it also stops the table filling its container: the anonymous table box inside a block box is sized to its content, so a three-column board renders at half the width of the panel around it. A wrapper with `overflow-x: auto` has the behaviour and none of that. §10 sanctions the two frontends differing in markup where they agree in behaviour; the datastar site can keep its `display: block` if its columns are wide enough not to notice.

### UI-043 — `@react-aria/optimize-locales-plugin` is configured with `['en']`, not `['en-GB']`, and the difference is a crash rather than a preference

*Accepted · 2026-08-07 · WP-SPA-UI.*

`@react-aria/optimize-locales-plugin` is configured with `['en']`, not `['en-GB']`, and the difference is a crash rather than a preference. react-aria-components imports its screen-reader strings for every locale it supports eagerly; catlog is English-only by design (fixed UTC everywhere, because "a leaderboard is a shared artefact"), so the rest are resolved to an empty module — 26 kB gzipped. The plugin matches a locale by language **and region**, so naming `en-GB` throws away `en-US`, which is what most browsers actually report; React Aria then finds no string table and every page dies on the first `Table` with `Cannot read properties of undefined (reading 'longPressMessage')`. A bare language keeps every English variant. Found by the smoke script, which is exactly the failure a unit test cannot see.

### UI-044 — `GET /v1/leaderboards` is the source of the window list on a board page, and of the front page's global tiles

*Accepted · 2026-08-07 · WP-SPA-UI.*

`GET /v1/leaderboards` is the source of the window list on a board page, and of the front page's global tiles. `BoardResponse` carries `period` and `bucket` but not `periods`, so a board page reads the set of windows off the index rather than hard-coding `alltime|daily|weekly|monthly|yearly` — the same request the header and the front page already make, and `s-maxage=30` at the CDN. The front-page tiles (boards, placements, busiest board) are assembled from the same response, which is §8.3's "ship the version assembled from `/v1/leaderboards` first": no `/v1/stats` endpoint is needed and none should be added on this account.

### UI-045 — A profile's rank is all-time only, so the SPA hides the "You: #147" strip on a windowed board rather than showing an all-time rank next to a weekly value

*Accepted · 2026-08-07 · WP-SPA-UI.*

A profile's rank is all-time only, so the SPA hides the "You: #147" strip on a windowed board rather than showing an all-time rank next to a weekly value. §8.1's gap is real: `readapi.Server.player` reads `player_stat`, and `Projections.StatAhead` has no `player_stat_period` equivalent. Showing the all-time number beside a weekly board would be a **wrong** number rather than a missing one. The strip, the "Your standing" panel and the profile's own ranks are therefore unwindowed and say so; the board pages offer `?period=` because `Board` already supports it. When a period-aware `StatAhead` lands, the strip becomes windowed and nothing else changes.

### UI-046 — The SPA's bundle grew from 137 kB gzipped to 165 kB on the initial load, and the increase is react-aria-components

*Accepted · 2026-08-07 · WP-SPA-UI.*

The SPA's bundle grew from 137 kB gzipped to 165 kB on the initial load, and the increase is react-aria-components. §10 makes the kit **required** on this side, and a `ComboBox` (search, on every page), `Tabs`, `Disclosure`, `ToggleButtonGroup`, `Select`, `CheckboxGroup` and `TagGroup` cost roughly 35 kB gzipped over the `Table` the old bundle had alone. Three things claw some of it back and are worth keeping: the locale plugin above (−26 kB), route-level `React.lazy` for `/compare`, `/search` and `/p/{handle}/events` — the screens a visitor reaches deliberately rather than lands on — and keeping `HandleTags` out of the kit's barrel so `TagGroup` is not dragged into the chunk every visitor downloads by a re-export. **A barrel that re-exports everything defeats route splitting**; the kit's `index.ts` says so in a comment.

### UI-047 — The SPA adopts §11's DOM contract even though the e2e suite does not assert against it

*Accepted · 2026-08-07 · WP-SPA-UI.*

The SPA adopts §11's DOM contract even though the e2e suite does not assert against it. `site/e2e/` drives the datastar site; nothing tests the SPA's markup but `spa/scripts/smoke.mjs`, which is the SPA's own. The ids and classes were adopted anyway — `#home-title`, `#featured-boards.featured-board[data-stat]`, `tr.board-row[data-rank][data-handle]` with `td.value[data-value]`, `#boards-index tr.boards-row[data-stat]`, `#boards-title`, `#boards-note`, `#board-title[data-stat]`, `#board-direction[data-ascending]`, `#profile-handle[data-handle]`, `#profile-stats tr[data-stat][data-rank]`, `#feed-panel`, `#feed[data-source][data-count]`, `li.feed-item[data-feed-id][data-type]`, `#not-found`, `#not-found-detail`, `#not-found-home` — because they make the two frontends diffable by a human comparing screenshots and by a script comparing DOM, which is the only thing that keeps a bake-off honest. **`data-value` in particular is not decoration**: the smoke script now reads every value cell's `data-value` and compares it against the float the API sent, which is the assertion that survives a career-time board rendering `5m 13s`.

### UI-048 — Handle search never sends a request below two characters, and that is enforced in the client as well as at the call sites

*Accepted · 2026-08-07 · WP-SPA-UI.*

Handle search never sends a request below two characters, and that is enforced in the client as well as at the call sites. `MinQueryLen` is 2 and a shorter query is a **400**, not an empty 200, so a search box that fires on the first keystroke errors on every single search. `searchHandles` resolves an empty result rather than issuing a request it knows will fail; the combo box expresses the guard as a `null` resource key so no hook becomes conditional; and the smoke script watches the network log and asserts that typing one character asked the origin nothing. The empty state says *"No handles match `xyz`"* — **not** "start with", because the endpoint is prefix-first and *then* substring.

### UI-049 — A column header names the unit only when every cell in the column ends in it — `units.Label`, rule 7, pinned by `units.LabelConformance`

*Accepted · 2026-08-07 · WP-UI-UNITS.*

The defect: `docs/ui-design.md` §4.4 said the board table keeps the unit in the column header and gave the literal `<th class="value">{{$board.Unit}}</th>`, so both frontends did exactly that, and a career-time board — which carries `unit: "ms"` and renders `37.5 s`, `10h 23m`, `243d 01h` — got a column of durations under a header reading `ms` (`MS ↑` in the SPA, where the header row is uppercased). The spec told both frontends to be wrong, identically, which is why the fix is one rule in `server/internal/units` rather than a patch in two templates. **The rule:** rules 3, 4 and 6 all render `value + symbol` and an SI prefix goes *before* the symbol, so `1.82 Mm`, `7 799 m/s` and `6 RUDs` all end in the unit the header names — the header shows it verbatim, in its own case. Rule 5 is the sole exception, because no cell in a duration column ends in `ms` and only some end in `s`; its header names the **quantity**, `Time`. No unit at all → `Value`. **The counting boards keep their label** (`RUDs`, `tumbles`, `orbits`, `bodies`, `dockings`, `stagings`, `kittens`) because the label *is* the name of the thing counted, which is exactly what a header wants, and it is what every cell already ends in — the question was asked and the answer is "do not invent `Count`". `units_test.go` and `units.test.ts` both assert the rule *mechanically* as well as by table: render a value in the unit and check whether the string ends in the label, which must be true for every family and false for `Time`. **`units.Measured` is the prose sibling** for the "Measured in ___." line above every board, and it deliberately keeps the storage unit the header drops — *"Measured in ms, shown as a duration."* — because that sentence is the one place a reader is told the API publishes milliseconds, which is what makes `data-value="537500"` and `title="537500 ms"` legible instead of mysterious. **The header cell is the one header cell neither frontend uppercases**: `M/S` is not a unit, `PA` is not a unit, and `RUDS` is not how catlog writes that word. datastar had `thead th.value { text-transform: none }` already; the SPA needed `normal-case tracking-normal` on that `HeadCell` and nowhere else. Applied to five surfaces per frontend (board table header, the "Measured in" line, the board index column, the comparison row header, and — unchanged — the profile's mixed-unit `Value` column). Verified live against a seeded server in both frontends: `fastest_to_luna` reads `Time` over `50 s` / `8m 57s`, `biggest_lithobrake_survived` reads `m/s` over `214 m/s`, `rud_total` reads `RUDs` over `6 RUDs`, and `distance_travelled` still reads `m` over `4.21 Mm` / `930 km`.

### UI-050 — Correction to the 2026-08-07 WP-SPA-UI entry above: `→` U+2192 does *not* fall inside `U+0000-00FF` or `U+2000-206F`

*Accepted · 2026-08-07 · WP-UI-UNITS.*

Correction to the 2026-08-07 WP-SPA-UI entry above: `→` U+2192 does *not* fall inside `U+0000-00FF` or `U+2000-206F`. `0x2192 > 0x206F`, so the arithmetic in that entry is simply wrong; the WP-UI-DATASTAR entry from the same day has it right, and `@fontsource-variable/inter@5.3.0`'s `unicode.json` confirms it — **U+2192 is in no subset of the package at all**, latin, latin-ext or otherwise. This file is append-only, so the correction is recorded here rather than edited there; `docs/ui-design.md` §2.4 has been fixed in place, since that is a living spec. Three literal arrows were still shipping in rendered HTML (`docs_api.gohtml` ×2, `docs_privacy.gohtml` ×1) and are now `›` U+203A, matching the `.more::after` treatment. The same check found two more glyphs outside every subset, both in the SPA and both rendering from a fallback face at the wrong width: `✓` U+2713 on the compare picker's checkbox and `▾` U+25BE on the event-type select. Both are now `lucide-react` icons — SVG, no font dependency — which is the general answer whenever a symbol is wanted. An e2e assertion now walks five rendered pages and fails on any `→`, and the rule to carry forward is that a `unicode-range` is **the list of glyphs the file contains, not a hint**: check `unicode.json`, do not do the arithmetic in your head.

### UI-051 — The datastar site's review findings were all real, and the fixes stay inside the no-component-library budget

*Accepted · 2026-08-08 · WP-UI-POLISH.*

The datastar site's review findings were all real, and the fixes stay inside the no-component-library budget. `#feed[data-count]` was dropped rather than kept fresh (the SSE path prepends single rows; a count nothing asserted is not worth per-event re-patching — §11 updated). `<time datetime>` now carries RFC 3339 via a new `iso` template func. The event-type filter unions `ingest.KnownTypes()` ∪ page ∪ active filter — the taxonomy already had one exported home, so web imports it rather than duplicating it. The home page degrades to an empty feed on a feed-read failure, matching the SSE prime path instead of 500ing. Only live-streamed feed rows animate (`data-arrived`, stamped by the patch, never the prime — the SPA's disco rationale, adopted). A stream-status hint (`#feed-status`) is driven by datastar's documented `datastar-fetch` events plus a MutationObserver, since v1.0.2 has no "first frame" event. Feed handles are profile links via `trimHandle` (every `stats.Summarize` branch is handle-first; plain-text fallback if that ever changes). 404s are `no-store` — the catch-all matches unbounded distinct URLs and each would have bought its own CDN entry. Plus: feed/table-wrap keyboard + ARIA treatment, the suggest popover's invalid listbox roles dropped for an honest live region, and the theme toggle names its current mode.

### UI-052 — The SPA's first paint sheds react-aria it wasn't using yet

*Accepted · 2026-08-08 · WP-UI-POLISH.*

The header search renders a plain accessible input and upgrades to the RAC ComboBox on first focus/hover — the dynamic import lives at module level because React Compiler silently bails on a component containing dynamic-import syntax, which `reactCompiler.test.ts` caught when the new component joined its list. The theme switch is a hand-rolled WAI-ARIA radiogroup. react/react-dom/scheduler are pinned to a stable `vendor` chunk via rolldown's `advancedChunks` (`manualChunks` is not supported on vite 8/rolldown). Eager JS: 164.3 → 148.7 KB gz, with another ~22 KB gz (ComboBox + overlay machinery) now loading only on first search interest.

### UI-053 — `useResource` caches for 30 s because the server's header can't

*Accepted · 2026-08-08 · WP-UI-POLISH.*

`useResource` caches for 30 s because the server's header can't. `s-maxage` is shared-cache-only, so browsers were storing nothing and every navigation refetched. A module-level cache (TTL matching the server's own freshness window, in-flight dedupe, errors never cached, refcounted abort so only the last consumer aborts a flight) keeps the client honest without a data library. Also: relative timestamps moved into a `RelativeTime` leaf so the 30 s clock tick re-renders ~50 text nodes instead of rebuilding every RAC table collection, and the feed store gained a single-event fast path (head insert, Set-based `arrived`) replacing a per-event full sort.

### UI-054 — `useResource` never hands an aborted request to a new consumer, and the fetch stub honours `signal`

*Accepted · 2026-08-08 · docs.*

**The symptom was every page of the React reader stuck on "Loading boards…" while the live feed worked perfectly**, which reads exactly like a server or CORS problem and is neither. `/v1/leaderboards` came back `net::ERR_ABORTED` once and was never requested again; `/v1/feed` and `/v1/feed/stream` were fine because the feed is nanostores plus SSE and does not go through this hook at all. Reproduced with and without `CATLOG_LIMITS_RATELIMIT_DISABLED=1`, so the rate-limit switch was a red herring.

**The mechanism.** `useResource` shares one in-flight request per key, and its cleanup cancels that request when the *last* consumer unmounts. React's StrictMode runs mount → cleanup → mount on every mount in development, so the cleanup dropped the reference count to zero and aborted, and the immediate remount then found that same entry still in the map and still unsettled — and `acquire`'s fast path reused it. The newcomer subscribed to a promise that had already rejected with the abort, and the effect's rejection branch deliberately ignores aborts (an abort is a consumer's own cleanup, not a failure to show). So nothing rendered, nothing errored, and nothing ever asked again.

The fix is one clause: an entry whose controller has already fired is not reusable, however fresh it is. A cancelled request is not an answer. Cached *successful* answers are unaffected — a settled entry is never aborted, because the cleanup only aborts when `!settled`.

**This is not only a StrictMode artefact.** Any unmount and remount inside the same commit hits it — a route change and back, a key that flips and returns — so it was a production bug that development merely made constant.

**Why 305 passing tests said nothing.** `stubFetch` ignored `init` entirely, so an aborted request still resolved `200`. The suite was structurally incapable of reproducing anything about cancellation, and the one existing abort test only asserted that `signal.aborted` became true — never that the promise rejected or that a later consumer recovered. The stub now checks the signal twice, as the real thing does: once on entry, and once after yielding, so a caller that aborts synchronously sees a rejection rather than an answer. With that alone the two new regression cases failed on `loading`, which is the browser symptom exactly.

The lesson worth keeping: **a test double that quietly ignores a parameter does not weaken a test, it deletes the whole category of bug that parameter exists for.**

### UI-055 — The React reader never renders out of the browser HTTP cache

*Accepted · 2026-08-08 · docs.*

**The symptom: the SPA showed data exactly one revision older than the datastar site, on every load, however many times you reloaded.** Both surfaces send byte-identical `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`, so the headers were not the difference — the browser's treatment of them was. A datastar page is a top-level **document**, and a reload revalidates a document. The SPA's data is a **`fetch` subresource**, which Chrome has not force-revalidated on reload for years, so it falls under ordinary cache rules.

**Those rules were worse than they look.** `s-maxage` is ignored by a private cache, but `stale-while-revalidate` is **not** — Chrome honours it. There is no `max-age`, no `Expires` and no `Last-Modified` on these responses, so the browser's freshness lifetime is **zero**: every response was stale the instant it was stored, and the 300-second SWR window then served it while revalidating behind it. Every page therefore rendered the *previous* load's body. Measured against a live server: `server=31 spa=30`, `server=32 spa=31`, `server=33 spa=32`, and current on every load with the HTTP cache disabled.

**The measurement that decided the fix.** A counting pass-through in front of catlogd showed the origin receiving **one request per page load regardless** — the SWR revalidation goes out whether or not the stale body was served. So in a private cache the staleness bought no saving at all: identical origin load, older data. There was no freshness-versus-cost trade to make, which is the only reason this is a one-line change rather than a judgement call about §2.

`apiGet` now sets `cache: 'no-cache'`. It changes only what this client asks for, so the CDN's SWR — where the header genuinely earns its place, one revalidation serving every visitor — is untouched; and Cloudflare ignores client `Cache-Control` request directives by default, so it cannot be used to bust the shared cache either. `no-cache` rather than `no-store` keeps a 304 available; nothing carries a validator today, so these are full 200s in practice, and the fix if that ever matters is an `ETag` on the server rather than a change here.

**The comment in `client.ts` asserted that these headers were "ignored by browsers", and that false premise is the whole reason this survived.** It is corrected in place, because a wrong explanation is worse than none — it stops the next reader looking. Pinned by `client.test.ts`, so dropping the option is a failing test rather than a silent regression.

---

## The mod and its KSA-free core

`catlog.lib`, the game project, the detector rules, the shipper, and the hard client-side reporting floor.

### MOD-001 — NuGet for `catlog.lib`, resolved by restore and pinned exactly

*Accepted · 2026-08-07 · WP6.*

NuGet for `catlog.lib`, resolved by restore and pinned exactly: `Microsoft.Data.Sqlite` **10.0.10**, `Ulid` (Cysharp) **1.4.1**, `Tomlyn` **2.10.1**. Plus one pin the plan does not list: **`SQLitePCLRaw.bundle_e_sqlite3` 3.0.5**, because `Microsoft.Data.Sqlite` 10.0.10 resolves `SQLitePCLRaw.lib.e_sqlite3` 2.1.11 transitively, which carries **GHSA-2m69-gcr7-jv3q** (high). `dotnet list mod/catlog.lib package --vulnerable --include-transitive` is clean with the pin. The pin has a do-not-remove comment in the csproj; WP8's game project inherits it through the ProjectReference and must copy the matching native `e_sqlite3` binaries.

### MOD-002 — `GameSignal`s do not travel through `SnapshotStore`

*Accepted · 2026-08-07 · WP6.*

The copied gatOS store is latest-wins, which is correct for passive telemetry (a dropped sample costs resolution; the detector compares prev/curr) and **wrong** for discrete signals — a dropped RUD or impact is a permanently lost leaderboard entry. `Telemetry/GameBridge.cs` is the seam: `Frames` is the latest-wins `SnapshotStore`, `Signals` is an unbounded `System.Threading.Channels.Channel<GameSignal>` (lossless, FIFO, single-writer/single-reader). The rationale is documented at both types.

### MOD-003 — Frame boundaries travel in-band as `FrameBoundarySignal`

*Accepted · 2026-08-07 · WP6.*

Channel order is the only thing that still carries "these happened in the same frame" once signals leave the game thread, and `ImpactCorrelator` needs exactly that. WP8 raises one per frame after `Universe.ApplyVehicleSolvers` **and** `InputEvents.ApplyInputEvents`.

### MOD-004 — No zeroed fallback snapshot, anywhere

*Accepted · 2026-08-07 · WP6.*

SEMK's `BuildEmptySnapshot` (zeroing parent body / situation / eccentricity on a throw) manufactures phantom SOI changes and phantom orbit-achieved edges when fed to a comparator, and those score. Contract for WP8: a per-vehicle read failure means the vehicle is **absent** from that frame's `TelemetryFrame.Vehicles`, logged once. `EventDetector` additionally refuses to report an SOI transition to or from a blank parent id.

### MOD-005 — `TelemetrySnapshot` carries an explicit `OrbitClass` discriminator (`Unknown`/`Bound`/`Hyperbolic`/`Parabolic`)

*Accepted · 2026-08-07 · WP6.*

`TelemetrySnapshot` carries an explicit `OrbitClass` discriminator (`Unknown`/`Bound`/`Hyperbolic`/`Parabolic`), filled by WP8 from `Orbit.IsBound()/IsHyperbolic()/IsParabolic()`. Nothing in `catlog.lib` NaN-sniffs an apsis: per [ksa-integration.md](ksa-integration.md) B4 a hyperbolic apoapsis is **negative**, not NaN. `Unknown` falls back to a finite `ecc < 1` so the simulator and hand-built test fixtures still work.

### MOD-006 — Contract clarification (no `ver` bump): `vehicle.orbit.ap_m`/`pe_m` are altitudes above the parent's mean radius, in metres

*Accepted · 2026-08-07 · WP6.*

Contract clarification (no `ver` bump): `vehicle.orbit.ap_m`/`pe_m` are altitudes above the parent's mean radius, in metres — not the game's from-centre radii. §4.2 did not say which; the §7.2 orbit-achieved rule (`pe_alt > atmo_height + 1000`) only makes sense with altitudes, so altitude it is, and `TelemetrySnapshot.ApAltM`/`PeAltM` are named to make that unmissable. WP1/WP4 read them as altitudes.

### MOD-007 — `inc_deg` is degrees

*Accepted · 2026-08-07 · WP6.*

SEMK stores the game's radians under a field named `Inclination` (an as-shipped bug); gatOS multiplies by `180/π`. WP8 must convert at capture.

### MOD-008 — `peak_g`/`max_q_pa` are `double?` on the snapshot and omitted from the payload when no sample in the window carried a reading

*Accepted · 2026-08-07 · WP6.*

`peak_g`/`max_q_pa` are `double?` on the snapshot and omitted from the payload when no sample in the window carried a reading (implements the 2026-08-06 orchestrator entry). `WindowAccumulator` folds them only over samples that had data; `TelemetryWindowPayload` marks both `[JsonIgnore(WhenWritingNull)]`.

### MOD-009 — `situation` predicates are derived from the string, never from a game enum

*Accepted · 2026-08-07 · WP6.*

`situation` predicates are derived from the string, never from a game enum. `Telemetry/SituationInfo.cs` encodes the verified packed-bitfield table (`value = (surfaceContact << 1) | onRails`, 8 values) as a static map keyed by the lowercase name, with `HasSurfaceContact`/`HasTerrainContact`/`HasOceanContact`/`IsOnRails`. Every lookup is total — an unrecognised value reports no contact and off-rails rather than throwing, so §4.2's "open set" holds and a ninth value in a future build degrades instead of crashing. There is no exhaustive `switch` over situations in catlog.

### MOD-010 — Detector rules are latched edges, not raw prev/curr diffs

*Accepted · 2026-08-07 · WP6.*

Debounce therefore rate-limits without losing a transition: a suppressed change is re-detected on the next sample and reported `from` the last state that actually reached the wire. Atmosphere is a Schmitt trigger on a latched `InAtmosphere` (enter below `atmo × 0.98`, exit above `atmo × 1.02`) — a bare threshold plus debounce still alternates. A **backwards jump in `sim_t`** (save load) rebaselines the vehicle instead of diffing across it.

### MOD-011 — Telemetry windows are half-open in sim time

*Accepted · 2026-08-07 · WP6.*

Telemetry windows are half-open in sim time: a window opened at `t0` covers `t0 ≤ t < t0 + window`, and the sample at exactly `t0 + window` closes it and opens the next. A flight ending flushes the partial window before `flight.ended`, so the seconds before a RUD are not discarded.

### MOD-012 — `vehicle.impact.survived` holds impacts one full frame, not one tick

*Accepted · 2026-08-07 · WP6.*

All impacts land before all physics destructions in a frame (verified, [ksa-integration.md](ksa-integration.md) §3), but a *manual* destroy lands in the game's later input-apply pass — a verdict taken at the end of the impact's own frame would call a scuttled vehicle a survivor. An impact seen in frame N is resolved at the end of frame N+1; a destruction in either frame flips `survived` to false.

### MOD-013 — `GameSignal` gains `EngineSignal`

*Accepted · 2026-08-07 · WP6.*

`GameSignal` gains `EngineSignal`, which the §7.2 signal list omits although §4.2 requires `engine.ignition`/`shutdown`/`flameout`. Also `SplashSignal` is correlated as an impact with `launch_pad = false`, and `FrameBoundarySignal` (above) is new. `EngineEventKind.Flameout` is a catlog-derived concept — the game has none ([ksa-integration.md](ksa-integration.md) B3).

### MOD-014 — A `FlaggedSignal` with a null `vehicle_id` is a session-wide flag

*Accepted · 2026-08-07 · WP6.*

A `FlaggedSignal` with a null `vehicle_id` is a session-wide flag (the `tuning` case has no vehicle). It taints every open flight *and* every flight started later in the session, emitting one `flight.flagged` per flight. Flags are deduped per (flight, flag), so a teleport detected on twenty consecutive frames emits once.

### MOD-015 — Outbox `kind`: only `telemetry.window` is kind 0 (passive/droppable); everything else is kind 1

*Accepted · 2026-08-07 · WP6.*

Outbox `kind`: only `telemetry.window` is kind 0 (passive/droppable); everything else is kind 1. `roster.snapshot` is kind 1 despite being periodic — it carries kitten totals that feed boards. `Prune` deletes oldest kind-0 rows in chunks until the cap is met and stops when only kind-1 rows remain.

### MOD-016 — The batch body is LF-separated NDJSON with a trailing LF

*Accepted · 2026-08-07 · WP6.*

The batch body is LF-separated NDJSON with a trailing LF, and the outbox stores each envelope's serialized line verbatim so the bytes the server hashes are the bytes the detector produced. One `JsonSerializerOptions` (`Util/CatlogJson.cs`) for the whole library; `DefaultIgnoreCondition` is deliberately *not* `WhenWritingNull`, because §4.1 requires `"flight": null` to be present — omission is opted into per property.

### MOD-017 — Shipper decisions beyond the §4.5.3 table

*Accepted · 2026-08-07 · WP6.*

Shipper decisions beyond the §4.5.3 table. (a) `400 malformed_batch` / `415` latch the shipper dead with one ERROR and **do not drop the batch** — retrying forever would spin and dropping would destroy data, so a poison-pill batch is made visible instead. (b) A `413` when the cap is already at the floor of 50 also latches dead, for the same reason. (c) The halved batch cap stays halved for the session (deterministic, no oscillation). (d) A `200 … "replay": true` advances the local `seq`/`ph` chain, because a replay short-circuit means that batch id was already stored and the server's stream state already moved. (e) An oversize compressed body is caught locally and halves the cap without spending a round trip.

### MOD-018 — Test framework divergence made explicit

*Accepted · 2026-08-07 · WP6.*

Test framework divergence made explicit: xunit 2.9.3 (per D13/§7.1) with gatOS's *structural* conventions carried over — mirrored directory layout, one `sealed` class per type with a plan-section doc comment, because-messages on assertions, an `internal static TestData` factory, and an assembly-wide log silencer (a `[ModuleInitializer]` here, where gatOS uses NUnit's `[SetUpFixture]`).

### MOD-019 — `contracts/testdata` conformance tests are written and skipping

*Accepted · 2026-08-07 · WP6.*

`contracts/testdata` conformance tests are written and skipping. xunit 2.9.3 has no dynamic `Assert.Skip` (that is xunit v3), so `Conformance/ContractVectorFactAttribute.cs` sets `FactAttribute.Skip` from its constructor when `contracts/testdata/expected/verify-results.json` is absent. 16 tests currently skip with the message *"run `catlogctl testvectors generate contracts/testdata` (WP2)"*; they switch themselves on the moment WP2's generator has run, with no code change. `TODO(WP2)` markers are on the attribute and the test class.

### MOD-020 — The assembly guard is re-anchored on `EventEnvelope`

*Accepted · 2026-08-07 · WP6.*

The assembly guard is re-anchored on `EventEnvelope` as §7.1 specifies, and additionally asserts that the only non-`System.*` references are the four expected NuGet assemblies. The WP0 `CatlogLib` marker type is **kept**, not deleted: `catlog.sim/Program.cs` and `catlog.integration.tests/PlaceholderTests.cs` still reference it and both are WP7's to replace — deleting it now would break `make mod-build`. WP7 should delete `mod/catlog.lib/CatlogLib.cs` once those placeholders are gone.

### MOD-021 — Files added beyond the §7.2 tree

*Accepted · 2026-08-07 · WP6.*

Files added beyond the §7.2 tree, all small and each with its rationale in XML docs: `Telemetry/GameBridge.cs` (the game-thread seam), `Telemetry/TelemetryFrame.cs` (the `SnapshotStore` payload, with the required `static Empty`), `Telemetry/SituationInfo.cs`, `Detect/EventPipeline.cs` (composes detector + windows + correlator + tracker; this is what WP7/WP8 drive), `Detect/EventFactory.cs`, `Events/EventTypes.cs` (registry split out of `Payloads.cs`), `Ship/BrotliCodec.cs`, `Ship/IShipperClock.cs` (+ `BackoffPolicy`), `Ship/ProofSigner.cs`, `Util/ModLog.cs`, `Util/Bytes.cs`, `Util/Ids.cs`, `Util/CatlogJson.cs`, `Util/SubsystemHealth.cs`, `Util/PerfStat.cs`. `ValueStat` was **not** copied — nothing in the KSA-free core counts raw numbers; WP8 can add it next to the alloc tripwire if the status window wants it.

### MOD-022 — Small implementation notes: base64url comes from the BCL (`System.Buffers.Text.Base64Url`, .NET 9+) — no hand-rolled `Replace('+','-')`; `BrotliCodec.Decompress` normalizes the BCL decoder's `InvalidOperationException` on corrupt input into `InvalidDataException` so callers have one exception type; no `JsonSerializerContext` source-gen (gatOS and unscience both use reflection-based STJ and the mod is neither trimmed nor AOT)

*Accepted · 2026-08-07 · WP6.*

Small implementation notes: base64url comes from the BCL (`System.Buffers.Text.Base64Url`, .NET 9+) — no hand-rolled `Replace('+','-')`; `BrotliCodec.Decompress` normalizes the BCL decoder's `InvalidOperationException` on corrupt input into `InvalidDataException` so callers have one exception type; no `JsonSerializerContext` source-gen (gatOS and unscience both use reflection-based STJ and the mod is neither trimmed nor AOT).

### MOD-023 — `mod/catlog.lib/CatlogLib.cs` deleted

*Accepted · 2026-08-07 · WP7.*

`mod/catlog.lib/CatlogLib.cs` deleted, as the 2026-08-07 WP6 entry asked: `catlog.sim/Program.cs` and `catlog.integration.tests/PlaceholderTests.cs` are both replaced by real code, so the WP0 marker type has no remaining referent. The assembly guard is anchored on `EventEnvelope` and is unaffected.

### MOD-024 — Server gap found by driving the real seams: `POST /admin/issue` does not reload the handle directory

*Accepted · 2026-08-07 · WP7.*

A credential minted by `catlogctl issue` against a *running* catlogd ships fine and folds fine, and then every board row for it is invisible — the read side resolves `player_id → handle` through the in-memory `directory` (§5.4), which is loaded at start and reloaded only by the routes that WP3/WP4 wired (`/admin/seed` does; `/admin/issue` does not). Symptom: `GET /v1/players/<handle>` 404s and `visibleRows` drops the player as "holding no handle yet" even though `player_stat` has rows. **No Go was changed** (WP3 owns identity and was working in the tree concurrently). `ReadApiClient.EnsureHandleVisible` works around it by calling `POST /admin/seed`, which is idempotent, reloads the directory and drains the projector; it prints a one-line note when it fires. **WP3 should call `directory.Reload` from the handle-claim path**, at which point that workaround can be deleted.

### MOD-025 — The deterministic board-read primitive is `GET /admin/stats` until `projector.lag_seq == 0` AND `projector.checkpoint_seq == events.max_seq`

*Accepted · 2026-08-07 · WP7.*

Nothing in `catlog.sim` sleeps-and-hopes. Both halves are load-bearing: on an empty log the lag is 0 because there is nothing to do, and a checkpoint that equals the head cannot on its own distinguish "caught up" from "the fold loop is not running". The ingest handler answers 200 only after the write transaction has committed, so "shipped" ⇒ "in `events.max_seq`", which is what makes the pair sufficient.

### MOD-026 — Scenario assertions are baseline-relative, not absolute

*Accepted · 2026-08-07 · WP7.*

Scenario assertions are baseline-relative, not absolute: a record board must end at `max(baseline, expected)` and a counter board at `baseline + delta`, with the baseline captured (after a projector wait) before the scenario runs. §7.3 words them absolutely (`biggest_lithobrake_survived=62`), which only ever passes once against a virgin database. Relative assertions are equally strict — a wrong value is still wrong — and they make every scenario re-runnable against a server that already has data. Verified: three consecutive passes of `hop-lithobrake`, `cheater` and `soak` against one server, `cheater`'s rebuild included, all green.

### MOD-027 — The simulator pumps `BatchShipper.ShipOnceAsync` synchronously rather than running `BatchShipper.RunAsync` as a background task

*Accepted · 2026-08-07 · WP7.*

Cancelling `RunAsync` mid-request would leave a batch the server stored and the outbox still holds — a re-ship, which the server correctly dedups, which would turn `soak`'s "dedup is 0" assertion into a statement about a race. The recovery table underneath is identical either way, and `catlog.lib.tests` already covers `RunAsync` on a virtual clock. WP8's game worker should use `RunAsync`; the simulator is the exception and says so at the class.

### MOD-028 — The simulator ships full batches (`PendingTrigger = BatchEventCap = 500`, age trigger disabled), unlike the mod's §7.2 defaults (64 pending / 15 s)

*Accepted · 2026-08-07 · WP7.*

A real session produces events over minutes; a scenario produces the same events in milliseconds, so a 64-event trigger would fire ~30 batches straight into §4.3's token bucket (1 batch / 2 s, burst 5) and spend the run in backoff. At 500 the 1 960-event `soak` ships in 4 batches — inside a real player's rate budget, and with the shipped server defaults, no rate-limit override.

### MOD-029 — Within a simulated frame, signals are processed before passive telemetry

*Accepted · 2026-08-07 · WP7.*

The game thread raises signals *during* a frame and samples at the end of it, so this is the honest order; it also puts `flight.started` ahead of that flight's first `telemetry.window` in the outbox, which §4.3 then preserves onto the wire. `EndFrame` is always called last, so the `FrameBoundarySignal` that resolves impacts is in the same drain.

### MOD-030 — `cheater` covers both flag orderings, because only one of them tests D22

*Accepted · 2026-08-07 · WP7.*

Flight `cheat-early` is flagged before its 9 000 m/s impact and three stagings, so the incremental fold already excludes them — the ordinary case. Flight `cheat-late` is flagged ~60 s *after* its 8 000 m/s impact and two stagings, which therefore **do** score incrementally; only `POST /admin/projections/rebuild` removes them. The scenario asserts the wrong-but-expected intermediate state (board reads 8 000, stagings +2) and then the healed state (both back to baseline). A scenario that only ever flagged first would pass with the rebuild path completely broken.

### MOD-031 — The §7.5 revocation case sets `credential.revoked_at` directly instead of calling `catlogctl`

*Accepted · 2026-08-07 · WP7.*

The verbs that revoke — `ban`, `purge`, `denylist` — are WP3's and still report "not yet implemented", and there is no admin route for it. The test stops the server, writes the same column `store.Events.RevokeCredential` writes (via `Microsoft.Data.Sqlite`) and restarts, so `authz.DenyList.LoadFrom` picks it up at start. Two facts this depends on, both verified here: **Turso writes SQLite-format files that `Microsoft.Data.Sqlite` reads and writes**, and the server's exclusive whole-file lock (WP1) makes stopping it a hard prerequisite rather than politeness — the stop is also what checkpoints the WAL into the main file. When WP3 lands, two lines become a `catlogctl` invocation and nothing else changes.

### MOD-032 — The oversize case runs its own server with `CATLOG_INGEST_MAX_EVENTS=60` and ignores `CATLOG_SERVER_URL`

*Accepted · 2026-08-07 · WP7.*

With §4.3's shipped 2 000-event cap the mod's *local* 1 MiB pre-check fires first and halves without a round trip (the WP6 decision (e)), so no server `413` is ever seen. Constraining the server is the only way to make the ladder real: 500 → 250 → 125 → 62 → 50, four honest `413`s and then a batch that fits. The other fixtures do honour `CATLOG_SERVER_URL` as §7.5 requires.

### MOD-033 — Both integration fixtures raise the rate limiter

*Accepted · 2026-08-07 · WP7.*

Both integration fixtures raise the rate limiter (`burst 200`, `100/s`) because they deliberately ship bursts — the halving ladder alone costs eight requests. §4.3's real bucket is covered by `server/integration`'s `TestIngestRateLimit`, and the simulator runs against the shipped defaults, so nothing is left unexercised.

### MOD-034 — Fixed a 1-in-256 flake in `catlog.lib.tests`

*Accepted · 2026-08-07 · WP7.*

Fixed a 1-in-256 flake in `catlog.lib.tests` (WP6's `JwsTests.SignatureIsSixtyFourBytesOfRawRs`), which asserted `signature[0] != 0x30` to rule out a DER SEQUENCE tag — but `r`'s leading byte is uniformly random, so one run in 256 failed. It now asks the BCL to interpret the bytes explicitly: `VerifyData(..., IeeeP1363FixedFieldConcatenation)` must succeed and `VerifyData(..., Rfc3279DerSequence)` must fail. Deterministic, and a more direct statement of the §4.5 claim.

### MOD-035 — `IScenario` carries `Summary` and `Asserts` beyond §7.3's `Name`/`Steps`/`Assert`

*Accepted · 2026-08-07 · WP7.*

`IScenario` carries `Summary` and `Asserts` beyond §7.3's `Name`/`Steps`/`Assert`, so `--list` can say what each scenario plays out and what it checks without a second table to keep in sync. `ReadApiClient` likewise carries the run's `RunSummary`, which is how `soak` asserts "`events.total` advanced by exactly what the pipeline produced" without hard-coding a number that every edit to the scenario would invalidate.

### MOD-036 — `--speed <n>` is sim seconds per wall second, and `0` (the default) runs unpaced

*Accepted · 2026-08-07 · WP7.*

`--speed <n>` is sim seconds per wall second, and `0` (the default) runs unpaced. §7.3 shows `--speed 100` without fixing the unit; unpaced has to be the default because the assertions do not care about pacing and a paced 30-minute `soak` would take 18 s at 100×. `make sim` gained `ASSERT=1`, `SPEED=`, `ADMIN_URL=`, and lists the catalogue when `SCENARIO` is empty.

### MOD-037 — Scenario body constants

*Accepted · 2026-08-07 · WP7.*

Scenario body constants (`kerbin` atmosphere 70 km, `mun` airless, `duna` 50 km) are the simulator's, not the game's — `docs/ksa-integration.md` documents how to *read* `GetAtmosphereReference()?.Physical.Height`, not what it returns per body. They are chosen to exercise the §7.2 rules explicitly: `vehicle.atmosphere` at ±2 % of the height, `vehicle.orbit: achieved` at `pe_alt > height + 1 km`. Body **names** match `internal/seed` (`kerbin`/`mun`/`duna`) so the demo dataset and the simulated dataset share a vocabulary.

### MOD-038 — `make test-integration` now depends on `server-build`

*Accepted · 2026-08-07 · WP7.*

`make test-integration` now depends on `server-build`: the mod leg spawns `server/bin/catlogd` (the Go leg builds its own copy into a temp dir, so it never needed it). Both legs are green — `server/integration` 10.7 s, `catlog.integration.tests` 6 tests.

### MOD-039 — `mod/catlog/catlog.csproj` joined `mod/catlog.slnx`, resolving deviation A2

*Accepted · 2026-08-07 · WP8.*

`mod/catlog/catlog.csproj` joined `mod/catlog.slnx`, resolving deviation A2. `Directory.Build.props` finds the KSA reference assemblies via `KSA_DLL_DIR`, then a sibling `ksa-game-assemblies` checkout, then the per-OS install; on this machine that resolves to `/Users/asherwin/repos/meow-sci/ksa-game-assemblies/current/dll/` and `make mod-build` compiles all five projects. **The consequence is deliberate and worth stating plainly: a machine with none of those three now cannot build the solution at all.** The alternative — leaving the game project out — means the one project that couples to KSA is the one project CI never compiles, and a decomp drop would break it silently until someone opened the game. The `<Reference>` items stay `Condition="Exists(...)"` so the failure is a legible `CS0246 type or namespace 'KSA' not found`, not an MSBuild reference error.

### MOD-040 — The mod opens two `OutboxDb` handles on the same file — one for the worker, one for the shipper

*Accepted · 2026-08-07 · WP8.*

The mod opens two `OutboxDb` handles on the same file — one for the worker, one for the shipper. `OutboxDb` holds a single `SqliteConnection`, which is not thread-safe, and WP8 is the first caller to run the detector loop and `BatchShipper.RunAsync` concurrently (`catlog.sim` pumps both from one thread). Two connections is precisely the case `OutboxDb.Open`'s `busy_timeout=3000` pragma already documents ("covers the shipper and the worker racing on the same file"), and WAL keeps appends and deletes from blocking each other's reads. Chosen over adding a lock inside `OutboxDb` because that would serialize the sim and the tests for a hazard neither has. **Pruning is the worker's alone** — the shipper is constructed with `OutboxCapBytes: 0` — so exactly one process deletes rows for size, whether or not a shipper exists (without a credential there is no shipper, and the outbox would otherwise grow unbounded).

### MOD-041 — The catlog frame boundary is raised in `[StarMapBeforeGui]`, not in a `Universe.ApplyVehicleSolvers` postfix — and the choice does not affect correctness

*Accepted · 2026-08-07 · WP8.*

The catlog frame boundary is raised in `[StarMapBeforeGui]`, not in a `Universe.ApplyVehicleSolvers` postfix — and the choice does not affect correctness. `Program.PrepareFrame` runs `Universe.ApplyVehicleSolvers()` (`Program.cs:1912`) and `InputEvents.ApplyInputEvents()` (`Program.cs:1918`) back to back with no mod hook between them, so a frame's impacts, its physics destructions and its manual destroys/recovers all land inside one uninterrupted stretch. Whichever side of that stretch `[StarMapBeforeGui]` falls on, an impact and the destruction that answers it are never split across two catlog frames, which is the property `ksa-integration.md` §3 actually needs. `[StarMapBeforeGui]` is preferred because it is the hook StarMap guarantees once per frame *with `dt`* (the `SampleClock` needs it) and because it is downstream of the input-apply pass, satisfying WP7's "call `EndFrame` after solver-apply **and** input-apply" literally. The `ApplyVehicleSolvers` postfix is still installed, as the heartbeat the status window reports (`Patcher.SolverBatches`) — the first thing to check when nothing is being recorded.

### MOD-042 — `flight.flagged: teleport` hooks `InputEvents.TeleportInputData.Apply()`, not `Vehicle.Teleport`

*Accepted · 2026-08-07 · WP8.*

B7 offers either. `Vehicle.Teleport` is the wrong one: it has three callers and only one is the player cheating — `EVADoor.cs:158` teleports a kitten as part of **normal EVA egress**, and `VehicleEditor.cs:2193` teleports the newly split vehicle on an editor decouple. Flagging there would taint every EVA and every editor split, i.e. quietly exclude ordinary play from every board. `TeleportInputData.Apply` is the player-command path and only that; its two producers are `Vehicle.TeleportToLocation` (`Vehicle.cs:3920`, the console/right-click teleport) and the Set Orbit debug window (`Vehicle.cs:4724`), which also answers B7's warning that `TeleportToLocation` alone misses the console/UI paths. It is a **struct** instance method, so the prefix takes `ref InputEvents.TeleportInputData __instance`.

### MOD-043 — `refuel` / `resource_edit` hook `Vehicle.RefillConsumables` / `DepleteConsumables`, and that covers the console too

*Accepted · 2026-08-07 · WP8.*

Verified in the decomp: `Vehicle.RefillConsumables()` has exactly one caller (`InputEvents.cs:547`, the terminal `refill`) and `DepleteConsumables()` exactly one (`InputEvents.cs:561`, terminal `empty`). The editor's and the EVA door's refills go through `PartTree.RefillConsumables` instead (`VehicleEditingSpace.cs:171`, `EVADoor.cs:178`) and are correctly **not** flagged. So no patch on the `InputEvents.VehicleResourcesChangeData` struct is needed — one fewer nested-struct patch to maintain.

### MOD-044 — `flight.started` comes from polling, not from `CelestialSystem.Register`

*Accepted · 2026-08-07 · WP8.*

`flight.started` comes from polling, not from `CelestialSystem.Register`. `Register` is called from the `Astronomical` constructor (`Astronomical.cs:159`), where the vehicle is half-built and `FlightPlan.Patches` is still empty — every read would hit the B6 throw. Instead a vehicle is registered the first time catlog *sees* it, and `PolledSignals.Track` is called both by the sample pass and by every vehicle-scoped Harmony patch body **before** the signal it is about to raise. That closes the hole where a vehicle created and destroyed inside one 0.5 s sample interval would emit a `vehicle.rud` and a `flight.ended` against a flight ULID with no `flight.started` for the server to join against. It also makes `CelestialSystem.Rename` a non-event to patch.

### MOD-045 — `Vehicle.Dispose(bool)` is the single `flight.ended` emitter

*Accepted · 2026-08-07 · WP8.*

`Vehicle.Dispose(bool)` is the single `flight.ended` emitter (B12: the true removal choke point, `Vehicle.cs:3510`, covering destroy / dock-consume / EVA-board / shutdown). The *reason* is decided by two intent sets the earlier patches fill: `Vehicle.Recover()` marks the id recovering, `Universe.DestroyVehicleFromEvent` and `Vehicle.KillCrew` mark it destroying, and anything else is `despawned`. A flight is only closed if catlog had opened one (`PolledSignals.Forget` returns whether the vehicle was tracked), so a vehicle catlog never saw cannot produce a `flight.ended` with no `flight.started`. A `KittenEva`'s disposal additionally emits `kitten.eva_end`, because its `Vehicle.Id` *is* the roster name. A **silent-removal safety net** in the poll ends any still-tracked vehicle that vanished from `Universe.CurrentSystem.All` without a dispose (a rename does exactly this) as `despawned`, rather than leaking a flight that never closes.

### MOD-046 — `kitten.kia` is emitted by roster diff, with the context supplied by the `KillCrew` patch

*Accepted · 2026-08-07 · WP8.*

`kitten.kia` is emitted by roster diff, with the context supplied by the `KillCrew` patch. `Kia = true` is written in exactly one place, reachable only from `Vehicle.KillCrew()`, whose only caller is the `!Recovered` branch of the manual-destroy path (§4). Emitting the event from the diff rather than from `KillCrew` gives one emit path (no dedup problem) and still catches a KIA arriving by any route a future build adds; `KillCrew` merely timestamps player intent, and a diff within 2 sim seconds of it is labelled `manual_destroy` instead of `unknown`. The **first** roster read is a baseline that emits nothing, so loading a save that already contains KIA kittens does not replay their deaths.

### MOD-047 — `engine.ignition` / `shutdown` / `flameout` are whole-vehicle, not per-engine

*Accepted · 2026-08-07 · WP8.*

`engine.ignition` / `shutdown` / `flameout` are whole-vehicle, not per-engine. `PartTree` exposes named `StateList`s for nozzles, cores, gimbals and the rest, but **not** for `EngineController`, so the parallel `EngineControllerState` span (which carries `IsPropellantAvailable`) is only reachable through `ModuleStateful<...>.TryGetFrom(Parts.States,...)` — a four-type-argument generic the mod would have to name explicitly and re-verify every build. The game already publishes the two globals it needs: `Vehicle.IsAnyEngineActive()` (`Vehicle.cs:6030`) and `Vehicle.IsAnyEnginePropellantAvailable()` (`:6131`). Edges on those two produce the three events, with `engine`/`count` filled from `Parts.Modules.Get<EngineController>()`. Consequence: a vehicle with two engine groups that shuts one down reports nothing until the last one stops. Recorded rather than hidden — per-engine granularity is a later change with a real cost.

### MOD-048 — B3's flameout predicate is implemented as stated and nothing more

*Accepted · 2026-08-07 · WP8.*

There is no flameout concept in the game; `IsActive && !IsPropellantAvailable` at the whole-vehicle level is what is shipped, and it is emitted on the falling edge of propellant availability while engines are active.

### MOD-049 — `flight.flagged: console` covers the terminal `destroy` command only

*Accepted · 2026-08-07 · WP8.*

`flight.flagged: console` covers the terminal `destroy` command only. `Universe.Destroy(string)` (`Universe.cs:1107`, `[TerminalAction("destroy", …)]`) is patched. The other terminal verbs reach the game through `RefillConsumables`/`DepleteConsumables`, which are already flagged as `refuel`/`resource_edit` — a more specific label, and `FlightTracker` dedupes per (flight, flag) so a double flag would be harmless anyway. There is no general "a console command was typed" hook and none was invented.

### MOD-050 — `flight.flagged: tuning` is session-wide (`vehicle_id = null`), checked every sample tick until it fires once

*Accepted · 2026-08-07 · WP8.*

B9: `KittenLocomotionTuning.Current` is a mutable public static that the shipped "Kitten Locomotion Tuning" debug window live-edits by ref, and `TumbleSpeedGate` is the sole classifier for `kitten.tumble`. Stock is `6.5f` (raised from 5.5 in r5131) and lives in `VehicleTelemetry.StockTumbleSpeedGate` with a `[KsaAnchor]` on the reader; any difference taints every flight in the session, including ones started after the flag was raised (`EventPipeline` already replays session flags onto new flights).

### MOD-051 — No `HotkeyGuard`

*Accepted · 2026-08-07 · WP8.*

The `imgui` skill requires it of every top-level mod, but the canonical implementation lives in `unscience/ksa-abstractions.lib`, which catlog does not depend on, and the guard exists solely to stop game hotkeys firing while an ImGui **text input** has focus. The status window has no text input by design — a checkbox and read-only rows — so there is nothing to guard. If an editable field is ever added, the ~30-line `GameSettings.OnKeyAll` prefix from the skill must be added with it.

### MOD-052 — F10 toggles the status window

*Accepted · 2026-08-07 · WP8.*

F3 and shift-F3 are the game's kitten-roster hotkeys (r5118) and F11 is gatOS's; F10 is free in both. The window is hidden by default so the mod is invisible until asked for.

### MOD-053 — Runtime state lives in `~/Documents/My Games/Kitten Space Agency/mods/catlog/`, never beside the installed DLLs

*Accepted · 2026-08-07 · WP8.*

Runtime state lives in `~/Documents/My Games/Kitten Space Agency/mods/catlog/`, never beside the installed DLLs (`catlog.toml`, `outbox.db`, `install-id.txt`, and the default `catlog-credential.json` location). A mod update replaces the install folder; the player's spool, settings and credential must survive it. The deploy target excludes `catlog.toml` and `catlog-credential.json` from its stale-file wipe for the Windows case where the two directories coincide. The **install ULID** is minted into `install-id.txt` on first run and is the salt that makes `kid` values incomparable across installs; if it cannot be persisted the mod runs with a session-only id and says so, because a degraded kitten identity is not a reason to refuse to record anything.

### MOD-054 — `session.started.game_build` reads `VersionInfo.Current.VersionString`

*Accepted · 2026-08-07 · WP8.*

`session.started.game_build` reads `VersionInfo.Current.VersionString` (`VersionInfo.cs:115`, formatted `v{Major}.{Minor}.{Build}.{Revision}{Suffix}` at `:143`) with the leading `v` stripped, cached after the first read, `"unknown"` if it throws. `mod_ver` comes from the assembly's `AssemblyInformationalVersion`, not a literal, so it cannot drift from the csproj. Neither is hard-coded — a batch stamped with the wrong build makes every cross-build comparison in §5 a lie.

### MOD-055 — Every KSA read is in `VehicleTelemetry.cs` and every one carries a `[KsaAnchor]`

*Accepted · 2026-08-07 · WP8.*

Every KSA read is in `VehicleTelemetry.cs` and every one carries a `[KsaAnchor]` with the `file:line` it was verified against and the units gotcha it embodies — `Orbit.Inclination` is radians, `Apoapsis`/`Periapsis` are radii from body centre, `Situation` is a packed bitfield, `StructuralLoad` is all-zero off full physics, `Vehicle.Crew` is seats not occupants, `FastestSpeed` is ecliptic-frame. `Patcher.cs` carries the `ksa-integration.md` §2 table row above each patch. The three `ChurnRisk.High` anchors are the surfaces that are **one build old** (`StructuralLoad`, the whole kitten-locomotion subsystem, `KittenLocomotionTuning`) — those are the first things to re-verify on the next decomp drop.

### MOD-056 — `peak_g` / `max_q_pa` are `null`, never `0`, using `MaxGLoad`/`MaxDynamicPressure` as the "was this struct written" discriminator

*Accepted · 2026-08-07 · WP8.*

B10 says an all-zero `StructuralLoad` means *no data this step*. `MaxGLoad` is the computed structural limit, floored at 5 by `VehicleStructuralLimits.EffectiveMaxGLoad` and written beside `PeakGLoad` at `VehicleUpdateTask.cs:492-497`, and `MaxDynamicPressure` is the hard-coded 200 kPa beside it — so `> 0` is an exact test for "written", crisper than sniffing the peaks themselves (a genuine 0 g reading under full physics is possible and must not be discarded).

### MOD-057 — Defect fix in `catlog.lib` (1/4): `BatchShipper.ConsecutiveFailures` is now advanced inside `ShipOnceAsync`

*Accepted · 2026-08-07 · WP8.*

Defect fix in `catlog.lib` (1/4): `BatchShipper.ConsecutiveFailures` is now advanced inside `ShipOnceAsync`, at the single `Record` choke point every return path passes through, instead of only by `RunAsync`. A caller that pumps `ShipOnceAsync` itself — `catlog.sim`, the integration tests, and WP8's bounded unload drain — read a permanent zero and, if it trusted it as a retry ceiling, would loop forever against a dead server. `RunAsync` now draws its backoff rung from `ConsecutiveFailures - 1` since the ladder has already advanced by the time an attempt returns. `StreamForked`/`TooLarge`/`Fatal` neither advance nor reset it: they are parameter changes, not transport faults, and resetting on a 413 would silently restart the backoff the next real failure is owed. Tests: `ShipOnceAsync_AdvancesConsecutiveFailures`, `ShipOnceAsync_ResetsConsecutiveFailuresOnSuccessAndOnAnEmptyOutbox`, `TooLarge_LeavesTheRetryLadderWhereItIs`.

### MOD-058 — Defect fix in `catlog.lib` (2/4): `ShipAttempt` gained `ServerAccepted` and `ServerDeduped`

*Accepted · 2026-08-07 · WP8.*

Defect fix in `catlog.lib` (2/4): `ShipAttempt` gained `ServerAccepted` and `ServerDeduped`, parsed from the §4.4 `200` body. `EventsShipped` is unchanged and now documented as what it always was — the *local* batch size — because `catlog.sim`'s scenario accounting depends on it. The two new fields are `int?`: "the server stored nothing" and "the server did not tell us" are different facts and the status window must not present the second as the first. It renders `7 accepted, 3 deduped` when the server said so and `64 sent (the server reported no counts)` when it did not. Tests: `AcceptedBatch_ReportsTheServersAcceptedAndDedupedCounts`, `Replay_ReportsZeroAcceptedAndTheDedupedCount`, `MissingCounts_AreNullRatherThanZero`.

### MOD-059 — Defect fix in `catlog.lib` (3/4): `EventPipeline.Flush` peeks the flight instead of minting one

*Accepted · 2026-08-07 · WP8.*

It drained the correlator through `EventFactory.FromResolvedImpact`, which calls `Tracker.FlightFor(...)` — so an impact resolved at session flush, after its flight had already ended, landed on a freshly minted flight ULID with no `flight.started`: a phantom the server's join can never resolve. `Flush` now uses `Tracker.PeekFlight` and drops-and-logs when there is none. `EventFactory.FromResolvedImpact` gained an overload taking an explicit flight id, so the minting behaviour is still available where it is correct. Tests: `Flush_DropsAnImpactWhoseFlightAlreadyEnded`, `Flush_StillEmitsAnImpactForALiveFlight`.

### MOD-060 — Defect fix in `catlog.lib` (4/4), found while wiring the mod: a manual destroy did not flip `survived`, and a same-frame flight end minted a phantom flight

*Accepted · 2026-08-07 · WP8.*

Defect fix in `catlog.lib` (4/4), found while wiring the mod: a manual destroy did not flip `survived`, and a same-frame flight end minted a phantom flight. `ImpactCorrelator`'s one-frame hold exists (its own XML doc says so) precisely so a manual destroy landing in the game's input-apply pass can still flip the verdict — but nothing ever told the correlator about one: only `RudSignal` called `Destroyed`, and a manual destroy produces no RUD. A player could scuttle after every hard landing and bank a free "survived" record. `EventPipeline.EndFlight` now calls `_correlator.Destroyed` when the reason is `destroyed`. In fixing that it became clear the hold had a second problem on the *common* path: an impact and the destruction it caused land in the same frame every single time, `EndFlight` retires the flight id, and the next frame boundary resolved the impact against a re-minted flight. `EndFlight` therefore also resolves that vehicle's outstanding impacts immediately — the verdict cannot change once the flight is over — via a new `ImpactCorrelator.DrainFor(vehicleId)`. Tests: `ManualDestroyAfterAnImpact_StillMarksItNotSurvived`, `RecoveryAfterAnImpact_LeavesItSurvived`, `DrainFor_TakesOneVehiclesImpactsAndLeavesTheRest`, `DrainFor_AnUnknownVehicle_IsEmptyAndHarmless`. `make mod-test`: 368 passing, up from 356.

### MOD-061 — Known limitation: `CelestialSystem.Rename` splits a flight in two

*Accepted · 2026-08-07 · WP8.*

A rename is deregister → rename → register with no dispose, so the old id disappears from telemetry (the silent-removal net closes it as `despawned`) and the new id is discovered as a new vehicle with its own `flight.started`. `Vehicle.LaunchGameTime` survives the rename, so `FlightTracker` would happily continue the flight if the two were keyed together — but the tracker is keyed by `(vehicle_id, launch_game_time)` and the id changed. Left as-is: renaming mid-flight is rare, the result is two honest flights rather than a corrupt one, and the fix needs a rename-aware key that WP8 should not invent unilaterally.

### MOD-062 — Ship cadence retuned: the age trigger is the normal path at 60 s, and the count trigger is a safety valve at 500, not the usual reason to ship

*Accepted · 2026-08-07 · WP6/WP8.*

Ship cadence retuned: the age trigger is the normal path at 60 s, and the count trigger is a safety valve at 500, not the usual reason to ship. §7.2 specified `≥64 pending or oldest ≥15 s`, which made the count trigger the normal path and turned a bulk telemetry pump into a near-live feed. Owner intent is one bulk shipment per minute. `Wire.ShipAgeTriggerSeconds` 15 → **60**; `Wire.ShipPendingTrigger` 64 → **500**. 500 is sized against a real minute: passive `telemetry.window` is one per active vehicle per 30 s, so a busy two-dozen-vehicle save emits ~48/min, and an eventful launch's discrete events add a few dozen more — a busy minute is ≤ ~150 events, so the valve sits >3× above ordinary play and only opens on a genuine burst or a backlog drain. It is also exactly `DefaultBatchEventCap`, so when it opens there is precisely one full batch to send rather than a partial one. Measured headroom at 500 against §4.3: **25%** of the 2000-event cap; **90.5 KiB** Brotli (8.8% of the 1 MiB cap) for worst-case incompressible `telemetry.window` lines — real telemetry compresses far better; **0.31 MiB** decompressed (3.9% of the 8 MiB cap). Against the token bucket (1 batch / 2 s, burst 5), one batch a minute is **3%** of the sustained allowance. `catlog.sim` is unaffected: WP7 already pins `PendingTrigger = BatchEventCap` and disables the age trigger outright for scenario determinism (comment expanded to say why that is deliberately independent of the shipped defaults). Tests: `Defaults_MakeTheAgeTriggerTheNormalPath`, `ShouldShip_ABusyMinuteFiresOnAgeNotOnCount`; the two `RunAsync_*` loop tests now pin `PendingTrigger: 1` so they measure the backoff ladder rather than the cadence.

### MOD-063 — Two new `catlog.toml` keys: `ship_interval_s` (default 60, clamped [1, 3600]) and `ship_max_pending` (default 500, clamped [50, 2000])

*Accepted · 2026-08-07 · WP6/WP8.*

A player who wants a tighter feed — or a test that cannot wait a minute — needs the cadence to be data, not a constant. Same rules as the rest of `ModConfig`: snake_case via Tomlyn's `SnakeCaseLower` policy, load-never-throws, clamp-don't-reject with a warning naming the TOML key. `CatlogRuntime` passes both into `ShipperOptions`. Tests: `ShipIntervalIsClamped`, `ShipMaxPendingIsClamped`, `CadenceKnobsFlowIntoShipperOptions`, plus the round-trip and first-run cases in `ModConfigTests`.

### MOD-064 — Finding, and the minimal fix: the stream chain could turn a safe "retry when in doubt" into a `409 stream_fork`, and the client — not the protocol — is what changed

*Accepted · 2026-08-07 · WP2/WP6.*

Verification order puts batch replay (step 11) before the stream check (step 12), which is correct, but the short-circuit only fires on a batch id the server has already stored. A client that times out has *not* seen a response, so it cannot advance `seq`; if it also mints a fresh `jti` for the resend it misses step 11 and lands on step 12, where its unchanged `seq` reads as a reused one — `409` for a request whose events were already safe and whose duplicate would have been harmless. That conflicts with the stated hard requirement. **The protocol was not changed.** The mod now mints its batch id per *body* rather than per attempt (`BatchShipper.BatchIdFor`, keyed on `bh`, persisted in `shipper_state` as `pending_batch_id`/`pending_bh`), so a resend of unchanged bytes carries the id the server already knows and gets a clean `200 replay`. Keying on the body hash is load-bearing, not incidental: if a prune or a `413` halving changes what the batch contains, the hash changes and a new id is minted — reusing it would let a replay short-circuit retire outbox rows the server never saw. Persisting it extends the same reasoning across a game crash mid-ship. The residual for third-party clients that ignore the rule is documented rather than hidden: `409` costs a round trip and an abandoned chain, never a row, because the `(player_id, event_id)` index absorbs the resend. Tests: `RetryingAFailedBatch_ReusesTheBatchId`, `AResizedBatch_GetsAFreshBatchId`, `TheBatchIdSurvivesAShipperRestart`, `AnAcceptedBatch_RetiresItsBatchId`, and Go-side `TestRetryWithANewBatchID` (all three shapes, including the 409 one, pinned so it cannot change silently). `ClockSkew_ResyncsFromTheDateHeaderAndRetriesOnce` previously asserted the resign minted a *new* batch id; that assertion was an anti-property and now asserts the opposite.

### MOD-065 — A hard, non-overridable minimum of 30 s between requests to the ingest endpoint, enforced in mod code and unreachable from `catlog.toml`

*Accepted · 2026-08-07 · owner decision.*

Owner decision: *"Implement a hard minimum reporting cadence in the mod code that cannot be overridden in the TOML exposed to players, to avoid abuse from a simple TOML config file. I don't want reporting to the server to EVER exceed once per 30 seconds, regardless of number of buffered events or what cadence is set in the TOML. This should be a hard-coded minimum."* **Threat model, stated so the scope is not argued about later:** the attacker is a player editing a text file. `catlog.toml` sits in the mod folder and every key in it is attacker-controlled by definition, so `ship_interval_s = 1` was a one-line edit that turned a stock install into a firehose. Someone who recompiles the assembly can do anything and always will be able to; that is explicitly *not* what this defends against. The floor exists so the **easy** path is closed. **The constant** is `Wire.MinShipIntervalSeconds = 30.0` in `mod/catlog.lib/Wire.cs`, a `const` (pinned as `IsLiteral` by a test, so it is baked into every call site and cannot be assigned at run time), documented in place with the threat model and the reasoning for the number: it is half of `ShipAgeTriggerSeconds`, so it never binds during ordinary play; it equals `TelemetryWindowSeconds`, so a floored batch is at most one telemetry window behind; and it is 6.7% of §4.3's per-credential token bucket (1 batch / 2 s), which is the *server's* backstop against a hostile client and a different mechanism entirely — this is the client-side promise that a stock install never approaches it. **Enforced in three places, deliberately, because clamping the config alone only closes the path you thought of.** (1) `ModConfig.Normalize` clamps `ship_interval_s` up to 30 — the old lower bound of 1 s was the hole — so the number the player reads in their own config is the number the mod honours. (2) `BatchShipper.ShouldShip` reports "not due" inside the window, checked **before** either trigger, so the `ship_max_pending` count trigger cannot open it early: 10 000 buffered events still ship one batch per window and the rest stay in the outbox, which is what the outbox is for. (3) `BatchShipper.SendAsync` refuses at the point of transmission, immediately before the POST, stamping the window as it goes. Only (3) is the guarantee; (1) and (2) are courtesies. (3) is what covers a hand-built `ShipperOptions`, every recovery retry, and any caller written next year. **It refuses rather than waits** — a new `ShipOutcome.Throttled`, no hidden sleep — because a concealed 30 s block inside a method the game thread can reach is a shutdown hang; every wait is now the caller's explicit choice, taken on the injected clock.

### MOD-066 — Retries are floored too, on the literal reading of the requirement, and the cost is recovery latency only

*Accepted · 2026-08-07 · owner decision.*

Retries are floored too, on the literal reading of the requirement, and the cost is recovery latency only. `409 stream_fork` and `413 too_large` used to resend immediately and the `429`/`5xx` backoff ladder used to start at 1 s. All of them now wait: the shipper takes `max(BackoffPolicy.Delay(n), 30 s)` at the point of waiting, and floors `Retry-After` the same way — a server asking to be retried sooner than 30 s does not get its way, one asking for longer does. **`BackoffPolicy` itself is untouched** and `BackoffPolicyTests` still pins it to 1, 2, 4, 8 s: it is the §4.5.3 schedule the contract publishes, and baking a floor into it would make the published ladder a fiction. The practical consequence, stated plainly: a `413` converging 500 → 50 by halving takes **four windows, two minutes**, instead of four back-to-back round trips. Nothing here is latency-sensitive and telemetry is loss-tolerant, so that costs nothing real. No case was found where the literal reading breaks correctness rather than merely slowing recovery — every retry is idempotent by construction (the batch id is minted per *body*, so a deferred resend carries the id the server already knows), and the outbox is durable, so waiting never loses an event. **`401 clock_skew` was the one that needed restructuring:** it used to recurse and re-POST inside the same window. It now learns and persists the offset, returns a new `ShipOutcome.ClockResynced`, and the *same* batch re-signs with the corrected `iat` on the next attempt — unchanged body, seq and batch id, so it is the same idempotent resend it always was, one window later. A skew that never resolves therefore costs one request per window forever rather than escalating a backoff, which is exactly the floor and not a hot loop.

### MOD-067 — The floor is measured against the injected `IShipperClock`, and that seam is unreachable from anything a player can edit

*Accepted · 2026-08-07 · owner decision.*

A wall-clock floor would stall `catlog.sim`, which compresses 30 minutes of play into ~0.2 s and ships several batches doing it, and `make test-integration` depends on it. The seam already existed so the retry ladder could be unit-tested without real waiting, and it is the right place: the unit tests and the simulator inject a virtual clock and prove a 30-second property in milliseconds, while `mod/catlog` — the assembly a player installs — constructs `BatchShipper` in exactly one place (`CatlogRuntime.Create`) and **omits the clock argument**, which pins it to `SystemShipperClock` and the real 30 s. The safe thing is the parameter's default, so a call site cannot get it wrong by omission. Three tests hold that shut: `OmittingTheClockYieldsTheRealClock`, and two source-level guards over `mod/catlog/**/*.cs` (comments stripped, so the guard reads code rather than prose) asserting that the shipped mod never names `IShipperClock`/`ShipperClock`/`clock:`, and reads no environment variable and no command line at all. Every knob in the shipped mod is `catlog.toml`, and `catlog.toml` cannot express the floor: it is not a property of `ModConfig`, unknown keys are dropped by Tomlyn, and the serialized file is asserted to contain no `floor`/`min_ship_interval` key. `catlog.sim` gets `SimShipperClock` (a separate console executable never shipped to players) and the integration tests get `AdvanceableClock`; both are anchored on real time so the proof's `iat` stays inside §4.3's ±300 s window while they wind forward.

### MOD-068 — The floor's "last request" timestamp is persisted, in `shipper_state.last_request_ms` next to `sid`/`seq`/`last_bh`/`clock_offset_ms`

*Accepted · 2026-08-07 · owner decision.*

In memory it would have handed a config-editing player a general bypass for ordinary shipping if they were willing to restart the game; on disk, a fresh session reads the stamp its predecessor left and waits out the remainder of the window before its first request (`TheFloorSurvivesAProcessRestart` relaunches five times inside one window and gets nothing out). Deleting `outbox.db` resets it, but deleting `outbox.db` also deletes the events, so there is nothing left to flood with. **The clock-skew interaction was considered and is why the comparison uses raw `IShipperClock.UtcNow` and never `BatchShipper.Now`:** the server-learned offset is attacker-adjacent input and a hostile `Date` header must not be able to buy a shorter window. A backwards jump of the system clock restarts the window from "now" rather than producing a huge remaining wait, so it costs one window and heals itself instead of refusing to ship until the calendar catches up — and it can never buy a *shorter* wait than the floor. A forwards jump can shorten one window; that is a system-clock attack rather than a TOML edit, it is outside the stated threat model, and it breaks the player's own proof `iat` in the process.

### MOD-069 — `FinalShip` — the unload-time courtesy flush — is exactly one attempt, hard-bounded, and the single exemption from the floor

*Accepted · 2026-08-07 · owner decision.*

Owner decisions, in order: *"FinalShip should be a single attempt and quit no matter what after. The next game run will naturally pick these up anyway since they are buffered in SQLite and persistent. I DO NOT want to cause unintentional shutdown hangs"*, then *"The FinalShip optimization should NOT be prevented by the minimum 30s window, it is a special case that should be allowed to run regardless of when the last API call was made."* The persistence premise is verified: `MarkShipped` (the `DELETE`) runs only on a `200`, so nothing leaves the outbox until the server acknowledges it and **`FinalShip` is an optimisation, never a correctness requirement** — skipping it entirely loses nothing. What replaced the old 5 s / `ConsecutiveFailures < 3` drain loop: one `ShipOnceAsync`, run on the thread pool, waited on for at most **2 s**, and cancelled and abandoned (never awaited again) if it has not finished — because a hung TCP connect or a server that accepts and never answers must not be able to hold the game open. Every outcome proceeds immediately to disposal, nothing throws across the host boundary, at most one log line, and disposal itself is now wrapped so a disposal racing an abandoned request cannot throw out of the host's unload path. **The exemption is narrow by construction:** a private `ShutdownExemption` enum threaded through private overloads, so the public `ShipOnceAsync` always passes `No` and no caller inside or outside the assembly can ask for `Yes`. It is defensible because it fires at most once per game session — abusing it means actually quitting and relaunching KSA, which costs far more than the 30 s it saves, and that self-limiting property is exactly what the in-session triggers lack. The exempt request **is still stamped**, so the next session's first ordinary batch waits out a full window from it: the exemption buys one batch on the way out, not a reset. Tested for promptness against an unreachable server, a handler that accepts and never responds, and an already-dead-latched shipper, because a shutdown hang would be invisible in normal play and infuriating in the wild.

---

## The load harness

`catlog.loadgen`: many randomised players through the real pipeline, and what one laptop actually does.

### LOAD-001 — The harness is a new sibling project, `mod/catlog.loadgen`, and `catlog.sim` was left alone

*Accepted · 2026-08-07 · WP-LOADGEN.*

The owner asked for "a new test simulator similar to `make sim`… which simulates over a time period with a high volume of data that exercises actual random data and events… for a large number of players". The obvious cheap move — a `--random` mode inside `catlog.sim` — was rejected: the sim is the **deterministic acceptance** tool, its six §7.3 scenarios are asserted to exact leaderboard values (`biggest_lithobrake_survived = 62`), and a randomised mode sharing its runner is one refactor away from making those assertions a statement about a dice roll. So the dependency runs one way only: `catlog.loadgen` references `catlog.sim` for `SimVehicle`/`SimBody`/`SimStep`/`SimShipperClock`/`ReadApiClient`, and **nothing in `catlog.sim` knows this project exists**. All six scenarios still pass with `ASSERT=1`. The new project is in `mod/catlog.slnx`, builds warning-free under `TreatWarningsAsErrors`, and its types are `internal` so the zero-warning policy is not paid for with ceremonial XML docs on a console tool. What it emphatically does **not** do is fabricate an `EventEnvelope`: like the sim, it emits only telemetry snapshots and `GameSignal`s, and the real `EventDetector`, `WindowAccumulator`, `ImpactCorrelator`, `OutboxDb`, `ProofSigner` and `BatchShipper` do their real jobs — a hand-authored batch posted straight at `/v1/ingest` would test the Go server and nothing else, which is not what "test the full feature set" means.

### LOAD-002 — `mockidp` gained one dev-only endpoint, `POST /generate`, and the committed cast and its DOM ids are byte-for-byte untouched

*Accepted · 2026-08-07 · WP-LOADGEN.*

The owner's preference was explicit — *"a backdoor for credential minting or a way to do it automated through APIs (APIs preferable if they can just use our mockidps somehow)"* — and the API path is also the only one that exercises the identity stack rather than skipping it, so it is the default (`--auth oauth`), with `POST /admin/issue` kept behind `--auth admin` as the fast path and the ingest-only control. The blocker was that `server/mockidp.toml` is a fixed list of five buttons; `POST /generate` mints N synthetic subjects instead. **Four properties make this safe.** (1) *catlogd did not change at all* — every generated subject goes through the same authorize → code → token → userinfo (or `id_token`) dance the static cast uses, so catlogd runs its real code exchange, its real ES256 `id_token` verification against the JWKS mockidp publishes, its real `user_key = HMAC(pepper, "<idp>:<sub>")` derivation, its real session cookie, and its real handle rules and quotas; the private key is generated in the harness and only its public JWK is ever sent, exactly as in the browser wizard. (2) *The two populations are separate maps and only the committed one is rendered*, so `#login-as-whiskers-discord-old-account` and its four siblings are unchanged and `TestDOMIdsAreStable` plus the playwright suite are unaffected — there is a new test, `TestGeneratedAccountsAreInvisibleToTheConsentPages`, that diffs all four pages before and after generating thirty accounts and fails if a single byte moved. (3) *The account-age gate is exercised harder, not weakened*: generated Discord subjects carry genuinely aged snowflakes built as `((created_ms − 1420070400000) << 22) | hash`, generated GitHub accounts carry a real `created_at`, and `new_every` deliberately mints a proportion that are **too young**, which the harness then asserts are refused with `account_too_new` (Google is never made too new — it publishes no account age and §4.7 gates it on quotas alone). (4) *mockidp is a development binary that is never deployed, never proxied and only ever bound to 127.0.0.1*, so a generative endpoint there is not a security boundary being widened; it is the local stand-in learning to have more than five customers.

### LOAD-003 — Subject derivation is deterministic, which forced the creation instants to be quantised

*Accepted · 2026-08-07 · WP-LOADGEN.*

A Discord snowflake *is* its creation millisecond in its high bits, so deriving one from `now` would make the same `seed` produce different subjects on every request and destroy the point of a seed. Aged accounts are therefore built from midnight UTC `age_days` ago, minus one second per index — the day quantisation is what makes them reproducible, and the per-index second is what makes them unique by construction rather than by hoping 22 hashed bits do not collide. Collisions against either population are still resolved rather than assumed away. Consequence, accepted: a `seed` reproduces the same subjects **for a UTC day**, not forever, and too-new accounts (quantised to the minute) are reproducible for a minute. That is enough for the job it has, which is re-running a failing load test.

### LOAD-004 — Reproducibility under concurrency is bought by giving every player its own generator, and `--seed` and `--namespace` are deliberately two different knobs

*Accepted · 2026-08-07 · WP-LOADGEN.*

There is no shared RNG: player *i* draws from `Prng.ForPlayer(seed, i)`, a pure function of the run seed and the index, in a fixed order, on one thread — so nothing about scheduling order, concurrency or server latency can reach a draw, and the events player *i* produces are a function of `(seed, i)` alone. `System.Random` was **not** used: its seeded stream is a runtime implementation detail that has already changed once, and `--seed` is a promise, so thirty lines of SplitMix64 live in `Prng.cs` instead. The proof is a printed digest — SHA-256 over `(type, sim_t)` for every envelope in order, per player, combined order-independently — which deliberately excludes the ULIDs, because event, flight and session ids are minted fresh every run by design and hashing them would make every digest unique and the check worthless. `--seed` namespaces the *gameplay*; `--namespace` namespaces the *identities* (mockidp subjects and handles) and defaults to a timestamp, so the same seed can be re-run against a database that already holds the previous run's players without fighting over handles. Demonstrated: two 40-player runs at `--seed 8675309` produced different namespaces and identical `events_generated`, `frames`, `batches`, per-type histogram and digest `aff1f0a83b7f546b`.

### LOAD-005 — The harness separates *cadence* from *floor*, and getting that wrong cost a whole run before it was noticed

*Accepted · 2026-08-07 · WP-LOADGEN.*

The harness separates *cadence* from *floor*, and getting that wrong cost a whole run before it was noticed. `Wire.MinShipIntervalSeconds` is measured against the injected `IShipperClock`, and the first design advanced that clock one frame-worth of sim time per frame, so the mod's own age trigger would fire naturally. It does not work, for a reason worth writing down: `BatchShipper.ShouldShip`'s age trigger compares the injected clock against `OutboxDb`'s `created_ms`, which is stamped from the **real** wall clock at append time and cannot be virtualised from outside `catlog.lib`. Against a wound-forward clock that comparison saturates after the first window and then answers "due" on every frame for the rest of the run — so `--ship-age` silently did nothing and every player shipped as fast as the floor allowed (a 250-player run produced 40 323 batches of ten events each instead of 4 748 of eighty-five). Worse, winding the clock by sim time put the proof's `iat` outside §4.3's ±300 s window on *every* batch: 4 498 `401 clock_skew` recoveries against 322 accepted batches, i.e. the run spent itself on the recovery path. **The shape that works:** the harness decides *when a batch is due* on sim time (`--ship-age`, defaulting to the mod's own 60 s), and leaves *whether it may go* entirely to `BatchShipper`; when the floor refuses, the clock is wound by the remainder and not one millisecond more, exactly as `catlog.sim` does. Same run afterwards: 4 748 batches, 252 resyncs. The recoveries do not go to zero and should not — compressing three hours into forty-three seconds means the client genuinely does drift out of the skew window about once per player, takes a `401`, relearns the offset from the `Date` header and carries on, which is the mod's real recovery path getting a workout it would otherwise never get. They are counted and named in the report rather than hidden, and `--assert` allows exactly as many `401`s as there were resyncs and not one more.

### LOAD-006 — The 30-second floor was not weakened, and the guard tests that say so still pass

*Accepted · 2026-08-07 · WP-LOADGEN.*

The 30-second floor was not weakened, and the guard tests that say so still pass. `catlog.loadgen` is a separate console assembly that no player installs, which is the same standing it has for `catlog.sim`: `mod/catlog` still constructs its shipper with no clock argument, still gets `SystemShipperClock` and a real thirty seconds, and `ClockSeamTests.TheShippedModNeverInjectsAClock` and `TheShippedModReadsNoEnvironmentVariablesOrCommandLine` read `mod/catlog`'s sources and are unaffected by a project that is not in that directory. `--ship-age` is clamped to `Wire.MinShipAgeTriggerSeconds` on the way in for the same reason `ModConfig.Normalize` clamps `ship_interval_s`: a configured cadence faster than the hard floor would be a number that does not describe what happens. `--clock real` exists so the client-side promise can be measured on its own terms, and the report says in one line which of the two a given run was measuring.

### LOAD-007 — Idempotency is demonstrated, not assumed, by re-sending one accepted batch byte for byte

*Accepted · 2026-08-07 · WP-LOADGEN.*

The wire identity of a batch is its proof `jti`, signed over the body hash, so the only way to reach §4.5.3 step 11's whole-batch replay short-circuit is to repeat the exact bytes — anything rebuilt would mint a new batch id and test the stream check instead. A recording `DelegatingHandler` therefore keeps the newest accepted request of the whole run and the probe repeats it, expecting `200 {"replay":true}`, a non-zero `deduped`, and `events.total` unchanged. **The "newest of the whole run" part is load-bearing and was learned the hard way:** verification reaches the skew check (step 8) long before the replay check (step 11), so a capture taken at the start of a 338-second run came back `401 clock_skew` and said nothing at all about idempotency. Revoked credentials are the mirror image and need no such care — revocation is checked at step 5, before the clock — so a revoking player's own last batch is kept and replayed after the revoke, and it answers `401 license_revoked` however old the proof is.

### LOAD-008 — What one laptop actually does, and which limit was actually binding

*Accepted · 2026-08-07 · WP-LOADGEN.*

Reference run on an Apple M4 Pro (14 cores, 48 GiB), `catlogd` on a throwaway data directory, everything on loopback: **250 players × 3 simulated hours = 750 player-hours, 405 098 events in 43.9 s — 9 228 events/s across 4 748 batches (108 batches/s, 33.5 MiB on the wire)**, ingest latency p50 75 ms / p90 196 ms / p99 365 ms, 1 142 concurrent read-API requests all 2xx, 5 194 live-feed frames, projector caught up 19.2 s after ingest stopped, and all twelve invariants held. Larger shapes: 500 players × 2 h → 621 914 events in 82 s; 800 players × 40 min → 335 302 events at 343 batches/s. **The bottleneck at every scale tried was the per-credential token bucket** (§4.3: 1 batch / 2 s, burst 5) — 800 aligned players produced 21 419 `429`s against 30 937 accepted batches — which is the intended behaviour and means aggregate throughput scales with the number of *credentials*, not with anything about one client. **The `503` write-channel path was never reached, at any scale, and that is a genuinely useful finding:** the bucket throttles clients long before 256 write jobs can queue, and since each client waits for its own reply it holds at most one job outstanding. Re-running with `ratelimit_per_jkt_per_s = 500` to take the bucket out of the picture *still* produced zero `503`s at 800 concurrent players — instead ingest p99 went to 11.4 s and eleven client requests never completed at all, because the cost that actually saturates is the **handler's** CPU work (two ECDSA verifications, Brotli, NDJSON parse) rather than the single writer goroutine, which drains its queue faster than the verifications can fill it. So the bounded channel is a correct backstop that a well-behaved fleet cannot reach, and the thing to watch as the population grows is verification cost per request, i.e. requests per second rather than events per second — which is also the argument for the mod's ~one-batch-a-minute cadence being the right shape.

### LOAD-009 — Two smaller notes

*Accepted · 2026-08-07 · WP-LOADGEN.*

Two smaller notes. (1) Progress goes to **stderr** and the report to **stdout**, so `make loadgen REPORT=json` pipes into `jq` with no filtering step; `catlog.lib`'s ambient `ModLog` sink is likewise redirected to stderr and throttled, because the default console sink writes to stdout and a 250-player run legitimately emits hundreds of clock-resync WARNs. (2) `catlog.sim`'s `ReadApiClient.EnsureHandleVisible` carries a comment saying `POST /admin/issue` does **not** reload the handle directory and works around it by posting `/admin/seed`. That premise is now stale: `adminapi/issue.go` calls `s.reloadDirectory(r.Context())` and `identity/api.go`'s `issue` does the same after a dashboard claim, so both issuance paths reload and the workaround never fires. It is left in place — it is defensive, it costs nothing when the handle is already visible, and editing the sim to chase a comment is not worth the risk to six asserted scenarios — but the load harness deliberately does not call it, because posting the demo dataset into the middle of a load run would corrupt every number in the report.

### LOAD-010 — Capability is gated on accumulated in-game time and on nothing else

*Accepted · 2026-08-07 · WP-LOADGEN.*

The owner asked for a simulator that "behaves like a player": launches from planets first, then orbital manoeuvres, rendezvous, transfers, landings and probes, with more craft in the air as time in game accumulates. That is a *career*, so `mod/catlog.loadgen/Career.cs` models one directly: six stages (`rookie`, `suborbital`, `orbital`, `operator`, `interplanetary`, `explorer`) opening at 0 / 3 / 10 / 30 / 80 / 200 in-game hours, and ten mission kinds each unlocking at a stage (`pad-test` and `hop` at rookie; `high-hop` at suborbital; `orbit`, `manoeuvre` and `deorbit` at orbital; `rendezvous` at operator; `transfer` and `landing` at interplanetary; `probe` at explorer). The stage is re-read at **every launch**, not once per player, so a career that crosses a threshold mid-run starts flying the thing it just unlocked — six of fourteen players did exactly that in the first calibration run. Fleet size (`0–1` resident craft at rookie, `6–15` at explorer) and *concurrency* (1 mission in flight at rookie, 5 at explorer) are gated the same way: growth in the report is growth in what the player is doing at once, not just a bigger number. Temperament (`cautious`/`steady`/`prolific`/`engineer`/`daredevil`) is deliberately **orthogonal** to stage — it moves risk appetite, cadence and mission mix, never capability — so a reckless veteran and a careful one fly the same things with very different loss rates. The old `PlayStyle` enum, which conflated the two, is gone.

### LOAD-011 — Players arrive with a career already behind them, and `sim_t` is career time

*Accepted · 2026-08-07 · WP-LOADGEN.*

A run's window is hours; the stages span hundreds of in-game hours. Starting every player at zero would have produced a population of identical beginners and a run in which nothing beyond a suborbital hop was ever reachable. Each player therefore draws a **prior career age** and the window is a slice out of the middle of a career: `PlayerScript` works in career seconds throughout (`_epoch = PriorSeconds`), `CareerClock` maps those back to `wall_t`, `VehicleCreatedSignal.LaunchGameTime` carries the career instant a craft was built, and residents carry a launch time in the career's *past* so their flight identity is stable across a save load. This lands exactly on WP-CAREER's contract that `sim_t` is "seconds since this career began": the harness's veterans now open their session at `sim_t ≈ 1.1e6` rather than pretending a 300-hour player reached orbit forty seconds into their career, and `fastest_to_orbit` / `fastest_to_luna` mean something for the data it generates. **No seam is missing** — `EventPipelineOptions.CareerId` defaults to a stable per-install career and the harness has exactly one career per player, which is the right answer; a save load emits `SessionLoadedSignal` with a null career id on purpose, because reloading a save does not change which save is being played.

### LOAD-012 — Failure is modelled by phase, and the phase is what makes it look like play

*Accepted · 2026-08-07 · WP-LOADGEN.*

A uniform loss rate in a uniform place is not a career. Each flight draws a loss probability from `stage × kind × temperament` (rookie 0.44 down to explorer 0.08; `landing` ×1.45, `deorbit` ×1.3, `probe` ×0.65; daredevil ×2.3, cautious ×0.7), and a lost flight then draws **where** it was lost from a table in which every phase carries an intrinsic difficulty and a pair of green/seasoned multipliers that the stage interpolates between. Early careers therefore lose vehicles on the pad (×4.0 green, ×0.25 seasoned), on ascent and at max-Q; late ones lose them on approach, on touchdown (×3.0 seasoned) and while closing on a docking port. The RUD cause follows from the phase because that is the physics — max-Q tears a rocket apart with `aerodynamic_forces`, a docking prang is a `collision`, a pad topple is `ground_impact` or `collision` and nothing else, and whether a bad descent ends `ground_impact`, `ocean_impact` or `hydrodynamic_forces` depends on what is underneath it. **A lost flight is also truncated to where it was lost**: the profile is laid out over the *planned* length and the failure cuts the *timeline*, so a pad failure produces four seconds of telemetry, one ignition and a RUD — "early careers fail early" as a fact on the wire rather than as a comment. The RUD payload's `peak_g`/`peak_q_pa`/`speed_ms`/`altitude_m` are drawn per phase for the same reason: a pad fire at 40 kPa and 60 km would be nonsense nothing in the pipeline would reject.

### LOAD-013 — The harness flies the solar system KSA actually ships, with KSA's own numbers

*Accepted · 2026-08-07 · WP-LOADGEN.*

The harness flies the solar system KSA actually ships, with KSA's own numbers. `catlog.loadgen` inherited `catlog.sim`'s `kerbin`/`mun`/`duna`, which is right for six hand-asserted scenarios and wrong here twice over: too small (`soi_bodies` counts *distinct* destinations) and not the game's. KSA is the real solar system — `Content/Core/SolSystem.xml` loads Sol, Mercury, Venus, Earth (`HomeBody="true"`), Luna, Mars, Phobos and Deimos, and `Vehicle.cs:3745` ships a "Teleport To Apollo 11 Landing Site" button — and WP-CAREER's `TimedBodies` enumerates the same eleven. `LoadBodies` now carries all eleven with radii and masses read out of `Content/Core/Astronomicals.xml`, surface gravity as `GM/R²` from those masses, and atmosphere heights computed the way the game computes them (`PhysicalAtmosphereReference.CalculateBoundaryHeight`, `max(-H·ln(1e-9/ρ₀), -H·ln(1e-4/P₀))` — 167 km for Earth, 455 km for Venus, 185 km for Mars). Every orbital speed is then *derived*, never drawn: low Earth orbit comes out at 7.8 km/s, low lunar orbit at 1.6 km/s and low Phobos orbit at 4 m/s because vis-viva says so. Three consequences fall straight out. Interplanetary cruises pass through the **star's** SOI, so the chain on the wire reads `earth → sol → mars` as it does in the game and in `internal/seed`, while a lunar transfer never leaves Earth's SOI and correctly gets no `sol` leg. A craft bound for a moon of another planet enters that planet's SOI first, so a Phobos mission visits Mars whether the player meant to or not. And EVAs are restricted to bodies whose escape speed exceeds 15 m/s — the stock tumble gate is 6.5 m/s and Deimos escapes at 6.2, so a kitten that tumbled there would simply leave. `catlog.sim` keeps `kerbin`/`mun`/`duna` and is untouched.

### LOAD-014 — Interplanetary cruises are compressed in sim time, and the reason is `--ship-age`

*Accepted · 2026-08-07 · WP-LOADGEN.*

A real Earth→Mars transfer is about 2.1e7 sim seconds. Reproducing that faithfully means modelling **time warp** — non-uniform frame spacing, because the mod samples at 2 Hz of *real* frames and a warped cruise is a handful of samples spread over months. That was designed and then rejected, and the blocker is not effort: `PlayerRunner` decides its batch cadence on **sim** time from `--ship-age` precisely so a run reproduces the traffic shape of a real player, and under warp every single frame exceeds a 60-second age trigger, so the cadence would collapse to one batch per frame and the harness would stop measuring the thing it exists to measure. Mission lengths are therefore *ordered* by distance class (moon ×1.0, inner ×1.7, outer ×2.6) and compressed to fit the window, so relative realism and every board's ranking hold while the calendar does not. What the harness models is **attended flight time** — the sim time a player actually watches at 1× — and that is the honest description of a 2 Hz sampler. Restoring true transfer durations means teaching the runner a warp-aware cadence first; noted here so the next person does not rediscover the collision.

### LOAD-015 — Coverage rotations are keyed on a dense cohort index, not on the player index

*Accepted · 2026-08-07 · WP-LOADGEN.*

Reproducibility keys the random stream on the account index (`Prng.ForPlayer(seed, index)`, unchanged). Coverage cannot: identities refused by the ≥30-day age gate never become players, so the surviving indices have holes, and a fourteen-player run that loses index 11 loses its only explorer — which is exactly how the first calibration run failed its own `career spread` check. Both rotations therefore key on the player's dense **position among the players that ran**: a twelve-rung career ladder (`rookie ×3, suborbital ×2, orbital ×2, operator ×2, interplanetary ×2, explorer ×1` — bottom-heavy, so it is a player base and not six equal cohorts) and the six-cause RUD rotation. The prior-age draw is a Pareto **clamped into its rung's band** rather than `max(natural, floor)`: taking the maximum let the Pareto's fat middle lift the entire bottom of the ladder past the rookie stage, and a run with no rookies in it is not a player base. Two launches are likewise forced rather than drawn — an interplanetary career opens with a transfer, an operator career with a rendezvous — with the *draw still made and then overridden*, so the guarantee has no effect on the rest of the random stream. Net: any run of twelve or more players covers every stage, every RUD cause, `vehicle.soi`, `vehicle.docked` and `kitten.tumble` **deterministically**, and `--seed` still replays byte for byte at any concurrency.

### LOAD-016 — Every player is guaranteed one loss, and that is a claim worth stating

*Accepted · 2026-08-07 · WP-LOADGEN.*

The covering RUD cause is pinned to the **first loss whose phase can physically carry it**, so a covering `ocean_impact` lands on a splashdown gone wrong and never on a pad fire. A career the window happened to catch with no losses at all has one manufactured on its first flight — common for a cautious explorer at 5.6 % per mission over six missions. Over the dozens of flights a career actually contains that is not a strong claim (nobody reaches an explorer's hours without losing something), and it is the price of the guarantee being deterministic rather than probable. The alternative — relaxing the taxonomy check to "usually all six causes" — was rejected outright: an invariant that is sometimes true is not an invariant.

### LOAD-017 — The report grew a `careers` section and the assertions grew two checks

*Accepted · 2026-08-07 · WP-LOADGEN.*

Totals alone cannot answer "did this population look like a plausible player base", so the report now prints stage distribution at the start and end of the window (with how many advanced), the career-age percentiles, temperament mix, resident craft *per player by stage*, missions attempted vs completed, the mission mix by kind, **losses by phase**, losses by cause, and the distinct bodies reached. Bodies reached counts only arrivals *inside the window* — a resident already parked round Mars is evidence the career has been there but produces no `vehicle.soi`, and leaving it out is what makes that line comparable player-for-player with the `soi_bodies` board. `--assert` gains `rud cause coverage` (all six produced) and `career spread` (every stage populated, somebody off the home world), both gated on the run being at least one full rotation of the ladder so a small run gets a weaker check rather than a flaky one; `taxonomy coverage` was extended from 11 to 17 required event types on the same condition. A richer career model made coverage *easier* to assert, which is the direction it was supposed to move.

### LOAD-018 — The board list is re-read after ingest, not just before it

*Accepted · 2026-08-07 · WP-LOADGEN.*

The board list is re-read after ingest, not just before it. `ReadLoad.DiscoverBoardsAsync` ran once, at the top of a run, so it learned the list an *empty* database publishes. That was harmless when every board key was a compile-time constant and became wrong the moment `fastest_to_<body>` and `rud_<cause>` started being published on a distinct-player threshold: a run that put eighteen players on ten other worlds reported a boards section that did not mention those boards existed. Discovery is now idempotent (it replaces the list rather than appending to it) and is called a second time after the readers have stopped and the projector has caught up. Cheap, and it is the difference between the harness demonstrating that late-career players reach other bodies and merely asserting it.

### LOAD-019 — Four defects a review of the career work turned up, all in `catlog.loadgen`

*Accepted · 2026-08-07 · WP-LOADGEN.*

Four defects a review of the career work turned up, all in `catlog.loadgen`. (1) **A docking needed the two craft to be in the same place** and did not check: a rendezvous mission launched from Earth picked its partner uniformly from a fleet spread across the system, so `vehicle.docked` was being emitted against a surface base on Luna. Mission partners are now filtered to craft in *home orbit*, resident-to-resident dockings require both craft at the same body and neither landed, and a career at or past the operator stage is guaranteed one orbiting home resident — which is both what a station keeper's save looks like and what keeps `vehicle.docked` deterministic. (2) **`Scuttled` emitted a `vehicle.impact` for craft that were nowhere near a surface**, including probes abandoned at Jupiter — shipping lithobrake data for an impact on a gas giant. The impact is now gated on the mission actually descending onto a landable body. (3) **`Reading`'s staging arm inverted its range on small bodies**: `Range(800, orbital * 0.9)` is `Range(800, 6.2)` at Phobos, and `Prng.Range` does not sort its arguments, so a staging failure reported up to 800 m/s against a moon whose escape speed is 9.7. Both bounds are body-relative now. (4) **A transport fault in the mid-run reissue took the whole run with it**: `PlayerRunner` converts a dead shipper into a `PlayerResult` with an error, but an `HttpRequestException` from the reissue round trip escaped the player task entirely, so `Task.WhenAll` threw, `report.Players` was never populated and a two-hundred-player run exited with no report at all. It is recorded as a failed player instead, which is exactly what the `players completed` invariant exists to surface.

### LOAD-020 — Three smaller ones from the same review

*Accepted · 2026-08-07 · WP-LOADGEN.*

Three smaller ones from the same review. (1) A read loop treated `HttpClient`'s own 30-second timeout as run cancellation and retired itself silently, so `read API under load` could pass vacuously on the handful of requests taken before the server got busy; a timeout is now counted as a transport error and the loop carries on. (2) `--seed 0` was indistinguishable from "no seed given" and was silently replaced by a random one while the report said the seed was pinned; whether the flag was *seen* is now tracked. (3) The extended taxonomy and career-spread checks are additionally gated on the window being at least twenty simulated minutes — below that the generator is never given room to fly a transfer or fit an EVA, so requiring their events would have failed the run for the operator's choice of `--duration` rather than for a defect. The coverage pass also no longer relocates a covering loss to the *first* admitting phase: `Pad` admits both `ground_impact` and `collision`, so that put every covering loss for two of the six causes on the launch pad and quietly manufactured part of the pad-heavy failure profile the report exists to measure. It now moves the loss as near as possible to where the flight was already going to fail.

### LOAD-021 — The load harness was slow because of the server's token bucket, and nothing else — measured, not argued

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

The report now carries a "where the time went" table (`PlayerTiming` in `mod/catlog.loadgen/PlayerRunner.cs`) that times shipping, generating, the 429 `Retry-After` sleep, the client's 30-second ship floor and the retry ladder separately. On the standard 25-player × 45-simulated-minute run it reads: rate-limit wait **1770.2 player-seconds (99.6%)**, shipping 6.2 s (0.3%), generating 1.7 s (0.1%), **ship floor 0.0 s**, retry backoff 0.0 s. Divided by the concurrency, the 429 sleeps are 70.8 of the ingest phase's 82.4 wall seconds. The harness process burned 5.8 s of CPU over 85 s of wall clock — under 0.5% of a 14-core machine. The floor bucket is timed *even under `--clock virtual`, where it is expected to be zero*, precisely because the 30-second floor is the most natural thing to blame: `WaitOutTheFloorAsync` winds an injected `IShipperClock` and returns, so the floor costs 172 clock-skew resyncs (≈0.7 s of extra round trips) and no wall time at all. A run that prints `ship floor 0 s` next to `rate-limit wait 1770 s` has ended the argument rather than continued it.

### LOAD-022 — `[limits] ratelimit_disabled` gets the `clock_control` treatment, and for the same reason

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

`[limits] ratelimit_disabled` gets the `clock_control` treatment, and for the same reason. §4.3's one batch per two seconds per credential is a hard ceiling of 250 events/s per player at `--batch 500`, so a harness run measures the token bucket and nothing else. The new knob removes §4.5.3 step 9 from the chain entirely — `authz.New` builds no `Limiter` and `verify` skips the check — rather than configuring an enormous rate, on the same principle that leaves `POST /admin/clock` unmounted rather than mounting a handler that refuses: a control that is absent cannot be half-on. It defaults false, `Config.Validate` refuses it combined with an `https://` base URL, and catlogd logs a WARN naming the base URL for as long as it runs with it on. Raising `ratelimit_per_jkt_per_s` remains ungated and is the answer for a real deployment that needs to ship faster, because that still leaves a limit in the chain. Same parameters, same seed, same 14,708 events, same digest: **84.99 s → 4.68 s** end to end, ingest **82.42 s → 0.86 s** (178 → 17,128 events/s), every invariant still passing and `deduped 0`.

### LOAD-023 — ECDSA is not the ceiling; the single writer goroutine is, and then the projector is

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

ECDSA is not the ceiling; the single writer goroutine is, and then the projector is. `net/http/pprof` on the admin mux, sampled during sustained ingest, settles the question both ways round. With fat batches (631 events, 46 batches/s): `Writer.process` is **35.4%** of server CPU, 98.6% of it in `InsertEvents`, while the whole ingest handler is 2.5% and `authz.Verifier.Verify` — *both* signature checks — is **0.07%**. With thin batches at 1,239 requests/s, where per-request cost is 40× more significant, `Verify` still only reaches **2.9%** (≈58 µs per request for two P-256 verifications) against `Writer.process` at 15.1%. Brotli never exceeds 0.1%; NDJSON parsing is 2%. The earlier note that the handler saturates before the writer does was measured on ~15-event batches and does not generalise. The bounded write channel never pushed back once — queue depth sat at 51–55 of 256 through a million-event run, which is the writer being the throttle and the 503 path being correctly never needed.

### LOAD-024 — The projector is the real ceiling for a run of any size, at ~3,300 events/s, and it does not care how many cores you have

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

Measured three ways: 3,350 events/s folding a 954k-event log with nothing else running (projections.db deleted, catlogd restarted, no polling at all), 3,554 events/s during the million-event run, and — the interesting one — **3,768 / 3,859 / 3,792 events/s at `GOMAXPROCS` 1, 2 and 14**. It is one goroutine making synchronous Turso cgo calls; the profile is 78% `runtime.pthread_cond_signal` reached through `newproc`, i.e. the Go scheduler waking OS threads for the goroutine `database/sql` spawns per statement. Ingest over the same sweep runs 16,462 → 27,714 events/s, so ingest degrades ~1.7× from 14 Ps to 1 while the projector does not move. Ingest stores at ~30,000 events/s and the projector folds at ~3,300: **a million events takes 36 seconds to store and five minutes to fold**, and `--assert` cannot begin until it reaches the head. That ratio, not the CPU, is what to fix if million-event runs ever need to be faster.

### LOAD-025 — A fixed projector deadline cannot survive a harness whose job is runs of unknown size, and 300 s was where a 1,058,811-event run died at 94% folded

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

A fixed projector deadline cannot survive a harness whose job is runs of unknown size, and 300 s was where a 1,058,811-event run died at 94% folded. `ReadApiClient.WaitForProjector`'s constant timeout is right for `catlog.sim` — a scenario writes a few hundred events and either folds them or is broken — and wrong here, because the fold is proportional to the run. `mod/catlog.loadgen/ProjectorWait.cs` replaces it with a **progress** bound: a projector whose `checkpoint_seq` is advancing is given as long as it needs, one that has not moved for 60 s has stalled and that is a failure at any size, and the run's own `--timeout` stays the absolute ceiling through the cancellation token. Its poll interval also backs off (25 ms → 1 s as the backlog grows) because `GET /admin/stats` runs `SELECT COUNT(*)` over the whole event table — ~300 ms of server work per call at a million rows, against the database the projector is reading. Polling that 40 times a second while waiting five minutes spends more server time answering the question than doing the work.

### LOAD-026 — `--concurrency`'s 4×-cores default is right only while the token bucket is on

*Accepted · 2026-08-07 · WP-LOADGEN-FAST.*

Its comment says players are network-bound "waiting out the server's per-credential token bucket", which is exactly true — and stops being true the moment the bucket is gone, at which point players are CPU-bound and oversubscription costs throughput. Identical 300-player workload, limiter off: `-c 14` → 28,277 events/s, `-c 56` (the default here) → 23,971, `-c 112` → 17,951, `-c 224` → 14,178, with harness CPU rising from 15.8 s to 48.8 s for the same work. The default is left alone, because it is correct for the default server; `--help`'s new "going fast" section and the report's verdict both name the trade and suggest `-c <cores>` for an unthrottled run. The recipe for a million events is `catlogd` with `CATLOG_LIMITS_RATELIMIT_DISABLED=1` and `--players 550 --duration 2.6h --batch 2000 --ship-age 1h --concurrency 14`: **1,058,811 events in 336 s**, 36 s of it ingest, 298 s of it the projector, `deduped 0`, all fourteen invariants green.

---

## nginx, systemd & deployment

The reverse proxy, the hardened unit, and why a deploy must fully stop the old process.

### OPS-001 — Dependencies added, all resolved offline from the local module cache

*Accepted · 2026-08-07 · WP9.*

Dependencies added, all resolved offline from the local module cache (`GOPROXY=file://$(go env GOMODCACHE)/cache/download`, so no network call): `github.com/testcontainers/testcontainers-go` **v0.43.0** — the last §5.1 package still unadded — plus `github.com/moby/moby/client` **v0.4.0** as a *direct* test dependency (see the docker-probe entry below; it was already in the graph as testcontainers' own client). Side effect worth knowing: `golang.org/x/sys` was upgraded **v0.38.0 → v0.45.0** by testcontainers' requirements. tursogo v0.7.2 needs ≥ v0.38.0 and `make server-test` is green on v0.45.0, so nothing was pinned back.

### OPS-002 — The §6.3 skip probe had to be made stronger than "docker answers": this machine has a podman socket linked at `/var/run/docker.sock`

*Accepted · 2026-08-07 · WP9.*

Docker Desktop is stopped, `~/.docker/run/docker.sock` does not exist, and testcontainers falls back to the default socket path — which here is a symlink to a running podman machine. `Provider.Health` (a `/info` call) therefore *succeeds*, and the run then dies inside container creation with `unable to find network with name or ID bridge`: testcontainers' `ProviderDocker` hardcodes Docker's default `bridge` network for the reaper and for the SSH tunnel behind `WithHostPortAccess`. Probing for that network does not help — podman's compat API **fabricates `bridge` on inspect** (verified: `GET /networks/bridge` returns 200 and the network then appears in `GET /networks`) and still rejects it at container create. The probe therefore identifies the engine: `/version` components must contain one named exactly `Engine` (podman answers `Podman Engine`, which is why the test is equality and not a substring). An engine reporting no components at all is trusted, because a false skip is worse than a failure that names its own cause. The skip message says how to fix it, including that `DOCKER_HOST=unix://…/podman.sock` makes testcontainers select its podman provider.

### OPS-003 — `infra/nginx/dev.conf` is §6.1 verbatim, and the two placeholders are substituted differently by each consumer

*Accepted · 2026-08-07 · WP9.*

`infra/nginx/dev.conf` is §6.1 verbatim, and the two placeholders are substituted differently by each consumer. `infra/compose.yaml` mounts it at `/etc/nginx/templates/nginx.conf.template` and lets the nginx image's envsubst step write `/etc/nginx/nginx.conf` (`NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx`); `NGINX_ENVSUBST_FILTER=^(UPSTREAM|STATIC_ROOT)$` is load-bearing, because without it envsubst also expands `$binary_remote_addr`, `$proxy_add_x_forwarded_for`, `$scheme` and `$host` into empty strings. The Go suite substitutes in-process instead, and **fails the test if either placeholder has disappeared from the file** — a dev.conf that hardcoded its upstream would otherwise make the suite test a config nobody ships.

### OPS-004 — `prod.conf.example` is a server-block fragment for the distro's `http {}`, not a whole nginx.conf like dev.conf

*Accepted · 2026-08-07 · WP9.*

Consequences documented in its header: the three `limit_req_zone`/`limit_conn_zone` declarations must be installed separately (a zone cannot live in a server block), and the HTTP/2 line is left as an explicit either/or rather than guessed — `http2 on;` needs nginx ≥ 1.25.1 while Debian 12 ships 1.22 and Ubuntu 24.04 ships 1.24, where the `http2` parameter belongs on `listen` instead. Prod zone sizing is `catlog_ingest` 2r/s burst 10 (≈4× a single player's §4.3 budget, so a household NAT is unaffected) and `catlog_web` 20r/s burst 40.

### OPS-005 — §13.7's Cloudflare `real_ip` block ships commented out, with the reason spelled out as a hazard rather than a note

*Accepted · 2026-08-07 · WP9.*

Per-IP zones key on `$binary_remote_addr`, which becomes a Cloudflare edge address once CF fronts the origin, so the zones must switch to `CF-Connecting-IP` via `set_real_ip_from` + `real_ip_header`. Enabling that *before* Cloudflare is in front and 443 is firewalled to CF's ranges is strictly worse than no rate limiting: any client can then pick its own bucket (a random value per request makes the limiter unreachable; a victim's address makes it a weapon), and the spoofed value also lands in `access.log`. The file states the required order — CF in front, firewall 443 to CF ranges, *then* uncomment.

### OPS-006 — The six §6.3 tests are subtests of one `TestNginxProxy` sharing one container

*Accepted · 2026-08-07 · WP9.*

Six containers would multiply a cold image pull by six and prove nothing extra. Ordering is deliberate: the burst test runs **last**, because it empties the `limit_req` bucket (which refills at 10r/s). catlogd's own limiter is configured at 1000/s burst 1000 in this fixture, so a 429 can only be nginx's — the burst test additionally asserts the 429 carries nginx's HTML error page rather than a §4.9 JSON body, and that fewer than 40 of the 40 requests reached the upstream.

### OPS-007 — Two parts of the §6.3 fixture are local stand-ins, and deliberately so

*Accepted · 2026-08-07 · WP9.*

Two parts of the §6.3 fixture are local stand-ins, and deliberately so. (a) The SSE endpoint is a minimal `text/event-stream` handler in the test package: the real datastar feed is WP4/WP5's, and what this test asserts is nginx's behaviour (headers out, frame flushed, frame delivered in under a second), which is identical whatever writes the frames. (b) `X-Forwarded-For` is asserted from a spy that *wraps* the real ingest handler, because no catlogd handler reads the header today (`ingest.clientIP` uses `RemoteAddr` on purpose — behind nginx the header is only trustworthy because nginx sets it). The spy sees the same `*http.Request` the handler is handed, and it doubles as the "did this reach Go at all?" counter for the 413, `/admin/` and static cases. Everything else in the suite — verifier, writer, store, credential minting — is the real code.

### OPS-008 — One subtest beyond §6.3: `/static/` is fetched through the proxy

*Accepted · 2026-08-07 · WP9.*

The `alias $STATIC_ROOT/;` location is the one directive in dev.conf that can break silently (a 404 looks like a missing build, not a broken proxy), and asserting that the request never reaches Go is the same one-line check the other cases already use.

### OPS-009 — `catlogd.service` sets `TURSO_GO_CACHE_DIR=/var/lib/catlog/turso-cache` and creates it in `ExecStartPre`

*Accepted · 2026-08-07 · WP9.*

This is what makes §11's hardening survivable: tursogo extracts a native `.so` and `dlopen`s it at startup, `ProtectSystem=strict` makes everything outside `ReadWritePaths` read-only, and `PrivateTmp` gives a writable `/tmp` that a hardened host may still mount `noexec` — which would turn a writable directory into a `dlopen` failure. Pointing the driver at a directory already inside `ReadWritePaths` removes both failure modes. Related and equally load-bearing: **`MemoryDenyWriteExecute` must NOT be set** (purego/fakecgo and the dlopen'd engine need executable mappings); the unit carries it commented out with that warning so nobody adds it back as "one more hardening flag".

### OPS-010 — `ReadWritePaths=/var/backups/catlog` belongs on `catlogd.service`, not on the nightly unit

*Accepted · 2026-08-07 · WP9.*

`ReadWritePaths=/var/backups/catlog` belongs on `catlogd.service`, not on the nightly unit. `catlogctl backup` is an admin-API call (§5.9) — the process that quiesces the writer and copies the database is *catlogd*, because a live Turso file cannot be read by another process at all (WP1's `TestSecondProcessIsLockedOut`) and `cp` of a WAL database is not a backup anyway. So the nightly unit writes nothing, needs no `ReadWritePaths`, and is pinned to `IPAddressAllow=localhost` / `IPAddressDeny=any`.

### OPS-011 — `catlog-nightly.service` is correct-but-ahead-of-code and says so in a comment

*Accepted · 2026-08-07 · WP9.*

`catlog-nightly.service` is correct-but-ahead-of-code and says so in a comment: `rebuild` lands in WP4, `archive` and `backup` in WP10, and all three exit non-zero today, so the timer must not be enabled until those are in. Two other notes are recorded in the unit itself: flags follow the verb (`catlogctl backup -config … <dest>`), because catlogctl's global flag set holds only `-version`; and a purge frees pages without shrinking the file — `VACUUM` stays behind the experimental DSN flag §5.4 forbids, so free pages accumulate and the honest reclamation path is restore-from-archive into a fresh database, not vacuuming.

### OPS-012 — `deploy.sh` stops before it starts, and never provisions

*Accepted · 2026-08-07 · WP9.*

The stop is explicit (`systemctl stop`, then poll `is-active` for up to 60 s, then refuse to install) rather than a `restart`, because the guarantee we need is *the old process has exited and released the file lock* — no rolling or blue/green deploy is possible. It keeps one generation (`catlogd.prev`) and prints the rollback command if `/healthz` does not answer within 60 s. `--dry-run` is total: every mutating step goes through one `run` wrapper, so a dry run builds nothing and creates no staging directory (verified). Systemd units install only under an opt-in `--install-units`; the **nginx config is never installed** (it is a `.example` with placeholders and TLS is owner-managed, D1) — it is only rsynced into the staging directory, where the script points out any drift from `/etc/nginx/sites-available/catlog`. Staging lives in `server/bin/deploy/`, which `.gitignore` already covers.

### OPS-013 — §13.1 cross-compile check, executed: `GOOS=linux GOARCH=amd64 go build ./cmd/catlogd` produces a 32 MB `ELF 64-bit LSB executable, x86-64 … dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2`, with `DT_NEEDED = libdl.so.2, libpthread.so.0, libc.so.6`

*Accepted · 2026-08-07 · WP9.*

§13.1 cross-compile check, executed: `GOOS=linux GOARCH=amd64 go build./cmd/catlogd` produces a 32 MB `ELF 64-bit LSB executable, x86-64 … dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2`, with `DT_NEEDED = libdl.so.2, libpthread.so.0, libc.so.6`. `CGO_ENABLED=0` changes nothing — the two builds are byte-identical (same Go BuildID) — which is the concrete proof behind the 2026-08-06 "cannot ship on scratch/distroless-static" entry: a glibc base is not a preference, it is three `DT_NEEDED` entries. **Running the artifact on linux remains unverified on this machine** (no docker), so the §13.1 item stays open until the first real deploy or a docker run.

### OPS-014 — The nginx configs are NOT validated by `nginx -t`

*Accepted · 2026-08-07 · WP9.*

There is no nginx binary on this machine and no usable docker daemon to borrow one from, and pulling an image would be a network call this work package deliberately did not make. What was checked is structural only: balanced braces, every directive line terminated, both placeholders present and substitutable, and every `$nginx_variable` surviving substitution — plus `docker compose -f infra/compose.yaml config`, which parses without a daemon. **First install on the VPS must run `nginx -t` before `systemctl reload nginx`.**

### OPS-015 — `infra/{nginx,systemd,deploy}/.gitkeep` deleted — the directories now hold real files, and a leftover `.gitkeep` invites the next reader to think the directory is still a placeholder

*Accepted · 2026-08-07 · WP9.*

`infra/{nginx,systemd,deploy}/.gitkeep` deleted — the directories now hold real files, and a leftover `.gitkeep` invites the next reader to think the directory is still a placeholder.

### OPS-016 — Adding testcontainers-go bumped `github.com/ebitengine/purego` v0.9.1 → v0.10.0 for the whole module — including tursogo's FFI path

*Accepted · 2026-08-07 · WP9.*

MVS, not a choice: `tursogo`/`turso-go-platform-libs` v0.7.2 require purego v0.9.1, while `testcontainers-go` v0.43.0 and its `gopsutil/v4` dependency require v0.10.0, so the maximum wins and the database driver's `dlopen`/FFI shim now runs on a purego its author did not pin. Neither a `replace` nor an `exclude` is a fix (it would only move the mismatch onto testcontainers). Evidence that it is fine today: `make server-test` (including WP1's two-handles-on-one-file, WAL-checkpoint and second-process-lock tests) and `make test-integration` (a real catlogd binary, real Turso files, the full ingest chain) are both green. Carry this forward with WP1's existing rule for tursogo — **every bump of tursogo or purego needs a behaviour re-probe**, and if the pairing ever breaks, the escape hatch is to move `internal/nginxproxy` into its own Go module so the test-only dependency stops constraining the server's.

### OPS-017 — `go mod tidy` is what keeps the build-tagged dependencies honest, and `go get` alone is not

*Accepted · 2026-08-07 · WP9.*

Because the only importer of testcontainers-go and moby/moby/client sits behind `//go:build docker`, `go get` recorded both as `// indirect`; `go mod tidy` (which considers all build tags) promotes them to direct requires. Anyone editing `server/go.mod` by hand should tidy afterwards rather than trust an untagged `go build`.

---

## Documentation

Decisions about the documents themselves.

### DOCS-001 — [CONSTITUTION.md](CONSTITUTION.md) added — the standing principles this log records decisions *against*

*Accepted · 2026-08-07 · docs.*

The constitution says what catlog optimises for (privacy, cost, the player's frame budget, local-first, an immutable log, derived-never-claimed numbers, trivial moderation, proportionate anti-cheat); `DECISIONS.md` stays what it has always been — what we actually chose, and why. Its §8 records the owner's governing rule that anti-cheat assumes stock KSA data and settings and goes no further, with a five-part test for "too far"; [integrity-audit.md](integrity-audit.md) is the first audit against it. Result: nothing in code today is too far, three items flagged Borderline (the `sid`/`seq`/`ph` chain's tamper-evidence claim, which the code does not deliver and which no surface reads; the rebuild's ±2 s KIA window, now largely redundant with `ImpactCorrelator`; and `peak_g_survived`'s incremental-vs-rebuild divergence), and four of the six `plans/CATLOG_PROPOSALS.md` §4.3 layers — physics plausibility, quarantine, z-scores/suspicion, report queues — are settled as **never to be built**.

### DOCS-002 — `plans/` is deleted; `docs/` is the only record, and the `§N` numbering survives inside it

*Accepted · 2026-08-08 · docs.*

`plans/INITIAL_IMPL_PLAN.md`, `plans/CATLOG_PROPOSALS.md` and `plans/INITIAL_OUTLINE_PLAN.md` were pre-build artefacts: a specification written before the code, a set of proposals that the research had already partly overturned, and a first outline. Everything in them is now either true and better stated by the code, or superseded and recorded above. Keeping them invited the failure they were already causing — a reader treating the plan as authoritative and the divergence as a bug.

**The obstacle was the cross-references, and they were not negotiable.** 1,719 `§N` citations across 292 source files point into the plan's section numbering — `§4.5.3` in the auth chain, `§5.4` at every storage rule, `§4.7` throughout identity. Rewriting them would have been a 292-file mechanical edit touching security-sensitive comments for no gain, and deleting the plan without doing it would have left every one of them dangling.

**So the numbering moved instead of dying.** `docs/` is the successor spec and each document declares which `§` it now owns; [ARCHITECTURE.md](ARCHITECTURE.md#5-the--section-index) carries the index that maps every section number a comment can cite to the document that now defines it. A `§4.5.3` in a Go comment resolves in one lookup, exactly as before, and the document it lands in is one that is maintained. Sections that only ever described work sequencing — the work-package breakdown, the risk register — are gone rather than rehomed; what survived of them is in [ROADMAP.md](ROADMAP.md).

**The rule this creates:** a new `§` number is never minted. The numbering is a stable citation space inherited from the plan, and it is frozen. New material gets a document and a heading, and code cites it by name.
