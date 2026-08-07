package adminapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/store"
)

// RegisterEvents mounts `POST /admin/events`.
//
//	POST /admin/events   {handle, events: [<§4.1 envelope>, …]}
//
// # Why this exists
//
// §8's `feed.spec.ts` is specified as "open home, POST a seed event via admin
// API, assert the feed item appears via SSE without reload", and nothing else on
// the admin API can produce a *new* feed row on demand: `POST /admin/seed` is
// deterministic and idempotent by construction (its event ids are derived, not
// minted), so the second call inserts nothing and publishes nothing.
//
// It is also the dev-loop tool the plan implies and does not otherwise provide:
// push one event and watch it come out of the live feed, without launching the
// game or the simulator.
//
// # What it is not
//
// It is not an ingest bypass anybody can reach: the admin mux is loopback-only
// and never proxied (§3, §5.9). It skips the §4.5.3 auth chain — that is the
// point of an admin path — but nothing downstream of it: envelopes are validated
// by the same decoder `/v1/ingest` uses, rows go through the same
// `INSERT OR IGNORE` dedup (D19), and the projector folds them with the same
// folds. What it does not exercise is the license/proof verification, which is
// covered end-to-end by the ingest integration tests and by `make e2e-full`.
func (s *Server) RegisterEvents() {
	s.mux.HandleFunc("POST /admin/events", s.handleEvents)
}

// EventsRequest is `POST /admin/events`.
type EventsRequest struct {
	// Handle names the player the events are attributed to. It must already
	// exist — this endpoint creates no accounts, so a typo fails loudly rather
	// than quietly filling the database with a player nobody can log in as.
	Handle string `json:"handle"`
	// Events are §4.1 envelopes. `id`, `session` and `wall_t` may be omitted and
	// are filled in; `type` must be in the §4.2 taxonomy.
	Events []json.RawMessage `json:"events"`
}

// EventsResponse is what `POST /admin/events` returns.
type EventsResponse struct {
	Handle   string `json:"handle"`
	PlayerID int64  `json:"player_id"`
	Accepted int    `json:"accepted"`
	Deduped  int    `json:"deduped"`
	// FoldedTo is the projector checkpoint once the new events have been folded.
	// The endpoint waits for it, so a caller can post an event and immediately
	// assert on a board — the feed row has already been published by then.
	FoldedTo int64 `json:"folded_to"`
}

// adminEnvelope is the §4.1 envelope with the fields this endpoint fills in made
// optional. It is deliberately not `ingest`'s decoder type: that one is strict
// about a wire format, and this is a hand-written dev input.
type adminEnvelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Ver     int             `json:"ver"`
	Flight  string          `json:"flight"`
	Session string          `json:"session"`
	Career  string          `json:"career"`
	SimT    *float64        `json:"sim_t"`
	WallT   int64           `json:"wall_t"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Events == nil {
		fail(w, authz.CodeInternal, "this server has no event store")
		return
	}
	var req EventsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Handle == "" || len(req.Events) == 0 {
		fail(w, authz.CodeBadRequest, "handle and at least one event are required")
		return
	}

	ctx := r.Context()
	handle, err := s.deps.Events.HandleByLC(ctx, req.Handle)
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, authz.CodeNotFound, "no such handle: "+req.Handle)
		return
	case err != nil:
		s.deps.Log.Error("admin events: handle lookup failed", "handle", req.Handle, "err", err)
		fail(w, authz.CodeInternal, "could not resolve the handle")
		return
	}

	now := s.deps.Now()
	evs := make([]store.Event, 0, len(req.Events))
	for i, raw := range req.Events {
		ev, err := decodeAdminEvent(raw, now)
		if err != nil {
			fail(w, authz.CodeMalformedBatch, fmt.Sprintf("event %d: %s", i, err))
			return
		}
		evs = append(evs, ev)
	}

	out := EventsResponse{Handle: handle.Handle, PlayerID: handle.PlayerID}
	err = s.WithWriteLock(func() error {
		var err error
		// Writing to events.db outside the ingest writer goroutine is exactly
		// what the §5.4 admin mutex exists for.
		out.Accepted, out.Deduped, err = s.deps.Events.InsertEvents(ctx, nil, handle.PlayerID, evs)
		return err
	})
	if err != nil {
		s.deps.Log.Error("admin events: insert failed", "handle", handle.Handle, "err", err)
		fail(w, authz.CodeInternal, "could not insert the events")
		return
	}

	// Fold before answering: the caller's next act is to assert on a board or a
	// feed line, and the feed row is published by the fold's commit (§5.6).
	if p := s.projections.Projector; p != nil {
		prog, err := p.Drain(ctx)
		if err != nil {
			s.deps.Log.Error("admin events: folding failed", "err", err)
			fail(w, authz.CodeInternal, "inserted, but the projections could not be folded")
			return
		}
		out.FoldedTo = prog.LastSeq
	}

	s.deps.Log.Info("events posted through the admin api",
		"handle", out.Handle, "accepted", out.Accepted, "deduped", out.Deduped, "folded_to", out.FoldedTo)
	writeJSON(w, http.StatusOK, out)
}

// DefaultCareer is the §4.1 career id a hand-written admin event lands in when
// it names none — the demo dataset's single career.
const DefaultCareer = "0000000000000000"

// decodeAdminEvent turns one hand-written envelope into a storable event,
// minting the identifiers a human should not have to.
func decodeAdminEvent(raw json.RawMessage, now time.Time) (store.Event, error) {
	var env adminEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return store.Event{}, fmt.Errorf("not a §4.1 envelope: %w", err)
	}
	if !ingest.KnownType(env.Type) {
		// Same rule as §4.1: an unknown type is version skew, and it must be
		// loud rather than stored.
		return store.Event{}, fmt.Errorf("unknown event type %q", env.Type)
	}

	ev := store.Event{Type: env.Type, Ver: max(env.Ver, 1), Payload: env.Payload}
	if len(ev.Payload) == 0 {
		ev.Payload = json.RawMessage(`{}`)
	}

	var err error
	if ev.ID, err = parseOrMint(env.ID, now); err != nil {
		return store.Event{}, fmt.Errorf("id: %w", err)
	}
	if ev.SessionID, err = parseOrMint(env.Session, now); err != nil {
		return store.Event{}, fmt.Errorf("session: %w", err)
	}
	// flight is genuinely optional (§4.1: null for session and roster events),
	// so an empty string stays zero rather than being minted.
	if env.Flight != "" {
		if ev.FlightID, err = ids.Parse(env.Flight); err != nil {
			return store.Event{}, fmt.Errorf("flight: %w", err)
		}
	}
	// career is a §4.1 envelope key with no sensible mint: a hand-written event
	// belongs to whatever career the author says, and to DefaultCareer when they
	// do not care. Making one up per event would put every seeded event in its
	// own career and silently empty the career-time boards.
	ev.Career = env.Career
	if ev.Career == "" {
		ev.Career = DefaultCareer
	}
	if !ingest.ValidCareer(ev.Career) {
		return store.Event{}, fmt.Errorf("career: not 16 lowercase Crockford base32 characters")
	}
	if env.SimT != nil {
		ev.SimTime.Float64, ev.SimTime.Valid = *env.SimT, true
	}
	if ev.WallTime = env.WallT; ev.WallTime == 0 {
		ev.WallTime = now.UnixMilli()
	}
	return ev, nil
}

func parseOrMint(s string, now time.Time) (ids.ID, error) {
	if s == "" {
		return ids.NewAt(now)
	}
	return ids.Parse(s)
}
