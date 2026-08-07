using System;
using System.Collections.Generic;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// The celestial bodies the harness flies around, and how sim time maps to the client wall clock.
/// </summary>
/// <remarks>
/// <c>catlog.sim</c>'s <see cref="SimBodies"/> has three, which is right for six hand-asserted
/// scenarios and too few here: <c>soi_bodies</c> counts <i>distinct</i> destinations, so a
/// three-body universe caps that board at three and never exercises the projector's per-body
/// bookkeeping. Body names are opaque to the server (§4.2); the atmosphere heights are what the
/// detector's boundary and orbit rules are exercised against, so they are explicit rather than
/// derived.
/// </remarks>
internal static class LoadBodies
{
    /// <summary>The home world, and the only one anything launches from.</summary>
    internal static SimBody Kerbin { get; } = SimBodies.Kerbin;

    /// <summary>Every body, home included.</summary>
    internal static IReadOnlyList<SimBody> All { get; } =
    [
        SimBodies.Kerbin,
        SimBodies.Mun,
        SimBodies.Duna,
        new SimBody("minmus", 0),
        new SimBody("ike", 0),
        new SimBody("eve", 90_000),
        new SimBody("laythe", 55_000),
        new SimBody("jool", 200_000),
        new SimBody("dres", 0),
        new SimBody("gilly", 0),
    ];

    /// <summary>The bodies an interplanetary flight can arrive at (everything but home).</summary>
    internal static IReadOnlyList<SimBody> Destinations { get; } =
    [
        SimBodies.Mun,
        SimBodies.Duna,
        new SimBody("minmus", 0),
        new SimBody("ike", 0),
        new SimBody("eve", 90_000),
        new SimBody("laythe", 55_000),
        new SimBody("jool", 200_000),
        new SimBody("dres", 0),
        new SimBody("gilly", 0),
    ];
}

/// <summary>
/// Maps sim seconds to the client unix milliseconds every envelope carries in <c>wall_t</c>.
/// </summary>
/// <remarks>
/// <para>
/// Anchored so a run's simulated span <i>ends</i> at "now": a six-hour run produces timestamps
/// covering the last six hours rather than the next six. Nothing on the server depends on it —
/// §4.1 makes <c>wall_t</c> untrusted and every read-API timestamp comes from <c>recv_time</c> —
/// but a harness that emitted clocks from the future would be a misleading model of the game, and
/// the point of this program is to look like play.
/// </para>
/// <para>
/// This is also why <c>catlog.sim</c>'s <see cref="SimClock"/> is not reused: its epoch is fixed
/// two hours in the past, which is exactly right for a scenario that compresses half an hour and
/// wrong for a run that compresses six.
/// </para>
/// </remarks>
internal sealed class LoadClock
{
    private readonly long _epochMs;

    /// <summary>Creates a clock whose simulated span ends now.</summary>
    /// <param name="durationSeconds">The simulated span, in sim seconds.</param>
    internal LoadClock(double durationSeconds)
        => _epochMs = DateTimeOffset.UtcNow.AddSeconds(-durationSeconds).ToUnixTimeMilliseconds();

    /// <summary>The client unix-millisecond stamp for a sim instant.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    internal long Wall(double simT) => _epochMs + (long)(simT * 1000.0);
}

/// <summary>Curve helpers, so a flight profile reads like a flight profile.</summary>
internal static class Curve
{
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

    /// <summary>A bell centred on <paramref name="peak"/> — max-Q, and the g spike of a reentry.</summary>
    /// <param name="u">Position along the segment.</param>
    /// <param name="peak">Where the maximum sits.</param>
    /// <param name="width">How sharp the bell is; smaller is sharper.</param>
    /// <returns>A multiplier in <c>(0, 1]</c>.</returns>
    internal static double Bell(double u, double peak, double width)
    {
        double d = (u - peak) / width;
        return Math.Exp(-d * d);
    }

    /// <summary>Kinetic energy in joules.</summary>
    /// <param name="massKg">Mass, in kilograms.</param>
    /// <param name="speedMs">Speed, in metres per second.</param>
    /// <returns>The energy.</returns>
    internal static double Energy(double massKg, double speedMs) => 0.5 * massKg * speedMs * speedMs;
}
