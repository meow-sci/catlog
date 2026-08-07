using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// How far a player has got. Capability is gated on this and on nothing else.
/// </summary>
/// <remarks>
/// The ladder is the one the game itself teaches: you get off the pad before you get to orbit, you
/// come back down before you go anywhere, and you learn to rendezvous before you leave the system.
/// A stage is a function of accumulated in-game time (see <see cref="Careers.StageFor"/>), so it
/// only ever moves forwards within a player and moves during a run rather than being fixed at the
/// start.
/// </remarks>
internal enum CareerStage
{
    /// <summary>Pad launches and short hops. Most of these do not end well.</summary>
    Rookie,

    /// <summary>Real suborbital flights: staging, max-Q, an apogee above the atmosphere.</summary>
    Suborbital,

    /// <summary>Stable orbits, orbital manoeuvres, and coming home again.</summary>
    Orbital,

    /// <summary>Rendezvous and docking. A standing fleet starts here.</summary>
    Operator,

    /// <summary>Transfers to other bodies, capture, and landing somewhere else.</summary>
    Interplanetary,

    /// <summary>The outer system, and probes that never come back.</summary>
    Explorer,
}

/// <summary>What a player is trying to do on a given flight.</summary>
internal enum MissionKind
{
    /// <summary>A hop barely off the pad: the first thing anyone flies, and the first thing anyone loses.</summary>
    PadTest,

    /// <summary>A suborbital hop, up and straight back down.</summary>
    Hop,

    /// <summary>A high lob that leaves the atmosphere and comes back through it hot.</summary>
    HighHop,

    /// <summary>Ascent to a stable orbit, left there.</summary>
    Orbit,

    /// <summary>Orbit plus a manoeuvre: a plane change, a phasing burn, an apoapsis raise.</summary>
    Manoeuvre,

    /// <summary>Orbit, rendezvous with a craft already up there, dock, undock.</summary>
    Rendezvous,

    /// <summary>Orbit, deorbit burn, reentry, and a landing or splashdown at home.</summary>
    Deorbit,

    /// <summary>Escape the home SOI and capture into orbit at another body.</summary>
    Transfer,

    /// <summary>A transfer that ends on the surface of somewhere else.</summary>
    Landing,

    /// <summary>A one-way probe: escape, cross the system, fly past something far away.</summary>
    Probe,
}

/// <summary>Where in a flight it went wrong.</summary>
/// <remarks>
/// The failure phase is the interesting half of the failure model. A career that fails at a uniform
/// rate in a uniform place is not a career; a rookie loses vehicles on the pad and at max-Q, and a
/// veteran loses them on landing approach and while docking.
/// </remarks>
internal enum FlightPhase
{
    /// <summary>Nothing went wrong.</summary>
    None,

    /// <summary>On the pad, or within a second or two of leaving it.</summary>
    Pad,

    /// <summary>Powered ascent, below max-Q.</summary>
    Ascent,

    /// <summary>Maximum dynamic pressure.</summary>
    MaxQ,

    /// <summary>A stage separation that did not separate cleanly.</summary>
    Staging,

    /// <summary>Coasting in orbit.</summary>
    Orbit,

    /// <summary>Under a manoeuvre burn.</summary>
    Manoeuvre,

    /// <summary>Closing on another craft, or on the docking port itself.</summary>
    Rendezvous,

    /// <summary>Escape, cruise or capture.</summary>
    Transfer,

    /// <summary>Atmospheric entry, where the g comes from.</summary>
    Reentry,

    /// <summary>The descent between entry and the ground.</summary>
    Descent,

    /// <summary>Touchdown.</summary>
    Landing,
}

/// <summary>How a mission finishes.</summary>
internal enum MissionOutcome
{
    /// <summary>Touched down and was recovered.</summary>
    Recovered,

    /// <summary>Splashed down and was recovered.</summary>
    Splashdown,

    /// <summary>Rapidly and unscheduledly disassembled, in <see cref="MissionSpec.FailPhase"/>.</summary>
    Rud,

    /// <summary>Left where it got to — in orbit, on another surface, or heading out.</summary>
    Parked,

    /// <summary>The player scuttled it. The only path that marks a kitten KIA (D11).</summary>
    Scuttled,
}

/// <summary>
/// How a player plays, independently of how far they have got.
/// </summary>
/// <remarks>
/// Temperament and <see cref="CareerStage"/> are deliberately orthogonal: the stage says what a
/// player <i>can</i> do, the temperament says what they choose to do with it and how much risk they
/// take doing it. A cautious veteran and a reckless veteran fly the same mission types with very
/// different loss rates.
/// </remarks>
internal enum Temperament
{
    /// <summary>Flies few missions, tests everything twice, loses very little.</summary>
    Cautious,

    /// <summary>The modal player.</summary>
    Steady,

    /// <summary>Launches constantly; volume rather than care.</summary>
    Prolific,

    /// <summary>A station keeper: big standing fleet, dockings, few landings.</summary>
    Engineer,

    /// <summary>Crashes things on purpose. Where most of the RUDs and the records come from.</summary>
    Daredevil,
}

/// <summary>One planned flight, drawn before any actor exists.</summary>
/// <remarks>
/// Planning and building are two passes on purpose. Every draw that decides <i>what</i> a career
/// does happens first, in one fixed order; the deterministic coverage pass then rewrites a field or
/// two without drawing anything; only then are the actors built, in a second fixed order. Splitting
/// it this way is what lets the coverage guarantee exist without making the run's random stream
/// depend on the guarantee.
/// </remarks>
/// <param name="Ordinal">Index within the player's career.</param>
/// <param name="StartT">Launch instant, in sim seconds.</param>
/// <param name="Kind">What the flight is for.</param>
/// <param name="Stage">The career stage the player was at when it launched.</param>
/// <param name="Length">Planned flight length, in sim seconds, before any failure truncates it.</param>
/// <param name="Destination">Where it ends up: home for everything below a transfer.</param>
/// <param name="OverWater">True when the terminal descent is over an ocean.</param>
/// <param name="Outcome">How it finishes.</param>
/// <param name="FailPhase">Where it went wrong; <see cref="FlightPhase.None"/> when it did not.</param>
/// <param name="Cause">The RUD cause, meaningful only for <see cref="MissionOutcome.Rud"/>.</param>
/// <param name="CrewCount">Occupied seats.</param>
/// <param name="Spectacular">True for the rare flight that produces a record rather than a number.</param>
/// <param name="DockPartner">The resident craft to rendezvous with, or null.</param>
internal sealed record MissionSpec(
    int Ordinal,
    double StartT,
    MissionKind Kind,
    CareerStage Stage,
    double Length,
    LoadBody Destination,
    bool OverWater,
    MissionOutcome Outcome,
    FlightPhase FailPhase,
    RudCause Cause,
    int CrewCount,
    bool Spectacular,
    string? DockPartner)
{
    /// <summary>The instant the craft leaves the timeline: truncated to the failure phase.</summary>
    internal double EndT => StartT + EffectiveLength;

    /// <summary>
    /// How long the flight actually lasts. A pad failure lasts seconds, not the whole planned
    /// profile — which is exactly what "early careers fail early" has to mean on the wire.
    /// </summary>
    internal double EffectiveLength => FailPhase == FlightPhase.None
        ? Length
        : Math.Max(6.0, Length * Careers.PhaseAt(Kind, FailPhase));
}

/// <summary>
/// What one player's career looked like, for the report.
/// </summary>
/// <param name="Temperament">How they play.</param>
/// <param name="StartStage">Where they were when the run's window opened.</param>
/// <param name="EndStage">Where they were when it closed.</param>
/// <param name="PriorHours">In-game hours accumulated before the window.</param>
/// <param name="CareerHours">In-game hours accumulated by the end of it.</param>
/// <param name="Fleet">Craft standing in the save for the whole run.</param>
/// <param name="Attempted">Missions launched.</param>
/// <param name="Completed">Missions that reached their objective.</param>
/// <param name="ByKind">Missions launched, indexed by <see cref="MissionKind"/>.</param>
/// <param name="FailuresByPhase">Losses, indexed by <see cref="FlightPhase"/>.</param>
/// <param name="CausesByKind">Losses, indexed by <see cref="RudCause"/>.</param>
/// <param name="BodiesReached">Distinct non-home bodies arrived at, in reach order.</param>
internal sealed record CareerSummary(
    Temperament Temperament,
    CareerStage StartStage,
    CareerStage EndStage,
    double PriorHours,
    double CareerHours,
    int Fleet,
    int Attempted,
    int Completed,
    IReadOnlyList<int> ByKind,
    IReadOnlyList<int> FailuresByPhase,
    IReadOnlyList<int> CausesByKind,
    IReadOnlyList<string> BodiesReached);

/// <summary>
/// The career model: every rule that turns "how long has this player been playing" into "what do
/// they fly, how many at once, and how does it go wrong".
/// </summary>
/// <remarks>
/// <para>
/// This is a table, not an algorithm, on purpose. Every number below is a claim about what play
/// looks like, and a claim in a table can be read and argued with; the same claim spread across
/// twelve <c>if</c> statements cannot.
/// </para>
/// <para>
/// <b>Nothing here draws.</b> Every method takes the <see cref="Prng"/> it needs as a parameter and
/// draws in a fixed order, so the whole model composes into <see cref="PlayerScript"/>'s single
/// deterministic stream.
/// </para>
/// </remarks>
internal static class Careers
{
    /// <summary>Every stage, in order.</summary>
    internal static readonly CareerStage[] Stages = Enum.GetValues<CareerStage>();

    /// <summary>The stage at which a career stops counting as green, for the report's split.</summary>
    internal const CareerStage Seasoned = CareerStage.Operator;

    /// <summary>Every mission kind, in order.</summary>
    internal static readonly MissionKind[] Kinds = Enum.GetValues<MissionKind>();

    /// <summary>Every failure phase, in order.</summary>
    internal static readonly FlightPhase[] Phases = Enum.GetValues<FlightPhase>();

    /// <summary>The six <c>vehicle.rud</c> causes, in taxonomy order.</summary>
    internal static readonly RudCause[] Causes =
    [
        RudCause.GroundImpact, RudCause.OceanImpact, RudCause.Collision,
        RudCause.ExcessiveGForce, RudCause.AerodynamicForces, RudCause.HydrodynamicForces,
    ];

    /// <summary>
    /// The in-game hours at which each stage opens.
    /// </summary>
    /// <remarks>
    /// Calibrated against how long the equivalent milestones take a real player: a couple of
    /// evenings to a reliable suborbital rocket, a week or so to a repeatable orbit, a month to
    /// rendezvous, and a serious investment before anything lands on another world.
    /// </remarks>
    private static readonly double[] StageHours = [0, 3, 10, 30, 80, 200];

    /// <summary>
    /// The cohort ladder: player <c>i</c> is guaranteed to have reached at least
    /// <c>Ladder[i % Ladder.Length]</c> before the run's window opens.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Left purely to the prior-experience draw, a run's population would be whatever the tail of a
    /// Pareto happened to produce — which means "does this run contain a player who has been to
    /// another body" would be a coin flip, and the taxonomy invariant would be flaky. That is the
    /// same problem the RUD-cause rotation already solves, solved the same way.
    /// </para>
    /// <para>
    /// The ladder is bottom-heavy so it does not flatten the population into six equal cohorts: a
    /// quarter of the slots are rookies and one in twelve is an explorer. The prior-age draw lands
    /// inside its rung's band rather than being free to leave it, so any run of twelve or more
    /// players covers every stage exactly, deterministically, and still looks like a player base.
    /// </para>
    /// </remarks>
    internal static readonly CareerStage[] Ladder =
    [
        CareerStage.Rookie, CareerStage.Rookie, CareerStage.Rookie,
        CareerStage.Suborbital, CareerStage.Suborbital,
        CareerStage.Orbital, CareerStage.Orbital,
        CareerStage.Operator, CareerStage.Operator,
        CareerStage.Interplanetary, CareerStage.Interplanetary,
        CareerStage.Explorer,
    ];

    /// <summary>The stage a career with this much accumulated in-game time has reached.</summary>
    /// <param name="careerSeconds">Accumulated in-game seconds.</param>
    /// <returns>The stage.</returns>
    internal static CareerStage StageFor(double careerSeconds)
    {
        CareerStage stage = CareerStage.Rookie;
        for (int i = 0; i < StageHours.Length; i++)
        {
            if (careerSeconds >= StageHours[i] * 3600.0)
                stage = (CareerStage)i;
        }

        return stage;
    }

    /// <summary>The in-game seconds at which a stage opens.</summary>
    /// <param name="stage">The stage.</param>
    /// <returns>The threshold, in seconds.</returns>
    internal static double OpensAt(CareerStage stage) => StageHours[(int)stage] * 3600.0;

    /// <summary>
    /// How much in-game time a player already had when the run's window opened.
    /// </summary>
    /// <remarks>
    /// <para>
    /// A player base is a spread of careers, not a cohort of beginners: this is what makes a
    /// three-hour run contain someone who has been playing for three hundred hours. The natural
    /// draw is Pareto — the shape play-time histograms actually have, with a long tail of people
    /// who have sunk absurd amounts of time into a cat game — and it is then fitted to this
    /// player's rung on <see cref="Ladder"/> so coverage is deterministic. The rung decides the
    /// stage; the draw decides where in it, and how far into the tail the explorers go.
    /// </para>
    /// </remarks>
    /// <param name="rng">The player's generator.</param>
    /// <param name="cohort">The player's dense position in the run; picks the ladder rung.</param>
    /// <returns>Prior in-game seconds.</returns>
    internal static double DrawPriorSeconds(Prng rng, int cohort)
    {
        // Pareto(xm = 2 h, alpha = 0.8): median ~5 h, p90 ~35 h, a long tail of people who have
        // sunk absurd amounts of time into a cat game. Capped so one unlucky draw cannot claim a
        // forty-year career.
        double u = Math.Max(1e-9, rng.NextDouble());
        double natural = Math.Min(2.0 * 3600.0 * Math.Pow(1.0 / u, 1.0 / 0.8), 1_500.0 * 3600.0);

        CareerStage rung = Ladder[cohort % Ladder.Length];
        double floor = OpensAt(rung);

        // The top rung is open-ended, so the tail lives there and nowhere else.
        if (rung == CareerStage.Explorer)
            return Math.Max(natural, floor * rng.Range(1.02, 2.6));

        // Every other rung is a band, and the draw is clamped into it rather than allowed to
        // exceed it. Taking the maximum instead would let the Pareto's fat middle lift the whole
        // bottom of the ladder past the rookie stage, which is precisely the cohort the guarantee
        // exists to protect — and a run with no rookies in it is not a player base.
        double ceiling = OpensAt((CareerStage)((int)rung + 1));
        double headroom = (ceiling - floor) * rng.Range(0.02, 0.94);
        return Math.Clamp(natural, floor + headroom, ceiling - Math.Max(60.0, (ceiling - floor) * 0.02));
    }

    /// <summary>The temperament draw.</summary>
    /// <param name="rng">The player's generator.</param>
    /// <returns>The temperament.</returns>
    internal static Temperament DrawTemperament(Prng rng) => rng.Weighted([
        (Temperament.Cautious, 20.0),
        (Temperament.Steady, 34.0),
        (Temperament.Prolific, 20.0),
        (Temperament.Engineer, 14.0),
        (Temperament.Daredevil, 12.0),
    ]);

    /// <summary>The lowest stage that can fly a mission kind.</summary>
    /// <param name="kind">The mission kind.</param>
    /// <returns>The stage it unlocks at.</returns>
    internal static CareerStage Unlocks(MissionKind kind) => kind switch
    {
        MissionKind.PadTest or MissionKind.Hop => CareerStage.Rookie,
        MissionKind.HighHop => CareerStage.Suborbital,
        MissionKind.Orbit or MissionKind.Manoeuvre or MissionKind.Deorbit => CareerStage.Orbital,
        MissionKind.Rendezvous => CareerStage.Operator,
        MissionKind.Transfer or MissionKind.Landing => CareerStage.Interplanetary,
        _ => CareerStage.Explorer,
    };

    /// <summary>
    /// Picks what this player flies next.
    /// </summary>
    /// <remarks>
    /// Newly unlocked capability is exciting but the bread and butter never stops: an interplanetary
    /// player still flies orbital missions, and even an explorer occasionally puts a test article on
    /// the pad. Only the mix moves.
    /// </remarks>
    /// <param name="rng">The player's generator.</param>
    /// <param name="stage">The player's current stage.</param>
    /// <param name="temperament">How they play.</param>
    /// <param name="canDock">True when a resident craft exists to rendezvous with.</param>
    /// <returns>The mission kind.</returns>
    internal static MissionKind DrawKind(Prng rng, CareerStage stage, Temperament temperament, bool canDock)
    {
        var choices = new List<(MissionKind Item, double Weight)>(Kinds.Length);
        foreach (MissionKind kind in Kinds)
        {
            if (Unlocks(kind) > stage)
                continue;
            if (kind == MissionKind.Rendezvous && !canDock)
                continue;

            double weight = BaseWeight(stage, kind) * Appetite(temperament, kind);
            if (weight > 0)
                choices.Add((kind, weight));
        }

        // A stage always has at least PadTest and Hop, so this cannot be empty.
        return choices.Count == 0 ? MissionKind.Hop : rng.Weighted(choices);
    }

    /// <summary>Planned length of a mission, in sim seconds.</summary>
    /// <param name="rng">The player's generator.</param>
    /// <param name="kind">The mission kind.</param>
    /// <param name="destination">Where it goes.</param>
    /// <returns>The length.</returns>
    internal static double DrawLength(Prng rng, MissionKind kind, LoadBody destination)
    {
        double reach = destination.Reach switch
        {
            BodyReach.Moon => 1.0,
            BodyReach.Inner => 1.7,
            BodyReach.Outer => 2.6,
            _ => 1.0,
        };

        return kind switch
        {
            MissionKind.PadTest => rng.Normal(80, 34, 40, 170),
            MissionKind.Hop => rng.Normal(240, 90, 130, 520),
            MissionKind.HighHop => rng.Normal(540, 170, 300, 980),
            MissionKind.Orbit => rng.Normal(720, 210, 430, 1_320),
            MissionKind.Manoeuvre => rng.Normal(930, 260, 540, 1_720),
            MissionKind.Rendezvous => rng.Normal(1_180, 320, 660, 2_150),
            MissionKind.Deorbit => rng.Normal(1_060, 300, 620, 2_050),
            MissionKind.Transfer => rng.Normal(1_500, 380, 950, 2_500) * reach,
            MissionKind.Landing => rng.Normal(1_750, 420, 1_100, 2_900) * reach,
            _ => rng.Normal(2_200, 560, 1_400, 3_800) * reach,
        };
    }

    /// <summary>The gap between one launch and the next, in sim seconds.</summary>
    /// <param name="rng">The player's generator.</param>
    /// <param name="stage">The player's current stage.</param>
    /// <param name="temperament">How they play.</param>
    /// <returns>The cadence.</returns>
    internal static double DrawCadence(Prng rng, CareerStage stage, Temperament temperament)
    {
        double baseline = stage switch
        {
            CareerStage.Rookie => rng.Range(400, 900),
            CareerStage.Suborbital => rng.Range(360, 820),
            CareerStage.Orbital => rng.Range(300, 740),
            CareerStage.Operator => rng.Range(260, 660),
            CareerStage.Interplanetary => rng.Range(220, 600),
            _ => rng.Range(180, 540),
        };

        return baseline * temperament switch
        {
            Temperament.Cautious => 1.5,
            Temperament.Prolific => 0.62,
            Temperament.Engineer => 1.3,
            Temperament.Daredevil => 0.7,
            _ => 1.0,
        };
    }

    /// <summary>
    /// How many craft this player has in flight at once.
    /// </summary>
    /// <remarks>
    /// A beginner flies one thing and watches it the whole way. A veteran has a lander on approach,
    /// a probe three weeks out and a station crew waiting for a resupply, and switches between them.
    /// </remarks>
    /// <param name="stage">The player's current stage.</param>
    /// <returns>The ceiling on concurrent missions.</returns>
    internal static int MaxConcurrent(CareerStage stage) => stage switch
    {
        CareerStage.Rookie or CareerStage.Suborbital => 1,
        CareerStage.Orbital => 2,
        CareerStage.Operator => 3,
        CareerStage.Interplanetary => 4,
        _ => 5,
    };

    /// <summary>How many craft sit in this player's save for the whole run.</summary>
    /// <param name="rng">The player's generator.</param>
    /// <param name="stage">The stage the player starts the run at.</param>
    /// <param name="temperament">How they play.</param>
    /// <returns>The resident count.</returns>
    internal static int DrawFleet(Prng rng, CareerStage stage, Temperament temperament)
    {
        int drawn = stage switch
        {
            CareerStage.Rookie => rng.Int(0, 2),
            CareerStage.Suborbital => rng.Int(0, 3),
            CareerStage.Orbital => rng.Int(1, 5),
            CareerStage.Operator => rng.Int(3, 9),
            CareerStage.Interplanetary => rng.Int(4, 12),
            _ => rng.Int(6, 16),
        };

        double scale = temperament switch
        {
            Temperament.Engineer => 1.6,
            Temperament.Daredevil => 0.5,
            Temperament.Cautious => 0.8,
            _ => 1.0,
        };

        return Math.Clamp((int)Math.Round(drawn * scale), 0, 24);
    }

    /// <summary>
    /// The chance a mission is lost.
    /// </summary>
    /// <remarks>
    /// Two independent things move it: how good the player is (stage) and what they were trying to
    /// do (kind). Landing somewhere else is the hardest thing in the list at every stage, and a pad
    /// test is the most likely thing for a beginner to lose because a beginner's rockets fall over.
    /// </remarks>
    /// <param name="stage">The player's stage.</param>
    /// <param name="kind">The mission kind.</param>
    /// <param name="temperament">How they play.</param>
    /// <returns>A probability in <c>[0.02, 0.85]</c>.</returns>
    internal static double FailureChance(CareerStage stage, MissionKind kind, Temperament temperament)
    {
        double bystage = stage switch
        {
            CareerStage.Rookie => 0.44,
            CareerStage.Suborbital => 0.32,
            CareerStage.Orbital => 0.22,
            CareerStage.Operator => 0.15,
            CareerStage.Interplanetary => 0.11,
            _ => 0.08,
        };

        double bykind = kind switch
        {
            MissionKind.PadTest => 1.25,
            MissionKind.Hop => 1.0,
            MissionKind.HighHop => 1.1,
            MissionKind.Orbit => 0.95,
            MissionKind.Manoeuvre => 0.85,
            MissionKind.Rendezvous => 1.15,
            MissionKind.Deorbit => 1.3,
            MissionKind.Transfer => 1.05,
            MissionKind.Landing => 1.45,
            _ => 0.65,
        };

        double bytemper = temperament switch
        {
            Temperament.Cautious => 0.7,
            Temperament.Prolific => 1.15,
            Temperament.Engineer => 0.85,
            Temperament.Daredevil => 2.3,
            _ => 1.0,
        };

        return Math.Clamp(bystage * bykind * bytemper, 0.02, 0.85);
    }

    /// <summary>
    /// Where a lost mission was lost.
    /// </summary>
    /// <remarks>
    /// This is the shape of the whole failure model. Each phase has an intrinsic difficulty and a
    /// pair of multipliers — one for a green career, one for a seasoned one — and the stage
    /// interpolates between them. Early careers therefore lose vehicles on the pad, on ascent and at
    /// max-Q; late ones lose them on approach, on touchdown and while closing on a docking port.
    /// </remarks>
    /// <param name="rng">The player's generator.</param>
    /// <param name="stage">The player's stage.</param>
    /// <param name="kind">The mission kind — decides which phases were even reached.</param>
    /// <returns>The phase.</returns>
    internal static FlightPhase DrawFailPhase(Prng rng, CareerStage stage, MissionKind kind)
    {
        var choices = new List<(FlightPhase Item, double Weight)>(Phases.Length);
        double green = Greenness[(int)stage];
        foreach (FlightPhase phase in Reachable(kind))
        {
            (double intrinsic, double early, double late) = PhaseRisk(phase);
            choices.Add((phase, intrinsic * Curve.Lerp(late, early, green)));
        }

        return choices.Count == 0 ? FlightPhase.Ascent : rng.Weighted(choices);
    }

    /// <summary>
    /// The RUD cause a failure in this phase produces.
    /// </summary>
    /// <remarks>
    /// The mapping is the physics, not a lookup for its own sake: max-Q tears a rocket apart with
    /// <c>aerodynamic_forces</c>, a docking prang is a <c>collision</c>, and whether a bad descent
    /// ends as <c>ground_impact</c>, <c>ocean_impact</c> or <c>hydrodynamic_forces</c> depends on
    /// what is underneath it.
    /// </remarks>
    /// <param name="rng">The player's generator.</param>
    /// <param name="phase">Where it went wrong.</param>
    /// <param name="overWater">True when the terminal descent is over an ocean.</param>
    /// <returns>The cause.</returns>
    internal static RudCause DrawCause(Prng rng, FlightPhase phase, bool overWater)
        => rng.Weighted(CauseWeights(phase, overWater));

    /// <summary>True when a failure in this phase can plausibly carry this cause.</summary>
    /// <param name="phase">The phase.</param>
    /// <param name="cause">The cause.</param>
    /// <param name="overWater">True when the terminal descent is over an ocean.</param>
    /// <returns>Whether the pairing is physical.</returns>
    internal static bool Admits(FlightPhase phase, RudCause cause, bool overWater)
    {
        foreach ((RudCause item, double weight) in CauseWeights(phase, overWater))
        {
            if (item == cause && weight > 0)
                return true;
        }

        return false;
    }

    /// <summary>The phases a mission of this kind actually flies through, in order.</summary>
    /// <param name="kind">The mission kind.</param>
    /// <returns>The phases.</returns>
    internal static IReadOnlyList<FlightPhase> Reachable(MissionKind kind) => kind switch
    {
        MissionKind.PadTest =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.Descent, FlightPhase.Landing],
        MissionKind.Hop =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Descent, FlightPhase.Landing],
        MissionKind.HighHop =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Reentry, FlightPhase.Descent, FlightPhase.Landing],
        MissionKind.Orbit =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging, FlightPhase.Orbit],
        MissionKind.Manoeuvre =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Manoeuvre],
        MissionKind.Rendezvous =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Manoeuvre, FlightPhase.Rendezvous],
        MissionKind.Deorbit =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Manoeuvre, FlightPhase.Reentry, FlightPhase.Descent,
             FlightPhase.Landing],
        MissionKind.Transfer =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Transfer],
        MissionKind.Landing =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Transfer, FlightPhase.Descent, FlightPhase.Landing],
        _ =>
            [FlightPhase.Pad, FlightPhase.Ascent, FlightPhase.MaxQ, FlightPhase.Staging,
             FlightPhase.Orbit, FlightPhase.Transfer],
    };

    /// <summary>
    /// Where in a flight profile a phase sits, as a fraction of the planned length.
    /// </summary>
    /// <remarks>
    /// This is what truncates a failed flight. A pad failure ends four percent of the way into a
    /// profile that would have taken four minutes, so it produces four seconds of telemetry, one
    /// ignition and a RUD — which is what a rookie's first launch actually looks like on the wire.
    /// </remarks>
    /// <param name="kind">The mission kind.</param>
    /// <param name="phase">The phase.</param>
    /// <returns>A fraction in <c>(0, 1]</c>.</returns>
    internal static double PhaseAt(MissionKind kind, FlightPhase phase)
    {
        // Every profile shares the same launch shape; only how much of it is compressed by what
        // comes afterwards changes, so the ascent fractions shrink as the mission gets longer.
        double squeeze = kind switch
        {
            MissionKind.PadTest => 2.6,
            MissionKind.Hop => 1.0,
            MissionKind.HighHop => 0.8,
            MissionKind.Orbit => 0.55,
            MissionKind.Manoeuvre => 0.46,
            MissionKind.Rendezvous => 0.4,
            MissionKind.Deorbit => 0.42,
            MissionKind.Transfer => 0.34,
            MissionKind.Landing => 0.28,
            _ => 0.3,
        };

        return phase switch
        {
            FlightPhase.Pad => Math.Min(0.09, 0.02 * squeeze),
            FlightPhase.Ascent => Math.Min(0.35, 0.15 * squeeze),
            FlightPhase.MaxQ => Math.Min(0.42, 0.22 * squeeze),
            FlightPhase.Staging => Math.Min(0.5, 0.30 * squeeze),
            FlightPhase.Orbit => kind is MissionKind.Orbit ? 0.62 : 0.40,
            FlightPhase.Manoeuvre => kind is MissionKind.Deorbit ? 0.68 : 0.58,
            FlightPhase.Rendezvous => 0.68,
            FlightPhase.Transfer => 0.55,
            FlightPhase.Reentry => kind is MissionKind.HighHop ? 0.74 : 0.80,
            FlightPhase.Descent => kind is MissionKind.PadTest ? 0.72 : 0.88,
            FlightPhase.Landing => 0.97,
            _ => 1.0,
        };
    }

    /// <summary>A human name for a phase, for the report.</summary>
    /// <param name="phase">The phase.</param>
    /// <returns>The label.</returns>
    internal static string Label(FlightPhase phase) => phase switch
    {
        FlightPhase.MaxQ => "max-q",
        _ => phase.ToString().ToLowerInvariant(),
    };

    /// <summary>A human name for a mission kind, for the report.</summary>
    /// <param name="kind">The kind.</param>
    /// <returns>The label.</returns>
    internal static string Label(MissionKind kind) => kind switch
    {
        MissionKind.PadTest => "pad-test",
        MissionKind.HighHop => "high-hop",
        _ => kind.ToString().ToLowerInvariant(),
    };

    /// <summary>A human name for a RUD cause, matching the wire spelling.</summary>
    /// <param name="cause">The cause.</param>
    /// <returns>The label.</returns>
    internal static string Label(RudCause cause) => cause switch
    {
        RudCause.GroundImpact => "ground_impact",
        RudCause.OceanImpact => "ocean_impact",
        RudCause.Collision => "collision",
        RudCause.ExcessiveGForce => "excessive_g_force",
        RudCause.AerodynamicForces => "aerodynamic_forces",
        _ => "hydrodynamic_forces",
    };

    // --- tables -----------------------------------------------------------------------

    /// <summary>
    /// How green a career at each stage still is, for <see cref="DrawFailPhase"/>.
    /// </summary>
    /// <remarks>
    /// Deliberately not linear in the stage index. Competence at not falling over on the pad is
    /// bought early and cheaply — an operator with thirty hours in has flown hundreds of launches —
    /// whereas competence at landing keeps being tested forever. A linear ramp left operators
    /// losing as many vehicles on the pad as on a docking approach, which is not what a player with
    /// a station in orbit looks like.
    /// </remarks>
    private static readonly double[] Greenness = [1.00, 0.68, 0.40, 0.22, 0.10, 0.00];

    private static double BaseWeight(CareerStage stage, MissionKind kind) => (stage, kind) switch
    {
        (CareerStage.Rookie, MissionKind.PadTest) => 34,
        (CareerStage.Rookie, MissionKind.Hop) => 66,

        (CareerStage.Suborbital, MissionKind.PadTest) => 14,
        (CareerStage.Suborbital, MissionKind.Hop) => 46,
        (CareerStage.Suborbital, MissionKind.HighHop) => 40,

        (CareerStage.Orbital, MissionKind.PadTest) => 4,
        (CareerStage.Orbital, MissionKind.Hop) => 16,
        (CareerStage.Orbital, MissionKind.HighHop) => 20,
        (CareerStage.Orbital, MissionKind.Orbit) => 26,
        (CareerStage.Orbital, MissionKind.Manoeuvre) => 14,
        (CareerStage.Orbital, MissionKind.Deorbit) => 20,

        (CareerStage.Operator, MissionKind.PadTest) => 2,
        (CareerStage.Operator, MissionKind.Hop) => 8,
        (CareerStage.Operator, MissionKind.HighHop) => 10,
        (CareerStage.Operator, MissionKind.Orbit) => 20,
        (CareerStage.Operator, MissionKind.Manoeuvre) => 16,
        (CareerStage.Operator, MissionKind.Deorbit) => 20,
        (CareerStage.Operator, MissionKind.Rendezvous) => 24,

        (CareerStage.Interplanetary, MissionKind.PadTest) => 1,
        (CareerStage.Interplanetary, MissionKind.Hop) => 5,
        (CareerStage.Interplanetary, MissionKind.HighHop) => 6,
        (CareerStage.Interplanetary, MissionKind.Orbit) => 14,
        (CareerStage.Interplanetary, MissionKind.Manoeuvre) => 12,
        (CareerStage.Interplanetary, MissionKind.Deorbit) => 15,
        (CareerStage.Interplanetary, MissionKind.Rendezvous) => 17,
        (CareerStage.Interplanetary, MissionKind.Transfer) => 18,
        (CareerStage.Interplanetary, MissionKind.Landing) => 12,

        (CareerStage.Explorer, MissionKind.PadTest) => 1,
        (CareerStage.Explorer, MissionKind.Hop) => 3,
        (CareerStage.Explorer, MissionKind.HighHop) => 4,
        (CareerStage.Explorer, MissionKind.Orbit) => 10,
        (CareerStage.Explorer, MissionKind.Manoeuvre) => 9,
        (CareerStage.Explorer, MissionKind.Deorbit) => 11,
        (CareerStage.Explorer, MissionKind.Rendezvous) => 14,
        (CareerStage.Explorer, MissionKind.Transfer) => 18,
        (CareerStage.Explorer, MissionKind.Landing) => 16,
        (CareerStage.Explorer, MissionKind.Probe) => 14,

        _ => 0,
    };

    private static double Appetite(Temperament temperament, MissionKind kind)
        => (temperament, kind) switch
        {
            (Temperament.Cautious, MissionKind.PadTest) => 2.0,
            (Temperament.Cautious, MissionKind.Hop) => 1.4,
            (Temperament.Cautious, MissionKind.Landing) => 0.6,
            (Temperament.Cautious, MissionKind.Probe) => 0.6,

            (Temperament.Engineer, MissionKind.Rendezvous) => 2.2,
            (Temperament.Engineer, MissionKind.Orbit) => 1.4,
            (Temperament.Engineer, MissionKind.PadTest) => 0.5,
            (Temperament.Engineer, MissionKind.Hop) => 0.5,

            (Temperament.Daredevil, MissionKind.PadTest) => 0.4,
            (Temperament.Daredevil, MissionKind.Hop) => 1.3,
            (Temperament.Daredevil, MissionKind.Deorbit) => 1.5,
            (Temperament.Daredevil, MissionKind.Landing) => 1.6,

            _ => 1.0,
        };

    private static (double Intrinsic, double Early, double Late) PhaseRisk(FlightPhase phase) => phase switch
    {
        // The pad and the early ascent are where a beginner's rockets come apart and where a
        // seasoned one's almost never do: that competence is bought early, cheaply, and for good.
        // Everything from reentry onwards keeps being dangerous forever, and touchdown most of all.
        FlightPhase.Pad => (1.0, 4.0, 0.10),
        FlightPhase.Ascent => (1.2, 3.4, 0.22),
        FlightPhase.MaxQ => (0.9, 2.6, 0.40),
        FlightPhase.Staging => (0.7, 1.6, 0.55),
        FlightPhase.Orbit => (0.3, 0.5, 0.80),
        FlightPhase.Manoeuvre => (0.4, 0.5, 1.05),
        FlightPhase.Rendezvous => (1.0, 0.8, 1.60),
        FlightPhase.Transfer => (0.6, 0.8, 1.50),
        FlightPhase.Reentry => (0.9, 1.0, 1.70),
        FlightPhase.Descent => (0.8, 1.0, 1.80),
        FlightPhase.Landing => (1.4, 1.2, 3.40),
        _ => (0.0, 0.0, 0.0),
    };

    private static IReadOnlyList<(RudCause Item, double Weight)> CauseWeights(FlightPhase phase, bool overWater)
        => phase switch
        {
            // A rocket that falls over on the pad hits the ground; one that topples into the tower
            // is a collision. Nothing else is available at zero altitude and zero speed.
            FlightPhase.Pad =>
                [(RudCause.GroundImpact, 68), (RudCause.Collision, 32)],
            FlightPhase.Ascent =>
                [(RudCause.GroundImpact, 42), (RudCause.AerodynamicForces, 34), (RudCause.ExcessiveGForce, 24)],
            FlightPhase.MaxQ =>
                [(RudCause.AerodynamicForces, 76), (RudCause.ExcessiveGForce, 24)],
            FlightPhase.Staging =>
                [(RudCause.Collision, 70), (RudCause.AerodynamicForces, 30)],
            FlightPhase.Orbit =>
                [(RudCause.Collision, 62), (RudCause.ExcessiveGForce, 38)],
            FlightPhase.Manoeuvre =>
                [(RudCause.ExcessiveGForce, 54), (RudCause.Collision, 46)],
            FlightPhase.Rendezvous =>
                [(RudCause.Collision, 90), (RudCause.ExcessiveGForce, 10)],
            FlightPhase.Transfer =>
                [(RudCause.ExcessiveGForce, 52), (RudCause.Collision, 48)],
            FlightPhase.Reentry =>
                [(RudCause.ExcessiveGForce, 56), (RudCause.AerodynamicForces, 44)],
            FlightPhase.Descent when overWater =>
                [(RudCause.AerodynamicForces, 42), (RudCause.HydrodynamicForces, 34), (RudCause.OceanImpact, 24)],
            FlightPhase.Descent =>
                [(RudCause.AerodynamicForces, 56), (RudCause.GroundImpact, 30), (RudCause.ExcessiveGForce, 14)],
            FlightPhase.Landing when overWater =>
                [(RudCause.OceanImpact, 58), (RudCause.HydrodynamicForces, 42)],
            FlightPhase.Landing =>
                [(RudCause.GroundImpact, 100)],
            _ =>
                [(RudCause.GroundImpact, 100)],
        };
}
