package identity

import (
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/authz"
)

// TestHandleValidationMatrix is the §12 WP3 matrix: valid, 151 characters,
// non-ASCII, reserved — plus every boundary of the §4.7 regex. The
// case-duplicate and retired cases are the store's (they need a database) and
// live in TestClaimHandleRules.
func TestHandleValidationMatrix(t *testing.T) {
	rules := NewHandleRules([]string{"Sponsor", "  spaced  "}, 5, 3, 30)

	for _, tc := range []struct {
		name   string
		handle string
		want   string // §4.9 code, "" for accepted
	}{
		// --- accepted ---
		{"plain", "whiskers_prime", ""},
		{"single character", "a", ""},
		{"single digit", "7", ""},
		{"dots dashes underscores inside", "a.b-c_d", ""},
		{"exactly 150 characters", "a" + strings.Repeat("b", 148) + "c", ""},
		{"mixed case is preserved for display", "Whiskers_Prime", ""},
		{"digits at both ends", "0catlogger9", ""},

		// --- handle_invalid: format ---
		{"empty", "", authz.CodeHandleInvalid},
		{"151 characters", strings.Repeat("a", 151), authz.CodeHandleInvalid},
		{"leading dot", ".whiskers", authz.CodeHandleInvalid},
		{"trailing dot", "whiskers.", authz.CodeHandleInvalid},
		{"leading dash", "-whiskers", authz.CodeHandleInvalid},
		{"trailing underscore", "whiskers_", authz.CodeHandleInvalid},
		{"space inside", "whiskers prime", authz.CodeHandleInvalid},
		{"slash", "whiskers/prime", authz.CodeHandleInvalid},
		{"at sign", "whiskers@catlog", authz.CodeHandleInvalid},
		{"non-ascii latin", "whiskérs", authz.CodeHandleInvalid},
		{"non-ascii cyrillic homoglyph", "whiskеrs", authz.CodeHandleInvalid}, // Cyrillic 'е'
		{"emoji", "whiskers🐈", authz.CodeHandleInvalid},
		{"newline", "whiskers\n", authz.CodeHandleInvalid},
		{"embedded NUL", "whis\x00kers", authz.CodeHandleInvalid},
		{"tab", "whis\tkers", authz.CodeHandleInvalid},

		// --- handle_reserved ---
		{"reserved: admin", "admin", authz.CodeHandleReserved},
		{"reserved: catlog", "catlog", authz.CodeHandleReserved},
		{"reserved: www", "www", authz.CodeHandleReserved},
		{"reserved is case-insensitive", "ADMIN", authz.CodeHandleReserved},
		{"reserved mixed case", "Moderator", authz.CodeHandleReserved},
		{"configured extra", "sponsor", authz.CodeHandleReserved},
		{"configured extra, original casing", "Sponsor", authz.CodeHandleReserved},
		{"configured extra is trimmed", "spaced", authz.CodeHandleReserved},
		{"a reserved word with a suffix is fine", "admin_of_cats", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, detail := rules.ValidateHandle(tc.handle)
			if code != tc.want {
				t.Errorf("ValidateHandle(%q) = %q (%s), want %q", tc.handle, code, detail, tc.want)
			}
			if code != "" && detail == "" {
				t.Error("a rejection must carry a detail a player can act on")
			}
		})
	}
}

// TestReservedListIsComplete pins the §4.7 list. Losing an entry silently would
// let someone claim `support` and impersonate the project.
func TestReservedListIsComplete(t *testing.T) {
	want := []string{
		"admin", "administrator", "catlog", "api", "root", "system",
		"mod", "moderator", "staff", "official", "support", "help", "www",
	}
	rules := NewHandleRules(nil, 5, 3, 30)
	for _, w := range want {
		if !rules.Reserve(w) {
			t.Errorf("%q is not reserved (§4.7)", w)
		}
	}
	if len(rules.Reserved) != len(want) {
		t.Errorf("reserved set has %d entries, want the %d of §4.7", len(rules.Reserved), len(want))
	}
	// Extras add, never replace.
	extra := NewHandleRules([]string{"zzz"}, 5, 3, 30)
	if !extra.Reserve("admin") || !extra.Reserve("zzz") {
		t.Error("configured extras must be added to the built-in list, not substituted for it")
	}
}

// TestHandlePatternIsByteOriented guards the property that makes the non-ASCII
// rejections above work: the regex classes are bytes, so no multi-byte rune can
// ever match a character class.
func TestHandlePatternIsByteOriented(t *testing.T) {
	for _, s := range []string{"é", "е", "🐈", "ﬀ", "​"} {
		if HandlePattern.MatchString(s) {
			t.Errorf("HandlePattern matched %q; §4.7 handles are US-ASCII", s)
		}
	}
}
