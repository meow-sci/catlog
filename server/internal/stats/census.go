package stats

import "context"

// CensusAllTypes is the `type` value of the row that counts **every** type.
//
// The empty string, because it is the one type name no event can have. It is
// stored rather than derived so that "how many events are there" is a point
// lookup instead of a group-by over every type there has ever been — and so
// that a type this build cannot name still lands in the total.
const CensusAllTypes = ""

// censusFold counts the log (see migrations/projections/0004_census.sql).
//
// It is the only fold that is not about a player. Every other one asks what
// somebody did; this one asks what catlog *has* — how many events, of what
// kinds, arriving how fast, since when. That makes it the one fold with no
// flag exclusion and no handle requirement: a flagged flight's telemetry is
// still telemetry catlog is storing, and hiding it here would make the census
// disagree with the log it is a census of.
//
// It writes ten rows per event — two type keys (its own, and [CensusAllTypes])
// across five periods — and every one of them merges in the [Batch], so a
// batch of five hundred events from one afternoon flushes ten rows rather than
// five thousand.
type censusFold struct{}

func (censusFold) Name() string { return "census" }

func (censusFold) Apply(_ context.Context, b *Batch, ev Event) error {
	b.countEvent(CensusAllTypes, PeriodAllTime, "", ev)
	b.countEvent(ev.Type, PeriodAllTime, "", ev)

	// An event with no receive stamp lands in no window — the same answer
	// eachPeriod gives, and for the same reason: a row whose window nobody can
	// determine belongs in none of them. The all-time rows above still have it,
	// so the total never disagrees with the log.
	if ev.RecvTime <= 0 {
		return nil
	}
	for i, bucket := range b.bucketsFor(ev.RecvTime) {
		if bucket == "" {
			continue
		}
		b.countEvent(CensusAllTypes, rollingPeriods[i], bucket, ev)
		b.countEvent(ev.Type, rollingPeriods[i], bucket, ev)
	}
	return nil
}
