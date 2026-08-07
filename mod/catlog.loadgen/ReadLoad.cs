using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// Hammers the public read API and the live feed while ingest is happening.
/// </summary>
/// <remarks>
/// <para>
/// A load harness that only measured ingest would be measuring half the system. Boards are read
/// far more often than they are written, the projector folds on the writer's notify while those
/// reads are being served, and the feed stream holds a connection open across the whole thing —
/// so all three run concurrently with the players here, and their latencies are reported
/// separately from ingest's rather than averaged into them.
/// </para>
/// <para>
/// Reads are paced (<c>--read-rps</c>) rather than run flat out by default, because a read loop
/// with no pacing on the same machine competes with the players for CPU and turns an ingest
/// measurement into a measurement of the harness.
/// </para>
/// </remarks>
internal sealed class ReadLoad
{
    /// <summary>Salt for the read loops' generators, so they do not shadow a player's stream.</summary>
    private const ulong ReaderSalt = 0x52454144_4C4F4F50UL;

    private readonly LoadOptions _options;
    private readonly HttpClient _http;
    private readonly IReadOnlyList<string> _handles;

    /// <summary>Creates the read-side load generator.</summary>
    /// <param name="options">The run's options.</param>
    /// <param name="transport">The shared transport.</param>
    /// <param name="handles">The handles to ask about.</param>
    internal ReadLoad(LoadOptions options, HttpMessageHandler transport, IReadOnlyList<string> handles)
    {
        _options = options;
        _handles = handles;
        _http = new HttpClient(transport, disposeHandler: false) { Timeout = TimeSpan.FromSeconds(30) };
    }

    /// <summary>Board keys the readers ask for; refreshed from the server before the run.</summary>
    internal List<string> Boards { get; } = [];

    /// <summary>Requests recorded by the read loops.</summary>
    internal HttpStats Stats { get; } = new("read");

    /// <summary>Frames received on the live feed.</summary>
    internal int FeedFrames { get; private set; }

    /// <summary>True when the feed subscription was established at all.</summary>
    internal bool FeedConnected { get; private set; }

    /// <summary>
    /// Learns the board list from the server, so the readers ask for boards that exist.
    /// </summary>
    /// <remarks>
    /// Safe to call more than once, and worth calling twice: the per-cause and per-body boards are
    /// data-driven and only published once enough distinct players are on them, so the list an
    /// empty database serves before a run is not the list it serves after one.
    /// </remarks>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A task that completes when the list is loaded.</returns>
    internal async Task DiscoverBoardsAsync(CancellationToken ct)
    {
        try
        {
            using HttpResponseMessage response =
                await _http.GetAsync(_options.Server + "/v1/leaderboards", ct).ConfigureAwait(false);
            if (!response.IsSuccessStatusCode)
                return;

            string body = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            using System.Text.Json.JsonDocument document = System.Text.Json.JsonDocument.Parse(body);
            var found = new List<string>();
            foreach (System.Text.Json.JsonElement board in document.RootElement.GetProperty("boards").EnumerateArray())
            {
                if (board.TryGetProperty("stat", out System.Text.Json.JsonElement stat)
                    && stat.GetString() is { Length: > 0 } key)
                {
                    found.Add(key);
                }
            }

            // Replaced, not appended: a second call must not double every board the readers pick
            // from, and must not leave a board behind that the server has stopped publishing.
            if (found.Count > 0)
            {
                Boards.Clear();
                Boards.AddRange(found);
            }
        }
        catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException or System.Text.Json.JsonException)
        {
            // The readers fall back to asking only for the endpoints that need no board name.
        }
    }

    /// <summary>Runs the read loops until cancelled.</summary>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A task that completes when every loop has stopped.</returns>
    internal async Task RunAsync(CancellationToken ct)
    {
        if (_options.Readers <= 0)
            return;

        var loops = new List<Task>(_options.Readers);
        for (int i = 0; i < _options.Readers; i++)
        {
            int index = i;
            loops.Add(Task.Run(() => LoopAsync(index, ct), CancellationToken.None));
        }

        await Task.WhenAll(loops).ConfigureAwait(false);
    }

    /// <summary>
    /// Holds a subscription to <c>GET /v1/feed/stream</c> open and counts what arrives.
    /// </summary>
    /// <remarks>
    /// The stream's frames are <c>event: feed</c>, not the default message event, and its
    /// heartbeat is a bare <c>: heartbeat</c> comment — so a subscriber that counted every line
    /// would be counting keep-alives. Counting the named event is the only honest measure of
    /// whether the projector's broadcaster kept up.
    /// </remarks>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A task that completes when the subscription ends.</returns>
    internal async Task SubscribeFeedAsync(CancellationToken ct)
    {
        if (!_options.Feed)
            return;

        try
        {
            using var request = new HttpRequestMessage(HttpMethod.Get, _options.Server + "/v1/feed/stream");
            request.Headers.TryAddWithoutValidation("Accept", "text/event-stream");
            using HttpResponseMessage response = await _http
                .SendAsync(request, HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false);
            if (!response.IsSuccessStatusCode)
                return;

            FeedConnected = true;
            await using Stream stream = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
            using var reader = new StreamReader(stream);
            while (!ct.IsCancellationRequested)
            {
                string? line = await reader.ReadLineAsync(ct).ConfigureAwait(false);
                if (line is null)
                    return;
                if (line.StartsWith("event: feed", StringComparison.Ordinal))
                    FeedFrames++;
            }
        }
        catch (Exception ex) when (ex is OperationCanceledException or HttpRequestException or IOException)
        {
            // Cancelled with the run, or the server closed the stream. Neither is a failure.
        }
    }

    private async Task LoopAsync(int index, CancellationToken ct)
    {
        var rng = new Prng((ulong)_options.Seed + ReaderSalt + (ulong)index);
        TimeSpan pace = _options.ReadRps > 0
            ? TimeSpan.FromSeconds(1.0 / _options.ReadRps)
            : TimeSpan.Zero;

        while (!ct.IsCancellationRequested)
        {
            string url = Pick(rng);
            long started = Stopwatch.GetTimestamp();
            try
            {
                using HttpResponseMessage response = await _http.GetAsync(url, ct).ConfigureAwait(false);
                _ = await response.Content.ReadAsByteArrayAsync(ct).ConfigureAwait(false);
                Stats.Record((int)response.StatusCode, Stopwatch.GetElapsedTime(started).TotalMilliseconds, 0);
            }
            catch (OperationCanceledException)
            {
                // HttpClient raises this for its own 30 s timeout as well as for cancellation, and
                // the two mean opposite things: one is the run ending, the other is a read the
                // server never answered. Treating a timeout as the former would silently retire
                // the reader for the rest of the run and leave `read API under load` passing on
                // the handful of requests it managed before the server got busy.
                if (ct.IsCancellationRequested)
                    return;
                Stats.RecordTransportError();
            }
            catch (HttpRequestException)
            {
                Stats.RecordTransportError();
            }

            if (pace > TimeSpan.Zero)
            {
                try
                {
                    await Task.Delay(pace, ct).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }
            }
        }
    }

    private string Pick(Prng rng)
    {
        int roll = rng.Int(0, 100);
        if (roll < 20 || Boards.Count == 0)
        {
            if (roll < 10 || _handles.Count == 0)
                return _options.Server + "/v1/leaderboards";
            return $"{_options.Server}/v1/players/{Uri.EscapeDataString(_handles[rng.Int(0, _handles.Count)])}";
        }

        if (roll < 35)
        {
            return string.Create(CultureInfo.InvariantCulture,
                $"{_options.Server}/v1/feed?limit={rng.Int(10, 200)}");
        }

        if (roll < 55 && _handles.Count > 0)
            return $"{_options.Server}/v1/players/{Uri.EscapeDataString(_handles[rng.Int(0, _handles.Count)])}";

        string board = Boards[rng.Int(0, Boards.Count)];
        int limit = rng.Int(10, 201);
        int offset = rng.Chance(0.25) ? rng.Int(0, 200) : 0;
        return string.Create(CultureInfo.InvariantCulture,
            $"{_options.Server}/v1/leaderboards/{board}?limit={limit}&offset={offset}");
    }
}
