// Package testvectors generates the cross-language conformance vectors of
// §4.10 into `contracts/testdata`.
//
// These files are the contract that guarantees mod↔server interop without the
// game: the Go suite and the C# suite both read them, so a change to either
// side that breaks the wire format fails a test instead of failing in orbit.
//
// # Byte-identical regeneration
//
// §4.10 requires regeneration to be byte-for-byte reproducible, and every
// source of variation is removed rather than tolerated:
//
//   - Keys are fixed. The three P-256 keys are PKCS#8 PEM constants in this
//     file, so `keys/*.pem` are literally those constants.
//   - Time is fixed. Everything is derived from [ReferenceTime]; nothing calls
//     time.Now.
//   - Identifiers are fixed. ULIDs are built from a fixed millisecond timestamp
//     plus ten entropy bytes derived by SHA-256 from a label, never minted.
//   - Signatures are fixed. ECDSA is randomized by nature, so signing goes
//     through [cjws.SignES256Deterministic], which uses the RFC 6979
//     deterministic nonce the standard library implements. The protected header
//     is emitted in a fixed member order for the same reason — it is covered by
//     the signature.
//   - JSON is fixed. Every claim set is a Go struct, and encoding/json emits
//     struct fields in declaration order.
//   - Compression is fixed. Brotli is deterministic for a given library version
//     and quality; [brotliQuality] and [brotliWindow] are pinned here and
//     `github.com/andybalholm/brotli` is pinned in go.mod.
//
// A file's bytes therefore depend only on this source file and on the pinned
// versions of go-jose, brotli and the Go toolchain's ECDSA. `TestGenerateIsByteIdentical`
// and `TestCommittedVectorsAreCurrent` enforce that from both directions.
//
// # A note on `license/license-claims.json`
//
// It holds the *exact* bytes of the license JWS payload plus one trailing
// newline, not a pretty-printed rendering. That makes it directly comparable
// with `base64url_decode(license-valid.jws.split('.')[1])` in either language.
package testvectors

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/andybalholm/brotli"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// The fixed parameters every vector is derived from.
const (
	// ReferenceTime is "now" for the whole vector set (unix seconds). A
	// verifier must evaluate the vectors at this instant — the valid license
	// expires long before any real clock reads it.
	ReferenceTime = 1_770_000_000

	// Issuer and HTU are the dev URLs of §3.
	Issuer = "http://127.0.0.1:8080"
	HTU    = "http://127.0.0.1:8080/v1/ingest"

	// Handle is the account the license is issued to.
	Handle = "whiskers_prime"

	// KID is the signing key id: "catlog-<yyyymm>" of ReferenceTime (§4.5.1).
	KID = "catlog-202602"

	// LicenseTTL is D16's 180 days, in seconds.
	LicenseTTL = 180 * 24 * 60 * 60

	// StaleSkew is how far proof-stale-iat.jws sits outside the ±300 s window.
	StaleSkew = 3600

	// brotliQuality and brotliWindow pin the compressor so batch-001.br is
	// reproducible.
	brotliQuality = 5
	brotliWindow  = 22
)

// The three fixed P-256 keys. Generated once, in 2026-08, and never again:
// changing them changes every file in the vector set.
//
// serverSigningPEM signs licenses (§4.5.1). clientPEM is the credential key the
// license is bound to (§4.6). wrongPEM is a second, unlicensed client key — it
// exists only to sign proofs/proof-wrong-key.jws and is deliberately *not*
// emitted, so a consumer cannot accidentally treat it as valid material.
const (
	serverSigningPEM = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgLgsVUNGDolPdxOCm
fScJREMdXI/FQ7I2ATgjz57rR1ChRANCAASl2QU5hr8CHAz1JED9nu5ycyz+CpBm
PjJGyo5J2UyO4yQThPxwHTH5Z9pJD0Gq2IfeHhBSaXtm949jzeocrggp
-----END PRIVATE KEY-----
`
	clientPEM = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgKXfPYGLl1N9LHLwt
06fnEB9Sh1kT2Rn84nwMKqNIW4WhRANCAASs3ab7yS8CHcQG+wirHkkN4nI3BJ7k
vtpSo6mthjh9/pfGNpGeo/byFktO9ZzPHgOuYruOqWhG/5Saa0AjlXCs
-----END PRIVATE KEY-----
`
	wrongPEM = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg6+WDtJ0b5McTmcAS
Ddl0AYZCnkCj+9z4tH6L0UrALoOhRANCAASyGEbNPXQxSQvE4dETPSdStccbWClr
Ug0nJnRLkgSoX8C1gV6fGqMfimBg7rwgnPP2xKdSMEto6swofqpv65Q7
-----END PRIVATE KEY-----
`
)

// Files is the §4.10 file list, in the order Generate writes it. Consumers use
// it to check that a checkout is complete.
var Files = []string{
	"keys/server-signing.pem",
	"keys/server-jwks.json",
	"keys/client-p256.pem",
	"keys/client-pub.jwk.json",
	"keys/client.jkt.txt",
	"license/license-valid.jws",
	"license/license-expired.jws",
	"license/license-claims.json",
	"batches/batch-001.ndjson",
	"batches/batch-001.br",
	"batches/batch-001.bh.txt",
	"proofs/proof-001.jws",
	"proofs/proof-002.jws",
	"proofs/proof-bad-bh.jws",
	"proofs/proof-wrong-key.jws",
	"proofs/proof-stale-iat.jws",
	"expected/verify-results.json",
}

// Expectation is one file's expected outcome (§4.10).
type Expectation struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Expected is `expected/verify-results.json`.
//
// §4.10 describes it as "map file → {ok, error}". The map is the `files`
// member; the scalars beside it are the context a verifier cannot guess —
// above all `reference_time`, without which "expired" and "stale" are
// meaningless.
type Expected struct {
	ReferenceTime int64                  `json:"reference_time"`
	Issuer        string                 `json:"issuer"`
	HTU           string                 `json:"htu"`
	Handle        string                 `json:"handle"`
	JKT           string                 `json:"jkt"`
	Steps         string                 `json:"steps"`
	Note          string                 `json:"note"`
	Files         map[string]Expectation `json:"files"`
}

// Generate writes the whole §4.10 vector set into dir, creating it if needed.
//
// Running it twice over the same directory produces identical bytes; running it
// over a stale directory overwrites every file it owns but removes nothing, so
// a renamed vector has to be deleted by hand (and `diff -r` against a fresh
// directory is what catches that).
func Generate(dir string) error {
	serverKey, clientKey, wrongKey, err := fixedKeys()
	if err != nil {
		return err
	}

	ref := time.Unix(ReferenceTime, 0).UTC()
	files := map[string][]byte{}

	// --- keys ---------------------------------------------------------------
	files["keys/server-signing.pem"] = []byte(serverSigningPEM)
	files["keys/client-p256.pem"] = []byte(clientPEM)

	jwks, err := serverJWKS(serverKey)
	if err != nil {
		return err
	}
	files["keys/server-jwks.json"] = jwks

	clientJWK, err := cjws.PublicJWK(&clientKey.PublicKey)
	if err != nil {
		return fmt.Errorf("testvectors: client jwk: %w", err)
	}
	indented, err := indent(clientJWK)
	if err != nil {
		return err
	}
	files["keys/client-pub.jwk.json"] = indented

	jkt, err := cjws.ThumbprintPublicKey(&clientKey.PublicKey)
	if err != nil {
		return fmt.Errorf("testvectors: client thumbprint: %w", err)
	}
	files["keys/client.jkt.txt"] = line(jkt)

	// --- licenses -----------------------------------------------------------
	// `sub` is a fixed 32-byte value. In production it is HMAC(pepper, subject)
	// (D17), but the vectors have no pepper — what matters to a verifier is
	// only that it is 32 bytes of base64url.
	userKeyBytes := sha256.Sum256([]byte("catlog-testvectors-user"))
	userKey, err := keys.UserKeyFromBytes(userKeyBytes[:])
	if err != nil {
		return err
	}

	valid := authz.LicenseClaims{
		Issuer:    Issuer,
		Subject:   userKey.B64U(),
		Handle:    Handle,
		Cnf:       authz.Confirmation{JKT: jkt},
		IssuedAt:  ReferenceTime,
		ExpiresAt: ReferenceTime + LicenseTTL,
		JTI:       "lic_" + ids.String(fixedULID(ReferenceTime*1000, "license-valid")),
		Ver:       authz.LicenseVer,
	}
	validPayload, err := json.Marshal(valid)
	if err != nil {
		return fmt.Errorf("testvectors: marshal license claims: %w", err)
	}
	files["license/license-claims.json"] = append(append([]byte(nil), validPayload...), '\n')

	validJWS, err := signLicense(serverKey, validPayload)
	if err != nil {
		return err
	}
	files["license/license-valid.jws"] = line(validJWS)

	// Expired: issued 30 million seconds before the reference instant, so its
	// exp is comfortably in the past at ReferenceTime.
	expired := valid
	expired.IssuedAt = ReferenceTime - 30_000_000
	expired.ExpiresAt = expired.IssuedAt + LicenseTTL
	expired.JTI = "lic_" + ids.String(fixedULID(uint64(expired.IssuedAt)*1000, "license-expired"))
	expiredPayload, err := json.Marshal(expired)
	if err != nil {
		return fmt.Errorf("testvectors: marshal expired license claims: %w", err)
	}
	expiredJWS, err := signLicense(serverKey, expiredPayload)
	if err != nil {
		return err
	}
	files["license/license-expired.jws"] = line(expiredJWS)

	// --- batch --------------------------------------------------------------
	ndjson := batch001()
	files["batches/batch-001.ndjson"] = ndjson

	compressed, err := compress(ndjson)
	if err != nil {
		return err
	}
	files["batches/batch-001.br"] = compressed

	// `bh` covers the body *as sent* — the compressed bytes, not the NDJSON.
	bh := authz.BodyHash(compressed)
	files["batches/batch-001.bh.txt"] = line(bh)

	// --- proofs -------------------------------------------------------------
	sid := ids.String(fixedULID(ReferenceTime*1000, "stream-001"))

	proof001 := authz.ProofClaims{
		JTI:      ids.String(fixedULID(ReferenceTime*1000, "batch-001")),
		IssuedAt: ReferenceTime, HTM: "POST", HTU: HTU,
		BH: bh, SID: sid, Seq: 1,
	}
	// seq 2 re-ships the same body under a new batch id: `ph` chains to the
	// previous body's hash, which for one batch file is the same value as `bh`.
	proof002 := proof001
	proof002.JTI = ids.String(fixedULID(ReferenceTime*1000+1, "batch-002"))
	proof002.Seq = 2
	proof002.PH = bh

	badBH := proof001
	badBH.JTI = ids.String(fixedULID(ReferenceTime*1000, "batch-bad-bh"))
	badBH.BH = authz.BodyHash([]byte("not the batch that was sent"))

	wrongKeyProof := proof001
	wrongKeyProof.JTI = ids.String(fixedULID(ReferenceTime*1000, "batch-wrong-key"))

	stale := proof001
	stale.JTI = ids.String(fixedULID(ReferenceTime*1000, "batch-stale"))
	stale.IssuedAt = ReferenceTime - StaleSkew

	for rel, spec := range map[string]struct {
		claims authz.ProofClaims
		key    *ecdsa.PrivateKey
	}{
		"proofs/proof-001.jws":       {proof001, clientKey},
		"proofs/proof-002.jws":       {proof002, clientKey},
		"proofs/proof-bad-bh.jws":    {badBH, clientKey},
		"proofs/proof-stale-iat.jws": {stale, clientKey},
		// Signed by, and carrying the JWK of, a key the license was never bound
		// to: it is a perfectly valid JWS that fails at §4.5.3 step 6.
		"proofs/proof-wrong-key.jws": {wrongKeyProof, wrongKey},
	} {
		jws, err := signProof(spec.key, spec.claims)
		if err != nil {
			return err
		}
		files[rel] = line(jws)
	}

	// --- expectations -------------------------------------------------------
	expected := Expected{
		ReferenceTime: ReferenceTime,
		Issuer:        Issuer,
		HTU:           HTU,
		Handle:        Handle,
		JKT:           jkt,
		Steps:         "1-10",
		Note: "Evaluate every file at reference_time. `ok` is the result of the §4.5.3 " +
			"credential checks (steps 1-10) only; steps 11-13 depend on server state, so " +
			"proof-002 is `ok` here even though a server with no stream_state row answers 409.",
		Files: map[string]Expectation{
			"license/license-valid.jws":   {OK: true},
			"license/license-expired.jws": {OK: false, Error: authz.CodeLicenseExpired},
			"proofs/proof-001.jws":        {OK: true},
			"proofs/proof-002.jws":        {OK: true},
			"proofs/proof-bad-bh.jws":     {OK: false, Error: authz.CodeProofInvalid},
			"proofs/proof-wrong-key.jws":  {OK: false, Error: authz.CodeProofInvalid},
			"proofs/proof-stale-iat.jws":  {OK: false, Error: authz.CodeClockSkew},
		},
	}
	expectedJSON, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return fmt.Errorf("testvectors: marshal expectations: %w", err)
	}
	files["expected/verify-results.json"] = append(expectedJSON, '\n')

	// --- write --------------------------------------------------------------
	if len(files) != len(Files) {
		return fmt.Errorf("testvectors: built %d files, the §4.10 list has %d", len(files), len(Files))
	}
	for _, rel := range Files {
		content, ok := files[rel]
		if !ok {
			return fmt.Errorf("testvectors: %s was not built", rel)
		}
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), content); err != nil {
			return err
		}
	}

	// A vector set that does not verify is worse than none: catch it here
	// rather than in a C# test six weeks from now.
	return selfCheck(dir, ref)
}

// --- helpers -----------------------------------------------------------------

func fixedKeys() (server, client, wrong *ecdsa.PrivateKey, err error) {
	if server, err = cjws.ParsePrivateKeyPEM([]byte(serverSigningPEM)); err != nil {
		return nil, nil, nil, fmt.Errorf("testvectors: server key: %w", err)
	}
	if client, err = cjws.ParsePrivateKeyPEM([]byte(clientPEM)); err != nil {
		return nil, nil, nil, fmt.Errorf("testvectors: client key: %w", err)
	}
	if wrong, err = cjws.ParsePrivateKeyPEM([]byte(wrongPEM)); err != nil {
		return nil, nil, nil, fmt.Errorf("testvectors: wrong key: %w", err)
	}
	return server, client, wrong, nil
}

// serverJWKS renders the key set exactly as GET /.well-known/catlog-jwks.json
// does, by going through the production type (§4.5.1).
func serverJWKS(server *ecdsa.PrivateKey) ([]byte, error) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		keys.SigningKey{KID: KID, Key: server}.Public(),
	}}
	raw, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("testvectors: marshal jwks: %w", err)
	}
	return indent(raw)
}

func signLicense(server *ecdsa.PrivateKey, payload []byte) (string, error) {
	jws, err := cjws.SignES256Deterministic(server, payload, cjws.SignOptions{
		Type: authz.LicenseType, KeyID: KID,
	})
	if err != nil {
		return "", fmt.Errorf("testvectors: sign license: %w", err)
	}
	return jws, nil
}

func signProof(key *ecdsa.PrivateKey, claims authz.ProofClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("testvectors: marshal proof claims: %w", err)
	}
	jws, err := cjws.SignES256Deterministic(key, payload, cjws.SignOptions{
		Type: authz.ProofType, EmbedJWK: true,
	})
	if err != nil {
		return "", fmt.Errorf("testvectors: sign proof: %w", err)
	}
	return jws, nil
}

// batch001 is the golden batch: one flight's worth of §4.2 events, covering a
// null flight (session.started), a nested payload (telemetry.window) and the
// three field types the envelope carries.
func batch001() []byte {
	session := ids.String(fixedULID(ReferenceTime*1000, "session-001"))
	flight := ids.String(fixedULID(ReferenceTime*1000+10, "flight-001"))
	// A §4.1 career id: 16 lowercase Crockford base32 characters. Fixed here like
	// every other identifier in this file, so regeneration stays byte-identical.
	const career = "b7k2q9x4m0nrt3vz"

	type env struct {
		ID      string  `json:"id"`
		Type    string  `json:"type"`
		Ver     int     `json:"ver"`
		Flight  *string `json:"flight"`
		Session string  `json:"session"`
		Career  string  `json:"career"`
		SimT    float64 `json:"sim_t"`
		WallT   int64   `json:"wall_t"`
		Payload any     `json:"payload"`
	}

	agg := func(mn, mx, mean, last float64) map[string]float64 {
		return map[string]float64{"min": mn, "max": mx, "mean": mean, "last": last}
	}

	events := []env{{
		ID: ids.String(fixedULID(ReferenceTime*1000, "ev-1")), Type: "session.started", Ver: 1,
		Flight: nil, Session: session, Career: career, SimT: 0, WallT: ReferenceTime * 1000,
		Payload: map[string]any{
			"mod_ver": "0.1.0", "game_build": "2026.8.5.5168",
			"install": ids.String(fixedULID(ReferenceTime*1000, "install")),
		},
	}, {
		ID: ids.String(fixedULID(ReferenceTime*1000+1000, "ev-2")), Type: "flight.started", Ver: 1,
		Flight: &flight, Session: session, Career: career, SimT: 100.5, WallT: ReferenceTime*1000 + 1000,
		Payload: map[string]any{
			"vehicle_name": "Kitten I", "body": "earth",
			"mass_kg": 12500.5, "part_count": 24, "crew_count": 2,
		},
	}, {
		ID: ids.String(fixedULID(ReferenceTime*1000+2000, "ev-3")), Type: "telemetry.window", Ver: 1,
		Flight: &flight, Session: session, Career: career, SimT: 130.5, WallT: ReferenceTime*1000 + 2000,
		Payload: map[string]any{
			"t0_sim": 100.5, "t1_sim": 130.5, "n": 60, "body": "earth",
			"alt_m":            agg(0, 42000.25, 21000.125, 42000.25),
			"surface_speed_ms": agg(0, 1450.5, 725.25, 1450.5),
			"orbital_speed_ms": agg(0, 1600.75, 800.375, 1600.75),
			"accel_ms2":        agg(0, 29.4, 14.7, 12.25),
			"peak_g":           3.5,
			"max_q_pa":         38000.5,
			"mass_kg_last":     9800.25,
		},
	}, {
		ID: ids.String(fixedULID(ReferenceTime*1000+3000, "ev-4")), Type: "vehicle.impact", Ver: 1,
		Flight: &flight, Session: session, Career: career, SimT: 214.75, WallT: ReferenceTime*1000 + 3000,
		Payload: map[string]any{
			"speed_ms": 214.5, "energy_j": 2.25e8, "survived": true,
			"launch_pad": false, "body": "duna", "crew_count": 2,
		},
	}, {
		ID: ids.String(fixedULID(ReferenceTime*1000+4000, "ev-5")), Type: "flight.ended", Ver: 1,
		Flight: &flight, Session: session, Career: career, SimT: 300, WallT: ReferenceTime*1000 + 4000,
		Payload: map[string]any{"reason": "recovered", "crew_count": 2},
	}}

	var buf bytes.Buffer
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			panic(err) // impossible: every field is a plain Go value
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := brotli.NewWriterOptions(&buf, brotli.WriterOptions{Quality: brotliQuality, LGWin: brotliWindow})
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("testvectors: brotli write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("testvectors: brotli close: %w", err)
	}
	return buf.Bytes(), nil
}

// fixedULID builds a valid ULID from a fixed timestamp and ten entropy bytes
// derived from a label. Deterministic, and still a real ULID: the timestamp
// prefix decodes and the alphabet is Crockford base32.
func fixedULID(ms uint64, label string) ids.ID {
	var id ids.ID
	if err := id.SetTime(ms); err != nil {
		panic(err) // ms is a constant; overflow is a programming error
	}
	sum := sha256.Sum256([]byte("catlog-testvectors:" + label))
	copy(id[6:], sum[:10])
	return id
}

// line renders a single-value text file: the value plus one newline. Consumers
// must trim trailing whitespace.
func line(s string) []byte { return []byte(s + "\n") }

// indent pretty-prints JSON with two spaces and a trailing newline. Formatting
// is irrelevant to any of the cryptography here — RFC 7638 canonicalizes the
// JWK itself — so the files are formatted for humans reading a diff.
func indent(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("testvectors: indent json: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("testvectors: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("testvectors: write %s: %w", path, err)
	}
	return nil
}
