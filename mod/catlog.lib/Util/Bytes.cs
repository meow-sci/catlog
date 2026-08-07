using System;
using System.Buffers.Text;
using System.Security.Cryptography;
using System.Text;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// base64url (unpadded, RFC 7515 §2) and SHA-256 helpers. Everything on the wire that is not
/// JSON is one of these two shapes: JWS segments, the proof's <c>bh</c>/<c>ph</c>, the
/// RFC 7638 thumbprint, the license <c>sub</c>.
/// </summary>
/// <remarks>
/// <see cref="Base64Url"/> is in the BCL as of .NET 9 — there is no hand-rolled
/// <c>Replace('+', '-')</c> anywhere in catlog.
/// </remarks>
public static class Bytes
{
    /// <summary>Encodes bytes as unpadded base64url.</summary>
    /// <param name="value">The bytes to encode.</param>
    /// <returns>The base64url text.</returns>
    public static string ToBase64Url(ReadOnlySpan<byte> value) => Base64Url.EncodeToString(value);

    /// <summary>Encodes UTF-8 text as unpadded base64url.</summary>
    /// <param name="value">The text to encode.</param>
    /// <returns>The base64url text.</returns>
    public static string Utf8ToBase64Url(string value)
        => Base64Url.EncodeToString(Encoding.UTF8.GetBytes(value));

    /// <summary>Decodes unpadded base64url.</summary>
    /// <param name="value">The base64url text.</param>
    /// <returns>The decoded bytes.</returns>
    /// <exception cref="FormatException">The input is not valid base64url.</exception>
    public static byte[] FromBase64Url(string value) => Base64Url.DecodeFromChars(value);

    /// <summary>Decodes unpadded base64url, returning false instead of throwing.</summary>
    /// <param name="value">The base64url text.</param>
    /// <param name="decoded">The decoded bytes, or an empty array on failure.</param>
    /// <returns>True when <paramref name="value"/> decoded.</returns>
    public static bool TryFromBase64Url(string? value, out byte[] decoded)
    {
        if (value is null)
        {
            decoded = [];
            return false;
        }

        try
        {
            decoded = Base64Url.DecodeFromChars(value);
            return true;
        }
        catch (FormatException)
        {
            decoded = [];
            return false;
        }
    }

    /// <summary>SHA-256 of the given bytes.</summary>
    /// <param name="value">The bytes to hash.</param>
    /// <returns>The 32-byte digest.</returns>
    public static byte[] Sha256(ReadOnlySpan<byte> value) => SHA256.HashData(value);

    /// <summary>SHA-256 of the given bytes, base64url-encoded — the <c>bh</c>/<c>ph</c>/<c>jkt</c> shape.</summary>
    /// <param name="value">The bytes to hash.</param>
    /// <returns>The base64url digest.</returns>
    public static string Sha256Base64Url(ReadOnlySpan<byte> value)
        => Base64Url.EncodeToString(SHA256.HashData(value));

    /// <summary>SHA-256 of the UTF-8 encoding of <paramref name="value"/>, base64url-encoded.</summary>
    /// <param name="value">The text to hash.</param>
    /// <returns>The base64url digest.</returns>
    public static string Sha256Base64Url(string value)
        => Sha256Base64Url(Encoding.UTF8.GetBytes(value));

    /// <summary>Constant-time comparison of two byte sequences.</summary>
    /// <param name="left">First sequence.</param>
    /// <param name="right">Second sequence.</param>
    /// <returns>True when the sequences are identical.</returns>
    public static bool FixedTimeEquals(ReadOnlySpan<byte> left, ReadOnlySpan<byte> right)
        => CryptographicOperations.FixedTimeEquals(left, right);
}
