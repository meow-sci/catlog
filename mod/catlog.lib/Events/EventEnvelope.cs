using System.Text.Json.Serialization;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Events;

/// <summary>
/// One event = one JSON object = one NDJSON line (§4.1). The single normative shape shared with
/// the Go server; the server rejects unknown envelope keys, so every property here is explicitly
/// named rather than relying on the naming policy — a C# rename must not silently change the wire.
/// </summary>
public sealed record EventEnvelope
{
    /// <summary>Client-minted ULID. The dedup key: <c>(player, event_id)</c> is unique server-side (D19).</summary>
    [JsonPropertyName("id")]
    public required string Id { get; init; }

    /// <summary>Namespaced lowercase type from <see cref="EventTypes"/>.</summary>
    [JsonPropertyName("type")]
    public required string Type { get; init; }

    /// <summary>Payload schema version, ≥1.</summary>
    [JsonPropertyName("ver")]
    public int Ver { get; init; } = Wire.EnvelopeVersion;

    /// <summary>
    /// Flight ULID, or null for session and roster events. Serialized as an explicit
    /// <c>"flight": null</c> — the key is always present.
    /// </summary>
    [JsonPropertyName("flight")]
    public string? Flight { get; init; }

    /// <summary>Session ULID. Never null.</summary>
    [JsonPropertyName("session")]
    public required string Session { get; init; }

    /// <summary>Universe sim seconds. May jump backwards across a save load.</summary>
    [JsonPropertyName("sim_t")]
    public double SimT { get; init; }

    /// <summary>Client unix milliseconds. Untrusted by the server.</summary>
    [JsonPropertyName("wall_t")]
    public long WallT { get; init; }

    /// <summary>
    /// The per-type payload object. Declared as <see cref="object"/> so
    /// <c>System.Text.Json</c> serializes the runtime type — one of the records in
    /// <c>Payloads.cs</c> on the way out, a <c>JsonElement</c> on the way back in.
    /// </summary>
    [JsonPropertyName("payload")]
    public required object Payload { get; init; }

    /// <summary>
    /// Serializes to exactly one NDJSON line, with no trailing newline. The shipper joins these
    /// with <c>\n</c>; the outbox stores this string verbatim so the bytes the server hashes are
    /// the bytes the detector produced.
    /// </summary>
    /// <returns>One line of NDJSON.</returns>
    public string ToNdjsonLine() => CatlogJson.Serialize(this);

    /// <summary>Convenience constructor used by <see cref="Detect.EventFactory"/> and tests.</summary>
    /// <param name="type">Event type name.</param>
    /// <param name="session">Session ULID.</param>
    /// <param name="flight">Flight ULID, or null.</param>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <param name="payload">The payload record.</param>
    /// <param name="id">Event ULID; a fresh one is minted when null.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope Create(
        string type,
        string session,
        string? flight,
        double simT,
        long wallMs,
        object payload,
        string? id = null) => new()
        {
            Id = id ?? Ids.NewUlid(),
            Type = type,
            Ver = EventTypes.VersionOf(type),
            Flight = flight,
            Session = session,
            SimT = simT,
            WallT = wallMs,
            Payload = payload,
        };
}
