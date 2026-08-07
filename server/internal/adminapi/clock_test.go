package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/clock"
)

// postClockStatus is a raw POST that returns only the status.
//
// The package's shared post helper decodes the body as JSON and fails when it
// cannot — which is right for every route that exists, and wrong here: the
// whole point of these two cases is that the route does NOT exist, and
// http.ServeMux answers an absent route with plain text.
func postClockStatus(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	res, err := srv.Client().Post(srv.URL+"/admin/clock", "application/json",
		strings.NewReader(`{"advance_ms":1000}`))
	if err != nil {
		t.Fatalf("POST /admin/clock: %v", err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// getClockJSON is a GET counterpart to this package's post helper, named for
// its one caller so it cannot collide with a general-purpose one later.
func getClockJSON(t *testing.T, srv *httptest.Server) (*http.Response, map[string]any) {
	t.Helper()
	res, err := http.Get(srv.URL + "/admin/clock")
	if err != nil {
		t.Fatalf("GET /admin/clock: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })

	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode /admin/clock: %v", err)
	}
	return res, body
}

// TestClockRoutesAreAbsentUnlessRegistered is the property that actually keeps
// this out of production: on a server that did not enable `[server]
// clock_control`, catlogd never calls RegisterClock, so the route does not
// exist rather than existing and refusing.
func TestClockRoutesAreAbsentUnlessRegistered(t *testing.T) {
	_, srv := newServer(t)

	if got := postClockStatus(t, srv); got != http.StatusNotFound {
		t.Errorf("POST /admin/clock on an unregistered server = %d, want 404", got)
	}
}

// TestRegisterClockIgnoresANilClock: a caller that forgets to build one must
// not get a route that panics.
func TestRegisterClockIgnoresANilClock(t *testing.T) {
	s, srv := newServer(t)
	s.RegisterClock(ClockDeps{Clock: nil})

	if got := postClockStatus(t, srv); got != http.StatusNotFound {
		t.Errorf("a nil clock mounted a route: %d", got)
	}
}

// TestClockAdvanceMovesTheServersNow walks the clock the way a long-span
// simulation does — in steps — and checks the offset accumulates.
func TestClockAdvanceMovesTheServersNow(t *testing.T) {
	s, srv := newServer(t)
	c := clock.New(true)
	s.RegisterClock(ClockDeps{Clock: c})

	day := (24 * time.Hour).Milliseconds()
	for i := range 3 {
		res, body := post(t, srv, "/admin/clock", map[string]any{"advance_ms": day})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("advance %d = %d: %v", i, res.StatusCode, body)
		}
	}

	if got, want := c.Offset(), 72*time.Hour; got != want {
		t.Errorf("offset = %v, want %v", got, want)
	}

	res, body := getClockJSON(t, srv)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/clock = %d", res.StatusCode)
	}
	offset, _ := body["offset_ms"].(float64)
	if int64(offset) != (72 * time.Hour).Milliseconds() {
		t.Errorf("offset_ms = %v, want %d", offset, (72 * time.Hour).Milliseconds())
	}
	if ctrl, _ := body["controllable"].(bool); !ctrl {
		t.Error("controllable = false on a controllable clock")
	}
	// now_ms - real_ms must be the offset, or the response is describing two
	// different clocks.
	now, _ := body["now_ms"].(float64)
	real, _ := body["real_ms"].(float64)
	if int64(now-real) != int64(offset) {
		t.Errorf("now_ms - real_ms = %v, want offset_ms %v", now-real, offset)
	}
}

// TestClockJumpsToAnAbsoluteInstant is the form a harness wants when it is
// filling in a specific month.
func TestClockJumpsToAnAbsoluteInstant(t *testing.T) {
	s, srv := newServer(t)
	c := clock.New(true)
	s.RegisterClock(ClockDeps{Clock: c})

	target := time.Now().Add(400 * 24 * time.Hour).UnixMilli()
	res, body := post(t, srv, "/admin/clock", map[string]any{"at_ms": target})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("at_ms = %d: %v", res.StatusCode, body)
	}
	// A second of slack: the clock reads real time when it computes the jump.
	if got := c.Now().UnixMilli(); got < target-2000 || got > target+2000 {
		t.Errorf("now = %d, want ≈ %d", got, target)
	}
}

// TestAnUncontrollableClockRefusesEvenWhenMounted is defence in depth: if the
// flag and the clock ever disagree, the clock wins.
func TestAnUncontrollableClockRefusesEvenWhenMounted(t *testing.T) {
	s, srv := newServer(t)
	s.RegisterClock(ClockDeps{Clock: clock.New(false)})

	res, _ := post(t, srv, "/admin/clock", map[string]any{"advance_ms": 1000})
	if res.StatusCode == http.StatusOK {
		t.Error("an uncontrollable clock accepted a move")
	}
}

// TestClockRejectsAnEmptyMove: silently doing nothing is the wrong answer to
// "move the clock by nothing in particular".
func TestClockRejectsAnEmptyMove(t *testing.T) {
	s, srv := newServer(t)
	s.RegisterClock(ClockDeps{Clock: clock.New(true)})

	res, _ := post(t, srv, "/admin/clock", map[string]any{})
	if res.StatusCode == http.StatusOK {
		t.Error("a body with neither advance_ms nor at_ms was accepted")
	}
}
