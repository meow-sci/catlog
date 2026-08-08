-- events.db 0003 — per-event payload compression.
--
-- Measured on the production log (1.05 M events, 657 MB): the payload JSON is
-- 66.5% of the file, and most of every telemetry.window payload is the same
-- key names and punctuation again. A shared trained zstd dictionary turns each
-- payload into a small self-describing frame; the JSON any reader sees is
-- byte-identical, because decoding happens inside the store's one read seam.
--
-- payload_dict is append-only: a row is inserted at open when the running
-- binary embeds a dictionary the table lacks, and rows are NEVER updated or
-- deleted, so every historical event row stays decodable forever — even by a
-- future binary that compresses with a newer dictionary. `bytes` is a trained
-- zstd dictionary (magic 0xEC30A437); the zstd dictionary ID embedded in it is
-- written into every compressed frame, which is what makes a row
-- self-describing. dict_id is catlog's own version number for the dictionary
-- (1 = the go:embed'ded v1), not the zstd-internal ID.
CREATE TABLE payload_dict (
  dict_id    INTEGER PRIMARY KEY,
  bytes      BLOB NOT NULL,
  created_at INTEGER NOT NULL              -- unix ms
);

-- How event.payload is encoded: 0 = JSON text (every row written before this
-- migration, and any row written with compression off or where compression
-- did not help), 1 = a zstd frame built with a payload_dict dictionary.
--
-- Existing rows are NEVER rewritten (the log is immutable, and VACUUM is
-- unused by policy §5.4, so a rewrite could not even reclaim the pages);
-- old rows simply keep enc = 0 and read back exactly as before.
ALTER TABLE event ADD COLUMN enc INTEGER NOT NULL DEFAULT 0;
