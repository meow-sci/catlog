-- projections.db 0005 — the build stamp: which binary's folds produced this
-- file, and over how much of the log.
--
-- # The problem it solves
--
-- projections.db is a cache of the event log (D8), and a deploy can change what
-- that cache is *supposed* to contain: a new board, a changed fold, a different
-- unit. The projector's cursor only moves forward, so after such a deploy the
-- incremental loop folds new events into the new shape while every event that
-- preceded the deploy is missing from it. The result is a board that is
-- populated, plausible and **wrong** — short by all of history — and nothing in
-- the system can tell, because a partially-folded board looks exactly like a
-- board nobody has set a record on yet.
--
-- The only correct answer is a full rebuild (PROJ-090: no backfill mechanism
-- exists and none should). This table is what makes the server able to notice
-- that one is owed, instead of relying on an operator remembering.
--
-- # What build_id is
--
-- `stats.BuildID` over the projections schema version, the ordered list of every
-- registered fold's name, and `stats.BuildVersion`. The first two catch a board
-- added, removed or renamed. The third is a hand-bumped constant and catches the
-- case the other two cannot see: a fold whose *name* is unchanged and whose
-- *meaning* changed. Bumping it is the same discipline as bumping an event's
-- `ver` — same commit as the change, no exceptions.
--
-- # complete
--
-- 0 while a build is in flight, 1 once it covers the whole log. A rebuild writes
-- its stamp into the scratch database before the swap, so a file on disk is
-- never labelled with a build it does not contain. A file that has never been
-- stamped at all — every projections.db that predates this migration — reads as
-- stale, which is the right default: it was built by an unknown fold set.
CREATE TABLE proj_build (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  build_id       TEXT NOT NULL,
  fold_version   INTEGER NOT NULL,
  schema_version INTEGER NOT NULL,
  built_from_seq INTEGER NOT NULL,        -- 0 for a full rebuild
  built_at       INTEGER NOT NULL,        -- unix ms
  complete       INTEGER NOT NULL DEFAULT 0
);
