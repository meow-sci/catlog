using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Events;

/// <summary>
/// Which event types a pipeline may emit — the runtime form of the <c>[events]</c> table in
/// <c>catlog.toml</c>.
/// </summary>
/// <remarks>
/// <para>
/// Deliberately a narrow value rather than a <c>ModConfig</c>: the pipeline is driven by the
/// shipped mod, <c>catlog.sim</c>, <c>catlog.loadgen</c> and the test suite, and only one of those
/// four has a config file. Handing it a set of names keeps the other three honest without giving
/// them a config object they have no use for.
/// </para>
/// <para>
/// <b>The spine cannot be filtered out here either.</b> <see cref="EventTypes.AlwaysReported"/>
/// names are dropped by <see cref="Create"/>, so a filter built by hand — bypassing
/// <c>ModConfig.Normalize</c> entirely — still cannot express "stop reporting
/// <c>flight.flagged</c>". This is the same two-layer shape as the 30-second reporting interval:
/// the config clamp is the courtesy so the file a player reads is the truth, and the layer below
/// is the guarantee, because clamping the config alone only closes the path you thought of.
/// </para>
/// </remarks>
public sealed class EventTypeFilter
{
    /// <summary>Everything enabled. The default for every caller that does not care.</summary>
    public static readonly EventTypeFilter All = new(null);

    // SortedSet, not HashSet: ≤22 ordinal strings make the lookup cost irrelevant, and a
    // deterministic enumeration order is what lets DisabledList be a stable, cacheable string.
    private readonly SortedSet<string>? _disabled;

    private EventTypeFilter(SortedSet<string>? disabled)
    {
        _disabled = disabled;
        DisabledList = disabled is null ? string.Empty : string.Join(", ", disabled);
    }

    /// <summary>The disabled type names, ordinal-sorted. Empty when everything is enabled.</summary>
    public IReadOnlyCollection<string> Disabled => _disabled ?? (IReadOnlyCollection<string>)Array.Empty<string>();

    /// <summary>
    /// The disabled names as one comma-separated string, or empty. Precomputed because the status
    /// window redraws it every frame and must not allocate to do so.
    /// </summary>
    public string DisabledList { get; }

    /// <summary>True when at least one type is disabled.</summary>
    public bool HasDisabled => _disabled is not null;

    /// <summary>
    /// Builds a filter that suppresses <paramref name="disabled"/>, minus anything
    /// <see cref="EventTypes.AlwaysReported"/> refuses to give up.
    /// </summary>
    /// <param name="disabled">Type names to suppress. Null or empty yields <see cref="All"/>.</param>
    /// <returns>The filter.</returns>
    public static EventTypeFilter Create(IEnumerable<string>? disabled)
    {
        if (disabled is null)
            return All;

        SortedSet<string>? set = null;
        foreach (string type in disabled)
        {
            if (EventTypes.IsAlwaysReported(type))
            {
                // Reached only by a caller that built its own filter: ModConfig.Normalize has
                // already dropped these with a warning naming the key. Say so rather than
                // swallowing it, because a silent refusal reads as a bug at the call site.
                ModLog.Log.Warn(
                    $"catlog: '{type}' cannot be disabled — it is one of the {EventTypes.AlwaysReported.Count} "
                    + "types every catlog install reports. Ignoring it.");
                continue;
            }

            (set ??= new SortedSet<string>(StringComparer.Ordinal)).Add(type);
        }

        return set is null ? All : new EventTypeFilter(set);
    }

    /// <summary>True when <paramref name="type"/> may be emitted.</summary>
    /// <param name="type">The event type name.</param>
    /// <returns>True unless the type was disabled.</returns>
    public bool IsEnabled(string type) => _disabled is null || !_disabled.Contains(type);
}
