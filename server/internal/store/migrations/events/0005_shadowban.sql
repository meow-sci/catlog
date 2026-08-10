-- events.db 0005 — shadow bans: moderation that withholds a player's log
-- instead of deleting it.
--
-- # What a shadow ban is here, and what it is not
--
-- Constitution §8 names "shadow-banning" as never to be built. That is the
-- **anti-cheat** sense: inferring from the shape of someone's data that they
-- are cheating and silently voiding them. This is the **moderation** sense, and
-- §5 says so explicitly — "bans, purges and deny-lists exist for abuse and
-- decency, not for stat manipulation, and nothing in §8 constrains them". It is
-- applied by a named administrator to a named account, never by a heuristic.
--
-- # Why a table and not a flag
--
-- A `WHERE NOT shadowbanned` predicate is a filter every present and future
-- read path has to remember. Moving the rows makes the exclusion structural:
-- every public surface reads either the `event` table or a projection folded
-- from it, so a withheld player is absent from both by construction, and a
-- reader added next year inherits that without knowing this feature exists.
--
-- It also makes the two decisions an operator actually has cheap and separate:
-- **restore** is a move back plus a rebuild, and **delete permanently** is a
-- `DELETE` against one table. Neither is reachable once the rows are gone,
-- which is the whole reason a ban stopped deleting them.
--
-- # seq is preserved, and that is load-bearing
--
-- A withheld row keeps its original seq, and 0004's allocator guarantees the
-- number is never handed out again while it is away. That is what makes a
-- restore exact rather than approximate: the events come back in their original
-- position in the log, so the rebuild that follows scores their records against
-- the same history — including the "whoever got there first keeps the rank"
-- tie-break, which is `updated_seq` and would otherwise be lost.

-- One row per shadowbanned player: the roster, and the review queue.
CREATE TABLE shadowban (
  player_id INTEGER PRIMARY KEY REFERENCES player(player_id),
  at        INTEGER NOT NULL,              -- unix ms
  reason    TEXT NOT NULL
);

-- The withheld half of the log. Same columns as `event` in the same order, so
-- the move is one INSERT..SELECT in each direction, plus when it was withheld.
CREATE TABLE shadowban_event (
  seq         INTEGER PRIMARY KEY,
  event_id    BLOB NOT NULL,
  player_id   INTEGER NOT NULL,
  flight_id   BLOB, session_id BLOB,
  type        TEXT NOT NULL, ver INTEGER NOT NULL DEFAULT 1,
  sim_time    REAL, wall_time INTEGER NOT NULL, recv_time INTEGER NOT NULL,
  payload     TEXT NOT NULL,
  career      TEXT,
  enc         INTEGER NOT NULL DEFAULT 0,
  withheld_at INTEGER NOT NULL             -- unix ms
);
-- The same dedup guarantee the live log has (D19): a shadowbanned player's
-- client is still shipping, still resending, and still entitled to converge.
CREATE UNIQUE INDEX sb_dedup ON shadowban_event(player_id, event_id);
CREATE INDEX sb_player ON shadowban_event(player_id, seq);
