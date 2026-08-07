// Command catlogd is the catlog backend: ingest, projections, read API and the
// server-rendered website.
//
// WP0 scaffolding: this is a minimal net/http server exposing only GET /healthz
// (§4.4). Config, storage, ingest, identity and the web UI arrive in WP1+.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is the catlogd build version. Kept in lockstep with the mod's
// `mod_ver` (§4.2 session.started) once WP6 lands.
const version = "0.1.0-dev"

func main() {
	var (
		listen      = flag.String("listen", "127.0.0.1:8080", "public HTTP listen address (§3)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("catlogd", version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(*listen, log); err != nil {
		log.Error("catlogd exited with error", "err", err)
		os.Exit(1)
	}
}

func run(listen string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("catlogd listening", "addr", listen, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("catlogd stopped")
	return <-errCh
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

// healthz implements GET /healthz (§4.4): no auth, no DB write.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(healthzBody); err != nil {
		slog.Warn("healthz write failed", "err", err)
	}
}
