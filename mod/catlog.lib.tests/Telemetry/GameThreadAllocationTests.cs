using System;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>
/// §7.2's governing requirement, as a number: <b>nothing that costs the player frames may run on
/// the game thread</b>. Allocation is the part of that a unit test can hold shut — every byte
/// allocated inside a frame is a byte the collector will come back for during one, and a per-tick
/// allocation that looks harmless at one vehicle is a per-tick allocation times the whole system.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="GC.GetAllocatedBytesForCurrentThread"/> is exact and thread-local, so these run
/// happily beside the rest of the suite. Each measurement is preceded by a warm-up pass: the first
/// call into a method JITs it, and tiered compilation, a dictionary's first bucket array and a
/// channel's first segment all allocate once. What is being pinned is the <i>steady state</i>.
/// </para>
/// <para>
/// <b>What this cannot reach.</b> The rest of the game-thread pass — <c>VehicleTelemetry.Sample</c>,
/// <c>PolledSignals.Poll</c> and the roster scan — lives in <c>mod/catlog</c>, which references KSA
/// and therefore cannot be loaded here (see <c>AssemblyGuardTests</c>). Those keep their guarantees
/// by construction instead: reused buffers, memoised per-vehicle constants, and a payload built
/// only on the tick it is emitted.
/// </para>
/// </remarks>
public sealed class GameThreadAllocationTests
{
    private const int Ticks = 2_000;

    /// <summary>
    /// The parts of a tick that run whether or not a sample is due: the rate limiter, the latest-wins
    /// frame slot, and the health latches the sampler clears and the runtime reads. All three must be
    /// free. A regression here is usually something that looks like nothing — a lambda, a
    /// <c>TaskCompletionSource</c> nobody waits on, a <c>ToArray()</c> on a status readout.
    /// </summary>
    [Fact]
    public void TheSteadyStateTickPathAllocatesNothing()
    {
        var clock = new SampleClock(2.0);
        var store = new SnapshotStore();
        var health = new SubsystemHealth();
        TelemetryFrame frame = TestData.Frame(1, TestData.Snapshot());

        Pass(clock, store, health, frame, warmup: true);
        long before = GC.GetAllocatedBytesForCurrentThread();
        Pass(clock, store, health, frame, warmup: false);
        long allocated = GC.GetAllocatedBytesForCurrentThread() - before;

        Assert.True(
            allocated == 0,
            $"the steady-state game-thread tick path allocated {allocated} bytes over {Ticks} ticks; "
            + "it must allocate nothing at all");
    }

    /// <summary>
    /// The hand-off itself does allocate, and has to: a frame is an immutable record that crosses to
    /// the worker, and a frame boundary is a signal in a lossless channel. What is pinned here is
    /// that it stays a small constant per tick rather than growing a per-vehicle or per-signal tail.
    /// </summary>
    [Fact]
    public void TheFrameHandOffStaysWithinASmallPerTickBudget()
    {
        // Two objects per tick plus the channel's amortised segment slots. Deliberately loose: this
        // is a tripwire for a new allocation, not a golden number to be tuned to the current one.
        const long BudgetPerTick = 256;

        var bridge = new GameBridge();
        TelemetrySnapshot[] vehicles = [TestData.Snapshot("v1"), TestData.Snapshot("v2")];

        HandOff(bridge, vehicles, warmup: true);
        long before = GC.GetAllocatedBytesForCurrentThread();
        HandOff(bridge, vehicles, warmup: false);
        long allocated = GC.GetAllocatedBytesForCurrentThread() - before;

        Assert.True(
            allocated <= BudgetPerTick * Ticks,
            $"the frame hand-off allocated {allocated / (double)Ticks:0.#} bytes per tick, over the "
            + $"{BudgetPerTick} byte budget");
    }

    private static void Pass(SampleClock clock, SnapshotStore store, SubsystemHealth health,
        TelemetryFrame frame, bool warmup)
    {
        int ticks = warmup ? 64 : Ticks;
        for (int i = 0; i < ticks; i++)
        {
            clock.Tick(0.5);
            health.Clear("sampler");
            if (!health.IsDead("outbox") && !health.IsDead("worker"))
                store.Publish(frame);
        }
    }

    private static void HandOff(GameBridge bridge, TelemetrySnapshot[] vehicles, bool warmup)
    {
        int ticks = warmup ? 64 : Ticks;
        for (int i = 0; i < ticks; i++)
        {
            bridge.PublishFrame(i * 0.5, TestData.WallMs, vehicles);
            bridge.EndFrame(i * 0.5, TestData.WallMs);

            // The worker's side of the channel, kept drained so the measurement is per-tick cost
            // rather than the cost of a queue nobody is reading.
            while (bridge.Signals.TryRead(out _))
            {
                // Discarded: this test is about the producer, not what the signals mean.
            }
        }
    }
}
