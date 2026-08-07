using System.Linq;
using System.Security.Cryptography;
using System.Text.Json;
using MeowSci.Catlog.Lib.Auth;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Auth;

/// <summary>
/// INITIAL_IMPL_PLAN §4.5.2: EC public JWK export and RFC 7638 thumbprints. The thumbprint is the
/// hinge of proof-of-possession — the license binds <c>cnf.jkt</c> and the server compares it to
/// the thumbprint of the JWK embedded in the proof header.
/// </summary>
public sealed class JwkTests
{
    /// <summary>The RSA key from RFC 7638 §3.1, in the canonical member order the RFC specifies.</summary>
    private const string Rfc7638RsaCanonicalJwk =
        "{\"e\":\"AQAB\",\"kty\":\"RSA\",\"n\":\"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78Lh"
        + "Wx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9y"
        + "BXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQv"
        + "RL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1j"
        + "F44-csFCur-kEgU8awapJzKnqDKgw\"}";

    /// <summary>The thumbprint RFC 7638 §3.1 publishes for that key.</summary>
    private const string Rfc7638RsaThumbprint = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs";

    /// <summary>The P-256 key from RFC 7515 Appendix A.3 (the JWS ES256 example key).</summary>
    private const string Rfc7515EcX = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU";

    private const string Rfc7515EcY = "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0";

    /// <summary>
    /// SHA-256 of <c>{"crv":"P-256","kty":"EC","x":…,"y":…}</c> for that key, computed
    /// independently of catlog's implementation.
    /// </summary>
    private const string Rfc7515EcThumbprint = "oKIywvGUpTVTyxMQ3bwIIeQUudfr_CkLMjCE19ECD-U";

    /// <summary>
    /// The only officially published thumbprint vector there is. It pins the hash-and-encode half
    /// of RFC 7638 independently of catlog's own EC serializer.
    /// </summary>
    [Fact]
    public void Thumbprint_MatchesTheRfc7638PublishedVector()
    {
        Assert.Equal(Rfc7638RsaThumbprint, Jwk.ThumbprintOfCanonicalJson(Rfc7638RsaCanonicalJwk));
    }

    [Fact]
    public void Thumbprint_MatchesAnIndependentlyComputedEcVector()
    {
        string jwk = $"{{\"kty\":\"EC\",\"y\":\"{Rfc7515EcY}\",\"x\":\"{Rfc7515EcX}\",\"crv\":\"P-256\"}}";

        Assert.True(Jwk.TryThumbprintOfEcJwk(jwk, out string thumbprint, out string error), error);
        Assert.Equal(Rfc7515EcThumbprint, thumbprint);
    }

    /// <summary>
    /// Member order and whitespace in the input must not matter — the required members are
    /// re-canonicalized before hashing.
    /// </summary>
    [Fact]
    public void Thumbprint_IsIndependentOfInputOrderAndWhitespace()
    {
        string ordered = $"{{\"crv\":\"P-256\",\"kty\":\"EC\",\"x\":\"{Rfc7515EcX}\",\"y\":\"{Rfc7515EcY}\"}}";
        string scrambled =
            $"{{\n  \"y\": \"{Rfc7515EcY}\",\n  \"kty\": \"EC\",\n  \"use\": \"sig\",\n"
            + $"  \"crv\": \"P-256\",\n  \"x\": \"{Rfc7515EcX}\"\n}}";

        Assert.True(Jwk.TryThumbprintOfEcJwk(ordered, out string a, out _));
        Assert.True(Jwk.TryThumbprintOfEcJwk(scrambled, out string b, out _));
        Assert.Equal(a, b);
    }

    [Fact]
    public void PublicJwkJson_IsTheCanonicalFourMembersInOrder()
    {
        using ECDsa key = TestKeys.NewKey();

        string jwk = Jwk.PublicJwkJson(key);

        Assert.StartsWith("{\"crv\":\"P-256\",\"kty\":\"EC\",\"x\":\"", jwk);
        Assert.DoesNotContain(' ', jwk);
        Assert.DoesNotContain("\"d\"", jwk); // the private scalar must never be exported

        using JsonDocument document = JsonDocument.Parse(jwk);
        Assert.Equal(4, document.RootElement.EnumerateObject().Count());
    }

    [Fact]
    public void PublicJwkJson_RoundTripsThroughImport()
    {
        using ECDsa original = TestKeys.NewKey();
        string jwk = Jwk.PublicJwkJson(original);

        Assert.True(Jwk.TryImportPublicJwk(jwk, out ECDsa? imported, out string error), error);
        using (imported)
        {
            Assert.Equal(Jwk.Thumbprint(original), Jwk.Thumbprint(imported!));
            // The imported public key verifies what the original private key signed.
            string compact = Jws.Sign("{\"alg\":\"ES256\"}", "{\"a\":1}", original);
            Assert.True(Jws.Verify(compact, imported!));
        }
    }

    [Fact]
    public void Thumbprint_IsStableForOneKeyAndDistinctBetweenKeys()
    {
        using ECDsa a = TestKeys.NewKey();
        using ECDsa b = TestKeys.NewKey();

        Assert.Equal(Jwk.Thumbprint(a), Jwk.Thumbprint(a));
        Assert.NotEqual(Jwk.Thumbprint(a), Jwk.Thumbprint(b));
        Assert.Equal(43, Jwk.Thumbprint(a).Length); // 32 bytes, unpadded base64url
    }

    [Theory]
    [InlineData("not json")]
    [InlineData("[]")]
    [InlineData("{\"kty\":\"RSA\",\"e\":\"AQAB\",\"n\":\"aa\"}")]
    [InlineData("{\"kty\":\"EC\",\"crv\":\"P-256\"}")]
    public void TryReadEcJwk_RejectsWhatIsNotAnEcJwk(string json)
    {
        Assert.False(Jwk.TryReadEcJwk(json, out _, out _, out _, out string error));
        Assert.NotEmpty(error);
    }

    [Fact]
    public void TryImportPublicJwk_RejectsAnotherCurve()
    {
        string jwk = "{\"kty\":\"EC\",\"crv\":\"P-384\",\"x\":\"" + Rfc7515EcX + "\",\"y\":\"" + Rfc7515EcY + "\"}";

        Assert.False(Jwk.TryImportPublicJwk(jwk, out ECDsa? key, out string error));
        Assert.Null(key);
        Assert.Contains("P-384", error);
    }

    [Fact]
    public void TryImportPublicJwk_RejectsWrongLengthCoordinates()
    {
        string jwk = "{\"kty\":\"EC\",\"crv\":\"P-256\",\"x\":\"AAAA\",\"y\":\"AAAA\"}";

        Assert.False(Jwk.TryImportPublicJwk(jwk, out _, out string error));
        Assert.Contains("32 bytes", error);
    }
}
