package identity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// The two published well-known documents (§4.8, §5.8).
const (
	JWKSPath     = "/.well-known/catlog-jwks.json"
	DenyListPath = "/.well-known/catlog-denylist.json"
)

// DenyListType is the `typ` header of the published deny-list JWS. It is not a
// license and not a proof, and giving it its own type means a deny-list can
// never be mistaken for either (§4.5).
const DenyListType = "catlog-denylist+jwt"

// DenyListDocument is the §5.8 payload.
type DenyListDocument struct {
	// Ver is the deny-list's version counter, bumped on every mutation. A
	// future multi-node puller uses it to tell a fetch it already has from one
	// it does not.
	Ver int64 `json:"ver"`
	// UpdatedAt is when this version was signed, unix ms.
	UpdatedAt int64 `json:"updated_at"`
	// BannedSubs is `b64u(user_key)` for every banned or purged account —
	// exactly the values a license `sub` carries (§4.5.1).
	BannedSubs []string `json:"banned_subs"`
	// RevokedJKTs is every revoked credential thumbprint.
	RevokedJKTs []string `json:"revoked_jkts"`
}

// DenyListPublisher signs and caches the §5.8 deny-list.
//
// # Why it is signed
//
// Single-node catlog does not need it: the in-process set is authoritative and
// no one else reads this file. It is signed anyway because the moment there are
// two nodes, the second one is pulling a ban list over HTTP from something it
// cannot otherwise authenticate — and retrofitting a signature onto a published
// document is a migration, while shipping one from the start is a header.
//
// A DenyListPublisher is safe for concurrent use.
type DenyListPublisher struct {
	signing keys.SigningKey
	deny    *authz.DenyList
	now     func() time.Time

	mu  sync.RWMutex
	doc string // compact JWS
	ver int64
	at  int64
}

// NewDenyListPublisher builds the publisher over the license signing key — the
// same key the JWKS already publishes, so a puller needs no second trust root.
func NewDenyListPublisher(signing keys.SigningKey, deny *authz.DenyList) *DenyListPublisher {
	return &DenyListPublisher{signing: signing, deny: deny, now: time.Now}
}

// SetClock replaces the publisher's clock. Tests only.
func (p *DenyListPublisher) SetClock(now func() time.Time) { p.now = now }

// Publish regenerates and signs the document from the current deny-list, and
// returns the compact JWS. Called at start and after every mutation (§5.8).
func (p *DenyListPublisher) Publish() (string, error) {
	subs, jkts, ver := p.deny.Snapshot()
	if subs == nil {
		subs = []string{}
	}
	if jkts == nil {
		jkts = []string{}
	}
	at := p.now().UnixMilli()

	payload, err := json.Marshal(DenyListDocument{
		Ver: ver, UpdatedAt: at, BannedSubs: subs, RevokedJKTs: jkts,
	})
	if err != nil {
		return "", fmt.Errorf("identity: marshal deny-list: %w", err)
	}
	jws, err := cjws.SignES256(p.signing.Key, payload, cjws.SignOptions{Type: DenyListType, KeyID: p.signing.KID})
	if err != nil {
		return "", fmt.Errorf("identity: sign deny-list: %w", err)
	}

	p.mu.Lock()
	p.doc, p.ver, p.at = jws, ver, at
	p.mu.Unlock()
	return jws, nil
}

// Document returns the cached JWS, publishing one on first use.
func (p *DenyListPublisher) Document() (string, error) {
	p.mu.RLock()
	doc := p.doc
	p.mu.RUnlock()
	if doc != "" {
		return doc, nil
	}
	return p.Publish()
}

// Version reports the published version and its timestamp.
func (p *DenyListPublisher) Version() (ver, updatedAt int64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ver, p.at
}

// --- handlers -----------------------------------------------------------------

// handleJWKS serves `GET /.well-known/catlog-jwks.json` (§4.5.1, §4.8): the
// public halves of every license signing key, active and retired, each with its
// kid.
func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	body, err := s.deps.Keys.JWKS()
	if err != nil {
		s.deps.Log.Error("rendering the jwks failed", "err", err)
		writeError(w, http.StatusInternalServerError, authz.CodeInternal, "could not render the key set")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", readCacheControl)
	_, _ = w.Write(body)
}

// handleDenyList serves `GET /.well-known/catlog-denylist.json` (§5.8).
//
// The body is a **compact JWS**, not a JSON object — §5.8 says "published as
// signed JWS", and the path is fixed by §4.8. Content-Type says so; see
// docs/DECISIONS.md.
func (s *Server) handleDenyList(w http.ResponseWriter, _ *http.Request) {
	doc, err := s.denylist.Document()
	if err != nil {
		s.deps.Log.Error("rendering the deny-list failed", "err", err)
		writeError(w, http.StatusInternalServerError, authz.CodeInternal, "could not render the deny-list")
		return
	}
	w.Header().Set("Content-Type", "application/jose")
	w.Header().Set("Cache-Control", readCacheControl)
	_, _ = w.Write([]byte(doc))
}
