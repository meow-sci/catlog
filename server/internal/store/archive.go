package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// The events.db half of the archiver (§5.10): the one-row cursor that says how
// far the log has been copied to the archive, and the restore path that replays
// archived chunks back into an empty database.
//
// # Restore preserves seq and player_id
//
// A restored event keeps the `seq` it was archived under, and a restored player
// keeps its `player_id`. Both are server-local values that nothing outside this
// process ever sees, so re-minting them would be harmless in isolation — except
// that `player_stat.updated_seq` and `player_stat.player_id` are built from
// them, and the whole point of the disaster-recovery path is that a rebuild over
// a restored log produces *the same projections* (§12 WP10). Re-minting would
// produce projections that were merely equivalent, and nothing would be able to
// tell that from subtly wrong.

// --- archive cursor ----------------------------------------------------------

// ArchiveCursor reads `archive_cursor.last_seq` — the highest seq already copied
// to the archive (§5.10). It is 0 before the first run, which is also the
// correct answer for a database that has never been archived: seq is a rowid, so
// the first event is seq 1.
func (e *Events) ArchiveCursor(ctx context.Context, q Querier) (int64, error) {
	if q == nil {
		q = e.Reader()
	}
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT last_seq FROM archive_cursor WHERE id = 1`).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("store: read archive cursor: %w", err)
	default:
		return seq, nil
	}
}

// SetArchiveCursor records how far the archive has been written to.
//
// The upsert is ON CONFLICT rather than an INSERT-then-UPDATE because the table
// is created empty and tursogo cannot tell a primary-key violation from any
// other constraint failure (§5.4).
func (e *Events) SetArchiveCursor(ctx context.Context, q Querier, lastSeq int64) error {
	if q == nil {
		q = e.Writer()
	}
	if lastSeq < 0 {
		return fmt.Errorf("store: archive cursor %d is negative", lastSeq)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO archive_cursor (id, last_seq) VALUES (1, ?)
		 ON CONFLICT (id) DO UPDATE SET last_seq = excluded.last_seq`, lastSeq); err != nil {
		return fmt.Errorf("store: set archive cursor: %w", err)
	}
	return nil
}

// --- restore -----------------------------------------------------------------

// ErrPlayerConflict means a restore found the target database already holding a
// different player under the identifier it was asked to restore into.
var ErrPlayerConflict = errors.New("store: player identity conflict")

// ErrSeqConflict means a restore found the target database already holding a
// different event at the seq it was asked to restore into.
var ErrSeqConflict = errors.New("store: event seq conflict")

// RestorePlayer recreates a player row at its original player_id (§5.10
// restore). It is idempotent, and it fails rather than papering over a
// mismatch: restoring into a database that already holds a *different* account
// under that id, or the same account under a different id, means the target is
// not the empty (or matching) database the caller believed it was.
//
// The archive carries only the raw event log (D8), so handles, credentials and
// bans are deliberately not restored — they come from a `catlogctl backup` copy
// of events.db, not from here.
func (e *Events) RestorePlayer(ctx context.Context, q Querier, playerID int64, uk keys.UserKey, idp string, createdAt int64) error {
	if q == nil {
		q = e.Writer()
	}
	if playerID <= 0 {
		return fmt.Errorf("store: restore player: player_id %d is not positive", playerID)
	}
	if idp == "" {
		return errors.New("store: restore player: empty idp")
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO player (player_id, user_key, idp, created_at) VALUES (?, ?, ?, ?)`,
		playerID, uk.Bytes(), idp, createdAt); err != nil {
		return fmt.Errorf("store: restore player %d: %w", playerID, err)
	}

	// OR IGNORE hides two different collisions — the id is taken, or the
	// user_key is — so both are checked explicitly rather than assumed away.
	var (
		gotID  int64
		gotKey []byte
	)
	err := q.QueryRowContext(ctx, `SELECT player_id, user_key FROM player WHERE user_key = ?`, uk.Bytes()).
		Scan(&gotID, &gotKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: player_id %d is already held by another account", ErrPlayerConflict, playerID)
	}
	if err != nil {
		return fmt.Errorf("store: restore player %d: %w", playerID, err)
	}
	if gotID != playerID {
		return fmt.Errorf("%w: %s is player %d here, not %d", ErrPlayerConflict, uk, gotID, playerID)
	}
	return nil
}

// RestoreEvents re-inserts archived events at their original seq (§5.10).
//
// Already-present events are counted as deduped, exactly as [Events.InsertEvents]
// does: a restore run twice, or a restore over a partially recovered log, must
// converge rather than fail (D19).
//
// A seq that is taken by a *different* event is a hard error. `INSERT OR IGNORE`
// cannot distinguish "this row is already here" from "something else owns this
// rowid", and quietly dropping an event during disaster recovery is the one
// failure mode this path must not have.
func (e *Events) RestoreEvents(ctx context.Context, q Querier, playerID int64, evs []StoredEvent) (inserted, deduped int, err error) {
	if q == nil {
		q = e.Writer()
	}
	const insertSQL = `INSERT OR IGNORE INTO event
	  (seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for _, se := range evs {
		if se.Seq <= 0 {
			return inserted, deduped, fmt.Errorf("store: restore event %s: seq %d is not positive", ids.String(se.ID), se.Seq)
		}
		payload := se.Payload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		res, err := q.ExecContext(ctx, insertSQL,
			se.Seq, ids.Bytes(se.ID), playerID, ids.NullBytes(se.FlightID), ids.NullBytes(se.SessionID),
			nullString(se.Career), se.Type, se.Ver, se.SimTime, se.WallTime, se.RecvTime, string(payload))
		if err != nil {
			return inserted, deduped, fmt.Errorf("store: restore event %s: %w", ids.String(se.ID), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return inserted, deduped, fmt.Errorf("store: restore event %s: %w", ids.String(se.ID), err)
		}
		if n == 1 {
			inserted++
			continue
		}

		// Nothing went in. Either this exact event is already stored (the dedup
		// index caught it — the ordinary idempotent-restore case) or the seq
		// belongs to something else. Ask which.
		seq, found, err := eventSeq(ctx, q, playerID, se.ID)
		if err != nil {
			return inserted, deduped, err
		}
		switch {
		case found && seq == se.Seq:
			deduped++
		case found:
			return inserted, deduped, fmt.Errorf("%w: event %s is stored at seq %d, the archive has it at %d",
				ErrSeqConflict, ids.String(se.ID), seq, se.Seq)
		default:
			return inserted, deduped, fmt.Errorf("%w: seq %d is already taken by another event",
				ErrSeqConflict, se.Seq)
		}
	}
	return inserted, deduped, nil
}

// eventSeq reports the seq an event is already stored under, if it is.
func eventSeq(ctx context.Context, q Querier, playerID int64, id ids.ID) (int64, bool, error) {
	var seq int64
	err := q.QueryRowContext(ctx,
		`SELECT seq FROM event WHERE player_id = ? AND event_id = ?`, playerID, ids.Bytes(id)).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("store: look up event %s: %w", ids.String(id), err)
	default:
		return seq, true, nil
	}
}
