using System;
using System.Diagnostics;
using System.Threading;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// A single-writer/many-reader timing counter. Copied from
/// <c>gatOS/gatOS.Logging/PerfStat.cs</c>: recording a sample is two
/// <see cref="Stopwatch.GetTimestamp"/> reads plus four integer stores — no allocation, no lock,
/// no boxing. Ticks are accumulated raw; the ticks→µs conversion happens only at read time, off
/// the hot path. This is the only instrumentation available on a KSA machine (§7.4).
/// </summary>
public sealed class PerfStat
{
    private static readonly double TicksToMicros = 1_000_000.0 / Stopwatch.Frequency;

    private long _count;
    private long _sumTicks;
    private long _maxTicks;
    private long _lastTicks;

    /// <summary>Starts a scope that records its lifetime into this stat on dispose.</summary>
    /// <returns>An allocation-free <c>using</c>-scoped timer.</returns>
    public Scope Measure() => new(this, Stopwatch.GetTimestamp());

    /// <summary>Records one elapsed measurement, in <see cref="Stopwatch"/> ticks.</summary>
    /// <param name="elapsedTicks">Elapsed ticks; negatives (clock hiccups) are clamped to zero.</param>
    public void Add(long elapsedTicks)
    {
        if (elapsedTicks < 0)
            elapsedTicks = 0;

        Volatile.Write(ref _sumTicks, _sumTicks + elapsedTicks);
        if (elapsedTicks > _maxTicks)
            Volatile.Write(ref _maxTicks, elapsedTicks);
        Volatile.Write(ref _lastTicks, elapsedTicks);
        // Count is written last (release): a reader that sees the new count also sees the new sum.
        Volatile.Write(ref _count, _count + 1);
    }

    /// <summary>How many measurements have been recorded.</summary>
    public long Count => Volatile.Read(ref _count);

    /// <summary>Mean measurement, in microseconds.</summary>
    public double AvgMicros
    {
        get
        {
            long c = Volatile.Read(ref _count);
            return c == 0 ? 0 : Volatile.Read(ref _sumTicks) * TicksToMicros / c;
        }
    }

    /// <summary>The most recent measurement, in microseconds.</summary>
    public double LastMicros => Volatile.Read(ref _lastTicks) * TicksToMicros;

    /// <summary>The largest measurement seen, in microseconds.</summary>
    public double MaxMicros => Volatile.Read(ref _maxTicks) * TicksToMicros;

    /// <summary>Zeroes every accumulator.</summary>
    public void Reset()
    {
        Volatile.Write(ref _count, 0);
        Volatile.Write(ref _sumTicks, 0);
        Volatile.Write(ref _maxTicks, 0);
        Volatile.Write(ref _lastTicks, 0);
    }

    /// <summary>A <c>using</c>-scoped timer that records its lifetime into the owning <see cref="PerfStat"/>.</summary>
    /// <param name="stat">The stat to record into.</param>
    /// <param name="startTimestamp">The <see cref="Stopwatch.GetTimestamp"/> value at scope entry.</param>
    public readonly struct Scope(PerfStat stat, long startTimestamp) : IDisposable
    {
        /// <summary>Records the scope's lifetime.</summary>
        public void Dispose() => stat.Add(Stopwatch.GetTimestamp() - startTimestamp);
    }
}
