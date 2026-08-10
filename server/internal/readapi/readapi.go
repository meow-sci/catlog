package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// CacheControl is the §4.8 header, verbatim. Every public read response carries
// it; the SSE feed is the only documented exception and lives in package web.
const CacheControl = "public, s-maxage=30, stale-while-revalidate=300"

// Paging bounds for `GET /v1/leaderboards/{stat}` (§4.8).
const (
	// DefaultLimit is what §4.8's example URL uses.
	DefaultLimit = 50
	// MaxLimit is §4.8's ceiling. A larger limit is clamped rather than
	// rejected: this is a cached public endpoint, and clamping keeps one
	// answer per (stat, limit, offset) instead of splitting the cache between
	// a 400 and a 200.
	MaxLimit = 200
	// scanPage is how many rows a filtered page reads from SQL at a time.
	// Bigger than a page so the common case — nobody banned — costs one query.
	scanPage = 256
	// maxScan bounds how far a page will scan looking for visible rows, so a
	// board consisting entirely of banned players cannot turn one request into
	// a full table scan.
	maxScan = 5000
)

// Projections runs a query against the live projections handle while holding the
// rebuild swap's read lock (§5.6). *projector.Live implements it.
type Projections interface {
	With(fn func(*store.Projections) error) error
	// WriteGen is a monotonic count of committed writes to the live handle. It
	// is the board census cache's key; see [Server.statCounts].
	WriteGen() int64
}

// Deps is what the read API needs.
type Deps struct {
	// Projections is the live projections handle. Required.
	Projections Projections
	// Events supplies the timestamps behind `updated_seq` (§4.8). Required.
	Events *store.Events
	// Directory resolves player_id ↔ handle and hides banned players. Required.
	Directory *directory.Directory
	// Feed is the §5.6 broadcaster behind `GET /v1/feed/stream`. Optional: a
	// Server built without one serves the feed snapshot but not the stream.
	Feed Feed
	// RawEvents is the raw event broadcaster behind `GET /v1/events/stream`
	// (*projector.RawBroadcaster). Optional, same rule as Feed: without one the
	// paginated `/v1/events` is served but the stream route does not exist.
	RawEvents RawEvents
	// MaxStreamClients caps concurrent SSE subscribers, per stream route. Zero
	// or less means [DefaultMaxStreamClients]. Over the cap a stream open is
	// answered 429 rate_limited + Retry-After rather than held.
	MaxStreamClients int
	// MinBoardPlayers is how many distinct players a board whose key came out of
	// the event stream (`fastest_to_<body>`, `rud_<cause>`) needs before
	// `GET /v1/leaderboards` lists it. Zero or less means [stats.DefaultMinPlayers].
	//
	// It is the whole of the answer to "a modified client could invent ten
	// thousand body names": one comparison, no new table, no new pipeline stage,
	// and nothing an honest player can trip. See stats.Catalog.
	MinBoardPlayers int
	// Now is the server clock. Defaults to [time.Now].
	//
	// Read for exactly one thing: deciding which rolling window "this week"
	// means when `?period=` is given without `?at=`. It is the same clock that
	// stamps recv_time and therefore the same clock the buckets were computed
	// from, which is what stops the live weekly board from pointing at a window
	// the folds are not writing to.
	Now func() time.Time
	// AllowedOrigins is [config.CORS.AllowedOrigins] — the browser origins that
	// may read these endpoints cross-origin. Empty means same-origin only.
	//
	// It reaches nothing but the routes [Server.Register] mounts. See cors.go
	// for why that boundary is the whole of the security argument.
	AllowedOrigins []string
	// Log receives one line per failed request.
	Log *slog.Logger
}

// Server serves the §4.8 endpoints.
type Server struct {
	deps Deps
	cors cors
	// minBoardPlayers is [Deps.MinBoardPlayers] with its default applied.
	minBoardPlayers int
	// counts memoizes the board census; see [Server.statCounts] in query.go.
	counts countsCache
	// stats memoizes `GET /v1/stats`; see [Server.Stats] in stats.go.
	stats statsCache
	// feedHub fans one broadcaster subscription out to every stream client;
	// see feed.go. Nil when no Feed was supplied.
	feedHub *feedHub
	// eventsHub is feedHub's twin for the raw event stream; see
	// events_stream.go. Nil when no RawEvents was supplied.
	eventsHub *eventsHub
}

// New builds the read API.
func New(deps Deps) (*Server, error) {
	switch {
	case deps.Projections == nil:
		return nil, errors.New("readapi: Projections is required")
	case deps.Events == nil:
		return nil, errors.New("readapi: Events is required")
	case deps.Directory == nil:
		return nil, errors.New("readapi: Directory is required")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	minPlayers := deps.MinBoardPlayers
	if minPlayers < 1 {
		minPlayers = stats.DefaultMinPlayers
	}
	maxClients := deps.MaxStreamClients
	if maxClients <= 0 {
		maxClients = DefaultMaxStreamClients
	}
	s := &Server{
		deps:            deps,
		cors:            cors{allowed: slices.Clone(deps.AllowedOrigins)},
		minBoardPlayers: minPlayers,
	}
	if deps.Feed != nil {
		s.feedHub = newFeedHub(deps.Feed, deps.Log, maxClients)
	}
	if deps.RawEvents != nil {
		s.eventsHub = newEventsHub(s, deps.RawEvents, deps.Log, maxClients)
	}
	return s, nil
}

// Register mounts the §4.8 routes on a mux.
//
// Every route registered here — and only the routes registered here — carries
// the cross-origin read headers (cors.go). `/v1/ingest`, `/api/*`, `/auth/*` and
// the admin mux are mounted elsewhere and stay same-origin.
func (s *Server) Register(mux *http.ServeMux) {
	s.public(mux, "/v1/systems", s.handleSystems)
	s.public(mux, "/v1/systems/{slug}", s.handleSystem)
	s.public(mux, "/v1/leaderboards", s.handleBoards)
	s.public(mux, "/v1/leaderboards/{stat}", s.handleBoard)
	s.public(mux, "/v1/players", s.handleSearch)
	s.public(mux, "/v1/players/{handle}", s.handlePlayer)
	s.public(mux, "/v1/players/{handle}/events", s.handlePlayerEvents)
	s.public(mux, "/v1/compare", s.handleCompare)
	s.public(mux, "/v1/events", s.handleEvents)
	if s.deps.RawEvents != nil {
		s.public(mux, "/v1/events/stream", s.handleEventsStream)
	}
	s.public(mux, "/v1/stats", s.handleStats)
	s.public(mux, "/v1/feed", s.handleFeed)
	if s.deps.Feed != nil {
		s.public(mux, "/v1/feed/stream", s.handleFeedStream)
	}
}

// public mounts one cross-origin-readable route: the GET, plus the OPTIONS that
// answers its preflight.
func (s *Server) public(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	// Go's pattern router treats "GET" as also matching HEAD, so a HEAD request
	// gets the same headers without a second registration.
	mux.HandleFunc("GET "+pattern, s.cors.wrap(h))
	mux.HandleFunc("OPTIONS "+pattern, s.cors.preflight)
}

// --- GET /v1/leaderboards ----------------------------------------------------

// BoardsResponse is `GET /v1/leaderboards` (§4.8).
type BoardsResponse struct {
	Boards []BoardSummary `json:"boards"`
	// MinPlayers is how many distinct players a board whose key came out of the
	// event stream (`fastest_to_<body>`, `rud_<cause>`) needs before it appears
	// in Boards.
	//
	// Published because otherwise the list is inexplicable: a player who has
	// been somewhere new and sees no board for it deserves to be told that it
	// needs a second visitor, rather than to file a bug. It is also what lets a
	// client say so without hard-coding the number.
	MinPlayers int `json:"min_players"`
}

// BoardSummary is one entry of [BoardsResponse].
type BoardSummary struct {
	Stat  string `json:"stat"`
	Title string `json:"title"`
	Unit  string `json:"unit"`
	// Ascending reports that the smallest value ranks first — true for the
	// career-time boards and false for every record and counter board.
	Ascending bool `json:"ascending"`
	// Count is how many players appear on the board. It counts rows, banned
	// players included: an exact figure would need the whole board read and
	// filtered on every request, and the number exists to say "this board has
	// entries", not to be summed against anything.
	Count int64 `json:"count"`
	// Periods are the windows `/v1/leaderboards/{stat}?period=` accepts.
	//
	// A period is a **dimension of a board, not a board**. Listing
	// `rud_total@weekly` as its own entry would multiply the index by five, and
	// with `fastest_to_<body>` taking its keys from the data that multiplier
	// applies to an unbounded list. So the index stays one row per board and
	// says which windows that board can be read over.
	Periods []string `json:"periods"`
}

func (s *Server) handleBoards(w http.ResponseWriter, r *http.Request) {
	out, err := s.BoardList(r.Context())
	if err != nil {
		s.fail(w, r, err, "read board counts")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// --- GET /v1/leaderboards/{stat} ---------------------------------------------

// BoardResponse is `GET /v1/leaderboards/{stat}` (§4.8).
type BoardResponse struct {
	Stat  string `json:"stat"`
	Title string `json:"title"`
	Unit  string `json:"unit"`
	// Ascending reports that the smallest value ranks first (§4.8).
	Ascending bool `json:"ascending"`
	// Period is the window these rows cover: `alltime` unless `?period=` asked
	// otherwise. Echoed so a client never has to remember what it requested.
	Period string `json:"period"`
	// Bucket names the window — `2026-08-07`, `2026-W32`, `2026-08`, `2026` —
	// and is empty for `alltime`. When `?at=` was not given this is the window
	// the server's clock is currently in, so a client can tell which week it is
	// looking at without computing one.
	Bucket string `json:"bucket,omitempty"`
	// Limit and Offset echo the effective paging after clamping.
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Rows   []BoardRow `json:"rows"`
}

// BoardRow is one leaderboard entry (§4.8).
type BoardRow struct {
	Rank   int     `json:"rank"`
	Handle string  `json:"handle"`
	Value  float64 `json:"value"`
	// Context is the board's per-row detail (body, flight, energy_j …), stored
	// verbatim as JSON by the fold. Absent for counter boards.
	Context json.RawMessage `json:"context,omitempty"`
	// Updated is the receive time of the event that set this value, unix ms.
	Updated int64 `json:"updated"`
	// Rewound is set on a career-time row whose career has had an earlier save
	// loaded (§4.1). It qualifies the number and does nothing else: the row is
	// ranked normally and the player is treated no differently.
	Rewound bool `json:"rewound,omitempty"`
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := s.paging(w, r)
	if !ok {
		return
	}
	period, ok := stats.ValidPeriod(r.URL.Query().Get("period"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"period must be one of "+strings.Join(stats.Periods(), ", "))
		return
	}
	bucket := r.URL.Query().Get("at")
	if bucket != "" && (period == stats.PeriodAllTime || !stats.ParseBucket(period, bucket)) {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"at is not a well-formed "+period+" window")
		return
	}

	out, known, err := s.Board(r.Context(), r.PathValue("stat"), period, bucket, limit, offset)
	switch {
	case !known:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such leaderboard")
	case err != nil:
		s.fail(w, r, err, "read leaderboard")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// visibleRows reads one page of a board with banned players removed.
//
// The filter cannot be pushed into SQL — `banned_at` is in the other database
// file (§5.4) — so the page is assembled by over-fetching and dropping. Ranks
// are positional over the visible rows, which is why a ban closes the gap it
// leaves rather than leaving a hole in the numbering.
func (s *Server) visibleRows(ctx context.Context, stat, period, bucket string, asc bool, limit, offset int) ([]store.StatRow, []string, error) {
	need := offset + limit
	var (
		visible []store.StatRow
		handles []string
		scanned int
	)
	for len(visible) < need && scanned < maxScan {
		// The first read is sized to the request — a 3-row featured board should
		// not read 256 rows to serve 3. 4× plus a little slack absorbs the usual
		// zero-or-few bans; only when that was not enough (someone on the page
		// really is banned) do later reads escalate to the full scan page.
		page := scanPage
		if scanned == 0 {
			page = min(max(need*4, need+16), scanPage)
		}
		var batch []store.StatRow
		err := s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			if period == stats.PeriodAllTime {
				batch, err = p.Leaderboard(ctx, stat, asc, page, scanned)
			} else {
				batch, err = p.LeaderboardPeriod(ctx, stat, period, bucket, asc, page, scanned)
			}
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		if len(batch) == 0 {
			break
		}
		scanned += len(batch)
		for _, row := range batch {
			handle, ok := s.deps.Directory.Handle(row.PlayerID)
			if !ok {
				continue // banned, purged, or holding no handle yet
			}
			visible = append(visible, row)
			handles = append(handles, handle)
			if len(visible) >= need {
				break
			}
		}
		if len(batch) < page {
			break // the board is exhausted
		}
	}
	if offset >= len(visible) {
		return nil, nil, nil
	}
	return visible[offset:], handles[offset:], nil
}

// paging reads and clamps ?limit= and ?offset= (§4.8).
func (s *Server) paging(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, ok = s.intParam(w, r, "limit", DefaultLimit)
	if !ok {
		return 0, 0, false
	}
	offset, ok = s.intParam(w, r, "offset", 0)
	if !ok {
		return 0, 0, false
	}
	limit, offset = ClampPaging(limit, offset)
	return limit, offset, true
}

func (s *Server) intParam(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest, name+" must be an integer")
		return 0, false
	}
	return v, true
}

// --- GET /v1/players/{handle} ------------------------------------------------

// PlayerResponse is `GET /v1/players/{handle}` (§4.8).
type PlayerResponse struct {
	Handle string `json:"handle"`
	// Since is when the handle was claimed, unix ms.
	Since int64       `json:"since"`
	Stats []PlayerRow `json:"stats"`
}

// PlayerRow is one of a player's board placements (§4.8).
type PlayerRow struct {
	Stat  string  `json:"stat"`
	Title string  `json:"title"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
	// Ascending reports that the smallest value ranks first — the same flag
	// [BoardSummary] publishes, repeated here because a profile shows a rank
	// next to a value and "#1 with the lowest number" is unreadable without it.
	// Without this a client would have to fetch the board index to render a
	// profile.
	Ascending bool `json:"ascending"`
	// Rank is the player's position among visible players on that board.
	Rank int `json:"rank"`
	// Players is how many players hold a value on the board — the denominator
	// that turns "#3" into "#3 of 41".
	//
	// It counts rows, banned players included, exactly like [BoardSummary.Count]
	// and for the same reason: an exact figure would need the whole board read
	// and filtered on every profile view. Rank is filtered, so a rank can be
	// better than this number implies, never worse.
	Players int64           `json:"players"`
	Context json.RawMessage `json:"context,omitempty"`
	Updated int64           `json:"updated"`
	// Rewound qualifies a career-time value; see [BoardRow.Rewound].
	Rewound bool `json:"rewound,omitempty"`
}

func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	out, known, err := s.Player(r.Context(), r.PathValue("handle"))
	switch {
	case !known:
		// Unknown, retired and banned are one answer on purpose (§4.8): telling
		// them apart would turn this endpoint into a ban oracle.
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
	case err != nil:
		s.fail(w, r, err, "read player stats")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// rank is the player's visible position on a board: everyone ahead of them,
// minus the banned players among those, plus one.
//
// ahead is the unfiltered count, resolved for the whole profile at once by
// [Server.aheadCounts]. The subtraction is what keeps a profile's rank
// consistent with the board page the same player appears on. Bans are rare, so
// the extra query only runs when somebody actually is banned.
func (s *Server) rank(ctx context.Context, row store.StatRow, asc bool, banned []int64, ahead int64) (int, error) {
	if len(banned) == 0 {
		return int(ahead) + 1, nil
	}

	var hidden []store.StatRow
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		hidden, err = p.StatsForPlayers(ctx, row.Stat, banned)
		return err
	})
	if err != nil {
		return 0, err
	}
	var hiddenAhead int64
	for _, h := range hidden {
		better := h.Value > row.Value
		if asc {
			better = h.Value < row.Value
		}
		if better || (h.Value == row.Value && h.UpdatedSeq < row.UpdatedSeq) {
			hiddenAhead++
		}
	}
	return int(max(ahead-hiddenAhead, 0)) + 1, nil
}

// --- responses ---------------------------------------------------------------

type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", CacheControl)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.deps.Log.Debug("read api response write failed", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, detail string) {
	s.writeJSON(w, status, errorBody{Error: code, Detail: detail})
}

// fail logs the cause and answers with the §4.9 `internal` shape. The cause
// never reaches the client.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, what string) {
	s.deps.Log.Error("read api request failed", "path", r.URL.Path, "what", what, "err", err)
	s.writeError(w, http.StatusInternalServerError, authz.CodeInternal, "could not "+what)
}
