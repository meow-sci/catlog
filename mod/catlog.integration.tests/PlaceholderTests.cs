using MeowSci.Catlog.Lib;
using Xunit;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>
/// WP0 placeholder so the project builds and `dotnet test` has something to run without a
/// server. WP7 replaces this with the §7.5 fixture (spawns server/bin/catlogd on a random
/// port with a temp data dir) and the ship / replay / tamper / skew / revoke / oversize cases.
/// </summary>
public sealed class PlaceholderTests
{
    [Fact]
    public void CatlogLib_IsReferenceable()
    {
        Assert.Equal("MeowSci.Catlog.Lib", CatlogLib.AssemblyName);
    }
}
