using System;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Ship;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>
/// Real wall time plus an offset the test controls, so a case that needs several batches can step
/// over the hard <see cref="Wire.MinShipIntervalSeconds"/> reporting floor without spending
/// thirty real seconds per request.
/// </summary>
/// <remarks>
/// <para>
/// The floor is measured against <see cref="IShipperClock.UtcNow"/>, and this is the injection
/// point that exists for exactly this reason. It is a test seam: <c>mod/catlog</c> — the assembly
/// a player installs — constructs its shipper without a clock and therefore always gets
/// <see cref="SystemShipperClock"/> and the real thirty seconds.
/// </para>
/// <para>
/// <b>Anchored half a skew window in the past.</b> The proof's <c>iat</c> comes off this clock and
/// the server rejects anything more than <see cref="Wire.ClockSkewSeconds"/> either side of its
/// own time, so a test that only ever winds forward would eventually sign itself out of the
/// window. Starting at <c>now − 150 s</c> spends the first half of the allowance catching up and
/// leaves the second half for the advances, which is enough for the ten-odd requests any case
/// here makes.
/// </para>
/// </remarks>
public sealed class AdvanceableClock : IShipperClock
{
    private TimeSpan _offset;

    /// <summary>Creates a clock offset from real time.</summary>
    /// <param name="startOffset">
    /// Where to start relative to now; defaults to half of <see cref="Wire.ClockSkewSeconds"/> in
    /// the past. Pass a large positive value to provoke <c>401 clock_skew</c> on purpose.
    /// </param>
    public AdvanceableClock(TimeSpan? startOffset = null)
        => _offset = startOffset ?? TimeSpan.FromSeconds(-Wire.ClockSkewSeconds / 2.0);

    /// <inheritdoc />
    public DateTimeOffset UtcNow => DateTimeOffset.UtcNow + _offset;

    /// <summary>Winds the clock forward. Never rewinds.</summary>
    /// <param name="by">How far.</param>
    public void Advance(TimeSpan by)
    {
        if (by > TimeSpan.Zero)
            _offset += by;
    }

    /// <summary>Winds the clock just far enough that <paramref name="shipper"/> may send again.</summary>
    /// <param name="shipper">The shipper whose window to open.</param>
    public void OpenShipWindow(BatchShipper shipper)
        => Advance(shipper.ThrottleRemaining + TimeSpan.FromMilliseconds(1));

    /// <inheritdoc />
    public Task Delay(TimeSpan delay, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        Advance(delay);
        return Task.CompletedTask;
    }
}
