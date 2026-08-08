package projector_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// benchEvents is how many events BenchmarkDrain folds. Override with
// CATLOG_BENCH_EVENTS.
const benchEvents = 100_000

// benchPlayers is how many distinct players the synthetic log spreads over.
const benchPlayers = 25

// BenchmarkDrain measures the projector's fold throughput over a synthetic log
// shaped like a load-harness run: many players, a telemetry-heavy mix.
func BenchmarkDrain(b *testing.B) {
	var folded int
	for b.Loop() {
		b.StopTimer()
		rig := newBenchRig(b, benchEvents, projector.Options{})
		b.StartTimer()

		prog, err := rig.proj.Drain(context.Background())
		if err != nil {
			b.Fatalf("drain: %v", err)
		}
		if prog.Read < benchEvents {
			b.Fatalf("folded %d events, want at least %d", prog.Read, benchEvents)
		}
		folded = prog.Read
	}
	b.ReportMetric(float64(folded), "events/op")
}

// BenchmarkDrainMemory reports what one drain costs in memory, which is the
// number that decides whether catlogd fits on a small VM: bytes allocated over
// the whole drain (GC pressure) and the peak live heap while it runs (the
// footprint).
func BenchmarkDrainMemory(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		rig := newBenchRig(b, benchEvents, projector.Options{})
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		stop := make(chan struct{})
		peak := make(chan uint64, 1)
		go func() {
			var hi uint64
			var m runtime.MemStats
			for {
				select {
				case <-stop:
					peak <- hi
					return
				default:
				}
				runtime.ReadMemStats(&m)
				hi = max(hi, m.HeapAlloc)
				time.Sleep(2 * time.Millisecond)
			}
		}()
		b.StartTimer()

		if _, err := rig.proj.Drain(context.Background()); err != nil {
			b.Fatalf("drain: %v", err)
		}

		b.StopTimer()
		close(stop)
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/float64(benchEvents), "alloc-B/event")
		b.ReportMetric(float64(after.Mallocs-before.Mallocs)/float64(benchEvents), "allocs/event")
		b.ReportMetric(float64(<-peak)/(1<<20), "peak-heap-MB")
		b.StartTimer()
	}
}

// BenchmarkDrainTuning sweeps the two knobs that decide how much of a batch the
// write-back cache gets to coalesce.
func BenchmarkDrainTuning(b *testing.B) {
	for _, batch := range []int{1000, 2000, 5000, 10000} {
		b.Run(fmt.Sprintf("batch=%d", batch), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				rig := newBenchRig(b, benchEvents, projector.Options{BatchSize: batch})
				b.StartTimer()
				if _, err := rig.proj.Drain(context.Background()); err != nil {
					b.Fatalf("drain: %v", err)
				}
			}
		})
	}
	for _, decoders := range []int{1, 4, 13} {
		b.Run(fmt.Sprintf("decoders=%d", decoders), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				rig := newBenchRig(b, benchEvents, projector.Options{Decoders: decoders})
				b.StartTimer()
				if _, err := rig.proj.Drain(context.Background()); err != nil {
					b.Fatalf("drain: %v", err)
				}
			}
		})
	}
}

type benchRig struct {
	events *store.Events
	live   *projector.Live
	proj   *projector.Projector
}

func newBenchRig(tb testing.TB, n int, opts projector.Options) *benchRig {
	tb.Helper()
	dir := tb.TempDir()
	events := openEvents(tb, filepath.Join(dir, "events.db"))
	projections := openProjections(tb, filepath.Join(dir, "projections.db"))

	set, err := keys.LoadOrCreate(filepath.Join(dir, "keys"))
	if err != nil {
		tb.Fatalf("keys: %v", err)
	}
	ctx := context.Background()

	players := make([]int64, benchPlayers)
	for i := range players {
		handle := fmt.Sprintf("p%03d", i)
		id, err := events.EnsurePlayer(ctx, nil, set.UserKey("dev", handle), "dev", 1)
		if err != nil {
			tb.Fatalf("ensure player: %v", err)
		}
		if err := events.ClaimHandle(ctx, id, handle, 1); err != nil {
			tb.Fatalf("claim handle: %v", err)
		}
		players[i] = id
	}

	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		tb.Fatalf("reload directory: %v", err)
	}

	// Write the log in per-player runs so the seq order interleaves the way a
	// real multi-player ingest does.
	perPlayer := n / benchPlayers
	if err := events.WithWriteTx(ctx, func(tx *sql.Tx) error {
		for round := 0; round*len(benchHistory) < perPlayer; round++ {
			for pi, pid := range players {
				evs := benchRun(pi, round)
				if _, _, err := events.InsertEvents(ctx, tx, pid, evs); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		tb.Fatalf("seed events: %v", err)
	}

	live := projector.NewLive(projections)
	opts.Events, opts.Live, opts.Directory = events, live, d
	opts.Broadcaster = projector.NewBroadcaster()
	opts.StoreOptions, opts.Log = testutil.Options(), testutil.DiscardLogger()
	p, err := projector.New(opts)
	if err != nil {
		tb.Fatalf("new projector: %v", err)
	}
	return &benchRig{events: events, live: live, proj: p}
}

func openEvents(tb testing.TB, path string) *store.Events {
	tb.Helper()
	db, err := store.OpenEvents(context.Background(), path, testutil.Options())
	if err != nil {
		tb.Fatalf("open events: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

func openProjections(tb testing.TB, path string) *store.Projections {
	tb.Helper()
	db, err := store.OpenProjections(context.Background(), path, testutil.Options())
	if err != nil {
		tb.Fatalf("open projections: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

// benchHistory is one flight's worth of event types, in the proportion the load
// harness produces: telemetry windows dominate.
var benchHistory = []string{
	"flight.started",
	"vehicle.staging", "vehicle.staging", "vehicle.staging",
	"telemetry.window", "telemetry.window", "telemetry.window", "telemetry.window",
	"telemetry.window", "telemetry.window", "telemetry.window", "telemetry.window",
	"vehicle.orbit",
	"vehicle.soi",
	"telemetry.window", "telemetry.window", "telemetry.window", "telemetry.window",
	"vehicle.docked",
	"kitten.tumble",
	"vehicle.impact",
	"flight.ended",
	"roster.snapshot",
}

var benchSeq int

// benchRun renders one flight for a player, as store.Events ready to insert.
func benchRun(playerIdx, round int) []store.Event {
	f := flight(playerIdx*4096 + round)
	career := fmt.Sprintf("c%d", playerIdx)
	out := make([]store.Event, 0, len(benchHistory))
	simT := float64(round) * 600
	for i, typ := range benchHistory {
		simT += 7
		var payload any
		fid := f
		switch typ {
		case "flight.started":
			payload = stats.FlightStarted{VehicleName: "V", Body: "earth", CrewCount: 2, MassKg: 41000, PartCount: 60}
		case "vehicle.staging":
			payload = stats.VehicleStaging{StageIndex: i}
		case "telemetry.window":
			payload = tw("earth", 800+float64(i*13), 7800+float64(i*7), 3.5+float64(i%5))
		case "vehicle.orbit":
			payload = stats.VehicleOrbit{Phase: "achieved", Body: "earth", ApM: 300000, PeM: 280000}
		case "vehicle.soi":
			payload = stats.VehicleSOI{FromBody: "earth", ToBody: []string{"luna", "sol", "mars", "venus"}[round%4]}
		case "vehicle.docked":
			payload = stats.VehicleDock{OtherFlight: ids.String(flight(9999))}
		case "kitten.tumble":
			payload = stats.KittenTumble{Kid: "k1", Name: "Comet", SpeedMs: 8.2, Body: "earth"}
		case "vehicle.impact":
			payload = stats.VehicleImpact{SpeedMs: 180, EnergyJ: 3.1e7, Survived: true, Body: "earth", CrewCount: 2}
		case "flight.ended":
			payload = stats.FlightEnded{Reason: "recovered", CrewCount: 2}
		case "roster.snapshot":
			fid = ids.Zero
			ks := make([]stats.RosterKitten, 6)
			for k := range ks {
				ks[k] = stats.RosterKitten{
					Kid: fmt.Sprintf("k%d", k), Name: "Comet",
					TravelledM: float64(1_200_000 + round*1000 + k), FastestMs: 29800,
					Missions: round, MissionTimeS: 900,
				}
			}
			payload = stats.RosterSnapshot{Kittens: ks}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		benchSeq++
		var id ids.ID
		id[0] = 0x7f
		id[12] = byte(benchSeq >> 24)
		id[13] = byte(benchSeq >> 16)
		id[14] = byte(benchSeq >> 8)
		id[15] = byte(benchSeq)
		var session ids.ID
		session[0] = 0x01
		session[15] = byte(playerIdx)
		out = append(out, store.Event{
			ID: id, FlightID: fid, SessionID: session, Career: career,
			Type: typ, Ver: 1,
			SimTime:  sql.NullFloat64{Float64: simT, Valid: true},
			WallTime: 1_770_000_000_000 + int64(benchSeq),
			Payload:  raw,
		})
	}
	return out
}
