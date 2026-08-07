package readapi

import (
	"context"
	"encoding/json"

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

// statCounts is `player_stat` grouped by stat: one row per (player, stat), so
// the count is the number of players on each board.
//
// One query answers "how big is every board", which is what the board index,
// a profile's `players` denominator and a comparison all need. Asking per board
// instead would be one indexed count per row rendered.
func (s *Server) statCounts(ctx context.Context) (map[string]int64, error) {
	var counts map[string]int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		counts, err = p.StatCounts(ctx)
		return err
	})
	return counts, err
}

// BoardList assembles `GET /v1/leaderboards` (§4.8).
func (s *Server) BoardList(ctx context.Context) (BoardsResponse, error) {
	counts, err := s.statCounts(ctx)
	if err != nil {
		return BoardsResponse{}, err
	}

	// The board list is assembled from the data, not from a table in the source.
	// Every board with a compile-time key is listed whether or not anyone is on
	// it — an empty board is still a board, and a UI that discovers boards here
	// must not lose one because nobody has scored yet. The two families whose
	// keys come out of the event stream (`fastest_to_<body>`, `rud_<cause>`) are
	// listed once [Deps.MinBoardPlayers] distinct players are on them; see
	// stats.Catalog for why that is the whole of the mitigation.
	all := stats.Catalog(counts, s.minBoardPlayers)
	out := BoardsResponse{
		Boards:     make([]BoardSummary, 0, len(all)),
		MinPlayers: s.minBoardPlayers,
	}
	for _, b := range all {
		out.Boards = append(out.Boards, BoardSummary{
			Stat: b.Stat, Title: b.Title, Unit: b.Unit,
			Ascending: b.Ascending, Count: counts[b.Stat],
			Periods: stats.Periods(),
		})
	}
	return out, nil
}

// Board assembles one page of `GET /v1/leaderboards/{stat}` (§4.8).
//
// limit and offset are clamped by [ClampPaging]; ok is false when stat is not a
// board this server serves — a key that describes nothing, or a family key
// nobody has ever scored on. A family board that exists but is not yet *listed*
// is still served: the alternative is a profile row that links to a 404, and a
// player's own achievement being hidden from them until somebody else repeats it.
func (s *Server) Board(ctx context.Context, stat, period, bucket string, limit, offset int) (BoardResponse, bool, error) {
	board, ok, err := s.board(ctx, stat)
	if err != nil || !ok {
		return BoardResponse{}, false, err
	}
	limit, offset = ClampPaging(limit, offset)

	// An unnamed window means "the one we are in", read from the server clock —
	// never from anything a client sent, which is the same rule recv_time
	// follows and for the same reason.
	if period != stats.PeriodAllTime && bucket == "" {
		bucket, _ = stats.CurrentBucket(period, s.deps.Now().UnixMilli())
	}

	rows, handles, err := s.visibleRows(ctx, stat, period, bucket, board.Ascending, limit, offset)
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

	rewound, err := s.rewound(ctx, board, rows)
	if err != nil {
		return BoardResponse{}, true, err
	}

	out := BoardResponse{
		Stat: board.Stat, Title: board.Title, Unit: board.Unit,
		Ascending: board.Ascending,
		Period:    period, Bucket: bucket,
		Limit: limit, Offset: offset,
		Rows: make([]BoardRow, 0, len(rows)),
	}
	for i, row := range rows {
		out.Rows = append(out.Rows, BoardRow{
			Rank:   offset + i + 1,
			Handle: handles[i],
			Value:  row.Value,
			// A career-time row's context carries the §4.1 career key, which is
			// derived from the mod's install id and would link one person's two
			// handles. [Redact] relabels it per player; see privacy.go. Every
			// other context blob passes through as the bytes the fold wrote.
			Context: Redact(row.PlayerID, row.Context),
			Updated: times[row.UpdatedSeq],
			Rewound: rewound[rowKey(row)],
		})
	}
	return out, true, nil
}

// board resolves a stat key to the board it names, reporting false for a key
// this server has no board for.
//
// A fixed key needs no query at all. A family key costs one indexed count, and
// only then — the price of "this board exists because the data made it" is one
// `SELECT count(*)` on the URLs that name a body.
func (s *Server) board(ctx context.Context, stat string) (stats.Board, bool, error) {
	if b, ok := stats.Known(stat, 0); ok {
		return b, true, nil
	}
	if _, describable := stats.Describe(stat); !describable {
		return stats.Board{}, false, nil
	}
	var players int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		players, err = p.StatPlayers(ctx, stat)
		return err
	})
	if err != nil {
		return stats.Board{}, false, err
	}
	b, ok := stats.Known(stat, players)
	return b, ok, nil
}

// rowContext is the sliver of a board row's context this package reads. The
// column is otherwise passed through verbatim — the fold decides what goes in it
// and the reader renders it — but `career` is the join key for the rewind mark.
type rowContext struct {
	Career string `json:"career"`
}

// careerOf extracts the career a career-time row was set in, or "" when the row
// has none (a non-career board, or a row written before the key existed).
func careerOf(row store.StatRow) string {
	if len(row.Context) == 0 {
		return ""
	}
	var c rowContext
	if err := json.Unmarshal(row.Context, &c); err != nil {
		return ""
	}
	return c.Career
}

func rowKey(row store.StatRow) careerKey { return careerKey{row.PlayerID, careerOf(row)} }

type careerKey struct {
	player int64
	career string
}

// rewound resolves the §4.1 rewind mark for a page of career-time rows: one
// query per distinct player on the page, and none at all for a board whose
// values are not career times.
func (s *Server) rewound(ctx context.Context, board stats.Board, rows []store.StatRow) (map[careerKey]bool, error) {
	if !board.Career || len(rows) == 0 {
		return nil, nil
	}
	byPlayer := map[int64][]string{}
	for _, row := range rows {
		if c := careerOf(row); c != "" {
			byPlayer[row.PlayerID] = append(byPlayer[row.PlayerID], c)
		}
	}
	out := map[careerKey]bool{}
	for player, careers := range byPlayer {
		var marked map[string]bool
		err := s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			marked, err = p.RewoundCareers(ctx, player, careers)
			return err
		})
		if err != nil {
			return nil, err
		}
		for career := range marked {
			out[careerKey{player, career}] = true
		}
	}
	return out, nil
}

// Player assembles `GET /v1/players/{handle}` (§4.8).
//
// ok is false for an unknown, retired or banned handle — one answer for all
// three on purpose, so the surface is not a ban oracle (§4.8).
func (s *Server) Player(ctx context.Context, handle string) (PlayerResponse, bool, error) {
	counts, err := s.statCounts(ctx)
	if err != nil {
		return PlayerResponse{}, false, err
	}
	return s.player(ctx, handle, counts)
}

// player is [Server.Player] with the board census supplied, so a comparison of
// several handles pays for it once rather than once per handle.
func (s *Server) player(ctx context.Context, handle string, counts map[string]int64) (PlayerResponse, bool, error) {
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

	careers := make([]string, 0, len(rows))
	for _, row := range rows {
		if board, known := stats.Describe(row.Stat); known && board.Career {
			if c := careerOf(row); c != "" {
				careers = append(careers, c)
			}
		}
	}
	var marked map[string]bool
	if len(careers) > 0 {
		err = s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			marked, err = p.RewoundCareers(ctx, entry.PlayerID, careers)
			return err
		})
		if err != nil {
			return PlayerResponse{}, true, err
		}
	}

	banned := s.deps.Directory.BannedIDs()
	out := PlayerResponse{Handle: entry.Handle, Since: entry.Since, Stats: make([]PlayerRow, 0, len(rows))}
	for _, row := range rows {
		// Every row the player holds is shown, listed board or not: a profile is
		// what this player did, not which leaderboards are big enough to appear
		// in the index. The row's own existence is what makes its board servable
		// (see [Server.board]), so the link is never dead.
		board, known := stats.Describe(row.Stat)
		if !known {
			// A stat this build cannot name — a board removed between releases,
			// still sitting in projections.db until the next rebuild.
			continue
		}
		rank, err := s.rank(ctx, row, board.Ascending, banned)
		if err != nil {
			return PlayerResponse{}, true, err
		}
		out.Stats = append(out.Stats, PlayerRow{
			Stat: row.Stat, Title: board.Title, Unit: board.Unit,
			Ascending: board.Ascending,
			Value:     row.Value, Rank: rank, Players: counts[row.Stat],
			// Relabelled per player, for the same reason as a board row's; see
			// privacy.go. The rewind mark above is resolved from the real career
			// key first, so the qualification survives the redaction.
			Context: Redact(entry.PlayerID, row.Context),
			Updated: times[row.UpdatedSeq],
			Rewound: board.Career && marked[careerOf(row)],
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
