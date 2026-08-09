# catlog — root orchestration (DEVELOPMENT.md).
# Every path is relative to the repo root; every target runs from there.
#
# SCOPE: all three buildable things — the Go server (`server/`), the .NET mod and
# harnesses (`mod/`), and the server-rendered datastar site (`site/`).

SHELL := /bin/bash
.DEFAULT_GOAL := help

SERVER_URL ?= http://127.0.0.1:8080
ADMIN_URL  ?= http://127.0.0.1:6060
MOCKIDP_URL ?= http://127.0.0.1:9090
SCENARIO   ?=
CRED       ?=
# make sim ASSERT=1 …  checks the leaderboards after the run (§7.3).
# make loadgen ASSERT=1 … checks the load harness's end-to-end invariants.
ASSERT     ?=
# make sim SPEED=100 … paces the run at 100 sim seconds per wall second; unset runs flat out.
SPEED      ?=
# make db-snapshot SNAPSHOT_DIR=/tmp/foo … where the snapshot lands.
SNAPSHOT_DIR ?= ./data-snapshot

# --- catlog.loadgen ---------------------------------------------------------
# The high-volume harness. Everything below is a pass-through: unset variables
# are simply not forwarded, so the tool's own defaults apply and `--help` stays
# the single place they are documented.
PLAYERS     ?=
DURATION    ?=
SEED        ?=
NAMESPACE   ?=
CONCURRENCY ?=
BATCH       ?=
SHIP_AGE    ?=
AUTH        ?=
IDP         ?=
CLOCK       ?=
READERS     ?=
READ_RPS    ?=
MODERATION  ?=
TOO_NEW     ?=
REPORT      ?=
TIMEOUT     ?=
VERBOSE     ?=
LOADGEN_ARGS ?=
# Throwaway data directory for `make e2e` (§8). Never ./data — see the target.
E2E_DATA_DIR ?= data-e2e

.PHONY: help bootstrap build server-build mod-build site-build \
        test server-test mod-test test-integration test-nginx \
        e2e e2e-browser e2e-full server-run-test-env \
        sim loadgen dev dev-server \
        mockidp-run keys seed testvectors db-snapshot clean precompress \
        preflight images images-smoke images-ship release provision deploy \
        rollback certs ops-status ops-logs ops-exec ops-backup ops-ssh \
        deploy-env

## help: list targets
help:
	@echo "catlog — make targets (see DEVELOPMENT.md)"
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/^## /  /'

## bootstrap: fetch every build dependency (go modules, NuGet, pnpm)
bootstrap:
	cd server && go mod download
	dotnet restore mod/catlog.slnx
	pnpm -C site install

## build: server-build + mod-build + site-build
build: server-build mod-build site-build

## server-build: compile catlogd, catlogctl and mockidp into server/bin/
server-build:
	cd server && go build -o bin/ ./cmd/...

## mod-build: build the game-free .NET solution in Release
mod-build:
	dotnet build mod/catlog.slnx -c Release

## site-build: bundle the datastar site's static assets into site/dist/
site-build:
	pnpm -C site build

## test: server-test + mod-test (no docker, no network)
test: server-test mod-test

## server-test: go unit tests
server-test:
	cd server && go test ./...

## mod-test: catlog.lib unit tests (incl. the §7.1 assembly guard)
mod-test:
	dotnet test mod/catlog.lib.tests

## test-integration: server integration tests + mod-vs-server tests
# The mod leg launches server/bin/catlogd on random loopback ports with throwaway
# data directories (§7.5), so the binaries have to exist before it runs.
test-integration: server-build
	cd server && go test -tags integration -count=1 ./integration/
	dotnet test mod/catlog.integration.tests -c Release

## test-nginx: testcontainers nginx suite (clean-skips without docker)
test-nginx: server-build
	@# §9.4: the suite self-skips when the daemon is unreachable; say how to fix
	@# that before the skip scrolls past, rather than after.
	@if ! command -v docker >/dev/null 2>&1; then \
	  echo "make test-nginx: no docker CLI on PATH — the suite will skip (§9.4)."; \
	  echo "                 install Docker Desktop: https://docs.docker.com/desktop/"; \
	elif ! docker info >/dev/null 2>&1; then \
	  echo "make test-nginx: the docker daemon is unreachable — the suite will skip (§9.4)."; \
	  echo "                 macOS: open -a Docker"; \
	  echo "                 Linux: sudo systemctl start docker"; \
	  echo "                 then re-run: make test-nginx"; \
	fi
	cd server && go test -tags docker -count=1 ./internal/nginxproxy/

## e2e: playwright suite (chromium) against a throwaway seeded catlogd + mockidp
# playwright.config.ts starts both servers itself (§8) via server-run-test-env
# and mockidp-run, so this target only has to make sure the binaries, the static
# bundle and the browser exist first.
e2e: server-build site-build e2e-browser
	pnpm -C site exec playwright test --config e2e/playwright.config.ts

## e2e-browser: download the chromium playwright drives (idempotent; the one allowed network fetch)
e2e-browser:
	pnpm -C site exec playwright install chromium

## server-run-test-env: catlogd on a throwaway, seeded data directory (§8 webServer)
# Never point this at ./data: tursogo takes an exclusive whole-file lock that
# shuts every other process out entirely (§5.4), so an e2e run must not share a
# database with a dev server — and must not inherit its leftover state either,
# which is why the directory is deleted rather than reused.
server-run-test-env: server-build
	@rm -rf $(E2E_DATA_DIR)
	@mkdir -p $(E2E_DATA_DIR)/keys
	@# Seeding needs a server that is already up, and this recipe has to *become*
	@# that server for playwright's webServer to track it — hence the background
	@# poller. It exits either way; catlogd is what stays in the foreground.
	@( for i in $$(seq 1 200); do \
	     if curl -fsS $(SERVER_URL)/healthz >/dev/null 2>&1; then \
	       curl -fsS -X POST $(ADMIN_URL)/admin/seed >/dev/null \
	         && echo "server-run-test-env: demo dataset seeded" >&2; \
	       exit 0; \
	     fi; \
	     sleep 0.1; \
	   done; \
	   echo "server-run-test-env: catlogd never became healthy" >&2 ) &
	@echo "server-run-test-env: data dir $(E2E_DATA_DIR)" >&2
	CATLOG_DATA_DIR=$(E2E_DATA_DIR) exec server/bin/catlogd -config server/catlogd.dev.toml

## e2e-full: full-stack proof — server + mockidp + catlogctl + sim + read API + playwright (§8)
e2e-full: server-build site-build e2e-browser
	scripts/e2e-full.sh

## sim: run a catlog.sim scenario (SCENARIO=<name> CRED=<path> [ASSERT=1] [SPEED=n]); no SCENARIO lists them
sim:
	@dotnet run --project mod/catlog.sim -c Release -v quiet -- \
	  $(if $(strip $(SCENARIO)),--scenario "$(SCENARIO)",--list) \
	  --server "$(SERVER_URL)" --admin "$(ADMIN_URL)" \
	  $(if $(strip $(CRED)),--credential "$(CRED)",) \
	  $(if $(strip $(ASSERT)),--assert,) \
	  $(if $(strip $(SPEED)),--speed "$(SPEED)",)

## loadgen: high-volume harness — many randomised players at a live server (PLAYERS=, DURATION=, SEED=, …; --help lists them all)
# Needs `make dev` (or the lighter `make dev-server`) in another terminal: it
# provisions every player through the real mockidp OAuth flow and ships through
# the real catlog.lib pipeline.
#
#   make loadgen                                     # 25 players, 45 simulated minutes
#   make loadgen PLAYERS=250 DURATION=3h ASSERT=1    # a serious run, invariants checked
#   make loadgen SEED=4242 REPORT=json               # reproducible; JSON on stdout, progress on stderr
#   make loadgen LOADGEN_ARGS="--auth admin --no-feed"   # anything not given its own variable
#
# Unset variables are not forwarded at all, so the tool's defaults apply and
# `make loadgen LOADGEN_ARGS=--help` remains the one place they are documented.
loadgen:
	@dotnet run --project mod/catlog.loadgen -c Release -v quiet -- \
	  --server "$(SERVER_URL)" --admin "$(ADMIN_URL)" --mockidp "$(MOCKIDP_URL)" \
	  $(if $(strip $(PLAYERS)),--players "$(PLAYERS)",) \
	  $(if $(strip $(DURATION)),--duration "$(DURATION)",) \
	  $(if $(strip $(SEED)),--seed "$(SEED)",) \
	  $(if $(strip $(NAMESPACE)),--namespace "$(NAMESPACE)",) \
	  $(if $(strip $(CONCURRENCY)),--concurrency "$(CONCURRENCY)",) \
	  $(if $(strip $(BATCH)),--batch "$(BATCH)",) \
	  $(if $(strip $(SHIP_AGE)),--ship-age "$(SHIP_AGE)",) \
	  $(if $(strip $(AUTH)),--auth "$(AUTH)",) \
	  $(if $(strip $(IDP)),--idp "$(IDP)",) \
	  $(if $(strip $(CLOCK)),--clock "$(CLOCK)",) \
	  $(if $(strip $(READERS)),--readers "$(READERS)",) \
	  $(if $(strip $(READ_RPS)),--read-rps "$(READ_RPS)",) \
	  $(if $(strip $(MODERATION)),--moderation "$(MODERATION)",) \
	  $(if $(strip $(TOO_NEW)),--too-new "$(TOO_NEW)",) \
	  $(if $(strip $(REPORT)),--report "$(REPORT)",) \
	  $(if $(strip $(TIMEOUT)),--timeout "$(TIMEOUT)",) \
	  $(if $(strip $(ASSERT)),--assert,) \
	  $(if $(strip $(VERBOSE)),--verbose,) \
	  $(LOADGEN_ARGS)

## dev: catlogd + mockidp (Ctrl-C stops both)
# One command brings up everything a browser can reach: the datastar site and
# the read API on 8080 (rendered by catlogd) and the three stand-in identity
# providers on 9090.
dev: server-build
	@echo "catlogd  -> $(SERVER_URL)   (datastar site + read API)"
	@echo "mockidp  -> $(MOCKIDP_URL)"
	@echo "log in at $(SERVER_URL)/auth/discord/start (also google, github)"
	@server/bin/catlogd -config server/catlogd.dev.toml & catlogd_pid=$$!; \
	 server/bin/mockidp -config server/mockidp.toml & mockidp_pid=$$!; \
	 trap 'kill $$catlogd_pid $$mockidp_pid 2>/dev/null' EXIT INT TERM; \
	 wait

## dev-server: same as `make dev` — kept because loadgen and the e2e docs name it
dev-server: dev

## mockidp-run: run mockidp alone on 127.0.0.1:9090 (playwright webServer, §8)
mockidp-run: server-build
	server/bin/mockidp -config server/mockidp.toml

## keys: create data/keys/{license-signing.pem,session.key,pepper.key}
keys: server-build
	server/bin/catlogctl keygen

## seed: insert the deterministic demo dataset for UI development
seed: server-build
	server/bin/catlogctl seed

## testvectors: regenerate contracts/testdata (§4.10) — byte-identical every run
testvectors: server-build
	server/bin/catlogctl testvectors generate contracts/testdata

## db-snapshot: copy the live databases to ./data-snapshot for IDE/ad-hoc SQL
db-snapshot:
	scripts/db-snapshot.sh $(SNAPSHOT_DIR)

## precompress: generate .br/.gz siblings for the built asset trees
# The same script the container build runs (infra/docker/Dockerfile.nginx), so
# the ratios can be inspected without building an image. `CHECK=1` writes
# nothing.
precompress: site-build
	node scripts/precompress.mjs $(if $(strip $(CHECK)),--check,) site/dist

# ===========================================================================
# DEPLOYMENT — build, push, provision, operate.  See docs/operations.md.
# ===========================================================================
#
# The normal release is two commands:
#
#     make release        build both images, smoke-test, push, print the digests
#     make deploy         pull them on the VM, replace catlogd, health-gate
#
# A first-time bring-up is four:
#
#     make preflight && make provision && make release && make deploy
#
# Every target below is a thin wrapper around one docker or scripts/ansible.sh
# command you could type by hand. Settings and secrets come from infra/deploy.env
# (gitignored; copy infra/deploy.env.example), which is included and exported
# here and nowhere else.

# Overridable so the deployment scripts can be exercised without writing to the
# real settings file, which holds every secret and has no backup anywhere.
DEPLOY_ENV   ?= infra/deploy.env
ANSIBLE_DIR  := infra/ansible
RELEASE_FILE := infra/.release.env
DIAG_DIR     := diagnostics

# Version stamped into the binary and used as the image tag. `git describe`
# rather than a hand-maintained number, so `make ops-status` can tell you
# exactly which commit is running.
#
# NAMESPACED, and that is not style. These were briefly called VERSION/COMMIT,
# and because the deployment targets need infra/deploy.env in the environment,
# the Makefile exported everything — at which point MSBuild picked `VERSION` up
# as a project property and `dotnet build` failed with "'0110f3c-dirty' is not a
# valid version string". `make test` is not allowed to care that these exist.
CATLOG_VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CATLOG_COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
CATLOG_BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Local image names. There is no registry: `make images-ship` streams them to
# the VM over ssh (scripts/ship-images.sh), so nothing needs a hostname prefix
# and nothing needs an account.
CATLOGD_REPO := catlog/catlogd
NGINX_REPO   := catlog/catlog-nginx

# Ansible runs in a container (scripts/ansible.sh, alpine/ansible), so the only
# things this machine needs are docker and ssh. The wrapper mounts infra/ and
# diagnostics/, passes infra/deploy.env with --env-file, and synthesises a passwd
# entry for your uid — without which OpenSSH refuses to start at all.
#
# infra/deploy.env is sourced INSIDE each deployment recipe, never `include`d
# with a global `export`. Two reasons, both learned the hard way:
#
#   * a global export leaks every make variable into every recipe, which is how
#     `VERSION` reached MSBuild and broke `make test` (see above);
#   * `make test` and every development target must keep working in a fresh
#     clone that has no deploy.env at all.
#
# `set -a` marks everything sourced for export, so Ansible's lookup('env', …)
# sees the whole file — including keys added after this Makefile was written.
LOAD_ENV = set -a; . $(CURDIR)/$(DEPLOY_ENV); set +a;
NEED_ENV = @test -f $(DEPLOY_ENV) || { echo "no $(DEPLOY_ENV) — run 'make deploy-env' first" >&2; exit 1; }

ANSIBLE = scripts/ansible.sh

## deploy-env: create infra/deploy.env from the example (safe to re-run)
deploy-env:
	@if [ -f $(DEPLOY_ENV) ]; then \
	  echo "$(DEPLOY_ENV) already exists — not overwriting."; \
	else \
	  cp infra/deploy.env.example $(DEPLOY_ENV); \
	  echo "created $(DEPLOY_ENV) — fill it in, then run 'make preflight'"; \
	fi

## preflight: check local tools, secrets and the VM. Read-only, changes nothing
preflight:
	@test -f $(DEPLOY_ENV) || { echo "no $(DEPLOY_ENV) — run 'make deploy-env' first" >&2; exit 1; }
	@command -v docker  >/dev/null || { echo "docker is not on PATH" >&2; exit 1; }
	@docker buildx version >/dev/null 2>&1 || { echo "docker buildx is missing" >&2; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "the docker daemon is unreachable — start Docker Desktop" >&2; exit 1; }
	@command -v ssh >/dev/null || { echo "ssh is not on PATH" >&2; exit 1; }
	@# Catches a base-image bump that removed a config key or a plugin, which
	@# --syntax-check cannot: it loads neither callbacks nor connection plugins.
	@ANSIBLE_ENTRY=ansible-config scripts/ansible.sh validate
	$(ANSIBLE) playbooks/preflight.yml

## images: build catlogd and catlog-nginx for linux/amd64
images:
	docker buildx build --platform linux/amd64 --load \
	  -f infra/docker/Dockerfile.catlogd \
	  --build-arg VERSION=$(CATLOG_VERSION) --build-arg COMMIT=$(CATLOG_COMMIT) \
	  --build-arg BUILD_DATE=$(CATLOG_BUILD_DATE) \
	  -t $(CATLOGD_REPO):$(CATLOG_VERSION) -t $(CATLOGD_REPO):sha-$(CATLOG_COMMIT) .
	docker buildx build --platform linux/amd64 --load \
	  -f infra/docker/Dockerfile.nginx \
	  --build-arg VERSION=$(CATLOG_VERSION) --build-arg COMMIT=$(CATLOG_COMMIT) \
	  -t $(NGINX_REPO):$(CATLOG_VERSION) -t $(NGINX_REPO):sha-$(CATLOG_COMMIT) .
	@echo
	@docker image inspect $(CATLOGD_REPO):$(CATLOG_VERSION) \
	  --format 'catlogd     {{.Size}} bytes, runs as {{if .Config.User}}{{.Config.User}}{{else}}root (!!){{end}}'
	@docker image inspect $(NGINX_REPO):$(CATLOG_VERSION) --format 'catlog-nginx {{.Size}} bytes'

## images-smoke: run the whole stack locally on a throwaway volume and prove it works
images-smoke:
	scripts/container-smoke.sh $(CATLOGD_REPO):$(CATLOG_VERSION) $(NGINX_REPO):$(CATLOG_VERSION)

## images-ship: stream both images to the VM over ssh, and record the release
# `docker save | ssh docker load`. No registry, no tarball on either end. Skips
# an image whose ID already matches the one on the VM, so a server-only redeploy
# does not re-send the nginx image.
images-ship:
	scripts/ship-images.sh $(CATLOGD_REPO):$(CATLOG_VERSION) $(NGINX_REPO):$(CATLOG_VERSION) $(CATLOG_VERSION)

## release: images + images-smoke + images-ship  (the one you run before deploying)
release:
	@if [ -n "$$(git status --porcelain)" ] && [ -z "$(ALLOW_DIRTY)" ]; then \
	  echo "the working tree is dirty — commit first, or 'make release ALLOW_DIRTY=1'" >&2; exit 1; fi
	@$(MAKE) images
	@$(MAKE) images-smoke
	@$(MAKE) images-ship
	@echo
	@echo "released $(CATLOG_VERSION). Next: make deploy"

## provision: one-time (and re-runnable) — baseline, storage, docker, firewall, certs, app
provision:
	$(NEED_ENV)
	$(ANSIBLE) playbooks/site.yml $(if $(strip $(TAGS)),--tags "$(TAGS)",) $(ANSIBLE_ARGS)

## deploy: pull the released digests on the VM and replace catlogd (CONFIRM=1 to skip the prompt)
deploy:
	@test -f $(RELEASE_FILE) || { echo "no $(RELEASE_FILE) — run 'make release' first" >&2; exit 1; }
	@cat $(RELEASE_FILE)
	@if [ -z "$(CONFIRM)" ]; then \
	  $(LOAD_ENV) printf '\nDeploy these to %s? catlogd will be STOPPED and restarted. [y/N] ' "$$CATLOG_SSH_HOST"; \
	  read a; [ "$$a" = y ] || { echo aborted; exit 1; }; fi
	$(NEED_ENV)
	set -a; . $(CURDIR)/$(RELEASE_FILE); set +a; \
	  $(ANSIBLE) playbooks/deploy.yml $(ANSIBLE_ARGS)

## rollback: return to the previous digests recorded on the VM
rollback:
	$(NEED_ENV)
	$(ANSIBLE) playbooks/rollback.yml $(ANSIBLE_ARGS)

## certs: issue or renew the TLS certificate, then reload nginx if it changed
# Issues only when there is no certificate or a name is missing from it, and
# acme.sh independently refuses to renew until within 30 days of expiry.
# FORCE=1 overrides both — needed when switching from the staging CA to the
# production one, and rate-limited by Let's Encrypt, so not routine.
certs:
	$(NEED_ENV)
	$(if $(strip $(FORCE)),ACME_FORCE=1,) $(ANSIBLE) playbooks/certs.yml $(ANSIBLE_ARGS)

## ops-status: one screen — containers, version, health, cert expiry, disk, firewall
ops-status:
	$(NEED_ENV)
	$(ANSIBLE) playbooks/ops.yml --tags status $(ANSIBLE_ARGS)

## ops-logs: gather a diagnostics bundle into ./diagnostics/ (SINCE=24h widens the window)
ops-logs:
	@mkdir -p $(DIAG_DIR)
	@stamp=$$(date -u +%Y%m%dT%H%M%SZ); dest="$(DIAG_DIR)/$$stamp"; mkdir -p "$$dest"; \
	 scripts/ansible.sh playbooks/ops.yml --tags logs \
	   -e catlog_fetch_dest="/work/diagnostics/$$stamp" $(ANSIBLE_ARGS) && \
	 tar -xzf "$(CURDIR)/$$dest/diagnostics.tar.gz" -C "$(CURDIR)/$$dest" --strip-components=1 && \
	 rm -f "$(CURDIR)/$$dest/diagnostics.tar.gz" && \
	 echo && echo "diagnostics in $$dest:" && ls -la "$(CURDIR)/$$dest"

## ops-exec: run a catlogctl verb against the live server (CMD='projections rebuild')
ops-exec:
	@test -n "$(CMD)" || { echo "usage: make ops-exec CMD='projections rebuild'" >&2; exit 1; }
	$(NEED_ENV)
	$(ANSIBLE) playbooks/ops.yml --tags exec -e catlog_ctl_cmd="$(CMD)" $(ANSIBLE_ARGS)

## ops-backup: take a backup on the VM (FETCH=1 also copies it here — it holds player data)
ops-backup:
	@if [ -n "$(FETCH)" ]; then \
	  printf 'This copies events.db, which holds player data, to this machine. Continue? [y/N] '; \
	  read a; [ "$$a" = y ] || { echo aborted; exit 1; }; fi
	@mkdir -p $(DIAG_DIR)
	$(NEED_ENV)
	$(ANSIBLE) playbooks/backup.yml \
	  $(if $(strip $(FETCH)),-e catlog_fetch_backup=true -e catlog_fetch_dest="$(CURDIR)/$(DIAG_DIR)",) \
	  $(ANSIBLE_ARGS)

## ops-ssh: an interactive shell on the VM
ops-ssh:
	$(NEED_ENV)
	@# Through the same generated config the playbooks use, so this is the same
	@# connection — same key, same pinned host key — and not whatever your own
	@# ~/.ssh/config would have decided.
	scripts/ssh.sh

## clean: remove build output (keeps data/ and node_modules/)
clean:
	rm -rf server/bin site/dist \
	       site/e2e/.report site/e2e/.results $(E2E_DATA_DIR) data-e2e-full
	dotnet clean mod/catlog.slnx -c Release --verbosity quiet
