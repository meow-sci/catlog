package identity

import (
	"regexp"
	"slices"
	"strings"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/store"
)

// HandlePattern is the §4.7 handle regex, verbatim: 1–150 characters of US-ASCII
// alphanumerics plus `.`, `_` and `-`, starting and ending alphanumeric.
//
// Go's regexp is byte-oriented for these classes, so a non-ASCII byte can never
// match — which is the point: `whiskers·prime` with a middle dot must be
// rejected, not silently normalized.
var HandlePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,148}[A-Za-z0-9])?$`)

// MaxHandleLen is the §4.7 length ceiling, stated separately from the regex so
// an over-long handle can be reported as such rather than as "does not match".
const MaxHandleLen = 150

// ReservedHandles is the §4.7 reserved list. Configuration adds to it; nothing
// removes from it.
var ReservedHandles = []string{
	"admin", "administrator", "catlog", "api", "root", "system",
	"mod", "moderator", "staff", "official", "support", "help", "www",
}

// HandleRules is the policy half of §4.7 — everything package store leaves to
// the identity layer: format, reserved words and the per-account quotas.
//
// The DB half (case-insensitive uniqueness against `handle`, permanent
// exclusion against `retired_handle`) is [store.Events.ClaimHandle]'s, and the
// two are deliberately not merged: uniqueness needs a transaction, and policy
// must be checkable without one.
type HandleRules struct {
	// Reserved is the lowercased reserved set: [ReservedHandles] plus the
	// configured extras (§4.7).
	Reserved map[string]struct{}
	// MaxHandles is the live-handle quota per account (§4.7: 5).
	MaxHandles int
	// IssuancesPerDay caps license issuances per account per 24 h, covering new
	// claims and reissues alike (§4.7: 3).
	IssuancesPerDay int
	// MinAccountAge is the account-age gate in days (§4.7: 30). Zero disables
	// it, which is what a test that does not care about ages wants.
	MinAccountAgeDays int
}

// NewHandleRules builds the rule set from configuration. extras are added to
// the built-in reserved list, case-insensitively.
func NewHandleRules(extras []string, maxHandles, issuancesPerDay, minAccountAgeDays int) HandleRules {
	reserved := make(map[string]struct{}, len(ReservedHandles)+len(extras))
	for _, w := range slices.Concat(ReservedHandles, extras) {
		if w = strings.TrimSpace(store.LC(w)); w != "" {
			reserved[w] = struct{}{}
		}
	}
	return HandleRules{
		Reserved:          reserved,
		MaxHandles:        maxHandles,
		IssuancesPerDay:   issuancesPerDay,
		MinAccountAgeDays: minAccountAgeDays,
	}
}

// ValidateHandle runs the format and reserved-word checks of §4.7 and returns
// the §4.9 code for the first failure, or "" when the handle is claimable as
// far as policy is concerned.
//
// The order matters for the message a player sees: an over-long or non-ASCII
// handle is `handle_invalid`, a well-formed one that happens to be on the
// reserved list is `handle_reserved`, and only the store can say
// `handle_taken`.
func (h HandleRules) ValidateHandle(handle string) (code, detail string) {
	switch {
	case handle == "":
		return authz.CodeHandleInvalid, "a handle is required"
	case len(handle) > MaxHandleLen:
		return authz.CodeHandleInvalid, "a handle is at most 150 characters"
	case !HandlePattern.MatchString(handle):
		return authz.CodeHandleInvalid,
			"a handle is US-ASCII letters, digits, '.', '_' and '-', starting and ending with a letter or digit"
	}
	if _, ok := h.Reserved[store.LC(handle)]; ok {
		return authz.CodeHandleReserved, "that handle is reserved"
	}
	return "", ""
}

// Reserve reports whether a handle is on the reserved list, ignoring format.
func (h HandleRules) Reserve(handle string) bool {
	_, ok := h.Reserved[store.LC(handle)]
	return ok
}
