package adminapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/store"
)

// ArchiveDeps is what the WP10 admin routes need. Like [ProjectionDeps] and
// [IdentityDeps] it is its own struct with its own entry point, so [New], the
// loopback guard and the §5.4 write mutex stay exactly as WP2 left them.
type ArchiveDeps struct {
	// Archiver copies the event log to the archive store and deletes a purged
	// player's prefix. Required.
	Archiver *archive.Archiver
}

// RegisterArchive mounts the §5.9/§5.10 archive routes:
//
//	POST /admin/archive/run       copy the log past the cursor into the archive
//	POST /admin/archive/restore   replay an archive into this server's events.db
//
// Restore is not in §5.9's table; §12 WP10 asks for it ("restore tool …
// replays chunks into a fresh events.db via admin — enables the DR story"). It
// has to be an admin route for the same reason every other stateful verb is:
// tursogo's exclusive whole-file lock means `catlogctl` can never open the
// database itself (§5.4).
func (s *Server) RegisterArchive(deps ArchiveDeps) {
	s.archive = deps
	s.mux.HandleFunc("POST /admin/archive/run", s.handleArchiveRun)
	s.mux.HandleFunc("POST /admin/archive/restore", s.handleArchiveRestore)
}

// archiveTimeout bounds an archive pass or a restore. Both are bulk passes over
// the event log; both hold the write mutex, and neither should inherit a
// client's patience.
const archiveTimeout = 30 * time.Minute

// --- POST /admin/archive/run ---------------------------------------------------

func (s *Server) handleArchiveRun(w http.ResponseWriter, r *http.Request) {
	a := s.archive.Archiver
	if a == nil {
		fail(w, authz.CodeInternal, "no archive is configured on this server")
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), archiveTimeout)
	defer cancel()

	var res archive.RunResult
	// The write mutex is what keeps a run from interleaving with the ingest
	// writer: the run reads the log and then advances the cursor, and a cursor
	// advanced past events that were still being committed would archive a hole.
	err := s.WithWriteLock(func() error {
		var err error
		res, err = a.Run(ctx)
		return err
	})
	if err != nil {
		s.deps.Log.Error("admin archive run failed", "err", err)
		fail(w, authz.CodeInternal, "could not run the archive pass")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- POST /admin/archive/restore -----------------------------------------------

// RestoreRequest is `POST /admin/archive/restore` (§12 WP10).
type RestoreRequest struct {
	// Dir is an archive root on the *server's* filesystem — the directory
	// holding `players/`. It is a server-side path for the same reason
	// `POST /admin/backup`'s dest is: catlogctl is an HTTP client, not a
	// process that can touch this machine's disk.
	Dir string `json:"dir"`
}

func (s *Server) handleArchiveRestore(w http.ResponseWriter, r *http.Request) {
	var req RestoreRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Dir == "" {
		fail(w, authz.CodeBadRequest, "dir is required")
		return
	}
	if s.deps.Events == nil {
		fail(w, authz.CodeInternal, "no database is open on this server")
		return
	}

	dir, err := filepath.Abs(req.Dir)
	if err != nil {
		fail(w, authz.CodeBadRequest, "dir is not a usable path")
		return
	}
	src, err := archive.OpenFSStore(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, authz.CodeNotFound, "there is no archive at "+dir)
			return
		}
		s.deps.Log.Error("admin archive restore failed", "dir", dir, "err", err)
		fail(w, authz.CodeBadRequest, "could not open the archive at "+dir)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), archiveTimeout)
	defer cancel()

	var res archive.RestoreResult
	err = s.WithWriteLock(func() error {
		var err error
		res, err = archive.Restore(ctx, s.deps.Events, src, s.deps.Log)
		return err
	})
	if err != nil {
		s.deps.Log.Error("admin archive restore failed", "dir", dir, "err", err)
		switch {
		case errors.Is(err, store.ErrSeqConflict), errors.Is(err, store.ErrPlayerConflict):
			// Almost always the same mistake: restoring into a database that is
			// not empty. Say so, rather than making an operator read a log.
			fail(w, authz.CodeBadRequest,
				"the target database already holds conflicting data — restore into a fresh data directory: "+err.Error())
		default:
			fail(w, authz.CodeInternal, "could not restore the archive")
		}
		return
	}

	s.deps.Log.Info("archive restored via admin API",
		"dir", dir, "players", len(res.Players), "events", res.Events, "inserted", res.Inserted)
	writeJSON(w, http.StatusOK, res)
}
