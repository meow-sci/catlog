package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/config"
)

// idTokenAlgs is the algorithm allow-list for an IdP-issued `id_token`.
//
// Wider than catlog's own {ES256} (§4.5) because this is somebody else's
// signature: real Google signs with RS256, and mockidp signs with ES256 so the
// dev path exercises a key catlog actually has to fetch. It is still an
// allow-list, so `none` and HMAC confusion remain impossible.
var idTokenAlgs = []jose.SignatureAlgorithm{jose.RS256, jose.ES256}

// JWKSMinRefetch is the shortest interval between two fetches of the same JWKS
// URL. It bounds how much traffic an unknown `kid` can generate — the miss path
// refetches, so without a floor a stream of forged tokens would be a request
// amplifier.
const JWKSMinRefetch = 10 * time.Second

// JWKSMaxAge is how long a cached JWKS is used without revalidation.
const JWKSMaxAge = 10 * time.Minute

// JWKSCache fetches and caches an issuer's JWKS (§5.8).
//
// The cache refetches on a `kid` miss as well as on expiry, which is what makes
// key rotation — including mockidp generating a new key on every restart —
// recover on its own instead of needing catlogd restarted.
//
// A JWKSCache is safe for concurrent use.
type JWKSCache struct {
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*jwksEntry
}

type jwksEntry struct {
	set       *jose.JSONWebKeySet
	fetchedAt time.Time
}

// NewJWKSCache builds a cache over an HTTP client.
func NewJWKSCache(c *http.Client) *JWKSCache {
	if c == nil {
		c = NewIdPClient()
	}
	return &JWKSCache{client: c, now: time.Now, entries: map[string]*jwksEntry{}}
}

// KeysFor returns the JWKS at url, fetching it when the cache is empty, stale,
// or does not contain kid.
func (c *JWKSCache) KeysFor(ctx context.Context, url, kid string) (*jose.JSONWebKeySet, error) {
	c.mu.Lock()
	entry := c.entries[url]
	now := c.now()
	fresh := entry != nil && now.Sub(entry.fetchedAt) < JWKSMaxAge
	hasKID := entry != nil && (kid == "" || len(entry.set.Key(kid)) > 0)
	// A miss on a fresh-but-incomplete set still refetches, but not more often
	// than JWKSMinRefetch.
	mayRefetch := entry == nil || now.Sub(entry.fetchedAt) >= JWKSMinRefetch
	c.mu.Unlock()

	if fresh && hasKID {
		return entry.set, nil
	}
	if !mayRefetch && entry != nil {
		return entry.set, nil
	}

	set, err := c.fetch(ctx, url)
	if err != nil {
		if entry != nil {
			// Serving a stale key set beats failing every login while an IdP
			// is down; the signature check still has to pass.
			return entry.set, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.entries[url] = &jwksEntry{set: set, fetchedAt: now}
	c.mu.Unlock()
	return set, nil
}

func (c *JWKSCache) fetch(ctx context.Context, url string) (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("identity: jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	body, err := doJSON(c.client, req, "jwks endpoint")
	if err != nil {
		return nil, err
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("identity: jwks is not a JSON web key set: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("identity: jwks is empty")
	}
	return &set, nil
}

// idTokenClaims is the subset of an OIDC id_token catlog verifies (§4.7).
type idTokenClaims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
}

// googleSubject verifies an id_token against the configured issuer's JWKS and
// extracts `sub` (§4.7, §5.8).
//
// Four things are checked, in the order that costs least: the token parses
// under the allow-list, its signature verifies against a published key, `iss`
// is the configured issuer, `aud` is our client id, and `exp` has not passed.
// Google publishes no account age, so there is no age gate here — quotas only.
func googleSubject(ctx context.Context, jwks *JWKSCache, cfg config.Google, idToken string) (Subject, error) {
	if idToken == "" {
		return Subject{}, errors.New("identity: google returned no id_token")
	}
	if cfg.JWKSURL == "" {
		return Subject{}, errors.New("identity: google jwks_url is not configured")
	}

	tok, err := jose.ParseSigned(idToken, idTokenAlgs)
	if err != nil {
		return Subject{}, fmt.Errorf("identity: google id_token is unreadable: %w", err)
	}
	if len(tok.Signatures) != 1 {
		return Subject{}, errors.New("identity: google id_token must carry exactly one signature")
	}
	kid := tok.Signatures[0].Header.KeyID

	set, err := jwks.KeysFor(ctx, cfg.JWKSURL, kid)
	if err != nil {
		return Subject{}, err
	}
	candidates := set.Keys
	if kid != "" {
		if byKID := set.Key(kid); len(byKID) > 0 {
			candidates = byKID
		}
	}

	var payload []byte
	for _, k := range candidates {
		if payload, err = tok.Verify(k); err == nil {
			break
		}
		payload = nil
	}
	if payload == nil {
		return Subject{}, errors.New("identity: google id_token signature does not verify against the published keys")
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Subject{}, fmt.Errorf("identity: google id_token claims are not JSON: %w", err)
	}
	switch {
	case cfg.Issuer != "" && claims.Issuer != cfg.Issuer:
		return Subject{}, fmt.Errorf("identity: google id_token iss is not %q", cfg.Issuer)
	case claims.Audience != cfg.ClientID:
		return Subject{}, errors.New("identity: google id_token aud is not this application")
	// DELIBERATELY time.Now, not the injected server clock. This token was
	// minted by somebody else's process, on real wall time, and lives for
	// minutes. A catlogd running with an offset clock (`[server]
	// clock_control`, dev only) would otherwise reject every perfectly good
	// Google login as expired. The offset is catlog's own notion of now; it has
	// no authority over another issuer's clock. Please do not "fix" this.
	case claims.Expires == 0 || time.Now().Unix() >= claims.Expires:
		return Subject{}, errors.New("identity: google id_token has expired")
	case claims.Subject == "":
		return Subject{}, errors.New("identity: google id_token carries no sub")
	}
	// Zero CreatedAt: Google publishes no account age, and §4.7 gates Google on
	// quotas alone.
	return Subject{ID: claims.Subject}, nil
}
