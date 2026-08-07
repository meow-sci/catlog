// Command mockidp stands in for Discord, Google and GitHub so the whole system
// runs locally (D2, §5.8.1). It mints its own signing key and mirrors the exact
// response shapes of the real providers for the fields catlogd reads.
//
// One binary, one port (§3: 127.0.0.1:9090), three providers:
//
//	GET  /discord/oauth/authorize          consent page → 302 with ?code=
//	POST /discord/oauth/token              form POST, checks client id/secret
//	GET  /discord/api/users/@me            {"id": "<snowflake>", …}
//	GET  /google/authorize                 consent page → 302 with ?code=
//	POST /google/token                     {"id_token": <really-signed ES256 JWS>, …}
//	GET  /google/jwks                      the JWKS that verifies it
//	GET  /github/login/oauth/authorize     consent page → 302 with ?code=
//	POST /github/login/oauth/access_token  form-encoded, or JSON on request
//	GET  /github/user                      {"id": 4242, "created_at": "…", …}
//	GET  /healthz                          {"ok": true}
//
// The cast lives in `server/mockidp.toml`: one aged Discord account and one
// minted at start-up, so catlogd's ≥30-day account-age gate is exercised in
// both directions, and the same pair for GitHub. Every consent-page button
// carries the stable DOM id `#login-as-<slug(label)>` that WP5's playwright
// suite clicks.
//
// mockidp holds nothing worth persisting: codes and tokens live in memory and
// the Google signing key is generated fresh at every start.
package main

import (
	"context"
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
)

const version = "0.1.0-dev"

func main() {
	var (
		listen      = flag.String("listen", "127.0.0.1:9090", "HTTP listen address (§3)")
		configPath  = flag.String("config", "", "path to mockidp.toml (defaults to the built-in cast)")
		baseURL     = flag.String("base-url", "", "how catlogd reaches this process; defaults to http://<listen>")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mockidp", version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Error("configuration is unusable", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, *listen, *baseURL, log, nil); err != nil {
		log.Error("mockidp exited with error", "err", err)
		os.Exit(1)
	}
}

// run boots the mock IdP and blocks until ctx is cancelled. ready, when
// non-nil, is called with the bound address — the seam tests use to reach a
// kernel-chosen port.
func run(ctx context.Context, cfg Config, listen, baseURL string, log *slog.Logger, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	if baseURL == "" {
		baseURL = "http://" + ln.Addr().String()
	}

	s, err := NewServer(cfg, baseURL, log)
	if err != nil {
		ln.Close()
		return err
	}

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}

	accounts := make([]string, 0, len(s.Accounts()))
	for _, a := range s.Accounts() {
		accounts = append(accounts, a.IdP+":#"+a.ElementID)
	}
	log.Info("mockidp listening",
		"addr", ln.Addr().String(), "base_url", baseURL, "version", version, "accounts", accounts)
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
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("mockidp stopped")
	return <-errCh
}
