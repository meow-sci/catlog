using System;
using System.IO;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Util;
using Tomlyn;

namespace MeowSci.Catlog.Lib.Config;

/// <summary>
/// <c>catlog.toml</c> — the player-editable mod settings (§7.2).
/// </summary>
/// <remarks>
/// <para>
/// Three rules, adapted from <c>gatOS/gatOS.GameMod/Configuration/GatOsConfig.cs</c>:
/// </para>
/// <list type="number">
///   <item><description><b>Never throw, always return a usable object.</b> Every failure path returns defaults.</description></item>
///   <item><description><b>Never overwrite a file that failed to parse.</b> The player's hand-edit, typo and all, stays on disk so they can fix it.</description></item>
///   <item><description><b>Clamp, do not reject.</b> An out-of-range value is warned about and clamped, never fatal.</description></item>
/// </list>
/// <para>
/// The naming policy is load-bearing and not optional: Tomlyn maps CLR member names verbatim
/// unless told otherwise, so without <c>PropertyNamingPolicy = SnakeCaseLower</c> every
/// snake_case key in the file is <b>silently ignored</b> — no exception, just defaults. The
/// options instance is cached because Tomlyn compiles mapping metadata on first use.
/// </para>
/// </remarks>
public sealed class ModConfig
{
    /// <summary>The schema this build understands. A file with a different value falls back to defaults.</summary>
    public const int CurrentSchema = 1;

    // One cached options instance: TomlSerializerOptions compiles mapping metadata on first use,
    // and it is an immutable sealed record, so sharing it is both safe and required for speed.
    private static readonly TomlSerializerOptions TomlOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    };

    private const string Header =
        """
        # catlog — Kitten Space Agency telemetry mod
        #
        # This file is rewritten by the mod when settings change in-game. Comments you add
        # outside this header will not survive a rewrite; unknown keys are ignored.
        # Out-of-range values are clamped with a warning rather than rejected.

        """;

    /// <summary>Config schema version. Do not edit by hand.</summary>
    public int Schema { get; set; } = CurrentSchema;

    /// <summary>Master switch. When false the mod collects nothing and ships nothing.</summary>
    public bool Enabled { get; set; } = true;

    /// <summary>The ingest endpoint. Must be an absolute http/https URL.</summary>
    public string IngestUrl { get; set; } = "http://127.0.0.1:8080/v1/ingest";

    /// <summary>Path to <c>catlog-credential.json</c>. Empty means "not configured yet".</summary>
    public string CredentialPath { get; set; } = "";

    /// <summary>Passive telemetry sample rate in Hz. Clamped to [0.1, 20].</summary>
    public double SampleHz { get; set; } = Wire.DefaultSampleHz;

    /// <summary>Telemetry window length in sim seconds. Clamped to [5, 300].</summary>
    public double WindowS { get; set; } = Wire.TelemetryWindowSeconds;

    /// <summary>Local outbox size cap in megabytes. Clamped to [1, 1000].</summary>
    public int OutboxCapMb { get; set; } = Wire.DefaultOutboxCapMb;

    /// <summary>Log verbosity: one of <c>debug</c>, <c>info</c>, <c>warn</c>, <c>error</c>.</summary>
    public string LogLevel { get; set; } = "info";

    /// <summary>The outbox cap in bytes.</summary>
    public long OutboxCapBytes => OutboxCapMb * 1024L * 1024L;

    /// <summary>
    /// True when shipping is configured coherently: enabled, with a parseable absolute http/https
    /// ingest URL and a credential path. When false the shipper must latch dead at load with one
    /// ERROR rather than failing per-batch forever.
    /// </summary>
    /// <param name="reason">Why shipping is not usable, or an empty string.</param>
    /// <returns>True when the shipper may start.</returns>
    public bool CanShip(out string reason)
    {
        if (!Enabled)
        {
            reason = "catlog is disabled in catlog.toml (enabled = false)";
            return false;
        }

        if (string.IsNullOrWhiteSpace(CredentialPath))
        {
            reason = "no credential_path is configured; download a credential from the dashboard";
            return false;
        }

        if (!Uri.TryCreate(IngestUrl, UriKind.Absolute, out Uri? uri)
            || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps))
        {
            reason = $"ingest_url '{IngestUrl}' is not an absolute http or https URL";
            return false;
        }

        reason = string.Empty;
        return true;
    }

    /// <summary>
    /// Loads the config, creating it with defaults on first run. Never throws.
    /// </summary>
    /// <param name="path">Path to <c>catlog.toml</c>.</param>
    /// <returns>A usable config; defaults when the file is missing, malformed, or from another schema.</returns>
    public static ModConfig LoadOrCreate(string path)
    {
        if (!File.Exists(path))
        {
            var fresh = new ModConfig();
            try
            {
                fresh.Save(path);
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or NotSupportedException)
            {
                ModLog.Log.Warn($"catlog: could not write the default config to '{path}': {ex.Message}");
            }

            return fresh; // Usable even when the write failed.
        }

        string text;
        try
        {
            text = File.ReadAllText(path);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or NotSupportedException)
        {
            ModLog.Log.Warn($"catlog: config '{path}' could not be read ({ex.Message}); using defaults.");
            return new ModConfig();
        }

        ModConfig config;
        try
        {
            config = TomlSerializer.Deserialize<ModConfig>(text, TomlOptions) ?? new ModConfig();
        }
        catch (Exception ex)
        {
            // The bad file is deliberately LEFT ON DISK, untouched, so the player can fix the typo.
            ModLog.Log.Warn(
                $"catlog: config '{path}' could not be parsed ({ex.Message}); using defaults. Fix or delete the file.");
            return new ModConfig();
        }

        if (config.Schema != CurrentSchema)
        {
            ModLog.Log.Warn(
                $"catlog: config '{path}' has schema {config.Schema}; this build understands {CurrentSchema}. "
                + "Using defaults (the file is left untouched).");
            return new ModConfig();
        }

        config.Normalize();
        return config;
    }

    /// <summary>Serializes to TOML, with the header comment.</summary>
    /// <returns>The file contents.</returns>
    public string Serialize()
    {
        var sb = new StringBuilder(Header);
        // Tomlyn owns value formatting: never reformat a scalar by hand.
        sb.Append(TomlSerializer.Serialize(this, TomlOptions));
        return sb.ToString();
    }

    /// <summary>Writes the config atomically (temp file + rename).</summary>
    /// <param name="path">Destination path.</param>
    public void Save(string path)
    {
        string? directory = System.IO.Path.GetDirectoryName(System.IO.Path.GetFullPath(path));
        if (!string.IsNullOrEmpty(directory))
            Directory.CreateDirectory(directory);
        AtomicFile.WriteAllText(path, Serialize());
    }

    /// <summary>Clamps every value into range, warning once per clamped field.</summary>
    public void Normalize()
    {
        Schema = CurrentSchema;
        SampleHz = Clamp(nameof(SampleHz), SampleHz, 0.1, 20.0);
        WindowS = Clamp(nameof(WindowS), WindowS, 5.0, 300.0);
        OutboxCapMb = Clamp(nameof(OutboxCapMb), OutboxCapMb, 1, 1000);
        LogLevel = OneOf(nameof(LogLevel), LogLevel, "info", ["debug", "info", "warn", "error"]);
        IngestUrl = (IngestUrl ?? string.Empty).Trim();
        CredentialPath = (CredentialPath ?? string.Empty).Trim();
    }

    private static double Clamp(string name, double value, double min, double max)
    {
        if (!double.IsFinite(value))
        {
            ModLog.Log.Warn($"catlog: config {Key(name)} is not a number; using {min}.");
            return min;
        }

        double clamped = Math.Clamp(value, min, max);
        if (!clamped.Equals(value))
            ModLog.Log.Warn($"catlog: config {Key(name)} {value} is outside [{min}, {max}]; using {clamped}.");
        return clamped;
    }

    private static int Clamp(string name, int value, int min, int max)
    {
        int clamped = Math.Clamp(value, min, max);
        if (clamped != value)
            ModLog.Log.Warn($"catlog: config {Key(name)} {value} is outside [{min}, {max}]; using {clamped}.");
        return clamped;
    }

    private static string OneOf(string name, string? value, string fallback, string[] allowed)
    {
        string normalized = (value ?? string.Empty).Trim().ToLowerInvariant();
        if (Array.IndexOf(allowed, normalized) >= 0)
            return normalized;

        ModLog.Log.Warn(
            $"catlog: config {Key(name)} '{value}' is not one of [{string.Join(", ", allowed)}]; using '{fallback}'.");
        return fallback;
    }

    // The TOML key for a CLR property name, so warnings name what the player actually typed.
    private static string Key(string clrName)
        => JsonNamingPolicy.SnakeCaseLower.ConvertName(clrName);
}
