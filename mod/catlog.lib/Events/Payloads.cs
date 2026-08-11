using System.Collections.Generic;
using System.Text.Json.Serialization;
using MeowSci.Catlog.Lib.Telemetry;

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

/// <summary><c>system.discovered</c>.</summary>
public sealed record SystemDiscoveredPayload(
    [property: JsonPropertyName("system")] string System,
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("home")] string HomeBody,
    [property: JsonPropertyName("bodies")] int BodyCount,
    [property: JsonPropertyName("complete")] bool Complete);

/// <summary><c>system.body</c>.</summary>
public sealed record SystemBodyPayload(
    [property: JsonPropertyName("system")] string System,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("class")] string Class,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("rank")] int Rank,
    [property: JsonPropertyName("parent")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? Parent,
    [property: JsonPropertyName("radius_m")] double RadiusM,
    [property: JsonPropertyName("mass_kg")] double MassKg,
    [property: JsonPropertyName("soi_m")] double SoiM,
    [property: JsonPropertyName("atmo_m")] double AtmoM,
    [property: JsonPropertyName("ocean_m")] double OceanM,
    [property: JsonPropertyName("angvel")] double AngVelRadS,
    [property: JsonPropertyName("axis")] Telemetry.Vec3 AxisCce,
    [property: JsonPropertyName("ccf_to_cce_t0")] Telemetry.Quat CcfToCceT0,
    [property: JsonPropertyName("sma_m"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? SmaM,
    [property: JsonPropertyName("ecc"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? Ecc,
    [property: JsonPropertyName("inc_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? IncDeg,
    [property: JsonPropertyName("lan_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? LanDeg,
    [property: JsonPropertyName("argp_deg"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? ArgpDeg,
    [property: JsonPropertyName("t_pe"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? TPe,
    [property: JsonPropertyName("period_s"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? PeriodS);

/// <summary><c>flight.started</c>.</summary>
/// <param name="VehicleName">Display name, ≤64 printable US-ASCII characters.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="MassKg">Total mass, in kilograms.</param>
/// <param name="PartCount">Part count.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="Kids">
/// The pseudonymous id of every kitten aboard at launch, in seat order; empty when uncrewed.
/// Always present — an uncrewed flight is an empty array, not a missing key, so a reader never has
/// to distinguish "nobody aboard" from "the mod did not say".
/// </param>
/// <param name="StageCount">
/// How many stages the vehicle has. <c>0</c> when the read failed, which is a real value here
/// rather than a lie: a vehicle genuinely can have no sequences, and the count is descriptive only.
/// </param>
/// <param name="EngineCount">
/// Installed rocket engines, including inactive engines. Omitted when the game read failed; a
/// present <c>0</c> means the vehicle genuinely launched without an engine.
/// </param>
/// <param name="Lat">Latitude in degrees, <b>omitted</b> when unreadable. See <see cref="Telemetry.TelemetrySnapshot.Lat"/>.</param>
/// <param name="Lon">Longitude in degrees, omitted under the same rule.</param>
public sealed record FlightStartedPayload(
    [property: JsonPropertyName("vehicle_name")] string VehicleName,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("mass_kg")] double MassKg,
    [property: JsonPropertyName("part_count")] int PartCount,
    [property: JsonPropertyName("crew_count")] int CrewCount,
    [property: JsonPropertyName("kids")] IReadOnlyList<string> Kids,
    [property: JsonPropertyName("stage_count")] int StageCount,
    [property: JsonPropertyName("engine_count")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    int? EngineCount,
    [property: JsonPropertyName("lat")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lat,
    [property: JsonPropertyName("lon")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lon);

/// <summary><c>flight.ended</c>.</summary>
/// <param name="Reason">One of <c>recovered</c>, <c>destroyed</c>, <c>despawned</c>.</param>
/// <param name="CrewCount">Occupied seats when the flight ended.</param>
/// <param name="Kids">The pseudonymous id of every kitten aboard at the end; empty when nobody was.</param>
/// <param name="Body">
/// Lowercase parent body name, or <c>"unknown"</c>. Without it a landing site is unplaceable: the
/// flight's last <c>telemetry.window</c> may be a whole window old and the vehicle may have changed
/// SOI since. <c>"unknown"</c> is the honest value on the one path that has no vehicle left to read
/// — the poll's silent-removal safety net — and is an ordinary member of the open <c>body</c> set.
/// </param>
/// <param name="Lat">Latitude in degrees, omitted when unreadable.</param>
/// <param name="Lon">Longitude in degrees, omitted when unreadable.</param>
public sealed record FlightEndedPayload(
    [property: JsonPropertyName("reason")] string Reason,
    [property: JsonPropertyName("crew_count")] int CrewCount,
    [property: JsonPropertyName("kids")] IReadOnlyList<string> Kids,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("lat")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lat,
    [property: JsonPropertyName("lon")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lon);

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
/// <param name="RadarAltM">
/// Terrain-relative altitude in metres, <b>omitted</b> when the game has no terrain sample. The
/// barometric <c>altitude_m</c> beside it is above the body's mean radius, which on a mountain or
/// in a crater is not the number a player means by "how high am I".
/// </param>
public sealed record VehicleSituationPayload(
    [property: JsonPropertyName("from")] string From,
    [property: JsonPropertyName("to")] string To,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("altitude_m")] double AltitudeM,
    [property: JsonPropertyName("surface_speed_ms")] double SurfaceSpeedMs,
    [property: JsonPropertyName("orbital_speed_ms")] double OrbitalSpeedMs,
    [property: JsonPropertyName("radar_alt_m")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? RadarAltM);

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
/// <param name="SmaM">Semi-major axis in metres.</param>
/// <param name="LanDeg">Longitude of the ascending node in degrees.</param>
/// <param name="ArgpDeg">Argument of periapsis in degrees.</param>
/// <param name="TPe">Time at periapsis, in game seconds.</param>
/// <param name="PeriodS">Orbital period in seconds, or 0 for an unbound trajectory.</param>
/// <param name="MassKg">
/// Total mass at the instant the milestone fired, in kilograms. Paired with
/// <c>flight.started.mass_kg</c> it is the only honest efficiency-shaped number reachable without
/// reading propellant: how much of what left the pad is still there.
/// </param>
public sealed record VehicleOrbitPayload(
    [property: JsonPropertyName("phase")] string Phase,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("ap_m")] double ApM,
    [property: JsonPropertyName("pe_m")] double PeM,
    [property: JsonPropertyName("ecc")] double Ecc,
    [property: JsonPropertyName("inc_deg")] double IncDeg,
    [property: JsonPropertyName("sma_m")] double SmaM,
    [property: JsonPropertyName("lan_deg")] double LanDeg,
    [property: JsonPropertyName("argp_deg")] double ArgpDeg,
    [property: JsonPropertyName("t_pe")] double TPe,
    [property: JsonPropertyName("period_s")] double PeriodS,
    [property: JsonPropertyName("mass_kg")] double MassKg);

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
/// <param name="PartCount">Parts on the intact vehicle at destruction.</param>
/// <param name="Lat">Latitude in degrees, omitted when unreadable.</param>
/// <param name="Lon">Longitude in degrees, omitted when unreadable.</param>
public sealed record VehicleRudPayload(
    [property: JsonPropertyName("cause")] string Cause,
    [property: JsonPropertyName("peak_g")] double PeakG,
    [property: JsonPropertyName("peak_q_pa")] double PeakQPa,
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("altitude_m")] double AltitudeM,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("crew_count")] int CrewCount,
    [property: JsonPropertyName("part_count")] int PartCount,
    [property: JsonPropertyName("lat")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lat,
    [property: JsonPropertyName("lon")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lon);

/// <summary><c>vehicle.impact</c>.</summary>
/// <param name="SpeedMs">Closing speed, in m/s.</param>
/// <param name="EnergyJ">Impact kinetic energy, in joules.</param>
/// <param name="Survived">True when no destruction of the same vehicle followed in the same or next frame.</param>
/// <param name="LaunchPad">True when the struck collider was the launch pad.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="Lat">Latitude in degrees, omitted when unreadable.</param>
/// <param name="Lon">Longitude in degrees, omitted when unreadable.</param>
public sealed record VehicleImpactPayload(
    [property: JsonPropertyName("speed_ms")] double SpeedMs,
    [property: JsonPropertyName("energy_j")] double EnergyJ,
    [property: JsonPropertyName("survived")] bool Survived,
    [property: JsonPropertyName("launch_pad")] bool LaunchPad,
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("crew_count")] int CrewCount,
    [property: JsonPropertyName("lat")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lat,
    [property: JsonPropertyName("lon")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lon);

/// <summary>
/// <c>vehicle.landed</c> — a vehicle came into contact with a surface it was not touching before.
/// </summary>
/// <remarks>
/// Emitted off the same transition as <c>vehicle.situation</c>, never a second detector, and
/// deliberately carries <b>no plausibility rule</b>: a one-metre hop is a landing. Constitution §8
/// forbids inferring intent from the shape of the data, so what counts as an interesting landing is
/// a question for the site to answer in words, not for the mod to answer by filtering.
/// </remarks>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="VerticalSpeedMs">Descent rate at touchdown in m/s, positive downwards.</param>
/// <param name="HorizontalSpeedMs">Ground-track speed at touchdown, in m/s.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="Survived">
/// False when the vehicle was destroyed within one full frame of the touchdown — resolved by
/// <see cref="Detect.ImpactCorrelator"/>, exactly as <c>vehicle.impact.survived</c> is, and never
/// inferred from the numbers above.
/// </param>
/// <param name="RadarAltM">
/// Terrain-relative altitude at the sample that detected the landing, in metres; omitted when the
/// game has no terrain sample. It is not expected to be 0: the detector runs at 2 Hz, so the sample
/// that first shows contact is up to half a second after the wheels touched.
/// </param>
/// <param name="Lat">Latitude in degrees, omitted when unreadable.</param>
/// <param name="Lon">Longitude in degrees, omitted when unreadable.</param>
public sealed record VehicleLandedPayload(
    [property: JsonPropertyName("body")] string Body,
    [property: JsonPropertyName("vertical_speed_ms")] double VerticalSpeedMs,
    [property: JsonPropertyName("horizontal_speed_ms")] double HorizontalSpeedMs,
    [property: JsonPropertyName("crew_count")] int CrewCount,
    [property: JsonPropertyName("survived")] bool Survived,
    [property: JsonPropertyName("radar_alt_m")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? RadarAltM,
    [property: JsonPropertyName("lat")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lat,
    [property: JsonPropertyName("lon")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    double? Lon);

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
/// <param name="From">Lowercase locomotion mode immediately before tumbling.</param>
/// <param name="SpeedMs">Tangential speed at the transition, in m/s.</param>
/// <param name="Body">Lowercase parent body name.</param>
public sealed record KittenTumblePayload(
    [property: JsonPropertyName("kid")] string Kid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("from")] string From,
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
/// <param name="RadarAltM">
/// Terrain-relative altitude aggregate in metres, folded over <b>only</b> the samples that carried
/// a reading and <b>omitted entirely</b> when none did — the same rule as <paramref name="PeakG"/>,
/// for the same reason. A window spent in orbit has no terrain below it, and a mean that counted
/// those samples as 0 would report a craft skimming the ground.
/// </param>
/// <param name="WarpMax">
/// The highest simulation speed seen in the window; 1 is real time. Descriptive only — it exists so
/// a reader can tell a 60-sample window from the handful of samples a 10 000× warp leaves behind,
/// not so anything can be rejected on it.
/// </param>
/// <param name="State">
/// Last sample's body-centred inertial position and velocity, or null when the complete reading
/// was unavailable or did not belong to <paramref name="Body"/>.
/// </param>
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
    [property: JsonPropertyName("mass_kg_last")] double MassKgLast,
    [property: JsonPropertyName("radar_alt_m")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Agg? RadarAltM,
    [property: JsonPropertyName("warp_max")] double WarpMax,
    [property: JsonPropertyName("state")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    StateVec? State);
