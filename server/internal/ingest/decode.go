package ingest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// Limits are the §4.3 wire limits. The mod mirrors these constants.
type Limits struct {
	// MaxBodyBytes caps the compressed request body (1 MiB → 413).
	MaxBodyBytes int64
	// MaxDecompressedBytes caps the brotli output (8 MiB → 413).
	MaxDecompressedBytes int64
	// MaxEvents caps events per batch (2000 → 413).
	MaxEvents int
	// MaxLineBytes caps one NDJSON line (16 KiB → 400).
	MaxLineBytes int
	// MaxInFlight caps how many requests may hold a body in memory at once
	// (read, decompressed, decoded — the expensive span of the handler). Not a
	// wire limit: the caps above bound what one request may cost, this bounds
	// how many such costs the process pays concurrently, which is what actually
	// sizes peak ingest memory on a small box. Zero means 4× GOMAXPROCS — a
	// few requests of headroom per core without letting a burst hold tens of
	// decompressed batches. The request over the cap gets the same 503 +
	// Retry-After the full write queue answers with.
	MaxInFlight int
}

// DefaultLimits returns the §4.3 values.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:         1 << 20,
		MaxDecompressedBytes: 8 << 20,
		MaxEvents:            2000,
		MaxLineBytes:         16 << 10,
		MaxInFlight:          4 * runtime.GOMAXPROCS(0),
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.MaxDecompressedBytes <= 0 {
		l.MaxDecompressedBytes = d.MaxDecompressedBytes
	}
	if l.MaxEvents <= 0 {
		l.MaxEvents = d.MaxEvents
	}
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = d.MaxLineBytes
	}
	if l.MaxInFlight <= 0 {
		l.MaxInFlight = d.MaxInFlight
	}
	return l
}

// readBody reads at most MaxBodyBytes+1 bytes and rejects anything longer.
//
// The cap is enforced *while reading* (§4.3): a 400 MiB body must cost one
// megabyte of memory and then a closed connection, not a 400 MiB allocation
// followed by a length check.
func readBody(r io.Reader, limits Limits) ([]byte, *authz.Error) {
	limits = limits.withDefaults()
	body, err := io.ReadAll(io.LimitReader(r, limits.MaxBodyBytes+1))
	if err != nil {
		return nil, &authz.Error{Code: authz.CodeBadRequest, Step: 10, Detail: "request body could not be read"}
	}
	if int64(len(body)) > limits.MaxBodyBytes {
		return nil, &authz.Error{
			Code: authz.CodeTooLarge, Step: 10,
			Detail: fmt.Sprintf("compressed body exceeds %d bytes", limits.MaxBodyBytes),
		}
	}
	return body, nil
}

// brotliReaders recycles decompressor state between requests. A brotli reader
// carries multi-hundred-KiB dictionaries and ring buffers; allocating one per
// batch made the decompressor, not the decompression, the ingest path's
// biggest allocation.
var brotliReaders = sync.Pool{New: func() any { return new(brotli.Reader) }}

// decompress expands a brotli body under the §4.3 8 MiB ceiling, again enforced
// while reading — a decompression bomb must not be materialized to discover
// that it is one.
func decompress(body []byte, limits Limits) ([]byte, *authz.Error) {
	limits = limits.withDefaults()
	r := brotliReaders.Get().(*brotli.Reader)
	defer brotliReaders.Put(r)
	if err := r.Reset(bytes.NewReader(body)); err != nil {
		return nil, &authz.Error{Code: authz.CodeMalformedBatch, Step: 13, Detail: "body is not valid brotli"}
	}
	out, err := io.ReadAll(io.LimitReader(r, limits.MaxDecompressedBytes+1))
	if err != nil {
		return nil, &authz.Error{Code: authz.CodeMalformedBatch, Step: 13, Detail: "body is not valid brotli"}
	}
	if int64(len(out)) > limits.MaxDecompressedBytes {
		return nil, &authz.Error{
			Code: authz.CodeTooLarge, Step: 13,
			Detail: fmt.Sprintf("decompressed batch exceeds %d bytes", limits.MaxDecompressedBytes),
		}
	}
	return out, nil
}

// envelope is the §4.1 event envelope as it arrives. Every field is a pointer
// or a string so "absent" and "zero" stay distinguishable; unknown keys are
// rejected by the decoder.
type envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Ver     *int            `json:"ver"`
	Flight  *string         `json:"flight"`
	Session *string         `json:"session"`
	Career  *string         `json:"career"`
	SimT    *float64        `json:"sim_t"`
	WallT   *int64          `json:"wall_t"`
	Payload json.RawMessage `json:"payload"`
}

// decodeNDJSON parses and validates a decompressed batch into storable events
// (§4.1, §4.3).
//
// Batch-fatal by design: one bad line rejects the whole batch. The mod and the
// server ship together, so a malformed or unknown-typed event means version
// skew, and §4.1 says to surface that loudly rather than to drop rows quietly.
//
// It scans line boundaries over the raw bytes and runs one json.Decoder down
// the whole batch, checking after every envelope that the value ended on its
// own line. The previous shape — a string copy of the batch, a split into per
// -line strings and a fresh decoder per line — allocated roughly three times
// the batch size before a single envelope was validated; the framing rules it
// enforced are unchanged.
func decodeNDJSON(data []byte, limits Limits) ([]store.Event, *authz.Error) {
	limits = limits.withDefaults()

	// A batch is NDJSON with an optional trailing newline; interior blank lines
	// are a framing error, not something to skip past.
	data = bytes.TrimSuffix(data, []byte{'\n'})
	if len(data) == 0 {
		return nil, malformed("batch contains no events")
	}
	if bytes.IndexByte(data, '\r') >= 0 {
		return nil, malformed("batch uses CRLF line endings; NDJSON is LF only")
	}

	nLines := bytes.Count(data, []byte{'\n'}) + 1
	if nLines > limits.MaxEvents {
		return nil, &authz.Error{
			Code: authz.CodeTooLarge, Step: 13,
			Detail: fmt.Sprintf("batch has %d events, the limit is %d", nLines, limits.MaxEvents),
		}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // §4.1: unknown envelope keys are rejected

	out := make([]store.Event, 0, nLines)
	pos := 0
	for i := range nLines {
		end := len(data)
		if nl := bytes.IndexByte(data[pos:], '\n'); nl >= 0 {
			end = pos + nl
		}
		ev, aerr := decodeEnvelope(dec, data[pos:end], int64(pos), int64(end), limits)
		if aerr != nil {
			aerr.Detail = fmt.Sprintf("line %d: %s", i+1, aerr.Detail)
			return nil, aerr
		}
		out = append(out, ev)
		pos = end + 1
	}
	return out, nil
}

// decodeEnvelope validates one NDJSON line. dec is the batch-wide decoder,
// already positioned at this line; start and end are the line's byte offsets in
// the batch, which is how the one-envelope-per-line framing is enforced without
// a per-line reader.
func decodeEnvelope(dec *json.Decoder, line []byte, start, end int64, limits Limits) (store.Event, *authz.Error) {
	if len(line) == 0 {
		return store.Event{}, malformed("empty line")
	}
	if len(line) > limits.MaxLineBytes {
		return store.Event{}, malformed(fmt.Sprintf("event line is %d bytes, the limit is %d", len(line), limits.MaxLineBytes))
	}

	var env envelope
	if err := dec.Decode(&env); err != nil {
		return store.Event{}, malformed(envelopeError(err, start))
	}
	// The decoder reads values, not lines: a value that parses but runs past
	// the newline — or leaves anything but blanks behind on its line — is the
	// framing error the old per-line reader caught with More().
	off := dec.InputOffset()
	if off > end {
		return store.Event{}, malformed("trailing content after the envelope")
	}
	if len(bytes.Trim(line[off-start:], " \t")) > 0 {
		return store.Event{}, malformed("trailing content after the envelope")
	}

	id, err := ids.Parse(env.ID)
	if err != nil {
		return store.Event{}, malformed("id is not a ULID")
	}
	if !KnownType(env.Type) {
		// §4.1: an unknown type means the mod and the server disagree about the
		// taxonomy. Reject the whole batch so the skew is impossible to miss.
		return store.Event{}, malformed(fmt.Sprintf("unknown event type %q", truncate(env.Type, 64)))
	}
	if env.Ver == nil {
		return store.Event{}, malformed("ver is missing")
	}
	if *env.Ver < 1 {
		return store.Event{}, malformed(fmt.Sprintf("ver %d, want ≥ 1", *env.Ver))
	}
	if env.Session == nil || *env.Session == "" {
		return store.Event{}, malformed("session is missing")
	}
	session, err := ids.Parse(*env.Session)
	if err != nil {
		return store.Event{}, malformed("session is not a ULID")
	}
	var flight ids.ID
	if env.Flight != nil {
		if flight, err = ids.Parse(*env.Flight); err != nil {
			return store.Event{}, malformed("flight is not a ULID")
		}
	}
	if env.Career == nil {
		return store.Event{}, malformed("career is missing")
	}
	if !validCareer(*env.Career) {
		return store.Event{}, malformed("career is not " + careerShape)
	}
	if env.WallT == nil {
		return store.Event{}, malformed("wall_t is missing")
	}

	payload := env.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	} else if !isJSONObject(payload) {
		return store.Event{}, malformed("payload is not a JSON object")
	}

	ev := store.Event{
		ID:        id,
		FlightID:  flight,
		SessionID: session,
		Career:    *env.Career,
		Type:      env.Type,
		Ver:       *env.Ver,
		WallTime:  *env.WallT,
		// Payload is stored verbatim: unknown *payload* keys are preserved for
		// forward compatibility (§4.1), which is the opposite of the envelope
		// rule immediately above.
		Payload: payload,
	}
	if env.SimT != nil {
		ev.SimTime = sql.NullFloat64{Float64: *env.SimT, Valid: true}
	}
	return ev, nil
}

// CareerLength is the character length of the §4.1 `career` id: 16 lowercase
// Crockford base32 characters, the same construction as a `kid`.
const CareerLength = 16

const careerShape = "16 lowercase Crockford base32 characters"

// ValidCareer reports whether s is a well-formed §4.1 career id. Exported so the
// admin event endpoint applies exactly the same rule as the wire does.
func ValidCareer(s string) bool { return validCareer(s) }

// validCareer checks the §4.1 career id. Fixed length and a closed alphabet,
// because this value becomes a grouping key on a public board: anything looser
// would let a client mint visually confusable careers, and anything longer would
// be a free text column on every stored row.
func validCareer(s string) bool {
	if len(s) != CareerLength {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z' && c != 'i' && c != 'l' && c != 'o' && c != 'u':
		default:
			return false
		}
	}
	return true
}

// envelopeError turns a decoder error into a client-safe detail. json's
// "unknown field" message is worth passing through — it names the offending key,
// which is exactly what a mod author needs. lineStart rebases the batch-wide
// decoder's syntax offset onto the offending line, matching what the old
// per-line reader reported.
func envelopeError(err error, lineStart int64) string {
	msg := err.Error()
	if strings.Contains(msg, "unknown field") {
		return "envelope has an " + truncate(msg[strings.Index(msg, "unknown field"):], 80)
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return fmt.Sprintf("invalid JSON at byte %d", max(syn.Offset-lineStart, 0))
	}
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		return fmt.Sprintf("field %q has the wrong type", typ.Field)
	}
	return "envelope is not a valid event object"
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}

func malformed(detail string) *authz.Error {
	return &authz.Error{Code: authz.CodeMalformedBatch, Step: 13, Detail: detail}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
