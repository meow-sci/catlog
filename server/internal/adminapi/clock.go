package adminapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/clock"
)

// ClockDeps carries the server clock for the §5.9 admin routes.
type ClockDeps struct {
	// Clock is catlogd's shared clock. Nil leaves the routes unmounted.
	Clock *clock.Clock
}

// RegisterClock mounts the development-only clock routes.
//
// # Why this exists
//
// Every timestamp catlog treats as authoritative is server-generated, so the
// server's clock is the only thing that decides which day, week, month or year
// a leaderboard row belongs to. A rolling yearly board cannot be tested by
// waiting a year. This is how a development build moves that clock.
//
// # Why it is safe
//
// Four independent things have to line up before this is reachable, and the
// first two are refusals rather than warnings:
//
//  1. `[server] clock_control` defaults to false, and [config.Config.Validate]
//     refuses to start at all if it is true on an https deployment.
//  2. [clock.Clock] itself returns [clock.ErrNotControllable] unless it was
//     built controllable, so even a caller that reached the handler gets
//     nothing.
//  3. The route is on the admin mux, which binds loopback and 403s any
//     non-loopback peer (see the package doc).
//  4. catlogd only calls this method when the flag is on, so on a normal
//     server the route does not exist and answers 404.
//
// Following the established idiom, this is its own Register… entry point rather
// than an edit to [New].
func (s *Server) RegisterClock(deps ClockDeps) {
	if deps.Clock == nil {
		return
	}
	s.clock = deps
	s.mux.HandleFunc("GET /admin/clock", s.handleClockRead)
	s.mux.HandleFunc("POST /admin/clock", s.handleClockMove)
}

// ClockRequest is the `POST /admin/clock` body. Exactly one field is used:
// AdvanceMS when it is non-zero, otherwise AtMS.
type ClockRequest struct {
	// AdvanceMS moves the clock relative to where it is now. Negative is
	// allowed.
	AdvanceMS int64 `json:"advance_ms,omitempty"`
	// AtMS jumps the clock to an absolute unix-millisecond instant.
	AtMS int64 `json:"at_ms,omitempty"`
}

// ClockResponse is what both clock routes answer.
type ClockResponse struct {
	// NowMS is the clock's reading, in unix milliseconds — the value the next
	// ingested event would carry as `recv_time`.
	NowMS int64 `json:"now_ms"`
	// RealMS is the underlying wall clock, so a caller can see the two diverge.
	RealMS int64 `json:"real_ms"`
	// OffsetMS is NowMS - RealMS.
	OffsetMS int64 `json:"offset_ms"`
	// Controllable reports whether this catlogd will accept a move.
	Controllable bool `json:"controllable"`
}

func (s *Server) handleClockRead(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.clockResponse())
}

func (s *Server) handleClockMove(w http.ResponseWriter, r *http.Request) {
	var req ClockRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	c := s.clock.Clock
	var err error
	switch {
	case req.AdvanceMS != 0:
		_, err = c.Advance(time.Duration(req.AdvanceMS) * time.Millisecond)
	case req.AtMS != 0:
		_, err = c.SetTo(time.UnixMilli(req.AtMS))
	default:
		fail(w, authz.CodeBadRequest, "give either advance_ms or at_ms")
		return
	}

	switch {
	case errors.Is(err, clock.ErrNotControllable):
		// Belt and braces: catlogd does not mount this route unless the flag is
		// on, so reaching here means the flag and the clock disagree.
		fail(w, authz.CodeBadRequest, err.Error())
		return
	case err != nil:
		fail(w, authz.CodeBadRequest, err.Error())
		return
	}

	res := s.clockResponse()
	// Loud on purpose. Moving the clock expires licenses (180 days) and
	// sessions (7 days), and a reader of these logs should be able to see the
	// moment the ground moved.
	s.deps.Log.Warn("server clock moved",
		"now_ms", res.NowMS, "offset_ms", res.OffsetMS,
		"advance_ms", req.AdvanceMS, "at_ms", req.AtMS)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) clockResponse() ClockResponse {
	c := s.clock.Clock
	now := c.Now()
	offset := c.Offset()
	return ClockResponse{
		NowMS:        now.UnixMilli(),
		RealMS:       now.Add(-offset).UnixMilli(),
		OffsetMS:     offset.Milliseconds(),
		Controllable: c.Controllable(),
	}
}
