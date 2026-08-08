using System.Collections.Generic;
using System.Linq;
using MeowSci.Catlog.Lib.Detect;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §7.2 / §4.2: <c>vehicle.impact.survived</c> = no destruction of the same
/// vehicle in the same frame. Impacts are held one full frame so a manual destroy — which the game
/// applies in the input pass, after the solver pass — still counts (docs/ksa-integration.md §3).
/// </summary>
public sealed class ImpactCorrelatorTests
{
    [Fact]
    public void ImpactAlone_Survives()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(speedMs: 62));

        Assert.Empty(correlator.EndFrame()); // held one frame
        ResolvedImpact resolved = Assert.Single(correlator.EndFrame());

        Assert.True(resolved.Survived, "no destruction followed, so the lithobrake was survived");
        Assert.Equal(62, resolved.Signal.SpeedMs);
    }

    [Fact]
    public void ImpactWithSameFrameDestruction_DoesNotSurvive()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact());
        correlator.Destroyed("v1");

        Assert.Empty(correlator.EndFrame());
        ResolvedImpact resolved = Assert.Single(correlator.EndFrame());

        Assert.False(resolved.Survived);
    }

    /// <summary>
    /// The reason for the extra frame: <c>InputEvents.ApplyInputEvents</c> runs after
    /// <c>Universe.ApplyVehicleSolvers</c>, so a player-initiated destroy of a vehicle that just
    /// hit the ground lands a frame later than the impact.
    /// </summary>
    [Fact]
    public void ImpactWithNextFrameDestruction_DoesNotSurvive()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact());
        correlator.EndFrame();

        correlator.Destroyed("v1");
        ResolvedImpact resolved = Assert.Single(correlator.EndFrame());

        Assert.False(resolved.Survived);
    }

    [Fact]
    public void DestructionOfADifferentVehicle_DoesNotAffectTheVerdict()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(vehicleId: "lander"));
        correlator.Destroyed("booster");

        correlator.EndFrame();
        ResolvedImpact resolved = Assert.Single(correlator.EndFrame());

        Assert.True(resolved.Survived);
    }

    [Fact]
    public void Splash_IsCorrelatedLikeAnImpactAndIsNeverALaunchPad()
    {
        var correlator = new ImpactCorrelator();
        correlator.Splash(new MeowSci.Catlog.Lib.Events.SplashSignal(
            SimT: 12, WallMs: TestData.WallMs, VehicleId: "v1",
            SpeedMs: 31, EnergyJ: 480_000, Body: "earth", CrewCount: 1));

        correlator.EndFrame();
        ResolvedImpact resolved = Assert.Single(correlator.EndFrame());

        Assert.True(resolved.Survived);
        Assert.False(resolved.Signal.LaunchPad);
        Assert.Equal(31, resolved.Signal.SpeedMs);
    }

    [Fact]
    public void MultipleImpactsInOneFrame_AllResolveInOrder()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(vehicleId: "a", speedMs: 10));
        correlator.Impact(TestData.Impact(vehicleId: "b", speedMs: 20));
        correlator.Destroyed("b");

        correlator.EndFrame();
        IReadOnlyList<ResolvedImpact> resolved = correlator.EndFrame();

        Assert.Equal(2, resolved.Count);
        Assert.True(resolved[0].Survived);
        Assert.False(resolved[1].Survived);
        Assert.Equal(10, resolved[0].Signal.SpeedMs);
    }

    [Fact]
    public void Drain_ResolvesEverythingOutstandingImmediately()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(vehicleId: "a"));
        correlator.EndFrame();
        correlator.Impact(TestData.Impact(vehicleId: "b"));

        Assert.Equal(2, correlator.Outstanding);
        IReadOnlyList<ResolvedImpact> drained = correlator.Drain();

        Assert.Equal(2, drained.Count);
        Assert.Equal(0, correlator.Outstanding);
        Assert.Empty(correlator.Drain());
    }

    /// <summary>
    /// A flight that ends takes its own outstanding impacts with it and leaves everyone else's
    /// alone — the verdict cannot change after the flight is over, and waiting would resolve them
    /// against a retired flight id.
    /// </summary>
    [Fact]
    public void DrainFor_TakesOneVehiclesImpactsAndLeavesTheRest()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(vehicleId: "a", speedMs: 10));
        correlator.Impact(TestData.Impact(vehicleId: "b", speedMs: 20));
        correlator.EndFrame();
        correlator.Impact(TestData.Impact(vehicleId: "a", speedMs: 30));
        correlator.Destroyed("a");

        IReadOnlyList<ResolvedImpact> drained = correlator.DrainFor("a");

        Assert.Equal(2, drained.Count);
        Assert.Equal([10, 30], drained.Select(static r => r.Signal.SpeedMs).ToArray());
        Assert.All(drained, static r => Assert.False(r.Survived));

        // b is untouched, both in count and in order.
        Assert.Equal(1, correlator.Outstanding);
        ResolvedImpact remaining = Assert.Single(correlator.Drain());
        Assert.Equal("b", remaining.Signal.VehicleId);
        Assert.True(remaining.Survived);
    }

    [Fact]
    public void DrainFor_AnUnknownVehicle_IsEmptyAndHarmless()
    {
        var correlator = new ImpactCorrelator();
        correlator.Impact(TestData.Impact(vehicleId: "a"));

        Assert.Empty(correlator.DrainFor("nobody"));
        Assert.Equal(1, correlator.Outstanding);
    }

    [Fact]
    public void EmptyFrames_ProduceNothing()
    {
        var correlator = new ImpactCorrelator();

        Assert.Empty(correlator.EndFrame());
        Assert.Empty(correlator.EndFrame());
        Assert.Equal(0, correlator.Outstanding);
    }
}
