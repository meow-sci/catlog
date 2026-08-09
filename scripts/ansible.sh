#!/usr/bin/env bash
# Run an Ansible command inside a container, so nothing but Docker and ssh has
# to exist on your machine.
#
#   scripts/ansible.sh playbooks/site.yml
#   scripts/ansible.sh playbooks/ops.yml --tags status
#   scripts/ansible.sh --syntax-check playbooks/deploy.yml
#
# `ansible-playbook` is assumed; pass ANSIBLE_ENTRY=ansible-inventory (or any
# other tool from the image) to run something else.
#
# The image is pinned and ships everything the playbooks import —
# community.docker, community.general and ansible.posix are all bundled, which
# is why there is no galaxy install step and no collections directory to keep.
#
# ---------------------------------------------------------------------------
# WHAT GETS MOUNTED, AND WHY EACH ONE IS NEEDED
# ---------------------------------------------------------------------------
#   infra/           → /work/infra          the playbooks, and the files they
#                                           template from. roles/catlog_app
#                                           copies ../../compose.prod.yaml and
#                                           roles/catlog_nginx renders
#                                           ../../nginx/*.j2, so the mount has
#                                           to be infra/, not infra/ansible/.
#   diagnostics/     → /work/diagnostics    where ops-logs fetches to; the only
#                                           path the container writes.
#   the ssh key      → /etc/catlog/id       read-only, plus id.pub if present
#   generated config → /etc/catlog/ssh_config, /etc/catlog/known_hosts
#   passwd + group   → /etc/passwd, /etc/group   see below
#
# YOUR ~/.ssh IS NOT MOUNTED, and that is a fix rather than an omission. An
# earlier version mounted all of it and let your own ~/.ssh/config apply — which
# works until that config names an IdentityFile by absolute path, the normal way
# to write one. The container's HOME is somewhere else entirely, so the path is
# not there and every connection fails.
#
# The connection is described explicitly instead: one key named in
# infra/deploy.env, and a generated config with exactly one Host entry, written
# by lib/deploy-env.sh from the same values the host-side ssh uses.
#
# The rest of the repository is deliberately not mounted either. data/ holds the
# signing key, the session key and the pepper, and no playbook has any business
# being able to see them.
#
set -euo pipefail

# shellcheck source=lib/deploy-env.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib/deploy-env.sh"

IMAGE="${ANSIBLE_IMAGE:-alpine/ansible:2.21.0}"
ENTRY="${ANSIBLE_ENTRY:-ansible-playbook}"

# The container runs as you, so anything it writes into diagnostics/ belongs to
# you rather than to root. HOME has to move with it: the image's default is
# /root, which your uid cannot read.
#
# HOME IS A TMPFS OWNED BY YOUR UID, and that one line is what makes the rest
# work. Bind-mounting ~/.ssh into $HOME makes Docker create the parent directory
# owned by ROOT, and a non-root process then cannot create anything else in it —
# which breaks, one at a time and with unrelated-looking errors, every Ansible
# path that defaults to ~/.ansible:
#
#   ~/.ansible/tmp   local staging       "Unable to create local directories"
#   ~/.ansible/cp    ssh ControlPath     the same, at connection time
#   ~/.ansible/tmp   remote staging, for a `delegate_to: localhost` task where
#                    the "target" is this container — the failure that would
#                    have taken out roles/catlog_nginx's Cloudflare fetch
#                    halfway through a provision run
#
# Setting each of those to somewhere writable works and is whack-a-mole; the
# next default to be added would break too. A tmpfs with uid= fixes the class:
# the parent is ours, so every ~/.ansible path just works.
CONTAINER_HOME=/tmp/ansible-home
mkdir -p "$ROOT/diagnostics"

# ---------------------------------------------------------------------------
# A passwd entry for the uid we run as
# ---------------------------------------------------------------------------
# Not optional, and not cosmetic. `--user 501:20` gives the container a uid that
# does not appear in its /etc/passwd, and OpenSSH refuses to start at all in
# that state: "No user exists for uid 501", exit 255, no connection attempted.
# Every playbook would fail at the first task with an error that says nothing
# about the real cause.
#
# So both files are synthesised for this run. They keep root, because plenty of
# tooling assumes uid 0 resolves, and add an entry for you.
PASSWD_FILE="${TMPDIR:-/tmp}/catlog-ansible-passwd"
GROUP_FILE="${TMPDIR:-/tmp}/catlog-ansible-group"
{
    echo "root:x:0:0:root:/root:/bin/sh"
    echo "ansible:x:$(id -u):$(id -g):ansible:${CONTAINER_HOME}:/bin/sh"
} > "$PASSWD_FILE"
{
    echo "root:x:0:"
    echo "ansible:x:$(id -g):"
} > "$GROUP_FILE"

# ANSIBLE_SSH_ARGS REPLACES ansible.cfg's ssh_args rather than adding to it. `-F`
# points ssh at the generated config, so the user, port, key and pinned host key
# all come from the one place lib/deploy-env.sh wrote them — the same values the
# host-side ssh in ship-images.sh uses.
SSH_ARGS="-F /etc/catlog/ssh_config -o ControlMaster=auto -o ControlPersist=120s"

# An interactive TTY when there is one — playbooks/restore.yml uses vars_prompt,
# and `docker run` without -t turns that prompt into a hang with no output.
#
# The `${arr[@]+"${arr[@]}"}` form below is not decoration: macOS ships bash 3.2,
# where expanding an EMPTY array as "${arr[@]}" under `set -u` is an unbound
# variable error. This script has to run on the machine it is written for.
TTY_FLAGS=()
[ -t 0 ] && TTY_FLAGS=(-it)

# SSH_AUTH_SOCK forwarding, when Docker Desktop offers it. Optional: a key file
# under ~/.ssh works without it, and this is only nicer when your key has a
# passphrase held by the agent.
PUB_FLAGS=()
[ -n "$SSH_KEY_PUB" ] && PUB_FLAGS=(-v "$SSH_KEY_PUB:/etc/catlog/id.pub:ro")

AGENT_FLAGS=()
if [ -n "${SSH_AUTH_SOCK:-}" ] && [ -S "/run/host-services/ssh-auth.sock" ]; then
    AGENT_FLAGS=(-v /run/host-services/ssh-auth.sock:/tmp/ssh-agent.sock
                 -e SSH_AUTH_SOCK=/tmp/ssh-agent.sock)
fi

exec docker run --rm ${TTY_FLAGS[@]+"${TTY_FLAGS[@]}"} \
    --user "$(id -u):$(id -g)" \
    -e HOME="$CONTAINER_HOME" \
    --tmpfs "$CONTAINER_HOME:uid=$(id -u),gid=$(id -g),mode=0755" \
    -e ANSIBLE_SSH_ARGS="$SSH_ARGS" \
    -e ANSIBLE_HOST_KEY_CHECKING=True \
    --env-file "$DEPLOY_ENV" \
    ${CATLOG_IMAGE:+-e CATLOG_IMAGE="$CATLOG_IMAGE"} \
    ${NGINX_IMAGE:+-e NGINX_IMAGE="$NGINX_IMAGE"} \
    ${CATLOG_IMAGE_ID:+-e CATLOG_IMAGE_ID="$CATLOG_IMAGE_ID"} \
    ${NGINX_IMAGE_ID:+-e NGINX_IMAGE_ID="$NGINX_IMAGE_ID"} \
    ${SINCE:+-e SINCE="$SINCE"} \
    -v "$ROOT/infra:/work/infra" \
    -v "$ROOT/diagnostics:/work/diagnostics" \
    -v "$SSH_KEY:/etc/catlog/id:ro" \
    -v "$CONTAINER_SSH_CONFIG:/etc/catlog/ssh_config:ro" \
    -v "$KNOWN_HOSTS_FILE:/etc/catlog/known_hosts:ro" \
    -v "$PASSWD_FILE:/etc/passwd:ro" \
    -v "$GROUP_FILE:/etc/group:ro" \
    ${PUB_FLAGS[@]+"${PUB_FLAGS[@]}"} \
    ${AGENT_FLAGS[@]+"${AGENT_FLAGS[@]}"} \
    -w /work/infra/ansible \
    "$IMAGE" \
    "$ENTRY" "$@"
