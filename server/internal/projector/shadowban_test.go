package projector_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

const moderationSystem = "moderation-system-hash"

func moderationHistory(career string, f int) []store.Event {
	flightID := flight(f)
	return []store.Event{
		discovery(career, moderationSystem),
		rootBody(moderationSystem),
		inCareer(ev(ids.Zero, "session.started", stats.SessionStarted{}, 0), career),
		inCareer(ev(flightID, "flight.started", stats.FlightStarted{
			VehicleName: "Moderated", Body: "earth", MassKg: 100, PartCount: 3, CrewCount: 1,
		}, 10), career),
		inCareer(ev(flightID, "vehicle.staging", stats.VehicleStaging{StageIndex: 0}, 12), career),
		inCareer(ev(flightID, "vehicle.soi", stats.VehicleSOI{FromBody: "earth", ToBody: "luna"}, 20), career),
		inCareer(ev(flightID, "vehicle.orbit", stats.VehicleOrbit{Phase: "achieved", Body: "earth"}, 22), career),
		inCareer(ev(flightID, "vehicle.impact", stats.VehicleImpact{
			SpeedMs: 25, EnergyJ: 1000, Survived: true, Body: "luna", CrewCount: 1,
		}, 25), career),
		inCareer(ev(flightID, "vehicle.rud", stats.VehicleRUD{
			Cause: "collision", Body: "luna", PartCount: 3, CrewCount: 2,
		}, 27), career),
		inCareer(ev(flightID, "flight.ended", stats.FlightEnded{Reason: "recovered", CrewCount: 1, Kids: []string{"kid-" + career}}, 30), career),
		inCareer(ev(ids.Zero, "roster.snapshot", stats.RosterSnapshot{Kittens: []stats.RosterKitten{{
			Kid: "kid-" + career, Name: "Comet", TravelledM: 50, FastestMs: 5,
			Missions: 1, MissionTimeS: 30,
		}}}, 31), career),
	}
}

// playerProjectionRows is every current player-owned projection family. The
// system catalogue and event census are deliberately absent: they are shared
// facts, not private rows. Future tables are added by their owning moderation
// task rather than guessed here.
func playerProjectionRows(t *testing.T, r *rig, playerID int64, handle string) map[string][]string {
	t.Helper()
	queries := map[string]string{
		"player_stat":        `SELECT player_id, stat, value, context, updated_seq FROM player_stat WHERE player_id = %d ORDER BY stat`,
		"career_stat":        `SELECT player_id, career, system, stat, value, context, updated_seq FROM career_stat WHERE player_id = %d ORDER BY career, stat`,
		"system_stat":        `SELECT player_id, system, stat, value, context, updated_seq FROM system_stat WHERE player_id = %d ORDER BY system, stat`,
		"flight_state":       `SELECT hex(flight_id), player_id, flags, ended_reason, crew, body, started_seq, engine_count, milestones, part_count, launch_mass_kg, career, first_orbit_seq FROM flight_state WHERE player_id = %d ORDER BY hex(flight_id)`,
		"player_body":        `SELECT player_id, kind, body, first_seq, first_sim_t FROM player_body WHERE player_id = %d ORDER BY kind, body`,
		"career_body":        `SELECT player_id, career, system, kind, body, first_seq, first_sim_t FROM career_body WHERE player_id = %d ORDER BY career, kind, body`,
		"kitten":             `SELECT player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq FROM kitten WHERE player_id = %d ORDER BY kid`,
		"career_kitten":      `SELECT player_id, career, system, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq FROM career_kitten WHERE player_id = %d ORDER BY career, kid`,
		"career":             `SELECT player_id, career, ordinal, system, system_changed, max_sim_t, rewound, first_seq, last_seq FROM career WHERE player_id = %d ORDER BY career`,
		"badge_award":        `SELECT player_id, career, badge, system, first_career, earned_seq, earned_at, earned_sim_t, context FROM badge_award WHERE player_id = %d ORDER BY career, badge`,
		"challenge_stat":     `SELECT player_id, career, challenge, system, value, context, updated_seq FROM challenge_stat WHERE player_id = %d ORDER BY career, system, challenge`,
		"challenge_member":   `SELECT player_id, career, system, challenge, member, first_seq FROM challenge_member WHERE player_id = %d ORDER BY career, system, challenge, member`,
		"player_stat_period": `SELECT player_id, stat, period, bucket, value, context, updated_seq FROM player_stat_period WHERE player_id = %d ORDER BY stat, period, bucket`,
	}
	out := make(map[string][]string, len(queries)+1)
	err := r.live.With(func(p *store.Projections) error {
		for name, query := range queries {
			rows, err := dump(t.Context(), p, fmt.Sprintf(query, playerID))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			out[name] = rows
		}
		rows, err := dump(t.Context(), p, `SELECT at, handle, type, summary FROM feed WHERE handle = '`+handle+`' ORDER BY id`)
		out["feed"] = rows
		return err
	})
	if err != nil {
		t.Fatalf("read player-owned projections: %v", err)
	}
	return out
}

func requirePopulatedPlayerProjections(t *testing.T, rows map[string][]string) {
	t.Helper()
	for table, values := range rows {
		// Storage foundations may precede their writers. The ownership tests seed
		// these tables non-vacuously until their ordinary folds exist.
		if (table == "badge_award" || table == "challenge_stat" || table == "challenge_member") && len(values) == 0 {
			continue
		}
		if len(values) == 0 {
			t.Errorf("fixture produced no %s rows", table)
		}
	}
}

func seedBadgeAward(t *testing.T, r *rig, playerID int64, career, badge string) {
	t.Helper()
	err := r.live.With(func(p *store.Projections) error {
		_, err := p.Writer().ExecContext(t.Context(), `
			INSERT INTO badge_award
				(player_id, career, badge, system, first_career, earned_seq, earned_at)
			VALUES (?, ?, ?, 'system-hash', 'first-save', 1, 1770000000000)`,
			playerID, career, badge)
		return err
	})
	if err != nil {
		t.Fatalf("seed badge awards: %v", err)
	}
}

func seedChallengeRows(t *testing.T, r *rig, playerID int64, career string) {
	t.Helper()
	err := r.live.With(func(p *store.Projections) error {
		if _, err := p.Writer().ExecContext(t.Context(), `
			INSERT INTO challenge_stat (player_id, career, challenge, system, value, context, updated_seq)
			VALUES (?, ?, 'structural-challenge', 'system-hash', 1, '{"fact":"shared"}', 1)`, playerID, career); err != nil {
			return err
		}
		_, err := p.Writer().ExecContext(t.Context(), `
			INSERT INTO challenge_member (player_id, career, system, challenge, member, first_seq)
			VALUES (?, ?, 'system-hash', 'structural-challenge', 'system-hash'||char(0)||'member', 1)`, playerID, career)
		return err
	})
	if err != nil {
		t.Fatalf("seed challenge rows: %v", err)
	}
}

func requireNoPlayerProjections(t *testing.T, rows map[string][]string) {
	t.Helper()
	for table, values := range rows {
		if len(values) != 0 {
			t.Errorf("private %s rows survived: %v", table, values)
		}
	}
}

func sharedCatalogueRows(t *testing.T, r *rig) (systems, bodies []string) {
	t.Helper()
	err := r.live.With(func(p *store.Projections) error {
		var err error
		// first_seq is intentionally omitted: removing the first reporter makes
		// the earliest remaining report the catalogue's new provenance, while
		// the shared content itself must remain.
		if systems, err = dump(t.Context(), p, `SELECT hash, system_id, name, slug, home_body, body_count, reported_complete FROM system ORDER BY hash`); err != nil {
			return err
		}
		bodies, err = dump(t.Context(), p, `SELECT hash, body, name, class, kind, rank, parent FROM system_body ORDER BY hash, body`)
		return err
	})
	if err != nil {
		t.Fatalf("read shared catalogue: %v", err)
	}
	return systems, bodies
}

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

	r.ship(subject, moderationHistory("subject-career", 1)...)
	r.ship(bystander, moderationHistory("bystander-career", 100)...)
	r.drain()
	seedBadgeAward(t, r, subject, "subject-career", "subject-badge")
	seedChallengeRows(t, r, subject, "subject-career")

	before := playerProjectionRows(t, r, subject, "griefer")
	requirePopulatedPlayerProjections(t, before)
	requireSeededBadge(t, before["badge_award"])
	bystanderBefore := playerProjectionRows(t, r, bystander, "honest_cat")
	requirePopulatedPlayerProjections(t, bystanderBefore)
	systemsBefore, bodiesBefore := sharedCatalogueRows(t, r)

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
	if got := playerProjectionRows(t, r, subject, "griefer"); !reflect.DeepEqual(got, before) {
		t.Error("player-owned projections changed before the rebuild cutover")
	}

	r.rebuild()

	requireNoPlayerProjections(t, playerProjectionRows(t, r, subject, "griefer"))
	if after := playerProjectionRows(t, r, bystander, "honest_cat"); !reflect.DeepEqual(after, bystanderBefore) {
		t.Error("the bystander's projections changed during the shadow-ban rebuild")
	}
	systemsAfter, bodiesAfter := sharedCatalogueRows(t, r)
	if !reflect.DeepEqual(systemsAfter, systemsBefore) || !reflect.DeepEqual(bodiesAfter, bodiesBefore) {
		t.Errorf("shared system catalogue changed: before=%v/%v after=%v/%v",
			systemsBefore, bodiesBefore, systemsAfter, bodiesAfter)
	}
}

// TestUnshadowbanRestoresEveryRecord is the other half: the events go back at
// their original seq, so the rebuild reproduces exactly the boards that existed
// before — values, and the tie-break order that decides who holds a record.
func TestUnshadowbanRestoresEveryRecord(t *testing.T) {
	r := newRig(t)
	subject := r.player("griefer")
	r.ship(subject, moderationHistory("subject-career", 1)...)
	r.drain()
	r.rebuild()
	before := playerProjectionRows(t, r, subject, "griefer")
	requirePopulatedPlayerProjections(t, before)

	if _, err := r.events.ShadowbanPlayer(t.Context(), subject, rigNow.UnixMilli(), "under review"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	r.rebuild()
	requireNoPlayerProjections(t, playerProjectionRows(t, r, subject, "griefer"))

	if _, err := r.events.UnshadowbanPlayer(t.Context(), subject); err != nil {
		t.Fatalf("UnshadowbanPlayer: %v", err)
	}
	r.rebuild()

	after := playerProjectionRows(t, r, subject, "griefer")
	if !reflect.DeepEqual(after, before) {
		t.Errorf("player projections differ after restore:\nbefore=%v\nafter=%v", before, after)
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
	r.ship(subject, moderationHistory("subject-career", 1)...)
	r.drain()

	requireNoPlayerProjections(t, playerProjectionRows(t, r, subject, "griefer"))
	if n, err := r.events.CountWithheldEvents(t.Context(), subject); err != nil || n == 0 {
		t.Errorf("withheld count = %d (err %v), want the shipped events", n, err)
	}
}

func TestPurgeLeavesNoPlayerOwnedProjectionRowsAfterRebuild(t *testing.T) {
	r := newRig(t)
	subject, bystander := r.player("deleted_cat"), r.player("honest_cat")
	history := moderationHistory("deleted-career", 200)
	r.ship(subject, history...)
	r.ship(bystander, moderationHistory("bystander-career", 300)...)
	r.drain()
	seedBadgeAward(t, r, subject, "deleted-career", "subject-badge")
	seedChallengeRows(t, r, subject, "deleted-career")

	before := playerProjectionRows(t, r, subject, "deleted_cat")
	requirePopulatedPlayerProjections(t, before)
	requireSeededBadge(t, before["badge_award"])
	bystanderBefore := playerProjectionRows(t, r, bystander, "honest_cat")
	systemsBefore, bodiesBefore := sharedCatalogueRows(t, r)

	counts, err := r.events.PurgePlayer(t.Context(), subject)
	if err != nil {
		t.Fatalf("PurgePlayer: %v", err)
	}
	if counts.Events != int64(len(history)) || counts.Withheld != 0 || counts.Handles != 1 {
		t.Errorf("purge counts = %+v, want %d live events, one handle, and no withheld events", counts, len(history))
	}
	if n, err := r.events.CountEvents(t.Context(), subject); err != nil || n != 0 {
		t.Errorf("live private log rows after purge = %d (err %v)", n, err)
	}
	if n, err := r.events.CountWithheldEvents(t.Context(), subject); err != nil || n != 0 {
		t.Errorf("withheld private log rows after purge = %d (err %v)", n, err)
	}

	// Like shadow-ban, events.db changes first and the old projection file
	// remains untouched until the atomic rebuild swap.
	if got := playerProjectionRows(t, r, subject, "deleted_cat"); !reflect.DeepEqual(got, before) {
		t.Error("purge mutated projections before the rebuild cutover")
	}
	r.rebuild()
	requireNoPlayerProjections(t, playerProjectionRows(t, r, subject, "deleted_cat"))
	if after := playerProjectionRows(t, r, bystander, "honest_cat"); !reflect.DeepEqual(after, bystanderBefore) {
		t.Error("the bystander's projections changed during the purge rebuild")
	}
	systemsAfter, bodiesAfter := sharedCatalogueRows(t, r)
	if !reflect.DeepEqual(systemsAfter, systemsBefore) || !reflect.DeepEqual(bodiesAfter, bodiesBefore) {
		t.Errorf("purge removed shared catalogue rows: before=%v/%v after=%v/%v",
			systemsBefore, bodiesBefore, systemsAfter, bodiesAfter)
	}
}

func requireSeededBadge(t *testing.T, rows []string) {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, "badge=subject-badge ") {
			return
		}
	}
	t.Fatalf("subject badge fixture missing from %v", rows)
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
