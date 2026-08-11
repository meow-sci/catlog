-- projections.db 0008 — the celestial systems players are playing in.
--
-- catlog holds NO list of celestial bodies (PROJ-033). These tables contain
-- only what a mod reported from the game, including worlds nobody visited.
CREATE TABLE system (
  hash        TEXT PRIMARY KEY,
  system_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  slug        TEXT NOT NULL,
  home_body   TEXT NOT NULL,
  body_count  INTEGER NOT NULL DEFAULT 0,
  reported_complete INTEGER NOT NULL DEFAULT 0,
  first_seq   INTEGER NOT NULL
);
CREATE UNIQUE INDEX system_slug ON system(slug);

CREATE TABLE system_body (
  hash     TEXT NOT NULL,
  body     TEXT NOT NULL,
  name     TEXT NOT NULL,
  class    TEXT NOT NULL,
  kind     TEXT NOT NULL,
  rank     INTEGER NOT NULL,
  parent   TEXT,
  radius_m REAL NOT NULL DEFAULT 0,
  mass_kg  REAL NOT NULL DEFAULT 0,
  soi_m    REAL NOT NULL DEFAULT 0,
  atmo_m   REAL NOT NULL DEFAULT 0,
  ocean_m  REAL NOT NULL DEFAULT 0,
  angvel   REAL NOT NULL DEFAULT 0,
  axis_x   REAL NOT NULL DEFAULT 0,
  axis_y   REAL NOT NULL DEFAULT 0,
  axis_z   REAL NOT NULL DEFAULT 0,
  sma_m    REAL,
  ecc      REAL,
  inc_deg  REAL,
  lan_deg  REAL,
  argp_deg REAL,
  t_pe     REAL,
  period_s REAL,
  ccf_to_cce_t0_x REAL NOT NULL,
  ccf_to_cce_t0_y REAL NOT NULL,
  ccf_to_cce_t0_z REAL NOT NULL,
  ccf_to_cce_t0_w REAL NOT NULL,
  first_seq INTEGER NOT NULL,
  PRIMARY KEY (hash, body)
);
CREATE INDEX system_body_kind ON system_body(hash, kind, body);
CREATE INDEX career_system ON career(system, player_id);
