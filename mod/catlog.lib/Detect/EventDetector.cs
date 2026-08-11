using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>The detector's event kinds. Debounce is tracked per (vehicle, kind).</summary>
public enum DetectKind
{
    /// <summary><c>vehicle.situation</c>.</summary>
    Situation = 0,

    /// <summary><c>vehicle.atmosphere</c> with <c>dir: "entered"</c>.</summary>
    AtmosphereEntered = 1,

    /// <summary><c>vehicle.atmosphere</c> with <c>dir: "exited"</c>.</summary>
    AtmosphereExited = 2,

    /// <summary><c>vehicle.orbit</c> with <c>phase: "achieved"</c>.</summary>
    OrbitAchieved = 3,

    /// <summary><c>vehicle.orbit</c> with <c>phase: "escaped"</c>.</summary>
    OrbitEscaped = 4,

    /// <summary><c>vehicle.soi</c>.</summary>
    SoiChange = 5,

    /// <summary>
    /// <c>vehicle.landed</c> — the surface-contact half of the same edge <see cref="Situation"/>
    /// fires on.
    /// </summary>
    /// <remarks>
    /// This kind deliberately has <b>no debounce timer of its own</b>: it is emitted from inside
    /// the situation rule, gated by that rule's timer, so <see cref="VehicleDetectState.CanFire"/>
    /// is never called with it. A landing carries a <see cref="LandingObservation"/> in
    /// <see cref="DetectedEvent.Payload"/> rather than a finished payload record, because
    /// <c>survived</c> is not knowable yet — see <see cref="ImpactCorrelator"/>.
    /// </remarks>
    Landing = 6,
}

/// <summary>A detected event, before it is given ids and turned into an <see cref="EventEnvelope"/>.</summary>
/// <param name="VehicleId">The vehicle it belongs to.</param>
/// <param name="Kind">Which rule fired.</param>
/// <param name="Type">The <see cref="EventTypes"/> name.</param>
/// <param name="Payload">The payload record.</param>
/// <param name="SimT">Universe sim seconds.</param>
/// <param name="WallMs">Client unix milliseconds.</param>
public sealed record DetectedEvent(
    string VehicleId,
    DetectKind Kind,
    string Type,
    object Payload,
    double SimT,
    long WallMs);

/// <summary>
/// Per-vehicle detector memory. Owned by <see cref="EventDetector"/>, pruned when a vehicle goes
/// away.
/// </summary>
/// <remarks>
/// The comparator itself is state-external (the shape unscience's <c>EventDetector</c> uses): all
/// mutable memory lives here so a single detector instance serves every vehicle. Unlike
/// unscience's eight hand-written <c>LastXxxTimeSec</c> fields, debounce timestamps are a fixed
/// array indexed by <see cref="DetectKind"/> — catlog has more kinds and per-field timers do not
/// scale.
/// </remarks>
public sealed class VehicleDetectState
{
    private static readonly int KindCount = Enum.GetValues<DetectKind>().Length;

    private readonly double[] _lastFire = NewTimers();

    /// <summary>The last snapshot observed, or null before the first one.</summary>
    public TelemetrySnapshot? Previous { get; internal set; }

    /// <summary>The situation last reported on the wire; null until the baseline sample.</summary>
    public string? ReportedSituation { get; internal set; }

    /// <summary>The parent body last reported on the wire; null until the baseline sample.</summary>
    public string? ReportedParentBodyId { get; internal set; }

    /// <summary>The latched atmosphere state (the Schmitt trigger's output); null until seeded.</summary>
    public bool? InAtmosphere { get; internal set; }

    /// <summary>Whether periapsis was last seen above the orbit-achieved threshold; null until seeded.</summary>
    public bool? OrbitAchieved { get; internal set; }

    /// <summary>Whether the orbit was last seen closed; null until seeded.</summary>
    public bool? BoundOrbit { get; internal set; }

    /// <summary>True when a debounce window for <paramref name="kind"/> has elapsed.</summary>
    /// <param name="kind">The event kind.</param>
    /// <param name="simT">Current sim seconds.</param>
    /// <returns>True when the kind may fire now.</returns>
    public bool CanFire(DetectKind kind, double simT)
        => simT - _lastFire[(int)kind] > Wire.DetectorDebounceSeconds;

    /// <summary>Records that <paramref name="kind"/> just fired.</summary>
    /// <param name="kind">The event kind.</param>
    /// <param name="simT">Current sim seconds.</param>
    public void MarkFired(DetectKind kind, double simT) => _lastFire[(int)kind] = simT;

    /// <summary>
    /// Drops every latch and timer. Used when sim time jumps backwards (a save was loaded), which
    /// makes the previous sample meaningless and every debounce timer a future timestamp.
    /// </summary>
    public void Rebaseline()
    {
        Previous = null;
        ReportedSituation = null;
        ReportedParentBodyId = null;
        InAtmosphere = null;
        OrbitAchieved = null;
        BoundOrbit = null;
        Array.Copy(NewTimers(), _lastFire, KindCount);
    }

    // double.MinValue seeding means "never fired" always passes the strict > comparison,
    // without a nullable.
    private static double[] NewTimers()
    {
        double[] timers = new double[KindCount];
        Array.Fill(timers, double.MinValue);
        return timers;
    }
}

/// <summary>
/// The prev/curr comparator: turns a stream of <see cref="TelemetrySnapshot"/>s into the five
/// polled event families of §4.2 (situation, atmosphere, orbit achieved/escaped, SOI).
/// </summary>
/// <remarks>
/// <para>
/// Runs on the worker, never the game thread. Every rule is edge-triggered off a <b>latch</b>
/// rather than off the raw previous snapshot, which is what makes the 2-second per-(vehicle, kind)
/// debounce rate-limiting rather than lossy: a transition suppressed by debounce is re-detected on
/// the next sample and reported from the last state that actually reached the wire.
/// </para>
/// <para>
/// The first sample for a vehicle is a <b>baseline</b>: it seeds the latches and emits nothing.
/// That is what stops an event storm at session load, and what makes a save reload (sim time
/// jumping backwards) safe — the state is rebaselined instead of diffed.
/// </para>
/// </remarks>
public sealed class EventDetector
{
    private readonly Dictionary<string, VehicleDetectState> _states = new(StringComparer.Ordinal);

    /// <summary>How many vehicles currently have detector state.</summary>
    public int TrackedVehicles => _states.Count;

    /// <summary>The state bag for <paramref name="vehicleId"/>, creating it on first sight.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>The mutable per-vehicle state.</returns>
    public VehicleDetectState StateFor(string vehicleId)
    {
        if (!_states.TryGetValue(vehicleId, out VehicleDetectState? state))
        {
            state = new VehicleDetectState();
            _states[vehicleId] = state;
        }

        return state;
    }

    /// <summary>Drops detector state for a vehicle that has left the simulation.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    public void Forget(string vehicleId) => _states.Remove(vehicleId);

    /// <summary>Drops detector state for every vehicle not in <paramref name="live"/>.</summary>
    /// <param name="live">The vehicle ids still present.</param>
    /// <returns>The vehicle ids that were dropped, so the caller can close their windows too.</returns>
    public IReadOnlyList<string> Prune(ICollection<string> live)
    {
        if (_states.Count == 0)
            return [];

        List<string>? stale = null;
        foreach (string id in _states.Keys)
        {
            if (!live.Contains(id))
                (stale ??= []).Add(id);
        }

        if (stale is null)
            return [];
        foreach (string id in stale)
            _states.Remove(id);
        return stale;
    }

    /// <summary>Runs every rule over one frame's worth of snapshots.</summary>
    /// <param name="frame">The frame.</param>
    /// <returns>The events detected, in vehicle then rule order.</returns>
    public IReadOnlyList<DetectedEvent> Observe(TelemetryFrame frame)
    {
        List<DetectedEvent>? events = null;
        foreach (TelemetrySnapshot snapshot in frame.Vehicles)
            Observe(snapshot, ref events);
        return events ?? (IReadOnlyList<DetectedEvent>)Array.Empty<DetectedEvent>();
    }

    /// <summary>Runs every rule over one vehicle's snapshot.</summary>
    /// <param name="snapshot">The snapshot.</param>
    /// <returns>The events detected for this vehicle.</returns>
    public IReadOnlyList<DetectedEvent> Observe(TelemetrySnapshot snapshot)
    {
        List<DetectedEvent>? events = null;
        Observe(snapshot, ref events);
        return events ?? (IReadOnlyList<DetectedEvent>)Array.Empty<DetectedEvent>();
    }

    private void Observe(TelemetrySnapshot curr, ref List<DetectedEvent>? sink)
    {
        VehicleDetectState state = StateFor(curr.VehicleId);

        // A save load rewinds Universe sim time. Diffing across that boundary would report a
        // teleport-sized situation change and leave every debounce timer in the future.
        if (state.Previous is { } previous && curr.SimT < previous.SimT)
            state.Rebaseline();

        bool baseline = state.Previous is null;

        CheckSituation(state, curr, baseline, ref sink);
        CheckSoi(state, curr, baseline, ref sink);
        CheckAtmosphere(state, curr, baseline, ref sink);
        CheckOrbitAchieved(state, curr, baseline, ref sink);
        CheckOrbitEscaped(state, curr, baseline, ref sink);

        state.Previous = curr;
    }

    private static void CheckSituation(
        VehicleDetectState state, TelemetrySnapshot curr, bool baseline, ref List<DetectedEvent>? sink)
    {
        if (baseline || state.ReportedSituation is null)
        {
            state.ReportedSituation = curr.Situation;
            return;
        }

        if (string.Equals(state.ReportedSituation, curr.Situation, StringComparison.Ordinal))
            return;
        if (!state.CanFire(DetectKind.Situation, curr.SimT))
            return;

        string from = state.ReportedSituation;

        Add(ref sink, new DetectedEvent(
            curr.VehicleId,
            DetectKind.Situation,
            EventTypes.VehicleSituation,
            new VehicleSituationPayload(
                From: from,
                To: curr.Situation,
                Body: curr.Body,
                AltitudeM: curr.AltitudeM,
                SurfaceSpeedMs: curr.SurfaceSpeedMs,
                OrbitalSpeedMs: curr.OrbitalSpeedMs,
                RadarAltM: curr.RadarAltM),
            curr.SimT,
            curr.WallMs));

        // A landing is this same edge seen from the other side, so it is detected here and nowhere
        // else. It inherits the situation rule's 2 s debounce by construction — it is emitted
        // inside the `CanFire` gate above and never marks a timer of its own — and that is the
        // behaviour we want, in both directions:
        //
        //   * a landing suppressed by debounce is not lost. The latch is only advanced when the
        //     situation event actually fires, so the edge is still pending on the next sample and
        //     both events are emitted then, off the same `from`.
        //   * a craft chattering between `freefall` and `landed` at 2 Hz — a bouncing lander, a
        //     rover on rough ground — would otherwise mint a landing every 500 ms, each one a
        //     record.
        //
        // Giving it its own timer would also let it fire on a transition whose `vehicle.situation`
        // was suppressed, leaving a `vehicle.landed` in the log with no situation change beside it
        // to explain it.
        if (IsTouchdown(from, curr.Situation))
        {
            Add(ref sink, new DetectedEvent(
                curr.VehicleId,
                DetectKind.Landing,
                EventTypes.VehicleLanded,
                new LandingObservation(
                    VehicleId: curr.VehicleId,
                    SimT: curr.SimT,
                    WallMs: curr.WallMs,
                    Body: curr.Body,
                    VerticalSpeedMs: curr.VerticalSpeedMs,
                    HorizontalSpeedMs: curr.HorizontalSpeedMs,
                    CrewCount: curr.CrewCount,
                    RadarAltM: curr.RadarAltM,
                    Lat: curr.Lat,
                    Lon: curr.Lon),
                curr.SimT,
                curr.WallMs));
        }

        state.ReportedSituation = curr.Situation;
        state.MarkFired(DetectKind.Situation, curr.SimT);
    }

    /// <summary>
    /// True when the transition is from a contact-free situation into one that touches terrain or
    /// ocean.
    /// </summary>
    /// <remarks>
    /// Both sides must be <b>known</b> situations. <see cref="SituationInfo"/> is total by design
    /// and reports "no contact" for a name it has never seen, so without the
    /// <see cref="SituationInfo.IsKnown"/> test a ninth situation added by a future build would
    /// read as flight and every transition out of it would score as a landing. Not knowing is not
    /// the same as being airborne.
    /// </remarks>
    private static bool IsTouchdown(string from, string to)
        => SituationInfo.IsKnown(from)
           && SituationInfo.IsKnown(to)
           && !SituationInfo.HasSurfaceContact(from)
           && SituationInfo.HasSurfaceContact(to);

    private static void CheckSoi(
        VehicleDetectState state, TelemetrySnapshot curr, bool baseline, ref List<DetectedEvent>? sink)
    {
        // A blank parent id means the read failed, not that the vehicle left every SOI. Never
        // report a transition to or from "".
        if (string.IsNullOrEmpty(curr.ParentBodyId))
            return;

        if (baseline || string.IsNullOrEmpty(state.ReportedParentBodyId))
        {
            state.ReportedParentBodyId = curr.ParentBodyId;
            return;
        }

        if (string.Equals(state.ReportedParentBodyId, curr.ParentBodyId, StringComparison.Ordinal))
            return;
        if (!state.CanFire(DetectKind.SoiChange, curr.SimT))
            return;

        string fromBody = state.Previous?.Body ?? state.ReportedParentBodyId;
        Add(ref sink, new DetectedEvent(
            curr.VehicleId,
            DetectKind.SoiChange,
            EventTypes.VehicleSoi,
            new VehicleSoiPayload(FromBody: fromBody, ToBody: curr.Body),
            curr.SimT,
            curr.WallMs));

        state.ReportedParentBodyId = curr.ParentBodyId;
        state.MarkFired(DetectKind.SoiChange, curr.SimT);
    }

    private static void CheckAtmosphere(
        VehicleDetectState state, TelemetrySnapshot curr, bool baseline, ref List<DetectedEvent>? sink)
    {
        double atmoHeight = curr.AtmoHeightM;
        bool hasAtmosphere = atmoHeight > 0;

        if (baseline || state.InAtmosphere is null)
        {
            state.InAtmosphere = hasAtmosphere && curr.AltitudeM < atmoHeight;
            return;
        }

        bool inside = state.InAtmosphere.Value;

        // The hysteresis band depends on which side of it we are currently latched to — that is
        // the whole point of a Schmitt trigger, and why this reads the latch rather than the
        // previous snapshot. A bare threshold makes a vehicle hovering at the boundary flap, and
        // the 2 s debounce only rate-limits the alternation, it does not suppress it.
        if (!inside)
        {
            if (!hasAtmosphere)
                return;
            if (curr.AltitudeM >= atmoHeight * (1.0 - Wire.AtmosphereHysteresis))
                return;
            if (!state.CanFire(DetectKind.AtmosphereEntered, curr.SimT))
                return;

            Add(ref sink, Atmosphere(curr, "entered", DetectKind.AtmosphereEntered));
            state.InAtmosphere = true;
            state.MarkFired(DetectKind.AtmosphereEntered, curr.SimT);
            return;
        }

        // Leaving the atmosphere by changing to an airless parent counts as an exit.
        bool exited = !hasAtmosphere || curr.AltitudeM > atmoHeight * (1.0 + Wire.AtmosphereHysteresis);
        if (!exited)
            return;
        if (!state.CanFire(DetectKind.AtmosphereExited, curr.SimT))
            return;

        Add(ref sink, Atmosphere(curr, "exited", DetectKind.AtmosphereExited));
        state.InAtmosphere = false;
        state.MarkFired(DetectKind.AtmosphereExited, curr.SimT);
    }

    private static DetectedEvent Atmosphere(TelemetrySnapshot curr, string dir, DetectKind kind)
        => new(
            curr.VehicleId,
            kind,
            EventTypes.VehicleAtmosphere,
            new VehicleAtmospherePayload(
                Dir: dir,
                Body: curr.Body,
                SpeedMs: curr.SurfaceSpeedMs,
                DynPressurePa: curr.DynPressurePa),
            curr.SimT,
            curr.WallMs);

    private static void CheckOrbitAchieved(
        VehicleDetectState state, TelemetrySnapshot curr, bool baseline, ref List<DetectedEvent>? sink)
    {
        // §7.2: ecc < 1 && pe_alt > atmo_height + 1000. On an airless body atmo_height is 0, so
        // the margin alone is the bar. Note there is no NaN sniff on apoapsis anywhere here —
        // hyperbolic apoapsis is negative, not NaN (docs/ksa-integration.md B4); the conic class
        // comes from TelemetrySnapshot.IsBoundOrbit.
        double safeAltitude = curr.AtmoHeightM + Wire.OrbitAchievedMarginM;
        bool above = curr.IsBoundOrbit && curr.PeAltM > safeAltitude;

        if (baseline || state.OrbitAchieved is null)
        {
            state.OrbitAchieved = above;
            return;
        }

        if (state.OrbitAchieved.Value)
        {
            // Falling back below the bar is not an event; it just re-arms the rising edge.
            if (!above)
                state.OrbitAchieved = false;
            return;
        }

        if (!above)
            return;
        if (!state.CanFire(DetectKind.OrbitAchieved, curr.SimT))
            return;

        Add(ref sink, Orbit(curr, "achieved", DetectKind.OrbitAchieved));
        state.OrbitAchieved = true;
        state.MarkFired(DetectKind.OrbitAchieved, curr.SimT);
    }

    private static void CheckOrbitEscaped(
        VehicleDetectState state, TelemetrySnapshot curr, bool baseline, ref List<DetectedEvent>? sink)
    {
        bool bound = curr.IsBoundOrbit;

        if (baseline || state.BoundOrbit is null)
        {
            state.BoundOrbit = bound;
            return;
        }

        if (!state.BoundOrbit.Value)
        {
            if (bound)
                state.BoundOrbit = true;
            return;
        }

        if (bound)
            return;
        if (!state.CanFire(DetectKind.OrbitEscaped, curr.SimT))
            return;

        Add(ref sink, Orbit(curr, "escaped", DetectKind.OrbitEscaped));
        state.BoundOrbit = false;
        state.MarkFired(DetectKind.OrbitEscaped, curr.SimT);
    }

    private static DetectedEvent Orbit(TelemetrySnapshot curr, string phase, DetectKind kind)
        => new(
            curr.VehicleId,
            kind,
            EventTypes.VehicleOrbit,
            new VehicleOrbitPayload(
                Phase: phase,
                Body: curr.Body,
                ApM: curr.ApAltM,
                PeM: curr.PeAltM,
                Ecc: curr.Ecc,
                IncDeg: curr.IncDeg,
                SmaM: curr.SmaM,
                LanDeg: curr.LanDeg,
                ArgpDeg: curr.ArgpDeg,
                TPe: curr.TPe,
                PeriodS: curr.IsBoundOrbit ? curr.PeriodS : 0.0,
                MassKg: curr.MassKg),
            curr.SimT,
            curr.WallMs);

    // Lazily allocated: the steady state is "no events", and this runs at 2 Hz per vehicle.
    private static void Add(ref List<DetectedEvent>? sink, DetectedEvent evt) => (sink ??= []).Add(evt);
}
