package authz

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/keys"
)

// LicenseCacheSize is the §4.5.3 step-2 cache capacity: 10k parsed licenses,
// keyed by the SHA-256 of the raw JWS string.
const LicenseCacheSize = 10_000

// licenseCache memoizes the expensive half of step 2 — the ECDSA verification
// and the claim unmarshal — for a license string that has already been seen.
//
// Only signature-verified claims go in, and nothing time-dependent is cached:
// `exp` (step 3), the deny-list (step 4) and the credential row (step 5) are
// re-evaluated on every request, so revoking or banning still takes effect
// immediately. A shipping client sends the same license on every batch, so the
// hit rate is essentially 100%.
type licenseCache struct {
	mu   sync.Mutex
	max  int
	ll   *list.List // front = most recently used
	byID map[[32]byte]*list.Element
}

type licenseEntry struct {
	key    [32]byte
	claims LicenseClaims
}

func newLicenseCache(max int) *licenseCache {
	if max <= 0 {
		max = LicenseCacheSize
	}
	return &licenseCache{max: max, ll: list.New(), byID: make(map[[32]byte]*list.Element, min(max, 1024))}
}

// cacheKey is the SHA-256 of the raw compact JWS, exactly as §4.5.3 step 2
// specifies.
func cacheKey(compact string) [32]byte { return sha256.Sum256([]byte(compact)) }

func (c *licenseCache) get(key [32]byte) (LicenseClaims, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[key]
	if !ok {
		return LicenseClaims{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*licenseEntry).claims, true
}

func (c *licenseCache) put(key [32]byte, claims LicenseClaims) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[key]; ok {
		el.Value.(*licenseEntry).claims = claims
		c.ll.MoveToFront(el)
		return
	}
	c.byID[key] = c.ll.PushFront(&licenseEntry{key: key, claims: claims})
	for c.ll.Len() > c.max {
		oldest := c.ll.Back()
		if oldest == nil {
			return
		}
		c.ll.Remove(oldest)
		delete(c.byID, oldest.Value.(*licenseEntry).key)
	}
}

func (c *licenseCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// --- credential cache --------------------------------------------------------

// CredentialCacheTTL bounds how long a verified credential row is trusted
// without a re-read. The deny-list version is the real invalidation — every
// ban, unban, revoke and purge bumps it — so the TTL is a safety net for a
// write that somehow reached the database without a deny-list refresh.
const CredentialCacheTTL = 60 * time.Second

// credentialCacheMax bounds the map. One entry per active credential, so
// hitting it means ten thousand distinct players in one TTL; dropping the lot
// costs each of them one re-read.
const credentialCacheMax = 10_000

// credEntry is one memoized step-5 success: the row facts the checks compared,
// plus when and against which deny-list version they were true.
type credEntry struct {
	playerID int64
	handle   string
	userKey  keys.UserKey
	at       time.Time
	denyVer  int64
}

// credCache memoizes §4.5.3 step 5 — CredentialByJKT plus PlayerByID — which
// every accepted batch was paying as two point queries. Only successes are
// cached: a hit stands in for "credential live, handle and player agree, not
// banned", and is honoured only while the deny-list version matches and the
// entry is younger than [CredentialCacheTTL].
type credCache struct {
	mu sync.Mutex
	m  map[string]credEntry
}

func newCredCache() *credCache {
	return &credCache{m: make(map[string]credEntry, 256)}
}

// get returns the cached player id when every fact the fill checked still
// matches this request's license.
func (c *credCache) get(jkt, handle string, userKey keys.UserKey, now time.Time, denyVer int64) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[jkt]
	if !ok || e.denyVer != denyVer || now.Sub(e.at) >= CredentialCacheTTL ||
		e.handle != handle || e.userKey != userKey {
		return 0, false
	}
	return e.playerID, true
}

func (c *credCache) put(jkt string, e credEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= credentialCacheMax {
		clear(c.m)
	}
	c.m[jkt] = e
}
