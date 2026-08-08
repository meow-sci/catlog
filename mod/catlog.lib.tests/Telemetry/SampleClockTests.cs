using System;
using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>§7.2: the dt-accumulating, drop-not-backfill sample rate limiter.</summary>
public sealed class SampleClockTests
{
    [Fact]
    public void FiresAtTheConfiguredRate()
    {
        var clock = new SampleClock(2.0);

        int fired = 0;
        for (int i = 0; i < 100; i++) // 100 frames of 1/60 s ≈ 1.667 s
        {
            if (clock.Tick(1.0 / 60.0))
                fired++;
        }

        Assert.True(fired is 3 or 4, $"≈2 Hz over 1.67 s, got {fired}");
    }

    [Fact]
    public void LongFrame_FiresOnceAndDropsMissedIntervals()
    {
        var clock = new SampleClock(2.0);

        Assert.True(clock.Tick(5.0), "a 5 s frame is well past one 0.5 s interval");
        Assert.False(clock.Tick(0.01), "no burst of catch-up samples after the hitch");
        Assert.False(clock.Tick(0.01), "still nothing: the backlog was dropped, not queued");
    }

    [Fact]
    public void SubIntervalPhase_IsKept()
    {
        var clock = new SampleClock(2.0);

        Assert.True(clock.Tick(0.6), "0.6 s is past the 0.5 s interval");
        Assert.False(clock.Tick(0.3), "0.1 s carried over + 0.3 s = 0.4 s, still short");
        Assert.True(clock.Tick(0.15), "0.4 + 0.15 crosses 0.5 s — phase was preserved");
    }

    [Fact]
    public void GarbageDt_IsIgnored()
    {
        var clock = new SampleClock(2.0);

        Assert.False(clock.Tick(double.NaN), "KSA can hand out a NaN dt on the first frame after a load");
        Assert.False(clock.Tick(double.PositiveInfinity), "an infinite dt must not fire");
        Assert.False(clock.Tick(-10.0), "a negative dt must not fire");
        Assert.True(clock.Tick(0.5), "a real dt still works after the garbage");
    }

    [Fact]
    public void SetRate_RetunesWithoutGlitching()
    {
        var clock = new SampleClock(1.0);
        Assert.False(clock.Tick(0.4), "0.4 s is short of the 1 s interval");

        clock.SetRate(2.0);
        Assert.True(clock.Tick(0.15), "accumulated 0.55 s now clears the new 0.5 s interval");
    }

    [Fact]
    public void SetRate_IgnoresGarbage()
    {
        var clock = new SampleClock(2.0);
        clock.SetRate(0);
        clock.SetRate(double.NaN);
        clock.SetRate(-1);

        Assert.False(clock.Tick(0.4), "the interval is unchanged at 0.5 s");
        Assert.True(clock.Tick(0.2));
    }

    [Fact]
    public void Reset_DropsTheAccumulator()
    {
        var clock = new SampleClock(2.0);
        clock.Tick(0.4);
        clock.Reset();

        Assert.False(clock.Tick(0.2), "reset dropped the accumulated 0.4 s");
    }

    [Theory]
    [InlineData(0.0)]
    [InlineData(-1.0)]
    [InlineData(double.NaN)]
    [InlineData(double.PositiveInfinity)]
    public void InvalidRate_Throws(double rateHz)
    {
        Assert.Throws<ArgumentOutOfRangeException>(() => new SampleClock(rateHz));
    }
}
