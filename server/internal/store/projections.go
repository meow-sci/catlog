package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AllProjections is the single checkpoint key every fold shares (§5.6).
const AllProjections = "all"

// Checkpoint reads a projection cursor. A missing row is seq 0, i.e. "start
// from the beginning" — which is also what a rebuild resets to (§5.6).
func (p *Projections) Checkpoint(ctx context.Context, q Querier, projection string) (int64, error) {
	if q == nil {
		q = p.Reader()
	}
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT last_seq FROM proj_checkpoint WHERE projection = ?`, projection).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read checkpoint %q: %w", projection, err)
	}
	return seq, nil
}

// SetCheckpoint advances a projection cursor. It belongs in the same
// transaction as the projection writes it accounts for, so that a crash between
// the two is impossible (§5.6).
func (p *Projections) SetCheckpoint(ctx context.Context, q Querier, projection string, lastSeq int64) error {
	if q == nil {
		q = p.Writer()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO proj_checkpoint (projection, last_seq, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (projection) DO UPDATE SET last_seq = excluded.last_seq, updated_at = excluded.updated_at`,
		projection, lastSeq, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("store: set checkpoint %q: %w", projection, err)
	}
	return nil
}

// nowMillis is the store's clock. A package-level function so tests can read
// the same value the writes use without threading a clock through every call.
func nowMillis() int64 { return time.Now().UnixMilli() }

// --- leaderboards ------------------------------------------------------------
//
// Everything below is the *read* side of projections.db: the queries the §4.8
// endpoints and the SSE feed run. The write side is fold rules and lives in
// package stats, where each statement sits next to the board rule it encodes.
//
// None of these queries can filter banned players: `banned_at` is a column of
// events.db and the two files cannot be joined (§5.4). The read API filters in
// Go against the in-memory handle directory, which is also what turns a
// player_id into a handle.

// StatRow is one `player_stat` row.
type StatRow struct {
	PlayerID   int64
	Stat       string
	Value      float64
	Context    json.RawMessage // nil when the board stores none
	UpdatedSeq int64
}

// Leaderboard reads a board page in rank order: value descending, and within a
// tie the earliest updated_seq first — whoever reached the number first (§5.6).
//
// limit and offset are raw: the caller over-fetches and drops hidden players
// itself, because this file knows nothing about bans.
func (p *Projections) Leaderboard(ctx context.Context, stat string, limit, offset int) ([]StatRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat
		 WHERE stat = ? ORDER BY value DESC, updated_seq ASC, player_id ASC LIMIT ? OFFSET ?`,
		stat, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("store: read leaderboard %q: %w", stat, err)
	}
	return scanStatRows(rows)
}

// PlayerStats reads every board a player appears on, in stat order.
func (p *Projections) PlayerStats(ctx context.Context, playerID int64) ([]StatRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat WHERE player_id = ? ORDER BY stat`,
		playerID)
	if err != nil {
		return nil, fmt.Errorf("store: read player stats %d: %w", playerID, err)
	}
	return scanStatRows(rows)
}

// StatsForPlayers reads one board's rows for a specific set of players. The read
// API uses it to discount banned players from a rank without reading the whole
// board.
func (p *Projections) StatsForPlayers(ctx context.Context, stat string, playerIDs []int64) ([]StatRow, error) {
	if len(playerIDs) == 0 {
		return nil, nil
	}
	// Chunked, because an IN list is a query-plan input and an unbounded one
	// would be a way to make a single request expensive.
	const chunk = 200
	var out []StatRow
	for start := 0; start < len(playerIDs); start += chunk {
		ids := playerIDs[start:min(start+chunk, len(playerIDs))]
		args := make([]any, 0, len(ids)+1)
		args = append(args, stat)
		for _, id := range ids {
			args = append(args, id)
		}
		q := `SELECT player_id, stat, value, context, updated_seq FROM player_stat
		      WHERE stat = ? AND player_id IN (` + placeholders(len(ids)) + `)`
		rows, err := p.Reader().QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read stats for players: %w", err)
		}
		part, err := scanStatRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// StatAhead counts the rows that outrank (value, updatedSeq) on a board — the
// unfiltered half of a player's rank.
func (p *Projections) StatAhead(ctx context.Context, stat string, value float64, updatedSeq int64) (int64, error) {
	var n int64
	err := p.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM player_stat
		 WHERE stat = ? AND (value > ? OR (value = ? AND updated_seq < ?))`,
		stat, value, value, updatedSeq).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: rank on %q: %w", stat, err)
	}
	return n, nil
}

// StatCounts is the per-board row count backing `GET /v1/leaderboards` (§4.8).
func (p *Projections) StatCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := p.Reader().QueryContext(ctx, `SELECT stat, count(*) FROM player_stat GROUP BY stat`)
	if err != nil {
		return nil, fmt.Errorf("store: count boards: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var (
			stat string
			n    int64
		)
		if err := rows.Scan(&stat, &n); err != nil {
			return nil, fmt.Errorf("store: scan board count: %w", err)
		}
		out[stat] = n
	}
	return out, rows.Err()
}

func scanStatRows(rows *sql.Rows) ([]StatRow, error) {
	defer rows.Close()
	var out []StatRow
	for rows.Next() {
		var (
			r  StatRow
			cx sql.NullString
		)
		if err := rows.Scan(&r.PlayerID, &r.Stat, &r.Value, &cx, &r.UpdatedSeq); err != nil {
			return nil, fmt.Errorf("store: scan player_stat: %w", err)
		}
		if cx.Valid && cx.String != "" {
			r.Context = json.RawMessage(cx.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// --- feed --------------------------------------------------------------------

// FeedCap is the §5.4 bound on the `feed` table. There is no VACUUM (§5.4), so
// the cap keeps free-page churn bounded rather than the file small.
const FeedCap = 500

// FeedRow is one row of `feed` (§5.4).
type FeedRow struct {
	ID int64 `json:"id"`
	// At is the server receive time in unix ms — never the client's wall_t,
	// which is untrusted (§4.1).
	At      int64  `json:"at"`
	Handle  string `json:"handle"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// InsertFeed appends one feed row and returns it with its assigned id.
func (p *Projections) InsertFeed(ctx context.Context, q Querier, row FeedRow) (FeedRow, error) {
	if q == nil {
		q = p.Writer()
	}
	res, err := q.ExecContext(ctx,
		`INSERT INTO feed (at, handle, type, summary) VALUES (?, ?, ?, ?)`,
		row.At, row.Handle, row.Type, row.Summary)
	if err != nil {
		return FeedRow{}, fmt.Errorf("store: insert feed row: %w", err)
	}
	if row.ID, err = res.LastInsertId(); err != nil {
		return FeedRow{}, fmt.Errorf("store: insert feed row: %w", err)
	}
	return row, nil
}

// CapFeed trims the feed to its newest keep rows (§5.4).
func (p *Projections) CapFeed(ctx context.Context, q Querier, keep int) error {
	if q == nil {
		q = p.Writer()
	}
	if keep <= 0 {
		keep = FeedCap
	}
	if _, err := q.ExecContext(ctx,
		`DELETE FROM feed WHERE id <= (SELECT max(id) FROM feed) - ?`, keep); err != nil {
		return fmt.Errorf("store: cap feed: %w", err)
	}
	return nil
}

// RecentFeed reads the newest feed rows first — what a fresh SSE subscriber is
// primed with before it starts receiving live ones (§5.7).
func (p *Projections) RecentFeed(ctx context.Context, limit int) ([]FeedRow, error) {
	if limit <= 0 || limit > FeedCap {
		limit = FeedCap
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT id, at, handle, type, summary FROM feed ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read feed: %w", err)
	}
	defer rows.Close()

	var out []FeedRow
	for rows.Next() {
		var r FeedRow
		if err := rows.Scan(&r.ID, &r.At, &r.Handle, &r.Type, &r.Summary); err != nil {
			return nil, fmt.Errorf("store: scan feed row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- counts ------------------------------------------------------------------

// ProjectionCounts is the row census `GET /admin/stats` reports (§5.9).
type ProjectionCounts struct {
	PlayerStat  int64 `json:"player_stat"`
	FlightState int64 `json:"flight_state"`
	PlayerBody  int64 `json:"player_body"`
	Kitten      int64 `json:"kitten"`
	Feed        int64 `json:"feed"`
	// FlaggedFlights is how many flights carry at least one flag bit — the
	// number that says whether the anti-cheat surface is doing anything.
	FlaggedFlights int64 `json:"flagged_flights"`
}

// Counts reads the row census.
func (p *Projections) Counts(ctx context.Context) (ProjectionCounts, error) {
	var c ProjectionCounts
	for _, q := range []struct {
		sql string
		dst *int64
	}{
		{`SELECT count(*) FROM player_stat`, &c.PlayerStat},
		{`SELECT count(*) FROM flight_state`, &c.FlightState},
		{`SELECT count(*) FROM player_body`, &c.PlayerBody},
		{`SELECT count(*) FROM kitten`, &c.Kitten},
		{`SELECT count(*) FROM feed`, &c.Feed},
		{`SELECT count(*) FROM flight_state WHERE flags <> 0`, &c.FlaggedFlights},
	} {
		if err := p.Reader().QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			return ProjectionCounts{}, fmt.Errorf("store: projection census: %w", err)
		}
	}
	return c, nil
}
