-- projections.db 0006 — a board, ranked per save and per celestial system.
--
-- A career is one KSA save played over time (§4.1 `career`). `player_stat` ranks
-- players; this table ranks (player, save) pairs on the same board keys, with the
-- same four merge rules and the same tie-break.
--
-- Why a table and not a compound stat key ("landings@<career>"): the argument is
-- in 0003_period.sql's header and has not changed. `stats.Describe` derives a
-- board's title, unit and direction from its key ALONE, and `stats.Catalog`
-- groups families by key prefix — both would have to start parsing. A scope is a
-- dimension of a board, so it is a column.
--
-- Why there is no period column here: a career already IS a time scope, and
-- players x boards x buckets x careers is a row count Constitution §2 has an
-- opinion about. `?scope=career&period=weekly` is a 400, deliberately.
--
-- Rebuildable from seq 0 like everything else in this file (D22).
CREATE TABLE career_stat (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,           -- 16 lowercase Crockford base32 chars, never ''
  -- The system this save is playing in, denormalised from `career` so that
  -- `?system=` is a predicate rather than a join (§3.18). '' until the career's
  -- `system.discovered` has been folded.
  system      TEXT NOT NULL DEFAULT '',
  stat        TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,                    -- JSON, same shape as player_stat.context
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, stat)
);
CREATE INDEX career_stat_system ON career_stat(stat, system, value, updated_seq);

-- The ranking index. `updated_seq` is in it because it is the tie-break, so the
-- ordering the read path asks for is covered end to end.
CREATE INDEX career_stat_rank ON career_stat(stat, value, updated_seq);

-- A per-player sequence number for a save, assigned in ascending first_seq order
-- and stable under rebuild.
--
-- It exists because the career id itself can never be published raw (it is
-- derived from the mod's install id, so it would link one person's two accounts
-- — see readapi/privacy.go and PROJ-049) and its per-player relabel is an opaque
-- 16-character token. A reader has to be able to recognise their own save. The
-- server deliberately never learns what the player called it, so an ordinal is
-- the most that can honestly be offered: "your third save".
--
-- 0 means "not yet assigned", which no row keeps after its first fold.
ALTER TABLE career ADD COLUMN ordinal INTEGER NOT NULL DEFAULT 0;
-- The last event seen for this save, whether or not it scored. Save pages use
-- this rather than max(board.updated_seq), which misses non-scoring and flagged
-- activity and fails entirely for a save with no board row.
ALTER TABLE career ADD COLUMN last_seq INTEGER NOT NULL DEFAULT 0;

-- --- the system scope ------------------------------------------------------
--
-- A celestial system is replaceable XML content (§3.15), so two players who both
-- reached something called `luna` may not have reached the same object. Ranking
-- them together is wrong, not merely uninteresting, so a system is the third
-- board scope and gets rows of its own.
--
-- The hash comes from the mod (§3.16). It is NOT install-derived and is
-- therefore NOT a deanonymisation hazard: every player running stock KSA
-- produces the same one, which is the entire point. It must never go through
-- readapi/privacy.go's per-player relabelling — that would break the only thing
-- it is for (§3.19).
CREATE TABLE system_stat (
  player_id   INTEGER NOT NULL,
  system      TEXT NOT NULL,           -- the system hash; never ''
  stat        TEXT NOT NULL,
  value       REAL NOT NULL,
  context     TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, system, stat)
);
CREATE INDEX system_stat_rank ON system_stat(stat, value, updated_seq);

-- The career learns which system it is playing in, once.
--
-- A system cannot change within a career, so this is written from the first
-- `system.discovered` for that career and never overwritten. `system_changed`
-- is the provenance mark for the case a player edits the XML and reloads: it
-- excludes nothing and scores nothing, exactly like `rewound` (§3.15, PROJ-023).
ALTER TABLE career ADD COLUMN system         TEXT NOT NULL DEFAULT '';
ALTER TABLE career ADD COLUMN system_changed INTEGER NOT NULL DEFAULT 0;
