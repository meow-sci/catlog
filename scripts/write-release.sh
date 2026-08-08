#!/usr/bin/env bash
# Record what `make images-push` just pushed, BY DIGEST.
#
#   scripts/write-release.sh infra/.release.env <catlogd-ref> <nginx-ref> <version>
#
# A tag can be moved; a digest cannot. `make deploy` sources this file and hands
# the digests to Ansible, and rollback depends on being able to name the exact
# bytes that were running before — so the digest is read back out of the local
# image after the push rather than assumed.
#
# Written as KEY=value rather than JSON so that `set -a; . infra/.release.env`
# is the whole of reading it, with no jq on anybody's machine.

set -euo pipefail

out=$1 catlogd_ref=$2 nginx_ref=$3 version=$4

digest_of() {
    local ref=$1 digest
    digest=$(docker image inspect "$ref" --format '{{index .RepoDigests 0}}' 2>/dev/null || true)
    if [ -z "$digest" ]; then
        echo "write-release: $ref has no repo digest — was it pushed?" >&2
        exit 1
    fi
    printf '%s\n' "$digest"
}

catlogd_digest=$(digest_of "$catlogd_ref")
nginx_digest=$(digest_of "$nginx_ref")

cat > "$out" <<RELEASE
# Written by scripts/write-release.sh. Sourced by 'make deploy'. Gitignored.
CATLOG_RELEASE_VERSION=$version
CATLOG_RELEASE_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CATLOG_IMAGE=$catlogd_digest
NGINX_IMAGE=$nginx_digest
RELEASE

echo
echo "released $version"
echo "  catlogd  $catlogd_digest"
echo "  nginx    $nginx_digest"
echo "  recorded in $out"
