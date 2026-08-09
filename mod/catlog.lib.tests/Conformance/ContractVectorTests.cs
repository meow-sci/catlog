using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Ship;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Conformance;

/// <summary>
/// §4.10: cross-language conformance against <c>contracts/testdata/</c> — the
/// vectors that guarantee mod↔server interop without KSA.
/// </summary>
/// <remarks>
/// <b>TODO(WP2)</b>: these vectors are produced by <c>catlogctl testvectors generate
/// contracts/testdata</c>, which lands in WP2. Until the directory has content every test here
/// skips with an explanatory message rather than failing — they are written and wired now so that
/// switching them on is a matter of running the generator, not of writing tests.
/// <para>
/// Signatures are randomized in both runtimes (ECDSA uses a per-signature nonce), so these verify
/// rather than byte-compare. The two things that <i>are</i> byte-comparable — the body hash and the
/// RFC 7638 thumbprint — are checked exactly.
/// </para>
/// </remarks>
public sealed class ContractVectorTests
{
    // ----- license --------------------------------------------------------------------

    [ContractVectorFact]
    public void GoProducedLicense_VerifiesAgainstTheServerJwks()
    {
        string root = RequireVectors();
        string license = ReadText(root, "license", "license-valid.jws");
        using ECDsa serverKey = ServerKeyFromJwks(root);

        Assert.True(Jws.TryVerify(license, serverKey, out string error), $"license-valid.jws must verify: {error}");
    }

    [ContractVectorFact]
    public void GoProducedLicense_HasTheContractClaims()
    {
        string root = RequireVectors();
        string license = ReadText(root, "license", "license-valid.jws");

        Assert.True(Credential.TryReadLicenseClaims(license, out LicenseClaims? claims, out string error), error);
        Assert.Equal(Wire.LicenseVersion, claims!.Version);
        Assert.NotEmpty(claims.Issuer);
        Assert.NotEmpty(claims.Handle);
        Assert.NotEmpty(claims.Jkt);
        Assert.True(claims.ExpiresAt > claims.IssuedAt, "exp must be after iat");

        using JsonDocument expected = JsonDocument.Parse(ReadText(root, "license", "license-claims.json"));
        JsonElement root0 = expected.RootElement;
        Assert.Equal(root0.GetProperty("iss").GetString(), claims.Issuer);
        Assert.Equal(root0.GetProperty("handle").GetString(), claims.Handle);
        Assert.Equal(root0.GetProperty("sub").GetString(), claims.Subject);
        Assert.Equal(root0.GetProperty("jti").GetString(), claims.JwtId);
        Assert.Equal(
            root0.GetProperty("cnf").GetProperty("jkt").GetString(), claims.Jkt);
    }

    [ContractVectorFact]
    public void ExpiredLicense_VerifiesButHasAPastExpiry()
    {
        string root = RequireVectors();
        string license = ReadText(root, "license", "license-expired.jws");
        using ECDsa serverKey = ServerKeyFromJwks(root);

        Assert.True(Jws.Verify(license, serverKey), "the signature is still valid; only exp has passed");
        Assert.True(Credential.TryReadLicenseClaims(license, out LicenseClaims? claims, out _));
        Assert.True(claims!.IsExpired(DateTimeOffset.UtcNow), "license-expired.jws must be expired now");
    }

    /// <summary>The header must carry the ES256 allow-list value and a key id the server can find.</summary>
    [ContractVectorFact]
    public void LicenseHeader_IsEs256WithAKid()
    {
        string root = RequireVectors();

        using JsonDocument? header = Jws.DecodeHeaderUnverified(ReadText(root, "license", "license-valid.jws"));

        Assert.NotNull(header);
        Assert.Equal(Wire.Alg, header!.RootElement.GetProperty("alg").GetString());
        Assert.Equal(Wire.LicenseTyp, header.RootElement.GetProperty("typ").GetString());
        Assert.NotEmpty(header.RootElement.GetProperty("kid").GetString()!);
    }

    // ----- thumbprint -----------------------------------------------------------------

    /// <summary>
    /// Reproduces <c>keys/client.jkt.txt</c> from <c>keys/client-pub.jwk.json</c>. If the C# and Go
    /// RFC 7638 canonicalizations ever disagree, every batch is rejected <c>401 proof_invalid</c>,
    /// so this is the single highest-value cross-language check in the suite.
    /// </summary>
    [ContractVectorFact]
    public void Jkt_ReproducesTheGoComputedThumbprint()
    {
        string root = RequireVectors();
        string jwk = ReadText(root, "keys", "client-pub.jwk.json");
        string expected = ReadText(root, "keys", "client.jkt.txt").Trim();

        Assert.True(Jwk.TryThumbprintOfEcJwk(jwk, out string thumbprint, out string error), error);
        Assert.Equal(expected, thumbprint);
    }

    [ContractVectorFact]
    public void ClientPrivateKeyPem_ProducesTheSameThumbprint()
    {
        string root = RequireVectors();
        string expected = ReadText(root, "keys", "client.jkt.txt").Trim();
        using ECDsa key = ECDsa.Create();
        key.ImportFromPem(ReadText(root, "keys", "client-p256.pem"));

        Assert.Equal(expected, Jwk.Thumbprint(key));
    }

    // ----- batch ----------------------------------------------------------------------

    /// <summary>Reproduces <c>batches/batch-001.bh.txt</c> from the compressed body as sent.</summary>
    [ContractVectorFact]
    public void Bh_ReproducesTheGoComputedBodyHash()
    {
        string root = RequireVectors();
        byte[] compressed = ReadBytes(root, "batches", "batch-001.br");
        string expected = ReadText(root, "batches", "batch-001.bh.txt").Trim();

        Assert.Equal(expected, Bytes.Sha256Base64Url(compressed));
    }

    [ContractVectorFact]
    public void Batch001_DecompressesToTheNdjsonVector()
    {
        string root = RequireVectors();
        byte[] compressed = ReadBytes(root, "batches", "batch-001.br");
        byte[] expected = ReadBytes(root, "batches", "batch-001.ndjson");

        Assert.Equal(expected, BrotliCodec.Decompress(compressed));
    }

    [ContractVectorFact]
    public void Batch001_IsAllKnownEnvelopeTypes()
    {
        string root = RequireVectors();

        string[] lines = BatchLines(root);
        Assert.NotEmpty(lines);
        foreach (string line in lines)
        {
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement envelope = document.RootElement;
            string type = envelope.GetProperty("type").GetString()!;
            Assert.True(EventTypes.IsKnown(type), $"unknown event type in the vector batch: {type}");
            Assert.True(Ids.IsUlid(envelope.GetProperty("id").GetString()));
            Assert.True(envelope.TryGetProperty("flight", out JsonElement flight), "the flight key is always present");
            Assert.True(
                flight.ValueKind is JsonValueKind.Null ||
                (flight.ValueKind is JsonValueKind.String && Ids.IsUlid(flight.GetString())),
                $"{type}: flight must be null or a ULID");
            Assert.NotEmpty(envelope.GetProperty("session").GetString()!);
            Assert.True(Ids.IsHash16(envelope.GetProperty("career").GetString()), "career is 16 Crockford chars");
            Assert.Equal(JsonValueKind.Object, envelope.GetProperty("payload").ValueKind);
            Assert.True(Encoding.UTF8.GetByteCount(line) <= Wire.MaxEventLineBytes);
        }
    }

    /// <summary>
    /// Every line's <c>ver</c> must equal what this build stamps for that type.
    /// </summary>
    /// <remarks>
    /// This is the drift the conformance layer exists to catch. The vectors are generated with a
    /// hard-coded <c>ver</c> per line, so a type that bumps on one side and not in the fixture
    /// leaves <c>contracts/testdata</c> pinning a payload shape nobody emits any more — which is
    /// worse than no vector, because both suites keep passing against it. The Go suite asserts the
    /// mirror of this against <c>projector.currentVer</c>.
    /// </remarks>
    [ContractVectorFact]
    public void Batch001_StampsTheRegistrysCurrentVersion()
    {
        string root = RequireVectors();

        foreach (string line in BatchLines(root))
        {
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement envelope = document.RootElement;
            string type = envelope.GetProperty("type").GetString()!;

            Assert.Equal(EventTypes.VersionOf(type), envelope.GetProperty("ver").GetInt32());
        }
    }

    /// <summary>
    /// The batch must exercise every registered type, so coverage cannot narrow silently.
    /// </summary>
    /// <remarks>
    /// A type that leaves the vectors leaves the cross-language contract with it: nothing else in
    /// either suite compares a Go-produced payload of that type against the C# record. Adding a
    /// type to <see cref="EventTypes"/> therefore fails here until the generator emits a line for
    /// it, which is the same "ship the registry together" rule the server's unknown-type rejection
    /// enforces on the wire.
    /// </remarks>
    [ContractVectorFact]
    public void Batch001_CoversEveryRegisteredType()
    {
        string root = RequireVectors();

        var covered = new HashSet<string>(StringComparer.Ordinal);
        foreach (string line in BatchLines(root))
        {
            using JsonDocument document = JsonDocument.Parse(line);
            covered.Add(document.RootElement.GetProperty("type").GetString()!);
        }

        string[] missing = EventTypes.All.Where(t => !covered.Contains(t)).OrderBy(t => t, StringComparer.Ordinal).ToArray();
        Assert.True(missing.Length == 0, $"no vector line covers: {string.Join(", ", missing)}");
    }

    /// <summary>
    /// Every payload must survive a round trip through its C# record with no key gained or lost.
    /// </summary>
    /// <remarks>
    /// The strongest check in this file, and the only one that looks inside <c>payload</c>. A key
    /// the record does not know is dropped on the way out; a key the record writes that the wire
    /// does not carry is gained. Both are silent in every other test, and both are how the two
    /// implementations drift apart — most sharply on the omit-don't-zero optionals, where the
    /// vectors deliberately carry the same field present on one line and absent on another
    /// (<c>telemetry.window</c>'s <c>peak_g</c> / <c>max_q_pa</c> / <c>radar_alt_m</c>, and the
    /// wire-v2 <c>lat</c> / <c>lon</c>). Key *order* is not compared: it is not a contract, since
    /// the proof's <c>bh</c> hashes the bytes the mod actually produced.
    /// </remarks>
    [ContractVectorFact]
    public void Batch001_PayloadsRoundTripThroughTheirRecords()
    {
        string root = RequireVectors();

        foreach (string line in BatchLines(root))
        {
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement envelope = document.RootElement;
            string type = envelope.GetProperty("type").GetString()!;
            string payload = envelope.GetProperty("payload").GetRawText();

            Type record = PayloadTypes[type];
            object? decoded = JsonSerializer.Deserialize(payload, record, CatlogJson.Options);
            Assert.NotNull(decoded);
            string reencoded = JsonSerializer.Serialize(decoded, record, CatlogJson.Options);

            using JsonDocument before = JsonDocument.Parse(payload);
            using JsonDocument after = JsonDocument.Parse(reencoded);
            AssertJsonEqual(before.RootElement, after.RootElement, type);
        }
    }

    /// <summary>
    /// The wire type each <c>type</c> decodes into, mirroring <c>stats.decodePayload</c>.
    /// </summary>
    private static readonly IReadOnlyDictionary<string, Type> PayloadTypes = new Dictionary<string, Type>(StringComparer.Ordinal)
    {
        [EventTypes.SessionStarted] = typeof(SessionStartedPayload),
        [EventTypes.FlightStarted] = typeof(FlightStartedPayload),
        [EventTypes.FlightEnded] = typeof(FlightEndedPayload),
        [EventTypes.FlightFlagged] = typeof(FlightFlaggedPayload),
        [EventTypes.VehicleSituation] = typeof(VehicleSituationPayload),
        [EventTypes.VehicleAtmosphere] = typeof(VehicleAtmospherePayload),
        [EventTypes.VehicleOrbit] = typeof(VehicleOrbitPayload),
        [EventTypes.VehicleSoi] = typeof(VehicleSoiPayload),
        [EventTypes.VehicleRud] = typeof(VehicleRudPayload),
        [EventTypes.VehicleImpact] = typeof(VehicleImpactPayload),
        [EventTypes.VehicleLanded] = typeof(VehicleLandedPayload),
        [EventTypes.VehicleStaging] = typeof(VehicleStagingPayload),
        [EventTypes.VehicleDocked] = typeof(VehicleDockPayload),
        [EventTypes.VehicleUndocked] = typeof(VehicleDockPayload),
        [EventTypes.EngineIgnition] = typeof(EnginePayload),
        [EventTypes.EngineShutdown] = typeof(EnginePayload),
        [EventTypes.EngineFlameout] = typeof(EnginePayload),
        [EventTypes.KittenEvaStart] = typeof(KittenEvaStartPayload),
        [EventTypes.KittenEvaEnd] = typeof(KittenEvaEndPayload),
        [EventTypes.KittenTumble] = typeof(KittenTumblePayload),
        [EventTypes.KittenKia] = typeof(KittenKiaPayload),
        [EventTypes.RosterSnapshot] = typeof(RosterSnapshotPayload),
        [EventTypes.TelemetryWindow] = typeof(TelemetryWindowPayload),
    };

    /// <summary>Structural JSON equality: key sets, values, array order. Key order is ignored.</summary>
    /// <param name="expected">The wire element.</param>
    /// <param name="actual">The re-serialized element.</param>
    /// <param name="path">Where in the payload this comparison is, for the failure message.</param>
    private static void AssertJsonEqual(JsonElement expected, JsonElement actual, string path)
    {
        Assert.True(expected.ValueKind == actual.ValueKind, $"{path}: {expected.ValueKind} became {actual.ValueKind}");
        switch (expected.ValueKind)
        {
            case JsonValueKind.Object:
                string[] want = expected.EnumerateObject().Select(p => p.Name).OrderBy(n => n, StringComparer.Ordinal).ToArray();
                string[] got = actual.EnumerateObject().Select(p => p.Name).OrderBy(n => n, StringComparer.Ordinal).ToArray();
                Assert.True(
                    want.SequenceEqual(got, StringComparer.Ordinal),
                    $"{path}: keys on the wire are [{string.Join(", ", want)}], the record writes [{string.Join(", ", got)}]");
                foreach (JsonProperty property in expected.EnumerateObject())
                    AssertJsonEqual(property.Value, actual.GetProperty(property.Name), $"{path}.{property.Name}");
                return;
            case JsonValueKind.Array:
                JsonElement[] wantItems = expected.EnumerateArray().ToArray();
                JsonElement[] gotItems = actual.EnumerateArray().ToArray();
                Assert.True(wantItems.Length == gotItems.Length, $"{path}: {wantItems.Length} items became {gotItems.Length}");
                for (int i = 0; i < wantItems.Length; i++)
                    AssertJsonEqual(wantItems[i], gotItems[i], $"{path}[{i}]");
                return;
            case JsonValueKind.Number:
                // Compared numerically, not textually: 2.25e8 and 225000000 are the same value,
                // and neither runtime promises the other's rendering.
                Assert.Equal(expected.GetDouble(), actual.GetDouble());
                return;
            case JsonValueKind.String:
                Assert.Equal(expected.GetString(), actual.GetString());
                return;
            default:
                // True, False and Null carry everything they mean in ValueKind.
                return;
        }
    }

    // ----- proofs ---------------------------------------------------------------------

    [ContractVectorFact]
    public void Proof001_VerifiesWithItsEmbeddedJwkAndOpensTheChain()
    {
        string root = RequireVectors();
        string proof = ReadText(root, "proofs", "proof-001.jws");

        JsonElement claims = VerifyProof(proof);
        Assert.Equal(1, claims.GetProperty("seq").GetInt64());
        Assert.False(claims.TryGetProperty("ph", out _), "seq 1 omits ph");
        Assert.Equal(Wire.HttpMethod, claims.GetProperty("htm").GetString());
        Assert.Equal(
            ReadText(root, "batches", "batch-001.bh.txt").Trim(),
            claims.GetProperty("bh").GetString());
    }

    [ContractVectorFact]
    public void Proof002_ChainsFromProof001()
    {
        string root = RequireVectors();

        JsonElement first = VerifyProof(ReadText(root, "proofs", "proof-001.jws"));
        JsonElement second = VerifyProof(ReadText(root, "proofs", "proof-002.jws"));

        Assert.Equal(2, second.GetProperty("seq").GetInt64());
        Assert.Equal(first.GetProperty("sid").GetString(), second.GetProperty("sid").GetString());
        Assert.Equal(first.GetProperty("bh").GetString(), second.GetProperty("ph").GetString());
    }

    [ContractVectorFact]
    public void ProofEmbeddedJwk_ThumbprintsToTheLicenseCnfJkt()
    {
        string root = RequireVectors();
        using JsonDocument? header = Jws.DecodeHeaderUnverified(ReadText(root, "proofs", "proof-001.jws"));
        string jwk = header!.RootElement.GetProperty("jwk").GetRawText();

        Assert.True(Jwk.TryThumbprintOfEcJwk(jwk, out string thumbprint, out string error), error);

        Assert.True(Credential.TryReadLicenseClaims(
            ReadText(root, "license", "license-valid.jws"), out LicenseClaims? claims, out _));
        Assert.Equal(claims!.Jkt, thumbprint);
    }

    [ContractVectorFact]
    public void ProofBadBh_DoesNotMatchTheBody()
    {
        string root = RequireVectors();

        JsonElement claims = VerifyProof(ReadText(root, "proofs", "proof-bad-bh.jws"));

        Assert.NotEqual(
            Bytes.Sha256Base64Url(ReadBytes(root, "batches", "batch-001.br")),
            claims.GetProperty("bh").GetString());
    }

    [ContractVectorFact]
    public void ProofWrongKey_DoesNotVerifyWithTheLicensedKey()
    {
        string root = RequireVectors();
        string proof = ReadText(root, "proofs", "proof-wrong-key.jws");
        string clientJwk = ReadText(root, "keys", "client-pub.jwk.json");

        Assert.True(Jwk.TryImportPublicJwk(clientJwk, out ECDsa? licensed, out string error), error);
        using (licensed)
            Assert.False(Jws.Verify(proof, licensed!), "proof-wrong-key must not verify with the licensed key");
    }

    [ContractVectorFact]
    public void ProofStaleIat_IsOutsideTheSkewWindow()
    {
        string root = RequireVectors();

        JsonElement claims = VerifyProof(ReadText(root, "proofs", "proof-stale-iat.jws"));
        long iat = claims.GetProperty("iat").GetInt64();
        long now = DateTimeOffset.UtcNow.ToUnixTimeSeconds();

        Assert.True(Math.Abs(now - iat) > Wire.ClockSkewSeconds, "proof-stale-iat must be outside ±300 s");
    }

    /// <summary>
    /// The expectations file is the contract between the two verifiers: whatever the Go side says
    /// about each vector, the C# side must be able to reach the same verdict.
    /// </summary>
    [ContractVectorFact]
    public void ExpectedResults_AgreeWithWhatTheModComputes()
    {
        string root = RequireVectors();
        using JsonDocument expectations = JsonDocument.Parse(ReadText(root, "expected", "verify-results.json"));

        // The per-file verdicts live under "files"; the sibling members are metadata
        // (reference_time, issuer, htu, jkt, ...) that the fixtures above consume. Enumerating
        // the root instead would try to read `ok` off a number.
        JsonElement files = expectations.RootElement.GetProperty("files");
        Assert.True(files.EnumerateObject().Any(), "verify-results.json declared no file verdicts");

        foreach (JsonProperty entry in files.EnumerateObject())
        {
            string file = entry.Name;
            bool expectedOk = entry.Value.GetProperty("ok").GetBoolean();
            string path = Path.Combine(root, file.Replace('/', Path.DirectorySeparatorChar));
            if (!File.Exists(path) || !file.Contains("proof", StringComparison.Ordinal))
                continue;

            string proof = File.ReadAllText(path).Trim();
            bool signatureOk = TryVerifyProofSignature(proof);
            if (expectedOk)
                Assert.True(signatureOk, $"{file}: Go says ok, the mod could not verify the signature");
        }
    }

    // ----- helpers --------------------------------------------------------------------

    // Only reachable from a [ContractVectorFact], which skips the test when this is null.
    private static string RequireVectors() => TestPaths.ContractsTestData!;

    private static string ReadText(string root, params string[] parts)
        => File.ReadAllText(Path.Combine(new[] { root }.Concat(parts).ToArray()));

    private static byte[] ReadBytes(string root, params string[] parts)
        => File.ReadAllBytes(Path.Combine(new[] { root }.Concat(parts).ToArray()));

    /// <summary>The conformance batch, one NDJSON line per element.</summary>
    /// <param name="root">The vector directory.</param>
    /// <returns>The non-empty lines of <c>batch-001.ndjson</c>.</returns>
    private static string[] BatchLines(string root)
        => Encoding.UTF8.GetString(ReadBytes(root, "batches", "batch-001.ndjson"))
            .Split('\n', StringSplitOptions.RemoveEmptyEntries);

    private static ECDsa ServerKeyFromJwks(string root)
    {
        using JsonDocument jwks = JsonDocument.Parse(ReadText(root, "keys", "server-jwks.json"));
        JsonElement key = jwks.RootElement.GetProperty("keys")[0];
        Assert.True(Jwk.TryImportPublicJwk(key.GetRawText(), out ECDsa? imported, out string error), error);
        return imported!;
    }

    private static JsonElement VerifyProof(string proof)
    {
        using JsonDocument? header = Jws.DecodeHeaderUnverified(proof);
        Assert.NotNull(header);
        Assert.Equal(Wire.Alg, header!.RootElement.GetProperty("alg").GetString());
        Assert.Equal(Wire.ProofTyp, header.RootElement.GetProperty("typ").GetString());

        string jwk = header.RootElement.GetProperty("jwk").GetRawText();
        Assert.True(Jwk.TryImportPublicJwk(jwk, out ECDsa? key, out string importError), importError);
        using (key)
            Assert.True(Jws.TryVerify(proof, key!, out string error), $"proof must verify: {error}");

        using JsonDocument? payload = Jws.DecodePayloadUnverified(proof);
        return payload!.RootElement.Clone();
    }

    private static bool TryVerifyProofSignature(string proof)
    {
        using JsonDocument? header = Jws.DecodeHeaderUnverified(proof);
        if (header is null || !header.RootElement.TryGetProperty("jwk", out JsonElement jwk))
            return false;
        if (!Jwk.TryImportPublicJwk(jwk.GetRawText(), out ECDsa? key, out _))
            return false;
        using (key)
            return Jws.Verify(proof, key!);
    }
}
