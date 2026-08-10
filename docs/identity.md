# catlog identity — user_key & handles

Owns **§4.7**.

> **Normative.** This document is the single source of truth for both the C# mod and the Go server.
> Handle rules are permanent by design (D9) — read [CONSTITUTION.md](CONSTITUTION.md) §1 and §7 before touching any of them. Anything that changes here needs a dated entry in [DECISIONS.md](DECISIONS.md) saying why,
> in the same commit — see [ARCHITECTURE.md](ARCHITECTURE.md#7-keeping-the-documentation-true).

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

## Moderation

Three actions, and choosing between them is choosing what happens to the account's **data**.

| Action | The account | Their events | Reversible |
|---|---|---|---|
| **Ban** | Credentials revoked, handles retired permanently (D9), refused at §4.5.3 step 4. They know at once. | Stay in the log and keep scoring until a rebuild; hidden from every read surface immediately. | `unban` |
| **Shadow ban** | Untouched. Credential valid, ingest answered `200` exactly as before. **They are not told.** | Moved to `shadowban_event`; everything they ship afterwards is routed there too. | `unshadowban` |
| **Purge** | Deleted: player row, handles, credentials. Tombstone `{user_key, reason, at}` keeps them out forever. | Deleted from both tables, and the archive prefix with them. | **No** |

**Ban** = set `banned_at`, revoke every live credential, retire every handle, refresh the deny-list.

**Purge** = `DELETE` all events, withheld events, batches and `stream_state` for the player, delete
credentials and handles (retiring `handle_lc`), delete the archive prefix, keep the tombstone and the
revoked jkts in the deny-list. Projections heal on the next rebuild; the fast path filters purged and
banned players out of read queries because they hold no directory entry.

### Shadow bans

A shadow ban is for the case a ban handles badly: someone whose behaviour needs reviewing rather than
punishing on the spot, and someone who would simply make another account the moment they were told.

It differs from a ban in exactly two ways, and both are the point:

- **Nothing is deleted.** Their events move to `shadowban_event` at their original `seq`, so both
  later decisions stay open — `unshadowban` puts them back exactly where they were, and
  `shadowban-delete` destroys them for good. A ban that deleted foreclosed one of them.
- **Their client keeps working.** No revocation, no deny-list entry, no tombstone: the mod ships and
  is answered normally, and everything it ships is withheld too. A review needs the evidence to keep
  arriving, and a loud ban stops it.

What it shares with a ban is the read side. The handle stops resolving the moment the directory
reloads, so every public surface treats the account as handle-less. **"Silent" means silent at ingest,
not undetectable**: a player who looks up their own profile will find it gone, exactly as a banned
player would. What they will not get is an error from their game.

Handles are deliberately **not** retired. Retirement is permanent and unclaimable-forever (D9), which
is the wrong shape for a reversible action — and the handle is still owned, so nobody else can take it
meanwhile.

Their board rows leave on the next rebuild, which every shadow-ban verb queues automatically (§5.6).
Until it lands the directory is what hides them.

**A shadow ban is not anti-cheat.** Constitution §8 forbids shadow-banning in the sense of inferring
a cheater from the shape of their data and silently voiding them. This is the moderation sense —
applied by a named administrator to a named account, for abuse or decency — which §8's own preamble
exempts: *"bans, purges and deny-lists exist for abuse and decency, not for stat manipulation, and
nothing in §8 constrains them."*
