package readapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The privacy boundary, in the same spirit as the CORS one in cors_test.go: a
// list of every public route, and a list of exactly what a regression on any of
// them would leak.
//
// Constitution §1 says the handle is the only public identity. The three values
// below are the ones that would break that quietly rather than loudly:
//
//	user_key   HMAC-SHA256(pepper, "<idp>:<subject>") — the account key. It is
//	           in no response struct in this package and never has been.
//	install    session.started's per-install ULID. One value per KSA
//	           installation, so per *person* — publishing it links one person's
//	           two accounts to each other.
//	career/kid both derived from the install id (docs/events.md), so both do the
//	           same linking in weaker forms.
//
// The scenario below is the one that matters: two accounts, two handles, one
// machine — the same install, the same save, the same kitten. If any of the
// three reached a response verbatim, `alpha` and `beta` would be provably the
// same person to anybody with curl.

// sharedInstall, sharedCareer and sharedKid are what two accounts on one
// machine have in common. Their exact values are irrelevant; that they are
// *identical between the two players* is the whole test.
var (
	sharedInstall = ids.String(ids.MustNew())
	sharedCareer  = "b7k2q9x4m0nrt3vz"
	sharedKid     = "k1tt3n0000000001"
)

// publicGETs is every route the read API serves, for one handle. A new
// endpoint has to be added here deliberately, which is the point: the question
// "does this leak?" is asked of every route, not of the routes somebody
// remembered.
func publicGETs(handle string) []string {
	return []string{
		"/v1/leaderboards",
		"/v1/leaderboards/" + stats.StatFastestToOrbit,
		"/v1/leaderboards/" + stats.StatKittenTumbles,
		"/v1/players?q=" + handle[:3],
		"/v1/players/" + handle,
		"/v1/players/" + handle + "/events",
		"/v1/players/" + handle + "/events?type=session.started",
		"/v1/compare?handles=alpha_pilot,beta_pilot",
		"/v1/feed",
	}
}

// twoAccountsOneMachine seeds the scenario: two players whose events carry the
// same install id, the same career and the same kitten id.
func twoAccountsOneMachine(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	for _, handle := range []string{"alpha_pilot", "beta_pilot"} {
		id := f.player(handle)
		f.rawEvent(id, "session.started", sharedCareer, map[string]any{
			"mod_ver": "0.1.0", "game_build": "2026.8.5.5168", "install": sharedInstall,
		})
		f.rawEvent(id, "kitten.tumble", sharedCareer, map[string]any{
			"kid": sharedKid, "name": "Bramble", "speed_ms": 8.9, "body": "mun",
		})
		f.rawEvent(id, "roster.snapshot", sharedCareer, map[string]any{
			"kittens": []any{map[string]any{"kid": sharedKid, "name": "Bramble", "travelled_m": 620000}},
		})
		// A career-time board row: its `context` is where the fold copies the
		// §4.1 career key, which is how it reaches a public surface today.
		f.statContext(id, stats.StatFastestToOrbit, 312500,
			fmt.Sprintf(`{"career":%q,"body":"earth"}`, sharedCareer))
		f.stat(id, stats.StatKittenTumbles, 4, 9)
	}
	return f
}

func TestNothingPublicCarriesAnInstallDerivedIdentifier(t *testing.T) {
	f := twoAccountsOneMachine(t)

	// Everything a regression would leak, and the name of the thing it would be.
	leaks := map[string]string{
		sharedInstall: "the install id — one value per machine, so publishing it links a person's accounts",
		sharedCareer:  "the raw career key, which is SHA-256(install_id + save name)",
		sharedKid:     "the raw kitten id, which is SHA-256(install_id + kitten name)",
		`"install"`:   "the install field itself; it must be dropped, not emptied",
		`"user_key"`:  "the account key — it is in no response struct in this package",
		`"wall_t"`:    "the client's own clock, whose offset from recv is a per-machine constant",
	}

	for _, handle := range []string{"alpha_pilot", "beta_pilot"} {
		for _, path := range publicGETs(handle) {
			body := f.get(path).Body.String()
			for needle, why := range leaks {
				if strings.Contains(body, needle) {
					t.Errorf("GET %s published %s — %s\n%s", path, needle, why, body)
				}
			}
		}
	}
}

// The point of relabelling rather than dropping: the labels still group a
// player's own rows, and they no longer link two players.
func TestRelabellingIsPerPlayerAndStable(t *testing.T) {
	f := twoAccountsOneMachine(t)

	careerOf := func(handle string) string {
		f.t.Helper()
		profile := decode[readapi.PlayerResponse](t, f.get("/v1/players/"+handle))
		for _, row := range profile.Stats {
			if row.Stat != stats.StatFastestToOrbit {
				continue
			}
			var ctx struct{ Career string }
			if err := json.Unmarshal(row.Context, &ctx); err != nil {
				t.Fatalf("%s: context is not JSON: %s", handle, row.Context)
			}
			return ctx.Career
		}
		t.Fatalf("%s has no career-time row", handle)
		return ""
	}
	kidOf := func(handle string) string {
		f.t.Helper()
		page := decode[readapi.EventsResponse](t, f.get("/v1/players/"+handle+"/events?type=kitten.tumble"))
		if len(page.Events) != 1 {
			t.Fatalf("%s: %d tumble events, want 1", handle, len(page.Events))
		}
		var p struct{ Kid string }
		if err := json.Unmarshal(page.Events[0].Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p.Kid
	}

	alphaCareer, betaCareer := careerOf("alpha_pilot"), careerOf("beta_pilot")
	alphaKid, betaKid := kidOf("alpha_pilot"), kidOf("beta_pilot")

	for name, pair := range map[string][2]string{
		"career": {alphaCareer, betaCareer},
		"kid":    {alphaKid, betaKid},
	} {
		if pair[0] == "" || pair[1] == "" {
			t.Fatalf("%s label is empty; the value was dropped, not relabelled", name)
		}
		if pair[0] == pair[1] {
			t.Errorf("both players published %s %q — the label is not per-player, so two accounts on one machine are linkable", name, pair[0])
		}
		if len(pair[0]) != readapi.LabelLen {
			t.Errorf("%s label %q is %d chars, want %d (the shape of the value it replaces)", name, pair[0], len(pair[0]), readapi.LabelLen)
		}
	}

	// Stable within one player and across endpoints, or it groups nothing: the
	// event log's career label must be the profile row's career label.
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/alpha_pilot/events"))
	labelled := 0
	for _, ev := range page.Events {
		if ev.Career == "" {
			// An event that carried no career gets no label rather than a made-up
			// one, exactly as §4.1 stores it (the column is nullable).
			continue
		}
		labelled++
		if ev.Career != alphaCareer {
			t.Errorf("event %s career label = %q, profile says %q", ev.Type, ev.Career, alphaCareer)
		}
	}
	if labelled == 0 {
		t.Error("no event carried a career label; the cross-endpoint check asserted nothing")
	}
	// And stable across calls, so a client may cache it or put it in a URL.
	if again := careerOf("alpha_pilot"); again != alphaCareer {
		t.Errorf("career label changed between requests: %q then %q", alphaCareer, again)
	}
}

// The roster snapshot nests its kitten ids inside an array of objects. The rule
// is by field name at any depth precisely so that shape needs no special case —
// and so a future payload that nests one deeper is covered before anybody
// notices.
func TestRedactionReachesNestedPayloadKeys(t *testing.T) {
	f := twoAccountsOneMachine(t)
	page := decode[readapi.EventsResponse](t, f.get("/v1/players/alpha_pilot/events?type=roster.snapshot"))
	if len(page.Events) != 1 {
		t.Fatalf("%d roster events, want 1", len(page.Events))
	}
	var p struct {
		Kittens []struct {
			Kid        string  `json:"kid"`
			Name       string  `json:"name"`
			TravelledM float64 `json:"travelled_m"`
		} `json:"kittens"`
	}
	if err := json.Unmarshal(page.Events[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Kittens) != 1 {
		t.Fatalf("payload = %s", page.Events[0].Payload)
	}
	if p.Kittens[0].Kid == sharedKid {
		t.Error("a nested kid was published raw")
	}
	// And the gameplay half is untouched: a raw-event view that redacted the
	// data is not worth building.
	if p.Kittens[0].Name != "Bramble" || p.Kittens[0].TravelledM != 620000 {
		t.Errorf("gameplay data was mangled: %+v", p.Kittens[0])
	}
}

// --- fixture helpers used by the tests above ---------------------------------

// rawEvent inserts one real §4.1 envelope for a player, with a payload written
// as it would arrive on the wire. It carries no flight, like a session or roster
// event.
func (f *fixture) rawEvent(playerID int64, typ, career string, payload map[string]any) int64 {
	f.t.Helper()
	return f.rawFlightEvent(playerID, ids.Zero, typ, career, payload)
}

// rawFlightEvent is [fixture.rawEvent] for an event that belongs to a flight.
func (f *fixture) rawFlightEvent(playerID int64, flight ids.ID, typ, career string, payload map[string]any) int64 {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	f.eventN++
	var id ids.ID
	// 0xEE namespaces these apart from [fixture.event]'s ids, so the two
	// helpers cannot mint the same event id and silently dedup.
	id[0], id[1], id[14], id[15] = byte(playerID), 0xEE, byte(f.eventN>>8), byte(f.eventN)
	if _, _, err := f.events.InsertEvents(f.t.Context(), nil, playerID, []store.Event{{
		ID: id, SessionID: id, FlightID: flight, Career: career, Type: typ, Ver: 1,
		WallTime: 1_770_000_000_000, Payload: raw,
	}}); err != nil {
		f.t.Fatal(err)
	}
	var seq int64
	if err := f.events.Reader().QueryRowContext(f.t.Context(),
		`SELECT seq FROM event WHERE player_id = ? ORDER BY seq DESC LIMIT 1`, playerID).Scan(&seq); err != nil {
		f.t.Fatal(err)
	}
	return seq
}

// statContext is [fixture.stat] with the fold's context blob spelled out.
func (f *fixture) statContext(playerID int64, stat string, value float64, context string) {
	f.t.Helper()
	seq := f.rawEvent(playerID, "vehicle.orbit", sharedCareer, map[string]any{"phase": "achieved", "body": "earth"})
	if _, err := f.proj.Writer().ExecContext(f.t.Context(),
		`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, ?, ?)`,
		playerID, stat, value, context, seq); err != nil {
		f.t.Fatal(err)
	}
}
