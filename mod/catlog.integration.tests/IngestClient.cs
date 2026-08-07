using System;
using System.Collections.Generic;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>Deliberate deviations from the happy path for one call to <see cref="IngestClient.ShipAsync"/>.</summary>
internal sealed class ShipOverrides
{
    /// <summary>Batch id to reuse, for the replay short-circuit.</summary>
    internal string? Jti { get; set; }

    /// <summary>Sequence number to send instead of the client's own.</summary>
    internal long? Seq { get; set; }

    /// <summary>Clock offset applied to the proof's <c>iat</c>.</summary>
    internal TimeSpan Skew { get; set; }

    /// <summary>Bytes to send instead of the ones the proof committed to.</summary>
    internal byte[]? TamperedBody { get; set; }

    /// <summary>False to leave the stream chain where it was, whatever the server answered.</summary>
    internal bool Advance { get; set; } = true;
}

/// <summary>One ingest response.</summary>
internal sealed record IngestResponse(
    int Status,
    string Error,
    int Accepted,
    int Deduped,
    bool Replay,
    string Body,
    DateTimeOffset? Date);

/// <summary>
/// A minimal ingest client built from <c>catlog.lib</c>'s own primitives, for the cases that need
/// control the <see cref="BatchShipper"/> deliberately does not give: reusing a batch id, sending
/// bytes the proof did not commit to, forcing a sequence number.
/// </summary>
/// <remarks>
/// Everything that could differ between the mod and the server is still the library's:
/// <see cref="BrotliCodec"/> for the body, <see cref="Bytes.Sha256Base64Url"/> for <c>bh</c>,
/// <see cref="ProofSigner"/> and <see cref="Jws"/> for the ES256 proof, and
/// <see cref="Credential.PublicJwkJson"/> for the embedded JWK the server thumbprints. Only the
/// retry policy is missing, because these tests are about the responses themselves.
/// </remarks>
internal sealed class IngestClient : IDisposable
{
    private readonly HttpClient _http = new() { Timeout = TimeSpan.FromSeconds(30) };
    private readonly Credential _credential;
    private readonly string _url;

    internal IngestClient(string ingestUrl, Credential credential)
    {
        _url = ingestUrl;
        _credential = credential;
        StreamId = Ids.NewUlid();
        Seq = 1;
    }

    /// <summary>The stream this client is on.</summary>
    internal string StreamId { get; set; }

    /// <summary>The sequence number the next batch carries.</summary>
    internal long Seq { get; set; }

    /// <summary>The previous accepted batch's <c>bh</c>, which becomes the next <c>ph</c>.</summary>
    internal string? PreviousBh { get; set; }

    /// <summary>The batch id of the last request, whatever the outcome.</summary>
    internal string LastJti { get; private set; } = string.Empty;

    internal async Task<IngestResponse> ShipAsync(byte[] ndjson, ShipOverrides? overrides = null)
    {
        ShipOverrides o = overrides ?? new ShipOverrides();

        byte[] signedBody = BrotliCodec.Compress(ndjson);
        byte[] sentBody = o.TamperedBody ?? signedBody;
        long seq = o.Seq ?? Seq;

        LastJti = o.Jti ?? Ids.NewUlid();
        var claims = new ProofClaims(
            Jti: LastJti,
            Iat: DateTimeOffset.UtcNow.Add(o.Skew).ToUnixTimeSeconds(),
            Htm: Wire.HttpMethod,
            Htu: _url,
            // Signed over the body the proof commits to — which is not necessarily the body sent.
            Bh: Bytes.Sha256Base64Url(signedBody),
            Sid: StreamId,
            Seq: seq,
            Ph: seq == 1 ? null : PreviousBh);

        using var request = new HttpRequestMessage(HttpMethod.Post, _url);
        var content = new ByteArrayContent(sentBody);
        content.Headers.ContentType = new MediaTypeHeaderValue(Wire.ContentType);
        content.Headers.ContentEncoding.Add(Wire.ContentEncoding);
        request.Content = content;
        request.Headers.TryAddWithoutValidation(Wire.LicenseHeader, _credential.License);
        request.Headers.TryAddWithoutValidation(Wire.ProofHeader, ProofSigner.Sign(_credential, claims));

        using HttpResponseMessage response = await _http.SendAsync(request);
        string body = await response.Content.ReadAsStringAsync();

        var parsed = new IngestResponse(
            Status: (int)response.StatusCode,
            Error: Read(body, "error"),
            Accepted: (int)ReadNumber(body, "accepted"),
            Deduped: (int)ReadNumber(body, "deduped"),
            Replay: ReadBool(body, "replay"),
            Body: body,
            Date: response.Headers.Date);

        if (o.Advance && response.IsSuccessStatusCode && !parsed.Replay)
        {
            PreviousBh = claims.Bh;
            Seq = seq + 1;
        }

        return parsed;
    }

    public void Dispose() => _http.Dispose();

    /// <summary>Renders envelopes as the LF-separated NDJSON body §4.3 specifies.</summary>
    /// <param name="envelopes">The events, in append order.</param>
    /// <returns>The UTF-8 body.</returns>
    internal static byte[] Ndjson(IReadOnlyList<EventEnvelope> envelopes)
    {
        var sb = new StringBuilder();
        foreach (EventEnvelope envelope in envelopes)
        {
            sb.Append(envelope.ToNdjsonLine());
            sb.Append('\n');
        }

        return Encoding.UTF8.GetBytes(sb.ToString());
    }

    private static string Read(string body, string name)
    {
        if (string.IsNullOrWhiteSpace(body))
            return string.Empty;
        try
        {
            using JsonDocument document = JsonDocument.Parse(body);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.String
                ? value.GetString() ?? string.Empty
                : string.Empty;
        }
        catch (JsonException)
        {
            return string.Empty;
        }
    }

    private static double ReadNumber(string body, string name)
    {
        if (string.IsNullOrWhiteSpace(body))
            return 0;
        try
        {
            using JsonDocument document = JsonDocument.Parse(body);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.Number
                ? value.GetDouble()
                : 0;
        }
        catch (JsonException)
        {
            return 0;
        }
    }

    private static bool ReadBool(string body, string name)
    {
        if (string.IsNullOrWhiteSpace(body))
            return false;
        try
        {
            using JsonDocument document = JsonDocument.Parse(body);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.True;
        }
        catch (JsonException)
        {
            return false;
        }
    }
}
