package store

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Per-event payload compression (events.db migration 0003).
//
// Measured on the production log (1.05 M events, 657 MB): the payload JSON
// TEXT column is 66.5% of the file, and the bulk of every telemetry.window
// payload (83.8% of rows) is the same key names and punctuation again. Each
// payload is therefore stored as one small zstd frame built with a shared
// trained dictionary, and decompressed inside this package's single read seam
// ([Events.scanStoredEvents]) — so the projector, the read API and the archive
// all see JSON byte-identical to what was ingested, and the archive's NDJSON
// chunks stay deterministic.
//
// Rows are self-describing via the `enc` column and are never rewritten:
// enc = 0 rows (everything written before 0003, plus any row written with
// compression off or where compression did not pay) are returned verbatim,
// enc = 1 rows are zstd frames whose header carries the zstd dictionary ID,
// resolved against the append-only payload_dict table. That is what makes the
// lazy migration safe — a mixed log reads correctly forever, and no historical
// dictionary is ever mutated.

// zstdDecoder lets store.go declare the Events field without importing zstd.
type zstdDecoder = zstd.Decoder

// payloadDictV1 is trained dictionary v1.
//
// Provenance: trained 2026-08-08 from the production data/events.db (1,054,660
// events, 657 MB) — 5,149 payloads sampled stratified across all 22 event
// types (proportional to row count with a floor of 20 per type;
// telemetry.window dominant at 83.8% of rows), one sample file per payload,
// then `zstd --train --maxdict=16384` (zstd CLI v1.5.7, cover algorithm;
// --train-fastcover and --train-legacy measured worse through the klauspost
// encoder on a 50,223-payload holdout: 3.20× and 3.18× vs 3.25×). The zstd
// dictionary ID embedded in the blob is what compressed frames reference.
//
//go:embed dict/payload_v1.zstd
var payloadDictV1 []byte

// payloadDictV1ID is dictionary v1's payload_dict.dict_id — catlog's own
// version number for the dictionary, not the zstd-internal ID inside the blob.
const payloadDictV1ID = 1

// event.enc values. Anything non-zero is treated as a zstd frame; new values
// would only exist to distinguish future encodings for humans reading the
// table, since the frame itself says which dictionary it needs.
const (
	encJSONText = 0 // payload column holds JSON text (pre-0003 rows, fallback)
	encZstdDict = 1 // payload column holds a zstd frame built with a payload_dict dictionary
)

// payloadEncoder is the shared write-path encoder: zstd level 3
// (SpeedDefault, the archive's level) with dictionary v1. One package-level
// encoder rather than per-call construction because building an Encoder
// allocates real state; EncodeAll on a shared Encoder is documented
// concurrency-safe (it draws per-call state from an internal pool).
// Concurrency 1 matches the single-writer discipline (§5.5) — concurrent
// callers serialize rather than each holding encoder state.
var payloadEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderDict(payloadDictV1),
		zstd.WithEncoderConcurrency(1),
	)
})

// initPayloadCodec runs at open, after migration: it appends any embedded
// dictionary the payload_dict table lacks (INSERT OR IGNORE — an existing row
// is never touched), then builds the decoder over every dictionary the table
// holds, so rows compressed by any past binary stay readable.
func (e *Events) initPayloadCodec(ctx context.Context) error {
	if _, err := e.Writer().ExecContext(ctx,
		`INSERT OR IGNORE INTO payload_dict (dict_id, bytes, created_at) VALUES (?, ?, ?)`,
		payloadDictV1ID, payloadDictV1, e.nowMillis()); err != nil {
		return fmt.Errorf("store: insert payload dictionary v%d: %w", payloadDictV1ID, err)
	}

	rows, err := e.Writer().QueryContext(ctx, `SELECT dict_id, bytes FROM payload_dict ORDER BY dict_id`)
	if err != nil {
		return fmt.Errorf("store: read payload dictionaries: %w", err)
	}
	defer rows.Close()

	var (
		dicts  [][]byte
		haveV1 bool
	)
	for rows.Next() {
		var (
			id int64
			b  []byte
		)
		if err := rows.Scan(&id, &b); err != nil {
			return fmt.Errorf("store: scan payload dictionary: %w", err)
		}
		dicts = append(dicts, b)
		if id == payloadDictV1ID && bytes.Equal(b, payloadDictV1) {
			haveV1 = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read payload dictionaries: %w", err)
	}
	if !haveV1 {
		// Defensive: the table should always hold v1 after the insert above,
		// but the encoder writes frames against the embedded bytes, so make
		// certain the decoder knows them even if the stored row ever diverged.
		dicts = append(dicts, payloadDictV1)
	}

	// DecodeAll on a shared Decoder is documented concurrency-safe; the frame
	// header's dictionary ID selects among the registered dictionaries.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(dicts...))
	if err != nil {
		return fmt.Errorf("store: build payload decoder: %w", err)
	}
	e.dec = dec
	return nil
}

// encodePayload prepares one payload for storage, returning the SQL argument
// and the `enc` column value. A nil or empty payload is stored as `{}` (the
// §4.1 "may be {}" default, same as before compression existed).
//
// The compressed form is used only when it is a strict win; otherwise — tiny
// payloads, incompressible ones, an encoder error (which EncodeAll cannot
// actually produce, but belt and braces: an event is never lost to its own
// compression) — the JSON text goes in verbatim as enc = 0.
func (e *Events) encodePayload(p []byte) (arg any, enc int) {
	if len(p) == 0 {
		p = []byte("{}")
	}
	if e.compress {
		if zenc, err := payloadEncoder(); err == nil {
			if c := zenc.EncodeAll(p, nil); len(c) < len(p) {
				return c, encZstdDict
			}
		}
	}
	return string(p), encJSONText
}

// decodePayload maps a stored (payload, enc) pair back to the ingested JSON.
func (e *Events) decodePayload(payload []byte, enc int) ([]byte, error) {
	if enc == encJSONText {
		return payload, nil
	}
	p, err := e.dec.DecodeAll(payload, nil)
	if err != nil {
		return nil, fmt.Errorf("store: decompress payload (enc=%d): %w", enc, err)
	}
	return p, nil
}
