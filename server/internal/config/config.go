// Package config loads the catlogd TOML configuration and applies CATLOG_*
// environment overrides (§5.3).
//
// Precedence, lowest to highest: built-in defaults (which are exactly the §5.3
// dev values, so catlogd runs with no config file at all) → the TOML file →
// environment variables.
//
// # Environment overrides
//
// Every scalar field is overridable by CATLOG_<SECTION>_<KEY>, uppercased with
// dots becoming underscores — the TOML path drives the name, so there is no
// second list to keep in sync:
//
//	[server] listen          → CATLOG_SERVER_LISTEN
//	[data]   dir             → CATLOG_DATA_DIR
//	[idp.discord] client_id  → CATLOG_IDP_DISCORD_CLIENT_ID
//	[ingest] accepted_htu    → CATLOG_INGEST_ACCEPTED_HTU  (comma-separated)
//
// This is what lets the deployment keep secrets out of the config file
// and what lets tests point a server at a temp directory.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// EnvPrefix prefixes every environment override.
const EnvPrefix = "CATLOG"

// Config is the whole of catlogd's configuration (§5.3).
type Config struct {
	Server Server `toml:"server"`
	Data   Data   `toml:"data"`
	Ingest Ingest `toml:"ingest"`
	Auth   Auth   `toml:"auth"`
	IdP    IdP    `toml:"idp"`
	Limits Limits `toml:"limits"`
	Boards Boards `toml:"boards"`
	// Projector is the [projector] section.
	Projector Projector `toml:"projector"`
	// Archive is the [archive] section.
	Archive Archive `toml:"archive"`
	CORS    CORS    `toml:"cors"`
}

// Archive is the [archive] section: the §5.10 nightly copy's per-run bound.
type Archive struct {
	// MaxEventsPerRun caps how many events one archive pass copies before it
	// advances the cursor and returns; a backlog is drained in resumable steps
	// of this size. Zero means the archiver's default (100,000).
	MaxEventsPerRun int `toml:"max_events_per_run"`
}

// Server is the [server] section.
type Server struct {
	// Listen is the public HTTP address (§3).
	Listen string `toml:"listen"`
	// AdminListen is the loopback-only admin mux (§3, §5.9). Never proxied.
	AdminListen string `toml:"admin_listen"`
	// BaseURL is both the license `iss` claim and the htu base (§4.5.1).
	BaseURL string `toml:"base_url"`
	// StaticDir is served at /static/ in dev; empty disables it because nginx
	// serves the assets in prod (§5.7).
	StaticDir string `toml:"static_dir"`
	// ClockControl mounts `POST /admin/clock`, which lets a caller move the
	// server's notion of now.
	//
	// **Development only, and off by default.** catlog's authoritative
	// timestamps are all server-generated — an event's `recv_time`, a license's
	// `iat`/`exp`, a session's expiry — so the server clock is the only thing
	// that decides which day, week, month or year a leaderboard row lands in.
	// Testing a yearly board otherwise means waiting a year.
	//
	// Three things keep it out of production: this defaults to false, the route
	// only exists on the loopback-only admin mux (§5.9), and [Config.Validate]
	// refuses the combination of `clock_control = true` with an https
	// `base_url` — because a deployment reachable over TLS is not a laptop.
	ClockControl bool `toml:"clock_control"`
	// MaxStreamClients caps concurrent SSE subscribers per stream route
	// (`/v1/feed/stream`, `/v1/events/stream`). Each open stream holds a
	// connection, a goroutine and a frame buffer for as long as the tab lives,
	// so this is the knob that keeps N browsers from being a memory bill.
	// Over the cap a stream open is answered 429 + Retry-After. Zero means
	// the read API's default (64, readapi.DefaultMaxStreamClients).
	MaxStreamClients int `toml:"max_stream_clients"`
}

// Data is the [data] section.
type Data struct {
	// Dir holds events.db, projections.db, keys/ and archive/ (§3). Relative
	// paths resolve against the process working directory.
	Dir string `toml:"dir"`
	// CheckpointIntervalS is how often each open database runs
	// `PRAGMA wal_checkpoint(TRUNCATE)`. Not in §5.3: the Turso WAL never
	// auto-checkpoints, so without an explicit timer the -wal file grows for
	// the life of the process. Zero or negative disables the timer; shutdown
	// still checkpoints.
	CheckpointIntervalS int `toml:"checkpoint_interval_s"`
	// CompressPayloads is whether events.db compresses event payloads on
	// write (zstd + a trained dictionary; see store migration 0003). Default
	// true; false is the escape hatch — new rows are then stored as plain
	// JSON text, and rows written either way stay readable, so the flag can
	// be flipped freely.
	CompressPayloads bool `toml:"compress_payloads"`
}

// Ingest is the [ingest] section (§4.3 limits, §4.5.2 htu).
type Ingest struct {
	// AcceptedHTU is compared against the proof's `htu` by string equality,
	// with no normalization (§4.5.2).
	AcceptedHTU []string `toml:"accepted_htu"`
	// MaxBodyBytes caps the compressed request body (§4.3: 1 MiB → 413).
	MaxBodyBytes int64 `toml:"max_body_bytes"`
	// MaxEvents caps events per batch (§4.3: 2000 → 413).
	MaxEvents int `toml:"max_events"`
	// MaxInFlight caps how many ingest requests may hold a body in memory at
	// once — the knob that sizes peak ingest memory on a small box. Zero (the
	// default) means 4× GOMAXPROCS; over the cap is 503 + Retry-After, same as
	// a full write queue.
	MaxInFlight int `toml:"max_inflight"`
}

// Auth is the [auth] section (D16, §4.7 quotas).
type Auth struct {
	LicenseTTLDays    int `toml:"license_ttl_days"`
	HandleQuota       int `toml:"handle_quota"`
	IssuancePerDay    int `toml:"issuance_per_day"`
	MinAccountAgeDays int `toml:"min_account_age_days"`
	// ReservedHandles are the "+ configurable extras" of §4.7's reserved list.
	// They are added to the built-in set (identity.ReservedHandles), never
	// substituted for it, and are matched case-insensitively.
	ReservedHandles []string `toml:"reserved_handles"`
}

// IdP is the [idp.*] sections (§4.7, §5.8). All URLs are configurable so the
// whole flow can point at mockidp (D2).
type IdP struct {
	Discord Discord `toml:"discord"`
	Google  Google  `toml:"google"`
	GitHub  GitHub  `toml:"github"`
}

// Discord is [idp.discord]: OAuth2 code flow, no OIDC (§4.7).
type Discord struct {
	AuthURL      string `toml:"auth_url"`
	TokenURL     string `toml:"token_url"`
	APIBase      string `toml:"api_base"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// Google is [idp.google]: OIDC code flow; the id_token is verified against
// JWKSURL (§4.7).
type Google struct {
	Issuer       string `toml:"issuer"`
	AuthURL      string `toml:"auth_url"`
	TokenURL     string `toml:"token_url"`
	JWKSURL      string `toml:"jwks_url"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// GitHub is [idp.github]: OAuth2 code flow, no scopes (§4.7).
type GitHub struct {
	AuthURL      string `toml:"auth_url"`
	TokenURL     string `toml:"token_url"`
	APIBase      string `toml:"api_base"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// Limits is the [limits] section: the per-credential token bucket (§4.3).
//
// # Two different knobs, on purpose
//
// RateLimitPerJKTPerS and RateLimitBurst *tune* the bucket. Whatever they are
// set to, every credential still has a bucket and a flood still meets a 429, so
// they are safe to change anywhere — including in production, where raising
// them is how you accommodate a client that legitimately ships more often.
//
// RateLimitDisabled *removes* the bucket. That is a different kind of change
// and it is gated like one; see its own comment.
type Limits struct {
	RateLimitPerJKTPerS float64 `toml:"ratelimit_per_jkt_per_s"`
	RateLimitBurst      int     `toml:"ratelimit_burst"`
	// RateLimitDisabled removes §4.5.3 step 9 from the verification chain
	// altogether: no token bucket is built, no credential is ever answered 429,
	// and a single client may ship as fast as it can sign.
	//
	// **Load testing only, and off by default.** The bucket is the one control
	// that bounds what a single stolen or modified client can cost the server,
	// and it is deliberately cheap to enforce — it sits after the two signature
	// checks and before the body is read, so a flood costs two ECDSA
	// verifications and nothing else. Turning it off means one client can fill
	// the write queue on its own.
	//
	// It exists because catlog.loadgen cannot otherwise measure the server: at
	// the §4.3 default of one batch per two seconds per credential, a harness
	// run is a measurement of the token bucket and of nothing else.
	//
	// Three things keep it out of production, the same three that keep
	// [Server.ClockControl] out: this defaults to false, [Config.Validate]
	// refuses the combination of `ratelimit_disabled = true` with an https
	// `base_url`, and catlogd logs a WARN naming the base URL for as long as
	// it runs with it on.
	//
	// To go *faster* rather than *unlimited*, raise RateLimitPerJKTPerS
	// instead. That keeps a real limit in the chain and needs no gate.
	RateLimitDisabled bool `toml:"ratelimit_disabled"`
}

// Boards is the [boards] section: how the public board index is assembled.
//
// Not in §5.3. It exists because two board families — `fastest_to_<body>` and
// `rud_<cause>` — take their keys from the event stream rather than from a list
// in the source, since KSA's celestial systems are content that mods extend
// (docs/events.md: `body` is "opaque to server").
type Boards struct {
	// MinPlayers is how many distinct players must hold a value on such a board
	// before `GET /v1/leaderboards` lists it.
	//
	// It is the whole of the answer to "a modified client could invent ten
	// thousand body names and fill the index": one comparison against a count
	// the index query already has. It is also correct on its own merits — a
	// leaderboard with one entrant is not a leaderboard — and it can never
	// punish an honest player, because the per-player value is recorded either
	// way and lowering this publishes history that is already there
	// (docs/CONSTITUTION.md §8, docs/DECISIONS.md).
	MinPlayers int `toml:"min_players"`
}

// Projector is the [projector] section: how much of the event log the fold
// loop takes on at a time.
//
// Not in §5.3. It exists because these are the two numbers that trade the
// projector's speed against its memory, and the right answer depends on the
// box. Folding is dominated by the cost of a Turso statement, so the projector
// buffers a batch's projection writes in memory and flushes the survivors
// together (internal/stats.Batch); a bigger batch coalesces more repeated
// writes to the same board and issues fewer statements. Measured on a
// telemetry-heavy synthetic log, going from 1,000 to 10,000 events per batch is
// worth about 20% — and multiplies the transient memory a batch holds by ten.
//
// The defaults are sized for a small VM. Raise batch_size on a machine with
// memory to spare.
type Projector struct {
	// BatchSize is how many events one fold transaction reads and folds. §5.6's
	// "batches of 1000". This is the projector's memory knob: peak footprint is
	// roughly this many events' decoded payloads.
	BatchSize int `toml:"batch_size"`
	// FlushRows is how many rows one flushed statement carries. Beyond a few
	// hundred the per-row cost is flat, so this exists to bound the bound
	// parameter count rather than to be tuned.
	FlushRows int `toml:"flush_rows"`
	// Decoders is how many goroutines decode a batch's payloads before the
	// serial fold. Zero means one per core, less one for the ingest that
	// produced the backlog. Decoding is the only part of folding that is not
	// serialised by the single writer; on a payload as small as §4.2's it is
	// also not where the time goes, so this is a knob for a future with fatter
	// payloads rather than a lever on today's numbers.
	Decoders int `toml:"decoders"`
	// TickS is the fallback poll interval in seconds. The ingest writer's
	// notify channel wakes the projector for every real arrival; the ticker
	// only recovers from a dropped wake-up, so this trades idle wake-ups
	// against worst-case lag after one. Zero means the projector's default
	// (5 s).
	TickS int `toml:"tick_s"`
}

// CORS is the [cors] section: which foreign origins may read the public §4.8
// endpoints from a browser.
//
// Not in §5.3. It exists because `spa/` is a second, independent frontend that
// is hosted somewhere else entirely (GitHub Pages) and reaches catlog over
// XHR — which the same-origin policy blocks unless the server says otherwise.
//
// The scope is deliberately narrow. Only the routes [readapi.Server.Register]
// mounts ever carry these headers; `/api/*`, the `/auth/*` flows and the admin
// mux are cookie-authenticated and must stay same-origin, and no response here
// is ever sent with `Access-Control-Allow-Credentials`.
type CORS struct {
	// AllowedOrigins is an exact-match allow-list of `scheme://host[:port]`
	// values — no wildcards, no suffix matching, and never `*`: the browser
	// compares the echoed origin byte for byte, so an entry that is not exactly
	// what the browser sends silently does nothing.
	//
	// Production sets it in the environment
	// (CATLOG_CORS_ALLOWED_ORIGINS=https://owner.github.io) rather than here,
	// so no deployment URL is baked into the repository.
	AllowedOrigins []string `toml:"allowed_origins"`
}

// Default returns the §5.3 defaults — the dev values, so catlogd starts with no
// config file. IdP credentials are deliberately empty: an unconfigured IdP is
// disabled, never silently pointed somewhere.
func Default() Config {
	return Config{
		Server: Server{
			Listen:      "127.0.0.1:8080",
			AdminListen: "127.0.0.1:6060",
			BaseURL:     "http://127.0.0.1:8080",
			StaticDir:   "../site/dist",
			// Off unless a config says otherwise. A default that allowed a real
			// deployment would be a default that shipped one.
			ClockControl: false,
			// Zero on purpose: the default lives in readapi
			// (DefaultMaxStreamClients, 64) — not imported, to keep config free
			// of a dependency on the read layer (same rule as min_players).
			MaxStreamClients: 0,
		},
		Data: Data{
			Dir: "./data",
			// 60 s == store.DefaultCheckpointInterval; not imported, to keep
			// config free of a dependency on the storage layer.
			CheckpointIntervalS: 60,
			CompressPayloads:    true,
		},
		Ingest: Ingest{
			AcceptedHTU:  []string{"http://127.0.0.1:8080/v1/ingest"},
			MaxBodyBytes: 1 << 20, // 1 MiB (§4.3)
			MaxEvents:    2000,    // §4.3
		},
		Auth: Auth{
			LicenseTTLDays:    180, // D16
			HandleQuota:       5,   // §4.7
			IssuancePerDay:    3,   // §4.7
			MinAccountAgeDays: 30,  // §4.7
		},
		Limits: Limits{
			RateLimitPerJKTPerS: 0.5, // 1 batch / 2 s (§4.3)
			RateLimitBurst:      5,
			// Off unless a config says otherwise. A default that shipped without
			// the token bucket would be a default that shipped a server one
			// client can saturate.
			RateLimitDisabled: false,
		},
		Boards: Boards{
			// 2 == stats.DefaultMinPlayers; not imported, to keep config free of
			// a dependency on the projection layer (same rule as the checkpoint
			// interval above). The default is the protection, not an opt-in.
			MinPlayers: 2,
		},
		Projector: Projector{
			// 1000 == projector.DefaultBatchSize and 500 ==
			// stats.DefaultFlushRows; not imported, to keep config free of a
			// dependency on the projection layer (same rule as the checkpoint
			// interval and min_players above).
			BatchSize: 1000,
			FlushRows: 500,
			// Zero means projector.DefaultDecoders, which needs to know how many
			// cores the machine has and so cannot be a constant here.
			Decoders: 0,
		},
		CORS: CORS{
			// Vite's dev server (5173) and its `preview` server (4173) on both
			// spellings of loopback — the four origins `pnpm -C spa dev` and
			// `pnpm -C spa preview` can actually appear as. Nothing public: a
			// default that allowed a real deployment would be a default that
			// shipped one.
			AllowedOrigins: []string{
				"http://127.0.0.1:5173",
				"http://localhost:5173",
				"http://127.0.0.1:4173",
				"http://localhost:4173",
			},
		},
	}
}

// Load reads path (when non-empty), applies environment overrides and
// validates. A missing file is an error — silently running on defaults because
// of a typo'd -config is how a prod server ends up issuing dev licenses.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return Config{}, fmt.Errorf("config: load %s: %w", path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, 0, len(undecoded))
			for _, k := range undecoded {
				keys = append(keys, k.String())
			}
			return Config{}, fmt.Errorf("config: %s has unknown keys: %s", path, strings.Join(keys, ", "))
		}
	}

	if err := applyEnv(&cfg, os.LookupEnv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects a configuration that would misbehave at runtime rather than
// at startup.
func (c Config) Validate() error {
	var errs []error
	if c.Server.Listen == "" {
		errs = append(errs, errors.New("server.listen is empty"))
	}
	if c.Server.ClockControl && strings.HasPrefix(strings.ToLower(c.Server.BaseURL), "https://") {
		// The one combination that must never exist: a TLS deployment whose
		// clock a caller can move. Everything catlog signs and every rolling
		// window it computes hangs off this clock, so this is a hard refusal at
		// start-up rather than a warning somebody scrolls past.
		errs = append(errs, errors.New(
			"server.clock_control must not be enabled on an https deployment: it is a development-only control"))
	}
	if c.Server.BaseURL == "" {
		errs = append(errs, errors.New("server.base_url is empty"))
	} else if u, err := url.Parse(c.Server.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("server.base_url: %w", err))
	} else if u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("server.base_url %q is not an absolute URL", c.Server.BaseURL))
	} else if strings.HasSuffix(c.Server.BaseURL, "/") {
		// base_url is concatenated to build issuer and htu values; a trailing
		// slash silently produces "…//v1/ingest" and breaks the §4.5.2
		// string-equality comparison.
		errs = append(errs, fmt.Errorf("server.base_url %q must not end in /", c.Server.BaseURL))
	}
	if c.Data.Dir == "" {
		errs = append(errs, errors.New("data.dir is empty"))
	}
	if len(c.Ingest.AcceptedHTU) == 0 {
		errs = append(errs, errors.New("ingest.accepted_htu is empty (no proof could ever be accepted)"))
	}
	if c.Ingest.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("ingest.max_body_bytes must be positive"))
	}
	if c.Ingest.MaxEvents <= 0 {
		errs = append(errs, errors.New("ingest.max_events must be positive"))
	}
	if c.Ingest.MaxInFlight < 0 {
		errs = append(errs, errors.New("ingest.max_inflight must be zero (auto) or positive"))
	}
	if c.Auth.LicenseTTLDays <= 0 {
		errs = append(errs, errors.New("auth.license_ttl_days must be positive"))
	}
	if c.Auth.HandleQuota <= 0 {
		errs = append(errs, errors.New("auth.handle_quota must be positive"))
	}
	if c.Auth.IssuancePerDay <= 0 {
		errs = append(errs, errors.New("auth.issuance_per_day must be positive"))
	}
	if c.Auth.MinAccountAgeDays < 0 {
		errs = append(errs, errors.New("auth.min_account_age_days is negative"))
	}
	if c.Limits.RateLimitPerJKTPerS <= 0 {
		errs = append(errs, errors.New("limits.ratelimit_per_jkt_per_s must be positive"))
	}
	if c.Limits.RateLimitBurst <= 0 {
		errs = append(errs, errors.New("limits.ratelimit_burst must be positive"))
	}
	if c.Limits.RateLimitDisabled && strings.HasPrefix(strings.ToLower(c.Server.BaseURL), "https://") {
		// The other combination that must never exist (see clock_control
		// above): a TLS deployment with no per-credential ceiling at all. The
		// token bucket is what bounds one stolen credential's cost, so this is
		// a hard refusal at start-up rather than a warning somebody scrolls
		// past. Raising limits.ratelimit_per_jkt_per_s is the supported way to
		// let a real deployment ship faster.
		errs = append(errs, errors.New(
			"limits.ratelimit_disabled must not be enabled on an https deployment: "+
				"it is a load-testing-only control; raise limits.ratelimit_per_jkt_per_s instead"))
	}
	if c.Boards.MinPlayers < 1 {
		errs = append(errs, errors.New("boards.min_players must be at least 1"))
	}
	if c.Projector.BatchSize < 1 {
		errs = append(errs, errors.New("projector.batch_size must be at least 1"))
	}
	if c.Projector.FlushRows < 1 {
		errs = append(errs, errors.New("projector.flush_rows must be at least 1"))
	}
	if c.Projector.Decoders < 0 {
		errs = append(errs, errors.New("projector.decoders must not be negative"))
	}
	if c.Projector.TickS < 0 {
		errs = append(errs, errors.New("projector.tick_s must be zero (default) or positive"))
	}
	if c.Archive.MaxEventsPerRun < 0 {
		errs = append(errs, errors.New("archive.max_events_per_run must be zero (default) or positive"))
	}
	// A CORS origin is compared to the browser's `Origin` header by exact string
	// equality, so a typo cannot fail at request time in any visible way — the
	// header simply never matches and the SPA sees an opaque network error with
	// nothing in the server log. Reject the malformed shapes at startup instead.
	for _, origin := range c.CORS.AllowedOrigins {
		errs = append(errs, validateOrigin(origin))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// validateOrigin rejects anything a browser would never send as an `Origin`.
// `*` is rejected too: catlog's public data would survive a wildcard, but the
// allow-list is also the place a future maintainer would reach for to "just make
// it work", and a wildcard there is one `Access-Control-Allow-Credentials` away
// from being a real hole.
func validateOrigin(origin string) error {
	switch {
	case origin == "":
		return errors.New("cors.allowed_origins contains an empty entry")
	case origin == "*":
		return errors.New(`cors.allowed_origins may not contain "*"; list the origins explicitly`)
	}
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return fmt.Errorf("cors.allowed_origins %q: %w", origin, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("cors.allowed_origins %q must be http:// or https://", origin)
	case u.Host == "":
		return fmt.Errorf("cors.allowed_origins %q has no host", origin)
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		// An `Origin` header is scheme + host + port and nothing else, so
		// "https://example.com/" never matches "https://example.com".
		return fmt.Errorf("cors.allowed_origins %q must be scheme://host[:port] with no path", origin)
	}
	return nil
}

// Derived paths (§3). All relative to Data.Dir, which is itself relative to the
// process working directory.

// EventsDBPath is data/events.db.
func (c Config) EventsDBPath() string { return filepath.Join(c.Data.Dir, "events.db") }

// ProjectionsDBPath is data/projections.db.
func (c Config) ProjectionsDBPath() string { return filepath.Join(c.Data.Dir, "projections.db") }

// KeysDir is data/keys/.
func (c Config) KeysDir() string { return filepath.Join(c.Data.Dir, "keys") }

// ArchiveDir is data/archive/ (§5.10).
func (c Config) ArchiveDir() string { return filepath.Join(c.Data.Dir, "archive") }

// CheckpointInterval is Data.CheckpointIntervalS as a duration; 0 means the
// timer is disabled.
func (c Config) CheckpointInterval() time.Duration {
	return time.Duration(c.Data.CheckpointIntervalS) * time.Second
}

// IngestURL is BaseURL + "/v1/ingest" — the value a dev proof's `htu` must
// carry (§4.5.2).
func (c Config) IngestURL() string { return c.Server.BaseURL + "/v1/ingest" }

// LicenseTTL is Auth.LicenseTTLDays as a duration (D16).
func (c Config) LicenseTTL() time.Duration {
	return time.Duration(c.Auth.LicenseTTLDays) * 24 * time.Hour
}

// --- environment overrides -------------------------------------------------

// applyEnv walks the struct and overrides any field whose CATLOG_* variable is
// set. lookup is injectable so tests need not mutate the process environment.
// A variable that is set but empty counts as an override, so
// CATLOG_SERVER_STATIC_DIR= disables static serving (§5.3: empty = disabled).
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	return walk(reflect.ValueOf(cfg).Elem(), EnvPrefix, lookup)
}

func walk(v reflect.Value, prefix string, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		name := field.Tag.Get("toml")
		if name == "" || name == "-" {
			continue
		}
		key := prefix + "_" + strings.ToUpper(strings.ReplaceAll(name, ".", "_"))
		fv := v.Field(i)

		if fv.Kind() == reflect.Struct {
			if err := walk(fv, key, lookup); err != nil {
				return err
			}
			continue
		}
		raw, ok := lookup(key)
		if !ok {
			continue
		}
		if err := set(fv, raw); err != nil {
			return fmt.Errorf("config: %s=%q: %w", key, raw, err)
		}
	}
	return nil
}

func set(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("want an integer: %w", err)
		}
		fv.SetInt(n)
	case reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("want a number: %w", err)
		}
		fv.SetFloat(f)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("want a boolean: %w", err)
		}
		fv.SetBool(b)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice type %s", fv.Type())
		}
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		fv.Set(reflect.ValueOf(out))
	default:
		return fmt.Errorf("unsupported kind %s", fv.Kind())
	}
	return nil
}
