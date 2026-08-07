using System;
using System.Collections.Generic;
using System.IO;
using System.Net.Http;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>What one player's run produced.</summary>
/// <param name="Index">The player's index.</param>
/// <param name="Handle">The claimed handle.</param>
/// <param name="IdP">Which identity provider signed them in.</param>
/// <param name="Career">What their career looked like across the run's window.</param>
/// <param name="Role">The moderation path they exercise, if any.</param>
/// <param name="Frames">Telemetry frames published.</param>
/// <param name="Events">Envelopes the pipeline produced and the outbox accepted.</param>
/// <param name="Batches">Batches the server accepted or replayed.</param>
/// <param name="ServerAccepted">Events the server said it stored.</param>
/// <param name="ServerDeduped">Events the server said it had already seen.</param>
/// <param name="ClockResyncs">
/// <c>401 clock_skew</c> recoveries. Expected under <c>--clock virtual</c>: a client whose floor is
/// wound forward eventually leaves the ±300 s proof window and relearns the offset from the
/// <c>Date</c> header, which is the mod's real recovery path.
/// </param>
/// <param name="StreamForks">409 recoveries.</param>
/// <param name="Oversize">413 recoveries.</param>
/// <param name="RateLimited">429 responses absorbed.</param>
/// <param name="Busy">503 responses absorbed — the server's bounded write channel pushing back.</param>
/// <param name="EventsByType">Envelope count per event type.</param>
/// <param name="Digest">A hash of the event stream, for seed-to-seed comparison.</param>
/// <param name="Elapsed">Wall-clock time this player took.</param>
/// <param name="Reissued">True when the player swapped credentials mid-run.</param>
/// <param name="Error">Why the player stopped early, or an empty string.</param>
internal sealed record PlayerResult(
    int Index,
    string Handle,
    string IdP,
    CareerSummary Career,
    ModerationRole Role,
    int Frames,
    int Events,
    int Batches,
    int ServerAccepted,
    int ServerDeduped,
    int ClockResyncs,
    int StreamForks,
    int Oversize,
    int RateLimited,
    int Busy,
    IReadOnlyDictionary<string, int> EventsByType,
    string Digest,
    TimeSpan Elapsed,
    bool Reissued,
    string Error);

/// <summary>
/// Drives one player through the real client: <see cref="GameBridge"/> → <see cref="EventPipeline"/>
/// → <see cref="OutboxDb"/> → <see cref="BatchShipper"/> → a live <c>catlogd</c>.
/// </summary>
/// <remarks>
/// <para>
/// This is <c>catlog.sim</c>'s <c>ScenarioRunner</c> loop, made asynchronous and made to run
/// hundreds of times at once. Nothing is stubbed and nothing is bypassed: the detector, the window
/// accumulator, the impact correlator, the SQLite outbox, the ES256 proof signer and the batch
/// shipper are all the shipping implementations, and no envelope is ever hand-built.
/// </para>
/// <para>
/// <b>Cadence and floor are two separate things here, on purpose.</b> The <i>cadence</i> — when a
/// batch is due — is decided on sim time from <c>--ship-age</c>, so a run reproduces the shape of
/// a real player's traffic: one batch per N sim seconds, which for the default N is the mod's own
/// sixty. The <i>floor</i> — whether that batch may actually go — is left entirely to
/// <c>BatchShipper</c>, and when it refuses, the injected clock is wound forward by the remainder
/// and not one millisecond more, exactly as <c>catlog.sim</c> does. Winding it by sim time instead
/// would look tidier and would be much worse: the proof's <c>iat</c> would leave §4.3's ±300 s
/// window on every batch, and the run would spend itself on clock-skew recoveries.
/// </para>
/// <para>
/// <b>What that does and does not buy.</b> It removes the client's self-imposed wait, and nothing
/// else. Every limit on the far side is real and is felt: the per-credential token bucket
/// (1 batch / 2 s, burst 5), the 256-deep write channel and its <c>503 + Retry-After</c>, the
/// single writer goroutine, and the ±300 s proof-skew window — which a wound-forward client does
/// eventually leave, at which point it takes a <c>401 clock_skew</c>, relearns the offset from the
/// <c>Date</c> header and carries on. Those resyncs are counted and reported rather than hidden,
/// because they are the honest cost of compressing time.
/// </para>
/// </remarks>
internal sealed class PlayerRunner : IDisposable
{
    /// <summary>The mod version the simulated sessions report.</summary>
    internal const string ModVersion = "0.1.0-loadgen";

    /// <summary>The game build the simulated sessions report.</summary>
    internal const string GameBuild = "2026.8.5.5168";

    private const int MaxConsecutiveFailures = 8;

    // One batch may cost a floor wind, a clock resync, a rate-limit wait and a retry ladder; this
    // is generous headroom over all of them and still bounded, so a pathological server cannot
    // hang a player forever.
    private const int MaxAttemptsPerBatch = 64;

    private readonly LoadOptions _options;
    private readonly PlayerAccount _account;
    private readonly HttpMessageHandler _transport;
    private readonly HttpStats _stats;
    private readonly Action<CapturedRequest>? _capture;
    private readonly string _outboxDir;
    private readonly OutboxDb _outbox;
    private readonly EventPipeline _pipeline;
    private readonly GameBridge _bridge = new();
    private readonly SampleClock _sampleClock = new(Wire.DefaultSampleHz);
    private readonly SimShipperClock? _virtualClock;
    private readonly Dictionary<string, int> _byType = new(StringComparer.Ordinal);
    private readonly StreamDigest _digest = new();
    private readonly Prng _jitter;

    private BatchShipper _shipper;
    private int _events;
    private int _batches;
    private int _serverAccepted;
    private int _serverDeduped;
    private int _clockResyncs;
    private int _streamForks;
    private int _oversize;
    private int _rateLimited;
    private int _busy;
    private int _failures;
    private bool _reissued;

    // The sim instant of the last ship opportunity taken; the age trigger's anchor.
    private double _lastShipSimT;

    /// <summary>Creates a runner and opens a throwaway outbox for it.</summary>
    /// <param name="options">The run's options.</param>
    /// <param name="account">The provisioned player.</param>
    /// <param name="transport">The shared HTTP transport.</param>
    /// <param name="stats">Where ingest requests are recorded.</param>
    /// <param name="capture">Non-null for the one player that feeds the replay probe.</param>
    internal PlayerRunner(
        LoadOptions options,
        PlayerAccount account,
        HttpMessageHandler transport,
        HttpStats stats,
        Action<CapturedRequest>? capture)
    {
        _options = options;
        _account = account;
        _transport = transport;
        _stats = stats;
        _capture = capture;
        _jitter = new Prng((ulong)options.Seed + ((ulong)account.Index * 7919UL));

        // A fresh outbox per player: the shipper's stream id and sequence live in it, and a run
        // must start a new stream rather than inherit a chain the server has already seen.
        _outboxDir = Path.Combine(Path.GetTempPath(), "catlog-loadgen-" + Ids.NewUlid());
        _outbox = OutboxDb.Open(Path.Combine(_outboxDir, "outbox.db"));

        _pipeline = new EventPipeline(new EventPipelineOptions(
            InstallId: InstallIdFor(account.Handle),
            ModVersion: ModVersion,
            GameBuild: GameBuild));

        _virtualClock = options.Clock == ShipClock.Virtual ? new SimShipperClock() : null;
        _shipper = NewShipper(account.Credential);
    }

    /// <summary>The temp directory holding this player's outbox.</summary>
    internal string OutboxDirectory => _outboxDir;

    /// <summary>Plays a script and drains everything it produced to the server.</summary>
    /// <param name="script">The player's script.</param>
    /// <param name="reissue">
    /// Called once at the half-way point for a player whose role is
    /// <see cref="ModerationRole.Reissue"/>; returns the replacement credential.
    /// </param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What the player produced.</returns>
    internal async Task<PlayerResult> RunAsync(
        PlayerScript script, Func<CancellationToken, Task<Credential?>>? reissue, CancellationToken ct)
    {
        long started = Environment.TickCount64;
        // sim_t is career time, so a player who arrives with three hundred in-game hours behind
        // them opens their session at 1.08e6 rather than at zero. Anchoring the frame loop, the
        // ship cadence and the session event anywhere else would emit a career that appears to
        // restart every run.
        double lastSimT = script.Epoch;
        _lastShipSimT = script.Epoch;
        int frames = 0;
        string error = string.Empty;
        bool reissuePending = reissue is not null && script.Role == ModerationRole.Reissue;

        try
        {
            Append([_pipeline.SessionStarted(script.Epoch, ScriptWall(script, script.Epoch))]);

            foreach (SimStep step in script.Steps())
            {
                ct.ThrowIfCancellationRequested();

                double dt = step.SimT - lastSimT;
                lastSimT = step.SimT;
                long wallMs = ScriptWall(script, step.SimT);

                foreach (GameSignal signal in step.Signals)
                    _bridge.Signal(signal);

                TelemetryFrame? frame = null;
                if (step.Snapshots.Count > 0 && _sampleClock.Tick(dt))
                {
                    frame = _bridge.PublishFrame(step.SimT, wallMs, step.Snapshots);
                    frames++;
                }

                _bridge.EndFrame(step.SimT, wallMs);

                Append(_pipeline.ProcessSignals(_bridge.DrainSignals()));
                if (frame is not null)
                    Append(_pipeline.ProcessFrame(frame));

                // The age trigger, decided here in **sim** seconds.
                //
                // BatchShipper's own age trigger reads OutboxDb's `created_ms`, which is stamped
                // from the real wall clock at append time and cannot be virtualised from out
                // here without reaching into catlog.lib. Against a wound-forward clock that
                // comparison saturates after the first window and then says "due" on every frame
                // for the rest of the run, so `--ship-age` would silently do nothing and every
                // player would ship as fast as the floor allowed. Deciding the cadence on sim
                // time reproduces the mod's real one instead: the oldest pending event is
                // `--ship-age` sim seconds old, exactly as it would be in play.
                if (step.SimT - _lastShipSimT >= _options.ShipAgeSeconds
                    || _outbox.PendingCount >= _options.BatchEvents)
                {
                    _lastShipSimT = step.SimT;
                    if (!await ShipDueAsync(ct).ConfigureAwait(false))
                        break;
                }

                if (reissuePending && step.SimT >= script.MidPoint)
                {
                    reissuePending = false;
                    await SwapCredentialAsync(reissue!, ct).ConfigureAwait(false);
                }
            }

            _bridge.Complete();
            Append(_pipeline.ProcessSignals(_bridge.DrainSignals()));
            Append(_pipeline.Flush(ScriptWall(script, lastSimT)));
            await DrainAsync(ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            error = "cancelled";
        }
        catch (SimException ex)
        {
            error = ex.Message;
        }

        if (error.Length == 0 && _outbox.PendingCount > 0)
            error = $"{_outbox.PendingCount} events never left the outbox";

        return new PlayerResult(
            _account.Index, _account.Handle, _account.IdP, script.Summary, script.Role,
            frames, _events, _batches, _serverAccepted, _serverDeduped,
            _clockResyncs, _streamForks, _oversize, _rateLimited, _busy,
            _byType, _digest.Value(),
            TimeSpan.FromMilliseconds(Environment.TickCount64 - started), _reissued, error);
    }

    /// <summary>Closes the outbox and removes its directory.</summary>
    public void Dispose()
    {
        _shipper.Dispose();
        _outbox.Dispose();
        if (_options.KeepOutboxes)
            return;

        try
        {
            if (Directory.Exists(_outboxDir))
                Directory.Delete(_outboxDir, recursive: true);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            // A leftover temp directory is not worth failing a run over.
        }
    }

    // --- shipping ---------------------------------------------------------------------

    /// <summary>
    /// Builds a shipper over this player's outbox. The recording handler it wraps is kept alive by
    /// the shipper's own <see cref="HttpClient"/>, and never disposes the shared transport.
    /// </summary>
    private BatchShipper NewShipper(Credential credential)
    {
        var handler = new RecordingHandler(_transport, _stats, _capture);
        return new BatchShipper(
            new ShipperOptions(
                IngestUrl: _options.IngestUrl,
                BatchEventCap: _options.BatchEvents,
                PendingTrigger: _options.BatchEvents,
                AgeTriggerSeconds: _options.ShipAgeSeconds,
                // Pruning off: this harness asserts that no event is silently lost, and a prune
                // would drop passive rows to stay under a cap and make that assertion a lie.
                OutboxCapBytes: 0),
            _outbox,
            credential,
            handler,
            _virtualClock);
    }

    private async Task DrainAsync(CancellationToken ct)
    {
        int guard = 0;
        while (_outbox.PendingCount > 0)
        {
            if (++guard > 100_000)
                throw new SimException("the drain made no progress; giving up rather than looping forever");
            if (!await ShipDueAsync(ct).ConfigureAwait(false))
                return;
        }
    }

    /// <summary>
    /// Ships one batch, working through whatever the §4.5.3 recovery table throws up, and returns
    /// when the batch has landed or the player cannot continue.
    /// </summary>
    /// <remarks>
    /// This is <see cref="BatchShipper.RunAsync"/>'s loop, driven by the caller instead of by a
    /// background task. Retrying <b>here</b> rather than at the next frame is what keeps the
    /// recoveries cheap: a <c>401 clock_skew</c> relearns the offset and the very next attempt
    /// carries a correct <c>iat</c>, whereas going back to the frame loop would let the virtual
    /// clock drift out of the window again before the retry and turn one resync into an unbounded
    /// stream of them.
    /// </remarks>
    private async Task<bool> ShipDueAsync(CancellationToken ct)
    {
        for (int attempts = 0; attempts < MaxAttemptsPerBatch; attempts++)
        {
            ShipAttempt attempt = await _shipper.ShipOnceAsync(ct).ConfigureAwait(false);
            switch (attempt.Outcome)
            {
                case ShipOutcome.Accepted:
                case ShipOutcome.Replayed:
                    _batches++;
                    _serverAccepted += attempt.ServerAccepted ?? 0;
                    _serverDeduped += attempt.ServerDeduped ?? 0;
                    _failures = 0;
                    return true;

                case ShipOutcome.NothingToShip:
                    _failures = 0;
                    return true;

                case ShipOutcome.StreamForked:
                    _streamForks++;
                    continue;

                case ShipOutcome.TooLarge:
                    _oversize++;
                    continue;

                case ShipOutcome.ClockResynced:
                    _clockResyncs++;
                    continue;

                case ShipOutcome.Throttled:
                    await WaitOutTheFloorAsync(ct).ConfigureAwait(false);
                    continue;

                case ShipOutcome.RateLimited:
                    // The server's per-credential token bucket. Real seconds, on purpose: this is
                    // the limit the run is supposed to feel.
                    _rateLimited++;
                    await Task.Delay(attempt.RetryAfter ?? TimeSpan.FromSeconds(2), ct).ConfigureAwait(false);
                    continue;

                case ShipOutcome.Fatal:
                    throw new SimException($"the shipper latched dead: {_shipper.DeadReason}");

                default:
                {
                    if (attempt.StatusCode == 503)
                        _busy++;

                    if (++_failures >= MaxConsecutiveFailures)
                    {
                        throw new SimException(
                            $"{MaxConsecutiveFailures} consecutive ship failures "
                            + $"(HTTP {attempt.StatusCode}, {attempt.Error})");
                    }

                    TimeSpan delay = attempt.RetryAfter ?? BackoffPolicy.Delay(_failures - 1, _jitter.NextDouble());
                    await Task.Delay(delay, ct).ConfigureAwait(false);
                    continue;
                }
            }
        }

        throw new SimException(
            $"one batch took more than {MaxAttemptsPerBatch} attempts and never landed");
    }

    /// <summary>
    /// Handles the hard <see cref="Wire.MinShipIntervalSeconds"/> floor.
    /// </summary>
    /// <remarks>
    /// Under <c>--clock virtual</c> the injected clock is wound past the window instead of slept
    /// through — the floor is still enforced, on the timeline the shipper is actually reading. The
    /// extra millisecond is not cosmetic: unix-millisecond truncation can leave the window a tick
    /// short of open. Under <c>--clock real</c> the wait is a real wait, because that is the whole
    /// point of the mode.
    /// </remarks>
    private async Task WaitOutTheFloorAsync(CancellationToken ct)
    {
        TimeSpan remaining = _shipper.ThrottleRemaining;
        if (_virtualClock is not null)
        {
            // Wound forward by the remainder and not one second more. Advancing further — by sim
            // time, say — would run the proof's `iat` out of §4.3's ±300 s window on every single
            // batch and turn the clock-skew recovery into the dominant cost of the run.
            _virtualClock.Advance(remaining + TimeSpan.FromMilliseconds(1));
            return;
        }

        await Task.Delay(remaining + TimeSpan.FromMilliseconds(50), ct).ConfigureAwait(false);
    }

    private async Task SwapCredentialAsync(Func<CancellationToken, Task<Credential?>> reissue, CancellationToken ct)
    {
        // Everything already produced goes out on the old credential first. A reissue revokes the
        // credential it replaces (D16), so anything left in the outbox would meet a 401 on a key
        // the server has just stopped trusting.
        await DrainAsync(ct).ConfigureAwait(false);

        Credential? replacement = await reissue(ct).ConfigureAwait(false);
        if (replacement is null)
            return;

        _shipper.Dispose();
        _account.Credential.Dispose();
        _account.Credential = replacement;
        // The same virtual clock instance carries over, so the floor is continuous across the
        // swap rather than reset by it — and the outbox carries sid/seq/last_bh, so the stream
        // chain continues under the new key exactly as it would for a real player.
        _shipper = NewShipper(replacement);
        _reissued = true;
    }

    // --- bookkeeping ------------------------------------------------------------------

    private void Append(IReadOnlyList<EventEnvelope> envelopes)
    {
        if (envelopes.Count == 0)
            return;

        _outbox.Append(envelopes);
        _events += envelopes.Count;
        foreach (EventEnvelope envelope in envelopes)
        {
            _byType[envelope.Type] = _byType.TryGetValue(envelope.Type, out int count) ? count + 1 : 1;
            _digest.Add(envelope.Type, envelope.SimT);
        }
    }

    private static long ScriptWall(PlayerScript script, double simT) => script.Wall(simT);

    /// <summary>
    /// The install ULID this player runs under, derived from the handle so a re-run with the same
    /// namespace produces the same <c>kid</c>s (§4.2 salts them with the install id).
    /// </summary>
    private static string InstallIdFor(string handle)
    {
        byte[] digest = SHA256.HashData(Encoding.UTF8.GetBytes("catlog-loadgen-install:" + handle));
        // A ULID's first six bytes are a millisecond timestamp; masking the top byte keeps the
        // value inside the encodable range so it round-trips through Ulid.Parse.
        digest[0] &= 0x0F;
        return new Ulid(new ReadOnlySpan<byte>(digest, 0, 16)).ToString();
    }
}
