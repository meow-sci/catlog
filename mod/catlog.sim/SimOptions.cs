using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using MeowSci.Catlog.Lib;

namespace MeowSci.Catlog.Sim;

/// <summary>Everything the CLI can be told (INITIAL_IMPL_PLAN §7.3).</summary>
public sealed class SimOptions
{
    /// <summary>The default public base URL (§3).</summary>
    public const string DefaultServer = "http://127.0.0.1:8080";

    /// <summary>The default loopback admin base URL (§3).</summary>
    public const string DefaultAdmin = "http://127.0.0.1:6060";

    /// <summary>The scenario to run; empty when only listing.</summary>
    public string Scenario { get; private set; } = string.Empty;

    /// <summary>The server's public base URL.</summary>
    public string Server { get; private set; } = DefaultServer;

    /// <summary>
    /// The loopback admin base URL. The simulator needs it for the deterministic projector wait
    /// (<see cref="ReadApiClient.WaitForProjector"/>), for <c>/debug/vars</c> and for the D22
    /// rebuild — none of which the public API exposes, by design (§5.9).
    /// </summary>
    public string Admin { get; private set; } = DefaultAdmin;

    /// <summary>Path to the §4.6 credential file.</summary>
    public string Credential { get; private set; } = string.Empty;

    /// <summary>Print the scenario catalog and exit.</summary>
    public bool List { get; private set; }

    /// <summary>Check the read API after the run.</summary>
    public bool Assert { get; private set; }

    /// <summary>
    /// Sim seconds played per wall second. Zero — the default — runs unpaced, which is what the
    /// assertions want; a finite value is for watching a scenario unfold.
    /// </summary>
    public double Speed { get; private set; }

    /// <summary>The install ULID; derived from the handle when not given, so <c>kid</c>s are stable across runs.</summary>
    public string? Install { get; private set; }

    /// <summary>
    /// Events per batch, and also the pending-count ship trigger.
    /// </summary>
    /// <remarks>
    /// The mod's own normal trigger is the ~60 s age trigger (§7.2), with a 500-event safety
    /// valve; both are right for a real session that produces events over minutes. A scenario
    /// produces the same events in milliseconds, where the age trigger would only ever fire on
    /// the final drain — so the runner pins the count trigger to this value and disables the age
    /// trigger outright (<see cref="ScenarioRunner"/>). Shipping deterministic full batches keeps
    /// a compressed 30 minutes of play inside the §4.3 token bucket (1 batch / 2 s, burst 5)
    /// instead of spending the run in backoff.
    /// </remarks>
    public int BatchEvents { get; private set; } = Wire.DefaultBatchEventCap;

    /// <summary>The ingest URL, sent verbatim as the proof's <c>htu</c> (§4.5.2 compares it by string equality).</summary>
    public string IngestUrl => Server + Wire.IngestPath;

    /// <summary>Parses command-line arguments.</summary>
    /// <param name="args">The raw arguments.</param>
    /// <returns>The parsed options.</returns>
    /// <exception cref="SimException">An argument was unknown, missing a value, or unparseable.</exception>
    public static SimOptions Parse(string[] args)
    {
        var options = new SimOptions();

        for (int i = 0; i < args.Length; i++)
        {
            string arg = args[i];
            switch (arg)
            {
                case "--list":
                case "-l":
                    options.List = true;
                    break;
                case "--assert":
                    options.Assert = true;
                    break;
                case "--scenario":
                case "-s":
                    options.Scenario = Value(args, ref i);
                    break;
                case "--server":
                    options.Server = Value(args, ref i).TrimEnd('/');
                    break;
                case "--admin":
                    options.Admin = Value(args, ref i).TrimEnd('/');
                    break;
                case "--credential":
                case "-c":
                    options.Credential = Value(args, ref i);
                    break;
                case "--install":
                    options.Install = Value(args, ref i);
                    break;
                case "--speed":
                    options.Speed = Double(Value(args, ref i), "--speed");
                    break;
                case "--batch":
                    options.BatchEvents = (int)Double(Value(args, ref i), "--batch");
                    break;
                case "":
                    break;
                default:
                    throw new SimException($"unknown argument '{arg}' (try --help)");
            }
        }

        // `make sim` always passes every flag, so empty strings mean "not set" rather than "set
        // to nothing" — treating them literally would send an empty --scenario as a lookup miss.
        if (string.IsNullOrWhiteSpace(options.Scenario))
            options.Scenario = string.Empty;
        if (string.IsNullOrWhiteSpace(options.Server))
            options.Server = DefaultServer;
        if (string.IsNullOrWhiteSpace(options.Admin))
            options.Admin = DefaultAdmin;
        if (options.BatchEvents < Wire.MinBatchEventCap)
            options.BatchEvents = Wire.MinBatchEventCap;

        return options;
    }

    /// <summary>
    /// The install ULID to run under: the <c>--install</c> value, else one derived from the
    /// handle so repeated runs produce the same <c>kid</c>s (§4.2 salts them with the install id).
    /// </summary>
    /// <param name="handle">The credential's handle.</param>
    /// <returns>A 26-character ULID.</returns>
    public string InstallIdFor(string handle)
    {
        if (!string.IsNullOrWhiteSpace(Install))
            return Install;

        byte[] digest = SHA256.HashData(Encoding.UTF8.GetBytes("catlog-sim-install:" + handle));

        // A ULID's first 6 bytes are a millisecond timestamp; masking the top byte keeps it inside
        // the encodable range so the value round-trips through Ulid.Parse.
        digest[0] &= 0x0F;
        return new Ulid(new ReadOnlySpan<byte>(digest, 0, 16)).ToString();
    }

    private static string Value(IReadOnlyList<string> args, ref int index)
    {
        if (index + 1 >= args.Count)
            throw new SimException($"'{args[index]}' needs a value");
        return args[++index];
    }

    private static double Double(string raw, string flag)
        => double.TryParse(raw, NumberStyles.Float, CultureInfo.InvariantCulture, out double value)
            ? value
            : throw new SimException($"'{flag}' needs a number, got '{raw}'");
}
