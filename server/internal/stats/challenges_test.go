package stats

import (
	"encoding/json"
	"strings"
	"testing"
)

func validTestChallenge(key string) Challenge {
	return Challenge{
		Key: key, Title: "Test", Blurb: "A test challenge.",
		Opens: 1_000, Closes: 2_000, Unit: "things", Scope: ScopePlayer,
	}
}

func TestChallengeAPIHasExactJSONAndLookup(t *testing.T) {
	c := validTestChallenge("lookup_test")
	got, ok := challengeByKey([]Challenge{c}, "lookup_test")
	if !ok || got != c {
		t.Fatalf("challengeByKey = %+v, %v; want %+v, true", got, ok, c)
	}
	if _, ok := challengeByKey([]Challenge{c}, "LOOKUP_TEST"); ok {
		t.Error("challenge lookup was not exact")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"challenge":"lookup_test","title":"Test","blurb":"A test challenge.","opens":1000,"closes":2000,"unit":"things","ascending":false,"scope":"player"}`
	if string(b) != want {
		t.Errorf("challenge JSON = %s, want %s", b, want)
	}
}

func TestChallengeCatalogueReturnsACloneAndLookupIsExact(t *testing.T) {
	want := Challenges()
	if len(want) != 6 {
		t.Fatalf("shipped %d challenge definitions, want six: %+v", len(want), want)
	}
	if _, ok := ChallengeByKey("not_shipped"); ok {
		t.Error("unknown challenge resolved")
	}
	got := Challenges()
	got[0].Title = "Changed"
	got = append(got, validTestChallenge("caller_mutation"))
	if current := Challenges(); len(current) != 6 || current[0].Title != want[0].Title {
		t.Error("caller mutated the compile-time challenge catalogue")
	}
}

func TestChallengeWindowIsHalfOpenOnTheProvidedReceiveTime(t *testing.T) {
	c := validTestChallenge("window_test")
	for _, tc := range []struct {
		name string
		at   int64
		want bool
	}{
		{"before", 999, false},
		{"opening instant", 1_000, true},
		{"inside", 1_999, true},
		{"closing instant", 2_000, false},
		{"after", 2_001, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.InWindow(tc.at); got != tc.want {
				t.Errorf("InWindow(%d) = %v, want %v", tc.at, got, tc.want)
			}
			if got := c.Open(tc.at); got != tc.want {
				t.Errorf("Open(%d) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestValidateChallengesRejectsEveryRegistryCorruption(t *testing.T) {
	base := validTestChallenge("weekly_test")
	tests := []struct {
		name  string
		defs  []Challenge
		names []string
		want  string
	}{
		{"duplicate key", []Challenge{base, base}, []string{"challenge:weekly_test", "challenge:weekly_test_2"}, "duplicate challenge key"},
		{"empty key", []Challenge{{Title: "x", Opens: 1, Closes: 2, Scope: ScopePlayer}}, []string{"challenge:"}, "invalid challenge key"},
		{"noncanonical key", []Challenge{func() Challenge { c := base; c.Key = "Weekly_Test"; return c }()}, []string{"challenge:Weekly_Test"}, "invalid challenge key"},
		{"board collision", []Challenge{func() Challenge { c := base; c.Key = StatLandings; return c }()}, []string{"challenge:" + StatLandings}, "collides with a board key"},
		{"dynamic board collision", []Challenge{func() Challenge { c := base; c.Key = "rud_future"; return c }()}, []string{"challenge:rud_future"}, "collides with a board key"},
		{"non-positive open", []Challenge{func() Challenge { c := base; c.Opens = 0; return c }()}, []string{"challenge:weekly_test"}, "non-positive time"},
		{"close before open", []Challenge{func() Challenge { c := base; c.Closes = c.Opens; return c }()}, []string{"challenge:weekly_test"}, "not after"},
		{"unknown scope", []Challenge{func() Challenge { c := base; c.Scope = "galaxy"; return c }()}, []string{"challenge:weekly_test"}, "invalid scope"},
		{"missing fold", []Challenge{base}, nil, "fold count"},
		{"duplicate fold name", []Challenge{base, validTestChallenge("other_test")}, []string{"challenge:weekly_test", "challenge:weekly_test"}, "duplicate or empty challenge fold name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChallenges(tc.defs, tc.names)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("validateChallenges() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestEveryShippedChallengeIsValid(t *testing.T) {
	if err := ValidateChallenges(); err != nil {
		t.Fatal(err)
	}
	if len(challengeFoldNames(Challenges())) != len(Challenges()) {
		t.Error("shipped challenge definitions and reserved fold identities differ")
	}
}

func TestEveryChallengeScopeValidates(t *testing.T) {
	for _, scope := range []string{ScopePlayer, ScopeCareer, ScopeSystem} {
		c := validTestChallenge("scope_" + scope)
		c.Scope = scope
		if err := validateChallenges([]Challenge{c}, challengeFoldNames([]Challenge{c})); err != nil {
			t.Errorf("scope %q: %v", scope, err)
		}
	}
}
