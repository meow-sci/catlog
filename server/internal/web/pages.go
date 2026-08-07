package web

import (
	"net/http"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/store"
)

// Cache headers. Public pages carry the §4.8 header — they are the same public
// facts the JSON API publishes, and a CDN in front of catlog should treat them
// the same way. Anything that depends on a session must not be cached anywhere.
const (
	publicCache  = readapi.CacheControl
	privateCache = "no-store"
)

// --- GET / -------------------------------------------------------------------

// homeData is the front page: the top of a few boards, plus the live feed.
type homeData struct {
	Featured []readapi.BoardResponse
	// Feed is the panel's server-rendered starting state. The SSE handler
	// replaces the whole list on connect and then prepends live lines, so this
	// is not merely a nicety: it is what the front page shows to a reader whose
	// browser never runs the datastar module at all.
	Feed FeedList
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := homeData{Featured: make([]readapi.BoardResponse, 0, len(FeaturedBoards))}
	data.Feed.Source = FeedSourceSSR
	if err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		data.Feed.Rows, err = p.RecentFeed(r.Context(), FeedLimit)
		return err
	}); err != nil {
		s.serverError(w, r, err, "read the activity feed")
		return
	}
	for _, stat := range FeaturedBoards {
		board, known, err := s.deps.Read.Board(r.Context(), stat, FeaturedRows, 0)
		if err != nil {
			s.serverError(w, r, err, "read the featured boards")
			return
		}
		if !known {
			// A featured stat that is not a board in this build is a
			// programming error, but not one worth a blank front page over.
			s.deps.Log.Warn("featured board does not exist", "stat", stat)
			continue
		}
		data.Featured = append(data.Featured, board)
	}
	s.render(w, r, http.StatusOK, "home", publicCache, page{
		Title: "catlog — leaderboards for things that went wrong",
		Nav:   "home",
		Data:  data,
	})
}

// --- GET /boards ---------------------------------------------------------------

func (s *Server) handleBoards(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Read.BoardList(r.Context())
	if err != nil {
		s.serverError(w, r, err, "read the board list")
		return
	}
	s.render(w, r, http.StatusOK, "boards", publicCache, page{
		Title: "Leaderboards — catlog",
		Nav:   "boards",
		Data:  list,
	})
}

// --- GET /boards/{stat} ---------------------------------------------------------

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	board, known, err := s.deps.Read.Board(r.Context(), r.PathValue("stat"), BoardRows, 0)
	switch {
	case !known:
		s.notFound(w, r, "No such leaderboard.")
		return
	case err != nil:
		s.serverError(w, r, err, "read the leaderboard")
		return
	}
	s.render(w, r, http.StatusOK, "board", publicCache, page{
		Title: board.Title + " — catlog",
		Nav:   "boards",
		Data:  board,
	})
}

// --- GET /p/{handle} ------------------------------------------------------------

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	player, known, err := s.deps.Read.Player(r.Context(), r.PathValue("handle"))
	switch {
	case !known:
		// Unknown, retired and banned are one answer (§4.8): anything else
		// makes the page a ban oracle.
		s.notFound(w, r, "No such player.")
		return
	case err != nil:
		s.serverError(w, r, err, "read the profile")
		return
	}
	s.render(w, r, http.StatusOK, "profile", publicCache, page{
		Title: player.Handle + " — catlog",
		Nav:   "boards",
		Data:  player,
	})
}

// --- GET /login ------------------------------------------------------------------

// idp is one sign-in choice on /login.
type idp struct {
	Key   string
	Name  string
	Start string
	// Note is what the provider gives catlog and what it does not.
	Note string
}

// loginData drives /login.
type loginData struct {
	IdPs []idp
	// Next is where to go after signing in. Only ever a same-site absolute path.
	Next string
}

// idps are the three §4.7 providers. Their `Note` text is a contract with D17,
// not marketing: catlog requests `identify` / `openid` / no scope respectively,
// and mockidp rejects an authorize request that asks for an email at all.
var idps = []idp{
	{"discord", "Discord", "/auth/discord/start", "identify scope only — catlog reads your user id and nothing else"},
	{"google", "Google", "/auth/google/start", "openid scope only — catlog reads the subject of your id_token"},
	{"github", "GitHub", "/auth/github/start", "no scopes — catlog reads your numeric user id"},
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "login", privateCache, page{
		Title: "Sign in — catlog",
		Nav:   "account",
		Data:  loginData{IdPs: idps, Next: safeNext(r.URL.Query().Get("next"))},
	})
}

// safeNext sanitises a `?next=` parameter down to a same-site absolute path.
// Anything else — a scheme, a host, a protocol-relative `//evil` — becomes the
// dashboard, so the sign-in page can never be turned into an open redirector.
func safeNext(next string) string {
	if len(next) < 2 || next[0] != '/' || next[1] == '/' {
		return "/dashboard"
	}
	return next
}

// --- GET /docs/{page} -------------------------------------------------------------

// docPages are the §12 WP5 documentation pages.
var docPages = map[string]string{
	"install": "Installing catlog",
	"privacy": "Privacy",
	"api":     "API",
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("page")
	title, ok := docPages[name]
	if !ok {
		s.notFound(w, r, "No such documentation page.")
		return
	}
	s.render(w, r, http.StatusOK, "docs_"+name, publicCache, page{
		Title: title + " — catlog",
		Nav:   "docs",
		Data:  nil,
	})
}

// handleDocsIndex sends bare /docs to the install page rather than 404ing: it is
// the page somebody arriving from the game needs first.
func (s *Server) handleDocsIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/docs/install", http.StatusFound)
}

// --- errors -----------------------------------------------------------------------

// notFoundData drives both the catch-all and the "no such thing" pages.
type notFoundData struct {
	Detail string
	Path   string
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.notFound(w, r, "That page does not exist.")
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, detail string) {
	s.render(w, r, http.StatusNotFound, "notfound", publicCache, page{
		Title: "Not found — catlog",
		Data:  notFoundData{Detail: detail, Path: r.URL.Path},
	})
}

// serverError logs the cause and shows a page that does not repeat it. The
// detail can name a database file or a query; the visitor gets the verb only.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error, what string) {
	s.deps.Log.Error("page render failed", "path", r.URL.Path, "what", what, "err", err)
	s.render(w, r, http.StatusInternalServerError, "notfound", privateCache, page{
		Title: "Something went wrong — catlog",
		Data:  notFoundData{Detail: "catlog could not " + what + ".", Path: r.URL.Path},
	})
}

// AuthError renders the login-failure page (§4.9), installed into the identity
// server by catlogd via identity.Server.SetErrorPage.
//
// The contract it must keep is the one WP3's built-in fallback established and
// the playwright suite asserts: `#auth-error[data-error="<code>"]`,
// `#auth-error-code`, `#auth-error-detail`, `#auth-error-retry`. Returning false
// would hand the request back to that fallback; it never does, because the
// template is validated at startup.
func (s *Server) AuthError(w http.ResponseWriter, r *http.Request, status int, code, detail string) bool {
	s.render(w, r, status, "autherror", privateCache, page{
		Title: "Sign-in failed — catlog",
		Nav:   "account",
		Data:  authErrorData{Code: code, Detail: detail},
	})
	return true
}

// authErrorData drives the login-failure page.
type authErrorData struct {
	Code   string
	Detail string
}
