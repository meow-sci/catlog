package stats

// What a `vehicle.situation` name says about the ground under the vehicle.
//
// This is a port of the mod's `SituationInfo.Table`
// (`mod/catlog.lib/Telemetry/SituationInfo.cs`), which itself re-derives the
// game's packed enum — `value = (surfaceContact << 1) | onRails` — from the
// wire *names*, so that `catlog.lib` needs no KSA reference. The eight rows are
// the whole enum; the on-rails bit is not carried here because no board reads
// it.
//
// It is a table and not a `switch` for the same reason the mod's is: §4.2 makes
// `situation` an **open set**, and every lookup has to be total. An unknown
// name — a ninth value a future build adds, or the literal `"unknown"` the mod
// emits on a failed read — reports no contact, which keeps it off
// `landed_bodies` and `splashdowns` rather than guessing it onto them.
//
// Keep it in step with the C# table. A row that disagreed would put a board on
// one side of a landing and not the other.

// surfaceContact is how much of the surface a vehicle is touching.
type surfaceContact uint8

const (
	contactNone surfaceContact = iota
	contactTerrain
	contactOcean
	contactTerrainAndOcean
)

var situationContact = map[string]surfaceContact{
	"maneuvering": contactNone,            // 0
	"freefall":    contactNone,            // 1
	"rolling":     contactTerrain,         // 2
	"landed":      contactTerrain,         // 3
	"sailing":     contactOcean,           // 4
	"floating":    contactOcean,           // 5
	"dragging":    contactTerrainAndOcean, // 6
	"bottomed":    contactTerrainAndOcean, // 7
}

// contactOf reports the surface contact a situation name implies, and
// [contactNone] for a name this build does not know.
func contactOf(situation string) surfaceContact { return situationContact[situation] }

// knownSituation reports whether a name is one of the eight the game enum has.
//
// Distinct from `contactOf(...) == contactNone`, and the difference matters:
// `maneuvering` and `freefall` are *known* to be off the ground, while
// `"unknown"` only means catlog could not tell. `softest_touchdown` needs the
// first and must not accept the second, because a touchdown measured from a
// situation nobody could read is not a measurement.
func knownSituation(situation string) bool {
	_, ok := situationContact[situation]
	return ok
}

// hasSurfaceContact reports whether the vehicle is touching terrain, ocean or
// both.
func hasSurfaceContact(situation string) bool { return contactOf(situation) != contactNone }
