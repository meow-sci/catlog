package authz

import (
	"context"
	"testing"
	"time"
)

// The tests below pin what happens to an in-flight credential when the
// **server** clock moves forward — which is what `[server] clock_control` and
// `internal/clock` exist to make possible, and what any simulation of months or
// years of play does on purpose.
//
// This is the one behaviour a long-span harness has to be built around, and it
// is not obvious from reading §4.5.3 top to bottom: the verification chain
// checks license expiry at **step 3** and proof skew at **step 8**, so a
// server that has jumped forward answers `license_expired` — not the
// `clock_skew` a reader might expect, and not the code the mod knows how to
// recover from. `clock_skew` is the only 401 the shipper retries; everything
// else latches it dead for the session. A harness that advances the clock and
// does not reissue therefore does not get slow, it stops.

// TestForwardClockJumpExpiresTheLicenseBeforeItReportsSkew is the sharp edge:
// both conditions are true at once, and the chain reports the one the client
// cannot recover from.
func TestForwardClockJumpExpiresTheLicenseBeforeItReportsSkew(t *testing.T) {
	f := newFixture(t)

	// A perfectly good request, built at the time the credential was issued.
	license := f.license(t, nil)
	proof := f.proof(t, nil)

	// Baseline: it verifies now.
	if _, err := f.v.Verify(context.Background(), Request{License: license, Proof: proof}); err != nil {
		t.Fatalf("the baseline request did not verify: %v", err)
	}

	// The server clock jumps a year. The license was minted with a 180-day TTL
	// and the proof's iat is a year behind, so step 3 and step 8 are both
	// violated — which is exactly why the ordering matters.
	jumped := f.now.Add(365 * 24 * time.Hour)
	f.v.SetClock(func() time.Time { return jumped })

	_, err := f.v.Verify(context.Background(), Request{License: license, Proof: proof})
	if err == nil {
		t.Fatal("a year-old license and a year-old proof verified after the clock jumped")
	}
	if err.Code != CodeLicenseExpired {
		t.Errorf("code = %q, want %q — §4.5.3 checks license expiry (step 3) before proof skew (step 8), "+
			"and a harness that moves the clock must reissue rather than expect a recoverable clock_skew",
			err.Code, CodeLicenseExpired)
	}
}

// TestASmallForwardJumpIsRecoverableSkew is the other half, and the reason the
// mod survives an ordinary clock disagreement: inside the license's lifetime, a
// jump past the ±300 s window is `clock_skew`, which the shipper heals from by
// re-reading the Date header.
func TestASmallForwardJumpIsRecoverableSkew(t *testing.T) {
	f := newFixture(t)
	license := f.license(t, nil)
	proof := f.proof(t, nil)

	// Well inside the 180-day license, well outside the 300-second skew window.
	f.v.SetClock(func() time.Time { return f.now.Add(time.Hour) })

	_, err := f.v.Verify(context.Background(), Request{License: license, Proof: proof})
	if err == nil {
		t.Fatal("an hour-stale proof verified")
	}
	if err.Code != CodeClockSkew {
		t.Errorf("code = %q, want %q — this is the only 401 the mod recovers from", err.Code, CodeClockSkew)
	}
}

// TestReissuingAfterAJumpRestoresService states the remedy the harness has to
// implement, as a test rather than as a comment: mint the credential and the
// proof at the server's *new* now and everything verifies again.
func TestReissuingAfterAJumpRestoresService(t *testing.T) {
	f := newFixture(t)

	jumped := f.now.Add(2 * 365 * 24 * time.Hour)
	f.v.SetClock(func() time.Time { return jumped })

	// Reissued at the clock's new reading, exactly as a player signing in again
	// on that day would get.
	license := f.license(t, func(c map[string]any) {
		c["iat"] = jumped.Add(-time.Hour).Unix()
		c["exp"] = jumped.Add(180 * 24 * time.Hour).Unix()
	})
	proof := f.proof(t, func(c map[string]any) {
		c["iat"] = jumped.Unix()
	})

	if _, err := f.v.Verify(context.Background(), Request{License: license, Proof: proof}); err != nil {
		t.Fatalf("a credential reissued at the new clock did not verify: %v (%s)", err, err.Code)
	}
}

// TestTheRateLimiterSurvivesAForwardJump: the token bucket measures absolute
// time deltas, so a jump refills every bucket rather than wedging one. Worth
// pinning — a limiter that answered Retry-After in months after a clock move
// would stall a simulation for no reason.
func TestTheRateLimiterSurvivesAForwardJump(t *testing.T) {
	f := newFixture(t)
	license := f.license(t, nil)

	// Spend the burst.
	for range 5 {
		if _, err := f.v.Verify(context.Background(),
			Request{License: license, Proof: f.proof(t, nil)}); err != nil && err.Code == CodeRateLimited {
			t.Fatalf("the burst was exhausted early: %v", err)
		}
	}

	jumped := f.now.Add(30 * 24 * time.Hour)
	f.v.SetClock(func() time.Time { return jumped })

	// Reissued for the new clock so the only thing under test is the limiter.
	license = f.license(t, func(c map[string]any) {
		c["iat"] = jumped.Add(-time.Hour).Unix()
		c["exp"] = jumped.Add(180 * 24 * time.Hour).Unix()
	})
	proof := f.proof(t, func(c map[string]any) { c["iat"] = jumped.Unix() })

	if _, err := f.v.Verify(context.Background(),
		Request{License: license, Proof: proof}); err != nil && err.Code == CodeRateLimited {
		t.Errorf("the limiter was still throttling a month later: %v", err)
	}
}
