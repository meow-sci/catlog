using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>An impact with its survival verdict resolved.</summary>
/// <param name="Signal">The original impact signal.</param>
/// <param name="Survived">False when the same vehicle was destroyed in the same or the following frame.</param>
public sealed record ResolvedImpact(ImpactSignal Signal, bool Survived);

/// <summary>
/// Pairs impacts with destructions to compute <c>vehicle.impact.survived</c> (§4.2).
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
/// Impacts are therefore held for <b>one full frame</b>: an impact seen in frame N is resolved at
/// the end of frame N+1, and any destruction of that vehicle in frame N or N+1 flips
/// <c>survived</c> to false. That is the "holds impacts one tick" rule of §7.2, with the extra
/// frame that the manual-destroy path needs.
/// </para>
/// <para>
/// Frame boundaries reach this class as <see cref="FrameBoundarySignal"/>s in the lossless signal
/// channel — see <see cref="Telemetry.GameBridge"/>.
/// </para>
/// </remarks>
public sealed class ImpactCorrelator
{
    private List<Entry> _pending = [];
    private List<Entry> _held = [];

    /// <summary>How many impacts are waiting for a verdict.</summary>
    public int Outstanding => _pending.Count + _held.Count;

    /// <summary>Records an impact seen in the current frame.</summary>
    /// <param name="signal">The impact.</param>
    public void Impact(ImpactSignal signal) => _pending.Add(new Entry(signal));

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
        signal.CrewCount));

    /// <summary>
    /// Records that a vehicle was destroyed in the current frame. Marks every outstanding impact
    /// for that vehicle as not survived.
    /// </summary>
    /// <param name="vehicleId">The destroyed vehicle.</param>
    public void Destroyed(string vehicleId)
    {
        Mark(_pending, vehicleId);
        Mark(_held, vehicleId);
    }

    /// <summary>
    /// Closes the current frame: returns the verdicts for impacts seen in the <i>previous</i> frame
    /// and promotes this frame's impacts to the holding slot.
    /// </summary>
    /// <returns>The resolved impacts, in the order they were recorded.</returns>
    public IReadOnlyList<ResolvedImpact> EndFrame()
    {
        IReadOnlyList<ResolvedImpact> due = Resolve(_held);
        _held = _pending;
        _pending = [];
        return due;
    }

    /// <summary>
    /// Resolves one vehicle's outstanding impacts immediately, used when its flight ends.
    /// </summary>
    /// <remarks>
    /// The one-frame hold exists so a destruction that lands after the impact's own frame — a
    /// manual destroy, which the game applies in its input pass — can still flip the verdict. Once
    /// the flight has ended there is no later destruction to wait for, so the verdict is already
    /// final; and waiting past this point would resolve the impact after the flight id has been
    /// retired, attaching it to a flight nothing else references.
    /// </remarks>
    /// <param name="vehicleId">The vehicle whose flight is ending.</param>
    /// <returns>That vehicle's outstanding impacts, resolved, oldest first.</returns>
    public IReadOnlyList<ResolvedImpact> DrainFor(string vehicleId)
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

    /// <summary>
    /// Resolves everything outstanding immediately — used at flight or session end, where there
    /// will be no further frame to wait for.
    /// </summary>
    /// <returns>Every outstanding impact, resolved.</returns>
    public IReadOnlyList<ResolvedImpact> Drain()
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
    private static List<Entry>? Take(List<Entry> entries, string vehicleId)
    {
        List<Entry>? taken = null;
        int keep = 0;
        for (int i = 0; i < entries.Count; i++)
        {
            if (string.Equals(entries[i].Signal.VehicleId, vehicleId, StringComparison.Ordinal))
                (taken ??= []).Add(entries[i]);
            else
                entries[keep++] = entries[i];
        }

        if (taken is not null)
            entries.RemoveRange(keep, entries.Count - keep);
        return taken;
    }

    private static void Mark(List<Entry> entries, string vehicleId)
    {
        for (int i = 0; i < entries.Count; i++)
        {
            if (string.Equals(entries[i].Signal.VehicleId, vehicleId, StringComparison.Ordinal))
                entries[i] = entries[i] with { Destroyed = true };
        }
    }

    private static IReadOnlyList<ResolvedImpact> Resolve(List<Entry> entries)
    {
        if (entries.Count == 0)
            return [];

        var resolved = new ResolvedImpact[entries.Count];
        for (int i = 0; i < entries.Count; i++)
            resolved[i] = new ResolvedImpact(entries[i].Signal, !entries[i].Destroyed);
        return resolved;
    }

    private readonly record struct Entry(ImpactSignal Signal, bool Destroyed = false);
}
