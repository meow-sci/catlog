using System;
using System.IO;
using System.Linq;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

public sealed class SystemSurveySourceTests
{
    [RepositoryFact]
    public void SurveyFiltersBodiesSortsRawIdsAndModelsForest()
    {
        string source = File.ReadAllText(RepositoryFile("mod", "catlog", "SystemSurvey.cs"));
        Assert.Contains("system.All.OfType<IParentBody>()", source, StringComparison.Ordinal);
        Assert.Contains("string.CompareOrdinal(left.Id, right.Id)", source, StringComparison.Ordinal);
        Assert.DoesNotContain("system.Count", source, StringComparison.Ordinal);
        Assert.Contains("rawSystemId,", source, StringComparison.Ordinal);
        Assert.DoesNotContain("Ids.SanitizeCatalogueName(rawSystemId)", source, StringComparison.Ordinal);
        Assert.Contains("rotating.GetRotationAxisCce() : double3.UnitZ", source, StringComparison.Ordinal);
        Assert.Contains(
            "while (current is Celestial celestial && celestial.Parent is { } parent)",
            source,
            StringComparison.Ordinal);
        Assert.Contains("current = parent;", source, StringComparison.Ordinal);
        Assert.DoesNotContain(
            "while (current is Celestial celestial)\n        {\n            rank = checked(rank + 1);",
            source,
            StringComparison.Ordinal);
    }

    [RepositoryFact]
    public void SurveyIsCapturedByPostfixAndHasAlreadyLoadedFallback()
    {
        string patcher = File.ReadAllText(RepositoryFile("mod", "catlog", "Patcher.cs"));
        string mod = File.ReadAllText(RepositoryFile("mod", "catlog", "Mod.cs"));
        Assert.Contains("postfix: nameof(LoadSystemPostfix)", patcher, StringComparison.Ordinal);
        int capture = patcher.IndexOf("SystemSurvey.CaptureCurrent();", StringComparison.Ordinal);
        int boundary = patcher.IndexOf("_runtime?.OnSessionBoundary();", capture, StringComparison.Ordinal);
        Assert.True(capture >= 0 && boundary > capture, "LoadSystem postfix must capture before establishing the boundary");
        Assert.Contains("if (Universe.CurrentSystem is not null)", mod, StringComparison.Ordinal);
        Assert.Contains("SystemSurvey.CaptureCurrent()", mod, StringComparison.Ordinal);
        Assert.Contains("_runtime.OnSessionBoundary()", mod, StringComparison.Ordinal);
    }

    private static string RepositoryFile(params string[] parts)
    {
        string? root = AppContext.BaseDirectory;
        while (root is not null && !File.Exists(Path.Combine(root, "AGENTS.md")))
            root = Directory.GetParent(root)?.FullName;
        return root is null ? string.Empty : Path.Combine(new[] { root }.Concat(parts).ToArray());
    }
}

public sealed class RepositoryFactAttribute : FactAttribute
{
    public RepositoryFactAttribute()
    {
        string? root = AppContext.BaseDirectory;
        while (root is not null && !File.Exists(Path.Combine(root, "AGENTS.md")))
            root = Directory.GetParent(root)?.FullName;
        if (root is null)
            Skip = "Repository sources are not on disk.";
    }
}
