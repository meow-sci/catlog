package testvectors

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// committedDir is contracts/testdata, relative to this package.
const committedDir = "../../../contracts/testdata"

// TestGenerateIsByteIdentical is the §4.10 reproducibility contract: two runs
// into two directories must produce identical bytes, and a third run over an
// existing directory must change nothing.
func TestGenerateIsByteIdentical(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	if err := Generate(first); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if err := Generate(second); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	compareTrees(t, first, second)

	// Regenerating in place is the `make testvectors` case: it must leave a
	// clean working tree.
	before := readTree(t, first)
	if err := Generate(first); err != nil {
		t.Fatalf("regenerate in place: %v", err)
	}
	after := readTree(t, first)
	for rel, want := range before {
		if got := after[rel]; string(got) != string(want) {
			t.Errorf("%s changed when regenerated in place", rel)
		}
	}
}

// TestCommittedVectorsAreCurrent fails if contracts/testdata has drifted from
// the generator — which is the only way a C# suite could end up testing against
// vectors no Go code produces any more.
func TestCommittedVectorsAreCurrent(t *testing.T) {
	if _, err := os.Stat(committedDir); os.IsNotExist(err) {
		t.Skip("contracts/testdata is not present in this checkout")
	}

	fresh := t.TempDir()
	if err := Generate(fresh); err != nil {
		t.Fatalf("generate: %v", err)
	}
	compareTrees(t, fresh, committedDir)
}

// TestCommittedVectorsVerify runs the self-check over the committed directory,
// so a hand-edited vector is caught even if the generator agrees with it.
func TestCommittedVectorsVerify(t *testing.T) {
	if _, err := os.Stat(committedDir); os.IsNotExist(err) {
		t.Skip("contracts/testdata is not present in this checkout")
	}
	if err := Verify(committedDir, time.Unix(ReferenceTime, 0).UTC()); err != nil {
		t.Fatalf("committed vectors do not verify: %v", err)
	}
}

// TestVectorsPassTheRealChain is the point of the whole exercise: the vectors
// are fed to the actual §4.5.3 verifier, with a real events.db behind it, and
// every outcome must match expected/verify-results.json.
//
// The C# suite (WP6/WP7) mirrors this test against the same files.
func TestVectorsPassTheRealChain(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}

	ref := time.Unix(ReferenceTime, 0).UTC()
	ks := vectorKeys(t, dir)
	events := testutil.MemEvents(t)

	// The credential rows the chain's step 5 needs.
	userKeyBytes := sha256.Sum256([]byte("catlog-testvectors-user"))
	userKey, err := keys.UserKeyFromBytes(userKeyBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := events.EnsurePlayer(t.Context(), nil, userKey, "dev", ref.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := events.ClaimHandle(t.Context(), playerID, Handle, ref.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	jkt := readText(t, dir, "keys/client.jkt.txt")
	if err := events.InsertCredential(t.Context(), nil, store.Credential{
		JKT: jkt, PlayerID: playerID, Handle: Handle, LicenseJTI: "lic_vectors",
		IssuedAt: ref.UnixMilli(), ExpiresAt: ref.Add(180 * 24 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	v := authz.New(authz.Config{
		Issuer:      Issuer,
		AcceptedHTU: []string{HTU},
		// A generous burst: this test replays several proofs at one instant.
		RatePerSecond: 1, Burst: 100,
	}, ks, events, authz.NewDenyList())
	v.SetClock(func() time.Time { return ref })

	license := readText(t, dir, "license/license-valid.jws")
	body, err := os.ReadFile(filepath.Join(dir, "batches", "batch-001.br"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		file    string
		license string
		wantErr string // "" means the chain must accept it
	}{
		{file: "proofs/proof-001.jws", license: license},
		{file: "proofs/proof-002.jws", license: license},
		{file: "proofs/proof-bad-bh.jws", license: license, wantErr: authz.CodeProofInvalid},
		{file: "proofs/proof-wrong-key.jws", license: license, wantErr: authz.CodeProofInvalid},
		{file: "proofs/proof-stale-iat.jws", license: license, wantErr: authz.CodeClockSkew},
		{file: "proofs/proof-001.jws", license: readText(t, dir, "license/license-expired.jws"), wantErr: authz.CodeLicenseExpired},
	}

	for _, tc := range cases {
		name := tc.file
		if tc.wantErr == authz.CodeLicenseExpired {
			name = "license-expired + " + name
		}
		t.Run(name, func(t *testing.T) {
			proof := readText(t, dir, tc.file)
			res, aerr := v.Verify(t.Context(), authz.Request{License: tc.license, Proof: proof})

			// bad-bh survives the chain and fails at step 10, which needs the
			// body — exactly where a real request would fail.
			if aerr == nil && res != nil {
				if e := res.CheckBodyHash(body); e != nil {
					aerr = e
				}
			}

			if tc.wantErr == "" {
				if aerr != nil {
					t.Fatalf("the chain rejected a valid vector: %v", aerr)
				}
				return
			}
			if aerr == nil {
				t.Fatalf("the chain accepted %s, which must fail with %s", tc.file, tc.wantErr)
			}
			if aerr.Code != tc.wantErr {
				t.Errorf("code = %s, want %s (detail: %s)", aerr.Code, tc.wantErr, aerr.Detail)
			}
		})
	}
}

// TestVerifyDetectsTampering proves the self-check is worth running: flip one
// byte of a signature and it must notice.
func TestVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}

	path := filepath.Join(dir, "license", "license-valid.jws")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.TrimSpace(string(raw)))
	// Flip a character inside the signature segment.
	tampered[len(tampered)-2] ^= 0x01
	if err := os.WriteFile(path, append(tampered, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Verify(dir, time.Unix(ReferenceTime, 0).UTC()); err == nil {
		t.Fatal("Verify accepted a tampered license")
	}
}

// vectorKeys builds a keys.Set whose active signing key is the vectors' server
// key under the vectors' kid, by laying out a real §3 keys directory.
func vectorKeys(t *testing.T, dir string) *keys.Set {
	t.Helper()

	keyDir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(filepath.Join(dir, "keys", "server-signing.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, keys.SigningFile), pem, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "license-signing.kid"), []byte(KID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	set, err := keys.LoadOrCreate(keyDir)
	if err != nil {
		t.Fatalf("load vector keys: %v", err)
	}
	if set.Signing.KID != KID {
		t.Fatalf("kid = %q, want %q", set.Signing.KID, KID)
	}
	return set
}

func readText(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.TrimSpace(string(b))
}

// readTree reads every file under root, keyed by slash-separated relative path.
func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// compareTrees asserts two directories hold exactly the same files with exactly
// the same bytes — the Go equivalent of the `diff -r` in the DoD.
func compareTrees(t *testing.T, a, b string) {
	t.Helper()

	left, right := readTree(t, a), readTree(t, b)
	for rel, want := range left {
		got, ok := right[rel]
		if !ok {
			t.Errorf("%s is missing from %s", rel, b)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s differs between %s and %s", rel, a, b)
		}
	}
	for rel := range right {
		if _, ok := left[rel]; !ok {
			t.Errorf("%s is present in %s but not in %s", rel, b, a)
		}
	}

	// And the tree is exactly the §4.10 file list — no strays, nothing missing.
	if len(left) != len(Files) {
		t.Errorf("%s holds %d files, the §4.10 list has %d", a, len(left), len(Files))
	}
	for _, rel := range Files {
		if _, ok := left[rel]; !ok {
			t.Errorf("%s is missing %s", a, rel)
		}
	}
}

// TestSignaturesAreNotAllTheSame is a sanity check on the deterministic signer:
// determinism must come from RFC 6979, not from a constant nonce or a signature
// that ignores its input.
func TestSignaturesAreNotAllTheSame(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}

	seen := map[string]string{}
	for _, rel := range []string{
		"proofs/proof-001.jws", "proofs/proof-002.jws", "proofs/proof-bad-bh.jws",
		"proofs/proof-wrong-key.jws", "proofs/proof-stale-iat.jws",
	} {
		jws := readText(t, dir, rel)
		sig := jws[strings.LastIndex(jws, ".")+1:]
		if prev, dup := seen[sig]; dup {
			t.Errorf("%s and %s share a signature", prev, rel)
		}
		seen[sig] = rel
	}

	// And the deterministic signer still produces verifiable JOSE.
	key, err := cjws.ParsePrivateKeyPEM([]byte(clientPEM))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cjws.VerifyES256(readText(t, dir, "proofs/proof-001.jws"), &key.PublicKey); err != nil {
		t.Errorf("proof-001 does not verify under the client key: %v", err)
	}
}
