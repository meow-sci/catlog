// Package projector folds the event log into projections: the checkpoint loop,
// the fold registry, the full rebuild-and-swap and the feed broadcaster (§5.6).
//
// # The loop
//
// One goroutine wakes on the ingest writer's notify channel or a one-second
// ticker, reads up to a thousand `event` rows past its checkpoint, decodes them,
// applies every fold from package stats, and writes **every projection update
// and the checkpoint row in one projections.db transaction**. Feed rows are
// pushed to the broadcaster only after that transaction commits, so a subscriber
// can never see an item the database would forget.
//
// All folds share the single checkpoint `projection = 'all'` (§5.6). That is
// what makes the transaction meaningful: a partial batch cannot exist, because
// there is one cursor and it moves with the writes it accounts for.
//
// # Versions and the upcast registry
//
// Stored events are immutable forever, so the only way to change a payload shape
// is to bump `ver` and teach the projector to read the old one. [Upcasters] is
// that registry, keyed (type, ver). It is empty at launch — every §4.2 type is
// `ver: 1` — and exists now so the first bump is a registration rather than a
// migration. An event whose `ver` is higher than this build understands is
// skipped and logged once (§4.1); it is not an error, because the row is
// perfectly valid and a later build will fold it on the next rebuild.
//
// # Rebuild is the correctness backstop
//
// D22 makes the incremental fold fast and the rebuild correct. [Projector.Rebuild]
// builds a complete set of projections into `projections.rebuild.db` from seq 0,
// closes the live handle, renames the file into place and reopens — all under
// the RWMutex the read API holds while it queries.
//
// The rebuild is deliberately *two passes* over the log. The first applies only
// the flight-state fold and indexes every `kitten.kia` by flight and sim time;
// the second scores the boards against a flight_state that is already complete
// for the entire history. That is what heals the two cases the incremental path
// gets wrong by construction: a `flight.flagged` that arrives after the flight's
// scoring events, and the two §5.6 refinements (the ±2 s KIA window on
// `biggest_lithobrake_survived`, and `ended_reason == 'recovered'` on
// `peak_g_survived`).
package projector
