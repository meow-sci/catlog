using System.Security.Cryptography;
using System.Text.Json;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Auth;

/// <summary>INITIAL_IMPL_PLAN §4.5: compact JWS, ES256 only, BCL only.</summary>
public sealed class JwsTests
{
    private const string Header = "{\"alg\":\"ES256\",\"typ\":\"catlog-proof+jwt\"}";

    [Fact]
    public void SignThenVerify_RoundTrips()
    {
        using ECDsa key = TestKeys.NewKey();

        string compact = Jws.Sign(Header, "{\"jti\":\"abc\",\"seq\":1}", key);

        Assert.True(Jws.Verify(compact, key), "a signature must verify with its own key");
        Assert.Equal(3, compact.Split('.').Length);
    }

    /// <summary>
    /// The .NET note in §4.5: <c>ECDsa.SignData(..., SHA256)</c> already emits IEEE P-1363
    /// <c>r‖s</c>, which for P-256 is exactly 64 bytes. A DER signature would be variable-length
    /// and around 70 bytes, and the Go verifier would reject it.
    /// </summary>
    [Fact]
    public void SignatureIsSixtyFourBytesOfRawRs()
    {
        using ECDsa key = TestKeys.NewKey();

        string compact = Jws.Sign(Header, "{}", key);
        byte[] signature = Bytes.FromBase64Url(compact.Split('.')[2]);

        Assert.Equal(Jws.Es256SignatureBytes, signature.Length);
        Assert.NotEqual(0x30, signature[0]); // 0x30 would be a DER SEQUENCE tag
    }

    [Fact]
    public void VerifyFails_WithADifferentKey()
    {
        using ECDsa signer = TestKeys.NewKey();
        using ECDsa other = TestKeys.NewKey();

        string compact = Jws.Sign(Header, "{\"a\":1}", signer);

        Assert.False(Jws.TryVerify(compact, other, out string error));
        Assert.Equal("signature does not verify", error);
    }

    [Fact]
    public void VerifyFails_WhenThePayloadIsTampered()
    {
        using ECDsa key = TestKeys.NewKey();
        string compact = Jws.Sign(Header, "{\"seq\":1}", key);
        string[] parts = compact.Split('.');
        string tampered = $"{parts[0]}.{Bytes.Utf8ToBase64Url("{\"seq\":2}")}.{parts[2]}";

        Assert.False(Jws.Verify(tampered, key));
    }

    [Theory]
    [InlineData("{\"alg\":\"none\"}")]
    [InlineData("{\"alg\":\"RS256\"}")]
    [InlineData("{\"alg\":\"ES384\"}")]
    [InlineData("{\"typ\":\"catlog-proof+jwt\"}")]
    public void Sign_RefusesAnythingButEs256(string header)
    {
        using ECDsa key = TestKeys.NewKey();

        Assert.Throws<System.ArgumentException>(() => Jws.Sign(header, "{}", key));
    }

    [Fact]
    public void Verify_RefusesANonEs256Header()
    {
        using ECDsa key = TestKeys.NewKey();
        // Hand-assemble a JWS whose header claims "none" but whose signature is a real ES256 one.
        string real = Jws.Sign(Header, "{}", key);
        string forged = $"{Bytes.Utf8ToBase64Url("{\"alg\":\"none\"}")}."
                        + $"{real.Split('.')[1]}.{real.Split('.')[2]}";

        Assert.False(Jws.TryVerify(forged, key, out string error));
        Assert.Equal("alg is not ES256", error);
    }

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("only.two")]
    [InlineData("a.b.c.d")]
    [InlineData("!!!.???.***")]
    public void TryParse_RejectsMalformedInput(string? compact)
    {
        Assert.False(Jws.TryParse(compact, out JwsParts? parts, out string error));
        Assert.Null(parts);
        Assert.NotEmpty(error);
    }

    [Fact]
    public void TryParse_RejectsAnOversizeJws()
    {
        string oversize = new string('a', Wire.MaxJwsBytes + 1);

        Assert.False(Jws.TryParse(oversize, out _, out string error));
        Assert.Contains("4096", error);
    }

    [Fact]
    public void DecodePayloadUnverified_ReadsClaimsWithoutAKey()
    {
        using ECDsa key = TestKeys.NewKey();
        string compact = Jws.Sign(Header, "{\"handle\":\"whiskers_prime\",\"exp\":1785552000}", key);

        using JsonDocument? document = Jws.DecodePayloadUnverified(compact);

        Assert.NotNull(document);
        Assert.Equal("whiskers_prime", document!.RootElement.GetProperty("handle").GetString());
    }

    [Fact]
    public void DecodeHeaderUnverified_ReadsTheProtectedHeader()
    {
        using ECDsa key = TestKeys.NewKey();
        string compact = Jws.Sign(Header, "{}", key);

        using JsonDocument? document = Jws.DecodeHeaderUnverified(compact);

        Assert.NotNull(document);
        Assert.Equal("catlog-proof+jwt", document!.RootElement.GetProperty("typ").GetString());
    }

    [Fact]
    public void SegmentsAreUnpaddedBase64Url()
    {
        using ECDsa key = TestKeys.NewKey();

        string compact = Jws.Sign(Header, "{\"padding\":\"aaa\"}", key);

        Assert.DoesNotContain('=', compact);
        Assert.DoesNotContain('+', compact);
        Assert.DoesNotContain('/', compact);
    }

    [Fact]
    public void VerifyFails_OnAWrongLengthSignature()
    {
        using ECDsa key = TestKeys.NewKey();
        string compact = Jws.Sign(Header, "{}", key);
        string[] parts = compact.Split('.');
        string truncated = $"{parts[0]}.{parts[1]}.{Bytes.ToBase64Url(new byte[32])}";

        Assert.False(Jws.TryVerify(truncated, key, out string error));
        Assert.Contains("64 bytes", error);
    }
}
