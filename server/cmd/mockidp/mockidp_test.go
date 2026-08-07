package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg, err := LoadConfig("../../mockidp.toml")
	if err != nil {
		t.Fatalf("load the committed mockidp.toml: %v", err)
	}
	s, err := NewServer(cfg, "http://127.0.0.1:9090", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	// The id_token `iss` must be what catlogd is configured to expect, not the
	// ephemeral test address.
	return s, ts
}

// TestCommittedConfigExercisesTheAgeGateBothWays is the §5.8.1 requirement: one
// aged Discord account and one minted now, so `account_too_new` is reachable.
func TestCommittedConfigExercisesTheAgeGateBothWays(t *testing.T) {
	cfg, err := LoadConfig("../../mockidp.toml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	now := time.Now()
	accounts, err := Resolve(cfg, now)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	const gate = 30 * 24 * time.Hour
	seen := map[string]map[bool]bool{} // idp → aged? → present
	for _, a := range accounts {
		aged := now.Sub(a.CreatedAt) >= gate
		if seen[a.IdP] == nil {
			seen[a.IdP] = map[bool]bool{}
		}
		seen[a.IdP][aged] = true
	}

	for _, idp := range []string{IdPDiscord, IdPGitHub} {
		if !seen[idp][true] {
			t.Errorf("mockidp.toml has no aged %s account; a successful login is untestable", idp)
		}
		if !seen[idp][false] {
			t.Errorf("mockidp.toml has no brand-new %s account; account_too_new is untestable (§5.8.1)", idp)
		}
	}
	if len(seen[IdPGoogle]) == 0 {
		t.Error("mockidp.toml has no google account")
	}
}

// TestDOMIdsAreStable pins the §5.8.1 contract WP5's playwright suite depends
// on: every button is `#login-as-<slug(label)>`.
func TestDOMIdsAreStable(t *testing.T) {
	for _, tc := range []struct{ label, want string }{
		{"Whiskers (Discord, old account)", "whiskers-discord-old-account"},
		{"Sprocket (Discord, new account)", "sprocket-discord-new-account"},
		{"Mittens (Google)", "mittens-google"},
		{"Clawdia (GitHub)", "clawdia-github"},
		{"Pixel (GitHub, new account)", "pixel-github-new-account"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"UPPER_snake.case", "upper-snake-case"},
	} {
		if got := Slug(tc.label); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}

	_, ts := testServer(t)
	body := get(t, ts.URL+"/discord/oauth/authorize?client_id=dev&response_type=code&scope=identify&redirect_uri="+
		url.QueryEscape("http://127.0.0.1:8080/auth/discord/callback")+"&state=s")
	for _, want := range []string{`id="login-as-whiskers-discord-old-account"`, `id="login-as-sprocket-discord-new-account"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the discord consent page is missing %s", want)
		}
	}
	// Only that provider's accounts appear on its page.
	if strings.Contains(body, "login-as-mittens-google") {
		t.Error("a google account appears on the discord consent page")
	}
}

// TestNoEmailScopeIsEverGranted enforces D17 at the mock provider, so the rule
// cannot rot into a comment.
func TestNoEmailScopeIsEverGranted(t *testing.T) {
	_, ts := testServer(t)
	for _, scope := range []string{"email", "identify email", "openid+email", "identify,email", "userinfo.email"} {
		u := ts.URL + "/discord/oauth/authorize?client_id=dev&response_type=code&redirect_uri=" +
			url.QueryEscape("http://x/cb") + "&scope=" + url.QueryEscape(scope)
		res, err := http.Get(u)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("scope %q = %d, want 400 (D17)", scope, res.StatusCode)
		}
	}
}

// TestDiscordFlow drives the whole Discord dance and checks the response shapes
// are the real provider's for the fields catlog reads (§5.8.1).
func TestDiscordFlow(t *testing.T) {
	_, ts := testServer(t)
	redirect := "http://127.0.0.1:8080/auth/discord/callback"

	code := authorizeFor(t, ts, "/discord/oauth/authorize", "login-as-whiskers-discord-old-account", redirect, "st4te")

	tok := postForm(t, ts.URL+"/discord/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(tok), &token); err != nil {
		t.Fatalf("token response: %v (%s)", err, tok)
	}
	if token.AccessToken == "" || token.TokenType != "Bearer" || token.Scope != "identify" {
		t.Fatalf("token = %+v, want a Bearer token scoped identify", token)
	}

	me := getAuth(t, ts.URL+"/discord/api/users/@me", token.AccessToken)
	var user map[string]any
	if err := json.Unmarshal([]byte(me), &user); err != nil {
		t.Fatalf("@me: %v", err)
	}
	// Discord returns the snowflake as a **string**; catlog reads exactly this.
	id, ok := user["id"].(string)
	if !ok || id != "100000000000000000" {
		t.Errorf("@me id = %v (%T), want the string snowflake", user["id"], user["id"])
	}
	if _, leaked := user["email"]; leaked {
		t.Error("mockidp returned an email (D17)")
	}

	// The code is single-use.
	again := postFormStatus(t, ts.URL+"/discord/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	})
	if again == http.StatusOK {
		t.Error("an authorization code was redeemable twice")
	}

	// The client secret is checked.
	bad := postFormStatus(t, ts.URL+"/discord/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {"whatever"},
		"client_id": {"dev"}, "client_secret": {"wrong"},
	})
	if bad != http.StatusUnauthorized {
		t.Errorf("a wrong client_secret = %d, want 401", bad)
	}
}

// TestGoogleFlow checks the id_token is really signed by the key the JWKS
// publishes (§5.8.1).
func TestGoogleFlow(t *testing.T) {
	s, ts := testServer(t)
	redirect := "http://127.0.0.1:8080/auth/google/callback"

	code := authorizeFor(t, ts, "/google/authorize", "login-as-mittens-google", redirect, "st4te")
	tok := postForm(t, ts.URL+"/google/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)

	var token struct {
		IDToken string `json:"id_token"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(tok), &token); err != nil {
		t.Fatalf("token response: %v", err)
	}
	if token.Scope != "openid" {
		t.Errorf("scope = %q, want openid (§4.7)", token.Scope)
	}

	// The JWKS is what verifies it — nothing else is available.
	var set jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(get(t, ts.URL+"/google/jwks")), &set); err != nil {
		t.Fatalf("jwks: %v", err)
	}
	if len(set.Keys) != 1 || set.Keys[0].KeyID != GoogleKID {
		t.Fatalf("jwks = %+v, want one key with kid %s", set.Keys, GoogleKID)
	}
	if !set.Keys[0].IsPublic() {
		t.Fatal("mockidp published a private key in its JWKS")
	}

	parsed, err := jose.ParseSigned(token.IDToken, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("id_token: %v", err)
	}
	payload, err := parsed.Verify(set.Keys[0])
	if err != nil {
		t.Fatalf("the id_token does not verify against the published JWKS: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims["iss"] != s.googleIssuer() {
		t.Errorf("iss = %v, want %s", claims["iss"], s.googleIssuer())
	}
	if claims["aud"] != "dev" {
		t.Errorf("aud = %v, want the client id", claims["aud"])
	}
	if claims["sub"] != "g-user-1" {
		t.Errorf("sub = %v, want g-user-1", claims["sub"])
	}
	if exp, ok := claims["exp"].(float64); !ok || int64(exp) <= time.Now().Unix() {
		t.Errorf("exp = %v, want a future unix time", claims["exp"])
	}
	if _, leaked := claims["email"]; leaked {
		t.Error("the id_token carries an email (D17)")
	}
}

// TestGitHubFlow checks the real provider's two quirks: a form-encoded token
// response by default, and `id` as a JSON number with an RFC 3339 `created_at`.
func TestGitHubFlow(t *testing.T) {
	_, ts := testServer(t)
	redirect := "http://127.0.0.1:8080/auth/github/callback"

	code := authorizeFor(t, ts, "/github/login/oauth/authorize", "login-as-clawdia-github", redirect, "st4te")

	// Without Accept: application/json, GitHub answers form-encoded.
	form := postForm(t, ts.URL+"/github/login/oauth/access_token", url.Values{
		"code": {code}, "redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, false)
	v, err := url.ParseQuery(form)
	if err != nil || v.Get("access_token") == "" {
		t.Fatalf("form token response = %q (err %v)", form, err)
	}

	// And JSON when asked.
	code2 := authorizeFor(t, ts, "/github/login/oauth/authorize", "login-as-clawdia-github", redirect, "s2")
	jsonBody := postForm(t, ts.URL+"/github/login/oauth/access_token", url.Values{
		"code": {code2}, "redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &token); err != nil {
		t.Fatalf("json token response: %v (%s)", err, jsonBody)
	}
	if token.TokenType != "bearer" {
		t.Errorf("token_type = %q, want github's lowercase bearer", token.TokenType)
	}

	user := getAuth(t, ts.URL+"/github/user", token.AccessToken)
	var raw map[string]any
	if err := json.Unmarshal([]byte(user), &raw); err != nil {
		t.Fatalf("/user: %v", err)
	}
	id, ok := raw["id"].(float64)
	if !ok || int64(id) != 4242 {
		t.Errorf("/user id = %v (%T), want the number 4242", raw["id"], raw["id"])
	}
	created, ok := raw["created_at"].(string)
	if !ok {
		t.Fatalf("/user created_at = %v", raw["created_at"])
	}
	if _, err := time.Parse(time.RFC3339, created); err != nil {
		t.Errorf("created_at %q is not RFC 3339: %v", created, err)
	}
	if created != "2020-01-01T00:00:00Z" {
		t.Errorf("created_at = %q, want the configured value", created)
	}
	if _, leaked := raw["email"]; leaked {
		t.Error("mockidp returned an email (D17)")
	}
}

// TestNewAccountsAreMintedNow checks the `new = true` entries really do fail the
// age gate — the reason they exist.
func TestNewAccountsAreMintedNow(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	accounts, err := Resolve(Config{ClientID: "dev", ClientSecret: "dev", Users: []User{
		{Label: "New Discord", IdP: IdPDiscord, New: true},
		{Label: "New GitHub", IdP: IdPGitHub, Sub: "9", New: true},
	}}, now)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The Discord snowflake must decode back to `now` through the §4.7 formula.
	id, err := strconv.ParseInt(accounts[0].Sub, 10, 64)
	if err != nil {
		t.Fatalf("minted snowflake %q: %v", accounts[0].Sub, err)
	}
	if got := (id >> SnowflakeShift) + DiscordEpoch; got != now.UnixMilli() {
		t.Errorf("minted snowflake decodes to %d ms, want %d", got, now.UnixMilli())
	}
	if !accounts[1].CreatedAt.Equal(now) {
		t.Errorf("new github created_at = %s, want %s", accounts[1].CreatedAt, now)
	}
}

// TestConfigRejectsAmbiguity guards the fixtures a test suite names by id.
func TestConfigRejectsAmbiguity(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"duplicate slug", Config{Users: []User{
			{Label: "Cat (Discord)", IdP: IdPDiscord, Sub: "1"},
			{Label: "cat-discord", IdP: IdPDiscord, Sub: "2"},
		}}},
		{"duplicate subject", Config{Users: []User{
			{Label: "A", IdP: IdPDiscord, Sub: "1"},
			{Label: "B", IdP: IdPDiscord, Sub: "1"},
		}}},
		{"unknown idp", Config{Users: []User{{Label: "A", IdP: "twitter", Sub: "1"}}}},
		{"no label", Config{Users: []User{{IdP: IdPDiscord, Sub: "1"}}}},
		{"github without created_at", Config{Users: []User{{Label: "A", IdP: IdPGitHub, Sub: "1"}}}},
		{"github non-numeric id", Config{Users: []User{{Label: "A", IdP: IdPGitHub, Sub: "abc", New: true}}}},
		{"discord non-numeric snowflake", Config{Users: []User{{Label: "A", IdP: IdPDiscord, Sub: "abc"}}}},
		{"empty", Config{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.cfg, now); err == nil {
				t.Error("Resolve accepted an ambiguous or incomplete cast")
			}
		})
	}
}

// TestAuthorizeRejectsBadRequests covers the guards a real provider has.
func TestAuthorizeRejectsBadRequests(t *testing.T) {
	_, ts := testServer(t)
	cb := url.QueryEscape("http://127.0.0.1:8080/auth/discord/callback")
	for _, tc := range []struct{ name, query string }{
		{"unknown client", "client_id=nope&response_type=code&redirect_uri=" + cb},
		{"implicit flow", "client_id=dev&response_type=token&redirect_uri=" + cb},
		{"no redirect_uri", "client_id=dev&response_type=code"},
		{"unknown user", "client_id=dev&response_type=code&redirect_uri=" + cb + "&user=999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := http.Get(ts.URL + "/discord/oauth/authorize?" + tc.query)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			res.Body.Close()
			if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusFound {
				t.Errorf("status = %d, want a rejection", res.StatusCode)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	_, ts := testServer(t)
	if got := strings.TrimSpace(get(t, ts.URL+"/healthz")); got != `{"ok":true}` {
		t.Errorf("/healthz = %q", got)
	}
}

// --- helpers -------------------------------------------------------------------

var hrefPattern = regexp.MustCompile(`id="([^"]+)"[^>]*href="([^"]+)"`)

// authorizeFor renders the consent page, finds the button with the given DOM id
// and follows it, returning the authorization code from the redirect — exactly
// what a browser (or WP5's playwright suite) does.
func authorizeFor(t *testing.T, ts *httptest.Server, path, elementID, redirect, state string) string {
	t.Helper()
	page := get(t, ts.URL+path+"?client_id=dev&response_type=code&redirect_uri="+
		url.QueryEscape(redirect)+"&state="+state)

	var href string
	for _, m := range hrefPattern.FindAllStringSubmatch(page, -1) {
		if m[1] == elementID {
			href = strings.ReplaceAll(m[2], "&amp;", "&")
		}
	}
	if href == "" {
		t.Fatalf("no #%s on %s\n%s", elementID, path, page)
	}

	// No redirect following: the Location header is what we want.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(ts.URL + href)
	if err != nil {
		t.Fatalf("follow %s: %v", href, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("clicking #%s = %d, want 302", elementID, res.StatusCode)
	}

	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), redirect) {
		t.Fatalf("redirected to %s, want %s", loc, redirect)
	}
	if got := loc.Query().Get("state"); got != state {
		t.Errorf("state round-trip = %q, want %q", got, state)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("no code in the redirect")
	}
	return code
}

func get(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

func getAuth(t *testing.T, url, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	return string(body)
}

func postForm(t *testing.T, url string, form url.Values, wantJSON bool) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if wantJSON {
		req.Header.Set("Accept", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", url, res.StatusCode, body)
	}
	return string(body)
}

func postFormStatus(t *testing.T, url string, form url.Values) int {
	t.Helper()
	res, err := http.PostForm(url, form)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	res.Body.Close()
	return res.StatusCode
}
