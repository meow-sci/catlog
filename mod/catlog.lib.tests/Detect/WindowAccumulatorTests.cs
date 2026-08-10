using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
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

    // ----- radar altitude: folded only over samples that had one ------------------------

    /// <summary>
    /// A window that spent every sample on rails or in orbit has no terrain reading at all, and a
    /// mean that counted those samples as 0 would report a craft skimming the ground. Same rule as
    /// <c>peak_g</c>, and the stakes are higher.
    /// </summary>
    [Fact]
    public void RadarAltitude_IsOmittedWhenNoSampleCarriedOne()
    {
        var accumulator = new WindowAccumulator(10.0);
        for (int i = 0; i < 4; i++)
            accumulator.Add(TestData.Snapshot(simT: i, radarAltM: null));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Add(TestData.Snapshot(simT: 10)));

        Assert.Null(closed.Payload.RadarAltM);
    }

    /// <summary>
    /// The mixed case is the one that matters: a climb out of the physics bubble stops producing
    /// readings part-way through the window, and the aggregate must describe the samples that had
    /// one rather than being diluted by the ones that did not.
    /// </summary>
    [Fact]
    public void RadarAltitude_FoldsOnlyTheSamplesThatHadAReading()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(simT: 0, radarAltM: 10));
        accumulator.Add(TestData.Snapshot(simT: 1, radarAltM: null));
        accumulator.Add(TestData.Snapshot(simT: 2, radarAltM: 30));
        accumulator.Add(TestData.Snapshot(simT: 3, radarAltM: 20));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Add(TestData.Snapshot(simT: 10)));

        // Mean 20 over three readings, not 15 over four.
        Assert.Equal(new Agg(10, 30, 20, 20), closed.Payload.RadarAltM);
        Assert.Equal(4, closed.Payload.N);
    }

    [Fact]
    public void WarpMax_IsTheHighestSimulationSpeedSeen()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(simT: 0, warpFactor: 1));
        accumulator.Add(TestData.Snapshot(simT: 1, warpFactor: 1_000));
        accumulator.Add(TestData.Snapshot(simT: 2, warpFactor: 50));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Add(TestData.Snapshot(simT: 10)));

        Assert.Equal(1_000, closed.Payload.WarpMax);
    }

    /// <summary>An unwarped window still says so out loud: 1, never 0.</summary>
    [Fact]
    public void WarpMax_IsOneForARealTimeWindow()
    {
        var accumulator = new WindowAccumulator(10.0);
        accumulator.Add(TestData.Snapshot(simT: 0));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Add(TestData.Snapshot(simT: 10)));

        Assert.Equal(1, closed.Payload.WarpMax);
    }

    [Fact]
    public void State_IsTheLastSamplesCompleteReading_NotAnAggregate()
    {
        var accumulator = new WindowAccumulator(10.0);
        var first = new StateVec(new Vec3(1, 2, 3), new Vec3(4, 5, 6));
        var last = new StateVec(new Vec3(10, 20, 30), new Vec3(40, 50, 60));
        accumulator.Add(TestData.Snapshot(simT: 0, state: first));
        accumulator.Add(TestData.Snapshot(simT: 1, state: last));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));

        Assert.Same(last, closed.Payload.State);
    }

    [Fact]
    public void BodyChange_WithUnreadableFinalStateClearsTheOldParentState()
    {
        var accumulator = new WindowAccumulator(10.0);
        var earth = new StateVec(new Vec3(1, 2, 3), new Vec3(4, 5, 6));
        accumulator.Add(TestData.Snapshot(simT: 0, body: "earth", state: earth));
        accumulator.Add(TestData.Snapshot(simT: 1, body: "duna", state: null));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));

        Assert.Equal("duna", closed.Payload.Body);
        Assert.Null(closed.Payload.State);
    }

    [Fact]
    public void BodyChange_CarriesOnlyTheNewParentsFinalState()
    {
        var accumulator = new WindowAccumulator(10.0);
        var earth = new StateVec(new Vec3(1, 2, 3), new Vec3(4, 5, 6));
        var duna = new StateVec(new Vec3(7, 8, 9), new Vec3(10, 11, 12));
        accumulator.Add(TestData.Snapshot(simT: 0, body: "earth", state: earth));
        accumulator.Add(TestData.Snapshot(simT: 1, body: "duna", state: duna));

        ClosedWindow closed = Assert.IsType<ClosedWindow>(accumulator.Flush("v1"));

        Assert.Equal("duna", closed.Payload.Body);
        Assert.Same(duna, closed.Payload.State);
    }
}
