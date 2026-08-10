using MeowSci.Catlog.Lib.Events;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Events;

public sealed class LocomotionModeNameTests
{
    [Theory]
    [InlineData("Mmu", "mmu")]
    [InlineData("Grounded", "grounded")]
    [InlineData("Airborne", "airborne")]
    [InlineData("Tumbling", "tumbling")]
    [InlineData("Rightening", "rightening")]
    [InlineData("Ladder", "ladder")]
    [InlineData(null, "unknown")]
    [InlineData("Swimming", "unknown")]
    [InlineData("99", "unknown")]
    public void FromGameName_IsTotal(string? mode, string expected)
        => Assert.Equal(expected, LocomotionModeName.FromGameName(mode));
}
