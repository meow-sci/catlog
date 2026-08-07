package stats

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The rolling windows a board can be read over. `alltime` is the default and is
// `player_stat`; the other four live in `player_stat_period`.
const (
	PeriodAllTime = "alltime"
	PeriodDaily   = "daily"
	PeriodWeekly  = "weekly"
	PeriodMonthly = "monthly"
	PeriodYearly  = "yearly"
)

// rollingPeriods is every window a fold writes, in the order the API lists
// them. Adding one here gives every board — fixed and dynamic — that window,
// because the write goes through the four helpers in fold.go that every fold
// already uses. There is no registry to update and nothing to enumerate: a
// `fastest_to_<body>` board for a body nobody had heard of gets its rolling
// windows on the same event that creates the board.
var rollingPeriods = []string{PeriodDaily, PeriodWeekly, PeriodMonthly, PeriodYearly}

// RollingPeriods returns the windows a board is bucketed into.
func RollingPeriods() []string { return append([]string(nil), rollingPeriods...) }

// Periods returns every value `?period=` accepts, `alltime` first.
func Periods() []string { return append([]string{PeriodAllTime}, rollingPeriods...) }

// ValidPeriod reports whether p is a period the API serves. The empty string is
// `alltime`, so an absent parameter means what it always meant.
func ValidPeriod(p string) (string, bool) {
	if p == "" {
		return PeriodAllTime, true
	}
	for _, known := range Periods() {
		if p == known {
			return p, true
		}
	}
	return "", false
}

// Retention is how many buckets of each window are kept, newest first.
//
// These are listing horizons, not data loss in the sense that matters: the log
// is untouched and a rebuild reconstructs whatever the current numbers say to
// keep. They exist because Constitution §2 — cheap enough to forget about — has
// an opinion about a table whose row count is players × boards × buckets and
// where the bucket count grows forever. Roughly a quarter of daily, a year of
// weekly, three years of monthly, and yearly for as long as anyone will care.
var Retention = map[string]int{
	PeriodDaily:   90,
	PeriodWeekly:  53,
	PeriodMonthly: 36,
	PeriodYearly:  20,
}

// Bucket is the window key an event falls in, from the **server's** receive
// time in unix milliseconds.
//
// It is UTC, always. A leaderboard week has to mean the same thing to every
// reader, and the alternative — the viewer's local week — would make two people
// disagree about who won.
//
// The formats sort chronologically as plain strings, which is the property
// retention and pagination both lean on:
//
//	daily    2026-08-07
//	weekly   2026-W32   (ISO week-numbering year, so the last days of December
//	                     can legitimately read 2027-W01)
//	monthly  2026-08
//	yearly   2026
func Bucket(period string, recvMS int64) (string, bool) {
	t := time.UnixMilli(recvMS).UTC()
	switch period {
	case PeriodDaily:
		return t.Format("2006-01-02"), true
	case PeriodWeekly:
		year, week := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week), true
	case PeriodMonthly:
		return t.Format("2006-01"), true
	case PeriodYearly:
		return t.Format("2006"), true
	default:
		return "", false
	}
}

// CurrentBucket is the window a period is "now" in, for a caller that wants the
// live board without naming a bucket. now is unix milliseconds and comes from
// the server clock, never from the browser.
func CurrentBucket(period string, nowMS int64) (string, bool) { return Bucket(period, nowMS) }

// ParseBucket validates a `?at=` value against the shape its period requires,
// so a malformed window is a 400 rather than a silently empty board.
func ParseBucket(period, bucket string) bool {
	switch period {
	case PeriodDaily:
		_, err := time.Parse("2006-01-02", bucket)
		return err == nil
	case PeriodWeekly:
		year, week, ok := strings.Cut(bucket, "-W")
		if !ok || len(year) != 4 || len(week) != 2 {
			return false
		}
		y, err1 := strconv.Atoi(year)
		w, err2 := strconv.Atoi(week)
		return err1 == nil && err2 == nil && y >= 1970 && y <= 9999 && w >= 1 && w <= 53
	case PeriodMonthly:
		_, err := time.Parse("2006-01", bucket)
		return err == nil
	case PeriodYearly:
		y, err := strconv.Atoi(bucket)
		return err == nil && len(bucket) == 4 && y >= 1970 && y <= 9999
	default:
		return false
	}
}

// retentionCutoff is the oldest bucket a period keeps, given the newest one
// seen. Anything strictly below it is deleted.
//
// It is computed by walking calendar steps back from the event's own receive
// time rather than by subtracting a fixed number of milliseconds, because
// months and ISO weeks are not fixed-length and "36 months ago" has to mean 36
// months.
func retentionCutoff(period string, recvMS int64) (string, bool) {
	keep, ok := Retention[period]
	if !ok || keep <= 0 {
		return "", false
	}
	t := time.UnixMilli(recvMS).UTC()
	switch period {
	case PeriodDaily:
		t = t.AddDate(0, 0, -(keep - 1))
	case PeriodWeekly:
		t = t.AddDate(0, 0, -7*(keep-1))
	case PeriodMonthly:
		t = t.AddDate(0, -(keep - 1), 0)
	case PeriodYearly:
		t = t.AddDate(-(keep - 1), 0, 0)
	default:
		return "", false
	}
	return Bucket(period, t.UnixMilli())
}
