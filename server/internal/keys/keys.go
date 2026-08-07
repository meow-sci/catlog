// Package keys loads or creates the pepper, session key and license signing key
// under data/keys/, and assembles the published JWKS (§4.5.1, §4.7).
//
// Three secrets, all created on first run by `catlogctl keygen` and never
// leaving this process:
//
//   - license-signing.pem — P-256 private key (PKCS#8 PEM). Signs license JWS
//     (§4.5.1) and the published deny-list (§5.8). Its public half is served at
//     /.well-known/catlog-jwks.json.
//   - session.key — 32 random bytes. HMAC key for the website session cookie
//     (§4.5.4).
//   - pepper.key — 32 random bytes. HMAC key deriving user_key from an IdP
//     subject (D17, §4.7). Losing it orphans every account; leaking it lets an
//     attacker link accounts back to IdP subjects.
//
// # Secret hygiene (§5.11)
//
// Nothing in this package renders a secret. [Set] implements [slog.LogValuer]
// so `slog.Info("keys", "keys", set)` emits metadata only, and [UserKey] —
// which is derived from the pepper and identifies a player — renders as a
// truncated b64u prefix, never in full.
//
// # Key rotation (§4.5.1)
//
// Rotation is "add a key with a new kid, keep the old one until every license
// it signed has expired". The active signing key is license-signing.pem with
// its kid in the license-signing.kid sidecar; retired keys are dropped in as
// license-signing-<kid>.pem and are loaded verify-only — they appear in the
// JWKS and resolve by kid, but never sign anything new.
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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/cjws"
)

// File names under the keys directory (§4.5.1, §4.5.4, §4.7).
const (
	SigningFile = "license-signing.pem"
	// kidFile records which kid the active signing key was created under, so
	// the kid survives restarts and file-mtime changes.
	kidFile     = "license-signing.kid"
	SessionFile = "session.key"
	PepperFile  = "pepper.key"

	// dirPerm and filePerm keep the keys directory and every secret in it
	// owner-only. LoadOrCreate refuses to load a secret that is group- or
	// world-readable.
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// SecretLen is the byte length of the session key and the pepper.
const SecretLen = 32

// retiredPattern matches a retired signing key dropped in for rotation:
// license-signing-<kid>.pem.
var retiredPattern = regexp.MustCompile(`^license-signing-([A-Za-z0-9._-]+)\.pem$`)

// KID returns the key id for a signing key created at t: "catlog-<yyyymm>"
// (§4.5.1).
func KID(t time.Time) string { return "catlog-" + t.UTC().Format("200601") }

// SigningKey is a license signing key and the kid that selects it.
type SigningKey struct {
	KID string
	Key *ecdsa.PrivateKey
}

// Public returns the JWK published for this key in the JWKS.
func (s SigningKey) Public() jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       &s.Key.PublicKey,
		KeyID:     s.KID,
		Algorithm: string(cjws.Alg),
		Use:       "sig",
	}
}

// Set is the loaded key material for one data directory.
//
// It is immutable after [LoadOrCreate] returns and safe for concurrent use.
type Set struct {
	// Dir is the keys directory these were loaded from.
	Dir string
	// Signing is the active license signing key — the one that signs.
	Signing SigningKey
	// Retired holds verify-only keys kept until the licenses they signed
	// expire. Published in the JWKS and resolvable by kid; never used to sign.
	Retired []SigningKey

	session []byte // 32 B, §4.5.4
	pepper  []byte // 32 B, D17/§4.7
}

// LoadOrCreate loads the key set from dir, creating dir and any missing secret.
// It is idempotent: an existing file is loaded, never regenerated, so calling
// it at every catlogd start and from `catlogctl keygen` are the same operation.
func LoadOrCreate(dir string) (*Set, error) {
	if dir == "" {
		return nil, errors.New("keys: empty directory")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("keys: create %s: %w", dir, err)
	}

	s := &Set{Dir: dir}

	signing, kid, err := loadOrCreateSigning(dir)
	if err != nil {
		return nil, err
	}
	s.Signing = SigningKey{KID: kid, Key: signing}

	if s.Retired, err = loadRetired(dir); err != nil {
		return nil, err
	}
	if s.session, err = loadOrCreateSecret(filepath.Join(dir, SessionFile)); err != nil {
		return nil, err
	}
	if s.pepper, err = loadOrCreateSecret(filepath.Join(dir, PepperFile)); err != nil {
		return nil, err
	}
	return s, nil
}

// Created reports which of the three secrets do not yet exist in dir. It is how
// `catlogctl keygen` tells the operator what it is about to create without
// having to create it first.
func Created(dir string) (missing []string, err error) {
	for _, name := range []string{SigningFile, SessionFile, PepperFile} {
		switch _, err := os.Stat(filepath.Join(dir, name)); {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, name)
		default:
			return nil, fmt.Errorf("keys: stat %s: %w", name, err)
		}
	}
	return missing, nil
}

// SigningKeyByKID resolves a kid to a key for verification — §4.5.3 step 2's
// "known kid". Retired keys resolve; unknown kids do not.
func (s *Set) SigningKeyByKID(kid string) (*ecdsa.PublicKey, bool) {
	if kid == s.Signing.KID {
		return &s.Signing.Key.PublicKey, true
	}
	for _, r := range s.Retired {
		if r.KID == kid {
			return &r.Key.PublicKey, true
		}
	}
	return nil, false
}

// JWKS renders the public key set served at /.well-known/catlog-jwks.json:
// `{"keys":[...]}`, active key first, each with its kid (§4.5.1).
func (s *Set) JWKS() (json.RawMessage, error) {
	set := jose.JSONWebKeySet{Keys: make([]jose.JSONWebKey, 0, 1+len(s.Retired))}
	set.Keys = append(set.Keys, s.Signing.Public())
	for _, r := range s.Retired {
		set.Keys = append(set.Keys, r.Public())
	}
	b, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("keys: marshal jwks: %w", err)
	}
	return b, nil
}

// SessionKey returns a copy of the 32-byte session HMAC key (§4.5.4). A copy,
// so a caller cannot scribble on the loaded secret.
func (s *Set) SessionKey() []byte { return append([]byte(nil), s.session...) }

// UserKey derives a player's stable identifier:
// HMAC-SHA256(pepper, "<idp>:<subject>") (D17, §4.7).
//
// The pepper never leaves this package and the subject is discarded by the
// caller immediately after derivation, so nothing in the system can walk back
// from a stored user_key to an IdP account.
//
// idp is one of "discord", "google", "github", or "dev" for the admin-issue
// path (§5.9), which uses the handle as the subject.
func (s *Set) UserKey(idp, subject string) UserKey {
	return s.UserKeyFromSubject(SubjectString(idp, subject))
}

// UserKeyFromSubject is [Set.UserKey] for an already-assembled subject string.
func (s *Set) UserKeyFromSubject(subject string) UserKey {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(subject))
	var uk UserKey
	copy(uk[:], mac.Sum(nil))
	return uk
}

// SubjectString assembles the §4.7 subject: "<idp>:<stable-subject>".
func SubjectString(idp, subject string) string { return idp + ":" + subject }

// LogValue implements slog.LogValuer so a Set logs as metadata only — never key
// material (§5.11).
func (s *Set) LogValue() slog.Value {
	retired := make([]string, 0, len(s.Retired))
	for _, r := range s.Retired {
		retired = append(retired, r.KID)
	}
	return slog.GroupValue(
		slog.String("dir", s.Dir),
		slog.String("signing_kid", s.Signing.KID),
		slog.Any("retired_kids", retired),
	)
}

// String implements fmt.Stringer with the same redaction as LogValue, so an
// accidental %v or %s cannot print a secret either.
func (s *Set) String() string {
	return fmt.Sprintf("keys.Set{dir:%s signing_kid:%s retired:%d}", s.Dir, s.Signing.KID, len(s.Retired))
}

// UserKey is the 32-byte HMAC identifying a player (D17). It is not secret, but
// it is the primary key of every row a player owns, so it is never logged in
// full (§5.11).
type UserKey [SecretLen]byte

// Bytes returns the raw 32 bytes for binding to the `player.user_key` BLOB.
func (u UserKey) Bytes() []byte { return append([]byte(nil), u[:]...) }

// B64U renders the full value — the license `sub` claim (§4.5.1). Safe on the
// wire, not safe in a log line.
func (u UserKey) B64U() string { return cjws.B64U(u[:]) }

// LogValue renders the §5.11 log form: a b64u prefix of at most 8 characters.
func (u UserKey) LogValue() slog.Value { return slog.StringValue(u.B64U()[:8]) }

// String implements fmt.Stringer with the same truncation, so %v cannot widen it.
func (u UserKey) String() string { return u.B64U()[:8] + "…" }

// ParseUserKey decodes the b64u form used in a license `sub` claim.
func ParseUserKey(s string) (UserKey, error) {
	var uk UserKey
	b, err := cjws.DecodeB64U(s)
	if err != nil {
		return uk, fmt.Errorf("keys: parse user_key: %w", err)
	}
	if len(b) != SecretLen {
		return uk, fmt.Errorf("keys: user_key is %d bytes, want %d", len(b), SecretLen)
	}
	copy(uk[:], b)
	return uk, nil
}

// UserKeyFromBytes wraps the 32 bytes read back out of a `user_key` column.
func UserKeyFromBytes(b []byte) (UserKey, error) {
	var uk UserKey
	if len(b) != SecretLen {
		return uk, fmt.Errorf("keys: user_key is %d bytes, want %d", len(b), SecretLen)
	}
	copy(uk[:], b)
	return uk, nil
}

// --- file handling ---------------------------------------------------------

func loadOrCreateSigning(dir string) (*ecdsa.PrivateKey, string, error) {
	path := filepath.Join(dir, SigningFile)
	kidPath := filepath.Join(dir, kidFile)

	key, err := readPrivateKeyPEM(path)
	switch {
	case err == nil:
		kid, err := readKID(kidPath)
		if err != nil {
			return nil, "", err
		}
		if kid == "" {
			// A key dropped in by hand, or created before the sidecar existed.
			// Date it from the file so the kid is at least stable, then persist.
			fi, err := os.Stat(path)
			if err != nil {
				return nil, "", fmt.Errorf("keys: stat %s: %w", path, err)
			}
			kid = KID(fi.ModTime())
			if err := writeSecret(kidPath, []byte(kid+"\n")); err != nil {
				return nil, "", err
			}
		}
		return key, kid, nil

	case errors.Is(err, fs.ErrNotExist):
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, "", fmt.Errorf("keys: generate signing key: %w", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, "", fmt.Errorf("keys: marshal signing key: %w", err)
		}
		if err := writeSecret(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			return nil, "", err
		}
		kid := KID(time.Now())
		if err := writeSecret(kidPath, []byte(kid+"\n")); err != nil {
			return nil, "", err
		}
		return key, kid, nil

	default:
		return nil, "", err
	}
}

func loadRetired(dir string) ([]SigningKey, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("keys: read %s: %w", dir, err)
	}
	var out []SigningKey
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := retiredPattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		key, err := readPrivateKeyPEM(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, SigningKey{KID: m[1], Key: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KID < out[j].KID })
	return out, nil
}

// readPrivateKeyPEM reads a PKCS#8 (or SEC1) EC P-256 private key. It returns a
// wrapped fs.ErrNotExist when the file is absent, which is the caller's signal
// to create one.
func readPrivateKeyPEM(path string) (*ecdsa.PrivateKey, error) {
	b, err := readSecret(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("keys: %s is not PEM", path)
	}

	var key any
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("keys: %s has PEM type %q, want PRIVATE KEY", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("keys: parse %s: %w", path, err)
	}

	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("keys: %s holds a %T, want an EC key", path, key)
	}
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("keys: %s is not P-256 (%w)", path, cjws.ErrBadCurve)
	}
	return ec, nil
}

func readKID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("keys: read %s: %w", path, err)
	}
	kid := strings.TrimSpace(string(b))
	if kid == "" {
		return "", nil
	}
	if !retiredPattern.MatchString("license-signing-" + kid + ".pem") {
		return "", fmt.Errorf("keys: %s holds an unusable kid %q", path, kid)
	}
	return kid, nil
}

func loadOrCreateSecret(path string) ([]byte, error) {
	b, err := readSecret(path)
	switch {
	case err == nil:
		if len(b) != SecretLen {
			return nil, fmt.Errorf("keys: %s is %d bytes, want %d", path, len(b), SecretLen)
		}
		return b, nil
	case errors.Is(err, fs.ErrNotExist):
		b := make([]byte, SecretLen)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("keys: generate %s: %w", filepath.Base(path), err)
		}
		if err := writeSecret(path, b); err != nil {
			return nil, err
		}
		return b, nil
	default:
		return nil, err
	}
}

// readSecret reads a key file and refuses anything readable beyond its owner —
// a leaked pepper is unrecoverable, so a permissive mode is a hard error rather
// than a warning.
func readSecret(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err // may wrap fs.ErrNotExist; callers check
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("keys: %s has mode %#o, want %#o (group/other must not read secrets)", path, mode, filePerm)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("keys: read %s: %w", path, err)
	}
	return b, nil
}

// writeSecret writes owner-only and refuses to clobber: every caller has
// already established the file is absent, so an existing file means a
// concurrent keygen and losing that race must not destroy a key.
func writeSecret(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("keys: create %s: %w", path, err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("keys: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("keys: close %s: %w", path, err)
	}
	return nil
}
