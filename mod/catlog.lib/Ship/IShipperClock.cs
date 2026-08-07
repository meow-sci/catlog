using System;
using System.Threading;
using System.Threading.Tasks;

namespace MeowSci.Catlog.Lib.Ship;

/// <summary>
/// Time and waiting, injected so the shipper's retry ladder can be unit-tested without a real
/// wall clock. Every <c>await</c> in <see cref="BatchShipper"/> goes through this — there is no
/// <c>Task.Delay</c> or <c>Thread.Sleep</c> anywhere in the shipper.
/// </summary>
public interface IShipperClock
{
    /// <summary>The current UTC instant.</summary>
    DateTimeOffset UtcNow { get; }

    /// <summary>Waits.</summary>
    /// <param name="delay">How long to wait.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A task that completes when the delay has elapsed.</returns>
    Task Delay(TimeSpan delay, CancellationToken ct);
}

/// <summary>The real clock.</summary>
public sealed class SystemShipperClock : IShipperClock
{
    /// <summary>A shared instance; the type is stateless.</summary>
    public static SystemShipperClock Instance { get; } = new();

    /// <inheritdoc />
    public DateTimeOffset UtcNow => DateTimeOffset.UtcNow;

    /// <inheritdoc />
    public Task Delay(TimeSpan delay, CancellationToken ct)
        => delay <= TimeSpan.Zero ? Task.CompletedTask : Task.Delay(delay, ct);
}

/// <summary>
/// The retry ladder from §4.5.3: exponential backoff <c>1 s · 2ⁿ</c> with <b>full</b> jitter,
/// capped at five minutes. Pure, so the schedule is testable without waiting for anything.
/// </summary>
/// <remarks>
/// Full jitter (a uniform draw over <c>[0, ceiling]</c>) rather than the ceiling itself: every
/// client that lost connectivity at the same moment would otherwise retry at the same moment, and
/// a server coming back up would be hit by the whole fleet in lockstep.
/// </remarks>
public static class BackoffPolicy
{
    /// <summary>The un-jittered ceiling for an attempt.</summary>
    /// <param name="attempt">Zero-based consecutive-failure count.</param>
    /// <returns>The ceiling, capped at <see cref="Wire.BackoffCapSeconds"/>.</returns>
    public static TimeSpan Ceiling(int attempt)
    {
        if (attempt < 0)
            attempt = 0;
        // Math.Pow overflows to +inf well before this matters; Min pins it to the cap either way.
        double seconds = Math.Min(Wire.BackoffCapSeconds, Wire.BackoffBaseSeconds * Math.Pow(2, attempt));
        return TimeSpan.FromSeconds(seconds);
    }

    /// <summary>The jittered delay for an attempt.</summary>
    /// <param name="attempt">Zero-based consecutive-failure count.</param>
    /// <param name="jitter">A uniform draw in <c>[0, 1]</c>.</param>
    /// <returns>The delay to wait before the next attempt.</returns>
    public static TimeSpan Delay(int attempt, double jitter)
        => Ceiling(attempt) * Math.Clamp(double.IsFinite(jitter) ? jitter : 1.0, 0.0, 1.0);
}
