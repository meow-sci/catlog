package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/meow-sci/catlog/server/internal/keys"
)

// Store is the archive backend (§5.10). Three operations, deliberately: it is
// the whole surface an R2 implementation would have to provide, and every one of
// them maps onto a single S3 call (PutObject, ListObjectsV2, DeleteObjects).
//
// Keys are slash-separated, never absolute, and never contain a `.` or `..`
// segment — [ValidateKey] is the shared rule, so a filesystem store cannot be
// walked out of and an object store cannot be handed a key the filesystem store
// would have refused.
type Store interface {
	// Put writes r at key. The write is immutable in the sense that matters:
	// an object is never modified in place, so a reader either sees the old
	// bytes or the new ones and never a half-written chunk.
	Put(ctx context.Context, key string, r io.Reader) error
	// List returns every key with the given prefix, sorted. The prefix is a
	// string prefix, not a directory: "players/ab" matches "players/abc/x".
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes everything under prefix, recursively. It is the purge path
	// (§4.7), and it is idempotent: deleting a prefix that holds nothing is not
	// an error, because a player who never shipped an event still has to be
	// purgeable.
	Delete(ctx context.Context, prefix string) error
}

// Getter reads an object back. It is separate from [Store] because §5.10
// specifies the three-method write surface, and only two callers need reads: the
// manifest update (which appends to a document it must first read) and restore.
// Both the filesystem store and an S3-compatible one implement it — GetObject is
// no less fundamental than PutObject — so nothing about the seam is unique to a
// local disk.
type Getter interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ErrNotFound is what a [Getter] returns for a key that is not there.
var ErrNotFound = errors.New("archive: key not found")

// ErrBadKey means a key or prefix would escape the archive root, or is
// otherwise unusable.
var ErrBadKey = errors.New("archive: unusable key")

// --- key layout (§5.10) -------------------------------------------------------

const (
	// PlayersPrefix is the root of the per-player tree. Everything the archiver
	// writes lives under it, which is what lets a future R2 bucket share space
	// with anything else.
	PlayersPrefix = "players/"
	// ChunksSegment separates a player's chunks from their manifest.
	ChunksSegment = "chunks/"
	// ChunkSuffix is the chunk file extension: NDJSON compressed with zstd.
	ChunkSuffix = ".ndjson.zst"
	// ManifestName is the per-player index (§5.10: chunk list + counts).
	ManifestName = "manifest.json"
)

// PlayerPrefix is `players/<sub>/` — the prefix a purge deletes (§4.7) and the
// exact string [identity.ArchivePurger] is handed.
func PlayerPrefix(sub string) string { return PlayersPrefix + sub + "/" }

// ChunkPrefix is `players/<sub>/chunks/`.
func ChunkPrefix(sub string) string { return PlayerPrefix(sub) + ChunksSegment }

// ChunkKey is `players/<sub>/chunks/<firstseq>-<lastseq>.ndjson.zst`.
func ChunkKey(sub string, firstSeq, lastSeq int64) string {
	return ChunkPrefix(sub) + strconv.FormatInt(firstSeq, 10) + "-" + strconv.FormatInt(lastSeq, 10) + ChunkSuffix
}

// ManifestKey is `players/<sub>/manifest.json`.
func ManifestKey(sub string) string { return PlayerPrefix(sub) + ManifestName }

// SubFromKey extracts the `sub` from any key under [PlayersPrefix].
func SubFromKey(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, PlayersPrefix)
	if !ok {
		return "", false
	}
	sub, _, ok := strings.Cut(rest, "/")
	if !ok || sub == "" {
		return "", false
	}
	return sub, true
}

// ValidateSub rejects anything that is not a `b64u(user_key)`.
//
// It is the archive's path-traversal defence and it is deliberately strict:
// every sub the system produces is 32 bytes of HMAC rendered base64url (D17), so
// there is no legitimate value containing a slash, a dot or a control character
// and no reason to accept one.
func ValidateSub(sub string) error {
	if sub == "" {
		return fmt.Errorf("%w: empty sub", ErrBadKey)
	}
	if _, err := keys.ParseUserKey(sub); err != nil {
		return fmt.Errorf("%w: sub is not a b64u user_key: %w", ErrBadKey, err)
	}
	return nil
}

// parseSub decodes a validated sub back into the user_key a restore needs to
// recreate the `player` row.
func parseSub(sub string) (keys.UserKey, error) {
	uk, err := keys.ParseUserKey(sub)
	if err != nil {
		return keys.UserKey{}, fmt.Errorf("%w: %w", ErrBadKey, err)
	}
	return uk, nil
}

// ValidateKey rejects a key that could escape the archive root or that no store
// could represent: absolute paths, `.`/`..` segments, empty segments,
// backslashes and control characters.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrBadKey)
	}
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("%w: %q ends in a slash (that is a prefix, not a key)", ErrBadKey, key)
	}
	return validatePathish(key)
}

// ValidatePrefix is [ValidateKey] for a listing or deletion prefix, which may
// end in a slash and may name a directory rather than an object.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		// Refused on purpose: `Delete("")` would mean "delete the archive", and
		// the one caller of Delete is a purge, which never means that.
		return fmt.Errorf("%w: empty prefix", ErrBadKey)
	}
	return validatePathish(strings.TrimSuffix(prefix, "/"))
}

func validatePathish(s string) error {
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("%w: %q is absolute", ErrBadKey, s)
	}
	if strings.ContainsRune(s, '\\') {
		return fmt.Errorf("%w: %q contains a backslash", ErrBadKey, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q contains a control character", ErrBadKey, s)
		}
	}
	for _, seg := range strings.Split(s, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: %q has an empty path segment", ErrBadKey, s)
		case ".", "..":
			return fmt.Errorf("%w: %q has a %q segment", ErrBadKey, s, seg)
		}
	}
	return nil
}
