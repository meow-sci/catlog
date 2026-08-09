using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>
/// Factories with defaulted parameters so a test names only the field it cares about, and uses
/// <c>with</c> for deltas. (The pattern from <c>gatOS.SimFs.Tests/TestData.cs</c>; the detector
/// tests would otherwise be walls of thirty-argument constructors.)
/// </summary>
internal static class TestData
{
    /// <summary>A plausible wall-clock stamp; fixed so envelopes are reproducible.</summary>
    internal const long WallMs = 1_770_000_000_000;

    /// <summary>Earth-like atmosphere height in metres, used as the default in these fixtures.</summary>
    internal const double AtmoHeight = 70_000;

    internal static TelemetrySnapshot Snapshot(
        string vehicleId = "v1",
        double simT = 100.0,
        string body = "earth",
        string situation = "freefall",
        double altitudeM = 100_000,
        double surfaceSpeedMs = 0,
        double orbitalSpeedMs = 0,
        double accelMs2 = 0,
        double massKg = 1_000,
        double atmoHeightM = AtmoHeight,
        double dynPressurePa = 0,
        double ecc = 0,
        double apAltM = 0,
        double peAltM = 0,
        double incDeg = 0,
        OrbitClass orbitClass = OrbitClass.Unknown,
        int crewCount = 0,
        int partCount = 1,
        double? peakG = null,
        double? maxQPa = null,
        long wallMs = WallMs,
        string? parentBodyId = null,
        string? vehicleName = null)
        => new(
            VehicleId: vehicleId,
            VehicleName: vehicleName ?? $"Vehicle {vehicleId}",
            SimT: simT,
            WallMs: wallMs,
            Body: body,
            Situation: situation,
            AltitudeM: altitudeM,
            SurfaceSpeedMs: surfaceSpeedMs,
            OrbitalSpeedMs: orbitalSpeedMs,
            AccelMs2: accelMs2,
            MassKg: massKg)
        {
            ParentBodyId = parentBodyId ?? body,
            AtmoHeightM = atmoHeightM,
            DynPressurePa = dynPressurePa,
            Ecc = ecc,
            ApAltM = apAltM,
            PeAltM = peAltM,
            IncDeg = incDeg,
            OrbitClass = orbitClass,
            CrewCount = crewCount,
            PartCount = partCount,
            PeakG = peakG,
            MaxQPa = maxQPa,
        };

    internal static TelemetryFrame Frame(long sequence, params TelemetrySnapshot[] vehicles)
        => new(sequence, vehicles.Length == 0 ? 0 : vehicles[0].SimT, WallMs, vehicles);

    internal static EventEnvelope Envelope(
        string type = EventTypes.VehicleStaging,
        string? id = null,
        string session = "01J9V5M3E8Z0FAKESESSION01",
        string? career = null,
        string? flight = "01J9V5M3E8Z0FAKEFLIGHT001",
        double simT = 12.5,
        long wallMs = WallMs,
        object? payload = null)
        => EventEnvelope.Create(
            type, session, career ?? CareerId, flight, simT, wallMs,
            payload ?? new VehicleStagingPayload(1), id);

    internal static IReadOnlyList<EventEnvelope> Envelopes(
        int count, string type = EventTypes.VehicleStaging, long wallMs = WallMs)
    {
        var list = new List<EventEnvelope>(count);
        for (int i = 0; i < count; i++)
            list.Add(Envelope(type: type, simT: i, wallMs: wallMs, payload: new VehicleStagingPayload(i)));
        return list;
    }

    internal static ImpactSignal Impact(
        string vehicleId = "v1",
        double simT = 10,
        double speedMs = 62,
        double energyJ = 1_922_000,
        bool launchPad = false,
        string body = "earth",
        int crewCount = 2)
        => new(simT, WallMs, vehicleId, speedMs, energyJ, launchPad, body, crewCount);

    internal static RudSignal Rud(
        string vehicleId = "v1",
        double simT = 10,
        RudCause cause = RudCause.GroundImpact,
        double peakG = 40,
        double peakQPa = 0,
        double speedMs = 62,
        double altitudeM = 0,
        string body = "earth",
        int crewCount = 2)
        => new(simT, WallMs, vehicleId, cause, peakG, peakQPa, speedMs, altitudeM, body, crewCount);

    internal static VehicleCreatedSignal Created(
        string vehicleId = "v1",
        double simT = 0,
        string vehicleName = "Test Rocket",
        string body = "earth",
        double massKg = 12_000,
        int partCount = 24,
        int crewCount = 2,
        double launchGameTime = 0)
        => new(simT, WallMs, vehicleId, vehicleName, body, massKg, partCount, crewCount, launchGameTime);

    /// <summary>Deterministic install id so <c>kid</c> values are stable across runs.</summary>
    internal const string InstallId = "01J9V5M3E8Z0FAKEINSTALL01";

    /// <summary>Deterministic session id so envelopes are reproducible.</summary>
    internal const string SessionId = "01J9V5M3E8Z0FAKESESSION01";

    /// <summary>Deterministic career id (§4.1) so envelopes are reproducible.</summary>
    internal static readonly string CareerId = Ids.CareerId(InstallId, "save:apollo");

    /// <summary>A second career, for tests that need two saves to be distinguishable.</summary>
    internal static readonly string OtherCareerId = Ids.CareerId(InstallId, "save:gemini");

    internal static EventPipelineOptions PipelineOptions(
        double windowSeconds = Wire.TelemetryWindowSeconds,
        string? sessionId = SessionId,
        string? careerId = null,
        EventTypeFilter? types = null)
        => new(InstallId, "0.1.0", "2026.8.5.5168", sessionId, windowSeconds, careerId ?? CareerId, types);

    internal static EventPipeline Pipeline(
        double windowSeconds = Wire.TelemetryWindowSeconds,
        string? careerId = null,
        EventTypeFilter? types = null)
        => new(PipelineOptions(windowSeconds, careerId: careerId, types: types));
}
