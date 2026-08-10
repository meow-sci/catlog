using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>
/// Replays one short landing profile in two saves, proving career scope keeps both rows while
/// player scope keeps one combined row.
/// </summary>
public sealed class TwoSavesScenario : IScenario
{
    private const string SecondCareer = "2222222222222222";
    private const string VehicleId = "two-saves-lander";
    private const string VehicleName = "Repeatable Lander";

    /// <inheritdoc />
    public string Name => "two-saves";

    /// <inheritdoc />
    public string Summary => "the same short landing in two different saves";

    /// <inheritdoc />
    public string Asserts => "landings has 2 save rows · landings has 1 player row";

    /// <inheritdoc />
    public IEnumerable<SimStep> Steps()
    {
        foreach (SimStep step in LandingProfile(loadSecondSave: false))
            yield return step;

        // KSA may move its sim clock backwards at a save boundary. The pipeline resets every
        // detector before it sees the second profile, exactly as the game-side worker does.
        foreach (SimStep step in LandingProfile(loadSecondSave: true))
            yield return step;
    }

    /// <inheritdoc />
    public void Assert(ReadApiClient api, string handle)
        => api.ExpectCareerRows(handle, "landings", careerRows: 2);

    private static IEnumerable<SimStep> LandingProfile(bool loadSecondSave)
    {
        var vehicle = new SimVehicle(
            VehicleId, VehicleName, SimBodies.Kerbin, crewCount: 1, partCount: 6, massKg: 2_500);
        vehicle.Fly(altitudeM: 20, surfaceSpeedMs: 4, accelMs2: 9.81, dynPressurePa: 100);

        var created = new VehicleCreatedSignal(
            0, SimClock.Wall(0), VehicleId, VehicleName, SimBodies.Kerbin.Name,
            vehicle.MassKg, vehicle.PartCount, vehicle.CrewCount, LaunchGameTime: 0);

        if (loadSecondSave)
        {
            yield return SimStep.At(0)
                .Emit(
                    new SessionLoadedSignal(
                        0, SimClock.Wall(0), ScenarioRunner.GameBuild, ScenarioRunner.ModVersion,
                        CareerId: SecondCareer),
                    created)
                .With(vehicle.Sample(0));
        }
        else
        {
            yield return SimStep.At(0)
                .Emit(created)
                .With(vehicle.Sample(0));
        }

        // The t=0 frame is below the 2 Hz sampling interval. This frame establishes the airborne
        // baseline; the following one is the contact edge that produces vehicle.landed.
        yield return SimStep.At(Play.Dt).With(vehicle.Sample(Play.Dt));

        vehicle.Rest("landed");
        yield return SimStep.At(2 * Play.Dt).With(vehicle.Sample(2 * Play.Dt));

        yield return SimStep.At(3 * Play.Dt)
            .Emit(new VehicleRecoveredSignal(
                3 * Play.Dt, SimClock.Wall(3 * Play.Dt), VehicleId, vehicle.CrewCount));
    }
}
