-- projections.db 0007 — the per-save halves of the two set-backed projections.
--
-- The siblings answer different final-schema questions:
--
--   player_body  — "which worlds has this PLAYER been to"   (lifetime, unchanged)
--   career_body  — "which worlds has this SAVE been to"
--   kitten       — "what has this kitten done"               (lifetime, unchanged)
--   career_kitten— "what has this kitten done IN THIS SAVE"
--
-- The last pair matters because `kid` is
-- SHA-256("catlog-kitten:" + install_id + ":" + roster_name) and carries no
-- career, while KSA's roster lives in UniverseData — which is the save. A cat
-- called Mittens in save A and a cat called Mittens in save B are two different
-- cats sharing one kid. `kitten` merges them under max(), which is the lifetime
-- answer; `career_kitten` keeps them apart, which is the per-save one.
--
-- Novelty is what drives the two counter boards, and keeping the tables separate
-- is what keeps the two novelty signals from contaminating each other: the
-- lifetime board increments when a row is new in `player_body`, the per-save one
-- when a row is new in `career_body`, and neither can see the other's rows.
CREATE TABLE career_body (
  player_id   INTEGER NOT NULL,
  career      TEXT NOT NULL,          -- 16 Crockford chars; never ''
  -- Denormalised from `career`, so the system-scoped set counts are one
  -- `count(DISTINCT body) … WHERE system = ?` rather than a join (§3.18).
  system      TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,          -- 'soi' | 'landed' | 'orbit_kid' (Task E3)
  body        TEXT NOT NULL,
  first_seq   INTEGER NOT NULL,
  first_sim_t REAL,                   -- seconds; NULL when the arrival carried no clock
  PRIMARY KEY (player_id, career, kind, body)
);
CREATE INDEX career_body_system ON career_body(player_id, system, kind, body);

CREATE TABLE career_kitten (
  player_id      INTEGER NOT NULL,
  career         TEXT NOT NULL,
  system         TEXT NOT NULL DEFAULT '',   -- denormalised, as on career_body
  kid            TEXT NOT NULL,
  name           TEXT NOT NULL,
  travelled_m    REAL NOT NULL DEFAULT 0,
  fastest_ms     REAL NOT NULL DEFAULT 0,   -- ecliptic-frame; must never become a speed board
  missions       INTEGER NOT NULL DEFAULT 0,
  mission_time_s REAL NOT NULL DEFAULT 0,
  kia            INTEGER NOT NULL DEFAULT 0,
  updated_seq    INTEGER NOT NULL,
  PRIMARY KEY (player_id, career, kid)
);
CREATE INDEX career_kitten_player ON career_kitten(player_id, career);
CREATE INDEX career_kitten_system ON career_kitten(player_id, system);
