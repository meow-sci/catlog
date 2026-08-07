# catlog

Passive telemetry from [Kitten Space Agency](https://www.rocketwerkz.com/) → community leaderboards.

A KSA mod watches your flights without asking you to do anything: situation changes, orbits,
staging, EVAs, dockings, rapid unscheduled disassemblies and 30-second telemetry windows are
detected client-side, spooled to a local outbox and shipped in signed, compressed batches to a
small Go server. The server stores the raw event log, folds it into leaderboards, and serves the
site — "biggest lithobrake survived", "fastest surface speed", "most kitten tumbles", and friends.

Design goals, in order: **you never send anything you didn't earn** (no email is ever requested
from any identity provider), **everything runs locally** (mock identity providers included, no
cloud service is contacted in development), and **the interesting code is testable without the
game** — the mod's core is a KSA-free library exercised by a console gameplay simulator.

## Layout

| Path | What |
|---|---|
| `server/` | Go module `github.com/meow-sci/catlog/server` — `catlogd`, `catlogctl`, `mockidp` |
| `mod/` | .NET 10 solution — `catlog.lib` (KSA-free core), `catlog.sim`, `catlog.loadgen`, tests, and the game mod |
| `site/` | Static assets (CSS/JS) + the Playwright e2e suite. HTML is rendered by the Go server. |
| `docs/` | The [constitution](docs/CONSTITUTION.md) plus the normative contracts, extracted from the plan: [events](docs/events.md), [ingest & read API](docs/ingest-api.md), [credential](docs/credential.md), [identity](docs/identity.md), [decisions](docs/DECISIONS.md) |
| `contracts/` | Cross-language conformance vectors consumed by both the Go and C# test suites |
| `infra/` | nginx configs, systemd units, deploy script |
| `plans/` | Background: the outline plan and the proposal document |

[INITIAL_IMPL_PLAN.md](INITIAL_IMPL_PLAN.md) is the authoritative spec and the work-package
breakdown; `docs/DECISIONS.md` is the running log of everything that deviated from it.
[docs/CONSTITUTION.md](docs/CONSTITUTION.md) sits above both: the standing principles catlog
optimises for — privacy, cost, the player's frame budget, local-first development, an immutable
log, and anti-cheat kept deliberately proportionate to a hobby project. Read it before making a
design decision; record the decision itself in `docs/DECISIONS.md`.

## Requirements

Go 1.26, .NET SDK 10, Node 24 + pnpm 11 (**pnpm only** — never `npm`/`npx`/`yarn`). Docker is
optional and only used by the nginx test suite.

## Quickstart

```sh
make bootstrap   # go mod download, dotnet restore, pnpm install
make build       # server binaries + .NET solution + site/dist
make test        # go unit tests + catlog.lib unit tests (no docker, no network)
```

Then check the server comes up:

```sh
server/bin/catlogd &
curl -s http://127.0.0.1:8080/healthz   # -> {"ok":true}
kill %1
```

`make help` lists every target.

## Dev loop

```sh
make keys && make site-build && make dev
```

…then visit <http://127.0.0.1:8080>, log in through one of the `mockidp` buttons, claim a handle,
download your credential file, and drive a scenario through the real pipeline:

```sh
make sim SCENARIO=hop-lithobrake CRED=$HOME/Downloads/catlog-credential.json
```

The board and the live feed update while it runs — the feed over server-sent events, with no reload.

`make dev` serves `site/dist` at `/static/`, so run `make site-build` after changing anything under
`site/assets/`. In production nginx serves the same tree and `[server] static_dir` is left empty.

## Load testing

`make sim` plays one scripted flight for one player and asserts exact leaderboard values.
`make loadgen` is the other half: **hundreds of players, randomised play, real identities**.

```sh
make loadgen                                     # 25 players, 45 simulated minutes each
make loadgen PLAYERS=250 DURATION=3h ASSERT=1    # a serious run, invariants checked
make loadgen SEED=4242 REPORT=json               # reproducible; JSON on stdout, progress on stderr
make loadgen LOADGEN_ARGS=--help                 # every flag, with what it measures
```

It needs `make dev` running in another terminal, and it touches nothing outside 127.0.0.1.

Each player is provisioned the way a real one is — `mockidp` mints a subject, catlogd runs the
OAuth code exchange, sets a session cookie and issues a license against a key pair generated in
the harness — and then drives the **real** `catlog.lib` pipeline: detector → SQLite outbox → ES256
proof → Brotli batch → `POST /v1/ingest`. No envelope is ever hand-authored. `--auth admin` swaps
the identity stack for `POST /admin/issue` when the question is about ingest alone.

Play is invented, not scripted, and it is invented as a **career**. Each player arrives with
in-game time already on the clock and is only capable of what that time has earned them: pad tests
and hops, then suborbital lobs, then orbit and orbital manoeuvres, then rendezvous and docking,
then transfers to other bodies, landings, and probes to the outer system. Fleet size and the
number of craft in flight at once grow the same way — one thing at a time for a beginner, five for
an explorer with a station, a lander on approach and a probe on its way to Saturn. Careers advance
during the run, so players cross stage boundaries while you watch.

Failure is career-appropriate and that is where the realism lives: beginners lose vehicles
*early* — on the pad, on ascent, at max-Q — and veterans lose them on final approach, on touchdown
and while closing on a docking port. The RUD cause follows from the phase, so all six occur in
proportions that match what the player was attempting. A lost flight is truncated to where it was
lost, so a pad failure really is four seconds of telemetry, one ignition and a crater.

Everything is flown around the solar system KSA actually ships — Earth, Luna, Sol, Mars, Phobos,
Venus, Jupiter and the rest — with radii, masses and atmosphere heights taken from the game's own
`Astronomicals.xml`, so orbital speeds are derived rather than invented and EVAs only happen where
a kitten could stand. On top of that: EVAs, dockings, tumbles, integrity flags and save reloads,
weighted so most flights are ordinary and records are rare. A small proportion of accounts are
minted deliberately too young for the 30-day age gate and are expected to be **refused**; another
few exercise reissue, revoke, admin ban and delete-my-data. The read API and the live feed are
hammered throughout.

`--seed` makes a run replayable: the same seed produces the same event stream whatever the
concurrency, and the report prints a digest of it to prove two runs agree. `--namespace` controls
the *identities* separately, so a seed can be re-run against a database that already holds the
last run's players.

`--assert` checks the invariants a run must have whatever it generated: zero silent loss
(`events.total` moved by exactly the number of envelopes produced), no unexplained non-2xx, no
duplicate delivery, a re-sent batch swallowed by the replay short-circuit, every too-young login
refused, the projector at the head of the log, every player visible on at least one board, all six
RUD causes produced, and — at twelve players or more, which is one full rotation of the career
ladder — every career stage populated with somebody off the home world.

The report's `careers` section says whether the population looked like a plausible player base:
stage distribution at the open and close of the window, career-age percentiles, fleet size per
player by stage, missions attempted against completed, and losses broken down by flight phase.

The client's hard 30-second ship floor is measured against an injected clock, exactly as
`catlog.sim` does, so a run compresses hours of play into seconds. **Every server-side limit stays
real** — the per-credential token bucket, the bounded write channel, the ±300 s proof-skew window —
and the report says which of them was actually binding. See `docs/DECISIONS.md` for what a run on
one laptop measured.

## End-to-end

```sh
make e2e        # playwright (chromium) against a throwaway, seeded catlogd + mockidp
make e2e-full   # the whole stack: catlogctl issue -> simulator -> read API -> browser
```

`make e2e` starts its own catlogd on a scratch data directory (`data-e2e/`), so it must not race a
`make dev` already holding port 8080 — Turso takes an exclusive lock on the database file, and only
one process may hold it (see `docs/DECISIONS.md`).
