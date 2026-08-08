// Package adminapi serves the loopback-only admin mux on 127.0.0.1:6060 —
// never proxied by nginx (§3, §5.9).
//
// # Why there is no authentication
//
// The mux has none, by design: it is bound to the loopback interface, nginx
// never proxies it, and reaching it means already having a shell on the box.
// [Server] additionally refuses any request whose peer is not a loopback
// address, so a misconfigured `admin_listen = "0.0.0.0:6060"` fails closed
// instead of exposing `/admin/issue` to the internet.
//
// # What lives here
//
//	GET  /debug/vars        expvar, including the §5.9 ingest counters
//	GET  /debug/pprof/…     net/http/pprof
//	POST /admin/issue       dev/test credential issuance (§5.9)
//
// WP3 (ban, unban, purge, denylist, backup — see identity.go), WP4 (stats,
// seed, rebuild — see projections.go) and WP10 (archive run and restore — see
// archive.go) add their routes through their own Register… entry point rather
// than by editing [New]; nothing about the shape below had to change to
// accommodate any of them. Every route that writes
// goes through [Server.WithWriteLock], which is the "admin mutex" §5.4 requires
// so admin writes never race the ingest writer goroutine.
package adminapi

import (
	"encoding/json"
	"expvar"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// MaxRequestBytes caps an admin request body. Generous for JSON, small enough
// that a stuck client cannot balloon the process.
const MaxRequestBytes = 64 << 10

// Deps is what the admin mux needs from the rest of the server. Fields the
// current work package does not use are nil-tolerant, so WP3/WP4 can fill them
// in without touching the constructor.
type Deps struct {
	Config   config.Config
	Keys     *keys.Set
	Events   *store.Events
	Verifier *authz.Verifier
	Log      *slog.Logger
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

// Server is the admin mux.
type Server struct {
	deps Deps
	mux  *http.ServeMux
	// mu is the §5.4 admin write mutex: one admin write at a time, and never
	// concurrent with another admin write transaction.
	mu sync.Mutex
	// projections carries the WP4 dependencies, installed by
	// [Server.RegisterProjections]. Zero until then; every route that reads it
	// tolerates nil members.
	projections ProjectionDeps
	// identity carries the WP3 dependencies, installed by
	// [Server.RegisterIdentity]. Same contract as projections.
	identity IdentityDeps
	// clock carries the development-only server clock, installed by
	// [Server.RegisterClock]. Zero (and its routes unmounted) unless
	// `[server] clock_control` is on.
	clock ClockDeps
	// archive carries the WP10 dependencies, installed by
	// [Server.RegisterArchive]. Same contract again.
	archive ArchiveDeps
	// stats memoizes the counting half of GET /admin/stats; see projections.go.
	stats statsCache
}

// New builds the admin mux with the WP2 routes: pprof, expvar and
// POST /admin/issue.
func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Server{deps: deps, mux: http.NewServeMux()}

	// expvar and pprof are mounted explicitly. Importing them also registers
	// their handlers on http.DefaultServeMux — harmless here only because
	// catlogd never serves DefaultServeMux, on either port.
	s.mux.Handle("GET /debug/vars", expvar.Handler())
	s.mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	s.mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	s.mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	s.mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	s.mux.HandleFunc("GET /admin/healthz", s.handleHealthz)
	s.mux.HandleFunc("POST /admin/issue", s.handleIssue)

	return s
}

// HandleFunc mounts an additional route. Later work packages use it rather than
// editing New.
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// Handle mounts an additional handler.
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// WithWriteLock runs fn holding the admin write mutex (§5.4).
func (s *Server) WithWriteLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}

// Handler returns the mux wrapped in the loopback guard.
func (s *Server) Handler() http.Handler { return loopbackOnly(s.mux, s.deps.Log) }

// ServeHTTP makes Server itself the handler, guard included.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }

// loopbackOnly rejects any request from a non-loopback peer. The admin mux is
// unauthenticated (§5.9), so this is the one thing standing between a
// misconfigured listen address and remote credential issuance.
func loopbackOnly(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			log.Warn("admin request from a non-loopback peer refused", "ip", host, "path", r.URL.Path)
			writeError(w, http.StatusForbidden, authz.CodeBadRequest, "the admin API is loopback only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- responses ---------------------------------------------------------------

type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("admin response write failed", "err", err)
	}
}

// writeError emits the §4.9 error shape with an explicit status.
func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, errorBody{Error: code, Detail: detail})
}

// fail emits the §4.9 error shape at the status the code maps to.
func fail(w http.ResponseWriter, code, detail string) {
	writeError(w, authz.Status(code), code, detail)
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
