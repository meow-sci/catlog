using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Sim;

namespace MeowSci.Catlog.LoadGen;

/// <summary>One synthetic identity, as <c>mockidp</c>'s <c>POST /generate</c> reports it.</summary>
/// <param name="IdP">discord, google or github.</param>
/// <param name="Sub">The provider-stable subject.</param>
/// <param name="AuthorizePath">The provider's authorize route on mockidp.</param>
/// <param name="TooNew">
/// True when the account was deliberately minted younger than the ≥30-day gate, so catlogd is
/// expected to <b>refuse</b> the login with <c>account_too_new</c>.
/// </param>
internal sealed record Subject(string IdP, string Sub, string AuthorizePath, bool TooNew);

/// <summary>The outcome of trying to provision one player.</summary>
/// <param name="Ok">True when a usable credential came back.</param>
/// <param name="Player">The provisioned player, or null.</param>
/// <param name="ErrorCode">The §4.9 code the server answered with, or an empty string.</param>
/// <param name="Detail">A human-readable reason, for the report.</param>
internal sealed record ProvisionResult(bool Ok, PlayerAccount? Player, string ErrorCode, string Detail);

/// <summary>A provisioned player: who they are and what they can ship with.</summary>
internal sealed class PlayerAccount : IDisposable
{
    /// <summary>Creates the record.</summary>
    /// <param name="index">The player's index in the run.</param>
    /// <param name="handle">The claimed handle.</param>
    /// <param name="idp">Which identity provider signed them in, or <c>dev</c> under <c>--auth admin</c>.</param>
    /// <param name="credential">The loaded credential.</param>
    /// <param name="session">The website session, when there is one.</param>
    internal PlayerAccount(int index, string handle, string idp, Credential credential, CookieJar? session)
    {
        Index = index;
        Handle = handle;
        IdP = idp;
        Credential = credential;
        Session = session;
    }

    /// <summary>The player's index in the run.</summary>
    internal int Index { get; }

    /// <summary>The claimed handle.</summary>
    internal string Handle { get; }

    /// <summary>Which identity provider signed them in.</summary>
    internal string IdP { get; }

    /// <summary>The credential the shipper signs with. Replaced by a reissue.</summary>
    internal Credential Credential { get; set; }

    /// <summary>The website session, or null under <c>--auth admin</c>.</summary>
    internal CookieJar? Session { get; }

    /// <summary>Disposes the credential's signing key.</summary>
    public void Dispose() => Credential.Dispose();
}

/// <summary>
/// A cookie jar small enough to read in one sitting.
/// </summary>
/// <remarks>
/// <see cref="CookieContainer"/> would work, but it lives on the handler, and a run with hundreds
/// of players wants <b>one</b> shared connection pool rather than one per player. Turning
/// <c>UseCookies</c> off and carrying the two cookies catlogd sets — the short-lived OAuth
/// <c>state</c> and the session — costs thirty lines and keeps the pool shared.
/// </remarks>
internal sealed class CookieJar
{
    private readonly Dictionary<string, string> _cookies = new(StringComparer.Ordinal);

    /// <summary>Reads every <c>Set-Cookie</c> on a response, honouring deletions.</summary>
    /// <param name="response">The response.</param>
    internal void Absorb(HttpResponseMessage response)
    {
        if (!response.Headers.TryGetValues("Set-Cookie", out IEnumerable<string>? headers))
            return;

        foreach (string header in headers)
        {
            string[] parts = header.Split(';');
            int equals = parts[0].IndexOf('=', StringComparison.Ordinal);
            if (equals <= 0)
                continue;

            string name = parts[0][..equals].Trim();
            string value = parts[0][(equals + 1)..].Trim();

            bool deleted = value.Length == 0;
            foreach (string attribute in parts)
            {
                string trimmed = attribute.Trim();
                if (trimmed.StartsWith("Max-Age=0", StringComparison.OrdinalIgnoreCase)
                    || trimmed.Equals("Max-Age=-1", StringComparison.OrdinalIgnoreCase))
                {
                    deleted = true;
                }
            }

            if (deleted)
                _cookies.Remove(name);
            else
                _cookies[name] = value;
        }
    }

    /// <summary>The <c>Cookie</c> request header, or an empty string when there is nothing to send.</summary>
    /// <returns>The header value.</returns>
    internal string Header()
    {
        if (_cookies.Count == 0)
            return string.Empty;
        var parts = new List<string>(_cookies.Count);
        foreach ((string name, string value) in _cookies)
            parts.Add(name + "=" + value);
        return string.Join("; ", parts);
    }

    /// <summary>True when the jar holds anything that looks like a session.</summary>
    internal bool HasSession
    {
        get
        {
            foreach (string name in _cookies.Keys)
            {
                if (name.Contains("sess", StringComparison.OrdinalIgnoreCase))
                    return true;
            }

            return false;
        }
    }
}

/// <summary>
/// Provisions players: mints subjects at <c>mockidp</c>, drives catlogd's real OAuth callback,
/// claims a handle with a client-generated key, and exercises the moderation endpoints.
/// </summary>
/// <remarks>
/// <para>
/// <b>Every step below is the real one.</b> catlogd performs its own code exchange against
/// mockidp, verifies the Google <c>id_token</c> against the JWKS mockidp publishes, derives
/// <c>user_key = HMAC(pepper, "&lt;idp&gt;:&lt;sub&gt;")</c>, applies the account-age gate, sets a
/// signed session cookie, enforces the handle rules and the per-account quotas, and signs the
/// license. Nothing here shortcuts any of it — the only thing that changed to make a load run
/// possible is that mockidp can now name more than five people.
/// </para>
/// <para>
/// The private key never leaves this process, exactly as in the browser wizard: the harness
/// generates a P-256 pair, sends the public JWK, and assembles the §4.6 credential file itself.
/// </para>
/// </remarks>
internal sealed class Provisioner
{
    private readonly LoadOptions _options;
    private readonly HttpClient _http;

    /// <summary>Creates a provisioner.</summary>
    /// <param name="options">The run's options.</param>
    /// <param name="handler">The shared transport; cookies are handled by <see cref="CookieJar"/>.</param>
    internal Provisioner(LoadOptions options, HttpMessageHandler handler)
    {
        _options = options;
        _http = new HttpClient(handler, disposeHandler: false) { Timeout = TimeSpan.FromSeconds(30) };
    }

    /// <summary>
    /// Asks mockidp for <paramref name="count"/> synthetic subjects.
    /// </summary>
    /// <param name="count">How many.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The subjects, in the order mockidp minted them.</returns>
    /// <exception cref="SimException">mockidp is unreachable or refused.</exception>
    internal async Task<IReadOnlyList<Subject>> MintSubjectsAsync(int count, CancellationToken ct)
    {
        int newEvery = _options.TooNewPercent <= 0 ? 0 : Math.Max(2, (int)Math.Round(100.0 / _options.TooNewPercent));
        string body = string.Create(CultureInfo.InvariantCulture,
            $$"""
              {"count":{{count}},"idp":"{{_options.IdP}}","seed":"{{_options.Namespace}}",
               "prefix":"loadgen","age_days":420,"new_every":{{newEvery}}}
              """);

        using var request = new HttpRequestMessage(HttpMethod.Post, _options.MockIdp + "/generate")
        {
            Content = new StringContent(body, Encoding.UTF8, "application/json"),
        };
        using HttpResponseMessage response = await _http.SendAsync(request, ct).ConfigureAwait(false);
        string text = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new SimException(
                $"mockidp POST /generate returned {(int)response.StatusCode}: {Truncate(text)}. "
                + "Is the mockidp on this checkout running? `make dev` starts it.");
        }

        using JsonDocument document = JsonDocument.Parse(text);
        var subjects = new List<Subject>(count);
        foreach (JsonElement row in document.RootElement.GetProperty("accounts").EnumerateArray())
        {
            subjects.Add(new Subject(
                Str(row, "idp"), Str(row, "sub"), Str(row, "authorize_path"),
                row.TryGetProperty("too_new", out JsonElement tooNew) && tooNew.GetBoolean()));
        }

        return subjects;
    }

    /// <summary>
    /// Runs the whole real player path for one subject: sign in, claim a handle, take delivery of
    /// a license, and assemble the §4.6 credential.
    /// </summary>
    /// <param name="index">The player's index.</param>
    /// <param name="subject">The subject to sign in as.</param>
    /// <param name="handle">The handle to claim.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What happened.</returns>
    internal async Task<ProvisionResult> ProvisionOAuthAsync(
        int index, Subject subject, string handle, CancellationToken ct)
    {
        var jar = new CookieJar();

        // 1. catlogd starts the flow and sets its OAuth state cookie.
        using HttpResponseMessage start = await SendAsync(
            HttpMethod.Get, $"{_options.Server}/auth/{subject.IdP}/start", jar, null, ct).ConfigureAwait(false);
        if (start.StatusCode != HttpStatusCode.Found || start.Headers.Location is null)
        {
            return Fail("start_failed",
                $"GET /auth/{subject.IdP}/start answered {(int)start.StatusCode}, expected a 302 to mockidp");
        }

        // 2. The consent page, answered directly. `?user=` is what a button click adds; the
        //    harness adds it itself because a generated subject has no button, by design.
        var authorize = new UriBuilder(start.Headers.Location);
        authorize.Query = authorize.Query.TrimStart('?') + "&user=" + Uri.EscapeDataString(subject.Sub);
        using HttpResponseMessage consent = await SendAsync(
            HttpMethod.Get, authorize.Uri.ToString(), jar, null, ct).ConfigureAwait(false);
        if (consent.StatusCode != HttpStatusCode.Found || consent.Headers.Location is null)
        {
            string detail = await consent.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            return Fail("authorize_failed",
                $"mockidp authorize answered {(int)consent.StatusCode}: {Truncate(detail)}");
        }

        // 3. catlogd's callback: real code exchange, real id_token verification for Google, real
        //    user_key derivation, real account-age gate, real session cookie.
        Uri callback = consent.Headers.Location.IsAbsoluteUri
            ? consent.Headers.Location
            : new Uri(new Uri(_options.Server), consent.Headers.Location);
        using HttpResponseMessage landed = await SendAsync(
            HttpMethod.Get, callback.ToString(), jar, null, ct).ConfigureAwait(false);

        if (landed.StatusCode != HttpStatusCode.Found)
        {
            string text = await landed.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            return Fail(ErrorCode(text), $"the callback answered {(int)landed.StatusCode}: {Truncate(text)}");
        }
        if (!jar.HasSession)
            return Fail("no_session", "the callback redirected but set no session cookie");

        // 4. The key pair is generated here and the private half never leaves — the same promise
        //    the browser wizard makes (§4.6, §5.7).
        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        string publicJwk = Jwk.PublicJwkJson(key);
        string claim = $"{{\"handle\":\"{handle}\",\"jwk\":{publicJwk}}}";

        using HttpResponseMessage issued = await SendAsync(
            HttpMethod.Post, _options.Server + "/api/handles", jar, claim, ct).ConfigureAwait(false);
        string issuedBody = await issued.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if (!issued.IsSuccessStatusCode)
            return Fail(ErrorCode(issuedBody), $"POST /api/handles answered {(int)issued.StatusCode}: {Truncate(issuedBody)}");

        CredentialLoadResult loaded = Assemble(handle, issuedBody, key);
        return loaded.Ok
            ? new ProvisionResult(true, new PlayerAccount(index, handle, subject.IdP, loaded.Credential!, jar), string.Empty, string.Empty)
            : Fail("credential_unusable", loaded.Error);
    }

    /// <summary>
    /// The fast path: <c>POST /admin/issue</c>. Skips the entire identity stack, which is exactly
    /// what makes it useful as a control when the question is about ingest alone.
    /// </summary>
    /// <param name="index">The player's index.</param>
    /// <param name="handle">The handle to issue for.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>What happened.</returns>
    internal async Task<ProvisionResult> ProvisionAdminAsync(int index, string handle, CancellationToken ct)
    {
        using HttpResponseMessage response = await SendAsync(
            HttpMethod.Post, _options.Admin + "/admin/issue", null, $"{{\"handle\":\"{handle}\"}}", ct)
            .ConfigureAwait(false);
        string body = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
            return Fail(ErrorCode(body), $"POST /admin/issue answered {(int)response.StatusCode}: {Truncate(body)}");

        using JsonDocument document = JsonDocument.Parse(body);
        string json = CredentialJson(
            handle, Str(document.RootElement, "license"), Str(document.RootElement, "private_key_pem"));
        CredentialLoadResult loaded = Credential.Parse(json);
        return loaded.Ok
            ? new ProvisionResult(true, new PlayerAccount(index, handle, "dev", loaded.Credential!, null), string.Empty, string.Empty)
            : Fail("credential_unusable", loaded.Error);
    }

    /// <summary>
    /// Reissues a player's license against a brand-new key pair, which revokes the credential it
    /// replaces (D16) — the path a player who lost their credential file takes.
    /// </summary>
    /// <param name="player">The player.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The new credential, or null with the reason.</returns>
    internal async Task<(Credential? Credential, string Error)> ReissueAsync(PlayerAccount player, CancellationToken ct)
    {
        if (player.Session is null)
            return (null, "reissue needs a website session; run with --auth oauth");

        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        string body = $"{{\"jwk\":{Jwk.PublicJwkJson(key)}}}";
        using HttpResponseMessage response = await SendAsync(
            HttpMethod.Post, $"{_options.Server}/api/handles/{Uri.EscapeDataString(player.Handle)}/reissue",
            player.Session, body, ct).ConfigureAwait(false);

        string text = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
            return (null, $"HTTP {(int)response.StatusCode} {ErrorCode(text)}");

        CredentialLoadResult loaded = Assemble(player.Handle, text, key);
        return loaded.Ok ? (loaded.Credential, string.Empty) : (null, loaded.Error);
    }

    /// <summary>Revokes every live credential on a player's handle.</summary>
    /// <param name="player">The player.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The error, or an empty string on success.</returns>
    internal async Task<string> RevokeAsync(PlayerAccount player, CancellationToken ct)
        => await PostAsync($"{_options.Server}/api/handles/{Uri.EscapeDataString(player.Handle)}/revoke",
            player.Session, "{}", ct).ConfigureAwait(false);

    /// <summary>Deletes a player's account and every event attached to it.</summary>
    /// <param name="player">The player.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The error, or an empty string on success.</returns>
    internal async Task<string> DeleteAsync(PlayerAccount player, CancellationToken ct)
        => await PostAsync(_options.Server + "/api/me/delete", player.Session, "{}", ct).ConfigureAwait(false);

    /// <summary>Bans a player through the loopback admin API.</summary>
    /// <param name="handle">The handle to ban.</param>
    /// <param name="purge">Whether to purge the account's data as well.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The error, or an empty string on success.</returns>
    internal async Task<string> AdminBanAsync(string handle, bool purge, CancellationToken ct)
        => await PostAsync(_options.Admin + "/admin/ban", null,
            $"{{\"handle\":\"{handle}\",\"reason\":\"loadgen moderation exercise\",\"purge\":{(purge ? "true" : "false")}}}",
            ct).ConfigureAwait(false);

    /// <summary>
    /// Re-sends one accepted batch byte for byte, to exercise §4.5.3 step 11's replay
    /// short-circuit.
    /// </summary>
    /// <param name="captured">The request to repeat.</param>
    /// <param name="ct">Cancellation.</param>
    /// <returns>The HTTP status and the response body.</returns>
    internal async Task<(int Status, string Body)> ReplayAsync(CapturedRequest captured, CancellationToken ct)
    {
        // The probe fires immediately after a player has finished shipping, so its credential's
        // token bucket is usually empty. A 429 here would say nothing about idempotency, so the
        // bucket is waited out rather than reported as the answer.
        for (int attempt = 0; ; attempt++)
        {
            using var request = new HttpRequestMessage(HttpMethod.Post, captured.Url);
            var content = new ByteArrayContent(captured.Body);
            content.Headers.ContentType = new MediaTypeHeaderValue("application/x-ndjson");
            content.Headers.ContentEncoding.Add("br");
            request.Content = content;
            request.Headers.TryAddWithoutValidation("X-Catlog-License", captured.License);
            request.Headers.TryAddWithoutValidation("X-Catlog-Proof", captured.Proof);

            using HttpResponseMessage response = await _http.SendAsync(request, ct).ConfigureAwait(false);
            string body = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);

            if ((int)response.StatusCode != 429 || attempt >= 8)
                return ((int)response.StatusCode, body);

            TimeSpan wait = response.Headers.RetryAfter?.Delta ?? TimeSpan.FromSeconds(2);
            await Task.Delay(wait + TimeSpan.FromMilliseconds(250), ct).ConfigureAwait(false);
        }
    }

    /// <summary>Checks that every process the run needs is up, before anything is provisioned.</summary>
    /// <param name="ct">Cancellation.</param>
    /// <returns>A list of problems; empty when everything answered.</returns>
    internal async Task<IReadOnlyList<string>> PreflightAsync(CancellationToken ct)
    {
        var problems = new List<string>();
        await Probe(_options.Server + "/healthz", "catlogd", "make dev").ConfigureAwait(false);
        await Probe(_options.Admin + "/admin/healthz", "the catlogd admin mux", "make dev").ConfigureAwait(false);
        if (_options.Auth == AuthMode.OAuth)
            await Probe(_options.MockIdp + "/healthz", "mockidp", "make dev").ConfigureAwait(false);
        return problems;

        async Task Probe(string url, string what, string fix)
        {
            try
            {
                using HttpResponseMessage response = await _http.GetAsync(url, ct).ConfigureAwait(false);
                if (!response.IsSuccessStatusCode)
                    problems.Add($"{what} answered HTTP {(int)response.StatusCode} at {url}");
            }
            catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException)
            {
                problems.Add($"{what} is unreachable at {url} ({ex.Message}) — start it with: {fix}");
            }
        }
    }

    // --- plumbing ---------------------------------------------------------------------

    private async Task<string> PostAsync(string url, CookieJar? jar, string body, CancellationToken ct)
    {
        using HttpResponseMessage response = await SendAsync(HttpMethod.Post, url, jar, body, ct).ConfigureAwait(false);
        if (response.IsSuccessStatusCode)
            return string.Empty;
        string text = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        return $"HTTP {(int)response.StatusCode} {ErrorCode(text)}: {Truncate(text)}";
    }

    private async Task<HttpResponseMessage> SendAsync(
        HttpMethod method, string url, CookieJar? jar, string? body, CancellationToken ct)
    {
        using var request = new HttpRequestMessage(method, url);
        if (body is not null)
            request.Content = new StringContent(body, Encoding.UTF8, "application/json");

        // §4.9 codes rather than the HTML login-failure page, which is what makes
        // `account_too_new` a value the harness can assert on.
        request.Headers.TryAddWithoutValidation("Accept", "application/json");
        // The §4.5.4 CSRF protection lets a request with neither header through; saying
        // same-origin explicitly means the harness exercises the path a browser takes.
        request.Headers.TryAddWithoutValidation("Sec-Fetch-Site", "same-origin");

        if (jar is not null)
        {
            string cookies = jar.Header();
            if (cookies.Length > 0)
                request.Headers.TryAddWithoutValidation("Cookie", cookies);
        }

        HttpResponseMessage response = await _http.SendAsync(request, ct).ConfigureAwait(false);
        jar?.Absorb(response);
        return response;
    }

    private static CredentialLoadResult Assemble(string handle, string licenseBody, ECDsa key)
    {
        using JsonDocument document = JsonDocument.Parse(licenseBody);
        return Credential.Parse(CredentialJson(
            handle, Str(document.RootElement, "license"), key.ExportPkcs8PrivateKeyPem()));
    }

    private static string CredentialJson(string handle, string license, string privateKeyPem)
        => JsonSerializer.Serialize(new Dictionary<string, object>(StringComparer.Ordinal)
        {
            ["format"] = 1,
            ["handle"] = handle,
            ["license"] = license,
            ["private_key_pem"] = privateKeyPem,
        });

    private static ProvisionResult Fail(string code, string detail) => new(false, null, code, detail);

    /// <summary>Pulls the §4.9 <c>error</c> code out of a response body, or the DOM contract's.</summary>
    private static string ErrorCode(string body)
    {
        try
        {
            using JsonDocument document = JsonDocument.Parse(body);
            if (document.RootElement.TryGetProperty("error", out JsonElement error) && error.ValueKind == JsonValueKind.String)
                return error.GetString() ?? "unknown";
        }
        catch (JsonException)
        {
            // Not JSON: the browser-facing failure page, whose contract is `data-error="<code>"`.
            const string marker = "data-error=\"";
            int at = body.IndexOf(marker, StringComparison.Ordinal);
            if (at >= 0)
            {
                int end = body.IndexOf('"', at + marker.Length);
                if (end > at)
                    return body[(at + marker.Length)..end];
            }
        }

        return "unknown";
    }

    private static string Str(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value) && value.ValueKind == JsonValueKind.String
            ? value.GetString() ?? string.Empty
            : string.Empty;

    private static string Truncate(string value)
        => value.Length <= 200 ? value.Replace('\n', ' ') : value[..200].Replace('\n', ' ');
}
