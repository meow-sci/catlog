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

    /// <summary>Ship as soon as this many events are pending in the outbox (§7.2).</summary>
    public const int ShipPendingTrigger = 64;

    /// <summary>Ship when the oldest pending event is at least this old, in seconds (§7.2).</summary>
    public const double ShipAgeTriggerSeconds = 15.0;

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

        /// <summary>Server-clock offset in milliseconds, learned from the <c>Date</c> header.</summary>
        public const string ClockOffsetMs = "clock_offset_ms";
    }
}
