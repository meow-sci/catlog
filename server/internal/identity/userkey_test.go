package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/keys"
)

// fixedPepperKeys writes a known pepper into a fresh keys directory and loads
// the set over it, so user_key derivation is deterministic and can be pinned to
// literal vectors.
func fixedPepperKeys(t *testing.T) *keys.Set {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create keys dir: %v", err)
	}
	pepper := make([]byte, keys.SecretLen)
	for i := range pepper {
		pepper[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, keys.PepperFile), pepper, 0o600); err != nil {
		t.Fatalf("write pepper: %v", err)
	}
	set, err := keys.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	return set
}

// TestUserKeyDerivationVectors pins D17 to literal values: user_key is
// HMAC-SHA256(pepper, "<idp>:<stable-subject>"), and the subject string is
// assembled exactly as §4.7's table says.
//
// These are computed against a pepper of the bytes 0x00…0x1f. If one of them
// changes, every account in every deployment has just been orphaned — which is
// precisely why they are written down rather than recomputed.
func TestUserKeyDerivationVectors(t *testing.T) {
	set := fixedPepperKeys(t)

	for _, tc := range []struct {
		idp     string
		subject string
		want    string
	}{
		// The aged Discord snowflake mockidp ships (§5.8.1).
		{IdPDiscord, "100000000000000000", "Iz2eMwd3OHpZYYOPIB-JRYG8407sYNMgVaI2aVvTItc"},
		{IdPGoogle, "g-user-1", "FvoqqNO3bJTWfcpYNo9SiptEXa8KkVDY-CF1vkfSEfk"},
		{IdPGitHub, "4242", "3EVPdXLt1DN36X49Z4-tw8eHs_K9-j-9E8W9uAASo9U"},
		// The synthetic dev IdP the admin-issue path uses (§5.9).
		{"dev", "whiskers_prime", "EIZJsgH5NjGWhqp_VxFmvMvOOErOspbl68CfB59nhIw"},
	} {
		t.Run(tc.idp+":"+tc.subject, func(t *testing.T) {
			if got := set.UserKey(tc.idp, tc.subject).B64U(); got != tc.want {
				t.Errorf("user_key = %s, want %s", got, tc.want)
			}
			// The two entry points must agree, or the admin path and the login
			// path would derive different keys for the same account.
			if got := set.UserKeyFromSubject(keys.SubjectString(tc.idp, tc.subject)).B64U(); got != tc.want {
				t.Errorf("UserKeyFromSubject = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestUserKeySeparatesIdPs is D10: the same subject string at two providers is
// two different accounts, never merged.
func TestUserKeySeparatesIdPs(t *testing.T) {
	set := fixedPepperKeys(t)
	if set.UserKey(IdPDiscord, "4242") == set.UserKey(IdPGitHub, "4242") {
		t.Error("discord:4242 and github:4242 derive the same user_key; accounts would merge across IdPs (D10)")
	}
	// §4.7's subject string is `"<idp>:" + subject`, which is unambiguous only
	// because no IdP name contains a colon. That is a property of the closed
	// set of names, so it is worth asserting rather than assuming.
	for _, idp := range []string{IdPDiscord, IdPGoogle, IdPGitHub, "dev"} {
		if strings.Contains(idp, ":") {
			t.Errorf("idp name %q contains a colon; the §4.7 subject string would be ambiguous", idp)
		}
	}
}

// TestSnowflakeTime pins the §4.7 Discord age computation:
// created_ms = (id >> 22) + 1420070400000.
func TestSnowflakeTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		snowflake string
		wantMS    int64
	}{
		{"epoch", "0", DiscordEpoch},
		// One millisecond after the epoch: the low 22 bits are worker/sequence
		// noise and must be shifted away, not rounded.
		{"one ms", "4194304", DiscordEpoch + 1},
		{"noise below the shift is discarded", "4194303", DiscordEpoch},
		// mockidp's aged account (§5.8.1): 2015-10-03T22:44:17.910Z.
		{"mockidp aged account", "100000000000000000", 1443912257910},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SnowflakeTime(tc.snowflake)
			if err != nil {
				t.Fatalf("SnowflakeTime(%s): %v", tc.snowflake, err)
			}
			if got.UnixMilli() != tc.wantMS {
				t.Errorf("SnowflakeTime(%s) = %d ms (%s), want %d ms",
					tc.snowflake, got.UnixMilli(), got.Format(time.RFC3339), tc.wantMS)
			}
		})
	}

	// A snowflake with the high bit set must not read as a negative time.
	got, err := SnowflakeTime("18446744073709551615") // 2^64-1
	if err != nil {
		t.Fatalf("max snowflake: %v", err)
	}
	if got.Before(time.UnixMilli(DiscordEpoch)) {
		t.Errorf("max snowflake read as %s, which is before the Discord epoch", got)
	}

	for _, bad := range []string{"", "not-a-number", "-1", "1e5", "0x10"} {
		if _, err := SnowflakeTime(bad); err == nil {
			t.Errorf("SnowflakeTime(%q) succeeded; a garbage id must not read as an ancient account", bad)
		}
	}
}

// TestAccountAgeGate exercises §4.7's gate in both directions for each IdP,
// which is the reason mockidp ships an aged account and a minted-now one.
func TestAccountAgeGate(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s := &Server{
		rules: HandleRules{MinAccountAgeDays: 30},
		deps:  Deps{Now: func() time.Time { return now }},
	}

	for _, tc := range []struct {
		name     string
		subject  Subject
		wantCode string
	}{
		{"aged discord account", Subject{ID: "1", CreatedAt: now.AddDate(0, 0, -365)}, ""},
		{"exactly 30 days", Subject{ID: "1", CreatedAt: now.AddDate(0, 0, -30)}, ""},
		{"29 days", Subject{ID: "1", CreatedAt: now.AddDate(0, 0, -29)}, "account_too_new"},
		{"minted now", Subject{ID: "1", CreatedAt: now}, "account_too_new"},
		// Google publishes no creation time; §4.7 gates it on quotas only.
		{"google has no age", Subject{ID: "g-user-1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := s.checkAccountAge(tc.subject)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}

	// A zero threshold disables the gate — what a test fixture wants.
	off := &Server{rules: HandleRules{}, deps: Deps{Now: func() time.Time { return now }}}
	if code, _ := off.checkAccountAge(Subject{ID: "1", CreatedAt: now}); code != "" {
		t.Errorf("min_account_age_days = 0 still gated: %s", code)
	}
}
