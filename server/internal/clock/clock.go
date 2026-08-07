// Package clock is catlogd's single source of "now".
//
// # Why this exists
//
// Every timestamp catlog treats as authoritative is server-generated: an
// event's `recv_time`, a license's `iat`/`exp`, a session's expiry, the `Date`
// header a client resynchronises against. The client's own `wall_t` is carried
// but never trusted (§4.1). That is the correct design and it has one
// consequence: **the server's clock is the only thing that decides which day,
// week, month or year a leaderboard row belongs to.**
//
// Rolling daily/weekly/monthly/yearly aggregates therefore cannot be exercised
// at all without being able to move that clock. A test that has to wait a
// calendar year to find out whether the yearly board rolls over is not a test.
// So catlogd's notion of now becomes one injectable value, and a development
// build can offset it.
//
// # What this is not
//
// It is not a way to rewrite history. The offset applies to *new* reads of the
// clock; nothing already stored moves. And it is not reachable in production:
// [Clock.Controllable] is false unless `[server] clock_control = true`, which
// `Config.Validate` refuses to accept on an https deployment, and the route
// that drives it is on the loopback-only admin mux.
//
// # What it deliberately does not cover
//
// Time that belongs to somebody else. `identity/google.go` checks an IdP's
// `id_token` expiry against the real wall clock on purpose: that token was
// minted by another process on real time, and comparing it against an offset
// clock would reject a perfectly good login. Monotonic measurements
// (`time.Since` for a rebuild's duration, tickers, context deadlines) are
// likewise untouched — they measure elapsed real time and should.
package clock

import (
	"errors"
	"sync"
	"time"
)

// ErrNotControllable is returned by [Clock.Advance] and [Clock.SetTo] on a
// clock that was not built with control enabled.
var ErrNotControllable = errors.New("clock: this catlogd was not started with [server] clock_control = true")

// MaxOffset bounds how far the clock may be moved from real time. Ten years is
// far more than any simulation needs and small enough that an offset typo
// cannot push a unix millisecond timestamp somewhere absurd.
const MaxOffset = 10 * 365 * 24 * time.Hour

// Clock is a wall clock plus an offset. The zero value is not usable; call
// [New].
type Clock struct {
	// controllable is fixed at construction. A clock that cannot be controlled
	// is exactly time.Now with an extra function call, which is what every
	// production catlogd runs on.
	controllable bool

	mu     sync.RWMutex
	offset time.Duration
	// real is the underlying source, swappable only from tests in this package.
	real func() time.Time
}

// New builds a clock. Pass controllable = false for anything that is not a
// development build.
func New(controllable bool) *Clock {
	return &Clock{controllable: controllable, real: time.Now}
}

// Now is the clock's reading. This is the function every other package should
// be handed; its signature is deliberately the one `Deps.Now func() time.Time`
// already uses across the server, so wiring it is a one-line change per package
// rather than a new interface.
func (c *Clock) Now() time.Time {
	c.mu.RLock()
	offset := c.offset
	c.mu.RUnlock()
	return c.real().Add(offset)
}

// Controllable reports whether this clock will accept an offset.
func (c *Clock) Controllable() bool { return c.controllable }

// Offset is how far ahead of (or behind) real time the clock is running.
func (c *Clock) Offset() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.offset
}

// Advance moves the clock by d, relative to where it already is. Negative
// values are allowed — a simulation that wants to fill in "last month" needs to
// go backwards — but see the warning on [Clock.SetTo] about what a backwards
// jump does to in-flight credentials.
func (c *Clock) Advance(d time.Duration) (time.Time, error) {
	if !c.controllable {
		return time.Time{}, ErrNotControllable
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.offset + d
	if next > MaxOffset || next < -MaxOffset {
		return time.Time{}, errors.New("clock: offset would exceed ±10 years")
	}
	c.offset = next
	return c.real().Add(c.offset), nil
}

// SetTo jumps the clock to an absolute instant.
//
// # What a jump breaks, and why that is the caller's problem
//
// Moving the server clock forward past a credential's lifetime does exactly
// what moving a real clock forward would: §4.5.3 step 3 starts answering
// `license_expired` (licenses last 180 days by default) and website sessions
// (7 days) stop decoding. A harness simulating a year of play has to reissue as
// it goes, which is what a player does too. The proof-skew check (step 8) needs
// no help — the mod resynchronises from the `Date` header, which this clock
// also stamps, so clients follow the server automatically.
func (c *Clock) SetTo(t time.Time) (time.Time, error) {
	if !c.controllable {
		return time.Time{}, ErrNotControllable
	}
	return c.Advance(t.Sub(c.Now()))
}
