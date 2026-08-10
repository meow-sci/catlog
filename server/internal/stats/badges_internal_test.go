package stats

import (
	"strings"
	"testing"
)

func TestBadgeFamiliesHaveExactOrderPlacementAndReturnAClone(t *testing.T) {
	got := BadgeFamilies()
	if len(got) != 3 {
		t.Fatalf("badge families = %d, want 3", len(got))
	}
	want := []string{"reached_", "orbited_", "landed_on_"}
	for i := range want {
		if got[i].prefix != want[i] || got[i].after != BadgeBeenToEverything {
			t.Errorf("family %d = prefix %q after %q, want %q after %q", i, got[i].prefix, got[i].after, want[i], BadgeBeenToEverything)
		}
	}
	got[0].prefix = "changed_"
	if BadgeFamilies()[0].prefix != "reached_" {
		t.Error("caller mutated badge family registry")
	}
}

func TestFamilyBadgeRejectsFixedCollisionsAndUsesStatSuffixBoundary(t *testing.T) {
	if key, ok := familyBadge("first_", "flight"); ok {
		t.Errorf("fixed collision produced %q", key)
	}
	if key, ok := familyBadge("reached_", strings.Repeat("a", MaxStatSuffixLen)); !ok || len(key) != len("reached_")+MaxStatSuffixLen {
		t.Errorf("maximum suffix key = %q, %v", key, ok)
	}
	if key, ok := familyBadge("reached_", strings.Repeat("a", MaxStatSuffixLen+1)); ok {
		t.Errorf("overlong suffix produced %q", key)
	}
}
