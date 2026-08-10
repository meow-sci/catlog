package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/meow-sci/catlog/server/internal/ids"
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
		projection, lastSeq, p.nowMillis()); err != nil {
		return fmt.Errorf("store: set checkpoint %q: %w", projection, err)
	}
	return nil
}

// --- the build stamp ----------------------------------------------------------

// ProjectionBuild is the `proj_build` row: which binary's folds produced this
// file, over how much of the log, and whether that build finished (0005).
type ProjectionBuild struct {
	BuildID       string `json:"build_id"`
	FoldVersion   int    `json:"fold_version"`
	SchemaVersion int    `json:"schema_version"`
	BuiltFromSeq  int64  `json:"built_from_seq"`
	BuiltAt       int64  `json:"built_at"`
	Complete      bool   `json:"complete"`
}

// Build reads the build stamp. An unstamped file — anything built before 0005,
// or a scratch database a rebuild has not finished — reports the zero value and
// no error, because "I do not know what built this" is a legitimate state and
// the caller's response to it is the same as to a mismatch.
func (p *Projections) Build(ctx context.Context) (ProjectionBuild, error) {
	var (
		b        ProjectionBuild
		complete int
	)
	err := p.Reader().QueryRowContext(ctx,
		`SELECT build_id, fold_version, schema_version, built_from_seq, built_at, complete
		   FROM proj_build WHERE id = 1`).
		Scan(&b.BuildID, &b.FoldVersion, &b.SchemaVersion, &b.BuiltFromSeq, &b.BuiltAt, &complete)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectionBuild{}, nil
	}
	if err != nil {
		return ProjectionBuild{}, fmt.Errorf("store: read the projection build stamp: %w", err)
	}
	b.Complete = complete != 0
	return b, nil
}

// SetBuild writes the build stamp.
//
// A rebuild calls it against its scratch database *before* the swap, so a file
// on disk is never labelled with a build it does not actually contain — the
// stamp and the rows it describes land in the same file, and the swap is
// atomic.
func (p *Projections) SetBuild(ctx context.Context, q Querier, b ProjectionBuild) error {
	if q == nil {
		q = p.Writer()
	}
	complete := 0
	if b.Complete {
		complete = 1
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO proj_build (id, build_id, fold_version, schema_version, built_from_seq, built_at, complete)
		 VALUES (1, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   build_id = excluded.build_id, fold_version = excluded.fold_version,
		   schema_version = excluded.schema_version, built_from_seq = excluded.built_from_seq,
		   built_at = excluded.built_at, complete = excluded.complete`,
		b.BuildID, b.FoldVersion, b.SchemaVersion, b.BuiltFromSeq, b.BuiltAt, complete); err != nil {
		return fmt.Errorf("store: write the projection build stamp: %w", err)
	}
	return nil
}

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

// Leaderboard reads a board page in rank order: best value first, and within a
// tie the earliest updated_seq — whoever reached the number first (§5.6).
//
// asc inverts what "best" means. It is false for every record and counter board
// (bigger is better) and true for the career-time boards, where the value is
// seconds since the career began and the smallest wins. The tie rule does not
// change with it: the earliest claimant keeps the rank either way.
//
// limit and offset are raw: the caller over-fetches and drops hidden players
// itself, because this file knows nothing about bans.
func (p *Projections) Leaderboard(ctx context.Context, stat string, asc bool, limit, offset int) ([]StatRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat
		 WHERE stat = ? ORDER BY value `+order+`, updated_seq ASC, player_id ASC LIMIT ? OFFSET ?`,
		stat, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("store: read leaderboard %q: %w", stat, err)
	}
	return scanStatRows(rows)
}

// LeaderboardPeriod is [Projections.Leaderboard] for one rolling window.
//
// Same shape, same ordering, same tie-break — the only difference is which
// table the rows come from, which is what lets the read API treat a period as a
// dimension of a board rather than as a different kind of thing.
func (p *Projections) LeaderboardPeriod(
	ctx context.Context, stat, period, bucket string, asc bool, limit, offset int,
) ([]StatRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat_period
		 WHERE stat = ? AND period = ? AND bucket = ?
		 ORDER BY value `+order+`, updated_seq ASC, player_id ASC LIMIT ? OFFSET ?`,
		stat, period, bucket, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("store: read %s leaderboard %q for %s: %w", period, stat, bucket, err)
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
// unfiltered half of a player's rank. asc has the same meaning as in
// [Projections.Leaderboard] and must agree with it, or a profile would report a
// rank the board page contradicts.
func (p *Projections) StatAhead(ctx context.Context, stat string, asc bool, value float64, updatedSeq int64) (int64, error) {
	cmp := ">"
	if asc {
		cmp = "<"
	}
	var n int64
	err := p.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM player_stat
		 WHERE stat = ? AND (value `+cmp+` ? OR (value = ? AND updated_seq < ?))`,
		stat, value, value, updatedSeq).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: rank on %q: %w", stat, err)
	}
	return n, nil
}

// StatAheadForPlayer is [Projections.StatAhead] for every one of a player's
// rows on the given boards, in one statement: a correlated count over each of
// the player's own rows. The boards must all rank in the same direction — the
// read API calls this once for descending boards and once for ascending, which
// makes a profile cost two rank statements instead of one per board.
//
// The tie rule inside the subquery is byte-for-byte [Projections.StatAhead]'s,
// which is what keeps a profile's rank consistent with the board page.
func (p *Projections) StatAheadForPlayer(ctx context.Context, playerID int64, statKeys []string, asc bool) (map[string]int64, error) {
	if len(statKeys) == 0 {
		return nil, nil
	}
	cmp := ">"
	if asc {
		cmp = "<"
	}
	out := make(map[string]int64, len(statKeys))
	// Chunked like [Projections.StatsForPlayers], and for the same reason —
	// though a player is on at most a few dozen boards, so one chunk is the
	// overwhelmingly common case.
	const chunk = 200
	for start := 0; start < len(statKeys); start += chunk {
		part := statKeys[start:min(start+chunk, len(statKeys))]
		args := make([]any, 0, len(part)+1)
		args = append(args, playerID)
		for _, s := range part {
			args = append(args, s)
		}
		rows, err := p.Reader().QueryContext(ctx,
			`SELECT me.stat,
			        (SELECT count(*) FROM player_stat o
			         WHERE o.stat = me.stat
			           AND (o.value `+cmp+` me.value OR (o.value = me.value AND o.updated_seq < me.updated_seq)))
			 FROM player_stat me
			 WHERE me.player_id = ? AND me.stat IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: ranks for player %d: %w", playerID, err)
		}
		for rows.Next() {
			var (
				stat string
				n    int64
			)
			if err := rows.Scan(&stat, &n); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: scan player rank: %w", err)
			}
			out[stat] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: ranks for player %d: %w", playerID, err)
		}
	}
	return out, nil
}

// StatPlayers is how many players hold a value on one board.
//
// `player_stat` has one row per (player, stat), so the row count *is* the number
// of distinct players — which is what decides whether a board whose key came out
// of the data is published (stats.Known, stats.Catalog). It runs off the
// `stat_rank` index rather than [Projections.StatCounts]' full group-by, because
// a single board page must not pay for a census of every board.
func (p *Projections) StatPlayers(ctx context.Context, stat string) (int64, error) {
	var n int64
	if err := p.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM player_stat WHERE stat = ?`, stat).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count players on %q: %w", stat, err)
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

// RewoundCareers reports which of the given careers have had an earlier save
// loaded (§4.1). The read API calls it once per board page and once per profile,
// so a career-time row can be shown qualified.
//
// Resolving the mark here rather than baking it into `player_stat.context` at
// fold time is what makes it exact: a career rewound long after a record was set
// still shows, with no rebuild needed.
func (p *Projections) RewoundCareers(ctx context.Context, playerID int64, careers []string) (map[string]bool, error) {
	if len(careers) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(careers)+1)
	args = append(args, playerID)
	for _, c := range careers {
		args = append(args, c)
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT career FROM career WHERE player_id = ? AND rewound <> 0 AND career IN (`+
			placeholders(len(careers))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read rewound careers: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var career string
		if err := rows.Scan(&career); err != nil {
			return nil, fmt.Errorf("store: scan rewound career: %w", err)
		}
		out[career] = true
	}
	return out, rows.Err()
}

// FlaggedFlights reports which of the given flights carry at least one flag bit
// (§5.4 `flight_state.flags`).
//
// # Why the read API needs this
//
// A flagged flight scores nothing, and the privacy page promises players it
// "never appears publicly". The boards get that for free — the folds skip the
// flight, so no row is ever written — but the raw-event view reads events.db
// directly, where nothing records that a flight was flagged. `flight_state` is
// in projections.db and `event` is in events.db, and §5.4 says the two files
// cannot be joined, so the public event page asks this question about the
// flights on the page it is assembling and drops what it must.
//
// It is a primary-key lookup per distinct flight, chunked exactly like
// [Events.RecvTimes] and for the same reason — one indexed probe per flight on
// one page, never a scan, and no new projector state to keep honest.
func (p *Projections) FlaggedFlights(ctx context.Context, flights []ids.ID) (map[ids.ID]bool, error) {
	if len(flights) == 0 {
		return nil, nil
	}
	out := make(map[ids.ID]bool, len(flights))
	const chunk = 200
	for start := 0; start < len(flights); start += chunk {
		part := flights[start:min(start+chunk, len(flights))]
		args := make([]any, len(part))
		for i, f := range part {
			args[i] = ids.Bytes(f)
		}
		rows, err := p.Reader().QueryContext(ctx,
			`SELECT flight_id FROM flight_state WHERE flags <> 0 AND flight_id IN (`+
				placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read flight flags: %w", err)
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: scan flight flags: %w", err)
			}
			id, err := ids.FromBytes(raw)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: flight flags: %w", err)
			}
			out[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: read flight flags: %w", err)
		}
	}
	return out, nil
}

// --- feed --------------------------------------------------------------------

// FeedCap is the §5.4 bound on the `feed` table. VACUUM is unused by policy
// (§5.4), so the cap keeps free-page churn bounded rather than the file small.
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

// FeedInsertChunk is how many feed rows one insert statement carries.
const FeedInsertChunk = 500

// InsertFeedRows appends a whole batch's feed rows in one statement per
// [FeedInsertChunk] and returns them with their assigned ids.
//
// The ids come from `LastInsertId` and the fact that **one** INSERT assigns
// consecutive rowids: the last row's id minus the row count is the row before
// the first. That is worth the paragraph because it is the only place in catlog
// that infers an id it did not read back, and it is safe here for reasons that
// are all structural rather than incidental — `feed.id` is an INTEGER PRIMARY
// KEY (a rowid alias), nothing ever supplies one explicitly, the writer handle
// is capped at a single connection so no other statement can interleave, and
// the whole thing is inside the projector's transaction.
//
// The alternative — a statement per row, for its own LastInsertId — is what
// this replaces, and at ~15 µs per tursogo statement a busy batch was paying
// more for the feed than for the leaderboards it fed from.
func (p *Projections) InsertFeedRows(ctx context.Context, q Querier, rows []FeedRow) ([]FeedRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	if q == nil {
		q = p.Writer()
	}
	var sb strings.Builder
	for start := 0; start < len(rows); start += FeedInsertChunk {
		end := min(start+FeedInsertChunk, len(rows))

		sb.Reset()
		sb.WriteString(`INSERT INTO feed (at, handle, type, summary) VALUES `)
		args := make([]any, 0, (end-start)*4)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString("(?, ?, ?, ?)")
			args = append(args, rows[i].At, rows[i].Handle, rows[i].Type, rows[i].Summary)
		}

		res, err := q.ExecContext(ctx, sb.String(), args...)
		if err != nil {
			return nil, fmt.Errorf("store: insert feed rows: %w", err)
		}
		last, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("store: insert feed rows: %w", err)
		}
		first := last - int64(end-start) + 1
		for i := start; i < end; i++ {
			rows[i].ID = first + int64(i-start)
		}
	}
	return rows, nil
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

// --- event_census --------------------------------------------------------------

// CensusRow is one row of `event_census` — how many events of one type landed in
// one window, and the ends of the range they landed across.
//
// Type is the empty string on the row that counts every type; see
// stats.CensusAllTypes for why that total is stored rather than summed.
type CensusRow struct {
	Type   string `json:"type"`
	Period string `json:"period,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	// Count is how many events the row has counted.
	Count int64 `json:"count"`
	// FirstSeq/LastSeq and FirstAt/LastAt bound what went into it. The times are
	// the **server's** receive stamps, unix ms, never a client clock.
	FirstSeq int64 `json:"first_seq,omitempty"`
	LastSeq  int64 `json:"last_seq,omitempty"`
	FirstAt  int64 `json:"first_at,omitempty"`
	LastAt   int64 `json:"last_at,omitempty"`
}

const censusColumns = `type, period, bucket, n, first_seq, last_seq, first_at, last_at`

// CensusWindow is every type's count inside one window, largest first.
//
// bucket is "" for `alltime`, which is exactly what the fold wrote, so the
// all-time totals and one week's breakdown are the same query. The
// [stats.CensusAllTypes] row comes back with the others — the caller wants both
// the total and the split, and separating them here would cost a second read.
func (p *Projections) CensusWindow(ctx context.Context, period, bucket string) ([]CensusRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT `+censusColumns+` FROM event_census
		 WHERE period = ? AND bucket = ? ORDER BY n DESC, type ASC`, period, bucket)
	if err != nil {
		return nil, fmt.Errorf("store: read census window %s/%s: %w", period, bucket, err)
	}
	return scanCensusRows(rows)
}

// CensusSeries is the newest `limit` buckets of one period, oldest first — the
// shape a sparkline wants.
//
// Totals only ([stats.CensusAllTypes]), because a series of every type over
// ninety days is thousands of rows for a chart with one line in it. Read
// newest-first off the index and reversed in Go: the alternative is a subquery
// tursogo has no reason to plan well.
func (p *Projections) CensusSeries(ctx context.Context, period, allTypes string, limit int) ([]CensusRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT `+censusColumns+` FROM event_census
		 WHERE period = ? AND type = ? ORDER BY bucket DESC LIMIT ?`, period, allTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read census series %s: %w", period, err)
	}
	out, err := scanCensusRows(rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// CensusBusiest is the fullest bucket of one period — "the busiest day catlog
// has ever had". Totals only, and ok is false when the period has no buckets
// yet.
func (p *Projections) CensusBusiest(ctx context.Context, period, allTypes string) (CensusRow, bool, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT `+censusColumns+` FROM event_census
		 WHERE period = ? AND type = ? ORDER BY n DESC, bucket DESC LIMIT 1`, period, allTypes)
	if err != nil {
		return CensusRow{}, false, fmt.Errorf("store: read busiest %s: %w", period, err)
	}
	out, err := scanCensusRows(rows)
	if err != nil || len(out) == 0 {
		return CensusRow{}, false, err
	}
	return out[0], true, nil
}

// CensusBuckets is how many buckets of one period the census holds — how many
// distinct days catlog has seen an event on, which is the denominator behind
// "events per day".
func (p *Projections) CensusBuckets(ctx context.Context, period, allTypes string) (int64, error) {
	var n int64
	if err := p.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM event_census WHERE period = ? AND type = ?`, period, allTypes).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count %s buckets: %w", period, err)
	}
	return n, nil
}

func scanCensusRows(rows *sql.Rows) ([]CensusRow, error) {
	defer rows.Close()
	var out []CensusRow
	for rows.Next() {
		var r CensusRow
		if err := rows.Scan(&r.Type, &r.Period, &r.Bucket, &r.Count,
			&r.FirstSeq, &r.LastSeq, &r.FirstAt, &r.LastAt); err != nil {
			return nil, fmt.Errorf("store: scan event_census: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- counts ------------------------------------------------------------------

// ProjectionCounts is the row census `GET /admin/stats` reports (§5.9).
type ProjectionCounts struct {
	PlayerStat   int64 `json:"player_stat"`
	CareerStat   int64 `json:"career_stat"`
	SystemStat   int64 `json:"system_stat"`
	FlightState  int64 `json:"flight_state"`
	PlayerBody   int64 `json:"player_body"`
	CareerBody   int64 `json:"career_body"`
	Career       int64 `json:"career"`
	Kitten       int64 `json:"kitten"`
	CareerKitten int64 `json:"career_kitten"`
	System       int64 `json:"system"`
	SystemBody   int64 `json:"system_body"`
	Feed         int64 `json:"feed"`
	// FlaggedFlights is how many flights carry at least one flag bit — the
	// number that says whether the anti-cheat surface is doing anything.
	FlaggedFlights int64 `json:"flagged_flights"`
	// RewoundCareers is how many careers have had an earlier save loaded. Not an
	// anti-cheat number: it says how much of the career-time data is qualified.
	RewoundCareers int64 `json:"rewound_careers"`
	// ScoringPlayers is how many distinct players hold a value on any board, and
	// Bodies is how many distinct celestial bodies anybody has reached.
	//
	// Both are the "how much is in here" half of `GET /v1/stats`. Bodies is the
	// one number on that page catlog could not have known in advance: the server
	// keeps no list of celestial bodies (§4.2 makes them opaque strings), so this
	// counts the ones players went to.
	ScoringPlayers int64 `json:"scoring_players"`
	Bodies         int64 `json:"bodies"`
}

// Counts reads the row census.
func (p *Projections) Counts(ctx context.Context) (ProjectionCounts, error) {
	var c ProjectionCounts
	for _, q := range []struct {
		sql string
		dst *int64
	}{
		{`SELECT count(*) FROM player_stat`, &c.PlayerStat},
		{`SELECT count(*) FROM career_stat`, &c.CareerStat},
		{`SELECT count(*) FROM system_stat`, &c.SystemStat},
		{`SELECT count(*) FROM flight_state`, &c.FlightState},
		{`SELECT count(*) FROM player_body`, &c.PlayerBody},
		{`SELECT count(*) FROM career_body`, &c.CareerBody},
		{`SELECT count(*) FROM career`, &c.Career},
		{`SELECT count(*) FROM career WHERE rewound <> 0`, &c.RewoundCareers},
		{`SELECT count(*) FROM kitten`, &c.Kitten},
		{`SELECT count(*) FROM career_kitten`, &c.CareerKitten},
		{`SELECT count(*) FROM system`, &c.System},
		{`SELECT count(*) FROM system_body`, &c.SystemBody},
		{`SELECT count(*) FROM feed`, &c.Feed},
		{`SELECT count(*) FROM flight_state WHERE flags <> 0`, &c.FlaggedFlights},
		{`SELECT count(DISTINCT player_id) FROM player_stat`, &c.ScoringPlayers},
		{`SELECT count(DISTINCT body) FROM player_body`, &c.Bodies},
	} {
		if err := p.Reader().QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			return ProjectionCounts{}, fmt.Errorf("store: projection census: %w", err)
		}
	}
	return c, nil
}

// SystemRow is one immutable celestial-system header. Complete is effective:
// the mod reported completion and the catalogue contains exactly BodyCount rows.
type SystemRow struct {
	Hash      string
	SystemID  string
	Name      string
	Slug      string
	HomeBody  string
	BodyCount int64
	Complete  bool
	FirstSeq  int64
}

// Systems reads every visible system header in first-seen order.
func (p *Projections) Systems(ctx context.Context) ([]SystemRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT s.hash, s.system_id, s.name, s.slug, s.home_body, s.body_count,
		        CASE WHEN s.reported_complete <> 0 AND
		          (SELECT count(*) FROM system_body b WHERE b.hash = s.hash) = s.body_count
		        THEN 1 ELSE 0 END, s.first_seq
		 FROM system s ORDER BY s.first_seq, s.hash`)
	if err != nil {
		return nil, fmt.Errorf("store: read systems: %w", err)
	}
	return scanSystems(rows)
}

// SystemBySlugOrHash resolves a public slug or raw content hash.
func (p *Projections) SystemBySlugOrHash(ctx context.Context, key string) (SystemRow, bool, error) {
	var r SystemRow
	var complete int64
	err := p.Reader().QueryRowContext(ctx,
		`SELECT s.hash, s.system_id, s.name, s.slug, s.home_body, s.body_count,
		        CASE WHEN s.reported_complete <> 0 AND
		          (SELECT count(*) FROM system_body b WHERE b.hash = s.hash) = s.body_count
		        THEN 1 ELSE 0 END, s.first_seq
		 FROM system s WHERE s.slug = ? OR s.hash = ? ORDER BY s.hash = ? DESC LIMIT 1`,
		key, key, key).Scan(&r.Hash, &r.SystemID, &r.Name, &r.Slug, &r.HomeBody,
		&r.BodyCount, &complete, &r.FirstSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemRow{}, false, nil
	}
	if err != nil {
		return SystemRow{}, false, fmt.Errorf("store: read system %q: %w", key, err)
	}
	r.Complete = complete != 0
	return r, true, nil
}

func scanSystems(rows *sql.Rows) ([]SystemRow, error) {
	defer rows.Close()
	var out []SystemRow
	for rows.Next() {
		var r SystemRow
		var complete int64
		if err := rows.Scan(&r.Hash, &r.SystemID, &r.Name, &r.Slug, &r.HomeBody,
			&r.BodyCount, &complete, &r.FirstSeq); err != nil {
			return nil, fmt.Errorf("store: scan system: %w", err)
		}
		r.Complete = complete != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SystemBodyRow is one first-write catalogue row. Class and Kind are stored as
// reported; neither is checked against a server-side allow-list.
type SystemBodyRow struct {
	Hash, Body, Name, Class, Kind                    string
	Rank                                             int64
	Parent                                           sql.NullString
	RadiusM, MassKg, SoiM, AtmoM, OceanM, AngVel     float64
	AxisX, AxisY, AxisZ                              float64
	SmaM, Ecc, IncDeg, LanDeg, ArgpDeg, TPe, PeriodS sql.NullFloat64
	QuatX, QuatY, QuatZ, QuatW                       float64
	FirstSeq                                         int64
}

// SystemBodies returns one system's catalogue in canonical body-key order.
func (p *Projections) SystemBodies(ctx context.Context, hash string) ([]SystemBodyRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT hash, body, name, class, kind, rank, parent,
		 radius_m, mass_kg, soi_m, atmo_m, ocean_m, angvel, axis_x, axis_y, axis_z,
		 sma_m, ecc, inc_deg, lan_deg, argp_deg, t_pe, period_s,
		 ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq
		 FROM system_body WHERE hash = ? ORDER BY body`, hash)
	if err != nil {
		return nil, fmt.Errorf("store: read system bodies %q: %w", hash, err)
	}
	defer rows.Close()
	var out []SystemBodyRow
	for rows.Next() {
		var r SystemBodyRow
		if err := rows.Scan(&r.Hash, &r.Body, &r.Name, &r.Class, &r.Kind, &r.Rank, &r.Parent,
			&r.RadiusM, &r.MassKg, &r.SoiM, &r.AtmoM, &r.OceanM, &r.AngVel,
			&r.AxisX, &r.AxisY, &r.AxisZ, &r.SmaM, &r.Ecc, &r.IncDeg, &r.LanDeg,
			&r.ArgpDeg, &r.TPe, &r.PeriodS, &r.QuatX, &r.QuatY, &r.QuatZ, &r.QuatW,
			&r.FirstSeq); err != nil {
			return nil, fmt.Errorf("store: scan system body: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SystemCareerCount is one player's number of careers bound to a system.
// Keeping the player dimension lets the read API remove players hidden by the
// events-database directory before publishing either aggregate.
type SystemCareerCount struct {
	Hash     string
	PlayerID int64
	Careers  int64
}

// SystemCareerCounts groups the career table by system and player. An empty
// hash returns every system; a non-empty hash narrows the result for detail
// reads. Score tables are deliberately not involved: loading a system is
// enough to count even when the career never moved a board.
func (p *Projections) SystemCareerCounts(ctx context.Context, hash string) ([]SystemCareerCount, error) {
	query := `SELECT system, player_id, count(*) FROM career WHERE system <> ''`
	var args []any
	if hash != "" {
		query += ` AND system = ?`
		args = append(args, hash)
	}
	query += ` GROUP BY system, player_id ORDER BY system, player_id`
	rows, err := p.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read system career counts: %w", err)
	}
	defer rows.Close()
	var out []SystemCareerCount
	for rows.Next() {
		var r SystemCareerCount
		if err := rows.Scan(&r.Hash, &r.PlayerID, &r.Careers); err != nil {
			return nil, fmt.Errorf("store: scan system career count: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CareerStatRow is a career_stat row plus the save metadata needed by public
// read surfaces. Career is an internal key and must be relabelled before it
// leaves the read API; Ordinal is the human-facing save number.
type CareerStatRow struct {
	PlayerID   int64
	Career     string
	System     string
	Ordinal    int64
	Stat       string
	Value      float64
	Context    json.RawMessage
	UpdatedSeq int64
}

// CareerLeaderboard reads a career-scoped board page in canonical rank order.
// An empty system means no filter; otherwise the denormalised career_stat
// column makes the filter a direct predicate.
func (p *Projections) CareerLeaderboard(
	ctx context.Context, stat, system string, asc bool, limit, offset int,
) ([]CareerStatRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	query := `SELECT cs.player_id, cs.career, cs.system, c.ordinal,
	                cs.stat, cs.value, cs.context, cs.updated_seq
	         FROM career_stat cs
	         JOIN career c ON c.player_id = cs.player_id AND c.career = cs.career
	         WHERE cs.stat = ?`
	args := []any{stat}
	if system != "" {
		query += ` AND cs.system = ?`
		args = append(args, system)
	}
	query += ` ORDER BY cs.value ` + order + `, cs.updated_seq ASC, cs.player_id ASC, cs.career ASC
	           LIMIT ? OFFSET ?`
	args = append(args, limit, max(offset, 0))
	rows, err := p.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read career leaderboard %q: %w", stat, err)
	}
	return scanCareerStatRows(rows)
}

// CareerStatsForPlayer reads every board row for one exact save in stat order.
func (p *Projections) CareerStatsForPlayer(ctx context.Context, playerID int64, career string) ([]CareerStatRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT cs.player_id, cs.career, cs.system, c.ordinal,
		        cs.stat, cs.value, cs.context, cs.updated_seq
		 FROM career_stat cs
		 JOIN career c ON c.player_id = cs.player_id AND c.career = cs.career
		 WHERE cs.player_id = ? AND cs.career = ? ORDER BY cs.stat`, playerID, career)
	if err != nil {
		return nil, fmt.Errorf("store: read career stats for player %d: %w", playerID, err)
	}
	return scanCareerStatRows(rows)
}

// CareerStatsForPlayers returns every matching save row owned by the supplied
// players. A player-level summary is insufficient for rank correction because
// one hidden player may own several saves ahead of a visible row.
func (p *Projections) CareerStatsForPlayers(
	ctx context.Context, stat, system string, playerIDs []int64,
) ([]CareerStatRow, error) {
	if len(playerIDs) == 0 {
		return nil, nil
	}
	const chunk = 200
	var out []CareerStatRow
	for start := 0; start < len(playerIDs); start += chunk {
		ids := playerIDs[start:min(start+chunk, len(playerIDs))]
		args := make([]any, 0, len(ids)+2)
		args = append(args, stat)
		query := `SELECT cs.player_id, cs.career, cs.system, c.ordinal,
		                 cs.stat, cs.value, cs.context, cs.updated_seq
		          FROM career_stat cs
		          JOIN career c ON c.player_id = cs.player_id AND c.career = cs.career
		          WHERE cs.stat = ?`
		if system != "" {
			query += ` AND cs.system = ?`
			args = append(args, system)
		}
		query += ` AND cs.player_id IN (` + placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
		query += ` ORDER BY cs.player_id, cs.career`
		rows, err := p.Reader().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read career stats for players: %w", err)
		}
		part, err := scanCareerStatRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// CareerStatAhead counts saves ahead of a value/tie pair. An empty system
// means all systems; otherwise only saves in that system participate.
func (p *Projections) CareerStatAhead(
	ctx context.Context, stat, system string, value float64, seq int64, asc bool,
) (int64, error) {
	cmp := ">"
	if asc {
		cmp = "<"
	}
	query := `SELECT count(*) FROM career_stat
	          WHERE stat = ? AND (value ` + cmp + ` ? OR (value = ? AND updated_seq < ?))`
	args := []any{stat, value, value, seq}
	if system != "" {
		query += ` AND system = ?`
		args = append(args, system)
	}
	var n int64
	if err := p.Reader().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: rank saves on %q: %w", stat, err)
	}
	return n, nil
}

// CareerStatEntrants counts saves, not players. career_stat's primary key is
// (player_id, career, stat), so one player may contribute several entrants.
func (p *Projections) CareerStatEntrants(ctx context.Context, stat, system string) (int64, error) {
	query := `SELECT count(*) FROM career_stat WHERE stat = ?`
	args := []any{stat}
	if system != "" {
		query += ` AND system = ?`
		args = append(args, system)
	}
	var n int64
	if err := p.Reader().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count save entrants on %q: %w", stat, err)
	}
	return n, nil
}

func scanCareerStatRows(rows *sql.Rows) ([]CareerStatRow, error) {
	defer rows.Close()
	var out []CareerStatRow
	for rows.Next() {
		var r CareerStatRow
		var cx sql.NullString
		if err := rows.Scan(&r.PlayerID, &r.Career, &r.System, &r.Ordinal,
			&r.Stat, &r.Value, &cx, &r.UpdatedSeq); err != nil {
			return nil, fmt.Errorf("store: scan career_stat: %w", err)
		}
		if cx.Valid && cx.String != "" {
			r.Context = json.RawMessage(cx.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CareerRow is store-owned save state. The raw Career key is internal and must
// be relabelled by readapi before publication.
type CareerRow struct {
	PlayerID      int64
	Career        string
	Ordinal       int64
	System        string
	SystemChanged bool
	MaxSimT       float64
	Rewound       bool
	FirstSeq      int64
	LastSeq       int64
}

// PlayerCareers returns a player's saves in ordinal order.
func (p *Projections) PlayerCareers(ctx context.Context, playerID int64) ([]CareerRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, career, ordinal, system, system_changed,
		        max_sim_t, rewound, first_seq, last_seq
		 FROM career WHERE player_id = ? ORDER BY ordinal, career`, playerID)
	if err != nil {
		return nil, fmt.Errorf("store: read careers for player %d: %w", playerID, err)
	}
	return scanCareerRows(rows)
}

// CareerByOrdinal resolves one player's public save number without exposing
// the raw career key as an input to the read API.
func (p *Projections) CareerByOrdinal(ctx context.Context, playerID, ordinal int64) (CareerRow, bool, error) {
	var r CareerRow
	var changed, rewound int64
	err := p.Reader().QueryRowContext(ctx,
		`SELECT player_id, career, ordinal, system, system_changed,
		        max_sim_t, rewound, first_seq, last_seq
		 FROM career WHERE player_id = ? AND ordinal = ?`, playerID, ordinal).
		Scan(&r.PlayerID, &r.Career, &r.Ordinal, &r.System, &changed,
			&r.MaxSimT, &rewound, &r.FirstSeq, &r.LastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return CareerRow{}, false, nil
	}
	if err != nil {
		return CareerRow{}, false, fmt.Errorf("store: read career %d for player %d: %w", ordinal, playerID, err)
	}
	r.SystemChanged, r.Rewound = changed != 0, rewound != 0
	return r, true, nil
}

func scanCareerRows(rows *sql.Rows) ([]CareerRow, error) {
	defer rows.Close()
	var out []CareerRow
	for rows.Next() {
		var r CareerRow
		var changed, rewound int64
		if err := rows.Scan(&r.PlayerID, &r.Career, &r.Ordinal, &r.System, &changed,
			&r.MaxSimT, &rewound, &r.FirstSeq, &r.LastSeq); err != nil {
			return nil, fmt.Errorf("store: scan career: %w", err)
		}
		r.SystemChanged, r.Rewound = changed != 0, rewound != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SystemStatRow is a system_stat row. System is public content identity, not a
// per-player value and must not be relabelled.
type SystemStatRow struct {
	PlayerID   int64
	System     string
	Stat       string
	Value      float64
	Context    json.RawMessage
	UpdatedSeq int64
}

// SystemLeaderboard reads a system-scoped board page in canonical rank order.
// An empty system returns (player, system) pairs across every system.
func (p *Projections) SystemLeaderboard(
	ctx context.Context, stat, system string, asc bool, limit, offset int,
) ([]SystemStatRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	query := `SELECT player_id, system, stat, value, context, updated_seq
	          FROM system_stat WHERE stat = ?`
	args := []any{stat}
	if system != "" {
		query += ` AND system = ?`
		args = append(args, system)
	}
	query += ` ORDER BY value ` + order + `, updated_seq ASC, player_id ASC, system ASC
	           LIMIT ? OFFSET ?`
	args = append(args, limit, max(offset, 0))
	rows, err := p.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read system leaderboard %q: %w", stat, err)
	}
	return scanSystemStatRows(rows)
}

// SystemStatsForPlayer reads one player's rows for one exact system in stat order.
func (p *Projections) SystemStatsForPlayer(ctx context.Context, playerID int64, system string) ([]SystemStatRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT player_id, system, stat, value, context, updated_seq
		 FROM system_stat WHERE player_id = ? AND system = ? ORDER BY stat`, playerID, system)
	if err != nil {
		return nil, fmt.Errorf("store: read system stats for player %d: %w", playerID, err)
	}
	return scanSystemStatRows(rows)
}

// SystemStatAhead counts (player, system) rows ahead of a value/tie pair.
func (p *Projections) SystemStatAhead(
	ctx context.Context, stat, system string, value float64, seq int64, asc bool,
) (int64, error) {
	cmp := ">"
	if asc {
		cmp = "<"
	}
	query := `SELECT count(*) FROM system_stat
	          WHERE stat = ? AND (value ` + cmp + ` ? OR (value = ? AND updated_seq < ?))`
	args := []any{stat, value, value, seq}
	if system != "" {
		query += ` AND system = ?`
		args = append(args, system)
	}
	var n int64
	if err := p.Reader().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: rank system entrants on %q: %w", stat, err)
	}
	return n, nil
}

// SystemStatEntrants counts (player, system) pairs, not distinct players. A
// player with rows in two systems contributes two entrants when unfiltered.
func (p *Projections) SystemStatEntrants(ctx context.Context, stat, system string) (int64, error) {
	query := `SELECT count(*) FROM system_stat WHERE stat = ?`
	args := []any{stat}
	if system != "" {
		query += ` AND system = ?`
		args = append(args, system)
	}
	var n int64
	if err := p.Reader().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count system entrants on %q: %w", stat, err)
	}
	return n, nil
}

func scanSystemStatRows(rows *sql.Rows) ([]SystemStatRow, error) {
	defer rows.Close()
	var out []SystemStatRow
	for rows.Next() {
		var r SystemStatRow
		var cx sql.NullString
		if err := rows.Scan(&r.PlayerID, &r.System, &r.Stat, &r.Value, &cx, &r.UpdatedSeq); err != nil {
			return nil, fmt.Errorf("store: scan system_stat: %w", err)
		}
		if cx.Valid && cx.String != "" {
			r.Context = json.RawMessage(cx.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PlayerSystems returns metadata for every catalogue to which one of the
// player's careers is bound, in catalogue first-seen order.
func (p *Projections) PlayerSystems(ctx context.Context, playerID int64) ([]SystemRow, error) {
	rows, err := p.Reader().QueryContext(ctx,
		`SELECT s.hash, s.system_id, s.name, s.slug, s.home_body, s.body_count,
		        CASE WHEN s.reported_complete <> 0 AND
		          (SELECT count(*) FROM system_body b WHERE b.hash = s.hash) = s.body_count
		        THEN 1 ELSE 0 END, s.first_seq
		 FROM system s
		 JOIN (SELECT DISTINCT system FROM career WHERE player_id = ? AND system <> '') c
		   ON c.system = s.hash
		 ORDER BY s.first_seq, s.hash`, playerID)
	if err != nil {
		return nil, fmt.Errorf("store: read systems for player %d: %w", playerID, err)
	}
	return scanSystems(rows)
}
