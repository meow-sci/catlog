package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
