using System;
using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Ship;

/// <summary>Construction parameters for a <see cref="BatchShipper"/>.</summary>
/// <param name="IngestUrl">
/// The configured ingest URL. Sent verbatim as the proof's <c>htu</c> claim, which the server
/// compares by string equality with no normalization (§4.5.2) — so this is kept as a string, not
/// round-tripped through <see cref="Uri"/>.
/// </param>
/// <param name="BatchEventCap">Initial events-per-batch cap; halved on <c>413</c>.</param>
/// <param name="PendingTrigger">Ship as soon as this many events are pending.</param>
/// <param name="AgeTriggerSeconds">Ship when the oldest pending event reaches this age.</param>
/// <param name="PollSeconds">How long the run loop idles when there is nothing to do.</param>
/// <param name="OutboxCapBytes">Outbox size cap; 0 disables pruning.</param>
public sealed record ShipperOptions(
    string IngestUrl,
    int BatchEventCap = Wire.DefaultBatchEventCap,
    int PendingTrigger = Wire.ShipPendingTrigger,
    double AgeTriggerSeconds = Wire.ShipAgeTriggerSeconds,
    double PollSeconds = 1.0,
    long OutboxCapBytes = Wire.DefaultOutboxCapMb * 1024L * 1024L);

/// <summary>What happened on one ship attempt.</summary>
public enum ShipOutcome
{
    /// <summary>The outbox was empty.</summary>
    NothingToShip,

    /// <summary>The server stored the batch.</summary>
    Accepted,

    /// <summary>The server had already seen this batch id and short-circuited.</summary>
    Replayed,

    /// <summary>
    /// <c>409 stream_fork</c>: a new stream id was minted and the sequence reset to 1. Retry
    /// immediately — no backoff, the next attempt starts a fresh chain.
    /// </summary>
    StreamForked,

    /// <summary><c>413 too_large</c> (or a locally detected oversize body): the batch cap was halved. Retry immediately.</summary>
    TooLarge,

    /// <summary><c>429</c>: back off, honouring <c>Retry-After</c> when present.</summary>
    RateLimited,

    /// <summary>A 5xx or an unexpected status: back off.</summary>
    ServerError,

    /// <summary>The request never completed: back off.</summary>
    NetworkError,

    /// <summary>The shipper is latched dead for the session; nothing further will be sent.</summary>
    Fatal,
}

/// <summary>The result of one ship attempt.</summary>
/// <param name="Outcome">What happened.</param>
/// <param name="StatusCode">The HTTP status, or 0 when the request never completed.</param>
/// <param name="EventsShipped">
/// How many events this client removed from its outbox — the <b>local</b> batch size, zero on a
/// whole-batch replay. It is not what the server said it stored: see <see cref="ServerAccepted"/>
/// and <see cref="ServerDeduped"/> for that.
/// </param>
/// <param name="Seq">The sequence number that was used.</param>
/// <param name="Sid">The stream id that was used.</param>
/// <param name="Error">The server's error code, or a local description; empty on success.</param>
/// <param name="RetryAfter">The server's <c>Retry-After</c>, when it sent one.</param>
public sealed record ShipAttempt(
    ShipOutcome Outcome,
    int StatusCode,
    int EventsShipped,
    long Seq,
    string Sid,
    string Error,
    TimeSpan? RetryAfter = null)
{
    /// <summary>
    /// The server's own <c>accepted</c> count from the <c>200</c> body (§4.4), or <c>null</c> when
    /// the server did not say (a non-2xx, or a body this client could not parse).
    /// </summary>
    /// <remarks>
    /// Nullable on purpose: "the server stored nothing" and "the server did not tell us" are
    /// different facts, and the status window must not present the second as the first.
    /// </remarks>
    public int? ServerAccepted { get; init; }

    /// <summary>The server's own <c>deduped</c> count from the <c>200</c> body, or <c>null</c> when it did not say.</summary>
    public int? ServerDeduped { get; init; }
}

/// <summary>
/// Drains the outbox to the ingest endpoint: compress, sign, POST, and apply the §4.5.3 mod-side
/// recovery table.
/// </summary>
/// <remarks>
/// <para>
/// Every external dependency is injectable — <see cref="HttpMessageHandler"/> for transport,
/// <see cref="IShipperClock"/> for time and waiting, and a jitter source for the backoff draw — so
/// every recovery path in the table below is exercised in unit tests with no sockets and no real
/// waiting.
/// </para>
/// <list type="table">
///   <listheader><term>Response</term><description>Recovery</description></listheader>
///   <item><term>200</term><description>Delete the shipped rows, <c>seq++</c>, <c>ph ← bh</c>.</description></item>
///   <item><term>401 clock_skew</term><description>Recompute the offset from the <c>Date</c> header, re-sign, retry <b>once</b>.</description></item>
///   <item><term>401 (other)</term><description>Latch dead: a bad, expired or revoked license does not fix itself.</description></item>
///   <item><term>409</term><description>Mint a new <c>sid</c>, reset <c>seq = 1</c>, abandon the old chain.</description></item>
///   <item><term>413</term><description>Halve the batch event cap, floor 50, retry.</description></item>
///   <item><term>429 / 5xx / network</term><description>Exponential backoff <c>1 s · 2ⁿ</c> with full jitter, capped at 5 min.</description></item>
///   <item><term>400 / 415</term><description>Latch dead. The batch is <b>not</b> dropped — a poison-pill batch must be visible, not silently destroyed.</description></item>
/// </list>
/// </remarks>
public sealed class BatchShipper : IDisposable
{
    private readonly ShipperOptions _options;
    private readonly OutboxDb _outbox;
    private readonly Credential _credential;
    private readonly HttpClient _http;
    private readonly bool _ownsHttpClient;
    private readonly IShipperClock _clock;
    private readonly Func<double> _jitter;
    private readonly Uri _endpoint;

    private string _sid;
    private long _seq;
    private string? _lastBh;
    private long _clockOffsetMs;
    private int _batchEventCap;
    private int _consecutiveFailures;

    /// <summary>Creates a shipper.</summary>
    /// <param name="options">Construction parameters.</param>
    /// <param name="outbox">The outbox to drain. Not owned; the caller disposes it.</param>
    /// <param name="credential">The player's credential. Not owned.</param>
    /// <param name="handler">Transport. A default <see cref="HttpClientHandler"/> is created when null.</param>
    /// <param name="clock">Time source; defaults to the real clock.</param>
    /// <param name="jitter">Uniform <c>[0, 1]</c> draw for backoff; defaults to <see cref="Random.Shared"/>.</param>
    /// <exception cref="UriFormatException"><paramref name="options"/> has an unparseable ingest URL.</exception>
    public BatchShipper(
        ShipperOptions options,
        OutboxDb outbox,
        Credential credential,
        HttpMessageHandler? handler = null,
        IShipperClock? clock = null,
        Func<double>? jitter = null)
    {
        _options = options;
        _outbox = outbox;
        _credential = credential;
        _clock = clock ?? SystemShipperClock.Instance;
        _jitter = jitter ?? Random.Shared.NextDouble;
        _endpoint = new Uri(options.IngestUrl, UriKind.Absolute);
        _ownsHttpClient = handler is null;
        _http = new HttpClient(handler ?? new HttpClientHandler(), disposeHandler: _ownsHttpClient);
        _batchEventCap = Math.Clamp(options.BatchEventCap, Wire.MinBatchEventCap, Wire.MaxEventsPerBatch);

        _sid = _outbox.GetState(Wire.StateKeys.StreamId) ?? string.Empty;
        if (string.IsNullOrEmpty(_sid))
        {
            _sid = Ids.NewUlid();
            _outbox.SetState(Wire.StateKeys.StreamId, _sid);
            _outbox.SetState(Wire.StateKeys.Seq, "1");
            _outbox.ClearState(Wire.StateKeys.LastBh);
        }

        _seq = ParseLong(_outbox.GetState(Wire.StateKeys.Seq), 1);
        if (_seq < 1)
            _seq = 1;
        _lastBh = _outbox.GetState(Wire.StateKeys.LastBh);
        _clockOffsetMs = ParseLong(_outbox.GetState(Wire.StateKeys.ClockOffsetMs), 0);
    }

    /// <summary>The current stream id.</summary>
    public string StreamId => _sid;

    /// <summary>The sequence number the next batch will carry.</summary>
    public long Sequence => _seq;

    /// <summary>The current events-per-batch cap, after any <c>413</c> halving.</summary>
    public int BatchEventCap => _batchEventCap;

    /// <summary>The learned server-clock offset in milliseconds (server time minus local time).</summary>
    public long ClockOffsetMs => _clockOffsetMs;

    /// <summary>
    /// Consecutive retryable failures (429 / 5xx / network), advanced by every
    /// <see cref="ShipOnceAsync"/> call and reset by any outcome that is not a transport fault.
    /// Drives the backoff ladder.
    /// </summary>
    /// <remarks>
    /// Maintained inside <see cref="ShipOnceAsync"/>, not in <see cref="RunAsync"/>, so a caller
    /// that pumps <see cref="ShipOnceAsync"/> itself — the simulator, the integration tests, any
    /// synchronous drain — sees the counter advance and can use it as a retry ceiling. It used to
    /// be advanced only by the run loop, which meant such a caller read a permanent zero and, if it
    /// trusted it, looped forever against a dead server.
    /// </remarks>
    public int ConsecutiveFailures => _consecutiveFailures;

    /// <summary>True when the shipper has latched dead for the session.</summary>
    public bool IsDead { get; private set; }

    /// <summary>Why the shipper latched dead; empty while alive.</summary>
    public string DeadReason { get; private set; } = string.Empty;

    /// <summary>The most recent attempt, for the status window.</summary>
    public ShipAttempt? LastAttempt { get; private set; }

    /// <summary>Local time corrected by the learned server offset — the basis for the proof's <c>iat</c>.</summary>
    public DateTimeOffset Now => _clock.UtcNow.AddMilliseconds(_clockOffsetMs);

    /// <summary>True when a batch is due under either trigger (§7.2: ≥64 pending, or oldest ≥15 s).</summary>
    /// <returns>True when the run loop should ship now.</returns>
    public bool ShouldShip()
    {
        long pending = _outbox.PendingCount;
        if (pending == 0)
            return false;
        if (pending >= _options.PendingTrigger)
            return true;

        long? oldest = _outbox.OldestCreatedMs;
        if (oldest is null)
            return false;
        double ageSeconds = (_clock.UtcNow.ToUnixTimeMilliseconds() - oldest.Value) / 1000.0;
        return ageSeconds >= _options.AgeTriggerSeconds;
    }

    /// <summary>
    /// Ships at most one batch and applies the recovery table. Never throws except on
    /// cancellation; every failure is reported as a <see cref="ShipAttempt"/>.
    /// </summary>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What happened.</returns>
    public async Task<ShipAttempt> ShipOnceAsync(CancellationToken ct = default)
    {
        if (IsDead)
            return Record(new ShipAttempt(ShipOutcome.Fatal, 0, 0, _seq, _sid, DeadReason));

        OutboxBatch batch;
        try
        {
            batch = _outbox.NextBatch(_batchEventCap);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            return Record(LatchDead($"the outbox could not be read: {ex.Message}", ex));
        }

        if (batch.IsEmpty)
            return Record(new ShipAttempt(ShipOutcome.NothingToShip, 0, 0, _seq, _sid, string.Empty));

        byte[] body = BrotliCodec.Compress(batch.ToNdjson());

        // Catch an oversize body before spending a round trip on a guaranteed 413.
        if (body.Length > Wire.MaxCompressedBodyBytes)
        {
            if (!HalveBatchCap())
            {
                return Record(LatchDead(
                    $"a {_batchEventCap}-event batch compresses to {body.Length} bytes, over the "
                    + $"{Wire.MaxCompressedBodyBytes}-byte cap; the outbox contains events that can never ship"));
            }

            return Record(new ShipAttempt(
                ShipOutcome.TooLarge, 0, 0, _seq, _sid, "compressed body over cap (detected locally)"));
        }

        return Record(await SendAsync(batch, body, allowSkewRetry: true, ct).ConfigureAwait(false));
    }

    /// <summary>
    /// The shipper loop: waits for a trigger, ships, and applies backoff. Returns when
    /// <paramref name="ct"/> is cancelled or the shipper latches dead.
    /// </summary>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A task that completes when the loop stops.</returns>
    public async Task RunAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested && !IsDead)
        {
            try
            {
                if (_options.OutboxCapBytes > 0)
                    _outbox.Prune(_options.OutboxCapBytes);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                LatchDead($"the outbox could not be pruned: {ex.Message}", ex);
                return;
            }

            if (!ShouldShip())
            {
                await _clock.Delay(TimeSpan.FromSeconds(_options.PollSeconds), ct).ConfigureAwait(false);
                continue;
            }

            // ShipOnceAsync owns _consecutiveFailures (see ConsecutiveFailures): by the time an
            // attempt is back the ladder has already advanced, so the rung this failure is owed is
            // one below the new count.
            ShipAttempt attempt = await ShipOnceAsync(ct).ConfigureAwait(false);
            switch (attempt.Outcome)
            {
                case ShipOutcome.Accepted:
                case ShipOutcome.Replayed:
                    continue; // Drain the rest of the outbox without waiting.

                case ShipOutcome.StreamForked:
                case ShipOutcome.TooLarge:
                    continue; // Parameters changed; retry immediately with the new ones.

                case ShipOutcome.NothingToShip:
                    await _clock.Delay(TimeSpan.FromSeconds(_options.PollSeconds), ct).ConfigureAwait(false);
                    continue;

                case ShipOutcome.Fatal:
                    return;

                default:
                    TimeSpan delay = attempt.RetryAfter
                                     ?? BackoffPolicy.Delay(Math.Max(0, _consecutiveFailures - 1), _jitter());
                    await _clock.Delay(delay, ct).ConfigureAwait(false);
                    continue;
            }
        }
    }

    /// <summary>Releases the transport when this instance created it.</summary>
    public void Dispose()
    {
        if (_ownsHttpClient)
            _http.Dispose();
    }

    private async Task<ShipAttempt> SendAsync(
        OutboxBatch batch, byte[] body, bool allowSkewRetry, CancellationToken ct)
    {
        string bh = Bytes.Sha256Base64Url(body);
        var claims = new ProofClaims(
            Jti: Ids.NewUlid(),
            Iat: Now.ToUnixTimeSeconds(),
            Htm: Wire.HttpMethod,
            Htu: _options.IngestUrl,
            Bh: bh,
            Sid: _sid,
            Seq: _seq,
            Ph: _seq == 1 ? null : _lastBh);

        string proof;
        try
        {
            proof = ProofSigner.Sign(_credential, claims);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            return LatchDead($"the batch proof could not be signed: {ex.Message}", ex);
        }

        using var request = new HttpRequestMessage(System.Net.Http.HttpMethod.Post, _endpoint);
        var content = new ByteArrayContent(body);
        content.Headers.ContentType = new MediaTypeHeaderValue(Wire.ContentType);
        content.Headers.ContentEncoding.Add(Wire.ContentEncoding);
        request.Content = content;
        request.Headers.TryAddWithoutValidation(Wire.LicenseHeader, _credential.License);
        request.Headers.TryAddWithoutValidation(Wire.ProofHeader, proof);

        HttpResponseMessage response;
        try
        {
            response = await _http.SendAsync(request, ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (ct.IsCancellationRequested)
        {
            throw;
        }
        catch (Exception ex)
        {
            return new ShipAttempt(ShipOutcome.NetworkError, 0, 0, _seq, _sid, ex.Message);
        }

        using (response)
        {
            string payload;
            try
            {
                payload = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
                throw;
            }
            catch (Exception ex)
            {
                return new ShipAttempt(ShipOutcome.NetworkError, (int)response.StatusCode, 0, _seq, _sid, ex.Message);
            }

            int status = (int)response.StatusCode;
            string error = ReadError(payload);

            if (response.IsSuccessStatusCode)
                return OnAccepted(batch, bh, payload, status);

            switch (response.StatusCode)
            {
                case HttpStatusCode.Unauthorized when string.Equals(error, Wire.Errors.ClockSkew, StringComparison.Ordinal):
                    LearnClockOffset(response, payload);
                    if (!allowSkewRetry)
                        return new ShipAttempt(ShipOutcome.ServerError, status, 0, _seq, _sid, error);
                    ModLog.Log.Warn(
                        $"catlog: server reported clock skew; resynced by {_clockOffsetMs} ms and retrying once.");
                    return await SendAsync(batch, body, allowSkewRetry: false, ct).ConfigureAwait(false);

                case HttpStatusCode.Unauthorized:
                    return LatchDead($"the server rejected the credential: {Describe(error, payload)}");

                case HttpStatusCode.Conflict:
                    return OnStreamFork(status, error);

                case HttpStatusCode.RequestEntityTooLarge:
                    if (!HalveBatchCap())
                    {
                        return LatchDead(
                            $"the server rejects even a {Wire.MinBatchEventCap}-event batch as too large: "
                            + Describe(error, payload));
                    }

                    ModLog.Log.Warn($"catlog: server returned 413; batch cap halved to {_batchEventCap}.");
                    return new ShipAttempt(ShipOutcome.TooLarge, status, 0, _seq, _sid, error);

                case HttpStatusCode.TooManyRequests:
                    return new ShipAttempt(
                        ShipOutcome.RateLimited, status, 0, _seq, _sid, error, ReadRetryAfter(response));

                case HttpStatusCode.BadRequest:
                case HttpStatusCode.UnsupportedMediaType:
                    // A contract violation, not a transient fault. Retrying forever would spin and
                    // dropping the batch would destroy data, so stop and surface it.
                    return LatchDead($"the server rejected the batch as malformed: {Describe(error, payload)}");

                default:
                    return new ShipAttempt(ShipOutcome.ServerError, status, 0, _seq, _sid, Describe(error, payload));
            }
        }
    }

    private ShipAttempt OnAccepted(OutboxBatch batch, string bh, string payload, int status)
    {
        bool replay = ReadBool(payload, "replay");
        int accepted = replay ? 0 : batch.Count;
        long usedSeq = _seq;

        _outbox.MarkShipped(batch.LastRowId);

        // A replay means this exact batch id was already stored, which means the stream state was
        // already advanced for it server-side — so the local chain advances either way.
        _seq++;
        _lastBh = bh;
        _outbox.SetState(Wire.StateKeys.Seq, _seq.ToString(CultureInfo.InvariantCulture));
        _outbox.SetState(Wire.StateKeys.LastBh, bh);

        return new ShipAttempt(
            replay ? ShipOutcome.Replayed : ShipOutcome.Accepted,
            status,
            accepted,
            usedSeq,
            _sid,
            string.Empty)
        {
            // §4.4: 200 carries {"accepted": n, "deduped": n} (plus "replay": true on the
            // short-circuit). Report what the server said, not what we sent — a status window that
            // renders the local batch size as "shipped" cannot tell the player that the server
            // deduped every row.
            ServerAccepted = TryReadInt32(payload, "accepted", out int serverAccepted) ? serverAccepted : null,
            ServerDeduped = TryReadInt32(payload, "deduped", out int serverDeduped) ? serverDeduped : null,
        };
    }

    private ShipAttempt OnStreamFork(int status, string error)
    {
        long forkedSeq = _seq;
        _sid = Ids.NewUlid();
        _seq = 1;
        _lastBh = null;
        _outbox.SetState(Wire.StateKeys.StreamId, _sid);
        _outbox.SetState(Wire.StateKeys.Seq, "1");
        _outbox.ClearState(Wire.StateKeys.LastBh);
        ModLog.Log.Warn($"catlog: server reported a stream fork at seq {forkedSeq}; new stream {_sid}, seq reset to 1.");
        return new ShipAttempt(ShipOutcome.StreamForked, status, 0, forkedSeq, _sid, error);
    }

    private bool HalveBatchCap()
    {
        if (_batchEventCap <= Wire.MinBatchEventCap)
            return false;
        _batchEventCap = Math.Max(Wire.MinBatchEventCap, _batchEventCap / 2);
        return true;
    }

    private void LearnClockOffset(HttpResponseMessage response, string payload)
    {
        long localMs = _clock.UtcNow.ToUnixTimeMilliseconds();

        // The Date header is the documented source (§4.4: "Date header always present — the mod
        // uses it for clock sync"). server_time in the 401 body is the fallback.
        if (response.Headers.Date is { } date)
        {
            _clockOffsetMs = date.ToUnixTimeMilliseconds() - localMs;
        }
        else if (TryReadInt64(payload, "server_time", out long serverMs))
        {
            _clockOffsetMs = serverMs - localMs;
        }
        else
        {
            return;
        }

        _outbox.SetState(Wire.StateKeys.ClockOffsetMs, _clockOffsetMs.ToString(CultureInfo.InvariantCulture));
    }

    private ShipAttempt LatchDead(string reason, Exception? exception = null)
    {
        if (!IsDead)
        {
            IsDead = true;
            DeadReason = reason;
            ModLog.Log.Error($"catlog: shipper disabled for this session: {reason}", exception);
        }

        return new ShipAttempt(ShipOutcome.Fatal, 0, 0, _seq, _sid, reason);
    }

    // The single choke point every ShipOnceAsync return path passes through, so the retry ladder is
    // maintained here rather than in RunAsync (see ConsecutiveFailures).
    private ShipAttempt Record(ShipAttempt attempt)
    {
        switch (attempt.Outcome)
        {
            case ShipOutcome.Accepted:
            case ShipOutcome.Replayed:
            case ShipOutcome.NothingToShip:
                _consecutiveFailures = 0;
                break;

            case ShipOutcome.StreamForked:
            case ShipOutcome.TooLarge:
            case ShipOutcome.Fatal:
                // Not transport faults: the parameters changed, or the shipper is done. Leaving the
                // ladder where it is means a 413 in the middle of a bad-network patch does not
                // silently reset the backoff the next failure is owed.
                break;

            default:
                _consecutiveFailures++;
                break;
        }

        LastAttempt = attempt;
        return attempt;
    }

    private static TimeSpan? ReadRetryAfter(HttpResponseMessage response)
    {
        RetryConditionHeaderValue? header = response.Headers.RetryAfter;
        if (header is null)
            return null;
        if (header.Delta is { } delta)
            return delta;
        if (header.Date is { } date)
        {
            TimeSpan until = date - DateTimeOffset.UtcNow;
            return until > TimeSpan.Zero ? until : TimeSpan.Zero;
        }

        return null;
    }

    private static string Describe(string error, string payload)
        => string.IsNullOrEmpty(error) ? Truncate(payload) : error;

    private static string Truncate(string value)
        => value.Length <= 200 ? value : value[..200];

    private static string ReadError(string payload)
    {
        if (string.IsNullOrWhiteSpace(payload))
            return string.Empty;

        try
        {
            using JsonDocument document = JsonDocument.Parse(payload);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty("error", out JsonElement error)
                   && error.ValueKind == JsonValueKind.String
                ? error.GetString() ?? string.Empty
                : string.Empty;
        }
        catch (JsonException)
        {
            return string.Empty;
        }
    }

    private static bool ReadBool(string payload, string name)
    {
        if (string.IsNullOrWhiteSpace(payload))
            return false;

        try
        {
            using JsonDocument document = JsonDocument.Parse(payload);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.True;
        }
        catch (JsonException)
        {
            return false;
        }
    }

    private static bool TryReadInt32(string payload, string name, out int parsed)
    {
        parsed = 0;
        if (string.IsNullOrWhiteSpace(payload))
            return false;

        try
        {
            using JsonDocument document = JsonDocument.Parse(payload);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.Number
                   && value.TryGetInt32(out parsed);
        }
        catch (JsonException)
        {
            return false;
        }
    }

    private static bool TryReadInt64(string payload, string name, out long parsed)
    {
        parsed = 0;
        if (string.IsNullOrWhiteSpace(payload))
            return false;

        try
        {
            using JsonDocument document = JsonDocument.Parse(payload);
            return document.RootElement.ValueKind == JsonValueKind.Object
                   && document.RootElement.TryGetProperty(name, out JsonElement value)
                   && value.ValueKind == JsonValueKind.Number
                   && value.TryGetInt64(out parsed);
        }
        catch (JsonException)
        {
            return false;
        }
    }

    private static long ParseLong(string? value, long fallback)
        => long.TryParse(value, NumberStyles.Integer, CultureInfo.InvariantCulture, out long parsed)
            ? parsed
            : fallback;
}
