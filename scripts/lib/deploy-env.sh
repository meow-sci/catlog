# Shared setup for the scripts that talk to the VM. Sourced, never executed.
#
#   . "$(dirname "$0")/lib/deploy-env.sh"
#
# Provides:
#   ROOT              the repository root
#   DEPLOY_ENV        path to infra/deploy.env, verified to exist
#   SSH_KEY           the private key, verified to exist and be private
#   KNOWN_HOSTS_FILE  a known_hosts built from the pinned CATLOG_SSH_HOST_KEY
#   SSH_CONFIG_FILE   a generated ssh config with one Host entry for the VM
#   SSH_OPTS          the options to reach the VM from THIS machine
#   catlog_ssh …      run a command on the VM
#
# ---------------------------------------------------------------------------
# WHY NOTHING HERE READS ~/.ssh/config
# ---------------------------------------------------------------------------
# An earlier version mounted the whole of ~/.ssh into the Ansible container and
# let your own config apply. That works right up until the config names an
# `IdentityFile` by absolute path — which is the normal way to write one — and
# the container, whose HOME is somewhere else entirely, cannot find it.
#
# So the connection is described explicitly instead: one key, named in
# infra/deploy.env, and a generated config with exactly one Host entry. The same
# file is used by ssh here and by ssh inside the container, so the two cannot
# drift, and nothing depends on how your personal ssh config happens to be
# arranged today.

# shellcheck shell=bash

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

# CATLOG_DEPLOY_ENV points these scripts at a different settings file.
#
# It exists so that testing them never involves writing to the real one.
# infra/deploy.env holds the Cloudflare token, the IdP secrets and the VM's
# identity; it is gitignored, so there is no copy to restore from, and anything
# that overwrites it costs somebody an afternoon of re-typing. Exercising the
# scripts against a throwaway file is the supported way:
#
#   CATLOG_DEPLOY_ENV=/tmp/probe.env scripts/ansible.sh --syntax-check …
DEPLOY_ENV="${CATLOG_DEPLOY_ENV:-$ROOT/infra/deploy.env}"

if [ ! -f "$DEPLOY_ENV" ]; then
    echo "no $DEPLOY_ENV — run 'make deploy-env' and fill it in" >&2
    exit 1
fi

set -a
# shellcheck disable=SC1090
. "$DEPLOY_ENV"
set +a

: "${CATLOG_SSH_HOST:?CATLOG_SSH_HOST is unset — see infra/deploy.env}"
CATLOG_SSH_USER="${CATLOG_SSH_USER:-root}"
CATLOG_SSH_PORT="${CATLOG_SSH_PORT:-22}"

# --- the key ----------------------------------------------------------------

: "${CATLOG_SSH_IDENTITY_FILE:?CATLOG_SSH_IDENTITY_FILE is unset — see infra/deploy.env}"
# `~` is not expanded inside a quoted value, and deploy.env is read by `make`,
# by this script and by `docker --env-file`, none of which agree about it.
SSH_KEY="${CATLOG_SSH_IDENTITY_FILE/#\~/$HOME}"

if [ ! -f "$SSH_KEY" ]; then
    echo "CATLOG_SSH_IDENTITY_FILE=$CATLOG_SSH_IDENTITY_FILE does not exist" >&2
    exit 1
fi

# ssh refuses a private key that is group- or world-readable, and its message
# ("UNPROTECTED PRIVATE KEY FILE") arrives after a wall of asterisks in the
# middle of a play. Say it here, where it is one line and one fix.
if [ -n "$(find "$SSH_KEY" -perm +077 2>/dev/null || find "$SSH_KEY" -perm /077 2>/dev/null)" ]; then
    echo "$SSH_KEY is readable by others — ssh will refuse it. Run: chmod 600 '$SSH_KEY'" >&2
    exit 1
fi

# The public half, if it is beside the private one. ssh reads it to offer the
# public key without touching the private one, which matters when the private
# key is held by an agent or is passphrase-protected.
SSH_KEY_PUB=""
[ -f "$SSH_KEY.pub" ] && SSH_KEY_PUB="$SSH_KEY.pub"

# --- the host key -----------------------------------------------------------
#
# Pinned rather than accepted on sight, so there is no trust-on-first-use step
# anywhere. `[host]:port` is the form OpenSSH uses for anything but port 22, and
# it is not interchangeable with the bare one.
KNOWN_HOSTS_FILE="${TMPDIR:-/tmp}/catlog-ssh-known-hosts"
if [ -n "${CATLOG_SSH_HOST_KEY:-}" ]; then
    if [ "$CATLOG_SSH_PORT" != "22" ]; then
        printf '[%s]:%s %s\n' "$CATLOG_SSH_HOST" "$CATLOG_SSH_PORT" "$CATLOG_SSH_HOST_KEY" > "$KNOWN_HOSTS_FILE"
    else
        printf '%s %s\n' "$CATLOG_SSH_HOST" "$CATLOG_SSH_HOST_KEY" > "$KNOWN_HOSTS_FILE"
    fi
    chmod 600 "$KNOWN_HOSTS_FILE"
    HOST_KEY_PINNED=true
else
    : > "$KNOWN_HOSTS_FILE"
    chmod 600 "$KNOWN_HOSTS_FILE"
    HOST_KEY_PINNED=false
fi

# --- the ssh config ---------------------------------------------------------
#
# Written twice, for the two places ssh runs: once with this machine's paths,
# once with the paths the container will see. Generating both from the same
# values is what stops them drifting.
#
# `IdentitiesOnly yes` is load-bearing: without it ssh offers every key the
# agent holds before the one we named, and a box with `MaxAuthTries` set will
# disconnect before reaching it.
_write_ssh_config() {
    local dest=$1 key=$2 known=$3
    # BOTH names on the Host line, and that is not belt-and-braces.
    #
    # `ssh catlog` matches the alias, but Ansible connects to `ansible_host` —
    # the address — so a block matching only the alias applies to none of the
    # real traffic. ssh silently falls back to ~/.ssh/id_* and the default
    # known_hosts, and the failure is an authentication error that names no
    # cause. Verified with `ssh -G <address>`, which prints what actually
    # resolves rather than what was intended.
    cat > "$dest" <<CONFIG
# Generated by scripts/lib/deploy-env.sh. Do not edit; edit infra/deploy.env.
Host catlog $CATLOG_SSH_HOST
    HostName $CATLOG_SSH_HOST
    User $CATLOG_SSH_USER
    Port $CATLOG_SSH_PORT
    IdentityFile $key
    IdentitiesOnly yes
    UserKnownHostsFile $known
    StrictHostKeyChecking yes
    ServerAliveInterval 30
CONFIG
    chmod 600 "$dest"
}

SSH_CONFIG_FILE="${TMPDIR:-/tmp}/catlog-ssh-config"
_write_ssh_config "$SSH_CONFIG_FILE" "$SSH_KEY" "$KNOWN_HOSTS_FILE"

# The container sees the key and the known_hosts at fixed paths, so its config
# has to name those rather than this machine's.
CONTAINER_SSH_CONFIG="${TMPDIR:-/tmp}/catlog-ssh-config.container"
_write_ssh_config "$CONTAINER_SSH_CONFIG" /etc/catlog/id /etc/catlog/known_hosts

SSH_OPTS="-F $SSH_CONFIG_FILE"

catlog_ssh() {
    # `catlog` is the Host entry above, so the user, port, key and host key all
    # come from one place.
    # shellcheck disable=SC2086
    ssh $SSH_OPTS catlog "$@"
}
