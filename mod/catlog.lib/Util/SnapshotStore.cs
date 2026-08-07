using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// The single-writer snapshot exchange between the game-thread sampler and the worker.
/// <see cref="Publish"/> swaps one volatile reference and wakes waiters; readers never lock.
/// </summary>
/// <remarks>
/// <para>
/// Copied from <c>gatOS/gatOS.SimFs/Snapshots/SnapshotStore.cs</c> (57 lines), retyped from
/// <c>SimSnapshot</c> to <see cref="TelemetryFrame"/>.
/// </para>
/// <para>
/// <b>Latest-wins, and that is only correct for telemetry.</b> A worker that falls behind silently
/// skips intermediate frames — fine for passive samples, where a dropped sample costs resolution
/// and nothing else, because the detector compares prev/curr. It is <b>wrong</b> for discrete
/// scoring signals: a dropped RUD or impact is a permanently lost leaderboard entry. Those go
/// through <see cref="Telemetry.GameBridge"/>'s unbounded channel instead.
/// </para>
/// </remarks>
public sealed class SnapshotStore
{
    private volatile TelemetryFrame _current = TelemetryFrame.Empty;
    private volatile TaskCompletionSource _signal = NewSignal();

    /// <summary>The latest published frame; never null, starts at <see cref="TelemetryFrame.Empty"/>.</summary>
    public TelemetryFrame Current => _current;

    /// <summary>
    /// Publishes a frame. Single writer by contract (the game-thread sampler);
    /// <paramref name="frame"/>'s sequence must exceed the current one.
    /// </summary>
    /// <param name="frame">The frame to publish.</param>
    public void Publish(TelemetryFrame frame)
    {
        _current = frame;
        // Swap first, then complete: a waiter that captured the old signal wakes and re-reads
        // _current; one that already captured the new signal waits for the next publish.
        TaskCompletionSource completed = Interlocked.Exchange(ref _signal, NewSignal());
        completed.SetResult();
    }

    /// <summary>
    /// Completes with the first frame whose sequence exceeds <paramref name="afterSequence"/>
    /// (immediately when one is already current).
    /// </summary>
    /// <param name="afterSequence">The last sequence the caller has already consumed.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The next frame.</returns>
    public async ValueTask<TelemetryFrame> WaitForNextAsync(long afterSequence, CancellationToken ct)
    {
        while (true)
        {
            ct.ThrowIfCancellationRequested();
            TelemetryFrame frame = _current;
            if (frame.Sequence > afterSequence)
                return frame;

            TaskCompletionSource signal = _signal;
            // Re-check after capturing the signal: a publish between the two volatile reads
            // would otherwise be missed (it completed the previous signal, not this one).
            frame = _current;
            if (frame.Sequence > afterSequence)
                return frame;

            await signal.Task.WaitAsync(ct).ConfigureAwait(false);
        }
    }

    // RunContinuationsAsynchronously is mandatory: without it SetResult() on the game thread
    // would inline-run every waiter's continuation on the game thread.
    private static TaskCompletionSource NewSignal()
        => new(TaskCreationOptions.RunContinuationsAsynchronously);
}
