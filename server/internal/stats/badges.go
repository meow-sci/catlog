package stats

import (
	"slices"
	"strings"
)

// Badge keys are compile-time constants for the fixed catalogue. Dynamic body
// badges are derived below with the same path hygiene as dynamic boards.
const (
	BadgeFirstFlight            = "first_flight"
	BadgeFirstStage             = "first_stage"
	BadgeFirstSpace             = "first_space"
	BadgeFirstOrbit             = "first_orbit"
	BadgeFirstLanding           = "first_landing"
	BadgeFirstRecovery          = "first_recovery"
	BadgeFirstEVA               = "first_eva"
	BadgeFirstDock              = "first_dock"
	BadgeFirstRUD               = "first_rud"
	BadgeCrewedOrbit            = "crewed_orbit"
	BadgeOrbitAndBack           = "orbit_and_back"
	BadgeDockedAfterOrbit       = "docked_after_orbit"
	BadgeCoaster                = "coaster"
	BadgeHeavyLifter            = "heavy_lifter"
	BadgeBigStack               = "big_stack"
	BadgeManyParts              = "many_parts"
	BadgeWellLit                = "well_lit"
	BadgeLithobraker            = "lithobraker"
	BadgeGroundTruth            = "ground_truth"
	BadgePressed                = "pressed"
	BadgeFeather                = "feather"
	BadgeCanyonRun              = "canyon_run"
	BadgeOldHand                = "old_hand"
	BadgeWanderer               = "wanderer"
	BadgeVoyager                = "voyager"
	BadgeGrandTour              = "grand_tour"
	BadgeGroundskeeper          = "groundskeeper"
	BadgeBeenToEveryPlanet      = "been_to_every_planet"
	BadgeBeenToEverything       = "been_to_everything"
	BadgeNotOnTheirFeet         = "not_on_their_feet"
	BadgePersistentlyUpsideDown = "persistently_upside_down"
	BadgeCrowdedCapsule         = "crowded_capsule"
	BadgeSpacewalker            = "spacewalker"
	BadgeTheLongWalk            = "the_long_walk"
	BadgeFerryService           = "ferry_service"
)

// Badge is metadata derived from a badge key alone.
type Badge struct {
	Key   string `json:"badge"`
	Title string `json:"title"`
	Blurb string `json:"blurb"`
	Group string `json:"group"`
	Tier  int    `json:"tier,omitempty"`
}

var fixedBadges = []Badge{
	{BadgeFirstFlight, "Off The Pad", "Your first flight.", "first-steps", 0},
	{BadgeFirstStage, "Separation", "You let go of something on purpose.", "first-steps", 0},
	{BadgeFirstSpace, "Above The Air", "You left the atmosphere.", "first-steps", 0},
	{BadgeFirstOrbit, "Around We Go", "You made orbit.", "first-steps", 0},
	{BadgeFirstLanding, "Wheels Down", "You put something down and it survived.", "first-steps", 0},
	{BadgeFirstRecovery, "Home Again", "You recovered a vehicle.", "first-steps", 0},
	{BadgeFirstEVA, "Outside", "A kitten went out.", "first-steps", 0},
	{BadgeFirstDock, "Well Met", "Two of your vehicles became one.", "first-steps", 0},
	{BadgeFirstRUD, "It Happens", "You lost a vehicle. Everyone does.", "first-steps", 0},

	{BadgeCrewedOrbit, "Passengers", "You brought company into orbit.", "flight", 0},
	{BadgeOrbitAndBack, "Round Trip", "You made orbit and brought the vehicle home.", "flight", 0},
	{BadgeDockedAfterOrbit, "Rendezvous", "You docked after making orbit.", "flight", 0},
	{BadgeCoaster, "Along For The Ride", "You reached another sphere without an engine.", "flight", 0},
	{BadgeHeavyLifter, "Heavy Lifter", "You put a notably heavy payload into orbit.", "flight", 0},
	{BadgeBigStack, "Tall Order", "You built a stack with ambitions.", "flight", 0},
	{BadgeManyParts, "Kit Bash", "You assembled a hundred parts into one vehicle.", "flight", 0},
	{BadgeWellLit, "Well Lit", "You have lit rather a lot of engines.", "flight", 0},

	{BadgeLithobraker, "Lithobraker", "You survived an enthusiastic arrival.", "survival", 1},
	{BadgeGroundTruth, "Ground Truth", "You survived an even more enthusiastic arrival.", "survival", 2},
	{BadgePressed, "Pressed", "You remained attached through ten g.", "survival", 0},
	{BadgeFeather, "Feather", "You landed with unusual restraint.", "survival", 0},
	{BadgeCanyonRun, "Canyon Run", "You passed within a hundred metres of the ground.", "survival", 0},
	{BadgeOldHand, "Old Hand", "You have landed often enough to look practised.", "survival", 0},

	{BadgeWanderer, "Wanderer", "You reached three worlds.", "exploration", 1},
	{BadgeVoyager, "Voyager", "You reached five worlds.", "exploration", 2},
	{BadgeGrandTour, "Grand Tour", "You reached eight worlds.", "exploration", 3},
	{BadgeGroundskeeper, "Groundskeeper", "You landed on three worlds.", "exploration", 0},
	{BadgeBeenToEveryPlanet, "Every World", "You visited every planet in this system.", "exploration", 0},
	{BadgeBeenToEverything, "Nothing Left", "You visited everything in this system.", "exploration", 0},

	{BadgeNotOnTheirFeet, "Not On Their Feet", "A kitten failed to land on their feet.", "kittens", 0},
	{BadgePersistentlyUpsideDown, "Persistently Upside Down", "Your kittens have tumbled fifty times.", "kittens", 0},
	{BadgeCrowdedCapsule, "Crowded Capsule", "You brought four kittens home at once.", "kittens", 0},
	{BadgeSpacewalker, "Spacewalker", "Your kittens have taken ten spacewalks.", "kittens", 0},
	{BadgeTheLongWalk, "The Long Walk", "A kitten spent an hour outside.", "kittens", 0},
	{BadgeFerryService, "Ferry Service", "You brought ten kittens to orbit and home.", "kittens", 0},
}

var fixedByBadge = func() map[string]Badge {
	m := make(map[string]Badge, len(fixedBadges))
	for _, b := range fixedBadges {
		m[b.Key] = b
	}
	return m
}()

// FixedBadges returns a copy of the fixed catalogue in display order.
func FixedBadges() []Badge { return slices.Clone(fixedBadges) }

type badgeFamily struct {
	prefix string
	after  string
	badge  func(key, suffix string) Badge
}

var badgeFamilies = []badgeFamily{
	{
		prefix: "reached_", after: BadgeBeenToEverything,
		badge: func(key, body string) Badge {
			return Badge{key, "Reached " + titleize(body), "You reached " + titleize(body) + ".", "exploration", 0}
		},
	},
	{
		prefix: "orbited_", after: BadgeBeenToEverything,
		badge: func(key, body string) Badge {
			return Badge{key, "Orbited " + titleize(body), "You made orbit around " + titleize(body) + ".", "exploration", 0}
		},
	},
	{
		prefix: "landed_on_", after: BadgeBeenToEverything,
		badge: func(key, body string) Badge {
			return Badge{key, "Landed on " + titleize(body), "You landed on " + titleize(body) + ".", "exploration", 0}
		},
	},
}

// BadgeFamilies returns the dynamic families in catalogue order.
func BadgeFamilies() []badgeFamily { return slices.Clone(badgeFamilies) }

// ReachedBadge returns the badge key for reaching body.
func ReachedBadge(body string) (string, bool) { return familyBadge("reached_", body) }

// OrbitedBadge returns the badge key for orbiting body.
func OrbitedBadge(body string) (string, bool) { return familyBadge("orbited_", body) }

// LandedOnBadge returns the badge key for landing on body.
func LandedOnBadge(body string) (string, bool) { return familyBadge("landed_on_", body) }

func familyBadge(prefix, value string) (string, bool) {
	suffix, ok := statSuffix(value)
	if !ok {
		return "", false
	}
	key := prefix + suffix
	if _, fixed := fixedByBadge[key]; fixed {
		return "", false
	}
	return key, true
}

// DescribeBadge derives fixed or dynamic metadata without consulting storage.
func DescribeBadge(key string) (Badge, bool) {
	if b, ok := fixedByBadge[key]; ok {
		return b, true
	}
	for _, f := range badgeFamilies {
		suffix, ok := strings.CutPrefix(key, f.prefix)
		if !ok {
			continue
		}
		normalized, valid := familyBadge(f.prefix, suffix)
		if !valid || normalized != key {
			return Badge{}, false
		}
		return f.badge(key, suffix), true
	}
	return Badge{}, false
}

// KnownBadge reports whether a fixed badge, or an earned family badge, may be
// served directly. Family catalogue visibility is the stricter BadgeCatalog.
func KnownBadge(key string, holders int64) (Badge, bool) {
	b, ok := DescribeBadge(key)
	if !ok {
		return Badge{}, false
	}
	if _, fixed := fixedByBadge[key]; fixed {
		return b, true
	}
	return b, holders > 0
}

// BadgeCatalog lists every fixed badge and qualifying family members. Families
// appear after the exploration fixed entries, in declared family order and key
// order within each family.
func BadgeCatalog(counts map[string]int64, minPlayers int) []Badge {
	if minPlayers < 1 {
		minPlayers = DefaultMinPlayers
	}
	members := make([][]Badge, len(badgeFamilies))
	for key, holders := range counts {
		if holders < int64(minPlayers) {
			continue
		}
		for i, f := range badgeFamilies {
			if !strings.HasPrefix(key, f.prefix) {
				continue
			}
			b, ok := DescribeBadge(key)
			if ok {
				members[i] = append(members[i], b)
			}
			break
		}
	}
	for i := range members {
		slices.SortFunc(members[i], func(a, b Badge) int { return strings.Compare(a.Key, b.Key) })
	}

	out := make([]Badge, 0, len(fixedBadges)+len(counts))
	for _, b := range fixedBadges {
		out = append(out, b)
		for i, f := range badgeFamilies {
			if f.after == b.Key {
				out = append(out, members[i]...)
			}
		}
	}
	return out
}
