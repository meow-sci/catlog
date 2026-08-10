package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// ArchivePurger deletes a player's archive prefix. It is the seam §4.7's purge
// path needs and WP10 fills: `archive.Store.Delete(ctx, "players/<sub>/")`
// behind an interface, so the identity layer never imports the archiver and a
// deployment with no archive configured purges correctly by doing nothing.
//
// sub is `b64u(user_key)` — the §5.10 key prefix and the license `sub`.
type ArchivePurger interface {
	DeletePlayerArchive(ctx context.Context, sub string) error
}

// ErrTargetRequired means neither a handle nor a sub was supplied.
var ErrTargetRequired = errors.New("identity: a handle or a sub is required")

// Moderator applies the §4.7 ban, unban and purge operations.
//
// # Both halves, always
//
// Every mutation writes the database **and** the in-memory deny-list, because
// they are consulted at different points of §4.5.3: the deny-list at step 4,
// before any query, and the database at step 5. A ban that reached only the
// database would still work — one step later and one query more expensive —
// but a ban that reached only the deny-list would evaporate at the next
// restart. [Moderator.refresh] rebuilds the deny-list from the database after
// every mutation rather than patching it, so the two can never disagree.
//
// Every method assumes the caller already holds the §5.4 write lock; none of
// them takes it, so they compose inside an admin handler's WithWriteLock.
type Moderator struct {
	events    *store.Events
	deny      *authz.DenyList
	dir       *directory.Directory
	publisher *DenyListPublisher
	archive   ArchivePurger
	log       *slog.Logger
	now       func() time.Time

	mu sync.Mutex
	// purged holds the thumbprints of credentials whose rows a purge deleted.
	// [authz.DenyList.LoadFrom] rebuilds from the database and so cannot know
	// about them; refresh re-applies them afterwards. In-process only, and it
	// does not need to survive a restart: a purged account is refused one step
	// earlier by its tombstone, which does persist (§4.5.3 step 4).
	purged map[string]struct{}
}

// NewModerator builds the moderation service. dir, publisher and archive may be
// nil; the first two only cost the caller a stale read surface, and a nil
// archive is the correct behaviour before WP10 lands.
func NewModerator(events *store.Events, deny *authz.DenyList, dir *directory.Directory, pub *DenyListPublisher, archive ArchivePurger, log *slog.Logger) *Moderator {
	if log == nil {
		log = slog.Default()
	}
	return &Moderator{
		events: events, deny: deny, dir: dir, publisher: pub, archive: archive,
		log: log, now: time.Now, purged: map[string]struct{}{},
	}
}

// SetClock replaces the moderator's clock. Tests only.
func (m *Moderator) SetClock(now func() time.Time) { m.now = now }

// SetArchive installs the purge seam. WP10 calls this at wiring time.
func (m *Moderator) SetArchive(a ArchivePurger) { m.archive = a }

// Target names an account: a handle or a `sub` (the b64u user_key that a
// license carries). Exactly one is needed (§5.9).
type Target struct {
	Handle string `json:"handle,omitempty"`
	Sub    string `json:"sub,omitempty"`
}

// Resolve turns a target into a player row.
func (m *Moderator) Resolve(ctx context.Context, t Target) (store.Player, error) {
	switch {
	case t.Handle != "":
		h, err := m.events.HandleByLC(ctx, t.Handle)
		if err != nil {
			return store.Player{}, err
		}
		return m.events.PlayerByID(ctx, h.PlayerID)
	case t.Sub != "":
		uk, err := keys.ParseUserKey(t.Sub)
		if err != nil {
			return store.Player{}, fmt.Errorf("%w: %w", store.ErrNotFound, err)
		}
		return m.events.PlayerByUserKey(ctx, uk)
	default:
		return store.Player{}, ErrTargetRequired
	}
}

// BanResult reports what a ban changed (§5.9).
type BanResult struct {
	Sub         string   `json:"sub"`
	IdP         string   `json:"idp"`
	BannedAt    int64    `json:"banned_at"`
	Reason      string   `json:"reason"`
	Handles     []string `json:"handles_retired"`
	Credentials []string `json:"credentials_revoked"`
	// Purge is present when the ban also purged (§5.9 `--purge`).
	Purge *PurgeResult `json:"purge,omitempty"`
}

// Ban sets banned_at, revokes every live credential, retires every handle and
// refreshes the deny-list (§5.9).
//
// The handles keep their owner: retirement blocks anyone *else* from claiming
// them (D9), while the surviving `handle` rows are what let [Moderator.Unban]
// hand them back.
func (m *Moderator) Ban(ctx context.Context, p store.Player, reason string) (BanResult, error) {
	if reason == "" {
		reason = "banned"
	}

	handles, err := m.events.HandlesForPlayer(ctx, p.ID)
	if err != nil {
		return BanResult{}, err
	}
	creds, err := m.events.CredentialsForPlayer(ctx, p.ID)
	if err != nil {
		return BanResult{}, err
	}

	// The ban's timestamp is the key [Moderator.Unban] selects on, so it must
	// not collide with a revocation this player already made themselves —
	// otherwise lifting the ban would resurrect a credential the player
	// deliberately killed. Milliseconds make a collision unlikely in
	// production and certain under a frozen test clock; stepping past it costs
	// nothing and removes the question.
	at := m.now().UnixMilli()
	for collidesWithRevocation(creds, at) {
		at++
	}
	res := BanResult{Sub: p.UserKey.B64U(), IdP: p.IdP, BannedAt: at, Reason: reason}

	if err := m.events.SetBan(ctx, nil, p.ID, at, reason); err != nil {
		return res, err
	}
	if err := m.events.RevokeCredentialsForPlayer(ctx, nil, p.ID, at); err != nil {
		return res, err
	}
	for _, h := range handles {
		if err := m.events.MarkHandleRetired(ctx, nil, h.Handle, reason, at); err != nil {
			return res, err
		}
		res.Handles = append(res.Handles, h.Handle)
	}
	for _, c := range creds {
		if !c.Revoked() {
			res.Credentials = append(res.Credentials, c.JKT)
		}
	}

	if err := m.refresh(ctx); err != nil {
		return res, err
	}
	// The user_key is logged truncated by construction (§5.11).
	m.log.Warn("player banned", "player", p.ID, "user_key", p.UserKey, "idp", p.IdP,
		"reason", reason, "handles", res.Handles, "credentials", len(res.Credentials))
	return res, nil
}

// collidesWithRevocation reports whether any already-revoked credential
// carries exactly this timestamp.
func collidesWithRevocation(creds []store.Credential, at int64) bool {
	for _, c := range creds {
		if c.RevokedAt.Valid && c.RevokedAt.Int64 == at {
			return true
		}
	}
	return false
}

// UnbanResult reports what a lifted ban restored (§5.9).
type UnbanResult struct {
	Sub         string   `json:"sub"`
	Handles     []string `json:"handles_restored"`
	Credentials int64    `json:"credentials_restored"`
}

// Unban lifts a ban: it clears banned_at, un-retires the handles the account
// still holds, and restores exactly the credentials that ban revoked.
//
// "Exactly" is the interesting word. A ban stamps one timestamp on every
// credential it revokes, so the inverse can select on it — a credential the
// player revoked from the dashboard, or one an earlier ban revoked, keeps its
// own timestamp and stays revoked (see store.UnrevokeCredentialsAt).
func (m *Moderator) Unban(ctx context.Context, p store.Player) (UnbanResult, error) {
	res := UnbanResult{Sub: p.UserKey.B64U()}
	if !p.Banned() {
		return res, nil
	}
	bannedAt := p.BannedAt.Int64

	handles, err := m.events.HandlesForPlayer(ctx, p.ID)
	if err != nil {
		return res, err
	}
	for _, h := range handles {
		if err := m.events.UnretireHandle(ctx, nil, h.Handle); err != nil {
			return res, err
		}
		res.Handles = append(res.Handles, h.Handle)
	}
	if res.Credentials, err = m.events.UnrevokeCredentialsAt(ctx, nil, p.ID, bannedAt); err != nil {
		return res, err
	}
	if err := m.events.SetBan(ctx, nil, p.ID, 0, ""); err != nil {
		return res, err
	}

	if err := m.refresh(ctx); err != nil {
		return res, err
	}
	m.log.Warn("ban lifted", "player", p.ID, "user_key", p.UserKey,
		"handles", res.Handles, "credentials", res.Credentials)
	return res, nil
}

// PurgeResult reports a purge (§4.7).
type PurgeResult struct {
	Sub      string            `json:"sub"`
	Reason   string            `json:"reason"`
	At       int64             `json:"at"`
	Handles  []string          `json:"handles_retired"`
	Revoked  []string          `json:"credentials_revoked"`
	Deleted  store.PurgeCounts `json:"deleted"`
	Archived bool              `json:"archive_deleted"`
}

// Purge deletes everything a player owns and leaves the minimal tombstone
// (§4.7): events, batches, streams, credentials, handles and the player row go;
// the handle_lc values are retired forever; the archive prefix is deleted if an
// archive store is wired; `{user_key, reason, at}` remains.
//
// The tombstone is what keeps the account banned after every row that could
// have said so is gone — [authz.DenyList.LoadFrom] reads tombstones as banned
// subjects, so a purged account is refused at §4.5.3 step 4 and at login.
func (m *Moderator) Purge(ctx context.Context, p store.Player, reason string) (PurgeResult, error) {
	if reason == "" {
		reason = "purged"
	}
	at := m.now().UnixMilli()
	res := PurgeResult{Sub: p.UserKey.B64U(), Reason: reason, At: at}

	handles, err := m.events.HandlesForPlayer(ctx, p.ID)
	if err != nil {
		return res, err
	}
	creds, err := m.events.CredentialsForPlayer(ctx, p.ID)
	if err != nil {
		return res, err
	}

	// Retire before deleting: PurgePlayer removes the `handle` rows, and a
	// handle that was deleted without being retired would become claimable by
	// the next person to ask (D9).
	for _, h := range handles {
		if err := m.events.MarkHandleRetired(ctx, nil, h.Handle, reason, at); err != nil {
			return res, err
		}
		res.Handles = append(res.Handles, h.Handle)
	}
	for _, c := range creds {
		res.Revoked = append(res.Revoked, c.JKT)
	}

	// The tombstone goes in before the delete, so a failure between the two
	// leaves an account that is banned but still present rather than one that
	// is gone and forgotten.
	if err := m.events.InsertTombstone(ctx, nil, store.Tombstone{UserKey: p.UserKey, Reason: reason, At: at}); err != nil {
		return res, err
	}
	if res.Deleted, err = m.events.PurgePlayer(ctx, p.ID); err != nil {
		return res, err
	}

	// The archive is the one store that outlives the database rows, so its
	// deletion is part of the purge rather than of a later sweep (§4.7, §5.10).
	if m.archive != nil {
		if err := m.archive.DeletePlayerArchive(ctx, res.Sub); err != nil {
			return res, fmt.Errorf("identity: delete archive prefix: %w", err)
		}
		res.Archived = true
	}

	// The revoked jkts of a deleted account have no credential row left to be
	// read from, so they are remembered here; refresh re-applies them after
	// every rebuild from the database.
	m.mu.Lock()
	for _, jkt := range res.Revoked {
		m.purged[jkt] = struct{}{}
	}
	m.mu.Unlock()

	if err := m.refresh(ctx); err != nil {
		return res, err
	}
	m.log.Warn("player purged", "user_key", p.UserKey, "reason", reason,
		"handles", res.Handles, "events", res.Deleted.Events, "archive_deleted", res.Archived)
	return res, nil
}

// --- shadow bans -------------------------------------------------------------
//
// A shadow ban is moderation that withholds instead of deleting, and it is the
// moderation sense of the term rather than the anti-cheat sense Constitution §8
// forbids: applied by a named administrator to a named account, never inferred
// from the shape of anyone's data (§5 exempts bans and purges from §8).
//
// It differs from [Moderator.Ban] in exactly two ways, and both are deliberate:
//
//   - **The credential keeps working.** No revocation, no deny-list entry, no
//     tombstone. The player's mod ships as normal and is answered as normal;
//     their events are routed into `shadowban_event` by the store's one write
//     fork. That is what makes it silent, and it is also what makes it useful —
//     a review needs the evidence to keep arriving, and a loud ban stops it.
//   - **Nothing is deleted.** The log is moved, not dropped, so the two
//     decisions a review ends in — restore, or delete for good — are both still
//     available afterwards. A ban that deleted foreclosed one of them.
//
// What it shares with a ban is the read side: the handle leaves the directory
// immediately, so every public surface treats the account as handle-less within
// one reload. Their rows leave the *projections* only when a rebuild runs, which
// is why every verb here reports whether one is needed.

// ShadowbanResult reports what a shadow ban withheld.
type ShadowbanResult struct {
	Sub    string `json:"sub"`
	IdP    string `json:"idp"`
	At     int64  `json:"at"`
	Reason string `json:"reason"`
	// Withheld is how many events this call moved out of the live log. A
	// repeat on an already-shadowbanned account reports zero and is otherwise a
	// no-op.
	Withheld int64 `json:"withheld"`
	// Handles are the handles that stopped resolving. They are **not** retired:
	// retirement is permanent and unclaimable-forever (D9), which is the wrong
	// shape for an action whose whole point is being reversible.
	Handles []string `json:"handles_hidden"`
}

// Shadowban withholds a player's log and hides them from every public surface,
// leaving their credential — and therefore their client — working.
//
// The handles are deliberately left un-retired. A ban retires them because a
// banned account is not coming back and the handle must never be claimable by
// an impersonator (D9); a shadow ban may be lifted tomorrow, and un-retiring is
// a second inverse to keep honest for no gain — the directory already makes the
// handle unresolvable, and it is still owned, so nobody else can claim it.
func (m *Moderator) Shadowban(ctx context.Context, p store.Player, reason string) (ShadowbanResult, error) {
	if reason == "" {
		reason = "shadowbanned"
	}
	at := m.now().UnixMilli()
	res := ShadowbanResult{Sub: p.UserKey.B64U(), IdP: p.IdP, At: at, Reason: reason}

	handles, err := m.events.HandlesForPlayer(ctx, p.ID)
	if err != nil {
		return res, err
	}
	if res.Withheld, err = m.events.ShadowbanPlayer(ctx, p.ID, at, reason); err != nil {
		return res, err
	}
	for _, h := range handles {
		res.Handles = append(res.Handles, h.Handle)
	}

	if err := m.refresh(ctx); err != nil {
		return res, err
	}
	m.log.Warn("player shadowbanned", "player", p.ID, "user_key", p.UserKey, "idp", p.IdP,
		"reason", reason, "withheld", res.Withheld, "handles", res.Handles)
	return res, nil
}

// UnshadowbanResult reports what a lifted shadow ban gave back.
type UnshadowbanResult struct {
	Sub string `json:"sub"`
	// Restored is how many events went back into the live log, at their
	// original seq. It is zero when the withheld events were deleted first,
	// which is honest rather than an error: there was nothing left to restore.
	Restored int64    `json:"restored"`
	Handles  []string `json:"handles_restored"`
}

// Unshadowban lifts a shadow ban: the withheld events go back into the log at
// the seq they left from, and the handles resolve again.
//
// The board rows those events earn do not come back until a rebuild runs. That
// is not a gap in this method — the projector's cursor only moves forward, so
// re-folding history is what a rebuild *is* (D22, PROJ-090).
func (m *Moderator) Unshadowban(ctx context.Context, p store.Player) (UnshadowbanResult, error) {
	res := UnshadowbanResult{Sub: p.UserKey.B64U()}

	handles, err := m.events.HandlesForPlayer(ctx, p.ID)
	if err != nil {
		return res, err
	}
	if res.Restored, err = m.events.UnshadowbanPlayer(ctx, p.ID); err != nil {
		return res, err
	}
	for _, h := range handles {
		res.Handles = append(res.Handles, h.Handle)
	}

	if err := m.refresh(ctx); err != nil {
		return res, err
	}
	m.log.Warn("shadow ban lifted", "player", p.ID, "user_key", p.UserKey,
		"restored", res.Restored, "handles", res.Handles)
	return res, nil
}

// DeleteWithheldResult reports the end of a review.
type DeleteWithheldResult struct {
	Sub     string `json:"sub"`
	Deleted int64  `json:"deleted"`
}

// DeleteWithheld destroys a shadowbanned player's withheld events permanently
// and leaves the shadow ban in place, so anything they ship next is withheld
// too.
//
// This is the one irreversible verb in the set, and it is deliberately not
// [Moderator.Purge]: purge deletes the *account* — every handle, credential and
// the player row, leaving a tombstone that keeps them out forever. "I have
// reviewed this data and it goes" and "this person is gone" are two different
// decisions and an operator should have to make them one at a time.
func (m *Moderator) DeleteWithheld(ctx context.Context, p store.Player) (DeleteWithheldResult, error) {
	res := DeleteWithheldResult{Sub: p.UserKey.B64U()}
	deleted, err := m.events.DeleteWithheldEvents(ctx, p.ID)
	if err != nil {
		return res, err
	}
	res.Deleted = deleted
	m.log.Warn("withheld events deleted", "player", p.ID, "user_key", p.UserKey, "deleted", deleted)
	return res, nil
}

// RevokeCredential revokes one credential in both halves — the row and the
// deny-list — and republishes. It is the dashboard's per-handle revoke and the
// reissue path's cleanup (§4.8).
func (m *Moderator) RevokeCredential(ctx context.Context, jkt string) error {
	if err := m.events.RevokeCredential(ctx, nil, jkt, m.now().UnixMilli()); err != nil {
		return err
	}
	if m.deny != nil {
		m.deny.AddJKT(jkt)
	}
	m.publish()
	return nil
}

// Refresh rebuilds the deny-list from the database, reloads the handle
// directory and republishes the signed deny-list. Exported so the
// `POST /admin/denylist/publish` route can force it, and so the handle-claim
// path can make a new handle visible.
func (m *Moderator) Refresh(ctx context.Context) error { return m.refresh(ctx) }

// refresh is the "both halves stay in step" step. Rebuilding the deny-list from
// the database rather than patching it is what makes a partially-applied
// mutation self-correcting — and what makes an unban actually restore ingest,
// since the un-revoked credentials simply stop appearing in the rebuild.
func (m *Moderator) refresh(ctx context.Context) error {
	if m.deny != nil {
		if err := m.deny.LoadFrom(ctx, m.events); err != nil {
			return err
		}
		m.mu.Lock()
		for jkt := range m.purged {
			m.deny.AddJKT(jkt)
		}
		m.mu.Unlock()
	}
	if m.dir != nil {
		if err := m.dir.Reload(ctx); err != nil {
			// A stale directory serves slightly old handles; it is not worth
			// failing a ban over, and the ban itself has already landed.
			m.log.Warn("handle directory reload failed after a moderation change", "err", err)
		}
	}
	m.publish()
	return nil
}

func (m *Moderator) publish() {
	if m.publisher == nil {
		return
	}
	if _, err := m.publisher.Publish(); err != nil {
		m.log.Warn("deny-list publication failed", "err", err)
	}
}
