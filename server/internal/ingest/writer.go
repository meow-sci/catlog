package ingest

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// QueueDepth is the §5.5 bound on the write queue. A full queue is answered
// with 503 + Retry-After rather than by growing memory: the mod already knows
// how to back off, and an unbounded queue only converts overload into an
// out-of-memory kill.
const QueueDepth = 256

// ErrBusy is returned by [Writer.Submit] when the queue is full.
var ErrBusy = errors.New("ingest: write queue is full")

// WriteJob is one verified batch handed to the writer (§5.5). Everything in it
// has already passed §4.5.3 steps 1–10; the writer owns 11–13.
type WriteJob struct {
	PlayerID int64
	JKT      string
	BatchID  string // proof jti
	SID      ids.ID
	Seq      int64
	BH       string // this batch's body hash — becomes the next batch's ph
	PH       string // previous batch's body hash, empty when seq == 1
	Events   []store.Event

	reply chan WriteResult
}

// respond delivers a result to whoever submitted the job. The channel is
// buffered, so this never blocks even when the handler has given up waiting;
// a job with no reply channel (only tests build one) is simply dropped.
func (j *WriteJob) respond(res WriteResult) {
	if j.reply == nil {
		return
	}
	j.reply <- res
}

// WriteResult is what the writer sends back: the §4.4 response, or the
// rejection that replaces it.
type WriteResult struct {
	Accepted int
	Deduped  int
	Replay   bool
	// Err is a §4.9 rejection produced by steps 11–13 (stream_fork, internal).
	Err *authz.Error
}

// Writer owns every write to events.db (§5.4, §5.5): one goroutine, one
// transaction at a time, so no two batches can interleave inside the stream
// chain check.
type Writer struct {
	events *store.Events
	log    *slog.Logger
	jobs   chan *WriteJob
	// notify wakes the projector after a commit (§5.5). Non-blocking sends, so
	// a slow or absent projector never stalls ingest.
	notify chan struct{}
	done   chan struct{}
	// now is the server clock, stamping `stream_state.updated_at` and
	// `ingest_batch.recv_time`. Defaults to [time.Now]; catlogd replaces it via
	// [Writer.SetClock] with the same clock the store and the verifier read, so
	// a batch's three timestamps cannot disagree about what day it is.
	now func() time.Time
}

// SetClock replaces the writer's clock. catlogd calls this once at start-up
// with its shared server clock; the default is [time.Now].
func (w *Writer) SetClock(now func() time.Time) {
	if now != nil {
		w.now = now
	}
}

// NewWriter builds the writer for one events database.
func NewWriter(events *store.Events, log *slog.Logger) *Writer {
	if log == nil {
		log = slog.Default()
	}
	return &Writer{
		events: events,
		log:    log,
		jobs:   make(chan *WriteJob, QueueDepth),
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
		now:    time.Now,
	}
}

// Submit queues a job without blocking. [ErrBusy] means the caller must answer
// 503 with Retry-After (§5.5).
func (w *Writer) Submit(job *WriteJob) error {
	job.reply = make(chan WriteResult, 1)
	select {
	case w.jobs <- job:
		return nil
	default:
		return ErrBusy
	}
}

// Await blocks for a submitted job's result, honouring the caller's deadline.
// On timeout the job may still commit — the client's retry is idempotent by
// construction (§4.5.3 step 11 replay, plus the event dedup index), which is
// what makes abandoning the wait safe.
func (w *Writer) Await(ctx context.Context, job *WriteJob) (WriteResult, error) {
	select {
	case res := <-job.reply:
		return res, nil
	case <-ctx.Done():
		return WriteResult{}, ctx.Err()
	}
}

// QueueDepth reports how many jobs are waiting.
func (w *Writer) QueueDepth() int { return len(w.jobs) }

// Notify is the projector's wake-up channel (§5.5, consumed in WP4).
func (w *Writer) Notify() <-chan struct{} { return w.notify }

// Run processes jobs until ctx is cancelled, then drains the queue so no
// handler is left waiting on a reply that will never come.
//
// It must be called exactly once per Writer, on its own goroutine.
func (w *Writer) Run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			w.drain()
			return
		case job := <-w.jobs:
			// The job's own context, not the request's: a client that hangs up
			// mid-commit must not roll back a transaction that is already
			// writing. The handler has its own 30 s deadline for waiting.
			w.process(context.WithoutCancel(ctx), job)
		}
	}
}

// Wait blocks until Run has returned.
func (w *Writer) Wait() { <-w.done }

func (w *Writer) drain() {
	for {
		select {
		case job := <-w.jobs:
			job.respond(WriteResult{Err: &authz.Error{
				Code: authz.CodeInternal, Step: 13, Detail: "server is shutting down",
			}})
		default:
			return
		}
	}
}

// errShortCircuit unwinds the transaction for the two outcomes that must not
// write anything: a replayed batch (step 11) and a forked stream (step 12).
// Neither is an error for the client — the rollback is just how "do nothing"
// is spelled inside a transaction.
var errShortCircuit = errors.New("ingest: short circuit")

// process runs §4.5.3 steps 11–13 in one transaction.
func (w *Writer) process(ctx context.Context, job *WriteJob) {
	var res WriteResult

	err := w.events.WithWriteTx(ctx, func(tx *sql.Tx) error {
		// --- step 11: whole-batch replay short-circuit ----------------------
		seen, err := w.events.BatchSeen(ctx, tx, job.PlayerID, job.BatchID)
		if err != nil {
			return err
		}
		if seen {
			res = WriteResult{Accepted: 0, Deduped: len(job.Events), Replay: true}
			return errShortCircuit
		}

		// --- step 12: stream chain ------------------------------------------
		state, found, err := w.events.StreamState(ctx, tx, job.PlayerID, job.SID)
		if err != nil {
			return err
		}
		gap := false
		switch {
		case !found:
			// A stream's first batch: seq must be 1 and there is nothing to
			// chain to.
			if job.Seq != 1 || job.PH != "" {
				res = WriteResult{Err: forkError("first batch of a stream must be seq 1 with no ph")}
				return errShortCircuit
			}
		case job.Seq <= state.LastSeq:
			res = WriteResult{Err: forkError("seq has already been used on this stream")}
			return errShortCircuit
		case job.Seq == state.LastSeq+1:
			if job.PH != state.LastBH {
				res = WriteResult{Err: forkError("ph does not chain to the previous batch")}
				return errShortCircuit
			}
		default:
			// seq > last+1: batches were lost. Telemetry is loss-tolerant, so
			// this is accepted and marked for forensics (§4.5.3 step 12).
			gap = true
		}

		// --- step 13: insert -------------------------------------------------
		accepted, deduped, err := w.events.InsertEvents(ctx, tx, job.PlayerID, job.Events)
		if err != nil {
			return err
		}
		now := w.now().UnixMilli()
		if err := w.events.UpsertStreamState(ctx, tx, store.StreamState{
			PlayerID: job.PlayerID, SID: job.SID, JKT: job.JKT,
			LastSeq: job.Seq, LastBH: job.BH, Gap: gap, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := w.events.InsertBatch(ctx, tx, job.PlayerID, job.BatchID, len(job.Events), now); err != nil {
			return err
		}
		res = WriteResult{Accepted: accepted, Deduped: deduped}
		return nil
	})

	switch {
	case errors.Is(err, errShortCircuit):
		// res already carries the outcome; nothing was written.
	case err != nil:
		w.log.Error("ingest write failed", "player", job.PlayerID, "batch", job.BatchID, "err", err)
		res = WriteResult{Err: &authz.Error{Code: authz.CodeInternal, Step: 13, Detail: "could not store the batch"}}
	default:
		metricAccepted.Add(int64(res.Accepted))
		metricDeduped.Add(int64(res.Deduped))
		metricBatches.Add(1)
		w.wake()
	}
	if res.Replay {
		metricReplayed.Add(1)
	}

	job.respond(res)
}

// wake nudges the projector without ever blocking on it.
func (w *Writer) wake() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func forkError(detail string) *authz.Error {
	return &authz.Error{Code: authz.CodeStreamFork, Step: 12, Detail: detail}
}
