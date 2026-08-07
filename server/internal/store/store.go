// Package store owns the two Turso databases (events, projections): open
// discipline, embedded migrations and typed queries — no ORM (§5.4).
//
// # One process per file
//
// tursogo takes an exclusive whole-file lock that excludes other processes
// entirely — a second process cannot even SELECT. That is why `catlogctl` goes
// through the admin API instead of opening a database (§5.9), why no sidecar
// may touch a live file, and why a deploy must fully stop the old process
// before starting the new one. Tests get a fresh temp file each (see package
// testutil).
//
// # Two handles per file
//
// Per §5.4 each file is opened twice: a writer capped at one connection, so
// write transactions are serialized by the pool itself, and a reader capped at
// four. Both handles live in this process, which the lock permits — verified,
// see docs/DECISIONS.md (WP1). An in-memory database is the exception: two
// `sql.Open(":memory:")` calls produce two unrelated databases, so [Open]
// collapses to a single handle there.
//
// # The WAL never checkpoints itself
//
// tursogo's WAL has no working auto-checkpoint: `PRAGMA wal_autocheckpoint`
// cannot be read, setting it does nothing, and `Close` does not checkpoint
// either. Left alone the -wal file grows for the life of the process while the
// main database file stays near-empty. [DB.Checkpoint] runs
// `PRAGMA wal_checkpoint(TRUNCATE)` explicitly; [Open] runs it on a timer and
// [DB.Close] runs it once more on the way out. This is a deviation from §5.4's
// "WAL is the default; set nothing" — see docs/DECISIONS.md (WP1).
//
// # SQL constraints
//
// No `WITH RECURSIVE` (unimplemented in tursogo — no flag enables it), no
// `VACUUM`, no `WITHOUT ROWID`, no `STRICT`, no expression indexes (§5.4).
// Dedup and upserts use `INSERT OR IGNORE` / `ON CONFLICT DO UPDATE` rather
// than error inspection, because tursogo collapses every constraint violation
// onto one sentinel and offers no extended result code.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "turso.tech/database/tursogo" // registers the "turso" database/sql driver
)

// DriverName is the database/sql driver tursogo registers.
const DriverName = "turso"

// MemoryPath opens a private in-memory database. Handy for throwaway unit
// stores; migration behaviour under the file lock and the WAL only shows up
// against a real file, so tests that care about either must use one.
const MemoryPath = ":memory:"

// Connection caps per §5.4. The writer's cap of one is load-bearing: it is what
// makes concurrent write transactions impossible without any locking of our own.
const (
	writerConns = 1
	readerConns = 4
)

// DefaultCheckpointInterval is the interval catlog ships with; it is what
// config.Default puts in `[data] checkpoint_interval_s`.
const DefaultCheckpointInterval = 60 * time.Second

//go:embed migrations/events/*.sql
var eventsMigrations embed.FS

//go:embed migrations/projections/*.sql
var projectionsMigrations embed.FS

// Options configures [Open].
type Options struct {
	// Logger receives open/migrate/checkpoint events. Defaults to slog.Default.
	Logger *slog.Logger
	// CheckpointInterval is how often the background WAL checkpoint runs.
	// Zero or negative disables the timer — [DB.Close] still checkpoints, so
	// disabling it is safe for a short-lived store (a test) and unwise for a
	// long-lived one. [DefaultCheckpointInterval] is what catlogd uses.
	CheckpointInterval time.Duration
	// Now is the server clock. Defaults to [time.Now].
	//
	// This is the seam that decides an event's `recv_time` — the timestamp
	// catlog treats as authoritative, as opposed to the client's untrusted
	// `wall_t` (§4.1). catlogd hands every package the same
	// `internal/clock.Clock`, so a development build can move the whole
	// server's notion of now and exercise time-bucketed projections without
	// waiting a calendar year for one.
	Now func() time.Time
}

func (o Options) now() func() time.Time {
	if o.Now != nil {
		return o.Now
	}
	return time.Now
}

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// DB is one open Turso database file: a writer handle, a reader handle, an
// applied migration set and a WAL checkpoint timer.
//
// A DB is safe for concurrent use.
type DB struct {
	path   string
	name   string // "events" or "projections", for logs
	memory bool
	log    *slog.Logger

	w *sql.DB
	r *sql.DB

	// Version is the schema version after migration.
	Version int

	// now is the server clock; see [Options.Now]. Never nil.
	now func() time.Time

	closeOnce sync.Once
	closeErr  error
	stop      chan struct{}
	stopped   chan struct{}
}

// nowMillis is the store's clock, in unix milliseconds.
//
// It was a package-level function reading [time.Now] directly. It is a method
// now because it stamps `recv_time`, and `recv_time` is the one timestamp in
// catlog that is authoritative — so it has to be the same clock the rest of the
// server is reading, not a second independent one.
func (d *DB) nowMillis() int64 { return d.now().UnixMilli() }

// Querier is the read/write surface shared by *sql.DB and *sql.Tx, so every
// typed query in this package composes into a caller's transaction or runs
// standalone.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Events is events.db: the raw log plus the identity tables (§5.4).
type Events struct{ *DB }

// Projections is projections.db: everything derived and rebuildable (§5.4).
type Projections struct{ *DB }

// OpenEvents opens (creating if needed) events.db at path and migrates it.
func OpenEvents(ctx context.Context, path string, opts Options) (*Events, error) {
	db, err := open(ctx, path, "events", eventsMigrations, "migrations/events", opts)
	if err != nil {
		return nil, err
	}
	return &Events{db}, nil
}

// OpenProjections opens (creating if needed) projections.db at path and
// migrates it.
func OpenProjections(ctx context.Context, path string, opts Options) (*Projections, error) {
	db, err := open(ctx, path, "projections", projectionsMigrations, "migrations/projections", opts)
	if err != nil {
		return nil, err
	}
	return &Projections{db}, nil
}

func open(ctx context.Context, path, name string, fsys fs.FS, dir string, opts Options) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	memory := path == MemoryPath

	if !memory {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("store: create directory for %s: %w", path, err)
		}
	}

	db := &DB{
		path:    path,
		name:    name,
		memory:  memory,
		log:     opts.logger().With("db", name),
		now:     opts.now(),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	var err error
	if db.w, err = openHandle(ctx, path, writerConns); err != nil {
		return nil, fmt.Errorf("store: open %s writer: %w", path, err)
	}

	if memory {
		// Two sql.Open(":memory:") calls are two unrelated databases, so the
		// reader must be the same handle. The one-connection cap then also
		// serializes reads, which is fine for a throwaway store.
		db.r = db.w
	} else if db.r, err = openHandle(ctx, path, readerConns); err != nil {
		db.w.Close()
		return nil, fmt.Errorf("store: open %s reader: %w", path, err)
	}

	if db.Version, err = migrate(ctx, db.w, fsys, dir, db.log); err != nil {
		db.closeHandles()
		return nil, err
	}

	if opts.CheckpointInterval > 0 && !memory {
		go db.checkpointLoop(opts.CheckpointInterval)
	} else {
		close(db.stopped)
	}

	db.log.Info("database open", "path", path, "schema_version", db.Version)
	return db, nil
}

func openHandle(ctx context.Context, path string, maxConns int) (*sql.DB, error) {
	// The DSN is a bare filesystem path; there is no file: URI form and no
	// _journal_mode/_foreign_keys params. WAL is already the default journal
	// mode and PRAGMA foreign_keys stays off on purpose (§5.4).
	h, err := sql.Open(DriverName, path)
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(maxConns)
	h.SetMaxIdleConns(maxConns)
	h.SetConnMaxIdleTime(0) // keep connections; reacquiring the file lock is not free
	// sql.Open is lazy — Ping is what surfaces "locked by another process".
	if err := h.PingContext(ctx); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// Path is the database file path, or [MemoryPath].
func (d *DB) Path() string { return d.path }

// Writer is the single-connection handle. Every write goes through it, so write
// transactions are serialized by the connection pool. Callers still keep the
// single-writer-goroutine discipline §5.5 requires — the cap is a backstop, not
// a substitute for it.
func (d *DB) Writer() *sql.DB { return d.w }

// Reader is the four-connection read handle. On an in-memory database it is the
// writer handle.
func (d *DB) Reader() *sql.DB { return d.r }

// WithWriteTx runs fn inside a write transaction on the writer handle,
// committing on success and rolling back on any error or panic.
func (d *DB) WithWriteTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := d.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin %s transaction: %w", d.name, err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit %s transaction: %w", d.name, err)
	}
	return nil
}

// Checkpoint folds the WAL back into the main database file and truncates it.
//
// Nothing else does this: tursogo's auto-checkpoint never fires and Close does
// not checkpoint. It runs on the writer handle, whose single connection
// serializes it against in-flight writes.
func (d *DB) Checkpoint(ctx context.Context) error {
	if d.memory {
		return nil
	}
	if _, err := d.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: checkpoint %s: %w", d.name, err)
	}
	return nil
}

// WALSize reports the size of the -wal sidecar in bytes, or 0 when there is
// none. Exposed for /admin/stats (§5.9) and for the checkpoint tests.
func (d *DB) WALSize() int64 {
	if d.memory {
		return 0
	}
	fi, err := os.Stat(d.path + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

// FileSize reports the size of the main database file in bytes. With no VACUUM
// available (§5.4), watching this is how purge-driven free-page growth gets
// noticed (§13.1).
func (d *DB) FileSize() int64 {
	if d.memory {
		return 0
	}
	fi, err := os.Stat(d.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func (d *DB) checkpointLoop(every time.Duration) {
	defer close(d.stopped)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			before := d.WALSize()
			err := d.Checkpoint(ctx)
			cancel()
			if err != nil {
				// Not fatal: a busy checkpoint just means the WAL keeps growing
				// until the next tick.
				d.log.Warn("wal checkpoint failed", "err", err)
				continue
			}
			d.log.Debug("wal checkpoint", "wal_bytes_before", before, "wal_bytes_after", d.WALSize())
		}
	}
}

// Close stops the checkpoint timer, runs a final checkpoint and closes both
// handles.
//
// It is idempotent — the second and later calls return the first call's result
// without touching the now-closed handles, so a `defer db.Close()` and an
// explicit shutdown path can coexist.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		close(d.stop)
		<-d.stopped

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var errs []error
		if err := d.Checkpoint(ctx); err != nil {
			// Report it, then still close: an un-checkpointed WAL is
			// recoverable (it is replayed on the next open), a leaked file lock
			// is not.
			errs = append(errs, err)
		}
		if err := d.closeHandles(); err != nil {
			errs = append(errs, err)
		}
		d.closeErr = errors.Join(errs...)
		if d.closeErr == nil {
			d.log.Info("database closed", "path", d.path)
		}
	})
	return d.closeErr
}

func (d *DB) closeHandles() error {
	var errs []error
	if d.r != nil && d.r != d.w {
		if err := d.r.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store: close %s reader: %w", d.name, err))
		}
	}
	if d.w != nil {
		if err := d.w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store: close %s writer: %w", d.name, err))
		}
	}
	return errors.Join(errs...)
}

// --- migrations ------------------------------------------------------------

// migration is one embedded NNNN_name.sql file.
type migration struct {
	version int
	name    string
	sql     string
}

// migrate applies every embedded migration not yet recorded in schema_version,
// in version order, each in its own transaction.
//
// schema_version carries a primary key on v, plus the file name and the apply
// time — §5.4 sketches `schema_version(v INTEGER NOT NULL)`, but without the
// key nothing stops a version being recorded twice (see docs/DECISIONS.md).
func migrate(ctx context.Context, w *sql.DB, fsys fs.FS, dir string, log *slog.Logger) (int, error) {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_version (
  v          INTEGER NOT NULL PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at INTEGER NOT NULL
)`
	if _, err := w.ExecContext(ctx, ddl); err != nil {
		return 0, fmt.Errorf("store: create schema_version: %w", err)
	}

	migrations, err := loadMigrations(fsys, dir)
	if err != nil {
		return 0, err
	}

	applied, err := appliedVersions(ctx, w)
	if err != nil {
		return 0, err
	}

	current := 0
	for _, m := range migrations {
		if applied[m.version] {
			current = max(current, m.version)
			continue
		}
		if err := applyOne(ctx, w, m); err != nil {
			return 0, err
		}
		log.Info("migration applied", "version", m.version, "name", m.name)
		current = max(current, m.version)
	}
	return current, nil
}

func applyOne(ctx context.Context, w *sql.DB, m migration) (err error) {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", m.name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// tursogo splits a multi-statement Exec itself, inside a transaction as
	// well as outside, so a migration file goes over the wire as one call.
	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", m.name, err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_version (v, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("store: record migration %s: %w", m.name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", m.name, err)
	}
	return nil
}

func appliedVersions(ctx context.Context, w *sql.DB) (map[int]bool, error) {
	rows, err := w.QueryContext(ctx, `SELECT v FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_version: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: scan schema_version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_version: %w", err)
	}
	return applied, nil
}

func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("store: read migrations %s: %w", dir, err)
	}

	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: migrations %s and %s share version %d", prev, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", e.Name(), err)
		}
		// tursogo rejects an Exec with no executable statement
		// ("API misuse: got null pointer"), so a comment-only file is a
		// packaging mistake worth catching here rather than at Exec time.
		if !hasStatement(string(body)) {
			return nil, fmt.Errorf("store: migration %s contains no SQL statement", e.Name())
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("store: no migrations found in %s", dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseVersion reads the NNNN_ prefix of a migration file name.
func parseVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("store: migration %q is not named NNNN_name.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("store: migration %q has no positive NNNN version prefix", name)
	}
	return v, nil
}

// hasStatement reports whether s holds anything but blank lines and -- comments.
func hasStatement(s string) bool {
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}
