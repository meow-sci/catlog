package stats

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

type badgeTestRow struct {
	playerID              int64
	career, badge, system string
	firstCareer           string
	earnedSeq, earnedAt   int64
	earnedSimT            sql.NullFloat64
	context               sql.NullString
}

func badgeRows(t *testing.T, p *store.Projections) []badgeTestRow {
	t.Helper()
	rows, err := p.Reader().QueryContext(t.Context(), `
		SELECT player_id, career, badge, system, first_career, earned_seq, earned_at, earned_sim_t, context
		FROM badge_award ORDER BY player_id, career, badge`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []badgeTestRow
	for rows.Next() {
		var r badgeTestRow
		if err := rows.Scan(&r.playerID, &r.career, &r.badge, &r.system, &r.firstCareer,
			&r.earnedSeq, &r.earnedAt, &r.earnedSimT, &r.context); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func withBadgeBatch(t *testing.T, p *store.Projections, opts BatchOptions, fn func(*Batch) error) {
	t.Helper()
	if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, opts)
		if err := fn(b); err != nil {
			return err
		}
		return b.Flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAwardWritesBothScopesWithExactServerProvenance(t *testing.T) {
	p := testutil.MemProjections(t)
	const career = "badgecareer00001"
	withBadgeBatch(t, p, BatchOptions{}, func(b *Batch) error {
		if err := b.BindCareerSystem(t.Context(), 7, career, "system-hash", 1); err != nil {
			return err
		}
		ev := Event{
			Seq: 8, PlayerID: 7, Career: career, FlightID: ids.ID{1},
			RecvTime: 1770000000123, WallTime: 999, SimTime: 0, HasSimTime: true,
		}
		if err := award(t.Context(), b, ev, BadgeFirstOrbit, map[string]any{"z": 2, "body": "luna"}); err != nil {
			return err
		}
		for _, scope := range []string{"", career} {
			has, err := b.HasBadge(t.Context(), 7, scope, BadgeFirstOrbit)
			if err != nil || !has {
				return fmt.Errorf("pending HasBadge(%q) = %v, %v", scope, has, err)
			}
		}
		// No career means lifetime only, empty system provenance and a NULL sim
		// time. The unknown key and untracked flight prove award itself validates
		// neither registry membership nor scoreability.
		return award(t.Context(), b, Event{
			Seq: 9, PlayerID: 7, FlightID: ids.ID{2}, RecvTime: 1770000000456,
		}, "future_badge", nil)
	})

	rows := badgeRows(t, p)
	if len(rows) != 3 {
		t.Fatalf("badge rows = %+v, want lifetime+career and lifetime-only", rows)
	}
	lifetime, save, future := rows[0], rows[2], rows[1]
	if lifetime.career != "" || lifetime.badge != BadgeFirstOrbit || lifetime.system != "system-hash" ||
		lifetime.firstCareer != career || lifetime.earnedSeq != 8 || lifetime.earnedAt != 1770000000123 ||
		!lifetime.earnedSimT.Valid || lifetime.earnedSimT.Float64 != 0 || lifetime.context.String != `{"body":"luna","z":2}` {
		t.Errorf("lifetime award = %+v", lifetime)
	}
	if save.career != career || save.badge != BadgeFirstOrbit || save.system != "system-hash" || save.firstCareer != "" ||
		save.earnedSeq != 8 || save.earnedAt != 1770000000123 || !save.earnedSimT.Valid || save.earnedSimT.Float64 != 0 ||
		save.context.String != lifetime.context.String {
		t.Errorf("save award = %+v", save)
	}
	if future.career != "" || future.badge != "future_badge" || future.system != "" || future.firstCareer != "" ||
		future.earnedSeq != 9 || future.earnedAt != 1770000000456 || future.earnedSimT.Valid || future.context.Valid {
		t.Errorf("careerless award = %+v", future)
	}
}

func TestPutBadgeKeepsLowestSeqAndMatchingFullProvenance(t *testing.T) {
	p := testutil.MemProjections(t)
	withBadgeBatch(t, p, BatchOptions{}, func(b *Batch) error {
		b.putBadge(3, "save", "test_badge", "late-system", "late-career",
			Event{Seq: 30, RecvTime: 300, SimTime: 30, HasSimTime: true}, `{"winner":"late"}`)
		b.putBadge(3, "save", "test_badge", "early-system", "early-career",
			Event{Seq: 10, RecvTime: 100, SimTime: 99, HasSimTime: false}, `{"winner":"early"}`)
		b.putBadge(3, "save", "test_badge", "middle-system", "middle-career",
			Event{Seq: 20, RecvTime: 200, SimTime: 0, HasSimTime: true}, `{"winner":"middle"}`)
		return nil
	})
	rows := badgeRows(t, p)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.system != "early-system" || r.firstCareer != "early-career" || r.earnedSeq != 10 || r.earnedAt != 100 ||
		r.earnedSimT.Valid || r.context.String != `{"winner":"early"}` {
		t.Errorf("winning row mixed candidate provenance: %+v", r)
	}
}

func TestHasBadgeReadsPendingAndSQLAndPostFlushDuplicateIsNoOp(t *testing.T) {
	p := testutil.MemProjections(t)
	withBadgeBatch(t, p, BatchOptions{}, func(b *Batch) error {
		has, err := b.HasBadge(t.Context(), 4, "", "once")
		if err != nil || has {
			return fmt.Errorf("missing HasBadge = %v, %v", has, err)
		}
		b.putBadge(4, "", "once", "original", "first", Event{Seq: 20, RecvTime: 200, SimTime: 2, HasSimTime: true}, "original")
		has, err = b.HasBadge(t.Context(), 4, "", "once")
		if err != nil || !has {
			return fmt.Errorf("pending HasBadge = %v, %v", has, err)
		}
		if err := b.Flush(t.Context()); err != nil {
			return err
		}
		b.putBadge(4, "", "once", "replacement", "earlier", Event{Seq: 10, RecvTime: 100}, "replacement")
		return nil
	})
	withBadgeBatch(t, p, BatchOptions{}, func(b *Batch) error {
		has, err := b.HasBadge(t.Context(), 4, "", "once")
		if err != nil || !has {
			return fmt.Errorf("SQL HasBadge = %v, %v", has, err)
		}
		b.putBadge(4, "", "once", "restart-replacement", "", Event{Seq: 1, RecvTime: 1}, nil)
		return nil
	})
	r := badgeRows(t, p)[0]
	if r.earnedSeq != 20 || r.system != "original" || r.context.String != "original" ||
		!r.earnedSimT.Valid || r.earnedSimT.Float64 != 2 {
		t.Errorf("post-flush duplicate replaced first row: %+v", r)
	}
}

func TestBadgeFlushChunksInCompositeKeyOrder(t *testing.T) {
	want := []badgeKey{{1, "", "a"}, {1, "", "z"}, {1, "b", "a"}, {2, "a", "a"}, {2, "b", "z"}}
	for _, flushRows := range []int{1, 0, 2, 10_000} {
		t.Run(fmt.Sprintf("flush_rows=%d", flushRows), func(t *testing.T) {
			p := testutil.MemProjections(t)
			var order []badgeKey
			withBadgeBatch(t, p, BatchOptions{FlushRows: flushRows}, func(b *Batch) error {
				for _, k := range []badgeKey{{2, "b", "z"}, {1, "b", "a"}, {1, "", "z"}, {1, "", "a"}, {2, "a", "a"}} {
					b.putBadge(k.playerID, k.career, k.badge, "", "", Event{Seq: k.playerID, RecvTime: 1}, nil)
				}
				if err := b.Flush(t.Context()); err != nil {
					return err
				}
				order = slices.Clone(b.badgeKeys)
				return nil
			})
			if !slices.Equal(order, want) {
				t.Errorf("flush order = %v, want %v", order, want)
			}
			if got := len(badgeRows(t, p)); got != len(want) {
				t.Errorf("chunked rows = %d, want %d", got, len(want))
			}
		})
	}
}

func TestBadgeFlushRollsBackWithItsTransaction(t *testing.T) {
	p := testutil.MemProjections(t)
	tx, err := p.Writer().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBatch(tx, BatchOptions{})
	b.putBadge(1, "", "rolled_back", "", "", Event{Seq: 1, RecvTime: 1}, nil)
	if err := b.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rows := badgeRows(t, p); len(rows) != 0 {
		t.Errorf("badge rows survived rollback: %+v", rows)
	}
}

func TestAwardRejectsUnencodableContextWithoutWriting(t *testing.T) {
	p := testutil.MemProjections(t)
	err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, BatchOptions{})
		err := award(t.Context(), b, Event{Seq: 1, PlayerID: 1}, "bad_context", map[string]any{"bad": make(chan int)})
		if err == nil {
			return errors.New("award accepted an unencodable context")
		}
		return b.Flush(t.Context())
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := badgeRows(t, p); len(rows) != 0 {
		t.Errorf("failed award wrote rows: %+v", rows)
	}
}
