package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/store"
)

// claim is the fixture shorthand for "this account holds this handle with a
// live credential".
func (f *fixture) claim(cookie *http.Cookie, handle string) (jkt string) {
	f.t.Helper()
	jwk, jkt, _ := publicJWK(f.t)
	rec := f.do(http.MethodPost, "/api/handles", cookie, ClaimRequest{Handle: handle, JWK: jwk})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("claim %q = %d (%s)", handle, rec.Code, rec.Body)
	}
	return jkt
}

func (f *fixture) player(handle string) store.Player {
	f.t.Helper()
	h, err := f.events.HandleByLC(context.Background(), handle)
	if err != nil {
		f.t.Fatalf("handle %q: %v", handle, err)
	}
	p, err := f.events.PlayerByID(context.Background(), h.PlayerID)
	if err != nil {
		f.t.Fatalf("player behind %q: %v", handle, err)
	}
	return p
}

// TestBanReachesBothHalves is the §5.8 invariant: a ban must land in the
// database *and* in the in-memory deny-list, or it only takes effect at
// §4.5.3 step 5 instead of step 4.
func TestBanReachesBothHalves(t *testing.T) {
	f := newFixture(t)
	cookie, uk, playerID := f.login(IdPDiscord, "100000000000000000")
	jkt := f.claim(cookie, "whiskers")

	res, err := f.server.Moderator().Ban(t.Context(), f.player("whiskers"), "cheating")
	if err != nil {
		t.Fatalf("ban: %v", err)
	}
	if res.Sub != uk.B64U() || len(res.Handles) != 1 || len(res.Credentials) != 1 {
		t.Errorf("ban result = %+v", res)
	}

	// Half one: the database.
	p, err := f.events.PlayerByID(t.Context(), playerID)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	if !p.Banned() {
		t.Error("banned_at is not set")
	}
	cred, err := f.events.CredentialByJKT(t.Context(), jkt)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if !cred.Revoked() {
		t.Error("the credential is not revoked in the database")
	}
	if retired, _ := f.events.HandleRetired(t.Context(), "whiskers"); !retired {
		t.Error("the handle is not retired")
	}

	// Half two: the deny-list, which is what step 4 reads.
	if !f.deny.HasSub(uk.B64U()) {
		t.Error("the banned sub is not on the in-memory deny-list")
	}
	if !f.deny.HasJKT(jkt) {
		t.Error("the revoked jkt is not on the in-memory deny-list")
	}

	// And the read side: a banned player has no handle at all (§4.8 404s them).
	if _, ok := f.dir.Lookup("whiskers"); ok {
		t.Error("a banned player's handle is still resolvable on the read side")
	}

	// The handle is unclaimable by anyone else while the ban stands (D9).
	other, _, _ := f.login(IdPGitHub, "4242")
	jwk, _, _ := publicJWK(t)
	rec := f.do(http.MethodPost, "/api/handles", other, ClaimRequest{Handle: "WHISKERS", JWK: jwk})
	if got := errorCode(t, rec); got != authz.CodeHandleTaken {
		t.Errorf("a banned account's handle was claimable: %q", got)
	}

	// And the banned player cannot use the dashboard.
	if rec := f.do(http.MethodGet, "/api/me", cookie, nil); errorCode(t, rec) != authz.CodeBanned {
		t.Errorf("a banned session reached /api/me: %d %s", rec.Code, rec.Body)
	}
}

// TestUnbanRestores is the other half of the §12 WP3 integration case: unban
// puts back exactly what the ban took.
func TestUnbanRestores(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	jkt := f.claim(cookie, "whiskers")

	if _, err := f.server.Moderator().Ban(t.Context(), f.player("whiskers"), "mistake"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	p, err := f.events.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	res, err := f.server.Moderator().Unban(t.Context(), p)
	if err != nil {
		t.Fatalf("unban: %v", err)
	}
	if res.Credentials != 1 || len(res.Handles) != 1 {
		t.Errorf("unban result = %+v, want one handle and one credential restored", res)
	}

	p, err = f.events.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	if p.Banned() {
		t.Error("banned_at survived the unban")
	}
	cred, err := f.events.CredentialByJKT(t.Context(), jkt)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.Revoked() {
		t.Error("the credential is still revoked after an unban")
	}
	if retired, _ := f.events.HandleRetired(t.Context(), "whiskers"); retired {
		t.Error("the handle is still retired after an unban")
	}
	if f.deny.HasSub(uk.B64U()) {
		t.Error("the sub is still on the deny-list after an unban")
	}
	if f.deny.HasJKT(jkt) {
		t.Error("the jkt is still on the deny-list after an unban — ingest would keep failing")
	}
	if _, ok := f.dir.Lookup("whiskers"); !ok {
		t.Error("the handle did not come back to the read side")
	}
	if rec := f.do(http.MethodGet, "/api/me", cookie, nil); rec.Code != http.StatusOK {
		t.Errorf("the unbanned player cannot use the dashboard: %d %s", rec.Code, rec.Body)
	}
}

// TestUnbanDoesNotResurrectSelfRevokedCredentials is why the ban timestamp is
// the selector: an unban is an inverse, not an amnesty.
func TestUnbanDoesNotResurrectSelfRevokedCredentials(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	lost := f.claim(cookie, "whiskers")

	// The player revokes their own credential (they lost the file), then
	// issues a new one, then gets banned.
	if rec := f.do(http.MethodPost, "/api/handles/whiskers/revoke", cookie, nil); rec.Code != 200 {
		t.Fatalf("revoke = %d (%s)", rec.Code, rec.Body)
	}
	jwk, live, _ := publicJWK(t)
	if rec := f.do(http.MethodPost, "/api/handles/whiskers/reissue", cookie, ReissueRequest{JWK: jwk}); rec.Code != 200 {
		t.Fatalf("reissue = %d (%s)", rec.Code, rec.Body)
	}

	if _, err := f.server.Moderator().Ban(t.Context(), f.player("whiskers"), "temporary"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	p, err := f.events.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	if _, err := f.server.Moderator().Unban(t.Context(), p); err != nil {
		t.Fatalf("unban: %v", err)
	}

	if c, _ := f.events.CredentialByJKT(t.Context(), lost); !c.Revoked() {
		t.Error("the self-revoked credential came back to life")
	}
	if !f.deny.HasJKT(lost) {
		t.Error("the self-revoked jkt left the deny-list")
	}
	if c, _ := f.events.CredentialByJKT(t.Context(), live); c.Revoked() {
		t.Error("the credential the ban revoked was not restored")
	}
}

// fakeArchive records the purge seam being used.
type fakeArchive struct {
	deleted []string
	err     error
}

func (a *fakeArchive) DeletePlayerArchive(_ context.Context, sub string) error {
	a.deleted = append(a.deleted, sub)
	return a.err
}

// TestPurgeCallsTheArchiveSeam pins the WP10 contract: a purge deletes the
// archive prefix when an archive store is wired, and reports honestly when
// there is none.
func TestPurgeCallsTheArchiveSeam(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	f.claim(cookie, "whiskers")

	archive := &fakeArchive{}
	f.server.Moderator().SetArchive(archive)

	res, err := f.server.Moderator().Purge(t.Context(), f.player("whiskers"), "gdpr")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if !res.Archived {
		t.Error("the purge did not report deleting the archive prefix")
	}
	if len(archive.deleted) != 1 || archive.deleted[0] != uk.B64U() {
		t.Errorf("archive deletions = %v, want [%s]", archive.deleted, uk.B64U())
	}

	// A failing archive fails the purge: leaving a deleted account's raw event
	// log in object storage is exactly the thing a purge is for.
	f2 := newFixture(t)
	cookie2, _, _ := f2.login(IdPDiscord, "100000000000000000")
	f2.claim(cookie2, "whiskers")
	f2.server.Moderator().SetArchive(&fakeArchive{err: errors.New("r2 is down")})
	if _, err := f2.server.Moderator().Purge(t.Context(), f2.player("whiskers"), "gdpr"); err == nil {
		t.Error("a purge succeeded despite the archive deletion failing")
	}
}

// TestPurgedAccountCannotComeBack is the tombstone's whole job.
func TestPurgedAccountCannotComeBack(t *testing.T) {
	f := newFixture(t)
	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	f.claim(cookie, "whiskers")

	if _, err := f.server.Moderator().Purge(t.Context(), f.player("whiskers"), "deleted"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Logging in again derives the same user_key, and the deny-list refuses it
	// before a row is touched — which is the only coherent behaviour, because a
	// new session would produce licenses that ingest rejects as `banned`.
	if !f.deny.HasSub(uk.B64U()) {
		t.Fatal("the tombstone did not reach the deny-list")
	}

	// A restart rebuilds the deny-list from the tombstone alone.
	fresh := authz.NewDenyList()
	if err := fresh.LoadFrom(t.Context(), f.events); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !fresh.HasSub(uk.B64U()) {
		t.Error("a restart would forget that this account was purged")
	}

	// And there is nothing left to unban.
	if _, err := f.server.Moderator().Resolve(t.Context(), Target{Sub: uk.B64U()}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Resolve after a purge: %v, want ErrNotFound", err)
	}
}

// TestResolveTargets covers the handle/sub selector §5.9 gives operators.
func TestResolveTargets(t *testing.T) {
	f := newFixture(t)
	cookie, uk, playerID := f.login(IdPDiscord, "100000000000000000")
	f.claim(cookie, "Whiskers")

	mod := f.server.Moderator()
	for _, tc := range []struct {
		name   string
		target Target
	}{
		{"by handle", Target{Handle: "Whiskers"}},
		{"by handle, other casing", Target{Handle: "wHiSkErS"}},
		{"by sub", Target{Sub: uk.B64U()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := mod.Resolve(t.Context(), tc.target)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if p.ID != playerID {
				t.Errorf("resolved player %d, want %d", p.ID, playerID)
			}
		})
	}

	if _, err := mod.Resolve(t.Context(), Target{}); !errors.Is(err, ErrTargetRequired) {
		t.Errorf("empty target: %v, want ErrTargetRequired", err)
	}
	for _, bad := range []Target{{Handle: "nobody"}, {Sub: "not-base64url!"}, {Sub: "AAAA"}} {
		if _, err := mod.Resolve(t.Context(), bad); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Resolve(%+v) = %v, want ErrNotFound", bad, err)
		}
	}
}

// TestDenyListDocumentIsSignedAndVersioned checks the §5.8 payload shape.
func TestDenyListDocumentIsSignedAndVersioned(t *testing.T) {
	f := newFixture(t)
	pub := f.server.DenyListPublisher()

	first, err := pub.Publish()
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	ver, at := pub.Version()
	if at == 0 {
		t.Error("the published document has no updated_at")
	}

	cookie, uk, _ := f.login(IdPDiscord, "100000000000000000")
	jkt := f.claim(cookie, "whiskers")
	if _, err := f.server.Moderator().Ban(t.Context(), f.player("whiskers"), "x"); err != nil {
		t.Fatalf("ban: %v", err)
	}

	second, err := pub.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if second == first {
		t.Fatal("the document did not change after a ban")
	}
	newVer, _ := pub.Version()
	if newVer <= ver {
		t.Errorf("ver = %d, want > %d", newVer, ver)
	}

	key, _ := f.keys.SigningKeyByKID(f.keys.Signing.KID)
	payload, err := cjws.VerifyES256(second, key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	var doc DenyListDocument
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(doc.BannedSubs) != 1 || doc.BannedSubs[0] != uk.B64U() {
		t.Errorf("banned_subs = %v", doc.BannedSubs)
	}
	if len(doc.RevokedJKTs) != 1 || doc.RevokedJKTs[0] != jkt {
		t.Errorf("revoked_jkts = %v", doc.RevokedJKTs)
	}
}
