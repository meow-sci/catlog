using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.RegularExpressions;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

/// <summary>D19 (client-minted ULIDs) and §4.2 (the <c>kid</c> derivation).</summary>
public sealed partial class IdsTests
{
    [Fact]
    public void NewUlid_IsTwentySixCharactersAndParses()
    {
        string ulid = Ids.NewUlid();

        Assert.Equal(Ids.UlidLength, ulid.Length);
        Assert.True(Ids.IsUlid(ulid));
    }

    [Fact]
    public void NewUlid_IsUnique()
    {
        var seen = new HashSet<string>(StringComparer.Ordinal);

        for (int i = 0; i < 10_000; i++)
            Assert.True(seen.Add(Ids.NewUlid()), "ULIDs must not collide");
    }

    [Fact]
    public void NewUlid_IsLexicographicallySortableByTime()
    {
        DateTimeOffset t0 = DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);

        string early = Ids.NewUlid(t0);
        string late = Ids.NewUlid(t0.AddSeconds(60));

        Assert.True(string.CompareOrdinal(early, late) < 0, "ULIDs sort by their timestamp prefix");
    }

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("not-a-ulid")]
    [InlineData("01J9V5M3E8Z0FAKEULID26CH")] // 24 chars
    public void IsUlid_RejectsNonUlids(string? value)
    {
        Assert.False(Ids.IsUlid(value));
    }

    // ----- kid ------------------------------------------------------------------------

    [Fact]
    public void KittenId_IsSixteenCrockfordCharacters()
    {
        string kid = Ids.KittenId(TestData.InstallId, "Whiskers");

        Assert.Equal(16, kid.Length);
        Assert.Matches(CrockfordLower(), kid);
    }

    [Fact]
    public void KittenId_IsStableForTheSameInputs()
    {
        Assert.Equal(
            Ids.KittenId(TestData.InstallId, "Whiskers"),
            Ids.KittenId(TestData.InstallId, "Whiskers"));
    }

    /// <summary>
    /// Salting with the install id is what stops the server learning that two players named a
    /// kitten the same thing.
    /// </summary>
    [Fact]
    public void KittenId_DiffersBetweenInstallsAndBetweenNames()
    {
        Assert.NotEqual(
            Ids.KittenId("install-a", "Whiskers"),
            Ids.KittenId("install-b", "Whiskers"));
        Assert.NotEqual(
            Ids.KittenId(TestData.InstallId, "Whiskers"),
            Ids.KittenId(TestData.InstallId, "whiskers"));
    }

    [Fact]
    public void KittenId_UsesTheContractPrefixAndFirstTenBytes()
    {
        // Recomputed here from the §4.2 definition, independently of the implementation:
        // lowercase Crockford base32 of the first 10 bytes of
        // SHA-256("catlog-kitten:" + install_id + ":" + roster_name).
        byte[] digest = System.Security.Cryptography.SHA256.HashData(
            System.Text.Encoding.UTF8.GetBytes($"catlog-kitten:{TestData.InstallId}:Whiskers"));
        string expected = Crockford(digest.Take(10).ToArray());

        Assert.Equal(expected, Ids.KittenId(TestData.InstallId, "Whiskers"));
    }

    // ----- name sanitization ----------------------------------------------------------

    [Theory]
    [InlineData("Whiskers", "Whiskers")]
    [InlineData("  Whiskers  ", "Whiskers")]
    [InlineData("Whïskers", "Whskers")] // non-ASCII stripped
    [InlineData("Whis\nkers", "Whiskers")] // control characters stripped
    [InlineData("", "kitten")]
    [InlineData(null, "kitten")]
    public void SanitizeName_KeepsPrintableAscii(string? input, string expected)
    {
        Assert.Equal(expected, Ids.SanitizeName(input));
    }

    [Fact]
    public void SanitizeName_CapsAtThirtyTwoCharacters()
    {
        Assert.Equal(32, Ids.SanitizeName(new string('x', 100)).Length);
    }

    [Fact]
    public void SanitizeVehicleName_CapsAtSixtyFourCharacters()
    {
        Assert.Equal(64, Ids.SanitizeVehicleName(new string('x', 200)).Length);
        Assert.Equal("vehicle", Ids.SanitizeVehicleName(null));
    }

    private static string Crockford(byte[] bytes)
    {
        const string Alphabet = "0123456789abcdefghjkmnpqrstvwxyz";
        ulong acc = 0;
        int bits = 0;
        var sb = new System.Text.StringBuilder();
        foreach (byte b in bytes)
        {
            acc = (acc << 8) | b;
            bits += 8;
            while (bits >= 5)
            {
                bits -= 5;
                sb.Append(Alphabet[(int)((acc >> bits) & 0x1F)]);
            }
        }

        return sb.ToString();
    }

    [GeneratedRegex("^[0-9abcdefghjkmnpqrstvwxyz]{16}$")]
    private static partial Regex CrockfordLower();
}
