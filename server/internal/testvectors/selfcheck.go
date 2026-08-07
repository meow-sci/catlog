package testvectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
)

// Verify re-reads a generated vector directory and checks every claim the set
// makes about itself, at the instant at.
//
// [Generate] runs it before returning, so a broken vector set can never be
// committed; the Go test suite runs it against the committed directory, and the
// C# suite mirrors it (§4.10). It deliberately needs no database: it checks the
// cryptography and the claim arithmetic, which is the part both languages must
// agree on.
func Verify(dir string, at time.Time) error {
	read := func(rel string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("testvectors: read %s: %w", rel, err)
		}
		return b, nil
	}
	text := func(rel string) (string, error) {
		b, err := read(rel)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}

	// Every promised file is present.
	for _, rel := range Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("testvectors: %s is missing: %w", rel, err)
		}
	}

	serverPEM, err := read("keys/server-signing.pem")
	if err != nil {
		return err
	}
	serverKey, err := cjws.ParsePrivateKeyPEM(serverPEM)
	if err != nil {
		return fmt.Errorf("testvectors: server-signing.pem: %w", err)
	}
	clientPEMBytes, err := read("keys/client-p256.pem")
	if err != nil {
		return err
	}
	clientKey, err := cjws.ParsePrivateKeyPEM(clientPEMBytes)
	if err != nil {
		return fmt.Errorf("testvectors: client-p256.pem: %w", err)
	}

	// The thumbprint file is the one the C# suite must reproduce from the JWK.
	wantJKT, err := text("keys/client.jkt.txt")
	if err != nil {
		return err
	}
	gotJKT, err := cjws.ThumbprintPublicKey(&clientKey.PublicKey)
	if err != nil {
		return err
	}
	if gotJKT != wantJKT {
		return fmt.Errorf("testvectors: client.jkt.txt is %q, the key thumbprints to %q", wantJKT, gotJKT)
	}
	jwkFile, err := read("keys/client-pub.jwk.json")
	if err != nil {
		return err
	}
	jwkPub, err := cjws.ParsePublicJWK(jwkFile)
	if err != nil {
		return fmt.Errorf("testvectors: client-pub.jwk.json: %w", err)
	}
	if fromJWK, err := cjws.ThumbprintPublicKey(jwkPub); err != nil {
		return err
	} else if fromJWK != wantJKT {
		return fmt.Errorf("testvectors: the published JWK thumbprints to %q, not %q", fromJWK, wantJKT)
	}

	// The batch and its hashes.
	ndjson, err := read("batches/batch-001.ndjson")
	if err != nil {
		return err
	}
	compressed, err := read("batches/batch-001.br")
	if err != nil {
		return err
	}
	roundTrip, err := io.ReadAll(brotli.NewReader(bytes.NewReader(compressed)))
	if err != nil {
		return fmt.Errorf("testvectors: batch-001.br does not decompress: %w", err)
	}
	if !bytes.Equal(roundTrip, ndjson) {
		return errors.New("testvectors: batch-001.br does not decompress to batch-001.ndjson")
	}
	wantBH, err := text("batches/batch-001.bh.txt")
	if err != nil {
		return err
	}
	if got := authz.BodyHash(compressed); got != wantBH {
		return fmt.Errorf("testvectors: batch-001.bh.txt is %q, the body hashes to %q", wantBH, got)
	}

	// Licenses: signature, then the claim arithmetic at `at`.
	licenseClaims := func(rel string) (authz.LicenseClaims, error) {
		jws, err := text(rel)
		if err != nil {
			return authz.LicenseClaims{}, err
		}
		payload, err := cjws.VerifyES256(jws, &serverKey.PublicKey)
		if err != nil {
			return authz.LicenseClaims{}, fmt.Errorf("testvectors: %s does not verify: %w", rel, err)
		}
		var c authz.LicenseClaims
		if err := json.Unmarshal(payload, &c); err != nil {
			return authz.LicenseClaims{}, fmt.Errorf("testvectors: %s claims: %w", rel, err)
		}
		return c, nil
	}

	valid, err := licenseClaims("license/license-valid.jws")
	if err != nil {
		return err
	}
	if valid.Expired(at) {
		return errors.New("testvectors: license-valid.jws is expired at the reference time")
	}
	if valid.Issuer != Issuer || valid.Handle != Handle || valid.Ver != authz.LicenseVer || valid.Cnf.JKT != wantJKT {
		return fmt.Errorf("testvectors: license-valid.jws claims are wrong: %+v", valid)
	}
	expired, err := licenseClaims("license/license-expired.jws")
	if err != nil {
		return err
	}
	if !expired.Expired(at) {
		return errors.New("testvectors: license-expired.jws is not expired at the reference time")
	}

	// license-claims.json is the exact signed payload.
	claimsFile, err := read("license/license-claims.json")
	if err != nil {
		return err
	}
	validJWS, err := text("license/license-valid.jws")
	if err != nil {
		return err
	}
	_, payload, err := cjws.ParseCompactUnverified(validJWS)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimRight(claimsFile, "\n"), payload) {
		return errors.New("testvectors: license-claims.json is not the license-valid.jws payload")
	}

	// Proofs: each must fail exactly where expected/verify-results.json says.
	expectedRaw, err := read("expected/verify-results.json")
	if err != nil {
		return err
	}
	var expected Expected
	if err := json.Unmarshal(expectedRaw, &expected); err != nil {
		return fmt.Errorf("testvectors: verify-results.json: %w", err)
	}
	if expected.ReferenceTime != ReferenceTime || expected.JKT != wantJKT {
		return errors.New("testvectors: verify-results.json disagrees with the vectors it describes")
	}

	for _, rel := range []string{
		"proofs/proof-001.jws", "proofs/proof-002.jws", "proofs/proof-bad-bh.jws",
		"proofs/proof-wrong-key.jws", "proofs/proof-stale-iat.jws",
	} {
		want, ok := expected.Files[rel]
		if !ok {
			return fmt.Errorf("testvectors: %s has no entry in verify-results.json", rel)
		}
		got, err := checkProof(dir, rel, wantJKT, wantBH, at)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("testvectors: %s verifies as %+v, verify-results.json says %+v", rel, got, want)
		}
	}
	return nil
}

// checkProof replays §4.5.3 steps 6–10 over one proof file, without a database:
// the embedded key, the signature, the HTTP binding, the skew window and the
// body hash.
func checkProof(dir, rel, jkt, bh string, at time.Time) (Expectation, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return Expectation{}, fmt.Errorf("testvectors: read %s: %w", rel, err)
	}
	jws := strings.TrimSpace(string(raw))

	header, payload, err := cjws.ParseCompactUnverified(jws)
	if err != nil {
		return Expectation{}, fmt.Errorf("testvectors: %s: %w", rel, err)
	}
	pub, err := cjws.PublicKeyOf(header.JWK)
	if err != nil {
		return Expectation{}, fmt.Errorf("testvectors: %s embedded jwk: %w", rel, err)
	}
	got, err := cjws.ThumbprintPublicKey(pub)
	if err != nil {
		return Expectation{}, err
	}
	if got != jkt { // step 6
		return Expectation{Error: authz.CodeProofInvalid}, nil
	}
	if _, err := cjws.VerifyES256(jws, pub); err != nil { // step 7
		return Expectation{Error: authz.CodeProofInvalid}, nil
	}

	var claims authz.ProofClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Expectation{}, fmt.Errorf("testvectors: %s claims: %w", rel, err)
	}
	if claims.HTM != "POST" || claims.HTU != HTU { // step 8
		return Expectation{Error: authz.CodeProofInvalid}, nil
	}
	skew := at.Unix() - claims.IssuedAt
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > authz.MaxSkew { // step 8
		return Expectation{Error: authz.CodeClockSkew}, nil
	}
	if claims.BH != bh { // step 10
		return Expectation{Error: authz.CodeProofInvalid}, nil
	}
	return Expectation{OK: true}, nil
}

// selfCheck is Verify, run by Generate over what it just wrote.
func selfCheck(dir string, at time.Time) error {
	if err := Verify(dir, at); err != nil {
		return fmt.Errorf("generated vectors failed their own check: %w", err)
	}
	return nil
}
