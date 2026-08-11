-- projections.db 0011 — immutable first-earned badge awards.
--
-- An empty career is the lifetime-award sentinel. System and first_career are
-- optional provenance represented by empty strings; NULL earned_sim_t and
-- context preserve facts that were not available when the award was earned.
CREATE TABLE badge_award (
  player_id    INTEGER NOT NULL,
  career       TEXT NOT NULL,
  badge        TEXT NOT NULL,
  system       TEXT NOT NULL DEFAULT '',
  first_career TEXT NOT NULL DEFAULT '',
  earned_seq   INTEGER NOT NULL,
  earned_at    INTEGER NOT NULL,
  earned_sim_t REAL,
  context      TEXT,
  PRIMARY KEY (player_id, career, badge)
);
CREATE INDEX badge_system ON badge_award(system, badge, earned_seq);
CREATE INDEX badge_holders ON badge_award(badge, earned_seq);
CREATE INDEX badge_by_career ON badge_award(player_id, career, earned_seq);
