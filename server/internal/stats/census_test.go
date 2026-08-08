package stats_test

import (
	"fmt"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The census counts the log, so its tests are about the log rather than about
// any player: how many events, of what kinds, in which windows.

// census reads event_census as a map keyed "type/period/bucket".
func census(t *testing.T, proj *store.Projections) map[string]int64 {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT type, period, bucket, n FROM event_census`)
	if err != nil {
		t.Fatalf("read event_census: %v", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var (
			typ, period, bucket string
			n                   int64
		)
		if err := rows.Scan(&typ, &period, &bucket, &n); err != nil {
			t.Fatalf("scan event_census: %v", err)
		}
		out[fmt.Sprintf("%s/%s/%s", typ, period, bucket)] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func foldCensus(t *testing.T, in []input) map[string]int64 {
	t.Helper()
	proj := testutil.MemProjections(t)
	apply(t, proj, in, 0, false)
	return census(t, proj)
}

// The whole feature in one test: a total, a per-type split, and the same two
// again inside every rolling window.
func TestCensusCountsEveryEventByTypeAndWindow(t *testing.T) {
	// Two days, deliberately in the same ISO week, month and year, so the
	// weekly/monthly/yearly rows carry both and only the daily rows split.
	day1, day2 := ms(t, "2026-03-02T09:00:00Z"), ms(t, "2026-03-03T09:00:00Z")
	got := foldCensus(t, []input{
		{typ: "session.started", recvMS: day1, payload: map[string]any{}},
		{typ: "kitten.tumble", flight: flightN(1), recvMS: day1,
			payload: map[string]any{"name": "boots", "speed_ms": 12.0, "body": "kerbin"}},
		{typ: "kitten.tumble", flight: flightN(1), recvMS: day2,
			payload: map[string]any{"name": "boots", "speed_ms": 14.0, "body": "kerbin"}},
	})

	for key, want := range map[string]int64{
		// All time: the total, then the split. The total is a stored row, not a
		// sum the reader has to do.
		"/alltime/":                3,
		"session.started/alltime/": 1,
		"kitten.tumble/alltime/":   2,

		// Daily splits the two days; every coarser window holds all three.
		"/daily/2026-03-02":                2,
		"/daily/2026-03-03":                1,
		"kitten.tumble/daily/2026-03-02":   1,
		"kitten.tumble/daily/2026-03-03":   1,
		"session.started/daily/2026-03-02": 1,
		"/weekly/2026-W10":                 3,
		"/monthly/2026-03":                 3,
		"/yearly/2026":                     3,
		"kitten.tumble/yearly/2026":        2,
	} {
		if got[key] != want {
			t.Errorf("census[%q] = %d, want %d", key, got[key], want)
		}
	}
	if _, ok := got["session.started/daily/2026-03-03"]; ok {
		t.Error("a type that saw no event on a day must have no row for it")
	}
}

// The census is a census, not a leaderboard: a flagged flight's events are
// still events catlog is storing, and dropping them here would make the count
// disagree with the log it counts.
func TestCensusCountsFlaggedFlightsThatNoBoardWill(t *testing.T) {
	flight := flightN(2)
	in := []input{
		{typ: "flight.flagged", flight: flight,
			payload: map[string]any{"reason": "teleport"}},
		{typ: "kitten.tumble", flight: flight,
			payload: map[string]any{"name": "boots", "speed_ms": 30.0, "body": "kerbin"}},
	}
	if got := foldCensus(t, in)["/alltime/"]; got != 2 {
		t.Errorf("census total = %d, want 2 — a flagged flight is still logged", got)
	}
	// And the board it would otherwise have scored is empty, which is what makes
	// the count above a deliberate difference rather than a missing exclusion.
	if rows := fold(t, in); len(rows) != 0 {
		t.Errorf("flagged flight scored %v", rows)
	}
}

// An event whose receive time nobody can determine belongs in no window — the
// same answer the rolling board windows give, and for the same reason. The
// all-time rows still have it, so the total never disagrees with the log.
func TestCensusPlacesAnUnstampedEventInNoWindow(t *testing.T) {
	got := foldCensus(t, []input{{typ: "session.started", recvMS: -1, payload: map[string]any{}}})
	if got["/alltime/"] != 1 {
		t.Errorf("all-time total = %d, want 1", got["/alltime/"])
	}
	for key := range got {
		if key != "/alltime/" && key != "session.started/alltime/" {
			t.Errorf("an event with no receive stamp wrote a window row %q", key)
		}
	}
}

// The census read helpers, against a folded database rather than hand-written
// rows: what the read API actually calls.
func TestCensusReadsBackAsWindowsSeriesAndBusiest(t *testing.T) {
	proj := testutil.MemProjections(t)
	apply(t, proj, []input{
		{typ: "session.started", recvMS: ms(t, "2026-03-02T09:00:00Z"), payload: map[string]any{}},
		{typ: "kitten.tumble", flight: flightN(1), recvMS: ms(t, "2026-03-02T10:00:00Z"),
			payload: map[string]any{"name": "boots", "speed_ms": 12.0, "body": "kerbin"}},
		{typ: "session.started", recvMS: ms(t, "2026-03-04T09:00:00Z"), payload: map[string]any{}},
	}, 0, false)
	ctx := t.Context()

	all, err := proj.CensusWindow(ctx, stats.PeriodAllTime, "")
	if err != nil {
		t.Fatal(err)
	}
	// Ordered by count, largest first, with the total row leading because it is
	// the largest by construction.
	if len(all) != 3 || all[0].Type != stats.CensusAllTypes || all[0].Count != 3 {
		t.Fatalf("all-time window = %+v", all)
	}
	if all[0].FirstAt != ms(t, "2026-03-02T09:00:00Z") || all[0].LastAt != ms(t, "2026-03-04T09:00:00Z") {
		t.Errorf("all-time bounds = %d..%d", all[0].FirstAt, all[0].LastAt)
	}

	series, err := proj.CensusSeries(ctx, stats.PeriodDaily, stats.CensusAllTypes, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first — the order a chart plots left to right — and only the days
	// that carry an event, because a day catlog was switched off is not a zero.
	if len(series) != 2 || series[0].Bucket != "2026-03-02" || series[1].Bucket != "2026-03-04" {
		t.Fatalf("daily series = %+v", series)
	}

	busiest, ok, err := proj.CensusBusiest(ctx, stats.PeriodDaily, stats.CensusAllTypes)
	if err != nil || !ok {
		t.Fatalf("busiest: ok=%v err=%v", ok, err)
	}
	if busiest.Bucket != "2026-03-02" || busiest.Count != 2 {
		t.Errorf("busiest day = %+v, want 2026-03-02 with 2", busiest)
	}

	days, err := proj.CensusBuckets(ctx, stats.PeriodDaily, stats.CensusAllTypes)
	if err != nil {
		t.Fatal(err)
	}
	if days != 2 {
		t.Errorf("days with an event = %d, want 2", days)
	}
}

// Nothing folded is not an error, and every helper has to say so rather than
// inventing a zeroth bucket.
func TestCensusOnAnEmptyLog(t *testing.T) {
	proj := testutil.MemProjections(t)
	ctx := t.Context()

	rows, err := proj.CensusWindow(ctx, stats.PeriodAllTime, "")
	if err != nil || len(rows) != 0 {
		t.Errorf("empty window = %v, %v", rows, err)
	}
	if _, ok, err := proj.CensusBusiest(ctx, stats.PeriodDaily, stats.CensusAllTypes); ok || err != nil {
		t.Errorf("busiest on an empty log = %v, %v", ok, err)
	}
	if n, err := proj.CensusBuckets(ctx, stats.PeriodDaily, stats.CensusAllTypes); n != 0 || err != nil {
		t.Errorf("buckets on an empty log = %d, %v", n, err)
	}
}
