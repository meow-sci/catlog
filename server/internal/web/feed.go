package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/starfederation/datastar-go/datastar"
)

// FeedLimit is how many lines the live panel holds, in the DOM and in the prime.
// The `feed` table itself is capped at [store.FeedCap] (500); this is smaller
// because the panel is a glance, not a log.
const FeedLimit = 30

// feedHeartbeat is how often the stream writes something when nothing is
// happening.
//
// Go cancels the request context when the client hangs up, so the loop does not
// need this to notice a closed browser tab. It exists for what sits *between*:
// nginx (`proxy_read_timeout 1h`, §6.1) and any CDN or corporate proxy that
// silently drops an idle connection. A patch every 20 s costs a few dozen bytes
// per client per minute and turns "the feed stopped after a while" into a
// non-problem.
const feedHeartbeat = 20 * time.Second

// handleFeed serves `GET /v1/feed/sse` (§4.8, §5.7): the datastar stream that
// patches new activity into `#feed`.
//
// The ordering below is load-bearing. Subscribing *before* priming is what makes
// the handover lossless: a feed row committed between the two arrives on the
// channel and is filtered out by id, whereas priming first would drop anything
// committed in the gap and the panel would silently miss it until the next
// event. Dedup is by id rather than by a set membership test because feed ids
// are a monotonic INTEGER PRIMARY KEY (§5.4) — everything at or below the
// high-water mark is already on the page.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	rows, cancel := s.deps.Feed.Subscribe()
	defer cancel()

	// nginx buffers proxied responses by default; this is the header that turns
	// it off for a stream that must be flushed frame by frame (§6.1 sets it too
	// — belt and braces, because the CDN in front of it is not our config).
	w.Header().Set("X-Accel-Buffering", "no")

	// NewSSE sets Content-Type, `Cache-Control: no-cache` and Connection.
	// This is the one public route that must NOT carry the §4.8 cache header:
	// a cached event stream is a stream that never updates.
	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()

	var primed []store.FeedRow
	if err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		primed, err = p.RecentFeed(ctx, FeedLimit)
		return err
	}); err != nil {
		s.deps.Log.Error("priming the feed failed", "err", err)
		// The connection stays open with an empty list rather than 500ing: the
		// live half still works, and datastar would reconnect into the same
		// failure anyway.
		primed = nil
	}

	// RecentFeed returns newest first, which is also the order the panel shows.
	high := int64(0)
	shown := make([]int64, 0, FeedLimit)
	for _, row := range primed {
		high = max(high, row.ID)
		shown = append(shown, row.ID)
	}

	list, err := s.fragment("feed-list", FeedList{Rows: primed, Source: FeedSourceSSE})
	if err != nil {
		s.deps.Log.Error("rendering the feed failed", "err", err)
		return
	}
	if err := sse.PatchElements(list); err != nil {
		return
	}

	ticker := time.NewTicker(feedHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if sse.IsClosed() {
				return
			}
			if err := sse.PatchElementf(
				`<span id="feed-heartbeat" hidden data-at="%d"></span>`, time.Now().UnixMilli()); err != nil {
				return
			}

		case batch, ok := <-rows:
			if !ok {
				return
			}
			for _, row := range batch {
				if row.ID <= high {
					continue // already primed, or replayed
				}
				high = row.ID

				// Arrived marks only this live copy: the CSS arrival flash is
				// scoped to it, so primed and reconnect-primed rows never
				// animate — the flash means "new since you opened this page",
				// not "the stream (re)connected".
				item, err := s.fragment("feed-item", FeedItem{FeedRow: row, Arrived: true})
				if err != nil {
					s.deps.Log.Error("rendering a feed line failed", "id", row.ID, "err", err)
					continue
				}
				// Prepend: the projector publishes in ascending id order, so
				// prepending each in turn leaves the newest at the top.
				if err := sse.PatchElements(item,
					datastar.WithSelector("#feed"), datastar.WithModePrepend()); err != nil {
					return
				}

				shown = append([]int64{row.ID}, shown...)
				for len(shown) > FeedLimit {
					oldest := shown[len(shown)-1]
					shown = shown[:len(shown)-1]
					if err := sse.RemoveElementByID(feedItemID(oldest)); err != nil {
						return
					}
				}
			}
		}
	}
}

// FeedList is what the `feed-list` template renders. Source distinguishes the
// copy the page shipped with from the one the stream patched over it.
type FeedList struct {
	Rows   []store.FeedRow
	Source string
}

// FeedItem is what the `feed-item` template renders: one row, plus whether it
// just arrived over the open stream. Arrived is false for every row a whole
// list renders (the SSR page and the SSE prime) and true only for the per-row
// live patch — it stamps `data-arrived`, which is the only thing the arrival
// flash is scoped to.
type FeedItem struct {
	store.FeedRow
	Arrived bool
}

// newFeedItem wraps a row for the `feed-list` template's range, exposed to
// templates as `feedItem`. A list never marks a row as arrived.
func newFeedItem(row store.FeedRow) FeedItem { return FeedItem{FeedRow: row} }

// The two values [FeedList.Source] takes.
const (
	FeedSourceSSR = "ssr"
	FeedSourceSSE = "sse"
)

// feedItemID is the DOM id of one feed line. The SSE handler removes lines that
// have scrolled past [FeedLimit] by this id, and the `feed-item` template stamps
// it, so the two must agree — hence one function, exposed to templates as
// `feedID`.
func feedItemID(id int64) string { return "feed-item-" + strconv.FormatInt(id, 10) }
