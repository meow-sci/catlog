using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using Brutal.ImGuiApi;
using Brutal.Numerics;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// The in-game status window (§7.4): collection on/off, queue depth, the last ship result, and the
/// handle the events are being filed under with its expiry.
/// </summary>
/// <remarks>
/// <para>
/// It answers the four questions a player actually has — <i>is it on, is anything stuck, did the
/// server take it, whose board am I on</i> — and it is the only diagnostic anyone gets on a KSA
/// machine, since the game project has no test suite by design.
/// </para>
/// <para>
/// Read-only by design apart from the master switch: no text input anywhere, so the mod needs no
/// <c>GameSettings.OnKeyAll</c> hotkey guard. Everything drawn is a plain field read; no file I/O,
/// no locks, no allocation beyond ImGui's own frame buffer.
/// </para>
/// </remarks>
public sealed class StatusWindow
{
    private static readonly float4 Good = new(0.45f, 0.85f, 0.45f, 1f);
    private static readonly float4 Bad = new(1f, 0.35f, 0.35f, 1f);
    private static readonly float4 Warn = new(1f, 0.78f, 0.30f, 1f);
    private static readonly float4 Label = new(0.65f, 0.68f, 0.72f, 1f);

    private ShipAttempt? _lastSeenAttempt;
    private long _lastAttemptTimestamp;

    /// <summary>Whether the window is drawn. Toggled with F10.</summary>
    public bool Visible { get; set; }

    /// <summary>Draws one frame. Never throws.</summary>
    /// <param name="runtime">The runtime to report on, or null when init failed.</param>
    /// <param name="initError">Why the mod is inert, when it is.</param>
    public void Draw(CatlogRuntime? runtime, string initError)
    {
        if (ImGui.IsKeyPressed(ImGuiKey.F10))
            Visible = !Visible;

        if (!Visible)
            return;

        ImGui.SetNextWindowSize(new float2(460, 420), ImGuiCond.FirstUseEver);
        bool open = Visible;
        if (ImGui.Begin("catlog"u8, ref open))
            DrawBody(runtime, initError);
        ImGui.End();
        Visible = open;
    }

    private void DrawBody(CatlogRuntime? runtime, string initError)
    {
        if (runtime is null)
        {
            ImGui.TextColored(Bad, "catlog failed to initialize."u8);
            ImGui.TextWrapped(initError.Length > 0 ? initError : "See the game log for the reason.");
            return;
        }

        DrawCollection(runtime);
        ImGui.Spacing();
        DrawShipping(runtime);
        ImGui.Spacing();
        DrawHealth(runtime);
        ImGui.Spacing();
        DrawDiagnostics(runtime);
    }

    private static void DrawCollection(CatlogRuntime runtime)
    {
        ImGui.SeparatorText("Collection"u8);

        bool enabled = runtime.CollectionEnabled;
        if (ImGui.Checkbox("Record this session"u8, ref enabled))
            runtime.CollectionEnabled = enabled;
        ImGui.SetItemTooltip(
            "Turning this off stops sampling immediately. Events already recorded still ship — they are "u8
            + "already your record of what happened."u8);

        if (!BeginRows("##collection"u8))
            return;

        Row("Status"u8);
        if (runtime.IsCollecting)
            ImGui.TextColored(Good, $"recording at {runtime.Config.SampleHz:0.##} Hz");
        else if (!runtime.CollectionEnabled)
            ImGui.TextColored(Warn, "paused"u8);
        else
            ImGui.TextColored(Bad, "stopped (see Health below)"u8);

        Row("Vehicles"u8);
        ImGui.Text($"{runtime.LastFrameVehicles} in the last sample");

        Row("Session"u8);
        ImGui.Text(runtime.Pipeline.SessionId);

        Row("Install"u8);
        ImGui.Text(runtime.InstallId);

        Row("Recorded"u8);
        ImGui.Text($"{runtime.EventsAppended} events");

        Row("Queued"u8);
        long pending = runtime.PendingEvents;
        if (pending > 5000)
            ImGui.TextColored(Warn, $"{pending} events waiting to ship");
        else
            ImGui.Text($"{pending} events");

        EndRows();
    }

    private void DrawShipping(CatlogRuntime runtime)
    {
        ImGui.SeparatorText("Shipping"u8);

        if (runtime.Shipper is not { } shipper)
        {
            ImGui.TextColored(Warn, "not shipping"u8);
            ImGui.TextWrapped(runtime.CredentialError.Length > 0
                ? runtime.CredentialError
                : "No shipper is configured.");
            ImGui.TextDisabled("Events are still spooled locally and will ship once a credential is set."u8);
            return;
        }

        if (!BeginRows("##shipping"u8))
            return;


        Row("Handle"u8);
        Credential? credential = runtime.Credential;
        ImGui.Text(credential?.Handle ?? "unknown");

        Row("Expires"u8);
        if (credential is { } cred)
        {
            DateTimeOffset expiry = cred.Claims.Expiry;
            TimeSpan remaining = expiry - DateTimeOffset.UtcNow;
            string when = expiry.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture);
            if (remaining <= TimeSpan.Zero)
                ImGui.TextColored(Bad, $"{when} (expired)");
            else if (remaining < TimeSpan.FromDays(7))
                ImGui.TextColored(Warn, $"{when} (in {remaining.TotalDays:0} days)");
            else
                ImGui.Text($"{when} (in {remaining.TotalDays:0} days)");
        }
        else
        {
            ImGui.TextDisabled("—"u8);
        }

        Row("Endpoint"u8);
        ImGui.Text(runtime.Config.IngestUrl);

        Row("Stream"u8);
        ImGui.Text($"{shipper.StreamId} · seq {shipper.Sequence}");

        Row("Last ship"u8);
        DrawLastAttempt(shipper);

        if (shipper.IsDead)
        {
            Row("Disabled"u8);
            ImGui.TextColored(Bad, shipper.DeadReason);
        }

        EndRows();
    }

    private void DrawLastAttempt(BatchShipper shipper)
    {
        ShipAttempt? attempt = shipper.LastAttempt;
        if (attempt is null)
        {
            ImGui.TextDisabled("nothing shipped yet"u8);
            return;
        }

        if (!ReferenceEquals(attempt, _lastSeenAttempt))
        {
            _lastSeenAttempt = attempt;
            _lastAttemptTimestamp = Stopwatch.GetTimestamp();
        }

        double agoSeconds = (Stopwatch.GetTimestamp() - _lastAttemptTimestamp) / (double)Stopwatch.Frequency;

        switch (attempt.Outcome)
        {
            case ShipOutcome.Accepted:
            case ShipOutcome.Replayed:
                // Report what the SERVER said, not the local batch size: "we sent 64" and "the
                // server stored 64" are different claims, and only the second one is news.
                string counts = attempt.ServerAccepted is { } accepted
                    ? $"{accepted} accepted, {attempt.ServerDeduped ?? 0} deduped"
                    : $"{attempt.EventsShipped} sent (the server reported no counts)";
                ImGui.TextColored(Good, $"HTTP {attempt.StatusCode} · {counts} · {Ago(agoSeconds)}");
                break;

            case ShipOutcome.NothingToShip:
                ImGui.TextDisabled($"idle · {Ago(agoSeconds)}");
                break;

            case ShipOutcome.Fatal:
                ImGui.TextColored(Bad, $"stopped: {attempt.Error}");
                break;

            case ShipOutcome.ClockResynced:
                // Not a failure and not a retry count: the offset was relearned and the same batch
                // goes again when the 30 s floor next opens.
                ImGui.TextColored(Warn, $"clock resynced by {shipper.ClockOffsetMs} ms · {Ago(agoSeconds)}");
                break;

            default:
                ImGui.TextColored(Warn,
                    $"HTTP {attempt.StatusCode} {attempt.Error} · retrying (attempt {shipper.ConsecutiveFailures}) · {Ago(agoSeconds)}");
                break;
        }
    }

    private static void DrawHealth(CatlogRuntime runtime)
    {
        ImGui.SeparatorText("Health"u8);

        IReadOnlyList<SubsystemFault> faults = runtime.Health.Snapshot();
        if (faults.Count == 0 && Patcher.Unresolved.Count == 0)
        {
            ImGui.TextColored(Good, "all subsystems nominal"u8);
        }
        else
        {
            foreach (SubsystemFault fault in faults)
            {
                ImGui.TextColored(fault.Permanent ? Bad : Warn, $"{fault.Subsystem}: {fault.Error}");
            }

            if (Patcher.Unresolved.Count > 0)
            {
                ImGui.TextColored(Warn,
                    $"{Patcher.Unresolved.Count} patch target(s) missing from this game build:");
                foreach (string target in Patcher.Unresolved)
                    ImGui.TextDisabled($"    {target}");
                ImGui.TextWrapped(
                    "Those signals are not being recorded. Everything else still works — this is what "
                    + "the mod does instead of failing to load against a newer game.");
            }
        }
    }

    private static void DrawDiagnostics(CatlogRuntime runtime)
    {
        if (!ImGui.CollapsingHeader("Diagnostics"u8))
            return;

        if (!BeginRows("##diagnostics"u8))
            return;


        Row("Game build"u8);
        ImGui.Text(VehicleTelemetry.GameBuild());

        Row("Mod version"u8);
        ImGui.Text(CatlogRuntime.ModVersion);

        Row("Patches"u8);
        ImGui.Text($"{Patcher.InstalledCount} installed, {Patcher.Unresolved.Count} unresolved");

        Row("Solver batches"u8);
        ImGui.Text($"{Patcher.SolverBatches}");

        Row("Sample"u8);
        PerfStat sample = runtime.SampleStats;
        ImGui.Text($"avg {sample.AvgMicros / 1000.0:0.000} ms · max {sample.MaxMicros / 1000.0:0.000} ms · n={sample.Count}");

        Row("Detect"u8);
        PerfStat detect = runtime.Pipeline.FrameStats;
        ImGui.Text($"avg {detect.AvgMicros / 1000.0:0.000} ms · max {detect.MaxMicros / 1000.0:0.000} ms · n={detect.Count}");

        Row("Read faults"u8);
        ImGui.Text($"{VehicleTelemetry.Faults.Count}");

        Row("Data folder"u8);
        ImGui.TextWrapped(ModPaths.DataDir);

        EndRows();
    }

    // BeginTable's End is conditional (unlike Begin/End), so this returns whether the table opened
    // and every caller guards on it — an unmatched EndTable is an ImGui assertion, not a no-op.
    private static bool BeginRows(ImString id)
    {
        ImGui.PushStyleVar(ImGuiStyleVar.CellPadding, new float2(6f, 3f));
        if (!ImGui.BeginTable(id, 2, ImGuiTableFlags.SizingStretchProp | ImGuiTableFlags.NoPadOuterX))
        {
            ImGui.PopStyleVar();
            return false;
        }

        ImGui.TableSetupColumn("##label"u8, ImGuiTableColumnFlags.WidthStretch, 1f);
        ImGui.TableSetupColumn("##value"u8, ImGuiTableColumnFlags.WidthStretch, 2.4f);
        return true;
    }

    private static void EndRows()
    {
        EndRows();
    }

    private static void Row(ImString label)
    {
        ImGui.TableNextRow();
        ImGui.TableNextColumn();
        ImGui.AlignTextToFramePadding();
        ImGui.TextColored(Label, label);
        ImGui.TableNextColumn();
        ImGui.AlignTextToFramePadding();
    }

    private static string Ago(double seconds) => seconds switch
    {
        < 2 => "just now",
        < 90 => $"{seconds:0}s ago",
        < 5400 => $"{seconds / 60:0}m ago",
        _ => $"{seconds / 3600:0}h ago",
    };
}
