# R2 archive — design only

Status: **design, not implemented.** Written by WP10 (`INITIAL_IMPL_PLAN.md` §5.10, §12).

**D8 is a hard boundary: no code in this repository calls R2, and the AWS SDK is not a
dependency of the `server` module.** The archive that exists and runs today is the filesystem
store in `server/internal/archive` (`fsStore`, rooted at `data/archive/`). This document
describes what a second implementation would look like when the owner decides to add one, so
that decision is a small, bounded piece of work rather than a redesign.

Everything below is the *only* thing that would change: one file implementing three methods.
The key layout, the chunk format, the manifest, the cursor, the archive run, the purge hook and
the restore path are all storage-agnostic already, and none of them would be touched.

---

## 1. What is archived

Only the **raw event log** (D8). Projections are derived and rebuildable (§5.6), so archiving
them would be archiving a cache — and the restore path proves the point by rebuilding them from
the restored log.

Archiving **copies**. Nothing in the archive path deletes an event, truncates the log or prunes a
local file. Local retention pruning is a separate decision that has not been taken; when it is,
it is a new verb, not a change to this one.

## 2. The seam

```go
// server/internal/archive/store.go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader) error   // immutable write
    List(ctx context.Context, prefix string) ([]string, error)
    Delete(ctx context.Context, prefix string) error          // recursive, for purge
}

type Getter interface {                                       // optional capability
    Get(ctx context.Context, key string) (io.ReadCloser, error)
}
```

`Store` is §5.10 verbatim. `Getter` is the one addition WP10 made, because the manifest is a
document the archiver appends to and the restore path reads back; both the filesystem store and
an S3-compatible one implement it (`GetObject` is no less fundamental than `PutObject`).

An R2 implementation is a type satisfying both. Nothing else in the tree changes:
`archive.Archiver`, `archive.Restore`, `adminapi`, `catlogctl` and the identity purge seam all
take a `Store` and never ask what is behind it.

## 3. Key layout — identical, deliberately

```
players/<b64u(user_key)>/chunks/<firstseq>-<lastseq>.ndjson.zst
players/<b64u(user_key)>/manifest.json
```

The same strings the filesystem store writes as paths become object keys. This is why the
layout was fixed before any of it was implemented, and why the tests assert it as literal
strings rather than by construction: a migration from disk to R2 is `aws s3 sync` (or `rclone
copy`) of `data/archive/` into the bucket root, and nothing has to be rewritten.

`<b64u(user_key)>` is the license `sub` claim (§4.5.1). Base64url is `[A-Za-z0-9_-]`, so every
key is S3-safe with no escaping and no case-folding surprises.

## 4. What the R2 implementation would be

| Concern | Choice |
|---|---|
| API | S3-compatible. R2 implements the S3 API; there is no Cloudflare-specific SDK to adopt. |
| Library | `github.com/aws/aws-sdk-go-v2` + `.../service/s3`. **Not currently a dependency — adding it is part of that future work package, not this one.** |
| Endpoint | `https://<account_id>.r2.cloudflarestorage.com`, via `s3.Options.BaseEndpoint`. |
| Region | `auto` (R2 ignores it, the SDK requires one). |
| Addressing | Path-style (`UsePathStyle: true`). R2 does not do virtual-host-style buckets. |
| Credentials | **Environment only**: `CATLOG_ARCHIVE_R2_ACCOUNT_ID`, `_BUCKET`, `_ACCESS_KEY_ID`, `_SECRET_ACCESS_KEY`, following §5.3's `CATLOG_*` override convention. Never in `catlogd.toml`, never in the database, never logged (§5.11). The systemd unit (§11) supplies them via `EnvironmentFile=`. |
| Checksums | The chunk's SHA-256 is already in the manifest, so integrity does not depend on the transport. Setting `ChecksumAlgorithm: sha256` on PutObject additionally makes the upload itself verified end to end. |
| Lifecycle rules | **None.** Chunks are immutable and are never rewritten, so there is nothing for a lifecycle rule to expire, transition or clean up. The one deletion the system performs is a purge, and a purge must be immediate rather than eventual — a lifecycle rule would turn "deleted on request" into "deleted within a day", which is not what §4.7 promises. |
| Versioning | **Off.** Object versioning would preserve a purged player's chunks as non-current versions — precisely the data a purge exists to remove. |
| Storage class | Standard. The archive is read during disaster recovery, when latency is the last thing anyone wants to add. |

### Method mapping

| `Store` method | S3 call | Notes |
|---|---|---|
| `Put` | `PutObject` | One call; a chunk is one object. Chunks are bounded by the 100k-events-per-run cap, comfortably under the 5 GiB single-PUT limit — a multipart path is not needed and should not be added speculatively. The filesystem store's write-temp-then-rename becomes free: `PutObject` is already atomic. |
| `List` | `ListObjectsV2` with `Prefix`, paginated | Return keys sorted; `ListObjectsV2` already returns them in lexicographic order, so the sort is a no-op assertion rather than work. |
| `Get` | `GetObject` | Map `NoSuchKey` onto `archive.ErrNotFound` — the archiver reads that as "this player has no manifest yet", so getting it wrong would make every run start a fresh manifest. |
| `Delete` | `ListObjectsV2` + `DeleteObjects` (batches of 1000) | Recursive by prefix. Must succeed on a prefix that holds nothing: a player who never shipped an event still has to be purgeable (§4.7). |

### Key validation stays

`archive.ValidateKey` / `ValidatePrefix` / `ValidateSub` are not filesystem defences that an
object store makes unnecessary. `Delete("")` against S3 means *delete the bucket's contents*,
which is a considerably worse outcome than the filesystem equivalent. Every implementation calls
them first.

## 5. Purge

Unchanged. `identity.Moderator.Purge` calls `ArchivePurger.DeletePlayerArchive(ctx, sub)`, which
is `Archiver.DeletePlayerArchive` → `Store.Delete(ctx, "players/<sub>/")`.

**A failing delete fails the purge**, deliberately: leaving a deleted account's raw event log in
object storage is exactly the thing a purge exists to prevent, and reporting success while it
sat there would be worse than an error an operator can retry. With R2 this means a purge can
fail on a network partition, and the correct operator response is to re-run `catlogctl purge` —
which is idempotent, precisely so that it can be.

## 6. Cost and operational shape

Chunks are written once per player per archive run (nightly per §11), so the write volume is
one PUT per active player per day plus one manifest PUT. R2 has no egress fee, which is what
makes a full restore affordable to actually rehearse rather than merely document.

The manifest is read before every player's chunk write (one GET per active player per run). If
that ever matters, the fix is to cache manifests in memory across a run rather than to change
the format.

## 7. Migration path, when the day comes

1. Add the SDK dependency and `server/internal/archive/r2store.go` implementing `Store` + `Getter`.
2. Add `[archive] backend = "fs" | "r2"` to §5.3's config, defaulting to `fs`. Credentials stay
   in the environment.
3. `rclone copy data/archive/ r2:<bucket>/` — the layout is already the bucket layout.
4. Flip the config, restart, run `catlogctl archive`. The cursor is in `events.db`, not in the
   store, so the first R2 run picks up exactly where the filesystem run stopped.
5. Verify by restoring into a scratch data directory: `catlogctl archive-restore -rebuild`
   against a local copy of the bucket, then compare `player_stat`. That is the same drill the
   WP10 integration test runs, and it is the only evidence that matters.
6. Keep `data/archive/` until a restore from R2 has been rehearsed at least once.

## 8. What is deliberately not designed here

- **Local retention pruning.** Archiving copies (§5.10); when the local log may be truncated is
  a separate decision with its own failure modes.
- **Multi-region or cross-provider replication.** One bucket, one copy, plus the local one.
- **Encryption at rest beyond R2's own.** The archive holds telemetry keyed by `user_key`; it
  contains no email, no IdP subject and no key material (D17, §4.7).
- **Streaming restore from R2.** The restore path reads whole chunks; chunks are bounded by the
  per-run cap, so there is nothing here to stream.
