package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// generate posts a GenerateRequest and returns the accounts, failing the test
// on anything but a 200.
func generate(t *testing.T, baseURL string, req GenerateRequest) []GeneratedAccount {
	t.Helper()
	body, status := generateStatus(t, baseURL, req)
	if status != http.StatusOK {
		t.Fatalf("POST /generate = %d: %s", status, body)
	}
	var res GenerateResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("decode /generate: %v (%s)", err, body)
	}
	if res.Count != len(res.Accounts) {
		t.Fatalf("count = %d but %d accounts", res.Count, len(res.Accounts))
	}
	return res.Accounts
}

func generateStatus(t *testing.T, baseURL string, req GenerateRequest) (string, int) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := http.Post(baseURL+"/generate", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /generate: %v", err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return string(out), res.StatusCode
}

// authorizeAs drives the authorize endpoint for a subject that has no button —
// exactly what the load harness does, and exactly what a browser click does
// once the `user` parameter is resolved.
func authorizeAs(t *testing.T, baseURL string, a GeneratedAccount, redirect, state string) string {
	t.Helper()
	u := baseURL + a.AuthorizePath + "?client_id=dev&response_type=code&user=" +
		url.QueryEscape(a.Sub) + "&redirect_uri=" + url.QueryEscape(redirect) + "&state=" + state

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(u)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		out, _ := io.ReadAll(res.Body)
		t.Fatalf("authorize %s = %d: %s", a.Sub, res.StatusCode, out)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}
	if got := loc.Query().Get("state"); got != state {
		t.Errorf("state round-trip = %q, want %q", got, state)
	}
	return code
}

// TestGeneratedAccountsAreInvisibleToTheConsentPages is the load-harness
// endpoint's most important property: it must not disturb §5.8.1's DOM-id
// contract, which `TestDOMIdsAreStable` and the playwright suite both pin.
func TestGeneratedAccountsAreInvisibleToTheConsentPages(t *testing.T) {
	s, ts := testServer(t)

	before := map[string]string{}
	for _, path := range authorizePaths {
		before[path] = get(t, ts.URL+path+"?client_id=dev&response_type=code&redirect_uri="+
			url.QueryEscape("http://127.0.0.1:8080/auth/x/callback")+"&state=s")
	}
	index := get(t, ts.URL+"/")

	accounts := generate(t, ts.URL, GenerateRequest{Count: 30, Seed: "dom"})
	if s.GeneratedCount() != 30 {
		t.Fatalf("GeneratedCount = %d, want 30", s.GeneratedCount())
	}

	for path, was := range before {
		now := get(t, ts.URL+path+"?client_id=dev&response_type=code&redirect_uri="+
			url.QueryEscape("http://127.0.0.1:8080/auth/x/callback")+"&state=s")
		if now != was {
			t.Errorf("the %s consent page changed after /generate", path)
		}
		for _, a := range accounts {
			if strings.Contains(now, a.Sub) {
				t.Errorf("generated subject %s leaked onto the %s consent page", a.Sub, path)
			}
		}
	}
	if now := get(t, ts.URL+"/"); now != index {
		t.Error("the index page changed after /generate")
	}
}

// TestGeneratedSubjectsAreDeterministicAndUnique is what makes a load run
// reproducible: the same seed must name the same players.
func TestGeneratedSubjectsAreDeterministicAndUnique(t *testing.T) {
	_, a := testServer(t)
	_, b := testServer(t)

	first := generate(t, a.URL, GenerateRequest{Count: 60, Seed: "abc", AgeDays: 90})
	second := generate(t, b.URL, GenerateRequest{Count: 60, Seed: "abc", AgeDays: 90})

	seen := map[string]bool{}
	for i := range first {
		if first[i].Sub != second[i].Sub || first[i].IdP != second[i].IdP {
			t.Fatalf("account %d differs across servers: %+v vs %+v", i, first[i], second[i])
		}
		key := first[i].IdP + ":" + first[i].Sub
		if seen[key] {
			t.Fatalf("duplicate subject %s", key)
		}
		seen[key] = true
	}

	// A different seed must name different players, or two load runs would
	// fight over the same handles.
	other := generate(t, a.URL, GenerateRequest{Count: 60, Seed: "xyz", AgeDays: 90})
	for i := range other {
		if seen[other[i].IdP+":"+other[i].Sub] {
			t.Errorf("seed 'xyz' account %d collided with seed 'abc'", i)
		}
	}
}

// TestGeneratedAccountsRoundTripThroughEveryProvider drives all three flows for
// a generated subject: catlogd's real code exchange, its real id_token
// verification and its real user endpoints all have to work with these.
func TestGeneratedAccountsRoundTripThroughEveryProvider(t *testing.T) {
	s, ts := testServer(t)
	accounts := generate(t, ts.URL, GenerateRequest{Count: 3, Seed: "rt", AgeDays: 365})

	byIdP := map[string]GeneratedAccount{}
	for _, a := range accounts {
		byIdP[a.IdP] = a
	}
	for _, idp := range generateOrder {
		if _, ok := byIdP[idp]; !ok {
			t.Fatalf("an empty idp did not round-robin: %+v", accounts)
		}
	}

	// --- Discord: the snowflake comes back as a string, and it is aged -------
	d := byIdP[IdPDiscord]
	redirect := "http://127.0.0.1:8080/auth/discord/callback"
	code := authorizeAs(t, ts.URL, d, redirect, "s1")
	tok := postForm(t, ts.URL+"/discord/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)
	var dt struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(tok), &dt); err != nil {
		t.Fatalf("discord token: %v", err)
	}
	var me map[string]any
	if err := json.Unmarshal([]byte(getAuth(t, ts.URL+"/discord/api/users/@me", dt.AccessToken)), &me); err != nil {
		t.Fatalf("@me: %v", err)
	}
	if me["id"] != d.Sub {
		t.Errorf("@me id = %v, want %q", me["id"], d.Sub)
	}
	if _, leaked := me["email"]; leaked {
		t.Error("a generated account leaked an email (D17)")
	}
	// The age the snowflake encodes is what catlogd's gate reads.
	id, err := strconv.ParseInt(d.Sub, 10, 64)
	if err != nil {
		t.Fatalf("generated discord sub %q is not a snowflake: %v", d.Sub, err)
	}
	created := time.UnixMilli((id >> SnowflakeShift) + DiscordEpoch)
	if age := time.Since(created); age < 300*24*time.Hour {
		t.Errorf("a 365-day account reads back as %v old", age)
	}

	// --- Google: the id_token really verifies against the published JWKS -----
	g := byIdP[IdPGoogle]
	redirect = "http://127.0.0.1:8080/auth/google/callback"
	code = authorizeAs(t, ts.URL, g, redirect, "s2")
	tok = postForm(t, ts.URL+"/google/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)
	var gt struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal([]byte(tok), &gt); err != nil {
		t.Fatalf("google token: %v", err)
	}
	parsed, err := jose.ParseSigned(gt.IDToken, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("id_token: %v", err)
	}
	payload, err := parsed.Verify(&s.googleKey.PublicKey)
	if err != nil {
		t.Fatalf("id_token does not verify: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("id_token claims: %v", err)
	}
	if claims["sub"] != g.Sub {
		t.Errorf("id_token sub = %v, want %q", claims["sub"], g.Sub)
	}

	// --- GitHub: a numeric id and an RFC 3339 created_at ---------------------
	h := byIdP[IdPGitHub]
	redirect = "http://127.0.0.1:8080/auth/github/callback"
	code = authorizeAs(t, ts.URL, h, redirect, "s3")
	tok = postForm(t, ts.URL+"/github/login/oauth/access_token", url.Values{
		"code": {code}, "redirect_uri": {redirect}, "client_id": {"dev"}, "client_secret": {"dev"},
	}, true)
	var ht struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(tok), &ht); err != nil {
		t.Fatalf("github token: %v", err)
	}
	var user struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(getAuth(t, ts.URL+"/github/user", ht.AccessToken)), &user); err != nil {
		t.Fatalf("github user: %v", err)
	}
	if strconv.FormatInt(user.ID, 10) != h.Sub {
		t.Errorf("github id = %d, want %q", user.ID, h.Sub)
	}
	created, err = time.Parse(time.RFC3339, user.CreatedAt)
	if err != nil {
		t.Fatalf("github created_at %q: %v", user.CreatedAt, err)
	}
	if age := time.Since(created); age < 300*24*time.Hour {
		t.Errorf("a 365-day github account reads back as %v old", age)
	}
}

// TestGeneratedAccountsCanBeDeliberatelyTooNew keeps the account-age gate
// exercised in both directions for the generated population too, which is the
// whole reason `new_every` exists.
func TestGeneratedAccountsCanBeDeliberatelyTooNew(t *testing.T) {
	_, ts := testServer(t)
	accounts := generate(t, ts.URL, GenerateRequest{Count: 12, Seed: "young", NewEvery: 3})

	var tooNew, aged int
	for _, a := range accounts {
		created, err := time.Parse(time.RFC3339, a.CreatedAt)
		if err != nil {
			t.Fatalf("created_at %q: %v", a.CreatedAt, err)
		}
		age := time.Since(created)
		switch {
		case a.TooNew:
			tooNew++
			if age > 30*24*time.Hour {
				t.Errorf("%s is flagged too_new but reads %v old", a.Sub, age)
			}
			if a.IdP == IdPGoogle {
				t.Error("google was flagged too_new; it publishes no account age (§4.7)")
			}
		default:
			aged++
			if age < 30*24*time.Hour {
				t.Errorf("%s is not flagged too_new but reads only %v old", a.Sub, age)
			}
		}
	}
	if tooNew == 0 || aged == 0 {
		t.Fatalf("new_every=3 over 12 accounts produced %d too-new and %d aged", tooNew, aged)
	}
}

// TestGenerateRejectsNonsense keeps the endpoint from silently doing something
// other than what was asked.
func TestGenerateRejectsNonsense(t *testing.T) {
	_, ts := testServer(t)

	for _, tc := range []struct {
		name string
		req  GenerateRequest
	}{
		{"negative count", GenerateRequest{Count: -1}},
		{"over the per-request cap", GenerateRequest{Count: MaxGeneratedPerRequest + 1}},
		{"unknown idp", GenerateRequest{Count: 1, IdP: "myspace"}},
		{"negative new_every", GenerateRequest{Count: 1, NewEvery: -2}},
	} {
		if _, status := generateStatus(t, ts.URL, tc.req); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.name, status)
		}
	}

	// An unknown field is a typo, not a default.
	res, err := http.Post(ts.URL+"/generate", "application/json", strings.NewReader(`{"cont": 5}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown field = %d, want 400", res.StatusCode)
	}

	// An empty body is one account, so `curl -XPOST .../generate` works.
	res, err = http.Post(ts.URL+"/generate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an empty body = %d, want 200", res.StatusCode)
	}
	var out GenerateResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 1 {
		t.Errorf("an empty body minted %d accounts, want 1", out.Count)
	}
}

// TestGeneratedSubjectsDoNotCollideWithTheCommittedCast: a generated account
// that reused a committed subject would silently be the same catlog player.
func TestGeneratedSubjectsDoNotCollideWithTheCommittedCast(t *testing.T) {
	s, ts := testServer(t)

	committed := map[string]bool{}
	for _, a := range s.Accounts() {
		committed[a.IdP+":"+a.Sub] = true
	}
	for _, a := range generate(t, ts.URL, GenerateRequest{Count: 500, Seed: "collide"}) {
		if committed[a.IdP+":"+a.Sub] {
			t.Fatalf("generated %s:%s collides with the committed cast", a.IdP, a.Sub)
		}
	}
}
