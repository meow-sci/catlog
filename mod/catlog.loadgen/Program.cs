using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Util;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// The <c>catlog.loadgen</c> entry point: provision many players for real, invent plausible play
/// for each of them, and push the whole lot through the real client pipeline at a live server.
/// </summary>
/// <remarks>
/// <para>
/// <b>What this is for.</b> <c>catlog.sim</c> answers "does the client still produce exactly the
/// right events for these six scripted situations". This answers a different question: "what
/// happens when two hundred people play at once". It is randomised on purpose, it is not asserted
/// to exact leaderboard values, and it must never be made to behave like the sim — a
/// non-deterministic acceptance test is worse than no acceptance test.
/// </para>
/// <para>
/// <b>What is real here.</b> All of it, except the game. The identities are minted at
/// <c>mockidp</c> and signed in through catlogd's own OAuth callback; the licenses are issued by
/// catlogd against keys generated in this process; the events come out of the real detector and
/// the real outbox and are signed by the real proof signer and shipped by the real batch shipper.
/// The only concession to running a day of play in a minute is the injected shipper clock, which
/// is the seam <c>catlog.sim</c> already uses and which the shipped mod cannot reach.
/// </para>
/// </remarks>
internal static class Program
{
    /// <summary>Exit code when the run completed and every requested invariant held.</summary>
    internal const int ExitOk = 0;

    /// <summary>Exit code when an invariant broke.</summary>
    internal const int ExitAssertionFailed = 1;

    /// <summary>Exit code when the arguments or the environment were wrong.</summary>
    internal const int ExitUsage = 2;

    private static async Task<int> Main(string[] args)
    {
        foreach (string arg in args)
        {
            if (arg is "--help" or "-h")
            {
                LoadOptions.Usage();
                return ExitOk;
            }
        }

        LoadOptions options;
        try
        {
            options = LoadOptions.Parse(args);
        }
        catch (SimException ex)
        {
            Console.Error.WriteLine("catlog.loadgen: " + ex.Message);
            return ExitUsage;
        }

        using var cancellation = new CancellationTokenSource();
        if (options.TimeoutSeconds > 0)
            cancellation.CancelAfter(TimeSpan.FromSeconds(options.TimeoutSeconds));
        Console.CancelKeyPress += (_, e) =>
        {
            e.Cancel = true;
            Console.Error.WriteLine("catlog.loadgen: interrupted; winding the run down…");
            cancellation.Cancel();
        };

        try
        {
            return await RunAsync(options, cancellation.Token).ConfigureAwait(false);
        }
        catch (SimException ex)
        {
            Console.Error.WriteLine("catlog.loadgen: " + ex.Message);
            return ExitUsage;
        }
        catch (OperationCanceledException)
        {
            Console.Error.WriteLine("catlog.loadgen: the run was cancelled before it finished.");
            return ExitUsage;
        }
    }

    private static async Task<int> RunAsync(LoadOptions options, CancellationToken ct)
    {
        long started = Stopwatch.GetTimestamp();
        var report = new RunReport(options);

        var logger = new LoadLogger();
        ModLog.SetLogger(logger);

        // The thread pool ramps its worker count slowly by design, and a run that starts two
        // hundred players at once would otherwise spend its first seconds waiting for threads
        // rather than for the server. Raising the floor removes that from the measurement.
        ThreadPool.GetMinThreads(out int workers, out int io);
        ThreadPool.SetMinThreads(Math.Max(workers, options.Concurrency + 8), Math.Max(io, options.Concurrency + 8));

        using var transport = new SocketsHttpHandler
        {
            // The Location headers are the OAuth dance; following them silently would hide the
            // step where the harness supplies the subject.
            AllowAutoRedirect = false,
            // Cookies are carried per player by CookieJar so one pool serves everyone.
            UseCookies = false,
            MaxConnectionsPerServer = Math.Max(64, options.Concurrency * 2),
            PooledConnectionLifetime = TimeSpan.FromMinutes(10),
        };

        var provisioner = new Provisioner(options, transport);
        IReadOnlyList<string> problems = await provisioner.PreflightAsync(ct).ConfigureAwait(false);
        if (problems.Count > 0)
        {
            foreach (string problem in problems)
                Console.Error.WriteLine("catlog.loadgen: " + problem);
            return ExitUsage;
        }

        // Progress goes to stderr and the report to stdout, so `--report json` pipes straight
        // into jq without a filter step.
        Console.Error.WriteLine($"catlog.loadgen — {options.Players} players × "
                                + $"{options.DurationSeconds / 60:0.#} simulated minutes, seed {options.Seed}");

        // --- provisioning -------------------------------------------------------------
        long provisionStarted = Stopwatch.GetTimestamp();
        List<PlayerAccount> accounts = await ProvisionAsync(options, provisioner, report, ct).ConfigureAwait(false);
        report.ProvisionElapsed = Stopwatch.GetElapsedTime(provisionStarted);

        if (accounts.Count == 0)
        {
            Console.Error.WriteLine("catlog.loadgen: no player could be provisioned; nothing to run.");
            report.Elapsed = Stopwatch.GetElapsedTime(started);
            Emit(report);
            return ExitUsage;
        }

        Console.Error.WriteLine($"provisioned {accounts.Count} players in {Text.Seconds(report.ProvisionElapsed)}"
                                + (report.TooNewRefused > 0
                                    ? $"; {report.TooNewRefused} logins correctly refused as account_too_new"
                                    : string.Empty));

        var api = new ReadApiClient(options.Server, options.Admin);
        try
        {
            // --- baseline -------------------------------------------------------------
            api.WaitForProjector(TimeSpan.FromSeconds(60));
            report.BaselineEvents = api.TotalEvents();
            report.BaselineVars = api.Vars();

            var handles = new List<string>(accounts.Count);
            foreach (PlayerAccount account in accounts)
                handles.Add(account.Handle);

            // --- read side, concurrent with ingest ------------------------------------
            var readLoad = new ReadLoad(options, transport, handles);
            await readLoad.DiscoverBoardsAsync(ct).ConfigureAwait(false);
            using var readCancel = CancellationTokenSource.CreateLinkedTokenSource(ct);
            Task readers = readLoad.RunAsync(readCancel.Token);
            Task feed = readLoad.SubscribeFeedAsync(readCancel.Token);

            // --- ingest ---------------------------------------------------------------
            var captures = new CaptureStore();
            long ingestStarted = Stopwatch.GetTimestamp();
            await IngestAsync(options, provisioner, accounts, report, transport, captures, ct).ConfigureAwait(false);
            report.IngestElapsed = Stopwatch.GetElapsedTime(ingestStarted);

            await readCancel.CancelAsync().ConfigureAwait(false);
            await Task.WhenAll(readers, feed).ConfigureAwait(false);
            report.Read = readLoad.Stats;
            report.FeedFrames = readLoad.FeedFrames;
            report.FeedConnected = readLoad.FeedConnected;

            // --- projector ------------------------------------------------------------
            long projectorStarted = Stopwatch.GetTimestamp();
            api.WaitForProjector(TimeSpan.FromSeconds(300));
            report.ProjectorCatchUp = Stopwatch.GetElapsedTime(projectorStarted);

            report.FinalEvents = api.TotalEvents();
            report.FinalVars = api.Vars();
            (report.CheckpointSeq, report.MaxSeq) = await SequencesAsync(options, transport, ct).ConfigureAwait(false);

            // --- idempotency ----------------------------------------------------------
            if (options.DedupProbe)
                await DedupProbeAsync(options, provisioner, api, report, captures, ct).ConfigureAwait(false);

            // --- boards and visibility, before anything is moderated away -------------
            //
            // Re-discovered rather than reused. `fastest_to_<body>` and `rud_<cause>` are
            // data-driven: a board is only published once enough distinct players are on it, so
            // the list learned before ingest is the list an *empty* database publishes. Asking
            // again is the difference between a report that says a run put players on ten other
            // worlds and a report that never mentions those boards exist.
            await readLoad.DiscoverBoardsAsync(ct).ConfigureAwait(false);
            await CollectBoardsAsync(options, transport, readLoad, report, ct).ConfigureAwait(false);
            CheckVisibility(api, accounts, report);

            // --- moderation -----------------------------------------------------------
            await ModerateAsync(provisioner, api, accounts, report, captures, ct).ConfigureAwait(false);

            if (options.Assert)
                Invariants.Check(options, report, api, accounts);
        }
        finally
        {
            api.Dispose();
            foreach (PlayerAccount account in accounts)
                account.Dispose();
        }

        report.Elapsed = Stopwatch.GetElapsedTime(started);
        report.LibWarnings = logger.Warnings;
        report.LibErrors = logger.Errors;
        Emit(report);
        return report.AllOk ? ExitOk : ExitAssertionFailed;
    }

    // --- phases ----------------------------------------------------------------------

    private static async Task<List<PlayerAccount>> ProvisionAsync(
        LoadOptions options, Provisioner provisioner, RunReport report, CancellationToken ct)
    {
        var accounts = new List<PlayerAccount>(options.Players);
        var gate = new SemaphoreSlim(Math.Min(options.Concurrency, 32));
        var results = new ConcurrentDictionary<int, ProvisionResult>();
        var subjects = new List<Subject>();

        if (options.Auth == AuthMode.OAuth)
        {
            // Mint spares for the identities that are supposed to be refused, so a run still ends
            // up with the number of players it was asked for.
            int spare = options.TooNewPercent <= 0
                ? 0
                : (int)Math.Ceiling(options.Players * options.TooNewPercent / 100.0) + 1;
            subjects = [.. await provisioner.MintSubjectsAsync(options.Players + spare, ct).ConfigureAwait(false)];
            report.SubjectsMinted = subjects.Count;
            foreach (Subject subject in subjects)
            {
                if (subject.TooNew)
                    report.TooNewMinted++;
            }
        }

        int attempts = options.Auth == AuthMode.OAuth ? subjects.Count : options.Players;
        var tasks = new List<Task>(attempts);
        for (int i = 0; i < attempts; i++)
        {
            int index = i;
            tasks.Add(Task.Run(async () =>
            {
                await gate.WaitAsync(ct).ConfigureAwait(false);
                try
                {
                    string handle = Text.Handle(options.Namespace, index);
                    results[index] = options.Auth == AuthMode.OAuth
                        ? await provisioner.ProvisionOAuthAsync(index, subjects[index], handle, ct).ConfigureAwait(false)
                        : await provisioner.ProvisionAdminAsync(index, handle, ct).ConfigureAwait(false);
                }
                catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException)
                {
                    results[index] = new ProvisionResult(false, null, "transport", ex.Message);
                }
                finally
                {
                    gate.Release();
                }
            }, ct));
        }

        await Task.WhenAll(tasks).ConfigureAwait(false);

        for (int i = 0; i < attempts; i++)
        {
            if (!results.TryGetValue(i, out ProvisionResult? result))
                continue;

            bool tooNew = options.Auth == AuthMode.OAuth && subjects[i].TooNew;
            if (result.Ok)
            {
                if (tooNew)
                {
                    // The gate let through an account it should have refused. Counted loudly and
                    // asserted on: this is a genuine bug if it ever happens.
                    report.TooNewAccepted++;
                }

                if (accounts.Count < options.Players)
                {
                    accounts.Add(result.Player!);
                    report.ProvisionOk++;
                    report.ProvisionByIdP.Bump(result.Player!.IdP);
                }
                else
                {
                    result.Player!.Dispose();
                }

                continue;
            }

            if (tooNew && result.ErrorCode == "account_too_new")
            {
                report.TooNewRefused++;
                continue;
            }

            report.ProvisionFailed++;
            report.ProvisionErrors.Bump(Text.Clip(result.ErrorCode + " — " + result.Detail, 140));
        }

        return accounts;
    }

    private static async Task IngestAsync(
        LoadOptions options,
        Provisioner provisioner,
        List<PlayerAccount> accounts,
        RunReport report,
        HttpMessageHandler transport,
        CaptureStore captures,
        CancellationToken ct)
    {
        var clock = new LoadClock(options.DurationSeconds);
        var gate = new SemaphoreSlim(options.Concurrency);
        var finished = new ConcurrentBag<PlayerResult>();
        int done = 0;
        int skippedRoles = 0;

        var tasks = new List<Task>(accounts.Count);
        for (int position = 0; position < accounts.Count; position++)
        {
            PlayerAccount player = accounts[position];
            // The random stream is keyed on the account index so a player's career is a function of
            // (seed, index) alone; the coverage rotations are keyed on this dense position, because
            // the age gate leaves holes in the indices and a rotation with holes drops rungs.
            int cohort = position;
            tasks.Add(Task.Run(async () =>
            {
                await gate.WaitAsync(ct).ConfigureAwait(false);
                var script = new PlayerScript(
                    player.Index, cohort, options.DurationSeconds, clock,
                    Prng.ForPlayer(options.Seed, player.Index), options.ModerationPercent,
                    dashboard: player.Session is not null);
                try
                {
                    if (script.RoleSkipped)
                        Interlocked.Increment(ref skippedRoles);

                    // Every accepted batch is offered to the capture store: the newest one in
                    // the whole run is what the replay probe repeats (its proof has to be inside
                    // the ±300 s skew window), and a revoking player's own last batch is kept so
                    // the revocation can be demonstrated rather than asserted.
                    bool keep = script.Role == ModerationRole.Revoke;
                    void Capture(CapturedRequest request) => captures.Offer(player.Index, request, keep);

                    using var runner = new PlayerRunner(options, player, transport, report.Ingest, Capture);
                    Func<CancellationToken, Task<Credential?>>? reissue = script.Role == ModerationRole.Reissue
                        ? async token =>
                        {
                            (Credential? credential, string error) =
                                await provisioner.ReissueAsync(player, token).ConfigureAwait(false);
                            if (credential is null)
                                lock (report.Moderation) report.Moderation.Add($"{player.Handle}: reissue FAILED — {error}");
                            return credential;
                        }
                        : null;

                    PlayerResult result = await runner.RunAsync(script, reissue, ct).ConfigureAwait(false);
                    finished.Add(result);

                    if (result.Reissued)
                    {
                        lock (report.Moderation)
                            report.Moderation.Add($"{player.Handle}: reissued mid-run; the replaced credential was revoked (D16)");
                    }

                    int n = Interlocked.Increment(ref done);
                    if (options.Verbose)
                    {
                        Console.Error.WriteLine(
                            $"  [{n}/{accounts.Count}] {result.Handle,-18} {script.Describe(),-52} "
                            + $"{result.Events,6} ev  {result.Batches,4} batches  {Text.Seconds(result.Elapsed)}"
                            + (result.Error.Length > 0 ? "  ERROR: " + result.Error : string.Empty));
                    }
                    else if (n % Math.Max(1, accounts.Count / 10) == 0)
                    {
                        Console.Error.WriteLine($"  {n}/{accounts.Count} players done");
                    }
                }
                catch (Exception ex) when (ex is HttpRequestException or IOException
                                               or TaskCanceledException or SimException)
                {
                    // One player must not take the run with it. PlayerRunner already converts a
                    // dead shipper into a PlayerResult with an Error, but a transport fault in the
                    // reissue round trip or a failure opening the outbox escapes it entirely — and
                    // an escaped exception means Task.WhenAll throws, report.Players is never
                    // populated, and the whole run exits with no report at all. Recorded as a
                    // failed player instead, which is what `players completed` is there to catch.
                    finished.Add(Failed(player, script, ex));
                    Interlocked.Increment(ref done);
                }
                finally
                {
                    gate.Release();
                }
            }, ct));
        }

        await Task.WhenAll(tasks).ConfigureAwait(false);

        var ordered = new List<PlayerResult>(finished);
        ordered.Sort(static (a, b) => a.Index.CompareTo(b.Index));
        report.Players.AddRange(ordered);

        if (skippedRoles > 0)
        {
            report.Moderation.Add(
                $"{skippedRoles} players drew a dashboard moderation role (reissue, revoke or "
                + "delete-my-data) and did not take it: --auth admin has no website session");
        }
    }

    /// <summary>A result for a player whose task threw before it could produce one of its own.</summary>
    private static PlayerResult Failed(PlayerAccount player, PlayerScript script, Exception ex) => new(
        player.Index, player.Handle, player.IdP, script.Summary, script.Role,
        Frames: 0, Events: 0, Batches: 0, ServerAccepted: 0, ServerDeduped: 0,
        ClockResyncs: 0, StreamForks: 0, Oversize: 0, RateLimited: 0, Busy: 0,
        EventsByType: new Dictionary<string, int>(StringComparer.Ordinal),
        Digest: string.Empty, Elapsed: TimeSpan.Zero, Reissued: false,
        Error: ex.GetType().Name + ": " + Text.Clip(ex.Message, 100));

    private static async Task DedupProbeAsync(
        LoadOptions options,
        Provisioner provisioner,
        ReadApiClient api,
        RunReport report,
        CaptureStore captures,
        CancellationToken ct)
    {
        CapturedRequest? captured = captures.Latest;
        if (captured is null)
        {
            report.DedupProbe = "skipped: no accepted batch was captured";
            return;
        }

        long before = api.TotalEvents();
        (int status, string body) = await provisioner.ReplayAsync(captured, ct).ConfigureAwait(false);
        long after = api.TotalEvents();

        bool replay = false;
        int deduped = 0;
        try
        {
            using JsonDocument document = JsonDocument.Parse(body);
            replay = document.RootElement.TryGetProperty("replay", out JsonElement flag) && flag.GetBoolean();
            deduped = document.RootElement.TryGetProperty("deduped", out JsonElement n) ? n.GetInt32() : 0;
        }
        catch (JsonException)
        {
            // Left false; the report says what actually came back.
        }

        report.DedupProbeOk = status == 200 && replay && deduped > 0 && after == before;
        report.DedupProbe =
            $"re-sent an accepted batch verbatim ({captured.Age.TotalSeconds:0.#} s old) → HTTP {status} "
            + $"replay={replay.ToString().ToLowerInvariant()} deduped={deduped}; events.total {before} → {after}";
    }

    /// <summary>
    /// Reads the head of every board, once, at the end of a run — the "did any of this actually
    /// land somewhere a player would see" check.
    /// </summary>
    private static async Task CollectBoardsAsync(
        LoadOptions options, HttpMessageHandler transport, ReadLoad readLoad, RunReport report, CancellationToken ct)
    {
        using var http = new HttpClient(transport, disposeHandler: false) { Timeout = TimeSpan.FromSeconds(30) };
        foreach (string stat in readLoad.Boards)
        {
            try
            {
                string body = await http
                    .GetStringAsync($"{options.Server}/v1/leaderboards/{stat}?limit=200&offset=0", ct)
                    .ConfigureAwait(false);
                using JsonDocument document = JsonDocument.Parse(body);
                JsonElement rows = document.RootElement.GetProperty("rows");
                int count = rows.GetArrayLength();
                report.Boards.Add(count == 0
                    ? new BoardSummary(stat, 0, string.Empty, 0)
                    : new BoardSummary(
                        stat, count,
                        rows[0].GetProperty("handle").GetString() ?? string.Empty,
                        rows[0].GetProperty("value").GetDouble()));
            }
            catch (Exception ex) when (ex is HttpRequestException or JsonException or KeyNotFoundException)
            {
                report.Boards.Add(new BoardSummary(stat, 0, string.Empty, 0));
            }
        }
    }

    private static void CheckVisibility(ReadApiClient api, List<PlayerAccount> accounts, RunReport report)
    {
        foreach (PlayerAccount account in accounts)
        {
            report.PlayersChecked++;
            PlayerSnapshot snapshot = api.Player(account.Handle);
            if (snapshot.Exists && snapshot.Stats.Count > 0)
                report.PlayersVisible++;
        }
    }

    private static async Task ModerateAsync(
        Provisioner provisioner,
        ReadApiClient api,
        List<PlayerAccount> accounts,
        RunReport report,
        CaptureStore captures,
        CancellationToken ct)
    {
        var byIndex = new Dictionary<int, PlayerAccount>();
        foreach (PlayerAccount account in accounts)
            byIndex[account.Index] = account;

        foreach (PlayerResult player in report.Players)
        {
            if (!byIndex.TryGetValue(player.Index, out PlayerAccount? account))
                continue;

            switch (player.Role)
            {
                case ModerationRole.Revoke:
                {
                    string error = await provisioner.RevokeAsync(account, ct).ConfigureAwait(false);
                    if (error.Length > 0)
                    {
                        report.Moderation.Add($"{account.Handle}: revoke FAILED — {error}");
                        break;
                    }

                    string proof = "revoked";
                    if (captures.TryFor(player.Index, out CapturedRequest? captured) && captured is not null)
                    {
                        (int status, string body) = await provisioner.ReplayAsync(captured, ct).ConfigureAwait(false);
                        proof = status == 401 && body.Contains("license_revoked", StringComparison.Ordinal)
                            ? "revoked; a replay of its last batch is now 401 license_revoked"
                            : $"revoked, but a replay answered HTTP {status}: {Text.Clip(body, 80)}";
                        report.Moderation.Add($"{account.Handle}: {proof}");
                        break;
                    }

                    report.Moderation.Add($"{account.Handle}: {proof}");
                    break;
                }

                case ModerationRole.Ban:
                {
                    string error = await provisioner.AdminBanAsync(account.Handle, purge: false, ct).ConfigureAwait(false);
                    report.Moderation.Add(error.Length > 0
                        ? $"{account.Handle}: admin ban FAILED — {error}"
                        : $"{account.Handle}: banned; /v1/players/{account.Handle} is now "
                          + (api.Player(account.Handle).Exists ? "STILL VISIBLE" : "404, and its handles are retired"));
                    break;
                }

                case ModerationRole.Delete:
                {
                    string error = await provisioner.DeleteAsync(account, ct).ConfigureAwait(false);
                    report.Moderation.Add(error.Length > 0
                        ? $"{account.Handle}: delete-my-data FAILED — {error}"
                        : $"{account.Handle}: deleted its own account; /v1/players/{account.Handle} is now "
                          + (api.Player(account.Handle).Exists ? "STILL VISIBLE" : "404"));
                    break;
                }

                default:
                    break;
            }
        }

        if (report.Moderation.Count > 0)
            api.WaitForProjector(TimeSpan.FromSeconds(120));
    }

    // --- small helpers ---------------------------------------------------------------

    private static async Task<(long Checkpoint, long MaxSeq)> SequencesAsync(
        LoadOptions options, HttpMessageHandler transport, CancellationToken ct)
    {
        using var http = new HttpClient(transport, disposeHandler: false) { Timeout = TimeSpan.FromSeconds(30) };
        string body = await http.GetStringAsync(options.Admin + "/admin/stats", ct).ConfigureAwait(false);
        using JsonDocument document = JsonDocument.Parse(body);
        return (
            document.RootElement.GetProperty("projector").GetProperty("checkpoint_seq").GetInt64(),
            document.RootElement.GetProperty("events").GetProperty("max_seq").GetInt64());
    }

    /// <summary>
    /// The <c>catlog.lib</c> log sink for a load run: stderr, throttled.
    /// </summary>
    /// <remarks>
    /// The default sink writes to stdout, which would put library chatter in the middle of a
    /// <c>--report json</c> document. It also writes one WARN per clock resync, and a run with
    /// two hundred players takes thousands of those by design — so the first few are shown, in
    /// case one of them is the interesting kind, and the rest are counted.
    /// </remarks>
    private sealed class LoadLogger : IModLogger
    {
        private const int ShowFirst = 8;

        private int _warns;
        private int _errors;

        internal int Warnings => _warns;

        internal int Errors => _errors;

        public void Debug(string message)
        {
        }

        public void Info(string message)
        {
        }

        public void Warn(string message)
        {
            int n = Interlocked.Increment(ref _warns);
            if (n <= ShowFirst)
                Console.Error.WriteLine("  lib warn: " + Text.Clip(message, 160));
            else if (n == ShowFirst + 1)
                Console.Error.WriteLine("  lib warn: (further warnings suppressed; the tally is in the report)");
        }

        public void Error(string message, Exception? exception = null)
        {
            int n = Interlocked.Increment(ref _errors);
            if (n <= ShowFirst)
                Console.Error.WriteLine("  lib error: " + Text.Clip(message + (exception is null ? string.Empty : ": " + exception.Message), 200));
        }
    }

    private static void Emit(RunReport report)
    {
        if (report.Options.Report is ReportFormat.Text or ReportFormat.Both)
            report.WriteText();
        if (report.Options.Report is ReportFormat.Json or ReportFormat.Both)
            report.WriteJson();
    }
}
