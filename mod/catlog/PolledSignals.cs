using System;
using System.Collections.Generic;
using KSA;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog;

/// <summary>
/// The signals the game does not announce and that cannot be recovered from a
/// <see cref="Lib.Telemetry.TelemetrySnapshot"/> either: kitten tumbles, engine ignition/shutdown/
/// flameout, KIA, and the live-tuning integrity flag. Polled on the game thread once per sample
/// tick and raised through the same lossless channel the Harmony patches use.
/// </summary>
/// <remarks>
/// <para>
/// These are edge detectors over per-vehicle state, deliberately separate from
/// <see cref="Lib.Detect.EventDetector"/>: that one runs on the worker over the published frame and
/// only sees fields carried on the snapshot, whereas these read game state the snapshot has no
/// field for (locomotion mode, engine globals, roster rows). Putting them here keeps
/// <c>catlog.lib</c>'s snapshot contract from growing a column per game subsystem.
/// </para>
/// <para>
/// Every read goes through <see cref="VehicleTelemetry"/>; nothing in this file touches a KSA
/// member directly, so a game bump breaks the build in one file, not two.
/// </para>
/// </remarks>
public sealed class PolledSignals
{
    /// <summary>Sim seconds between periodic <c>roster.snapshot</c> events (§4.2: every 10 min of play).</summary>
    public const double RosterIntervalSeconds = 600.0;

    // Attributing a KIA to a manual destroy needs the two facts to be close in time: KillCrew runs
    // in the input-apply pass and the roster row is already flipped by the time this poll sees it,
    // so a window of a couple of sim seconds covers the frame gap without swallowing a later,
    // unrelated death.
    private const double ManualDestroyWindowSeconds = 2.0;

    private readonly Dictionary<string, VehicleState> _vehicles = new(StringComparer.Ordinal);
    private readonly Dictionary<string, bool> _kia = new(StringComparer.Ordinal);
    private readonly HashSet<string> _live = new(StringComparer.Ordinal);

    // Reused across ticks, like _live: the KIA scan runs at the sample rate and must not allocate.
    private readonly List<RosterKia> _rosterKia = [];

    private double _lastRosterSimT = double.NegativeInfinity;
    private double _lastManualDestroySimT = double.NegativeInfinity;
    private bool _rosterSeeded;
    private bool _tuningFlagged;

    /// <summary>
    /// Records that the player deliberately destroyed a vehicle, so the next roster diff can
    /// attribute the resulting KIA correctly.
    /// </summary>
    /// <remarks>
    /// <c>docs/ksa-integration.md</c> §4: <c>Vehicle.KillCrew()</c> has exactly one caller — the
    /// <c>!Recovered</c> branch of the manual-destroy path — and it is the only thing in the entire
    /// game that sets <c>Kia = true</c>. A physics RUD never reaches it.
    /// </remarks>
    /// <param name="simT">Universe sim seconds when the destroy was applied.</param>
    public void NoteManualDestroy(double simT) => _lastManualDestroySimT = simT;

    /// <summary>Forgets everything. Called at a save load, which rebuilds the whole world.</summary>
    public void Reset()
    {
        _vehicles.Clear();
        _kia.Clear();
        _lastRosterSimT = double.NegativeInfinity;
        _lastManualDestroySimT = double.NegativeInfinity;
        _rosterSeeded = false;
        _tuningFlagged = false;
    }

    /// <summary>
    /// Ensures a vehicle has an open flight, emitting <c>flight.started</c> the first time it is
    /// seen. Called both by the sample pass and — before any vehicle-scoped signal — by
    /// <see cref="Patcher"/>.
    /// </summary>
    /// <remarks>
    /// Without this, a vehicle created and destroyed inside one 0.5 s sample interval would produce
    /// a <c>vehicle.rud</c> and a <c>flight.ended</c> against a flight ULID that has no
    /// <c>flight.started</c> — a phantom flight the server's join can never resolve. Registering
    /// lazily at the first signal, in the same batch and ahead of it, closes that hole for every
    /// signal source at once.
    /// </remarks>
    /// <param name="vehicle">The vehicle.</param>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <param name="into">The list to append a <see cref="VehicleCreatedSignal"/> to, if one is due.</param>
    /// <returns>The vehicle id, or an empty string when it could not be read.</returns>
    public string Track(Vehicle vehicle, double simT, long wallMs, List<GameSignal> into)
    {
        string id = VehicleTelemetry.IdOf(vehicle);
        if (id.Length == 0 || _vehicles.ContainsKey(id))
            return id;

        _vehicles[id] = Observe(vehicle);
        into.Add(new VehicleCreatedSignal(
            simT,
            wallMs,
            id,
            id, // KSA has no display name separate from the id.
            VehicleTelemetry.BodyOf(vehicle),
            VehicleTelemetry.MassKg(vehicle),
            VehicleTelemetry.PartCount(vehicle),
            VehicleTelemetry.CrewCount(vehicle),
            VehicleTelemetry.LaunchGameTime(vehicle)));
        return id;
    }

    /// <summary>
    /// Drops a vehicle's tracked state because the game removed it, so the poll's silent-removal
    /// safety net does not report it a second time.
    /// </summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>True when the vehicle was being tracked — i.e. when a <c>flight.ended</c> is owed.</returns>
    public bool Forget(string vehicleId) => _vehicles.Remove(vehicleId);

    /// <summary>
    /// Polls one tick and appends whatever it found to <paramref name="into"/>.
    /// </summary>
    /// <param name="vehicles">The live vehicles, as collected by <see cref="VehicleTelemetry.CollectVehicles"/>.</param>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <param name="into">The list to append signals to.</param>
    public void Poll(IReadOnlyList<Vehicle> vehicles, double simT, long wallMs, List<GameSignal> into)
    {
        CheckTuning(simT, wallMs, into);

        _live.Clear();
        foreach (Vehicle vehicle in vehicles)
        {
            string id = Track(vehicle, simT, wallMs, into);
            if (id.Length == 0)
                continue;
            _live.Add(id);
            PollVehicle(vehicle, id, simT, wallMs, into);
        }

        Prune(simT, wallMs, into);
        PollRoster(simT, wallMs, into);
    }

    /// <summary>Emits a final <c>roster.snapshot</c>, for session end.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <param name="wallMs">Client unix milliseconds.</param>
    /// <param name="into">The list to append to.</param>
    public void EmitRoster(double simT, long wallMs, List<GameSignal> into)
    {
        IReadOnlyList<RosterKitten> roster = VehicleTelemetry.SampleRoster();
        if (roster.Count > 0)
            into.Add(new RosterSampleSignal(simT, wallMs, roster));
        _lastRosterSimT = simT;
    }

    private void PollVehicle(Vehicle vehicle, string id, double simT, long wallMs, List<GameSignal> into)
    {
        // Track() has just seeded the state for a vehicle seen for the first time, and that seed is
        // a baseline that emits nothing (WP7 requirement 4): a vehicle already present at save-load
        // must not fire a spurious ignition or tumble on the frame it is discovered.
        if (!_vehicles.TryGetValue(id, out VehicleState state))
            return;

        VehicleState now = Observe(vehicle);

        // Tumble: count transitions INTO Tumbling only. A tumble ends Tumbling → Rightening →
        // Grounded, so counting transitions out would double-count via Rightening
        // (docs/ksa-integration.md §5).
        if (now.Locomotion == LocomotionMode.Tumbling && state.Locomotion != LocomotionMode.Tumbling)
        {
            into.Add(new TumbleSignal(
                simT, wallMs, id, VehicleTelemetry.GroundSpeedMs(vehicle), VehicleTelemetry.BodyOf(vehicle)));
        }

        if (now.EngineActive != state.EngineActive)
        {
            (string engine, int count) = VehicleTelemetry.ActiveEngines(vehicle);
            into.Add(new EngineSignal(
                simT,
                wallMs,
                id,
                now.EngineActive ? EngineEventKind.Ignition : EngineEventKind.Shutdown,
                engine.Length == 0 ? "unknown" : engine,
                now.EngineActive ? Math.Max(1, count) : Math.Max(1, state.EngineCount)));
        }
        else if (now.EngineActive && state.EnginePropellant && !now.EnginePropellant)
        {
            // The game has no flameout concept (B3); this is its own predicate,
            // IsActive && !IsPropellantAvailable, at whole-vehicle granularity.
            (string engine, int count) = VehicleTelemetry.ActiveEngines(vehicle);
            into.Add(new EngineSignal(
                simT, wallMs, id, EngineEventKind.Flameout,
                engine.Length == 0 ? "unknown" : engine, Math.Max(1, count)));
        }

        _vehicles[id] = now;
    }

    private static VehicleState Observe(Vehicle vehicle)
    {
        (string _, int count) = VehicleTelemetry.ActiveEngines(vehicle);
        return new VehicleState(
            VehicleTelemetry.LocomotionMode(vehicle),
            VehicleTelemetry.IsAnyEngineActive(vehicle),
            VehicleTelemetry.IsAnyEnginePropellantAvailable(vehicle),
            count);
    }

    // The silent-removal safety net. Vehicle.Dispose is the game's single removal choke point and
    // the Harmony prefix on it calls Forget, so anything still tracked but absent from the roster
    // left some other way — a CelestialSystem.Rename (deregister → rename → register, which is not
    // a dispose) or a docking merge whose consumed vehicle we missed. Ending the flight as
    // "despawned" is better than leaking an open flight that never closes.
    private void Prune(double simT, long wallMs, List<GameSignal> into)
    {
        if (_vehicles.Count == _live.Count)
            return;

        List<string>? gone = null;
        foreach (string id in _vehicles.Keys)
        {
            if (!_live.Contains(id))
                (gone ??= []).Add(id);
        }

        if (gone is null)
            return;

        foreach (string id in gone)
        {
            _vehicles.Remove(id);
            into.Add(new VehicleRemovedSignal(simT, wallMs, id, FlightEndReason.Despawned, 0));
        }
    }

    private void CheckTuning(double simT, long wallMs, List<GameSignal> into)
    {
        if (_tuningFlagged)
            return;

        float gate = VehicleTelemetry.TumbleSpeedGate();
        if (gate.Equals(VehicleTelemetry.StockTumbleSpeedGate))
            return;

        _tuningFlagged = true;
        into.Add(new FlaggedSignal(
            simT,
            wallMs,
            VehicleId: null, // session-wide: it taints every flight, including ones started later
            FlightFlag.Tuning,
            $"KittenLocomotionTuning.Current.TumbleSpeedGate is {gate.ToString("0.###", System.Globalization.CultureInfo.InvariantCulture)}, "
            + $"stock is {VehicleTelemetry.StockTumbleSpeedGate.ToString("0.###", System.Globalization.CultureInfo.InvariantCulture)}"));
    }

    // Two cadences, two reads. The KIA diff below runs on every sample tick and must stay that
    // responsive — a death that took ten minutes to notice would be attributed to the wrong flight
    // and, past the manual-destroy window, to the wrong cause. The roster.snapshot PAYLOAD is due
    // once every RosterIntervalSeconds, so it is built only when it is about to be emitted; the
    // scan that feeds the diff carries no payload fields and allocates nothing.
    private void PollRoster(double simT, long wallMs, List<GameSignal> into)
    {
        VehicleTelemetry.SampleRosterKia(_rosterKia);
        if (_rosterKia.Count == 0)
            return;

        bool manualDestroyNearby = simT - _lastManualDestroySimT <= ManualDestroyWindowSeconds;

        foreach (RosterKia kitten in _rosterKia)
        {
            bool wasKia = _kia.TryGetValue(kitten.Name, out bool previous) && previous;
            _kia[kitten.Name] = kitten.Kia;

            // The first roster read is a baseline: a save that already contains KIA kittens must
            // not replay their deaths (WP7 requirement 4).
            if (!_rosterSeeded || wasKia || !kitten.Kia)
                continue;

            into.Add(new KiaSignal(
                simT,
                wallMs,
                kitten.Name,
                manualDestroyNearby ? KiaContext.ManualDestroy : KiaContext.Unknown));
        }

        _rosterSeeded = true;

        if (simT - _lastRosterSimT < RosterIntervalSeconds)
            return;

        // Now, and only now, pay for the full payload read.
        IReadOnlyList<RosterKitten> roster = VehicleTelemetry.SampleRoster();
        if (roster.Count == 0)
            return;

        _lastRosterSimT = simT;
        into.Add(new RosterSampleSignal(simT, wallMs, roster));
    }

    private readonly record struct VehicleState(
        LocomotionMode? Locomotion,
        bool EngineActive,
        bool EnginePropellant,
        int EngineCount);
}
