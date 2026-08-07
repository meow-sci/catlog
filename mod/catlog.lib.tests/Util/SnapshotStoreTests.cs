using System;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

/// <summary>
/// INITIAL_IMPL_PLAN §7.2: the game-thread → worker snapshot exchange (copied from gatOS).
/// </summary>
public sealed class SnapshotStoreTests
{
    [Fact]
    public void Current_StartsAtTheEmptyFrame()
    {
        var store = new SnapshotStore();

        Assert.Same(TelemetryFrame.Empty, store.Current);
        Assert.Equal(0, store.Current.Sequence);
        Assert.Empty(store.Current.Vehicles);
    }

    [Fact]
    public async Task WaitForNext_CompletesImmediately_WhenANewerFrameIsCurrent()
    {
        var store = new SnapshotStore();
        store.Publish(TestData.Frame(5, TestData.Snapshot()));

        TelemetryFrame frame = await store.WaitForNextAsync(0, CancellationToken.None);

        Assert.Equal(5, frame.Sequence);
    }

    [Fact]
    public async Task WaitForNext_ParksUntilPublish()
    {
        var store = new SnapshotStore();
        store.Publish(TestData.Frame(1, TestData.Snapshot()));

        ValueTask<TelemetryFrame> waiter = store.WaitForNextAsync(1, CancellationToken.None);
        Assert.False(waiter.IsCompleted, "must park until something newer is published");

        store.Publish(TestData.Frame(2, TestData.Snapshot()));
        TelemetryFrame frame = await waiter;

        Assert.Equal(2, frame.Sequence);
    }

    [Fact]
    public async Task WaitForNext_WakesEveryWaiter()
    {
        var store = new SnapshotStore();

        ValueTask<TelemetryFrame> a = store.WaitForNextAsync(0, CancellationToken.None);
        ValueTask<TelemetryFrame> b = store.WaitForNextAsync(0, CancellationToken.None);
        store.Publish(TestData.Frame(1, TestData.Snapshot()));

        Assert.Equal(1, (await a).Sequence);
        Assert.Equal(1, (await b).Sequence);
    }

    [Fact]
    public async Task WaitForNext_ObservesCancellation()
    {
        var store = new SnapshotStore();
        using var cts = new CancellationTokenSource();

        ValueTask<TelemetryFrame> waiter = store.WaitForNextAsync(0, cts.Token);
        await cts.CancelAsync();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(async () => await waiter);
    }

    /// <summary>
    /// Latest-wins is the whole point on this path: a worker that misses frames sees the newest
    /// one, not a queue. Signals must therefore not travel here (see GameBridge).
    /// </summary>
    [Fact]
    public async Task Publish_IsLatestWins()
    {
        var store = new SnapshotStore();
        store.Publish(TestData.Frame(1, TestData.Snapshot(simT: 1)));
        store.Publish(TestData.Frame(2, TestData.Snapshot(simT: 2)));
        store.Publish(TestData.Frame(3, TestData.Snapshot(simT: 3)));

        TelemetryFrame frame = await store.WaitForNextAsync(0, CancellationToken.None);

        Assert.Equal(3, frame.Sequence);
    }
}
