// Command mockidp stands in for Discord, Google and GitHub so the whole system
// runs locally (D2, §5.8.1). It mints its own signing key and mirrors the exact
// response shapes of the real providers for the fields catlogd reads.
//
// WP0 scaffolding: only GET /healthz is served; the OAuth/OIDC endpoints land in
// WP3.
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

const version = "0.1.0-dev"

func main() {
	var (
		listen      = flag.String("listen", "127.0.0.1:9090", "HTTP listen address (§3)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mockidp", version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(*listen, log); err != nil {
		log.Error("mockidp exited with error", "err", err)
		os.Exit(1)
	}
}

func run(listen string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		body, _ := json.Marshal(map[string]bool{"ok": true})
		_, _ = w.Write(body)
	})

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("mockidp listening", "addr", listen, "version", version)
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
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("mockidp stopped")
	return <-errCh
}
