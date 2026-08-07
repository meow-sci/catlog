# catlog credential file

Origin: [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md) §4.6, extracted verbatim.

> Everything in this document is the single source of truth for both the C# mod and the Go
> server. Changing anything here requires bumping `format` and a line in
> [DECISIONS.md](DECISIONS.md). The license and proof JWS it carries are specified in
> [ingest-api.md](ingest-api.md).

## Credential file (what the player downloads / the sim uses)

`catlog-credential.json` — assembled **client-side** (browser or `catlogctl issue`); the private key never reaches the server in the browser flow:

```jsonc
{
  "format": 1,
  "handle": "whiskers_prime",
  "license": "<compact license JWS>",
  "private_key_pem": "-----BEGIN PRIVATE KEY-----\n...(PKCS#8 EC P-256)...\n-----END PRIVATE KEY-----\n"
}
```

Mod default location: `<KSA user dir>/mods/catlog/credential.json`; sim/tests take a path argument. Loader must: parse license (unverified decode) to display handle/expiry, compute jkt from the private key's public part, and **refuse to start shipping if jkt ≠ license `cnf.jkt`**.
