package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// IssuanceWindow is the §4.7 rolling window the issuance quota is counted over.
const IssuanceWindow = 24 * time.Hour

// --- responses (§4.8) ----------------------------------------------------------

// MeResponse is `GET /api/me`.
type MeResponse struct {
	// Sub is `b64u(user_key)` — the same value a license carries in `sub`
	// (§4.5.1). Returned only to the account it belongs to.
	Sub string `json:"sub"`
	// IdP is which provider this account signed in with. There is no
	// cross-provider merge (D10), so it never changes.
	IdP string `json:"idp"`
	// Since is the account's creation time in unix ms.
	Since int64 `json:"since"`
	// Handles / HandleQuota and Issuances24h / IssuanceQuota are the §4.7
	// quotas, reported so the dashboard can grey out a button rather than
	// discover the limit by hitting it.
	Handles       int `json:"handles"`
	HandleQuota   int `json:"handle_quota"`
	Issuances24h  int `json:"issuances_24h"`
	IssuanceQuota int `json:"issuance_quota"`
	// LicenseTTLDays is how long a freshly issued license lasts (D16).
	LicenseTTLDays int `json:"license_ttl_days"`
}

// HandlesResponse is `GET /api/handles`.
type HandlesResponse struct {
	Handles []HandleView `json:"handles"`
}

// HandleView is one owned handle and the credentials issued against it.
type HandleView struct {
	Handle string `json:"handle"`
	Since  int64  `json:"since"`
	// Retired is true when a ban retired this handle. The account still owns
	// it — nobody else can ever claim it (D9) — but it is not usable.
	Retired     bool             `json:"retired"`
	Credentials []CredentialView `json:"credentials"`
}

// CredentialView is the credential metadata the dashboard lists (§5.7). It
// never includes key material: the server has only the thumbprint.
type CredentialView struct {
	JKT        string `json:"jkt"`
	LicenseJTI string `json:"license_jti"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Revoked    bool   `json:"revoked"`
	RevokedAt  int64  `json:"revoked_at,omitempty"`
}

// LicenseResponse is what `POST /api/handles` and `.../reissue` return. §4.8
// specifies `{license}`; the rest is metadata the wizard would otherwise have
// to decode the JWS to learn (§5.7 step 3 shows the jkt fingerprint).
type LicenseResponse struct {
	Handle    string `json:"handle"`
	JKT       string `json:"jkt"`
	License   string `json:"license"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// RevokeResponse is `POST /api/handles/{handle}/revoke`.
type RevokeResponse struct {
	Handle  string   `json:"handle"`
	Revoked []string `json:"revoked"`
}

// ClaimRequest is `POST /api/handles`.
type ClaimRequest struct {
	Handle string `json:"handle"`
	// JWK is the client's **public** key as {kty,crv,x,y}. The private half is
	// generated in the browser and never sent (§4.6, §5.7) — a JWK carrying a
	// `d` is rejected.
	JWK json.RawMessage `json:"jwk"`
}

// ReissueRequest is `POST /api/handles/{handle}/reissue`.
type ReissueRequest struct {
	JWK json.RawMessage `json:"jwk"`
}

// reloadDirectory refreshes the in-memory player_id ↔ handle map (§5.4).
//
// Every mutating identity path calls it, including the ones that cannot
// actually change the map (a revoke, a reissue). That is deliberate: the map is
// one indexed scan of a table with one row per handle, and "every write
// reloads" is a rule you can check by reading the call sites, whereas "the
// writes that change handle rows reload" is a judgement that has to be made
// again correctly at every new call site. The failure mode of getting it wrong
// is a player whose events fold perfectly and who is invisible on every board.
func (s *Server) reloadDirectory(ctx context.Context) {
	if s.deps.Directory == nil {
		return
	}
	if err := s.deps.Directory.Reload(ctx); err != nil {
		s.deps.Log.Warn("handle directory reload failed", "err", err)
	}
}

// --- session gate ---------------------------------------------------------------

// account is the authenticated caller of a dashboard endpoint.
type account struct {
	player  store.Player
	userKey keys.UserKey
}

// authenticate resolves the session cookie to a live, unbanned player, writing
// the §4.9 rejection itself when it cannot.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (account, bool) {
	uk, err := s.sessions.From(r)
	if err != nil {
		if !errors.Is(err, ErrNoSession) {
			// A forged or expired cookie: drop it, so the browser stops
			// presenting something that will never work.
			s.sessions.Clear(w)
			s.deps.Log.Warn("session cookie rejected", "err", err, "path", r.URL.Path)
		}
		writeError(w, http.StatusUnauthorized, authz.CodeLicenseInvalid, "sign in to use this endpoint")
		return account{}, false
	}

	// The deny-list first, as at §4.5.3 step 4: a banned or purged account is
	// refused before a query runs.
	if s.deps.Deny.HasSub(uk.B64U()) {
		s.sessions.Clear(w)
		fail(w, authz.CodeBanned, "this account has been banned or deleted")
		return account{}, false
	}

	player, err := s.deps.Events.PlayerByUserKey(r.Context(), uk)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.sessions.Clear(w)
		writeError(w, http.StatusUnauthorized, authz.CodeLicenseInvalid, "this account no longer exists")
		return account{}, false
	case err != nil:
		s.deps.Log.Error("reading the session's player failed", "user_key", uk, "err", err)
		fail(w, authz.CodeInternal, "could not read the account")
		return account{}, false
	case player.Banned():
		s.sessions.Clear(w)
		fail(w, authz.CodeBanned, "this account has been banned")
		return account{}, false
	}
	return account{player: player, userKey: uk}, true
}

// --- GET /api/me -----------------------------------------------------------------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	handles, err := s.deps.Events.HandlesForPlayer(r.Context(), acct.player.ID)
	if err != nil {
		s.deps.Log.Error("listing handles failed", "player", acct.player.ID, "err", err)
		fail(w, authz.CodeInternal, "could not read your handles")
		return
	}
	creds, err := s.deps.Events.CredentialsForPlayer(r.Context(), acct.player.ID)
	if err != nil {
		s.deps.Log.Error("listing credentials failed", "player", acct.player.ID, "err", err)
		fail(w, authz.CodeInternal, "could not read your credentials")
		return
	}

	writeJSON(w, http.StatusOK, MeResponse{
		Sub:            acct.userKey.B64U(),
		IdP:            acct.player.IdP,
		Since:          acct.player.CreatedAt,
		Handles:        len(handles),
		HandleQuota:    s.rules.MaxHandles,
		Issuances24h:   s.recentIssuances(creds),
		IssuanceQuota:  s.rules.IssuancesPerDay,
		LicenseTTLDays: s.deps.Config.Auth.LicenseTTLDays,
	})
}

// recentIssuances counts the licenses issued inside the §4.7 rolling window.
// Revoked ones still count: the quota limits issuance, not live credentials,
// which is what stops "issue, revoke, issue" from being an unlimited loop.
func (s *Server) recentIssuances(creds []store.Credential) int {
	cutoff := s.deps.Now().Add(-IssuanceWindow).UnixMilli()
	n := 0
	for _, c := range creds {
		if c.IssuedAt >= cutoff {
			n++
		}
	}
	return n
}

// --- GET /api/handles -------------------------------------------------------------

func (s *Server) handleListHandles(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	out, err := s.handleViews(r.Context(), acct.player.ID)
	if err != nil {
		s.deps.Log.Error("listing handles failed", "player", acct.player.ID, "err", err)
		fail(w, authz.CodeInternal, "could not read your handles")
		return
	}
	writeJSON(w, http.StatusOK, HandlesResponse{Handles: out})
}

func (s *Server) handleViews(ctx context.Context, playerID int64) ([]HandleView, error) {
	handles, err := s.deps.Events.HandlesForPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}
	creds, err := s.deps.Events.CredentialsForPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	byHandle := map[string][]CredentialView{}
	for _, c := range creds {
		v := CredentialView{
			JKT: c.JKT, LicenseJTI: c.LicenseJTI,
			IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt, Revoked: c.Revoked(),
		}
		if c.RevokedAt.Valid {
			v.RevokedAt = c.RevokedAt.Int64
		}
		lc := store.LC(c.Handle)
		byHandle[lc] = append(byHandle[lc], v)
	}

	out := make([]HandleView, 0, len(handles))
	for _, h := range handles {
		retired, err := s.deps.Events.HandleRetired(ctx, h.HandleLC)
		if err != nil {
			return nil, err
		}
		view := HandleView{Handle: h.Handle, Since: h.CreatedAt, Retired: retired}
		view.Credentials = byHandle[h.HandleLC]
		if view.Credentials == nil {
			view.Credentials = []CredentialView{}
		}
		out = append(out, view)
	}
	return out, nil
}

// --- POST /api/handles -------------------------------------------------------------

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var req ClaimRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if code, detail := s.rules.ValidateHandle(req.Handle); code != "" {
		fail(w, code, detail)
		return
	}
	s.issue(w, r, acct, req.Handle, req.JWK, true)
}

// --- POST /api/handles/{handle}/reissue ----------------------------------------------

func (s *Server) handleReissue(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var req ReissueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.issue(w, r, acct, r.PathValue("handle"), req.JWK, false)
}

// issue mints a license for a handle, claiming it first when this is a new
// claim (§4.7, §4.8).
//
// Everything after the key parse runs under the §5.4 write lock, because the
// quota checks and the claim have to be one atomic decision: two concurrent
// claims that both read "4 handles" would otherwise both write a fifth.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, acct account, handle string, rawJWK json.RawMessage, newClaim bool) {
	if len(rawJWK) == 0 {
		fail(w, authz.CodeBadRequest, "a public jwk is required")
		return
	}
	// ParsePublicJWK refuses a JWK carrying a private `d`, which is the one
	// mistake a browser wizard could make that would be catastrophic (§5.7,
	// risk 6): the private key must never reach the server.
	pub, err := cjws.ParsePublicJWK(rawJWK)
	if err != nil {
		fail(w, authz.CodeBadRequest, "jwk must be an EC P-256 public key ({kty,crv,x,y}) with no private part")
		return
	}
	jkt, err := cjws.ThumbprintPublicKey(pub)
	if err != nil {
		fail(w, authz.CodeBadRequest, "jwk cannot be thumbprinted")
		return
	}

	ctx := r.Context()
	now := s.deps.Now()
	var (
		out      LicenseResponse
		apiCode  string
		apiWhy   string
		revoking []string
	)

	err = s.deps.WriteLock(func() error {
		// One credential per key, matching the dev path (§5.9): a reissue
		// means a new key pair, so an accidental replay of the same JWK is an
		// error rather than a silent second license on the same key.
		switch _, err := s.deps.Events.CredentialByJKT(ctx, jkt); {
		case err == nil:
			apiCode, apiWhy = authz.CodeBadRequest, "that key already has a credential; generate a new key pair"
			return nil
		case errors.Is(err, store.ErrNotFound):
		default:
			return err
		}

		creds, err := s.deps.Events.CredentialsForPlayer(ctx, acct.player.ID)
		if err != nil {
			return err
		}
		if n := s.recentIssuances(creds); n >= s.rules.IssuancesPerDay {
			apiCode, apiWhy = authz.CodeQuotaExceeded,
				"this account has already issued its licenses for today; try again tomorrow"
			return nil
		}

		if newClaim {
			owned, err := s.deps.Events.HandlesForPlayer(ctx, acct.player.ID)
			if err != nil {
				return err
			}
			if len(owned) >= s.rules.MaxHandles {
				apiCode, apiWhy = authz.CodeQuotaExceeded, "this account already holds the maximum number of handles"
				return nil
			}
			switch err := s.deps.Events.ClaimHandle(ctx, acct.player.ID, handle, now.UnixMilli()); {
			case errors.Is(err, store.ErrHandleRetired):
				apiCode, apiWhy = authz.CodeHandleTaken, "that handle is retired and can never be claimed again"
				return nil
			case errors.Is(err, store.ErrHandleTaken):
				apiCode, apiWhy = authz.CodeHandleTaken, "that handle is already taken"
				return nil
			case err != nil:
				return err
			}
		} else {
			// A reissue is only ever for a handle this account already holds.
			existing, err := s.deps.Events.HandleByLC(ctx, handle)
			switch {
			case errors.Is(err, store.ErrNotFound), err == nil && existing.PlayerID != acct.player.ID:
				apiCode, apiWhy = authz.CodeNotFound, "you do not hold that handle"
				return nil
			case err != nil:
				return err
			}
			handle = existing.Handle // the stored casing wins (§4.7)

			// Reissue is the deny-list touchpoint (D16): the credential being
			// replaced stops working, which is the whole reason a player who
			// lost their credential file reissues rather than re-downloading.
			for _, c := range creds {
				if store.LC(c.Handle) == store.LC(handle) && !c.Revoked() {
					revoking = append(revoking, c.JKT)
				}
			}
		}

		license, claims, err := authz.IssueLicense(s.deps.Keys.Signing, authz.IssueRequest{
			Issuer:   s.deps.Config.Server.BaseURL,
			UserKey:  acct.userKey,
			Handle:   handle,
			JKT:      jkt,
			IssuedAt: now,
			TTL:      s.deps.Config.LicenseTTL(),
		})
		if err != nil {
			return err
		}
		if err := s.deps.Events.InsertCredential(ctx, nil, store.Credential{
			JKT:        jkt,
			PlayerID:   acct.player.ID,
			Handle:     handle,
			LicenseJTI: claims.JTI,
			IssuedAt:   claims.IssuedAt * 1000,
			ExpiresAt:  claims.ExpiresAt * 1000,
		}); err != nil {
			return err
		}
		for _, old := range revoking {
			if err := s.moderator.RevokeCredential(ctx, old); err != nil {
				return err
			}
		}

		out = LicenseResponse{
			Handle: handle, JKT: jkt, License: license,
			IssuedAt: claims.IssuedAt * 1000, ExpiresAt: claims.ExpiresAt * 1000,
		}
		return nil
	})
	if err != nil {
		s.deps.Log.Error("issuing a license failed", "player", acct.player.ID, "handle", handle, "err", err)
		fail(w, authz.CodeInternal, "could not issue the license")
		return
	}
	if apiCode != "" {
		fail(w, apiCode, apiWhy)
		return
	}

	// The handle has to reach the read side before the player can be found by
	// it (§5.4).
	s.reloadDirectory(ctx)
	s.deps.Log.Info("license issued", "player", acct.player.ID, "handle", out.Handle,
		"jkt", out.JKT, "new_handle", newClaim, "replaced", len(revoking), "expires_at", out.ExpiresAt)
	writeJSON(w, http.StatusOK, out)
}

// --- POST /api/handles/{handle}/revoke -----------------------------------------------

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	handle := r.PathValue("handle")

	ctx := r.Context()
	var (
		out     RevokeResponse
		apiCode string
		apiWhy  string
	)
	err := s.deps.WriteLock(func() error {
		existing, err := s.deps.Events.HandleByLC(ctx, handle)
		switch {
		case errors.Is(err, store.ErrNotFound), err == nil && existing.PlayerID != acct.player.ID:
			apiCode, apiWhy = authz.CodeNotFound, "you do not hold that handle"
			return nil
		case err != nil:
			return err
		}
		out.Handle = existing.Handle

		creds, err := s.deps.Events.CredentialsForPlayer(ctx, acct.player.ID)
		if err != nil {
			return err
		}
		for _, c := range creds {
			if store.LC(c.Handle) != existing.HandleLC || c.Revoked() {
				continue
			}
			if err := s.moderator.RevokeCredential(ctx, c.JKT); err != nil {
				return err
			}
			out.Revoked = append(out.Revoked, c.JKT)
		}
		return nil
	})
	if err != nil {
		s.deps.Log.Error("revoking credentials failed", "player", acct.player.ID, "handle", handle, "err", err)
		fail(w, authz.CodeInternal, "could not revoke the credentials")
		return
	}
	if apiCode != "" {
		fail(w, apiCode, apiWhy)
		return
	}
	if out.Revoked == nil {
		out.Revoked = []string{}
	}
	s.reloadDirectory(ctx)
	// The handle itself stays: it is immutable and permanent (D9). What is
	// revoked is the ability to ship with it.
	s.deps.Log.Info("credentials revoked from the dashboard",
		"player", acct.player.ID, "handle", out.Handle, "count", len(out.Revoked))
	writeJSON(w, http.StatusOK, out)
}

// --- POST /api/me/delete ---------------------------------------------------------

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	var res PurgeResult
	err := s.deps.WriteLock(func() error {
		var err error
		res, err = s.moderator.Purge(r.Context(), acct.player, "account deleted by its owner")
		return err
	})
	if err != nil {
		s.deps.Log.Error("delete-my-data failed", "player", acct.player.ID, "err", err)
		fail(w, authz.CodeInternal, "could not delete the account")
		return
	}

	// The session goes with the data. It would fail authenticate on the next
	// request anyway — the tombstone puts the sub on the deny-list — but a
	// browser that is still holding a cookie for a deleted account is a bug
	// report waiting to happen.
	s.sessions.Clear(w)
	writeJSON(w, http.StatusOK, res)
}

// --- POST /api/logout ------------------------------------------------------------

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.sessions.Clear(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeJSON reads a capped, strict JSON body.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(w, authz.CodeBadRequest, "request body: "+err.Error())
		return false
	}
	return true
}
