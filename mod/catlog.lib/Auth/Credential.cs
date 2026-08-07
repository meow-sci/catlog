using System;
using System.IO;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Auth;

/// <summary>The on-disk shape of <c>catlog-credential.json</c> (§4.6).</summary>
/// <param name="Format">Format version; must be <see cref="Wire.CredentialFormat"/>.</param>
/// <param name="Handle">The player handle the license was issued for.</param>
/// <param name="License">The compact license JWS.</param>
/// <param name="PrivateKeyPem">A PKCS#8 EC P-256 private key in PEM form. Never leaves the machine.</param>
public sealed record CredentialFile(
    [property: JsonPropertyName("format")] int Format,
    [property: JsonPropertyName("handle")] string Handle,
    [property: JsonPropertyName("license")] string License,
    [property: JsonPropertyName("private_key_pem")] string PrivateKeyPem);

/// <summary>The claims of a license JWS (§4.5.1), decoded but <b>not</b> verified.</summary>
/// <param name="Issuer"><c>iss</c> — the server base URL.</param>
/// <param name="Subject"><c>sub</c> — base64url of the 32-byte user key.</param>
/// <param name="Handle"><c>handle</c>.</param>
/// <param name="Jkt"><c>cnf.jkt</c> — the RFC 7638 thumbprint the license is bound to.</param>
/// <param name="IssuedAt"><c>iat</c>, unix seconds.</param>
/// <param name="ExpiresAt"><c>exp</c>, unix seconds.</param>
/// <param name="JwtId"><c>jti</c>.</param>
/// <param name="Version"><c>ver</c>.</param>
public sealed record LicenseClaims(
    string Issuer,
    string Subject,
    string Handle,
    string Jkt,
    long IssuedAt,
    long ExpiresAt,
    string JwtId,
    int Version)
{
    /// <summary>The expiry instant.</summary>
    public DateTimeOffset Expiry => DateTimeOffset.FromUnixTimeSeconds(ExpiresAt);

    /// <summary>True when the license has expired at <paramref name="now"/>.</summary>
    /// <param name="now">The instant to test against.</param>
    /// <returns>True when expired.</returns>
    public bool IsExpired(DateTimeOffset now) => now.ToUnixTimeSeconds() >= ExpiresAt;
}

/// <summary>The result of loading a credential: either a usable credential or a reason it is not.</summary>
/// <param name="Credential">The loaded credential, or null.</param>
/// <param name="Error">Why loading failed, or an empty string.</param>
public sealed record CredentialLoadResult(Credential? Credential, string Error)
{
    /// <summary>True when a usable credential was produced.</summary>
    public bool Ok => Credential is not null;
}

/// <summary>
/// A loaded credential: the player's signing key plus the license that binds it.
/// </summary>
/// <remarks>
/// <para>
/// Loading <b>never throws</b> — a missing, malformed, or mismatched credential must disable the
/// shipper with one ERROR log, not crash the game (§7.2 dead-latch).
/// </para>
/// <para>
/// The load path refuses to produce a credential when the key's RFC 7638 thumbprint does not equal
/// the license's <c>cnf.jkt</c>, exactly as §4.6 requires. Shipping with a mismatch would mean
/// every batch coming back <c>401 proof_invalid</c>; failing at load turns that into one clear
/// message.
/// </para>
/// </remarks>
public sealed class Credential : IDisposable
{
    private Credential(string handle, string license, ECDsa key, LicenseClaims claims, string jkt)
    {
        Handle = handle;
        License = license;
        Key = key;
        Claims = claims;
        Jkt = jkt;
        PublicJwkJson = Jwk.PublicJwkJson(key);
    }

    /// <summary>The player handle.</summary>
    public string Handle { get; }

    /// <summary>The compact license JWS, sent verbatim in <c>X-Catlog-License</c>.</summary>
    public string License { get; }

    /// <summary>The P-256 private key used to sign per-batch proofs.</summary>
    public ECDsa Key { get; }

    /// <summary>The license claims (decoded, unverified).</summary>
    public LicenseClaims Claims { get; }

    /// <summary>The RFC 7638 thumbprint of <see cref="Key"/>; equal to <c>Claims.Jkt</c> by construction.</summary>
    public string Jkt { get; }

    /// <summary>The canonical public JWK embedded in every proof header.</summary>
    public string PublicJwkJson { get; }

    /// <summary>Loads a credential file from disk.</summary>
    /// <param name="path">Path to <c>catlog-credential.json</c>.</param>
    /// <returns>The credential, or the reason it could not be loaded.</returns>
    public static CredentialLoadResult Load(string path)
    {
        if (string.IsNullOrWhiteSpace(path))
            return new CredentialLoadResult(null, "no credential path is configured");

        string json;
        try
        {
            if (!File.Exists(path))
                return new CredentialLoadResult(null, $"credential file '{path}' does not exist");
            json = File.ReadAllText(path);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or NotSupportedException)
        {
            return new CredentialLoadResult(null, $"credential file '{path}' could not be read: {ex.Message}");
        }

        return Parse(json);
    }

    /// <summary>Parses a credential from JSON text.</summary>
    /// <param name="json">The credential JSON.</param>
    /// <returns>The credential, or the reason it could not be parsed.</returns>
    public static CredentialLoadResult Parse(string json)
    {
        CredentialFile? file;
        try
        {
            file = JsonSerializer.Deserialize<CredentialFile>(json, CatlogJson.Options);
        }
        catch (JsonException ex)
        {
            return new CredentialLoadResult(null, $"credential is not valid JSON: {ex.Message}");
        }

        if (file is null)
            return new CredentialLoadResult(null, "credential is empty");
        if (file.Format != Wire.CredentialFormat)
            return new CredentialLoadResult(null,
                $"credential format {file.Format} is not supported (expected {Wire.CredentialFormat})");
        if (string.IsNullOrWhiteSpace(file.License))
            return new CredentialLoadResult(null, "credential has no license");
        if (string.IsNullOrWhiteSpace(file.PrivateKeyPem))
            return new CredentialLoadResult(null, "credential has no private key");

        if (!TryReadLicenseClaims(file.License, out LicenseClaims? claims, out string claimsError))
            return new CredentialLoadResult(null, claimsError);

        ECDsa key;
        try
        {
            key = ECDsa.Create();
            key.ImportFromPem(file.PrivateKeyPem);
        }
        catch (Exception ex) when (ex is ArgumentException or CryptographicException)
        {
            return new CredentialLoadResult(null, $"credential private key could not be read: {ex.Message}");
        }

        string jkt;
        try
        {
            jkt = Jwk.Thumbprint(key);
        }
        catch (CryptographicException ex)
        {
            key.Dispose();
            return new CredentialLoadResult(null, $"credential private key is not a P-256 key: {ex.Message}");
        }

        if (!string.Equals(jkt, claims!.Jkt, StringComparison.Ordinal))
        {
            key.Dispose();
            return new CredentialLoadResult(null,
                $"credential key thumbprint '{jkt}' does not match the license cnf.jkt '{claims.Jkt}' — "
                + "this credential cannot ship; re-download it from the dashboard");
        }

        string handle = string.IsNullOrWhiteSpace(file.Handle) ? claims.Handle : file.Handle;
        return new CredentialLoadResult(new Credential(handle, file.License.Trim(), key, claims, jkt), string.Empty);
    }

    /// <summary>Decodes a license JWS's claims without verifying its signature (§4.6).</summary>
    /// <param name="licenseJws">The compact license JWS.</param>
    /// <param name="claims">The decoded claims, or null.</param>
    /// <param name="error">Why decoding failed, or an empty string.</param>
    /// <returns>True when the claims were decoded.</returns>
    public static bool TryReadLicenseClaims(string licenseJws, out LicenseClaims? claims, out string error)
    {
        claims = null;

        using JsonDocument? payload = Jws.DecodePayloadUnverified(licenseJws);
        if (payload is null)
        {
            error = "license is not a well-formed compact JWS";
            return false;
        }

        JsonElement root = payload.RootElement;
        if (root.ValueKind != JsonValueKind.Object)
        {
            error = "license claims are not a JSON object";
            return false;
        }

        if (!root.TryGetProperty("cnf", out JsonElement cnf)
            || cnf.ValueKind != JsonValueKind.Object
            || !cnf.TryGetProperty("jkt", out JsonElement jktElement)
            || jktElement.ValueKind != JsonValueKind.String)
        {
            error = "license has no cnf.jkt";
            return false;
        }

        claims = new LicenseClaims(
            Issuer: String(root, "iss"),
            Subject: String(root, "sub"),
            Handle: String(root, "handle"),
            Jkt: jktElement.GetString() ?? string.Empty,
            IssuedAt: Int64(root, "iat"),
            ExpiresAt: Int64(root, "exp"),
            JwtId: String(root, "jti"),
            Version: (int)Int64(root, "ver"));
        error = string.Empty;
        return true;
    }

    /// <summary>Disposes the signing key.</summary>
    public void Dispose() => Key.Dispose();

    private static string String(JsonElement root, string name)
        => root.TryGetProperty(name, out JsonElement value) && value.ValueKind == JsonValueKind.String
            ? value.GetString() ?? string.Empty
            : string.Empty;

    private static long Int64(JsonElement root, string name)
        => root.TryGetProperty(name, out JsonElement value)
           && value.ValueKind == JsonValueKind.Number
           && value.TryGetInt64(out long parsed)
            ? parsed
            : 0;
}
