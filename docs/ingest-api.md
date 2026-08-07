# catlog HTTP API — ingest, auth, read, errors, conformance vectors

Origin: [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md) §4.3–§4.5 and §4.8–§4.10, extracted verbatim.

> Everything in this document is the single source of truth for both the C# mod and the Go
> server. Changing anything here requires bumping `ver` on the affected endpoint and a line in
> [DECISIONS.md](DECISIONS.md). Event payloads live in [events.md](events.md).

## Wire format & limits

- Batch = NDJSON (one envelope per line, `\n` separated, UTF-8, no BOM), compressed with **Brotli**; request header `Content-Encoding: br`, `Content-Type: application/x-ndjson`.
- Events within a batch are ordered by outbox append order (oldest first). A batch never mixes streams.
- The envelope's keys are exactly those in [events.md](events.md) §4.1 and **unknown envelope keys reject the batch**. Every one is required, including `career` — 16 lowercase Crockford base32 characters (`0-9 a-z` minus `i l o u`), stable for the lifetime of one KSA save. A malformed or missing `career` is `400 malformed_batch`, like any other envelope error.
- Limits (server-enforced; mirror in mod constants):

| Limit | Value | Violation |
|---|---|---|
| Max compressed body | 1 MiB | `413 too_large` |
| Max decompressed | 8 MiB | `413 too_large` |
| Max events/batch | 2000 | `413 too_large` |
| Max single event line | 16 KiB | `400 malformed_batch` |
| Clock skew (proof `iat`) | ±300 s | `401 clock_skew` |
| Per-credential rate | token bucket: 1 batch / 2 s, burst 5 | `429` + `Retry-After` |

**Client-side rate floor (mod, not server).** The token bucket above is the server's backstop against a *hostile* client, and it stays exactly as specified. Complementing it, the mod enforces its own hard minimum of **one request per 30 s** (`Wire.MinShipIntervalSeconds`) at its point of transmission, so a stock install never approaches the bucket in the first place — a client at its own floor spends 6.7% of the sustained allowance. It is a compile-time constant with no corresponding key in `catlog.toml`: `ship_interval_s` is clamped up to it, and the shipper independently refuses to transmit inside the window whatever the config says, so editing the TOML cannot turn one install into a firehose. Every retry in the recovery table below is subject to it too, including `409` and `413`, which used to resend immediately. The single exemption is the mod's once-per-session courtesy flush at game unload. Servers must not rely on this — it is a promise about the shipped client, not a wire rule — but it is why a well-behaved client should never produce a `429`.

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

## Idempotency contract

> **This is a hard requirement, not a best-effort property.** A client that is unsure whether a
> request landed must be able to send it again, blindly, with no ill effect. Everything in this
> section is enforced by a database index and covered by tests
> (`server/internal/ingest/idempotency_test.go`, `server/integration/idempotency_test.go`,
> `server/internal/store/events_test.go`, `mod/catlog.lib.tests/Ship/BatchShipperTests.cs`).

### The key

| Level | Key | Client half | Server half | Enforced by |
|---|---|---|---|---|
| Event | `(player_id, event_id)` | envelope `id` — a ULID the client mints, one per event, stable across every resend | `player_id`, resolved from the verified credential | `CREATE UNIQUE INDEX ev_dedup ON event(player_id, event_id)` + `INSERT OR IGNORE` |
| Career | `(player_id, career)` | envelope `career` — opaque to the server; the client keeps it stable per save | `player_id`, same derivation | Grouping key only: it scopes `sim_t` so the career-time boards are comparable (see [events.md](events.md)) |
| Batch | `(player_id, batch_id)` | proof `jti` — a ULID the client mints, one per batch | `player_id`, same derivation | `PRIMARY KEY (player_id, batch_id)` on `ingest_batch` (verification step 11) |

**The server never trusts a client-supplied identity.** `player_id` comes from exactly one place:
step 5 looks up the `credential` row by the proof key's thumbprint (`cnf.jkt`), and cross-checks
that the row's player has the `user_key` the license `sub` names. There is no player, handle,
subject or account field anywhere in the request body, and §4.1 rejects **unknown envelope keys**
outright — so a hostile client cannot invent one and cannot write into another player's namespace.
Unknown *payload* keys are preserved verbatim for forward compatibility, and are never read as
identity: a payload containing `"player_id": 7` is stored as data and attributed to the sender.

Consequences a client can rely on:

- Two players who mint the **same** event id get two distinct rows. Dedup is per player, never global.
- One player cannot suppress another's writes by guessing batch ids: replay is scoped per player too.
- A ULID collision within one player is the only way to lose an event, which is what ULIDs are for.

### Which retries are safe

| You did this | Server answers | Stored |
|---|---|---|
| Resend the identical request (same `jti`, same body), N times | `200 {"accepted": 0, "deduped": n, "replay": true}` every time | unchanged |
| Resend the same events under a **new** `jti` on the **next** `seq` | `200 {"accepted": 0, "deduped": n}` | unchanged |
| Send a batch that **overlaps** an earlier one | `200 {"accepted": <new>, "deduped": <already had>}` | only the new events |
| Any of the above after a server restart | identical — the guarantee is index-backed, not in-memory | unchanged |
| Resend the same events under a **new** `jti` on the **same** `seq` | `409 stream_fork` — see below | unchanged |

Nothing is ever double-counted, and no retry can corrupt a projection: the projector folds the
event log, and a deduped event never enters it.

### The one rule for clients: keep the batch id across retries of the same bytes

A timeout is the case that matters — you did not see a response, so you cannot know whether the
batch committed, and you cannot advance `seq`. If you also mint a **fresh** `jti` for the resend,
you miss the step-11 replay short-circuit and fall through to step 12, where your unchanged `seq`
reads as a reused one and earns `409 stream_fork`. The events were already safe; the duplicate
would have been harmless; the chain rejects it anyway.

So: **mint the batch id once per batch *body*, and reuse it for every retry of those bytes.**
Mint a new one only when the batch contents change (after a `413` halving, or when the batch is
rebuilt from a different set of pending events). The mod does exactly this and persists the pairing
to its outbox, so a game crash mid-ship replays cleanly rather than forking
(`BatchShipper.BatchIdFor`).

A client that ignores this rule still loses nothing: `409` is recoverable — mint a new `sid`, reset
`seq = 1`, resend — and the event dedup index absorbs the resend so the data converges either way.
It costs a round trip and an abandoned chain, not a row. That residual is pinned by
`TestRetryWithANewBatchID/a_fresh_batch_id_on_the_same_seq_forks_the_stream`.

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

Every one of those retries also waits out the mod's client-side 30 s floor (see the limits table): "retry" means "on the next attempt", never "immediately", and each backoff is `max(ladder, 30 s)` — so the published 1 s·2ⁿ ladder only starts to bind at its sixth rung. `Retry-After` is honoured when it asks for *longer* and floored when it asks for shorter. This costs recovery latency and nothing else: a `413` converging 500 → 50 takes four windows rather than four round trips, which for a loss-tolerant bulk telemetry pump is free.

### What the stream chain actually buys (`sid` / `seq` / `ph`)

Stated plainly, because it is easy to over-claim and the wire cost is real (a `seq`, a 43-byte `ph`,
and a `stream_state` row per stream):

**It does provide:**

- **Gap visibility.** A skipped `seq` is accepted — telemetry is loss-tolerant — and marks the
  stream permanently. That marker is the only signal in the system that distinguishes "this client
  shipped less" from "this client's batches were lost in transit", and it is surfaced at
  `GET /admin/stats` as `streams.{total, gapped, gapped_players}` (and by `catlogctl stats`).
- **Ordering hygiene and debuggability.** `ph` chains each batch to the bytes of the previous one,
  so a support question of the form "did batch N really precede batch N+1 from this install?" has
  an answer.

**It does not provide:**

- **Dedup.** That is the `(player_id, event_id)` unique index plus the `(player_id, batch_id)`
  replay short-circuit, both of which work with the chain removed. See the idempotency contract above.
- **Ordering of the stored log.** That is the server-local `event.seq` rowid.
- **Credential-theft detection.** Earlier design notes framed a fork as "a high-signal indicator of
  credential theft". It is not, and nothing in the server treats it as one: a fork is not counted
  per player, not alerted on, and not retained — and the documented client recovery is to mint a new
  `sid` and carry on, so a thief pays exactly one `409` and is otherwise unimpeded. Treat a `409` as
  what it is: a client whose local chain state and the server's disagree, usually because of a lost
  response or a restored save.

The chain stays (D5, the wire contract, the mod mirror and the §4.10 conformance vectors all pin
it), but it must not be relied on for anything in the second list.

### Sessions & CSRF (website only — unrelated to ingest)

Cookie `catlog_sess` (prod: `__Host-catlog_sess`): value `b64u(user_key) + "." + exp_unix + "." + b64u(HMAC-SHA256(session_key, user_key_bytes || exp))`; TTL 7 days; `HttpOnly; SameSite=Lax; Path=/` (+`Secure` in prod). `session_key` = 32 random bytes at `data/keys/session.key`. CSRF: wrap mutating routes with Go 1.25 `http.CrossOriginProtection`. OAuth `state`: 32 random bytes, stored in a short-lived cookie, compared on callback.

## Read API (public, CDN-cacheable JSON)

All responses `Cache-Control: public, s-maxage=30, stale-while-revalidate=300` except SSE.

- `GET /v1/leaderboards` → `{"min_players": n, "boards": [{"stat": "biggest_lithobrake_survived", "title": s, "unit": "m/s", "ascending": false, "count": n}]}`
- `GET /v1/leaderboards/{stat}?limit=50&offset=0` (limit ≤ 200) → `{"stat": s, "ascending": b, "rows": [{"rank": 1, "handle": s, "value": f, "context": {…}, "updated": unix_ms, "rewound"?: true}]}`
- `GET /v1/players/{handle}` → `{"handle": s, "since": unix_ms, "stats": [{"stat": s, "title": s, "unit": s, "value": f, "ascending": b, "rank": n, "players": n, "context": {…}, "updated": unix_ms, "rewound"?: true}]}` (404 if unknown/banned)
- `GET /v1/players?q=whis&limit=20` → `{"query": s, "limit": n, "handles": [s], "truncated"?: true}` — handle search
- `GET /v1/players/{handle}/events?limit=50&before=<cursor>&type=<event type>` → `{"handle": s, "limit": n, "type"?: s, "next"?: <cursor>, "events": [{"id": ulid, "type": s, "ver": n, "session"?: ulid, "flight"?: ulid, "career"?: s, "sim_t"?: f, "recv": unix_ms, "payload": {…}}]}` (404 if unknown/banned)
- `GET /v1/compare?handles=a,b,c` → `{"handles": [{"handle": s, "found": b, "since"?: unix_ms}], "boards": [{"stat": s, "title": s, "unit": s, "ascending": b, "players": n, "rows": [{"handle": s, "value": f, "rank": n, "context": {…}, "updated": unix_ms, "rewound"?: true}]}]}`

`ascending` and `players` on a profile row are what a profile page needs to render "#3 of 41" without also fetching the board index; `players` is the board's row count, banned players included, exactly like `count` above — the rank is filtered, so a rank is never *worse* than that denominator implies.

### Handle search — `GET /v1/players?q=`

Handles and nothing else: a result is a list of links, and everything behind one is a request away. Prefix matches come first, then substring matches, each group ordered by the lowercase handle, so the same URL is always the same answer and a CDN can hold it. Banned players are absent because the in-memory directory it scans is the same one every board resolves handles through. `q` shorter than **2** characters or longer than **150** (§4.7's handle cap) is `400 bad_request`; `limit` defaults to 20 and clamps to 50; `truncated` says a narrower query is needed, because there is no offset.

### N-handle comparison — `GET /v1/compare?handles=`

Up to **8** handles, deduplicated case-insensitively, in request order — repeating `?handles=` is the same as one comma-separated list, and extras past the cap are dropped rather than rejected, so the echoed `handles` array is the authoritative column order. Each handle is assembled by the same code behind `GET /v1/players/{handle}` and then pivoted board-first, so the two endpoints cannot disagree. `rank` is the position on the whole board, not among the compared handles.

An unknown, retired or banned handle comes back as `{"handle": s, "found": false}` — one answer for all three, exactly as the profile endpoint 404s all three, and no more than asking for that one profile already reveals. A board is included when **at least one** of the compared handles is on it, and carries only the rows that exist: an absent player is absent, not zero. The `min_players` listing rule does not apply here, for the same reason it does not apply to a profile — that threshold governs the public index.

### Raw event browsing — `GET /v1/players/{handle}/events`

What catlog actually recorded, newest first: the §4.1 envelope and the §4.2 payload, including payload keys this build has never heard of. Paging is by cursor (`next` → `?before=`), not offset, because the log grows at the head; treat the cursor as opaque, and page until `next` is **absent** rather than until a page comes back short — a `?type=`-filtered page that hits the server's scan bound looks identical to the last page. `limit` defaults to 50 and clamps to 200.

**What is redacted, and why.** Three §4.2 fields are derived from the mod's install id, which is one value per *machine* and therefore per person rather than per account. catlog is built so one person may hold two accounts with no way to tell from outside — and both accounts ship from the same install, so publishing any of these raw would link them:

| field | where | published as |
|---|---|---|
| `install` | `session.started.payload` | **dropped** — there is nothing to group by, so nothing to keep |
| `kid` | `kitten.*`, `roster.snapshot.kittens[]` | **relabelled per player** |
| `career` | the §4.1 envelope, and the `context` of every career-time board row | **relabelled per player** |

A relabelling is a 16-character Crockford base32 token, the same shape as the value it replaces, stable for a player and unrelatable between players. It still groups a player's own rows — "these records came from one save", "these EVAs were one kitten" — which is why the two are relabelled rather than dropped. The rules are keyed **by field name at any depth**, so a nested or newly-added occurrence is covered without being enumerated; a new §4.2 field with the same property must be added to that list. The envelope's `wall_t` is not published either: it is the untrusted client clock, and its offset from `recv` is a per-machine constant.

Everything else is published. Flight and session ids are per-occurrence ULIDs with no install in them, and body names, vehicle names, kitten names and every number are gameplay — a raw view that hid them would not be worth building. Two residuals are accepted rather than hidden: a person who names their kittens the same way under both accounts is correlatable by those names (the same exposure as picking a recognisable handle), and receive times correlate anything shipped at the same moment (the activity feed has published per-handle timestamps since §5.6).

`user_key` appears in no read-API response and never has.

`ascending` is `true` on the career-time boards (`fastest_to_orbit`, `fastest_to_<body>`), where the value is seconds since the career began and the **smallest** value ranks first; it is `false` on every record and counter board. The tie rule does not change with it: an equal value keeps the earlier claimant's rank. `rewound` is emitted only when true, and only on a career-time row whose career has had an earlier save loaded — it qualifies the number and has no other effect (see [events.md](events.md)).

**The board list is not fixed, and a client must not treat it as one.** Most stat keys are compile-time constants — one per fold — but two families are not: `fastest_to_<body>` and `rud_<cause>` take their second half from the event stream, because KSA's celestial systems are game content and `body` is opaque to the server ([events.md](events.md)). Titles, units and `ascending` are derived from the key, so a board for a place nobody has ever named in this repository arrives fully described. `min_players` is how many *distinct* players such a board needs before it is listed here (default 2, `[boards] min_players`); below that it is still served at `/v1/leaderboards/{stat}` and still appears on the profile of whoever is on it, it is just not in the index. A key that is neither a fixed board nor a family board anybody holds a value on is a 404.
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
