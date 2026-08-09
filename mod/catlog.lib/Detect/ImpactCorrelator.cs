using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>An impact with its survival verdict resolved.</summary>
/// <param name="Signal">The original impact signal.</param>
/// <param name="Survived">False when the same vehicle was destroyed in the same or the following frame.</param>
public sealed record ResolvedImpact(ImpactSignal Signal, bool Survived);

/// <summary>
/// A touchdown the detector saw, before its survival verdict is known.
/// </summary>
/// <remarks>
/// Not a <see cref="GameSignal"/>: it originates on the worker, from a prev/curr comparison of two
/// <see cref="Telemetry.TelemetrySnapshot"/>s, not from a Harmony patch body on the game thread.
/// It carries everything <c>vehicle.landed</c> needs except <c>survived</c>, which is what the
/// correlator is for.
/// </remarks>
/// <param name="VehicleId">The vehicle that touched down.</param>
/// <param name="SimT">Universe sim seconds of the sample that first showed contact.</param>
/// <param name="WallMs">Client unix milliseconds of that sample.</param>
/// <param name="Body">Lowercase parent body name.</param>
/// <param name="VerticalSpeedMs">Descent rate at touchdown, positive downwards, in m/s.</param>
/// <param name="HorizontalSpeedMs">Ground-track speed at touchdown, in m/s.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="RadarAltM">Terrain-relative altitude at that sample, or null when unreadable.</param>
/// <param name="Lat">Latitude in degrees, or null when unreadable.</param>
/// <param name="Lon">Longitude in degrees, or null when unreadable.</param>
public sealed record LandingObservation(
    string VehicleId,
    double SimT,
    long WallMs,
    string Body,
    double VerticalSpeedMs,
    double HorizontalSpeedMs,
    int CrewCount,
    double? RadarAltM,
    double? Lat,
    double? Lon);

/// <summary>A touchdown with its survival verdict resolved.</summary>
/// <param name="Landing">The observation.</param>
/// <param name="Survived">False when the vehicle was destroyed in the same or the following frame.</param>
public sealed record ResolvedLanding(LandingObservation Landing, bool Survived);

/// <summary>
/// Everything one call to <see cref="ImpactCorrelator.EndFrame"/> (or a drain) settled.
/// </summary>
/// <remarks>
/// The two lists come back together because they are settled by <b>one</b> advance of <b>one</b>
/// hold: splitting them into two methods would mean two calls, and a caller that made only one of
/// them would silently strand the other kind forever.
/// </remarks>
/// <param name="Impacts">Impacts whose verdict is now final, oldest first.</param>
/// <param name="Landings">Touchdowns whose verdict is now final, oldest first.</param>
public readonly record struct Verdicts(
    IReadOnlyList<ResolvedImpact> Impacts,
    IReadOnlyList<ResolvedLanding> Landings)
{
    /// <summary>Nothing settled. The common case, and it allocates nothing.</summary>
    public static Verdicts None { get; } = new([], []);

    /// <summary>How many verdicts of either kind this carries.</summary>
    public int Count => Impacts.Count + Landings.Count;
}

/// <summary>
/// Pairs impacts and touchdowns with destructions to compute <c>vehicle.impact.survived</c> and
/// <c>vehicle.landed.survived</c> (§4.2).
/// </summary>
/// <remarks>
/// <para>
/// The game applies <b>every</b> impact and splash for a frame before <b>any</b> physics
/// destruction in that frame (a two-pass structure, verified in <c>docs/ksa-integration.md</c> §3),
/// so an impact that killed the vehicle is always followed by the destruction in the same frame.
/// But a <i>manual</i> destroy lands later still, in the game's input-apply pass, a few lines after
/// the solver-apply pass. So a verdict taken at the end of the impact's own frame would call a
/// scuttled vehicle a survivor.
/// </para>
/// <para>
/// Observations are therefore held for <b>one full frame</b>: one seen in frame N is resolved at
/// the end of frame N+1, and any destruction of that vehicle in frame N or N+1 flips
/// <c>survived</c> to false. That is the "holds impacts one tick" rule of §7.2, with the extra
/// frame that the manual-destroy path needs.
/// </para>
/// <para>
/// <b>Landings use the same hold, not a second one.</b> A landing is a destruction-sensitive
/// verdict for exactly the same reason an impact is — without the hold, a player could scuttle a
/// craft that fell over on touchdown and still bank the landing — so it goes through this class
/// rather than being inferred anywhere else. The one asymmetry is where it enters: a landing is
/// detected on the worker while processing frame N's telemetry, which happens just <i>after</i>
/// frame N's boundary has already been consumed, so it lands in the pending slot one step later
/// than an impact raised inside frame N and settles a frame further on. That is strictly the safer
/// direction and needs no special-casing.
/// </para>
/// <para>
/// Frame boundaries reach this class as <see cref="FrameBoundarySignal"/>s in the lossless signal
/// channel — see <see cref="Telemetry.GameBridge"/>.
/// </para>
/// </remarks>
public sealed class ImpactCorrelator
{
    private readonly Hold<ImpactSignal, ResolvedImpact> _impacts =
        new(static signal => signal.VehicleId, static (signal, destroyed) => new ResolvedImpact(signal, !destroyed));

    private readonly Hold<LandingObservation, ResolvedLanding> _landings =
        new(static landing => landing.VehicleId, static (landing, destroyed) => new ResolvedLanding(landing, !destroyed));

    /// <summary>How many observations of either kind are waiting for a verdict.</summary>
    public int Outstanding => _impacts.Outstanding + _landings.Outstanding;

    /// <summary>Records an impact seen in the current frame.</summary>
    /// <param name="signal">The impact.</param>
    public void Impact(ImpactSignal signal) => _impacts.Add(signal);

    /// <summary>
    /// Records a splash as an impact. The game's splash event carries no launch-pad concept, so
    /// <c>launch_pad</c> is false.
    /// </summary>
    /// <param name="signal">The splash.</param>
    public void Splash(SplashSignal signal) => Impact(new ImpactSignal(
        signal.SimT,
        signal.WallMs,
        signal.VehicleId,
        signal.SpeedMs,
        signal.EnergyJ,
        LaunchPad: false,
        signal.Body,
        signal.CrewCount,
        signal.Lat,
        signal.Lon));

    /// <summary>Records a touchdown the detector saw this frame.</summary>
    /// <param name="landing">The observation.</param>
    public void Landed(LandingObservation landing) => _landings.Add(landing);

    /// <summary>
    /// Records that a vehicle was destroyed in the current frame. Marks every outstanding
    /// observation for that vehicle as not survived.
    /// </summary>
    /// <param name="vehicleId">The destroyed vehicle.</param>
    public void Destroyed(string vehicleId)
    {
        _impacts.Destroyed(vehicleId);
        _landings.Destroyed(vehicleId);
    }

    /// <summary>
    /// Closes the current frame: returns the verdicts for observations seen in the <i>previous</i>
    /// frame and promotes this frame's to the holding slot.
    /// </summary>
    /// <returns>The resolved impacts and landings, each in the order they were recorded.</returns>
    public Verdicts EndFrame() => new(_impacts.EndFrame(), _landings.EndFrame());

    /// <summary>
    /// Resolves one vehicle's outstanding observations immediately, used when its flight ends.
    /// </summary>
    /// <remarks>
    /// The one-frame hold exists so a destruction that lands after the observation's own frame — a
    /// manual destroy, which the game applies in its input pass — can still flip the verdict. Once
    /// the flight has ended there is no later destruction to wait for, so the verdict is already
    /// final; and waiting past this point would resolve it after the flight id has been retired,
    /// attaching it to a flight nothing else references.
    /// </remarks>
    /// <param name="vehicleId">The vehicle whose flight is ending.</param>
    /// <returns>That vehicle's outstanding observations, resolved, oldest first.</returns>
    public Verdicts DrainFor(string vehicleId) => new(_impacts.DrainFor(vehicleId), _landings.DrainFor(vehicleId));

    /// <summary>
    /// Resolves everything outstanding immediately — used at flight or session end, where there
    /// will be no further frame to wait for.
    /// </summary>
    /// <returns>Every outstanding observation, resolved.</returns>
    public Verdicts Drain() => new(_impacts.Drain(), _landings.Drain());

    /// <summary>
    /// The two-slot hold, once. <typeparamref name="T"/> is what was observed and
    /// <typeparamref name="TResolved"/> what it becomes once the verdict is known; the vehicle-id
    /// accessor and the resolver are the only things the two instantiations differ by.
    /// </summary>
    /// <typeparam name="T">The observation type.</typeparam>
    /// <typeparam name="TResolved">The resolved type.</typeparam>
    private sealed class Hold<T, TResolved>(Func<T, string> vehicleOf, Func<T, bool, TResolved> resolve)
    {
        private List<Entry> _pending = [];
        private List<Entry> _held = [];

        internal int Outstanding => _pending.Count + _held.Count;

        internal void Add(T item) => _pending.Add(new Entry(item));

        internal void Destroyed(string vehicleId)
        {
            Mark(_pending, vehicleId);
            Mark(_held, vehicleId);
        }

        internal IReadOnlyList<TResolved> EndFrame()
        {
            IReadOnlyList<TResolved> due = Resolve(_held);
            _held = _pending;
            _pending = [];
            return due;
        }

        internal IReadOnlyList<TResolved> DrainFor(string vehicleId)
        {
            List<Entry>? due = Take(_held, vehicleId);
            List<Entry>? fresh = Take(_pending, vehicleId);
            if (due is null && fresh is null)
                return [];

            if (due is null)
                return Resolve(fresh!);
            if (fresh is not null)
                due.AddRange(fresh);
            return Resolve(due);
        }

        internal IReadOnlyList<TResolved> Drain()
        {
            if (_held.Count == 0 && _pending.Count == 0)
                return [];

            var all = new List<Entry>(_held.Count + _pending.Count);
            all.AddRange(_held);
            all.AddRange(_pending);
            _held = [];
            _pending = [];
            return Resolve(all);
        }

        // Removes and returns one vehicle's entries, preserving the order of both the taken and the
        // remaining ones.
        private List<Entry>? Take(List<Entry> entries, string vehicleId)
        {
            List<Entry>? taken = null;
            int keep = 0;
            for (int i = 0; i < entries.Count; i++)
            {
                if (string.Equals(vehicleOf(entries[i].Item), vehicleId, StringComparison.Ordinal))
                    (taken ??= []).Add(entries[i]);
                else
                    entries[keep++] = entries[i];
            }

            if (taken is not null)
                entries.RemoveRange(keep, entries.Count - keep);
            return taken;
        }

        private void Mark(List<Entry> entries, string vehicleId)
        {
            for (int i = 0; i < entries.Count; i++)
            {
                if (string.Equals(vehicleOf(entries[i].Item), vehicleId, StringComparison.Ordinal))
                    entries[i] = entries[i] with { Destroyed = true };
            }
        }

        private IReadOnlyList<TResolved> Resolve(List<Entry> entries)
        {
            if (entries.Count == 0)
                return [];

            var resolved = new TResolved[entries.Count];
            for (int i = 0; i < entries.Count; i++)
                resolved[i] = resolve(entries[i].Item, entries[i].Destroyed);
            return resolved;
        }

        private readonly record struct Entry(T Item, bool Destroyed = false);
    }
}
