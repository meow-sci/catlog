-- projections.db 0004 — the event census: catlog's statistics about catlog.
--
-- Every other projection in this file answers a question about a *player*: who
-- hit the ground hardest, who got to Luna fastest, who has lost the most
-- vehicles. This one answers questions about the log itself — how many events
-- there are, of what kinds, arriving how fast, since when. It is the data for
-- nerds about the data collected by nerds, and it backs `GET /v1/stats` and the
-- "Stats of stats" page.
--
-- # Why a table rather than a count over events.db
--
-- `SELECT type, count(*) FROM event GROUP BY type` is a full scan of the
-- largest table catlog has (1.05 M rows, 657 MB — see events 0003), and the
-- per-window breakdown is that scan again with a date function per row, which
-- tursogo does not have. A public page may not cost that. Folding it is O(1)
-- per event and turns the whole page into a handful of indexed reads.
--
-- # The shape
--
-- One row per (type, period, bucket), where:
--
--   * `type` is the event type, and **the empty string is the total** across
--     every type. Storing that as a row rather than summing the others at read
--     time is what makes "how many events are there" a point lookup instead of
--     a group-by, and it keeps the total honest for a type this build cannot
--     name.
--   * `period` is 'alltime' or one of the four rolling windows, spelled exactly
--     as stats/period.go spells them.
--   * `bucket` is that window's key — '2026-08-07', '2026-W32', '2026-08',
--     '2026' — and '' for alltime. Same formats as `player_stat_period`, so it
--     sorts chronologically as plain text and nothing here asks SQL what a week
--     is.
--
-- So "events last month, by type" is one indexed range, and "a daily series for
-- a sparkline" is another.
--
-- # Why no retention, when player_stat_period has some
--
-- `player_stat_period` is players × boards × buckets, which grows without
-- bound in three dimensions at once. This is types × buckets: a couple of dozen
-- types over a daily bucket each is under ten thousand rows a year, and the
-- whole point of the page is the long view — "the busiest day catlog has ever
-- had" is not a question a 90-day horizon can answer. So it keeps everything,
-- and that is a decision rather than an oversight.
--
-- Rebuildable from seq 0 like every other projection (D22), and derived from
-- `recv_time` rather than the wall clock, so a rebuild lands every event in
-- exactly the window the incremental fold put it in.
CREATE TABLE event_census (
  type   TEXT NOT NULL,             -- '' = every type
  period TEXT NOT NULL,             -- 'alltime' | 'daily' | 'weekly' | 'monthly' | 'yearly'
  bucket TEXT NOT NULL,             -- '' for alltime
  n      INTEGER NOT NULL,
  -- The seq and receive time of the first and last event counted into this
  -- row. first_* is what dates the log ("catlog has been watching since…"),
  -- last_* is what says whether a window is still filling.
  first_seq INTEGER NOT NULL,
  last_seq  INTEGER NOT NULL,
  first_at  INTEGER NOT NULL,       -- unix ms, server receive time
  last_at   INTEGER NOT NULL,
  PRIMARY KEY (type, period, bucket)
);

-- The read shapes: one window across every type ("this month, broken down"),
-- and one period's buckets newest-first for the daily series. Both are covered
-- by leading with (period, bucket).
CREATE INDEX census_window ON event_census(period, bucket, type);

-- "The busiest day catlog has ever had", and its quiet twin, without sorting
-- the table.
CREATE INDEX census_busiest ON event_census(period, type, n);
