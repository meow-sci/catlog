using System;
using System.Collections.Generic;
using System.Globalization;
using MeowSci.Catlog.Lib;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>How player credentials are obtained.</summary>
internal enum AuthMode
{
    /// <summary>
    /// The real player path: <c>mockidp</c> mints a subject, catlogd runs the OAuth code
    /// exchange, sets a session cookie, and issues a license against a client-generated key
    /// through <c>POST /api/handles</c>. The default, because it is the only mode that exercises
    /// the identity stack.
    /// </summary>
    OAuth,

    /// <summary>
    /// <c>POST /admin/issue</c>: a synthetic <c>dev:</c> player, minted in one round trip. Fast,
    /// and useful as a control when the point is to isolate ingest from identity.
    /// </summary>
    Admin,
}

/// <summary>Which clock the shipper's 30-second floor is measured against.</summary>
internal enum ShipClock
{
    /// <summary>
    /// A virtual clock the harness winds forward, exactly as <c>catlog.sim</c> does. The floor is
    /// still enforced — on the timeline the shipper reads — but it costs no wall time, so the run
    /// measures the <b>server</b>. Every server-side limit stays real.
    /// </summary>
    Virtual,

    /// <summary>
    /// The real system clock and a real 30-second floor. Models a player's actual cadence; a run
    /// of any size takes hours. Use it to measure the <b>client</b>.
    /// </summary>
    Real,
}

/// <summary>What to print at the end.</summary>
internal enum ReportFormat
{
    /// <summary>Human-readable tables.</summary>
    Text,

    /// <summary>One JSON object, for a spreadsheet or a CI artifact.</summary>
    Json,

    /// <summary>Both, text first.</summary>
    Both,
}

/// <summary>Everything <c>catlog.loadgen</c> can be told.</summary>
internal sealed class LoadOptions
{
    /// <summary>The default public base URL.</summary>
    internal const string DefaultServer = "http://127.0.0.1:8080";

    /// <summary>The default loopback admin base URL.</summary>
    internal const string DefaultAdmin = "http://127.0.0.1:6060";

    /// <summary>The default mockidp base URL (§3).</summary>
    internal const string DefaultMockIdp = "http://127.0.0.1:9090";

    /// <summary>How many players to provision and run.</summary>
    internal int Players { get; private set; } = 25;

    /// <summary>Simulated play time per player, in sim seconds.</summary>
    internal double DurationSeconds { get; private set; } = 45 * 60;

    /// <summary>
    /// The RNG seed. Every gameplay decision in the run derives from it; see
    /// <see cref="Prng.ForPlayer"/> for how that survives concurrency.
    /// </summary>
    internal long Seed { get; private set; }

    /// <summary>True when <see cref="Seed"/> was chosen for the operator rather than given.</summary>
    internal bool SeedWasGenerated { get; private set; }

    /// <summary>
    /// Namespaces the <i>identities</i> — the mockidp subjects and the handles — separately from
    /// <see cref="Seed"/>, which namespaces the <i>gameplay</i>. Two runs with the same seed and
    /// different namespaces produce the same event stream under different players, which is what
    /// makes a seed re-runnable against a database that already holds the previous run.
    /// </summary>
    internal string Namespace { get; private set; } = string.Empty;

    /// <summary>The server's public base URL.</summary>
    internal string Server { get; private set; } = DefaultServer;

    /// <summary>The loopback admin base URL.</summary>
    internal string Admin { get; private set; } = DefaultAdmin;

    /// <summary>The mockidp base URL.</summary>
    internal string MockIdp { get; private set; } = DefaultMockIdp;

    /// <summary>How credentials are obtained.</summary>
    internal AuthMode Auth { get; private set; } = AuthMode.OAuth;

    /// <summary>Which identity provider generated accounts use; empty means all three.</summary>
    internal string IdP { get; private set; } = string.Empty;

    /// <summary>How many players run at once.</summary>
    internal int Concurrency { get; private set; }

    /// <summary>Events per batch, and the pending-count ship trigger.</summary>
    internal int BatchEvents { get; private set; } = Wire.DefaultBatchEventCap;

    /// <summary>
    /// The age trigger, in <b>sim</b> seconds: ship when the oldest pending event is this old.
    /// Defaults to the mod's own <see cref="Wire.ShipAgeTriggerSeconds"/>, so a run's batch
    /// cadence is the cadence a real player produces. Raising it trades fidelity for fewer, fatter
    /// batches, which is what to do when the point is bytes per second rather than requests per
    /// second. It can never go below the hard floor.
    /// </summary>
    internal double ShipAgeSeconds { get; private set; } = Wire.ShipAgeTriggerSeconds;

    /// <summary>Which clock the ship floor is measured against.</summary>
    internal ShipClock Clock { get; private set; } = ShipClock.Virtual;

    /// <summary>Concurrent read-API clients hammering the public endpoints during ingest.</summary>
    internal int Readers { get; private set; } = 4;

    /// <summary>Requests per second per reader; 0 runs them flat out.</summary>
    internal double ReadRps { get; private set; } = 5;

    /// <summary>Subscribe to <c>GET /v1/feed/stream</c> for the duration of the run.</summary>
    internal bool Feed { get; private set; } = true;

    /// <summary>
    /// Percentage of provisioned players that also exercise a moderation path — reissue, revoke,
    /// admin ban, or delete-my-data.
    /// </summary>
    internal int ModerationPercent { get; private set; } = 8;

    /// <summary>
    /// Percentage of generated identities deliberately minted too new for the ≥30-day account-age
    /// gate. These are <b>expected to be refused</b>; they never become players.
    /// </summary>
    internal int TooNewPercent { get; private set; } = 5;

    /// <summary>Re-send one accepted batch verbatim to prove the server's replay short-circuit.</summary>
    internal bool DedupProbe { get; private set; } = true;

    /// <summary>Check the end-to-end invariants and exit non-zero when one fails.</summary>
    internal bool Assert { get; private set; }

    /// <summary>What to print.</summary>
    internal ReportFormat Report { get; private set; } = ReportFormat.Text;

    /// <summary>Keep the per-player outbox directories instead of deleting them.</summary>
    internal bool KeepOutboxes { get; private set; }

    /// <summary>Ceiling on the whole run, in seconds; 0 disables it.</summary>
    internal double TimeoutSeconds { get; private set; } = 1800;

    /// <summary>Print a line per player as it finishes.</summary>
    internal bool Verbose { get; private set; }

    /// <summary>The ingest URL, sent verbatim as the proof's <c>htu</c>.</summary>
    internal string IngestUrl => Server + Wire.IngestPath;

    /// <summary>Parses the command line.</summary>
    /// <param name="args">Raw arguments.</param>
    /// <returns>The parsed options.</returns>
    /// <exception cref="SimException">An argument was unknown, missing a value, or unparseable.</exception>
    internal static LoadOptions Parse(string[] args)
    {
        var o = new LoadOptions();
        // Tracked rather than inferred from the value: `--seed 0` is a seed an operator can
        // legitimately pin, and testing `Seed == 0` would silently replace it with a random one
        // and then print "(chosen; pass --seed to replay)" for a run they thought was pinned.
        bool seeded = false;

        for (int i = 0; i < args.Length; i++)
        {
            string arg = args[i];
            switch (arg)
            {
                case "": break;
                case "--players" or "-n": o.Players = Int(Value(args, ref i), arg, 1, 100_000); break;
                case "--duration" or "-d": o.DurationSeconds = Duration(Value(args, ref i), arg); break;
                case "--seed": o.Seed = Long(Value(args, ref i), arg); seeded = true; break;
                case "--namespace": o.Namespace = Value(args, ref i).Trim(); break;
                case "--server": o.Server = Url(Value(args, ref i), DefaultServer); break;
                case "--admin": o.Admin = Url(Value(args, ref i), DefaultAdmin); break;
                case "--mockidp": o.MockIdp = Url(Value(args, ref i), DefaultMockIdp); break;
                case "--auth": o.Auth = Auth_(Value(args, ref i)); break;
                case "--idp": o.IdP = IdP_(Value(args, ref i)); break;
                case "--concurrency" or "-c": o.Concurrency = Int(Value(args, ref i), arg, 1, 10_000); break;
                case "--batch": o.BatchEvents = Int(Value(args, ref i), arg, Wire.MinBatchEventCap, Wire.MaxEventsPerBatch); break;
                case "--ship-age": o.ShipAgeSeconds = Duration(Value(args, ref i), arg); break;
                case "--clock": o.Clock = Clock_(Value(args, ref i)); break;
                case "--readers": o.Readers = Int(Value(args, ref i), arg, 0, 1_000); break;
                case "--read-rps": o.ReadRps = Number(Value(args, ref i), arg); break;
                case "--feed": o.Feed = true; break;
                case "--no-feed": o.Feed = false; break;
                case "--moderation": o.ModerationPercent = Int(Value(args, ref i), arg, 0, 100); break;
                case "--too-new": o.TooNewPercent = Int(Value(args, ref i), arg, 0, 100); break;
                case "--dedup-probe": o.DedupProbe = true; break;
                case "--no-dedup-probe": o.DedupProbe = false; break;
                case "--assert": o.Assert = true; break;
                case "--report": o.Report = Report_(Value(args, ref i)); break;
                case "--keep-outboxes": o.KeepOutboxes = true; break;
                case "--timeout": o.TimeoutSeconds = Duration(Value(args, ref i), arg); break;
                case "--verbose" or "-v": o.Verbose = true; break;
                default: throw new SimException($"unknown argument '{arg}' (try --help)");
            }
        }

        if (!seeded)
        {
            // A run with no seed still has to be re-runnable, so one is chosen and printed rather
            // than left implicit.
            o.Seed = Math.Abs(BitConverter.ToInt64(Guid.NewGuid().ToByteArray(), 0)) % 1_000_000_007;
            if (o.Seed == 0)
                o.Seed = 1;
            o.SeedWasGenerated = true;
        }

        if (string.IsNullOrWhiteSpace(o.Namespace))
        {
            // Identities must not collide between runs against the same database, so the default
            // namespace is time-derived — the one thing in the harness that is deliberately not a
            // function of the seed. Pin it with --namespace to reuse a run's players.
            o.Namespace = "lg" + DateTimeOffset.UtcNow.ToUnixTimeMilliseconds().ToString("x", CultureInfo.InvariantCulture);
        }

        if (o.Concurrency <= 0)
        {
            // Players are network-bound far more than CPU-bound (each spends most of its life
            // waiting out the server's per-credential token bucket), so the default oversubscribes
            // the machine on purpose.
            o.Concurrency = Math.Clamp(Environment.ProcessorCount * 4, 4, 256);
        }

        // The floor is not negotiable from a command line either. Wire.MinShipAgeTriggerSeconds
        // *is* Wire.MinShipIntervalSeconds: a configured cadence faster than the hard floor would
        // be a number that does not describe what happens.
        o.ShipAgeSeconds = Math.Clamp(
            o.ShipAgeSeconds, Wire.MinShipAgeTriggerSeconds, Wire.MaxShipAgeTriggerSeconds);

        o.Concurrency = Math.Min(o.Concurrency, o.Players);
        return o;
    }

    /// <summary>Writes <c>--help</c>.</summary>
    internal static void Usage()
    {
        Console.WriteLine("catlog.loadgen — high-volume, randomised, many-player load harness");
        Console.WriteLine();
        Console.WriteLine("usage: catlog.loadgen [options]");
        Console.WriteLine();
        Console.WriteLine("Provisions N players through the real mockidp OAuth flow, invents a plausible");
        Console.WriteLine("CAREER for each of them, and drives every one through the real catlog.lib");
        Console.WriteLine("pipeline (detector -> outbox -> ES256 proof -> brotli batch -> POST /v1/ingest)");
        Console.WriteLine("at a live catlogd. Nothing is hand-authored: no envelope is ever built here.");
        Console.WriteLine();
        Console.WriteLine("Each player arrives with in-game time already on the clock and can only attempt");
        Console.WriteLine("what it has earned: pad tests and hops, then suborbital lobs, orbit and orbital");
        Console.WriteLine("manoeuvres, rendezvous and docking, transfers to other bodies, landings, and");
        Console.WriteLine("probes to the outer system. Fleet size and craft in flight at once grow with it,");
        Console.WriteLine("and so does competence: beginners lose vehicles on the pad and at max-Q,");
        Console.WriteLine("veterans lose them on approach and while docking. --duration is the window this");
        Console.WriteLine("run watches, not the length of the career.");
        Console.WriteLine();
        Console.WriteLine("scale");
        Console.WriteLine("  -n, --players <n>       players to provision and run (default 25)");
        Console.WriteLine("  -d, --duration <spec>   simulated play per player: 90s, 45m, 3h (default 45m)");
        Console.WriteLine("  -c, --concurrency <n>   players in flight at once (default 4x cores, <= players)");
        Console.WriteLine("      --batch <n>         events per batch and pending-ship trigger "
                          + $"(default {Wire.DefaultBatchEventCap}, {Wire.MinBatchEventCap}..{Wire.MaxEventsPerBatch})");
        Console.WriteLine("      --ship-age <spec>   age trigger in SIM seconds (default 60s, the mod's own).");
        Console.WriteLine("                          Raise it for fewer, fatter batches; it can never go");
        Console.WriteLine($"                          below the hard {Wire.MinShipIntervalSeconds:0} s floor.");
        Console.WriteLine();
        Console.WriteLine("reproducibility");
        Console.WriteLine("      --seed <n>          gameplay seed; the same seed replays the same event");
        Console.WriteLine("                          stream, whatever the concurrency (default: chosen and printed)");
        Console.WriteLine("      --namespace <s>     identity namespace: mockidp subjects and handles.");
        Console.WriteLine("                          Defaults to a timestamp so a re-run does not collide");
        Console.WriteLine("                          with the players the last one claimed.");
        Console.WriteLine();
        Console.WriteLine("targets");
        Console.WriteLine($"      --server <url>      public base URL (default {DefaultServer})");
        Console.WriteLine($"      --admin <url>       loopback admin base URL (default {DefaultAdmin})");
        Console.WriteLine($"      --mockidp <url>     mock identity provider (default {DefaultMockIdp})");
        Console.WriteLine();
        Console.WriteLine("identity");
        Console.WriteLine("      --auth oauth|admin  oauth (default): mockidp -> catlogd callback -> session");
        Console.WriteLine("                          -> POST /api/handles with a client-generated key.");
        Console.WriteLine("                          admin: POST /admin/issue, the fast path; skips the");
        Console.WriteLine("                          entire identity stack and is the ingest-only control.");
        Console.WriteLine("      --idp <name>        discord | google | github; default is all three,");
        Console.WriteLine("                          round-robined, so every subject-resolution path runs");
        Console.WriteLine("      --too-new <pct>     percent of identities minted too young for the >=30-day");
        Console.WriteLine("                          account-age gate; they are expected to be REFUSED (default 5)");
        Console.WriteLine("      --moderation <pct>  percent of players that also exercise reissue, revoke,");
        Console.WriteLine("                          admin ban or delete-my-data (default 8)");
        Console.WriteLine();
        Console.WriteLine("load shape");
        Console.WriteLine("      --clock virtual|real");
        Console.WriteLine("                          virtual (default): the client's hard 30 s ship floor is");
        Console.WriteLine("                          wound forward on an injected clock, so the run measures");
        Console.WriteLine("                          the SERVER. Every server limit stays real: the");
        Console.WriteLine("                          per-credential token bucket (1 batch / 2 s, burst 5),");
        Console.WriteLine("                          the 256-deep write channel and its 503 + Retry-After.");
        Console.WriteLine("                          real: a real 30 s floor per player, which measures the");
        Console.WriteLine("                          CLIENT and takes hours.");
        Console.WriteLine("      --readers <n>       concurrent read-API clients during ingest (default 4)");
        Console.WriteLine("      --read-rps <n>      requests per second per reader; 0 = flat out (default 5)");
        Console.WriteLine("      --feed / --no-feed  subscribe to GET /v1/feed/stream while the run happens");
        Console.WriteLine();
        Console.WriteLine("checks and output");
        Console.WriteLine("      --assert            check the invariants and exit 1 if any fails");
        Console.WriteLine("      --dedup-probe / --no-dedup-probe");
        Console.WriteLine("                          re-send one accepted batch verbatim and require the");
        Console.WriteLine("                          server's replay short-circuit to swallow it (default on)");
        Console.WriteLine("      --report text|json|both   (default text). Progress goes to stderr and");
        Console.WriteLine("                          the report to stdout, so json pipes straight into jq.");
        Console.WriteLine("      --timeout <spec>    ceiling on the whole run (default 30m; 0 disables)");
        Console.WriteLine("      --keep-outboxes     leave the per-player temp outboxes on disk");
        Console.WriteLine("  -v, --verbose           one line per player as it finishes");
        Console.WriteLine("  -h, --help              this");
        Console.WriteLine();
        Console.WriteLine("going fast (millions of events)");
        Console.WriteLine("  The default ceiling is the SERVER, not this harness. §4.3 allows one batch");
        Console.WriteLine("  per two seconds per credential (burst 5), so a player shipping 500-event");
        Console.WriteLine("  batches cannot exceed 250 events/s however fast the machine is, and a default");
        Console.WriteLine("  run spends ~85% of its wall clock in Retry-After sleeps. The \"where the time");
        Console.WriteLine("  went\" table in the report shows exactly that, run by run.");
        Console.WriteLine();
        Console.WriteLine("  Three levers, in the order they pay off:");
        Console.WriteLine();
        Console.WriteLine("  1. Take the token bucket out of the measurement. Start catlogd with");
        Console.WriteLine("       CATLOG_LIMITS_RATELIMIT_DISABLED=1 server/bin/catlogd -config …");
        Console.WriteLine("     §4.5.3 step 9 is then not part of the chain at all and no batch is ever");
        Console.WriteLine("     429'd. COST: the per-credential bucket is the only thing bounding what one");
        Console.WriteLine("     stolen credential can do to the server, so catlogd refuses to start with");
        Console.WriteLine("     this on an https base_url and warns for as long as it runs with it on.");
        Console.WriteLine("     SAFE WHEN: a throwaway local server you are deliberately measuring.");
        Console.WriteLine("     To go faster but stay limited, raise the rate instead — that needs no gate:");
        Console.WriteLine("       CATLOG_LIMITS_RATELIMIT_PER_JKT_PER_S=50 server/bin/catlogd -config …");
        Console.WriteLine();
        Console.WriteLine("  2. Send fewer, fatter batches:  --ship-age 1h --batch 2000");
        Console.WriteLine("     At the default 60 s age trigger a batch carries ~15 events, so a million");
        Console.WriteLine("     events is ~68,000 requests. At 1h it carries ~600 and the same million is");
        Console.WriteLine("     ~1,600. COST: fidelity. Real players ship every 60 s, so this stops");
        Console.WriteLine("     modelling request rate and starts modelling bytes per second. Leave it");
        Console.WriteLine("     alone when the question is \"how many clients can catlogd hold\".");
        Console.WriteLine();
        Console.WriteLine("  3. Match --concurrency to the machine. The default oversubscribes (4x cores)");
        Console.WriteLine("     because throttled players spend their lives asleep; with the bucket out of");
        Console.WriteLine("     the way they are CPU-bound and oversubscription LOSES throughput. Try");
        Console.WriteLine($"     -c {Environment.ProcessorCount} (this machine's core count) for an unthrottled run.");
        Console.WriteLine();
        Console.WriteLine("  Volume comes from player-hours: a player generates ~780 events per simulated");
        Console.WriteLine("  hour, so a million events needs ~1,300 of them (e.g. -n 500 -d 2.6h). None of");
        Console.WriteLine("  this bypasses anything: every batch is still brotli-compressed, ES256-signed,");
        Console.WriteLine("  and verified twice by the server. What binds after the bucket is gone is the");
        Console.WriteLine("  projector, which folds an order of magnitude slower than ingest stores — and");
        Console.WriteLine("  --assert cannot start until it reaches the head. Budget for it.");
        Console.WriteLine();
        Console.WriteLine("Requires a running catlogd and mockidp:  make dev");
        Console.WriteLine("Everything is loopback. No external host is contacted, ever.");
    }

    private static string Value(IReadOnlyList<string> args, ref int index)
        => index + 1 < args.Count ? args[++index] : throw new SimException($"'{args[index]}' needs a value");

    private static string Url(string raw, string fallback)
        => string.IsNullOrWhiteSpace(raw) ? fallback : raw.TrimEnd('/');

    private static int Int(string raw, string flag, int min, int max)
    {
        if (!int.TryParse(raw, NumberStyles.Integer, CultureInfo.InvariantCulture, out int value))
            throw new SimException($"'{flag}' needs a whole number, got '{raw}'");
        if (value < min || value > max)
            throw new SimException($"'{flag}' must be between {min} and {max}, got {value}");
        return value;
    }

    private static long Long(string raw, string flag)
        => long.TryParse(raw, NumberStyles.Integer, CultureInfo.InvariantCulture, out long value)
            ? value
            : throw new SimException($"'{flag}' needs a whole number, got '{raw}'");

    private static double Number(string raw, string flag)
        => double.TryParse(raw, NumberStyles.Float, CultureInfo.InvariantCulture, out double value) && value >= 0
            ? value
            : throw new SimException($"'{flag}' needs a non-negative number, got '{raw}'");

    /// <summary>Parses <c>90</c>, <c>90s</c>, <c>45m</c>, <c>3h</c> into seconds.</summary>
    private static double Duration(string raw, string flag)
    {
        string text = raw.Trim();
        if (text.Length == 0)
            throw new SimException($"'{flag}' needs a duration, e.g. 45m");

        double scale = char.ToLowerInvariant(text[^1]) switch
        {
            's' => 1.0,
            'm' => 60.0,
            'h' => 3600.0,
            'd' => 86_400.0,
            _ => 0.0,
        };
        if (scale > 0)
            text = text[..^1];
        else
            scale = 1.0;

        if (!double.TryParse(text, NumberStyles.Float, CultureInfo.InvariantCulture, out double value) || value < 0)
            throw new SimException($"'{flag}' needs a duration like 90s, 45m or 3h, got '{raw}'");
        return value * scale;
    }

    private static AuthMode Auth_(string raw) => raw.ToLowerInvariant() switch
    {
        "oauth" or "" => AuthMode.OAuth,
        "admin" => AuthMode.Admin,
        _ => throw new SimException($"'--auth' is oauth or admin, got '{raw}'"),
    };

    private static ShipClock Clock_(string raw) => raw.ToLowerInvariant() switch
    {
        "virtual" or "" => ShipClock.Virtual,
        "real" => ShipClock.Real,
        _ => throw new SimException($"'--clock' is virtual or real, got '{raw}'"),
    };

    private static ReportFormat Report_(string raw) => raw.ToLowerInvariant() switch
    {
        "text" or "" => ReportFormat.Text,
        "json" => ReportFormat.Json,
        "both" => ReportFormat.Both,
        _ => throw new SimException($"'--report' is text, json or both, got '{raw}'"),
    };

    private static string IdP_(string raw) => raw.ToLowerInvariant() switch
    {
        "" or "mixed" or "all" => string.Empty,
        "discord" or "google" or "github" => raw.ToLowerInvariant(),
        _ => throw new SimException($"'--idp' is discord, google, github or mixed, got '{raw}'"),
    };
}
