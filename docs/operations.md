# catlog operations (`infra/`)

Owns **§6 and §11** — the reverse proxy, the container images, the deployment and the runbook.
Reasons are in [DECISIONS.md](DECISIONS.md), area `OPS-*`.

Production is **two containers on one Linux x86_64 VM**, fronted by Cloudflare. Everything about
that box is described by `infra/ansible/`, and everything you do to it is a `make` target run from
your own machine.

---

## The whole operator surface

```sh
make deploy-env      # once: copy infra/deploy.env.example → infra/deploy.env, fill it in
make preflight       # read-only: local tools, secrets, the VM. Changes nothing
make provision       # one-time and re-runnable: baseline, storage, docker, firewall, certs
make release         # build both images, smoke-test the stack, stream them to the VM over ssh
make deploy          # stop→start catlogd on the shipped images, health-gate, recreate nginx
```

Never done this before? Go to [Zero to running](#zero-to-running), which is the whole thing in
order, including the parts you do in somebody else's web UI.

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

**There is no registry.** `make release` streams both images to the VM with
`docker save | ssh docker load` — one pipe, nothing written to disk on either end, no account, no
credential on the box that could be stolen and used to publish a poisoned image. It skips any image
whose ID already matches the VM's, so a server-only redeploy does not re-send the nginx image
(OPS-032).

---

## What is installed on the VM, and what is not

| | |
|---|---|
| **Your machine** | `docker` + `buildx`, `make`, `ssh`, `git`. **No Ansible** |
| **The VM** | Docker CE + compose plugin, `python3`, `openssh`, and optionally `unattended-upgrades` |

**Ansible runs in a container.** `scripts/ansible.sh` wraps `alpine/ansible:2.21.0`, which already
ships `community.docker`, `community.general` and `ansible.posix` — so there is nothing to
`pip install`, no virtualenv to keep, and no version of Ansible on your machine to drift. Every
`make` target that touches the VM goes through it, and you can drive it directly:

```sh
scripts/ansible.sh playbooks/ops.yml --tags status
scripts/ansible.sh --check --diff playbooks/site.yml
ANSIBLE_ENTRY=ansible-inventory scripts/ansible.sh --list
```

It mounts `infra/` (the playbooks and the files they template from) and `diagnostics/` (the only
path it writes), passes `infra/deploy.env` with `--env-file`, and mounts `~/.ssh` **read-only**. The
rest of the repository is deliberately not mounted — `data/` holds the signing key, the session key
and the pepper, and no playbook has any business seeing them.

**The whole connection is described in `deploy.env`; your own `~/.ssh/config` is not consulted.**
`CATLOG_SSH_IDENTITY_FILE` names the key, and `scripts/lib/deploy-env.sh` generates a one-`Host` ssh
config from it, mounting the key at a fixed path inside the container. That is not a preference: the
container's `HOME` is not your home directory, so an `IdentityFile` written as an absolute path — the
normal way to write one — points at nothing in there. The same generated config drives `make ops-ssh`
and the image shipping, so all three are the same connection and cannot drift.

Two things it does that are not obvious:

- **The VM's host key is pinned, not accepted on sight.** `CATLOG_SSH_HOST_KEY` becomes the
  `UserKnownHostsFile` for the run, with `StrictHostKeyChecking=yes`. There is no trust-on-first-use
  step and no prompt to click through, and a server presenting a different key stops every play
  immediately — which is what a rebuilt droplet or a machine-in-the-middle deserves.
- The container runs as your uid, and the wrapper synthesises `/etc/passwd` and `/etc/group` entries
  for it. Without them OpenSSH refuses to start at all — "No user exists for uid 501", exit 255,
  no connection attempted — and every play would fail with an error naming nothing useful.

Deliberately absent from the VM: `go`, `node`/`pnpm`, `.NET`, `git`, `ansible`, `rsync`, `nginx`,
`certbot`/`acme.sh`, `logrotate`, any monitoring agent, any language runtime.

Three choices keep that list short:

- **nginx and acme.sh exist only as containers.** acme.sh is a one-shot run by a host timer, and
  nothing long-running holds the Docker socket (OPS-025).
- **No log files are written to the volume.** Both containers log to stdout, Docker's `json-file`
  driver rotates them, and `docker logs` is the single source (OPS-027).
- **No firewall is managed on the box at all** (OPS-024). Ports 22, 80 and 443 are governed entirely
  by the DigitalOcean cloud firewall, by hand. Docker maintains its own iptables rules for the ports
  it publishes and needs no help.

**Nothing in these playbooks narrows access to anything.** No `nftables` ruleset, no `DOCKER-USER`
chain, no `ufw`, and sshd's configuration is left alone. A firewall rule that locks the operator out
is the one class of mistake that cannot be undone from the machine it was made on, and a cloud
firewall is undone with one click from a laptop. The one thing derived from Cloudflare's published
ranges is nginx's `set_real_ip_from`, which is proxy configuration rather than packet filtering.

---

## Zero to running

Nothing built, nothing configured, an empty Debian/Ubuntu droplet and a `science.fail` zone on
Cloudflare.

| | |
|---|---|
| Droplet | its public IPv4 — `$CATLOG_SSH_HOST` below, and **only** in `infra/deploy.env` |
| Public name | `catlog.science.fail` — Cloudflare **proxied** |
| Direct name | `origin.catlog.science.fail` — **DNS only** |

The droplet's address and its host key are the two facts that identify one specific machine, so they
live in gitignored `infra/deploy.env` and nowhere else. The names are public DNS and the product's
own URLs, so they are written out. Several commands below read the address from that file:

```sh
set -a; . infra/deploy.env; set +a      # $CATLOG_SSH_HOST, $CATLOG_SSH_USER, …
```

You need, on your machine: Docker (running), `make`, `ssh`, `git`. That is the whole list —
[Ansible runs in a container](#what-is-installed-on-the-vm-and-what-is-not).

### 1 — Pin the host key, then prove you can reach the droplet

The droplet's SSH host key is knowable before anything connects to it, so it is **pinned** rather
than accepted on sight. Read it from the **droplet console** — DigitalOcean → your droplet → Access →
Launch Droplet Console — which does not travel over the network you are trying to authenticate:

```sh
cat /etc/ssh/ssh_host_ed25519_key.pub          # in the droplet console
```

Paste that line into `CATLOG_SSH_HOST_KEY` in `infra/deploy.env` (step 6), and the droplet's IP into
`CATLOG_SSH_HOST`. Reading a host key off an unverified connection only pins whatever answered; the
console step is what makes the pin mean anything.

From then on there is no trust-on-first-use step and no prompt to click through: `scripts/ansible.sh`
writes a `known_hosts` from that value and points ssh at it with `StrictHostKeyChecking=yes`. If the
droplet is ever rebuilt, every play stops until you verify and replace the key — which is the correct
response to a host whose identity changed.

Then check you can get in, and that the NVMe volume is mounted where you expect:

```sh
set -a; . infra/deploy.env; set +a
ssh "$CATLOG_SSH_USER@$CATLOG_SSH_HOST" \
  'cat /etc/os-release; uname -m; findmnt /mnt/catlog_db_prime; df -h /mnt/catlog_db_prime'
```

DigitalOcean formats the volume ext4, mounts it and maintains its fstab entry, so there is no device
to identify and nothing to format. `findmnt` must print a line — if it does not, the volume is not
attached, and `make provision` will refuse rather than quietly put the databases on the root disk.

### 2 — DNS, in the Cloudflare dashboard

**DNS → Records → Add record**, twice:

| Type | Name | IPv4 address | Proxy status |
|---|---|---|---|
| `A` | `catlog` | the droplet's IP | **Proxied** (orange cloud) |
| `A` | `origin.catlog` | the same IP | **DNS only** (grey cloud) |

`catlog.science.fail` is baked into every licence catlog issues, as the `htu` the mod signs against
and compares by exact string equality. Choose it once — changing it later invalidates credentials
already in players' hands.

`origin.catlog.science.fail` publishes the droplet's address, which is the point: it is how you tell
"our origin is broken" from "the edge is broken" in one `curl`. It does not weaken Cloudflare,
because both firewall layers below refuse 443 from anywhere except Cloudflare and your own address.

### 3 — Cloudflare zone settings

**SSL/TLS → Overview → Full (strict)**. Do this before any traffic: the origin will have a real
Let's Encrypt certificate, and anything less than Full (strict) is either unencrypted to the origin
or unauthenticated.

Then:

| Where | Setting |
|---|---|
| SSL/TLS → Edge Certificates | **Always Use HTTPS: on** |
| Speed → Optimization | **Rocket Loader: off**, **Mirage: off** — they rewrite HTML and defer scripts, and the datastar pages depend on their own script order |
| Security → Bots | **Bot Fight Mode: off** — it would challenge the mod's shipper, which cannot solve a challenge |
| Caching → Cache Rules | see below |

Cache rules, in order:

1. **Bypass cache** — `(http.request.uri.path eq "/v1/ingest") or (http.request.uri.path contains "/sse") or (http.request.uri.path contains "/stream") or (starts_with(http.request.uri.path, "/auth/")) or (starts_with(http.request.uri.path, "/api/")) or (http.request.uri.path eq "/dashboard")`
2. **Cache everything** — `starts_with(http.request.uri.path, "/static/") or starts_with(http.request.uri.path, "/app/assets/")`

The SSE bypass matters twice: Cloudflare buffers a stream it might cache **or compress**, and the
symptom is feed frames arriving seconds late.

### 4 — The Cloudflare API token (for ACME)

**My Profile → API Tokens → Create Token → Create Custom Token**:

| Setting | Value |
|---|---|
| Permissions | `Zone` → `DNS` → **Edit** |
| | `Zone` → `Zone` → **Read** |
| Zone Resources | Include → Specific zone → `science.fail` |

Copy the token once — it is not shown again. A Global API Key would also work and **must not be
used**: it can do anything to every zone on the account, and this credential ends up in
`/opt/catlog/.env` on a public-facing VM.

### 5 — The DigitalOcean cloud firewall

**This is the only firewall.** Nothing on the box filters packets — no `nftables`, no `DOCKER-USER`
rules, no `ufw` — so these rules are the whole of who can reach the droplet. Get them right.

The upside of putting it here is that every mistake is recoverable from a laptop: a rule that shuts
you out is one click to undo, rather than something that has to be fixed on a machine it has made
unreachable.

**Networking → Firewalls → Create Firewall**, name it `catlog`.

**Inbound rules** — delete the defaults, then add:

| Type | Protocol | Port | Sources |
|---|---|---|---|
| Custom | TCP | `443` | the 22 Cloudflare CIDRs below, **plus your own address** |
| SSH | TCP | `22` | whatever you want it to be |

**Port 22 is yours alone to manage, here and only here.** Nothing in the playbooks narrows SSH: the
host ruleset accepts it from anywhere and sshd's configuration is left untouched. That is deliberate.
A firewall rule that locks the operator out of their own machine is the one mistake that cannot be
fixed from the machine, and a home connection's address is not the same address next week. Getting it
wrong in the DigitalOcean UI costs one click and a page reload.

Cloudflare's ranges, for pasting into the Sources box:

```
173.245.48.0/20, 103.21.244.0/22, 103.22.200.0/22, 103.31.4.0/22, 141.101.64.0/18,
108.162.192.0/18, 190.93.240.0/20, 188.114.96.0/20, 197.234.240.0/22, 198.41.128.0/17,
162.158.0.0/15, 104.16.0.0/13, 104.24.0.0/14, 172.64.0.0/13, 131.0.72.0/22,
2400:cb00::/32, 2606:4700::/32, 2803:f800::/32, 2405:b500::/32, 2405:8100::/32,
2a06:98c0::/29, 2c0f:f248::/32
```

**Port 80 is deliberately absent.** With SSL/TLS at Full (strict), Cloudflare always reaches the
origin over 443, and "Always Use HTTPS" redirects plaintext at the edge without ever contacting the
droplet. ACME is DNS-01, so it needs no inbound port either. Nothing legitimate arrives on 80.

**Outbound rules**: leave the permissive defaults. The droplet needs to reach apt, Let's Encrypt, the
Cloudflare API and the three identity providers. It never pulls a container image — those arrive over
the ssh connection you already have.

Then **Droplets → add `catlog`** to the firewall.

With `doctl`, the same thing:

```sh
CF="173.245.48.0/20,103.21.244.0/22,103.22.200.0/22,103.31.4.0/22,141.101.64.0/18,\
108.162.192.0/18,190.93.240.0/20,188.114.96.0/20,197.234.240.0/22,198.41.128.0/17,\
162.158.0.0/15,104.16.0.0/13,104.24.0.0/14,172.64.0.0/13,131.0.72.0/22,\
2400:cb00::/32,2606:4700::/32,2803:f800::/32,2405:b500::/32,2405:8100::/32,\
2a06:98c0::/29,2c0f:f248::/32"
ME=203.0.113.5/32          # your address — curl -s https://ifconfig.me

doctl compute firewall create --name catlog \
  --inbound-rules "protocol:tcp,ports:443,address:${CF},address:${ME} protocol:tcp,ports:22,address:${ME}" \
  --outbound-rules "protocol:tcp,ports:all,address:0.0.0.0/0,address:::/0 protocol:udp,ports:all,address:0.0.0.0/0,address:::/0 protocol:icmp,address:0.0.0.0/0,address:::/0" \
  --droplet-ids "$(doctl compute droplet list --format ID,Name --no-header | awk '/catlog/{print $1}')"
```

**Cloudflare changes these ranges, and this list is maintained by hand.** `roles/catlog_nginx`
refetches them on every run for nginx's `set_real_ip_from` and says so when the published list has
moved — that is your cue to update the rules here too. Nothing updates them for you.

### 6 — Fill in `deploy.env`

```sh
make deploy-env          # copies infra/deploy.env.example → infra/deploy.env
```

It already carries `catlog.science.fail` and `origin.catlog.science.fail`. Replace every
`CHANGE_ME`:

| Key | Value |
|---|---|
| `CATLOG_SSH_HOST` | the droplet's public IPv4 |
| `CATLOG_SSH_HOST_KEY` | the host key from step 1 |
| `CATLOG_SSH_IDENTITY_FILE` | the private key that reaches the droplet, e.g. `~/.ssh/digitalocean` |
| `ACME_EMAIL` | where Let's Encrypt sends expiry warnings |
| `CF_API_TOKEN` | from step 4 |
| `DHI_USER` / `DHI_TOKEN` | a Docker Hub PAT; DHI is free but authenticated. Build-time only |
| `CATLOG_IDP_*` | the three OAuth apps' credentials |

Set **`ACME_STAGING=1`** for now. Let's Encrypt's production CA allows five failed issuances an hour;
the staging CA has no limit worth worrying about, and you want the DNS-01 plumbing proved before you
start spending real attempts.

The OAuth applications' redirect URIs are
`https://catlog.science.fail/auth/{discord,google,github}/callback`.

`infra/deploy.env` is gitignored and is the only place any of this exists outside the VM.

### 7 — Preflight

```sh
docker login dhi.io      # the hardened base images; free, but authenticated
make preflight
```

Read-only. It reports every missing key at once, checks the droplet is Debian-family, and — the two
worth watching — reports whether the data root is a real mount point and refuses if it is `noexec`.

### 8 — Provision

```sh
make provision
```

Baseline packages and the `catlog` user, the NVMe mount and its directory tree, Docker CE, the on-box
firewall, the Let's Encrypt certificate, the nginx configuration and the compose project. Ten to
fifteen minutes, mostly apt and the certificate.

It creates no filesystem and touches no fstab: the volume arrives formatted and mounted, and the
storage role only verifies that — a real mount point, not `noexec`, and able to execute a file — then
lays out the directories on it.

Idempotent: running it again on a healthy box changes nothing.

### 9 — Build, push and deploy

```sh
make release      # builds both images, smoke-tests the whole stack, pushes, records the digests
make deploy       # pulls those digests, stop→start catlogd, health-gates, recreates nginx
```

`make release` runs `scripts/container-smoke.sh` as a hard gate, so a broken image never reaches the
VM. First run takes a few minutes — the nginx image compiles ngx_brotli — and streams about 85 MB.
Later releases usually send only catlogd, because the nginx image is unchanged and gets skipped.

Nothing is published anywhere. The images exist in your local Docker daemon and on the VM, and
`infra/.release.env` records exactly which ones, by content hash.

### 10 — Verify, through the bypass first

```sh
curl -fsS https://origin.catlog.science.fail/healthz    # the origin, Cloudflare out of the picture
curl -fsS https://catlog.science.fail/healthz           # the same, through Cloudflare
curl -sI -H 'Accept-Encoding: br' https://catlog.science.fail/static/css/catlog.css | grep -i content-encoding
curl -fsS https://catlog.science.fail/app/ | head -3    # the React reader
make ops-status
```

Checking the bypass first is the habit worth forming: if it works and the proxied name does not, the
problem is Cloudflare's side, and you have halved the search space without logging into anything.

With `ACME_STAGING=1` your browser will call the certificate untrusted, and `curl` needs `-k`. That
is expected until the next step.

### 11 — Real certificates, then HSTS

```sh
# in infra/deploy.env: remove ACME_STAGING (or set it to 0)
make certs
```

`roles/acme` sees the staging certificate, reissues against the production CA and reloads nginx.
Confirm in a browser that the padlock is clean, then — and only then:

```sh
# in infra/deploy.env: CATLOG_HSTS_MAX_AGE=31536000
make provision TAGS=nginx
```

HSTS last, deliberately. It is a promise you cannot withdraw for `max-age` seconds, and a browser
that has seen it will refuse to talk to the name over plaintext for a year no matter what you do
next.

### 12 — Seed and admit players

```sh
make ops-exec CMD='keygen'           # only if the key set was not created at first boot
make ops-status
```

Optionally enable the nightly maintenance timer, once `rebuild`, `archive` and `backup` are all live
— it is installed disabled precisely because a timer that fails every night at 04:30 trains you to
ignore it. Set `catlog_nightly_enabled: true` and re-run `make provision TAGS=maintenance`.

### If something is wrong

`make ops-status` first, `make ops-logs` second — the latter fetches a full diagnostics bundle into
`./diagnostics/` and the symptom table under [Triage](#triage) maps each failure to the file in it
that names the cause.

---

---

## §6 nginx

Two files, split along the line of what depends on the domain (OPS-022).

**`infra/nginx/nginx.conf` is baked into the image.** Compression, the `limit_req`/`limit_conn`
zones, the `catlogd` upstream and the JSON log format are all domain-independent, so they ship with
the image and are validated by `nginx -t` **during the build** — which is what catches a brotli
module compiled against the wrong nginx version, in the build rather than on the VM at 3am.

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

`real_ip` is therefore on unconditionally, which is only safe because Ansible applies both halves
from **one fact, in one run, in the required order**:
`roles/catlog_nginx` fetches Cloudflare's published ranges on every run and renders
`set_real_ip_from` from them, refusing to apply a list that does not look like CIDRs.

**Its safety depends on something this repository does not control:** the origin must not be
reachable on 443 from outside those ranges, which is the DigitalOcean firewall's rule. Widen that
rule and `real_ip` stops being a fix and becomes the hole (OPS-024).

### Where access control actually lives

**The DigitalOcean cloud firewall, and nowhere else** — step 5 of
[Zero to running](#zero-to-running). Nothing on the box filters packets.

That is a deliberate choice rather than an omission. A host firewall on a machine you reach over the
network is the one component whose failure mode is losing the machine, and there was nothing it could
express here that the cloud firewall cannot: the box listens on 22 (sshd) and 80/443 (Docker's
published ports), and that is the whole surface.

One thing worth knowing if you ever reconsider: **published container ports never traverse the INPUT
chain.** They are DNAT'd in `nat/PREROUTING` and filtered in `FORWARD`, so a host ruleset that
"blocks everything but 22" would leave 443 wide open while looking correct. Any on-box rule for a
container port has to go in `DOCKER-USER`, and has to be re-applied whenever Docker restarts, because
Docker re-creates and flushes that chain. A cloud firewall has neither problem.

`make ops-logs` still collects `iptables -S` and the nat table — for reading, not for managing. A
published port that is unreachable usually shows up there as a missing DNAT rule.

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
| `catlog/catlogd` | `dhi.io/static:20250419-glibc-debian13` | 21 MB | `catlogd`, `catlogctl` |
| `catlog/catlog-nginx` | `nginx:1.29` + `ngx_brotli` | 64 MB | nginx, `site/dist`, `spa/dist`, both pre-compressed |

Local names, not registry paths: they are built on your machine and streamed to the VM.

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

**No write-xor-execute restriction may be applied to catlogd.** purego's FFI shim and the `dlopen`'d
Turso engine need executable mappings, so the container runs with no seccomp profile beyond Docker's
default and nothing in `compose.prod.yaml` forbids them. It is the one hardening flag that must not
be added.

`TURSO_GO_CACHE_DIR` points at `/var/lib/catlog/turso-cache`, a bind mount from
`$CATLOG_DATA_ROOT/turso-cache`, and `roles/storage` proves that filesystem can execute a file rather
than trusting `findmnt` alone. A `noexec`
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
/opt/catlog/                     compose.yaml (from git), .env (rendered), deployed.json
/mnt/catlog_db_prime/            the NVMe volume — mounted by DigitalOcean, ext4, NEVER noexec
├── config/       0750 root:catlog   catlogd.toml (0640), catlogd.env (0640)
├── data/         0750 <uid>         events.db, projections.db, keys/ (0700), archive/
├── turso-cache/  0700 <uid>         the extracted libturso_sync_sdk_kit.so
├── backups/      0750 <uid>         catlogctl backup output
├── acme/         0700 root          acme.sh state; live/ is mounted read-only into nginx
└── nginx/conf/                      10-catlog.conf, 20-realip.conf
```

**The volume is the provider's.** Nothing here discovers a block device, creates a filesystem or
writes an fstab entry — DigitalOcean does all three. `roles/storage` checks two things and then
creates directories:

- **that the path is a real mount point.** A volume that failed to attach leaves an ordinary empty
  directory behind, and catlog would open its databases on the root disk — an order of magnitude
  smaller, and gone with the droplet — without complaining once.
- **that it is not `noexec`, and can actually execute a file.** See below; we no longer own the
  mount, so this can only be refused, not fixed.

Only one path is configured, `CATLOG_DATA_ROOT`, and it flows to the container through the compose
`.env`. Everything that must survive a rebuild is under it: one thing to back up, one thing to move.

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
| `catlog.science.fail` | `A` | the droplet's IP | **Proxied — orange cloud** | The public origin. Everything reaches catlog through this, behind Cloudflare's DDoS absorption and analytics. |
| `origin.catlog.science.fail` | `A` | the same address | **DNS only — grey cloud** | The bypass. Resolves straight to the box, so one `curl` distinguishes "our origin is broken" from "the edge is broken". |

Set them in the Cloudflare dashboard under **DNS → Records**. The orange/grey cloud toggle is the
"Proxy status" column.

**`catlog.science.fail` is baked into every licence catlog issues** as the `htu` the mod signs against
(`[ingest] accepted_htu`, compared by exact string equality with no normalisation). Choose it once —
changing it later invalidates credentials already in the wild.

**Who may use the bypass name is the cloud firewall's answer.** nginx answers on it to anyone who
can open a connection; restricting inbound 443 to Cloudflare's ranges plus your own address is what
makes that a short list. A name that bypasses the DDoS front door should not be reachable by the
internet at large — and that rule belongs in the one place all the other access rules live.

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

### The Cloudflare API token, and the zone settings

Both are procedure rather than design, and both live in
[Zero to running](#zero-to-running) — steps 3 and 4 — with the exact values.

Two of them are worth restating because getting them wrong is quiet rather than loud:

- **SSL/TLS must be Full (strict).** There is a real Let's Encrypt certificate at the origin;
  anything less is either unencrypted to the origin or unauthenticated.
- **SSE must be excluded from Cloudflare's caching *and* its compression.** Cloudflare buffers a
  stream it might cache or compress, and the symptom is feed frames arriving seconds late. Check the
  zone's Cache Rules before suspecting nginx.

**A Global API Key would work and must not be used.** It can do anything to every zone on the
account, and this credential ends up in `/opt/catlog/.env` on a public-facing VM. The scoped token
is `Zone:DNS:Edit` + `Zone:Zone:Read` on the one zone.

**Do not enable nginx's `proxy_cache` micro-cache with Cloudflare in front.** Two stacked shared
caches make every staleness question twice as hard.

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
| rate limiting the wrong people | `nginx.log` client addresses. If they are Cloudflare edge IPs, `real_ip` is not in force — check `firewall.txt` and re-run `make provision TAGS=nginx` |
| 443 reachable from somewhere it should not be | the DigitalOcean firewall's inbound rules — nothing on the box restricts it |
| a published port unreachable | `iptables.txt`, for a missing DNAT in Docker's `nat` table |

`make ops-exec CMD='projections rebuild'` and friends reach the admin mux. The catlogd image has no
shell, which is not an obstacle: `docker compose exec` runs the binary directly. For a real shell,
`make ops-ssh` then `docker compose exec nginx sh` — nginx is the image that has one.
