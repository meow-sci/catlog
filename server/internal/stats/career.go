package stats

import (
	"context"
	"database/sql"
	"fmt"
)

// A **career** is one KSA save played over time (§4.1). It is the unit every
// time-to-milestone board is measured in, and it exists because `sim_t` is only
// meaningful relative to a career:
//
//   - KSA starts a new game at sim time exactly 0 and serialises the clock into
//     the save (`UniverseData.GameTime`, restored by `Universe.DeserializeSave`),
//     so `sim_t` on any event *is* "seconds since this career began" — no second
//     clock and no per-career origin has to be shipped or stored.
//   - `session_id` is minted at every save-*load* boundary, so one career is many
//     sessions and two unrelated saves interleave freely in one player's log.
//     Without the career key those two facts are indistinguishable.
//
// The one thing this table adds beyond grouping is the **rewind mark**, which is
// stated in full in docs/events.md and repeated here because the rule has to be
// visible where it is implemented:
//
//	A career is marked rewound when a `session.started` for it arrives carrying a
//	`sim_t` lower than the highest `sim_t` already seen in that career. That is
//	exactly "an earlier save of this career was loaded", because a save load is
//	the only thing that mints a session.
//
// The mark is **not** an integrity check and has no consequence: it excludes
// nothing, scores nothing and queues nothing (docs/CONSTITUTION.md §8). It is
// provenance on a derived number — a career time only means anything if the
// clock ran forward, so when it did not, the number is qualified rather than
// hidden. catlog cannot tell save-scumming from ordinary reloading and does not
// try; see docs/integrity-audit.md.
//
// Comparing only at session boundaries is what makes the rule threshold-free.
// Within a session the mod's emission order is very slightly lossy — a telemetry
// window closes with the sim time of its *end*, and `Flush` drains pending
// impacts after the frame loop has stopped — so a naive "any decrease" test
// would need an epsilon tuned to the window length. A save load has no such
// ambiguity.

// CareerState is a row of `career` (projections 0002).
type CareerState struct {
	PlayerID int64
	Career   string
	// MaxSimT is the highest sim_t ever observed in this career: the career's
	// high-water mark, and what a later session.started is compared against.
	MaxSimT float64
	// Rewound reports that an earlier save of this career has been loaded.
	Rewound  bool
	FirstSeq int64
}

// careerFold maintains `career`. It runs alongside [flightFold], before the
// boards, so that a board fold writing a time for career C is writing it after C
// exists.
//
// The mark is deliberately *not* copied into the board row's context. The read
// API resolves it from this table using the career the row already records,
// which makes it exact at every moment — a career rewound long after a record
// was set still shows the mark, with no rebuild needed.
type careerFold struct{}

func (careerFold) Name() string { return "career" }

func (careerFold) Apply(ctx context.Context, tx *sql.Tx, ev Event, _ FlightStateReader) error {
	if !ev.HasCareer() {
		return nil
	}
	if !ev.HasSimTime {
		// No clock reading: the career exists, but it contributes nothing to the
		// high-water mark and cannot be evidence of a rewind either way.
		return ensureCareer(ctx, tx, ev)
	}

	// The rewind test is `session.started` only, and it has to run *before* the
	// high-water mark is advanced by this same event.
	if ev.Type == "session.started" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE career SET rewound = 1 WHERE player_id = ? AND career = ? AND max_sim_t > ?`,
			ev.PlayerID, ev.Career, ev.SimTime); err != nil {
			return fmt.Errorf("stats: mark career %q rewound: %w", ev.Career, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO career (player_id, career, max_sim_t, rewound, first_seq) VALUES (?, ?, ?, 0, ?)
		 ON CONFLICT (player_id, career) DO UPDATE SET max_sim_t = excluded.max_sim_t
		 WHERE excluded.max_sim_t > career.max_sim_t`,
		ev.PlayerID, ev.Career, ev.SimTime, ev.Seq); err != nil {
		return fmt.Errorf("stats: advance career %q: %w", ev.Career, err)
	}
	return nil
}

func ensureCareer(ctx context.Context, tx *sql.Tx, ev Event) error {
	// OR IGNORE, never error inspection: tursogo collapses every constraint
	// violation onto one sentinel (docs/DECISIONS.md, WP1).
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO career (player_id, career, max_sim_t, rewound, first_seq) VALUES (?, ?, 0, 0, ?)`,
		ev.PlayerID, ev.Career, ev.Seq); err != nil {
		return fmt.Errorf("stats: ensure career %q: %w", ev.Career, err)
	}
	return nil
}
