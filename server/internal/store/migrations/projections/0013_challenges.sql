-- projections.db 0013 — time-boxed challenges.
--
-- A challenge is a board with a curated rule and an explicit start and end date.
-- Its definition will live in server/internal/stats/challenges.go — in the
-- deployed artifact, never in mutable runtime state — because incremental and
-- rebuilt projections must fold history against the same rule.
--
-- These rows do not use player_stat_period: its calendar buckets are deleted by
-- retention, while closed challenge results are the permanent archive.
--
-- Scope sentinels:
--   player: career='', system=''
--   career: career='<id>', system='<hash>'
--   system: career='', system='<hash>'
CREATE TABLE challenge_stat (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,
  challenge   TEXT NOT NULL,
  system      TEXT NOT NULL DEFAULT '',
  value       REAL NOT NULL,
  context     TEXT,
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, system, challenge)
);

CREATE INDEX challenge_rank ON challenge_stat(challenge, system, value, updated_seq);

-- Batch-aware distinct members for set-valued challenge rules. Career and
-- system use the same sentinels as challenge_stat.
CREATE TABLE challenge_member (
  player_id INTEGER NOT NULL,
  career    TEXT NOT NULL,
  system    TEXT NOT NULL,
  challenge TEXT NOT NULL,
  member    TEXT NOT NULL,
  first_seq INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, system, challenge, member)
);

CREATE INDEX challenge_member_count
  ON challenge_member(challenge, player_id, career, system);
