// Package stats holds the per-board fold implementations and the board metadata
// for the launch leaderboards (§5.6).
//
// # The shape of a fold
//
// A [Fold] turns one decoded event into projection writes. The projector
// (package projector) reads a batch of events past its checkpoint and applies
// every registered fold to every event inside a single projections.db
// transaction, so a fold never has to think about durability, ordering or
// partial application — it either sees the whole batch commit or none of it.
//
// [FlightFold] is itself a fold and is always applied first, because every other
// fold asks the flight state whether the event's flight has been flagged.
//
// # Why the SQL lives here rather than in package store
//
// store owns the read side of projections.db (leaderboards, profiles, the feed)
// because those queries belong next to the schema and are shared with the read
// API. The *write* side is fold rules expressed as SQL: `INSERT … ON CONFLICT …
// WHERE excluded.value > player_stat.value` is not a query, it is the
// "records keep the earlier of two equal values" rule. Splitting the rule from
// its statement would put the interesting half of every board in one file and
// its meaning in another, so the writes stay here.
//
// # Two rules every board obeys
//
//   - Any event whose flight carries a flag bit is skipped. §5.6 scopes this to
//     "record" folds; catlog applies it to counters as well — see
//     docs/DECISIONS.md, and [FlagTuning] for the case that forces it.
//   - Ties keep the earliest updated_seq. A record is only replaced by a
//     strictly larger value, and a counter's updated_seq is the seq at which it
//     reached its current count, so whoever got there first outranks whoever
//     arrived later at the same number.
//
// # Rebuild refinements
//
// Two boards cannot be computed exactly by a single forward pass, because they
// depend on events that arrive after the event being scored (§5.6):
// `biggest_lithobrake_survived` needs the ±2 s `kitten.kia` window of §4.2, and
// `peak_g_survived` needs the flight's eventual `ended_reason`. The incremental
// path scores them optimistically; a rebuild sets [FlightStateReader.Refined]
// and applies both rules, which is what makes rebuild the correctness backstop
// D22 asks for.
package stats
