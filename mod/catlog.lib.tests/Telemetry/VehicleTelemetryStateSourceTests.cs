using System;
using System.IO;
using System.Linq;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>Source invariants for the KSA-dependent body-centred inertial state read.</summary>
public sealed class VehicleTelemetryStateSourceTests
{
    [RepositoryFact]
    public void SampleUsesTheExactRefReadonlyStateAndRejectsParentMismatch()
    {
        string source = File.ReadAllText(RepositoryFile("mod", "catlog", "VehicleTelemetry.cs"));

        Assert.Contains("State = StateOf(orbit, body)", source, StringComparison.Ordinal);
        Assert.Contains("ref readonly StateVectors sv = ref orbit.StateVectors;", source, StringComparison.Ordinal);
        Assert.Contains("double3 pos = sv.PositionCci;", source, StringComparison.Ordinal);
        Assert.Contains("double3 vel = sv.VelocityCci;", source, StringComparison.Ordinal);
        Assert.Contains(
            "if (!StringComparer.Ordinal.Equals(body, BodyName(orbit.Parent)))",
            source,
            StringComparison.Ordinal);
        Assert.Contains(
            "StateVec.FiniteOrNull(pos.X, pos.Y, pos.Z, vel.X, vel.Y, vel.Z)",
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
