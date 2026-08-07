// The queries in this file are the Go-side substitute for the join catlog
// cannot write: `player_stat` and `feed` live in projections.db, `handle`,
// `player` and `event` live in events.db, and the two files cannot be joined
// (§5.4). Everything the read API needs from events.db to render a projection
// row is fetched here and matched up in Go.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// DirectoryRow is one live handle with the ban state of the account behind it —
// the whole of what the in-memory player_id → handle map needs (§5.4).
type DirectoryRow struct {
	Handle    string
	HandleLC  string
	PlayerID  int64
	CreatedAt int64 // unix ms
	Banned    bool
}

// Directory lists every live handle joined to its player's ban state.
//
// This exists because projections.db cannot be joined to events.db (§5.4): the
// read API resolves player_id → handle from an in-memory map built by this
// query at start and rebuilt whenever a handle is created, revoked or banned.
// The join is within one file, so it is a plain SQL join.
func (e *Events) Directory(ctx context.Context) ([]DirectoryRow, error) {
	rows, err := e.Reader().QueryContext(ctx,
		`SELECT h.handle, h.handle_lc, h.player_id, h.created_at, p.banned_at
		 FROM handle h JOIN player p ON p.player_id = h.player_id
		 ORDER BY h.player_id, h.created_at, h.handle`)
	if err != nil {
		return nil, fmt.Errorf("store: read handle directory: %w", err)
	}
	defer rows.Close()

	var out []DirectoryRow
	for rows.Next() {
		var (
			r      DirectoryRow
			banned sql.NullInt64
		)
		if err := rows.Scan(&r.Handle, &r.HandleLC, &r.PlayerID, &r.CreatedAt, &banned); err != nil {
			return nil, fmt.Errorf("store: scan handle directory: %w", err)
		}
		r.Banned = banned.Valid
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecvTimes maps event seqs to their server receive time in unix ms.
//
// `player_stat.updated_seq` records *which* event set a record; §4.8's board
// rows have to report *when*. The timestamp is not duplicated into
// projections.db because it is already in events.db and a rebuild would have to
// keep the copy honest; a keyed lookup of at most one page of seqs is cheaper
// than that obligation.
func (e *Events) RecvTimes(ctx context.Context, seqs []int64) (map[int64]int64, error) {
	if len(seqs) == 0 {
		return nil, nil
	}
	out := make(map[int64]int64, len(seqs))
	const chunk = 200
	for start := 0; start < len(seqs); start += chunk {
		part := seqs[start:min(start+chunk, len(seqs))]
		args := make([]any, len(part))
		for i, s := range part {
			args[i] = s
		}
		rows, err := e.Reader().QueryContext(ctx,
			`SELECT seq, recv_time FROM event WHERE seq IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read event times: %w", err)
		}
		for rows.Next() {
			var seq, recv int64
			if err := rows.Scan(&seq, &recv); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: scan event time: %w", err)
			}
			out[seq] = recv
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: read event times: %w", err)
		}
	}
	return out, nil
}

// CountPlayers reports how many players exist and how many of those are banned
// — two of the numbers `GET /admin/stats` reports (§5.9).
func (e *Events) CountPlayers(ctx context.Context) (total, banned int64, err error) {
	if err := e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM player`).Scan(&total); err != nil {
		return 0, 0, fmt.Errorf("store: count players: %w", err)
	}
	if err := e.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM player WHERE banned_at IS NOT NULL`).Scan(&banned); err != nil {
		return 0, 0, fmt.Errorf("store: count banned players: %w", err)
	}
	return total, banned, nil
}
