package stats_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/meow-sci/catlog/server/internal/stats"
)

func TestFixedBadgeCatalogueExactOrderAndMetadata(t *testing.T) {
	want := []struct {
		key, title, group string
		tier              int
	}{
		{"first_flight", "Off The Pad", "first-steps", 0}, {"first_stage", "Separation", "first-steps", 0},
		{"first_space", "Above The Air", "first-steps", 0}, {"first_orbit", "Around We Go", "first-steps", 0},
		{"first_landing", "Wheels Down", "first-steps", 0}, {"first_recovery", "Home Again", "first-steps", 0},
		{"first_eva", "Outside", "first-steps", 0}, {"first_dock", "Well Met", "first-steps", 0},
		{"first_rud", "It Happens", "first-steps", 0},
		{"crewed_orbit", "Passengers", "flight", 0}, {"orbit_and_back", "Round Trip", "flight", 0},
		{"docked_after_orbit", "Rendezvous", "flight", 0}, {"coaster", "Along For The Ride", "flight", 0},
		{"heavy_lifter", "Heavy Lifter", "flight", 0}, {"big_stack", "Tall Order", "flight", 0},
		{"many_parts", "Kit Bash", "flight", 0}, {"well_lit", "Well Lit", "flight", 0},
		{"lithobraker", "Lithobraker", "survival", 1}, {"ground_truth", "Ground Truth", "survival", 2},
		{"pressed", "Pressed", "survival", 0}, {"feather", "Feather", "survival", 0},
		{"canyon_run", "Canyon Run", "survival", 0}, {"old_hand", "Old Hand", "survival", 0},
		{"wanderer", "Wanderer", "exploration", 1}, {"voyager", "Voyager", "exploration", 2},
		{"grand_tour", "Grand Tour", "exploration", 3}, {"groundskeeper", "Groundskeeper", "exploration", 0},
		{"been_to_every_planet", "Every World", "exploration", 0}, {"been_to_everything", "Nothing Left", "exploration", 0},
		{"not_on_their_feet", "Not On Their Feet", "kittens", 0},
		{"persistently_upside_down", "Persistently Upside Down", "kittens", 0},
		{"crowded_capsule", "Crowded Capsule", "kittens", 0}, {"spacewalker", "Spacewalker", "kittens", 0},
		{"the_long_walk", "The Long Walk", "kittens", 0}, {"ferry_service", "Ferry Service", "kittens", 0},
	}
	got := stats.FixedBadges()
	if len(got) != 35 || len(got) != len(want) {
		t.Fatalf("fixed badges = %d, want 35", len(got))
	}
	keys := make(map[string]bool, len(got))
	titles := make(map[string]bool, len(got))
	for i := range want {
		if got[i].Key != want[i].key || got[i].Title != want[i].title || got[i].Group != want[i].group || got[i].Tier != want[i].tier {
			t.Errorf("badge %d = %+v, want key=%q title=%q group=%q tier=%d", i, got[i], want[i].key, want[i].title, want[i].group, want[i].tier)
		}
		if keys[got[i].Key] {
			t.Errorf("duplicate fixed badge key %q", got[i].Key)
		}
		if titles[got[i].Title] {
			t.Errorf("duplicate fixed badge title %q", got[i].Title)
		}
		keys[got[i].Key] = true
		titles[got[i].Title] = true
	}
}

func TestBadgeBlurbsAreOneDrySentence(t *testing.T) {
	for _, b := range stats.FixedBadges() {
		if b.Blurb == "" || !strings.HasSuffix(b.Blurb, ".") || strings.ContainsAny(b.Blurb, "!\n") {
			t.Errorf("%s blurb is not a plain sentence: %q", b.Key, b.Blurb)
		}
		first, _ := utf8FirstRune(b.Blurb)
		if !unicode.IsUpper(first) {
			t.Errorf("%s blurb is not sentence case: %q", b.Key, b.Blurb)
		}
	}
}

func utf8FirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func TestBadgeJSONOmitsStandaloneTier(t *testing.T) {
	b, err := json.Marshal(stats.FixedBadges()[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tier") || !strings.Contains(string(b), `"badge":"first_flight"`) {
		t.Errorf("standalone JSON = %s", b)
	}
	b, err = json.Marshal(stats.FixedBadges()[17])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tier":1`) {
		t.Errorf("tiered JSON = %s", b)
	}
}

func TestFixedBadgesReturnsAClone(t *testing.T) {
	a := stats.FixedBadges()
	a[0].Title = "Changed"
	a = append(a, stats.Badge{Key: "invented"})
	b := stats.FixedBadges()
	if len(b) != 35 || b[0].Title != "Off The Pad" {
		t.Fatalf("caller mutated fixed catalogue: %v", b[:1])
	}
}

func TestBadgeFamiliesDeriveOpenSetMetadataAndValidatePaths(t *testing.T) {
	tests := []struct {
		make       func(string) (string, bool)
		key, title string
	}{
		{stats.ReachedBadge, "reached_zephyria_prime", "Reached Zephyria Prime"},
		{stats.OrbitedBadge, "orbited_zephyria_prime", "Orbited Zephyria Prime"},
		{stats.LandedOnBadge, "landed_on_zephyria_prime", "Landed on Zephyria Prime"},
	}
	for _, tt := range tests {
		key, ok := tt.make("Zephyria_Prime")
		if !ok || key != tt.key {
			t.Fatalf("family key = %q, %v; want %q", key, ok, tt.key)
		}
		b, ok := stats.DescribeBadge(key)
		if !ok || b.Title != tt.title || b.Group != "exploration" || b.Tier != 0 || !strings.HasSuffix(b.Blurb, ".") {
			t.Errorf("DescribeBadge(%q) = %+v, %v", key, b, ok)
		}
	}
	for _, makeKey := range []func(string) (string, bool){stats.ReachedBadge, stats.OrbitedBadge, stats.LandedOnBadge} {
		if key, ok := makeKey(strings.Repeat("a", stats.MaxStatSuffixLen)); !ok || !strings.HasSuffix(key, strings.Repeat("a", stats.MaxStatSuffixLen)) {
			t.Errorf("family rejected maximum suffix: %q, %v", key, ok)
		}
	}
	for _, body := range []string{"", "bad/body", "_leading", strings.Repeat("a", stats.MaxStatSuffixLen+1)} {
		if key, ok := stats.LandedOnBadge(body); ok {
			t.Errorf("LandedOnBadge(%q) = %q, want rejection", body, key)
		}
	}
	for _, key := range []string{"orbited_Luna", "reached_bad/body", "landed_on_", strings.Repeat("a", 41), "not_a_badge"} {
		if b, ok := stats.DescribeBadge(key); ok {
			t.Errorf("DescribeBadge(%q) = %+v, want rejection", key, b)
		}
	}
}

func TestDescribeAndKnownBadge(t *testing.T) {
	if b, ok := stats.DescribeBadge(stats.BadgeFirstOrbit); !ok || b.Title != "Around We Go" {
		t.Errorf("fixed description = %+v, %v", b, ok)
	}
	if _, ok := stats.KnownBadge(stats.BadgeFirstOrbit, 0); !ok {
		t.Error("fixed badge is not known while empty")
	}
	if _, ok := stats.KnownBadge("orbited_luna", 0); ok {
		t.Error("unearned family badge is known")
	}
	if _, ok := stats.KnownBadge("orbited_luna", 1); !ok {
		t.Error("earned family badge is not known")
	}
}

func TestBadgeCatalogThresholdDedupAndDeterministicPlacement(t *testing.T) {
	counts := map[string]int64{
		"reached_duna": 2, "reached_luna": 1,
		"orbited_duna": 2, "orbited_luna": 2,
		"landed_on_luna": 2, "not_a_badge": 99,
		stats.BadgeFirstOrbit: 99,
	}
	before := maps.Clone(counts)
	got := stats.BadgeCatalog(counts, 2)
	if !maps.Equal(counts, before) {
		t.Fatalf("BadgeCatalog mutated counts: %v", counts)
	}
	if len(got) != 39 {
		t.Fatalf("catalog length = %d, want 35 fixed + 4 family", len(got))
	}
	var keys []string
	for _, b := range got {
		keys = append(keys, b.Key)
	}
	anchor := slices.Index(keys, stats.BadgeBeenToEverything)
	wantAfter := []string{"reached_duna", "orbited_duna", "orbited_luna", "landed_on_luna", stats.BadgeNotOnTheirFeet}
	if anchor < 0 || !slices.Equal(keys[anchor+1:anchor+6], wantAfter) {
		t.Fatalf("family placement = %v, want %v", keys[max(0, anchor):min(len(keys), anchor+7)], wantAfter)
	}
	again := stats.BadgeCatalog(counts, 2)
	if !slices.Equal(got, again) {
		t.Error("catalog order is not deterministic")
	}
	defaults := stats.BadgeCatalog(counts, 0)
	if !slices.Equal(got, defaults) {
		t.Error("nonpositive minPlayers did not use DefaultMinPlayers")
	}
	low := stats.BadgeCatalog(counts, 1)
	seenLuna := 0
	for _, b := range low {
		if b.Key == "reached_luna" {
			seenLuna++
		}
	}
	if seenLuna != 1 {
		t.Errorf("single-holder family member appears %d times", seenLuna)
	}
}
