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
    /// <param name="tracker">Supplies the career, session and flight ids.</param>
    /// <param name="detected">The detector's output.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromDetected(FlightTracker tracker, DetectedEvent detected)
        => EventEnvelope.Create(
            detected.Type,
            tracker.SessionId,
            tracker.CareerId,
            tracker.FlightFor(detected.VehicleId),
            detected.SimT,
            detected.WallMs,
            detected.Payload);

    /// <summary>Wraps a closed telemetry window.</summary>
    /// <param name="tracker">Supplies the career, session and flight ids.</param>
    /// <param name="window">The closed window.</param>
    /// <param name="wallMs">Client unix milliseconds at close time.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromWindow(FlightTracker tracker, ClosedWindow window, long wallMs)
        => EventEnvelope.Create(
            EventTypes.TelemetryWindow,
            tracker.SessionId,
            tracker.CareerId,
            tracker.FlightFor(window.VehicleId),
            window.Payload.T1Sim,
            wallMs,
            window.Payload);

    /// <summary>
    /// Wraps an impact whose survival verdict is now known, minting a flight for the vehicle if it
    /// does not have one.
    /// </summary>
    /// <param name="tracker">Supplies the career, session and flight ids.</param>
    /// <param name="impact">The resolved impact.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromResolvedImpact(FlightTracker tracker, ResolvedImpact impact)
        => FromResolvedImpact(tracker, impact, tracker.FlightFor(impact.Signal.VehicleId));

    /// <summary>
    /// Wraps a resolved impact onto an explicitly supplied flight — the form callers use when they
    /// must <b>not</b> mint one (<see cref="EventPipeline.Flush"/> resolves impacts after flights
    /// have ended, and a minted flight there would be a phantom with no <c>flight.started</c>).
    /// </summary>
    /// <param name="tracker">Supplies the career id and the session ULID.</param>
    /// <param name="impact">The resolved impact.</param>
    /// <param name="flight">The flight ULID to attribute the impact to.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromResolvedImpact(FlightTracker tracker, ResolvedImpact impact, string flight)
        => EventEnvelope.Create(
            EventTypes.VehicleImpact,
            tracker.SessionId,
            tracker.CareerId,
            flight,
            impact.Signal.SimT,
            impact.Signal.WallMs,
            new VehicleImpactPayload(
                SpeedMs: impact.Signal.SpeedMs,
                EnergyJ: impact.Signal.EnergyJ,
                Survived: impact.Survived,
                LaunchPad: impact.Signal.LaunchPad,
                Body: impact.Signal.Body,
                CrewCount: impact.Signal.CrewCount,
                Lat: impact.Signal.Lat,
                Lon: impact.Signal.Lon));

    /// <summary>
    /// Wraps a touchdown whose survival verdict is now known, onto an explicitly supplied flight.
    /// </summary>
    /// <remarks>
    /// The flight is always passed in, never looked up here: a landing settles either at a frame
    /// boundary — where the vehicle is by definition still in telemetry and its flight open — or as
    /// the flight ends, where minting would produce exactly the phantom
    /// <see cref="EventPipeline.Flush"/>'s peek semantics exist to prevent. The two call sites want
    /// different policies, so neither is baked in here.
    /// </remarks>
    /// <param name="tracker">Supplies the career id and the session ULID.</param>
    /// <param name="landing">The resolved landing.</param>
    /// <param name="flight">The flight ULID to attribute the landing to.</param>
    /// <returns>The envelope.</returns>
    public static EventEnvelope FromResolvedLanding(FlightTracker tracker, ResolvedLanding landing, string flight)
        => EventEnvelope.Create(
            EventTypes.VehicleLanded,
            tracker.SessionId,
            tracker.CareerId,
            flight,
            landing.Landing.SimT,
            landing.Landing.WallMs,
            new VehicleLandedPayload(
                Body: landing.Landing.Body,
                VerticalSpeedMs: landing.Landing.VerticalSpeedMs,
                HorizontalSpeedMs: landing.Landing.HorizontalSpeedMs,
                CrewCount: landing.Landing.CrewCount,
                Survived: landing.Survived,
                RadarAltM: landing.Landing.RadarAltM,
                Lat: landing.Landing.Lat,
                Lon: landing.Landing.Lon));

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
