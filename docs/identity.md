# catlog identity — user_key & handles

Origin: [INITIAL_IMPL_PLAN.md](../INITIAL_IMPL_PLAN.md) §4.7, extracted verbatim.

> Everything in this document is the single source of truth for both the C# mod and the Go
> server. Changing anything here requires a line in [DECISIONS.md](DECISIONS.md).

## Identity, user_key, handles

`user_key = HMAC-SHA256(pepper, subject_string)`, where `subject_string` is:

| IdP | subject_string | Flow | Scopes | Account-age gate |
|---|---|---|---|---|
| Discord | `"discord:" + snowflake id` | OAuth2 code (no OIDC) → `GET /api/users/@me` | `identify` only | snowflake age ≥ 30 days (`(id>>22)+1420070400000`) |
| Google | `"google:" + id_token sub` | OIDC code flow; verify `id_token` against issuer JWKS | `openid` only | none (quotas only) |
| GitHub | `"github:" + numeric user id` | OAuth2 code → `GET /user` | none (default) | `created_at` ≥ 30 days |

`pepper` = 32 random bytes at `data/keys/pepper.key` (created by `catlogctl keygen`). Never in the DB, never logged. **Email is never requested from any IdP.** Discard IdP tokens immediately after reading the subject.

Handle rules (D9):

- Regex: `^[A-Za-z0-9]([A-Za-z0-9._-]{0,148}[A-Za-z0-9])?$` (1–150 chars, US-ASCII alnum + `._-`, must start/end alnum).
- Uniqueness: case-insensitive (`handle_lc` column, unique index). Original casing preserved for display.
- Reserved list (rejected at claim): `admin, administrator, catlog, api, root, system, mod, moderator, staff, official, support, help, www` + configurable extras.
- Immutable: no rename. New handle = new claim (subject to quota).
- **Never recycled**: ban or account deletion moves `handle_lc` into `retired_handle` permanently; claim checks consult both tables.
- Quotas per account: ≤ 5 live handles; ≤ 3 license issuances per 24 h (covers new + reissue).

Ban/delete: purge = `DELETE` all events/batches/stream_state for player, delete credentials + handles (retiring handle_lc), delete archive prefix (fs store), keep tombstone `{user_key, reason, at}` + revoked jkts in deny-list. Projections heal on next rebuild; fast path filters banned players in read queries.
