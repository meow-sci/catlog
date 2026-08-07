package authz

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// LicenseVer is the only license `ver` this server accepts (§4.5.1).
const LicenseVer = 1

// LicenseType and ProofType are the `typ` header values (§4.5.1, §4.5.2).
const (
	LicenseType = "catlog-license+jwt"
	ProofType   = "catlog-proof+jwt"
)

// MaxSkew is the §4.3 clock-skew allowance for a proof's `iat`.
const MaxSkew = 300 * time.Second

// Confirmation is the license `cnf` claim: the RFC 7638 thumbprint of the
// client key the license is bound to (§4.5.1).
type Confirmation struct {
	JKT string `json:"jkt"`
}

// LicenseClaims is the §4.5.1 claim set.
//
// Field order is the §4.5.1 order and is load-bearing: encoding/json emits
// struct fields in declaration order, which is what makes a signed license
// reproducible byte-for-byte in the conformance vectors (§4.10).
type LicenseClaims struct {
	Issuer    string       `json:"iss"`
	Subject   string       `json:"sub"` // b64u(32-byte user_key)
	Handle    string       `json:"handle"`
	Cnf       Confirmation `json:"cnf"`
	IssuedAt  int64        `json:"iat"` // unix seconds
	ExpiresAt int64        `json:"exp"` // unix seconds
	JTI       string       `json:"jti"` // "lic_<ulid>"
	Ver       int          `json:"ver"`
}

// UserKey decodes the `sub` claim into the 32-byte user_key it encodes.
func (c LicenseClaims) UserKey() (keys.UserKey, error) { return keys.ParseUserKey(c.Subject) }

// Expired reports whether the license has passed its `exp` at now.
func (c LicenseClaims) Expired(now time.Time) bool { return now.Unix() >= c.ExpiresAt }

// ProofClaims is the §4.5.2 claim set. Field order is the §4.5.2 order, for the
// same reproducibility reason as [LicenseClaims].
type ProofClaims struct {
	JTI       string `json:"jti"` // batch id (ULID)
	IssuedAt  int64  `json:"iat"` // unix seconds
	HTM       string `json:"htm"`
	HTU       string `json:"htu"`
	BH        string `json:"bh"`           // b64u(sha256(body as sent))
	SID       string `json:"sid"`          // stream id (ULID)
	Seq       int64  `json:"seq"`          // 1-based, monotonic per (jkt, sid)
	PH        string `json:"ph,omitempty"` // omitted when seq == 1
	sid       ids.ID
	batchULID ids.ID
}

// StreamID is the parsed `sid` claim.
func (c ProofClaims) StreamID() ids.ID { return c.sid }

// BatchID is the batch identifier — the proof's `jti` — as stored in
// `ingest_batch` (§5.4).
func (c ProofClaims) BatchID() string { return c.JTI }

// BodyHash returns b64u(sha256(body)), the value a proof's `bh` must equal
// (§4.5.2). Exported because the mod-side and vector-side both need one
// definition of it.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return cjws.B64U(sum[:])
}

// IssueRequest describes one license to mint (§4.5.1).
type IssueRequest struct {
	// Issuer is the license `iss` — the server base URL.
	Issuer string
	// UserKey identifies the player; it becomes `sub`.
	UserKey keys.UserKey
	// Handle is the claimed handle the license is issued for.
	Handle string
	// JKT is the RFC 7638 thumbprint of the client's public key (`cnf.jkt`).
	JKT string
	// IssuedAt is the `iat` instant; zero means now.
	IssuedAt time.Time
	// TTL is the license lifetime (D16: 180 days).
	TTL time.Duration
	// JTI overrides the generated "lic_<ulid>" identifier. Only the conformance
	// vectors set it — everything else wants a fresh ULID.
	JTI string
}

// IssueLicense mints and signs a license JWS (§4.5.1).
//
// The signature is randomized, which is the right default; the conformance
// vectors sign the same claim bytes with [cjws.SignES256Deterministic] instead
// (§4.10).
func IssueLicense(signing keys.SigningKey, req IssueRequest) (string, LicenseClaims, error) {
	if signing.Key == nil {
		return "", LicenseClaims{}, errors.New("authz: no signing key")
	}
	if req.Issuer == "" {
		return "", LicenseClaims{}, errors.New("authz: issue with empty issuer")
	}
	if req.Handle == "" {
		return "", LicenseClaims{}, errors.New("authz: issue with empty handle")
	}
	if req.JKT == "" {
		return "", LicenseClaims{}, errors.New("authz: issue with empty jkt")
	}
	if req.TTL <= 0 {
		return "", LicenseClaims{}, errors.New("authz: issue with non-positive ttl")
	}

	iat := req.IssuedAt
	if iat.IsZero() {
		iat = time.Now()
	}
	jti := req.JTI
	if jti == "" {
		id, err := ids.New()
		if err != nil {
			return "", LicenseClaims{}, fmt.Errorf("authz: mint license jti: %w", err)
		}
		jti = "lic_" + ids.String(id)
	}

	claims := LicenseClaims{
		Issuer:    req.Issuer,
		Subject:   req.UserKey.B64U(),
		Handle:    req.Handle,
		Cnf:       Confirmation{JKT: req.JKT},
		IssuedAt:  iat.Unix(),
		ExpiresAt: iat.Add(req.TTL).Unix(),
		JTI:       jti,
		Ver:       LicenseVer,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", LicenseClaims{}, fmt.Errorf("authz: marshal license claims: %w", err)
	}
	jws, err := cjws.SignES256(signing.Key, payload, cjws.SignOptions{Type: LicenseType, KeyID: signing.KID})
	if err != nil {
		return "", LicenseClaims{}, fmt.Errorf("authz: sign license: %w", err)
	}
	return jws, claims, nil
}

// --- structural parsing (§4.5.3 step 1) -------------------------------------

// compactParts is the result of the step-1 structural check: a compact JWS split
// into its three segments with the header decoded.
type compactParts struct {
	headerJSON []byte
	payload    []byte
}

// jwsHeader is the protected header catlog reads (§4.5.1, §4.5.2).
type jwsHeader struct {
	Alg string          `json:"alg"`
	KID string          `json:"kid"`
	Typ string          `json:"typ"`
	JWK json.RawMessage `json:"jwk"`
}

// splitCompact does the cheapest possible structural validation of a compact
// JWS: size, three base64url segments, a header that is JSON.
//
// It deliberately does not use go-jose. Step 1 runs before anything else on
// every request including hostile ones, so it must not allocate a parser, and
// its failures must be attributable to step 1 rather than to the algorithm
// check that belongs to steps 2 and 6.
func splitCompact(compact string) (compactParts, error) {
	if len(compact) > cjws.MaxCompactBytes {
		return compactParts{}, fmt.Errorf("%d bytes exceeds the %d byte limit", len(compact), cjws.MaxCompactBytes)
	}
	h, rest, ok := strings.Cut(compact, ".")
	if !ok {
		return compactParts{}, errors.New("not a compact JWS")
	}
	p, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return compactParts{}, errors.New("not a compact JWS")
	}
	if h == "" || sig == "" || strings.Contains(sig, ".") {
		return compactParts{}, errors.New("not a compact JWS")
	}

	headerJSON, err := cjws.DecodeB64U(h)
	if err != nil {
		return compactParts{}, errors.New("header is not base64url")
	}
	payload, err := cjws.DecodeB64U(p)
	if err != nil {
		return compactParts{}, errors.New("payload is not base64url")
	}
	if _, err := cjws.DecodeB64U(sig); err != nil {
		return compactParts{}, errors.New("signature is not base64url")
	}
	if !json.Valid(headerJSON) {
		return compactParts{}, errors.New("header is not JSON")
	}
	return compactParts{headerJSON: headerJSON, payload: payload}, nil
}

// header decodes the protected header of an already-split JWS.
func (p compactParts) header() (jwsHeader, error) {
	var h jwsHeader
	if err := json.Unmarshal(p.headerJSON, &h); err != nil {
		return jwsHeader{}, fmt.Errorf("header: %w", err)
	}
	return h, nil
}
