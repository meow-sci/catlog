using System;
using System.Diagnostics;
using System.Globalization;
using System.Net.Http;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// Waits for the projector to reach the head of the event log, on a deadline that is a function of
/// progress rather than of a constant.
/// </summary>
/// <remarks>
/// <para>
/// <b>Why this is not <c>ReadApiClient.WaitForProjector</c>.</b> That one takes a fixed timeout,
/// which is right for <c>catlog.sim</c>: a scenario writes a few hundred events and either folds
/// them promptly or is broken. It is wrong here. The projector folds one event at a time on one
/// goroutine, comfortably an order of magnitude slower than ingest stores, so the time it needs is
/// proportional to the size of the run — and a load harness exists to make runs whose size is not
/// known in advance. A constant large enough for a million events is a constant that lets a
/// genuinely wedged projector hang a small run for ten minutes, and a constant small enough for a
/// small run makes a million-event run fail at 94% folded with everything working correctly. That
/// is not a hypothetical: 300 s was the constant, and it is precisely where a 1,058,811-event run
/// stopped.
/// </para>
/// <para>
/// <b>What replaces it.</b> Progress. A projector that is moving is given as long as it needs; one
/// that has not advanced its checkpoint for <see cref="DefaultStallGrace"/> has stalled, and that
/// is a real failure whatever the size of the run. The run's own <c>--timeout</c> remains the
/// absolute ceiling, through the cancellation token, so nothing here can wait forever.
/// </para>
/// <para>
/// <b>The poll interval backs off, and that is load-bearing.</b> <c>GET /admin/stats</c> runs
/// <c>SELECT COUNT(*)</c> over the whole event table; at a million rows that is ~300 ms of server
/// work per call, against the same database the projector is reading. Polling it 40 times a second
/// while waiting an hour for a big fold would spend more server time answering the question than
/// doing the work. Small backlogs are still polled tightly, because there the latency of noticing
/// is what matters.
/// </para>
/// </remarks>
internal static class ProjectorWait
{
    /// <summary>How long the checkpoint may sit still before the projector is called stalled.</summary>
    internal static readonly TimeSpan DefaultStallGrace = TimeSpan.FromSeconds(60);

    private const int MinPollMs = 25;
    private const int MaxPollMs = 1000;

    /// <summary>
    /// Polls <c>GET /admin/stats</c> until <c>projector.lag_seq == 0</c> and
    /// <c>projector.checkpoint_seq == events.max_seq</c>.
    /// </summary>
    /// <param name="adminUrl">The loopback admin base URL.</param>
    /// <param name="transport">The shared HTTP transport.</param>
    /// <param name="stallGrace">
    /// How long the checkpoint may fail to advance before this gives up. <see cref="TimeSpan.Zero"/>
    /// means <see cref="DefaultStallGrace"/>.
    /// </param>
    /// <param name="ct">Cancellation — in practice the run's <c>--timeout</c>.</param>
    /// <returns>How long the wait took and how fast the projector folded.</returns>
    /// <exception cref="SimException">The projector stopped making progress.</exception>
    internal static async Task<ProjectorProgress> ForHeadAsync(
        string adminUrl, HttpMessageHandler transport, TimeSpan stallGrace, CancellationToken ct)
    {
        if (stallGrace <= TimeSpan.Zero)
            stallGrace = DefaultStallGrace;

        using var http = new HttpClient(transport, disposeHandler: false) { Timeout = TimeSpan.FromSeconds(60) };
        long started = Stopwatch.GetTimestamp();
        long lastMoved = started;
        long firstCheckpoint = -1;
        long checkpoint = -1;
        long lag = -1;
        long maxSeq = -1;

        while (true)
        {
            ct.ThrowIfCancellationRequested();

            string body = await http.GetStringAsync(adminUrl + "/admin/stats", ct).ConfigureAwait(false);
            using (JsonDocument stats = JsonDocument.Parse(body))
            {
                JsonElement projector = stats.RootElement.GetProperty("projector");
                long seen = projector.GetProperty("checkpoint_seq").GetInt64();
                lag = projector.GetProperty("lag_seq").GetInt64();
                maxSeq = stats.RootElement.GetProperty("events").GetProperty("max_seq").GetInt64();

                if (firstCheckpoint < 0)
                    firstCheckpoint = seen;
                if (seen != checkpoint)
                {
                    checkpoint = seen;
                    lastMoved = Stopwatch.GetTimestamp();
                }

                if (lag == 0 && checkpoint == maxSeq)
                {
                    TimeSpan took = Stopwatch.GetElapsedTime(started);
                    return new ProjectorProgress(took, checkpoint - firstCheckpoint);
                }
            }

            TimeSpan sinceMoved = Stopwatch.GetElapsedTime(lastMoved);
            if (sinceMoved >= stallGrace)
            {
                throw new SimException(
                    $"the projector stopped advancing for {sinceMoved.TotalSeconds:0.#} s "
                    + $"(lag_seq={lag}, checkpoint_seq={checkpoint}, events.max_seq={maxSeq}). "
                    + "A projector that is merely slow is waited out; this one is stuck.");
            }

            // Tight while there is little left to fold, relaxed while there is a lot: the answer
            // cannot arrive sooner than the work, and each question costs a full COUNT(*).
            int pollMs = lag > 20_000 ? MaxPollMs : lag > 2_000 ? 250 : MinPollMs;
            await Task.Delay(pollMs, ct).ConfigureAwait(false);
        }
    }
}

/// <summary>What one projector wait cost and covered.</summary>
/// <param name="Elapsed">Wall-clock time spent waiting.</param>
/// <param name="Folded">Events the projector folded while this waited.</param>
internal readonly record struct ProjectorProgress(TimeSpan Elapsed, long Folded)
{
    /// <summary>Events folded per second, or 0 when nothing had to be folded.</summary>
    internal double EventsPerSecond
        => Elapsed.TotalSeconds > 0.001 && Folded > 0 ? Folded / Elapsed.TotalSeconds : 0;

    /// <summary>A one-line summary for the progress log.</summary>
    /// <returns>The summary.</returns>
    public override string ToString()
        => Folded <= 0
            ? "already at the head"
            : string.Create(
                CultureInfo.InvariantCulture,
                $"folded {Folded} events in {Elapsed.TotalSeconds:0.#} s ({EventsPerSecond:0} events/s)");
}
