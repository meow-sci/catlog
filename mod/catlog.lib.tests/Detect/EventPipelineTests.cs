using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §7.5 golden scenarios: an input snapshot/signal sequence produces an exact
/// envelope sequence. This is the test that pins the whole worker-side pipeline together.
/// </summary>
public sealed class EventPipelineTests
{
    [Fact]
    public void SessionStarted_CarriesTheInstallAndNoFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope envelope = pipeline.SessionStarted(0, TestData.WallMs);

        Assert.Equal(EventTypes.SessionStarted, envelope.Type);
        Assert.Null(envelope.Flight);
        Assert.Equal(TestData.SessionId, envelope.Session);
        var payload = Assert.IsType<SessionStartedPayload>(envelope.Payload);
        Assert.Equal(TestData.InstallId, payload.Install);
        Assert.Equal("2026.8.5.5168", payload.GameBuild);
    }

    /// <summary>
    /// The <c>hop-lithobrake</c> shape from §7.3: launch, a hard survivable impact, recovery.
    /// </summary>
    [Fact]
    public void GoldenScenario_HopAndSurvivedLithobrake()
    {
        EventPipeline pipeline = TestData.Pipeline();
        var produced = new List<EventEnvelope>();

        produced.Add(pipeline.SessionStarted(0, TestData.WallMs));
        produced.AddRange(pipeline.ProcessSignal(TestData.Created(simT: 0, crewCount: 2)));

        // Liftoff and a climb, sampled at 2 Hz.
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(1,
            TestData.Snapshot(simT: 0, situation: "landed", altitudeM: 0))));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(2,
            TestData.Snapshot(simT: 4, situation: "freefall", altitudeM: 4_000, surfaceSpeedMs: 300))));

        // Impact at 62 m/s, then two frame boundaries so the correlator's one-frame hold expires.
        produced.AddRange(pipeline.ProcessSignal(TestData.Impact(simT: 30, speedMs: 62, crewCount: 2)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30, TestData.WallMs, 3)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30.1, TestData.WallMs, 4)));

        produced.AddRange(pipeline.ProcessSignal(
            new VehicleRecoveredSignal(31, TestData.WallMs, "v1", 2)));

        Assert.Equal(
            [
                EventTypes.SessionStarted,
                EventTypes.FlightStarted,
                EventTypes.VehicleSituation,
                EventTypes.VehicleImpact,
                EventTypes.TelemetryWindow, // the partial window is flushed before flight.ended
                EventTypes.FlightEnded,
            ],
            produced.Select(static e => e.Type).ToArray());

        var impact = Assert.IsType<VehicleImpactPayload>(
            produced.Single(static e => e.Type == EventTypes.VehicleImpact).Payload);
        Assert.True(impact.Survived, "no destruction followed the impact");
        Assert.Equal(62, impact.SpeedMs);
        Assert.Equal(2, impact.CrewCount);
        Assert.False(impact.LaunchPad);

        var ended = Assert.IsType<FlightEndedPayload>(
            produced.Single(static e => e.Type == EventTypes.FlightEnded).Payload);
        Assert.Equal("recovered", ended.Reason);

        // Every event for this vehicle carries one and the same flight id.
        string[] flights = produced
            .Where(static e => e.Flight is not null)
            .Select(static e => e.Flight!)
            .Distinct()
            .ToArray();
        Assert.Single(flights);
        Assert.True(Ids.IsUlid(flights[0]));
    }

    /// <summary>The <c>rud-sampler</c> shape: an impact that was immediately fatal.</summary>
    [Fact]
    public void GoldenScenario_ImpactFollowedByRudDoesNotSurvive()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created());

        var produced = new List<EventEnvelope>();
        produced.AddRange(pipeline.ProcessSignal(TestData.Impact(simT: 30, speedMs: 210)));
        produced.AddRange(pipeline.ProcessSignal(TestData.Rud(simT: 30, cause: RudCause.GroundImpact)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30, TestData.WallMs, 1)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30.1, TestData.WallMs, 2)));
        produced.AddRange(pipeline.ProcessSignal(
            new VehicleRemovedSignal(30.2, TestData.WallMs, "v1", FlightEndReason.Destroyed, 2)));

        Assert.Equal(
            [EventTypes.VehicleRud, EventTypes.VehicleImpact, EventTypes.FlightEnded],
            produced.Select(static e => e.Type).ToArray());

        var impact = Assert.IsType<VehicleImpactPayload>(
            produced.Single(static e => e.Type == EventTypes.VehicleImpact).Payload);
        Assert.False(impact.Survived, "the vehicle was destroyed in the same frame as the impact");

        var rud = Assert.IsType<VehicleRudPayload>(
            produced.Single(static e => e.Type == EventTypes.VehicleRud).Payload);
        Assert.Equal("ground_impact", rud.Cause);
        Assert.Equal(2, rud.CrewCount);
    }

    [Fact]
    public void TelemetryWindows_AreEmittedOncePerWindowPerVehicle()
    {
        EventPipeline pipeline = TestData.Pipeline(windowSeconds: 10.0);
        var produced = new List<EventEnvelope>();

        for (int i = 0; i <= 20; i++)
        {
            produced.AddRange(pipeline.ProcessFrame(TestData.Frame(i + 1,
                TestData.Snapshot(simT: i, altitudeM: 1_000 + i))));
        }

        EventEnvelope[] windows = produced.Where(static e => e.Type == EventTypes.TelemetryWindow).ToArray();

        Assert.Equal(2, windows.Length);
        var first = Assert.IsType<TelemetryWindowPayload>(windows[0].Payload);
        Assert.Equal(0, first.T0Sim);
        Assert.Equal(9, first.T1Sim);
        Assert.Equal(10, first.N);
        Assert.Equal(new Agg(1_000, 1_009, 1_004.5, 1_009), first.AltM);
    }

    [Fact]
    public void Flags_AreEmittedOncePerFlightAndTaintLaterFlights()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "v1"));

        EventEnvelope flagged = Assert.Single(pipeline.ProcessSignal(
            new FlaggedSignal(5, TestData.WallMs, "v1", FlightFlag.Teleport, "Vehicle.Teleport")));
        var payload = Assert.IsType<FlightFlaggedPayload>(flagged.Payload);
        Assert.Equal("teleport", payload.Flag);
        Assert.Equal("Vehicle.Teleport", payload.Detail);

        Assert.Empty(pipeline.ProcessSignal(
            new FlaggedSignal(6, TestData.WallMs, "v1", FlightFlag.Teleport, "again")));
    }

    /// <summary>
    /// A session-wide flag (live tumble-gate tuning) has no vehicle. It must taint every open
    /// flight and every flight started afterwards, or the tumble board is forgeable.
    /// </summary>
    [Fact]
    public void SessionWideFlag_TaintsOpenAndFutureFlights()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "v1"));

        IReadOnlyList<EventEnvelope> now = pipeline.ProcessSignal(
            new FlaggedSignal(5, TestData.WallMs, null, FlightFlag.Tuning, "TumbleSpeedGate=2.0"));
        EventEnvelope flagged = Assert.Single(now);
        Assert.Equal("tuning", ((FlightFlaggedPayload)flagged.Payload).Flag);

        IReadOnlyList<EventEnvelope> later = pipeline.ProcessSignal(
            TestData.Created(vehicleId: "v2", launchGameTime: 50));

        Assert.Equal(
            [EventTypes.FlightStarted, EventTypes.FlightFlagged],
            later.Select(static e => e.Type).ToArray());
    }

    [Fact]
    public void KittenEvents_HashTheRosterNameIntoAStableKid()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope tumble = Assert.Single(pipeline.ProcessSignal(
            new TumbleSignal(10, TestData.WallMs, "Whiskers", 7.2, "earth")));
        var payload = Assert.IsType<KittenTumblePayload>(tumble.Payload);

        Assert.Equal(Ids.KittenId(TestData.InstallId, "Whiskers"), payload.Kid);
        Assert.Equal("Whiskers", payload.Name);
        Assert.Equal(16, payload.Kid.Length);
        Assert.Null(tumble.Flight);
    }

    [Fact]
    public void RosterSnapshot_HashesEveryName()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope roster = Assert.Single(pipeline.ProcessSignal(new RosterSampleSignal(
            600, TestData.WallMs,
            [
                new RosterKitten("Whiskers", 1_200, 30_000, 3, 900, false),
                new RosterKitten("Mittens", 0, 0, 0, 0, true),
            ])));

        var payload = Assert.IsType<RosterSnapshotPayload>(roster.Payload);
        Assert.Equal(2, payload.Kittens.Count);
        Assert.Equal(Ids.KittenId(TestData.InstallId, "Mittens"), payload.Kittens[1].Kid);
        Assert.True(payload.Kittens[1].Kia);
        Assert.Null(roster.Flight);
    }

    [Fact]
    public void DockAndUndock_ResolveTheOtherVehiclesFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "station", launchGameTime: 0));
        pipeline.ProcessSignal(TestData.Created(vehicleId: "capsule", launchGameTime: 10));
        string stationFlight = pipeline.Tracker.PeekFlight("station")!;

        EventEnvelope docked = Assert.Single(pipeline.ProcessSignal(
            new DockSignal(100, TestData.WallMs, "capsule", "station")));

        Assert.Equal(EventTypes.VehicleDocked, docked.Type);
        Assert.Equal(stationFlight, ((VehicleDockPayload)docked.Payload).OtherFlight);
    }

    [Fact]
    public void DockWithAnUnknownPartner_ReportsANullOtherFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "capsule"));

        EventEnvelope docked = Assert.Single(pipeline.ProcessSignal(
            new UndockSignal(100, TestData.WallMs, "capsule", "debris")));

        Assert.Null(((VehicleDockPayload)docked.Payload).OtherFlight);
    }

    [Theory]
    [InlineData(EngineEventKind.Ignition, EventTypes.EngineIgnition)]
    [InlineData(EngineEventKind.Shutdown, EventTypes.EngineShutdown)]
    [InlineData(EngineEventKind.Flameout, EventTypes.EngineFlameout)]
    public void EngineSignals_MapToTheirEventTypes(EngineEventKind kind, string expectedType)
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope envelope = Assert.Single(pipeline.ProcessSignal(
            new EngineSignal(5, TestData.WallMs, "v1", kind, "Merlin", 9)));

        Assert.Equal(expectedType, envelope.Type);
        var payload = Assert.IsType<EnginePayload>(envelope.Payload);
        Assert.Equal("Merlin", payload.Engine);
        Assert.Equal(9, payload.Count);
    }

    /// <summary>
    /// A save load is a teardown-and-rebuild boundary: new session id, and no detector state is
    /// carried across it.
    /// </summary>
    [Fact]
    public void SessionLoaded_RotatesTheSessionAndRebaselines()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created());
        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 100, situation: "landed")));
        string oldSession = pipeline.SessionId;

        EventEnvelope started = Assert.Single(pipeline.ProcessSignal(
            new SessionLoadedSignal(0, TestData.WallMs, "2026.8.5.5168", "0.1.0")));

        Assert.Equal(EventTypes.SessionStarted, started.Type);
        Assert.NotEqual(oldSession, pipeline.SessionId);
        Assert.Equal(pipeline.SessionId, started.Session);

        // Post-load samples are a fresh baseline, not a diff against the pre-load world.
        Assert.Empty(pipeline.ProcessFrame(TestData.Frame(2, TestData.Snapshot(simT: 0, situation: "freefall"))));
    }

    [Fact]
    public void Flush_ClosesPartialWindowsAndOutstandingImpacts()
    {
        EventPipeline pipeline = TestData.Pipeline(windowSeconds: 30.0);
        pipeline.ProcessSignal(TestData.Created());
        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 0)));
        pipeline.ProcessSignal(TestData.Impact(simT: 1));

        IReadOnlyList<EventEnvelope> flushed = pipeline.Flush(TestData.WallMs);

        Assert.Equal(
            [EventTypes.VehicleImpact, EventTypes.TelemetryWindow],
            flushed.Select(static e => e.Type).ToArray());
    }

    [Fact]
    public void UnknownSignalSubtype_IsIgnoredRatherThanThrowing()
    {
        EventPipeline pipeline = TestData.Pipeline();

        Assert.Empty(pipeline.ProcessSignal(new UnknownSignal(1, TestData.WallMs)));
    }

    /// <summary>Every envelope must serialize to one parseable NDJSON line with the §4.1 keys.</summary>
    [Fact]
    public void EveryEmittedEnvelope_SerializesToValidNdjson()
    {
        EventPipeline pipeline = TestData.Pipeline(windowSeconds: 5.0);
        var produced = new List<EventEnvelope> { pipeline.SessionStarted(0, TestData.WallMs) };
        produced.AddRange(pipeline.ProcessSignal(TestData.Created()));
        produced.AddRange(pipeline.ProcessSignal(TestData.Rud()));
        produced.AddRange(pipeline.ProcessSignal(new TumbleSignal(1, TestData.WallMs, "Whiskers", 7, "earth")));
        for (int i = 0; i <= 12; i++)
            produced.AddRange(pipeline.ProcessFrame(TestData.Frame(i + 1, TestData.Snapshot(simT: i))));

        Assert.NotEmpty(produced);
        foreach (EventEnvelope envelope in produced)
        {
            string line = envelope.ToNdjsonLine();
            Assert.DoesNotContain('\n', line);
            Assert.DoesNotContain("NaN", line);
            Assert.DoesNotContain("Infinity", line);

            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement root = document.RootElement;
            Assert.True(Ids.IsUlid(root.GetProperty("id").GetString()), "id must be a ULID");
            Assert.True(EventTypes.IsKnown(root.GetProperty("type").GetString()),
                $"'{root.GetProperty("type").GetString()}' must be in the launch-set registry");
            Assert.Equal(1, root.GetProperty("ver").GetInt32());
            Assert.True(root.TryGetProperty("flight", out _), "the flight key is always present, even when null");
            Assert.Equal(JsonValueKind.Object, root.GetProperty("payload").ValueKind);
        }
    }

    /// <summary>
    /// A manual destroy produces no <c>vehicle.rud</c> — only a <c>flight.ended</c> with reason
    /// <c>destroyed</c>, from the game's input-apply pass. It must still flip <c>survived</c>, or
    /// scuttling after every hard landing banks a free "survived" record.
    /// </summary>
    [Fact]
    public void ManualDestroyAfterAnImpact_StillMarksItNotSurvived()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));

        string flight = pipeline.Tracker.PeekFlight("v1")!;

        pipeline.ProcessSignal(TestData.Impact(simT: 10, speedMs: 55));
        // No RudSignal: this is the player hitting Abandon, which lands later in the same frame.
        IReadOnlyList<EventEnvelope> ended =
            pipeline.ProcessSignal(new VehicleRemovedSignal(10, TestData.WallMs, "v1", FlightEndReason.Destroyed, 1));

        EventEnvelope impact = Assert.Single(ended, static e => e.Type == EventTypes.VehicleImpact);
        Assert.False(Assert.IsType<VehicleImpactPayload>(impact.Payload).Survived,
            "a scuttled vehicle did not survive the impact that preceded the scuttle");

        // Resolved against the flight that is ending, not a re-minted phantom.
        Assert.Equal(flight, impact.Flight);
        Assert.Equal(flight, Assert.Single(ended, static e => e.Type == EventTypes.FlightEnded).Flight);

        // And nothing is left for a later boundary to resolve a second time.
        Assert.Empty(pipeline.ProcessSignal(new FrameBoundarySignal(10.1, TestData.WallMs, 1)));
    }

    [Fact]
    public void RecoveryAfterAnImpact_LeavesItSurvived()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));
        string flight = pipeline.Tracker.PeekFlight("v1")!;

        pipeline.ProcessSignal(TestData.Impact(simT: 10, speedMs: 55));
        IReadOnlyList<EventEnvelope> ended =
            pipeline.ProcessSignal(new VehicleRecoveredSignal(10, TestData.WallMs, "v1", 1));

        EventEnvelope impact = Assert.Single(ended, static e => e.Type == EventTypes.VehicleImpact);
        Assert.True(Assert.IsType<VehicleImpactPayload>(impact.Payload).Survived);
        Assert.Equal(flight, impact.Flight);
    }

    // ----- Flush: peek semantics for the correlator drain (WP8 lib fix) --------------------

    /// <summary>
    /// An impact still outstanding when the flight has already ended must be dropped, not attached
    /// to a freshly minted flight ULID that has no <c>flight.started</c> to join against.
    /// </summary>
    [Fact]
    public void Flush_DropsAnImpactWhoseFlightAlreadyEnded()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));

        // The impact lands in the last frame the game ever runs; the vehicle is removed in the same
        // frame, so the correlator's one-frame hold never expires before the session flushes.
        pipeline.ProcessSignal(TestData.Impact(simT: 10, speedMs: 40));
        pipeline.ProcessSignal(new VehicleRemovedSignal(10, TestData.WallMs, "v1", FlightEndReason.Destroyed, 0));

        IReadOnlyList<EventEnvelope> flushed = pipeline.Flush(TestData.WallMs);

        Assert.DoesNotContain(flushed, static e => e.Type == EventTypes.VehicleImpact);
        Assert.Empty(pipeline.Tracker.ActiveVehicleIds);
    }

    /// <summary>The same drain still emits for a vehicle whose flight is open — peeking is not "drop everything".</summary>
    [Fact]
    public void Flush_StillEmitsAnImpactForALiveFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));
        string flight = pipeline.Tracker.PeekFlight("v1")!;

        pipeline.ProcessSignal(TestData.Impact(simT: 10, speedMs: 40));

        EventEnvelope impact = Assert.Single(
            pipeline.Flush(TestData.WallMs), static e => e.Type == EventTypes.VehicleImpact);
        Assert.Equal(flight, impact.Flight);
        Assert.True(Assert.IsType<VehicleImpactPayload>(impact.Payload).Survived);
    }

    private sealed record UnknownSignal(double SimT, long WallMs) : GameSignal(SimT, WallMs);
}
