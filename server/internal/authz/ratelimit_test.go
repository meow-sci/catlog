package authz

import (
	"strconv"
	"testing"
	"time"
)

// TestLimiterBurstAndRefill pins the §4.3 bucket exactly: burst 5, then one
// batch every two seconds.
func TestLimiterBurstAndRefill(t *testing.T) {
	now := time.Unix(1_770_000_000, 0)
	l := NewLimiter(0.5, 5)
	l.now = func() time.Time { return now }

	for i := range 5 {
		if ok, _ := l.Allow("jkt-a"); !ok {
			t.Fatalf("burst request %d was refused", i+1)
		}
	}
	ok, retry := l.Allow("jkt-a")
	if ok {
		t.Fatal("the sixth request in the same instant was allowed")
	}
	if retry != 2*time.Second {
		t.Errorf("Retry-After = %s, want 2s (one token at 0.5/s)", retry)
	}

	// A different credential has its own bucket.
	if ok, _ := l.Allow("jkt-b"); !ok {
		t.Error("a second credential was rate limited by the first")
	}

	// One token appears two seconds later, and only one.
	now = now.Add(2 * time.Second)
	if ok, _ := l.Allow("jkt-a"); !ok {
		t.Error("no token after 2 s")
	}
	if ok, _ := l.Allow("jkt-a"); ok {
		t.Error("two tokens after 2 s; the rate is 0.5/s")
	}

	// The bucket refills to the burst ceiling and no further.
	now = now.Add(time.Hour)
	for i := range 5 {
		if ok, _ := l.Allow("jkt-a"); !ok {
			t.Fatalf("request %d after a long idle period was refused", i+1)
		}
	}
	if ok, _ := l.Allow("jkt-a"); ok {
		t.Error("burst exceeded 5 after a long idle period")
	}
}

// TestLimiterSweepIsLossless proves the map cannot grow without bound and that
// evicting a full bucket changes nothing observable.
func TestLimiterSweepIsLossless(t *testing.T) {
	now := time.Unix(1_770_000_000, 0)
	l := NewLimiter(0.5, 5)
	l.now = func() time.Time { return now }

	// One credential holds a partially drained bucket.
	for range 5 {
		l.Allow("busy")
	}

	// Everything else touches its bucket once, long enough ago to be full again.
	for i := range 100 {
		l.Allow("idle-" + strconv.Itoa(i))
	}
	now = now.Add(time.Hour)
	l.mu.Lock()
	l.sweepLocked(now)
	l.mu.Unlock()

	if l.Len() != 0 {
		t.Errorf("sweep left %d buckets; every one had refilled", l.Len())
	}
	// The swept "busy" bucket also refilled during that hour, so it is
	// indistinguishable from a fresh one — which is exactly why dropping it is
	// lossless.
	if ok, _ := l.Allow("busy"); !ok {
		t.Error("a refilled credential was refused after its bucket was swept")
	}
}

// TestLicenseCacheEvictsLeastRecentlyUsed pins the LRU behaviour of §4.5.3
// step 2's 10k cache.
func TestLicenseCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newLicenseCache(2)
	a, b, d := cacheKey("a"), cacheKey("b"), cacheKey("c")

	c.put(a, LicenseClaims{JTI: "a"})
	c.put(b, LicenseClaims{JTI: "b"})
	if _, ok := c.get(a); !ok { // a becomes the most recently used
		t.Fatal("a was evicted early")
	}
	c.put(d, LicenseClaims{JTI: "c"})

	if _, ok := c.get(b); ok {
		t.Error("b survived; it was the least recently used")
	}
	if got, ok := c.get(a); !ok || got.JTI != "a" {
		t.Error("a was evicted despite being used most recently")
	}
	if got, ok := c.get(d); !ok || got.JTI != "c" {
		t.Error("c is missing")
	}
	if c.len() != 2 {
		t.Errorf("cache holds %d entries, want 2", c.len())
	}
}
