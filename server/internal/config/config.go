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
// This is what lets the systemd unit (§11) keep secrets out of the config file
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
type Limits struct {
	RateLimitPerJKTPerS float64 `toml:"ratelimit_per_jkt_per_s"`
	RateLimitBurst      int     `toml:"ratelimit_burst"`
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
		},
		Data: Data{
			Dir: "./data",
			// 60 s == store.DefaultCheckpointInterval; not imported, to keep
			// config free of a dependency on the storage layer.
			CheckpointIntervalS: 60,
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
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
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
