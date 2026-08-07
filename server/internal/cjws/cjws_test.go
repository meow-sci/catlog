package cjws

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256: %v", err)
	}
	return k
}

func TestSignVerifyRoundTrip(t *testing.T) {
	key := p256(t)
	payload := []byte(`{"iss":"http://127.0.0.1:8080","ver":1}`)

	compact, err := SignES256(key, payload, SignOptions{Type: "catlog-license+jwt", KeyID: "catlog-202608"})
	if err != nil {
		t.Fatalf("SignES256: %v", err)
	}
	if strings.Count(compact, ".") != 2 {
		t.Fatalf("not compact serialization: %q", compact)
	}

	got, err := VerifyES256(compact, &key.PublicKey)
	if err != nil {
		t.Fatalf("VerifyES256: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}

	h, unverified, err := ParseCompactUnverified(compact)
	if err != nil {
		t.Fatalf("ParseCompactUnverified: %v", err)
	}
	if h.Alg != "ES256" {
		t.Errorf("alg = %q, want ES256", h.Alg)
	}
	if h.KeyID != "catlog-202608" {
		t.Errorf("kid = %q, want catlog-202608", h.KeyID)
	}
	if h.Type != "catlog-license+jwt" {
		t.Errorf("typ = %q, want catlog-license+jwt", h.Type)
	}
	if h.JWK != nil {
		t.Errorf("license header must not embed a jwk, got %+v", h.JWK)
	}
	if string(unverified) != string(payload) {
		t.Errorf("unverified payload = %q, want %q", unverified, payload)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	compact, err := SignES256(p256(t), []byte(`{}`), SignOptions{Type: "catlog-proof+jwt"})
	if err != nil {
		t.Fatalf("SignES256: %v", err)
	}
	other := p256(t)
	if _, err := VerifyES256(compact, &other.PublicKey); !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	key := p256(t)
	compact, err := SignES256(key, []byte(`{"seq":1}`), SignOptions{Type: "catlog-proof+jwt"})
	if err != nil {
		t.Fatalf("SignES256: %v", err)
	}
	parts := strings.Split(compact, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"seq":9}`))
	if _, err := VerifyES256(strings.Join(parts, "."), &key.PublicKey); !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

// TestParseRejectsNonES256 covers the alg allow-list (§4.5): no "none", no HMAC.
func TestParseRejectsNonES256(t *testing.T) {
	// alg:none, hand-assembled — go-jose will not produce one.
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"catlog-proof+jwt"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`))
	none := hdr + "." + body + "."
	if _, _, err := ParseCompactUnverified(none); err == nil {
		t.Fatal("alg:none accepted, want rejection")
	}

	// HS256 over a symmetric key, produced by go-jose so it is structurally valid.
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: make([]byte, 32)}, nil)
	if err != nil {
		t.Fatalf("new HS256 signer: %v", err)
	}
	sig, err := signer.Sign([]byte(`{}`))
	if err != nil {
		t.Fatalf("HS256 sign: %v", err)
	}
	hs, err := sig.CompactSerialize()
	if err != nil {
		t.Fatalf("HS256 serialize: %v", err)
	}
	if _, _, err := ParseCompactUnverified(hs); !errors.Is(err, ErrBadAlg) {
		t.Errorf("HS256 err = %v, want ErrBadAlg", err)
	}
}

func TestParseRejectsOversizeAndMalformed(t *testing.T) {
	if _, _, err := ParseCompactUnverified(strings.Repeat("a", MaxCompactBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize err = %v, want ErrTooLarge", err)
	}
	for _, bad := range []string{"", "a.b", "a.b.c.d", "not-a-jws"} {
		if _, _, err := ParseCompactUnverified(bad); err == nil {
			t.Errorf("ParseCompactUnverified(%q) = nil error, want failure", bad)
		}
	}
}

func TestSignRejectsNonP256(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384: %v", err)
	}
	if _, err := SignES256(k, []byte(`{}`), SignOptions{}); !errors.Is(err, ErrBadCurve) {
		t.Errorf("err = %v, want ErrBadCurve", err)
	}
	if _, err := VerifyES256("a.b.c", &k.PublicKey); !errors.Is(err, ErrBadCurve) {
		t.Errorf("verify err = %v, want ErrBadCurve", err)
	}
}

// TestEmbeddedJWKIsMinimalAndMatchesThumbprint pins the §4.5.2 proof header:
// the embedded jwk carries exactly {kty,crv,x,y}, and its thumbprint is the
// value a license's cnf.jkt must equal (§4.5.3 step 6).
func TestEmbeddedJWKIsMinimalAndMatchesThumbprint(t *testing.T) {
	key := p256(t)
	compact, err := SignES256(key, []byte(`{"seq":1}`), SignOptions{Type: "catlog-proof+jwt", EmbedJWK: true})
	if err != nil {
		t.Fatalf("SignES256: %v", err)
	}
	h, _, err := ParseCompactUnverified(compact)
	if err != nil {
		t.Fatalf("ParseCompactUnverified: %v", err)
	}
	if h.JWK == nil {
		t.Fatal("proof header has no embedded jwk")
	}

	raw, err := h.JWK.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal embedded jwk: %v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("unmarshal embedded jwk: %v", err)
	}
	want := map[string]bool{"kty": true, "crv": true, "x": true, "y": true}
	for k := range members {
		if !want[k] {
			t.Errorf("embedded jwk has extra member %q (§4.5.2 allows only kty,crv,x,y); full jwk = %s", k, raw)
		}
	}
	for k := range want {
		if _, ok := members[k]; !ok {
			t.Errorf("embedded jwk missing member %q; full jwk = %s", k, raw)
		}
	}

	pub, err := PublicKeyOf(h.JWK)
	if err != nil {
		t.Fatalf("PublicKeyOf: %v", err)
	}
	if !pub.Equal(&key.PublicKey) {
		t.Error("embedded jwk is not the signing key's public part")
	}
	if _, err := VerifyES256(compact, pub); err != nil {
		t.Errorf("verify with embedded jwk: %v", err)
	}

	fromHeader, err := Thumbprint(h.JWK)
	if err != nil {
		t.Fatalf("Thumbprint: %v", err)
	}
	fromKey, err := ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("ThumbprintPublicKey: %v", err)
	}
	if fromHeader != fromKey {
		t.Errorf("thumbprint mismatch: header %q vs key %q", fromHeader, fromKey)
	}
}

// TestThumbprintRFC7638Vector checks Thumbprint against the published vector in
// RFC 7638 §3.1 — the only normative JWK thumbprint test vector the RFC ships.
// It is an RSA key; catlog only ever thumbprints P-256 keys, but this pins the
// canonicalization and digest against the standard rather than against
// ourselves.
func TestThumbprintRFC7638Vector(t *testing.T) {
	// RFC 7638 §3.1, verbatim.
	const rfc7638Key = `{
      "kty": "RSA",
      "n": "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw",
      "e": "AQAB",
      "alg": "RS256",
      "kid": "2011-04-29"
     }`
	const want = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"

	var k jose.JSONWebKey
	if err := k.UnmarshalJSON([]byte(rfc7638Key)); err != nil {
		t.Fatalf("parse RFC 7638 key: %v", err)
	}
	got, err := Thumbprint(&k)
	if err != nil {
		t.Fatalf("Thumbprint: %v", err)
	}
	if got != want {
		t.Errorf("RFC 7638 §3.1 thumbprint = %q, want %q", got, want)
	}
}

// TestThumbprintECCanonicalization independently reproduces RFC 7638 §3.2's
// required-members-in-lexicographic-order rule for an EC key (crv, kty, x, y)
// and checks Thumbprint against it. The RFC publishes no EC vector, so this is
// the equivalent check computed from the spec text rather than from go-jose.
func TestThumbprintECCanonicalization(t *testing.T) {
	key := p256(t)

	raw, err := PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatalf("PublicJWK: %v", err)
	}
	var members struct {
		Crv string `json:"crv"`
		Kty string `json:"kty"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("unmarshal jwk: %v", err)
	}
	if members.Kty != "EC" || members.Crv != "P-256" {
		t.Fatalf("jwk = %s, want kty EC / crv P-256", raw)
	}
	// RFC 7638 §3.2: no whitespace, members in lexicographic order, required
	// members only. For EC that is exactly crv, kty, x, y.
	canonical := `{"crv":"` + members.Crv + `","kty":"` + members.Kty + `","x":"` + members.X + `","y":"` + members.Y + `"}`
	sum := sha256.Sum256([]byte(canonical))
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	got, err := ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("ThumbprintPublicKey: %v", err)
	}
	if got != want {
		t.Errorf("EC thumbprint = %q, want %q (canonical form %s)", got, want, canonical)
	}
	if len(got) != 43 { // 32-byte SHA-256 in unpadded base64url
		t.Errorf("thumbprint length = %d, want 43", len(got))
	}
}

// TestParsePublicJWKRejectsPrivateKey guards §13.6: a browser's
// exportKey("jwk") on a private key includes "d", and that must never be
// accepted as a client public key.
func TestParsePublicJWKRejectsPrivateKey(t *testing.T) {
	key := p256(t)
	priv, err := (&jose.JSONWebKey{Key: key}).MarshalJSON()
	if err != nil {
		t.Fatalf("marshal private jwk: %v", err)
	}
	if !strings.Contains(string(priv), `"d"`) {
		t.Fatalf("expected a private jwk with a d member, got %s", priv)
	}
	if _, err := ParsePublicJWK(priv); err == nil {
		t.Error("ParsePublicJWK accepted a private JWK, want rejection")
	}

	pub, err := PublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatalf("PublicJWK: %v", err)
	}
	if strings.Contains(string(pub), `"d"`) {
		t.Errorf("PublicJWK leaked private material: %s", pub)
	}
	got, err := ParsePublicJWK(pub)
	if err != nil {
		t.Fatalf("ParsePublicJWK: %v", err)
	}
	if !got.Equal(&key.PublicKey) {
		t.Error("round-tripped public JWK is a different key")
	}
}

func TestB64URoundTrip(t *testing.T) {
	in := []byte{0x00, 0xff, 0x10, 0x20, 0x7f}
	s := B64U(in)
	if strings.ContainsAny(s, "=+/") {
		t.Errorf("B64U(%x) = %q, want unpadded base64url", in, s)
	}
	out, err := DecodeB64U(s)
	if err != nil {
		t.Fatalf("DecodeB64U: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("round trip = %x, want %x", out, in)
	}
}
