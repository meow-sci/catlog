using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// A craft that exists for the whole run: a station element, a relay, a surface base.
/// </summary>
/// <remarks>
/// <para>
/// Residents are where the <i>volume</i> comes from. They produce one
/// <c>telemetry.window</c> per thirty sim seconds each and almost nothing else, which is exactly
/// the shape of a real busy save — and exactly the shape that makes the outbox, the batch cap and
/// the projector work for their living. They start already in orbit, so their first sample is a
/// detector baseline and none of them emits a spurious <c>vehicle.orbit: achieved</c>.
/// </para>
/// <para>
/// A resident is a career artefact: it was launched at some point in the player's past, which is
/// what <c>launchGameTime</c> carries, and it can only be somewhere the career has already been.
/// </para>
/// </remarks>
internal sealed class ResidentActor
{
    private readonly SimVehicle _vehicle;
    private readonly CareerClock _clock;
    private readonly LoadBody _body;
    private readonly double _launchGameTime;
    private readonly double _driftPeriod;
    private readonly double _driftAmplitude;
    private readonly bool _landed;

    /// <summary>Creates a resident craft.</summary>
    /// <param name="id">The vehicle id.</param>
    /// <param name="ordinal">Its index within the player's fleet.</param>
    /// <param name="body">The body it is at.</param>
    /// <param name="launchGameTime">When in the career it was launched, in in-game seconds.</param>
    /// <param name="mustOrbit">
    /// True to keep this craft off the surface whatever the draw says — the station a career at or
    /// past the operator stage always has something to rendezvous with.
    /// </param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The player's career clock.</param>
    internal ResidentActor(
        string id, int ordinal, LoadBody body, double launchGameTime, bool mustOrbit,
        Prng rng, CareerClock clock)
    {
        _clock = clock;
        _body = body;
        _launchGameTime = launchGameTime;
        // The draw happens either way, so forcing a craft into orbit costs the stream nothing.
        _landed = body.Landable && rng.Chance(0.22) && !mustOrbit;

        _vehicle = new SimVehicle(
            id,
            _landed ? $"Surface Module {ordinal + 1}" : $"Orbital Element {ordinal + 1}",
            body.Sim,
            crewCount: rng.Chance(0.3) ? rng.Int(1, 4) : 0,
            partCount: rng.Int(4, 40),
            massKg: rng.Range(600, 42_000));

        if (_landed)
        {
            _vehicle.Rest(body.Ocean && rng.Chance(0.12) ? "floating" : "landed");
        }
        else
        {
            double pe = body.ParkingFloorM + rng.Range(4_000, Math.Max(20_000, body.RadiusM * 0.6));
            double ap = pe + rng.Range(0, Math.Max(10_000, body.RadiusM * 0.4));
            Orbits.Circular(_vehicle, body, ap, pe);
            _vehicle.IncDeg = rng.Range(0, 145);
        }

        _driftPeriod = rng.Range(180, 900);
        // Station keeping is a small correction, not a manoeuvre: an amplitude scaled to the body's
        // own orbital speed keeps a relay round Phobos — where orbital speed is about 4 m/s — from
        // appearing to change velocity by more than its escape speed.
        _driftAmplitude = _landed ? 0 : Math.Max(0.4, body.OrbitSpeedAt(body.ParkingFloorM) * rng.Range(0.001, 0.02));
    }

    /// <summary>The vehicle id.</summary>
    internal string Id => _vehicle.Id;

    /// <summary>The body this craft is at.</summary>
    internal LoadBody Body => _body;

    /// <summary>True when it is sitting on a surface rather than in orbit.</summary>
    internal bool Landed => _landed;

    /// <summary>Residents exist for the whole run.</summary>
    /// <param name="simT">Career sim seconds.</param>
    /// <returns>Always true.</returns>
    internal bool Alive(double simT) => simT >= _clock.Epoch;

    /// <summary>The creation signal, raised as the run's window opens.</summary>
    /// <returns>The signal.</returns>
    internal VehicleCreatedSignal Created() => new(
        _clock.Epoch, _clock.Wall(_clock.Epoch), _vehicle.Id, _vehicle.Name, _vehicle.Body.Name,
        _vehicle.MassKg, _vehicle.PartCount, _vehicle.CrewCount, LaunchGameTime: _launchGameTime);

    /// <summary>The re-registration signal the game raises for this craft after a save load.</summary>
    /// <param name="simT">When the save was loaded.</param>
    /// <returns>The signal.</returns>
    internal VehicleCreatedSignal Recreated(double simT) => new(
        simT, _clock.Wall(simT), _vehicle.Id, _vehicle.Name, _vehicle.Body.Name,
        _vehicle.MassKg, _vehicle.PartCount, _vehicle.CrewCount, LaunchGameTime: _launchGameTime);

    /// <summary>Samples the craft.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>The snapshot.</returns>
    internal TelemetrySnapshot Sample(double simT)
    {
        if (!_landed && _driftAmplitude > 0)
        {
            // A little orbital variation, so a window's min/max/mean are three different numbers
            // rather than the same constant three times — which is what the projector's speed
            // boards actually fold.
            double phase = Math.Sin(2 * Math.PI * simT / _driftPeriod);
            _vehicle.OrbitalSpeedMs += _driftAmplitude * phase * 0.01;
            _vehicle.SurfaceSpeedMs = _vehicle.OrbitalSpeedMs - _body.RotationMs;
        }

        return _vehicle.Sample(simT) with { WallMs = _clock.Wall(simT) };
    }
}

/// <summary>A kitten on the surface with the suit on: EVA start, some tumbles, EVA end.</summary>
internal sealed class EvaActor
{
    private readonly SimVehicle _vehicle;
    private readonly CareerClock _clock;
    private readonly LoadBody _body;
    private readonly string _kitten;
    private readonly double _start;
    private readonly double _end;
    private readonly double _stride;
    private readonly List<double> _tumbleTimes = [];
    private readonly List<double> _tumbleSpeeds = [];

    /// <summary>Creates an EVA episode.</summary>
    /// <param name="id">The EVA vehicle's id.</param>
    /// <param name="kitten">The kitten's roster name.</param>
    /// <param name="body">The body being walked on; must be one a kitten can stand on.</param>
    /// <param name="startT">When the EVA starts, in sim seconds.</param>
    /// <param name="length">How long it lasts, in sim seconds.</param>
    /// <param name="tumbles">How many times the kitten falls over.</param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The player's career clock.</param>
    internal EvaActor(
        string id, string kitten, LoadBody body, double startT, double length, int tumbles,
        Prng rng, CareerClock clock)
    {
        _clock = clock;
        _kitten = kitten;
        _body = body;
        _start = startT;
        _end = startT + length;
        _stride = rng.Range(18, 48);

        _vehicle = new SimVehicle(id, kitten, body.Sim, crewCount: 1, partCount: 2, massKg: rng.Range(88, 104));
        _vehicle.Rest("rolling");

        for (int i = 0; i < tumbles; i++)
        {
            double at = startT + ((i + 1) * length / (tumbles + 1.0));
            _tumbleTimes.Add(at);
            // The stock tumble gate is 6.5 m/s (docs/ksa-integration.md B9) and the game classifies
            // the transition, so the harness emits the signal the game would emit rather than
            // re-deriving the rule. Most tumbles are just over the gate; a few are the sort that
            // ends up on a highlight reel — and low gravity makes those much easier to reach.
            double reach = 1.0 + Math.Clamp(3.0 / Math.Max(0.05, body.SurfaceGravityMs2), 0.4, 3.2);
            _tumbleSpeeds.Add(rng.Chance(0.12)
                ? rng.Range(12.0, 12.0 + (10.0 * reach))
                : rng.Range(6.6, 6.6 + (2.4 * reach)));
        }
    }

    /// <summary>True while the kitten is outside.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Whether to sample.</returns>
    internal bool Alive(double simT) => simT >= _start && simT < _end;

    /// <summary>Registers the EVA's discrete signals.</summary>
    /// <param name="emit">The script's scheduling callback.</param>
    internal void Schedule(Action<double, GameSignal[]> emit)
    {
        emit(_start, [
            new VehicleCreatedSignal(
                _start, _clock.Wall(_start), _vehicle.Id, _kitten, _body.Name,
                _vehicle.MassKg, _vehicle.PartCount, 1, LaunchGameTime: _start),
            new EvaStartSignal(_start, _clock.Wall(_start), _kitten, _vehicle.Id),
        ]);

        for (int i = 0; i < _tumbleTimes.Count; i++)
        {
            double at = _tumbleTimes[i];
            emit(at, [new TumbleSignal(
                at, _clock.Wall(at), _kitten, i % 2 == 0 ? "airborne" : "grounded",
                _tumbleSpeeds[i], _body.Name)]);
        }

        emit(_end, [
            new EvaEndSignal(_end, _clock.Wall(_end), _kitten, _end - _start),
            new VehicleRemovedSignal(_end, _clock.Wall(_end), _vehicle.Id, FlightEndReason.Despawned, 1),
        ]);
    }

    /// <summary>Samples the kitten.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>The snapshot.</returns>
    internal TelemetrySnapshot Sample(double simT)
    {
        double phase = (simT - _start) % _stride / _stride;
        _vehicle.Situation = phase < 0.85 ? "rolling" : "dragging";
        _vehicle.AltitudeM = 1.1 + (0.6 * Math.Sin(phase * Math.PI));
        // A kitten covers ground faster where it weighs less — but never faster than the body can
        // hold on to it, which is why EvaActor is only ever pointed at a Walkable body.
        double top = Math.Min(
            Math.Clamp(9.5 / Math.Sqrt(Math.Max(0.1, _body.SurfaceGravityMs2) / 9.81), 6.0, 26.0),
            _body.EscapeSpeedAt(0) * 0.4);
        _vehicle.SurfaceSpeedMs = Curve.Lerp(0.2, top, Curve.Ease(phase));
        _vehicle.OrbitalSpeedMs = _vehicle.SurfaceSpeedMs;
        _vehicle.AccelMs2 = _body.SurfaceGravityMs2;
        return _vehicle.Sample(simT) with { WallMs = _clock.Wall(simT) };
    }
}

/// <summary>Puts a <see cref="SimVehicle"/> on an orbit that is right for the body it is round.</summary>
internal static class Orbits
{
    /// <summary>Parks a craft on a closed orbit, on rails, at this body's own speeds.</summary>
    /// <param name="vehicle">The craft.</param>
    /// <param name="body">The parent body.</param>
    /// <param name="apAltM">Apoapsis altitude, in metres.</param>
    /// <param name="peAltM">Periapsis altitude, in metres.</param>
    internal static void Circular(SimVehicle vehicle, LoadBody body, double apAltM, double peAltM)
    {
        double speed = body.OrbitSpeedAt((apAltM + peAltM) * 0.5);
        vehicle.Orbit(apAltM, peAltM, speed, speed - body.RotationMs);
        // SimVehicle derives eccentricity with the home world's radius baked in, which is right
        // for the three-body universe the six scenarios fly in and wrong round a moon.
        vehicle.Ecc = body.Eccentricity(apAltM, peAltM);
    }
}

/// <summary>
/// One launch-to-verdict mission, flown as a sequence of phases.
/// </summary>
/// <remarks>
/// <para>
/// The phases are what produce the detector's five polled families without ever naming them: a
/// craft that climbs past the atmosphere height produces <c>vehicle.atmosphere: exited</c> because
/// its altitude crossed a boundary, not because the harness asked for one. The same is true of
/// <c>vehicle.situation</c>, <c>vehicle.orbit</c> both ways, and <c>vehicle.soi</c>. If the
/// detector's rules change, these numbers move — which is the property that makes this a test
/// rather than a fixture.
/// </para>
/// <para>
/// <b>A lost flight ends where it was lost.</b> The profile is always laid out over the mission's
/// <i>planned</i> length, and a failure truncates the <i>timeline</i> instead of rescaling the
/// profile — so a pad failure produces four seconds of telemetry, one ignition and a RUD, and a
/// botched landing produces the whole flight and then a hole in the ground. Rescaling would make
/// every failure look like a complete mission that happened to end badly, which is the opposite of
/// what a career looks like.
/// </para>
/// <para>
/// Impacts are scheduled one frame before the vehicle leaves the timeline, never on the last one:
/// <see cref="MeowSci.Catlog.Lib.Detect.ImpactCorrelator"/> holds an impact for a full frame to see
/// whether anything destroyed the craft, and a vehicle that vanished in the same frame would be
/// judged on a frame that never happened.
/// </para>
/// </remarks>
internal sealed class MissionActor
{
    private static readonly string[] Boosters =
        ["RE-M3 Mainsail", "LV-T45 Swivel", "LV-909 Terrier", "S1 SRB-KD25k", "RE-I5 Skipper"];

    private static readonly string[] Uppers = ["LV-909 Terrier", "LV-N Nerv", "RE-L10 Poodle"];

    private readonly SimVehicle _vehicle;
    private readonly CareerClock _clock;
    private readonly MissionSpec _spec;
    private readonly List<Phase> _phases = [];
    private readonly List<GameSignal> _signals = [];
    private readonly IReadOnlyList<string> _crew;
    private readonly LoadBody _launch = LoadBodies.Earth;
    private readonly LoadBody _destination;
    private readonly double _planned;
    private readonly double _touchdownMs;

    /// <summary>Builds a mission from its plan.</summary>
    /// <param name="id">The vehicle id.</param>
    /// <param name="spec">The planned flight; every instant in it is career time.</param>
    /// <param name="crew">The player's roster, for crew names.</param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The player's career clock.</param>
    internal MissionActor(
        string id,
        MissionSpec spec,
        IReadOnlyList<string> crew,
        Prng rng,
        CareerClock clock)
    {
        _clock = clock;
        _spec = spec;
        _crew = crew;
        _destination = spec.Destination;
        _planned = spec.Length;

        LoadBody terminal = spec.Kind == MissionKind.Landing ? _destination : _launch;
        // Records are rare because this is rare: almost everybody lands at a boring speed, and now
        // and then somebody walks away from something that should have been a crater.
        _touchdownMs = spec.Spectacular
            ? Math.Clamp(terminal.TouchdownSpeedMs(rng) * rng.Range(6.0, 22.0), 40, 340)
            : terminal.TouchdownSpeedMs(rng);

        _vehicle = new SimVehicle(
            id,
            Name(spec),
            _launch.Sim,
            crewCount: spec.CrewCount,
            partCount: PartCount(rng, spec.Kind),
            massKg: Mass(rng, spec.Kind));

        BuildPhases(rng);
        BuildSignals(rng);
    }

    /// <summary>The vehicle id.</summary>
    internal string Id => _vehicle.Id;

    /// <summary>Launch instant, in sim seconds.</summary>
    internal double StartT => _spec.StartT;

    /// <summary>How long the craft is actually in the simulation, in sim seconds.</summary>
    internal double Length => _spec.EffectiveLength;

    /// <summary>The instant the vehicle leaves the timeline.</summary>
    internal double EndT => _spec.EndT;

    /// <summary>Where this mission was going.</summary>
    internal LoadBody Destination => _destination;

    /// <summary>When the craft docked, or null when it never rendezvoused.</summary>
    internal double? DockAt { get; private set; }

    /// <summary>When the craft touched down somewhere other than home, or null.</summary>
    internal double? SurfaceArrival { get; private set; }

    /// <summary>True when the craft actually got into the destination's SOI before it stopped.</summary>
    internal bool ReachedDestination { get; private set; }

    /// <summary>True when the craft left the home sphere of influence at all.</summary>
    /// <remarks>
    /// A transfer that came apart mid-cruise still visited the star, and the board counts it.
    /// </remarks>
    internal bool LeftHomeSoi { get; private set; }

    /// <summary>True while the craft is in the simulation.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Whether to sample.</returns>
    internal bool Alive(double simT) => simT >= StartT && simT < EndT;

    /// <summary>Registers the mission's discrete signals on the script's schedule.</summary>
    /// <param name="emit">The scheduling callback.</param>
    internal void Schedule(Action<double, GameSignal[]> emit)
    {
        foreach (GameSignal signal in _signals)
            emit(signal.SimT, [signal]);
    }

    /// <summary>Advances the craft to <paramref name="simT"/> and samples it.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>The snapshot.</returns>
    internal TelemetrySnapshot Sample(double simT)
    {
        double r = simT - StartT;
        for (int i = _phases.Count - 1; i >= 0; i--)
        {
            Phase phase = _phases[i];
            if (r < phase.T0)
                continue;
            double span = Math.Max(1e-6, phase.T1 - phase.T0);
            phase.Apply(_vehicle, Math.Clamp((r - phase.T0) / span, 0.0, 1.0));
            break;
        }

        return _vehicle.Sample(simT) with { WallMs = _clock.Wall(simT) };
    }

    // --- profile construction ---------------------------------------------------------

    private void BuildPhases(Prng rng)
    {
        double l = _planned;
        double atmo = _launch.AtmoHeightM;

        // On the pad, and staying there long enough for the detector to take a baseline: a
        // periapsis deep inside the body is what stops the orbit-achieved latch seeding true.
        Add(0, 3, static (v, _) =>
        {
            v.Rest("landed");
            v.PeAltM = -700_000;
            v.ApAltM = 0;
            v.Ecc = 0.9;
            v.OrbitClass = OrbitClass.Bound;
        });

        switch (_spec.Kind)
        {
            case MissionKind.PadTest:
                Ballistic(rng, l, rng.Range(80, 2_600), rng.Range(30, 140), rng.Range(400, 4_000), 0.35, 0.5);
                break;

            case MissionKind.Hop:
                // Stays inside the atmosphere: a hop is not a spaceflight and must not produce a
                // vehicle.atmosphere pair just because it went up a long way.
                Ballistic(
                    rng, l,
                    apogee: rng.Normal(30_000, 17_000, 4_000, atmo * 0.88),
                    vMax: rng.Normal(760, 260, 180, 1_700),
                    maxQ: rng.Normal(32_000, 12_000, 6_000, 62_000),
                    climbEnd: 0.34, apogeeAt: 0.52);
                break;

            case MissionKind.HighHop:
                // Clears the atmosphere by a comfortable margin, so the ±2 % hysteresis band is
                // crossed cleanly in both directions and the reentry is a real one.
                Ballistic(
                    rng, l,
                    apogee: rng.Normal(atmo * 1.9, atmo * 0.55, atmo * 1.25, atmo * 4.5),
                    vMax: rng.Normal(2_300, 450, 1_500, 3_400),
                    maxQ: rng.Normal(41_000, 12_000, 12_000, 68_000),
                    climbEnd: 0.30, apogeeAt: 0.52);
                break;

            case MissionKind.Orbit:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.14, 0.32);
                Add(l * 0.32, l, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });
                break;
            }

            case MissionKind.Manoeuvre:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.12, 0.28);
                Add(l * 0.28, l * 0.55, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                // The manoeuvre itself: a plane change and an apoapsis raise, under power, which is
                // a freefall → maneuvering → freefall pair the detector has to see.
                double newAp = ap + rng.Range(40_000, 900_000);
                double newInc = Math.Clamp(inc + rng.Range(-38, 38), 0, 145);
                double burn = _launch.OrbitSpeedAt(pe) * rng.Range(1.02, 1.14);
                Add(l * 0.55, l * 0.62, (v, u) =>
                {
                    v.Situation = "maneuvering";
                    v.AltitudeM = pe;
                    v.OrbitalSpeedMs = Curve.Lerp(_launch.OrbitSpeedAt(pe), burn, Curve.Ease(u));
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs - _launch.RotationMs;
                    v.AccelMs2 = 12.0;
                    v.PeakG = 1.22;
                    v.DynPressurePa = 0;
                    v.MaxQPa = null;
                    v.ApAltM = Curve.Lerp(ap, newAp, Curve.Ease(u));
                    v.PeAltM = pe;
                    v.Ecc = _launch.Eccentricity(v.ApAltM, pe);
                    v.IncDeg = Curve.Lerp(inc, newInc, Curve.Ease(u));
                    v.OrbitClass = OrbitClass.Bound;
                });

                Add(l * 0.62, l, (v, _) =>
                {
                    Orbits.Circular(v, _launch, newAp, pe);
                    v.IncDeg = newInc;
                });
                break;
            }

            case MissionKind.Rendezvous:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.11, 0.26);
                Add(l * 0.26, l * 0.48, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                // Phasing: a small burn onto a matching orbit, then the close.
                double targetPe = pe + rng.Range(-12_000, 22_000);
                double trim = rng.Range(0.998, 1.004);
                Add(l * 0.48, l * 0.58, (v, u) =>
                {
                    v.Situation = "maneuvering";
                    v.AltitudeM = Curve.Lerp(pe, targetPe, Curve.Ease(u));
                    v.OrbitalSpeedMs = _launch.OrbitSpeedAt(v.AltitudeM) * trim;
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs - _launch.RotationMs;
                    v.AccelMs2 = 1.9;
                    v.PeakG = 0.19;
                    v.DynPressurePa = 0;
                    v.MaxQPa = null;
                    v.ApAltM = Curve.Lerp(ap, targetPe + 3_000, Curve.Ease(u));
                    v.PeAltM = v.AltitudeM;
                    v.Ecc = _launch.Eccentricity(v.ApAltM, v.PeAltM);
                    v.IncDeg = inc;
                    v.OrbitClass = OrbitClass.Bound;
                });

                Add(l * 0.58, l, (v, _) =>
                {
                    Orbits.Circular(v, _launch, targetPe + 3_000, targetPe);
                    v.IncDeg = inc;
                });

                DockAt = StartT + (l * 0.70);
                break;
            }

            case MissionKind.Deorbit:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.11, 0.26);
                Add(l * 0.26, l * 0.62, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                // The retrograde burn: periapsis drops inside the atmosphere, which re-arms the
                // orbit-achieved latch so a later flight's insertion is a rising edge again.
                double orbital = _launch.OrbitSpeedAt(pe);
                Add(l * 0.62, l * 0.72, (v, u) =>
                {
                    v.Situation = "maneuvering";
                    v.AltitudeM = Curve.Lerp(pe, pe * 0.72, Curve.Ease(u));
                    v.OrbitalSpeedMs = orbital * Curve.Lerp(1.0, 0.94, u);
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs - _launch.RotationMs;
                    v.AccelMs2 = 3.1;
                    v.PeakG = 0.32;
                    v.DynPressurePa = 0;
                    v.MaxQPa = null;
                    v.ApAltM = ap;
                    v.PeAltM = Curve.Lerp(pe, -48_000, Curve.Ease(u));
                    v.Ecc = Math.Clamp(_launch.Eccentricity(ap, Math.Max(0, v.PeAltM)), 0, 0.94);
                    v.OrbitClass = OrbitClass.Bound;
                });

                Reentry(rng, l * 0.72, l - 6, pe * 0.72, orbital * 0.94, ap);
                break;
            }

            case MissionKind.Transfer:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.10, 0.24);
                Add(l * 0.24, l * 0.40, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                Escape(rng, l * 0.40, l * 0.52, pe, ap);
                Cruise(l * 0.52, l * 0.66, pe);
                Arrive(rng, l * 0.66, l * 0.80);
                double capturePe = _destination.ParkingFloorM + rng.Range(6_000, Math.Max(30_000, _destination.RadiusM * 0.5));
                double captureAp = capturePe + rng.Range(2_000, Math.Max(20_000, _destination.RadiusM * 0.8));
                Add(l * 0.80, l, (v, _) =>
                {
                    v.Body = _destination.Sim;
                    Orbits.Circular(v, _destination, captureAp, capturePe);
                });
                break;
            }

            case MissionKind.Landing:
            {
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.08, 0.20);
                Add(l * 0.20, l * 0.32, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                Escape(rng, l * 0.32, l * 0.42, pe, ap);
                Cruise(l * 0.42, l * 0.56, pe);
                Arrive(rng, l * 0.56, l * 0.70);

                double parkPe = _destination.ParkingFloorM + rng.Range(4_000, Math.Max(20_000, _destination.RadiusM * 0.3));
                double parkAp = parkPe + rng.Range(1_000, 20_000);
                Add(l * 0.70, l * 0.82, (v, _) =>
                {
                    v.Body = _destination.Sim;
                    Orbits.Circular(v, _destination, parkAp, parkPe);
                });

                Descend(rng, l * 0.82, l - 6, parkPe, _destination);
                // Only a craft that actually got down leaves a kitten anywhere to walk.
                if (StartT + l - 6 < EndT)
                    SurfaceArrival = StartT + l - 6;
                break;
            }

            default:
            {
                // A probe: out of the home SOI, across the system, past something far away, and
                // then on out. It never captures — the conic stays hyperbolic all the way.
                (double pe, double ap, double inc) = Parking(rng, _launch);
                ToOrbit(rng, l, pe, ap, inc, 0.08, 0.20);
                Add(l * 0.20, l * 0.30, (v, _) =>
                {
                    Orbits.Circular(v, _launch, ap, pe);
                    v.IncDeg = inc;
                });

                Escape(rng, l * 0.30, l * 0.42, pe, ap);
                Cruise(l * 0.42, l * 0.62, pe);
                Arrive(rng, l * 0.62, l * 0.78);

                double flyby = _destination.ParkingFloorM + rng.Range(20_000, Math.Max(80_000, _destination.RadiusM));
                double outbound = _destination.EscapeSpeedAt(flyby) * rng.Range(1.04, 1.3);
                Add(l * 0.78, l, (v, u) =>
                {
                    v.Body = _destination.Sim;
                    v.Situation = "freefall";
                    v.AltitudeM = Curve.Lerp(flyby, flyby * 26.0, Curve.Ease(u));
                    v.OrbitalSpeedMs = Curve.Lerp(outbound, outbound * 0.55, u);
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs;
                    v.AccelMs2 = 0;
                    v.DynPressurePa = 0;
                    v.PeakG = null;
                    v.MaxQPa = null;
                    v.Ecc = 1.6;
                    v.OrbitClass = OrbitClass.Hyperbolic;
                    v.ApAltM = -1;
                    v.PeAltM = -Math.Max(40_000, _destination.RadiusM * 0.4);
                });
                break;
            }
        }

        // The last few seconds: rolling to a stop, bobbing, or sitting there waiting to be
        // recovered. The correlator needs the craft to still exist for a frame after an impact,
        // and this is that frame.
        if (_spec.Kind is MissionKind.PadTest or MissionKind.Hop or MissionKind.HighHop
            or MissionKind.Deorbit or MissionKind.Landing)
        {
            string settled = _spec.Outcome == MissionOutcome.Splashdown ? "floating" : "rolling";
            Add(l - 6, l, (v, u) =>
            {
                v.Rest(u < 0.4 ? settled : (_spec.Outcome == MissionOutcome.Splashdown ? "floating" : "landed"));
                v.SurfaceSpeedMs = Curve.Lerp(4.0, 0.0, u);
            });
        }
    }

    /// <summary>A ballistic up-and-down: the pad test, the hop and the high lob.</summary>
    private void Ballistic(
        Prng rng, double l, double apogee, double vMax, double maxQ, double climbEnd, double apogeeAt)
    {
        Add(3, l * climbEnd, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(0, apogee * 0.82, Curve.Ease(u)),
                Curve.Lerp(0, vMax, Curve.Ease(u)),
                Curve.Lerp(13, 29, u),
                maxQ * Curve.Bell(u, 0.33, 0.35));
            v.PeAltM = Curve.Lerp(-700_000, -150_000, u);
            v.ApAltM = Curve.Lerp(0, apogee, u);
        });

        Add(l * climbEnd, l * apogeeAt, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(apogee * 0.82, apogee, Math.Sin(u * Math.PI * 0.5)),
                Curve.Lerp(vMax, vMax * 0.16, Curve.Ease(u)),
                0.4,
                Curve.Lerp(900, 20, u));
            v.Situation = "freefall";
        });

        double peakG = _spec.Spectacular ? rng.Range(70, 190) : rng.Range(14, 52);
        Add(l * apogeeAt, l - 6, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(apogee, 0, Curve.Ease(u)),
                u < 0.72
                    ? Curve.Lerp(vMax * 0.16, Math.Max(_touchdownMs, vMax * 0.5), u / 0.72)
                    : Curve.Lerp(Math.Max(_touchdownMs, vMax * 0.5), _touchdownMs, (u - 0.72) / 0.28),
                Curve.Lerp(9.8, peakG, Curve.Bell(u, 0.78, 0.22)),
                Curve.Lerp(20, 26_000, Curve.Ease(u)));
        });
    }

    /// <summary>The shared launch-through-circularisation arc of every orbital profile.</summary>
    private void ToOrbit(Prng rng, double l, double pe, double ap, double inc, double stage1, double insertion)
    {
        double atmo = _launch.AtmoHeightM;
        double orbital = _launch.OrbitSpeedAt(pe);
        double q = rng.Normal(38_000, 11_000, 9_000, 64_000);

        Add(3, l * stage1, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(0, atmo * 0.68, Curve.Ease(u)),
                Curve.Lerp(0, orbital * 0.62, Curve.Ease(u)),
                Curve.Lerp(12, 31, u),
                q * Curve.Bell(u, 0.31, 0.33));
            v.PeAltM = Curve.Lerp(-700_000, -390_000, u);
            v.ApAltM = Curve.Lerp(0, atmo * 1.3, u);
        });

        Add(l * stage1, l * insertion, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(atmo * 0.68, pe, Curve.Ease(u)),
                Curve.Lerp(orbital * 0.62, orbital - _launch.RotationMs, Curve.Ease(u)),
                Curve.Lerp(9, 21, u),
                Curve.Lerp(8_400, 0, Curve.Ease(Math.Min(1.0, u * 2.2))));
            v.OrbitalSpeedMs = Curve.Lerp(orbital * 0.62, orbital, Curve.Ease(u));
            // The rising edge the orbit-achieved rule fires on: periapsis clears the atmosphere
            // plus the §7.2 one-kilometre margin only in the last fraction of the insertion.
            v.PeAltM = Curve.Lerp(-390_000, pe, Curve.Ease(u));
            v.ApAltM = Curve.Lerp(atmo * 1.3, ap, Curve.Ease(u));
            v.Ecc = Curve.Lerp(0.86, _launch.Eccentricity(ap, pe), Curve.Ease(u));
            v.IncDeg = inc;
        });
    }

    /// <summary>The escape burn: bound conic to hyperbolic, which is what <c>orbit: escaped</c> reads.</summary>
    private void Escape(Prng rng, double t0, double t1, double pe, double ap)
    {
        // A hyperbolic conic is negative-apoapsis, never NaN (docs/ksa-integration.md B4), and the
        // class is what the detector reads.
        double orbital = _launch.OrbitSpeedAt(pe);
        double escape = _launch.EscapeSpeedAt(pe) * rng.Range(1.01, 1.18);

        Add(t0, t1, (v, u) =>
        {
            v.Situation = "maneuvering";
            v.AltitudeM = Curve.Lerp(pe, pe * 2.4, Curve.Ease(u));
            v.OrbitalSpeedMs = Curve.Lerp(orbital, escape, Curve.Ease(u));
            v.SurfaceSpeedMs = v.OrbitalSpeedMs - _launch.RotationMs;
            v.AccelMs2 = 8.4;
            v.DynPressurePa = 0;
            v.PeakG = 0.86;
            v.MaxQPa = null;
            v.Ecc = Curve.Lerp(0.4, 1.6, Curve.Ease(u));
            v.OrbitClass = u > 0.55 ? OrbitClass.Hyperbolic : OrbitClass.Bound;
            v.ApAltM = u > 0.55 ? -1 : Curve.Lerp(ap, 4_000_000, u);
            // Below the orbit-achieved bar, so the latch re-arms and the capture at the
            // destination is a rising edge rather than a no-op.
            v.PeAltM = Curve.Lerp(pe, -260_000, Curve.Ease(u));
        });
    }

    /// <summary>
    /// The coast between departure and arrival.
    /// </summary>
    /// <remarks>
    /// Anything going further than the Moon leaves Earth's sphere of influence and spends the
    /// crossing in the <b>star's</b>, which is what the game does and what makes the SOI chain on
    /// the wire read <c>earth → sol → mars</c> rather than teleporting between planets. A lunar
    /// transfer never leaves Earth's SOI at all, so it does not get a sol leg.
    /// </remarks>
    private void Cruise(double t0, double t1, double pe)
    {
        double escape = _launch.EscapeSpeedAt(pe);
        bool heliocentric = _destination.Reach != BodyReach.Moon;
        LoadBody cruiseBody = heliocentric ? LoadBodies.Sol : _launch;
        // Heliocentric altitudes are measured from the star's surface, so a craft loitering near
        // Earth's orbit sits at about 1.49e11 m and moves in or out from there.
        double near = heliocentric ? 1.489e11 : pe * 2.4;
        double far = heliocentric
            ? (_destination.Reach == BodyReach.Outer ? 6.0e11 : 2.0e11)
            : 300_000_000.0;

        Add(t0, t1, (v, u) =>
        {
            v.Body = cruiseBody.Sim;
            v.Situation = "freefall";
            v.AltitudeM = Curve.Lerp(near, far, Curve.Ease(u));
            v.OrbitalSpeedMs = Curve.Lerp(escape, escape * 0.28, u);
            v.SurfaceSpeedMs = v.OrbitalSpeedMs;
            v.AccelMs2 = 0;
            v.DynPressurePa = 0;
            v.PeakG = null;
            v.MaxQPa = null;
            v.Ecc = 1.2;
            v.OrbitClass = OrbitClass.Hyperbolic;
            v.ApAltM = -1;
            v.PeAltM = -260_000;
        });

        LeftHomeSoi = heliocentric && StartT + t0 + 1 < EndT;
    }

    /// <summary>
    /// Arrival: the parent body changes, which is the only thing <c>vehicle.soi</c> needs.
    /// </summary>
    /// <remarks>
    /// A craft bound for a moon of another planet enters that planet's SOI first — Phobos is inside
    /// Mars, so a Phobos mission visits Mars on the way whether the player meant to or not, and the
    /// <c>soi_bodies</c> board counts both because both actually happened.
    /// </remarks>
    private void Arrive(Prng rng, double t0, double t1)
    {
        // An arrival is a hyperbolic pass: escape speed plus a modest excess, not a multiple of it.
        // At Jupiter escape is already 59 km/s, so a loose multiplier here is what would put a
        // physically impossible number at the top of the fastest-speed board.
        double approach = _destination.EscapeSpeedAt(_destination.ParkingFloorM) * rng.Range(1.02, 1.22);
        double entry = Math.Max(_destination.RadiusM * 4.0, 400_000);

        LoadBody? via = LoadBodies.ParentOf(_destination);
        double split = t0;
        if (via is not null && via.Reach is not (BodyReach.Star or BodyReach.Home))
        {
            split = t0 + ((t1 - t0) * 0.5);
            LoadBody host = via;
            double hostEntry = Math.Max(host.RadiusM * 4.0, 400_000);
            Add(t0, split, (v, u) =>
            {
                v.Body = host.Sim;
                v.Situation = "freefall";
                v.AltitudeM = Curve.Lerp(hostEntry, host.ParkingFloorM + (host.RadiusM * 0.4), Curve.Ease(u));
                v.OrbitalSpeedMs = host.EscapeSpeedAt(host.ParkingFloorM) * Curve.Lerp(0.6, 1.1, u);
                v.SurfaceSpeedMs = v.OrbitalSpeedMs;
                v.AccelMs2 = 0;
                v.DynPressurePa = 0;
                v.PeakG = null;
                v.MaxQPa = null;
                v.Ecc = 1.25;
                v.OrbitClass = OrbitClass.Hyperbolic;
                v.ApAltM = -1;
                v.PeAltM = -Math.Max(40_000, host.RadiusM * 0.3);
            });
        }

        Add(split, t1, (v, u) =>
        {
            v.Body = _destination.Sim;
            v.Situation = "freefall";
            v.AltitudeM = Curve.Lerp(entry, _destination.ParkingFloorM + (_destination.RadiusM * 0.2), Curve.Ease(u));
            v.OrbitalSpeedMs = Curve.Lerp(approach * 0.55, approach, Curve.Ease(u));
            v.SurfaceSpeedMs = v.OrbitalSpeedMs;
            v.AccelMs2 = 0;
            v.DynPressurePa = 0;
            v.PeakG = null;
            v.MaxQPa = null;
            v.Ecc = 1.3;
            v.OrbitClass = OrbitClass.Hyperbolic;
            v.ApAltM = -1;
            v.PeAltM = -Math.Max(40_000, _destination.RadiusM * 0.3);
        });

        // Reached only if the craft is still flying when the parent body changes.
        ReachedDestination = StartT + t1 < EndT;
    }

    /// <summary>Atmospheric entry and the descent behind it — the reentry g and the max-Q on the way down.</summary>
    private void Reentry(Prng rng, double t0, double t1, double fromAltM, double speed, double ap)
    {
        double peak = _spec.Spectacular ? rng.Range(70, 180) : rng.Range(18, 58);
        double dryMass = _vehicle.MassKg * rng.Range(0.08, 0.3);

        Add(t0, t1, (v, u) =>
        {
            v.MassKg = dryMass;
            // Fly derives peak_g from the acceleration, so the g spike is expressed as one — a
            // reading the game only has under full physics, which is exactly where this is.
            v.Fly(
                Curve.Lerp(fromAltM, 0, Curve.Ease(u)),
                u < 0.6
                    ? Curve.Lerp(speed, 260, u / 0.6)
                    : Curve.Lerp(260, _touchdownMs, (u - 0.6) / 0.4),
                Math.Max(9.81, peak * 9.81 * Curve.Bell(u, 0.42, 0.24)),
                46_000 * Curve.Bell(u, 0.46, 0.26));
            v.PeAltM = -48_000;
            v.ApAltM = Curve.Lerp(ap, 0, Curve.Ease(u));
            v.Ecc = 0.9;
            v.OrbitClass = OrbitClass.Bound;
        });
    }

    /// <summary>Powered descent to another body's surface — no parachutes where there is no air.</summary>
    private void Descend(Prng rng, double t0, double t1, double fromAltM, LoadBody body)
    {
        double start = body.OrbitSpeedAt(fromAltM);
        double heat = body.AtmoHeightM > 0 ? rng.Range(8_000, 40_000) : 0;
        double peak = body.AtmoHeightM > 0
            ? (_spec.Spectacular ? rng.Range(40, 120) : rng.Range(8, 34))
            : rng.Range(0.5, 4.0) * Math.Max(0.3, body.SurfaceGravityMs2 / 9.81);

        Add(t0, t1, (v, u) =>
        {
            v.Body = body.Sim;
            v.Situation = "maneuvering";
            v.AltitudeM = Curve.Lerp(fromAltM, 0, Curve.Ease(u));
            v.SurfaceSpeedMs = u < 0.7
                ? Curve.Lerp(start, Math.Max(_touchdownMs, start * 0.12), u / 0.7)
                : Curve.Lerp(Math.Max(_touchdownMs, start * 0.12), _touchdownMs, (u - 0.7) / 0.3);
            v.OrbitalSpeedMs = v.SurfaceSpeedMs;
            v.AccelMs2 = Math.Max(body.SurfaceGravityMs2, peak * 9.81 * Curve.Bell(u, 0.5, 0.3));
            v.DynPressurePa = heat * Curve.Bell(u, 0.5, 0.28);
            v.PeakG = peak * Curve.Bell(u, 0.5, 0.3);
            v.MaxQPa = heat > 0 ? v.DynPressurePa : null;
            v.PeAltM = -Math.Max(20_000, body.RadiusM * 0.2);
            v.ApAltM = Curve.Lerp(fromAltM, 0, Curve.Ease(u));
            v.Ecc = 0.85;
            v.OrbitClass = OrbitClass.Bound;
        });
    }

    private (double Pe, double Ap, double Inc) Parking(Prng rng, LoadBody body)
    {
        double pe = body.ParkingFloorM + rng.Normal(90_000, 70_000, 2_000, Math.Max(60_000, body.RadiusM * 1.2));
        double ap = pe + rng.Normal(38_000, 40_000, 900, Math.Max(40_000, body.RadiusM * 0.8));
        return (pe, ap, rng.Range(0, 98));
    }

    // --- discrete signals -------------------------------------------------------------

    private void BuildSignals(Prng rng)
    {
        double l = _planned;
        long w(double r) => _clock.Wall(StartT + r);
        double s(double r) => StartT + r;

        // The launch instant is already career time, which is exactly what LaunchGameTime means:
        // vehicle id plus launch time is the flight's identity across a save load.
        _signals.Add(new VehicleCreatedSignal(
            s(0), w(0), _vehicle.Id, _vehicle.Name, _launch.Name,
            _vehicle.MassKg, _vehicle.PartCount, _spec.CrewCount, LaunchGameTime: StartT));

        string booster = rng.Pick(Boosters);
        string upper = rng.Pick(Uppers);

        _signals.Add(new EngineSignal(s(3), w(3), _vehicle.Id, EngineEventKind.Ignition, booster, rng.Int(1, 6)));
        _signals.Add(new StagingSignal(s(3), w(3), _vehicle.Id, 0));

        int stages = _spec.Kind switch
        {
            MissionKind.PadTest => rng.Int(1, 3),
            MissionKind.Hop or MissionKind.HighHop => rng.Int(1, 4),
            MissionKind.Orbit or MissionKind.Manoeuvre or MissionKind.Deorbit => rng.Int(2, 6),
            _ => rng.Int(3, 8),
        };
        for (int i = 1; i <= stages; i++)
        {
            double at = s(l * (0.10 + (0.55 * i / (stages + 1.0))));
            _signals.Add(new StagingSignal(at, _clock.Wall(at), _vehicle.Id, i));
        }

        double cutoff = s(l * 0.22);
        // A stage that runs dry rather than being shut down. The game has no flameout concept —
        // the game project derives it from IsActive && !IsPropellantAvailable — so it is its own
        // event type and worth producing.
        _signals.Add(rng.Chance(0.22)
            ? new EngineSignal(cutoff, _clock.Wall(cutoff), _vehicle.Id, EngineEventKind.Flameout, booster, 1)
            : new EngineSignal(cutoff, _clock.Wall(cutoff), _vehicle.Id, EngineEventKind.Shutdown, booster, rng.Int(1, 4)));

        if (_spec.Kind is not (MissionKind.PadTest or MissionKind.Hop or MissionKind.HighHop))
        {
            double relight = s(l * 0.42);
            _signals.Add(new EngineSignal(relight, _clock.Wall(relight), _vehicle.Id, EngineEventKind.Ignition, upper, 1));
            double quiet = s(l * 0.48);
            _signals.Add(new EngineSignal(quiet, _clock.Wall(quiet), _vehicle.Id, EngineEventKind.Shutdown, upper, 1));
        }

        // Everything scheduled past the point the craft was lost never happened.
        _signals.RemoveAll(signal => signal.SimT >= EndT - 0.001 && signal.SimT > StartT);

        BuildTerminus(rng);
    }

    private void BuildTerminus(Prng rng)
    {
        double last = EndT - PlayerScript.Dt;
        double touchdown = EndT - 6;
        LoadBody impactBody = ReachedDestination ? _destination : _launch;
        double energy = Curve.Energy(_vehicle.MassKg * 0.2, _touchdownMs);

        switch (_spec.Outcome)
        {
            case MissionOutcome.Recovered:
                _signals.Add(new ImpactSignal(
                    touchdown, _clock.Wall(touchdown), _vehicle.Id,
                    SpeedMs: _touchdownMs, EnergyJ: energy,
                    // A pad strike is excluded from the lithobrake board, so a few of them are
                    // generated on purpose: the exclusion has to be exercised, not assumed.
                    LaunchPad: _spec.Kind == MissionKind.PadTest ? rng.Chance(0.45) : rng.Chance(0.04),
                    Body: impactBody.Name, CrewCount: _spec.CrewCount));
                _signals.Add(new VehicleRecoveredSignal(last, _clock.Wall(last), _vehicle.Id, _spec.CrewCount));
                break;

            case MissionOutcome.Splashdown:
                _signals.Add(new SplashSignal(
                    touchdown, _clock.Wall(touchdown), _vehicle.Id,
                    SpeedMs: Math.Min(_touchdownMs, 90), EnergyJ: energy,
                    Body: impactBody.Name, CrewCount: _spec.CrewCount));
                _signals.Add(new VehicleRecoveredSignal(last, _clock.Wall(last), _vehicle.Id, _spec.CrewCount));
                break;

            case MissionOutcome.Rud:
            {
                (double peakG, double peakQ, double speed, double altitude) = Reading(rng, impactBody);
                bool struckSomething = _spec.Cause is RudCause.GroundImpact or RudCause.OceanImpact;
                if (struckSomething)
                {
                    // Same frame as the destruction, so the correlator resolves it to
                    // survived = false. That is the interesting half of the impact rule.
                    _signals.Add(new ImpactSignal(
                        last, _clock.Wall(last), _vehicle.Id,
                        SpeedMs: Math.Max(1.0, speed), EnergyJ: Curve.Energy(_vehicle.MassKg * 0.2, speed),
                        LaunchPad: _spec.FailPhase == FlightPhase.Pad,
                        Body: impactBody.Name, CrewCount: _spec.CrewCount));
                }

                _signals.Add(new RudSignal(
                    last, _clock.Wall(last), _vehicle.Id, _spec.Cause,
                    PeakG: peakG,
                    PeakQPa: peakQ,
                    SpeedMs: speed,
                    AltitudeM: altitude,
                    Body: impactBody.Name,
                    // Per D11 physics destruction never kills crew.
                    CrewCount: _spec.CrewCount,
                    PartCount: _vehicle.PartCount));
                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Destroyed, _spec.CrewCount));
                break;
            }

            case MissionOutcome.Scuttled:
            {
                // Scuttling after a hard landing is the exact move the correlator's one-frame hold
                // exists to stop from banking a "survived" record, so it is generated deliberately.
                // Only for a craft that was actually coming down on something, though: a station
                // scuttled in orbit never touches anything, and a probe abandoned at Jupiter has no
                // surface to touch — an impact there would put a lithobrake on a gas giant.
                if (Descends(_spec.Kind) && impactBody.Landable)
                {
                    _signals.Add(new ImpactSignal(
                        touchdown, _clock.Wall(touchdown), _vehicle.Id,
                        SpeedMs: Math.Max(_touchdownMs, 40), EnergyJ: energy,
                        LaunchPad: false, Body: impactBody.Name, CrewCount: _spec.CrewCount));
                }

                // KillCrew runs before the vehicle is disposed, and that order is what gives the
                // KIA a flight to name: emitting it after the removal would leave the pipeline with
                // a retired flight and a null attribution, which is not what a scuttle looks like.
                var killed = new List<string>();
                for (int i = 0; i < _spec.CrewCount && i < _crew.Count; i++)
                    killed.Add(_crew[i]);
                if (killed.Count > 0)
                    _signals.Add(new CrewKilledSignal(last, _clock.Wall(last), _vehicle.Id, killed));

                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Destroyed, _spec.CrewCount));

                // The only path that sets the game's Kia flag (docs/ksa-integration.md §4).
                foreach (string name in killed)
                    _signals.Add(new KiaSignal(last, _clock.Wall(last), name, KiaContext.ManualDestroy));
                break;
            }

            default:
                // A craft that landed somewhere else and stayed there still touched down, and that
                // touchdown is the only lithobrake record anyone will ever set off-world.
                if (_spec.Kind == MissionKind.Landing && impactBody.Landable)
                {
                    _signals.Add(new ImpactSignal(
                        touchdown, _clock.Wall(touchdown), _vehicle.Id,
                        SpeedMs: _touchdownMs, EnergyJ: energy,
                        LaunchPad: false, Body: impactBody.Name, CrewCount: _spec.CrewCount));
                }

                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Despawned, _spec.CrewCount));
                break;
        }
    }

    /// <summary>
    /// The numbers a destruction in this phase reports.
    /// </summary>
    /// <remarks>
    /// A RUD payload carries peak g, peak dynamic pressure, speed and altitude, and those four have
    /// to agree with where the vehicle was when it came apart. A pad fire at 40 kPa and 60 km would
    /// be nonsense on the wire even though nothing in the pipeline would reject it.
    /// </remarks>
    private (double PeakG, double PeakQ, double Speed, double Altitude) Reading(Prng rng, LoadBody body)
    {
        double orbital = body.OrbitSpeedAt(body.ParkingFloorM);
        return _spec.FailPhase switch
        {
            FlightPhase.Pad => (rng.Range(2, 9), rng.Range(0, 400), rng.Range(0, 24), rng.Range(0, 70)),
            FlightPhase.Ascent => (rng.Range(3, 13), rng.Range(9_000, 40_000), rng.Range(140, 900), rng.Range(1_500, 26_000)),
            FlightPhase.MaxQ => (rng.Range(4, 16), rng.Range(34_000, 72_000), rng.Range(380, 1_300), rng.Range(7_000, 21_000)),
            // Ordered explicitly: on a body whose whole orbital speed is 7 m/s the upper bound
            // would otherwise sit below the lower one, and Prng.Range does not sort its arguments.
            FlightPhase.Staging => (rng.Range(2, 11), rng.Range(2_000, 30_000), rng.Range(orbital * 0.2, Math.Max(orbital * 0.9, orbital * 0.25)), rng.Range(28_000, 62_000)),
            FlightPhase.Orbit => (rng.Range(0.2, 2.5), 0, orbital * rng.Range(0.96, 1.02), body.ParkingFloorM * rng.Range(1.0, 3.0)),
            FlightPhase.Manoeuvre => (rng.Range(0.6, 5.0), 0, orbital * rng.Range(0.94, 1.12), body.ParkingFloorM * rng.Range(1.0, 2.4)),
            FlightPhase.Rendezvous => (rng.Range(0.2, 3.5), 0, orbital * rng.Range(0.98, 1.01), body.ParkingFloorM * rng.Range(1.0, 2.0)),
            FlightPhase.Transfer => (rng.Range(0.5, 6.0), 0, body.EscapeSpeedAt(body.ParkingFloorM) * rng.Range(0.8, 1.4), rng.Range(500_000, 9_000_000)),
            FlightPhase.Reentry => (_spec.Spectacular ? rng.Range(70, 180) : rng.Range(9, 62), rng.Range(20_000, 62_000), orbital * rng.Range(0.7, 1.0), rng.Range(28_000, 64_000)),
            FlightPhase.Descent => (rng.Range(3, 22), rng.Range(6_000, 48_000), rng.Range(90, 900), rng.Range(600, 22_000)),
            FlightPhase.Landing => (rng.Range(5, 45), rng.Range(0, 6_000), Math.Clamp(_touchdownMs * rng.Range(2.0, 9.0), 4.0, 900.0), rng.Range(0, 60)),
            _ => (rng.Range(4, 30), rng.Range(0, 30_000), rng.Range(20, 600), rng.Range(0, 8_000)),
        };
    }

    /// <summary>True for the kinds that end by coming down on something.</summary>
    private static bool Descends(MissionKind kind) => kind
        is MissionKind.PadTest or MissionKind.Hop or MissionKind.HighHop
        or MissionKind.Deorbit or MissionKind.Landing;

    private static string Name(MissionSpec spec) => spec.Kind switch
    {
        MissionKind.PadTest => $"Test Article {spec.Ordinal + 1:000}",
        MissionKind.Probe => $"Probe {spec.Ordinal + 1:000}",
        MissionKind.Landing => $"Lander {spec.Ordinal + 1:000}",
        MissionKind.Transfer => $"Transfer Stage {spec.Ordinal + 1:000}",
        MissionKind.Rendezvous => $"Resupply {spec.Ordinal + 1:000}",
        _ => $"Flight {spec.Ordinal + 1:000}",
    };

    private static int PartCount(Prng rng, MissionKind kind) => kind switch
    {
        MissionKind.PadTest => rng.Int(4, 24),
        MissionKind.Hop => rng.Int(8, 48),
        MissionKind.HighHop => rng.Int(12, 70),
        MissionKind.Probe => rng.Int(14, 60),
        MissionKind.Transfer or MissionKind.Landing => rng.Int(30, 180),
        _ => rng.Int(18, 120),
    };

    private static double Mass(Prng rng, MissionKind kind) => kind switch
    {
        MissionKind.PadTest => rng.Normal(4_200, 2_600, 900, 16_000),
        MissionKind.Hop => rng.Normal(16_000, 9_000, 2_400, 52_000),
        MissionKind.HighHop => rng.Normal(34_000, 14_000, 8_000, 90_000),
        MissionKind.Probe => rng.Normal(46_000, 18_000, 12_000, 120_000),
        MissionKind.Transfer or MissionKind.Landing => rng.Normal(78_000, 26_000, 24_000, 190_000),
        _ => rng.Normal(44_000, 18_000, 9_000, 140_000),
    };

    private void Add(double t0, double t1, Action<SimVehicle, double> apply)
        => _phases.Add(new Phase(t0, Math.Max(t0 + 0.5, t1), apply));

    private sealed record Phase(double T0, double T1, Action<SimVehicle, double> Apply);
}
