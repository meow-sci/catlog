package readapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

func seedBadgeCareer(t *testing.T, f *fixture, playerID, ordinal int64, career, system string) {
	t.Helper()
	f.projWrite(`INSERT INTO career (player_id, career, first_seq, last_seq, ordinal, system)
		VALUES (?, ?, 1, 1, ?, ?)`, playerID, career, ordinal, system)
}

func seedBadgeAward(t *testing.T, f *fixture, playerID int64, career, badge, system, firstCareer string,
	seq, at int64, simT *float64, context string,
) {
	t.Helper()
	var sim any
	if simT != nil {
		sim = *simT
	}
	var cx any
	if context != "" {
		cx = context
	}
	f.projWrite(`INSERT INTO badge_award
		(player_id, career, badge, system, first_career, earned_seq, earned_at, earned_sim_t, context)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, playerID, career, badge, system, firstCareer, seq, at, sim, cx)
}

func seedCompleteBadgeSystem(t *testing.T, f *fixture, hash, slug string, complete bool) {
	t.Helper()
	flag := 0
	if complete {
		flag = 1
	}
	seedSystem(t, f, hash, hash, strings.ToUpper(slug), slug, 2, flag, 1)
	seedRoot(t, f, hash, "alpha-root", "Alpha", 0, 1)
	seedOrbitingBody(t, f, hash)
}

func TestBadgeCatalogueFixedAndGatedFamiliesInRegistryOrder(t *testing.T) {
	f := newFixture(t, func(d *readapi.Deps) { d.MinBoardPlayers = 2 })
	p1, p2 := f.player("one"), f.player("two")
	seedBadgeAward(t, f, p1, "", "orbited_luna", "", "a", 1, 101, nil, "")
	seedBadgeAward(t, f, p2, "", "orbited_luna", "", "b", 2, 102, nil, "")
	seedBadgeAward(t, f, p1, "", "reached_duna", "", "a", 3, 103, nil, "")

	rec := f.get("/v1/badges")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[readapi.BadgesResponse](t, rec)
	if got.MinPlayers != 2 || len(got.Badges) != len(stats.FixedBadges())+1 {
		t.Fatalf("catalogue size/gate = %d/%d: %+v", got.MinPlayers, len(got.Badges), got.Badges)
	}
	keys := make([]string, 0, len(got.Badges))
	for _, badge := range got.Badges {
		keys = append(keys, badge.Badge)
	}
	want := stats.BadgeCatalog(map[string]int64{"orbited_luna": 2, "reached_duna": 1}, 2)
	for i, badge := range want {
		if keys[i] != badge.Key {
			t.Fatalf("catalogue[%d] = %q, want %q", i, keys[i], badge.Key)
		}
	}
	for _, badge := range got.Badges {
		if badge.Badge == "orbited_luna" && badge.Holders != 2 {
			t.Errorf("orbited_luna holders = %d, want 2", badge.Holders)
		}
	}
}

func TestBadgeDetailVisibilitySystemFilterAndRepresentativeSave(t *testing.T) {
	f := newFixture(t)
	seedCompleteBadgeSystem(t, f, "hash-sol", "sol", true)
	seedCompleteBadgeSystem(t, f, "hash-alt", "alt", true)
	hidden, first, later := f.player("hidden"), f.player("first"), f.player("later")
	seedBadgeCareer(t, f, hidden, 1, "hidden-sol", "hash-sol")
	seedBadgeCareer(t, f, hidden, 2, "hidden-sol-two", "hash-sol")
	seedBadgeCareer(t, f, first, 1, "first-alt", "hash-alt")
	seedBadgeCareer(t, f, first, 2, "first-sol-late", "hash-sol")
	seedBadgeCareer(t, f, first, 3, "first-sol-early", "hash-sol")
	seedBadgeCareer(t, f, later, 1, "later-sol", "hash-sol")
	zero := 0.0
	for _, a := range []struct {
		player                int64
		career, system, first string
		seq, at               int64
	}{
		{hidden, "", "hash-sol", "hidden-sol", 1, 101},
		{first, "", "hash-alt", "first-alt", 2, 102},
		{later, "", "hash-sol", "later-sol", 5, 105},
		{hidden, "hidden-sol", "hash-sol", "", 1, 101},
		{hidden, "hidden-sol-two", "hash-sol", "", 6, 106},
		{first, "first-sol-late", "hash-sol", "", 4, 104},
		{first, "first-sol-early", "hash-sol", "", 3, 103},
		{later, "later-sol", "hash-sol", "", 5, 105},
	} {
		seedBadgeAward(t, f, a.player, a.career, stats.BadgeFirstOrbit, a.system, a.first,
			a.seq, a.at, &zero, `{"career":"`+a.career+`","install":"drop"}`)
	}
	f.ban("hidden")

	for _, key := range []string{"sol", "hash-sol"} {
		rec := f.get("/v1/badges/first_orbit?system=" + key)
		if rec.Code != http.StatusOK {
			t.Fatalf("system %s status = %d: %s", key, rec.Code, rec.Body)
		}
		got := decode[readapi.BadgeResponse](t, rec)
		if got.Holders != 3 || len(got.Rows) != 2 || got.Rows[0].Rank != 1 || got.Rows[1].Rank != 2 {
			t.Fatalf("holders/visible ranks = %d %+v", got.Holders, got.Rows)
		}
		if got.Rows[0].Handle != "first" || got.Rows[0].Save != 3 || got.Rows[0].Earned != 103 {
			t.Errorf("representative save = %+v", got.Rows[0])
		}
		if got.Rows[0].System == nil || got.Rows[0].System.Hash != "hash-sol" || got.Rows[0].SimT == nil || *got.Rows[0].SimT != 0 {
			t.Errorf("system/sim_t = %+v", got.Rows[0])
		}
		if strings.Contains(string(got.Rows[0].Context), "first-sol-early") || strings.Contains(string(got.Rows[0].Context), "install") {
			t.Errorf("context was not redacted: %s", got.Rows[0].Context)
		}
	}

	global := decode[readapi.BadgeResponse](t, f.get("/v1/badges/first_orbit"))
	if global.Holders != 3 || len(global.Rows) != 2 || global.Rows[0].Handle != "first" || global.Rows[0].Save != 1 || global.Rows[0].System.Hash != "hash-alt" {
		t.Fatalf("lifetime holders = %+v", global)
	}
}

func TestBadgeDetailFamilyKnown404PagingAndSystemErrors(t *testing.T) {
	f := newFixture(t)
	p := f.player("pilot")
	seedBadgeAward(t, f, p, "", "reached_luna", "", "save", 1, 101, nil, "")
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/v1/badges/reached_luna?limit=999&offset=-1", 200},
		{"/v1/badges/reached_duna", 404},
		{"/v1/badges/not_a_badge", 404},
		{"/v1/badges/first_flight?system=missing", 404},
		{"/v1/badges/first_flight?limit=nope", 400},
	} {
		rec := f.get(tc.path)
		if rec.Code != tc.code {
			t.Errorf("GET %s = %d, want %d: %s", tc.path, rec.Code, tc.code, rec.Body)
		}
	}
	got := decode[readapi.BadgeResponse](t, f.get("/v1/badges/reached_luna?limit=999&offset=-1"))
	if got.Limit != readapi.MaxLimit || got.Offset != 0 {
		t.Errorf("paging = %d/%d", got.Limit, got.Offset)
	}
	empty := decode[readapi.BadgeResponse](t, f.get("/v1/badges/first_flight"))
	if empty.Holders != 0 || empty.Rows == nil || len(empty.Rows) != 0 {
		t.Errorf("empty fixed badge = %+v", empty)
	}
}

func TestPlayerBadgeChecklistsAndSaveCatalogueCompleteness(t *testing.T) {
	f := newFixture(t)
	p := f.player("pilot")
	seedCompleteBadgeSystem(t, f, "hash-complete", "complete", true)
	seedCompleteBadgeSystem(t, f, "hash-incomplete", "incomplete", false)
	seedBadgeCareer(t, f, p, 1, "save-complete", "hash-complete")
	seedBadgeCareer(t, f, p, 2, "save-incomplete", "hash-incomplete")
	seedBadgeAward(t, f, p, "", stats.BadgeFirstFlight, "hash-complete", "save-complete", 1, 101, nil, "")
	seedBadgeAward(t, f, p, "save-complete", stats.BadgeFirstFlight, "hash-complete", "", 1, 101, nil, "")
	// Acquisition order is deliberately opposite catalogue order: response
	// order is the stable registry, not timing.
	seedBadgeAward(t, f, p, "save-complete", "reached_alpha-root", "hash-complete", "", 0, 100, nil, `{"body":"alpha-root"}`)
	other := f.player("other")
	seedBadgeAward(t, f, other, "", stats.BadgeFirstOrbit, "", "other-save", 3, 103, nil, "")

	lifetime := decode[readapi.PlayerBadgesResponse](t, f.get("/v1/players/pilot/badges"))
	if len(lifetime.Earned) != 1 || lifetime.Earned[0].Save != 1 || len(lifetime.Unearned) != len(stats.FixedBadges())-1 {
		t.Fatalf("lifetime checklist = %+v", lifetime)
	}
	for _, b := range lifetime.Unearned {
		if b.Badge == stats.BadgeFirstOrbit && b.Holders != 1 {
			t.Errorf("unearned summary holders = %d, want 1", b.Holders)
		}
		if strings.HasPrefix(b.Badge, "reached_") || strings.HasPrefix(b.Badge, "orbited_") || strings.HasPrefix(b.Badge, "landed_on_") {
			t.Fatalf("lifetime family checklist leaked: %s", b.Badge)
		}
	}
	for i, meta := range stats.FixedBadges()[1:] {
		if lifetime.Unearned[i].Badge != meta.Key {
			t.Fatalf("lifetime unearned[%d] = %q, want %q", i, lifetime.Unearned[i].Badge, meta.Key)
		}
	}

	save := decode[readapi.PlayerBadgesResponse](t, f.get("/v1/players/pilot/saves/1/badges"))
	if len(save.Earned) != 2 || save.Earned[0].Save != 1 || save.Earned[0].System == nil || save.Earned[0].System.Hash != "hash-complete" {
		t.Fatalf("save checklist earned = %+v", save)
	}
	if save.Earned[0].Badge != stats.BadgeFirstFlight || save.Earned[1].Badge != "reached_alpha-root" {
		t.Fatalf("save earned order = %q, %q", save.Earned[0].Badge, save.Earned[1].Badge)
	}
	wantFamily := map[string]bool{
		"orbited_alpha-root": true, "landed_on_alpha-root": true,
		"reached_beta-child": true, "orbited_beta-child": true, "landed_on_beta-child": true,
	}
	for _, b := range save.Unearned {
		delete(wantFamily, b.Badge)
	}
	if len(wantFamily) != 0 {
		t.Errorf("missing family checklist keys: %v", wantFamily)
	}
	incomplete := decode[readapi.PlayerBadgesResponse](t, f.get("/v1/players/pilot/saves/2/badges"))
	if len(incomplete.Unearned) != len(stats.FixedBadges()) {
		t.Fatalf("incomplete system added family checklist: %d", len(incomplete.Unearned))
	}
	for _, b := range incomplete.Unearned {
		if strings.HasPrefix(b.Badge, "reached_") || strings.HasPrefix(b.Badge, "orbited_") || strings.HasPrefix(b.Badge, "landed_on_") {
			t.Fatalf("incomplete family key = %s", b.Badge)
		}
	}
}

func TestPlayerBadge404sAndProjectionFailures(t *testing.T) {
	f := newFixture(t)
	f.player("pilot")
	f.player("banned")
	f.ban("banned")
	f.player("retired")
	if err := f.events.RetireHandle(t.Context(), nil, "retired", "purged", 123); err != nil {
		t.Fatal(err)
	}
	if err := f.dir.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/players/missing/badges", "/v1/players/missing/saves/1/badges",
		"/v1/players/banned/badges", "/v1/players/banned/saves/1/badges",
		"/v1/players/retired/badges", "/v1/players/retired/saves/1/badges",
		"/v1/players/pilot/saves/no/badges", "/v1/players/pilot/saves/1/badges",
	} {
		rec := f.get(path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(path, "/banned/") || strings.Contains(path, "/retired/") || strings.Contains(path, "/missing/") {
			if detail := errorDetail(t, rec.Body.Bytes()); detail != "no such player" {
				t.Errorf("GET %s detail = %q", path, detail)
			}
		}
	}
	fail := newFixture(t, func(d *readapi.Deps) { d.Projections = failedProjections{} })
	fail.player("pilot")
	for _, path := range []string{"/v1/badges", "/v1/badges/first_flight", "/v1/players/pilot/badges", "/v1/players/pilot/saves/1/badges"} {
		if rec := fail.get(path); rec.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestBadgeResponsesNeverMarshalStoreCareerColumns(t *testing.T) {
	for _, typ := range []any{readapi.BadgeHolderRow{}, readapi.BadgeAward{}, readapi.PlayerBadgesResponse{}} {
		raw, err := json.Marshal(typ)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, ok := decoded["career"]; ok {
			t.Fatalf("public badge carrier has career: %T %s", typ, raw)
		}
	}
}
