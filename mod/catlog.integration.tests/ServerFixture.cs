using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using Xunit;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>A credential minted by <c>catlogctl issue</c> against the fixture's server (§4.6).</summary>
public sealed record IssuedCredential(string Handle, string Path, Credential Credential) : IDisposable
{
    public void Dispose() => Credential.Dispose();
}

/// <summary>
/// A real <c>catlogd</c> on a random loopback port with a throwaway data directory
/// (§7.5 — see docs/mod.md).
/// </summary>
/// <remarks>
/// <para>
/// Nothing here is a test double. The binary under test is <c>server/bin/catlogd</c> as built by
/// <c>make server-build</c>, credentials come from <c>catlogctl issue</c> against the live admin
/// API, and the mod side is <c>catlog.lib</c>'s own <see cref="Credential"/>,
/// <see cref="MeowSci.Catlog.Lib.Ship.ProofSigner"/>, <see cref="MeowSci.Catlog.Lib.Ship.BrotliCodec"/>
/// and <see cref="MeowSci.Catlog.Lib.Ship.BatchShipper"/>. That is the whole point of the suite:
/// the two implementations of §4.5 only agree if they are made to talk to each other.
/// </para>
/// <para>
/// Ports are reserved by binding <c>:0</c> and released again before the server starts, rather than
/// letting the server pick. §4.5.2 compares the proof's <c>htu</c> to the configured
/// <c>accepted_htu</c> by exact string equality, so the port has to be known before the process
/// exists — the same reason <c>server/integration</c> does it this way on the Go side.
/// </para>
/// <para>
/// <c>CATLOG_SERVER_URL</c> points the fixture at an already-running server instead (§7.5), with
/// <c>CATLOG_ADMIN_URL</c> for its admin mux; subclasses that need specific server configuration
/// override <see cref="AlwaysSpawn"/> and keep their own process either way.
/// </para>
/// </remarks>
public class ServerFixture : IAsyncLifetime
{
    private static readonly TimeSpan StartupTimeout = TimeSpan.FromSeconds(60);

    private readonly HttpClient _http = new() { Timeout = TimeSpan.FromSeconds(30) };
    private readonly List<IssuedCredential> _issued = [];

    private Process? _process;
    private int _publicPort;
    private int _adminPort;

    /// <summary>The public base URL.</summary>
    public string BaseUrl { get; private set; } = string.Empty;

    /// <summary>The loopback admin base URL.</summary>
    public string AdminUrl { get; private set; } = string.Empty;

    /// <summary>The ingest endpoint; the exact string every proof's <c>htu</c> must carry.</summary>
    public string IngestUrl => BaseUrl + "/v1/ingest";

    /// <summary>The server's data directory, or an empty string when an external server is in use.</summary>
    public string DataDir { get; private set; } = string.Empty;

    /// <summary>True when the fixture attached to an externally started server.</summary>
    public bool UsingExternalServer { get; private set; }

    /// <summary>Extra <c>CATLOG_*</c> environment entries for the spawned server.</summary>
    protected virtual IReadOnlyList<string> ExtraEnvironment => [];

    /// <summary>
    /// True when this fixture must own its process — because it reconfigures the server or stops
    /// and restarts it — and therefore ignores <c>CATLOG_SERVER_URL</c>.
    /// </summary>
    protected virtual bool AlwaysSpawn => false;

    public async Task InitializeAsync()
    {
        string? external = Environment.GetEnvironmentVariable("CATLOG_SERVER_URL");
        if (!AlwaysSpawn && !string.IsNullOrWhiteSpace(external))
        {
            UsingExternalServer = true;
            BaseUrl = external.TrimEnd('/');
            AdminUrl = (Environment.GetEnvironmentVariable("CATLOG_ADMIN_URL") ?? "http://127.0.0.1:6060").TrimEnd('/');
            await WaitForHealthAsync();
            return;
        }

        DataDir = Path.Combine(Path.GetTempPath(), "catlog-itest-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(DataDir);
        _publicPort = FreePort();
        _adminPort = FreePort();
        BaseUrl = $"http://127.0.0.1:{_publicPort}";
        AdminUrl = $"http://127.0.0.1:{_adminPort}";
        Start();
        await WaitForHealthAsync();
    }

    public Task DisposeAsync()
    {
        foreach (IssuedCredential credential in _issued)
            credential.Dispose();
        _issued.Clear();

        Stop();
        _http.Dispose();

        if (DataDir.Length > 0)
        {
            try
            {
                Directory.Delete(DataDir, recursive: true);
            }
            catch (IOException)
            {
                // A leftover temp directory is not a test failure.
            }
        }

        return Task.CompletedTask;
    }

    /// <summary>Starts (or restarts) the server process against the same data directory and ports.</summary>
    public void Start()
    {
        if (UsingExternalServer || _process is { HasExited: false })
            return;

        var info = new ProcessStartInfo(RepoLayout.Catlogd)
        {
            WorkingDirectory = RepoLayout.Root,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
        };

        info.Environment["CATLOG_DATA_DIR"] = DataDir;
        info.Environment["CATLOG_SERVER_LISTEN"] = $"127.0.0.1:{_publicPort}";
        info.Environment["CATLOG_SERVER_ADMIN_LISTEN"] = $"127.0.0.1:{_adminPort}";
        info.Environment["CATLOG_SERVER_BASE_URL"] = BaseUrl;
        info.Environment["CATLOG_INGEST_ACCEPTED_HTU"] = IngestUrl;
        // Nothing here serves HTML, and pointing at ../site/dist would depend on a pnpm build.
        info.Environment["CATLOG_SERVER_STATIC_DIR"] = string.Empty;
        // §4.3's bucket is 1 batch / 2 s burst 5. These tests deliberately ship bursts (the 413
        // halving ladder alone costs eight requests), so the limiter is opened up — it has its own
        // coverage in server/integration.
        info.Environment["CATLOG_LIMITS_RATELIMIT_BURST"] = "200";
        info.Environment["CATLOG_LIMITS_RATELIMIT_PER_JKT_PER_S"] = "100";
        info.Environment["CATLOG_DATA_CHECKPOINT_INTERVAL_S"] = "1";
        foreach (string entry in ExtraEnvironment)
        {
            int split = entry.IndexOf('=', StringComparison.Ordinal);
            info.Environment[entry[..split]] = entry[(split + 1)..];
        }

        _process = Process.Start(info)
                   ?? throw new InvalidOperationException("could not start " + RepoLayout.Catlogd);

        // Drain the pipes so a chatty server cannot fill its buffer and block.
        _process.OutputDataReceived += (_, _) => { };
        _process.ErrorDataReceived += (_, _) => { };
        _process.BeginOutputReadLine();
        _process.BeginErrorReadLine();
    }

    /// <summary>
    /// Stops the server with SIGINT and waits for it to exit.
    /// </summary>
    /// <remarks>
    /// A clean exit is load-bearing, not politeness: <c>store.DB.Close</c> runs
    /// <c>PRAGMA wal_checkpoint(TRUNCATE)</c>, and Turso takes an exclusive whole-file lock while
    /// it runs, so nothing else can read <c>events.db</c> until the process is gone and the WAL is
    /// folded in.
    /// </remarks>
    public void Stop()
    {
        if (_process is null || _process.HasExited)
        {
            _process = null;
            return;
        }

        try
        {
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                _process.Kill(entireProcessTree: true);
            }
            else
            {
                // .NET exposes no "send SIGINT"; /bin/kill is the portable-enough way to ask for a
                // graceful shutdown rather than SIGKILL.
                using Process? kill = Process.Start("/bin/kill", $"-INT {_process.Id}");
                kill?.WaitForExit(5_000);
            }

            if (!_process.WaitForExit(30_000))
                _process.Kill(entireProcessTree: true);
        }
        catch (InvalidOperationException)
        {
            // Already gone.
        }
        finally
        {
            _process?.Dispose();
            _process = null;
        }
    }

    /// <summary>Mints a credential for <paramref name="handle"/> through <c>catlogctl issue</c>.</summary>
    /// <param name="handle">The handle to claim.</param>
    /// <returns>The loaded credential; disposed with the fixture.</returns>
    public IssuedCredential Issue(string handle)
    {
        string outDir = Path.Combine(Path.GetTempPath(), "catlog-cred-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(outDir);

        var info = new ProcessStartInfo(RepoLayout.Catlogctl)
        {
            WorkingDirectory = RepoLayout.Root,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
        };
        info.ArgumentList.Add("issue");
        info.ArgumentList.Add("-handle");
        info.ArgumentList.Add(handle);
        info.ArgumentList.Add("-out");
        info.ArgumentList.Add(outDir);
        info.ArgumentList.Add("-admin");
        info.ArgumentList.Add(AdminUrl);

        using Process process = Process.Start(info)
                                ?? throw new InvalidOperationException("could not start " + RepoLayout.Catlogctl);
        string stdout = process.StandardOutput.ReadToEnd();
        string stderr = process.StandardError.ReadToEnd();
        process.WaitForExit(30_000);
        if (process.ExitCode != 0)
            throw new InvalidOperationException($"catlogctl issue failed ({process.ExitCode}):\n{stdout}\n{stderr}");

        string path = Path.Combine(outDir, "catlog-credential.json");
        CredentialLoadResult loaded = Credential.Load(path);
        if (!loaded.Ok)
            throw new InvalidOperationException($"the issued credential does not load: {loaded.Error}");

        var issued = new IssuedCredential(handle, path, loaded.Credential!);
        _issued.Add(issued);
        return issued;
    }

    /// <summary>Reads <c>GET /admin/stats</c>.</summary>
    /// <returns>The parsed response.</returns>
    public JsonDocument AdminStats()
    {
        using HttpResponseMessage response = _http.Send(new HttpRequestMessage(HttpMethod.Get, AdminUrl + "/admin/stats"));
        response.EnsureSuccessStatusCode();
        return JsonDocument.Parse(response.Content.ReadAsStringAsync().GetAwaiter().GetResult());
    }

    /// <summary>How many events the server has stored.</summary>
    /// <returns>The <c>events.total</c> counter.</returns>
    public long TotalEvents()
    {
        using JsonDocument stats = AdminStats();
        return stats.RootElement.GetProperty("events").GetProperty("total").GetInt64();
    }

    private async Task WaitForHealthAsync()
    {
        var deadline = DateTimeOffset.UtcNow + StartupTimeout;
        while (DateTimeOffset.UtcNow < deadline)
        {
            try
            {
                using HttpResponseMessage response = await _http.GetAsync(BaseUrl + "/healthz");
                if (response.StatusCode == HttpStatusCode.OK)
                    return;
            }
            catch (HttpRequestException)
            {
                // Not listening yet.
            }

            if (_process is { HasExited: true })
                throw new InvalidOperationException($"catlogd exited during startup with code {_process.ExitCode}");

            await Task.Delay(50);
        }

        throw new TimeoutException($"catlogd never answered {BaseUrl}/healthz");
    }

    private static int FreePort()
    {
        using var listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();
        int port = ((IPEndPoint)listener.LocalEndpoint).Port;
        listener.Stop();
        return port;
    }
}

/// <summary>Locates the repository and the binaries <c>make server-build</c> produces.</summary>
internal static class RepoLayout
{
    private static readonly Lazy<string> RootPath = new(FindRoot);

    /// <summary>The repository root.</summary>
    internal static string Root => RootPath.Value;

    /// <summary>The <c>catlogd</c> binary.</summary>
    internal static string Catlogd => Binary("catlogd");

    /// <summary>The <c>catlogctl</c> binary.</summary>
    internal static string Catlogctl => Binary("catlogctl");

    private static string Binary(string name)
    {
        string path = Path.Combine(Root, "server", "bin",
            RuntimeInformation.IsOSPlatform(OSPlatform.Windows) ? name + ".exe" : name);
        if (!File.Exists(path))
            throw new InvalidOperationException($"{path} is missing — run `make server-build` first.");
        return path;
    }

    private static string FindRoot()
    {
        var dir = new DirectoryInfo(AppContext.BaseDirectory);
        while (dir is not null)
        {
            // Three markers, not one: a lone common filename can match a parent directory by
            // accident. The previous marker was INITIAL_IMPL_PLAN.md, which lived in plans/ and
            // never at the root — see the note in catlog.lib.tests/TestEnv.cs.
            if (File.Exists(Path.Combine(dir.FullName, "Makefile"))
                && Directory.Exists(Path.Combine(dir.FullName, "server"))
                && Directory.Exists(Path.Combine(dir.FullName, "contracts")))
            {
                return dir.FullName;
            }

            dir = dir.Parent;
        }

        throw new InvalidOperationException(
            $"could not find the catlog repository root above {AppContext.BaseDirectory}");
    }
}
