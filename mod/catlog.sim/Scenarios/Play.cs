using System;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>Shared helpers for writing a flight profile as a sequence of frames.</summary>
internal static class Play
{
    /// <summary>
    /// One frame, in sim seconds. Matches the D15 sampling cadence exactly, so the runner's real
    /// <see cref="MeowSci.Catlog.Lib.Telemetry.SampleClock"/> passes every frame through rather
    /// than dropping most of them — a scenario that stepped faster would silently lose samples
    /// and make its window arithmetic unpredictable.
    /// </summary>
    internal const double Dt = 1.0 / MeowSci.Catlog.Lib.Wire.DefaultSampleHz;

    /// <summary>Linear interpolation, clamped to the endpoints.</summary>
    /// <param name="from">Value at <c>u = 0</c>.</param>
    /// <param name="to">Value at <c>u = 1</c>.</param>
    /// <param name="u">Position along the segment.</param>
    /// <returns>The interpolated value.</returns>
    internal static double Lerp(double from, double to, double u)
        => from + ((to - from) * Math.Clamp(u, 0.0, 1.0));

    /// <summary>Smoothstep, for profiles that should not start and stop with a corner.</summary>
    /// <param name="u">Position along the segment.</param>
    /// <returns>The eased position.</returns>
    internal static double Ease(double u)
    {
        double c = Math.Clamp(u, 0.0, 1.0);
        return c * c * (3.0 - (2.0 * c));
    }

    /// <summary>Kinetic energy in joules.</summary>
    /// <param name="massKg">Mass, in kilograms.</param>
    /// <param name="speedMs">Speed, in metres per second.</param>
    /// <returns>The energy.</returns>
    internal static double Energy(double massKg, double speedMs) => 0.5 * massKg * speedMs * speedMs;
}
