namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>
/// Value scrubbing for the game-side sampler. Copied from
/// <c>gatOS/gatOS.SimFs/Telemetry/Sanitize.cs</c>.
/// </summary>
/// <remarks>
/// JSON has no NaN or Infinity literal: <c>System.Text.Json</c> writes them as bare
/// <c>NaN</c>/<c>Infinity</c> tokens, which are invalid JSON and would earn the whole batch a
/// <c>400 malformed_batch</c> from the Go server. Scrubbing at the <b>capture</b> boundary rather
/// than at serialization means every downstream consumer — detector, window folds, NDJSON writer —
/// can assume finite doubles.
/// </remarks>
public static class Sanitize
{
    /// <summary>The value, or 0 when NaN or ±∞.</summary>
    /// <param name="value">The raw reading.</param>
    /// <returns>A finite double.</returns>
    public static double Finite(double value) => double.IsFinite(value) ? value : 0;

    /// <summary>The value, or <paramref name="fallback"/> when NaN or ±∞.</summary>
    /// <param name="value">The raw reading.</param>
    /// <param name="fallback">Value to substitute for non-finite input.</param>
    /// <returns>A finite double.</returns>
    public static double Finite(double value, double fallback)
        => double.IsFinite(value) ? value : fallback;

    /// <summary>
    /// Converts a from-body-centre radius (the game's <c>Orbit.Apoapsis</c>/<c>Periapsis</c>
    /// convention) to an above-surface altitude. Both operands are guarded: a missing parent
    /// yielding <c>meanRadiusMeters == 0</c> would otherwise silently report "altitude = radius".
    /// </summary>
    /// <param name="radiusMeters">Radius from the body's centre, in metres.</param>
    /// <param name="meanRadiusMeters">The body's mean radius, in metres.</param>
    /// <returns>Altitude above the mean radius, or 0 when either input is non-finite.</returns>
    /// <remarks>
    /// Note this returns a <b>negative</b> altitude for a hyperbolic apoapsis (which is itself
    /// negative — <c>docs/ksa-integration.md</c> B4). That is correct and deliberate: consumers
    /// branch on <see cref="OrbitClass"/>, never on the sign or NaN-ness of the apsis.
    /// </remarks>
    public static double RadiusToAltitude(double radiusMeters, double meanRadiusMeters)
        => double.IsFinite(radiusMeters) && double.IsFinite(meanRadiusMeters)
            ? radiusMeters - meanRadiusMeters
            : 0;
}
