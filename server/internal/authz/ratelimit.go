package authz

import (
	"math"
	"sync"
	"time"
)

// maxBuckets caps the rate limiter's map. Reaching it triggers a sweep of every
// bucket that has refilled to full, which is exact rather than approximate: a
// full bucket is indistinguishable from a bucket that was never created.
const maxBuckets = 50_000

// Limiter is the per-credential token bucket of §4.3: 1 batch / 2 s with a
// burst of 5, keyed by `jkt`.
//
// It is consulted at §4.5.3 step 9 — after the signatures prove who is asking,
// before the body is read, so a flood costs the server two ECDSA verifications
// and nothing else.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter returns a limiter granting rate tokens per second up to burst.
func NewLimiter(rate float64, burst int) *Limiter {
	if rate <= 0 {
		rate = 0.5 // §4.3: one batch per two seconds
	}
	if burst <= 0 {
		burst = 5
	}
	return &Limiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
}

// Allow takes one token for key. When it returns false, retryAfter is how long
// until a token exists — the value of the Retry-After header (§4.4).
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= maxBuckets {
			l.sweepLocked(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// Round up to whole seconds: Retry-After is expressed in seconds, and a 0
	// there invites an immediate retry that fails again.
	wait := time.Duration(math.Ceil((1-b.tokens)/l.rate)) * time.Second
	if wait <= 0 {
		wait = time.Second
	}
	return false, wait
}

// sweepLocked drops every bucket that has refilled to full. Such a bucket
// carries no state — recreating it yields the same result — so this is a
// lossless eviction, not an approximation.
func (l *Limiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// Len reports how many buckets are live. For tests and /admin/stats.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
