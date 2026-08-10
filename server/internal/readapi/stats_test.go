package readapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

// `GET /v1/stats` is the only endpoint that describes catlog rather than its
// players, so what these tests pin is the arithmetic on top of the census —
// the shares, the averages, the ordering — rather than the fold, which
// stats/census_test.go covers against real events.

// countEvents writes census rows the way the fold does, through a write
// transaction so the response cache's write generation moves.
func (f *fixture) countEvents(typ, period, bucket string, n, firstAt, lastAt int64) {
	f.t.Helper()
	f.projWrite(`INSERT INTO event_census
	    (type, period, bucket, n, first_seq, last_seq, first_at, last_at)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		typ, period, bucket, n, 1, n, firstAt, lastAt)
}

// seedCensus writes an all-time total of 100 across two types, plus three
// daily buckets, one of which is the busiest.
func seedCensus(f *fixture) {
	const (
		day1 = 1_800_000_000_000
		day2 = day1 + 86_400_000
		day3 = day2 + 86_400_000
	)
	f.countEvents(stats.CensusAllTypes, stats.PeriodAllTime, "", 100, day1, day3)
	f.countEvents("telemetry.window", stats.PeriodAllTime, "", 75, day1, day3)
	f.countEvents("vehicle.rud", stats.PeriodAllTime, "", 25, day2, day3)

	f.countEvents(stats.CensusAllTypes, stats.PeriodDaily, "2026-08-06", 20, day1, day1)
	f.countEvents(stats.CensusAllTypes, stats.PeriodDaily, "2026-08-07", 70, day2, day2)
	f.countEvents(stats.CensusAllTypes, stats.PeriodDaily, "2026-08-08", 10, day3, day3)
	f.countEvents("telemetry.window", stats.PeriodDaily, "2026-08-08", 6, day3, day3)
	f.countEvents("vehicle.rud", stats.PeriodDaily, "2026-08-08", 4, day3, day3)
}

func TestStatsCountsTheLogRatherThanThePlayers(t *testing.T) {
	f := newFixture(t)
	seedCensus(f)

	out := decode[readapi.StatsResponse](t, f.get("/v1/stats"))

	if out.Events.Total != 100 {
		t.Errorf("total = %d, want 100", out.Events.Total)
	}
	// The total is the stored row, never a sum of the types: a type this build
	// cannot name is in the first and not the second.
	if len(out.Events.Types) != 2 {
		t.Fatalf("types = %+v", out.Events.Types)
	}
	// Most-counted first, with the share the frontends would otherwise each
	// have to compute (and each have to guard against a zero denominator).
	if out.Events.Types[0].Type != "telemetry.window" || out.Events.Types[0].Share != 0.75 {
		t.Errorf("busiest type = %+v, want telemetry.window at 0.75", out.Events.Types[0])
	}
	if out.Collection.Types != 2 {
		t.Errorf("distinct types = %d, want 2", out.Collection.Types)
	}

	// Three days with an event, so the average is over three — not over the
	// span, because catlog being switched off is not the same as catlog being
	// quiet.
	if out.Events.Days != 3 {
		t.Errorf("days = %d, want 3", out.Events.Days)
	}
	if got := out.Events.PerDay; got < 33.3 || got > 33.4 {
		t.Errorf("per day = %v, want 100/3", got)
	}
	if out.Events.Busiest == nil || out.Events.Busiest.Bucket != "2026-08-07" || out.Events.Busiest.Count != 70 {
		t.Errorf("busiest = %+v, want 2026-08-07 with 70", out.Events.Busiest)
	}

	// Oldest first: the order a chart plots left to right.
	if len(out.Events.Daily) != 3 || out.Events.Daily[0].Bucket != "2026-08-06" {
		t.Fatalf("daily series = %+v", out.Events.Daily)
	}

	// One entry per rolling window, in the API's own order, each naming the
	// window the server clock is in — so a reader knows which week they are
	// looking at without computing one.
	if len(out.Events.Windows) != len(stats.RollingPeriods()) {
		t.Fatalf("windows = %+v", out.Events.Windows)
	}
	for i, period := range stats.RollingPeriods() {
		w := out.Events.Windows[i]
		if w.Period != period {
			t.Errorf("window %d is %q, want %q", i, w.Period, period)
		}
		if w.Bucket == "" {
			t.Errorf("window %q names no bucket", period)
		}
	}
	if out.Generated == 0 {
		t.Error("the response does not say when it was assembled")
	}
}

// The per-window breakdown, which is the "each type per d/w/m/y bucket" half of
// the page. The daily window is the one the fixture's clock is not in, so this
// asks for the bucket the fold actually wrote.
func TestStatsBreaksAWindowDownByType(t *testing.T) {
	f := newFixture(t, func(d *readapi.Deps) {
		// Pinned so "today" is the day the fixture wrote a breakdown for.
		d.Now = func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	})
	seedCensus(f)

	out := decode[readapi.StatsResponse](t, f.get("/v1/stats"))
	today := out.Events.Windows[0]
	if today.Period != stats.PeriodDaily || today.Bucket != "2026-08-08" || today.Count != 10 {
		t.Fatalf("today = %+v", today)
	}
	if len(today.Types) != 2 {
		t.Fatalf("today's breakdown = %+v", today.Types)
	}
	if today.Types[0].Type != "telemetry.window" || today.Types[0].Count != 6 || today.Types[0].Share != 0.6 {
		t.Errorf("today's busiest type = %+v", today.Types[0])
	}
	// A per-window row's first/last would only repeat the window's own bounds,
	// so they are left off rather than published as noise.
	if today.Types[0].First != 0 || today.Types[0].Last != 0 {
		t.Errorf("a window breakdown carries per-type bounds: %+v", today.Types[0])
	}
}

// Everything here is a projection, so the page has to publish how current it
// is: a number that disagrees with another page is a lag, not a bug, and only
// this says so.
func TestStatsPublishesTheProjectorLag(t *testing.T) {
	f := newFixture(t)
	alice := f.player("alice")
	f.stat(alice, stats.StatRUDTotal, 3, 1)

	out := decode[readapi.StatsResponse](t, f.get("/v1/stats"))
	if out.Collection.LogHead == 0 {
		t.Error("log head is zero with an event in the log")
	}
	// The fixture writes projections directly and never advances the cursor, so
	// the whole log is lag — which is exactly the state this field exists to
	// make visible.
	if out.Collection.Lag != out.Collection.LogHead {
		t.Errorf("lag = %d, want %d", out.Collection.Lag, out.Collection.LogHead)
	}
	if out.Collection.Handles != 1 || out.Collection.ScoringPlayers != 1 {
		t.Errorf("handles = %d, scoring = %d, want 1 and 1", out.Collection.Handles, out.Collection.ScoringPlayers)
	}
	if out.Collection.Boards == 0 {
		t.Error("no boards published")
	}
}

// An empty database is a legitimate answer, not a 500 and not a page of nulls:
// this is what catlog serves on the morning of launch.
func TestStatsOnAnEmptyCollection(t *testing.T) {
	f := newFixture(t)
	out := decode[readapi.StatsResponse](t, f.get("/v1/stats"))

	if out.Events.Total != 0 || out.Events.PerDay != 0 || out.Events.Busiest != nil {
		t.Errorf("empty collection = %+v", out.Events)
	}
	// Empty arrays rather than nulls: a client mapping over these should not
	// have to null-check first.
	if out.Events.Types == nil || out.Events.Daily == nil || out.Events.Windows == nil {
		t.Errorf("null arrays in %+v", out.Events)
	}
	for _, w := range out.Events.Windows {
		if w.Types == nil {
			t.Errorf("window %q has a null breakdown", w.Period)
		}
	}
}

func TestStatsCountsSystemsAndBodiesAndCachesByProjectionGeneration(t *testing.T) {
	f := newFixture(t)
	counting := &countingLive{p: f.proj}
	srv, err := readapi.New(readapi.Deps{
		Projections: counting, Events: f.events, Directory: f.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := srv.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := counting.calls
	if first.Collection.Systems != 0 || first.Collection.SystemBodies != 0 {
		t.Fatalf("empty system census = %+v", first.Collection)
	}
	if _, err := srv.Stats(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counting.calls != afterFirst {
		t.Errorf("cached stats queried projections again: %d -> %d", afterFirst, counting.calls)
	}

	seedSystem(t, f, "hash-sol", "Sol", "Solar System", "solar-system", 2, 1, 1)
	seedRoot(t, f, "hash-sol", "sol", "Sol", 0, 1)
	seedOrbitingBody(t, f, "hash-sol")
	updated, err := srv.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Collection.Systems != 1 || updated.Collection.SystemBodies != 2 {
		t.Errorf("system census = %d systems, %d bodies, want 1 and 2",
			updated.Collection.Systems, updated.Collection.SystemBodies)
	}
	if counting.calls == afterFirst {
		t.Error("projection write generation did not invalidate the stats cache")
	}
}

func TestStatsProjectionFailureIsA500(t *testing.T) {
	f := newFixture(t, func(d *readapi.Deps) { d.Projections = failedProjections{} })
	rec := f.get("/v1/stats")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
}
