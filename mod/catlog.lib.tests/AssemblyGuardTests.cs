using System;
using System.Linq;
using System.Reflection;
using MeowSci.Catlog.Lib.Events;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>
/// INITIAL_IMPL_PLAN D13 / §7.1: catlog.lib is KSA-free. This is the test that keeps the whole
/// ingest path unit-testable on a machine with no game install — if it ever fails, the demarcation
/// has been breached and the offending reference belongs in mod/catlog/.
/// </summary>
public sealed class AssemblyGuardTests
{
    /// <summary>Assembly-name prefixes that must never appear in catlog.lib's metadata.</summary>
    private static readonly string[] ForbiddenPrefixes = ["KSA", "Brutal", "0Harmony", "StarMap", "Planet"];

    /// <summary>
    /// Anchored on <see cref="EventEnvelope"/> as §7.1 specifies: the guard follows a type the
    /// library genuinely needs, so it cannot be satisfied by a marker that outlives its purpose.
    /// </summary>
    [Fact]
    public void CatlogLib_ReferencesNoGameAssemblies()
    {
        Assembly lib = typeof(EventEnvelope).Assembly;

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
    public void CatlogLib_IsTheAssemblyUnderTest()
    {
        Assert.Equal("MeowSci.Catlog.Lib", typeof(EventEnvelope).Assembly.GetName().Name);
    }

    /// <summary>
    /// The NuGet dependencies added in WP6 (Microsoft.Data.Sqlite, Ulid, Tomlyn) must not have
    /// dragged in anything game-shaped transitively either.
    /// </summary>
    [Fact]
    public void CatlogLib_ReferencesOnlyExpectedThirdPartyAssemblies()
    {
        string[] thirdParty = typeof(EventEnvelope).Assembly.GetReferencedAssemblies()
            .Select(static name => name.Name ?? string.Empty)
            .Where(static name => !name.StartsWith("System.", StringComparison.Ordinal)
                                  && !string.Equals(name, "System", StringComparison.Ordinal)
                                  && !string.Equals(name, "netstandard", StringComparison.Ordinal))
            .OrderBy(static name => name, StringComparer.Ordinal)
            .ToArray();

        Assert.True(
            thirdParty.All(static name =>
                    name is "Microsoft.Data.Sqlite"
                    or "SQLitePCLRaw.batteries_v2"
                    or "SQLitePCLRaw.core"
                    or "Tomlyn"
                    or "Ulid"),
            "unexpected third-party reference in catlog.lib: " + string.Join(", ", thirdParty));
    }
}
