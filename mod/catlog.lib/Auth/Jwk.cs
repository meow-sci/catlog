using System;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Auth;

/// <summary>
/// EC public JWK export and RFC 7638 thumbprints, BCL only.
/// </summary>
/// <remarks>
/// The thumbprint (<c>jkt</c>) is the hinge of the whole proof-of-possession scheme (§4.5): the
/// license binds a <c>cnf.jkt</c>, the proof embeds its public JWK, and the server checks that the
/// thumbprint of the embedded key equals the bound one. Get the canonicalization wrong and every
/// batch is rejected with <c>proof_invalid</c>, so it is spelled out here rather than assembled by
/// a serializer: <b>required members only, lexicographic order, no whitespace, UTF-8</b> — for an
/// EC key that is exactly <c>{"crv":…,"kty":"EC","x":…,"y":…}</c>.
/// </remarks>
public static class Jwk
{
    /// <summary>Coordinate size in bytes for P-256.</summary>
    private const int P256CoordinateBytes = 32;

    /// <summary>
    /// The canonical public JWK for a P-256 key: the four required members, lexicographically
    /// ordered, no whitespace. This exact string is what gets hashed for the thumbprint, and it is
    /// also what goes in the proof JWS header's <c>jwk</c> member (§4.5.2 — <c>kty,crv,x,y</c> only).
    /// </summary>
    /// <param name="key">The key; only the public part is read.</param>
    /// <returns>The canonical JWK JSON.</returns>
    /// <exception cref="CryptographicException">The key is not on P-256.</exception>
    public static string PublicJwkJson(ECDsa key)
    {
        ECParameters parameters = key.ExportParameters(includePrivateParameters: false);
        string x = Coordinate(parameters.Q.X, nameof(parameters.Q.X));
        string y = Coordinate(parameters.Q.Y, nameof(parameters.Q.Y));
        return $"{{\"crv\":\"{Wire.Crv}\",\"kty\":\"{Wire.Kty}\",\"x\":\"{x}\",\"y\":\"{y}\"}}";
    }

    /// <summary>The RFC 7638 SHA-256 thumbprint of a P-256 key.</summary>
    /// <param name="key">The key; only the public part is read.</param>
    /// <returns>The base64url thumbprint.</returns>
    public static string Thumbprint(ECDsa key) => ThumbprintOfCanonicalJson(PublicJwkJson(key));

    /// <summary>
    /// The RFC 7638 thumbprint of an already-canonical JWK JSON string.
    /// </summary>
    /// <param name="canonicalJwkJson">
    /// The JWK's required members only, lexicographically ordered, with no whitespace. For EC that
    /// is <c>crv,kty,x,y</c>; for RSA it is <c>e,kty,n</c>; for oct it is <c>k,kty</c>.
    /// </param>
    /// <returns>The base64url thumbprint.</returns>
    /// <remarks>
    /// Public so the conformance suite can feed it the RFC 7638 §3.1 published RSA vector — the
    /// only officially published thumbprint test vector there is, and the one thing that proves
    /// the hash-and-encode half of the algorithm independently of catlog's own EC serializer.
    /// </remarks>
    public static string ThumbprintOfCanonicalJson(string canonicalJwkJson)
        => Bytes.Sha256Base64Url(Encoding.UTF8.GetBytes(canonicalJwkJson));

    /// <summary>
    /// The RFC 7638 thumbprint of an EC JWK given in any member order, with any extra members.
    /// The required members are extracted and re-canonicalized before hashing.
    /// </summary>
    /// <param name="jwkJson">The JWK JSON.</param>
    /// <param name="thumbprint">The base64url thumbprint, or an empty string on failure.</param>
    /// <param name="error">Why extraction failed, or an empty string.</param>
    /// <returns>True when a thumbprint was computed.</returns>
    public static bool TryThumbprintOfEcJwk(string jwkJson, out string thumbprint, out string error)
    {
        thumbprint = string.Empty;

        if (!TryReadEcJwk(jwkJson, out string crv, out string x, out string y, out error))
            return false;

        thumbprint = ThumbprintOfCanonicalJson($"{{\"crv\":\"{crv}\",\"kty\":\"EC\",\"x\":\"{x}\",\"y\":\"{y}\"}}");
        return true;
    }

    /// <summary>Imports a P-256 public JWK.</summary>
    /// <param name="jwkJson">The JWK JSON.</param>
    /// <param name="key">The imported key, or null on failure.</param>
    /// <param name="error">Why the import failed, or an empty string.</param>
    /// <returns>True when the key was imported.</returns>
    public static bool TryImportPublicJwk(string jwkJson, out ECDsa? key, out string error)
    {
        key = null;

        if (!TryReadEcJwk(jwkJson, out string crv, out string x, out string y, out error))
            return false;

        if (!string.Equals(crv, Wire.Crv, StringComparison.Ordinal))
        {
            error = $"unsupported curve '{crv}'";
            return false;
        }

        if (!Bytes.TryFromBase64Url(x, out byte[] xBytes) || !Bytes.TryFromBase64Url(y, out byte[] yBytes))
        {
            error = "x or y is not valid base64url";
            return false;
        }

        if (xBytes.Length != P256CoordinateBytes || yBytes.Length != P256CoordinateBytes)
        {
            error = $"P-256 coordinates are {P256CoordinateBytes} bytes";
            return false;
        }

        try
        {
            key = ECDsa.Create(new ECParameters
            {
                Curve = ECCurve.NamedCurves.nistP256,
                Q = new ECPoint { X = xBytes, Y = yBytes },
            });
            error = string.Empty;
            return true;
        }
        catch (CryptographicException ex)
        {
            error = ex.Message;
            return false;
        }
    }

    /// <summary>Reads the four EC members from a JWK, whatever order they appear in.</summary>
    /// <param name="jwkJson">The JWK JSON.</param>
    /// <param name="crv">The curve name.</param>
    /// <param name="x">The base64url x coordinate.</param>
    /// <param name="y">The base64url y coordinate.</param>
    /// <param name="error">Why extraction failed, or an empty string.</param>
    /// <returns>True when all four members were present and of the right shape.</returns>
    public static bool TryReadEcJwk(string jwkJson, out string crv, out string x, out string y, out string error)
    {
        crv = x = y = string.Empty;

        JsonDocument document;
        try
        {
            document = JsonDocument.Parse(jwkJson);
        }
        catch (JsonException ex)
        {
            error = $"JWK is not valid JSON: {ex.Message}";
            return false;
        }

        using (document)
        {
            JsonElement root = document.RootElement;
            if (root.ValueKind != JsonValueKind.Object)
            {
                error = "JWK is not a JSON object";
                return false;
            }

            if (!TryGetString(root, "kty", out string kty) || !string.Equals(kty, Wire.Kty, StringComparison.Ordinal))
            {
                error = "JWK kty must be EC";
                return false;
            }

            if (!TryGetString(root, "crv", out crv)
                || !TryGetString(root, "x", out x)
                || !TryGetString(root, "y", out y))
            {
                error = "JWK is missing crv, x or y";
                return false;
            }
        }

        error = string.Empty;
        return true;
    }

    private static bool TryGetString(JsonElement element, string name, out string value)
    {
        if (element.TryGetProperty(name, out JsonElement property) && property.ValueKind == JsonValueKind.String)
        {
            value = property.GetString() ?? string.Empty;
            return value.Length > 0;
        }

        value = string.Empty;
        return false;
    }

    private static string Coordinate(byte[]? coordinate, string name)
    {
        if (coordinate is null)
            throw new CryptographicException($"The key has no {name} coordinate.");

        if (coordinate.Length == P256CoordinateBytes)
            return Bytes.ToBase64Url(coordinate);

        // RFC 7518 §6.2.1.2: the coordinate is fixed-width for the curve, left-padded with zeroes.
        // .NET's named-curve export is already 32 bytes, but a key that came from somewhere else
        // may have had leading zeroes trimmed.
        if (coordinate.Length > P256CoordinateBytes)
            throw new CryptographicException($"The {name} coordinate is not a P-256 coordinate.");

        Span<byte> padded = stackalloc byte[P256CoordinateBytes];
        coordinate.CopyTo(padded[(P256CoordinateBytes - coordinate.Length)..]);
        return Bytes.ToBase64Url(padded);
    }
}
