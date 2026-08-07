using System;
using System.Collections.Generic;
using System.Globalization;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>How a player plays. Drives fleet size, mission cadence and appetite for risk.</summary>
internal enum PlayStyle
{
    /// <summary>Few flights, small craft, mostly suborbital, almost never a record.</summary>
    Rookie,

    /// <summary>The modal player: a handful of missions, one or two resident craft.</summary>
    Regular,

    /// <summary>A standing fleet and a steady mission cadence.</summary>
    Veteran,

    /// <summary>A station keeper: lots of residents, few launches, dockings.</summary>
    Engineer,

    /// <summary>Crashes things on purpose. Where most of the RUDs and the records come from.</summary>
    Daredevil,
}

/// <summary>The flight profile a mission flies.</summary>
internal enum MissionProfile
{
    /// <summary>Up and straight back down.</summary>
    Hop,

    /// <summary>To orbit and back.</summary>
    Orbit,

    /// <summary>To orbit, out of the home SOI, and into another one.</summary>
    Interplanetary,
}

/// <summary>How a mission finishes.</summary>
internal enum MissionEnd
{
    /// <summary>Touched down and was recovered.</summary>
    Recovered,

    /// <summary>Splashed down and was recovered.</summary>
    Splashdown,

    /// <summary>Rapidly and unscheduledly disassembled.</summary>
    Rud,

    /// <summary>Left the simulation without a verdict (still out there).</summary>
    Despawned,

    /// <summary>The player scuttled it. The only path that marks a kitten KIA (D11).</summary>
    ManualDestroy,
}

/// <summary>The moderation path a player exercises after its events have landed, if any.</summary>
internal enum ModerationRole
{
    /// <summary>Nothing; the overwhelming majority.</summary>
    None,

    /// <summary>Reissues its license half way through the run and finishes on the new credential.</summary>
    Reissue,

    /// <summary>Revokes its own credentials once it has finished shipping.</summary>
    Revoke,

    /// <summary>Is banned through the loopback admin API.</summary>
    Ban,

    /// <summary>Deletes its own account and data from the dashboard API.</summary>
    Delete,
}

/// <summary>
/// One player's whole simulated save: a resident fleet, a sequence of missions, EVAs, dockings,
/// integrity flags, roster snapshots and the occasional save reload, merged into one frame
/// timeline.
/// </summary>
/// <remarks>
/// <para>
/// Nothing here builds an <see cref="EventEnvelope"/>. A script emits exactly what a Harmony patch
/// can produce — <see cref="TelemetrySnapshot"/>s and <see cref="GameSignal"/>s — and the real
/// detector, window accumulator, impact correlator, outbox, proof signer and shipper do the rest.
/// That is the whole point: a regression anywhere in the client shows up here as a wrong number,
/// not as a number the harness invented.
/// </para>
/// <para>
/// Every draw comes from this player's own <see cref="Prng"/>, in a fixed order, on one thread.
/// Two runs with the same <c>--seed</c> therefore produce the same events for player <c>i</c>
/// however the players are scheduled.
/// </para>
/// </remarks>
internal sealed class PlayerScript
{
    /// <summary>Frame length in sim seconds — the D15 sampling cadence exactly, so the runner's
    /// real <see cref="SampleClock"/> passes every frame rather than dropping most of them.</summary>
    internal const double Dt = 1.0 / Wire.DefaultSampleHz;

    private static readonly string[] KittenNames =
    [
        "Jebediah", "Bill", "Bob", "Valentina", "Mortimer", "Gene", "Wernher", "Linus",
        "Hazel", "Pip", "Marmalade", "Biscuit", "Domino", "Tuppence", "Saffron", "Bramble",
    ];

    private static readonly RudCause[] RudCauses =
    [
        RudCause.GroundImpact, RudCause.OceanImpact, RudCause.Collision,
        RudCause.ExcessiveGForce, RudCause.AerodynamicForces, RudCause.HydrodynamicForces,
    ];

    private static readonly FlightFlag[] VehicleFlags =
    [
        FlightFlag.Teleport, FlightFlag.Refuel, FlightFlag.ResourceEdit, FlightFlag.Console,
    ];

    private readonly LoadClock _clock;
    private readonly Prng _rng;
    private readonly double _duration;
    private readonly int _index;
    private readonly List<ResidentActor> _residents = [];
    private readonly List<MissionActor> _missions = [];
    private readonly List<EvaActor> _evas = [];
    private readonly List<string> _crew = [];
    private readonly Dictionary<int, List<GameSignal>> _schedule = [];
    private readonly List<double> _saveLoads = [];

    /// <summary>Builds a player's script. Every decision is drawn here, in order.</summary>
    /// <param name="index">The player's zero-based index.</param>
    /// <param name="durationSeconds">Simulated play, in sim seconds.</param>
    /// <param name="clock">The run's sim-time → wall-time mapping.</param>
    /// <param name="rng">This player's generator.</param>
    /// <param name="moderationPercent">Percentage of players that exercise a moderation path.</param>
    /// <param name="dashboard">
    /// True when this player has a website session. Under <c>--auth admin</c> there is none — the
    /// mode exists precisely to skip the identity stack — so the three roles that go through the
    /// dashboard API are not available and are dropped rather than attempted and reported as
    /// failures. The draw itself is unchanged either way, so the two modes stay comparable.
    /// </param>
    internal PlayerScript(
        int index, double durationSeconds, LoadClock clock, Prng rng, int moderationPercent, bool dashboard)
    {
        _index = index;
        _clock = clock;
        _rng = rng;
        _duration = Math.Max(120.0, durationSeconds);

        ModerationRole drawn = DrawRole(moderationPercent);
        Role = dashboard || drawn is ModerationRole.None or ModerationRole.Ban
            ? drawn
            : ModerationRole.None;
        RoleSkipped = Role != drawn;
        Style = _rng.Weighted([
            (PlayStyle.Rookie, 22.0),
            (PlayStyle.Regular, 38.0),
            (PlayStyle.Veteran, 20.0),
            (PlayStyle.Engineer, 12.0),
            (PlayStyle.Daredevil, 8.0),
        ]);

        DrawCrew();
        DrawResidents();
        DrawMissions();
        DrawEvas();
        DrawSaveLoads();
    }

    /// <summary>How this player plays.</summary>
    internal PlayStyle Style { get; }

    /// <summary>The moderation path this player exercises, if any.</summary>
    internal ModerationRole Role { get; }

    /// <summary>True when a drawn moderation role was dropped for want of a website session.</summary>
    internal bool RoleSkipped { get; }

    /// <summary>How many missions this player flies.</summary>
    internal int MissionCount => _missions.Count;

    /// <summary>How many craft sit in this player's save for the whole run.</summary>
    internal int ResidentCount => _residents.Count;

    /// <summary>How many frames the timeline is.</summary>
    internal int FrameCount => (int)Math.Round(_duration / Dt) + 1;

    /// <summary>The sim instant half way through, where a mid-run reissue happens.</summary>
    internal double MidPoint => _duration * 0.5;

    /// <summary>The client unix-millisecond stamp for a sim instant.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    internal long Wall(double simT) => _clock.Wall(simT);

    /// <summary>One line describing this player, for <c>--verbose</c> and the report.</summary>
    /// <returns>The description.</returns>
    internal string Describe()
    {
        string counts = string.Create(CultureInfo.InvariantCulture,
            $"{_residents.Count} resident, {_missions.Count} mission, {_evas.Count} eva, {_saveLoads.Count} reload");
        return Style.ToString().ToLowerInvariant() + ": " + counts
               + (Role == ModerationRole.None ? string.Empty : ", " + Role.ToString().ToLowerInvariant());
    }

    /// <summary>The frames to feed through the pipeline, generated lazily.</summary>
    /// <returns>The frames, in increasing sim time.</returns>
    internal IEnumerable<SimStep> Steps()
    {
        int frames = FrameCount;
        var live = new List<TelemetrySnapshot>(_residents.Count + 4);

        for (int frame = 0; frame < frames; frame++)
        {
            double t = frame * Dt;

            live.Clear();
            foreach (ResidentActor resident in _residents)
            {
                if (resident.Alive(t))
                    live.Add(resident.Sample(t));
            }

            foreach (MissionActor mission in _missions)
            {
                if (mission.Alive(t))
                    live.Add(mission.Sample(t));
            }

            foreach (EvaActor eva in _evas)
            {
                if (eva.Alive(t))
                    live.Add(eva.Sample(t));
            }

            SimStep step = SimStep.At(t).With([.. live]);
            if (_schedule.TryGetValue(frame, out List<GameSignal>? signals))
                step = step.Emit([.. signals]);
            yield return step;
        }
    }

    // --- drawing ---------------------------------------------------------------------

    private ModerationRole DrawRole(int moderationPercent)
    {
        if (moderationPercent <= 0 || !_rng.Chance(moderationPercent / 100.0))
            return ModerationRole.None;

        // Reissue is the common real-world case (a player who lost their credential file);
        // deletion is the rarest and the most destructive, so it stays rarest here too.
        return _rng.Weighted([
            (ModerationRole.Reissue, 45.0),
            (ModerationRole.Revoke, 25.0),
            (ModerationRole.Ban, 18.0),
            (ModerationRole.Delete, 12.0),
        ]);
    }

    private void DrawCrew()
    {
        int count = Style switch
        {
            PlayStyle.Rookie => _rng.Int(2, 4),
            PlayStyle.Veteran or PlayStyle.Engineer => _rng.Int(4, 9),
            _ => _rng.Int(3, 6),
        };

        var used = new HashSet<int>();
        for (int i = 0; i < count; i++)
        {
            int pick = _rng.Int(0, KittenNames.Length);
            while (!used.Add(pick))
                pick = (pick + 1) % KittenNames.Length;
            _crew.Add($"{KittenNames[pick]} Kerman");
        }
    }

    private void DrawResidents()
    {
        int count = Style switch
        {
            PlayStyle.Rookie => _rng.Int(0, 2),
            PlayStyle.Regular => _rng.Int(1, 4),
            PlayStyle.Veteran => _rng.Int(3, 9),
            PlayStyle.Engineer => _rng.Int(6, 16),
            _ => _rng.Int(0, 3),
        };

        for (int i = 0; i < count; i++)
        {
            var resident = new ResidentActor($"p{_index}-res-{i:00}", i, _rng, _clock);
            _residents.Add(resident);
            Emit(0, resident.Created());
        }

        // Station keeping: a couple of the residents dock and separate again. A docking needs two
        // live craft and a flight id for the partner, which is why it is scheduled here rather
        // than inside an actor.
        if (_residents.Count >= 2)
        {
            int dockings = Style == PlayStyle.Engineer ? _rng.Int(1, 5) : _rng.Int(0, 2);
            for (int i = 0; i < dockings; i++)
            {
                int a = _rng.Int(0, _residents.Count);
                int b = (a + 1 + _rng.Int(0, _residents.Count - 1)) % _residents.Count;
                double t = _rng.Range(60, Math.Max(120, _duration - 180));
                double undock = t + _rng.Range(40, 400);

                Emit(t, new DockSignal(t, Wall(t), _residents[a].Id, _residents[b].Id));
                if (undock < _duration - 10)
                    Emit(undock, new UndockSignal(undock, Wall(undock), _residents[a].Id, _residents[b].Id));
            }
        }
    }

    private void DrawMissions()
    {
        double cadence = Style switch
        {
            PlayStyle.Rookie => _rng.Range(700, 1400),
            PlayStyle.Regular => _rng.Range(450, 900),
            PlayStyle.Veteran => _rng.Range(320, 700),
            PlayStyle.Engineer => _rng.Range(800, 1600),
            _ => _rng.Range(240, 520),
        };

        double t = _rng.Range(10, 120);
        int ordinal = 0;
        while (t < _duration - 90 && ordinal < 400)
        {
            MissionProfile profile = _rng.Weighted([
                (MissionProfile.Hop, Style == PlayStyle.Rookie ? 70.0 : 34.0),
                (MissionProfile.Orbit, Style == PlayStyle.Rookie ? 25.0 : 48.0),
                (MissionProfile.Interplanetary, Style is PlayStyle.Veteran or PlayStyle.Engineer ? 24.0 : 10.0),
            ]);

            MissionEnd end = _rng.Weighted([
                (MissionEnd.Recovered, Style == PlayStyle.Daredevil ? 34.0 : 56.0),
                (MissionEnd.Splashdown, 18.0),
                (MissionEnd.Rud, Style == PlayStyle.Daredevil ? 40.0 : 16.0),
                (MissionEnd.Despawned, 7.0),
                (MissionEnd.ManualDestroy, 3.0),
            ]);

            // Coverage, deliberately: leaving all six causes to the weights means the rarest one
            // is absent from most runs, and "all six RUD causes are exercised" is a property this
            // harness is supposed to have. The first RUD a player flies takes the cause its index
            // names, so any run with six or more players covers the enum.
            RudCause cause = ordinal == 0 || _rng.Chance(0.25)
                ? RudCauses[(_index + ordinal) % RudCauses.Length]
                : _rng.Weighted([
                    (RudCause.GroundImpact, 34.0),
                    (RudCause.OceanImpact, 14.0),
                    (RudCause.Collision, 16.0),
                    (RudCause.ExcessiveGForce, 14.0),
                    (RudCause.AerodynamicForces, 14.0),
                    (RudCause.HydrodynamicForces, 8.0),
                ]);

            var mission = new MissionActor(
                id: $"p{_index}-m{ordinal:000}",
                ordinal: ordinal,
                startT: t,
                profile: profile,
                end: end,
                cause: cause,
                style: Style,
                crew: _crew,
                rng: _rng,
                clock: _clock);

            if (mission.EndT > _duration)
                break;

            mission.Schedule(Emit);
            _missions.Add(mission);

            // An integrity flag now and then, on a flight and on the session as a whole. A flagged
            // flight is excluded from every record board, so this is the path that keeps the
            // projector's exclusion honest under load.
            if (_rng.Chance(0.035))
            {
                double flagT = mission.StartT + _rng.Range(5, Math.Max(10, mission.Length * 0.6));
                FlightFlag flag = _rng.Pick(VehicleFlags);
                Emit(flagT, new FlaggedSignal(flagT, Wall(flagT), mission.Id, flag, "loadgen: " + flag));
            }

            t += cadence * _rng.Range(0.55, 1.7);
            ordinal++;
        }

        // Live tuning is session-wide (it edits the tumble-speed gate), so it taints every flight
        // in the session including ones started later — a different code path from a per-vehicle
        // flag, and worth exercising.
        if (_rng.Chance(0.02))
        {
            double flagT = _rng.Range(30, Math.Max(60, _duration * 0.7));
            Emit(flagT, new FlaggedSignal(flagT, Wall(flagT), null, FlightFlag.Tuning, "loadgen: debug window open"));
        }

        // Roster snapshots on §4.2's ten-minute cadence, and one at session end.
        for (double r = 600; r < _duration; r += 600)
            Emit(r, new RosterSampleSignal(r, Wall(r), Roster(r)));
        double last = Math.Max(0, _duration - Dt);
        Emit(last, new RosterSampleSignal(last, Wall(last), Roster(last)));
    }

    private void DrawEvas()
    {
        int count = Style switch
        {
            PlayStyle.Rookie => _rng.Int(0, 2),
            PlayStyle.Veteran or PlayStyle.Engineer => _rng.Int(1, 4),
            PlayStyle.Daredevil => _rng.Int(1, 5),
            _ => _rng.Int(0, 3),
        };

        for (int i = 0; i < count; i++)
        {
            double start = _rng.Range(30, Math.Max(60, _duration - 260));
            double length = _rng.Range(90, 400);
            if (start + length > _duration - 5)
                continue;

            var eva = new EvaActor(
                id: $"p{_index}-eva-{i}",
                kitten: _crew[_rng.Int(0, _crew.Count)],
                body: _rng.Pick(LoadBodies.All),
                startT: start,
                length: length,
                // A daredevil's kitten falls over a lot more. The tumble gate is 6.5 m/s in stock
                // KSA and the game classifies the transition, so the harness emits the signal the
                // game would emit rather than re-deriving the rule.
                tumbles: Style == PlayStyle.Daredevil ? _rng.Int(2, 9) : _rng.Int(0, 4),
                rng: _rng,
                clock: _clock);

            eva.Schedule(Emit);
            _evas.Add(eva);
        }
    }

    private void DrawSaveLoads()
    {
        // A save load is a hard teardown: the pipeline drops its detector, its windows, its
        // correlator and its open flights and starts a new session. It is rare in play and it is
        // the single most disruptive thing that can happen to the client, so it is worth having a
        // few of them in a large run and worth keeping them rare.
        if (_duration < 900 || !_rng.Chance(0.12))
            return;

        double t = _rng.Range(_duration * 0.3, _duration * 0.8);
        _saveLoads.Add(t);
        Emit(t, new SessionLoadedSignal(t, Wall(t), PlayerRunner.GameBuild, PlayerRunner.ModVersion));

        // Everything still in the save is re-registered by the game after a load. Without this the
        // residents would carry on producing telemetry for a flight that has no `flight.started` —
        // a phantom flight, which is a bug in the harness rather than a fact about the client.
        foreach (ResidentActor resident in _residents)
            Emit(t, resident.Recreated(t));
    }

    private IReadOnlyList<RosterKitten> Roster(double t)
    {
        var rows = new List<RosterKitten>(_crew.Count);
        for (int i = 0; i < _crew.Count; i++)
        {
            // Monotonic in t, and a pure function of (player, kitten, t) so a re-run reproduces it.
            double scale = 0.4 + (0.25 * ((i * 7 % 5) + 1));
            rows.Add(new RosterKitten(
                Name: _crew[i],
                TravelledM: (400 * (i + 1)) + (t * scale * 1.7),
                // Ecliptic-frame, so ~30 km/s on the home world. Recorded for completeness; the
                // speed boards come from telemetry.window.
                FastestMs: 29_800 + (i * 37) + (t * 0.02),
                Missions: 1 + i + (int)(t / 900),
                MissionTimeS: t * scale,
                Kia: false));
        }

        return rows;
    }

    // --- scheduling ------------------------------------------------------------------

    private void Emit(double t, params GameSignal[] signals)
    {
        if (signals.Length == 0)
            return;

        int frame = Math.Clamp((int)Math.Round(t / Dt), 0, FrameCount - 1);
        if (!_schedule.TryGetValue(frame, out List<GameSignal>? bucket))
            _schedule[frame] = bucket = [];
        bucket.AddRange(signals);
    }
}
