using System;
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
    }

    /// <summary>
    /// A tumbling kitten is a KittenEva, and a KittenEva's vehicle id IS her roster name. The
    /// tumble has to name that flight: <c>tuning</c> flags the flight, and the server can only
    /// exclude events that name one, so a flightless tumble scores however far the gate was moved.
    /// </summary>
    [Fact]
    public void Tumble_IsAttributedToTheKittensEvaFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();
        EventEnvelope started = Assert.Single(pipeline.ProcessSignal(
            TestData.Created(vehicleId: "Whiskers", vehicleName: "Whiskers", crewCount: 1)));

        EventEnvelope tumble = Assert.Single(pipeline.ProcessSignal(
            new TumbleSignal(10, TestData.WallMs, "Whiskers", 7.2, "earth")));

        Assert.NotNull(tumble.Flight);
        Assert.Equal(started.Flight, tumble.Flight);
    }

    /// <summary>
    /// The end-to-end shape the <c>tuning</c> flag needs: the flag is session-wide and lands on the
    /// EVA flight, and the tumbles that follow name that same flight — which is the join the
    /// server's exclusion is made of.
    /// </summary>
    [Fact]
    public void Tumble_NamesTheFlightTheTuningFlagWasRaisedOn()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "Whiskers", vehicleName: "Whiskers", crewCount: 1));

        EventEnvelope flagged = Assert.Single(pipeline.ProcessSignal(
            new FlaggedSignal(5, TestData.WallMs, null, FlightFlag.Tuning, "TumbleSpeedGate=0.5")));
        EventEnvelope tumble = Assert.Single(pipeline.ProcessSignal(
            new TumbleSignal(6, TestData.WallMs, "Whiskers", 0.7, "earth")));

        Assert.Equal(EventTypes.FlightFlagged, flagged.Type);
        Assert.Equal(flagged.Flight, tumble.Flight);
    }

    /// <summary>
    /// No open flight for the tumbling kitten means no flight on the event — the tumble is peeked,
    /// not minted, because a minted flight would have no <c>flight.started</c> for the server to
    /// join against and would put a phantom on the board.
    /// </summary>
    [Fact]
    public void Tumble_WithNoOpenFlight_StaysNullRatherThanMintingAPhantom()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope tumble = Assert.Single(pipeline.ProcessSignal(
            new TumbleSignal(10, TestData.WallMs, "Whiskers", 7.2, "earth")));

        Assert.Null(tumble.Flight);
        Assert.Empty(pipeline.Tracker.ActiveVehicleIds);
    }

    /// <summary>
    /// The real KIA shape: <c>KillCrew</c> reads the seats while the flight is still open, the
    /// vehicle is disposed in the same frame, and the roster diff notices the death a tick later —
    /// by which time the flight has ended. The event must still name it, or the ±2 s window that
    /// disqualifies a fatal crash from the impact boards has nothing to match on.
    /// </summary>
    [Fact]
    public void Kia_IsAttributedToTheFlightWhoseCrewWasKilled()
    {
        EventPipeline pipeline = TestData.Pipeline();
        EventEnvelope started = Assert.Single(pipeline.ProcessSignal(TestData.Created(crewCount: 2)));

        Assert.Empty(pipeline.ProcessSignal(
            new CrewKilledSignal(30, TestData.WallMs, "v1", ["Whiskers", "Mittens"])));
        pipeline.ProcessSignal(new VehicleRemovedSignal(30, TestData.WallMs, "v1", FlightEndReason.Destroyed, 2));

        EventEnvelope kia = Assert.Single(pipeline.ProcessSignal(
            new KiaSignal(30.5, TestData.WallMs, "Whiskers", KiaContext.ManualDestroy)));

        Assert.Equal(EventTypes.KittenKia, kia.Type);
        Assert.Equal(started.Flight, kia.Flight);
        Assert.Equal("manual_destroy", ((KittenKiaPayload)kia.Payload).Context);
    }

    /// <summary>
    /// A kitten who died outside is attributable too: her EVA vehicle is a flight of her own, and
    /// only the <c>eva_start</c>/<c>eva_end</c> pair proves an id belongs to a KittenEva rather
    /// than to a rocket a player named after her.
    /// </summary>
    [Fact]
    public void Kia_ForAKittenOnEva_UsesHerEvaFlight()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(vehicleId: "Whiskers", vehicleName: "Whiskers", crewCount: 1));
        EventEnvelope eva = Assert.Single(pipeline.ProcessSignal(
            new EvaStartSignal(5, TestData.WallMs, "Whiskers", "Whiskers")));

        EventEnvelope kia = Assert.Single(pipeline.ProcessSignal(
            new KiaSignal(9, TestData.WallMs, "Whiskers", KiaContext.Unknown)));

        Assert.Equal(eva.Flight, kia.Flight);
        Assert.NotNull(kia.Flight);
    }

    /// <summary>
    /// Everything the mod cannot prove stays null. A death with no crew kill behind it, one whose
    /// crew kill named somebody else, and one that arrived too late to belong to it are all
    /// unattributable — and a guess there would void an innocent flight's impact record, which is
    /// the one failure the ±2 s window must never produce.
    /// </summary>
    [Theory]
    [InlineData("Whiskers", false, 30.5)] // no crew kill at all
    [InlineData("Mittens", true, 30.5)]   // the crew kill named another kitten
    [InlineData("Whiskers", true, 40.0)]  // long past the window
    public void Kia_WithNothingToProveAFlight_IsNull(string name, bool crewKilled, double kiaSimT)
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(crewCount: 2));
        if (crewKilled)
        {
            pipeline.ProcessSignal(new CrewKilledSignal(30, TestData.WallMs, "v1", ["Whiskers"]));
            pipeline.ProcessSignal(
                new VehicleRemovedSignal(30, TestData.WallMs, "v1", FlightEndReason.Destroyed, 2));
        }

        EventEnvelope kia = Assert.Single(pipeline.ProcessSignal(
            new KiaSignal(kiaSimT, TestData.WallMs, name, KiaContext.ManualDestroy)));

        Assert.Null(kia.Flight);
    }

    /// <summary>
    /// A crew kill on a vehicle catlog never saw start is remembered as nothing: naming a flight
    /// the server has no <c>flight.started</c> for is the phantom hazard again, one step removed.
    /// </summary>
    [Fact]
    public void Kia_FromACrewKillOnAnUnknownVehicle_IsNull()
    {
        EventPipeline pipeline = TestData.Pipeline();

        pipeline.ProcessSignal(new CrewKilledSignal(30, TestData.WallMs, "ghost", ["Whiskers"]));
        EventEnvelope kia = Assert.Single(pipeline.ProcessSignal(
            new KiaSignal(30.5, TestData.WallMs, "Whiskers", KiaContext.ManualDestroy)));

        Assert.Null(kia.Flight);
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

        EventEnvelope started = pipeline.ProcessSignal(
            new SessionLoadedSignal(0, TestData.WallMs, "2026.8.5.5168", "0.1.0", System: TestData.SystemSurvey()))
            .Single(static e => e.Type == EventTypes.SessionStarted);

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
            Assert.Equal(
                EventTypes.VersionOf(root.GetProperty("type").GetString()!),
                root.GetProperty("ver").GetInt32());
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

    // ----- [events]: a disabled type is subtracted and nothing else moves ------------------

    /// <summary>
    /// The whole promise of the <c>[events]</c> table, stated as a diff: switching a type off
    /// removes exactly those events from the golden sequence and leaves every remaining envelope
    /// byte for byte what it was. Suppression happens at <c>Add</c>, after every detector, tracker,
    /// correlator and window has already advanced, so it cannot rewind anything.
    /// </summary>
    [Theory]
    [InlineData("telemetry.window")]
    [InlineData("vehicle.impact")]
    [InlineData("vehicle.situation")]
    public void GoldenScenario_DisablingATypeSubtractsOnlyThatType(string disabled)
    {
        string[] baseline = Canonical(HopAndSurvivedLithobrake(EventTypeFilter.All));
        string[] filtered = Canonical(HopAndSurvivedLithobrake(EventTypeFilter.Create([disabled])));

        Assert.Contains(baseline, line => line.Contains($"\"type\":\"{disabled}\"", StringComparison.Ordinal));
        Assert.Equal(
            baseline.Where(line => !line.Contains($"\"type\":\"{disabled}\"", StringComparison.Ordinal)).ToArray(),
            filtered);
    }

    /// <summary>
    /// The state-preservation half, made concrete. <c>vehicle.rud</c> is what tells the correlator
    /// the vehicle did not survive; with it switched off the RUD event is gone but the impact still
    /// reports <c>survived: false</c>, because the correlator was told before <c>Add</c> ever ran.
    /// A filter placed in <c>Dispatch</c> would have skipped the case body and quietly turned a
    /// fatal crash into a survived one — which is the whole reason the filter is not there.
    /// </summary>
    [Fact]
    public void DisablingVehicleRud_DoesNotMakeAFatalImpactLookSurvived()
    {
        EventPipeline pipeline = TestData.Pipeline(types: EventTypeFilter.Create([EventTypes.VehicleRud]));
        pipeline.ProcessSignal(TestData.Created());

        var produced = new List<EventEnvelope>();
        produced.AddRange(pipeline.ProcessSignal(TestData.Impact(simT: 30, speedMs: 210)));
        produced.AddRange(pipeline.ProcessSignal(TestData.Rud(simT: 30, cause: RudCause.GroundImpact)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30, TestData.WallMs, 1)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30.1, TestData.WallMs, 2)));
        produced.AddRange(pipeline.ProcessSignal(
            new VehicleRemovedSignal(30.2, TestData.WallMs, "v1", FlightEndReason.Destroyed, 2)));

        Assert.Equal(
            [EventTypes.VehicleImpact, EventTypes.FlightEnded],
            produced.Select(static e => e.Type).ToArray());

        var impact = Assert.IsType<VehicleImpactPayload>(
            produced.Single(static e => e.Type == EventTypes.VehicleImpact).Payload);
        Assert.False(impact.Survived, "the correlator was told about the destruction before Add ran");
    }

    /// <summary>
    /// A disabled type is not a disabled <i>detector</i>: the flag dedup, the window fold and the
    /// flight bookkeeping all still run, so the events that remain are unchanged. Here the flag is
    /// still recorded on the tracker even though nothing could switch <c>flight.flagged</c> off —
    /// what is switched off is <c>flight.started</c>'s neighbour, <c>vehicle.staging</c>.
    /// </summary>
    [Fact]
    public void DisablingATypeLeavesTheFlightIdentityIntact()
    {
        EventPipeline pipeline = TestData.Pipeline(types: EventTypeFilter.Create([EventTypes.VehicleStaging]));

        IReadOnlyList<EventEnvelope> created = pipeline.ProcessSignal(TestData.Created(vehicleId: "v1"));
        string flight = Assert.Single(created).Flight!;

        Assert.Empty(pipeline.ProcessSignal(new StagingSignal(5, TestData.WallMs, "v1", 2)));

        EventEnvelope ended = Assert.Single(pipeline.ProcessSignal(
            new VehicleRecoveredSignal(10, TestData.WallMs, "v1", 2)));
        Assert.Equal(EventTypes.FlightEnded, ended.Type);
        Assert.Equal(flight, ended.Flight);
    }

    // ----- the landing edge -------------------------------------------------------------------

    /// <summary>
    /// The golden landing shape: one contact-free → contact transition produces
    /// <c>vehicle.situation</c> immediately and <c>vehicle.landed</c> once the correlator's hold
    /// has expired without a destruction. Two events, one detection, in that order.
    /// </summary>
    [Fact]
    public void GoldenScenario_TouchdownEmitsSituationThenLanding()
    {
        EventPipeline pipeline = TestData.Pipeline();
        var produced = new List<EventEnvelope>();

        produced.AddRange(pipeline.ProcessSignal(TestData.Created(simT: 0, crewCount: 2)));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(1,
            TestData.Snapshot(simT: 0, situation: "freefall", altitudeM: 900))));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(2,
            TestData.Snapshot(simT: 5, situation: "landed", altitudeM: 12, radarAltM: 0.3,
                verticalSpeedMs: 1.8, horizontalSpeedMs: 0.2, crewCount: 2, lat: 28.6, lon: -80.6))));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(5, TestData.WallMs, 1)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(5.1, TestData.WallMs, 2)));

        Assert.Equal(
            [EventTypes.FlightStarted, EventTypes.VehicleSituation, EventTypes.VehicleLanded],
            produced.Select(static e => e.Type).ToArray());

        var landed = Assert.IsType<VehicleLandedPayload>(
            produced.Single(static e => e.Type == EventTypes.VehicleLanded).Payload);
        Assert.True(landed.Survived, "nothing destroyed the vehicle after it touched down");
        Assert.Equal("earth", landed.Body);
        Assert.Equal(1.8, landed.VerticalSpeedMs);
        Assert.Equal(0.2, landed.HorizontalSpeedMs);
        Assert.Equal(2, landed.CrewCount);
        Assert.Equal(0.3, landed.RadarAltM);
        Assert.Equal(28.6, landed.Lat);
        Assert.Equal(-80.6, landed.Lon);

        // One flight, and the landing is on it rather than on a freshly minted phantom.
        Assert.Single(produced.Select(static e => e.Flight).Distinct());
    }

    /// <summary>
    /// The whole reason <c>survived</c> is routed through <see cref="ImpactCorrelator"/> rather
    /// than inferred: a player who tips over on touchdown and immediately scuttles must not bank
    /// the landing. The verdict is settled as the flight ends, against that flight.
    /// </summary>
    [Fact]
    public void Landing_FollowedByAScuttle_DoesNotSurvive()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));
        string flight = pipeline.Tracker.PeekFlight("v1")!;

        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 0, situation: "freefall")));
        pipeline.ProcessFrame(TestData.Frame(2, TestData.Snapshot(simT: 5, situation: "landed")));

        // No RudSignal: this is the player hitting Abandon, which the game applies in its input pass.
        IReadOnlyList<EventEnvelope> ended = pipeline.ProcessSignal(
            new VehicleRemovedSignal(5.1, TestData.WallMs, "v1", FlightEndReason.Destroyed, 1));

        EventEnvelope landed = Assert.Single(ended, static e => e.Type == EventTypes.VehicleLanded);
        Assert.False(Assert.IsType<VehicleLandedPayload>(landed.Payload).Survived);
        Assert.Equal(flight, landed.Flight);

        // Nothing is left for a later boundary to resolve a second time.
        Assert.Empty(pipeline.ProcessSignal(new FrameBoundarySignal(5.2, TestData.WallMs, 1)));
    }

    /// <summary>
    /// Same peek semantics as an outstanding impact: a landing whose flight has already ended when
    /// the session flushes is dropped rather than minted onto a phantom flight.
    /// </summary>
    [Fact]
    public void Flush_DropsALandingWhoseFlightAlreadyEnded()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));
        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 0, situation: "freefall")));

        // The touchdown is detected in the last frame the game ever runs, and the vehicle is gone
        // before the hold expires.
        pipeline.ProcessFrame(TestData.Frame(2, TestData.Snapshot(simT: 5, situation: "landed")));
        pipeline.ProcessSignal(new VehicleRemovedSignal(5, TestData.WallMs, "v1", FlightEndReason.Despawned, 0));

        Assert.DoesNotContain(pipeline.Flush(TestData.WallMs), static e => e.Type == EventTypes.VehicleLanded);
        Assert.Empty(pipeline.Tracker.ActiveVehicleIds);
    }

    // ----- crew identity, body and position on the flight events ------------------------------

    [Fact]
    public void FlightStarted_CarriesTheCrewAsKidsPlusStagesAndPosition()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope started = Assert.Single(pipeline.ProcessSignal(TestData.Created(
            crewCount: 2, kittenNames: ["Whiskers", "Mittens"], stageCount: 3, lat: 28.6, lon: -80.6)));

        var payload = Assert.IsType<FlightStartedPayload>(started.Payload);
        Assert.Equal(
            [Ids.KittenId(TestData.InstallId, "Whiskers"), Ids.KittenId(TestData.InstallId, "Mittens")],
            payload.Kids);
        Assert.Equal(3, payload.StageCount);
        Assert.Equal(28.6, payload.Lat);
        Assert.Equal(-80.6, payload.Lon);
    }

    /// <summary>An uncrewed flight says so with an empty array, never a null and never a missing key.</summary>
    [Fact]
    public void FlightStarted_WithNoCrew_CarriesAnEmptyKidsArray()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope started = Assert.Single(pipeline.ProcessSignal(TestData.Created(crewCount: 0)));

        Assert.Empty(Assert.IsType<FlightStartedPayload>(started.Payload).Kids);

        using JsonDocument document = JsonDocument.Parse(started.ToNdjsonLine());
        JsonElement kids = document.RootElement.GetProperty("payload").GetProperty("kids");
        Assert.Equal(JsonValueKind.Array, kids.ValueKind);
        Assert.Equal(0, kids.GetArrayLength());
    }

    [Fact]
    public void FlightEnded_CarriesTheCrewBodyAndPosition()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(crewCount: 1));

        EventEnvelope ended = Assert.Single(pipeline.ProcessSignal(new VehicleRecoveredSignal(
            100, TestData.WallMs, "v1", 1, "moon", ["Whiskers"], 0.67, -23.47)));

        var payload = Assert.IsType<FlightEndedPayload>(ended.Payload);
        Assert.Equal("recovered", payload.Reason);
        Assert.Equal("moon", payload.Body);
        Assert.Equal([Ids.KittenId(TestData.InstallId, "Whiskers")], payload.Kids);
        Assert.Equal(0.67, payload.Lat);
        Assert.Equal(-23.47, payload.Lon);
    }

    /// <summary>
    /// The one producer with no vehicle left to read — the poll's silent-removal net — says
    /// <c>unknown</c> and omits the position, rather than inventing a body it cannot see.
    /// </summary>
    [Fact]
    public void FlightEnded_WithNothingLeftToRead_SaysUnknownAndOmitsThePosition()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created());

        EventEnvelope ended = Assert.Single(pipeline.ProcessSignal(
            new VehicleRemovedSignal(50, TestData.WallMs, "v1", FlightEndReason.Despawned, 0)));

        var payload = Assert.IsType<FlightEndedPayload>(ended.Payload);
        Assert.Equal("unknown", payload.Body);
        Assert.Empty(payload.Kids);

        using JsonDocument document = JsonDocument.Parse(ended.ToNdjsonLine());
        JsonElement body = document.RootElement.GetProperty("payload");
        Assert.False(body.TryGetProperty("lat", out _));
        Assert.False(body.TryGetProperty("lon", out _));
    }

    [Fact]
    public void OrbitAchieved_CarriesTheMassAtTheMilestone()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created());
        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 0, peAltM: 0, massKg: 400_000,
            orbitClass: OrbitClass.Bound)));

        EventEnvelope orbit = Assert.Single(
            pipeline.ProcessFrame(TestData.Frame(2, TestData.Snapshot(simT: 300, peAltM: 200_000,
                massKg: 22_500, orbitClass: OrbitClass.Bound))),
            static e => e.Type == EventTypes.VehicleOrbit);

        Assert.Equal(22_500, Assert.IsType<VehicleOrbitPayload>(orbit.Payload).MassKg);
    }

    // ----- absent means absent, on the wire ---------------------------------------------------

    /// <summary>
    /// The rule the whole optional-field design exists for: a position that could not be read is
    /// <b>missing from the JSON</b>, not <c>null</c> and not <c>0</c>. Latitude 0 is the equator and
    /// radar altitude 0 is the ground; either one emitted as a stand-in for "we do not know" is a
    /// wrong record rather than a missing one.
    /// </summary>
    [Fact]
    public void UnreadableOptionalKeys_AreAbsentFromTheJsonEntirely()
    {
        EventPipeline pipeline = TestData.Pipeline();
        var produced = new List<EventEnvelope>();

        produced.AddRange(pipeline.ProcessSignal(TestData.Created(simT: 0)));
        produced.AddRange(pipeline.ProcessSignal(TestData.Rud(simT: 1)));
        produced.AddRange(pipeline.ProcessSignal(TestData.Impact(simT: 2)));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 3, situation: "freefall"))));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(2, TestData.Snapshot(simT: 6, situation: "landed"))));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(6, TestData.WallMs, 1)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(6.1, TestData.WallMs, 2)));

        // Every type that can carry a position is represented above.
        Assert.Contains(produced, static e => e.Type == EventTypes.VehicleLanded);
        Assert.Contains(produced, static e => e.Type == EventTypes.VehicleImpact);

        foreach (EventEnvelope envelope in produced)
        {
            string line = envelope.ToNdjsonLine();
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement payload = document.RootElement.GetProperty("payload");

            Assert.False(payload.TryGetProperty("lat", out _), $"{envelope.Type} must omit lat, not zero it");
            Assert.False(payload.TryGetProperty("lon", out _), $"{envelope.Type} must omit lon, not zero it");
            Assert.False(payload.TryGetProperty("radar_alt_m", out _),
                $"{envelope.Type} must omit radar_alt_m, not zero it");
            Assert.DoesNotContain("null", line, StringComparison.Ordinal);
        }
    }

    /// <summary>And the same keys are present, with their values, the moment the game can read them.</summary>
    [Fact]
    public void ReadableOptionalKeys_ArePresentInTheJson()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));
        pipeline.ProcessFrame(TestData.Frame(1, TestData.Snapshot(simT: 0, situation: "freefall")));

        IReadOnlyList<EventEnvelope> onTouchdown = pipeline.ProcessFrame(TestData.Frame(2,
            TestData.Snapshot(simT: 5, situation: "landed", radarAltM: 1.5, lat: -0.0, lon: 12.25)));
        pipeline.ProcessSignal(new FrameBoundarySignal(5, TestData.WallMs, 1));
        IReadOnlyList<EventEnvelope> settled = pipeline.ProcessSignal(
            new FrameBoundarySignal(5.1, TestData.WallMs, 2));

        JsonElement situation = Payload(Assert.Single(onTouchdown, static e => e.Type == EventTypes.VehicleSituation));
        Assert.Equal(1.5, situation.GetProperty("radar_alt_m").GetDouble());

        JsonElement landed = Payload(Assert.Single(settled, static e => e.Type == EventTypes.VehicleLanded));
        Assert.Equal(1.5, landed.GetProperty("radar_alt_m").GetDouble());

        // Zero really is a value here: the prime meridian and the equator are places, and this is
        // the case the omit-do-not-zero rule exists to keep distinguishable from "unknown".
        Assert.Equal(0.0, landed.GetProperty("lat").GetDouble());
        Assert.Equal(12.25, landed.GetProperty("lon").GetDouble());

        static JsonElement Payload(EventEnvelope envelope)
        {
            using JsonDocument document = JsonDocument.Parse(envelope.ToNdjsonLine());
            return document.RootElement.GetProperty("payload").Clone();
        }
    }

    // The hop-lithobrake shape from GoldenScenario_HopAndSurvivedLithobrake, parameterised by the
    // filter so the two runs are the same inputs in the same order.
    private static List<EventEnvelope> HopAndSurvivedLithobrake(EventTypeFilter types)
    {
        EventPipeline pipeline = TestData.Pipeline(types: types);
        var produced = new List<EventEnvelope>();

        produced.Add(pipeline.SessionStarted(0, TestData.WallMs));
        produced.AddRange(pipeline.ProcessSignal(TestData.Created(simT: 0, crewCount: 2)));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(1,
            TestData.Snapshot(simT: 0, situation: "landed", altitudeM: 0))));
        produced.AddRange(pipeline.ProcessFrame(TestData.Frame(2,
            TestData.Snapshot(simT: 4, situation: "freefall", altitudeM: 4_000, surfaceSpeedMs: 300))));
        produced.AddRange(pipeline.ProcessSignal(TestData.Impact(simT: 30, speedMs: 62, crewCount: 2)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30, TestData.WallMs, 3)));
        produced.AddRange(pipeline.ProcessSignal(new FrameBoundarySignal(30.1, TestData.WallMs, 4)));
        produced.AddRange(pipeline.ProcessSignal(
            new VehicleRecoveredSignal(31, TestData.WallMs, "v1", 2)));

        return produced;
    }

    // The NDJSON line each envelope would ship, with the two freshly minted ULIDs replaced by
    // stable placeholders — they differ between any two runs by construction, and everything else
    // in the line is exactly what the server would receive.
    private static string[] Canonical(IEnumerable<EventEnvelope> produced)
    {
        var flights = new Dictionary<string, string>(StringComparer.Ordinal);
        return produced
            .Select(e => (e with
            {
                Id = "<id>",
                Flight = e.Flight is null
                    ? null
                    : Placeholder(flights, e.Flight),
            }).ToNdjsonLine())
            .ToArray();

        static string Placeholder(Dictionary<string, string> seen, string flight)
        {
            if (!seen.TryGetValue(flight, out string? name))
            {
                name = $"<flight-{seen.Count}>";
                seen[flight] = name;
            }

            return name;
        }
    }

    private sealed record UnknownSignal(double SimT, long WallMs) : GameSignal(SimT, WallMs);
}
