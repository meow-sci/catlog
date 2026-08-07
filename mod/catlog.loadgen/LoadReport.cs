using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Text.Json;

namespace MeowSci.Catlog.LoadGen;

/// <summary>One invariant check.</summary>
/// <param name="Ok">Whether it held.</param>
/// <param name="Label">What was checked.</param>
/// <param name="Expected">The value required.</param>
/// <param name="Actual">The value observed.</param>
/// <param name="Note">Why this check exists.</param>
internal sealed record Check(bool Ok, string Label, string Expected, string Actual, string Note);

/// <summary>A board and what the run put on it.</summary>
/// <param name="Stat">The board's stat key.</param>
/// <param name="Rows">How many rows the first page returned.</param>
/// <param name="TopHandle">The rank-1 handle, or an empty string.</param>
/// <param name="TopValue">The rank-1 value.</param>
internal sealed record BoardSummary(string Stat, int Rows, string TopHandle, double TopValue);

/// <summary>
/// Everything the run measured, and the two renderings of it.
/// </summary>
/// <remarks>
/// A load harness whose only output is "done" is not useful, so this deliberately reports the
/// numbers that let someone decide what to do next: where the events went, what the server said
/// about them, how long each leg took, and — the one that matters most — which limit was actually
/// binding.
/// </remarks>
internal sealed class RunReport
{
    /// <summary>Creates a report for a run.</summary>
    /// <param name="options">The run's options.</param>
    internal RunReport(LoadOptions options) => Options = options;

    /// <summary>The run's options.</summary>
    internal LoadOptions Options { get; }

    /// <summary>Subjects mockidp minted, including the deliberately too-new ones.</summary>
    internal int SubjectsMinted { get; set; }

    /// <summary>Subjects minted younger than the ≥30-day gate.</summary>
    internal int TooNewMinted { get; set; }

    /// <summary>Too-new subjects catlogd actually refused with <c>account_too_new</c>.</summary>
    internal int TooNewRefused { get; set; }

    /// <summary>Too-new subjects catlogd let through. Any of these is a bug.</summary>
    internal int TooNewAccepted { get; set; }

    /// <summary>Players provisioned successfully.</summary>
    internal int ProvisionOk { get; set; }

    /// <summary>Provisioning attempts that failed for a reason other than the age gate.</summary>
    internal int ProvisionFailed { get; set; }

    /// <summary>Successful provisions by identity provider.</summary>
    internal SortedDictionary<string, int> ProvisionByIdP { get; } = [];

    /// <summary>Distinct provisioning failure reasons, with counts.</summary>
    internal SortedDictionary<string, int> ProvisionErrors { get; } = [];

    /// <summary>How long provisioning took.</summary>
    internal TimeSpan ProvisionElapsed { get; set; }

    /// <summary>Per-player outcomes.</summary>
    internal List<PlayerResult> Players { get; } = [];

    /// <summary>Ingest request statistics.</summary>
    internal HttpStats Ingest { get; } = new("ingest");

    /// <summary>How long the ingest phase took, from first player start to last player finish.</summary>
    internal TimeSpan IngestElapsed { get; set; }

    /// <summary><c>events.total</c> before and after the ingest phase.</summary>
    internal long BaselineEvents { get; set; }

    /// <summary><c>events.total</c> after the ingest phase.</summary>
    internal long FinalEvents { get; set; }

    /// <summary><c>/debug/vars</c> before the run.</summary>
    internal IReadOnlyDictionary<string, double> BaselineVars { get; set; } = new Dictionary<string, double>();

    /// <summary><c>/debug/vars</c> after the run.</summary>
    internal IReadOnlyDictionary<string, double> FinalVars { get; set; } = new Dictionary<string, double>();

    /// <summary>Read-API request statistics.</summary>
    internal HttpStats? Read { get; set; }

    /// <summary>Live-feed frames received.</summary>
    internal int FeedFrames { get; set; }

    /// <summary>Whether the live-feed subscription connected at all.</summary>
    internal bool FeedConnected { get; set; }

    /// <summary>How long the projector took to reach the head after ingest stopped.</summary>
    internal TimeSpan ProjectorCatchUp { get; set; }

    /// <summary>The projector's checkpoint once it settled.</summary>
    internal long CheckpointSeq { get; set; }

    /// <summary>The event log's head sequence.</summary>
    internal long MaxSeq { get; set; }

    /// <summary>One line describing what the replay probe did, or an empty string when it was off.</summary>
    internal string DedupProbe { get; set; } = string.Empty;

    /// <summary>Whether the replay probe got the short-circuit it asked for.</summary>
    internal bool DedupProbeOk { get; set; }

    /// <summary>One line per moderation action taken.</summary>
    internal List<string> Moderation { get; } = [];

    /// <summary>The boards, once the projector settled.</summary>
    internal List<BoardSummary> Boards { get; } = [];

    /// <summary>How many of this run's non-moderated players are on at least one board.</summary>
    internal int PlayersVisible { get; set; }

    /// <summary>How many were checked for visibility.</summary>
    internal int PlayersChecked { get; set; }

    /// <summary>The invariant checks, when <c>--assert</c> was given.</summary>
    internal List<Check> Checks { get; } = [];

    /// <summary>Total wall time.</summary>
    internal TimeSpan Elapsed { get; set; }

    /// <summary>WARN lines the client library emitted, mostly the expected clock resyncs.</summary>
    internal int LibWarnings { get; set; }

    /// <summary>ERROR lines the client library emitted. Any of these is worth reading.</summary>
    internal int LibErrors { get; set; }

    /// <summary>True when every check passed (or none were requested).</summary>
    internal bool AllOk
    {
        get
        {
            foreach (Check check in Checks)
            {
                if (!check.Ok)
                    return false;
            }

            return true;
        }
    }

    // --- aggregates --------------------------------------------------------------------

    /// <summary>Events the client pipeline produced across every player.</summary>
    internal long EventsGenerated => Sum(static p => p.Events);

    /// <summary>Batches the server accepted or replayed.</summary>
    internal long Batches => Sum(static p => p.Batches);

    /// <summary>Events the server said it stored.</summary>
    internal long ServerAccepted => Sum(static p => p.ServerAccepted);

    /// <summary>Events the server said it had already seen.</summary>
    internal long ServerDeduped => Sum(static p => p.ServerDeduped);

    /// <summary>Frames published.</summary>
    internal long Frames => Sum(static p => p.Frames);

    /// <summary><c>401 clock_skew</c> recoveries — expected under a virtual clock.</summary>
    internal long ClockResyncs => Sum(static p => p.ClockResyncs);

    /// <summary>409 recoveries.</summary>
    internal long StreamForks => Sum(static p => p.StreamForks);

    /// <summary>413 recoveries.</summary>
    internal long Oversize => Sum(static p => p.Oversize);

    /// <summary>429 responses absorbed.</summary>
    internal long RateLimited => Sum(static p => p.RateLimited);

    /// <summary>503 responses absorbed — the server's write channel pushing back.</summary>
    internal long Busy => Sum(static p => p.Busy);

    /// <summary>Players that stopped early.</summary>
    internal int PlayersWithErrors
    {
        get
        {
            int n = 0;
            foreach (PlayerResult player in Players)
            {
                if (player.Error.Length > 0)
                    n++;
            }

            return n;
        }
    }

    /// <summary>The stored-event delta the run is responsible for.</summary>
    internal long EventsStored => FinalEvents - BaselineEvents;

    /// <summary>Events per second of the ingest phase.</summary>
    internal double EventsPerSecond
        => IngestElapsed.TotalSeconds > 0 ? EventsGenerated / IngestElapsed.TotalSeconds : 0;

    /// <summary>Batches per second of the ingest phase.</summary>
    internal double BatchesPerSecond
        => IngestElapsed.TotalSeconds > 0 ? Batches / IngestElapsed.TotalSeconds : 0;

    /// <summary>Simulated play time across every player, in hours.</summary>
    internal double SimulatedHours => Players.Count * Options.DurationSeconds / 3600.0;

    /// <summary>The combined event-stream digest — the value two runs of one seed must agree on.</summary>
    internal string Digest
    {
        get
        {
            var digests = new List<string>(Players.Count);
            foreach (PlayerResult player in Players)
                digests.Add(player.Index.ToString(CultureInfo.InvariantCulture) + ":" + player.Digest);
            return StreamDigest.Combine(digests);
        }
    }

    /// <summary>Envelope counts per type across every player.</summary>
    internal SortedDictionary<string, long> EventsByType()
    {
        var totals = new SortedDictionary<string, long>(StringComparer.Ordinal);
        foreach (PlayerResult player in Players)
        {
            foreach ((string type, int count) in player.EventsByType)
                totals[type] = totals.TryGetValue(type, out long n) ? n + count : count;
        }

        return totals;
    }

    /// <summary>A <c>/debug/vars</c> counter's movement across the run.</summary>
    /// <param name="name">The expvar name.</param>
    /// <returns>The delta.</returns>
    internal double VarDelta(string name)
        => (FinalVars.TryGetValue(name, out double after) ? after : 0)
           - (BaselineVars.TryGetValue(name, out double before) ? before : 0);

    /// <summary>
    /// The harness's own reading of which limit bound the run.
    /// </summary>
    /// <remarks>
    /// Stated rather than left to the reader, because the single most common way to misread a load
    /// test is to quote a throughput number that was actually the client's own throttle. The order
    /// below is the order in which a constraint, if present, dominates everything below it.
    /// </remarks>
    internal string Bottleneck()
    {
        if (Players.Count == 0)
            return "nothing ran";
        if (Busy > 0)
        {
            return $"the server's bounded write channel: {Busy} × 503 + Retry-After. "
                   + "Ingest offered work faster than the single writer goroutine could commit it.";
        }
        if (RateLimited > Batches * 0.15)
        {
            return $"the server's per-credential token bucket: {RateLimited} × 429 against "
                   + $"{Batches} accepted batches (1 batch / 2 s, burst 5, per jkt). "
                   + "More players raises aggregate throughput; a bigger --batch lowers the request rate.";
        }
        if (Options.Clock == ShipClock.Real)
        {
            return $"the client's own {MeowSci.Catlog.Lib.Wire.MinShipIntervalSeconds:0}-second ship floor, "
                   + "which is exactly what --clock real is for measuring.";
        }
        if (RateLimited > 0)
        {
            return $"mixed: {RateLimited} × 429 from the per-credential bucket, but under the "
                   + "threshold where it dominates. The remaining time is generation plus ingest latency.";
        }

        return "neither the token bucket nor the write channel ever pushed back — the run was "
               + "bounded by event generation and ingest round-trip time on this machine.";
    }

    // --- rendering ---------------------------------------------------------------------

    /// <summary>Writes the human-readable report to stdout.</summary>
    internal void WriteText()
    {
        Console.WriteLine();
        Console.WriteLine("═══ configuration ═══════════════════════════════════════════════");
        Row("server", $"{Options.Server}   (ingest {Options.IngestUrl})");
        Row("admin", Options.Admin);
        if (Options.Auth == AuthMode.OAuth)
            Row("mockidp", Options.MockIdp);
        Row("auth", Options.Auth == AuthMode.OAuth
            ? "oauth — mockidp → catlogd callback → session → POST /api/handles"
            : "admin — POST /admin/issue (identity stack bypassed)");
        Row("players", $"{Options.Players} requested, {Players.Count} ran, concurrency {Options.Concurrency}");
        Row("simulated", $"{Fmt(Options.DurationSeconds / 60)} min each — {Fmt(SimulatedHours)} player-hours total");
        Row("seed", $"{Options.Seed}{(Options.SeedWasGenerated ? "  (chosen; pass --seed to replay)" : string.Empty)}");
        Row("namespace", Options.Namespace);
        Row("ship", $"batch ≤ {Options.BatchEvents} events, age trigger {Fmt(Options.ShipAgeSeconds)} sim s, "
                    + $"hard floor {Fmt(MeowSci.Catlog.Lib.Wire.MinShipIntervalSeconds)} s on the "
                    + (Options.Clock == ShipClock.Virtual ? "virtual clock" : "real clock"));

        Console.WriteLine();
        Console.WriteLine("═══ provisioning ════════════════════════════════════════════════");
        Row("subjects minted", $"{SubjectsMinted} at mockidp ({TooNewMinted} deliberately too young)");
        Row("age gate", TooNewMinted == 0
            ? "(no too-new identities requested)"
            : $"{TooNewRefused}/{TooNewMinted} refused with account_too_new"
              + (TooNewAccepted > 0 ? $"  ** {TooNewAccepted} LET THROUGH **" : string.Empty));
        Row("provisioned", $"{ProvisionOk} ok, {ProvisionFailed} failed, in {Fmt(ProvisionElapsed.TotalSeconds)} s");
        if (ProvisionByIdP.Count > 0)
        {
            var parts = new List<string>(ProvisionByIdP.Count);
            foreach ((string idp, int count) in ProvisionByIdP)
                parts.Add($"{idp}×{count}");
            Row("by idp", string.Join("  ", parts));
        }

        foreach ((string reason, int count) in ProvisionErrors)
            Row("  failure", $"{count}× {reason}");

        Console.WriteLine();
        Console.WriteLine("═══ ingest ══════════════════════════════════════════════════════");
        Row("events generated", $"{EventsGenerated}   over {Frames} telemetry frames");
        Row("events stored", $"{EventsStored}   (events.total {BaselineEvents} → {FinalEvents})");
        Row("server said", $"accepted {ServerAccepted}, deduped {ServerDeduped}");
        Row("batches", $"{Batches}   ({Fmt(EventsGenerated / Math.Max(1.0, Batches))} events each)");
        Row("wall clock", $"{Fmt(IngestElapsed.TotalSeconds)} s");
        Row("throughput", $"{Fmt(EventsPerSecond)} events/s   {Fmt(BatchesPerSecond)} batches/s   "
                          + $"{Fmt(Ingest.BytesSent / 1024.0 / 1024.0)} MiB on the wire "
                          + "(every attempt, retries included)");
        Row("latency", $"mean {Fmt(Ingest.MeanMs)} ms   p50 {Fmt(Ingest.Percentile(50))}   "
                       + $"p90 {Fmt(Ingest.Percentile(90))}   p99 {Fmt(Ingest.Percentile(99))}   "
                       + $"max {Fmt(Ingest.Percentile(100))}");
        Row("status", Ingest.StatusLine());
        Row("recoveries", $"clock resync {ClockResyncs}   stream fork {StreamForks}   oversize {Oversize}   "
                          + $"rate limited {RateLimited}   busy(503) {Busy}");
        if (Ingest.TransportErrors > 0)
            Row("transport", $"{Ingest.TransportErrors} requests never completed");
        if (PlayersWithErrors > 0)
        {
            Row("players failed", PlayersWithErrors.ToString(CultureInfo.InvariantCulture));
            int shown = 0;
            foreach (PlayerResult player in Players)
            {
                if (player.Error.Length == 0 || shown++ >= 5)
                    continue;
                Row("  " + player.Handle, player.Error);
            }
        }

        Console.WriteLine();
        Console.WriteLine("═══ events by type ══════════════════════════════════════════════");
        foreach ((string type, long count) in EventsByType())
            Console.WriteLine($"  {count,10}  {type}");
        Row("digest", Digest + "   (a re-run with --seed " + Options.Seed + " must print this again)");

        Console.WriteLine();
        Console.WriteLine("═══ read side ═══════════════════════════════════════════════════");
        if (Read is { Requests: > 0 })
        {
            Row("requests", $"{Read.Requests} in flight with ingest, {Read.Successes} on 2xx");
            Row("latency", $"mean {Fmt(Read.MeanMs)} ms   p50 {Fmt(Read.Percentile(50))}   "
                           + $"p90 {Fmt(Read.Percentile(90))}   p99 {Fmt(Read.Percentile(99))}");
            Row("status", Read.StatusLine());
        }
        else
        {
            Row("requests", "(no readers)");
        }

        Row("live feed", FeedConnected
            ? $"{FeedFrames} frames on GET /v1/feed/stream"
            : "(not subscribed)");
        Row("projector", $"caught up in {Fmt(ProjectorCatchUp.TotalSeconds)} s — "
                         + $"checkpoint_seq {CheckpointSeq} == events.max_seq {MaxSeq}");
        if (DedupProbe.Length > 0)
            Row("replay probe", DedupProbe);

        if (Moderation.Count > 0)
        {
            Console.WriteLine();
            Console.WriteLine("═══ moderation ══════════════════════════════════════════════════");
            foreach (string line in Moderation)
                Console.WriteLine("  " + line);
        }

        Console.WriteLine();
        Console.WriteLine("═══ boards ══════════════════════════════════════════════════════");
        foreach (BoardSummary board in Boards)
        {
            Console.WriteLine(board.TopHandle.Length == 0
                ? $"  {board.Stat,-32} {board.Rows,5} rows"
                : $"  {board.Stat,-32} {board.Rows,5} rows   #1 {board.TopHandle} = {Fmt(board.TopValue)}");
        }

        Row("visibility", $"{PlayersVisible}/{PlayersChecked} players on at least one board");

        Console.WriteLine();
        Console.WriteLine("═══ verdict ═════════════════════════════════════════════════════");
        Row("bottleneck", Bottleneck());
        Row("client log", $"{LibWarnings} warnings, {LibErrors} errors from catlog.lib");
        Row("total", $"{Fmt(Elapsed.TotalSeconds)} s wall clock");

        if (Checks.Count == 0)
        {
            Console.WriteLine();
            Console.WriteLine("no invariants checked (pass --assert)");
            return;
        }

        int width = 4;
        foreach (Check check in Checks)
            width = Math.Max(width, check.Label.Length);

        Console.WriteLine();
        Console.WriteLine("═══ invariants ══════════════════════════════════════════════════");
        foreach (Check check in Checks)
        {
            Console.WriteLine($"  {(check.Ok ? "PASS" : "FAIL")}  {check.Label.PadRight(width)}  "
                              + $"expected {check.Expected}, got {check.Actual}");
            Console.WriteLine($"        {new string(' ', width)}  {check.Note}");
        }

        Console.WriteLine();
        if (AllOk)
        {
            Console.WriteLine($"OK — {Checks.Count} invariants held");
            return;
        }

        int failed = 0;
        foreach (Check check in Checks)
        {
            if (!check.Ok)
                failed++;
        }

        Console.Error.WriteLine($"FAILED — {failed} of {Checks.Count} invariants broke");
    }

    /// <summary>Writes the machine-readable report to stdout.</summary>
    internal void WriteJson()
    {
        var checks = new List<Dictionary<string, object>>(Checks.Count);
        foreach (Check check in Checks)
        {
            checks.Add(new Dictionary<string, object>(StringComparer.Ordinal)
            {
                ["ok"] = check.Ok, ["label"] = check.Label,
                ["expected"] = check.Expected, ["actual"] = check.Actual,
            });
        }

        var boards = new List<Dictionary<string, object>>(Boards.Count);
        foreach (BoardSummary board in Boards)
        {
            boards.Add(new Dictionary<string, object>(StringComparer.Ordinal)
            {
                ["stat"] = board.Stat, ["rows"] = board.Rows,
                ["top_handle"] = board.TopHandle, ["top_value"] = board.TopValue,
            });
        }

        var status = new Dictionary<string, long>(StringComparer.Ordinal);
        foreach ((int code, long count) in Ingest.ByStatus)
            status[code.ToString(CultureInfo.InvariantCulture)] = count;

        var byType = new Dictionary<string, long>(StringComparer.Ordinal);
        foreach ((string type, long count) in EventsByType())
            byType[type] = count;

        var payload = new Dictionary<string, object>(StringComparer.Ordinal)
        {
            ["seed"] = Options.Seed,
            ["namespace"] = Options.Namespace,
            ["players_requested"] = Options.Players,
            ["players_ran"] = Players.Count,
            ["concurrency"] = Options.Concurrency,
            ["duration_s"] = Options.DurationSeconds,
            ["auth"] = Options.Auth.ToString().ToLowerInvariant(),
            ["clock"] = Options.Clock.ToString().ToLowerInvariant(),
            ["digest"] = Digest,
            ["subjects_minted"] = SubjectsMinted,
            ["too_new_minted"] = TooNewMinted,
            ["too_new_refused"] = TooNewRefused,
            ["provisioned"] = ProvisionOk,
            ["provision_failed"] = ProvisionFailed,
            ["events_generated"] = EventsGenerated,
            ["events_stored"] = EventsStored,
            ["server_accepted"] = ServerAccepted,
            ["server_deduped"] = ServerDeduped,
            ["batches"] = Batches,
            ["frames"] = Frames,
            ["clock_resyncs"] = ClockResyncs,
            ["stream_forks"] = StreamForks,
            ["oversize"] = Oversize,
            ["rate_limited"] = RateLimited,
            ["busy_503"] = Busy,
            ["ingest_bytes"] = Ingest.BytesSent,
            ["ingest_elapsed_s"] = Math.Round(IngestElapsed.TotalSeconds, 3),
            ["events_per_second"] = Math.Round(EventsPerSecond, 2),
            ["batches_per_second"] = Math.Round(BatchesPerSecond, 3),
            ["ingest_latency_ms"] = new Dictionary<string, double>(StringComparer.Ordinal)
            {
                ["mean"] = Math.Round(Ingest.MeanMs, 2),
                ["p50"] = Math.Round(Ingest.Percentile(50), 2),
                ["p90"] = Math.Round(Ingest.Percentile(90), 2),
                ["p99"] = Math.Round(Ingest.Percentile(99), 2),
                ["max"] = Math.Round(Ingest.Percentile(100), 2),
            },
            ["ingest_status"] = status,
            ["read_requests"] = Read?.Requests ?? 0,
            ["read_latency_p99_ms"] = Math.Round(Read?.Percentile(99) ?? 0, 2),
            ["feed_frames"] = FeedFrames,
            ["projector_catchup_s"] = Math.Round(ProjectorCatchUp.TotalSeconds, 3),
            ["projector_checkpoint_seq"] = CheckpointSeq,
            ["events_max_seq"] = MaxSeq,
            ["events_by_type"] = byType,
            ["boards"] = boards,
            ["players_visible"] = PlayersVisible,
            ["players_checked"] = PlayersChecked,
            ["bottleneck"] = Bottleneck(),
            ["elapsed_s"] = Math.Round(Elapsed.TotalSeconds, 3),
            ["checks"] = checks,
            ["ok"] = AllOk,
        };

        Console.WriteLine(JsonSerializer.Serialize(payload, new JsonSerializerOptions { WriteIndented = true }));
    }

    private long Sum(Func<PlayerResult, long> selector)
    {
        long total = 0;
        foreach (PlayerResult player in Players)
            total += selector(player);
        return total;
    }

    private static void Row(string label, string value)
        => Console.WriteLine($"  {label.PadRight(17)} {value}");

    private static string Fmt(double value)
        => value == Math.Floor(value) && Math.Abs(value) < 1e12
            ? ((long)value).ToString(CultureInfo.InvariantCulture)
            : value.ToString("0.##", CultureInfo.InvariantCulture);
}

/// <summary>Small helpers shared by the orchestration.</summary>
internal static class Text
{
    /// <summary>Bumps a counter in a dictionary.</summary>
    /// <typeparam name="TKey">Key type.</typeparam>
    /// <param name="counts">The dictionary.</param>
    /// <param name="key">The key.</param>
    internal static void Bump<TKey>(this IDictionary<TKey, int> counts, TKey key)
        where TKey : notnull
        => counts[key] = counts.TryGetValue(key, out int n) ? n + 1 : 1;

    /// <summary>Trims a string for a one-line report row.</summary>
    /// <param name="value">The text.</param>
    /// <param name="max">The ceiling.</param>
    /// <returns>The trimmed text.</returns>
    internal static string Clip(string value, int max = 120)
    {
        string flat = value.Replace('\n', ' ').Replace('\r', ' ');
        return flat.Length <= max ? flat : flat[..max] + "…";
    }

    /// <summary>Renders a duration for a log line.</summary>
    /// <param name="span">The duration.</param>
    /// <returns>The rendering.</returns>
    internal static string Seconds(TimeSpan span)
        => span.TotalSeconds.ToString("0.00", CultureInfo.InvariantCulture) + " s";

    /// <summary>Builds a handle for a player, inside the §4.7 rules.</summary>
    /// <param name="ns">The run's identity namespace.</param>
    /// <param name="index">The player's index.</param>
    /// <returns>The handle.</returns>
    internal static string Handle(string ns, int index)
    {
        var clean = new StringBuilder(ns.Length);
        foreach (char c in ns)
        {
            if (char.IsAsciiLetterOrDigit(c))
                clean.Append(char.ToLowerInvariant(c));
        }

        if (clean.Length == 0)
            clean.Append("lg");
        // Handles are 1–150 characters of US-ASCII alphanumerics plus `.`, `_` and `-`, starting
        // and ending alphanumeric (§4.7). This is well inside that.
        if (clean.Length > 24)
            clean.Length = 24;
        return clean.ToString() + "_" + index.ToString("0000", CultureInfo.InvariantCulture);
    }
}
