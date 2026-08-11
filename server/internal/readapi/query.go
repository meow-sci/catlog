package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

// statCountsTTL is how long one board census may be served before it is
// recounted. Anything under the CDN's own s-maxage=30 is invisible from
// outside; 10 s just bounds how often an origin-hitting burst pays the
// group-by.
const statCountsTTL = 10 * time.Second

// countsCache memoizes [store.Projections.StatCounts] — see statCounts.
type countsCache struct {
	mu     sync.Mutex
	at     time.Time
	gen    int64
	counts map[string]int64
}

// statCounts is `player_stat` grouped by stat: one row per (player, stat), so
// the count is the number of players on each board.
//
// One query answers "how big is every board", which is what the board index,
// a profile's `players` denominator and a comparison all need. Asking per board
// instead would be one indexed count per row rendered.
//
// The answer is cached for [statCountsTTL], and only while nothing has been
// written to projections.db: the fold is the only thing that changes this
// census, so an idle server serves it for free and a busy one pays the group-by
// at most once per TTL. The mutex is held across the recount on purpose —
// concurrent misses wait for one query instead of racing their own.
//
// The key is [store.DB.WriteGen] on projections.db, not the head of the event
// log. Those differ in the window that matters: ingest stops, the head stops
// moving, and the fold is still running. Keyed on the head, a read landing in
// that window cached a half-folded census and served it for the whole TTL —
// which is `GET /v1/leaderboards` under-reporting its boards for ten seconds
// right after a load run, exactly when something is looking.
//
// Callers treat the returned map as read-only; it is shared.
func (s *Server) statCounts(ctx context.Context) (map[string]int64, error) {
	gen := s.deps.Projections.WriteGen()

	s.counts.mu.Lock()
	defer s.counts.mu.Unlock()
	now := s.deps.Now()
	if s.counts.counts != nil && s.counts.gen == gen && now.Sub(s.counts.at) < statCountsTTL {
		return s.counts.counts, nil
	}

	var counts map[string]int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		counts, err = p.StatCounts(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.counts.counts, s.counts.gen, s.counts.at = counts, gen, now
	return counts, nil
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
	// must not lose one because nobody has scored yet. Families whose keys come
	// out of the event stream are listed once [Deps.MinBoardPlayers] distinct
	// players are on them; see stats.Catalog for why that is the whole of the
	// mitigation.
	all := stats.Catalog(counts, s.minBoardPlayers)
	out := BoardsResponse{
		Boards:     make([]BoardSummary, 0, len(all)),
		MinPlayers: s.minBoardPlayers,
	}
	for _, b := range all {
		out.Boards = append(out.Boards, BoardSummary{
			Stat: b.Stat, Title: b.Title, Unit: b.Unit,
			Ascending: b.Ascending, Count: counts[b.Stat],
			Periods: stats.Periods(), Scopes: stats.Scopes(),
			BodyDerived: b.BodyDerived,
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
func (s *Server) Board(
	ctx context.Context, stat, period, bucket, scope, system string, limit, offset int,
) (BoardResponse, bool, error) {
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

	var rows []boardPageRow
	var handles []string
	switch scope {
	case stats.ScopePlayer:
		playerRows, visibleHandles, err := s.visiblePlayerRows(
			ctx, stat, period, bucket, board.Ascending, limit, offset)
		if err != nil {
			return BoardResponse{}, true, err
		}
		handles = visibleHandles
		rows = make([]boardPageRow, 0, len(playerRows))
		for _, row := range playerRows {
			career := ""
			if board.Career {
				career = careerOf(row)
			}
			rows = append(rows, boardPageRow{
				PlayerID: row.PlayerID, Career: career, Value: row.Value,
				Context: row.Context, UpdatedSeq: row.UpdatedSeq,
			})
		}
	case stats.ScopeCareer:
		careerRows, visibleHandles, err := s.visibleCareerRows(
			ctx, stat, system, board.Ascending, limit, offset)
		if err != nil {
			return BoardResponse{}, true, err
		}
		handles = visibleHandles
		rows = make([]boardPageRow, 0, len(careerRows))
		for _, row := range careerRows {
			rows = append(rows, boardPageRow{
				PlayerID: row.PlayerID, Career: row.Career, System: row.System,
				Save: row.Ordinal, Value: row.Value, Context: row.Context,
				UpdatedSeq: row.UpdatedSeq,
			})
		}
	case stats.ScopeSystem:
		systemRows, visibleHandles, err := s.visibleSystemRows(
			ctx, stat, system, board.Ascending, limit, offset)
		if err != nil {
			return BoardResponse{}, true, err
		}
		handles = visibleHandles
		rows = make([]boardPageRow, 0, len(systemRows))
		for _, row := range systemRows {
			rows = append(rows, boardPageRow{
				PlayerID: row.PlayerID, System: row.System, Value: row.Value,
				Context: row.Context, UpdatedSeq: row.UpdatedSeq,
			})
		}
	default:
		return BoardResponse{}, true, fmt.Errorf("readapi: unsupported board scope %q", scope)
	}

	seqs := make([]int64, 0, len(rows))
	for _, row := range rows {
		seqs = append(seqs, row.UpdatedSeq)
	}
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return BoardResponse{}, true, err
	}

	careerKeys := make([]careerKey, 0, len(rows))
	systemHashes := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Career != "" {
			careerKeys = append(careerKeys, careerKey{row.PlayerID, row.Career})
		}
		if row.System != "" {
			systemHashes = append(systemHashes, row.System)
		}
	}
	rewound, err := s.rewoundCareers(ctx, careerKeys)
	if err != nil {
		return BoardResponse{}, true, err
	}
	systems, err := s.systemRefs(ctx, systemHashes)
	if err != nil {
		return BoardResponse{}, true, err
	}

	out := BoardResponse{
		Stat: board.Stat, Title: board.Title, Unit: board.Unit,
		Ascending: board.Ascending,
		Scope:     scope, Period: period, Bucket: bucket,
		Limit: limit, Offset: offset,
		Rows: make([]BoardRow, 0, len(rows)),
	}
	for i, row := range rows {
		var systemRef *SystemRef
		if row.System != "" {
			ref, exists := systems[row.System]
			if !exists {
				return BoardResponse{}, true, fmt.Errorf("readapi: no metadata for system %q", row.System)
			}
			systemRef = &ref
		}
		out.Rows = append(out.Rows, BoardRow{
			Rank:   offset + i + 1,
			Handle: handles[i],
			Save:   row.Save,
			SaveID: Label(row.PlayerID, kindCareer, row.Career),
			System: systemRef,
			Value:  row.Value,
			// A career-time row's context carries the §4.1 career key, which is
			// derived from the mod's install id and would link one person's two
			// handles. [Redact] relabels it per player; see privacy.go. Every
			// other context blob passes through as the bytes the fold wrote.
			Context: Redact(row.PlayerID, row.Context),
			Updated: times[row.UpdatedSeq],
			Rewound: rewound[careerKey{row.PlayerID, row.Career}],
		})
	}
	return out, true, nil
}

type boardPageRow struct {
	PlayerID   int64
	Career     string
	System     string
	Save       int64
	Value      float64
	Context    json.RawMessage
	UpdatedSeq int64
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
// and the reader renders it — but `career` is the join key for the rewind mark
// and the winning save's friendly system identity.
type rowContext struct {
	Career string `json:"career"`
}

// careerOf extracts the career a row was set in, or "" when its winning context
// has none. It never guesses from the player's other saves.
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

type careerKey struct {
	player int64
	career string
}

// rewoundCareers resolves the §4.1 rewind mark for the exact save represented
// by each row. Career-scope boards always supply their own career, even when the
// board's value is not a career-relative time. Player scope supplies a career
// only for the career-time boards whose context carries one.
func (s *Server) rewoundCareers(ctx context.Context, keys []careerKey) (map[careerKey]bool, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	byPlayer := map[int64][]string{}
	seen := map[careerKey]bool{}
	for _, key := range keys {
		if key.career != "" && !seen[key] {
			byPlayer[key.player] = append(byPlayer[key.player], key.career)
			seen[key] = true
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

// systemBySlug resolves the URL-facing slug or an already-canonical hash.
func (s *Server) systemBySlug(ctx context.Context, key string) (string, bool, error) {
	ref, ok, err := s.ResolveSystem(ctx, key)
	return ref.Hash, ok, err
}

// ResolveSystem returns the compact public identity for a slug or hash. It is
// the narrow non-HTTP seam used by page handlers that must pass the canonical
// hash back into a scoped board read without querying store directly.
func (s *Server) ResolveSystem(ctx context.Context, key string) (SystemRef, bool, error) {
	var row store.SystemRow
	var ok bool
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		row, ok, err = p.SystemBySlugOrHash(ctx, key)
		return err
	})
	if err != nil || !ok {
		return SystemRef{}, ok, err
	}
	return SystemRef{Hash: row.Hash, Name: row.Name, Slug: row.Slug}, true, nil
}

// systemRefs resolves every distinct system carried by one page in one query.
func (s *Server) systemRefs(ctx context.Context, hashes []string) (map[string]SystemRef, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	wanted := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		wanted[hash] = true
	}
	var rows []store.SystemRow
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		rows, err = p.Systems(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]SystemRef, len(wanted))
	for _, row := range rows {
		if wanted[row.Hash] {
			out[row.Hash] = SystemRef{Hash: row.Hash, Name: row.Name, Slug: row.Slug}
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
	out, known, err := s.player(ctx, handle, counts)
	if err != nil || !known {
		return out, known, err
	}
	if err := s.attachPlayerSystems(ctx, []*PlayerResponse{&out}); err != nil {
		return PlayerResponse{}, true, err
	}
	return out, true, nil
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

	// Both rank directions in one pass each: two statements per profile, however
	// many boards the player is on, instead of one [store.Projections.StatAhead]
	// per row — which a comparison then multiplied by its handle count.
	ahead, err := s.aheadCounts(ctx, entry.PlayerID, rows)
	if err != nil {
		return PlayerResponse{}, true, err
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
		rank, err := s.rank(ctx, row, board.Ascending, banned, ahead[row.Stat])
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
			Context:  Redact(entry.PlayerID, row.Context),
			Updated:  times[row.UpdatedSeq],
			Rewound:  board.Career && marked[careerOf(row)],
			playerID: entry.PlayerID,
			career:   careerOf(row),
		})
	}
	return out, true, nil
}

// attachPlayerSystems resolves the friendly system for every player row whose
// winning context carries a raw career. Career bindings are read once per
// player under one projections view, then all distinct system headers are read
// in one metadata batch. A missing career, an unbound career and an orphaned
// hash all remain the same honest optional omission.
func (s *Server) attachPlayerSystems(ctx context.Context, profiles []*PlayerResponse) error {
	needed := map[careerKey]bool{}
	byPlayer := map[int64]bool{}
	for _, profile := range profiles {
		for _, row := range profile.Stats {
			key := careerKey{player: row.playerID, career: row.career}
			if key.player != 0 && key.career != "" {
				needed[key] = true
				byPlayer[key.player] = true
			}
		}
	}
	if len(needed) == 0 {
		return nil
	}

	bindings := make(map[careerKey]string, len(needed))
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for playerID := range byPlayer {
			careers, err := p.PlayerCareers(ctx, playerID)
			if err != nil {
				return err
			}
			for _, career := range careers {
				key := careerKey{player: playerID, career: career.Career}
				if needed[key] && career.System != "" {
					bindings[key] = career.System
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	hashes := make([]string, 0, len(bindings))
	for _, hash := range bindings {
		hashes = append(hashes, hash)
	}
	refs, err := s.systemRefs(ctx, hashes)
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		for i := range profile.Stats {
			row := &profile.Stats[i]
			hash := bindings[careerKey{player: row.playerID, career: row.career}]
			if ref, ok := refs[hash]; ok {
				copy := ref
				row.System = &copy
			}
		}
	}
	return nil
}

// aheadCounts resolves the unfiltered half of every rank on a profile: for each
// of the player's rows on a board this build can name, how many rows outrank
// it. One [store.Projections.StatAheadForPlayer] statement per rank direction.
func (s *Server) aheadCounts(ctx context.Context, playerID int64, rows []store.StatRow) (map[string]int64, error) {
	var desc, asc []string
	for _, row := range rows {
		board, known := stats.Describe(row.Stat)
		if !known {
			continue
		}
		if board.Ascending {
			asc = append(asc, row.Stat)
		} else {
			desc = append(desc, row.Stat)
		}
	}

	out := map[string]int64{}
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for _, dir := range []struct {
			statKeys []string
			asc      bool
		}{{desc, false}, {asc, true}} {
			part, err := p.StatAheadForPlayer(ctx, playerID, dir.statKeys, dir.asc)
			if err != nil {
				return err
			}
			for stat, n := range part {
				out[stat] = n
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClampPaging applies §4.8's bounds. A too-large limit is clamped rather than
// rejected: this is a CDN-cached public endpoint, and clamping keeps one answer
// per (stat, limit, offset) instead of splitting the cache between a 400 and a
// 200.
func ClampPaging(limit, offset int) (int, int) {
	return min(max(limit, 1), MaxLimit), max(offset, 0)
}
