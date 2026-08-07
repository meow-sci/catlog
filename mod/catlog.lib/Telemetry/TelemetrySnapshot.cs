namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>
/// The conic class of a vehicle's current orbit, supplied by the game project.
/// </summary>
/// <remarks>
/// <c>catlog.lib</c> is KSA-free, so it cannot call the game's <c>Orbit.IsBound()</c> /
/// <c>IsHyperbolic()</c> / <c>IsParabolic()</c> helpers — but it must not NaN-sniff either:
/// <c>docs/ksa-integration.md</c> B4 establishes that a <b>hyperbolic apoapsis is negative, not
/// NaN</b> (only a parabolic orbit yields NaN). So the discriminator travels on the snapshot,
/// filled in by <c>mod/catlog</c> from those three helpers, and <see cref="Unknown"/> falls back
/// to the eccentricity test for callers (the simulator, hand-built test fixtures) that do not
/// have the game's classifier.
/// </remarks>
public enum OrbitClass
{
    /// <summary>Not supplied — consumers fall back to <c>ecc &lt; 1</c>.</summary>
    Unknown = 0,

    /// <summary>Elliptical or circular: <c>ecc &lt; 1</c>, apoapsis meaningful.</summary>
    Bound = 1,

    /// <summary>Hyperbolic: <c>ecc &gt; 1</c>, apoapsis <b>negative</b>, period NaN.</summary>
    Hyperbolic = 2,

    /// <summary>Parabolic: <c>ecc == 1</c>, apoapsis NaN, semi-major axis +∞.</summary>
    Parabolic = 3,
}

/// <summary>
/// One vehicle's passive telemetry at one sim instant — plain primitives only, no game types.
/// The game project (WP8) fills this in; <c>catlog.lib</c> and <c>catlog.sim</c> only ever read it.
/// </summary>
/// <remarks>
/// <para>
/// Shape follows <c>gatOS/gatOS.SimFs/Snapshots/SimSnapshot.cs</c>: a positional record for the
/// fields every construction site must supply, plus init-only properties for the rest, so adding a
/// field later does not break every existing <c>new TelemetrySnapshot(...)</c> in the test suite.
/// </para>
/// <para>
/// All doubles are finite by contract — the game project scrubs with
/// <see cref="Sanitize"/> at capture time so that no NaN/Infinity token can reach the NDJSON
/// (which would be invalid JSON and earn a <c>400 malformed_batch</c>). The two
/// <see cref="System.Nullable{T}"/> fields are the deliberate exception: see
/// <see cref="PeakG"/>.
/// </para>
/// </remarks>
/// <param name="VehicleId">Stable per-vehicle key. Detector state and flights are keyed by it.</param>
/// <param name="VehicleName">Display name, sanitized to ≤64 printable US-ASCII characters (§4.2).</param>
/// <param name="SimT">Universe sim seconds. May jump backwards across a save load.</param>
/// <param name="WallMs">Client unix milliseconds (untrusted by the server).</param>
/// <param name="Body">Lowercase parent celestial body name — the wire's <c>body</c> field.</param>
/// <param name="Situation">Lowercase situation name; an open set (see <see cref="SituationInfo"/>).</param>
/// <param name="AltitudeM">Barometric altitude above the parent's mean radius, in metres.</param>
/// <param name="SurfaceSpeedMs">Speed relative to the rotating body frame, in m/s.</param>
/// <param name="OrbitalSpeedMs">Speed in the body-centred inertial frame, in m/s.</param>
/// <param name="AccelMs2">Magnitude of body-frame acceleration, in m/s².</param>
/// <param name="MassKg">Total vehicle mass, in kilograms.</param>
public sealed record TelemetrySnapshot(
    string VehicleId,
    string VehicleName,
    double SimT,
    long WallMs,
    string Body,
    string Situation,
    double AltitudeM,
    double SurfaceSpeedMs,
    double OrbitalSpeedMs,
    double AccelMs2,
    double MassKg)
{
    private readonly string? _parentBodyId;

    /// <summary>
    /// The game's parent-body identity key, used for SOI-change detection. Defaults to
    /// <see cref="Body"/> when the game project does not distinguish the two.
    /// </summary>
    public string ParentBodyId
    {
        get => _parentBodyId ?? Body;
        init => _parentBodyId = value;
    }

    /// <summary>Height of the parent's atmosphere above its mean radius, in metres; 0 when airless.</summary>
    public double AtmoHeightM { get; init; }

    /// <summary>True when the parent body has an atmosphere at all.</summary>
    public bool HasAtmosphere => AtmoHeightM > 0;

    /// <summary>Dynamic pressure, in pascals.</summary>
    public double DynPressurePa { get; init; }

    /// <summary>Orbital eccentricity.</summary>
    public double Ecc { get; init; }

    /// <summary>
    /// Apoapsis <b>altitude</b> above the parent's mean radius, in metres — not the game's radius
    /// convention. This is what <c>vehicle.orbit.ap_m</c> carries.
    /// </summary>
    public double ApAltM { get; init; }

    /// <summary>
    /// Periapsis <b>altitude</b> above the parent's mean radius, in metres. This is what
    /// <c>vehicle.orbit.pe_m</c> carries, and what the orbit-achieved rule compares against.
    /// </summary>
    public double PeAltM { get; init; }

    /// <summary>Inclination in <b>degrees</b> — the game stores radians, the wire wants <c>inc_deg</c>.</summary>
    public double IncDeg { get; init; }

    /// <summary>The conic class, supplied by the game project. See <see cref="OrbitClass"/>.</summary>
    public OrbitClass OrbitClass { get; init; } = OrbitClass.Unknown;

    /// <summary>Number of occupied crew seats.</summary>
    public int CrewCount { get; init; }

    /// <summary>Number of parts.</summary>
    public int PartCount { get; init; }

    /// <summary>
    /// Peak g-load this step, or <c>null</c> when the game has no reading.
    /// </summary>
    /// <remarks>
    /// Nullable on purpose. The game's <c>Vehicle.StructuralLoad</c> is written only under full
    /// physics and reset every prepared step, so an all-zero reading from an on-rails or freefall
    /// vehicle means "no data this step", not "zero g" (<c>docs/ksa-integration.md</c> B10).
    /// Reporting zero would corrupt the peak-g board with fake minima, so the window fold omits
    /// <c>peak_g</c> entirely rather than emitting 0.
    /// </remarks>
    public double? PeakG { get; init; }

    /// <summary>Peak dynamic pressure this step in pascals, or <c>null</c> when unavailable. Same rule as <see cref="PeakG"/>.</summary>
    public double? MaxQPa { get; init; }

    /// <summary>True when the situation indicates terrain and/or ocean contact.</summary>
    public bool HasSurfaceContact => SituationInfo.HasSurfaceContact(Situation);

    /// <summary>True when the situation indicates terrain contact.</summary>
    public bool HasTerrainContact => SituationInfo.HasTerrainContact(Situation);

    /// <summary>True when the situation indicates ocean contact.</summary>
    public bool HasOceanContact => SituationInfo.HasOceanContact(Situation);

    /// <summary>True when the situation's on-rails bit is set.</summary>
    public bool IsOnRails => SituationInfo.IsOnRails(Situation);

    /// <summary>
    /// True when this is a closed orbit. Uses <see cref="OrbitClass"/> when the game supplied it,
    /// falling back to a finite <c>ecc &lt; 1</c> otherwise — never a NaN sniff on apoapsis.
    /// </summary>
    public bool IsBoundOrbit => OrbitClass switch
    {
        OrbitClass.Bound => true,
        OrbitClass.Hyperbolic or OrbitClass.Parabolic => false,
        _ => double.IsFinite(Ecc) && Ecc < 1.0,
    };
}
