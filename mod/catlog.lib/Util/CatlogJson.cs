using System.Text.Encodings.Web;
using System.Text.Json;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// The one <see cref="JsonSerializerOptions"/> instance in catlog. The NDJSON the outbox
/// stores, the NDJSON the shipper compresses and the JSON <c>catlog.sim</c> prints must be the
/// same bytes, or the proof's <c>bh</c> (§4.5.2) will not match what the server hashes.
/// </summary>
/// <remarks>
/// Pattern from <c>gatOS/gatOS.SimFs/SimJson.cs</c>. No source-generated context: gatOS and
/// unscience both use reflection-based STJ and the mod is neither trimmed nor AOT-compiled
/// (§7.2 marks source-gen optional).
/// <para>
/// Note the absence of <c>DefaultIgnoreCondition = WhenWritingNull</c>: §4.1 requires
/// <c>"flight": null</c> to be <em>present</em> on session/roster events, so omission is opted
/// into per-property with <c>[JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]</c>
/// exactly where the contract says a key may be absent (<c>telemetry.window.peak_g</c>,
/// <c>max_q_pa</c>, and the proof's <c>ph</c>).
/// </para>
/// </remarks>
public static class CatlogJson
{
    /// <summary>Shared serializer options: snake_case names, relaxed escaping, compact output.</summary>
    public static readonly JsonSerializerOptions Options = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
        WriteIndented = false,
    };

    /// <summary>Serializes a value with <see cref="Options"/>.</summary>
    /// <typeparam name="T">The value's declared type.</typeparam>
    /// <param name="value">The value.</param>
    /// <returns>The JSON text.</returns>
    public static string Serialize<T>(T value) => JsonSerializer.Serialize(value, Options);

    /// <summary>Serializes a value straight to UTF-8 bytes. Byte-identical to <see cref="Serialize{T}"/>.</summary>
    /// <typeparam name="T">The value's declared type.</typeparam>
    /// <param name="value">The value.</param>
    /// <returns>The UTF-8 JSON bytes.</returns>
    public static byte[] SerializeToUtf8Bytes<T>(T value)
        => JsonSerializer.SerializeToUtf8Bytes(value, Options);

    /// <summary>Deserializes with <see cref="Options"/>.</summary>
    /// <typeparam name="T">The target type.</typeparam>
    /// <param name="json">The JSON text.</param>
    /// <returns>The deserialized value, or null.</returns>
    public static T? Deserialize<T>(string json) => JsonSerializer.Deserialize<T>(json, Options);
}
