using System;
using System.Buffers.Binary;
using System.Collections.Generic;
using System.IO;
using System.Security.Cryptography;
using System.Text;
using MeowSci.Catlog.Lib.Telemetry;

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
        => Hash16($"catlog-kitten:{installId}:{rosterName}");

    /// <summary>Length in characters of a <c>kid</c> or a <c>career</c> id.</summary>
    public const int Hash16Length = 16;

    /// <summary>
    /// The §4.1 career id: lowercase Crockford base32 of the first 10 bytes of
    /// <c>SHA-256("catlog-career:" + install_id + ":" + save_key)</c>, 16 characters.
    /// </summary>
    /// <param name="installId">The install ULID (stable per installation).</param>
    /// <param name="saveKey">
    /// What identifies the save this career lives in. The shipped mod uses <c>"save:" + save name</c>
    /// for a named KSA save and <c>"new:" + a fresh ULID</c> for a game that has not been saved yet
    /// (<c>docs/events.md</c>); nothing here depends on the shape.
    /// </param>
    /// <returns>The 16-character career id.</returns>
    /// <remarks>
    /// Salted with the install id for the same reason <see cref="KittenId"/> is: the server never
    /// learns a save's name, and two players who both call a save <c>"apollo"</c> do not collide.
    /// </remarks>
    public static string CareerId(string installId, string saveKey)
        => Hash16($"catlog-career:{installId}:{saveKey}");

    /// <summary>
    /// The shared content id for a celestial-system survey. Unlike career and kitten ids this has
    /// no install salt: every player with byte-for-byte equivalent content must produce the same
    /// id. Raw strings are strict UTF-8 length-prefixed values, bodies are raw-id ordinal sorted,
    /// and all numbers have a culture-independent binary encoding.
    /// </summary>
    public static string SystemId(SystemHashInput input)
    {
        ArgumentNullException.ThrowIfNull(input);

        using var bytes = new MemoryStream();
        bytes.Write("catlog-system-v1"u8);
        WriteString(bytes, input.SystemId);
        WriteString(bytes, input.DisplayName);
        WriteString(bytes, input.HomeBodyId);
        WriteInt32(bytes, input.BodyCount);

        var bodies = new List<SystemBodyHashInput>(input.Bodies);
        bodies.Sort(static (left, right) => string.CompareOrdinal(left.Id, right.Id));
        foreach (SystemBodyHashInput body in bodies)
        {
            WriteString(bytes, body.Id);
            WriteOptionalString(bytes, body.ParentId);
            WriteString(bytes, body.Class);
            WriteString(bytes, body.Kind);
            WriteInt32(bytes, body.Rank);
            WriteDouble(bytes, body.RadiusM);
            WriteDouble(bytes, body.MassKg);
            WriteDouble(bytes, body.SoiM);
            WriteDouble(bytes, body.AtmoM);
            WriteDouble(bytes, body.OceanM);
            WriteDouble(bytes, body.AngularVelocityRadS);
            WriteDouble(bytes, body.AxisCce.X);
            WriteDouble(bytes, body.AxisCce.Y);
            WriteDouble(bytes, body.AxisCce.Z);
            WriteDouble(bytes, body.CcfToCceT0.X);
            WriteDouble(bytes, body.CcfToCceT0.Y);
            WriteDouble(bytes, body.CcfToCceT0.Z);
            WriteDouble(bytes, body.CcfToCceT0.W);

            bytes.WriteByte(body.Orbit is null ? (byte)0 : (byte)1);
            if (body.Orbit is { } orbit)
            {
                WriteDouble(bytes, orbit.SemiMajorAxisM);
                WriteDouble(bytes, orbit.Eccentricity);
                WriteDouble(bytes, orbit.InclinationDeg);
                WriteDouble(bytes, orbit.LongitudeAscendingNodeDeg);
                WriteDouble(bytes, orbit.ArgumentPeriapsisDeg);
                WriteDouble(bytes, orbit.TimeAtPeriapsis);
            }

            bytes.WriteByte(body.PeriodS.HasValue ? (byte)1 : (byte)0);
            if (body.PeriodS is double period)
                WriteDouble(bytes, period);
        }

        byte[] digest = SHA256.HashData(bytes.GetBuffer().AsSpan(0, checked((int)bytes.Length)));
        return Crockford16(digest);
    }

    /// <summary>True when <paramref name="value"/> is a well-formed 16-character Crockford id.</summary>
    /// <param name="value">Candidate text.</param>
    /// <returns>True when the text is 16 lowercase Crockford base32 characters.</returns>
    public static bool IsHash16(string? value)
    {
        if (value is null || value.Length != Hash16Length)
            return false;
        foreach (char c in value)
        {
            if (Crockford.IndexOf(c) < 0)
                return false;
        }

        return true;
    }

    /// <summary>
    /// Lowercase Crockford base32 of the first 10 bytes of the UTF-8 SHA-256 of
    /// <paramref name="material"/> — 80 bits, exactly 16 characters, no padding.
    /// </summary>
    private static string Hash16(string material)
    {
        byte[] digest = SHA256.HashData(Encoding.UTF8.GetBytes(material));

        return Crockford16(digest);
    }

    private static string Crockford16(ReadOnlySpan<byte> digest)
    {
        Span<char> chars = stackalloc char[Hash16Length];
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

    private static void WriteString(Stream stream, string value)
    {
        ArgumentNullException.ThrowIfNull(value);
        byte[] encoded = new UTF8Encoding(false, true).GetBytes(value);
        Span<byte> length = stackalloc byte[4];
        BinaryPrimitives.WriteUInt32BigEndian(length, checked((uint)encoded.Length));
        stream.Write(length);
        stream.Write(encoded);
    }

    private static void WriteOptionalString(Stream stream, string? value)
    {
        stream.WriteByte(value is null ? (byte)0 : (byte)1);
        if (value is not null)
            WriteString(stream, value);
    }

    private static void WriteInt32(Stream stream, int value)
    {
        Span<byte> encoded = stackalloc byte[4];
        BinaryPrimitives.WriteInt32BigEndian(encoded, value);
        stream.Write(encoded);
    }

    private static void WriteDouble(Stream stream, double value)
    {
        if (double.IsNaN(value))
        {
            stream.WriteByte(3);
            return;
        }
        if (double.IsPositiveInfinity(value))
        {
            stream.WriteByte(1);
            return;
        }
        if (double.IsNegativeInfinity(value))
        {
            stream.WriteByte(2);
            return;
        }

        stream.WriteByte(0);
        long bits = BitConverter.DoubleToInt64Bits(value == 0.0 ? 0.0 : value);
        Span<byte> encoded = stackalloc byte[8];
        BinaryPrimitives.WriteInt64BigEndian(encoded, bits);
        stream.Write(encoded);
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

    /// <summary>Sanitizes a system display name to its printable 64-character wire form.</summary>
    public static string SanitizeSystemName(string? name) => SanitizeAscii(name, 64, "system");

    /// <summary>Sanitizes a system or body raw id for display without changing its join identity.</summary>
    public static string SanitizeCatalogueName(string? name) => SanitizeAscii(name, 64, "unknown");

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
