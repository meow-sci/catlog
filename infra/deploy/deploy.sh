#!/usr/bin/env bash
# catlog — deploy to the owner-managed VPS (§11, D1).
#
#   infra/deploy/deploy.sh --host catlog@vps.example.com [--dry-run]
#
# What it does, in order:
#   1. cross-compile catlogd + catlogctl for linux/amd64
#   2. pnpm -C site build
#   3. rsync binaries, site/dist and the deploy assets into a staging directory
#      on the VPS
#   4. over ssh: STOP catlogd, install the binaries, sync the static assets,
#      START catlogd, and wait for /healthz
#
# What it will NEVER do (D1 — the droplet is owner-managed; see the header of
# infra/systemd/catlogd.service for the one-time setup):
#   * create users, directories or databases        * install packages
#   * write /etc/catlog/catlogd.toml or catlogd.env * touch nginx's config
#   * obtain or renew certificates                  * change firewall rules
# The nginx and systemd files are copied into the staging directory only, for
# the owner to diff and install by hand (or with --install-units for systemd).
#
# ---------------------------------------------------------------------------
# WHY THERE IS A DOWNTIME WINDOW, AND WHY IT IS NOT A BUG
# ---------------------------------------------------------------------------
# Turso holds an exclusive whole-file lock on events.db/projections.db that
# excludes other PROCESSES entirely — readers included (verified in WP1,
# TestSecondProcessIsLockedOut). A rolling or blue/green deploy would put two
# catlogd processes on the same files, and the new one would fail to open them.
# So this script stops the old process, waits for it to exit, and only then
# starts the new one. Expect a few seconds of 502 from nginx; the mod's shipper
# treats 5xx as retryable with backoff (§4.5.3), so no telemetry is lost.
#
# Idempotent: running it twice with no source changes leaves the VPS in exactly
# the same state (install(1) and rsync(1) are declarative, systemctl start on a
# running unit is a no-op).

set -euo pipefail

# --- defaults ---------------------------------------------------------------

HOST="${CATLOG_DEPLOY_HOST:-}"
SSH_OPTS="${CATLOG_DEPLOY_SSH_OPTS:-}"
REMOTE_SUDO="${CATLOG_DEPLOY_SUDO:-sudo}"
DRY_RUN=false
INSTALL_UNITS=false
SKIP_SITE=false

# Paths on the VPS. They match infra/systemd/catlogd.service; change both or
# neither.
REMOTE_STAGE=/var/lib/catlog/incoming
REMOTE_BIN=/usr/local/bin
REMOTE_SITE=/var/lib/catlog/site
HEALTH_URL=http://127.0.0.1:8080/healthz

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
STAGE="$ROOT/server/bin/deploy"   # under server/bin/, which .gitignore covers

usage() {
    cat <<'USAGE'
usage: infra/deploy/deploy.sh --host <user@host> [options]

options:
  --host <user@host>   VPS to deploy to (or set CATLOG_DEPLOY_HOST)
  --dry-run            print every command and change nothing, locally or remotely
  --install-units      also install infra/systemd/* into /etc/systemd/system
                       and daemon-reload (off by default: unit files are the
                       owner's, and enabling the nightly timer needs WP4+WP10)
  --skip-site-build    reuse the existing site/dist instead of running pnpm
  --sudo <cmd>         remote privilege escalation, default "sudo"; use
                       --sudo "" when deploying as root
  --ssh-opt <opt>      extra ssh option, repeatable (e.g. --ssh-opt "-p 2222")
  -h, --help           this text

environment:
  CATLOG_DEPLOY_HOST, CATLOG_DEPLOY_SSH_OPTS, CATLOG_DEPLOY_SUDO
USAGE
}

# --- argument parsing -------------------------------------------------------

while [ $# -gt 0 ]; do
    case "$1" in
        --host)            HOST="${2:?--host needs a value}"; shift 2 ;;
        --host=*)          HOST="${1#*=}"; shift ;;
        --dry-run)         DRY_RUN=true; shift ;;
        --install-units)   INSTALL_UNITS=true; shift ;;
        --skip-site-build) SKIP_SITE=true; shift ;;
        --sudo)            REMOTE_SUDO="${2-}"; shift 2 ;;
        --sudo=*)          REMOTE_SUDO="${1#*=}"; shift ;;
        --ssh-opt)         SSH_OPTS="$SSH_OPTS ${2:?--ssh-opt needs a value}"; shift 2 ;;
        -h|--help)         usage; exit 0 ;;
        *)                 echo "deploy.sh: unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# --- helpers ----------------------------------------------------------------

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die()  { printf 'deploy.sh: %s\n' "$*" >&2; exit 1; }

# run executes a command, or prints it when --dry-run is set. Every mutating
# step goes through this, which is what makes --dry-run total.
run() {
    if $DRY_RUN; then
        printf '    [dry-run] '; printf '%q ' "$@"; printf '\n'
        return 0
    fi
    "$@"
}

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"; }

# --- preflight (read-only, runs even under --dry-run) -----------------------

[ -n "$HOST" ] || { usage >&2; die "no --host given"; }

need go
need rsync
need ssh
$SKIP_SITE || need pnpm

[ -f "$ROOT/server/go.mod" ] || die "cannot find the repo root (looked at $ROOT)"

REV="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
DIRTY=""
if ! git -C "$ROOT" diff --quiet HEAD 2>/dev/null; then DIRTY=" (working tree dirty)"; fi

log "catlog deploy → $HOST"
note "source     $ROOT @ $REV$DIRTY"
note "staging    $REMOTE_STAGE"
note "binaries   $REMOTE_BIN/{catlogd,catlogctl}"
note "site       $REMOTE_SITE"
if $DRY_RUN; then note "DRY RUN — nothing will be built, copied, stopped or started"; fi

# --- 1. cross-compile linux/amd64 ------------------------------------------

log "build catlogd + catlogctl (linux/amd64)"
# CGO_ENABLED=0 is honest but does NOT produce a static binary here: tursogo
# reaches its engine through purego, whose fakecgo shim leaves a glibc ELF
# interpreter in the executable and extracts a native .so at startup. That is
# why the target must be a glibc distro (Debian/Ubuntu — what DO ships) and why
# `scratch`/`distroless/static` images do not work. Alpine would need
# `-tags musl`.
run mkdir -p "$STAGE/bin" "$STAGE/site" "$STAGE/systemd" "$STAGE/nginx"
run env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go -C "$ROOT/server" build -trimpath -ldflags "-s -w" \
    -o "$STAGE/bin/" ./cmd/catlogd ./cmd/catlogctl

if ! $DRY_RUN && command -v file >/dev/null 2>&1; then
    note "$(file -b "$STAGE/bin/catlogd")"
fi

# --- 2. site assets ---------------------------------------------------------

if $SKIP_SITE; then
    log "site build skipped (--skip-site-build)"
    [ -d "$ROOT/site/dist" ] || die "--skip-site-build needs an existing site/dist"
else
    log "build site assets"
    run pnpm -C "$ROOT/site" build
fi

# --- 3. stage the payload ---------------------------------------------------

log "stage payload in $STAGE"
run rsync -a --delete "$ROOT/site/dist/" "$STAGE/site/"
run rsync -a --delete "$ROOT/infra/systemd/" "$STAGE/systemd/"
run rsync -a --delete "$ROOT/infra/nginx/" "$STAGE/nginx/"

log "copy payload to $HOST:$REMOTE_STAGE"
# --delete keeps the staging directory an exact mirror, so a file removed from
# the repo does not linger on the VPS and get installed by a later run.
# --rsync-path runs the remote rsync under sudo, because $REMOTE_STAGE lives
# under /var/lib/catlog. If the deploy account has no passwordless sudo, either
# chown $REMOTE_STAGE to it and pass --sudo "", or configure NOPASSWD.
run rsync -az --delete --rsync-path="$REMOTE_SUDO rsync" -e "ssh $SSH_OPTS" \
    "$STAGE/" "$HOST:$REMOTE_STAGE/"

# --- 4. install and restart -------------------------------------------------

# The remote half is one script so the stop→install→start sequence cannot be
# interrupted between ssh invocations. It is `set -euo pipefail` in its own
# right: a failed install must not be followed by a start.
remote_script() {
    cat <<REMOTE
set -euo pipefail
SUDO="$REMOTE_SUDO"
STAGE="$REMOTE_STAGE"
BIN="$REMOTE_BIN"
SITE="$REMOTE_SITE"
INSTALL_UNITS=$INSTALL_UNITS
HEALTH_URL="$HEALTH_URL"
REMOTE
    cat <<'REMOTE'

say() { printf '    [%s] %s\n' "$(hostname -s)" "$*"; }

[ -x "$STAGE/bin/catlogd" ] || { echo "staged catlogd is missing" >&2; exit 1; }

# Stop first, always. Two catlogd processes cannot share the database files.
say "stopping catlogd"
$SUDO systemctl stop catlogd || true
# systemctl stop is synchronous, but be explicit: the file lock is released by
# process exit, not by the stop request.
for _ in $(seq 1 60); do
    $SUDO systemctl is-active --quiet catlogd || break
    sleep 1
done
if $SUDO systemctl is-active --quiet catlogd; then
    echo "catlogd is still running after 60 s — refusing to install over a live process" >&2
    exit 1
fi

# Keep one generation for rollback. `cp -a` not `mv`: if the install fails the
# old binary is still in place.
if [ -f "$BIN/catlogd" ]; then $SUDO cp -a "$BIN/catlogd" "$BIN/catlogd.prev"; fi
if [ -f "$BIN/catlogctl" ]; then $SUDO cp -a "$BIN/catlogctl" "$BIN/catlogctl.prev"; fi

say "installing binaries"
$SUDO install -m 0755 "$STAGE/bin/catlogd"   "$BIN/catlogd"
$SUDO install -m 0755 "$STAGE/bin/catlogctl" "$BIN/catlogctl"

say "syncing static assets"
$SUDO mkdir -p "$SITE"
$SUDO rsync -a --delete "$STAGE/site/" "$SITE/"
if id -u catlog >/dev/null 2>&1; then $SUDO chown -R catlog:catlog "$SITE"; fi

if [ "$INSTALL_UNITS" = "true" ]; then
    say "installing systemd units"
    $SUDO install -m 0644 "$STAGE/systemd/catlogd.service"        /etc/systemd/system/catlogd.service
    $SUDO install -m 0644 "$STAGE/systemd/catlog-nightly.service" /etc/systemd/system/catlog-nightly.service
    $SUDO install -m 0644 "$STAGE/systemd/catlog-nightly.timer"   /etc/systemd/system/catlog-nightly.timer
    $SUDO systemctl daemon-reload
fi

say "starting catlogd"
$SUDO systemctl start catlogd

# curl is not guaranteed on a minimal Debian image; wget is the usual fallback.
health() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1
    elif command -v wget >/dev/null 2>&1; then
        wget -q -T 2 -O /dev/null "$HEALTH_URL" >/dev/null 2>&1
    else
        echo "neither curl nor wget is installed — cannot verify $HEALTH_URL" >&2
        return 0
    fi
}

ok=false
for _ in $(seq 1 60); do
    if health; then ok=true; break; fi
    sleep 1
done
if [ "$ok" != true ]; then
    echo "catlogd did not answer $HEALTH_URL within 60 s" >&2
    $SUDO systemctl --no-pager --lines=40 status catlogd >&2 || true
    echo >&2
    echo "rollback:  $SUDO systemctl stop catlogd && $SUDO cp -a $BIN/catlogd.prev $BIN/catlogd && $SUDO systemctl start catlogd" >&2
    exit 1
fi
say "healthy"

# The nginx configuration is deliberately NOT installed: prod.conf.example
# carries <PLACEHOLDER>s and TLS is owner-managed (D1).
if [ -f /etc/nginx/sites-available/catlog ] &&
   ! diff -q "$STAGE/nginx/prod.conf.example" /etc/nginx/sites-available/catlog >/dev/null 2>&1; then
    say "note: /etc/nginx/sites-available/catlog differs from the shipped example —"
    say "      diff $STAGE/nginx/prod.conf.example /etc/nginx/sites-available/catlog"
fi
REMOTE
}

log "install and restart on $HOST"
if $DRY_RUN; then
    printf '    [dry-run] ssh %s %s bash -s <<REMOTE\n' "$SSH_OPTS" "$HOST"
    remote_script | sed 's/^/    | /'
    printf '    [dry-run] REMOTE\n'
else
    # shellcheck disable=SC2086  # SSH_OPTS is a deliberate option list
    remote_script | ssh $SSH_OPTS "$HOST" bash -s
fi

log "done"
if $DRY_RUN; then note "dry run: nothing was changed"; fi
exit 0
