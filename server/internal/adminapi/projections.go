package adminapi

import (
	"context"
	"net/http"
	"sync"
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
	Streams     StreamStats            `json:"streams"`
	Projector   ProjectorStats         `json:"projector"`
	Projections store.ProjectionCounts `json:"projections"`
	Storage     StorageStats           `json:"storage"`
	Boards      []BoardCount           `json:"boards"`
}

// StreamStats surfaces the §4.5.3 step-12 chain's one genuinely useful output:
// how many client streams have a hole in them.
//
// A gap means a batch left a client and never arrived — accepted anyway,
// because telemetry is loss-tolerant, but marked permanently. Without this the
// `gap` column was written on every commit and read by nobody, which made the
// chain's cost real and its benefit theoretical. Rising `gapped` against a flat
// `total` is the shape to watch: it means shipments are being lost in transit,
// not that clients are restarting.
type StreamStats struct {
	Total         int64 `json:"total"`
	Gapped        int64 `json:"gapped"`
	GappedPlayers int64 `json:"gapped_players"`
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

// statsCacheTTL bounds how long the census half of `GET /admin/stats` may be
// served from memory once nothing is writing. The loadgen harness and the mod's
// projector-wait both poll this endpoint; a poll loop watching the projector
// catch up must not run a full `count(*)` over the event table on every
// iteration.
//
// It is a backstop, not the correctness argument: the cache is keyed on
// [store.DB.WriteGen], so a committed write invalidates it immediately. The TTL
// only covers what changes without a write of ours — the projections file
// growing under the projector's own transactions, say.
const statsCacheTTL = 30 * time.Second

// statsCache memoizes the counting half of the stats response — everything that
// costs a `count(*)` and can only change when a write commits. The live half —
// max_seq, the projector checkpoint and lag, queue depth, handle count, file
// sizes — is read fresh on every request, because those are exactly the numbers
// the pollers poll for.
//
// The key is (events write generation, projections write generation), not the
// head of the event log. Appending events moves the head, but a purge (§4.7)
// deletes a player's rows without moving it — so a cache keyed on the head
// answered `events.total` with a pre-purge count for the whole TTL, and
// catlog.loadgen's zero-loss invariant read that stale count as its baseline
// and failed. Whatever changes the census had to commit a transaction to change
// it, so counting transactions is the key that cannot miss.
type statsCache struct {
	mu          sync.Mutex
	at          time.Time
	eventsGen   int64
	projGen     int64
	valid       bool
	events      EventStats // Total, Players, Banned; MaxSeq and Handles are always fresh
	streams     StreamStats
	projections store.ProjectionCounts
	boards      []BoardCount
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out StatsResponse

	var maxSeq int64
	if s.deps.Events != nil {
		var err error
		if maxSeq, err = s.deps.Events.MaxSeq(ctx); err != nil {
			s.fail(w, err, "read the event head")
			return
		}
	}

	if err := s.census(ctx, &out); err != nil {
		s.fail(w, err, "read the stats census")
		return
	}
	out.Events.MaxSeq = maxSeq

	if s.deps.Events != nil {
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
		err := p.Live().With(func(proj *store.Projections) error {
			out.Storage.ProjectionsDBBytes = proj.FileSize()
			out.Storage.ProjectionsWALBytes = proj.WALSize()
			return nil
		})
		if err != nil {
			s.fail(w, err, "read the projection files")
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// census fills the counted half of out, from the cache when the head of the
// log has not moved since it was filled and it is younger than [statsCacheTTL].
// The mutex is held across a recount on purpose: concurrent pollers wait for
// one set of queries rather than racing their own.
func (s *Server) census(ctx context.Context, out *StatsResponse) error {
	// Sampled before the queries run, never after. A write that commits while
	// the census is being counted then lands on a generation *newer* than the
	// one cached, so the next request recounts. Sampling afterwards would file
	// half-fresh numbers under the new generation and keep them.
	eventsGen, projGen, err := s.censusGen()
	if err != nil {
		return err
	}

	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	now := s.deps.Now()
	if s.stats.valid && s.stats.eventsGen == eventsGen && s.stats.projGen == projGen &&
		now.Sub(s.stats.at) < statsCacheTTL {
		out.Events, out.Streams, out.Projections, out.Boards =
			s.stats.events, s.stats.streams, s.stats.projections, s.stats.boards
		return nil
	}

	var fresh StatsResponse
	if s.deps.Events != nil {
		total, err := s.deps.Events.CountEvents(ctx, 0)
		if err != nil {
			return err
		}
		players, banned, err := s.deps.Events.CountPlayers(ctx)
		if err != nil {
			return err
		}
		census, err := s.deps.Events.StreamCensus(ctx)
		if err != nil {
			return err
		}
		fresh.Events = EventStats{Total: total, Players: players, Banned: banned}
		fresh.Streams = StreamStats{
			Total: census.Total, Gapped: census.Gapped, GappedPlayers: census.GappedPlayers,
		}
	}
	if p := s.projections.Projector; p != nil {
		var counts map[string]int64
		err := p.Live().With(func(proj *store.Projections) error {
			var err error
			if fresh.Projections, err = proj.Counts(ctx); err != nil {
				return err
			}
			counts, err = proj.StatCounts(ctx)
			return err
		})
		if err != nil {
			return err
		}
		// minPlayers 1: the owner's view is every board that exists, including
		// the ones `GET /v1/leaderboards` is still holding back because only one
		// player is on them. Publication is a display rule; this endpoint is not
		// a display.
		for _, b := range stats.Catalog(counts, 1) {
			fresh.Boards = append(fresh.Boards, BoardCount{Stat: b.Stat, Count: counts[b.Stat]})
		}
	}

	s.stats.events, s.stats.streams, s.stats.projections, s.stats.boards =
		fresh.Events, fresh.Streams, fresh.Projections, fresh.Boards
	s.stats.eventsGen, s.stats.projGen = eventsGen, projGen
	s.stats.at, s.stats.valid = now, true
	out.Events, out.Streams, out.Projections, out.Boards =
		fresh.Events, fresh.Streams, fresh.Projections, fresh.Boards
	return nil
}

// censusGen reads the write generation of each database the census counts.
// Together they are the cache key: nothing in the census can change without one
// of the two committing a transaction.
func (s *Server) censusGen() (eventsGen, projGen int64, err error) {
	if s.deps.Events != nil {
		eventsGen = s.deps.Events.WriteGen()
	}
	if p := s.projections.Projector; p != nil {
		projGen = p.Live().WriteGen()
	}
	return eventsGen, projGen, err
}

func (s *Server) fail(w http.ResponseWriter, err error, what string) {
	s.deps.Log.Error("admin stats failed", "what", what, "err", err)
	fail(w, authz.CodeInternal, "could not "+what)
}
