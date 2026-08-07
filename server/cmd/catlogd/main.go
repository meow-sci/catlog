// Command catlogd is the catlog backend: ingest, projections, read API and the
// server-rendered website.
//
// WP1 wiring: it loads config (§5.3), loads or creates the key set (§4.5.1,
// §4.7), opens and migrates both Turso databases (§5.4), serves GET /healthz
// (§4.4) and shuts down cleanly — draining the HTTP server, checkpointing each
// WAL and releasing the database file locks.
//
// The ingest path (WP2), identity and admin API (WP3), projector and read API
// (WP4) and the web UI (WP5) mount onto this skeleton.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
)

// version is the catlogd build version. Kept in lockstep with the mod's
// `mod_ver` (§4.2 session.started) once WP6 lands.
const version = "0.1.0-dev"

// shutdownGrace bounds the whole shutdown: HTTP drain, then a final WAL
// checkpoint per database.
const shutdownGrace = 20 * time.Second

func main() {
	var (
		configPath  = flag.String("config", "", "path to catlogd.toml (defaults to the built-in §5.3 dev values)")
		listen      = flag.String("listen", "", "public HTTP listen address, overriding the config (§3)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("catlogd", version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("configuration is unusable", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}

	// Signals are trapped before anything is opened, so a Ctrl-C during a slow
	// migration still reaches the shutdown path.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log, nil); err != nil {
		log.Error("catlogd exited with error", "err", err)
		os.Exit(1)
	}
}

// run boots the server and blocks until ctx is cancelled.
//
// ready, when non-nil, is called with the bound listener address once the
// server is accepting connections — the seam the startup test uses to reach a
// port chosen by the kernel.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ready func(net.Addr)) error {
	// Keys first: cheap, and a bad keys directory should fail before database
	// files are created.
	keySet, err := keys.LoadOrCreate(cfg.KeysDir())
	if err != nil {
		return fmt.Errorf("load keys: %w", err)
	}
	log.Info("keys ready", "keys", keySet)

	storeOpts := store.Options{Logger: log, CheckpointInterval: cfg.CheckpointInterval()}

	events, err := store.OpenEvents(ctx, cfg.EventsDBPath(), storeOpts)
	if err != nil {
		return fmt.Errorf("open events database: %w", err)
	}
	defer closeDB(log, "events", events.DB)

	projections, err := store.OpenProjections(ctx, cfg.ProjectionsDBPath(), storeOpts)
	if err != nil {
		return fmt.Errorf("open projections database: %w", err)
	}
	defer closeDB(log, "projections", projections.DB)

	mux := http.NewServeMux()
	// /healthz answers from memory on purpose: it must stay up while the
	// databases are busy or being checkpointed, so it never touches them (§4.4).
	mux.HandleFunc("GET /healthz", healthz)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}

	log.Info("catlogd listening",
		"addr", ln.Addr().String(),
		"version", version,
		"base_url", cfg.Server.BaseURL,
		"data_dir", cfg.Data.Dir,
		"events_schema", events.Version,
		"projections_schema", projections.Version)
	if ready != nil {
		ready(ln.Addr())
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// The deferred closes run next: each checkpoints its WAL and releases the
	// file lock. Releasing the lock matters as much as the checkpoint — the
	// next catlogd cannot start until this process has let go (§5.4).
	log.Info("catlogd stopped")
	return <-errCh
}

// closeDB closes a database and logs rather than swallowing the error: by the
// time this runs, run has no caller left to report to.
func closeDB(log *slog.Logger, name string, db *store.DB) {
	if err := db.Close(); err != nil {
		log.Error("closing database failed", "db", name, "err", err)
	}
}

// healthzBody is the exact §4.4 health response body. Encoded once at init so
// the endpoint neither allocates nor emits a trailing newline.
var healthzBody = func() []byte {
	b, err := json.Marshal(map[string]bool{"ok": true})
	if err != nil {
		panic(err)
	}
	return b
}()

// healthz implements GET /healthz (§4.4): no auth, no DB read, no DB write. It
// deliberately reports process liveness only — a health check that queried the
// database would take the whole service down for a load balancer whenever a
// checkpoint ran long.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(healthzBody); err != nil {
		slog.Warn("healthz write failed", "err", err)
	}
}
