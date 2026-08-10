using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim;

/// <summary>
/// A celestial body as the simulator needs to know it: a name for the wire and an atmosphere
/// height for the detector's boundary rules.
/// </summary>
/// <param name="Name">Lowercase body name — opaque to the server (§4.2).</param>
/// <param name="AtmoHeightM">Atmosphere height above the mean radius, in metres; 0 when airless.</param>
public sealed record SimBody(string Name, double AtmoHeightM);

/// <summary>The bodies the canonical scenarios fly around.</summary>
/// <remarks>
/// The names match the ones <c>server/internal/seed</c> already uses, so the demo dataset and the
/// simulated dataset speak the same vocabulary. The atmosphere heights are the numbers the
/// detector's rules are exercised against — <c>vehicle.atmosphere</c> fires at ±2 % of
/// <see cref="SimBody.AtmoHeightM"/> and <c>vehicle.orbit</c> needs a periapsis 1 km above it
/// (§7.2) — so they are chosen to be plausible and, above all, explicit.
/// </remarks>
public static class SimBodies
{
    /// <summary>The home world: thick atmosphere, launch site, oceans.</summary>
    public static SimBody Kerbin { get; } = new("kerbin", 70_000);

    /// <summary>The airless moon.</summary>
    public static SimBody Mun { get; } = new("mun", 0);

    /// <summary>The thin-atmosphere neighbour.</summary>
    public static SimBody Duna { get; } = new("duna", 50_000);
}

/// <summary>
/// A mutable vehicle model the scenarios steer frame by frame. <see cref="Sample"/> renders its
/// current state as the immutable <see cref="TelemetrySnapshot"/> the game project would build.
/// </summary>
/// <remarks>
/// This exists so a scenario reads like a flight profile ("climb, throttle up, exit the
/// atmosphere") rather than like a list of constructor arguments, and so every snapshot in a run
/// is internally consistent: one place decides that a vehicle sitting on the pad is
/// <c>landed</c> with zero speed and a periapsis below the surface.
/// </remarks>
public sealed class SimVehicle
{
    /// <summary>Creates a vehicle sitting on the launch pad.</summary>
    /// <param name="id">Stable vehicle id; flights and detector state are keyed by it.</param>
    /// <param name="name">Display name.</param>
    /// <param name="body">The parent body.</param>
    /// <param name="crewCount">Occupied seats.</param>
    /// <param name="partCount">Part count.</param>
    /// <param name="massKg">Total mass, in kilograms.</param>
    public SimVehicle(string id, string name, SimBody body, int crewCount, int partCount, double massKg)
    {
        Id = id;
        Name = name;
        Body = body;
        CrewCount = crewCount;
        PartCount = partCount;
        MassKg = massKg;

        // On the pad: touching terrain, on rails, and on a conic whose periapsis is deep inside
        // the body — which is what stops the orbit-achieved latch seeding true at t0.
        Situation = "landed";
        PeAltM = -body.AtmoHeightM - 500_000;
        ApAltM = 0;
        Ecc = 0.9;
        OrbitClass = OrbitClass.Bound;
    }

    /// <summary>The vehicle id.</summary>
    public string Id { get; }

    /// <summary>The display name.</summary>
    public string Name { get; }

    /// <summary>The parent body. Assigning it moves the vehicle's SOI.</summary>
    public SimBody Body { get; set; }

    /// <summary>Occupied seats.</summary>
    public int CrewCount { get; set; }

    /// <summary>Part count.</summary>
    public int PartCount { get; set; }

    /// <summary>Total mass, in kilograms.</summary>
    public double MassKg { get; set; }

    /// <summary>Lowercase situation name (see <see cref="SituationInfo"/>).</summary>
    public string Situation { get; set; }

    /// <summary>Barometric altitude above the parent's mean radius, in metres.</summary>
    public double AltitudeM { get; set; }

    /// <summary>Speed in the rotating body frame, in m/s.</summary>
    public double SurfaceSpeedMs { get; set; }

    /// <summary>Speed in the body-centred inertial frame, in m/s.</summary>
    public double OrbitalSpeedMs { get; set; }

    /// <summary>Body-frame acceleration magnitude, in m/s².</summary>
    public double AccelMs2 { get; set; }

    /// <summary>Dynamic pressure, in pascals.</summary>
    public double DynPressurePa { get; set; }

    /// <summary>Orbital eccentricity.</summary>
    public double Ecc { get; set; }

    /// <summary>Apoapsis altitude above the mean radius, in metres.</summary>
    public double ApAltM { get; set; }

    /// <summary>Periapsis altitude above the mean radius, in metres.</summary>
    public double PeAltM { get; set; }

    /// <summary>Inclination, in degrees.</summary>
    public double IncDeg { get; set; }

    /// <summary>Semi-major axis, in metres.</summary>
    public double SmaM { get; set; }

    /// <summary>Longitude of the ascending node, in degrees.</summary>
    public double LanDeg { get; set; }

    /// <summary>Argument of periapsis, in degrees.</summary>
    public double ArgpDeg { get; set; }

    /// <summary>Time at periapsis, in game seconds.</summary>
    public double TPe { get; set; }

    /// <summary>Orbital period in seconds, or 0 for an unbound trajectory.</summary>
    public double PeriodS { get; set; }

    /// <summary>The conic class the game project would report.</summary>
    public OrbitClass OrbitClass { get; set; }

    /// <summary>
    /// Peak g-load, or null when there is no reading. Null is the on-rails and freefall case: the
    /// game only writes <c>StructuralLoad</c> under full physics, and reporting 0 would put fake
    /// minima on the <c>peak_g_survived</c> board (<c>docs/ksa-integration.md</c> B10).
    /// </summary>
    public double? PeakG { get; set; }

    /// <summary>Peak dynamic pressure, in pascals, or null when there is no reading.</summary>
    public double? MaxQPa { get; set; }

    /// <summary>Renders the current state as a snapshot.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>The snapshot the game project would have built.</returns>
    public TelemetrySnapshot Sample(double simT) => new(
        VehicleId: Id,
        VehicleName: Name,
        SimT: simT,
        WallMs: SimClock.Wall(simT),
        Body: Body.Name,
        Situation: Situation,
        AltitudeM: AltitudeM,
        SurfaceSpeedMs: SurfaceSpeedMs,
        OrbitalSpeedMs: OrbitalSpeedMs,
        AccelMs2: AccelMs2,
        MassKg: MassKg)
    {
        ParentBodyId = Body.Name,
        AtmoHeightM = Body.AtmoHeightM,
        DynPressurePa = DynPressurePa,
        Ecc = Ecc,
        ApAltM = ApAltM,
        PeAltM = PeAltM,
        IncDeg = IncDeg,
        SmaM = SmaM,
        LanDeg = LanDeg,
        ArgpDeg = ArgpDeg,
        TPe = TPe,
        PeriodS = PeriodS,
        OrbitClass = OrbitClass,
        CrewCount = CrewCount,
        PartCount = PartCount,
        PeakG = PeakG,
        MaxQPa = MaxQPa,
    };

    /// <summary>Puts the vehicle in powered atmospheric flight, under full physics.</summary>
    /// <param name="altitudeM">Altitude, in metres.</param>
    /// <param name="surfaceSpeedMs">Surface-relative speed, in m/s.</param>
    /// <param name="accelMs2">Acceleration magnitude, in m/s².</param>
    /// <param name="dynPressurePa">Dynamic pressure, in pascals.</param>
    public void Fly(double altitudeM, double surfaceSpeedMs, double accelMs2, double dynPressurePa)
    {
        Situation = "maneuvering";
        AltitudeM = altitudeM;
        SurfaceSpeedMs = surfaceSpeedMs;
        OrbitalSpeedMs = surfaceSpeedMs;
        AccelMs2 = accelMs2;
        DynPressurePa = dynPressurePa;
        PeakG = accelMs2 / 9.81;
        MaxQPa = dynPressurePa;
    }

    /// <summary>Puts the vehicle on a closed orbit, on rails.</summary>
    /// <param name="apAltM">Apoapsis altitude, in metres.</param>
    /// <param name="peAltM">Periapsis altitude, in metres.</param>
    /// <param name="orbitalSpeedMs">Inertial speed, in m/s.</param>
    /// <param name="surfaceSpeedMs">Surface-relative speed, in m/s.</param>
    public void Orbit(double apAltM, double peAltM, double orbitalSpeedMs, double surfaceSpeedMs)
    {
        Situation = "freefall";
        AltitudeM = peAltM;
        ApAltM = apAltM;
        PeAltM = peAltM;
        Ecc = apAltM <= peAltM ? 0.0 : (apAltM - peAltM) / (apAltM + peAltM + 2 * 600_000);
        SmaM = 600_000 + ((apAltM + peAltM) * 0.5);
        PeriodS = 2 * System.Math.PI * SmaM / System.Math.Max(1, orbitalSpeedMs);
        OrbitClass = OrbitClass.Bound;
        OrbitalSpeedMs = orbitalSpeedMs;
        SurfaceSpeedMs = surfaceSpeedMs;
        AccelMs2 = 0;
        DynPressurePa = 0;

        // On rails there is no structural-load reading at all, which is exactly the case
        // TelemetrySnapshot.PeakG is nullable for.
        PeakG = null;
        MaxQPa = null;
    }

    /// <summary>Puts the vehicle on the surface, stationary.</summary>
    /// <param name="situation">The surface situation, e.g. <c>landed</c> or <c>floating</c>.</param>
    public void Rest(string situation)
    {
        Situation = situation;
        AltitudeM = 0;
        SurfaceSpeedMs = 0;
        OrbitalSpeedMs = 0;
        AccelMs2 = 0;
        DynPressurePa = 0;
        PeakG = null;
        MaxQPa = null;
    }
}
