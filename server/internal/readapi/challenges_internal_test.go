package readapi

import (
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
)

func TestChallengeStateBoundariesAndCatalogueOrdering(t *testing.T) {
	definition := stats.Challenge{Opens: 10, Closes: 20}
	for now, want := range map[int64]string{9: "upcoming", 10: "open", 19: "open", 20: "closed"} {
		if got := challengeState(definition, now); got != want {
			t.Errorf("state at %d = %q, want %q", now, got, want)
		}
	}
	if got := challengeStateOrder("open"); got != 0 {
		t.Fatalf("open order = %d", got)
	}
	rows := []ChallengeSummary{
		{Challenge: "closed-old", State: "closed", Opens: 1, Closes: 2},
		{Challenge: "upcoming-old", State: "upcoming", Opens: 5, Closes: 8},
		{Challenge: "open-old", State: "open", Opens: 3, Closes: 7},
		{Challenge: "closed-new", State: "closed", Opens: 2, Closes: 3},
		{Challenge: "open-new-a", State: "open", Opens: 4, Closes: 9},
		{Challenge: "open-new-b", State: "open", Opens: 4, Closes: 9},
		{Challenge: "upcoming-new", State: "upcoming", Opens: 6, Closes: 9},
	}
	sortChallengeSummaries(rows)
	got := make([]string, len(rows))
	for i := range rows {
		got[i] = rows[i].Challenge
	}
	want := []string{"open-new-a", "open-new-b", "open-old", "upcoming-new", "upcoming-old", "closed-new", "closed-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
