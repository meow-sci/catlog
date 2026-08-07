package projector_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// Every test here uses file-backed stores. The rebuild swap is a close, an
// os.Rename and a reopen, so an in-memory database would exercise none of it
// (§12 WP4).

// rig is a projector wired to real files, with the three tables a test reads.
type rig struct {
	t      *testing.T
	dir    string
	events *store.Events
	live   *projector.Live
	proj   *projector.Projector
	dirs   *directory.Directory
	bcast  *projector.Broadcaster
	log    *slog.Logger

	players map[string]int64
}

func newRig(t *testing.T, opts ...func(*projector.Options)) *rig {
	t.Helper()
	dir := t.TempDir()
	events := testutil.EventsAt(t, filepath.Join(dir, "events.db"))
	projections := testutil.ProjectionsAt(t, filepath.Join(dir, "projections.db"))

	d := directory.New(events)
	if err := d.Reload(t.Context()); err != nil {
		t.Fatalf("load directory: %v", err)
	}
	bcast := projector.NewBroadcaster()

	o := projector.Options{
		Events:       events,
		Live:         projector.NewLive(projections),
		Directory:    d,
		Broadcaster:  bcast,
		StoreOptions: testutil.Options(),
		Log:          testutil.DiscardLogger(),
	}
	for _, fn := range opts {
		fn(&o)
	}
	p, err := projector.New(o)
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	return &rig{
		t: t, dir: dir, events: events, live: o.Live, proj: p, dirs: d,
		bcast: bcast, log: o.Log, players: map[string]int64{},
	}
}

// player creates a player with a handle so the feed can name them.
func (r *rig) player(handle string) int64 {
	r.t.Helper()
	if id, ok := r.players[handle]; ok {
		return id
	}
	set := testutil.Keys(r.t)
	id, err := r.events.EnsurePlayer(r.t.Context(), nil, set.UserKey("dev", handle), "dev", 1)
	if err != nil {
		r.t.Fatalf("create player %s: %v", handle, err)
	}
	if err := r.events.ClaimHandle(r.t.Context(), id, handle, 1); err != nil {
		r.t.Fatalf("claim %s: %v", handle, err)
	}
	if err := r.dirs.Reload(r.t.Context()); err != nil {
		r.t.Fatalf("reload directory: %v", err)
	}
	r.players[handle] = id
	return id
}

// ship writes events for a player, exactly as the ingest writer would.
func (r *rig) ship(playerID int64, evs ...store.Event) {
	r.t.Helper()
	if _, _, err := r.events.InsertEvents(r.t.Context(), nil, playerID, evs); err != nil {
		r.t.Fatalf("insert events: %v", err)
	}
}

func (r *rig) drain() projector.Progress {
	r.t.Helper()
	prog, err := r.proj.Drain(r.t.Context())
	if err != nil {
		r.t.Fatalf("drain: %v", err)
	}
	return prog
}

func (r *rig) rebuild() projector.RebuildResult {
	r.t.Helper()
	res, err := r.proj.Rebuild(r.t.Context())
	if err != nil {
		r.t.Fatalf("rebuild: %v", err)
	}
	return res
}

// --- event construction ------------------------------------------------------

var evCounter int

func ev(flight ids.ID, typ string, payload any, simT float64) store.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	evCounter++
	var id ids.ID
	id[0] = 0x7f
	id[14] = byte(evCounter >> 8)
	id[15] = byte(evCounter)
	var session ids.ID
	session[0] = 0x01
	return store.Event{
		ID: id, FlightID: flight, SessionID: session,
		Type: typ, Ver: 1,
		SimTime:  sql.NullFloat64{Float64: simT, Valid: true},
		WallTime: 1_770_000_000_000 + int64(evCounter),
		Payload:  raw,
	}
}

func flight(n int) ids.ID {
	var id ids.ID
	id[0] = 0x02
	id[14] = byte(n >> 8)
	id[15] = byte(n)
	return id
}

// cleanHistory is a history with no flags, no scuttled kittens and every flight
// recovered — the conditions under which §5.6's incremental rules and its
// rebuild refinements are supposed to agree exactly.
func cleanHistory(f0 int) []store.Event {
	fa, fb := flight(f0), flight(f0+1)
	return []store.Event{
		ev(ids.Zero, "session.started", map[string]any{"mod_ver": "0.1.0", "game_build": "2026.8.5.5168", "install": "x"}, 0),
		ev(fa, "flight.started", stats.FlightStarted{VehicleName: "A", Body: "kerbin", CrewCount: 2}, 10),
		ev(fa, "vehicle.staging", stats.VehicleStaging{StageIndex: 0}, 12),
		ev(fa, "vehicle.staging", stats.VehicleStaging{StageIndex: 1}, 14),
		ev(fa, "telemetry.window", tw("kerbin", 2400, 7800, 4.5), 30),
		ev(fa, "vehicle.orbit", stats.VehicleOrbit{Phase: "achieved", Body: "kerbin", ApM: 300000, PeM: 280000}, 40),
		ev(fa, "vehicle.soi", stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"}, 50),
		ev(fa, "vehicle.docked", stats.VehicleDock{OtherFlight: ids.String(flight(99))}, 55),
		ev(fa, "vehicle.impact", stats.VehicleImpact{SpeedMs: 180, EnergyJ: 3.1e7, Survived: true, Body: "mun", CrewCount: 2}, 60),
		ev(fa, "flight.ended", stats.FlightEnded{Reason: "recovered", CrewCount: 2}, 70),
		ev(fb, "flight.started", stats.FlightStarted{VehicleName: "B", Body: "duna", CrewCount: 1}, 80),
		ev(fb, "telemetry.window", tw("duna", 780, 3100, 9.6), 90),
		ev(fb, "vehicle.rud", stats.VehicleRUD{Cause: "ground_impact", SpeedMs: 320, Body: "duna"}, 95),
		ev(fb, "kitten.tumble", stats.KittenTumble{Kid: "k1", Name: "Comet", SpeedMs: 8.2, Body: "duna"}, 96),
		ev(fb, "flight.ended", stats.FlightEnded{Reason: "recovered", CrewCount: 1}, 99),
		ev(ids.Zero, "roster.snapshot", stats.RosterSnapshot{Kittens: []stats.RosterKitten{
			{Kid: "k1", Name: "Comet", TravelledM: 1_200_000, FastestMs: 29800, Missions: 3, MissionTimeS: 900},
		}}, 100),
	}
}

func tw(body string, surface, orbital, peakG float64) stats.TelemetryWindow {
	g := peakG
	return stats.TelemetryWindow{
		T0Sim: 0, T1Sim: 30, N: 60, Body: body,
		SurfaceSpeedMs: stats.Agg{Max: surface},
		OrbitalSpeedMs: stats.Agg{Max: orbital},
		PeakG:          &g,
	}
}

// --- snapshots ---------------------------------------------------------------

// snapshot is every projection table rendered as comparable text. Feed rows are
// compared by content rather than by rowid so an id offset cannot make two
// identical feeds look different.
type snapshot struct {
	Stats   []string
	Flights []string
	Bodies  []string
	Kittens []string
	Feed    []string
	Cursor  int64
}

func (r *rig) snapshot() snapshot {
	r.t.Helper()
	var s snapshot
	err := r.live.With(func(p *store.Projections) error {
		ctx := r.t.Context()
		var err error
		if s.Stats, err = dump(ctx, p, `SELECT player_id, stat, value, coalesce(context,''), updated_seq FROM player_stat ORDER BY player_id, stat`); err != nil {
			return err
		}
		if s.Flights, err = dump(ctx, p, `SELECT hex(flight_id), player_id, flags, coalesce(ended_reason,''), coalesce(crew,-1), coalesce(body,''), started_seq FROM flight_state ORDER BY hex(flight_id)`); err != nil {
			return err
		}
		if s.Bodies, err = dump(ctx, p, `SELECT player_id, kind, body, first_seq FROM player_body ORDER BY player_id, kind, body`); err != nil {
			return err
		}
		if s.Kittens, err = dump(ctx, p, `SELECT player_id, kid, name, travelled_m, fastest_ms, missions, mission_time_s, kia, updated_seq FROM kitten ORDER BY player_id, kid`); err != nil {
			return err
		}
		if s.Feed, err = dump(ctx, p, `SELECT at, handle, type, summary FROM feed ORDER BY id`); err != nil {
			return err
		}
		s.Cursor, err = p.Checkpoint(ctx, nil, store.AllProjections)
		return err
	})
	if err != nil {
		r.t.Fatalf("snapshot: %v", err)
	}
	return s
}

func dump(ctx context.Context, p *store.Projections, query string) ([]string, error) {
	rows, err := p.Reader().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, err
		}
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = fmt.Sprintf("%s=%v", cols[i], *(c.(*any)))
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out, rows.Err()
}

// withoutAt drops the server-assigned timestamp from feed lines, for comparisons
// between two runs that inserted the same events at different wall times.
func withoutAt(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if _, rest, ok := strings.Cut(l, " "); ok {
			out = append(out, rest)
			continue
		}
		out = append(out, l)
	}
	return out
}

func diff(t *testing.T, label string, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	t.Errorf("%s differs:\n incremental: %s\n rebuild:     %s",
		label, strings.Join(want, "\n              "), strings.Join(got, "\n              "))
}

// --- the tests ---------------------------------------------------------------

func TestFoldsABatchAndAdvancesTheCheckpoint(t *testing.T) {
	r := newRig(t)
	p := r.player("whiskers")
	r.ship(p, cleanHistory(1)...)

	prog := r.drain()
	if prog.Read != 16 {
		t.Fatalf("read %d events, want 16", prog.Read)
	}
	snap := r.snapshot()
	if snap.Cursor != 16 {
		t.Errorf("checkpoint = %d, want 16", snap.Cursor)
	}
	if len(snap.Stats) == 0 {
		t.Fatal("no stats were written")
	}
	if len(snap.Feed) == 0 {
		t.Fatal("no feed rows were written")
	}
	if r.proj.Lag() != 0 {
		t.Errorf("lag = %d after draining, want 0", r.proj.Lag())
	}
}

func TestCheckpointResumesMidStreamWithoutDoubleCounting(t *testing.T) {
	// A counter is the sharp edge here: replaying an already-folded event would
	// silently inflate every count. The checkpoint is committed in the same
	// transaction as the writes it accounts for, which is what makes a resume
	// exact rather than approximate.
	r := newRig(t, func(o *projector.Options) { o.BatchSize = 3 })
	p := r.player("whiskers")
	r.ship(p, cleanHistory(1)...)

	prog, err := r.proj.Step(r.t.Context())
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if prog.LastSeq != 3 || !prog.More {
		t.Fatalf("first step stopped at seq %d (more=%v), want 3 with more pending", prog.LastSeq, prog.More)
	}

	// A second projector over the same files, as a restart would be.
	resumed, err := projector.New(projector.Options{
		Events: r.events, Live: r.live, Directory: r.dirs,
		BatchSize: 3, Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Drain(t.Context()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got := r.snapshot()

	// Fold the same history from scratch in a second rig and compare.
	fresh := newRig(t)
	fp := fresh.player("whiskers")
	evCounter = 0 // the same event ids, so the two logs are identical
	fresh.ship(fp, cleanHistory(1)...)
	fresh.drain()
	want := fresh.snapshot()

	diff(t, "player_stat", got.Stats, want.Stats)
	diff(t, "flight_state", got.Flights, want.Flights)
	diff(t, "kitten", got.Kittens, want.Kittens)
	// The two rigs inserted their events at slightly different wall times, so
	// the feed's server-assigned `at` legitimately differs; the content must not.
	diff(t, "feed", withoutAt(got.Feed), withoutAt(want.Feed))
	if got.Cursor != want.Cursor {
		t.Errorf("checkpoint = %d, want %d", got.Cursor, want.Cursor)
	}
}

func TestRebuildEqualsIncrementalForAnUnflaggedHistory(t *testing.T) {
	// D22's whole premise: the fast path and the backstop must agree whenever
	// nothing forced them apart. "Unflagged" here also means no scuttled
	// kittens and no lost flights, because those are the two §5.6 refinements a
	// rebuild applies and the incremental path cannot.
	r := newRig(t)
	for i, handle := range []string{"whiskers", "mittens", "clawdia"} {
		p := r.player(handle)
		r.ship(p, cleanHistory(1+i*10)...)
	}
	r.drain()
	incremental := r.snapshot()

	res := r.rebuild()
	if res.LastSeq != incremental.Cursor {
		t.Errorf("rebuild checkpoint = %d, incremental = %d", res.LastSeq, incremental.Cursor)
	}
	rebuilt := r.snapshot()

	diff(t, "player_stat", rebuilt.Stats, incremental.Stats)
	diff(t, "flight_state", rebuilt.Flights, incremental.Flights)
	diff(t, "player_body", rebuilt.Bodies, incremental.Bodies)
	diff(t, "kitten", rebuilt.Kittens, incremental.Kittens)
	diff(t, "feed", rebuilt.Feed, incremental.Feed)
	if rebuilt.Cursor != incremental.Cursor {
		t.Errorf("checkpoint after rebuild = %d, want %d", rebuilt.Cursor, incremental.Cursor)
	}
	if len(incremental.Stats) == 0 {
		t.Fatal("the fixture produced no stats, so the comparison proved nothing")
	}
}

func TestRebuildHealsALateFlag(t *testing.T) {
	// The case the incremental path gets wrong by construction: the mod detects
	// a teleport only after the flight has already scored, so the flag arrives
	// in a later batch (§5.6, D22).
	r := newRig(t)
	p := r.player("whiskers")
	cheat := flight(500)

	r.ship(p,
		ev(cheat, "flight.started", stats.FlightStarted{VehicleName: "Cheater", Body: "kerbin", CrewCount: 1}, 10),
		ev(cheat, "telemetry.window", tw("kerbin", 9000, 40000, 88), 20),
		ev(cheat, "vehicle.impact", stats.VehicleImpact{SpeedMs: 9000, EnergyJ: 1e12, Survived: true, Body: "kerbin", CrewCount: 1}, 30),
		ev(cheat, "kitten.tumble", stats.KittenTumble{Kid: "k1", Name: "Comet", SpeedMs: 44, Body: "kerbin"}, 31),
		ev(cheat, "flight.ended", stats.FlightEnded{Reason: "recovered", CrewCount: 1}, 40),
	)
	r.drain()

	before := statMap(t, r.snapshot())
	if before["1/biggest_lithobrake_survived"] != 9000 {
		t.Fatalf("incremental lithobrake = %v, want 9000 — the fixture must actually score first",
			before["1/biggest_lithobrake_survived"])
	}
	if before["1/kitten_tumbles"] != 1 {
		t.Fatalf("incremental kitten_tumbles = %v, want 1", before["1/kitten_tumbles"])
	}
	feedBefore := len(r.snapshot().Feed)
	if feedBefore == 0 {
		t.Fatal("the cheated flight produced no feed rows, so healing them proves nothing")
	}

	// The flag lands late.
	r.ship(p, ev(cheat, "flight.flagged", stats.FlightFlagged{Flag: "teleport", Detail: "moved 4.2e6 m in one frame"}, 50))
	r.drain()

	stillWrong := statMap(t, r.snapshot())
	if stillWrong["1/biggest_lithobrake_survived"] != 9000 {
		t.Fatal("the incremental path retracted a record on its own; this test no longer covers what it claims")
	}

	r.rebuild()
	after := r.snapshot()
	healed := statMap(t, after)
	for _, stat := range []string{
		"1/biggest_lithobrake_survived", "1/kitten_tumbles", "1/peak_g_survived",
		"1/fastest_surface_speed", "1/fastest_orbital_speed", "1/kittens_recovered",
	} {
		if v, ok := healed[stat]; ok {
			t.Errorf("%s survived the rebuild with value %v; the whole flight was flagged", stat, v)
		}
	}
	if len(after.Feed) != 0 {
		t.Errorf("the rebuilt feed still shows the cheated flight: %v", after.Feed)
	}
	if len(after.Flights) != 1 || !strings.Contains(after.Flights[0], "flags=1") {
		t.Errorf("flight_state after rebuild = %v, want the teleport bit set", after.Flights)
	}
}

func TestRebuildAppliesTheKIAWindow(t *testing.T) {
	r := newRig(t)
	p := r.player("whiskers")
	f := flight(600)
	r.ship(p,
		ev(f, "flight.started", stats.FlightStarted{VehicleName: "Scuttled", Body: "duna", CrewCount: 1}, 100),
		ev(f, "vehicle.impact", stats.VehicleImpact{SpeedMs: 640, Survived: true, Body: "duna", CrewCount: 1}, 200),
		ev(f, "kitten.kia", stats.KittenKIA{Kid: "k1", Name: "Comet", Context: "manual_destroy"}, 201.2),
		ev(f, "flight.ended", stats.FlightEnded{Reason: "recovered", CrewCount: 0}, 210),
	)
	r.drain()
	if statMap(t, r.snapshot())["1/biggest_lithobrake_survived"] != 640 {
		t.Fatal("the incremental path did not score the impact, so the refinement proves nothing")
	}

	res := r.rebuild()
	if res.KIAFlights != 1 {
		t.Errorf("rebuild indexed %d kia flights, want 1", res.KIAFlights)
	}
	if v, ok := statMap(t, r.snapshot())["1/biggest_lithobrake_survived"]; ok {
		t.Errorf("lithobrake = %v after rebuild; a kitten died 1.2 s later (§4.2 ±2 s)", v)
	}
}

func TestRebuildSwapsTheFileAndLeavesNoScratch(t *testing.T) {
	r := newRig(t)
	p := r.player("whiskers")
	r.ship(p, cleanHistory(1)...)
	r.drain()

	livePath := r.live.Path()
	before, err := os.Stat(livePath)
	if err != nil {
		t.Fatal(err)
	}

	res := r.rebuild()
	if res.Path != livePath {
		t.Errorf("rebuild reported path %q, want the live path %q", res.Path, livePath)
	}
	after, err := os.Stat(livePath)
	if err != nil {
		t.Fatalf("the live database is gone after the swap: %v", err)
	}
	if os.SameFile(before, after) {
		t.Error("the live path still points at the same inode; nothing was swapped")
	}
	for _, leftover := range []string{
		livePath + ".old",
		strings.TrimSuffix(livePath, ".db") + ".rebuild.db",
		strings.TrimSuffix(livePath, ".db") + ".rebuild.db-wal",
		strings.TrimSuffix(livePath, ".db") + ".rebuild.db-shm",
	} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s was left behind", filepath.Base(leftover))
		}
	}

	// The swapped-in handle must be usable, not just present.
	if len(r.snapshot().Stats) == 0 {
		t.Error("the reopened database has no stats")
	}
}

func TestReadsKeepWorkingAcrossASwap(t *testing.T) {
	r := newRig(t)
	p := r.player("whiskers")
	r.ship(p, cleanHistory(1)...)
	r.drain()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			err := r.live.With(func(pr *store.Projections) error {
				_, err := pr.StatCounts(context.Background())
				return err
			})
			if err != nil {
				t.Errorf("read during rebuild: %v", err)
				return
			}
		}
	}()

	r.rebuild()
	close(stop)
	wg.Wait()
}

func TestFeedIsCappedAndBroadcast(t *testing.T) {
	r := newRig(t)
	p := r.player("whiskers")

	sub, cancel := r.bcast.Subscribe()
	defer cancel()
	if r.bcast.Clients() != 1 {
		t.Fatalf("subscribers = %d, want 1", r.bcast.Clients())
	}

	// Comfortably more feed-worthy events than the cap.
	var evs []store.Event
	f := flight(700)
	evs = append(evs, ev(f, "flight.started", stats.FlightStarted{VehicleName: "Loop", Body: "mun", CrewCount: 1}, 0))
	for i := range store.FeedCap + 25 {
		evs = append(evs, ev(f, "kitten.tumble", stats.KittenTumble{
			Kid: "k1", Name: "Comet", SpeedMs: float64(i%20) + 6, Body: "mun",
		}, float64(i)))
	}
	r.ship(p, evs...)
	r.drain()

	var n int64
	if err := r.live.With(func(pr *store.Projections) error {
		return pr.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM feed`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != store.FeedCap {
		t.Errorf("feed has %d rows, want the §5.4 cap of %d", n, store.FeedCap)
	}

	select {
	case batch := <-sub:
		if len(batch) == 0 {
			t.Error("an empty batch was broadcast")
		}
	case <-time.After(2 * time.Second):
		t.Error("nothing was broadcast to the subscriber")
	}

	// A fresh SSE subscriber is primed from the table, newest first (§5.7).
	var recent []store.FeedRow
	if err := r.live.With(func(pr *store.Projections) error {
		var err error
		recent, err = pr.RecentFeed(t.Context(), 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(recent) != 10 || recent[0].ID <= recent[9].ID {
		t.Errorf("RecentFeed returned %d rows, newest-first = %v", len(recent), len(recent) > 1 && recent[0].ID > recent[1].ID)
	}

	cancel()
	if r.bcast.Clients() != 0 {
		t.Errorf("subscribers = %d after cancel, want 0", r.bcast.Clients())
	}
}

func TestUnknownVersionIsSkippedAndLoggedOnce(t *testing.T) {
	// §4.1: an unknown-but-higher ver is accepted and stored; the projector
	// skips it and logs once. Skipping must still advance the checkpoint, or one
	// event from a newer mod would wedge every projection behind it.
	var buf syncBuffer
	r := newRig(t, func(o *projector.Options) {
		o.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	})
	p := r.player("whiskers")

	f := flight(800)
	future := ev(f, "vehicle.impact", map[string]any{"speed_ms": 214, "survived": true, "crew_count": 1}, 10)
	future.Ver = 7
	second := future
	second.ID[15] = 0xAB
	third := future
	third.ID[15] = 0xAC

	good := ev(f, "vehicle.impact", stats.VehicleImpact{SpeedMs: 100, Survived: true, Body: "duna", CrewCount: 1}, 20)
	r.ship(p, future, second, third, good)

	prog := r.drain()
	if prog.Skipped != 3 {
		t.Errorf("skipped %d events, want 3", prog.Skipped)
	}
	if snap := r.snapshot(); snap.Cursor != 4 {
		t.Errorf("checkpoint = %d, want 4: a skip must still advance the cursor", snap.Cursor)
	}
	if got := statMap(t, r.snapshot())["1/biggest_lithobrake_survived"]; got != 100 {
		t.Errorf("lithobrake = %v, want 100 from the one event this build understands", got)
	}
	if n := strings.Count(buf.String(), "skipping events this build cannot fold"); n != 1 {
		t.Errorf("logged the skip %d times, want exactly 1:\n%s", n, buf.String())
	}
}

func TestRunWakesOnNotify(t *testing.T) {
	notify := make(chan struct{}, 1)
	r := newRig(t, func(o *projector.Options) {
		o.Notify = notify
		o.Tick = time.Hour // only the notify channel can wake it
	})
	p := r.player("whiskers")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.proj.Run(ctx)
	defer func() { cancel(); r.proj.Wait() }()

	r.ship(p, cleanHistory(1)...)
	notify <- struct{}{}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.snapshot().Cursor == 16 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the projector never folded after a notify; checkpoint = %d", r.snapshot().Cursor)
}

func TestRebuildRefusesAnInMemoryDatabase(t *testing.T) {
	events := testutil.MemEvents(t)
	d := directory.New(events)
	if err := d.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := projector.New(projector.Options{
		Events: events, Live: projector.NewLive(testutil.MemProjections(t)),
		Directory: d, Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rebuild(t.Context()); err == nil {
		t.Error("rebuilding an in-memory database succeeded; there is no file to rename")
	}
}

// --- helpers -----------------------------------------------------------------

func statMap(t *testing.T, s snapshot) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, line := range s.Stats {
		var player int64
		var stat string
		var value float64
		// player_id=1 stat=rud_total value=1 context= updated_seq=13
		if _, err := fmt.Sscanf(line, "player_id=%d stat=%s value=%g", &player, &stat, &value); err != nil {
			t.Fatalf("unparseable stat line %q: %v", line, err)
		}
		out[fmt.Sprintf("%d/%s", player, stat)] = value
	}
	return out
}

// syncBuffer is a strings.Builder a slog handler may write to from any
// goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
