using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// A complete orbital mission: launch, atmosphere exit, circularisation, a few orbits on rails,
/// deorbit, re-entry, splashdown and recovery (INITIAL_IMPL_PLAN §7.3 scenario 2).
/// </summary>
/// <remarks>
/// <para>
/// Every event this scenario asserts is <b>detected</b>, not signalled: nothing tells the pipeline
/// that the vehicle reached orbit. <c>vehicle.orbit</c> comes out of the §7.2 rule
/// (<c>bound &amp;&amp; pe_alt &gt; atmo_height + 1000</c>) applied to the periapsis the
/// circularisation burn raised, and <c>fastest_orbital_speed</c> comes out of a 30 s
/// <c>telemetry.window</c> fold over samples this scenario never aggregates itself.
/// </para>
/// <para>
/// The deorbit burn drops the periapsis below the surface, which re-arms the orbit-achieved latch
/// without emitting anything — so the board must read exactly one orbit, not two.
/// </para>
/// </remarks>
public sealed class OrbitAndBackScenario : IScenario
{
    private const string Id = "orbiter-1";
    private const string VehicleName = "Kittenbird 7";
    private const int Crew = 3;
    private const double MassKg = 42_000;

    /// <summary>Circular parking orbit speed; the value <c>fastest_orbital_speed</c> must end up at.</summary>
    private const double OrbitalSpeedMs = 7784.0;

    private const double ApM = 320_000;
    private const double PeM = 300_000;
    private const double SplashdownMs = 6.4;

    /// <summary>Sim time at which the orbit-achieved latch fires — the value `fastest_to_orbit` must end at.</summary>
    private const double OrbitAchievedSimT = 190.0;

    /// <inheritdoc />
    public string Name => "orbit-and-back";

    /// <inheritdoc />
    public string Summary => "launch to a 300×320 km kerbin orbit, coast, deorbit, splash down and recover 3 crew";

    /// <inheritdoc />
    public string Asserts => "orbits_achieved += 1 · fastest_orbital_speed = 7784 · fastest_to_orbit = 190";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        var v = new SimVehicle(Id, VehicleName, SimBodies.Kerbin, Crew, 68, MassKg);

        yield return SimStep.At(0)
            .Emit(new VehicleCreatedSignal(
                0, SimClock.Wall(0), Id, VehicleName, v.Body.Name, MassKg, v.PartCount, Crew,
                LaunchGameTime: 0))
            .With(v.Sample(0));

        for (double t = Play.Dt; t < 4.0; t += Play.Dt)
            yield return SimStep.At(t).With(v.Sample(t));

        // --- first stage: 4 s → 90 s, pad to 46 km ---
        yield return SimStep.At(4.0)
            .Emit(
                new EngineSignal(4.0, SimClock.Wall(4.0), Id, EngineEventKind.Ignition, "RE-M3 Mainsail", 1),
                new StagingSignal(4.0, SimClock.Wall(4.0), Id, 0))
            .With(Ascend(v, 4.0, 4.0, 90.0));

        for (double t = 4.5; t < 90.0; t += Play.Dt)
            yield return SimStep.At(t).With(Ascend(v, t, 4.0, 90.0));

        // --- staging and second stage: 90 s → 190 s, 46 km to 130 km, atmosphere exit en route ---
        v.MassKg = 14_500;
        v.PartCount = 31;
        yield return SimStep.At(90.0)
            .Emit(
                new EngineSignal(90.0, SimClock.Wall(90.0), Id, EngineEventKind.Shutdown, "RE-M3 Mainsail", 1),
                new StagingSignal(90.0, SimClock.Wall(90.0), Id, 1),
                new EngineSignal(90.0, SimClock.Wall(90.0), Id, EngineEventKind.Ignition, "LV-909 Terrier", 1))
            .With(Climb(v, 90.0, 90.0, 190.0));

        for (double t = 90.5; t < 190.0; t += Play.Dt)
            yield return SimStep.At(t).With(Climb(v, t, 90.0, 190.0));

        // --- circularisation completes: the periapsis clears atmo + 1 km and the detector fires ---
        yield return SimStep.At(190.0)
            .Emit(new EngineSignal(190.0, SimClock.Wall(190.0), Id, EngineEventKind.Shutdown, "LV-909 Terrier", 1))
            .With(Parked(v, 190.0));

        // --- three orbits on rails: 190 s → 700 s. No structural-load reading exists on rails,
        //     which is exactly the "omit peak_g, do not report zero" case. ---
        for (double t = 190.5; t < 700.0; t += Play.Dt)
            yield return SimStep.At(t).With(Parked(v, t));

        // --- deorbit burn: periapsis goes below the surface, re-arming the latch silently ---
        yield return SimStep.At(700.0)
            .Emit(new EngineSignal(700.0, SimClock.Wall(700.0), Id, EngineEventKind.Ignition, "LV-909 Terrier", 1))
            .With(Deorbit(v, 700.0, 700.0, 760.0));

        for (double t = 700.5; t < 760.0; t += Play.Dt)
            yield return SimStep.At(t).With(Deorbit(v, t, 700.0, 760.0));

        // --- re-entry: 760 s → 900 s, crossing back into the atmosphere ---
        v.MassKg = 4_800;
        v.PartCount = 12;
        yield return SimStep.At(760.0)
            .Emit(
                new EngineSignal(760.0, SimClock.Wall(760.0), Id, EngineEventKind.Shutdown, "LV-909 Terrier", 1),
                new StagingSignal(760.0, SimClock.Wall(760.0), Id, 2))
            .With(Reenter(v, 760.0, 760.0, 900.0));

        for (double t = 760.5; t < 900.0; t += Play.Dt)
            yield return SimStep.At(t).With(Reenter(v, t, 760.0, 900.0));

        // --- splashdown ---
        v.Rest("floating");
        yield return SimStep.At(900.0)
            .Emit(new SplashSignal(
                900.0, SimClock.Wall(900.0), Id,
                SpeedMs: SplashdownMs,
                EnergyJ: Play.Energy(4_800, SplashdownMs),
                Body: v.Body.Name,
                CrewCount: Crew))
            .With(v.Sample(900.0));

        for (double t = 900.5; t < 908.0; t += Play.Dt)
            yield return SimStep.At(t).With(v.Sample(t));

        yield return SimStep.At(908.0)
            .Emit(new VehicleRecoveredSignal(908.0, SimClock.Wall(908.0), Id, Crew));
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        api.ExpectCounter(handle, "orbits_achieved", 1);
        api.ExpectRecord(handle, "fastest_orbital_speed", OrbitalSpeedMs);
        // Career time, not wall time: the scenario's clock starts at 0 and the
        // periapsis clears atmosphere + 1 km at t = 190 s (§4.1).
        api.ExpectBest(handle, "fastest_to_orbit", OrbitAchievedSimT);
    }

    private static TelemetrySnapshot Ascend(SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(0, 46_000, Play.Ease(u)),
            surfaceSpeedMs: Play.Lerp(0, 1_640, Play.Ease(u)),
            accelMs2: Play.Lerp(12, 33, u),
            dynPressurePa: 41_000 * Math.Exp(-9 * (u - 0.3) * (u - 0.3)));
        v.PeAltM = Play.Lerp(-620_000, -400_000, u);
        v.ApAltM = Play.Lerp(0, 96_000, u);
        return v.Sample(t);
    }

    private static TelemetrySnapshot Climb(SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(46_000, 130_000, Play.Ease(u)),
            surfaceSpeedMs: Play.Lerp(1_640, 7_400, Play.Ease(u)),
            accelMs2: Play.Lerp(9, 21, u),
            dynPressurePa: Play.Lerp(9_400, 0, Play.Ease(Math.Min(1.0, u * 2.4))));
        v.OrbitalSpeedMs = Play.Lerp(1_640, OrbitalSpeedMs - 60, Play.Ease(u));

        // The periapsis is what the orbit-achieved rule watches. It stays below atmo + 1 km until
        // the very end of the burn, so exactly one rising edge exists in the whole scenario.
        v.PeAltM = Play.Lerp(-400_000, 40_000, Play.Ease(u));
        v.ApAltM = Play.Lerp(96_000, ApM, Play.Ease(u));
        v.Ecc = Play.Lerp(0.85, 0.05, Play.Ease(u));
        return v.Sample(t);
    }

    private static TelemetrySnapshot Parked(SimVehicle v, double t)
    {
        v.Orbit(ApM, PeM, OrbitalSpeedMs, surfaceSpeedMs: OrbitalSpeedMs - 174.5);
        v.IncDeg = 28.5;
        return v.Sample(t);
    }

    private static TelemetrySnapshot Deorbit(SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Orbit(ApM, Play.Lerp(PeM, -45_000, Play.Ease(u)), Play.Lerp(OrbitalSpeedMs, 7_610, u), 7_430);
        v.AltitudeM = Play.Lerp(PeM, 180_000, Play.Ease(u));
        v.Situation = "maneuvering";
        v.AccelMs2 = 3.1;
        v.PeakG = 0.32;
        v.MaxQPa = 0;
        return v.Sample(t);
    }

    private static TelemetrySnapshot Reenter(SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(180_000, 0, Play.Ease(u)),
            surfaceSpeedMs: u < 0.55 ? Play.Lerp(7_430, 260, u / 0.55) : Play.Lerp(260, SplashdownMs, (u - 0.55) / 0.45),
            accelMs2: 62.0 * Math.Exp(-26 * (u - 0.45) * (u - 0.45)),
            dynPressurePa: 47_000 * Math.Exp(-24 * (u - 0.45) * (u - 0.45)));
        v.OrbitalSpeedMs = v.SurfaceSpeedMs;
        v.PeAltM = -45_000;
        v.ApAltM = Play.Lerp(ApM, 0, Play.Ease(u));
        return v.Sample(t);
    }
}
