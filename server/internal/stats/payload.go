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

// Agg is the §4.2 aggregate object.
type Agg struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
	Last float64 `json:"last"`
}

// FlightStarted is `flight.started`.
type FlightStarted struct {
	VehicleName string  `json:"vehicle_name"`
	Body        string  `json:"body"`
	MassKg      float64 `json:"mass_kg"`
	PartCount   int     `json:"part_count"`
	CrewCount   int     `json:"crew_count"`
}

// FlightEnded is `flight.ended`.
type FlightEnded struct {
	Reason    string `json:"reason"` // recovered | destroyed | despawned
	CrewCount int    `json:"crew_count"`
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
	From           string  `json:"from"`
	To             string  `json:"to"`
	Body           string  `json:"body"`
	AltitudeM      float64 `json:"altitude_m"`
	SurfaceSpeedMs float64 `json:"surface_speed_ms"`
	OrbitalSpeedMs float64 `json:"orbital_speed_ms"`
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
}

// VehicleSOI is `vehicle.soi`.
type VehicleSOI struct {
	FromBody string `json:"from_body"`
	ToBody   string `json:"to_body"`
}

// VehicleRUD is `vehicle.rud`.
type VehicleRUD struct {
	Cause     string  `json:"cause"`
	PeakG     float64 `json:"peak_g"`
	PeakQPa   float64 `json:"peak_q_pa"`
	SpeedMs   float64 `json:"speed_ms"`
	AltitudeM float64 `json:"altitude_m"`
	Body      string  `json:"body"`
	CrewCount int     `json:"crew_count"`
}

// VehicleImpact is `vehicle.impact`. `survived` is mod-computed (§7.2): it means
// no destruction of the same vehicle in the same frame or the next.
type VehicleImpact struct {
	SpeedMs   float64 `json:"speed_ms"`
	EnergyJ   float64 `json:"energy_j"`
	Survived  bool    `json:"survived"`
	LaunchPad bool    `json:"launch_pad"`
	Body      string  `json:"body"`
	CrewCount int     `json:"crew_count"`
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
}

// SessionStarted is `session.started`.
type SessionStarted struct {
	ModVer    string `json:"mod_ver"`
	GameBuild string `json:"game_build"`
	Install   string `json:"install"`
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
