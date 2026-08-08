package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/starfederation/datastar-go/datastar"
)

// The raw event log pages — `/p/{handle}/events` and `/events` — and their
// shared datastar live tail, `GET /v1/events/sse`.
//
// The two pages render rows through one `event-row` partial, so a row on the
// per-handle page, a row on the global page, the SSE prime and a live patch
// are all byte-identical — the same discipline the feed keeps with `feed-item`.

// EventRows is one page of the raw log, and the live tail's DOM cap.
const EventRows = 50

// EventsSSEPath is the datastar live tail behind both events pages. The JSON
// twin is readapi's `/v1/events/stream`; this one carries rendered `event-row`
// HTML addressed to DOM ids the templates own, exactly like [FeedPath].
const EventsSSEPath = "/v1/events/sse"

// EventItem is what the `event-row` partial renders: one public row, plus
// whether it just arrived over the open stream — [FeedItem]'s shape, for the
// same reason. Arrived is false for every row a whole page or prime renders
// and true only for the per-row live patch; it stamps `data-arrived`, the only
// thing the arrival flash is scoped to.
type EventItem struct {
	readapi.EventRow
	Arrived bool
}

// newEventItem wraps a row for the `events-body` template's range, exposed to
// templates as `eventItem`. A list never marks a row as arrived.
func newEventItem(row readapi.EventRow) EventItem { return EventItem{EventRow: row} }

// eventRowID is the DOM id of one log row. The SSE tail removes rows that have
// scrolled past [EventRows] by this id, and the `event-row` template stamps it,
// so the two must agree — hence one function, exposed as `eventRowID`.
func eventRowID(seq int64) string { return "event-row-" + strconv.FormatInt(seq, 10) }

// EventsBody is what the `events-body` template renders: the log's `<tbody>`.
// Source is `data-source`, the same "ssr"|"sse" readiness signal the feed list
// carries — the page ships "ssr" and the tail's prime replaces the body with an
// identical one marked "sse". Filter drives only the empty-state copy.
type EventsBody struct {
	Rows   []readapi.EventRow
	Source string
	Filter string
}

// eventChip is one `?type=` filter choice, with its URL precomputed: the global
// page's chips must carry `?handle=` through, and building URLs in Go keeps the
// two pages' chip markup one shared partial instead of two diverging ones.
type eventChip struct {
	Type     string
	URL      string
	Selected bool
}

// eventsPanel is the shared table-plus-tail block both pages hand to the
// `events-panel` partial.
type eventsPanel struct {
	EventsBody
	// Label names the scrollable region for assistive tech.
	Label string
	// Live renders the tail: the `data-init` element that opens the SSE, and
	// the pause/resume control me.js wires. Only page one is live — a page read
	// at a cursor is historical, and a tail prepending today's rows above
	// last week's would be a lie about order.
	Live bool
	// SSEURL is the tail's URL, carrying the page's own filter.
	SSEURL string
}

// eventsData drives both events pages.
type eventsData struct {
	readapi.EventsResponse
	// Panel is the shared table + live tail block.
	Panel eventsPanel
	// AllURL is the unfiltered view of this same page ("All types").
	AllURL string
	// Chips are the filter's choices: the §4.2 taxonomy unioned with whatever
	// this page actually contains. The page's own types alone would collapse
	// the filter — a filtered page contains only the active type, so every
	// other choice would vanish the moment one was made.
	Chips []eventChip
	// Filter is the active `?type=`, empty for none.
	Filter string
	// HandleFilter is the global page's active `?handle=`, canonical casing,
	// empty for none (and always empty on the per-handle page, whose handle is
	// the path, not a filter).
	HandleFilter string
	// ClearURL drops the handle filter and keeps the type filter.
	ClearURL string
	// Before echoes the cursor this page was read at, so "back to newest" can
	// be offered only when it would do something.
	Before string
	// NewestURL is this view at its newest page.
	NewestURL string
	// NextURL is the link to the next (older) page, empty at the end of the log.
	//
	// It is built from `Next` and nothing else: a filtered page that hit the
	// server's scan bound comes back short *with* a cursor and is not the end of
	// the log, so paging until a page looks short would silently truncate
	// somebody's history.
	NextURL string
}

// eventTypeChoices is the chip union both pages share: ingest's registry — the
// same allow-list the batch validator enforces, mirrored by docs/events.md and
// the SPA's KNOWN_EVENT_TYPES — plus the page's own types, plus the active
// filter (so its chip cannot vanish under itself). A type a newer mod version
// introduces becomes filterable as soon as one shows up.
func eventTypeChoices(events []readapi.EventRow, filter string) []string {
	seen := map[string]bool{}
	var types []string
	for _, t := range ingest.KnownTypes() {
		seen[t] = true
		types = append(types, t)
	}
	for _, ev := range events {
		if !seen[ev.Type] {
			seen[ev.Type] = true
			types = append(types, ev.Type)
		}
	}
	if filter != "" && !seen[filter] {
		types = append(types, filter)
	}
	sort.Strings(types)
	return types
}

// eventsURL assembles a page URL for either events view: base is the page path
// and the non-empty parameters become the query, in a fixed order so the same
// view is always the same URL.
func eventsURL(base, typ, handle, before string) string {
	q := url.Values{}
	if typ != "" {
		q.Set("type", typ)
	}
	if handle != "" {
		q.Set("handle", handle)
	}
	if before != "" {
		q.Set("before", before)
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// eventsPage assembles everything both handlers share, once the rows are in
// hand. base is the page's own path; handleFilter is the global page's
// `?handle=` (empty on the per-handle page, where the handle is in base).
func eventsPage(out readapi.EventsResponse, base, filter, handleFilter, before string) eventsData {
	// The shared partial always renders a handle link, so the per-player
	// envelope's one handle is stamped onto each of its rows — the global rows
	// already carry their own.
	for i := range out.Events {
		if out.Events[i].Handle == "" {
			out.Events[i].Handle = out.Handle
		}
	}

	data := eventsData{
		EventsResponse: out,
		Filter:         filter,
		HandleFilter:   handleFilter,
		Before:         before,
		AllURL:         eventsURL(base, "", handleFilter, ""),
		ClearURL:       eventsURL("/events", filter, "", ""),
		NewestURL:      eventsURL(base, filter, handleFilter, ""),
		Panel: eventsPanel{
			EventsBody: EventsBody{Rows: out.Events, Source: "ssr", Filter: filter},
			// Only page one tails: a cursor-paged view is historical.
			Live:   before == "",
			SSEURL: eventsURL(EventsSSEPath, filter, handleFilter, ""),
		},
	}
	for _, t := range eventTypeChoices(out.Events, filter) {
		data.Chips = append(data.Chips, eventChip{
			Type:     t,
			URL:      eventsURL(base, t, handleFilter, ""),
			Selected: t == filter,
		})
	}
	if out.Next != "" {
		data.NextURL = eventsURL(base, filter, handleFilter, out.Next)
	}
	return data
}

// beforeCursor parses `?before=`. ok is false after the 404 has been written.
func (s *Server) beforeCursor(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	raw := r.URL.Query().Get("before")
	if raw == "" {
		return 0, "", true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		s.notFound(w, r, "That is not a cursor catlog issued.")
		return 0, "", false
	}
	return n, raw, true
}

// --- GET /p/{handle}/events -------------------------------------------------------

func (s *Server) handlePlayerEvents(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	filter := r.URL.Query().Get("type")
	before, beforeRaw, ok := s.beforeCursor(w, r)
	if !ok {
		return
	}

	out, known, err := s.deps.Read.PlayerEvents(r.Context(), handle, filter, before, EventRows)
	switch {
	case !known:
		s.notFound(w, r, "No such player.")
		return
	case err != nil:
		s.serverError(w, r, err, "read the event log")
		return
	}

	data := eventsPage(out, "/p/"+url.PathEscape(out.Handle)+"/events", filter, "", beforeRaw)
	// The tail is this page's view: one handle, whatever type is filtered.
	data.Panel.SSEURL = eventsURL(EventsSSEPath, filter, out.Handle, "")
	data.Panel.Label = out.Handle + "’s raw events"

	s.render(w, r, http.StatusOK, "events", publicCache, page{
		Title: out.Handle + "'s events — catlog",
		Nav:   "boards",
		Data:  data,
	})
}

// --- GET /events -------------------------------------------------------------------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := q.Get("type")
	handle := strings.TrimSpace(q.Get("handle"))
	before, beforeRaw, ok := s.beforeCursor(w, r)
	if !ok {
		return
	}

	var out readapi.EventsResponse
	var err error
	if handle != "" {
		// The same delegation `GET /v1/events?handle=` does: the filtered view
		// is the per-player view wearing the global envelope, so an unknown,
		// retired or banned handle gets the same one answer here as everywhere
		// else, and the two pages cannot disagree about whose events these are.
		var known bool
		out, known, err = s.deps.Read.PlayerEvents(r.Context(), handle, filter, before, EventRows)
		if err == nil && !known {
			s.notFound(w, r, "No such player.")
			return
		}
		handle = out.Handle // canonical casing
	} else {
		out, err = s.deps.Read.GlobalEvents(r.Context(), filter, before, EventRows)
	}
	if err != nil {
		s.serverError(w, r, err, "read the event log")
		return
	}

	data := eventsPage(out, "/events", filter, handle, beforeRaw)
	data.Panel.Label = "Raw events, all players"

	s.render(w, r, http.StatusOK, "events_all", publicCache, page{
		Title: "Raw events — catlog",
		Nav:   "events",
		Data:  data,
	})
}

// --- GET /v1/events/sse -------------------------------------------------------------

// handleEventsSSE serves the datastar live tail both events pages open on page
// one: the raw broadcaster's committed batches, put through the readapi
// redaction seam, rendered with the same `event-row` partial the pages use,
// and prepended into `#events-body`.
//
// The shape is [Server.handleFeed]'s, ordering included: subscribing *before*
// priming is what makes the handover lossless — a row committed between the
// two arrives on the channel and is dropped by the high-water mark, whereas
// priming first would lose it silently. Dedup is by Seq, which is strictly
// increasing with arrival.
//
// `?type=` and `?handle=` scope the tail to the page's own filter. The handle
// is matched case-insensitively rather than resolved: the pages embed the
// canonical casing before this URL exists, they 404 an unknown handle before
// any tail is rendered, and a hand-crafted URL for a handle nobody holds
// simply streams nothing — the same non-answer as an empty filter match.
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	handle := r.URL.Query().Get("handle")

	batches, cancel := s.deps.Raw.Subscribe()
	defer cancel()

	// See handleFeed: the header that stops nginx buffering a stream.
	w.Header().Set("X-Accel-Buffering", "no")

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()

	// The prime: the newest page under this subscriber's own filter, rendered
	// as the whole tbody. On page one it replaces identical rows — that is the
	// point: `data-source="sse"` is the readiness signal, and after a
	// reconnect it is also what heals the gap the closed stream left.
	prime := readapi.EventsResponse{}
	var err error
	if handle != "" {
		var known bool
		prime, known, err = s.deps.Read.PlayerEvents(ctx, handle, typ, 0, EventRows)
		if err == nil && !known {
			prime = readapi.EventsResponse{}
		}
	} else {
		prime, err = s.deps.Read.GlobalEvents(ctx, typ, 0, EventRows)
	}
	if err != nil {
		s.deps.Log.Error("priming the event tail failed", "err", err)
		// The connection stays open with an empty body rather than 500ing,
		// exactly as the feed does: the live half still works.
		prime = readapi.EventsResponse{}
	}
	for i := range prime.Events {
		if prime.Events[i].Handle == "" {
			prime.Events[i].Handle = prime.Handle
		}
	}

	high := int64(0)
	shown := make([]int64, 0, EventRows)
	for _, row := range prime.Events {
		high = max(high, row.Seq)
		shown = append(shown, row.Seq)
	}

	body, err := s.fragment("events-body", EventsBody{Rows: prime.Events, Source: "sse", Filter: typ})
	if err != nil {
		s.deps.Log.Error("rendering the event tail prime failed", "err", err)
		return
	}
	if err := sse.PatchElements(body); err != nil {
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
				`<span id="events-heartbeat" hidden data-at="%d"></span>`, time.Now().UnixMilli()); err != nil {
				return
			}

		case batch, ok := <-batches:
			if !ok {
				return
			}
			// Everything from the broadcaster goes through the redaction seam
			// before this handler renders a byte of it. An error fails closed:
			// a batch whose flags cannot be read might contain a flagged
			// flight, so none of it is published (see readapi.PublicEvents).
			rows, err := s.deps.Read.PublicEvents(ctx, batch)
			if err != nil {
				s.deps.Log.Error("rendering the event tail batch failed", "err", err)
				continue
			}
			for _, row := range rows {
				if row.Seq <= high {
					continue // already primed, or replayed
				}
				high = row.Seq
				if typ != "" && row.Type != typ {
					continue
				}
				if handle != "" && !strings.EqualFold(row.Handle, handle) {
					continue
				}

				// Arrived marks only this live copy, so primed and
				// reconnect-primed rows never animate (see FeedItem).
				item, err := s.fragment("event-row", EventItem{EventRow: row, Arrived: true})
				if err != nil {
					s.deps.Log.Error("rendering a log row failed", "seq", row.Seq, "err", err)
					continue
				}
				// The broadcaster publishes in seq order, so prepending each in
				// turn leaves the newest at the top.
				if err := sse.PatchElements(item,
					datastar.WithSelector("#events-body"), datastar.WithModePrepend()); err != nil {
					return
				}

				shown = append([]int64{row.Seq}, shown...)
				for len(shown) > EventRows {
					oldest := shown[len(shown)-1]
					shown = shown[:len(shown)-1]
					if err := sse.RemoveElementByID(eventRowID(oldest)); err != nil {
						return
					}
				}
			}
		}
	}
}
