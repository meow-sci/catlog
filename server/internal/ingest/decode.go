package ingest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
}

// DefaultLimits returns the §4.3 values.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:         1 << 20,
		MaxDecompressedBytes: 8 << 20,
		MaxEvents:            2000,
		MaxLineBytes:         16 << 10,
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

// decompress expands a brotli body under the §4.3 8 MiB ceiling, again enforced
// while reading — a decompression bomb must not be materialized to discover
// that it is one.
func decompress(body []byte, limits Limits) ([]byte, *authz.Error) {
	limits = limits.withDefaults()
	r := brotli.NewReader(bytes.NewReader(body))
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
func decodeNDJSON(data []byte, limits Limits) ([]store.Event, *authz.Error) {
	limits = limits.withDefaults()

	// A batch is NDJSON with an optional trailing newline; interior blank lines
	// are a framing error, not something to skip past.
	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, malformed("batch contains no events")
	}
	if strings.ContainsRune(text, '\r') {
		return nil, malformed("batch uses CRLF line endings; NDJSON is LF only")
	}

	lines := strings.Split(text, "\n")
	if len(lines) > limits.MaxEvents {
		return nil, &authz.Error{
			Code: authz.CodeTooLarge, Step: 13,
			Detail: fmt.Sprintf("batch has %d events, the limit is %d", len(lines), limits.MaxEvents),
		}
	}

	out := make([]store.Event, 0, len(lines))
	for i, line := range lines {
		ev, aerr := decodeEnvelope(line, limits)
		if aerr != nil {
			aerr.Detail = fmt.Sprintf("line %d: %s", i+1, aerr.Detail)
			return nil, aerr
		}
		out = append(out, ev)
	}
	return out, nil
}

func decodeEnvelope(line string, limits Limits) (store.Event, *authz.Error) {
	if line == "" {
		return store.Event{}, malformed("empty line")
	}
	if len(line) > limits.MaxLineBytes {
		return store.Event{}, malformed(fmt.Sprintf("event line is %d bytes, the limit is %d", len(line), limits.MaxLineBytes))
	}

	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields() // §4.1: unknown envelope keys are rejected

	var env envelope
	if err := dec.Decode(&env); err != nil {
		return store.Event{}, malformed(envelopeError(err))
	}
	if dec.More() {
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
// which is exactly what a mod author needs.
func envelopeError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "unknown field") {
		return "envelope has an " + truncate(msg[strings.Index(msg, "unknown field"):], 80)
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return fmt.Sprintf("invalid JSON at byte %d", syn.Offset)
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
