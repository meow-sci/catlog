package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default() is invalid: %v", err)
	}
}

func TestLoadWithoutFileUsesDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("Load(\"\") = %+v, want %+v", cfg, Default())
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("Load of a missing file succeeded; a typo'd -config must not silently run on defaults")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \"1.2.3.4:1\"\nlissen = \"typo\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("err = %v, want an unknown-keys complaint", err)
	}
}

// TestDevConfigMatchesSpec loads the committed dev config and checks the values
// WP3 depends on: every IdP points at mockidp on 9090 with dev/dev (§5.3).
func TestDevConfigMatchesSpec(t *testing.T) {
	cfg, err := Load("../../catlogd.dev.toml")
	if err != nil {
		t.Fatalf("load dev config: %v", err)
	}

	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Errorf("server.listen = %q, want 127.0.0.1:8080 (§3)", cfg.Server.Listen)
	}
	if cfg.Server.AdminListen != "127.0.0.1:6060" {
		t.Errorf("server.admin_listen = %q, want 127.0.0.1:6060 (§3)", cfg.Server.AdminListen)
	}
	if cfg.Server.BaseURL != "http://127.0.0.1:8080" {
		t.Errorf("server.base_url = %q, want http://127.0.0.1:8080 (§3)", cfg.Server.BaseURL)
	}
	if got := cfg.IngestURL(); got != cfg.Ingest.AcceptedHTU[0] {
		t.Errorf("IngestURL() = %q but accepted_htu[0] = %q; a dev proof would be rejected (§4.5.2)",
			got, cfg.Ingest.AcceptedHTU[0])
	}
	if cfg.Auth.LicenseTTLDays != 180 {
		t.Errorf("auth.license_ttl_days = %d, want 180 (D16)", cfg.Auth.LicenseTTLDays)
	}

	idpURLs := map[string][]string{
		"discord": {cfg.IdP.Discord.AuthURL, cfg.IdP.Discord.TokenURL, cfg.IdP.Discord.APIBase},
		"google":  {cfg.IdP.Google.Issuer, cfg.IdP.Google.AuthURL, cfg.IdP.Google.TokenURL, cfg.IdP.Google.JWKSURL},
		"github":  {cfg.IdP.GitHub.AuthURL, cfg.IdP.GitHub.TokenURL, cfg.IdP.GitHub.APIBase},
	}
	for idp, urls := range idpURLs {
		for _, u := range urls {
			if !strings.HasPrefix(u, "http://127.0.0.1:9090/") {
				t.Errorf("%s URL %q does not point at mockidp on 127.0.0.1:9090 (D2, §5.8.1)", idp, u)
			}
		}
	}
	creds := [][2]string{
		{cfg.IdP.Discord.ClientID, cfg.IdP.Discord.ClientSecret},
		{cfg.IdP.Google.ClientID, cfg.IdP.Google.ClientSecret},
		{cfg.IdP.GitHub.ClientID, cfg.IdP.GitHub.ClientSecret},
	}
	for i, c := range creds {
		if c[0] != "dev" || c[1] != "dev" {
			t.Errorf("idp[%d] client id/secret = %q/%q, want dev/dev (§5.3)", i, c[0], c[1])
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	env := map[string]string{
		"CATLOG_SERVER_LISTEN":                  "0.0.0.0:9999",
		"CATLOG_SERVER_STATIC_DIR":              "", // set-but-empty disables static serving
		"CATLOG_DATA_DIR":                       "/var/lib/catlog",
		"CATLOG_DATA_CHECKPOINT_INTERVAL_S":     "15",
		"CATLOG_INGEST_MAX_BODY_BYTES":          "2097152",
		"CATLOG_INGEST_ACCEPTED_HTU":            "https://a.example/v1/ingest, https://b.example/v1/ingest",
		"CATLOG_AUTH_LICENSE_TTL_DAYS":          "30",
		"CATLOG_IDP_DISCORD_CLIENT_SECRET":      "s3cret",
		"CATLOG_IDP_GOOGLE_JWKS_URL":            "https://www.googleapis.com/oauth2/v3/certs",
		"CATLOG_LIMITS_RATELIMIT_PER_JKT_PER_S": "2.5",
		"CATLOG_LIMITS_RATELIMIT_BURST":         "9",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	cfg := Default()
	if err := applyEnv(&cfg, lookup); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}

	if cfg.Server.Listen != "0.0.0.0:9999" {
		t.Errorf("server.listen = %q", cfg.Server.Listen)
	}
	if cfg.Server.StaticDir != "" {
		t.Errorf("server.static_dir = %q, want empty (set-but-empty must override)", cfg.Server.StaticDir)
	}
	if cfg.Data.Dir != "/var/lib/catlog" {
		t.Errorf("data.dir = %q", cfg.Data.Dir)
	}
	if cfg.Data.CheckpointIntervalS != 15 {
		t.Errorf("data.checkpoint_interval_s = %d, want 15", cfg.Data.CheckpointIntervalS)
	}
	if cfg.Ingest.MaxBodyBytes != 2<<20 {
		t.Errorf("ingest.max_body_bytes = %d", cfg.Ingest.MaxBodyBytes)
	}
	want := []string{"https://a.example/v1/ingest", "https://b.example/v1/ingest"}
	if len(cfg.Ingest.AcceptedHTU) != len(want) {
		t.Fatalf("accepted_htu = %v, want %v", cfg.Ingest.AcceptedHTU, want)
	}
	for i := range want {
		if cfg.Ingest.AcceptedHTU[i] != want[i] {
			t.Errorf("accepted_htu[%d] = %q, want %q (surrounding spaces must be trimmed)",
				i, cfg.Ingest.AcceptedHTU[i], want[i])
		}
	}
	if cfg.Auth.LicenseTTLDays != 30 {
		t.Errorf("auth.license_ttl_days = %d", cfg.Auth.LicenseTTLDays)
	}
	if cfg.IdP.Discord.ClientSecret != "s3cret" {
		t.Errorf("idp.discord.client_secret = %q", cfg.IdP.Discord.ClientSecret)
	}
	if cfg.IdP.Google.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Errorf("idp.google.jwks_url = %q", cfg.IdP.Google.JWKSURL)
	}
	if cfg.Limits.RateLimitPerJKTPerS != 2.5 {
		t.Errorf("limits.ratelimit_per_jkt_per_s = %v", cfg.Limits.RateLimitPerJKTPerS)
	}
	if cfg.Limits.RateLimitBurst != 9 {
		t.Errorf("limits.ratelimit_burst = %d", cfg.Limits.RateLimitBurst)
	}
}

func TestEnvOverrideTypeErrors(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"CATLOG_INGEST_MAX_EVENTS", "lots"},
		{"CATLOG_LIMITS_RATELIMIT_PER_JKT_PER_S", "fast"},
	} {
		env := map[string]string{tc.key: tc.val}
		cfg := Default()
		err := applyEnv(&cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err == nil {
			t.Errorf("%s=%q accepted, want an error", tc.key, tc.val)
		} else if !strings.Contains(err.Error(), tc.key) {
			t.Errorf("err = %v, want it to name %s", err, tc.key)
		}
	}
}

// TestEnvOverridesReachEveryScalar makes the reflective walk self-checking: if
// a new field is added without a toml tag it silently loses env support, so
// assert the walk visits every leaf.
func TestEnvOverridesReachEveryScalar(t *testing.T) {
	var visited []string
	cfg := Default()
	if err := applyEnv(&cfg, func(k string) (string, bool) { visited = append(visited, k); return "", false }); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	for _, want := range []string{
		"CATLOG_SERVER_LISTEN", "CATLOG_SERVER_ADMIN_LISTEN", "CATLOG_SERVER_BASE_URL", "CATLOG_SERVER_STATIC_DIR",
		"CATLOG_DATA_DIR", "CATLOG_DATA_CHECKPOINT_INTERVAL_S",
		"CATLOG_INGEST_ACCEPTED_HTU", "CATLOG_INGEST_MAX_BODY_BYTES", "CATLOG_INGEST_MAX_EVENTS",
		"CATLOG_AUTH_LICENSE_TTL_DAYS", "CATLOG_AUTH_HANDLE_QUOTA", "CATLOG_AUTH_ISSUANCE_PER_DAY",
		"CATLOG_AUTH_MIN_ACCOUNT_AGE_DAYS",
		"CATLOG_IDP_DISCORD_AUTH_URL", "CATLOG_IDP_DISCORD_TOKEN_URL", "CATLOG_IDP_DISCORD_API_BASE",
		"CATLOG_IDP_DISCORD_CLIENT_ID", "CATLOG_IDP_DISCORD_CLIENT_SECRET",
		"CATLOG_IDP_GOOGLE_ISSUER", "CATLOG_IDP_GOOGLE_JWKS_URL",
		"CATLOG_IDP_GITHUB_API_BASE",
		"CATLOG_LIMITS_RATELIMIT_PER_JKT_PER_S", "CATLOG_LIMITS_RATELIMIT_BURST",
	} {
		if !contains(visited, want) {
			t.Errorf("%s is not reachable from the environment; visited = %v", want, visited)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	tests := map[string]func(*Config){
		"empty listen":      func(c *Config) { c.Server.Listen = "" },
		"empty base_url":    func(c *Config) { c.Server.BaseURL = "" },
		"relative base_url": func(c *Config) { c.Server.BaseURL = "127.0.0.1:8080" },
		"trailing slash":    func(c *Config) { c.Server.BaseURL = "http://127.0.0.1:8080/" },
		"empty data dir":    func(c *Config) { c.Data.Dir = "" },
		"no accepted_htu":   func(c *Config) { c.Ingest.AcceptedHTU = nil },
		"zero max_body":     func(c *Config) { c.Ingest.MaxBodyBytes = 0 },
		"zero max_events":   func(c *Config) { c.Ingest.MaxEvents = 0 },
		"zero ttl":          func(c *Config) { c.Auth.LicenseTTLDays = 0 },
		"zero handle quota": func(c *Config) { c.Auth.HandleQuota = 0 },
		"zero issuance":     func(c *Config) { c.Auth.IssuancePerDay = 0 },
		"negative age gate": func(c *Config) { c.Auth.MinAccountAgeDays = -1 },
		"zero rate":         func(c *Config) { c.Limits.RateLimitPerJKTPerS = 0 },
		"zero burst":        func(c *Config) { c.Limits.RateLimitBurst = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("Validate accepted it, want an error")
			}
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	cfg := Default()
	cfg.Data.Dir = "/srv/catlog"

	for _, tc := range []struct{ got, want string }{
		{cfg.EventsDBPath(), "/srv/catlog/events.db"},
		{cfg.ProjectionsDBPath(), "/srv/catlog/projections.db"},
		{cfg.KeysDir(), "/srv/catlog/keys"},
		{cfg.ArchiveDir(), "/srv/catlog/archive"},
	} {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}
	if got := cfg.LicenseTTL().Hours(); got != 180*24 {
		t.Errorf("LicenseTTL = %v hours, want %v", got, 180*24)
	}
	if got := cfg.CheckpointInterval().Seconds(); got != 60 {
		t.Errorf("CheckpointInterval = %v s, want 60", got)
	}
}

// TestCheckpointIntervalDisable pins the zero/negative semantics store relies
// on: the timer is off, and only DB.Close checkpoints.
func TestCheckpointIntervalDisable(t *testing.T) {
	cfg := Default()
	for _, v := range []int{0, -1} {
		cfg.Data.CheckpointIntervalS = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("checkpoint_interval_s = %d rejected: %v", v, err)
		}
		if got := cfg.CheckpointInterval(); got > 0 {
			t.Errorf("CheckpointInterval() = %v for setting %d, want <= 0 (timer off)", got, v)
		}
	}
}
