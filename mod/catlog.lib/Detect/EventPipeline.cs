using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>Construction parameters for an <see cref="EventPipeline"/>.</summary>
/// <param name="InstallId">The install ULID (§4.2 <c>session.started.install</c>, and the <c>kid</c> salt).</param>
/// <param name="ModVersion">The catlog mod version string.</param>
/// <param name="GameBuild">The game build string.</param>
/// <param name="SessionId">Session ULID; a fresh one is minted when null.</param>
/// <param name="WindowSeconds">Telemetry window length in sim seconds.</param>
public sealed record EventPipelineOptions(
    string InstallId,
    string ModVersion = "0.1.0",
    string GameBuild = "unknown",
    string? SessionId = null,
    double WindowSeconds = Wire.TelemetryWindowSeconds);

/// <summary>
/// The worker-side pipeline: telemetry frames and game signals in, <see cref="EventEnvelope"/>s
/// out. Composes <see cref="EventDetector"/>, <see cref="WindowAccumulator"/>,
/// <see cref="ImpactCorrelator"/> and <see cref="FlightTracker"/>.
/// </summary>
/// <remarks>
/// <para>
/// Single-threaded by contract: one worker owns an instance and calls it from one place. It has no
/// locks because the two inputs it consumes — <see cref="Telemetry.GameBridge.Frames"/> and
/// <see cref="Telemetry.GameBridge.Signals"/> — are both read by that same worker.
/// </para>
/// <para>
/// The output order is the order the events must reach the outbox, and the outbox preserves it:
/// §4.3 requires a batch's events to be in append order.
/// </para>
/// </remarks>
public sealed class EventPipeline
{
    private readonly EventPipelineOptions _options;
    private readonly HashSet<FlightFlag> _sessionFlags = [];
    private readonly HashSet<string> _liveVehicles = new(StringComparer.Ordinal);

    private EventDetector _detector = new();
    private WindowAccumulator _windows;
    private ImpactCorrelator _correlator = new();

    /// <summary>Creates a pipeline.</summary>
    /// <param name="options">Construction parameters.</param>
    public EventPipeline(EventPipelineOptions options)
    {
        _options = options;
        _windows = new WindowAccumulator(options.WindowSeconds);
        Tracker = new FlightTracker(options.InstallId, options.SessionId);
    }

    /// <summary>The identity bookkeeper. Exposed for the simulator and the status window.</summary>
    public FlightTracker Tracker { get; }

    /// <summary>The current session ULID.</summary>
    public string SessionId => Tracker.SessionId;

    /// <summary>Timing for the per-frame detect + window pass.</summary>
    public PerfStat FrameStats { get; } = new();

    /// <summary>Timing for the per-signal pass.</summary>
    public PerfStat SignalStats { get; } = new();

    /// <summary>
    /// The <c>session.started</c> event. Emit exactly once per session, before anything else.
    /// </summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <returns>The envelope.</returns>
    public EventEnvelope SessionStarted(double simT, long wallMs)
        => EventEnvelope.Create(
            EventTypes.SessionStarted,
            Tracker.SessionId,
            flight: null,
            simT,
            wallMs,
            new SessionStartedPayload(_options.ModVersion, _options.GameBuild, _options.InstallId));

    /// <summary>Runs the detector and the window folds over one published frame.</summary>
    /// <param name="frame">The frame.</param>
    /// <returns>The envelopes produced, in emission order.</returns>
    public IReadOnlyList<EventEnvelope> ProcessFrame(TelemetryFrame frame)
    {
        using PerfStat.Scope _ = FrameStats.Measure();

        List<EventEnvelope>? envelopes = null;

        foreach (DetectedEvent detected in _detector.Observe(frame))
            Add(ref envelopes, EventFactory.FromDetected(Tracker, detected));

        foreach (TelemetrySnapshot snapshot in frame.Vehicles)
        {
            if (_windows.Add(snapshot) is { } closed)
                Add(ref envelopes, EventFactory.FromWindow(Tracker, closed, frame.WallMs));
        }

        PruneStaleVehicles(frame, ref envelopes);

        return envelopes ?? (IReadOnlyList<EventEnvelope>)Array.Empty<EventEnvelope>();
    }

    /// <summary>Maps one game signal to zero or more envelopes.</summary>
    /// <param name="signal">The signal.</param>
    /// <returns>The envelopes produced, in emission order.</returns>
    public IReadOnlyList<EventEnvelope> ProcessSignal(GameSignal signal)
    {
        using PerfStat.Scope _ = SignalStats.Measure();

        List<EventEnvelope>? envelopes = null;
        Dispatch(signal, ref envelopes);
        return envelopes ?? (IReadOnlyList<EventEnvelope>)Array.Empty<EventEnvelope>();
    }

    /// <summary>Maps a batch of signals, preserving order.</summary>
    /// <param name="signals">The signals.</param>
    /// <returns>The envelopes produced, in emission order.</returns>
    public IReadOnlyList<EventEnvelope> ProcessSignals(IEnumerable<GameSignal> signals)
    {
        List<EventEnvelope>? envelopes = null;
        foreach (GameSignal signal in signals)
            Dispatch(signal, ref envelopes);
        return envelopes ?? (IReadOnlyList<EventEnvelope>)Array.Empty<EventEnvelope>();
    }

    /// <summary>
    /// Closes everything still open: partial telemetry windows and impacts still awaiting a
    /// verdict. Call at session end, before the last ship.
    /// </summary>
    /// <remarks>
    /// <b>Peek semantics, deliberately.</b> Impacts drained here are resolved out of band, after
    /// the frame loop has stopped — and by then a vehicle's flight may already have ended (the
    /// impact that killed it produced a <c>flight.ended</c> a moment earlier). Minting a flight for
    /// it, which is what <see cref="FlightTracker.FlightFor"/> would do, would attach the impact to
    /// a flight ULID that has no <c>flight.started</c> and never will: a phantom flight on the
    /// leaderboard's join. So the flight is <i>peeked</i>, and an impact with no live flight is
    /// dropped with a log rather than invented onto one.
    /// </remarks>
    /// <param name="wallMs">Client unix milliseconds to stamp on the flushed windows.</param>
    /// <returns>The envelopes produced.</returns>
    public IReadOnlyList<EventEnvelope> Flush(long wallMs)
    {
        List<EventEnvelope>? envelopes = null;

        foreach (ResolvedImpact impact in _correlator.Drain())
        {
            if (Tracker.PeekFlight(impact.Signal.VehicleId) is not { } flight)
            {
                ModLog.Log.Debug(
                    $"catlog: dropping an impact for vehicle '{impact.Signal.VehicleId}' at sim_t "
                    + $"{impact.Signal.SimT:0.###} — its flight had already ended when the session flushed, "
                    + "and attributing it would mint a flight with no flight.started.");
                continue;
            }

            Add(ref envelopes, EventFactory.FromResolvedImpact(Tracker, impact, flight));
        }

        foreach (ClosedWindow window in _windows.FlushAll())
            Add(ref envelopes, EventFactory.FromWindow(Tracker, window, wallMs));

        return envelopes ?? (IReadOnlyList<EventEnvelope>)Array.Empty<EventEnvelope>();
    }

    private void Dispatch(GameSignal signal, ref List<EventEnvelope>? envelopes)
    {
        switch (signal)
        {
            case FrameBoundarySignal:
                foreach (ResolvedImpact impact in _correlator.EndFrame())
                    Add(ref envelopes, EventFactory.FromResolvedImpact(Tracker, impact));
                break;

            case SessionLoadedSignal loaded:
                OnSessionLoaded(loaded, ref envelopes);
                break;

            case VehicleCreatedSignal created:
                OnVehicleCreated(created, ref envelopes);
                break;

            case VehicleRecoveredSignal recovered:
                EndFlight(recovered.VehicleId, FlightEndReason.Recovered, recovered.CrewCount,
                    recovered.SimT, recovered.WallMs, ref envelopes);
                break;

            case VehicleRemovedSignal removed:
                EndFlight(removed.VehicleId, removed.Reason, removed.CrewCount,
                    removed.SimT, removed.WallMs, ref envelopes);
                break;

            case RudSignal rud:
                // Order matters: tell the correlator first so an impact recorded earlier in this
                // same frame resolves to survived = false.
                _correlator.Destroyed(rud.VehicleId);
                Add(ref envelopes, Vehicle(rud.VehicleId, EventTypes.VehicleRud, rud.SimT, rud.WallMs,
                    new VehicleRudPayload(
                        Cause: EventTypes.ToWire(rud.Cause),
                        PeakG: rud.PeakG,
                        PeakQPa: rud.PeakQPa,
                        SpeedMs: rud.SpeedMs,
                        AltitudeM: rud.AltitudeM,
                        Body: rud.Body,
                        CrewCount: rud.CrewCount)));
                break;

            case ImpactSignal impact:
                _correlator.Impact(impact);
                break;

            case SplashSignal splash:
                _correlator.Splash(splash);
                break;

            case StagingSignal staging:
                Add(ref envelopes, Vehicle(staging.VehicleId, EventTypes.VehicleStaging,
                    staging.SimT, staging.WallMs, new VehicleStagingPayload(staging.StageIndex)));
                break;

            case DockSignal dock:
                Add(ref envelopes, Vehicle(dock.VehicleId, EventTypes.VehicleDocked,
                    dock.SimT, dock.WallMs, new VehicleDockPayload(Tracker.PeekFlight(dock.OtherVehicleId))));
                break;

            case UndockSignal undock:
                Add(ref envelopes, Vehicle(undock.VehicleId, EventTypes.VehicleUndocked,
                    undock.SimT, undock.WallMs, new VehicleDockPayload(Tracker.PeekFlight(undock.OtherVehicleId))));
                break;

            case EngineSignal engine:
                Add(ref envelopes, Vehicle(engine.VehicleId, EventTypes.TypeOf(engine.Kind),
                    engine.SimT, engine.WallMs, new EnginePayload(engine.Engine, engine.Count)));
                break;

            case EvaStartSignal eva:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenEvaStart, Tracker.SessionId,
                    eva.VehicleId is null ? null : Tracker.FlightFor(eva.VehicleId),
                    eva.SimT, eva.WallMs,
                    new KittenEvaStartPayload(Kid(eva.KittenName), Ids.SanitizeName(eva.KittenName))));
                break;

            case EvaEndSignal eva:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenEvaEnd, Tracker.SessionId, flight: null, eva.SimT, eva.WallMs,
                    new KittenEvaEndPayload(
                        Kid(eva.KittenName), Ids.SanitizeName(eva.KittenName), eva.DurationS)));
                break;

            case TumbleSignal tumble:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenTumble, Tracker.SessionId, flight: null, tumble.SimT, tumble.WallMs,
                    new KittenTumblePayload(
                        Kid(tumble.KittenName), Ids.SanitizeName(tumble.KittenName),
                        tumble.SpeedMs, tumble.Body)));
                break;

            case KiaSignal kia:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenKia, Tracker.SessionId, flight: null, kia.SimT, kia.WallMs,
                    new KittenKiaPayload(
                        Kid(kia.KittenName), Ids.SanitizeName(kia.KittenName),
                        EventTypes.ToWire(kia.Context))));
                break;

            case FlaggedSignal flagged:
                OnFlagged(flagged, ref envelopes);
                break;

            case RosterSampleSignal roster:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.RosterSnapshot, Tracker.SessionId, flight: null,
                    roster.SimT, roster.WallMs,
                    EventFactory.RosterPayload(_options.InstallId, roster.Kittens)));
                break;

            default:
                // Unknown signal subtype: ignore rather than throw. Signals arrive from Harmony
                // patch bodies and must never be able to kill the worker.
                ModLog.Log.Debug($"catlog: ignoring unhandled signal {signal.GetType().Name}.");
                break;
        }
    }

    private void OnSessionLoaded(SessionLoadedSignal loaded, ref List<EventEnvelope>? envelopes)
    {
        // A save load is a teardown-and-rebuild boundary in the game: every vehicle is destroyed
        // and re-registered and sim time can jump either way. Start completely fresh rather than
        // diffing across it.
        _detector = new EventDetector();
        _windows = new WindowAccumulator(_options.WindowSeconds);
        _correlator = new ImpactCorrelator();
        _liveVehicles.Clear();
        _sessionFlags.Clear();
        Tracker.NewSession();

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.SessionStarted, Tracker.SessionId, flight: null, loaded.SimT, loaded.WallMs,
            new SessionStartedPayload(loaded.ModVersion, loaded.GameBuild, _options.InstallId)));
    }

    private void OnVehicleCreated(VehicleCreatedSignal created, ref List<EventEnvelope>? envelopes)
    {
        string flight = Tracker.FlightFor(created.VehicleId, created.LaunchGameTime);

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.FlightStarted, Tracker.SessionId, flight, created.SimT, created.WallMs,
            new FlightStartedPayload(
                VehicleName: Ids.SanitizeVehicleName(created.VehicleName),
                Body: created.Body,
                MassKg: created.MassKg,
                PartCount: created.PartCount,
                CrewCount: created.CrewCount)));

        // A session-wide flag (live tuning, a console command) taints every flight in the session,
        // including ones started after the flag was raised.
        foreach (FlightFlag flag in _sessionFlags)
        {
            if (Tracker.AddFlag(created.VehicleId, flag))
            {
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.FlightFlagged, Tracker.SessionId, flight, created.SimT, created.WallMs,
                    new FlightFlaggedPayload(EventTypes.ToWire(flag), "session-wide flag")));
            }
        }
    }

    private void OnFlagged(FlaggedSignal flagged, ref List<EventEnvelope>? envelopes)
    {
        if (flagged.VehicleId is { } vehicleId)
        {
            if (!Tracker.AddFlag(vehicleId, flagged.Flag))
                return;
            Add(ref envelopes, Vehicle(vehicleId, EventTypes.FlightFlagged, flagged.SimT, flagged.WallMs,
                new FlightFlaggedPayload(EventTypes.ToWire(flagged.Flag), flagged.Detail)));
            return;
        }

        // No vehicle: the flag is session-wide. Taint every open flight now, and remember it so
        // flights started later are tainted too.
        _sessionFlags.Add(flagged.Flag);
        foreach (string id in new List<string>(Tracker.ActiveVehicleIds))
        {
            if (!Tracker.AddFlag(id, flagged.Flag))
                continue;
            Add(ref envelopes, Vehicle(id, EventTypes.FlightFlagged, flagged.SimT, flagged.WallMs,
                new FlightFlaggedPayload(EventTypes.ToWire(flagged.Flag), flagged.Detail)));
        }
    }

    private void EndFlight(
        string vehicleId,
        FlightEndReason reason,
        int crewCount,
        double simT,
        long wallMs,
        ref List<EventEnvelope>? envelopes)
    {
        // A destroyed vehicle did not survive whatever it just hit. RudSignal tells the correlator
        // directly, but a *manual* destroy has no RUD — it arrives only as this flight end, from
        // the game's input-apply pass. Without this the correlator's one-frame hold, which exists
        // precisely so a scuttled vehicle is not called a survivor, has nothing to learn from and a
        // player could scuttle after every hard landing to bank a "survived" record.
        if (reason == FlightEndReason.Destroyed)
            _correlator.Destroyed(vehicleId);

        string flight = Tracker.FlightFor(vehicleId);

        // Resolve this vehicle's outstanding impacts here, while the flight id is still live. The
        // verdict cannot change after the flight ends, and leaving them to the next frame boundary
        // would resolve them against a retired flight — which FlightTracker would silently re-mint
        // as a brand-new one with no flight.started. That is the same phantom-flight hazard Flush
        // guards against with peek semantics, on the far more common path: an impact and the
        // destruction it caused land in the same frame every single time.
        foreach (ResolvedImpact impact in _correlator.DrainFor(vehicleId))
            Add(ref envelopes, EventFactory.FromResolvedImpact(Tracker, impact, flight));

        // The last partial window is worth keeping: it covers the seconds immediately before a
        // RUD or a recovery, which is exactly the interesting part.
        if (_windows.Flush(vehicleId) is { } window)
            Add(ref envelopes, EventFactory.FromWindow(Tracker, window, wallMs));

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.FlightEnded, Tracker.SessionId, flight, simT, wallMs,
            new FlightEndedPayload(EventTypes.ToWire(reason), crewCount)));

        Tracker.EndFlight(vehicleId);
        _detector.Forget(vehicleId);
        _windows.Forget(vehicleId);
    }

    private EventEnvelope Vehicle(string vehicleId, string type, double simT, long wallMs, object payload)
        => EventEnvelope.Create(type, Tracker.SessionId, Tracker.FlightFor(vehicleId), simT, wallMs, payload);

    private string Kid(string kittenName) => Ids.KittenId(_options.InstallId, kittenName);

    private void PruneStaleVehicles(TelemetryFrame frame, ref List<EventEnvelope>? envelopes)
    {
        // Cheap guard: only rebuild the live set when the roster size disagrees with what the
        // detector is tracking. The steady state is "same vehicles as last frame".
        if (_detector.TrackedVehicles == frame.Vehicles.Count)
            return;

        _liveVehicles.Clear();
        foreach (TelemetrySnapshot snapshot in frame.Vehicles)
            _liveVehicles.Add(snapshot.VehicleId);

        // A vehicle can leave telemetry without a removal signal (a docking merge, a mid-teardown
        // read that keeps failing). Close its window rather than leaking the partial fold.
        foreach (string gone in _detector.Prune(_liveVehicles))
        {
            if (_windows.Flush(gone) is { } window)
                Add(ref envelopes, EventFactory.FromWindow(Tracker, window, frame.WallMs));
        }
    }

    private static void Add(ref List<EventEnvelope>? envelopes, EventEnvelope envelope)
        => (envelopes ??= []).Add(envelope);
}
