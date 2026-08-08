package stats

import (
	"context"
	"fmt"
)

// The window half of the four write helpers in fold.go.
//
// These are where the projector's statement count used to live: every board
// value fans out into four rolling windows, so a single record write was five
// statements and a telemetry window was fifteen. They record into the batch now
// and leave as one statement per rule per flush.
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
func periodRecord(ctx context.Context, b *Batch, ev Event, stat string, value float64, cx any) error {
	return eachPeriod(ctx, b, ev, func(period, bucket string) {
		b.putPeriod(kindRecord, ev.PlayerID, stat, period, bucket, value, cx, ev.Seq)
	})
}

// periodBest keeps the smallest value seen inside each window — the mirror of
// periodRecord, for the ascending career-time boards.
func periodBest(ctx context.Context, b *Batch, ev Event, stat string, value float64, cx any) error {
	return eachPeriod(ctx, b, ev, func(period, bucket string) {
		b.putPeriod(kindBest, ev.PlayerID, stat, period, bucket, value, cx, ev.Seq)
	})
}

// periodAdd accumulates a delta inside each window.
//
// This is the case that makes the table necessary rather than convenient: you
// cannot recover "how many RUDs in week 32" from a running lifetime total, so
// the deltas have to land in their window as they arrive.
func periodAdd(ctx context.Context, b *Batch, ev Event, stat string, delta float64) error {
	return eachPeriod(ctx, b, ev, func(period, bucket string) {
		b.putPeriod(kindCount, ev.PlayerID, stat, period, bucket, delta, nil, ev.Seq)
	})
}

// eachPeriod applies write to every rolling window the event falls in, then
// trims aged-out windows on the [periodTrimEvery] cadence.
//
// An event with no receive stamp writes no windows at all. That is the honest
// answer rather than a fallback to "now": a row whose window nobody can
// determine belongs in no window, and the all-time board still has it.
func eachPeriod(ctx context.Context, b *Batch, ev Event, write func(period, bucket string)) error {
	if ev.RecvTime <= 0 {
		return nil
	}
	for i, bucket := range b.bucketsFor(ev.RecvTime) {
		if bucket == "" {
			continue
		}
		write(rollingPeriods[i], bucket)
	}
	if ev.Seq%periodTrimEvery == 0 {
		return b.trimPeriods(ctx, ev.Seq, ev.RecvTime)
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
// It is called once per trim seq, not once per board write at that seq. The
// deletes were always idempotent — an event that wrote three boards ran the
// same four DELETEs three times — so collapsing them changes nothing except
// how much the projector pays for its own housekeeping.
func (b *Batch) trimPeriods(ctx context.Context, seq, recvMS int64) error {
	if b.trimmedSeq == seq {
		return nil
	}
	b.trimmedSeq = seq

	// The buffered window writes go out first. One that this batch is still
	// holding is a row the one-statement-at-a-time path would already have
	// written, and therefore a row this delete would already have been able to
	// reach. It takes a batch spanning more than the retention horizon for that
	// to be more than theoretical — which is to say a rebuild, replaying a
	// history with quiet stretches in it, which is exactly the pass whose
	// answer has to match the incremental one.
	if err := b.flushPeriods(ctx); err != nil {
		return err
	}
	for _, period := range rollingPeriods {
		cutoff, ok := retentionCutoff(period, recvMS)
		if !ok {
			continue
		}
		if _, err := b.tx.ExecContext(ctx,
			`DELETE FROM player_stat_period WHERE period = ? AND bucket < ?`, period, cutoff); err != nil {
			return fmt.Errorf("stats: trim %s windows before %s: %w", period, cutoff, err)
		}
	}
	return nil
}
