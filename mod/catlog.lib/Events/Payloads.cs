using System.Collections.Generic;
using System.Text.Json.Serialization;

namespace MeowSci.Catlog.Lib.Events;

/// <summary>
/// The four-number aggregate object §4.2 calls <c>agg</c>.
/// </summary>
/// <param name="Min">Smallest sample in the window.</param>
/// <param name="Max">Largest sample in the window.</param>
/// <param name="Mean">Arithmetic mean of the samples.</param>
/// <param name="Last">The final sample in the window.</param>
public sealed record Agg(
    [property: JsonPropertyName("min")] double Min,
    [property: JsonPropertyName("max")] double Max,
    [property: JsonPropertyName("mean")] double Mean,
    [property: JsonPropertyName("last")] double Last);

/// <summary><c>session.started</c>.</summary>
/// <param name="ModVer">The catlog mod version.</param>
/// <param name="GameBuild">The game build string.</param>
/// <param name="Install">The install ULID, stable across sessions on one machine.</param>
public sealed record SessionStartedPayload(
    [property: JsonPropertyName("mod_ver")] string ModVer,
    [property: JsonPropertyName("game_build")] string GameBuild,
    [property: JsonPropertyName("install")] string Install);

/// <summary><c>flight.started</c>.</summary>
/// <param name="VehicleName">Display name, ≤64 printable US-ASCII characters.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="MassKg">Total mass, in kilograms.</param>
/// <param name="PartCount">Part count.</param>
/// <param name="CrewCount">Occupied seats.</param>
public sealed record FlightStartedPayload(
    [property: JsonPropertyName("vehicle_name")] string VehicleName,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("mass_kg")] double MassKg,
    [property: JsonPropertyName("part_count")] int PartCount,
    [property: JsonPropertyName("crew_count")] int CrewCount);

/// <summary><c>flight.ended</c>.</summary>
/// <param name="Reason">One of <c>recovered</c>, <c>destroyed</c>, <c>despawned</c>.</param>
/// <param name="CrewCount">Occupied seats when the flight ended.</param>
public sealed record FlightEndedPayload(
    [property: JsonPropertyName("reason")] string Reason,
    [property: JsonPropertyName("crew_count")] int CrewCount);

/// <summary><c>flight.flagged</c>.</summary>
/// <param name="Flag">One of <c>teleport</c>, <c>refuel</c>, <c>resource_edit</c>, <c>console</c>, <c>tuning</c>.</param>
/// <param name="Detail">Free text describing what was detected.</param>
public sealed record FlightFlaggedPayload(
    [property: JsonPropertyName("flag")] string Flag,
    [property: JsonPropertyName("detail")] string Detail);

/// <summary><c>vehicle.situation</c>.</summary>
/// <param name="From">Previous lowercase situation name.</param>
/// <param name="To">New lowercase situation name.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="AltitudeM">Altitude at the transition, in metres.</param>
/// <param name="SurfaceSpeedMs">Surface-relative speed, in m/s.</param>
/// <param name="OrbitalSpeedMs">Inertial speed, in m/s.</param>
public sealed record VehicleSituationPayload(
    [property: JsonPropertyName("from")] string From,
    [property: JsonPropertyName("to")] string To,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("altitude_m")] double AltitudeM,
    [property: JsonPropertyName("surface_speed_ms")] double SurfaceSpeedMs,
    [property: JsonPropertyName("orbital_speed_ms")] double OrbitalSpeedMs);

/// <summary><c>vehicle.atmosphere</c>.</summary>
/// <param name="Dir"><c>entered</c> or <c>exited</c>.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="SpeedMs">Surface-relative speed at the crossing, in m/s.</param>
/// <param name="DynPressurePa">Dynamic pressure at the crossing, in pascals.</param>
public sealed record VehicleAtmospherePayload(
    [property: JsonPropertyName("dir")] string Dir,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("dyn_pressure_pa")] double DynPressurePa);

/// <summary><c>vehicle.orbit</c>.</summary>
/// <param name="Phase"><c>achieved</c> or <c>escaped</c>.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="ApM">Apoapsis <b>altitude</b> above the parent's mean radius, in metres.</param>
/// <param name="PeM">Periapsis <b>altitude</b> above the parent's mean radius, in metres.</param>
/// <param name="Ecc">Eccentricity.</param>
/// <param name="IncDeg">Inclination in degrees (the game stores radians — convert).</param>
public sealed record VehicleOrbitPayload(
    [property: JsonPropertyName("phase")] string Phase,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("ap_m")] double ApM,
    [property: JsonPropertyName("pe_m")] double PeM,
    [property: JsonPropertyName("ecc")] double Ecc,
    [property: JsonPropertyName("inc_deg")] double IncDeg);

/// <summary><c>vehicle.soi</c>.</summary>
/// <param name="FromBody">Lowercase name of the body left.</param>
/// <param name="ToBody">Lowercase name of the body entered.</param>
public sealed record VehicleSoiPayload(
    [property: JsonPropertyName("from_body")] string FromBody,
    [property: JsonPropertyName("to_body")] string ToBody);

/// <summary><c>vehicle.rud</c>.</summary>
/// <param name="Cause">One of the six game destruction causes.</param>
/// <param name="PeakG">Peak g-load from the destruction event.</param>
/// <param name="PeakQPa">Peak dynamic pressure from the destruction event, in pascals.</param>
/// <param name="SpeedMs">Speed at destruction, in m/s.</param>
/// <param name="AltitudeM">Altitude at destruction, in metres.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">Occupied seats (all of whom survive — D11).</param>
public sealed record VehicleRudPayload(
    [property: JsonPropertyName("cause")] string Cause,
    [property: JsonPropertyName("peak_g")] double PeakG,
    [property: JsonPropertyName("peak_q_pa")] double PeakQPa,
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("altitude_m")] double AltitudeM,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("crew_count")] int CrewCount);

/// <summary><c>vehicle.impact</c>.</summary>
/// <param name="SpeedMs">Closing speed, in m/s.</param>
/// <param name="EnergyJ">Impact kinetic energy, in joules.</param>
/// <param name="Survived">True when no destruction of the same vehicle followed in the same or next frame.</param>
/// <param name="LaunchPad">True when the struck collider was the launch pad.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">Occupied seats.</param>
public sealed record VehicleImpactPayload(
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("energy_j")] double EnergyJ,
    [property: JsonPropertyName("survived")] bool Survived,
    [property: JsonPropertyName("launch_pad")] bool LaunchPad,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("crew_count")] int CrewCount);

/// <summary><c>vehicle.staging</c>.</summary>
/// <param name="StageIndex">Zero-based index of the activated stage.</param>
public sealed record VehicleStagingPayload(
    [property: JsonPropertyName("stage_index")] int StageIndex);

/// <summary><c>vehicle.docked</c> / <c>vehicle.undocked</c>.</summary>
/// <param name="OtherFlight">The other vehicle's flight ULID, or null when it has no flight.</param>
public sealed record VehicleDockPayload(
    [property: JsonPropertyName("other_flight")] string? OtherFlight);

/// <summary><c>engine.ignition</c> / <c>engine.shutdown</c> / <c>engine.flameout</c>.</summary>
/// <param name="Engine">Engine template name.</param>
/// <param name="Count">How many engines of that template were affected.</param>
public sealed record EnginePayload(
    [property: JsonPropertyName("engine")] string Engine,
    [property: JsonPropertyName("count")] int Count);

/// <summary><c>kitten.eva_start</c>.</summary>
/// <param name="Kid">Pseudonymous kitten id (§4.2).</param>
/// <param name="Name">Sanitized roster display name.</param>
public sealed record KittenEvaStartPayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name);

/// <summary><c>kitten.eva_end</c>.</summary>
/// <param name="Kid">Pseudonymous kitten id.</param>
/// <param name="Name">Sanitized roster display name.</param>
/// <param name="DurationS">EVA duration, in sim seconds.</param>
public sealed record KittenEvaEndPayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("duration_s")] double DurationS);

/// <summary><c>kitten.tumble</c>.</summary>
/// <param name="Kid">Pseudonymous kitten id.</param>
/// <param name="Name">Sanitized roster display name.</param>
/// <param name="SpeedMs">Tangential speed at the transition, in m/s.</param>
/// <param name="Body">Lowercase parent body name.</param>
public sealed record KittenTumblePayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("body")] string Body);

/// <summary><c>kitten.kia</c>.</summary>
/// <param name="Kid">Pseudonymous kitten id.</param>
/// <param name="Name">Sanitized roster display name.</param>
/// <param name="Context"><c>rud</c>, <c>manual_destroy</c> or <c>unknown</c>.</param>
public sealed record KittenKiaPayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("context")] string Context);

/// <summary>One kitten row inside a <c>roster.snapshot</c>.</summary>
/// <param name="Kid">Pseudonymous kitten id.</param>
/// <param name="Name">Sanitized roster display name.</param>
/// <param name="TravelledM">Lifetime distance travelled, in metres.</param>
/// <param name="FastestMs">The game's ecliptic-frame <c>FastestSpeed</c>, in m/s.</param>
/// <param name="Missions">Mission count.</param>
/// <param name="MissionTimeS">Banked mission time, in seconds.</param>
/// <param name="Kia">The game's KIA flag.</param>
public sealed record RosterKittenPayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("travelled_m")] double TravelledM,
    [property: JsonPropertyName("fastest_ms")] double FastestMs,
    [property: JsonPropertyName("missions")] int Missions,
    [property: JsonPropertyName("mission_time_s")] double MissionTimeS,
    [property: JsonPropertyName("kia")] bool Kia);

/// <summary><c>roster.snapshot</c>.</summary>
/// <param name="Kittens">Every roster entry at sample time.</param>
public sealed record RosterSnapshotPayload(
    [property: JsonPropertyName("kittens")] IReadOnlyList<RosterKittenPayload> Kittens);

/// <summary>
/// <c>telemetry.window</c> — one per vehicle per 30 s of sim-time active flight (D15).
/// </summary>
/// <param name="T0Sim">Sim time of the first sample in the window.</param>
/// <param name="T1Sim">Sim time of the last sample in the window.</param>
/// <param name="N">Number of samples folded.</param>
/// <param name="Body">Lowercase parent body name at the end of the window.</param>
/// <param name="AltM">Altitude aggregate, in metres.</param>
/// <param name="SurfaceSpeedMs">Surface-relative speed aggregate, in m/s.</param>
/// <param name="OrbitalSpeedMs">Inertial speed aggregate, in m/s.</param>
/// <param name="AccelMs2">Acceleration-magnitude aggregate, in m/s².</param>
/// <param name="PeakG">
/// Largest g-load seen, or <b>omitted</b> when no sample in the window carried a reading. See
/// <see cref="Telemetry.TelemetrySnapshot.PeakG"/>: an all-zero <c>StructuralLoad</c> means "no
/// data", and reporting 0 would corrupt the peak-g board.
/// </param>
/// <param name="MaxQPa">Largest dynamic pressure seen in pascals, omitted under the same rule.</param>
/// <param name="MassKgLast">Mass at the end of the window, in kilograms.</param>
public sealed record TelemetryWindowPayload(
    [property: JsonPropertyName("t0_sim")] double T0Sim,
    [property: JsonPropertyName("t1_sim")] double T1Sim,
    [property: JsonPropertyName("n")] int N,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("alt_m")] Agg AltM,
    [property: JsonPropertyName("surface_speed_ms")] Agg SurfaceSpeedMs,
    [property: JsonPropertyName("orbital_speed_ms")] Agg OrbitalSpeedMs,
    [property: JsonPropertyName("accel_ms2")] Agg AccelMs2,
    [property: JsonPropertyName("peak_g")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? PeakG,
    [property: JsonPropertyName("max_q_pa")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? MaxQPa,
    [property: JsonPropertyName("mass_kg_last")] double MassKgLast);
