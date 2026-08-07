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
/// Residents are where the <i>volume</i> comes from. They produce one
/// <c>telemetry.window</c> per thirty sim seconds each and almost nothing else, which is exactly
/// the shape of a real busy save — and exactly the shape that makes the outbox, the batch cap and
/// the projector work for their living. They start already in orbit, so their first sample is a
/// detector baseline and none of them emits a spurious <c>vehicle.orbit: achieved</c>.
/// </remarks>
internal sealed class ResidentActor
{
    private readonly SimVehicle _vehicle;
    private readonly LoadClock _clock;
    private readonly double _driftPeriod;
    private readonly double _driftAmplitude;
    private readonly bool _landed;

    /// <summary>Creates a resident craft.</summary>
    /// <param name="id">The vehicle id.</param>
    /// <param name="ordinal">Its index within the player's fleet.</param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The run clock.</param>
    internal ResidentActor(string id, int ordinal, Prng rng, LoadClock clock)
    {
        _clock = clock;

        SimBody body = rng.Weighted([
            (LoadBodies.Kerbin, 55.0),
            (LoadBodies.All[1], 22.0),
            (LoadBodies.All[3], 12.0),
            (rng.Pick(LoadBodies.Destinations), 11.0),
        ]);

        _landed = rng.Chance(0.22);
        _vehicle = new SimVehicle(
            id,
            _landed ? $"Surface Module {ordinal + 1}" : $"Orbital Element {ordinal + 1}",
            body,
            crewCount: rng.Chance(0.3) ? rng.Int(1, 4) : 0,
            partCount: rng.Int(4, 40),
            massKg: rng.Range(600, 42_000));

        if (_landed)
        {
            _vehicle.Rest(rng.Chance(0.12) ? "floating" : "landed");
        }
        else
        {
            double pe = body.AtmoHeightM + rng.Range(20_000, 400_000);
            double ap = pe + rng.Range(0, 260_000);
            double speed = rng.Range(480, 2_600);
            _vehicle.Orbit(ap, pe, speed, speed - rng.Range(0, 180));
            _vehicle.IncDeg = rng.Range(0, 145);
        }

        _driftPeriod = rng.Range(180, 900);
        _driftAmplitude = _landed ? 0 : rng.Range(2, 60);
    }

    /// <summary>The vehicle id.</summary>
    internal string Id => _vehicle.Id;

    /// <summary>Residents exist for the whole run.</summary>
    /// <param name="simT">Universe sim seconds.</param>
    /// <returns>Always true.</returns>
    internal bool Alive(double simT) => simT >= 0;

    /// <summary>The creation signal, raised at t = 0.</summary>
    /// <returns>The signal.</returns>
    internal VehicleCreatedSignal Created() => new(
        0, _clock.Wall(0), _vehicle.Id, _vehicle.Name, _vehicle.Body.Name,
        _vehicle.MassKg, _vehicle.PartCount, _vehicle.CrewCount, LaunchGameTime: 0);

    /// <summary>The re-registration signal the game raises for this craft after a save load.</summary>
    /// <param name="simT">When the save was loaded.</param>
    /// <returns>The signal.</returns>
    internal VehicleCreatedSignal Recreated(double simT) => new(
        simT, _clock.Wall(simT), _vehicle.Id, _vehicle.Name, _vehicle.Body.Name,
        _vehicle.MassKg, _vehicle.PartCount, _vehicle.CrewCount, LaunchGameTime: 0);

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
            _vehicle.SurfaceSpeedMs = _vehicle.OrbitalSpeedMs - 170;
        }

        return _vehicle.Sample(simT) with { WallMs = _clock.Wall(simT) };
    }
}

/// <summary>A kitten on the surface with the suit on: EVA start, some tumbles, EVA end.</summary>
internal sealed class EvaActor
{
    private readonly SimVehicle _vehicle;
    private readonly LoadClock _clock;
    private readonly string _kitten;
    private readonly double _start;
    private readonly double _end;
    private readonly double _stride;
    private readonly List<double> _tumbleTimes = [];
    private readonly List<double> _tumbleSpeeds = [];
    private readonly SimBody _body;

    /// <summary>Creates an EVA episode.</summary>
    /// <param name="id">The EVA vehicle's id.</param>
    /// <param name="kitten">The kitten's roster name.</param>
    /// <param name="body">The body being walked on.</param>
    /// <param name="startT">When the EVA starts, in sim seconds.</param>
    /// <param name="length">How long it lasts, in sim seconds.</param>
    /// <param name="tumbles">How many times the kitten falls over.</param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The run clock.</param>
    internal EvaActor(
        string id, string kitten, SimBody body, double startT, double length, int tumbles,
        Prng rng, LoadClock clock)
    {
        _clock = clock;
        _kitten = kitten;
        _body = body;
        _start = startT;
        _end = startT + length;
        _stride = rng.Range(18, 48);

        _vehicle = new SimVehicle(id, kitten, body, crewCount: 1, partCount: 2, massKg: rng.Range(88, 104));
        _vehicle.Rest("rolling");

        for (int i = 0; i < tumbles; i++)
        {
            double at = startT + ((i + 1) * length / (tumbles + 1.0));
            _tumbleTimes.Add(at);
            // The stock tumble gate is 6.5 m/s. Most tumbles are just over it; a few are the sort
            // that ends up on a highlight reel.
            _tumbleSpeeds.Add(rng.Chance(0.12) ? rng.Range(14, 34) : rng.Range(6.6, 12.5));
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
            emit(at, [new TumbleSignal(at, _clock.Wall(at), _kitten, _tumbleSpeeds[i], _body.Name)]);
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
        _vehicle.SurfaceSpeedMs = Curve.Lerp(0.2, 9.5, Curve.Ease(phase));
        _vehicle.OrbitalSpeedMs = _vehicle.SurfaceSpeedMs;
        _vehicle.AccelMs2 = 1.63;
        return _vehicle.Sample(simT) with { WallMs = _clock.Wall(simT) };
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
/// Impacts are scheduled one frame before the vehicle leaves the timeline, never on the last one:
/// <see cref="MeowSci.Catlog.Lib.Detect.ImpactCorrelator"/> holds an impact for a full frame to see
/// whether anything destroyed the craft, and a vehicle that vanished in the same frame would be
/// judged on a frame that never happened.
/// </para>
/// </remarks>
internal sealed class MissionActor
{
    private readonly SimVehicle _vehicle;
    private readonly LoadClock _clock;
    private readonly List<Phase> _phases = [];
    private readonly List<GameSignal> _signals = [];
    private readonly MissionProfile _profile;
    private readonly MissionEnd _end;
    private readonly RudCause _cause;
    private readonly IReadOnlyList<string> _crew;
    private readonly int _crewCount;
    private readonly double _touchdownMs;
    private readonly SimBody _launchBody = LoadBodies.Kerbin;
    private readonly SimBody _destination;

    /// <summary>Builds a mission.</summary>
    /// <param name="id">The vehicle id.</param>
    /// <param name="ordinal">Index within the player's run.</param>
    /// <param name="startT">Launch instant, in sim seconds.</param>
    /// <param name="profile">The flight profile.</param>
    /// <param name="end">How it finishes.</param>
    /// <param name="cause">The RUD cause, used only when <paramref name="end"/> is a RUD.</param>
    /// <param name="style">The player's style; sets the appetite for records.</param>
    /// <param name="crew">The player's roster, for crew names.</param>
    /// <param name="rng">The player's generator.</param>
    /// <param name="clock">The run clock.</param>
    internal MissionActor(
        string id,
        int ordinal,
        double startT,
        MissionProfile profile,
        MissionEnd end,
        RudCause cause,
        PlayStyle style,
        IReadOnlyList<string> crew,
        Prng rng,
        LoadClock clock)
    {
        _clock = clock;
        _profile = profile;
        _cause = cause;
        _crew = crew;
        StartT = startT;

        // A craft that leaves the home SOI is not recovered at the launch site, and one that is
        // still under way at the end of the session has not splashed down either. Forcing this
        // rather than letting the weights produce it keeps the generated data plausible.
        _end = profile == MissionProfile.Interplanetary && end is MissionEnd.Recovered or MissionEnd.Splashdown
            ? MissionEnd.Despawned
            : end;

        Length = profile switch
        {
            MissionProfile.Hop => rng.Normal(230, 90, 120, 520),
            MissionProfile.Orbit => rng.Normal(720, 260, 380, 1_500),
            _ => rng.Normal(1_450, 520, 700, 2_800),
        };

        _crewCount = (int)rng.Weighted([(0.0, 34.0), (1.0, 20.0), (2.0, 26.0), (3.0, 12.0), (4.0, 8.0)]);
        _destination = rng.Pick(LoadBodies.Destinations);

        // Records are rare because this draw makes them rare: a daredevil occasionally walks away
        // from something absurd, and everybody else lands at a boring speed.
        bool spectacular = style == PlayStyle.Daredevil ? rng.Chance(0.18) : rng.Chance(0.02);
        _touchdownMs = spectacular
            ? rng.Range(120, 340)
            : rng.Normal(24, 22, 1.5, 95);

        _vehicle = new SimVehicle(
            id,
            $"Flight {ordinal + 1:000}",
            _launchBody,
            crewCount: _crewCount,
            partCount: rng.Int(8, 150),
            massKg: rng.Normal(28_000, 15_000, 2_400, 130_000));

        BuildPhases(rng, style, spectacular);
        BuildSignals(rng);
        EndT = StartT + Length;
    }

    /// <summary>The vehicle id.</summary>
    internal string Id => _vehicle.Id;

    /// <summary>Launch instant, in sim seconds.</summary>
    internal double StartT { get; }

    /// <summary>Mission length, in sim seconds.</summary>
    internal double Length { get; }

    /// <summary>The instant the vehicle leaves the timeline.</summary>
    internal double EndT { get; }

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

    private void BuildPhases(Prng rng, PlayStyle style, bool spectacular)
    {
        double l = Length;
        double atmo = _launchBody.AtmoHeightM;

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

        switch (_profile)
        {
            case MissionProfile.Hop:
            {
                double apogee = rng.Normal(32_000, 24_000, 4_000, 118_000);
                double vMax = rng.Normal(760, 260, 180, 1_800);
                double q = rng.Normal(32_000, 12_000, 6_000, 62_000);

                Add(3, l * 0.34, (v, u) =>
                {
                    v.Fly(
                        Curve.Lerp(0, apogee * 0.82, Curve.Ease(u)),
                        Curve.Lerp(0, vMax, Curve.Ease(u)),
                        Curve.Lerp(13, 29, u),
                        q * Curve.Bell(u, 0.33, 0.35));
                    v.PeAltM = Curve.Lerp(-700_000, -150_000, u);
                    v.ApAltM = Curve.Lerp(0, apogee, u);
                });

                Add(l * 0.34, l * 0.52, (v, u) =>
                {
                    v.Fly(
                        Curve.Lerp(apogee * 0.82, apogee, Math.Sin(u * Math.PI * 0.5)),
                        Curve.Lerp(vMax, vMax * 0.16, Curve.Ease(u)),
                        0.4,
                        Curve.Lerp(900, 20, u));
                    v.Situation = "freefall";
                });

                double peakG = spectacular ? rng.Range(90, 210) : rng.Range(22, 62);
                Add(l * 0.52, l - 6, (v, u) =>
                {
                    v.Fly(
                        Curve.Lerp(apogee, 0, Curve.Ease(u)),
                        u < 0.72
                            ? Curve.Lerp(vMax * 0.16, Math.Max(_touchdownMs, 180), u / 0.72)
                            : Curve.Lerp(Math.Max(_touchdownMs, 180), _touchdownMs, (u - 0.72) / 0.28),
                        Curve.Lerp(9.8, peakG, Curve.Bell(u, 0.78, 0.22)),
                        Curve.Lerp(20, 26_000, Curve.Ease(u)));
                });
                break;
            }

            case MissionProfile.Orbit:
            {
                double pe = rng.Normal(190_000, 90_000, atmo + 6_000, 780_000);
                double ap = pe + rng.Normal(38_000, 40_000, 900, 420_000);
                double orbital = rng.Normal(7_640, 380, 6_400, 9_100);
                double inc = rng.Range(0, 98);
                BuildToOrbit(rng, l, atmo, pe, ap, orbital, inc, 0.14, 0.30, 0.72);

                Add(l * 0.72, l * 0.79, (v, u) =>
                {
                    v.Orbit(ap, Curve.Lerp(pe, -48_000, Curve.Ease(u)), orbital * 0.98, (orbital * 0.98) - 170);
                    v.AltitudeM = Curve.Lerp(pe, pe * 0.55, Curve.Ease(u));
                    v.Situation = "maneuvering";
                    v.AccelMs2 = 2.6;
                    v.PeakG = 0.27;
                });

                double reentryG = spectacular ? rng.Range(80, 190) : rng.Range(30, 74);
                double dryMass = _vehicle.MassKg * rng.Range(0.08, 0.3);
                Add(l * 0.79, l - 6, (v, u) =>
                {
                    v.MassKg = dryMass;
                    v.Fly(
                        Curve.Lerp(pe * 0.55, 0, Curve.Ease(u)),
                        u < 0.6
                            ? Curve.Lerp(orbital * 0.96, 260, u / 0.6)
                            : Curve.Lerp(260, _touchdownMs, (u - 0.6) / 0.4),
                        reentryG * Curve.Bell(u, 0.42, 0.24),
                        46_000 * Curve.Bell(u, 0.46, 0.26));
                    v.PeAltM = -48_000;
                    v.ApAltM = Curve.Lerp(ap, 0, Curve.Ease(u));
                });
                break;
            }

            default:
            {
                double pe = rng.Normal(210_000, 90_000, atmo + 8_000, 700_000);
                double ap = pe + rng.Normal(45_000, 40_000, 1_000, 380_000);
                double orbital = rng.Normal(7_700, 340, 6_600, 9_200);
                BuildToOrbit(rng, l, atmo, pe, ap, orbital, rng.Range(0, 40), 0.12, 0.26, 0.48);

                // The escape burn. A hyperbolic conic is negative-apoapsis, never NaN
                // (docs/ksa-integration.md B4), and the class is what the detector reads.
                double escape = rng.Normal(11_400, 1_600, 9_200, 17_500);
                Add(l * 0.48, l * 0.56, (v, u) =>
                {
                    v.Situation = "maneuvering";
                    v.AltitudeM = Curve.Lerp(pe, pe * 2.4, Curve.Ease(u));
                    v.OrbitalSpeedMs = Curve.Lerp(orbital, escape, Curve.Ease(u));
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs - 170;
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

                Add(l * 0.56, l * 0.62, (v, u) =>
                {
                    v.Situation = "freefall";
                    v.AltitudeM = Curve.Lerp(pe * 2.4, 14_000_000, Curve.Ease(u));
                    v.OrbitalSpeedMs = Curve.Lerp(escape, escape * 0.35, u);
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs;
                    v.AccelMs2 = 0;
                    v.PeakG = null;
                    v.OrbitClass = OrbitClass.Hyperbolic;
                    v.ApAltM = -1;
                    v.PeAltM = -260_000;
                });

                // Arrival: the parent body changes, which is the only thing vehicle.soi needs.
                SimBody target = _destination;
                Add(l * 0.62, l * 0.76, (v, u) =>
                {
                    v.Body = target;
                    v.Situation = "freefall";
                    v.AltitudeM = Curve.Lerp(3_400_000, target.AtmoHeightM + 90_000, Curve.Ease(u));
                    v.OrbitalSpeedMs = Curve.Lerp(escape * 0.35, 1_450, Curve.Ease(u));
                    v.SurfaceSpeedMs = v.OrbitalSpeedMs;
                    v.OrbitClass = OrbitClass.Hyperbolic;
                    v.ApAltM = -1;
                    v.PeAltM = -140_000;
                });

                double capturePe = target.AtmoHeightM + rng.Range(14_000, 180_000);
                double captureAp = capturePe + rng.Range(2_000, 240_000);
                double captureSpeed = rng.Range(420, 2_300);
                Add(l * 0.76, l, (v, _) =>
                {
                    v.Body = target;
                    v.Orbit(captureAp, capturePe, captureSpeed, captureSpeed - 12);
                });
                break;
            }
        }

        // The last few seconds: rolling to a stop, bobbing, or sitting there waiting to be
        // recovered. The correlator needs the craft to still exist for a frame after an impact,
        // and this is that frame.
        if (_profile != MissionProfile.Interplanetary)
        {
            string settled = _end == MissionEnd.Splashdown ? "floating" : "rolling";
            Add(l - 6, l, (v, u) =>
            {
                v.Rest(u < 0.4 ? settled : (_end == MissionEnd.Splashdown ? "floating" : "landed"));
                v.SurfaceSpeedMs = Curve.Lerp(4.0, 0.0, u);
            });
        }
    }

    /// <summary>The shared launch-through-circularisation arc of the orbital profiles.</summary>
    private void BuildToOrbit(
        Prng rng, double l, double atmo, double pe, double ap, double orbital, double inc,
        double stage1, double insertion, double coastEnd)
    {
        double q = rng.Normal(38_000, 11_000, 9_000, 64_000);

        Add(3, l * stage1, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(0, atmo * 0.68, Curve.Ease(u)),
                Curve.Lerp(0, 1_650, Curve.Ease(u)),
                Curve.Lerp(12, 31, u),
                q * Curve.Bell(u, 0.31, 0.33));
            v.PeAltM = Curve.Lerp(-700_000, -390_000, u);
            v.ApAltM = Curve.Lerp(0, atmo * 1.3, u);
        });

        Add(l * stage1, l * insertion, (v, u) =>
        {
            v.Fly(
                Curve.Lerp(atmo * 0.68, pe, Curve.Ease(u)),
                Curve.Lerp(1_650, orbital - 180, Curve.Ease(u)),
                Curve.Lerp(9, 21, u),
                Curve.Lerp(8_400, 0, Curve.Ease(Math.Min(1.0, u * 2.2))));
            v.OrbitalSpeedMs = Curve.Lerp(1_650, orbital, Curve.Ease(u));
            // The rising edge the orbit-achieved rule fires on: periapsis clears the atmosphere
            // plus the §7.2 one-kilometre margin only in the last fraction of the insertion.
            v.PeAltM = Curve.Lerp(-390_000, pe, Curve.Ease(u));
            v.ApAltM = Curve.Lerp(atmo * 1.3, ap, Curve.Ease(u));
            v.Ecc = Curve.Lerp(0.86, 0.03, Curve.Ease(u));
            v.IncDeg = inc;
        });

        Add(l * insertion, l * coastEnd, (v, _) =>
        {
            v.Orbit(ap, pe, orbital, orbital - 175);
            v.IncDeg = inc;
        });
    }

    private void BuildSignals(Prng rng)
    {
        double l = Length;
        long w(double r) => _clock.Wall(StartT + r);
        double s(double r) => StartT + r;

        _signals.Add(new VehicleCreatedSignal(
            s(0), w(0), _vehicle.Id, _vehicle.Name, _launchBody.Name,
            _vehicle.MassKg, _vehicle.PartCount, _crewCount, LaunchGameTime: s(0)));

        string booster = rng.Pick(["RE-M3 Mainsail", "LV-T45 Swivel", "LV-909 Terrier", "S1 SRB-KD25k", "RE-I5 Skipper"]);
        string upper = rng.Pick(["LV-909 Terrier", "LV-N Nerv", "RE-L10 Poodle"]);

        _signals.Add(new EngineSignal(s(3), w(3), _vehicle.Id, EngineEventKind.Ignition, booster, rng.Int(1, 6)));
        _signals.Add(new StagingSignal(s(3), w(3), _vehicle.Id, 0));

        int stages = _profile switch
        {
            MissionProfile.Hop => rng.Int(1, 4),
            MissionProfile.Orbit => rng.Int(2, 6),
            _ => rng.Int(3, 8),
        };
        for (int i = 1; i <= stages; i++)
        {
            double at = s(l * (0.10 + (0.55 * i / (stages + 1.0))));
            _signals.Add(new StagingSignal(at, _clock.Wall(at), _vehicle.Id, i));
        }

        double cutoff = s(l * 0.30);
        // A stage that runs dry rather than being shut down. The game has no flameout concept —
        // the game project derives it from IsActive && !IsPropellantAvailable — so it is its own
        // event type and worth producing.
        _signals.Add(rng.Chance(0.22)
            ? new EngineSignal(cutoff, _clock.Wall(cutoff), _vehicle.Id, EngineEventKind.Flameout, booster, 1)
            : new EngineSignal(cutoff, _clock.Wall(cutoff), _vehicle.Id, EngineEventKind.Shutdown, booster, rng.Int(1, 4)));

        if (_profile != MissionProfile.Hop)
        {
            double relight = s(l * 0.46);
            _signals.Add(new EngineSignal(relight, _clock.Wall(relight), _vehicle.Id, EngineEventKind.Ignition, upper, 1));
            double quiet = s(l * 0.52);
            _signals.Add(new EngineSignal(quiet, _clock.Wall(quiet), _vehicle.Id, EngineEventKind.Shutdown, upper, 1));
        }

        BuildTerminus(rng);
    }

    private void BuildTerminus(Prng rng)
    {
        double last = StartT + Length - PlayerScript.Dt;
        double touchdown = StartT + Length - 6;
        SimBody impactBody = _profile == MissionProfile.Interplanetary ? _destination : _launchBody;
        double energy = Curve.Energy(_vehicle.MassKg * 0.2, _touchdownMs);

        switch (_end)
        {
            case MissionEnd.Recovered:
                _signals.Add(new ImpactSignal(
                    touchdown, _clock.Wall(touchdown), _vehicle.Id,
                    SpeedMs: _touchdownMs, EnergyJ: energy,
                    // A pad strike is excluded from the lithobrake board, so a few of them are
                    // generated on purpose: the exclusion has to be exercised, not assumed.
                    LaunchPad: rng.Chance(0.05),
                    Body: impactBody.Name, CrewCount: _crewCount));
                _signals.Add(new VehicleRecoveredSignal(last, _clock.Wall(last), _vehicle.Id, _crewCount));
                break;

            case MissionEnd.Splashdown:
                _signals.Add(new SplashSignal(
                    touchdown, _clock.Wall(touchdown), _vehicle.Id,
                    SpeedMs: Math.Min(_touchdownMs, 90), EnergyJ: energy,
                    Body: impactBody.Name, CrewCount: _crewCount));
                _signals.Add(new VehicleRecoveredSignal(last, _clock.Wall(last), _vehicle.Id, _crewCount));
                break;

            case MissionEnd.Rud:
            {
                bool struckSomething = _cause is RudCause.GroundImpact or RudCause.OceanImpact;
                if (struckSomething)
                {
                    // Same frame as the destruction, so the correlator resolves it to
                    // survived = false. That is the interesting half of the impact rule.
                    _signals.Add(new ImpactSignal(
                        last, _clock.Wall(last), _vehicle.Id,
                        SpeedMs: Math.Max(_touchdownMs, rng.Range(60, 620)), EnergyJ: energy,
                        LaunchPad: false, Body: impactBody.Name, CrewCount: _crewCount));
                }

                _signals.Add(new RudSignal(
                    last, _clock.Wall(last), _vehicle.Id, _cause,
                    PeakG: rng.Range(6, 120),
                    PeakQPa: rng.Range(2_000, 90_000),
                    SpeedMs: rng.Range(40, 2_400),
                    AltitudeM: rng.Range(0, 60_000),
                    Body: impactBody.Name,
                    // Per D11 physics destruction never kills crew.
                    CrewCount: _crewCount));
                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Destroyed, _crewCount));
                break;
            }

            case MissionEnd.ManualDestroy:
            {
                // Scuttling after a hard landing is the exact move the correlator's one-frame hold
                // exists to stop from banking a "survived" record, so it is generated deliberately.
                _signals.Add(new ImpactSignal(
                    touchdown, _clock.Wall(touchdown), _vehicle.Id,
                    SpeedMs: Math.Max(_touchdownMs, 40), EnergyJ: energy,
                    LaunchPad: false, Body: impactBody.Name, CrewCount: _crewCount));
                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Destroyed, _crewCount));

                // The only path that sets the game's Kia flag (docs/ksa-integration.md §4).
                for (int i = 0; i < _crewCount && i < _crew.Count; i++)
                {
                    _signals.Add(new KiaSignal(
                        last, _clock.Wall(last), _crew[i], KiaContext.ManualDestroy));
                }

                break;
            }

            default:
                _signals.Add(new VehicleRemovedSignal(
                    last, _clock.Wall(last), _vehicle.Id, FlightEndReason.Despawned, _crewCount));
                break;
        }
    }

    private void Add(double t0, double t1, Action<SimVehicle, double> apply)
        => _phases.Add(new Phase(t0, Math.Max(t0 + 0.5, t1), apply));

    private sealed record Phase(double T0, double T1, Action<SimVehicle, double> Apply);
}
