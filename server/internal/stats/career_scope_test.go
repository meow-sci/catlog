package stats

import (
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

func readPlayerScopeStats(t *testing.T, proj *store.Projections) map[string]scopedRow {
	t.Helper()
	rows, err := proj.Reader().QueryContext(t.Context(),
		`SELECT player_id, stat, value, context, updated_seq
		 FROM player_stat ORDER BY player_id, stat`)
	if err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	defer rows.Close()

	out := map[string]scopedRow{}
	for rows.Next() {
		var (
			player, seq int64
			stat        string
			value       float64
			cx          sql.NullString
		)
		if err := rows.Scan(&player, &stat, &value, &cx, &seq); err != nil {
			t.Fatalf("scan player_stat: %v", err)
		}
		out[fmt.Sprintf("%d/%s", player, stat)] = scopedRow{
			value: value, context: cx.String, seq: seq,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("player_stat rows: %v", err)
	}
	return out
}

func scopeFlight(n byte) ids.ID {
	var id ids.ID
	id[0], id[15] = 1, n
	return id
}

func foldScopeEvents(t *testing.T, p *store.Projections, events ...Event) {
	t.Helper()
	withScopeBatch(t, p, 1000, func(b *Batch) error {
		for _, ev := range events {
			for _, fold := range Folds() {
				if err := fold.Apply(t.Context(), b, ev); err != nil {
					return fmt.Errorf("fold %s at seq %d: %w", fold.Name(), ev.Seq, err)
				}
			}
		}
		return nil
	})
}

func TestEveryBoardKindWritesItsCareerScope(t *testing.T) {
	p := scopeProjections(t)
	career := "testcareer000001"
	foldScopeEvents(t, p,
		Event{Seq: 1, PlayerID: 1, FlightID: scopeFlight(1), Career: career,
			Type: "vehicle.impact", Payload: VehicleImpact{
				SpeedMs: 120, Survived: true, Body: "luna", CrewCount: 1,
			}},
		Event{Seq: 2, PlayerID: 1, FlightID: scopeFlight(2), Career: career,
			Type: "vehicle.landed", Payload: VehicleLanded{
				Body: "luna", VerticalSpeedMs: 2, HorizontalSpeedMs: 1, CrewCount: 1, Survived: true,
			}},
		Event{Seq: 3, PlayerID: 1, FlightID: scopeFlight(3), Career: career,
			Type: "vehicle.staging", Payload: VehicleStaging{StageIndex: 1}},
	)

	player := readPlayerScopeStats(t, p)
	saves := readCareerStats(t, p)
	for _, stat := range []string{StatBiggestLithobrakeSurvived, StatSoftestLanding, StatStagings} {
		got := saves["1/"+career+"/"+stat]
		want := player["1/"+stat]
		if got.value != want.value || got.context != want.context || got.seq != want.seq {
			t.Errorf("%s career row = %+v, player row = %+v", stat, got, want)
		}
	}
}

func TestCareerScopeIsPerSaveNotPerPlayer(t *testing.T) {
	p := scopeProjections(t)
	careerA, careerB := "testcareer000001", "testcareer000002"
	var events []Event
	for i, career := range []string{careerA, careerA, careerB, careerA} {
		events = append(events, Event{
			Seq: int64(i + 1), PlayerID: 1, FlightID: scopeFlight(byte(i + 1)), Career: career,
			Type: "vehicle.landed", Payload: VehicleLanded{
				Body: "earth", VerticalSpeedMs: float64(i + 1), Survived: true,
			},
		})
	}
	foldScopeEvents(t, p, events...)

	player := readPlayerScopeStats(t, p)
	saves := readCareerStats(t, p)
	if got := player["1/"+StatLandings].value; got != 4 {
		t.Errorf("player landings = %v, want 4", got)
	}
	if got := saves["1/"+careerA+"/"+StatLandings].value; got != 3 {
		t.Errorf("career A landings = %v, want 3", got)
	}
	if got := saves["1/"+careerB+"/"+StatLandings].value; got != 1 {
		t.Errorf("career B landings = %v, want 1", got)
	}
}

func TestAnEventWithNoCareerWritesNoCareerRow(t *testing.T) {
	p := scopeProjections(t)
	foldScopeEvents(t, p, Event{
		Seq: 1, PlayerID: 1, FlightID: scopeFlight(1),
		Type: "vehicle.staging", Payload: VehicleStaging{StageIndex: 1},
	})
	if got := readPlayerScopeStats(t, p)["1/"+StatStagings].value; got != 1 {
		t.Errorf("player stagings = %v, want 1", got)
	}
	if got := readCareerStats(t, p); len(got) != 0 {
		t.Errorf("career rows = %v, want none", got)
	}
}

func TestCareerScopeTieKeepsTheEarlierSeq(t *testing.T) {
	p := scopeProjections(t)
	career := "testcareer000001"
	foldScopeEvents(t, p,
		Event{Seq: 1, PlayerID: 1, FlightID: scopeFlight(1), Career: career,
			Type: "vehicle.impact", Payload: VehicleImpact{SpeedMs: 80, Survived: true, Body: "earth", CrewCount: 1}},
		Event{Seq: 2, PlayerID: 1, FlightID: scopeFlight(2), Career: career,
			Type: "vehicle.impact", Payload: VehicleImpact{SpeedMs: 80, Survived: true, Body: "mars", CrewCount: 1}},
	)
	got := readCareerStats(t, p)["1/"+career+"/"+StatBiggestLithobrakeSurvived]
	if got.seq != 1 || got.context == "" {
		t.Errorf("tied career record = %+v, want the contextual seq-1 claim", got)
	}
}

func TestDynamicBoardsGetTheirCareerScopeForFree(t *testing.T) {
	p := scopeProjections(t)
	career := "testcareer000001"
	foldScopeEvents(t, p,
		Event{Seq: 1, PlayerID: 1, FlightID: scopeFlight(1), Career: career,
			Type: "vehicle.rud", Payload: VehicleRUD{Cause: "quantum_foam", Body: "earth"}},
		Event{Seq: 2, PlayerID: 1, FlightID: scopeFlight(2), Career: career,
			Type: "vehicle.soi", Payload: VehicleSOI{FromBody: "earth", ToBody: "zephyria_prime"},
			SimTime: 500, HasSimTime: true},
	)
	saves := readCareerStats(t, p)
	for stat, want := range map[string]float64{
		"rud_quantum_foam": 1, "fastest_to_zephyria_prime": 500_000,
	} {
		if got := saves["1/"+career+"/"+stat].value; got != want {
			t.Errorf("dynamic %s = %v, want %v", stat, got, want)
		}
	}
}

func TestTumbleSplitWritesEveryScopeWithoutChangingTheTotal(t *testing.T) {
	p := scopeProjections(t)
	career, system := "testcareer000001", "testsystem000001"
	seedCareer(t, p, 1, career, system)
	clean, flagged := scopeFlight(20), scopeFlight(21)
	foldScopeEvents(t, p,
		Event{Seq: 2, PlayerID: 1, FlightID: clean, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "grounded", Body: "earth"}},
		Event{Seq: 3, PlayerID: 1, FlightID: clean, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "airborne", Body: "earth"}},
		Event{Seq: 4, PlayerID: 1, FlightID: clean, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "future-mode", Body: "mars"}},
		Event{Seq: 5, PlayerID: 1, FlightID: clean, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "unknown", Body: "mars"}},
		// An unkeyable body retains both consequences that do not depend on a
		// family key: the all-tumble total and the airborne split.
		Event{Seq: 6, PlayerID: 1, FlightID: clean, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "airborne", Body: "bad/body"}},
		Event{Seq: 7, PlayerID: 1, FlightID: flagged, Career: career,
			Type: "flight.flagged", Payload: FlightFlagged{Flag: "tuning"}},
		Event{Seq: 8, PlayerID: 1, FlightID: flagged, Career: career,
			Type: "kitten.tumble", Payload: KittenTumble{From: "airborne", Body: "luna"}},
	)

	want := map[string]float64{
		StatKittenTumbles:   5,
		StatBotchedLandings: 2,
		"tumbles_on_earth":  2,
		"tumbles_on_mars":   2,
	}
	assertValues := func(label string, rows map[string]scopedRow, prefix string) {
		t.Helper()
		if len(rows) != len(want) {
			t.Errorf("%s rows = %v, want exactly %v", label, rows, want)
		}
		for stat, value := range want {
			if got := rows[prefix+stat].value; got != value {
				t.Errorf("%s %s = %v, want %v", label, stat, got, value)
			}
		}
	}
	assertValues("player", readPlayerScopeStats(t, p), "1/")
	assertValues("career", readCareerStats(t, p), "1/"+career+"/")
	assertValues("system", readSystemStats(t, p), "1/"+system+"/")
}

func TestFlaggedFlightScoresNothingInEitherScope(t *testing.T) {
	p := scopeProjections(t)
	career, system := "testcareer000001", "testsystem000001"
	seedCareer(t, p, 1, career, system)
	flight := scopeFlight(1)
	foldScopeEvents(t, p,
		Event{Seq: 1, PlayerID: 1, FlightID: flight, Career: career,
			Type: "flight.flagged", Payload: FlightFlagged{Flag: "teleport"}},
		Event{Seq: 2, PlayerID: 1, FlightID: flight, Career: career,
			Type: "vehicle.impact", Payload: VehicleImpact{SpeedMs: 900, Survived: true, CrewCount: 1}},
	)
	if got := readPlayerScopeStats(t, p); len(got) != 0 {
		t.Errorf("flagged flight scored player rows: %v", got)
	}
	if got := readCareerStats(t, p); len(got) != 0 {
		t.Errorf("flagged flight scored career rows: %v", got)
	}
	if got := readSystemStats(t, p); len(got) != 0 {
		t.Errorf("flagged flight scored system rows: %v", got)
	}
}

func TestDerivedTotalsUseScopeSpecificValues(t *testing.T) {
	p := scopeProjections(t)
	career, system := "testcareer000001", "testsystem000001"
	seedCareer(t, p, 1, career, system)
	withScopeBatch(t, p, 1000, func(b *Batch) error {
		ev := Event{Seq: 2, PlayerID: 1, Career: career}
		if err := setValue(t.Context(), b, ev, "derived", 100); err != nil {
			return err
		}
		if err := setCareerValue(t.Context(), b, ev, "derived", 40); err != nil {
			return err
		}
		return setSystemValue(t.Context(), b, ev, "derived", 70)
	})

	if got := readPlayerScopeStats(t, p)["1/derived"].value; got != 100 {
		t.Errorf("player derived total = %v, want 100", got)
	}
	if got := readCareerStats(t, p)["1/"+career+"/derived"].value; got != 40 {
		t.Errorf("career derived total = %v, want 40", got)
	}
	if got := readSystemStats(t, p)["1/"+system+"/derived"].value; got != 70 {
		t.Errorf("system derived total = %v, want 70", got)
	}
}
