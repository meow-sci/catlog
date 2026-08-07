using System;
using System.Globalization;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Data.Sqlite;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Lib.Outbox;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>A server this test stops, edits underneath and restarts, so it never shares a process.</summary>
public sealed class RevocableServerFixture : ServerFixture
{
    protected override bool AlwaysSpawn => true;
}

/// <summary>
/// The §7.5 revocation case: a credential that worked stops working, with
/// <c>401 license_revoked</c>, and the shipper latches dead rather than retrying forever.
/// </summary>
/// <remarks>
/// <para>
/// <b>How the credential is revoked, and why it is done this way.</b> §7.5 says "revoke the
/// credential via <c>catlogctl</c>", but the verbs that revoke — <c>ban</c>, <c>purge</c>,
/// <c>denylist</c> — are WP3's and report "not yet implemented" today, and there is no admin route
/// for it either. So this test performs the revocation the way WP3's verb eventually will: it sets
/// <c>credential.revoked_at</c>, which is exactly what <c>store.Events.RevokeCredential</c> does
/// and what <c>authz.DenyList.LoadFrom</c> reads at start.
/// </para>
/// <para>
/// The write goes through <c>Microsoft.Data.Sqlite</c> while the server is stopped. That is sound
/// for two reasons that are worth stating: Turso writes SQLite-format files (verified — the
/// updated row is read back by the server on restart), and Turso takes an <b>exclusive whole-file
/// lock</b> while running (WP1's <c>TestSecondProcessIsLockedOut</c>), so the stop is not
/// politeness but a hard prerequisite — it is also what checkpoints the WAL into the main file.
/// </para>
/// <para>
/// When WP3 lands, the two lines in <see cref="Revoke"/> become a <c>catlogctl</c> invocation and
/// nothing else about this test changes.
/// </para>
/// </remarks>
public sealed class RevocationTests : IClassFixture<RevocableServerFixture>, IDisposable
{
    private const int Events = 12;

    private readonly RevocableServerFixture _server;
    private readonly string _outboxDir =
        Path.Combine(Path.GetTempPath(), "catlog-itest-outbox-" + Guid.NewGuid().ToString("N"));

    public RevocationTests(RevocableServerFixture server) => _server = server;

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
    public async Task ARevokedCredentialIsRefusedAndTheShipperLatchesDead()
    {
        IssuedCredential credential = _server.Issue("itest_revoked");
        byte[] batch = TestBatch.Ndjson(Events);

        // --- it works to begin with ---
        using (var client = new IngestClient(_server.IngestUrl, credential.Credential))
        {
            IngestResponse accepted = await client.ShipAsync(batch);
            Assert.True(accepted.Status == 200, $"before revocation: {accepted.Status} {accepted.Body}");
            Assert.Equal(Events, accepted.Accepted);
        }

        // --- revoke, and bring the server back so it reloads the deny-list ---
        _server.Stop();
        int updated = Revoke(Path.Combine(_server.DataDir, "events.db"), credential.Credential.Jkt);
        Assert.Equal(1, updated);
        _server.Start();
        await WaitForHealthAsync();

        // --- the same credential is now refused, by code ---
        using (var client = new IngestClient(_server.IngestUrl, credential.Credential))
        {
            IngestResponse refused = await client.ShipAsync(TestBatch.Ndjson(Events));
            Assert.True(refused.Status == 401, $"after revocation: {refused.Status} {refused.Body}");
            Assert.Equal(Wire.Errors.LicenseRevoked, refused.Error);
        }

        // --- and the real shipper stops trying: a revoked licence does not fix itself ---
        using OutboxDb outbox = OutboxDb.Open(Path.Combine(_outboxDir, "outbox.db"));
        outbox.Append(TestBatch.Build(Events));
        using var shipper = new BatchShipper(
            new ShipperOptions(_server.IngestUrl, OutboxCapBytes: 0),
            outbox,
            credential.Credential);

        ShipAttempt attempt = await shipper.ShipOnceAsync(CancellationToken.None);
        Assert.Equal(ShipOutcome.Fatal, attempt.Outcome);
        Assert.True(shipper.IsDead, "a 401 that is not clock_skew must latch the shipper dead");
        Assert.Contains(Wire.Errors.LicenseRevoked, shipper.DeadReason, StringComparison.Ordinal);

        // The events are still in the outbox: a poison-pill batch is made visible, never destroyed.
        Assert.Equal(Events, outbox.PendingCount);
    }

    /// <summary>
    /// Marks a credential revoked in <c>events.db</c>. The server must not be running.
    /// </summary>
    /// <param name="eventsDbPath">Path to <c>events.db</c>.</param>
    /// <param name="jkt">The credential's RFC 7638 thumbprint.</param>
    /// <returns>How many rows were updated.</returns>
    private static int Revoke(string eventsDbPath, string jkt)
    {
        Assert.True(File.Exists(eventsDbPath), $"{eventsDbPath} does not exist");
        SQLitePCL.Batteries_V2.Init();

        using var connection = new SqliteConnection(
            new SqliteConnectionStringBuilder { DataSource = eventsDbPath, Pooling = false }.ToString());
        connection.Open();

        using SqliteCommand command = connection.CreateCommand();
        command.CommandText = "UPDATE credential SET revoked_at = $at WHERE jkt = $jkt AND revoked_at IS NULL";
        command.Parameters.AddWithValue("$at", DateTimeOffset.UtcNow.ToUnixTimeMilliseconds());
        command.Parameters.AddWithValue("$jkt", jkt);
        return command.ExecuteNonQuery();
    }

    private async Task WaitForHealthAsync()
    {
        using var http = new System.Net.Http.HttpClient { Timeout = TimeSpan.FromSeconds(5) };
        DateTimeOffset deadline = DateTimeOffset.UtcNow.AddSeconds(60);
        while (DateTimeOffset.UtcNow < deadline)
        {
            try
            {
                using System.Net.Http.HttpResponseMessage response = await http.GetAsync(_server.BaseUrl + "/healthz");
                if (response.IsSuccessStatusCode)
                    return;
            }
            catch (System.Net.Http.HttpRequestException)
            {
                // Not listening yet.
            }

            await Task.Delay(50);
        }

        throw new TimeoutException(
            "catlogd did not come back after the revocation "
            + $"(base {_server.BaseUrl}, data {_server.DataDir}, at {DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture)})");
    }
}
