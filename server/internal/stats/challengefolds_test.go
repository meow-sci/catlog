package stats

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func syntheticChallenge(key, scope string) Challenge {
	return Challenge{Key: key, Title: key, Blurb: key, Opens: 1_000, Closes: 2_000, Unit: "things", Scope: scope}
}

func syntheticFold(c Challenge, kind statKind, value challengeValue) Fold {
	folds, err := challengeFoldsFor([]Challenge{c}, map[string]challengeRule{c.Key: {kind: kind, value: value}})
	if err != nil {
		panic(err)
	}
	return folds[0]
}

type challengeTestRow struct {
	playerID          int64
	career, challenge string
	system, context   string
	value             float64
	seq               int64
}

func challengeRows(t *testing.T, p *store.Projections) []challengeTestRow {
	t.Helper()
	rows, err := p.Reader().QueryContext(t.Context(), `
		SELECT player_id, career, challenge, system, value, coalesce(context,''), updated_seq
		FROM challenge_stat ORDER BY player_id, career, system, challenge`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []challengeTestRow
	for rows.Next() {
		var row challengeTestRow
		if err := rows.Scan(&row.playerID, &row.career, &row.challenge, &row.system, &row.value, &row.context, &row.seq); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func applyChallengeBatches(t *testing.T, p *store.Projections, events []Event, folds []Fold, batchSize int, refined bool) {
	t.Helper()
	if batchSize <= 0 {
		batchSize = len(events)
	}
	run := func(events []Event, folds []Fold, refined bool) {
		t.Helper()
		if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
			var b *Batch
			if refined {
				b = NewRefinedBatch(tx, nil, BatchOptions{})
			} else {
				b = NewBatch(tx, BatchOptions{})
			}
			for _, ev := range events {
				for _, fold := range folds {
					if err := fold.Apply(t.Context(), b, ev); err != nil {
						return fmt.Errorf("%s: %w", fold.Name(), err)
					}
				}
			}
			return b.Flush(t.Context())
		}); err != nil {
			t.Fatal(err)
		}
	}
	if refined {
		for start := 0; start < len(events); start += batchSize {
			run(events[start:min(start+batchSize, len(events))], StateFolds(), false)
		}
		for start := 0; start < len(events); start += batchSize {
			run(events[start:min(start+batchSize, len(events))], folds, true)
		}
		return
	}
	all := append(StateFolds(), folds...)
	for start := 0; start < len(events); start += batchSize {
		run(events[start:min(start+batchSize, len(events))], all, false)
	}
}

func TestChallengeWindowGateRunsBeforeValue(t *testing.T) {
	c := syntheticChallenge("window_test", ScopePlayer)
	for _, tc := range []struct {
		name     string
		recv     int64
		wantRows int
	}{
		{"before", 999, 0},
		{"opening inclusive", 1_000, 1},
		{"inside", 1_500, 1},
		{"closing exclusive", 2_000, 0},
		{"after", 2_001, 0},
		{"no receive time", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			fold := syntheticFold(c, kindRecord, func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) {
				called = true
				return 1, nil, true, nil
			})
			p := testutil.MemProjections(t)
			applyChallengeBatches(t, p, []Event{{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: tc.recv}}, []Fold{fold}, 1, false)
			if got := len(challengeRows(t, p)); got != tc.wantRows {
				t.Errorf("rows = %d, want %d", got, tc.wantRows)
			}
			if called != (tc.wantRows == 1) {
				t.Errorf("value called = %v, want %v", called, tc.wantRows == 1)
			}
		})
	}
}

func TestChallengeWriteKindsMatchBoardMergesAcrossBatches(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        statKind
		values      []float64
		wantValue   float64
		wantSeq     int64
		wantContext string
	}{
		{"record", kindRecord, []float64{10, 10, 9, 11}, 11, 4, `{"candidate":4}`},
		{"best", kindBest, []float64{10, 10, 11, 9}, 9, 4, `{"candidate":4}`},
		{"count", kindCount, []float64{1, 2, 3, 4}, 10, 4, ""},
	} {
		for _, batchSize := range []int{1, DefaultFlushRows} {
			t.Run(fmt.Sprintf("%s/batch=%d", tc.name, batchSize), func(t *testing.T) {
				c := syntheticChallenge("merge_"+tc.name, ScopePlayer)
				fold := syntheticFold(c, tc.kind, func(_ context.Context, _ *Batch, ev Event) (float64, map[string]any, bool, error) {
					value := tc.values[ev.Seq-1]
					if tc.kind == kindCount {
						return value, nil, true, nil
					}
					return value, map[string]any{"candidate": ev.Seq}, true, nil
				})
				events := make([]Event, len(tc.values))
				for i := range events {
					events[i] = Event{Seq: int64(i + 1), PlayerID: 1, Type: "synthetic", RecvTime: 1_500}
				}
				p := testutil.MemProjections(t)
				applyChallengeBatches(t, p, events, []Fold{fold}, batchSize, false)
				rows := challengeRows(t, p)
				if len(rows) != 1 || rows[0].value != tc.wantValue || rows[0].seq != tc.wantSeq || rows[0].context != tc.wantContext {
					t.Errorf("rows = %+v, want value=%v seq=%d context=%q", rows, tc.wantValue, tc.wantSeq, tc.wantContext)
				}
			})
		}
	}
}

func TestChallengeFlushIsChunkedInCompositeKeyOrder(t *testing.T) {
	want := []challengeStatKey{
		{playerID: 1, career: "", system: "", challenge: "a"},
		{playerID: 1, career: "", system: "system", challenge: "z"},
		{playerID: 1, career: "save", system: "system", challenge: "a"},
		{playerID: 2, career: "", system: "", challenge: "a"},
	}
	for _, flushRows := range []int{1, 2, DefaultFlushRows} {
		t.Run(fmt.Sprintf("flush_rows=%d", flushRows), func(t *testing.T) {
			p := testutil.MemProjections(t)
			var got []challengeStatKey
			if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
				b := NewBatch(tx, BatchOptions{FlushRows: flushRows})
				for i := len(want) - 1; i >= 0; i-- {
					b.putChallengeStat(kindRecord, want[i], float64(i+1), nil, int64(i+1))
				}
				if err := b.Flush(t.Context()); err != nil {
					return err
				}
				got = slices.Clone(b.challengeStatKeys)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("flush order = %+v, want %+v", got, want)
			}
		})
	}
}

func TestChallengeMergeContinuesAcrossProjectionReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projections.db")
	first := testutil.ProjectionsAt(t, path)
	c := syntheticChallenge("reopen_test", ScopePlayer)
	fold := syntheticFold(c, kindRecord, func(_ context.Context, _ *Batch, ev Event) (float64, map[string]any, bool, error) {
		return ev.SimTime, map[string]any{"seq": ev.Seq}, true, nil
	})
	applyChallengeBatches(t, first, []Event{{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: 1_500, SimTime: 10}}, []Fold{fold}, 1, false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.OpenProjections(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	applyChallengeBatches(t, second, []Event{
		{Seq: 2, PlayerID: 1, Type: "synthetic", RecvTime: 1_500, SimTime: 10},
		{Seq: 3, PlayerID: 1, Type: "synthetic", RecvTime: 1_500, SimTime: 11},
	}, []Fold{fold}, 1, false)
	rows := challengeRows(t, second)
	if len(rows) != 1 || rows[0].value != 11 || rows[0].seq != 3 || rows[0].context != `{"seq":3}` {
		t.Errorf("row after reopen = %+v", rows)
	}
}

func TestChallengeContextUsesSharedDeterministicEncoder(t *testing.T) {
	c := syntheticChallenge("context_test", ScopePlayer)
	good := syntheticFold(c, kindRecord, func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) {
		return 1, map[string]any{"z": 2, "a": "first"}, true, nil
	})
	p := testutil.MemProjections(t)
	applyChallengeBatches(t, p, []Event{{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: 1_500}}, []Fold{good}, 1, false)
	if got := challengeRows(t, p); len(got) != 1 || got[0].context != `{"a":"first","z":2}` {
		t.Errorf("encoded context = %+v", got)
	}

	bad := syntheticFold(c, kindRecord, func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) {
		return 2, map[string]any{"bad": make(chan int)}, true, nil
	})
	err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, BatchOptions{})
		return bad.Apply(t.Context(), b, Event{Seq: 2, PlayerID: 1, Type: "synthetic", RecvTime: 1_500})
	})
	if err == nil || !strings.Contains(err.Error(), "encode context") {
		t.Errorf("unencodable context error = %v", err)
	}
}

func TestChallengeTieKeepsTheEarlierSeqAndContext(t *testing.T) {
	c := syntheticChallenge("tie_test", ScopePlayer)
	fold := syntheticFold(c, kindRecord, func(_ context.Context, _ *Batch, ev Event) (float64, map[string]any, bool, error) {
		return 10, map[string]any{"winner": ev.Seq}, true, nil
	})
	for _, batchSize := range []int{1, DefaultFlushRows} {
		p := testutil.MemProjections(t)
		applyChallengeBatches(t, p, []Event{
			{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: 1_500},
			{Seq: 2, PlayerID: 1, Type: "synthetic", RecvTime: 1_500},
		}, []Fold{fold}, batchSize, false)
		rows := challengeRows(t, p)
		if len(rows) != 1 || rows[0].seq != 1 || rows[0].context != `{"winner":1}` {
			t.Errorf("batch %d tie row = %+v", batchSize, rows)
		}
	}
}

func TestChallengeScopesAndMissingIdentity(t *testing.T) {
	const career, system = "testcareer000001", "testsystem000001"
	p := testutil.MemProjections(t)
	if _, err := p.Writer().ExecContext(t.Context(), `
		INSERT INTO career (player_id, career, first_seq, last_seq, ordinal, system)
		VALUES (1, ?, 1, 1, 1, ?)`, career, system); err != nil {
		t.Fatal(err)
	}
	var folds []Fold
	for _, scope := range []string{ScopePlayer, ScopeCareer, ScopeSystem} {
		c := syntheticChallenge("scope_"+scope, scope)
		folds = append(folds, syntheticFold(c, kindRecord, func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) {
			return 1, nil, true, nil
		}))
	}
	applyChallengeBatches(t, p, []Event{{Seq: 2, PlayerID: 1, Career: career, Type: "synthetic", RecvTime: 1_500}}, folds, 1, false)
	want := []challengeTestRow{
		{playerID: 1, career: "", challenge: "scope_player", system: "", value: 1, seq: 2},
		{playerID: 1, career: "", challenge: "scope_system", system: system, value: 1, seq: 2},
		{playerID: 1, career: career, challenge: "scope_career", system: system, value: 1, seq: 2},
	}
	if got := challengeRows(t, p); !slices.Equal(got, want) {
		t.Errorf("scope rows = %+v, want %+v", got, want)
	}

	missing := testutil.MemProjections(t)
	applyChallengeBatches(t, missing, []Event{{Seq: 1, PlayerID: 2, Type: "synthetic", RecvTime: 1_500}}, folds[1:], 1, false)
	if got := challengeRows(t, missing); len(got) != 0 {
		t.Errorf("missing identity wrote scoped rows: %+v", got)
	}
	unknownSystem := testutil.MemProjections(t)
	applyChallengeBatches(t, unknownSystem, []Event{{Seq: 1, PlayerID: 2, Career: career, Type: "synthetic", RecvTime: 1_500}}, folds[1:], 1, false)
	if got := challengeRows(t, unknownSystem); len(got) != 0 {
		t.Errorf("unknown system wrote scoped rows: %+v", got)
	}
}

func TestUnrefinedChallengeHistoryRebuildsIdentically(t *testing.T) {
	c := syntheticChallenge("rebuild_test", ScopePlayer)
	fold := syntheticFold(c, kindRecord, func(_ context.Context, _ *Batch, ev Event) (float64, map[string]any, bool, error) {
		return float64(ev.Seq), map[string]any{"seq": ev.Seq}, true, nil
	})
	events := []Event{
		{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: 999},
		{Seq: 2, PlayerID: 1, Type: "synthetic", RecvTime: 1_000},
		{Seq: 3, PlayerID: 1, Type: "synthetic", RecvTime: 1_500},
		{Seq: 4, PlayerID: 1, Type: "synthetic", RecvTime: 2_000},
	}
	var want []challengeTestRow
	for _, batchSize := range []int{1, 2, DefaultFlushRows} {
		p := testutil.MemProjections(t)
		applyChallengeBatches(t, p, events, []Fold{fold}, batchSize, false)
		got := challengeRows(t, p)
		if want == nil {
			want = got
		} else if !slices.Equal(got, want) {
			t.Errorf("batch %d = %+v, want %+v", batchSize, got, want)
		}
	}
	p := testutil.MemProjections(t)
	applyChallengeBatches(t, p, events, []Fold{fold}, 2, true)
	if got := challengeRows(t, p); !slices.Equal(got, want) {
		t.Errorf("refined replay = %+v, want %+v", got, want)
	}
}

func TestLateFlagRemovesAChallengeContributionOnRebuild(t *testing.T) {
	c := syntheticChallenge("late_flag_test", ScopePlayer)
	fold := syntheticFold(c, kindRecord, func(ctx context.Context, b *Batch, ev Event) (float64, map[string]any, bool, error) {
		if ev.Type != "synthetic" {
			return 0, nil, false, nil
		}
		ok, err := scoreable(ctx, ev, b)
		return 1, nil, ok, err
	})
	flight := ids.ID{1}
	events := []Event{
		{Seq: 1, PlayerID: 1, FlightID: flight, Type: "synthetic", RecvTime: 1_500},
		{Seq: 2, PlayerID: 1, FlightID: flight, Type: "flight.flagged", Payload: FlightFlagged{Flag: "teleport"}, RecvTime: 1_600},
	}
	incremental := testutil.MemProjections(t)
	applyChallengeBatches(t, incremental, events, []Fold{fold}, 1, false)
	if got := len(challengeRows(t, incremental)); got != 1 {
		t.Fatalf("incremental rows = %d, want optimistic row", got)
	}
	rebuilt := testutil.MemProjections(t)
	applyChallengeBatches(t, rebuilt, events, []Fold{fold}, 1, true)
	if got := challengeRows(t, rebuilt); len(got) != 0 {
		t.Errorf("late-flagged contribution survived refined rebuild: %+v", got)
	}
}

func TestFlightEligibilityBelongsToTheConcreteChallengeRule(t *testing.T) {
	c := syntheticChallenge("explicit_eligibility_test", ScopePlayer)
	flight := ids.ID{2}
	events := []Event{
		{Seq: 1, PlayerID: 1, FlightID: flight, Type: "flight.flagged", Payload: FlightFlagged{Flag: "teleport"}, RecvTime: 1_400},
		{Seq: 2, PlayerID: 1, FlightID: flight, Type: "synthetic", RecvTime: 1_500},
	}
	unsafeRule := syntheticFold(c, kindRecord, func(_ context.Context, _ *Batch, ev Event) (float64, map[string]any, bool, error) {
		return 1, nil, ev.Type == "synthetic", nil
	})
	p := testutil.MemProjections(t)
	applyChallengeBatches(t, p, events, []Fold{unsafeRule}, 1, false)
	if got := len(challengeRows(t, p)); got != 1 {
		t.Fatalf("generic engine unexpectedly applied flight eligibility: rows=%d", got)
	}

	safeRule := syntheticFold(c, kindRecord, func(ctx context.Context, b *Batch, ev Event) (float64, map[string]any, bool, error) {
		if ev.Type != "synthetic" {
			return 0, nil, false, nil
		}
		ok, err := scoreable(ctx, ev, b)
		return 1, nil, ok, err
	})
	safe := testutil.MemProjections(t)
	applyChallengeBatches(t, safe, events, []Fold{safeRule}, 1, false)
	if got := challengeRows(t, safe); len(got) != 0 {
		t.Errorf("explicitly scoreable rule accepted flagged flight: %+v", got)
	}
}

func TestAChallengeAddedAfterTheFactFillsFromHistoryOnRebuild(t *testing.T) {
	events := []Event{{Seq: 1, PlayerID: 1, Type: "synthetic", RecvTime: 1_500}}
	before := testutil.MemProjections(t)
	applyChallengeBatches(t, before, events, nil, 1, false)
	if got := challengeRows(t, before); len(got) != 0 {
		t.Fatalf("history without challenge produced rows: %+v", got)
	}
	c := syntheticChallenge("retroactive_test", ScopePlayer)
	fold := syntheticFold(c, kindCount, func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) {
		return 1, nil, true, nil
	})
	after := testutil.MemProjections(t)
	applyChallengeBatches(t, after, events, []Fold{fold}, 1, true)
	if got := challengeRows(t, after); len(got) != 1 || got[0].value != 1 {
		t.Errorf("rebuild with added challenge = %+v, want historical result", got)
	}
}

func TestChallengeDefinitionNameChangesBuildID(t *testing.T) {
	c := syntheticChallenge("build_id_test", ScopePlayer)
	folds, err := challengeFoldsFor([]Challenge{c}, map[string]challengeRule{c.Key: {
		kind:  kindCount,
		value: func(context.Context, *Batch, Event) (float64, map[string]any, bool, error) { return 1, nil, true, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"flight_state", "board", "event_census"}
	withChallenge := slices.Insert(slices.Clone(base), 2, folds[0].Name())
	if buildIDForNames(13, base) == buildIDForNames(13, withChallenge) {
		t.Error("adding a stable challenge definition/fold name did not change BuildID")
	}
}

func TestChallengeConstructionAndSecondPassUseActualValidation(t *testing.T) {
	if len(ChallengeFolds()) != 0 {
		t.Fatal("H3 shipped a concrete challenge fold")
	}
	second := SecondPassFolds()
	boards, badges := len(BoardFolds()), len(BadgeFolds())
	if second[boards].Name() != BadgeFolds()[0].Name() || second[boards+badges].Name() != LogFolds()[0].Name() {
		t.Errorf("second-pass order is not board -> badge -> challenge -> log: %v", foldNames(second))
	}
	if err := ValidateChallenges(); err != nil {
		t.Fatal(err)
	}
	order := secondPassFolds(
		[]Fold{namedChallengeTestFold("board")},
		[]Fold{namedChallengeTestFold("badge")},
		[]Fold{namedChallengeTestFold("challenge:test")},
		[]Fold{namedChallengeTestFold("log")},
	)
	if got := foldNames(order); !slices.Equal(got, []string{"board", "badge", "challenge:test", "log"}) {
		t.Errorf("injected second-pass order = %v", got)
	}
	_, err := challengeFoldsFor([]Challenge{syntheticChallenge("missing_rule", ScopePlayer)}, nil)
	if err == nil || !strings.Contains(err.Error(), "rule count") {
		t.Errorf("missing executable rule = %v", err)
	}
}

type namedChallengeTestFold string

func (f namedChallengeTestFold) Name() string                             { return string(f) }
func (namedChallengeTestFold) Apply(context.Context, *Batch, Event) error { return nil }
