using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// Thirty minutes of a busy save compressed into a few seconds: a thirty-craft resident fleet,
/// three complete missions, an EVA and a docking, ~2 000 events including telemetry windows
/// (INITIAL_IMPL_PLAN §7.3 scenario 6).
/// </summary>
/// <remarks>
/// <para>
/// This is the scenario that tests volume rather than a rule. Its two headline assertions are
/// about the transport, not the boards: <b>every event the pipeline produced reached
/// <c>events.db</c></b> (<c>events.total</c> advanced by exactly the number of envelopes the
/// outbox accepted) and <b>none of them arrived twice</b> (<c>ingest_deduped</c> did not move). A
/// dropped batch, a mis-chained <c>ph</c>, a re-shipped batch after a partial failure or an
/// off-by-one in the outbox drain all show up as one of those two numbers being wrong.
/// </para>
/// <para>
/// The resident fleet is what generates the bulk: thirty craft sampled at 2 Hz for 1 800 sim
/// seconds is sixty <c>telemetry.window</c> events each. They start already in orbit, so their
/// first sample is a detector baseline and none of them emits a spurious
/// <c>vehicle.orbit: achieved</c> — which is itself worth asserting, because the three that the
/// board must show all come from the missions.
/// </para>
/// </remarks>
public sealed class SoakScenario : IScenario
{
    private const double DurationS = 1_800;
    private const int ResidentCount = 30;
    private const int MissionCount = 3;
    private const int MissionCrew = 2;
    private const int StagingsPerMission = 4;
    private const string EvaBaseId = "mun-base-1";
    private const string EvaId = "eva-bill";
    private const string EvaKitten = "Bill Kerman";

    private static readonly double[] TumbleSpeeds = [7.4, 8.9, 6.8, 11.2];

    /// <inheritdoc />
    public string Name => "soak";

    /// <inheritdoc />
    public string Summary =>
        "30 min of compressed play: a 30-craft resident fleet, 3 full missions, an EVA and a docking";

    /// <inheritdoc />
    public string Asserts =>
        "events.total += exactly what the pipeline produced · ingest_deduped += 0 · "
        + "orbits_achieved += 3 · stagings += 12 · kittens_recovered += 6 · kitten_tumbles += 4 · dockings += 1";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        var schedule = new Dictionary<int, List<GameSignal>>();

        void At(double t, params GameSignal[] signals)
        {
            int frame = (int)Math.Round(t / Play.Dt);
            if (!schedule.TryGetValue(frame, out List<GameSignal>? bucket))
                schedule[frame] = bucket = [];
            bucket.AddRange(signals);
        }

        List<SimVehicle> residents = BuildResidents();
        foreach (SimVehicle r in residents)
        {
            At(0, new VehicleCreatedSignal(
                0, SimClock.Wall(0), r.Id, r.Name, r.Body.Name, r.MassKg, r.PartCount, r.CrewCount,
                LaunchGameTime: 0));
        }

        var missions = new List<Mission>();
        for (int i = 0; i < MissionCount; i++)
        {
            var mission = new Mission(i, 100.0 + (i * 600.0));
            mission.Schedule(At);
            missions.Add(mission);
        }

        // --- EVA from the mun surface base, 1 000 s → 1 200 s ---
        var eva = new SimVehicle(EvaId, EvaKitten, SimBodies.Mun, crewCount: 1, partCount: 2, massKg: 94);
        eva.Rest("rolling");
        At(1_000,
            new VehicleCreatedSignal(
                1_000, SimClock.Wall(1_000), EvaId, EvaKitten, SimBodies.Mun.Name, eva.MassKg, 2, 1,
                LaunchGameTime: 1_000),
            new EvaStartSignal(1_000, SimClock.Wall(1_000), EvaKitten, EvaId));
        for (int i = 0; i < TumbleSpeeds.Length; i++)
        {
            double t = 1_040 + (i * 35);
            At(t, new TumbleSignal(t, SimClock.Wall(t), EvaKitten, TumbleSpeeds[i], SimBodies.Mun.Name));
        }

        At(1_200,
            new EvaEndSignal(1_200, SimClock.Wall(1_200), EvaKitten, DurationS: 200),
            new VehicleRemovedSignal(1_200, SimClock.Wall(1_200), EvaId, FlightEndReason.Despawned, 1));

        // --- a station visit: two resident craft dock and separate again ---
        At(1_500, new DockSignal(1_500, SimClock.Wall(1_500), residents[1].Id, residents[2].Id));
        At(1_560, new UndockSignal(1_560, SimClock.Wall(1_560), residents[1].Id, residents[2].Id));

        // --- roster snapshots, §4.2's every-10-minutes cadence ---
        foreach (double t in new[] { 600.0, 1_200.0, 1_795.0 })
            At(t, new RosterSampleSignal(t, SimClock.Wall(t), Roster(t)));

        int frames = (int)Math.Round(DurationS / Play.Dt);
        for (int frame = 0; frame <= frames; frame++)
        {
            double t = frame * Play.Dt;
            var vehicles = new List<TelemetrySnapshot>(ResidentCount + MissionCount + 1);

            for (int i = 0; i < residents.Count; i++)
            {
                SimVehicle r = residents[i];

                // One craft leaves kerbin's sphere of influence halfway through, which is the only
                // vehicle.soi in the run — the detector finds it from the parent-body change alone.
                if (i == 0 && t >= 900.0 && r.Body != SimBodies.Mun)
                {
                    r.Body = SimBodies.Mun;
                    r.Orbit(apAltM: 92_000, peAltM: 61_000, orbitalSpeedMs: 548, surfaceSpeedMs: 548);
                }

                vehicles.Add(r.Sample(t));
            }

            foreach (Mission mission in missions)
            {
                if (mission.Alive(t))
                    vehicles.Add(mission.Sample(t));
            }

            if (t >= 1_000.0 && t < 1_200.0)
            {
                double phase = (t - 1_000.0) % 35.0;
                eva.Situation = "rolling";
                eva.AltitudeM = 1.2;
                eva.SurfaceSpeedMs = Play.Lerp(0.3, 9.0, Play.Ease(phase / 35.0));
                eva.OrbitalSpeedMs = eva.SurfaceSpeedMs;
                eva.AccelMs2 = 1.63;
                vehicles.Add(eva.Sample(t));
            }

            SimStep step = SimStep.At(t).With([.. vehicles]);
            if (schedule.TryGetValue(frame, out List<GameSignal>? signals))
                step = step.Emit([.. signals]);
            yield return step;
        }
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        RunSummary? run = api.Run;
        if (run is null)
            throw new SimException("the soak scenario needs the run summary to assert on event counts");

        api.ExpectEventsStored(run.Events);
        api.ExpectVarDelta(
            "ingest_accepted", run.Events,
            "every envelope the pipeline produced was accepted on its first presentation");
        api.ExpectVarDelta(
            "ingest_deduped", 0,
            "nothing was shipped twice: the outbox drains in order and only deletes on a 200");
        api.Record(
            ok: run.Events >= 1_500,
            label: "volume",
            expected: ">= 1500 events",
            actual: $"{run.Events} events in {run.Batches} batches ({run.Frames} frames)",
            note: "§7.3 sizes this scenario at ~2k events including telemetry windows");

        api.ExpectCounter(handle, "orbits_achieved", MissionCount);
        api.ExpectCounter(handle, "stagings", MissionCount * StagingsPerMission);
        api.ExpectCounter(handle, "kittens_recovered", MissionCount * MissionCrew);
        api.ExpectCounter(handle, "kitten_tumbles", TumbleSpeeds.Length);
        api.ExpectCounter(handle, "dockings", 1);
        api.ExpectCounter(handle, "rud_total", 0);
    }

    private static List<SimVehicle> BuildResidents()
    {
        var residents = new List<SimVehicle>(ResidentCount);
        for (int i = 0; i < ResidentCount; i++)
        {
            // A believable long-lived population: a station and its tenants in kerbin orbit, a few
            // munar satellites, and a surface base with a shed.
            bool mun = i >= 22;
            bool surface = i >= 27;
            SimBody body = mun ? SimBodies.Mun : SimBodies.Kerbin;
            string id = surface && i == 27 ? EvaBaseId : $"resident-{i:00}";
            var v = new SimVehicle(
                id,
                surface ? $"Mun Base Module {i - 26}" : mun ? $"Munar Relay {i - 21}" : $"Station Element {i + 1}",
                body,
                crewCount: i % 7 == 0 ? 2 : 0,
                partCount: 6 + (i % 11),
                massKg: 900 + (i * 137));

            if (surface)
            {
                v.Rest("landed");
            }
            else if (mun)
            {
                v.Orbit(apAltM: 120_000 + (i * 2_500), peAltM: 90_000 + (i * 1_500), orbitalSpeedMs: 542 + i, surfaceSpeedMs: 538 + i);
            }
            else
            {
                v.Orbit(apAltM: 410_000 + (i * 4_000), peAltM: 396_000 + (i * 3_000), orbitalSpeedMs: 7_640 - (i * 3), surfaceSpeedMs: 7_460 - (i * 3));
            }

            residents.Add(v);
        }

        return residents;
    }

    private static IReadOnlyList<RosterKitten> Roster(double t) =>
    [
        new(EvaKitten, TravelledM: 900 + (t * 0.8), FastestMs: 29_940, Missions: 6, MissionTimeS: t, Kia: false),
        new("Jebediah Kerman", TravelledM: 1_400 + (t * 1.1), FastestMs: 30_120, Missions: 9, MissionTimeS: t * 1.4, Kia: false),
        new("Bob Kerman", TravelledM: 220 + (t * 0.2), FastestMs: 29_880, Missions: 2, MissionTimeS: t * 0.3, Kia: false),
    ];

    /// <summary>One launch-to-recovery mission, driven off its own relative clock.</summary>
    private sealed class Mission
    {
        private const double Length = 480.0;

        private readonly SimVehicle _vehicle;
        private readonly double _t0;

        internal Mission(int index, double startSimT)
        {
            _t0 = startSimT;
            _vehicle = new SimVehicle(
                $"mission-{index}", $"Expedition {index + 1}", SimBodies.Kerbin,
                crewCount: MissionCrew, partCount: 54, massKg: 38_000);
            _vehicle.Rest("landed");
        }

        internal string Id => _vehicle.Id;

        /// <summary>True while the vehicle exists; false from the recovery frame onwards.</summary>
        /// <param name="t">Universe sim seconds.</param>
        /// <returns>Whether the vehicle should appear in the frame.</returns>
        internal bool Alive(double t) => t >= _t0 && t < _t0 + Length;

        /// <summary>Registers this mission's discrete signals on the scenario's schedule.</summary>
        /// <param name="at">The scenario's scheduling callback.</param>
        internal void Schedule(Action<double, GameSignal[]> at)
        {
            long w(double r) => SimClock.Wall(_t0 + r);
            double s(double r) => _t0 + r;

            at(s(0), [new VehicleCreatedSignal(
                s(0), w(0), Id, _vehicle.Name, SimBodies.Kerbin.Name, _vehicle.MassKg, _vehicle.PartCount,
                MissionCrew, LaunchGameTime: s(0))]);

            at(s(3), [
                new EngineSignal(s(3), w(3), Id, EngineEventKind.Ignition, "RE-M3 Mainsail", 3),
                new StagingSignal(s(3), w(3), Id, 0),
            ]);
            at(s(80), [
                new EngineSignal(s(80), w(80), Id, EngineEventKind.Shutdown, "RE-M3 Mainsail", 3),
                new StagingSignal(s(80), w(80), Id, 1),
                new EngineSignal(s(80), w(80), Id, EngineEventKind.Ignition, "LV-909 Terrier", 1),
            ]);
            at(s(170), [new EngineSignal(s(170), w(170), Id, EngineEventKind.Shutdown, "LV-909 Terrier", 1)]);
            at(s(360), [new EngineSignal(s(360), w(360), Id, EngineEventKind.Ignition, "LV-909 Terrier", 1)]);
            at(s(400), [
                new EngineSignal(s(400), w(400), Id, EngineEventKind.Shutdown, "LV-909 Terrier", 1),
                new StagingSignal(s(400), w(400), Id, 2),
            ]);
            at(s(470), [
                new StagingSignal(s(470), w(470), Id, 3),
                new SplashSignal(s(470), w(470), Id, SpeedMs: 6.1, EnergyJ: Play.Energy(4_600, 6.1),
                    Body: SimBodies.Kerbin.Name, CrewCount: MissionCrew),
            ]);
            at(s(Length), [new VehicleRecoveredSignal(s(Length), w(Length), Id, MissionCrew)]);
        }

        /// <summary>Advances the flight profile to <paramref name="t"/> and samples it.</summary>
        /// <param name="t">Universe sim seconds.</param>
        /// <returns>The snapshot.</returns>
        internal TelemetrySnapshot Sample(double t)
        {
            double r = t - _t0;
            switch (r)
            {
                case < 3:
                    _vehicle.Rest("landed");
                    break;

                case < 80:
                {
                    double u = (r - 3) / 77.0;
                    _vehicle.Fly(Play.Lerp(0, 46_000, Play.Ease(u)), Play.Lerp(0, 1_600, Play.Ease(u)),
                        Play.Lerp(12, 31, u), 39_000 * Math.Exp(-9 * (u - 0.3) * (u - 0.3)));
                    _vehicle.PeAltM = Play.Lerp(-620_000, -380_000, u);
                    _vehicle.ApAltM = Play.Lerp(0, 94_000, u);
                    break;
                }

                case < 170:
                {
                    double u = (r - 80) / 90.0;
                    _vehicle.Fly(Play.Lerp(46_000, 128_000, Play.Ease(u)), Play.Lerp(1_600, 7_380, Play.Ease(u)),
                        Play.Lerp(9, 19, u), Play.Lerp(8_800, 0, Play.Ease(Math.Min(1.0, u * 2.4))));
                    _vehicle.OrbitalSpeedMs = Play.Lerp(1_600, 7_690, Play.Ease(u));
                    _vehicle.PeAltM = Play.Lerp(-380_000, 38_000, Play.Ease(u));
                    _vehicle.ApAltM = Play.Lerp(94_000, 300_000, Play.Ease(u));
                    _vehicle.Ecc = Play.Lerp(0.84, 0.04, Play.Ease(u));
                    break;
                }

                case < 360:
                    _vehicle.Orbit(apAltM: 300_000, peAltM: 282_000, orbitalSpeedMs: 7_752, surfaceSpeedMs: 7_578);
                    _vehicle.IncDeg = 51.6;
                    break;

                case < 400:
                {
                    double u = (r - 360) / 40.0;
                    _vehicle.Orbit(300_000, Play.Lerp(282_000, -42_000, Play.Ease(u)), 7_690, 7_520);
                    _vehicle.AltitudeM = Play.Lerp(282_000, 172_000, Play.Ease(u));
                    _vehicle.Situation = "maneuvering";
                    _vehicle.AccelMs2 = 2.8;
                    _vehicle.PeakG = 0.29;
                    break;
                }

                case < 470:
                {
                    double u = (r - 400) / 70.0;
                    _vehicle.MassKg = 4_600;
                    _vehicle.Fly(
                        Play.Lerp(172_000, 0, Play.Ease(u)),
                        u < 0.55 ? Play.Lerp(7_520, 240, u / 0.55) : Play.Lerp(240, 6.1, (u - 0.55) / 0.45),
                        58.0 * Math.Exp(-26 * (u - 0.45) * (u - 0.45)),
                        44_000 * Math.Exp(-24 * (u - 0.45) * (u - 0.45)));
                    _vehicle.OrbitalSpeedMs = _vehicle.SurfaceSpeedMs;
                    _vehicle.PeAltM = -42_000;
                    _vehicle.ApAltM = Play.Lerp(300_000, 0, Play.Ease(u));
                    break;
                }

                default:
                    _vehicle.Rest("floating");
                    break;
            }

            return _vehicle.Sample(t);
        }
    }
}
