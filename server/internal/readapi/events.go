package readapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The raw-event views: `GET /v1/players/{handle}/events` and `GET /v1/events`
// (the live half lives in events_stream.go).
//
// # Why catlog publishes this at all
//
// Every other endpoint here is a *derived* number — a fold's opinion about a
// history nobody outside the server can see. This is the history: the event
// types, when the server received them, and what they carried. It is what makes
// "your record is 214 m/s" checkable rather than merely asserted, and it is the
// surface a player uses to understand why a board says what it says.
//
// # What it does not show
//
// The install-derived identifiers, per privacy.go: `install` is dropped, and
// `career` and `kid` are relabelled per player. That file has the reasoning;
// the short version is that publishing them would let anyone link one person's
// two handles, which is the one thing the handle-only identity model (§1) is
// for. The envelope's `wall_t` is omitted as well — it is the untrusted client
// clock, and its offset from `recv` is a per-machine constant.
//
// Everything else is published, including payload keys this build has never
// heard of: §4.1 preserves unknown payload keys, and a raw view that dropped
// them would be lying about what catlog recorded.
//
// # Paging
//
// Newest first, by cursor rather than offset: the log is append-only at the
// head, so an offset would drift under a reader whenever the player shipped
// between two requests. `next` is opaque — it is the value to pass back as
// `?before=`, and nothing else.
const (
	// DefaultEventLimit is one screen of history.
	DefaultEventLimit = 50
	// MaxEventLimit is the ceiling, clamped rather than rejected for the same
	// cache reason as [MaxLimit].
	MaxEventLimit = 200
	// eventScanPage is how many rows a `?type=`-filtered page reads at a time.
	eventScanPage = 256
	// maxEventScan bounds how far one filtered request will walk backwards
	// looking for matches, so asking for a type a player has never emitted
	// cannot turn a single request into a scan of their whole history. A page
	// that stops here comes back short *with* a cursor, which is exactly what
	// the cursor is for.
	maxEventScan = 5000
)

// EventsResponse is `GET /v1/players/{handle}/events`, and also
// `GET /v1/events` — the same page of the same log, so the same envelope. The
// per-player endpoint always fills Handle; the global one echoes it only when
// `?handle=` filtered.
type EventsResponse struct {
	Handle string `json:"handle,omitempty"`
	// Limit echoes the effective page size after clamping.
	Limit int `json:"limit"`
	// Type echoes the `?type=` filter when one was given.
	Type string `json:"type,omitempty"`
	// Next is the cursor for the next (older) page, absent once the log is
	// exhausted. Treat it as opaque.
	//
	// A short page carrying a cursor is not the end of the log — a filtered
	// page that hit [maxEventScan] looks exactly like that — so a client pages
	// until Next is absent, not until a page comes back short.
	Next string `json:"next,omitempty"`
	// Events are newest first.
	Events []EventRow `json:"events"`
}

// EventRow is one stored §4.1 envelope, as the public API publishes it.
type EventRow struct {
	// Seq is the server-assigned position in the stored log — the same value
	// the paging cursor is made of, and the `id:` of the row's stream frame.
	// Strictly increasing with arrival, so it is also what a client merges the
	// snapshot and the stream by.
	Seq int64 `json:"seq"`
	// Handle names the player on the global views (`GET /v1/events` and the
	// stream), where a page mixes players. The per-player endpoint omits it:
	// its envelope already names the one handle every row belongs to.
	Handle string `json:"handle,omitempty"`
	// ID is the envelope's client-minted ULID — the dedup key (§4.5).
	ID   string `json:"id"`
	Type string `json:"type"`
	Ver  int    `json:"ver"`
	// Session is the save-load boundary this event belongs to, Flight the
	// flight. Both are per-occurrence ULIDs with nothing derived from the
	// install in them, and they are what group a page of events into something
	// legible. Flight is absent on session and roster events (§4.1).
	Session string `json:"session,omitempty"`
	Flight  string `json:"flight,omitempty"`
	// Career is the §4.1 career key **relabelled per player** (privacy.go). It
	// still groups a player's events by save, and it can no longer be compared
	// against another player's.
	Career string `json:"career,omitempty"`
	// SimT is seconds since this career's game started. Absent, not zero, when
	// the event carried none.
	SimT *float64 `json:"sim_t,omitempty"`
	// Recv is the **server's** receive time in unix ms. The client's own
	// `wall_t` is not published; see the file comment.
	Recv int64 `json:"recv"`
	// Payload is the §4.2 payload with the redaction in privacy.go applied,
	// and otherwise verbatim — unknown keys included.
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) handlePlayerEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := s.intParam(w, r, "limit", DefaultEventLimit)
	if !ok {
		return
	}
	before, ok := s.intParam(w, r, "before", 0)
	if !ok {
		return
	}
	if before < 0 {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest, "before is not a cursor this API issued")
		return
	}

	out, known, err := s.PlayerEvents(r.Context(), r.PathValue("handle"), r.URL.Query().Get("type"), int64(before), limit)
	switch {
	case !known:
		// The same one answer for unknown, retired and banned as everywhere
		// else (§4.8), so this endpoint is not the ban oracle the others refuse
		// to be.
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
	case err != nil:
		s.fail(w, r, err, "read the event log")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// handleEvents serves `GET /v1/events`: the whole log's newest page, every
// player mixed together — the same rows, redaction and paging as the per-player
// view, with each row naming its handle.
//
// `?handle=` narrows it to one player by delegating to [Server.PlayerEvents] —
// the same code behind `GET /v1/players/{handle}/events`, so the two cannot
// disagree — and 404s an unknown, retired or banned handle with the same one
// answer for all three.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := s.intParam(w, r, "limit", DefaultEventLimit)
	if !ok {
		return
	}
	before, ok := s.intParam(w, r, "before", 0)
	if !ok {
		return
	}
	if before < 0 {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest, "before is not a cursor this API issued")
		return
	}
	typ := r.URL.Query().Get("type")

	if handle := r.URL.Query().Get("handle"); handle != "" {
		out, known, err := s.PlayerEvents(r.Context(), handle, typ, int64(before), limit)
		switch {
		case !known:
			s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
		case err != nil:
			s.fail(w, r, err, "read the event log")
		default:
			// The global view's rows always name their player, filtered or not.
			for i := range out.Events {
				out.Events[i].Handle = out.Handle
			}
			s.writeJSON(w, http.StatusOK, out)
		}
		return
	}

	out, err := s.GlobalEvents(r.Context(), typ, int64(before), limit)
	if err != nil {
		s.fail(w, r, err, "read the event log")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// PlayerEvents assembles `GET /v1/players/{handle}/events`.
//
// ok is false for an unknown, retired or banned handle.
func (s *Server) PlayerEvents(ctx context.Context, handle, typ string, before int64, limit int) (EventsResponse, bool, error) {
	entry, ok := s.deps.Directory.Lookup(handle)
	if !ok {
		return EventsResponse{}, false, nil
	}
	limit = min(max(limit, 1), MaxEventLimit)

	rows, next, err := s.scanEvents(ctx, typ, nil, before, limit,
		func(before int64, limit int) ([]store.StoredEvent, error) {
			return s.deps.Events.PlayerEvents(ctx, entry.PlayerID, before, limit)
		})
	if err != nil {
		return EventsResponse{}, true, err
	}

	out := EventsResponse{
		Handle: entry.Handle, Limit: limit, Type: typ,
		Events: make([]EventRow, 0, len(rows)),
	}
	if next > 0 {
		out.Next = strconv.FormatInt(next, 10)
	}
	for _, ev := range rows {
		// No per-row handle: the envelope above names it once.
		out.Events = append(out.Events, eventRow(ev, ""))
	}
	return out, true, nil
}

// GlobalEvents assembles `GET /v1/events` without a `?handle=` filter (with
// one, the request is [Server.PlayerEvents] wearing the global envelope — see
// handleEvents).
func (s *Server) GlobalEvents(ctx context.Context, typ string, before int64, limit int) (EventsResponse, error) {
	limit = min(max(limit, 1), MaxEventLimit)

	// The same over-fetch-and-drop as the per-player page, plus one more drop:
	// a row whose player holds no handle in the directory — banned, purged, or
	// never claimed one — has no public name to publish under, exactly the rule
	// [Server.visibleRows] applies to boards.
	rows, next, err := s.scanEvents(ctx, typ,
		func(ev store.StoredEvent) bool {
			_, ok := s.deps.Directory.Handle(ev.PlayerID)
			return ok
		},
		before, limit,
		func(before int64, limit int) ([]store.StoredEvent, error) {
			return s.deps.Events.RecentEvents(ctx, before, limit)
		})
	if err != nil {
		return EventsResponse{}, err
	}

	out := EventsResponse{Limit: limit, Type: typ, Events: make([]EventRow, 0, len(rows))}
	if next > 0 {
		out.Next = strconv.FormatInt(next, 10)
	}
	for _, ev := range rows {
		handle, _ := s.deps.Directory.Handle(ev.PlayerID)
		out.Events = append(out.Events, eventRow(ev, handle))
	}
	return out, nil
}

// eventRow renders one stored event as the public API publishes it, redaction
// included: the payload through [Redact] and the career through [Label], both
// salted with **the row's own** player id — a page that mixes players relabels
// each row for its player, never once for the page, or two players' rows would
// share labels and be linkable.
//
// handle is stamped onto the row for the global views and left empty ­— and so
// omitted — for the per-player view.
func eventRow(ev store.StoredEvent, handle string) EventRow {
	row := EventRow{
		Seq:     ev.Seq,
		Handle:  handle,
		ID:      ids.String(ev.ID),
		Type:    ev.Type,
		Ver:     ev.Ver,
		Career:  Label(ev.PlayerID, kindCareer, ev.Career),
		Recv:    ev.RecvTime,
		Payload: Redact(ev.PlayerID, ev.Payload),
	}
	if ev.SessionID != ids.Zero {
		row.Session = ids.String(ev.SessionID)
	}
	if ev.FlightID != ids.Zero {
		row.Flight = ids.String(ev.FlightID)
	}
	if ev.SimTime.Valid {
		t := ev.SimTime.Float64
		row.SimT = &t
	}
	return row
}

// scanEvents reads one page of an event log source, applying the `?type=`
// filter, the flagged-flight exclusion, and the caller's extra keep predicate
// (nil for none) in Go. fetch reads one batch of the underlying log, newest
// first, from an exclusive `before` cursor — [store.Events.PlayerEvents] bound
// to a player, or [store.Events.RecentEvents] for the global view.
//
// None of the filters is in SQL. `type` has no index — `ev_player` is
// (player_id, seq) — so a SQL `type = ?` would let one request walk a whole
// history looking for a type that is not there; and the flag lives in the other
// database file (§5.4), so it cannot be joined at all. Filtering here, over
// pages, is the same over-fetch-and-drop shape [Server.visibleRows] uses for
// bans, and it is what makes [maxEventScan] a real bound rather than a comment.
//
// The flagged-flight lookup is per fetched batch, not per row: one
// [store.Projections.FlaggedFlights] IN-query resolves every distinct flight
// the batch touches (see [Server.flaggedFlights]).
func (s *Server) scanEvents(ctx context.Context, typ string, keep func(store.StoredEvent) bool, before int64, limit int,
	fetch func(before int64, limit int) ([]store.StoredEvent, error)) ([]store.StoredEvent, int64, error) {
	var (
		out     []store.StoredEvent
		cursor  = before
		scanned int
	)
	for len(out) < limit && scanned < maxEventScan {
		page := limit - len(out)
		if typ != "" {
			page = eventScanPage
		}
		batch, err := fetch(cursor, page)
		if err != nil {
			return nil, 0, err
		}
		flagged, err := s.flaggedFlights(ctx, batch)
		if err != nil {
			return nil, 0, err
		}
		consumed := 0
		for _, ev := range batch {
			cursor = ev.Seq
			consumed++
			scanned++
			if typ != "" && ev.Type != typ {
				continue
			}
			if flagged[ev.FlightID] {
				continue
			}
			if keep != nil && !keep(ev) {
				continue
			}
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
		if consumed == len(batch) && len(batch) < page {
			return out, 0, nil // the log is exhausted; there is no next page
		}
	}
	return out, cursor, nil
}

// flaggedFlights asks projections.db which of the flights on this batch carry a
// flag bit.
//
// # Why the public event log is not the whole event log
//
// §5.7's privacy page promises that a flagged flight "scores nothing and never
// appears publicly". The boards keep that promise by construction — the folds
// skip a flagged flight, so no row exists to publish — but this endpoint reads
// events.db directly, where nothing says a flight was flagged, and it would
// otherwise publish every event of it.
//
// The promise is also the *only* reading of the flags that Constitution §8's
// consequence test permits. A flag's sole effect may be to exclude a flight
// from the boards; it "never treats a player differently because of accumulated
// history". A browsable public record of which of somebody's flights were
// flagged is precisely a durable public consequence attached to a person — and
// the flags are `teleport`, `refuel`, `resource_edit`, `console` and `tuning`,
// so such a page would be publicly labelling somebody who nudged a debug window
// as a cheat. Excluded, therefore, rather than shown-and-marked.
//
// This is not an "own data" view: a player cannot see their own flagged flights
// here either, and the endpoint cannot tell who is asking — it takes no
// credentials at all (cors.go). Closing that half of the promise is dashboard
// work; see docs/DECISIONS.md.
func (s *Server) flaggedFlights(ctx context.Context, batch []store.StoredEvent) (map[ids.ID]bool, error) {
	seen := map[ids.ID]bool{}
	flights := make([]ids.ID, 0, len(batch))
	for _, ev := range batch {
		// Session and roster events carry no flight (§4.1) and belong to no
		// flight that could be flagged.
		if ev.FlightID == ids.Zero || seen[ev.FlightID] {
			continue
		}
		seen[ev.FlightID] = true
		flights = append(flights, ev.FlightID)
	}
	if len(flights) == 0 {
		return nil, nil
	}
	var flagged map[ids.ID]bool
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		flagged, err = p.FlaggedFlights(ctx, flights)
		return err
	})
	return flagged, err
}
