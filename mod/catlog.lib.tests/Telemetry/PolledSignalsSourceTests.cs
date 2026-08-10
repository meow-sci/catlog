using System;
using System.IO;
using System.Linq;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>Source invariants for the KSA-dependent tumble edge detector.</summary>
public sealed class PolledSignalsSourceTests
{
    [RepositoryFact]
    public void TumbleOriginComesFromThePreviousLocomotionState()
    {
        string source = File.ReadAllText(RepositoryFile("mod", "catlog", "PolledSignals.cs"));
        int transition = source.IndexOf(
            "now.Locomotion == LocomotionMode.Tumbling && state.Locomotion != LocomotionMode.Tumbling",
            StringComparison.Ordinal);
        int previous = source.IndexOf(
            "LocomotionModeName.FromGameName(state.Locomotion?.ToString())",
            transition,
            StringComparison.Ordinal);
        int stateAdvance = source.IndexOf("_vehicles[id] = now;", transition, StringComparison.Ordinal);

        Assert.True(transition >= 0, "the tumble detector must remain an edge into Tumbling");
        Assert.True(previous > transition && previous < stateAdvance,
            "TumbleSignal must map the previous state.Locomotion before advancing tracked state");
        Assert.DoesNotContain(
            "LocomotionModeName.FromGameName(now.Locomotion?.ToString())",
            source,
            StringComparison.Ordinal);
    }

    private static string RepositoryFile(params string[] parts)
    {
        string? root = AppContext.BaseDirectory;
        while (root is not null && !File.Exists(Path.Combine(root, "AGENTS.md")))
            root = Directory.GetParent(root)?.FullName;
        return root is null ? string.Empty : Path.Combine(new[] { root }.Concat(parts).ToArray());
    }
}
