using System;
using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>How much of the surface a vehicle is touching, decoded from the situation name.</summary>
public enum SurfaceContact
{
    /// <summary>Airborne, in freefall, or on an unrecognised situation.</summary>
    None = 0,

    /// <summary>Touching terrain.</summary>
    Terrain = 1,

    /// <summary>Touching ocean.</summary>
    Ocean = 2,

    /// <summary>Touching both (dragging along a shoreline / bottomed out).</summary>
    TerrainAndOcean = 3,
}

/// <summary>
/// Predicates over the <c>situation</c> string without referencing the game.
/// </summary>
/// <remarks>
/// <para>
/// §4.2 makes <c>situation</c> an <b>open set</b> of lowercase strings, opaque to the server.
/// The game's enum is nevertheless a packed bitfield — <c>value = (surfaceContact &lt;&lt; 1) | onRails</c>
/// with <c>SurfaceContact { None, Terrain, Ocean, TerrainAndOcean }</c> (verified in
/// <c>docs/ksa-integration.md</c> §1, <c>KSA/SituationEx.cs:56,62</c>) — which yields the complete
/// eight-value table below. Encoding it here as a static map keyed by the lowercased name lets the
/// detector and <c>catlog.sim</c> reason about surface contact with zero KSA references.
/// </para>
/// <para>
/// Every lookup is total: an unknown situation reports no contact and off-rails rather than
/// throwing, so a future game build that adds a ninth value degrades instead of crashing. There
/// is deliberately no exhaustive <c>switch</c> anywhere in catlog over this set.
/// </para>
/// </remarks>
public static class SituationInfo
{
    private static readonly Dictionary<string, (SurfaceContact Contact, bool OnRails)> Table =
        new(StringComparer.OrdinalIgnoreCase)
        {
            ["maneuvering"] = (SurfaceContact.None, false),           // 0
            ["freefall"] = (SurfaceContact.None, true),               // 1
            ["rolling"] = (SurfaceContact.Terrain, false),            // 2
            ["landed"] = (SurfaceContact.Terrain, true),              // 3
            ["sailing"] = (SurfaceContact.Ocean, false),              // 4
            ["floating"] = (SurfaceContact.Ocean, true),              // 5
            ["dragging"] = (SurfaceContact.TerrainAndOcean, false),   // 6
            ["bottomed"] = (SurfaceContact.TerrainAndOcean, true),    // 7
        };

    /// <summary>True when <paramref name="situation"/> is one of the eight known values.</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>True when the situation is recognised.</returns>
    public static bool IsKnown(string? situation)
        => situation is not null && Table.ContainsKey(situation);

    /// <summary>The surface contact implied by <paramref name="situation"/>; <see cref="SurfaceContact.None"/> when unknown.</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>The decoded surface contact.</returns>
    public static SurfaceContact ContactOf(string? situation)
        => situation is not null && Table.TryGetValue(situation, out var info) ? info.Contact : SurfaceContact.None;

    /// <summary>True when the vehicle is touching terrain and/or ocean.</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>True when there is any surface contact.</returns>
    public static bool HasSurfaceContact(string? situation) => ContactOf(situation) != SurfaceContact.None;

    /// <summary>True when the vehicle is touching terrain.</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>True when terrain is being touched.</returns>
    public static bool HasTerrainContact(string? situation)
        => ContactOf(situation) is SurfaceContact.Terrain or SurfaceContact.TerrainAndOcean;

    /// <summary>True when the vehicle is touching ocean.</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>True when ocean is being touched.</returns>
    public static bool HasOceanContact(string? situation)
        => ContactOf(situation) is SurfaceContact.Ocean or SurfaceContact.TerrainAndOcean;

    /// <summary>True when the vehicle is on rails (analytic propagation rather than full physics).</summary>
    /// <param name="situation">Lowercase situation name.</param>
    /// <returns>True when the situation's on-rails bit is set; false when unknown.</returns>
    public static bool IsOnRails(string? situation)
        => situation is not null && Table.TryGetValue(situation, out var info) && info.OnRails;

    /// <summary>Normalises a raw situation name to the lowercase form the wire uses.</summary>
    /// <param name="situation">Raw situation name, typically a game enum's <c>ToString()</c>.</param>
    /// <returns>The lowercase name, or <c>"unknown"</c> for null/empty input.</returns>
    public static string Normalize(string? situation)
        => string.IsNullOrWhiteSpace(situation) ? "unknown" : situation.Trim().ToLowerInvariant();
}
