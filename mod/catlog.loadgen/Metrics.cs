using System;
using System.Collections.Generic;
using System.Collections.Concurrent;
using System.Diagnostics;
using System.Globalization;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// One exact HTTP request, kept so it can be sent again byte for byte.
/// </summary>
/// <remarks>
/// This is how the harness proves the server's idempotency contract rather than assuming it. A
/// batch's identity on the wire is its proof <c>jti</c>, and the proof is signed over the body
/// hash — so the only way to exercise §4.5.3 step 11's whole-batch replay short-circuit is to
/// resend the same bytes with the same proof. Rebuilding a "similar" request would mint a new
/// batch id and test the stream check instead.
/// </remarks>
/// <param name="Url">The absolute ingest URL.</param>
/// <param name="License">The <c>X-Catlog-License</c> value.</param>
/// <param name="Proof">The <c>X-Catlog-Proof</c> value.</param>
/// <param name="Body">The brotli-compressed body, exactly as sent.</param>
/// <param name="AtTicks"><see cref="Environment.TickCount64"/> when it was sent.</param>
internal sealed record CapturedRequest(string Url, string License, string Proof, byte[] Body, long AtTicks)
{
    /// <summary>How long ago this request was sent.</summary>
    internal TimeSpan Age => TimeSpan.FromMilliseconds(Environment.TickCount64 - AtTicks);
}

/// <summary>
/// Holds the requests the harness may want to send a second time.
/// </summary>
/// <remarks>
/// <para>
/// Two different things are kept, for two different reasons. <see cref="Latest"/> is the most
/// recent accepted batch from <i>any</i> player, and it exists because a proof's <c>iat</c> has to
/// be inside §4.3's ±300 s window when it is replayed — verification reaches the skew check (step
/// 8) long before it reaches the replay short-circuit (step 11), so an old capture would come back
/// <c>401 clock_skew</c> and say nothing at all about idempotency. Keeping the newest means the
/// probe fires seconds after the batch it repeats, however long the run was.
/// </para>
/// <para>
/// <see cref="TryFor"/> keeps one request per named player, for the accounts whose credentials are
/// about to be revoked. Those are exempt from the staleness problem: revocation is checked at step
/// 5, before the clock, so a revoked credential answers <c>401 license_revoked</c> no matter how
/// old the proof is — which is precisely the thing worth demonstrating.
/// </para>
/// </remarks>
internal sealed class CaptureStore
{
    private readonly object _gate = new();
    private readonly ConcurrentDictionary<int, CapturedRequest> _byPlayer = new();
    private CapturedRequest? _latest;

    /// <summary>The most recent accepted batch from any player.</summary>
    internal CapturedRequest? Latest
    {
        get
        {
            lock (_gate)
                return _latest;
        }
    }

    /// <summary>Records an accepted batch.</summary>
    /// <param name="playerIndex">Who sent it.</param>
    /// <param name="request">The request.</param>
    /// <param name="keepForPlayer">True to also keep it under <paramref name="playerIndex"/>.</param>
    internal void Offer(int playerIndex, CapturedRequest request, bool keepForPlayer)
    {
        lock (_gate)
        {
            if (_latest is null || request.AtTicks >= _latest.AtTicks)
                _latest = request;
        }

        if (keepForPlayer)
            _byPlayer[playerIndex] = request;
    }

    /// <summary>The most recent accepted batch from one player.</summary>
    /// <param name="playerIndex">Who to look up.</param>
    /// <param name="request">The request, when there is one.</param>
    /// <returns>True when a request was kept for that player.</returns>
    internal bool TryFor(int playerIndex, out CapturedRequest? request)
        => _byPlayer.TryGetValue(playerIndex, out request);
}

/// <summary>Latency and status bookkeeping for one class of traffic.</summary>
/// <remarks>
/// Thread-safe by one lock rather than by lock-free cleverness: every call site has just spent a
/// network round trip, so the lock is never the interesting cost, and a plain lock is a thing a
/// reader can verify at a glance.
/// </remarks>
internal sealed class HttpStats
{
    private readonly object _gate = new();
    private readonly List<double> _latencies = [];
    private readonly SortedDictionary<int, long> _byStatus = [];

    /// <summary>What this set of counters is about, for the report.</summary>
    /// <param name="name">A short label, e.g. <c>ingest</c>.</param>
    internal HttpStats(string name) => Name = name;

    /// <summary>The label.</summary>
    internal string Name { get; }

    /// <summary>Total requests that produced a response.</summary>
    internal long Requests { get; private set; }

    /// <summary>Requests that never produced a response at all.</summary>
    internal long TransportErrors { get; private set; }

    /// <summary>Total request bytes put on the wire.</summary>
    internal long BytesSent { get; private set; }

    /// <summary>Status code → count.</summary>
    internal IReadOnlyDictionary<int, long> ByStatus
    {
        get
        {
            lock (_gate)
                return new SortedDictionary<int, long>(_byStatus);
        }
    }

    /// <summary>Records one completed request.</summary>
    /// <param name="status">The HTTP status.</param>
    /// <param name="elapsedMs">Round-trip time in milliseconds.</param>
    /// <param name="bytes">Request body size.</param>
    internal void Record(int status, double elapsedMs, long bytes)
    {
        lock (_gate)
        {
            Requests++;
            BytesSent += bytes;
            _latencies.Add(elapsedMs);
            _byStatus[status] = _byStatus.TryGetValue(status, out long n) ? n + 1 : 1;
        }
    }

    /// <summary>Records a request that never completed.</summary>
    internal void RecordTransportError()
    {
        lock (_gate)
            TransportErrors++;
    }

    /// <summary>How many requests landed on 2xx.</summary>
    internal long Successes
    {
        get
        {
            lock (_gate)
            {
                long n = 0;
                foreach ((int status, long count) in _byStatus)
                {
                    if (status is >= 200 and < 300)
                        n += count;
                }

                return n;
            }
        }
    }

    /// <summary>The latency at a percentile, in milliseconds; 0 when nothing was recorded.</summary>
    /// <param name="percentile">The percentile, 0–100.</param>
    /// <returns>The latency.</returns>
    internal double Percentile(double percentile)
    {
        lock (_gate)
        {
            if (_latencies.Count == 0)
                return 0;
            var sorted = new List<double>(_latencies);
            sorted.Sort();
            int index = (int)Math.Round((percentile / 100.0) * (sorted.Count - 1), MidpointRounding.AwayFromZero);
            return sorted[Math.Clamp(index, 0, sorted.Count - 1)];
        }
    }

    /// <summary>The mean latency in milliseconds.</summary>
    internal double MeanMs
    {
        get
        {
            lock (_gate)
            {
                if (_latencies.Count == 0)
                    return 0;
                double sum = 0;
                foreach (double value in _latencies)
                    sum += value;
                return sum / _latencies.Count;
            }
        }
    }

    /// <summary>Renders the status histogram as <c>200×1234 429×17</c>.</summary>
    /// <returns>The rendering; <c>(none)</c> when empty.</returns>
    internal string StatusLine()
    {
        lock (_gate)
        {
            if (_byStatus.Count == 0)
                return "(none)";
            var parts = new List<string>(_byStatus.Count);
            foreach ((int status, long count) in _byStatus)
                parts.Add(string.Create(CultureInfo.InvariantCulture, $"{status}×{count}"));
            return string.Join("  ", parts);
        }
    }
}

/// <summary>
/// Wraps the shipper's transport so every ingest request is timed and counted, and so one of them
/// can be captured for the replay probe.
/// </summary>
/// <remarks>
/// Deliberately a <see cref="DelegatingHandler"/> rather than instrumentation inside
/// <c>BatchShipper</c>: the shipper the harness drives has to be the one players run, unmodified,
/// and measuring it from outside is the only way to keep that true.
/// </remarks>
internal sealed class RecordingHandler : DelegatingHandler
{
    private readonly HttpStats _stats;
    private readonly Action<CapturedRequest>? _capture;

    /// <summary>Creates the handler.</summary>
    /// <param name="inner">The real transport. Shared; this handler never disposes it.</param>
    /// <param name="stats">Where to record.</param>
    /// <param name="capture">
    /// Called with every accepted request, when non-null. The body has already been materialised
    /// for the byte count, so this costs a reference and nothing else.
    /// </param>
    internal RecordingHandler(HttpMessageHandler inner, HttpStats stats, Action<CapturedRequest>? capture = null)
    {
        InnerHandler = inner;
        _stats = stats;
        _capture = capture;
    }

    /// <inheritdoc />
    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request, CancellationToken cancellationToken)
    {
        byte[] body = request.Content is null
            ? []
            : await request.Content.ReadAsByteArrayAsync(cancellationToken).ConfigureAwait(false);

        long started = Stopwatch.GetTimestamp();
        HttpResponseMessage response;
        try
        {
            response = await base.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception) when (!cancellationToken.IsCancellationRequested)
        {
            _stats.RecordTransportError();
            throw;
        }

        double elapsedMs = Stopwatch.GetElapsedTime(started).TotalMilliseconds;
        _stats.Record((int)response.StatusCode, elapsedMs, body.Length);

        if (_capture is not null && response.IsSuccessStatusCode)
        {
            _capture(new CapturedRequest(
                request.RequestUri?.ToString() ?? string.Empty,
                Header(request, "X-Catlog-License"),
                Header(request, "X-Catlog-Proof"),
                body,
                Environment.TickCount64));
        }

        return response;
    }

    /// <inheritdoc />
    /// <remarks>
    /// The inner handler is shared across every player in the run, so this deliberately does not
    /// pass disposal down: releasing the connection pool the moment one player finished would tear
    /// the run apart.
    /// </remarks>
    protected override void Dispose(bool disposing) => base.Dispose(disposing: false);

    private static string Header(HttpRequestMessage request, string name)
        => request.Headers.TryGetValues(name, out IEnumerable<string>? values)
            ? string.Join(string.Empty, values)
            : string.Empty;
}
