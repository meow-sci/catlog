package stats

import (
	"context"
	"database/sql"
	"fmt"
)

// The window half of the four write helpers in fold.go.
//
// Every board value catlog computes passes through putBest, putRecord,
// addCount or setValue, so hanging the rolling windows off those four is what
// makes periods compose with the dynamic board families for free: a
// `fastest_to_<body>` board that comes into existence the moment two players
// reach somewhere new gets its daily, weekly, monthly and yearly windows on the
// same event, with no registry to update and nothing to enumerate.
//
// Every one of these derives its bucket from ev.RecvTime — the server's own
// receive stamp — and never from the wall clock. That is not a stylistic
// preference: the projector's rebuild replays historical events, and a bucket
// taken from time.Now() would put a two-year-old event in this morning's
// window, so a rebuilt projections.db would disagree with the incremental one
// and TestRebuildEqualsIncrementalForAnUnflaggedHistory would (correctly) fail.

// periodTrimEvery is how often, in events, a fold prunes windows that have aged
// past [Retention].
//
// Gated on the event's sequence number rather than on a timer or a sample,
// because seq is the projector's cursor: a rebuild replays the same seqs in the
// same order and therefore trims at exactly the same points, which keeps
// rebuild == incremental. A timer or a random draw would not.
const periodTrimEvery = 512

// periodRecord keeps the largest value seen inside each window.
func periodRecord(ctx context.Context, tx *sql.Tx, ev Event, stat string, value float64, cx any) error {
	return eachPeriod(ctx, tx, ev, func(period, bucket string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO player_stat_period (player_id, stat, period, bucket, value, context, updated_seq)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
			   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
			 WHERE excluded.value > player_stat_period.value`,
			ev.PlayerID, stat, period, bucket, value, cx, ev.Seq)
		return err
	})
}

// periodBest keeps the smallest value seen inside each window — the mirror of
// periodRecord, for the ascending career-time boards.
func periodBest(ctx context.Context, tx *sql.Tx, ev Event, stat string, value float64, cx any) error {
	return eachPeriod(ctx, tx, ev, func(period, bucket string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO player_stat_period (player_id, stat, period, bucket, value, context, updated_seq)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
			   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
			 WHERE excluded.value < player_stat_period.value`,
			ev.PlayerID, stat, period, bucket, value, cx, ev.Seq)
		return err
	})
}

// periodAdd accumulates a delta inside each window.
//
// This is the case that makes the table necessary rather than convenient: you
// cannot recover "how many RUDs in week 32" from a running lifetime total, so
// the deltas have to land in their window as they arrive.
func periodAdd(ctx context.Context, tx *sql.Tx, ev Event, stat string, delta float64) error {
	return eachPeriod(ctx, tx, ev, func(period, bucket string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO player_stat_period (player_id, stat, period, bucket, value, context, updated_seq)
			 VALUES (?, ?, ?, ?, ?, NULL, ?)
			 ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
			   value = player_stat_period.value + excluded.value, updated_seq = excluded.updated_seq`,
			ev.PlayerID, stat, period, bucket, delta, ev.Seq)
		return err
	})
}

// eachPeriod applies write to every rolling window the event falls in, then
// trims aged-out windows on the [periodTrimEvery] cadence.
//
// An event with no receive stamp writes no windows at all. That is the honest
// answer rather than a fallback to "now": a row whose window nobody can
// determine belongs in no window, and the all-time board still has it.
func eachPeriod(ctx context.Context, tx *sql.Tx, ev Event, write func(period, bucket string) error) error {
	if ev.RecvTime <= 0 {
		return nil
	}
	for _, period := range rollingPeriods {
		bucket, ok := Bucket(period, ev.RecvTime)
		if !ok {
			continue
		}
		if err := write(period, bucket); err != nil {
			return fmt.Errorf("stats: %s window %s of %s: %w", period, bucket, fmt.Sprintf("player %d", ev.PlayerID), err)
		}
	}
	if ev.Seq%periodTrimEvery == 0 {
		return trimPeriods(ctx, tx, ev.RecvTime)
	}
	return nil
}

// trimPeriods deletes windows older than [Retention].
//
// A plain `bucket < cutoff` string comparison, which works because every bucket
// format sorts chronologically as text (see [Bucket]) — no date arithmetic in
// SQL, which tursogo would not thank us for. It runs inside the projector's
// transaction, like the feed cap, because there is no VACUUM to tidy up after a
// deletion that happened somewhere else.
func trimPeriods(ctx context.Context, tx *sql.Tx, recvMS int64) error {
	for _, period := range rollingPeriods {
		cutoff, ok := retentionCutoff(period, recvMS)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM player_stat_period WHERE period = ? AND bucket < ?`, period, cutoff); err != nil {
			return fmt.Errorf("stats: trim %s windows before %s: %w", period, cutoff, err)
		}
	}
	return nil
}
