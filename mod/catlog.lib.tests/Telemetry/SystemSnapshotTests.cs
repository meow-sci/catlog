using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

public sealed class SystemSnapshotTests
{
    [Theory]
    [InlineData("StellarBody", false, "star")]
    [InlineData("PlanetaryBody", true, "planet")]
    [InlineData("TerrestrialBody", true, "planet")]
    [InlineData("AtmosphericBody", true, "planet")]
    [InlineData("PlanetaryBody", false, "moon")]
    [InlineData("TerrestrialBody", false, "moon")]
    [InlineData("AtmosphericBody", false, "moon")]
    [InlineData("MinorBody", false, "minor")]
    [InlineData("Asteroid", false, "minor")]
    [InlineData("Comet", false, "minor")]
    [InlineData("PeriodicComet", false, "minor")]
    [InlineData("InterstellarComet", false, "minor")]
    [InlineData("FutureModBody", true, "other")]
    public void KindMapping_IsExhaustiveAndUnknownSafe(
        string className,
        bool parentIsStellar,
        string expected)
    {
        Assert.Equal(expected, SystemBodyKind.FromClass(className, parentIsStellar));
    }

    [Fact]
    public void QuaternionCanonicalization_PinsIdentityNegationAndHalfTurn()
    {
        Assert.Equal(new Quat(0, 0, 0, 1), Quat.Canonical(0, 0, 0, 2));
        Assert.Equal(
            Quat.Canonical(1, 2, 3, 4),
            Quat.Canonical(-1, -2, -3, -4));
        Assert.Equal(new Quat(1, 0, 0, 0), Quat.Canonical(-2, 0, 0, 0));
        Assert.Equal(0L, BitConverter.DoubleToInt64Bits(Quat.Canonical(-0.0, 0, 0, -2).X));
    }

    [Fact]
    public void QuaternionCanonicalization_RetainsInvalidInputForHonestHashing()
    {
        Quat invalid = Quat.Canonical(double.NaN, 0, 0, 1);
        Assert.True(double.IsNaN(invalid.X));
    }

    [Fact]
    public void SnapshotModelsAForestAndRetainsDepthWithinEachRoot()
    {
        IReadOnlyList<SystemBodySnapshot> bodies = new List<SystemBodySnapshot>
        {
            Body("alpha", null, 0),
            Body("alpha-moon", "alpha", 1),
            Body("beta", null, 0),
            Body("beta-moon", "beta", 1),
        }.AsReadOnly();
        var hash = new SystemHashInput("two-roots", "Two Roots", "alpha", 4, Array.Empty<SystemBodyHashInput>());
        var snapshot = new SystemSnapshot("0000000000000000", "two-roots", "Two Roots", "alpha", bodies, hash);

        Assert.Equal(new[] { "alpha", "beta" }, snapshot.Roots);
        Assert.Equal(0, bodies[0].Rank);
        Assert.Equal(1, bodies[1].Rank);
        Assert.Equal(0, bodies[2].Rank);
        Assert.Equal(1, bodies[3].Rank);
    }

    private static SystemBodySnapshot Body(string id, string? parent, int rank)
        => new(
            id, id, "Future", "other", rank, parent, 1, 1, 1, 0, 0, 0,
            new Vec3(0, 0, 1), new Quat(0, 0, 0, 1),
            null, null, null, null, null, null, null);
}
