#!/usr/bin/env bash
# The gate between `make images` and a push. See docs/operations.md.
#
#   scripts/container-smoke.sh <catlogd-image> <nginx-image>
#
# Brings the real production compose project up on a throwaway data directory
# and asserts, in order, the things that would otherwise be discovered on the
# VM. The first one is the whole reason this script exists:
#
#   catlogd becoming healthy PROVES the hardened base can run it — the glibc
#   interpreter resolved, the 19 MB Turso shared object was extracted and
#   dlopen()d, the key set was created and both databases were migrated. None of
#   that is provable by building the image, and all of it is a property of the
#   base image choice.
#
# Everything is torn down on exit, including on failure.

set -euo pipefail

CATLOGD_IMAGE=${1:?usage: container-smoke.sh <catlogd-image> <nginx-image>}
NGINX_IMAGE=${2:?usage: container-smoke.sh <catlogd-image> <nginx-image>}

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/catlog-smoke.XXXXXX")"
PROJECT="catlog-smoke-$$"
PASS=0
FAIL=0
OVERRIDE=""

cleanup() {
    docker compose -p "$PROJECT" -f "$WORK/compose.yaml" \
        ${OVERRIDE:+-f "$OVERRIDE"} --env-file "$WORK/.env" \
        down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()   { PASS=$((PASS + 1)); printf '    \033[32mok\033[0m   %s\n' "$*"; }
bad()  { FAIL=$((FAIL + 1)); printf '    \033[31mFAIL\033[0m %s\n' "$*"; }
# `set +o pipefail` inside the subshell is load-bearing. Several checks below
# are `curl … | grep -q …`, and grep -q exits the moment it matches — which
# closes the pipe, hands curl a SIGPIPE, and under pipefail turns a PASSING
# assertion into a failure. It only bites on bodies large enough that curl is
# still writing, so it presents as a flake that appears once the demo dataset is
# seeded and not before.
check() { if ( set +o pipefail; eval "$2" ) >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }

# --- a throwaway data root, laid out exactly as roles/storage lays out the real one
say "staging a throwaway deployment in $WORK"

# The uid the hardened image runs as. Read from the image rather than assumed:
# a mismatch between this and the directory owner is the single most common
# first-boot failure, and the smoke test should catch it here.
IMAGE_USER=$(docker image inspect "$CATLOGD_IMAGE" --format '{{.Config.User}}')
UID_GID=${IMAGE_USER:-65532:65532}
case "$UID_GID" in
    *:*) SMOKE_UID=${UID_GID%%:*}; SMOKE_GID=${UID_GID##*:} ;;
    nonroot|"") SMOKE_UID=65532; SMOKE_GID=65532 ;;
    *) SMOKE_UID=$UID_GID; SMOKE_GID=$UID_GID ;;
esac
printf '    image runs as %s -> using %s:%s\n' "${IMAGE_USER:-<unset>}" "$SMOKE_UID" "$SMOKE_GID"

mkdir -p "$WORK"/{config,data,turso-cache,backups,nginx/conf,acme/live,acme/data}
# Docker Desktop on macOS maps ownership through its VM, so a chown here is
# advisory. On Linux it is what makes the bind mounts writable at all.
chmod -R 0777 "$WORK/data" "$WORK/turso-cache" "$WORK/backups" 2>/dev/null || true

cp "$ROOT/infra/compose.prod.yaml" "$WORK/compose.yaml"

cat > "$WORK/.env" <<ENVEOF
CATLOG_IMAGE=$CATLOGD_IMAGE
NGINX_IMAGE=$NGINX_IMAGE
CATLOG_UID=$SMOKE_UID
CATLOG_GID=$SMOKE_GID
CATLOG_DATA_ROOT=$WORK
CATLOG_GOMEMLIMIT=256MiB
CATLOG_MEM_LIMIT=512m
CF_API_TOKEN=unused-in-smoke
ENVEOF

# A minimal catlogd.toml. Deliberately NOT the production template: this must
# run with no domain, no certificate and no identity provider, and the point is
# to exercise the image rather than the configuration.
cat > "$WORK/config/catlogd.toml" <<'TOMLEOF'
[server]
listen       = "0.0.0.0:8080"
admin_listen = "127.0.0.1:6060"
base_url     = "http://127.0.0.1:8080"
static_dir   = ""
[data]
dir = "/var/lib/catlog/data"
[ingest]
accepted_htu = ["http://127.0.0.1:8080/v1/ingest"]
TOMLEOF
: > "$WORK/config/catlogd.env"

# A plaintext server block on 8080. The production one needs certificates; what
# is being proved here is the proxying, the pre-compressed assets and the SPA
# routing, none of which is about TLS.
cat > "$WORK/nginx/conf/10-catlog.conf" <<'NGINXEOF'
server {
    listen 8080;
    server_name _;

    location = /v1/ingest {
        proxy_pass http://$catlogd_upstream;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        brotli off; gzip off;
    }
    location ~ ^/v1/(feed/(sse|stream)|events/(sse|stream))$ {
        proxy_pass http://$catlogd_upstream;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_buffering off;
        brotli off; gzip off;
    }
    location /static/ {
        alias /usr/share/nginx/catlog/site/;
        add_header Vary Accept-Encoding;
    }
    location /app/assets/ {
        alias /usr/share/nginx/catlog/spa/assets/;
        add_header Vary Accept-Encoding;
        add_header Cache-Control "public, max-age=31536000, immutable";
    }
    location /app/ {
        alias /usr/share/nginx/catlog/spa/;
        try_files $uri $uri/ /app/index.html;
        add_header Vary Accept-Encoding;
    }
    location /admin/ { return 403; }
    location / {
        proxy_pass http://$catlogd_upstream;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
    }
}
NGINXEOF
cat > "$WORK/nginx/conf/20-realip.conf" <<'REALIPEOF'
map $host $catlog_block_direct { default 0; }
REALIPEOF

# nginx publishes 80/443 in the production file; remap so a smoke run never
# collides with anything already listening on this machine, and so the run needs
# no privileged port.
OVERRIDE="$WORK/compose.override.yaml"
cat > "$OVERRIDE" <<'OVERRIDEEOF'
services:
  nginx:
    ports: !override
      - "127.0.0.1:18080:8080"
OVERRIDEEOF
dc() {
    docker compose -p "$PROJECT" -f "$WORK/compose.yaml" -f "$OVERRIDE" \
        --env-file "$WORK/.env" "$@"
}

BASE=http://127.0.0.1:18080

# --- 1. the one that proves the base image works ---------------------------
say "1. catlogd starts on the hardened base"
dc up -d catlogd >/dev/null

healthy=false
for _ in $(seq 1 60); do
    state=$(docker inspect --format '{{.State.Health.Status}}' \
        "$(dc ps -q catlogd)" 2>/dev/null || echo starting)
    [ "$state" = healthy ] && { healthy=true; break; }
    [ "$state" = unhealthy ] && break
    sleep 2
done

if $healthy; then
    ok "catlogd is healthy — glibc loader, dlopen'd Turso engine, keys, migrations"
else
    bad "catlogd never became healthy"
    echo
    dc logs --tail 60 catlogd
    echo
    echo "If this says anything about a shared object, the runtime base is wrong:"
    echo "  the binary is dynamically linked and the driver dlopen()s an extracted .so,"
    echo "  so the base needs glibc and TURSO_GO_CACHE_DIR must be writable AND exec-capable."
    exit 1
fi

# --- 2. it wrote what it should have, as whom it should have ---------------
say "2. first boot created the key set"
check "data/keys exists"          "test -d '$WORK/data/keys'"
check "events.db exists"          "test -f '$WORK/data/events.db'"
check "projections.db exists"     "test -f '$WORK/data/projections.db'"
check "the Turso .so was extracted and cached" \
      "find '$WORK/turso-cache' -name 'libturso_sync_sdk_kit.so' | grep -q ."

# --- 3. the admin mux is reachable only from inside the namespace ----------
say "3. the admin mux"
check "catlogctl reaches 127.0.0.1:6060 through 'compose exec'" \
      "dc exec -T catlogd catlogctl seed"
check "the admin port is not published" \
      "! dc port catlogd 6060"

# --- 4. the proxy, the assets and the SPA ---------------------------------
say "4. nginx"
dc up -d nginx >/dev/null
for _ in $(seq 1 30); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break; sleep 1; done

check "/healthz answers through the proxy"      "curl -fsS '$BASE/healthz' | grep -q '\"ok\":true'"
check "the datastar home page renders"          "curl -fsS '$BASE/' | grep -qi '<html'"
check "/admin/ is refused"                      "test \"\$(curl -s -o /dev/null -w '%{http_code}' '$BASE/admin/stats')\" = 403"
check "/app/ serves the reader"                 "curl -fsS '$BASE/app/' | grep -qi '<html'"
check "an /app/ deep link serves the reader"    "curl -fsS '$BASE/app/boards/rud_total' | grep -qi '<html'"

# --- 5. pre-compressed assets ---------------------------------------------
# The assertion the whole of §5 exists for. `--compressed` would decode
# transparently and prove nothing, so the encoding header is read directly.
say "5. pre-compressed static assets"
enc() { curl -s -o /dev/null -D - -H "Accept-Encoding: $1" "$BASE/static/css/catlog.css" \
        | tr -d '\r' | awk 'tolower($1)=="content-encoding:"{print $2}'; }

[ "$(enc 'gzip, br')" = br ]   && ok "Accept-Encoding: gzip, br  -> br (brotli_static wins over gzip_static)" \
                               || bad "Accept-Encoding: gzip, br -> '$(enc 'gzip, br')', expected br"
[ "$(enc 'gzip')" = gzip ]     && ok "Accept-Encoding: gzip      -> gzip (gzip_static)" \
                               || bad "Accept-Encoding: gzip -> '$(enc gzip)', expected gzip"
[ -z "$(enc 'identity')" ]     && ok "Accept-Encoding: identity  -> uncompressed original is still shipped" \
                               || bad "Accept-Encoding: identity -> '$(enc identity)', expected none"

check "the static response carries Vary: Accept-Encoding" \
      "curl -s -D - -o /dev/null '$BASE/static/css/catlog.css' | tr -d '\r' | grep -qi '^vary:.*accept-encoding'"
# Compares what came off the wire against the .br file inside the image, rather
# than decoding it locally: macOS ships curl without brotli, so `--compressed`
# would silently return an empty body and the check would be testing curl.
# Byte-identical transmission of the exact pre-compressed artefact is also the
# stronger claim — it proves nginx served the file rather than re-compressing.
served_br=$(curl -s -H 'Accept-Encoding: br' "$BASE/static/css/catlog.css" | shasum -a 256 | cut -d' ' -f1)
in_image=$(dc exec -T nginx sha256sum /usr/share/nginx/catlog/site/css/catlog.css.br | cut -d' ' -f1)
[ -n "$served_br" ] && [ "$served_br" = "$in_image" ] \
    && ok "the br response is byte-identical to the .br file baked into the image" \
    || bad "br response ($served_br) != the image's .br file ($in_image)"

served_id=$(curl -s -H 'Accept-Encoding: identity' "$BASE/static/css/catlog.css" | shasum -a 256 | cut -d' ' -f1)
origin_id=$(dc exec -T nginx sha256sum /usr/share/nginx/catlog/site/css/catlog.css | cut -d' ' -f1)
[ -n "$served_id" ] && [ "$served_id" = "$origin_id" ] \
    && ok "the identity response is byte-identical to the original" \
    || bad "identity response ($served_id) != the original ($origin_id)"
check "hashed SPA assets are immutable" \
      "curl -s -D - -o /dev/null \"$BASE/app/\$(curl -s '$BASE/app/' | grep -o 'assets/[^\\\"]*\\.js' | head -1)\" \
       | grep -qi 'immutable'"

# --- 6. proxied responses are compressed on the fly -----------------------
say "6. dynamic compression of proxied responses"
check "the datastar home page comes back brotli-compressed" \
      "curl -s -D - -o /dev/null -H 'Accept-Encoding: br' '$BASE/' | tr -d '\r' | grep -qi '^content-encoding: br'"
check "the SSE feed is NOT compressed" \
      "! curl -s -D - -o /dev/null --max-time 3 -H 'Accept-Encoding: gzip, br' '$BASE/v1/feed/sse' \
         | tr -d '\r' | grep -qi '^content-encoding:'"

# --- 7. shutdown ----------------------------------------------------------
say "7. clean shutdown"
dc stop -t 90 catlogd >/dev/null
check "the WAL was checkpointed away on shutdown" \
      "test ! -s '$WORK/data/events.db-wal'"

say "8. the same data directory reopens"
dc up -d catlogd >/dev/null
reopened=false
for _ in $(seq 1 45); do
    [ "$(docker inspect --format '{{.State.Health.Status}}' "$(dc ps -q catlogd)" 2>/dev/null)" = healthy ] \
        && { reopened=true; break; }
    sleep 2
done
$reopened && ok "the lock was released and the databases reopened cleanly" \
           || bad "catlogd could not reopen its own data directory"

# --- report ---------------------------------------------------------------
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
