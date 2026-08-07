package adminapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// newModerationServer builds an admin server with the WP3 routes mounted the
// way catlogd mounts them.
func newModerationServer(t *testing.T) (*Server, *httptest.Server, *store.Events, *authz.DenyList) {
	t.Helper()

	cfg := testutil.Config(t)
	events := testutil.Events(t)
	ks := testutil.KeysAt(t, cfg.Data.Dir)
	deny := authz.NewDenyList()
	dir := directory.New(events)
	if err := dir.Reload(t.Context()); err != nil {
		t.Fatalf("directory reload: %v", err)
	}

	s := New(Deps{
		Config:   cfg,
		Keys:     ks,
		Events:   events,
		Verifier: authz.New(authz.Config{Issuer: cfg.Server.BaseURL, RatePerSecond: 1, Burst: 5}, ks, events, deny),
		Log:      testutil.DiscardLogger(),
		Now:      func() time.Time { return time.Unix(1_770_000_000, 0).UTC() },
	})

	ident, err := identity.New(identity.Deps{
		Config: cfg, Keys: ks, Events: events, Deny: deny, Directory: dir,
		WriteLock: s.WithWriteLock, Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}
	s.RegisterIdentity(IdentityDeps{Moderator: ident.Moderator(), DenyList: ident.DenyListPublisher()})

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, events, deny
}

// TestAdminBanUnbanPurgeRoutes drives the §5.9 verbs through HTTP, which is the
// only way catlogctl ever reaches them.
func TestAdminBanUnbanPurgeRoutes(t *testing.T) {
	_, srv, events, deny := newModerationServer(t)

	// A dev-issued credential gives us a real player, handle and credential.
	res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "banme"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}
	jkt, _ := body["jkt"].(string)

	// --- ban ---
	res, body = post(t, srv, "/admin/ban", BanRequest{Handle: "banme", Reason: "testing"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ban = %d: %v", res.StatusCode, body)
	}
	sub, _ := body["sub"].(string)
	if sub == "" {
		t.Fatalf("ban response has no sub: %v", body)
	}
	if !deny.HasSub(sub) || !deny.HasJKT(jkt) {
		t.Error("the ban did not reach the in-memory deny-list")
	}
	if retired, _ := events.HandleRetired(t.Context(), "banme"); !retired {
		t.Error("the ban did not retire the handle")
	}

	// Banning by sub works too, and is idempotent.
	if res, body = post(t, srv, "/admin/ban", BanRequest{Sub: sub, Reason: "again"}); res.StatusCode != http.StatusOK {
		t.Fatalf("second ban = %d: %v", res.StatusCode, body)
	}

	// --- denylist publish ---
	res, body = post(t, srv, "/admin/denylist/publish", struct{}{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("denylist publish = %d: %v", res.StatusCode, body)
	}
	if body["banned_subs"] != float64(1) || body["revoked_jkts"] != float64(1) {
		t.Errorf("denylist response = %v, want one sub and one jkt", body)
	}
	if body["path"] != identity.DenyListPath {
		t.Errorf("denylist path = %v, want %s", body["path"], identity.DenyListPath)
	}

	// --- unban ---
	res, body = post(t, srv, "/admin/unban", BanRequest{Sub: sub})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unban = %d: %v", res.StatusCode, body)
	}
	if deny.HasSub(sub) {
		t.Error("the unban did not clear the deny-list")
	}
	if retired, _ := events.HandleRetired(t.Context(), "banme"); retired {
		t.Error("the unban did not un-retire the handle")
	}

	// --- purge ---
	res, body = post(t, srv, "/admin/purge", BanRequest{Sub: sub, Reason: "gdpr"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("purge = %d: %v", res.StatusCode, body)
	}
	if !deny.HasSub(sub) {
		t.Error("the purge's tombstone did not reach the deny-list")
	}
	if _, err := events.HandleByLC(t.Context(), "banme"); err != store.ErrNotFound {
		t.Errorf("the handle row survived the purge: %v", err)
	}
	if retired, _ := events.HandleRetired(t.Context(), "banme"); !retired {
		t.Error("the purge did not retire the handle")
	}

	// A purged account is gone, so a second purge is a 404 rather than a 500.
	res, body = post(t, srv, "/admin/purge", BanRequest{Sub: sub})
	if res.StatusCode != http.StatusNotFound || body["error"] != authz.CodeNotFound {
		t.Errorf("purging a purged account = %d %v, want 404 not_found", res.StatusCode, body)
	}
}

// TestAdminModerationRejectsBadTargets covers the §4.9 mapping.
func TestAdminModerationRejectsBadTargets(t *testing.T) {
	_, srv, _, _ := newModerationServer(t)

	for _, path := range []string{"/admin/ban", "/admin/unban", "/admin/purge"} {
		t.Run(path, func(t *testing.T) {
			res, body := post(t, srv, path, BanRequest{})
			if res.StatusCode != http.StatusBadRequest || body["error"] != authz.CodeBadRequest {
				t.Errorf("no target = %d %v, want 400 bad_request", res.StatusCode, body)
			}
			res, body = post(t, srv, path, BanRequest{Handle: "nobody"})
			if res.StatusCode != http.StatusNotFound || body["error"] != authz.CodeNotFound {
				t.Errorf("unknown handle = %d %v, want 404 not_found", res.StatusCode, body)
			}
			res, body = post(t, srv, path, BanRequest{Sub: "not-a-user-key"})
			if res.StatusCode != http.StatusNotFound {
				t.Errorf("garbage sub = %d %v, want 404", res.StatusCode, body)
			}
		})
	}
}

// TestAdminBanWithPurge is §5.9's `--purge`.
func TestAdminBanWithPurge(t *testing.T) {
	_, srv, events, deny := newModerationServer(t)

	if res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "goneforever"}); res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}
	res, body := post(t, srv, "/admin/ban", BanRequest{Handle: "goneforever", Reason: "abuse", Purge: true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ban --purge = %d: %v", res.StatusCode, body)
	}
	purge, ok := body["purge"].(map[string]any)
	if !ok {
		t.Fatalf("no purge report in the ban response: %v", body)
	}
	if purge["archive_deleted"] != false {
		t.Errorf("archive_deleted = %v, want false with no archive store wired (WP10)", purge["archive_deleted"])
	}
	sub, _ := body["sub"].(string)
	if !deny.HasSub(sub) {
		t.Error("the purged sub is not on the deny-list")
	}
	if _, err := events.HandleByLC(t.Context(), "goneforever"); err != store.ErrNotFound {
		t.Errorf("the handle row survived: %v", err)
	}
}

// TestAdminBackup is §5.9's backup verb: the writer is quiesced and both files
// land in the destination.
func TestAdminBackup(t *testing.T) {
	_, srv, _, _ := newModerationServer(t)

	if res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "backupcat"}); res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}

	dest := t.TempDir()
	res, body := post(t, srv, "/admin/backup", BackupRequest{Dest: dest})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("backup = %d: %v", res.StatusCode, body)
	}
	files, _ := body["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("backup copied nothing: %v", body)
	}

	fi, err := os.Stat(filepath.Join(dest, "events.db"))
	if err != nil {
		t.Fatalf("events.db was not copied: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("the copied events.db is empty")
	}
	// The copy is a real database: reopening it must find the handle.
	copied := testutil.EventsAt(t, filepath.Join(dest, "events.db"))
	if _, err := copied.HandleByLC(t.Context(), "backupcat"); err != nil {
		t.Errorf("the backup does not contain the handle: %v", err)
	}

	if res, body := post(t, srv, "/admin/backup", BackupRequest{}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("backup with no dest = %d %v, want 400", res.StatusCode, body)
	}
}

// TestIssueMakesTheHandleResolvableImmediately is the regression test for the
// bug WP7's simulator found: `catlogctl issue` against a *running* catlogd
// created a handle the read path had never heard of, so every board row for
// that player was silently invisible until a restart or a `/admin/seed`.
//
// The directory is loaded once at start (§5.4), so any handle created at
// runtime needs an explicit reload — from this route as much as from the
// dashboard's.
func TestIssueMakesTheHandleResolvableImmediately(t *testing.T) {
	cfg := testutil.Config(t)
	events := testutil.Events(t)
	ks := testutil.KeysAt(t, cfg.Data.Dir)
	dir := directory.New(events)
	if err := dir.Reload(t.Context()); err != nil {
		t.Fatalf("directory reload: %v", err)
	}

	s := New(Deps{
		Config: cfg, Keys: ks, Events: events,
		Log: testutil.DiscardLogger(),
		Now: func() time.Time { return time.Unix(1_770_000_000, 0).UTC() },
	})
	// Only the directory: the routes exercised here touch nothing else in
	// ProjectionDeps, and a nil projector must not be required to make a
	// handle visible.
	s.RegisterProjections(ProjectionDeps{Directory: dir})

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	if _, ok := dir.Lookup("simcat"); ok {
		t.Fatal("the handle exists before it was issued")
	}
	res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "SimCat"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}

	// No restart, no seed, no rebuild: the read path must resolve it now.
	entry, ok := dir.Lookup("simcat")
	if !ok {
		t.Fatal("the issued handle is not in the directory; every board row for this player would be invisible")
	}
	if entry.Handle != "SimCat" {
		t.Errorf("directory handle = %q, want the claimed casing %q", entry.Handle, "SimCat")
	}
	// And player_id → handle, which is the direction the leaderboard filter
	// and the feed use.
	if got, ok := dir.Handle(entry.PlayerID); !ok || got != "SimCat" {
		t.Errorf("directory.Handle(%d) = %q/%v, want SimCat", entry.PlayerID, got, ok)
	}
}

// TestModerationRoutesAreLoopbackOnly re-checks the WP2 guard still covers the
// routes WP3 added — these delete player data.
func TestModerationRoutesAreLoopbackOnly(t *testing.T) {
	s, _, _, _ := newModerationServer(t)

	for _, path := range []string{"/admin/ban", "/admin/unban", "/admin/purge", "/admin/purge", "/admin/backup"} {
		req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
		req.RemoteAddr = "203.0.113.7:1234"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s from a remote peer = %d, want 403", path, rec.Code)
		}
	}
}
