package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The live half of the raw event log: `GET /v1/events/stream`.
//
// # One log, two transports
//
// `GET /v1/events` is the snapshot half; this is the stream. The same pairing —
// and the same reconnect rule — as `/v1/feed` + `/v1/feed/stream`: the stream
// never replays history, a reconnecting client re-reads the paginated view.
// Each frame's `id:` is the row's seq, which is also [EventRow.Seq] on the
// paginated rows, so the two halves merge by one number.
//
// # Where the rows come from, and why the stream cannot lie by omission
//
// The projector publishes every stored event its incremental batch read,
// post-commit, in seq order — including events this build cannot decode, which
// the folds skip (§4.1) but the log contains. See projector.Options.Raw for the
// contract, including why a rebuild streams nothing.
//
// # The privacy boundary lives here, not at the socket
//
// Rendering happens once per row, in the hub: the payload through [Redact] and
// the career through [Label], salted with that row's player id; rows of
// handle-less players (banned, purged, unclaimed) and rows in flagged flights
// are dropped before a frame exists. A handler only ever writes bytes the hub
// already made safe, so no filter, reconnect or error path can reach an
// unredacted payload.

// RawEvents is the raw event broadcaster. *projector.RawBroadcaster implements
// it; declared here, like [Feed], so this package depends on nothing above the
// store layer.
type RawEvents interface {
	Subscribe() (<-chan []store.StoredEvent, func())
}

// DefaultMaxStreamClients is the per-route cap on concurrent SSE subscribers
// when [Deps.MaxStreamClients] is zero. Each stream client holds a connection,
// a goroutine and a frame buffer for as long as its tab lives; 64 is far above
// any plausible audience for a hobby leaderboard while keeping the worst case
// a bounded number rather than a memory bill (Constitution §2). Production
// nginx additionally holds `limit_conn 20` per IP in front of this.
const DefaultMaxStreamClients = 64

// rawEventName is the SSE `event:` of every data frame on the stream.
const rawEventName = "raw"

// rawFrame is one rendered stream event: the shared SSE bytes, plus the two
// fields the per-subscriber filters match on — kept beside the bytes so a
// filtered subscriber costs two string compares per event, not a decode.
type rawFrame struct {
	typ    string
	handle string
	bytes  []byte
}

// eventsHub is [feedHub] for the raw event stream: one upstream subscription,
// taken when the first client arrives and cancelled when the last one leaves;
// each row redacted, marshalled and framed once per broadcast; identical bytes
// fanned out to every subscriber.
//
// Unlike the feed hub it does not forward every row it is handed: rendering is
// also where the drop rules above are applied, so what subscribers share is
// the already-public view of the batch.
type eventsHub struct {
	srv *Server
	raw RawEvents
	log *slog.Logger
	cap int

	mu   sync.Mutex
	next int64
	subs map[int64]chan []rawFrame
	stop func()
}

func newEventsHub(srv *Server, raw RawEvents, log *slog.Logger, cap int) *eventsHub {
	return &eventsHub{srv: srv, raw: raw, log: log, cap: cap, subs: map[int64]chan []rawFrame{}}
}

// subscribe registers a stream client. ok is false at the cap, for the same
// reason as [feedHub.subscribe].
func (h *eventsHub) subscribe() (frames <-chan []rawFrame, cancel func(), ok bool) {
	ch := make(chan []rawFrame, 8)

	h.mu.Lock()
	if len(h.subs) >= h.cap {
		h.mu.Unlock()
		return nil, nil, false
	}
	id := h.next
	h.next++
	h.subs[id] = ch
	if h.stop == nil {
		rows, cancel := h.raw.Subscribe()
		h.stop = cancel
		go h.run(rows)
	}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			if len(h.subs) == 0 && h.stop != nil {
				h.stop()
				h.stop = nil
			}
			h.mu.Unlock()
			close(ch)
		})
	}, true
}

// run renders each upstream batch once and fans the frames out. It exits when
// the upstream subscription is cancelled, which closes rows.
func (h *eventsHub) run(rows <-chan []store.StoredEvent) {
	for batch := range rows {
		frames := h.render(batch)
		if len(frames) == 0 {
			continue
		}
		h.mu.Lock()
		for _, ch := range h.subs {
			select {
			case ch <- frames:
			default:
				// Dropped on purpose — same rule as the broadcaster.
			}
		}
		h.mu.Unlock()
	}
}

// PublicEvents renders one committed batch as its public rows: rows of
// handle-less players (banned, purged, unclaimed) and rows in flagged flights
// are dropped, and each surviving row is redacted keyed by its own player id —
// exactly what the paginated views publish, because it is the same [eventRow].
//
// It exists so package web's datastar stream can consume the raw broadcaster
// without owning a second copy of the drop and redaction rules: this hub's own
// render step goes through it too, so the two streams cannot disagree about
// what is public. An error means the flag lookup failed; the caller must
// publish none of the batch (fail closed — a batch whose flags cannot be read
// might contain a flagged flight).
func (s *Server) PublicEvents(ctx context.Context, batch []store.StoredEvent) ([]EventRow, error) {
	flagged, err := s.flaggedFlights(ctx, batch)
	if err != nil {
		return nil, err
	}
	rows := make([]EventRow, 0, len(batch))
	for _, ev := range batch {
		handle, ok := s.deps.Directory.Handle(ev.PlayerID)
		if !ok || flagged[ev.FlightID] {
			continue
		}
		rows = append(rows, eventRow(ev, handle))
	}
	return rows, nil
}

// render turns one committed batch into its public frames: [Server.PublicEvents]
// for the drop and redaction rules (the flagged-flight set resolved once for
// the whole batch, the same one-IN-query shape the paginated scan uses), then
// one marshal and one SSE framing per surviving row.
func (h *eventsHub) render(batch []store.StoredEvent) []rawFrame {
	// Not a request context: the hub outlives any one subscriber. The read is
	// one indexed IN-query against the live projections.
	rows, err := h.srv.PublicEvents(context.Background(), batch)
	if err != nil {
		// Fail closed: see PublicEvents. The paginated view keeps serving; a
		// stream gap is what the reconnect rule is for.
		h.log.Error("resolving flight flags for the event stream failed", "err", err)
		return nil
	}
	frames := make([]rawFrame, 0, len(rows))
	for _, row := range rows {
		payload, err := json.Marshal(row)
		if err != nil {
			h.log.Error("encoding a raw event failed", "seq", row.Seq, "err", err)
			continue
		}
		frames = append(frames, rawFrame{
			typ:    row.Type,
			handle: row.Handle,
			bytes:  fmt.Appendf(nil, "id: %d\nevent: %s\ndata: %s\n\n", row.Seq, rawEventName, payload),
		})
	}
	return frames
}

// handleEventsStream serves `GET /v1/events/stream`: one `raw` event per stored
// row, JSON-encoded [EventRow], plus a comment heartbeat.
//
// `?type=` and `?handle=` narrow it server-side, per subscriber: the frames are
// rendered once for everybody, and a filtered subscriber compares two strings
// per event before writing — the cheap end of the telemetry.window firehose
// problem, since a filter that skips a frame costs no I/O at all. An unknown,
// retired or banned `?handle=` is the usual one-answer 404.
//
// Registered only when a raw broadcaster was supplied, like the feed stream.
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	handle := r.URL.Query().Get("handle")
	if handle != "" {
		entry, ok := s.deps.Directory.Lookup(handle)
		if !ok {
			s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
			return
		}
		// Canonical spelling: frames carry the claimed casing.
		handle = entry.Handle
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, authz.CodeInternal, "streaming is unavailable")
		return
	}

	frames, cancel, ok := s.eventsHub.subscribe()
	if !ok {
		s.refuseStream(w)
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if _, err := fmt.Fprintf(w, "retry: %d\n\n", feedRetry); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(feedHeartbeat)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()

		case batch, ok := <-frames:
			if !ok {
				return
			}
			wrote := false
			for _, fr := range batch {
				if typ != "" && fr.typ != typ {
					continue
				}
				if handle != "" && fr.handle != handle {
					continue
				}
				// Rendered once by the hub; every subscriber writes these same
				// bytes.
				if _, err := w.Write(fr.bytes); err != nil {
					return
				}
				wrote = true
			}
			if wrote {
				flusher.Flush()
			}
		}
	}
}

// refuseStream answers a stream open that would exceed [Deps.MaxStreamClients]:
// the §4.9 rate_limited shape, with a Retry-After a well-behaved client (and
// the browser's EventSource retry) can honour.
func (s *Server) refuseStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	// Not the §4.8 cache header: a shared cache holding this 429 for 30 s
	// would refuse streams to every client behind it.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.Itoa(feedRetry/1000))
	w.WriteHeader(http.StatusTooManyRequests)
	if err := json.NewEncoder(w).Encode(errorBody{Error: authz.CodeRateLimited, Detail: "too many open streams; retry later"}); err != nil {
		s.deps.Log.Debug("read api response write failed", "err", err)
	}
}
