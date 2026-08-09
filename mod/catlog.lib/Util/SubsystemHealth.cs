using System;
using System.Collections.Generic;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>A latched subsystem fault, as rendered by the in-game status window (§7.4).</summary>
/// <param name="Subsystem">The subsystem name, e.g. <c>"outbox"</c>.</param>
/// <param name="Error">The first fault message seen.</param>
/// <param name="Permanent">True when the latch never clears for the rest of the session.</param>
public sealed record SubsystemFault(string Subsystem, string Error, bool Permanent);

/// <summary>
/// Per-subsystem dead-latch registry (§7.2: "one ERROR log then disable that subsystem for the
/// session; never throw across the host boundary"). Adapted from
/// <c>gatOS/gatOS.GameMod/Game/Ksa/KsaHealth.cs</c>, with the addition of a permanent latch:
/// gatOS clears on success because a KSA reader can transiently fail, but a bad credential or an
/// unopenable outbox will not fix itself and must stay off and stay visible.
/// </summary>
/// <remarks>
/// <b>Copy-on-write, because the readers are on the game thread.</b> Latches are written a handful
/// of times per session at most, and read several times per <i>frame</i> — <c>IsCollecting</c>
/// checks two of them before every tick, the sample pass clears one after every successful pass,
/// and the status window enumerates them while it is open. Taking a monitor for each of those puts
/// the game thread behind whichever background task happens to be faulting or reading at that
/// moment; a table that is replaced rather than mutated lets every read be a plain lookup on an
/// object nobody will ever touch again. Writers still serialise on <c>_gate</c>, which is what keeps
/// "the first fault wins, and it logs exactly once" true.
/// </remarks>
public sealed class SubsystemHealth
{
    private readonly object _gate = new();

    // Never mutated after publication: a writer builds a new Latches and swaps it in.
    private volatile Latches _latches = Latches.Empty;

    /// <summary>True while <paramref name="subsystem"/> is latched faulted.</summary>
    /// <param name="subsystem">Subsystem name.</param>
    /// <returns>True when the subsystem is disabled.</returns>
    public bool IsDead(string subsystem) => _latches.Map.ContainsKey(subsystem);

    /// <summary>
    /// Latches <paramref name="subsystem"/> faulted on the first fault and logs once. Subsequent
    /// faults for the same subsystem are silent — this is what stops a per-frame exception from
    /// becoming a per-frame log line.
    /// </summary>
    /// <param name="subsystem">Subsystem name.</param>
    /// <param name="error">Human-readable reason.</param>
    /// <param name="exception">The fault, when there is one.</param>
    /// <param name="permanent">False to allow <see cref="Clear"/> to un-latch on a later success.</param>
    public void Fault(string subsystem, string error, Exception? exception = null, bool permanent = true)
    {
        lock (_gate)
        {
            Latches current = _latches;
            if (current.Map.ContainsKey(subsystem))
                return;

            var map = new Dictionary<string, SubsystemFault>(current.Map, StringComparer.Ordinal)
            {
                [subsystem] = new SubsystemFault(subsystem, error, permanent),
            };
            _latches = new Latches(map);
        }

        ModLog.Log.Error($"catlog: subsystem '{subsystem}' disabled for this session: {error}", exception);
    }

    /// <summary>
    /// Clears a non-permanent latch after a success. A permanent latch is never cleared — call
    /// <see cref="Reset"/> if a whole new session is starting.
    /// </summary>
    /// <param name="subsystem">Subsystem name.</param>
    public void Clear(string subsystem)
    {
        // The healthy path, which is the one that runs every sample tick: nothing latched, nothing
        // to do, no lock taken.
        if (!_latches.Map.ContainsKey(subsystem))
            return;

        bool recovered;
        lock (_gate)
        {
            Latches current = _latches;
            recovered = current.Map.TryGetValue(subsystem, out SubsystemFault? fault) && !fault.Permanent;
            if (recovered)
            {
                var map = new Dictionary<string, SubsystemFault>(current.Map, StringComparer.Ordinal);
                map.Remove(subsystem);
                _latches = new Latches(map);
            }
        }

        if (recovered)
            ModLog.Log.Info($"catlog: subsystem '{subsystem}' recovered.");
    }

    /// <summary>Drops every latch. Session boundaries only.</summary>
    public void Reset()
    {
        lock (_gate)
            _latches = Latches.Empty;
    }

    /// <summary>The currently latched faults, for the status window.</summary>
    /// <returns>A snapshot of the latched faults.</returns>
    public IReadOnlyList<SubsystemFault> Snapshot() => _latches.All;

    // One immutable table plus the array the status window renders, built together so a per-frame
    // Snapshot() is a field read rather than a copy.
    private sealed class Latches(Dictionary<string, SubsystemFault> map)
    {
        internal static Latches Empty { get; } = new(new Dictionary<string, SubsystemFault>(StringComparer.Ordinal));

        internal Dictionary<string, SubsystemFault> Map { get; } = map;

        internal SubsystemFault[] All { get; } = map.Count == 0 ? [] : [.. map.Values];
    }
}
