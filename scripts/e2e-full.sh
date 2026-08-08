#!/usr/bin/env bash
#
# catlog full-stack proof (§8, "Full stack proof").
#
#   clean data dir
#     → start catlogd + mockidp
#     → catlogctl issue --handle sim_ace
#     → run the simulator's hop-lithobrake scenario with --assert
#     → curl the board JSON and assert sim_ace is rank 1
#     → seed the demo dataset
#     → run boards.spec.ts against the same instance
#
# Everything is local: no external host is contacted at any point (D2). The one
# thing this proves that nothing else does is that the *whole* chain works end to
# end in one process tree — the .NET mod core signing real proofs, the Go ingest
# verifying them, the projector folding them, the read API publishing them and
# the browser rendering them.
#
# Why the seed happens between the curl and playwright: sim_ace's 62 m/s
# lithobrake is only rank 1 on an otherwise-empty board, and `demo_crasher`'s
# seeded 214 m/s would outrank it. So the rank-1 assertion runs on the clean
# instance, and the demo data — which boards.spec.ts asserts by literal value —
# is inserted immediately afterwards.

set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DATA_DIR="${E2E_FULL_DATA_DIR:-data-e2e-full}"
SERVER_URL="${SERVER_URL:-http://127.0.0.1:8080}"
ADMIN_URL="${ADMIN_URL:-http://127.0.0.1:6060}"
MOCKIDP_URL="${MOCKIDP_URL:-http://127.0.0.1:9090}"
HANDLE="${E2E_FULL_HANDLE:-sim_ace}"
SCENARIO="hop-lithobrake"
BOARD="biggest_lithobrake_survived"
# The value HopLithobrakeScenario reports it will set (§7.3): touchdown at 62 m/s.
EXPECT_VALUE=62

CRED_DIR="$(mktemp -d "${TMPDIR:-/tmp}/catlog-e2e-full.XXXXXX")"
LOG_DIR="$CRED_DIR/logs"
mkdir -p "$LOG_DIR"

catlogd_pid=""
mockidp_pid=""

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  local status=$?
  for pid in "$catlogd_pid" "$mockidp_pid"; do
    [[ -n "$pid" ]] || continue
    kill "$pid" 2>/dev/null || true
  done
  # Wait for catlogd to actually exit: tursogo holds an exclusive whole-file
  # lock (§5.4), so a survivor shuts the *next* run out of its own database.
  for pid in "$catlogd_pid" "$mockidp_pid"; do
    [[ -n "$pid" ]] || continue
    for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    kill -9 "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  if (( status != 0 )); then
    echo
    echo "--- catlogd log (tail) ------------------------------------------"
    tail -n 40 "$LOG_DIR/catlogd.log" 2>/dev/null || true
    echo "--- mockidp log (tail) ------------------------------------------"
    tail -n 20 "$LOG_DIR/mockidp.log" 2>/dev/null || true
    echo "-----------------------------------------------------------------"
    echo "artifacts kept in $CRED_DIR"
  else
    rm -rf "$CRED_DIR"
  fi
  return $status
}
trap cleanup EXIT INT TERM

wait_for() { # wait_for <url> <what>
  for _ in $(seq 1 200); do
    if curl -fsS "$1" >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  fail "$2 never became healthy at $1"
}

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
need curl
need python3
need dotnet
need go

# The Makefile target depends on server-build, but this script is also runnable
# on its own — and a `make clean` in another terminal can remove the binaries
# between the two. Rebuilding is cheap and idempotent; missing binaries three
# steps in are not.
for bin in catlogd catlogctl mockidp; do
  if [[ ! -x "server/bin/$bin" ]]; then
    say "building server binaries (server/bin/$bin is missing)"
    (cd server && go build -o bin/ ./cmd/...)
    break
  fi
done

# --- 1. a clean instance --------------------------------------------------------

say "1/6  clean data directory: $DATA_DIR"
rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR/keys"

say "2/6  start catlogd + mockidp"
CATLOG_DATA_DIR="$DATA_DIR" server/bin/catlogd -config server/catlogd.dev.toml \
  >"$LOG_DIR/catlogd.log" 2>&1 &
catlogd_pid=$!
server/bin/mockidp -config server/mockidp.toml >"$LOG_DIR/mockidp.log" 2>&1 &
mockidp_pid=$!

wait_for "$SERVER_URL/healthz" catlogd
wait_for "$MOCKIDP_URL/healthz" mockidp
echo "catlogd  $SERVER_URL (admin $ADMIN_URL)"
echo "mockidp  $MOCKIDP_URL"

# --- 2. a real credential -------------------------------------------------------

say "3/6  catlogctl issue --handle $HANDLE"
server/bin/catlogctl issue -handle "$HANDLE" -out "$CRED_DIR" -admin "$ADMIN_URL"
CRED="$CRED_DIR/catlog-credential.json"
[[ -f "$CRED" ]] || fail "catlogctl issue produced no $CRED"
python3 - "$CRED" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
missing = [k for k in ("format", "handle", "license", "private_key_pem") if k not in c]
if missing:
    raise SystemExit(f"credential is missing {missing}")
if "-----BEGIN PRIVATE KEY-----" not in c["private_key_pem"]:
    raise SystemExit("credential carries no PKCS#8 private key")
print(f"credential ok: handle={c['handle']} format={c['format']}")
PY

# --- 3. fly it ------------------------------------------------------------------

say "4/6  simulator: $SCENARIO --assert"
# The scenario ships real §4.1 batches with real ES256 proofs over the real
# ingest API, and --assert reads the leaderboards back through the read API.
dotnet run --project mod/catlog.sim -c Release -v quiet -- \
  --scenario "$SCENARIO" \
  --server "$SERVER_URL" \
  --admin "$ADMIN_URL" \
  --credential "$CRED" \
  --assert

# --- 4. the board says so too ---------------------------------------------------

say "5/6  GET /v1/leaderboards/$BOARD → $HANDLE at rank 1"
curl -fsS "$SERVER_URL/v1/leaderboards/$BOARD" >"$CRED_DIR/board.json"
python3 - "$CRED_DIR/board.json" "$HANDLE" "$EXPECT_VALUE" <<'PY'
import json, sys
board = json.load(open(sys.argv[1]))
want_handle, want_value = sys.argv[2], float(sys.argv[3])
rows = board.get("rows") or []
if not rows:
    raise SystemExit(f"{board['stat']} is empty — the simulator's events never scored")
top = rows[0]
if top["rank"] != 1:
    raise SystemExit(f"the first row is rank {top['rank']}, not 1")
if top["handle"] != want_handle:
    raise SystemExit(f"rank 1 is {top['handle']!r}, want {want_handle!r}")
if abs(top["value"] - want_value) > 1e-6:
    raise SystemExit(f"rank 1 value is {top['value']}, want {want_value}")
print(f"rank 1: {top['handle']} = {top['value']} {board['unit']}  context={top.get('context')}")
PY

# --- 5. and the browser agrees --------------------------------------------------

say "6/6  seed the demo dataset, then playwright boards.spec.ts against this instance"
curl -fsS -X POST "$ADMIN_URL/admin/seed" >/dev/null
# CATLOG_E2E_EXTERNAL tells playwright.config.ts not to start its own servers:
# the whole point is to render *this* instance, the one the simulator flew into.
CATLOG_E2E_EXTERNAL=1 \
CATLOG_E2E_BASE_URL="$SERVER_URL" \
CATLOG_E2E_ADMIN_URL="$ADMIN_URL" \
  pnpm -C site exec playwright test --config e2e/playwright.config.ts boards.spec.ts

say "e2e-full: PASS"
