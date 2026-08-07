using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// A kitten EVAs from a mun lander, bounds around at low gravity and goes over three times
/// (INITIAL_IMPL_PLAN §7.3 scenario 4).
/// </summary>
/// <remarks>
/// <para>
/// Every tumble speed here is above the game's <c>TumbleSpeedGate</c> of 6.5 m/s
/// (<c>docs/ksa-integration.md</c> B9) and the kitten is in terrain contact each time, which is
/// the game's whole classifier for <c>LocomotionMode.Tumbling</c>. Nothing is flagged
/// <c>tuning</c>, so all three count.
/// </para>
/// <para>
/// <c>kitten.tumble</c> carries <c>flight: null</c> (§4.2), which is why this board is the one
/// place a flag on a flight cannot suppress a score — worth exercising alongside the
/// <c>cheater</c> scenario, which proves the opposite for every flight-attributed board.
/// </para>
/// </remarks>
public sealed class TumbleweedScenario : IScenario
{
    private const string LanderId = "mun-lander-3";
    private const string EvaId = "eva-valentina";
    private const string KittenName = "Valentina Kerman";

    /// <summary>The game's stock tumble gate; every speed below is above it.</summary>
    private const double TumbleGateMs = 6.5;

    private static readonly double[] TumbleSpeeds = [8.2, 7.1, 9.4];

    /// <inheritdoc />
    public string Name => "tumbleweed";

    /// <inheritdoc />
    public string Summary => "mun EVA: one kitten out the hatch, three tumbles above the 6.5 m/s gate, back aboard";

    /// <inheritdoc />
    public string Asserts => "kitten_tumbles += 3";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        var lander = new SimVehicle(LanderId, "Mun Lander III", SimBodies.Mun, crewCount: 1, partCount: 21, massKg: 3_200);
        lander.Rest("landed");

        var eva = new SimVehicle(EvaId, KittenName, SimBodies.Mun, crewCount: 1, partCount: 2, massKg: 94);
        eva.Rest("rolling");

        yield return SimStep.At(0)
            .Emit(new VehicleCreatedSignal(
                0, SimClock.Wall(0), LanderId, lander.Name, lander.Body.Name, lander.MassKg,
                lander.PartCount, 1, LaunchGameTime: 0))
            .With(lander.Sample(0));

        for (double t = Play.Dt; t < 6.0; t += Play.Dt)
            yield return SimStep.At(t).With(lander.Sample(t));

        // --- egress: the kitten becomes its own vehicle, and the lander's seat empties ---
        lander.CrewCount = 0;
        yield return SimStep.At(6.0)
            .Emit(
                new VehicleCreatedSignal(
                    6.0, SimClock.Wall(6.0), EvaId, KittenName, eva.Body.Name, eva.MassKg,
                    eva.PartCount, 1, LaunchGameTime: 6.0),
                new EvaStartSignal(6.0, SimClock.Wall(6.0), KittenName, EvaId))
            .With(lander.Sample(6.0), eva.Sample(6.0));

        // --- three bounding sprints, each ending in a tumble ---
        double now = 6.5;
        foreach (double speed in TumbleSpeeds)
        {
            double runStart = now;
            for (; now < runStart + 18.0; now += Play.Dt)
            {
                double u = (now - runStart) / 18.0;
                eva.Situation = "rolling";
                eva.SurfaceSpeedMs = Play.Lerp(0.4, speed, Play.Ease(u));
                eva.OrbitalSpeedMs = eva.SurfaceSpeedMs;
                eva.AltitudeM = 1.1;
                eva.AccelMs2 = 1.63;
                yield return SimStep.At(now).With(lander.Sample(now), eva.Sample(now));
            }

            // Terrain contact plus tangential speed over the gate: the game's tumble condition.
            yield return SimStep.At(now)
                .Emit(new TumbleSignal(now, SimClock.Wall(now), KittenName, speed, eva.Body.Name))
                .With(lander.Sample(now), eva.Sample(now));

            eva.Rest("rolling");
            now += Play.Dt;
            yield return SimStep.At(now).With(lander.Sample(now), eva.Sample(now));
            now += Play.Dt;
        }

        // --- back in the hatch: the EVA vehicle despawns, the seat refills ---
        lander.CrewCount = 1;
        yield return SimStep.At(now)
            .Emit(
                new EvaEndSignal(now, SimClock.Wall(now), KittenName, DurationS: now - 6.0),
                new VehicleRemovedSignal(now, SimClock.Wall(now), EvaId, FlightEndReason.Despawned, 1))
            .With(lander.Sample(now));

        now += Play.Dt;
        yield return SimStep.At(now).With(lander.Sample(now));

        now += Play.Dt;
        yield return SimStep.At(now).Emit(new RosterSampleSignal(
            now,
            SimClock.Wall(now),
            [new RosterKitten(KittenName, TravelledM: 412.0, FastestMs: 29_812.0, Missions: 4, MissionTimeS: 9_140.0, Kia: false)]));

        now += Play.Dt;
        yield return SimStep.At(now)
            .Emit(new VehicleRecoveredSignal(now, SimClock.Wall(now), LanderId, 1));
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        api.ExpectCounter(handle, "kitten_tumbles", TumbleSpeeds.Length);
        api.Record(
            ok: true,
            label: "tumble gate",
            expected: $"> {TumbleGateMs} m/s",
            actual: string.Join(", ", TumbleSpeeds),
            note: "every tumble speed clears the game's stock TumbleSpeedGate (ksa-integration.md B9)");
    }
}
