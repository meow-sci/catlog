using System;
using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>A KSA-free three-component vector used by the immutable system catalogue.</summary>
public sealed record Vec3(double X, double Y, double Z);

/// <summary>The fixed semantic classification layered over KSA's opaque runtime class string.</summary>
public static class SystemBodyKind
{
    /// <summary>Maps every known class and preserves future classes as <c>other</c>.</summary>
    public static string FromClass(string className, bool parentIsStellar)
        => className switch
        {
            "StellarBody" => "star",
            "PlanetaryBody" or "TerrestrialBody" or "AtmosphericBody"
                => parentIsStellar ? "planet" : "moon",
            "MinorBody" or "Asteroid" or "Comet" or "PeriodicComet" or "InterstellarComet"
                => "minor",
            _ => "other",
        };
}

/// <summary>A KSA-free quaternion in explicit x/y/z/w component order.</summary>
public sealed record Quat(double X, double Y, double Z, double W)
{
    /// <summary>
    /// Returns the unit quaternion's unique q/-q representation. Invalid and zero quaternions are
    /// returned unchanged so the survey can retain an honest, deterministically hashable failure.
    /// </summary>
    public static Quat Canonical(double x, double y, double z, double w)
    {
        if (!double.IsFinite(x) || !double.IsFinite(y) || !double.IsFinite(z) || !double.IsFinite(w))
            return new Quat(x, y, z, w);

        double scale = Math.Max(Math.Max(Math.Abs(x), Math.Abs(y)), Math.Max(Math.Abs(z), Math.Abs(w)));
        if (scale == 0.0)
            return new Quat(x, y, z, w);

        x /= scale;
        y /= scale;
        z /= scale;
        w /= scale;
        double length = Math.Sqrt((x * x) + (y * y) + (z * z) + (w * w));

        x = Zero(x / length);
        y = Zero(y / length);
        z = Zero(z / length);
        w = Zero(w / length);

        // q and -q are the same orientation. W first is intentional; for a 180-degree rotation
        // W is zero, so X, Y and Z provide the deterministic tie-break in that order.
        double first = w != 0.0 ? w : x != 0.0 ? x : y != 0.0 ? y : z;
        if (first < 0.0)
            return new Quat(Zero(-x), Zero(-y), Zero(-z), Zero(-w));
        return new Quat(x, y, z, w);
    }

    private static double Zero(double value) => value == 0.0 ? 0.0 : value;
}

/// <summary>
/// The raw, unsanitised inputs to one body's content identity. These are deliberately separate
/// from <see cref="SystemBodySnapshot"/>, whose names are canonical wire join/display values.
/// </summary>
public sealed record SystemBodyHashInput(
    string Id,
    string? ParentId,
    string Class,
    string Kind,
    int Rank,
    double RadiusM,
    double MassKg,
    double SoiM,
    double AtmoM,
    double OceanM,
    double AngularVelocityRadS,
    Vec3 AxisCce,
    Quat CcfToCceT0,
    SystemOrbitHashInput? Orbit,
    double? PeriodS);

/// <summary>The all-or-nothing six-value orbital-shape group used by the system hash.</summary>
public sealed record SystemOrbitHashInput(
    double SemiMajorAxisM,
    double Eccentricity,
    double InclinationDeg,
    double LongitudeAscendingNodeDeg,
    double ArgumentPeriapsisDeg,
    double TimeAtPeriapsis);

/// <summary>The complete raw content identity input for a loaded celestial system.</summary>
public sealed record SystemHashInput(
    string SystemId,
    string DisplayName,
    string HomeBodyId,
    int BodyCount,
    IReadOnlyList<SystemBodyHashInput> Bodies);

/// <summary>One immutable, canonical wire-facing body row cached by the game-facing survey.</summary>
public sealed record SystemBodySnapshot(
    string Body,
    string Name,
    string Class,
    string Kind,
    int Rank,
    string? Parent,
    double RadiusM,
    double MassKg,
    double SoiM,
    double AtmoM,
    double OceanM,
    double AngularVelocityRadS,
    Vec3 AxisCce,
    Quat CcfToCceT0,
    double? SemiMajorAxisM,
    double? Eccentricity,
    double? InclinationDeg,
    double? LongitudeAscendingNodeDeg,
    double? ArgumentPeriapsisDeg,
    double? TimeAtPeriapsis,
    double? PeriodS);

/// <summary>
/// An immutable launch-time system survey. It carries canonical wire values and, separately, the
/// raw inputs that produced <see cref="SystemId"/>; no session, career or clock belongs here.
/// </summary>
public sealed record SystemSnapshot(
    string SystemId,
    string Id,
    string Name,
    string HomeBody,
    IReadOnlyList<SystemBodySnapshot> Bodies,
    SystemHashInput HashInput)
{
    /// <summary>The filtered celestial body count, never <c>CelestialSystem.Count</c>.</summary>
    public int BodyCount => Bodies.Count;

    /// <summary>Every parentless body in canonical body order; a system is a forest.</summary>
    public IReadOnlyList<string> Roots
    {
        get
        {
            var roots = new List<string>();
            foreach (SystemBodySnapshot body in Bodies)
            {
                if (body.Parent is null)
                    roots.Add(body.Body);
            }
            return roots.AsReadOnly();
        }
    }
}
