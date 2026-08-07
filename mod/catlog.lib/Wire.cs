namespace MeowSci.Catlog.Lib;

/// <summary>
/// Every wire constant and limit from INITIAL_IMPL_PLAN §4.3–§4.5 (mirrored in
/// <c>docs/ingest-api.md</c>), in one place. The Go server enforces these; the mod mirrors
/// them so a batch is never built that the server would reject. Changing anything here is a
/// contract change and needs a line in <c>docs/DECISIONS.md</c>.
/// </summary>
public static class Wire
{
    // ----- Envelope -------------------------------------------------------------------

    /// <summary>Payload schema version carried by every launch-set event type (§4.2).</summary>
    public const int EnvelopeVersion = 1;

    // ----- Wire format & limits (§4.3) -------------------------------------------------

    /// <summary>Maximum compressed request body, in bytes. Over it the server answers <c>413 too_large</c>.</summary>
    public const int MaxCompressedBodyBytes = 1 * 1024 * 1024;

    /// <summary>Maximum decompressed NDJSON, in bytes. Over it the server answers <c>413 too_large</c>.</summary>
    public const int MaxDecompressedBytes = 8 * 1024 * 1024;

    /// <summary>Maximum events in one batch. Over it the server answers <c>413 too_large</c>.</summary>
    public const int MaxEventsPerBatch = 2000;

    /// <summary>Maximum length of a single NDJSON line, in bytes. Over it the server answers <c>400 malformed_batch</c>.</summary>
    public const int MaxEventLineBytes = 16 * 1024;

    /// <summary>Accepted proof <c>iat</c> skew, in seconds, either side of server time.</summary>
    public const int ClockSkewSeconds = 300;

    /// <summary>Maximum size of either JWS header value, in bytes (verification step 1).</summary>
    public const int MaxJwsBytes = 4 * 1024;

    // ----- Ingest HTTP (§4.4) -----------------------------------------------------------

    /// <summary>Path component of the ingest endpoint. The configured <c>ingest_url</c> ends with it.</summary>
    public const string IngestPath = "/v1/ingest";

    /// <summary>Request <c>Content-Type</c> for a batch.</summary>
    public const string ContentType = "application/x-ndjson";

    /// <summary>Request <c>Content-Encoding</c> for a batch (D18).</summary>
    public const string ContentEncoding = "br";

    /// <summary>Header carrying the compact license JWS.</summary>
    public const string LicenseHeader = "X-Catlog-License";

    /// <summary>Header carrying the compact per-batch proof JWS.</summary>
    public const string ProofHeader = "X-Catlog-Proof";

    /// <summary>HTTP method the proof's <c>htm</c> claim must name.</summary>
    public const string HttpMethod = "POST";

    // ----- JWS / JWK (§4.5) --------------------------------------------------------------

    /// <summary>The only accepted JWS algorithm. No <c>none</c>, no RSA.</summary>
    public const string Alg = "ES256";

    /// <summary>The only accepted EC curve.</summary>
    public const string Crv = "P-256";

    /// <summary>JWK key type for the client's signing key.</summary>
    public const string Kty = "EC";

    /// <summary><c>typ</c> of the server-signed license JWS.</summary>
    public const string LicenseTyp = "catlog-license+jwt";

    /// <summary><c>typ</c> of the client-signed per-batch proof JWS.</summary>
    public const string ProofTyp = "catlog-proof+jwt";

    /// <summary>Version claim the server requires on a license.</summary>
    public const int LicenseVersion = 1;

    /// <summary><c>format</c> value of the credential file (§4.6).</summary>
    public const int CredentialFormat = 1;

    // ----- Error codes (§4.9) -------------------------------------------------------------

    /// <summary>Every error code the server can return, as a stable set for switch statements.</summary>
    public static class Errors
    {
        /// <summary><c>bad_request</c>.</summary>
        public const string BadRequest = "bad_request";

        /// <summary><c>malformed_batch</c> — undecodable body, bad envelope, or unknown event type.</summary>
        public const string MalformedBatch = "malformed_batch";

        /// <summary><c>unsupported_encoding</c>.</summary>
        public const string UnsupportedEncoding = "unsupported_encoding";

        /// <summary><c>license_invalid</c>.</summary>
        public const string LicenseInvalid = "license_invalid";

        /// <summary><c>license_expired</c>.</summary>
        public const string LicenseExpired = "license_expired";

        /// <summary><c>license_revoked</c>.</summary>
        public const string LicenseRevoked = "license_revoked";

        /// <summary><c>proof_invalid</c>.</summary>
        public const string ProofInvalid = "proof_invalid";

        /// <summary><c>clock_skew</c> — the only 401 the mod can recover from.</summary>
        public const string ClockSkew = "clock_skew";

        /// <summary><c>banned</c>.</summary>
        public const string Banned = "banned";

        /// <summary><c>stream_fork</c> — seq conflict; the mod mints a new <c>sid</c>.</summary>
        public const string StreamFork = "stream_fork";

        /// <summary><c>rate_limited</c>.</summary>
        public const string RateLimited = "rate_limited";

        /// <summary><c>too_large</c> — the mod halves its batch event cap.</summary>
        public const string TooLarge = "too_large";

        /// <summary><c>not_found</c>.</summary>
        public const string NotFound = "not_found";

        /// <summary><c>internal</c>.</summary>
        public const string Internal = "internal";
    }

    // ----- Mod-side behaviour (§7.2, §4.5.3) ----------------------------------------------

    /// <summary>Passive telemetry sample rate, in Hz (D15). Enforced by drop-not-backfill.</summary>
    public const double DefaultSampleHz = 2.0;

    /// <summary>Passive telemetry aggregation window, in sim seconds (D15).</summary>
    public const double TelemetryWindowSeconds = 30.0;

    /// <summary>Detector debounce per (vehicle, event kind), in sim seconds (§7.2).</summary>
    public const double DetectorDebounceSeconds = 2.0;

    /// <summary>Atmosphere-boundary hysteresis band, as a fraction of the body's atmosphere height (§7.2).</summary>
    public const double AtmosphereHysteresis = 0.02;

    /// <summary>Margin above the atmosphere that a periapsis must clear to count as orbit achieved, in metres (§7.2).</summary>
    public const double OrbitAchievedMarginM = 1000.0;

    /// <summary>
    /// The <b>safety valve</b>: ship early when this many events are pending, regardless of age.
    /// </summary>
    /// <remarks>
    /// <para>
    /// This is deliberately <i>not</i> the normal reason to ship —
    /// <see cref="ShipAgeTriggerSeconds"/> is. The mod buffers into the outbox and a background
    /// worker pumps in bulk about once a minute; the count trigger exists only so an unusually
    /// busy minute (or a backlog draining after an outage) cannot grow an unbounded batch.
    /// </para>
    /// <para>
    /// 500 is chosen against a real minute of play. Passive <c>telemetry.window</c> events are one
    /// per active vehicle per 30 s, so a busy save with two dozen vehicles emits ~48 of them per
    /// minute, and the discrete events of an eventful launch add a few dozen more — call a busy
    /// minute ≤150 events. 500 is over three times that, so the age trigger stays the normal path.
    /// It is also exactly <see cref="DefaultBatchEventCap"/>, so when the valve does open there is
    /// precisely one full batch to send rather than a partial one.
    /// </para>
    /// <para>
    /// Headroom against the §4.3 limits at 500 events: 25% of the 2000-event cap; a measured
    /// 90.5 KiB Brotli body (8.8% of the 1 MiB cap) for worst-case incompressible
    /// <c>telemetry.window</c> lines; 0.31 MiB decompressed (3.9% of the 8 MiB cap). Against the
    /// token bucket (1 batch / 2 s, burst 5), one batch a minute is 3% of the sustained allowance.
    /// </para>
    /// </remarks>
    public const int ShipPendingTrigger = 500;

    /// <summary>
    /// The normal ship trigger: ship when the oldest pending event is at least this old, in
    /// seconds (§7.2). One minute — the mod is a bulk telemetry pump, not a live feed.
    /// </summary>
    public const double ShipAgeTriggerSeconds = 60.0;

    /// <summary>
    /// <b>The hard rate floor: the mod never issues two requests to the ingest endpoint less than
    /// this many seconds apart. Not configurable, by design.</b>
    /// </summary>
    /// <remarks>
    /// <para>
    /// <b>Threat model.</b> <c>catlog.toml</c> is a plain text file in the player's mod directory,
    /// so every knob in it is attacker-controlled by definition. Before this constant existed,
    /// <c>ship_interval_s = 1</c> turned a stock install into a firehose — one line, no tools, no
    /// rebuild. Someone who recompiles the assembly can of course do anything; that is out of scope
    /// and always will be. What is in scope is that the <i>easy</i> path is closed, so this floor
    /// lives in code, is a compile-time constant, and is reachable from no TOML key. Adding one
    /// would mean deliberately editing this line.
    /// </para>
    /// <para>
    /// <b>Where it is enforced.</b> Three places, on purpose, because clamping the config alone
    /// only closes the path someone thought of:
    /// </para>
    /// <list type="number">
    ///   <item><description>
    ///     <c>ModConfig.Normalize</c> clamps <c>ship_interval_s</c> up to this value, so the
    ///     configured cadence and the enforced cadence agree and the player sees one warning
    ///     rather than mysterious behaviour.
    ///   </description></item>
    ///   <item><description>
    ///     <c>BatchShipper.ShouldShip</c> reports "not due" inside the window, so neither the age
    ///     trigger nor the <see cref="ShipPendingTrigger"/> count trigger can open it early — a
    ///     10 000-event burst still ships one batch per window and buffers the rest.
    ///   </description></item>
    ///   <item><description>
    ///     <c>BatchShipper</c> refuses at the point of transmission, immediately before the POST.
    ///     That is the one that actually holds: it covers a directly-constructed
    ///     <c>ShipperOptions</c>, every <c>401</c>/<c>409</c>/<c>413</c>/<c>429</c>/<c>5xx</c>
    ///     recovery retry, and any caller written after this comment.
    ///   </description></item>
    /// </list>
    /// <para>
    /// <b>One exemption, by name:</b> <c>BatchShipper.FinalShip</c>, the single-attempt courtesy
    /// flush at game unload. It fires at most once per session, so abusing it costs a full quit
    /// and relaunch of KSA — far more than the 30 s it saves — and it is self-limiting in a way the
    /// in-session triggers are not. It is threaded through a private enum that no caller can reach,
    /// and it still stamps the window, so the next session's first ordinary batch waits out a full
    /// one. Nothing else is exempt, ever.
    /// </para>
    /// <para>
    /// <b>It is measured against <c>IShipperClock</c></b>, the seam that already existed so the
    /// retry ladder could be unit-tested without real waiting. The shipped mod never passes a
    /// clock, so it gets <c>SystemShipperClock</c> and a real 30 s floor; the unit tests and
    /// <c>catlog.sim</c> pass a virtual one. Nothing a player can edit reaches that parameter.
    /// </para>
    /// <para>
    /// <b>Thirty seconds</b> is one half of <see cref="ShipAgeTriggerSeconds"/>, so it never binds
    /// during ordinary play — the shipped cadence is already one batch a minute — and it is the
    /// same length as <see cref="TelemetryWindowSeconds"/>, so a floored batch is still at most one
    /// telemetry window behind. Against §4.3's per-credential token bucket (1 batch / 2 s, burst 5)
    /// a client at the floor spends 6.7% of its sustained allowance: the server-side bucket is the
    /// backstop for a hostile client, and this is the client-side promise that a stock install
    /// never approaches it.
    /// </para>
    /// </remarks>
    public const double MinShipIntervalSeconds = 30.0;

    /// <summary>
    /// Lower clamp for a configured ship interval, in seconds. It <b>is</b>
    /// <see cref="MinShipIntervalSeconds"/>: a configured cadence faster than the hard floor would
    /// be a lie the player could read in their own config file.
    /// </summary>
    public const double MinShipAgeTriggerSeconds = MinShipIntervalSeconds;

    /// <summary>Upper clamp for a configured ship interval, in seconds (one hour).</summary>
    public const double MaxShipAgeTriggerSeconds = 3600.0;

    /// <summary>Initial events-per-batch cap; halved on <c>413</c>, never below <see cref="MinBatchEventCap"/>.</summary>
    public const int DefaultBatchEventCap = 500;

    /// <summary>Floor for the <c>413</c> batch-cap halving ladder (§4.5.3).</summary>
    public const int MinBatchEventCap = 50;

    /// <summary>Base of the retry backoff ladder <c>1 s · 2ⁿ</c> with full jitter (§4.5.3).</summary>
    public const double BackoffBaseSeconds = 1.0;

    /// <summary>Cap of the retry backoff ladder, in seconds (§4.5.3).</summary>
    public const double BackoffCapSeconds = 300.0;

    /// <summary>Default local outbox size cap, in megabytes (§7.2).</summary>
    public const int DefaultOutboxCapMb = 50;

    // ----- shipper_state keys (§7.2 DDL) --------------------------------------------------

    /// <summary>Key names used in the outbox's <c>shipper_state</c> k/v table.</summary>
    public static class StateKeys
    {
        /// <summary>Current stream id (a ULID). One outbox instance epoch.</summary>
        public const string StreamId = "sid";

        /// <summary>Next sequence number to use, 1-based and strictly monotonic per (jkt, sid).</summary>
        public const string Seq = "seq";

        /// <summary>Body hash of the last accepted batch — the next proof's <c>ph</c>.</summary>
        public const string LastBh = "last_bh";

        /// <summary>
        /// The batch id (proof <c>jti</c>) minted for the batch currently in flight, and
        /// <see cref="PendingBh"/> the body it belongs to. Held across retries so a resend of an
        /// identical body reuses its batch id and lands on the server's §4.5.3 step-11 replay
        /// short-circuit instead of its step-12 stream check. Cleared once the batch is accepted.
        /// </summary>
        public const string PendingBatchId = "pending_batch_id";

        /// <summary>The body hash <see cref="PendingBatchId"/> was minted for.</summary>
        public const string PendingBh = "pending_bh";

        /// <summary>Server-clock offset in milliseconds, learned from the <c>Date</c> header.</summary>
        public const string ClockOffsetMs = "clock_offset_ms";

        /// <summary>
        /// Client unix milliseconds at which the last request to the ingest endpoint was issued —
        /// the <see cref="MinShipIntervalSeconds"/> floor's anchor.
        /// </summary>
        /// <remarks>
        /// Persisted rather than held in memory so that quitting and reloading the game is not a
        /// way around the floor: a fresh session reads the stamp its predecessor left and waits out
        /// the remainder of the window before its first request. Deleting the outbox resets it, but
        /// deleting the outbox also deletes the events, so there is nothing left to flood with.
        /// </remarks>
        public const string LastRequestMs = "last_request_ms";
    }
}
