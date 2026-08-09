package ingest

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/meow-sci/catlog/server/internal/authz"
)

const validLine = `{"id":"01JZ0000000000000000000001","type":"vehicle.rud","ver":1,"flight":"01JZ0000000000000000000002","session":"01JZ0000000000000000000003","career":"0123456789abcdef","sim_t":12345.678,"wall_t":1770000000123,"payload":{"cause":"ground_impact"}}`

// TestDecodeNDJSONValid checks the fields that reach the store, including the
// two forward-compatibility rules of §4.1: a null flight becomes SQL NULL, and
// unknown *payload* keys survive verbatim.
func TestDecodeNDJSONValid(t *testing.T) {
	batch := validLine + "\n" +
		`{"id":"01JZ0000000000000000000004","type":"session.started","ver":1,"flight":null,"session":"01JZ0000000000000000000003","career":"0123456789abcdef","sim_t":0,"wall_t":1770000000124,"payload":{"mod_ver":"0.1.0","future_key":{"nested":true}}}` + "\n"

	events, aerr := decodeNDJSON([]byte(batch), DefaultLimits())
	if aerr != nil {
		t.Fatalf("decodeNDJSON: %v", aerr)
	}
	if len(events) != 2 {
		t.Fatalf("decoded %d events, want 2", len(events))
	}

	first := events[0]
	if first.Type != "vehicle.rud" || first.Ver != 1 {
		t.Errorf("first event = %s ver %d", first.Type, first.Ver)
	}
	if !first.SimTime.Valid || first.SimTime.Float64 != 12345.678 {
		t.Errorf("sim_t = %v, want 12345.678", first.SimTime)
	}
	if first.WallTime != 1770000000123 {
		t.Errorf("wall_t = %d", first.WallTime)
	}
	if first.FlightID.Compare(events[1].FlightID) == 0 {
		t.Error("the two events share a flight id; the second should be zero")
	}

	second := events[1]
	if !second.FlightID.IsZero() {
		t.Error("a null flight must decode to the zero ULID, which the store maps to SQL NULL")
	}
	if !strings.Contains(string(second.Payload), "future_key") {
		t.Errorf("unknown payload keys must be preserved (§4.1); got %s", second.Payload)
	}
	// sim_t: 0 is a real reading, not an absent one.
	if !second.SimTime.Valid || second.SimTime.Float64 != 0 {
		t.Errorf("sim_t 0 must stay a value, got %v", second.SimTime)
	}
}

// TestDecodeNDJSONRejections is the §4.1/§4.3 rejection matrix. Every case is
// batch-fatal: one bad line rejects the batch.
func TestDecodeNDJSONRejections(t *testing.T) {
	cases := []struct {
		name string
		code string
		body string
	}{
		{"unknown type", authz.CodeMalformedBatch, strings.Replace(validLine, "vehicle.rud", "vehicle.teleported", 1)},
		{"unknown envelope key", authz.CodeMalformedBatch, strings.Replace(validLine, `"ver":1`, `"ver":1,"nonce":"x"`, 1)},
		{"id is not a ULID", authz.CodeMalformedBatch, strings.Replace(validLine, "01JZ0000000000000000000001", "not-a-ulid", 1)},
		{"session missing", authz.CodeMalformedBatch, strings.Replace(validLine, `"session":"01JZ0000000000000000000003",`, "", 1)},
		{"session null", authz.CodeMalformedBatch, strings.Replace(validLine, `"session":"01JZ0000000000000000000003"`, `"session":null`, 1)},
		{"ver missing", authz.CodeMalformedBatch, strings.Replace(validLine, `"ver":1,`, "", 1)},
		{"ver below 1", authz.CodeMalformedBatch, strings.Replace(validLine, `"ver":1`, `"ver":0`, 1)},
		{"wall_t missing", authz.CodeMalformedBatch, strings.Replace(validLine, `"wall_t":1770000000123,`, "", 1)},
		{"payload is not an object", authz.CodeMalformedBatch, strings.Replace(validLine, `"payload":{"cause":"ground_impact"}`, `"payload":[1,2]`, 1)},
		{"not JSON", authz.CodeMalformedBatch, "{" + validLine},
		{"empty batch", authz.CodeMalformedBatch, ""},
		{"only a newline", authz.CodeMalformedBatch, "\n"},
		{"interior blank line", authz.CodeMalformedBatch, validLine + "\n\n" + validLine + "\n"},
		{"CRLF framing", authz.CodeMalformedBatch, validLine + "\r\n"},
		{"trailing content on a line", authz.CodeMalformedBatch, validLine + " {}\n"},
		// One envelope per line: a JSON value that only parses by continuing
		// past the newline is a framing error, not two half-lines.
		{"envelope spans two lines", authz.CodeMalformedBatch, `{"id":"01JZ0000000000000000000001",` + "\n" + `"type":"vehicle.rud"}` + "\n" + validLine + "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, aerr := decodeNDJSON([]byte(tc.body), DefaultLimits())
			if aerr == nil {
				t.Fatal("decodeNDJSON accepted it")
			}
			if aerr.Code != tc.code {
				t.Errorf("code = %q, want %q (detail: %s)", aerr.Code, tc.code, aerr.Detail)
			}
			if got := authz.Status(aerr.Code); got != 400 {
				t.Errorf("status = %d, want 400", got)
			}
		})
	}
}

// TestDecodeNDJSONErrorNamesTheLine pins that a rejection points at the
// offending line, and that a syntax offset is line-relative — both survived the
// move from a decoder per line to one decoder per batch.
func TestDecodeNDJSONErrorNamesTheLine(t *testing.T) {
	batch := validLine + "\n" + "{broken\n" + validLine + "\n"
	_, aerr := decodeNDJSON([]byte(batch), DefaultLimits())
	if aerr == nil {
		t.Fatal("decodeNDJSON accepted a broken line")
	}
	if !strings.HasPrefix(aerr.Detail, "line 2: ") {
		t.Errorf("detail = %q, want a 'line 2: ' prefix", aerr.Detail)
	}
	// A per-line decoder over "{broken" alone reports byte 2; the batch-wide
	// decoder must rebase its absolute offset to say the same thing.
	if !strings.Contains(aerr.Detail, "invalid JSON at byte 2") {
		t.Errorf("detail = %q, want a line-relative byte offset (byte 2)", aerr.Detail)
	}
}

// TestDecodeNDJSONHigherVerIsAccepted pins the other half of §4.1: an unknown
// *type* is fatal, an unknown-but-higher *ver* is stored for the projector to
// skip.
func TestDecodeNDJSONHigherVerIsAccepted(t *testing.T) {
	body := strings.Replace(validLine, `"ver":1`, `"ver":7`, 1) + "\n"
	events, aerr := decodeNDJSON([]byte(body), DefaultLimits())
	if aerr != nil {
		t.Fatalf("a higher ver must be accepted and stored: %v", aerr)
	}
	if events[0].Ver != 7 {
		t.Errorf("ver = %d, want 7", events[0].Ver)
	}
}

// TestDecodeNDJSONSizeLimits pins the §4.3 caps that are not about the body.
func TestDecodeNDJSONSizeLimits(t *testing.T) {
	limits := DefaultLimits()

	t.Run("too many events", func(t *testing.T) {
		var b strings.Builder
		for range limits.MaxEvents + 1 {
			b.WriteString(validLine)
			b.WriteByte('\n')
		}
		_, aerr := decodeNDJSON([]byte(b.String()), limits)
		if aerr == nil || aerr.Code != authz.CodeTooLarge {
			t.Fatalf("got %v, want %s", aerr, authz.CodeTooLarge)
		}
	})

	t.Run("event line over 16 KiB", func(t *testing.T) {
		padded := strings.Replace(validLine, `"cause":"ground_impact"`,
			`"cause":"`+strings.Repeat("x", limits.MaxLineBytes)+`"`, 1)
		_, aerr := decodeNDJSON([]byte(padded+"\n"), limits)
		if aerr == nil || aerr.Code != authz.CodeMalformedBatch {
			t.Fatalf("got %v, want %s", aerr, authz.CodeMalformedBatch)
		}
	})

	t.Run("exactly at the event limit is fine", func(t *testing.T) {
		var b strings.Builder
		for range limits.MaxEvents {
			b.WriteString(validLine)
			b.WriteByte('\n')
		}
		events, aerr := decodeNDJSON([]byte(b.String()), limits)
		if aerr != nil {
			t.Fatalf("%d events must be accepted: %v", limits.MaxEvents, aerr)
		}
		if len(events) != limits.MaxEvents {
			t.Errorf("decoded %d events, want %d", len(events), limits.MaxEvents)
		}
	})
}

// TestDecompressCapIsEnforcedWhileReading is the decompression-bomb guard: an
// 8 MiB ceiling that never materializes the full expansion.
func TestDecompressCapIsEnforcedWhileReading(t *testing.T) {
	limits := DefaultLimits()

	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	// 32 MiB of zeros compresses to a few hundred bytes.
	chunk := make([]byte, 1<<20)
	for range 32 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	bomb := buf.Bytes()
	if int64(len(bomb)) > limits.MaxBodyBytes {
		t.Fatalf("the test bomb is %d bytes; it must fit the 1 MiB body cap to be interesting", len(bomb))
	}

	_, aerr := decompress(bomb, limits)
	if aerr == nil || aerr.Code != authz.CodeTooLarge {
		t.Fatalf("got %v, want %s", aerr, authz.CodeTooLarge)
	}

	t.Run("garbage is not brotli", func(t *testing.T) {
		_, aerr := decompress([]byte("this is not brotli at all, not even close"), limits)
		if aerr == nil || aerr.Code != authz.CodeMalformedBatch {
			t.Fatalf("got %v, want %s", aerr, authz.CodeMalformedBatch)
		}
	})
}

// TestReadBodyCap pins the compressed-body cap and, more importantly, that it
// is applied while reading rather than after.
func TestReadBodyCap(t *testing.T) {
	limits := Limits{MaxBodyBytes: 1024}

	body, aerr := readBody(bytes.NewReader(make([]byte, 1024)), limits)
	if aerr != nil {
		t.Fatalf("a body exactly at the cap must be accepted: %v", aerr)
	}
	if len(body) != 1024 {
		t.Errorf("read %d bytes, want 1024", len(body))
	}

	huge := &countingReader{n: 64 << 20}
	_, aerr = readBody(huge, limits)
	if aerr == nil || aerr.Code != authz.CodeTooLarge {
		t.Fatalf("got %v, want %s", aerr, authz.CodeTooLarge)
	}
	if huge.read > 4096 {
		t.Errorf("read %d bytes of a 64 MiB body; the cap must stop the read, not follow it", huge.read)
	}
}

// countingReader yields n zero bytes and records how many were consumed.
type countingReader struct {
	n    int
	read int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.read >= r.n {
		return 0, io.EOF
	}
	n := min(len(p), r.n-r.read)
	for i := range n {
		p[i] = 0
	}
	r.read += n
	return n, nil
}

// TestKnownTypesMatchTheTaxonomy guards the registry against drift: every type
// docs/events.md lists must be accepted, and nothing else.
func TestKnownTypesMatchTheTaxonomy(t *testing.T) {
	if len(KnownTypes()) != 23 {
		t.Errorf("registry holds %d types, want the 23 of §4.2", len(KnownTypes()))
	}
	for _, want := range []string{"vehicle.rud", "vehicle.landed", "telemetry.window", "flight.flagged", "roster.snapshot"} {
		if !KnownType(want) {
			t.Errorf("%q is missing from the registry", want)
		}
	}
	for _, bad := range []string{"", "Vehicle.Rud", "vehicle.rud ", "vehicle.unknown"} {
		if KnownType(bad) {
			t.Errorf("%q must not be a known type", bad)
		}
	}
}
