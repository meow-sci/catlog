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
// folds its result from retained history (PROJ-090). Definitions and their
// executable rules always land together.
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

const (
	// 2026-08-10T00:00:00Z .. 2026-08-17T00:00:00Z
	week33Opens  int64 = 1_786_320_000_000
	week33Closes int64 = 1_786_924_800_000
)

// challengeCatalogue is the single compile-time insertion point for curated
// definitions. Windows are explicit on every literal: there is no mutable or
// runtime-relative challenge calendar.
var challengeCatalogue = []Challenge{
	{
		Key: "heavy_lift_week", Title: "Heavy Lift Week",
		Blurb: "Get the heaviest payload you can into orbit. The number is what the whole " +
			"vehicle weighed the moment it got there, propellant included — catlog cannot " +
			"tell the cargo from the rocket, and does not try.",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "kg", Scope: ScopeSystem,
	},
	{
		Key: "speedrun_orbit", Title: "From Scratch To Orbit",
		Blurb: "Start a save and get to orbit. The clock is the game clock, counted from the beginning of that save.",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "ms", Ascending: true, Scope: ScopeCareer,
	},
	{
		Key: "tumbleweek", Title: "Tumbleweek",
		Blurb: "The most kitten tumbles",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "tumbles", Scope: ScopePlayer,
	},
	{
		Key: "coasting_class", Title: "Coasting Class",
		Blurb: "The most distinct worlds reached in-window on flights that launched with no engine installed. " +
			"RCS thrusters and other non-engine propulsion still qualify.",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "bodies", Scope: ScopeSystem,
	},
	{
		Key: "feather_touch", Title: "Feather Touch",
		Blurb: "The gentlest surviving landing away from that system's home body",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "m/s", Ascending: true, Scope: ScopeSystem,
	},
	{
		Key: "full_house", Title: "Full House",
		Blurb: "The most kittens brought home in one piece at once",
		Opens: week33Opens, Closes: week33Closes,
		Unit: "kittens", Scope: ScopePlayer,
	},
}

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
