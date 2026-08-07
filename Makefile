# catlog — root orchestration (INITIAL_IMPL_PLAN.md §9.1).
# Every path is relative to the repo root; every work package keeps these green.
#
# Targets whose work package has not landed yet print "not yet implemented (WPn)"
# and exit 0, so `make` never breaks while the plan is being executed in order.

SHELL := /bin/bash
.DEFAULT_GOAL := help

SERVER_URL ?= http://127.0.0.1:8080
SCENARIO   ?=
CRED       ?=

.PHONY: help bootstrap build server-build mod-build site-build \
        test server-test mod-test test-integration test-nginx \
        e2e e2e-full sim dev keys seed testvectors clean

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
test-integration:
	@echo "make test-integration: not yet implemented (WP2 server side, WP7 mod side)"
	@# cd server && go test -tags integration ./...
	@# dotnet test mod/catlog.integration.tests

## test-nginx: testcontainers nginx suite (clean-skips without docker)
test-nginx:
	@echo "make test-nginx: not yet implemented (WP9)"
	@# cd server && go test -tags docker ./internal/nginxproxy/

## e2e: playwright suite against a local catlogd + mockidp
e2e:
	@echo "make e2e: not yet implemented (WP5)"
	@# $(MAKE) site-build && pnpm -C site exec playwright test

## e2e-full: full-stack proof — server + mockidp + sim + read API + playwright
e2e-full:
	@echo "make e2e-full: not yet implemented (WP5 + WP7); scripts/e2e-full.sh lands there"

## sim: run a catlog.sim scenario (SCENARIO=<name> CRED=<path to credential json>)
sim:
	dotnet run --project mod/catlog.sim -- --scenario "$(SCENARIO)" --server "$(SERVER_URL)" --credential "$(CRED)"

## dev: run catlogd + mockidp in the foreground (Ctrl-C stops both)
dev: server-build
	@echo "catlogd  -> $(SERVER_URL)"
	@echo "mockidp  -> http://127.0.0.1:9090"
	@server/bin/catlogd & catlogd_pid=$$!; \
	 server/bin/mockidp & mockidp_pid=$$!; \
	 trap 'kill $$catlogd_pid $$mockidp_pid 2>/dev/null' EXIT INT TERM; \
	 wait

## keys: create data/keys/{license-signing.pem,session.key,pepper.key}
keys:
	@echo "make keys: not yet implemented (WP1)"
	@# server/bin/catlogctl keygen

## seed: insert the deterministic demo dataset for UI development
seed:
	@echo "make seed: not yet implemented (WP4)"
	@# server/bin/catlogctl seed

## testvectors: regenerate contracts/testdata (§4.10)
testvectors:
	@echo "make testvectors: not yet implemented (WP2)"
	@# server/bin/catlogctl testvectors generate contracts/testdata

## clean: remove build output (keeps data/ and node_modules/)
clean:
	rm -rf server/bin site/dist
	dotnet clean mod/catlog.slnx -c Release --verbosity quiet
