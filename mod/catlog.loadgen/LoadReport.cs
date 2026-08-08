using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.LoadGen;

/// <summary>One invariant check.</summary>
/// <param name="Ok">Whether it held.</param>
/// <param name="Label">What was checked.</param>
/// <param name="Expected">The value required.</param>
/// <param name="Actual">The value observed.</param>
/// <param name="Note">Why this check exists.</param>
internal sealed record Check(bool Ok, string Label, string Expected, string Actual, string Note);

/// <summary>
/// The run's careers, summed across every player.
/// </summary>
/// <remarks>
/// Counters are plain arrays indexed by the enums rather than dictionaries, so the aggregate is
/// order-stable without a comparer and a re-run of a seed prints the same section in the same
/// order.
/// </remarks>
internal sealed class CareerRollup
{
    /// <summary>Players by the stage they were at when the window opened.</summary>
    internal int[] StartStage { get; } = new int[Careers.Stages.Length];

    /// <summary>Players by the stage they were at when it closed.</summary>
    internal int[] EndStage { get; } = new int[Careers.Stages.Length];

    /// <summary>Resident craft, by the owner's opening stage.</summary>
    internal int[] FleetAtStage { get; } = new int[Careers.Stages.Length];

    /// <summary>Players, by opening stage — the denominator for <see cref="FleetAtStage"/>.</summary>
    internal int[] PlayersAtStage { get; } = new int[Careers.Stages.Length];

    /// <summary>Players by temperament.</summary>
    internal int[] Temperaments { get; } = new int[Enum.GetValues<Temperament>().Length];

    /// <summary>Missions launched, by kind.</summary>
    internal int[] ByKind { get; } = new int[Careers.Kinds.Length];

    /// <summary>Missions lost, by the phase they were lost in.</summary>
    internal int[] ByPhase { get; } = new int[Careers.Phases.Length];

    /// <summary>Losses by phase for careers below <see cref="Careers.Seasoned"/>.</summary>
    internal int[] GreenPhase { get; } = new int[Careers.Phases.Length];

    /// <summary>Losses by phase for careers at or past <see cref="Careers.Seasoned"/>.</summary>
    internal int[] SeasonedPhase { get; } = new int[Careers.Phases.Length];

    /// <summary>Missions lost, by <c>vehicle.rud</c> cause.</summary>
    internal int[] ByCause { get; } = new int[Careers.Causes.Length];

    /// <summary>Distinct bodies arrived at, and how many players got to each.</summary>
    internal SortedDictionary<string, int> Bodies { get; } = new(StringComparer.Ordinal);

    /// <summary>Career ages in in-game hours, sorted, for the percentiles.</summary>
    internal List<double> Hours { get; } = [];

    /// <summary>Players that crossed a stage boundary during the run.</summary>
    internal int Advanced { get; set; }

    /// <summary>Resident craft across every save.</summary>
    internal long Fleet { get; set; }

    /// <summary>Missions launched.</summary>
    internal long Attempted { get; set; }

    /// <summary>Missions that reached their objective.</summary>
    internal long Completed { get; set; }

    /// <summary>Players that reached at least one body other than home.</summary>
    internal int PlayersOffWorld { get; set; }

    /// <summary>A percentile of the career-age distribution, in in-game hours.</summary>
    /// <param name="percent">The percentile, 0–100.</param>
    /// <returns>The value, or 0 when nothing ran.</returns>
    internal double HoursAt(double percent)
    {
        if (Hours.Count == 0)
            return 0;
        int index = (int)Math.Round((percent / 100.0) * (Hours.Count - 1));
        return Hours[Math.Clamp(index, 0, Hours.Count - 1)];
    }
}

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

    /// <summary>
    /// Where the players' wall clock went, summed across every player.
    /// </summary>
    /// <remarks>
    /// These are <b>player-seconds</b>, not wall seconds: players run <c>--concurrency</c> at a
    /// time, so the run's ingest phase costs roughly <c>total / concurrency</c> of wall clock. That
    /// is the arithmetic that turns "885 rate-limit waits × 2 s" into "seventy of the eighty-three
    /// seconds this run took", and it is why the table prints both.
    /// </remarks>
    internal PlayerTiming Timing
    {
        get
        {
            var total = default(PlayerTiming);
            foreach (PlayerResult player in Players)
                total += player.Timing;
            return total;
        }
    }

    /// <summary>Total player-seconds the ingest phase accounted for.</summary>
    internal double PlayerSeconds
    {
        get
        {
            double total = 0;
            foreach (PlayerResult player in Players)
                total += player.Elapsed.TotalSeconds;
            return total;
        }
    }

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

    /// <summary>
    /// The run's careers, added up.
    /// </summary>
    /// <remarks>
    /// The point of this section is that a reader can decide, from the numbers alone, whether the
    /// population looked like a player base: bottom-heavy in stage, growing in fleet size and
    /// concurrency with career age, and failing early when it is green and on landing when it is
    /// not. A run whose losses were spread evenly across phases would be visible here immediately.
    /// </remarks>
    /// <returns>The rollup.</returns>
    internal CareerRollup Rollup()
    {
        var roll = new CareerRollup();
        foreach (PlayerResult player in Players)
        {
            CareerSummary c = player.Career;
            roll.StartStage[(int)c.StartStage]++;
            roll.EndStage[(int)c.EndStage]++;
            roll.FleetAtStage[(int)c.StartStage] += c.Fleet;
            roll.PlayersAtStage[(int)c.StartStage]++;
            roll.Temperaments[(int)c.Temperament]++;
            if (c.EndStage != c.StartStage)
                roll.Advanced++;

            roll.Fleet += c.Fleet;
            roll.Attempted += c.Attempted;
            roll.Completed += c.Completed;
            roll.Hours.Add(c.CareerHours);

            Accumulate(roll.ByKind, c.ByKind);
            Accumulate(roll.ByPhase, c.FailuresByPhase);
            Accumulate(roll.ByCause, c.CausesByKind);
            Accumulate(
                c.StartStage < Careers.Seasoned ? roll.GreenPhase : roll.SeasonedPhase,
                c.FailuresByPhase);

            foreach (string body in c.BodiesReached)
                roll.Bodies[body] = roll.Bodies.TryGetValue(body, out int n) ? n + 1 : 1;

            if (c.BodiesReached.Count > 0)
                roll.PlayersOffWorld++;
        }

        roll.Hours.Sort();
        return roll;
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
                   + $"{Batches} accepted batches (1 batch / 2 s, burst 5, per jkt), costing "
                   + $"{Fmt(Timing.RateLimitWait.TotalSeconds)} player-seconds of Retry-After waiting. "
                   + "It is a per-credential limit, so more players raise aggregate throughput and a "
                   + "bigger --ship-age lowers the request rate. To take it out of the measurement "
                   + "entirely, start catlogd with CATLOG_LIMITS_RATELIMIT_DISABLED=1 (see --help, "
                   + "\"going fast\").";
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

        // Nothing pushed back, so the interesting question is which phase spent the run. The
        // projector is the usual answer at volume: it folds one event at a time on one goroutine
        // and is comfortably an order of magnitude slower than ingest, so a run big enough to be
        // worth doing spends most of its wall clock waiting for the head rather than reaching it.
        if (ProjectorCatchUp > IngestElapsed && ProjectorCatchUp.TotalSeconds > 1)
        {
            double rate = EventsStored / Math.Max(0.001, ProjectorCatchUp.TotalSeconds);
            return $"the projector, not ingest: {Fmt(IngestElapsed.TotalSeconds)} s to store "
                   + $"{EventsStored} events and {Fmt(ProjectorCatchUp.TotalSeconds)} s to fold them "
                   + $"(~{Fmt(rate)} events/s). Nothing pushed back on the write path. The wait is "
                   + "real work — --assert and every leaderboard read need the head — but it is what "
                   + "sets the ceiling on a run of this size.";
        }
        if (Timing.Generate > Timing.Ship && Options.Concurrency > Environment.ProcessorCount)
        {
            return "the harness itself: more player-seconds went into generating events than into "
                   + $"shipping them, at concurrency {Options.Concurrency} on {Environment.ProcessorCount} "
                   + "cores. With no server-side throttle to absorb, players are CPU-bound and "
                   + $"oversubscription costs throughput — try -c {Environment.ProcessorCount}.";
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

        WriteWhereTheTimeWent();
        WriteCareers();

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

    /// <summary>
    /// Writes the wall-clock accounting: where the run's seconds actually went.
    /// </summary>
    /// <remarks>
    /// <para>
    /// This section exists because every plausible explanation for a slow run — the client's hard
    /// 30-second ship floor, the server's token bucket, the bounded write channel, ECDSA, the
    /// projector — produces the same symptom, and arguing about which one it is from a throughput
    /// number is guesswork. Each is timed, and the largest number wins the argument.
    /// </para>
    /// <para>
    /// The two halves measure different things and are printed separately on purpose. The
    /// <b>phases</b> are wall clock, in sequence, and they sum to the run. The <b>player time</b> is
    /// player-seconds spent concurrently inside the ingest phase; divide by <c>--concurrency</c> to
    /// turn it back into wall clock.
    /// </para>
    /// </remarks>
    private void WriteWhereTheTimeWent()
    {
        if (Players.Count == 0)
            return;

        PlayerTiming t = Timing;
        double players = Math.Max(1.0, PlayerSeconds);
        double run = Math.Max(0.001, Elapsed.TotalSeconds);

        Console.WriteLine();
        Console.WriteLine("═══ where the time went ═════════════════════════════════════════");
        Row("phases", $"provision {Fmt(ProvisionElapsed.TotalSeconds)} s"
                      + $"   ingest {Fmt(IngestElapsed.TotalSeconds)} s"
                      + $"   projector {Fmt(ProjectorCatchUp.TotalSeconds)} s"
                      + $"   other {Fmt(Math.Max(0, run - ProvisionElapsed.TotalSeconds - IngestElapsed.TotalSeconds - ProjectorCatchUp.TotalSeconds))} s");
        Row("player time", $"{Fmt(players)} player-seconds across {Players.Count} players "
                           + $"at concurrency {Options.Concurrency}");
        Bucket("  generating", t.Generate, players);
        Bucket("  shipping", t.Ship, players);
        Bucket("  rate-limit wait", t.RateLimitWait, players);
        Bucket("  ship-floor wait", t.FloorWait, players);
        Bucket("  retry backoff", t.RetryWait, players);

        // The one line that answers the question everybody actually asks first.
        Row("ship floor", Options.Clock == ShipClock.Virtual
            ? $"{Fmt(t.FloorWait.TotalSeconds)} s of real waiting — the hard "
              + $"{Fmt(MeowSci.Catlog.Lib.Wire.MinShipIntervalSeconds)} s floor is enforced against the "
              + "injected clock, which is wound forward rather than slept through. It costs "
              + $"{ClockResyncs} clock resync(s), not wall time."
            : $"{Fmt(t.FloorWait.TotalSeconds)} player-seconds of real waiting — this is "
              + "--clock real, so the floor is the point of the run.");
    }

    /// <summary>Writes one timing bucket as seconds and as a share of the player time.</summary>
    /// <param name="label">The row label.</param>
    /// <param name="value">The bucket.</param>
    /// <param name="total">Total player-seconds.</param>
    private static void Bucket(string label, TimeSpan value, double total)
        => Row(label, $"{Fmt(value.TotalSeconds),10} s   {value.TotalSeconds / total * 100,5:0.0}%");

    /// <summary>
    /// Writes the careers section: what the population looked like and how it failed.
    /// </summary>
    private void WriteCareers()
    {
        if (Players.Count == 0)
            return;

        CareerRollup roll = Rollup();

        Console.WriteLine();
        Console.WriteLine("═══ careers ═════════════════════════════════════════════════════");
        Row("stage at start", Counts(Careers.Stages, roll.StartStage, static s => s.ToString().ToLowerInvariant()));
        Row("stage at end", Counts(Careers.Stages, roll.EndStage, static s => s.ToString().ToLowerInvariant())
                            + (roll.Advanced > 0 ? $"   ({roll.Advanced} advanced during the run)" : string.Empty));
        Row("career age", $"median {Fmt(roll.HoursAt(50))} h   p90 {Fmt(roll.HoursAt(90))} h   "
                          + $"oldest {Fmt(roll.HoursAt(100))} h   (in-game, prior + simulated)");
        Row("temperament", Counts(
            Enum.GetValues<Temperament>(), roll.Temperaments, static t => t.ToString().ToLowerInvariant()));

        var fleet = new List<string>(Careers.Stages.Length);
        foreach (CareerStage stage in Careers.Stages)
        {
            int players = roll.PlayersAtStage[(int)stage];
            if (players == 0)
                continue;
            fleet.Add(string.Create(CultureInfo.InvariantCulture,
                $"{stage.ToString().ToLowerInvariant()} {roll.FleetAtStage[(int)stage] / (double)players:0.#}"));
        }

        Row("fleet", $"{roll.Fleet} resident craft — per player by stage: " + string.Join("  ", fleet));

        long lost = roll.Attempted - roll.Completed;
        Row("missions", $"{roll.Attempted} attempted, {roll.Completed} completed "
                        + $"({(roll.Attempted == 0 ? 0 : 100.0 * roll.Completed / roll.Attempted):0}%), "
                        + $"{lost} lost");
        Row("by kind", Counts(Careers.Kinds, roll.ByKind, Careers.Label));
        // Split, because the aggregate hides the whole point: a population that is mostly beginners
        // fails mostly on the pad whatever the veterans are doing.
        Row("lost — green", Counts(Careers.Phases, roll.GreenPhase, Careers.Label));
        Row("lost — veteran", Counts(Careers.Phases, roll.SeasonedPhase, Careers.Label));
        Row("rud cause", Counts(Careers.Causes, roll.ByCause, Careers.Label));

        var bodies = new List<string>(roll.Bodies.Count);
        foreach (LoadBody body in LoadBodies.All)
        {
            if (roll.Bodies.TryGetValue(body.Name, out int players))
                bodies.Add($"{body.Name} {players}");
        }

        Row("bodies reached", bodies.Count == 0
            ? "(nobody left the home SOI)"
            : string.Join("  ", bodies) + $"   — {roll.PlayersOffWorld}/{Players.Count} players got somewhere else");
    }

    private static string Counts<T>(IReadOnlyList<T> values, IReadOnlyList<int> counts, Func<T, string> label)
    {
        var parts = new List<string>(values.Count);
        for (int i = 0; i < values.Count && i < counts.Count; i++)
        {
            if (counts[i] > 0)
                parts.Add(label(values[i]) + " " + counts[i].ToString(CultureInfo.InvariantCulture));
        }

        return parts.Count == 0 ? "(none)" : string.Join("  ", parts);
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
            // Player-seconds, summed across players running --concurrency at a time; divide by the
            // concurrency to read them as wall clock. See the "where the time went" table.
            ["player_time_s"] = new Dictionary<string, double>(StringComparer.Ordinal)
            {
                ["total"] = Math.Round(PlayerSeconds, 3),
                ["generate"] = Math.Round(Timing.Generate.TotalSeconds, 3),
                ["ship"] = Math.Round(Timing.Ship.TotalSeconds, 3),
                ["rate_limit_wait"] = Math.Round(Timing.RateLimitWait.TotalSeconds, 3),
                ["ship_floor_wait"] = Math.Round(Timing.FloorWait.TotalSeconds, 3),
                ["retry_wait"] = Math.Round(Timing.RetryWait.TotalSeconds, 3),
            },
            ["provision_elapsed_s"] = Math.Round(ProvisionElapsed.TotalSeconds, 3),
            ["read_requests"] = Read?.Requests ?? 0,
            ["read_latency_p99_ms"] = Math.Round(Read?.Percentile(99) ?? 0, 2),
            ["feed_frames"] = FeedFrames,
            ["projector_catchup_s"] = Math.Round(ProjectorCatchUp.TotalSeconds, 3),
            ["projector_checkpoint_seq"] = CheckpointSeq,
            ["events_max_seq"] = MaxSeq,
            ["events_by_type"] = byType,
            ["careers"] = CareersJson(),
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

    private Dictionary<string, object> CareersJson()
    {
        CareerRollup roll = Rollup();
        var bodies = new Dictionary<string, int>(StringComparer.Ordinal);
        foreach ((string body, int players) in roll.Bodies)
            bodies[body] = players;

        return new Dictionary<string, object>(StringComparer.Ordinal)
        {
            ["stage_at_start"] = Map(Careers.Stages, roll.StartStage, static s => s.ToString().ToLowerInvariant()),
            ["stage_at_end"] = Map(Careers.Stages, roll.EndStage, static s => s.ToString().ToLowerInvariant()),
            ["advanced_a_stage"] = roll.Advanced,
            ["temperament"] = Map(
                Enum.GetValues<Temperament>(), roll.Temperaments, static t => t.ToString().ToLowerInvariant()),
            ["career_hours"] = new Dictionary<string, double>(StringComparer.Ordinal)
            {
                ["p50"] = Math.Round(roll.HoursAt(50), 2),
                ["p90"] = Math.Round(roll.HoursAt(90), 2),
                ["max"] = Math.Round(roll.HoursAt(100), 2),
            },
            ["resident_craft"] = roll.Fleet,
            ["missions_attempted"] = roll.Attempted,
            ["missions_completed"] = roll.Completed,
            ["missions_by_kind"] = Map(Careers.Kinds, roll.ByKind, Careers.Label),
            ["losses_by_phase"] = Map(Careers.Phases, roll.ByPhase, Careers.Label),
            ["losses_by_phase_green"] = Map(Careers.Phases, roll.GreenPhase, Careers.Label),
            ["losses_by_phase_veteran"] = Map(Careers.Phases, roll.SeasonedPhase, Careers.Label),
            ["losses_by_cause"] = Map(Careers.Causes, roll.ByCause, Careers.Label),
            ["bodies_reached"] = bodies,
            ["players_off_world"] = roll.PlayersOffWorld,
        };
    }

    private static Dictionary<string, int> Map<T>(
        IReadOnlyList<T> values, IReadOnlyList<int> counts, Func<T, string> label)
    {
        var map = new Dictionary<string, int>(StringComparer.Ordinal);
        for (int i = 0; i < values.Count && i < counts.Count; i++)
        {
            if (counts[i] > 0)
                map[label(values[i])] = counts[i];
        }

        return map;
    }

    private static void Accumulate(int[] into, IReadOnlyList<int> from)
    {
        for (int i = 0; i < into.Length && i < from.Count; i++)
            into[i] += from[i];
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

    /// <summary>
    /// Adjectives for <see cref="Handle"/>. 64 entries — the count is load-bearing, see there.
    /// </summary>
    private static readonly string[] Adjectives =
    [
        "amber", "ancient", "bashful", "blazing", "bold", "brave", "brisk", "chipper",
        "clever", "cosmic", "cranky", "crisp", "curious", "dapper", "daring", "dizzy",
        "dreamy", "eager", "electric", "fearless", "fluffy", "frosty", "gallant", "gentle",
        "gleaming", "glorious", "grumpy", "hasty", "hungry", "idle", "jolly", "keen",
        "lucky", "lunar", "merry", "mighty", "nimble", "noble", "opal", "patient",
        "peculiar", "plucky", "polite", "quiet", "restless", "rowdy", "rusty", "scruffy",
        "serene", "sleepy", "smitten", "snug", "solar", "spry", "stellar", "sturdy",
        "sunny", "tidy", "tiny", "valiant", "velvet", "wandering", "whiskered", "wistful",
    ];

    /// <summary>
    /// Nouns for <see cref="Handle"/>. 64 entries — the count is load-bearing, see there.
    /// </summary>
    private static readonly string[] Nouns =
    [
        "airlock", "apogee", "ascent", "aurora", "beacon", "biscuit", "booster", "cabin",
        "canopy", "comet", "compass", "corona", "cradle", "crater", "dynamo", "eclipse",
        "ember", "fairing", "ferry", "gantry", "gasket", "gimbal", "hatch", "kepler",
        "kitten", "lander", "lantern", "lattice", "meridian", "meteor", "mitten", "nebula",
        "nozzle", "orbit", "paddock", "parsec", "payload", "pinwheel", "piston", "plume",
        "quasar", "radiator", "rover", "rudder", "saucer", "sextant", "shuttle", "sprocket",
        "stanchion", "starling", "strut", "tether", "thruster", "trellis", "truss", "turbine",
        "vector", "vernier", "voyager", "whisker", "window", "zenith", "zephyr", "zodiac",
    ];

    /// <summary>Builds a handle for a player, inside the §4.7 rules.</summary>
    /// <param name="ns">The run's identity namespace.</param>
    /// <param name="index">The player's index.</param>
    /// <returns>The handle.</returns>
    /// <remarks>
    /// <para>
    /// Docker-style <c>adjective_noun</c>, because a page of <c>lg19fdd32c036_0018</c> is
    /// unreadable and a load run's whole purpose is to be looked at. Underscore, not a space:
    /// §4.7 allows US-ASCII alphanumerics plus <c>. _ -</c> only, so a space would come back
    /// <c>handle_invalid</c> and provision nothing.
    /// </para>
    /// <para>
    /// <b>The namespace suffix is not decoration.</b> Handles are globally unique and never
    /// recycled (D9), so a second run that re-drew <c>plucky_pinwheel</c> would fail to claim
    /// it forever after. The suffix is derived from the namespace — which defaults to a
    /// timestamp — so each run occupies its own corner of the handle space.
    /// </para>
    /// <para>
    /// The pair is chosen by walking the cross product with a stride coprime to its size, so
    /// distinct indices give distinct pairs: no collision handling, and no chance of two
    /// players in one run racing for one handle. That holds for the first
    /// <see cref="Adjectives"/> × <see cref="Nouns"/> = 4096 players; beyond that the walk
    /// wraps and the index is appended to keep it unique. The stride is offset by the
    /// namespace hash so two runs get different names rather than the same list twice.
    /// </para>
    /// </remarks>
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

        // A short, stable suffix rather than the whole namespace: enough to separate runs,
        // short enough that the readable half stays the part you notice.
        uint hash = 2166136261u;
        foreach (char c in clean.ToString())
            hash = (hash ^ c) * 16777619u;
        string suffix = (hash % 0xFFFFFu).ToString("x5", CultureInfo.InvariantCulture);

        // index → slot must be a bijection, or two players race for one handle. Both steps
        // below are bijections on 12 bits, so their composition is one:
        //
        //   1. multiply by an odd number — invertible modulo any power of two;
        //   2. rotate the 12 bits — a permutation.
        //
        // The rotation is why it is here rather than the multiply alone. A constant stride
        // moves the low bits (the noun) briskly and the high bits (the adjective) by a near
        // constant, so consecutive players came out `gleaming_saucer`, `blazing_kepler`,
        // `rusty_ascent`, `gleaming_sextant` — a three-adjective cycle you notice immediately.
        // Rotating carries the fast-moving low bits up into the adjective.
        int combos = Adjectives.Length * Nouns.Length; // 4096 = 2^12
        uint mixed = ((uint)index * 2731u + hash) & 0xFFFu;
        uint slot = ((mixed << 5) | (mixed >> 7)) & 0xFFFu;
        string pair = Adjectives[(int)slot / Nouns.Length] + "_" + Nouns[(int)slot % Nouns.Length];

        return index < combos
            ? pair + "_" + suffix
            : pair + "_" + suffix + index.ToString("0000", CultureInfo.InvariantCulture);
    }
}
