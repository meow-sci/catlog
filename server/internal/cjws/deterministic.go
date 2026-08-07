package cjws

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// SignES256Deterministic is [SignES256] with RFC 6979 deterministic ECDSA: the
// same key and payload always produce byte-identical output.
//
// This exists for the cross-language conformance vectors (§4.10), which must
// regenerate byte-for-byte. Ordinary signing uses [SignES256] — a randomized
// nonce is the conservative choice for production, and nothing outside the
// vector generator needs reproducible signature bytes.
//
// Determinism comes from the standard library, not from us: since Go 1.24
// crypto/ecdsa implements FIPS 186-5 / RFC 6979 deterministic signing and
// [ecdsa.PrivateKey.Sign] selects it when the random source is nil. The
// protected header is built here rather than by go-jose so its member order is
// fixed too — go-jose offers no ordering guarantee, and the header bytes are
// covered by the signature.
//
// Header member order is exactly §4.5.1/§4.5.2: alg, kid, typ, jwk (absent
// members are omitted).
func SignES256Deterministic(key *ecdsa.PrivateKey, payload []byte, opts SignOptions) (string, error) {
	if key == nil {
		return "", errors.New("cjws: nil signing key")
	}
	if key.Curve != elliptic.P256() {
		return "", ErrBadCurve
	}

	h := header{Alg: string(Alg), KID: opts.KeyID, Typ: opts.Type}
	if opts.EmbedJWK {
		jwk, err := PublicJWK(&key.PublicKey)
		if err != nil {
			return "", err
		}
		h.JWK = jwk
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("cjws: marshal header: %w", err)
	}

	signingInput := B64U(hb) + "." + B64U(payload)
	sig, err := signRawES256(key, []byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + B64U(sig), nil
}

// header is the protected header of a catlog JWS. Field order is the JSON
// member order encoding/json emits, which is what makes the compact form
// reproducible.
type header struct {
	Alg string          `json:"alg"`
	KID string          `json:"kid,omitempty"`
	Typ string          `json:"typ,omitempty"`
	JWK json.RawMessage `json:"jwk,omitempty"`
}

// signRawES256 produces the 64-byte r‖s (IEEE P-1363) signature JWS requires,
// deterministically. crypto/ecdsa returns ASN.1 DER, so the two integers are
// unpacked and left-padded to the 32-byte field width.
func signRawES256(key *ecdsa.PrivateKey, message []byte) ([]byte, error) {
	digest := sha256.Sum256(message)
	// A nil random source selects the RFC 6979 path (crypto/ecdsa).
	der, err := key.Sign(nil, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("cjws: deterministic sign: %w", err)
	}
	r, s, err := parseDERSignature(der)
	if err != nil {
		return nil, err
	}

	const coordLen = 32 // P-256 field width
	out := make([]byte, 2*coordLen)
	if len(r) > coordLen || len(s) > coordLen {
		return nil, fmt.Errorf("cjws: signature integers do not fit P-256 (%d, %d bytes)", len(r), len(s))
	}
	copy(out[coordLen-len(r):coordLen], r)
	copy(out[2*coordLen-len(s):], s)
	return out, nil
}

// parseDERSignature unwraps SEQUENCE { r INTEGER, s INTEGER } into big-endian
// magnitudes with leading zero bytes stripped.
func parseDERSignature(der []byte) (r, s []byte, err error) {
	var parsed struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("cjws: malformed ASN.1 ECDSA signature: %w", err)
	}
	if len(rest) != 0 {
		return nil, nil, errors.New("cjws: trailing bytes after ASN.1 ECDSA signature")
	}
	if parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		return nil, nil, errors.New("cjws: non-positive ECDSA signature integer")
	}
	return parsed.R.Bytes(), parsed.S.Bytes(), nil
}
