# catlog operations (`infra/`)

Owns **§6 and §11** — the reverse proxy, the container images, the deployment and the runbook.
Reasons are in [DECISIONS.md](DECISIONS.md), area `OPS-*`.

Production is **two containers on one Linux x86_64 VM**, fronted by Cloudflare. Everything about
that box is described by `infra/ansible/`, and everything you do to it is a `make` target run from
your own machine.

> **The systemd path is being retired.** `infra/systemd/` and `infra/deploy/deploy.sh` still work and
> are still in the tree, but they are superseded by everything below and will be deleted once the
> container path has run a full deploy → upgrade → rollback → restore cycle against a real host.
> They are described at the end, under [The path being retired](#the-path-being-retired).

---

## The whole operator surface

```sh
make deploy-env      # once: copy infra/deploy.env.example → infra/deploy.env, fill it in
make preflight       # read-only: local tools, secrets, the VM. Changes nothing
make provision       # one-time and re-runnable: baseline, storage, docker, firewall, certs
make release         # build both images, smoke-test the stack, push to GHCR, record the digests
make deploy          # pull those digests, stop→start catlogd, health-gate, recreate nginx
```

Steady state is `make release && make deploy`. Everything runs from your machine — no CI required,
and `infra/deploy.env` is the only place a secret exists outside the VM (OPS-026).

| Target | Does |
|---|---|
| `make rollback` | return to the digests recorded in `/opt/catlog/deployed.json` |
| `make certs` | issue or renew, then reload nginx if the certificate changed |
| `make ops-status` | one screen: containers, version, health, cert expiry, disk, firewall |
| `make ops-logs` | a diagnostics bundle fetched into `./diagnostics/` (`SINCE=24h` widens the window) |
| `make ops-exec CMD='…'` | a `catlogctl` verb against the live admin mux |
| `make ops-backup [FETCH=1]` | a backup on the VM; `FETCH=1` copies it here and prompts first |
| `make ops-ssh` | an interactive shell on the VM |
| `make precompress` | the `.br`/`.gz` sibling generation, locally, to inspect the ratios |

`make deploy` prints both digest sets and asks before it stops anything (`CONFIRM=1` skips the
prompt). `make release` refuses a dirty working tree unless `ALLOW_DIRTY=1`.

---

## What is installed on the VM, and what is not

| | |
|---|---|
| **Your machine** | `docker` + `buildx`, `ansible-core`, `make`, `ssh` |
| **The VM** | Docker CE + compose plugin, `python3`, `openssh`, and optionally `unattended-upgrades` |

Deliberately absent from the VM: `go`, `node`/`pnpm`, `.NET`, `git`, `ansible`, `rsync`, `nginx`,
`certbot`/`acme.sh`, `logrotate`, any monitoring agent, any language runtime.

Three choices keep that list short:

- **nginx and acme.sh exist only as containers.** acme.sh is a one-shot run by a host timer, and
  nothing long-running holds the Docker socket (OPS-025).
- **No log files are written to the volume.** Both containers log to stdout, Docker's `json-file`
  driver rotates them, and `docker logs` is the single source (OPS-027).
- **The firewall is nftables plus `DOCKER-USER` rules**, both already available — no `ufw` (OPS-024).

---

## §6 nginx

Two files, split along the line of what depends on the domain (OPS-022).

**`infra/nginx/nginx.conf` is baked into the image.** Compression, the `limit_req`/`limit_conn`
zones, the `catlogd` upstream and the JSON log format are all domain-independent, so they ship with
the image and are validated by `nginx -t` **during the build** — which is what catches a brotli
module compiled against the wrong nginx version, in CI rather than on the VM at 3am.

**`site.conf.j2` and `realip.conf.j2` are rendered by Ansible** into `/etc/nginx/catlog.d/`. Names,
certificates and the Cloudflare ranges are the only host-specific things, and they are the only
things here.

Individual **files** are bind-mounted, never the directory, so the baked `00-bootstrap.conf`
survives. That file is the `default_server`: it answers 503 with a reason before Ansible has run, and
afterwards it answers any Host header that matches no `server_name` — a direct-IP scan, a stale DNS
entry, somebody else's domain pointed here.

`infra/nginx/dev.conf` is unchanged: a complete `nginx.conf` with `$UPSTREAM`/`$STATIC_ROOT`
placeholders, used by `infra/compose.yaml` and by the testcontainers suite.

**catlogd is resolved per request, not at parse time.** `proxy_pass http://$catlogd_upstream;` with
`resolver 127.0.0.11` rather than an `upstream {}` block, because an upstream block resolves its
names when the config is read — which fails `nginx -t` in the build, and, far worse, means an nginx
restarting during the deploy window (when catlogd is deliberately stopped) would fail to start and
stay down. Now it always starts, and answers 502 → the maintenance page, while catlogd is away
(OPS-029).

### The locations that matter

| Location | Why it is special |
|---|---|
| `/v1/ingest` | The body is brotli-compressed and **hashed byte for byte**. `brotli off; gzip off;` and never a `gunzip`, `brotli` or `sub_filter` filter. It also carries **no** `error_page` — the shipper needs the real 5xx to know to retry. |
| `/v1/{feed,events}/{sse,stream}` | `proxy_buffering off`, `proxy_cache off`, 1 h read timeout, `X-Accel-Buffering: no`, compression off in both directions. **Never compress a stream.** |
| `/static/` | The datastar site's assets, from the image. Pre-compressed; `expires 1h` because there is no content hashing in the site build. |
| `/app/`, `/app/assets/` | The React reader. `try_files … /app/index.html` for deep links; the hashed assets are `immutable` for a year because vite *does* hash. |
| `/admin/` | `return 403`. Belt and braces — the admin mux binds loopback inside the container's own namespace and refuses non-loopback peers. |
| everything else | Proxied, dynamically compressed, and the only place `error_page 502` shows the maintenance page. |

### Compression

`brotli_static on; gzip_static on;` serve the `.br`/`.gz` siblings that `scripts/precompress.mjs`
wrote at build time — brotli quality 11, which no request-time setting could afford. Measured on
`site/dist`: 132 kB → 40 kB, 70% saved.

Dynamic `brotli`/`gzip` at level 5 covers **proxied** responses only: catlogd's server-rendered HTML
and its JSON have no file to pre-compress, and catlogd has no compression middleware and will not
gain one. `text/event-stream` is in neither type list.

Which encoding a client gets is the client's choice. The one non-obvious property — that
`brotli_static` runs ahead of the built-in `gzip_static`, so a browser advertising both gets brotli —
is asserted in `scripts/container-smoke.sh` rather than believed.

### The Cloudflare hazard, and how it was resolved

Per-IP zones key on the remote address, which becomes a Cloudflare edge address the moment CF fronts
the origin. The fix is `real_ip` — and enabling `real_ip` while the origin is still reachable
directly is **strictly worse than having no rate limiting at all**: any client can then send its own
`CF-Connecting-IP` and choose its bucket. A fresh random value per request makes the limiter
unreachable; a victim's address makes it a weapon; the spoofed value also lands in the access log.

This used to be a commented-out block with a warning. It is now unconditional and safe, because
Ansible applies both halves from **one fact, in one run, in the required order**:
`roles/cloudflare_firewall` fetches the ranges and restricts 80/443 to them, then
`roles/catlog_nginx` renders `set_real_ip_from` from the same list — and refuses to render at all if
the firewall role has not run (OPS-024).

### The firewall detail that is easy to get wrong

**Published container ports never traverse the INPUT chain.** They are DNAT'd in `nat/PREROUTING`
and filtered in `FORWARD`, so a host ruleset that "blocks everything but 22" leaves 443 open to the
internet, silently. The rules that matter live in a `CATLOG-EDGE` chain jumped from `DOCKER-USER`.

`catlog-firewall.service` is `PartOf=docker.service` because restarting Docker re-creates and flushes
`DOCKER-USER`. `make ops-status` reports the rule count for the same reason.

### §6.3 The test suite

`server/internal/nginxproxy`, behind `//go:build docker`, driving a real nginx container via
testcontainers against `infra/nginx/dev.conf`. It asserts ingest round-trips, `X-Forwarded-For`
reaches the handler, an oversize body is rejected by nginx and never reaches Go, a burst earns
nginx's own 429 (with its HTML error page, which proves whose limiter answered), an SSE frame
arrives in under a second, `/admin/` is refused, and `/static/` is served without touching Go.

**The skip probe identifies the engine, not just "docker answers".** A podman socket linked at the
default docker path passes a naive health check and then dies inside container creation, because
testcontainers hardcodes Docker's default `bridge` network — which podman's compat API *fabricates*
on inspect while still rejecting at create. The probe requires a component named exactly `Engine`.

The production configuration is covered instead by `scripts/container-smoke.sh`, which runs the real
compose project against the real images. Both are worth having: the testcontainers suite is fast and
runs in `make test-nginx`; the smoke test proves the shipped artefact.

---

## §11 Deployment

### The images

| Image | Base | Size | Contents |
|---|---|---|---|
| `ghcr.io/meow-sci/catlogd` | `dhi.io/static:20250419-glibc-debian13` | 21 MB | `catlogd`, `catlogctl` |
| `ghcr.io/meow-sci/catlog-nginx` | `nginx:1.29` + `ngx_brotli` | | nginx, `site/dist`, `spa/dist`, both pre-compressed |

**The runtime base needs glibc, and needs as little else as possible.**

Glibc, because tursogo is CGO-free but purego's shim emits a dynamically-linked ELF even at
`CGO_ENABLED=0`. Read off the binary the build produces, by the `readelf` step in the Dockerfile:

```
interpreter  /lib64/ld-linux-x86-64.so.2
DT_NEEDED    libdl.so.2  libpthread.so.0  libc.so.6
```

The driver also extracts a 19 MB shared object and `dlopen`s it at startup, so the runtime needs a
writable, **exec-capable** directory too. `scratch` and `distroless/static` have no loader at all;
Alpine needs `-tags musl` and a different embedded `.so`.

As little else as possible, because **`dhi.io/golang:1.26-debian13` — the non-dev "runtime" variant —
carries `bash` and `/usr/local/go/bin/go`**: 18,764 files and 81 MB. It is a Go *runtime* image, and
an RCE landing in an image with a shell and a compiler gives back most of what the hardened base was
for. `dhi.io/static:…-glibc-debian13` is 1,148 files and 21 MB, with no shell, no package manager and
no compiler — just `ld-linux`, `libc`/`libdl`/`libpthread`/`libm`, `ca-certificates`, `tzdata` and a
`nonroot` user. See OPS-019.

The static base sets **no** default user, so `USER nonroot` (uid 65532) in the Dockerfile is
load-bearing. The `20250419` in the tag is a version label, not a build date.

`TURSO_GO_CACHE_DIR` points at `/var/lib/catlog/turso-cache`, a bind mount from the NVMe volume, and
`roles/storage` proves that filesystem can execute a file rather than trusting `findmnt`. A `noexec`
mount produces a startup crash whose message names a permission error on a file catlogd has just
successfully written — one of the least obvious failures in this system, which is why it is checked
in `preflight`, enforced in `storage`, and named in the smoke test's failure output.

**Cross-compilation is the normal case.** The development machine is macOS/arm64 and the target is
linux/amd64. `Dockerfile.catlogd`'s builder is `FROM --platform=$BUILDPLATFORM` with `GOOS`/`GOARCH`
from BuildKit's `TARGETOS`/`TARGETARCH`, so the Go toolchain runs natively at full speed and emits an
amd64 binary — and the build asserts the ELF machine type matches, so a cross-compile that hit the
wrong architecture fails there rather than on the VM. Only `Dockerfile.nginx`'s C stage (nginx +
ngx_brotli + a static libbrotli) runs emulated (OPS-031).

`mockidp` is never built into the image. It is a stand-in identity provider for development.

**`.dockerignore` is a security control, not an optimisation.** Without it, `docker build .` uploads
`data/` — the signing key, the session key and the pepper — into the daemon, where one careless
`COPY . .` in a future stage bakes them into a published layer (OPS-028).

### The layout on the VM

```
/opt/catlog/                compose.yaml (from git), .env (rendered), deployed.json
/mnt/catlog/                the NVMe volume — nosuid,nodev,noatime, NEVER noexec
├── config/    0750 root:catlog   catlogd.toml (0640), catlogd.env (0640)
├── data/      0750 <uid>         events.db, projections.db, keys/ (0700), archive/
├── turso-cache/ 0700 <uid>       the extracted libturso_sync_sdk_kit.so
├── backups/   0750 <uid>         catlogctl backup output
├── acme/      0700 root          acme.sh state; live/ is mounted read-only into nginx
└── nginx/conf/                   10-catlog.conf, 20-realip.conf
```

`<uid>` is the hardened image's nonroot user. `roles/catlog_app` asserts the image's configured user
matches `catlog_uid` before it deploys anything — a silent mismatch is a permission-denied on
`data/keys/` at first boot, which reads like a bug in catlogd rather than a uid mismatch.

### Why deploying costs a few seconds of 502

Turso's exclusive whole-file lock excludes other **processes**, and it is released by process
**exit**, not by the stop request. So there is no rolling deploy, no blue/green, and no `up -d`
recreate: `roles/catlog_app/tasks/deploy.yml` pulls first (a failed pull must cost nothing), stops,
polls until the container has actually exited, starts, and gates on the healthcheck — rolling back to
the previous digests and failing loudly if that gate does not pass.

The mod's shipper treats 5xx as retryable with backoff and spools locally, so **no telemetry is
lost**. nginx shows a maintenance page for the HTML surface only; `/v1/` deliberately keeps its real
5xx (OPS-020).

### Health checks in an image with no shell

`catlogd -healthcheck` performs one `GET /healthz` against its own listen address and exits 0 or 1.
It opens no database, permanently and on purpose: an exclusive lock means a database-touching probe
would fail exactly when the server is healthy.

### Disaster recovery

`catlogctl backup` copies `events.db` **and its `-wal`** after quiescing the writer — it runs *inside*
catlogd, because a live Turso file cannot be read by another process and `cp` of a WAL database is
not a backup anyway. `projections.db` is never backed up: it is rebuildable by design.

The archive holds the raw event log and the `player` rows and nothing else; handles, credentials,
bans and tombstones are identity state in `events.db`. So the runbook is **restore the backup, then
replay any archive newer than it, then rebuild projections** — which is exactly what
`playbooks/restore.yml` does, in that order, after prompting for the hostname. It moves the replaced
databases aside as `*.pre-restore` rather than deleting them.

`playbooks/restore.yml` has deliberately **no make target**: every step replaces live state.

A purge frees pages without shrinking the file — `VACUUM` stays behind an experimental DSN flag catlog
does not enable — so the honest reclamation path is restore-from-archive into a fresh database.
`GET /admin/stats` reports both file sizes and both WAL sizes; that is the number to watch, and
`make ops-logs` collects it.

---

## DNS and TLS

You do the DNS by hand; everything else is automated. **Two records**, and they are different on
purpose.

### The two records

| Name | Type | Value | Cloudflare proxy | Purpose |
|---|---|---|---|---|
| `catlog.<domain>` | `A` (+ `AAAA`) | the VM's public IP | **Proxied — orange cloud** | The public origin. Everything reaches catlog through this, behind Cloudflare's DDoS absorption and analytics. |
| `origin.catlog.<domain>` | `A` (+ `AAAA`) | the same IP | **DNS only — grey cloud** | The bypass. Resolves straight to the box, so one `curl` distinguishes "our origin is broken" from "the edge is broken". |

Set them in the Cloudflare dashboard under **DNS → Records**. The orange/grey cloud toggle is the
"Proxy status" column.

**`catlog.<domain>` is baked into every licence catlog issues** as the `htu` the mod signs against
(`[ingest] accepted_htu`, compared by exact string equality with no normalisation). Choose it once —
changing it later invalidates credentials already in the wild.

**The bypass name is not public access.** It is restricted to `CATLOG_DIRECT_ALLOW_CIDRS` at two
layers: the `CATLOG-EDGE` firewall chain, and an nginx rule keyed on `$realip_remote_addr` — the peer
address, before `real_ip` rewrote `$remote_addr`, because that is the one address a client cannot
choose for itself. A name that bypasses the DDoS front door must not be open to everyone.

Leave `CATLOG_ORIGIN_DOMAIN` unset if you do not want one. You will regret it the first time
something is wrong.

### Why ACME does not need either record to exist yet

The certificate is issued by **DNS-01**: acme.sh creates a `_acme-challenge` TXT record through the
Cloudflare API, Let's Encrypt reads it, and the record is removed. Nothing connects to the VM.

That matters twice. It works through an orange-clouded record, which **HTTP-01 does not** —
Cloudflare's "Always Use HTTPS" redirects `/.well-known/acme-challenge/` before it reaches the
origin. And it works before DNS points at the box at all, so `make provision` can complete on a host
nothing is routed to yet.

Both names go on **one certificate**, as SANs. `roles/acme` reads the SANs off the existing
certificate and reissues if a name is missing, so adding `CATLOG_ORIGIN_DOMAIN` later just works.

### The Cloudflare API token

Create it at **My Profile → API Tokens → Create Token → Create Custom Token**:

| Setting | Value |
|---|---|
| Permissions | `Zone` → `DNS` → **Edit** |
| | `Zone` → `Zone` → **Read** |
| Zone Resources | Include → Specific zone → `<domain>` |

Put it in `infra/deploy.env` as `CF_API_TOKEN`. Optionally add `CF_ZONE_ID` (shown on the zone's
Overview page) so acme.sh does not have to search for the zone.

**A Global API Key would work and must not be used.** It can do anything to every zone on the
account, and this credential is written to `/opt/catlog/.env` on a public-facing VM.

### Cloudflare zone settings

| Setting | Value | Why |
|---|---|---|
| SSL/TLS → Overview | **Full (strict)** | There is a real Let's Encrypt certificate at the origin. Anything less is either unencrypted to the origin or unauthenticated. |
| SSL/TLS → Edge Certificates → Always Use HTTPS | on | Nothing depends on plaintext; DNS-01 has no challenge path to break. |
| Caching → Cache Rules | cache `/static/*` and `/app/assets/*`; **bypass** `/v1/ingest`, every SSE path, `/auth/*`, `/api/*`, `/dashboard` | The read API already emits `s-maxage=30` and is designed for a CDN. The bypasses are cookie-authenticated, streaming, or byte-hashed. |
| Compression | leave on | It re-compresses at the edge; our origin brotli reduces the CF↔origin leg, which is the one we pay for. |
| Security → Bot Fight Mode | **off for `/v1/*`** | It would challenge the mod's shipper, which cannot solve a challenge. |
| Speed → Rocket Loader / Mirage | off | They rewrite HTML and defer scripts; the datastar pages depend on their own script order. |

**SSE needs excluding from both caching and compression.** Cloudflare buffers a stream otherwise, and
the symptom is frames arriving late — check the zone's Cache Rules and Compression before suspecting
nginx.

**Do not enable nginx's `proxy_cache` micro-cache with Cloudflare in front.** Two stacked shared
caches make every staleness question twice as hard.

### First bring-up, in order

```sh
make deploy-env                     # fill in infra/deploy.env
make ansible-deps
make preflight                      # read-only; fails naming every missing key at once
```

1. **Create both DNS records now.** ACME does not need them, but the firewall does not care and
   Cloudflare needs the zone to exist before the token can touch it.
2. `make provision` — baseline, NVMe mount, Docker, firewall, ACME, nginx, the compose project.
   Set `ACME_STAGING=1` in `deploy.env` for the first run: Let's Encrypt's production CA gives you
   five failed issuances an hour, and the staging CA has no meaningful limit.
3. `make release && make deploy`.
4. Verify **through the bypass first**, which tests the origin with Cloudflare out of the picture:
   ```sh
   curl -fsS https://origin.catlog.<domain>/healthz          # {"ok":true}
   curl -fsS https://catlog.<domain>/healthz                 # same, through Cloudflare
   curl -sI  -H 'Accept-Encoding: br' https://catlog.<domain>/static/css/catlog.css | grep -i content-encoding
   ```
5. Turn off `ACME_STAGING`, `make certs`, and confirm the certificate is trusted.
6. Only then set `CATLOG_HSTS_MAX_AGE=31536000` and re-run `make provision --tags nginx`. HSTS is a
   promise you cannot take back for `max-age` seconds.

### Renewal

`catlog-acme.timer` runs daily with a 30-minute jitter. acme.sh only acts within 30 days of expiry,
so it is a no-op on most days — which is what you want from something that must never be the reason a
renewal was missed. When the certificate changes, the host script reloads nginx (`nginx -t` first);
when it does not, nothing happens. `make ops-status` reports the expiry date.

---

## Triage

`make ops-status` first. `make ops-logs` when that is not enough — it collects, over the existing SSH
connection, into `./diagnostics/<timestamp>/`:

`compose-ps.txt` · `catlogd.log` · `nginx.log` · `admin-stats.json` · `deployed.json` ·
`inspect-catlogd.json` · `mounts.txt` · `disk.txt` · `mem.txt` · `dmesg-oom.txt` · `cert.txt` ·
`firewall.txt` · `units.txt` · `versions.txt` · the rendered nginx configuration and `catlogd.toml`

**It never collects `catlogd.env`, `data/keys/` or the databases.** A bundle containing the session
key or the pepper is one you have to treat as a secret forever, and you will not. Pulling data is
`make ops-backup FETCH=1`, which prompts.

| Symptom | The file that names the cause |
|---|---|
| catlogd exits at startup | `catlogd.log` (a `dlopen` or shared-object error) + `mounts.txt` (`noexec`) |
| catlogd cannot write | `inspect-catlogd.json` — the container's user against the volume's owner |
| 502s after a deploy | `deployed.json` + the restart count in `inspect-catlogd.json` |
| ingest failing for everyone | `catlogd.log` for `htu` mismatches — `accepted_htu` is compared by exact string equality |
| SSE frames arriving late | `nginx.log`, then Cloudflare's Cache Rules and Compression |
| the disk filling | `admin-stats.json` WAL sizes — the Turso WAL never auto-checkpoints |
| rate limiting the wrong people | `nginx.log` client addresses. If they are Cloudflare edge IPs, `real_ip` is not in force — check `firewall.txt` and re-run `make provision --tags firewall,nginx` |
| 443 unexpectedly open | `firewall.txt` for the `CATLOG-EDGE` chain. A Docker restart flushes `DOCKER-USER` |

`make ops-exec CMD='projections rebuild'` and friends reach the admin mux. The catlogd image has no
shell, which is not an obstacle: `docker compose exec` runs the binary directly. For a real shell,
`make ops-ssh` then `docker compose exec nginx sh` — nginx is the image that has one.

---

## The path being retired

`infra/systemd/catlogd.service`, `infra/systemd/catlog-nightly.{service,timer}` and
`infra/deploy/deploy.sh` install catlogd as a systemd unit on a hand-managed VPS. They are superseded
by everything above and will be deleted once the container path has proved itself end to end.

Two things in the unit file are load-bearing and are the same constraints the container path faces,
which is why they are worth reading even if you never run it: `TURSO_GO_CACHE_DIR` must point inside
`ReadWritePaths` (`ProtectSystem=strict` plus a possibly-`noexec` `PrivateTmp` is otherwise a
`dlopen` failure), and `MemoryDenyWriteExecute` must **not** be set (the FFI shim and the dlopen'd
engine need executable mappings). The container path expresses the first as a bind mount and the
second as the absence of a seccomp profile.

`deploy.sh` stops before it starts for the same reason `make deploy` does, and for exactly as long.
