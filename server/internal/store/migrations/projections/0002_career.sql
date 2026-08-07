-- projections.db 0002 — careers and career-relative arrival times.
--
-- A career is one KSA save played over time (§4.1 `career`). It is the unit the
-- "fastest career time to X" boards are measured in, and the unit the rewind
-- mark applies to.
--
-- Everything here is derived from events.db and is rebuildable from seq 0, like
-- the rest of this file (D22).
CREATE TABLE career (
  player_id INTEGER NOT NULL,
  career    TEXT NOT NULL,              -- 16 lowercase Crockford base32 chars
  -- The highest sim_t ever seen in this career: its high-water mark.
  max_sim_t REAL NOT NULL DEFAULT 0,
  -- Set when a session.started for this career arrived with a sim_t below
  -- max_sim_t, i.e. an earlier save of this career was loaded. It is a
  -- provenance mark on the career's times and has no other effect: nothing is
  -- excluded, nothing is scored (docs/CONSTITUTION.md §8, docs/events.md).
  rewound   INTEGER NOT NULL DEFAULT 0,
  first_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career)
);

-- How far into the career the player first reached this body. Recorded for
-- every body, including ones with no board of their own, so that widening
-- stats.TimedBodies later is a rebuild rather than a data-loss discovery.
-- NULL means "reached, but on an event that carried no career or no clock".
ALTER TABLE player_body ADD COLUMN first_sim_t REAL;
