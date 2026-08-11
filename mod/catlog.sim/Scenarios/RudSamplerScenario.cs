using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// Six short flights, one destroyed per §4.2 cause (§7.3 scenario 3).
/// </summary>
/// <remarks>
/// <para>
/// The per-cause boards are a closed enum server-side — an unknown cause counts towards
/// <c>rud_total</c> and nothing else — so running every one of them is the only way to prove the
/// mod's <see cref="MeowSci.Catlog.Lib.Events.RudCause"/> mapping agrees with the server's.
/// </para>
/// <para>
/// The two impact-driven causes also drive the <b>negative</b> half of the impact correlator: the
/// impact and the destruction arrive in the same frame, so <c>vehicle.impact.survived</c> must
/// come out false and none of these six may reach <c>biggest_lithobrake_survived</c>.
/// </para>
/// </remarks>
public sealed class RudSamplerScenario : IScenario
{
    private sealed record Flight(
        string Id,
        string Name,
        RudCause Cause,
        string Situation,
        double AltitudeM,
        double SpeedMs,
        double PeakG,
        double PeakQPa,
        bool Impact);

    private static readonly Flight[] Flights =
    [
        new("rud-ground", "Lawn Dart", RudCause.GroundImpact, "maneuvering", 0, 214, 78, 3_100, Impact: true),
        new("rud-ocean", "Deep Six", RudCause.OceanImpact, "sailing", 0, 141, 52, 2_400, Impact: true),
        new("rud-collision", "Rendezvous II", RudCause.Collision, "freefall", 184_000, 11.4, 9, 0, Impact: false),
        new("rud-gforce", "Overshoot", RudCause.ExcessiveGForce, "maneuvering", 31_000, 2_960, 26.4, 61_000, Impact: false),
        new("rud-aero", "Max Q", RudCause.AerodynamicForces, "maneuvering", 9_800, 640, 4.1, 96_000, Impact: false),
        new("rud-hydro", "Submersible", RudCause.HydrodynamicForces, "bottomed", 0, 38, 3.4, 412_000, Impact: false),
    ];

    /// <inheritdoc />
    public string Name => "rud-sampler";

    /// <inheritdoc />
    public string Summary => "six flights, one rapid unscheduled disassembly per §4.2 cause";

    /// <inheritdoc />
    public string Asserts => "rud_total += 6 · each rud_<cause> += 1 · nothing on biggest_lithobrake_survived";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        double t = 0;

        foreach (Flight flight in Flights)
        {
            SimBody body = BodyFor(flight.Cause);
            var v = new SimVehicle(flight.Id, flight.Name, body, crewCount: 1, partCount: 17, massKg: 9_400);

            double start = t;
            yield return SimStep.At(t)
                .Emit(new VehicleCreatedSignal(
                    t, SimClock.Wall(t), flight.Id, flight.Name, body.Name, v.MassKg, v.PartCount, 1,
                    LaunchGameTime: t))
                .With(Approach(v, flight, t, 0));

            // 25 s of flight per vehicle: long enough to close a partial telemetry window on the
            // way out, short enough that six flights stay a quick scenario.
            for (t = start + Play.Dt; t < start + 25.0; t += Play.Dt)
                yield return SimStep.At(t).With(Approach(v, flight, t, (t - start) / 25.0));

            // The frame everything happens in. Order matters and the channel preserves it: the
            // impact is recorded first, then the destruction marks it, so the correlator's verdict
            // one frame later is survived = false.
            var signals = new List<GameSignal>(2);
            if (flight.Impact)
            {
                signals.Add(new ImpactSignal(
                    t, SimClock.Wall(t), flight.Id, flight.SpeedMs, Play.Energy(v.MassKg, flight.SpeedMs),
                    LaunchPad: false, Body: body.Name, CrewCount: 1));
            }

            signals.Add(new RudSignal(
                t, SimClock.Wall(t), flight.Id, flight.Cause,
                PeakG: flight.PeakG,
                PeakQPa: flight.PeakQPa,
                SpeedMs: flight.SpeedMs,
                AltitudeM: flight.AltitudeM,
                Body: body.Name,
                CrewCount: 1,
                PartCount: v.PartCount));

            yield return SimStep.At(t).Emit([.. signals]).With(Approach(v, flight, t, 1.0));

            // One empty frame: the vehicle no longer exists, and this boundary is what resolves
            // the impact the RUD marked.
            t += Play.Dt;
            yield return SimStep.At(t);

            t += Play.Dt;
            yield return SimStep.At(t)
                .Emit(new VehicleRemovedSignal(t, SimClock.Wall(t), flight.Id, FlightEndReason.Destroyed, 1));

            t += 2.0;
        }
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        api.ExpectCounter(handle, "rud_total", Flights.Length);
        foreach (Flight flight in Flights)
            api.ExpectCounter(handle, "rud_" + EventTypes.ToWire(flight.Cause), 1);

        api.ExpectUnchanged(
            handle,
            "biggest_lithobrake_survived",
            "every impact here was followed by a same-frame destruction, so survived = false");
    }

    private static SimBody BodyFor(RudCause cause) => cause switch
    {
        // A collision in orbit and a g-force break-up on a re-entry are the two that are not about
        // hitting kerbin's surface; they still happen in its sphere of influence.
        RudCause.Collision => SimBodies.Kerbin,
        RudCause.ExcessiveGForce => SimBodies.Duna,
        _ => SimBodies.Kerbin,
    };

    private static TelemetrySnapshot Approach(SimVehicle v, Flight flight, double t, double u)
    {
        v.Situation = flight.Situation;
        v.AltitudeM = Play.Lerp(Math.Max(flight.AltitudeM, 2_400), flight.AltitudeM, Play.Ease(u));
        v.SurfaceSpeedMs = Play.Lerp(flight.SpeedMs * 0.45, flight.SpeedMs, Play.Ease(u));
        v.OrbitalSpeedMs = v.SurfaceSpeedMs;
        v.AccelMs2 = Play.Lerp(9.8, flight.PeakG * 9.81, Play.Ease(u));
        v.DynPressurePa = Play.Lerp(0, flight.PeakQPa, Play.Ease(u));
        v.PeakG = v.AccelMs2 / 9.81;
        v.MaxQPa = v.DynPressurePa;
        v.PeAltM = -500_000;
        v.ApAltM = flight.AltitudeM + 30_000;
        return v.Sample(t);
    }
}
