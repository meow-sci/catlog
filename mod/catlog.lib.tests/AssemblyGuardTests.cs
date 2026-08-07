using System;
using System.Linq;
using System.Reflection;
using MeowSci.Catlog.Lib;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>
/// Enforces D13 / §7.1: catlog.lib is KSA-free. This is the test that keeps the whole
/// ingest path unit-testable on a machine with no game install — if it ever fails, the
/// demarcation has been breached and the offending reference belongs in mod/catlog/.
/// </summary>
public sealed class AssemblyGuardTests
{
    /// <summary>Assembly-name prefixes that must never appear in catlog.lib's metadata.</summary>
    private static readonly string[] ForbiddenPrefixes = ["KSA", "Brutal", "0Harmony"];

    [Fact]
    public void CatlogLib_ReferencesNoGameAssemblies()
    {
        Assembly lib = typeof(CatlogLib).Assembly;

        string[] offenders = lib.GetReferencedAssemblies()
            .Select(static name => name.Name ?? string.Empty)
            .Where(static name => ForbiddenPrefixes.Any(prefix =>
                name.StartsWith(prefix, StringComparison.OrdinalIgnoreCase)))
            .OrderBy(static name => name, StringComparer.Ordinal)
            .ToArray();

        Assert.True(
            offenders.Length == 0,
            $"{lib.GetName().Name} must not reference KSA/Brutal/Harmony assemblies, but references: "
                + string.Join(", ", offenders));
    }

    [Fact]
    public void CatlogLib_AssemblyNameMatchesTheMarker()
    {
        Assert.Equal(CatlogLib.AssemblyName, typeof(CatlogLib).Assembly.GetName().Name);
    }
}
