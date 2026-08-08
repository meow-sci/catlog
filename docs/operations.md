# catlog operations (`infra/`)

Owns **§6, §6.1–§6.3 and §11** — the reverse proxy, the systemd units and the deploy script.
Reasons are in [DECISIONS.md](DECISIONS.md), area `OPS-*`.

**catlog produces deploy assets and provisions nothing** (D1). The VPS is owner-managed: DNS, TLS,
the firewall, package installation and the Cloudflare setup are all the owner's runbook, and
`deploy.sh` is written so that it *cannot* stray into them.

---

## §6 nginx

Two configs, and they are deliberately different kinds of file.

**`infra/nginx/dev.conf` is a complete `nginx.conf`** with two placeholders, `$UPSTREAM` and
`$STATIC_ROOT`. Two consumers substitute them differently: `infra/compose.yaml` mounts it as a
template and lets the nginx image's envsubst step write the real file, and the Go test suite
substitutes in-process. `NGINX_ENVSUBST_FILTER` is load-bearing in the compose path — without it,
envsubst also expands `$binary_remote_addr`, `$proxy_add_x_forwarded_for`, `$scheme` and `$host` into
empty strings.

**`infra/nginx/prod.conf.example` is a server-block fragment** for the distribution's own `http {}`,
not a whole config. Two consequences are documented in its header: the `limit_req_zone` /
`limit_conn_zone` declarations must be installed separately (a zone cannot live in a server block),
and the HTTP/2 directive is left as an explicit either/or rather than guessed, because `http2 on;`
needs nginx ≥ 1.25.1 while Debian 12 ships 1.22 and Ubuntu 24.04 ships 1.24.

Production zone sizing: `catlog_ingest` at 2 r/s burst 10 — roughly 4× a single player's budget, so a
household behind one NAT address is unaffected — and `catlog_web` at 20 r/s burst 40.

### The locations that matter

| Location | Why it is special |
|---|---|
| `/v1/ingest` | The body is brotli-compressed and **hashed byte for byte**. Never add a gunzip, brotli or `sub_filter` here — the config carries a standing comment saying so. |
| `/v1/feed/sse` | `proxy_buffering off`, `proxy_cache off`, long read timeout, `X-Accel-Buffering: no`. Buffering defeats streaming, and the contract is that a frame arrives in under a second. **Never compress.** |
| `/static/` | An `alias` at the built site. The one directive that can break silently — a 404 looks like a missing build rather than a broken proxy. |
| `/admin/` | `return 403`. Belt and braces: the admin mux binds loopback and additionally refuses non-loopback peers. |
| everything else | Proxied, with `gzip on` — response compression is the proxy's job, because catlogd has no compression middleware and will not gain one. |

### The Cloudflare hazard

`prod.conf.example` ships a `real_ip` block **commented out**, with the reason stated as a hazard
rather than a note. Per-IP zones key on the remote address, which becomes a Cloudflare edge address
once CF fronts the origin — so the zones must switch to `CF-Connecting-IP`.

**Enabling that before Cloudflare is in front and 443 is firewalled to CF's ranges is strictly worse
than having no rate limiting at all.** Any client can then choose its own bucket: a random value per
request makes the limiter unreachable, and a victim's address makes it a weapon. The spoofed value
also lands in the access log. The required order is: CF in front → firewall 443 to CF ranges → *then*
uncomment.

### §6.3 The test suite

`server/internal/nginxproxy`, behind `//go:build docker`, driving a real `nginx:stable-alpine`
container via testcontainers. Six subtests share **one** container — six containers would multiply a
cold image pull by six and prove nothing extra — and the ordering is deliberate: the burst test runs
last, because it empties the rate-limit bucket.

It asserts ingest round-trips through the proxy, `X-Forwarded-For` reaches the handler, an oversize
body is rejected by nginx and never reaches Go, a burst earns nginx's own 429 (with its HTML error
page rather than a JSON error body, which proves whose limiter answered), an SSE frame arrives in
under a second, `/admin/` is refused, and `/static/` is served without touching Go.

**The skip probe identifies the engine, not just "docker answers".** A podman socket linked at the
default docker path passes a naive health check and then dies inside container creation, because
testcontainers hardcodes Docker's default `bridge` network — and podman's compat API *fabricates* that
network on inspect while still rejecting it at create. So the probe requires a component named exactly
`Engine` (podman answers `Podman Engine`), and an engine reporting no components at all is trusted,
because a false skip is worse than a failure that names its own cause. The skip message says how to
fix it.

**The configs are not validated by `nginx -t` in CI.** What is checked is structural: balanced braces,
terminated directives, both placeholders present and substitutable, and every nginx variable surviving
substitution. **The first install on a VPS must run `nginx -t` before `systemctl reload nginx`.**

---

## §11 Deployment

### The binary cannot ship on `scratch` or `distroless/static`

`tursogo` is CGO-free, but purego's shim still emits a dynamically-linked ELF with a glibc
interpreter even at `CGO_ENABLED=0` — the two builds are byte-identical, same Go build id, with three
`DT_NEEDED` entries. The driver also extracts a native shared object to disk and `dlopen`s it at
startup, so a read-only or `noexec` temporary directory breaks it.

A glibc base — Debian or Ubuntu, which the target droplet is — is not a preference here, it is those
three `DT_NEEDED` entries. Alpine needs `-tags musl`. `windows/arm64` compiles and fails at runtime.

### systemd

`catlogd.service` runs as `User=catlog` out of `/var/lib/catlog`, hardened with `ProtectSystem=strict`,
`NoNewPrivileges`, `PrivateTmp` and a narrow `ReadWritePaths`. Two settings in it are load-bearing and
must not be "tidied":

- **`TURSO_GO_CACHE_DIR` points inside `ReadWritePaths`,** and `ExecStartPre` creates it. This is what
  makes the hardening survivable: `ProtectSystem=strict` makes everything outside `ReadWritePaths`
  read-only, and `PrivateTmp` gives a writable `/tmp` that a hardened host may still mount `noexec` —
  which turns a writable directory into a `dlopen` failure.
- **`MemoryDenyWriteExecute` must NOT be set.** The FFI shim and the `dlopen`'d engine need executable
  mappings. The unit carries it commented out with that warning, so nobody adds it back as "one more
  hardening flag".

`ReadWritePaths=/var/backups/catlog` belongs on **`catlogd.service`**, not on the nightly unit,
because `catlogctl backup` is an admin API call — the process that quiesces the writer and copies the
database is catlogd. A live Turso file cannot be read by another process at all, and `cp` of a WAL
database is not a backup anyway. The nightly unit therefore writes nothing and is pinned to loopback
only.

`catlog-nightly.{service,timer}` runs rebuild → archive → backup at 04:30 UTC. It is
correct-but-ahead-of-code in one respect and says so in a comment: enabling the timer requires all
three verbs to be live.

A purge frees pages without shrinking the file — `VACUUM` stays behind an experimental DSN flag that
catlog does not enable — so the honest reclamation path is restore-from-archive into a fresh
database, not vacuuming. `GET /admin/stats` reports both file sizes and both WAL sizes; that is the
number to watch.

### `deploy.sh`

```sh
infra/deploy/deploy.sh --host catlog@vps.example.com [--dry-run] [--install-units]
```

Cross-compiles for linux/amd64, builds the site, rsyncs into a staging directory, then over ssh:
**stop**, install, sync static assets, **start**, and wait for `/healthz`.

**It stops before it starts, and that is not a bug.** The exclusive whole-file lock means two catlogd
processes cannot share the database files, so no rolling or blue-green deploy is possible. The stop
is explicit — `systemctl stop`, then poll `is-active` for up to 60 s, then *refuse to install* —
rather than a `restart`, because the guarantee needed is "the old process has exited and released the
lock". Expect a few seconds of 502; the mod's shipper is built for exactly that and loses nothing.

It keeps one previous generation and prints the rollback command if `/healthz` does not answer within
60 s. `--dry-run` is total: every mutating step goes through one wrapper, so a dry run builds nothing
and creates no staging directory.

**What it will never do:** create users, directories or databases; install packages; write the
production config or environment file; touch nginx's configuration; obtain or renew certificates;
change firewall rules. The nginx and systemd files are copied into staging only, for the owner to
diff and install by hand — the nginx config is a `.example` with placeholders and TLS is owner-managed,
so installing it automatically could only ever be wrong.

### Disaster recovery

`catlogctl backup` copies `events.db` **and its `-wal`** after quiescing the writer. `projections.db`
is never backed up — it is rebuildable by design.

The archive holds the raw event log and the `player` rows and nothing else; handles, credentials, bans
and tombstones are identity state. So the runbook is: **restore the backup, then replay any archive
newer than it, then rebuild projections.** A restored server's first archive run is a no-op, and that
is correct.
