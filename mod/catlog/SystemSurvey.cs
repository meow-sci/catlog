using System;
using System.Collections.Generic;
using Brutal.Numerics;
using KSA;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// Reads the loaded celestial forest once on the game thread and caches a KSA-free immutable copy.
/// The cache is replayed at session boundaries by C2; it intentionally contains no clocks or ids
/// whose lifetime is shorter than the loaded content.
/// </summary>
[KsaAnchor("Universe.CurrentSystem; CelestialSystem.Id/All/HomeBody",
    SourceFile = "KSA/Universe.cs:92 / KSA/CelestialSystem.cs:55-61", Verified = "2026-08-10",
    GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
    Notes = "All also contains vehicles. Survey only All.OfType<IParentBody>(); Count is never used.")]
public static class SystemSurvey
{
    /// <summary>The most recent successfully materialised survey, or null before a system exists.</summary>
    public static SystemSnapshot? Cached { get; private set; }

    /// <summary>Surveys <see cref="Universe.CurrentSystem"/>, clearing the cache when it is null.</summary>
    [KsaAnchor("Universe.CurrentSystem",
        SourceFile = "KSA/Universe.cs:92", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Called on the game thread from LoadSystem postfix or AllModsLoaded fallback.")]
    public static SystemSnapshot? CaptureCurrent()
    {
        CelestialSystem? system = Universe.CurrentSystem;
        // Never let a failed survey of a newly loaded system leave the previous system cached.
        Cached = null;
        if (system is not null)
            Cached = Capture(system);
        return Cached;
    }

    /// <summary>Clears the process-lifetime cache during mod unload.</summary>
    public static void Clear() => Cached = null;

    [KsaAnchor("CelestialSystem.All.OfType<IParentBody>(); IParentBody.Id; Astronomical.Class",
        SourceFile = "KSA/CelestialSystem.cs:57 / KSA/LookupCollection.cs:12 / KSA/Astronomical.cs:96,100",
        Verified = "2026-08-10", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "TypeFilter is a ref struct over a Span; materialise during this game-thread call.")]
    private static SystemSnapshot Capture(CelestialSystem system)
    {
        var bodies = new List<IParentBody>();
        foreach (IParentBody body in system.All.OfType<IParentBody>())
            bodies.Add(body);

        // CelestialSystem.All is swap-removed when save loading destroys template vehicles. Its
        // order can therefore change in one process with unchanged content. Hashing that order
        // would split one system into two, so raw Id ordinal order is mandatory before any read.
        bodies.Sort(static (left, right) => string.CompareOrdinal(left.Id, right.Id));

        string rawSystemId = system.Id;
        string rawDisplayName = ResolveDisplayName(rawSystemId);
        string rawHome = system.HomeBody?.Id ?? string.Empty;
        var hashBodies = new List<SystemBodyHashInput>(bodies.Count);
        var wireBodies = new List<SystemBodySnapshot>(bodies.Count);
        foreach (IParentBody body in bodies)
            CaptureBody(body, hashBodies, wireBodies);

        var hashInput = new SystemHashInput(
            rawSystemId,
            rawDisplayName,
            rawHome,
            bodies.Count,
            hashBodies.AsReadOnly());
        string systemId = Ids.SystemId(hashInput);
        return new SystemSnapshot(
            systemId,
            rawSystemId,
            Ids.SanitizeSystemName(rawDisplayName),
            VehicleTelemetry.BodyName(system.HomeBody),
            wireBodies.AsReadOnly(),
            hashInput);
    }

    [KsaAnchor("SelectSystem.Systems; SystemInfo.Id; SystemInfo.DisplayName.Value",
        SourceFile = "KSA/SelectSystem.cs:18 / KSA/SystemInfo.cs:11,29", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Medium,
        Notes = "Launcher metadata owns display names; unresolved ids deliberately fall back to raw id.")]
    private static string ResolveDisplayName(string systemId)
    {
        foreach (SystemInfo info in SelectSystem.Systems)
        {
            if (string.Equals(info.Id, systemId, StringComparison.Ordinal))
                return info.DisplayName.Value;
        }
        return systemId;
    }

    [KsaAnchor("IParentBody.Mass/MeanRadius/SphereOfInfluence/GetAngularVelocity/GetAtmosphereReference/GetOceanReference",
        SourceFile = "KSA/IParentBody.cs:11-91 / KSA/Astronomical.cs:327-334", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Atmosphere Physical.Height and ocean Level are metres above mean radius.")]
    [KsaAnchor("IParentBody.GetCcf2Cce(SimTime.Zero); Celestial.GetRotationAxisCce(); Brutal.Numerics.doubleQuat.X/Y/Z/W",
        SourceFile = "KSA/IParentBody.cs:35 / KSA/Celestial.cs:575-578,622-625 / Brutal.Numerics/doubleQuat.cs:17-29",
        Verified = "2026-08-10", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Orientation is at SimTime.Zero; Celestial supplies its CCE rotation axis and StellarBody uses its fixed UnitZ axis.")]
    [KsaAnchor("Celestial.Parent/Orbit fields",
        SourceFile = "KSA/Celestial.cs:73,99-115 / KSA/Orbit.cs:1144-1174", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Angles are radians and converted here; TimeAtPeriapsis is absolute SimTime seconds.")]
    private static void CaptureBody(
        IParentBody body,
        List<SystemBodyHashInput> hashBodies,
        List<SystemBodySnapshot> wireBodies)
    {
        var astronomical = (Astronomical)body;
        IParentBody? parent = body is Celestial celestial ? celestial.Parent : null;
        string rawId = body.Id;
        string? rawParent = parent?.Id;
        string className = astronomical.Class;
        string kind = SystemBodyKind.FromClass(className, parent is StellarBody);
        int rank = Rank(body);

        double atmo = body.GetAtmosphereReference() is { } atmosphere
            ? (double)atmosphere.Physical.Height
            : 0.0;
        double ocean = body.GetOceanReference() is { } oceanReference
            ? (double)oceanReference.Level
            : 0.0;
        doubleQuat orientation = body.GetCcf2Cce(SimTime.Zero);
        Quat quaternion = Quat.Canonical(
            orientation.X, orientation.Y, orientation.Z, orientation.W);
        // Use Brutal.Numerics directly. KSA.Double3Ex also has a Bepu Symmetric3x3 overload;
        // extension-method resolution would unnecessarily pull BepuUtilities into the mod.
        double3 axis = body is Celestial rotating ? rotating.GetRotationAxisCce() : double3.UnitZ;
        var axisSnapshot = new Vec3(axis.X, axis.Y, axis.Z);

        SystemOrbitHashInput? hashOrbit = null;
        double? period = null;
        if (body is Celestial orbiting)
        {
            var candidate = new SystemOrbitHashInput(
                orbiting.SemiMajorAxis,
                orbiting.Eccentricity,
                RadiansToDegrees(orbiting.Inclination),
                RadiansToDegrees(orbiting.LongitudeOfAscendingNode),
                RadiansToDegrees(orbiting.ArgumentOfPeriapsis),
                orbiting.TimeAtPeriapsis.Seconds());
            if (OrbitIsFinite(candidate))
            {
                hashOrbit = candidate;
            }
            if (double.IsFinite(orbiting.Period))
                period = orbiting.Period;
        }

        var hashBody = new SystemBodyHashInput(
            rawId, rawParent, className, kind, rank, body.MeanRadius, body.Mass,
            body.SphereOfInfluence, atmo, ocean, body.GetAngularVelocity(), axisSnapshot,
            quaternion, hashOrbit, period);
        hashBodies.Add(hashBody);

        // +Inf is the documented stellar SOI and is the sole wire representation exception. Keep
        // it raw above for content identity, but the eventual JSON row must remain finite.
        double wireSoi = parent is null && double.IsPositiveInfinity(body.SphereOfInfluence)
            ? 0.0
            : body.SphereOfInfluence;
        wireBodies.Add(new SystemBodySnapshot(
            VehicleTelemetry.BodyName(body), Ids.SanitizeCatalogueName(rawId), className, kind, rank,
            parent is null ? null : VehicleTelemetry.BodyName(parent), body.MeanRadius, body.Mass,
            wireSoi, atmo, ocean, body.GetAngularVelocity(), axisSnapshot, quaternion,
            hashOrbit?.SemiMajorAxisM, hashOrbit?.Eccentricity, hashOrbit?.InclinationDeg,
            hashOrbit?.LongitudeAscendingNodeDeg, hashOrbit?.ArgumentPeriapsisDeg,
            hashOrbit?.TimeAtPeriapsis, period));
    }

    [KsaAnchor("Celestial.Parent",
        SourceFile = "KSA/Celestial.cs:73", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Depth is computed from each actual registered root; no singular root is assumed.")]
    private static int Rank(IParentBody body)
    {
        int rank = 0;
        IParentBody current = body;
        // Rank is edge depth, not a count of Celestial nodes. A modded Celestial implementation
        // may be parentless despite the stock property's non-null annotation; that object is a
        // root and must therefore remain rank zero.
        while (current is Celestial celestial && celestial.Parent is { } parent)
        {
            rank = checked(rank + 1);
            current = parent;
        }
        return rank;
    }

    private static bool OrbitIsFinite(SystemOrbitHashInput orbit)
        => double.IsFinite(orbit.SemiMajorAxisM)
           && double.IsFinite(orbit.Eccentricity)
           && double.IsFinite(orbit.InclinationDeg)
           && double.IsFinite(orbit.LongitudeAscendingNodeDeg)
           && double.IsFinite(orbit.ArgumentPeriapsisDeg)
           && double.IsFinite(orbit.TimeAtPeriapsis);

    private static double RadiansToDegrees(double radians) => radians * (180.0 / Math.PI);
}
