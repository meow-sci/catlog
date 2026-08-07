//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// startupTimeout bounds how long we wait for a freshly built catlogd to answer
// /healthz. Generous: the first run also creates keys and migrates two
// databases.
const startupTimeout = 60 * time.Second

// server is a running catlogd built from this working tree.
type server struct {
	t         *testing.T
	dataDir   string
	binDir    string
	baseURL   string
	adminURL  string
	cmd       *exec.Cmd
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	stopped   bool
	catlogctl string
}

// startServer builds catlogd and catlogctl, boots catlogd against a fresh data
// directory and waits for it to serve /healthz. extraEnv overrides configuration
// the way §5.3's CATLOG_* variables do.
func startServer(t *testing.T, extraEnv ...string) *server {
	t.Helper()

	binDir := t.TempDir()
	build(t, binDir)

	// Ports are chosen up front rather than by binding :0, because the license
	// `iss` and the accepted `htu` have to name the port before the process
	// starts (§4.5.2 compares htu by exact string equality).
	public := freePort(t)
	admin := freePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", public)

	s := &server{
		t:         t,
		dataDir:   t.TempDir(),
		binDir:    binDir,
		baseURL:   baseURL,
		adminURL:  fmt.Sprintf("http://127.0.0.1:%d", admin),
		stdout:    &bytes.Buffer{},
		stderr:    &bytes.Buffer{},
		catlogctl: filepath.Join(binDir, "catlogctl"),
	}

	s.cmd = exec.Command(filepath.Join(binDir, "catlogd"))
	s.cmd.Env = append(os.Environ(),
		"CATLOG_DATA_DIR="+s.dataDir,
		"CATLOG_DATA_CHECKPOINT_INTERVAL_S=1",
		fmt.Sprintf("CATLOG_SERVER_LISTEN=127.0.0.1:%d", public),
		fmt.Sprintf("CATLOG_SERVER_ADMIN_LISTEN=127.0.0.1:%d", admin),
		"CATLOG_SERVER_BASE_URL="+baseURL,
		"CATLOG_INGEST_ACCEPTED_HTU="+baseURL+"/v1/ingest",
	)
	s.cmd.Env = append(s.cmd.Env, extraEnv...)
	s.cmd.Stdout = s.stdout
	s.cmd.Stderr = s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start catlogd: %v", err)
	}
	// Registered before the cleanup, so a process that somehow escapes its
	// t.Cleanup is still reaped by TestMain (see main_test.go): a catlogd that
	// outlives its test holds an exclusive database file lock (§5.4).
	trackChild(s.cmd, "catlogd")
	t.Cleanup(func() { s.stop() })

	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		res, err := http.Get(s.baseURL + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return s
			}
		}
		if s.cmd.ProcessState != nil {
			t.Fatalf("catlogd exited during startup\nstdout:\n%s\nstderr:\n%s", s.stdout, s.stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("catlogd never answered /healthz\nstdout:\n%s\nstderr:\n%s", s.stdout, s.stderr)
	return nil
}

// stop sends SIGTERM and waits for a clean exit, which is also what releases
// the Turso file locks (§5.4) so the test can open events.db itself.
func (s *server) stop() {
	if s.stopped {
		return
	}
	s.stopped = true
	// Deregistered unconditionally: whichever branch below runs, this function
	// is the one responsible for the process from here on, and leaving it in
	// the registry would have TestMain report a leak that is not one.
	defer untrackChild(s.cmd)

	if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
		s.t.Logf("signalling catlogd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			s.t.Errorf("catlogd exited with error: %v\nstdout:\n%s\nstderr:\n%s", err, s.stdout, s.stderr)
		}
	case <-time.After(gracefulStop):
		// Kill *and reap*: an unwaited child stays a zombie, and on some
		// platforms the file lock is not released until it is reaped (§5.4).
		_ = s.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		s.t.Errorf("catlogd did not shut down within %s; killed\nstdout:\n%s\nstderr:\n%s",
			gracefulStop, s.stdout, s.stderr)
	}
}

func build(t *testing.T, outDir string) {
	t.Helper()
	// All three binaries: the identity suite (WP3) drives the OAuth dance
	// against a real mockidp process, not a stub.
	cmd := exec.Command("go", "build", "-o", outDir+string(os.PathSeparator), "./cmd/...")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// issue runs `catlogctl issue` against the live admin API and returns the
// credential file it wrote (§4.6, §5.9).
func (s *server) issue(handle string) credential {
	s.t.Helper()

	outDir := s.t.TempDir()
	cmd := exec.Command(s.catlogctl, "issue", "-handle", handle, "-out", outDir, "-admin", s.adminURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("catlogctl issue: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "catlog-credential.json"))
	if err != nil {
		s.t.Fatalf("read credential: %v", err)
	}
	var c credential
	if err := json.Unmarshal(raw, &c); err != nil {
		s.t.Fatalf("credential is not JSON: %v", err)
	}
	if c.Format != 1 || c.Handle != handle || c.License == "" || c.PrivateKeyPEM == "" {
		s.t.Fatalf("incomplete credential file: %+v", c)
	}
	if c.key, err = cjws.ParsePrivateKeyPEM([]byte(c.PrivateKeyPEM)); err != nil {
		s.t.Fatalf("credential private key: %v", err)
	}
	if c.jkt, err = cjws.ThumbprintPublicKey(&c.key.PublicKey); err != nil {
		s.t.Fatal(err)
	}
	return c
}

// credential is the §4.6 file, plus the two derived values a shipper needs.
type credential struct {
	Format        int    `json:"format"`
	Handle        string `json:"handle"`
	License       string `json:"license"`
	PrivateKeyPEM string `json:"private_key_pem"`

	key *ecdsa.PrivateKey
	jkt string
}

// shipper is the Go-side stand-in for the mod's outbox shipper (§7.2): it owns
// a stream, signs one proof per batch and chains `ph` to the previous body.
type shipper struct {
	t      *testing.T
	cred   credential
	url    string
	sid    ids.ID
	seq    int64
	prev   []byte
	client *http.Client
}

func newShipper(t *testing.T, s *server, cred credential) *shipper {
	t.Helper()
	sid, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	return &shipper{
		t: t, cred: cred, url: s.baseURL + "/v1/ingest", sid: sid, seq: 1,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// shipOpts are the deliberate deviations one call makes from the happy path.
type shipOpts struct {
	Seq      int64
	JTI      string
	RawBody  []byte // sent instead of brotli(ndjson)
	Skew     time.Duration
	NoAdvace bool
}

type shipResult struct {
	Status int
	Body   map[string]any
	Header http.Header
}

// ship posts one batch, mirroring what the mod does per §4.3/§4.5.2.
func (sh *shipper) ship(ndjson []byte, opts ...func(*shipOpts)) shipResult {
	sh.t.Helper()

	var o shipOpts
	for _, fn := range opts {
		fn(&o)
	}

	body := o.RawBody
	if body == nil {
		var buf bytes.Buffer
		w := brotli.NewWriterLevel(&buf, brotli.DefaultCompression)
		if _, err := w.Write(ndjson); err != nil {
			sh.t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			sh.t.Fatal(err)
		}
		body = buf.Bytes()
	}

	seq := o.Seq
	if seq == 0 {
		seq = sh.seq
	}
	jti := o.JTI
	if jti == "" {
		id, err := ids.New()
		if err != nil {
			sh.t.Fatal(err)
		}
		jti = ids.String(id)
	}

	claims := authz.ProofClaims{
		JTI:      jti,
		IssuedAt: time.Now().Add(o.Skew).Unix(),
		HTM:      "POST",
		HTU:      sh.url,
		BH:       authz.BodyHash(body),
		SID:      ids.String(sh.sid),
		Seq:      seq,
	}
	if seq > 1 && sh.prev != nil {
		claims.PH = authz.BodyHash(sh.prev)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		sh.t.Fatal(err)
	}
	proof, err := cjws.SignES256(sh.cred.key, payload, cjws.SignOptions{Type: authz.ProofType, EmbedJWK: true})
	if err != nil {
		sh.t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, sh.url, bytes.NewReader(body))
	if err != nil {
		sh.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "br")
	req.Header.Set("X-Catlog-License", sh.cred.License)
	req.Header.Set("X-Catlog-Proof", proof)

	res, err := sh.client.Do(req)
	if err != nil {
		sh.t.Fatalf("POST /v1/ingest: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		sh.t.Fatal(err)
	}
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			sh.t.Fatalf("response is not JSON (%d): %q", res.StatusCode, raw)
		}
	}

	if res.StatusCode == http.StatusOK && decoded["replay"] != true && !o.NoAdvace {
		sh.prev = body
		sh.seq = seq + 1
	}
	return shipResult{Status: res.StatusCode, Body: decoded, Header: res.Header}
}

// goldenBatch is the committed conformance batch (§4.10) — the same bytes the
// C# suite ships.
func goldenBatch(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "batches", "batch-001.ndjson"))
	if err != nil {
		t.Fatalf("read the golden batch: %v", err)
	}
	return b
}

// TestIngestEndToEnd is the WP2 acceptance test: a real catlogd, a credential
// minted through the real admin API and CLI, the golden batch shipped over
// HTTP, and then — once the process has exited and released its file lock — the
// rows it wrote, read straight out of events.db.
func TestIngestEndToEnd(t *testing.T) {
	// A generous burst so the scenarios below are order-independent; the §4.3
	// default is exercised on its own in TestIngestRateLimit.
	s := startServer(t, "CATLOG_LIMITS_RATELIMIT_BURST=100")
	cred := s.issue("integration_cat")

	// §4.6: the loader refuses to ship unless the key thumbprints to the
	// license's cnf.jkt. Check the file the CLI wrote really satisfies that.
	_, payload, err := cjws.ParseCompactUnverified(cred.License)
	if err != nil {
		t.Fatalf("parse license: %v", err)
	}
	var claims authz.LicenseClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Cnf.JKT != cred.jkt {
		t.Fatalf("license cnf.jkt %q, key thumbprint %q", claims.Cnf.JKT, cred.jkt)
	}
	if claims.Issuer != s.baseURL {
		t.Errorf("license iss = %q, want %q", claims.Issuer, s.baseURL)
	}

	batch := goldenBatch(t)
	events := bytes.Count(bytes.TrimRight(batch, "\n"), []byte("\n")) + 1
	sh := newShipper(t, s, cred)

	var firstJTI string
	t.Run("first batch is stored", func(t *testing.T) {
		id, err := ids.New()
		if err != nil {
			t.Fatal(err)
		}
		firstJTI = ids.String(id)

		res := sh.ship(batch, func(o *shipOpts) { o.JTI = firstJTI })
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(events) {
			t.Errorf("accepted = %v, want %d", res.Body["accepted"], events)
		}
		if res.Header.Get("Date") == "" {
			t.Error("no Date header (§4.4)")
		}
	})

	t.Run("replaying a batch id short-circuits", func(t *testing.T) {
		res := sh.ship(batch, func(o *shipOpts) { o.JTI = firstJTI; o.Seq = 1; o.NoAdvace = true })
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["replay"] != true || res.Body["accepted"] != float64(0) {
			t.Fatalf("body = %v, want a replay short-circuit", res.Body)
		}
	})

	t.Run("the same events under a new batch id dedup", func(t *testing.T) {
		res := sh.ship(batch)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(0) || res.Body["deduped"] != float64(events) {
			t.Fatalf("body = %v, want accepted 0 deduped %d", res.Body, events)
		}
	})

	t.Run("a reused seq forks the stream", func(t *testing.T) {
		res := sh.ship(batch, func(o *shipOpts) { o.Seq = 1; o.NoAdvace = true })
		if res.Status != http.StatusConflict {
			t.Fatalf("status = %d, body %v, want 409", res.Status, res.Body)
		}
		if res.Body["error"] != authz.CodeStreamFork {
			t.Errorf("error = %v, want %s", res.Body["error"], authz.CodeStreamFork)
		}
	})

	t.Run("clock skew is rejected with the server time", func(t *testing.T) {
		res := sh.ship(batch, func(o *shipOpts) { o.Skew = -10 * time.Minute; o.NoAdvace = true })
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("status = %d, body %v, want 401", res.Status, res.Body)
		}
		if res.Body["error"] != authz.CodeClockSkew {
			t.Fatalf("error = %v, want %s", res.Body["error"], authz.CodeClockSkew)
		}
		if _, ok := res.Body["server_time"]; !ok {
			t.Error("no server_time in the 401 body (§4.4)")
		}
		// The mod's recovery path: re-read Date, re-sign, retry once.
		if _, err := http.ParseTime(res.Header.Get("Date")); err != nil {
			t.Errorf("Date header is unusable: %v", err)
		}
	})

	t.Run("an oversize body is refused", func(t *testing.T) {
		big := make([]byte, (1<<20)+1)
		for i := range big {
			big[i] = byte(i * 31) // incompressible enough to stay over the cap
		}
		res := sh.ship(nil, func(o *shipOpts) { o.RawBody = big; o.NoAdvace = true })
		if res.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, body %v, want 413", res.Status, res.Body)
		}
		if res.Body["error"] != authz.CodeTooLarge {
			t.Errorf("error = %v, want %s", res.Body["error"], authz.CodeTooLarge)
		}
	})

	t.Run("a new stream recovers from the fork", func(t *testing.T) {
		sid, err := ids.New()
		if err != nil {
			t.Fatal(err)
		}
		sh.sid, sh.seq, sh.prev = sid, 1, nil

		res := sh.ship(batch)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["deduped"] != float64(events) {
			t.Errorf("body = %v, want every event deduped on the new stream", res.Body)
		}
	})

	t.Run("the admin mux exposes the ingest counters", func(t *testing.T) {
		res, err := http.Get(s.adminURL + "/debug/vars")
		if err != nil {
			t.Fatalf("GET /debug/vars: %v", err)
		}
		defer res.Body.Close()

		var vars map[string]any
		if err := json.NewDecoder(res.Body).Decode(&vars); err != nil {
			t.Fatalf("expvar output: %v", err)
		}
		if got := vars["ingest_accepted"]; got != float64(events) {
			t.Errorf("ingest_accepted = %v, want %d", got, events)
		}
		if got := vars["ingest_deduped"]; got != float64(2*events) {
			t.Errorf("ingest_deduped = %v, want %d", got, 2*events)
		}
		if got := vars["ingest_rejected_stream_fork"]; got != float64(1) {
			t.Errorf("ingest_rejected_stream_fork = %v, want 1", got)
		}
		if got := vars["ingest_rejected_clock_skew"]; got != float64(1) {
			t.Errorf("ingest_rejected_clock_skew = %v, want 1", got)
		}
		if got := vars["ingest_rejected_too_large"]; got != float64(1) {
			t.Errorf("ingest_rejected_too_large = %v, want 1", got)
		}
	})

	// Everything above went over HTTP. Now stop the server — which releases the
	// exclusive database lock (§5.4) — and read the rows it wrote.
	s.stop()
	assertStoredRows(t, s, cred, events)
}

// assertStoredRows opens the events database the server just released and
// checks what actually landed.
func assertStoredRows(t *testing.T, s *server, cred credential, events int) {
	t.Helper()

	ctx := context.Background()
	db, err := store.OpenEvents(ctx, filepath.Join(s.dataDir, "events.db"), store.Options{})
	if err != nil {
		t.Fatalf("open events.db after shutdown: %v", err)
	}
	defer db.Close()

	c, err := db.CredentialByJKT(ctx, cred.jkt)
	if err != nil {
		t.Fatalf("credential row: %v", err)
	}
	if c.Handle != cred.Handle || c.Revoked() {
		t.Errorf("credential row = %+v", c)
	}

	n, err := db.CountEvents(ctx, c.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(events) {
		t.Errorf("stored %d events, want %d — the batch was shipped four times and must dedup", n, events)
	}

	rows, err := db.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, row := range rows {
		types = append(types, row.Type)
		if row.RecvTime == 0 || row.WallTime == 0 {
			t.Errorf("row %d has no timestamps: %+v", row.Seq, row)
		}
	}
	// The golden batch opens with session.started, whose `flight` is null.
	if len(rows) > 0 {
		if rows[0].Type != "session.started" {
			t.Errorf("first row is %q, want session.started", rows[0].Type)
		}
		if !rows[0].FlightID.IsZero() {
			t.Error("session.started must store a NULL flight_id")
		}
	}
	if !strings.Contains(strings.Join(types, ","), "vehicle.impact") {
		t.Errorf("stored types = %v, want the golden batch's vehicle.impact", types)
	}

	// Two streams were used: the forked one and its replacement.
	if _, found, err := db.StreamState(ctx, nil, c.PlayerID, ids.Zero); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("a stream_state row exists for the zero sid")
	}
}

// TestIngestRateLimit exercises the §4.3 token bucket against a server running
// the shipped defaults: 1 batch / 2 s, burst 5, keyed by credential.
func TestIngestRateLimit(t *testing.T) {
	s := startServer(t)
	cred := s.issue("rate_limited_cat")
	sh := newShipper(t, s, cred)
	batch := goldenBatch(t)

	// The burst is 5. The sixth batch inside two seconds must be refused, and
	// the refusal must tell the mod how long to wait.
	for i := range 5 {
		if res := sh.ship(batch); res.Status != http.StatusOK {
			t.Fatalf("burst batch %d: status %d body %v", i+1, res.Status, res.Body)
		}
	}
	res := sh.ship(batch, func(o *shipOpts) { o.NoAdvace = true })
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body %v, want 429", res.Status, res.Body)
	}
	if res.Body["error"] != authz.CodeRateLimited {
		t.Errorf("error = %v, want %s", res.Body["error"], authz.CodeRateLimited)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After (§4.4)")
	}

	// A second credential has its own bucket: the limit is per credential, not
	// per server or per IP.
	other := s.issue("unaffected_cat")
	if res := newShipper(t, s, other).ship(batch); res.Status != http.StatusOK {
		t.Fatalf("a second credential was rate limited by the first: %d %v", res.Status, res.Body)
	}
}
