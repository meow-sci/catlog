package projector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// DefaultBatchSize is §5.6's "batches of 1000".
const DefaultBatchSize = 1000

// DefaultTick is §5.6's fallback poll: the projector wakes on the ingest
// writer's notify channel, and once a second regardless, so a missed wake-up
// costs a second of lag rather than an unbounded backlog.
const DefaultTick = time.Second

// Options configures [New].
type Options struct {
	// Events is the source log. Required.
	Events *store.Events
	// Live is the projections handle, wrapped for the rebuild swap. Required.
	Live *Live
	// Directory resolves player_id → handle for the feed (§5.4). Required.
	Directory *directory.Directory
	// Broadcaster receives committed feed rows. Optional; nil means no feed
	// fan-out, which is what the fold tests use.
	Broadcaster *Broadcaster
	// Notify is the ingest writer's wake-up channel (§5.5). Optional; without
	// it the projector runs on its ticker alone.
	Notify <-chan struct{}
	// Upcasters is the (type, ver) registry. Optional; nil means the launch
	// registry, which is empty.
	Upcasters *Upcasters
	// StoreOptions are passed to [store.OpenProjections] when a rebuild opens
	// the scratch database and reopens the live one.
	StoreOptions store.Options
	// BatchSize overrides [DefaultBatchSize].
	BatchSize int
	// FlushRows overrides [stats.DefaultFlushRows]: how many rows one flushed
	// statement carries when a batch's buffered projection writes go out.
	FlushRows int
	// Decoders overrides [DefaultDecoders]: how many goroutines decode a
	// batch's payloads before the serial fold.
	Decoders int
	// Tick overrides [DefaultTick].
	Tick time.Duration
	// Log receives one line per batch at debug and per rebuild at info.
	Log *slog.Logger
}

// Projector is the §5.6 fold loop plus the rebuild.
//
// One instance owns one projections database. [Projector.Run] must be called at
// most once; every other method is safe to call from anywhere, including while
// Run is going.
type Projector struct {
	events    *store.Events
	live      *Live
	dir       *directory.Directory
	bcast     *Broadcaster
	notify    <-chan struct{}
	upcasters *Upcasters
	storeOpts store.Options
	batchSize int
	flushRows int
	decoders  int
	tick      time.Duration
	log       *slog.Logger

	folds      []stats.Fold
	boardFolds []stats.Fold
	stateFolds []stats.Fold

	// applyMu serializes the incremental loop against a rebuild. Both mutate
	// the same projections database, and a rebuild also swaps the handle out
	// from under it.
	applyMu sync.Mutex

	// skipped remembers which (type, ver) pairs have already been logged, so a
	// million events from a newer mod produce one line rather than a million
	// (§4.1: "logs once"). Only touched under applyMu.
	skipped map[string]struct{}

	lag        atomic.Int64
	checkpoint atomic.Int64
	done       chan struct{}
}

// Progress is what one [Projector.Step] did.
type Progress struct {
	// Read is how many event rows the batch contained.
	Read int
	// Skipped is how many of those could not be decoded (§4.1).
	Skipped int
	// Feed is how many feed rows the batch produced.
	Feed int
	// LastSeq is the checkpoint after the batch.
	LastSeq int64
	// More reports whether the batch filled, i.e. there is certainly more.
	More bool
}

// New builds a projector. It does not read anything until [Projector.Step] or
// [Projector.Run] runs.
func New(opts Options) (*Projector, error) {
	switch {
	case opts.Events == nil:
		return nil, errors.New("projector: Events is required")
	case opts.Live == nil:
		return nil, errors.New("projector: Live is required")
	case opts.Directory == nil:
		return nil, errors.New("projector: Directory is required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Upcasters == nil {
		opts.Upcasters = NewUpcasters()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.FlushRows <= 0 {
		opts.FlushRows = stats.DefaultFlushRows
	}
	if opts.Decoders <= 0 {
		opts.Decoders = DefaultDecoders()
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultTick
	}
	return &Projector{
		events:     opts.Events,
		live:       opts.Live,
		dir:        opts.Directory,
		bcast:      opts.Broadcaster,
		notify:     opts.Notify,
		upcasters:  opts.Upcasters,
		storeOpts:  opts.StoreOptions,
		batchSize:  opts.BatchSize,
		flushRows:  opts.FlushRows,
		decoders:   opts.Decoders,
		tick:       opts.Tick,
		log:        opts.Log.With("component", "projector"),
		folds:      stats.Folds(),
		boardFolds: stats.BoardFolds(),
		stateFolds: stats.StateFolds(),
		skipped:    map[string]struct{}{},
		done:       make(chan struct{}),
	}, nil
}

// Run folds until ctx is cancelled, waking on the notify channel or the ticker
// (§5.6). It drains the backlog on each wake-up rather than one batch per tick,
// so a restart with a large log catches up at full speed.
func (p *Projector) Run(ctx context.Context) {
	defer close(p.done)

	t := time.NewTicker(p.tick)
	defer t.Stop()

	// One pass immediately: a restart should not wait a second before folding
	// whatever arrived while the process was down.
	p.drain(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.drain(ctx)
		case <-p.notify:
			p.drain(ctx)
		}
	}
}

// Wait blocks until [Projector.Run] has returned.
func (p *Projector) Wait() { <-p.done }

// drain folds batches until the log is exhausted or ctx is cancelled. A failure
// is logged and the loop stops: the checkpoint did not move, so the same batch
// is retried on the next wake-up rather than being skipped.
func (p *Projector) drain(ctx context.Context) {
	for {
		prog, err := p.Step(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Error("projection batch failed", "err", err)
			}
			return
		}
		if !prog.More {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// Drain folds every pending event and returns the last batch's progress. Tests
// and the seed endpoint use it to make the projector deterministic.
func (p *Projector) Drain(ctx context.Context) (Progress, error) {
	var last Progress
	for {
		prog, err := p.Step(ctx)
		if err != nil {
			return last, err
		}
		last.Read += prog.Read
		last.Skipped += prog.Skipped
		last.Feed += prog.Feed
		last.LastSeq = prog.LastSeq
		if !prog.More {
			return last, nil
		}
	}
}

// Step folds one batch: read past the checkpoint, apply every fold, write the
// projection updates **and** the checkpoint in one transaction, then publish the
// feed rows the transaction committed (§5.6).
func (p *Projector) Step(ctx context.Context) (Progress, error) {
	p.applyMu.Lock()
	defer p.applyMu.Unlock()

	var (
		prog Progress
		feed []store.FeedRow
	)
	err := p.live.With(func(proj *store.Projections) error {
		after, err := proj.Checkpoint(ctx, nil, store.AllProjections)
		if err != nil {
			return err
		}
		prog.LastSeq = after

		evs, err := p.events.EventsSince(ctx, after, p.batchSize)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		prog.Read = len(evs)
		prog.More = len(evs) == p.batchSize

		// Decoding is pure CPU and touches no database, so it happens across
		// every core before the transaction opens. The fold itself stays on one
		// goroutine: Turso has a single writer, and a batch's folds read each
		// other's writes.
		decoded := p.decodeAll(ctx, evs)

		return proj.WithWriteTx(ctx, func(tx *sql.Tx) error {
			feed = feed[:0]
			b := stats.NewBatch(tx, stats.BatchOptions{FlushRows: p.flushRows})
			last := after
			for i, d := range decoded {
				last = d.seq
				if !d.ok {
					prog.Skipped++
					p.logSkipOnce(evs[i], d.err)
					continue
				}
				if err := p.applyFolds(ctx, b, p.folds, d.ev); err != nil {
					return err
				}
				row, ok, err := p.feedRow(ctx, b, d.ev)
				if err != nil {
					return err
				}
				if ok {
					feed = append(feed, row)
				}
			}
			if err := b.Flush(ctx); err != nil {
				return err
			}
			if len(feed) > 0 {
				if feed, err = proj.InsertFeedRows(ctx, tx, feed); err != nil {
					return err
				}
				if err := proj.CapFeed(ctx, tx, store.FeedCap); err != nil {
					return err
				}
			}
			prog.LastSeq = last
			return proj.SetCheckpoint(ctx, tx, store.AllProjections, last)
		})
	})
	if err != nil {
		return Progress{}, err
	}

	prog.Feed = len(feed)
	if p.bcast != nil && len(feed) > 0 {
		p.bcast.Publish(feed)
	}
	if prog.Skipped > 0 {
		metricSkipped.Add(int64(prog.Skipped))
	}
	p.publishLag(ctx, prog.LastSeq)
	return prog, nil
}

// applyFolds runs a fold list over one event, naming the fold that failed:
// "which board broke" is the only useful thing in the log line.
func (p *Projector) applyFolds(ctx context.Context, b *stats.Batch, folds []stats.Fold, ev stats.Event) error {
	for _, f := range folds {
		if err := f.Apply(ctx, b, ev); err != nil {
			return fmt.Errorf("projector: fold %s at seq %d (%s): %w", f.Name(), ev.Seq, ev.Type, err)
		}
	}
	return nil
}

// feedRow renders the §5.6 feed line for an event, if it has one. The handle
// comes from the in-memory directory because projections.db cannot join to
// events.db (§5.4); a player with no handle — or a banned one, who is absent
// from the directory — produces no feed row.
//
// It only renders: the whole batch's rows are inserted together at the end, and
// pick up their ids there.
func (p *Projector) feedRow(ctx context.Context, b *stats.Batch, ev stats.Event) (store.FeedRow, bool, error) {
	handle, ok := p.dir.Handle(ev.PlayerID)
	if !ok {
		return store.FeedRow{}, false, nil
	}
	summary, ok, err := stats.Summarize(ctx, ev, handle, b)
	if err != nil || !ok {
		return store.FeedRow{}, false, err
	}
	return store.FeedRow{At: ev.RecvTime, Handle: handle, Type: ev.Type, Summary: summary}, true, nil
}

// logSkipOnce reports an event this build cannot decode.
//
// A payload it cannot handle is skipped and logged once (§4.1) rather than
// returned as an error: the row is valid, the batch that carried it was
// accepted, and failing here would wedge the checkpoint forever behind one event
// this build happens not to understand. The next build — or the next rebuild —
// folds it.
//
// It is called from the serial fold loop rather than from the decode fan-out,
// so "once" still names the first such event by seq.
func (p *Projector) logSkipOnce(se store.StoredEvent, err error) {
	key := fmt.Sprintf("%s@%d", se.Type, se.Ver)
	if _, seen := p.skipped[key]; seen {
		return
	}
	p.skipped[key] = struct{}{}
	// No payload in the line: it is player-supplied and unbounded (§5.11).
	p.log.Warn("skipping events this build cannot fold",
		"type", se.Type, "ver", se.Ver, "first_seq", se.Seq, "err", err)
}

// publishLag refreshes `projector_lag_seq` (§5.9).
func (p *Projector) publishLag(ctx context.Context, checkpoint int64) {
	p.checkpoint.Store(checkpoint)
	metricCheckpoint.Set(checkpoint)

	head, err := p.events.MaxSeq(ctx)
	if err != nil {
		return
	}
	lag := max(head-checkpoint, 0)
	p.lag.Store(lag)
	metricLag.Set(lag)
}

// Lag is the last measured distance between the checkpoint and the head of the
// log, in event rows (§5.9).
func (p *Projector) Lag() int64 { return p.lag.Load() }

// CheckpointSeq is the last committed checkpoint.
func (p *Projector) CheckpointSeq() int64 { return p.checkpoint.Load() }

// Live exposes the projections handle for the read API, which queries it under
// the swap RWMutex.
func (p *Projector) Live() *Live { return p.live }

// Broadcaster exposes the feed fan-out for the SSE handler (WP5).
func (p *Projector) Broadcaster() *Broadcaster { return p.bcast }

// FoldNames lists the registered folds in application order — a /admin/stats
// number that makes "which boards does this build compute" answerable without
// reading the source.
func (p *Projector) FoldNames() []string {
	out := make([]string, 0, len(p.folds))
	for _, f := range p.folds {
		out = append(out, f.Name())
	}
	return out
}
