package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/directory"
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
	return &Server{deps: deps, cors: cors{allowed: slices.Clone(deps.AllowedOrigins)}}, nil
}

// Register mounts the §4.8 routes on a mux.
//
// Every route registered here — and only the routes registered here — carries
// the cross-origin read headers (cors.go). `/v1/ingest`, `/api/*`, `/auth/*` and
// the admin mux are mounted elsewhere and stay same-origin.
func (s *Server) Register(mux *http.ServeMux) {
	s.public(mux, "/v1/leaderboards", s.handleBoards)
	s.public(mux, "/v1/leaderboards/{stat}", s.handleBoard)
	s.public(mux, "/v1/players/{handle}", s.handlePlayer)
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
	out, known, err := s.Board(r.Context(), r.PathValue("stat"), limit, offset)
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
func (s *Server) visibleRows(ctx context.Context, stat string, asc bool, limit, offset int) ([]store.StatRow, []string, error) {
	need := offset + limit
	var (
		visible []store.StatRow
		handles []string
		scanned int
	)
	for len(visible) < need && scanned < maxScan {
		page := max(need-len(visible), scanPage)
		var batch []store.StatRow
		err := s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			batch, err = p.Leaderboard(ctx, stat, asc, page, scanned)
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
	// Rank is the player's position among visible players on that board.
	Rank    int             `json:"rank"`
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
// The subtraction is what keeps a profile's rank consistent with the board page
// the same player appears on. Bans are rare, so the extra query only runs when
// somebody actually is banned.
func (s *Server) rank(ctx context.Context, row store.StatRow, asc bool, banned []int64) (int, error) {
	var ahead int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		ahead, err = p.StatAhead(ctx, row.Stat, asc, row.Value, row.UpdatedSeq)
		return err
	})
	if err != nil {
		return 0, err
	}
	if len(banned) == 0 {
		return int(ahead) + 1, nil
	}

	var hidden []store.StatRow
	err = s.deps.Projections.With(func(p *store.Projections) error {
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
