package cjws

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// TestRFC6979Vector pins the determinism claim to the specification rather than
// to "it happened to be stable twice": RFC 6979 Appendix A.2.5 gives (k, r, s)
// for P-256 + SHA-256 over the message "sample". If the standard library ever
// stopped doing deterministic ECDSA — or started hedging the nonce — this test
// fails and the §4.10 vectors stop being reproducible.
func TestRFC6979Vector(t *testing.T) {
	const (
		privHex = "C9AFA9D845BA75166B5C215767B1D6934E50C3DB36E89B127B8A622B120F6721"
		wantR   = "EFD48B2AACB6A8FD1140DD9CD45E81D69D2C877B56AAF991C34D0EA84EAF3716"
		wantS   = "F7CB1C942D657C41D436C7A1B6E29F65F3E900DBB9AFF4064DC4AB2F843ACDA8"
	)

	d, ok := new(big.Int).SetString(privHex, 16)
	if !ok {
		t.Fatal("bad test constant")
	}
	key := &ecdsa.PrivateKey{D: d}
	key.Curve = elliptic.P256()
	key.PublicKey.Curve = elliptic.P256()
	key.PublicKey.X, key.PublicKey.Y = elliptic.P256().ScalarBaseMult(d.Bytes())

	sig, err := signRawES256(key, []byte("sample"))
	if err != nil {
		t.Fatalf("signRawES256: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (r‖s, IEEE P-1363)", len(sig))
	}
	if got := strings.ToUpper(hex.EncodeToString(sig[:32])); got != wantR {
		t.Errorf("r = %s, want %s", got, wantR)
	}
	if got := strings.ToUpper(hex.EncodeToString(sig[32:])); got != wantS {
		t.Errorf("s = %s, want %s", got, wantS)
	}
}

// TestDeterministicSignIsByteIdentical is the property the conformance vectors
// depend on: same key, same payload, same bytes — every time.
func TestDeterministicSignIsByteIdentical(t *testing.T) {
	key := testKey(t)
	payload := []byte(`{"jti":"lic_01","ver":1}`)
	opts := SignOptions{Type: "catlog-license+jwt", KeyID: "catlog-202602"}

	first, err := SignES256Deterministic(key, payload, opts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	for i := range 8 {
		again, err := SignES256Deterministic(key, payload, opts)
		if err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("signature %d differs:\n%s\n%s", i, first, again)
		}
	}

	// And the randomized signer must NOT be stable — otherwise the two paths
	// have been confused with each other somewhere.
	a, err := SignES256(key, payload, opts)
	if err != nil {
		t.Fatalf("randomized sign: %v", err)
	}
	b, err := SignES256(key, payload, opts)
	if err != nil {
		t.Fatalf("randomized sign: %v", err)
	}
	if a == b {
		t.Error("SignES256 produced identical bytes twice; it is supposed to be randomized")
	}
}

// TestDeterministicSignVerifies proves the hand-built compact form is real JOSE:
// go-jose parses and verifies it, and the header carries the §4.5 members.
func TestDeterministicSignVerifies(t *testing.T) {
	key := testKey(t)
	payload := []byte(`{"htm":"POST"}`)

	compact, err := SignES256Deterministic(key, payload, SignOptions{Type: "catlog-proof+jwt", EmbedJWK: true})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	got, err := VerifyES256(compact, &key.PublicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload = %s, want %s", got, payload)
	}

	h, _, err := ParseCompactUnverified(compact)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.Alg != "ES256" || h.Type != "catlog-proof+jwt" || h.JWK == nil {
		t.Fatalf("header = %+v, want ES256/catlog-proof+jwt with an embedded jwk", h)
	}

	// Header member order is part of the contract: it is covered by the
	// signature, so a reordering would silently change every vector file.
	raw, err := DecodeB64U(strings.Split(compact, ".")[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if want := `{"alg":"ES256","typ":"catlog-proof+jwt","jwk":`; !strings.HasPrefix(string(raw), want) {
		t.Errorf("header = %s, want prefix %s", raw, want)
	}

	// The embedded JWK is exactly {kty,crv,x,y} (§4.5.2) — no kid, alg or use.
	var hdr struct {
		JWK map[string]any `json:"jwk"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	for k := range hdr.JWK {
		switch k {
		case "kty", "crv", "x", "y":
		default:
			t.Errorf("embedded jwk carries unexpected member %q", k)
		}
	}
}

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	// A fixed key, so this file's expectations never depend on entropy.
	d, _ := new(big.Int).SetString("7f8b2c5d9e1a3f4b6c8d0e2a4f6b8c1d3e5a7f9b2c4d6e8a0f1b3c5d7e9a2f4b", 16)
	key := &ecdsa.PrivateKey{D: d}
	key.Curve = elliptic.P256()
	key.PublicKey.Curve = elliptic.P256()
	key.PublicKey.X, key.PublicKey.Y = elliptic.P256().ScalarBaseMult(d.Bytes())
	return key
}
