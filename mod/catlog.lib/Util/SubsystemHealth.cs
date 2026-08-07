using System;
using System.Collections.Generic;
using System.Linq;

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
public sealed class SubsystemHealth
{
    private readonly object _gate = new();
    private readonly Dictionary<string, SubsystemFault> _faults = new(StringComparer.Ordinal);

    /// <summary>True while <paramref name="subsystem"/> is latched faulted.</summary>
    /// <param name="subsystem">Subsystem name.</param>
    /// <returns>True when the subsystem is disabled.</returns>
    public bool IsDead(string subsystem)
    {
        lock (_gate)
            return _faults.ContainsKey(subsystem);
    }

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
            if (_faults.ContainsKey(subsystem))
                return;
            _faults[subsystem] = new SubsystemFault(subsystem, error, permanent);
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
        bool recovered;
        lock (_gate)
        {
            recovered = _faults.TryGetValue(subsystem, out SubsystemFault? fault)
                        && !fault.Permanent
                        && _faults.Remove(subsystem);
        }

        if (recovered)
            ModLog.Log.Info($"catlog: subsystem '{subsystem}' recovered.");
    }

    /// <summary>Drops every latch. Session boundaries only.</summary>
    public void Reset()
    {
        lock (_gate)
            _faults.Clear();
    }

    /// <summary>The currently latched faults, for the status window.</summary>
    /// <returns>A snapshot of the latched faults.</returns>
    public IReadOnlyList<SubsystemFault> Snapshot()
    {
        lock (_gate)
            return _faults.Count == 0 ? [] : _faults.Values.ToArray();
    }
}
