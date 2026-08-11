-- projections.db 0009 — persist the final-v1 flight.started engine reading.
--
-- NULL means flight.started was not seen or the KSA module read failed. A
-- present zero is meaningful: the vehicle began this flight with no installed
-- rocket engines. Later flight facts and milestone bits get their own migration
-- rather than claiming columns this vertical slice does not yet populate.
ALTER TABLE flight_state ADD COLUMN engine_count INTEGER;
