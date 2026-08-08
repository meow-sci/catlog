package authz

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

const (
	testIssuer = "http://127.0.0.1:8080"
	testHTU    = "http://127.0.0.1:8080/v1/ingest"
)

// fixture is one verifier wired to a real events.db, a real key set and a
// working credential — the state every chain test starts from.
type fixture struct {
	v      *Verifier
	events *store.Events
	keys   *keys.Set
	cred   testutil.Cred
	now    time.Time
	body   []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	e := testutil.MemEvents(t)
	ks := testutil.Keys(t)
	now := time.Unix(1_770_000_000, 0).UTC()

	cred := testutil.CredentialAt(t, e, ks, testIssuer, "whiskers_prime", now.Add(-time.Hour), 180*24*time.Hour)

	v := New(Config{
		Issuer:        testIssuer,
		AcceptedHTU:   []string{testHTU},
		RatePerSecond: 0.5,
		Burst:         5,
	}, ks, e, NewDenyList())
	v.SetClock(func() time.Time { return now })

	return &fixture{v: v, events: e, keys: ks, cred: cred, now: now, body: []byte("brotli-body-bytes")}
}

// proof builds a valid proof and lets a case mutate the claims first.
func (f *fixture) proof(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{
		"jti": ids.String(testutil.ULID(t)),
		"iat": f.now.Unix(),
		"htm": "POST",
		"htu": testHTU,
		"bh":  testutil.B64USHA256(f.body),
		"sid": ids.String(testutil.ULID(t)),
		"seq": int64(1),
	}
	if mutate != nil {
		mutate(claims)
	}
	return testutil.MintProof(t, f.cred.Key, claims)
}

// license builds a license with the fixture's claims, mutated by the case.
func (f *fixture) license(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{
		"iss":    testIssuer,
		"sub":    f.cred.UserKey.B64U(),
		"handle": f.cred.Handle,
		"cnf":    map[string]any{"jkt": f.cred.JKT},
		"iat":    f.now.Add(-time.Hour).Unix(),
		"exp":    f.now.Add(180 * 24 * time.Hour).Unix(),
		"jti":    "lic_" + ids.String(testutil.ULID(t)),
		"ver":    1,
	}
	if mutate != nil {
		mutate(claims)
	}
	return testutil.MintLicense(t, f.keys, claims)
}

// TestVerifyHappyPath is the baseline every rejection case is a mutation of.
func TestVerifyHappyPath(t *testing.T) {
	f := newFixture(t)

	res, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)})
	if aerr != nil {
		t.Fatalf("Verify: %v", aerr)
	}
	if res.PlayerID != f.cred.PlayerID {
		t.Errorf("player = %d, want %d", res.PlayerID, f.cred.PlayerID)
	}
	if res.JKT != f.cred.JKT {
		t.Errorf("jkt = %q, want %q", res.JKT, f.cred.JKT)
	}
	if res.Handle != f.cred.Handle {
		t.Errorf("handle = %q, want %q", res.Handle, f.cred.Handle)
	}
	if res.Seq() != 1 {
		t.Errorf("seq = %d, want 1", res.Seq())
	}
	if res.SID() == ids.Zero {
		t.Error("sid did not parse")
	}
	if aerr := res.CheckBodyHash(f.body); aerr != nil {
		t.Errorf("CheckBodyHash: %v", aerr)
	}
	if aerr := res.CheckBodyHash([]byte("other")); aerr == nil {
		t.Error("CheckBodyHash accepted the wrong body")
	} else if aerr.Code != CodeProofInvalid || aerr.Step != 10 {
		t.Errorf("wrong-body rejection = %s at step %d, want %s at step 10", aerr.Code, aerr.Step, CodeProofInvalid)
	}
}

// TestVerifyChainPerStep walks §4.5.3 step by step: every case breaks exactly
// one thing, and asserts both the §4.9 code and the step that must produce it.
func TestVerifyChainPerStep(t *testing.T) {
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		step  int
		code  string
		build func(t *testing.T, f *fixture) Request
	}{{
		name: "step 1: license header missing",
		step: 1, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: "", Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 1: proof header missing",
		step: 1, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: ""}
		},
	}, {
		name: "step 1: license over 4 KiB",
		step: 1, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License + strings.Repeat("A", cjws.MaxCompactBytes), Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 1: proof is not compact",
		step: 1, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: "not.a.valid.jws"}
		},
	}, {
		name: "step 2: license alg is not ES256",
		step: 2, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: reheader(t, f.cred.License, map[string]any{
				"alg": "HS256", "kid": f.keys.Signing.KID, "typ": LicenseType,
			}), Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 2: unknown kid",
		step: 2, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: testutil.MintLicenseWithKey(t, f.keys.Signing.Key, "catlog-199001", licenseClaimsMap(t, f, nil)),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 2: signed by a foreign key",
		step: 2, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: testutil.MintLicenseWithKey(t, otherKey, f.keys.Signing.KID, licenseClaimsMap(t, f, nil)),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 3: wrong issuer",
		step: 3, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: f.license(t, func(c map[string]any) { c["iss"] = "http://evil.example" }),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 3: expired",
		step: 3, code: CodeLicenseExpired,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: f.license(t, func(c map[string]any) { c["exp"] = f.now.Add(-time.Second).Unix() }),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 3: unknown ver",
		step: 3, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: f.license(t, func(c map[string]any) { c["ver"] = 2 }),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 4: sub on the deny-list",
		step: 4, code: CodeBanned,
		build: func(t *testing.T, f *fixture) Request {
			f.v.DenyList().AddSub(f.cred.UserKey.B64U())
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 4: jkt on the deny-list",
		step: 4, code: CodeLicenseRevoked,
		build: func(t *testing.T, f *fixture) Request {
			f.v.DenyList().AddJKT(f.cred.JKT)
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 5: no credential row",
		step: 5, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			// A license for a key the server never issued against: structurally
			// perfect, signed by us, but there is no credential row.
			jkt, err := cjws.ThumbprintPublicKey(&otherKey.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			lic := f.license(t, func(c map[string]any) { c["cnf"] = map[string]any{"jkt": jkt} })
			claims := map[string]any{
				"jti": ids.String(testutil.ULID(t)), "iat": f.now.Unix(), "htm": "POST", "htu": testHTU,
				"bh": testutil.B64USHA256(f.body), "sid": ids.String(testutil.ULID(t)), "seq": int64(1),
			}
			return Request{License: lic, Proof: testutil.MintProof(t, otherKey, claims)}
		},
	}, {
		name: "step 5: credential revoked in the database",
		step: 5, code: CodeLicenseRevoked,
		build: func(t *testing.T, f *fixture) Request {
			if err := f.events.RevokeCredential(context.Background(), nil, f.cred.JKT, f.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 5: player banned in the database",
		step: 5, code: CodeBanned,
		build: func(t *testing.T, f *fixture) Request {
			if err := f.events.SetBan(context.Background(), nil, f.cred.PlayerID, f.now.UnixMilli(), "test"); err != nil {
				t.Fatal(err)
			}
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}, {
		name: "step 5: license handle does not match the credential",
		step: 5, code: CodeLicenseInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: f.license(t, func(c map[string]any) { c["handle"] = "someone_else" }),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name: "step 6: proof key is not the licensed key",
		step: 6, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			claims := map[string]any{
				"jti": ids.String(testutil.ULID(t)), "iat": f.now.Unix(), "htm": "POST", "htu": testHTU,
				"bh": testutil.B64USHA256(f.body), "sid": ids.String(testutil.ULID(t)), "seq": int64(1),
			}
			return Request{License: f.cred.License, Proof: testutil.MintProof(t, otherKey, claims)}
		},
	}, {
		name: "step 6: proof has no embedded jwk",
		step: 6, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			p := f.proof(t, nil)
			return Request{License: f.cred.License, Proof: reheader(t, p, map[string]any{"alg": "ES256", "typ": ProofType})}
		},
	}, {
		name: "step 7: proof signature does not verify",
		step: 7, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			p := f.proof(t, nil)
			// Swap the payload for a different one, keeping header+signature:
			// the embedded key still thumbprints correctly (step 6 passes), the
			// signature no longer covers the payload.
			parts := strings.Split(p, ".")
			parts[1] = cjws.B64U([]byte(`{"jti":"tampered"}`))
			return Request{License: f.cred.License, Proof: strings.Join(parts, ".")}
		},
	}, {
		name: "step 8: wrong htm",
		step: 8, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) { c["htm"] = "PUT" })}
		},
	}, {
		name: "step 8: htu is not an accepted ingest URL",
		step: 8, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) {
				c["htu"] = "http://127.0.0.1:8080/v1/ingest/" // trailing slash: no normalization (§4.5.2)
			})}
		},
	}, {
		name: "step 8: iat too far in the past",
		step: 8, code: CodeClockSkew,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) {
				c["iat"] = f.now.Add(-301 * time.Second).Unix()
			})}
		},
	}, {
		name: "step 8: iat too far in the future",
		step: 8, code: CodeClockSkew,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) {
				c["iat"] = f.now.Add(301 * time.Second).Unix()
			})}
		},
	}, {
		name: "step 8: seq below 1",
		step: 8, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) { c["seq"] = 0 })}
		},
	}, {
		name: "step 8: bh is not a SHA-256",
		step: 8, code: CodeProofInvalid,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) { c["bh"] = "short" })}
		},
	}, {
		name: "step 9: token bucket exhausted",
		step: 9, code: CodeRateLimited,
		build: func(t *testing.T, f *fixture) Request {
			// Burst is 5 and the clock never advances, so the sixth attempt has
			// no token left.
			for range 5 {
				if _, aerr := f.v.Verify(context.Background(), Request{License: f.cred.License, Proof: f.proof(t, nil)}); aerr != nil {
					t.Fatalf("priming the bucket failed: %v", aerr)
				}
			}
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			req := tc.build(t, f)

			res, aerr := f.v.Verify(t.Context(), req)
			if aerr == nil {
				t.Fatalf("Verify accepted %+v", res)
			}
			if aerr.Code != tc.code {
				t.Errorf("code = %q, want %q (detail: %s)", aerr.Code, tc.code, aerr.Detail)
			}
			if aerr.Step != tc.step {
				t.Errorf("step = %d, want %d (detail: %s)", aerr.Step, tc.step, aerr.Detail)
			}
			if want := Status(tc.code); aerr.Status() != want {
				t.Errorf("status = %d, want %d", aerr.Status(), want)
			}
		})
	}
}

// TestCheapChecksRunBeforeSignatureVerification is the DoS-resistance
// assertion. The §4.5.3 order exists so that an attacker cannot make the server
// do public-key work for free: a request that fails at step 1 must cost zero
// ECDSA verifications, one that fails before step 7 must cost at most the one
// license verification, and only a fully-authenticated request pays for two.
func TestCheapChecksRunBeforeSignatureVerification(t *testing.T) {
	cases := []struct {
		name             string
		wantLicenseVerif uint64
		wantProofVerif   uint64
		build            func(t *testing.T, f *fixture) Request
	}{{
		name: "step 1 costs nothing",
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: "garbage", Proof: "garbage"}
		},
	}, {
		name: "an unknown kid costs nothing", // the key lookup precedes the verify
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: testutil.MintLicenseWithKey(t, f.keys.Signing.Key, "catlog-199001", licenseClaimsMap(t, f, nil)),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name:             "an expired license costs one verification, never the proof",
		wantLicenseVerif: 1,
		build: func(t *testing.T, f *fixture) Request {
			return Request{
				License: f.license(t, func(c map[string]any) { c["exp"] = f.now.Add(-time.Second).Unix() }),
				Proof:   f.proof(t, nil),
			}
		},
	}, {
		name:             "a banned sub costs one verification, never the proof",
		wantLicenseVerif: 1,
		build: func(t *testing.T, f *fixture) Request {
			f.v.DenyList().AddSub(f.cred.UserKey.B64U())
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}, {
		name:             "a mismatched proof key costs one verification, never the proof",
		wantLicenseVerif: 1,
		build: func(t *testing.T, f *fixture) Request {
			other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			claims := map[string]any{
				"jti": ids.String(testutil.ULID(t)), "iat": f.now.Unix(), "htm": "POST", "htu": testHTU,
				"bh": testutil.B64USHA256(f.body), "sid": ids.String(testutil.ULID(t)), "seq": int64(1),
			}
			return Request{License: f.cred.License, Proof: testutil.MintProof(t, other, claims)}
		},
	}, {
		name:             "a valid request pays for both",
		wantLicenseVerif: 1,
		wantProofVerif:   1,
		build: func(t *testing.T, f *fixture) Request {
			return Request{License: f.cred.License, Proof: f.proof(t, nil)}
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			req := tc.build(t, f)
			before := f.v.Stats()

			_, _ = f.v.Verify(t.Context(), req)

			got := f.v.Stats()
			if n := got.LicenseVerifies - before.LicenseVerifies; n != tc.wantLicenseVerif {
				t.Errorf("license ECDSA verifications = %d, want %d", n, tc.wantLicenseVerif)
			}
			if n := got.ProofVerifies - before.ProofVerifies; n != tc.wantProofVerif {
				t.Errorf("proof ECDSA verifications = %d, want %d", n, tc.wantProofVerif)
			}
		})
	}
}

// TestLicenseCacheSkipsTheSecondVerification pins §4.5.3 step 2's LRU: the same
// license string verifies once, then rides the cache — while the time-dependent
// checks keep running on every request.
func TestLicenseCacheSkipsTheSecondVerification(t *testing.T) {
	f := newFixture(t)

	for i := range 4 {
		if _, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)}); aerr != nil {
			t.Fatalf("request %d: %v", i, aerr)
		}
	}

	stats := f.v.Stats()
	if stats.LicenseVerifies != 1 {
		t.Errorf("license verifications = %d, want 1 (the rest must hit the cache)", stats.LicenseVerifies)
	}
	if stats.LicenseCacheHits != 3 {
		t.Errorf("cache hits = %d, want 3", stats.LicenseCacheHits)
	}
	if stats.ProofVerifies != 4 {
		t.Errorf("proof verifications = %d, want 4 (proofs are never cached)", stats.ProofVerifies)
	}

	// Cached or not, a revocation takes effect on the very next request.
	f.v.DenyList().AddJKT(f.cred.JKT)
	_, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)})
	if aerr == nil || aerr.Code != CodeLicenseRevoked {
		t.Fatalf("after revocation: %v, want %s", aerr, CodeLicenseRevoked)
	}

	// And expiry is evaluated per request, not cached with the claims.
	f.v.SetClock(func() time.Time { return f.cred.ExpiresAt.Add(time.Second) })
	f.v.DenyList().Replace(nil, nil)
	_, aerr = f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, func(c map[string]any) {
		c["iat"] = f.cred.ExpiresAt.Add(time.Second).Unix()
	})})
	if aerr == nil || aerr.Code != CodeLicenseExpired {
		t.Fatalf("after expiry: %v, want %s", aerr, CodeLicenseExpired)
	}
}

// TestCredentialCache pins the step-5 memoization: a repeat batch skips the two
// point queries, any deny-list mutation drops the cache immediately, and the
// TTL catches a row change that never touched the deny-list.
func TestCredentialCache(t *testing.T) {
	f := newFixture(t)
	verify := func() *Error {
		_, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)})
		return aerr
	}

	if aerr := verify(); aerr != nil {
		t.Fatalf("warm-up: %v", aerr)
	}

	// Revoke the row directly, bypassing the moderation paths — the drift the
	// TTL exists for. The cached success still answers: nothing bumped the
	// deny-list and the entry is fresh.
	if err := f.events.RevokeCredential(t.Context(), nil, f.cred.JKT, f.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if aerr := verify(); aerr != nil {
		t.Fatalf("within the TTL with no deny-list change: %v, want the cached success", aerr)
	}

	// Any deny-list mutation — here a ban of somebody else entirely — must drop
	// the cache, so the next request re-reads the row and sees the revocation.
	f.v.DenyList().AddSub("someone_else")
	if aerr := verify(); aerr == nil || aerr.Code != CodeLicenseRevoked {
		t.Fatalf("after a deny-list bump: %v, want %s", aerr, CodeLicenseRevoked)
	}
}

// TestCredentialCacheExpires pins the safety net: with no deny-list activity at
// all, a cached credential is re-read once it is older than CredentialCacheTTL.
func TestCredentialCacheExpires(t *testing.T) {
	f := newFixture(t)
	if _, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)}); aerr != nil {
		t.Fatalf("warm-up: %v", aerr)
	}
	if err := f.events.RevokeCredential(t.Context(), nil, f.cred.JKT, f.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	f.now = f.now.Add(CredentialCacheTTL + time.Second)
	f.v.SetClock(func() time.Time { return f.now })
	_, aerr := f.v.Verify(t.Context(), Request{License: f.cred.License, Proof: f.proof(t, nil)})
	if aerr == nil || aerr.Code != CodeLicenseRevoked {
		t.Fatalf("after the TTL: %v, want %s", aerr, CodeLicenseRevoked)
	}
}

// TestIssueLicenseRoundTrip checks the issuance side against the verification
// side: what IssueLicense mints, Verify accepts.
func TestIssueLicenseRoundTrip(t *testing.T) {
	e := testutil.MemEvents(t)
	ks := testutil.Keys(t)
	now := time.Unix(1_770_000_000, 0).UTC()

	key := testutil.ClientKey(t)
	jkt, err := cjws.ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	uk := ks.UserKey("dev", "issued_handle")
	playerID, err := e.EnsurePlayer(t.Context(), nil, uk, "dev", now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}

	jws, claims, err := IssueLicense(ks.Signing, IssueRequest{
		Issuer:   testIssuer,
		UserKey:  uk,
		Handle:   "issued_handle",
		JKT:      jkt,
		IssuedAt: now,
		TTL:      180 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueLicense: %v", err)
	}
	if claims.ExpiresAt-claims.IssuedAt != 180*24*3600 {
		t.Errorf("ttl = %d s, want %d s (D16)", claims.ExpiresAt-claims.IssuedAt, 180*24*3600)
	}
	if !strings.HasPrefix(claims.JTI, "lic_") {
		t.Errorf("jti = %q, want a lic_ prefix", claims.JTI)
	}

	if err := e.InsertCredential(t.Context(), nil, store.Credential{
		JKT: jkt, PlayerID: playerID, Handle: "issued_handle", LicenseJTI: claims.JTI,
		IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(180 * 24 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	v := New(Config{Issuer: testIssuer, AcceptedHTU: []string{testHTU}}, ks, e, NewDenyList())
	v.SetClock(func() time.Time { return now })

	body := []byte("body")
	proof := testutil.MintProof(t, key, map[string]any{
		"jti": ids.String(testutil.ULID(t)), "iat": now.Unix(), "htm": "POST", "htu": testHTU,
		"bh": testutil.B64USHA256(body), "sid": ids.String(testutil.ULID(t)), "seq": int64(1),
	})
	res, aerr := v.Verify(t.Context(), Request{License: jws, Proof: proof})
	if aerr != nil {
		t.Fatalf("Verify on a freshly issued license: %v", aerr)
	}
	if res.PlayerID != playerID {
		t.Errorf("player = %d, want %d", res.PlayerID, playerID)
	}
}

// TestDenyListLoadFrom checks the §5.8 refresh: tombstones, bans and revoked
// credentials all end up in the in-memory set.
func TestDenyListLoadFrom(t *testing.T) {
	e := testutil.MemEvents(t)
	ks := testutil.Keys(t)
	cred := testutil.Credential(t, e, ks, testIssuer, "doomed")

	if err := e.RevokeCredential(t.Context(), nil, cred.JKT, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.SetBan(t.Context(), nil, cred.PlayerID, 1, "cheating"); err != nil {
		t.Fatal(err)
	}
	purged := ks.UserKey("dev", "purged_player")
	if err := e.InsertTombstone(t.Context(), nil, store.Tombstone{UserKey: purged, Reason: "delete", At: 1}); err != nil {
		t.Fatal(err)
	}

	d := NewDenyList()
	if err := d.LoadFrom(t.Context(), e); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !d.HasJKT(cred.JKT) {
		t.Error("revoked jkt is not on the deny-list")
	}
	if !d.HasSub(cred.UserKey.B64U()) {
		t.Error("banned sub is not on the deny-list")
	}
	if !d.HasSub(purged.B64U()) {
		t.Error("tombstoned sub is not on the deny-list")
	}

	subs, jkts, ver := d.Snapshot()
	if len(subs) != 2 || len(jkts) != 1 || ver == 0 {
		t.Errorf("snapshot = %d subs, %d jkts, ver %d; want 2, 1, non-zero", len(subs), len(jkts), ver)
	}
}

// reheader re-encodes a compact JWS with a different protected header, leaving
// payload and signature untouched. Only useful for building headers a signer
// would refuse to produce.
func reheader(t *testing.T, compact string, header map[string]any) string {
	t.Helper()
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWS: %q", compact)
	}
	b, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	parts[0] = cjws.B64U(b)
	return strings.Join(parts, ".")
}

// licenseClaimsMap is the fixture's default license claim set as a map, so a
// case can re-sign it with a different key or kid.
func licenseClaimsMap(t *testing.T, f *fixture, mutate func(map[string]any)) map[string]any {
	t.Helper()
	c := map[string]any{
		"iss":    testIssuer,
		"sub":    f.cred.UserKey.B64U(),
		"handle": f.cred.Handle,
		"cnf":    map[string]any{"jkt": f.cred.JKT},
		"iat":    f.now.Add(-time.Hour).Unix(),
		"exp":    f.now.Add(180 * 24 * time.Hour).Unix(),
		"jti":    "lic_" + ids.String(testutil.ULID(t)),
		"ver":    1,
	}
	if mutate != nil {
		out := map[string]any{}
		maps.Copy(out, c)
		mutate(out)
		return out
	}
	return c
}
