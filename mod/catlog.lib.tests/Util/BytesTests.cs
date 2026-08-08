using System;
using System.Text;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

/// <summary>
/// §4.5: base64url without padding, and the SHA-256 shapes behind <c>bh</c>,
/// <c>ph</c> and <c>jkt</c>.
/// </summary>
public sealed class BytesTests
{
    [Theory]
    [InlineData("", "")]
    [InlineData("f", "Zg")]
    [InlineData("fo", "Zm8")]
    [InlineData("foo", "Zm9v")]
    [InlineData("foob", "Zm9vYg")]
    [InlineData("fooba", "Zm9vYmE")]
    [InlineData("foobar", "Zm9vYmFy")]
    public void Base64UrlIsUnpadded(string input, string expected)
    {
        Assert.Equal(expected, Bytes.Utf8ToBase64Url(input));
    }

    [Fact]
    public void Base64UrlUsesTheUrlAlphabet()
    {
        // 0xFB 0xFF encodes to "+/8" in standard base64 and "-_8" in base64url.
        string encoded = Bytes.ToBase64Url([0xFB, 0xFF]);

        Assert.Equal("-_8", encoded);
        Assert.DoesNotContain('+', encoded);
        Assert.DoesNotContain('/', encoded);
        Assert.DoesNotContain('=', encoded);
    }

    [Fact]
    public void Base64UrlRoundTrips()
    {
        byte[] original = new byte[256];
        for (int i = 0; i < original.Length; i++)
            original[i] = (byte)i;

        Assert.Equal(original, Bytes.FromBase64Url(Bytes.ToBase64Url(original)));
    }

    [Theory]
    [InlineData(null)]
    [InlineData("!!!!")]
    [InlineData("a")] // a single character can never be a base64url group
    public void TryFromBase64Url_RejectsGarbageWithoutThrowing(string? input)
    {
        Assert.False(Bytes.TryFromBase64Url(input, out byte[] decoded));
        Assert.Empty(decoded);
    }

    /// <summary>
    /// The canonical NIST empty-string SHA-256, base64url-encoded. This is the exact shape the
    /// proof's <c>bh</c> carries.
    /// </summary>
    [Fact]
    public void Sha256Base64Url_MatchesTheKnownEmptyDigest()
    {
        Assert.Equal("47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU", Bytes.Sha256Base64Url(Array.Empty<byte>()));
    }

    [Fact]
    public void Sha256Base64Url_MatchesTheKnownAbcDigest()
    {
        // SHA-256("abc") = ba7816bf 8f01cfea 414140de 5dae2223 b00361a3 96177a9c b410ff61 f20015ad
        Assert.Equal("ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0", Bytes.Sha256Base64Url("abc"));
    }

    [Fact]
    public void Sha256_ProducesThirtyTwoBytes()
    {
        Assert.Equal(32, Bytes.Sha256(Encoding.UTF8.GetBytes("catlog")).Length);
    }

    [Fact]
    public void FixedTimeEquals()
    {
        byte[] a = Bytes.Sha256("catlog"u8);
        byte[] b = Bytes.Sha256("catlog"u8);
        byte[] c = Bytes.Sha256("catlogg"u8);

        Assert.True(Bytes.FixedTimeEquals(a, b));
        Assert.False(Bytes.FixedTimeEquals(a, c));
    }
}
