package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// The flight_state.flags bitfield (§5.4, extended by the docs/events.md
// amendment). Any bit set excludes the flight from every board.
const (
	FlagTeleport     int64 = 1 << 0
	FlagRefuel       int64 = 1 << 1
	FlagResourceEdit int64 = 1 << 2
	FlagConsole      int64 = 1 << 3
	// FlagTuning marks a flight during which the game's debug window was
	// live-editing KittenLocomotionTuning.Current.TumbleSpeedGate — the sole
	// classifier for kitten.tumble. It is the reason the flag exclusion has to
	// cover counter boards and not only record boards: `kitten_tumbles` is a
	// counter, and without the exclusion this flag would protect nothing
	// (docs/events.md, docs/DECISIONS.md).
	FlagTuning int64 = 1 << 4
	// FlagOther is set for a `flight.flagged` value this build does not know.
	// Not in §5.4: an unrecognised flag is a newer mod telling us something is
	// wrong with this flight, and the only safe reading of "something is wrong"
	// is to exclude it. Failing open would make every future flag a scoring
	// loophole until the server caught up.
	FlagOther int64 = 1 << 5
)

// FlagBit maps a §4.2 `flight.flagged.flag` value onto its bit, returning
// [FlagOther] for anything it does not recognise.
func FlagBit(flag string) int64 {
	switch flag {
	case "teleport":
		return FlagTeleport
	case "refuel":
		return FlagRefuel
	case "resource_edit":
		return FlagResourceEdit
	case "console":
		return FlagConsole
	case "tuning":
		return FlagTuning
	default:
		return FlagOther
	}
}

// FlagNames renders a flags bitfield for logs and for /admin/stats.
func FlagNames(flags int64) []string {
	var out []string
	for _, f := range []struct {
		bit  int64
		name string
	}{
		{FlagTeleport, "teleport"},
		{FlagRefuel, "refuel"},
		{FlagResourceEdit, "resource_edit"},
		{FlagConsole, "console"},
		{FlagTuning, "tuning"},
		{FlagOther, "other"},
	} {
		if flags&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// FlightState is a row of `flight_state` (§5.4).
type FlightState struct {
	FlightID    ids.ID
	PlayerID    int64
	Flags       int64
	EndedReason string
	Crew        sql.NullInt64
	Body        string
	StartedSeq  int64
}

// Flagged reports whether any flag bit is set.
func (f FlightState) Flagged() bool { return f.Flags != 0 }

// Recovered reports whether the flight ended with the vehicle recovered — the
// condition `peak_g_survived` applies during a rebuild (§5.6).
func (f FlightState) Recovered() bool { return f.EndedReason == "recovered" }

// flightFold maintains `flight_state` (§5.6). It is applied before every other
// fold, because every other fold asks it whether the flight has been flagged.
type flightFold struct{}

func (flightFold) Name() string { return "flight_state" }

func (flightFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, _ FlightStateReader) error {
	if !ev.HasFlight() {
		return nil
	}
	// Every flight-bearing event creates the row, not just flight.started: a
	// batch can be split so that a flight.flagged is folded before the
	// flight.started it belongs to has arrived, and the flag must not be lost.
	if err := ensureFlight(ctx, tx, ev.FlightID, ev.PlayerID, ev.Seq); err != nil {
		return err
	}

	switch ev.Type {
	case "flight.started":
		p, ok := payloadOf[FlightStarted](ev)
		if !ok {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE flight_state SET crew = ?, body = ?, started_seq = ? WHERE flight_id = ?`,
			p.CrewCount, p.Body, ev.Seq, ids.Bytes(ev.FlightID)); err != nil {
			return fmt.Errorf("stats: flight started %s: %w", ids.String(ev.FlightID), err)
		}
	case "flight.ended":
		p, ok := payloadOf[FlightEnded](ev)
		if !ok {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE flight_state SET ended_reason = ? WHERE flight_id = ?`,
			p.Reason, ids.Bytes(ev.FlightID)); err != nil {
			return fmt.Errorf("stats: flight ended %s: %w", ids.String(ev.FlightID), err)
		}
	case "flight.flagged":
		p, ok := payloadOf[FlightFlagged](ev)
		if !ok {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE flight_state SET flags = flags | ? WHERE flight_id = ?`,
			FlagBit(p.Flag), ids.Bytes(ev.FlightID)); err != nil {
			return fmt.Errorf("stats: flight flagged %s: %w", ids.String(ev.FlightID), err)
		}
	}
	return nil
}

func ensureFlight(ctx context.Context, tx *sql.Tx, flight ids.ID, playerID, seq int64) error {
	// OR IGNORE, never error inspection: tursogo collapses every constraint
	// violation onto one sentinel (docs/DECISIONS.md, WP1).
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO flight_state (flight_id, player_id, flags, started_seq) VALUES (?, ?, 0, ?)`,
		ids.Bytes(flight), playerID, seq); err != nil {
		return fmt.Errorf("stats: ensure flight %s: %w", ids.String(flight), err)
	}
	return nil
}

// --- the reader the projector hands to the folds -----------------------------

// Flights reads flight_state out of the transaction the folds are writing to, so
// a fold sees the flag that [flightFold] set for the very same event.
//
// It carries the rebuild refinements too (§5.6): `refined` turns on the exact
// rules, and `kia` is the per-flight index of kitten.kia sim times the rebuild's
// first pass collected.
type Flights struct {
	q       store.Querier
	refined bool
	kia     map[ids.ID][]float64
}

// NewFlights builds an incremental reader over q.
func NewFlights(q store.Querier) *Flights { return &Flights{q: q} }

// NewRefinedFlights builds the rebuild reader: flight_state is already complete
// for the whole history, and kia indexes every kitten.kia by flight and sim time.
func NewRefinedFlights(q store.Querier, kia map[ids.ID][]float64) *Flights {
	return &Flights{q: q, refined: true, kia: kia}
}

// Refined implements [FlightStateReader].
func (f *Flights) Refined() bool { return f.refined }

// KIANear implements [FlightStateReader]: §4.2's ±2 s crew-survival window.
func (f *Flights) KIANear(flight ids.ID, simT float64) bool {
	for _, t := range f.kia[flight] {
		d := t - simT
		if d < 0 {
			d = -d
		}
		if d <= KIAWindowSeconds {
			return true
		}
	}
	return false
}

// Flight implements [FlightStateReader].
func (f *Flights) Flight(ctx context.Context, id ids.ID) (FlightState, bool, error) {
	st := FlightState{FlightID: id}
	var reason, body sql.NullString
	err := f.q.QueryRowContext(ctx,
		`SELECT player_id, flags, ended_reason, crew, body, started_seq FROM flight_state WHERE flight_id = ?`,
		ids.Bytes(id)).Scan(&st.PlayerID, &st.Flags, &reason, &st.Crew, &body, &st.StartedSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return FlightState{}, false, nil
	}
	if err != nil {
		return FlightState{}, false, fmt.Errorf("stats: read flight %s: %w", ids.String(id), err)
	}
	st.EndedReason = reason.String
	st.Body = body.String
	return st, true, nil
}
