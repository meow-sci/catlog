using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Events;

/// <summary>Why a flight ended (<c>flight.ended.reason</c>, §4.2).</summary>
public enum FlightEndReason
{
    /// <summary>The player recovered the vehicle.</summary>
    Recovered,

    /// <summary>The vehicle was destroyed (physics RUD or a manual destroy).</summary>
    Destroyed,

    /// <summary>The vehicle left the simulation some other way (save load, docking merge, EVA board).</summary>
    Despawned,
}

/// <summary>The game's six vehicle-destruction causes (<c>vehicle.rud.cause</c>, §4.2).</summary>
public enum RudCause
{
    /// <summary><c>ground_impact</c>.</summary>
    GroundImpact,

    /// <summary><c>ocean_impact</c>.</summary>
    OceanImpact,

    /// <summary><c>collision</c>.</summary>
    Collision,

    /// <summary><c>excessive_g_force</c>.</summary>
    ExcessiveGForce,

    /// <summary><c>aerodynamic_forces</c>.</summary>
    AerodynamicForces,

    /// <summary><c>hydrodynamic_forces</c>.</summary>
    HydrodynamicForces,
}

/// <summary>Which engine event fired (<c>engine.ignition</c> / <c>engine.shutdown</c> / <c>engine.flameout</c>).</summary>
public enum EngineEventKind
{
    /// <summary>An engine activated.</summary>
    Ignition,

    /// <summary>An engine deactivated.</summary>
    Shutdown,

    /// <summary>
    /// An active engine ran out of propellant. The game has no flameout concept
    /// (<c>docs/ksa-integration.md</c> B3); the game project derives this from
    /// <c>IsActive &amp;&amp; !IsPropellantAvailable</c>.
    /// </summary>
    Flameout,
}

/// <summary>How a kitten came to be marked KIA (<c>kitten.kia.context</c>, §4.2).</summary>
public enum KiaContext
{
    /// <summary>A vehicle RUD. Per D11 this path never actually fires — see <c>docs/ksa-integration.md</c> §4.</summary>
    Rud,

    /// <summary>The player deliberately abandoned/destroyed the vehicle. The only path that sets the game's <c>Kia</c> flag.</summary>
    ManualDestroy,

    /// <summary>Observed by roster diff with no attributable cause.</summary>
    Unknown,
}

/// <summary>Integrity flags on a flight (<c>flight.flagged.flag</c>, §4.2).</summary>
public enum FlightFlag
{
    /// <summary>The vehicle was teleported.</summary>
    Teleport,

    /// <summary>Consumables were refilled from outside the simulation.</summary>
    Refuel,

    /// <summary>Resources were edited directly.</summary>
    ResourceEdit,

    /// <summary>A terminal/console command was issued.</summary>
    Console,

    /// <summary>
    /// Live tuning differs from stock. The game ships a debug window that edits
    /// <c>KittenLocomotionTuning.Current.TumbleSpeedGate</c> (stock 6.5 m/s), the sole classifier
    /// for <c>kitten.tumble</c>; without this flag the tumble board is trivially forgeable
    /// (<c>docs/ksa-integration.md</c> B9, DECISIONS 2026-08-06).
    /// </summary>
    Tuning,
}

/// <summary>One kitten's row in a <c>roster.snapshot</c>.</summary>
/// <param name="Name">Roster display name (sanitized when the envelope is built).</param>
/// <param name="TravelledM">Lifetime distance travelled, in metres.</param>
/// <param name="FastestMs">
/// The game's <c>FastestSpeed</c>, in m/s. <b>Ecliptic-frame</b> — it includes the parent body's
/// orbital motion, so it reads ~30 km/s on Earth. Recorded for completeness only; the speed boards
/// derive from <c>telemetry.window</c> (DECISIONS 2026-08-06).
/// </param>
/// <param name="Missions">Mission count.</param>
/// <param name="MissionTimeS">Banked mission time, in seconds.</param>
/// <param name="Kia">The game's KIA flag.</param>
public sealed record RosterKitten(
    string Name,
    double TravelledM,
    double FastestMs,
    int Missions,
    double MissionTimeS,
    bool Kia);

/// <summary>
/// A discrete, game-thread-originated occurrence: something that happened once and cannot be
/// recovered by comparing two telemetry samples.
/// </summary>
/// <remarks>
/// Signals travel through <see cref="Telemetry.GameBridge"/>'s <b>unbounded, lossless</b> channel,
/// never through the latest-wins <see cref="Util.SnapshotStore"/>: every one of these is a scoring
/// event, and a dropped RUD or impact is a permanently lost leaderboard entry.
/// </remarks>
/// <param name="SimT">Universe sim seconds when the signal was raised.</param>
/// <param name="WallMs">Client unix milliseconds when the signal was raised.</param>
public abstract record GameSignal(double SimT, long WallMs);

/// <summary>
/// Marks the end of one game frame. The game project raises exactly one per frame, after the
/// game's own <c>Universe.ApplyVehicleSolvers</c> and <c>InputEvents.ApplyInputEvents</c> have run.
/// </summary>
/// <remarks>
/// Ordering matters and the channel preserves it: this is what gives
/// <see cref="Detect.ImpactCorrelator"/> its frame boundaries after the signals have left the game
/// thread. Without an in-band marker the correlator would have to guess where one frame ended.
/// </remarks>
/// <param name="SimT">Universe sim seconds at the frame boundary.</param>
/// <param name="WallMs">Client unix milliseconds at the frame boundary.</param>
/// <param name="Sequence">The game-thread frame counter, for diagnostics.</param>
public sealed record FrameBoundarySignal(double SimT, long WallMs, long Sequence)
    : GameSignal(SimT, WallMs);

/// <summary>A save was loaded or a new game started — a session boundary.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="GameBuild">The game build string, e.g. <c>2026.8.5.5168</c>.</param>
/// <param name="ModVersion">The catlog mod version.</param>
public sealed record SessionLoadedSignal(double SimT, long WallMs, string GameBuild, string ModVersion)
    : GameSignal(SimT, WallMs);

/// <summary>A vehicle entered the simulation — the start of a flight.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="VehicleName">Display name.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="MassKg">Total mass in kilograms.</param>
/// <param name="PartCount">Part count.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="LaunchGameTime">
/// The game's <c>Vehicle.LaunchGameTime</c>. Together with the vehicle id this is the flight's
/// identity, so a rename or a re-registration after a save load does not mint a second flight.
/// </param>
public sealed record VehicleCreatedSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    string VehicleName,
    string Body,
    double MassKg,
    int PartCount,
    int CrewCount,
    double LaunchGameTime) : GameSignal(SimT, WallMs);

/// <summary>A vehicle left the simulation.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="Reason">Why the flight ended.</param>
/// <param name="CrewCount">Occupied seats at the moment it ended.</param>
public sealed record VehicleRemovedSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    FlightEndReason Reason,
    int CrewCount) : GameSignal(SimT, WallMs);

/// <summary>The player recovered a vehicle. Ends the flight with <c>reason: "recovered"</c>.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="CrewCount">Occupied seats.</param>
public sealed record VehicleRecoveredSignal(double SimT, long WallMs, string VehicleId, int CrewCount)
    : GameSignal(SimT, WallMs);

/// <summary>A vehicle was destroyed by the physics solver.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="Cause">The game's destruction cause.</param>
/// <param name="PeakG">Peak g-load from the destruction event.</param>
/// <param name="PeakQPa">Peak dynamic pressure from the destruction event, in pascals.</param>
/// <param name="SpeedMs">Speed at destruction, in m/s.</param>
/// <param name="AltitudeM">Altitude at destruction, in metres.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">
/// Occupied seats. Per D11 these kittens all survive: the physics destruction path never calls
/// <c>KillCrew</c> (<c>docs/ksa-integration.md</c> §4).
/// </param>
public sealed record RudSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    RudCause Cause,
    double PeakG,
    double PeakQPa,
    double SpeedMs,
    double AltitudeM,
    string Body,
    int CrewCount) : GameSignal(SimT, WallMs);

/// <summary>A ground impact. Paired with a same-or-next-frame destruction by <see cref="Detect.ImpactCorrelator"/>.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="SpeedMs">Closing normal speed, in m/s.</param>
/// <param name="EnergyJ">Impact kinetic energy, in joules.</param>
/// <param name="LaunchPad">True when the struck collider was the launch pad (excluded from the lithobrake board).</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">Occupied seats.</param>
public sealed record ImpactSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    double SpeedMs,
    double EnergyJ,
    bool LaunchPad,
    string Body,
    int CrewCount) : GameSignal(SimT, WallMs);

/// <summary>
/// A water splash. Treated as an impact with <c>launch_pad = false</c>; the game's splash event
/// carries no velocity, so the game project reconstructs <c>v ≈ √(2E/m)</c>.
/// </summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="SpeedMs">Reconstructed impact speed, in m/s.</param>
/// <param name="EnergyJ">Impact kinetic energy, in joules.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="CrewCount">Occupied seats.</param>
public sealed record SplashSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    double SpeedMs,
    double EnergyJ,
    string Body,
    int CrewCount) : GameSignal(SimT, WallMs);

/// <summary>The player activated the next stage.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="StageIndex">Zero-based index of the activated stage.</param>
public sealed record StagingSignal(double SimT, long WallMs, string VehicleId, int StageIndex)
    : GameSignal(SimT, WallMs);

/// <summary>Two vehicles docked.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle the event is attributed to.</param>
/// <param name="OtherVehicleId">The other vehicle's id; resolved to its flight ULID on the wire.</param>
public sealed record DockSignal(double SimT, long WallMs, string VehicleId, string OtherVehicleId)
    : GameSignal(SimT, WallMs);

/// <summary>A vehicle undocked.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle the event is attributed to.</param>
/// <param name="OtherVehicleId">The vehicle that split off; resolved to its flight ULID on the wire.</param>
public sealed record UndockSignal(double SimT, long WallMs, string VehicleId, string OtherVehicleId)
    : GameSignal(SimT, WallMs);

/// <summary>An engine ignited, shut down, or ran dry.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The vehicle's id.</param>
/// <param name="Kind">Which engine event.</param>
/// <param name="Engine">Engine template name.</param>
/// <param name="Count">How many engines of that template were affected.</param>
public sealed record EngineSignal(
    double SimT,
    long WallMs,
    string VehicleId,
    EngineEventKind Kind,
    string Engine,
    int Count) : GameSignal(SimT, WallMs);

/// <summary>A kitten went EVA.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="KittenName">The kitten's roster name; hashed into <c>kid</c> on the wire.</param>
/// <param name="VehicleId">The EVA vehicle's id, when there is one.</param>
public sealed record EvaStartSignal(double SimT, long WallMs, string KittenName, string? VehicleId = null)
    : GameSignal(SimT, WallMs);

/// <summary>A kitten's EVA ended.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="KittenName">The kitten's roster name.</param>
/// <param name="DurationS">EVA duration, in sim seconds.</param>
public sealed record EvaEndSignal(double SimT, long WallMs, string KittenName, double DurationS)
    : GameSignal(SimT, WallMs);

/// <summary>A kitten started tumbling.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="KittenName">The kitten's roster name.</param>
/// <param name="SpeedMs">Tangential speed at the transition, in m/s.</param>
/// <param name="Body">Lowercase parent body name.</param>
public sealed record TumbleSignal(double SimT, long WallMs, string KittenName, double SpeedMs, string Body)
    : GameSignal(SimT, WallMs);

/// <summary>A kitten was marked KIA.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="KittenName">The kitten's roster name.</param>
/// <param name="Context">How it happened.</param>
public sealed record KiaSignal(double SimT, long WallMs, string KittenName, KiaContext Context)
    : GameSignal(SimT, WallMs);

/// <summary>An integrity flag was raised.</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="VehicleId">The affected vehicle, or null for a session-wide flag such as <see cref="FlightFlag.Tuning"/>.</param>
/// <param name="Flag">Which flag.</param>
/// <param name="Detail">Free text describing what was detected.</param>
public sealed record FlaggedSignal(
    double SimT,
    long WallMs,
    string? VehicleId,
    FlightFlag Flag,
    string Detail) : GameSignal(SimT, WallMs);

/// <summary>A periodic roster sample (every 10 minutes of play, and at session end).</summary>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
/// <param name="Kittens">One row per roster entry.</param>
public sealed record RosterSampleSignal(double SimT, long WallMs, IReadOnlyList<RosterKitten> Kittens)
    : GameSignal(SimT, WallMs);
