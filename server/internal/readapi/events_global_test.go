package readapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The global raw log: `GET /v1/events`. The same log the per-player view pages,
// with the players mixed together — so these tests lean on the same fixtures
// and assert what is different: rows name their handle, and the drop rules that
// were implicit per player (you cannot ask for a banned player) hold per row.

func TestGlobalEventViewMixesPlayersNewestFirst(t *testing.T) {
	f := newFixture(t)
	alpha, beta := f.player("alpha_pilot"), f.player("beta_pilot")
	f.rawEvent(alpha, "session.started", sharedCareer, map[string]any{"mod_ver": "0.1.0"})
	f.rawEvent(beta, "session.started", sharedCareer, map[string]any{"mod_ver": "0.1.0"})
	f.rawEvent(alpha, "vehicle.staging", sharedCareer, map[string]any{"stage_index": 0})

	page := decode[readapi.EventsResponse](t, f.get("/v1/events"))
	if page.Limit != readapi.DefaultEventLimit {
		t.Errorf("limit = %d, want %d", page.Limit, readapi.DefaultEventLimit)
	}
	// The envelope names no handle — this page has every player's rows on it.
	if page.Handle != "" {
		t.Errorf("handle = %q on an unfiltered global page", page.Handle)
	}
	if len(page.Events) != 3 {
		t.Fatalf("%d events, want 3", len(page.Events))
	}
	// Newest first by seq, players interleaved as they arrived.
	if page.Events[0].Handle != "alpha_pilot" || page.Events[1].Handle != "beta_pilot" {
		t.Errorf("handles = %s, %s; want alpha_pilot, beta_pilot", page.Events[0].Handle, page.Events[1].Handle)
	}
	for i, ev := range page.Events {
		if ev.Seq == 0 {
			t.Errorf("event %d has no seq", i)
		}
		if i > 0 && page.Events[i-1].Seq <= ev.Seq {
			t.Errorf("seqs are not descending: %d then %d", page.Events[i-1].Seq, ev.Seq)
		}
		if ev.Handle == "" {
			t.Errorf("event %d names no handle on the global view", i)
		}
	}
	if page.Next != "" {
		t.Errorf("next = %q on a page that reached the end of the log", page.Next)
	}
}

// The per-player view carries seq too — it is the cursor value, and what a
// stream client merges by — and omits the per-row handle its envelope already
// names.
func TestPlayerEventViewCarriesSeqAndOmitsHandle(t *testing.T) {
	f := aFlight(t)
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/whiskers/events"))
	if len(page.Events) == 0 {
		t.Fatal("no events")
	}
	for i, ev := range page.Events {
		if ev.Seq == 0 {
			t.Errorf("event %d has no seq", i)
		}
	}
	// Exactly one "handle": the envelope's. The rows do not repeat it.
	if body := f.get("/v1/players/whiskers/events").Body.String(); strings.Count(body, `"handle":`) != 1 {
		t.Errorf("per-player rows repeat the handle: %s", body)
	}
}

func TestGlobalEventViewPagesByCursor(t *testing.T) {
	f := newFixture(t)
	alpha, beta := f.player("alpha_pilot"), f.player("beta_pilot")
	for i := range 6 {
		f.rawEvent(alpha, "vehicle.staging", sharedCareer, map[string]any{"stage_index": i})
		f.rawEvent(beta, "vehicle.staging", sharedCareer, map[string]any{"stage_index": i})
	}

	seen := map[string]bool{}
	page := decode[readapi.EventsResponse](t, f.get("/v1/events?limit=5"))
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
		page = decode[readapi.EventsResponse](t, f.get("/v1/events?limit=5&before="+page.Next))
	}
	if len(seen) != 12 {
		t.Errorf("paged through %d events, want 12", len(seen))
	}
}

// `?handle=` delegates to the same code as `/v1/players/{handle}/events`, so
// the two cannot disagree — and the global shape survives: the envelope echoes
// the handle and the rows still name it.
func TestGlobalEventViewFiltersByHandle(t *testing.T) {
	f := newFixture(t)
	alpha, beta := f.player("alpha_pilot"), f.player("beta_pilot")
	f.rawEvent(alpha, "vehicle.staging", sharedCareer, map[string]any{"stage_index": 0})
	f.rawEvent(beta, "vehicle.staging", sharedCareer, map[string]any{"stage_index": 1})

	page := decode[readapi.EventsResponse](t, f.get("/v1/events?handle=alpha_pilot"))
	if page.Handle != "alpha_pilot" {
		t.Errorf("handle not echoed: %+v", page)
	}
	if len(page.Events) != 1 || page.Events[0].Handle != "alpha_pilot" {
		t.Fatalf("events = %+v, want alpha_pilot's one row, named", page.Events)
	}

	// Unknown, retired and banned are the same one answer as the per-player
	// endpoint's 404 (§4.8) — this route must not become the ban oracle either.
	f.ban("beta_pilot")
	for _, handle := range []string{"beta_pilot", "never_existed"} {
		if rec := f.get("/v1/events?handle=" + handle); rec.Code != http.StatusNotFound {
			t.Errorf("?handle=%s returned %d, want 404", handle, rec.Code)
		}
	}
}

func TestGlobalEventViewFiltersByType(t *testing.T) {
	f := newFixture(t)
	alpha := f.player("alpha_pilot")
	f.rawEvent(alpha, "session.started", sharedCareer, map[string]any{"mod_ver": "0.1.0"})
	f.rawEvent(alpha, "vehicle.staging", sharedCareer, map[string]any{"stage_index": 0})

	page := decode[readapi.EventsResponse](t, f.get("/v1/events?type=vehicle.staging"))
	if page.Type != "vehicle.staging" {
		t.Errorf("type not echoed: %+v", page)
	}
	if len(page.Events) != 1 || page.Events[0].Type != "vehicle.staging" {
		t.Fatalf("events = %+v", page.Events)
	}
	if empty := decode[readapi.EventsResponse](t, f.get("/v1/events?type=not.a.type")); len(empty.Events) != 0 {
		t.Errorf("events = %+v", empty.Events)
	}
}

func TestGlobalEventViewClampsAndValidates(t *testing.T) {
	f := newFixture(t)
	f.player("alpha_pilot")
	for _, tc := range []struct {
		query string
		want  int
	}{{"?limit=9999", readapi.MaxEventLimit}, {"?limit=0", 1}, {"", readapi.DefaultEventLimit}} {
		if got := decode[readapi.EventsResponse](t, f.get("/v1/events"+tc.query)); got.Limit != tc.want {
			t.Errorf("limit%q echoed %d, want %d", tc.query, got.Limit, tc.want)
		}
	}
	for _, bad := range []string{"?before=banana", "?before=-1", "?limit=banana"} {
		if rec := f.get("/v1/events" + bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", bad, rec.Code)
		}
	}
	// `[]`, never `null`.
	if body := f.get("/v1/events?type=not.a.type").Body.String(); !strings.Contains(body, `"events":[]`) {
		t.Errorf("body = %s", body)
	}
}

// The per-row half of the drop rules: a banned player's events vanish from the
// global page (the per-player route already 404s them whole), and a flagged
// flight's events are excluded exactly as they are per player.
func TestGlobalEventViewDropsBannedAndFlagged(t *testing.T) {
	f := newFixture(t)
	honest, banned := f.player("honest_cat"), f.player("banned_cat")
	clean, cheated := testutil.ULID(t), testutil.ULID(t)

	f.rawFlightEvent(honest, clean, "vehicle.impact", sharedCareer, map[string]any{"speed_ms": 214, "body": "duna"})
	f.rawFlightEvent(honest, cheated, "vehicle.impact", sharedCareer, map[string]any{"speed_ms": 999, "body": "kerbin"})
	f.flagFlight(honest, cheated, stats.FlagTeleport)
	f.rawEvent(banned, "vehicle.staging", sharedCareer, map[string]any{"stage_index": 7})
	f.ban("banned_cat")

	rec := f.get("/v1/events")
	page := decode[readapi.EventsResponse](t, rec)
	if len(page.Events) != 1 || page.Events[0].Handle != "honest_cat" {
		t.Fatalf("events = %+v, want only the honest flight's row", page.Events)
	}
	body := rec.Body.String()
	for _, leak := range []string{"banned_cat", `"stage_index":7`, "999", ids.String(cheated)} {
		if strings.Contains(body, leak) {
			t.Errorf("the global view published %q", leak)
		}
	}
}
