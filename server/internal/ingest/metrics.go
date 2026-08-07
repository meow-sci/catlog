package ingest

import (
	"expvar"

	"github.com/meow-sci/catlog/server/internal/authz"
)

// The §5.9 ingest counters, published on the admin mux at /debug/vars.
//
// Every §4.9 code gets its `ingest_rejected_<code>` counter at init, even the
// ones ingest cannot produce: a dashboard that has to guess whether a missing
// variable means "zero" or "not implemented" is worse than a row of zeros.
var (
	metricAccepted   = publishInt("ingest_accepted")
	metricDeduped    = publishInt("ingest_deduped")
	metricReplayed   = publishInt("ingest_replayed")
	metricBatches    = publishInt("ingest_batches")
	metricRejections = publishInt("ingest_rejected")
	metricRejected   = publishRejectionCounters()
)

// publishInt registers an expvar Int, tolerating a name that already exists
// (expvar.NewInt panics on a duplicate, and tests re-enter this path).
func publishInt(name string) *expvar.Int {
	if v := expvar.Get(name); v != nil {
		if i, ok := v.(*expvar.Int); ok {
			return i
		}
	}
	i := new(expvar.Int)
	expvar.Publish(name, i)
	return i
}

func publishRejectionCounters() map[string]*expvar.Int {
	out := make(map[string]*expvar.Int)
	for _, code := range authz.Codes() {
		out[code] = publishInt("ingest_rejected_" + code)
	}
	return out
}

// countRejection bumps the total and the per-code counter.
func countRejection(code string) {
	metricRejections.Add(1)
	if c, ok := metricRejected[code]; ok {
		c.Add(1)
	}
}

// PublishQueueDepth exposes the write queue's depth as `ingest_queue_depth`.
// Not in the §5.9 list, but backpressure is invisible without it: the 503 path
// only fires once the queue is already full.
func PublishQueueDepth(w *Writer) {
	const name = "ingest_queue_depth"
	if expvar.Get(name) != nil {
		return
	}
	expvar.Publish(name, expvar.Func(func() any { return w.QueueDepth() }))
}
