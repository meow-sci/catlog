# Developing catlog

How to build, run and test every part of catlog, and what each testing mode actually exercises.

New to the codebase? Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) first — it says what the
pieces are. This document says how to drive them.

---

## Prerequisites

| Toolchain | Version | Needed for |
|---|---|---|
| Go | 1.26 | `server/` |
| .NET SDK | 10 | `mod/` |
| Node + pnpm | 24 / 11 | `site/`, `spa/` |
| Docker | any | **optional** — one test suite (`make test-nginx`) |

**pnpm only.** Never `npm`, `npx` or `yarn` — the two frontends have their own lockfiles and a
foreign package manager will rewrite them.

**A KSA install is optional but the solution assumes one is findable.** `mod/Directory.Build.props`
resolves the game's reference assemblies through a ladder — `$KSA_DLL_DIR`, then a sibling
`ksa-game-assemblies/current/dll/` checkout, then the per-OS install path. With none of them,
`mod/catlog` (and therefore `make mod-build`) cannot compile. Everything else builds anywhere.

```sh
make bootstrap    # go mod download · dotnet restore · pnpm install ×2
make build        # server binaries · .NET solution · site/dist · spa/dist
make test         # unit tests: go + catlog.lib + spa. No docker, no network.
```

`make help` lists every target.

---

## The dev loop

```sh
make keys         # once: creates data/keys/{license-signing.pem,session.key,pepper.key}
make site-build   # once, and after any change under site/assets/
make dev
```

`make dev` runs three processes in the foreground and stops all of them on Ctrl-C:

| | URL | What |
|---|---|---|
| `catlogd` | <http://127.0.0.1:8080> | The read API, the ingest endpoint, and the server-rendered datastar site |
| `spa` | <http://127.0.0.1:5173> | The React reader (vite, with HMR) |
| `mockidp` | <http://127.0.0.1:9090> | Stand-in Discord, Google and GitHub |
| `catlogd` admin | <http://127.0.0.1:6060> | Loopback-only admin API — seed, rebuild, stats, ban |

Then: log in at <http://127.0.0.1:8080/login> through any `mockidp` button, claim a handle, download
the credential file, and drive a real flight through the whole pipeline:

```sh
make sim SCENARIO=hop-lithobrake CRED=$HOME/Downloads/catlog-credential.json
```

Both frontends update while it runs — the feed over server-sent events, with no reload.

**`make seed`** inserts a deterministic demo dataset (`demo_ace`, `demo_tumbler`, `demo_crasher`)
if you want boards to look at without flying anything. It is idempotent.

### Variants

| Command | When |
|---|---|
| `make dev-server` | catlogd + mockidp only, no reader. What `make loadgen` and the e2e suites want. |
| `make spa-dev` | Just the reader's vite server, against a catlogd you started yourself. |
| `make spa-preview` | The **built** bundle on its own origin at `:4173`. See "CORS" below. |

### Why the SPA runs at `:5173` and not behind catlogd

Vite proxies `/v1` to `CATLOG_DEV_API` (which `make dev` threads from `SERVER_URL`), so the reader
runs **same-origin** in development and does not depend on the server's CORS allow-list being right.
That is the point — but it also means `make dev` cannot catch a CORS mistake.

`make spa-preview` is the other half: a built bundle on a different origin, cross-origin against
catlogd, which is the shape a *separately hosted* reader has. Both ports are already in
`catlogd.dev.toml`'s `[cors] allowed_origins`.

**Production is not that shape.** nginx serves the reader at `/app/` on the same origin as the read
API, built with an empty `VITE_CATLOG_API_BASE`, so `[cors] allowed_origins` is **empty** there
([UI-056](docs/DECISIONS.md#ui-056)). `make spa-preview` remains the only local target that exercises
the allow-list, and the allow-list remains live code for anyone hosting the reader elsewhere — exact
`scheme://host[:port]` strings only, because catlogd refuses to start on a wildcard or a trailing
slash and a malformed entry silently never matches.

### Configuration

`server/catlogd.dev.toml` is committed and is what makes "everything runs locally" true — every IdP
points at `mockidp`, so no real Discord/Google/GitHub request is ever made in development or in any
test. Any value is overridable from the environment as `CATLOG_<SECTION>_<KEY>`:

```sh
CATLOG_SERVER_LISTEN=127.0.0.1:9999 make dev
CATLOG_DATA_DIR=/tmp/catlog-scratch make dev
```

Production keeps its secrets in the environment, never in a file.

---

## Building each part

| Target | Runs | Output |
|---|---|---|
| `make server-build` | `go build ./cmd/...` | `server/bin/{catlogd,catlogctl,mockidp}` |
| `make mod-build` | `dotnet build mod/catlog.slnx -c Release` | five projects, incl. the game mod |
| `make site-build` | esbuild + asset copy | `site/dist/` — served at `/static/` in dev, by nginx in prod |
| `make spa-build` | `tsc -b && vite build` | `spa/dist/` — `index.html`, `404.html`, hashed assets |

### Building the datastar site

`site/scripts/build.mjs` bundles `assets/js/*.js` with esbuild and copies the CSS, the Inter subset
and the vendored datastar bundle into `dist/`. There is no dev server and no framework: the Go server
renders every page, and serves `site/dist` at `/static/` when `[server] static_dir` is set. In
production that key is left empty and nginx serves the same tree.

**Re-run `make site-build` after editing anything under `site/assets/`.** Nothing watches it.

### Building the React reader

`VITE_CATLOG_API_BASE` is the read API's origin, **baked in at build time**:

| Value | Meaning |
|---|---|
| unset | `http://127.0.0.1:8080` — the local dev server (`spa/.env`) |
| `""` | same origin; requests come out as `/v1/…` (`spa/.env.development`, used by `pnpm dev`) |
| `https://catlog.example` | a deployed API |

A real environment variable wins over `.env`, which is how the GitHub Pages workflow points a build
at production. `SPA_BASE=/sub/` builds for a subpath deployment; the router reads the same value back
out of `import.meta.env.BASE_URL`, so nothing in the source assumes `/`.

**A static host must answer an unmatched path with `index.html`** — the router uses real paths, not
fragments. The build emits `dist/404.html` as a byte copy of `index.html`, which is what GitHub Pages
needs; nginx wants `try_files $uri $uri/ /index.html`. Without it, deep links break and *nothing else
does*, so test a deep link rather than the home page. `spa/README.md` has the per-host table.

---

## Testing

Five modes, in ascending order of how much they need to be real.

### 1. Unit tests — `make test`

**No docker, no network, no database files, no game.** Three suites:

| | What it covers |
|---|---|
| `make server-test` | Every Go package: the auth chain step by step, fold golden tests, migration idempotence, the rebuild-equals-incremental property, redaction, the unit formatter, secret hygiene. |
| `make mod-test` | `catlog.lib`: detector edges, window boundaries, impact correlation, outbox pruning and crash recovery, the JWS/JWK implementation, the shipper's recovery table on a virtual clock, and the **assembly guard** that proves zero KSA references. |
| `make spa-test` | The reader: the API client, the router, the unit-formatter port against generated vectors, page rendering, and an assertion that the React Compiler actually ran. |

This is the one that must always be green. `make spa-check` additionally runs the reader's
typecheck, lint and format check — that is what its CI does.

### 2. Integration tests — `make test-integration`

**Real binaries, real Turso database files, real HTTP, on random loopback ports. No game, no
browser.** Two legs:

- `server/integration` (`-tags integration`) builds and boots `catlogd`, mints credentials, and
  drives ingest, identity, idempotency, the read API and archive restore end to end.
- `mod/catlog.integration.tests` spawns `server/bin/catlogd` on throwaway data directories and ships
  through the **real** `catlog.lib` pipeline against it: acceptance, replay, tampering, clock skew,
  revocation, and the `413` halving ladder.

This is where mod↔server interop is proven for real. The static half of that guarantee is
`contracts/testdata` — deterministic vectors generated by `make testvectors`, consumed by both the Go
and C# suites, and asserted byte-identical on regeneration.

### 3. Functional / acceptance — `make sim`

**One scripted flight, one player, exact leaderboard assertions.** `catlog.sim` is the deterministic
acceptance tool: six scenarios, each driving the real detector → outbox → proof → shipper chain
against a live server, then asserting what the boards say.

```sh
make sim                                    # list the scenarios
make sim SCENARIO=hop-lithobrake CRED=… ASSERT=1
make sim SCENARIO=cheater CRED=… ASSERT=1   # proves a flagged flight scores nothing
make sim SCENARIO=soak CRED=… SPEED=100     # pace at 100 sim seconds per wall second
```

Assertions are baseline-relative, so a scenario is re-runnable against a database that already holds
data. Needs `make dev` (or `make dev-server`) in another terminal.

### 4. End-to-end — `make e2e`, `make e2e-full`

**A real browser (chromium) against a real server.**

```sh
make e2e        # playwright against a throwaway, seeded catlogd + mockidp
make e2e-full   # the whole stack: catlogctl issue → simulator → read API → browser
```

`make e2e` starts its own catlogd on a scratch data directory (`data-e2e/`), wiped at every run so
the seeded fixture is deterministic. **It must not race a `make dev`** — Turso's exclusive file lock
means one process per database, and port 8080 is already taken. Stop the dev server first.

The suite covers the journeys only a browser can prove: the OAuth dance through all three providers,
the account-age gate, the credential wizard (including an assertion that the private key never leaves
the page), the boards, the SSE feed arriving without a reload, and revoke / delete-my-data.

`spa/` has its own browser check, `make spa-smoke`: real chromium against a **built, served** bundle
and a seeded catlogd, testing a deep link from a cold context. Start `make spa-preview` first.

### 5. Load testing — `make loadgen`

**Hundreds of players, randomised careers, real identities, real crypto.** This is the other half of
`make sim`: where the simulator asserts exact values for one scripted flight, `catlog.loadgen`
invents plausible play for a whole population and asserts the invariants that must hold whatever it
generated.

```sh
make loadgen                                     # 25 players, 45 simulated minutes each
make loadgen PLAYERS=250 DURATION=3h ASSERT=1    # a serious run, invariants checked
make loadgen SEED=4242 REPORT=json               # reproducible; JSON on stdout
make loadgen LOADGEN_ARGS=--help                 # every flag, and what it measures
```

Needs `make dev-server` in another terminal. It touches nothing outside 127.0.0.1.

Every player is provisioned the way a real one is — `mockidp` mints a subject, catlogd runs the OAuth
code exchange, sets a session cookie and issues a license against a key pair generated in the harness
— and then drives the real pipeline. No envelope is ever hand-authored. Play is invented as a
**career**: players arrive with in-game time already on the clock and are only capable of what that
time has earned them, from pad tests through orbit and docking to probes to the outer system, with
failures weighted by flight phase. `--assert` checks fourteen invariants including zero silent loss,
no duplicate delivery, replay short-circuiting, refusal of every too-young account, and the projector
sitting at the head of the log.

`--seed` makes a run replayable; `--namespace` separates the *identities* from the *gameplay*, so the
same seed can re-run against a database that already holds the last run's players.

#### Making the numbers mean something

A default server is tuned for the internet, not for a load harness, and will happily spend an entire
run measuring its own rate limiter. Two settings decide whether a big run says anything.

**1. Turn off the per-credential token bucket.** The shipped limit is one batch per two seconds per
credential; on a standard 25-player run that was **99.6%** of all player time — 1,770 player-seconds
of waiting against 6.2 s of actual shipping.

```sh
CATLOG_LIMITS_RATELIMIT_DISABLED=1 make dev-server
```

It removes the rate-limit step from the chain entirely rather than configuring a huge rate, on the
principle that a control which is absent cannot be half-on. catlogd logs a WARN for as long as it
runs with it, and **refuses to start** if it is combined with an `https://` base URL, so it cannot
escape a laptop. Raising `[limits] ratelimit_per_jkt_per_s` is the ungated alternative and the right
answer for a real deployment, because that still leaves a limit in the chain.

**2. Set `--concurrency` to your core count.** The 4×-cores default is correct while the bucket is on
(players are network-bound waiting it out) and wrong the moment it is off, when they become CPU-bound
and oversubscription costs throughput. Same 300-player workload, limiter off: `-c 14` → 28,277
events/s, `-c 56` → 23,971, `-c 112` → 17,951, `-c 224` → 14,178.

**Then fatten the batches.** `--batch 2000` is the `[ingest] max_events` ceiling, and `--ship-age 1h`
lets the client ship what it has accumulated instead of holding it.

A million events on a 14-core laptop:

```sh
# terminal 1
CATLOG_LIMITS_RATELIMIT_DISABLED=1 make dev-server
# terminal 2
make loadgen PLAYERS=550 DURATION=2.6h BATCH=2000 SHIP_AGE=1h CONCURRENCY=14 SEED=4242 ASSERT=1
```

**1,058,811 events in 31.9 s**, all invariants green, `deduped 0` — 36,303 events/s over 1,677
batches at p99 319 ms, with catlogd peaking at 111 MB RSS and the read API serving 576 requests at a
p99 of 7.8 ms *while* the writer was saturated. Ingest is the ceiling; the projector keeps pace in
real time and is no longer the thing to fix.

`server/internal/projector/bench_test.go` measures the fold on its own, with no server, network or
harness in the way:

```sh
cd server && go test ./internal/projector/ -run '^$' -bench 'BenchmarkDrain' -benchtime 1x
```

#### Running it more than once

Runs accumulate, and that is fine — `--assert` measures *deltas*, and `--namespace` defaults to a
timestamp so each run mints its own players. Two practical notes:

- **`data/events.db` only grows.** A 550-player run adds about 340 MB, and there is no `VACUUM` by
  policy, so a purge does not give the pages back. For throughput work, point the server at a scratch
  database and delete it afterwards: `CATLOG_DATA_DIR=/tmp/catlog-load`.
- **One process per database file.** A stray `catlogd` or an IDE database tool holding
  `data/events.db` stops `make dev` from starting at all. `make db-snapshot` copies both databases to
  `./data-snapshot` for ad-hoc SQL.

### The optional one — `make test-nginx`

Drives **real nginx containers** via testcontainers to assert the reverse proxy's behaviour: rate
limit zones, the `413` before Go sees it, `/admin/` refusal, `/static/` serving, SSE frames arriving
under a second, and `X-Forwarded-For`. It clean-skips with a message when docker is unreachable, and
nothing in `make test` touches docker.

---

## Common tasks

| Task | Command |
|---|---|
| Create signing keys | `make keys` |
| Insert the demo dataset | `make seed` (or `POST :6060/admin/seed`) |
| Regenerate conformance vectors | `make testvectors` — byte-identical every run |
| Rebuild projections from the log | `POST :6060/admin/projections/rebuild` |
| Look at the databases | `make db-snapshot` then open `./data-snapshot` |
| Push one event and watch the feed | `POST :6060/admin/events` |
| Move the server's clock (dev only) | `POST :6060/admin/clock` |
| Remove build output | `make clean` (keeps `data/` and `node_modules/`) |
| Inspect the brotli/gzip ratios | `make precompress` (`CHECK=1` writes nothing) |

`POST /admin/clock` is what makes a rolling *yearly* leaderboard testable without waiting a year. It
is mounted only when `[server] clock_control = true`, catlogd refuses to start with it on an `https`
base URL, and the route lives on the loopback-only admin mux.

---

## Deploying

The production deployment is containers, and it is driven from here rather than from CI. The full
runbook — DNS, certificates, the firewall, triage — is [docs/operations.md](docs/operations.md).
What you type:

```sh
make deploy-env      # once: copy infra/deploy.env.example → infra/deploy.env and fill it in
make preflight       # read-only: local tools, secrets, the VM
make provision       # one-time and re-runnable: baseline, storage, docker, firewall, certs
make release         # build both images, smoke-test the stack, stream them to the VM over ssh
make deploy          # stop→start catlogd on the shipped images, health-gate, recreate nginx
```

Steady state is `make release && make deploy`. `make ops-status` and `make ops-logs` are the two you
will use when something is wrong; `make ops-logs` fetches a diagnostics bundle into `./diagnostics/`.

`make images-smoke` (run inside `make release`) is worth knowing about on its own: it brings the real
compose project up on a throwaway volume and proves the hardened base can actually run catlogd —
the glibc loader, the dlopen'd Turso engine, the key set, both migrations. Nothing about building the
image proves any of that.

`infra/deploy.env` is gitignored and is the only place a deployment secret exists outside the VM.
There is no vault and no `--extra-vars`.

**Ansible is not installed on your machine** — `scripts/ansible.sh` runs it in `alpine/ansible`,
which already ships every collection the playbooks import. Docker, `make` and `ssh` are the whole
local requirement. You can drive it directly when you want something the make targets do not wrap:

```sh
scripts/ansible.sh --check --diff playbooks/site.yml
scripts/ansible.sh playbooks/ops.yml --tags status
```

**Going from an empty VM to a running deployment** — including the DNS records, the Cloudflare zone
settings and the DigitalOcean firewall — is [docs/operations.md → Zero to running](docs/operations.md#zero-to-running).

---

## Conventions

- **Commits** are conventional: `feat(server): …`, `fix(mod): …`, `perf(loadgen): …`.
- **Every change keeps `make test` green**, and adds tests for what it changed.
- **Every change keeps the documentation true.** The table of what to update when is in
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#7-keeping-the-documentation-true), and every change gets
  a dated entry in [docs/DECISIONS.md](docs/DECISIONS.md) saying *why*.
- **Read [docs/CONSTITUTION.md](docs/CONSTITUTION.md) before making a design decision.** Most of the
  time the principle is why the code looks the way it does.
