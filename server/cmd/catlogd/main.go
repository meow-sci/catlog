// Command catlogd is the catlog backend: ingest, projections, read API and the
// server-rendered website.
//
// Wiring as of WP3: it loads config (§5.3), loads or creates the key set
// (§4.5.1, §4.7), opens and migrates both Turso databases (§5.4), serves
// GET /healthz and POST /v1/ingest on the public port (§4.4), runs the
// projector and the §4.8 read API, serves the identity flows, the dashboard
// JSON API and the two well-known documents (§4.7, §5.8), serves the
// loopback-only admin mux on its own port (§5.9), and shuts down cleanly —
// draining both HTTP servers, stopping the ingest writer, checkpointing each
// WAL and releasing the database file locks.
//
// Wiring as of WP10 additionally builds the filesystem archive store
// (data/archive/) and hands it to both the admin mux and the identity purge
// path. The web UI (WP5) mounts onto the same skeleton.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/clock"
	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/identity"
	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/web"
)

// version is the catlogd build version. Kept in lockstep with the mod's
// `mod_ver` (§4.2 session.started) once WP6 lands.
//
// A `var`, not a `const`, so the container build can stamp the real thing:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// That is what lets `make ops-status` say which build is actually running,
// rather than which one somebody believes they deployed.
var version = "0.1.0-dev"

// shutdownGrace bounds the whole shutdown: HTTP drain, writer drain, then a
// final WAL checkpoint per database.
const shutdownGrace = 20 * time.Second

func main() {
	var (
		configPath  = flag.String("config", "", "path to catlogd.toml (defaults to the built-in §5.3 dev values)")
		listen      = flag.String("listen", "", "public HTTP listen address, overriding the config (§3)")
		adminListen = flag.String("admin-listen", "", "loopback admin listen address, overriding the config (§3)")
		showVersion = flag.Bool("version", false, "print version and exit")
		healthCheck = flag.Bool("healthcheck", false, "probe GET /healthz on this server's listen address; exit 0 if healthy")
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
	if *adminListen != "" {
		cfg.Server.AdminListen = *adminListen
	}

	// -healthcheck runs *after* the config is resolved and before anything is
	// opened. It is the container HEALTHCHECK: the runtime image has no shell
	// and no curl, so the probe has to be the binary itself.
	if *healthCheck {
		if err := probeHealth(cfg.Server.Listen); err != nil {
			fmt.Fprintln(os.Stderr, "catlogd healthcheck:", err)
			os.Exit(1)
		}
		return
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
// ready, when non-nil, is called with the bound public and admin addresses once
// both servers are accepting connections — the seam the startup test uses to
// reach ports chosen by the kernel.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ready func(public, admin net.Addr)) error {
	// Keys first: cheap, and a bad keys directory should fail before database
	// files are created.
	keySet, err := keys.LoadOrCreate(cfg.KeysDir())
	if err != nil {
		return fmt.Errorf("load keys: %w", err)
	}
	log.Info("keys ready", "keys", keySet)

	// One clock for the whole server. Every authoritative timestamp catlog
	// produces — an event's `recv_time`, a license's `iat`/`exp`, a session's
	// expiry, the `Date` header a client resynchronises against — reads this,
	// so they cannot disagree about what day it is. It is [time.Now] unless
	// `[server] clock_control` is on, which Validate refuses for an https
	// deployment.
	srvClock := clock.New(cfg.Server.ClockControl)
	if srvClock.Controllable() {
		log.Warn("server clock control is ENABLED — POST /admin/clock can move this server's notion of now",
			"base_url", cfg.Server.BaseURL)
	}

	storeOpts := store.Options{
		Logger:             log,
		CheckpointInterval: cfg.CheckpointInterval(),
		Now:                srvClock.Now,
	}

	// Payload compression is an events.db concern only; the flag is ignored
	// by OpenProjections, so the shared options struct stays shared.
	eventsOpts := storeOpts
	eventsOpts.DisablePayloadCompression = !cfg.Data.CompressPayloads
	events, err := store.OpenEvents(ctx, cfg.EventsDBPath(), eventsOpts)
	if err != nil {
		return fmt.Errorf("open events database: %w", err)
	}
	defer closeDB(log, "events", events.DB)

	projections, err := store.OpenProjections(ctx, cfg.ProjectionsDBPath(), storeOpts)
	if err != nil {
		return fmt.Errorf("open projections database: %w", err)
	}
	defer closeDB(log, "projections", projections.DB)

	// The deny-list is in-memory and authoritative in-process (§5.8); it is
	// rebuilt from the database at every start so a ban survives a restart.
	deny := authz.NewDenyList()
	if err := deny.LoadFrom(ctx, events); err != nil {
		return fmt.Errorf("load deny-list: %w", err)
	}

	// The per-credential token bucket (§4.3, §4.5.3 step 9). `ratelimit_disabled`
	// omits it entirely so catlog.loadgen can measure the server rather than the
	// bucket; Validate refuses that combination on an https base_url, and it is
	// announced here for as long as the process runs, the same as clock control.
	if cfg.Limits.RateLimitDisabled {
		log.Warn("per-credential rate limiting is DISABLED — one credential may ship as fast as it can sign",
			"base_url", cfg.Server.BaseURL)
	}
	verifier := authz.New(authz.Config{
		Issuer:            cfg.Server.BaseURL,
		AcceptedHTU:       cfg.Ingest.AcceptedHTU,
		RatePerSecond:     cfg.Limits.RateLimitPerJKTPerS,
		Burst:             cfg.Limits.RateLimitBurst,
		RateLimitDisabled: cfg.Limits.RateLimitDisabled,
	}, keySet, events, deny)
	// Also re-points the rate limiter, so the token bucket and the skew window
	// agree with the clock that stamps recv_time.
	verifier.SetClock(srvClock.Now)

	// One writer goroutine owns every write to events.db (§5.5). It runs on its
	// own context so shutdown can drain HTTP first and only then stop it.
	writerCtx, stopWriter := context.WithCancel(context.Background())
	defer stopWriter()
	writer := ingest.NewWriter(events, log)
	writer.SetClock(srvClock.Now)
	go writer.Run(writerCtx)
	ingest.PublishQueueDepth(writer)

	ingestHandler := ingest.NewHandler(verifier, writer, ingest.Limits{
		MaxBodyBytes: cfg.Ingest.MaxBodyBytes,
		MaxEvents:    cfg.Ingest.MaxEvents,
		MaxInFlight:  cfg.Ingest.MaxInFlight,
	}, log)

	// The in-memory player_id ↔ handle map (§5.4): projections.db cannot be
	// joined to events.db, so the read API and the feed resolve handles here.
	// Identity mutations (claim, revoke, ban) call Reload.
	dir := directory.New(events)
	if err := dir.Reload(ctx); err != nil {
		return fmt.Errorf("load handle directory: %w", err)
	}

	// The projector owns projections.db from here on: the read API queries it
	// through Live, which holds the RWMutex a rebuild's swap needs (§5.6).
	// Shutdown must close the *live* handle rather than the one opened above —
	// a rebuild replaces it with a different object on the same path.
	live := projector.NewLive(projections)
	defer func() {
		if err := live.Close(); err != nil {
			log.Error("closing database failed", "db", "projections", "err", err)
		}
	}()
	broadcaster := projector.NewBroadcaster()
	// The raw twin: every stored event the fold loop commits past, for the
	// public `/v1/events/stream`. Redaction happens in readapi, at publish.
	rawBroadcaster := projector.NewRawBroadcaster()
	proj, err := projector.New(projector.Options{
		Events:       events,
		Live:         live,
		Directory:    dir,
		Broadcaster:  broadcaster,
		Raw:          rawBroadcaster,
		Notify:       writer.Notify(),
		StoreOptions: storeOpts,
		BatchSize:    cfg.Projector.BatchSize,
		FlushRows:    cfg.Projector.FlushRows,
		Decoders:     cfg.Projector.Decoders,
		Tick:         time.Duration(cfg.Projector.TickS) * time.Second,
		Log:          log,
	})
	if err != nil {
		return fmt.Errorf("build projector: %w", err)
	}
	projectorCtx, stopProjector := context.WithCancel(context.Background())
	go proj.Run(projectorCtx)
	// Registered after the live-handle close above, so LIFO stops the fold loop
	// before the database it writes to goes away.
	defer func() {
		stopProjector()
		proj.Wait()
	}()

	reader, err := readapi.New(readapi.Deps{
		Projections: live,
		Events:      events,
		Directory:   dir,
		// The JSON half of the activity feed (`/v1/feed/stream`), for any
		// client that wants events rather than markup: the datastar stream in
		// package web is HTML and stays where it is.
		Feed: broadcaster,
		// The live half of the raw event log (`/v1/events/stream`).
		RawEvents: rawBroadcaster,
		// Concurrent SSE subscribers per stream route (config [server]).
		MaxStreamClients: cfg.Server.MaxStreamClients,
		// How many players a board whose key came out of the event stream needs
		// before the public index lists it (config.Boards).
		MinBoardPlayers: cfg.Boards.MinPlayers,
		// Which window "this week" is, on the same clock that stamped the
		// buckets — see readapi.Deps.Now.
		Now: srvClock.Now,
		// Cross-origin reads, and nothing else — see readapi/cors.go. The
		// dashboard API, the auth flows and the admin mux never see this list.
		AllowedOrigins: cfg.CORS.AllowedOrigins,
		Log:            log,
	})
	if err != nil {
		return fmt.Errorf("build read api: %w", err)
	}

	mux := http.NewServeMux()
	// /healthz answers from memory on purpose: it must stay up while the
	// databases are busy or being checkpointed, so it never touches them (§4.4).
	mux.HandleFunc("GET /healthz", healthz)
	ingestHandler.Register(mux)
	reader.Register(mux)

	admin := adminapi.New(adminapi.Deps{
		Config:   cfg,
		Keys:     keySet,
		Events:   events,
		Verifier: verifier,
		Log:      log,
		Now:      srvClock.Now,
	})
	// Unmounted unless the clock is controllable, so on a normal server
	// /admin/clock does not exist at all.
	admin.RegisterClock(adminapi.ClockDeps{Clock: srvClock})
	admin.RegisterProjections(adminapi.ProjectionDeps{
		Projector: proj,
		Directory: dir,
		Writer:    writer,
	})

	// The archiver (§5.10, D8): a filesystem store rooted at data/archive/, and
	// nothing that talks to R2 — that implementation is design-only
	// (docs/r2-archive-design.md). It is built before identity because identity
	// takes it as the purge seam.
	archiveStore, err := archive.NewFSStore(cfg.ArchiveDir())
	if err != nil {
		return fmt.Errorf("open archive store: %w", err)
	}
	archiver, err := archive.New(archive.Options{
		Events: events, Store: archiveStore, Log: log, Now: srvClock.Now,
		MaxEvents: cfg.Archive.MaxEventsPerRun,
	})
	if err != nil {
		return fmt.Errorf("build archiver: %w", err)
	}
	admin.RegisterArchive(adminapi.ArchiveDeps{Archiver: archiver})

	// Identity (§4.7, §5.8): the three IdP flows, the session cookie, handle
	// claims and issuance, and the two well-known documents. Its writes share
	// the admin API's mutex, which is what makes a dashboard claim and an admin
	// ban serialise against each other (§5.4).
	ident, err := identity.New(identity.Deps{
		Config:    cfg,
		Keys:      keySet,
		Events:    events,
		Deny:      deny,
		Directory: dir,
		WriteLock: admin.WithWriteLock,
		// The purge seam (§4.7): a purge deletes the player's archive prefix
		// too, and fails if it cannot — leaving a deleted account's raw event
		// log in storage is the one thing a purge exists to prevent.
		Archive: archiver,
		Log:     log,
		Now:     srvClock.Now,
	})
	if err != nil {
		return fmt.Errorf("build identity: %w", err)
	}
	ident.Register(mux)
	admin.RegisterIdentity(adminapi.IdentityDeps{
		Moderator: ident.Moderator(),
		DenyList:  ident.DenyListPublisher(),
	})
	// `POST /admin/events`: the loopback-only path that pushes events without the
	// §4.5.3 auth chain. It is what the §8 feed spec and the dev loop use to make
	// something happen on demand (§5.9's seed is idempotent and cannot).
	admin.RegisterEvents()

	// The web UI (§5.7). It is built after identity because it needs that
	// server's session codec and account loader, and it hands back the
	// login-failure template — the one direction that has to happen after
	// construction because the two are mutually dependent.
	site, err := web.New(web.Deps{
		Config:      cfg,
		Read:        reader,
		Projections: live,
		Feed:        broadcaster,
		// The events pages' live tail (`/v1/events/sse`): the same raw
		// broadcaster the readapi JSON stream consumes, redacted through the
		// same readapi seam before anything is rendered.
		Raw:      rawBroadcaster,
		Sessions: ident.Sessions(),
		Accounts: ident,
		Log:      log,
	})
	if err != nil {
		return fmt.Errorf("build web ui: %w", err)
	}
	ident.SetErrorPage(site.AuthError)
	// Registered last: its `GET /` is the catch-all 404 page, and Go's pattern
	// router only falls through to it when nothing more specific matched.
	site.Register(mux)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	adminSrv := &http.Server{Handler: admin.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}
	// The admin mux is loopback-only and never proxied (§3, §5.9); the handler
	// also refuses non-loopback peers, so a bad address here fails closed.
	adminLn, err := net.Listen("tcp", cfg.Server.AdminListen)
	if err != nil {
		ln.Close()
		return fmt.Errorf("listen on %s: %w", cfg.Server.AdminListen, err)
	}

	log.Info("catlogd listening",
		"addr", ln.Addr().String(),
		"admin_addr", adminLn.Addr().String(),
		"version", version,
		"base_url", cfg.Server.BaseURL,
		"data_dir", cfg.Data.Dir,
		"events_schema", events.Version,
		"projections_schema", projections.Version)
	if ready != nil {
		ready(ln.Addr(), adminLn.Addr())
	}

	errCh := make(chan error, 2)
	go func() { errCh <- serve(srv, ln) }()
	go func() { errCh <- serve(adminSrv, adminLn) }()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	var errs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("graceful shutdown: %w", err))
	}
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("admin graceful shutdown: %w", err))
	}
	// HTTP is drained, so nothing new can be queued: stop the writer and wait
	// for the in-flight transaction to commit before the databases close.
	stopWriter()
	writer.Wait()
	// Then the projector, so the last batch is folded rather than replayed on
	// the next start.
	stopProjector()
	proj.Wait()

	// The deferred closes run next: each checkpoints its WAL and releases the
	// file lock. Releasing the lock matters as much as the checkpoint — the
	// next catlogd cannot start until this process has let go (§5.4).
	log.Info("catlogd stopped")
	errs = append(errs, <-errCh, <-errCh)
	return errors.Join(errs...)
}

func serve(srv *http.Server, ln net.Listener) error {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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

// healthProbeTimeout bounds the -healthcheck request. Docker's own
// --timeout kills the process on top of this; a shorter bound of our own means
// the failure says which side gave up.
const healthProbeTimeout = 2 * time.Second

// probeHealth implements `catlogd -healthcheck` (§4.4): one GET /healthz
// against this server's own listen address.
//
// It exists because the production runtime image is a Docker Hardened Image
// with no shell and no HTTP client, so `HEALTHCHECK CMD curl …` is not
// available — the probe must be a binary, and shipping a second one to make a
// single request would be a second thing to build, stamp and keep in sync.
//
// It opens no database, on purpose and permanently. tursogo takes an exclusive
// whole-file lock that excludes other processes, so a probe that opened
// events.db would fail whenever the server was healthy and succeed only when it
// was not — an inversion that would restart a working server every ten seconds.
// Process liveness is exactly what /healthz reports, and exactly what this
// checks.
func probeHealth(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen address %q is unusable: %w", listen, err)
	}
	// 0.0.0.0 and :: are bind addresses, not destinations — dialling them is
	// undefined on some platforms and wrong on all of them. The probe runs
	// inside the same network namespace as the server, so loopback is both
	// correct and the only address that cannot be firewalled away from it.
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, int64(len(healthzBody))+1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", url, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, res.StatusCode)
	}
	// Compared to the exact body the endpoint serves, not merely "2xx": a proxy
	// or a stray handler answering 200 with something else is not this server.
	if !bytes.Equal(bytes.TrimSpace(body), healthzBody) {
		return fmt.Errorf("%s answered 200 with an unexpected body: %q", url, body)
	}
	return nil
}
