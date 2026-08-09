using System;
using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Events;

/// <summary>
/// The §4.2 launch-set registry: every event type name, its payload schema version, and whether
/// it is a passive sample or a scoring event.
/// </summary>
/// <remarks>
/// The server rejects the whole batch with <c>400 malformed_batch</c> on an unknown type, so this
/// list and the Go registry ship together. Adding a type here without adding it there breaks
/// ingestion loudly, by design. The versions below must equal the server's
/// <c>projector.currentVer</c> exactly: a type this side calls <c>ver</c> 2 and that side still
/// folds at <c>ver</c> 1 is skipped by the projector as a future version.
/// </remarks>
public static class EventTypes
{
    /// <summary>A session began (mod load or save load).</summary>
    public const string SessionStarted = "session.started";

    /// <summary>A flight began.</summary>
    public const string FlightStarted = "flight.started";

    /// <summary>A flight ended.</summary>
    public const string FlightEnded = "flight.ended";

    /// <summary>An integrity flag was raised on a flight.</summary>
    public const string FlightFlagged = "flight.flagged";

    /// <summary>The vehicle's situation changed.</summary>
    public const string VehicleSituation = "vehicle.situation";

    /// <summary>The vehicle crossed an atmosphere boundary.</summary>
    public const string VehicleAtmosphere = "vehicle.atmosphere";

    /// <summary>The vehicle achieved or escaped orbit.</summary>
    public const string VehicleOrbit = "vehicle.orbit";

    /// <summary>The vehicle changed sphere of influence.</summary>
    public const string VehicleSoi = "vehicle.soi";

    /// <summary>The vehicle was rapidly and unscheduledly disassembled.</summary>
    public const string VehicleRud = "vehicle.rud";

    /// <summary>The vehicle hit something.</summary>
    public const string VehicleImpact = "vehicle.impact";

    /// <summary>The vehicle touched down on a surface it was not touching before.</summary>
    public const string VehicleLanded = "vehicle.landed";

    /// <summary>A stage was activated.</summary>
    public const string VehicleStaging = "vehicle.staging";

    /// <summary>The vehicle docked.</summary>
    public const string VehicleDocked = "vehicle.docked";

    /// <summary>The vehicle undocked.</summary>
    public const string VehicleUndocked = "vehicle.undocked";

    /// <summary>An engine ignited.</summary>
    public const string EngineIgnition = "engine.ignition";

    /// <summary>An engine shut down.</summary>
    public const string EngineShutdown = "engine.shutdown";

    /// <summary>An engine ran out of propellant while active.</summary>
    public const string EngineFlameout = "engine.flameout";

    /// <summary>A kitten went EVA.</summary>
    public const string KittenEvaStart = "kitten.eva_start";

    /// <summary>A kitten's EVA ended.</summary>
    public const string KittenEvaEnd = "kitten.eva_end";

    /// <summary>A kitten tumbled.</summary>
    public const string KittenTumble = "kitten.tumble";

    /// <summary>A kitten was marked KIA.</summary>
    public const string KittenKia = "kitten.kia";

    /// <summary>A periodic roster snapshot.</summary>
    public const string RosterSnapshot = "roster.snapshot";

    /// <summary>A 30-second passive telemetry window.</summary>
    public const string TelemetryWindow = "telemetry.window";

    /// <summary>Outbox <c>kind</c> for passive rows: droppable under pressure.</summary>
    public const int KindPassive = 0;

    /// <summary>Outbox <c>kind</c> for scoring rows: never pruned.</summary>
    public const int KindEvent = 1;

    private static readonly Dictionary<string, int> Versions = new(StringComparer.Ordinal)
    {
        [SessionStarted] = 1,
        // ---- wire v2 -------------------------------------------------------------------------
        // Seven types gained keys in one wave, and every one of them is a payload change rather
        // than a semantic one: each ver 2 payload is its ver 1 payload plus fields. That makes the
        // server's upcaster identity in every case — a ver 1 event still folds correctly, it simply
        // says less — which is exactly why they could all move together.
        //
        //   flight.started    + kids, stage_count, lat, lon
        //   flight.ended      + kids, body, lat, lon
        //   vehicle.situation + radar_alt_m
        //   vehicle.orbit     + mass_kg
        //   vehicle.rud       + lat, lon
        //   vehicle.impact    + lat, lon
        //   telemetry.window  + radar_alt_m, warp_max
        [FlightStarted] = 2,
        [FlightEnded] = 2,
        [FlightFlagged] = 1,
        [VehicleSituation] = 2,
        [VehicleAtmosphere] = 1,
        [VehicleOrbit] = 2,
        [VehicleSoi] = 1,
        [VehicleRud] = 2,
        [VehicleImpact] = 2,
        // New in wire v2: there is no ver 0 to upcast from.
        [VehicleLanded] = 1,
        [VehicleStaging] = 1,
        [VehicleDocked] = 1,
        [VehicleUndocked] = 1,
        [EngineIgnition] = 1,
        [EngineShutdown] = 1,
        [EngineFlameout] = 1,
        [KittenEvaStart] = 1,
        [KittenEvaEnd] = 1,
        // ver 2: both gained a non-null `flight`. The payload bytes are unchanged, but the
        // envelope contract is not, and the change is one a reader of the immutable log has to be
        // able to see — a ver 1 kitten.tumble can never be excluded by its flight's `tuning` flag
        // (it names no flight), and a ver 1 kitten.kia can never disqualify an impact under the
        // ±2 s window, so the two versions score differently and the log has to say which is which.
        [KittenTumble] = 2,
        [KittenKia] = 2,
        [RosterSnapshot] = 1,
        [TelemetryWindow] = 2,
    };

    // The five types a player cannot switch off in catlog.toml. Each one is load-bearing for
    // something a leaderboard depends on being able to *exclude*, so turning it off is a cheat
    // rather than a preference:
    //
    //   flight.flagged  — the sole writer of the server's flag bits, and therefore the only
    //                     exclusion gate for every board, the live feed and the public raw-event
    //                     views. Without it, teleporting, refuelling, resource-editing and console
    //                     cheating all score normally and appear publicly.
    //   kitten.kia      — drives the ±2 s window that disqualifies a fatal crash from
    //                     biggest_lithobrake_survived and biggest_impact_energy. Without it, a
    //                     crash that killed the crew scores as a survival.
    //   session.started — the only source of the save-scum rewind mark.
    //   flight.started  — without it every flight is a server-side phantom: a flight ULID that
    //                     appears on joins with no start, no crew and no body, which is the exact
    //                     hazard Flush's peek semantics exist to prevent.
    //   flight.ended    — kills ended_reason, so nothing is ever "recovered": peak_g_survived and
    //                     max_q_survived score nothing on a rebuild, and kittens_recovered and
    //                     biggest_recovery score nothing at all.
    //
    // This is a HashSet rather than a switch so EventTypeFilter, ModConfig and the tests all read
    // the same list, and so a test can assert it is exactly these five.
    private static readonly HashSet<string> AlwaysReportedTypes = new(StringComparer.Ordinal)
    {
        SessionStarted,
        FlightStarted,
        FlightEnded,
        FlightFlagged,
        KittenKia,
    };

    /// <summary>Every registered type name.</summary>
    public static IReadOnlyCollection<string> All => Versions.Keys;

    /// <summary>
    /// The types no configuration may suppress: the correctness and integrity spine.
    /// </summary>
    public static IReadOnlyCollection<string> AlwaysReported => AlwaysReportedTypes;

    /// <summary>True when <paramref name="type"/> may never be disabled.</summary>
    /// <param name="type">The event type name.</param>
    /// <returns>True for the integrity spine.</returns>
    public static bool IsAlwaysReported(string? type) => type is not null && AlwaysReportedTypes.Contains(type);

    /// <summary>True when <paramref name="type"/> is in the registry.</summary>
    /// <param name="type">The event type name.</param>
    /// <returns>True when known.</returns>
    public static bool IsKnown(string? type) => type is not null && Versions.ContainsKey(type);

    /// <summary>The payload schema version for <paramref name="type"/>.</summary>
    /// <param name="type">The event type name.</param>
    /// <returns>The version, or <see cref="Wire.EnvelopeVersion"/> for an unregistered type.</returns>
    public static int VersionOf(string type)
        => Versions.TryGetValue(type, out int ver) ? ver : Wire.EnvelopeVersion;

    /// <summary>
    /// The outbox <c>kind</c> for <paramref name="type"/>: <see cref="KindPassive"/> only for
    /// <see cref="TelemetryWindow"/>, <see cref="KindEvent"/> for everything else.
    /// </summary>
    /// <param name="type">The event type name.</param>
    /// <returns>0 for passive rows, 1 for scoring rows.</returns>
    /// <remarks>
    /// <c>Prune</c> drops passive rows oldest-first and never touches kind 1, so the classification
    /// here decides what survives a full outbox. Everything that can move a leaderboard — including
    /// <c>roster.snapshot</c>, which carries kitten totals — is kind 1.
    /// </remarks>
    public static int KindOf(string type)
        => string.Equals(type, TelemetryWindow, StringComparison.Ordinal) ? KindPassive : KindEvent;

    /// <summary>Maps a <see cref="RudCause"/> to its wire string.</summary>
    /// <param name="cause">The cause.</param>
    /// <returns>The <c>vehicle.rud.cause</c> value.</returns>
    public static string ToWire(RudCause cause) => cause switch
    {
        RudCause.GroundImpact => "ground_impact",
        RudCause.OceanImpact => "ocean_impact",
        RudCause.Collision => "collision",
        RudCause.ExcessiveGForce => "excessive_g_force",
        RudCause.AerodynamicForces => "aerodynamic_forces",
        RudCause.HydrodynamicForces => "hydrodynamic_forces",
        _ => "collision",
    };

    /// <summary>Maps a <see cref="FlightEndReason"/> to its wire string.</summary>
    /// <param name="reason">The reason.</param>
    /// <returns>The <c>flight.ended.reason</c> value.</returns>
    public static string ToWire(FlightEndReason reason) => reason switch
    {
        FlightEndReason.Recovered => "recovered",
        FlightEndReason.Destroyed => "destroyed",
        _ => "despawned",
    };

    /// <summary>Maps a <see cref="FlightFlag"/> to its wire string.</summary>
    /// <param name="flag">The flag.</param>
    /// <returns>The <c>flight.flagged.flag</c> value.</returns>
    public static string ToWire(FlightFlag flag) => flag switch
    {
        FlightFlag.Teleport => "teleport",
        FlightFlag.Refuel => "refuel",
        FlightFlag.ResourceEdit => "resource_edit",
        FlightFlag.Console => "console",
        _ => "tuning",
    };

    /// <summary>Maps a <see cref="KiaContext"/> to its wire string.</summary>
    /// <param name="context">The context.</param>
    /// <returns>The <c>kitten.kia.context</c> value.</returns>
    public static string ToWire(KiaContext context) => context switch
    {
        KiaContext.Rud => "rud",
        KiaContext.ManualDestroy => "manual_destroy",
        _ => "unknown",
    };

    /// <summary>Maps an <see cref="EngineEventKind"/> to its event type name.</summary>
    /// <param name="kind">The engine event kind.</param>
    /// <returns>One of the three <c>engine.*</c> type names.</returns>
    public static string TypeOf(EngineEventKind kind) => kind switch
    {
        EngineEventKind.Ignition => EngineIgnition,
        EngineEventKind.Shutdown => EngineShutdown,
        _ => EngineFlameout,
    };
}
