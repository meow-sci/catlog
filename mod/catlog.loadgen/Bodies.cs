using System;
using System.Collections.Generic;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// How far a body is from the launch site, in career terms: which stage can reach it and how long
/// getting there takes.
/// </summary>
internal enum BodyReach
{
    /// <summary>The launch site itself.</summary>
    Home,

    /// <summary>The star. Nothing targets it; everything leaving the home SOI passes through it.</summary>
    Star,

    /// <summary>A moon of the home world — the first thing anyone leaves for.</summary>
    Moon,

    /// <summary>An inner planet or one of its moons: a real transfer window.</summary>
    Inner,

    /// <summary>The outer system. Probe territory.</summary>
    Outer,
}

/// <summary>
/// A celestial body with enough physics attached that a flight profile around it can be plausible
/// rather than merely labelled.
/// </summary>
/// <remarks>
/// <para>
/// <c>catlog.sim</c>'s <see cref="SimBody"/> carries a name and an atmosphere height, which is all
/// the <i>detector</i> needs. It is not enough for a <i>career</i>: a craft in low orbit around a
/// 13 km asteroid does not travel at 2 km/s, and a kitten does not stand on a gas giant. The radii
/// and surface gravities below are the stock KSA/KSP figures, so every orbital speed, escape speed
/// and touchdown speed the harness generates is derived from the same numbers the game uses rather
/// than invented per call site.
/// </para>
/// <para>
/// Speeds are computed, never drawn: <c>v = sqrt(g·R²/(R+h))</c> is vis-viva for a circular orbit
/// with <c>μ = g·R²</c>. That is what makes "this run's fastest orbital speed" a statement about
/// where the player went rather than about what the random number generator felt like.
/// </para>
/// </remarks>
/// <param name="Sim">The <c>catlog.sim</c> body this wraps — the name and atmosphere the wire sees.</param>
/// <param name="RadiusM">Mean radius, in metres.</param>
/// <param name="SurfaceGravityMs2">Surface gravity, in m/s².</param>
/// <param name="RotationMs">Equatorial rotation speed, in m/s: the surface/orbital speed gap.</param>
/// <param name="Ocean">True when the body has a sea to splash into.</param>
/// <param name="Landable">False for a star or a gas giant: there is no surface to touch.</param>
/// <param name="Reach">How far away it is, in career terms.</param>
/// <param name="ParentName">
/// The body whose SOI contains this one, or an empty string for the star. A craft arriving at a
/// moon of another planet enters that planet's SOI first, which is both what the game does and
/// what makes <c>soi_bodies</c> count the bodies a player actually passed through.
/// </param>
internal sealed record LoadBody(
    SimBody Sim,
    double RadiusM,
    double SurfaceGravityMs2,
    double RotationMs,
    bool Ocean,
    bool Landable,
    BodyReach Reach,
    string ParentName)
{
    /// <summary>The lowercase body name the wire carries.</summary>
    internal string Name => Sim.Name;

    /// <summary>Atmosphere height above the mean radius, in metres; 0 when airless.</summary>
    internal double AtmoHeightM => Sim.AtmoHeightM;

    /// <summary>
    /// True when a kitten can be outside on it and still be there a minute later.
    /// </summary>
    /// <remarks>
    /// The stock tumble gate is 6.5 m/s. On Deimos escape velocity is 6.2 m/s and on Phobos it is
    /// 11.3, so a kitten that tumbled there would simply leave — which is why the harness does not
    /// put one there. This is the "EVAs only where a kitten could actually be" rule, stated as
    /// arithmetic on the game's own numbers rather than as a hand-maintained list.
    /// </remarks>
    internal bool Walkable => Landable && EscapeSpeedAt(0) > 15.0;

    /// <summary>
    /// The lowest altitude a stable orbit can sit at: clear of the atmosphere by §7.2's
    /// one-kilometre margin, with room to spare, or a few percent of the radius when airless.
    /// </summary>
    internal double ParkingFloorM => AtmoHeightM > 0
        ? AtmoHeightM + 8_000
        : Math.Max(4_000, RadiusM * 0.03);

    /// <summary>Circular orbital speed at an altitude, in m/s.</summary>
    /// <param name="altitudeM">Altitude above the mean radius, in metres.</param>
    /// <returns>The speed.</returns>
    internal double OrbitSpeedAt(double altitudeM)
        => Math.Sqrt(SurfaceGravityMs2 * RadiusM * RadiusM / Math.Max(1.0, RadiusM + altitudeM));

    /// <summary>Escape speed at an altitude, in m/s.</summary>
    /// <param name="altitudeM">Altitude above the mean radius, in metres.</param>
    /// <returns>The speed.</returns>
    internal double EscapeSpeedAt(double altitudeM) => Math.Sqrt(2.0) * OrbitSpeedAt(altitudeM);

    /// <summary>
    /// Eccentricity for an apoapsis/periapsis pair, using <i>this</i> body's radius.
    /// </summary>
    /// <remarks>
    /// <see cref="SimVehicle.Orbit"/> derives eccentricity with the home world's radius baked in,
    /// which is right for the three-body universe the six scenarios fly in and wrong around a moon.
    /// Callers overwrite <see cref="SimVehicle.Ecc"/> with this afterwards.
    /// </remarks>
    /// <param name="apAltM">Apoapsis altitude, in metres.</param>
    /// <param name="peAltM">Periapsis altitude, in metres.</param>
    /// <returns>The eccentricity.</returns>
    internal double Eccentricity(double apAltM, double peAltM)
        => apAltM <= peAltM ? 0.0 : (apAltM - peAltM) / (apAltM + peAltM + (2.0 * RadiusM));

    /// <summary>
    /// A plausible touchdown speed for a controlled landing here.
    /// </summary>
    /// <remarks>
    /// Terminal velocity under a parachute scales with gravity, and there is no parachute at all
    /// on an airless body — which is why a hard landing on the Mun and a hard landing at home are
    /// different kinds of mistake, and why Gilly is almost impossible to crash into.
    /// </remarks>
    /// <param name="rng">The player's generator.</param>
    /// <returns>The speed, in m/s.</returns>
    internal double TouchdownSpeedMs(Prng rng)
    {
        double scale = Math.Sqrt(Math.Max(0.02, SurfaceGravityMs2) / 9.81);
        return AtmoHeightM > 0
            ? rng.Normal(7.0 * scale, 4.0 * scale, 0.8, 60.0 * scale)
            : rng.Normal(3.0 + (2.6 * scale), 2.4, 0.4, 40.0 * scale);
    }
}

/// <summary>
/// The celestial bodies the harness flies around: the eleven permanent members of the system
/// <b>KSA actually ships</b>.
/// </summary>
/// <remarks>
/// <para>
/// <b>Why not <see cref="SimBodies"/>.</b> <c>catlog.sim</c> flies around <c>kerbin</c>,
/// <c>mun</c> and <c>duna</c>, which is right for six hand-asserted scenarios and wrong here twice
/// over. It is too small — <c>soi_bodies</c> counts <i>distinct</i> destinations, so a three-body
/// universe caps that board at three — and the names are not the game's. KSA is the real solar
/// system: <c>Content/Core/SolSystem.xml</c> loads Sol, Mercury, Venus, Earth (<c>HomeBody</c>),
/// Luna, Mars, Phobos and Deimos, and <c>Vehicle.cs:3745</c> ships a "Teleport To Apollo 11
/// Landing Site" button. A harness that generated flights to Duna would be generating data no
/// player can ever produce, and the server's per-body boards would all be empty.
/// </para>
/// <para>
/// <b>Where the numbers come from.</b> Radii and masses are read straight out of
/// <c>Content/Core/Astronomicals.xml</c>; surface gravity is <c>GM/R²</c> from the same masses.
/// Atmosphere heights are what <c>PhysicalAtmosphereReference.CalculateBoundaryHeight()</c>
/// computes from the sea-level density, sea-level pressure and scale height in that file —
/// <c>max(-H·ln(1e-9/ρ₀), -H·ln(1e-4/P₀))</c> — not a number anyone here chose. That is what makes
/// the generated orbits right rather than merely plausible: low Earth orbit comes out at 7.8 km/s
/// and low lunar orbit at 1.6 km/s because the arithmetic says so.
/// </para>
/// </remarks>
internal static class LoadBodies
{
    /// <summary>The home world, and the only thing anything launches from.</summary>
    internal static LoadBody Earth { get; } = new(
        new SimBody("earth", 167_000), 6_371_000, 9.81, 465,
        Ocean: true, Landable: true, BodyReach.Home, "sol");

    /// <summary>The star. Every craft that leaves the home SOI is in this one next.</summary>
    internal static LoadBody Sol { get; } = new(
        new SimBody("sol", 0), 696_342_000, 274.0, 2_000,
        Ocean: false, Landable: false, BodyReach.Star, string.Empty);

    /// <summary>Every body, home and star included, in reach order.</summary>
    internal static IReadOnlyList<LoadBody> All { get; } =
    [
        Earth,
        Sol,
        new(new SimBody("luna", 0), 1_737_100, 1.625, 5,
            Ocean: false, Landable: true, BodyReach.Moon, "earth"),
        new(new SimBody("mercury", 0), 2_439_700, 3.70, 3,
            Ocean: false, Landable: true, BodyReach.Inner, "sol"),
        new(new SimBody("venus", 455_000), 6_051_800, 8.87, 2,
            Ocean: false, Landable: true, BodyReach.Inner, "sol"),
        new(new SimBody("mars", 185_000), 3_389_500, 3.73, 241,
            Ocean: false, Landable: true, BodyReach.Inner, "sol"),
        new(new SimBody("phobos", 0), 11_267, 0.00568, 3,
            Ocean: false, Landable: true, BodyReach.Inner, "mars"),
        new(new SimBody("deimos", 0), 6_200, 0.00312, 0.4,
            Ocean: false, Landable: true, BodyReach.Inner, "mars"),
        new(new SimBody("jupiter", 3_110_000), 69_911_000, 25.9, 12_570,
            Ocean: false, Landable: false, BodyReach.Outer, "sol"),
        new(new SimBody("saturn", 1_244_000), 58_232_000, 11.19, 9_870,
            Ocean: false, Landable: false, BodyReach.Outer, "sol"),
        new(new SimBody("uranus", 170_000), 25_362_000, 9.01, 2_590,
            Ocean: false, Landable: false, BodyReach.Outer, "sol"),
    ];

    /// <summary>The moons of the home world — the first thing anyone leaves for.</summary>
    internal static IReadOnlyList<LoadBody> Moons { get; } = Where(BodyReach.Moon);

    /// <summary>The inner system.</summary>
    internal static IReadOnlyList<LoadBody> Inner { get; } = Where(BodyReach.Inner);

    /// <summary>The outer system — probe territory.</summary>
    internal static IReadOnlyList<LoadBody> Outer { get; } = Where(BodyReach.Outer);

    /// <summary>Everything a flight can be aimed at: not home, and not the star.</summary>
    internal static IReadOnlyList<LoadBody> Destinations { get; } = BuildDestinations();

    /// <summary>The body whose SOI contains <paramref name="body"/>, or null for the star.</summary>
    /// <param name="body">The body.</param>
    /// <returns>Its parent, or null.</returns>
    internal static LoadBody? ParentOf(LoadBody body)
    {
        if (body.ParentName.Length == 0)
            return null;
        foreach (LoadBody candidate in All)
        {
            if (string.Equals(candidate.Name, body.ParentName, StringComparison.Ordinal))
                return candidate;
        }

        return null;
    }

    /// <summary>The bodies a career at <paramref name="stage"/> can plausibly have reached.</summary>
    /// <param name="stage">The career stage.</param>
    /// <returns>The reachable destinations; empty below <see cref="CareerStage.Interplanetary"/>.</returns>
    internal static IReadOnlyList<LoadBody> ReachableAt(CareerStage stage) => stage switch
    {
        CareerStage.Explorer => Destinations,
        CareerStage.Interplanetary => InnerAndMoons,
        _ => [],
    };

    private static IReadOnlyList<LoadBody> InnerAndMoons { get; } = BuildInnerAndMoons();

    private static IReadOnlyList<LoadBody> Where(BodyReach reach)
    {
        var list = new List<LoadBody>();
        foreach (LoadBody body in All)
        {
            if (body.Reach == reach)
                list.Add(body);
        }

        return list;
    }

    private static IReadOnlyList<LoadBody> BuildDestinations()
    {
        var list = new List<LoadBody>();
        foreach (LoadBody body in All)
        {
            if (body.Reach is not (BodyReach.Home or BodyReach.Star))
                list.Add(body);
        }

        return list;
    }

    private static IReadOnlyList<LoadBody> BuildInnerAndMoons()
    {
        var list = new List<LoadBody>(Moons);
        list.AddRange(Inner);
        return list;
    }
}

/// <summary>
/// Maps sim seconds to the client unix milliseconds every envelope carries in <c>wall_t</c>.
/// </summary>
/// <remarks>
/// <para>
/// Anchored so a run's simulated span <i>ends</i> at "now": a six-hour run produces timestamps
/// covering the last six hours rather than the next six. Nothing on the server depends on it —
/// §4.1 makes <c>wall_t</c> untrusted and every read-API timestamp comes from <c>recv_time</c> —
/// but a harness that emitted clocks from the future would be a misleading model of the game, and
/// the point of this program is to look like play.
/// </para>
/// <para>
/// This is also why <c>catlog.sim</c>'s <see cref="SimClock"/> is not reused: its epoch is fixed
/// two hours in the past, which is exactly right for a scenario that compresses half an hour and
/// wrong for a run that compresses six.
/// </para>
/// </remarks>
internal sealed class LoadClock
{
    private readonly long _epochMs;

    /// <summary>Creates a clock whose simulated span ends now.</summary>
    /// <param name="durationSeconds">The simulated span, in sim seconds.</param>
    internal LoadClock(double durationSeconds)
        => _epochMs = DateTimeOffset.UtcNow.AddSeconds(-durationSeconds).ToUnixTimeMilliseconds();

    /// <summary>The client unix-millisecond stamp for a sim instant.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    internal long Wall(double simT) => _epochMs + (long)(simT * 1000.0);
}

/// <summary>
/// One player's career clock: sim time measured from the moment their save began.
/// </summary>
/// <remarks>
/// <para>
/// <c>sim_t</c> is <b>seconds since the career started</b>, not seconds since the harness started
/// watching (§4.1). A player who arrives with three hundred in-game hours behind them therefore
/// produces events at <c>sim_t ≈ 1.08e6</c> and upwards, and their first orbit of the run is not
/// an orbit forty seconds into their career. Getting this wrong would not fail anything — it would
/// quietly make every "time from game start to X" board wrong for every player the harness
/// generated, which is worse.
/// </para>
/// <para>
/// The wall clock underneath stays anchored to the run's window: <c>wall_t</c> is when the events
/// were produced, which is the last few hours, whatever the career says.
/// </para>
/// </remarks>
internal sealed class CareerClock
{
    private readonly LoadClock _clock;

    /// <summary>Creates a career clock.</summary>
    /// <param name="clock">The run's window clock.</param>
    /// <param name="epoch">Career seconds already elapsed when the window opened.</param>
    internal CareerClock(LoadClock clock, double epoch)
    {
        _clock = clock;
        Epoch = epoch;
    }

    /// <summary>Career seconds already elapsed when the run's window opened.</summary>
    internal double Epoch { get; }

    /// <summary>The client unix-millisecond stamp for a career instant.</summary>
    /// <param name="careerT">Career sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    internal long Wall(double careerT) => _clock.Wall(careerT - Epoch);
}

/// <summary>Curve helpers, so a flight profile reads like a flight profile.</summary>
internal static class Curve
{
    /// <summary>Linear interpolation, clamped to the endpoints.</summary>
    /// <param name="from">Value at <c>u = 0</c>.</param>
    /// <param name="to">Value at <c>u = 1</c>.</param>
    /// <param name="u">Position along the segment.</param>
    /// <returns>The interpolated value.</returns>
    internal static double Lerp(double from, double to, double u)
        => from + ((to - from) * Math.Clamp(u, 0.0, 1.0));

    /// <summary>Smoothstep, for profiles that should not start and stop with a corner.</summary>
    /// <param name="u">Position along the segment.</param>
    /// <returns>The eased position.</returns>
    internal static double Ease(double u)
    {
        double c = Math.Clamp(u, 0.0, 1.0);
        return c * c * (3.0 - (2.0 * c));
    }

    /// <summary>A bell centred on <paramref name="peak"/> — max-Q, and the g spike of a reentry.</summary>
    /// <param name="u">Position along the segment.</param>
    /// <param name="peak">Where the maximum sits.</param>
    /// <param name="width">How sharp the bell is; smaller is sharper.</param>
    /// <returns>A multiplier in <c>(0, 1]</c>.</returns>
    internal static double Bell(double u, double peak, double width)
    {
        double d = (u - peak) / width;
        return Math.Exp(-d * d);
    }

    /// <summary>Kinetic energy in joules.</summary>
    /// <param name="massKg">Mass, in kilograms.</param>
    /// <param name="speedMs">Speed, in metres per second.</param>
    /// <returns>The energy.</returns>
    internal static double Energy(double massKg, double speedMs) => 0.5 * massKg * speedMs * speedMs;
}
