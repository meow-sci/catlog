using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Security.Cryptography;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Ship;

/// <summary>
/// INITIAL_IMPL_PLAN §4.5.2 / §4.5.3 / §7.2: batch triggers, the seq/ph chain, and every mod-side
/// recovery path. No sockets and no real waiting — the transport and the clock are both injected.
/// </summary>
public sealed class BatchShipperTests
{
    private const string IngestUrl = "http://127.0.0.1:8080/v1/ingest";

    /// <summary>The hard, non-configurable minimum between two requests to the ingest endpoint.</summary>
    private static readonly TimeSpan ShipFloor = TimeSpan.FromSeconds(Wire.MinShipIntervalSeconds);

    // ----- request shape --------------------------------------------------------------

    [Fact]
    public async Task EmptyOutbox_ShipsNothing()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.NothingToShip, attempt.Outcome);
        Assert.Empty(harness.Handler.Requests);
    }

    [Fact]
    public async Task AcceptedBatch_HasTheContractRequestShape()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok(3)));
        harness.Outbox.Append(TestData.Envelopes(3));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Accepted, attempt.Outcome);
        RecordedRequest request = Assert.Single(harness.Handler.Requests);

        Assert.Equal(Wire.ContentType, request.ContentType);
        Assert.Equal(Wire.ContentEncoding, request.ContentEncoding);
        Assert.Equal(harness.Credential.License, request.License);
        Assert.Equal(3, request.Ndjson.Split('\n', StringSplitOptions.RemoveEmptyEntries).Length);

        // The proof verifies with the credential key, and the header carries only the public JWK.
        Assert.True(Jws.Verify(request.Proof, harness.Credential.Key), "the proof must verify");
        JsonElement header = request.ProofHeader;
        Assert.Equal(Wire.Alg, header.GetProperty("alg").GetString());
        Assert.Equal(Wire.ProofTyp, header.GetProperty("typ").GetString());
        Assert.Equal(4, header.GetProperty("jwk").EnumerateObject().Count());

        JsonElement claims = request.ProofClaims;
        Assert.Equal("POST", claims.GetProperty("htm").GetString());
        Assert.Equal(IngestUrl, claims.GetProperty("htu").GetString());
        Assert.Equal(1, claims.GetProperty("seq").GetInt64());
        Assert.True(Ids.IsUlid(claims.GetProperty("jti").GetString()), "jti is the batch ULID");
        Assert.True(Ids.IsUlid(claims.GetProperty("sid").GetString()), "sid is the stream ULID");
        Assert.False(claims.TryGetProperty("ph", out _), "ph is omitted when seq == 1");

        // bh is over the raw body bytes AS SENT, i.e. post-Brotli.
        Assert.Equal(Bytes.Sha256Base64Url(request.Body), claims.GetProperty("bh").GetString());
    }

    [Fact]
    public async Task SeqAndPhChainAcrossBatches()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));

        harness.Outbox.Append(TestData.Envelopes(2));
        await harness.Shipper.ShipOnceAsync();
        harness.OpenShipWindow();
        harness.Outbox.Append(TestData.Envelopes(2));
        await harness.Shipper.ShipOnceAsync();
        harness.OpenShipWindow();
        harness.Outbox.Append(TestData.Envelopes(2));
        await harness.Shipper.ShipOnceAsync();

        Assert.Equal(3, harness.Handler.Requests.Count);

        for (int i = 0; i < 3; i++)
        {
            JsonElement claims = harness.Handler.Requests[i].ProofClaims;
            Assert.Equal(i + 1, claims.GetProperty("seq").GetInt64());
            if (i == 0)
            {
                Assert.False(claims.TryGetProperty("ph", out _));
            }
            else
            {
                // ph is the hash of the PREVIOUS batch's body bytes.
                Assert.Equal(
                    Bytes.Sha256Base64Url(harness.Handler.Requests[i - 1].Body),
                    claims.GetProperty("ph").GetString());
            }
        }

        // Every batch in a chain shares one stream id.
        Assert.Single(harness.Handler.Requests.Select(static r => r.ProofClaims.GetProperty("sid").GetString()!)
            .Distinct());
    }

    [Fact]
    public async Task AcceptedBatch_DeletesTheShippedRowsOnly()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));
        harness.Outbox.Append(TestData.Envelopes(10));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync(default);

        Assert.Equal(ShipOutcome.Accepted, attempt.Outcome);
        Assert.Equal(10, attempt.EventsShipped);
        Assert.Equal(0, harness.Outbox.PendingCount);
    }

    [Fact]
    public async Task Replay_IsReportedButStillAdvancesTheChain()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok(2, replay: true)));
        harness.Outbox.Append(TestData.Envelopes(2));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Replayed, attempt.Outcome);
        Assert.Equal(0, harness.Outbox.PendingCount);
        Assert.Equal(2, harness.Shipper.Sequence);
    }

    // ----- §4.5.3 recovery table ------------------------------------------------------

    /// <summary>
    /// 401 clock_skew → recompute the offset from the <c>Date</c> header and re-sign the same
    /// batch on the next attempt.
    /// </summary>
    /// <remarks>
    /// The re-signed request used to go out immediately, inside the same
    /// <see cref="Wire.MinShipIntervalSeconds"/> window. It no longer does: a retry is a request
    /// to the ingest endpoint like any other, so it waits for the window like any other. Nothing
    /// about the recovery changes except when it lands — the offset is learned and persisted
    /// straight away, and the batch id, the seq and the bytes are all still the originals.
    /// </remarks>
    [Fact]
    public async Task ClockSkew_ResyncsFromTheDateHeaderAndResendsInTheNextWindow()
    {
        DateTimeOffset localNow = DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);
        DateTimeOffset serverNow = localNow.AddHours(3); // the player's clock is three hours slow
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.ClockSkew(serverNow),
            () => FakeHttpHandler.Ok());
        using var harness = new Harness(handler, clock: new FakeShipperClock(localNow));
        harness.Outbox.Append(TestData.Envelopes(1));

        ShipAttempt resync = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.ClockResynced, resync.Outcome);
        Assert.Single(handler.Requests);
        Assert.Equal((long)TimeSpan.FromHours(3).TotalMilliseconds, harness.Shipper.ClockOffsetMs);
        Assert.Equal(1, harness.Outbox.PendingCount);

        // Not even a resync buys a request inside the window.
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(handler.Requests);

        harness.OpenShipWindow();
        ShipAttempt accepted = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Accepted, accepted.Outcome);
        Assert.Equal(2, handler.Requests.Count);

        long firstIat = handler.Requests[0].ProofClaims.GetProperty("iat").GetInt64();
        long secondIat = handler.Requests[1].ProofClaims.GetProperty("iat").GetInt64();
        Assert.Equal(localNow.ToUnixTimeSeconds(), firstIat);
        Assert.Equal(serverNow.Add(ShipFloor).ToUnixTimeSeconds(), secondIat);

        // Only `iat` moved. The body, the seq and — because this is a retry of the same bytes —
        // the batch id are all unchanged, so the resend is idempotent even if the "skewed" request
        // had somehow been stored.
        Assert.Equal(
            handler.Requests[0].ProofClaims.GetProperty("jti").GetString(),
            handler.Requests[1].ProofClaims.GetProperty("jti").GetString());
        Assert.Equal(
            handler.Requests[0].ProofClaims.GetProperty("seq").GetInt64(),
            handler.Requests[1].ProofClaims.GetProperty("seq").GetInt64());
        Assert.Equal(handler.Requests[0].Body, handler.Requests[1].Body);
    }

    /// <summary>
    /// A skew that never resolves is retryable rather than fatal, and costs one request per
    /// window — never a hot loop, because the floor is what paces it.
    /// </summary>
    [Fact]
    public async Task ClockSkew_ThatNeverResolves_CostsOneRequestPerWindow()
    {
        DateTimeOffset serverNow = DateTimeOffset.UnixEpoch.AddSeconds(1_780_000_000);
        var handler = FakeHttpHandler.Always(() => FakeHttpHandler.ClockSkew(serverNow));
        using var harness = new Harness(handler);
        harness.Outbox.Append(TestData.Envelopes(1));

        for (int window = 0; window < 3; window++)
        {
            Assert.Equal(ShipOutcome.ClockResynced, (await harness.Shipper.ShipOnceAsync()).Outcome);
            Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
            harness.OpenShipWindow();
        }

        Assert.Equal(3, handler.Requests.Count);
        Assert.False(harness.Shipper.IsDead, "a persistent skew is retryable, not fatal");
        Assert.Equal(1, harness.Outbox.PendingCount);
    }

    [Fact]
    public async Task ClockSkew_FallsBackToServerTimeWhenThereIsNoDateHeader()
    {
        DateTimeOffset localNow = DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);
        DateTimeOffset serverNow = localNow.AddMinutes(-11);
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(
                HttpStatusCode.Unauthorized, Wire.Errors.ClockSkew,
                $"\"server_time\":{serverNow.ToUnixTimeMilliseconds()}"),
            () => FakeHttpHandler.Ok());
        using var harness = new Harness(handler, clock: new FakeShipperClock(localNow));
        harness.Outbox.Append(TestData.Envelopes(1));

        await harness.Shipper.ShipOnceAsync();

        Assert.Equal((long)TimeSpan.FromMinutes(-11).TotalMilliseconds, harness.Shipper.ClockOffsetMs);
    }

    [Theory]
    [InlineData(Wire.Errors.LicenseInvalid)]
    [InlineData(Wire.Errors.LicenseExpired)]
    [InlineData(Wire.Errors.LicenseRevoked)]
    [InlineData(Wire.Errors.ProofInvalid)]
    [InlineData(Wire.Errors.Banned)]
    public async Task OtherUnauthorizedCodes_LatchTheShipperDead(string code)
    {
        var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Error(HttpStatusCode.Unauthorized, code));
        using var harness = new Harness(handler);
        harness.Outbox.Append(TestData.Envelopes(1));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Fatal, attempt.Outcome);
        Assert.True(harness.Shipper.IsDead);
        Assert.Contains(code, harness.Shipper.DeadReason);

        // A dead shipper never sends again, and never destroys the events it could not ship.
        Assert.Equal(ShipOutcome.Fatal, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(handler.Requests);
        Assert.Equal(1, harness.Outbox.PendingCount);
    }

    /// <summary>409 → mint a new sid, reset seq to 1, abandon the old chain.</summary>
    [Fact]
    public async Task StreamFork_MintsANewStreamAndResetsTheSequence()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Ok(),
            () => FakeHttpHandler.Error(HttpStatusCode.Conflict, Wire.Errors.StreamFork),
            () => FakeHttpHandler.Ok());
        using var harness = new Harness(handler);

        harness.Outbox.Append(TestData.Envelopes(1));
        await harness.Shipper.ShipOnceAsync();
        string originalSid = harness.Shipper.StreamId;

        harness.OpenShipWindow();
        harness.Outbox.Append(TestData.Envelopes(1));
        ShipAttempt forked = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.StreamForked, forked.Outcome);
        Assert.NotEqual(originalSid, harness.Shipper.StreamId);
        Assert.Equal(1, harness.Shipper.Sequence);
        Assert.Equal(1, harness.Outbox.PendingCount);

        // A fork is a parameter change, not a licence to send again inside the window.
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

        // The retry starts a fresh chain: seq 1, no ph.
        harness.OpenShipWindow();
        ShipAttempt retry = await harness.Shipper.ShipOnceAsync();
        Assert.Equal(ShipOutcome.Accepted, retry.Outcome);
        JsonElement claims = handler.Requests[^1].ProofClaims;
        Assert.Equal(1, claims.GetProperty("seq").GetInt64());
        Assert.False(claims.TryGetProperty("ph", out _));
        Assert.Equal(harness.Shipper.StreamId, claims.GetProperty("sid").GetString());
    }

    /// <summary>413 → halve the batch event cap, floor 50, retry.</summary>
    [Fact]
    public async Task TooLarge_HalvesTheBatchCap()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(HttpStatusCode.RequestEntityTooLarge, Wire.Errors.TooLarge),
            () => FakeHttpHandler.Ok());
        using var harness = new Harness(handler, batchEventCap: 400);
        harness.Outbox.Append(TestData.Envelopes(300));

        ShipAttempt tooLarge = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.TooLarge, tooLarge.Outcome);
        Assert.Equal(200, harness.Shipper.BatchEventCap);
        Assert.Equal(300, harness.Outbox.PendingCount);

        // The smaller batch still waits for the window: a 413 does not buy a faster retry, it just
        // makes the next one smaller. Converging 500 → 50 therefore costs four windows, not four
        // round trips back to back, and that is the intended trade.
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

        harness.OpenShipWindow();
        ShipAttempt retry = await harness.Shipper.ShipOnceAsync();
        Assert.Equal(ShipOutcome.Accepted, retry.Outcome);
        Assert.Equal(200, retry.EventsShipped);
    }

    [Fact]
    public async Task TooLarge_AtTheFloor_LatchesDeadRatherThanSpinning()
    {
        var handler = FakeHttpHandler.Always(
            () => FakeHttpHandler.Error(HttpStatusCode.RequestEntityTooLarge, Wire.Errors.TooLarge));
        using var harness = new Harness(handler, batchEventCap: Wire.MinBatchEventCap);
        harness.Outbox.Append(TestData.Envelopes(60));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Fatal, attempt.Outcome);
        Assert.Equal(Wire.MinBatchEventCap, harness.Shipper.BatchEventCap);
        Assert.Equal(60, harness.Outbox.PendingCount);
    }

    [Fact]
    public async Task RateLimited_ReportsTheServersRetryAfter()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.RateLimited(7)));
        harness.Outbox.Append(TestData.Envelopes(1));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.RateLimited, attempt.Outcome);
        Assert.Equal(TimeSpan.FromSeconds(7), attempt.RetryAfter);
        Assert.Equal(1, harness.Outbox.PendingCount);
    }

    [Theory]
    [InlineData(HttpStatusCode.InternalServerError)]
    [InlineData(HttpStatusCode.BadGateway)]
    [InlineData(HttpStatusCode.ServiceUnavailable)]
    public async Task ServerErrors_AreRetryable(HttpStatusCode status)
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Error(status, Wire.Errors.Internal)));
        harness.Outbox.Append(TestData.Envelopes(1));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.ServerError, attempt.Outcome);
        Assert.False(harness.Shipper.IsDead);
        Assert.Equal(1, harness.Outbox.PendingCount);
    }

    [Fact]
    public async Task NetworkFailure_IsRetryableAndKeepsTheEvents()
    {
        var handler = new ThrowingHandler();
        using var harness = new Harness(handler);
        harness.Outbox.Append(TestData.Envelopes(4));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.NetworkError, attempt.Outcome);
        Assert.False(harness.Shipper.IsDead);
        Assert.Equal(4, harness.Outbox.PendingCount);
        Assert.Equal(1, harness.Shipper.Sequence);
    }

    [Theory]
    [InlineData(HttpStatusCode.BadRequest, Wire.Errors.MalformedBatch)]
    [InlineData(HttpStatusCode.UnsupportedMediaType, Wire.Errors.UnsupportedEncoding)]
    public async Task ContractViolations_LatchDeadWithoutDroppingTheBatch(HttpStatusCode status, string code)
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Error(status, code)));
        harness.Outbox.Append(TestData.Envelopes(5));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Fatal, attempt.Outcome);
        Assert.Equal(5, harness.Outbox.PendingCount);
    }

    // ----- idempotent retries (the client half of the contract) -------------------------

    /// <summary>
    /// The client half of the idempotency guarantee: a resend of unchanged bytes must carry the
    /// batch id the server already knows, so it lands on the §4.5.3 step-11 replay short-circuit
    /// rather than falling through to the step-12 stream check and earning a <c>409</c> for a
    /// request that was already safe.
    /// </summary>
    [Fact]
    public async Task RetryingAFailedBatch_ReusesTheBatchId()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal),
            () => FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal),
            () => FakeHttpHandler.Ok(3));
        using var harness = new Harness(handler);
        harness.Outbox.Append(TestData.Envelopes(3));

        Assert.Equal(ShipOutcome.ServerError, (await harness.Shipper.ShipOnceAsync()).Outcome);
        harness.OpenShipWindow();
        Assert.Equal(ShipOutcome.ServerError, (await harness.Shipper.ShipOnceAsync()).Outcome);
        harness.OpenShipWindow();
        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);

        string[] batchIds = [.. handler.Requests.Select(r => r.ProofClaims.GetProperty("jti").GetString()!)];
        Assert.Equal(3, batchIds.Length);
        Assert.Single(batchIds.Distinct());

        // The seq did not move either — three attempts, one batch.
        Assert.All(handler.Requests, r => Assert.Equal(1, r.ProofClaims.GetProperty("seq").GetInt64()));
    }

    /// <summary>
    /// The id is bound to the bytes, not to "there is a batch in flight". A <c>413</c> halving
    /// changes what the batch contains, so it must change the batch id too — reusing it would let
    /// a replay short-circuit retire outbox rows the server never saw.
    /// </summary>
    [Fact]
    public async Task AResizedBatch_GetsAFreshBatchId()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(HttpStatusCode.RequestEntityTooLarge, Wire.Errors.TooLarge),
            () => FakeHttpHandler.Ok(50));
        using var harness = new Harness(handler, batchEventCap: 100);
        harness.Outbox.Append(TestData.Envelopes(100));

        Assert.Equal(ShipOutcome.TooLarge, (await harness.Shipper.ShipOnceAsync()).Outcome);
        harness.OpenShipWindow();
        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);

        Assert.Equal(2, handler.Requests.Count);
        Assert.NotEqual(
            handler.Requests[0].ProofClaims.GetProperty("jti").GetString(),
            handler.Requests[1].ProofClaims.GetProperty("jti").GetString());
        Assert.NotEqual(
            handler.Requests[0].ProofClaims.GetProperty("bh").GetString(),
            handler.Requests[1].ProofClaims.GetProperty("bh").GetString());
    }

    /// <summary>
    /// A game crash mid-ship is the same "did it land?" question as a timeout, only with a process
    /// boundary in the middle — so the batch id has to be durable, not just in memory.
    /// </summary>
    [Fact]
    public async Task TheBatchIdSurvivesAShipperRestart()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            string firstBatchId;
            using (OutboxDb outbox = OutboxDb.Open(path))
            {
                var handler = FakeHttpHandler.Always(
                    () => FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal));
                using var shipper = new BatchShipper(
                    new ShipperOptions(IngestUrl), outbox, credential, handler, new FakeShipperClock());
                outbox.Append(TestData.Envelopes(4));
                await shipper.ShipOnceAsync();
                firstBatchId = handler.Requests[0].ProofClaims.GetProperty("jti").GetString()!;
            }

            using (OutboxDb reopened = OutboxDb.Open(path))
            {
                // Whether the pre-crash request actually committed is unknowable, so the resend is
                // sent under the same id and the server decides: replay if it landed, insert if not.
                // The restart clock is wound past the floor because the floor survives a restart
                // too — see ShipFloorTests.TheFloorSurvivesAProcessRestart.
                var restartClock = new FakeShipperClock();
                restartClock.Advance(ShipFloor);
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok(0, replay: true));
                using var restarted = new BatchShipper(
                    new ShipperOptions(IngestUrl), reopened, credential, handler, restartClock);

                Assert.Equal(ShipOutcome.Replayed, (await restarted.ShipOnceAsync()).Outcome);
                Assert.Equal(firstBatchId, handler.Requests[0].ProofClaims.GetProperty("jti").GetString());
                Assert.Equal(0, reopened.PendingCount);
            }
        }
    }

    /// <summary>
    /// Once a batch is accepted its id is retired: the next batch is a different batch and must say
    /// so, or the server would replay-short-circuit events it has never seen.
    /// </summary>
    [Fact]
    public async Task AnAcceptedBatch_RetiresItsBatchId()
    {
        var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
        using var harness = new Harness(handler);

        harness.Outbox.Append(TestData.Envelopes(2));
        await harness.Shipper.ShipOnceAsync();
        Assert.Null(harness.Outbox.GetState(Wire.StateKeys.PendingBatchId));

        harness.OpenShipWindow();
        harness.Outbox.Append(TestData.Envelopes(2));
        await harness.Shipper.ShipOnceAsync();

        Assert.Equal(2, handler.Requests.Count);
        Assert.NotEqual(
            handler.Requests[0].ProofClaims.GetProperty("jti").GetString(),
            handler.Requests[1].ProofClaims.GetProperty("jti").GetString());
    }

    // ----- triggers, loop, persistence -------------------------------------------------

    [Fact]
    public void ShouldShip_FiresOnThePendingCountTrigger()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));

        Assert.False(harness.Shipper.ShouldShip(), "an empty outbox never triggers");

        harness.Outbox.Append(TestData.Envelopes(Wire.ShipPendingTrigger - 1));
        Assert.False(
            harness.Shipper.ShouldShip(),
            $"{Wire.ShipPendingTrigger - 1} pending is under the {Wire.ShipPendingTrigger}-event safety valve");

        harness.Outbox.Append([TestData.Envelope()]);
        Assert.True(harness.Shipper.ShouldShip());
    }

    /// <summary>
    /// The shipped cadence (§7.2 as retuned): the <b>age</b> trigger is the normal path at ~60 s
    /// and the count trigger is only a safety valve, sized so an ordinary busy minute of play
    /// never reaches it. Passive <c>telemetry.window</c> events are one per vehicle per 30 s, so
    /// a two-dozen-vehicle save emits ~48 a minute; the valve must sit far above that, and far
    /// below the §4.3 2000-event batch cap.
    /// </summary>
    [Fact]
    public void Defaults_MakeTheAgeTriggerTheNormalPath()
    {
        Assert.Equal(60.0, Wire.ShipAgeTriggerSeconds);

        // Three times the ~150 events a busy minute can produce, so the valve stays shut.
        Assert.True(Wire.ShipPendingTrigger >= 450, "the count trigger would fire during ordinary play");

        // Real headroom under the §4.3 caps, and exactly one full batch when it does open.
        Assert.True(Wire.ShipPendingTrigger <= Wire.MaxEventsPerBatch / 2);
        Assert.Equal(Wire.DefaultBatchEventCap, Wire.ShipPendingTrigger);
    }

    /// <summary>
    /// A minute of a busy save must ship on the clock, not on the count: the events pile up
    /// silently until the age trigger fires.
    /// </summary>
    [Fact]
    public void ShouldShip_ABusyMinuteFiresOnAgeNotOnCount()
    {
        var clock = new FakeShipperClock();
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), clock: clock);

        // 24 vehicles × 2 telemetry.window per minute, plus a generous 100 discrete events.
        harness.Outbox.Append(TestData.Envelopes(148, wallMs: clock.UtcNow.ToUnixTimeMilliseconds()));

        clock.Advance(TimeSpan.FromSeconds(Wire.ShipAgeTriggerSeconds - 1));
        Assert.False(harness.Shipper.ShouldShip(), "a busy minute must not trip the count safety valve");

        clock.Advance(TimeSpan.FromSeconds(1));
        Assert.True(harness.Shipper.ShouldShip(), "the age trigger is the normal ship path");
    }

    [Fact]
    public void ShouldShip_FiresOnTheAgeTrigger()
    {
        var clock = new FakeShipperClock();
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), clock: clock);
        harness.Outbox.Append([TestData.Envelope(wallMs: clock.UtcNow.ToUnixTimeMilliseconds())]);

        Assert.False(harness.Shipper.ShouldShip(), "one fresh event is under both triggers");

        clock.Advance(TimeSpan.FromSeconds(Wire.ShipAgeTriggerSeconds));

        Assert.True(
            harness.Shipper.ShouldShip(), $"the oldest event is now {Wire.ShipAgeTriggerSeconds} s old");
    }

    /// <summary>
    /// Offline accumulation: the outbox grows while shipping fails and nothing is lost, and each
    /// failure waits a full-jitter backoff drawn from the doubling ladder — floored, like every
    /// other wait, at <see cref="Wire.MinShipIntervalSeconds"/>.
    /// </summary>
    /// <remarks>
    /// The ladder itself is unchanged and <c>BackoffPolicyTests</c> still pins it to 1, 2, 4, 8 s:
    /// <see cref="BackoffPolicy"/> is the §4.5.3 schedule the contract publishes, and baking a
    /// floor into it would make that published schedule a fiction. The shipper takes the maximum
    /// of the two at the point of waiting, which is why the first rungs all read 30 s here — the
    /// ladder only starts to matter once it climbs past the floor.
    /// </remarks>
    [Fact]
    public async Task RunAsync_BacksOffOnRepeatedFailuresAndLosesNothing()
    {
        var clock = new FakeShipperClock();
        using var cts = new CancellationTokenSource();
        var handler = new FakeHttpHandler((_, index) =>
        {
            if (index >= 4)
                cts.Cancel();
            return FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal);
        });
        // The trigger is pinned so this measures the backoff ladder and nothing else; the shipped
        // ~60 s cadence has its own tests above.
        using var harness = new Harness(handler, clock: clock, jitter: () => 1.0, pendingTrigger: 1);
        harness.Outbox.Append(TestData.Envelopes(100));

        try
        {
            await harness.Shipper.RunAsync(cts.Token);
        }
        catch (OperationCanceledException)
        {
            // Expected: the loop is cancelled from inside the handler.
        }

        // 1, 2, 4 and 8 s are all under the floor, so all four rungs are served as 30 s. The ladder
        // is still climbing underneath: the fifth rung it would draw is 16 s, still floored, and
        // only at the sixth (32 s) does the backoff itself become the binding constraint.
        Assert.Equal([ShipFloor, ShipFloor, ShipFloor, ShipFloor], clock.Delays.Take(4).ToArray());
        Assert.Equal(100, harness.Outbox.PendingCount);
    }

    /// <summary>
    /// The loop used to drain the outbox flat out, batch after batch, with no wait at all between
    /// accepted batches. It cannot any more: a big backlog is shipped one batch per floor window,
    /// and the events that are waiting stay in the outbox rather than being dropped.
    /// </summary>
    [Fact]
    public async Task RunAsync_WaitsAFloorWindowBetweenAcceptedBatches()
    {
        var clock = new FakeShipperClock();
        using var cts = new CancellationTokenSource();
        var handler = new FakeHttpHandler((_, index) =>
        {
            if (index >= 2)
                cts.Cancel();
            return FakeHttpHandler.Ok();
        });
        using var harness = new Harness(
            handler, clock: clock, batchEventCap: Wire.MinBatchEventCap, pendingTrigger: 1);
        harness.Outbox.Append(TestData.Envelopes(200));

        DateTimeOffset started = clock.UtcNow;
        try
        {
            await harness.Shipper.RunAsync(cts.Token);
        }
        catch (OperationCanceledException)
        {
        }

        Assert.True(harness.Handler.Requests.Count >= 2);

        // Whatever mix of poll ticks and throttle waits the loop chose, the outcome is the one
        // that matters: n requests took at least (n − 1) full windows of virtual time.
        TimeSpan elapsed = clock.UtcNow - started;
        Assert.True(
            elapsed >= ShipFloor * (harness.Handler.Requests.Count - 1),
            $"{harness.Handler.Requests.Count} requests in {elapsed.TotalSeconds:0.0} s breaches the floor");
    }

    /// <summary>
    /// The stream is "one outbox instance epoch": restarting the process must continue the same
    /// chain, or the server answers 409 on the first batch after every restart.
    /// </summary>
    [Fact]
    public async Task StreamStateSurvivesAShipperRestart()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            string sid;
            using (OutboxDb outbox = OutboxDb.Open(path))
            {
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
                using var shipper = new BatchShipper(
                    new ShipperOptions(IngestUrl), outbox, credential, handler, new FakeShipperClock());
                outbox.Append(TestData.Envelopes(2));
                await shipper.ShipOnceAsync();
                sid = shipper.StreamId;
                Assert.Equal(2, shipper.Sequence);
            }

            using (OutboxDb reopened = OutboxDb.Open(path))
            {
                var restartClock = new FakeShipperClock();
                restartClock.Advance(ShipFloor); // the floor survives the restart; the chain is what is under test here
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
                using var restarted = new BatchShipper(
                    new ShipperOptions(IngestUrl), reopened, credential, handler, restartClock);

                Assert.Equal(sid, restarted.StreamId);
                Assert.Equal(2, restarted.Sequence);

                reopened.Append(TestData.Envelopes(1));
                await restarted.ShipOnceAsync();

                JsonElement claims = handler.Requests[0].ProofClaims;
                Assert.Equal(2, claims.GetProperty("seq").GetInt64());
                Assert.True(claims.TryGetProperty("ph", out _), "the chain continues across a restart");
            }
        }
    }

    [Fact]
    public async Task ClockOffsetSurvivesAShipperRestart()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            DateTimeOffset localNow = DateTimeOffset.UnixEpoch.AddSeconds(1_770_000_000);
            using (OutboxDb outbox = OutboxDb.Open(path))
            {
                var handler = FakeHttpHandler.Scripted(
                    () => FakeHttpHandler.ClockSkew(localNow.AddMinutes(20)),
                    () => FakeHttpHandler.Ok());
                using var shipper = new BatchShipper(
                    new ShipperOptions(IngestUrl), outbox, credential, handler, new FakeShipperClock(localNow));
                outbox.Append(TestData.Envelopes(1));
                await shipper.ShipOnceAsync();
            }

            using (OutboxDb reopened = OutboxDb.Open(path))
            {
                using var restarted = new BatchShipper(
                    new ShipperOptions(IngestUrl), reopened, credential,
                    FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), new FakeShipperClock(localNow));

                Assert.Equal((long)TimeSpan.FromMinutes(20).TotalMilliseconds, restarted.ClockOffsetMs);
            }
        }
    }

    // ----- what the SERVER said, and the retry ladder (WP8 lib fixes) ------------------

    /// <summary>
    /// The §4.4 <c>200</c> body carries the server's own counts; the status window has to be able
    /// to say what the server did with the batch, not just how many rows this client sent.
    /// </summary>
    [Fact]
    public async Task AcceptedBatch_ReportsTheServersAcceptedAndDedupedCounts()
    {
        using var harness = new Harness(FakeHttpHandler.Always(
            () => FakeHttpHandler.Counts(accepted: 7, deduped: 3)));
        harness.Outbox.Append(TestData.Envelopes(10));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Accepted, attempt.Outcome);
        Assert.Equal(10, attempt.EventsShipped);
        Assert.Equal(7, attempt.ServerAccepted);
        Assert.Equal(3, attempt.ServerDeduped);
    }

    [Fact]
    public async Task Replay_ReportsZeroAcceptedAndTheDedupedCount()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok(4, replay: true)));
        harness.Outbox.Append(TestData.Envelopes(4));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Replayed, attempt.Outcome);
        Assert.Equal(0, attempt.ServerAccepted);
        Assert.Equal(4, attempt.ServerDeduped);
    }

    /// <summary>A body without the counts must read as "the server did not say", never as zero.</summary>
    [Fact]
    public async Task MissingCounts_AreNullRatherThanZero()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Empty200()));
        harness.Outbox.Append(TestData.Envelopes(2));

        ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();

        Assert.Equal(ShipOutcome.Accepted, attempt.Outcome);
        Assert.Null(attempt.ServerAccepted);
        Assert.Null(attempt.ServerDeduped);
    }

    /// <summary>
    /// A caller that pumps <see cref="BatchShipper.ShipOnceAsync"/> itself (the simulator, the
    /// integration tests) must see the retry ladder advance — it used to be advanced only by
    /// <see cref="BatchShipper.RunAsync"/>, so such a caller read a permanent zero.
    /// </summary>
    [Fact]
    public async Task ShipOnceAsync_AdvancesConsecutiveFailures()
    {
        using var harness = new Harness(FakeHttpHandler.Always(
            () => FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal)));
        harness.Outbox.Append(TestData.Envelopes(3));

        Assert.Equal(0, harness.Shipper.ConsecutiveFailures);
        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(1, harness.Shipper.ConsecutiveFailures);
        harness.OpenShipWindow();
        await harness.Shipper.ShipOnceAsync();
        harness.OpenShipWindow();
        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(3, harness.Shipper.ConsecutiveFailures);
    }

    /// <summary>
    /// A throttled call is not a failure and must not climb the ladder — otherwise a shipper that
    /// merely had nothing it was allowed to send yet would come back with a five-minute backoff
    /// waiting for it.
    /// </summary>
    [Fact]
    public async Task Throttled_LeavesTheRetryLadderAndTheLastAttemptAlone()
    {
        using var harness = new Harness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok(2)));
        harness.Outbox.Append(TestData.Envelopes(4));

        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);
        ShipAttempt? accepted = harness.Shipper.LastAttempt;

        for (int i = 0; i < 5; i++)
            Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

        Assert.Equal(0, harness.Shipper.ConsecutiveFailures);

        // The status window still reads the last real attempt, not a wall of refusals.
        Assert.Same(accepted, harness.Shipper.LastAttempt);
    }

    [Fact]
    public async Task ShipOnceAsync_ResetsConsecutiveFailuresOnSuccessAndOnAnEmptyOutbox()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(HttpStatusCode.BadGateway, Wire.Errors.Internal),
            () => FakeHttpHandler.Error(HttpStatusCode.BadGateway, Wire.Errors.Internal),
            () => FakeHttpHandler.Ok(2));
        using var harness = new Harness(handler);
        harness.Outbox.Append(TestData.Envelopes(2));

        await harness.Shipper.ShipOnceAsync();
        harness.OpenShipWindow();
        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(2, harness.Shipper.ConsecutiveFailures);

        harness.OpenShipWindow();
        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(0, harness.Shipper.ConsecutiveFailures);

        // NothingToShip is not a fault either.
        harness.OpenShipWindow();
        Assert.Equal(ShipOutcome.NothingToShip, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(0, harness.Shipper.ConsecutiveFailures);
    }

    /// <summary>
    /// A 413 changes the batch parameters rather than failing transport, so it must neither advance
    /// nor reset the ladder — otherwise an oversize batch in the middle of a bad-network patch
    /// silently restarts the backoff the next real failure is owed.
    /// </summary>
    [Fact]
    public async Task TooLarge_LeavesTheRetryLadderWhereItIs()
    {
        var handler = FakeHttpHandler.Scripted(
            () => FakeHttpHandler.Error(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal),
            () => FakeHttpHandler.Error(HttpStatusCode.RequestEntityTooLarge, Wire.Errors.TooLarge));
        using var harness = new Harness(handler, batchEventCap: 400);
        harness.Outbox.Append(TestData.Envelopes(300));

        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(1, harness.Shipper.ConsecutiveFailures);

        harness.OpenShipWindow();
        Assert.Equal(ShipOutcome.TooLarge, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(1, harness.Shipper.ConsecutiveFailures);
    }

    private sealed class ThrowingHandler : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
            => throw new HttpRequestException("connection refused");
    }

    private sealed class Harness : IDisposable
    {
        private readonly TempDir _dir = new();
        private readonly ECDsa _serverKey;

        internal Harness(
            HttpMessageHandler handler,
            FakeShipperClock? clock = null,
            int batchEventCap = Wire.DefaultBatchEventCap,
            Func<double>? jitter = null,
            int pendingTrigger = Wire.ShipPendingTrigger,
            double ageTriggerSeconds = Wire.ShipAgeTriggerSeconds)
        {
            (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
            Credential = credential;
            _serverKey = serverKey;
            Handler = handler as FakeHttpHandler ?? new FakeHttpHandler((_, _) => FakeHttpHandler.Ok());
            Clock = clock ?? new FakeShipperClock();
            Outbox = OutboxDb.Open(_dir.File("outbox.db"));
            Shipper = new BatchShipper(
                new ShipperOptions(
                    IngestUrl,
                    BatchEventCap: batchEventCap,
                    PendingTrigger: pendingTrigger,
                    AgeTriggerSeconds: ageTriggerSeconds,
                    OutboxCapBytes: 0),
                Outbox,
                Credential,
                handler,
                Clock,
                jitter ?? (static () => 1.0));
        }

        internal Credential Credential { get; }

        internal OutboxDb Outbox { get; }

        internal BatchShipper Shipper { get; }

        internal FakeHttpHandler Handler { get; }

        internal FakeShipperClock Clock { get; }

        /// <summary>
        /// Winds the virtual clock past the hard <see cref="Wire.MinShipIntervalSeconds"/> floor,
        /// so the shipper is allowed to issue another request.
        /// </summary>
        /// <remarks>
        /// Every test below that ships more than once has to call this, and that is the floor
        /// doing its job rather than the tests routing around it: without it the second request
        /// comes back <see cref="ShipOutcome.Throttled"/> and nothing goes on the wire.
        /// <c>ShipFloorTests</c> is where that refusal is asserted directly.
        /// </remarks>
        internal void OpenShipWindow() => Clock.Advance(ShipFloor);

        public void Dispose()
        {
            Shipper.Dispose();
            Outbox.Dispose();
            Credential.Dispose();
            _serverKey.Dispose();
            _dir.Dispose();
        }
    }
}
