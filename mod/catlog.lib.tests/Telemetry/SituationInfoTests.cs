using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

/// <summary>
/// INITIAL_IMPL_PLAN §4.2 (situation is an open set) plus the packed-bitfield table verified in
/// docs/ksa-integration.md §1: <c>value = (surfaceContact &lt;&lt; 1) | onRails</c>.
/// </summary>
public sealed class SituationInfoTests
{
    [Theory]
    [InlineData("maneuvering", SurfaceContact.None, false)]
    [InlineData("freefall", SurfaceContact.None, true)]
    [InlineData("rolling", SurfaceContact.Terrain, false)]
    [InlineData("landed", SurfaceContact.Terrain, true)]
    [InlineData("sailing", SurfaceContact.Ocean, false)]
    [InlineData("floating", SurfaceContact.Ocean, true)]
    [InlineData("dragging", SurfaceContact.TerrainAndOcean, false)]
    [InlineData("bottomed", SurfaceContact.TerrainAndOcean, true)]
    public void DecodesTheCompleteEightValueTable(string situation, SurfaceContact contact, bool onRails)
    {
        Assert.Equal(contact, SituationInfo.ContactOf(situation));
        Assert.Equal(onRails, SituationInfo.IsOnRails(situation));
        Assert.True(SituationInfo.IsKnown(situation), $"'{situation}' is one of the game's eight values");
    }

    [Theory]
    [InlineData("rolling")]
    [InlineData("landed")]
    [InlineData("dragging")]
    [InlineData("bottomed")]
    public void TerrainContact(string situation)
    {
        Assert.True(SituationInfo.HasTerrainContact(situation));
        Assert.True(SituationInfo.HasSurfaceContact(situation));
    }

    [Theory]
    [InlineData("sailing")]
    [InlineData("floating")]
    [InlineData("dragging")]
    [InlineData("bottomed")]
    public void OceanContact(string situation)
    {
        Assert.True(SituationInfo.HasOceanContact(situation));
        Assert.True(SituationInfo.HasSurfaceContact(situation));
    }

    [Theory]
    [InlineData("maneuvering")]
    [InlineData("freefall")]
    public void AirborneStatesHaveNoContact(string situation)
    {
        Assert.False(SituationInfo.HasSurfaceContact(situation));
        Assert.False(SituationInfo.HasTerrainContact(situation));
        Assert.False(SituationInfo.HasOceanContact(situation));
    }

    /// <summary>
    /// The open-set contract: a value from a future game build must degrade, never throw and never
    /// fall off the end of an exhaustive switch.
    /// </summary>
    [Theory]
    [InlineData("ladderclimbing")]
    [InlineData("")]
    [InlineData(null)]
    public void UnknownSituations_DegradeToNoContactOffRails(string? situation)
    {
        Assert.False(SituationInfo.IsKnown(situation));
        Assert.Equal(SurfaceContact.None, SituationInfo.ContactOf(situation));
        Assert.False(SituationInfo.HasSurfaceContact(situation));
        Assert.False(SituationInfo.IsOnRails(situation));
    }

    [Theory]
    [InlineData("Landed", "landed")]
    [InlineData("  Freefall ", "freefall")]
    [InlineData(null, "unknown")]
    [InlineData("   ", "unknown")]
    public void Normalize_LowercasesAndDefaults(string? raw, string expected)
    {
        Assert.Equal(expected, SituationInfo.Normalize(raw));
    }
}
