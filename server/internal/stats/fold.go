package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/meow-sci/catlog/server/internal/ids"
)

// Fold is one projection rule (§5.6). The projector applies every registered
// fold to every event of a batch inside one projections.db transaction.
//
// §5.6 writes the signature as `Apply(tx *sql.Tx, ev DecodedEvent, fs
// FlightStateReader) error`. Two additions, both recorded in docs/DECISIONS.md:
// a context, because every database call underneath takes one and a rebuild must
// be cancellable; and Name, because "which fold failed" is the only useful thing
// to put in the log line when one does.
type Fold interface {
	// Name identifies the fold in logs and in /admin/stats.
	Name() string
	// Apply writes this fold's contribution for one event. Returning an error
	// aborts the whole batch: a fold that cannot write must not be allowed to
	// advance the checkpoint past the event it dropped.
	Apply(ctx context.Context, tx *sql.Tx, ev Event, fs FlightStateReader) error
}

// FlightStateReader is what a fold may ask about the flight an event belongs to.
//
// The first method is §5.6's; the other two carry the rebuild-only refinements.
// They live on the same interface because a fold has exactly three parameters
// and the refinement is a property of the pass, not of the event: during
// incremental folding Refined is false and KIANear always answers false, so the
// same fold code produces the optimistic incremental answer and the exact
// rebuild answer without branching anywhere else.
type FlightStateReader interface {
	// Flight reads flight_state for a flight, reporting false when no row
	// exists yet.
	Flight(ctx context.Context, id ids.ID) (FlightState, bool, error)
	// Refined reports whether the caller is a rebuild, where flight_state is
	// already complete for the entire history (§5.6).
	Refined() bool
	// KIANear reports whether a kitten.kia landed in this flight within ±2.0 s
	// of simT (§4.2's crew-survival window). Always false outside a rebuild.
	KIANear(flight ids.ID, simT float64) bool
}

// KIAWindowSeconds is the ±window of §4.2's crew-survival rule.
const KIAWindowSeconds = 2.0

// Folds returns every fold in application order: flight state first (§5.6), then
// the boards. The order of the boards among themselves does not matter — no two
// write the same (player_id, stat).
func Folds() []Fold {
	return append([]Fold{FlightFold()}, BoardFolds()...)
}

// FlightFold returns the flight_state fold, which every other fold depends on.
func FlightFold() Fold { return flightFold{} }

// BoardFolds returns every leaderboard fold, in board-metadata order.
//
// A rebuild applies these on its second pass, after [FlightFold] alone has built
// a complete flight_state on the first (§5.6).
func BoardFolds() []Fold {
	return []Fold{
		lithobrakeFold{},
		peakGFold{},
		speedFold{stat: StatFastestSurfaceSpeed, surface: true},
		speedFold{stat: StatFastestOrbitalSpeed},
		countFold{stat: StatKittenTumbles, eventType: "kitten.tumble"},
		rudFold{},
		orbitsFold{},
		soiFold{},
		countFold{stat: StatDockings, eventType: "vehicle.docked"},
		countFold{stat: StatStagings, eventType: "vehicle.staging"},
		recoveredFold{},
		distanceFold{},
	}
}

// --- shared write helpers ----------------------------------------------------
//
// These are the two shapes every board write takes. They are here rather than in
// package store because the SQL *is* the rule: the WHERE guard on the upsert is
// how "ties keep the earliest updated_seq" is spelled.

// putRecord writes a record (max) board. The row is replaced only by a strictly
// larger value, so an equal value leaves the earlier updated_seq — and therefore
// the earlier claimant's rank — untouched (§5.6).
func putRecord(ctx context.Context, tx *sql.Tx, playerID int64, stat string, value float64, context map[string]any, seq int64) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (player_id, stat) DO UPDATE SET
		   value = excluded.value, context = excluded.context, updated_seq = excluded.updated_seq
		 WHERE excluded.value > player_stat.value`,
		playerID, stat, value, cx, seq)
	if err != nil {
		return fmt.Errorf("stats: record %s for player %d: %w", stat, playerID, err)
	}
	return nil
}

// addCount advances a counter board. updated_seq becomes the seq at which the
// counter reached its new value, which is what makes the tie-break "whoever got
// to N first" rather than "whoever was written last".
func addCount(ctx context.Context, tx *sql.Tx, playerID int64, stat string, delta float64, seq int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, NULL, ?)
		 ON CONFLICT (player_id, stat) DO UPDATE SET
		   value = player_stat.value + excluded.value, updated_seq = excluded.updated_seq`,
		playerID, stat, delta, seq)
	if err != nil {
		return fmt.Errorf("stats: count %s for player %d: %w", stat, playerID, err)
	}
	return nil
}

// setValue writes a derived total, replacing whatever was there. Used by the two
// boards whose value is a function of another table (`soi_bodies` counts
// player_body, `distance_travelled` sums kitten) rather than an accumulation.
func setValue(ctx context.Context, tx *sql.Tx, playerID int64, stat string, value float64, seq int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, NULL, ?)
		 ON CONFLICT (player_id, stat) DO UPDATE SET
		   value = excluded.value, updated_seq = excluded.updated_seq
		 WHERE excluded.value <> player_stat.value`,
		playerID, stat, value, seq)
	if err != nil {
		return fmt.Errorf("stats: set %s for player %d: %w", stat, playerID, err)
	}
	return nil
}

// encodeContext renders a stat's context column. encoding/json sorts map keys,
// so the same context always produces the same bytes — which is what lets a
// rebuild be compared to the incremental result column for column.
func encodeContext(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("stats: encode context: %w", err)
	}
	return string(b), nil
}

// scoreable reports whether an event may contribute to a board: it must belong
// to a flight, and that flight must carry no flag bit (§5.6).
//
// Events with no flight (session.started, roster.snapshot) are scoreable — there
// is no flight to have been flagged, and `distance_travelled` folds one of them.
func scoreable(ctx context.Context, ev Event, fs FlightStateReader) (bool, error) {
	if !ev.HasFlight() {
		return true, nil
	}
	st, ok, err := fs.Flight(ctx, ev.FlightID)
	if err != nil {
		return false, err
	}
	if !ok {
		// No flight_state row means no flight.flagged has ever been seen for it.
		// The row is created by FlightFold for every event carrying a flight, so
		// in practice this only happens if FlightFold was not applied.
		return true, nil
	}
	return st.Flags == 0, nil
}
