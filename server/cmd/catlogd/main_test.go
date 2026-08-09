package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// TestHealthz pins the §4.4 health contract: 200, JSON content type, exact body.
func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got, want := res.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Body.String(), `{"ok":true}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestStartupCreatesEverythingAndShutsDownCleanly boots the real wiring against
// a temp data directory: keys are created, both databases are opened and
// migrated, /healthz answers, and a cancelled context tears everything down
// with the WALs checkpointed and the file locks released.
func TestStartupCreatesEverythingAndShutsDownCleanly(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.Server.Listen = "127.0.0.1:0" // let the kernel pick a free port
	cfg.Data.CheckpointIntervalS = 1

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, cfg, testutil.DiscardLogger(), func(pub, _ net.Addr) { addrCh <- pub }) }()

	var addr net.Addr
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("catlogd exited during startup: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("catlogd never became ready")
	}

	// Everything §3 says lives under the data directory must exist.
	for _, rel := range []string{
		"events.db",
		"projections.db",
		filepath.Join("keys", keys.SigningFile),
		filepath.Join("keys", keys.SessionFile),
		filepath.Join("keys", keys.PepperFile),
	} {
		if _, err := os.Stat(filepath.Join(cfg.Data.Dir, rel)); err != nil {
			t.Errorf("startup did not create %s: %v", rel, err)
		}
	}

	// The health contract, over a real socket this time.
	res, err := http.Get("http://" + addr.String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read /healthz body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	var health struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &health); err != nil || !health.OK {
		t.Errorf("body = %q (err %v), want {\"ok\":true}", body, err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("shutdown returned an error: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("catlogd did not shut down")
	}

	// Shutdown checkpointed both WALs, so neither -wal file has content left.
	for _, name := range []string{"events.db", "projections.db"} {
		wal := filepath.Join(cfg.Data.Dir, name+"-wal")
		if fi, err := os.Stat(wal); err == nil && fi.Size() != 0 {
			t.Errorf("%s is %d B after shutdown, want 0 (shutdown must checkpoint)", wal, fi.Size())
		}
	}

	// The file locks are gone: a fresh catlogd can bind the same databases,
	// which is exactly what a redeploy does (§5.4).
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	addrCh2 := make(chan net.Addr, 1)
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- run(ctx2, cfg, testutil.DiscardLogger(), func(pub, _ net.Addr) { addrCh2 <- pub }) }()
	select {
	case <-addrCh2:
	case err := <-errCh2:
		t.Fatalf("restart against the same data directory failed: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("restart never became ready")
	}
	cancel2()
	if err := <-errCh2; err != nil {
		t.Errorf("second shutdown returned an error: %v", err)
	}
}

// TestCORSCoversTheReadAPIAndNothingElse is the test that has to exist.
//
// The §4.8 read endpoints answer cross-origin requests, because a browser
// reader on another origin is a legitimate consumer of them. Every other route
// on the public listener is either cookie-authenticated (`/api/*`, `/auth/*`,
// `/dashboard`) or signature-authenticated with a same-origin assumption
// (`/v1/ingest`), and an `Access-Control-Allow-Origin` on any of them would let
// a page on the allow-listed origin read a signed-in user's account out of the
// browser.
//
// Only the whole wiring can prove this: the boundary is *which mux entries the
// middleware is attached to*, and that is decided in run(), not in any one
// package. Hence a test here rather than in internal/readapi.
func TestCORSCoversTheReadAPIAndNothingElse(t *testing.T) {
	const origin = "https://reader.example.invalid"

	cfg := testutil.Config(t)
	cfg.CORS.AllowedOrigins = []string{origin}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, cfg, testutil.DiscardLogger(), func(p, _ net.Addr) { addrCh <- p }) }()

	var addr net.Addr
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("catlogd exited during startup: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("catlogd never became ready")
	}

	// Redirects are not followed: /auth/{idp}/start answers 302, and following
	// it would report the headers of the wrong response.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// The body is closed, never drained: `/v1/feed/sse` is an endless stream and
	// reading it to EOF would hang the test. Only the response headers matter
	// here anyway.
	acao := func(method, path string) string {
		t.Helper()
		reqCtx, done := context.WithTimeout(t.Context(), 30*time.Second)
		defer done()
		req, err := http.NewRequestWithContext(reqCtx, method, "http://"+addr.String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("%s %s sent Access-Control-Allow-Credentials = %q; nothing here may take credentials", method, path, got)
		}
		return res.Header.Get("Access-Control-Allow-Origin")
	}

	// The public read API (§4.8) — the only cross-origin surface there is.
	for _, path := range []string{
		"/v1/leaderboards",
		"/v1/leaderboards/kitten_tumbles",
		"/v1/players/nobody",
		"/v1/feed",
	} {
		if got := acao(http.MethodGet, path); got != origin {
			t.Errorf("GET %s Access-Control-Allow-Origin = %q, want %q", path, got, origin)
		}
	}
	// The preflight for the same routes.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, "http://"+addr.String()+"/v1/leaderboards", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /v1/leaderboards: %v", err)
	}
	res.Body.Close()
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, origin)
	}

	// Everything else. A regression here is a real vulnerability, not a config
	// nit, so each case names what it would leak.
	for _, tc := range []struct{ method, path, leaks string }{
		{http.MethodGet, "/api/me", "the signed-in account and its handles"},
		{http.MethodGet, "/api/handles", "the signed-in account's handles"},
		{http.MethodPost, "/api/handles", "handle claims made as the signed-in user"},
		{http.MethodPost, "/api/logout", "session termination as the signed-in user"},
		{http.MethodPost, "/api/me/delete", "account deletion as the signed-in user"},
		{http.MethodGet, "/auth/discord/start", "the OAuth state cookie"},
		{http.MethodGet, "/auth/discord/callback", "an authorization code exchange"},
		{http.MethodGet, "/dashboard", "the dashboard page for the signed-in user"},
		{http.MethodPost, "/v1/ingest", "the §4.5.3 ingest surface"},
		{http.MethodGet, "/healthz", "nothing, but it is not a §4.8 read route"},
		{http.MethodGet, "/", "the server-rendered site"},
	} {
		if got := acao(tc.method, tc.path); got != "" {
			t.Errorf("%s %s carries Access-Control-Allow-Origin = %q — cross-origin JS could read %s",
				tc.method, tc.path, got, tc.leaks)
		}
	}
	// The SSE feed the server-rendered site depends on is untouched: still
	// same-origin, still where it was.
	if got := acao(http.MethodGet, "/v1/feed/sse"); got != "" {
		t.Errorf("/v1/feed/sse carries Access-Control-Allow-Origin = %q; the datastar route must stay same-origin", got)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("shutdown returned an error: %v", err)
	}
}

// TestRunRejectsUnusableConfig checks that a bad listen address fails loudly
// rather than leaving a half-open pair of databases behind.
func TestRunRejectsUnusableConfig(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.Server.Listen = "256.256.256.256:99999"

	err := run(t.Context(), cfg, testutil.DiscardLogger(), nil)
	if err == nil {
		t.Fatal("run succeeded with an unusable listen address")
	}

	// The databases it opened before failing must have been closed, so a
	// second attempt can still bind them.
	if err := run(t.Context(), cfg, testutil.DiscardLogger(), nil); err == nil {
		t.Error("second run succeeded; expected the same listen failure")
	}
}

// TestIngestAndAdminAreWired is a wiring smoke test, not a behaviour test: the
// §4.4 ingest route and the §5.9 admin mux both answer on the ports run() bound.
// Everything they do is covered in internal/ingest and internal/adminapi; what
// is only testable here is that main actually mounted them.
func TestIngestAndAdminAreWired(t *testing.T) {
	cfg := testutil.Config(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type addrs struct{ public, admin net.Addr }
	addrCh := make(chan addrs, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, testutil.DiscardLogger(), func(p, a net.Addr) { addrCh <- addrs{p, a} })
	}()

	var got addrs
	select {
	case got = <-addrCh:
	case err := <-errCh:
		t.Fatalf("catlogd exited during startup: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("catlogd never became ready")
	}

	// The ingest route is mounted: no Content-Encoding is a 415 (§4.4), which
	// only the real handler produces.
	res, err := http.Post("http://"+got.public.String()+"/v1/ingest", "application/x-ndjson", nil)
	if err != nil {
		t.Fatalf("POST /v1/ingest: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("POST /v1/ingest without Content-Encoding = %d (%s), want 415", res.StatusCode, body)
	}
	if res.Header.Get("Date") == "" {
		t.Error("the ingest handler must always set Date (§4.4)")
	}

	// The admin mux is up on its own port, and it is not the public one.
	if got.admin.String() == got.public.String() {
		t.Fatal("the admin mux shares the public listener")
	}
	adminRes, err := http.Get("http://" + got.admin.String() + "/admin/healthz")
	if err != nil {
		t.Fatalf("GET /admin/healthz: %v", err)
	}
	adminRes.Body.Close()
	if adminRes.StatusCode != http.StatusOK {
		t.Errorf("GET /admin/healthz = %d, want 200", adminRes.StatusCode)
	}

	// /admin/issue must not be reachable on the public port.
	pubAdmin, err := http.Get("http://" + got.public.String() + "/admin/healthz")
	if err != nil {
		t.Fatalf("GET public /admin/healthz: %v", err)
	}
	pubAdmin.Body.Close()
	if pubAdmin.StatusCode != http.StatusNotFound {
		t.Errorf("the admin API answered on the public port with %d", pubAdmin.StatusCode)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("shutdown returned an error: %v", err)
	}
}

// TestProbeHealth covers `catlogd -healthcheck`, the container HEALTHCHECK
// (§4.4). The four cases are the four ways the probe is asked to decide:
// a real server, a server that is not there, one answering the wrong status,
// and one answering 200 with a body that is not ours — the last because a
// probe that accepted any 200 would call a misrouted proxy healthy.
func TestProbeHealth(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(healthz))
		defer srv.Close()
		if err := probeHealth(hostPort(t, srv.URL)); err != nil {
			t.Fatalf("probeHealth on a healthy server: %v", err)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		// A port that was just released: nothing is bound, and the dial fails
		// fast rather than hanging until the probe's own timeout.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		if err := probeHealth(addr); err == nil {
			t.Fatal("probeHealth succeeded against a closed port")
		}
	})

	t.Run("wrong status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		if err := probeHealth(hostPort(t, srv.URL)); err == nil {
			t.Fatal("probeHealth succeeded against a 503")
		}
	})

	t.Run("wrong body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := io.WriteString(w, `{"ok":false}`); err != nil {
				t.Error(err)
			}
		}))
		defer srv.Close()
		if err := probeHealth(hostPort(t, srv.URL)); err == nil {
			t.Fatal("probeHealth succeeded against a 200 that was not ours")
		}
	})

	// The bind-address rewrite: 0.0.0.0 is not a destination, and the container
	// listens on it. Without the rewrite the probe would fail on every healthy
	// production container, which is the failure mode this case exists for.
	t.Run("wildcard bind address probes loopback", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(healthz)}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		_, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := probeHealth(net.JoinHostPort("0.0.0.0", port)); err != nil {
			t.Fatalf("probeHealth on 0.0.0.0: %v", err)
		}
	})
}

// hostPort strips the scheme from an httptest URL: probeHealth takes a listen
// address, which is what the config holds, not a URL.
func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
