package readapi

import (
	"context"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The functions here assemble the §4.8 response bodies without touching HTTP.
//
// They exist because the server-rendered pages (§5.7) show the same numbers the
// JSON API publishes, and the interesting part of that assembly is not the JSON
// encoding — it is `visibleRows`' over-fetch-and-drop pass over the directory
// (§5.4) and the rank arithmetic that keeps a profile consistent with the board
// page it appears on. A second implementation of either in package `web` would
// be a second place for a banned player to leak onto a public surface, so the
// handlers in readapi.go and the templates in web both call these.

// BoardList assembles `GET /v1/leaderboards` (§4.8).
func (s *Server) BoardList(ctx context.Context) (BoardsResponse, error) {
	var counts map[string]int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		counts, err = p.StatCounts(ctx)
		return err
	})
	if err != nil {
		return BoardsResponse{}, err
	}

	// Every board is listed whether or not anyone is on it: the set of boards
	// is a property of the build, not of the data, and a UI that discovers
	// boards from this endpoint must not lose one because nobody has scored yet.
	all := stats.Boards()
	out := BoardsResponse{Boards: make([]BoardSummary, 0, len(all))}
	for _, b := range all {
		out.Boards = append(out.Boards, BoardSummary{
			Stat: b.Stat, Title: b.Title, Unit: b.Unit, Count: counts[b.Stat],
		})
	}
	return out, nil
}

// Board assembles one page of `GET /v1/leaderboards/{stat}` (§4.8).
//
// limit and offset are clamped by [ClampPaging]; ok is false when stat is not a
// board this build publishes.
func (s *Server) Board(ctx context.Context, stat string, limit, offset int) (BoardResponse, bool, error) {
	board, ok := stats.BoardFor(stat)
	if !ok {
		return BoardResponse{}, false, nil
	}
	limit, offset = ClampPaging(limit, offset)

	rows, handles, err := s.visibleRows(ctx, stat, limit, offset)
	if err != nil {
		return BoardResponse{}, true, err
	}

	seqs := make([]int64, 0, len(rows))
	for _, row := range rows {
		seqs = append(seqs, row.UpdatedSeq)
	}
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return BoardResponse{}, true, err
	}

	out := BoardResponse{
		Stat: board.Stat, Title: board.Title, Unit: board.Unit,
		Limit: limit, Offset: offset,
		Rows: make([]BoardRow, 0, len(rows)),
	}
	for i, row := range rows {
		out.Rows = append(out.Rows, BoardRow{
			Rank:    offset + i + 1,
			Handle:  handles[i],
			Value:   row.Value,
			Context: row.Context,
			Updated: times[row.UpdatedSeq],
		})
	}
	return out, true, nil
}

// Player assembles `GET /v1/players/{handle}` (§4.8).
//
// ok is false for an unknown, retired or banned handle — one answer for all
// three on purpose, so the surface is not a ban oracle (§4.8).
func (s *Server) Player(ctx context.Context, handle string) (PlayerResponse, bool, error) {
	entry, ok := s.deps.Directory.Lookup(handle)
	if !ok {
		return PlayerResponse{}, false, nil
	}

	var rows []store.StatRow
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		rows, err = p.PlayerStats(ctx, entry.PlayerID)
		return err
	})
	if err != nil {
		return PlayerResponse{}, true, err
	}

	seqs := make([]int64, 0, len(rows))
	for _, row := range rows {
		seqs = append(seqs, row.UpdatedSeq)
	}
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return PlayerResponse{}, true, err
	}

	banned := s.deps.Directory.BannedIDs()
	out := PlayerResponse{Handle: entry.Handle, Since: entry.Since, Stats: make([]PlayerRow, 0, len(rows))}
	for _, row := range rows {
		board, known := stats.BoardFor(row.Stat)
		if !known {
			// A stat this build no longer publishes — a board removed between
			// releases, still sitting in projections.db until the next rebuild.
			continue
		}
		rank, err := s.rank(ctx, row, banned)
		if err != nil {
			return PlayerResponse{}, true, err
		}
		out.Stats = append(out.Stats, PlayerRow{
			Stat: row.Stat, Title: board.Title, Unit: board.Unit,
			Value: row.Value, Rank: rank, Context: row.Context,
			Updated: times[row.UpdatedSeq],
		})
	}
	return out, true, nil
}

// ClampPaging applies §4.8's bounds. A too-large limit is clamped rather than
// rejected: this is a CDN-cached public endpoint, and clamping keeps one answer
// per (stat, limit, offset) instead of splitting the cache between a 400 and a
// 200.
func ClampPaging(limit, offset int) (int, int) {
	return min(max(limit, 1), MaxLimit), max(offset, 0)
}
