using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Reflection;
using System.Threading;
using System.Threading.Tasks;
using KSA;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Config;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// Everything catlog owns for one game session: the config, the outbox, the detector pipeline, the
/// shipper, and the two background tasks that drive them. <see cref="Mod"/> is the StarMap shell
/// around this; <see cref="Patcher"/> feeds it signals.
/// </summary>
/// <remarks>
/// <para>
/// <b>The threading contract, in three sentences.</b> The game thread calls <see cref="Tick"/> once
/// per frame and <see cref="Signal"/> from Harmony patch bodies; both are non-blocking and never
/// throw. A single worker task drains <see cref="GameBridge.Signals"/> — the lossless channel — and
/// consumes the latest published <see cref="TelemetryFrame"/> after each frame boundary, running
/// the detector and appending to the outbox. A second task runs
/// <see cref="BatchShipper.RunAsync"/>. Nothing on the game thread ever waits on either.
/// </para>
/// <para>
/// <b>Two SQLite connections, one file.</b> <see cref="OutboxDb"/> holds a single
/// <see cref="Microsoft.Data.Sqlite.SqliteConnection"/>, which is not thread-safe, so the worker
/// and the shipper each open their own handle on the same database. That is precisely the case
/// <c>OutboxDb.Open</c>'s <c>busy_timeout=3000</c> pragma is there for ("covers the shipper and the
/// worker racing on the same file"), and WAL means the worker's appends and the shipper's deletes
/// do not block each other's reads. Pruning is done <b>only</b> by the worker (the shipper is
/// constructed with <c>OutboxCapBytes: 0</c>) so there is exactly one process deleting rows for
/// size, whether or not a shipper exists.
/// </para>
/// </remarks>
public sealed class CatlogRuntime : IDisposable
{
    private const string OutboxSubsystem = "outbox";
    private const string WorkerSubsystem = "worker";
    private const string SamplerSubsystem = "sampler";

    private static readonly TimeSpan WorkerDrainBudget = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan FinalShipBudget = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan PruneInterval = TimeSpan.FromSeconds(30);

    private readonly GameBridge _bridge = new();
    private readonly SubsystemHealth _health = new();
    private readonly PolledSignals _polled = new();
    private readonly CancellationTokenSource _cts = new();

    private readonly List<Vehicle> _vehicleBuffer = [];
    private readonly List<TelemetrySnapshot> _snapshotBuffer = [];
    private readonly List<GameSignal> _signalBuffer = [];

    private readonly ModConfig _config;
    private readonly OutboxDb? _workerOutbox;
    private readonly OutboxDb? _shipperOutbox;
    private readonly Credential? _credential;
    private readonly BatchShipper? _shipper;
    private readonly EventPipeline _pipeline;
    private readonly SampleClock _clock;

    private Task? _workerTask;
    private Task? _shipperTask;
    private long _lastPruneTimestamp;

    private long _eventsAppended;
    private long _pendingEvents;
    private bool _disposed;

    private CatlogRuntime(
        ModConfig config,
        OutboxDb? workerOutbox,
        OutboxDb? shipperOutbox,
        Credential? credential,
        BatchShipper? shipper,
        EventPipeline pipeline,
        string installId,
        string credentialError)
    {
        _config = config;
        _workerOutbox = workerOutbox;
        _shipperOutbox = shipperOutbox;
        _credential = credential;
        _shipper = shipper;
        _pipeline = pipeline;
        _clock = new SampleClock(config.SampleHz);
        InstallId = installId;
        CredentialError = credentialError;
        CollectionEnabled = config.Enabled;
    }

    /// <summary>The catlog mod version, from the assembly rather than a literal that can drift from <c>mod.toml</c>.</summary>
    public static string ModVersion { get; } = ReadModVersion();

    /// <summary>The install ULID.</summary>
    public string InstallId { get; }

    /// <summary>Why no credential is loaded, or an empty string when one is.</summary>
    public string CredentialError { get; }

    /// <summary>The loaded settings.</summary>
    public ModConfig Config => _config;

    /// <summary>Per-subsystem fault latches, rendered by the status window.</summary>
    public SubsystemHealth Health => _health;

    /// <summary>The detector pipeline, for the status window's session/flight readout.</summary>
    public EventPipeline Pipeline => _pipeline;

    /// <summary>The shipper, or null when shipping is not configured.</summary>
    public BatchShipper? Shipper => _shipper;

    /// <summary>The loaded credential, or null.</summary>
    public Credential? Credential => _credential;

    /// <summary>
    /// The in-game master switch. Turning it off stops sampling immediately; already-collected
    /// events still ship, because they are already the player's record of what happened.
    /// </summary>
    public bool CollectionEnabled { get; set; }

    /// <summary>How many envelopes the pipeline has produced and the outbox accepted this session.</summary>
    public long EventsAppended => Interlocked.Read(ref _eventsAppended);

    /// <summary>How many events are waiting to ship, as of the last append.</summary>
    public long PendingEvents => Interlocked.Read(ref _pendingEvents);

    /// <summary>How many vehicles the last sample pass published.</summary>
    public int LastFrameVehicles { get; private set; }

    /// <summary>Timing of the game-thread sample pass.</summary>
    public PerfStat SampleStats { get; } = new();

    /// <summary>
    /// True when the mod is currently collecting. Also false once the outbox or the worker has
    /// latched dead — without the worker check the lossless signal channel would keep accepting
    /// writes that nobody will ever read, which is an unbounded leak dressed up as "still running".
    /// </summary>
    public bool IsCollecting
        => CollectionEnabled
           && _workerOutbox is not null
           && !_health.IsDead(OutboxSubsystem)
           && !_health.IsDead(WorkerSubsystem);

    /// <summary>
    /// Builds the runtime. Never throws: every failure latches a subsystem and is surfaced in the
    /// status window rather than taking down mod loading.
    /// </summary>
    /// <returns>The runtime.</returns>
    public static CatlogRuntime Create()
    {
        ModPaths.EnsureDataDir();
        ModConfig config = ModConfig.LoadOrCreate(ModPaths.ConfigFile);
        string installId = ModPaths.LoadOrCreateInstallId();

        var health = new SubsystemHealth();
        OutboxDb? workerOutbox = null;
        OutboxDb? shipperOutbox = null;
        try
        {
            workerOutbox = OutboxDb.Open(ModPaths.OutboxFile);
            shipperOutbox = OutboxDb.Open(ModPaths.OutboxFile);
        }
        catch (Exception ex)
        {
            workerOutbox?.Dispose();
            shipperOutbox?.Dispose();
            workerOutbox = null;
            shipperOutbox = null;
            health.Fault(OutboxSubsystem, $"could not open '{ModPaths.OutboxFile}': {ex.Message}", ex);
        }

        (Credential? credential, string credentialError) = LoadCredential(config);

        var pipeline = new EventPipeline(new EventPipelineOptions(
            InstallId: installId,
            ModVersion: ModVersion,
            GameBuild: VehicleTelemetry.GameBuild(),
            WindowSeconds: config.WindowS));

        BatchShipper? shipper = null;
        if (credential is not null && shipperOutbox is not null)
        {
            try
            {
                shipper = new BatchShipper(
                    new ShipperOptions(
                        IngestUrl: config.IngestUrl,
                        // The player's cadence knobs: ship_interval_s is the normal trigger
                        // (~60 s), ship_max_pending only the safety valve.
                        PendingTrigger: config.ShipMaxPending,
                        AgeTriggerSeconds: config.ShipIntervalS,
                        // Pruning is the worker's job — exactly one pruner, whether or not a
                        // shipper exists (see the class remarks).
                        OutboxCapBytes: 0),
                    shipperOutbox,
                    credential);
            }
            catch (Exception ex)
            {
                health.Fault("shipper", $"could not start: {ex.Message}", ex);
            }
        }

        var runtime = new CatlogRuntime(
            config, workerOutbox, shipperOutbox, credential, shipper, pipeline, installId, credentialError);
        runtime.CopyHealth(health);
        runtime.Start();
        return runtime;
    }

    /// <summary>
    /// Raises a discrete signal. Called from Harmony patch bodies on the game thread; never blocks
    /// and never throws.
    /// </summary>
    /// <param name="signal">The signal.</param>
    public void Signal(GameSignal signal)
    {
        if (!IsCollecting)
            return;
        _bridge.Signal(signal);
    }

    /// <summary>
    /// Records that the player deliberately destroyed a vehicle, so the next roster diff attributes
    /// the KIA to <see cref="KiaContext.ManualDestroy"/>.
    /// </summary>
    /// <param name="simT">Universe sim seconds.</param>
    public void NoteManualDestroy(double simT) => _polled.NoteManualDestroy(simT);

    /// <summary>
    /// Ensures a vehicle has an open flight before any vehicle-scoped signal is raised for it.
    /// Game thread only. See <see cref="PolledSignals.Track"/> for why this exists.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <param name="into">Receives a <c>flight.started</c> signal if one is due.</param>
    /// <returns>The vehicle id, empty when it could not be read.</returns>
    public string Track(Vehicle vehicle, double simT, long wallMs, List<GameSignal> into)
    {
        if (!IsCollecting)
            return string.Empty;

        try
        {
            return _polled.Track(vehicle, simT, wallMs, into);
        }
        catch (Exception ex)
        {
            _health.Fault(SamplerSubsystem, ex.Message, ex, permanent: false);
            return string.Empty;
        }
    }

    /// <summary>
    /// Drops a vehicle's polled state because the game removed it.
    /// </summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>True when catlog had an open flight for it, i.e. a <c>flight.ended</c> is owed.</returns>
    public bool Forget(string vehicleId) => IsCollecting && _polled.Forget(vehicleId);

    /// <summary>
    /// A save was loaded or a new game started: a hard teardown-and-rebuild boundary. Mints a new
    /// session and drops every piece of cross-frame state.
    /// </summary>
    public void OnSessionBoundary()
    {
        if (!IsCollecting)
            return;

        _polled.Reset();
        _clock.Reset();
        double simT = VehicleTelemetry.SimTimeSeconds();
        _bridge.Signal(new SessionLoadedSignal(
            simT, WallMs(), VehicleTelemetry.GameBuild(), ModVersion));
    }

    /// <summary>
    /// The per-frame game-thread pass: sample at the configured cadence, then close the frame.
    /// </summary>
    /// <remarks>
    /// <para>
    /// <b><see cref="GameBridge.EndFrame"/> is called unconditionally, on every frame</b>, including
    /// frames where the sample clock did not fire. It is what advances
    /// <see cref="ImpactCorrelator"/>; skipping it on unsampled frames delays every <c>survived</c>
    /// verdict indefinitely, and at 2 Hz that would be most frames.
    /// </para>
    /// <para>
    /// <b>Where this runs matters, and the design does not depend on knowing exactly where.</b>
    /// <c>Program.PrepareFrame</c> runs <c>Universe.ApplyVehicleSolvers()</c>
    /// (<c>Program.cs:1912</c>) and then <c>InputEvents.ApplyInputEvents()</c>
    /// (<c>Program.cs:1918</c>) back to back, before any GUI work, so a frame's impacts, its physics
    /// destructions and its manual destroys/recovers all land inside one uninterrupted stretch that
    /// has no mod hook in it. <c>[StarMapBeforeGui]</c> is invoked outside that stretch — the GUI
    /// phase follows it — so whichever side of it the hook falls on, an impact and the destruction
    /// that answers it are never split across two catlog frames, and
    /// <see cref="ImpactCorrelator"/>'s verdict is the same either way. That is the property
    /// <c>docs/ksa-integration.md</c> §3 actually requires; "after solver-apply and input-apply" is
    /// how it is stated, and this satisfies it.
    /// </para>
    /// <para>
    /// It is also why a recovered or destroyed vehicle is naturally absent from this frame's
    /// snapshot list (WP7 requirement 2): <c>Vehicle.Dispose</c> has already deregistered it from
    /// <c>Universe.CurrentSystem.All</c>, so <see cref="VehicleTelemetry.CollectVehicles"/> cannot
    /// see it. A lingering snapshot would re-create detector state and reopen a window on a closed
    /// flight.
    /// </para>
    /// </remarks>
    /// <param name="dt">Frame delta in seconds, as StarMap supplies it.</param>
    public void Tick(double dt)
    {
        if (!IsCollecting)
        {
            _clock.Reset();
            return;
        }

        double simT;
        long wallMs = WallMs();
        try
        {
            simT = VehicleTelemetry.SimTimeSeconds();

            if (_clock.Tick(dt))
            {
                using (SampleStats.Measure())
                    SamplePass(simT, wallMs);

                // A self-healing latch, gatOS's KsaHealth semantics: the window shows "sampler
                // degraded" only while it actually is, and each fault episode logs exactly once.
                _health.Clear(SamplerSubsystem);
            }
        }
        catch (Exception ex)
        {
            _health.Fault(SamplerSubsystem, ex.Message, ex, permanent: false);
            simT = VehicleTelemetry.SimTimeSeconds();
        }

        // Unconditional, even when the sample above threw: the correlator must still see the frame.
        _bridge.EndFrame(simT, wallMs);
    }

    /// <summary>Stops collecting, drains the worker, ships what it can, and closes the outbox.</summary>
    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;

        CollectionEnabled = false;

        // Emit the closing roster snapshot before the channel is completed (§4.2: "and on session
        // end"), then tell the worker no more signals are coming so it drains and flushes.
        try
        {
            var closing = new List<GameSignal>(1);
            _polled.EmitRoster(VehicleTelemetry.SimTimeSeconds(), WallMs(), closing);
            foreach (GameSignal signal in closing)
                _bridge.Signal(signal);
        }
        catch (Exception ex)
        {
            ModLog.Log.Debug($"catlog: the closing roster snapshot failed: {ex.Message}");
        }

        _bridge.Complete();

        try
        {
            if (_workerTask is { } worker && !worker.Wait(WorkerDrainBudget))
                ModLog.Log.Warn("catlog: the worker did not drain within 5 s at unload; some events may be unsaved.");
        }
        catch (Exception ex)
        {
            ModLog.Log.Warn($"catlog: the worker faulted at unload: {ex.Message}");
        }

        // Stop the shipper loop and wait for it BEFORE the final drain. Both use the same
        // SqliteConnection, which is not thread-safe, so the synchronous drain below must be the
        // sole owner of the shipper by the time it runs.
        _cts.Cancel();
        try
        {
            _shipperTask?.Wait(TimeSpan.FromSeconds(2));
        }
        catch (Exception)
        {
            // A cancelled shipper faults its task; that is the expected shutdown path.
        }

        FinalShip();

        _shipper?.Dispose();
        _shipperOutbox?.Dispose();
        _workerOutbox?.Dispose();
        _credential?.Dispose();
        _cts.Dispose();
    }

    private void Start()
    {
        if (_workerOutbox is null)
        {
            ModLog.Log.Error(
                "catlog: the outbox could not be opened, so nothing will be collected this session. "
                + "See the catlog window for the reason.");
            return;
        }

        // session.started for a session that never loads a save (the game already had a system
        // loaded, or the player is in the main menu). A subsequent save load raises its own
        // SessionLoadedSignal, which mints a fresh session id — which is correct, not a duplicate.
        Append([_pipeline.SessionStarted(VehicleTelemetry.SimTimeSeconds(), WallMs())]);

        _workerTask = Task.Run(() => RunWorkerAsync(_cts.Token));

        if (_shipper is not null)
        {
            _shipperTask = Task.Run(() => _shipper.RunAsync(_cts.Token));
            ModLog.Log.Info(
                $"catlog: collecting for handle '{_credential!.Handle}', shipping to {_config.IngestUrl}.");
        }
        else
        {
            ModLog.Log.Info(
                $"catlog: collecting locally; not shipping ({(CredentialError.Length > 0 ? CredentialError : "no shipper")}). "
                + $"Events are spooled in '{ModPaths.OutboxFile}' up to {_config.OutboxCapMb} MB.");
        }
    }

    private void SamplePass(double simT, long wallMs)
    {
        VehicleTelemetry.CollectVehicles(_vehicleBuffer);

        // Signals first, then the frame — the order the worker consumes them in, and the order that
        // puts flight.started before the first telemetry.window of that flight.
        _signalBuffer.Clear();
        _polled.Poll(_vehicleBuffer, simT, wallMs, _signalBuffer);
        foreach (GameSignal signal in _signalBuffer)
            _bridge.Signal(signal);
        _signalBuffer.Clear();

        _snapshotBuffer.Clear();
        foreach (Vehicle vehicle in _vehicleBuffer)
        {
            // Null means "this vehicle could not be read" — omit it, never zero-fill (WP7
            // requirement 7). VehicleTelemetry has already logged once.
            if (VehicleTelemetry.Sample(vehicle, simT, wallMs) is { } snapshot)
                _snapshotBuffer.Add(snapshot);
        }

        LastFrameVehicles = _snapshotBuffer.Count;

        // ToArray, not the buffer: the frame crosses to the worker thread and must be immutable
        // there while this buffer is reused next tick.
        _bridge.PublishFrame(simT, wallMs, _snapshotBuffer.ToArray());
    }

    private async Task RunWorkerAsync(CancellationToken ct)
    {
        long lastFrameSequence = 0;

        try
        {
            while (await _bridge.Signals.WaitToReadAsync(ct).ConfigureAwait(false))
            {
                while (_bridge.Signals.TryRead(out GameSignal? signal))
                {
                    Append(_pipeline.ProcessSignal(signal));

                    // A frame boundary is written after the frame was published, so by the time it
                    // is read the store already holds that frame. Consuming it here — rather than
                    // racing the store on its own — is what preserves "signals before telemetry"
                    // within a frame.
                    if (signal is not FrameBoundarySignal)
                        continue;

                    TelemetryFrame frame = _bridge.Frames.Current;
                    if (frame.Sequence <= lastFrameSequence)
                        continue;

                    lastFrameSequence = frame.Sequence;
                    Append(_pipeline.ProcessFrame(frame));
                }

                MaybePrune();
            }
        }
        catch (OperationCanceledException)
        {
            // Unload. Fall through to the flush.
        }
        catch (Exception ex)
        {
            _health.Fault(WorkerSubsystem, ex.Message, ex);
            ModLog.Log.Error("catlog: the detector worker stopped; nothing further will be recorded.", ex);
        }

        try
        {
            // Close partial windows and resolve impacts still awaiting a verdict. Flush uses peek
            // semantics, so an impact whose flight already ended is dropped rather than attached to
            // a freshly minted phantom flight.
            Append(_pipeline.Flush(WallMs()));
        }
        catch (Exception ex)
        {
            ModLog.Log.Warn($"catlog: the final pipeline flush failed: {ex.Message}");
        }
    }

    private void Append(IReadOnlyList<EventEnvelope> envelopes)
    {
        if (envelopes.Count == 0 || _workerOutbox is null || _health.IsDead(OutboxSubsystem))
            return;

        try
        {
            _workerOutbox.Append(envelopes);
            Interlocked.Add(ref _eventsAppended, envelopes.Count);
            Interlocked.Exchange(ref _pendingEvents, _workerOutbox.PendingCount);
        }
        catch (Exception ex)
        {
            _health.Fault(OutboxSubsystem, ex.Message, ex);
            ModLog.Log.Error(
                "catlog: the outbox stopped accepting events; collection is disabled for this session.", ex);
        }
    }

    private void MaybePrune()
    {
        if (_workerOutbox is null || _config.OutboxCapBytes <= 0 || _health.IsDead(OutboxSubsystem))
            return;

        long now = Stopwatch.GetTimestamp();
        if (_lastPruneTimestamp != 0 && now - _lastPruneTimestamp < Stopwatch.Frequency * PruneInterval.TotalSeconds)
            return;

        _lastPruneTimestamp = now;
        try
        {
            _workerOutbox.Prune(_config.OutboxCapBytes);
        }
        catch (Exception ex)
        {
            _health.Fault(OutboxSubsystem, ex.Message, ex);
        }
    }

    // A bounded best-effort drain at unload: the game is closing, but a player who just finished a
    // flight should not have to relaunch to see it on the board. ConsecutiveFailures is the ceiling
    // — which only works because it is now advanced by ShipOnceAsync itself.
    private void FinalShip()
    {
        if (_shipper is null || _shipper.IsDead)
            return;

        long deadline = Stopwatch.GetTimestamp() + (long)(Stopwatch.Frequency * FinalShipBudget.TotalSeconds);
        try
        {
            while (Stopwatch.GetTimestamp() < deadline && _shipper.ConsecutiveFailures < 3)
            {
                ShipAttempt attempt = _shipper.ShipOnceAsync(CancellationToken.None).GetAwaiter().GetResult();
                if (attempt.Outcome is ShipOutcome.NothingToShip or ShipOutcome.Fatal)
                    return;
            }
        }
        catch (Exception ex)
        {
            ModLog.Log.Debug($"catlog: the final ship attempt failed: {ex.Message}");
        }
    }

    private void CopyHealth(SubsystemHealth source)
    {
        foreach (SubsystemFault fault in source.Snapshot())
            _health.Fault(fault.Subsystem, fault.Error, exception: null, fault.Permanent);
    }

    private static (Credential?, string) LoadCredential(ModConfig config)
    {
        if (!config.CanShip(out string reason))
            return (null, reason);

        CredentialLoadResult result = Credential.Load(config.CredentialPath);
        if (!result.Ok)
        {
            ModLog.Log.Error($"catlog: the credential could not be loaded, so nothing will ship: {result.Error}");
            return (null, result.Error);
        }

        return (result.Credential, string.Empty);
    }

    private static long WallMs() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

    private static string ReadModVersion()
    {
        try
        {
            string? informational = typeof(CatlogRuntime).Assembly
                .GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion;
            if (!string.IsNullOrEmpty(informational))
            {
                // Strip the source-revision suffix the SDK appends ("0.1.0+abcdef0").
                int plus = informational.IndexOf('+');
                return plus > 0 ? informational[..plus] : informational;
            }

            return typeof(CatlogRuntime).Assembly.GetName().Version?.ToString(3) ?? "0.0.0";
        }
        catch (Exception)
        {
            return "0.0.0";
        }
    }
}
