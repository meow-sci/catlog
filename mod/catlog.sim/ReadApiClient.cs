using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;

namespace MeowSci.Catlog.Sim;

/// <summary>The outcome of one scenario assertion.</summary>
/// <param name="Ok">True when the observed value matched.</param>
/// <param name="Label">What was checked, e.g. a board stat key.</param>
/// <param name="Expected">The expected value, rendered.</param>
/// <param name="Actual">The observed value, rendered.</param>
/// <param name="Note">Why this check exists, or how it was derived.</param>
public sealed record CheckResult(bool Ok, string Label, string Expected, string Actual, string Note);

/// <summary>A player's board placements at one instant, as <c>GET /v1/players/{handle}</c> reports them.</summary>
/// <param name="Handle">The handle asked about.</param>
/// <param name="Exists">False when the server answered 404 — an unknown, retired or banned handle.</param>
/// <param name="Stats">stat key → value, for every board the player is on.</param>
public sealed record PlayerSnapshot(string Handle, bool Exists, IReadOnlyDictionary<string, double> Stats)
{
    /// <summary>The player's value on a board, or null when they are not on it.</summary>
    /// <param name="stat">The board's stat key.</param>
    /// <returns>The value, or null.</returns>
    public double? Get(string stat) => Stats.TryGetValue(stat, out double value) ? value : null;
}

/// <summary>What the read and admin APIs looked like before a scenario ran.</summary>
/// <param name="Player">The player's board placements.</param>
/// <param name="TotalEvents"><c>/admin/stats</c> <c>events.total</c>.</param>
/// <param name="Vars">The numeric <c>/debug/vars</c> counters.</param>
public sealed record Baseline(
    PlayerSnapshot Player,
    long TotalEvents,
    IReadOnlyDictionary<string, double> Vars)
{
    /// <summary>A counter's value before the run; 0 when it was not published.</summary>
    /// <param name="name">The expvar name.</param>
    /// <returns>The value.</returns>
    public double Var(string name) => Vars.TryGetValue(name, out double value) ? value : 0;
}

/// <summary>
/// The scenario-facing client for the server's public read API and its loopback admin API
/// (§7.3, §4.8, §5.9).
/// </summary>
/// <remarks>
/// <para>
/// <b>Board reads are made deterministic, not merely likely.</b> Boards are eventually consistent:
/// the ingest writer commits and returns 200, then the projector wakes on the writer's notify (or
/// its ≤1 s ticker) and folds. Sleeping and hoping would make every assertion a race. Instead
/// <see cref="WaitForProjector"/> polls <c>GET /admin/stats</c> until
/// <c>projector.lag_seq == 0</c> <b>and</b> <c>projector.checkpoint_seq == events.max_seq</c>,
/// which together mean "every event that has been stored has also been folded". Both halves are
/// required: on an empty log the lag is 0 because there is nothing to do, and after a restart the
/// checkpoint alone cannot distinguish "caught up" from "not running".
/// </para>
/// <para>
/// <b>Assertions are relative to a baseline</b> captured before the scenario runs. A record board
/// must end at <c>max(baseline, expected)</c> and a counter board at <c>baseline + delta</c>, so a
/// scenario is re-runnable against a server that already has data — including a second run of the
/// same scenario with the same credential — instead of only ever passing once against a virgin
/// database.
/// </para>
/// <para>
/// The methods are synchronous because §7.3 fixes <see cref="IScenario.Assert"/>'s signature and a
/// console harness has nothing useful to do while it waits.
/// </para>
/// </remarks>
public sealed class ReadApiClient : IDisposable
{
    private const int BoardPageSize = 200;
    private const int MaxBoardPages = 5;

    private readonly HttpClient _http = new() { Timeout = TimeSpan.FromSeconds(30) };
    private readonly List<CheckResult> _checks = [];
    private readonly string _serverUrl;
    private readonly string _adminUrl;

    /// <summary>Creates a client.</summary>
    /// <param name="serverUrl">Public base URL, e.g. <c>http://127.0.0.1:8080</c>.</param>
    /// <param name="adminUrl">Loopback admin base URL, e.g. <c>http://127.0.0.1:6060</c>.</param>
    public ReadApiClient(string serverUrl, string adminUrl)
    {
        _serverUrl = serverUrl.TrimEnd('/');
        _adminUrl = adminUrl.TrimEnd('/');
    }

    /// <summary>Every check run so far, in order.</summary>
    public IReadOnlyList<CheckResult> Checks => _checks;

    /// <summary>True when every check so far passed.</summary>
    public bool AllOk
    {
        get
        {
            foreach (CheckResult check in _checks)
            {
                if (!check.Ok)
                    return false;
            }

            return true;
        }
    }

    /// <summary>
    /// What the run that is being asserted produced. Installed by <see cref="ScenarioRunner"/>
    /// before <see cref="IScenario.Assert"/> is called, so a scenario can assert on its own event
    /// count — which is the only way to state "every event we produced was stored, exactly once"
    /// without hard-coding a number that every edit to the scenario would invalidate.
    /// </summary>
    public RunSummary? Run { get; set; }

    /// <summary>The pre-run baseline. Set by <see cref="CaptureBaseline"/>.</summary>
    public Baseline Baseline { get; private set; } =
        new(new PlayerSnapshot(string.Empty, false, new Dictionary<string, double>()), 0, new Dictionary<string, double>());

    /// <summary>Records the state the scenario starts from, so every assertion can be a delta.</summary>
    /// <param name="handle">The credential's handle.</param>
    /// <returns>The captured baseline.</returns>
    public Baseline CaptureBaseline(string handle)
    {
        EnsureHandleVisible(handle);
        WaitForProjector(TimeSpan.FromSeconds(30));
        using JsonDocument stats = GetJson(_adminUrl + "/admin/stats");
        Baseline = new Baseline(
            Player(handle),
            Int64(stats.RootElement.GetProperty("events"), "total"),
            Vars());
        return Baseline;
    }

    /// <summary>
    /// Makes sure the running server can resolve <paramref name="handle"/> to a player, forcing a
    /// handle-directory reload when it cannot.
    /// </summary>
    /// <param name="handle">The credential's handle.</param>
    /// <exception cref="SimException">The handle is still unknown after a reload.</exception>
    /// <remarks>
    /// <para>
    /// The read side resolves <c>player_id → handle</c> through an in-memory directory (§5.4),
    /// because the two Turso files cannot be joined. A handle claimed against a *running* catlogd
    /// that never reached that directory ships fine and folds fine, and is then filtered out of
    /// every board as "holding no handle yet" — a failure that is completely silent from the
    /// client's side.
    /// </para>
    /// <para>
    /// That gap is fixed: <c>POST /admin/issue</c> and <c>POST /api/handles</c> both reload the
    /// directory now, so this method should never find an unresolvable handle. It is kept as a
    /// backstop precisely because the failure is silent — <c>POST /admin/seed</c> forces a reload
    /// (idempotent, and it drains the projector before returning), and the note it prints is the
    /// only thing that would tell you the reload had regressed rather than the scenario being
    /// wrong.
    /// </para>
    /// </remarks>
    public void EnsureHandleVisible(string handle)
    {
        if (Player(handle).Exists)
            return;

        // The bug this worked around is fixed: POST /admin/issue and POST /api/handles both call
        // reloadDirectory now, so this branch should be unreachable. It is kept as a cheap backstop
        // because the failure it catches is silent — the events fold perfectly and the player is
        // simply absent from every board — and the note below is the only thing that would tell you
        // the reload regressed rather than the scenario being wrong.
        Console.WriteLine(
            $"note: the server cannot resolve '{handle}' yet, which should no longer happen — the "
            + "handle-directory reload may have regressed. Forcing a reload via POST /admin/seed "
            + "(idempotent).");

        using var request = new HttpRequestMessage(HttpMethod.Post, _adminUrl + "/admin/seed");
        request.Content = new StringContent("{}", Encoding.UTF8, "application/json");
        using HttpResponseMessage response = _http.Send(request);
        string body = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();
        if (!response.IsSuccessStatusCode)
            throw new SimException($"POST /admin/seed returned {(int)response.StatusCode}: {Truncate(body)}");

        if (!Player(handle).Exists)
        {
            throw new SimException(
                $"the server still does not know the handle '{handle}'. Was the credential issued "
                + $"against {_adminUrl}? Restarting catlogd also reloads the directory.");
        }
    }

    /// <summary>
    /// Blocks until the projector has folded every stored event, so the next board read is a
    /// statement about the data rather than about timing.
    /// </summary>
    /// <param name="timeout">How long to wait before giving up.</param>
    /// <exception cref="SimException">The projector did not catch up in time.</exception>
    public void WaitForProjector(TimeSpan timeout)
    {
        var elapsed = Stopwatch.StartNew();
        long lastLag = -1;
        long lastCheckpoint = -1;
        long lastMaxSeq = -1;

        while (elapsed.Elapsed < timeout)
        {
            using (JsonDocument stats = GetJson(_adminUrl + "/admin/stats"))
            {
                JsonElement projector = stats.RootElement.GetProperty("projector");
                lastLag = Int64(projector, "lag_seq");
                lastCheckpoint = Int64(projector, "checkpoint_seq");
                lastMaxSeq = Int64(stats.RootElement.GetProperty("events"), "max_seq");
                if (lastLag == 0 && lastCheckpoint == lastMaxSeq)
                    return;
            }

            Thread.Sleep(25);
        }

        throw new SimException(
            $"the projector did not catch up within {timeout.TotalSeconds:0.#} s "
            + $"(lag_seq={lastLag}, checkpoint_seq={lastCheckpoint}, events.max_seq={lastMaxSeq})");
    }

    /// <summary>
    /// Runs <c>POST /admin/projections/rebuild</c> — the D22 correctness backstop — and waits for
    /// the projector to settle afterwards.
    /// </summary>
    /// <returns>A one-line summary of what the rebuild did.</returns>
    public string Rebuild()
    {
        using var request = new HttpRequestMessage(HttpMethod.Post, _adminUrl + "/admin/projections/rebuild");
        // The endpoint answers 202 and runs the rebuild in the background, because at
        // production size it is minutes long and no HTTP request should be holding a
        // connection for that. A scenario needs the finished result in one call, so it
        // asks to wait — which is exactly what `wait` exists for.
        request.Content = new StringContent(
            "{\"wait\":true,\"reason\":\"catlog.sim scenario\"}", Encoding.UTF8, "application/json");
        using HttpResponseMessage response = _http.Send(request);
        string body = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();
        if (!response.IsSuccessStatusCode)
            throw new SimException($"POST /admin/projections/rebuild returned {(int)response.StatusCode}: {body}");

        using JsonDocument document = JsonDocument.Parse(body);
        JsonElement root = document.RootElement;
        if (!root.TryGetProperty("result", out JsonElement result) || result.ValueKind != JsonValueKind.Object)
        {
            string phase = root.TryGetProperty("phase", out JsonElement p) ? p.GetString() ?? "?" : "?";
            throw new SimException($"the rebuild finished as '{phase}' but reported no result: {body}");
        }
        WaitForProjector(TimeSpan.FromSeconds(60));
        return $"events={Int64(result, "events")} last_seq={Int64(result, "last_seq")} "
               + $"flights={Int64(result, "flights")} stats={Int64(result, "stats")} "
               + $"duration_ms={Int64(result, "duration_ms")}";
    }

    /// <summary>Reads a player's board placements (<c>GET /v1/players/{handle}</c>).</summary>
    /// <param name="handle">The handle.</param>
    /// <returns>The snapshot; <see cref="PlayerSnapshot.Exists"/> is false on 404.</returns>
    public PlayerSnapshot Player(string handle)
    {
        using HttpResponseMessage response = _http.Send(
            new HttpRequestMessage(HttpMethod.Get, $"{_serverUrl}/v1/players/{Uri.EscapeDataString(handle)}"));
        string body = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();

        if (response.StatusCode == HttpStatusCode.NotFound)
            return new PlayerSnapshot(handle, false, new Dictionary<string, double>());
        if (!response.IsSuccessStatusCode)
            throw new SimException($"GET /v1/players/{handle} returned {(int)response.StatusCode}: {body}");

        var stats = new Dictionary<string, double>(StringComparer.Ordinal);
        using JsonDocument document = JsonDocument.Parse(body);
        foreach (JsonElement row in document.RootElement.GetProperty("stats").EnumerateArray())
            stats[row.GetProperty("stat").GetString() ?? string.Empty] = row.GetProperty("value").GetDouble();
        return new PlayerSnapshot(handle, true, stats);
    }

    /// <summary>
    /// Finds a handle's row on a leaderboard page (<c>GET /v1/leaderboards/{stat}</c>).
    /// </summary>
    /// <param name="stat">The board's stat key.</param>
    /// <param name="handle">The handle to look for.</param>
    /// <returns>The rank and value, or null when the handle is not on the board.</returns>
    public (int Rank, double Value)? BoardRow(string stat, string handle)
    {
        for (int page = 0; page < MaxBoardPages; page++)
        {
            int offset = page * BoardPageSize;
            using JsonDocument document = GetJson(
                $"{_serverUrl}/v1/leaderboards/{stat}?limit={BoardPageSize}&offset={offset}");
            JsonElement rows = document.RootElement.GetProperty("rows");
            int count = 0;
            foreach (JsonElement row in rows.EnumerateArray())
            {
                count++;
                if (string.Equals(row.GetProperty("handle").GetString(), handle, StringComparison.Ordinal))
                    return (row.GetProperty("rank").GetInt32(), row.GetProperty("value").GetDouble());
            }

            if (count < BoardPageSize)
                return null;
        }

        return null;
    }

    /// <summary>Reads the numeric <c>GET /debug/vars</c> counters.</summary>
    /// <returns>Counter name → value, for the numeric entries only.</returns>
    public IReadOnlyDictionary<string, double> Vars()
    {
        var vars = new Dictionary<string, double>(StringComparer.Ordinal);
        using JsonDocument document = GetJson(_adminUrl + "/debug/vars");
        foreach (JsonProperty property in document.RootElement.EnumerateObject())
        {
            if (property.Value.ValueKind == JsonValueKind.Number && property.Value.TryGetDouble(out double value))
                vars[property.Name] = value;
        }

        return vars;
    }

    /// <summary><c>/admin/stats</c> <c>events.total</c>.</summary>
    /// <returns>How many events the server has stored.</returns>
    public long TotalEvents()
    {
        using JsonDocument stats = GetJson(_adminUrl + "/admin/stats");
        return Int64(stats.RootElement.GetProperty("events"), "total");
    }

    // --- assertions -----------------------------------------------------------------

    /// <summary>
    /// Asserts a <b>record</b> board: the player's value must end at the larger of the baseline
    /// and <paramref name="value"/>, and the board page must agree with the profile page.
    /// </summary>
    /// <param name="handle">The player.</param>
    /// <param name="stat">The board's stat key.</param>
    /// <param name="value">The record the scenario should have set.</param>
    public void ExpectRecord(string handle, string stat, double value)
    {
        double? baseline = Baseline.Player.Get(stat);
        double expected = baseline is { } b ? Math.Max(b, value) : value;
        double? actual = Player(handle).Get(stat);

        Record(
            ok: actual is { } a && Math.Abs(a - expected) < 1e-6,
            label: stat,
            expected: Num(expected),
            actual: actual is null ? "(not on the board)" : Num(actual.Value),
            note: baseline is null
                ? $"record board; scenario set {Num(value)}"
                : $"record board; max(baseline {Num(baseline.Value)}, {Num(value)})");

        (int Rank, double Value)? row = BoardRow(stat, handle);
        Record(
            ok: row is { } r && Math.Abs(r.Value - expected) < 1e-6,
            label: stat + " (board page)",
            expected: Num(expected),
            actual: row is null ? "(handle absent from the board)" : $"{Num(row.Value.Value)} at rank {row.Value.Rank}",
            note: "GET /v1/leaderboards/" + stat);
    }

    /// <summary>
    /// Asserts a <b>career-time</b> board: the player's value must end at the <i>smaller</i> of the
    /// baseline and <paramref name="value"/>, because the value is seconds since the career began
    /// and the fastest run wins (§4.1). The board page must agree with the profile page.
    /// </summary>
    /// <param name="handle">The player.</param>
    /// <param name="stat">The board's stat key.</param>
    /// <param name="value">The career time the scenario should have set.</param>
    public void ExpectBest(string handle, string stat, double value)
    {
        double? baseline = Baseline.Player.Get(stat);
        double expected = baseline is { } b ? Math.Min(b, value) : value;
        double? actual = Player(handle).Get(stat);

        Record(
            ok: actual is { } a && Math.Abs(a - expected) < 1e-6,
            label: stat,
            expected: Num(expected),
            actual: actual is null ? "(not on the board)" : Num(actual.Value),
            note: baseline is null
                ? $"career-time board; scenario set {Num(value)} s"
                : $"career-time board; min(baseline {Num(baseline.Value)}, {Num(value)}) s");

        (int Rank, double Value)? row = BoardRow(stat, handle);
        Record(
            ok: row is { } r && Math.Abs(r.Value - expected) < 1e-6,
            label: stat + " (board page)",
            expected: Num(expected),
            actual: row is null ? "(handle absent from the board)" : $"{Num(row.Value.Value)} at rank {row.Value.Rank}",
            note: "GET /v1/leaderboards/" + stat);
    }

    /// <summary>Asserts a <b>counter</b> board advanced by exactly <paramref name="delta"/>.</summary>
    /// <param name="handle">The player.</param>
    /// <param name="stat">The board's stat key.</param>
    /// <param name="delta">How much the scenario should have added.</param>
    public void ExpectCounter(string handle, string stat, double delta)
    {
        double baseline = Baseline.Player.Get(stat) ?? 0;
        double expected = baseline + delta;
        double actual = Player(handle).Get(stat) ?? 0;

        Record(
            ok: Math.Abs(actual - expected) < 1e-6,
            label: stat,
            expected: Num(expected),
            actual: Num(actual),
            note: $"counter board; baseline {Num(baseline)} + {Num(delta)}");
    }

    /// <summary>
    /// Asserts a board is exactly where it started — nothing the scenario did may have scored on it.
    /// </summary>
    /// <param name="handle">The player.</param>
    /// <param name="stat">The board's stat key.</param>
    /// <param name="why">Why nothing should have scored, shown in the report.</param>
    public void ExpectUnchanged(string handle, string stat, string why)
    {
        double? baseline = Baseline.Player.Get(stat);
        double? actual = Player(handle).Get(stat);
        bool ok = baseline is null
            ? actual is null || Math.Abs(actual.Value) < 1e-6
            : actual is { } a && Math.Abs(a - baseline.Value) < 1e-6;

        Record(
            ok: ok,
            label: stat,
            expected: baseline is null ? "(unchanged: not on the board)" : $"(unchanged: {Num(baseline.Value)})",
            actual: actual is null ? "(not on the board)" : Num(actual.Value),
            note: why);
    }

    /// <summary>Asserts a <c>/debug/vars</c> counter moved by exactly <paramref name="delta"/>.</summary>
    /// <param name="name">The expvar name.</param>
    /// <param name="delta">The expected change.</param>
    /// <param name="note">Why this matters, shown in the report.</param>
    public void ExpectVarDelta(string name, double delta, string note)
    {
        double baseline = Baseline.Var(name);
        double actual = Vars().TryGetValue(name, out double now) ? now : 0;
        Record(
            ok: Math.Abs(actual - baseline - delta) < 1e-6,
            label: name,
            expected: Num(baseline + delta),
            actual: Num(actual),
            note: note);
    }

    /// <summary>Asserts the server stored exactly <paramref name="delta"/> more events.</summary>
    /// <param name="delta">How many events the scenario appended to the outbox.</param>
    public void ExpectEventsStored(long delta)
    {
        long actual = TotalEvents();
        Record(
            ok: actual == Baseline.TotalEvents + delta,
            label: "events.total",
            expected: (Baseline.TotalEvents + delta).ToString(CultureInfo.InvariantCulture),
            actual: actual.ToString(CultureInfo.InvariantCulture),
            note: $"every one of the {delta} events the pipeline produced reached events.db, exactly once");
    }

    /// <summary>Records a hand-rolled check.</summary>
    /// <param name="ok">Whether it passed.</param>
    /// <param name="label">What was checked.</param>
    /// <param name="expected">The expected value.</param>
    /// <param name="actual">The observed value.</param>
    /// <param name="note">Why this check exists.</param>
    public void Record(bool ok, string label, string expected, string actual, string note)
        => _checks.Add(new CheckResult(ok, label, expected, actual, note));

    /// <summary>Releases the HTTP client.</summary>
    public void Dispose() => _http.Dispose();

    private JsonDocument GetJson(string url)
    {
        using HttpResponseMessage response = _http.Send(new HttpRequestMessage(HttpMethod.Get, url));
        string body = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();
        if (!response.IsSuccessStatusCode)
            throw new SimException($"GET {url} returned {(int)response.StatusCode}: {Truncate(body)}");

        try
        {
            return JsonDocument.Parse(body);
        }
        catch (JsonException ex)
        {
            throw new SimException($"GET {url} did not return JSON: {ex.Message}");
        }
    }

    private static long Int64(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value) && value.TryGetInt64(out long parsed) ? parsed : 0;

    private static string Num(double value)
        => value == Math.Floor(value) && Math.Abs(value) < 1e15
            ? ((long)value).ToString(CultureInfo.InvariantCulture)
            : value.ToString("0.######", CultureInfo.InvariantCulture);

    private static string Truncate(string value) => value.Length <= 300 ? value : value[..300];
}

/// <summary>A simulator-level failure: bad arguments, an unreachable server, a dead shipper.</summary>
public sealed class SimException : Exception
{
    /// <summary>Creates the exception.</summary>
    /// <param name="message">What went wrong, phrased for the operator.</param>
    public SimException(string message)
        : base(message)
    {
    }
}
