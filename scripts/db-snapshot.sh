#!/usr/bin/env bash
# Snapshot the live catlog databases into a scratch directory you can open from
# an IDE / any stock SQLite client for ad-hoc analysis.
#
# Why this exists: tursogo holds an exclusive whole-file lock for the lifetime of
# the catlogd process, so a second process cannot open events.db or
# projections.db at all — not even read-only (docs/DECISIONS.md, WP1;
# store_test.go TestSecondProcessIsLockedOut). The *file format* is plain SQLite,
# so a copy reads fine in any client; it is only the lock that blocks you.
#
# The lock is advisory, so `cp` itself is never refused. The risk a plain `cp`
# carries is a torn page set if catlogd writes mid-copy. This script narrows that
# window by asking catlogd to quiesce its writer and checkpoint first, via the
# admin API, whenever the admin API is reachable.
#
# Usage:
#   scripts/db-snapshot.sh [dest-dir]
#
#   dest-dir  where to write the snapshot (default: ./data-snapshot)
#
# Env:
#   DATA_DIR   catlogd's [data] dir            (default: ./data)
#   ADMIN_URL  catlogd's admin API base URL    (default: http://127.0.0.1:6060)

set -euo pipefail

DEST="${1:-./data-snapshot}"
DATA_DIR="${DATA_DIR:-./data}"
ADMIN_URL="${ADMIN_URL:-http://127.0.0.1:6060}"

mkdir -p "$DEST"
DEST_ABS="$(cd "$DEST" && pwd)"

# `catlogctl backup` / POST /admin/backup takes the write lock, runs
# PRAGMA wal_checkpoint(TRUNCATE) and copies events.db + its -wal itself. That is
# the only genuinely consistent snapshot available, so prefer it when catlogd is
# up. It covers events.db only.
events_done=0
if curl -fsS --max-time 3 "$ADMIN_URL/admin/healthz" >/dev/null 2>&1; then
  echo "catlogd is up — asking it to quiesce and copy events.db"
  if curl -fsS --max-time 300 -X POST "$ADMIN_URL/admin/backup" \
       -H 'content-type: application/json' \
       -d "{\"dest\":\"$DEST_ABS\"}" >/dev/null; then
    events_done=1
  else
    echo "  admin backup failed; falling back to a plain copy" >&2
  fi
else
  echo "catlogd admin API not reachable at $ADMIN_URL — plain copy of whatever is on disk"
fi

# projections.db has no admin backup verb (it is rebuildable by design, D8), and
# events.db lands here too if the admin path was unavailable. Copy the -wal
# alongside every main file: tursogo never auto-checkpoints, so the main file on
# its own can be nearly empty and a WAL-less copy would silently read as stale.
for db in events projections; do
  [ "$db" = events ] && [ "$events_done" = 1 ] && continue
  src="$DATA_DIR/$db.db"
  [ -f "$src" ] || { echo "  skip $db.db (not present)"; continue; }
  cp "$src" "$DEST/$db.db"
  [ -f "$src-wal" ] && cp "$src-wal" "$DEST/$db.db-wal"
  [ -f "$src-shm" ] && cp "$src-shm" "$DEST/$db.db-shm"
  echo "  copied $db.db"
done

echo
echo "snapshot in $DEST_ABS:"
for f in "$DEST"/*.db; do
  [ -f "$f" ] || continue
  # integrity_check also forces the -wal to be replayed, so this is a real
  # readability test and not just a stat().
  if out="$(sqlite3 "$f" 'PRAGMA integrity_check;' 2>&1)" && [ "$out" = ok ]; then
    echo "  $(basename "$f")  $(wc -c <"$f" | tr -d ' ') bytes  integrity=ok"
  else
    echo "  $(basename "$f")  INTEGRITY CHECK FAILED: $out" >&2
  fi
done
echo
echo "point your IDE at: $DEST_ABS/events.db"
