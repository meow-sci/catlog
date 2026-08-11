using System;
using System.Linq;
using System.Text.Json;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

public sealed class SystemBoundaryTests
{
    [Fact]
    public void DeliverySendsBodiesOnceAndPersistsMarker()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");
        SystemSnapshot survey = Survey();
        const string career = "fedcba9876543210";

        using (OutboxDb outbox = OutboxDb.Open(path))
        {
            EventPipeline pipeline = TestData.Pipeline(careerId: career);
            Assert.Equal(4, SystemBoundaryDelivery.Append(pipeline, outbox,
                new SessionLoadedSignal(1, TestData.WallMs, "build", "mod", career, survey)));
            Assert.Equal("1", outbox.GetState(Wire.StateKeys.Survey(career, survey.SystemId)));
        }

        using OutboxDb reopened = OutboxDb.Open(path);
        EventPipeline restarted = TestData.Pipeline(careerId: career);
        Assert.Equal(2, SystemBoundaryDelivery.Append(restarted, reopened,
            new SessionLoadedSignal(2, TestData.WallMs, "build", "mod", career, survey)));
        Assert.Equal(6, reopened.PendingCount);
        string[] types = reopened.NextBatch().Lines.Select(static line =>
            JsonDocument.Parse(line).RootElement.GetProperty("type").GetString()!).ToArray();
        Assert.Equal(2, types.Count(static type => type == EventTypes.SystemBody));
        bool[] completeness = reopened.NextBatch().Lines.Select(static line => JsonDocument.Parse(line).RootElement)
            .Where(static row => row.GetProperty("type").GetString() == EventTypes.SystemDiscovered)
            .Select(static row => row.GetProperty("payload").GetProperty("complete").GetBoolean()).ToArray();
        Assert.Equal([true, false], completeness);
    }

    [Fact]
    public void DisabledBodiesDoNotMarkAndReenableSendsCatalogue()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        SystemSnapshot survey = Survey();
        const string career = "fedcba9876543210";
        string marker = Wire.StateKeys.Survey(career, survey.SystemId);
        EventPipeline disabled = TestData.Pipeline(
            careerId: career, types: EventTypeFilter.Create([EventTypes.SystemBody]));

        Assert.Equal(2, SystemBoundaryDelivery.Append(disabled, outbox,
            new SessionLoadedSignal(1, TestData.WallMs, "build", "mod", career, survey)));
        Assert.Null(outbox.GetState(marker));
        using (JsonDocument first = JsonDocument.Parse(outbox.NextBatch(maxEvents: 1).Lines[0]))
            Assert.False(first.RootElement.GetProperty("payload").GetProperty("complete").GetBoolean());

        EventPipeline enabled = TestData.Pipeline(careerId: career);
        Assert.Equal(4, SystemBoundaryDelivery.Append(enabled, outbox,
            new SessionLoadedSignal(2, TestData.WallMs, "build", "mod", career, survey)));
        Assert.Equal("1", outbox.GetState(marker));
    }

    [Fact]
    public void CapAndInvalidRequiredNumericProduceNoBodiesOrMarker()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        const string career = "fedcba9876543210";
        EventPipeline pipeline = TestData.Pipeline(careerId: career);
        SystemSnapshot invalid = Survey(body => body with { AxisCce = new Vec3(0, double.PositiveInfinity, 0) });

        Assert.Equal(2, SystemBoundaryDelivery.Append(pipeline, outbox,
            new SessionLoadedSignal(1, TestData.WallMs, "build", "mod", career, invalid)));
        Assert.Null(outbox.GetState(Wire.StateKeys.Survey(career, invalid.SystemId)));

        SystemBodySnapshot body = Survey().Bodies[0];
        var many = Enumerable.Repeat(body, Wire.MaxSystemBodies + 1).ToArray();
        var capped = invalid with { SystemId = "01kittencap", Bodies = many };
        Assert.Equal(2, SystemBoundaryDelivery.Append(pipeline, outbox,
            new SessionLoadedSignal(2, TestData.WallMs, "build", "mod", career, capped)));
        Assert.Null(outbox.GetState(Wire.StateKeys.Survey(career, capped.SystemId)));
    }

    [Fact]
    public void BoundaryOrdersHeaderBodiesThenSessionOnTheNewIdentity()
    {
        EventPipeline pipeline = TestData.Pipeline();
        SystemSnapshot survey = Survey();

        var events = pipeline.ProcessSignal(new SessionLoadedSignal(
            12, TestData.WallMs, "build", "mod", "fedcba9876543210", survey,
            IncludeSystemBodies: true, SystemComplete: true));

        Assert.Equal(
            [EventTypes.SystemDiscovered, EventTypes.SystemBody, EventTypes.SystemBody, EventTypes.SessionStarted],
            events.Select(static e => e.Type).ToArray());
        Assert.All(events, static e => Assert.Equal("fedcba9876543210", e.Career));
        Assert.All(events, e => Assert.Equal(pipeline.SessionId, e.Session));
        Assert.Equal(["sol", "earth"], events.Where(static e => e.Type == EventTypes.SystemBody)
            .Select(static e => Assert.IsType<SystemBodyPayload>(e.Payload).Body).ToArray());
    }

    [Fact]
    public void HeaderOnlyBoundarySaysIncompleteAndNullSystemEmitsNothing()
    {
        EventPipeline pipeline = TestData.Pipeline();
        var headerOnly = pipeline.ProcessSignal(new SessionLoadedSignal(
            12, TestData.WallMs, "build", "mod", null, Survey()));

        Assert.Equal([EventTypes.SystemDiscovered, EventTypes.SessionStarted],
            headerOnly.Select(static e => e.Type).ToArray());
        Assert.False(Assert.IsType<SystemDiscoveredPayload>(headerOnly[0].Payload).Complete);
        Assert.Empty(pipeline.ProcessSignal(new SessionLoadedSignal(13, TestData.WallMs, "build", "mod")));
    }

    [Theory]
    [InlineData(double.NaN)]
    [InlineData(double.PositiveInfinity)]
    [InlineData(double.NegativeInfinity)]
    public void OrbitOptionalsAreAllOrNothingAndPeriodIsIndependent(double bad)
    {
        SystemSnapshot survey = Survey(body => body with
        {
            Eccentricity = bad,
            PeriodS = bad,
        });
        EventPipeline pipeline = TestData.Pipeline();
        var events = pipeline.ProcessSignal(new SessionLoadedSignal(
            12, TestData.WallMs, "build", "mod", null, survey, true, true));
        SystemBodyPayload earth = events.Select(static e => e.Payload).OfType<SystemBodyPayload>()
            .Single(static p => p.Body == "earth");

        string json = JsonSerializer.Serialize(earth, CatlogJson.Options);
        Assert.DoesNotContain("sma_m", json, StringComparison.Ordinal);
        Assert.DoesNotContain("ecc", json, StringComparison.Ordinal);
        Assert.DoesNotContain("period_s", json, StringComparison.Ordinal);
    }

    [Theory]
    [InlineData(double.NaN)]
    [InlineData(double.PositiveInfinity)]
    [InlineData(double.NegativeInfinity)]
    public void InvalidRequiredNumericEmitsIncompleteHeaderAndNoBodyOrMarker(double bad)
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        const string career = "fedcba9876543210";
        SystemSnapshot survey = Survey(body => body with { RadiusM = bad });
        EventPipeline pipeline = TestData.Pipeline(careerId: career);

        Assert.Equal(2, SystemBoundaryDelivery.Append(pipeline, outbox,
            new SessionLoadedSignal(1, TestData.WallMs, "build", "mod", career, survey)));
        JsonElement[] rows = outbox.NextBatch().Lines.Select(static line =>
            JsonDocument.Parse(line).RootElement.Clone()).ToArray();
        Assert.Equal([EventTypes.SystemDiscovered, EventTypes.SessionStarted],
            rows.Select(static row => row.GetProperty("type").GetString()!).ToArray());
        Assert.False(rows[0].GetProperty("payload").GetProperty("complete").GetBoolean());
        Assert.Null(outbox.GetState(Wire.StateKeys.Survey(career, survey.SystemId)));
    }

    private static SystemSnapshot Survey(Func<SystemBodySnapshot, SystemBodySnapshot>? earthEdit = null)
    {
        var root = new SystemBodySnapshot(
            "sol", "Sol", "StellarBody", "star", 0, null,
            10, 20, 0, 0, 0, 1, new Vec3(0, 1, 0), new Quat(0, 0, 0, 1),
            null, null, null, null, null, null, null);
        var earth = new SystemBodySnapshot(
            "earth", "Earth", "TerrestrialBody", "planet", 1, "sol",
            3, 4, 5, 6, 0, 1, new Vec3(0, 1, 0), new Quat(0, 0, 0, 1),
            100, 0.1, 2, 3, 4, -5, 50);
        earth = earthEdit?.Invoke(earth) ?? earth;
        var hash = new SystemHashInput("Sol", "Sol", "Earth", 2, Array.Empty<SystemBodyHashInput>());
        return new SystemSnapshot("01kittensol", "Sol", "Sol", "earth", [root, earth], hash);
    }
}
