using System;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>
/// The sampler's frame-dt rate limiter: accumulate dt and fire at most once per call when an
/// interval has elapsed. Missed intervals after a long frame are <b>dropped, not back-filled</b>.
/// </summary>
/// <remarks>
/// Copied from <c>gatOS/gatOS.SimFs/Telemetry/SampleClock.cs</c>. The alternative — unscience's
/// <c>while (_accumulator >= interval)</c> loop in <c>MonitoringLoop.cs:34-38</c> — is a hitch
/// amplifier: after a five-second stall (scene load, warp transition, alt-tab) it fires ten full
/// multi-vehicle sample passes back to back inside one frame, all stamped with the <i>same</i>
/// sim time, which then feed the prev/curr detector as zero-delta pairs. This one fires at most
/// once per <see cref="Tick"/> and zeroes the accumulator when the backlog exceeds one interval.
/// </remarks>
public sealed class SampleClock
{
    private double _intervalSeconds;
    private double _accumulator;

    /// <summary>Creates a clock at the given cadence.</summary>
    /// <param name="rateHz">Samples per second; the caller clamps (catlog.toml allows 0.1–20).</param>
    /// <exception cref="ArgumentOutOfRangeException">The rate is not positive and finite.</exception>
    public SampleClock(double rateHz)
    {
        if (rateHz <= 0 || !double.IsFinite(rateHz))
            throw new ArgumentOutOfRangeException(nameof(rateHz), rateHz, "Sample rate must be positive.");
        _intervalSeconds = 1.0 / rateHz;
    }

    /// <summary>
    /// Changes the cadence live (the player retuned <c>sample_hz</c> in-game). Accumulated phase is
    /// kept, so a rate change never fires an immediate extra sample nor stalls a due one; a
    /// non-positive or non-finite rate is ignored.
    /// </summary>
    /// <param name="rateHz">The new rate, in samples per second.</param>
    public void SetRate(double rateHz)
    {
        if (rateHz > 0 && double.IsFinite(rateHz))
            _intervalSeconds = 1.0 / rateHz;
    }

    /// <summary>Advances by one frame.</summary>
    /// <param name="dt">Frame delta in seconds; garbage (NaN, negative) is ignored.</param>
    /// <returns>True when a sample is due now.</returns>
    public bool Tick(double dt)
    {
        if (dt > 0 && double.IsFinite(dt))
            _accumulator += dt;
        if (_accumulator < _intervalSeconds)
            return false;

        // Keep sub-interval phase for drift-free cadence, but a backlog of more than one
        // interval (a long frame hitch) is dropped outright, never burst-replayed.
        _accumulator -= _intervalSeconds;
        if (_accumulator >= _intervalSeconds)
            _accumulator = 0;
        return true;
    }

    /// <summary>
    /// Drops accumulated time — used when sampling was gated off, so reactivation does not fire
    /// instantly from a stale accumulator.
    /// </summary>
    public void Reset() => _accumulator = 0;
}
