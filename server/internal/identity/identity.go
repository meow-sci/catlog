// Package identity implements the Discord/Google/GitHub OAuth flows, user_key
// derivation, handle rules and website sessions (§4.7, §5.8).
//
// # One engine, three providers
//
// All three IdPs run the same OAuth2 authorization-code flow, parameterised
// from `[idp.*]` in the config (§5.3) so every URL points at `mockidp` in dev
// and at the real provider in production — the code cannot tell the difference,
// which is what makes D2's "100% local" promise testable rather than aspirational.
// Only two things differ per provider: where the stable subject comes from
// (Discord and GitHub: one API call; Google: a signed `id_token` verified
// against the issuer's JWKS) and how account age is read (§4.7).
//
// # What catlog learns about a player
//
// A subject string, and nothing else. No email is ever requested, from any
// provider, in any scope (D17) — `mockidp` refuses an authorize request that
// asks for one, so the rule is enforced rather than merely written down. The
// subject is immediately hashed into `user_key = HMAC-SHA256(pepper, "<idp>:<sub>")`
// and discarded along with the IdP's tokens (§4.7), so nothing in the system
// can walk back from a stored row to an account at Discord.
//
// # Sessions
//
// The website session is a signed cookie, not a server-side table: catlog has
// no session store, and §4.5.4's construction means a restart does not log
// everyone out. CSRF is `http.CrossOriginProtection` on every mutating route
// plus SameSite=Lax cookies; the OAuth `state` is its own short-lived cookie.
//
// # Where writes go
//
// Every write here goes through [Deps.WriteLock] — the same §5.4 admin mutex
// the admin API uses — because events.db has exactly one writer connection and
// two unsynchronised writers would serialise on the pool in an order nobody
// chose.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// readCacheControl is the §4.8 cache header. The well-known documents are
// public and change rarely; everything under /api/ is `no-store`.
const readCacheControl = "public, s-maxage=30, stale-while-revalidate=300"

// MaxRequestBytes caps a dashboard API request body. A public JWK is a few
// hundred bytes; this is three orders of magnitude of headroom.
const MaxRequestBytes = 64 << 10

// DashboardPath is where a completed login lands (§5.8). WP5 renders it.
const DashboardPath = "/dashboard"

// WriteLock serializes writes to events.db that do not go through the ingest
// writer goroutine — the §5.4 admin mutex. catlogd passes the admin API's, so
// an admin ban and a dashboard handle claim cannot interleave.
type WriteLock func(fn func() error) error

// Deps is what the identity layer needs from the rest of the server.
type Deps struct {
	// Config supplies the IdP endpoints, the issuer, the quotas and the TTL.
	Config config.Config
	// Keys derives user_key, signs licenses and the deny-list, and holds the
	// session key. Required.
	Keys *keys.Set
	// Events is events.db. Required.
	Events *store.Events
	// Deny is the §5.8 in-memory set. Required: every ban and revoke must
	// reach it as well as the database.
	Deny *authz.DenyList
	// Directory is reloaded whenever a handle appears or disappears (§5.4).
	Directory *directory.Directory
	// WriteLock serializes events.db writes. Defaults to a private mutex,
	// which is correct only when nothing else writes — catlogd passes the
	// admin API's.
	WriteLock WriteLock
	// Archive is the purge seam (WP10). Optional; nil means a purge has no
	// archive to delete.
	Archive ArchivePurger
	// HTTPClient calls the IdPs. Defaults to [NewIdPClient].
	HTTPClient *http.Client
	// Log receives one line per login, claim and moderation event.
	Log *slog.Logger
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

// Server serves the §4.8 identity routes: the OAuth flows, the dashboard JSON
// API and the two well-known documents.
type Server struct {
	deps      Deps
	sessions  *Sessions
	rules     HandleRules
	providers map[string]*Provider
	jwks      *JWKSCache
	denylist  *DenyListPublisher
	moderator *Moderator
	csrf      *http.CrossOriginProtection
	// errorPage is WP5's login-failure template, installed by [Server.SetErrorPage].
	// nil means the built-in page in [Server.authError] answers instead.
	errorPage ErrorPage

	ownLock sync.Mutex
}

// New builds the identity server.
func New(deps Deps) (*Server, error) {
	switch {
	case deps.Keys == nil:
		return nil, errors.New("identity: Keys is required")
	case deps.Events == nil:
		return nil, errors.New("identity: Events is required")
	case deps.Deny == nil:
		return nil, errors.New("identity: Deny is required")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = NewIdPClient()
	}

	// `Secure` and the `__Host-` prefix follow the deployment's scheme: a dev
	// server on http://127.0.0.1 cannot set a Secure cookie at all, and a
	// production server must (§4.5.4).
	secure := strings.HasPrefix(deps.Config.Server.BaseURL, "https://")
	sessions, err := NewSessions(deps.Keys.SessionKey(), secure)
	if err != nil {
		return nil, err
	}
	sessions.SetClock(deps.Now)

	jwks := NewJWKSCache(deps.HTTPClient)
	denylist := NewDenyListPublisher(deps.Keys.Signing, deps.Deny)
	denylist.SetClock(deps.Now)

	moderator := NewModerator(deps.Events, deps.Deny, deps.Directory, denylist, deps.Archive, deps.Log)
	moderator.SetClock(deps.Now)

	s := &Server{
		deps:     deps,
		sessions: sessions,
		rules: NewHandleRules(deps.Config.Auth.ReservedHandles,
			deps.Config.Auth.HandleQuota, deps.Config.Auth.IssuancePerDay, deps.Config.Auth.MinAccountAgeDays),
		providers: Providers(deps.Config, jwks),
		jwks:      jwks,
		denylist:  denylist,
		moderator: moderator,
		csrf:      newCSRF(deps.Config.Server.BaseURL, deps.Log),
	}
	if s.deps.WriteLock == nil {
		s.deps.WriteLock = func(fn func() error) error {
			s.ownLock.Lock()
			defer s.ownLock.Unlock()
			return fn()
		}
	}
	// Publish once at start so the document exists before the first ban.
	if _, err := denylist.Publish(); err != nil {
		return nil, err
	}
	return s, nil
}

// Moderator exposes the ban/unban/purge service so the admin API can mount its
// §5.9 routes over exactly the code the dashboard's delete-my-data uses.
func (s *Server) Moderator() *Moderator { return s.moderator }

// Sessions exposes the session codec, which WP5's dashboard handlers need to
// gate a page on a login.
func (s *Server) Sessions() *Sessions { return s.sessions }

// DenyListPublisher exposes the §5.8 publisher.
func (s *Server) DenyListPublisher() *DenyListPublisher { return s.denylist }

// Register mounts the §4.8 identity routes on a mux.
//
// Mutating routes are wrapped in [http.CrossOriginProtection]; safe methods are
// not, because §4.8's GETs change nothing and the protection lets them through
// anyway.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/{idp}/start", s.handleStart)
	mux.HandleFunc("GET /auth/{idp}/callback", s.handleCallback)

	mux.HandleFunc("GET "+JWKSPath, s.handleJWKS)
	mux.HandleFunc("GET "+DenyListPath, s.handleDenyList)

	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/handles", s.handleListHandles)

	s.post(mux, "POST /api/handles", s.handleClaim)
	s.post(mux, "POST /api/handles/{handle}/reissue", s.handleReissue)
	s.post(mux, "POST /api/handles/{handle}/revoke", s.handleRevoke)
	s.post(mux, "POST /api/me/delete", s.handleDelete)
	s.post(mux, "POST /api/logout", s.handleLogout)
}

// post mounts a mutating route behind the CSRF protection (§4.5.4).
func (s *Server) post(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.Handle(pattern, s.csrf.Handler(h))
}

// newCSRF builds the §4.5.4 CSRF protection.
//
// `Sec-Fetch-Site: same-site` is rejected by the stdlib check — only
// `same-origin` and `none` pass — so a deployment where the dashboard and the
// API sit on sibling subdomains needs its origin trusted explicitly. Adding the
// configured base URL costs nothing in the single-origin case and is the
// difference between working and not in the two-subdomain one.
func newCSRF(baseURL string, log *slog.Logger) *http.CrossOriginProtection {
	c := http.NewCrossOriginProtection()
	if baseURL != "" {
		if err := c.AddTrustedOrigin(baseURL); err != nil {
			log.Warn("base_url is not usable as a trusted CSRF origin", "base_url", baseURL, "err", err)
		}
	}
	c.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, authz.CodeBadRequest, "cross-origin request rejected")
	}))
	return c
}

// --- the OAuth flow ------------------------------------------------------------

// handleStart implements `GET /auth/{idp}/start` (§4.8): mint a state, remember
// it in a short-lived cookie, and redirect to the provider.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	idp := r.PathValue("idp")
	p, ok := s.providers[idp]
	if !ok {
		s.authError(w, r, http.StatusNotFound, authz.CodeNotFound, "no such identity provider")
		return
	}
	if !p.Configured() {
		s.authError(w, r, http.StatusNotFound, authz.CodeNotFound,
			"the "+idp+" login is not configured on this server")
		return
	}

	state, err := s.sessions.NewState(w, idp)
	if err != nil {
		s.deps.Log.Error("minting an oauth state failed", "idp", idp, "err", err)
		s.authError(w, r, http.StatusInternalServerError, authz.CodeInternal, "could not start the login")
		return
	}
	dest, err := p.AuthorizeURL(s.redirectURI(idp), state)
	if err != nil {
		s.deps.Log.Error("building the authorize url failed", "idp", idp, "err", err)
		s.authError(w, r, http.StatusInternalServerError, authz.CodeInternal, "could not start the login")
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// redirectURI is the callback the provider sends the browser back to. It is
// derived from base_url rather than from the incoming request, so a spoofed
// Host header cannot redirect a code anywhere else.
func (s *Server) redirectURI(idp string) string {
	return s.deps.Config.Server.BaseURL + "/auth/" + idp + "/callback"
}

// handleCallback implements `GET /auth/{idp}/callback` (§4.8, §5.8): check the
// state, redeem the code, gate on account age, derive user_key, upsert the
// player, set the session, redirect to the dashboard.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	idp := r.PathValue("idp")
	p, ok := s.providers[idp]
	if !ok || !p.Configured() {
		s.authError(w, r, http.StatusNotFound, authz.CodeNotFound, "no such identity provider")
		return
	}

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// The player declined, or the provider refused. Not our failure.
		s.authError(w, r, http.StatusBadRequest, authz.CodeBadRequest, "the "+idp+" login was not completed: "+e)
		return
	}
	if err := s.sessions.CheckState(w, r, idp, q.Get("state")); err != nil {
		s.deps.Log.Warn("oauth state check failed", "idp", idp, "err", err)
		s.authError(w, r, http.StatusBadRequest, authz.CodeBadRequest, "this login could not be verified; please start again")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.authError(w, r, http.StatusBadRequest, authz.CodeBadRequest, "the provider returned no authorization code")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*IdPTimeout)
	defer cancel()

	// The access token lives and dies inside Exchange: it is used for the one
	// call that reads the subject and is never returned here (§4.7, §5.11).
	subject, err := p.Exchange(ctx, s.deps.HTTPClient, s.redirectURI(idp), code)
	if err != nil {
		s.deps.Log.Warn("idp exchange failed", "idp", idp, "err", err)
		s.authError(w, r, http.StatusBadGateway, authz.CodeInternal, "the identity provider could not be reached")
		return
	}

	if code, detail := s.checkAccountAge(subject); code != "" {
		s.deps.Log.Info("login refused by the account-age gate", "idp", idp, "created_at", subject.CreatedAt)
		s.authError(w, r, authz.Status(code), code, detail)
		return
	}

	// From here on the IdP subject is gone: only its HMAC survives (D17).
	userKey := s.deps.Keys.UserKey(idp, subject.ID)

	// A banned or purged account is refused before a row is touched — the same
	// deny-list §4.5.3 step 4 consults, so login and ingest cannot disagree.
	if s.deps.Deny.HasSub(userKey.B64U()) {
		s.bannedPage(w, r)
		return
	}

	var player store.Player
	err = s.deps.WriteLock(func() error {
		id, err := s.deps.Events.EnsurePlayer(ctx, nil, userKey, idp, s.deps.Now().UnixMilli())
		if err != nil {
			return err
		}
		player, err = s.deps.Events.PlayerByID(ctx, id)
		return err
	})
	if err != nil {
		s.deps.Log.Error("upserting the player failed", "idp", idp, "user_key", userKey, "err", err)
		s.authError(w, r, http.StatusInternalServerError, authz.CodeInternal, "could not complete the login")
		return
	}
	if player.Banned() {
		s.bannedPage(w, r)
		return
	}

	exp := s.sessions.Issue(w, userKey)
	s.deps.Log.Info("login", "idp", idp, "player", player.ID, "user_key", userKey, "session_expires", exp.Unix())
	http.Redirect(w, r, DashboardPath, http.StatusFound)
}

// checkAccountAge is the §4.7 gate. A provider that publishes no creation time
// (Google) has a zero CreatedAt and is waved through — quotas are its only
// limit.
func (s *Server) checkAccountAge(sub Subject) (code, detail string) {
	if s.rules.MinAccountAgeDays <= 0 || sub.CreatedAt.IsZero() {
		return "", ""
	}
	want := time.Duration(s.rules.MinAccountAgeDays) * 24 * time.Hour
	if age := s.deps.Now().Sub(sub.CreatedAt); age < want {
		return authz.CodeAccountTooNew,
			fmt.Sprintf("this account is %d days old; catlog requires %d", int(age.Hours()/24), s.rules.MinAccountAgeDays)
	}
	return "", ""
}

// --- responses -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("identity response write failed", "err", err)
	}
}

// errorBody is the §4.9 shape.
type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, errorBody{Error: code, Detail: detail})
}

// fail emits a §4.9 error at the status its code maps to.
func fail(w http.ResponseWriter, code, detail string) {
	writeError(w, authz.Status(code), code, detail)
}

// authError renders a browser-facing failure of the login flow.
//
// The flow is a top-level navigation, so the answer has to be readable in a
// browser window. WP5 installs a real template via [Server.SetErrorPage]; what
// remains here is the fallback for a catlogd wired without a web UI, and it
// carries the same contract the template must: `#auth-error` with the §4.9 code
// in `data-error`, and the code repeated in `#auth-error-code`. A client that
// asked for JSON gets the §4.9 body instead, whichever renderer is installed.
func (s *Server) authError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if wantsJSON(r) {
		writeError(w, status, code, detail)
		return
	}
	if s.errorPage != nil && s.errorPage(w, r, status, code, detail) {
		return
	}
	page := `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>catlog &mdash; sign-in failed</title></head>
<body>
<main id="auth-error" data-error="` + html.EscapeString(code) + `">
<h1>Sign-in failed</h1>
<p id="auth-error-code">` + html.EscapeString(code) + `</p>
<p id="auth-error-detail">` + html.EscapeString(detail) + `</p>
<p><a id="auth-error-retry" href="/login">Back to sign in</a></p>
</main>
</body></html>
`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(page))
}

// bannedPage is the §5.8 "account banned" response: no session is set, and the
// browser is told so in the same shape as every other login failure.
func (s *Server) bannedPage(w http.ResponseWriter, r *http.Request) {
	// Belt and braces: a banned player must not keep a session they already
	// had, and the cookie is cheap to clear here.
	s.sessions.Clear(w)
	s.authError(w, r, authz.Status(authz.CodeBanned), authz.CodeBanned,
		"this account has been banned or deleted; its handles are retired permanently")
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}
