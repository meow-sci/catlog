package authz

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/meow-sci/catlog/server/internal/store"
)

// DenyList is the in-memory ban/revocation set consulted by §4.5.3 step 4 —
// before any database access, which is the whole point: a banned credential
// must cost an attacker a map lookup, not a query.
//
// It is authoritative in-process (single node, §5.8). WP3 owns the mutation
// paths (ban, revoke, purge) and the published `/.well-known/catlog-denylist.json`
// form; this type is the store they refresh.
//
// A DenyList is safe for concurrent use and its zero value is not — use
// [NewDenyList].
type DenyList struct {
	mu   sync.RWMutex
	subs map[string]struct{} // b64u(user_key) — banned or purged accounts
	jkts map[string]struct{} // revoked credential thumbprints
	ver  int64               // bumped on every mutation, for the published form
}

// NewDenyList returns an empty deny-list.
func NewDenyList() *DenyList {
	return &DenyList{subs: map[string]struct{}{}, jkts: map[string]struct{}{}}
}

// HasSub reports whether a license `sub` is banned (§4.5.3 step 4).
func (d *DenyList) HasSub(sub string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.subs[sub]
	return ok
}

// HasJKT reports whether a credential thumbprint is revoked (§4.5.3 step 4).
func (d *DenyList) HasJKT(jkt string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.jkts[jkt]
	return ok
}

// Replace swaps the whole set atomically.
func (d *DenyList) Replace(subs, jkts []string) {
	ns := make(map[string]struct{}, len(subs))
	for _, s := range subs {
		ns[s] = struct{}{}
	}
	nj := make(map[string]struct{}, len(jkts))
	for _, j := range jkts {
		nj[j] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs, d.jkts = ns, nj
	d.ver++
}

// AddSub bans one subject.
func (d *DenyList) AddSub(sub string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs[sub] = struct{}{}
	d.ver++
}

// AddJKT revokes one credential thumbprint.
func (d *DenyList) AddJKT(jkt string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.jkts[jkt] = struct{}{}
	d.ver++
}

// Snapshot returns the sorted contents and the version counter — what the
// published deny-list JWS is built from (§5.8).
func (d *DenyList) Snapshot() (subs, jkts []string, ver int64) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	subs = make([]string, 0, len(d.subs))
	for s := range d.subs {
		subs = append(subs, s)
	}
	jkts = make([]string, 0, len(d.jkts))
	for j := range d.jkts {
		jkts = append(jkts, j)
	}
	slices.Sort(subs)
	slices.Sort(jkts)
	return subs, jkts, d.ver
}

// LoadFrom rebuilds the set from events.db: every purged account's tombstone,
// every banned player, and every revoked credential (§5.8). Called at start and
// after any mutation.
func (d *DenyList) LoadFrom(ctx context.Context, e *store.Events) error {
	tombstones, err := e.Tombstones(ctx)
	if err != nil {
		return fmt.Errorf("authz: load deny-list tombstones: %w", err)
	}
	banned, err := e.BannedUserKeys(ctx)
	if err != nil {
		return fmt.Errorf("authz: load deny-list bans: %w", err)
	}
	jkts, err := e.RevokedJKTs(ctx)
	if err != nil {
		return fmt.Errorf("authz: load deny-list revocations: %w", err)
	}

	subs := make([]string, 0, len(tombstones)+len(banned))
	for _, t := range tombstones {
		subs = append(subs, t.UserKey.B64U())
	}
	for _, uk := range banned {
		subs = append(subs, uk.B64U())
	}
	d.Replace(subs, jkts)
	return nil
}
