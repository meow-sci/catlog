// Package seed builds and installs the deterministic demo dataset behind
// `POST /admin/seed` and `catlogctl seed` (§5.9).
//
// # What it is for
//
// WP5's pages and WP7's assertions both need a server that already has
// something on its boards. Rather than script gameplay, seed writes a fixed
// history of real §4.2 events for three synthetic dev players — `demo_ace`,
// `demo_tumbler`, `demo_crasher` — chosen so that between them they set a record
// on every launch board, including the ones nobody hits by accident (a survived
// lithobrake, all six RUD causes, a flagged flight that must score nothing), and
// genuinely earn representative fixed, tier and body-family badges. When the
// caller fixes the server receive clock inside Week 33, the same ordinary
// history also exercises every shipped challenge rule.
//
// # Why it is deterministic
//
// Every event id is derived from SHA-256 of a fixed string, and every timestamp
// counts up from a fixed epoch, so seeding twice inserts the same rows twice and
// the `(player_id, event_id)` dedup index (D19) turns the second run into a
// no-op. That makes `make seed` safe to run repeatedly, and it makes the board
// values something a test can assert against by literal value.
//
// It is not a fixture for the fold tests: those build their own events, because
// a golden test that shares its input with the demo data stops being a test of
// the rule and becomes a test of the demo data.
package seed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// IdP is the `idp` the demo players are recorded under — the same synthetic
// namespace `POST /admin/issue` uses (§5.9), so demo accounts can never collide
// with a real one and are trivial to find and delete.
const IdP = "dev"

// The three demo handles (§5.9).
const (
	HandleAce     = "demo_ace"
	HandleTumbler = "demo_tumbler"
	HandleCrasher = "demo_crasher"
	// DemoSystemHash is the shared system identity reported by the seeded
	// players' second saves. Their first saves deliberately predate discovery,
	// so the saves page exercises both a friendly system and an unknown one.
	DemoSystemHash    = "ADRTaA6cER7uqfoM_p880GQ6gEevTrhCycd44NRSQ_I"
	demoAltSystemHash = "Hav5mtEeCT3_I8RHVAo74PDa8rXAupy55iZ3Rj61r24"
)

// Handles lists the demo handles in the order they are created.
func Handles() []string { return []string{HandleAce, HandleTumbler, HandleCrasher} }

// RUDCauses are the §4.2 `vehicle.rud.cause` values the demo dataset flies.
//
// This is fixture data, not an allow-list: the server holds no list of causes at
// all any more, and `rud_<cause>` boards come into existence because a cause
// appeared in the event stream (server/internal/stats/boards.go). It lives here
// so the demo covers the causes the game ships today.
var RUDCauses = []string{
	"ground_impact",
	"ocean_impact",
	"collision",
	"excessive_g_force",
	"aerodynamic_forces",
	"hydrodynamic_forces",
}

// StockBodyRun is the interplanetary SOI chain the demo flies, using KSA's own
// body names: a lunar transfer never leaves Earth's SOI, and an interplanetary
// cruise passes through the star's.
//
// Two demo players fly it, on purpose. A `fastest_to_<body>` board is only
// *listed* once two distinct players are on it (stats.Catalog), and a body-family
// badge has the same two-player publication gate. A demo where one player had
// been everywhere alone would show neither dynamic surface.
var StockBodyRun = []stats.VehicleSOI{
	{FromBody: "earth", ToBody: "luna"},
	{FromBody: "luna", ToBody: "sol"},
	{FromBody: "sol", ToBody: "mars"},
}

// EpochMS is the fixed wall-clock the dataset counts from: 2026-01-01T00:00:00Z.
// A fixed epoch means the events sort the same way on every machine and the
// derived ULIDs are byte-identical run to run.
const EpochMS int64 = 1767225600000

// ChallengeRecvMS is the fixed receive clock used by the throwaway e2e
// server before it installs this dataset. It falls inside the six shipped
// Week 33 challenge windows; event wall times remain based on EpochMS above.
const ChallengeRecvMS int64 = 1786665600000

// Result reports what a seed run did.
type Result struct {
	Players  []string `json:"players"`
	Events   int      `json:"events"`
	Accepted int      `json:"accepted"`
	Deduped  int      `json:"deduped"`
}

// PlayerData is one demo player's fixed history.
type PlayerData struct {
	Handle string
	Events []store.Event
}

// Dataset returns the demo history. Pure: no clock, no randomness, no I/O.
func Dataset() []PlayerData {
	return []PlayerData{ace(), tumbler(), crasher()}
}

// Apply installs the dataset into events.db: it creates the three dev players,
// claims their handles if free, and inserts their events.
//
// It is idempotent. Re-running reports every event as deduped, because the ids
// are derived rather than minted (D19).
//
// The caller must hold the admin write mutex (§5.4): this writes to events.db
// outside the ingest writer goroutine, which is exactly what that mutex exists
// to serialize.
func Apply(ctx context.Context, events *store.Events, keySet *keys.Set, now int64) (Result, error) {
	if keySet == nil {
		return Result{}, fmt.Errorf("seed: a key set is required to derive demo user_keys")
	}
	var res Result
	for _, p := range Dataset() {
		userKey := keySet.UserKey(IdP, p.Handle)
		playerID, err := events.EnsurePlayer(ctx, nil, userKey, IdP, now)
		if err != nil {
			return Result{}, fmt.Errorf("seed: create %s: %w", p.Handle, err)
		}
		switch existing, err := events.HandleByLC(ctx, p.Handle); {
		case err == nil && existing.PlayerID != playerID:
			return Result{}, fmt.Errorf("seed: handle %q belongs to another player", p.Handle)
		case err == nil:
			// already claimed by this demo player
		default:
			if err := events.ClaimHandle(ctx, playerID, p.Handle, now); err != nil {
				return Result{}, fmt.Errorf("seed: claim %q: %w", p.Handle, err)
			}
		}

		accepted, deduped, err := events.InsertEvents(ctx, nil, playerID, p.Events)
		if err != nil {
			return Result{}, fmt.Errorf("seed: insert %s events: %w", p.Handle, err)
		}
		res.Players = append(res.Players, p.Handle)
		res.Events += len(p.Events)
		res.Accepted += accepted
		res.Deduped += deduped
	}
	return res, nil
}

// --- the dataset -------------------------------------------------------------

// ace flies competently: two orbits, a docking, a clean recovery. It owns the
// speed and g boards.
func ace() PlayerData {
	b := newBuilder(HandleAce)
	noEngines := 0
	b.session()
	b.startFlight(1, stats.FlightStarted{
		VehicleName: "Whisker IX", Body: "kerbin", MassKg: 42000, PartCount: 34, CrewCount: 3,
	})
	b.add("vehicle.staging", stats.VehicleStaging{StageIndex: 0})
	b.add("vehicle.staging", stats.VehicleStaging{StageIndex: 1})
	b.add("vehicle.staging", stats.VehicleStaging{StageIndex: 2})
	b.add("telemetry.window", window("kerbin", 2410, 7820, 4.2))
	b.add("vehicle.orbit", stats.VehicleOrbit{
		Phase: "achieved", Body: "kerbin", ApM: 320000, PeM: 295000, Ecc: 0.002, IncDeg: 28.5,
	})
	b.add("vehicle.soi", stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"})
	b.add("vehicle.orbit", stats.VehicleOrbit{
		Phase: "achieved", Body: "mun", ApM: 42000, PeM: 39000, Ecc: 0.018, IncDeg: 4.1,
	})
	b.add("vehicle.docked", stats.VehicleDock{OtherFlight: ids.String(seedULID("docking-target", 1))})
	b.add("telemetry.window", window("mun", 640, 9450, 6.8))
	b.add("vehicle.landed", landing("mun", 2.4))
	b.add("vehicle.landed", landing("mun", 1.8))
	b.endFlight(stats.FlightEnded{Reason: "recovered", CrewCount: 3})
	// A second save, flown fast and using the stock KSA body names — this is what
	// puts `demo_ace` on `fastest_to_orbit` and the per-body boards. Its clock
	// restarts near zero because sim_t is seconds since *this* career began.
	b.newCareerInSystem(2, 0)
	b.startFlight(9, stats.FlightStarted{
		VehicleName: "Direct Ascent", Body: "earth", MassKg: 51000, PartCount: 41, CrewCount: 2,
		EngineCount: &noEngines,
	})
	b.add("vehicle.orbit", stats.VehicleOrbit{
		Phase: "achieved", Body: "earth", MassKg: 51000,
		ApM: 410000, PeM: 402000, Ecc: 0.001, IncDeg: 51.6,
	})
	for _, soi := range StockBodyRun {
		b.add("vehicle.soi", soi)
	}
	b.add("vehicle.landed", landing("mars", 3.1))
	b.endFlight(stats.FlightEnded{Reason: "recovered", CrewCount: 2})
	// Loading this save again under different system metadata makes the demo
	// exercise both provenance qualifications on the public save pages. The
	// first discovered identity remains the friendly label for the save.
	b.reloadCareerInSystem(3, 5, demoAltSystemHash, "SolDense", "Sol Dense")

	b.roster(
		stats.RosterKitten{Kid: "ace0000000000001", Name: "Comet", TravelledM: 1_820_000, FastestMs: 29_812, Missions: 4, MissionTimeS: 41200, KIA: false},
		stats.RosterKitten{Kid: "ace0000000000002", Name: "Nimbus", TravelledM: 1_410_000, FastestMs: 29_804, Missions: 3, MissionTimeS: 33100, KIA: false},
		stats.RosterKitten{Kid: "ace0000000000003", Name: "Pilot", TravelledM: 980_000, FastestMs: 29_798, Missions: 2, MissionTimeS: 21050, KIA: false},
	)
	return b.done()
}

// tumbler goes EVA and falls over a lot. It owns `kitten_tumbles` and
// `soi_bodies`.
func tumbler() PlayerData {
	b := newBuilder(HandleTumbler)
	b.session()
	b.startFlight(1, stats.FlightStarted{
		VehicleName: "Tumbleweed", Body: "mun", MassKg: 3100, PartCount: 12, CrewCount: 2,
	})
	b.add("telemetry.window", window("mun", 18, 1620, 1.1))
	b.add("vehicle.soi", stats.VehicleSOI{FromBody: "kerbin", ToBody: "mun"})
	b.add("vehicle.soi", stats.VehicleSOI{FromBody: "mun", ToBody: "minmus"})
	b.add("kitten.eva_start", map[string]any{"kid": "tum0000000000001", "name": "Bramble"})
	for i, speed := range []float64{7.2, 8.9, 6.6, 11.4} {
		b.add("kitten.tumble", stats.KittenTumble{
			Kid: "tum0000000000001", Name: "Bramble", SpeedMs: speed,
			Body: []string{"mun", "mun", "minmus", "minmus"}[i], From: []string{"airborne", "grounded", "airborne", "grounded"}[i],
		})
	}
	b.add("kitten.eva_end", map[string]any{"kid": "tum0000000000001", "name": "Bramble", "duration_s": 640.5})
	b.endFlight(stats.FlightEnded{Reason: "recovered", CrewCount: 2})

	// A second flier for every `rud_<cause>` board. `demo_crasher` sets them all
	// too; a per-cause board is only listed once two distinct players are on it
	// (stats.Catalog), so without this the demo index would show none of them.
	for i, cause := range RUDCauses {
		b.startFlight(2+i, stats.FlightStarted{
			VehicleName: "Bad Idea " + strconv.Itoa(i+1), Body: "mun", MassKg: 2200, PartCount: 9, CrewCount: 0,
		})
		b.add("vehicle.rud", stats.VehicleRUD{
			Cause: cause, PeakG: 6 + float64(i), PeakQPa: 21000 + float64(i)*900,
			SpeedMs: 120 + float64(i)*30, AltitudeM: 900 - float64(i)*100, Body: "mun", CrewCount: 0,
		})
		b.endFlight(stats.FlightEnded{Reason: "destroyed", CrewCount: 0})
	}

	b.roster(
		stats.RosterKitten{Kid: "tum0000000000001", Name: "Bramble", TravelledM: 620_000, FastestMs: 29_790, Missions: 6, MissionTimeS: 51000, KIA: false},
		stats.RosterKitten{Kid: "tum0000000000002", Name: "Sorrel", TravelledM: 310_000, FastestMs: 29_781, Missions: 2, MissionTimeS: 14400, KIA: false},
	)
	return b.done()
}

// crasher takes vehicles apart in every documented way, survives one
// spectacular lithobrake, and cheats once — the flagged flight scores nothing,
// which is what makes it worth seeding.
func crasher() PlayerData {
	b := newBuilder(HandleCrasher)
	b.session()

	// The record everyone comes to see (§5.6's own example: 214 m/s on duna).
	b.startFlight(1, stats.FlightStarted{
		VehicleName: "Lawn Dart", Body: "duna", MassKg: 8200, PartCount: 19, CrewCount: 1,
	})
	b.add("telemetry.window", window("duna", 782, 3120, 9.6))
	b.add("vehicle.impact", stats.VehicleImpact{
		SpeedMs: 214, EnergyJ: 4.8e7, Survived: true, LaunchPad: false, Body: "duna", CrewCount: 1,
	})
	b.add("vehicle.landed", landing("duna", 4.2))
	b.endFlight(stats.FlightEnded{Reason: "recovered", CrewCount: 1})

	// One flight per §4.2 RUD cause, so every `rud_<cause>` board has an entry.
	for i, cause := range RUDCauses {
		b.startFlight(2+i, stats.FlightStarted{
			VehicleName: "Test Article " + strconv.Itoa(i+1), Body: "kerbin", MassKg: 5400, PartCount: 15, CrewCount: 0,
		})
		b.add("vehicle.rud", stats.VehicleRUD{
			Cause: cause, PeakG: 12 + float64(i), PeakQPa: 48000 + float64(i)*1000,
			SpeedMs: 300 + float64(i)*40, AltitudeM: 2400 - float64(i)*200, Body: "kerbin", CrewCount: 0,
		})
		b.endFlight(stats.FlightEnded{Reason: "destroyed", CrewCount: 0})
	}

	// The cheated flight. The flag is emitted *before* the impact so the
	// incremental fold already excludes it — the seeded database is meant to be
	// the canonical answer, not a demonstration of the incremental path's known
	// blind spot. That blind spot, and the rebuild that heals it, is covered by
	// the projector tests instead.
	b.startFlight(2+len(RUDCauses), stats.FlightStarted{
		VehicleName: "Definitely Legitimate", Body: "kerbin", MassKg: 1200, PartCount: 4, CrewCount: 1,
	})
	b.add("flight.flagged", stats.FlightFlagged{
		Flag: "teleport", Detail: "position moved 4.2e6 m in one frame",
	})
	b.add("vehicle.impact", stats.VehicleImpact{
		SpeedMs: 999, EnergyJ: 9.9e9, Survived: true, LaunchPad: false, Body: "kerbin", CrewCount: 1,
	})
	b.add("telemetry.window", window("kerbin", 9999, 99999, 99.9))
	b.endFlight(stats.FlightEnded{Reason: "recovered", CrewCount: 1})

	// A second save, flown slowly to the same stock bodies `demo_ace` visited.
	// It is what puts two players on `fastest_to_luna`/`_sol`/`_mars`, which is
	// what makes those boards appear in the index at all (stats.Catalog) — and
	// `demo_ace` still owns every one of them, because slower is worse here.
	b.newCareerInSystem(2, 500)
	b.startFlight(20, stats.FlightStarted{
		VehicleName: "Slow Boat", Body: "earth", MassKg: 47000, PartCount: 38, CrewCount: 1,
	})
	for _, soi := range StockBodyRun {
		b.add("vehicle.soi", soi)
	}
	b.add("vehicle.landed", landing("mars", 8.6))
	b.endFlight(stats.FlightEnded{Reason: "despawned", CrewCount: 0})

	b.roster(
		stats.RosterKitten{Kid: "cra0000000000001", Name: "Ferro", TravelledM: 205_000, FastestMs: 29_772, Missions: 8, MissionTimeS: 9200, KIA: false},
	)
	return b.done()
}

// window builds a telemetry.window payload with the aggregates a board reads.
func window(body string, surfaceMax, orbitalMax, peakG float64) stats.TelemetryWindow {
	g := peakG
	q := peakG * 5200
	return stats.TelemetryWindow{
		T0Sim: 0, T1Sim: 30, N: 60, Body: body,
		AltM:           stats.Agg{Min: 0, Max: 120000, Mean: 41000, Last: 118000},
		SurfaceSpeedMs: stats.Agg{Min: 0, Max: surfaceMax, Mean: surfaceMax / 2, Last: surfaceMax * 0.8},
		OrbitalSpeedMs: stats.Agg{Min: 0, Max: orbitalMax, Mean: orbitalMax / 2, Last: orbitalMax * 0.9},
		AccelMs2:       stats.Agg{Min: 0, Max: peakG * 9.81, Mean: peakG * 3, Last: peakG * 2},
		PeakG:          &g,
		MaxQPa:         &q,
		MassKgLast:     18400,
	}
}

func landing(body string, verticalSpeed float64) stats.VehicleLanded {
	return stats.VehicleLanded{
		Body: body, VerticalSpeedMs: verticalSpeed, HorizontalSpeedMs: verticalSpeed / 2,
		CrewCount: 1, Survived: true,
	}
}

// --- builder -----------------------------------------------------------------

// builder assembles one player's history, assigning derived ids and a
// monotonically advancing clock as it goes.
type builder struct {
	handle    string
	sessionID ids.ID
	career    string
	flight    ids.ID
	n         int
	simT      float64
	evs       []store.Event
}

func newBuilder(handle string) *builder {
	return &builder{
		handle:    handle,
		sessionID: seedULID(handle+":session", 1),
		career:    seedCareer(handle, 1),
	}
}

func (b *builder) session() {
	b.flight = ids.Zero
	b.add("session.started", stats.SessionStarted{
		ModVer: "0.1.0", GameBuild: "2026.8.5.5168", Install: ids.String(seedULID(b.handle+":install", 1)),
	})
}

// career starts a second save for this player: a fresh session, a fresh career
// id and a clock that restarts near zero, because `sim_t` counts seconds since
// *that* career began (§4.1). It is what makes the demo dataset exercise the
// career-time boards honestly rather than by accident.
func (b *builder) newCareer(n int, simT float64) {
	b.switchCareer(n, simT)
	b.session()
}

// newCareerInSystem preserves the shipped ordering contract: the system
// identity is known before session.started announces the loaded game.
func (b *builder) newCareerInSystem(n int, simT float64) {
	b.switchCareer(n, simT)
	b.discoverSystem(DemoSystemHash, "Sol", "Sol")
	b.session()
}

func (b *builder) reloadCareerInSystem(session int, simT float64, hash, id, name string) {
	b.sessionID = seedULID(b.handle+":session", session)
	b.simT = simT
	b.discoverSystem(hash, id, name)
	b.session()
}

func (b *builder) switchCareer(n int, simT float64) {
	b.sessionID = seedULID(b.handle+":session", n)
	b.career = seedCareer(b.handle, n)
	b.simT = simT
}

// discoverSystem gives a save its friendly content identity without advancing
// gameplay time: surveying the loaded XML is metadata work, not flying. The
// event still has a valid sim_t, then the next gameplay event resumes the same
// clock reading.
func (b *builder) discoverSystem(hash, id, name string) {
	clock := b.simT
	b.add("system.discovered", stats.SystemDiscovered{
		System: hash, ID: id, Name: name, Home: "earth", Complete: false,
	})
	b.simT = clock
}

func (b *builder) startFlight(n int, p stats.FlightStarted) {
	b.flight = seedULID(b.handle+":flight", n)
	b.add("flight.started", p)
}

func (b *builder) endFlight(p stats.FlightEnded) {
	b.add("flight.ended", p)
	b.flight = ids.Zero
}

// roster is emitted with no flight, exactly as §4.1 requires.
func (b *builder) roster(kittens ...stats.RosterKitten) {
	b.flight = ids.Zero
	b.add("roster.snapshot", stats.RosterSnapshot{Kittens: kittens})
}

func (b *builder) add(typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		// Only reachable by a programming error in this file: every payload is
		// a struct or a literal map of JSON-safe values.
		panic("seed: " + typ + ": " + err.Error())
	}
	b.n++
	b.simT += 12.5
	b.evs = append(b.evs, store.Event{
		ID:        seedULID(b.handle, b.n),
		FlightID:  b.flight,
		SessionID: b.sessionID,
		Career:    b.career,
		Type:      typ,
		Ver:       1,
		SimTime:   nullFloat(b.simT),
		WallTime:  EpochMS + int64(b.n)*1000,
		Payload:   raw,
	})
}

func (b *builder) done() PlayerData {
	return PlayerData{Handle: b.handle, Events: b.evs}
}

// seedCareer derives a §4.1 career id (16 lowercase Crockford base32
// characters) from a handle, the same shape the mod produces. Stable across
// machines and runs for the same reason seedULID is.
func seedCareer(handle string, n int) string {
	const crockford = "0123456789abcdefghjkmnpqrstvwxyz"
	sum := sha256.Sum256([]byte("catlog-seed-career:" + handle + ":" + strconv.Itoa(n)))
	out := make([]byte, 16)
	for i := range out {
		out[i] = crockford[sum[i]&0x1F]
	}
	return string(out)
}

// seedULID derives a ULID from a fixed string. The timestamp half is the fixed
// epoch and the entropy half is SHA-256 of the label, so the value is stable
// across machines and runs — which is what makes seeding idempotent against the
// (player_id, event_id) dedup index (D19).
func seedULID(label string, n int) ids.ID {
	sum := sha256.Sum256([]byte("catlog-seed:" + label + ":" + strconv.Itoa(n)))
	var id ids.ID
	ms := uint64(EpochMS)
	for i := range 6 {
		id[i] = byte(ms >> (40 - 8*i))
	}
	copy(id[6:], sum[:10])
	return id
}

func nullFloat(v float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: v, Valid: true}
}
