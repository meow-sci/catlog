// The queries in this file are the write and read halves of the shadow-ban
// path (migration 0005): withhold a player's log rather than delete it, keep
// accepting what they ship, and leave both later decisions — restore, or delete
// permanently — one statement away.
//
// See `migrations/events/0005_shadowban.sql` for why this is a table move and
// not a filter, and for why it is the moderation sense of "shadow ban" rather
// than the anti-cheat sense Constitution §8 forbids.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// Shadowban is one row of the roster: who is withheld, since when, and why.
type Shadowban struct {
	PlayerID int64  `json:"player_id"`
	At       int64  `json:"at"`     // unix ms
	Reason   string `json:"reason"` // never empty
	// Events is how many of their events are currently withheld. It is a
	// separate count rather than a column, because a shadowbanned player keeps
	// shipping and the number keeps moving.
	Events int64 `json:"events"`
}

// shadowbanEventColumns is `event`'s column list in its physical order, which
// `shadowban_event` mirrors exactly so the move is one INSERT..SELECT each way.
const shadowbanEventColumns = `seq, event_id, player_id, flight_id, session_id, type, ver, sim_time, wall_time, recv_time, payload, career, enc`

const shadowbanEventSelect = `SELECT seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload, enc FROM shadowban_event`

// Shadowbanned reports whether a player's events are being withheld.
//
// This is the ingest routing lookup, and it deliberately reads the table rather
// than a cached set: it runs once per *batch* against a primary key, next to
// two ECDSA verifications and a Brotli decode, so the cost is unmeasurable — and
// a cache here would be a coherence question on the one path where getting it
// wrong publishes a shadowbanned player's records to a leaderboard.
func (e *Events) Shadowbanned(ctx context.Context, q Querier, playerID int64) (bool, error) {
	if q == nil {
		q = e.Reader()
	}
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM shadowban WHERE player_id = ?`, playerID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: check shadowban on player %d: %w", playerID, err)
	default:
		return true, nil
	}
}

// ShadowbannedIDs is every shadowbanned player_id — what the handle directory
// loads so a withheld player stops resolving on the read side (§5.4).
func (e *Events) ShadowbannedIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := e.Reader().QueryContext(ctx, `SELECT player_id FROM shadowban`)
	if err != nil {
		return nil, fmt.Errorf("store: list shadowbans: %w", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan shadowban: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Shadowbans is the roster with its live event counts, oldest ban first — the
// review queue `GET /admin/shadowban` serves.
func (e *Events) Shadowbans(ctx context.Context) ([]Shadowban, error) {
	rows, err := e.Reader().QueryContext(ctx,
		`SELECT s.player_id, s.at, s.reason, count(ev.seq)
		   FROM shadowban s LEFT JOIN shadowban_event ev ON ev.player_id = s.player_id
		  GROUP BY s.player_id, s.at, s.reason
		  ORDER BY s.at, s.player_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list shadowbans: %w", err)
	}
	defer rows.Close()

	var out []Shadowban
	for rows.Next() {
		var s Shadowban
		if err := rows.Scan(&s.PlayerID, &s.At, &s.Reason, &s.Events); err != nil {
			return nil, fmt.Errorf("store: scan shadowban: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ShadowbanFor reads one roster row, returning [ErrNotFound] when the player is
// not shadowbanned.
func (e *Events) ShadowbanFor(ctx context.Context, playerID int64) (Shadowban, error) {
	s := Shadowban{PlayerID: playerID}
	err := e.Reader().QueryRowContext(ctx,
		`SELECT at, reason FROM shadowban WHERE player_id = ?`, playerID).Scan(&s.At, &s.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Shadowban{}, ErrNotFound
	}
	if err != nil {
		return Shadowban{}, fmt.Errorf("store: read shadowban on player %d: %w", playerID, err)
	}
	if s.Events, err = e.CountWithheldEvents(ctx, playerID); err != nil {
		return Shadowban{}, err
	}
	return s, nil
}

// ShadowbanPlayer shadowbans a player: it records the roster row and moves every
// event they own out of the live log, in one transaction.
//
// Returning the count of moved rows rather than the total means "how many this
// call withheld" — a second call on an already-shadowbanned player moves nothing
// and reports zero, which is what makes the operation idempotent.
//
// The projections are **not** touched. They live in the other file and cannot be
// joined (§5.4); a rebuild is what removes the withheld player's rows from every
// board, and the caller is expected to queue one. Until it finishes, the handle
// directory is what hides them.
func (e *Events) ShadowbanPlayer(ctx context.Context, playerID, at int64, reason string) (moved int64, err error) {
	if reason == "" {
		reason = "shadowbanned"
	}
	err = e.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shadowban (player_id, at, reason) VALUES (?, ?, ?)
			 ON CONFLICT (player_id) DO UPDATE SET reason = excluded.reason`,
			playerID, at, reason); err != nil {
			return fmt.Errorf("store: shadowban player %d: %w", playerID, err)
		}
		moved, err = withholdEvents(ctx, tx, playerID, at)
		return err
	})
	return moved, err
}

// withholdEvents moves a player's live events into `shadowban_event`, keeping
// every column and — crucially — the original seq (0005).
func withholdEvents(ctx context.Context, q Querier, playerID, at int64) (int64, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO shadowban_event (`+shadowbanEventColumns+`, withheld_at)
		 SELECT `+shadowbanEventColumns+`, ? FROM event WHERE player_id = ?`, at, playerID)
	if err != nil {
		return 0, fmt.Errorf("store: withhold player %d events: %w", playerID, err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: withhold player %d events: %w", playerID, err)
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM event WHERE player_id = ?`, playerID); err != nil {
		return 0, fmt.Errorf("store: withhold player %d events: %w", playerID, err)
	}
	return moved, nil
}

// UnshadowbanPlayer lifts a shadow ban: it drops the roster row and moves every
// withheld event back into the live log at its original seq.
//
// The insert is a plain INSERT, not `INSERT OR IGNORE`. A seq collision here
// would mean 0004's allocator handed out a number that was already spoken for,
// which is a bug and not a condition to absorb — and absorbing it would drop a
// player's events during the operation whose entire purpose is giving them back.
func (e *Events) UnshadowbanPlayer(ctx context.Context, playerID int64) (restored int64, err error) {
	err = e.WithWriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO event (`+shadowbanEventColumns+`)
			 SELECT `+shadowbanEventColumns+` FROM shadowban_event WHERE player_id = ?`, playerID)
		if err != nil {
			return fmt.Errorf("store: restore player %d events: %w", playerID, err)
		}
		if restored, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("store: restore player %d events: %w", playerID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shadowban_event WHERE player_id = ?`, playerID); err != nil {
			return fmt.Errorf("store: restore player %d events: %w", playerID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shadowban WHERE player_id = ?`, playerID); err != nil {
			return fmt.Errorf("store: clear shadowban on player %d: %w", playerID, err)
		}
		return nil
	})
	return restored, err
}

// DeleteWithheldEvents destroys a shadowbanned player's withheld events for
// good, leaving the shadow ban itself in place so anything they ship next is
// still withheld.
//
// This is the end of the review process and it is the one irreversible verb in
// the set. It does not touch `event` (there is nothing of theirs there), the
// player row, their handles or their credentials — deleting the account is what
// `purge` is for, and conflating the two would make "I have reviewed this and
// the data goes" also mean "and the account is gone", which are not the same
// decision.
func (e *Events) DeleteWithheldEvents(ctx context.Context, playerID int64) (deleted int64, err error) {
	err = e.WithWriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM shadowban_event WHERE player_id = ?`, playerID)
		if err != nil {
			return fmt.Errorf("store: delete player %d withheld events: %w", playerID, err)
		}
		deleted, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: delete player %d withheld events: %w", playerID, err)
		}
		return nil
	})
	return deleted, err
}

// CountWithheldEvents counts one player's withheld events, or every withheld
// event when playerID is 0.
func (e *Events) CountWithheldEvents(ctx context.Context, playerID int64) (int64, error) {
	var (
		n   int64
		err error
	)
	if playerID == 0 {
		err = e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM shadowban_event`).Scan(&n)
	} else {
		err = e.Reader().QueryRowContext(ctx,
			`SELECT count(*) FROM shadowban_event WHERE player_id = ?`, playerID).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("store: count withheld events: %w", err)
	}
	return n, nil
}

// WithheldEvents reads one page of a player's withheld log, newest first — the
// review surface. Same cursor shape as [Events.PlayerEvents]: `before` is an
// exclusive upper bound on seq, and zero starts at the newest.
func (e *Events) WithheldEvents(ctx context.Context, playerID, before int64, limit int) ([]StoredEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	q := shadowbanEventSelect + ` WHERE player_id = ? ORDER BY seq DESC LIMIT ?`
	args := []any{playerID, limit}
	if before > 0 {
		q = shadowbanEventSelect + ` WHERE player_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`
		args = []any{playerID, before, limit}
	}
	rows, err := e.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read player %d withheld events: %w", playerID, err)
	}
	return e.scanStoredEvents(rows, limit)
}

// MaxWithheldSeq is the highest seq held in `shadowban_event`, or 0 when
// nothing is withheld. The seq allocator reconciles against it at open, because
// a withheld row owns its number just as firmly as a live one does (0004).
func (e *Events) MaxWithheldSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := e.Reader().QueryRowContext(ctx, `SELECT max(seq) FROM shadowban_event`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: read max withheld seq: %w", err)
	}
	return seq.Int64, nil
}

// insertWithheldEvents is [Events.InsertEvents] for a shadowbanned player: the
// same rows, the same allocated seqs, the same dedup guarantee, into the other
// table.
//
// It is a separate statement builder rather than a table name substituted into
// one, because the two tables genuinely differ by a column and a shared builder
// parameterised on that would be harder to read than the duplication.
func (e *Events) insertWithheldEvents(ctx context.Context, q Querier, playerID int64, evs []Event, seq int64) (accepted, deduped int, err error) {
	recv := e.nowMillis()
	var sb strings.Builder
	for start := 0; start < len(evs); start += EventInsertChunk {
		end := min(start+EventInsertChunk, len(evs))

		sb.Reset()
		sb.WriteString(`INSERT OR IGNORE INTO shadowban_event
	  (seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload, enc, withheld_at)
	  VALUES `)
		args := make([]any, 0, (end-start)*14)
		for i := start; i < end; i++ {
			ev := evs[i]
			payload, enc := e.encodePayload(ev.Payload)
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args,
				seq+int64(i), ids.Bytes(ev.ID), playerID, ids.NullBytes(ev.FlightID), ids.NullBytes(ev.SessionID),
				nullString(ev.Career), ev.Type, ev.Ver, ev.SimTime, ev.WallTime, recv, payload, enc, recv)
		}
		res, err := q.ExecContext(ctx, sb.String(), args...)
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert withheld events: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert withheld events: %w", err)
		}
		accepted += int(n)
		deduped += (end - start) - int(n)
	}
	return accepted, deduped, nil
}
