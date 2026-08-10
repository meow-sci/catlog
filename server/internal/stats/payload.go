package stats

import (
	"encoding/json"
	"fmt"
)

// The §4.2 payload shapes, as far as the folds care about them.
//
// Decoding is deliberately *lenient about unknown keys*: §4.1 preserves unknown
// payload keys for forward compatibility, and the row in events.db stores the
// payload verbatim, so a newer mod's extra field must decode cleanly here rather
// than fail the batch. That is the exact opposite of the envelope rule, which
// package ingest enforces with DisallowUnknownFields.
//
// Fields that are genuinely optional are pointers, not zero values. That
// distinction is load-bearing for telemetry.window.peak_g and max_q_pa: the
// game's StructuralLoad is only written under full physics, so the mod omits
// them rather than reporting zero, and a fold that treated a missing reading as
// a real 0 would poison the peak_g board (see docs/ksa-integration.md).
//
// The position keys obey the same rule and need it more, because their zero is
// a *place*: `lat`/`lon` 0 is a point in the ocean south of Ghana and
// `radar_alt_m` 0 is the ground. The mod omits the key entirely when the read
// failed — it never sends null and never sends 0 — so every one of them is a
// pointer here and a nil is "the mod could not say", not "sea level at the
// equator".

// Agg is the §4.2 aggregate object.
type Agg struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
	Last float64 `json:"last"`
}

// FlightStarted is `flight.started`.
//
// Kids is the pseudonymous id of every kitten aboard at launch, in seat order.
// It is always present on the wire and is `[]` for an uncrewed flight, so a nil
// slice here means the key was absent rather than an empty cabin — a
// distinction no fold needs today and every fold would need if it were ever
// collapsed.
//
// StageCount is 0 when the read failed, which is the same thing MassKg,
// PartCount and CrewCount do; `biggest_stack` therefore gates on `> 0` exactly
// as the other three launch boards do.
type FlightStarted struct {
	VehicleName string   `json:"vehicle_name"`
	Body        string   `json:"body"`
	MassKg      float64  `json:"mass_kg"`
	PartCount   int      `json:"part_count"`
	CrewCount   int      `json:"crew_count"`
	Kids        []string `json:"kids"`
	StageCount  int      `json:"stage_count"`
	EngineCount *int     `json:"engine_count"`
	Lat         *float64 `json:"lat"`
	Lon         *float64 `json:"lon"`
}

// FlightEnded is `flight.ended`.
//
// Body may be the literal `"unknown"`, which is an ordinary member of §4.2's
// open body set and not a sentinel to branch on: it is what the mod's
// silent-removal safety net reports when there is no vehicle left to ask. There
// is no `landed_on_unknown` board and there must not be one — a board keyed on
// a body requires a real body, and [statSuffix] is the only gate that decides
// which names can be one.
type FlightEnded struct {
	Reason    string   `json:"reason"` // recovered | destroyed | despawned
	CrewCount int      `json:"crew_count"`
	Kids      []string `json:"kids"`
	Body      string   `json:"body"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
}

// FlightFlagged is `flight.flagged`.
type FlightFlagged struct {
	Flag   string `json:"flag"` // teleport|refuel|resource_edit|console|tuning
	Detail string `json:"detail"`
}

// VehicleSituation is `vehicle.situation`.
//
// From and To are situation *names*, an open set (§4.2). situation.go decodes
// the surface contact each one implies; nothing here switches on the value.
// From is the situation last actually reported on the wire rather than the
// previous 2 Hz sample, which is what makes a debounced transition report the
// edge a player would describe.
type VehicleSituation struct {
	From           string   `json:"from"`
	To             string   `json:"to"`
	Body           string   `json:"body"`
	AltitudeM      float64  `json:"altitude_m"`
	SurfaceSpeedMs float64  `json:"surface_speed_ms"`
	OrbitalSpeedMs float64  `json:"orbital_speed_ms"`
	RadarAltM      *float64 `json:"radar_alt_m"`
}

// VehicleAtmosphere is `vehicle.atmosphere`.
//
// SpeedMs is surface-relative, not orbital, so `fastest_entry` is comparable
// with the lithobrake and RUD speeds rather than with an orbital velocity.
type VehicleAtmosphere struct {
	Dir           string  `json:"dir"` // entered | exited
	Body          string  `json:"body"`
	SpeedMs       float64 `json:"speed_ms"`
	DynPressurePa float64 `json:"dyn_pressure_pa"`
}

// VehicleOrbit is `vehicle.orbit`.
//
// ApM and PeM are **altitudes above the parent body's mean radius**, in metres —
// not from-centre radii. §4.2 left that ambiguous; docs/ksa-integration.md
// settles it, and the mod names its snapshot fields ApAltM/PeAltM to match.
type VehicleOrbit struct {
	Phase  string  `json:"phase"` // achieved | escaped
	Body   string  `json:"body"`
	ApM    float64 `json:"ap_m"`
	PeM    float64 `json:"pe_m"`
	Ecc    float64 `json:"ecc"`
	IncDeg float64 `json:"inc_deg"`
	// MassKg is the mass at the instant the milestone fired, and is written as
	// 0 when the read failed — the same thing FlightStarted.MassKg does. That
	// is why `heaviest_to_orbit` gates on `> 0` rather than trusting the field:
	// an unreadable vehicle must not rank as a zero-tonne payload.
	MassKg float64 `json:"mass_kg"`
}

// VehicleSOI is `vehicle.soi`.
type VehicleSOI struct {
	FromBody string `json:"from_body"`
	ToBody   string `json:"to_body"`
}

// VehicleRUD is `vehicle.rud`.
type VehicleRUD struct {
	Cause     string   `json:"cause"`
	PeakG     float64  `json:"peak_g"`
	PeakQPa   float64  `json:"peak_q_pa"`
	SpeedMs   float64  `json:"speed_ms"`
	AltitudeM float64  `json:"altitude_m"`
	Body      string   `json:"body"`
	CrewCount int      `json:"crew_count"`
	PartCount int      `json:"part_count"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
}

// VehicleImpact is `vehicle.impact`. `survived` is mod-computed (§7.2): it means
// no destruction of the same vehicle in the same frame or the next.
type VehicleImpact struct {
	SpeedMs   float64  `json:"speed_ms"`
	EnergyJ   float64  `json:"energy_j"`
	Survived  bool     `json:"survived"`
	LaunchPad bool     `json:"launch_pad"`
	Body      string   `json:"body"`
	CrewCount int      `json:"crew_count"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
}

// VehicleLanded is `vehicle.landed` — a vehicle touched a surface it was not
// touching before.
//
// It is emitted off the same detection as the `vehicle.situation` beside it
// (contact-free → surface contact, sharing that rule's 2 s debounce), so it is
// not a second opinion about when a landing happened. What it adds is the two
// numbers a situation change cannot carry — a descent rate decomposed from the
// ground-track speed — and `survived`.
//
// VerticalSpeedMs is **positive downwards**: a soft touchdown is a small
// positive number, which is why `softest_landing` is an ascending board.
// HorizontalSpeedMs is a magnitude and is never negative.
//
// Survived has already been through the same one-full-frame destruction hold as
// `vehicle.impact.survived` and is authoritative. Nothing here re-derives it
// from a nearby `vehicle.rud` or `flight.ended`; that would be a second, worse
// answer to a question the mod already answered.
//
// There is deliberately **no plausibility rule**: a one-metre hop is a landing.
// Filtering on "was that a real landing" infers intent from the shape of the
// data, which Constitution §8 forbids.
type VehicleLanded struct {
	Body              string  `json:"body"`
	VerticalSpeedMs   float64 `json:"vertical_speed_ms"`
	HorizontalSpeedMs float64 `json:"horizontal_speed_ms"`
	CrewCount         int     `json:"crew_count"`
	Survived          bool    `json:"survived"`
	// RadarAltM is the terrain-relative altitude at the detecting sample, and is
	// **not expected to be 0**: detection runs at 2 Hz, so the sample that first
	// shows contact is up to half a second after the wheels touched. Absent when
	// the game had no terrain reading.
	RadarAltM *float64 `json:"radar_alt_m"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
}

// VehicleStaging is `vehicle.staging`.
type VehicleStaging struct {
	StageIndex int `json:"stage_index"`
}

// VehicleDock is `vehicle.docked` and `vehicle.undocked`.
type VehicleDock struct {
	OtherFlight string `json:"other_flight"`
}

// Engine is `engine.ignition`, `engine.shutdown` and `engine.flameout`. One
// type for all three because the payload is one type in the mod too: the events
// differ by wire name alone, and the folds that read them key on the event type
// rather than on anything in here.
//
// The readings are whole-vehicle, not per-engine (docs/ksa-integration.md B3):
// Engine is the template id of the first active controller found, and Count the
// number of active ones — so a vehicle that shuts down one of two engine groups
// reports nothing at all until the last one stops.
type Engine struct {
	Engine string `json:"engine"`
	Count  int    `json:"count"`
}

// KittenEvaStart is `kitten.eva_start`.
type KittenEvaStart struct {
	Kid  string `json:"kid"`
	Name string `json:"name"`
}

// KittenEvaEnd is `kitten.eva_end`.
//
// DurationS is sim seconds, and is **0.0 when the EVA vehicle's launch time was
// never readable** — indistinguishable from an instantaneous spacewalk, which
// is why `longest_eva` requires it to be strictly positive.
type KittenEvaEnd struct {
	Kid       string  `json:"kid"`
	Name      string  `json:"name"`
	DurationS float64 `json:"duration_s"`
}

// KittenTumble is `kitten.tumble`.
type KittenTumble struct {
	Kid     string  `json:"kid"`
	Name    string  `json:"name"`
	From    string  `json:"from"`
	SpeedMs float64 `json:"speed_ms"`
	Body    string  `json:"body"`
}

// KittenKIA is `kitten.kia`. Per the decomp verification the game only writes
// Kia on the player-initiated destroy path, so this signals a deliberate
// scuttling rather than an impact fatality (docs/events.md).
type KittenKIA struct {
	Kid     string `json:"kid"`
	Name    string `json:"name"`
	Context string `json:"context"` // rud | manual_destroy | unknown
}

// RosterKitten is one entry of a `roster.snapshot`.
//
// FastestMs is the game's own FastestSpeed, which is **ecliptic-frame** (it
// carries the parent body's orbital motion, ~30 km/s on Earth). It is stored for
// completeness and must never become a speed board; the speed boards derive from
// telemetry.window (docs/ksa-integration.md).
type RosterKitten struct {
	Kid          string  `json:"kid"`
	Name         string  `json:"name"`
	TravelledM   float64 `json:"travelled_m"`
	FastestMs    float64 `json:"fastest_ms"`
	Missions     int     `json:"missions"`
	MissionTimeS float64 `json:"mission_time_s"`
	KIA          bool    `json:"kia"`
}

// RosterSnapshot is `roster.snapshot`.
type RosterSnapshot struct {
	Kittens []RosterKitten `json:"kittens"`
}

// TelemetryWindow is `telemetry.window`: one per vehicle per 30 s of sim time.
type TelemetryWindow struct {
	T0Sim          float64  `json:"t0_sim"`
	T1Sim          float64  `json:"t1_sim"`
	N              int      `json:"n"`
	Body           string   `json:"body"`
	AltM           Agg      `json:"alt_m"`
	SurfaceSpeedMs Agg      `json:"surface_speed_ms"`
	OrbitalSpeedMs Agg      `json:"orbital_speed_ms"`
	AccelMs2       Agg      `json:"accel_ms2"`
	PeakG          *float64 `json:"peak_g"`
	MaxQPa         *float64 `json:"max_q_pa"`
	MassKgLast     float64  `json:"mass_kg_last"`
	// RadarAltM is folded over **only** the samples that carried a terrain
	// reading and is absent when none did — the peak_g rule, for the peak_g
	// reason. Its population is therefore not N, and nothing may reconstruct a
	// sample count from it.
	RadarAltM *Agg `json:"radar_alt_m"`
	// WarpMax is the highest simulation speed seen in the window; 1 is real
	// time. **Descriptive only** (Constitution §8): it may annotate a row, and
	// it may never reject or disqualify one. It is not a cheat signal.
	WarpMax float64 `json:"warp_max"`
}

// SessionStarted is `session.started`.
type SessionStarted struct {
	ModVer    string `json:"mod_ver"`
	GameBuild string `json:"game_build"`
	Install   string `json:"install"`
}

// SystemDiscovered binds a session and career to one canonical system hash.
type SystemDiscovered struct {
	System   string `json:"system"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Home     string `json:"home"`
	Bodies   int    `json:"bodies"`
	Complete bool   `json:"complete"`
}

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Quat struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
	W float64 `json:"w"`
}

// SystemBody is one immutable celestial catalogue row. The six shape values
// are pointers because they are absent as a group on roots or unreadable orbits;
// PeriodS is independently absent for unbound conics.
type SystemBody struct {
	System     string   `json:"system"`
	Body       string   `json:"body"`
	Name       string   `json:"name"`
	Class      string   `json:"class"`
	Kind       string   `json:"kind"`
	Rank       int      `json:"rank"`
	Parent     *string  `json:"parent"`
	RadiusM    float64  `json:"radius_m"`
	MassKg     float64  `json:"mass_kg"`
	SoiM       float64  `json:"soi_m"`
	AtmoM      float64  `json:"atmo_m"`
	OceanM     float64  `json:"ocean_m"`
	AngVel     float64  `json:"angvel"`
	Axis       Vec3     `json:"axis"`
	CcfToCceT0 Quat     `json:"ccf_to_cce_t0"`
	SmaM       *float64 `json:"sma_m"`
	Ecc        *float64 `json:"ecc"`
	IncDeg     *float64 `json:"inc_deg"`
	LanDeg     *float64 `json:"lan_deg"`
	ArgpDeg    *float64 `json:"argp_deg"`
	TPe        *float64 `json:"t_pe"`
	PeriodS    *float64 `json:"period_s"`
}

// decodePayload turns a verbatim payload into its typed form.
//
// A type with no typed form yields (nil, nil): the event is still folded (it
// still creates flight state, and it still advances the checkpoint), it just has
// no payload any fold reads.
func decodePayload(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "session.started":
		return decodeInto[SessionStarted](raw)
	case "system.discovered":
		return decodeInto[SystemDiscovered](raw)
	case "system.body":
		return decodeInto[SystemBody](raw)
	case "flight.started":
		return decodeInto[FlightStarted](raw)
	case "flight.ended":
		return decodeInto[FlightEnded](raw)
	case "flight.flagged":
		return decodeInto[FlightFlagged](raw)
	case "vehicle.situation":
		return decodeInto[VehicleSituation](raw)
	case "vehicle.atmosphere":
		return decodeInto[VehicleAtmosphere](raw)
	case "vehicle.orbit":
		return decodeInto[VehicleOrbit](raw)
	case "vehicle.soi":
		return decodeInto[VehicleSOI](raw)
	case "vehicle.rud":
		return decodeInto[VehicleRUD](raw)
	case "vehicle.impact":
		return decodeInto[VehicleImpact](raw)
	case "vehicle.landed":
		return decodeInto[VehicleLanded](raw)
	case "vehicle.staging":
		return decodeInto[VehicleStaging](raw)
	case "vehicle.docked", "vehicle.undocked":
		return decodeInto[VehicleDock](raw)
	case "engine.ignition", "engine.shutdown", "engine.flameout":
		return decodeInto[Engine](raw)
	case "kitten.eva_start":
		return decodeInto[KittenEvaStart](raw)
	case "kitten.eva_end":
		return decodeInto[KittenEvaEnd](raw)
	case "kitten.tumble":
		return decodeInto[KittenTumble](raw)
	case "kitten.kia":
		return decodeInto[KittenKIA](raw)
	case "roster.snapshot":
		return decodeInto[RosterSnapshot](raw)
	case "telemetry.window":
		return decodeInto[TelemetryWindow](raw)
	default:
		// Every §4.2 type this build knows has a typed form. What lands here is
		// a type a *newer* mod introduced: it is still stored verbatim, still
		// counted by the census and still advances the checkpoint — it simply
		// has no payload a fold of this build can read. A rebuild after the
		// decoder lands folds the history that was skipped (§5.6, D22).
		return nil, nil
	}
}

func decodeInto[T any](raw json.RawMessage) (any, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("stats: decode payload as %T: %w", v, err)
	}
	return v, nil
}

// payloadOf extracts a fold's typed payload, reporting false when the event is
// of a different type (which is the normal case — every fold sees every event).
func payloadOf[T any](ev Event) (T, bool) {
	p, ok := ev.Payload.(T)
	return p, ok
}
