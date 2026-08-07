package testutil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The `typ` header values from §4.5.1 and §4.5.2, repeated here rather than
// imported: these helpers are the fixtures the authz chain is tested against,
// and a fixture that shares constants with the code under test can agree with
// it about the wrong thing.
const (
	LicenseType = "catlog-license+jwt"
	ProofType   = "catlog-proof+jwt"
)

// Cred is a minted dev credential: a client key, the license bound to it, and
// the player/handle rows that back it in events.db.
type Cred struct {
	// Key is the client's P-256 private key (§4.6).
	Key *ecdsa.PrivateKey
	// JKT is the RFC 7638 thumbprint of its public half — the license cnf.jkt.
	JKT string
	// License is the compact license JWS.
	License string
	// Handle, UserKey and PlayerID are the identity rows behind the license.
	Handle   string
	UserKey  keys.UserKey
	PlayerID int64
	// Issuer is the `iss` the license carries.
	Issuer string
	// IssuedAt and ExpiresAt bound the license validity window.
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Credential mints a working credential the §4.5.3 chain accepts: it creates
// the dev player (user_key = HMAC(pepper, "dev:"+handle), §5.9), claims the
// handle, signs a license and inserts the credential row.
func Credential(t *testing.T, e *store.Events, set *keys.Set, issuer, handle string) Cred {
	t.Helper()
	return CredentialAt(t, e, set, issuer, handle, time.Now(), 180*24*time.Hour)
}

// CredentialAt is [Credential] with an explicit issue time and TTL — for
// expiry tests.
func CredentialAt(t *testing.T, e *store.Events, set *keys.Set, issuer, handle string, iat time.Time, ttl time.Duration) Cred {
	t.Helper()

	key := ClientKey(t)
	jkt, err := cjws.ThumbprintPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("thumbprint client key: %v", err)
	}

	ctx := context.Background()
	uk := set.UserKey("dev", handle)
	playerID, err := e.EnsurePlayer(ctx, nil, uk, "dev", iat.UnixMilli())
	if err != nil {
		t.Fatalf("ensure dev player: %v", err)
	}
	if err := e.ClaimHandle(ctx, playerID, handle, iat.UnixMilli()); err != nil {
		t.Fatalf("claim handle %q: %v", handle, err)
	}

	jti := "lic_" + ids.String(ULID(t))
	exp := iat.Add(ttl)
	license := MintLicense(t, set, map[string]any{
		"iss":    issuer,
		"sub":    uk.B64U(),
		"handle": handle,
		"cnf":    map[string]any{"jkt": jkt},
		"iat":    iat.Unix(),
		"exp":    exp.Unix(),
		"jti":    jti,
		"ver":    1,
	})

	if err := e.InsertCredential(ctx, nil, store.Credential{
		JKT:        jkt,
		PlayerID:   playerID,
		Handle:     handle,
		LicenseJTI: jti,
		IssuedAt:   iat.UnixMilli(),
		ExpiresAt:  exp.UnixMilli(),
	}); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	return Cred{
		Key: key, JKT: jkt, License: license, Handle: handle,
		UserKey: uk, PlayerID: playerID, Issuer: issuer,
		IssuedAt: iat, ExpiresAt: exp,
	}
}

// MintLicense signs an arbitrary claim set as a license JWS with the key set's
// active signing key. Claims are a map so a test can build a deliberately
// broken license (wrong `ver`, foreign `iss`, missing `cnf`).
func MintLicense(t *testing.T, set *keys.Set, claims map[string]any) string {
	t.Helper()
	return MintLicenseWithKey(t, set.Signing.Key, set.Signing.KID, claims)
}

// MintLicenseWithKey is [MintLicense] with an explicit signing key and kid —
// for "signed by the wrong key" and "unknown kid" cases.
func MintLicenseWithKey(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal license claims: %v", err)
	}
	jws, err := cjws.SignES256(key, payload, cjws.SignOptions{Type: LicenseType, KeyID: kid})
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}
	return jws
}

// MintProof signs an arbitrary claim set as a proof JWS, embedding key's public
// JWK in the header as §4.5.2 requires.
func MintProof(t *testing.T, key *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal proof claims: %v", err)
	}
	jws, err := cjws.SignES256(key, payload, cjws.SignOptions{Type: ProofType, EmbedJWK: true})
	if err != nil {
		t.Fatalf("sign proof: %v", err)
	}
	return jws
}

// ProofOpts describes one batch's proof (§4.5.2).
type ProofOpts struct {
	// HTU is the ingest URL the proof is bound to.
	HTU string
	// At is the `iat` instant; zero means now.
	At time.Time
	// SID is the stream; zero mints a fresh one.
	SID ids.ID
	// Seq is the 1-based batch number within the stream.
	Seq int64
	// Body is the request body as sent (post-brotli); `bh` is its SHA-256.
	Body []byte
	// PrevBody is the previous batch's body, if any; `ph` is its SHA-256 and is
	// omitted when nil (§4.5.2: omitted when seq == 1).
	PrevBody []byte
	// JTI overrides the batch id; empty mints a fresh ULID.
	JTI string
}

// Proof signs a proof for one batch with the credential's client key.
func (c Cred) Proof(t *testing.T, o ProofOpts) string {
	t.Helper()

	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	sid := o.SID
	if sid == ids.Zero {
		sid = ULID(t)
	}
	seq := o.Seq
	if seq == 0 {
		seq = 1
	}
	jti := o.JTI
	if jti == "" {
		jti = ids.String(ULID(t))
	}

	claims := map[string]any{
		"jti": jti,
		"iat": at.Unix(),
		"htm": "POST",
		"htu": o.HTU,
		"bh":  B64USHA256(o.Body),
		"sid": ids.String(sid),
		"seq": seq,
	}
	if o.PrevBody != nil {
		claims["ph"] = B64USHA256(o.PrevBody)
	}
	return MintProof(t, c.Key, claims)
}

// B64USHA256 is b64u(sha256(b)) — the `bh`/`ph` encoding of §4.5.2.
func B64USHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return cjws.B64U(sum[:])
}

// Brotli compresses b the way the mod ships a batch (§4.3).
func Brotli(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.DefaultCompression)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}
