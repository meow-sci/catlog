package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

const (
	testIssuer = "http://127.0.0.1:8080"
	testHTU    = "http://127.0.0.1:8080/v1/ingest"
)

// rig is a live ingest endpoint over a real events database: verifier, bounded
// queue, writer goroutine and an httptest server.
type rig struct {
	t       *testing.T
	events  *store.Events
	keys    *keys.Set
	v       *authz.Verifier
	w       *Writer
	h       *Handler
	srv     *httptest.Server
	cred    testutil.Cred
	now     time.Time
	advance time.Duration // clock movement per shipped batch, to refill the bucket
	sid     ids.ID
	seq     int64
	prev    []byte
}

func newRig(t *testing.T) *rig { return newRigWith(t, true) }

// newRigWith optionally leaves the writer goroutine unstarted, which is how the
// backpressure and timeout tests get a queue that nothing drains.
func newRigWith(t *testing.T, runWriter bool) *rig {
	t.Helper()

	// File-backed, not in-memory: the writer runs a transaction on the writer
	// handle while requests read on the reader handle, and only a real file has
	// both (§5.4).
	events := testutil.Events(t)
	ks := testutil.Keys(t)
	now := time.Unix(1_770_000_000, 0).UTC()

	r := &rig{
		t: t, events: events, keys: ks, now: now,
		advance: 2 * time.Second, // one token per batch at 0.5/s (§4.3)
		seq:     1,
	}
	r.cred = testutil.CredentialAt(t, events, ks, testIssuer, "whiskers_prime", now.Add(-time.Hour), 180*24*time.Hour)
	r.sid = testutil.ULID(t)

	r.v = authz.New(authz.Config{
		Issuer:        testIssuer,
		AcceptedHTU:   []string{testHTU},
		RatePerSecond: 0.5,
		Burst:         5,
	}, ks, events, authz.NewDenyList())
	r.v.SetClock(func() time.Time { return r.now })

	r.w = NewWriter(events, testutil.DiscardLogger())
	if runWriter {
		ctx, cancel := context.WithCancel(context.Background())
		go r.w.Run(ctx)
		t.Cleanup(func() { cancel(); r.w.Wait() })
	}

	r.h = NewHandler(r.v, r.w, DefaultLimits(), testutil.DiscardLogger())

	mux := http.NewServeMux()
	r.h.Register(mux)
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)

	return r
}

// batch renders n distinct events as an NDJSON batch.
func (r *rig) batch(n int) []byte {
	r.t.Helper()
	var b strings.Builder
	for range n {
		fmt.Fprintf(&b, `{"id":%q,"type":"vehicle.rud","ver":1,"flight":%q,"session":%q,"career":"0123456789abcdef","sim_t":%d.5,"wall_t":%d,"payload":{"cause":"ground_impact","speed_ms":%d}}`+"\n",
			ids.String(testutil.ULID(r.t)), ids.String(r.sid), ids.String(r.sid), n, r.now.UnixMilli(), n*7)
	}
	return []byte(b.String())
}

// shipOpts tweaks one request away from the happy path.
type shipOpts struct {
	License         string
	Proof           string
	ContentEncoding *string
	Body            []byte
	Seq             int64
	SID             ids.ID
	NoPH            bool
	JTI             string
	SkipCompression bool
	// As ships under a different credential — a second player on the same
	// server. Its own stream is tracked in asSID/asSeq/asPrev.
	As     *testutil.Cred
	AsSID  ids.ID
	AsSeq  int64
	AsPrev []byte
}

// otherPlayer mints a second credential backed by its own player row, so a test
// can prove that identity is per-credential and not global.
func (r *rig) otherPlayer(handle string) testutil.Cred {
	r.t.Helper()
	return testutil.CredentialAt(r.t, r.events, r.keys, testIssuer, handle, r.now.Add(-time.Hour), 180*24*time.Hour)
}

// ship posts one batch and returns the response with its decoded body.
func (r *rig) ship(ndjson []byte, opts ...func(*shipOpts)) (*http.Response, map[string]any) {
	r.t.Helper()

	var o shipOpts
	for _, fn := range opts {
		fn(&o)
	}

	body := o.Body
	if body == nil {
		if o.SkipCompression {
			body = ndjson
		} else {
			body = testutil.Brotli(r.t, ndjson)
		}
	}

	// The shipping identity: the rig's own credential and stream, or the second
	// player the caller named.
	cred, streamSeq, streamSID, streamPrev := r.cred, r.seq, r.sid, r.prev
	if o.As != nil {
		cred, streamSeq, streamSID, streamPrev = *o.As, o.AsSeq, o.AsSID, o.AsPrev
		if streamSeq == 0 {
			streamSeq = 1
		}
	}

	seq := o.Seq
	if seq == 0 {
		seq = streamSeq
	}
	sid := o.SID
	if sid == ids.Zero {
		sid = streamSID
	}
	prev := streamPrev
	if o.NoPH || seq == 1 {
		prev = nil
	}

	proof := o.Proof
	if proof == "" {
		proof = cred.Proof(r.t, testutil.ProofOpts{
			HTU: testHTU, At: r.now, SID: sid, Seq: seq, Body: body, PrevBody: prev, JTI: o.JTI,
		})
	}
	license := o.License
	if license == "" {
		license = cred.License
	}

	req, err := http.NewRequest(http.MethodPost, r.srv.URL+Path, bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	enc := ContentEncoding
	if o.ContentEncoding != nil {
		enc = *o.ContentEncoding
	}
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	req.Header.Set("X-Catlog-License", license)
	req.Header.Set("X-Catlog-Proof", proof)

	res, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatalf("POST %s: %v", Path, err)
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		r.t.Fatalf("read response: %v", err)
	}

	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			r.t.Fatalf("response is not JSON (%d): %q", res.StatusCode, raw)
		}
	}

	// A batch the server accepted advances the local chain the way the mod's
	// shipper would. Only the rig's own stream is tracked; a second player's
	// chain is the caller's to carry in shipOpts.
	if res.StatusCode == http.StatusOK && decoded["replay"] != true && o.As == nil {
		r.prev = body
		r.seq = seq + 1
	}
	r.now = r.now.Add(r.advance)
	return res, decoded
}

func wantStatus(t *testing.T, res *http.Response, body map[string]any, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %v)", res.StatusCode, status, body)
	}
	if res.Header.Get("Date") == "" {
		t.Error("the Date header is missing; §4.4 requires it on every response")
	}
}

func wantError(t *testing.T, res *http.Response, body map[string]any, status int, code string) {
	t.Helper()
	wantStatus(t, res, body, status)
	if got, _ := body["error"].(string); got != code {
		t.Errorf("error = %v, want %q", body["error"], code)
	}
	if status == http.StatusUnauthorized {
		if _, ok := body["server_time"]; !ok {
			t.Error("a 401 must carry server_time so the mod can correct its clock (§4.4)")
		}
	}
}

// TestIngestHappyPath is the §4.4 200 contract, end to end through the writer.
func TestIngestHappyPath(t *testing.T) {
	r := newRig(t)

	res, body := r.ship(r.batch(3))
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(3) || body["deduped"] != float64(0) {
		t.Fatalf("body = %v, want accepted 3 deduped 0", body)
	}
	if _, ok := body["replay"]; ok {
		t.Error("replay must be omitted on an ordinary 200")
	}

	rows, err := r.events.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("stored %d rows, want 3", len(rows))
	}
	for _, row := range rows {
		if row.PlayerID != r.cred.PlayerID {
			t.Errorf("row player = %d, want %d", row.PlayerID, r.cred.PlayerID)
		}
		if row.Type != "vehicle.rud" || row.RecvTime == 0 {
			t.Errorf("row = %+v", row)
		}
	}

	// The second batch chains onto the first.
	res, body = r.ship(r.batch(2))
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(2) {
		t.Fatalf("body = %v, want accepted 2", body)
	}

	state, found, err := r.events.StreamState(t.Context(), nil, r.cred.PlayerID, r.sid)
	if err != nil || !found {
		t.Fatalf("stream state: found=%v err=%v", found, err)
	}
	if state.LastSeq != 2 {
		t.Errorf("last_seq = %d, want 2", state.LastSeq)
	}
	if state.Gap {
		t.Error("gap marker set on a contiguous stream")
	}
	if state.JKT != r.cred.JKT {
		t.Errorf("stream jkt = %q, want %q", state.JKT, r.cred.JKT)
	}
}

// TestIngestDedupAndReplay covers the two idempotency paths of §4.4/§4.5.3: the
// same events under a new batch id are deduped row by row, and the same batch
// id short-circuits entirely.
func TestIngestDedupAndReplay(t *testing.T) {
	r := newRig(t)

	batch := r.batch(4)
	compressed := testutil.Brotli(t, batch)
	jti := ids.String(testutil.ULID(t))

	res, body := r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti })
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(4) {
		t.Fatalf("first ship: %v", body)
	}

	// Step 11: identical batch id → whole-batch replay, nothing re-read.
	res, body = r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti; o.Seq = 1 })
	wantStatus(t, res, body, http.StatusOK)
	if body["replay"] != true || body["accepted"] != float64(0) || body["deduped"] != float64(4) {
		t.Fatalf("replay response = %v, want {accepted:0, deduped:4, replay:true}", body)
	}

	// Same events, new batch id, next seq → the dedup index catches them.
	res, body = r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.Seq = 2 })
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(0) || body["deduped"] != float64(4) {
		t.Fatalf("re-ship response = %v, want {accepted:0, deduped:4}", body)
	}

	n, err := r.events.CountEvents(t.Context(), r.cred.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("stored %d events, want 4 — dedup is a union merge (D19)", n)
	}
}

// TestIngestStreamRules walks §4.5.3 step 12: fork on a reused seq, fork on a
// broken chain, fork on a first batch that is not seq 1, and a tolerated gap.
func TestIngestStreamRules(t *testing.T) {
	t.Run("first batch must be seq 1 with no ph", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.Seq = 2 })
		wantError(t, res, body, http.StatusConflict, authz.CodeStreamFork)
	})

	t.Run("replayed seq forks", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1))
		wantStatus(t, res, body, http.StatusOK)

		res, body = r.ship(r.batch(1), func(o *shipOpts) { o.Seq = 1 })
		wantError(t, res, body, http.StatusConflict, authz.CodeStreamFork)
	})

	t.Run("wrong ph forks", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1))
		wantStatus(t, res, body, http.StatusOK)

		// seq 2 with no ph at all: the chain cannot be checked, so it is a fork.
		res, body = r.ship(r.batch(1), func(o *shipOpts) { o.Seq = 2; o.NoPH = true })
		wantError(t, res, body, http.StatusConflict, authz.CodeStreamFork)
	})

	t.Run("a gap is accepted and marked", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1))
		wantStatus(t, res, body, http.StatusOK)

		// seq 5 after seq 1: batches were lost. Telemetry is loss-tolerant, so
		// this is stored with a forensic marker rather than refused (§4.5.3).
		res, body = r.ship(r.batch(2), func(o *shipOpts) { o.Seq = 5 })
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(2) {
			t.Fatalf("body = %v, want accepted 2", body)
		}

		state, found, err := r.events.StreamState(t.Context(), nil, r.cred.PlayerID, r.sid)
		if err != nil || !found {
			t.Fatalf("stream state: found=%v err=%v", found, err)
		}
		if !state.Gap {
			t.Error("gap marker not set after a skipped seq")
		}
		if state.LastSeq != 5 {
			t.Errorf("last_seq = %d, want 5", state.LastSeq)
		}

		// The marker is sticky: a later contiguous batch must not clear it.
		res, body = r.ship(r.batch(1), func(o *shipOpts) { o.Seq = 6 })
		wantStatus(t, res, body, http.StatusOK)
		state, _, err = r.events.StreamState(t.Context(), nil, r.cred.PlayerID, r.sid)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Gap {
			t.Error("the gap marker must be sticky")
		}
	})

	t.Run("a new stream starts clean", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1))
		wantStatus(t, res, body, http.StatusOK)

		// This is the mod's 409 recovery: mint a new sid, reset seq to 1.
		fresh := testutil.ULID(t)
		res, body = r.ship(r.batch(1), func(o *shipOpts) { o.SID = fresh; o.Seq = 1 })
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(1) {
			t.Fatalf("body = %v", body)
		}
	})
}

// TestIngestHTTPRejections pins the §4.4 status table for everything the
// handler decides itself.
func TestIngestHTTPRejections(t *testing.T) {
	t.Run("missing Content-Encoding", func(t *testing.T) {
		r := newRig(t)
		none := ""
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.ContentEncoding = &none })
		wantError(t, res, body, http.StatusUnsupportedMediaType, authz.CodeUnsupportedEncoding)
	})

	t.Run("wrong Content-Encoding", func(t *testing.T) {
		r := newRig(t)
		gzip := "gzip"
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.ContentEncoding = &gzip })
		wantError(t, res, body, http.StatusUnsupportedMediaType, authz.CodeUnsupportedEncoding)
	})

	t.Run("body over 1 MiB", func(t *testing.T) {
		r := newRig(t)
		// Random-ish incompressible bytes, so the compressed body really is over
		// the cap.
		big := make([]byte, r.h.limits.MaxBodyBytes+1)
		for i := range big {
			big[i] = byte(i * 31)
		}
		res, body := r.ship(nil, func(o *shipOpts) { o.Body = big })
		wantError(t, res, body, http.StatusRequestEntityTooLarge, authz.CodeTooLarge)
	})

	t.Run("body is not brotli", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.SkipCompression = true })
		wantError(t, res, body, http.StatusBadRequest, authz.CodeMalformedBatch)
	})

	t.Run("unknown event type", func(t *testing.T) {
		r := newRig(t)
		bad := bytes.Replace(r.batch(1), []byte("vehicle.rud"), []byte("vehicle.warp"), 1)
		res, body := r.ship(bad)
		wantError(t, res, body, http.StatusBadRequest, authz.CodeMalformedBatch)
		if detail, _ := body["detail"].(string); !strings.Contains(detail, "vehicle.warp") {
			t.Errorf("detail = %q, want it to name the unknown type", detail)
		}
	})

	t.Run("body hash does not match the proof", func(t *testing.T) {
		r := newRig(t)
		signed := testutil.Brotli(t, r.batch(1))
		sent := testutil.Brotli(t, r.batch(2))
		proof := r.cred.Proof(t, testutil.ProofOpts{HTU: testHTU, At: r.now, SID: r.sid, Seq: 1, Body: signed})
		res, body := r.ship(nil, func(o *shipOpts) { o.Body = sent; o.Proof = proof })
		wantError(t, res, body, http.StatusUnauthorized, authz.CodeProofInvalid)
	})

	t.Run("clock skew", func(t *testing.T) {
		r := newRig(t)
		batch := testutil.Brotli(t, r.batch(1))
		proof := r.cred.Proof(t, testutil.ProofOpts{
			HTU: testHTU, At: r.now.Add(-10 * time.Minute), SID: r.sid, Seq: 1, Body: batch,
		})
		res, body := r.ship(nil, func(o *shipOpts) { o.Body = batch; o.Proof = proof })
		wantError(t, res, body, http.StatusUnauthorized, authz.CodeClockSkew)

		// The mod recovers by re-reading Date, so the value has to be usable.
		if _, err := http.ParseTime(res.Header.Get("Date")); err != nil {
			t.Errorf("Date header %q is unparseable: %v", res.Header.Get("Date"), err)
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		r := newRig(t)
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.License = "none" })
		wantError(t, res, body, http.StatusUnauthorized, authz.CodeLicenseInvalid)
	})

	t.Run("rate limited", func(t *testing.T) {
		r := newRig(t)
		r.advance = 0 // freeze the clock: the bucket cannot refill

		for i := range 5 {
			res, body := r.ship(r.batch(1), func(o *shipOpts) { o.Seq = int64(i + 1) })
			wantStatus(t, res, body, http.StatusOK)
		}
		res, body := r.ship(r.batch(1), func(o *shipOpts) { o.Seq = 6 })
		wantError(t, res, body, http.StatusTooManyRequests, authz.CodeRateLimited)
		if res.Header.Get("Retry-After") == "" {
			t.Error("a 429 must carry Retry-After (§4.4)")
		}
	})
}

// TestIngestBackpressure covers the §5.5 bounded queue: a full queue answers
// 503 + Retry-After: 5 rather than growing memory.
func TestIngestBackpressure(t *testing.T) {
	r := newRigWith(t, false) // no writer goroutine: what goes in stays in

	// Fill the queue. Nothing consumes these; the point is that the 257th job
	// has nowhere to go.
	for range QueueDepth {
		select {
		case r.w.jobs <- &WriteJob{reply: make(chan WriteResult, 1)}:
		default:
			t.Fatalf("the queue filled early at depth %d, want %d", r.w.QueueDepth(), QueueDepth)
		}
	}
	if r.w.QueueDepth() != QueueDepth {
		t.Fatalf("queue depth = %d, want %d", r.w.QueueDepth(), QueueDepth)
	}

	res, body := r.ship(r.batch(1))
	wantStatus(t, res, body, http.StatusServiceUnavailable)
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want \"5\" (§5.5)", got)
	}

	if err := r.w.Submit(&WriteJob{}); err == nil {
		t.Error("Submit on a full queue must return ErrBusy")
	}
}

// TestIngestInFlightCap covers the [Limits.MaxInFlight] gate: a request that
// arrives while every slot is holding a body is answered 503 + Retry-After
// before it reads a byte, exactly like a full write queue.
func TestIngestInFlightCap(t *testing.T) {
	r := newRig(t)

	// Occupy every slot. The gate sits between authz and the body read, so a
	// full channel is indistinguishable from that many requests mid-body.
	for range cap(r.h.inflight) {
		r.h.inflight <- struct{}{}
	}

	res, body := r.ship(r.batch(1))
	wantStatus(t, res, body, http.StatusServiceUnavailable)
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want \"5\"", got)
	}

	// Freeing one slot makes the same request land.
	<-r.h.inflight
	res, body = r.ship(r.batch(1))
	wantStatus(t, res, body, http.StatusOK)
}

// TestIngestWriterTimeout proves the handler never blocks forever on a stalled
// writer: it gives up on its own deadline and reports backpressure.
func TestIngestWriterTimeout(t *testing.T) {
	r := newRigWith(t, false) // the job is queued and never processed
	r.h.timeout = 150 * time.Millisecond

	start := time.Now()
	res, body := r.ship(r.batch(1))
	wantStatus(t, res, body, http.StatusServiceUnavailable)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the handler waited %s; its deadline is %s", elapsed, r.h.timeout)
	}
}

// TestRejectionsAreLoggedOncePerMinute pins §5.11: a hostile client cannot
// flood the log, and the line never carries credential contents.
func TestRejectionsAreLoggedOncePerMinute(t *testing.T) {
	now := time.Unix(1_770_000_000, 0)
	l := newRejectLogger(func() time.Time { return now })

	if !l.allow("jkt-a") {
		t.Fatal("the first rejection must be logged")
	}
	for range 100 {
		if l.allow("jkt-a") {
			t.Fatal("a second line was emitted within the minute")
		}
	}
	if !l.allow("jkt-b") {
		t.Error("a different credential must get its own line")
	}

	now = now.Add(time.Minute)
	if !l.allow("jkt-a") {
		t.Error("a line must be allowed again after a minute")
	}
}

// TestRejectionLogHasNoSecrets checks the shape of the WARN line itself: code,
// ip, detail, a truncated jkt — and never the license or proof (§5.11).
func TestRejectionLogHasNoSecrets(t *testing.T) {
	r := newRig(t)

	var buf bytes.Buffer
	r.h.log = slogJSON(&buf)

	batch := testutil.Brotli(t, r.batch(1))
	proof := r.cred.Proof(t, testutil.ProofOpts{
		HTU: testHTU, At: r.now.Add(-time.Hour), SID: r.sid, Seq: 1, Body: batch,
	})
	res, body := r.ship(nil, func(o *shipOpts) { o.Body = batch; o.Proof = proof })
	wantError(t, res, body, http.StatusUnauthorized, authz.CodeClockSkew)

	logged := buf.String()
	if !strings.Contains(logged, authz.CodeClockSkew) {
		t.Errorf("the rejection was not logged: %q", logged)
	}
	if strings.Contains(logged, proof) || strings.Contains(logged, r.cred.License) {
		t.Error("the log line carries the proof or the license verbatim (§5.11)")
	}
	if strings.Contains(logged, r.cred.JKT) {
		t.Error("the log line carries the full jkt; it must be truncated (§5.11)")
	}
	if !strings.Contains(logged, `"ip":`) {
		t.Errorf("the log line has no ip field: %q", logged)
	}
}

// slogJSON is a JSON logger over a buffer, so a test can read what was logged.
func slogJSON(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestExpvarCountersMove checks the §5.9 counters are wired to real outcomes.
func TestExpvarCountersMove(t *testing.T) {
	r := newRig(t)

	before := metricAccepted.Value()
	beforeRejected := metricRejected[authz.CodeUnsupportedEncoding].Value()

	res, body := r.ship(r.batch(3))
	wantStatus(t, res, body, http.StatusOK)
	if got := metricAccepted.Value() - before; got != 3 {
		t.Errorf("ingest_accepted moved by %d, want 3", got)
	}

	none := ""
	res, body = r.ship(r.batch(1), func(o *shipOpts) { o.ContentEncoding = &none })
	wantError(t, res, body, http.StatusUnsupportedMediaType, authz.CodeUnsupportedEncoding)
	if got := metricRejected[authz.CodeUnsupportedEncoding].Value() - beforeRejected; got != 1 {
		t.Errorf("ingest_rejected_unsupported_encoding moved by %d, want 1", got)
	}
}
