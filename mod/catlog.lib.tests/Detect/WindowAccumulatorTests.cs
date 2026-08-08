using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §7.2 / D15: the 30 s sim-time min/max/mean/last folds behind
/// <c>telemetry.window</c>.
/// </summary>
public sealed class WindowAccumulatorTests
{
    [Fact]
    public void AggregateMath_MatchesHandComputedValues()
    {
        var accumulator = new WindowAccumulator(10.0);

        // Altitudes 100, 300, 200, 400 → min 100, max 400, mean 250, last 400.
        double[] altitudes = [100, 300, 200, 400];
        for (int i = 0; i < altitudes.Length; i++)
        {
            accumulator.Add(TestData.Snapshot(
                simT: i, altitudeM: altitudes[i], surfaceSpeedMs: altitudes[i] / 10.0,
                orbitalSpeedMs: 7_800, accelMs2: i, massKg: 1_000 - i));
        }

        ClosedWindow closed = Assert.IsType<ClosedWindow>(
            accumulator.Add(TestData.Snapshot(simT: 10, altitudeM: 999)));
        TelemetryWindowPayload payload = closed.Payload;

        Assert.Equal(4, payload.N);
        Assert.Equal(0, payload.T0Sim);
        Assert.Equal(3, payload.T1Sim);
        Assert.Equal(new Agg(100, 400, 250, 400), payload.AltM);
        Assert.Equal(new Agg(10, 40, 25, 40), payload.SurfaceSpeedMs);
        Assert.Equal(new Agg(7_800, 7_800, 7_800, 7_800), payload.OrbitalSpeedMs);
        Assert.Equal(new Agg(0, 3, 1.5, 3), payload.AccelMs2);
        Assert.Equal(997, payload.MassKgLast);
    }

    /// <summary>
    /// The boundary is half-open: the sample at exactly <c>t0 + window</c> closes the window and
    /// opens the next one, so it is never counted twice.
    /// </summary>
    [Fact]
    public void WindowBoundaryAtExactlyThirtySeconds()
    {
        var accumulator = new WindowAccumulator(30.0);

        for (double t = 0; t < 30.0; t += 0.5)
            Assert.Null(accumulator.Add(TestData.Snapshot(simT: t)));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Add(TestData.Snapshot(simT: 30.0)));

        Assert.Equal(60, closed.Payload.N); // 0.0 … 29.5 at 2 Hz
        Assert.Equal(0.0, closed.Payload.T0Sim);
        Assert.Equal(29.5, closed.Payload.T1Sim);

        // The boundary sample opened the next window rather than joining the closed one.
        ClosedWindow next = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));
        Assert.Equal(1, next.Payload.N);
        Assert.Equal(30.0, next.Payload.T0Sim);
    }

    /// <summary>
    /// The peak-g rule from DECISIONS 2026-08-06: an all-zero <c>StructuralLoad</c> means "no data
    /// this step", so the fold must omit the field rather than report a fabricated 0.
    /// </summary>
    [Fact]
    public void PeakGAndMaxQ_AreOmittedWhenNoSampleCarriedAReading()
    {
        var accumulator = new WindowAccumulator(5.0);
        accumulator.Add(TestData.Snapshot(simT: 0));
        accumulator.Add(TestData.Snapshot(simT: 1));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));

        Assert.Null(closed.Payload.PeakG);
        Assert.Null(closed.Payload.MaxQPa);
    }

    [Fact]
    public void PeakGAndMaxQ_FoldOnlyOverSamplesThatHadAReading()
    {
        var accumulator = new WindowAccumulator(5.0);
        accumulator.Add(TestData.Snapshot(simT: 0)); // on rails: no reading
        accumulator.Add(TestData.Snapshot(simT: 1, peakG: 4.5, maxQPa: 12_000));
        accumulator.Add(TestData.Snapshot(simT: 2, peakG: 2.0, maxQPa: 30_000));
        accumulator.Add(TestData.Snapshot(simT: 3)); // back on rails

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));

        Assert.Equal(4.5, closed.Payload.PeakG);
        Assert.Equal(30_000, closed.Payload.MaxQPa);
        Assert.Equal(4, closed.Payload.N);
    }

    [Fact]
    public void WindowsAreIndependentPerVehicle()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(vehicleId: "a", simT: 0, altitudeM: 10));
        accumulator.Add(TestData.Snapshot(vehicleId: "b", simT: 0, altitudeM: 1_000));
        accumulator.Add(TestData.Snapshot(vehicleId: "a", simT: 1, altitudeM: 30));

        Assert.Equal(2, accumulator.OpenWindows);

        ClosedWindow a = Assert.IsType<ClosedWindow>(accumulator.Flush("a"));
        Assert.Equal(new Agg(10, 30, 20, 30), a.Payload.AltM);

        ClosedWindow b = Assert.IsType<ClosedWindow>(accumulator.Flush("b"));
        Assert.Equal(new Agg(1_000, 1_000, 1_000, 1_000), b.Payload.AltM);
    }

    [Fact]
    public void Flush_OnAVehicleWithNoOpenWindow_ReturnsNull()
    {
        var accumulator = new WindowAccumulator(10.0);

        Assert.Null(accumulator.Flush("nobody"));
    }

    [Fact]
    public void FlushAll_ClosesEveryOpenWindow()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(vehicleId: "a"));
        accumulator.Add(TestData.Snapshot(vehicleId: "b"));

        IReadOnlyList<ClosedWindow> closed = accumulator.FlushAll();

        Assert.Equal(2, closed.Count);
        Assert.Equal(0, accumulator.OpenWindows);
    }

    [Fact]
    public void Forget_DiscardsAPartialWindow()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(vehicleId: "a"));
        accumulator.Forget("a");

        Assert.Equal(0, accumulator.OpenWindows);
        Assert.Null(accumulator.Flush("a"));
    }

    [Fact]
    public void SimTimeGoingBackwards_StartsAFreshWindow()
    {
        var accumulator = new WindowAccumulator(30.0);
        accumulator.Add(TestData.Snapshot(simT: 100, altitudeM: 5_000));

        // A save load: the partial window spans two timelines and its mean would be meaningless.
        Assert.Null(accumulator.Add(TestData.Snapshot(simT: 5, altitudeM: 10)));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));
        Assert.Equal(1, closed.Payload.N);
        Assert.Equal(5, closed.Payload.T0Sim);
    }

    [Theory]
    [InlineData(0.0)]
    [InlineData(-1.0)]
    [InlineData(double.NaN)]
    public void InvalidWindowLength_Throws(double windowSeconds)
    {
        Assert.Throws<ArgumentOutOfRangeException>(() => new WindowAccumulator(windowSeconds));
    }
}
