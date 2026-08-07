using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>
/// One game-thread sample pass: every live vehicle's telemetry at one sim instant, handed to the
/// worker as a single immutable object.
/// </summary>
/// <remarks>
/// Published through <see cref="Util.SnapshotStore"/> with a single reference swap, so the game
/// thread never waits on the worker and the worker never holds a reference the game thread
/// mutates. <b>Only passive telemetry rides here</b> — discrete
/// <see cref="Events.GameSignal"/>s go through a lossless channel instead
/// (<see cref="GameBridge"/> explains why).
/// </remarks>
/// <param name="Sequence">Strictly increasing publish counter. <see cref="Util.SnapshotStore"/> depends on it.</param>
/// <param name="SimT">Universe sim seconds at capture.</param>
/// <param name="WallMs">Client unix milliseconds at capture.</param>
/// <param name="Vehicles">One snapshot per successfully sampled vehicle. A vehicle whose read threw is <b>absent</b>, never zero-filled.</param>
public sealed record TelemetryFrame(
    long Sequence,
    double SimT,
    long WallMs,
    IReadOnlyList<TelemetrySnapshot> Vehicles)
{
    /// <summary>The pre-first-publish frame: sequence 0, no vehicles. The never-null seed.</summary>
    public static TelemetryFrame Empty { get; } = new(0, 0, 0, []);
}
