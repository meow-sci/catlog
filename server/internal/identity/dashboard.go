package identity

import (
	"context"
	"errors"
	"net/http"

	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// ErrNoAccount reports a session that no longer resolves to a usable account:
// the player was purged, or banned since the cookie was issued.
//
// The web UI treats it exactly like a missing cookie — clear the session and
// send the browser to /login — which is why it is one error rather than two.
// Telling a banned player *which* of the two happened on a page they can reach
// without signing in again would leak moderation state.
var ErrNoAccount = errors.New("identity: session does not resolve to a live account")

// DashboardData is everything `/dashboard` renders (§5.7).
//
// It deliberately reuses the §4.8 JSON shapes rather than defining page structs:
// the dashboard and `GET /api/me` + `GET /api/handles` must never disagree, and
// the cheapest way to guarantee that is for them to be the same values.
//
// Note what is *not* here: [MeResponse.Sub] is populated (it is the same struct
// the API returns to the account that owns it) but no template may render it.
// A `user_key` on a page is one screenshot away from being public, and §5.11
// keeps it out of logs for the same reason.
type DashboardData struct {
	Me      MeResponse
	Handles []HandleView
}

// LoadDashboard assembles [DashboardData] for the account a session cookie
// resolved to.
//
// Exported for package web, which owns the page but must not own the queries:
// the handle/credential view is the same code path `GET /api/handles` serves,
// including the retired-handle lookup, so the page cannot drift from the API.
func (s *Server) LoadDashboard(ctx context.Context, uk keys.UserKey) (DashboardData, error) {
	// The deny-list first, as at §4.5.3 step 4 — a banned or purged account is
	// refused before a query runs.
	if s.deps.Deny.HasSub(uk.B64U()) {
		return DashboardData{}, ErrNoAccount
	}

	player, err := s.deps.Events.PlayerByUserKey(ctx, uk)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return DashboardData{}, ErrNoAccount
	case err != nil:
		return DashboardData{}, err
	case player.Banned():
		return DashboardData{}, ErrNoAccount
	}

	handles, err := s.handleViews(ctx, player.ID)
	if err != nil {
		return DashboardData{}, err
	}
	creds, err := s.deps.Events.CredentialsForPlayer(ctx, player.ID)
	if err != nil {
		return DashboardData{}, err
	}

	return DashboardData{
		Me: MeResponse{
			Sub:            uk.B64U(),
			IdP:            player.IdP,
			Since:          player.CreatedAt,
			Handles:        len(handles),
			HandleQuota:    s.rules.MaxHandles,
			Issuances24h:   s.recentIssuances(creds),
			IssuanceQuota:  s.rules.IssuancesPerDay,
			LicenseTTLDays: s.deps.Config.Auth.LicenseTTLDays,
		},
		Handles: handles,
	}, nil
}

// ErrorPage renders a browser-facing failure of the login flow (§4.9).
//
// [Server.SetErrorPage] installs one; returning false falls back to the
// self-contained page in identity.go, which is what keeps this package's own
// tests — and a catlogd wired without a web UI — working unchanged. The
// signature carries the §4.9 code and detail rather than an error, because the
// code *is* the contract: whoever renders the page must expose it as
// `#auth-error[data-error]`.
type ErrorPage func(w http.ResponseWriter, r *http.Request, status int, code, detail string) bool

// SetErrorPage installs the renderer for login failures. Passing nil restores
// the built-in page.
//
// It is a setter rather than a [Deps] field because package web needs this
// server's [Server.Sessions] to build itself: the two are mutually dependent at
// wiring time, and one of the two directions has to happen after construction.
func (s *Server) SetErrorPage(fn ErrorPage) { s.errorPage = fn }
