// Package cjws wraps go-jose with catlog's narrow JWS surface: ES256 sign and
// verify, RFC 7638 thumbprints, unverified compact parse (§4.5).
//
// Deliberately thin. Everything here is mechanical JOSE; the policy — claim
// checks, the §4.5.3 verification order, deny-lists, replay windows — lives in
// package authz, which is built on top of this (WP2).
//
// Two rules are enforced here rather than by callers, because getting them
// wrong is a vulnerability rather than a bug:
//
//   - The algorithm allow-list is exactly {ES256}. go-jose v4 requires an
//     explicit allow-list at parse time, and this package never passes anything
//     else — so "alg": "none", RSA and HMAC confusion attacks are impossible by
//     construction (§4.5).
//   - Keys must be P-256. An ES256 header over a P-384 key is rejected.
package cjws

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// MaxCompactBytes is the §4.5.3 step-1 size cap: each JWS presented to the
// ingest endpoint must be at most 4 KiB.
const MaxCompactBytes = 4 << 10

// Alg is the one and only signature algorithm catlog accepts (§4.5).
const Alg = jose.ES256

// allowed is the algorithm allow-list handed to every go-jose parse. Declared
// once so no call site can widen it.
var allowed = []jose.SignatureAlgorithm{Alg}

// Errors returned by this package. Callers map them onto the §4.9 registry;
// this package has no opinion about which code a given failure deserves.
var (
	// ErrNotCompact means the input is not three base64url segments.
	ErrNotCompact = errors.New("cjws: not a compact JWS")
	// ErrTooLarge means the compact JWS exceeds MaxCompactBytes.
	ErrTooLarge = errors.New("cjws: compact JWS too large")
	// ErrBadAlg means the header "alg" is not ES256.
	ErrBadAlg = errors.New("cjws: alg is not ES256")
	// ErrBadCurve means an EC key is not on P-256.
	ErrBadCurve = errors.New("cjws: key is not P-256")
	// ErrBadSignature means the signature did not verify under the given key.
	ErrBadSignature = errors.New("cjws: signature does not verify")
	// ErrNoJWK means the header carries no embedded "jwk" (proof JWS, §4.5.2).
	ErrNoJWK = errors.New("cjws: no embedded jwk in header")
)

// SignOptions configures the protected header written by [SignES256].
type SignOptions struct {
	// Type sets "typ" — "catlog-license+jwt" or "catlog-proof+jwt" (§4.5).
	Type string
	// KeyID sets "kid". Omitted when empty. Licenses carry one
	// ("catlog-<yyyymm>"); proofs do not.
	KeyID string
	// EmbedJWK writes the signer's public JWK into the header as "jwk", which
	// is how a proof JWS carries the key its thumbprint must match (§4.5.2).
	// Mutually exclusive with KeyID in practice, though not enforced.
	EmbedJWK bool
}

// SignES256 signs payload with key and returns a compact JWS.
//
// payload is written verbatim as the JWS payload — this signs bytes, not
// claims. Callers marshal their own claim set, which keeps key ordering and
// number formatting under the caller's control (it matters for the
// cross-language conformance vectors, §4.10).
func SignES256(key *ecdsa.PrivateKey, payload []byte, opts SignOptions) (string, error) {
	if key == nil {
		return "", errors.New("cjws: nil signing key")
	}
	if key.Curve != elliptic.P256() {
		return "", ErrBadCurve
	}

	so := (&jose.SignerOptions{}).WithType(jose.ContentType(opts.Type))
	so.EmbedJWK = opts.EmbedJWK
	if opts.KeyID != "" {
		so.WithHeader(jose.HeaderKey("kid"), opts.KeyID)
	}

	// go-jose only embeds a "jwk" when the signing key is itself a JSONWebKey;
	// a bare *ecdsa.PrivateKey yields no header. Wrap it so EmbedJWK works, and
	// leave KeyID/Algorithm/Use unset so the embedded JWK marshals to exactly
	// {kty,crv,x,y} as §4.5.2 requires.
	var signingKey any = key
	if opts.EmbedJWK {
		signingKey = jose.JSONWebKey{Key: key}
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: Alg, Key: signingKey}, so)
	if err != nil {
		return "", fmt.Errorf("cjws: new signer: %w", err)
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("cjws: sign: %w", err)
	}
	compact, err := sig.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("cjws: serialize: %w", err)
	}
	return compact, nil
}

// VerifyES256 verifies compact under pub and returns the payload.
//
// The returned payload is trustworthy only in the sense that it was signed by
// pub; nothing about its contents has been checked.
func VerifyES256(compact string, pub *ecdsa.PublicKey) ([]byte, error) {
	if pub == nil {
		return nil, errors.New("cjws: nil verification key")
	}
	if pub.Curve != elliptic.P256() {
		return nil, ErrBadCurve
	}
	sig, err := parse(compact)
	if err != nil {
		return nil, err
	}
	payload, err := sig.Verify(pub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return payload, nil
}

// Header is the subset of a JOSE protected header catlog reads (§4.5.1, §4.5.2).
type Header struct {
	// Alg is the "alg" parameter as presented. Always "ES256" for anything
	// [ParseCompactUnverified] returns, since parsing enforces the allow-list.
	Alg string
	// KeyID is "kid" — the license signing key selector, empty on proofs.
	KeyID string
	// Type is "typ" — "catlog-license+jwt" or "catlog-proof+jwt".
	Type string
	// JWK is the embedded public key from "jwk", nil when absent. Present on
	// proofs (§4.5.2), absent on licenses.
	JWK *jose.JSONWebKey
}

// ParseCompactUnverified parses a compact JWS without checking its signature,
// returning the protected header and the raw payload.
//
// This exists for the two places the plan needs data before it can pick a key:
// §4.5.3 step 2 (read "kid" to select the license signing key) and step 6 (read
// the embedded "jwk" to thumbprint it). It also backs the credential loader's
// "decode to display handle and expiry" path (§4.6).
//
// The name says what it is. The payload it returns is attacker-controlled until
// [VerifyES256] has succeeded over the same string; never act on it before then.
func ParseCompactUnverified(compact string) (Header, []byte, error) {
	sig, err := parse(compact)
	if err != nil {
		return Header{}, nil, err
	}
	// parse() rejects anything but a single-signature compact JWS, so index 0
	// is always present.
	jh := sig.Signatures[0].Header
	h := Header{Alg: jh.Algorithm, KeyID: jh.KeyID, JWK: jh.JSONWebKey}
	if typ, ok := jh.ExtraHeaders[jose.HeaderType]; ok {
		h.Type, _ = typ.(string)
	}
	return h, sig.UnsafePayloadWithoutVerification(), nil
}

// parse applies the shared structural checks: size cap, compact shape and the
// {ES256} allow-list.
func parse(compact string) (*jose.JSONWebSignature, error) {
	if len(compact) > MaxCompactBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(compact))
	}
	if strings.Count(compact, ".") != 2 {
		return nil, ErrNotCompact
	}
	sig, err := jose.ParseSigned(compact, allowed)
	if err != nil {
		// go-jose reports an out-of-allow-list "alg" as a parse failure; make
		// that specific case legible to callers mapping onto §4.9 codes.
		if strings.Contains(err.Error(), "unexpected signature algorithm") {
			return nil, fmt.Errorf("%w: %v", ErrBadAlg, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrNotCompact, err)
	}
	if len(sig.Signatures) != 1 {
		return nil, fmt.Errorf("%w: %d signatures", ErrNotCompact, len(sig.Signatures))
	}
	if sig.Signatures[0].Header.Algorithm != string(Alg) {
		return nil, ErrBadAlg
	}
	return sig, nil
}

// Thumbprint computes the RFC 7638 SHA-256 JWK thumbprint, base64url-encoded
// without padding — the `cnf.jkt` / `jkt` value used throughout §4.5.
//
// RFC 7638 hashes a canonical JSON object containing only the required members
// in lexicographic order (for EC: crv, kty, x, y), so the result is independent
// of how the key was serialized. go-jose implements that canonicalization.
func Thumbprint(k *jose.JSONWebKey) (string, error) {
	if k == nil {
		return "", errors.New("cjws: nil jwk")
	}
	if ec, ok := k.Key.(*ecdsa.PublicKey); ok && ec.Curve != elliptic.P256() {
		return "", ErrBadCurve
	}
	if ec, ok := k.Key.(*ecdsa.PrivateKey); ok && ec.Curve != elliptic.P256() {
		return "", ErrBadCurve
	}
	sum, err := k.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("cjws: thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// ThumbprintPublicKey is [Thumbprint] for a bare P-256 public key.
func ThumbprintPublicKey(pub *ecdsa.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("cjws: nil public key")
	}
	return Thumbprint(&jose.JSONWebKey{Key: pub})
}

// PublicJWK renders pub as the minimal public JWK §4.5.2 requires: exactly
// {kty, crv, x, y}, no kid, no alg, no use.
func PublicJWK(pub *ecdsa.PublicKey) (json.RawMessage, error) {
	if pub == nil {
		return nil, errors.New("cjws: nil public key")
	}
	if pub.Curve != elliptic.P256() {
		return nil, ErrBadCurve
	}
	b, err := (&jose.JSONWebKey{Key: pub}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("cjws: marshal jwk: %w", err)
	}
	return b, nil
}

// ParsePublicJWK decodes a JWK and returns its P-256 public key. It rejects
// anything that is not an EC P-256 *public* key — in particular a JWK that
// carries a private "d" member, which must never be accepted from a client
// (§13.6).
func ParsePublicJWK(raw []byte) (*ecdsa.PublicKey, error) {
	var k jose.JSONWebKey
	if err := k.UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("cjws: parse jwk: %w", err)
	}
	return publicKeyOf(&k)
}

// PublicKeyOf extracts the P-256 public key from a parsed JWK, rejecting
// private material and wrong curves. Used on the "jwk" embedded in a proof
// header (§4.5.3 step 6).
func PublicKeyOf(k *jose.JSONWebKey) (*ecdsa.PublicKey, error) {
	if k == nil {
		return nil, ErrNoJWK
	}
	return publicKeyOf(k)
}

func publicKeyOf(k *jose.JSONWebKey) (*ecdsa.PublicKey, error) {
	pub, ok := k.Key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("cjws: jwk is %T, want an EC public key", k.Key)
	}
	if pub.Curve != elliptic.P256() {
		return nil, ErrBadCurve
	}
	return pub, nil
}

// B64U encodes b as base64url without padding — the encoding used by every
// catlog field that carries bytes in JSON (`sub`, `bh`, `ph`, `jkt`, §4.5).
func B64U(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// DecodeB64U decodes base64url without padding.
func DecodeB64U(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("cjws: decode base64url: %w", err)
	}
	return b, nil
}
