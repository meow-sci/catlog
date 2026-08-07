//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// mockidp is a running mock IdP built from this working tree (§5.8.1).
type mockidp struct {
	t       *testing.T
	baseURL string
	cmd     *exec.Cmd
	out     *bytes.Buffer
	stopped bool
}

// startMockIdP boots the real mockidp binary on a loopback port, serving the
// committed `server/mockidp.toml` cast. Nothing in this file ever touches a
// real identity provider (D2).
func startMockIdP(t *testing.T, binDir string) *mockidp {
	t.Helper()

	port := freePort(t)
	m := &mockidp{
		t:       t,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		out:     &bytes.Buffer{},
	}
	m.cmd = exec.Command(filepath.Join(binDir, "mockidp"),
		"-listen", fmt.Sprintf("127.0.0.1:%d", port),
		"-config", filepath.Join("..", "mockidp.toml"),
		"-base-url", m.baseURL)
	m.cmd.Stdout = m.out
	m.cmd.Stderr = m.out
	if err := m.cmd.Start(); err != nil {
		t.Fatalf("start mockidp: %v", err)
	}
	trackChild(m.cmd, "mockidp")
	t.Cleanup(func() { m.stop() })

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(m.baseURL + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return m
			}
		}
		if m.cmd.ProcessState != nil {
			t.Fatalf("mockidp exited during startup:\n%s", m.out)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mockidp never answered /healthz:\n%s", m.out)
	return nil
}

func (m *mockidp) stop() {
	if m.stopped {
		return
	}
	m.stopped = true
	defer untrackChild(m.cmd)

	_ = m.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- m.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = m.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// env renders the CATLOG_IDP_* overrides that point catlogd at this mockidp —
// the same values `server/catlogd.dev.toml` carries, with the port moved.
func (m *mockidp) env() []string {
	return []string{
		"CATLOG_IDP_DISCORD_AUTH_URL=" + m.baseURL + "/discord/oauth/authorize",
		"CATLOG_IDP_DISCORD_TOKEN_URL=" + m.baseURL + "/discord/oauth/token",
		"CATLOG_IDP_DISCORD_API_BASE=" + m.baseURL + "/discord/api",
		"CATLOG_IDP_DISCORD_CLIENT_ID=dev",
		"CATLOG_IDP_DISCORD_CLIENT_SECRET=dev",

		"CATLOG_IDP_GOOGLE_ISSUER=" + m.baseURL + "/google",
		"CATLOG_IDP_GOOGLE_AUTH_URL=" + m.baseURL + "/google/authorize",
		"CATLOG_IDP_GOOGLE_TOKEN_URL=" + m.baseURL + "/google/token",
		"CATLOG_IDP_GOOGLE_JWKS_URL=" + m.baseURL + "/google/jwks",
		"CATLOG_IDP_GOOGLE_CLIENT_ID=dev",
		"CATLOG_IDP_GOOGLE_CLIENT_SECRET=dev",

		"CATLOG_IDP_GITHUB_AUTH_URL=" + m.baseURL + "/github/login/oauth/authorize",
		"CATLOG_IDP_GITHUB_TOKEN_URL=" + m.baseURL + "/github/login/oauth/access_token",
		"CATLOG_IDP_GITHUB_API_BASE=" + m.baseURL + "/github",
		"CATLOG_IDP_GITHUB_CLIENT_ID=dev",
		"CATLOG_IDP_GITHUB_CLIENT_SECRET=dev",
	}
}

// startIdentityStack boots catlogd and mockidp together.
func startIdentityStack(t *testing.T, extraEnv ...string) (*server, *mockidp) {
	t.Helper()
	binDir := t.TempDir()
	build(t, binDir)
	m := startMockIdP(t, binDir)
	s := startServer(t, append(m.env(), extraEnv...)...)
	return s, m
}

// browser is a Go http.Client behaving like one: it keeps a cookie jar and
// follows redirects, which is all a login flow needs (§12 WP3).
type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newBrowser(t *testing.T, s *server) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &browser{t: t, base: s.baseURL, client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (b *browser) get(url string) (*http.Response, string) {
	b.t.Helper()
	res, err := b.client.Get(url)
	if err != nil {
		b.t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		b.t.Fatal(err)
	}
	return res, string(raw)
}

func (b *browser) postJSON(path string, body any) (*http.Response, []byte) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, b.base+path, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// What a browser's same-origin fetch sends, and what §4.5.4's CSRF check
	// looks at.
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		b.t.Fatal(err)
	}
	return res, raw
}

// sessionCookie reports whether the jar holds a catlog session.
func (b *browser) sessionCookie() *http.Cookie {
	u, err := url.Parse(b.base)
	if err != nil {
		b.t.Fatal(err)
	}
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == identity.SessionCookie {
			return c
		}
	}
	return nil
}

var loginLink = regexp.MustCompile(`id="([^"]+)"[^>]*href="([^"]+)"`)

// login drives the whole OAuth dance the way a person would: start the flow,
// land on the mock provider's consent page, click the button with the stable
// §5.8.1 DOM id, and follow the redirect back into catlogd.
func (b *browser) login(m *mockidp, idp, elementID string) (*http.Response, string) {
	b.t.Helper()

	res, page := b.get(b.base + "/auth/" + idp + "/start")
	if !strings.Contains(page, "mockidp") {
		b.t.Fatalf("/auth/%s/start did not land on mockidp (status %d):\n%s", idp, res.StatusCode, page)
	}
	if !strings.HasPrefix(res.Request.URL.String(), m.baseURL) {
		b.t.Fatalf("/auth/%s/start redirected to %s, want mockidp", idp, res.Request.URL)
	}

	href := consentLink(b.t, page, elementID)
	return b.get(m.baseURL + href)
}

// consentLink finds the href behind a stable §5.8.1 DOM id.
func consentLink(t *testing.T, page, elementID string) string {
	t.Helper()
	for _, match := range loginLink.FindAllStringSubmatch(page, -1) {
		if match[1] == elementID {
			return strings.ReplaceAll(match[2], "&amp;", "&")
		}
	}
	t.Fatalf("no #%s on the consent page:\n%s", elementID, page)
	return ""
}

// TestOAuthDanceAllThreeIdPs is the §12 WP3 integration case: the full code
// flow against mockidp, for Discord, Google and GitHub, with a cookie jar —
// then a handle claimed through the real dashboard API and the license checked.
func TestOAuthDanceAllThreeIdPs(t *testing.T) {
	s, m := startIdentityStack(t)

	for _, tc := range []struct {
		idp       string
		elementID string
		handle    string
	}{
		{"discord", "login-as-whiskers-discord-old-account", "e2e_whiskers"},
		{"google", "login-as-mittens-google", "e2e_mittens"},
		{"github", "login-as-clawdia-github", "e2e_clawdia"},
	} {
		t.Run(tc.idp, func(t *testing.T) {
			b := newBrowser(t, s)

			final, _ := b.login(m, tc.idp, tc.elementID)
			// The callback redirects to /dashboard, which WP5 owns; until then
			// it 404s. What matters here is where we landed and what we carry.
			if final.Request.URL.Path != identity.DashboardPath {
				t.Fatalf("landed on %s, want %s (status %d)", final.Request.URL.Path, identity.DashboardPath, final.StatusCode)
			}
			cookie := b.sessionCookie()
			if cookie == nil {
				t.Fatal("no session cookie after a completed login (§4.5.4)")
			}
			if parts := strings.Split(cookie.Value, "."); len(parts) != 3 {
				t.Errorf("session cookie has %d parts, want the §4.5.4 three", len(parts))
			}

			// GET /api/me — the session authenticates.
			res, body := b.get(s.baseURL + "/api/me")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /api/me = %d: %s", res.StatusCode, body)
			}
			var me identity.MeResponse
			mustJSON(t, []byte(body), &me)
			if me.IdP != tc.idp {
				t.Errorf("/api/me idp = %q, want %q", me.IdP, tc.idp)
			}
			if me.Sub == "" || me.HandleQuota != 5 || me.IssuanceQuota != 3 {
				t.Errorf("/api/me = %+v, want the §4.7 quotas", me)
			}

			// POST /api/handles — the private key is generated here and never
			// sent, exactly as the browser wizard does (§4.6, §5.7).
			key := testutil.ClientKey(t)
			jwk, err := cjws.PublicJWK(&key.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			jkt, err := cjws.ThumbprintPublicKey(&key.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			res, raw := b.postJSON("/api/handles", identity.ClaimRequest{Handle: tc.handle, JWK: jwk})
			if res.StatusCode != http.StatusOK {
				t.Fatalf("POST /api/handles = %d: %s", res.StatusCode, raw)
			}
			var issued identity.LicenseResponse
			mustJSON(t, raw, &issued)

			claims := decodeLicense(t, issued.License)
			if claims.Handle != tc.handle {
				t.Errorf("license handle = %q, want %q", claims.Handle, tc.handle)
			}
			if claims.Subject != me.Sub {
				t.Errorf("license sub = %q, want the account's user_key %q", claims.Subject, me.Sub)
			}
			if claims.Cnf.JKT != jkt {
				t.Errorf("license cnf.jkt = %q, want the thumbprint of the key we generated %q", claims.Cnf.JKT, jkt)
			}
			if claims.Issuer != s.baseURL {
				t.Errorf("license iss = %q, want %q", claims.Issuer, s.baseURL)
			}

			// And the credential shows up on the dashboard.
			res, body = b.get(s.baseURL + "/api/handles")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /api/handles = %d: %s", res.StatusCode, body)
			}
			var handles identity.HandlesResponse
			mustJSON(t, []byte(body), &handles)
			if len(handles.Handles) != 1 || handles.Handles[0].Handle != tc.handle {
				t.Fatalf("GET /api/handles = %+v", handles.Handles)
			}
			if len(handles.Handles[0].Credentials) != 1 || handles.Handles[0].Credentials[0].JKT != jkt {
				t.Errorf("credential list = %+v", handles.Handles[0].Credentials)
			}

			// The handle is live on the public read API within the same request.
			res, body = b.get(s.baseURL + "/v1/players/" + tc.handle)
			if res.StatusCode != http.StatusOK {
				t.Errorf("GET /v1/players/%s = %d: %s", tc.handle, res.StatusCode, body)
			}
		})
	}

	// Three separate accounts, no merging across providers (D10).
	var stats struct {
		Events struct {
			Players int64 `json:"players"`
			Handles int64 `json:"handles"`
		} `json:"events"`
	}
	s.adminJSON(t, http.MethodGet, "/admin/stats", &stats)
	if stats.Events.Players != 3 || stats.Events.Handles != 3 {
		t.Errorf("after three logins: %d players, %d handles, want 3 and 3", stats.Events.Players, stats.Events.Handles)
	}
}

// TestAccountAgeGateRejectsNewAccounts is the other direction of §4.7's gate,
// and the reason mockidp mints a snowflake at start-up.
func TestAccountAgeGateRejectsNewAccounts(t *testing.T) {
	s, m := startIdentityStack(t)

	for _, tc := range []struct{ idp, elementID string }{
		{"discord", "login-as-sprocket-discord-new-account"},
		{"github", "login-as-pixel-github-new-account"},
	} {
		t.Run(tc.idp, func(t *testing.T) {
			b := newBrowser(t, s)
			final, page := b.login(m, tc.idp, tc.elementID)

			if final.StatusCode != http.StatusForbidden {
				t.Fatalf("a brand-new account logged in: status %d", final.StatusCode)
			}
			if b.sessionCookie() != nil {
				t.Error("a refused login still set a session cookie")
			}
			// The §4.9 code lands in the attribute WP5's playwright suite reads.
			if !strings.Contains(page, `data-error="`+authz.CodeAccountTooNew+`"`) {
				t.Errorf("the refusal page does not carry data-error=%q:\n%s", authz.CodeAccountTooNew, page)
			}
			if !strings.Contains(page, `id="auth-error"`) {
				t.Error("the refusal page has no #auth-error element for WP5 to assert on")
			}
			if !strings.Contains(page, authz.CodeAccountTooNew) {
				t.Error("the refusal page does not name the error code")
			}
		})
	}
}

// TestOAuthStateIsEnforced checks §4.5.4's state cookie: a callback that did not
// come from a flow this browser started is refused.
func TestOAuthStateIsEnforced(t *testing.T) {
	s, m := startIdentityStack(t)
	b := newBrowser(t, s)

	// Start a flow, capture a real code, then complete the callback from a
	// *different* browser — the CSRF-of-login case the state exists for.
	_, page := b.get(s.baseURL + "/auth/discord/start")
	href := consentLink(t, page, "login-as-whiskers-discord-old-account")

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(m.baseURL + href)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code, state := loc.Query().Get("code"), loc.Query().Get("state")
	if code == "" || state == "" {
		t.Fatalf("mockidp redirect carried no code/state: %s", loc)
	}

	other := newBrowser(t, s)
	cbRes, body := other.get(s.baseURL + "/auth/discord/callback?code=" + code + "&state=" + state)
	if cbRes.StatusCode == http.StatusOK || other.sessionCookie() != nil {
		t.Fatalf("a callback with no matching state cookie succeeded: %d\n%s", cbRes.StatusCode, body)
	}

	// A tampered state fails for the browser that *did* start the flow.
	cbRes, body = b.get(s.baseURL + "/auth/discord/callback?code=" + code + "&state=tampered")
	if cbRes.StatusCode == http.StatusOK || b.sessionCookie() != nil {
		t.Fatalf("a mismatched state was accepted: %d\n%s", cbRes.StatusCode, body)
	}
}

// TestBanBlocksIngestAndUnbanRestoresIt is the §12 WP3 case: a credential minted
// through the real dashboard flow ships fine, stops the moment the account is
// banned, and works again when the ban is lifted.
func TestBanBlocksIngestAndUnbanRestoresIt(t *testing.T) {
	s, m := startIdentityStack(t, "CATLOG_LIMITS_RATELIMIT_BURST=100")

	b := newBrowser(t, s)
	b.login(m, "discord", "login-as-whiskers-discord-old-account")

	key := testutil.ClientKey(t)
	jwk, err := cjws.PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	res, raw := b.postJSON("/api/handles", identity.ClaimRequest{Handle: "ban_me", JWK: jwk})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("claim = %d: %s", res.StatusCode, raw)
	}
	var issued identity.LicenseResponse
	mustJSON(t, raw, &issued)

	pem, err := cjws.MarshalPrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	cred := credential{
		Format: 1, Handle: issued.Handle, License: issued.License, PrivateKeyPEM: pem,
		key: key, jkt: issued.JKT,
	}
	sh := newShipper(t, s, cred)
	batch := goldenBatch(t)

	if got := sh.ship(batch); got.Status != http.StatusOK {
		t.Fatalf("first ship = %d, body %v", got.Status, got.Body)
	}

	// Ban through the real CLI, which goes through the real admin API (§5.9).
	out := s.catlogctlRun(t, "ban", "-handle", "ban_me", "-reason", "integration test", "-admin", s.adminURL)
	if !strings.Contains(out, "handles retired:      1") {
		t.Errorf("catlogctl ban output:\n%s", out)
	}

	banned := sh.ship(batch, func(o *shipOpts) { o.NoAdvace = true })
	if banned.Status != http.StatusUnauthorized {
		t.Fatalf("ingest after a ban = %d, body %v, want 401", banned.Status, banned.Body)
	}
	// The deny-list is consulted at step 4, before any database access, so the
	// code is `banned` rather than `license_revoked` (§4.5.3, §5.8).
	if banned.Body["error"] != authz.CodeBanned {
		t.Errorf("error = %v, want %s", banned.Body["error"], authz.CodeBanned)
	}

	// The public read API forgets the player too (§4.8).
	if res, _ := s.public(t, "/v1/players/ban_me"); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/players/ban_me after a ban = %d, want 404", res.StatusCode)
	}

	// The published deny-list names them (§5.8). It is fetched raw rather than
	// through s.public: the body is a compact JWS, not JSON (see
	// docs/DECISIONS.md).
	denyRes, denyBody := s.rawGet(t, identity.DenyListPath)
	if denyRes.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", identity.DenyListPath, denyRes.StatusCode)
	}
	if got := denyRes.Header.Get("Content-Type"); got != "application/jose" {
		t.Errorf("deny-list Content-Type = %q", got)
	}
	_, payload, err := cjws.ParseCompactUnverified(strings.TrimSpace(string(denyBody)))
	if err != nil {
		t.Fatalf("the published deny-list is not a compact JWS: %v", err)
	}
	var doc identity.DenyListDocument
	mustJSON(t, payload, &doc)
	if len(doc.BannedSubs) != 1 || len(doc.RevokedJKTs) != 1 || doc.RevokedJKTs[0] != issued.JKT {
		t.Errorf("published deny-list = %+v", doc)
	}
	// And it verifies against the published JWKS, which is the whole point of
	// signing it.
	jwksRes, jwksBody := s.public(t, identity.JWKSPath)
	if jwksRes.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", identity.JWKSPath, jwksRes.StatusCode)
	}
	verifyAgainstJWKS(t, jwksBody, strings.TrimSpace(string(denyBody)))

	// Unban restores both halves.
	out = s.catlogctlRun(t, "unban", "-handle", "ban_me", "-admin", s.adminURL)
	if !strings.Contains(out, "credentials restored: 1") {
		t.Errorf("catlogctl unban output:\n%s", out)
	}

	restored := sh.ship(batch, func(o *shipOpts) { o.NoAdvace = true })
	if restored.Status != http.StatusOK {
		t.Fatalf("ingest after an unban = %d, body %v, want 200", restored.Status, restored.Body)
	}
	if res, _ := s.public(t, "/v1/players/ban_me"); res.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/players/ban_me after an unban = %d, want 200", res.StatusCode)
	}

	// A purge is the end of the line: the rows go, the tombstone stays, and the
	// handle can never be reclaimed.
	out = s.catlogctlRun(t, "purge", "-handle", "ban_me", "-yes", "-reason", "integration test", "-admin", s.adminURL)
	if !strings.Contains(out, "tombstone kept") {
		t.Errorf("catlogctl purge output:\n%s", out)
	}
	purged := sh.ship(batch, func(o *shipOpts) { o.NoAdvace = true })
	if purged.Status != http.StatusUnauthorized {
		t.Errorf("ingest after a purge = %d, body %v, want 401", purged.Status, purged.Body)
	}

	// Backup, while we have a populated database (§5.9).
	dest := t.TempDir()
	out = s.catlogctlRun(t, "backup", "-admin", s.adminURL, dest)
	if !strings.Contains(out, "events.db") {
		t.Errorf("catlogctl backup output:\n%s", out)
	}
	if fi, err := os.Stat(filepath.Join(dest, "events.db")); err != nil || fi.Size() == 0 {
		t.Errorf("backup did not write events.db: %v", err)
	}
}

// TestDeleteMyDataRetiresTheHandleForever is the §4.7 lifecycle a WP5 spec will
// drive through the UI.
func TestDeleteMyDataRetiresTheHandleForever(t *testing.T) {
	s, m := startIdentityStack(t)

	b := newBrowser(t, s)
	b.login(m, "discord", "login-as-whiskers-discord-old-account")

	key := testutil.ClientKey(t)
	jwk, err := cjws.PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if res, raw := b.postJSON("/api/handles", identity.ClaimRequest{Handle: "goodbye", JWK: jwk}); res.StatusCode != http.StatusOK {
		t.Fatalf("claim = %d: %s", res.StatusCode, raw)
	}

	res, raw := b.postJSON("/api/me/delete", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d: %s", res.StatusCode, raw)
	}

	// Logged out, and the profile is gone.
	if r, body := b.get(s.baseURL + "/api/me"); r.StatusCode == http.StatusOK {
		t.Errorf("the session survived delete-my-data: %s", body)
	}
	if r, _ := s.public(t, "/v1/players/goodbye"); r.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/players/goodbye = %d, want 404", r.StatusCode)
	}

	// A second account cannot take the handle (D9).
	other := newBrowser(t, s)
	other.login(m, "github", "login-as-clawdia-github")
	key2 := testutil.ClientKey(t)
	jwk2, err := cjws.PublicJWK(&key2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	res, raw = other.postJSON("/api/handles", identity.ClaimRequest{Handle: "GOODBYE", JWK: jwk2})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("reclaiming a retired handle = %d: %s", res.StatusCode, raw)
	}
	var e struct {
		Error string `json:"error"`
	}
	mustJSON(t, raw, &e)
	if e.Error != authz.CodeHandleTaken {
		t.Errorf("error = %q, want handle_taken", e.Error)
	}

	// And the deleted account cannot log back in: the tombstone is on the
	// deny-list, so a new session would only mint licenses ingest refuses.
	back := newBrowser(t, s)
	final, _ := back.login(m, "discord", "login-as-whiskers-discord-old-account")
	if final.StatusCode != authz.Status(authz.CodeBanned) {
		t.Errorf("a purged account logged back in: %d", final.StatusCode)
	}
	if back.sessionCookie() != nil {
		t.Error("a purged account was given a session")
	}
}

// TestRuntimeHandleIsVisibleWithoutARestart is the regression test for the bug
// WP7's simulator hit: a handle claimed against a *running* catlogd was
// invisible to the read API until a restart or a `/admin/seed`, because the
// in-memory handle directory (§5.4) is loaded at start and nothing reloaded it.
//
// Both creation paths are covered, because both were broken and both matter:
// `catlogctl issue` is what every simulator run and integration fixture uses,
// and `POST /api/handles` is what real players use.
func TestRuntimeHandleIsVisibleWithoutARestart(t *testing.T) {
	s, m := startIdentityStack(t)

	t.Run("catlogctl issue", func(t *testing.T) {
		// A handle nobody has heard of 404s, which is what makes the assertion
		// below meaningful rather than vacuous.
		if res, _ := s.public(t, "/v1/players/sim_ace"); res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /v1/players/sim_ace before issuing = %d, want 404", res.StatusCode)
		}

		cred := s.issue("sim_ace")

		// No restart, no seed, no rebuild — just the issue call.
		res, body := s.public(t, "/v1/players/sim_ace")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/players/sim_ace immediately after `catlogctl issue` = %d: %s\n"+
				"the read path never learned the handle (§5.4 directory reload)", res.StatusCode, body)
		}
		var player struct {
			Handle string `json:"handle"`
			Since  int64  `json:"since"`
		}
		mustJSON(t, body, &player)
		if player.Handle != "sim_ace" {
			t.Errorf("handle = %q, want sim_ace", player.Handle)
		}

		// And the events it ships fold onto a *visible* player: the symptom was
		// a player whose events folded perfectly and who appeared on no board.
		sh := newShipper(t, s, cred)
		if got := sh.ship(goldenBatch(t)); got.Status != http.StatusOK {
			t.Fatalf("ship = %d, body %v", got.Status, got.Body)
		}

		// A named board with a literal value, not "whatever the profile lists
		// first". `batch-001.ndjson` carries one `vehicle.impact` — survived,
		// 2 crew, not on the launch pad, 214.5 m/s on duna — which is §5.6's
		// own worked example of `biggest_lithobrake_survived`. Asserting the
		// number means this test fails loudly if the batch, the fold or the
		// board rule changes, instead of quietly asserting something else.
		stats := waitForPlayerStats(t, s, "sim_ace", lithobrakeStat)
		if got := stats[lithobrakeStat]; got != lithobrakeValue {
			t.Errorf("%s = %v, want %v (the golden batch's impact)", lithobrakeStat, got, lithobrakeValue)
		}

		// The leaderboard filter has to agree: `readapi.visibleRows` drops any
		// row whose player_id the directory cannot name, which is the other
		// half of the bug and the half a profile lookup would not catch.
		waitForBoard(t, s, lithobrakeStat, "sim_ace", lithobrakeValue)

		// Stronger, and the invariant worth pinning for its own sake: every
		// stat the profile reports must also place the player on that board.
		// A profile that named a board the player is absent from would be a
		// read-API inconsistency users would hit, not just tests.
		for stat := range stats {
			waitForBoard(t, s, stat, "sim_ace", stats[stat])
		}
	})

	t.Run("POST /api/handles", func(t *testing.T) {
		b := newBrowser(t, s)
		b.login(m, "discord", "login-as-whiskers-discord-old-account")

		key := testutil.ClientKey(t)
		jwk, err := cjws.PublicJWK(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if res, raw := b.postJSON("/api/handles", identity.ClaimRequest{Handle: "dash_cat", JWK: jwk}); res.StatusCode != http.StatusOK {
			t.Fatalf("claim = %d: %s", res.StatusCode, raw)
		}
		if res, body := s.public(t, "/v1/players/dash_cat"); res.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/players/dash_cat immediately after the claim = %d: %s", res.StatusCode, body)
		}
	})
}

// What `contracts/testdata/batches/batch-001.ndjson` deterministically produces
// (§4.10 fixes the batch; §5.6 fixes the fold), so the assertions above name a
// value rather than discovering one.
const (
	lithobrakeStat  = "biggest_lithobrake_survived"
	lithobrakeValue = 214.5
)

// projectorWait bounds how long a fold may take before the test calls it a
// failure. The projector wakes on the writer's notify channel, so this is
// slack, not a delay anyone waits out.
const projectorWait = 15 * time.Second

// waitForPlayerStats polls a profile until the named stat has been folded onto
// it, and returns every stat the player holds as stat → value.
//
// Waiting for a *named* stat is what makes the caller deterministic: waiting
// for "any stat" would return whichever board happened to be folded and sorted
// first, and would silently start asserting something else the day the batch or
// the fold registry changes.
func waitForPlayerStats(t *testing.T, s *server, handle, want string) map[string]float64 {
	t.Helper()
	deadline := time.Now().Add(projectorWait)
	var last string
	for time.Now().Before(deadline) {
		res, body := s.public(t, "/v1/players/"+handle)
		if res.StatusCode == http.StatusOK {
			var player struct {
				Stats []struct {
					Stat  string  `json:"stat"`
					Value float64 `json:"value"`
				} `json:"stats"`
			}
			mustJSON(t, body, &player)
			out := make(map[string]float64, len(player.Stats))
			for _, st := range player.Stats {
				out[st.Stat] = st.Value
			}
			if _, ok := out[want]; ok {
				return out
			}
			last = string(body)
		} else {
			last = fmt.Sprintf("HTTP %d: %s", res.StatusCode, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never appeared on %s's profile\nlast profile: %s", want, handle, last)
	return nil
}

// waitForBoard polls a leaderboard until the handle appears on it with the
// expected value, which is what proves the fold and the directory agree about
// who this player is.
func waitForBoard(t *testing.T, s *server, stat, handle string, want float64) {
	t.Helper()
	deadline := time.Now().Add(projectorWait)
	var last string
	for time.Now().Before(deadline) {
		res, body := s.public(t, "/v1/leaderboards/"+stat)
		if res.StatusCode == http.StatusOK {
			var board struct {
				Rows []struct {
					Handle string  `json:"handle"`
					Value  float64 `json:"value"`
				} `json:"rows"`
			}
			mustJSON(t, body, &board)
			for _, row := range board.Rows {
				if row.Handle != handle {
					continue
				}
				if row.Value != want {
					t.Fatalf("%s is on the %s board at %v, but their profile says %v",
						handle, stat, row.Value, want)
				}
				return
			}
			last = string(body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never appeared on the %s board; the events folded but the read path cannot name the player\nlast board: %s",
		handle, stat, last)
}

// TestRevokeWithoutABanIsLicenseRevoked pins which §4.9 code the chain actually
// produces. §4.5.3 step 4 checks `sub` before `jkt`, so a ban — which does both
// — surfaces as `banned`; revoking a credential on its own leaves the sub clean
// and surfaces as `license_revoked`. Both are asserted so neither can silently
// become the other.
func TestRevokeWithoutABanIsLicenseRevoked(t *testing.T) {
	s, m := startIdentityStack(t, "CATLOG_LIMITS_RATELIMIT_BURST=100")

	b := newBrowser(t, s)
	b.login(m, "discord", "login-as-whiskers-discord-old-account")

	key := testutil.ClientKey(t)
	jwk, err := cjws.PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	res, raw := b.postJSON("/api/handles", identity.ClaimRequest{Handle: "revoke_me", JWK: jwk})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("claim = %d: %s", res.StatusCode, raw)
	}
	var issued identity.LicenseResponse
	mustJSON(t, raw, &issued)

	pem, err := cjws.MarshalPrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	sh := newShipper(t, s, credential{
		Format: 1, Handle: issued.Handle, License: issued.License, PrivateKeyPEM: pem,
		key: key, jkt: issued.JKT,
	})
	batch := goldenBatch(t)
	if got := sh.ship(batch); got.Status != http.StatusOK {
		t.Fatalf("ship before the revoke = %d, body %v", got.Status, got.Body)
	}

	// Revoke from the dashboard: the credential dies, the account does not.
	res, raw = b.postJSON("/api/handles/revoke_me/revoke", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d: %s", res.StatusCode, raw)
	}

	got := sh.ship(batch, func(o *shipOpts) { o.NoAdvace = true })
	if got.Status != http.StatusUnauthorized {
		t.Fatalf("ingest after a revoke = %d, body %v, want 401", got.Status, got.Body)
	}
	if got.Body["error"] != authz.CodeLicenseRevoked {
		t.Errorf("error = %v, want %s (the sub is clean; only the jkt is on the deny-list)",
			got.Body["error"], authz.CodeLicenseRevoked)
	}

	// The account itself is untouched: the handle is still theirs, still
	// public, and the dashboard still works (D9 — revoking is not banning).
	if r, body := s.public(t, "/v1/players/revoke_me"); r.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/players/revoke_me after a revoke = %d: %s", r.StatusCode, body)
	}
	if r, body := b.get(s.baseURL + "/api/me"); r.StatusCode != http.StatusOK {
		t.Errorf("the dashboard stopped working after a revoke: %d %s", r.StatusCode, body)
	}
}

// --- helpers -------------------------------------------------------------------

// rawGet fetches a public path without asserting a content type — the
// deny-list is a compact JWS, not JSON.
func (s *server) rawGet(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Get(s.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return res, body
}

// catlogctlRun runs a catlogctl verb and returns its combined output.
func (s *server) catlogctlRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(s.catlogctl, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("catlogctl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func decodeLicense(t *testing.T, license string) authz.LicenseClaims {
	t.Helper()
	_, payload, err := cjws.ParseCompactUnverified(license)
	if err != nil {
		t.Fatalf("parse license: %v", err)
	}
	var claims authz.LicenseClaims
	mustJSON(t, payload, &claims)
	return claims
}

// verifyAgainstJWKS checks a compact JWS verifies under one of the published
// keys — what a second node pulling the deny-list would do (§5.8).
func verifyAgainstJWKS(t *testing.T, jwksBody []byte, compact string) {
	t.Helper()
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	mustJSON(t, jwksBody, &set)
	if len(set.Keys) == 0 {
		t.Fatal("the published JWKS is empty")
	}
	for _, raw := range set.Keys {
		pub, err := cjws.ParsePublicJWK(raw)
		if err != nil {
			continue
		}
		if _, err := cjws.VerifyES256(compact, pub); err == nil {
			return
		}
	}
	t.Fatal("the published deny-list does not verify against any published key")
}
