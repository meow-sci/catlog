package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/meow-sci/catlog/server/internal/cjws"
)

// The shapes below are the real providers' responses, trimmed to the fields
// catlog reads plus enough of their neighbours that the shape is recognisable
// (§5.8.1). No email field appears anywhere: catlog never asks for one (D17).

// --- Discord -----------------------------------------------------------------

// discordToken is `POST https://discord.com/api/oauth2/token`.
type discordToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// discordUser is `GET https://discord.com/api/users/@me`. The snowflake is a
// **string** in Discord's JSON — it does not fit a JS number — and catlog reads
// exactly this field (§4.7).
type discordUser struct {
	ID            string  `json:"id"`
	Username      string  `json:"username"`
	Discriminator string  `json:"discriminator"`
	GlobalName    string  `json:"global_name"`
	Avatar        *string `json:"avatar"`
}

func (s *Server) handleDiscordToken(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.tokenRequest(w, r, IdPDiscord)
	if !ok {
		return
	}
	tok, err := s.mintToken(IdPDiscord, acct.Sub)
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	refresh, err := randomToken()
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, discordToken{
		AccessToken:  tok,
		TokenType:    "Bearer",
		ExpiresIn:    604800,
		RefreshToken: refresh,
		Scope:        "identify",
	})
}

func (s *Server) handleDiscordMe(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.bearer(r, IdPDiscord)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]int{"code": 0, "message": 401})
		return
	}
	writeJSON(w, http.StatusOK, discordUser{
		ID:            acct.Sub,
		Username:      acct.Name,
		Discriminator: "0",
		GlobalName:    acct.Label,
	})
}

// --- Google ------------------------------------------------------------------

// googleToken is `POST https://oauth2.googleapis.com/token` for the OIDC code
// flow. `id_token` is the only field catlog reads.
type googleToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
}

// googleIDClaims is the id_token catlog verifies against the JWKS (§4.7).
type googleIDClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	AZP      string `json:"azp"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func (s *Server) handleGoogleToken(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.tokenRequest(w, r, IdPGoogle)
	if !ok {
		return
	}
	tok, err := s.mintToken(IdPGoogle, acct.Sub)
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	now := s.now()
	claims := googleIDClaims{
		Issuer:   s.googleIssuer(),
		Audience: s.cfg.ClientID,
		Subject:  acct.Sub,
		AZP:      s.cfg.ClientID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	// Really signed, with a key whose public half is only obtainable from
	// /google/jwks — so catlogd's verifier is genuinely exercised (§5.8.1).
	idToken, err := cjws.SignES256(s.googleKey, payload, cjws.SignOptions{Type: "JWT", KeyID: GoogleKID})
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, googleToken{
		AccessToken: tok,
		ExpiresIn:   3599,
		Scope:       "openid",
		TokenType:   "Bearer",
		IDToken:     idToken,
	})
}

// googleIssuer is the `iss` of every id_token: the base URL plus /google, which
// is what `[idp.google] issuer` names in catlogd.dev.toml (§5.3).
func (s *Server) googleIssuer() string { return s.baseURL + "/google" }

func (s *Server) handleGoogleJWKS(w http.ResponseWriter, _ *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &s.googleKey.PublicKey,
		KeyID:     GoogleKID,
		Algorithm: string(cjws.Alg),
		Use:       "sig",
	}}}
	// Real JWKS endpoints are cacheable; catlogd caches on its side too, and
	// must survive this key changing when mockidp restarts.
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, set)
}

// --- GitHub ------------------------------------------------------------------

// githubToken is `POST https://github.com/login/oauth/access_token`. GitHub
// answers form-encoded unless the caller asks for JSON, and mockidp reproduces
// that so a missing Accept header fails here rather than in production.
type githubToken struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
}

// githubUser is `GET https://api.github.com/user`. `id` is a JSON **number**
// and `created_at` is RFC 3339 — the two fields catlog reads (§4.7).
type githubUser struct {
	Login     string `json:"login"`
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Server) handleGitHubToken(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.tokenRequest(w, r, IdPGitHub)
	if !ok {
		return
	}
	tok, err := s.mintToken(IdPGitHub, acct.Sub)
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	if !wantsJSON(r) {
		// GitHub's default: application/x-www-form-urlencoded.
		body := url.Values{"access_token": {tok}, "scope": {""}, "token_type": {"bearer"}}.Encode()
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
		return
	}
	writeJSON(w, http.StatusOK, githubToken{AccessToken: tok, Scope: "", TokenType: "bearer"})
}

func (s *Server) handleGitHubUser(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.bearer(r, IdPGitHub)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	id, err := strconv.ParseInt(acct.Sub, 10, 64)
	if err != nil {
		s.oauthError(w, http.StatusInternalServerError, "server_error", "github id is not numeric")
		return
	}
	writeJSON(w, http.StatusOK, githubUser{
		Login:     acct.Name,
		ID:        id,
		NodeID:    "MDQ6VXNlcg==",
		Type:      "User",
		Name:      acct.Label,
		CreatedAt: acct.CreatedAt.Format(time.RFC3339),
		UpdatedAt: acct.CreatedAt.Format(time.RFC3339),
	})
}

func wantsJSON(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		if v == "" {
			continue
		}
		if containsToken(v, "application/json") {
			return true
		}
	}
	return false
}

func containsToken(header, want string) bool {
	for i := 0; i+len(want) <= len(header); i++ {
		if header[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
