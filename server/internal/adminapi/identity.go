package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/store"
)

// IdentityDeps is what the WP3 admin routes need. Like [ProjectionDeps] it is
// its own struct with its own entry point, so the constructor, the loopback
// guard and the §5.4 write mutex stay exactly as WP2 left them.
type IdentityDeps struct {
	// Moderator applies bans, unbans and purges to both the database and the
	// in-memory deny-list. Required.
	Moderator *identity.Moderator
	// DenyList publishes the signed §5.8 document. Required.
	DenyList *identity.DenyListPublisher
}

// RegisterIdentity mounts the §5.9 identity routes:
//
//	POST /admin/ban               ban, revoke, retire, refresh the deny-list
//	POST /admin/unban             lift a ban
//	POST /admin/purge             delete everything, keep the tombstone
//	POST /admin/denylist/publish  regenerate the signed deny-list
//	POST /admin/backup            quiesce the writer and copy events.db
func (s *Server) RegisterIdentity(deps IdentityDeps) {
	s.identity = deps
	s.mux.HandleFunc("POST /admin/ban", s.handleBan)
	s.mux.HandleFunc("POST /admin/unban", s.handleUnban)
	s.mux.HandleFunc("POST /admin/purge", s.handlePurge)
	s.mux.HandleFunc("POST /admin/denylist/publish", s.handleDenyListPublish)
	s.mux.HandleFunc("POST /admin/backup", s.handleBackup)
}

// reloadDirectory refreshes the in-memory player_id ↔ handle map (§5.4) after an
// admin write that created, retired or hid a handle.
//
// It is not optional bookkeeping. projections.db cannot be joined to events.db,
// so the read API resolves every player_id through this map — and the map is
// loaded once at start. A handle created while catlogd is running is therefore
// invisible to `GET /v1/players/{handle}` and dropped by the leaderboard filter
// as "holding no handle yet" until something reloads it. The events fold
// correctly the whole time, which is what makes the symptom so confusing.
//
// A failed reload is logged, not fatal: the write it follows has already
// landed, and the next reload will pick it up.
func (s *Server) reloadDirectory(ctx context.Context) {
	d := s.projections.Directory
	if d == nil {
		return
	}
	if err := d.Reload(ctx); err != nil {
		s.deps.Log.Warn("handle directory reload failed", "err", err)
	}
}

// --- POST /admin/ban ------------------------------------------------------------

// BanRequest is `POST /admin/ban` (§5.9).
type BanRequest struct {
	// Handle or Sub names the account; exactly one is needed. Sub is the
	// b64u user_key a license carries, which is what an ingest rejection log
	// gives an operator to work from.
	Handle string `json:"handle,omitempty"`
	Sub    string `json:"sub,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Purge also deletes every row the account owns (§5.9 `--purge`).
	Purge bool `json:"purge,omitempty"`
}

func (s *Server) handleBan(w http.ResponseWriter, r *http.Request) {
	var req BanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.BanResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), identity.Target{Handle: req.Handle, Sub: req.Sub})
		if err != nil {
			return err
		}
		if res, err = mod.Ban(r.Context(), player, req.Reason); err != nil {
			return err
		}
		if req.Purge {
			// Re-read: the ban has changed the row the purge reports on.
			player, err = mod.Resolve(r.Context(), identity.Target{Sub: res.Sub})
			if err != nil {
				return err
			}
			purge, err := mod.Purge(r.Context(), player, req.Reason)
			if err != nil {
				return err
			}
			res.Purge = &purge
		}
		return nil
	})
	if s.moderationFailed(w, err, "ban") {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- POST /admin/unban ------------------------------------------------------------

func (s *Server) handleUnban(w http.ResponseWriter, r *http.Request) {
	var req BanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.UnbanResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), identity.Target{Handle: req.Handle, Sub: req.Sub})
		if err != nil {
			return err
		}
		res, err = mod.Unban(r.Context(), player)
		return err
	})
	if s.moderationFailed(w, err, "unban") {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- POST /admin/purge ------------------------------------------------------------

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	var req BanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mod := s.identity.Moderator
	if mod == nil {
		fail(w, authz.CodeInternal, "moderation is not configured on this server")
		return
	}

	var res identity.PurgeResult
	err := s.WithWriteLock(func() error {
		player, err := mod.Resolve(r.Context(), identity.Target{Handle: req.Handle, Sub: req.Sub})
		if err != nil {
			return err
		}
		res, err = mod.Purge(r.Context(), player, req.Reason)
		return err
	})
	if s.moderationFailed(w, err, "purge") {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// moderationFailed maps a moderation error onto the §4.9 registry, reporting
// whether it wrote a response.
func (s *Server) moderationFailed(w http.ResponseWriter, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		fail(w, authz.CodeNotFound, "no such account")
	case errors.Is(err, identity.ErrTargetRequired):
		fail(w, authz.CodeBadRequest, "supply a handle or a sub")
	default:
		s.deps.Log.Error("admin "+what+" failed", "err", err)
		fail(w, authz.CodeInternal, "could not "+what+" the account")
	}
	return true
}

// --- POST /admin/denylist/publish ---------------------------------------------------

// DenyListResponse is `POST /admin/denylist/publish` (§5.9).
type DenyListResponse struct {
	Ver         int64    `json:"ver"`
	UpdatedAt   int64    `json:"updated_at"`
	BannedSubs  int      `json:"banned_subs"`
	RevokedJKTs int      `json:"revoked_jkts"`
	Path        string   `json:"path"`
	Subs        []string `json:"subs,omitempty"`
}

func (s *Server) handleDenyListPublish(w http.ResponseWriter, r *http.Request) {
	mod, pub := s.identity.Moderator, s.identity.DenyList
	if mod == nil || pub == nil {
		fail(w, authz.CodeInternal, "the deny-list is not configured on this server")
		return
	}

	// Refresh rebuilds the set from the database first, so `denylist` doubles
	// as the repair verb when an operator suspects the two halves have drifted.
	err := s.WithWriteLock(func() error { return mod.Refresh(r.Context()) })
	if err != nil {
		s.deps.Log.Error("admin denylist publish failed", "err", err)
		fail(w, authz.CodeInternal, "could not regenerate the deny-list")
		return
	}

	ver, at := pub.Version()
	var subs, jkts []string
	if s.deps.Verifier != nil {
		subs, jkts, _ = s.deps.Verifier.DenyList().Snapshot()
	}
	writeJSON(w, http.StatusOK, DenyListResponse{
		Ver: ver, UpdatedAt: at,
		BannedSubs: len(subs), RevokedJKTs: len(jkts),
		Path: identity.DenyListPath, Subs: subs,
	})
}

// --- POST /admin/backup ---------------------------------------------------------

// BackupRequest is `POST /admin/backup` (§5.9).
type BackupRequest struct {
	// Dest is a directory on the server's filesystem. It is created if absent.
	Dest string `json:"dest"`
}

// BackupResponse reports a completed backup.
type BackupResponse struct {
	Dest  string       `json:"dest"`
	Files []BackupFile `json:"files"`
	// DurationMS is how long the writer was quiesced.
	DurationMS int64 `json:"duration_ms"`
}

// BackupFile is one copied file.
type BackupFile struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// handleBackup implements `POST /admin/backup` (§5.9): quiesce the writer, copy
// `events.db` and its `-wal` sidecar to dest, resume.
//
// # What "quiesce" means here
//
// The admin mutex alone is not enough: it excludes other admin writes, not the
// ingest writer goroutine. What excludes that is the database's single writer
// connection (§5.4) — so the copy happens inside a write transaction, which
// owns that connection for its duration and parks the ingest writer on
// `BeginTx` until the files are on disk.
//
// The WAL is copied as well as the main file, and deliberately so: Turso's WAL
// never auto-checkpoints (see docs/DECISIONS.md, WP1), so `events.db` alone can
// be nearly empty. A checkpoint runs first to keep the copy small; anything
// committed between it and the transaction is caught by the WAL copy.
//
// projections.db is not backed up. It is derived and rebuildable from the event
// log by design (D8, §5.6) — backing it up would be backing up a cache.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req BackupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Dest == "" {
		fail(w, authz.CodeBadRequest, "dest is required")
		return
	}
	if s.deps.Events == nil {
		fail(w, authz.CodeInternal, "no database is open on this server")
		return
	}

	dest, err := filepath.Abs(req.Dest)
	if err != nil {
		fail(w, authz.CodeBadRequest, "dest is not a usable path")
		return
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		s.deps.Log.Error("admin backup failed", "dest", dest, "err", err)
		fail(w, authz.CodeInternal, "could not create the destination directory")
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backupTimeout)
	defer cancel()

	res := BackupResponse{Dest: dest}
	start := time.Now()
	err = s.WithWriteLock(func() error {
		// Fold the WAL back into the main file so the copy is the small one.
		// A busy checkpoint is not fatal — the WAL copy below covers it.
		if err := s.deps.Events.Checkpoint(ctx); err != nil {
			s.deps.Log.Warn("backup: pre-copy checkpoint failed", "err", err)
		}
		return s.deps.Events.WithWriteTx(ctx, func(*sql.Tx) error {
			src := s.deps.Events.Path()
			for _, name := range []string{src, src + "-wal"} {
				n, err := copyIfExists(name, filepath.Join(dest, filepath.Base(name)))
				if err != nil {
					return err
				}
				if n >= 0 {
					res.Files = append(res.Files, BackupFile{Name: filepath.Base(name), Bytes: n})
				}
			}
			return nil
		})
	})
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		s.deps.Log.Error("admin backup failed", "dest", dest, "err", err)
		fail(w, authz.CodeInternal, "could not copy the database")
		return
	}

	s.deps.Log.Info("backup written", "dest", dest, "files", len(res.Files), "duration_ms", res.DurationMS)
	writeJSON(w, http.StatusOK, res)
}

// backupTimeout bounds a backup. A copy of a multi-gigabyte event log is slow;
// an unbounded one holds the writer forever.
const backupTimeout = 30 * time.Minute

// copyIfExists copies src to dst, returning -1 when src does not exist (there
// is no `-wal` after a clean checkpoint, which is the good case).
func copyIfExists(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("adminapi: open %s: %w", src, err)
	}
	defer in.Close()

	// 0600: an event log is player data.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("adminapi: create %s: %w", dst, err)
	}
	n, err := io.Copy(out, in)
	if err != nil {
		out.Close()
		return 0, fmt.Errorf("adminapi: copy %s: %w", src, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return 0, fmt.Errorf("adminapi: sync %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("adminapi: close %s: %w", dst, err)
	}
	return n, nil
}
