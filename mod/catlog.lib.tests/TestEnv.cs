using System;
using System.IO;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>A throwaway directory that deletes itself on dispose.</summary>
internal sealed class TempDir : IDisposable
{
    internal TempDir(string prefix = "catlog-test")
    {
        Path = System.IO.Path.Combine(
            System.IO.Path.GetTempPath(), $"{prefix}-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path);
    }

    /// <summary>The directory's absolute path.</summary>
    internal string Path { get; }

    /// <summary>A path inside the directory.</summary>
    internal string File(string name) => System.IO.Path.Combine(Path, name);

    public void Dispose()
    {
        try
        {
            if (Directory.Exists(Path))
                Directory.Delete(Path, recursive: true);
        }
        catch (IOException)
        {
            // A leaked temp directory is not worth failing a test over.
        }
    }
}

/// <summary>Locates repository-relative fixtures from the test binary's output directory.</summary>
internal static class TestPaths
{
    /// <summary>
    /// The repository root, found by walking up from the test assembly until a directory
    /// containing <c>INITIAL_IMPL_PLAN.md</c> is seen; null when not found (e.g. a packaged run).
    /// </summary>
    internal static string? RepoRoot { get; } = FindRepoRoot();

    /// <summary>
    /// <c>contracts/testdata</c>, or null when it does not exist yet. WP2 generates it with
    /// <c>catlogctl testvectors generate</c>; until then the conformance suite skips.
    /// </summary>
    internal static string? ContractsTestData
    {
        get
        {
            if (RepoRoot is null)
                return null;
            string path = System.IO.Path.Combine(RepoRoot, "contracts", "testdata");
            // The directory is committed with only a .gitkeep until WP2 lands, so presence is not
            // enough — probe for the manifest the generator writes.
            return System.IO.File.Exists(System.IO.Path.Combine(path, "expected", "verify-results.json"))
                ? path
                : null;
        }
    }

    private static string? FindRepoRoot()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            if (System.IO.File.Exists(System.IO.Path.Combine(directory.FullName, "INITIAL_IMPL_PLAN.md")))
                return directory.FullName;
            directory = directory.Parent;
        }

        return null;
    }
}
