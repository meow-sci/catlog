package readapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// `GET /v1/stats` — the stats of the stats.
//
// Every other endpoint in this package answers a question about a player. This
// one answers questions about the collection itself: how many events there are,
// of what kinds, arriving how fast, since when, and how much has been derived
// from them. It is the only public surface that describes catlog rather than
// its players, and the numbers in it are deliberately *not* game achievements —
// no records, no rankings, nobody's handle.
//
// # Why this is affordable now and was not before
//
// The front page's tiles carry a comment saying the honest summary stops at
// what `GET /v1/leaderboards` already computes, "until somebody decides a
// public `/v1/stats` is worth its unbounded half". This is that decision, and
// the unbounded half is gone: the event census is a projection
// (migrations/projections/0004_census.sql), so "how many events, by type, by
// window" is a handful of indexed reads rather than a scan of the largest table
// catlog has.
//
// What is left is about twenty small queries, which is still more than a public
// page should pay per request — so the whole answer is memoised the same way the
// board census is, on [Projections.WriteGen] plus a short TTL. See
// [Server.Stats].

// statsTTL is how long one assembled answer may be served before it is
// rebuilt. Same reasoning as statCountsTTL: under the CDN's own s-maxage=30 it
// is invisible from outside, and it bounds what an origin-hitting burst costs.
const statsTTL = 10 * time.Second

// DailySeriesDays is how many daily buckets the response carries as a series.
//
// Ninety, which is the daily retention `player_stat_period` keeps and therefore
// the horizon the rest of catlog already thinks in — though the census itself
// keeps every bucket forever (it is types × buckets, not players × boards ×
// buckets), so a longer series is a parameter away if a chart ever wants one.
const DailySeriesDays = 90

// StatsResponse is `GET /v1/stats`.
type StatsResponse struct {
	// Generated is the server clock when this answer was assembled, unix ms. It
	// is here because the response is cached twice over — once in this process,
	// once in a shared cache — and a page showing "events today" should be able
	// to say how old "today" is.
	Generated int64 `json:"generated"`
	// Events describes the log.
	Events EventStats `json:"events"`
	// Collection describes what has been derived from it.
	Collection CollectionStats `json:"collection"`
}

// TypeCount is one event type's contribution.
type TypeCount struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
	// Share is Count as a fraction of the enclosing total, 0–1. Published rather
	// than left to the client because every client wants it and none should have
	// to decide what to do when the denominator is zero.
	Share float64 `json:"share"`
	// First and Last are the receive times of the oldest and newest event of
	// this type in this window, unix ms. Absent in a per-window breakdown where
	// they would only repeat the window's own bounds.
	First int64 `json:"first,omitempty"`
	Last  int64 `json:"last,omitempty"`
}

// WindowCount is one bucket of one period, with no breakdown.
type WindowCount struct {
	Period string `json:"period"`
	// Bucket is the window's key — `2026-08-07`, `2026-W32`, `2026-08`, `2026`.
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}

// WindowStats is one rolling window, broken down by type.
type WindowStats struct {
	Period string `json:"period"`
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
	// Types is every type seen in the window, most-counted first.
	Types []TypeCount `json:"types"`
}

// EventStats is the log, counted.
type EventStats struct {
	// Total is every event the projector has folded.
	//
	// It counts what the *census* has seen, which is every event past the
	// checkpoint minus the ones this build could not decode (§4.1). That is
	// almost always the whole log; [CollectionStats.LogHead] is the other number
	// and the two together are the honest answer.
	Total int64 `json:"total"`
	// Types is every type, all time, most-counted first.
	Types []TypeCount `json:"types"`
	// Windows is today, this week, this month and this year — each the window
	// the server clock is currently in, each broken down by type.
	Windows []WindowStats `json:"windows"`
	// First and Last are the receive times of the oldest and newest event, unix
	// ms. First is when catlog started watching.
	First int64 `json:"first,omitempty"`
	Last  int64 `json:"last,omitempty"`
	// Days is how many distinct days carry at least one event, and PerDay is
	// Total over Days.
	//
	// Days rather than "days since the first event", because catlog being switched
	// off for a fortnight is not the same fact as catlog being quiet for one, and
	// only the first is something an average should be diluted by.
	Days   int64   `json:"days"`
	PerDay float64 `json:"per_day"`
	// Busiest is the fullest single day, ever. Absent before there is one.
	Busiest *WindowCount `json:"busiest,omitempty"`
	// Daily is the newest [DailySeriesDays] daily totals, **oldest first** — the
	// order a chart plots left to right.
	Daily []WindowCount `json:"daily"`
}

// CollectionStats is what catlog has derived from the log, and how far it has
// got.
type CollectionStats struct {
	// Boards is how many leaderboards are published, and Placements the sum of
	// their populations — the same two figures the front page shows.
	Boards     int   `json:"boards"`
	Placements int64 `json:"placements"`
	// Types is how many distinct event types have ever arrived. Not a constant:
	// catlog stores an event of a type it cannot fold (§4.1), so this counts what
	// the mod actually sent rather than what this build knows about.
	Types int `json:"types"`
	// Handles is how many handles are claimed and visible — retired and banned
	// ones are not in the directory and are not counted here.
	Handles int `json:"handles"`
	// ScoringPlayers is how many of them hold a value on any board.
	ScoringPlayers int64 `json:"scoring_players"`
	// Flights, and how many of them carry a §4.2 flag bit.
	Flights        int64 `json:"flights"`
	FlaggedFlights int64 `json:"flagged_flights"`
	// Careers, and how many have had an earlier save loaded (§4.1).
	Careers        int64 `json:"careers"`
	RewoundCareers int64 `json:"rewound_careers"`
	// Kittens is every kitten anybody has ever flown.
	Kittens int64 `json:"kittens"`
	// Systems and SystemBodies count the surveyed celestial-system headers and
	// immutable catalogue rows catlog has projected.
	Systems      int64 `json:"systems"`
	SystemBodies int64 `json:"system_bodies"`
	// Bodies is how many distinct celestial bodies anybody has reached.
	//
	// The one number here catlog could not have known in advance: bodies are
	// opaque strings on the wire (§4.2) and the server keeps no list of them, so
	// this counts the ones players went to.
	Bodies int64 `json:"bodies"`
	// FeedRows is how many lines the activity feed is holding — capped, so this
	// is a fact about the cap once catlog is busy.
	FeedRows int64 `json:"feed_rows"`
	// LogHead is the newest seq in events.db, Projected is the projector's
	// checkpoint, and Lag is the gap.
	//
	// Published because it is the one thing that explains a number on this page
	// disagreeing with a number on another: everything else here is a projection,
	// and a projection is only as current as its cursor.
	LogHead   int64 `json:"log_head"`
	Projected int64 `json:"projected"`
	Lag       int64 `json:"lag"`
}

// statsCache memoises [Server.Stats]; see [Server.statCounts] for the same
// arrangement over the board census, and why the key is the write generation
// rather than the head of the log.
type statsCache struct {
	mu   sync.Mutex
	at   time.Time
	gen  int64
	body *StatsResponse
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	out, err := s.Stats(r.Context())
	if err != nil {
		s.fail(w, r, err, "read the collection stats")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// Stats assembles `GET /v1/stats`.
//
// The result is shared and must be treated as read-only, exactly like
// [Server.statCounts]' map: the cache hands the same pointer's contents to every
// concurrent caller rather than deep-copying twenty slices per request.
func (s *Server) Stats(ctx context.Context) (StatsResponse, error) {
	gen := s.deps.Projections.WriteGen()

	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	now := s.deps.Now()
	if s.stats.body != nil && s.stats.gen == gen && now.Sub(s.stats.at) < statsTTL {
		return *s.stats.body, nil
	}

	out, err := s.buildStats(ctx, now)
	if err != nil {
		return StatsResponse{}, err
	}
	s.stats.body, s.stats.gen, s.stats.at = &out, gen, now
	return out, nil
}

func (s *Server) buildStats(ctx context.Context, now time.Time) (StatsResponse, error) {
	out := StatsResponse{Generated: now.UnixMilli()}

	// One projections read for everything the census and the census-adjacent
	// counts need: `With` takes the rebuild swap's read lock, and taking it once
	// means the whole page describes one view of the database rather than a
	// handful taken either side of a swap.
	var (
		allTime  []store.CensusRow
		windows  [][]store.CensusRow
		daily    []store.CensusRow
		busiest  store.CensusRow
		haveBusy bool
		days     int64
		counts   store.ProjectionCounts
		cursor   int64
	)
	rolling := stats.RollingPeriods()
	buckets := make([]string, len(rolling))
	for i, period := range rolling {
		buckets[i], _ = stats.CurrentBucket(period, now.UnixMilli())
	}

	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		if allTime, err = p.CensusWindow(ctx, stats.PeriodAllTime, ""); err != nil {
			return err
		}
		windows = make([][]store.CensusRow, len(rolling))
		for i, period := range rolling {
			if windows[i], err = p.CensusWindow(ctx, period, buckets[i]); err != nil {
				return err
			}
		}
		if daily, err = p.CensusSeries(ctx, stats.PeriodDaily, stats.CensusAllTypes, DailySeriesDays); err != nil {
			return err
		}
		if busiest, haveBusy, err = p.CensusBusiest(ctx, stats.PeriodDaily, stats.CensusAllTypes); err != nil {
			return err
		}
		if days, err = p.CensusBuckets(ctx, stats.PeriodDaily, stats.CensusAllTypes); err != nil {
			return err
		}
		if counts, err = p.Counts(ctx); err != nil {
			return err
		}
		cursor, err = p.Checkpoint(ctx, nil, store.AllProjections)
		return err
	})
	if err != nil {
		return StatsResponse{}, err
	}

	head, err := s.deps.Events.MaxSeq(ctx)
	if err != nil {
		return StatsResponse{}, err
	}
	boards, err := s.BoardList(ctx)
	if err != nil {
		return StatsResponse{}, err
	}

	total, first, last := totalRow(allTime)
	out.Events = EventStats{
		Total:   total,
		Types:   typeCounts(allTime, total, true),
		Windows: make([]WindowStats, 0, len(rolling)),
		First:   first,
		Last:    last,
		Days:    days,
		Daily:   windowCounts(daily),
	}
	if days > 0 {
		out.Events.PerDay = float64(total) / float64(days)
	}
	if haveBusy {
		out.Events.Busiest = &WindowCount{Period: busiest.Period, Bucket: busiest.Bucket, Count: busiest.Count}
	}
	for i, period := range rolling {
		n, _, _ := totalRow(windows[i])
		out.Events.Windows = append(out.Events.Windows, WindowStats{
			Period: period, Bucket: buckets[i], Count: n,
			Types: typeCounts(windows[i], n, false),
		})
	}

	out.Collection = CollectionStats{
		Boards:         len(boards.Boards),
		Types:          len(out.Events.Types),
		Handles:        s.deps.Directory.Len(),
		ScoringPlayers: counts.ScoringPlayers,
		Flights:        counts.FlightState,
		FlaggedFlights: counts.FlaggedFlights,
		Careers:        counts.Career,
		RewoundCareers: counts.RewoundCareers,
		Kittens:        counts.Kitten,
		Systems:        counts.System,
		SystemBodies:   counts.SystemBody,
		Bodies:         counts.Bodies,
		FeedRows:       counts.Feed,
		LogHead:        head,
		Projected:      cursor,
		Lag:            max(head-cursor, 0),
	}
	for _, b := range boards.Boards {
		out.Collection.Placements += b.Count
	}
	return out, nil
}

// totalRow pulls the [stats.CensusAllTypes] row out of a window.
//
// The total is a stored row rather than a sum of the others, so this reads it
// instead of adding — which is what keeps it right for a type this build cannot
// name, and what makes the bounds (`first`, `last`) the window's own rather than
// the widest type's.
func totalRow(rows []store.CensusRow) (total, first, last int64) {
	for _, r := range rows {
		if r.Type == stats.CensusAllTypes {
			return r.Count, r.FirstAt, r.LastAt
		}
	}
	return 0, 0, 0
}

// typeCounts turns a census window into the per-type breakdown, dropping the
// total row. `bounds` carries the per-type first/last stamps, which are worth
// having all-time and merely repeat the window elsewhere.
func typeCounts(rows []store.CensusRow, total int64, bounds bool) []TypeCount {
	out := make([]TypeCount, 0, len(rows))
	for _, r := range rows {
		if r.Type == stats.CensusAllTypes {
			continue
		}
		tc := TypeCount{Type: r.Type, Count: r.Count}
		if total > 0 {
			tc.Share = float64(r.Count) / float64(total)
		}
		if bounds {
			tc.First, tc.Last = r.FirstAt, r.LastAt
		}
		out = append(out, tc)
	}
	return out
}

func windowCounts(rows []store.CensusRow) []WindowCount {
	out := make([]WindowCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, WindowCount{Period: r.Period, Bucket: r.Bucket, Count: r.Count})
	}
	return out
}
