// Package authz issues licenses and runs the license+proof verification chain
// in the exact order of §4.5.3, plus the deny-list and per-credential token
// buckets (§4.3).
//
// # The order is the security property
//
// §4.5.3 numbers thirteen steps "cheapest first", and [Verifier.Verify]
// implements steps 1–10 in exactly that order. This is not style: an
// unauthenticated attacker must not be able to make the server do an ECDSA
// verification, a database query or a body read by sending garbage. Each step
// therefore costs strictly more than the one before it, and each rejects with
// its own §4.9 code:
//
//  1. headers present, ≤ 4 KiB, compact shape       license_invalid / proof_invalid
//  2. license alg + known kid + signature           license_invalid
//  3. license claims: iss, exp, ver                 license_invalid / license_expired
//  4. deny-list: sub banned, jkt revoked            banned / license_revoked
//  5. credential row: exists, live, matches         license_invalid / license_revoked / banned
//  6. proof alg + P-256 jwk + thumbprint == cnf.jkt proof_invalid
//  7. proof signature                               proof_invalid
//  8. htm, htu, |iat - now| ≤ 300 s                 proof_invalid / clock_skew
//  9. token bucket keyed jkt                        rate_limited
//  10. body hash: bh == b64u(sha256(body))           proof_invalid
//
// Steps 11–13 (replay, stream chain, insert) touch the database as writes and
// belong to the ingest writer goroutine (§5.5), not here.
//
// [Verifier.Stats] counts the ECDSA verifications actually performed, which is
// what makes the ordering observable — and testable — from outside.
package authz

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// Config is the policy half of the chain — everything that comes from
// configuration rather than from the request (§5.3).
type Config struct {
	// Issuer is the value a license `iss` must equal (§4.5.3 step 3).
	Issuer string
	// AcceptedHTU is matched against the proof `htu` by exact string equality,
	// with no normalization (§4.5.2).
	AcceptedHTU []string
	// MaxSkew bounds |iat - now| on a proof. Zero means [MaxSkew] (±300 s).
	MaxSkew time.Duration
	// RatePerSecond and Burst configure the per-jkt token bucket (§4.3).
	RatePerSecond float64
	Burst         int
	// CacheSize overrides the license cache capacity. Zero means
	// [LicenseCacheSize].
	CacheSize int
}

// Stats counts the work the chain has done. The signature counters are the
// DoS-resistance assertion: a request rejected at step 3 must show one license
// verification and zero proof verifications.
type Stats struct {
	LicenseVerifies  uint64
	ProofVerifies    uint64
	LicenseCacheHits uint64
	Rejections       uint64
}

// Verifier runs the §4.5.3 chain. It is safe for concurrent use.
type Verifier struct {
	cfg     Config
	keys    *keys.Set
	events  *store.Events
	deny    *DenyList
	cache   *licenseCache
	limiter *Limiter

	// now is the clock, injectable so skew and rate-limit tests are
	// deterministic.
	now func() time.Time

	licenseVerifies  atomic.Uint64
	proofVerifies    atomic.Uint64
	licenseCacheHits atomic.Uint64
	rejections       atomic.Uint64
}

// New builds a verifier over a key set, the events database and a deny-list.
func New(cfg Config, ks *keys.Set, events *store.Events, deny *DenyList) *Verifier {
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = MaxSkew
	}
	if deny == nil {
		deny = NewDenyList()
	}
	return &Verifier{
		cfg:     cfg,
		keys:    ks,
		events:  events,
		deny:    deny,
		cache:   newLicenseCache(cfg.CacheSize),
		limiter: NewLimiter(cfg.RatePerSecond, cfg.Burst),
		now:     time.Now,
	}
}

// SetClock replaces the verifier's clock, and the rate limiter's with it.
//
// catlogd calls this once at start-up with its shared server clock (which *is*
// [time.Now] unless the deployment enabled `[server] clock_control`), so the
// license-expiry check, the ±300 s proof-skew window, the `Date` header a
// client resynchronises against and the token bucket all read one clock. Tests
// use it the same way.
func (v *Verifier) SetClock(now func() time.Time) {
	v.now = now
	v.limiter.now = now
}

// Now is the verifier's current time. Handlers use it for the `server_time`
// field and the Date header so a client sees one consistent clock (§4.4).
func (v *Verifier) Now() time.Time { return v.now() }

// DenyList exposes the set for WP3's mutation paths.
func (v *Verifier) DenyList() *DenyList { return v.deny }

// Limiter exposes the token buckets, for /admin/stats.
func (v *Verifier) Limiter() *Limiter { return v.limiter }

// Stats snapshots the counters.
func (v *Verifier) Stats() Stats {
	return Stats{
		LicenseVerifies:  v.licenseVerifies.Load(),
		ProofVerifies:    v.proofVerifies.Load(),
		LicenseCacheHits: v.licenseCacheHits.Load(),
		Rejections:       v.rejections.Load(),
	}
}

// Request is one ingest attempt's credentials: the two compact JWS from the
// X-Catlog-License and X-Catlog-Proof headers.
type Request struct {
	License string
	Proof   string
}

// Result is a passed chain: who the caller is and what they claim about the
// batch they are about to send.
type Result struct {
	// License is the verified license claim set.
	License LicenseClaims
	// Proof is the verified proof claim set.
	Proof ProofClaims
	// JKT is the credential thumbprint — the license `cnf.jkt`, which equals
	// the thumbprint of the proof's embedded key (step 6).
	JKT string
	// PlayerID is the owning player's row id.
	PlayerID int64
	// Handle is the credential's handle.
	Handle string
	// UserKey is the player's 32-byte identifier, decoded from `sub`.
	UserKey keys.UserKey
}

// SID is the stream identifier the batch belongs to (§4.5.2).
func (r *Result) SID() ids.ID { return r.Proof.sid }

// Seq is the batch's 1-based sequence number within its stream.
func (r *Result) Seq() int64 { return r.Proof.Seq }

// CheckBodyHash is §4.5.3 step 10's second half: the request body, as sent
// (post-brotli), must hash to the proof's `bh`.
//
// The body read itself lives in the ingest handler because that is where the
// size caps are enforced while reading (§4.3); only the comparison is policy.
func (r *Result) CheckBodyHash(body []byte) *Error {
	if got := BodyHash(body); got != r.Proof.BH {
		return fail(10, CodeProofInvalid, "body hash does not match the proof bh claim")
	}
	return nil
}

// Verify runs §4.5.3 steps 1–10 (bar the body read) in order, returning either
// a Result or the first rejection.
func (v *Verifier) Verify(ctx context.Context, req Request) (*Result, *Error) {
	res, err := v.verify(ctx, req)
	if err != nil {
		v.rejections.Add(1)
	}
	return res, err
}

func (v *Verifier) verify(ctx context.Context, req Request) (*Result, *Error) {
	now := v.now()

	// --- step 1: both headers present, ≤ 4 KiB, compact shape ---------------
	if req.License == "" {
		return nil, fail(1, CodeLicenseInvalid, "missing X-Catlog-License header")
	}
	if req.Proof == "" {
		return nil, fail(1, CodeProofInvalid, "missing X-Catlog-Proof header")
	}
	licParts, perr := splitCompact(req.License)
	if perr != nil {
		return nil, failf(1, CodeLicenseInvalid, "license: %s", perr)
	}
	proofParts, perr := splitCompact(req.Proof)
	if perr != nil {
		return nil, failf(1, CodeProofInvalid, "proof: %s", perr)
	}

	// --- step 2: license alg, known kid, signature --------------------------
	license, aerr := v.licenseClaims(req.License, licParts)
	if aerr != nil {
		return nil, aerr
	}

	// --- step 3: license claims --------------------------------------------
	if license.Ver != LicenseVer {
		return nil, failf(3, CodeLicenseInvalid, "license ver %d, want %d", license.Ver, LicenseVer)
	}
	if license.Issuer != v.cfg.Issuer {
		return nil, fail(3, CodeLicenseInvalid, "license iss does not match this server")
	}
	if license.Expired(now) {
		return nil, fail(3, CodeLicenseExpired, "license has expired")
	}
	if license.Handle == "" || license.Cnf.JKT == "" {
		return nil, fail(3, CodeLicenseInvalid, "license is missing handle or cnf.jkt")
	}
	userKey, err := license.UserKey()
	if err != nil {
		return nil, fail(3, CodeLicenseInvalid, "license sub is not a 32-byte user key")
	}

	// --- step 4: deny-list --------------------------------------------------
	if v.deny.HasSub(license.Subject) {
		return nil, fail(4, CodeBanned, "account is banned")
	}
	if v.deny.HasJKT(license.Cnf.JKT) {
		return nil, fail(4, CodeLicenseRevoked, "credential is revoked")
	}

	// --- step 5: credential row --------------------------------------------
	playerID, aerr := v.credential(ctx, license, userKey)
	if aerr != nil {
		return nil, aerr
	}

	// --- step 6: proof alg, embedded P-256 jwk, thumbprint ------------------
	proofKey, aerr := v.proofKey(proofParts, license.Cnf.JKT)
	if aerr != nil {
		return nil, aerr
	}

	// --- step 7: proof signature -------------------------------------------
	v.proofVerifies.Add(1)
	if _, err := cjws.VerifyES256(req.Proof, proofKey); err != nil {
		return nil, fail(7, CodeProofInvalid, "proof signature does not verify")
	}

	// --- step 8: htm, htu, iat skew ----------------------------------------
	proof, aerr := v.proofClaims(proofParts.payload, now)
	if aerr != nil {
		return nil, aerr
	}

	// --- step 9: rate limit, before the body is read ------------------------
	if ok, retry := v.limiter.Allow(license.Cnf.JKT); !ok {
		e := fail(9, CodeRateLimited, "too many batches for this credential")
		e.RetryAfter = int(retry / time.Second)
		return nil, e
	}

	// Step 10 is the caller's: it reads the body under the §4.3 caps and calls
	// Result.CheckBodyHash.
	return &Result{
		License:  license,
		Proof:    proof,
		JKT:      license.Cnf.JKT,
		PlayerID: playerID,
		Handle:   license.Handle,
		UserKey:  userKey,
	}, nil
}

// licenseClaims is step 2: algorithm, known kid, signature — memoized by the
// SHA-256 of the raw JWS (§4.5.3 step 2, LRU 10k).
func (v *Verifier) licenseClaims(compact string, parts compactParts) (LicenseClaims, *Error) {
	key := cacheKey(compact)
	if claims, ok := v.cache.get(key); ok {
		v.licenseCacheHits.Add(1)
		return claims, nil
	}

	h, err := parts.header()
	if err != nil {
		return LicenseClaims{}, fail(2, CodeLicenseInvalid, "license header is unreadable")
	}
	if h.Alg != string(cjws.Alg) {
		return LicenseClaims{}, failf(2, CodeLicenseInvalid, "license alg %q is not %s", h.Alg, cjws.Alg)
	}
	if h.KID == "" {
		return LicenseClaims{}, fail(2, CodeLicenseInvalid, "license has no kid")
	}
	pub, ok := v.keys.SigningKeyByKID(h.KID)
	if !ok {
		return LicenseClaims{}, fail(2, CodeLicenseInvalid, "license kid is unknown to this server")
	}

	v.licenseVerifies.Add(1)
	payload, err := cjws.VerifyES256(compact, pub)
	if err != nil {
		return LicenseClaims{}, fail(2, CodeLicenseInvalid, "license signature does not verify")
	}

	var claims LicenseClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return LicenseClaims{}, fail(2, CodeLicenseInvalid, "license claims are not JSON")
	}
	v.cache.put(key, claims)
	return claims, nil
}

// credential is step 5: the credential row must exist, be live, and agree with
// the license about handle and player. It also catches a banned or deleted
// account, whose rows the license alone cannot know about.
func (v *Verifier) credential(ctx context.Context, license LicenseClaims, userKey keys.UserKey) (int64, *Error) {
	cred, err := v.events.CredentialByJKT(ctx, license.Cnf.JKT)
	if errors.Is(err, store.ErrNotFound) {
		return 0, fail(5, CodeLicenseInvalid, "no credential for this key")
	}
	if err != nil {
		return 0, internalErr(5, err)
	}
	if cred.Revoked() {
		return 0, fail(5, CodeLicenseRevoked, "credential is revoked")
	}
	if cred.Handle != license.Handle {
		return 0, fail(5, CodeLicenseInvalid, "credential handle does not match the license")
	}

	player, err := v.events.PlayerByID(ctx, cred.PlayerID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, fail(5, CodeLicenseInvalid, "credential has no player")
	}
	if err != nil {
		return 0, internalErr(5, err)
	}
	if player.Banned() {
		return 0, fail(5, CodeBanned, "account is banned")
	}
	if player.UserKey != userKey {
		return 0, fail(5, CodeLicenseInvalid, "license sub does not match the credential's player")
	}
	return cred.PlayerID, nil
}

// proofKey is step 6: the proof header must be ES256 with an embedded P-256
// public JWK whose RFC 7638 thumbprint is the license `cnf.jkt`. That equality
// is the whole proof-of-possession binding.
func (v *Verifier) proofKey(parts compactParts, wantJKT string) (*ecdsa.PublicKey, *Error) {
	h, err := parts.header()
	if err != nil {
		return nil, fail(6, CodeProofInvalid, "proof header is unreadable")
	}
	if h.Alg != string(cjws.Alg) {
		return nil, failf(6, CodeProofInvalid, "proof alg %q is not %s", h.Alg, cjws.Alg)
	}
	if len(h.JWK) == 0 {
		return nil, fail(6, CodeProofInvalid, "proof header has no embedded jwk")
	}

	var jwk jose.JSONWebKey
	if err := jwk.UnmarshalJSON(h.JWK); err != nil {
		return nil, fail(6, CodeProofInvalid, "proof jwk is unreadable")
	}
	pub, err := cjws.PublicKeyOf(&jwk)
	if err != nil {
		return nil, fail(6, CodeProofInvalid, "proof jwk is not an EC P-256 public key")
	}
	jkt, err := cjws.ThumbprintPublicKey(pub)
	if err != nil {
		return nil, fail(6, CodeProofInvalid, "proof jwk cannot be thumbprinted")
	}
	if jkt != wantJKT {
		return nil, fail(6, CodeProofInvalid, "proof key does not match the license cnf.jkt")
	}
	return pub, nil
}

// proofClaims is step 8: the HTTP binding (htm, htu) and the clock-skew window,
// plus the structural validation of the fields steps 10–12 will use.
func (v *Verifier) proofClaims(payload []byte, now time.Time) (ProofClaims, *Error) {
	var c ProofClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fail(8, CodeProofInvalid, "proof claims are not JSON")
	}
	if c.HTM != "POST" {
		return c, failf(8, CodeProofInvalid, "proof htm %q, want POST", c.HTM)
	}
	if !slices.Contains(v.cfg.AcceptedHTU, c.HTU) {
		return c, fail(8, CodeProofInvalid, "proof htu is not an accepted ingest URL")
	}

	// The skew check is what the mod recovers from by re-reading the Date
	// header, so it gets its own code (§4.5.3 step 8).
	skew := time.Duration(now.Unix()-c.IssuedAt) * time.Second
	if skew < 0 {
		skew = -skew
	}
	if skew > v.cfg.MaxSkew {
		return c, failf(8, CodeClockSkew, "proof iat is %d s away from server time", int(skew.Seconds()))
	}

	// Structural checks on the fields the writer will trust (§4.5.3 steps
	// 10–12). Cheap, and doing them here means the writer never has to.
	var err error
	if c.batchULID, err = ids.Parse(c.JTI); err != nil {
		return c, fail(8, CodeProofInvalid, "proof jti is not a ULID")
	}
	if c.sid, err = ids.Parse(c.SID); err != nil {
		return c, fail(8, CodeProofInvalid, "proof sid is not a ULID")
	}
	if c.Seq < 1 {
		return c, failf(8, CodeProofInvalid, "proof seq %d, want ≥ 1", c.Seq)
	}
	if !isSHA256B64U(c.BH) {
		return c, fail(8, CodeProofInvalid, "proof bh is not a base64url SHA-256")
	}
	if c.PH != "" && !isSHA256B64U(c.PH) {
		return c, fail(8, CodeProofInvalid, "proof ph is not a base64url SHA-256")
	}
	return c, nil
}

// isSHA256B64U reports whether s is 32 bytes of unpadded base64url.
func isSHA256B64U(s string) bool {
	b, err := cjws.DecodeB64U(s)
	return err == nil && len(b) == 32
}

// String renders a Result for logs: identity only, never license or proof
// contents (§5.11).
func (r *Result) String() string {
	return fmt.Sprintf("authz.Result{player:%d handle:%s jkt:%s… seq:%d}", r.PlayerID, r.Handle, safePrefix(r.JKT), r.Proof.Seq)
}

func safePrefix(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
