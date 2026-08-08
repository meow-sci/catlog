// Package ingest is the POST /v1/ingest pipeline: capped body reads, brotli
// decompression, NDJSON envelope validation and the single-writer store path
// (§4.3, §4.4, §5.5).
//
// # Shape of a request
//
// The handler runs §4.5.3 steps 1–10 inline and writes nothing:
//
//	Content-Encoding: br?          → 415 unsupported_encoding
//	authz.Verify (steps 1–9)       → 401/429 per §4.9
//	read body under the §4.3 caps  → 413 too_large
//	bh == sha256(body) (step 10)   → 401 proof_invalid
//	brotli + NDJSON + envelopes    → 400 malformed_batch / 413 too_large
//
// It then hands a [WriteJob] to the bounded queue and waits. The single writer
// goroutine owns steps 11–13 — replay short-circuit, stream chain, and the one
// transaction that inserts the events, upserts stream_state and records the
// batch. A full queue is 503 + Retry-After: 5.
//
// Nothing in the handler touches the database, which is what keeps the cost of
// a hostile request bounded by two ECDSA verifications and one megabyte of
// reading.
package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
)

// Path is the ingest route (§4.4).
const Path = "/v1/ingest"

// HandlerTimeout bounds one request end to end (§5.5).
const HandlerTimeout = 30 * time.Second

// BusyRetryAfter is the Retry-After sent when the write queue is full (§5.5).
const BusyRetryAfter = 5

// ContentEncoding is the only accepted body encoding (§4.3).
const ContentEncoding = "br"

// Handler serves POST /v1/ingest.
type Handler struct {
	verifier *authz.Verifier
	writer   *Writer
	limits   Limits
	log      *slog.Logger
	rejects  *rejectLogger
	timeout  time.Duration
	// inflight is the [Limits.MaxInFlight] semaphore: a slot is held from just
	// before the body is read until the response is written, which is the span
	// where a request owns up to 9 MiB of buffers. Without it, peak ingest
	// memory scales with however many connections nginx lets through.
	inflight chan struct{}
}

// NewHandler wires the verification chain to the write queue.
func NewHandler(v *authz.Verifier, w *Writer, limits Limits, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	limits = limits.withDefaults()
	return &Handler{
		verifier: v,
		writer:   w,
		limits:   limits,
		log:      log,
		rejects:  newRejectLogger(v.Now),
		timeout:  HandlerTimeout,
		inflight: make(chan struct{}, limits.MaxInFlight),
	}
}

// Register mounts the handler on a mux (§4.4: POST only).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST "+Path, h)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := h.verifier.Now()
	// §4.4: the Date header is always present — the mod syncs its clock from it,
	// which is how a client recovers from a clock_skew rejection. Setting it
	// explicitly (rather than leaving it to net/http) means it is present on
	// every path, including the ones that never reach a real response writer.
	w.Header().Set("Date", now.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "no-store")

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	// Step 0: the body encoding. Cheapest possible check, and a mod that ships
	// uncompressed bodies must find out before it burns a rate-limit token.
	if !hasBrotli(r.Header.Get("Content-Encoding")) {
		h.reject(w, r, "", &authz.Error{
			Code:   authz.CodeUnsupportedEncoding,
			Detail: "POST " + Path + " requires Content-Encoding: " + ContentEncoding,
		}, now)
		return
	}
	// A declared length over the cap is refused before a byte is read.
	if r.ContentLength > h.limits.MaxBodyBytes {
		h.reject(w, r, "", &authz.Error{
			Code: authz.CodeTooLarge, Step: 10,
			Detail: "Content-Length exceeds " + strconv.FormatInt(h.limits.MaxBodyBytes, 10) + " bytes",
		}, now)
		return
	}

	// Steps 1–9.
	res, aerr := h.verifier.Verify(ctx, authz.Request{
		License: r.Header.Get("X-Catlog-License"),
		Proof:   r.Header.Get("X-Catlog-Proof"),
	})
	if aerr != nil {
		h.reject(w, r, "", aerr, now)
		return
	}

	// The in-flight gate, taken before a byte of body is read: everything past
	// this point holds real memory. Non-blocking on purpose — a queue of
	// waiting-to-read requests would just be the memory problem wearing a
	// different hat, and the mod already knows what a 503 + Retry-After means.
	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		h.rejectStatus(w, r, res.JKT, http.StatusServiceUnavailable, &authz.Error{
			Code: authz.CodeInternal, Step: 10, Detail: "too many batches in flight", RetryAfter: BusyRetryAfter,
		}, now)
		return
	}

	// Step 10: read the body under the §4.3 caps, then match it against `bh`.
	body, aerr := readBody(r.Body, h.limits)
	if aerr != nil {
		h.reject(w, r, res.JKT, aerr, now)
		return
	}
	if aerr := res.CheckBodyHash(body); aerr != nil {
		h.reject(w, r, res.JKT, aerr, now)
		return
	}

	// The CPU half of step 13: decompress, parse, validate. No database yet.
	raw, aerr := decompress(body, h.limits)
	if aerr != nil {
		h.reject(w, r, res.JKT, aerr, now)
		return
	}
	events, aerr := decodeNDJSON(raw, h.limits)
	if aerr != nil {
		h.reject(w, r, res.JKT, aerr, now)
		return
	}

	// Steps 11–13 belong to the writer.
	job := &WriteJob{
		PlayerID: res.PlayerID,
		JKT:      res.JKT,
		BatchID:  res.Proof.BatchID(),
		SID:      res.SID(),
		Seq:      res.Seq(),
		BH:       res.Proof.BH,
		PH:       res.Proof.PH,
		Events:   events,
	}
	if err := h.writer.Submit(job); err != nil {
		// Backpressure, not failure: the queue is bounded on purpose (§5.5).
		h.rejectStatus(w, r, res.JKT, http.StatusServiceUnavailable, &authz.Error{
			Code: authz.CodeInternal, Step: 13, Detail: "ingest queue is full", RetryAfter: BusyRetryAfter,
		}, now)
		return
	}

	result, err := h.writer.Await(ctx, job)
	if err != nil {
		// The write may still land; the client's retry is idempotent (§4.5.3
		// step 11 plus the (player, event_id) dedup index), so a 503 here is
		// safe as well as honest.
		h.rejectStatus(w, r, res.JKT, http.StatusServiceUnavailable, &authz.Error{
			Code: authz.CodeInternal, Step: 13, Detail: "the write did not complete in time", RetryAfter: BusyRetryAfter,
		}, now)
		return
	}
	if result.Err != nil {
		h.reject(w, r, res.JKT, result.Err, now)
		return
	}

	writeJSON(w, http.StatusOK, response{
		Accepted: result.Accepted,
		Deduped:  result.Deduped,
		Replay:   result.Replay,
	})
}

// response is the §4.4 success body. `replay` is omitted unless true, so the
// ordinary case is exactly `{"accepted": n, "deduped": n}`.
type response struct {
	Accepted int  `json:"accepted"`
	Deduped  int  `json:"deduped"`
	Replay   bool `json:"replay,omitempty"`
}

// errorBody is the §4.9 JSON shape.
type errorBody struct {
	Error      string `json:"error"`
	Detail     string `json:"detail,omitempty"`
	ServerTime int64  `json:"server_time,omitempty"`
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request, jkt string, aerr *authz.Error, now time.Time) {
	h.rejectStatus(w, r, jkt, aerr.Status(), aerr, now)
}

func (h *Handler) rejectStatus(w http.ResponseWriter, r *http.Request, jkt string, status int, aerr *authz.Error, now time.Time) {
	countRejection(aerr.Code)

	// §5.11: one WARN line per rejection, rate limited to 1/min per credential
	// so a hostile client cannot flood the log. Never the license, the proof or
	// any key material — the code, the thumbprint prefix, the peer and a short
	// detail are enough to diagnose one.
	key := jkt
	if key == "" {
		key = clientIP(r)
	}
	if h.rejects.allow(key) {
		attrs := []any{"code", aerr.Code, "ip", clientIP(r), "detail", aerr.Detail}
		if aerr.Step > 0 {
			attrs = append(attrs, "step", aerr.Step)
		}
		if jkt != "" {
			attrs = append(attrs, "jkt", jktPrefix(jkt))
		}
		if err := aerr.Unwrap(); err != nil {
			attrs = append(attrs, "err", err)
		}
		h.log.Warn("ingest rejected", attrs...)
	}

	if aerr.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(aerr.RetryAfter))
	}
	body := errorBody{Error: aerr.Code, Detail: aerr.Detail}
	if status == http.StatusUnauthorized {
		// §4.4: an auth failure carries the server clock so the mod can correct
		// its offset — clock_skew needs it, and the others cost nothing.
		body.ServerTime = now.UnixMilli()
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("ingest response write failed", "err", err)
	}
}

// hasBrotli reports whether the Content-Encoding header names brotli and
// nothing else. Identity, gzip and stacked encodings are all rejected: §4.3
// specifies exactly one wire format.
func hasBrotli(header string) bool {
	return strings.EqualFold(strings.TrimSpace(header), ContentEncoding)
}

// clientIP is the peer address for the log line. It is deliberately
// RemoteAddr and not X-Forwarded-For: catlogd binds loopback behind nginx
// (§6), so the header is attacker-controlled unless the proxy is known to set
// it. WP9 revisits this when nginx lands.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// jktPrefix truncates a thumbprint for logging: enough to correlate lines, not
// enough to be a credential identifier in a log aggregator (§5.11).
func jktPrefix(jkt string) string {
	if len(jkt) <= 8 {
		return jkt
	}
	return jkt[:8]
}

// rejectLogger throttles rejection logging to one line per key per minute
// (§5.11).
type rejectLogger struct {
	mu    sync.Mutex
	last  map[string]time.Time
	every time.Duration
	now   func() time.Time
}

// maxRejectKeys bounds the throttle map. Hitting it drops every expired entry,
// which is lossless: an expired entry permits the next line anyway.
const maxRejectKeys = 10_000

func newRejectLogger(now func() time.Time) *rejectLogger {
	if now == nil {
		now = time.Now
	}
	return &rejectLogger{last: map[string]time.Time{}, every: time.Minute, now: now}
}

func (l *rejectLogger) allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if last, ok := l.last[key]; ok && now.Sub(last) < l.every {
		return false
	}
	if len(l.last) >= maxRejectKeys {
		for k, t := range l.last {
			if now.Sub(t) >= l.every {
				delete(l.last, k)
			}
		}
	}
	l.last[key] = now
	return true
}
