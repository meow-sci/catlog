#!/usr/bin/env bash
# Send the built images to the VM over ssh. No registry, no files.
#
#   scripts/ship-images.sh <catlogd-ref> <nginx-ref> <version>
#
# `docker save … | ssh … docker load` — one pipe, streamed. Nothing is written
# to disk on either end: no tarball here, no staging file there, nothing to
# clean up if it is interrupted, and no copy of the image left lying around on
# a laptop or in /tmp on a public-facing box.
#
# ---------------------------------------------------------------------------
# WHY THERE IS NO REGISTRY
# ---------------------------------------------------------------------------
# One VM, one operator, two images totalling ~85 MB. A registry would add an
# account, a credential on the VM that can be stolen, a storage quota, and a
# service that has to be up at deploy time — to solve a distribution problem
# that is one ssh pipe.
#
# What it costs, and this is the real trade: there is no off-box copy of what is
# deployed. Lose the droplet and the images are gone with it; they are rebuilt
# from the git tag by `make release`, which is the recovery path and is why the
# version is stamped into the binary. Rollback still works without a network,
# because the previous image is still on the VM.
#
# ---------------------------------------------------------------------------
# WHY IT DOES NOT COMPRESS
# ---------------------------------------------------------------------------
# Measured, not assumed: `docker save` output is already compressed — the layers
# come out of the content store as-is — and piping it through gzip changed
# 20.7 MB into 20.6 MB. `ssh -C` would spend CPU on both ends for the same
# nothing.

set -euo pipefail

# shellcheck source=lib/deploy-env.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib/deploy-env.sh"

CATLOGD_REF=${1:?usage: ship-images.sh <catlogd-ref> <nginx-ref> <version>}
NGINX_REF=${2:?usage: ship-images.sh <catlogd-ref> <nginx-ref> <version>}
VERSION=${3:?usage: ship-images.sh <catlogd-ref> <nginx-ref> <version>}
RELEASE_FILE="$ROOT/infra/.release.env"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }

# Docker must be reachable before anything else is attempted.
docker info >/dev/null 2>&1 || { echo "the docker daemon is unreachable" >&2; exit 1; }

ship() {
    local ref=$1 local_id remote_id size

    local_id=$(docker image inspect "$ref" --format '{{.Id}}' 2>/dev/null || true)
    if [ -z "$local_id" ]; then
        echo "ship-images: $ref is not in the local daemon — run 'make images' first" >&2
        exit 1
    fi

    # Ask first. A redeploy that changed only catlogd should not re-send 64 MB
    # of nginx, and an unchanged deploy should send nothing at all. The image ID
    # is the config digest: it changes if and only if the image does.
    remote_id=$(catlog_ssh "docker image inspect --format '{{.Id}}' '$ref' 2>/dev/null || true" | tr -d '\r')
    if [ "$remote_id" = "$local_id" ]; then
        note "$ref — already on the VM, skipped"
        return 0
    fi

    size=$(docker image inspect "$ref" --format '{{.Size}}')
    note "$ref — sending $(( size / 1000000 )) MB"
    docker save "$ref" | catlog_ssh 'docker load'
}

say "shipping to $CATLOG_SSH_USER@$CATLOG_SSH_HOST"
ship "$CATLOGD_REF"
ship "$NGINX_REF"

# Verify rather than assume. `docker load` is quiet about a truncated stream in
# some versions, and a half-loaded image would fail at `compose up` with an
# error about the image being missing rather than about the transfer.
say "verifying"
for ref in "$CATLOGD_REF" "$NGINX_REF"; do
    lid=$(docker image inspect "$ref" --format '{{.Id}}')
    rid=$(catlog_ssh "docker image inspect --format '{{.Id}}' '$ref'" | tr -d '\r')
    if [ "$lid" != "$rid" ]; then
        echo "ship-images: $ref did not arrive intact (local $lid, remote $rid)" >&2
        exit 1
    fi
    note "$ref  $lid"
done

# The image ID, not a registry digest: there is no registry to assign one, and
# the ID is the same kind of immutable content hash. `make deploy` pins the tag,
# and the VM records both — the tag is what compose reads, the ID is what proves
# the tag still means what it meant.
cat > "$RELEASE_FILE" <<RELEASE
# Written by scripts/ship-images.sh. Sourced by 'make deploy'. Gitignored.
CATLOG_RELEASE_VERSION=$VERSION
CATLOG_RELEASE_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CATLOG_IMAGE=$CATLOGD_REF
NGINX_IMAGE=$NGINX_REF
CATLOG_IMAGE_ID=$(docker image inspect "$CATLOGD_REF" --format '{{.Id}}')
NGINX_IMAGE_ID=$(docker image inspect "$NGINX_REF" --format '{{.Id}}')
RELEASE

say "released $VERSION"
note "recorded in $(basename "$RELEASE_FILE") — next: make deploy"
