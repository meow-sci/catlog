package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// The three IdPs catlog supports (§4.7). mockidp implements one authorize /
// token / userinfo triple per name.
const (
	IdPDiscord = "discord"
	IdPGoogle  = "google"
	IdPGitHub  = "github"
)

// DiscordEpoch is the Discord snowflake epoch in unix ms: a snowflake's
// creation time is `(id >> 22) + DiscordEpoch` (§4.7). mockidp uses it in both
// directions — to mint a brand-new snowflake, and (in tests) to read one back.
const DiscordEpoch int64 = 1420070400000

// SnowflakeShift is the timestamp shift of a Discord snowflake.
const SnowflakeShift = 22

// Config is `server/mockidp.toml` (§5.8.1).
type Config struct {
	// ClientID and ClientSecret are what the token endpoints check. They
	// default to the dev/dev pair `catlogd.dev.toml` carries (§5.3).
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	// Users is the cast list: one "Login as …" button each.
	Users []User `toml:"user"`
}

// User is one test account.
type User struct {
	Label     string `toml:"label"`
	IdP       string `toml:"idp"`
	Sub       string `toml:"sub"`
	New       bool   `toml:"new"`
	Name      string `toml:"name"`
	CreatedAt string `toml:"created_at"`
}

// Default is the built-in cast: it mirrors `server/mockidp.toml` so mockidp
// runs with no config file at all, which is what keeps `make dev` a one-liner.
func Default() Config {
	return Config{
		ClientID:     "dev",
		ClientSecret: "dev",
		Users: []User{
			{Label: "Whiskers (Discord, old account)", IdP: IdPDiscord, Sub: "100000000000000000", Name: "whiskers"},
			{Label: "Sprocket (Discord, new account)", IdP: IdPDiscord, New: true, Name: "sprocket"},
			{Label: "Mittens (Google)", IdP: IdPGoogle, Sub: "g-user-1", Name: "Mittens"},
			{Label: "Clawdia (GitHub)", IdP: IdPGitHub, Sub: "4242", Name: "clawdia", CreatedAt: "2020-01-01T00:00:00Z"},
			{Label: "Pixel (GitHub, new account)", IdP: IdPGitHub, Sub: "4243", New: true, Name: "pixel"},
		},
	}
}

// LoadConfig reads path, or returns [Default] when path is empty. Unknown keys
// are an error: a typo in a test fixture must not silently produce a different
// cast than the one the assertions name.
func LoadConfig(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	// A file replaces the built-in cast wholesale rather than appending to it,
	// so a config that lists two users produces exactly two buttons.
	cfg.Users = nil

	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("mockidp: load %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		names := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			names = append(names, k.String())
		}
		return Config{}, fmt.Errorf("mockidp: %s has unknown keys: %s", path, strings.Join(names, ", "))
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "dev"
	}
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = "dev"
	}
	return cfg, nil
}

// Account is a [User] resolved against a concrete clock: the subject the
// provider will report, when the account was created, and the DOM id its button
// carries.
type Account struct {
	Label string
	IdP   string
	// Sub is the exact string the provider's user endpoint returns (for GitHub
	// it is rendered as a JSON number, so it must parse as an integer).
	Sub string
	// Name is the display name echoed in the provider response.
	Name string
	// CreatedAt is the account's creation instant — what catlogd's age gate
	// reads, directly for GitHub and via the snowflake for Discord (§4.7).
	CreatedAt time.Time
	// ElementID is the stable `#login-as-<slug>` DOM id WP5's playwright suite
	// clicks (§5.8.1).
	ElementID string
}

// Resolve turns the configured users into accounts as of now, minting the
// snowflakes and timestamps of the `new = true` entries so the account-age gate
// is exercised in both directions (§5.8.1).
func Resolve(cfg Config, now time.Time) ([]Account, error) {
	if len(cfg.Users) == 0 {
		return nil, errors.New("mockidp: no [[user]] entries configured")
	}

	out := make([]Account, 0, len(cfg.Users))
	seenID := map[string]string{}
	seenSub := map[string]string{}
	for i, u := range cfg.Users {
		if u.Label == "" {
			return nil, fmt.Errorf("mockidp: user %d has no label", i)
		}
		a := Account{Label: u.Label, IdP: u.IdP, Sub: u.Sub, Name: u.Name}
		a.ElementID = "login-as-" + Slug(u.Label)

		switch u.IdP {
		case IdPDiscord:
			if u.New {
				a.Sub = strconv.FormatInt((now.UnixMilli()-DiscordEpoch)<<SnowflakeShift, 10)
			}
			id, err := strconv.ParseInt(a.Sub, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("mockidp: user %q: discord sub %q is not a snowflake: %w", u.Label, a.Sub, err)
			}
			a.CreatedAt = time.UnixMilli((id >> SnowflakeShift) + DiscordEpoch).UTC()

		case IdPGoogle:
			if a.Sub == "" {
				return nil, fmt.Errorf("mockidp: user %q: google needs a sub", u.Label)
			}
			// Google publishes no account age and catlog gates on quotas only
			// (§4.7), so the timestamp is informational.
			a.CreatedAt = now.UTC()

		case IdPGitHub:
			if _, err := strconv.ParseInt(a.Sub, 10, 64); err != nil {
				return nil, fmt.Errorf("mockidp: user %q: github sub %q is not a numeric id: %w", u.Label, a.Sub, err)
			}
			switch {
			case u.New:
				a.CreatedAt = now.UTC()
			case u.CreatedAt != "":
				t, err := time.Parse(time.RFC3339, u.CreatedAt)
				if err != nil {
					return nil, fmt.Errorf("mockidp: user %q: created_at %q is not RFC 3339: %w", u.Label, u.CreatedAt, err)
				}
				a.CreatedAt = t.UTC()
			default:
				return nil, fmt.Errorf("mockidp: user %q: github needs created_at or new = true", u.Label)
			}

		default:
			return nil, fmt.Errorf("mockidp: user %q has idp %q, want discord, google or github", u.Label, u.IdP)
		}

		if prev, dup := seenID[a.ElementID]; dup {
			return nil, fmt.Errorf("mockidp: users %q and %q both slug to %s", prev, u.Label, a.ElementID)
		}
		seenID[a.ElementID] = u.Label

		key := a.IdP + ":" + a.Sub
		if prev, dup := seenSub[key]; dup {
			return nil, fmt.Errorf("mockidp: users %q and %q share the subject %s", prev, u.Label, key)
		}
		seenSub[key] = u.Label

		out = append(out, a)
	}
	return out, nil
}

// Slug lowercases a label and collapses every run of non-alphanumeric bytes to
// a single '-', which is what makes `#login-as-<slug>` predictable enough for
// a playwright selector to be written by hand (§5.8.1).
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteByte(c + ('a' - 'A'))
		default:
			dash = true
		}
	}
	return b.String()
}
