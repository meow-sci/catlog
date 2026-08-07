package adminapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"regexp"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/store"
)

// DevIdP is the `idp` a synthetic admin-issued player is recorded under, and
// the prefix of its user_key subject: `HMAC(pepper, "dev:" + handle)` (§5.9).
//
// It is deliberately not one of the three real IdPs (§4.7), so a dev credential
// can never collide with a real account and is trivial to find and delete.
const DevIdP = "dev"

// handlePattern is the §4.7 handle format. Only the format is enforced here:
// the reserved list, the quotas and the retired-handle rules belong to the
// identity layer (WP3), and this is the dev-only path.
var handlePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,148}[A-Za-z0-9])?$`)

// IssueRequest is the POST /admin/issue body (§5.9).
type IssueRequest struct {
	// Handle is the handle to issue for; it is created if it does not exist.
	Handle string `json:"handle"`
	// JWK is the client's public key as {kty,crv,x,y}. When absent the server
	// generates a key pair and returns the private half — dev only, and the
	// reason this endpoint is loopback-bound.
	JWK json.RawMessage `json:"jwk,omitempty"`
}

// IssueResponse is what catlogctl turns into a `catlog-credential.json` (§4.6).
type IssueResponse struct {
	Handle    string `json:"handle"`
	JKT       string `json:"jkt"`
	License   string `json:"license"`
	IssuedAt  int64  `json:"issued_at"`  // unix ms
	ExpiresAt int64  `json:"expires_at"` // unix ms
	// PrivateKeyPEM is present only when the server generated the key.
	PrivateKeyPEM string `json:"private_key_pem,omitempty"`
}

// handleIssue implements POST /admin/issue (§5.9): create the synthetic dev
// player if needed, claim the handle, mint a license bound to the client key
// and record the credential.
//
// **Dev and test only.** Real issuance is the dashboard flow of §4.8, where the
// private key is generated in the browser and never reaches the server.
func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	var req IssueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !handlePattern.MatchString(req.Handle) {
		fail(w, authz.CodeHandleInvalid, "handle must match "+handlePattern.String())
		return
	}
	if s.deps.Keys == nil || s.deps.Events == nil {
		fail(w, authz.CodeInternal, "issuance is not configured on this server")
		return
	}

	pub, privPEM, ok := s.clientKey(w, req.JWK)
	if !ok {
		return
	}
	jkt, err := cjws.ThumbprintPublicKey(pub)
	if err != nil {
		fail(w, authz.CodeBadRequest, "jwk cannot be thumbprinted")
		return
	}

	now := s.deps.Now()
	cfg := s.deps.Config
	var res IssueResponse

	err = s.WithWriteLock(func() error {
		ctx := r.Context()

		// One credential per key: re-issuing means a new key pair, which is
		// also what the dashboard flow does (§5.7).
		switch _, err := s.deps.Events.CredentialByJKT(ctx, jkt); {
		case err == nil:
			return errKeyInUse
		case errors.Is(err, store.ErrNotFound):
		default:
			return err
		}

		userKey := s.deps.Keys.UserKey(DevIdP, req.Handle)
		playerID, err := s.deps.Events.EnsurePlayer(ctx, nil, userKey, DevIdP, now.UnixMilli())
		if err != nil {
			return err
		}

		// The handle may already belong to this dev player (a second issuance
		// for the same handle is a reissue); anyone else's handle is refused.
		switch existing, err := s.deps.Events.HandleByLC(ctx, req.Handle); {
		case errors.Is(err, store.ErrNotFound):
			if err := s.deps.Events.ClaimHandle(ctx, playerID, req.Handle, now.UnixMilli()); err != nil {
				return err
			}
		case err != nil:
			return err
		case existing.PlayerID != playerID:
			return errHandleTaken
		}

		license, claims, err := authz.IssueLicense(s.deps.Keys.Signing, authz.IssueRequest{
			Issuer:   cfg.Server.BaseURL,
			UserKey:  userKey,
			Handle:   req.Handle,
			JKT:      jkt,
			IssuedAt: now,
			TTL:      cfg.LicenseTTL(),
		})
		if err != nil {
			return err
		}

		if err := s.deps.Events.InsertCredential(ctx, nil, store.Credential{
			JKT:        jkt,
			PlayerID:   playerID,
			Handle:     req.Handle,
			LicenseJTI: claims.JTI,
			IssuedAt:   claims.IssuedAt * 1000,
			ExpiresAt:  claims.ExpiresAt * 1000,
		}); err != nil {
			return err
		}

		res = IssueResponse{
			Handle:        req.Handle,
			JKT:           jkt,
			License:       license,
			IssuedAt:      claims.IssuedAt * 1000,
			ExpiresAt:     claims.ExpiresAt * 1000,
			PrivateKeyPEM: privPEM,
		}
		return nil
	})

	switch {
	case errors.Is(err, errKeyInUse):
		fail(w, authz.CodeBadRequest, "that key already has a credential; generate a new key pair")
		return
	case errors.Is(err, errHandleTaken), errors.Is(err, store.ErrHandleTaken):
		fail(w, authz.CodeHandleTaken, "that handle belongs to another account")
		return
	case errors.Is(err, store.ErrHandleRetired):
		fail(w, authz.CodeHandleTaken, "that handle has been retired and can never be reclaimed")
		return
	case err != nil:
		s.deps.Log.Error("admin issue failed", "handle", req.Handle, "err", err)
		fail(w, authz.CodeInternal, "could not issue the credential")
		return
	}

	// The handle and the thumbprint are public; the private key, when the
	// server generated it, is in the response body and nowhere else — never in
	// a log line (§5.11).
	s.deps.Log.Info("credential issued", "handle", res.Handle, "jkt", res.JKT, "expires_at", res.ExpiresAt)
	writeJSON(w, http.StatusOK, res)
}

var (
	errKeyInUse    = errors.New("adminapi: key already has a credential")
	errHandleTaken = errors.New("adminapi: handle belongs to another account")
)

// clientKey resolves the request's key: the supplied public JWK, or a freshly
// generated pair whose private half is returned as PKCS#8 PEM (§4.6).
func (s *Server) clientKey(w http.ResponseWriter, raw json.RawMessage) (*ecdsa.PublicKey, string, bool) {
	if len(raw) > 0 {
		pub, err := cjws.ParsePublicJWK(raw)
		if err != nil {
			fail(w, authz.CodeBadRequest, "jwk must be an EC P-256 public key ({kty,crv,x,y})")
			return nil, "", false
		}
		return pub, "", true
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fail(w, authz.CodeInternal, "could not generate a key pair")
		return nil, "", false
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		fail(w, authz.CodeInternal, "could not encode the generated key")
		return nil, "", false
	}
	return &key.PublicKey, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), true
}
