package authz

import (
	"container/list"
	"crypto/sha256"
	"sync"
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
