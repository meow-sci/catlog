using System;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Ship;

namespace MeowSci.Catlog.Sim;

/// <summary>
/// The simulator's clock: real wall time plus a virtual offset that the runner advances instead of
/// sleeping.
/// </summary>
/// <remarks>
/// <para>
/// <b>Why this type has to exist.</b> The shipper's hard
/// <see cref="Wire.MinShipIntervalSeconds"/> floor is measured against
/// <see cref="IShipperClock.UtcNow"/>. A scenario compresses half an hour of play into a fifth of
/// a second and ships several batches doing it, so on the real clock every batch after the first
/// would be refused and the run would take minutes of dead waiting. Injecting a clock the runner
/// can wind forward is precisely what the <see cref="IShipperClock"/> seam is for, and it is the
/// same seam the unit tests use.
/// </para>
/// <para>
/// <b>This is a test harness, and it is not reachable from the game.</b> <c>catlog.sim</c> is a
/// separate console executable that is never shipped to players; <c>mod/catlog</c> constructs its
/// shipper without a clock argument and therefore gets <see cref="SystemShipperClock"/> and a real
/// 30 s floor. Nothing in <c>catlog.toml</c> selects between them.
/// </para>
/// <para>
/// <b>Anchored on real time rather than an arbitrary epoch</b>, because the sim talks to a live
/// <c>catlogd</c> and the proof's <c>iat</c> has to land inside §4.3's ±300 s skew window. The
/// virtual offset therefore only ever runs the clock <i>forward</i> of real time, by the floor once
/// per batch. A long enough run would eventually leave the window; that is not a silent failure —
/// the server answers <c>401 clock_skew</c>, the shipper relearns the offset from the <c>Date</c>
/// header and the next attempt succeeds, which is the same recovery a player with a wrong clock
/// gets.
/// </para>
/// </remarks>
public sealed class SimShipperClock : IShipperClock
{
    private TimeSpan _virtualOffset;

    /// <inheritdoc />
    public DateTimeOffset UtcNow => DateTimeOffset.UtcNow + _virtualOffset;

    /// <summary>How far ahead of real time the scenario has wound this clock.</summary>
    public TimeSpan VirtualOffset => _virtualOffset;

    /// <summary>Winds the clock forward without waiting.</summary>
    /// <param name="by">How far. Non-positive spans are ignored — this clock never rewinds.</param>
    public void Advance(TimeSpan by)
    {
        if (by > TimeSpan.Zero)
            _virtualOffset += by;
    }

    /// <inheritdoc />
    /// <remarks>
    /// Returns immediately and books the time instead. The shipper's own
    /// <see cref="BatchShipper.RunAsync"/> loop is not what the runner drives (see
    /// <see cref="ScenarioRunner"/>), so in practice this is the floor's wait and nothing else.
    /// </remarks>
    public Task Delay(TimeSpan delay, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        Advance(delay);
        return Task.CompletedTask;
    }
}
