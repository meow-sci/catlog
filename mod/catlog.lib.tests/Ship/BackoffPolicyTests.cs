using System;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Ship;

/// <summary>
/// §4.5.3: exponential backoff <c>1 s · 2ⁿ</c> with full jitter, capped at
/// five minutes.
/// </summary>
public sealed class BackoffPolicyTests
{
    [Theory]
    [InlineData(0, 1)]
    [InlineData(1, 2)]
    [InlineData(2, 4)]
    [InlineData(3, 8)]
    [InlineData(8, 256)]
    public void CeilingDoublesEachAttempt(int attempt, double expectedSeconds)
    {
        Assert.Equal(expectedSeconds, BackoffPolicy.Ceiling(attempt).TotalSeconds);
    }

    [Theory]
    [InlineData(9)]
    [InlineData(20)]
    [InlineData(1000)]
    public void CeilingIsCappedAtFiveMinutes(int attempt)
    {
        Assert.Equal(Wire.BackoffCapSeconds, BackoffPolicy.Ceiling(attempt).TotalSeconds);
    }

    [Fact]
    public void NegativeAttemptIsTreatedAsTheFirst()
    {
        Assert.Equal(TimeSpan.FromSeconds(1), BackoffPolicy.Ceiling(-3));
    }

    /// <summary>
    /// Full jitter — a uniform draw over <c>[0, ceiling]</c>, not the ceiling itself. Every client
    /// that lost connectivity at the same moment would otherwise retry in lockstep.
    /// </summary>
    [Theory]
    [InlineData(0.0, 0.0)]
    [InlineData(0.5, 2.0)]
    [InlineData(1.0, 4.0)]
    public void DelayScalesTheCeilingByTheJitterDraw(double jitter, double expectedSeconds)
    {
        Assert.Equal(expectedSeconds, BackoffPolicy.Delay(2, jitter).TotalSeconds);
    }

    [Theory]
    [InlineData(-1.0)]
    [InlineData(5.0)]
    [InlineData(double.NaN)]
    public void JitterOutsideTheUnitRangeIsClamped(double jitter)
    {
        TimeSpan delay = BackoffPolicy.Delay(2, jitter);

        Assert.InRange(delay.TotalSeconds, 0, 4);
    }
}
