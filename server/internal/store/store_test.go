package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// tableNames lists the user tables in an open database.
func tableNames(t *testing.T, db *store.DB) []string {
	t.Helper()
	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return out
}

func indexNames(t *testing.T, db *store.DB) []string {
	t.Helper()
	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT name FROM sqlite_schema WHERE type = 'index' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		out = append(out, n)
	}
	return out
}

func indexColumns(t *testing.T, db *store.DB, index string) []string {
	t.Helper()
	rows, err := db.Reader().QueryContext(t.Context(), `PRAGMA index_info(`+index+`)`)
	if err != nil {
		t.Fatalf("list %s columns: %v", index, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq, cid int
		var name string
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			t.Fatalf("scan %s column: %v", index, err)
		}
		out = append(out, name)
	}
	return out
}

func schemaVersions(t *testing.T, db *store.DB) []int {
	t.Helper()
	rows, err := db.Reader().QueryContext(t.Context(), `SELECT v FROM schema_version ORDER BY v`)
	if err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan schema_version: %v", err)
		}
		out = append(out, v)
	}
	return out
}

// TestMigrationsCreateTheFullDDL checks that both §5.4 schemas land exactly.
func TestMigrationsCreateTheFullDDL(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		e := testutil.Events(t)
		want := []string{
			"archive_cursor", "credential", "event", "event_seq", "handle", "ingest_batch",
			"payload_dict", "player", "retired_handle", "schema_version",
			"shadowban", "shadowban_event", "stream_state", "tombstone",
		}
		if got := tableNames(t, e.DB); !equal(got, want) {
			t.Errorf("tables = %v, want %v", got, want)
		}
		wantIdx := []string{"cred_player", "ev_dedup", "ev_player", "sb_dedup", "sb_player"}
		if got := indexNames(t, e.DB); !equal(got, wantIdx) {
			t.Errorf("indexes = %v, want %v", got, wantIdx)
		}
		if e.Version != 5 {
			t.Errorf("schema version = %d, want 5", e.Version)
		}
	})

	t.Run("projections", func(t *testing.T) {
		p := testutil.Projections(t)
		want := []string{
			"badge_award", "career", "career_body", "career_kitten", "career_stat", "event_census", "feed", "flight_state", "kitten", "player_body",
			"player_stat", "player_stat_period", "proj_build", "proj_checkpoint", "schema_version",
			"system", "system_body", "system_stat",
		}
		if got := tableNames(t, p.DB); !equal(got, want) {
			t.Errorf("tables = %v, want %v", got, want)
		}
		wantIdx := []string{
			"badge_by_career", "badge_holders", "badge_system",
			"career_body_system", "career_kitten_player", "career_kitten_system",
			"career_stat_rank", "career_stat_system",
			"census_busiest", "census_window",
			"fs_player", "stat_period_age", "stat_period_rank", "stat_rank", "system_stat_rank",
			"career_system", "system_body_kind", "system_slug",
		}
		slices.Sort(wantIdx)
		if got := indexNames(t, p.DB); !equal(got, wantIdx) {
			t.Errorf("indexes = %v, want %v", got, wantIdx)
		}
		if p.Version != 11 {
			t.Errorf("schema version = %d, want 11", p.Version)
		}
		rows, err := p.Reader().QueryContext(t.Context(), `PRAGMA table_info(flight_state)`)
		if err != nil {
			t.Fatalf("flight_state columns: %v", err)
		}
		defer rows.Close()
		var columns []string
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, fmt.Sprintf("%s %s null=%t default=%s pk=%d", name, typ, notNull == 0, defaultValue.String, pk))
		}
		wantColumns := []string{
			"flight_id BLOB null=true default= pk=1",
			"player_id INTEGER null=false default= pk=0",
			"flags INTEGER null=false default=0 pk=0",
			"ended_reason TEXT null=true default= pk=0",
			"crew INTEGER null=true default= pk=0",
			"body TEXT null=true default= pk=0",
			"started_seq INTEGER null=false default= pk=0",
			"engine_count INTEGER null=true default= pk=0",
			"milestones INTEGER null=false default=0 pk=0",
			"part_count INTEGER null=true default= pk=0",
			"launch_mass_kg REAL null=true default= pk=0",
			"career TEXT null=false default='' pk=0",
		}
		if !slices.Equal(columns, wantColumns) {
			t.Errorf("flight_state columns = %v, want %v", columns, wantColumns)
		}

		rows, err = p.Reader().QueryContext(t.Context(), `PRAGMA table_info(badge_award)`)
		if err != nil {
			t.Fatalf("badge_award columns: %v", err)
		}
		defer rows.Close()
		columns = nil
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, fmt.Sprintf("%s %s null=%t default=%s pk=%d", name, typ, notNull == 0, defaultValue.String, pk))
		}
		wantColumns = []string{
			"player_id INTEGER null=false default= pk=1",
			"career TEXT null=false default= pk=1",
			"badge TEXT null=false default= pk=1",
			"system TEXT null=false default='' pk=0",
			"first_career TEXT null=false default='' pk=0",
			"earned_seq INTEGER null=false default= pk=0",
			"earned_at INTEGER null=false default= pk=0",
			"earned_sim_t REAL null=true default= pk=0",
			"context TEXT null=true default= pk=0",
		}
		if !slices.Equal(columns, wantColumns) {
			t.Errorf("badge_award columns = %v, want %v", columns, wantColumns)
		}
		for index, want := range map[string][]string{
			"badge_system":    {"system", "badge", "earned_seq"},
			"badge_holders":   {"badge", "earned_seq"},
			"badge_by_career": {"player_id", "career", "earned_seq"},
		} {
			if got := indexColumns(t, p.DB, index); !slices.Equal(got, want) {
				t.Errorf("%s columns = %v, want %v", index, got, want)
			}
		}
	})
}

func TestBadgeAwardSurvivesProjectionRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projections.db")
	first := testutil.ProjectionsAt(t, path)
	_, err := first.Writer().ExecContext(t.Context(), `
		INSERT INTO badge_award
			(player_id, career, badge, system, first_career, earned_seq, earned_at, earned_sim_t, context)
		VALUES
			(7, '', 'lifetime', '', 'first-save', 11, 1770000000000, NULL, NULL),
			(7, 'second-save', 'career-award', 'system-hash', '', 12, 1770000001000, 42.5, '{"body":"luna"}')`)
	if err != nil {
		t.Fatalf("seed badge awards: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close projections: %v", err)
	}

	second, err := store.OpenProjections(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen projections: %v", err)
	}
	defer second.Close()
	if second.Version != 11 {
		t.Fatalf("schema version after reopen = %d, want 11", second.Version)
	}
	rows, err := second.Reader().QueryContext(t.Context(), `
		SELECT player_id, career, badge, system, first_career, earned_seq, earned_at,
		       earned_sim_t IS NULL, context IS NULL
		FROM badge_award ORDER BY career, badge`)
	if err != nil {
		t.Fatalf("read badge awards: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var playerID, earnedSeq, earnedAt, simNull, contextNull int64
		var career, badge, system, firstCareer string
		if err := rows.Scan(&playerID, &career, &badge, &system, &firstCareer, &earnedSeq, &earnedAt, &simNull, &contextNull); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d/%s/%s/%s/%s/%d/%d/%d/%d", playerID, career, badge, system, firstCareer, earnedSeq, earnedAt, simNull, contextNull))
	}
	want := []string{
		"7//lifetime//first-save/11/1770000000000/1/1",
		"7/second-save/career-award/system-hash//12/1770000001000/0/0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("badge awards after restart = %v, want %v", got, want)
	}
}

// TestMigrationsAreIdempotent reopens a real file and checks nothing is applied
// twice. It deliberately uses a file rather than :memory: — reopening an
// in-memory database would create a fresh one and prove nothing (§12 WP1).
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")

	first := testutil.EventsAt(t, path)
	tablesBefore := tableNames(t, first.DB)
	versionsBefore := schemaVersions(t, first.DB)

	// A row that must survive the second migration run untouched.
	set := testutil.Keys(t)
	playerID := testutil.Player(t, first, set, "discord", "100000000000000000")
	if err := first.ClaimHandle(t.Context(), playerID, "whiskers_prime", 1770000000000); err != nil {
		t.Fatalf("claim handle: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if got := tableNames(t, second.DB); !equal(got, tablesBefore) {
		t.Errorf("tables after reopen = %v, want %v", got, tablesBefore)
	}
	if got := schemaVersions(t, second.DB); !equal(got, versionsBefore) {
		t.Errorf("schema_version rows after reopen = %v, want %v (a migration was applied twice)", got, versionsBefore)
	}
	if second.Version != first.Version {
		t.Errorf("version after reopen = %d, want %d", second.Version, first.Version)
	}

	h, err := second.HandleByLC(t.Context(), "WHISKERS_PRIME")
	if err != nil {
		t.Fatalf("handle lost across reopen: %v", err)
	}
	if h.PlayerID != playerID {
		t.Errorf("handle player_id = %d, want %d", h.PlayerID, playerID)
	}

	// Third run, to be sure the second did not leave the version table in a
	// state that only tolerates one extra pass.
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
	third, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer third.Close()
	if got := schemaVersions(t, third.DB); !equal(got, versionsBefore) {
		t.Errorf("schema_version rows after third open = %v, want %v", got, versionsBefore)
	}
}

func TestMigrationsRunOnMemoryStores(t *testing.T) {
	e := testutil.MemEvents(t)
	if e.Version != 5 {
		t.Errorf("version = %d, want 5", e.Version)
	}
	if got := len(tableNames(t, e.DB)); got == 0 {
		t.Error("in-memory store has no tables")
	}
	// One handle, shared: two sql.Open(":memory:") calls would be two databases.
	if e.Reader() != e.Writer() {
		t.Error("in-memory store must share one handle between reader and writer")
	}
	if e.Path() != store.MemoryPath {
		t.Errorf("path = %q, want %q", e.Path(), store.MemoryPath)
	}
}

func TestProjectionCountsIncludeScopeTables(t *testing.T) {
	p := testutil.MemProjections(t)
	for _, stmt := range []string{
		`INSERT INTO career_body (player_id, career, system, kind, body, first_seq) VALUES (1, 'testcareer000001', 'testsystem000001', 'soi', 'luna', 1)`,
		`INSERT INTO career_kitten (player_id, career, system, kid, name, updated_seq) VALUES (1, 'testcareer000001', 'testsystem000001', 'kid1', 'Mittens', 1)`,
		`INSERT INTO career_stat (player_id, career, stat, value, updated_seq) VALUES (1, 'testcareer000001', 'landings', 2, 1)`,
		`INSERT INTO system_stat (player_id, system, stat, value, updated_seq) VALUES (1, 'testsystem000001', 'landings', 2, 1)`,
		`INSERT INTO system (hash, system_id, name, slug, home_body, body_count, reported_complete, first_seq) VALUES ('testsystem000001', 'Test', 'Test', 'test', 'home', 1, 1, 1)`,
		`INSERT INTO system_body (hash, body, name, class, kind, rank, radius_m, mass_kg, soi_m, atmo_m, ocean_m, angvel, axis_x, axis_y, axis_z, ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq) VALUES ('testsystem000001', 'home', 'Home', 'Anything', 'other', 0, 1, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 1)`,
		`INSERT INTO badge_award (player_id, career, badge, earned_seq, earned_at) VALUES (1, '', 'first_steps', 1, 1770000000000)`,
	} {
		if _, err := p.Writer().ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed projection scope count: %v", err)
		}
	}
	c, err := p.Counts(t.Context())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if c.CareerStat != 1 || c.SystemStat != 1 || c.CareerBody != 1 || c.CareerKitten != 1 || c.System != 1 || c.SystemBody != 1 || c.BadgeAward != 1 {
		t.Errorf("scope counts = career stat %d, system stat %d, career body %d, career kitten %d, systems %d, system bodies %d, badge awards %d; want all 1",
			c.CareerStat, c.SystemStat, c.CareerBody, c.CareerKitten, c.System, c.SystemBody, c.BadgeAward)
	}
}

// TestTwoHandlesOnOneFile settles the §5.4 question: does the plan's
// writer-handle + reader-handle arrangement actually work under tursogo's
// exclusive whole-file lock, or does it self-deadlock?
//
// Answer: it works. Both handles live in the same process, and the lock only
// excludes *other processes* (see TestSecondProcessIsLockedOut). Recorded in
// docs/DECISIONS.md.
func TestTwoHandlesOnOneFile(t *testing.T) {
	e := testutil.Events(t)

	if e.Reader() == e.Writer() {
		t.Fatal("file-backed store collapsed to one handle; the split is what this test checks")
	}
	if got := e.Writer().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer MaxOpenConns = %d, want 1 (§5.4)", got)
	}
	if got := e.Reader().Stats().MaxOpenConnections; got != 4 {
		t.Errorf("reader MaxOpenConns = %d, want 4 (§5.4)", got)
	}

	set := testutil.Keys(t)
	playerID := testutil.Player(t, e, set, "discord", "reader-writer")

	// A read on the reader handle while a write transaction is open on the
	// writer handle. If the two handles fought over the file lock, this is
	// where it would hang.
	t.Run("read during open write transaction", func(t *testing.T) {
		tx, err := e.Writer().BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()

		if _, _, err := e.InsertEvents(t.Context(), tx, playerID, []store.Event{newEvent(t, "vehicle.rud")}); err != nil {
			t.Fatalf("insert inside txn: %v", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		var n int64
		if err := e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM event`).Scan(&n); err != nil {
			t.Fatalf("reader blocked by an open write txn on the other handle: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})

	// Sustained: one writer goroutine (matching §5.5's discipline) against four
	// concurrent readers, both handles hot.
	t.Run("sustained concurrent load", func(t *testing.T) {
		const writes, reads = 200, 200
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()

		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			errs []error
		)
		record := func(err error) {
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			for range writes {
				if _, _, err := e.InsertEvents(ctx, nil, playerID, []store.Event{newEvent(t, "telemetry.window")}); err != nil {
					record(fmt.Errorf("write: %w", err))
					return
				}
			}
		}()
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range reads {
					var n int64
					if err := e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM event`).Scan(&n); err != nil {
						record(fmt.Errorf("read: %w", err))
						return
					}
				}
			}()
		}

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("two-handle load deadlocked (90 s); the §5.4 split does not work and the store must use a single handle")
		}
		for _, err := range errs {
			t.Errorf("concurrent load: %v", err)
		}
	})
}

// lockProbeEnv carries the database path to the helper subprocess below.
const lockProbeEnv = "CATLOG_TEST_LOCK_PROBE"

// TestSecondProcessIsLockedOut documents the constraint the whole design rests
// on: another *process* cannot open the file at all, not even to read (§5.4).
// It is why catlogctl goes through the admin API rather than opening files, why
// no backup or metrics sidecar may touch a live database, and why a deploy must
// fully stop the old catlogd before starting the new one.
//
// It re-executes this test binary as the second process (the standard Go
// helper-process pattern), so nothing has to be compiled.
func TestSecondProcessIsLockedOut(t *testing.T) {
	if os.Getenv(lockProbeEnv) != "" {
		t.Skip("running as the helper subprocess")
	}
	e := testutil.Events(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProbeHelper$", "-test.v")
	cmd.Env = append(os.Environ(), lockProbeEnv+"="+e.Path())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper subprocess: %v\n%s", err, out)
	}

	switch {
	case strings.Contains(string(out), "PROBE_READ_OK"):
		t.Fatalf("a second process read the live database; the one-process-per-file rule no longer holds:\n%s", out)
	case !strings.Contains(string(out), "PROBE_FAILED"):
		t.Fatalf("helper produced no verdict:\n%s", out)
	case !strings.Contains(strings.ToLower(string(out)), "lock"):
		t.Errorf("second process failed for a reason other than the file lock:\n%s", out)
	}
}

// TestLockProbeHelper is the subprocess half of TestSecondProcessIsLockedOut.
// It is a no-op unless the environment names a database to probe.
func TestLockProbeHelper(t *testing.T) {
	path := os.Getenv(lockProbeEnv)
	if path == "" {
		t.Skip("helper for TestSecondProcessIsLockedOut")
	}
	db, err := sql.Open(store.DriverName, path)
	if err != nil {
		fmt.Println("PROBE_FAILED open:", err)
		return
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM player`).Scan(&n); err != nil {
		fmt.Println("PROBE_FAILED read:", err)
		return
	}
	fmt.Println("PROBE_READ_OK", n)
}

// TestWALGrowsUntilCheckpointed proves the deviation recorded in
// docs/DECISIONS.md: tursogo never auto-checkpoints, so the -wal file grows
// while the main file stays tiny until PRAGMA wal_checkpoint(TRUNCATE) runs.
func TestWALGrowsUntilCheckpointed(t *testing.T) {
	e := testutil.Events(t) // checkpoint timer disabled, so this is deterministic
	set := testutil.Keys(t)
	playerID := testutil.Player(t, e, set, "discord", "wal")

	const n = 4000
	if err := e.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		evs := make([]store.Event, 0, 200)
		for i := range n {
			evs = append(evs, newEvent(t, "telemetry.window"))
			if len(evs) == cap(evs) || i == n-1 {
				if _, _, err := e.InsertEvents(t.Context(), tx, playerID, evs); err != nil {
					return err
				}
				evs = evs[:0]
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	walBefore, fileBefore := e.WALSize(), e.FileSize()
	if walBefore == 0 {
		t.Fatal("no WAL after 4000 inserts; this test can no longer detect a regression")
	}
	t.Logf("before checkpoint: db=%d B wal=%d B", fileBefore, walBefore)

	if err := e.Checkpoint(t.Context()); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	walAfter, fileAfter := e.WALSize(), e.FileSize()
	t.Logf("after  checkpoint: db=%d B wal=%d B", fileAfter, walAfter)

	if walAfter != 0 {
		t.Errorf("wal = %d B after TRUNCATE checkpoint, want 0", walAfter)
	}
	if fileAfter <= fileBefore {
		t.Errorf("main file did not grow across the checkpoint (%d → %d B); pages were not folded back",
			fileBefore, fileAfter)
	}

	// Data must survive the checkpoint and a reopen.
	got, err := e.CountEvents(t.Context(), playerID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Errorf("events after checkpoint = %d, want %d", got, n)
	}
}

// TestCloseCheckpoints pins the shutdown half of the WAL story: Close runs a
// final checkpoint, so a stopped catlogd never leaves an unbounded -wal behind
// (tursogo's own Close does not do this).
func TestCloseCheckpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.db")

	e, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	set := testutil.Keys(t)
	playerID := testutil.Player(t, e, set, "discord", "close")
	for range 500 {
		if _, _, err := e.InsertEvents(t.Context(), nil, playerID, []store.Event{newEvent(t, "vehicle.staging")}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if e.WALSize() == 0 {
		t.Fatal("no WAL to checkpoint")
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("wal = %d B after Close, want 0 (Close must checkpoint)", fi.Size())
	}

	// Everything is still there on reopen.
	reopened, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	n, err := reopened.CountEvents(t.Context(), playerID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 500 {
		t.Errorf("events after reopen = %d, want 500", n)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	e, err := store.OpenEvents(t.Context(), filepath.Join(t.TempDir(), "events.db"), testutil.Options())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// A second Close must not hang on the already-closed stop channel; an error
	// from the closed handles is fine, a deadlock is not.
	done := make(chan struct{})
	go func() { _ = e.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("second Close deadlocked")
	}
}

func TestCheckpointTimerRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	e, err := store.OpenEvents(t.Context(), path, store.Options{
		Logger:             testutil.DiscardLogger(),
		CheckpointInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	set := testutil.Keys(t)
	playerID := testutil.Player(t, e, set, "discord", "timer")
	for range 400 {
		if _, _, err := e.InsertEvents(t.Context(), nil, playerID, []store.Event{newEvent(t, "vehicle.staging")}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if e.WALSize() == 0 {
			return // the timer fired and truncated
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Errorf("background checkpoint never truncated the WAL (still %d B)", e.WALSize())
}

func TestWithWriteTxRollsBack(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	playerID := testutil.Player(t, e, set, "discord", "rollback")

	sentinel := errors.New("boom")
	err := e.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		if _, _, err := e.InsertEvents(t.Context(), tx, playerID, []store.Event{newEvent(t, "vehicle.rud")}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
	n, err := e.CountEvents(t.Context(), playerID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("events after rollback = %d, want 0", n)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := store.OpenEvents(t.Context(), "", testutil.Options()); err == nil {
		t.Error("OpenEvents(\"\") succeeded, want an error")
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "events.db")
	e := testutil.EventsAt(t, path)
	if _, err := os.Stat(e.Path()); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestProjectionCheckpoint(t *testing.T) {
	p := testutil.MemProjections(t)

	seq, err := p.Checkpoint(t.Context(), nil, store.AllProjections)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if seq != 0 {
		t.Errorf("initial checkpoint = %d, want 0", seq)
	}

	if err := p.SetCheckpoint(t.Context(), nil, store.AllProjections, 42); err != nil {
		t.Fatalf("SetCheckpoint: %v", err)
	}
	if err := p.SetCheckpoint(t.Context(), nil, store.AllProjections, 99); err != nil {
		t.Fatalf("SetCheckpoint (update): %v", err)
	}
	if seq, err = p.Checkpoint(t.Context(), nil, store.AllProjections); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if seq != 99 {
		t.Errorf("checkpoint = %d, want 99", seq)
	}
}

// TestStatAheadForPlayerMatchesStatAhead pins the collapsed rank query to the
// per-row one: same counts, same tie rule, in both directions. If the two ever
// disagree, a profile shows a rank its board page contradicts.
func TestStatAheadForPlayerMatchesStatAhead(t *testing.T) {
	p := testutil.MemProjections(t)

	// Two boards, four players, including a value tie broken by updated_seq.
	rows := []struct {
		player int64
		stat   string
		value  float64
		seq    int64
	}{
		{1, "rud_total", 10, 5}, {2, "rud_total", 25, 3}, {3, "rud_total", 25, 2}, {4, "rud_total", 7, 9},
		{1, "fastest_to_orbit", 300, 4}, {2, "fastest_to_orbit", 120, 6}, {3, "fastest_to_orbit", 120, 1},
	}
	for _, r := range rows {
		if _, err := p.Writer().ExecContext(t.Context(),
			`INSERT INTO player_stat (player_id, stat, value, context, updated_seq) VALUES (?, ?, ?, NULL, ?)`,
			r.player, r.stat, r.value, r.seq); err != nil {
			t.Fatal(err)
		}
	}

	for _, r := range rows {
		asc := r.stat == "fastest_to_orbit" // career times rank smallest-first
		bulk, err := p.StatAheadForPlayer(t.Context(), r.player, []string{r.stat}, asc)
		if err != nil {
			t.Fatalf("StatAheadForPlayer(%d, %s): %v", r.player, r.stat, err)
		}
		one, err := p.StatAhead(t.Context(), r.stat, asc, r.value, r.seq)
		if err != nil {
			t.Fatalf("StatAhead(%s): %v", r.stat, err)
		}
		if bulk[r.stat] != one {
			t.Errorf("player %d on %s: bulk ahead = %d, per-row = %d", r.player, r.stat, bulk[r.stat], one)
		}
	}

	// Both directions in one call each, the way a profile asks.
	bulk, err := p.StatAheadForPlayer(t.Context(), 2, []string{"rud_total"}, false)
	if err != nil {
		t.Fatalf("StatAheadForPlayer: %v", err)
	}
	if bulk["rud_total"] != 1 { // only the seq-2 tie-winner is ahead of seq 3
		t.Errorf("player 2 rud_total ahead = %d, want 1 (tie broken by earliest seq)", bulk["rud_total"])
	}
	if empty, err := p.StatAheadForPlayer(t.Context(), 2, nil, false); err != nil || empty != nil {
		t.Errorf("no stats = (%v, %v), want (nil, nil)", empty, err)
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
