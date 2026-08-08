package readapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// aFlight seeds one player with a small, realistic history: a session, a flight
// with three events in it, and a roster snapshot with no flight at all (§4.1).
func aFlight(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	id := f.player("whiskers")
	f.rawEvent(id, "session.started", sharedCareer, map[string]any{
		"mod_ver": "0.1.0", "game_build": "2026.8.5.5168", "install": sharedInstall,
	})
	f.rawEvent(id, "flight.started", sharedCareer, map[string]any{
		"vehicle_name": "Lawn Dart", "body": "duna", "mass_kg": 8200, "part_count": 19, "crew_count": 1,
	})
	f.rawEvent(id, "vehicle.impact", sharedCareer, map[string]any{
		"speed_ms": 214, "energy_j": 4.8e7, "survived": true, "launch_pad": false,
		"body": "duna", "crew_count": 1,
	})
	f.rawEvent(id, "flight.ended", sharedCareer, map[string]any{"reason": "recovered", "crew_count": 1})
	f.rawEvent(id, "roster.snapshot", sharedCareer, map[string]any{
		"kittens": []any{map[string]any{"kid": sharedKid, "name": "Ferro", "travelled_m": 205000}},
	})
	return f
}

func TestEventViewShowsWhatCatlogRecorded(t *testing.T) {
	f := aFlight(t)
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events"))

	if page.Handle != "whiskers" || page.Limit != readapi.DefaultEventLimit {
		t.Errorf("response header = %+v", page)
	}
	if len(page.Events) != 5 {
		t.Fatalf("%d events, want 5: %+v", len(page.Events), page.Events)
	}
	// Newest first — the order a log is read in.
	if page.Events[0].Type != "roster.snapshot" || page.Events[4].Type != "session.started" {
		t.Errorf("order = %s … %s, want newest first", page.Events[0].Type, page.Events[4].Type)
	}
	// The log is exhausted, so there is no cursor. A client pages until this is
	// absent, and here it must be absent on the first page.
	if page.Next != "" {
		t.Errorf("next = %q on a page that reached the end of the log", page.Next)
	}

	impact := page.Events[2]
	if impact.Type != "vehicle.impact" {
		t.Fatalf("event 2 = %s", impact.Type)
	}
	for what, got := range map[string]string{"id": impact.ID, "session": impact.Session, "career": impact.Career} {
		if got == "" {
			t.Errorf("event has no %s", what)
		}
	}
	if impact.Recv == 0 || impact.Ver != 1 {
		t.Errorf("event = %+v", impact)
	}
	// The payload is the gameplay data, verbatim.
	var p struct {
		SpeedMs  float64 `json:"speed_ms"`
		EnergyJ  float64 `json:"energy_j"`
		Survived bool    `json:"survived"`
		Body     string  `json:"body"`
	}
	if err := json.Unmarshal(impact.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SpeedMs != 214 || p.EnergyJ != 4.8e7 || !p.Survived || p.Body != "duna" {
		t.Errorf("payload = %s", impact.Payload)
	}
}

func TestEventViewPagesByCursor(t *testing.T) {
	f := newFixture(t)
	id := f.player("whiskers")
	for i := range 12 {
		f.rawEvent(id, "vehicle.staging", sharedCareer, map[string]any{"stage_index": i})
	}

	first := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events?limit=5"))
	if len(first.Events) != 5 || first.Next == "" {
		t.Fatalf("first page: %d events, next %q", len(first.Events), first.Next)
	}
	seen := map[string]bool{}
	page := first
	for pages := 0; ; pages++ {
		for _, ev := range page.Events {
			if seen[ev.ID] {
				t.Fatalf("event %s came back twice; the cursor is not exclusive", ev.ID)
			}
			seen[ev.ID] = true
		}
		if page.Next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("the cursor never terminated")
		}
		page = decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events?limit=5&before="+page.Next))
	}
	if len(seen) != 12 {
		t.Errorf("paged through %d events, want 12", len(seen))
	}
}

func TestEventViewFiltersByType(t *testing.T) {
	f := aFlight(t)
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events?type=vehicle.impact"))
	if page.Type != "vehicle.impact" {
		t.Errorf("type not echoed: %+v", page)
	}
	if len(page.Events) != 1 || page.Events[0].Type != "vehicle.impact" {
		t.Fatalf("events = %+v", page.Events)
	}
	// A type nobody has ever emitted is an empty page, not an error: the filter
	// is a convenience over the log, not a schema check.
	empty := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events?type=not.a.type"))
	if len(empty.Events) != 0 {
		t.Errorf("events = %+v", empty.Events)
	}
}

func TestEventViewClampsAndValidates(t *testing.T) {
	f := aFlight(t)
	for _, tc := range []struct {
		query string
		want  int
	}{{"?limit=9999", readapi.MaxEventLimit}, {"?limit=0", 1}, {"", readapi.DefaultEventLimit}} {
		got := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events"+tc.query))
		if got.Limit != tc.want {
			t.Errorf("limit%q echoed %d, want %d", tc.query, got.Limit, tc.want)
		}
	}
	for _, bad := range []string{"?before=banana", "?before=-1", "?limit=banana"} {
		if rec := f.get("/v1/players/whiskers/events" + bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", bad, rec.Code)
		}
	}
}

func TestEventViewIsNotABanOracle(t *testing.T) {
	// Unknown, retired and banned are one answer here too (§4.8).
	f := aFlight(t)
	f.player("was_banned")
	f.rawEvent(f.byHandle["was_banned"], "vehicle.staging", sharedCareer, map[string]any{"stage_index": 0})
	f.ban("was_banned")

	for _, handle := range []string{"was_banned", "never_existed"} {
		rec := f.get("/v1/players/" + handle + "/events")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", handle, rec.Code)
		}
	}
}

func TestEventViewOfAPlayerWhoHasShippedNothing(t *testing.T) {
	f := newFixture(t)
	f.player("fresh_cat")
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/fresh_cat/events"))
	if len(page.Events) != 0 || page.Next != "" {
		t.Errorf("got %+v", page)
	}
	// `[]`, never `null`.
	if body := f.get("/v1/players/fresh_cat/events").Body.String(); !strings.Contains(body, `"events":[]`) {
		t.Errorf("body = %s", body)
	}
}

// The privacy page (§5.7) promises players that "flights flagged as cheated are
// stored and shown to you, but score nothing and never appear publicly". The
// boards keep that by construction — a flagged flight is never folded, so there
// is no row to publish — but this endpoint reads the log directly, and would
// publish every event of one unless it asks.
//
// It is also the only reading of the flags Constitution §8 allows: a flag may
// exclude a flight from the boards and nothing else. A browsable public list of
// somebody's flagged flights is a durable public consequence attached to a
// person, and the flags include `tuning` and `console` — a player who opened a
// debug window is not a cheat and must not be published as one.
func TestEventViewNeverPublishesAFlaggedFlight(t *testing.T) {
	f := newFixture(t)
	id := f.player("whiskers")
	clean, cheated := testutil.ULID(t), testutil.ULID(t)

	f.rawEvent(id, "session.started", sharedCareer, map[string]any{"mod_ver": "0.1.0"})
	f.rawFlightEvent(id, clean, "flight.started", sharedCareer, map[string]any{"vehicle_name": "Honest Rocket"})
	f.rawFlightEvent(id, clean, "vehicle.impact", sharedCareer, map[string]any{"speed_ms": 214, "body": "duna"})

	f.rawFlightEvent(id, cheated, "flight.started", sharedCareer, map[string]any{"vehicle_name": "Definitely Legitimate"})
	f.rawFlightEvent(id, cheated, "flight.flagged", sharedCareer, map[string]any{"flag": "teleport", "detail": "moved 4.2e6 m in one frame"})
	f.rawFlightEvent(id, cheated, "vehicle.impact", sharedCareer, map[string]any{"speed_ms": 999, "body": "kerbin"})
	f.flagFlight(id, cheated, stats.FlagTeleport)

	// Every way of asking, including the two that would reach it most directly:
	// a type only the flagged flight emitted, and paging over the whole log.
	for _, path := range []string{
		"/v1/players/whiskers/events",
		"/v1/players/whiskers/events?limit=1",
		"/v1/players/whiskers/events?type=vehicle.impact",
		"/v1/players/whiskers/events?type=flight.flagged",
		"/v1/players/whiskers/events?type=flight.started",
	} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		for url, page := path, (readapi.EventsResponse{}); ; url = path + sep + "before=" + page.Next {
			rec := f.get(url)
			page = decode[readapi.EventsResponse](t, rec)
			body := rec.Body.String()
			for _, leak := range []string{
				"Definitely Legitimate", // the flagged flight's vehicle
				"999",                   // its impact, which scores nothing
				"teleport",              // the flag itself
				ids.String(cheated),     // and the flight id it all hangs off
			} {
				if strings.Contains(body, leak) {
					t.Fatalf("GET %s published %q from a flagged flight", url, leak)
				}
			}
			if page.Next == "" {
				break
			}
		}
	}

	// The honest flight is untouched: the exclusion is per flight, not per player.
	all := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events"))
	if len(all.Events) != 3 {
		t.Fatalf("%d events, want the session and the two of the honest flight: %+v", len(all.Events), all.Events)
	}
	if !strings.Contains(f.get("/v1/players/whiskers/events").Body.String(), "Honest Rocket") {
		t.Error("the honest flight was excluded too")
	}
}

// flagFlight writes the flight_state row the projector's flightFold would have
// written for a flagged flight. Hand-written for the same reason the rest of
// this fixture is: a filtering bug must not be able to hide behind a fold bug.
func (f *fixture) flagFlight(playerID int64, flight ids.ID, flags int64) {
	f.t.Helper()
	f.projWrite(`INSERT INTO flight_state (flight_id, player_id, flags, started_seq) VALUES (?, ?, ?, 1)`,
		ids.Bytes(flight), playerID, flags)
}
