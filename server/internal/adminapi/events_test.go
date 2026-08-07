package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// eventsServer is newServer plus the `POST /admin/events` route. It is not
// mounted by New: catlogd registers it explicitly, so a build that does not want
// an ingest-bypassing write path simply does not call RegisterEvents.
func eventsServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, srv := newServer(t)
	s.RegisterEvents()
	return s, srv
}

// raw marshals a hand-written envelope the way a caller would send it.
func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPostEventsStoresAndFolds is the path §8's feed spec depends on: a handle
// that exists, envelopes with the identifiers filled in, and rows in events.db.
func TestPostEventsStoresAndFolds(t *testing.T) {
	s, srv := eventsServer(t)

	if _, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "demo_ace"}); body["jkt"] == nil {
		t.Fatalf("could not create the handle: %v", body)
	}

	flight := ids.MustNew().String()
	res, body := post(t, srv, "/admin/events", EventsRequest{
		Handle: "demo_ace",
		Events: []json.RawMessage{
			raw(t, map[string]any{
				"type":    "vehicle.rud",
				"flight":  flight,
				"sim_t":   12.5,
				"payload": map[string]any{"cause": "collision", "speed_ms": 321.0, "body": "duna", "crew_count": 1},
			}),
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", res.StatusCode, body)
	}
	if got := body["accepted"]; got != float64(1) {
		t.Errorf("accepted = %v, want 1", got)
	}

	// The identifiers the caller omitted were minted, not left zero: an event
	// with no id would collide with every other one under the (player, event_id)
	// dedup index (D19).
	rows, err := s.deps.Events.EventsSince(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d events, want 1", len(rows))
	}
	switch {
	case rows[0].ID == ids.Zero:
		t.Error("the event id was not minted")
	case rows[0].SessionID == ids.Zero:
		t.Error("the session id was not minted")
	case rows[0].Type != "vehicle.rud":
		t.Errorf("type = %q", rows[0].Type)
	case rows[0].WallTime == 0:
		t.Error("wall_t was not filled in")
	}

	// Posting the same body twice inserts twice, because the ids are minted per
	// request rather than derived. That is the difference from /admin/seed and
	// the reason this endpoint exists.
	if _, body := post(t, srv, "/admin/events", EventsRequest{
		Handle: "demo_ace",
		Events: []json.RawMessage{raw(t, map[string]any{"type": "vehicle.staging", "flight": flight, "payload": map[string]any{"stage_index": 1}})},
	}); body["accepted"] != float64(1) {
		t.Errorf("second post accepted = %v, want 1", body["accepted"])
	}
}

func TestPostEventsRejectsWhatItShould(t *testing.T) {
	_, srv := eventsServer(t)
	if _, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "demo_ace"}); body["jkt"] == nil {
		t.Fatalf("could not create the handle: %v", body)
	}

	cases := []struct {
		name string
		req  EventsRequest
		want string
	}{
		{
			name: "unknown handle",
			req:  EventsRequest{Handle: "nobody", Events: []json.RawMessage{raw(t, map[string]any{"type": "vehicle.staging"})}},
			want: "not_found",
		},
		{
			// §4.1: an unknown type is version skew and must be loud, not stored.
			name: "unknown type",
			req:  EventsRequest{Handle: "demo_ace", Events: []json.RawMessage{raw(t, map[string]any{"type": "vehicle.teleported"})}},
			want: "malformed_batch",
		},
		{
			name: "unparseable flight id",
			req: EventsRequest{Handle: "demo_ace", Events: []json.RawMessage{
				raw(t, map[string]any{"type": "vehicle.staging", "flight": "not-a-ulid"}),
			}},
			want: "malformed_batch",
		},
		{
			name: "no events",
			req:  EventsRequest{Handle: "demo_ace"},
			want: "bad_request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := post(t, srv, "/admin/events", tc.req)
			if res.StatusCode == http.StatusOK {
				t.Fatalf("expected a rejection, got 200: %v", body)
			}
			if got := body["error"]; got != tc.want {
				t.Errorf("error = %v, want %q (%v)", got, tc.want, body)
			}
		})
	}
}
