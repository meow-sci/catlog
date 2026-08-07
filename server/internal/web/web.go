// Package web renders the catlog site with html/template (go:embed) and serves
// the datastar SSE feed (§5.7).
//
// # Where the numbers come from
//
// Nothing here queries projections.db directly. Every board, row and rank comes
// from [readapi]'s query methods — the same code `GET /v1/leaderboards/{stat}`
// answers with — because the over-fetch-and-drop pass that hides banned players
// (§5.4: the two database files cannot be joined) is the single place that
// filter exists. A page that assembled its own rows would be a second place for
// a banned player to reappear, and it would be the more visible of the two.
//
// # What may never appear on a page
//
// A `user_key`, a license, or a private key. The dashboard is the only page that
// sees any of the three (its [identity.DashboardData] carries `sub`), and it
// renders none of them: the credential file is assembled in the browser by
// `keygen.js` and the private half never leaves it (§4.6, §5.7, risk 6).
//
// # HTML and static assets
//
// Templates live here rather than in `site/` because they are server code (D14).
// `site/` owns the CSS, the vendored datastar bundle and `keygen.js`, which Go
// serves from `dist/` at `/static/` in development and nginx serves in
// production (§5.3 `static_dir`).
package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// FeaturedBoards are the three boards the home page shows the top three of
// (§5.7). One record, one speed record and one counter, so the front page shows
// what the three kinds of board look like without anybody having to click.
//
// Every entry is deliberately a *fixed* board key — one of the constants above,
// which exist because a fold of that name does. The board index is otherwise
// assembled from the data (stats.Catalog), and a front page pinned to a board
// that only exists while somebody keeps flying there would be a front page that
// can empty itself. handleHome skips an entry that is not a board, so being
// wrong here costs a log line rather than a blank page.
var FeaturedBoards = []string{
	stats.StatBiggestLithobrakeSurvived,
	stats.StatFastestOrbitalSpeed,
	stats.StatRUDTotal,
}

// FeaturedRows is how many rows of each featured board the home page shows.
const FeaturedRows = 3

// BoardRows is the row cap on `/boards/{stat}` (§5.7).
const BoardRows = 100

// Read is the subset of [readapi.Server] the pages need.
type Read interface {
	BoardList(ctx context.Context) (readapi.BoardsResponse, error)
	Board(ctx context.Context, stat string, limit, offset int) (readapi.BoardResponse, bool, error)
	Player(ctx context.Context, handle string) (readapi.PlayerResponse, bool, error)
}

// Projections runs a query against the live projections handle while holding the
// rebuild swap's read lock (§5.6). *projector.Live implements it. Only the SSE
// feed uses it, to prime a new subscriber from `feed`.
type Projections interface {
	With(fn func(*store.Projections) error) error
}

// Feed is the §5.6 broadcaster. *projector.Broadcaster implements it.
type Feed interface {
	Subscribe() (<-chan []store.FeedRow, func())
}

// Accounts loads the signed-in account's dashboard. *identity.Server implements
// it.
type Accounts interface {
	LoadDashboard(ctx context.Context, uk keys.UserKey) (identity.DashboardData, error)
}

// Deps is what the web UI needs from the rest of the server.
type Deps struct {
	// Config supplies the base URL (shown in the docs) and static_dir.
	Config config.Config
	// Read answers every public page. Required.
	Read Read
	// Projections primes a new SSE subscriber. Required.
	Projections Projections
	// Feed is the live half of the same. Required.
	Feed Feed
	// Sessions gates /dashboard. Required.
	Sessions *identity.Sessions
	// Accounts loads what /dashboard renders. Required.
	Accounts Accounts
	// Log receives one line per failed render.
	Log *slog.Logger
}

// Server serves the §5.7 HTML routes and the SSE feed.
type Server struct {
	deps Deps
	tpl  *templateSet
}

// New builds the web UI, parsing and validating every template up front: a
// template that does not parse is a startup failure rather than a 500 on the one
// page nobody visits until launch day.
func New(deps Deps) (*Server, error) {
	switch {
	case deps.Read == nil:
		return nil, errors.New("web: Read is required")
	case deps.Projections == nil:
		return nil, errors.New("web: Projections is required")
	case deps.Feed == nil:
		return nil, errors.New("web: Feed is required")
	case deps.Sessions == nil:
		return nil, errors.New("web: Sessions is required")
	case deps.Accounts == nil:
		return nil, errors.New("web: Accounts is required")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	tpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{deps: deps, tpl: tpl}, nil
}

// Register mounts the §5.7 routes.
//
// `GET /` is the catch-all 404 page and must be registered last in spirit if not
// in code: Go's pattern router picks the most specific match, so every route
// above — and every route the ingest, read and identity servers registered —
// still wins over it.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /boards", s.handleBoards)
	mux.HandleFunc("GET /boards/{stat}", s.handleBoard)
	mux.HandleFunc("GET /p/{handle}", s.handleProfile)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("GET /dashboard", s.handleDashboard)
	mux.HandleFunc("GET /docs/{page}", s.handleDocs)
	mux.HandleFunc("GET /docs", s.handleDocsIndex)

	// The one route that must not carry the §4.8 cache header.
	mux.HandleFunc("GET "+FeedPath, s.handleFeed)

	if dir := s.deps.Config.Server.StaticDir; dir != "" {
		mux.Handle("GET /static/", s.staticHandler(dir))
	}

	mux.HandleFunc("GET /", s.handleNotFound)
}

// FeedPath is the §4.8 SSE endpoint.
const FeedPath = "/v1/feed/sse"

// staticHandler serves `site/dist` at `/static/` in development (§5.3). In
// production static_dir is empty and nginx serves the same tree, so this route
// does not exist there at all.
func (s *Server) staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are content-addressed by nothing at all — there is no hashing
		// step in the §8 build — so a short max-age is the honest answer. Long
		// enough to spare the page load, short enough that `make site-build`
		// followed by a reload shows the change.
		w.Header().Set("Cache-Control", "public, max-age=60")
		fs.ServeHTTP(w, r)
	}))
}
