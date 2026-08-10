package stats

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

type scopedRow struct {
	system  string
	value   float64
	context string
	seq     int64
}

// readCareerStats dumps career_stat as "player/career/stat" → exact row.
func readCareerStats(t *testing.T, proj *store.Projections) map[string]scopedRow {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, career, system, stat, value, context, updated_seq
		 FROM career_stat ORDER BY player_id, career, stat`)
	if err != nil {
		t.Fatalf("read career_stat: %v", err)
	}
	defer rows.Close()

	out := map[string]scopedRow{}
	for rows.Next() {
		var (
			player, seq          int64
			career, system, stat string
			value                float64
			cx                   sql.NullString
		)
		if err := rows.Scan(&player, &career, &system, &stat, &value, &cx, &seq); err != nil {
			t.Fatalf("scan career_stat: %v", err)
		}
		out[fmt.Sprintf("%d/%s/%s", player, career, stat)] = scopedRow{
			system: system, value: value, context: cx.String, seq: seq,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("career_stat rows: %v", err)
	}
	return out
}

func readSystemStats(t *testing.T, proj *store.Projections) map[string]scopedRow {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, system, stat, value, context, updated_seq
		 FROM system_stat ORDER BY player_id, system, stat`)
	if err != nil {
		t.Fatalf("read system_stat: %v", err)
	}
	defer rows.Close()

	out := map[string]scopedRow{}
	for rows.Next() {
		var (
			player, seq  int64
			system, stat string
			value        float64
			cx           sql.NullString
		)
		if err := rows.Scan(&player, &system, &stat, &value, &cx, &seq); err != nil {
			t.Fatalf("scan system_stat: %v", err)
		}
		out[fmt.Sprintf("%d/%s/%s", player, system, stat)] = scopedRow{
			system: system, value: value, context: cx.String, seq: seq,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("system_stat rows: %v", err)
	}
	return out
}

func scopeProjections(t *testing.T) *store.Projections {
	t.Helper()
	p, err := store.OpenProjections(t.Context(), store.MemoryPath, store.Options{})
	if err != nil {
		t.Fatalf("open projections: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("close projections: %v", err)
		}
	})
	return p
}

func withScopeBatch(t *testing.T, p *store.Projections, flushRows int, fn func(*Batch) error) {
	t.Helper()
	err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, BatchOptions{FlushRows: flushRows})
		if err := fn(b); err != nil {
			return err
		}
		return b.Flush(t.Context())
	})
	if err != nil {
		t.Fatalf("scope batch: %v", err)
	}
}

func seedCareer(t *testing.T, p *store.Projections, player int64, career, system string) {
	t.Helper()
	_, err := p.Writer().ExecContext(t.Context(),
		`INSERT INTO career (player_id, career, first_seq, last_seq, system) VALUES (?, ?, 1, 1, ?)`,
		player, career, system)
	if err != nil {
		t.Fatalf("seed career: %v", err)
	}
}

func TestScopeVocabulary(t *testing.T) {
	want := []string{ScopePlayer, ScopeCareer, ScopeSystem}
	got := Scopes()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Scopes() = %v, want %v", got, want)
	}
	for in, expect := range map[string]string{
		"": ScopePlayer, ScopePlayer: ScopePlayer, ScopeCareer: ScopeCareer, ScopeSystem: ScopeSystem,
	} {
		if got, ok := ValidScope(in); !ok || got != expect {
			t.Errorf("ValidScope(%q) = %q, %v; want %q, true", in, got, ok, expect)
		}
	}
	if got, ok := ValidScope("period"); ok || got != "" {
		t.Errorf("ValidScope(period) = %q, %v; want empty, false", got, ok)
	}
	// Callers cannot mutate a later result through an earlier slice.
	got[0] = "changed"
	if Scopes()[0] != ScopePlayer {
		t.Error("Scopes returned shared mutable storage")
	}
}

func TestScopedAccumulatorsMergeAndFlushInChunks(t *testing.T) {
	p := scopeProjections(t)
	seedCareer(t, p, 1, "testcareer000001", "testsystem000001")
	seedCareer(t, p, 1, "testcareer000002", "testsystem000001")
	seedCareer(t, p, 2, "testcareer000003", "")

	withScopeBatch(t, p, 1, func(b *Batch) error {
		ctx := t.Context()
		for _, write := range []struct {
			kind  statKind
			ev    Event
			stat  string
			value float64
			cx    any
		}{
			{kindRecord, Event{PlayerID: 1, Career: "testcareer000001", Seq: 2}, "record", 10, "first"},
			{kindRecord, Event{PlayerID: 1, Career: "testcareer000001", Seq: 3}, "record", 10, "tie"},
			{kindRecord, Event{PlayerID: 1, Career: "testcareer000001", Seq: 4}, "record", 12, "winner"},
			{kindBest, Event{PlayerID: 1, Career: "testcareer000001", Seq: 5}, "best", 9, "slow"},
			{kindBest, Event{PlayerID: 1, Career: "testcareer000001", Seq: 6}, "best", 7, "fast"},
			{kindCount, Event{PlayerID: 1, Career: "testcareer000001", Seq: 7}, "count", 1, nil},
			{kindCount, Event{PlayerID: 1, Career: "testcareer000001", Seq: 8}, "count", 2, nil},
			// A second save contributes independently to career scope and merges
			// into the same (player, system) counter.
			{kindCount, Event{PlayerID: 1, Career: "testcareer000002", Seq: 9}, "count", 4, nil},
			// Unknown system: career row yes, incomparable system row no.
			{kindCount, Event{PlayerID: 2, Career: "testcareer000003", Seq: 10}, "count", 5, nil},
		} {
			if err := b.putScoped(ctx, write.kind, write.ev, write.stat, write.value, write.cx); err != nil {
				return err
			}
		}

		ev := Event{PlayerID: 1, Career: "testcareer000001", Seq: 11}
		b.putCareerStat(kindSet, ev, "testsystem000001", "set", 4, "old")
		b.putSystemStat(kindSet, ev, "testsystem000001", "set", 4, "old")
		ev.Seq = 12
		b.putCareerStat(kindSet, ev, "testsystem000001", "set", 4, "same")
		b.putSystemStat(kindSet, ev, "testsystem000001", "set", 4, "same")
		ev.Seq = 13
		b.putCareerStat(kindSet, ev, "testsystem000001", "set", 6, "new")
		b.putSystemStat(kindSet, ev, "testsystem000001", "set", 6, "new")
		return nil
	})

	career := readCareerStats(t, p)
	for key, want := range map[string]scopedRow{
		"1/testcareer000001/record": {system: "testsystem000001", value: 12, context: "winner", seq: 4},
		"1/testcareer000001/best":   {system: "testsystem000001", value: 7, context: "fast", seq: 6},
		"1/testcareer000001/count":  {system: "testsystem000001", value: 3, seq: 8},
		"1/testcareer000002/count":  {system: "testsystem000001", value: 4, seq: 9},
		"1/testcareer000001/set":    {system: "testsystem000001", value: 6, context: "new", seq: 13},
		"2/testcareer000003/count":  {value: 5, seq: 10},
	} {
		if got := career[key]; got != want {
			t.Errorf("career %s = %+v, want %+v", key, got, want)
		}
	}
	if len(career) != 6 {
		t.Errorf("career rows = %d, want 6: %v", len(career), career)
	}

	systems := readSystemStats(t, p)
	for key, want := range map[string]scopedRow{
		"1/testsystem000001/record": {system: "testsystem000001", value: 12, context: "winner", seq: 4},
		"1/testsystem000001/best":   {system: "testsystem000001", value: 7, context: "fast", seq: 6},
		"1/testsystem000001/count":  {system: "testsystem000001", value: 7, seq: 9},
		"1/testsystem000001/set":    {system: "testsystem000001", value: 6, context: "new", seq: 13},
	} {
		if got := systems[key]; got != want {
			t.Errorf("system %s = %+v, want %+v", key, got, want)
		}
	}
	if len(systems) != 4 {
		t.Errorf("system rows = %d, want 4: %v", len(systems), systems)
	}
}

func TestScopedSQLMergeMatchesTheInMemoryMerge(t *testing.T) {
	p := scopeProjections(t)
	seedCareer(t, p, 1, "testcareer000001", "testsystem000001")

	write := func(seq int64, record, best, count, set float64) {
		withScopeBatch(t, p, 1000, func(b *Batch) error {
			ev := Event{PlayerID: 1, Career: "testcareer000001", Seq: seq}
			for kind, item := range map[statKind]struct {
				stat  string
				value float64
			}{
				kindRecord: {"record", record}, kindBest: {"best", best},
				kindCount: {"count", count}, kindSet: {"set", set},
			} {
				b.putCareerStat(kind, ev, "testsystem000001", item.stat, item.value, fmt.Sprintf("seq-%d", seq))
				b.putSystemStat(kind, ev, "testsystem000001", item.stat, item.value, fmt.Sprintf("seq-%d", seq))
			}
			return nil
		})
	}
	write(1, 10, 5, 2, 8)
	write(2, 9, 6, 3, 8) // neither record, best nor unchanged set moves

	for label, rows := range map[string]map[string]scopedRow{
		"career": readCareerStats(t, p), "system": readSystemStats(t, p),
	} {
		prefix := "1/testsystem000001/"
		if label == "career" {
			prefix = "1/testcareer000001/"
		}
		for stat, want := range map[string]scopedRow{
			"record": {system: "testsystem000001", value: 10, context: "seq-1", seq: 1},
			"best":   {system: "testsystem000001", value: 5, context: "seq-1", seq: 1},
			"count":  {system: "testsystem000001", value: 5, context: "seq-1", seq: 2},
			"set":    {system: "testsystem000001", value: 8, context: "seq-1", seq: 1},
		} {
			if got := rows[prefix+stat]; got != want {
				t.Errorf("%s %s = %+v, want %+v", label, stat, got, want)
			}
		}
	}
}

func TestCareerSystemAndCareerStatValueReadThrough(t *testing.T) {
	p := scopeProjections(t)
	seedCareer(t, p, 1, "testcareer000001", "testsystem000001")
	if _, err := p.Writer().ExecContext(t.Context(),
		`INSERT INTO career_stat (player_id, career, system, stat, value, updated_seq)
		 VALUES (1, 'testcareer000001', 'testsystem000001', 'derived', 7, 1)`); err != nil {
		t.Fatal(err)
	}

	withScopeBatch(t, p, 1000, func(b *Batch) error {
		system, err := b.CareerSystem(t.Context(), 1, "testcareer000001")
		if err != nil || system != "testsystem000001" {
			return fmt.Errorf("CareerSystem = %q, %v", system, err)
		}
		missing, err := b.CareerSystem(t.Context(), 1, "testcareer999999")
		if err != nil || missing != "" {
			return fmt.Errorf("missing CareerSystem = %q, %v", missing, err)
		}
		v, err := b.CareerStatValue(t.Context(), 1, "testcareer000001", "derived")
		if err != nil || v != 7 {
			return fmt.Errorf("stored CareerStatValue = %v, %v", v, err)
		}
		ev := Event{PlayerID: 1, Career: "testcareer000001", Seq: 2}
		b.putCareerStat(kindSet, ev, system, "derived", 11, nil)
		v, err = b.CareerStatValue(t.Context(), 1, "testcareer000001", "derived")
		if err != nil || v != 11 {
			return fmt.Errorf("pending CareerStatValue = %v, %v", v, err)
		}
		return nil
	})
}

func TestExistingFoldsDoNotWriteScopedRowsYet(t *testing.T) {
	p := scopeProjections(t)
	seedCareer(t, p, 1, "testcareer000001", "testsystem000001")

	withScopeBatch(t, p, 1000, func(b *Batch) error {
		ev := Event{
			Seq: 1, PlayerID: 1, FlightID: ids.ID{1}, Career: "testcareer000001",
			Type: "vehicle.staging", Payload: VehicleStaging{StageIndex: 1},
		}
		for _, fold := range Folds() {
			if err := fold.Apply(context.Background(), b, ev); err != nil {
				return fmt.Errorf("%s: %w", fold.Name(), err)
			}
		}
		return nil
	})

	if got := readCareerStats(t, p); len(got) != 0 {
		t.Errorf("existing folds wrote career scope before A3: %v", got)
	}
	if got := readSystemStats(t, p); len(got) != 0 {
		t.Errorf("existing folds wrote system scope before A3: %v", got)
	}
}
