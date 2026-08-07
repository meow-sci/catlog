// Package testutil spins up throwaway stores, mints test credentials and holds
// the golden-file helpers shared by the server test suites (§5.2).
//
// # Why every store gets its own directory
//
// tursogo holds an exclusive whole-file lock that excludes other processes
// entirely, so two tests must never share a database path. Every constructor
// here allocates a fresh [testing.T.TempDir] and registers cleanup, which makes
// `go test ./...` safe to run with its default per-package parallelism.
//
// # File-backed versus in-memory
//
// [MemEvents] and [MemProjections] are the fast path for logic that only needs
// tables. They are genuinely different databases though — an in-memory store
// has one handle instead of two and no WAL at all — so anything that depends on
// the reader/writer split, the file lock or checkpointing must use the
// file-backed [Events] and [Projections] (§12 WP1).
package testutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// DataDir returns a fresh data directory laid out like §3: it contains keys/
// and is where events.db and projections.db go.
func DataDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// Options returns store options suited to tests: logs discarded, background
// checkpointing off. Tests that exercise checkpointing call
// [store.DB.Checkpoint] directly, which is deterministic; a timer would not be.
// Close still checkpoints, so nothing leaks an unbounded WAL.
func Options() store.Options {
	return store.Options{Logger: DiscardLogger()}
}

// DiscardLogger is a slog.Logger that drops everything.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Events opens a file-backed events.db in a fresh temp directory, migrated and
// closed at test end. Use this whenever the file lock, the reader/writer split
// or the WAL matters.
func Events(t *testing.T) *store.Events {
	t.Helper()
	return EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
}

// EventsAt opens a file-backed events.db at an explicit path — for tests that
// need to reopen the same file, or to inspect it on disk.
func EventsAt(t *testing.T, path string) *store.Events {
	t.Helper()
	db, err := store.OpenEvents(t.Context(), path, Options())
	if err != nil {
		t.Fatalf("open events store at %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close events store: %v", err)
		}
	})
	return db
}

// Projections opens a file-backed projections.db in a fresh temp directory.
func Projections(t *testing.T) *store.Projections {
	t.Helper()
	return ProjectionsAt(t, filepath.Join(t.TempDir(), "projections.db"))
}

// ProjectionsAt opens a file-backed projections.db at an explicit path.
func ProjectionsAt(t *testing.T, path string) *store.Projections {
	t.Helper()
	db, err := store.OpenProjections(t.Context(), path, Options())
	if err != nil {
		t.Fatalf("open projections store at %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close projections store: %v", err)
		}
	})
	return db
}

// MemEvents opens an in-memory events.db. Fast, but single-handle and
// WAL-less — see the package doc.
func MemEvents(t *testing.T) *store.Events {
	t.Helper()
	return EventsAt(t, store.MemoryPath)
}

// MemProjections opens an in-memory projections.db.
func MemProjections(t *testing.T) *store.Projections {
	t.Helper()
	return ProjectionsAt(t, store.MemoryPath)
}

// Keys creates a fresh key set in a temp directory: a new signing key, session
// key and pepper, so user_keys never collide across tests.
func Keys(t *testing.T) *keys.Set {
	t.Helper()
	set, err := keys.LoadOrCreate(filepath.Join(t.TempDir(), "keys"))
	if err != nil {
		t.Fatalf("create keys: %v", err)
	}
	return set
}

// KeysAt creates or loads a key set under an explicit data directory, matching
// the §3 layout (dir/keys/).
func KeysAt(t *testing.T, dataDir string) *keys.Set {
	t.Helper()
	set, err := keys.LoadOrCreate(filepath.Join(dataDir, "keys"))
	if err != nil {
		t.Fatalf("create keys in %s: %v", dataDir, err)
	}
	return set
}

// Config returns the default configuration pointed at a fresh temp data
// directory, so a test server never touches ./data.
func Config(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Data.Dir = DataDir(t)
	cfg.Data.CheckpointIntervalS = 0
	return cfg
}

// ClientKey generates a P-256 key pair standing in for a player's credential
// key (§4.6). Fresh per call.
func ClientKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	return key
}

// Player creates a player with a user_key derived from subject and returns its
// player_id.
func Player(t *testing.T, e *store.Events, set *keys.Set, idp, subject string) int64 {
	t.Helper()
	id, err := e.EnsurePlayer(context.Background(), nil, set.UserKey(idp, subject), idp, 0)
	if err != nil {
		t.Fatalf("ensure player %s:%s: %v", idp, subject, err)
	}
	return id
}

// ULID mints a ULID, failing the test rather than returning an error.
func ULID(t *testing.T) ids.ID {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatalf("mint ulid: %v", err)
	}
	return id
}
