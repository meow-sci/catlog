package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/cjws"
)

func TestLoadOrCreateCreatesAllThreeSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")

	missing, err := Created(dir)
	if err != nil {
		t.Fatalf("Created: %v", err)
	}
	if len(missing) != 3 {
		t.Errorf("missing = %v, want all three", missing)
	}

	set, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	for _, name := range []string{SigningFile, SessionFile, PepperFile, kidFile} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := fi.Mode().Perm(); mode != filePerm {
			t.Errorf("%s mode = %#o, want %#o", name, mode, filePerm)
		}
	}
	if fi, err := os.Stat(dir); err != nil {
		t.Fatalf("stat keys dir: %v", err)
	} else if mode := fi.Mode().Perm(); mode != dirPerm {
		t.Errorf("keys dir mode = %#o, want %#o", mode, dirPerm)
	}

	if set.Signing.Key.Curve != elliptic.P256() {
		t.Error("signing key is not P-256")
	}
	if want := KID(time.Now()); set.Signing.KID != want {
		t.Errorf("kid = %q, want %q", set.Signing.KID, want)
	}
	if got := len(set.SessionKey()); got != SecretLen {
		t.Errorf("session key len = %d, want %d", got, SecretLen)
	}

	if missing, err = Created(dir); err != nil {
		t.Fatalf("Created after create: %v", err)
	} else if len(missing) != 0 {
		t.Errorf("missing after create = %v, want none", missing)
	}
}

// TestLoadOrCreateIsIdempotent is the property catlogd depends on: every start
// calls LoadOrCreate, and it must never rotate a key out from under live
// licenses and sessions.
func TestLoadOrCreateIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}

	if !first.Signing.Key.Equal(second.Signing.Key) {
		t.Error("signing key changed on reload")
	}
	if first.Signing.KID != second.Signing.KID {
		t.Errorf("kid changed on reload: %q -> %q", first.Signing.KID, second.Signing.KID)
	}
	if string(first.SessionKey()) != string(second.SessionKey()) {
		t.Error("session key changed on reload")
	}
	if first.UserKey("discord", "12345") != second.UserKey("discord", "12345") {
		t.Error("pepper changed on reload (every user_key would be orphaned)")
	}
}

func TestSigningKeyRoundTripsThroughPEM(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, SigningFile))
	if err != nil {
		t.Fatalf("read pem: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("signing key is not a PKCS#8 PEM: %q", raw)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse PKCS#8: %v", err)
	}
	if !parsed.(*ecdsa.PrivateKey).Equal(set.Signing.Key) {
		t.Error("PEM on disk is a different key than the loaded one")
	}

	// The loaded key must actually sign a license-shaped JWS the JWKS verifies.
	compact, err := cjws.SignES256(set.Signing.Key, []byte(`{"ver":1}`),
		cjws.SignOptions{Type: "catlog-license+jwt", KeyID: set.Signing.KID})
	if err != nil {
		t.Fatalf("sign with loaded key: %v", err)
	}
	pub, ok := set.SigningKeyByKID(set.Signing.KID)
	if !ok {
		t.Fatalf("SigningKeyByKID(%q) not found", set.Signing.KID)
	}
	if _, err := cjws.VerifyES256(compact, pub); err != nil {
		t.Errorf("verify with key from JWKS lookup: %v", err)
	}
	if _, ok := set.SigningKeyByKID("catlog-000000"); ok {
		t.Error("unknown kid resolved, want miss")
	}
}

func TestJWKS(t *testing.T) {
	set, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	raw, err := set.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}

	var envelope struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal jwks: %v", err)
	}
	if len(envelope.Keys) != 1 {
		t.Fatalf("jwks has %d keys, want 1: %s", len(envelope.Keys), raw)
	}
	if strings.Contains(string(raw), `"d"`) {
		t.Fatalf("JWKS leaked private material: %s", raw)
	}

	var parsed jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse jwks: %v", err)
	}
	k := parsed.Keys[0]
	if k.KeyID != set.Signing.KID {
		t.Errorf("jwks kid = %q, want %q", k.KeyID, set.Signing.KID)
	}
	if k.Algorithm != "ES256" || k.Use != "sig" {
		t.Errorf("jwks alg/use = %q/%q, want ES256/sig", k.Algorithm, k.Use)
	}
	pub, ok := k.Key.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("jwks key is %T, want *ecdsa.PublicKey", k.Key)
	}
	if !pub.Equal(&set.Signing.Key.PublicKey) {
		t.Error("jwks publishes a different key than the signing key")
	}
}

// TestRetiredKeysAreVerifyOnly exercises the §4.5.1 rotation story.
func TestRetiredKeysAreVerifyOnly(t *testing.T) {
	dir := t.TempDir()

	old, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const oldKID = "catlog-202601"
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeSecret(filepath.Join(dir, "license-signing-"+oldKID+".pem"),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatalf("write retired key: %v", err)
	}

	set, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if len(set.Retired) != 1 || set.Retired[0].KID != oldKID {
		t.Fatalf("retired = %+v, want one key with kid %q", set.Retired, oldKID)
	}
	if set.Signing.Key.Equal(old) {
		t.Error("a retired key became the active signing key")
	}

	// A license signed by the retired key still verifies via its kid.
	compact, err := cjws.SignES256(old, []byte(`{"ver":1}`), cjws.SignOptions{KeyID: oldKID})
	if err != nil {
		t.Fatalf("sign with retired key: %v", err)
	}
	pub, ok := set.SigningKeyByKID(oldKID)
	if !ok {
		t.Fatal("retired kid does not resolve")
	}
	if _, err := cjws.VerifyES256(compact, pub); err != nil {
		t.Errorf("verify retired-key license: %v", err)
	}

	raw, err := set.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var parsed jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse jwks: %v", err)
	}
	if len(parsed.Keys) != 2 {
		t.Errorf("jwks has %d keys, want 2 (active + retired)", len(parsed.Keys))
	}
	if parsed.Keys[0].KeyID != set.Signing.KID {
		t.Errorf("jwks[0] kid = %q, want the active key %q", parsed.Keys[0].KeyID, set.Signing.KID)
	}
}

// TestUserKeyDerivation pins D17: HMAC-SHA256(pepper, "<idp>:<subject>").
func TestUserKeyDerivation(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	pepper, err := os.ReadFile(filepath.Join(dir, PepperFile))
	if err != nil {
		t.Fatalf("read pepper: %v", err)
	}
	if len(pepper) != SecretLen {
		t.Fatalf("pepper is %d bytes, want %d", len(pepper), SecretLen)
	}

	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte("discord:100000000000000000"))
	var want UserKey
	copy(want[:], mac.Sum(nil))

	if got := set.UserKey("discord", "100000000000000000"); got != want {
		t.Errorf("UserKey = %x, want %x", got[:], want[:])
	}
	if got := set.UserKeyFromSubject("discord:100000000000000000"); got != want {
		t.Errorf("UserKeyFromSubject = %x, want %x", got[:], want[:])
	}

	// Distinct IdPs never collide (D10: no auto-merge across IdPs).
	if set.UserKey("discord", "x") == set.UserKey("github", "x") {
		t.Error("same subject under two IdPs produced the same user_key")
	}
	// A different pepper produces different keys — the whole point of D17.
	other, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate other: %v", err)
	}
	if other.UserKey("discord", "x") == set.UserKey("discord", "x") {
		t.Error("two peppers produced the same user_key")
	}
}

func TestUserKeyEncoding(t *testing.T) {
	set, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	uk := set.UserKey("google", "g-user-1")

	if got := len(uk.Bytes()); got != SecretLen {
		t.Errorf("Bytes len = %d, want %d", got, SecretLen)
	}
	round, err := ParseUserKey(uk.B64U())
	if err != nil {
		t.Fatalf("ParseUserKey: %v", err)
	}
	if round != uk {
		t.Error("b64u round trip changed the user_key")
	}
	fromBytes, err := UserKeyFromBytes(uk.Bytes())
	if err != nil {
		t.Fatalf("UserKeyFromBytes: %v", err)
	}
	if fromBytes != uk {
		t.Error("bytes round trip changed the user_key")
	}
	if _, err := UserKeyFromBytes([]byte{1, 2, 3}); err == nil {
		t.Error("UserKeyFromBytes accepted 3 bytes, want failure")
	}
	if _, err := ParseUserKey("not-base64url!!"); err == nil {
		t.Error("ParseUserKey accepted junk, want failure")
	}
}

// TestSecretsNeverRender is the §5.11 guard: no formatting or logging path may
// emit the pepper, the session key, or a full user_key.
func TestSecretsNeverRender(t *testing.T) {
	dir := t.TempDir()
	set, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	uk := set.UserKey("discord", "100000000000000000")
	log.Info("startup", "keys", set, "player", uk)
	line := buf.String()

	for name, secret := range map[string][]byte{
		"session key": set.SessionKey(),
		"pepper":      set.pepper,
	} {
		if strings.Contains(line, cjws.B64U(secret)) {
			t.Errorf("log line leaked the %s: %s", name, line)
		}
	}
	if strings.Contains(line, uk.B64U()) {
		t.Errorf("log line leaked the full user_key: %s", line)
	}
	if !strings.Contains(line, uk.B64U()[:8]) {
		t.Errorf("log line lost the user_key prefix entirely: %s", line)
	}
	if !strings.Contains(line, set.Signing.KID) {
		t.Errorf("log line should still carry the signing kid: %s", line)
	}

	// %v and %s must be just as safe as slog.
	if got := fmt.Sprintf("%v %s", set, uk); strings.Contains(got, cjws.B64U(set.pepper)) || strings.Contains(got, uk.B64U()) {
		t.Errorf("fmt verbs leaked a secret: %s", got)
	}
	if got := set.String(); strings.Contains(got, cjws.B64U(set.pepper)) {
		t.Errorf("Set.String leaked the pepper: %s", got)
	}
	if got := uk.String(); strings.Contains(got, uk.B64U()) {
		t.Errorf("UserKey.String rendered the full value: %s", got)
	}
}

func TestLoadRejectsPermissiveMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	path := filepath.Join(dir, PepperFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Error("LoadOrCreate accepted a world-readable pepper, want failure")
	} else if !strings.Contains(err.Error(), "mode") {
		t.Errorf("err = %v, want a mode complaint", err)
	}
}

func TestLoadRejectsCorruptSecrets(t *testing.T) {
	t.Run("short pepper", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := writeSecret(filepath.Join(dir, PepperFile), []byte("too short")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadOrCreate(dir); err == nil {
			t.Error("accepted a short pepper, want failure")
		}
	})

	t.Run("non-PEM signing key", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := writeSecret(filepath.Join(dir, SigningFile), []byte("hello")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadOrCreate(dir); err == nil {
			t.Error("accepted a non-PEM signing key, want failure")
		}
	})

	t.Run("wrong curve", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := writeSecret(filepath.Join(dir, SigningFile),
			pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadOrCreate(dir); err == nil {
			t.Error("accepted a P-384 signing key, want failure")
		}
	})
}

func TestKID(t *testing.T) {
	got := KID(time.Date(2026, 8, 6, 23, 59, 0, 0, time.UTC))
	if got != "catlog-202608" {
		t.Errorf("KID = %q, want catlog-202608", got)
	}
}
