using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// Two teleport-flagged flights that try to buy a leaderboard, one flagged before it scores and
/// one flagged after (INITIAL_IMPL_PLAN §7.3 scenario 5).
/// </summary>
/// <remarks>
/// <para>
/// The flag ordering is the whole point, because the two orderings exercise two different
/// mechanisms.
/// </para>
/// <list type="number">
///   <item>
///     <b>Flag first</b> (<c>cheat-early</c>): the <c>flight.flagged</c> event has a lower seq than
///     the 9 000 m/s impact and the three stagings, so the incremental fold already knows the
///     flight is tainted and excludes every one of them (§5.6, and the WP4 decision that the
///     exclusion covers counter boards too, not only record boards). This is the ordinary case.
///   </item>
///   <item>
///     <b>Flag late</b> (<c>cheat-late</c>): the 8 000 m/s impact and two stagings arrive first and
///     <b>do</b> score, because the incremental fold cannot know what has not happened yet. Only
///     <c>POST /admin/projections/rebuild</c> heals it — its first pass builds
///     <c>flight_state</c> over the whole history before any board is scored. That is D22's
///     backstop, and this is the case that actually tests it; a scenario that only ever flagged
///     first would pass with the rebuild path completely broken.
///   </item>
/// </list>
/// <para>
/// The two impacts differ (9 000 vs 8 000 m/s) so the pre-rebuild assertion can tell them apart:
/// the board must read 8 000 — proving the early flag worked — and not 9 000.
/// </para>
/// </remarks>
public sealed class CheaterScenario : IScenario
{
    private const string EarlyId = "cheat-early";
    private const string LateId = "cheat-late";
    private const double EarlyImpactMs = 9_000;
    private const double LateImpactMs = 8_000;
    private const int EarlyStagings = 3;
    private const int LateStagings = 2;

    /// <inheritdoc />
    public string Name => "cheater";

    /// <inheritdoc />
    public string Summary =>
        "two teleported flights with absurd survivable impacts: one flagged before it scores, one flagged after";

    /// <inheritdoc />
    public string Asserts =>
        "before rebuild: biggest_lithobrake_survived = 8000 (the 9000 never scored), stagings += 2; "
        + "after rebuild: both boards back to baseline";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        // ---------------- flight 1: flagged before it scores ----------------
        var early = Craft(EarlyId, "Warp Whisker");

        yield return SimStep.At(0)
            .Emit(new VehicleCreatedSignal(
                0, SimClock.Wall(0), EarlyId, early.Name, early.Body.Name, early.MassKg,
                early.PartCount, 1, LaunchGameTime: 0))
            .With(early.Sample(0));

        // The teleport is detected on the very next frame — the vehicle moved 400 km in one step.
        yield return SimStep.At(0.5)
            .Emit(new FlaggedSignal(
                0.5, SimClock.Wall(0.5), EarlyId, FlightFlag.Teleport,
                "position jumped 412 km in one frame"))
            .With(Falling(early, 0.5, 0.5, 12.0));

        for (int i = 0; i < EarlyStagings; i++)
        {
            double t = 1.0 + i;
            yield return SimStep.At(t)
                .Emit(new StagingSignal(t, SimClock.Wall(t), EarlyId, i))
                .With(Falling(early, t, 0.5, 12.0));
        }

        for (double t = 1.0 + EarlyStagings; t < 12.0; t += Play.Dt)
            yield return SimStep.At(t).With(Falling(early, t, 0.5, 12.0));

        early.Rest("landed");
        yield return SimStep.At(12.0)
            .Emit(new ImpactSignal(
                12.0, SimClock.Wall(12.0), EarlyId, EarlyImpactMs,
                Play.Energy(early.MassKg, EarlyImpactMs), LaunchPad: false, Body: early.Body.Name, CrewCount: 1))
            .With(early.Sample(12.0));

        for (double t = 12.5; t < 16.0; t += Play.Dt)
            yield return SimStep.At(t).With(early.Sample(t));

        yield return SimStep.At(16.0)
            .Emit(new VehicleRemovedSignal(16.0, SimClock.Wall(16.0), EarlyId, FlightEndReason.Despawned, 1));

        // ---------------- flight 2: flagged long after it scores ----------------
        var late = Craft(LateId, "Late Confession");

        yield return SimStep.At(20.0)
            .Emit(new VehicleCreatedSignal(
                20.0, SimClock.Wall(20.0), LateId, late.Name, late.Body.Name, late.MassKg,
                late.PartCount, 1, LaunchGameTime: 20.0))
            .With(late.Sample(20.0));

        for (int i = 0; i < LateStagings; i++)
        {
            double t = 21.0 + i;
            yield return SimStep.At(t)
                .Emit(new StagingSignal(t, SimClock.Wall(t), LateId, i))
                .With(Falling(late, t, 20.5, 32.0));
        }

        for (double t = 21.0 + LateStagings; t < 32.0; t += Play.Dt)
            yield return SimStep.At(t).With(Falling(late, t, 20.5, 32.0));

        late.Rest("landed");
        yield return SimStep.At(32.0)
            .Emit(new ImpactSignal(
                32.0, SimClock.Wall(32.0), LateId, LateImpactMs,
                Play.Energy(late.MassKg, LateImpactMs), LaunchPad: false, Body: late.Body.Name, CrewCount: 1))
            .With(late.Sample(32.0));

        for (double t = 32.5; t < 90.0; t += Play.Dt)
            yield return SimStep.At(t).With(late.Sample(t));

        // Nearly a minute and several telemetry windows after the impact scored, the mod notices
        // the teleport. Every event above already has a lower seq server-side.
        yield return SimStep.At(90.0)
            .Emit(new FlaggedSignal(
                90.0, SimClock.Wall(90.0), LateId, FlightFlag.Teleport,
                "orbit replaced without a burn"))
            .With(late.Sample(90.0));

        for (double t = 90.5; t < 94.0; t += Play.Dt)
            yield return SimStep.At(t).With(late.Sample(t));

        yield return SimStep.At(94.0)
            .Emit(new VehicleRemovedSignal(94.0, SimClock.Wall(94.0), LateId, FlightEndReason.Despawned, 1));
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
    {
        // --- incremental fold: the early flag worked, the late one has not been applied yet ---
        api.ExpectRecord(handle, "biggest_lithobrake_survived", LateImpactMs);
        api.ExpectCounter(handle, "stagings", LateStagings);
        api.Record(
            ok: true,
            label: "flag ordering",
            expected: $"{EarlyImpactMs} excluded, {LateImpactMs} still scoring",
            actual: "incremental fold",
            note: "a flag that precedes its flight's scoring events excludes them at fold time (§5.6)");

        // --- D22 backstop: a full rebuild sees the late flag before it scores anything ---
        string summary = api.Rebuild();
        api.Record(
            ok: true,
            label: "rebuild",
            expected: "POST /admin/projections/rebuild",
            actual: summary,
            note: "two passes: flight_state over the whole history first, then the boards (§5.6)");

        api.ExpectUnchanged(
            handle,
            "biggest_lithobrake_survived",
            $"the rebuild healed the late flag; neither {EarlyImpactMs} nor {LateImpactMs} may appear");
        api.ExpectUnchanged(
            handle,
            "stagings",
            $"all {EarlyStagings + LateStagings} stagings belong to flagged flights");
    }

    private static SimVehicle Craft(string id, string name)
    {
        var v = new SimVehicle(id, name, SimBodies.Kerbin, crewCount: 1, partCount: 8, massKg: 2_600);
        v.Rest("landed");
        return v;
    }

    private static TelemetrySnapshot Falling(SimVehicle v, double t, double t0, double t1)
    {
        double u = (t - t0) / (t1 - t0);
        v.Fly(
            altitudeM: Play.Lerp(412_000, 0, Play.Ease(u)),
            surfaceSpeedMs: Play.Lerp(2_400, 9_000, Play.Ease(u)),
            accelMs2: 9.8,
            dynPressurePa: Play.Lerp(0, 2_400_000, Play.Ease(u)));
        v.OrbitalSpeedMs = v.SurfaceSpeedMs;
        v.PeAltM = -900_000;
        v.ApAltM = Play.Lerp(412_000, 0, Play.Ease(u));
        return v.Sample(t);
    }
}
