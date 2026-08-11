-- projections.db 0010 — retain launch facts and set-only flight milestones.
--
-- NULL launch facts mean flight.started was never observed. Present zeroes are
-- real readings. Career is the first nonempty career carried by any event for
-- the flight and is never replaced by a later value.
ALTER TABLE flight_state ADD COLUMN milestones INTEGER NOT NULL DEFAULT 0;
ALTER TABLE flight_state ADD COLUMN part_count INTEGER;
ALTER TABLE flight_state ADD COLUMN launch_mass_kg REAL;
ALTER TABLE flight_state ADD COLUMN career TEXT NOT NULL DEFAULT '';
