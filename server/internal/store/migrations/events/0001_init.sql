-- events.db initial schema — INITIAL_IMPL_PLAN.md §5.4, verbatim.
--
-- Turso discipline applied throughout (§5.4): no STRICT, no expression indexes
-- (handle_lc is materialized instead), UTF-8 only. WITHOUT ROWID and VACUUM
-- are avoided by policy (§5.4), not capability — tursogo supports both behind
-- a DSN experimental flag we do not enable (docs/DECISIONS.md, WP1). FOREIGN
-- KEY clauses are documentation — PRAGMA foreign_keys is never enabled, so
-- nothing here is enforced by the engine.
--
-- ULIDs are stored as 16-byte BLOBs (D19); timestamps are unix milliseconds
-- stored as INTEGER; payloads are JSON text.

-- A player is one IdP account. user_key = HMAC-SHA256(pepper, "<idp>:<sub>")
-- (D17) — the only account identifier that exists; no email is ever stored
-- because none is ever requested (§4.7).
CREATE TABLE player (
  player_id  INTEGER PRIMARY KEY,
  user_key   BLOB NOT NULL UNIQUE,          -- 32 B
  idp        TEXT NOT NULL,                 -- 'discord'|'google'|'github'
  created_at INTEGER NOT NULL,              -- unix ms
  banned_at  INTEGER, ban_reason TEXT
);

-- Handles are globally unique case-insensitively, first claim wins, immutable,
-- and never recycled (D9). handle keeps the display casing; handle_lc carries
-- the uniqueness constraint.
CREATE TABLE handle (
  handle     TEXT PRIMARY KEY,
  handle_lc  TEXT NOT NULL UNIQUE,
  player_id  INTEGER NOT NULL REFERENCES player(player_id),
  created_at INTEGER NOT NULL
);

-- Retirement is permanent: a banned or deleted account's handle moves here and
-- can never be claimed again, which blocks impersonation of a prior owner (D9).
CREATE TABLE retired_handle (handle_lc TEXT PRIMARY KEY, reason TEXT NOT NULL, retired_at INTEGER NOT NULL);

-- One row per issued license: the client key's RFC 7638 thumbprint is the
-- primary key, so §4.5.3 step 5 is a point lookup.
CREATE TABLE credential (
  jkt        TEXT PRIMARY KEY,              -- b64u thumbprint
  player_id  INTEGER NOT NULL, handle TEXT NOT NULL,
  license_jti TEXT NOT NULL,
  issued_at  INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER
);
CREATE INDEX cred_player ON credential(player_id);

-- The raw event log. seq is the rowid, so it is the server-local total order
-- and doubles as the projector cursor and the archive cursor.
CREATE TABLE event (
  seq       INTEGER PRIMARY KEY,            -- rowid: server-local total order = projector cursor
  event_id  BLOB NOT NULL,                  -- 16 B ULID
  player_id INTEGER NOT NULL,
  flight_id BLOB, session_id BLOB,          -- 16 B ULIDs, flight nullable
  type      TEXT NOT NULL, ver INTEGER NOT NULL DEFAULT 1,
  sim_time  REAL, wall_time INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  payload   TEXT NOT NULL                   -- JSON
);
-- The idempotence guarantee (D19): a client may resend anything, and
-- INSERT OR IGNORE against this index makes the merge converge.
CREATE UNIQUE INDEX ev_dedup ON event(player_id, event_id);
CREATE INDEX ev_player ON event(player_id, seq);

-- Whole-batch replay short-circuit (§4.5.3 step 11): batch_id is the proof jti.
CREATE TABLE ingest_batch (
  player_id INTEGER NOT NULL, batch_id TEXT NOT NULL,   -- proof jti
  n_events INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  PRIMARY KEY (player_id, batch_id)
);

-- Per-stream hash chain (§4.5.3 step 12). gap marks a skipped seq: telemetry is
-- loss-tolerant, so a gap is recorded for forensics rather than rejected.
CREATE TABLE stream_state (
  player_id INTEGER NOT NULL, sid BLOB NOT NULL, jkt TEXT NOT NULL,
  last_seq INTEGER NOT NULL, last_bh TEXT NOT NULL, gap INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (player_id, sid)
);

-- What survives a purge (§4.7): enough to keep a banned subject out, nothing
-- that identifies them.
CREATE TABLE tombstone (user_key BLOB PRIMARY KEY, reason TEXT NOT NULL, at INTEGER NOT NULL);

CREATE TABLE archive_cursor (id INTEGER PRIMARY KEY CHECK (id = 1), last_seq INTEGER NOT NULL);
