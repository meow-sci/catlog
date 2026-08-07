using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// A suborbital hop that comes down hard but intact: apogee 45 km on kerbin, touchdown at
/// 62 m/s with two kittens aboard, then recovery (INITIAL_IMPL_PLAN §7.3 scenario 1).
/// </summary>
/// <remarks>
/// The two boards this asserts exercise opposite halves of the pipeline.
/// <c>biggest_lithobrake_survived</c> comes from an <see cref="ImpactSignal"/> whose
/// <c>survived</c> flag is not in the signal at all — <see cref="MeowSci.Catlog.Lib.Detect.ImpactCorrelator"/>
/// computes it by holding the impact a full frame and seeing that nothing destroyed the vehicle
/// (§4.2, and the WP6 one-full-frame rule for manual destroys). <c>kittens_recovered</c> comes
/// from <c>flight.ended</c>, which the pipeline only emits because a
/// <see cref="VehicleRecoveredSignal"/> closed the flight the tracker opened.
/// </remarks>
public sealed class HopLithobrakeScenario : IScenario
{
    private const string Id = "hopper-1";
    private const string VehicleName = "Hopper I";
    private const int Crew = 2;
    private const double MassKg = 18_000;
    private const double TouchdownMs = 62.0;
    private const double ApogeeM = 45_000;

    /// <inheritdoc />
    public string Name => "hop-lithobrake";

    /// <inheritdoc />
    public string Summary => "suborbital hop on kerbin; 62 m/s survivable touchdown with 2 crew, then recovery";

    /// <inheritdoc />
    public string Asserts => "biggest_lithobrake_survived = 62 · kittens_recovered += 2";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        var v = new SimVehicle(Id, VehicleName, SimBodies.Kerbin, Crew, 24, MassKg);

        // --- pad: register the vehicle, then hold long enough to seed the detector's latches ---
        yield return SimStep.At(0)
            .Emit(new VehicleCreatedSignal(
                0, SimClock.Wall(0), Id, VehicleName, v.Body.Name, MassKg, v.PartCount, Crew,
                LaunchGameTime: 0))
            .With(v.Sample(0));

        for (double t = Play.Dt; t < 3.0; t += Play.Dt)
            yield return SimStep.At(t).With(v.Sample(t));

        // --- ignition and ascent: 3 s → 55 s, burning to 38 km ---
        yield return SimStep.At(3.0)
            .Emit(
                new EngineSignal(3.0, SimClock.Wall(3.0), Id, EngineEventKind.Ignition, "LV-T45 Swivel", 1),
                new StagingSignal(3.0, SimClock.Wall(3.0), Id, 0))
            .With(Ascend(v, 3.0, 3.0, 55.0));

        for (double t = 3.5; t < 55.0; t += Play.Dt)
            yield return SimStep.At(t).With(Ascend(v, t, 3.0, 55.0));

        // --- burnout and coast to apogee: 55 s → 95 s ---
        yield return SimStep.At(55.0)
            .Emit(new EngineSignal(55.0, SimClock.Wall(55.0), Id, EngineEventKind.Shutdown, "LV-T45 Swivel", 1))
            .With(Coast(v, 55.0, 55.0, 95.0));

        for (double t = 55.5; t < 95.0; t += Play.Dt)
            yield return SimStep.At(t).With(Coast(v, t, 55.0, 95.0));

        // --- descent: 95 s → 150 s, arriving at the surface doing 62 m/s ---
        for (double t = 95.0; t < 150.0; t += Play.Dt)
            yield return SimStep.At(t).With(Descend(v, t, 95.0, 150.0));

        // --- touchdown: the impact the lithobrake board is about ---
        v.Rest("rolling");
        v.SurfaceSpeedMs = 4.0;
        yield return SimStep.At(150.0)
            .Emit(new ImpactSignal(
                150.0, SimClock.Wall(150.0), Id,
                SpeedMs: TouchdownMs,
                EnergyJ: Play.Energy(MassKg, TouchdownMs),
                LaunchPad: false,
                Body: v.Body.Name,
                CrewCount: Crew))
            .With(v.Sample(150.0));

        // The correlator resolves an impact at the end of the *following* frame, so the vehicle
        // has to still be there for it. Rolling to a stop covers that, and is what actually
        // happens.
        v.Rest("landed");
        for (double t = 150.5; t < 158.0; t += Play.Dt)
            yield return SimStep.At(t).With(v.Sample(t));

        // --- recovery ---
        yield return SimStep.At(158.0)
            .Emit(new VehicleRecoveredSignal(158.0, SimClock.Wall(158.0), Id, Crew));
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        api.ExpectRecord(handle, "biggest_lithobrake_survived", TouchdownMs);
        api.ExpectCounter(handle, "kittens_recovered", Crew);
    }

    private static TelemetrySnapshot Ascend(
        SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(0, 38_000, Play.Ease(u)),
            surfaceSpeedMs: Play.Lerp(0, 780, Play.Ease(u)),
            accelMs2: Play.Lerp(14, 29, u),
            // Max Q around a third of the way up, then thinning air.
            dynPressurePa: 34_000 * Math.Exp(-8 * (u - 0.33) * (u - 0.33)));
        v.PeAltM = Play.Lerp(-600_000, -140_000, u);
        v.ApAltM = Play.Lerp(0, ApogeeM, u);
        return v.Sample(t);
    }

    private static TelemetrySnapshot Coast(
        SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(38_000, ApogeeM, Math.Sin(u * Math.PI * 0.5)),
            surfaceSpeedMs: Play.Lerp(780, 120, Play.Ease(u)),
            accelMs2: 0.4,
            dynPressurePa: Play.Lerp(900, 30, u));
        v.Situation = "freefall";
        return v.Sample(t);
    }

    private static TelemetrySnapshot Descend(
        SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(ApogeeM, 0, Play.Ease(u)),
            // Terminal velocity under a partly-failed chute: fast enough to hurt, slow enough to
            // walk away from (D11: physics destruction never kills crew, and nothing destroys
            // this vehicle).
            surfaceSpeedMs: u < 0.75 ? Play.Lerp(120, 310, u / 0.75) : Play.Lerp(310, TouchdownMs, (u - 0.75) / 0.25),
            accelMs2: u < 0.75 ? 9.8 : 44.0,
            dynPressurePa: Play.Lerp(30, 26_000, Play.Ease(u)));
        return v.Sample(t);
    }
}
