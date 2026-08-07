using System;

namespace MeowSci.Catlog;

/// <summary>
/// How likely a KSA member is to move, be renamed, or change shape across game builds. The
/// defaults encode the 2026-08 survey in <c>docs/ksa-integration.md</c>.
/// </summary>
public enum ChurnRisk
{
    /// <summary>Core vehicle/orbit/time state and the struct-of-arrays state pattern. Byte-identical 5117 → 5168.</summary>
    Low,

    /// <summary>InputEvents-mediated operations, per-module controllers, docking, staging.</summary>
    Medium,

    /// <summary>Anything that appeared in the current build, or is reached by reflection, or lives in FX/template internals.</summary>
    High,
}

/// <summary>
/// Marks a member as a binding point to a specific KSA API. Every member in
/// <c>mod/catlog</c> that touches a KSA type carries one, so the game-bump playbook is a grep:
/// when a new decomp drop breaks the build, the failing <c>[KsaAnchor]</c> sites are the work list,
/// and the <c>Verified</c>/<c>GameVersion</c> fields say when each was last checked and against
/// what. Purely documentary — no runtime behaviour.
/// </summary>
/// <remarks>
/// Copied from <c>gatOS/gatOS.GameMod/Game/Ksa/KsaAnchor.cs</c>, which is where the pattern was
/// proven across three KSA build upgrades. The <c>Notes</c> field is where the unit gotchas live:
/// <c>Orbit.Inclination</c> is radians, <c>Orbit.Apoapsis</c>/<c>Periapsis</c> are radii from body
/// centre, <c>Situation</c> is a packed bitfield, <c>StructuralLoad</c> is all-zero off full
/// physics. Those are exactly the facts a rename does not break and a semantics change does.
/// </remarks>
[AttributeUsage(
    AttributeTargets.Method | AttributeTargets.Property | AttributeTargets.Class
    | AttributeTargets.Field | AttributeTargets.Constructor,
    AllowMultiple = true)]
public sealed class KsaAnchorAttribute : Attribute
{
    /// <summary>Creates an anchor.</summary>
    /// <param name="member">The KSA member this code binds to, e.g. <c>"Vehicle.GetBarometricAltitude()"</c>.</param>
    public KsaAnchorAttribute(string member) => Member = member;

    /// <summary>The KSA member this code binds to.</summary>
    public string Member { get; }

    /// <summary>Source file under the decompiled tree where the member lives.</summary>
    public string SourceFile { get; init; } = "";

    /// <summary>ISO date the binding was last verified against the sources.</summary>
    public string Verified { get; init; } = "";

    /// <summary>Game build string at verification time.</summary>
    public string GameVersion { get; init; } = "";

    /// <summary>The member's churn risk; drives re-verification priority.</summary>
    public ChurnRisk Risk { get; init; } = ChurnRisk.Medium;

    /// <summary>Free-form notes: units, NaN behaviour, gotchas, the <c>docs/ksa-integration.md</c> row.</summary>
    public string Notes { get; init; } = "";
}
