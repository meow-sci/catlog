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

    // Null means "nobody is parked". The shipped worker never awaits — it reads Current when a
    // FrameBoundarySignal comes out of the lossless channel — so on the path that actually runs in
    // the game there is no waiter, and minting a TaskCompletionSource per publish was one
    // allocation per sample tick that nothing ever completed a wait on. A waiter installs one
    // lazily; a publish only completes one that is already there.
    private volatile TaskCompletionSource? _signal;

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
        // Publish the frame first, then take the waiter slot: a waiter that installed its signal
        // before this ran wakes and re-reads _current, and one that installs after it re-reads
        // _current itself before parking (see WaitForNextAsync), so neither ordering can miss a
        // frame. Clearing the slot rather than replacing it is what makes the no-waiter case free.
        Interlocked.Exchange(ref _signal, null)?.SetResult();
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

            TaskCompletionSource signal = InstallSignal();
            // Re-check after installing the signal: a publish between the two volatile reads
            // would otherwise be missed (it saw an empty slot, or completed an earlier signal).
            frame = _current;
            if (frame.Sequence > afterSequence)
                return frame;

            await signal.Task.WaitAsync(ct).ConfigureAwait(false);
        }
    }

    // The slot, creating it if this is the first waiter since the last publish. Every waiter shares
    // one signal, which is what makes "wake everybody" a single SetResult.
    private TaskCompletionSource InstallSignal()
    {
        TaskCompletionSource? existing = _signal;
        if (existing is not null)
            return existing;

        var created = NewSignal();
        return Interlocked.CompareExchange(ref _signal, created, null) ?? created;
    }

    // RunContinuationsAsynchronously is mandatory: without it SetResult() on the game thread
    // would inline-run every waiter's continuation on the game thread.
    private static TaskCompletionSource NewSignal()
        => new(TaskCreationOptions.RunContinuationsAsynchronously);
}
