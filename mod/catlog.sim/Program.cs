using System;
using System.Collections.Generic;
using System.Globalization;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Sim.Scenarios;

namespace MeowSci.Catlog.Sim;

/// <summary>
/// The <c>catlog.sim</c> entry point: plays a scripted gameplay scenario through the real
/// <c>catlog.lib</c> pipeline at a live server, then checks the leaderboards it should have moved
/// (INITIAL_IMPL_PLAN §7.3).
/// </summary>
/// <remarks>
/// No part of this program fabricates an <c>EventEnvelope</c>. Scenarios emit telemetry snapshots
/// and game signals — the two things a Harmony patch can produce — and the detector, the window
/// accumulator, the impact correlator, the SQLite outbox, the ES256 proof signer and the batch
/// shipper all do their real jobs. That is the point: this harness is the only place the whole
/// client is exercised end to end without the game, and a regression anywhere in it must be able
/// to fail a scenario.
/// </remarks>
public static class Program
{
    /// <summary>Exit code when every assertion passed (or none were requested).</summary>
    public const int ExitOk = 0;

    /// <summary>Exit code when a board assertion failed.</summary>
    public const int ExitAssertionFailed = 1;

    /// <summary>Exit code when the arguments or the environment were wrong.</summary>
    public const int ExitUsage = 2;

    /// <summary>Runs the simulator.</summary>
    /// <param name="args">Command-line arguments.</param>
    /// <returns>Process exit code.</returns>
    public static int Main(string[] args)
    {
        foreach (string arg in args)
        {
            if (arg is "--help" or "-h")
            {
                Usage();
                return ExitOk;
            }
        }

        SimOptions options;
        try
        {
            options = SimOptions.Parse(args);
        }
        catch (SimException ex)
        {
            Console.Error.WriteLine("catlog.sim: " + ex.Message);
            return ExitUsage;
        }

        if (options.List || string.IsNullOrEmpty(options.Scenario))
        {
            List();
            return options.List ? ExitOk : ExitUsage;
        }

        try
        {
            return RunScenario(options);
        }
        catch (SimException ex)
        {
            Console.Error.WriteLine("catlog.sim: " + ex.Message);
            return ExitUsage;
        }
    }

    private static int RunScenario(SimOptions options)
    {
        IScenario scenario = ScenarioCatalog.Find(options.Scenario);

        CredentialLoadResult loaded = Credential.Load(options.Credential);
        if (!loaded.Ok)
        {
            throw new SimException(
                $"{loaded.Error}. Mint one with: server/bin/catlogctl issue --handle <name> --out <dir>");
        }

        using Credential credential = loaded.Credential!;
        using var api = new ReadApiClient(options.Server, options.Admin);

        Console.WriteLine($"catlog.sim — {scenario.Name}");
        Console.WriteLine($"  {scenario.Summary}");
        Console.WriteLine($"  server     {options.Server}   (ingest {options.IngestUrl})");
        Console.WriteLine($"  admin      {options.Admin}");
        Console.WriteLine($"  handle     {credential.Handle}   (jkt {credential.Jkt})");
        Console.WriteLine($"  license    expires {credential.Claims.Expiry:u}");
        Console.WriteLine();

        Baseline baseline = api.CaptureBaseline(credential.Handle);
        Console.WriteLine(baseline.Player.Exists
            ? $"baseline: {credential.Handle} is on {baseline.Player.Stats.Count} board(s); "
              + $"{baseline.TotalEvents} events stored"
            : $"baseline: {credential.Handle} is on no boards yet; {baseline.TotalEvents} events stored");

        using var runner = new ScenarioRunner(options, credential);
        RunSummary summary = runner.Run(scenario);
        api.Run = summary;

        Console.WriteLine();
        Console.WriteLine($"ran {scenario.Name}: {summary.Frames} frames → {summary.Events} events "
                          + $"→ {summary.Batches} batches ({summary.Shipped} accepted) in {summary.Duration.TotalSeconds:0.00} s");
        Console.WriteLine($"  session {summary.SessionId}  install {summary.InstallId}");
        PrintEventTypes(summary.EventsByType);

        if (!options.Assert)
        {
            Console.WriteLine();
            Console.WriteLine("no assertions requested (pass --assert to check the leaderboards)");
            return ExitOk;
        }

        Console.WriteLine();
        Console.WriteLine("waiting for the projector to fold every stored event…");
        api.WaitForProjector(TimeSpan.FromSeconds(60));
        scenario.Assert(api, credential.Handle);

        return PrintChecks(api) ? ExitOk : ExitAssertionFailed;
    }

    private static void PrintEventTypes(IReadOnlyDictionary<string, int> byType)
    {
        var names = new List<string>(byType.Keys);
        names.Sort(StringComparer.Ordinal);
        foreach (string name in names)
            Console.WriteLine($"    {byType[name],6}  {name}");
    }

    private static bool PrintChecks(ReadApiClient api)
    {
        int width = 4;
        foreach (CheckResult check in api.Checks)
            width = Math.Max(width, check.Label.Length);

        Console.WriteLine();
        Console.WriteLine("assertions");
        foreach (CheckResult check in api.Checks)
        {
            Console.WriteLine(
                $"  {(check.Ok ? "PASS" : "FAIL")}  {check.Label.PadRight(width)}  "
                + $"expected {check.Expected}, got {check.Actual}");
            Console.WriteLine($"        {new string(' ', width)}  {check.Note}");
        }

        Console.WriteLine();
        if (api.AllOk)
        {
            Console.WriteLine(
                $"OK — {api.Checks.Count.ToString(CultureInfo.InvariantCulture)} assertions passed");
            return true;
        }

        int failed = 0;
        foreach (CheckResult check in api.Checks)
        {
            if (!check.Ok)
                failed++;
        }

        Console.Error.WriteLine($"FAILED — {failed} of {api.Checks.Count} assertions failed");
        return false;
    }

    private static void List()
    {
        Console.WriteLine("catlog.sim scenarios (INITIAL_IMPL_PLAN §7.3)");
        Console.WriteLine();
        foreach (IScenario scenario in ScenarioCatalog.All)
        {
            Console.WriteLine($"  {scenario.Name}");
            Console.WriteLine($"      {scenario.Summary}");
            Console.WriteLine($"      asserts: {scenario.Asserts}");
            Console.WriteLine();
        }
    }

    private static void Usage()
    {
        Console.WriteLine("catlog.sim — gameplay simulator for the catlog client pipeline");
        Console.WriteLine();
        Console.WriteLine("usage: catlog.sim --scenario <name> --credential <path> [options]");
        Console.WriteLine("       catlog.sim --list");
        Console.WriteLine();
        Console.WriteLine("options:");
        Console.WriteLine("  --scenario, -s <name>   scenario to play (see --list)");
        Console.WriteLine($"  --server <url>          public base URL (default {SimOptions.DefaultServer})");
        Console.WriteLine($"  --admin <url>           loopback admin base URL (default {SimOptions.DefaultAdmin})");
        Console.WriteLine("  --credential, -c <path> catlog-credential.json (§4.6)");
        Console.WriteLine("  --list, -l              print the scenario catalog and exit");
        Console.WriteLine("  --assert                check the leaderboards after the run");
        Console.WriteLine("  --speed <n>             sim seconds per wall second; 0 (default) runs unpaced");
        Console.WriteLine("  --batch <n>             events per batch and pending-ship trigger");
        Console.WriteLine("  --install <ulid>        install id to run under (default: derived from the handle)");
        Console.WriteLine();
        Console.WriteLine("The credential's handle is the player the scenario scores for. Mint one with:");
        Console.WriteLine("  server/bin/catlogctl issue -handle sim_cat -out /tmp/simcred");
    }
}
