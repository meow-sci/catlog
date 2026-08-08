# catlog architecture

What catlog is made of, where each piece lives, and how a number gets from a kitten's bad landing
onto a public leaderboard.

This is the map. The detail lives in the documents it points at, and the reasons live in
[DECISIONS.md](DECISIONS.md).

- **[CONSTITUTION.md](CONSTITUTION.md)** — what catlog optimises for. Read before deciding anything.
- **[DECISIONS.md](DECISIONS.md)** — what it decided, and why. Read before re-opening anything.
- **[ROADMAP.md](ROADMAP.md)** — what is not built, and what is deliberately not going to be.
- **[../DEVELOPMENT.md](../DEVELOPMENT.md)** — how to build, run and test it.

---

## 1. The shape of it

```
KSA (the game)
   │  Harmony patches + a 2 Hz sampler
   ▼
mod/catlog ────────────── mod/catlog.lib ──────────────┐   the KSA-free core:
 (the only code that       detector · 30 s windows ·   │   detect, spool, sign, ship
  touches the game)        SQLite outbox · ES256 proof │
                                                       │  POST /v1/ingest
                                                       │  brotli(NDJSON), signed per batch
                                                       ▼
                                              server/  catlogd
                                                       │
                            ┌──────────────────────────┼──────────────────────────┐
                            ▼                          ▼                          ▼
                       events.db                  projector                  projections.db
                    the immutable log   ──fold──▶  one goroutine   ──write──▶  boards, profiles,
                    (append only)                                              feed, census
                                                                                    │
                            ┌───────────────────────────────────────────────────────┤
                            ▼                                                       ▼
                   site/ + server/internal/web                                    spa/
                   server-rendered datastar site                        static React reader
                   (same origin, sessions, dashboard)                   (any host, read-only)
```

Two properties do most of the work:

**The log is the only thing that matters.** Events are appended and never rewritten. Every board,
profile, feed line and statistic is a projection that can be deleted and rebuilt from the log — which
is why the nightly rebuild is a real correctness backstop rather than a ritual, and why an
"we should have recorded that differently" is an upcaster rather than data loss (Constitution §5).

**The server decides what a number is worth.** The mod reports what happened; nothing on the wire is
a stat. Leaderboard values are always folded from events, stat keys are compile-time constants, and
enums are allow-lists (Constitution §6).

---

## 2. Repository layout

| Path | What is in it | Deeper |
|---|---|---|
| `server/` | Go module `github.com/meow-sci/catlog/server`. Three binaries: `catlogd`, `catlogctl`, `mockidp`. | [server.md](server.md) |
| `mod/` | .NET 10 solution: `catlog.lib` (KSA-free core), `catlog` (the game mod), `catlog.sim`, `catlog.loadgen`, two test projects. | [mod.md](mod.md) |
| `site/` | The datastar site's static assets (CSS, three JS modules, vendored datastar) and the Playwright e2e suite. **HTML is rendered by the Go server** — the templates live in `server/internal/web/templates/`. | [ui-design.md](ui-design.md) |
| `spa/` | The React reader: a standalone Vite app over the public read API. Own lockfile, own toolchain, own deployment. | [ui-design.md](ui-design.md), `spa/README.md` |
| `contracts/` | Cross-language conformance vectors, generated deterministically and consumed by **both** the Go and C# suites. This is what guarantees mod↔server interop without the game. | [ingest-api.md](ingest-api.md) |
| `docs/` | This directory. The specification, the decisions, the design record. | — |
| `infra/` | nginx configs, systemd units, the deploy script, an optional compose file. | [operations.md](operations.md) |
| `scripts/` | `e2e-full.sh` (the whole-stack proof) and `db-snapshot.sh`. | [../DEVELOPMENT.md](../DEVELOPMENT.md) |
| `data/` | Runtime state, git-ignored: `events.db`, `projections.db`, `keys/`, `archive/`. | — |

Four toolchains: **Go 1.26**, **.NET SDK 10**, **Node 24 + pnpm 11**. `pnpm` only — never `npm`,
`npx` or `yarn`. Docker is optional and used by exactly one test suite.

### The two frontends

catlog has two, and they are independent by design — same data, two UI patterns, kept side by side
so they can be compared:

|  | `site/` + `server/internal/web/` | `spa/` |
|---|---|---|
| Rendering | Server-rendered Go templates, datastar for interactivity | React 19, client-rendered, React Compiler on |
| Auth | Sessions, the dashboard, credential issuance | **None.** Anonymous, read-only |
| Hosting | Served by `catlogd` (nginx serves `/static/` in prod) | Any static host; GitHub Pages workflow included |
| Talks to | Everything | Seven `GET /v1/…` endpoints, cross-origin |
| Build | `make site-build` (esbuild) | `make spa-build` (vite) |

Neither requires the other to be running, and they can live on different domains. The only thing
tying them together is the read API and the CORS allow-list that lets a browser reach it. The design
contract they both implement is [ui-design.md](ui-design.md).

---

## 3. Ports, paths and processes

| Thing | Value |
|---|---|
| `catlogd` public HTTP | `127.0.0.1:8080` |
| `catlogd` admin HTTP — loopback only, never proxied | `127.0.0.1:6060` |
| `mockidp` (the local stand-in for Discord/Google/GitHub) | `127.0.0.1:9090` |
| `spa/` vite dev server | `127.0.0.1:5173` |
| `spa/` vite preview (built bundle, cross-origin) | `127.0.0.1:4173` |
| nginx (dev, docker, optional) | `127.0.0.1:8081` |
| Data directory | `./data/` → `events.db`, `projections.db`, `keys/`, `archive/` |
| Dev issuer / proof `htu` base | `http://127.0.0.1:8080` |

**One process per database file.** tursogo takes an exclusive whole-file lock that shuts every other
process out entirely — not just other writers, *all* access. Three consequences that bite in
practice: `catlogctl` never opens a database (it is an HTTP client for the admin API), `make e2e`
runs on its own throwaway data directory, and an IDE database tool left open on `data/events.db`
stops `make dev` from starting at all. `make db-snapshot` exists for ad-hoc SQL.

---

## 4. How an event becomes a leaderboard row

1. **Detected in the game.** `mod/catlog` polls at 2 Hz and carries Harmony patches at the game's own
   choke points. Every KSA read is in one file and carries a `[KsaAnchor]` naming the `file:line` it
   was verified against.
2. **Turned into events by `catlog.lib`.** The detector compares frames as latched edges, the window
   accumulator folds 30 seconds of sim time into one `telemetry.window`, and the impact correlator
   holds an impact one frame so a destruction can still flip its `survived` verdict.
3. **Spooled.** Every envelope is appended to a local SQLite outbox with its serialized bytes stored
   verbatim, so the bytes the server hashes are the bytes the detector produced. The outbox is
   durable: nothing is deleted until the server answers `200`.
4. **Shipped.** The shipper compresses a batch to brotli NDJSON, mints a batch ULID, signs an ES256
   proof over the body hash and the stream chain, and POSTs it. A hard 30-second floor between
   requests is enforced at the point of transmission and is unreachable from the player's TOML.
5. **Verified.** `catlogd` runs the [§4.5.3 chain](ingest-api.md#server-verification-order-cheapest-first--implement-exactly-this-order)
   cheapest-check-first — structural parse, license, deny-list, credential row, proof, skew, rate
   limit, body hash, replay short-circuit, stream check — and only then decodes the body.
6. **Stored.** One writer goroutine owns `events.db`. Events insert with `INSERT OR IGNORE` on
   `(player_id, event_id)`, so a resend is free.
7. **Folded.** The projector wakes on the writer's notification, reads the log past its checkpoint,
   applies every registered fold in one transaction, and advances the checkpoint. Flagged flights
   score nothing.
8. **Served.** The read API answers from `projections.db` with `Cache-Control: s-maxage=30` so a CDN
   can absorb popularity for free, and pushes new feed rows to SSE subscribers.

The [idempotency contract](ingest-api.md#idempotency-contract) is a hard requirement at every step: a
client that does not know whether a request landed may send it again, blindly, with no ill effect.

---

## 5. The `§` section index

Roughly 1,700 comments across the Go, C# and TypeScript sources cite section numbers — `§4.5.3` in
the auth chain, `§5.4` at every storage rule, `§4.7` throughout identity. That numbering came from
the original implementation plan, which no longer exists; the numbers survived it because rewriting
them would have been a 292-file mechanical edit for no gain (DECISIONS.md, `DOCS-002`).

**This table is how you resolve one.** A `§N` in a comment means the section below, in the document
named.

| § | Subject | Now defined in |
|---|---|---|
| §1 | The locked decisions (D1–D22) | [DECISIONS.md](DECISIONS.md#locked-decisions-do-not-re-litigate) |
| §2 | Repository layout | this document, §2 |
| §3, §3.1 | Fixed local ports & paths | this document, §3 |
| §4.1 | Event envelope | [events.md](events.md) |
| §4.2 | Event taxonomy | [events.md](events.md) |
| §4.3 | Wire format & limits | [ingest-api.md](ingest-api.md) |
| §4.4 | Ingest HTTP API & responses | [ingest-api.md](ingest-api.md) |
| §4.5, §4.5.1–.2 | License & proof JWS | [ingest-api.md](ingest-api.md) |
| §4.5.3 | Server verification order | [ingest-api.md](ingest-api.md) |
| §4.5.4 | Sessions & CSRF | [ingest-api.md](ingest-api.md) |
| §4.6 | The credential file | [credential.md](credential.md) |
| §4.7 | Identity, `user_key`, handles, moderation | [identity.md](identity.md) |
| §4.8 | Read API, site routes, dashboard API | [ingest-api.md](ingest-api.md) |
| §4.9 | Error code registry | [ingest-api.md](ingest-api.md) |
| §4.10 | Conformance vectors | [ingest-api.md](ingest-api.md) |
| §5.1–§5.2 | Go module, dependencies, packages | [server.md](server.md) |
| §5.3 | Configuration | [server.md](server.md) |
| §5.4 | Storage discipline & DDL | [server.md](server.md) |
| §5.5 | Ingest pipeline | [server.md](server.md) |
| §5.6 | Projector & the boards | [server.md](server.md) |
| §5.7 | The server-rendered site | [server.md](server.md), [ui-design.md](ui-design.md) |
| §5.8, §5.8.1 | Identity endpoints, deny-list, `mockidp` | [server.md](server.md) |
| §5.9 | Admin API & `catlogctl` | [server.md](server.md) |
| §5.10 | Archiver | [server.md](server.md), [r2-archive-design.md](r2-archive-design.md) |
| §5.11 | Logging & secret hygiene | [server.md](server.md) |
| §6, §6.1–§6.3 | nginx: dev, prod, the testcontainers suite | [operations.md](operations.md) |
| §7.1 | .NET projects & the KSA-free demarcation | [mod.md](mod.md) |
| §7.2 | `catlog.lib` internals | [mod.md](mod.md) |
| §7.3 | `catlog.sim` scenarios | [mod.md](mod.md) |
| §7.4 | The game project | [mod.md](mod.md), [ksa-integration.md](ksa-integration.md) |
| §7.5 | Mod test suites | [mod.md](mod.md) |
| §8 | The site build & the e2e suite | [../DEVELOPMENT.md](../DEVELOPMENT.md) |
| §9, §9.1–§9.4 | Local development & orchestration | [../DEVELOPMENT.md](../DEVELOPMENT.md) |
| §10 | The "everything runs locally" guarantee | [CONSTITUTION.md](CONSTITUTION.md) §4, and §6 below |
| §11 | Deployment assets | [operations.md](operations.md) |
| §12 | Work packages | *gone — the build order is history; see [ROADMAP.md](ROADMAP.md)* |
| §13 | Risks & watch items | [ROADMAP.md](ROADMAP.md) |

**Never mint a new `§` number.** This is a frozen citation space inherited from a document that is
gone. New material gets a document and a heading, and code cites it by name — `docs/server.md`, not
`§5.14`.

---

## 6. What is simulated, and what may not be

Constitution §4 requires the whole system to run and be tested with no network call to anything.
These are the stand-ins, and this list is exhaustive — anything else must be real:

| Real thing | Local stand-in |
|---|---|
| Discord, Google, GitHub | `mockidp`, with the exact response shapes of each, and its own signed `id_token` for Google |
| Cloudflare R2 | `archive/fsStore`, using an identical key layout so the migration is `rclone copy` |
| The KSA game runtime | `catlog.sim` scenarios and `catlog.loadgen` careers, feeding the **real** lib pipeline |
| Cloudflare CDN | nothing — Go sets correct `Cache-Control` and a CDN is transparent |
| The VPS and its TLS | `infra/nginx/dev.conf` over HTTP; `prod.conf.example` is documented, not run |

The point of the list is what it forbids. `mockidp` is not a shortcut around the identity stack —
catlogd runs its real code exchange, real `id_token` verification, real `user_key` derivation and
real handle rules against it. The load harness does not hand-author an envelope; it drives the real
detector, outbox, signer and shipper. A test that fakes the thing under test proves nothing.

---

## 7. Keeping the documentation true

**This is a hard rule, not an aspiration.** catlog's documentation is load-bearing: it is the
specification two independent implementations are built against, and both a human and an agent
reading this repository will trust it over the code.

A change that makes a document wrong is an incomplete change. In the same commit:

| If you change… | You must update… |
|---|---|
| An event, envelope field or payload | [events.md](events.md), **and bump `ver`** on the affected event |
| An HTTP endpoint, status, header or error code | [ingest-api.md](ingest-api.md) |
| The credential file's shape | [credential.md](credential.md), **and bump `format`** |
| Handle rules, `user_key`, moderation semantics | [identity.md](identity.md) |
| A Go package's role, the schema, config keys, admin routes | [server.md](server.md) |
| A detector rule, the outbox, the shipper, a KSA patch point | [mod.md](mod.md), and [ksa-integration.md](ksa-integration.md) if a patch point moved |
| nginx, systemd, or the deploy script | [operations.md](operations.md) |
| A Make target, a build flag, a test scenario | [../DEVELOPMENT.md](../DEVELOPMENT.md) |
| Anything a visitor to the website would notice | [../README.md](../README.md) |
| Repo layout, ports, or a new top-level directory | this document |
| An integrity check | [integrity-audit.md](integrity-audit.md), against Constitution §8's five tests |

And **always**: a dated entry in [DECISIONS.md](DECISIONS.md), in the right area, saying *why* — not
only what. A decision without its reasoning gets re-litigated within the year; that is the entire
purpose of the file.

Two further rules:

- **Never leave a document describing something that is gone.** Delete the passage or mark it
  superseded. A stale paragraph is worse than a missing one, because it is believed.
- **Never mint a new `§` number** (§5 above).

`AGENTS`-facing detail, including where to look first for a given kind of task, is in
[../CLAUDE.md](../CLAUDE.md).
