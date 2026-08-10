// Package directory is the in-memory player_id ↔ handle map (§5.4).
//
// # Why it exists
//
// catlog keeps two Turso database files and cross-file joins are impossible
// (§5.4). Every projection row is keyed by player_id, and every public surface —
// leaderboards, profiles, the feed — is keyed by handle, so something has to
// bridge the two in Go. That something is this package: a snapshot of the
// `handle` table joined to `player.banned_at`, loaded at start and reloaded
// whenever a handle is created, revoked or a player is banned.
//
// # Why bans live here too
//
// §4.7 says a purge heals the projections on the next rebuild and that the fast
// path filters banned players in read queries. The fast path cannot filter in
// SQL — `banned_at` is in the other file — so it filters here instead: a banned
// player is simply absent from the directory, which makes every read surface
// treat them as if they had no handle at all. That is one place to get right
// rather than one per endpoint.
//
// A **shadowbanned** player (migration 0005) is absent for the same reason and
// by the same predicate ([store.DirectoryRow.Hidden]), but for a different
// duration: their events have already left the log, so the rebuild that follows
// removes them from the projections permanently and this map is only what
// covers the minutes in between.
package directory

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/meow-sci/catlog/server/internal/store"
)

// Entry is a resolved handle.
type Entry struct {
	// Handle is the display casing (§4.7 preserves it; uniqueness is on the
	// lowercase form).
	Handle string
	// PlayerID is the events.db player_id, which is also the projections.db key.
	PlayerID int64
	// Since is the handle's creation time in unix ms — the `since` field of
	// `GET /v1/players/{handle}` (§4.8).
	Since int64
}

// Directory is a concurrent-safe snapshot of the live handles.
//
// The zero value is not usable; call [New] then [Directory.Reload].
type Directory struct {
	events *store.Events

	mu sync.RWMutex
	// byLC resolves a lowercased handle. Banned players are absent.
	byLC map[string]Entry
	// primary is a player's public handle: the oldest one they still hold, so
	// the identity a leaderboard shows does not move when they claim a second.
	primary map[int64]Entry
	// banned is every banned player_id. Kept rather than discarded so a rank
	// can discount the banned players ahead of a visible one.
	banned map[int64]bool
}

// New builds an empty directory over an events database. Nothing is loaded
// until [Directory.Reload] runs.
func New(events *store.Events) *Directory {
	return &Directory{
		events:  events,
		byLC:    map[string]Entry{},
		primary: map[int64]Entry{},
		banned:  map[int64]bool{},
	}
}

// Reload rebuilds the snapshot from events.db. It is the callback §5.4 asks for:
// handle creation, revocation and ban all call it, and it is cheap enough
// (one indexed scan of a table with one row per handle) that a finer-grained
// invalidation would be false economy.
//
// A failed reload leaves the previous snapshot in place: serving slightly stale
// handles beats serving none.
func (d *Directory) Reload(ctx context.Context) error {
	rows, err := d.events.Directory(ctx)
	if err != nil {
		return err
	}

	byLC := make(map[string]Entry, len(rows))
	primary := make(map[int64]Entry, len(rows))
	banned := map[int64]bool{}
	for _, r := range rows {
		if r.Hidden() {
			// Banned and shadowbanned are one thing here. A shadow ban is
			// silent on the *ingest* side — the client keeps working, and keeps
			// producing the evidence a review reads — but a moderation action
			// has to take effect on the public side immediately, and this map
			// is the only mechanism that acts faster than a rebuild.
			banned[r.PlayerID] = true
			continue
		}
		e := Entry{Handle: r.Handle, PlayerID: r.PlayerID, Since: r.CreatedAt}
		byLC[r.HandleLC] = e
		// The query orders by (player_id, created_at, handle), so the first
		// handle seen for a player is the oldest.
		if _, seen := primary[r.PlayerID]; !seen {
			primary[r.PlayerID] = e
		}
	}

	d.mu.Lock()
	d.byLC, d.primary, d.banned = byLC, primary, banned
	d.mu.Unlock()
	return nil
}

// Handle returns a player's public handle, reporting false when the player is
// unknown, holds no handle, or is banned (§4.8: banned players 404).
func (d *Directory) Handle(playerID int64) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.primary[playerID]
	return e.Handle, ok
}

// Lookup resolves a handle case-insensitively, reporting false for an unknown,
// retired or banned handle.
func (d *Directory) Lookup(handle string) (Entry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.byLC[store.LC(handle)]
	return e, ok
}

// Search finds live handles containing q, case-insensitively: the handles whose
// lowercase form starts with q first, then the rest that merely contain it, each
// group in lexicographic order. limit caps the result; truncated reports that
// there were more.
//
// # Why it is a scan of the map rather than a query
//
// The directory is already the authoritative set of *visible* handles — a
// banned player is absent from it (see the package comment) — so searching it
// needs no ban filter and no round trip to SQL, which has no index that could
// answer a substring query anyway. The cost is one pass over one string per
// live handle, which is the same pass [Reload] does on every handle claim.
//
// # Why the whole match set is collected before truncating
//
// This is an unauthenticated, CDN-fronted endpoint, so its answer has to be a
// pure function of its query: stopping the scan early would hand back whichever
// matches Go's randomised map iteration reached first, and a CDN would cache one
// arbitrary subset per query. Sorting the full match set costs more and is the
// only way the same URL means the same thing twice.
func (d *Directory) Search(q string, limit int) (handles []string, truncated bool) {
	q = store.LC(strings.TrimSpace(q))
	if q == "" || limit <= 0 {
		return nil, false
	}

	var prefix, contains []string
	d.mu.RLock()
	for lc := range d.byLC {
		switch {
		case strings.HasPrefix(lc, q):
			prefix = append(prefix, lc)
		case strings.Contains(lc, q):
			contains = append(contains, lc)
		}
	}
	slices.Sort(prefix)
	slices.Sort(contains)
	all := append(prefix, contains...)
	if len(all) > limit {
		all, truncated = all[:limit], true
	}
	// Resolved to display casing only now, so the ordering key stays the
	// lowercase form that uniqueness is defined on (§4.7).
	handles = make([]string, len(all))
	for i, lc := range all {
		handles[i] = d.byLC[lc].Handle
	}
	d.mu.RUnlock()
	return handles, truncated
}

// BannedIDs lists every banned player_id. The read API subtracts these from a
// rank, which is the one place the count of hidden players matters rather than
// just their absence.
func (d *Directory) BannedIDs() []int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.banned) == 0 {
		return nil
	}
	out := make([]int64, 0, len(d.banned))
	for id := range d.banned {
		out = append(out, id)
	}
	return out
}

// Len reports how many live, unbanned handles are known — a /admin/stats number.
func (d *Directory) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.byLC)
}
