package stats

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func runBadgeBatch(t *testing.T, p *store.Projections, flushRows int, fn func(*Batch) error) {
	t.Helper()
	if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, BatchOptions{FlushRows: flushRows})
		if err := fn(b); err != nil {
			return err
		}
		return b.Flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventBadgeIsOnceKeepsEarliestAndWritesBothScopes(t *testing.T) {
	p := testutil.MemProjections(t)
	fold := eventBadge{badge: "test_event", typ: "test.event"}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.EnsureCareer(t.Context(), 1, "save-a", 1); err != nil {
			return err
		}
		for _, seq := range []int64{30, 10, 20} {
			if err := fold.Apply(t.Context(), b, Event{Seq: seq, PlayerID: 1, Career: "save-a", Type: "test.event", RecvTime: seq}); err != nil {
				return err
			}
		}
		return nil
	})
	rows := badgeRows(t, p)
	if len(rows) != 2 || rows[0].career != "" || rows[1].career != "save-a" || rows[0].earnedSeq != 10 || rows[1].earnedSeq != 10 {
		t.Fatalf("event badge rows = %+v", rows)
	}
}

func TestEventBadgePerSaveDoesNotInventAnotherSave(t *testing.T) {
	p := testutil.MemProjections(t)
	fold := eventBadge{badge: "test_event", typ: "test.event"}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.EnsureCareer(t.Context(), 1, "save-a", 1); err != nil {
			return err
		}
		if err := b.EnsureCareer(t.Context(), 1, "save-b", 2); err != nil {
			return err
		}
		if err := fold.Apply(t.Context(), b, Event{Seq: 1, PlayerID: 1, Career: "save-a", Type: "test.event"}); err != nil {
			return err
		}
		return fold.Apply(t.Context(), b, Event{Seq: 2, PlayerID: 1, Career: "save-b", Type: "other.event"})
	})
	rows := badgeRows(t, p)
	if len(rows) != 2 || rows[0].earnedSeq != 1 || rows[1].career != "save-a" {
		t.Fatalf("per-save rows = %+v", rows)
	}
}

func TestThresholdBadgeReadsPostWriteValueAndSeparatesScopes(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-save", true: "split-saves"}[split], func(t *testing.T) {
			p := testutil.MemProjections(t)
			fold := thresholdBadge{badge: "ten_landings", stat: StatLandings, n: 10}
			runBadgeBatch(t, p, 0, func(b *Batch) error {
				for i := int64(1); i <= 12; i++ {
					career := "save-a"
					if split && i > 6 {
						career = "save-b"
					}
					ev := Event{Seq: i, PlayerID: 1, Career: career, RecvTime: 1770000000000 + i}
					if err := b.EnsureCareer(t.Context(), 1, career, i); err != nil {
						return err
					}
					if err := addCount(t.Context(), b, ev, StatLandings, 1); err != nil {
						return err
					}
					if err := fold.Apply(t.Context(), b, ev); err != nil {
						return err
					}
				}
				return nil
			})
			rows := badgeRows(t, p)
			if !split {
				if len(rows) != 2 || rows[0].earnedSeq != 10 || rows[1].earnedSeq != 10 {
					t.Fatalf("post-write threshold rows = %+v", rows)
				}
			} else if len(rows) != 1 || rows[0].career != "" || rows[0].earnedSeq != 10 {
				t.Fatalf("split threshold rows = %+v", rows)
			}
		})
	}
}

func TestBelowThresholdRejectsZero(t *testing.T) {
	f := thresholdBadge{badge: "below", stat: "soft", n: 0.5, below: true}
	if f.met(0) || f.met(-1) || !f.met(0.5) || !f.met(0.25) || f.met(0.6) {
		t.Error("below threshold does not implement 0 < value <= n")
	}
}

func TestBelowThresholdSeesFirstBestAfterEarlierAbsentRead(t *testing.T) {
	p := testutil.MemProjections(t)
	f := thresholdBadge{badge: "below", stat: "soft", n: 0.5, below: true}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
			return err
		}
		before := Event{Seq: 1, PlayerID: 1, Career: "save"}
		if err := f.Apply(t.Context(), b, before); err != nil {
			return err
		}
		after := Event{Seq: 2, PlayerID: 1, Career: "save"}
		if err := putBest(t.Context(), b, after, "soft", 0.25, nil); err != nil {
			return err
		}
		return f.Apply(t.Context(), b, after)
	})
	rows := badgeRows(t, p)
	if len(rows) != 2 || rows[0].earnedSeq != 2 || rows[1].earnedSeq != 2 {
		t.Fatalf("below threshold rows after absent read = %+v", rows)
	}
}

func TestCompositeCandidatesUseStoredFactsAndStartOrdering(t *testing.T) {
	crew := sql.NullInt64{Int64: 1, Valid: true}
	zeroEngines := sql.NullInt64{Int64: 0, Valid: true}
	orbit := Event{Seq: 5, Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved"}}
	if crewedOrbitCandidate(orbit, FlightState{StartedSeq: 6, Crew: crew}) ||
		!crewedOrbitCandidate(orbit, FlightState{StartedSeq: 4, Crew: crew}) {
		t.Error("crewed orbit ignored HasStartFactAt")
	}
	soi := Event{Seq: 5, Type: "vehicle.soi", Payload: VehicleSOI{ToBody: "luna"}}
	if coasterCandidate(soi, FlightState{StartedSeq: 6, EngineCount: zeroEngines}) ||
		!coasterCandidate(soi, FlightState{StartedSeq: 4, EngineCount: zeroEngines}) {
		t.Error("coaster ignored HasStartFactAt or explicit zero")
	}
	if coasterCandidate(soi, FlightState{StartedSeq: 4, EngineCount: sql.NullInt64{Int64: 1, Valid: true}}) ||
		coasterCandidate(soi, FlightState{StartedSeq: 4}) {
		t.Error("coaster treated installed or unknown engines as zero engines")
	}
	if crewedOrbitCandidate(orbit, FlightState{StartedSeq: 4, Crew: sql.NullInt64{Int64: 0, Valid: true}}) {
		t.Error("crewed orbit accepted an explicit zero crew")
	}
	ended := Event{Type: "flight.ended", Payload: FlightEnded{Reason: "recovered"}}
	ended.Seq = 7
	if !orbitAndBackCandidate(ended, FlightState{Milestones: MilestoneOrbit, FirstOrbitSeq: 5}) {
		t.Error("orbit-and-back rejected recovered orbit milestone")
	}
	docked := Event{Type: "vehicle.docked", Payload: VehicleDock{}}
	docked.Seq = 7
	if !dockedAfterOrbitCandidate(docked, FlightState{Milestones: MilestoneOrbit, FirstOrbitSeq: 5}) {
		t.Error("docked-after-orbit rejected orbit milestone")
	}
	if dockedAfterOrbitCandidate(docked, FlightState{Milestones: MilestoneOrbit, FirstOrbitSeq: 8}) ||
		orbitAndBackCandidate(ended, FlightState{Milestones: MilestoneOrbit, FirstOrbitSeq: 8}) {
		t.Error("future orbit sequence leaked into an earlier composite candidate")
	}
}

func TestOrbitCompositeOrderingIsBatchReloadAndRebuildStable(t *testing.T) {
	tests := []struct {
		name      string
		orbitSeq  int64
		candidate Event
		fold      compositeBadge
		want      int
	}{
		{"dock-after", 5, Event{Seq: 7, Type: "vehicle.docked", Payload: VehicleDock{}}, compositeBadge{badge: "dock", typ: "vehicle.docked", when: dockedAfterOrbitCandidate}, 2},
		{"dock-before", 8, Event{Seq: 7, Type: "vehicle.docked", Payload: VehicleDock{}}, compositeBadge{badge: "dock", typ: "vehicle.docked", when: dockedAfterOrbitCandidate}, 0},
		{"dock-same-seq", 7, Event{Seq: 7, Type: "vehicle.docked", Payload: VehicleDock{}}, compositeBadge{badge: "dock", typ: "vehicle.docked", when: dockedAfterOrbitCandidate}, 0},
		{"recovery-after", 5, Event{Seq: 7, Type: "flight.ended", Payload: FlightEnded{Reason: "recovered"}}, compositeBadge{badge: "home", typ: "flight.ended", when: orbitAndBackCandidate}, 2},
		{"recovery-before", 8, Event{Seq: 7, Type: "flight.ended", Payload: FlightEnded{Reason: "recovered"}}, compositeBadge{badge: "home", typ: "flight.ended", when: orbitAndBackCandidate}, 0},
		{"recovery-same-seq", 7, Event{Seq: 7, Type: "flight.ended", Payload: FlightEnded{Reason: "recovered"}}, compositeBadge{badge: "home", typ: "flight.ended", when: orbitAndBackCandidate}, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []string{"same-batch-final-state", "split-reload", "refined-rebuild"} {
				t.Run(mode, func(t *testing.T) {
					p := testutil.MemProjections(t)
					flight := ids.ID{9}
					candidate := test.candidate
					candidate.PlayerID, candidate.Career, candidate.FlightID = 1, "save", flight
					orbit := Event{Seq: test.orbitSeq, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved"}}

					if mode == "refined-rebuild" {
						runBadgeBatch(t, p, 0, func(b *Batch) error { return (flightFold{}).Apply(t.Context(), b, orbit) })
						if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
							b := NewRefinedBatch(tx, nil, BatchOptions{})
							if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
								return err
							}
							if err := test.fold.Apply(t.Context(), b, candidate); err != nil {
								return err
							}
							return b.Flush(t.Context())
						}); err != nil {
							t.Fatal(err)
						}
					} else if mode == "split-reload" && candidate.Seq < orbit.Seq {
						runBadgeBatch(t, p, 0, func(b *Batch) error {
							if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
								return err
							}
							return test.fold.Apply(t.Context(), b, candidate)
						})
						runBadgeBatch(t, p, 0, func(b *Batch) error { return (flightFold{}).Apply(t.Context(), b, orbit) })
					} else {
						runBadgeBatch(t, p, 0, func(b *Batch) error {
							if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
								return err
							}
							if err := (flightFold{}).Apply(t.Context(), b, orbit); err != nil {
								return err
							}
							if mode == "split-reload" {
								return nil
							}
							return test.fold.Apply(t.Context(), b, candidate)
						})
						if mode == "split-reload" {
							runBadgeBatch(t, p, 0, func(b *Batch) error { return test.fold.Apply(t.Context(), b, candidate) })
						}
					}
					if got := len(badgeRows(t, p)); got != test.want {
						t.Fatalf("badge rows = %d, want %d", got, test.want)
					}
				})
			}
		})
	}
}

func TestFirstOrbitSeqKeepsEarliestAcrossFlushAndReload(t *testing.T) {
	p := testutil.MemProjections(t)
	flight := ids.ID{10}
	for _, seq := range []int64{9, 12, 3, 6} {
		runBadgeBatch(t, p, 1, func(b *Batch) error {
			return (flightFold{}).Apply(t.Context(), b, Event{Seq: seq, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved"}})
		})
	}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		state, found, err := b.Flight(t.Context(), flight)
		if err != nil || !found {
			return err
		}
		if state.FirstOrbitSeq != 3 || state.Milestones&MilestoneOrbit == 0 {
			t.Fatalf("reloaded orbit state = seq %d milestones %d", state.FirstOrbitSeq, state.Milestones)
		}
		return nil
	})
}

func TestFlaggedFlightCandidateEarnsNoEventOrCompositeBadge(t *testing.T) {
	p := testutil.MemProjections(t)
	flight := ids.ID{1}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
			return err
		}
		if err := b.EnsureFlight(t.Context(), flight, 1, "save"); err != nil {
			return err
		}
		if err := b.StartFlight(t.Context(), flight, 1, "earth", nil, 1, 1, 1); err != nil {
			return err
		}
		if err := b.FlagFlight(t.Context(), flight, FlagTeleport); err != nil {
			return err
		}
		ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved"}}
		if err := (eventBadge{badge: "event", typ: "vehicle.orbit"}).Apply(t.Context(), b, ev); err != nil {
			return err
		}
		return (compositeBadge{badge: "composite", typ: "vehicle.orbit", when: crewedOrbitCandidate}).Apply(t.Context(), b, ev)
	})
	if rows := badgeRows(t, p); len(rows) != 0 {
		t.Errorf("flagged flight earned badges: %+v", rows)
	}
}

func TestLateFlagBadgeDivergenceIsCorrectedByRefinedReplay(t *testing.T) {
	project := func(t *testing.T, refined bool) []badgeTestRow {
		p := testutil.MemProjections(t)
		if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
			var b *Batch
			if refined {
				b = NewRefinedBatch(tx, nil, BatchOptions{})
			} else {
				b = NewBatch(tx, BatchOptions{})
			}
			flight := ids.ID{3}
			if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
				return err
			}
			if err := b.EnsureFlight(t.Context(), flight, 1, "save"); err != nil {
				return err
			}
			if refined {
				if err := b.FlagFlight(t.Context(), flight, FlagTeleport); err != nil {
					return err
				}
			}
			ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "candidate"}
			if err := (eventBadge{badge: "late_flag", typ: "candidate"}).Apply(t.Context(), b, ev); err != nil {
				return err
			}
			if !refined {
				if err := b.FlagFlight(t.Context(), flight, FlagTeleport); err != nil {
					return err
				}
			}
			return b.Flush(t.Context())
		}); err != nil {
			t.Fatal(err)
		}
		return badgeRows(t, p)
	}
	if got := project(t, false); len(got) != 2 {
		t.Fatalf("incremental late-flag rows = %+v, want optimistic award", got)
	}
	if got := project(t, true); len(got) != 0 {
		t.Fatalf("refined late-flag rows = %+v, want correction", got)
	}
}

func TestRefinedKIAAndRecoveryRulesRemoveThresholdBadges(t *testing.T) {
	impact := func(t *testing.T, refined bool) []badgeTestRow {
		p := testutil.MemProjections(t)
		flight := ids.ID{4}
		kia := map[ids.ID][]float64{flight: {10}}
		if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
			b := NewBatch(tx, BatchOptions{})
			if refined {
				b = NewRefinedBatch(tx, kia, BatchOptions{})
			}
			if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
				return err
			}
			ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.impact", SimTime: 10, HasSimTime: true,
				Payload: VehicleImpact{SpeedMs: 60, EnergyJ: 1, Survived: true, CrewCount: 1, Body: "luna"}}
			if err := (lithobrakeFold{}).Apply(t.Context(), b, ev); err != nil {
				return err
			}
			if err := (thresholdBadge{badge: "lithobraker_test", stat: StatBiggestLithobrakeSurvived, n: 50}).Apply(t.Context(), b, ev); err != nil {
				return err
			}
			return b.Flush(t.Context())
		}); err != nil {
			t.Fatal(err)
		}
		return badgeRows(t, p)
	}
	if len(impact(t, false)) != 2 || len(impact(t, true)) != 0 {
		t.Error("KIA refinement did not remove the survived-impact threshold badge")
	}

	load := func(t *testing.T, refined bool) []badgeTestRow {
		p := testutil.MemProjections(t)
		flight := ids.ID{5}
		if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
			b := NewBatch(tx, BatchOptions{})
			if refined {
				b = NewRefinedBatch(tx, nil, BatchOptions{})
			}
			if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
				return err
			}
			if err := b.EnsureFlight(t.Context(), flight, 1, "save"); err != nil {
				return err
			}
			if err := b.EndFlight(t.Context(), flight, "destroyed"); err != nil {
				return err
			}
			g := 12.0
			ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "telemetry.window", Payload: TelemetryWindow{PeakG: &g}}
			if err := (peakGFold{}).Apply(t.Context(), b, ev); err != nil {
				return err
			}
			if err := (thresholdBadge{badge: "pressed_test", stat: StatPeakGSurvived, n: 10}).Apply(t.Context(), b, ev); err != nil {
				return err
			}
			return b.Flush(t.Context())
		}); err != nil {
			t.Fatal(err)
		}
		return badgeRows(t, p)
	}
	if len(load(t, false)) != 2 || len(load(t, true)) != 0 {
		t.Error("recovery refinement did not remove the structural-load threshold badge")
	}
}

func TestHonestBadgeHistoryIsBatchAndRefinedStable(t *testing.T) {
	project := func(t *testing.T, batchSize int, refined bool) []badgeTestRow {
		p := testutil.MemProjections(t)
		events := make([]Event, 12)
		for i := range events {
			events[i] = Event{Seq: int64(i + 1), PlayerID: 1, Career: "save", Type: "landing", RecvTime: 1770000000000 + int64(i)}
		}
		for start := 0; start < len(events); start += batchSize {
			end := min(start+batchSize, len(events))
			if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
				b := NewBatch(tx, BatchOptions{})
				if refined {
					b = NewRefinedBatch(tx, nil, BatchOptions{})
				}
				for _, ev := range events[start:end] {
					if err := b.EnsureCareer(t.Context(), 1, "save", ev.Seq); err != nil {
						return err
					}
					if err := addCount(t.Context(), b, ev, StatLandings, 1); err != nil {
						return err
					}
					if err := (eventBadge{badge: "first", typ: "landing"}).Apply(t.Context(), b, ev); err != nil {
						return err
					}
					if err := (thresholdBadge{badge: "ten", stat: StatLandings, n: 10}).Apply(t.Context(), b, ev); err != nil {
						return err
					}
				}
				return b.Flush(t.Context())
			}); err != nil {
				t.Fatal(err)
			}
		}
		return badgeRows(t, p)
	}
	want := project(t, 1, false)
	for _, batchSize := range []int{3, 100} {
		if got := project(t, batchSize, false); !slices.Equal(got, want) {
			t.Errorf("batch %d badges = %+v, want %+v", batchSize, got, want)
		}
	}
	if got := project(t, 3, true); !slices.Equal(got, want) {
		t.Errorf("refined honest badges = %+v, want %+v", got, want)
	}
}

func TestUnkeyableFamilyBodySkipsOnlyBadge(t *testing.T) {
	p := testutil.MemProjections(t)
	flight := ids.ID{2}
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.BindCareerSystem(t.Context(), 1, "save", "system", 1); err != nil {
			return err
		}
		ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.soi", Payload: VehicleSOI{ToBody: "bad/body"}}
		if err := (soiFold{}).Apply(t.Context(), b, ev); err != nil {
			return err
		}
		return (reachedBodyBadge{}).Apply(t.Context(), b, ev)
	})
	if rows := badgeRows(t, p); len(rows) != 0 {
		t.Errorf("unkeyable body earned family badge: %+v", rows)
	}
	var value float64
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT value FROM player_stat WHERE player_id=1 AND stat=?`, StatSOIBodies).Scan(&value); err != nil || value != 1 {
		t.Errorf("tier source after unkeyable body = %v, %v; want 1", value, err)
	}
}

func TestSecondPassOrderNamesAndBuildIdentity(t *testing.T) {
	badges := BadgeFolds()
	if len(badges) != 38 {
		t.Fatalf("BadgeFolds() has %d entries, want 35 fixed and 3 families", len(badges))
	}
	second := SecondPassFolds()
	boards := BoardFolds()
	challenges := ChallengeFolds()
	if err := validateFoldNames(second); err != nil {
		t.Fatalf("actual second-pass names are not unique: %v", err)
	}
	if len(second) != len(boards)+len(badges)+len(challenges)+len(LogFolds()) ||
		second[len(boards)].Name() != "badge:"+BadgeFirstFlight ||
		second[len(boards)+len(badges)+len(challenges)].Name() != LogFolds()[0].Name() {
		t.Fatalf("second-pass boundary = %v", foldNames(second))
	}
	t.Run("constructor rejects duplicate names", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("second-pass constructor accepted duplicate fold names")
			}
		}()
		secondPassFolds(nil, []Fold{eventBadge{badge: "same"}, thresholdBadge{badge: "same"}}, nil, nil)
	})
	t.Run("constructor rejects empty names", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("second-pass constructor accepted an empty fold name")
			}
		}()
		secondPassFolds(nil, []Fold{testNamedFold{}}, nil, nil)
	})
	base := []string{"state", "board", "census"}
	added := slices.Insert(slices.Clone(base), 2, "badge:first_orbit")
	if buildIDForNames(12, base) == buildIDForNames(12, added) || buildIDForNames(12, added) == buildIDForNames(12, base) {
		t.Error("adding/removing a badge fold did not change build identity")
	}
	for _, test := range []struct {
		fold Fold
		want string
	}{
		{eventBadge{badge: "first_orbit"}, "badge:first_orbit"},
		{thresholdBadge{badge: "old_hand"}, "badge:old_hand"},
		{compositeBadge{badge: "crewed_orbit"}, "badge:crewed_orbit"},
		{reachedBodyBadge{}, "badge-family:reached"},
		{orbitedBodyBadge{}, "badge-family:orbited"},
		{landedOnBodyBadge{}, "badge-family:landed_on"},
	} {
		if test.fold.Name() != test.want {
			t.Errorf("fold name = %q, want %q", test.fold.Name(), test.want)
		}
	}
}

func TestActiveBadgeCatalogueCoverageOrderAndShapes(t *testing.T) {
	want := []struct {
		key, shape, source string
		n                  float64
		below              bool
	}{
		{BadgeFirstFlight, "event", "flight.started", 0, false},
		{BadgeFirstStage, "event", "vehicle.staging", 0, false},
		{BadgeFirstSpace, "event", "vehicle.atmosphere", 0, false},
		{BadgeFirstOrbit, "event", "vehicle.orbit", 0, false},
		{BadgeFirstLanding, "event", "vehicle.landed", 0, false},
		{BadgeFirstRecovery, "event", "flight.ended", 0, false},
		{BadgeFirstEVA, "event", "kitten.eva_start", 0, false},
		{BadgeFirstDock, "event", "vehicle.docked", 0, false},
		{BadgeFirstRUD, "event", "vehicle.rud", 0, false},
		{BadgeCrewedOrbit, "composite", "vehicle.orbit", 0, false},
		{BadgeOrbitAndBack, "composite", "flight.ended", 0, false},
		{BadgeDockedAfterOrbit, "composite", "vehicle.docked", 0, false},
		{BadgeCoaster, "composite", "vehicle.soi", 0, false},
		{BadgeHeavyLifter, "threshold", StatHeaviestToOrbit, 20_000, false},
		{BadgeBigStack, "threshold", StatBiggestStack, 5, false},
		{BadgeManyParts, "threshold", StatMostParts, 100, false},
		{BadgeWellLit, "threshold", StatEngineIgnitions, 100, false},
		{BadgeLithobraker, "threshold", StatBiggestLithobrakeSurvived, 50, false},
		{BadgeGroundTruth, "threshold", StatBiggestLithobrakeSurvived, 100, false},
		{BadgePressed, "threshold", StatPeakGSurvived, 10, false},
		{BadgeFeather, "threshold", StatSoftestLanding, 0.5, true},
		{BadgeCanyonRun, "threshold", StatLowestPass, 100, true},
		{BadgeOldHand, "threshold", StatLandings, 25, false},
		{BadgeWanderer, "threshold", StatSOIBodies, 3, false},
		{BadgeVoyager, "threshold", StatSOIBodies, 5, false},
		{BadgeGrandTour, "threshold", StatSOIBodies, 8, false},
		{BadgeGroundskeeper, "threshold", StatLandedBodies, 3, false},
		{BadgeBeenToEveryPlanet, "everywhere", "planet", 0, false},
		{BadgeBeenToEverything, "everywhere", "", 0, false},
		{"reached_", "family", "vehicle.soi", 0, false},
		{"orbited_", "family", "vehicle.orbit", 0, false},
		{"landed_on_", "family", "vehicle.landed", 0, false},
		{BadgeNotOnTheirFeet, "event", "kitten.tumble", 0, false},
		{BadgePersistentlyUpsideDown, "threshold", StatKittenTumbles, 50, false},
		{BadgeCrowdedCapsule, "threshold", StatBiggestRecovery, 4, false},
		{BadgeSpacewalker, "threshold", StatEVAs, 10, false},
		{BadgeTheLongWalk, "threshold", StatLongestEVA, 3_600, false},
		{BadgeFerryService, "threshold", StatKittensToOrbitAndBack, 10, false},
	}

	folds := BadgeFolds()
	if len(folds) != len(want) {
		t.Fatalf("active folds = %d, want %d", len(folds), len(want))
	}
	activeFixed := map[string]bool{}
	for i, fold := range folds {
		got := want[i]
		switch f := fold.(type) {
		case eventBadge:
			if got.shape != "event" || f.badge != got.key || f.typ != got.source {
				t.Errorf("fold %d = event %q/%q, want %+v", i, f.badge, f.typ, got)
			}
			activeFixed[f.badge] = true
		case compositeBadge:
			if got.shape != "composite" || f.badge != got.key || f.typ != got.source {
				t.Errorf("fold %d = composite %q/%q, want %+v", i, f.badge, f.typ, got)
			}
			activeFixed[f.badge] = true
		case thresholdBadge:
			if got.shape != "threshold" || f.badge != got.key || f.stat != got.source || f.n != got.n || f.below != got.below {
				t.Errorf("fold %d = threshold %q/%q/%g/%v, want %+v", i, f.badge, f.stat, f.n, f.below, got)
			}
			activeFixed[f.badge] = true
		case everywhereBadge:
			if got.shape != "everywhere" || f.badge != got.key || f.kind != got.source {
				t.Errorf("fold %d = everywhere %q/%q, want %+v", i, f.badge, f.kind, got)
			}
			activeFixed[f.badge] = true
		case reachedBodyBadge:
			if got.key != "reached_" || got.source != "vehicle.soi" {
				t.Errorf("fold %d = reached family, want %+v", i, got)
			}
		case orbitedBodyBadge:
			if got.key != "orbited_" || got.source != "vehicle.orbit" {
				t.Errorf("fold %d = orbited family, want %+v", i, got)
			}
		case landedOnBodyBadge:
			if got.key != "landed_on_" || got.source != "vehicle.landed" {
				t.Errorf("fold %d = landed family, want %+v", i, got)
			}
		default:
			t.Errorf("fold %d has unexpected type %T", i, fold)
		}
	}
	for _, badge := range FixedBadges() {
		if !activeFixed[badge.Key] {
			t.Errorf("fixed badge %q is not active", badge.Key)
		}
	}
	withoutBadges := append(foldNames(StateFolds()), foldNames(BoardFolds())...)
	withoutBadges = append(withoutBadges, foldNames(LogFolds())...)
	if BuildID(12) == buildIDForNames(12, withoutBadges) {
		t.Error("activating the F5 catalogue did not change BuildID from the F4 fold set")
	}
}

func TestActiveEventPredicatesUseExactBoundaries(t *testing.T) {
	tests := []struct {
		name string
		when func(Event) bool
		yes  Event
		no   Event
	}{
		{"space", atmosphereExited, Event{Type: "vehicle.atmosphere", Payload: VehicleAtmosphere{Dir: "exited"}}, Event{Type: "vehicle.atmosphere", Payload: VehicleAtmosphere{Dir: "entered"}}},
		{"orbit", orbitAchieved, Event{Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved"}}, Event{Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "escaped"}}},
		{"landing", landingSurvived, Event{Type: "vehicle.landed", Payload: VehicleLanded{Survived: true}}, Event{Type: "vehicle.landed", Payload: VehicleLanded{Survived: false}}},
		{"recovery", flightRecovered, Event{Type: "flight.ended", Payload: FlightEnded{Reason: "recovered"}}, Event{Type: "flight.ended", Payload: FlightEnded{Reason: "destroyed"}}},
		{"tumble", tumbleFromAirborne, Event{Type: "kitten.tumble", Payload: KittenTumble{From: "airborne"}}, Event{Type: "kitten.tumble", Payload: KittenTumble{From: "grounded"}}},
	}
	for _, test := range tests {
		if !test.when(test.yes) || test.when(test.no) || test.when(Event{Payload: struct{}{}}) {
			t.Errorf("%s predicate did not keep its exact payload boundary", test.name)
		}
	}
}

func TestEveryActiveThresholdPredicateUsesItsExactBoundary(t *testing.T) {
	count := 0
	for _, fold := range BadgeFolds() {
		f, ok := fold.(thresholdBadge)
		if !ok {
			continue
		}
		count++
		t.Run(f.badge, func(t *testing.T) {
			if !f.met(f.n) {
				t.Errorf("boundary %g did not qualify", f.n)
			}
			if f.below {
				if !f.met(f.n/2) || f.met(0) || f.met(f.n+0.01) {
					t.Errorf("below predicate is not 0 < value <= %g", f.n)
				}
				return
			}
			if !f.met(f.n+1) || f.met(f.n-0.01) {
				t.Errorf("threshold predicate is not value >= %g", f.n)
			}
		})
	}
	if count != 19 {
		t.Fatalf("tested %d active thresholds, want all 19", count)
	}
}

func TestExplorationTiersCoexistAndFixedContextsAreNull(t *testing.T) {
	p := testutil.MemProjections(t)
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		ev := Event{Seq: 8, PlayerID: 1, Career: "save", Type: "threshold"}
		if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
			return err
		}
		if err := setValue(t.Context(), b, ev, StatSOIBodies, 8); err != nil {
			return err
		}
		if err := setCareerValue(t.Context(), b, ev, StatSOIBodies, 8); err != nil {
			return err
		}
		for _, fold := range BadgeFolds() {
			if f, ok := fold.(thresholdBadge); ok && (f.badge == BadgeWanderer || f.badge == BadgeVoyager || f.badge == BadgeGrandTour) {
				if err := f.Apply(t.Context(), b, ev); err != nil {
					return err
				}
			}
		}
		return nil
	})
	rows := badgeRows(t, p)
	if len(rows) != 6 {
		t.Fatalf("tier rows = %+v, want all three in both scopes", rows)
	}
	for _, row := range rows {
		if row.context.Valid {
			t.Errorf("fixed badge %q context = %q, want SQL NULL", row.badge, row.context.String)
		}
	}
}

func TestFamilyBadgeContextContainsOnlyBody(t *testing.T) {
	p := testutil.MemProjections(t)
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := b.EnsureCareer(t.Context(), 1, "save", 1); err != nil {
			return err
		}
		ev := Event{Seq: 2, PlayerID: 1, Career: "save", FlightID: ids.ID{12}, Type: "vehicle.soi", Payload: VehicleSOI{ToBody: "luna"}}
		return (reachedBodyBadge{}).Apply(t.Context(), b, ev)
	})
	for _, row := range badgeRows(t, p) {
		if !row.context.Valid || row.context.String != `{"body":"luna"}` {
			t.Errorf("family context = %+v, want only body", row.context)
		}
	}
}

const everywhereTestSystem = "everywhere-system"

func everywhereSystemBody(body, kind, class string, parent *string) SystemBody {
	return SystemBody{
		System: everywhereTestSystem, Body: body, Name: body, Kind: kind, Class: class,
		Parent: parent, RadiusM: 1, MassKg: 1, Axis: Vec3{Z: 1}, CcfToCceT0: Quat{W: 1},
	}
}

func prepareEverywhereSystem(ctx context.Context, b *Batch, complete bool, declared int, bodies ...SystemBody) error {
	if err := b.UpsertSystem(ctx, SystemDiscovered{
		System: everywhereTestSystem, ID: "test", Name: "Test", Home: "root",
		Bodies: declared, Complete: complete,
	}, 1); err != nil {
		return err
	}
	if err := b.BindCareerSystem(ctx, 1, "save", everywhereTestSystem, 1); err != nil {
		return err
	}
	for i, body := range bodies {
		if err := b.InsertSystemBody(ctx, body, int64(i+2)); err != nil {
			return err
		}
	}
	return nil
}

func applyEverywhereSOI(ctx context.Context, b *Batch, seq int64, body string) error {
	ev := Event{
		Seq: seq, PlayerID: 1, Career: "save", Type: "vehicle.soi",
		Payload: VehicleSOI{ToBody: body}, RecvTime: 1_700_000_000_000 + seq,
	}
	if err := (soiFold{}).Apply(ctx, b, ev); err != nil {
		return err
	}
	for _, fold := range []Fold{
		everywhereBadge{badge: BadgeBeenToEveryPlanet, kind: "planet"},
		everywhereBadge{badge: BadgeBeenToEverything},
	} {
		if err := fold.Apply(ctx, b, ev); err != nil {
			return err
		}
	}
	return nil
}

func TestEverywhereAwardsOnLastMissingBodyAcrossBatchBoundaries(t *testing.T) {
	root := "root"
	bodies := []SystemBody{
		everywhereSystemBody(root, "star", "RootClass", nil),
		everywhereSystemBody("alpha", "planet", "PlanetClass", &root),
		everywhereSystemBody("beta", "planet", "FutureUnknownPlanetClass", &root),
	}
	for _, split := range []bool{false, true} {
		name := map[bool]string{false: "large-pending-batch", true: "one-event-batches"}[split]
		t.Run(name, func(t *testing.T) {
			p := testutil.MemProjections(t)
			setup := func(b *Batch) error {
				return prepareEverywhereSystem(t.Context(), b, true, len(bodies), bodies...)
			}
			visits := []string{root, "alpha", "beta"}
			if split {
				runBadgeBatch(t, p, 1, setup)
				for i, body := range visits[:2] {
					seq := int64(10 + i)
					runBadgeBatch(t, p, 1, func(b *Batch) error { return applyEverywhereSOI(t.Context(), b, seq, body) })
				}
				if got := len(badgeRows(t, p)); got != 0 {
					t.Fatalf("awards before final body = %d", got)
				}
				runBadgeBatch(t, p, 1, func(b *Batch) error { return applyEverywhereSOI(t.Context(), b, 12, "beta") })
			} else {
				runBadgeBatch(t, p, 0, func(b *Batch) error {
					if err := setup(b); err != nil {
						return err
					}
					for i, body := range visits[:2] {
						if err := applyEverywhereSOI(t.Context(), b, int64(10+i), body); err != nil {
							return err
						}
					}
					for _, badge := range []string{BadgeBeenToEveryPlanet, BadgeBeenToEverything} {
						earned, err := b.HasBadge(t.Context(), 1, "", badge)
						if err != nil || earned {
							return fmt.Errorf("%s before final body = %v, %v", badge, earned, err)
						}
					}
					return applyEverywhereSOI(t.Context(), b, 12, "beta")
				})
			}

			rows := badgeRows(t, p)
			if len(rows) != 4 {
				t.Fatalf("final everywhere rows = %+v", rows)
			}
			for _, row := range rows {
				if row.system != everywhereTestSystem || row.earnedSeq != 12 || row.context.Valid {
					t.Errorf("everywhere row = %+v", row)
				}
				if row.career == "" && row.firstCareer != "save" {
					t.Errorf("lifetime provenance = %+v", row)
				}
				if row.career == "save" && row.firstCareer != "" {
					t.Errorf("save provenance = %+v", row)
				}
			}
		})
	}
}

func TestEverywhereRefusesIncompleteAndVacuousCatalogues(t *testing.T) {
	root := everywhereSystemBody("root", "star", "RootClass", nil)
	planet := everywhereSystemBody("planet", "planet", "UnknownFutureClass", nil)
	tests := []struct {
		name     string
		header   bool
		complete bool
		declared int
		bodies   []SystemBody
		fold     everywhereBadge
		visit    string
	}{
		{"missing-header", false, false, 0, nil, everywhereBadge{badge: BadgeBeenToEverything}, "root"},
		{"reported-incomplete", true, false, 1, []SystemBody{root}, everywhereBadge{badge: BadgeBeenToEverything}, "root"},
		{"short-catalogue", true, true, 2, []SystemBody{root}, everywhereBadge{badge: BadgeBeenToEverything}, "root"},
		{"empty-planet-subset", true, true, 1, []SystemBody{root}, everywhereBadge{badge: BadgeBeenToEveryPlanet, kind: "planet"}, "root"},
		{"empty-all-subset", true, true, 0, nil, everywhereBadge{badge: BadgeBeenToEverything}, "invented"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := testutil.MemProjections(t)
			runBadgeBatch(t, p, 0, func(b *Batch) error {
				if test.header {
					if err := prepareEverywhereSystem(t.Context(), b, test.complete, test.declared, test.bodies...); err != nil {
						return err
					}
				} else if err := b.BindCareerSystem(t.Context(), 1, "save", everywhereTestSystem, 1); err != nil {
					return err
				}
				ev := Event{Seq: 10, PlayerID: 1, Career: "save", Type: "vehicle.soi", Payload: VehicleSOI{ToBody: test.visit}}
				if err := (soiFold{}).Apply(t.Context(), b, ev); err != nil {
					return err
				}
				return test.fold.Apply(t.Context(), b, ev)
			})
			if rows := badgeRows(t, p); len(rows) != 0 {
				t.Errorf("ineligible catalogue awarded: %+v", rows)
			}
		})
	}

	// An unknown concrete class is irrelevant: the emitted normalized kind is
	// the subset contract, so this one-body planet catalogue awards both keys.
	p := testutil.MemProjections(t)
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := prepareEverywhereSystem(t.Context(), b, true, 1, planet); err != nil {
			return err
		}
		return applyEverywhereSOI(t.Context(), b, 10, "planet")
	})
	if rows := badgeRows(t, p); len(rows) != 4 {
		t.Fatalf("explicit planet kind with unknown class rows = %+v", rows)
	}
}

func TestEverywhereDoesNotRetroAwardWhenCatalogueCompletes(t *testing.T) {
	p := testutil.MemProjections(t)
	root := everywhereSystemBody("root", "star", "RootClass", nil)
	planet := everywhereSystemBody("planet", "planet", "PlanetClass", nil)
	runBadgeBatch(t, p, 0, func(b *Batch) error {
		if err := prepareEverywhereSystem(t.Context(), b, true, 2, root); err != nil {
			return err
		}
		return applyEverywhereSOI(t.Context(), b, 10, "root")
	})
	runBadgeBatch(t, p, 0, func(b *Batch) error { return b.InsertSystemBody(t.Context(), planet, 11) })
	if rows := badgeRows(t, p); len(rows) != 0 {
		t.Fatalf("catalogue completion retro-awarded without an SOI: %+v", rows)
	}
	runBadgeBatch(t, p, 0, func(b *Batch) error { return applyEverywhereSOI(t.Context(), b, 12, "planet") })
	if rows := badgeRows(t, p); len(rows) != 4 {
		t.Fatalf("qualifying SOI after catalogue completion rows = %+v", rows)
	}
}

func TestActiveCatalogueIsSameAcrossBatchSplitsAndRefinedReplay(t *testing.T) {
	flight := ids.ID{13}
	zero := 0
	events := []Event{
		{Seq: 1, PlayerID: 1, Career: "save", FlightID: flight, Type: "flight.started", Payload: FlightStarted{Body: "earth", CrewCount: 1, EngineCount: &zero, PartCount: 100, StageCount: 5}},
		{Seq: 2, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.orbit", Payload: VehicleOrbit{Phase: "achieved", Body: "earth", MassKg: 20_000}},
		{Seq: 3, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.soi", Payload: VehicleSOI{FromBody: "earth", ToBody: "luna"}},
		{Seq: 4, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.docked", Payload: VehicleDock{}},
		{Seq: 5, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.landed", Payload: VehicleLanded{Body: "luna", Survived: true, VerticalSpeedMs: 0.5}},
		{Seq: 6, PlayerID: 1, Career: "save", FlightID: flight, Type: "flight.ended", Payload: FlightEnded{Reason: "recovered", CrewCount: 4, Kids: []string{"a"}}},
		{Seq: 7, PlayerID: 1, Career: "save", FlightID: flight, Type: "kitten.tumble", Payload: KittenTumble{From: "airborne", Body: "luna"}},
		{Seq: 8, PlayerID: 1, Career: "save", FlightID: flight, Type: "kitten.eva_start", Payload: struct{}{}},
		{Seq: 9, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.staging", Payload: VehicleStaging{StageIndex: 0}},
		{Seq: 10, PlayerID: 1, Career: "save", FlightID: flight, Type: "vehicle.rud", Payload: VehicleRUD{Body: "luna", Cause: "collision", PartCount: 1}},
	}

	project := func(t *testing.T, mode string) []badgeTestRow {
		p := testutil.MemProjections(t)
		applyEvents := func(folds []Fold, refined bool, subset []Event) {
			t.Helper()
			if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
				b := NewBatch(tx, BatchOptions{})
				if refined {
					b = NewRefinedBatch(tx, nil, BatchOptions{})
				}
				for _, ev := range subset {
					for _, fold := range folds {
						if err := fold.Apply(t.Context(), b, ev); err != nil {
							return err
						}
					}
				}
				return b.Flush(t.Context())
			}); err != nil {
				t.Fatal(err)
			}
		}

		switch mode {
		case "same":
			applyEvents(Folds(), false, events)
		case "split":
			for _, ev := range events {
				applyEvents(Folds(), false, []Event{ev})
			}
		case "refined":
			applyEvents(StateFolds(), false, events)
			applyEvents(SecondPassFolds(), true, events)
		default:
			t.Fatalf("unknown mode %q", mode)
		}
		return badgeRows(t, p)
	}

	want := project(t, "same")
	if len(want) == 0 {
		t.Fatal("representative active catalogue history earned no badges")
	}
	for _, mode := range []string{"split", "refined"} {
		if got := project(t, mode); !slices.Equal(got, want) {
			t.Errorf("%s badge projection = %+v, want %+v", mode, got, want)
		}
	}
}

func foldNames(folds []Fold) []string {
	out := make([]string, len(folds))
	for i, fold := range folds {
		out[i] = fold.Name()
	}
	return out
}

type testNamedFold struct{ name string }

func (f testNamedFold) Name() string                             { return f.name }
func (testNamedFold) Apply(context.Context, *Batch, Event) error { return nil }
