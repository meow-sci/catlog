using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>
/// A server that refuses more than 60 events per batch, so the mod's <c>413</c> halving ladder is
/// a real round trip rather than a locally-detected oversize.
/// </summary>
/// <remarks>
/// This fixture always owns its process: the case is about a server limit, and pointing it at
/// whatever <c>CATLOG_SERVER_URL</c> names would silently turn the test into a no-op on a server
/// running §4.3's default of 2 000.
/// </remarks>
public sealed class ConstrainedServerFixture : ServerFixture
{
    /// <summary>The server-side events-per-batch cap this fixture runs with.</summary>
    public const int MaxEventsPerBatch = 60;

    protected override bool AlwaysSpawn => true;

    protected override IReadOnlyList<string> ExtraEnvironment =>
        ["CATLOG_INGEST_MAX_EVENTS=" + MaxEventsPerBatch];
}

/// <summary>The §7.5 oversize case: <c>413 too_large</c> → halve the batch cap → succeed.</summary>
public sealed class OversizeTests : IClassFixture<ConstrainedServerFixture>, IDisposable
{
    private const int Events = 200;

    private readonly ConstrainedServerFixture _server;
    private readonly string _outboxDir =
        Path.Combine(Path.GetTempPath(), "catlog-itest-outbox-" + Guid.NewGuid().ToString("N"));

    public OversizeTests(ConstrainedServerFixture server) => _server = server;

    public void Dispose()
    {
        try
        {
            if (Directory.Exists(_outboxDir))
                Directory.Delete(_outboxDir, recursive: true);
        }
        catch (IOException)
        {
            // Not a test failure.
        }
    }

    [Fact]
    public async Task A413HalvesTheBatchCapUntilTheBatchFits()
    {
        IssuedCredential credential = _server.Issue("itest_oversize");
        using OutboxDb outbox = OutboxDb.Open(Path.Combine(_outboxDir, "outbox.db"));
        outbox.Append(TestBatch.Build(Events));

        // The halving ladder is four rejections and then several accepted batches — nine requests
        // in all, and the hard 30 s reporting floor stands between each pair of them. The clock is
        // injected so those nine windows cost microseconds instead of four minutes; the floor is
        // still enforced, on the timeline the shipper actually reads.
        var clock = new AdvanceableClock();
        using var shipper = new BatchShipper(
            new ShipperOptions(_server.IngestUrl, BatchEventCap: 500, OutboxCapBytes: 0),
            outbox,
            credential.Credential,
            handler: null,
            clock: clock);

        Assert.Equal(500, shipper.BatchEventCap);

        var caps = new List<int>();
        var rejections = new List<int>();
        long before = _server.TotalEvents();

        for (int attempts = 0; outbox.PendingCount > 0 && attempts < 40; attempts++)
        {
            clock.OpenShipWindow(shipper);
            ShipAttempt attempt = await shipper.ShipOnceAsync(CancellationToken.None);
            Assert.False(
                shipper.IsDead,
                $"the shipper latched dead during the halving ladder: {shipper.DeadReason}");

            if (attempt.Outcome == ShipOutcome.TooLarge)
            {
                rejections.Add(attempt.StatusCode);
                caps.Add(shipper.BatchEventCap);
                continue;
            }

            Assert.True(
                attempt.Outcome == ShipOutcome.Accepted,
                $"unexpected outcome {attempt.Outcome} ({attempt.StatusCode} {attempt.Error})");
        }

        // 500 → 250 → 125 → 62 → 50: four server rejections, each an honest 413, and then a
        // 50-event batch that fits under the server's 60.
        Assert.Equal([250, 125, 62, Wire.MinBatchEventCap], caps);
        Assert.All(rejections, status => Assert.Equal(413, status));
        Assert.Equal(Wire.MinBatchEventCap, shipper.BatchEventCap);

        Assert.Equal(0, outbox.PendingCount);
        Assert.Equal(before + Events, _server.TotalEvents());
    }
}
