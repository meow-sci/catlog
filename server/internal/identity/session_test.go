package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/keys"
)

func testSessions(t *testing.T, secure bool) (*Sessions, keys.UserKey, time.Time) {
	t.Helper()
	key := make([]byte, keys.SecretLen)
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	s, err := NewSessions(key, secure)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	var uk keys.UserKey
	for i := range uk {
		uk[i] = byte(i)
	}
	return s, uk, now
}

// TestSessionCookieShape pins the §4.5.4 construction byte for byte:
// b64u(user_key) "." exp "." b64u(HMAC-SHA256(session_key, user_key || exp)).
func TestSessionCookieShape(t *testing.T) {
	s, uk, now := testSessions(t, false)
	exp := now.Add(SessionTTL)

	value := s.Encode(uk, exp)
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		t.Fatalf("cookie has %d parts, want 3: %q", len(parts), value)
	}
	if parts[0] != cjws.B64U(uk[:]) {
		t.Errorf("part 1 = %q, want b64u(user_key) %q", parts[0], cjws.B64U(uk[:]))
	}
	if parts[1] != strconv.FormatInt(exp.Unix(), 10) {
		t.Errorf("part 2 = %q, want exp %d", parts[1], exp.Unix())
	}

	// The MAC, recomputed here from the spec rather than from the code.
	mac := hmac.New(sha256.New, s.key)
	mac.Write(uk[:])
	mac.Write([]byte(strconv.FormatInt(exp.Unix(), 10)))
	if parts[2] != cjws.B64U(mac.Sum(nil)) {
		t.Errorf("part 3 = %q, want the HMAC over user_key||exp", parts[2])
	}

	// The TTL is 7 days (§4.5.4).
	if got := exp.Sub(now); got != 7*24*time.Hour {
		t.Errorf("session TTL = %s, want 168h", got)
	}
}

// TestSessionRoundTrip is the happy path.
func TestSessionRoundTrip(t *testing.T) {
	s, uk, now := testSessions(t, false)
	got, err := s.Decode(s.Encode(uk, now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != uk {
		t.Errorf("round-tripped user_key = %s, want %s", got, uk)
	}
}

// TestSessionForgeryIsRejected is the §12 WP3 forge-rejection test: every way a
// cookie can be tampered with must fail closed.
func TestSessionForgeryIsRejected(t *testing.T) {
	s, uk, now := testSessions(t, false)
	exp := now.Add(time.Hour)
	valid := s.Encode(uk, exp)
	parts := strings.Split(valid, ".")

	// A second user_key, to swap in.
	var other keys.UserKey
	for i := range other {
		other[i] = byte(0xFF - i)
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"no separators", cjws.B64U(uk[:])},
		{"two parts", parts[0] + "." + parts[1]},
		{"four parts", valid + ".extra"},
		{"user_key swapped, original mac", cjws.B64U(other[:]) + "." + parts[1] + "." + parts[2]},
		{"expiry extended, original mac", parts[0] + "." + strconv.FormatInt(exp.Add(time.Hour).Unix(), 10) + "." + parts[2]},
		{"expiry rewritten to the far future", parts[0] + ".99999999999." + parts[2]},
		{"mac from a different session key", parts[0] + "." + parts[1] + "." + cjws.B64U(make([]byte, 32))},
		{"mac truncated", parts[0] + "." + parts[1] + "." + parts[2][:20]},
		{"mac not base64url", parts[0] + "." + parts[1] + ".!!!!"},
		{"user_key not base64url", "!!!." + parts[1] + "." + parts[2]},
		{"user_key wrong length", cjws.B64U(uk[:16]) + "." + parts[1] + "." + parts[2]},
		{"expiry not a number", parts[0] + ".tomorrow." + parts[2]},
		{"expired", s.Encode(uk, now.Add(-time.Second))},
		{"expired exactly now", s.Encode(uk, now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Decode(tc.value)
			if err == nil {
				t.Fatalf("Decode(%q) succeeded and returned %s; a forged cookie must be rejected", tc.value, got)
			}
			if got != (keys.UserKey{}) {
				t.Errorf("Decode returned a non-zero user_key alongside an error")
			}
		})
	}

	// A cookie signed with a *different* session key must not verify — the
	// case a stolen cookie from another deployment represents.
	otherKey := make([]byte, keys.SecretLen)
	otherSessions, err := NewSessions(otherKey, false)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	otherSessions.SetClock(func() time.Time { return now })
	if _, err := s.Decode(otherSessions.Encode(uk, exp)); err == nil {
		t.Error("a cookie signed with another server's session key verified")
	}
}

// TestSessionCookieAttributes checks the §4.5.4 attributes in both deployment
// modes.
func TestSessionCookieAttributes(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "dev", true: "prod"}[secure], func(t *testing.T) {
			s, uk, _ := testSessions(t, secure)
			rec := httptest.NewRecorder()
			s.Issue(rec, uk)

			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("Issue set %d cookies, want 1", len(cookies))
			}
			c := cookies[0]

			wantName := SessionCookie
			if secure {
				wantName = SessionCookieSecure
			}
			if c.Name != wantName {
				t.Errorf("name = %q, want %q", c.Name, wantName)
			}
			if !c.HttpOnly {
				t.Error("cookie is not HttpOnly (§4.5.4)")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax (§4.5.4)", c.SameSite)
			}
			if c.Path != "/" {
				t.Errorf("Path = %q, want / (§4.5.4)", c.Path)
			}
			if c.Secure != secure {
				t.Errorf("Secure = %v, want %v", c.Secure, secure)
			}
			if c.MaxAge != int(SessionTTL/time.Second) {
				t.Errorf("Max-Age = %d, want %d", c.MaxAge, int(SessionTTL/time.Second))
			}
		})
	}
}

// TestSessionClearExpiresTheCookie checks logout actually removes it.
func TestSessionClearExpiresTheCookie(t *testing.T) {
	s, _, _ := testSessions(t, false)
	rec := httptest.NewRecorder()
	s.Clear(rec)

	c := rec.Result().Cookies()[0]
	if c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("Clear wrote value %q max-age %d, want an immediate expiry", c.Value, c.MaxAge)
	}
}

// TestSessionFromRequest covers the ErrNoSession / forged distinction the
// dashboard gate relies on.
func TestSessionFromRequest(t *testing.T) {
	s, uk, now := testSessions(t, false)

	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if _, err := s.From(r); err != ErrNoSession {
		t.Errorf("no cookie: err = %v, want ErrNoSession", err)
	}

	r.AddCookie(&http.Cookie{Name: s.CookieName(), Value: s.Encode(uk, now.Add(time.Hour))})
	got, err := s.From(r)
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if got != uk {
		t.Errorf("user_key = %s, want %s", got, uk)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	bad.AddCookie(&http.Cookie{Name: s.CookieName(), Value: "forged.1.aaaa"})
	if _, err := s.From(bad); err == nil || err == ErrNoSession {
		t.Errorf("forged cookie: err = %v, want a verification failure", err)
	}
}

// TestOAuthState pins the §4.5.4 state contract: 32 random bytes, a short-lived
// cookie, single use, compared on callback, and bound to the IdP that minted it.
func TestOAuthState(t *testing.T) {
	s, _, _ := testSessions(t, false)

	rec := httptest.NewRecorder()
	state, err := s.NewState(rec, IdPDiscord)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	raw, err := cjws.DecodeB64U(state)
	if err != nil || len(raw) != StateBytes {
		t.Fatalf("state decodes to %d bytes (err %v), want %d", len(raw), err, StateBytes)
	}

	c := rec.Result().Cookies()[0]
	if c.Name != s.StateCookieName() || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie = %+v, want an HttpOnly Lax cookie named %s", c, s.StateCookieName())
	}
	if c.MaxAge != int(StateTTL/time.Second) {
		t.Errorf("state cookie max-age = %d, want %d", c.MaxAge, int(StateTTL/time.Second))
	}

	withCookie := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/auth/discord/callback", nil)
		r.AddCookie(c)
		return r
	}

	if err := s.CheckState(httptest.NewRecorder(), withCookie(), IdPDiscord, state); err != nil {
		t.Errorf("matching state rejected: %v", err)
	}
	if err := s.CheckState(httptest.NewRecorder(), withCookie(), IdPDiscord, "not-the-state"); err == nil {
		t.Error("a mismatched state was accepted")
	}
	if err := s.CheckState(httptest.NewRecorder(), withCookie(), IdPDiscord, ""); err == nil {
		t.Error("an empty state was accepted")
	}
	if err := s.CheckState(httptest.NewRecorder(), withCookie(), IdPGitHub, state); err == nil {
		t.Error("a discord state completed a github callback")
	}
	if err := s.CheckState(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), IdPDiscord, state); err == nil {
		t.Error("a callback with no state cookie was accepted")
	}

	// Two states are never the same value.
	other, err := s.NewState(httptest.NewRecorder(), IdPDiscord)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if other == state {
		t.Error("two states collided; they are supposed to be 32 random bytes")
	}
}

// TestNewSessionsRejectsShortKey guards against a truncated session.key.
func TestNewSessionsRejectsShortKey(t *testing.T) {
	if _, err := NewSessions(make([]byte, 16), false); err == nil {
		t.Error("a 16-byte session key was accepted; §4.5.4 requires 32")
	}
}
