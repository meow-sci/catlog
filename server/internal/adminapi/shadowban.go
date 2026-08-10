package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The shadow-ban half of the §5.9 moderation routes.
//
// Each of the three mutating verbs ends the same way — queue a rebuild — and
// that is the part worth understanding. Withholding a player's events removes
// them from the *log* immediately, but the boards are a separate file folded
// from the log by a cursor that only moves forward (§5.6), so the records those
// events earned sit there until the whole history is folded again. The handle
// directory hides the player in the meantime, and the rebuild makes it true.
//
// A rebuild is minutes at production size, so it is queued rather than waited
// on: the operator gets an answer immediately, with a job to watch.

// ShadowbanRequest names an account, exactly as [BanRequest] does.
type ShadowbanRequest struct {
	Handle string `json:"handle,omitempty"`
	Sub    string `json:"sub,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (r ShadowbanRequest) target() identity.Target {
	return identity.Target{Handle: r.Handle, Sub: r.Sub}
}

// ShadowbanResponse wraps a moderation result with the rebuild it triggered, so
// one call tells an operator both what happened and what is now running.
type ShadowbanResponse struct {
	Shadowban   *identity.ShadowbanResult      `json:"shadowban,omitempty"`
	Unshadowban *identity.UnshadowbanResult    `json:"unshadowban,omitempty"`
	Deleted     *identity.DeleteWithheldResult `json:"deleted,omitempty"`
	// Rebuild is the job that will remove — or restore — this player's rows on
	// every board. Absent only when no projector is running.
	Rebuild *rebuildHandle `json:"rebuild,omitempty"`
}

// rebuildHandle is the minimum an operator needs to follow the job up: which
// phase it is in and why it started. The full status is one GET away.
type rebuildHandle struct {
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
	Queued bool   `json:"queued"`
}

func (s *Server) handleShadowban(w http.ResponseWriter, r *http.Request) {
	var req ShadowbanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.ShadowbanResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), req.target())
		if err != nil {
			return err
		}
		res, err = mod.Shadowban(r.Context(), player, req.Reason)
		return err
	})
	if s.moderationFailed(w, err, "shadowban") {
		return
	}

	out := ShadowbanResponse{Shadowban: &res}
	out.Rebuild = s.queueRebuild("a player was shadowbanned")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUnshadowban(w http.ResponseWriter, r *http.Request) {
	var req ShadowbanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.UnshadowbanResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), req.target())
		if err != nil {
			return err
		}
		res, err = mod.Unshadowban(r.Context(), player)
		return err
	})
	if s.moderationFailed(w, err, "unshadowban") {
		return
	}

	out := ShadowbanResponse{Unshadowban: &res}
	out.Rebuild = s.queueRebuild("a shadow ban was lifted")
	writeJSON(w, http.StatusOK, out)
}

// handleShadowbanDelete is the irreversible one, and it is deliberately its own
// route rather than a flag on `POST /admin/shadowban`. A destructive action
// should not be one mistyped boolean away from a reversible one.
//
// No rebuild is queued: the events were already absent from the log and from
// every projection, so destroying them changes nothing a board can see. Saying
// so here is the point — an operator who expects a rebuild and does not get one
// should find the reason next to the code that decided.
func (s *Server) handleShadowbanDelete(w http.ResponseWriter, r *http.Request) {
	var req ShadowbanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.DeleteWithheldResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), req.target())
		if err != nil {
			return err
		}
		res, err = mod.DeleteWithheld(r.Context(), player)
		return err
	})
	if s.moderationFailed(w, err, "delete the withheld events of") {
		return
	}
	writeJSON(w, http.StatusOK, ShadowbanResponse{Deleted: &res})
}

// ShadowbanListResponse is `GET /admin/shadowban` (§5.9): the review queue.
type ShadowbanListResponse struct {
	Shadowbans []ShadowbanRow `json:"shadowbans"`
	// Total is how many events are withheld across every shadow ban — the one
	// number that says how much data is being held pending review.
	Total int64 `json:"total_events"`
}

// ShadowbanRow is one roster entry, resolved to something an operator can name.
type ShadowbanRow struct {
	store.Shadowban
	Sub string `json:"sub"`
	// Handles are the handles the account still owns. They do not resolve
	// publicly while the ban is on, but they are how a human recognises who
	// this is.
	Handles []string `json:"handles"`
}

func (s *Server) handleShadowbanList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Events == nil {
		fail(w, authz.CodeInternal, "no database is open on this server")
		return
	}
	ctx := r.Context()

	rows, err := s.deps.Events.Shadowbans(ctx)
	if err != nil {
		s.fail(w, err, "list the shadow bans")
		return
	}

	out := ShadowbanListResponse{Shadowbans: make([]ShadowbanRow, 0, len(rows))}
	for _, sb := range rows {
		row := ShadowbanRow{Shadowban: sb}
		if p, err := s.deps.Events.PlayerByID(ctx, sb.PlayerID); err == nil {
			row.Sub = p.UserKey.B64U()
		}
		handles, err := s.deps.Events.HandlesForPlayer(ctx, sb.PlayerID)
		if err != nil {
			s.fail(w, err, "list the shadow bans")
			return
		}
		for _, h := range handles {
			row.Handles = append(row.Handles, h.Handle)
		}
		out.Shadowbans = append(out.Shadowbans, row)
		out.Total += sb.Events
	}
	writeJSON(w, http.StatusOK, out)
}

// ShadowbanEventsResponse is `GET /admin/shadowban/events` — the content the
// review actually reads.
type ShadowbanEventsResponse struct {
	Sub    string              `json:"sub"`
	Events []ShadowbanEventRow `json:"events"`
	Next   int64               `json:"next_before,omitempty"`
	Ban    *store.Shadowban    `json:"shadowban,omitempty"`
}

// ShadowbanEventRow is one withheld event, unredacted.
//
// This endpoint is loopback-only admin (§5.9) and its entire purpose is letting
// a human decide whether the data should be restored or destroyed, so the
// §4.8 redaction the public event views apply would defeat it. That is the one
// place in catlog where a raw payload leaves the database, and it is worth
// being explicit that it is deliberate.
type ShadowbanEventRow struct {
	Seq      int64           `json:"seq"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Ver      int             `json:"ver"`
	Flight   string          `json:"flight,omitempty"`
	Session  string          `json:"session,omitempty"`
	Career   string          `json:"career,omitempty"`
	SimTime  *float64        `json:"sim_t,omitempty"`
	WallTime int64           `json:"wall_t"`
	RecvTime int64           `json:"recv_t"`
	Payload  json.RawMessage `json:"payload"`
}

// shadowbanEventPage bounds one review page.
const shadowbanEventPage = 200

func (s *Server) handleShadowbanEvents(w http.ResponseWriter, r *http.Request) {
	mod := s.identity.Moderator
	if mod == nil || s.deps.Events == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}
	ctx := r.Context()

	player, err := mod.Resolve(ctx, identity.Target{
		Handle: r.URL.Query().Get("handle"),
		Sub:    r.URL.Query().Get("sub"),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, authz.CodeNotFound, "no such account")
			return
		}
		if errors.Is(err, identity.ErrTargetRequired) {
			fail(w, authz.CodeBadRequest, "supply a handle or a sub")
			return
		}
		s.fail(w, err, "resolve the account")
		return
	}

	limit := shadowbanEventPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			fail(w, authz.CodeBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, shadowbanEventPage)
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			fail(w, authz.CodeBadRequest, "before must be a non-negative seq")
			return
		}
		before = n
	}

	evs, err := s.deps.Events.WithheldEvents(ctx, player.ID, before, limit)
	if err != nil {
		s.fail(w, err, "read the withheld events")
		return
	}

	out := ShadowbanEventsResponse{Sub: player.UserKey.B64U(), Events: make([]ShadowbanEventRow, 0, len(evs))}
	if sb, err := s.deps.Events.ShadowbanFor(ctx, player.ID); err == nil {
		out.Ban = &sb
	}
	for _, se := range evs {
		row := ShadowbanEventRow{
			Seq: se.Seq, ID: ids.String(se.ID), Type: se.Type, Ver: se.Ver,
			Career: se.Career, WallTime: se.WallTime, RecvTime: se.RecvTime,
			Payload: se.Payload,
		}
		if se.FlightID != ids.Zero {
			row.Flight = ids.String(se.FlightID)
		}
		if se.SessionID != ids.Zero {
			row.Session = ids.String(se.SessionID)
		}
		if se.SimTime.Valid {
			v := se.SimTime.Float64
			row.SimTime = &v
		}
		out.Events = append(out.Events, row)
	}
	// The cursor is the oldest seq on this page, so the next request continues
	// from it — the same shape as the public paginated log (§4.8).
	if len(evs) == limit {
		out.Next = evs[len(evs)-1].Seq
	}
	writeJSON(w, http.StatusOK, out)
}

// queueRebuild asks the projector for a rebuild and reports the handle, or nil
// when there is no projector (a server running ingest only).
func (s *Server) queueRebuild(reason string) *rebuildHandle {
	p := s.projections.Projector
	if p == nil {
		return nil
	}
	status := p.RequestRebuild(reason)
	return &rebuildHandle{Phase: string(status.Phase), Reason: status.Reason, Queued: status.Queued}
}
