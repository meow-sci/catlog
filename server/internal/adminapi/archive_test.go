package adminapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// TestArchiveRoutesAreNotConfiguredUntilRegistered: every Register… entry point
// is optional, and a server without one must say so rather than panic.
func TestArchiveRoutesAreNotConfiguredUntilRegistered(t *testing.T) {
	s, srv := newServer(t)
	s.RegisterArchive(ArchiveDeps{}) // routes mounted, no archiver behind them

	res, body := post(t, srv, "/admin/archive/run", struct{}{})
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, body %v", res.StatusCode, body)
	}
	if got, _ := body["error"].(string); got != "internal" {
		t.Errorf("error = %q", got)
	}
}

// TestArchiveRunAndRestoreOverTheAdminAPI exercises both routes the way
// `catlogctl` does: run a pass against a populated log, then restore that
// archive into a second, empty server.
func TestArchiveRunAndRestoreOverTheAdminAPI(t *testing.T) {
	// --- server one: archive ---
	s, srv := newServer(t)
	events := s.deps.Events
	playerID := testutil.Player(t, events, s.deps.Keys, "dev", "ace")
	insertEvents(t, events, playerID, 6)

	root := filepath.Join(t.TempDir(), "archive")
	st, err := archive.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := archive.New(archive.Options{Events: events, Store: st, Log: testutil.DiscardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterArchive(ArchiveDeps{Archiver: a})

	res, body := post(t, srv, "/admin/archive/run", struct{}{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("run: status = %d, body %v", res.StatusCode, body)
	}
	if got := body["events"]; got != float64(6) {
		t.Errorf("archived %v events, want 6", got)
	}
	if got := body["to_seq"]; got != float64(6) {
		t.Errorf("cursor left at %v, want 6", got)
	}

	// A second run is a no-op, which is what makes the nightly timer safe.
	if _, body := post(t, srv, "/admin/archive/run", struct{}{}); body["events"] != float64(0) {
		t.Errorf("a repeat run archived %v events", body["events"])
	}

	// --- server two: restore ---
	s2, srv2 := newServer(t)
	a2, err := archive.New(archive.Options{
		Events: s2.deps.Events,
		Store:  mustStore(t, filepath.Join(t.TempDir(), "archive2")),
		Log:    testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s2.RegisterArchive(ArchiveDeps{Archiver: a2})

	res, body = post(t, srv2, "/admin/archive/restore", RestoreRequest{Dir: root})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("restore: status = %d, body %v", res.StatusCode, body)
	}
	if body["inserted"] != float64(6) || body["last_seq"] != float64(6) {
		t.Errorf("restore = %v", body)
	}
	n, err := s2.deps.Events.CountEvents(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Errorf("the restored server holds %d events, want 6", n)
	}
}

// TestRestoreRejectsABadDirectory: the two mistakes an operator actually makes
// are a typo'd path and a second restore into a live server.
func TestRestoreRejectsABadDirectory(t *testing.T) {
	s, srv := newServer(t)
	s.RegisterArchive(ArchiveDeps{Archiver: mustArchiver(t, s.deps.Events)})

	if res, body := post(t, srv, "/admin/archive/restore", RestoreRequest{}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("empty dir = %d %v", res.StatusCode, body)
	}
	missing := filepath.Join(t.TempDir(), "typo")
	res, body := post(t, srv, "/admin/archive/restore", RestoreRequest{Dir: missing})
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("missing dir = %d %v", res.StatusCode, body)
	}
}

func mustStore(t *testing.T, dir string) archive.Store {
	t.Helper()
	s, err := archive.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustArchiver(t *testing.T, events *store.Events) *archive.Archiver {
	t.Helper()
	a, err := archive.New(archive.Options{
		Events: events, Store: mustStore(t, filepath.Join(t.TempDir(), "archive")),
		Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func insertEvents(t *testing.T, e *store.Events, playerID int64, n int) {
	t.Helper()
	session := testutil.ULID(t)
	evs := make([]store.Event, 0, n)
	for range n {
		evs = append(evs, store.Event{
			ID:        testutil.ULID(t),
			SessionID: session,
			Type:      "vehicle.staging",
			Ver:       1,
			WallTime:  1_700_000_000_000,
			Payload:   json.RawMessage(`{"stage_index":1}`),
		})
	}
	if _, _, err := e.InsertEvents(t.Context(), nil, playerID, evs); err != nil {
		t.Fatalf("insert events: %v", err)
	}
}
