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
| `mod/` | .NET 10 solution — `catlog.lib` (KSA-free core), `catlog.sim`, tests; the game mod joins in WP8 |
| `site/` | Static assets (CSS/JS) + the Playwright e2e suite. HTML is rendered by the Go server. |
| `docs/` | The normative contracts, extracted from the plan: [events](docs/events.md), [ingest & read API](docs/ingest-api.md), [credential](docs/credential.md), [identity](docs/identity.md), [decisions](docs/DECISIONS.md) |
| `contracts/` | Cross-language conformance vectors consumed by both the Go and C# test suites |
| `infra/` | nginx configs, systemd units, deploy script |
| `plans/` | Background: the outline plan and the proposal document |

[INITIAL_IMPL_PLAN.md](INITIAL_IMPL_PLAN.md) is the authoritative spec and the work-package
breakdown; `docs/DECISIONS.md` is the running log of everything that deviated from it.

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

Once the identity, ingest and UI work packages have landed, the loop is:

```sh
make keys && make dev
```

…then visit <http://127.0.0.1:8080>, log in through one of the `mockidp` buttons, claim a handle,
download your credential file, and drive a scenario through the real pipeline:

```sh
make sim SCENARIO=hop-lithobrake CRED=~/Downloads/catlog-credential.json
```

The board and the live feed update while it runs. Targets that are not implemented yet say so and
exit cleanly.
