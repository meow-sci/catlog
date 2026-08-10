package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// A **Batch** is one projector batch's worth of projection writes, held in
// memory and flushed as a handful of multi-row statements (§5.6).
//
// # Why this exists
//
// It is an optimisation, and it is the difference between a million-event
// projection taking five minutes and taking twenty seconds. Measured on an M4
// Pro, one tursogo statement costs **14–18 µs** end to end — an FFI transition
// into the engine, a prepare, a bind per parameter and an execute — against
// roughly 5 µs per row when the same rows arrive inside one multi-row
// statement. That number is the whole performance story of the projector:
// folding an event the direct way issued about **21 statements**, so an event
// cost ~385 µs and nothing else was close.
//
// Those 21 divide into three kinds of waste, all of which this type removes:
//
//   - **Repeated reads.** Every board fold asked `flight_state` whether the
//     flight was flagged, so one telemetry window ran the same SELECT four
//     times. They are answered from a read-through cache now.
//   - **Repeated writes to the same key.** Twenty telemetry windows from one
//     player in one batch each wrote `fastest_orbital_speed`. Only the largest
//     could ever survive, so the other nineteen were pure cost. The merge rules
//     below apply in memory and one row is written.
//   - **Row-at-a-time statements.** Every board value fans out into four
//     rolling windows (period.go), so the window writes alone were 12 of the 21.
//     They leave as one statement per kind per flush.
//
// # Why it is not concurrent
//
// Turso, like SQLite, has one writer, and the ~5 µs per row that survives
// batching is real work inside the engine rather than overhead a second
// goroutine could overlap with. Parallelism belongs upstream of here, where
// decoding payloads is pure CPU (see the projector's decode fan-out); a batch
// is owned by the one goroutine folding into it and does no locking.
//
// # What it does not change
//
// Every merge rule below is the in-memory spelling of the SQL it replaces,
// including the tie-breaks: a record board keeps the earlier `updated_seq` on
// an equal value because the merge replaces only on a *strictly* greater one,
// exactly as `WHERE excluded.value > player_stat.value` did. Reads see this
// batch's own writes, so a fold still observes the flag another fold set for
// the very same event. And the flush is key-sorted, so a batch produces the
// same statements in the same order every time — which is what keeps a rebuild
// comparable to the incremental result column for column.
type Batch struct {
	tx *sql.Tx

	// flushRows is the row cap on one flushed statement.
	flushRows int

	// The rebuild refinements, unchanged from §5.6: `refined` turns on the
	// exact rules and `kia` indexes kitten.kia by flight and sim time.
	refined bool
	kia     map[ids.ID][]float64

	// Read-through caches. An entry is loaded on first touch and survives a
	// flush, because after one it holds exactly what the database holds.
	flights map[ids.ID]*flightEntry
	careers map[careerKey]*careerEntry
	// careerOrdinals is the next-save sequence high-water per player. It is
	// loaded once per player per batch and advanced as new careers are first
	// seen, so several saves created in one batch receive distinct ordinals.
	careerOrdinals map[int64]int64
	bodies         map[int64]map[bodyKey]*bodyEntry
	careerBodies   map[int64]map[careerBodyKey]*careerBodyEntry
	kittens        map[int64]map[string]*kittenEntry
	careerKittens  map[int64]map[careerKittenKey]*careerKittenEntry
	// values caches `player_stat.value` for the one helper that has to read it
	// back (setValue, for its window delta).
	values       map[statKey]float64
	careerValues map[careerStatKey]float64

	// Write accumulators, one map per rule. Cleared by every flush. Indexed by
	// [statKind] rather than carrying it in the value, so the flush walks one
	// kind at a time — each kind's ON CONFLICT clause is a different statement.
	stats       [numStatKinds]map[statKey]*pendingStat
	careerStats [numStatKinds]map[careerStatKey]*pendingCareerStat
	systemStats [numStatKinds]map[systemStatKey]*pendingStat
	periods     [numStatKinds]map[periodKey]*pendingStat
	// census is the event count per (type, period, bucket). One map rather than
	// one per kind: there is only one rule — add — and the flush is one
	// statement.
	census map[censusKey]*pendingCensus

	// bucketMS/bucketKeys memoise [Bucket] for one receive time. Every board
	// write asks for the same four window keys, and a batch's events mostly
	// share a receive stamp — they arrived in one ingest — so a single-entry
	// memo turns a `time.Format` per period per board write per event into one
	// per distinct receive time.
	bucketMS   int64
	bucketOK   bool
	bucketKeys []string

	// trimmedSeq is the last seq whose retention trim has run, so several board
	// writes on one event share a trim rather than each paying for one.
	trimmedSeq int64

	// dirty flights/careers/bodies/kittens, so a flush walks what changed
	// rather than everything the batch has ever read.
	dirtyFlights       []ids.ID
	dirtyCareers       []careerKey
	dirtyBodies        []playerBodyKey
	dirtyCareerBodies  []playerCareerBodyKey
	dirtyKittens       []playerKittenKey
	dirtyCareerKittens []playerCareerKittenKey

	// Scratch for the sorted key lists a flush builds. Reused rather than
	// reallocated: a drain flushes once per batch forever, and a fresh slice of
	// every pending key each time was the largest allocation the batch made
	// that was not the driver's.
	statKeys       []statKey
	careerStatKeys []careerStatKey
	systemStatKeys []systemStatKey
	periodKeys     []periodKey
	censusKeys     []censusKey
	args           []any
}

// DefaultFlushRows is how many rows one flushed statement carries. Beyond a
// few hundred the per-row cost is flat (5.5 µs at 50 rows, 5.2 µs at 200), so
// this is chosen for a comfortable parameter count rather than for speed: the
// widest row here binds seven columns, so a flush stays under 3,500 bound
// parameters.
const DefaultFlushRows = 500

// BatchOptions configures [NewBatch].
type BatchOptions struct {
	// FlushRows overrides [DefaultFlushRows].
	FlushRows int
}

// NewBatch opens an incremental batch over tx.
func NewBatch(tx *sql.Tx, opts BatchOptions) *Batch {
	b := &Batch{
		tx:             tx,
		flushRows:      opts.FlushRows,
		flights:        map[ids.ID]*flightEntry{},
		careers:        map[careerKey]*careerEntry{},
		careerOrdinals: map[int64]int64{},
		bodies:         map[int64]map[bodyKey]*bodyEntry{},
		careerBodies:   map[int64]map[careerBodyKey]*careerBodyEntry{},
		kittens:        map[int64]map[string]*kittenEntry{},
		careerKittens:  map[int64]map[careerKittenKey]*careerKittenEntry{},
		values:         map[statKey]float64{},
		careerValues:   map[careerStatKey]float64{},
		census:         map[censusKey]*pendingCensus{},
	}
	for k := range b.stats {
		b.stats[k] = map[statKey]*pendingStat{}
		b.careerStats[k] = map[careerStatKey]*pendingCareerStat{}
		b.systemStats[k] = map[systemStatKey]*pendingStat{}
		b.periods[k] = map[periodKey]*pendingStat{}
	}
	if b.flushRows <= 0 {
		b.flushRows = DefaultFlushRows
	}
	return b
}

// NewRefinedBatch opens the rebuild's batch: `flight_state` is already complete
// for the whole history, and kia indexes every kitten.kia by flight and sim
// time (§5.6).
func NewRefinedBatch(tx *sql.Tx, kia map[ids.ID][]float64, opts BatchOptions) *Batch {
	b := NewBatch(tx, opts)
	b.refined, b.kia = true, kia
	return b
}

// Tx is the transaction the batch flushes into. The projector needs it for the
// writes that are not projections — the checkpoint, the feed cap — which share
// the commit so a batch is still all-or-nothing.
func (b *Batch) Tx() *sql.Tx { return b.tx }

// Refined implements [FlightStateReader].
func (b *Batch) Refined() bool { return b.refined }

// KIANear implements [FlightStateReader]: §4.2's ±2 s crew-survival window.
func (b *Batch) KIANear(flight ids.ID, simT float64) bool {
	for _, t := range b.kia[flight] {
		if d := t - simT; d <= KIAWindowSeconds && -d <= KIAWindowSeconds {
			return true
		}
	}
	return false
}

// bucketsFor is the rolling-window key an event falls in, per period, aligned
// with rollingPeriods. An empty string means the period has no bucket, which
// [eachPeriod] skips.
func (b *Batch) bucketsFor(recvMS int64) []string {
	if b.bucketOK && b.bucketMS == recvMS {
		return b.bucketKeys
	}
	if b.bucketKeys == nil {
		b.bucketKeys = make([]string, len(rollingPeriods))
	}
	for i, period := range rollingPeriods {
		bucket, ok := Bucket(period, recvMS)
		if !ok {
			bucket = ""
		}
		b.bucketKeys[i] = bucket
	}
	b.bucketMS, b.bucketOK = recvMS, true
	return b.bucketKeys
}

// --- flight_state -------------------------------------------------------------

type flightEntry struct {
	playerID   int64
	flags      int64
	reason     sql.NullString
	crew       sql.NullInt64
	body       sql.NullString
	startedSeq int64

	// exists reports that a row exists — in the database, or pending here.
	exists bool
	dirty  bool
}

func (e *flightEntry) state(id ids.ID) FlightState {
	return FlightState{
		FlightID: id, PlayerID: e.playerID, Flags: e.flags,
		EndedReason: e.reason.String, Crew: e.crew, Body: e.body.String,
		StartedSeq: e.startedSeq,
	}
}

// Flight implements [FlightStateReader], answering from the cache.
func (b *Batch) Flight(ctx context.Context, id ids.ID) (FlightState, bool, error) {
	e, err := b.flightEntry(ctx, id)
	if err != nil || !e.exists {
		return FlightState{}, false, err
	}
	return e.state(id), true, nil
}

func (b *Batch) flightEntry(ctx context.Context, id ids.ID) (*flightEntry, error) {
	if e, ok := b.flights[id]; ok {
		return e, nil
	}
	e := &flightEntry{}
	err := b.tx.QueryRowContext(ctx,
		`SELECT player_id, flags, ended_reason, crew, body, started_seq FROM flight_state WHERE flight_id = ?`,
		ids.Bytes(id)).Scan(&e.playerID, &e.flags, &e.reason, &e.crew, &e.body, &e.startedSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("stats: read flight %s: %w", ids.String(id), err)
	default:
		e.exists = true
	}
	b.flights[id] = e
	return e, nil
}

func (b *Batch) touchFlight(id ids.ID, e *flightEntry) {
	if !e.dirty {
		e.dirty = true
		b.dirtyFlights = append(b.dirtyFlights, id)
	}
}

// EnsureFlight creates the flight's row if it has none, standing in for the
// `INSERT OR IGNORE` every flight-bearing event ran.
func (b *Batch) EnsureFlight(ctx context.Context, id ids.ID, playerID, seq int64) error {
	e, err := b.flightEntry(ctx, id)
	if err != nil {
		return err
	}
	if !e.exists {
		e.exists = true
		e.playerID, e.flags, e.startedSeq = playerID, 0, seq
		b.touchFlight(id, e)
	}
	return nil
}

// StartFlight records `flight.started`.
func (b *Batch) StartFlight(ctx context.Context, id ids.ID, crew int, body string, seq int64) error {
	e, err := b.flightEntry(ctx, id)
	if err != nil {
		return err
	}
	e.crew = sql.NullInt64{Int64: int64(crew), Valid: true}
	e.body = sql.NullString{String: body, Valid: true}
	e.startedSeq = seq
	b.touchFlight(id, e)
	return nil
}

// EndFlight records `flight.ended`.
func (b *Batch) EndFlight(ctx context.Context, id ids.ID, reason string) error {
	e, err := b.flightEntry(ctx, id)
	if err != nil {
		return err
	}
	e.reason = sql.NullString{String: reason, Valid: true}
	b.touchFlight(id, e)
	return nil
}

// FlagFlight sets a bit of `flight_state.flags`.
func (b *Batch) FlagFlight(ctx context.Context, id ids.ID, bit int64) error {
	e, err := b.flightEntry(ctx, id)
	if err != nil {
		return err
	}
	e.flags |= bit
	b.touchFlight(id, e)
	return nil
}

func (b *Batch) flushFlights(ctx context.Context) error {
	if len(b.dirtyFlights) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyFlights, func(x, y ids.ID) int { return strings.Compare(string(x[:]), string(y[:])) })
	err := b.write(ctx, len(b.dirtyFlights), 7,
		`INSERT INTO flight_state (flight_id, player_id, flags, ended_reason, crew, body, started_seq) VALUES `,
		` ON CONFLICT (flight_id) DO UPDATE SET
		   flags = excluded.flags, ended_reason = excluded.ended_reason,
		   crew = excluded.crew, body = excluded.body, started_seq = excluded.started_seq`,
		func(i int, args []any) []any {
			id := b.dirtyFlights[i]
			e := b.flights[id]
			e.dirty = false
			return append(args, ids.Bytes(id), e.playerID, e.flags, e.reason, e.crew, e.body, e.startedSeq)
		})
	if err != nil {
		return fmt.Errorf("stats: flush flight_state: %w", err)
	}
	b.dirtyFlights = b.dirtyFlights[:0]
	return nil
}

// --- career -------------------------------------------------------------------

type careerKey struct {
	playerID int64
	career   string
}

type careerEntry struct {
	maxSimT  float64
	rewound  bool
	firstSeq int64
	lastSeq  int64
	ordinal  int64
	system   string
	exists   bool
	dirty    bool
}

func (b *Batch) careerEntry(ctx context.Context, k careerKey) (*careerEntry, error) {
	if e, ok := b.careers[k]; ok {
		return e, nil
	}
	e := &careerEntry{}
	var rewound int64
	err := b.tx.QueryRowContext(ctx,
		`SELECT max_sim_t, rewound, first_seq, last_seq, ordinal, system FROM career WHERE player_id = ? AND career = ?`,
		k.playerID, k.career).Scan(&e.maxSimT, &rewound, &e.firstSeq, &e.lastSeq, &e.ordinal, &e.system)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("stats: read career %q: %w", k.career, err)
	default:
		e.exists, e.rewound = true, rewound != 0
	}
	b.careers[k] = e
	return e, nil
}

func (b *Batch) touchCareer(k careerKey, e *careerEntry) {
	if !e.dirty {
		e.dirty = true
		b.dirtyCareers = append(b.dirtyCareers, k)
	}
}

// nextOrdinal is the sequence number the next new save of this player takes.
//
// Read once per player per batch from the table, then advanced in memory, so a
// batch that first sees three of a player's saves numbers them 1, 2, 3 in seq
// order rather than three times 1.
//
// Replay-stable by construction: careers are first seen in ascending seq order
// on both the incremental and the rebuild path, so the same save gets the same
// number every time. That is what lets the ordinal be a URL segment.
func (b *Batch) nextOrdinal(ctx context.Context, playerID int64) (int64, error) {
	if n, ok := b.careerOrdinals[playerID]; ok {
		b.careerOrdinals[playerID] = n + 1
		return n + 1, nil
	}
	var high int64
	err := b.tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(ordinal), 0) FROM career WHERE player_id = ?`, playerID).Scan(&high)
	if err != nil {
		return 0, fmt.Errorf("stats: read career ordinal high-water for %d: %w", playerID, err)
	}
	b.careerOrdinals[playerID] = high + 1
	return high + 1, nil
}

// EnsureCareer creates the career's row if it has none and records this event
// as its latest activity, even when the event has no clock reading or score.
func (b *Batch) EnsureCareer(ctx context.Context, playerID int64, career string, seq int64) error {
	k := careerKey{playerID, career}
	e, err := b.careerEntry(ctx, k)
	if err != nil {
		return err
	}
	if !e.exists {
		ordinal, err := b.nextOrdinal(ctx, playerID)
		if err != nil {
			return err
		}
		e.exists, e.maxSimT, e.rewound, e.firstSeq = true, 0, false, seq
		e.ordinal = ordinal
	}
	e.lastSeq = seq
	b.touchCareer(k, e)
	return nil
}

// MarkRewound sets the rewind mark when simT is below the career's high-water
// mark. Like the UPDATE it replaces, it only ever marks a career that already
// exists — a career first seen by this very event cannot have been rewound.
func (b *Batch) MarkRewound(ctx context.Context, playerID int64, career string, simT float64) error {
	k := careerKey{playerID, career}
	e, err := b.careerEntry(ctx, k)
	if err != nil {
		return err
	}
	if e.exists && !e.rewound && e.maxSimT > simT {
		e.rewound = true
		b.touchCareer(k, e)
	}
	return nil
}

// AdvanceCareer raises the career's high-water mark, creating the row if it has
// none, and always records this event as the career's latest activity.
func (b *Batch) AdvanceCareer(ctx context.Context, playerID int64, career string, simT float64, seq int64) error {
	k := careerKey{playerID, career}
	e, err := b.careerEntry(ctx, k)
	if err != nil {
		return err
	}
	switch {
	case !e.exists:
		ordinal, err := b.nextOrdinal(ctx, playerID)
		if err != nil {
			return err
		}
		e.exists, e.maxSimT, e.rewound, e.firstSeq = true, simT, false, seq
		e.ordinal = ordinal
	case simT > e.maxSimT:
		e.maxSimT = simT
	}
	e.lastSeq = seq
	b.touchCareer(k, e)
	return nil
}

func (b *Batch) flushCareers(ctx context.Context) error {
	if len(b.dirtyCareers) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyCareers, compareCareerKey)
	err := b.write(ctx, len(b.dirtyCareers), 7,
		`INSERT INTO career (player_id, career, max_sim_t, rewound, first_seq, last_seq, ordinal) VALUES `,
		` ON CONFLICT (player_id, career) DO UPDATE SET
		   max_sim_t = excluded.max_sim_t, rewound = excluded.rewound, last_seq = excluded.last_seq`,
		func(i int, args []any) []any {
			k := b.dirtyCareers[i]
			e := b.careers[k]
			e.dirty = false
			return append(args, k.playerID, k.career, e.maxSimT, boolInt(e.rewound), e.firstSeq, e.lastSeq, e.ordinal)
		})
	if err != nil {
		return fmt.Errorf("stats: flush career: %w", err)
	}
	b.dirtyCareers = b.dirtyCareers[:0]
	return nil
}

// Career reports a career's state, for the folds that need the high-water mark.
func (b *Batch) Career(ctx context.Context, playerID int64, career string) (CareerState, bool, error) {
	e, err := b.careerEntry(ctx, careerKey{playerID, career})
	if err != nil || !e.exists {
		return CareerState{}, false, err
	}
	return CareerState{
		PlayerID: playerID, Career: career,
		MaxSimT: e.maxSimT, Rewound: e.rewound, FirstSeq: e.firstSeq, LastSeq: e.lastSeq,
		Ordinal: e.ordinal,
	}, true, nil
}

// CareerSystem reads the system a save is playing in, answering from this
// batch's own pending writes first. "" means not yet known.
func (b *Batch) CareerSystem(ctx context.Context, playerID int64, career string) (string, error) {
	e, err := b.careerEntry(ctx, careerKey{playerID, career})
	if err != nil || !e.exists {
		return "", err
	}
	return e.system, nil
}

// --- player_body ---------------------------------------------------------------

type bodyKey struct{ kind, body string }

type playerBodyKey struct {
	playerID int64
	bodyKey
}

type bodyEntry struct {
	firstSeq  int64
	firstSimT sql.NullFloat64
	dirty     bool
}

// playerBodies loads a player's whole body set on first touch. It is bounded by
// the solar system rather than by the log, so one query per player per batch
// replaces one per `vehicle.soi` event.
func (b *Batch) playerBodies(ctx context.Context, playerID int64) (map[bodyKey]*bodyEntry, error) {
	if m, ok := b.bodies[playerID]; ok {
		return m, nil
	}
	rows, err := b.tx.QueryContext(ctx,
		`SELECT kind, body, first_seq, first_sim_t FROM player_body WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, fmt.Errorf("stats: read bodies for player %d: %w", playerID, err)
	}
	defer rows.Close()

	m := map[bodyKey]*bodyEntry{}
	for rows.Next() {
		var k bodyKey
		e := &bodyEntry{}
		if err := rows.Scan(&k.kind, &k.body, &e.firstSeq, &e.firstSimT); err != nil {
			return nil, fmt.Errorf("stats: scan body for player %d: %w", playerID, err)
		}
		m[k] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read bodies for player %d: %w", playerID, err)
	}
	b.bodies[playerID] = m
	return m, nil
}

func (b *Batch) touchBody(k playerBodyKey, e *bodyEntry) {
	if !e.dirty {
		e.dirty = true
		b.dirtyBodies = append(b.dirtyBodies, k)
	}
}

// AddBody records a body the player has reached, reporting whether it was new —
// which is what the `soi_bodies` counter advances on.
func (b *Batch) AddBody(ctx context.Context, playerID int64, kind, body string, seq int64) (bool, error) {
	m, err := b.playerBodies(ctx, playerID)
	if err != nil {
		return false, err
	}
	k := bodyKey{kind, body}
	if _, ok := m[k]; ok {
		return false, nil
	}
	e := &bodyEntry{firstSeq: seq}
	m[k] = e
	b.touchBody(playerBodyKey{playerID, k}, e)
	return true, nil
}

// LowerBodyTime keeps the earliest career-relative arrival time at a body. Like
// the UPDATE it replaces it does nothing when the row does not exist.
func (b *Batch) LowerBodyTime(ctx context.Context, playerID int64, kind, body string, t float64) error {
	m, err := b.playerBodies(ctx, playerID)
	if err != nil {
		return err
	}
	k := bodyKey{kind, body}
	e, ok := m[k]
	if !ok {
		return nil
	}
	if e.firstSimT.Valid && e.firstSimT.Float64 <= t {
		return nil
	}
	e.firstSimT = sql.NullFloat64{Float64: t, Valid: true}
	b.touchBody(playerBodyKey{playerID, k}, e)
	return nil
}

func (b *Batch) BodyCount(ctx context.Context, playerID int64, kind string) (int64, error) {
	m, err := b.playerBodies(ctx, playerID)
	if err != nil {
		return 0, err
	}
	var n int64
	for k := range m {
		if k.kind == kind {
			n++
		}
	}
	return n, nil
}

func (b *Batch) flushBodies(ctx context.Context) error {
	if len(b.dirtyBodies) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyBodies, comparePlayerBodyKey)
	err := b.write(ctx, len(b.dirtyBodies), 5,
		`INSERT INTO player_body (player_id, kind, body, first_seq, first_sim_t) VALUES `,
		` ON CONFLICT (player_id, kind, body) DO UPDATE SET first_sim_t = excluded.first_sim_t`,
		func(i int, args []any) []any {
			k := b.dirtyBodies[i]
			e := b.bodies[k.playerID][k.bodyKey]
			e.dirty = false
			return append(args, k.playerID, k.kind, k.body, e.firstSeq, e.firstSimT)
		})
	if err != nil {
		return fmt.Errorf("stats: flush player_body: %w", err)
	}
	b.dirtyBodies = b.dirtyBodies[:0]
	return nil
}

// --- career_body ---------------------------------------------------------------

type careerBodyKey struct{ career, kind, body string }

type playerCareerBodyKey struct {
	playerID int64
	careerBodyKey
}

type careerBodyEntry struct {
	system    string
	firstSeq  int64
	firstSimT sql.NullFloat64
	dirty     bool
}

func (b *Batch) playerCareerBodies(ctx context.Context, playerID int64) (map[careerBodyKey]*careerBodyEntry, error) {
	if m, ok := b.careerBodies[playerID]; ok {
		return m, nil
	}
	rows, err := b.tx.QueryContext(ctx,
		`SELECT career, system, kind, body, first_seq, first_sim_t
		 FROM career_body WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, fmt.Errorf("stats: read career bodies for player %d: %w", playerID, err)
	}
	defer rows.Close()

	m := map[careerBodyKey]*careerBodyEntry{}
	for rows.Next() {
		var k careerBodyKey
		e := &careerBodyEntry{}
		if err := rows.Scan(&k.career, &e.system, &k.kind, &k.body, &e.firstSeq, &e.firstSimT); err != nil {
			return nil, fmt.Errorf("stats: scan career body for player %d: %w", playerID, err)
		}
		m[k] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read career bodies for player %d: %w", playerID, err)
	}
	b.careerBodies[playerID] = m
	return m, nil
}

func (b *Batch) touchCareerBody(k playerCareerBodyKey, e *careerBodyEntry) {
	if !e.dirty {
		e.dirty = true
		b.dirtyCareerBodies = append(b.dirtyCareerBodies, k)
	}
}

// AddCareerBody records a body reached in one save. A missing career or an
// undiscovered system cannot identify a scoped row and is deliberately ignored.
func (b *Batch) AddCareerBody(ctx context.Context, ev Event, kind, body string) (bool, error) {
	if ev.Career == "" {
		return false, nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil || system == "" {
		return false, err
	}
	m, err := b.playerCareerBodies(ctx, ev.PlayerID)
	if err != nil {
		return false, err
	}
	k := careerBodyKey{ev.Career, kind, body}
	if _, ok := m[k]; ok {
		return false, nil
	}
	e := &careerBodyEntry{system: system, firstSeq: ev.Seq}
	m[k] = e
	b.touchCareerBody(playerCareerBodyKey{ev.PlayerID, k}, e)
	return true, nil
}

// LowerCareerBodyTime keeps the earliest arrival time for a body in one save.
func (b *Batch) LowerCareerBodyTime(ctx context.Context, ev Event, kind, body string, t float64) error {
	if ev.Career == "" {
		return nil
	}
	m, err := b.playerCareerBodies(ctx, ev.PlayerID)
	if err != nil {
		return err
	}
	k := careerBodyKey{ev.Career, kind, body}
	e, ok := m[k]
	if !ok {
		return nil
	}
	if e.firstSimT.Valid && e.firstSimT.Float64 <= t {
		return nil
	}
	e.firstSimT = sql.NullFloat64{Float64: t, Valid: true}
	b.touchCareerBody(playerCareerBodyKey{ev.PlayerID, k}, e)
	return nil
}

func (b *Batch) CareerBodyCount(ctx context.Context, playerID int64, career, kind string) (int64, error) {
	m, err := b.playerCareerBodies(ctx, playerID)
	if err != nil {
		return 0, err
	}
	var n int64
	for k := range m {
		if k.career == career && k.kind == kind {
			n++
		}
	}
	return n, nil
}

// SystemBodyCount reports the union across this player's saves in the event's
// system. ok is false while that save's system is unknown.
func (b *Batch) SystemBodyCount(ctx context.Context, ev Event, kind string) (n int64, ok bool, err error) {
	if ev.Career == "" {
		return 0, false, nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil || system == "" {
		return 0, false, err
	}
	m, err := b.playerCareerBodies(ctx, ev.PlayerID)
	if err != nil {
		return 0, false, err
	}
	bodies := map[string]struct{}{}
	for k, e := range m {
		if e.system == system && k.kind == kind {
			bodies[k.body] = struct{}{}
		}
	}
	return int64(len(bodies)), true, nil
}

func (b *Batch) flushCareerBodies(ctx context.Context) error {
	if len(b.dirtyCareerBodies) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyCareerBodies, comparePlayerCareerBodyKey)
	err := b.write(ctx, len(b.dirtyCareerBodies), 7,
		`INSERT INTO career_body (player_id, career, system, kind, body, first_seq, first_sim_t) VALUES `,
		` ON CONFLICT (player_id, career, kind, body) DO UPDATE SET first_sim_t = excluded.first_sim_t`,
		func(i int, args []any) []any {
			k := b.dirtyCareerBodies[i]
			e := b.careerBodies[k.playerID][k.careerBodyKey]
			e.dirty = false
			return append(args, k.playerID, k.career, e.system, k.kind, k.body, e.firstSeq, e.firstSimT)
		})
	if err != nil {
		return fmt.Errorf("stats: flush career_body: %w", err)
	}
	b.dirtyCareerBodies = b.dirtyCareerBodies[:0]
	return nil
}

// --- kitten ---------------------------------------------------------------------

type playerKittenKey struct {
	playerID int64
	kid      string
}

type kittenEntry struct {
	name         string
	travelledM   float64
	fastestMs    float64
	missions     int64
	missionTimeS float64
	kia          int64
	updatedSeq   int64
	dirty        bool
}

func (b *Batch) playerKittens(ctx context.Context, playerID int64) (map[string]*kittenEntry, error) {
	if m, ok := b.kittens[playerID]; ok {
		return m, nil
	}
	rows, err := b.tx.QueryContext(ctx,
		`SELECT kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq
		 FROM kitten WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, fmt.Errorf("stats: read kittens for player %d: %w", playerID, err)
	}
	defer rows.Close()

	m := map[string]*kittenEntry{}
	for rows.Next() {
		var kid string
		e := &kittenEntry{}
		if err := rows.Scan(&kid, &e.name, &e.travelledM, &e.fastestMs,
			&e.missions, &e.missionTimeS, &e.kia, &e.updatedSeq); err != nil {
			return nil, fmt.Errorf("stats: scan kitten for player %d: %w", playerID, err)
		}
		m[kid] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read kittens for player %d: %w", playerID, err)
	}
	b.kittens[playerID] = m
	return m, nil
}

// UpsertKitten folds one `roster.snapshot` entry. Every running total merges
// with max() for the reason distanceFold gives: a snapshot that arrives out of
// order can fail to advance a total but must never rewind one.
func (b *Batch) UpsertKitten(ctx context.Context, playerID int64, k RosterKitten, seq int64) error {
	m, err := b.playerKittens(ctx, playerID)
	if err != nil {
		return err
	}
	kia := int64(0)
	if k.KIA {
		kia = 1
	}
	e, ok := m[k.Kid]
	if !ok {
		e = &kittenEntry{}
		m[k.Kid] = e
	}
	e.name = k.Name
	e.travelledM = max(e.travelledM, k.TravelledM)
	e.fastestMs = max(e.fastestMs, k.FastestMs)
	e.missions = max(e.missions, int64(k.Missions))
	e.missionTimeS = max(e.missionTimeS, k.MissionTimeS)
	e.kia = max(e.kia, kia)
	e.updatedSeq = seq
	if !e.dirty {
		e.dirty = true
		b.dirtyKittens = append(b.dirtyKittens, playerKittenKey{playerID, k.Kid})
	}
	return nil
}

// KittenDistance sums the per-save kitten rows. A kid is not save-scoped, so
// summing the lifetime kitten table would collapse same-named cats in two saves.
func (b *Batch) KittenDistance(ctx context.Context, playerID int64) (float64, error) {
	m, err := b.playerCareerKittens(ctx, playerID)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, e := range m {
		total += e.travelledM
	}
	return total, nil
}

// KittenTop is one kitten's claim on a per-kitten board: the value and whose it
// is.
type KittenTop struct {
	Name  string
	Value float64
}

// KittenTops reports the leader of each `kitten` column a record board reads —
// the furthest-travelled kitten and the most-flown one — for
// `top_kitten_distance` and `top_kitten_missions`.
//
// Ties are broken by `kid`, and that is not a nicety: the winner's *name* lands
// in the board row's context, Go randomises map iteration order, and a rebuild
// has to reproduce the incremental context byte for byte (see encodeContext in
// fold.go). Two kittens on the same number would otherwise disagree between
// runs of the same log.
func (b *Batch) KittenTops(ctx context.Context, playerID int64) (travelled, missions KittenTop, err error) {
	m, err := b.playerKittens(ctx, playerID)
	if err != nil {
		return KittenTop{}, KittenTop{}, err
	}
	for _, kid := range slices.Sorted(maps.Keys(m)) {
		e := m[kid]
		if e.travelledM > travelled.Value {
			travelled = KittenTop{Name: e.name, Value: e.travelledM}
		}
		if v := float64(e.missions); v > missions.Value {
			missions = KittenTop{Name: e.name, Value: v}
		}
	}
	return travelled, missions, nil
}

func (b *Batch) flushKittens(ctx context.Context) error {
	if len(b.dirtyKittens) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyKittens, comparePlayerKittenKey)
	err := b.write(ctx, len(b.dirtyKittens), 9,
		`INSERT INTO kitten (player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq) VALUES `,
		` ON CONFLICT (player_id, kid) DO UPDATE SET
		   name = excluded.name, travelled_m = excluded.travelled_m, fastest_ms = excluded.fastest_ms,
		   missions = excluded.missions, mission_time_s = excluded.mission_time_s,
		   kia = excluded.kia, updated_seq = excluded.updated_seq`,
		func(i int, args []any) []any {
			k := b.dirtyKittens[i]
			e := b.kittens[k.playerID][k.kid]
			e.dirty = false
			return append(args, k.playerID, k.kid, e.name, e.travelledM, e.fastestMs,
				e.missions, e.missionTimeS, e.kia, e.updatedSeq)
		})
	if err != nil {
		return fmt.Errorf("stats: flush kitten: %w", err)
	}
	b.dirtyKittens = b.dirtyKittens[:0]
	return nil
}

// --- career_kitten -------------------------------------------------------------

type careerKittenKey struct{ career, kid string }

type playerCareerKittenKey struct {
	playerID int64
	careerKittenKey
}

type careerKittenEntry struct {
	system       string
	name         string
	travelledM   float64
	fastestMs    float64
	missions     int64
	missionTimeS float64
	kia          int64
	updatedSeq   int64
	dirty        bool
}

func (b *Batch) playerCareerKittens(ctx context.Context, playerID int64) (map[careerKittenKey]*careerKittenEntry, error) {
	if m, ok := b.careerKittens[playerID]; ok {
		return m, nil
	}
	rows, err := b.tx.QueryContext(ctx,
		`SELECT career, system, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq
		 FROM career_kitten WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, fmt.Errorf("stats: read career kittens for player %d: %w", playerID, err)
	}
	defer rows.Close()

	m := map[careerKittenKey]*careerKittenEntry{}
	for rows.Next() {
		var k careerKittenKey
		e := &careerKittenEntry{}
		if err := rows.Scan(&k.career, &e.system, &k.kid, &e.name, &e.travelledM,
			&e.fastestMs, &e.missions, &e.missionTimeS, &e.kia, &e.updatedSeq); err != nil {
			return nil, fmt.Errorf("stats: scan career kitten for player %d: %w", playerID, err)
		}
		m[k] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read career kittens for player %d: %w", playerID, err)
	}
	b.careerKittens[playerID] = m
	return m, nil
}

// UpsertCareerKitten folds a roster total into its save-specific row. Without
// both a career and its discovered system, there is no scoped identity to write.
func (b *Batch) UpsertCareerKitten(ctx context.Context, ev Event, k RosterKitten) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil || system == "" {
		return err
	}
	m, err := b.playerCareerKittens(ctx, ev.PlayerID)
	if err != nil {
		return err
	}
	key := careerKittenKey{ev.Career, k.Kid}
	e, ok := m[key]
	if !ok {
		e = &careerKittenEntry{system: system}
		m[key] = e
	}
	kia := int64(0)
	if k.KIA {
		kia = 1
	}
	e.name = k.Name
	e.travelledM = max(e.travelledM, k.TravelledM)
	e.fastestMs = max(e.fastestMs, k.FastestMs)
	e.missions = max(e.missions, int64(k.Missions))
	e.missionTimeS = max(e.missionTimeS, k.MissionTimeS)
	e.kia = max(e.kia, kia)
	e.updatedSeq = ev.Seq
	if !e.dirty {
		e.dirty = true
		b.dirtyCareerKittens = append(b.dirtyCareerKittens, playerCareerKittenKey{ev.PlayerID, key})
	}
	return nil
}

func (b *Batch) CareerKittenDistance(ctx context.Context, playerID int64, career string) (float64, error) {
	m, err := b.playerCareerKittens(ctx, playerID)
	if err != nil {
		return 0, err
	}
	var total float64
	for k, e := range m {
		if k.career == career {
			total += e.travelledM
		}
	}
	return total, nil
}

func (b *Batch) SystemKittenDistance(ctx context.Context, playerID int64, system string) (float64, error) {
	m, err := b.playerCareerKittens(ctx, playerID)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, e := range m {
		if e.system == system {
			total += e.travelledM
		}
	}
	return total, nil
}

func (b *Batch) CareerKittenTops(ctx context.Context, playerID int64, career string) (travelled, missions KittenTop, err error) {
	m, err := b.playerCareerKittens(ctx, playerID)
	if err != nil {
		return KittenTop{}, KittenTop{}, err
	}
	keys := slices.AppendSeq([]careerKittenKey(nil), maps.Keys(m))
	slices.SortFunc(keys, compareCareerKittenKey)
	for _, k := range keys {
		if k.career != career {
			continue
		}
		e := m[k]
		if e.travelledM > travelled.Value {
			travelled = KittenTop{Name: e.name, Value: e.travelledM}
		}
		if v := float64(e.missions); v > missions.Value {
			missions = KittenTop{Name: e.name, Value: v}
		}
	}
	return travelled, missions, nil
}

func (b *Batch) SystemKittenTops(ctx context.Context, playerID int64, system string) (travelled, missions KittenTop, err error) {
	m, err := b.playerCareerKittens(ctx, playerID)
	if err != nil {
		return KittenTop{}, KittenTop{}, err
	}
	keys := slices.AppendSeq([]careerKittenKey(nil), maps.Keys(m))
	slices.SortFunc(keys, compareCareerKittenKey)
	for _, k := range keys {
		e := m[k]
		if e.system != system {
			continue
		}
		if e.travelledM > travelled.Value {
			travelled = KittenTop{Name: e.name, Value: e.travelledM}
		}
		if v := float64(e.missions); v > missions.Value {
			missions = KittenTop{Name: e.name, Value: v}
		}
	}
	return travelled, missions, nil
}

func (b *Batch) flushCareerKittens(ctx context.Context) error {
	if len(b.dirtyCareerKittens) == 0 {
		return nil
	}
	slices.SortFunc(b.dirtyCareerKittens, comparePlayerCareerKittenKey)
	err := b.write(ctx, len(b.dirtyCareerKittens), 11,
		`INSERT INTO career_kitten (player_id, career, system, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq) VALUES `,
		` ON CONFLICT (player_id, career, kid) DO UPDATE SET
		   name = excluded.name, travelled_m = excluded.travelled_m, fastest_ms = excluded.fastest_ms,
		   missions = excluded.missions, mission_time_s = excluded.mission_time_s,
		   kia = excluded.kia, updated_seq = excluded.updated_seq`,
		func(i int, args []any) []any {
			k := b.dirtyCareerKittens[i]
			e := b.careerKittens[k.playerID][k.careerKittenKey]
			e.dirty = false
			return append(args, k.playerID, k.career, e.system, k.kid, e.name, e.travelledM,
				e.fastestMs, e.missions, e.missionTimeS, e.kia, e.updatedSeq)
		})
	if err != nil {
		return fmt.Errorf("stats: flush career_kitten: %w", err)
	}
	b.dirtyCareerKittens = b.dirtyCareerKittens[:0]
	return nil
}

// --- player_stat and player_stat_period ------------------------------------------

// statKind is which of the four write rules a pending row follows. It is the
// in-memory form of the four helpers in fold.go, and the flush emits one
// statement per kind because each kind's `ON CONFLICT` clause differs.
type statKind uint8

const (
	kindRecord statKind = iota // largest wins; ties keep the earlier seq
	kindBest                   // smallest wins; ties keep the earlier seq
	kindCount                  // deltas accumulate
	kindSet                    // a derived total, replaced outright
	numStatKinds
)

type statKey struct {
	playerID int64
	stat     string
}

type periodKey struct {
	playerID int64
	stat     string
	period   string
	bucket   string
}

// careerStatKey is the primary key of career_stat. `system` is a written column,
// not part of the key — a save cannot change systems (§3.15), so it can never
// split a row in two.
type careerStatKey struct {
	playerID int64
	career   string
	stat     string
}

// systemStatKey is the primary key of system_stat.
type systemStatKey struct {
	playerID int64
	system   string
	stat     string
}

type pendingStat struct {
	value float64
	cx    any
	seq   int64
}

type pendingCareerStat struct {
	pendingStat
	system string
}

// merge folds a new write into a pending one under the kind's rule, reporting
// whether the pending row changed. The strict comparisons are what preserve
// "whoever got there first keeps the rank": an equal value leaves the earlier
// seq in place, exactly as the SQL guard did.
//
// kindSet is the one that needs a word. Its SQL carried
// `WHERE excluded.value <> player_stat.value`, so an event that recomputed the
// same total left `updated_seq` alone; merging only on a changed value is that
// rule, and it is why the pending seq ends up being the seq of the last event
// that actually moved the number.
func (p *pendingStat) merge(kind statKind, value float64, cx any, seq int64) {
	switch kind {
	case kindCount:
		p.value += value
		p.seq = seq
	case kindRecord:
		if value > p.value {
			p.value, p.cx, p.seq = value, cx, seq
		}
	case kindBest:
		if value < p.value {
			p.value, p.cx, p.seq = value, cx, seq
		}
	case kindSet:
		if value != p.value {
			p.value, p.cx, p.seq = value, cx, seq
		}
	}
}

func (b *Batch) putStat(kind statKind, playerID int64, stat string, value float64, cx any, seq int64) {
	k := statKey{playerID, stat}
	if p, ok := b.stats[kind][k]; ok {
		p.merge(kind, value, cx, seq)
	} else {
		b.stats[kind][k] = &pendingStat{value: value, cx: cx, seq: seq}
	}
	if kind == kindSet {
		b.values[k] = value
	}
}

func (b *Batch) putPeriod(kind statKind, playerID int64, stat, period, bucket string, value float64, cx any, seq int64) {
	k := periodKey{playerID, stat, period, bucket}
	if p, ok := b.periods[kind][k]; ok {
		p.merge(kind, value, cx, seq)
		return
	}
	b.periods[kind][k] = &pendingStat{value: value, cx: cx, seq: seq}
}

// putScoped records a record/best/count board write in every scope beyond
// `player`, so a fold never has to name them and a scope added later composes
// for free. Derived totals (kindSet) use explicit scope-specific queries and
// writers instead; one scope's total is not another's.
func (b *Batch) putScoped(ctx context.Context, kind statKind, ev Event, stat string, value float64, cx any) error {
	if ev.Career == "" {
		return nil
	}
	system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
	if err != nil {
		return err
	}
	b.putCareerStat(kind, ev, system, stat, value, cx)
	if system != "" {
		b.putSystemStat(kind, ev, system, stat, value, cx)
	}
	return nil
}

// putCareerStat records a board write in the career scope. A no-op when the
// event has no career: a row keyed on "" would merge every save at once.
func (b *Batch) putCareerStat(kind statKind, ev Event, system, stat string, value float64, cx any) {
	if ev.Career == "" {
		return
	}
	k := careerStatKey{ev.PlayerID, ev.Career, stat}
	if p, ok := b.careerStats[kind][k]; ok {
		p.merge(kind, value, cx, ev.Seq)
	} else {
		b.careerStats[kind][k] = &pendingCareerStat{
			pendingStat: pendingStat{value: value, cx: cx, seq: ev.Seq},
			system:      system,
		}
	}
	if kind == kindSet {
		b.careerValues[k] = value
	}
}

// putSystemStat records a board write keyed by (player, celestial system,
// stat). An unknown system has no comparable scope and writes no row.
func (b *Batch) putSystemStat(kind statKind, ev Event, system, stat string, value float64, cx any) {
	if system == "" {
		return
	}
	k := systemStatKey{ev.PlayerID, system, stat}
	if p, ok := b.systemStats[kind][k]; ok {
		p.merge(kind, value, cx, ev.Seq)
	} else {
		b.systemStats[kind][k] = &pendingStat{value: value, cx: cx, seq: ev.Seq}
	}
}

// StatValue is a player's current value on a board, counting writes this batch
// has buffered but not yet flushed. Only setValue needs it — a derived total's
// *window* contribution is what it grew by, which needs the previous total.
func (b *Batch) StatValue(ctx context.Context, playerID int64, stat string) (float64, error) {
	k := statKey{playerID, stat}
	if v, ok := b.values[k]; ok {
		return v, nil
	}
	var v float64
	err := b.tx.QueryRowContext(ctx,
		`SELECT value FROM player_stat WHERE player_id = ? AND stat = ?`, playerID, stat).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v = 0
	case err != nil:
		return 0, fmt.Errorf("stats: read %s for player %d: %w", stat, playerID, err)
	}
	b.values[k] = v
	return v, nil
}

// CareerStatValue reads a career-scoped board value, answering from this
// batch's own pending writes first so two reads in one batch see each other
// exactly as they did when each was its own statement.
func (b *Batch) CareerStatValue(ctx context.Context, playerID int64, career, stat string) (float64, error) {
	k := careerStatKey{playerID, career, stat}
	if v, ok := b.careerValues[k]; ok {
		return v, nil
	}
	var v float64
	err := b.tx.QueryRowContext(ctx,
		`SELECT value FROM career_stat WHERE player_id = ? AND career = ? AND stat = ?`,
		playerID, career, stat).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v = 0
	case err != nil:
		return 0, fmt.Errorf("stats: read %s for player %d career %q: %w", stat, playerID, career, err)
	}
	b.careerValues[k] = v
	return v, nil
}

// statFlush is one kind's flush: the conflict clause that spells its rule.
// Indexed by [statKind]; kindSet has no window form, so periodFlush leaves it
// empty and nothing ever writes one.
var statFlush = [numStatKinds]string{
	kindRecord: ` ON CONFLICT (player_id, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value > player_stat.value`,
	kindBest: ` ON CONFLICT (player_id, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value < player_stat.value`,
	kindCount: ` ON CONFLICT (player_id, stat) DO UPDATE SET
	   value = player_stat.value + excluded.value, updated_seq = excluded.updated_seq`,
	kindSet: ` ON CONFLICT (player_id, stat) DO UPDATE SET
	   value = excluded.value, updated_seq = excluded.updated_seq
	 WHERE excluded.value <> player_stat.value`,
}

var periodFlush = [numStatKinds]string{
	kindRecord: ` ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value > player_stat_period.value`,
	kindBest: ` ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value < player_stat_period.value`,
	kindCount: ` ON CONFLICT (player_id, stat, period, bucket) DO UPDATE SET
	   value = player_stat_period.value + excluded.value, updated_seq = excluded.updated_seq`,
}

var careerStatFlush = [numStatKinds]string{
	kindRecord: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value > career_stat.value`,
	kindBest: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value < career_stat.value`,
	kindCount: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = career_stat.value + excluded.value, updated_seq = excluded.updated_seq`,
	kindSet: ` ON CONFLICT (player_id, career, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value <> career_stat.value`,
}

var systemStatFlush = [numStatKinds]string{
	kindRecord: ` ON CONFLICT (player_id, system, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value > system_stat.value`,
	kindBest: ` ON CONFLICT (player_id, system, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value < system_stat.value`,
	kindCount: ` ON CONFLICT (player_id, system, stat) DO UPDATE SET
	   value = system_stat.value + excluded.value, updated_seq = excluded.updated_seq`,
	kindSet: ` ON CONFLICT (player_id, system, stat) DO UPDATE SET
	   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
	 WHERE excluded.value <> system_stat.value`,
}

func (b *Batch) flushStats(ctx context.Context) error {
	for kind, pending := range b.stats {
		if len(pending) == 0 {
			continue
		}
		keys := slices.AppendSeq(b.statKeys[:0], maps.Keys(pending))
		slices.SortFunc(keys, compareStatKey)
		b.statKeys = keys
		err := b.write(ctx, len(keys), 5,
			`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES `,
			statFlush[statKind(kind)],
			func(i int, args []any) []any {
				k := keys[i]
				p := pending[k]
				return append(args, k.playerID, k.stat, p.value, p.cx, p.seq)
			})
		if err != nil {
			return fmt.Errorf("stats: flush player_stat: %w", err)
		}
		clear(pending)
	}
	return nil
}

func (b *Batch) flushCareerStats(ctx context.Context) error {
	for kind, pending := range b.careerStats {
		if len(pending) == 0 {
			continue
		}
		keys := slices.AppendSeq(b.careerStatKeys[:0], maps.Keys(pending))
		slices.SortFunc(keys, compareCareerStatKey)
		b.careerStatKeys = keys
		err := b.write(ctx, len(keys), 7,
			`INSERT INTO career_stat (player_id, career, system, stat, value, context, updated_seq) VALUES `,
			careerStatFlush[statKind(kind)],
			func(i int, args []any) []any {
				k := keys[i]
				p := pending[k]
				return append(args, k.playerID, k.career, p.system, k.stat, p.value, p.cx, p.seq)
			})
		if err != nil {
			return fmt.Errorf("stats: flush career_stat: %w", err)
		}
		clear(pending)
	}
	return nil
}

func (b *Batch) flushSystemStats(ctx context.Context) error {
	for kind, pending := range b.systemStats {
		if len(pending) == 0 {
			continue
		}
		keys := slices.AppendSeq(b.systemStatKeys[:0], maps.Keys(pending))
		slices.SortFunc(keys, compareSystemStatKey)
		b.systemStatKeys = keys
		err := b.write(ctx, len(keys), 6,
			`INSERT INTO system_stat (player_id, system, stat, value, context, updated_seq) VALUES `,
			systemStatFlush[statKind(kind)],
			func(i int, args []any) []any {
				k := keys[i]
				p := pending[k]
				return append(args, k.playerID, k.system, k.stat, p.value, p.cx, p.seq)
			})
		if err != nil {
			return fmt.Errorf("stats: flush system_stat: %w", err)
		}
		clear(pending)
	}
	return nil
}

func (b *Batch) flushPeriods(ctx context.Context) error {
	for kind, pending := range b.periods {
		if len(pending) == 0 {
			continue
		}
		keys := slices.AppendSeq(b.periodKeys[:0], maps.Keys(pending))
		slices.SortFunc(keys, comparePeriodKey)
		b.periodKeys = keys
		err := b.write(ctx, len(keys), 7,
			`INSERT INTO player_stat_period (player_id, stat, period, bucket, value, context, updated_seq) VALUES `,
			periodFlush[statKind(kind)],
			func(i int, args []any) []any {
				k := keys[i]
				p := pending[k]
				return append(args, k.playerID, k.stat, k.period, k.bucket, p.value, p.cx, p.seq)
			})
		if err != nil {
			return fmt.Errorf("stats: flush player_stat_period: %w", err)
		}
		clear(pending)
	}
	return nil
}

// --- event_census -----------------------------------------------------------------

type censusKey struct {
	typ    string
	period string
	bucket string
}

// pendingCensus is one census row's contribution from this batch: a count, and
// the ends of the seq/time range it covers.
type pendingCensus struct {
	n                 int64
	firstSeq, lastSeq int64
	firstAt, lastAt   int64
}

// countEvent records one event against one census row. See census.go.
//
// Events reach a batch in seq order, so the first one to touch a key sets the
// range's lower end and every later one only moves the upper — but the
// comparisons are written out rather than assumed, because `recv_time` is a
// server clock and a clock is allowed to step backwards.
func (b *Batch) countEvent(typ, period, bucket string, ev Event) {
	k := censusKey{typ, period, bucket}
	p, ok := b.census[k]
	if !ok {
		b.census[k] = &pendingCensus{
			n:        1,
			firstSeq: ev.Seq, lastSeq: ev.Seq,
			firstAt: ev.RecvTime, lastAt: ev.RecvTime,
		}
		return
	}
	p.n++
	p.firstSeq = min(p.firstSeq, ev.Seq)
	p.lastSeq = max(p.lastSeq, ev.Seq)
	p.firstAt = min(p.firstAt, ev.RecvTime)
	p.lastAt = max(p.lastAt, ev.RecvTime)
}

// censusFlush accumulates the count and widens the range. The CASE expressions
// are `min`/`max` spelled the long way: tursogo's scalar function set is not
// something to bet a projection on, and this is the same arithmetic.
const censusFlush = ` ON CONFLICT (type, period, bucket) DO UPDATE SET
	   n = event_census.n + excluded.n,
	   first_seq = CASE WHEN excluded.first_seq < event_census.first_seq THEN excluded.first_seq ELSE event_census.first_seq END,
	   last_seq  = CASE WHEN excluded.last_seq  > event_census.last_seq  THEN excluded.last_seq  ELSE event_census.last_seq  END,
	   first_at  = CASE WHEN excluded.first_at  < event_census.first_at  THEN excluded.first_at  ELSE event_census.first_at  END,
	   last_at   = CASE WHEN excluded.last_at   > event_census.last_at   THEN excluded.last_at   ELSE event_census.last_at   END`

func (b *Batch) flushCensus(ctx context.Context) error {
	if len(b.census) == 0 {
		return nil
	}
	keys := slices.AppendSeq(b.censusKeys[:0], maps.Keys(b.census))
	slices.SortFunc(keys, compareCensusKey)
	b.censusKeys = keys
	err := b.write(ctx, len(keys), 8,
		`INSERT INTO event_census (type, period, bucket, n, first_seq, last_seq, first_at, last_at) VALUES `,
		censusFlush,
		func(i int, args []any) []any {
			k := keys[i]
			p := b.census[k]
			return append(args, k.typ, k.period, k.bucket, p.n, p.firstSeq, p.lastSeq, p.firstAt, p.lastAt)
		})
	if err != nil {
		return fmt.Errorf("stats: flush event_census: %w", err)
	}
	clear(b.census)
	return nil
}

// --- flush ------------------------------------------------------------------------

// Flush writes everything the batch is holding. The projector calls it once at
// the end of a batch, and [Batch.trimPeriods] calls it before a retention
// delete so that the delete sees the same rows it would have seen when the
// writes went out one at a time.
//
// Read-through caches survive a flush: afterwards they hold exactly what the
// database holds, so re-reading would buy nothing.
func (b *Batch) Flush(ctx context.Context) error {
	for _, fn := range []func(context.Context) error{
		b.flushFlights, b.flushCareers, b.flushBodies, b.flushCareerBodies,
		b.flushKittens, b.flushCareerKittens, b.flushStats, b.flushCareerStats, b.flushSystemStats,
		b.flushPeriods, b.flushCensus,
	} {
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return nil
}

// write emits rows in chunks of [Batch.flushRows] as `prefix VALUES (…),(…) suffix`.
// row appends one row's bound parameters and is called once per row, in order.
func (b *Batch) write(ctx context.Context, rows, cols int, prefix, suffix string, row func(i int, args []any) []any) error {
	args := b.args[:0]
	var sb strings.Builder
	for start := 0; start < rows; start += b.flushRows {
		end := min(start+b.flushRows, rows)

		sb.Reset()
		sb.Grow(len(prefix) + len(suffix) + (end-start)*(2*cols+3))
		sb.WriteString(prefix)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteByte('(')
			for c := range cols {
				if c > 0 {
					sb.WriteByte(',')
				}
				sb.WriteByte('?')
			}
			sb.WriteByte(')')
		}
		sb.WriteString(suffix)

		args = args[:0]
		for i := start; i < end; i++ {
			args = row(i, args)
		}
		if _, err := b.tx.ExecContext(ctx, sb.String(), args...); err != nil {
			b.args = args[:0]
			return err
		}
	}
	b.args = args[:0]
	return nil
}

// --- key ordering -------------------------------------------------------------------

func compareCensusKey(x, y censusKey) int {
	if c := strings.Compare(x.typ, y.typ); c != 0 {
		return c
	}
	if c := strings.Compare(x.period, y.period); c != 0 {
		return c
	}
	return strings.Compare(x.bucket, y.bucket)
}

func compareStatKey(x, y statKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	return strings.Compare(x.stat, y.stat)
}

func comparePeriodKey(x, y periodKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	if c := strings.Compare(x.stat, y.stat); c != 0 {
		return c
	}
	if c := strings.Compare(x.period, y.period); c != 0 {
		return c
	}
	return strings.Compare(x.bucket, y.bucket)
}

func compareCareerStatKey(x, y careerStatKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	if c := strings.Compare(x.career, y.career); c != 0 {
		return c
	}
	return strings.Compare(x.stat, y.stat)
}

func compareSystemStatKey(x, y systemStatKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	if c := strings.Compare(x.system, y.system); c != 0 {
		return c
	}
	return strings.Compare(x.stat, y.stat)
}

func compareCareerKey(x, y careerKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	return strings.Compare(x.career, y.career)
}

func comparePlayerBodyKey(x, y playerBodyKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	if c := strings.Compare(x.kind, y.kind); c != 0 {
		return c
	}
	return strings.Compare(x.body, y.body)
}

func compareCareerBodyKey(x, y careerBodyKey) int {
	if c := strings.Compare(x.career, y.career); c != 0 {
		return c
	}
	if c := strings.Compare(x.kind, y.kind); c != 0 {
		return c
	}
	return strings.Compare(x.body, y.body)
}

func comparePlayerCareerBodyKey(x, y playerCareerBodyKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	return compareCareerBodyKey(x.careerBodyKey, y.careerBodyKey)
}

func comparePlayerKittenKey(x, y playerKittenKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	return strings.Compare(x.kid, y.kid)
}

func compareCareerKittenKey(x, y careerKittenKey) int {
	if c := strings.Compare(x.career, y.career); c != 0 {
		return c
	}
	return strings.Compare(x.kid, y.kid)
}

func comparePlayerCareerKittenKey(x, y playerCareerKittenKey) int {
	if c := cmpInt64(x.playerID, y.playerID); c != 0 {
		return c
	}
	return compareCareerKittenKey(x.careerKittenKey, y.careerKittenKey)
}

func cmpInt64(x, y int64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
