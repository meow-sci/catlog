package identity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// fixture is a whole identity server over a throwaway events.db.
type fixture struct {
	t      *testing.T
	server *Server
	mux    *http.ServeMux
	events *store.Events
	keys   *keys.Set
	deny   *authz.DenyList
	dir    *directory.Directory
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	// File-backed rather than :memory:, because the claim path runs a write
	// transaction while a read handle is open — the split only exists on a
	// real file (testutil package doc).
	events := testutil.Events(t)
	keySet := testutil.Keys(t)
	deny := authz.NewDenyList()
	dir := directory.New(events)
	if err := dir.Reload(t.Context()); err != nil {
		t.Fatalf("directory reload: %v", err)
	}

	cfg := testutil.Config(t)
	cfg.Server.BaseURL = "http://127.0.0.1:8080"
	cfg.Auth.HandleQuota = 5
	cfg.Auth.IssuancePerDay = 3
	cfg.Auth.MinAccountAgeDays = 30

	f := &fixture{
		t: t, events: events, keys: keySet, deny: deny, dir: dir,
		now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}

	s, err := New(Deps{
		Config: cfg, Keys: keySet, Events: events, Deny: deny, Directory: dir,
		Log: testutil.DiscardLogger(),
		Now: func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}
	f.server = s
	f.mux = http.NewServeMux()
	s.Register(f.mux)
	return f
}

// login creates a player and returns a request cookie holding a valid session.
func (f *fixture) login(idp, subject string) (*http.Cookie, keys.UserKey, int64) {
	f.t.Helper()
	uk := f.keys.UserKey(idp, subject)
	id, err := f.events.EnsurePlayer(context.Background(), nil, uk, idp, f.now.UnixMilli())
	if err != nil {
		f.t.Fatalf("ensure player: %v", err)
	}
	return &http.Cookie{
		Name:  f.server.sessions.CookieName(),
		Value: f.server.sessions.Encode(uk, f.now.Add(time.Hour)),
	}, uk, id
}

// do runs a request through the registered mux.
func (f *fixture) do(method, path string, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var r *http.Request
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, r)
	return rec
}

// publicJWK generates a client key pair and returns its public JWK plus the
// RFC 7638 thumbprint the license must bind to.
func publicJWK(t *testing.T) (json.RawMessage, string, *ecdsa.PrivateKey) {
	t.Helper()
	key := testutil.ClientKey(t)
	jwk, err := cjws.PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatalf("public jwk: %v", err)
	}
	jkt, err := cjws.ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	return jwk, jkt, key
}

// errorCode decodes a §4.9 error body.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("response %d is not a §4.9 error body: %s", rec.Code, rec.Body)
	}
	return e.Error
}

// --- session gate ---------------------------------------------------------------

// TestDashboardRequiresASession checks every §4.8 endpoint refuses an
// unauthenticated or forged caller.
func TestDashboardRequiresASession(t *testing.T) {
	f := newFixture(t)
	forged := &http.Cookie{Name: f.server.sessions.CookieName(), Value: "AAAA.9999999999.BBBB"}

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/me", nil},
		{http.MethodGet, "/api/handles", nil},
		{http.MethodPost, "/api/handles", ClaimRequest{Handle: "x"}},
		{http.MethodPost, "/api/handles/x/reissue", ReissueRequest{}},
		{http.MethodPost, "/api/handles/x/revoke", nil},
		{http.MethodPost, "/api/me/delete", nil},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			for name, cookie := range map[string]*http.Cookie{"no cookie": nil, "forged cookie": forged} {
				rec := f.do(tc.method, tc.path, cookie, tc.body)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s: status = %d, want 401", name, rec.Code)
				}
			}
		})
	}

	// Logout is deliberately not gated: it must work even when the cookie is
	// the problem.
	if rec := f.do(http.MethodPost, "/api/logout", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("POST /api/logout without a session = %d, want 200", rec.Code)
	}
}

// --- claim ------------------------------------------------------------------------

// TestClaimIssuesABoundLicense is the happy path of §4.8's `POST /api/handles`.
func TestClaimIssuesABoundLicense(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	jwk, jkt, _ := publicJWK(t)

	rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "Whiskers_Prime", JWK: jwk})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}
	var out LicenseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.JKT != jkt {
		t.Errorf("jkt = %s, want %s", out.JKT, jkt)
	}

	// The license must verify under the server key and carry the §4.5.1 claims.
	pub, ok := f.keys.SigningKeyByKID(f.keys.Signing.KID)
	if !ok {
		t.Fatal("the signing kid does not resolve")
	}
	payload, err := cjws.VerifyES256(out.License, pub)
	if err != nil {
		t.Fatalf("the issued license does not verify: %v", err)
	}
	var claims authz.LicenseClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("license claims: %v", err)
	}
	if claims.Subject != uk.B64U() {
		t.Errorf("license sub = %s, want the caller's user_key %s", claims.Subject, uk.B64U())
	}
	if claims.Handle != "Whiskers_Prime" {
		t.Errorf("license handle = %q, want the claimed casing", claims.Handle)
	}
	if claims.Cnf.JKT != jkt {
		t.Errorf("license cnf.jkt = %s, want %s", claims.Cnf.JKT, jkt)
	}
	if claims.Issuer != "http://127.0.0.1:8080" {
		t.Errorf("license iss = %q", claims.Issuer)
	}
	if got := claims.ExpiresAt - claims.IssuedAt; got != int64(180*24*3600) {
		t.Errorf("license ttl = %d s, want 180 days (D16)", got)
	}

	// The handle is now resolvable on the read side (§5.4).
	if _, ok := f.dir.Lookup("whiskers_prime"); !ok {
		t.Error("the claimed handle did not reach the directory")
	}
}

// TestClaimHandleRules covers the store-backed half of the §4.7 matrix:
// case-insensitive duplicates and retired handles.
func TestClaimHandleRules(t *testing.T) {
	f := newFixture(t)
	first, _, _ := f.login(IdPDiscord, "100000000000000000")
	second, _, _ := f.login(IdPGitHub, "4242")

	jwk, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", first, ClaimRequest{Handle: "Whiskers", JWK: jwk}); rec.Code != 200 {
		t.Fatalf("first claim = %d (%s)", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name     string
		cookie   *http.Cookie
		handle   string
		want     string
		wantHTTP int
	}{
		{"exact duplicate, another account", second, "Whiskers", authz.CodeHandleTaken, http.StatusConflict},
		{"case duplicate, another account", second, "WHISKERS", authz.CodeHandleTaken, http.StatusConflict},
		{"case duplicate, lowercase", second, "whiskers", authz.CodeHandleTaken, http.StatusConflict},
		{"duplicate by the same owner", first, "whiskers", authz.CodeHandleTaken, http.StatusConflict},
		{"151 characters", second, strings.Repeat("a", 151), authz.CodeHandleInvalid, http.StatusBadRequest},
		{"non-ascii", second, "whiskérs", authz.CodeHandleInvalid, http.StatusBadRequest},
		{"reserved", second, "admin", authz.CodeHandleReserved, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jwk, _, _ := publicJWK(t)
			rec := f.do(http.MethodPost, "/api/handles", tc.cookie, ClaimRequest{Handle: tc.handle, JWK: jwk})
			if got := errorCode(t, rec); got != tc.want {
				t.Errorf("error = %q, want %q (body %s)", got, tc.want, rec.Body)
			}
			if rec.Code != tc.wantHTTP {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantHTTP)
			}
		})
	}

	// A retired handle can never be claimed again (D9) — not by its former
	// owner, not by anyone.
	if err := f.events.RetireHandle(t.Context(), nil, "Whiskers", "test", f.now.UnixMilli()); err != nil {
		t.Fatalf("retire: %v", err)
	}
	jwk2, _, _ := publicJWK(t)
	rec := f.do(http.MethodPost, "/api/handles", second, ClaimRequest{Handle: "whiskers", JWK: jwk2})
	if got := errorCode(t, rec); got != authz.CodeHandleTaken {
		t.Errorf("retired handle: error = %q, want handle_taken", got)
	}
}

// TestClaimRefusesAPrivateKey is risk 6 of §13: only the public half may ever
// leave the browser.
func TestClaimRefusesAPrivateKey(t *testing.T) {
	f := newFixture(t)
	cookie, _, _ := f.login(IdPDiscord, "100000000000000000")

	key := testutil.ClientKey(t)
	// A full private JWK, exactly what exportKey("jwk", privateKey) produces.
	priv, err := json.Marshal(map[string]string{
		"kty": "EC", "crv": "P-256",
		"x": cjws.B64U(key.PublicKey.X.Bytes()),
		"y": cjws.B64U(key.PublicKey.Y.Bytes()),
		"d": cjws.B64U(key.D.Bytes()),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "leaky", JWK: priv})
	if got := errorCode(t, rec); got != authz.CodeBadRequest {
		t.Errorf("a private JWK was accepted (error %q, status %d): %s", got, rec.Code, rec.Body)
	}

	for _, bad := range []struct {
		name string
		jwk  string
	}{
		{"missing jwk", ``},
		{"not a jwk", `{"hello":"world"}`},
		{"wrong curve", `{"kty":"EC","crv":"P-384","x":"AA","y":"AA"}`},
		{"rsa", `{"kty":"RSA","n":"AA","e":"AQAB"}`},
	} {
		t.Run(bad.name, func(t *testing.T) {
			var raw json.RawMessage
			if bad.jwk != "" {
				raw = json.RawMessage(bad.jwk)
			}
			rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "leaky2", JWK: raw})
			if rec.Code == http.StatusOK {
				t.Errorf("%s was accepted", bad.name)
			}
		})
	}
}

// TestHandleQuota is §4.7's "≤ 5 live handles".
func TestHandleQuota(t *testing.T) {
	f := newFixture(t)
	cookie, _, _ := f.login(IdPDiscord, "100000000000000000")

	// The issuance quota would bite at 3, so raise it out of the way; this
	// test is about the handle quota alone.
	f.server.rules.IssuancesPerDay = 100

	for i := range 5 {
		jwk, _, _ := publicJWK(t)
		rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: fmt.Sprintf("cat%d", i), JWK: jwk})
		if rec.Code != http.StatusOK {
			t.Fatalf("claim %d = %d (%s)", i, rec.Code, rec.Body)
		}
	}

	jwk, _, _ := publicJWK(t)
	rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat5", JWK: jwk})
	if got := errorCode(t, rec); got != authz.CodeQuotaExceeded {
		t.Errorf("sixth handle: error = %q, want quota_exceeded", got)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
}

// TestIssuanceQuota is §4.7's "≤ 3 license issuances per 24 h, covering new and
// reissue" — including that the window rolls.
func TestIssuanceQuota(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")

	// Three issuances: two new handles and one reissue, which is exactly the
	// mix §4.7 says the quota covers.
	jwk1, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat0", JWK: jwk1}); rec.Code != 200 {
		t.Fatalf("claim 0 = %d (%s)", rec.Code, rec.Body)
	}
	jwk2, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat1", JWK: jwk2}); rec.Code != 200 {
		t.Fatalf("claim 1 = %d (%s)", rec.Code, rec.Body)
	}
	jwk3, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles/cat0/reissue", cookie, ReissueRequest{JWK: jwk3}); rec.Code != 200 {
		t.Fatalf("reissue = %d (%s)", rec.Code, rec.Body)
	}

	jwk4, _, _ := publicJWK(t)
	rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat2", JWK: jwk4})
	if got := errorCode(t, rec); got != authz.CodeQuotaExceeded {
		t.Errorf("fourth issuance: error = %q, want quota_exceeded (body %s)", got, rec.Body)
	}
	// A reissue is blocked by the same quota.
	jwk5, _, _ := publicJWK(t)
	rec = f.do(http.MethodPost, "/api/handles/cat1/reissue", cookie, ReissueRequest{JWK: jwk5})
	if got := errorCode(t, rec); got != authz.CodeQuotaExceeded {
		t.Errorf("fourth issuance via reissue: error = %q, want quota_exceeded", got)
	}

	// The window rolls: 24 h and one second later the quota is clear again. The
	// session has to be re-minted, because the clock moved past its 7-day
	// expiry — which is itself a small proof that the TTL is enforced.
	f.now = f.now.Add(24*time.Hour + time.Second)
	cookie = &http.Cookie{
		Name:  f.server.sessions.CookieName(),
		Value: f.server.sessions.Encode(uk, f.now.Add(time.Hour)),
	}
	jwk6, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat2", JWK: jwk6}); rec.Code != 200 {
		t.Errorf("after the window rolled: %d (%s)", rec.Code, rec.Body)
	}

	// /api/me reports the quotas so the wizard can grey out a button.
	var me MeResponse
	rec = f.do(http.MethodGet, "/api/me", cookie, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /api/me: %v", err)
	}
	if me.IssuanceQuota != 3 || me.HandleQuota != 5 {
		t.Errorf("/api/me quotas = %d/%d, want 3 issuances and 5 handles", me.IssuanceQuota, me.HandleQuota)
	}
	if me.Issuances24h != 1 {
		t.Errorf("/api/me issuances_24h = %d, want 1 (the earlier three fell out of the window)", me.Issuances24h)
	}
	if me.IdP != IdPDiscord {
		t.Errorf("/api/me idp = %q", me.IdP)
	}
}

// TestOneCredentialPerKey pins the §5.9 rule the dev path already enforces: a
// reissue means a new key pair.
func TestOneCredentialPerKey(t *testing.T) {
	f := newFixture(t)
	cookie, _, _ := f.login(IdPDiscord, "100000000000000000")
	jwk, _, _ := publicJWK(t)

	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat", JWK: jwk}); rec.Code != 200 {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}
	rec := f.do(http.MethodPost, "/api/handles/cat/reissue", cookie, ReissueRequest{JWK: jwk})
	if rec.Code == http.StatusOK {
		t.Error("the same key was given a second credential")
	}
}

// TestReissueRevokesThePrevious is D16: reissue is the deny-list touchpoint.
func TestReissueRevokesThePrevious(t *testing.T) {
	f := newFixture(t)
	cookie, _, playerID := f.login(IdPDiscord, "100000000000000000")

	jwk1, jkt1, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat", JWK: jwk1}); rec.Code != 200 {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}
	jwk2, jkt2, _ := publicJWK(t)
	rec := f.do(http.MethodPost, "/api/handles/CAT/reissue", cookie, ReissueRequest{JWK: jwk2})
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue = %d (%s)", rec.Code, rec.Body)
	}
	var out LicenseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The stored casing wins, whatever the URL said (§4.7 immutability).
	if out.Handle != "cat" {
		t.Errorf("reissued handle = %q, want the stored casing %q", out.Handle, "cat")
	}
	if out.JKT != jkt2 {
		t.Errorf("reissued jkt = %s, want %s", out.JKT, jkt2)
	}

	// Both halves, §5.8: the row and the in-memory set.
	old, err := f.events.CredentialByJKT(t.Context(), jkt1)
	if err != nil {
		t.Fatalf("read the old credential: %v", err)
	}
	if !old.Revoked() {
		t.Error("the replaced credential is not revoked in the database")
	}
	if !f.deny.HasJKT(jkt1) {
		t.Error("the replaced credential is not on the in-memory deny-list")
	}
	if f.deny.HasJKT(jkt2) {
		t.Error("the freshly issued credential is on the deny-list")
	}

	// And the dashboard shows exactly that.
	views, err := f.server.handleViews(t.Context(), playerID)
	if err != nil {
		t.Fatalf("handleViews: %v", err)
	}
	if len(views) != 1 || len(views[0].Credentials) != 2 {
		t.Fatalf("dashboard shows %d handles", len(views))
	}
	live := 0
	for _, c := range views[0].Credentials {
		if !c.Revoked {
			live++
		}
	}
	if live != 1 {
		t.Errorf("%d live credentials after a reissue, want 1", live)
	}
}

// TestReissueOnlyForOwnedHandles keeps a reissue from being a handle theft.
func TestReissueAndRevokeRequireOwnership(t *testing.T) {
	f := newFixture(t)
	owner, _, _ := f.login(IdPDiscord, "100000000000000000")
	other, _, _ := f.login(IdPGitHub, "4242")

	jwk, _, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles", owner, ClaimRequest{Handle: "cat", JWK: jwk}); rec.Code != 200 {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}

	jwk2, _, _ := publicJWK(t)
	for _, tc := range []struct{ name, method, path string }{
		{"reissue someone else's handle", http.MethodPost, "/api/handles/cat/reissue"},
		{"reissue a handle nobody holds", http.MethodPost, "/api/handles/nobody/reissue"},
	} {
		rec := f.do(tc.method, tc.path, other, ReissueRequest{JWK: jwk2})
		if got := errorCode(t, rec); got != authz.CodeNotFound {
			t.Errorf("%s: error = %q, want not_found", tc.name, got)
		}
	}
	rec := f.do(http.MethodPost, "/api/handles/cat/revoke", other, nil)
	if got := errorCode(t, rec); got != authz.CodeNotFound {
		t.Errorf("revoking someone else's handle: error = %q, want not_found", got)
	}
}

// TestRevokeKeepsTheHandle is D9: revocation stops shipping, it does not
// release the name.
func TestRevokeKeepsTheHandle(t *testing.T) {
	f := newFixture(t)
	cookie, _, _ := f.login(IdPDiscord, "100000000000000000")
	jwk, jkt, _ := publicJWK(t)

	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat", JWK: jwk}); rec.Code != 200 {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}
	rec := f.do(http.MethodPost, "/api/handles/cat/revoke", cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d (%s)", rec.Code, rec.Body)
	}
	var out RevokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Revoked) != 1 || out.Revoked[0] != jkt {
		t.Errorf("revoked = %v, want [%s]", out.Revoked, jkt)
	}
	if !f.deny.HasJKT(jkt) {
		t.Error("the revoked jkt did not reach the in-memory deny-list")
	}
	if _, err := f.events.HandleByLC(t.Context(), "cat"); err != nil {
		t.Errorf("the handle went away with the credential: %v", err)
	}

	// Idempotent: a second revoke finds nothing left to do.
	rec = f.do(http.MethodPost, "/api/handles/cat/revoke", cookie, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Revoked) != 0 {
		t.Errorf("second revoke reported %v", out.Revoked)
	}
}

// TestDeleteMyData is §4.8's `POST /api/me/delete`: everything goes, the handle
// is retired forever, the session is cleared and the account cannot come back.
func TestDeleteMyData(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	jwk, jkt, _ := publicJWK(t)

	if rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: "cat", JWK: jwk}); rec.Code != 200 {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body)
	}

	rec := f.do(http.MethodPost, "/api/me/delete", cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d (%s)", rec.Code, rec.Body)
	}
	var res PurgeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Deleted.Handles != 1 || res.Deleted.Credentials != 1 {
		t.Errorf("deleted %+v, want one handle and one credential", res.Deleted)
	}

	// The session cookie is cleared in the response.
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == f.server.sessions.CookieName() && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("delete-my-data did not clear the session cookie")
	}

	// The rows are gone and the tombstone remains.
	if _, err := f.events.PlayerByUserKey(t.Context(), uk); err == nil {
		t.Error("the player row survived the delete")
	}
	tombs, err := f.events.Tombstones(t.Context())
	if err != nil || len(tombs) != 1 || tombs[0].UserKey != uk {
		t.Errorf("tombstones = %v (err %v), want exactly this account", tombs, err)
	}

	// Deny-list, both halves: the sub is banned and the credential revoked.
	if !f.deny.HasSub(uk.B64U()) {
		t.Error("the purged sub is not on the deny-list")
	}
	if !f.deny.HasJKT(jkt) {
		t.Error("the purged credential is not on the deny-list")
	}

	// The handle is retired forever — a second account cannot take it (D9).
	other, _, _ := f.login(IdPGitHub, "4242")
	jwk2, _, _ := publicJWK(t)
	rec = f.do(http.MethodPost, "/api/handles", other, ClaimRequest{Handle: "CAT", JWK: jwk2})
	if got := errorCode(t, rec); got != authz.CodeHandleTaken {
		t.Errorf("a deleted account's handle was reclaimable: error = %q", got)
	}

	// And the original session no longer authenticates anything.
	if rec := f.do(http.MethodGet, "/api/me", cookie, nil); rec.Code == http.StatusOK {
		t.Error("the deleted account's session still works")
	}
}

// --- CSRF (§4.5.4) ------------------------------------------------------------------

// TestMutatingRoutesAreCSRFProtected checks the stdlib protection is actually
// wrapped around the POSTs, and that the safe methods are untouched.
func TestMutatingRoutesAreCSRFProtected(t *testing.T) {
	f := newFixture(t)
	cookie, _, _ := f.login(IdPDiscord, "100000000000000000")

	post := func(site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.AddCookie(cookie)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, r)
		return rec
	}

	if rec := post("same-origin"); rec.Code != http.StatusOK {
		t.Errorf("same-origin POST = %d, want 200", rec.Code)
	}
	if rec := post("none"); rec.Code != http.StatusOK {
		t.Errorf("Sec-Fetch-Site: none POST = %d, want 200", rec.Code)
	}
	rec := post("cross-site")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
	if got := errorCode(t, rec); got != authz.CodeBadRequest {
		t.Errorf("CSRF rejection body = %q, want the §4.9 shape", got)
	}

	// `same-site` is rejected too — a sibling subdomain needs AddTrustedOrigin
	// (see docs/DECISIONS.md).
	if rec := post("same-site"); rec.Code != http.StatusForbidden {
		t.Errorf("Sec-Fetch-Site: same-site POST = %d, want 403", rec.Code)
	}
	// The configured base URL is trusted explicitly, which is what makes a
	// two-subdomain deployment work.
	r := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.AddCookie(cookie)
	trusted := httptest.NewRecorder()
	f.mux.ServeHTTP(trusted, r)
	if trusted.Code != http.StatusOK {
		t.Errorf("a request from the configured base_url = %d, want 200", trusted.Code)
	}

	// A safe method is never blocked (§4.8's GETs change nothing).
	r = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(cookie)
	safe := httptest.NewRecorder()
	f.mux.ServeHTTP(safe, r)
	if safe.Code != http.StatusOK {
		t.Errorf("cross-site GET /api/me = %d, want 200", safe.Code)
	}
}

// --- well-known documents (§4.8, §5.8) ------------------------------------------------

func TestWellKnownDocuments(t *testing.T) {
	f := newFixture(t)

	rec := f.do(http.MethodGet, JWKSPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks = %d", rec.Code)
	}
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("jwks is not JSON: %v", err)
	}
	if len(set.Keys) == 0 || set.Keys[0]["kid"] != f.keys.Signing.KID {
		t.Errorf("jwks = %v, want the active kid %s", set.Keys, f.keys.Signing.KID)
	}
	// Never a private key.
	if _, leaked := set.Keys[0]["d"]; leaked {
		t.Fatal("the published JWKS contains a private key")
	}

	rec = f.do(http.MethodGet, DenyListPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("denylist = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/jose" {
		t.Errorf("denylist Content-Type = %q, want application/jose", ct)
	}
	// It is a JWS signed by the license signing key (§5.8).
	pub, _ := f.keys.SigningKeyByKID(f.keys.Signing.KID)
	payload, err := cjws.VerifyES256(strings.TrimSpace(rec.Body.String()), pub)
	if err != nil {
		t.Fatalf("the published deny-list does not verify: %v", err)
	}
	var doc DenyListDocument
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("deny-list payload: %v", err)
	}
	if doc.BannedSubs == nil || doc.RevokedJKTs == nil {
		t.Error("an empty deny-list must publish empty arrays, not nulls")
	}

	// A ban regenerates it, with a higher version.
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	_ = cookie
	player, err := f.events.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	if _, err := f.server.Moderator().Ban(t.Context(), player, "testing"); err != nil {
		t.Fatalf("ban: %v", err)
	}

	rec = f.do(http.MethodGet, DenyListPath, nil, nil)
	payload, err = cjws.VerifyES256(strings.TrimSpace(rec.Body.String()), pub)
	if err != nil {
		t.Fatalf("republished deny-list does not verify: %v", err)
	}
	var after DenyListDocument
	if err := json.Unmarshal(payload, &after); err != nil {
		t.Fatalf("deny-list payload: %v", err)
	}
	if after.Ver <= doc.Ver {
		t.Errorf("deny-list ver = %d, want > %d after a ban", after.Ver, doc.Ver)
	}
	if len(after.BannedSubs) != 1 || after.BannedSubs[0] != uk.B64U() {
		t.Errorf("banned_subs = %v, want [%s]", after.BannedSubs, uk.B64U())
	}
}
