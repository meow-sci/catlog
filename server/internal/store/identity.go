// The queries in this file are the write half of §4.7's ban / unban / purge
// paths. They are separate from events.go because they are the only statements
// in catlog that *delete* rather than append: everything else in events.db is
// an append-only log, and a `DELETE FROM event` deserves to be somewhere you
// have to go looking for it.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// MarkHandleRetired records a handle as permanently retired **without** taking
// it away from its owner (D9).
//
// This is the ban path. [Events.RetireHandle] deletes the `handle` row as well,
// which is right for a purge — the account is gone — but wrong for a ban that
// may be lifted: the handle_lc must be unclaimable by anyone else while the ban
// stands, and must still belong to the same account if it is lifted. Keeping
// the row and adding the retirement gives both, because a live `handle` row
// already blocks another claim.
func (e *Events) MarkHandleRetired(ctx context.Context, q Querier, handle, reason string, at int64) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO retired_handle (handle_lc, reason, retired_at) VALUES (?, ?, ?)`,
		LC(handle), reason, at); err != nil {
		return fmt.Errorf("store: retire handle %q: %w", handle, err)
	}
	return nil
}

// UnretireHandle removes a retirement record. It is only ever called for a
// handle whose `handle` row still exists — i.e. one retired by a ban that is
// now being lifted. A purged handle's row is gone, so nothing can un-retire it,
// which is what makes "never recycled" hold (D9).
func (e *Events) UnretireHandle(ctx context.Context, q Querier, handle string) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM retired_handle WHERE handle_lc = ?`, LC(handle)); err != nil {
		return fmt.Errorf("store: un-retire handle %q: %w", handle, err)
	}
	return nil
}

// UnrevokeCredentialsAt clears the revocation on a player's credentials that
// were revoked at exactly `at` — the timestamp a ban stamped on them.
//
// Matching on the exact instant is what makes unban an inverse rather than an
// amnesty: a credential the player revoked themselves from the dashboard, or
// one revoked by an earlier ban, carries a different timestamp and stays
// revoked.
func (e *Events) UnrevokeCredentialsAt(ctx context.Context, q Querier, playerID, at int64) (int64, error) {
	if q == nil {
		q = e.autocommit()
	}
	res, err := q.ExecContext(ctx,
		`UPDATE credential SET revoked_at = NULL WHERE player_id = ? AND revoked_at = ?`, playerID, at)
	if err != nil {
		return 0, fmt.Errorf("store: un-revoke player %d credentials: %w", playerID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: un-revoke player %d credentials: %w", playerID, err)
	}
	return n, nil
}

// PurgeCounts reports what a purge deleted (§4.7).
type PurgeCounts struct {
	Events int64 `json:"events"`
	// Withheld is how many of their events were being held by a shadow ban
	// (0005). A purge takes those too — an account deletion that left a copy of
	// the log in the other table would be a privacy failure dressed as a
	// feature, and §4.7 promises the rows are gone.
	Withheld    int64 `json:"withheld_events"`
	Batches     int64 `json:"batches"`
	Streams     int64 `json:"streams"`
	Credentials int64 `json:"credentials"`
	Handles     int64 `json:"handles"`
}

// PurgePlayer deletes everything a player owns in events.db, in one transaction:
// every event, ingest batch and stream, every credential, every handle, and the
// player row itself (§4.7).
//
// It deliberately does **not** write the tombstone or retire the handles — the
// caller does both, because a purge without them would silently make a banned
// account reclaimable. Callers must therefore read the handles and credentials
// they need *before* calling this.
//
// Projections are not touched: they live in the other file, cannot be joined,
// and §4.7 heals them on the next rebuild. The read API already hides a player
// with no handle, so a purged account disappears from every public surface the
// moment the directory reloads.
func (e *Events) PurgePlayer(ctx context.Context, playerID int64) (PurgeCounts, error) {
	var c PurgeCounts
	err := e.WithWriteTx(ctx, func(tx *sql.Tx) error {
		for _, step := range []struct {
			sql string
			dst *int64
		}{
			{`DELETE FROM event WHERE player_id = ?`, &c.Events},
			{`DELETE FROM shadowban_event WHERE player_id = ?`, &c.Withheld},
			{`DELETE FROM shadowban WHERE player_id = ?`, nil},
			{`DELETE FROM ingest_batch WHERE player_id = ?`, &c.Batches},
			{`DELETE FROM stream_state WHERE player_id = ?`, &c.Streams},
			{`DELETE FROM credential WHERE player_id = ?`, &c.Credentials},
			{`DELETE FROM handle WHERE player_id = ?`, &c.Handles},
			{`DELETE FROM player WHERE player_id = ?`, nil},
		} {
			res, err := tx.ExecContext(ctx, step.sql, playerID)
			if err != nil {
				return fmt.Errorf("store: purge player %d: %w", playerID, err)
			}
			if step.dst != nil {
				n, err := res.RowsAffected()
				if err != nil {
					return fmt.Errorf("store: purge player %d: %w", playerID, err)
				}
				*step.dst = n
			}
		}
		return nil
	})
	// VACUUM is unused by policy (§5.4), so the freed pages stay in the file.
	// /admin/stats reports the file size for exactly this reason (§13.1).
	return c, err
}
