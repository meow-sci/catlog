package archive

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// MaxChunkBytes caps how much a single chunk may decompress to. A chunk this
// process wrote cannot exceed it, so tripping it means the archive has been
// tampered with or corrupted — which is exactly when a restore should stop
// rather than allocate whatever the header claims.
const MaxChunkBytes = 512 << 20

// line is one archived event: the §4.1 wire envelope, verbatim, plus the two
// server-local values that make a restore faithful (§5.10).
//
// The underscore prefix is the whole convention: `_seq` and `_recv` are not
// envelope fields and never were — a mod that received this line would reject
// them as unknown envelope keys (§4.1) — so marking them as server metadata
// keeps the format honest about which half is the wire contract.
//
// Field order is declaration order (encoding/json does not sort struct fields),
// so two runs over the same events produce byte-identical lines.
type line struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Ver     int             `json:"ver"`
	Flight  *string         `json:"flight"`
	Session string          `json:"session"`
	Career  string          `json:"career"`
	SimT    *float64        `json:"sim_t"`
	WallT   int64           `json:"wall_t"`
	Payload json.RawMessage `json:"payload"`
	// Seq is the server-local total order (§5.4 `event.seq`). A restore
	// re-inserts at exactly this value, which is what makes a rebuilt
	// `player_stat.updated_seq` match the original.
	Seq int64 `json:"_seq"`
	// Recv is `event.recv_time`: when the server accepted the event, as opposed
	// to `wall_t`, which is the client's untrusted clock (§4.1).
	Recv int64 `json:"_recv"`
}

// encodeLine renders one stored event as an archive NDJSON line, without the
// terminating newline.
func encodeLine(se store.StoredEvent) ([]byte, error) {
	l := line{
		ID:      ids.String(se.ID),
		Type:    se.Type,
		Ver:     se.Ver,
		Session: ids.String(se.SessionID),
		Career:  se.Career,
		WallT:   se.WallTime,
		Payload: se.Payload,
		Seq:     se.Seq,
		Recv:    se.RecvTime,
	}
	if se.FlightID != ids.Zero {
		f := ids.String(se.FlightID)
		l.Flight = &f
	}
	if se.SimTime.Valid {
		v := se.SimTime.Float64
		l.SimT = &v
	}
	if len(l.Payload) == 0 {
		l.Payload = json.RawMessage("{}")
	}
	b, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("archive: encode event %s: %w", l.ID, err)
	}
	return b, nil
}

// decodeLine parses an archive NDJSON line back into a stored event.
//
// Strict about unknown fields on purpose: this is disaster recovery, and a line
// carrying a field this build does not understand means the archive was written
// by a newer server. Failing loudly beats restoring a subset of each event.
func decodeLine(b []byte) (store.StoredEvent, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	var l line
	if err := dec.Decode(&l); err != nil {
		return store.StoredEvent{}, fmt.Errorf("archive: decode chunk line: %w", err)
	}
	if dec.More() {
		return store.StoredEvent{}, fmt.Errorf("archive: chunk line has trailing content")
	}

	id, err := ids.Parse(l.ID)
	if err != nil {
		return store.StoredEvent{}, fmt.Errorf("archive: chunk line id: %w", err)
	}
	session, err := ids.Parse(l.Session)
	if err != nil {
		return store.StoredEvent{}, fmt.Errorf("archive: chunk line %s session: %w", l.ID, err)
	}
	var flight ids.ID
	if l.Flight != nil && *l.Flight != "" {
		if flight, err = ids.Parse(*l.Flight); err != nil {
			return store.StoredEvent{}, fmt.Errorf("archive: chunk line %s flight: %w", l.ID, err)
		}
	}
	if l.Type == "" {
		return store.StoredEvent{}, fmt.Errorf("archive: chunk line %s has no type", l.ID)
	}
	if l.Seq <= 0 {
		return store.StoredEvent{}, fmt.Errorf("archive: chunk line %s has seq %d", l.ID, l.Seq)
	}
	payload := l.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}

	se := store.StoredEvent{
		Seq:      l.Seq,
		RecvTime: l.Recv,
		Event: store.Event{
			ID:        id,
			FlightID:  flight,
			SessionID: session,
			Career:    l.Career,
			Type:      l.Type,
			Ver:       l.Ver,
			WallTime:  l.WallT,
			Payload:   payload,
		},
	}
	if l.SimT != nil {
		se.SimTime = sql.NullFloat64{Float64: *l.SimT, Valid: true}
	}
	return se, nil
}

// --- zstd ---------------------------------------------------------------------

// compressChunk compresses raw NDJSON deterministically.
//
// Determinism is a requirement, not a nicety (§12 WP10): the same events must
// produce the same bytes, so an operator can compare an archive against a
// re-run. Two settings buy it — a pinned encoder level, and single-threaded
// encoding, because a concurrent encoder splits the input into blocks by
// goroutine scheduling and the block boundaries land in the output.
func compressChunk(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		return nil, fmt.Errorf("archive: build zstd encoder: %w", err)
	}
	if _, err := enc.Write(raw); err != nil {
		enc.Close()
		return nil, fmt.Errorf("archive: compress chunk: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("archive: finish chunk: %w", err)
	}
	return buf.Bytes(), nil
}

// decompressChunk expands a chunk under [MaxChunkBytes].
func decompressChunk(r io.Reader) ([]byte, error) {
	dec, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("archive: build zstd decoder: %w", err)
	}
	defer dec.Close()

	out, err := io.ReadAll(io.LimitReader(dec, MaxChunkBytes+1))
	if err != nil {
		return nil, fmt.Errorf("archive: decompress chunk: %w", err)
	}
	if int64(len(out)) > MaxChunkBytes {
		return nil, fmt.Errorf("archive: chunk decompresses to more than %d bytes", MaxChunkBytes)
	}
	return out, nil
}

// decodeChunk parses a decompressed chunk into stored events, in file order.
func decodeChunk(raw []byte) ([]store.StoredEvent, error) {
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	out := make([]store.StoredEvent, 0, len(lines))
	for i, l := range lines {
		se, err := decodeLine([]byte(l))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, se)
	}
	return out, nil
}
