using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>
/// The §7.5 ingest cases against a real <c>catlogd</c>: ship, replay, tamper, and the clock-skew
/// recovery the mod is required to perform.
/// </summary>
public sealed class IngestTests : IClassFixture<ServerFixture>, IDisposable
{
    private const int BatchSize = 24;

    private readonly ServerFixture _server;
    private readonly List<IDisposable> _disposables = [];

    public IngestTests(ServerFixture server) => _server = server;

    public void Dispose()
    {
        foreach (IDisposable disposable in _disposables)
            disposable.Dispose();
    }

    [Fact]
    public async Task ShippingABatchStoresEveryEvent()
    {
        IssuedCredential credential = _server.Issue("itest_ship");
        using var client = new IngestClient(_server.IngestUrl, credential.Credential);
        byte[] batch = TestBatch.Ndjson(BatchSize);

        long before = _server.TotalEvents();
        IngestResponse response = await client.ShipAsync(batch);

        Assert.True(response.Status == 200, $"expected 200, got {response.Status}: {response.Body}");
        Assert.Equal(BatchSize, response.Accepted);
        Assert.Equal(0, response.Deduped);
        Assert.False(response.Replay, "a first presentation is not a replay");

        // §4.4 requires Date on every response; the mod's clock_skew recovery reads it.
        Assert.True(response.Date is not null, "no Date header on the 200 (§4.4)");
        Assert.Equal(before + BatchSize, _server.TotalEvents());
    }

    [Fact]
    public async Task ReshippingTheSameBatchIdIsAReplay()
    {
        IssuedCredential credential = _server.Issue("itest_replay");
        using var client = new IngestClient(_server.IngestUrl, credential.Credential);
        byte[] batch = TestBatch.Ndjson(BatchSize);

        IngestResponse first = await client.ShipAsync(batch);
        Assert.True(first.Status == 200, $"first ship: {first.Status} {first.Body}");
        Assert.Equal(BatchSize, first.Accepted);

        // Exactly the same batch id, sequence and bytes: the §4.5.3 step-11 short-circuit, which
        // is what a mod re-ships after a crash between "server committed" and "outbox pruned".
        IngestResponse replay = await client.ShipAsync(batch, new ShipOverrides
        {
            Jti = client.LastJti,
            Seq = 1,
            Advance = false,
        });

        Assert.True(replay.Status == 200, $"replay: {replay.Status} {replay.Body}");
        Assert.True(replay.Replay, $"expected a replay short-circuit, got {replay.Body}");
        Assert.Equal(0, replay.Accepted);
    }

    [Fact]
    public async Task TamperingWithTheBodyIsRejected()
    {
        IssuedCredential credential = _server.Issue("itest_tamper");
        using var client = new IngestClient(_server.IngestUrl, credential.Credential);

        byte[] ndjson = TestBatch.Ndjson(BatchSize);
        byte[] tampered = BrotliCodec.Compress(ndjson);
        tampered[^1] ^= 0xFF;

        long before = _server.TotalEvents();
        IngestResponse response = await client.ShipAsync(
            ndjson,
            new ShipOverrides { TamperedBody = tampered, Advance = false });

        Assert.True(response.Status == 401, $"expected 401, got {response.Status}: {response.Body}");
        Assert.Equal(Wire.Errors.ProofInvalid, response.Error);
        Assert.Equal(before, _server.TotalEvents());
    }

    [Fact]
    public async Task AClockSkewedShipperResyncsAndSucceeds()
    {
        IssuedCredential credential = _server.Issue("itest_skew");
        using OutboxDb outbox = OpenOutbox();
        outbox.Append(TestBatch.Build(BatchSize));

        // Ten minutes fast: well outside §4.3's ±300 s window, so the first attempt must come back
        // 401 clock_skew and the shipper must learn the offset from Date and retry exactly once.
        var clock = new OffsetClock(TimeSpan.FromMinutes(10));
        using var shipper = new BatchShipper(
            new ShipperOptions(_server.IngestUrl, BatchEventCap: 500, OutboxCapBytes: 0),
            outbox,
            credential.Credential,
            handler: null,
            clock: clock);

        long before = _server.TotalEvents();
        ShipAttempt attempt = await shipper.ShipOnceAsync(CancellationToken.None);

        Assert.True(
            attempt.Outcome == ShipOutcome.Accepted,
            $"expected the skew retry to succeed, got {attempt.Outcome} ({attempt.StatusCode} {attempt.Error})");
        Assert.Equal(BatchSize, attempt.EventsShipped);
        Assert.Equal(before + BatchSize, _server.TotalEvents());

        // The learned offset is roughly minus the skew: the shipper now signs in server time.
        Assert.InRange(shipper.ClockOffsetMs, -615_000, -585_000);
        Assert.False(shipper.IsDead, "a recoverable clock skew must not latch the shipper dead");
        Assert.Equal(0, outbox.PendingCount);
    }

    private OutboxDb OpenOutbox()
    {
        string dir = Path.Combine(Path.GetTempPath(), "catlog-itest-outbox-" + Guid.NewGuid().ToString("N"));
        OutboxDb outbox = OutboxDb.Open(Path.Combine(dir, "outbox.db"));
        _disposables.Add(new DirectoryCleanup(dir));
        return outbox;
    }

    /// <summary>A clock that is deliberately wrong, so the §4.5.3 skew path is a real round trip.</summary>
    private sealed class OffsetClock(TimeSpan offset) : IShipperClock
    {
        public DateTimeOffset UtcNow => DateTimeOffset.UtcNow + offset;

        public Task Delay(TimeSpan delay, CancellationToken ct) => Task.CompletedTask;
    }

    private sealed class DirectoryCleanup(string path) : IDisposable
    {
        public void Dispose()
        {
            try
            {
                if (Directory.Exists(path))
                    Directory.Delete(path, recursive: true);
            }
            catch (IOException)
            {
                // Not a test failure.
            }
        }
    }
}
