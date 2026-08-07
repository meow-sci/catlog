# catlog — root orchestration (INITIAL_IMPL_PLAN.md §9.1).
# Every path is relative to the repo root; every work package keeps these green.
#
# Targets whose work package has not landed yet print "not yet implemented (WPn)"
# and exit 0, so `make` never breaks while the plan is being executed in order.

SHELL := /bin/bash
.DEFAULT_GOAL := help

SERVER_URL ?= http://127.0.0.1:8080
ADMIN_URL  ?= http://127.0.0.1:6060
SCENARIO   ?=
CRED       ?=
# make sim ASSERT=1 …  checks the leaderboards after the run (§7.3).
ASSERT     ?=
# make sim SPEED=100 … paces the run at 100 sim seconds per wall second; unset runs flat out.
SPEED      ?=
# Throwaway data directory for `make e2e` (§8). Never ./data — see the target.
E2E_DATA_DIR ?= data-e2e

.PHONY: help bootstrap build server-build mod-build site-build \
        test server-test mod-test test-integration test-nginx \
        e2e e2e-browser e2e-full server-run-test-env \
        sim dev mockidp-run keys seed testvectors clean

## help: list targets
help:
	@echo "catlog — make targets (INITIAL_IMPL_PLAN §9.1)"
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

## site-build: bundle static assets into site/dist/
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
# The mod leg spawns server/bin/catlogd on random loopback ports with throwaway
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

## dev: run catlogd + mockidp in the foreground (Ctrl-C stops both)
dev: server-build
	@echo "catlogd  -> $(SERVER_URL)"
	@echo "mockidp  -> http://127.0.0.1:9090"
	@echo "log in at $(SERVER_URL)/auth/discord/start (also google, github)"
	@server/bin/catlogd -config server/catlogd.dev.toml & catlogd_pid=$$!; \
	 server/bin/mockidp -config server/mockidp.toml & mockidp_pid=$$!; \
	 trap 'kill $$catlogd_pid $$mockidp_pid 2>/dev/null' EXIT INT TERM; \
	 wait

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

## clean: remove build output (keeps data/ and node_modules/)
clean:
	rm -rf server/bin site/dist site/e2e/.report site/e2e/.results $(E2E_DATA_DIR) data-e2e-full
	dotnet clean mod/catlog.slnx -c Release --verbosity quiet
