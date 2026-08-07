package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/meow-sci/catlog/server/internal/identity"
)

// dashboardData is what `/dashboard` renders (§5.7).
//
// It carries [identity.DashboardData] rather than copying fields out of it, and
// the template renders `.Me.IdP`, the quotas and the handle list — never
// `.Me.Sub`. That value is the `user_key`: §5.11 keeps it out of logs, and a
// page is a worse place for it than a log.
type dashboardData struct {
	Account identity.DashboardData
	// LicenseTTLDays and the quota numbers are shown so a player knows the
	// limits before hitting one (§5.7).
	Live    []identity.HandleView
	Retired []identity.HandleView
	// CanClaim is false once the handle quota is used up; the wizard renders
	// disabled rather than absent, so the reason is visible.
	CanClaim bool
	// CanIssue is false once the 24 h issuance quota is used up.
	CanIssue bool
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	uk, err := s.deps.Sessions.From(r)
	if err != nil {
		if !errors.Is(err, identity.ErrNoSession) {
			// A forged or expired cookie: drop it, so the browser stops
			// presenting something that will never work.
			s.deps.Sessions.Clear(w)
			s.deps.Log.Warn("session cookie rejected at the dashboard", "err", err)
		}
		s.toLogin(w, r)
		return
	}

	account, err := s.deps.Accounts.LoadDashboard(r.Context(), uk)
	switch {
	case errors.Is(err, identity.ErrNoAccount):
		// Banned or purged since the cookie was minted. Clearing it is what
		// stops the browser looping between here and /login.
		s.deps.Sessions.Clear(w)
		s.toLogin(w, r)
		return
	case err != nil:
		s.serverError(w, r, err, "read your account")
		return
	}

	data := dashboardData{
		Account:  account,
		CanClaim: account.Me.Handles < account.Me.HandleQuota,
		CanIssue: account.Me.Issuances24h < account.Me.IssuanceQuota,
	}
	for _, h := range account.Handles {
		if h.Retired {
			data.Retired = append(data.Retired, h)
		} else {
			data.Live = append(data.Live, h)
		}
	}

	s.render(w, r, http.StatusOK, "dashboard", privateCache, page{
		Title:   "Dashboard — catlog",
		Nav:     "account",
		Scripts: []string{"/static/js/keygen.js"},
		Data:    data,
	})
}

// toLogin sends an unauthenticated visitor to sign in, remembering where they
// were headed.
func (s *Server) toLogin(w http.ResponseWriter, r *http.Request) {
	dest := "/login?next=" + url.QueryEscape(r.URL.Path)
	w.Header().Set("Cache-Control", privateCache)
	http.Redirect(w, r, dest, http.StatusFound)
}
