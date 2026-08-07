using System;
using System.Collections.Generic;
using System.Globalization;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// The properties a run has to have, whatever the numbers were.
/// </summary>
/// <remarks>
/// <para>
/// These are deliberately <b>invariants</b> rather than expected values. A randomised harness
/// cannot assert that <c>biggest_lithobrake_survived</c> is 62 — that is <c>catlog.sim</c>'s job
/// and it must stay <c>catlog.sim</c>'s job. What it can assert is the set of things that must be
/// true no matter what was generated: nothing was lost, nothing arrived twice that should not
/// have, nothing was refused that should not have been, the projector reached the head, and every
/// player who shipped something is visible to a reader.
/// </para>
/// <para>
/// Each check states what it is protecting against, because a failing assertion whose purpose has
/// to be reconstructed is an assertion that gets deleted.
/// </para>
/// </remarks>
internal static class Invariants
{
    /// <summary>Runs every check and records it on the report.</summary>
    /// <param name="options">The run's options.</param>
    /// <param name="report">The report to append to.</param>
    /// <param name="api">The read/admin API client.</param>
    /// <param name="accounts">The players that ran.</param>
    internal static void Check(
        LoadOptions options, RunReport report, ReadApiClient api, IReadOnlyList<PlayerAccount> accounts)
    {
        ZeroLoss(report);
        NoUnexpectedRejections(report);
        Dedup(options, report);
        AgeGate(report);
        Projector(report);
        Visibility(report);
        PlayersFinished(report);
        Taxonomy(report);
        ReadSide(report);
        Moderated(api, report, accounts);
    }

    /// <summary>
    /// Every event the client produced reached <c>events.db</c>, exactly once.
    /// </summary>
    /// <remarks>
    /// The single most important property in the whole system: a telemetry pipeline that silently
    /// drops is worse than one that visibly fails, because the leaderboard still looks plausible.
    /// A dropped batch, a mis-chained <c>ph</c>, an off-by-one in the outbox drain and a
    /// double-delete all land here.
    /// </remarks>
    private static void ZeroLoss(RunReport report)
    {
        report.Checks.Add(new Check(
            Ok: report.EventsStored == report.EventsGenerated,
            Label: "events.total delta",
            Expected: report.EventsGenerated.ToString(CultureInfo.InvariantCulture),
            Actual: report.EventsStored.ToString(CultureInfo.InvariantCulture),
            Note: "zero silent loss: every envelope the pipeline produced is in events.db, exactly once"));

        double accepted = report.VarDelta("ingest_accepted");
        report.Checks.Add(new Check(
            Ok: Math.Abs(accepted - report.EventsGenerated) < 0.5,
            Label: "ingest_accepted delta",
            Expected: report.EventsGenerated.ToString(CultureInfo.InvariantCulture),
            Actual: accepted.ToString("0", CultureInfo.InvariantCulture),
            Note: "every envelope was accepted on its first presentation, not on a retry"));
    }

    /// <summary>
    /// Nothing was refused that should not have been.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Four statuses are <b>expected</b> and are excluded by name:
    /// </para>
    /// <list type="bullet">
    ///   <item><description><c>429</c> — the per-credential token bucket. Hitting it is the point.</description></item>
    ///   <item><description><c>503</c> — the bounded write channel. Also the point; the client backs off and retries.</description></item>
    ///   <item><description>
    ///     <c>401 clock_skew</c> under <c>--clock virtual</c> — a client whose floor is wound
    ///     forward leaves the ±300 s proof window and relearns the offset from the <c>Date</c>
    ///     header. Counted as <c>ClockResyncs</c> and allowed exactly that many times.
    ///   </description></item>
    ///   <item><description><c>409</c> / <c>413</c> — the stream-fork and oversize ladders, if they fired at all.</description></item>
    /// </list>
    /// <para>
    /// Everything else — a <c>400</c>, a <c>415</c>, an unexplained <c>401</c>, a <c>5xx</c> that
    /// is not the queue — means the client built something the server would not take, and that is
    /// a bug in one of them.
    /// </para>
    /// </remarks>
    private static void NoUnexpectedRejections(RunReport report)
    {
        long unexplained = 0;
        var offenders = new List<string>();

        foreach ((int status, long count) in report.Ingest.ByStatus)
        {
            if (status is >= 200 and < 300)
                continue;

            long allowed = status switch
            {
                429 => count,
                503 => count,
                401 => report.ClockResyncs,
                409 => report.StreamForks,
                413 => report.Oversize,
                _ => 0,
            };

            if (count > allowed)
            {
                unexplained += count - allowed;
                offenders.Add(string.Create(CultureInfo.InvariantCulture, $"{status}×{count - allowed}"));
            }
        }

        report.Checks.Add(new Check(
            Ok: unexplained == 0 && report.Ingest.TransportErrors == 0,
            Label: "unexplained non-2xx",
            Expected: "0",
            Actual: unexplained.ToString(CultureInfo.InvariantCulture)
                    + (offenders.Count > 0 ? " (" + string.Join(" ", offenders) + ")" : string.Empty)
                    + (report.Ingest.TransportErrors > 0
                        ? $" + {report.Ingest.TransportErrors} transport failures"
                        : string.Empty),
            Note: "429/503 (backpressure), 401 clock_skew, 409 and 413 are accounted for by name; "
                  + "anything else means a batch the server would not take"));
    }

    /// <summary>
    /// Deduplication where it is expected, and nowhere else.
    /// </summary>
    /// <remarks>
    /// Both halves matter. A run that never re-sends must move <c>ingest_deduped</c> by exactly
    /// zero, or the client is shipping something twice. And the replay probe must be swallowed by
    /// §4.5.3 step 11 without storing anything, or the idempotency contract is not holding.
    /// </remarks>
    private static void Dedup(LoadOptions options, RunReport report)
    {
        // The probe is the only deliberate duplicate, and it is not counted in the players' own
        // ServerDeduped totals because it is sent outside the shipper.
        report.Checks.Add(new Check(
            Ok: report.ServerDeduped == 0,
            Label: "deduped during ingest",
            Expected: "0",
            Actual: report.ServerDeduped.ToString(CultureInfo.InvariantCulture),
            Note: "nothing was shipped twice: the outbox drains in order and only deletes on a 200"));

        if (!options.DedupProbe)
            return;

        report.Checks.Add(new Check(
            Ok: report.DedupProbeOk,
            Label: "replay short-circuit",
            Expected: "200 replay=true, deduped>0, events.total unchanged",
            Actual: report.DedupProbe.Length == 0 ? "(not run)" : report.DedupProbe,
            Note: "re-sending an accepted batch byte for byte must be swallowed by §4.5.3 step 11"));
    }

    /// <summary>Every deliberately-too-young account was refused, and none slipped through.</summary>
    private static void AgeGate(RunReport report)
    {
        if (report.TooNewMinted == 0)
            return;

        report.Checks.Add(new Check(
            Ok: report.TooNewAccepted == 0 && report.TooNewRefused == report.TooNewMinted,
            Label: "account-age gate",
            Expected: $"{report.TooNewMinted} refused, 0 accepted",
            Actual: $"{report.TooNewRefused} refused, {report.TooNewAccepted} accepted",
            Note: "§4.7's ≥30-day gate, exercised in the direction that must fail"));
    }

    /// <summary>The projector reached the head of the log.</summary>
    private static void Projector(RunReport report)
    {
        report.Checks.Add(new Check(
            Ok: report.CheckpointSeq == report.MaxSeq && report.MaxSeq > 0,
            Label: "projector at head",
            Expected: $"checkpoint_seq == events.max_seq ({report.MaxSeq})",
            Actual: report.CheckpointSeq.ToString(CultureInfo.InvariantCulture),
            Note: "everything stored has also been folded; a board read is now a statement about the data"));
    }

    /// <summary>Every player that shipped is visible to a reader.</summary>
    /// <remarks>
    /// This is the check that catches the failure mode nothing else does: a player whose events
    /// fold perfectly and who is filtered out of every board because the in-memory handle
    /// directory never learned their handle (§5.4).
    /// </remarks>
    private static void Visibility(RunReport report)
    {
        report.Checks.Add(new Check(
            Ok: report.PlayersChecked > 0 && report.PlayersVisible == report.PlayersChecked,
            Label: "player visibility",
            Expected: $"{report.PlayersChecked}/{report.PlayersChecked} on at least one board",
            Actual: $"{report.PlayersVisible}/{report.PlayersChecked}",
            Note: "a player whose handle never reached the read side folds perfectly and is invisible"));
    }

    /// <summary>No player stopped early.</summary>
    private static void PlayersFinished(RunReport report)
    {
        report.Checks.Add(new Check(
            Ok: report.PlayersWithErrors == 0,
            Label: "players completed",
            Expected: $"{report.Players.Count} of {report.Players.Count}",
            Actual: (report.Players.Count - report.PlayersWithErrors).ToString(CultureInfo.InvariantCulture)
                    + " of " + report.Players.Count.ToString(CultureInfo.InvariantCulture),
            Note: "a player that latched dead or could not drain leaves events unshipped"));
    }

    /// <summary>
    /// The run actually exercised the taxonomy rather than one corner of it.
    /// </summary>
    /// <remarks>
    /// A load harness that produced two million <c>telemetry.window</c> events and nothing else
    /// would pass every other check here and would be testing almost nothing. This is the check
    /// that keeps the generator honest, and it is why the RUD-cause draw has a coverage rotation
    /// in it.
    /// </remarks>
    private static void Taxonomy(RunReport report)
    {
        string[] required =
        [
            "session.started", "flight.started", "flight.ended", "telemetry.window",
            "vehicle.situation", "vehicle.atmosphere", "vehicle.orbit", "vehicle.impact",
            "vehicle.staging", "engine.ignition", "roster.snapshot",
        ];

        IReadOnlyDictionary<string, long> byType = report.EventsByType();
        var missing = new List<string>();
        foreach (string type in required)
        {
            if (!byType.ContainsKey(type))
                missing.Add(type);
        }

        report.Checks.Add(new Check(
            Ok: missing.Count == 0,
            Label: "taxonomy coverage",
            Expected: $"{required.Length} core event types present",
            Actual: missing.Count == 0 ? "all present" : "missing " + string.Join(", ", missing),
            Note: "a run that only produced telemetry windows would pass every other check here"));
    }

    /// <summary>The read API stayed healthy while ingest was happening.</summary>
    private static void ReadSide(RunReport report)
    {
        if (report.Read is not { Requests: > 0 })
            return;

        long failures = report.Read.Requests - report.Read.Successes;
        report.Checks.Add(new Check(
            Ok: failures == 0 && report.Read.TransportErrors == 0,
            Label: "read API under load",
            Expected: "every read on 2xx",
            Actual: failures == 0 && report.Read.TransportErrors == 0
                ? $"{report.Read.Requests} reads, all 2xx"
                : $"{failures} non-2xx, {report.Read.TransportErrors} transport failures ({report.Read.StatusLine()})",
            Note: "boards, profiles and the feed are served from projections.db while the writer is busy"));
    }

    /// <summary>A banned or deleted player is gone from the read side.</summary>
    private static void Moderated(ReadApiClient api, RunReport report, IReadOnlyList<PlayerAccount> accounts)
    {
        var handles = new Dictionary<int, string>();
        foreach (PlayerAccount account in accounts)
            handles[account.Index] = account.Handle;

        int expected = 0;
        var stillVisible = new List<string>();
        foreach (PlayerResult player in report.Players)
        {
            if (player.Role is not (ModerationRole.Ban or ModerationRole.Delete))
                continue;
            if (!handles.TryGetValue(player.Index, out string? handle))
                continue;

            expected++;
            if (api.Player(handle).Exists)
                stillVisible.Add(handle);
        }

        if (expected == 0)
            return;

        report.Checks.Add(new Check(
            Ok: stillVisible.Count == 0,
            Label: "banned and deleted",
            Expected: $"{expected} accounts answer 404",
            Actual: stillVisible.Count == 0 ? "all 404" : "still visible: " + string.Join(", ", stillVisible),
            Note: "a ban or a delete-my-data removes the player from every public surface (D9, §4.7)"));
    }
}
