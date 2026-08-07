using System;
using System.IO;
using System.Security.Cryptography;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>
/// Key and credential fixtures. Everything here is generated in-process — no network, no files
/// outside a temp directory, no checked-in private keys.
/// </summary>
internal static class TestKeys
{
    /// <summary>A fresh P-256 key pair.</summary>
    internal static ECDsa NewKey() => ECDsa.Create(ECCurve.NamedCurves.nistP256);

    /// <summary>
    /// Mints a license JWS the way the Go server would (§4.5.1), signed by
    /// <paramref name="serverKey"/> and bound to <paramref name="jkt"/>.
    /// </summary>
    internal static string License(
        ECDsa serverKey,
        string jkt,
        string handle = "whiskers_prime",
        string issuer = "http://127.0.0.1:8080",
        DateTimeOffset? issuedAt = null,
        TimeSpan? lifetime = null)
    {
        DateTimeOffset iat = issuedAt ?? DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);
        DateTimeOffset exp = iat + (lifetime ?? TimeSpan.FromDays(180));
        string header = $"{{\"alg\":\"ES256\",\"kid\":\"catlog-202608\",\"typ\":\"{Wire.LicenseTyp}\"}}";
        string claims =
            $$"""
              {"iss":"{{issuer}}","sub":"aGVsbG8td29ybGQtdGVzdC1zdWJqZWN0LTMyLWJ5dGVz","handle":"{{handle}}",
              "cnf":{"jkt":"{{jkt}}"},"iat":{{iat.ToUnixTimeSeconds()}},"exp":{{exp.ToUnixTimeSeconds()}},
              "jti":"lic_01J9V5M3E8Z0FAKELICENSE1","ver":1}
              """.Replace("\n", string.Empty);
        return Jws.Sign(header, claims, serverKey);
    }

    /// <summary>The §4.6 credential file JSON for a client key and license.</summary>
    internal static string CredentialJson(ECDsa clientKey, string license, string handle = "whiskers_prime")
    {
        string pem = new string(clientKey.ExportPkcs8PrivateKeyPem());
        var file = new CredentialFile(Wire.CredentialFormat, handle, license, pem);
        return CatlogJson.Serialize(file);
    }

    /// <summary>
    /// A fully coherent credential: fresh client key, a license bound to its thumbprint. The
    /// server key is returned so a test can verify the license signature.
    /// </summary>
    internal static (Credential Credential, ECDsa ServerKey, string Json) Credential(
        string handle = "whiskers_prime",
        DateTimeOffset? issuedAt = null,
        TimeSpan? lifetime = null)
    {
        using ECDsa clientKey = NewKey();
        ECDsa serverKey = NewKey();
        string license = License(serverKey, Jwk.Thumbprint(clientKey), handle, issuedAt: issuedAt, lifetime: lifetime);
        string json = CredentialJson(clientKey, license, handle);
        CredentialLoadResult result = MeowSci.Catlog.Lib.Auth.Credential.Parse(json);
        if (!result.Ok)
            throw new InvalidOperationException($"test credential fixture is broken: {result.Error}");
        return (result.Credential!, serverKey, json);
    }

    /// <summary>Writes a credential file into <paramref name="directory"/> and returns its path.</summary>
    internal static string WriteCredential(string directory, string json)
    {
        string path = Path.Combine(directory, "catlog-credential.json");
        File.WriteAllText(path, json);
        return path;
    }
}
