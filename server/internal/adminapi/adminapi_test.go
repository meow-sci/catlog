package adminapi

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	// Linked in for its side effect: the §5.9 ingest counters are registered by
	// package ingest's init, and catlogd links both packages. /debug/vars only
	// shows what the binary has actually registered.
	_ "github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func newServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	cfg := testutil.Config(t)
	events := testutil.Events(t)
	ks := testutil.KeysAt(t, cfg.Data.Dir)

	s := New(Deps{
		Config: cfg,
		Keys:   ks,
		Events: events,
		Log:    testutil.DiscardLogger(),
		Now:    func() time.Time { return time.Unix(1_770_000_000, 0).UTC() },
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func post(t *testing.T, srv *httptest.Server, path string, body any) (*http.Response, map[string]any) {
	t.Helper()

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Post(srv.URL+path, "application/json", &buf)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()

	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("response is not JSON (%d): %v", res.StatusCode, err)
	}
	return res, decoded
}

// TestIssueGeneratesAKeyAndAWorkingLicense is the §5.9 dev path end to end: no
// jwk in, a complete credential out, and the license verifies against the
// §4.5.3 chain.
func TestIssueGeneratesAKeyAndAWorkingLicense(t *testing.T) {
	s, srv := newServer(t)

	res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "whiskers_prime"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", res.StatusCode, body)
	}

	license, _ := body["license"].(string)
	jkt, _ := body["jkt"].(string)
	privPEM, _ := body["private_key_pem"].(string)
	if license == "" || jkt == "" {
		t.Fatalf("incomplete response: %v", body)
	}
	if !strings.HasPrefix(privPEM, "-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("private_key_pem is not PKCS#8 PEM: %q", truncate(privPEM))
	}
	if body["expires_at"].(float64) <= body["issued_at"].(float64) {
		t.Errorf("expires_at is not after issued_at: %v", body)
	}

	// §4.6: the credential file is only usable if the key's thumbprint is the
	// license cnf.jkt. Check that binding the way the mod's loader does.
	key := parsePEM(t, privPEM)
	gotJKT, err := cjws.ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if gotJKT != jkt {
		t.Fatalf("jkt = %q, but the returned private key thumbprints to %q", jkt, gotJKT)
	}

	// And the whole chain accepts it.
	v := authz.New(authz.Config{
		Issuer:      s.deps.Config.Server.BaseURL,
		AcceptedHTU: []string{s.deps.Config.IngestURL()},
	}, s.deps.Keys, s.deps.Events, authz.NewDenyList())
	now := s.deps.Now()
	v.SetClock(func() time.Time { return now })

	bodyBytes := []byte("a batch")
	proof := testutil.MintProof(t, key, map[string]any{
		"jti": ids.String(testutil.ULID(t)),
		"iat": now.Unix(),
		"htm": "POST",
		"htu": s.deps.Config.IngestURL(),
		"bh":  testutil.B64USHA256(bodyBytes),
		"sid": ids.String(testutil.ULID(t)),
		"seq": int64(1),
	})
	result, aerr := v.Verify(t.Context(), authz.Request{License: license, Proof: proof})
	if aerr != nil {
		t.Fatalf("the issued credential does not pass the chain: %v", aerr)
	}
	if result.Handle != "whiskers_prime" {
		t.Errorf("handle = %q", result.Handle)
	}
	if aerr := result.CheckBodyHash(bodyBytes); aerr != nil {
		t.Errorf("CheckBodyHash: %v", aerr)
	}
}

// TestIssueAcceptsAClientJWK is the path catlogctl and the dashboard use: the
// private key never reaches the server.
func TestIssueAcceptsAClientJWK(t *testing.T) {
	_, srv := newServer(t)

	key := testutil.ClientKey(t)
	jwk, err := cjws.PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "mittens", JWK: jwk})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", res.StatusCode, body)
	}
	if _, ok := body["private_key_pem"]; ok {
		t.Error("the server returned a private key for a client-supplied jwk")
	}

	want, err := cjws.ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if body["jkt"] != want {
		t.Errorf("jkt = %v, want %q", body["jkt"], want)
	}
}

// TestIssueRejections covers the ways the dev path says no.
func TestIssueRejections(t *testing.T) {
	t.Run("invalid handle", func(t *testing.T) {
		_, srv := newServer(t)
		res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "-nope-"})
		if res.StatusCode != http.StatusBadRequest || body["error"] != authz.CodeHandleInvalid {
			t.Fatalf("status %d body %v, want 400 %s", res.StatusCode, body, authz.CodeHandleInvalid)
		}
	})

	t.Run("a private jwk is refused", func(t *testing.T) {
		_, srv := newServer(t)
		// A JWK carrying "d" must never be accepted from a client (§13.6).
		res, body := post(t, srv, "/admin/issue", map[string]any{
			"handle": "sneaky",
			"jwk": map[string]any{
				"kty": "EC", "crv": "P-256",
				"x": "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU",
				"y": "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0",
				"d": "jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI",
			},
		})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, body %v", res.StatusCode, body)
		}
	})

	t.Run("someone else's handle", func(t *testing.T) {
		s, srv := newServer(t)
		// A real (non-dev) player already holds it, so the synthetic dev player
		// for that handle is a different account.
		other := testutil.Player(t, s.deps.Events, s.deps.Keys, "discord", "100000000000000000")
		if err := s.deps.Events.ClaimHandle(t.Context(), other, "taken_handle", 1); err != nil {
			t.Fatal(err)
		}
		res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "taken_handle"})
		if body["error"] != authz.CodeHandleTaken {
			t.Fatalf("status %d body %v, want %s", res.StatusCode, body, authz.CodeHandleTaken)
		}
	})

	t.Run("the same key twice", func(t *testing.T) {
		_, srv := newServer(t)
		key := testutil.ClientKey(t)
		jwk, err := cjws.PublicJWK(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "first", JWK: jwk}); res.StatusCode != http.StatusOK {
			t.Fatalf("first issue failed: %d %v", res.StatusCode, body)
		}
		res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "first", JWK: jwk})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d body %v, want 400", res.StatusCode, body)
		}
	})

	t.Run("unknown body field", func(t *testing.T) {
		_, srv := newServer(t)
		res, body := post(t, srv, "/admin/issue", map[string]any{"handle": "x", "extra": 1})
		if res.StatusCode != http.StatusBadRequest || body["error"] != authz.CodeBadRequest {
			t.Fatalf("status %d body %v, want 400 %s", res.StatusCode, body, authz.CodeBadRequest)
		}
	})

	t.Run("issue is POST only", func(t *testing.T) {
		_, srv := newServer(t)
		res, err := srv.Client().Get(srv.URL + "/admin/issue")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET /admin/issue = %d, want 405", res.StatusCode)
		}
	})
}

// TestReissueForTheSameHandle proves a dev handle can get a second credential
// with a new key — the ordinary "I lost my credential file" path.
func TestReissueForTheSameHandle(t *testing.T) {
	_, srv := newServer(t)

	res, first := post(t, srv, "/admin/issue", IssueRequest{Handle: "reissued"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first issue: %d %v", res.StatusCode, first)
	}
	res, second := post(t, srv, "/admin/issue", IssueRequest{Handle: "reissued"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second issue: %d %v", res.StatusCode, second)
	}
	if first["jkt"] == second["jkt"] {
		t.Error("the second issuance reused the first key")
	}
	if first["license"] == second["license"] {
		t.Error("the second issuance reused the first license")
	}
}

// TestAdminSurface checks the rest of the WP2 mux: expvar carries the §5.9
// ingest counters, and pprof is mounted.
func TestAdminSurface(t *testing.T) {
	_, srv := newServer(t)

	res, err := srv.Client().Get(srv.URL + "/debug/vars")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/debug/vars = %d", res.StatusCode)
	}
	var vars map[string]any
	if err := json.NewDecoder(res.Body).Decode(&vars); err != nil {
		t.Fatalf("expvar output is not JSON: %v", err)
	}
	for _, want := range []string{"ingest_accepted", "ingest_deduped", "ingest_rejected_malformed_batch", "ingest_rejected_rate_limited"} {
		if _, ok := vars[want]; !ok {
			t.Errorf("expvar %q is missing (§5.9)", want)
		}
	}

	pprofRes, err := srv.Client().Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	pprofRes.Body.Close()
	if pprofRes.StatusCode != http.StatusOK {
		t.Errorf("/debug/pprof/ = %d, want 200", pprofRes.StatusCode)
	}
}

// TestNonLoopbackIsRefused proves the guard that makes an unauthenticated admin
// mux defensible.
func TestNonLoopbackIsRefused(t *testing.T) {
	s, _ := newServer(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/issue", strings.NewReader(`{"handle":"x"}`))
	req.RemoteAddr = "203.0.113.7:51234"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// parsePEM is the §4.6 loader's key step, as a test helper.
func parsePEM(t *testing.T, pemText string) *ecdsa.PrivateKey {
	t.Helper()
	key, err := cjws.ParsePrivateKeyPEM([]byte(pemText))
	if err != nil {
		t.Fatalf("parse private key PEM: %v", err)
	}
	return key
}

func truncate(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "…"
}
