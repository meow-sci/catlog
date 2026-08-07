//go:build docker

// The six §6.3 tests: a real nginx container, configured from the file that
// actually ships (infra/nginx/dev.conf), in front of catlogd's real ingest
// handler.
//
// Run with `make test-nginx`. Without a reachable docker daemon every test
// skips with the reason — `make test` must never need docker (§9.4).
package nginxproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

const (
	// nginxImage is pinned by tag on purpose: §6.3 names it, and "stable" is
	// what the VPS runs.
	nginxImage = "nginx:stable-alpine"

	// proxyPort is the port dev.conf listens on (§3).
	proxyPort = "8081/tcp"

	// staticRoot is where the container's static assets live. It must be a
	// directory that already exists in the image — docker's put-archive does
	// not create parents.
	staticRoot = "/usr/share/nginx/html"

	// startupTimeout covers the image pull on a cold machine.
	startupTimeout = 3 * time.Minute
)

// devConfPath is infra/nginx/dev.conf, relative to this package.
var devConfPath = filepath.Join("..", "..", "..", "infra", "nginx", "dev.conf")

// TestNginxProxy is the whole §6.3 suite. One container serves all six cases:
// starting six would multiply a slow image pull by six and prove nothing extra,
// and the subtests are ordered so that the one which deliberately exhausts the
// limit_req zone runs last.
func TestNginxProxy(t *testing.T) {
	skipWithoutDocker(t)

	r := newRig(t)

	t.Run("1_ingest_round_trip", r.testIngestRoundTrip)
	t.Run("2_x_forwarded_for_reaches_the_handler", r.testForwardedFor)
	t.Run("3_oversize_body_is_413_from_nginx", r.testOversizeBody)
	t.Run("5_sse_frame_arrives_promptly", r.testSSEUnbuffered)
	t.Run("6_admin_is_403", r.testAdminForbidden)
	t.Run("static_assets_are_served_by_nginx", r.testStatic)
	// Last: this one deliberately empties the limit_req bucket, and the zone
	// refills at 10r/s.
	t.Run("4_burst_is_rate_limited", r.testBurstRateLimited)
}

// skipWithoutDocker is the §6.3 / §9.4 skip path. A missing daemon is not a
// test failure — it is a test that cannot run.
//
// The probe is deliberately more than a ping, because a ping is not decisive.
// `Provider.Health` only asks the endpoint for `/info`, and every
// Docker-API-compatible engine answers that. On a machine where Docker Desktop
// is stopped but a podman machine has linked its socket at
// /var/run/docker.sock — which is the exact state of the machine this suite was
// written on — the ping succeeds and the run then dies forty lines deep inside
// container creation with "unable to find network with name or ID bridge".
// Testcontainers' ProviderDocker hardcodes Docker's default `bridge` network
// for the reaper and for the SSH tunnel behind WithHostPortAccess; podman's
// compat API answers an *inspect* of "bridge" (it fabricates the network on
// demand) but rejects it at container create, so probing for the network proves
// nothing. Identifying the engine does.
func skipWithoutDocker(t *testing.T) {
	t.Helper()

	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		t.Skip(dockerUnavailable("no usable docker configuration", err))
	}
	dp, ok := provider.(*testcontainers.DockerProvider)
	if !ok {
		_ = provider.Close()
		t.Skip(dockerUnavailable("the docker provider is not a DockerProvider", nil))
	}
	defer dp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	host := dp.Client().DaemonHost()
	if _, err := dp.Client().Ping(ctx, client.PingOptions{}); err != nil {
		t.Skip(dockerUnavailable("cannot reach the daemon at "+host, err))
	}

	ver, err := dp.Client().ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Skip(dockerUnavailable("cannot read the version of the engine at "+host, err))
	}
	if !isDockerEngine(ver) {
		t.Skip(dockerUnavailable(fmt.Sprintf(
			"the engine at %s is %q (version %s), not Docker; testcontainers' ProviderDocker "+
				"needs Docker's `bridge` network, which a compat API accepts on inspect and "+
				"rejects on container create.\n"+
				"\tIf that socket is podman's, `export DOCKER_HOST=unix://<…>/podman.sock` makes "+
				"testcontainers select its podman provider", host, engineName(ver), ver.Version), nil))
	}
}

// isDockerEngine reports whether /version came from Docker. Docker names its
// engine component exactly "Engine"; podman answers "Podman Engine", which
// contains it — hence the equality test rather than a substring one. An engine
// that reports no components at all is given the benefit of the doubt: the
// field is documented as informational, and a false skip is worse than a
// failure that names its own cause.
func isDockerEngine(ver client.ServerVersionResult) bool {
	if len(ver.Components) == 0 {
		return true
	}
	for _, c := range ver.Components {
		if c.Name == "Engine" {
			return true
		}
	}
	return false
}

func engineName(ver client.ServerVersionResult) string {
	if len(ver.Components) > 0 {
		return ver.Components[0].Name
	}
	if ver.Platform.Name != "" {
		return ver.Platform.Name
	}
	return "unknown"
}

func dockerUnavailable(reason string, err error) string {
	msg := "docker unavailable: " + reason
	if err != nil {
		msg += ": " + err.Error()
	}
	return msg + "\n" +
		"\tThe nginx suite (§6.3) needs a running Docker daemon; `make test` never does (§9.4).\n" +
		"\tmacOS: open -a Docker    Linux: sudo systemctl start docker\n" +
		"\tOr point DOCKER_HOST at a real docker daemon, then re-run: make test-nginx"
}

// --- fixture ---------------------------------------------------------------

// rig is catlogd's real handlers on a host port, with nginx in front of them.
//
// Order matters and is the reason for the deferred wiring below: nginx cannot
// start until the upstream port is known, and the ingest verifier cannot be
// built until nginx's mapped port is known — the proof's `htu` is compared to
// the configured URL by exact string equality (§4.5.2), and through the proxy
// that URL is nginx's, not the upstream's.
type rig struct {
	t *testing.T

	upstream *httptest.Server
	hostPort int
	baseURL  string // through nginx, e.g. http://127.0.0.1:32770

	events *store.Events
	cred   testutil.Cred
	spy    *spy
	hub    *sseHub

	client *http.Client
}

func newRig(t *testing.T) *rig {
	t.Helper()

	r := &rig{
		t:      t,
		spy:    &spy{},
		hub:    newSSEHub(),
		client: &http.Client{Timeout: 30 * time.Second},
	}
	r.events = testutil.Events(t)
	ks := testutil.Keys(t)

	// The ingest handler is installed after nginx is up (see above), so the mux
	// routes through a holder that is empty until then.
	late := &deferred{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	mux.Handle("POST /v1/ingest", late)
	mux.Handle("GET /v1/feed/sse", r.hub)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>catlog</title>")
	})

	// httptest binds 127.0.0.1:0, which is what WithHostPortAccess needs: the
	// tunnel dials this process's own loopback.
	r.upstream = httptest.NewUnstartedServer(r.spy.wrap(mux))
	r.upstream.Start()
	t.Cleanup(r.upstream.Close)
	addr, ok := r.upstream.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("upstream is not listening on TCP: %v", r.upstream.Listener.Addr())
	}
	r.hostPort = addr.Port

	r.startNginx()

	// Now the proxy URL is known: build the verification chain against it.
	v := authz.New(authz.Config{
		Issuer:      r.baseURL,
		AcceptedHTU: []string{r.baseURL + "/v1/ingest"},
		// Deliberately far above §4.3's 1 batch / 2 s. This suite asserts what
		// nginx does; leaving catlogd's own limiter in play would make a 429
		// ambiguous, and subtest 4 asserts precisely that nginx produced it.
		RatePerSecond: 1000,
		Burst:         1000,
	}, ks, r.events, authz.NewDenyList())

	w := ingest.NewWriter(r.events, testutil.DiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	t.Cleanup(func() { cancel(); w.Wait() })

	late.set(ingest.NewHandler(v, w, ingest.DefaultLimits(), testutil.DiscardLogger()))
	r.cred = testutil.Credential(t, r.events, ks, r.baseURL, "nginx_cat")

	return r
}

// startNginx renders dev.conf and runs it in a container, mapping 8081.
func (r *rig) startNginx() {
	r.t.Helper()

	conf := renderDevConf(r.t, fmt.Sprintf("host.testcontainers.internal:%d", r.hostPort), staticRoot)

	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, nginxImage,
		testcontainers.WithExposedPorts(proxyPort),
		// Opens the SSH tunnel that makes host.testcontainers.internal resolve
		// to this test process's loopback (§6.3).
		testcontainers.WithHostPortAccess(r.hostPort),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				Reader:            strings.NewReader(conf),
				ContainerFilePath: "/etc/nginx/nginx.conf",
				FileMode:          0o644,
			},
			testcontainers.ContainerFile{
				Reader:            strings.NewReader("static ok\n"),
				ContainerFilePath: staticRoot + "/ping.txt",
				FileMode:          0o644,
			},
		),
		// /healthz travels the catch-all location to the Go upstream, so a
		// ready container also proves the whole path is wired.
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/healthz").WithPort(proxyPort).WithStartupTimeout(startupTimeout),
		),
	)
	testcontainers.CleanupContainer(r.t, ctr)
	if err != nil {
		r.t.Fatalf("start %s: %v", nginxImage, err)
	}

	base, err := ctr.PortEndpoint(ctx, proxyPort, "http")
	if err != nil {
		r.t.Fatalf("resolve the mapped port: %v", err)
	}
	r.baseURL = base
}

// renderDevConf substitutes the two placeholders §6.1 defines. It fails loudly
// if either is missing: a dev.conf that hardcoded an upstream would silently
// test nothing.
func renderDevConf(t *testing.T, upstream, static string) string {
	t.Helper()

	raw, err := os.ReadFile(devConfPath)
	if err != nil {
		t.Fatalf("read %s: %v", devConfPath, err)
	}
	conf := string(raw)
	for _, placeholder := range []string{"$UPSTREAM", "$STATIC_ROOT"} {
		if !strings.Contains(conf, placeholder) {
			t.Fatalf("%s no longer contains %s — §6.1 requires both placeholders", devConfPath, placeholder)
		}
	}
	return strings.NewReplacer("$UPSTREAM", upstream, "$STATIC_ROOT", static).Replace(conf)
}

// deferred is an http.Handler whose target is installed after the mux is
// serving. 503 before that happens, which no test should ever see.
type deferred struct {
	mu sync.RWMutex
	h  http.Handler
}

func (d *deferred) set(h http.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.h = h
}

func (d *deferred) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	d.mu.RLock()
	h := d.h
	d.mu.RUnlock()
	if h == nil {
		http.Error(w, "handler not installed yet", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, req)
}

// spy counts what reaches the upstream and remembers the last request's
// headers. It wraps the mux, so what it records is what the handler was
// handed — the same *http.Request.
type spy struct {
	hits atomic.Int64

	mu     sync.Mutex
	last   http.Header
	lastAt string
}

func (s *spy) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.last = r.Header.Clone()
		s.last.Set("Host", r.Host) // Host is not in Header; keep it together
		s.lastAt = r.URL.Path
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *spy) lastHeaders() (http.Header, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.lastAt
}

// --- 1. ingest round-trip ---------------------------------------------------

// testIngestRoundTrip ships a real batch through nginx: two headers, a brotli
// body whose SHA-256 is covered by the proof (§4.5.2 `bh`), and a 200 with the
// accepted count. Any rewriting, re-compression or header mangling by the proxy
// shows up here as a 401 proof_invalid rather than as a subtle corruption.
func (r *rig) testIngestRoundTrip(t *testing.T) {
	sid := testutil.ULID(t)
	ndjson := r.batch(t, sid, 3)
	body := testutil.Brotli(t, ndjson)

	res, decoded := r.ship(t, body, testutil.ProofOpts{
		HTU:  r.baseURL + "/v1/ingest",
		SID:  sid,
		Seq:  1,
		Body: body,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v — the batch did not survive the proxy", res.StatusCode, decoded)
	}
	if decoded["accepted"] != float64(3) {
		t.Fatalf("accepted = %v, want 3 (body %v)", decoded["accepted"], decoded)
	}

	// The rows really landed, on the far side of nginx.
	n, err := r.events.CountEvents(t.Context(), r.cred.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("events.db holds %d events for the player, want 3", n)
	}
}

// --- 2. X-Forwarded-For -----------------------------------------------------

// testForwardedFor asserts the §6.1 proxy_set_header block reaches the Go
// handler. catlogd binds loopback, so without this header there is no way to
// attribute a request to a client at all.
func (r *rig) testForwardedFor(t *testing.T) {
	// Any request to /v1/ingest will do: the header is set by the location, and
	// the spy records what the handler received. This one is rejected 415 by
	// catlogd (no Content-Encoding), which is fine — it still reached it.
	req, err := http.NewRequest(http.MethodPost, r.baseURL+"/v1/ingest", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("POST through the proxy: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	headers, path := r.spy.lastHeaders()
	if path != "/v1/ingest" {
		t.Fatalf("the last upstream request was %q, want /v1/ingest", path)
	}

	xff := headers.Get("X-Forwarded-For")
	if xff == "" {
		t.Fatal("no X-Forwarded-For reached the handler")
	}
	first := strings.TrimSpace(strings.Split(xff, ",")[0])
	if net.ParseIP(first) == nil {
		t.Errorf("X-Forwarded-For = %q, whose first element is not an IP", xff)
	}
	if got := headers.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http (dev.conf is plaintext)", got)
	}
	if headers.Get("Host") == "" {
		t.Error("no Host reached the handler")
	}
}

// --- 3. oversize body -------------------------------------------------------

// testOversizeBody proves `client_max_body_size 2m` rejects a 3 MiB body at the
// proxy. catlogd's own cap is 1 MiB (§4.3); nginx's job is to make sure the
// bytes never cost the Go process anything.
func (r *rig) testOversizeBody(t *testing.T) {
	before := r.spy.hits.Load()

	big := bytes.Repeat([]byte("A"), 3<<20)
	req, err := http.NewRequest(http.MethodPost, r.baseURL+"/v1/ingest", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "br")
	req.ContentLength = int64(len(big))

	res, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("POST 3 MiB: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %.200q)", res.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(string(body)), "413") {
		t.Errorf("the 413 body does not look like nginx's error page: %.200q", body)
	}
	if after := r.spy.hits.Load(); after != before {
		t.Errorf("the upstream saw %d request(s) — nginx must reject the body before proxying it", after-before)
	}
}

// --- 4. limit_req -----------------------------------------------------------

// testBurstRateLimited fires 40 requests at /v1/ingest. The zone is 10r/s with
// burst=20 nodelay, so the tail must come back 429 from nginx — with catlogd's
// own limiter set far above anything this test can reach, a 429 can only be the
// proxy's.
func (r *rig) testBurstRateLimited(t *testing.T) {
	const n = 40

	before := r.spy.hits.Load()

	var limited, htmlBodies int
	for range n {
		req, err := http.NewRequest(http.MethodPost, r.baseURL+"/v1/ingest", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.client.Do(req)
		if err != nil {
			t.Fatalf("burst request: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode == http.StatusTooManyRequests {
			limited++
			// nginx's own error page is HTML; catlogd answers §4.9 JSON. This
			// is what distinguishes the two 429s.
			if strings.Contains(res.Header.Get("Content-Type"), "text/html") &&
				strings.Contains(string(body), "429") {
				htmlBodies++
			}
		}
	}

	if limited == 0 {
		t.Fatalf("%d rapid requests produced no 429 — limit_req is not in effect", n)
	}
	if htmlBodies == 0 {
		t.Errorf("%d requests were 429'd but none carried nginx's error page — the limiter may be catlogd's, not the proxy's", limited)
	}
	if reached := r.spy.hits.Load() - before; reached >= n {
		t.Errorf("all %d requests reached the upstream; nginx rejected none", n)
	}
	t.Logf("%d/%d requests were rejected by limit_req, %d reached the upstream",
		limited, n, r.spy.hits.Load()-before)
}

// --- 5. SSE -----------------------------------------------------------------

// testSSEUnbuffered proves `proxy_buffering off` on the feed location. With
// buffering on, nginx holds a small flushed frame until its buffer fills, which
// is indistinguishable from a dead feed for a datastar client (§5.7).
func (r *rig) testSSEUnbuffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/v1/feed/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// No client timeout: the stream is meant to stay open.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET /v1/feed/sse: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if got := res.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no (§6.1 add_header)", got)
	}

	// Wait for the handler to register the subscriber, so the measurement below
	// times nginx and not the connection setup.
	if !r.hub.waitForClient(5 * time.Second) {
		t.Fatal("the SSE handler never saw the connection")
	}

	lines := make(chan string, 1)
	go func() {
		br := bufio.NewReader(res.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				lines <- strings.TrimSpace(line)
				return
			}
		}
	}()

	start := time.Now()
	r.hub.broadcast("lithobrake at 214 m/s")

	select {
	case line := <-lines:
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("the frame took %s to arrive — nginx is buffering the feed", elapsed)
		} else {
			t.Logf("frame arrived in %s: %s", elapsed, line)
		}
	case <-time.After(time.Second):
		t.Fatal("no SSE frame within 1 s — proxy_buffering is not off (§6.1)")
	}
}

// --- 6. /admin/ -------------------------------------------------------------

// testAdminForbidden covers the belt-and-suspenders 403. The admin mux binds
// 127.0.0.1:6060 and refuses non-loopback peers (§5.9), so this is the second
// of three independent reasons /admin/ is unreachable from outside — it is the
// one that survives a typo in catlogd.toml.
func (r *rig) testAdminForbidden(t *testing.T) {
	before := r.spy.hits.Load()

	for _, path := range []string{"/admin/", "/admin/issue", "/admin/ban"} {
		res, err := r.client.Get(r.baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s → %d, want 403", path, res.StatusCode)
		}
	}
	if after := r.spy.hits.Load(); after != before {
		t.Errorf("%d /admin/ request(s) reached the upstream; nginx must answer them itself", after-before)
	}
}

// --- static assets (beyond §6.3, one line of config away from silently 404ing)

func (r *rig) testStatic(t *testing.T) {
	before := r.spy.hits.Load()

	res, err := r.client.Get(r.baseURL + "/static/ping.txt")
	if err != nil {
		t.Fatalf("GET /static/ping.txt: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — check the `alias $STATIC_ROOT/` location", res.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "static ok" {
		t.Errorf("body = %q", body)
	}
	if after := r.spy.hits.Load(); after != before {
		t.Error("a /static/ request reached the upstream; nginx serves the assets")
	}
}

// --- helpers ----------------------------------------------------------------

// batch renders n valid §4.1 envelopes as NDJSON.
func (r *rig) batch(t *testing.T, session ids.ID, n int) []byte {
	t.Helper()

	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b,
			`{"id":%q,"type":"vehicle.impact","ver":1,"flight":%q,"session":%q,"sim_t":%d.5,"wall_t":%d,`+
				`"payload":{"speed_ms":%d,"energy_j":1000,"survived":true,"launch_pad":false,"body":"earth","crew_count":2}}`+"\n",
			ids.String(testutil.ULID(t)), ids.String(session), ids.String(session),
			i, time.Now().UnixMilli(), 60+i)
	}
	return []byte(b.String())
}

// ship posts one signed batch through the proxy, exactly as the mod would
// (§4.3, §4.5.2).
func (r *rig) ship(t *testing.T, body []byte, opts testutil.ProofOpts) (*http.Response, map[string]any) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, r.baseURL+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "br")
	req.Header.Set("X-Catlog-License", r.cred.License)
	req.Header.Set("X-Catlog-Proof", r.cred.Proof(t, opts))

	res, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/ingest through the proxy: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("response is not JSON (%d): %.300q", res.StatusCode, raw)
		}
	}
	return res, decoded
}

// --- SSE stand-in -----------------------------------------------------------

// sseHub is a minimal event-stream handler standing in for the datastar feed
// (§4.8, §5.7), which lands in WP4/WP5. What this suite asserts is nginx's
// behaviour — headers out, frame flushed, frame delivered — and that is
// identical whatever writes the frames.
type sseHub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

func newSSEHub() *sseHub { return &sseHub{clients: map[chan string]struct{}{}} }

func (h *sseHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan string, 8)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "event: datastar-patch-elements\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (h *sseHub) broadcast(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// waitForClient blocks until a connection has reached the handler, so a test
// cannot measure nginx's latency against a stream nobody is subscribed to.
func (h *sseHub) waitForClient(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.clients)
		h.mu.Unlock()
		if n > 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
