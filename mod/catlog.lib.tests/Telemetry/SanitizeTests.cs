using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>INITIAL_IMPL_PLAN §7.2: NaN/Inf scrubbing at the capture boundary.</summary>
public sealed class SanitizeTests
{
    [Theory]
    [InlineData(0.0, 0.0)]
    [InlineData(1234.5, 1234.5)]
    [InlineData(-9.75, -9.75)]
    [InlineData(double.NaN, 0.0)]
    [InlineData(double.PositiveInfinity, 0.0)]
    [InlineData(double.NegativeInfinity, 0.0)]
    public void Finite_ZeroesNonFiniteValues(double input, double expected)
    {
        Assert.Equal(expected, Sanitize.Finite(input));
    }

    [Fact]
    public void Finite_CanSubstituteACallerChosenFallback()
    {
        Assert.Equal(-1.0, Sanitize.Finite(double.NaN, -1.0));
        Assert.Equal(7.0, Sanitize.Finite(7.0, -1.0));
    }

    [Fact]
    public void RadiusToAltitude_SubtractsTheMeanRadius()
    {
        Assert.Equal(200_000, Sanitize.RadiusToAltitude(6_571_000, 6_371_000));
    }

    [Fact]
    public void RadiusToAltitude_KeepsANegativeResult()
    {
        // A hyperbolic apoapsis is negative, not NaN (docs/ksa-integration.md B4). Callers branch
        // on the orbit class, so the sign must survive rather than be clamped.
        Assert.Equal(-1_000_000, Sanitize.RadiusToAltitude(5_371_000, 6_371_000));
    }

    [Theory]
    [InlineData(double.NaN, 6_371_000.0)]
    [InlineData(6_571_000.0, double.NaN)]
    [InlineData(double.PositiveInfinity, 6_371_000.0)]
    public void RadiusToAltitude_GuardsBothOperands(double radius, double meanRadius)
    {
        Assert.Equal(0.0, Sanitize.RadiusToAltitude(radius, meanRadius));
    }
}
