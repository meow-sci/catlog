using System.Text.Json.Serialization;
using MeowSci.Catlog.Lib.Auth;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Ship;

/// <summary>The claim set of a per-batch proof JWS (§4.5.2).</summary>
/// <param name="Jti">The batch ULID. Doubles as the replay short-circuit key server-side.</param>
/// <param name="Iat">Client time corrected by the learned server-clock offset, in unix seconds.</param>
/// <param name="Htm">Always <c>POST</c>.</param>
/// <param name="Htu">The configured ingest URL, compared byte-for-byte against the server's allow-list.</param>
/// <param name="Bh">base64url SHA-256 of the raw request body <b>as sent</b>, i.e. post-Brotli.</param>
/// <param name="Sid">The stream ULID — one outbox instance epoch.</param>
/// <param name="Seq">1-based, strictly monotonic per (jkt, sid).</param>
/// <param name="Ph">
/// base64url SHA-256 of the previous batch's body bytes. <b>Omitted</b> when <paramref name="Seq"/>
/// is 1 — the server requires its absence there, so this is a null-omitted property rather than an
/// empty string.
/// </param>
public sealed record ProofClaims(
    [property: JsonPropertyName("jti")] string Jti,
    [property: JsonPropertyName("iat")] long Iat,
    [property: JsonPropertyName("htm")] string Htm,
    [property: JsonPropertyName("htu")] string Htu,
    [property: JsonPropertyName("bh")] string Bh,
    [property: JsonPropertyName("sid")] string Sid,
    [property: JsonPropertyName("seq")] long Seq,
    [property: JsonPropertyName("ph")]
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    string? Ph);

/// <summary>Builds the compact proof JWS that accompanies every batch.</summary>
public static class ProofSigner
{
    /// <summary>Signs a proof.</summary>
    /// <param name="credential">The player's credential; supplies the key and its public JWK.</param>
    /// <param name="claims">The claim set.</param>
    /// <returns>The compact proof JWS.</returns>
    public static string Sign(Credential credential, ProofClaims claims)
    {
        // The header embeds the public JWK so the server can verify the signature and compare its
        // thumbprint to the license's cnf.jkt (§4.5.3 step 6). Only kty/crv/x/y are permitted.
        string header =
            $"{{\"alg\":\"{Wire.Alg}\",\"typ\":\"{Wire.ProofTyp}\",\"jwk\":{credential.PublicJwkJson}}}";
        return Jws.Sign(header, CatlogJson.Serialize(claims), credential.Key);
    }
}
