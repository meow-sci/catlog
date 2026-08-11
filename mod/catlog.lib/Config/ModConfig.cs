using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Events;
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
        #
        # ship_interval_s has a HARD MINIMUM of 30 seconds that lives in the mod code, not
        # here. Setting it lower is clamped up to 30, and even if it were not, the shipper
        # refuses to send two batches to the server less than 30 seconds apart no matter
        # what this file says. There is no key that raises or lowers that. Events are never
        # lost by waiting: they queue in outbox.db and go out in the next batch.
        #
        # ---------------------------------------------------------------------------------
        # [events] — per-event-type reporting.
        #
        # Every type is reported unless you list it here set to false. An omitted key means
        # enabled, so the [events] table at the end of this file is empty on a fresh install
        # and stays that way until you put something in it.
        #
        # QUOTE THE KEYS. In TOML a bare  telemetry.window = false  under [events] is a nested
        # table named "window" inside a table named "telemetry" — not a key called
        # "telemetry.window". The mod cannot read that, the whole file fails to parse, and you
        # silently get stock settings back. Write the name in quotes, always.
        #
        # Six types cannot be switched off, because switching them off breaks scoring integrity
        # or mandatory system attribution. Setting them to false here is dropped with a warning:
        #
        #   flight.flagged   marks a run as tainted. It is the ONLY thing that does, so
        #                    without it teleporting, refuelling, resource editing and console
        #                    commands all score normally and show up publicly.
        #   kitten.kia       is what stops a crash that killed the crew being counted as a
        #                    lithobrake you survived.
        #   session.started  is the only record that a save was reloaded.
        #   system.discovered binds the session and career to the loaded celestial system.
        #   flight.started   is what makes a flight a flight — with no start there is no crew,
        #                    no body, and nothing for a board to join against.
        #   flight.ended     is what makes a flight recovered. Without it nothing you land is
        #                    ever counted as landed.
        #
        # Everything else is yours to turn off, and every one you turn off is a board you
        # stop appearing on. The full list, with what each one feeds:
        #
        # [events]
        # "session.started" = true    # locked on
        # "system.discovered" = true  # locked on
        # "system.body" = true        # celestial catalogue and system-scoped boards
        # "flight.started" = true     # locked on
        # "flight.ended" = true       # locked on
        # "flight.flagged" = true     # locked on
        # "vehicle.situation" = true  # softest_touchdown, landed_bodies, splashdowns
        # "vehicle.atmosphere" = true # fastest_entry
        # "vehicle.orbit" = true      # orbits_achieved, fastest_to_orbit, highest_apoapsis,
        #                             # lowest_orbit, roundest_orbit, steepest_orbit
        # "vehicle.soi" = true        # soi_bodies and every fastest_to_<body> board
        # "vehicle.rud" = true        # rud_total and every rud_<cause> board
        # "vehicle.impact" = true     # biggest_lithobrake_survived, biggest_impact_energy
        # "vehicle.landed" = true     # every landing board: where, how hard, and whether it held
        # "vehicle.staging" = true    # stagings, most_stages
        # "vehicle.docked" = true     # dockings
        # "vehicle.undocked" = true   # no board reads it today; it is part of the story
        # "engine.ignition" = true    # engine_ignitions
        # "engine.shutdown" = true    # no board reads it today
        # "engine.flameout" = true    # flameouts
        # "kitten.eva_start" = true   # evas
        # "kitten.eva_end" = true     # longest_eva
        # "kitten.tumble" = true      # kitten_tumbles
        # "kitten.kia" = true         # locked on
        # "roster.snapshot" = true    # distance_travelled, top_kitten_distance,
        #                             # top_kitten_missions, and the kitten roster itself
        # "telemetry.window" = true   # peak_g_survived, max_q_survived, highest_altitude,
        #                             # fastest_surface_speed, fastest_orbital_speed. Also the
        #                             # only kind of row the outbox is allowed to drop when it
        #                             # fills up, so turning it off leaves it nothing to shed.
        # ---------------------------------------------------------------------------------

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

    /// <summary>
    /// How long an event may sit in the outbox before the shipper pumps, in seconds. This is the
    /// normal ship cadence — the mod is a bulk telemetry pump, not a live feed. Clamped to
    /// [<see cref="Wire.MinShipAgeTriggerSeconds"/>, <see cref="Wire.MaxShipAgeTriggerSeconds"/>].
    /// </summary>
    /// <remarks>
    /// The lower clamp is <see cref="Wire.MinShipIntervalSeconds"/>, the hard-coded floor, and the
    /// clamp is the courtesy rather than the enforcement: <c>BatchShipper</c> refuses to transmit
    /// inside the window whatever this says, so editing the file below 30 changes nothing except
    /// that the player would otherwise be reading a number the mod does not honour.
    /// </remarks>
    public double ShipIntervalS { get; set; } = Wire.ShipAgeTriggerSeconds;

    /// <summary>
    /// Safety valve: ship early once this many events are pending, whatever
    /// <see cref="ShipIntervalS"/> says. Not the normal trigger (see
    /// <see cref="Wire.ShipPendingTrigger"/>). Clamped to
    /// [<see cref="Wire.MinBatchEventCap"/>, <see cref="Wire.MaxEventsPerBatch"/>].
    /// </summary>
    /// <remarks>
    /// "Whatever <see cref="ShipIntervalS"/> says" does <b>not</b> extend to
    /// <see cref="Wire.MinShipIntervalSeconds"/>. Lowering this key makes batches smaller and more
    /// frequent only up to the floor; past it the events queue in the outbox instead.
    /// </remarks>
    public int ShipMaxPending { get; set; } = Wire.ShipPendingTrigger;

    /// <summary>Log verbosity: one of <c>debug</c>, <c>info</c>, <c>warn</c>, <c>error</c>.</summary>
    public string LogLevel { get; set; } = "info";

    /// <summary>The outbox cap in bytes.</summary>
    public long OutboxCapBytes => OutboxCapMb * 1024L * 1024L;

    /// <summary>
    /// Per-event-type reporting, keyed by the wire type name (§4.2). A key set to <c>false</c>
    /// stops that type being produced at all; an absent key means enabled.
    /// </summary>
    /// <remarks>
    /// <para>
    /// <b>Empty by default, and it stays empty.</b> Writing all 25 registered keys out would mean every
    /// future type silently arriving switched to whatever the file happens to say, and would turn a
    /// one-line opt-out into a wall of noise. The full list ships commented out in
    /// <c>Header</c> instead — that block is the only place the shape is documented for a player,
    /// since there is no example file, so it has a test holding it in step with the registry.
    /// </para>
    /// <para>
    /// <b>This property is declared last on purpose.</b> Tomlyn writes members in declaration
    /// order, and a TOML table header swallows every key that follows it — a scalar declared after
    /// this one would be emitted underneath <c>[events]</c> and then fail to parse as a bool,
    /// taking the whole file down to defaults. New scalar keys go above.
    /// </para>
    /// <para>
    /// Keys arrive quoted (<c>"telemetry.window" = false</c>) because they contain dots. Tomlyn
    /// emits them that way and a bare dotted key is a parse error, not a key — see the header.
    /// </para>
    /// </remarks>
    public Dictionary<string, bool> Events { get; set; } = new(StringComparer.Ordinal);

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
        ShipIntervalS = Clamp(
            nameof(ShipIntervalS), ShipIntervalS, Wire.MinShipAgeTriggerSeconds, Wire.MaxShipAgeTriggerSeconds);
        ShipMaxPending = Clamp(nameof(ShipMaxPending), ShipMaxPending, Wire.MinBatchEventCap, Wire.MaxEventsPerBatch);
        LogLevel = OneOf(nameof(LogLevel), LogLevel, "info", ["debug", "info", "warn", "error"]);
        IngestUrl = (IngestUrl ?? string.Empty).Trim();
        CredentialPath = (CredentialPath ?? string.Empty).Trim();
        Events = NormalizeEvents(Events);
    }

    /// <summary>
    /// The emission filter this config expresses, for
    /// <see cref="Detect.EventPipelineOptions.Types"/>.
    /// </summary>
    /// <returns>The filter; <see cref="EventTypeFilter.All"/> when nothing is disabled.</returns>
    public EventTypeFilter EventFilter()
    {
        List<string>? disabled = null;
        foreach (KeyValuePair<string, bool> entry in Events)
        {
            if (!entry.Value)
                (disabled ??= []).Add(entry.Key);
        }

        return EventTypeFilter.Create(disabled);
    }

    // Rule 3 applied to a table: drop what cannot be honoured, warn about it, and let the rewritten
    // file be the truth. Unknown keys go the same way Tomlyn's unknown root keys already do —
    // ignored — because a typo must not be able to cost a player their whole config.
    private static Dictionary<string, bool> NormalizeEvents(Dictionary<string, bool>? events)
    {
        var normalized = new Dictionary<string, bool>(StringComparer.Ordinal);
        if (events is null)
            return normalized;

        foreach (KeyValuePair<string, bool> entry in events)
        {
            if (!EventTypes.IsKnown(entry.Key))
            {
                ModLog.Log.Warn(
                    $"catlog: config [events] has no event type '{entry.Key}'; ignoring it.");
                continue;
            }

            if (!entry.Value && EventTypes.IsAlwaysReported(entry.Key))
            {
                // Dropped rather than rewritten to true: absent already means enabled, so dropping
                // says the same thing in fewer words, and the saved file then reads as what the
                // mod is actually doing.
                ModLog.Log.Warn(
                    $"catlog: config [events] \"{entry.Key}\" = false is ignored — that type is part of "
                    + "how a run is scored honestly and cannot be switched off. Reporting it.");
                continue;
            }

            normalized[entry.Key] = entry.Value;
        }

        return normalized;
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
