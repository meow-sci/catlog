using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

/// <summary>
/// §7.4: the in-game timing counters behind the status window. Copied from
/// gatOS; this pins the accumulator arithmetic, not the wall-clock accuracy.
/// </summary>
public sealed class PerfStatTests
{
    [Fact]
    public void StartsEmpty()
    {
        var stat = new PerfStat();

        Assert.Equal(0, stat.Count);
        Assert.Equal(0, stat.AvgMicros);
        Assert.Equal(0, stat.MaxMicros);
        Assert.Equal(0, stat.LastMicros);
    }

    [Fact]
    public void TracksCountAverageMaxAndLast()
    {
        var stat = new PerfStat();

        stat.Add(100);
        stat.Add(300);
        stat.Add(200);

        Assert.Equal(3, stat.Count);
        Assert.True(stat.MaxMicros > stat.AvgMicros, "300 ticks is the largest sample");
        Assert.True(stat.AvgMicros > 0);
        Assert.True(stat.LastMicros > 0);
    }

    /// <summary>A clock hiccup must never poison the running sum.</summary>
    [Fact]
    public void NegativeElapsedIsClampedToZero()
    {
        var stat = new PerfStat();

        stat.Add(-1_000);

        Assert.Equal(1, stat.Count);
        Assert.Equal(0, stat.LastMicros);
        Assert.Equal(0, stat.AvgMicros);
    }

    [Fact]
    public void MeasureScopeRecordsOneSample()
    {
        var stat = new PerfStat();

        using (stat.Measure())
        {
        }

        Assert.Equal(1, stat.Count);
    }

    [Fact]
    public void ResetZeroesEverything()
    {
        var stat = new PerfStat();
        stat.Add(500);

        stat.Reset();

        Assert.Equal(0, stat.Count);
        Assert.Equal(0, stat.MaxMicros);
    }
}
