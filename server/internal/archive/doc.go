// Package archive copies the raw event log to durable object storage: the
// [Store] interface, its filesystem implementation, the zstd chunk writer, the
// per-player manifest, the cursor and the restore path (§5.10).
//
// # Only the log, and only ever copied
//
// The raw event log is the only thing archived. Projections are derived and
// rebuildable by design (D8, §5.6), so archiving them would be archiving a
// cache — and a restore proves the point: it replays chunks into an empty
// events.db and a rebuild reconstructs every projection from them.
//
// Archiving **copies**. Nothing in this package deletes an event, truncates the
// log or prunes a local file; the only deletion here is [Archiver.DeletePlayerArchive],
// which serves the §4.7 purge. Local retention pruning is a separate decision
// that has not been taken (§5.10).
//
// # Key layout
//
// Identical for the filesystem store today and an R2 bucket later
// (docs/r2-archive-design.md), because the layout is the contract and the
// implementation is not:
//
//	players/<sub>/chunks/<firstseq>-<lastseq>.ndjson.zst
//	players/<sub>/manifest.json
//
// `sub` is `b64u(user_key)` — the same value a license carries (§4.5.1) and the
// same value the purge seam is handed, which is what lets a purge delete a
// player's archive without this package and the identity package knowing about
// each other.
//
// # Secret hygiene
//
// `sub` is derived from a player's user_key. It is safe on the wire and unsafe
// in a log line, so every log statement here renders at most an 8-character
// prefix (§5.11).
package archive
