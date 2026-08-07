package projector

import "expvar"

// The §5.9 projector counters, published on the admin mux at /debug/vars
// alongside the ingest ones.
var (
	// metricLag is `projector_lag_seq`: how many event rows sit between the
	// checkpoint and the head of the log. Zero means fully caught up; a number
	// that stops falling is the first symptom of a wedged fold.
	metricLag = publishInt("projector_lag_seq")
	// metricSSEClients is `sse_clients`: live feed subscribers.
	metricSSEClients = publishInt("sse_clients")
	// metricCheckpoint is not in §5.9's list. Lag alone cannot distinguish
	// "caught up" from "not running": both read zero on an empty log.
	metricCheckpoint = publishInt("projector_checkpoint_seq")
	// metricSkipped counts events the projector could not decode (§4.1).
	metricSkipped = publishInt("projector_skipped")
	// metricRebuilds counts completed rebuilds (§5.6, D22).
	metricRebuilds = publishInt("projector_rebuilds")
)

// publishInt registers an expvar Int, tolerating a name that already exists —
// tests construct several projectors in one process.
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
