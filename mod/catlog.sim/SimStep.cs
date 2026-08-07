using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim;

/// <summary>
/// One simulated game frame: the sim instant, the passive telemetry sampled at it, and the
/// discrete signals the game raised during it (INITIAL_IMPL_PLAN §7.3).
/// </summary>
/// <remarks>
/// <para>
/// A step is the simulator's stand-in for one turn of the game loop, and the runner feeds it
/// through the seam WP8 will use verbatim: signals go to
/// <see cref="MeowSci.Catlog.Lib.Telemetry.GameBridge.Signal"/>, snapshots to
/// <see cref="MeowSci.Catlog.Lib.Telemetry.GameBridge.PublishFrame"/>, and every step closes with
/// <see cref="MeowSci.Catlog.Lib.Telemetry.GameBridge.EndFrame"/>.
/// </para>
/// <para>
/// <see cref="SimT"/> must increase monotonically across a scenario except where the scenario is
/// deliberately simulating a save load — the detector rebaselines on a backwards jump, which is a
/// behaviour worth exercising but never by accident.
/// </para>
/// </remarks>
/// <param name="SimT">Universe sim seconds at this frame.</param>
/// <param name="Snapshots">One snapshot per live vehicle, or empty for a signal-only frame.</param>
/// <param name="Signals">The discrete signals raised during the frame, in game-thread order.</param>
public sealed record SimStep(
    double SimT,
    IReadOnlyList<TelemetrySnapshot> Snapshots,
    IReadOnlyList<GameSignal> Signals)
{
    /// <summary>An empty frame at <paramref name="simT"/>.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>The step.</returns>
    public static SimStep At(double simT) => new(simT, [], []);

    /// <summary>Adds passive telemetry to this frame.</summary>
    /// <param name="snapshots">One snapshot per vehicle sampled successfully.</param>
    /// <returns>A new step carrying the snapshots.</returns>
    public SimStep With(params TelemetrySnapshot[] snapshots)
        => this with { Snapshots = Concat(Snapshots, snapshots) };

    /// <summary>Adds discrete signals to this frame.</summary>
    /// <param name="signals">The signals, in the order the game thread raised them.</param>
    /// <returns>A new step carrying the signals.</returns>
    public SimStep Emit(params GameSignal[] signals)
        => this with { Signals = Concat(Signals, signals) };

    private static IReadOnlyList<T> Concat<T>(IReadOnlyList<T> existing, T[] added)
    {
        if (added.Length == 0)
            return existing;
        if (existing.Count == 0)
            return added;

        var merged = new List<T>(existing.Count + added.Length);
        merged.AddRange(existing);
        merged.AddRange(added);
        return merged;
    }
}

/// <summary>
/// A scripted gameplay scenario: a sequence of frames plus the leaderboard values the events
/// those frames produce must land on (INITIAL_IMPL_PLAN §7.3).
/// </summary>
/// <remarks>
/// Scenarios never build <see cref="EventEnvelope"/>s. They emit exactly what a game thread emits —
/// telemetry snapshots and <see cref="GameSignal"/>s — and everything downstream is the real
/// <c>catlog.lib</c> pipeline, so a bug in the detector, the window accumulator, the impact
/// correlator, the outbox or the shipper fails a scenario instead of being simulated away.
/// </remarks>
public interface IScenario
{
    /// <summary>The scenario's CLI name, e.g. <c>hop-lithobrake</c>.</summary>
    string Name { get; }

    /// <summary>One line describing what the scenario plays out.</summary>
    string Summary { get; }

    /// <summary>The boards the scenario asserts, for <c>--list</c>.</summary>
    string Asserts { get; }

    /// <summary>The frames to feed through the pipeline, lazily generated.</summary>
    /// <returns>The frames, in increasing sim time.</returns>
    IEnumerable<SimStep> Steps();

    /// <summary>
    /// Checks the read API against what the scenario should have scored. Called only under
    /// <c>--assert</c>, after the runner has waited for the projector to catch up.
    /// </summary>
    /// <param name="api">The read/admin API client; also carries the pre-run baseline.</param>
    /// <param name="handle">The credential's handle — the player whose rows to check.</param>
    void Assert(ReadApiClient api, string handle);
}

/// <summary>
/// Maps sim time to the client wall clock the events carry in <c>wall_t</c>.
/// </summary>
/// <remarks>
/// The epoch is fixed once per process and set two hours in the past, so a compressed 30-minute
/// scenario produces timestamps that are plausibly recent rather than in the future. Nothing on
/// the server depends on this — §4.1 makes <c>wall_t</c> untrusted and every read-API timestamp
/// comes from <c>recv_time</c> — but a simulator that emitted implausible clocks would be a
/// misleading model of the game.
/// </remarks>
public static class SimClock
{
    private static readonly long EpochMs =
        DateTimeOffset.UtcNow.AddHours(-2).ToUnixTimeMilliseconds();

    /// <summary>The client unix-millisecond stamp for a sim instant.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    public static long Wall(double simT) => EpochMs + (long)(simT * 1000.0);
}
