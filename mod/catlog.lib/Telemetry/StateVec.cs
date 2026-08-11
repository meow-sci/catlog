using System;
using System.Text.Json.Serialization;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>A body-centred inertial position and velocity at one sample time.</summary>
/// <param name="Pos">Position relative to the named parent body, in metres.</param>
/// <param name="Vel">Velocity relative to the named parent body, in metres per second.</param>
public sealed record StateVec(
    [property: JsonPropertyName("pos")] Vec3 Pos,
    [property: JsonPropertyName("vel")] Vec3 Vel)
{
    /// <summary>Creates a state only when all six components are finite.</summary>
    /// <returns>The complete state, or null; a partial/origin fallback is never fabricated.</returns>
    public static StateVec? FiniteOrNull(
        double px, double py, double pz, double vx, double vy, double vz)
        => double.IsFinite(px) && double.IsFinite(py) && double.IsFinite(pz)
           && double.IsFinite(vx) && double.IsFinite(vy) && double.IsFinite(vz)
            ? new StateVec(new Vec3(px, py, pz), new Vec3(vx, vy, vz))
            : null;
}
