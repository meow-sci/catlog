package stats

import (
	"fmt"
	"slices"
)

// A Challenge is a board with a curated rule and an explicit start and end
// date. Definitions live in this deployed artifact, never mutable runtime
// state, so incremental projection and rebuild apply the same rules.
//
// Adding a challenge whose window is already past is valid: the next rebuild
// folds its result from retained history (PROJ-090). H2 deliberately ships no
// definitions; H4 adds the first literals alongside their folds.
type Challenge struct {
	Key   string `json:"challenge"`
	Title string `json:"title"`
	// Blurb is the rule, in one or two sentences, in the player's words.
	Blurb string `json:"blurb"`
	// Opens and Closes are Unix milliseconds in UTC, with a half-open
	// [Opens, Closes) window over the server's event receive stamp.
	Opens  int64 `json:"opens"`
	Closes int64 `json:"closes"`
	// Unit and Ascending have the same meanings as on a Board.
	Unit      string `json:"unit"`
	Ascending bool   `json:"ascending"`
	// Scope is ScopePlayer, ScopeCareer or ScopeSystem.
	Scope string `json:"scope"`
}

// challengeCatalogue is intentionally empty at the H2 boundary. Challenge
// definitions and their folds land together in H4; this slice is the single
// compile-time insertion point for those literals.
var challengeCatalogue = []Challenge{}

// Challenges returns a copy of the compile-time catalogue in display order.
func Challenges() []Challenge { return slices.Clone(challengeCatalogue) }

// ChallengeByKey returns one compile-time definition by exact key.
func ChallengeByKey(key string) (Challenge, bool) {
	return challengeByKey(challengeCatalogue, key)
}

func challengeByKey(defs []Challenge, key string) (Challenge, bool) {
	for _, challenge := range defs {
		if challenge.Key == key {
			return challenge, true
		}
	}
	return Challenge{}, false
}

// Open reports whether now falls inside the challenge window. It is a
// presentation-only answer for a future read API; folds use [Challenge.InWindow]
// with the event's receive stamp instead.
func (c Challenge) Open(nowMS int64) bool { return c.InWindow(nowMS) }

// InWindow reports whether a server-assigned event receive timestamp is in the
// half-open challenge window. It never consults time.Now or client wall time.
func (c Challenge) InWindow(recvMS int64) bool {
	return recvMS >= c.Opens && recvMS < c.Closes
}

const challengeFoldPrefix = "challenge:"

func challengeFoldNames(defs []Challenge) []string {
	names := make([]string, len(defs))
	for i, challenge := range defs {
		names[i] = challengeFoldPrefix + challenge.Key
	}
	return names
}

// ValidateChallenges validates the shipped compile-time catalogue. catlogd
// calls this before opening keys or databases so a corrupt registry cannot run.
func ValidateChallenges() error {
	folds, err := challengeFoldsFor(challengeCatalogue, challengeRules)
	if err != nil {
		return err
	}
	names := make([]string, len(folds))
	for i, fold := range folds {
		names[i] = fold.Name()
	}
	return validateChallenges(challengeCatalogue, names)
}

func validateChallenges(defs []Challenge, foldNames []string) error {
	seenKeys := make(map[string]bool, len(defs))
	for _, challenge := range defs {
		if seenKeys[challenge.Key] {
			return fmt.Errorf("stats: duplicate challenge key %q", challenge.Key)
		}
		seenKeys[challenge.Key] = true

		normalized, ok := statSuffix(challenge.Key)
		if !ok || normalized != challenge.Key {
			return fmt.Errorf("stats: invalid challenge key %q", challenge.Key)
		}
		if _, collision := Describe(challenge.Key); collision {
			return fmt.Errorf("stats: challenge key %q collides with a board key", challenge.Key)
		}
		if challenge.Opens <= 0 {
			return fmt.Errorf("stats: challenge %q opens at non-positive time %d", challenge.Key, challenge.Opens)
		}
		if challenge.Closes <= challenge.Opens {
			return fmt.Errorf("stats: challenge %q closes at %d, not after %d", challenge.Key, challenge.Closes, challenge.Opens)
		}
		if challenge.Scope != ScopePlayer && challenge.Scope != ScopeCareer && challenge.Scope != ScopeSystem {
			return fmt.Errorf("stats: challenge %q has invalid scope %q", challenge.Key, challenge.Scope)
		}
	}

	if len(foldNames) != len(defs) {
		return fmt.Errorf("stats: challenge fold count %d does not match definition count %d", len(foldNames), len(defs))
	}
	seenNames := make(map[string]bool, len(foldNames))
	for _, name := range foldNames {
		if name == "" || seenNames[name] {
			return fmt.Errorf("stats: duplicate or empty challenge fold name %q", name)
		}
		seenNames[name] = true
	}
	return nil
}
