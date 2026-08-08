package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/store"
)

// The JSON activity feed.
//
// # Why this exists next to /v1/feed/sse
//
// `GET /v1/feed/sse` (package web) is a datastar stream. Its payload is HTML —
// `PatchElements` frames carrying rendered `feed-item` fragments addressed to
// DOM ids the Go templates own. That is exactly the right transport for the
// server-rendered site, where the browser's only job is to splice the fragment
// in, and it is why that site gets its live feed for free.
//
// It is the wrong transport for a second, independently deployed frontend. A
// React client consuming it would have to scrape data back out of markup it did
// not render, and would break the next time somebody renamed a CSS class in a
// template it cannot see. So the same rows are published here in the shape the
// projector actually stores them, and the datastar route is left untouched.
//
// # Two routes, because a feed is two problems
//
// `GET /v1/feed` is the snapshot: what the panel shows on first paint, and what
// a client falls back to if it cannot hold a stream open. `GET /v1/feed/stream`
// is the live half. The stream deliberately does *not* replay history — a
// reconnecting client re-reads the snapshot, which is one round trip and cannot
// drift, rather than the server growing a `Last-Event-ID` cursor over a table
// that is capped at [store.FeedCap] and can lose rows out from under it.

// Feed is the §5.6 broadcaster. *projector.Broadcaster implements it. It is
// declared here, rather than imported, so this package keeps depending on
// nothing above the store layer.
type Feed interface {
	Subscribe() (<-chan []store.FeedRow, func())
}

// feedHub sits between the broadcaster and the stream handlers so each row is
// JSON-encoded **once** per broadcast, not once per subscriber: with N tabs
// open, the projector's commit used to cost N identical marshals of every row.
// Package web does the same for its HTML frames; this is the JSON half.
//
// The hub holds exactly one upstream subscription, taken when the first client
// arrives and cancelled when the last one leaves, so a server nobody is
// streaming from runs no extra goroutine.
type feedHub struct {
	feed Feed
	log  *slog.Logger
	// cap is [Deps.MaxStreamClients] with its default applied: how many
	// subscribers this hub will carry before refusing the next one.
	cap int

	mu   sync.Mutex
	next int64
	subs map[int64]chan [][]byte
	stop func()
}

func newFeedHub(feed Feed, log *slog.Logger, cap int) *feedHub {
	return &feedHub{feed: feed, log: log, cap: cap, subs: map[int64]chan [][]byte{}}
}

// subscribe registers a stream client. Frames arrive fully rendered — one SSE
// `id:`/`event:`/`data:` block per feed row — and are shared between
// subscribers, so a handler writes them and must not modify them.
//
// ok is false when the hub is at [feedHub.cap]: every stream open holds a
// connection, a goroutine and a buffer for as long as the tab lives, so the
// cap is what keeps N browsers from being a memory bill (Constitution §2). The
// caller answers 429 and the client retries later.
func (h *feedHub) subscribe() (frames <-chan [][]byte, cancel func(), ok bool) {
	// The buffer mirrors the broadcaster's own: a client that cannot keep up
	// loses batches rather than stalling anyone else.
	ch := make(chan [][]byte, 8)

	h.mu.Lock()
	if len(h.subs) >= h.cap {
		h.mu.Unlock()
		return nil, nil, false
	}
	id := h.next
	h.next++
	h.subs[id] = ch
	if h.stop == nil {
		rows, cancel := h.feed.Subscribe()
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
func (h *feedHub) run(rows <-chan []store.FeedRow) {
	for batch := range rows {
		frames := make([][]byte, 0, len(batch))
		for _, row := range batch {
			payload, err := json.Marshal(row)
			if err != nil {
				h.log.Error("encoding a feed row failed", "id", row.ID, "err", err)
				continue
			}
			// `id:` lets a browser surface lastEventId; the server does not
			// consume it on reconnect (see the package comment above).
			frames = append(frames, fmt.Appendf(nil, "id: %d\nevent: feed\ndata: %s\n\n", row.ID, payload))
		}
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

// Paging bounds for `GET /v1/feed`.
const (
	// DefaultFeedLimit matches the server-rendered panel's own row count, so the
	// two frontends show the same amount of history by default.
	DefaultFeedLimit = 30
	// MaxFeedLimit is the whole table (§5.4). Asking for more is clamped, for
	// the same cache reason as [MaxLimit].
	MaxFeedLimit = store.FeedCap
)

// feedHeartbeat is how often the stream writes a comment when nothing is
// happening. Go cancels the request context when the client hangs up, so this is
// not how a closed tab is noticed — it exists for the proxies in between (§6.1's
// nginx, any CDN) that drop an idle connection without telling either end.
const feedHeartbeat = 20 * time.Second

// feedRetry is the `retry:` the stream opens with — how long a browser waits
// before reconnecting an EventSource that dropped, in milliseconds.
const feedRetry = 5000

// FeedResponse is `GET /v1/feed`.
type FeedResponse struct {
	// Limit echoes the effective row count after clamping.
	Limit int `json:"limit"`
	// Rows are newest first, the order a feed panel displays.
	Rows []store.FeedRow `json:"rows"`
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	limit, ok := s.intParam(w, r, "limit", DefaultFeedLimit)
	if !ok {
		return
	}
	out, err := s.RecentFeed(r.Context(), limit)
	if err != nil {
		s.fail(w, r, err, "read the activity feed")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// RecentFeed assembles `GET /v1/feed`.
func (s *Server) RecentFeed(ctx context.Context, limit int) (FeedResponse, error) {
	limit = min(max(limit, 1), MaxFeedLimit)
	out := FeedResponse{Limit: limit, Rows: []store.FeedRow{}}
	err := s.deps.Projections.With(func(p *store.Projections) error {
		rows, err := p.RecentFeed(ctx, limit)
		if err != nil {
			return err
		}
		if rows != nil {
			out.Rows = rows
		}
		return nil
	})
	if err != nil {
		return FeedResponse{}, err
	}
	return out, nil
}

// handleFeedStream serves `GET /v1/feed/stream`: one `feed` event per new row,
// JSON-encoded, plus a comment heartbeat.
//
// Registered only when a broadcaster was supplied, so a Server built without one
// (every unit test that does not care about the feed) simply has no such route
// rather than a route that answers 500.
func (s *Server) handleFeedStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Nothing in catlogd's wiring can produce this; a ResponseWriter that
		// cannot flush would buffer the whole stream forever, so say so rather
		// than hang.
		s.writeError(w, http.StatusInternalServerError, "internal", "streaming is unavailable")
		return
	}

	// Subscribe before the first byte goes out. There is no priming here, so
	// there is no window to lose a row in — but the ordering also means a client
	// that opened the stream and then fetched the snapshot cannot miss anything
	// committed between the two calls, only see it twice, which it dedupes by id.
	frames, cancel, ok := s.feedHub.subscribe()
	if !ok {
		s.refuseStream(w)
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// The one public read route that must not carry the §4.8 cache header: a
	// cached event stream is a stream that never updates.
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default (§6.1 sets this too — belt and
	// braces, because the CDN in front of it is not our config).
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
			for _, frame := range batch {
				// Rendered once by the hub; every subscriber writes these same
				// bytes.
				if _, err := w.Write(frame); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}
