package stats_test

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// ms is the receive stamp for a UTC date, so a case can say which window it
// means instead of doing arithmetic in its head.
func ms(t *testing.T, iso string) int64 {
	t.Helper()
	when, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("bad test date %q: %v", iso, err)
	}
	return when.UnixMilli()
}

// readPeriods dumps player_stat_period as "player/stat/period/bucket" → value.
func readPeriods(t *testing.T, proj *store.Projections) map[string]float64 {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, stat, period, bucket, value FROM player_stat_period
		 ORDER BY player_id, stat, period, bucket`)
	if err != nil {
		t.Fatalf("read player_stat_period: %v", err)
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var (
			player               int64
			stat, period, bucket string
			value                float64
		)
		if err := rows.Scan(&player, &stat, &period, &bucket, &value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[fmt.Sprintf("%d/%s/%s/%s", player, stat, period, bucket)] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func foldPeriods(t *testing.T, in []input) map[string]float64 {
	t.Helper()
	proj := testutil.MemProjections(t)
	apply(t, proj, in, 0, false)
	return readPeriods(t, proj)
}

// --- the bucket function, which needs no database ------------------------------

// TestBucketFormatsSortChronologically is the property retention and paging both
// lean on: every window key is comparable as plain text, so `bucket < cutoff` is
// a valid age test and no SQL has to know what a week is.
func TestBucketFormatsSortChronologically(t *testing.T) {
	for _, period := range stats.RollingPeriods() {
		var keys []string
		for _, iso := range []string{
			"2025-12-28T00:00:00Z", "2026-01-01T00:00:00Z", "2026-03-15T00:00:00Z",
			"2026-08-07T12:00:00Z", "2027-02-01T00:00:00Z",
		} {
			b, ok := stats.Bucket(period, ms(t, iso))
			if !ok {
				t.Fatalf("Bucket(%q) refused a valid period", period)
			}
			keys = append(keys, b)
		}
		if !slices.IsSorted(keys) {
			t.Errorf("%s buckets do not sort chronologically as strings: %v", period, keys)
		}
	}
}

// TestBucketShapes pins the exact wire form, because `?at=` takes it verbatim.
func TestBucketShapes(t *testing.T) {
	at := ms(t, "2026-08-07T13:45:00Z")
	for period, want := range map[string]string{
		stats.PeriodDaily:   "2026-08-07",
		stats.PeriodWeekly:  "2026-W32",
		stats.PeriodMonthly: "2026-08",
		stats.PeriodYearly:  "2026",
	} {
		got, ok := stats.Bucket(period, at)
		if !ok || got != want {
			t.Errorf("Bucket(%q) = %q (ok=%v), want %q", period, got, ok, want)
		}
		if !stats.ParseBucket(period, got) {
			t.Errorf("ParseBucket rejected the key Bucket produced: %s %q", period, got)
		}
	}
	if _, ok := stats.Bucket("fortnightly", at); ok {
		t.Error("Bucket accepted a period that does not exist")
	}
}

// TestWeeklyBucketUsesTheISOWeekYear: the last days of December belong to the
// next year's week 1, and a naive Format("2006") would file them under the wrong
// year and split one week across two buckets.
func TestWeeklyBucketUsesTheISOWeekYear(t *testing.T) {
	got, _ := stats.Bucket(stats.PeriodWeekly, ms(t, "2025-12-31T00:00:00Z"))
	if got != "2026-W01" {
		t.Errorf("31 Dec 2025 is in %q, want 2026-W01 — ISO week-numbering year", got)
	}
}

// TestValidPeriodTreatsAbsenceAsAllTime keeps every existing URL working: no
// `?period=` means what it has always meant.
func TestValidPeriodTreatsAbsenceAsAllTime(t *testing.T) {
	if p, ok := stats.ValidPeriod(""); !ok || p != stats.PeriodAllTime {
		t.Errorf("ValidPeriod(\"\") = %q (ok=%v), want alltime", p, ok)
	}
	if _, ok := stats.ValidPeriod("hourly"); ok {
		t.Error("ValidPeriod accepted a window that does not exist")
	}
}

// --- the folds -----------------------------------------------------------------

// TestPeriodBucketsComeFromRecvTime is the load-bearing property of the whole
// design. Two RUDs a month apart must land in different windows, and they do so
// because the fold reads the event's server receive stamp — not the wall clock,
// which during a rebuild is years away from when the event happened.
func TestPeriodBucketsComeFromRecvTime(t *testing.T) {
	f := flightN(1)
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "ground_impact"}, recvMS: ms(t, "2026-07-04T10:00:00Z")},
		{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "ground_impact"}, recvMS: ms(t, "2026-08-07T10:00:00Z")},
	})

	for key, want := range map[string]float64{
		"1/rud_total/daily/2026-07-04":    1,
		"1/rud_total/daily/2026-08-07":    1,
		"1/rud_total/monthly/2026-07":     1,
		"1/rud_total/monthly/2026-08":     1,
		"1/rud_total/yearly/2026":         2,
		"1/rud_ground_impact/yearly/2026": 2,
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

// TestPeriodCountersAccumulateWithinAWindow: a counter's weekly value is not
// derivable from its lifetime total, which is the reason this table exists.
func TestPeriodCountersAccumulateWithinAWindow(t *testing.T) {
	f := flightN(1)
	day := ms(t, "2026-08-07T09:00:00Z")
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}, recvMS: day},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 2}, recvMS: day + 3600_000},
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 3}, recvMS: day + 7200_000},
	})
	if got["1/stagings/daily/2026-08-07"] != 3 {
		t.Errorf("stagings that day = %v, want 3", got["1/stagings/daily/2026-08-07"])
	}
	if got["1/stagings/weekly/2026-W32"] != 3 {
		t.Errorf("stagings that week = %v, want 3", got["1/stagings/weekly/2026-W32"])
	}
}

func TestPartsLostPeriodsSumAndKeepTheBiggestLoss(t *testing.T) {
	f := flightN(1)
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "collision", PartCount: 7, CrewCount: 3}, recvMS: ms(t, "2026-08-07T09:00:00Z")},
		{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "collision", PartCount: 4, CrewCount: 1}, recvMS: ms(t, "2026-08-07T10:00:00Z")},
		{flight: f, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "collision", PartCount: 12, CrewCount: 5}, recvMS: ms(t, "2026-09-02T09:00:00Z")},
	})
	for key, want := range map[string]float64{
		"1/parts_lost/daily/2026-08-07":         11,
		"1/biggest_parts_lost/daily/2026-08-07": 7,
		"1/parts_lost/yearly/2026":              23,
		"1/biggest_parts_lost/yearly/2026":      12,
		"1/kittens_wrecked/daily/2026-08-07":    4,
		"1/biggest_crew_wreck/daily/2026-08-07": 3,
		"1/kittens_wrecked/yearly/2026":         9,
		"1/biggest_crew_wreck/yearly/2026":      5,
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

func TestBodySprintPeriodsTrackGrowthOfTheBestSave(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	august := ms(t, "2026-08-07T09:00:00Z")
	september := ms(t, "2026-09-02T09:00:00Z")
	got := foldPeriods(t, []input{
		{career: defaultCareer, flight: f1, typ: "vehicle.soi", simT: 1, recvMS: august, payload: stats.VehicleSOI{ToBody: "a-one"}},
		{career: defaultCareer, flight: f1, typ: "vehicle.soi", simT: 2, recvMS: august + 1, payload: stats.VehicleSOI{ToBody: "a-two"}},
		// The second save does not move the player maximum until its third body.
		{career: otherCareer, flight: f2, typ: "vehicle.soi", simT: 1, recvMS: august + 2, payload: stats.VehicleSOI{ToBody: "b-one"}},
		{career: otherCareer, flight: f2, typ: "vehicle.soi", simT: 2, recvMS: september, payload: stats.VehicleSOI{ToBody: "b-two"}},
		{career: otherCareer, flight: f2, typ: "vehicle.soi", simT: 3, recvMS: september + 1, payload: stats.VehicleSOI{ToBody: "b-three"}},
	})
	for _, stat := range []string{stats.StatBodiesBy1Y, stats.StatBodiesBy10Y} {
		for key, want := range map[string]float64{
			"1/" + stat + "/daily/2026-08-07": 2,
			"1/" + stat + "/daily/2026-09-02": 1,
			"1/" + stat + "/yearly/2026":      3,
		} {
			if got[key] != want {
				t.Errorf("%s = %v, want %v", key, got[key], want)
			}
		}
	}
}

// TestPeriodRecordsKeepTheBestInEachWindow: a record board's window value is the
// best achieved *inside* that window, so a later worse attempt does not lower it
// and a later better one does raise it.
func TestPeriodRecordsKeepTheBestInEachWindow(t *testing.T) {
	f := flightN(1)
	day := ms(t, "2026-08-07T09:00:00Z")
	impact := func(speed float64, at int64) input {
		return input{flight: f, typ: "vehicle.impact", recvMS: at,
			payload: stats.VehicleImpact{SpeedMs: speed, Survived: true, CrewCount: 1}}
	}
	got := foldPeriods(t, []input{
		impact(40, day),
		impact(120, day+3600_000),
		impact(60, day+7200_000),
		impact(15, ms(t, "2026-08-20T09:00:00Z")), // a different day and month-mate
	})

	if v := got["1/biggest_lithobrake_survived/daily/2026-08-07"]; v != 120 {
		t.Errorf("best that day = %v, want 120", v)
	}
	if v := got["1/biggest_lithobrake_survived/daily/2026-08-20"]; v != 15 {
		t.Errorf("best on the later day = %v, want 15 — each window keeps its own best", v)
	}
	if v := got["1/biggest_lithobrake_survived/monthly/2026-08"]; v != 120 {
		t.Errorf("best that month = %v, want 120", v)
	}
}

// TestCareerBoardsGetWindowsToo, and in milliseconds: the ascending boards use
// the min-per-window mirror, and their unit is the one the projection publishes.
func TestCareerBoardsGetWindowsToo(t *testing.T) {
	f := flightN(1)
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.orbit", simT: 300,
			payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, recvMS: ms(t, "2026-08-07T09:00:00Z")},
		{flight: f, career: "othercareer00001", typ: "vehicle.orbit", simT: 120,
			payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, recvMS: ms(t, "2026-08-07T11:00:00Z")},
	})
	if v := got["1/fastest_to_orbit/daily/2026-08-07"]; v != 120_000 {
		t.Errorf("fastest that day = %v, want 120000 ms — the window keeps the minimum", v)
	}
}

// TestDynamicBoardsGetTheirWindowsForFree is requirement one of the design: a
// board whose key came out of the event stream must get its rolling windows on
// the very event that creates it, with no registry anywhere to update.
func TestDynamicBoardsGetTheirWindowsForFree(t *testing.T) {
	f := flightN(1)
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.soi", simT: 900,
			payload: stats.VehicleSOI{FromBody: "earth", ToBody: "zephyria_prime"},
			recvMS:  ms(t, "2026-08-07T09:00:00Z")},
	})
	if v := got["1/fastest_to_zephyria_prime/monthly/2026-08"]; v != 900_000 {
		t.Errorf("a body nobody listed = %v, want 900000 ms in its own monthly window", v)
	}
}

// TestAnEventWithNoRecvTimeWritesNoWindows: a row whose window cannot be
// determined belongs in no window. The all-time board still has it.
func TestAnEventWithNoRecvTimeWritesNoWindows(t *testing.T) {
	f := flightN(1)
	got := foldPeriods(t, []input{
		{flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}, recvMS: -1},
	})
	if len(got) != 0 {
		t.Errorf("an event with no receive stamp wrote %d windows, want 0: %v", len(got), got)
	}
}

// TestRebuildEqualsIncrementalForPeriods is the same guarantee the all-time
// boards have, and the reason buckets may never come from time.Now(): replaying
// a history must land every row in the window the incremental fold put it in.
func TestRebuildEqualsIncrementalForPeriods(t *testing.T) {
	f1, f2 := flightN(1), flightN(2)
	history := []input{
		{flight: f1, typ: "vehicle.rud", payload: stats.VehicleRUD{Cause: "ground_impact"}, recvMS: ms(t, "2026-01-02T10:00:00Z")},
		{flight: f1, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1}, recvMS: ms(t, "2026-01-02T11:00:00Z")},
		{flight: f2, typ: "vehicle.impact", payload: stats.VehicleImpact{SpeedMs: 88, Survived: true, CrewCount: 2}, recvMS: ms(t, "2026-03-09T10:00:00Z")},
		{flight: f2, typ: "vehicle.orbit", simT: 450, payload: stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, recvMS: ms(t, "2026-03-09T12:00:00Z")},
		{flight: f2, typ: "vehicle.soi", simT: 700, payload: stats.VehicleSOI{FromBody: "earth", ToBody: "luna"}, recvMS: ms(t, "2026-12-31T23:00:00Z")},
	}

	incremental := testutil.MemProjections(t)
	for i := range history {
		apply(t, incremental, history[i:i+1], int64(i), false)
	}

	rebuilt := testutil.MemProjections(t)
	apply(t, rebuilt, history, 0, true)

	got, want := readPeriods(t, incremental), readPeriods(t, rebuilt)
	if len(got) == 0 {
		t.Fatal("the incremental pass wrote no windows at all")
	}
	for _, key := range slices.Sorted(maps.Keys(want)) {
		if got[key] != want[key] {
			t.Errorf("%s: incremental %v, rebuilt %v", key, got[key], want[key])
		}
	}
	for _, key := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[key]; !ok {
			t.Errorf("%s exists incrementally (%v) and not after a rebuild", key, got[key])
		}
	}
}

// TestRetentionDropsWindowsThatHaveAgedOut, and does so deterministically: the
// trim is gated on the event's sequence number, so a rebuild trims at exactly
// the same points as the incremental path.
func TestRetentionDropsWindowsThatHaveAgedOut(t *testing.T) {
	proj := testutil.MemProjections(t)
	f := flightN(1)

	// One event a day for long enough to push the first days past the daily
	// horizon, ending on a sequence number that triggers a trim.
	var history []input
	day := ms(t, "2024-01-01T00:00:00Z")
	for i := range 512 {
		history = append(history, input{
			flight: f, typ: "vehicle.staging", payload: stats.VehicleStaging{StageIndex: 1},
			recvMS: day + int64(i)*24*3600_000,
		})
	}
	apply(t, proj, history, 0, false)

	got := readPeriods(t, proj)
	// Only this board's windows: vehicle.staging also feeds `most_stages`, and
	// each board is trimmed to the horizon on its own.
	var daily []string
	for key := range got {
		if k := key; len(k) > 0 && contains(k, "/stagings/daily/") {
			daily = append(daily, k)
		}
	}
	keep := stats.Retention[stats.PeriodDaily]
	if len(daily) == 0 {
		t.Fatal("no daily windows survived at all")
	}
	if len(daily) > keep {
		t.Errorf("%d daily windows kept, want at most %d", len(daily), keep)
	}
	// And the ones kept are the newest: the very first day must be gone.
	if _, ok := got["1/stagings/daily/2024-01-01"]; ok {
		t.Error("the oldest daily window survived retention")
	}
	// Yearly is far inside its horizon and must be untouched.
	if got["1/stagings/yearly/2024"] == 0 {
		t.Error("the yearly window was trimmed; it is nowhere near its horizon")
	}
}
