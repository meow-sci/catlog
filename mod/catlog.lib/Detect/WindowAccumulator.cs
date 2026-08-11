using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>A closed window and the vehicle it belongs to.</summary>
/// <param name="VehicleId">The vehicle the window covers.</param>
/// <param name="Payload">The <c>telemetry.window</c> payload.</param>
public sealed record ClosedWindow(string VehicleId, TelemetryWindowPayload Payload);

/// <summary>
/// The 30-second sim-time min/max/mean/last folds behind <c>telemetry.window</c> (D15). One
/// window per vehicle per window period; no raw sample firehose ever leaves the machine.
/// </summary>
/// <remarks>
/// <para>
/// Windows are half-open in sim time: a window that opened at <c>t0</c> covers samples with
/// <c>t0 ≤ t &lt; t0 + window</c>. A sample at exactly <c>t0 + window</c> <b>closes</b> the window
/// and becomes the first sample of the next one, so a 2 Hz stream over 30 s produces windows of 60
/// samples and the boundary never lands in two windows at once.
/// </para>
/// <para>
/// <c>peak_g</c>, <c>max_q_pa</c> and <c>radar_alt_m</c> fold only over samples that actually
/// carried a reading and are <b>omitted</b> from the payload when no sample did — see
/// <see cref="TelemetrySnapshot.PeakG"/> for why reporting 0 would corrupt the peak-g board, and
/// <see cref="TelemetrySnapshot.RadarAltM"/> for why a zeroed terrain altitude is worse still.
/// </para>
/// </remarks>
public sealed class WindowAccumulator
{
    private readonly Dictionary<string, VehicleWindow> _windows = new(StringComparer.Ordinal);
    private readonly double _windowSeconds;

    /// <summary>Creates an accumulator.</summary>
    /// <param name="windowSeconds">Window length in sim seconds; defaults to the D15 value of 30.</param>
    /// <exception cref="ArgumentOutOfRangeException">The window length is not positive and finite.</exception>
    public WindowAccumulator(double windowSeconds = Wire.TelemetryWindowSeconds)
    {
        if (windowSeconds <= 0 || !double.IsFinite(windowSeconds))
            throw new ArgumentOutOfRangeException(
                nameof(windowSeconds), windowSeconds, "Window length must be positive.");
        _windowSeconds = windowSeconds;
    }

    /// <summary>How many vehicles currently have an open window.</summary>
    public int OpenWindows => _windows.Count;

    /// <summary>Folds one sample in.</summary>
    /// <param name="snapshot">The sample.</param>
    /// <returns>The window this sample closed, or null when the current window is still open.</returns>
    public ClosedWindow? Add(TelemetrySnapshot snapshot)
    {
        if (!_windows.TryGetValue(snapshot.VehicleId, out VehicleWindow? window))
        {
            _windows[snapshot.VehicleId] = new VehicleWindow(snapshot);
            return null;
        }

        // Sim time ran backwards: a save was loaded. The partial window spans two different
        // timelines and its mean would be meaningless, so drop it and start over.
        if (snapshot.SimT < window.T0)
        {
            _windows[snapshot.VehicleId] = new VehicleWindow(snapshot);
            return null;
        }

        if (snapshot.SimT - window.T0 >= _windowSeconds)
        {
            TelemetryWindowPayload payload = window.Close();
            _windows[snapshot.VehicleId] = new VehicleWindow(snapshot);
            return new ClosedWindow(snapshot.VehicleId, payload);
        }

        window.Add(snapshot);
        return null;
    }

    /// <summary>
    /// Closes and returns a vehicle's partial window early — used when a flight ends, so the last
    /// few seconds before a RUD or recovery are not thrown away.
    /// </summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>The closed window, or null when there was nothing open.</returns>
    public ClosedWindow? Flush(string vehicleId)
    {
        if (!_windows.Remove(vehicleId, out VehicleWindow? window))
            return null;
        return new ClosedWindow(vehicleId, window.Close());
    }

    /// <summary>Closes every open window (session end).</summary>
    /// <returns>One closed window per vehicle, in no particular order.</returns>
    public IReadOnlyList<ClosedWindow> FlushAll()
    {
        if (_windows.Count == 0)
            return [];

        var closed = new List<ClosedWindow>(_windows.Count);
        foreach (KeyValuePair<string, VehicleWindow> entry in _windows)
            closed.Add(new ClosedWindow(entry.Key, entry.Value.Close()));
        _windows.Clear();
        return closed;
    }

    /// <summary>Discards a vehicle's partial window without emitting it.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    public void Forget(string vehicleId) => _windows.Remove(vehicleId);

    private sealed class VehicleWindow
    {
        private readonly Fold _alt = new();
        private readonly Fold _surfaceSpeed = new();
        private readonly Fold _orbitalSpeed = new();
        private readonly Fold _accel = new();

        // Nullable, not a Fold seeded at zero: a window can legitimately contain no terrain
        // reading at all (a whole window spent in orbit), and it is allocated on the first sample
        // that has one so the common case costs nothing.
        private Fold? _radarAlt;

        private string _body;
        private double _massKgLast;
        private double _t1;
        private double? _peakG;
        private double? _maxQPa;
        private double _warpMax;
        private StateVec? _state;
        private int _n;

        internal VehicleWindow(TelemetrySnapshot first)
        {
            T0 = first.SimT;
            _body = first.Body;
            Add(first);
        }

        internal double T0 { get; }

        internal void Add(TelemetrySnapshot s)
        {
            _alt.Add(s.AltitudeM);
            _surfaceSpeed.Add(s.SurfaceSpeedMs);
            _orbitalSpeed.Add(s.OrbitalSpeedMs);
            _accel.Add(s.AccelMs2);

            if (s.PeakG is { } g && (_peakG is null || g > _peakG.Value))
                _peakG = g;
            if (s.MaxQPa is { } q && (_maxQPa is null || q > _maxQPa.Value))
                _maxQPa = q;
            if (s.RadarAltM is { } radar)
                (_radarAlt ??= new Fold()).Add(radar);
            if (s.WarpFactor > _warpMax)
                _warpMax = s.WarpFactor;

            _body = s.Body;
            _massKgLast = s.MassKg;
            // Atomic last-sample provenance: assigning null deliberately clears an older state,
            // especially across an SOI/body change where retaining it would mislabel the frame.
            _state = s.State;
            _t1 = s.SimT;
            _n++;
        }

        internal TelemetryWindowPayload Close() => new(
            T0Sim: T0,
            T1Sim: _t1,
            N: _n,
            Body: _body,
            AltM: _alt.ToAgg(),
            SurfaceSpeedMs: _surfaceSpeed.ToAgg(),
            OrbitalSpeedMs: _orbitalSpeed.ToAgg(),
            AccelMs2: _accel.ToAgg(),
            PeakG: _peakG,
            MaxQPa: _maxQPa,
            MassKgLast: _massKgLast,
            RadarAltM: _radarAlt?.ToAgg(),
            WarpMax: _warpMax,
            State: _state);
    }

    private sealed class Fold
    {
        private double _min = double.PositiveInfinity;
        private double _max = double.NegativeInfinity;
        private double _sum;
        private double _last;
        private int _n;

        internal void Add(double value)
        {
            if (value < _min)
                _min = value;
            if (value > _max)
                _max = value;
            _sum += value;
            _last = value;
            _n++;
        }

        internal Agg ToAgg() => _n == 0
            ? new Agg(0, 0, 0, 0)
            : new Agg(_min, _max, _sum / _n, _last);
    }
}
