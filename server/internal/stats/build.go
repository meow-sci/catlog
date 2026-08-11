package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// BuildVersion is the version of what the folds in this package *mean*.
//
// # When to bump it
//
// **Whenever a change alters the number a fold produces from history it has
// already seen, without changing any fold's name.** A new threshold, a changed
// unit, a widened eligibility rule, a different tie-break, a corrected formula:
// all of those leave the fold registry identical and make every projection built
// by the previous binary wrong.
//
// You do *not* need to bump it for a fold that is added, removed or renamed —
// [BuildID] hashes the registry, so those are detected for free.
//
// This is the same discipline as bumping an event's `ver` when its payload
// changes, and it has the same standard of proof: same commit as the change,
// recorded in DECISIONS.md. The cost of forgetting is a board that is quietly
// short of history until somebody happens to run a rebuild.
const BuildVersion = 2

// BuildID identifies the projection build this binary produces: the projections
// schema version, [BuildVersion], and the ordered name of every registered fold.
//
// A projections.db whose stamp does not equal this is a file built by a
// different definition of the boards, and the only thing that can be said about
// its contents is that they are not this binary's answer. See
// `migrations/projections/0005_build.sql`.
func BuildID(schemaVersion int) string {
	return buildIDForNames(schemaVersion, FoldNames())
}

func buildIDForNames(schemaVersion int, names []string) string {
	h := sha256.New()
	// Field-separated and newline-terminated, so no two different registries
	// can hash to the same byte stream by concatenation.
	h.Write([]byte("catlog-projection-build\n"))
	h.Write([]byte(strconv.Itoa(schemaVersion) + "\n"))
	h.Write([]byte(strconv.Itoa(BuildVersion) + "\n"))
	for _, name := range names {
		h.Write([]byte(name + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// FoldNames is every registered fold's name, in the order the projector applies
// them: the state folds first, then the second pass.
//
// The order is part of the identity on purpose. Folds are applied in sequence
// against a shared batch, so two registries holding the same names in a
// different order are not guaranteed to produce the same projection.
func FoldNames() []string {
	folds := append(StateFolds(), SecondPassFolds()...)
	out := make([]string, 0, len(folds))
	for _, f := range folds {
		out = append(out, f.Name())
	}
	return out
}

// FoldSetSummary is a human-readable one-liner for a log line: how many folds,
// and the build they hash to.
func FoldSetSummary(schemaVersion int) string {
	names := FoldNames()
	return strconv.Itoa(len(names)) + " folds, build " + BuildID(schemaVersion) +
		" (fold version " + strconv.Itoa(BuildVersion) + ")"
}

// SameBuild reports whether a stamp read off a projections file was produced by
// this binary's folds.
func SameBuild(stamped string, schemaVersion int) bool {
	return stamped != "" && strings.EqualFold(stamped, BuildID(schemaVersion))
}
