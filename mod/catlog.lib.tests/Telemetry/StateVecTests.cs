using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

public sealed class StateVecTests
{
    [Fact]
    public void FiniteOrNull_PreservesALegitimateOrigin()
    {
        StateVec state = Assert.IsType<StateVec>(StateVec.FiniteOrNull(0, 0, 0, 0, 0, 0));

        Assert.Equal(new Vec3(0, 0, 0), state.Pos);
        Assert.Equal(new Vec3(0, 0, 0), state.Vel);
    }

    [Theory]
    [InlineData(double.NaN)]
    [InlineData(double.PositiveInfinity)]
    [InlineData(double.NegativeInfinity)]
    public void FiniteOrNull_OmitsTheWholeStateWhenAnyComponentIsNonFinite(double nonFinite)
    {
        for (int component = 0; component < 6; component++)
        {
            double[] values = [1, 2, 3, 4, 5, 6];
            values[component] = nonFinite;

            Assert.Null(StateVec.FiniteOrNull(
                values[0], values[1], values[2], values[3], values[4], values[5]));
        }
    }
}
