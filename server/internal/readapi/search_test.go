package readapi_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

func TestSearchRanksPrefixMatchesFirst(t *testing.T) {
	f := newFixture(t)
	for _, handle := range []string{"Whiskers_Prime", "whiskers_two", "not_whiskers", "unrelated"} {
		f.player(handle)
	}

	got := decode[readapi.SearchResponse](t, f.get("/v1/players?q=whisk"))
	want := []string{"Whiskers_Prime", "whiskers_two", "not_whiskers"}
	if !slices.Equal(got.Handles, want) {
		t.Errorf("handles = %v, want %v (prefix matches first, then substring, each lexicographic)", got.Handles, want)
	}
	if got.Query != "whisk" || got.Limit != readapi.DefaultSearchLimit {
		t.Errorf("response echoes %q/%d", got.Query, got.Limit)
	}
	// Display casing is preserved, and the match is case-insensitive both ways.
	if upper := decode[readapi.SearchResponse](t, f.get("/v1/players?q=WHISKERS_PRIME")); !slices.Equal(upper.Handles, []string{"Whiskers_Prime"}) {
		t.Errorf("case-insensitive search = %v", upper.Handles)
	}
}

func TestSearchReturnsHandlesAndNothingElse(t *testing.T) {
	// The cheapest endpoint in the API must stay the cheapest: a search result
	// is a list of links, and everything behind a link is one request away.
	f := newFixture(t)
	p := f.player("whiskers")
	f.stat(p, stats.StatStagings, 42, 1)

	body := f.get("/v1/players?q=whis").Body.String()
	for _, leaked := range []string{"42", "stagings", "since", "rank"} {
		if strings.Contains(body, leaked) {
			t.Errorf("search response carries %q: %s", leaked, body)
		}
	}
}

func TestSearchHidesBannedPlayers(t *testing.T) {
	// Not a special case: the directory a search scans is the same one every
	// board page resolves handles through, and a banned player is absent from it.
	f := newFixture(t)
	f.player("honest_cat")
	f.player("banned_cat")
	f.ban("banned_cat")

	got := decode[readapi.SearchResponse](t, f.get("/v1/players?q=cat"))
	if !slices.Equal(got.Handles, []string{"honest_cat"}) {
		t.Errorf("handles = %v, want only the visible one", got.Handles)
	}
}

func TestSearchBoundsItsOwnCost(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.player(string(rune('a'+i)) + "_common_name")
	}

	// A query shorter than the minimum never reaches the scan. It is a 400
	// rather than an empty 200 so the answer is honest, and — like every §4.8
	// response — it is cacheable, so a client hammering it is answered by the CDN.
	for _, q := range []string{"", "a"} {
		rec := f.get("/v1/players?q=" + q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("q=%q returned %d, want 400", q, rec.Code)
		}
	}
	long := make([]byte, readapi.MaxQueryLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if rec := f.get("/v1/players?q=" + string(long)); rec.Code != http.StatusBadRequest {
		t.Errorf("an over-long query returned %d, want 400", rec.Code)
	}

	// The limit clamps rather than 400s, for the same cache reason as the board
	// limit: one answer per (q, limit), not a split between a 400 and a 200.
	for _, tc := range []struct {
		query string
		want  int
	}{{"&limit=999", readapi.MaxSearchLimit}, {"&limit=0", 1}, {"", readapi.DefaultSearchLimit}} {
		got := decode[readapi.SearchResponse](t, f.get("/v1/players?q=common"+tc.query))
		if got.Limit != tc.want {
			t.Errorf("limit%q echoed %d, want %d", tc.query, got.Limit, tc.want)
		}
	}

	// Truncation is reported, not hidden, and the truncated answer is the first
	// page of a deterministic order — the same URL must mean the same thing
	// twice or the CDN in front of this caches an arbitrary subset.
	got := decode[readapi.SearchResponse](t, f.get("/v1/players?q=common&limit=2"))
	if len(got.Handles) != 2 || !got.Truncated {
		t.Fatalf("limit=2 gave %v truncated=%v", got.Handles, got.Truncated)
	}
	for range 3 {
		again := decode[readapi.SearchResponse](t, f.get("/v1/players?q=common&limit=2"))
		if !slices.Equal(again.Handles, got.Handles) {
			t.Fatalf("the same query gave %v then %v", got.Handles, again.Handles)
		}
	}
}

func TestSearchWithNoMatchesIsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	f.player("whiskers")
	rec := f.get("/v1/players?q=nobody")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// `[]`, never `null`: a client should not have to defend against both.
	if body := rec.Body.String(); !strings.Contains(body, `"handles":[]`) {
		t.Errorf("body = %s", body)
	}
}
