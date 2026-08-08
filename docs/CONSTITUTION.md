# catlog constitution

catlog is a hobby project. One person runs it, on one cheap VPS, for the fun of a Kitten Space
Agency community that did not ask for it. Everything below follows from that.

These are the standing principles — what catlog optimises for when two reasonable designs
compete. They are not a design document and not a feature list;
[ARCHITECTURE.md](ARCHITECTURE.md) describes what exists. **[DECISIONS.md](DECISIONS.md) records what
we chose; this document says what we were optimising for when we chose it.** When a new decision
has to be made, read this first, decide, then write the decision (and its rationale) into
`DECISIONS.md`. When an existing decision looks wrong, check it against these principles before
re-opening it — most of the time the principle is why it looks the way it does.

A principle can lose to another principle. None of them lose to "it would be neat".

---

## 1. The handle is the only public identity, and no email is ever received

catlog never asks any identity provider for an email scope, never stores one, and derives its
player key as `HMAC-SHA256(pepper, "<idp>:<stable subject>")` (D17). The handle a player claims is
the only thing any public surface ever shows.

*Why:* what we never receive cannot leak, cannot be enumerated out of a database dump, and cannot
be subpoenaed. It also makes bans stick — an email is something a player can change, an IdP
subject is not. This is the one principle where "we'll add it later if we need it" is wrong:
a system that never had the data has a property a system that deleted the data does not.

## 2. It must stay cheap enough to forget about

One small binary, one embedded database, no framework, no managed services, no per-request cloud
bill (D1, D3, D4, D20). Read surfaces are static-cacheable so a CDN can absorb popularity for free.

*Why:* the failure mode for a hobby project is not a crash, it is a monthly invoice that makes the
owner turn it off. Anything whose cost scales with attention rather than with usefulness is
suspect.

## 3. The mod is a guest in someone else's game

The mod must never cost the player frames. Passive telemetry is windowed and sampled, not a raw
firehose (D15). Game-thread work is bounded and allocation-free where it can be; a subsystem that
faults logs once and latches dead rather than spamming or throwing inside a frame.

*Why:* a player installed catlog to be on a leaderboard, not to trade FPS for it. A mod that costs
performance gets uninstalled, and deserves to be. This principle outranks any amount of extra data
we might like to collect.

## 4. Everything runs locally, offline, on one machine

The whole system — mod, server, site, identity providers, archive — runs and is tested with no
network call to anything (D2, §10). External services are design targets, replaced by local
stand-ins that match their shapes.

*Why:* one person must be able to work on this on a plane, and the test suite must never be red
because somebody else's service is down or because a free tier ran out. It is also the cheapest
possible guarantee that we are not accidentally depending on a vendor.

## 5. The event log is immutable; everything else is rebuildable

Stored events are never rewritten. Every board, profile and feed line is a projection that can be
thrown away and rebuilt from the log (D8, D22). Only the log is archived, because only the log is
irreplaceable.

*Why:* it turns most classes of mistake into a rebuild rather than a migration, and most classes of
"we should have recorded that differently" into an upcaster rather than data loss. It is also what
makes the nightly rebuild a real correctness backstop instead of a ritual.

## 6. Every number is derived, never claimed

The mod reports what happened; the server decides what it is worth. Leaderboard values are always
computed by folding events, never accepted as a submitted stat. Stat keys are compile-time
constants and enums are allow-lists, so a client cannot mint a board or a category.

*Why:* this is the only integrity property that costs nothing — it falls out of event sourcing —
and it is the one that actually matters. It is also why the rest of this document can be so
relaxed about cheating.

## 7. Moderation must be trivial and total

One command must be able to remove a person and everything attached to them: events, batches,
streams, credentials, archive prefix — leaving a tombstone and a retired handle so the account
cannot come back and the handle cannot be impersonated (D9, §4.7).

*Why:* the owner is the entire moderation team. If removing a bad actor is a project, it will not
happen. Note that this is *separate* from §8 below: bans, purges and deny-lists exist for abuse and
decency, not for stat manipulation, and nothing in §8 constrains them.

## 8. Anti-cheat is proportionate, or it does not exist

> *In the owner's words:* "Our efforts to thwart bad actors from manipulating stats should be
> limited to assuming KSA game default data and settings. Simple reasoning beyond that is
> acceptable. I do not want in-depth, layered, complex code looking for esoteric patterns that
> shouldn't be theoretically possible unless it's a bad actor. This is just for fun. If we have
> instances that go that far, flag them for me to decide on removal."

catlog assumes stock KSA data and settings. A check that notices the game is *not* stock — a
default that was edited, a debug command that was used, a cheat the game itself ships — is in
scope, cheap, and welcome. A machine that tries to infer cheating from the *shape* of a player's
data is not.

The client is attacker-controlled and always will be. Signatures prove who sent something, never
that it is true. We accept that a determined person can put a fake number on a leaderboard about a
cat game, and we spend our complexity budget elsewhere.

### The test for "too far"

A proposed integrity check is **in scope** only if all five hold. If any fails, it is out of scope:
do not build it, and if it already exists, flag it in [integrity-audit.md](integrity-audit.md) for
the owner to decide.

1. **Stock-data test.** It compares against something KSA ships — a default value, a game
   constant, a game-provided predicate — or against catlog's own wire contract. It does not model
   what a player "ought" to be able to achieve.
2. **One-look test.** A reader can state what it does and why in one sentence, and the whole rule
   is visible in one place.
3. **No-new-machinery test.** It adds no new table, no new pipeline stage, no background job, and
   no state that accumulates across events, flights or players.
4. **Honest-player test.** A player doing nothing unusual can never trip it. If it needs a
   threshold tuned to keep false positives down, it is already too far.
5. **Consequence test.** Its only effect is to exclude a flight from the boards, or to reject a
   malformed request. It never queues work for a human, never scores suspicion, and never treats a
   player differently because of accumulated history.

Named and out of scope, so nobody has to re-argue them: physics-plausibility envelopes,
quarantine or pending-record pipelines, replay traces attached to record claims, robust z-scores
and statistical outlier detection, suspicion multipliers or reputation scores, shadow-banning,
community-report queues, and client attestation of any kind.

### What this principle does *not* govern

Three things look like anti-cheat and are not. They are load-bearing for other reasons and this
principle is never a reason to remove them:

- **Protocol hygiene** — signature verification, size caps, rate limits, strict framing, the
  known-type allow-list. These exist so an unauthenticated stranger cannot cost us CPU or disk.
- **Moderation** — bans, purges, tombstones, handle retirement, the deny-list (§7 above).
- **Durability** — archive checksums, restore verification, backup integrity. These protect the
  log from bit rot and from us, not from players.

## 9. The documentation is part of the system, not a description of it

Every contract in `docs/` is what two independent implementations — the Go server and the C# mod —
are built against. A change that leaves a document wrong is an **incomplete change**. In the same
commit: update the affected document, and record the decision and its *reasoning* in
[DECISIONS.md](DECISIONS.md). The full table of what to update when is in
[ARCHITECTURE.md](ARCHITECTURE.md#7-keeping-the-documentation-true), and the agent-facing version is in
[../CLAUDE.md](../CLAUDE.md).

*Why:* one person maintains this, assisted by agents that read the repository fresh every time, and
neither can hold the reasons in their head. A wrong document is worse than a missing one, because it
is believed — and a decision recorded without its *why* gets re-litigated within the year, which is
the specific failure this whole file exists to prevent. The cost is a paragraph at the time of the
change; the alternative is re-deriving an argument that was already won.

Two corollaries. **Nothing describes something that is gone** — a passage whose subject was deleted
is deleted or marked superseded, never left. And **a contract change carries its version bump**: an
event or payload bumps `ver`, an endpoint bumps `ver`, the credential file bumps `format`. Those
bumps are what let a client and a server disagree loudly rather than silently.

---

## Applying this

Most decisions are settled by naming which principle wins and writing that down. If two principles
genuinely collide and the answer is not obvious, that is the owner's call — record it in
`DECISIONS.md` with the collision spelled out, because the next person will hit the same one.

Nothing here is permanent. Amending a principle is fine; doing it deliberately, in a commit that
says so, is the whole point of writing them down.
