using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>
/// §7.2 threading contract: telemetry is latest-wins, discrete signals are
/// lossless. This is the seam WP8 calls from the game thread.
/// </summary>
public sealed class GameBridgeTests
{
    [Fact]
    public void PublishFrame_StampsAStrictlyIncreasingSequence()
    {
        var bridge = new GameBridge();

        TelemetryFrame first = bridge.PublishFrame(1.0, 100, [TestData.Snapshot()]);
        TelemetryFrame second = bridge.PublishFrame(1.5, 150, [TestData.Snapshot()]);

        Assert.Equal(1, first.Sequence);
        Assert.Equal(2, second.Sequence);
        Assert.Same(second, bridge.Frames.Current);
    }

    /// <summary>
    /// The load-bearing difference from the telemetry path: nothing is dropped, no matter how far
    /// behind the worker is. A dropped RUD is a permanently lost leaderboard entry.
    /// </summary>
    [Fact]
    public void Signals_AreLosslessAndOrdered()
    {
        var bridge = new GameBridge();

        for (int i = 0; i < 5000; i++)
            Assert.True(bridge.Signal(TestData.Impact(simT: i)));

        IReadOnlyList<GameSignal> drained = bridge.DrainSignals();

        Assert.Equal(5000, drained.Count);
        Assert.Equal(5000, bridge.SignalsWritten);
        Assert.Equal(0, bridge.SignalsDropped);
        for (int i = 0; i < drained.Count; i++)
            Assert.Equal(i, drained[i].SimT);
    }

    [Fact]
    public void EndFrame_WritesABoundaryMarkerInBand()
    {
        var bridge = new GameBridge();
        bridge.PublishFrame(1.0, 100, [TestData.Snapshot()]);
        bridge.Signal(TestData.Impact());
        bridge.EndFrame(1.0, 100);

        IReadOnlyList<GameSignal> drained = bridge.DrainSignals();

        Assert.Equal(2, drained.Count);
        Assert.IsType<ImpactSignal>(drained[0]);
        FrameBoundarySignal boundary = Assert.IsType<FrameBoundarySignal>(drained[1]);
        Assert.Equal(1, boundary.Sequence);
    }

    [Fact]
    public void Signal_RefusesNullWithoutThrowing()
    {
        var bridge = new GameBridge();

        Assert.False(bridge.Signal(null!), "a Harmony patch body must never be able to kill the bridge");
        Assert.Equal(0, bridge.SignalsWritten);
    }

    [Fact]
    public void Signal_AfterComplete_IsCountedAsDropped()
    {
        var bridge = new GameBridge();
        bridge.Complete();

        Assert.False(bridge.Signal(TestData.Impact()));
        Assert.Equal(1, bridge.SignalsDropped);
    }

    [Fact]
    public void DrainSignals_OnAnEmptyBridge_AllocatesNothingSurprising()
    {
        var bridge = new GameBridge();

        Assert.Empty(bridge.DrainSignals());
    }
}
