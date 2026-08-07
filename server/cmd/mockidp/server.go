package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CodeTTL is how long an authorization code stays redeemable. Short, like the
// real thing, and long enough that a human clicking a button in a browser
// never notices it.
const CodeTTL = 5 * time.Minute

// GoogleKID is the kid mockidp's Google signing key is published under. Stable
// across restarts even though the key itself is not, so a stale JWKS cache
// fails with "signature does not verify" rather than "unknown kid" — which is
// the more interesting failure to have exercised.
const GoogleKID = "mockidp-google-1"

// Server is the whole mock IdP: one cast list, one authorization-code store,
// one bearer-token store and one P-256 key for Google's id_token.
type Server struct {
	cfg      Config
	accounts []Account
	log      *slog.Logger
	// baseURL is how catlogd reaches this process; it is the `iss` of the
	// Google id_token, so it must match `[idp.google] issuer` in the config
	// catlogd loads (§5.8.1).
	baseURL string
	now     func() time.Time

	googleKey *ecdsa.PrivateKey

	mu     sync.Mutex
	codes  map[string]grant
	tokens map[string]grant
	// generated is the second population: subjects minted by POST /generate
	// for the load harness (see generate.go). Kept apart from [Server.accounts]
	// on purpose — nothing renders it, so the §5.8.1 `#login-as-<slug>` DOM ids
	// stay exactly the five the playwright suite clicks.
	generated map[string]Account
}

// grant is an issued authorization code or access token: which account it
// speaks for, and (for a code) what it was bound to at the authorize step.
type grant struct {
	idp         string
	sub         string
	redirectURI string
	expires     time.Time
}

// NewServer builds the mock IdP. It mints a fresh P-256 key for Google's
// id_token on every start — mockidp holds nothing worth persisting, and a
// per-process key proves catlogd really fetches the JWKS rather than trusting
// something baked in.
func NewServer(cfg Config, baseURL string, log *slog.Logger) (*Server, error) {
	accounts, err := Resolve(cfg, time.Now())
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mockidp: generate google signing key: %w", err)
	}
	return &Server{
		cfg:       cfg,
		accounts:  accounts,
		log:       log,
		baseURL:   strings.TrimRight(baseURL, "/"),
		now:       time.Now,
		googleKey: key,
		codes:     map[string]grant{},
		tokens:    map[string]grant{},
		generated: map[string]Account{},
	}, nil
}

// Handler mounts every route (§5.8.1).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// Discord: OAuth2 code flow, no OIDC (§4.7).
	mux.HandleFunc("GET /discord/oauth/authorize", s.authorize(IdPDiscord))
	mux.HandleFunc("POST /discord/oauth/token", s.handleDiscordToken)
	mux.HandleFunc("GET /discord/api/users/@me", s.handleDiscordMe)

	// Google: OIDC code flow; the id_token is really signed (§5.8.1).
	mux.HandleFunc("GET /google/authorize", s.authorize(IdPGoogle))
	mux.HandleFunc("POST /google/token", s.handleGoogleToken)
	mux.HandleFunc("GET /google/jwks", s.handleGoogleJWKS)

	// GitHub: OAuth2 code flow, default scopes (§4.7).
	mux.HandleFunc("GET /github/login/oauth/authorize", s.authorize(IdPGitHub))
	mux.HandleFunc("POST /github/login/oauth/access_token", s.handleGitHubToken)
	mux.HandleFunc("GET /github/user", s.handleGitHubUser)

	// Programmatic subjects for the load harness (generate.go). Dev-only, like
	// the rest of this binary; it adds a second population of accounts and
	// changes nothing else, least of all catlogd.
	mux.HandleFunc("POST /generate", s.handleGenerate)

	return mux
}

// Accounts exposes the resolved cast, for tests and the index page.
func (s *Server) Accounts() []Account { return s.accounts }

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- the authorize page ------------------------------------------------------

// authorize is the shared authorization endpoint for all three IdPs. Without a
// `user` parameter it renders the consent page; with one it mints a code and
// redirects, which is exactly the shape a real provider's "Authorize" button
// has.
func (s *Server) authorize(idp string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if got := q.Get("client_id"); got != s.cfg.ClientID {
			s.oauthError(w, http.StatusBadRequest, "invalid_client",
				fmt.Sprintf("client_id %q is not registered", got))
			return
		}
		if got := q.Get("response_type"); got != "code" {
			s.oauthError(w, http.StatusBadRequest, "unsupported_response_type",
				fmt.Sprintf("response_type %q, want code", got))
			return
		}
		redirectURI := q.Get("redirect_uri")
		if redirectURI == "" {
			s.oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is required")
			return
		}
		if _, err := url.Parse(redirectURI); err != nil {
			s.oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not a URL")
			return
		}

		// D17 is enforced here rather than merely documented: catlog must never
		// ask any IdP for an email, so a mock IdP that happily granted one
		// would let the rule rot. Asking for it is a hard error.
		if scope := q.Get("scope"); containsEmailScope(scope) {
			s.oauthError(w, http.StatusBadRequest, "invalid_scope",
				"catlog never requests an email scope (D17); mockidp refuses to grant one")
			return
		}

		sub := q.Get("user")
		if sub == "" {
			s.renderConsent(w, idp, r.URL)
			return
		}

		acct, ok := s.account(idp, sub)
		if !ok {
			s.oauthError(w, http.StatusNotFound, "invalid_request",
				fmt.Sprintf("no %s user %q in this cast", idp, sub))
			return
		}

		code, err := s.mintCode(idp, acct.Sub, redirectURI)
		if err != nil {
			s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}

		dest, _ := url.Parse(redirectURI)
		dq := dest.Query()
		dq.Set("code", code)
		if state := q.Get("state"); state != "" {
			dq.Set("state", state)
		}
		dest.RawQuery = dq.Encode()

		s.log.Info("authorization granted", "idp", idp, "label", acct.Label, "redirect_uri", redirectURI)
		http.Redirect(w, r, dest.String(), http.StatusFound)
	}
}

// containsEmailScope reports whether a space- or comma-separated scope string
// asks for an email.
func containsEmailScope(scope string) bool {
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ' ' || r == ',' || r == '+' }) {
		if strings.Contains(strings.ToLower(s), "email") {
			return true
		}
	}
	return false
}

// renderConsent writes the "Login as …" page. Every button is a plain link
// carrying the original query plus `user=<sub>`, so it works identically for a
// browser click (WP5's playwright suite) and for a Go http.Client that follows
// redirects.
//
// The DOM ids are the contract: `#login-as-<slug(label)>` (§5.8.1).
func (s *Server) renderConsent(w http.ResponseWriter, idp string, self *url.URL) {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>mockidp — sign in with ` + html.EscapeString(idp) + `</title>
<style>
 body{font-family:system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;line-height:1.5}
 h1{font-size:1.3rem} .who{color:#666;font-size:.9rem}
 a.login-as{display:block;margin:.5rem 0;padding:.7rem 1rem;border:1px solid #888;border-radius:.4rem;
            text-decoration:none;color:inherit;background:#f6f6f6}
 a.login-as:hover{background:#eee}
 footer{margin-top:2rem;color:#888;font-size:.8rem}
</style></head><body>
<h1 id="mockidp-title">mockidp &mdash; sign in with ` + html.EscapeString(idp) + `</h1>
<p class="who">This is a local stand-in. No request ever leaves 127.0.0.1 (D2).</p>
<div id="login-choices">
`)

	for _, a := range s.accounts {
		if a.IdP != idp {
			continue
		}
		next := *self
		q := next.Query()
		q.Set("user", a.Sub)
		next.RawQuery = q.Encode()
		fmt.Fprintf(&b,
			"<a class=\"login-as\" id=\"%s\" data-sub=\"%s\" data-created-at=\"%s\" href=\"%s\">Login as %s</a>\n",
			html.EscapeString(a.ElementID), html.EscapeString(a.Sub),
			a.CreatedAt.Format(time.RFC3339), html.EscapeString(next.RequestURI()),
			html.EscapeString(a.Label))
	}

	b.WriteString(`</div>
<footer>catlog mockidp &middot; <a href="/">all providers</a></footer>
</body></html>
`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>mockidp</title></head><body>
<h1>catlog mockidp</h1>
<p>Local stand-in for Discord, Google and GitHub (INITIAL_IMPL_PLAN &sect;5.8.1).</p>
<ul>`)
	for _, a := range s.accounts {
		fmt.Fprintf(&b, "<li>%s &mdash; <code>%s:%s</code>, created %s</li>",
			html.EscapeString(a.Label), a.IdP, html.EscapeString(a.Sub), a.CreatedAt.Format(time.RFC3339))
	}
	b.WriteString(`</ul>
<p>Start a flow from catlogd: <code>/auth/{discord,google,github}/start</code>.</p>
</body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// --- code / token plumbing ---------------------------------------------------

// account resolves a subject to the account behind it: the committed cast
// first, then the accounts POST /generate minted. Both populations redeem codes
// and answer userinfo through exactly the same code below — the only difference
// between them is that one is rendered as a button and the other is not.
func (s *Server) account(idp, sub string) (Account, bool) {
	for _, a := range s.accounts {
		if a.IdP == idp && a.Sub == sub {
			return a, true
		}
	}
	return s.generatedAccount(idp, sub)
}

func (s *Server) mintCode(idp, sub, redirectURI string) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = grant{idp: idp, sub: sub, redirectURI: redirectURI, expires: s.now().Add(CodeTTL)}
	return code, nil
}

// redeem exchanges a code for the account it stands for. Codes are single-use —
// a replayed code is an error, exactly as at a real provider — and the
// redirect_uri must match the one the code was issued against.
func (s *Server) redeem(idp, code, redirectURI string) (Account, error) {
	s.mu.Lock()
	g, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	s.mu.Unlock()

	switch {
	case !ok:
		return Account{}, fmt.Errorf("unknown or already-redeemed code")
	case g.idp != idp:
		return Account{}, fmt.Errorf("code was issued for %s", g.idp)
	case s.now().After(g.expires):
		return Account{}, fmt.Errorf("code has expired")
	case redirectURI != "" && redirectURI != g.redirectURI:
		return Account{}, fmt.Errorf("redirect_uri does not match the one the code was issued for")
	}
	acct, ok := s.account(idp, g.sub)
	if !ok {
		return Account{}, fmt.Errorf("the account behind this code is gone")
	}
	return acct, nil
}

func (s *Server) mintToken(idp, sub string) (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok] = grant{idp: idp, sub: sub, expires: s.now().Add(time.Hour)}
	return tok, nil
}

// bearer resolves the Authorization header to an account. Both the OAuth
// "Bearer" and GitHub's legacy "token" prefixes are accepted, because both are
// what the respective real APIs take.
func (s *Server) bearer(r *http.Request, idp string) (Account, bool) {
	auth := r.Header.Get("Authorization")
	var tok string
	switch {
	case strings.HasPrefix(auth, "Bearer "):
		tok = strings.TrimSpace(auth[len("Bearer "):])
	case strings.HasPrefix(auth, "token "):
		tok = strings.TrimSpace(auth[len("token "):])
	default:
		return Account{}, false
	}

	s.mu.Lock()
	g, ok := s.tokens[tok]
	s.mu.Unlock()
	if !ok || g.idp != idp || s.now().After(g.expires) {
		return Account{}, false
	}
	return s.account(idp, g.sub)
}

// checkClient validates the form-posted client credentials, which is what makes
// the dev/dev pair in `catlogd.dev.toml` load-bearing rather than decorative.
// HTTP Basic is accepted too — Google and GitHub both allow it.
func (s *Server) checkClient(r *http.Request) error {
	id, secret := r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	if id == "" && secret == "" {
		if u, p, ok := r.BasicAuth(); ok {
			id, secret = u, p
		}
	}
	if id != s.cfg.ClientID || secret != s.cfg.ClientSecret {
		return fmt.Errorf("client_id/client_secret do not match a registered application")
	}
	return nil
}

// tokenRequest runs the checks every token endpoint shares and returns the
// account the code stands for.
func (s *Server) tokenRequest(w http.ResponseWriter, r *http.Request, idp string) (Account, bool) {
	if err := r.ParseForm(); err != nil {
		s.oauthError(w, http.StatusBadRequest, "invalid_request", "body is not a form")
		return Account{}, false
	}
	if err := s.checkClient(r); err != nil {
		s.oauthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return Account{}, false
	}
	// GitHub does not require grant_type; Discord and Google do. Accept an
	// empty one, reject a wrong one.
	if gt := r.PostFormValue("grant_type"); gt != "" && gt != "authorization_code" {
		s.oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			fmt.Sprintf("grant_type %q, want authorization_code", gt))
		return Account{}, false
	}

	acct, err := s.redeem(idp, r.PostFormValue("code"), r.PostFormValue("redirect_uri"))
	if err != nil {
		s.oauthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return Account{}, false
	}
	s.log.Info("code redeemed", "idp", idp, "label", acct.Label)
	return acct, true
}

// --- responses ---------------------------------------------------------------

// oauthError is the RFC 6749 §5.2 error body all three providers use.
func (s *Server) oauthError(w http.ResponseWriter, status int, code, description string) {
	s.log.Warn("mockidp rejected a request", "error", code, "detail", description)
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("mockidp response write failed", "err", err)
	}
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mockidp: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
