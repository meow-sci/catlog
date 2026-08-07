package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// Session cookie names (§4.5.4). The `__Host-` prefix is used whenever the site
// is served over https: browsers refuse to accept such a cookie unless it is
// Secure, Path=/ and has no Domain, which makes a subdomain unable to plant one.
const (
	SessionCookie       = "catlog_sess"
	SessionCookieSecure = "__Host-catlog_sess"
	StateCookie         = "catlog_oauth"
	StateCookieSecure   = "__Host-catlog_oauth"
)

// SessionTTL is the §4.5.4 session lifetime.
const SessionTTL = 7 * 24 * time.Hour

// StateTTL bounds how long an OAuth `state` cookie stays valid. A login that
// takes longer than this has to be restarted, which is cheap; leaving the
// cookie around for the session's lifetime would not be.
const StateTTL = 10 * time.Minute

// StateBytes is the §4.5.4 OAuth state length.
const StateBytes = 32

// ErrNoSession means the request carried no session cookie at all — as opposed
// to carrying one that failed to authenticate.
var ErrNoSession = errors.New("identity: no session cookie")

// Sessions mints and verifies the §4.5.4 website session cookie.
//
// # The cookie
//
//	b64u(user_key) "." exp_unix "." b64u(HMAC-SHA256(session_key, user_key_bytes || exp))
//
// The MAC covers the user_key bytes concatenated with the **decimal ASCII** of
// `exp` — the same characters the cookie itself carries, so what is verified is
// exactly what was presented (§4.5.4 writes `user_key_bytes || exp` without
// pinning an integer encoding; see docs/DECISIONS.md).
//
// A Sessions is safe for concurrent use.
type Sessions struct {
	key    []byte
	secure bool
	now    func() time.Time
}

// NewSessions builds the session codec over the 32-byte session key. secure
// turns on the `Secure` attribute and the `__Host-` cookie prefix; catlogd sets
// it when the configured base URL is https (§4.5.4).
func NewSessions(sessionKey []byte, secure bool) (*Sessions, error) {
	if len(sessionKey) != keys.SecretLen {
		return nil, fmt.Errorf("identity: session key is %d bytes, want %d", len(sessionKey), keys.SecretLen)
	}
	return &Sessions{key: append([]byte(nil), sessionKey...), secure: secure, now: time.Now}, nil
}

// SetClock replaces the session clock. Tests only.
func (s *Sessions) SetClock(now func() time.Time) { s.now = now }

// CookieName is the session cookie's name for this deployment (§4.5.4).
func (s *Sessions) CookieName() string {
	if s.secure {
		return SessionCookieSecure
	}
	return SessionCookie
}

// StateCookieName is the OAuth state cookie's name for this deployment.
func (s *Sessions) StateCookieName() string {
	if s.secure {
		return StateCookieSecure
	}
	return StateCookie
}

// Encode builds the cookie value for uk, expiring at exp.
func (s *Sessions) Encode(uk keys.UserKey, exp time.Time) string {
	expUnix := exp.Unix()
	return cjws.B64U(uk[:]) + "." + strconv.FormatInt(expUnix, 10) + "." + cjws.B64U(s.mac(uk, expUnix))
}

// Decode authenticates a cookie value and returns the user_key it carries.
//
// Every failure — wrong shape, bad base64, wrong length, forged MAC, expired —
// returns an error and no user_key. There is deliberately no "expired but
// otherwise valid" outcome: an expired cookie is not a session.
func (s *Sessions) Decode(value string) (keys.UserKey, error) {
	var uk keys.UserKey

	rawKey, rest, ok := strings.Cut(value, ".")
	if !ok {
		return uk, errors.New("identity: session cookie is malformed")
	}
	rawExp, rawMAC, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(rawMAC, ".") {
		return uk, errors.New("identity: session cookie is malformed")
	}

	uk, err := keys.ParseUserKey(rawKey)
	if err != nil {
		return keys.UserKey{}, errors.New("identity: session cookie has no user key")
	}
	exp, err := strconv.ParseInt(rawExp, 10, 64)
	if err != nil {
		return keys.UserKey{}, errors.New("identity: session cookie has no expiry")
	}
	mac, err := cjws.DecodeB64U(rawMAC)
	if err != nil {
		return keys.UserKey{}, errors.New("identity: session cookie signature is not base64url")
	}

	// Constant time, and before the expiry check: a forged cookie must not be
	// able to learn anything from the order in which it is rejected.
	if subtle.ConstantTimeCompare(mac, s.mac(uk, exp)) != 1 {
		return keys.UserKey{}, errors.New("identity: session cookie signature does not verify")
	}
	if s.now().Unix() >= exp {
		return keys.UserKey{}, errors.New("identity: session has expired")
	}
	return uk, nil
}

func (s *Sessions) mac(uk keys.UserKey, exp int64) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(uk[:])
	h.Write([]byte(strconv.FormatInt(exp, 10)))
	return h.Sum(nil)
}

// Issue writes a fresh session cookie for uk (§4.5.4).
func (s *Sessions) Issue(w http.ResponseWriter, uk keys.UserKey) time.Time {
	exp := s.now().Add(SessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     s.CookieName(),
		Value:    s.Encode(uk, exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return exp
}

// Clear expires the session cookie — logout and delete-my-data (§4.8).
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.CookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// From reads and authenticates the session on a request. It returns
// [ErrNoSession] when there is no cookie, so a handler can tell "logged out"
// from "presented something forged".
func (s *Sessions) From(r *http.Request) (keys.UserKey, error) {
	c, err := r.Cookie(s.CookieName())
	if err != nil {
		return keys.UserKey{}, ErrNoSession
	}
	return s.Decode(c.Value)
}

// --- OAuth state (§4.5.4) -----------------------------------------------------

// NewState mints 32 random bytes and stores them in a short-lived cookie
// alongside the IdP the flow belongs to. The callback compares both, which is
// what stops an attacker completing someone else's login (or swapping in a
// different provider's code).
func (s *Sessions) NewState(w http.ResponseWriter, idp string) (string, error) {
	b := make([]byte, StateBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: mint oauth state: %w", err)
	}
	state := cjws.B64U(b)
	http.SetCookie(w, &http.Cookie{
		Name:     s.StateCookieName(),
		Value:    state + "." + idp,
		Path:     "/",
		MaxAge:   int(StateTTL / time.Second),
		HttpOnly: true,
		Secure:   s.secure,
		// Lax, not Strict: the callback is a top-level navigation *from the
		// IdP*, and a Strict cookie would not be sent with it.
		SameSite: http.SameSiteLaxMode,
	})
	return state, nil
}

// CheckState compares the callback's `state` parameter with the cookie and
// clears the cookie either way — a state is single-use.
func (s *Sessions) CheckState(w http.ResponseWriter, r *http.Request, idp, got string) error {
	s.clearState(w)

	c, err := r.Cookie(s.StateCookieName())
	if err != nil {
		return errors.New("identity: no oauth state cookie; start the login again")
	}
	want, cookieIdP, ok := strings.Cut(c.Value, ".")
	if !ok {
		return errors.New("identity: oauth state cookie is malformed")
	}
	if cookieIdP != idp {
		return fmt.Errorf("identity: oauth state belongs to the %s flow", cookieIdP)
	}
	if got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return errors.New("identity: oauth state does not match")
	}
	return nil
}

func (s *Sessions) clearState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.StateCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}
