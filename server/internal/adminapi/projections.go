package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/seed"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// ProjectionDeps is what the WP4 admin routes need. It is a separate struct
// from [Deps] on purpose: these routes were added to an existing server and
// registering them through their own entry point keeps the constructor, the
// loopback guard and the write mutex exactly as WP2 left them.
type ProjectionDeps struct {
	// Projector runs the rebuild and reports lag. Required.
	Projector *projector.Projector
	// Directory is reloaded after a seed, so the new handles resolve. Required.
	Directory *directory.Directory
	// Writer supplies the ingest queue depth for /admin/stats. Optional.
	Writer *ingest.Writer
}

// RegisterProjections mounts the §5.9 projection routes:
//
//	POST /admin/seed                   the deterministic demo dataset
//	POST /admin/projections/rebuild    §5.6 rebuild + atomic swap
//	GET  /admin/stats                  counters
func (s *Server) RegisterProjections(deps ProjectionDeps) {
	s.projections = deps
	s.mux.HandleFunc("POST /admin/seed", s.handleSeed)
	s.mux.HandleFunc("POST /admin/projections/rebuild", s.handleRebuild)
	s.mux.HandleFunc("GET /admin/stats", s.handleStats)
}

// --- POST /admin/seed --------------------------------------------------------

// SeedResponse is what `POST /admin/seed` returns (§5.9).
type SeedResponse struct {
	Players  []string `json:"players"`
	Events   int      `json:"events"`
	Accepted int      `json:"accepted"`
	Deduped  int      `json:"deduped"`
	// FoldedTo is the projector checkpoint once the seeded events have been
	// folded. The endpoint waits for that before answering, so a caller can
	// seed and immediately read a board (which is the entire point of seeding).
	FoldedTo int64 `json:"folded_to"`
}

func (s *Server) handleSeed(w http.ResponseWriter, r *http.Request) {
	if s.deps.Keys == nil || s.deps.Events == nil {
		fail(w, authz.CodeInternal, "seeding is not configured on this server")
		return
	}

	var res seed.Result
	err := s.WithWriteLock(func() error {
		var err error
		// Writing to events.db outside the ingest writer goroutine is exactly
		// what the §5.4 admin mutex exists for.
		res, err = seed.Apply(r.Context(), s.deps.Events, s.deps.Keys, s.deps.Now().UnixMilli())
		return err
	})
	if err != nil {
		s.deps.Log.Error("admin seed failed", "err", err)
		fail(w, authz.CodeInternal, "could not seed the demo dataset")
		return
	}

	out := SeedResponse{Players: res.Players, Events: res.Events, Accepted: res.Accepted, Deduped: res.Deduped}

	// The demo handles are new, so the read side has to learn them before the
	// feed can name anyone (§5.4's in-memory map).
	if d := s.projections.Directory; d != nil {
		if err := d.Reload(r.Context()); err != nil {
			s.deps.Log.Warn("admin seed: directory reload failed", "err", err)
		}
	}
	if p := s.projections.Projector; p != nil {
		prog, err := p.Drain(r.Context())
		if err != nil {
			s.deps.Log.Error("admin seed: folding failed", "err", err)
			fail(w, authz.CodeInternal, "seeded, but the projections could not be folded")
			return
		}
		out.FoldedTo = prog.LastSeq
	}

	s.deps.Log.Info("demo dataset seeded",
		"players", out.Players, "events", out.Events, "accepted", out.Accepted,
		"deduped", out.Deduped, "folded_to", out.FoldedTo)
	writeJSON(w, http.StatusOK, out)
}

// --- POST /admin/projections/rebuild -----------------------------------------

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	p := s.projections.Projector
	if p == nil {
		fail(w, authz.CodeInternal, "no projector is running on this server")
		return
	}

	// A rebuild reads the whole event log; it must not inherit a client's
	// impatience, and it must not run twice at once. The write mutex gives the
	// second property; a detached context with a generous ceiling gives the
	// first.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), rebuildTimeout)
	defer cancel()

	var res projector.RebuildResult
	err := s.WithWriteLock(func() error {
		var err error
		res, err = p.Rebuild(ctx)
		return err
	})
	if err != nil {
		s.deps.Log.Error("admin rebuild failed", "err", err)
		fail(w, authz.CodeInternal, "could not rebuild the projections")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// rebuildTimeout bounds a rebuild. Generous: it is two passes over the entire
// event log, and the alternative to finishing is a half-built scratch file.
const rebuildTimeout = 30 * time.Minute

// --- GET /admin/stats --------------------------------------------------------

// StatsResponse is `GET /admin/stats` (§5.9).
type StatsResponse struct {
	Events      EventStats             `json:"events"`
	Ingest      IngestStats            `json:"ingest"`
	Projector   ProjectorStats         `json:"projector"`
	Projections store.ProjectionCounts `json:"projections"`
	Storage     StorageStats           `json:"storage"`
	Boards      []BoardCount           `json:"boards"`
}

// EventStats counts what is in events.db.
type EventStats struct {
	Total   int64 `json:"total"`
	MaxSeq  int64 `json:"max_seq"`
	Players int64 `json:"players"`
	Banned  int64 `json:"banned"`
	Handles int64 `json:"handles"`
}

// IngestStats is the §5.5 write queue.
type IngestStats struct {
	QueueDepth int `json:"queue_depth"`
	QueueCap   int `json:"queue_cap"`
}

// ProjectorStats is the §5.6 loop.
type ProjectorStats struct {
	CheckpointSeq int64    `json:"checkpoint_seq"`
	LagSeq        int64    `json:"lag_seq"`
	Folds         []string `json:"folds"`
}

// StorageStats is the file-size watch §13.1 asks for: with no VACUUM, a purge
// leaves free pages behind and only the file size shows it.
type StorageStats struct {
	EventsDBBytes       int64 `json:"events_db_bytes"`
	EventsWALBytes      int64 `json:"events_wal_bytes"`
	ProjectionsDBBytes  int64 `json:"projections_db_bytes"`
	ProjectionsWALBytes int64 `json:"projections_wal_bytes"`
}

// BoardCount is one board's population.
type BoardCount struct {
	Stat  string `json:"stat"`
	Count int64  `json:"count"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out StatsResponse

	if s.deps.Events != nil {
		total, err := s.deps.Events.CountEvents(ctx, 0)
		if err != nil {
			s.fail(w, err, "count events")
			return
		}
		maxSeq, err := s.deps.Events.MaxSeq(ctx)
		if err != nil {
			s.fail(w, err, "read the event head")
			return
		}
		players, banned, err := s.deps.Events.CountPlayers(ctx)
		if err != nil {
			s.fail(w, err, "count players")
			return
		}
		out.Events = EventStats{Total: total, MaxSeq: maxSeq, Players: players, Banned: banned}
		out.Storage.EventsDBBytes = s.deps.Events.FileSize()
		out.Storage.EventsWALBytes = s.deps.Events.WALSize()
	}
	if d := s.projections.Directory; d != nil {
		out.Events.Handles = int64(d.Len())
	}
	if wtr := s.projections.Writer; wtr != nil {
		out.Ingest = IngestStats{QueueDepth: wtr.QueueDepth(), QueueCap: ingest.QueueDepth}
	}

	if p := s.projections.Projector; p != nil {
		out.Projector = ProjectorStats{
			CheckpointSeq: p.CheckpointSeq(),
			LagSeq:        p.Lag(),
			Folds:         p.FoldNames(),
		}
		var counts map[string]int64
		err := p.Live().With(func(proj *store.Projections) error {
			var err error
			if out.Projections, err = proj.Counts(ctx); err != nil {
				return err
			}
			counts, err = proj.StatCounts(ctx)
			out.Storage.ProjectionsDBBytes = proj.FileSize()
			out.Storage.ProjectionsWALBytes = proj.WALSize()
			return err
		})
		if err != nil {
			s.fail(w, err, "read the projection census")
			return
		}
		for _, b := range stats.Boards() {
			out.Boards = append(out.Boards, BoardCount{Stat: b.Stat, Count: counts[b.Stat]})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fail(w http.ResponseWriter, err error, what string) {
	s.deps.Log.Error("admin stats failed", "what", what, "err", err)
	fail(w, authz.CodeInternal, "could not "+what)
}
