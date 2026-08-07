using System;
using System.IO;
using System.Security.Cryptography;
using MeowSci.Catlog.Lib.Auth;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Auth;

/// <summary>
/// INITIAL_IMPL_PLAN §4.6: the credential file. Loading never throws, and a credential whose key
/// does not match the license's <c>cnf.jkt</c> is refused rather than shipped.
/// </summary>
public sealed class CredentialTests
{
    [Fact]
    public void Parse_AcceptsACoherentCredential()
    {
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            Assert.Equal("whiskers_prime", credential.Handle);
            Assert.Equal(credential.Jkt, credential.Claims.Jkt);
            Assert.Equal("http://127.0.0.1:8080", credential.Claims.Issuer);
            Assert.Equal(1, credential.Claims.Version);
            Assert.True(Jws.Verify(credential.License, serverKey), "the license must verify with the server key");
        }
    }

    /// <summary>
    /// §4.6: "refuse to start shipping if jkt ≠ license cnf.jkt". Shipping anyway would mean every
    /// batch coming back <c>401 proof_invalid</c>; failing at load turns that into one message.
    /// </summary>
    [Fact]
    public void Parse_RefusesAKeyThatDoesNotMatchTheLicenseThumbprint()
    {
        using ECDsa clientKey = TestKeys.NewKey();
        using ECDsa strangerKey = TestKeys.NewKey();
        using ECDsa serverKey = TestKeys.NewKey();
        string license = TestKeys.License(serverKey, Jwk.Thumbprint(strangerKey));

        CredentialLoadResult result = Credential.Parse(TestKeys.CredentialJson(clientKey, license));

        Assert.False(result.Ok);
        Assert.Contains("does not match the license cnf.jkt", result.Error);
    }

    [Theory]
    [InlineData("")]
    [InlineData("not json at all")]
    [InlineData("{}")]
    [InlineData("{\"format\":2,\"handle\":\"h\",\"license\":\"a.b.c\",\"private_key_pem\":\"x\"}")]
    [InlineData("{\"format\":1,\"handle\":\"h\",\"license\":\"\",\"private_key_pem\":\"x\"}")]
    [InlineData("{\"format\":1,\"handle\":\"h\",\"license\":\"a.b.c\",\"private_key_pem\":\"\"}")]
    [InlineData("{\"format\":1,\"handle\":\"h\",\"license\":\"garbage\",\"private_key_pem\":\"x\"}")]
    public void Parse_NeverThrowsAndAlwaysExplains(string json)
    {
        CredentialLoadResult result = Credential.Parse(json);

        Assert.False(result.Ok);
        Assert.NotEmpty(result.Error);
    }

    [Fact]
    public void Parse_RejectsALicenseWithNoCnfJkt()
    {
        using ECDsa clientKey = TestKeys.NewKey();
        using ECDsa serverKey = TestKeys.NewKey();
        string license = Jws.Sign(
            "{\"alg\":\"ES256\"}", "{\"iss\":\"http://x\",\"handle\":\"h\"}", serverKey);

        CredentialLoadResult result = Credential.Parse(TestKeys.CredentialJson(clientKey, license));

        Assert.False(result.Ok);
        Assert.Contains("cnf.jkt", result.Error);
    }

    [Fact]
    public void Parse_RejectsAnUnusablePrivateKey()
    {
        using ECDsa serverKey = TestKeys.NewKey();
        using ECDsa realKey = TestKeys.NewKey();
        string license = TestKeys.License(serverKey, Jwk.Thumbprint(realKey));
        string json = "{\"format\":1,\"handle\":\"h\",\"license\":\"" + license
                      + "\",\"private_key_pem\":\"-----BEGIN PRIVATE KEY-----\\nnope\\n-----END PRIVATE KEY-----\"}";

        CredentialLoadResult result = Credential.Parse(json);

        Assert.False(result.Ok);
        Assert.Contains("private key", result.Error);
    }

    [Fact]
    public void Load_ReadsFromDiskAndReportsAMissingFile()
    {
        using var dir = new TempDir();
        (Credential fixture, ECDsa serverKey, string json) = TestKeys.Credential();
        fixture.Dispose();
        serverKey.Dispose();
        string path = TestKeys.WriteCredential(dir.Path, json);

        CredentialLoadResult ok = Credential.Load(path);
        using (ok.Credential)
            Assert.True(ok.Ok, ok.Error);

        CredentialLoadResult missing = Credential.Load(Path.Combine(dir.Path, "nope.json"));
        Assert.False(missing.Ok);
        Assert.Contains("does not exist", missing.Error);

        CredentialLoadResult blank = Credential.Load("   ");
        Assert.False(blank.Ok);
        Assert.Contains("no credential path", blank.Error);
    }

    [Fact]
    public void Claims_ExposeExpiry()
    {
        DateTimeOffset issued = DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);
        (Credential credential, ECDsa serverKey, _) =
            TestKeys.Credential(issuedAt: issued, lifetime: TimeSpan.FromDays(180));
        using (credential)
        using (serverKey)
        {
            Assert.Equal(issued.AddDays(180).ToUnixTimeSeconds(), credential.Claims.ExpiresAt);
            Assert.False(credential.Claims.IsExpired(issued.AddDays(179)));
            Assert.True(credential.Claims.IsExpired(issued.AddDays(181)));
        }
    }

    [Fact]
    public void PublicJwkJson_IsWhatGoesInTheProofHeader()
    {
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            Assert.Equal(Jwk.Thumbprint(credential.Key), credential.Jkt);
            Assert.True(
                Jwk.TryThumbprintOfEcJwk(credential.PublicJwkJson, out string thumbprint, out string error), error);
            Assert.Equal(credential.Jkt, thumbprint);
        }
    }
}
