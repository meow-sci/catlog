package projector_test

import (
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// newRawRig is a rig whose projector also publishes raw stored events.
func newRawRig(t *testing.T) (*rig, *projector.RawBroadcaster) {
	t.Helper()
	raw := projector.NewRawBroadcaster()
	r := newRig(t, func(o *projector.Options) { o.Raw = raw })
	return r, raw
}

// receive reads one published batch or fails.
func receive(t *testing.T, sub <-chan []store.StoredEvent) []store.StoredEvent {
	t.Helper()
	select {
	case batch := <-sub:
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was published to the raw subscriber")
		return nil
	}
}

// The correctness point of the raw stream: the projector skips an event its
// build cannot decode (§4.1, logSkipOnce) — but the raw views read events.db,
// where that event exists, so the live stream must carry it too or the stream
// and the paginated log would silently diverge.
func TestRawStreamCarriesEveryStoredEvent(t *testing.T) {
	r, raw := newRawRig(t)
	p := r.player("whiskers")

	sub, cancel := raw.Subscribe()
	defer cancel()

	f := flight(900)
	future := ev(f, "vehicle.impact", map[string]any{"speed_ms": 214, "survived": true, "crew_count": 1}, 5)
	future.Ver = 7 // no decoder for this version: the fold skips it
	good := ev(f, "vehicle.impact", stats.VehicleImpact{SpeedMs: 100, Survived: true, Body: "duna", CrewCount: 1}, 10)
	r.ship(p, future, good)

	if prog := r.drain(); prog.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1 — the fold must have skipped the ver-7 event", prog.Skipped)
	}

	batch := receive(t, sub)
	if len(batch) != 2 {
		t.Fatalf("published %d events, want both stored ones (skip included)", len(batch))
	}
	// Seq order, undecodable first — the order they were stored in.
	if batch[0].Ver != 7 || batch[1].Ver != 1 {
		t.Errorf("published vers = %d, %d; want the skipped ver-7 event then the folded one", batch[0].Ver, batch[1].Ver)
	}
	if batch[0].Seq >= batch[1].Seq || batch[0].Seq == 0 {
		t.Errorf("seqs = %d, %d; want stored seq order", batch[0].Seq, batch[1].Seq)
	}
	if string(batch[1].Payload) == "" || batch[1].PlayerID != p {
		t.Errorf("published row lost its stored fields: %+v", batch[1])
	}
}

// A rebuild folds the whole log again, and must stream none of it: the raw
// broadcaster publishes only from the incremental post-commit path, the same
// rule the feed broadcaster follows. A client on the stream during the nightly
// rebuild sees silence, not history.
func TestRebuildDoesNotRepublishRaw(t *testing.T) {
	r, raw := newRawRig(t)
	p := r.player("whiskers")

	f := flight(910)
	r.ship(p,
		ev(f, "flight.started", stats.FlightStarted{VehicleName: "Replay Bait", Body: "mun", CrewCount: 1}, 0),
		ev(f, "vehicle.impact", stats.VehicleImpact{SpeedMs: 120, Survived: true, Body: "mun", CrewCount: 1}, 8),
	)

	sub, cancel := raw.Subscribe()
	defer cancel()
	r.drain()
	receive(t, sub) // the incremental pass publishes, once

	r.rebuild()
	select {
	case batch := <-sub:
		t.Fatalf("a rebuild re-streamed %d events to a connected client", len(batch))
	case <-time.After(300 * time.Millisecond):
		// Silence is the contract.
	}

	// And the incremental loop resumes publishing after the swap.
	r.ship(p, ev(f, "vehicle.staging", map[string]any{"stage_index": 1}, 9))
	r.drain()
	after := receive(t, sub)
	if len(after) != 1 || after[0].Type != "vehicle.staging" {
		t.Errorf("post-rebuild publish = %+v, want the one new event", after)
	}
}
