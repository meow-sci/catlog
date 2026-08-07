package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/config"
)

// The three IdP names, which are also the `{idp}` path segment of
// `/auth/{idp}/start` and the prefix of the §4.7 subject string.
const (
	IdPDiscord = "discord"
	IdPGoogle  = "google"
	IdPGitHub  = "github"
)

// DiscordEpoch is the Discord snowflake epoch in unix ms: an account's creation
// time is `(id >> 22) + DiscordEpoch` (§4.7).
const DiscordEpoch int64 = 1420070400000

// snowflakeShift is the timestamp shift of a Discord snowflake.
const snowflakeShift = 22

// MaxIdPResponseBytes caps every IdP response body. Generous for a token or a
// user object, small enough that a hostile or broken IdP cannot exhaust memory.
const MaxIdPResponseBytes = 1 << 20

// IdPTimeout bounds a single call to an IdP. A login that hangs must fail fast:
// the player is watching a browser tab.
const IdPTimeout = 15 * time.Second

// Subject is what an IdP tells catlog about an account, and the only thing it
// is ever asked: a stable identifier and, where the provider publishes one, the
// account's creation time (§4.7). No email, ever (D17).
type Subject struct {
	// ID is the provider-stable subject: a Discord snowflake, a Google
	// id_token `sub`, or a GitHub numeric user id.
	ID string
	// CreatedAt is the account's creation instant, or the zero time when the
	// provider publishes none (Google — quotas only, §4.7).
	CreatedAt time.Time
}

// Provider is one configured IdP: the OAuth2 endpoints, the client credentials
// and the code that turns a token response into a [Subject].
type Provider struct {
	// Name is "discord", "google" or "github".
	Name string
	// AuthURL, TokenURL and APIBase come from `[idp.<name>]` (§5.3).
	AuthURL  string
	TokenURL string
	APIBase  string
	ClientID string
	// Scope is the §4.7 scope, exactly: `identify` for Discord, `openid` for
	// Google, empty for GitHub. **Never an email scope** (D17).
	Scope string

	clientSecret string
	// resolve turns a token response into the subject. It is the only
	// per-provider behaviour.
	resolve func(ctx context.Context, c *http.Client, tok tokenResponse) (Subject, error)
}

// Configured reports whether the provider has enough configuration to run a
// flow. An unconfigured IdP is disabled, never silently pointed somewhere
// (§5.3).
func (p *Provider) Configured() bool {
	return p != nil && p.AuthURL != "" && p.TokenURL != "" && p.ClientID != ""
}

// Providers builds the three §4.7 providers from configuration. Google is
// additionally handed a JWKS cache, because its subject comes from a signed
// id_token rather than from an API call.
func Providers(cfg config.Config, jwks *JWKSCache) map[string]*Provider {
	discord := &Provider{
		Name: IdPDiscord, AuthURL: cfg.IdP.Discord.AuthURL, TokenURL: cfg.IdP.Discord.TokenURL,
		APIBase: cfg.IdP.Discord.APIBase, ClientID: cfg.IdP.Discord.ClientID,
		clientSecret: cfg.IdP.Discord.ClientSecret,
		// §4.7: `identify` only. Adding `email` here would be the single
		// change that breaks D17, which is why it is a constant and not a
		// configuration key.
		Scope: "identify",
	}
	discord.resolve = func(ctx context.Context, c *http.Client, tok tokenResponse) (Subject, error) {
		return discordSubject(ctx, c, discord.APIBase, tok.AccessToken)
	}

	google := &Provider{
		Name: IdPGoogle, AuthURL: cfg.IdP.Google.AuthURL, TokenURL: cfg.IdP.Google.TokenURL,
		ClientID: cfg.IdP.Google.ClientID, clientSecret: cfg.IdP.Google.ClientSecret,
		Scope: "openid",
	}
	google.resolve = func(ctx context.Context, c *http.Client, tok tokenResponse) (Subject, error) {
		return googleSubject(ctx, jwks, cfg.IdP.Google, tok.IDToken)
	}

	github := &Provider{
		Name: IdPGitHub, AuthURL: cfg.IdP.GitHub.AuthURL, TokenURL: cfg.IdP.GitHub.TokenURL,
		APIBase: cfg.IdP.GitHub.APIBase, ClientID: cfg.IdP.GitHub.ClientID,
		clientSecret: cfg.IdP.GitHub.ClientSecret,
		// GitHub's default scope set is "no scopes", which already grants
		// GET /user. Asking for anything would be asking for too much.
		Scope: "",
	}
	github.resolve = func(ctx context.Context, c *http.Client, tok tokenResponse) (Subject, error) {
		return githubSubject(ctx, c, github.APIBase, tok.AccessToken)
	}

	return map[string]*Provider{IdPDiscord: discord, IdPGoogle: google, IdPGitHub: github}
}

// AuthorizeURL builds the redirect that starts the flow.
func (p *Provider) AuthorizeURL(redirectURI, state string) (string, error) {
	u, err := url.Parse(p.AuthURL)
	if err != nil {
		return "", fmt.Errorf("identity: %s auth_url: %w", p.Name, err)
	}
	q := u.Query()
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// tokenResponse is the union of the three providers' token bodies, reduced to
// the two fields catlog reads. Both are secrets in flight and neither is ever
// stored or logged (§4.7: discard IdP tokens immediately after reading the
// subject; §5.11).
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
}

// Exchange redeems an authorization code and resolves the subject behind it.
//
// The token never leaves this function: it is used for the one call that reads
// the subject and then goes out of scope. Nothing returns it, nothing stores
// it, nothing logs it (§4.7).
func (p *Provider) Exchange(ctx context.Context, c *http.Client, redirectURI, code string) (Subject, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {p.ClientID},
		"client_secret": {p.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Subject{}, fmt.Errorf("identity: %s token request: %w", p.Name, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub answers form-encoded unless JSON is requested explicitly; the
	// other two answer JSON regardless.
	req.Header.Set("Accept", "application/json")

	body, err := doJSON(c, req, p.Name+" token endpoint")
	if err != nil {
		return Subject{}, err
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		// Fall back to the form encoding GitHub uses when Accept is ignored,
		// so a proxy that strips the header is a recoverable condition.
		v, perr := url.ParseQuery(string(body))
		if perr != nil || v.Get("access_token") == "" {
			return Subject{}, fmt.Errorf("identity: %s token response is unreadable", p.Name)
		}
		tok = tokenResponse{AccessToken: v.Get("access_token"), IDToken: v.Get("id_token")}
	}
	if tok.AccessToken == "" && tok.IDToken == "" {
		return Subject{}, fmt.Errorf("identity: %s returned no token", p.Name)
	}
	return p.resolve(ctx, c, tok)
}

// --- per-provider subject resolution ------------------------------------------

// discordSubject reads `GET <api_base>/users/@me` and derives the account age
// from the snowflake (§4.7).
func discordSubject(ctx context.Context, c *http.Client, apiBase, accessToken string) (Subject, error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := getJSON(ctx, c, apiBase+"/users/@me", accessToken, "discord user", &body); err != nil {
		return Subject{}, err
	}
	if body.ID == "" {
		return Subject{}, errors.New("identity: discord returned no user id")
	}
	created, err := SnowflakeTime(body.ID)
	if err != nil {
		return Subject{}, err
	}
	return Subject{ID: body.ID, CreatedAt: created}, nil
}

// SnowflakeTime is the §4.7 Discord account-age computation:
// `(id >> 22) + 1420070400000`, in unix milliseconds.
func SnowflakeTime(snowflake string) (time.Time, error) {
	// Snowflakes are unsigned 64-bit; parsing unsigned and converting is what
	// keeps a legitimate high-bit id (year 2154 and beyond) from going
	// negative — and what makes a garbage value an error rather than a
	// suspiciously old account.
	id, err := strconv.ParseUint(snowflake, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("identity: discord id %q is not a snowflake", snowflake)
	}
	return time.UnixMilli(int64(id>>snowflakeShift) + DiscordEpoch).UTC(), nil
}

// githubSubject reads `GET <api_base>/user` and takes `created_at` at face
// value (§4.7).
func githubSubject(ctx context.Context, c *http.Client, apiBase, accessToken string) (Subject, error) {
	var body struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
	}
	if err := getJSON(ctx, c, apiBase+"/user", accessToken, "github user", &body); err != nil {
		return Subject{}, err
	}
	if body.ID == 0 {
		return Subject{}, errors.New("identity: github returned no user id")
	}
	created, err := time.Parse(time.RFC3339, body.CreatedAt)
	if err != nil {
		return Subject{}, fmt.Errorf("identity: github created_at %q is not RFC 3339", body.CreatedAt)
	}
	return Subject{ID: strconv.FormatInt(body.ID, 10), CreatedAt: created.UTC()}, nil
}

// --- HTTP helpers -------------------------------------------------------------

// NewIdPClient is the http.Client every IdP call uses: a bounded timeout and no
// cookie jar (an IdP session must never be carried between requests).
func NewIdPClient() *http.Client {
	return &http.Client{Timeout: IdPTimeout}
}

func getJSON(ctx context.Context, c *http.Client, url, accessToken, what string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("identity: %s request: %w", what, err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	body, err := doJSON(c, req, what)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("identity: %s response is not JSON", what)
	}
	return nil
}

// doJSON performs a request and returns the capped body, turning a non-2xx into
// an error whose text names the endpoint but never quotes the response — an
// IdP's error body can echo the token that was presented (§5.11).
func doJSON(c *http.Client, req *http.Request, what string) ([]byte, error) {
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("identity: %s is unreachable: %w", what, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, MaxIdPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("identity: reading from %s: %w", what, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("identity: %s answered HTTP %d", what, res.StatusCode)
	}
	return body, nil
}
