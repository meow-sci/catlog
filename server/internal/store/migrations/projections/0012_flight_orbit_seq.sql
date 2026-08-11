-- projections.db 0012 — retain the first achieved-orbit sequence.
--
-- The milestone bit remains the accumulated fact. This sequence is the durable
-- ordering fact composite badges need during the rebuild's completed first pass.
ALTER TABLE flight_state ADD COLUMN first_orbit_seq INTEGER NOT NULL DEFAULT 0;
