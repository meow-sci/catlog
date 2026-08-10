-- events.db 0004 — an explicit seq allocator, so a deletion can never hand a
-- sequence number out twice.
--
-- `event.seq` is the rowid, and SQLite assigns `max(rowid) + 1` when the column
-- is omitted. That is fine for a table that is only ever appended to, and
-- events.db is not one: `PurgePlayer` deletes a player's rows (§4.7) and
-- `WithholdPlayerEvents` moves a shadowbanned player's rows out (0005). Delete
-- the row that happens to hold the highest seq and the next insert **reuses**
-- that number.
--
-- A reused seq is not a cosmetic problem. seq is the projector's cursor and the
-- archiver's cursor, and both have already passed it: the re-issued event is
-- stored, never folded onto any board, and never archived — silently, with no
-- error anywhere. It is also the one thing that makes the shadowban tables
-- unsound, because a withheld row and a live row would claim the same seq and
-- the restore in `RestorePlayerEvents` would collide.
--
-- So the allocator is explicit and it only ever moves forward. `next_seq` is
-- the next value to hand out; `InsertEvents` reserves a run of them inside the
-- caller's transaction, and `RaiseSeqFloor` lifts it past anything a restore
-- inserted at an explicit seq (§5.10). Gaps are expected and harmless — every
-- reader scans `seq > cursor` and none assumes density.
CREATE TABLE event_seq (
  id       INTEGER PRIMARY KEY CHECK (id = 1),
  next_seq INTEGER NOT NULL
);

-- Seeded from the log as it stands, so an existing database keeps allocating
-- exactly where its rowids left off.
INSERT INTO event_seq (id, next_seq) SELECT 1, coalesce(max(seq), 0) + 1 FROM event;
