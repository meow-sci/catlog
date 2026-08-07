using System;
using System.IO;
using MeowSci.Catlog.Lib.Config;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Config;

/// <summary>
/// INITIAL_IMPL_PLAN §7.2: <c>catlog.toml</c> — load never throws, clamp don't reject, atomic save.
/// </summary>
public sealed class ModConfigTests
{
    /// <summary>
    /// The Tomlyn trap this pins: without <c>PropertyNamingPolicy = SnakeCaseLower</c> every
    /// snake_case key is <b>silently ignored</b> — no exception, just defaults. Silent data loss
    /// needs a round-trip test, not an error-path test.
    /// </summary>
    [Fact]
    public void SnakeCaseKeysSurviveASaveLoadRoundTrip()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        var original = new ModConfig
        {
            Enabled = false,
            IngestUrl = "https://catlog.example/v1/ingest",
            CredentialPath = "/tmp/catlog-credential.json",
            SampleHz = 4.0,
            WindowS = 60.0,
            OutboxCapMb = 128,
            ShipIntervalS = 120.0,
            ShipMaxPending = 250,
            LogLevel = "debug",
        };

        original.Save(path);
        string text = File.ReadAllText(path);
        ModConfig loaded = ModConfig.LoadOrCreate(path);

        Assert.Contains("ingest_url", text);
        Assert.Contains("credential_path", text);
        Assert.Contains("sample_hz", text);
        Assert.Contains("window_s", text);
        Assert.Contains("outbox_cap_mb", text);
        // "ship_interval_s" also appears in the header comment that warns about the 30 s floor, so
        // this looks for the assignment rather than the word.
        Assert.Contains("ship_interval_s = ", text, StringComparison.Ordinal);
        Assert.Contains("ship_max_pending", text);
        Assert.Contains("log_level", text);

        Assert.False(loaded.Enabled);
        Assert.Equal("https://catlog.example/v1/ingest", loaded.IngestUrl);
        Assert.Equal("/tmp/catlog-credential.json", loaded.CredentialPath);
        Assert.Equal(4.0, loaded.SampleHz);
        Assert.Equal(60.0, loaded.WindowS);
        Assert.Equal(128, loaded.OutboxCapMb);
        Assert.Equal(120.0, loaded.ShipIntervalS);
        Assert.Equal(250, loaded.ShipMaxPending);
        Assert.Equal("debug", loaded.LogLevel);
    }

    [Fact]
    public void FirstRun_WritesDefaultsAndReturnsThem()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.True(File.Exists(path), "the default config should have been written");
        Assert.True(config.Enabled);
        Assert.Equal(Wire.DefaultSampleHz, config.SampleHz);
        Assert.Equal(Wire.TelemetryWindowSeconds, config.WindowS);
        Assert.Equal(Wire.DefaultOutboxCapMb, config.OutboxCapMb);

        // The shipped cadence: pump roughly once a minute, with the count trigger as a valve.
        Assert.Equal(60.0, config.ShipIntervalS);
        Assert.Equal(Wire.ShipPendingTrigger, config.ShipMaxPending);
    }

    /// <summary>
    /// Rule 2: a file that failed to parse is never overwritten — the player's hand-edit, typo and
    /// all, stays on disk so they can fix it.
    /// </summary>
    [Fact]
    public void MalformedToml_YieldsDefaultsAndLeavesTheFileAlone()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        const string broken = "this is not [ valid toml =\n";
        File.WriteAllText(path, broken);

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.True(config.Enabled, "defaults are returned");
        Assert.Equal(broken, File.ReadAllText(path));
    }

    [Fact]
    public void UnknownSchema_YieldsDefaultsAndLeavesTheFileAlone()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        const string future = "schema = 99\nsample_hz = 17.0\n";
        File.WriteAllText(path, future);

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.Equal(Wire.DefaultSampleHz, config.SampleHz);
        Assert.Equal(future, File.ReadAllText(path));
    }

    [Fact]
    public void UnknownKeysAreIgnored()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        File.WriteAllText(path, "schema = 1\nsample_hz = 3.0\nfuture_option = \"???\"\n");

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.Equal(3.0, config.SampleHz);
    }

    [Theory]
    [InlineData(0.0, 0.1)]
    [InlineData(-5.0, 0.1)]
    [InlineData(1000.0, 20.0)]
    [InlineData(double.NaN, 0.1)]
    [InlineData(2.0, 2.0)]
    public void SampleHzIsClampedNotRejected(double input, double expected)
    {
        var config = new ModConfig { SampleHz = input };

        config.Normalize();

        Assert.Equal(expected, config.SampleHz);
    }

    [Theory]
    [InlineData(1.0, 5.0)]
    [InlineData(10_000.0, 300.0)]
    [InlineData(30.0, 30.0)]
    public void WindowSecondsAreClamped(double input, double expected)
    {
        var config = new ModConfig { WindowS = input };

        config.Normalize();

        Assert.Equal(expected, config.WindowS);
    }

    [Theory]
    [InlineData(0, 1)]
    [InlineData(100_000, 1_000)]
    [InlineData(50, 50)]
    public void OutboxCapIsClamped(int input, int expected)
    {
        var config = new ModConfig { OutboxCapMb = input };

        config.Normalize();

        Assert.Equal(expected, config.OutboxCapMb);
    }

    /// <summary>
    /// The cadence knobs follow the same clamp-don't-reject rule as everything else: a player who
    /// asks for a 0 s or a one-day interval gets the nearest legal value and a warning, never a
    /// refused config.
    /// </summary>
    [Theory]
    [InlineData(0.0, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(-30.0, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(86_400.0, Wire.MaxShipAgeTriggerSeconds)]
    [InlineData(double.NaN, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(1.0, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(5.0, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(29.999, Wire.MinShipAgeTriggerSeconds)]
    [InlineData(30.0, 30.0)]
    [InlineData(60.0, 60.0)]
    public void ShipIntervalIsClamped(double input, double expected)
    {
        var config = new ModConfig { ShipIntervalS = input };

        config.Normalize();

        Assert.Equal(expected, config.ShipIntervalS);
    }

    /// <summary>
    /// The clamp's lower bound <b>is</b> the hard floor, and the floor is 30 s. Pinned as a
    /// separate assertion because the whole point of the exercise is that no edit to
    /// <c>catlog.toml</c> can produce a faster reporting cadence than this — a clamp that quietly
    /// drifted back down to 1 s would reopen the hole the floor was added to close.
    /// </summary>
    [Fact]
    public void TheShipIntervalClampIsTheHardFloor()
    {
        Assert.Equal(30.0, Wire.MinShipIntervalSeconds);
        Assert.Equal(Wire.MinShipIntervalSeconds, Wire.MinShipAgeTriggerSeconds);

        // The shipped default still sits above it, so the floor is not the normal cadence.
        Assert.True(new ModConfig().ShipIntervalS >= Wire.MinShipIntervalSeconds);
    }

    [Theory]
    [InlineData(0, Wire.MinBatchEventCap)]
    [InlineData(-1, Wire.MinBatchEventCap)]
    [InlineData(1_000_000, Wire.MaxEventsPerBatch)]
    [InlineData(120, 120)]
    public void ShipMaxPendingIsClamped(int input, int expected)
    {
        var config = new ModConfig { ShipMaxPending = input };

        config.Normalize();

        Assert.Equal(expected, config.ShipMaxPending);
    }

    /// <summary>
    /// A cadence change must actually reach the shipper: a player sets <c>ship_interval_s</c> and
    /// the trigger moves with it — down to the floor and no further.
    /// </summary>
    [Fact]
    public void CadenceKnobsFlowIntoShipperOptions()
    {
        var config = new ModConfig { ShipIntervalS = 120.0, ShipMaxPending = 75 };
        config.Normalize();

        var options = new ShipperOptions(
            config.IngestUrl,
            PendingTrigger: config.ShipMaxPending,
            AgeTriggerSeconds: config.ShipIntervalS);

        Assert.Equal(120.0, options.AgeTriggerSeconds);
        Assert.Equal(75, options.PendingTrigger);

        // …and the impatient version of the same edit reaches the shipper as 30, not as 2.
        var impatient = new ModConfig { ShipIntervalS = 2.0 };
        impatient.Normalize();
        Assert.Equal(
            Wire.MinShipIntervalSeconds,
            new ShipperOptions(impatient.IngestUrl, AgeTriggerSeconds: impatient.ShipIntervalS).AgeTriggerSeconds);
    }

    [Theory]
    [InlineData("DEBUG", "debug")]
    [InlineData(" warn ", "warn")]
    [InlineData("shouty", "info")]
    [InlineData("", "info")]
    public void LogLevelIsNormalizedToTheAllowList(string input, string expected)
    {
        var config = new ModConfig { LogLevel = input };

        config.Normalize();

        Assert.Equal(expected, config.LogLevel);
    }

    [Fact]
    public void CanShip_RequiresEnabledCredentialAndAnAbsoluteHttpUrl()
    {
        Assert.False(new ModConfig { Enabled = false }.CanShip(out string disabled));
        Assert.Contains("disabled", disabled);

        Assert.False(new ModConfig { CredentialPath = "" }.CanShip(out string noCredential));
        Assert.Contains("credential_path", noCredential);

        Assert.False(
            new ModConfig { CredentialPath = "/x.json", IngestUrl = "not a url" }.CanShip(out string badUrl));
        Assert.Contains("ingest_url", badUrl);

        Assert.False(
            new ModConfig { CredentialPath = "/x.json", IngestUrl = "ftp://x/y" }.CanShip(out string badScheme));
        Assert.Contains("ingest_url", badScheme);

        Assert.True(new ModConfig { CredentialPath = "/x.json" }.CanShip(out string ok));
        Assert.Empty(ok);
    }

    [Fact]
    public void Save_IsAtomicAndLeavesNoTempFileBehind()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");

        new ModConfig().Save(path);
        new ModConfig { SampleHz = 5 }.Save(path);

        Assert.False(File.Exists(path + ".tmp"), "the temp file must be renamed away, not left behind");
        Assert.Equal(5, ModConfig.LoadOrCreate(path).SampleHz);
    }

    [Fact]
    public void Save_CreatesMissingDirectories()
    {
        using var dir = new TempDir();
        string path = Path.Combine(dir.Path, "mods", "catlog", "catlog.toml");

        new ModConfig().Save(path);

        Assert.True(File.Exists(path));
    }

    [Fact]
    public void SerializedFileCarriesTheHeaderComment()
    {
        Assert.StartsWith("# catlog", new ModConfig().Serialize());
    }
}
