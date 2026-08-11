package stats

// The scopes a board can be ranked in.
//
// `player` is `player_stat` and is what every existing URL means. `career` is
// `career_stat`, which ranks (player, save) pairs: one row per save, so a player
// with five saves legitimately holds five rows on a board and their best one
// sorts to the top on its merits. `system` is `system_stat`, which ranks
// (player, celestial system) pairs — the scope a body board actually wants,
// because a lifetime board for a *name* merges systems by construction.
//
// A scope is a **dimension of a board, not a board** — the same argument periods
// settled (PROJ-042). `GET /v1/leaderboards` stays one row per board and each row
// publishes the scopes it can be read in; `?scope=` selects one.
const (
	ScopePlayer = "player"
	ScopeCareer = "career"
	// ScopeSystem ranks (player, celestial system) pairs. It is the only
	// COMPARABLE scope for anything derived from a body name: KSA's system is
	// replaceable XML content, so two players who both reached something called
	// `luna` may not have reached the same object (§3.15, §3.18).
	ScopeSystem = "system"
)

// Scopes returns every value `?scope=` accepts, `player` first.
func Scopes() []string { return []string{ScopePlayer, ScopeCareer, ScopeSystem} }

// ValidScope reports whether s is a scope the API serves. The empty string is
// `player`, so an absent parameter keeps the semantic default.
func ValidScope(s string) (string, bool) {
	if s == "" {
		return ScopePlayer, true
	}
	for _, known := range Scopes() {
		if s == known {
			return s, true
		}
	}
	return "", false
}
