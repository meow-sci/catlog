using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Sim;

/// <summary>What one scenario run produced.</summary>
/// <param name="Scenario">The scenario name.</param>
/// <param name="Frames">How many frames were fed through the bridge.</param>
/// <param name="Events">How many envelopes the pipeline produced and the outbox accepted.</param>
/// <param name="Batches">How many batches the shipper sent.</param>
/// <param name="Shipped">How many events the server accepted across those batches.</param>
/// <param name="EventsByType">Envelope count per event type, for the run report.</param>
/// <param name="Duration">Wall-clock time the run took.</param>
/// <param name="SessionId">The session ULID the events carry.</param>
/// <param name="InstallId">The install ULID the events carry.</param>
public sealed record RunSummary(
    string Scenario,
    int Frames,
    int Events,
    int Batches,
    int Shipped,
    IReadOnlyDictionary<string, int> EventsByType,
    TimeSpan Duration,
    string SessionId,
    string InstallId);

/// <summary>
/// Drives a scenario through the real client pipeline: <see cref="GameBridge"/> →
/// <see cref="EventPipeline"/> → <see cref="OutboxDb"/> → <see cref="BatchShipper"/> → a live
/// server (§7.3).
/// </summary>
/// <remarks>
/// <para>
/// The wiring below is deliberately the same wiring WP8 will write inside the game, with the
/// scenario standing in for the game thread. Nothing is stubbed: the detector, the window
/// accumulator, the impact correlator, the SQLite outbox, the ES256 proof signer and the batch
/// shipper are all the shipping implementations, so a regression in any of them fails a scenario.
/// </para>
/// <para>
/// <b>The one deliberate departure from WP8's wiring is that the shipper is pumped synchronously
/// rather than run as a background task.</b> <see cref="BatchShipper.RunAsync"/> is the real mod's
/// loop, but cancelling it mid-request would leave a batch that the server stored and the outbox
/// still holds — a re-ship, which the server correctly dedups. That would make the soak scenario's
/// "dedup is 0" assertion a statement about a race rather than about the pipeline. The recovery
/// table underneath (<see cref="BatchShipper.ShipOnceAsync"/>) is identical either way, and
/// <c>catlog.lib.tests</c> covers <see cref="BatchShipper.RunAsync"/> on a virtual clock.
/// </para>
/// <para>
/// <b>Within a frame, signals are processed before passive telemetry.</b> The game thread raises
/// signals during the frame and samples at the end of it, so this is the honest order; it also
/// means a <c>flight.started</c> precedes the first <c>telemetry.window</c> of that flight in the
/// outbox, which is the order §4.3 then preserves onto the wire.
/// </para>
/// </remarks>
public sealed class ScenarioRunner : IDisposable
{
    /// <summary>The mod version the simulated session reports.</summary>
    public const string ModVersion = "0.1.0-sim";

    /// <summary>The game build the simulated session reports (the build WP6/WP8 verified against).</summary>
    public const string GameBuild = "2026.8.5.5168";

    private const int MaxConsecutiveShipFailures = 8;

    private readonly SimOptions _options;
    private readonly Credential _credential;
    private readonly string _outboxDir;
    private readonly OutboxDb _outbox;
    private readonly SimShipperClock _shipperClock = new();
    private readonly BatchShipper _shipper;
    private readonly EventPipeline _pipeline;
    private readonly GameBridge _bridge = new();
    private readonly SampleClock _sampleClock = new(Wire.DefaultSampleHz);
    private readonly Dictionary<string, int> _byType = new(StringComparer.Ordinal);
    private readonly Random _jitter = new(0x0CA71065);

    private int _events;
    private int _batches;
    private int _shipped;

    // BatchShipper.ConsecutiveFailures is advanced by its own RunAsync loop, which this runner
    // deliberately does not use (see the class remarks), so the retry ladder is counted here.
    private int _failures;

    /// <summary>Creates a runner and opens a throwaway outbox for it.</summary>
    /// <param name="options">The parsed CLI options.</param>
    /// <param name="credential">The loaded credential.</param>
    public ScenarioRunner(SimOptions options, Credential credential)
    {
        _options = options;
        _credential = credential;

        // A fresh outbox per run: the shipper's stream id and sequence live in it, and a scenario
        // must start a new stream rather than inherit a chain the server has already seen.
        _outboxDir = Path.Combine(Path.GetTempPath(), "catlog-sim-" + Ids.NewUlid());
        _outbox = OutboxDb.Open(Path.Combine(_outboxDir, "outbox.db"));

        string installId = options.InstallIdFor(credential.Handle);
        _pipeline = new EventPipeline(new EventPipelineOptions(
            InstallId: installId,
            ModVersion: ModVersion,
            GameBuild: GameBuild,
            // One scenario is one KSA save: its clock starts at zero and runs
            // forward, which is exactly a career (§4.1). Deriving the id from the
            // scenario name rather than minting one keeps a re-run comparable
            // with the run before it, so the career-time assertions are stable.
            CareerId: Ids.CareerId(installId, "sim:" + options.Scenario)));

        _shipper = new BatchShipper(
            new ShipperOptions(
                IngestUrl: options.IngestUrl,
                BatchEventCap: options.BatchEvents,
                // WP7 pins both triggers for determinism, and that is deliberately independent of
                // the mod's shipped defaults: a scenario must ship on a fixed event count, not on
                // a wall clock. The age trigger — the mod's *normal* path at ~60 s — is disabled
                // because a scenario has no idle time and it would only ever fire on the final
                // drain, at which point the explicit Drain() has already done the work.
                PendingTrigger: options.BatchEvents,
                AgeTriggerSeconds: double.MaxValue,
                OutboxCapBytes: 0),
            _outbox,
            credential,
            handler: null,
            clock: _shipperClock);
    }

    /// <summary>The outbox directory, deleted on <see cref="Dispose"/>.</summary>
    public string OutboxDirectory => _outboxDir;

    /// <summary>Plays a scenario and drains everything it produced to the server.</summary>
    /// <param name="scenario">The scenario.</param>
    /// <returns>What the run produced.</returns>
    /// <exception cref="SimException">The shipper latched dead or could not drain.</exception>
    public RunSummary Run(IScenario scenario)
    {
        long startedTicks = Environment.TickCount64;
        double lastSimT = 0;
        int frames = 0;

        Append([_pipeline.SessionStarted(0, SimClock.Wall(0))]);

        foreach (SimStep step in scenario.Steps())
        {
            double dt = step.SimT - lastSimT;
            lastSimT = step.SimT;
            long wallMs = SimClock.Wall(step.SimT);

            foreach (GameSignal signal in step.Signals)
                _bridge.Signal(signal);

            // The 2 Hz cadence is enforced by the real SampleClock, drop-not-backfill included
            // (§7.2, D15) — a scenario stepping faster than 2 Hz has samples dropped exactly as
            // the game would drop them.
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

            while (_shipper.ShouldShip())
                ShipOne();

            Pace(dt);
        }

        _bridge.Complete();
        Append(_pipeline.ProcessSignals(_bridge.DrainSignals()));
        Append(_pipeline.Flush(SimClock.Wall(lastSimT)));
        Drain();

        return new RunSummary(
            scenario.Name,
            frames,
            _events,
            _batches,
            _shipped,
            _byType,
            TimeSpan.FromMilliseconds(Environment.TickCount64 - startedTicks),
            _pipeline.SessionId,
            _pipeline.Tracker.InstallId);
    }

    /// <summary>Closes the outbox and removes its directory.</summary>
    public void Dispose()
    {
        _shipper.Dispose();
        _outbox.Dispose();
        try
        {
            if (Directory.Exists(_outboxDir))
                Directory.Delete(_outboxDir, recursive: true);
        }
        catch (IOException)
        {
            // A leftover temp directory is not worth failing a run over.
        }
    }

    private void Append(IReadOnlyList<EventEnvelope> envelopes)
    {
        if (envelopes.Count == 0)
            return;

        _outbox.Append(envelopes);
        _events += envelopes.Count;
        foreach (EventEnvelope envelope in envelopes)
            _byType[envelope.Type] = _byType.TryGetValue(envelope.Type, out int count) ? count + 1 : 1;
    }

    private void Drain()
    {
        while (_outbox.PendingCount > 0)
            ShipOne();
    }

    private void ShipOne()
    {
        ShipAttempt attempt = _shipper.ShipOnceAsync(CancellationToken.None).GetAwaiter().GetResult();
        switch (attempt.Outcome)
        {
            case ShipOutcome.Accepted:
            case ShipOutcome.Replayed:
                _batches++;
                _shipped += attempt.EventsShipped;
                _failures = 0;
                return;

            case ShipOutcome.NothingToShip:
                _failures = 0;
                return;

            case ShipOutcome.StreamForked:
            case ShipOutcome.TooLarge:
            case ShipOutcome.ClockResynced:
                // Parameters changed; the next attempt uses the new ones.
                return;

            case ShipOutcome.Throttled:
                // The hard 30 s reporting floor. A scenario has no wall-clock time to spend on it,
                // so the injected clock is wound past the window rather than slept through — the
                // floor is still enforced, on the timeline the shipper is actually reading.
                // The extra millisecond is not cosmetic: unix-millisecond truncation can leave the
                // window a tick short of open, and a loop that advances by exactly the remainder
                // would then need a second pass every time.
                _shipperClock.Advance(_shipper.ThrottleRemaining + TimeSpan.FromMilliseconds(1));
                return;

            case ShipOutcome.Fatal:
                throw new SimException($"the shipper latched dead: {_shipper.DeadReason}");

            default:
                if (++_failures >= MaxConsecutiveShipFailures)
                {
                    throw new SimException(
                        $"the shipper failed {MaxConsecutiveShipFailures} times in a row "
                        + $"(HTTP {attempt.StatusCode}, {attempt.Error}); is {_options.Server} running?");
                }

                TimeSpan delay = attempt.RetryAfter
                                 ?? BackoffPolicy.Delay(_failures - 1, _jitter.NextDouble());
                Console.WriteLine(
                    $"  ship: HTTP {attempt.StatusCode} {attempt.Error}; retrying in {delay.TotalSeconds:0.0}s");
                Thread.Sleep(delay);
                return;
        }
    }

    private void Pace(double dtSimSeconds)
    {
        if (_options.Speed <= 0 || dtSimSeconds <= 0)
            return;

        double wallSeconds = dtSimSeconds / _options.Speed;
        if (wallSeconds > 0.0005)
            Thread.Sleep(TimeSpan.FromSeconds(wallSeconds));
    }
}
