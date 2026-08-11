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
/// <param name="CareerId">
/// Career id for the save being played (§4.1 <c>career</c>). Null derives a stable per-install
/// career, which is the correct answer for any caller that has no concept of a KSA save — one such
/// caller has exactly one career, forever.
/// </param>
/// <param name="Types">
/// Which event types may be emitted (§7.2 <c>[events]</c>). Null means all of them, which is the
/// right answer for every caller that has no config file — the simulator, the load generator and
/// the tests. A filter here cannot suppress <see cref="EventTypes.AlwaysReported"/> whatever it
/// names; see <see cref="EventTypeFilter"/>.
/// </param>
public sealed record EventPipelineOptions(
    string InstallId,
    string ModVersion = "0.1.0",
    string GameBuild = "unknown",
    string? SessionId = null,
    double WindowSeconds = Wire.TelemetryWindowSeconds,
    string? CareerId = null,
    EventTypeFilter? Types = null);

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
    private readonly EventTypeFilter _types;
    private readonly HashSet<FlightFlag> _sessionFlags = [];
    private readonly HashSet<string> _liveVehicles = new(StringComparer.Ordinal);

    // Kitten roster name -> the flight of the vehicle whose crew was just killed, captured while
    // that flight was still open. Read by the kitten.kia attribution below; see CrewKilledSignal.
    private readonly Dictionary<string, PendingCrewKill> _crewKills = new(StringComparer.Ordinal);

    // Kitten roster name -> its EVA vehicle id, for the kittens currently outside. Only the
    // eva_start/eva_end pair proves an id belongs to a KittenEva rather than to a rocket a player
    // happened to name after a kitten, and a wrong flight attribution disqualifies an innocent
    // record — so this map, not a bare lookup of the name in the tracker.
    private readonly Dictionary<string, string> _evaVehicles = new(StringComparer.Ordinal);

    private EventDetector _detector = new();
    private WindowAccumulator _windows;
    private ImpactCorrelator _correlator = new();

    /// <summary>
    /// How long a <see cref="CrewKilledSignal"/> may be used to attribute a <c>kitten.kia</c>, in
    /// sim seconds. The roster flag flips inside <c>KillCrew</c> and the diff that notices it runs
    /// on the next sample tick, so the two are always within one poll interval of each other; the
    /// window is the same 2.0 s the game-side context labelling uses (<c>PolledSignals</c>), and it
    /// is short enough that an unrelated later death cannot borrow the record.
    /// </summary>
    private const double CrewKillWindowSeconds = 2.0;

    /// <summary>Creates a pipeline.</summary>
    /// <param name="options">Construction parameters.</param>
    public EventPipeline(EventPipelineOptions options)
    {
        _options = options;
        _types = options.Types ?? EventTypeFilter.All;
        _windows = new WindowAccumulator(options.WindowSeconds);
        Tracker = new FlightTracker(options.InstallId, options.SessionId, options.CareerId);
    }

    /// <summary>The identity bookkeeper. Exposed for the simulator and the status window.</summary>
    public FlightTracker Tracker { get; }

    /// <summary>The emission filter in force. Exposed so the status window can report it.</summary>
    public EventTypeFilter Types => _types;

    /// <summary>The current session ULID.</summary>
    public string SessionId => Tracker.SessionId;

    /// <summary>The current career id (§4.1).</summary>
    public string CareerId => Tracker.CareerId;

    /// <summary>Timing for the per-frame detect + window pass.</summary>
    public PerfStat FrameStats { get; } = new();

    /// <summary>Timing for the per-signal pass.</summary>
    public PerfStat SignalStats { get; } = new();

    /// <summary>
    /// The <c>session.started</c> event. Session-boundary callers normally use
    /// <see cref="ProcessSignal(GameSignal)"/> so the system catalogue can precede it.
    /// </summary>
    /// <remarks>
    /// The one path that returns an envelope without going through <see cref="Add"/>, and so the
    /// one path the <c>[events]</c> filter does not see. That is safe precisely because
    /// <c>session.started</c> is in <see cref="EventTypes.AlwaysReported"/>: there is no
    /// configuration that could have suppressed it, so there is nothing here to check. If that
    /// list ever loses <c>session.started</c>, this method has to grow a filter test.
    /// </remarks>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <returns>The envelope.</returns>
    public EventEnvelope SessionStarted(double simT, long wallMs)
        => EventEnvelope.Create(
            EventTypes.SessionStarted,
            Tracker.SessionId,
            Tracker.CareerId,
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
        {
            // A landing is the one detector output that is not yet an event: `survived` is not
            // knowable until a frame has passed without a destruction, so it goes into the same
            // hold `vehicle.impact` uses and is emitted from there. Everything else is final the
            // moment it is detected.
            if (detected.Kind == DetectKind.Landing && detected.Payload is LandingObservation landing)
            {
                _correlator.Landed(landing);
                continue;
            }

            Add(ref envelopes, EventFactory.FromDetected(Tracker, detected));
        }

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

        Verdicts outstanding = _correlator.Drain();

        foreach (ResolvedImpact impact in outstanding.Impacts)
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

        foreach (ResolvedLanding landing in outstanding.Landings)
        {
            if (Tracker.PeekFlight(landing.Landing.VehicleId) is not { } flight)
            {
                ModLog.Log.Debug(
                    $"catlog: dropping a landing for vehicle '{landing.Landing.VehicleId}' at sim_t "
                    + $"{landing.Landing.SimT:0.###} — same reason as an outstanding impact: its flight "
                    + "had already ended when the session flushed.");
                continue;
            }

            Add(ref envelopes, EventFactory.FromResolvedLanding(Tracker, landing, flight));
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
                Verdicts settled = _correlator.EndFrame();
                foreach (ResolvedImpact impact in settled.Impacts)
                    Add(ref envelopes, EventFactory.FromResolvedImpact(Tracker, impact));
                foreach (ResolvedLanding landing in settled.Landings)
                {
                    // FlightFor, as for impacts: a landing is only detected from a vehicle that was
                    // in the published frame, and PolledSignals.Track has already emitted that
                    // vehicle's flight.started ahead of the frame, so this resolves rather than
                    // mints. The flight-end and session-flush paths, where that is not true, peek.
                    Add(ref envelopes, EventFactory.FromResolvedLanding(
                        Tracker, landing, Tracker.FlightFor(landing.Landing.VehicleId)));
                }

                break;

            case SessionLoadedSignal loaded:
                OnSessionLoaded(loaded, ref envelopes);
                break;

            case VehicleCreatedSignal created:
                OnVehicleCreated(created, ref envelopes);
                break;

            case VehicleRecoveredSignal recovered:
                EndFlight(
                    recovered.VehicleId, FlightEndReason.Recovered, recovered.CrewCount,
                    recovered.Body, recovered.KittenNames, recovered.Lat, recovered.Lon,
                    recovered.SimT, recovered.WallMs, ref envelopes);
                break;

            case VehicleRemovedSignal removed:
                EndFlight(
                    removed.VehicleId, removed.Reason, removed.CrewCount,
                    removed.Body, removed.KittenNames, removed.Lat, removed.Lon,
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
                        CrewCount: rud.CrewCount,
                        PartCount: rud.PartCount,
                        Lat: rud.Lat,
                        Lon: rud.Lon)));
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
                if (eva.VehicleId is { } evaVehicleId)
                    _evaVehicles[eva.KittenName] = evaVehicleId;
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenEvaStart, Tracker.SessionId, Tracker.CareerId,
                    eva.VehicleId is null ? null : Tracker.FlightFor(eva.VehicleId),
                    eva.SimT, eva.WallMs,
                    new KittenEvaStartPayload(Kid(eva.KittenName), Ids.SanitizeName(eva.KittenName))));
                break;

            case EvaEndSignal eva:
                _evaVehicles.Remove(eva.KittenName);
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenEvaEnd, Tracker.SessionId, Tracker.CareerId, flight: null, eva.SimT, eva.WallMs,
                    new KittenEvaEndPayload(
                        Kid(eva.KittenName), Ids.SanitizeName(eva.KittenName), eva.DurationS)));
                break;

            case TumbleSignal tumble:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenTumble, Tracker.SessionId, Tracker.CareerId,
                    // A tumbling kitten IS a vehicle — a KittenEva whose Vehicle.Id is her roster
                    // name — so the tumble belongs to that EVA flight, and it has to: `tuning`
                    // flags the flight, and a flag can only exclude events that name one. The
                    // signal's source polls the vehicle it has already registered, so the flight is
                    // open by the time this runs.
                    //
                    // Peek, not FlightFor: minting here would attach the tumble to a flight ULID
                    // that has no flight.started and never will — the phantom flight Flush's peek
                    // semantics exist to prevent — and a tumble is exactly the event a phantom
                    // would be minted for, since it can arrive from a vehicle whose id could not be
                    // read. A null flight scores as it always did; an invented one poisons a join.
                    Tracker.PeekFlight(tumble.KittenName),
                    tumble.SimT, tumble.WallMs,
                    new KittenTumblePayload(
                        Kid(tumble.KittenName), Ids.SanitizeName(tumble.KittenName),
                        tumble.From, tumble.SpeedMs, tumble.Body)));
                break;

            case CrewKilledSignal killed:
                OnCrewKilled(killed);
                break;

            case KiaSignal kia:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.KittenKia, Tracker.SessionId, Tracker.CareerId, FlightForKia(kia),
                    kia.SimT, kia.WallMs,
                    new KittenKiaPayload(
                        Kid(kia.KittenName), Ids.SanitizeName(kia.KittenName),
                        EventTypes.ToWire(kia.Context))));
                break;

            case FlaggedSignal flagged:
                OnFlagged(flagged, ref envelopes);
                break;

            case RosterSampleSignal roster:
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.RosterSnapshot, Tracker.SessionId, Tracker.CareerId, flight: null,
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
        _crewKills.Clear();
        _evaVehicles.Clear();
        Tracker.NewSession(loaded.CareerId);

        if (loaded.System is null)
            return;

        SystemSnapshot system = loaded.System;
        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.SystemDiscovered, Tracker.SessionId, Tracker.CareerId, flight: null,
            loaded.SimT, loaded.WallMs,
            new SystemDiscoveredPayload(
                system.SystemId, system.Id, system.Name, system.HomeBody,
                system.BodyCount, loaded.SystemComplete)));

        if (loaded.IncludeSystemBodies)
        {
            foreach (SystemBodySnapshot body in system.Bodies)
            {
                bool shape = IsFinite(body.SemiMajorAxisM)
                    && IsFinite(body.Eccentricity)
                    && IsFinite(body.InclinationDeg)
                    && IsFinite(body.LongitudeAscendingNodeDeg)
                    && IsFinite(body.ArgumentPeriapsisDeg)
                    && IsFinite(body.TimeAtPeriapsis);
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.SystemBody, Tracker.SessionId, Tracker.CareerId, flight: null,
                    loaded.SimT, loaded.WallMs,
                    new SystemBodyPayload(
                        system.SystemId, body.Body, body.Name, body.Class, body.Kind, body.Rank,
                        body.Parent, body.RadiusM, body.MassKg, body.SoiM, body.AtmoM, body.OceanM,
                        body.AngularVelocityRadS, body.AxisCce, body.CcfToCceT0,
                        shape ? body.SemiMajorAxisM : null,
                        shape ? body.Eccentricity : null,
                        shape ? body.InclinationDeg : null,
                        shape ? body.LongitudeAscendingNodeDeg : null,
                        shape ? body.ArgumentPeriapsisDeg : null,
                        shape ? body.TimeAtPeriapsis : null,
                        IsFinite(body.PeriodS) ? body.PeriodS : null)));
            }
        }

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.SessionStarted, Tracker.SessionId, Tracker.CareerId, flight: null, loaded.SimT, loaded.WallMs,
            new SessionStartedPayload(loaded.ModVersion, loaded.GameBuild, _options.InstallId)));
    }

    private static bool IsFinite(double? value) => value.HasValue && double.IsFinite(value.Value);

    private void OnVehicleCreated(VehicleCreatedSignal created, ref List<EventEnvelope>? envelopes)
    {
        string flight = Tracker.FlightFor(created.VehicleId, created.LaunchGameTime);

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.FlightStarted, Tracker.SessionId, Tracker.CareerId, flight, created.SimT, created.WallMs,
            new FlightStartedPayload(
                VehicleName: Ids.SanitizeVehicleName(created.VehicleName),
                Body: created.Body,
                MassKg: created.MassKg,
                PartCount: created.PartCount,
                CrewCount: created.CrewCount,
                Kids: Kids(created.KittenNames),
                StageCount: created.StageCount,
                EngineCount: created.EngineCount,
                Lat: created.Lat,
                Lon: created.Lon)));

        // A session-wide flag (live tuning, a console command) taints every flight in the session,
        // including ones started after the flag was raised.
        foreach (FlightFlag flag in _sessionFlags)
        {
            if (Tracker.AddFlag(created.VehicleId, flag))
            {
                Add(ref envelopes, EventEnvelope.Create(
                    EventTypes.FlightFlagged, Tracker.SessionId, Tracker.CareerId, flight, created.SimT, created.WallMs,
                    new FlightFlaggedPayload(EventTypes.ToWire(flag), "session-wide flag")));
            }
        }
    }

    private void OnCrewKilled(CrewKilledSignal killed)
    {
        // Nothing to remember if the vehicle has no open flight: a crew kill on a vehicle catlog
        // never saw start would only let the KIA below name a flight the server cannot join.
        if (Tracker.PeekFlight(killed.VehicleId) is not { } flight)
            return;

        // The map is keyed by kitten and only ever holds the crew of the last few kills, but a
        // session that scuttles crewed vehicles all day would still grow it without this: an entry
        // older than the window can never be used again.
        if (_crewKills.Count > 0)
        {
            foreach (string name in new List<string>(_crewKills.Keys))
            {
                if (Math.Abs(killed.SimT - _crewKills[name].SimT) > CrewKillWindowSeconds)
                    _crewKills.Remove(name);
            }
        }

        foreach (string name in killed.KittenNames)
            _crewKills[name] = new PendingCrewKill(flight, killed.SimT);
    }

    /// <summary>
    /// The flight a <c>kitten.kia</c> belongs to, or null when the mod cannot name one honestly.
    /// </summary>
    /// <remarks>
    /// <para>
    /// The KIA itself comes from a roster diff (MOD-046) and so knows a name and a time, nothing
    /// else. Two things can turn that into a flight, and both are exact rather than inferred:
    /// </para>
    /// <list type="number">
    /// <item>
    /// A <see cref="CrewKilledSignal"/> for this kitten inside the window. <c>Vehicle.KillCrew</c>
    /// is the only writer of the roster's <c>Kia</c> flag (D11, <c>docs/ksa-integration.md</c> §4)
    /// and the patch reads the seats at that instant, so a kitten named there died aboard that
    /// vehicle. This is the path that fires in a real game.
    /// </item>
    /// <item>
    /// The kitten is outside right now, and a kitten outside is a vehicle of her own whose id is
    /// her roster name — her EVA flight is hers and nobody else's. A fallback for a KIA that
    /// reaches the roster by some route <c>KillCrew</c> does not cover.
    /// </item>
    /// </list>
    /// <para>
    /// Otherwise: null, deliberately. A kitten who died aboard a vehicle whose crew kill catlog did
    /// not see — a future build that flags KIA some other way, a seat whose roster entry could not
    /// be resolved, a crew kill more than <see cref="CrewKillWindowSeconds"/> before the diff — is
    /// not attributable, and guessing at the flight would disqualify an innocent flight's impact
    /// record under the ±2 s window. A missed disqualification is recoverable; a wrongly voided
    /// record is not.
    /// </para>
    /// </remarks>
    /// <param name="kia">The signal.</param>
    /// <returns>The flight ULID, or null.</returns>
    private string? FlightForKia(KiaSignal kia)
    {
        if (_crewKills.Remove(kia.KittenName, out PendingCrewKill pending))
        {
            if (Math.Abs(kia.SimT - pending.SimT) <= CrewKillWindowSeconds)
                return pending.FlightId;
        }

        if (_evaVehicles.TryGetValue(kia.KittenName, out string? vehicleId))
            return Tracker.PeekFlight(vehicleId);

        return null;
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
        string body,
        IReadOnlyList<string>? kittenNames,
        double? lat,
        double? lon,
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
        Verdicts due = _correlator.DrainFor(vehicleId);
        foreach (ResolvedImpact impact in due.Impacts)
            Add(ref envelopes, EventFactory.FromResolvedImpact(Tracker, impact, flight));

        // A landing that has not yet cleared the hold when the flight ends is settled here for the
        // same reason an impact is — and this is the path that carries the whole point of routing
        // landings through the correlator at all: the destruction below has already told it, so a
        // craft scuttled where it stood reports survived: false rather than banking the touchdown.
        foreach (ResolvedLanding landing in due.Landings)
            Add(ref envelopes, EventFactory.FromResolvedLanding(Tracker, landing, flight));

        // The last partial window is worth keeping: it covers the seconds immediately before a
        // RUD or a recovery, which is exactly the interesting part.
        if (_windows.Flush(vehicleId) is { } window)
            Add(ref envelopes, EventFactory.FromWindow(Tracker, window, wallMs));

        Add(ref envelopes, EventEnvelope.Create(
            EventTypes.FlightEnded, Tracker.SessionId, Tracker.CareerId, flight, simT, wallMs,
            new FlightEndedPayload(
                Reason: EventTypes.ToWire(reason),
                CrewCount: crewCount,
                Kids: Kids(kittenNames),
                Body: string.IsNullOrEmpty(body) ? "unknown" : body,
                Lat: lat,
                Lon: lon)));

        Tracker.EndFlight(vehicleId);
        _detector.Forget(vehicleId);
        _windows.Forget(vehicleId);
    }

    private EventEnvelope Vehicle(string vehicleId, string type, double simT, long wallMs, object payload)
        => EventEnvelope.Create(type, Tracker.SessionId, Tracker.CareerId, Tracker.FlightFor(vehicleId), simT, wallMs, payload);

    private string Kid(string kittenName) => Ids.KittenId(_options.InstallId, kittenName);

    /// <summary>
    /// The <c>kids</c> array for a crew list: the same per-install relabelling every other kitten
    /// field uses, and an empty array — never a null and never a missing key — when there is nobody
    /// aboard or the seats could not be read.
    /// </summary>
    /// <param name="kittenNames">The roster names, or null.</param>
    /// <returns>The pseudonymous ids, in the order given.</returns>
    private IReadOnlyList<string> Kids(IReadOnlyList<string>? kittenNames)
    {
        if (kittenNames is null || kittenNames.Count == 0)
            return [];

        var kids = new string[kittenNames.Count];
        for (int i = 0; i < kittenNames.Count; i++)
            kids[i] = Kid(kittenNames[i]);
        return kids;
    }

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

    /// <summary>
    /// The single funnel every produced envelope passes through, and therefore the one place the
    /// <c>[events]</c> filter belongs.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Suppression here is late on purpose. C# evaluates the argument before the call, so by the
    /// time this runs the flight ULID has been minted, the flag has been recorded on the tracker,
    /// the window has closed and the impact has been resolved. Dropping the envelope costs one
    /// wasted ULID and nothing else — no detector state is rewound, so a disabled type cannot
    /// change what the *other* types say. Filtering in <c>Dispatch</c> would skip the case body and
    /// take that bookkeeping with it.
    /// </para>
    /// <para>
    /// The <see cref="EventTypes.IsAlwaysReported"/> test is deliberately redundant with
    /// <see cref="EventTypeFilter.Create"/>'s: this is the choke point, so this is where the
    /// guarantee has to be readable, not two files away.
    /// </para>
    /// </remarks>
    private void Add(ref List<EventEnvelope>? envelopes, EventEnvelope envelope)
    {
        if (!EventTypes.IsAlwaysReported(envelope.Type) && !_types.IsEnabled(envelope.Type))
            return;

        (envelopes ??= []).Add(envelope);
    }

    private readonly record struct PendingCrewKill(string FlightId, double SimT);
}
