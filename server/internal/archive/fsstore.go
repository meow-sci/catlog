package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Permissions for the archive tree. The archive is raw player telemetry, so it
// is owner-only — the same reasoning as the backup copy (§5.9).
const (
	archiveDirPerm  fs.FileMode = 0o750
	archiveFilePerm fs.FileMode = 0o600
)

// FSStore is the filesystem [Store]: the dev and test implementation, rooted at
// `data/archive/` (§3, §5.10, D8).
//
// It is the only [Store] implementation that exists. R2 is design-only and no
// code in this repository calls it (D8); docs/r2-archive-design.md describes what
// a second implementation would look like, and the answer is "this file, with
// three S3 calls instead of three syscalls".
//
// An FSStore is safe for concurrent use by callers that do not write the same
// key at once. The archiver serialises itself through the admin write mutex, so
// that condition holds.
type FSStore struct {
	root string
}

var (
	_ Store  = (*FSStore)(nil)
	_ Getter = (*FSStore)(nil)
)

// NewFSStore roots a store at dir, creating it if it does not exist.
func NewFSStore(dir string) (*FSStore, error) {
	if dir == "" {
		return nil, errors.New("archive: empty archive directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, archiveDirPerm); err != nil {
		return nil, fmt.Errorf("archive: create %s: %w", abs, err)
	}
	return &FSStore{root: abs}, nil
}

// OpenFSStore roots a store at an existing directory, failing if it is missing.
// Restore uses it: being handed a path that does not exist is a typo worth
// reporting, not an empty archive worth silently creating.
func OpenFSStore(dir string) (*FSStore, error) {
	if dir == "" {
		return nil, errors.New("archive: empty archive directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve %s: %w", dir, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("archive: open %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("archive: %s is not a directory", abs)
	}
	return &FSStore{root: abs}, nil
}

// Root is the absolute directory the store writes into.
func (s *FSStore) Root() string { return s.root }

// path resolves a validated key to an absolute filesystem path. The final
// containment check is belt and braces over [ValidateKey]: cheap, and the
// consequence of a gap is writing outside the data directory.
func (s *FSStore) path(key string) (string, error) {
	p := filepath.Join(s.root, filepath.FromSlash(key))
	if p != s.root && !strings.HasPrefix(p, s.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes the archive root", ErrBadKey, key)
	}
	return p, nil
}

// Put writes r at key, atomically: the bytes land in a temporary file in the
// destination directory and are renamed into place, so a crash mid-write leaves
// either the previous object or nothing — never a truncated chunk that a restore
// would later fail to decompress.
func (s *FSStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	dst, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), archiveDirPerm); err != nil {
		return fmt.Errorf("archive: create %s: %w", filepath.Dir(dst), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return fmt.Errorf("archive: create temporary file for %s: %w", key, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if err := tmp.Chmod(archiveFilePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("archive: chmod %s: %w", key, err)
	}
	if _, err := io.Copy(tmp, readerWithContext(ctx, r)); err != nil {
		tmp.Close()
		return fmt.Errorf("archive: write %s: %w", key, err)
	}
	// Durability before visibility: the rename must not be able to publish a key
	// whose contents are still only in the page cache.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("archive: sync %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("archive: close %s: %w", key, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("archive: publish %s: %w", key, err)
	}
	return nil
}

// Get opens an object, returning [ErrNotFound] for a missing key.
func (s *FSStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("archive: read %s: %w", key, err)
	}
	return f, nil
}

// List returns every key with the given prefix, sorted. Directories are not
// keys, and neither are the in-flight temporary files Put creates.
func (s *FSStore) List(ctx context.Context, prefix string) ([]string, error) {
	if prefix != "" {
		if err := ValidatePrefix(prefix); err != nil {
			return nil, err
		}
	}
	var out []string
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // an archive that has never been written to is empty, not broken
	}
	if err != nil {
		return nil, fmt.Errorf("archive: list %q: %w", prefix, err)
	}
	sort.Strings(out)
	return out, nil
}

// Delete removes everything under prefix, recursively, and prunes the
// directories that become empty as a result.
//
// Deleting nothing is success. That is what makes the purge path (§4.7) work for
// a player who never shipped an event — and it is why a purge can be re-run
// after a partial failure.
func (s *FSStore) Delete(ctx context.Context, prefix string) error {
	if err := ValidatePrefix(prefix); err != nil {
		return err
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	dir, err := s.path(trimmed)
	if err != nil {
		return err
	}

	// The common case: the prefix is a directory, so one RemoveAll does it.
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("archive: delete %q: %w", prefix, err)
		}
		return s.pruneEmptyParents(filepath.Dir(dir))
	}

	// The general case: a partial-name prefix, which S3 semantics allow and a
	// filesystem does not model. Fall back to matching keys individually.
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		p, err := s.path(key)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("archive: delete %s: %w", key, err)
		}
		if err := s.pruneEmptyParents(filepath.Dir(p)); err != nil {
			return err
		}
	}
	return nil
}

// pruneEmptyParents removes now-empty directories up to (but never including)
// the archive root, so a purged player leaves no husk behind.
func (s *FSStore) pruneEmptyParents(dir string) error {
	for dir != s.root && strings.HasPrefix(dir, s.root+string(filepath.Separator)) {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			dir = filepath.Dir(dir)
			continue
		}
		if err != nil {
			return fmt.Errorf("archive: read %s: %w", dir, err)
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("archive: remove %s: %w", dir, err)
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// readerWithContext makes a long copy cancellable. io.Copy has no context, and
// a multi-megabyte chunk written to a slow disk should still notice a shutdown.
func readerWithContext(ctx context.Context, r io.Reader) io.Reader {
	return &ctxReader{ctx: ctx, r: r}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
