package projector

import (
	"sync"

	"github.com/meow-sci/catlog/server/internal/store"
)

// Live holds the process's current projections.db handle behind the RWMutex the
// rebuild's atomic swap needs (§5.6).
//
// Readers take the read lock for the duration of a query, so a swap — which
// closes the handle, renames the file and reopens — can never happen underneath
// an in-flight read. The lock is held for whole queries rather than around a
// pointer read on purpose: handing out the pointer and releasing would let a
// caller run a query against a handle that was closed a microsecond later.
type Live struct {
	mu   sync.RWMutex
	db   *store.Projections
	path string
}

// NewLive wraps an open projections database.
func NewLive(db *store.Projections) *Live {
	return &Live{db: db, path: db.Path()}
}

// With runs fn against the live handle under the read lock. Several may run
// concurrently; a swap waits for all of them.
func (l *Live) With(fn func(*store.Projections) error) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return fn(l.db)
}

// Path is the database file path. It never changes: a rebuild renames a new file
// onto this path rather than pointing the process at a different one, so
// external things (backups, the config) keep working.
func (l *Live) Path() string { return l.path }

// exclusive runs fn with the write lock held, i.e. with every reader shut out.
// The rebuild's close-rename-reopen sequence runs inside it.
func (l *Live) exclusive(fn func(cur *store.Projections) (*store.Projections, error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	next, err := fn(l.db)
	if next != nil {
		l.db = next
	}
	return err
}

// Close closes the current handle. It goes through the write lock so it cannot
// race a rebuild's swap, and it is what catlogd's shutdown must call rather than
// closing the handle it opened: after a rebuild that handle is a closed stale
// one and the live database is a different object on the same path.
func (l *Live) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		return nil
	}
	return l.db.Close()
}
