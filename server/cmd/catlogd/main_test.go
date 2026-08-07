package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
