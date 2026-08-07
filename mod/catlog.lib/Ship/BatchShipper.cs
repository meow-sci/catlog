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
/// <param name="PendingTrigger">
/// Safety valve: ship early when this many events are pending. Not the normal trigger — see
/// <see cref="Wire.ShipPendingTrigger"/>.
/// </param>
/// <param name="AgeTriggerSeconds">
/// The normal trigger: ship when the oldest pending event reaches this age (~60 s by default).
/// </param>
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
    /// <c>409 stream_fork</c>: a new stream id was minted and the sequence reset to 1. Retry when
    /// the <see cref="Wire.MinShipIntervalSeconds"/> window next opens — no backoff, the next
    /// attempt starts a fresh chain.
    /// </summary>
    StreamForked,

    /// <summary>
    /// <c>413 too_large</c> (or a locally detected oversize body): the batch cap was halved. Retry
    /// when the <see cref="Wire.MinShipIntervalSeconds"/> window next opens.
    /// </summary>
    TooLarge,

    /// <summary>
    /// <c>401 clock_skew</c>: the server-clock offset was relearned and persisted. Nothing was
    /// sent; the same batch re-signs with the corrected <c>iat</c> on the next attempt.
    /// </summary>
    ClockResynced,

    /// <summary>
    /// Nothing was sent: the <see cref="Wire.MinShipIntervalSeconds"/> floor has not elapsed since
    /// the previous request. Not a failure — the events stay in the outbox and
    /// <see cref="BatchShipper.ThrottleRemaining"/> says how long is left.
    /// </summary>
    Throttled,

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
///   <item><term>401 clock_skew</term><description>Recompute the offset from the <c>Date</c> header and persist it; the same batch re-signs on the next attempt.</description></item>
///   <item><term>401 (other)</term><description>Latch dead: a bad, expired or revoked license does not fix itself.</description></item>
///   <item><term>409</term><description>Mint a new <c>sid</c>, reset <c>seq = 1</c>, abandon the old chain.</description></item>
///   <item><term>413</term><description>Halve the batch event cap, floor 50, retry.</description></item>
///   <item><term>429 / 5xx / network</term><description>Exponential backoff <c>1 s · 2ⁿ</c> with full jitter, capped at 5 min.</description></item>
///   <item><term>400 / 415</term><description>Latch dead. The batch is <b>not</b> dropped — a poison-pill batch must be visible, not silently destroyed.</description></item>
/// </list>
/// <para>
/// <b>Every one of those retries is also subject to the <see cref="Wire.MinShipIntervalSeconds"/>
/// floor</b>, which is enforced in <see cref="SendAsync"/> immediately before the POST and
/// therefore cannot be walked around by any caller, any trigger or any recovery path. "Retry
/// immediately" in the table above means "retry on the next attempt with the new parameters", and
/// the next attempt is never sooner than the floor allows. See <see cref="ThrottleRemaining"/>.
/// </para>
/// <para>
/// Every one of those retries is safe to repeat blindly, because the batch id is minted per
/// <i>body</i> rather than per attempt (see <c>BatchIdFor</c>): a resend of unchanged bytes carries
/// the batch id the server already knows and short-circuits at §4.5.3 step 11. That is the client
/// half of the idempotency contract in <c>docs/ingest-api.md</c>.
/// </para>
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
    private string? _pendingBatchId;
    private string? _pendingBh;
    private long _clockOffsetMs;
    private long _lastRequestMs;
    private int _batchEventCap;
    private int _consecutiveFailures;

    /// <summary>Creates a shipper.</summary>
    /// <param name="options">Construction parameters.</param>
    /// <param name="outbox">The outbox to drain. Not owned; the caller disposes it.</param>
    /// <param name="credential">The player's credential. Not owned.</param>
    /// <param name="handler">Transport. A default <see cref="HttpClientHandler"/> is created when null.</param>
    /// <param name="clock">
    /// Time source; <b>defaults to the real clock</b>, and that default is what makes the
    /// <see cref="Wire.MinShipIntervalSeconds"/> floor a real 30 seconds in the shipped mod. It is
    /// a test seam and nothing else: <c>mod/catlog</c> constructs this type in exactly one place
    /// and omits the argument, so no config key, environment variable or debug flag can reach it —
    /// see <c>CatlogRuntime.Create</c> and the guard test that pins it.
    /// </param>
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
        _pendingBatchId = _outbox.GetState(Wire.StateKeys.PendingBatchId);
        _pendingBh = _outbox.GetState(Wire.StateKeys.PendingBh);
        _clockOffsetMs = ParseLong(_outbox.GetState(Wire.StateKeys.ClockOffsetMs), 0);

        // Read back rather than started fresh: a relaunch inside the window must inherit it, or
        // quit-and-reload would be a way to send a request whenever you liked.
        _lastRequestMs = ParseLong(_outbox.GetState(Wire.StateKeys.LastRequestMs), 0);
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
    /// <see cref="ShipOnceAsync(CancellationToken)"/> call and reset by any outcome that is not a transport fault.
    /// Drives the backoff ladder.
    /// </summary>
    /// <remarks>
    /// Maintained inside <see cref="ShipOnceAsync(CancellationToken)"/>, not in <see cref="RunAsync"/>, so a caller
    /// that pumps <see cref="ShipOnceAsync(CancellationToken)"/> itself — the simulator, the integration tests, any
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

    /// <summary>
    /// How long is left of the <see cref="Wire.MinShipIntervalSeconds"/> floor;
    /// <see cref="TimeSpan.Zero"/> when a request may be issued now.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Measured against the injected clock's <b>raw</b> <see cref="IShipperClock.UtcNow"/>, not
    /// against <see cref="Now"/>: the server-learned offset is attacker-adjacent input, and a
    /// hostile <c>Date</c> header must not be able to buy a shorter window.
    /// </para>
    /// <para>
    /// A backwards jump of the system clock is treated as "the window restarts now" rather than as
    /// a huge remaining wait, so a player who changes their clock loses one window and the mod
    /// heals itself instead of refusing to ship until the calendar catches up. It can never buy a
    /// <i>shorter</i> wait than the floor. That self-heal <b>re-stamps the anchor</b>, which is the
    /// one case where reading this property writes to the outbox; it happens once per jump, not
    /// once per read.
    /// </para>
    /// </remarks>
    public TimeSpan ThrottleRemaining
    {
        get
        {
            long nowMs = _clock.UtcNow.ToUnixTimeMilliseconds();
            if (_lastRequestMs <= 0)
                return TimeSpan.Zero;

            long elapsedMs = nowMs - _lastRequestMs;
            if (elapsedMs < 0)
            {
                StampRequest(nowMs);
                elapsedMs = 0;
            }

            double remainingMs = (Wire.MinShipIntervalSeconds * 1000.0) - elapsedMs;
            return remainingMs <= 0 ? TimeSpan.Zero : TimeSpan.FromMilliseconds(remainingMs);
        }
    }

    /// <summary>
    /// True when a batch is due under either trigger — the oldest pending event has reached
    /// <see cref="ShipperOptions.AgeTriggerSeconds"/> (the normal path, ~60 s), or
    /// <see cref="ShipperOptions.PendingTrigger"/> events have piled up (the safety valve) — and
    /// the <see cref="Wire.MinShipIntervalSeconds"/> floor allows a request.
    /// </summary>
    /// <remarks>
    /// The floor is checked <b>before</b> either trigger, which is the point: a burst of ten
    /// thousand events sails past <see cref="ShipperOptions.PendingTrigger"/> and still cannot open
    /// the window early. Buffering is what the outbox is for. This is the first of the two
    /// enforcement points; <see cref="SendAsync"/> is the one that actually guarantees it.
    /// </remarks>
    /// <returns>True when the run loop should ship now.</returns>
    public bool ShouldShip()
    {
        long pending = _outbox.PendingCount;
        if (pending == 0)
            return false;
        if (ThrottleRemaining > TimeSpan.Zero)
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
    /// <remarks>
    /// Returns <see cref="ShipOutcome.Throttled"/> without sending anything while the
    /// <see cref="Wire.MinShipIntervalSeconds"/> floor is closed. It <b>refuses</b> rather than
    /// waiting on purpose: a hidden 30-second block inside a method the game thread can reach
    /// (see <see cref="FinalShip"/>) would be a shutdown hang. Every wait is the caller's explicit
    /// choice, taken on the injected clock.
    /// </remarks>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What happened.</returns>
    public Task<ShipAttempt> ShipOnceAsync(CancellationToken ct = default)
        => ShipOnceAsync(ct, ShutdownExemption.No);

    private async Task<ShipAttempt> ShipOnceAsync(CancellationToken ct, ShutdownExemption exemption)
    {
        if (IsDead)
            return Record(new ShipAttempt(ShipOutcome.Fatal, 0, 0, _seq, _sid, DeadReason));

        // A cheap early-out. It is not the guarantee — SendAsync is — but there is no point
        // reading the outbox and spending a Brotli pass on a batch that cannot be sent yet.
        if (exemption == ShutdownExemption.No && ThrottleRemaining is { TotalMilliseconds: > 0 } remaining)
            return Record(ThrottledAttempt(remaining));

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

        return Record(await SendAsync(batch, body, exemption, ct).ConfigureAwait(false));
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
                case ShipOutcome.StreamForked:
                case ShipOutcome.TooLarge:
                case ShipOutcome.ClockResynced:
                    // Either the outbox has more to give or the parameters changed. Loop straight
                    // back: ShouldShip() and the transmission gate together hold the next request
                    // to the floor, so there is nothing to sleep off here.
                    continue;

                case ShipOutcome.Throttled:
                    await _clock.Delay(ThrottleRemaining, ct).ConfigureAwait(false);
                    continue;

                case ShipOutcome.NothingToShip:
                    await _clock.Delay(TimeSpan.FromSeconds(_options.PollSeconds), ct).ConfigureAwait(false);
                    continue;

                case ShipOutcome.Fatal:
                    return;

                default:
                    // §4.5.3's ladder, then the floor on top of it. BackoffPolicy stays the pure
                    // 1 s·2ⁿ schedule the contract documents; a floor baked into it would make the
                    // published ladder a fiction. Retry-After is floored the same way — a server
                    // asking for a faster retry than 30 s does not get one.
                    TimeSpan delay = attempt.RetryAfter
                                     ?? BackoffPolicy.Delay(Math.Max(0, _consecutiveFailures - 1), _jitter());
                    await _clock.Delay(AtLeastTheFloor(delay), ct).ConfigureAwait(false);
                    continue;
            }
        }
    }

    /// <summary>
    /// The shutdown courtesy flush: <b>exactly one</b> ship attempt, hard-bounded, synchronous,
    /// and incapable of throwing or of hanging the caller.
    /// </summary>
    /// <remarks>
    /// <para>
    /// <b>This is an optimisation, never a correctness requirement.</b> The outbox is a SQLite
    /// file and <c>MarkShipped</c> — the <c>DELETE</c> — runs only on a <c>200</c>, so nothing is
    /// removed until the server has acknowledged it and anything unshipped is simply picked up by
    /// the next run. Skipping this entirely loses nothing; all it buys is that a player who just
    /// landed sees it on the board without relaunching.
    /// </para>
    /// <para>
    /// So it is deliberately not a drain loop. One attempt, any outcome, then done — a retry
    /// ladder at game-unload time trades a guarantee we already have for a hang the player would
    /// experience as the game refusing to close.
    /// </para>
    /// <para>
    /// <b>It is the one path exempt from the <see cref="Wire.MinShipIntervalSeconds"/> floor</b>,
    /// via <see cref="ShutdownExemption"/> — see that type for why the exemption is safe and why
    /// it is unreachable from anywhere else. The request is still stamped, so the next session's
    /// first ordinary batch waits out a full window from it.
    /// </para>
    /// <para>
    /// The attempt runs on the thread pool and the caller waits on it for at most
    /// <paramref name="timeout"/>. If it has not finished by then it is cancelled and abandoned
    /// — never awaited again — because a hung TCP connect or a server that accepts and never
    /// answers must not be able to hold the game open.
    /// </para>
    /// </remarks>
    /// <param name="timeout">The hard ceiling on how long the caller may block. Keep it tight.</param>
    /// <returns>What happened, for one line in the log. Never throws.</returns>
    public ShipAttempt FinalShip(TimeSpan timeout)
    {
        if (IsDead)
            return new ShipAttempt(ShipOutcome.Fatal, 0, 0, _seq, _sid, DeadReason);

        var cts = new CancellationTokenSource();
        Task<ShipAttempt> attempt;
        try
        {
            // Task.Run, not an inline await: the outbox read, the Brotli pass and the ES256
            // signature all move off the caller's thread, so the only thing it does is wait, and
            // the wait below is the only thing that bounds it.
            attempt = Task.Run(
                () => ShipOnceAsync(cts.Token, ShutdownExemption.Yes), CancellationToken.None);
        }
        catch (Exception ex)
        {
            cts.Dispose();
            return new ShipAttempt(ShipOutcome.NetworkError, 0, 0, _seq, _sid, ex.Message);
        }

        try
        {
            attempt.Wait(timeout);
        }
        catch (Exception)
        {
            // A faulted or cancelled task; the state checks below say which.
        }

        if (attempt.IsCompletedSuccessfully)
        {
            cts.Dispose();
            return attempt.Result;
        }

        string reason = attempt.IsCompleted
            ? attempt.Exception?.GetBaseException().Message ?? "the shutdown flush was cancelled"
            : $"the shutdown flush did not finish within {timeout.TotalSeconds:0.#} s";

        // Abandoned, not awaited: cancel so the socket lets go, then observe whatever it ends up
        // doing so an abandoned failure cannot resurface as an unobserved task exception, and
        // dispose the token source only once nothing can still be holding it.
        cts.Cancel();
        _ = attempt.ContinueWith(
            static (finished, state) =>
            {
                _ = finished.Exception;
                ((CancellationTokenSource)state!).Dispose();
            },
            cts,
            CancellationToken.None,
            TaskContinuationOptions.None,
            TaskScheduler.Default);

        return new ShipAttempt(ShipOutcome.NetworkError, 0, 0, _seq, _sid, reason);
    }

    /// <summary>Releases the transport when this instance created it.</summary>
    public void Dispose()
    {
        if (_ownsHttpClient)
            _http.Dispose();
    }

    /// <summary>
    /// The single point of transmission, and therefore the single place the
    /// <see cref="Wire.MinShipIntervalSeconds"/> floor has to hold. Nothing else in this type
    /// touches <see cref="HttpClient"/>.
    /// </summary>
    /// <param name="batch">The batch to send.</param>
    /// <param name="body">Its compressed bytes.</param>
    /// <param name="exemption">
    /// The one narrow bypass, reachable only from <see cref="FinalShip"/> — see
    /// <see cref="ShutdownExemption"/>.
    /// </param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What happened.</returns>
    private async Task<ShipAttempt> SendAsync(
        OutboxBatch batch, byte[] body, ShutdownExemption exemption, CancellationToken ct)
    {
        // The gate. Deliberately here rather than only in ShouldShip() or only in the config
        // clamp: this covers the age trigger, the count trigger, a hand-built ShipperOptions,
        // every recovery retry above, and whatever calls this next year.
        if (!TryBeginRequest(exemption, out TimeSpan remaining))
            return ThrottledAttempt(remaining);

        string bh = Bytes.Sha256Base64Url(body);
        var claims = new ProofClaims(
            Jti: BatchIdFor(bh),
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
                // The offset is learned and persisted here, and the SAME batch re-signs with it on
                // the next attempt. This used to recurse and re-POST immediately; that was a
                // second request inside the same window, so it is now deferred like every other
                // retry. Nothing is lost — the body, the seq and the batch id are unchanged, so
                // the resend is the same idempotent resend it always was, one window later.
                case HttpStatusCode.Unauthorized when string.Equals(error, Wire.Errors.ClockSkew, StringComparison.Ordinal):
                    LearnClockOffset(response, payload);
                    ModLog.Log.Warn(
                        $"catlog: server reported clock skew; resynced by {_clockOffsetMs} ms. The same batch "
                        + $"re-signs on the next attempt, at most {Wire.MinShipIntervalSeconds:0} s from now.");
                    return new ShipAttempt(ShipOutcome.ClockResynced, status, 0, _seq, _sid, error);

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

    /// <summary>
    /// The batch id (proof <c>jti</c>) to send this body under: the one already minted for it if
    /// this is a resend, a fresh ULID otherwise.
    /// </summary>
    /// <remarks>
    /// <para>
    /// This is what makes "retry when in doubt" free. A request whose response never arrived —
    /// a timeout, a dropped connection, a 503 from a full write queue — may or may not have
    /// committed server-side, and the client cannot tell. Minting a fresh <c>jti</c> for the
    /// resend would miss the §4.5.3 step-11 replay short-circuit and fall through to step 12,
    /// where the unchanged <c>seq</c> reads as a reused one and earns a <c>409 stream_fork</c>.
    /// Reusing the id turns exactly that case into the <c>200 {"replay": true}</c> it should be.
    /// </para>
    /// <para>
    /// It is keyed on the body hash, not on "there is a batch in flight", and that is the
    /// load-bearing part: the id must never outlive the bytes it was minted for. If a prune or a
    /// <c>413</c> halving changes what the next batch contains, the hash changes with it and a
    /// new id is minted — otherwise a replay short-circuit would retire outbox rows the server
    /// had never seen.
    /// </para>
    /// <para>
    /// Persisted, so the same reasoning holds across a game crash mid-ship. A restart that
    /// re-sends the identical body replays cleanly instead of forking the stream.
    /// </para>
    /// </remarks>
    /// <param name="bh">The body hash this batch will carry as the proof's <c>bh</c>.</param>
    /// <returns>The batch id.</returns>
    private string BatchIdFor(string bh)
    {
        if (_pendingBatchId is { Length: > 0 } existing && string.Equals(_pendingBh, bh, StringComparison.Ordinal))
            return existing;

        string minted = Ids.NewUlid();
        _pendingBatchId = minted;
        _pendingBh = bh;
        _outbox.SetState(Wire.StateKeys.PendingBatchId, minted);
        _outbox.SetState(Wire.StateKeys.PendingBh, bh);
        return minted;
    }

    private void ClearPendingBatchId()
    {
        _pendingBatchId = null;
        _pendingBh = null;
        _outbox.ClearState(Wire.StateKeys.PendingBatchId);
        _outbox.ClearState(Wire.StateKeys.PendingBh);
    }

    private ShipAttempt OnAccepted(OutboxBatch batch, string bh, string payload, int status)
    {
        bool replay = ReadBool(payload, "replay");
        int accepted = replay ? 0 : batch.Count;
        long usedSeq = _seq;

        _outbox.MarkShipped(batch.LastRowId);
        ClearPendingBatchId();

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
            case ShipOutcome.Throttled:
                // Not an attempt at anything: no request was made, so neither the ladder nor
                // LastAttempt moves. Overwriting LastAttempt here would replace "shipped 240
                // events" in the status window with "throttled" every time the loop came round.
                return attempt;

            case ShipOutcome.Accepted:
            case ShipOutcome.Replayed:
            case ShipOutcome.NothingToShip:
                _consecutiveFailures = 0;
                break;

            case ShipOutcome.StreamForked:
            case ShipOutcome.TooLarge:
            case ShipOutcome.ClockResynced:
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

    /// <summary>
    /// Opens the <see cref="Wire.MinShipIntervalSeconds"/> window for exactly one request and
    /// stamps it, or reports how long is left.
    /// </summary>
    /// <remarks>
    /// The stamp is taken <b>before</b> the request rather than after it, so the floor is a
    /// start-to-start interval: a slow round trip does not buy the next one a shorter wait, and a
    /// request that fails or never completes still counts, because the server saw it either way.
    /// </remarks>
    /// <param name="exemption">The <see cref="FinalShip"/> bypass; <see cref="ShutdownExemption.No"/> everywhere else.</param>
    /// <param name="remaining">How long is left when the answer is false.</param>
    /// <returns>True when the caller may transmit.</returns>
    private bool TryBeginRequest(ShutdownExemption exemption, out TimeSpan remaining)
    {
        remaining = ThrottleRemaining;
        if (exemption == ShutdownExemption.No && remaining > TimeSpan.Zero)
            return false;

        // The exempt request is still stamped. It is a real request, so the *next* window starts
        // from it: relaunching the game does not then get a free ordinary batch on top.
        remaining = TimeSpan.Zero;
        StampRequest(_clock.UtcNow.ToUnixTimeMilliseconds());
        return true;
    }

    /// <summary>
    /// Whether an attempt is the once-per-session <see cref="FinalShip"/> flush, which is the
    /// <b>only</b> exemption from the <see cref="Wire.MinShipIntervalSeconds"/> floor.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Private, and threaded through private overloads only, so it is not a general bypass: the
    /// public <see cref="ShipOnceAsync(CancellationToken)"/> always passes
    /// <see cref="No"/> and there is no way for any caller — inside this assembly or outside it —
    /// to ask for <see cref="Yes"/>.
    /// </para>
    /// <para>
    /// <b>Why the exemption is safe.</b> It fires at most once per game session, so abusing it
    /// means actually quitting and relaunching KSA, which costs far more than the 30 s it would
    /// save. That self-limiting property is exactly what the in-session triggers lack, which is
    /// why they get no such exemption. What it buys is that a player who just finished a flight
    /// and quit sees it on the board without relaunching first.
    /// </para>
    /// </remarks>
    private enum ShutdownExemption
    {
        /// <summary>The floor applies. Every path except <see cref="FinalShip"/>.</summary>
        No,

        /// <summary>The floor does not apply. <see cref="FinalShip"/> and nothing else.</summary>
        Yes,
    }

    private void StampRequest(long atMs)
    {
        _lastRequestMs = atMs;
        _outbox.SetState(Wire.StateKeys.LastRequestMs, atMs.ToString(CultureInfo.InvariantCulture));
    }

    private ShipAttempt ThrottledAttempt(TimeSpan remaining)
        => new(
            ShipOutcome.Throttled,
            0,
            0,
            _seq,
            _sid,
            $"the {Wire.MinShipIntervalSeconds:0} s minimum reporting interval has "
            + $"{remaining.TotalSeconds:0.0} s left");

    private static TimeSpan AtLeastTheFloor(TimeSpan delay)
    {
        TimeSpan floor = TimeSpan.FromSeconds(Wire.MinShipIntervalSeconds);
        return delay < floor ? floor : delay;
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
