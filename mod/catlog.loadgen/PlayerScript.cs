using System;
using System.Collections.Generic;
using System.Globalization;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

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
/// One player's career, sliced to the run's window: a standing fleet, a sequence of missions whose
/// ambition is gated on how long they have been playing, EVAs, dockings, integrity flags, roster
/// snapshots and the occasional save reload, merged into one frame timeline.
/// </summary>
/// <remarks>
/// <para>
/// <b>This is a career, not a bag of flights.</b> A player arrives with in-game time already on the
/// clock (<see cref="PriorSeconds"/>), that clock advances monotonically through the run, and what
/// they are capable of attempting is a function of it and of nothing else. A rookie flies pad tests
/// and hops and loses a lot of them on the pad; an explorer has a station, three things in flight
/// at once and a probe on its way to the outer system, and the flights they lose are lost on final
/// approach. See <see cref="Careers"/> for every rule and every number.
/// </para>
/// <para>
/// Nothing here builds an <see cref="EventEnvelope"/>. A script emits exactly what a Harmony patch
/// can produce — <see cref="TelemetrySnapshot"/>s and <see cref="GameSignal"/>s — and the real
/// detector, window accumulator, impact correlator, outbox, proof signer and shipper do the rest.
/// That is the whole point: a regression anywhere in the client shows up here as a wrong number,
/// not as a number the harness invented.
/// </para>
/// <para>
/// <b>Reproducibility.</b> Every draw comes from this player's own <see cref="Prng"/>, in a fixed
/// order, on one thread. The career model adds branching but no new sources of entropy: planning
/// draws first and in full, the coverage pass rewrites fields without drawing, and the actors are
/// built afterwards. Two runs with the same <c>--seed</c> therefore produce the same events for
/// player <c>i</c> however the players are scheduled.
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

    private static readonly FlightFlag[] VehicleFlags =
    [
        FlightFlag.Teleport, FlightFlag.Refuel, FlightFlag.ResourceEdit, FlightFlag.Console,
    ];

    private readonly CareerClock _clock;
    private readonly Prng _rng;
    private readonly double _duration;
    private readonly double _epoch;
    private readonly double _end;
    private readonly int _index;
    private readonly int _cohort;
    private readonly List<ResidentActor> _residents = [];
    private readonly List<MissionActor> _missions = [];
    private readonly List<EvaActor> _evas = [];
    private readonly List<string> _crew = [];
    private readonly Dictionary<int, List<GameSignal>> _schedule = [];
    private readonly List<double> _saveLoads = [];
    private readonly int[] _byKind = new int[Careers.Kinds.Length];
    private readonly int[] _byPhase = new int[Careers.Phases.Length];
    private readonly int[] _byCause = new int[Careers.Causes.Length];
    private readonly List<LoadBody> _reached = [];

    private int _completed;

    /// <summary>Builds a player's career. Every decision is drawn here, in order.</summary>
    /// <param name="index">The player's zero-based index; keys their random stream.</param>
    /// <param name="cohort">
    /// The player's position among the players that actually ran, which is what the coverage
    /// rotations are keyed on — the career ladder and the RUD cause.
    /// <para>
    /// It is deliberately <b>not</b> <paramref name="index"/>. Identities refused by the ≥30-day
    /// age gate never become players, so the surviving indices have holes in them, and a rotation
    /// keyed on a sparse index can drop a whole rung: a fourteen-player run that loses index 11
    /// loses its only explorer. Keying the rotations on a dense position makes the guarantee hold
    /// while the random stream stays a function of <c>(seed, index)</c> alone.
    /// </para>
    /// </param>
    /// <param name="durationSeconds">The run's window on the career, in sim seconds.</param>
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
        int index,
        int cohort,
        double durationSeconds,
        LoadClock clock,
        Prng rng,
        int moderationPercent,
        bool dashboard)
    {
        _index = index;
        _cohort = cohort;
        _rng = rng;
        _duration = Math.Max(120.0, durationSeconds);

        ModerationRole drawn = DrawRole(moderationPercent);
        Role = dashboard || drawn is ModerationRole.None or ModerationRole.Ban
            ? drawn
            : ModerationRole.None;
        RoleSkipped = Role != drawn;

        Temperament = Careers.DrawTemperament(_rng);
        PriorSeconds = Careers.DrawPriorSeconds(_rng, _cohort);
        StartStage = Careers.StageFor(PriorSeconds);
        EndStage = Careers.StageFor(PriorSeconds + _duration);

        // Everything from here on is in career seconds, because that is what sim_t is.
        _epoch = PriorSeconds;
        _end = _epoch + _duration;
        _clock = new CareerClock(clock, _epoch);

        DrawCrew();
        DrawResidents();

        List<MissionSpec> plan = PlanMissions();
        EnsureRudCoverage(plan);
        BuildMissions(plan);

        DrawEvas();
        DrawSessionFlag();
        DrawSaveLoads();
        ScheduleRoster();
    }

    /// <summary>How this player plays, independently of how far they have got.</summary>
    internal Temperament Temperament { get; }

    /// <summary>In-game seconds this player had accumulated before the run's window opened.</summary>
    internal double PriorSeconds { get; }

    /// <summary>The stage the player was at when the window opened.</summary>
    internal CareerStage StartStage { get; }

    /// <summary>The stage the player was at when it closed.</summary>
    internal CareerStage EndStage { get; }

    /// <summary>The moderation path this player exercises, if any.</summary>
    internal ModerationRole Role { get; }

    /// <summary>True when a drawn moderation role was dropped for want of a website session.</summary>
    internal bool RoleSkipped { get; }

    /// <summary>How many frames the timeline is.</summary>
    internal int FrameCount => (int)Math.Round(_duration / Dt) + 1;

    /// <summary>Career seconds at the instant the run's window opens — the first frame's <c>sim_t</c>.</summary>
    internal double Epoch => _epoch;

    /// <summary>The career instant half way through the window, where a mid-run reissue happens.</summary>
    internal double MidPoint => _epoch + (_duration * 0.5);

    /// <summary>What this career looked like, for the run report.</summary>
    internal CareerSummary Summary => new(
        Temperament: Temperament,
        StartStage: StartStage,
        EndStage: EndStage,
        PriorHours: PriorSeconds / 3600.0,
        CareerHours: (PriorSeconds + _duration) / 3600.0,
        Fleet: _residents.Count,
        Attempted: _missions.Count,
        Completed: _completed,
        ByKind: _byKind,
        FailuresByPhase: _byPhase,
        CausesByKind: _byCause,
        BodiesReached: BodyNames());

    /// <summary>The client unix-millisecond stamp for a career instant.</summary>
    /// <param name="simT">Career sim seconds.</param>
    /// <returns>Unix milliseconds.</returns>
    internal long Wall(double simT) => _clock.Wall(simT);

    /// <summary>One line describing this player, for <c>--verbose</c> and the report.</summary>
    /// <returns>The description.</returns>
    internal string Describe()
    {
        string stage = StartStage == EndStage
            ? StartStage.ToString().ToLowerInvariant()
            : StartStage.ToString().ToLowerInvariant() + "→" + EndStage.ToString().ToLowerInvariant();
        string counts = string.Create(CultureInfo.InvariantCulture,
            $"{PriorSeconds / 3600.0:0.#}h, {_residents.Count} fleet, {_missions.Count} mission, "
            + $"{_missions.Count - _completed} lost, {_evas.Count} eva");
        return stage + "/" + Temperament.ToString().ToLowerInvariant() + ": " + counts
               + (Role == ModerationRole.None ? string.Empty : ", " + Role.ToString().ToLowerInvariant());
    }

    /// <summary>The frames to feed through the pipeline, generated lazily.</summary>
    /// <returns>The frames, in increasing sim time.</returns>
    internal IEnumerable<SimStep> Steps()
    {
        int frames = FrameCount;
        var live = new List<TelemetrySnapshot>(_residents.Count + 8);

        for (int frame = 0; frame < frames; frame++)
        {
            double t = _epoch + (frame * Dt);

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

    // --- population ------------------------------------------------------------------

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
        // A roster grows with a career: a rookie has the three kittens the game starts them with,
        // an explorer has hired most of the list.
        int count = StartStage switch
        {
            CareerStage.Rookie => _rng.Int(2, 4),
            CareerStage.Suborbital => _rng.Int(3, 5),
            CareerStage.Orbital => _rng.Int(3, 7),
            CareerStage.Operator => _rng.Int(4, 9),
            CareerStage.Interplanetary => _rng.Int(5, 11),
            _ => _rng.Int(6, 13),
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
        int count = Careers.DrawFleet(_rng, StartStage, Temperament);

        // A resident can only be somewhere the career has already been. A player who has never
        // left the home SOI does not have a relay around Duna, however long they have played.
        var homes = new List<LoadBody> { LoadBodies.Earth };
        homes.AddRange(LoadBodies.ReachableAt(StartStage));

        // A career that can rendezvous always has something in home orbit to rendezvous with. It is
        // what an operator's save looks like, and it is what makes vehicle.docked deterministic.
        bool station = StartStage >= CareerStage.Operator;

        for (int i = 0; i < count; i++)
        {
            LoadBody body = homes.Count == 1
                ? LoadBodies.Earth
                // Most of a fleet stays at home even for an explorer; the far-flung elements are
                // the minority, exactly as they are in a real save.
                : (_rng.Chance(0.55) ? LoadBodies.Earth : _rng.Pick(homes));
            if (i == 0 && station)
                body = LoadBodies.Earth;

            // Craft that were launched before the window opened: their launch is somewhere in the
            // career's past, which is what makes flight identity stable across a save load.
            double launchedAt = Math.Max(0, _epoch - _rng.Range(600, Math.Max(1_200, _epoch * 0.9)));
            var resident = new ResidentActor(
                $"p{_index}-res-{i:00}", i, body, launchedAt, mustOrbit: i == 0 && station, _rng, _clock);
            _residents.Add(resident);
            Emit(_epoch, resident.Created());

            // A resident sitting round Mars is evidence the career has been to Mars, but it does
            // not produce a `vehicle.soi` inside this window — it was already there when the window
            // opened. Leaving it out of BodiesReached is what makes that line comparable, player
            // for player, with the `soi_bodies` board the run is about to be checked against.
        }

        // Station keeping: a couple of the residents dock and separate again. A docking needs two
        // live craft and a flight id for the partner, which is why it is scheduled here rather
        // than inside an actor — and it needs the two craft to be in the same place, which a
        // uniform pick over a fleet spread across the system does not give.
        if (_residents.Count >= 2)
        {
            int dockings = Temperament == Temperament.Engineer ? _rng.Int(1, 5) : _rng.Int(0, 2);
            for (int i = 0; i < dockings; i++)
            {
                int a = _rng.Int(0, _residents.Count);
                int b = (a + 1 + _rng.Int(0, _residents.Count - 1)) % _residents.Count;
                double t = _epoch + _rng.Range(60, Math.Max(120, _duration - 180));
                double undock = t + _rng.Range(40, 400);

                ResidentActor first = _residents[a];
                ResidentActor second = _residents[b];
                if (first.Landed || second.Landed || !ReferenceEquals(first.Body, second.Body))
                    continue;

                Emit(t, new DockSignal(t, Wall(t), first.Id, second.Id));
                if (undock < _end - 10)
                    Emit(undock, new UndockSignal(undock, Wall(undock), first.Id, second.Id));
            }
        }
    }

    // --- mission planning --------------------------------------------------------------

    /// <summary>
    /// Lays out the career's flights across the window.
    /// </summary>
    /// <remarks>
    /// <para>
    /// The loop walks in-game time forwards, re-reads the stage at every launch (so a career that
    /// crosses a threshold mid-run starts flying the next thing it unlocked), and refuses to start
    /// more craft than the stage can keep track of at once — which is where fleet growth turns into
    /// <i>concurrency</i> growth rather than just a bigger number in the report.
    /// </para>
    /// <para>
    /// Two launches are forced rather than drawn, and both are coverage guarantees rather than
    /// gameplay claims: an interplanetary career opens the window with a transfer already going,
    /// and an operator career flies a rendezvous. Without them "did this run contain an SOI change"
    /// would be a coin flip and the taxonomy invariant would be flaky. Both are things the careers
    /// in question do constantly anyway; the guarantee only fixes <i>when</i>.
    /// </para>
    /// </remarks>
    private List<MissionSpec> PlanMissions()
    {
        var plan = new List<MissionSpec>();
        var inFlight = new List<double>();
        double t = _epoch + _rng.Range(8, 90);
        int ordinal = 0;

        while (ordinal < 400 && t < _end - 40)
        {
            CareerStage stage = Careers.StageFor(t);
            t = NextFreeSlot(inFlight, t, Careers.MaxConcurrent(stage));
            if (t >= _end - 40)
                break;

            stage = Careers.StageFor(t);
            bool canDock = stage >= CareerStage.Operator && HasHomeStation();

            // Drawn first, then possibly overridden: keeping the draw unconditional is what makes
            // the guaranteed launches free of any effect on the rest of the random stream.
            MissionKind kind = Careers.DrawKind(_rng, stage, Temperament, canDock);
            bool forced = false;
            if (ordinal == 0 && stage >= CareerStage.Interplanetary)
            {
                kind = MissionKind.Transfer;
                forced = true;
            }
            else if (ordinal == 0 && stage >= CareerStage.Operator && canDock)
            {
                kind = MissionKind.Rendezvous;
                forced = true;
            }
            else if (ordinal == 1 && stage >= CareerStage.Interplanetary && canDock)
            {
                kind = MissionKind.Rendezvous;
                forced = true;
            }

            LoadBody destination = DrawDestination(kind, stage, forced);
            double length = Careers.DrawLength(_rng, kind, destination);
            LoadBody terminal = TerminalBody(kind, destination);
            bool overWater = terminal.Ocean && Descends(kind) && _rng.Chance(0.34);
            int crew = DrawCrewCount(kind);
            bool spectacular = Temperament == Temperament.Daredevil ? _rng.Chance(0.2) : _rng.Chance(0.025);
            bool lost = _rng.Chance(Careers.FailureChance(stage, kind, Temperament));
            FlightPhase phase = lost ? Careers.DrawFailPhase(_rng, stage, kind) : FlightPhase.None;
            RudCause cause = lost ? Careers.DrawCause(_rng, phase, overWater) : RudCause.GroundImpact;
            MissionOutcome outcome = lost ? MissionOutcome.Rud : DrawSuccess(kind, overWater);
            // A rendezvous launches from Earth and stays in Earth orbit, so the thing it meets has
            // to be in Earth orbit too. Meeting a surface base on Luna would put a docking on the
            // board that could not physically have happened.
            string? partner = kind == MissionKind.Rendezvous ? PickOrbitingPartner(LoadBodies.Earth) : null;

            // Nothing may run past the end of the window: a flight cut off by the frame loop would
            // have no verdict, which is a harness bug rather than a fact about the client.
            double room = _end - t - 20;
            double floor = Floor(kind);
            if (room < floor)
            {
                if (!forced)
                {
                    t += Careers.DrawCadence(_rng, stage, Temperament) * _rng.Range(0.6, 1.6);
                    continue;
                }

                if (room < 120)
                    break;
            }

            length = Math.Min(length, room);

            var spec = new MissionSpec(
                Ordinal: ordinal, StartT: t, Kind: kind, Stage: stage, Length: length,
                Destination: destination, OverWater: overWater, Outcome: outcome, FailPhase: phase,
                Cause: cause, CrewCount: crew, Spectacular: spectacular, DockPartner: partner);

            plan.Add(spec);
            inFlight.Add(spec.EndT);
            ordinal++;
            t += Careers.DrawCadence(_rng, stage, Temperament) * _rng.Range(0.6, 1.6);
        }

        return plan;
    }

    /// <summary>
    /// The first instant at or after <paramref name="t"/> at which the player has a free slot.
    /// </summary>
    private static double NextFreeSlot(List<double> inFlight, double t, int ceiling)
    {
        while (true)
        {
            int busy = 0;
            double earliest = double.MaxValue;
            foreach (double end in inFlight)
            {
                if (end <= t)
                    continue;
                busy++;
                if (end < earliest)
                    earliest = end;
            }

            if (busy < ceiling)
                return t;
            t = earliest + 1.0;
        }
    }

    /// <summary>
    /// Guarantees this player produces the <c>vehicle.rud</c> cause its index is responsible for.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Left entirely to the failure model, the rarest cause is absent from most runs and "all six
    /// RUD causes are exercised" stops being a property this harness has. The rotation assigns
    /// cause <c>index % 6</c> to each player and pins it to the <b>first loss whose phase can
    /// physically carry it</b> — so the pairing stays honest: a covering <c>ocean_impact</c> lands
    /// on a splashdown gone wrong, never on a pad fire.
    /// </para>
    /// <para>
    /// A career the window caught with no losses at all has one manufactured on its first flight.
    /// Over the dozens of flights a career actually contains that is not a strong claim — nobody
    /// gets to an explorer's hours without losing something — and it is the price of the guarantee
    /// being deterministic rather than probable.
    /// </para>
    /// </remarks>
    private void EnsureRudCoverage(List<MissionSpec> plan)
    {
        if (plan.Count == 0)
            return;

        RudCause cause = Careers.Causes[_cohort % Careers.Causes.Length];

        // 1. A loss already in the right phase: relabel it and stop.
        for (int i = 0; i < plan.Count; i++)
        {
            MissionSpec spec = plan[i];
            if (spec.Outcome == MissionOutcome.Rud && Careers.Admits(spec.FailPhase, cause, spec.OverWater))
            {
                plan[i] = spec with { Cause = cause };
                return;
            }
        }

        // 2. A loss that could have happened in a phase that carries it: move it there.
        for (int i = 0; i < plan.Count; i++)
        {
            if (plan[i].Outcome != MissionOutcome.Rud)
                continue;
            if (TryRephrase(plan, i, cause))
                return;
        }

        // 3. No loss at all in the window: the first flight becomes one.
        for (int i = 0; i < plan.Count; i++)
        {
            if (TryRephrase(plan, i, cause))
                return;
        }

        // 4. Nothing in the plan can carry it — only reachable when every flight was a transfer
        // to an airless world. The first flight becomes a home-body hop, which carries anything.
        MissionSpec first = plan[0] with
        {
            Kind = MissionKind.Hop,
            Destination = LoadBodies.Earth,
            Length = Math.Min(plan[0].Length, 320),
            Outcome = MissionOutcome.Rud,
        };
        plan[0] = first;
        _ = TryRephrase(plan, 0, cause);
    }

    /// <summary>
    /// Moves a flight's loss to a phase that can carry <paramref name="cause"/>, or converts it.
    /// </summary>
    /// <remarks>
    /// The admitting phase is chosen as near as possible to where the flight was <i>already</i>
    /// going to be lost, and failing that as late as possible. Taking the first admitting phase
    /// instead is what a naive implementation does, and it would put every covering
    /// <c>ground_impact</c> and <c>collision</c> on the pad — which would quietly manufacture the
    /// pad-heavy failure profile the report is trying to measure.
    /// </remarks>
    private static bool TryRephrase(List<MissionSpec> plan, int index, RudCause cause)
    {
        MissionSpec spec = plan[index];
        bool water = TerminalBody(spec.Kind, spec.Destination).Ocean && Descends(spec.Kind);
        IReadOnlyList<FlightPhase> reachable = Careers.Reachable(spec.Kind);

        int anchor = reachable.Count - 1;
        for (int i = 0; i < reachable.Count; i++)
        {
            if (reachable[i] == spec.FailPhase)
                anchor = i;
        }

        int best = -1;
        bool bestWater = false;
        for (int i = 0; i < reachable.Count; i++)
        {
            bool dry = Careers.Admits(reachable[i], cause, overWater: false);
            bool wet = water && Careers.Admits(reachable[i], cause, overWater: true);
            if (!dry && !wet)
                continue;
            if (best >= 0 && Math.Abs(i - anchor) >= Math.Abs(best - anchor))
                continue;

            best = i;
            bestWater = !dry;
        }

        if (best < 0)
            return false;

        plan[index] = spec with
        {
            Outcome = MissionOutcome.Rud,
            FailPhase = reachable[best],
            Cause = cause,
            OverWater = bestWater,
        };
        return true;
    }

    private void BuildMissions(List<MissionSpec> plan)
    {
        foreach (MissionSpec spec in plan)
        {
            var mission = new MissionActor(
                id: $"p{_index}-m{spec.Ordinal:000}",
                spec: spec,
                crew: _crew,
                rng: _rng,
                clock: _clock);

            mission.Schedule(Emit);
            _missions.Add(mission);

            _byKind[(int)spec.Kind]++;
            if (spec.Outcome == MissionOutcome.Rud)
            {
                _byPhase[(int)spec.FailPhase]++;
                _byCause[Array.IndexOf(Careers.Causes, spec.Cause)]++;
            }
            else
            {
                _completed++;
            }

            // Everything the craft actually flew through counts, not just where it was aimed: a
            // Phobos mission visits Mars, and every interplanetary flight visits the star.
            if (mission.LeftHomeSoi)
                Reach(LoadBodies.Sol);
            if (mission.ReachedDestination)
            {
                if (LoadBodies.ParentOf(spec.Destination) is { Reach: not (BodyReach.Star or BodyReach.Home) } via)
                    Reach(via);
                Reach(spec.Destination);
            }

            // A rendezvous is only a rendezvous if there was something to meet and the craft got
            // far enough to meet it: the dock needs two flight ids, which is why it lives here
            // rather than inside the actor.
            if (spec.DockPartner is { } partner && mission.DockAt is { } dockT && dockT + 25 < mission.EndT)
            {
                double undock = Math.Min(dockT + _rng.Range(45, 260), mission.EndT - 5);
                Emit(dockT, new DockSignal(dockT, Wall(dockT), mission.Id, partner));
                if (undock > dockT + 5)
                    Emit(undock, new UndockSignal(undock, Wall(undock), mission.Id, partner));
            }

            // An integrity flag now and then. A flagged flight is excluded from every record board,
            // so this is the path that keeps the projector's exclusion honest under load.
            if (_rng.Chance(0.035))
            {
                double flagT = spec.StartT + _rng.Range(3, Math.Max(6, mission.Length * 0.6));
                FlightFlag flag = _rng.Pick(VehicleFlags);
                Emit(flagT, new FlaggedSignal(flagT, Wall(flagT), mission.Id, flag, "loadgen: " + flag));
            }
        }
    }

    // --- the rest of the save ----------------------------------------------------------

    /// <summary>
    /// EVAs, on bodies the kitten could actually be standing on.
    /// </summary>
    /// <remarks>
    /// A kitten can be outside at the space centre, next to a craft that is parked on a surface,
    /// or after a landing somewhere else — and never before the craft that carried them got there,
    /// and never on the gas giant. Drawing a body uniformly from the system would put rookies on
    /// Laythe, which is exactly the sort of implausible input that makes the detector produce the
    /// wrong events.
    /// </remarks>
    private void DrawEvas()
    {
        var places = new List<(LoadBody Body, double From)> { (LoadBodies.Earth, _epoch) };
        foreach (ResidentActor resident in _residents)
        {
            if (resident.Landed && resident.Body.Walkable)
                places.Add((resident.Body, _epoch));
        }

        foreach (MissionActor mission in _missions)
        {
            if (mission.SurfaceArrival is { } arrived && mission.Destination.Walkable)
                places.Add((mission.Destination, arrived));
        }

        int count = StartStage switch
        {
            CareerStage.Rookie => _rng.Int(0, 2),
            CareerStage.Suborbital => _rng.Int(0, 3),
            CareerStage.Orbital or CareerStage.Operator => _rng.Int(1, 4),
            _ => _rng.Int(1, 6),
        };
        if (Temperament == Temperament.Daredevil)
            count += _rng.Int(1, 4);

        // Kittens go outside on every save that has got as far as landing anything, so a career at
        // or past the orbital stage always produces at least one EVA — which is what makes
        // kitten.eva_start and kitten.tumble deterministic taxonomy coverage rather than luck.
        if (StartStage >= CareerStage.Orbital)
            count = Math.Max(1, count);

        for (int i = 0; i < count; i++)
        {
            (LoadBody body, double from) = places[_rng.Int(0, places.Count)];
            double earliest = Math.Max(from + 20, _epoch + 30);
            if (earliest > _end - 140)
                continue;

            double start = _rng.Range(earliest, Math.Max(earliest + 30, _end - 130));
            double length = _rng.Range(90, 400);
            if (start + length > _end - 5)
                continue;

            int tumbles = Temperament == Temperament.Daredevil ? _rng.Int(2, 9) : _rng.Int(0, 4);
            if (i == 0 && StartStage >= CareerStage.Orbital)
                tumbles = Math.Max(1, tumbles);

            var eva = new EvaActor(
                id: $"p{_index}-eva-{i}",
                kitten: _crew[_rng.Int(0, _crew.Count)],
                body: body,
                startT: start,
                length: length,
                tumbles: tumbles,
                rng: _rng,
                clock: _clock);

            eva.Schedule(Emit);
            _evas.Add(eva);
        }
    }

    private void DrawSessionFlag()
    {
        // Live tuning is session-wide (it edits the tumble-speed gate), so it taints every flight
        // in the session including ones started later — a different code path from a per-vehicle
        // flag, and worth exercising.
        if (!_rng.Chance(0.02))
            return;

        double flagT = _epoch + _rng.Range(30, Math.Max(60, _duration * 0.7));
        Emit(flagT, new FlaggedSignal(flagT, Wall(flagT), null, FlightFlag.Tuning, "loadgen: debug window open"));
    }

    private void DrawSaveLoads()
    {
        // A save load is a hard teardown: the pipeline drops its detector, its windows, its
        // correlator and its open flights and starts a new session. It is rare in play and it is
        // the single most disruptive thing that can happen to the client, so it is worth having a
        // few of them in a large run and worth keeping them rare.
        if (_duration < 900 || !_rng.Chance(0.12))
            return;

        double t = _epoch + _rng.Range(_duration * 0.3, _duration * 0.8);
        _saveLoads.Add(t);
        // The career id is deliberately left null: reloading a save does not change which save is
        // being played, and a career that changed identity every time the player quit to the menu
        // would make every sim_t in it incomparable with the last.
        Emit(t, new SessionLoadedSignal(t, Wall(t), PlayerRunner.GameBuild, PlayerRunner.ModVersion));

        // Everything still in the save is re-registered by the game after a load. Without this the
        // residents would carry on producing telemetry for a flight that has no `flight.started` —
        // a phantom flight, which is a bug in the harness rather than a fact about the client.
        foreach (ResidentActor resident in _residents)
            Emit(t, resident.Recreated(t));
    }

    private void ScheduleRoster()
    {
        // Roster snapshots on §4.2's ten-minute cadence, and one at session end.
        for (double r = 600; r < _duration; r += 600)
            Emit(_epoch + r, new RosterSampleSignal(_epoch + r, Wall(_epoch + r), Roster(_epoch + r)));
        double last = _epoch + Math.Max(0, _duration - Dt);
        Emit(last, new RosterSampleSignal(last, Wall(last), Roster(last)));
    }

    private IReadOnlyList<RosterKitten> Roster(double careerT)
    {
        var rows = new List<RosterKitten>(_crew.Count);
        double career = careerT;
        for (int i = 0; i < _crew.Count; i++)
        {
            // Monotonic in t, and a pure function of (player, kitten, career time) so a re-run
            // reproduces it. The totals carry the career, not just the window — a veteran's crew
            // has flown far more than a rookie's even on their first frame.
            double scale = 0.4 + (0.25 * ((i * 7 % 5) + 1));
            rows.Add(new RosterKitten(
                Name: _crew[i],
                TravelledM: (400 * (i + 1)) + (career * scale * 1.7),
                // Ecliptic-frame, so ~30 km/s on the home world. Recorded for completeness; the
                // speed boards come from telemetry.window.
                FastestMs: 29_800 + (i * 37) + (Math.Min(career, 90_000) * 0.02),
                Missions: 1 + i + (int)(career / 1_800),
                MissionTimeS: career * scale,
                Kia: false));
        }

        return rows;
    }

    // --- small decisions ---------------------------------------------------------------

    /// <summary>True when something of this player's is in home orbit to rendezvous with.</summary>
    private bool HasHomeStation()
    {
        foreach (ResidentActor resident in _residents)
        {
            if (!resident.Landed && ReferenceEquals(resident.Body, LoadBodies.Earth))
                return true;
        }

        return false;
    }

    /// <summary>
    /// A resident in orbit around <paramref name="body"/> that something else could dock with.
    /// </summary>
    /// <remarks>
    /// Always consumes exactly one draw whether or not a partner exists, so the presence of a
    /// dockable craft cannot shift the rest of the player's random stream.
    /// </remarks>
    private string? PickOrbitingPartner(LoadBody body)
    {
        var candidates = new List<string>();
        foreach (ResidentActor resident in _residents)
        {
            if (!resident.Landed && ReferenceEquals(resident.Body, body))
                candidates.Add(resident.Id);
        }

        int pick = _rng.Int(0, Math.Max(1, candidates.Count));
        return candidates.Count == 0 ? null : candidates[pick];
    }

    private LoadBody DrawDestination(MissionKind kind, CareerStage stage, bool forced)
    {
        if (kind is not (MissionKind.Transfer or MissionKind.Landing or MissionKind.Probe))
            return LoadBodies.Earth;

        // A guaranteed opener goes somewhere close, so it fits inside the window whatever the
        // window is. Everything else is drawn from what the career can reach, weighted so the
        // moons are routine and the outer system is an expedition.
        IReadOnlyList<LoadBody> candidates = forced
            ? LoadBodies.Moons
            : kind == MissionKind.Probe
                ? LoadBodies.Outer
                : LoadBodies.ReachableAt(stage);

        var choices = new List<(LoadBody Item, double Weight)>(candidates.Count);
        foreach (LoadBody body in candidates)
        {
            if (kind == MissionKind.Landing && !body.Landable)
                continue;

            choices.Add((body, body.Reach switch
            {
                BodyReach.Moon => 100.0,
                BodyReach.Inner => 42.0,
                _ => 18.0,
            }));
        }

        // The fallback is the Moon, not LoadBodies.All[1]: that index is the star, which is neither
        // landable nor a place anyone aims at, and an off-by-one here would be a lander pointed at
        // the Sun rather than a loud failure.
        return choices.Count == 0 ? LoadBodies.Moons[0] : _rng.Weighted(choices);
    }

    private int DrawCrewCount(MissionKind kind)
    {
        // Probes are uncrewed by definition; nobody rides a first pad test either.
        if (kind is MissionKind.Probe)
            return 0;
        if (kind is MissionKind.PadTest)
            return _rng.Chance(0.2) ? 1 : 0;

        int seats = (int)_rng.Weighted([(0.0, 30.0), (1.0, 22.0), (2.0, 26.0), (3.0, 14.0), (4.0, 8.0)]);
        return Math.Min(seats, _crew.Count);
    }

    private MissionOutcome DrawSuccess(MissionKind kind, bool overWater)
    {
        // Scuttling is the only path that marks a kitten KIA (D11), and it is the player tidying
        // up, so it is rare and it is never the point of a mission.
        if (_rng.Chance(0.03))
            return MissionOutcome.Scuttled;

        return Descends(kind) && kind != MissionKind.Landing
            ? (overWater ? MissionOutcome.Splashdown : MissionOutcome.Recovered)
            : MissionOutcome.Parked;
    }

    private static bool Descends(MissionKind kind) => kind
        is MissionKind.PadTest or MissionKind.Hop or MissionKind.HighHop
        or MissionKind.Deorbit or MissionKind.Landing;

    private static LoadBody TerminalBody(MissionKind kind, LoadBody destination)
        => kind == MissionKind.Landing ? destination : LoadBodies.Earth;

    private static double Floor(MissionKind kind) => kind switch
    {
        MissionKind.PadTest => 40,
        MissionKind.Hop => 130,
        MissionKind.HighHop => 300,
        MissionKind.Orbit => 430,
        MissionKind.Manoeuvre => 540,
        MissionKind.Rendezvous => 660,
        MissionKind.Deorbit => 620,
        MissionKind.Transfer => 700,
        MissionKind.Landing => 820,
        _ => 900,
    };

    private void Reach(LoadBody body)
    {
        if (body.Reach == BodyReach.Home)
            return;
        foreach (LoadBody seen in _reached)
        {
            if (ReferenceEquals(seen, body))
                return;
        }

        _reached.Add(body);
    }

    private IReadOnlyList<string> BodyNames()
    {
        // Ordered by the body table rather than by when they were reached, so two runs of a seed
        // print the same list.
        var names = new List<string>(_reached.Count);
        foreach (LoadBody body in LoadBodies.All)
        {
            foreach (LoadBody seen in _reached)
            {
                if (!ReferenceEquals(seen, body))
                    continue;
                names.Add(body.Name);
                break;
            }
        }

        return names;
    }

    // --- scheduling ------------------------------------------------------------------

    private void Emit(double t, params GameSignal[] signals)
    {
        if (signals.Length == 0)
            return;

        int frame = Math.Clamp((int)Math.Round((t - _epoch) / Dt), 0, FrameCount - 1);
        if (!_schedule.TryGetValue(frame, out List<GameSignal>? bucket))
            _schedule[frame] = bucket = [];
        bucket.AddRange(signals);
    }
}
