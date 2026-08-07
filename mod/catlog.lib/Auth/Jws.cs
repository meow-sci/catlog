using System;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Auth;

/// <summary>The three base64url segments of a compact JWS, with the first two decoded.</summary>
/// <param name="ProtectedHeader">The encoded protected header segment.</param>
/// <param name="Payload">The encoded payload segment.</param>
/// <param name="Signature">The encoded signature segment.</param>
/// <param name="HeaderJson">The decoded header JSON.</param>
/// <param name="PayloadJson">The decoded payload JSON.</param>
public sealed record JwsParts(
    string ProtectedHeader,
    string Payload,
    string Signature,
    string HeaderJson,
    string PayloadJson)
{
    /// <summary>The bytes covered by the signature: <c>base64url(header) || '.' || base64url(payload)</c>.</summary>
    /// <returns>The ASCII signing input.</returns>
    public byte[] SigningInput() => Encoding.ASCII.GetBytes($"{ProtectedHeader}.{Payload}");
}

/// <summary>
/// Compact JWS with ES256, BCL only — no NuGet JOSE library (D5/D21: the server uses go-jose, the
/// mod implements this by hand).
/// </summary>
/// <remarks>
/// <para>
/// <b>No DER conversion anywhere.</b> .NET's <c>ECDsa.SignData(byte[], HashAlgorithmName)</c>
/// already emits the IEEE P-1363 <c>r‖s</c> fixed-width form that JWS requires; the DER-encoded
/// form is only produced by the overloads that take a <c>DSASignatureFormat</c>. For P-256 that
/// makes every signature exactly 64 bytes.
/// </para>
/// <para>
/// The algorithm allow-list is a single value. Anything else — <c>none</c>, RSA, a different curve
/// — is rejected before any cryptography happens.
/// </para>
/// </remarks>
public static class Jws
{
    /// <summary>Length in bytes of an ES256 signature (r‖s, 32 bytes each).</summary>
    public const int Es256SignatureBytes = 64;

    /// <summary>Signs a header/payload pair, producing a compact JWS.</summary>
    /// <param name="headerJson">The protected header JSON. Must declare <c>"alg":"ES256"</c>.</param>
    /// <param name="payloadJson">The claims JSON.</param>
    /// <param name="key">A P-256 private key.</param>
    /// <returns>The compact serialization.</returns>
    /// <exception cref="ArgumentException">The header does not declare ES256.</exception>
    public static string Sign(string headerJson, string payloadJson, ECDsa key)
    {
        if (!DeclaresEs256(headerJson))
            throw new ArgumentException("The protected header must declare \"alg\":\"ES256\".", nameof(headerJson));

        string encodedHeader = Bytes.Utf8ToBase64Url(headerJson);
        string encodedPayload = Bytes.Utf8ToBase64Url(payloadJson);
        byte[] signingInput = Encoding.ASCII.GetBytes($"{encodedHeader}.{encodedPayload}");
        byte[] signature = key.SignData(signingInput, HashAlgorithmName.SHA256);
        return $"{encodedHeader}.{encodedPayload}.{Bytes.ToBase64Url(signature)}";
    }

    /// <summary>Splits and decodes a compact JWS without verifying it.</summary>
    /// <param name="compact">The compact serialization.</param>
    /// <param name="parts">The decoded parts, or null on failure.</param>
    /// <param name="error">Why parsing failed, or an empty string.</param>
    /// <returns>True when the input parsed.</returns>
    public static bool TryParse(string? compact, out JwsParts? parts, out string error)
    {
        parts = null;

        if (string.IsNullOrEmpty(compact))
        {
            error = "empty JWS";
            return false;
        }

        if (Encoding.UTF8.GetByteCount(compact) > Wire.MaxJwsBytes)
        {
            error = $"JWS exceeds {Wire.MaxJwsBytes} bytes";
            return false;
        }

        string[] segments = compact.Split('.');
        if (segments.Length != 3)
        {
            error = "a compact JWS has exactly three segments";
            return false;
        }

        if (!Bytes.TryFromBase64Url(segments[0], out byte[] headerBytes)
            || !Bytes.TryFromBase64Url(segments[1], out byte[] payloadBytes)
            || !Bytes.TryFromBase64Url(segments[2], out _))
        {
            error = "a JWS segment is not valid base64url";
            return false;
        }

        parts = new JwsParts(
            segments[0],
            segments[1],
            segments[2],
            Encoding.UTF8.GetString(headerBytes),
            Encoding.UTF8.GetString(payloadBytes));
        error = string.Empty;
        return true;
    }

    /// <summary>Verifies a compact JWS against a public key.</summary>
    /// <param name="compact">The compact serialization.</param>
    /// <param name="publicKey">The P-256 public key.</param>
    /// <returns>True when the signature is valid and the header declares ES256.</returns>
    public static bool Verify(string? compact, ECDsa publicKey) => TryVerify(compact, publicKey, out _);

    /// <summary>Verifies a compact JWS, reporting why it failed.</summary>
    /// <param name="compact">The compact serialization.</param>
    /// <param name="publicKey">The P-256 public key.</param>
    /// <param name="error">Why verification failed, or an empty string.</param>
    /// <returns>True when the signature is valid.</returns>
    public static bool TryVerify(string? compact, ECDsa publicKey, out string error)
    {
        if (!TryParse(compact, out JwsParts? parts, out error))
            return false;

        if (!DeclaresEs256(parts!.HeaderJson))
        {
            error = "alg is not ES256";
            return false;
        }

        if (!Bytes.TryFromBase64Url(parts.Signature, out byte[] signature))
        {
            error = "signature is not valid base64url";
            return false;
        }

        if (signature.Length != Es256SignatureBytes)
        {
            error = $"an ES256 signature is {Es256SignatureBytes} bytes, got {signature.Length}";
            return false;
        }

        if (!publicKey.VerifyData(parts.SigningInput(), signature, HashAlgorithmName.SHA256))
        {
            error = "signature does not verify";
            return false;
        }

        error = string.Empty;
        return true;
    }

    /// <summary>Decodes the payload of a compact JWS <b>without</b> verifying it.</summary>
    /// <param name="compact">The compact serialization.</param>
    /// <returns>The claims as a <see cref="JsonDocument"/>, or null when the input does not parse.</returns>
    /// <remarks>
    /// Used to display the handle and expiry of a license before the mod has ever contacted the
    /// server (§4.6). Never treat the result as trusted.
    /// </remarks>
    public static JsonDocument? DecodePayloadUnverified(string? compact)
    {
        if (!TryParse(compact, out JwsParts? parts, out _))
            return null;

        try
        {
            return JsonDocument.Parse(parts!.PayloadJson);
        }
        catch (JsonException)
        {
            return null;
        }
    }

    /// <summary>Decodes the protected header of a compact JWS <b>without</b> verifying it.</summary>
    /// <param name="compact">The compact serialization.</param>
    /// <returns>The header as a <see cref="JsonDocument"/>, or null when the input does not parse.</returns>
    public static JsonDocument? DecodeHeaderUnverified(string? compact)
    {
        if (!TryParse(compact, out JwsParts? parts, out _))
            return null;

        try
        {
            return JsonDocument.Parse(parts!.HeaderJson);
        }
        catch (JsonException)
        {
            return null;
        }
    }

    private static bool DeclaresEs256(string headerJson)
    {
        try
        {
            using JsonDocument document = JsonDocument.Parse(headerJson);
            return document.RootElement.TryGetProperty("alg", out JsonElement alg)
                   && alg.ValueKind == JsonValueKind.String
                   && string.Equals(alg.GetString(), Wire.Alg, StringComparison.Ordinal);
        }
        catch (JsonException)
        {
            return false;
        }
    }
}
