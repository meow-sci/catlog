using System;
using System.IO;
using System.Linq;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>Source invariants for the KSA-dependent orbital-element reads.</summary>
public sealed class VehicleTelemetryOrbitSourceTests
{
    [RepositoryFact]
    public void SampleReadsSanitizesAndConvertsEveryFinalV1OrbitElement()
    {
        string source = File.ReadAllText(RepositoryFile("mod", "catlog", "VehicleTelemetry.cs"));

        Assert.Contains("SmaM = Sanitize.Finite(orbit.SemiMajorAxis)", source, StringComparison.Ordinal);
        Assert.Contains(
            "LanDeg = Sanitize.Finite(orbit.LongitudeOfAscendingNode * RadToDeg)",
            source,
            StringComparison.Ordinal);
        Assert.Contains(
            "ArgpDeg = Sanitize.Finite(orbit.ArgumentOfPeriapsis * RadToDeg)",
            source,
            StringComparison.Ordinal);
        Assert.Contains("TPe = Sanitize.Finite(orbit.TimeAtPeriapsis.Seconds())", source, StringComparison.Ordinal);
        Assert.Contains(
            "PeriodS = conic == OrbitClass.Bound ? Sanitize.Finite(orbit.Period) : 0.0",
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
