-- Rolling daily/weekly/monthly/yearly leaderboards.
--
-- The value a player holds on a board *within one calendar window*, alongside
-- the all-time value `player_stat` already holds. "Biggest lithobrake this
-- week", "RUDs this month", "fastest to Luna this year".
--
-- # Why a table and not a stat key
--
-- Encoding the window into the key (`rud_total@2026-W32`) would have been less
-- schema and much worse: `stats.Describe` derives a board's title, unit and
-- direction from its key, `stats.Catalog` groups families by key prefix, and
-- `/v1/leaderboards/{stat}` is an exact-match lookup. A key that also carried a
-- date would make every one of those parse a compound. A period is a
-- *dimension* of a board, so it is a column.
--
-- # Why the bucket is a string
--
-- It is computed in Go from the event's `recv_time` (see stats/period.go) and
-- written as the same ISO-ish text that the read API takes as `?at=`:
-- `2026-08-07`, `2026-W32`, `2026-08`, `2026`. Every one of those sorts
-- chronologically as a plain string, which is what lets retention be a
-- `bucket < ?` delete and pagination be an ordinary index scan. tursogo has no
-- recursive CTEs and no date functions worth relying on, so nothing here asks
-- SQL to know what a week is.
--
-- # Why it is per (player, stat, period, bucket)
--
-- A counter's weekly value is not derivable from its all-time total — you
-- cannot subtract your way back to "how many RUDs in week 32" from a running
-- sum — so the deltas have to be accumulated into their window as they arrive.
-- Record and best boards could in principle be recomputed, but only by
-- re-reading every event in the window, which is a rebuild by another name.
--
-- Like every projection this is rebuildable: it is a pure function of the event
-- log, and `POST /admin/projections/rebuild` truncates and replays it. Buckets
-- derive from `recv_time` and never from the wall clock, so a rebuild lands
-- every row in exactly the window the incremental fold put it in.
CREATE TABLE player_stat_period (
  player_id INTEGER NOT NULL,
  stat      TEXT NOT NULL,
  -- 'daily' | 'weekly' | 'monthly' | 'yearly'. Not 'alltime': that is
  -- player_stat, and duplicating it here would be two sources of truth for the
  -- same number.
  period    TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  value     REAL NOT NULL,
  context   TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, stat, period, bucket)
);

-- The board page: one window of one board, ordered by value. Covers both
-- directions — SQLite scans an index backwards for DESC — and carries
-- updated_seq so the "whoever got there first keeps the rank" tie-break is
-- served from the index rather than from a sort.
CREATE INDEX stat_period_rank ON player_stat_period(stat, period, bucket, value, updated_seq);

-- Retention deletes whole windows that have aged out, across every player and
-- board at once, so it needs to find rows by age rather than by player.
CREATE INDEX stat_period_age ON player_stat_period(period, bucket);
