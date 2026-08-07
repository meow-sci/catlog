# catlog decisions

Two parts:

1. **Locked decisions** — the §1 table from [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md), reproduced here so implementers never have to leave `docs/`. Do not re-litigate these.
2. **Deviations & resolved decisions** — an append-only log. Newest entries go at the **bottom**. Every work package that deviates from the plan, or that resolves something the plan left open (library versions, package names, verified facts), adds one line with the date and a rationale.

---

## Locked decisions (do not re-litigate)

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

## Deviations & resolved decisions

Format: `YYYY-MM-DD · WPn · <what changed> — <why>`.

- 2026-08-06 · WP0 · **A1: `mod/Directory.Build.props` resolves the KSA assemblies dynamically** instead of assuming the Windows game install the plan (§7.1, via `unscience/Directory.Build.props`) assumed. Ladder copied from `gatOS/Directory.Build.props`, first match wins: `$(KSA_DLL_DIR)` → sibling `../../ksa-game-assemblies/current/dll/` → `C:\Program Files\Kitten Space Agency\` → `$(HOME)/repos/meow-sci/ksa-game-assemblies/current/dll/` — so the game project builds on this macOS dev machine, which has no game install. Verified: it resolves to the sibling checkout (37 assemblies incl. `KSA.dll`, `Brutal.*.dll`, `Planet.*.dll`).
- 2026-08-06 · WP0 · **A1 (cont.): `KSAUserDir` on non-Windows is `$(HOME)/Documents/My Games/Kitten Space Agency/`** (gatOS points it at its own mods checkout instead) and the dist override env var is `CATLOG_DIST_DIR` — catlog needs the real per-OS KSA user dir, not a repo-local one.
- 2026-08-06 · WP0 · **A1 (cont.): `ImplicitUsings` is `disable`**, unlike gatOS which enables it — plan §7.2 house style; every using in `mod/` is explicit.
- 2026-08-06 · WP0 · **A2: `mod/catlog/` is not permanently excluded from `mod/catlog.slnx`.** Plan §7.1 excludes it because a KSA-less machine cannot build it; A1 makes the DLLs resolve here, so WP8 adds `catlog/catlog.csproj` to the solution rather than building it out-of-band. The slnx carries a comment saying so.
- 2026-08-06 · WP0 · **A3: the current game build is `2026.8.5.5168`** (`ksa-game-assemblies/current/version.json`), not `2026.7.3.4826` as the plan states. `docs/events.md` uses the new value in the `session.started` example. Consequence: the `CATLOG_PROPOSALS.md` §1.3 patch table was verified against the **older** decomp and must be re-verified against `ksa-game-assemblies/current/decomp` in WP8 before any Harmony patch is written.
- 2026-08-06 · WP0 · **A4: nothing in WP0 depends on docker** (the daemon is down on this machine). `make test-nginx` is a stub until WP9 and `make test` never touches docker, per §9.4.
- 2026-08-06 · WP0 · `.gitignore` adds `*.db-shm` alongside the §2 list — SQLite/Turso writes a shared-memory file next to every WAL, and leaving it untracked-but-unignored dirties every status check.
- 2026-08-06 · WP0 · Test framework for `mod/` is **xunit 2.9.3** + `xunit.runner.visualstudio` 3.1.4 + `Microsoft.NET.Test.Sdk` 17.14.1 (the versions the .NET 10.0.100 SDK's own `dotnet new xunit` template pins). Note the sibling gatOS repo uses NUnit; the plan (§7.1) specifies xunit for catlog, so catlog does not follow gatOS here.
- 2026-08-06 · WP0 · Site dev dependencies pinned exactly (no `^`): `esbuild` 0.28.1, `@picocss/pico` 2.1.1, `@playwright/test` 1.62.1. `packageManager` is `pnpm@11.12.0` per §8.
- 2026-08-06 · WP0 · The **datastar npm package is deliberately unresolved.** `site/scripts/build.mjs` carries a `TODO(WP5)` where its browser bundle joins the vendor copy list; WP5 resolves the real package name under the starfederation org at `pnpm add` time and records name + version here. Same applies to the Go SDK (`github.com/starfederation/datastar-go`) at WP1/WP5 `go get` time.
- 2026-08-06 · WP0 · pnpm 11 no longer reads the `pnpm` field in `package.json`; build-script approval moved to `site/pnpm-workspace.yaml` (`allowBuilds: esbuild: true`). Without it a clean `pnpm install` exits 1 with `ERR_PNPM_IGNORED_BUILDS` and `make bootstrap` fails.
- 2026-08-06 · WP0 · `server/go.mod` has **zero dependencies** so far — none of the §5.1 packages are imported yet, and an unused `require` block would not survive `go mod tidy`. WP1 adds them with `go get <pkg>@latest` and records every resolved version here, as §5.1 requires.
- 2026-08-06 · WP0 · `server/internal/` contains exactly the §5.2 packages (each with a `doc.go`). `internal/nginxproxy/` from §6.3 is **not** pre-created — WP9 creates it with its test file, since a package holding only a build-tagged test file would otherwise be empty.
- 2026-08-06 · WP0 · The playwright config in §8 references `make -C .. server-run-test-env` and `make -C .. mockidp-run`, which are not in the §9.1 target list. They are **not** in the WP0 Makefile; WP5 adds them together with `e2e`.
