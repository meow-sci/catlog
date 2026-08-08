using System.Collections.Generic;
using System.Linq;
using System.Text;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Outbox;

/// <summary>
/// §7.2: the SQLite write-ahead outbox — append/drain ordering, prune drops
/// passive first, crash recovery on reopen mid-batch, and the shipper-state round-trip.
/// </summary>
public sealed class OutboxDbTests
{
    [Fact]
    public void AppendAndDrain_PreserveOrder()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));

        IReadOnlyList<EventEnvelope> events = TestData.Envelopes(5);
        Assert.Equal(5, outbox.Append(events));

        OutboxBatch batch = outbox.NextBatch();

        // §4.3: events within a batch are ordered by outbox append order, oldest first.
        Assert.Equal(
            events.Select(static e => e.Id).ToArray(),
            batch.Lines.Select(static line => System.Text.Json.JsonDocument.Parse(line)
                .RootElement.GetProperty("id").GetString()).ToArray());
        Assert.Equal(5, outbox.PendingCount);
    }

    [Fact]
    public void Append_IsIdempotentOnEventId()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        IReadOnlyList<EventEnvelope> events = TestData.Envelopes(3);

        Assert.Equal(3, outbox.Append(events));
        Assert.Equal(0, outbox.Append(events));
        Assert.Equal(3, outbox.PendingCount);
    }

    [Fact]
    public void NextBatch_RespectsTheEventCap()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append(TestData.Envelopes(200));

        OutboxBatch batch = outbox.NextBatch(maxEvents: 50);

        Assert.Equal(50, batch.Count);
    }

    [Fact]
    public void NextBatch_RespectsTheByteCap()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append(TestData.Envelopes(50));
        int lineBytes = Encoding.UTF8.GetByteCount(outbox.NextBatch(maxEvents: 1).Lines[0]);

        OutboxBatch batch = outbox.NextBatch(maxEvents: 50, maxBytes: (lineBytes * 3) + 3);

        Assert.InRange(batch.Count, 1, 3);
    }

    [Fact]
    public void MarkShipped_DeletesOnlyTheShippedRows()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append(TestData.Envelopes(10));
        OutboxBatch batch = outbox.NextBatch(maxEvents: 4);

        Assert.Equal(4, outbox.MarkShipped(batch.LastRowId));
        Assert.Equal(6, outbox.PendingCount);
    }

    /// <summary>
    /// Nothing is deleted until the server has answered 200, so a process killed mid-ship
    /// re-ships. The server dedups on <c>(player, event_id)</c>, so a duplicate costs nothing;
    /// a deletion would cost the events.
    /// </summary>
    [Fact]
    public void CrashRecovery_ReopeningMidBatchLosesNothing()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");

        string[] firstBatchIds;
        using (OutboxDb outbox = OutboxDb.Open(path))
        {
            outbox.Append(TestData.Envelopes(8));
            OutboxBatch batch = outbox.NextBatch(maxEvents: 4);
            firstBatchIds = batch.Lines.ToArray();
            // No MarkShipped: the process dies here, mid-ship.
        }

        using (OutboxDb reopened = OutboxDb.Open(path))
        {
            Assert.Equal(8, reopened.PendingCount);
            OutboxBatch again = reopened.NextBatch(maxEvents: 4);
            Assert.Equal(firstBatchIds, again.Lines.ToArray());
        }
    }

    [Fact]
    public void ShipperState_RoundTripsAcrossReopen()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");

        using (OutboxDb outbox = OutboxDb.Open(path))
        {
            outbox.SetState(Wire.StateKeys.StreamId, "01J9V5M3E8Z0FAKESTREAM001");
            outbox.SetState(Wire.StateKeys.Seq, "42");
            outbox.SetState(Wire.StateKeys.LastBh, "abc123");
            outbox.SetState(Wire.StateKeys.ClockOffsetMs, "-1500");
            outbox.SetState(Wire.StateKeys.Seq, "43"); // upsert
        }

        using (OutboxDb reopened = OutboxDb.Open(path))
        {
            Assert.Equal("01J9V5M3E8Z0FAKESTREAM001", reopened.GetState(Wire.StateKeys.StreamId));
            Assert.Equal("43", reopened.GetState(Wire.StateKeys.Seq));
            Assert.Equal("abc123", reopened.GetState(Wire.StateKeys.LastBh));
            Assert.Equal("-1500", reopened.GetState(Wire.StateKeys.ClockOffsetMs));
            Assert.Null(reopened.GetState("never-set"));

            reopened.ClearState(Wire.StateKeys.LastBh);
            Assert.Null(reopened.GetState(Wire.StateKeys.LastBh));
        }
    }

    /// <summary>
    /// Losing a <c>telemetry.window</c> costs resolution on a graph; losing a
    /// <c>vehicle.impact</c> costs a leaderboard entry. Prune must reflect that.
    /// </summary>
    [Fact]
    public void Prune_DropsOldestPassiveFirstAndNeverScoringEvents()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));

        var mixed = new List<EventEnvelope>();
        for (int i = 0; i < 300; i++)
        {
            mixed.Add(TestData.Envelope(
                type: EventTypes.TelemetryWindow,
                simT: i,
                payload: Window(i)));
            mixed.Add(TestData.Envelope(
                type: EventTypes.VehicleStaging, simT: i, payload: new VehicleStagingPayload(i)));
        }

        outbox.Append(mixed);
        long before = outbox.TotalBytes;

        int dropped = outbox.Prune(before / 4);

        Assert.True(dropped > 0, "pruning should have dropped something");
        OutboxBatch remaining = outbox.NextBatch(maxEvents: Wire.MaxEventsPerBatch);
        string[] types = remaining.Lines
            .Select(static line => System.Text.Json.JsonDocument.Parse(line)
                .RootElement.GetProperty("type").GetString()!)
            .ToArray();
        Assert.Equal(300, types.Count(static t => t == EventTypes.VehicleStaging));
        Assert.DoesNotContain(EventTypes.TelemetryWindow, types);
    }

    [Fact]
    public void Prune_StopsWhenOnlyScoringEventsRemain()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append(TestData.Envelopes(100, EventTypes.VehicleStaging));

        int dropped = outbox.Prune(1);

        Assert.Equal(0, dropped);
        Assert.Equal(100, outbox.PendingCount);
    }

    [Fact]
    public void EmptyOutbox_ReportsEmptyBatchAndNoOldest()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));

        Assert.Equal(0, outbox.PendingCount);
        Assert.Null(outbox.OldestCreatedMs);
        Assert.True(outbox.NextBatch().IsEmpty);
        Assert.Equal(0, outbox.MarkShipped(0));
        Assert.Equal(0, outbox.Prune(0));
    }

    [Fact]
    public void OldestCreatedMs_IsTheFirstAppendedEventsWallClock()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append([TestData.Envelope(wallMs: 1_000), TestData.Envelope(wallMs: 500)]);

        Assert.Equal(1_000, outbox.OldestCreatedMs);
    }

    [Fact]
    public void ToNdjson_IsLfSeparatedWithATrailingNewline()
    {
        using var dir = new TempDir();
        using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
        outbox.Append(TestData.Envelopes(3));

        string ndjson = Encoding.UTF8.GetString(outbox.NextBatch().ToNdjson());

        Assert.EndsWith("\n", ndjson);
        Assert.Equal(3, ndjson.Split('\n', System.StringSplitOptions.RemoveEmptyEntries).Length);
        Assert.DoesNotContain('\r', ndjson);
    }

    [Fact]
    public void CreatesParentDirectories()
    {
        using var dir = new TempDir();
        string path = System.IO.Path.Combine(dir.Path, "nested", "deeper", "outbox.db");

        using OutboxDb outbox = OutboxDb.Open(path);

        Assert.Equal(0, outbox.PendingCount);
        Assert.True(System.IO.File.Exists(path));
    }

    [Fact]
    public void DisposeIsIdempotent()
    {
        using var dir = new TempDir();
        OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));

        outbox.Dispose();
        outbox.Dispose();
    }

    private static TelemetryWindowPayload Window(int i) => new(
        T0Sim: i * 30.0, T1Sim: (i * 30.0) + 29.5, N: 60, Body: "earth",
        AltM: new Agg(0, 1, 0.5, 1), SurfaceSpeedMs: new Agg(0, 1, 0.5, 1),
        OrbitalSpeedMs: new Agg(0, 1, 0.5, 1), AccelMs2: new Agg(0, 1, 0.5, 1),
        PeakG: null, MaxQPa: null, MassKgLast: 1_000);
}
