package readapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The origin the SPA is pretended to be served from. Not a real deployment URL —
// the point of the allow-list is that it is configuration.
const spaOrigin = "https://example.invalid"

// corsMux is a read API that allows exactly [spaOrigin].
func corsMux(t *testing.T) *http.ServeMux {
	t.Helper()
	events := testutil.MemEvents(t)
	dir := directory.New(events)
	if err := dir.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv, err := readapi.New(readapi.Deps{
		Projections:    live{testutil.MemProjections(t)},
		Events:         events,
		Directory:      dir,
		AllowedOrigins: []string{spaOrigin},
		Log:            testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// readPaths is every route the read API mounts — the exact set that may be read
// cross-origin, and the list a future route has to be added to deliberately.
var readPaths = []string{
	"/v1/leaderboards",
	"/v1/leaderboards/kitten_tumbles",
	"/v1/players?q=whis",
	"/v1/players/whiskers",
	"/v1/players/whiskers/events",
	"/v1/compare?handles=whiskers,mittens",
	"/v1/feed",
}

func TestCORSAllowsTheConfiguredOriginOnEveryReadRoute(t *testing.T) {
	mux := corsMux(t)
	for _, path := range readPaths {
		rec := do(t, mux, http.MethodGet, path, spaOrigin)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != spaOrigin {
			t.Errorf("GET %s Access-Control-Allow-Origin = %q, want %q", path, got, spaOrigin)
		}
		// The §4.8 responses are cached by a CDN (s-maxage=30). Without this a
		// shared cache could serve the allow-listed answer to everyone.
		if got := rec.Header().Get("Vary"); got != "Origin" {
			t.Errorf("GET %s Vary = %q, want Origin", path, got)
		}
		// Anonymous public facts: there is nothing a cookie could add, so the
		// browser must never be told to send one.
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("GET %s sent Access-Control-Allow-Credentials = %q; the read API takes no credentials", path, got)
		}
	}
}

func TestCORSRefusesAnUnlistedOrigin(t *testing.T) {
	for _, origin := range []string{
		"https://evil.invalid",
		// Prefix and suffix games: exact match, not "starts with".
		spaOrigin + ".evil.invalid",
		"https://evil.invalid?" + spaOrigin,
		// A trailing slash is not an origin, and must not be treated as one.
		spaOrigin + "/",
	} {
		mux := corsMux(t)
		rec := do(t, mux, http.MethodGet, "/v1/leaderboards", origin)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q was allowed (Access-Control-Allow-Origin = %q)", origin, got)
		}
		// It is still a perfectly good request — the browser is what enforces
		// the block, and a curl or a CDN prefetch must not start 403ing.
		if rec.Code != http.StatusOK {
			t.Errorf("origin %q made the request fail with %d; CORS gates the browser, not the server", origin, rec.Code)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	mux := corsMux(t)

	req := httptest.NewRequest(http.MethodOptions, "/v1/leaderboards/kitten_tumbles", nil)
	req.Header.Set("Origin", spaOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "x-trace-id")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":  spaOrigin,
		"Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
		"Access-Control-Allow-Headers": "x-trace-id",
		"Access-Control-Max-Age":       "600",
		"Cache-Control":                "no-store",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("preflight %s = %q, want %q", header, got, want)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("preflight sent Access-Control-Allow-Credentials = %q", got)
	}

	// An unlisted origin gets a bare 204: no headers, so the browser blocks it.
	req = httptest.NewRequest(http.MethodOptions, "/v1/leaderboards", nil)
	req.Header.Set("Origin", "https://evil.invalid")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("unlisted preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted preflight allowed the origin: %q", got)
	}
}

// TestCORSIsOffByDefaultForAServerWithNoAllowList pins the posture of a
// deployment that has no second frontend: no allow-list, no headers, ever.
func TestCORSIsOffByDefaultForAServerWithNoAllowList(t *testing.T) {
	f := newFixture(t) // built with no AllowedOrigins
	rec := do(t, f.mux, http.MethodGet, "/v1/leaderboards", spaOrigin)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a read API with no allow-list emitted Access-Control-Allow-Origin = %q", got)
	}
}
