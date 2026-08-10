package projector_test

import (
	"context"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// statRows is every board row a player holds, keyed by stat.
func statRows(t *testing.T, r *rig, playerID int64) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	err := r.live.With(func(p *store.Projections) error {
		rows, err := p.PlayerStats(t.Context(), playerID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out[row.Stat] = row.Value
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read player stats: %v", err)
	}
	return out
}

// TestShadowbanLeavesTheBoardsOnRebuild is the end-to-end claim the whole
// feature makes: withhold a player's log, rebuild, and every record they held is
// gone — while the player beside them is untouched.
//
// It is deliberately an integration test across the two files. The store test
// proves the rows moved; only this proves that moving them is *sufficient* —
// that no board, no career row and no flight row keeps a copy of what they did.
func TestShadowbanLeavesTheBoardsOnRebuild(t *testing.T) {
	r := newRig(t)
	subject, bystander := r.player("griefer"), r.player("honest_cat")

	r.ship(subject, cleanHistory(1)...)
	r.ship(bystander, cleanHistory(100)...)
	r.drain()

	before := statRows(t, r, subject)
	if len(before) == 0 {
		t.Fatal("the subject holds no board rows, so this test would prove nothing")
	}
	bystanderBefore := statRows(t, r, bystander)

	moved, err := r.events.ShadowbanPlayer(t.Context(), subject, rigNow.UnixMilli(), "harassment")
	if err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	if moved == 0 {
		t.Fatal("the shadow ban withheld no events")
	}

	// Before the rebuild the boards still hold their records: the projector's
	// cursor only moves forward, so removing events from the log cannot take
	// back what they already scored. This is exactly why every moderation verb
	// queues a rebuild, and why the handle directory hides them meanwhile.
	if got := statRows(t, r, subject); len(got) != len(before) {
		t.Errorf("board rows changed without a rebuild: %d, want %d", len(got), len(before))
	}

	r.rebuild()

	if got := statRows(t, r, subject); len(got) != 0 {
		t.Errorf("the shadowbanned player still holds %d board rows after a rebuild: %v", len(got), got)
	}
	after := statRows(t, r, bystander)
	if len(after) != len(bystanderBefore) {
		t.Errorf("the bystander holds %d board rows, want %d", len(after), len(bystanderBefore))
	}
	for stat, want := range bystanderBefore {
		if after[stat] != want {
			t.Errorf("the bystander's %s moved: %v, want %v", stat, after[stat], want)
		}
	}
}

// TestUnshadowbanRestoresEveryRecord is the other half: the events go back at
// their original seq, so the rebuild reproduces exactly the boards that existed
// before — values, and the tie-break order that decides who holds a record.
func TestUnshadowbanRestoresEveryRecord(t *testing.T) {
	r := newRig(t)
	subject := r.player("griefer")
	r.ship(subject, cleanHistory(1)...)
	r.drain()
	r.rebuild()
	before := statRows(t, r, subject)

	if _, err := r.events.ShadowbanPlayer(t.Context(), subject, rigNow.UnixMilli(), "under review"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	r.rebuild()
	if got := statRows(t, r, subject); len(got) != 0 {
		t.Fatalf("still on %d boards while withheld", len(got))
	}

	if _, err := r.events.UnshadowbanPlayer(t.Context(), subject); err != nil {
		t.Fatalf("UnshadowbanPlayer: %v", err)
	}
	r.rebuild()

	after := statRows(t, r, subject)
	if len(after) != len(before) {
		t.Fatalf("holds %d board rows after the restore, want the original %d", len(after), len(before))
	}
	for stat, want := range before {
		if after[stat] != want {
			t.Errorf("%s = %v after the round trip, want %v", stat, after[stat], want)
		}
	}
}

// TestWithheldEventsNeverFoldIncrementally: an event that arrives while the ban
// is on must never reach a board, not even before a rebuild. The store routes it
// to the other table, so the projector cannot see it at all — which is the
// property that makes a shadow ban safe to leave running for weeks.
func TestWithheldEventsNeverFoldIncrementally(t *testing.T) {
	r := newRig(t)
	subject := r.player("griefer")

	if _, err := r.events.ShadowbanPlayer(t.Context(), subject, rigNow.UnixMilli(), "abuse"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	r.ship(subject, cleanHistory(1)...)
	r.drain()

	if got := statRows(t, r, subject); len(got) != 0 {
		t.Errorf("a shadowbanned player scored %d boards incrementally: %v", len(got), got)
	}
	if n, err := r.events.CountWithheldEvents(t.Context(), subject); err != nil || n == 0 {
		t.Errorf("withheld count = %d (err %v), want the shipped events", n, err)
	}
}

// --- the build stamp ----------------------------------------------------------

// TestRebuildStampsTheBuild: a rebuilt file carries this binary's fold-set
// identity, which is what every later start compares against.
func TestRebuildStampsTheBuild(t *testing.T) {
	r := newRig(t)
	r.ship(r.player("ace"), cleanHistory(1)...)
	r.drain()

	res := r.rebuild()
	if res.BuildID == "" {
		t.Fatal("the rebuild stamped no build id")
	}

	info, err := r.proj.Build(t.Context())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if info.Stale {
		t.Errorf("a freshly rebuilt database reads as stale: %+v", info)
	}
	if info.Stamped.BuildID != res.BuildID {
		t.Errorf("stamped %q, rebuild reported %q", info.Stamped.BuildID, res.BuildID)
	}
	if !info.Stamped.Complete {
		t.Error("the stamp is not marked complete")
	}
	if info.Stamped.FoldVersion != stats.BuildVersion {
		t.Errorf("fold version = %d, want %d", info.Stamped.FoldVersion, stats.BuildVersion)
	}
}

// TestAStaleBuildSuspendsTheFoldLoopAndRebuilds is the deploy story.
//
// A file stamped by some other fold set must not be folded into: doing so mixes
// two definitions of the boards in one database, and the result — a board
// holding only the events since the deploy — is indistinguishable from a board
// nobody has scored on. So the loop parks, the old file keeps serving, and the
// rebuild that lands is what starts it again.
func TestAStaleBuildSuspendsTheFoldLoopAndRebuilds(t *testing.T) {
	r := newRig(t, func(o *projector.Options) { o.AutoRebuild = true })
	subject := r.player("ace")
	r.ship(subject, cleanHistory(1)...)
	r.drain()

	// Stamp the live file as something else built it — exactly what a deploy
	// that adds a board looks like from the file's point of view.
	err := r.live.With(func(p *store.Projections) error {
		return p.SetBuild(t.Context(), nil, store.ProjectionBuild{
			BuildID:       "0000000000000000deadbeefdeadbeef",
			FoldVersion:   stats.BuildVersion - 1,
			SchemaVersion: p.Version,
			BuiltAt:       rigNow.UnixMilli(),
			Complete:      true,
		})
	})
	if err != nil {
		t.Fatalf("stamp a foreign build: %v", err)
	}
	if info, err := r.proj.Build(t.Context()); err != nil || !info.Stale {
		t.Fatalf("the foreign stamp did not read as stale (err %v)", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.proj.Run(ctx)

	// Wait for the automatic rebuild to reach a terminal phase. Waiting on
	// `Suspended()` alone would race the startup check, which has not run yet
	// at the moment Run is spawned — the loop would see "not suspended" and
	// conclude, wrongly, that everything was already fine.
	deadline := time.Now().Add(30 * time.Second)
	var st projector.RebuildStatus
	for {
		st = r.proj.RebuildStatus()
		if st.Phase == projector.PhaseDone || st.Phase == projector.PhaseFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the automatic rebuild never finished: suspended=%v status=%+v",
				r.proj.Suspended(), st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Phase != projector.PhaseDone {
		t.Fatalf("rebuild phase = %q, want done (err %q)", st.Phase, st.Err)
	}
	if r.proj.Suspended() {
		t.Error("the fold loop is still suspended after a successful rebuild")
	}
	if info, err := r.proj.Build(t.Context()); err != nil || info.Stale {
		t.Errorf("the build is still stale after the automatic rebuild: %+v (err %v)", info, err)
	}
	if got := statRows(t, r, subject); len(got) == 0 {
		t.Error("the rebuilt database holds no board rows")
	}
}

// TestSuspendedProjectorFoldsNothing pins the half of the above that matters
// most: while suspended, a new event must not move the checkpoint. A board that
// is stale is honest; a board holding only what arrived after a deploy is not.
func TestSuspendedProjectorFoldsNothing(t *testing.T) {
	r := newRig(t, func(o *projector.Options) { o.AutoRebuild = false })
	subject := r.player("ace")
	r.ship(subject, cleanHistory(1)...)
	r.drain()
	checkpoint := r.proj.CheckpointSeq()

	err := r.live.With(func(p *store.Projections) error {
		return p.SetBuild(t.Context(), nil, store.ProjectionBuild{
			BuildID: "not-this-binary", FoldVersion: 0, SchemaVersion: p.Version,
			BuiltAt: rigNow.UnixMilli(), Complete: true,
		})
	})
	if err != nil {
		t.Fatalf("stamp a foreign build: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.proj.Run(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for !r.proj.Suspended() {
		if time.Now().After(deadline) {
			t.Fatal("the fold loop was never suspended by the foreign build stamp")
		}
		time.Sleep(10 * time.Millisecond)
	}

	r.ship(subject, cleanHistory(200)...)
	time.Sleep(200 * time.Millisecond)

	if got := r.proj.CheckpointSeq(); got != checkpoint {
		t.Errorf("the checkpoint moved to %d while suspended, want %d", got, checkpoint)
	}
	if st := r.proj.RebuildStatus(); st.Phase.Running() {
		t.Errorf("a rebuild started with auto_rebuild off: %+v", st)
	}
}
