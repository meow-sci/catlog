using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Security.Cryptography;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Config;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Ship;

/// <summary>
/// The hard, non-overridable reporting floor: the mod never issues two requests to the ingest
/// endpoint less than <see cref="Wire.MinShipIntervalSeconds"/> apart, whatever
/// <c>catlog.toml</c> says, whatever the outbox holds, and whichever recovery path is running.
/// </summary>
/// <remarks>
/// <para>
/// <b>The threat this closes.</b> <c>catlog.toml</c> is a text file in the player's mod folder.
/// Before the floor, <c>ship_interval_s = 1</c> was a one-line edit that turned a stock install
/// into a firehose. Recompiling the assembly can still do anything — that is out of scope, and
/// always will be — but the easy path is shut.
/// </para>
/// <para>
/// Every test here runs on a virtual clock, so the suite proves a 30-second property in
/// milliseconds. The clock is injected through <see cref="IShipperClock"/>, which is a test seam
/// and nothing else: the shipped mod constructs its shipper without one and gets the real clock,
/// and <see cref="ClockSeamTests"/> pins that.
/// </para>
/// </remarks>
public sealed class ShipFloorTests
{
    private const string IngestUrl = "http://127.0.0.1:8080/v1/ingest";

    private static readonly TimeSpan Floor = TimeSpan.FromSeconds(Wire.MinShipIntervalSeconds);

    // ----- the constant itself ----------------------------------------------------------

    /// <summary>
    /// The floor is a compile-time constant and it is thirty seconds. This test exists so that
    /// changing it is a deliberate act with a failing test attached, not a quiet edit.
    /// </summary>
    [Fact]
    public void TheFloorIsAThirtySecondCompileTimeConstant()
    {
        Assert.Equal(30.0, Wire.MinShipIntervalSeconds);

        // A const, not a static readonly: it has to be unreachable from configuration, and being
        // baked into every call site at compile time is part of how that is guaranteed.
        Assert.True(
            typeof(Wire).GetField(nameof(Wire.MinShipIntervalSeconds))!.IsLiteral,
            "the floor must be a const so no code path can assign it at run time");

        // It never binds during ordinary play: the shipped cadence is already twice as slow.
        Assert.True(Wire.ShipAgeTriggerSeconds >= Wire.MinShipIntervalSeconds);
    }

    // ----- the config can never reach under it -------------------------------------------

    /// <summary>
    /// The headline case, end to end from a real TOML file: a player writes
    /// <c>ship_interval_s = 1</c>, and the shipper the mod builds from that file ships at 30.
    /// </summary>
    [Fact]
    public void AOneSecondIntervalInTomlBecomesThirty()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        File.WriteAllText(
            path,
            """
            schema = 1
            enabled = true
            ingest_url = "http://127.0.0.1:8080/v1/ingest"
            credential_path = "/tmp/catlog-credential.json"
            ship_interval_s = 1.0
            ship_max_pending = 50
            """);

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.Equal(Wire.MinShipIntervalSeconds, config.ShipIntervalS);

        // And the value that reaches the shipper is the clamped one, not the file's.
        var options = new ShipperOptions(
            config.IngestUrl,
            PendingTrigger: config.ShipMaxPending,
            AgeTriggerSeconds: config.ShipIntervalS);
        Assert.Equal(Wire.MinShipIntervalSeconds, options.AgeTriggerSeconds);
    }

    [Theory]
    [InlineData(0.0)]
    [InlineData(-1.0)]
    [InlineData(-86_400.0)]
    [InlineData(0.001)]
    [InlineData(double.NaN)]
    [InlineData(double.NegativeInfinity)]
    [InlineData(double.PositiveInfinity)]
    [InlineData(double.MaxValue)]
    [InlineData(1e300)]
    public void NoIntervalValueEverClampsBelowTheFloor(double input)
    {
        var config = new ModConfig { ShipIntervalS = input };

        config.Normalize();

        Assert.InRange(config.ShipIntervalS, Wire.MinShipIntervalSeconds, Wire.MaxShipAgeTriggerSeconds);
    }

    /// <summary>
    /// There is no second knob. No key in <c>catlog.toml</c> raises or lowers the floor, so a
    /// file that tries to name one is simply ignored — Tomlyn drops unknown keys and the floor is
    /// not a property to begin with.
    /// </summary>
    [Fact]
    public void NoTomlKeyCanNameTheFloor()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        File.WriteAllText(
            path,
            """
            schema = 1
            ship_interval_s = 1.0
            min_ship_interval_s = 0.1
            ship_floor_s = 0
            min_ship_interval_seconds = 0
            disable_rate_floor = true
            """);

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.Equal(Wire.MinShipIntervalSeconds, config.ShipIntervalS);
        Assert.Equal(30.0, Wire.MinShipIntervalSeconds);

        // The serialized round trip cannot carry such a key either: the floor is not a member.
        string written = config.Serialize();
        Assert.DoesNotContain("min_ship_interval", written, StringComparison.OrdinalIgnoreCase);
        Assert.DoesNotContain("floor", written, StringComparison.OrdinalIgnoreCase);

        // And the file the player reads says so in as many words.
        Assert.Contains("HARD MINIMUM of 30 seconds", written, StringComparison.Ordinal);
    }

    // ----- defence in depth: the clamp is not what enforces it ---------------------------

    /// <summary>
    /// The clamp is a courtesy. This is the guarantee: a <see cref="ShipperOptions"/> built by
    /// hand, bypassing <see cref="ModConfig"/> entirely, with a one-second age trigger and a
    /// one-event count trigger, still cannot send twice inside a window.
    /// </summary>
    [Fact]
    public async Task ADirectlyConstructedShipperOptions_IsStillFloored()
    {
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Ok()),
            new ShipperOptions(
                IngestUrl,
                PendingTrigger: 1,
                AgeTriggerSeconds: 1.0,
                PollSeconds: 0.001,
                OutboxCapBytes: 0));
        harness.Outbox.Append(TestData.Envelopes(10));

        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);

        harness.Outbox.Append(TestData.Envelopes(10));
        harness.Clock.Advance(TimeSpan.FromSeconds(29));

        // The age trigger says yes, the count trigger says yes, and the answer is still no.
        Assert.False(harness.Shipper.ShouldShip());
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(harness.Handler.Requests);

        harness.Clock.Advance(TimeSpan.FromSeconds(1));
        Assert.True(harness.Shipper.ShouldShip());
        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(2, harness.Handler.Requests.Count);
    }

    [Fact]
    public async Task TheWindowOpensAtExactlyTheFloorAndNotAMillisecondSooner()
    {
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), batchEventCap: Wire.MinBatchEventCap);
        harness.Outbox.Append(TestData.Envelopes(200));

        await harness.Shipper.ShipOnceAsync();

        harness.Clock.Advance(Floor - TimeSpan.FromMilliseconds(1));
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

        harness.Clock.Advance(TimeSpan.FromMilliseconds(1));
        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);
    }

    // ----- the count trigger cannot open it ----------------------------------------------

    /// <summary>
    /// Ten thousand events — twenty times <see cref="Wire.ShipPendingTrigger"/> — still go out one
    /// batch per window, and nothing is dropped on the floor while they wait. Buffering is what
    /// the outbox is for.
    /// </summary>
    [Fact]
    public async Task ABurstFarOverTheCountTrigger_ShipsOneBatchPerWindowAndLosesNothing()
    {
        const int burst = 10_000;
        using var harness = new FloorHarness(new FakeHttpHandler((request, _) =>
            FakeHttpHandler.Ok(CountLines(request.Ndjson))));
        harness.Outbox.Append(TestData.Envelopes(burst));

        Assert.True(harness.Outbox.PendingCount >= Wire.ShipPendingTrigger * 20);

        int shipped = 0;
        for (int window = 0; window < 8; window++)
        {
            ShipAttempt attempt = await harness.Shipper.ShipOnceAsync();
            Assert.Equal(ShipOutcome.Accepted, attempt.Outcome);
            shipped += attempt.EventsShipped;

            // Hammering inside the window achieves nothing at all.
            for (int i = 0; i < 25; i++)
                Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

            harness.Clock.Advance(Floor);
        }

        Assert.Equal(8, harness.Handler.Requests.Count);
        Assert.Equal(burst, shipped + harness.Outbox.PendingCount);
        Assert.True(harness.Outbox.PendingCount > 0, "the rest of the burst is still queued, not lost");
    }

    // ----- the recovery paths --------------------------------------------------------------

    /// <summary>
    /// §4.5.3's recovery table, one row at a time: whatever the server says, the next request is
    /// never sooner than the floor. <c>409</c> and <c>413</c> used to retry immediately and
    /// <c>429</c>/<c>5xx</c> backoff used to start at one second; all four now wait.
    /// </summary>
    [Theory]
    [InlineData(HttpStatusCode.Conflict, Wire.Errors.StreamFork, ShipOutcome.StreamForked)]
    [InlineData(HttpStatusCode.RequestEntityTooLarge, Wire.Errors.TooLarge, ShipOutcome.TooLarge)]
    [InlineData(HttpStatusCode.TooManyRequests, Wire.Errors.RateLimited, ShipOutcome.RateLimited)]
    [InlineData(HttpStatusCode.InternalServerError, Wire.Errors.Internal, ShipOutcome.ServerError)]
    [InlineData(HttpStatusCode.ServiceUnavailable, Wire.Errors.Internal, ShipOutcome.ServerError)]
    [InlineData(HttpStatusCode.BadGateway, Wire.Errors.Internal, ShipOutcome.ServerError)]
    public async Task EveryRecoveryPath_WaitsForTheWindowBeforeRetrying(
        HttpStatusCode status, string code, ShipOutcome expected)
    {
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Error(status, code)),
            batchEventCap: 400);
        harness.Outbox.Append(TestData.Envelopes(300));

        Assert.Equal(expected, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(harness.Handler.Requests);

        for (int i = 0; i < 10; i++)
        {
            harness.Clock.Advance(TimeSpan.FromSeconds(2));
            Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        }

        // Ten attempts over twenty seconds bought exactly nothing.
        Assert.Single(harness.Handler.Requests);
        Assert.Equal(300, harness.Outbox.PendingCount);

        harness.Clock.Advance(Floor);
        await harness.Shipper.ShipOnceAsync();
        Assert.Equal(2, harness.Handler.Requests.Count);
    }

    /// <summary>
    /// A server asking to be hit sooner than the floor does not get its way. <c>Retry-After</c> is
    /// advice; the floor is not.
    /// </summary>
    [Fact]
    public async Task AShortRetryAfter_IsRaisedToTheFloor()
    {
        var clock = new FakeShipperClock();
        using var cts = new CancellationTokenSource();
        var handler = new FakeHttpHandler((_, index) =>
        {
            if (index >= 2)
                cts.Cancel();
            return FakeHttpHandler.RateLimited(1);
        });
        using var harness = new FloorHarness(handler, clock: clock, pendingTrigger: 1);
        harness.Outbox.Append(TestData.Envelopes(50));

        try
        {
            await harness.Shipper.RunAsync(cts.Token);
        }
        catch (OperationCanceledException)
        {
            // The handler cancels the loop; expected.
        }

        Assert.All(clock.Delays, delay => Assert.True(
            delay >= Floor || delay <= TimeSpan.FromSeconds(1.0),
            $"a {delay.TotalSeconds:0.###} s wait is neither a poll tick nor a floored backoff"));
        Assert.Contains(clock.Delays, delay => delay >= Floor);
    }

    /// <summary>
    /// A locally-detected oversize body never reaches the network, so it is not a request — but
    /// the halved retry that follows it is, and that one waits.
    /// </summary>
    [Fact]
    public async Task ALocallyDetectedOversizeBatch_StillWaitsBeforeItsRetry()
    {
        using var harness = new FloorHarness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));
        harness.Outbox.Append(TestData.Envelopes(100));

        await harness.Shipper.ShipOnceAsync();
        Assert.Single(harness.Handler.Requests);

        harness.Outbox.Append(TestData.Envelopes(100));
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(harness.Handler.Requests);
    }

    // ----- the long run ---------------------------------------------------------------------

    /// <summary>
    /// The property the owner actually asked for, stated as arithmetic over a long virtualised
    /// run: however many events arrive and however often the loop is pumped, the number of
    /// requests never exceeds one per <see cref="Wire.MinShipIntervalSeconds"/> of elapsed time.
    /// </summary>
    [Fact]
    public async Task OverALongRun_RequestsNeverExceedElapsedOverTheFloor()
    {
        const int minutes = 90;
        using var harness = new FloorHarness(
            new FakeHttpHandler((request, _) => FakeHttpHandler.Ok(CountLines(request.Ndjson))),
            new ShipperOptions(
                IngestUrl,
                // Every knob turned to its most aggressive setting, and one of them past what the
                // config clamp would even permit.
                BatchEventCap: Wire.MinBatchEventCap,
                PendingTrigger: 1,
                AgeTriggerSeconds: 0.001,
                PollSeconds: 0.001,
                OutboxCapBytes: 0));

        DateTimeOffset started = harness.Clock.UtcNow;

        // 90 simulated minutes, pumped four times a second — 21 600 chances to send.
        for (int tick = 0; tick < minutes * 60 * 4; tick++)
        {
            harness.Outbox.Append(TestData.Envelopes(3, wallMs: harness.Clock.UtcNow.ToUnixTimeMilliseconds()));
            if (harness.Shipper.ShouldShip())
                await harness.Shipper.ShipOnceAsync();
            await harness.Shipper.ShipOnceAsync(); // and a caller that ignores ShouldShip entirely
            harness.Clock.Advance(TimeSpan.FromMilliseconds(250));
        }

        TimeSpan elapsed = harness.Clock.UtcNow - started;
        int ceiling = (int)Math.Ceiling(elapsed.TotalSeconds / Wire.MinShipIntervalSeconds);

        Assert.True(
            harness.Handler.Requests.Count <= ceiling,
            $"{harness.Handler.Requests.Count} requests in {elapsed.TotalMinutes:0} minutes exceeds the "
            + $"{ceiling}-request ceiling the {Wire.MinShipIntervalSeconds:0} s floor allows");

        // Not vacuous: it did keep shipping the whole time, it just did it at the floor.
        Assert.True(harness.Handler.Requests.Count >= ceiling - 1);
    }

    // ----- persistence ------------------------------------------------------------------------

    /// <summary>
    /// Quitting and relaunching is not a way around the floor. The stamp lives in
    /// <c>shipper_state</c> next to <c>sid</c> and <c>seq</c>, so a fresh session inherits the
    /// window it was born into.
    /// </summary>
    /// <remarks>
    /// Persisting it was the deliberate choice over keeping it in memory. In memory, a player who
    /// was willing to restart the game would have had a general bypass for ordinary shipping; on
    /// disk, the only thing a restart resets is the outbox it also deletes. The cost is that the
    /// comparison spans a process boundary and therefore rests on the local wall clock rather than
    /// on a monotonic source — see <see cref="AClockRolledBackwards_CostsAWindowAndHealsItself"/>
    /// for how that is handled. It is never compared against the server-learned offset, so a
    /// hostile <c>Date</c> header cannot shorten a window.
    /// </remarks>
    [Fact]
    public async Task TheFloorSurvivesAProcessRestart()
    {
        using var dir = new TempDir();
        string path = dir.File("outbox.db");
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            var clock = new FakeShipperClock();

            using (OutboxDb outbox = OutboxDb.Open(path))
            {
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
                using var shipper = new BatchShipper(
                    new ShipperOptions(IngestUrl, OutboxCapBytes: 0), outbox, credential, handler, clock);
                outbox.Append(TestData.Envelopes(4));
                Assert.Equal(ShipOutcome.Accepted, (await shipper.ShipOnceAsync()).Outcome);
            }

            Assert.Equal(
                clock.UtcNow.ToUnixTimeMilliseconds(),
                long.Parse(ReadState(path, Wire.StateKeys.LastRequestMs)!, System.Globalization.CultureInfo.InvariantCulture));

            // Relaunch one second later, five times over. Each new session reads the stamp.
            for (int relaunch = 0; relaunch < 5; relaunch++)
            {
                clock.Advance(TimeSpan.FromSeconds(1));
                using OutboxDb reopened = OutboxDb.Open(path);
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
                using var restarted = new BatchShipper(
                    new ShipperOptions(IngestUrl, OutboxCapBytes: 0), reopened, credential, handler, clock);

                reopened.Append(TestData.Envelopes(4));
                Assert.Equal(ShipOutcome.Throttled, (await restarted.ShipOnceAsync()).Outcome);
                Assert.Empty(handler.Requests);
            }

            clock.Advance(Floor);
            using (OutboxDb reopened = OutboxDb.Open(path))
            {
                var handler = FakeHttpHandler.Always(() => FakeHttpHandler.Ok());
                using var restarted = new BatchShipper(
                    new ShipperOptions(IngestUrl, OutboxCapBytes: 0), reopened, credential, handler, clock);

                Assert.Equal(ShipOutcome.Accepted, (await restarted.ShipOnceAsync()).Outcome);
            }
        }
    }

    /// <summary>
    /// Winding the system clock backwards restarts the window rather than blocking shipping until
    /// the calendar catches up — and it never buys a shorter one.
    /// </summary>
    [Fact]
    public async Task AClockRolledBackwards_CostsAWindowAndHealsItself()
    {
        var clock = new FakeShipperClock();
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Ok()),
            clock: clock,
            batchEventCap: Wire.MinBatchEventCap);
        harness.Outbox.Append(TestData.Envelopes(100));

        await harness.Shipper.ShipOnceAsync();

        clock.UtcNow -= TimeSpan.FromDays(365);
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Single(harness.Handler.Requests);

        // One window later — measured from the rolled-back "now", not from a year in the future.
        clock.Advance(Floor);
        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(2, harness.Handler.Requests.Count);
    }

    // ----- the shutdown flush ------------------------------------------------------------------

    /// <summary>
    /// <c>FinalShip</c> is the one narrow exemption: a courtesy flush that fires at most once per
    /// game session, so exploiting it costs a full quit and relaunch of KSA — far more than the
    /// 30 s it saves.
    /// </summary>
    [Fact]
    public async Task FinalShip_IsExemptFromTheFloorButStillStampsIt()
    {
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), batchEventCap: Wire.MinBatchEventCap);
        harness.Outbox.Append(TestData.Envelopes(200));

        Assert.Equal(ShipOutcome.Accepted, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);

        // The window is firmly shut, and the shutdown flush goes anyway.
        Assert.True(harness.Shipper.ThrottleRemaining > TimeSpan.Zero);
        ShipAttempt final = harness.Shipper.FinalShip(TimeSpan.FromSeconds(5));

        Assert.Equal(ShipOutcome.Accepted, final.Outcome);
        Assert.Equal(2, harness.Handler.Requests.Count);

        // It is still a request, so the next window starts from it: the exemption buys one batch
        // on the way out, not a reset.
        Assert.Equal(Floor, harness.Shipper.ThrottleRemaining);
        Assert.Equal(ShipOutcome.Throttled, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.Equal(2, harness.Handler.Requests.Count);
    }

    /// <summary>One attempt. Not a drain loop, not a retry ladder.</summary>
    [Fact]
    public void FinalShip_MakesExactlyOneAttemptEvenWithAFullOutbox()
    {
        using var harness = new FloorHarness(
            FakeHttpHandler.Always(() => FakeHttpHandler.Ok()), batchEventCap: Wire.MinBatchEventCap);
        harness.Outbox.Append(TestData.Envelopes(1_000));

        harness.Shipper.FinalShip(TimeSpan.FromSeconds(5));

        Assert.Single(harness.Handler.Requests);
        Assert.Equal(950, harness.Outbox.PendingCount); // the rest waits for the next run
    }

    /// <summary>
    /// The failure mode that would be invisible in testing and infuriating in the wild: a server
    /// that accepts the connection and then never answers. Shutdown must not wait on it.
    /// </summary>
    [Fact]
    public void FinalShip_DoesNotHangWhenTheServerNeverResponds()
    {
        using var handler = new HangingHandler();
        using var harness = new FloorHarness(handler);
        harness.Outbox.Append(TestData.Envelopes(20));

        var stopwatch = Stopwatch.StartNew();
        ShipAttempt attempt = harness.Shipper.FinalShip(TimeSpan.FromMilliseconds(250));
        stopwatch.Stop();

        Assert.Equal(ShipOutcome.NetworkError, attempt.Outcome);
        Assert.True(
            stopwatch.Elapsed < TimeSpan.FromSeconds(5),
            $"the shutdown flush blocked for {stopwatch.Elapsed.TotalSeconds:0.0} s against a hung server");
        Assert.Equal(20, harness.Outbox.PendingCount); // nothing lost; the next run picks it up
    }

    [Fact]
    public void FinalShip_DoesNotThrowWhenTheServerIsUnreachable()
    {
        using var harness = new FloorHarness(new RefusingHandler());
        harness.Outbox.Append(TestData.Envelopes(20));

        var stopwatch = Stopwatch.StartNew();
        ShipAttempt attempt = harness.Shipper.FinalShip(TimeSpan.FromSeconds(2));
        stopwatch.Stop();

        Assert.Equal(ShipOutcome.NetworkError, attempt.Outcome);
        Assert.True(stopwatch.Elapsed < TimeSpan.FromSeconds(2));
        Assert.Equal(20, harness.Outbox.PendingCount);
    }

    [Fact]
    public async Task FinalShip_IsANoOpOnADeadLatchedShipper()
    {
        using var harness = new FloorHarness(FakeHttpHandler.Always(
            () => FakeHttpHandler.Error(HttpStatusCode.Unauthorized, Wire.Errors.LicenseRevoked)));
        harness.Outbox.Append(TestData.Envelopes(20));

        Assert.Equal(ShipOutcome.Fatal, (await harness.Shipper.ShipOnceAsync()).Outcome);
        Assert.True(harness.Shipper.IsDead);

        ShipAttempt attempt = harness.Shipper.FinalShip(TimeSpan.FromSeconds(2));

        Assert.Equal(ShipOutcome.Fatal, attempt.Outcome);
        Assert.Single(harness.Handler.Requests);
        Assert.Equal(20, harness.Outbox.PendingCount);
    }

    [Fact]
    public void FinalShip_OnAnEmptyOutbox_SaysSoAndSendsNothing()
    {
        using var harness = new FloorHarness(FakeHttpHandler.Always(() => FakeHttpHandler.Ok()));

        ShipAttempt attempt = harness.Shipper.FinalShip(TimeSpan.FromSeconds(2));

        Assert.Equal(ShipOutcome.NothingToShip, attempt.Outcome);
        Assert.Empty(harness.Handler.Requests);
    }

    private static string? ReadState(string path, string key)
    {
        using OutboxDb outbox = OutboxDb.Open(path);
        return outbox.GetState(key);
    }

    private static int CountLines(string ndjson)
        => ndjson.Split('\n', StringSplitOptions.RemoveEmptyEntries).Length;

    /// <summary>A transport that accepts the request and never answers, until it is cancelled.</summary>
    private sealed class HangingHandler : HttpMessageHandler
    {
        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken).ConfigureAwait(false);
            throw new UnreachableException();
        }
    }

    /// <summary>A transport that fails the way a dead server does.</summary>
    private sealed class RefusingHandler : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
            => throw new HttpRequestException("connection refused");
    }

    private sealed class FloorHarness : IDisposable
    {
        private readonly TempDir _dir = new();
        private readonly ECDsa _serverKey;
        private readonly Credential _credential;

        internal FloorHarness(
            HttpMessageHandler handler,
            ShipperOptions? options = null,
            FakeShipperClock? clock = null,
            int batchEventCap = Wire.DefaultBatchEventCap,
            int pendingTrigger = Wire.ShipPendingTrigger)
        {
            (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
            _credential = credential;
            _serverKey = serverKey;
            Handler = handler as FakeHttpHandler ?? new FakeHttpHandler((_, _) => FakeHttpHandler.Ok());
            Clock = clock ?? new FakeShipperClock();
            Outbox = OutboxDb.Open(_dir.File("outbox.db"));
            Shipper = new BatchShipper(
                options ?? new ShipperOptions(
                    IngestUrl,
                    BatchEventCap: batchEventCap,
                    PendingTrigger: pendingTrigger,
                    OutboxCapBytes: 0),
                Outbox,
                _credential,
                handler,
                Clock,
                static () => 1.0);
        }

        internal OutboxDb Outbox { get; }

        internal BatchShipper Shipper { get; }

        internal FakeHttpHandler Handler { get; }

        internal FakeShipperClock Clock { get; }

        public void Dispose()
        {
            Shipper.Dispose();
            Outbox.Dispose();
            _credential.Dispose();
            _serverKey.Dispose();
            _dir.Dispose();
        }
    }
}

/// <summary>
/// The clock-injection seam is a test seam and nothing else: the shipped game mod must construct
/// its shipper with the real clock, unconditionally.
/// </summary>
/// <remarks>
/// These assertions read <c>mod/catlog</c>'s <b>source</b>, because that is where the property
/// actually lives — the game project cannot be loaded here (it references KSA, which
/// <c>AssemblyGuardTests</c> keeps out of this assembly), and "no config path reaches this
/// parameter" is a statement about what the code says rather than about what one call returns.
/// They skip rather than fail when the repository is not on disk, exactly as the conformance
/// vectors do.
/// </remarks>
public sealed class ClockSeamTests
{
    /// <summary>
    /// The default is the safe thing: omit the clock and you get the real one. A future refactor
    /// that made the parameter required would push the decision out to every call site, which is
    /// precisely what must not happen.
    /// </summary>
    [Fact]
    public void OmittingTheClockYieldsTheRealClock()
    {
        using var dir = new TempDir();
        (Credential credential, ECDsa serverKey, _) = TestKeys.Credential();
        using (credential)
        using (serverKey)
        {
            using OutboxDb outbox = OutboxDb.Open(dir.File("outbox.db"));
            using var shipper = new BatchShipper(
                new ShipperOptions("http://127.0.0.1:8080/v1/ingest", OutboxCapBytes: 0), outbox, credential);

            // Now is the real clock plus a zero offset, so it tracks wall time.
            Assert.InRange(
                (shipper.Now - DateTimeOffset.UtcNow).Duration(),
                TimeSpan.Zero,
                TimeSpan.FromSeconds(5));
        }
    }

    /// <summary>
    /// <c>mod/catlog</c> — everything a player installs — never names the clock seam. No
    /// <see cref="IShipperClock"/>, no <c>clock:</c> argument, no substitute implementation. The
    /// floor it measures is therefore always the real thirty seconds.
    /// </summary>
    [GameModSourceFact]
    public void TheShippedModNeverInjectsAClock()
    {
        string[] sources = GameModSources();
        var offenders = new List<string>();
        foreach (string file in sources)
        {
            string code = StripComments(File.ReadAllText(file));
            if (code.Contains("IShipperClock", StringComparison.Ordinal)
                || code.Contains("clock:", StringComparison.Ordinal)
                || code.Contains("ShipperClock", StringComparison.Ordinal))
            {
                offenders.Add(Path.GetFileName(file));
            }
        }

        Assert.True(
            offenders.Count == 0,
            "the shipped mod must construct BatchShipper with the default (real) clock and never "
            + "name the seam, but these files do: " + string.Join(", ", offenders));
    }

    /// <summary>
    /// And there is nothing else in the shipped mod that could reach a seam either: no environment
    /// variable and no command-line switch is read anywhere in it. Every knob is
    /// <c>catlog.toml</c>, and <c>catlog.toml</c> cannot express the floor.
    /// </summary>
    [GameModSourceFact]
    public void TheShippedModReadsNoEnvironmentVariablesOrCommandLine()
    {
        string[] sources = GameModSources();
        var offenders = new List<string>();
        foreach (string file in sources)
        {
            string code = StripComments(File.ReadAllText(file));
            if (code.Contains("GetEnvironmentVariable", StringComparison.Ordinal)
                || code.Contains("GetCommandLineArgs", StringComparison.Ordinal))
            {
                offenders.Add(Path.GetFileName(file));
            }
        }

        Assert.True(
            offenders.Count == 0,
            "the shipped mod must take its settings from catlog.toml only, but these files read the "
            + "environment or the command line: " + string.Join(", ", offenders));
    }

    /// <summary>
    /// Drops <c>//</c> comments so the guards read code rather than prose. Without this, a comment
    /// explaining <i>why</i> the mod must never name the clock seam would itself trip the guard —
    /// which would be an excellent way to teach everyone to stop writing the comment.
    /// </summary>
    /// <param name="source">C# source text.</param>
    /// <returns>The same text with comment tails removed.</returns>
    private static string StripComments(string source)
    {
        var code = new System.Text.StringBuilder(source.Length);
        foreach (string line in source.Split('\n'))
        {
            int comment = line.IndexOf("//", StringComparison.Ordinal);
            code.Append(comment < 0 ? line : line[..comment]).Append('\n');
        }

        return code.ToString();
    }

    /// <summary>Every <c>.cs</c> file in the shipped game mod, ignoring build output.</summary>
    /// <returns>The source paths; empty when the repository is not on disk.</returns>
    internal static string[] GameModSources()
    {
        if (TestPaths.RepoRoot is not { } root)
            return [];

        string directory = Path.Combine(root, "mod", "catlog");
        if (!Directory.Exists(directory))
            return [];

        var sources = new List<string>();
        foreach (string file in Directory.GetFiles(directory, "*.cs", SearchOption.AllDirectories))
        {
            // obj/ holds generated AssemblyInfo, which is not something anyone wrote.
            if (!file.Contains($"{Path.DirectorySeparatorChar}obj{Path.DirectorySeparatorChar}", StringComparison.Ordinal)
                && !file.Contains($"{Path.DirectorySeparatorChar}bin{Path.DirectorySeparatorChar}", StringComparison.Ordinal))
            {
                sources.Add(file);
            }
        }

        return [.. sources];
    }
}

/// <summary>
/// A <see cref="FactAttribute"/> that skips itself when <c>mod/catlog</c>'s sources are not on
/// disk — a packaged run of the test assembly, rather than one from the repository.
/// </summary>
/// <remarks>
/// Same xunit 2 idiom as <c>ContractVectorFactAttribute</c>: a conditional skip set from the
/// constructor, because <c>Assert.Skip</c> is a v3 feature.
/// </remarks>
[Xunit.Sdk.XunitTestCaseDiscoverer("Xunit.Sdk.FactDiscoverer", "xunit.execution.dotnet")]
public sealed class GameModSourceFactAttribute : FactAttribute
{
    /// <summary>Marks the test skipped when the game mod's sources cannot be found.</summary>
    public GameModSourceFactAttribute()
    {
        if (ClockSeamTests.GameModSources().Length == 0)
            Skip = "mod/catlog sources are not on disk; this guard only runs from the repository.";
    }
}
