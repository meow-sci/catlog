package projector

import (
	"context"
	"runtime"
	"sync"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// Decoding a batch is the one part of folding that is genuinely parallel.
//
// Everything else the projector does is serial by construction: Turso has a
// single writer, and a batch's folds read each other's writes — a board fold
// asks whether the flight the *same event* just flagged is flagged. Decoding
// asks nothing and writes nothing. It is `encoding/json` over a payload the
// database already handed us, so it fans out across cores and the fold loop
// then walks the results in seq order exactly as if nothing had happened.
//
// It is not where the projector's time went — that was the ~15 µs each of some
// twenty-odd SQL statements per event, which is what [stats.Batch] removes. But
// once those are gone, decoding is a real share of what is left, and it is free
// to take.

// decodeParallelMin is the batch size below which decoding stays inline.
// Spawning goroutines to unmarshal a handful of payloads costs more than it
// saves, and a projector caught up with the log folds one or two events at a
// time forever.
const decodeParallelMin = 64

// DefaultDecoders is how many goroutines decode one batch's payloads.
//
// One less than GOMAXPROCS, floored at one: catlogd is also serving the ingest
// that produces this backlog, and a projector that took every core would be
// competing with the thing feeding it.
func DefaultDecoders() int { return max(1, runtime.GOMAXPROCS(0)-1) }

// decodedEvent is one stored event after the decode pass. A row this build
// cannot decode is not an error — it is skipped and logged once (§4.1) — so the
// failure travels with the row rather than aborting the batch.
type decodedEvent struct {
	seq int64
	ev  stats.Event
	ok  bool
	err error
}

// decodeAll decodes a batch's payloads, in parallel when there are enough of
// them to be worth it. The result is index-aligned with evs.
//
// Nothing here touches the projector's mutable state: the skip log is written
// by the fold loop, in seq order, so "logs once" still means the first event of
// a kind rather than whichever goroutine got there first.
func (p *Projector) decodeAll(ctx context.Context, evs []store.StoredEvent) []decodedEvent {
	out := make([]decodedEvent, len(evs))

	workers := p.decoders
	if workers > 1 && len(evs) >= decodeParallelMin {
		var wg sync.WaitGroup
		chunk := (len(evs) + workers - 1) / workers
		for start := 0; start < len(evs); start += chunk {
			end := min(start+chunk, len(evs))
			wg.Add(1)
			go func(start, end int) {
				defer wg.Done()
				p.decodeRange(evs, out, start, end)
			}(start, end)
		}
		wg.Wait()
		return out
	}

	p.decodeRange(evs, out, 0, len(evs))
	return out
}

func (p *Projector) decodeRange(evs []store.StoredEvent, out []decodedEvent, start, end int) {
	for i := start; i < end; i++ {
		se := evs[i]
		out[i].seq = se.Seq
		// The upcaster registry is read-only once the projector is built, so
		// several goroutines may consult it at once; stats.Decode is pure.
		raw, err := p.upcasters.Apply(se.Type, se.Ver, se.Payload)
		if err != nil {
			out[i].err = err
			continue
		}
		ev, err := stats.Decode(se, raw)
		if err != nil {
			out[i].err = err
			continue
		}
		out[i].ev, out[i].ok = ev, true
	}
}
