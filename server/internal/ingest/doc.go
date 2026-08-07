// Package ingest reads capped request bodies, decompresses brotli, decodes and
// validates NDJSON envelopes and drives the single-writer pipeline (§4.3, §5.5).
// Implemented in WP2.
package ingest
