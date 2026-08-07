# catlog HTTP API — ingest, auth, read, errors, conformance vectors

Origin: [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md) §4.3–§4.5 and §4.8–§4.10, extracted verbatim.

> Everything in this document is the single source of truth for both the C# mod and the Go
> server. Changing anything here requires bumping `ver` on the affected endpoint and a line in
> [DECISIONS.md](DECISIONS.md). Event payloads live in [events.md](events.md).

## Wire format & limits

- Batch = NDJSON (one envelope per line, `\n` separated, UTF-8, no BOM), compressed with **Brotli**; request header `Content-Encoding: br`, `Content-Type: application/x-ndjson`.
- Events within a batch are ordered by outbox append order (oldest first). A batch never mixes streams.
- Limits (server-enforced; mirror in mod constants):

| Limit | Value | Violation |
|---|---|---|
| Max compressed body | 1 MiB | `413 too_large` |
| Max decompressed | 8 MiB | `413 too_large` |
| Max events/batch | 2000 | `413 too_large` |
| Max single event line | 16 KiB | `400 malformed_batch` |
| Clock skew (proof `iat`) | ±300 s | `401 clock_skew` |
| Per-credential rate | token bucket: 1 batch / 2 s, burst 5 | `429` + `Retry-After` |

## Ingest HTTP API

```
POST /v1/ingest
Content-Type: application/x-ndjson
Content-Encoding: br
X-Catlog-License: <compact license JWS>
X-Catlog-Proof:   <compact proof JWS>
<brotli(NDJSON)>
```

Responses (JSON body; `Date` header always present — the mod uses it for clock sync):

| Status | Body | Meaning |
|---|---|---|
| 200 | `{"accepted": n, "deduped": n}` | Stored (some rows may be duplicate event_ids). |
| 200 | `{"accepted": 0, "deduped": n, "replay": true}` | Whole-batch replay short-circuit (batch_id seen). |
| 400 | `{"error": "malformed_batch", "detail": s}` | Undecodable body / bad envelope / unknown type. |
| 401 | `{"error": <auth code, see error registry below>, "server_time": unix_ms}` | Auth failure. `clock_skew` includes `server_time`. |
| 409 | `{"error": "stream_fork"}` | seq conflict (see "Server verification order"). Mod recovery: mint new stream. |
| 413 | `{"error": "too_large"}` | Over limits. Mod recovery: halve batch size, retry. |
| 415 | `{"error": "unsupported_encoding"}` | Missing/wrong Content-Encoding. |
| 429 | `{"error": "rate_limited"}` + `Retry-After` | Back off. |

Public read endpoints below. Health: `GET /healthz` → `200 {"ok": true}` (no auth, no DB write).

## Auth: license + proof JWS (ES256 only)

All JWS are **compact serialization**, alg allow-list `{ES256}` (reject anything else — no `none`, no RSA). Base64url without padding. .NET note: `ECDsa.SignData(..., SHA256)` already emits the r‖s IEEE-P1363 format JWS requires — no DER conversion.

### License JWS (server-signed, issued by dashboard/admin)

Header: `{"alg": "ES256", "kid": "catlog-<yyyymm>", "typ": "catlog-license+jwt"}`
Claims:

```jsonc
{
  "iss": "<issuer base URL, e.g. http://127.0.0.1:8080>",
  "sub": "<base64url(32-byte user_key)>",
  "handle": "whiskers_prime",
  "cnf": { "jkt": "<RFC 7638 SHA-256 thumbprint of client public JWK>" },
  "iat": 1770000000,
  "exp": 1785552000,            // iat + 180 days
  "jti": "lic_<ulid>",
  "ver": 1
}
```

Server signing key: P-256, PEM at `data/keys/license-signing.pem` (created by `catlogctl keygen`). Public JWKS served at `GET /.well-known/catlog-jwks.json` (`{"keys":[...]}`, each with `kid`). Rotation = add a key with new `kid`, keep old until all licenses expire.

### Proof JWS (client-signed, one per batch)

Header: `{"alg": "ES256", "typ": "catlog-proof+jwt", "jwk": {<public EC JWK: kty,crv,x,y only>}}`
Claims:

```jsonc
{
  "jti": "<batch_id ulid>",      // doubles as the batch id for replay short-circuit
  "iat": 1770000000,             // client time corrected by server-Date offset
  "htm": "POST",
  "htu": "<configured ingest URL, e.g. http://127.0.0.1:8080/v1/ingest>",
  "bh":  "<base64url(SHA-256(raw request body bytes AS SENT, i.e. post-brotli))>",
  "sid": "<stream ulid>",        // stream = one outbox instance epoch
  "seq": 42,                     // 1-based, strictly monotonic per (jkt, sid)
  "ph":  "<base64url(SHA-256(previous batch's body bytes))>"   // OMITTED when seq == 1
}
```

`htu` is compared against the server config list `accepted_htu` (string equality, no normalization) — dev config lists the dev URL, prod lists the public URL.

### Server verification order (cheapest first — implement exactly this order)

1. Both headers present; each JWS ≤ 4 KiB; compact format parses.
2. License header: `alg == ES256`, known `kid` → verify signature with server key (cache parsed licenses by SHA-256 of the raw JWS string, LRU 10k).
3. License claims: `iss` matches config; `exp` not passed; `ver == 1`.
4. Deny-list: `sub` not banned, `cnf.jkt` not revoked (in-memory set, §5.8).
5. DB: credential row for `jkt` exists, not revoked, matches `handle` + player (this also catches deleted accounts).
6. Proof header: `alg == ES256`, embedded `jwk` is P-256; **thumbprint(jwk) == license `cnf.jkt`** — else `401 proof_invalid`.
7. Proof signature verifies with embedded jwk.
8. `htm == "POST"`, `htu ∈ accepted_htu`, `|iat - now| ≤ 300 s` (else `clock_skew`).
9. Rate limit token bucket keyed `jkt` (see limits table) — before reading the body.
10. Read body (enforce size caps while reading). `bh == b64u(sha256(body))` — else `proof_invalid`.
11. Batch replay: row exists in `ingest_batch(player, jti)` → `200 replay` short-circuit, stop.
12. Stream check against `stream_state(player, sid)`: no row → require `seq == 1` and no `ph` (else `409`). Row exists → `seq == last_seq + 1 && ph == last_bh` accepted; `seq <= last_seq` → `409 stream_fork`; `seq > last_seq + 1` → accept but set `gap` marker in stream_state (telemetry is loss-tolerant; forensics only).
13. Decompress (cap 8 MiB), parse NDJSON, validate envelopes, txn: insert events (`INSERT OR IGNORE` on `(player_id,event_id)`), upsert `stream_state`, insert `ingest_batch`, commit.

Mod-side failure handling: `401 clock_skew` → recompute offset from `Date` header, re-sign, retry once; `409` → mint new `sid`, reset `seq=1`, continue (old chain abandoned); `413` → halve batch event cap (floor 50), retry; `429`/`5xx`/network → exponential backoff 1 s·2ⁿ + full jitter, cap 5 min, batches coalesce.

### Sessions & CSRF (website only — unrelated to ingest)

Cookie `catlog_sess` (prod: `__Host-catlog_sess`): value `b64u(user_key) + "." + exp_unix + "." + b64u(HMAC-SHA256(session_key, user_key_bytes || exp))`; TTL 7 days; `HttpOnly; SameSite=Lax; Path=/` (+`Secure` in prod). `session_key` = 32 random bytes at `data/keys/session.key`. CSRF: wrap mutating routes with Go 1.25 `http.CrossOriginProtection`. OAuth `state`: 32 random bytes, stored in a short-lived cookie, compared on callback.

## Read API (public, CDN-cacheable JSON)

All responses `Cache-Control: public, s-maxage=30, stale-while-revalidate=300` except SSE.

- `GET /v1/leaderboards` → `{"boards": [{"stat": "biggest_lithobrake_survived", "title": s, "unit": "m/s", "count": n}]}`
- `GET /v1/leaderboards/{stat}?limit=50&offset=0` (limit ≤ 200) → `{"stat": s, "rows": [{"rank": 1, "handle": s, "value": f, "context": {…}, "updated": unix_ms}]}`
- `GET /v1/players/{handle}` → `{"handle": s, "since": unix_ms, "stats": [{"stat": s, "value": f, "rank": n, "context": {…}}]}` (404 if unknown/banned)
- `GET /v1/feed/sse` → datastar SSE stream of recent-activity fragments (no cache)
- `GET /.well-known/catlog-jwks.json`, `GET /.well-known/catlog-denylist.json` (§5.8)

Site HTML routes (§5.7): `/`, `/boards`, `/boards/{stat}`, `/p/{handle}`, `/login`, `/auth/{idp}/start`, `/auth/{idp}/callback`, `/dashboard`, `/docs/{install,privacy,api}`. Dashboard JSON API (session-auth’d, CSRF-protected): `GET /api/me`, `GET /api/handles`, `POST /api/handles` `{handle, jwk}` → `{license}`, `POST /api/handles/{handle}/reissue` `{jwk}` → `{license}`, `POST /api/handles/{handle}/revoke`, `POST /api/me/delete`, `POST /api/logout`.

## Error code registry

`bad_request, malformed_batch, unsupported_encoding, license_invalid, license_expired, license_revoked, proof_invalid, clock_skew, banned, stream_fork, rate_limited, too_large, not_found, handle_taken, handle_invalid, handle_reserved, quota_exceeded, account_too_new, internal`. JSON shape everywhere: `{"error": code, "detail"?: s, "server_time"?: ms}`.

## Cross-language conformance vectors — `contracts/testdata/`

Generated deterministically by `catlogctl testvectors generate contracts/testdata` (fixed keys, fixed timestamps — no randomness; regeneration is byte-identical). Consumed by **both** Go and C# test suites; this is what guarantees mod↔server interop without KSA.

Files:

```
keys/server-signing.pem, keys/server-jwks.json
keys/client-p256.pem, keys/client-pub.jwk.json, keys/client.jkt.txt
license/license-valid.jws, license-expired.jws, license-claims.json
batches/batch-001.ndjson, batch-001.br, batch-001.bh.txt
proofs/proof-001.jws (valid, seq=1), proof-002.jws (seq=2, correct ph),
       proof-bad-bh.jws, proof-wrong-key.jws, proof-stale-iat.jws
expected/verify-results.json   // map file → {ok: bool, error: code}
```

C# tests must: verify Go-produced license + proofs (signature + claims), reproduce `batch-001.bh.txt` from `batch-001.br`, reproduce `client.jkt.txt` from the JWK (RFC 7638), and produce proofs that the Go verifier accepts (exercised again live in WP7). Signatures are randomized (both runtimes), so tests verify rather than byte-compare.
