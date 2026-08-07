using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>
/// Pure conversions from detector/window/correlator output to <see cref="EventEnvelope"/>s.
/// Everything stateful lives in <see cref="FlightTracker"/> and <see cref="EventPipeline"/>.
/// </summary>
public static class EventFactory
{
    /// <summary>Wraps a detected polled event.</summary>
    /// <param name="tracker">Supplies the session and flight ULIDs.</param>
    /// <param name="detected">The detector's output.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromDetected(FlightTracker tracker, DetectedEvent detected)
        => EventEnvelope.Create(
            detected.Type,
            tracker.SessionId,
            tracker.FlightFor(detected.VehicleId),
            detected.SimT,
            detected.WallMs,
            detected.Payload);

    /// <summary>Wraps a closed telemetry window.</summary>
    /// <param name="tracker">Supplies the session and flight ULIDs.</param>
    /// <param name="window">The closed window.</param>
    /// <param name="wallMs">Client unix milliseconds at close time.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromWindow(FlightTracker tracker, ClosedWindow window, long wallMs)
        => EventEnvelope.Create(
            EventTypes.TelemetryWindow,
            tracker.SessionId,
            tracker.FlightFor(window.VehicleId),
            window.Payload.T1Sim,
            wallMs,
            window.Payload);

    /// <summary>Wraps an impact whose survival verdict is now known.</summary>
    /// <param name="tracker">Supplies the session and flight ULIDs.</param>
    /// <param name="impact">The resolved impact.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromResolvedImpact(FlightTracker tracker, ResolvedImpact impact)
        => EventEnvelope.Create(
            EventTypes.VehicleImpact,
            tracker.SessionId,
            tracker.FlightFor(impact.Signal.VehicleId),
            impact.Signal.SimT,
            impact.Signal.WallMs,
            new VehicleImpactPayload(
                SpeedMs: impact.Signal.SpeedMs,
                EnergyJ: impact.Signal.EnergyJ,
                Survived: impact.Survived,
                LaunchPad: impact.Signal.LaunchPad,
                Body: impact.Signal.Body,
                CrewCount: impact.Signal.CrewCount));

    /// <summary>Builds a <c>roster.snapshot</c> payload, hashing every roster name into a <c>kid</c>.</summary>
    /// <param name="installId">The install ULID; salts the hash so ids are not comparable across installs.</param>
    /// <param name="kittens">The roster rows.</param>
    /// <returns>The payload.</returns>
    public static RosterSnapshotPayload RosterPayload(string installId, IReadOnlyList<RosterKitten> kittens)
    {
        var rows = new List<RosterKittenPayload>(kittens.Count);
        foreach (RosterKitten k in kittens)
        {
            rows.Add(new RosterKittenPayload(
                Kid: Ids.KittenId(installId, k.Name),
                Name: Ids.SanitizeName(k.Name),
                TravelledM: k.TravelledM,
                FastestMs: k.FastestMs,
                Missions: k.Missions,
                MissionTimeS: k.MissionTimeS,
                Kia: k.Kia));
        }

        return new RosterSnapshotPayload(rows);
    }
}
