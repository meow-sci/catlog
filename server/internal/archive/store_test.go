package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/keys"
)

// testSub returns a syntactically real `sub`: b64u of 32 bytes, exactly what a
// license carries (§4.5.1). Anything shorter would be rejected by ValidateSub,
// which is the point of deriving it rather than making one up.
func testSub(t *testing.T, seed byte) string {
	t.Helper()
	var uk keys.UserKey
	for i := range uk {
		uk[i] = seed + byte(i)
	}
	return uk.B64U()
}

func newStore(t *testing.T) *FSStore {
	t.Helper()
	s, err := NewFSStore(filepath.Join(t.TempDir(), "archive"))
	if err != nil {
		t.Fatalf("new fs store: %v", err)
	}
	return s
}

func put(t *testing.T, s *FSStore, key, body string) {
	t.Helper()
	if err := s.Put(t.Context(), key, strings.NewReader(body)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func get(t *testing.T, s *FSStore, key string) string {
	t.Helper()
	rc, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return string(b)
}

func list(t *testing.T, s *FSStore, prefix string) []string {
	t.Helper()
	keys, err := s.List(t.Context(), prefix)
	if err != nil {
		t.Fatalf("list %q: %v", prefix, err)
	}
	return keys
}

// TestKeyLayoutIsTheDocumentedOne pins §5.10's key layout. It is the contract a
// future R2 implementation inherits verbatim, so it is worth asserting as a
// literal string rather than by construction.
func TestKeyLayoutIsTheDocumentedOne(t *testing.T) {
	const sub = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	if got, want := ChunkKey(sub, 1, 250), "players/"+sub+"/chunks/1-250.ndjson.zst"; got != want {
		t.Errorf("ChunkKey = %q, want %q", got, want)
	}
	if got, want := ManifestKey(sub), "players/"+sub+"/manifest.json"; got != want {
		t.Errorf("ManifestKey = %q, want %q", got, want)
	}
	if got, want := PlayerPrefix(sub), "players/"+sub+"/"; got != want {
		t.Errorf("PlayerPrefix = %q, want %q", got, want)
	}
	got, ok := SubFromKey(ChunkKey(sub, 1, 2))
	if !ok || got != sub {
		t.Errorf("SubFromKey(chunk) = %q, %v", got, ok)
	}
}

// TestKeysThatWouldEscapeTheRootAreRefused is the traversal defence. The archive
// path is fed a `sub` that ultimately comes from a license claim, so "it is
// always well-formed" is an assumption worth not making.
func TestKeysThatWouldEscapeTheRootAreRefused(t *testing.T) {
	s := newStore(t)

	for _, key := range []string{
		"", "/etc/passwd", "../outside", "players/../../outside",
		"players/./x", "players//x", `players\x`, "players/x\x00y",
		"players/x/", // a prefix, not a key
	} {
		if err := s.Put(t.Context(), key, strings.NewReader("x")); !errors.Is(err, ErrBadKey) {
			t.Errorf("Put(%q) = %v, want ErrBadKey", key, err)
		}
	}
	for _, prefix := range []string{"", "../", "players/../.."} {
		if err := s.Delete(t.Context(), prefix); !errors.Is(err, ErrBadKey) {
			t.Errorf("Delete(%q) = %v, want ErrBadKey", prefix, err)
		}
	}

	// And a `sub` has to be a real one: 32 bytes of b64u, nothing else.
	for _, sub := range []string{"", "..", "a/b", "short", strings.Repeat("A", 43) + "="} {
		if err := ValidateSub(sub); !errors.Is(err, ErrBadKey) {
			t.Errorf("ValidateSub(%q) = %v, want ErrBadKey", sub, err)
		}
	}
	if err := ValidateSub(testSub(t, 1)); err != nil {
		t.Errorf("a real sub was refused: %v", err)
	}
}

func TestPutGetListDelete(t *testing.T) {
	s := newStore(t)
	a, b := testSub(t, 1), testSub(t, 100)

	put(t, s, ChunkKey(a, 1, 10), "first")
	put(t, s, ChunkKey(a, 11, 20), "second")
	put(t, s, ManifestKey(a), "manifest-a")
	put(t, s, ChunkKey(b, 1, 5), "other player")

	if got := get(t, s, ChunkKey(a, 11, 20)); got != "second" {
		t.Errorf("chunk body = %q", got)
	}

	// List is sorted and prefix-scoped.
	want := []string{ChunkKey(a, 1, 10), ChunkKey(a, 11, 20), ManifestKey(a)}
	slices.Sort(want)
	if got := list(t, s, PlayerPrefix(a)); !slices.Equal(got, want) {
		t.Errorf("List(%q) = %v, want %v", PlayerPrefix(a), got, want)
	}
	if got := list(t, s, ""); len(got) != 4 {
		t.Errorf("List(\"\") = %v, want 4 keys", got)
	}

	// A missing key is ErrNotFound, not a filesystem error.
	if _, err := s.Get(t.Context(), ChunkKey(a, 999, 1000)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}

	// Delete is recursive, scoped, and leaves no empty husk behind.
	if err := s.Delete(t.Context(), PlayerPrefix(a)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := list(t, s, PlayerPrefix(a)); len(got) != 0 {
		t.Errorf("after delete, List = %v, want nothing", got)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "players", a)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the purged player's directory is still there: %v", err)
	}
	if got := list(t, s, PlayerPrefix(b)); len(got) != 1 {
		t.Errorf("deleting one player took another's data: %v", got)
	}

	// Idempotent: a player who never archived anything still has to be purgeable.
	if err := s.Delete(t.Context(), PlayerPrefix(a)); err != nil {
		t.Errorf("deleting an absent prefix failed: %v", err)
	}
	if err := s.Delete(t.Context(), PlayerPrefix(testSub(t, 200))); err != nil {
		t.Errorf("deleting an unknown player's prefix failed: %v", err)
	}
}

// TestPutReplacesAtomically covers the manifest case: chunks are written once,
// but a player's manifest is rewritten on every run, and a half-written one
// would fail every future restore.
func TestPutReplacesAtomically(t *testing.T) {
	s := newStore(t)
	sub := testSub(t, 7)

	put(t, s, ManifestKey(sub), "v1")
	put(t, s, ManifestKey(sub), "v2-longer")
	if got := get(t, s, ManifestKey(sub)); got != "v2-longer" {
		t.Errorf("manifest = %q, want the second write", got)
	}
	if got := list(t, s, PlayerPrefix(sub)); len(got) != 1 {
		t.Errorf("a replace left %v behind — a temporary file leaked", got)
	}
}

// TestListIgnoresInFlightTemporaries makes sure the atomic-write mechanism is
// invisible: a crashed Put must not produce a key a restore would try to read.
func TestListIgnoresInFlightTemporaries(t *testing.T) {
	s := newStore(t)
	sub := testSub(t, 9)
	put(t, s, ChunkKey(sub, 1, 2), "real")

	tmp := filepath.Join(s.Root(), "players", sub, "chunks", ".tmp-1-2.ndjson.zst-1234")
	if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := list(t, s, ""); len(got) != 1 || got[0] != ChunkKey(sub, 1, 2) {
		t.Errorf("List = %v, want only the published chunk", got)
	}
}

// TestListOfAnUntouchedArchiveIsEmpty: a server that has never archived has no
// tree at all, and that is not an error anywhere — least of all on the purge
// path, which lists before it deletes.
func TestListOfAnUntouchedArchiveIsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	s, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if got := list(t, s, PlayersPrefix); len(got) != 0 {
		t.Errorf("List = %v, want nothing", got)
	}
}

func TestOpenFSStoreRequiresAnExistingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := OpenFSStore(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenFSStore(missing) = %v, want a not-exist error", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("OpenFSStore created the directory it was asked to open")
	}
}

// TestPutIsCancellable proves the context actually reaches the copy loop.
func TestPutIsCancellable(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := s.Put(ctx, ChunkKey(testSub(t, 3), 1, 2), bytes.NewReader(make([]byte, 1<<20)))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Put on a cancelled context = %v", err)
	}
}
