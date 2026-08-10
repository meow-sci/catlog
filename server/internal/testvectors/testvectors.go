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

// batch001 is the golden batch: three flights' worth of §4.2 events, one line
// each, at the versions and payload shapes the wire is at today.
//
// It is a *shape* fixture before it is a narrative one. Every registry type
// appears at least once, and the lines are chosen so that the shapes a reader
// can get wrong are all pinned somewhere:
//
//   - the envelope's three `flight` cases — always-null (`session.started`,
//     `kitten.eva_end`, `roster.snapshot`), always-present, and the conditional
//     null of a `vehicle.docked` whose `other_flight` peeked at nothing;
//   - omit-don't-zero, in both directions and on the same event type: the first
//     `telemetry.window` carries `peak_g`, `max_q_pa` and the `radar_alt_m`
//     aggregate, the second (spent in orbit under 1000× warp) carries none of
//     the three. A consumer that reads an absent optional as 0 passes on one
//     line and fails on the other;
//   - `lat`/`lon` present on `flight.started`, `vehicle.landed`, `vehicle.impact`
//     and `flight.ended`, and absent on the uncrewed probe's `flight.started`,
//     on `vehicle.rud` and on the safety-net `flight.ended`. A latitude of 0 is
//     a place, so the key is left out rather than zeroed;
//   - the `kids` array populated (2 kittens) and empty (an uncrewed probe) —
//     `[]` is "nobody aboard", a missing key is "the mod could not say";
//   - `stage_count` 0, which unlike a latitude *is* a real value;
//   - a nested array of objects (`roster.snapshot.kittens`) and a nested
//     optional object (`telemetry.window.radar_alt_m`);
//   - a JSON `null` inside a payload (`vehicle.docked.other_flight`).
//
// Determinism: every identifier is a [fixedULID] of a constant, every `wall_t`
// is derived from the line's index, and the payloads are `map[string]any`, which
// encoding/json emits in sorted key order. Nothing here reads a clock.
func batch001() []byte {
	session := ids.String(fixedULID(ReferenceTime*1000, "session-001"))
	// Three flights and one EVA. `mission` is the crewed flight everything
	// interesting happens on; `probe` splits off it and is closed by the mod's
	// silent-removal safety net; `wreck` is a flagged flight that is lost.
	mission := ids.String(fixedULID(ReferenceTime*1000+10, "flight-001"))
	wreck := ids.String(fixedULID(ReferenceTime*1000+20, "flight-002"))
	probe := ids.String(fixedULID(ReferenceTime*1000+30, "flight-003"))
	eva := ids.String(fixedULID(ReferenceTime*1000+40, "flight-eva"))

	// A §4.1 career id: 16 lowercase Crockford base32 characters. Fixed here like
	// every other identifier in this file, so regeneration stays byte-identical.
	const career = "b7k2q9x4m0nrt3vz"
	// Two `kid`s, the same 16-character Crockford shape a career id has (§4.7).
	const (
		kidAce    = "c3n7v8k1p5q9r2s6"
		kidPepper = "d4m8w0j2t6y1z5b3"
	)

	type spec struct {
		label   string
		typ     string
		ver     int
		flight  *string
		simT    float64
		payload map[string]any
	}

	agg := func(mn, mx, mean, last float64) map[string]float64 {
		return map[string]float64{"min": mn, "max": mx, "mean": mean, "last": last}
	}

	specs := []spec{{
		label: "ev-system-complete", typ: "system.discovered", ver: 1, flight: nil, simT: 0,
		payload: map[string]any{
			"system": "01kittensol", "id": "Sol", "name": "Sol", "home": "earth",
			"bodies": 2, "complete": true,
		},
	}, {
		label: "ev-system-root", typ: "system.body", ver: 1, flight: nil, simT: 0,
		payload: map[string]any{
			"system": "01kittensol", "body": "sol", "name": "Sol", "class": "StellarBody",
			"kind": "star", "rank": 0, "radius_m": 696340000.0, "mass_kg": 1.98847e30,
			"soi_m": 0.0, "atmo_m": 0.0, "ocean_m": 0.0, "angvel": 2.865e-6,
			"axis":          map[string]any{"x": 0.0, "y": 1.0, "z": 0.0},
			"ccf_to_cce_t0": map[string]any{"x": 0.0, "y": 0.0, "z": 0.0, "w": 1.0},
		},
	}, {
		label: "ev-system-orbit", typ: "system.body", ver: 1, flight: nil, simT: 0,
		payload: map[string]any{
			"system": "01kittensol", "body": "earth", "name": "Earth", "class": "TerrestrialBody",
			"kind": "planet", "rank": 1, "parent": "sol", "radius_m": 6371000.0,
			"mass_kg": 5.9722e24, "soi_m": 924000000.0, "atmo_m": 100000.0, "ocean_m": 0.0,
			"angvel": 7.2921159e-5, "axis": map[string]any{"x": 0.0, "y": 1.0, "z": 0.0},
			"ccf_to_cce_t0": map[string]any{"x": 0.0, "y": 0.0, "z": 0.0, "w": 1.0},
			"sma_m":         149597870700.0, "ecc": 0.0167086, "inc_deg": 0.00005,
			"lan_deg": -11.26064, "argp_deg": 114.20783, "t_pe": -1234.5, "period_s": 31558149.8,
		},
	}, {
		label: "ev-system-incomplete", typ: "system.discovered", ver: 1, flight: nil, simT: 0,
		payload: map[string]any{
			"system": "01kittenbad", "id": "Generated", "name": "Generated", "home": "origin",
			"bodies": 5001, "complete": false,
		},
	}, {
		label: "ev-1", typ: "session.started", ver: 1, flight: nil, simT: 0,
		payload: map[string]any{
			"mod_ver": "0.1.0", "game_build": "2026.8.5.5168",
			"install": ids.String(fixedULID(ReferenceTime*1000, "install")),
		},
	}, {
		// Crewed launch: `kids` populated, `lat`/`lon` readable, a real stage count.
		label: "ev-2", typ: "flight.started", ver: 1, flight: &mission, simT: 100.5,
		payload: map[string]any{
			"vehicle_name": "Kitten I", "body": "earth",
			"mass_kg": 12500.5, "part_count": 24, "crew_count": 2,
			"kids": []string{kidAce, kidPepper}, "stage_count": 3,
			"lat": 28.5721, "lon": -80.648,
		},
	}, {
		// radar_alt_m PRESENT: the game had a terrain sample under the pad.
		label: "ev-3", typ: "vehicle.situation", ver: 1, flight: &mission, simT: 102.5,
		payload: map[string]any{
			"from": "landed", "to": "maneuvering", "body": "earth",
			"altitude_m": 12.5, "surface_speed_ms": 3.25, "orbital_speed_ms": 465.1,
			"radar_alt_m": 2.5,
		},
	}, {
		label: "ev-4", typ: "vehicle.staging", ver: 1, flight: &mission, simT: 103,
		payload: map[string]any{"stage_index": 0},
	}, {
		label: "ev-5", typ: "engine.ignition", ver: 1, flight: &mission, simT: 103.25,
		payload: map[string]any{"engine": "kitten_booster_v1", "count": 4},
	}, {
		// A full 60-sample window under full physics: every optional PRESENT,
		// including the radar_alt_m aggregate.
		label: "ev-6", typ: "telemetry.window", ver: 1, flight: &mission, simT: 130.5,
		payload: map[string]any{
			"t0_sim": 100.5, "t1_sim": 130.5, "n": 60, "body": "earth",
			"alt_m":            agg(0, 42000.25, 21000.125, 42000.25),
			"surface_speed_ms": agg(0, 1450.5, 725.25, 1450.5),
			"orbital_speed_ms": agg(0, 1600.75, 800.375, 1600.75),
			"accel_ms2":        agg(0, 29.4, 14.7, 12.25),
			"peak_g":           3.5,
			"max_q_pa":         38000.5,
			"mass_kg_last":     9800.25,
			"radar_alt_m":      agg(0, 41880.5, 20940.25, 41880.5),
			"warp_max":         1,
		},
	}, {
		label: "ev-7", typ: "vehicle.atmosphere", ver: 1, flight: &mission, simT: 145,
		payload: map[string]any{
			"dir": "exited", "body": "earth", "speed_ms": 1850.5, "dyn_pressure_pa": 120.25,
		},
	}, {
		label: "ev-8", typ: "engine.shutdown", ver: 1, flight: &mission, simT: 168.5,
		payload: map[string]any{"engine": "kitten_booster_v1", "count": 4},
	}, {
		label: "ev-9", typ: "vehicle.orbit", ver: 1, flight: &mission, simT: 172.25,
		payload: map[string]any{
			"phase": "achieved", "body": "earth",
			"ap_m": 185000.5, "pe_m": 172400.25, "ecc": 0.0034, "inc_deg": 28.58,
			"mass_kg": 4820.75,
		},
	}, {
		// The same type as ev-6 with every optional ABSENT: on rails at 1000×
		// warp there is no StructuralLoad and no terrain below, and `n` is 3
		// rather than 60 because the window spans 30 *sim* seconds.
		label: "ev-10", typ: "telemetry.window", ver: 1, flight: &mission, simT: 200.5,
		payload: map[string]any{
			"t0_sim": 170.5, "t1_sim": 200.5, "n": 3, "body": "earth",
			"alt_m":            agg(185010.5, 186220.75, 185615.625, 186220.75),
			"surface_speed_ms": agg(7602.25, 7640.5, 7621.375, 7640.5),
			"orbital_speed_ms": agg(7660.5, 7699.25, 7679.875, 7699.25),
			"accel_ms2":        agg(0, 0.02, 0.01, 0.02),
			"mass_kg_last":     4820.75,
			"warp_max":         1000,
		},
	}, {
		label: "ev-11", typ: "vehicle.soi", ver: 1, flight: &mission, simT: 205,
		payload: map[string]any{"from_body": "earth", "to_body": "duna"},
	}, {
		// A documented legal shape neither side pinned before: the peek found no
		// open flight for the other vehicle, so the key is present and null.
		label: "ev-12", typ: "vehicle.docked", ver: 1, flight: &mission, simT: 210,
		payload: map[string]any{"other_flight": nil},
	}, {
		// The EVA kitten's own flight, minted by the EVA rather than by a
		// flight.started (§ kitten.eva_start).
		label: "ev-13", typ: "kitten.eva_start", ver: 1, flight: &eva, simT: 215,
		payload: map[string]any{"kid": kidAce, "name": "Ace"},
	}, {
		// A tumble that names the flight it happened on.
		label: "ev-14", typ: "kitten.tumble", ver: 1, flight: &eva, simT: 218.5,
		payload: map[string]any{"kid": kidAce, "name": "Ace", "speed_ms": 4.25, "body": "duna"},
	}, {
		// `flight` is explicitly null here, asymmetrically with eva_start.
		label: "ev-15", typ: "kitten.eva_end", ver: 1, flight: nil, simT: 232.75,
		payload: map[string]any{"kid": kidAce, "name": "Ace", "duration_s": 17.75},
	}, {
		// The uncrewed probe that splits off: `kids` EMPTY, `stage_count` 0 (a
		// real value), `lat`/`lon` ABSENT (not readable, and 0 would be a place).
		label: "ev-16", typ: "flight.started", ver: 1, flight: &probe, simT: 240,
		payload: map[string]any{
			"vehicle_name": "Kitten I Probe", "body": "duna",
			"mass_kg": 850.75, "part_count": 6, "crew_count": 0,
			"kids": []string{}, "stage_count": 0,
		},
	}, {
		label: "ev-17", typ: "vehicle.undocked", ver: 1, flight: &mission, simT: 240.5,
		payload: map[string]any{"other_flight": probe},
	}, {
		label: "ev-18", typ: "engine.flameout", ver: 1, flight: &mission, simT: 248.5,
		payload: map[string]any{"engine": "kitten_lander_v2", "count": 1},
	}, {
		// §5.6's worked example of biggest_lithobrake_survived, with the
		// position keys present.
		label: "ev-19", typ: "vehicle.impact", ver: 1, flight: &mission, simT: 258.25,
		payload: map[string]any{
			"speed_ms": 214.5, "energy_j": 2.25e8, "survived": true,
			"launch_pad": false, "body": "duna", "crew_count": 2,
			"lat": 12.4405, "lon": -74.8201,
		},
	}, {
		// radar_alt_m ABSENT on the type that usually carries it — the same key
		// ev-3 carries, so a consumer cannot pass by assuming either shape.
		label: "ev-20", typ: "vehicle.landed", ver: 1, flight: &mission, simT: 262.5,
		payload: map[string]any{
			"body": "duna", "vertical_speed_ms": 4.75, "horizontal_speed_ms": 1.25,
			"crew_count": 2, "survived": true,
			"lat": 12.4405, "lon": -74.8201,
		},
	}, {
		label: "ev-21", typ: "flight.ended", ver: 1, flight: &mission, simT: 300,
		payload: map[string]any{
			"reason": "recovered", "crew_count": 2,
			"kids": []string{kidAce, kidPepper}, "body": "duna",
			"lat": 12.4405, "lon": -74.8201,
		},
	}, {
		// The silent-removal safety net: no vehicle left to read, so `body` is
		// the literal "unknown", `kids` is `[]`, `crew_count` is 0 and the
		// position keys are absent (§ flight.ended).
		label: "ev-22", typ: "flight.ended", ver: 1, flight: &probe, simT: 305,
		payload: map[string]any{
			"reason": "despawned", "crew_count": 0,
			"kids": []string{}, "body": "unknown",
		},
	}, {
		label: "ev-23", typ: "flight.flagged", ver: 1, flight: &wreck, simT: 312.5,
		payload: map[string]any{"flag": "tuning", "detail": "session-wide flag"},
	}, {
		// lat/lon ABSENT: the destruction prefix could not place the vehicle.
		// peak_g and peak_q_pa are NOT optional here — they come off the
		// destruction event and are written as 0 rather than omitted.
		label: "ev-24", typ: "vehicle.rud", ver: 1, flight: &wreck, simT: 318.25,
		payload: map[string]any{
			"cause": "ground_impact", "peak_g": 12.5, "peak_q_pa": 74500.25,
			"speed_ms": 312.75, "altitude_m": 0.5, "body": "earth", "crew_count": 1,
		},
	}, {
		// A KIA that names the flight it happened on.
		// The player scuttled the wreck, which is the only path that sets Kia.
		label: "ev-25", typ: "kitten.kia", ver: 1, flight: &wreck, simT: 320.5,
		payload: map[string]any{"kid": kidPepper, "name": "Pepper", "context": "manual_destroy"},
	}, {
		label: "ev-26", typ: "flight.ended", ver: 1, flight: &wreck, simT: 321,
		payload: map[string]any{
			"reason": "destroyed", "crew_count": 1,
			"kids": []string{kidPepper}, "body": "earth",
			"lat": 28.6084, "lon": -80.6043,
		},
	}, {
		// A nested array of objects, and the third always-null `flight`.
		label: "ev-27", typ: "roster.snapshot", ver: 1, flight: nil, simT: 600,
		payload: map[string]any{"kittens": []map[string]any{{
			"kid": kidAce, "name": "Ace", "travelled_m": 4.2e7,
			"fastest_ms": 29800.5, "missions": 3, "mission_time_s": 18450.25, "kia": false,
		}, {
			"kid": kidPepper, "name": "Pepper", "travelled_m": 1.85e7,
			"fastest_ms": 29790.25, "missions": 2, "mission_time_s": 9600.5, "kia": true,
		}}},
	}}

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

	var buf bytes.Buffer
	for i, s := range specs {
		// One second of wall time per line, so `id` and `wall_t` are derived
		// from the line's position and nothing else.
		wall := int64(ReferenceTime)*1000 + int64(i)*1000
		b, err := json.Marshal(env{
			ID: ids.String(fixedULID(uint64(wall), s.label)), Type: s.typ, Ver: s.ver,
			Flight: s.flight, Session: session, Career: career,
			SimT: s.simT, WallT: wall, Payload: s.payload,
		})
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
