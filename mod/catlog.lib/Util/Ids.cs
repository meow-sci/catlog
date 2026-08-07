using System;
using System.Security.Cryptography;
using System.Text;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// Client-minted identifiers: ULIDs for events/flights/sessions/streams (D19), and the
/// pseudonymous kitten id from §4.2.
/// </summary>
public static class Ids
{
    /// <summary>Crockford base32, lowercased. Used for <c>kid</c> (§4.2).</summary>
    private const string Crockford = "0123456789abcdefghjkmnpqrstvwxyz";

    /// <summary>Length in characters of the canonical ULID text form.</summary>
    public const int UlidLength = 26;

    /// <summary>Mints a new ULID as its canonical 26-character text form.</summary>
    /// <returns>The new ULID.</returns>
    public static string NewUlid() => Ulid.NewUlid().ToString();

    /// <summary>Mints a new ULID whose timestamp component is <paramref name="when"/>.</summary>
    /// <param name="when">The timestamp to embed.</param>
    /// <returns>The new ULID.</returns>
    public static string NewUlid(DateTimeOffset when) => Ulid.NewUlid(when).ToString();

    /// <summary>True when <paramref name="value"/> parses as a ULID.</summary>
    /// <param name="value">Candidate text.</param>
    /// <returns>True when the text is a well-formed ULID.</returns>
    public static bool IsUlid(string? value) => value is not null && Ulid.TryParse(value, out _);

    /// <summary>
    /// The §4.2 kitten id: lowercase Crockford base32 of the first 10 bytes of
    /// <c>SHA-256("catlog-kitten:" + install_id + ":" + roster_name)</c>, 16 characters.
    /// </summary>
    /// <param name="installId">The install ULID (stable per installation).</param>
    /// <param name="rosterName">The kitten's roster display name.</param>
    /// <returns>The 16-character kitten id.</returns>
    /// <remarks>
    /// Salting with the install id means the same kitten name on two installs produces two ids —
    /// the server never learns that two players named a kitten the same thing.
    /// </remarks>
    public static string KittenId(string installId, string rosterName)
    {
        byte[] digest = SHA256.HashData(
            Encoding.UTF8.GetBytes($"catlog-kitten:{installId}:{rosterName}"));

        // 10 bytes = 80 bits = exactly 16 base32 characters, no padding.
        Span<char> chars = stackalloc char[16];
        ulong acc = 0;
        int bits = 0;
        int outIndex = 0;
        for (int i = 0; i < 10; i++)
        {
            acc = (acc << 8) | digest[i];
            bits += 8;
            while (bits >= 5)
            {
                bits -= 5;
                chars[outIndex++] = Crockford[(int)((acc >> bits) & 0x1F)];
            }
        }

        return new string(chars);
    }

    /// <summary>
    /// Sanitizes a roster display name to printable US-ASCII, at most 32 characters (§4.2 —
    /// this is a moderation surface, so nothing exotic reaches the server).
    /// </summary>
    /// <param name="name">The raw display name.</param>
    /// <returns>The sanitized name; empty input yields <c>"kitten"</c>.</returns>
    public static string SanitizeName(string? name) => SanitizeAscii(name, 32, "kitten");

    /// <summary>
    /// Sanitizes a vehicle name to printable US-ASCII, at most 64 characters (§4.2
    /// <c>flight.started.vehicle_name</c>).
    /// </summary>
    /// <param name="name">The raw vehicle name.</param>
    /// <returns>The sanitized name; empty input yields <c>"vehicle"</c>.</returns>
    public static string SanitizeVehicleName(string? name) => SanitizeAscii(name, 64, "vehicle");

    private static string SanitizeAscii(string? value, int maxLength, string fallback)
    {
        if (string.IsNullOrEmpty(value))
            return fallback;

        var sb = new StringBuilder(Math.Min(value.Length, maxLength));
        foreach (char c in value)
        {
            if (sb.Length == maxLength)
                break;
            // Printable US-ASCII only: space (0x20) through tilde (0x7E).
            if (c is >= ' ' and <= '~')
                sb.Append(c);
        }

        string result = sb.ToString().Trim();
        return result.Length == 0 ? fallback : result;
    }
}
