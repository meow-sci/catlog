package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/starfederation/datastar-go/datastar"
)

// Cache headers. Public pages carry the §4.8 header — they are the same public
// facts the JSON API publishes, and a CDN in front of catlog should treat them
// the same way. Anything that depends on a session must not be cached anywhere.
const (
	publicCache  = readapi.CacheControl
	privateCache = "no-store"
)

// --- GET / -------------------------------------------------------------------

// homeData is the front page: where am I, what is happening, and how am I doing.
type homeData struct {
	Featured []readapi.BoardResponse
	// Feed is the panel's server-rendered starting state. The SSE handler
	// replaces the whole list on connect and then prepends live lines, so this
	// is not merely a nicety: it is what the front page shows to a reader whose
	// browser never runs the datastar module at all.
	Feed FeedList
	// Tiles are the global "what is catlog holding" numbers.
	Tiles homeTiles
}

// homeTiles are assembled from `GET /v1/leaderboards` and nothing else.
//
// That endpoint answers "how many players are on each board", which is not the
// same question as "how many events exist" — but it is a question with a bounded
// cost that catlog already computes for the index, and it is the honest version
// of a front-page summary until somebody decides a public `/v1/stats` is worth
// its unbounded half.
type homeTiles struct {
	// Boards is how many boards are published.
	Boards int
	// Placements is the sum of every board's population: one player on one
	// board, counted once per board.
	Placements int64
	// BusiestTitle and BusiestCount name the most-populated board.
	BusiestTitle string
	BusiestStat  string
	BusiestCount int64
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := homeData{Featured: make([]readapi.BoardResponse, 0, len(FeaturedBoards))}
	data.Feed.Source = FeedSourceSSR
	if err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		data.Feed.Rows, err = p.RecentFeed(r.Context(), FeedLimit)
		return err
	}); err != nil {
		// Degrade exactly as the SSE prime does for the same query (feed.go):
		// an empty feed panel — which the stream will fill the moment the read
		// works again — beats 500ing the whole front page over its one live
		// decoration.
		s.deps.Log.Error("priming the front-page feed failed", "err", err)
		data.Feed.Rows = nil
	}
	list, err := s.deps.Read.BoardList(r.Context())
	if err != nil {
		s.serverError(w, r, err, "read the board list")
		return
	}
	data.Tiles.Boards = len(list.Boards)
	for _, b := range list.Boards {
		data.Tiles.Placements += b.Count
		if b.Count > data.Tiles.BusiestCount {
			data.Tiles.BusiestCount, data.Tiles.BusiestTitle, data.Tiles.BusiestStat = b.Count, b.Title, b.Stat
		}
	}
	for _, stat := range FeaturedBoards {
		board, known, err := s.deps.Read.Board(r.Context(), stat, stats.PeriodAllTime, "", stats.ScopePlayer, "", FeaturedRows, 0)
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

// boardData is one board page: the rows, plus what the page needs to offer the
// window selector and the pager.
type boardData struct {
	readapi.BoardResponse
	// Periods are the windows this board can be read over, in display order.
	Periods []string
	// HasMore says a next page probably exists.
	//
	// Inferred from a full page rather than known: `BoardResponse` publishes no
	// total, deliberately, and inferring is honest — a full page might be the
	// last one, in which case the next page says "nobody is on this board yet"
	// and the reader has lost nothing.
	HasMore bool
	// Prev and Next are offsets, valid only when the corresponding flag is set.
	Prev, Next int
	// FirstRank and LastRank are what the pager counts out loud.
	FirstRank, LastRank int
}

// periodLabels are how a window is written for a reader. The API's keys are
// durations ("weekly"); what a reader wants to click is the window ("This
// week"), which is also what makes the selector answer "what happened lately"
// rather than "pick a projection".
var periodLabels = map[string]string{
	stats.PeriodAllTime: "All time",
	stats.PeriodDaily:   "Today",
	stats.PeriodWeekly:  "This week",
	stats.PeriodMonthly: "This month",
	stats.PeriodYearly:  "This year",
}

// periodLabel is [periodLabels] with a fallback, so a window added to the API
// renders its own key rather than an empty chip.
func periodLabel(period string) string {
	if label, ok := periodLabels[period]; ok {
		return label
	}
	return period
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	period, ok := stats.ValidPeriod(r.URL.Query().Get("period"))
	if !ok {
		// A window this server does not serve is not a board that does not
		// exist: say which of the two it is.
		s.notFound(w, r, "catlog has no such window. Try all time, today, this week, this month or this year.")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			s.notFound(w, r, "That is not a page of this board.")
			return
		}
		offset = n
	}

	board, known, err := s.deps.Read.Board(r.Context(), r.PathValue("stat"), period, "", stats.ScopePlayer, "", BoardRows, offset)
	switch {
	case !known:
		s.notFound(w, r, "No such leaderboard.")
		return
	case err != nil:
		s.serverError(w, r, err, "read the leaderboard")
		return
	}

	data := boardData{
		BoardResponse: board,
		Periods:       stats.Periods(),
		HasMore:       len(board.Rows) >= board.Limit,
		Prev:          max(board.Offset-board.Limit, 0),
		Next:          board.Offset + board.Limit,
		FirstRank:     board.Offset + 1,
		LastRank:      board.Offset + len(board.Rows),
	}
	s.render(w, r, http.StatusOK, "board", publicCache, page{
		Title: board.Title + " — catlog",
		Nav:   "boards",
		Data:  data,
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

// --- GET /search -------------------------------------------------------------------

// SearchRows is how many handles a results page shows.
const SearchRows = readapi.MaxSearchLimit

// searchData is `/search?q=`.
type searchData struct {
	// Query is what was typed, trimmed, in the caller's casing.
	Query string
	// TooShort is a query of one character. It is not an error and must not read
	// like one: the API answers 400 below two characters, so the page simply
	// does not ask.
	TooShort bool
	Result   readapi.SearchResponse
	// With is the comparison set being assembled, carried through the URL so
	// "add this one too" is a link rather than a session.
	With []string
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > readapi.MaxQueryLen {
		// Longer than any handle can be, so nothing can match. Truncating is
		// what the search box does client-side; doing it here too means a pasted
		// URL behaves the same way rather than 400ing.
		q = q[:readapi.MaxQueryLen]
	}
	data := searchData{
		Query:    q,
		TooShort: q != "" && len(q) < readapi.MinQueryLen,
		With:     readapi.SplitHandles(strings.Join(r.URL.Query()["with"], ",")),
	}
	if len(q) >= readapi.MinQueryLen {
		data.Result = s.deps.Read.Search(q, SearchRows)
	}
	s.render(w, r, http.StatusOK, "search", publicCache, page{
		Title:  "Search — catlog",
		Nav:    "search",
		Search: q,
		Data:   data,
	})
}

// handleSearchSuggest answers the header box's debounced datastar patch with a
// `#search-suggest` list.
//
// It is an enhancement and nothing depends on it: the box is an ordinary
// `<form action="/search" method="get">` that works with JavaScript off, and
// this only replaces one element inside it.
func (s *Server) handleSearchSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > readapi.MaxQueryLen {
		q = q[:readapi.MaxQueryLen]
	}
	var result readapi.SearchResponse
	if len(q) >= readapi.MinQueryLen {
		result = s.deps.Read.Search(q, SuggestRows)
	}
	// Below two characters this renders an empty list, which is what clears the
	// popover on backspace. It never renders an error: the right fix for the
	// API's 400 is not to send the request.
	html, err := s.fragment("search-suggest", result)
	if err != nil {
		s.deps.Log.Error("rendering search suggestions failed", "err", err)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElements(html); err != nil {
		s.deps.Log.Debug("search suggestion patch failed", "err", err)
	}
}

// SuggestRows is how many suggestions the header box offers. Smaller than a
// results page: this is a shortcut, not the search.
const SuggestRows = 8

// --- GET /compare -------------------------------------------------------------------

// compareData is `/compare?handles=`.
type compareData struct {
	readapi.CompareResponse
	// Asked is how many handles the query string named before the cap.
	Asked int
	// Columns is the effective handle list, with the URL that drops each one.
	Columns []compareColumn
	// Rows is the pivot: one per board, each already aligned to Columns, so the
	// template never has to look a handle up in a map.
	Rows []compareRow
}

// compareColumn is one handle heading.
type compareColumn struct {
	readapi.ComparePlayer
	// RemoveURL is the comparison without this handle.
	RemoveURL string
}

// compareRow is one board across every compared handle.
type compareRow struct {
	readapi.CompareBoard
	// Cells is one per column, in column order. A handle that is not on this
	// board gets an absent cell rather than a zero one — the same rule the folds
	// follow for a missing measurement.
	Cells []compareCell
}

// compareCell is one player's placement on one compared board, or its absence.
type compareCell struct {
	Present bool
	Best    bool
	Row     readapi.CompareRow
}

// pivot aligns each board's rows to the column order and marks the best cell.
//
// Two rules, both of which a comparison table gets wrong by default:
//
//   - **"Best" is decided by the board's published `ascending`, never inferred
//     from the numbers.** `fastest_to_luna` ranks the smallest value first, and
//     a table that guessed would crown the slowest ascent on exactly the boards
//     where the mistake is hardest to spot.
//   - **A tie marks every tied cell.** The counter boards tie constantly — two
//     players with one ocean-impact RUD each — and picking whichever arrived
//     first would be an arbitrary winner presented as a fact.
//
// A board only one of them is on marks nothing: there is nobody to be better
// than, and a mark there would read as a claim about the board rather than about
// the comparison.
func pivot(out readapi.CompareResponse) []compareRow {
	rows := make([]compareRow, 0, len(out.Boards))
	for _, board := range out.Boards {
		byHandle := make(map[string]readapi.CompareRow, len(board.Rows))
		best, haveBest := 0.0, false
		for _, row := range board.Rows {
			byHandle[row.Handle] = row
			if !haveBest || (board.Ascending && row.Value < best) || (!board.Ascending && row.Value > best) {
				best, haveBest = row.Value, true
			}
		}
		contested := haveBest && len(board.Rows) > 1
		cells := make([]compareCell, 0, len(out.Handles))
		for _, col := range out.Handles {
			row, ok := byHandle[col.Handle]
			cells = append(cells, compareCell{
				Present: ok,
				Best:    ok && contested && row.Value == best,
				Row:     row,
			})
		}
		rows = append(rows, compareRow{CompareBoard: board, Cells: cells})
	}
	return rows
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := strings.Join(q["handles"], ",")

	// `?add=` is the form's field. It is merged and redirected away rather than
	// rendered, so the URL in the address bar is always the comparison — which
	// is the entire point of holding the set in the query string.
	if add := strings.TrimSpace(q.Get("add")); add != "" {
		w.Header().Set("Cache-Control", publicCache)
		http.Redirect(w, r, comparePath(raw+","+add), http.StatusFound)
		return
	}

	asked := 0
	for _, h := range strings.Split(raw, ",") {
		if strings.TrimSpace(h) != "" {
			asked++
		}
	}
	handles := readapi.SplitHandles(raw)

	out, err := s.deps.Read.Compare(r.Context(), handles)
	if err != nil {
		s.serverError(w, r, err, "compare those players")
		return
	}

	data := compareData{CompareResponse: out, Asked: asked, Rows: pivot(out)}
	for _, player := range out.Handles {
		rest := make([]string, 0, len(out.Handles)-1)
		for _, other := range out.Handles {
			if other.Handle != player.Handle {
				rest = append(rest, other.Handle)
			}
		}
		data.Columns = append(data.Columns, compareColumn{
			ComparePlayer: player,
			RemoveURL:     comparePath(rest),
		})
	}

	s.render(w, r, http.StatusOK, "compare", publicCache, page{
		Title: "Compare — catlog",
		Nav:   "compare",
		Data:  data,
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
	// Same public cache header as the compare redirect above: the target never
	// varies, so a shared cache may hold the 302 like any other public page.
	w.Header().Set("Cache-Control", publicCache)
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
	// Never publicly cached: the catch-all matches every URL nobody thought
	// of, and `s-maxage` would let each distinct miss occupy its own CDN entry
	// — an unbounded cache anybody can fill by asking for nonsense.
	s.render(w, r, http.StatusNotFound, "notfound", privateCache, page{
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
