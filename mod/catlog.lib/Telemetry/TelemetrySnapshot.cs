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

    /// <summary>Semi-major axis, in metres.</summary>
    public double SmaM { get; init; }

    /// <summary>Longitude of the ascending node, in degrees.</summary>
    public double LanDeg { get; init; }

    /// <summary>Argument of periapsis, in degrees.</summary>
    public double ArgpDeg { get; init; }

    /// <summary>Time at periapsis, in game seconds.</summary>
    public double TPe { get; init; }

    /// <summary>Orbital period in seconds, or 0 for an unbound trajectory.</summary>
    public double PeriodS { get; init; }

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

    /// <summary>
    /// Altitude above the terrain (or the ocean surface, whichever is higher) directly below, in
    /// metres — or <c>null</c> when the game has no terrain sample for this vehicle.
    /// </summary>
    /// <remarks>
    /// Nullable for the same reason as <see cref="PeakG"/>, and it matters more here: the game only
    /// samples the heightmap under a vehicle that is inside the parent's near-surface physics
    /// radius, so a craft in orbit has <b>no</b> terrain-relative altitude at all. Reporting 0 there
    /// would say "on the ground", which is the exact opposite of the truth. Absent means absent.
    /// </remarks>
    public double? RadarAltM { get; init; }

    /// <summary>
    /// Latitude on the parent body in degrees, or <c>null</c> when it is not readable.
    /// </summary>
    /// <remarks>
    /// Only a <c>Celestial</c> parent has a body-fixed frame to take a latitude in; a vehicle
    /// orbiting another vehicle has none. <b>A zeroed latitude is a real place</b> — the equator —
    /// so an unreadable one is omitted rather than defaulted, and every consumer branches on null.
    /// </remarks>
    public double? Lat { get; init; }

    /// <summary>Longitude on the parent body in degrees, or <c>null</c>. Same rule as <see cref="Lat"/>.</summary>
    public double? Lon { get; init; }

    /// <summary>
    /// Surface-relative descent rate in m/s, <b>positive downwards</b> — the radial component of
    /// the surface-frame velocity, negated so a landing reads as a positive number.
    /// </summary>
    /// <remarks>
    /// Carried on every sample rather than only at touchdown because the landing edge is detected
    /// by a prev/curr comparison on the worker, which sees nothing but snapshots: by the time the
    /// detector knows a landing happened, the game thread is several frames on and cannot be asked
    /// again.
    /// </remarks>
    public double VerticalSpeedMs { get; init; }

    /// <summary>Surface-relative ground-track speed in m/s: the tangential component of the surface-frame velocity.</summary>
    public double HorizontalSpeedMs { get; init; }

    /// <summary>
    /// The universe's simulation speed (time-warp factor) at capture; 1 is real time.
    /// </summary>
    /// <remarks>
    /// Universe-wide, not per-vehicle, but it rides here because the window fold is only ever
    /// handed snapshots. It exists because <c>telemetry.window</c> samples at 2 Hz <b>wall</b> clock
    /// and aggregates over 30 <b>sim</b> seconds: under warp a window closes on far fewer samples
    /// than the nominal 60, and every mean in it is drawn from a thinner sample. Recording the
    /// highest warp seen lets a reader tell a dense window from a smeared one instead of guessing.
    /// Defaults to 1 so a fixture that does not care reads as real time rather than as a stopped
    /// clock.
    /// </remarks>
    public double WarpFactor { get; init; } = 1.0;

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
