# catlog — build, package and distribution plan

**Status:** implemented and verified. Both images build; `scripts/container-smoke.sh` passes 23/23
against the real compose project. Where building the thing contradicted this plan, the plan was
corrected — see §3.2 (the runtime base) and OPS-019/028/029/030 in `docs/DECISIONS.md`.
**Target:** a container-native production deployment on a single Linux `x86_64` VM with an
NVMe-backed volume, fronted by Cloudflare proxied DNS.
**Scope:** replaces `infra/deploy/deploy.sh` + `infra/systemd/*` as the production delivery path.
The systemd path stays in the tree until the container path has run a full deploy → upgrade →
rollback → restore cycle, then it is deleted (rule 3: no document may describe something that is
gone).

This plan is written against what the repository **actually builds today**, not against a sketch of
it. §1 is the evidence; every later decision cites it.

---

## 0. Decisions taken, and the ones that were made for you

Answered by the owner:

| # | Decision |
|---|---|
| A | The React reader is served **from the same host at `/app/`** (`SPA_BASE=/app/`), same-origin with the read API. |
| B | TLS is **Let's Encrypt**, via an `acme.sh` **DNS-01** flow (not a long-running sidecar — see §7.5). |
| C | **Docker CE + Compose**, images on **GHCR** (`ghcr.io/meow-sci/*`). |
| D | Ansible owns the **full baseline + app**: Docker, the NVMe mount, directory layout, firewall, certs, the compose project, deploy/upgrade/rollback/backup. |

Made here, with reasons in the sections named:

| # | Decision | Where |
|---|---|---|
| E | **Two images**, not one: `catlogd` (DHI Go) and `catlog-nginx` (nginx + brotli + baked static assets). | §3.1 |
| F | Runtime base is **`dhi.io/static:…-glibc-debian13`**. Not the golang non-dev variant — measuring it showed 18,764 files including `bash` and the Go compiler. Not Alpine, not `scratch`. | §3.2 |
| G | Static assets live in the **nginx image**, not in the catlogd image, and not in a shared volume. | §3.3 |
| H | Assets are **pre-compressed at build time** (brotli q11 + gzip -9) by a Node script; nginx serves them with `brotli_static`/`gzip_static`. | §5 |
| I | `catlogd` gains a **`-healthcheck` flag** so a shell-less image can have a `HEALTHCHECK`. | §4.5 |
| J | Deploy is **stop → start with auto-rollback**, never rolling. The exclusive database lock forbids anything else. | §6.4 |
| K | **D1 is superseded for the container target only.** Ansible provisions. The reasoning is in §12. | §12 |
| L | **Two commands for a release** (`make release && make deploy`), one gitignored secrets file, and a one-command diagnostics bundle. The VM gets Docker, python3 and sshd — nothing else. | §8 |

---

## 1. Ground truth: what the build produces today

### 1.1 The four buildable things

| Thing | Command | Output | Ships to prod? |
|---|---|---|---|
| Go server | `cd server && go build -o bin/ ./cmd/...` | `server/bin/{catlogd,catlogctl,mockidp}` | `catlogd` yes; `catlogctl` yes (admin client); `mockidp` **never** |
| Datastar site assets | `pnpm -C site build` (esbuild, `site/scripts/build.mjs`) | `site/dist/{js,css,vendor,fonts}` | yes |
| React reader | `pnpm -C spa build` (tsc + vite/rolldown) | `spa/dist/` (hashed chunks, `index.html`, `404.html`) | yes |
| .NET mod + harnesses | `dotnet build mod/catlog.slnx` | mod DLLs, `sim`, `loadgen` | **no** — client-side and test-only |

The mod is out of scope for this plan entirely: it ships to players, not to the server.

### 1.2 The constraint that decides the base image

`docs/operations.md` states it and the module source confirms it:

- `tursogo` is CGO-free but reaches its engine through **purego**, whose shim emits a
  **dynamically-linked ELF with a glibc interpreter even at `CGO_ENABLED=0`** — three `DT_NEEDED`
  entries. `scratch` and `distroless/static` cannot run it.
- `turso-go-platform-libs` **embeds a 19 MB `libturso_sync_sdk_kit.so`** and, at first database open,
  **extracts it to disk and `dlopen`s it**
  (`embeddedLibraryTryCreate` → `purego.Dlopen(path, RTLD_NOW|RTLD_GLOBAL)`).
  The destination is `$TURSO_GO_CACHE_DIR`, else `os.UserCacheDir()`, else `os.TempDir()`.
  It `chmod 0755`s the file and verifies its SHA-256 against the embedded digest on every start.
- Therefore the runtime needs: **glibc + `ld.so`**, a **writable directory**, and that directory must
  be **`exec`-capable**. A `noexec` mount is a `dlopen` failure at startup, not a slow path.
- Alpine/musl is possible but requires building with **`-tags musl`** (a different embedded `.so`).
  Not chosen: the glibc path is the one the repo has evidence for.
- **`MemoryDenyWriteExecute` / `--security-opt` equivalents must not be applied.** The FFI shim needs
  executable mappings.

### 1.3 What `catlogd` needs on disk at runtime

Everything HTML is compiled in. `go:embed` covers:

- `server/internal/web/templates/*.gohtml` — every page, including `/docs/*`
- `server/internal/store/migrations/{events,projections}/*.sql`
- `server/internal/store/dict/payload_v1.zstd` — the trained zstd payload dictionary

So the runtime file dependencies are exactly:

1. the config file (`-config`), plus `CATLOG_*` environment overrides for secrets;
2. `[data] dir` — `events.db`, `projections.db`, `keys/` (auto-created 0700 by `keys.LoadOrCreate`),
   `archive/`;
3. a writable+exec cache dir for the extracted `.so`;
4. **CA certificates** — outbound TLS to Discord/Google/GitHub IdPs;
5. `site/dist`, **only if `[server] static_dir` is set**. In production it is empty and nginx serves
   the tree (`web.go:216`, `config.go:72`). This is why the assets go in the nginx image (§3.3).

`tzdata` is not required — every catlog timestamp is UTC by design — but the DHI runtime base carries
it anyway.

### 1.4 What must never be compressed, buffered or rewritten

Standing rules from `CLAUDE.md`, `docs/operations.md` and the config comments:

- **`POST /v1/ingest`** — the body is brotli-compressed NDJSON whose SHA-256 is covered by the proof
  JWS (`bh`). nginx must pass it through **byte for byte**: no `gunzip`, no `brotli` filter, no
  `sub_filter`. `client_max_body_size 2m` (the app caps at 1 MiB compressed).
- **`/v1/feed/sse`, `/v1/feed/stream`, `/v1/events/sse`, `/v1/events/stream`** — `proxy_buffering off`,
  `proxy_cache off`, long read timeout, `X-Accel-Buffering: no`, **never compressed**.
- **`/admin/`** — `return 403`. The admin mux binds loopback and refuses non-loopback peers; the
  nginx rule is the belt that survives a typo.
- `catlogd` **has no compression middleware and will not gain one.** All response compression is
  nginx's job. That is the whole reason §5 exists.

### 1.5 The lock

Turso holds an **exclusive whole-file lock excluding other processes, readers included**
(`TestSecondProcessIsLockedOut`). Consequences that shape §6:

- exactly one `catlogd` container per data directory, ever;
- **no rolling restart, no blue/green, no second container "to warm up"**;
- `catlogctl` is an HTTP client for the loopback admin API — it never opens a database, so it can run
  in a throwaway container against the running one;
- backups are taken **by catlogd** through the admin API, not by copying files.

### 1.6 Current asset inventory

`site/dist/` after `pnpm -C site build`:

```
css/catlog.css
js/{intl,me,keygen}.js  + .js.map
vendor/datastar.js          # vendored, not on npm; pinned + SRI in docs/DECISIONS.md
fonts/inter-latin-wght-normal.woff2   # ~48 kB, latin subset, self-hosted
```

`spa/dist/` after `pnpm -C spa build`: `index.html`, `404.html`, `favicon.svg`, and hashed
`assets/*.{js,css}` including a pinned `vendor` chunk (React + react-dom + scheduler) that survives
shell edits — which is what makes long-lived immutable caching worthwhile at `/app/assets/`.

Both frontends are hermetic: no CDN, no runtime fetch of a third-party asset.

---

## 2. Target topology

```
                      Cloudflare (proxied DNS, Full-strict, DDoS + stats)
                                      │  443/80, CF ranges only
┌─────────────────────────────────────┼──────────────────────────────────────┐
│ VM (linux/amd64)                    ▼                                      │
│   nftables: 80,443 ← CF ranges · 22 ← admin CIDR · everything else denied   │
│                                                                            │
│   ┌────────────────────┐  catlog_net (bridge, internal)  ┌──────────────┐  │
│   │ catlog-nginx       │────────────────────────────────▶│ catlogd      │  │
│   │ nginx + ngx_brotli │        http://catlogd:8080      │ DHI Go image │  │
│   │ /static/  (baked)  │                                 │ :8080 public │  │
│   │ /app/     (baked)  │                                 │ :6060 admin  │  │
│   │ TLS, brotli, rate  │                                 │  (never      │  │
│   │ limits, real_ip    │                                 │   published) │  │
│   └────────┬───────────┘                                 └──────┬───────┘  │
│            │ certs (ro)                                         │          │
│            ▼                                                    ▼          │
│   /mnt/catlog/acme/live/…                    /mnt/catlog/{data,config,…}   │
│                                              NVMe SSD, exec-capable        │
└────────────────────────────────────────────────────────────────────────────┘
```

Route ownership on the single origin hostname `catlog.<domain>`:

| Path | Served by | Notes |
|---|---|---|
| `/` , `/boards/*`, `/p/*`, `/events`, `/stats`, `/search`, `/compare`, `/login`, `/dashboard`, `/docs/*` | catlogd (datastar site, embedded templates) | proxied, dynamically compressed |
| `/static/*` | **nginx, from the image** | pre-compressed, immutable-ish caching |
| `/app/*` | **nginx, from the image** (SPA) | `try_files … /app/index.html` for deep links |
| `/v1/*` (JSON read API) | catlogd | proxied, dynamically compressed |
| `/v1/ingest` | catlogd | **passthrough, never touched** |
| `/v1/{feed,events}/{sse,stream}` | catlogd | unbuffered, uncompressed |
| `/auth/*`, `/api/*`, `/.well-known/*` | catlogd | cookie-authenticated, same-origin |
| `/admin/*` | nginx | `403` |

**Serving the SPA at `/app/` retires the CORS allow-list in production.** The bundle is built with
`VITE_CATLOG_API_BASE=` (empty → relative `/v1/...`, see `spa/src/api/client.ts:33`) and
`SPA_BASE=/app/` (router reads it back from `import.meta.env.BASE_URL`,
`spa/src/state/router.ts:115`). `CATLOG_CORS_ALLOWED_ORIGINS` is therefore **empty in production**,
which is strictly safer than listing an origin. `.github/workflows/spa-pages.yml` becomes redundant
and should be deleted in the same work package (rule 3).

**This amends `UI-017`**, which chose GitHub Pages for the reader. It does not overturn its
reasoning — UI-017 argues for a second, independently-deployable frontend, and that survives: the
SPA keeps its own lockfile, toolchain, build and image stage, and can still be pointed at any static
host by changing two build variables. What changes is the *default* host, and with it two claims
made elsewhere in the docs that become false and must be corrected, not left standing (rule 3):

- `docs/ARCHITECTURE.md`'s two-frontends table — the `Hosting` row ("Any static host; GitHub Pages
  workflow included") and the `Talks to` row ("cross-origin").
- `DEVELOPMENT.md`, *"Why the SPA runs at `:5173` and not behind catlogd"* — its closing claim that
  "a deployed reader needs its real origin added there" is exactly what stops being true.
- `spa/README.md`'s hosting table and its `spa-pages.yml` paragraph.

One thing that must **not** be "fixed" as a consequence: `make spa-preview` stays. It is the only
local target that runs the bundle cross-origin, and the CORS allow-list remains live code for any
other deployment of the reader — production simply no longer uses it. Deleting the target because
production stopped needing it would remove the only test of a path that still exists.

---

## 3. Image design

### 3.1 Two images, not one

A single image would have to run two processes (nginx + catlogd) under a supervisor inside a base
that has no shell. Two images keep each one's attack surface honest, let nginx restart without
touching the database lock, and let the SPA/site ship on a different cadence to the server binary.

| Image | Base (final stage) | Contents |
|---|---|---|
| `ghcr.io/meow-sci/catlogd` | `dhi.io/golang:1.26-debian13` | `catlogd`, `catlogctl` |
| `ghcr.io/meow-sci/catlog-nginx` | `nginx:<pinned>` + brotli module | nginx, `site/dist`, `spa/dist`, both pre-compressed, conf |

### 3.2 Why `dhi.io/golang:1.26-debian13` and not something smaller

The owner's stated reason for DHI — *"only the Go runtime and nothing else, not even a shell"* — is
satisfied by the non-dev variant: DHI runtime variants ship **no shell and no package manager** and
run as a **nonroot user** by default.

Two candidates fit §1.2's glibc + `dlopen` requirement:

1. **`dhi.io/golang:1.26-debian13`** (non-dev). This is exactly the pattern DHI's own Go guide
   recommends for "CGO-enabled apps" — build in `-dev`, run in non-dev — and our purego/`dlopen`
   binary is that case in all but name.
2. `dhi.io/static:<v>-glibc-debian13` — smaller still (ca-certificates + tzdata + libc6, nonroot),
   and would also work.

**Chosen: (1)**, because it is what the owner asked for and what DHI documents for this shape.
**Recorded as a follow-up:** measure (2) once the first image exists; if it runs, it is a free
reduction and should be adopted with a `DECISIONS.md` entry. Both must be verified against §1.2 with
the smoke test in §9.2 — a startup that reaches `/healthz` proves `ld.so`, `dlopen` and the writable
cache dir all work.

**Rejected:** `scratch`, `distroless/static`, any Alpine/musl variant (would need `-tags musl` and a
second embedded `.so` we have no evidence for), and single-stage builds.

Base image **tags are pinned by digest** in the Dockerfile (`FROM …@sha256:…`) with a Renovate/
Dependabot rule to bump them; DHI's value evaporates if the tag floats or never moves.

Pulling from `dhi.io` requires an authenticated Docker account (free tier, PAT). That auth is needed
**in CI only** — the VM pulls our finished image from GHCR, which already contains the base layers.

### 3.3 Why the static assets go in the nginx image

The alternative — bake them into the catlogd image and share a volume — creates a second copy that
can drift from the one nginx reads, and re-introduces a bind mount the hardened runtime does not
otherwise need. Baking them into nginx means:

- the asset bundle and the nginx config that serves it are versioned together, as one artifact;
- `[server] static_dir` stays **empty** in production, so `catlogd` mounts no `/static/` route at all
  — a route that does not exist cannot be misconfigured;
- a CSS-only change redeploys nginx (a plain restart) and never stops `catlogd`, so it costs **zero**
  ingest downtime. Given §1.5 that is a real operational win, not a tidiness argument.

---

## 4. `Dockerfile.catlogd`

Location: `infra/docker/Dockerfile.catlogd`. Build context: repository root.

### 4.1 Stages

```
deps    (dhi.io/golang:1.26-debian13-dev)  go mod download, cached
build   (deps)                             go build catlogd + catlogctl
runtime (dhi.io/golang:1.26-debian13)      COPY --from=build, nonroot, ENTRYPOINT
```

`deps` exists so `go.mod`/`go.sum` alone invalidate the module cache; source edits do not re-download
~60 modules (including the 19 MB platform-libs).

### 4.2 Build flags

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64
go build -trimpath \
  -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${DATE}" \
  -o /out/ ./cmd/catlogd ./cmd/catlogctl
```

- `-trimpath` for reproducibility (matches `deploy.sh` today).
- `main.version` currently a `const` in `cmd/catlogd/main.go:50` — change it to a `var` so `-X` works.
  `catlogd -version` then prints the real build, which the deploy play asserts against the tag it
  believed it was installing.
- **`mockidp` is never built into the image.** It is a development identity provider; shipping it
  into production would be a credential-issuing service nobody asked for.
- Use BuildKit cache mounts for `/go/pkg/mod` and `/root/.cache/go-build`, keyed by `GOARCH`.

### 4.3 The runtime stage

```dockerfile
FROM dhi.io/golang:1.26-debian13@sha256:… AS runtime
COPY --from=build /out/catlogd   /usr/local/bin/catlogd
COPY --from=build /out/catlogctl /usr/local/bin/catlogctl

ENV TURSO_GO_CACHE_DIR=/var/lib/catlog/turso-cache
ENV CATLOG_SERVER_LISTEN=0.0.0.0:8080
ENV CATLOG_SERVER_ADMIN_LISTEN=127.0.0.1:6060
ENV CATLOG_SERVER_STATIC_DIR=

WORKDIR /var/lib/catlog
EXPOSE 8080
USER nonroot
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/catlogd", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/catlogd"]
CMD ["-config", "/etc/catlog/catlogd.toml"]
```

Points that are load-bearing rather than stylistic:

- **`CATLOG_SERVER_LISTEN=0.0.0.0:8080`.** The dev default is `127.0.0.1:8080`, which inside a
  container means "unreachable from nginx". `8080` is above 1024, so nonroot binds it fine.
- **`admin_listen` stays `127.0.0.1:6060`** — inside the container's own network namespace, so it is
  reachable *only* by a process in that namespace (`docker exec` / `docker compose run` sharing the
  namespace). It is never published and never joins `catlog_net`. `catlogctl` reaches it as described
  in §6.5.
- **`CATLOG_SERVER_STATIC_DIR=` (empty)** disables the `/static/` route (§1.3).
- **`TURSO_GO_CACHE_DIR` points into the mounted volume** (§1.2). Same reasoning as the systemd unit,
  same failure mode if forgotten.
- No `ENV CATLOG_*` secrets. Secrets arrive via the compose `env_file`, rendered by Ansible from
  your one gitignored `deploy.env` (§7.4, §8.2).

### 4.4 Filesystem posture

Compose (§6.2) runs the container with `read_only: true`, a `tmpfs` `/tmp`, `cap_drop: [ALL]`,
`security_opt: [no-new-privileges:true]`, and exactly two writable bind mounts:
`/var/lib/catlog` (data + turso-cache) and nothing else. `/etc/catlog` is mounted **read-only**.

### 4.5 New: `catlogd -healthcheck` *(code change)*

A shell-less image cannot run `HEALTHCHECK CMD curl …`. Rather than shipping a second binary or a
busybox layer, add a flag to `catlogd`:

```
-healthcheck   perform GET http://127.0.0.1:<listen-port>/healthz; exit 0 on {"ok":true}, 1 otherwise
```

It must not open a database (§1.5 — a health probe that took the lock would be a self-inflicted
outage), read only the config for the port, use a 2 s timeout, and print nothing on success.
Requires: a unit test, and updates to `docs/server.md` + a `DECISIONS.md` entry.

---

## 5. `Dockerfile.nginx` — brotli, and pre-compressed assets

Location: `infra/docker/Dockerfile.nginx`. Build context: repository root.

### 5.1 Stages

```
site-build  (node:24-bookworm, pinned by digest)   pnpm -C site build   → site/dist
spa-build   (node:24-bookworm, pinned by digest)   pnpm -C spa build    → spa/dist   (SPA_BASE=/app/, VITE_CATLOG_API_BASE=)
precompress (same)                                 node scripts/precompress.mjs on both trees
nginx-mods  (nginx:<pinned>, ENABLED_MODULES=brotli)  builds ngx_brotli as a dynamic module
runtime     (nginx:<pinned>)                       + brotli .so + conf + both asset trees
```

The Node stages are **build-only and never shipped**; the official `node` image is fine there. Both
frontends use **pnpm with `--frozen-lockfile`**, from their own lockfiles, exactly as CI does today.
pnpm itself comes from **corepack against each package's `packageManager` field** (`pnpm@11.20.0` at
the time of writing) rather than a floating `npm i -g pnpm` — the repo's "pnpm only, pinned" rule
should not weaken just because the build moved into a container.

`spa-build` runs the same four gates the Pages workflow runs (`typecheck`, `lint`, `fmt:check`,
`test`) — a bundle that does not pass them must not reach an image either. It also asserts
`spa/dist/404.html` exists (the deep-link fallback), for parity, even though nginx uses `try_files`
rather than a 404 document.

### 5.2 The brotli module

nginx's official image repository ships a documented mechanism for third-party dynamic modules:

```
docker build --build-arg ENABLED_MODULES="brotli" -f <nginx/docker-nginx modules Dockerfile> …
```

`brotli` is one of the pre-packaged pkg-oss modules. The result is
`ngx_http_brotli_filter_module.so` + `ngx_http_brotli_static_module.so`, loaded with `load_module`
at the top of `nginx.conf`.

We vendor that stage into our own Dockerfile rather than depending on an external image, so the
module is built from the **same pinned nginx version** as the runtime stage — a module built against
a different nginx refuses to load, and it refuses at start, loudly, which is the good failure.

### 5.3 `scripts/precompress.mjs` *(new)*

Node's `zlib` has brotli built in, so this needs **no new dependency** — which matters, because both
frontends are deliberately hermetic.

```
for each file under <dir>:
  skip if ext ∈ {.br,.gz,.woff2,.woff,.png,.jpg,.jpeg,.webp,.avif,.ico,.zip,.zst}
  skip if size < 1024
  emit  <file>.br   zlib.brotliCompressSync(buf, {
                       [BROTLI_PARAM_QUALITY]: 11,
                       [BROTLI_PARAM_LGWIN]: 24,
                       [BROTLI_PARAM_SIZE_HINT]: buf.length })
  emit  <file>.gz   zlib.gzipSync(buf, { level: 9 })
  delete either sibling if it is not smaller than the original
print a table: file, raw, br, gz, ratio
```

Compressed extensions: `.html .css .js .mjs .map .json .svg .txt .xml .webmanifest`.
`woff2` is already brotli-compressed internally — recompressing it costs bytes and CPU.

Exposed locally as `make precompress` so the ratios are inspectable without a Docker build.

### 5.4 nginx configuration

The production config becomes a **real, tested, installed file** — `infra/nginx/prod.conf.tmpl`,
rendered by Ansible — replacing today's `prod.conf.example` with its `<PLACEHOLDER>`s (rule 3: the
`.example` is then deleted, not left beside its successor).

Compression:

```nginx
# http{}
brotli_static  on;      # serve foo.css.br when it exists and the client accepts br
gzip_static    on;      # serve foo.css.gz otherwise
gzip_vary      on;

brotli          on;     # on-the-fly, for PROXIED responses only (see below)
brotli_comp_level 5;    # dynamic path: latency matters more than the last 2%
brotli_min_length 1024;
brotli_types  text/html text/plain text/css text/xml
              application/javascript application/json application/xml
              application/manifest+json image/svg+xml;

gzip            on;
gzip_comp_level 5;
gzip_min_length 1024;
gzip_proxied    any;
gzip_types      <same list>;
```

- **`text/html` is in the dynamic list on purpose** — every datastar page is server-rendered HTML
  from catlogd and there is no file to pre-compress. Same for `application/json` from `/v1/*`.
- **`text/event-stream` is in neither list**, and the SSE location sets `brotli off; gzip off;`
  explicitly. Compressing a stream buffers it, which breaks the sub-second frame contract (§1.4).
- **`location = /v1/ingest` sets `brotli off; gzip off;`** and adds no request filters. Request-body
  passthrough is untouched by any of the above, but the explicit `off` is documentation that survives
  a future edit to the `http{}` defaults.
- **Preference (br over gzip)** is *the client's* to express — nginx serves `br` when the client's
  `Accept-Encoding` includes it and a `.br` exists. Cloudflare's edge always advertises both. The one
  ordering nuance worth asserting in a test: with `ngx_brotli` loaded as a dynamic module and
  `brotli_static on`, its static handler runs before the built-in `gzip_static`, so a client sending
  `Accept-Encoding: gzip, br` gets `br`. §9.1 pins that behaviour with an assertion rather than a
  belief.

Static locations:

```nginx
location /static/ {
    alias /usr/share/nginx/catlog/site/;
    add_header Vary Accept-Encoding;          # brotli_static does not add it for us
    add_header X-Content-Type-Options nosniff always;
    expires 1h;                                # assets are NOT content-hashed (site/ has no hashing step)
    access_log off;
}

# http{} — both logs go to the container's stdout/stderr, never to a file.
# `docker logs` becomes the single source (§8.1), which is what removes logrotate
# from the VM and makes `make ops-logs` one command.
access_log /dev/stdout;
error_log  /dev/stderr warn;

location /app/assets/ {                        # vite output IS content-hashed
    alias /usr/share/nginx/catlog/spa/assets/;
    add_header Vary Accept-Encoding;
    expires 1y;
    add_header Cache-Control "public, max-age=31536000, immutable";
    access_log off;
}

location /app/ {
    alias /usr/share/nginx/catlog/spa/;
    try_files $uri $uri/ /app/index.html;      # HTML5 deep links
    add_header Vary Accept-Encoding;
    expires 5m;                                # the shell must be able to point at new hashes
}
```

The `1h` on `/static/` is inherited honestly from `web.go:216`'s reasoning: **there is no hashing
step in the site build**, so a long max-age would strand a CSS change behind a browser cache. Adding
content hashing to `site/scripts/build.mjs` is listed as future work in §13, not smuggled in here.

Everything else — the ingest passthrough, the SSE regex location, `/admin/ → 403`, `client_max_body_size 2m`,
the `limit_req`/`limit_conn` zones and their sizing — is **carried across verbatim** from
`infra/nginx/prod.conf.example`, which already encodes reasoning that has been paid for. This plan
changes where those directives live and adds compression; it does not re-litigate them.

### 5.5 Cloudflare `real_ip` — now unconditional, and safe

`prod.conf.example` ships the `set_real_ip_from` block commented out, with a correct and emphatic
warning: enabling it before CF is in front and 443 is CF-only lets any client choose its own
rate-limit bucket.

In this plan **Ansible owns both halves and applies them in the required order**, so the hazard
dissolves: the firewall role restricts 80/443 to Cloudflare ranges *before* the nginx role renders a
config containing `real_ip_header CF-Connecting-IP; real_ip_recursive off;`, from the **same
`cloudflare_ranges` fact**. One list, two consumers, applied in one order — which is exactly the
condition the warning demanded.

The ranges are fetched from `https://www.cloudflare.com/ips-v4` and `…/ips-v6` at play time, with a
vendored fallback committed for air-gapped runs, and a `catlog-cf-ranges.timer` (or a scheduled
Ansible run) refreshes them. A drift check runs in `--check` mode and reports without changing.

---

## 6. Runtime layout on the VM

### 6.1 The NVMe volume

Mounted at **`/mnt/catlog`** (the Ansible variable is `catlog_data_root`; anything else works as long
as it is one path).

```
/mnt/catlog/
├── config/          0750 root:catlog   catlogd.toml (0640), catlogd.env (0640)
├── data/            0750 <uid>:<gid>   events.db, projections.db, keys/ (0700), archive/
├── turso-cache/     0700 <uid>:<gid>   extracted libturso_sync_sdk_kit.so   ← must be exec-capable
├── backups/         0750 <uid>:<gid>   catlogctl backup output
├── acme/            0700 root          acme.sh state + issued certs
└── nginx/           0755 root          optional proxy_cache dir
```

`fstab`: `nosuid,nodev,noatime` — and **never `noexec`** (§1.2). The Ansible storage role asserts
this and fails the play with the reason rather than letting `catlogd` die at `dlopen` time.

*Rejected alternative:* putting `turso-cache` on a container `tmpfs` so the volume could be `noexec`.
Compose's `tmpfs` options expose only `size` and `mode` — not the `exec` flag — and Docker's default
tmpfs flags include `noexec`. It would fail in exactly the way we are trying to prevent.

`<uid>:<gid>` is the DHI nonroot user. Pin it explicitly (`user: "65532:65532"` or whatever
`docker image inspect` reports) in **both** the compose file and `catlog_uid`/`catlog_gid`, and add a
preflight task that compares the image's configured user against the variable and fails on mismatch.
A silent mismatch here is a `permission denied` on `keys/` at first boot.

### 6.2 The compose project

`/opt/catlog/compose.yaml`, rendered by Ansible, image references **pinned by digest**:

```yaml
name: catlog
services:
  catlogd:
    image: ghcr.io/meow-sci/catlogd@sha256:…
    user: "65532:65532"
    read_only: true
    tmpfs: [/tmp]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    env_file: [/mnt/catlog/config/catlogd.env]
    volumes:
      - /mnt/catlog/config/catlogd.toml:/etc/catlog/catlogd.toml:ro
      - /mnt/catlog/data:/var/lib/catlog/data
      - /mnt/catlog/turso-cache:/var/lib/catlog/turso-cache
      - /mnt/catlog/backups:/var/lib/catlog/backups
    networks: [catlog_net]
    restart: unless-stopped
    stop_grace_period: 60s          # matches TimeoutStopSec: drain, checkpoint both WALs, close
    mem_limit: <box − nginx − headroom>
    environment:
      GOMEMLIMIT: "700MiB"          # sized to the box; GOGC=300 once the limit is set
      GOGC: "300"

  nginx:
    image: ghcr.io/meow-sci/catlog-nginx@sha256:…
    ports: ["80:80", "443:443"]
    read_only: true
    tmpfs: [/tmp, /var/cache/nginx, /var/run]
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    security_opt: ["no-new-privileges:true"]
    volumes:
      - /mnt/catlog/acme/live:/etc/nginx/certs:ro
      - /mnt/catlog/nginx:/var/cache/nginx/catlog
    depends_on:
      catlogd: {condition: service_healthy}
    networks: [catlog_net]
    restart: unless-stopped

networks:
  catlog_net: {driver: bridge}
```

`SIGTERM` reaches `catlogd` as PID 1 and `main.go` already traps `SIGINT`/`SIGTERM` before opening
anything, so shutdown drains the writer, checkpoints both WALs and releases the lock. **`stop_grace_period`
must be ≥ the shutdown grace** or Docker `SIGKILL`s a checkpoint in progress — recoverable, but
pointless (`docs/operations.md`).

### 6.3 Ports

Only nginx publishes. `catlogd:8080` is reachable only on `catlog_net`; `:6060` only inside its own
namespace. `mockidp` does not exist in production.

### 6.4 Deploy = stop → start, with automatic rollback

Forced by §1.5. The deploy play:

1. record the currently running digests to `/opt/catlog/deployed.json`;
2. `docker compose pull` the new digests (**before** stopping anything — a failed pull must not cost
   downtime);
3. `docker compose stop catlogd`, then poll until the container has actually **exited** (the lock is
   released by process exit, not by the stop request) — mirroring `deploy.sh`'s 60-attempt poll;
4. `docker compose up -d catlogd`; poll `/healthz` for up to 60 s;
5. on failure: re-pin the previous digest, `up -d`, re-poll, and fail the play loudly with the logs;
6. `docker compose up -d nginx` (a plain recreate — nginx holds no lock);
7. assert `catlogd -version` matches the tag being deployed;
8. `docker image prune` keeping the previous generation, for a fast rollback.

Expect a few seconds of 502 on the ingest path. The mod's shipper treats 5xx as retryable with
backoff, so **no telemetry is lost** — this is the same trade `deploy.sh` documents today.

nginx serves a **maintenance page for the HTML surfaces only** during the window
(`proxy_intercept_errors on; error_page 502 503 /maintenance.html;` inside `location /`).
It must **not** be applied to `location = /v1/ingest` or `/v1/*`: turning a 502 into a 200-with-HTML
would make the shipper discard a batch it should have retried, and would hand the JSON API a body no
client can parse.

### 6.5 `catlogctl` and the admin mux

`catlogctl` is an HTTP client for `127.0.0.1:6060`. From the host:

```sh
docker compose exec catlogd catlogctl <verb>     # same namespace → loopback works
```

`exec` needs no shell in the image (it execs the binary directly), so the shell-less base is not an
obstacle. Ansible wraps the ones operators actually run — `backup`, `archive`, `projections rebuild`,
`seed` (staging only) — as tagged plays, so the admin surface has a documented entry point rather
than a remembered incantation.

### 6.6 Nightly maintenance

`catlog-nightly.{service,timer}` is replaced by an Ansible-installed systemd timer on the **host**
that runs `docker compose exec -T catlogd catlogctl` for rebuild → archive → backup at 04:30 UTC.
It is installed **disabled** with the same honesty as today's unit: enabling it requires all three
verbs to be live. Backups land in `/mnt/catlog/backups` (written by catlogd, per §1.5) and an
off-box copy is out of scope for this plan and named in §13.

---

## 7. Ansible

### 7.1 Layout

```
infra/ansible/
├── ansible.cfg
├── requirements.yml                     community.docker, community.general, ansible.posix
├── inventories/prod/
│   ├── hosts.yml
│   ├── group_vars/catlog.yml            non-secret vars; secrets via lookup('env', …) (§8.2)
│   └── host_vars/<host>.yml
├── playbooks/
│   ├── preflight.yml       read-only: tools, secrets, reachability, the noexec check
│   ├── site.yml            baseline → storage → docker → firewall → certs → app
│   ├── baseline.yml        packages, users, sshd, unattended-upgrades, sysctl, journald
│   ├── storage.yml         NVMe mount + directory tree + the noexec assertion
│   ├── firewall.yml        nftables + Cloudflare ranges
│   ├── certs.yml           acme.sh DNS-01 issue/renew + nginx reload
│   ├── deploy.yml          §6.4, the one that runs on every release
│   ├── rollback.yml        re-pin the previous digests from deployed.json
│   ├── ops.yml             status + the §8.4 diagnostics bundle + catlogctl passthrough
│   ├── backup.yml          catlogctl backup + retention
│   └── restore.yml         restore → replay archive → rebuild projections
└── roles/{common,storage,docker,cloudflare_firewall,acme,catlog_app,catlog_nginx,maintenance}
```

### 7.2 Roles

| Role | Owns | Notes |
|---|---|---|
| `common` | packages, `catlog` system user/group with the pinned uid/gid, sshd hardening, unattended-upgrades, journald limits, timezone=UTC | |
| `storage` | NVMe discovery, filesystem, `fstab`, mount, the directory tree of §6.1, ownership, modes | **asserts the mount is not `noexec`** and fails with the `dlopen` reason |
| `docker` | Docker CE + compose plugin from the official repo, `daemon.json` (`json-file` rotation at 50m × 5, `live-restore`), GHCR login | rotation is load-bearing: both containers log to stdout (§8.1), so an unrotated driver fills the NVMe |
| `cloudflare_firewall` | **nftables** default-deny (already in the base system — no package installed); 80/443 from CF ranges only; 22 from `ADMIN_SSH_CIDR`; range refresh + drift check. Defers to `ufw` if it is already active | publishes `cloudflare_ranges` as a fact for `catlog_nginx` |
| `acme` | `acme.sh` in DNS-01 mode, scoped CF token, cert + key into `/mnt/catlog/acme/live`, renewal timer, nginx reload hook | §7.5 |
| `catlog_app` | `catlogd.toml`, `catlogd.env`, compose rendering, digest pinning, deploy/rollback logic, health gating | |
| `catlog_nginx` | `nginx.conf` + site config from `prod.conf.tmpl`, `real_ip` block, `nginx -t` **before** reload | validation is a hard gate, not a notification |
| `maintenance` | nightly timer, backup/restore plays, log/disk checks | installed disabled (§6.6) |

Every role is idempotent, every play supports `--check` and `--diff`, and destructive steps
(filesystem creation, firewall application) sit behind an explicit tag.

### 7.3 The production `catlogd.toml`

Rendered from a template. Differences from `catlogd.dev.toml` that must be asserted, not assumed:

| Key | Production value | Why |
|---|---|---|
| `[server] listen` | `0.0.0.0:8080` | container networking |
| `[server] admin_listen` | `127.0.0.1:6060` | never proxied, never published |
| `[server] base_url` | `https://catlog.<domain>` | license `iss` **and** the `htu` base |
| `[server] static_dir` | `""` | nginx serves it (§3.3) |
| `[server] clock_control` | `false` | `Validate()` refuses `true` with an https `base_url` — the config would not even start |
| `[ingest] accepted_htu` | `["https://catlog.<domain>/v1/ingest"]` | compared by **exact string equality**, no normalisation. A trailing slash or an `http://` here breaks every shipment |
| `[limits] ratelimit_disabled` | `false` | also refused on an https `base_url` |
| `[cors] allowed_origins` | `[]` | the SPA is same-origin now (§2) |
| `[data] dir` | `/var/lib/catlog/data` | the NVMe bind mount |

A CI job renders the template with the production variables and runs a config-validation check, so a
typo in `accepted_htu` fails a pipeline rather than a player's upload.

### 7.4 Secrets

One gitignored `infra/deploy.env` on your machine, read by Ansible through `lookup('env', …)` — the
full key table and the reasoning are in **§8.2**.

On the VM they land in exactly one place: `/mnt/catlog/config/catlogd.env`, mode `0640`, owned
`root:catlog`, rendered with `no_log: true`. The `CATLOG_<SECTION>_<KEY>` override mechanism already
exists (`config.go` package doc) and is what the systemd unit uses today. **No secret is ever written
into the TOML, none is baked into an image, and none is passed on an `ansible-playbook` command
line** — `--extra-vars` would put it in your shell history and in the VM's process table.

### 7.5 Certificates

`acme.sh` in **DNS-01** mode with a Cloudflare API token scoped to `Zone:DNS:Edit` +
`Zone:Zone:Read` on the single zone. DNS-01 rather than HTTP-01 because the record is orange-clouded
and CF's "Always Use HTTPS" redirects `/.well-known/acme-challenge/` — a fight not worth having.

**It is not a long-running sidecar.** A systemd timer on the host runs `acme.sh --cron` in a
short-lived container that has the ACME state directory and nothing else, then reloads nginx from
the host (`docker compose exec nginx nginx -s reload`). This is deliberate: a permanently running
container with the Docker socket mounted so it can reload nginx would be a larger hole than the one
DHI closes on the other side of the diagram.

Cloudflare SSL mode must be **Full (strict)** — with a real Let's Encrypt cert that is simply
correct, and it is what makes the CF↔origin leg authenticated rather than opportunistic.

---

## 8. The operator interface

Everything above is machinery. This section is the whole of what you actually type, and it is the
part the rest of the plan has to serve rather than the other way round.

**The normal release is two commands:**

```sh
make release      # build both images, smoke-test the stack, push to GHCR, print the digests
make deploy       # pull those digests on the VM, stop→start catlogd, health-gate, recreate nginx
```

**A first-time bring-up is four:**

```sh
make preflight    # check local tools + that every required secret is present; changes nothing
make provision    # one-time: baseline, NVMe mount, docker, firewall, certificates
make release
make deploy
```

Everything runs **from your machine**. Nothing in the release path needs CI, a runner or a bastion.

### 8.1 What is installed where

| Machine | Needs | Why |
|---|---|---|
| **Yours** | `docker` + `buildx`, `ansible-core`, `make`, `ssh` | buildx builds `linux/amd64` from macOS; ansible-core is the only genuinely new tool (`brew install ansible` / `uv tool install ansible-core`) |
| **The VM** | Docker CE + compose plugin, `python3`, `openssh` | Docker runs the two containers; python3 is Ansible's only remote requirement and ships in the base image of every Debian/Ubuntu release |

**Deliberately not installed on the VM** — each of these is a package the container path removes:

`go` · `node`/`pnpm` · `.NET` · `git` · `ansible` · `rsync` · `nginx` · `certbot`/`acme.sh` ·
`logrotate` · `curl`-as-a-dependency · any monitoring agent · any language runtime at all.

Consequences worth stating, because they are what keeps that list short:

- **nginx and acme.sh exist only as containers.** acme.sh runs as a short-lived container from a
  host timer (§7.5); nothing long-running holds the Docker socket.
- **No log files are written to the VM's filesystem.** Both containers log to stdout/stderr, Docker's
  `json-file` driver rotates them (`daemon.json`: `max-size=50m`, `max-file=5`), and `docker logs` is
  the single source. That removes `logrotate`, removes a disk-fill failure mode on the NVMe volume,
  and is what makes §8.4's log gathering one command instead of a scavenger hunt.
- **The firewall uses `nftables`, which is already in the base system** on Debian 13 / Ubuntu 24.04.
  The role renders a ruleset rather than installing `ufw`. If `ufw` is already present and active it
  is used instead, because two firewall front-ends on one box is a worse outcome than either.
- `unattended-upgrades` is the one optional package the baseline installs, behind
  `catlog_unattended_upgrades: true`. It is on by default: an unpatched kernel is a larger surface
  than one apt timer.

### 8.2 Secrets: one gitignored file

```
infra/deploy.env           # gitignored, real values, never leaves your machine
infra/deploy.env.example   # committed, every key documented, all values empty
```

`make` includes and exports it; Ansible reads the values with `lookup('env', …)`. **There is no
second mechanism** — no vault password file, no `--extra-vars` on the command line (which lands
secrets in your shell history and the process table), no secret in an image, no secret in
`catlogd.toml`.

| Key | Used by | Notes |
|---|---|---|
| `CATLOG_HOST` | ansible | `user@host` of the VM |
| `CATLOG_DOMAIN` | ansible, nginx, certs | e.g. `catlog.example.com` |
| `ADMIN_SSH_CIDR` | firewall role | who may reach port 22 |
| `GHCR_USER`, `GHCR_TOKEN` | `make images-push` | `write:packages`. **Laptop only** — no playbook reads it |
| `GHCR_PULL_TOKEN` | the VM's pull | `read:packages` **only**, and a different token; preflight refuses if it matches the one above |
| `DHI_USER`, `DHI_TOKEN` | `make images` | Docker Hub PAT; **build-time only**, the VM never sees it |
| `CF_API_TOKEN` | acme role | scoped `Zone:DNS:Edit` + `Zone:Zone:Read` on the one zone |
| `CATLOG_IDP_{DISCORD,GOOGLE,GITHUB}_CLIENT_{ID,SECRET}` | catlogd | reach the container through `catlogd.env` only |
| `ACME_EMAIL` | acme role | Let's Encrypt account contact |
| `CATLOG_GOMEMLIMIT` | compose | sized to the box (§6.2) |

`make preflight` fails naming **every** missing key at once, not the first one — a preflight that
makes you re-run it six times is not a preflight.

Add to `.gitignore`: `infra/deploy.env`, `diagnostics/`, `infra/ansible/*.retry`.

*Optional, not default:* `ansible-vault` for the same values, if these ever need to be shared with a
second operator. It is one variable file swap. Until there is a second operator it is pure ceremony —
the file is already gitignored and already only on your disk.

### 8.3 The commands

Thin `make` wrappers, in the style the repo already uses for `pnpm -C spa …`: each one is a single
underlying command you can also type by hand, and `make help` lists them all.

| Target | Does | Needs |
|---|---|---|
| `make preflight` | verifies local tools, `deploy.env` completeness, SSH reachability, that the VM's mount is not `noexec`. Read-only | — |
| `make images` | `buildx` both images for `linux/amd64`, tagged `:sha-<short>` and `:v<x.y.z>` | `DHI_*` |
| `make images-smoke` | brings the stack up locally on a throwaway volume and runs §9.2 | docker |
| `make images-push` | pushes both to GHCR with SBOM + provenance; prints the two digests | `GHCR_*` |
| `make release` | `images` → `images-smoke` → `images-push`, and writes the digests to `infra/.release.json` | both |
| `make provision` | `playbooks/site.yml`: baseline, storage, docker, firewall, certs. Idempotent, re-runnable | all |
| `make deploy` | `playbooks/deploy.yml` with the digests from `infra/.release.json` (§6.4) | `GHCR_*` |
| `make rollback` | re-pins the previous digests from the VM's `deployed.json` and health-gates the result | — |
| `make certs` | forces an acme renewal + nginx reload | `CF_API_TOKEN` |
| `make ops-status` | one screen: container states, `/healthz`, deployed version and digests, cert expiry, disk, memory, WAL sizes | — |
| `make ops-logs` | gathers a diagnostics bundle to `./diagnostics/` (§8.4) | — |
| `make ops-exec CMD='…'` | runs `catlogctl` against the live admin mux | — |
| `make ops-backup` | `catlogctl backup` on the VM; `FETCH=1` also pulls it local | — |
| `make ops-ssh` | an interactive shell on the VM | — |

Guardrails, because "two commands" must not also mean "two ways to lose an afternoon":

- `make deploy` **prints the digests and the currently-deployed ones and asks for confirmation**
  unless `CONFIRM=1`. In a stop-the-world deploy, the diff is the thing you want to read first.
- `make release` refuses a dirty working tree unless `ALLOW_DIRTY=1`, and stamps the real
  `git describe` into the binary — so `make ops-status` can tell you exactly what is running.
- `make deploy` auto-rolls-back on a failed health gate (§6.4) and exits non-zero. You never have to
  remember the rollback command **during** the incident; `make rollback` exists for the one you
  notice later.
- Every target is idempotent. Re-running `make provision` on a healthy box changes nothing.

### 8.4 Triage and diagnostics

`make ops-logs` runs one playbook that collects, over the existing SSH connection, into
`./diagnostics/<host>-<utc-timestamp>/`:

```
compose-ps.txt            docker compose ps -a
catlogd.log               docker logs --since=${SINCE:-2h}   (JSON lines, slog)
nginx.log                 docker logs --since=${SINCE:-2h}   (access + error, both on stdout/stderr)
admin-stats.json          catlogctl → GET /admin/stats  (db + WAL sizes: the number to watch)
healthz.json              through nginx, and direct on catlog_net
deployed.json             the digests actually running
inspect-catlogd.json      docker inspect: user, mounts, limits, restart count, OOM kills
mounts.txt                findmnt /mnt/catlog — proves exec/noexec at the time of the fault
disk.txt, mem.txt         df -h, free -m, and the NVMe's SMART summary if available
cert.txt                  openssl x509 -noout -dates on the live cert
dmesg-oom.txt             any OOM-killer lines
nft-ruleset.txt           the live firewall
versions.txt              catlogd -version, nginx -v, docker version, kernel
```

Transferred with `ansible.builtin.fetch` (SFTP, part of openssh) — **no `rsync` on the VM**.
`SINCE=24h make ops-logs` widens the window; `SERVICE=nginx` narrows the collection.

The bundle is designed to answer, without a second round trip, the failure modes this architecture
actually has:

| Symptom | The file that names the cause |
|---|---|
| catlogd exits at startup | `catlogd.log` (`dlopen` / `.so` extraction) + `mounts.txt` (`noexec`) — §1.2 |
| catlogd cannot write | `inspect-catlogd.json` user vs the volume's owner — §6.1 |
| 502s after a deploy | `deployed.json` + restart count + the health-gate output |
| ingest failing for everyone | `catlogd.log` for `htu` mismatches — the §7.3 exact-string trap |
| SSE frames arriving late | `nginx.log` + the CF cache/compression settings in §11 |
| disk filling | `admin-stats.json` WAL sizes (the WAL never auto-checkpoints) |
| rate limiting the wrong people | `nginx.log` client addresses — if they are CF edge IPs, `real_ip` is wrong (§5.5) |

`make ops-exec CMD='projections rebuild'` and friends cover the admin verbs. There is no shell in the
catlogd image and that is not an obstacle: `docker compose exec` execs the binary directly.
`make ops-ssh` plus `docker compose exec nginx sh` reaches a real shell in the one image that has
one, when you need it.

**Never collected:** `catlogd.env`, anything under `data/keys/`, and the databases. A diagnostics
bundle that contains the session key or the pepper is a diagnostics bundle you have to treat as a
secret forever. `make ops-backup FETCH=1` is the deliberate, separate action for pulling data, and it
pulls `events.db` — which holds player data — so it prompts before it does.

---

## 9. Testing

### 9.1 Extend `server/internal/nginxproxy` to the production config

The suite already exists, already drives a real nginx container via testcontainers behind
`//go:build docker`, and already asserts ingest round-trip, `X-Forwarded-For`, the oversize
rejection, nginx's own 429, sub-second SSE, `/admin/ → 403` and `/static/` bypassing Go. It is the
right place for all of this, and it is why `infra/nginx/dev.conf` is executable rather than aspirational.

Add, against a container built from **`Dockerfile.nginx`** (so the assertions cover the shipped
artifact, not a paraphrase):

| Assertion | Guards |
|---|---|
| `GET /static/css/catlog.css` with `Accept-Encoding: gzip, br` returns `Content-Encoding: br` and the byte-identical decompressed body | `brotli_static`, and the br-beats-gzip ordering nuance (§5.4) |
| the same request with `Accept-Encoding: gzip` returns `gzip` | `gzip_static` fallback |
| the same request with no `Accept-Encoding` returns the identity body | the original file is still shipped |
| every response above carries `Vary: Accept-Encoding` | cache correctness behind Cloudflare |
| a proxied HTML page returns `Content-Encoding: br` | the dynamic path is on for catlogd's output |
| `GET /v1/feed/sse` returns **no** `Content-Encoding` and the first frame lands < 1 s | §1.4 |
| `POST /v1/ingest` body arrives byte-identical (existing test, re-run against the prod config) | §1.4 — the highest-consequence assertion in the file |
| `GET /app/deep/link` returns the SPA `index.html` with 200 | `try_files` |
| `GET /app/assets/<hashed>` carries `immutable` | §5.4 |
| `GET /admin/anything` → 403 | §1.4 |

Also keep the existing structural checks (balanced braces, placeholders substitutable, nginx
variables surviving substitution) and add **`nginx -t` inside the built image** as a build-stage
`RUN`, so a broken config fails the image build rather than the deploy.

### 9.2 Container smoke test — `make images-smoke`

Brings the whole compose stack up on a throwaway volume, **on your machine before every push**
(§8.3), and asserts in order:

1. `catlogd` becomes healthy — **this is the test that proves §1.2**: glibc interpreter resolved,
   `.so` extracted, `dlopen` succeeded, keys created, both databases migrated;
2. `keys/` was created `0700` owned by the nonroot uid (catches the §6.1 uid mismatch);
3. `POST /admin/seed` via `docker compose exec` (proves the admin mux is reachable in-namespace and
   not from `catlog_net`);
4. the datastar home page renders and its `/static/` assets return 200 pre-compressed;
5. `/app/` and a deep link return the SPA;
6. a signed ingest batch from `contracts/testdata` round-trips;
7. `docker compose stop` exits within the grace period and the WAL files are checkpointed;
8. a second `up` reopens the same volume cleanly (proves nothing was corrupted and the lock was
   released).

It is a hard gate inside `make release`, so a broken image cannot reach GHCR. If `ci.yml` is ever
built (§10) it runs the same target on every PR touching `server/`, `site/`, `spa/` or `infra/`.

### 9.3 Ansible

`make preflight` is `--check --diff` against the real host and is the routine gate; `ansible-lint`
and `yamllint` run locally (and in `ci.yml` if it exists). Molecule is
**not** proposed: the smoke test above covers the containers, and the roles' real risk is host state
(mounts, firewall) that a container-based Molecule run cannot honestly exercise.

### 9.4 Image supply chain

`docker buildx` with `--provenance=mode=max --sbom=true`, pushed as attestations to GHCR; Trivy or
Grype scan gating on HIGH/CRITICAL; optional cosign keyless signing with an Ansible-side verification
step before deploy. This is most of DHI's point — an unscanned image on a hardened base has thrown
away the reason for the hardened base.

---

## 10. CI/CD — later, and optional

**The release path in §8 is the primary one and is not a stopgap.** `make release && make deploy`
from a laptop is a complete, reproducible delivery mechanism: the images are digest-pinned, the
smoke test is the same one CI would run, and the deploy is the same playbook. Nothing below is
required for the system to ship.

What CI adds, when it is worth adding:

| Workflow | Trigger | Adds |
|---|---|---|
| `ci.yml` | PR, push | `make test`, `make test-integration`, `make test-nginx`, `spa-check` — **worth doing first**, and independent of everything else here |
| `images.yml` | tags `v*` | builds and pushes without occupying your machine for the ~5 minutes two image builds take; keeps a build log you did not have to save |
| `deploy.yml` | manual dispatch with a digest | an approval gate and an audit trail, for when a second person can deploy |

Details that matter whenever they are built:

- **DHI pull auth** (`docker/login-action` against `dhi.io` with a Docker PAT) is needed in
  `images.yml` only. The VM never authenticates to `dhi.io`.
- **Tags:** `:v1.2.3`, `:sha-<short>`. **Deploys always pin a digest** — a tag can be moved, a digest
  cannot, and §6.4's rollback depends on that. This is already true of `make release`, which is why
  moving to CI later changes nothing about how a deploy works.
- **Cache:** GHA cache for the Go module/build caches and the pnpm store, keyed on the lockfiles.
- `.github/workflows/spa-pages.yml` is **deleted** when `/app/` goes live (§2) — that one does not
  wait for the rest.

---

## 11. Cloudflare

| Setting | Value | Why |
|---|---|---|
| DNS | `catlog.<domain>` A/AAAA, **proxied** | DDoS absorption, analytics |
| SSL/TLS mode | **Full (strict)** | real LE cert at the origin (§7.5) |
| Origin firewall | 80/443 from CF ranges only | the precondition for `real_ip` (§5.5) |
| Cache rules | cache `/static/*` and `/app/assets/*` aggressively; **bypass** `/v1/ingest`, all SSE routes, `/auth/*`, `/api/*`, `/dashboard` | the read API already emits `s-maxage=30`, designed for exactly this |
| Compression | leave CF's on; it re-compresses at the edge | our origin brotli reduces the **CF↔origin** leg, which is the leg we pay for |
| SSE | must be excluded from CF caching **and** compression | CF buffers SSE otherwise — the symptom is late frames, and the existing config comment already warns about it |
| Always Use HTTPS | on | with DNS-01 there is no ACME path to break |
| Bot Fight Mode | **off** on `/v1/*` | it would challenge the mod's shipper, which cannot solve a challenge |

**Do not enable nginx's `proxy_cache` micro-cache with Cloudflare in front** — the existing
`prod.conf.example` comment says it, and the reason holds: two stacked shared caches make every
staleness question twice as hard.

---

## 12. What this supersedes, and the D1 question

**D1 says catlog produces deploy assets and provisions nothing.** This plan's Ansible layer
provisions: packages, mounts, users, firewall, certificates.

That is a real conflict and it should be recorded as a scoped supersede, not quietly ignored:

> D1's reasoning is that a hand-managed VPS's state belongs to its owner, and that a deploy script
> which strays into provisioning will eventually strand the owner between the script's model and the
> box's reality. A container host has no such divergence to protect: its entire state *is* the
> playbook, and the failure D1 guards against — a script and a human disagreeing about the box —
> is instead prevented by having exactly one authority. **D1 stands for `deploy.sh` and the systemd
> path; it is superseded for the container target.**

Documents to update **in the same commit as the implementation** (CLAUDE.md's constitution, rules
1–3):

| Document | Change |
|---|---|
| `docs/operations.md` | Rewrite §11. The systemd/`deploy.sh` sections describe a path that no longer exists once this lands — delete them, do not leave them beside their replacement |
| `docs/DECISIONS.md` | New `OPS-018`…`OPS-0nn` entries (next free is **OPS-018**): the two-image split, the DHI base choice and its rejected alternatives, static-in-nginx, pre-compression, the D1 supersede, stop-start-with-rollback, `real_ip` becoming unconditional. Plus one `UI-054` (next free is **UI-054**) amending `UI-017`: the reader's default host moves to `/app/` on the origin, and why that does not undo UI-017's independence argument |
| `docs/ARCHITECTURE.md` | `infra/` layout, the new container ports/topology, **and the two-frontends table's `Hosting` and `Talks to` rows** (§2) |
| `docs/server.md` | the `-healthcheck` flag; the production config table (§7.3) |
| `DEVELOPMENT.md` | `make container-*` targets; correct the CORS claim in *"Why the SPA runs at `:5173`"* (§2) |
| `spa/README.md` | the hosting table and the `spa-pages.yml` paragraph (§2) |
| `README.md` | how the site is served, if a visitor would notice `/app/` |
| `docs/ROADMAP.md` | what §13 leaves unbuilt |

No `§` number is minted (rule 4); new material is cited by document and heading.

---

## 13. Work packages

Each is independently reviewable and leaves the tree green. **WP-K is the one that makes the rest
usable** — it can be built incrementally alongside WP-C…WP-J (each target wrapping work that already
exists) rather than waiting until the end, and it should be, because a half-built plan you can drive
with three commands is worth more than a finished one you drive by hand.

| WP | Deliverable | Done when |
|---|---|---|
| **WP-A** | `catlogd -healthcheck`; `version` becomes a `var` for `-X` stamping | unit test passes; `docs/server.md` + a `DECISIONS.md` entry updated |
| **WP-B** | `scripts/precompress.mjs`, `make precompress` | run on `site/dist` + `spa/dist`, ratios printed, no sibling larger than its original |
| **WP-C** | `infra/docker/Dockerfile.catlogd` | image builds; container reaches `/healthz` on a throwaway volume; image < 250 MB; `docker history` shows no shell, no package manager |
| **WP-D** | `infra/docker/Dockerfile.nginx` (asset stages + brotli module + baked trees) | `nginx -t` passes inside the build; both asset trees present with `.br`/`.gz` siblings |
| **WP-E** | `infra/nginx/prod.conf.tmpl`, replacing `prod.conf.example` | every §9.1 assertion passes; `prod.conf.example` deleted |
| **WP-F** | `infra/compose.prod.yaml` + `make images` / `images-smoke` | §9.2's eight steps pass on your machine against a throwaway volume |
| **WP-G** | `nginxproxy` suite extended to the prod image (§9.1) | `make test-nginx` green; the ingest-passthrough assertion runs against the prod config |
| **WP-H** | Ansible `common`, `storage`, `docker` roles + `baseline.yml` | `--check` clean on a fresh VM; the `noexec` assertion fires when it should |
| **WP-I** | `cloudflare_firewall` + `acme` roles | 443 reachable only from CF; cert issued; nginx reloads on renewal |
| **WP-J** | `catlog_app` + `catlog_nginx` roles, `deploy.yml`, `rollback.yml` | a full deploy → break-the-image → auto-rollback cycle on staging |
| **WP-K** | **The operator surface (§8):** `infra/deploy.env.example`, the `make` targets, `preflight.yml`, `ops.yml`, the diagnostics bundle, `.gitignore` entries | a fresh clone + a filled `deploy.env` reaches a running deploy in four commands, and `make ops-logs` returns a bundle that answers every row of §8.4's symptom table |
| **WP-L** | `backup.yml`, `restore.yml`, nightly timer | restore → replay archive → rebuild projections, exercised end to end on staging |
| **WP-M** | Delete `spa-pages.yml`; the §2 doc corrections; documentation sweep (§12) | no document describes the systemd path or claims the reader is cross-origin; every `OPS-0nn` entry states its *why* |
| **WP-N** *(optional)* | `ci.yml`, later `images.yml` / `deploy.yml` (§10) | green on PRs; the local path in §8 keeps working unchanged either way |

### Deliberately not in this plan

- **Content hashing for `site/dist`.** Would let `/static/` go immutable-for-a-year. Real win,
  independent change, touches `site/scripts/build.mjs` and every `<script src>` in the templates.
- **Off-box backup replication** (R2/S3). `docs/r2-archive-design.md` owns that design.
- **Multi-arch images.** The target is `linux/amd64`. `arm64` costs a second build and buys nothing
  today.
- **Metrics/tracing export.** JSON logs to journald is what exists; a Prometheus surface is a separate
  decision.
- **Staging environment provisioning.** The plan assumes one exists for WP-J and WP-L; standing one
  up is its own work. (`make provision CATLOG_HOST=…` against a second VM is the whole of it, but
  the VM itself is not free.)
- **Any orchestration beyond `make` + Ansible.** No Kubernetes, no Swarm, no Terraform. One VM, one
  compose project, one playbook directory — anything more is a second system to operate.
- **Adopting `dhi.io/static:<v>-glibc-debian13`** as the runtime base — measure after WP-C (§3.2).
