package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	rows, cancel := s.deps.Feed.Subscribe()
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

		case batch, ok := <-rows:
			if !ok {
				return
			}
			for _, row := range batch {
				payload, err := json.Marshal(row)
				if err != nil {
					s.deps.Log.Error("encoding a feed row failed", "id", row.ID, "err", err)
					continue
				}
				// `id:` lets a browser surface lastEventId; the server does not
				// consume it on reconnect (see the package comment above).
				if _, err := fmt.Fprintf(w, "id: %d\nevent: feed\ndata: %s\n\n", row.ID, payload); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}
