using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Ship;

namespace MeowSci.Catlog.Lib.Tests.Ship;

/// <summary>One request the shipper made, captured in full.</summary>
internal sealed record RecordedRequest(
    byte[] Body,
    string? License,
    string? Proof,
    string? ContentType,
    string? ContentEncoding)
{
    /// <summary>The proof JWS's claims, decoded (not verified).</summary>
    internal JsonElement ProofClaims
    {
        get
        {
            using JsonDocument? document = MeowSci.Catlog.Lib.Auth.Jws.DecodePayloadUnverified(Proof);
            return document is null
                ? throw new InvalidOperationException("the request carried no decodable proof")
                : document.RootElement.Clone();
        }
    }

    /// <summary>The proof JWS's protected header, decoded (not verified).</summary>
    internal JsonElement ProofHeader
    {
        get
        {
            using JsonDocument? document = MeowSci.Catlog.Lib.Auth.Jws.DecodeHeaderUnverified(Proof);
            return document is null
                ? throw new InvalidOperationException("the request carried no decodable proof")
                : document.RootElement.Clone();
        }
    }

    /// <summary>The decompressed NDJSON body.</summary>
    internal string Ndjson => Encoding.UTF8.GetString(BrotliCodec.Decompress(Body));
}

/// <summary>
/// A scripted transport. No sockets: the shipper's entire recovery table is exercised by handing
/// it canned responses, in order, and inspecting what it sent.
/// </summary>
internal sealed class FakeHttpHandler : HttpMessageHandler
{
    private readonly Func<RecordedRequest, int, HttpResponseMessage> _responder;

    internal FakeHttpHandler(Func<RecordedRequest, int, HttpResponseMessage> responder)
        => _responder = responder;

    /// <summary>Answers each request from a queue, cycling on the last entry once exhausted.</summary>
    internal static FakeHttpHandler Scripted(params Func<HttpResponseMessage>[] responses)
        => new((_, index) => responses[Math.Min(index, responses.Length - 1)]());

    /// <summary>Answers every request identically.</summary>
    internal static FakeHttpHandler Always(Func<HttpResponseMessage> response) => new((_, _) => response());

    /// <summary>Every request the shipper made, in order.</summary>
    internal List<RecordedRequest> Requests { get; } = [];

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request, CancellationToken cancellationToken)
    {
        byte[] body = request.Content is null
            ? []
            : await request.Content.ReadAsByteArrayAsync(cancellationToken).ConfigureAwait(false);

        var recorded = new RecordedRequest(
            body,
            First(request.Headers, Wire.LicenseHeader),
            First(request.Headers, Wire.ProofHeader),
            request.Content?.Headers.ContentType?.MediaType,
            FirstEncoding(request.Content));

        Requests.Add(recorded);
        return _responder(recorded, Requests.Count - 1);
    }

    /// <summary>A <c>200 {"accepted": n, "deduped": 0}</c>.</summary>
    internal static HttpResponseMessage Ok(int accepted = 1, bool replay = false)
        => Json(HttpStatusCode.OK, replay
            ? "{\"accepted\":0,\"deduped\":" + accepted + ",\"replay\":true}"
            : "{\"accepted\":" + accepted + ",\"deduped\":0}");

    /// <summary>An error response with the standard <c>{"error": code}</c> body.</summary>
    internal static HttpResponseMessage Error(HttpStatusCode status, string code, string? extra = null)
        => Json(status, extra is null ? $"{{\"error\":\"{code}\"}}" : $"{{\"error\":\"{code}\",{extra}}}");

    /// <summary>A <c>401 clock_skew</c> carrying a <c>Date</c> header at <paramref name="serverTime"/>.</summary>
    internal static HttpResponseMessage ClockSkew(DateTimeOffset serverTime)
    {
        HttpResponseMessage response = Error(
            HttpStatusCode.Unauthorized,
            Wire.Errors.ClockSkew,
            $"\"server_time\":{serverTime.ToUnixTimeMilliseconds()}");
        response.Headers.Date = serverTime;
        return response;
    }

    /// <summary>A <c>429</c> with a <c>Retry-After</c> in seconds.</summary>
    internal static HttpResponseMessage RateLimited(int retryAfterSeconds)
    {
        HttpResponseMessage response = Error(HttpStatusCode.TooManyRequests, Wire.Errors.RateLimited);
        response.Headers.RetryAfter = new RetryConditionHeaderValue(TimeSpan.FromSeconds(retryAfterSeconds));
        return response;
    }

    private static HttpResponseMessage Json(HttpStatusCode status, string body)
        => new(status) { Content = new StringContent(body, Encoding.UTF8, "application/json") };

    private static string? First(HttpRequestHeaders headers, string name)
        => headers.TryGetValues(name, out IEnumerable<string>? values)
            ? string.Join(",", values)
            : null;

    private static string? FirstEncoding(HttpContent? content)
    {
        if (content is null)
            return null;
        foreach (string encoding in content.Headers.ContentEncoding)
            return encoding;
        return null;
    }
}

/// <summary>
/// A virtualized clock: <see cref="Delay"/> completes immediately, records what it was asked to
/// wait for, and advances <see cref="UtcNow"/> by that amount. No test ever waits on wall time.
/// </summary>
internal sealed class FakeShipperClock : IShipperClock
{
    internal FakeShipperClock(DateTimeOffset? start = null)
        => UtcNow = start ?? DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);

    /// <inheritdoc />
    public DateTimeOffset UtcNow { get; set; }

    /// <summary>Every delay the shipper asked for, in order.</summary>
    internal List<TimeSpan> Delays { get; } = [];

    /// <summary>Advances the clock without recording a delay.</summary>
    internal void Advance(TimeSpan by) => UtcNow += by;

    /// <inheritdoc />
    public Task Delay(TimeSpan delay, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        Delays.Add(delay);
        UtcNow += delay;
        return Task.CompletedTask;
    }
}
