-- projections.db initial schema — INITIAL_IMPL_PLAN.md §5.4, verbatim.
--
-- Everything here is derived from events.db and is rebuildable from seq 0
-- (D22), so this file is never archived and losing it costs only a rebuild.
--
-- Cross-file joins are impossible (two separate database files, §5.4): a
-- player_id here is resolved to a handle in Go, via the in-memory map loaded at
-- start and invalidated by the identity code.
--
-- Same Turso discipline as events.db: no STRICT, no expression indexes, FK
-- clauses are documentation only. WITHOUT ROWID (and VACUUM) are avoided by
-- policy (§5.4), not capability — they exist behind a DSN experimental flag
-- we do not enable (docs/DECISIONS.md, WP1).

-- One shared cursor for every fold: `projection = 'all'` (§5.6).
CREATE TABLE proj_checkpoint (projection TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL);

-- The leaderboard source of truth. updated_seq is the tie-break: the earliest
-- seq wins a tie (§5.6).
CREATE TABLE player_stat (
  player_id INTEGER NOT NULL, stat TEXT NOT NULL,
  value REAL NOT NULL, context TEXT,        -- JSON: body, flight, sim_t, etc.
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, stat)
);
CREATE INDEX stat_rank ON player_stat(stat, value);

-- flags is a bitfield of flight.flagged values (§4.2). Record folds skip any
-- flight with any bit set.
--   bit0 teleport, bit1 refuel, bit2 resource_edit, bit3 console,
--   bit4 tuning  (added by the WP0 contract amendment in docs/DECISIONS.md:
--                 the game's debug window live-edits the tumble speed gate,
--                 which is the sole classifier for kitten.tumble)
CREATE TABLE flight_state (
  flight_id BLOB PRIMARY KEY, player_id INTEGER NOT NULL,
  flags INTEGER NOT NULL DEFAULT 0,
  ended_reason TEXT, crew INTEGER, body TEXT, started_seq INTEGER NOT NULL
);
CREATE INDEX fs_player ON flight_state(player_id);

-- Distinct-body sets, e.g. kind='soi' backing the soi_bodies board (§5.6).
CREATE TABLE player_body (player_id INTEGER NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, first_seq INTEGER NOT NULL, PRIMARY KEY (player_id, kind, body));

-- Per-kitten roster totals folded from roster.snapshot (§4.2).
CREATE TABLE kitten (
  player_id INTEGER NOT NULL, kid TEXT NOT NULL, name TEXT NOT NULL,
  travelled_m REAL NOT NULL DEFAULT 0, fastest_ms REAL NOT NULL DEFAULT 0,
  missions INTEGER NOT NULL DEFAULT 0, mission_time_s REAL NOT NULL DEFAULT 0,
  kia INTEGER NOT NULL DEFAULT 0, updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, kid)
);

-- Recent-activity feed for the SSE panel (§5.7). Capped at 500 rows by the
-- projector after each insert — VACUUM is unused by policy (§5.4), so the
-- cap keeps the free-page churn bounded rather than the file small.
CREATE TABLE feed (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, handle TEXT NOT NULL, type TEXT NOT NULL, summary TEXT NOT NULL);
