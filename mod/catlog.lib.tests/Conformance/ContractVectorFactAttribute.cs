using Xunit;
using Xunit.Sdk;

namespace MeowSci.Catlog.Lib.Tests.Conformance;

/// <summary>
/// A <see cref="FactAttribute"/> that skips itself — with an explanatory message — when
/// <c>contracts/testdata/</c> has not been generated yet.
/// </summary>
/// <remarks>
/// <b>TODO(WP2)</b>: the vectors come from <c>catlogctl testvectors generate contracts/testdata</c>,
/// which lands in WP2. Once it has run, every test wearing this attribute switches itself on with
/// no code change.
/// <para>
/// xunit 2.9's <c>Assert</c> has no dynamic <c>Skip</c> (that arrived in xunit v3), and an
/// unconditional <c>[Fact(Skip = …)]</c> would never switch back on. Setting
/// <see cref="FactAttribute.Skip"/> from a subclass constructor is the xunit 2 idiom for a
/// conditional skip, and it needs no extra package.
/// </para>
/// </remarks>
[XunitTestCaseDiscoverer("Xunit.Sdk.FactDiscoverer", "xunit.execution.dotnet")]
public sealed class ContractVectorFactAttribute : FactAttribute
{
    /// <summary>Marks the test skipped when the conformance vectors are absent.</summary>
    public ContractVectorFactAttribute()
    {
        if (TestPaths.ContractsTestData is null)
        {
            Skip = "contracts/testdata is empty — run `catlogctl testvectors generate contracts/testdata` "
                   + "(WP2) to enable the cross-language conformance suite.";
        }
    }
}
