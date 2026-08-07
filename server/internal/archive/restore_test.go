package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/seed"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// statRow is a `player_stat` row as a comparable value. Every column is here on
// purpose — `updated_seq` and `context` are exactly the fields that would drift
// if a restore re-minted seq numbers, and a comparison that dropped them would
// pass while the disaster-recovery path was quietly wrong.
type statRow struct {
	PlayerID   int64
	Stat       string
	Value      float64
	Context    string
	UpdatedSeq int64
}

func (r statRow) String() string {
	return fmt.Sprintf("player=%d stat=%s value=%v seq=%d context=%s", r.PlayerID, r.Stat, r.Value, r.UpdatedSeq, r.Context)
}

// readStats dumps every player_stat row in a stable order.
func readStats(t *testing.T, live *projector.Live) []statRow {
	t.Helper()
	var out []statRow
	err := live.With(func(p *store.Projections) error {
		rows, err := p.Reader().QueryContext(t.Context(),
			`SELECT player_id, stat, value, context, updated_seq FROM player_stat ORDER BY player_id, stat`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r   statRow
				ctx []byte
			)
			if err := rows.Scan(&r.PlayerID, &r.Stat, &r.Value, &ctx, &r.UpdatedSeq); err != nil {
				return err
			}
			r.Context = string(ctx)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	return out
}

// buildProjections folds an events database into a fresh projections database
// and returns the resulting player_stat rows, via the same rebuild the nightly
// job runs (§5.6).
func buildProjections(t *testing.T, events *store.Events, dir string) []statRow {
	t.Helper()
	projections := testutil.ProjectionsAt(t, filepath.Join(dir, "projections.db"))
	d := directory.New(events)
	if err := d.Reload(t.Context()); err != nil {
		t.Fatalf("load directory: %v", err)
	}
	live := projector.NewLive(projections)
	p, err := projector.New(projector.Options{
		Events: events, Live: live, Directory: d,
		StoreOptions: testutil.Options(), Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatalf("build projector: %v", err)
	}
	if _, err := p.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return readStats(t, live)
}

// TestRestoreRoundTripsAndRebuildsIdenticalProjections is the WP10 test that
// makes the disaster-recovery story real: archive a populated server, throw the
// server away, replay the archive into an empty database, rebuild, and get the
// same `player_stat` rows back — same players, same values, same contexts, same
// `updated_seq`.
//
// Nothing here mocks the middle. The seeded dataset goes through the real
// archiver, the real zstd chunks on a real filesystem store, the real restore
// and the real §5.6 rebuild.
func TestRestoreRoundTripsAndRebuildsIdenticalProjections(t *testing.T) {
	// --- the original server ---
	origDir := t.TempDir()
	orig := testutil.EventsAt(t, filepath.Join(origDir, "events.db"))
	keySet := testutil.KeysAt(t, origDir)

	if _, err := seed.Apply(t.Context(), orig, keySet, 1_700_000_000_000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := buildProjections(t, orig, origDir)
	if len(want) == 0 {
		t.Fatal("the seeded dataset produced no player_stat rows; the test would prove nothing")
	}

	archiveRoot := filepath.Join(origDir, "archive")
	st, err := NewFSStore(archiveRoot)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Events: orig, Store: st, Log: testutil.DiscardLogger(), Now: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Run(t.Context())
	if err != nil {
		t.Fatalf("archive run: %v", err)
	}
	origEvents, err := orig.CountEvents(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Events != origEvents {
		t.Fatalf("archived %d of %d events", run.Events, origEvents)
	}
	if len(run.Players) != len(seed.Handles()) {
		t.Fatalf("archived %d players, want %d", len(run.Players), len(seed.Handles()))
	}

	// --- the disaster: a brand new, empty data directory ---
	freshDir := t.TempDir()
	fresh := testutil.EventsAt(t, filepath.Join(freshDir, "events.db"))

	// Deliberately a *different* key set. The archive carries user_key bytes, not
	// a derivation of them, so a restore must not need the original pepper — and
	// after a total loss it will not have one.
	_ = testutil.KeysAt(t, freshDir)

	src, err := OpenFSStore(archiveRoot)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(t.Context(), fresh, src, testutil.DiscardLogger())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Events != origEvents || res.Inserted != origEvents || res.Deduped != 0 {
		t.Errorf("restore = %d events (%d new, %d deduped), want %d new", res.Events, res.Inserted, res.Deduped, origEvents)
	}
	if res.Cursor != res.LastSeq || res.LastSeq != run.ToSeq {
		t.Errorf("restore left the cursor at %d (last seq %d), the archive ended at %d", res.Cursor, res.LastSeq, run.ToSeq)
	}

	// --- the proof ---
	got := buildProjections(t, fresh, freshDir)
	compareStats(t, want, got)

	// The event log itself came back whole, at the same seq numbers.
	assertSameLog(t, orig, fresh)

	// A restored archive re-archives to byte-identical chunks, because seq and
	// recv_time survived the round trip. That is a stronger statement than the
	// determinism test alone: it says the format has no server-local state in it.
	freshArchive := filepath.Join(freshDir, "archive")
	fs2, err := NewFSStore(freshArchive)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := New(Options{Events: fresh, Store: fs2, Log: testutil.DiscardLogger(), Now: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.SetArchiveCursor(t.Context(), nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Run(t.Context()); err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	assertSameTree(t, archiveRoot, freshArchive)
}

func compareStats(t *testing.T, want, got []statRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("restored projections hold %d player_stat rows, the original held %d\n original: %v\n restored: %v",
			len(got), len(want), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("player_stat row %d differs:\n original: %s\n restored: %s", i, want[i], got[i])
		}
	}
	t.Logf("%d player_stat rows matched exactly after restore + rebuild", len(want))
}

// assertSameLog compares two event logs row for row, seq included.
func assertSameLog(t *testing.T, a, b *store.Events) {
	t.Helper()
	left, err := a.EventsSince(t.Context(), 0, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.EventsSince(t.Context(), 0, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != len(right) {
		t.Fatalf("the restored log holds %d events, the original held %d", len(right), len(left))
	}
	for i := range left {
		l, r := left[i], right[i]
		if l.Seq != r.Seq || l.PlayerID != r.PlayerID || l.ID != r.ID || l.Type != r.Type ||
			l.Ver != r.Ver || l.WallTime != r.WallTime || l.RecvTime != r.RecvTime ||
			l.SimTime != r.SimTime || l.FlightID != r.FlightID || l.SessionID != r.SessionID ||
			!json.Valid(r.Payload) || string(l.Payload) != string(r.Payload) {
			t.Fatalf("event %d differs:\n original: %+v\n restored: %+v", i, l, r)
		}
	}
}

// assertSameTree compares two archive trees byte for byte.
func assertSameTree(t *testing.T, a, b string) {
	t.Helper()
	walk := func(root string) map[string]string {
		out := map[string]string{}
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = string(body)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		return out
	}
	left, right := walk(a), walk(b)
	if len(left) != len(right) {
		t.Fatalf("re-archiving a restored log produced %d objects, the original had %d", len(right), len(left))
	}
	for key, want := range left {
		got, ok := right[key]
		if !ok {
			t.Errorf("%s is missing from the re-archived tree", key)
			continue
		}
		if got != want {
			t.Errorf("%s is not byte-identical after a restore round trip", key)
		}
	}
}

// TestRestoreIsIdempotent: a restore interrupted and re-run must converge, not
// double-insert and not fail.
func TestRestoreIsIdempotent(t *testing.T) {
	f := newFixture(t, 0)
	ace, _ := f.player("ace")
	bee, _ := f.player("bee")
	f.ship(ace, 6)
	f.ship(bee, 4)
	f.run()

	target := testutil.EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
	first, err := Restore(t.Context(), target, f.store, testutil.DiscardLogger())
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	second, err := Restore(t.Context(), target, f.store, testutil.DiscardLogger())
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if first.Inserted != 10 || first.Deduped != 0 {
		t.Errorf("first restore = %d new, %d deduped", first.Inserted, first.Deduped)
	}
	if second.Inserted != 0 || second.Deduped != 10 {
		t.Errorf("second restore = %d new, %d deduped; want everything deduped", second.Inserted, second.Deduped)
	}
	n, err := target.CountEvents(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("the target holds %d events after two restores, want 10", n)
	}
}

// TestRestoreRefusesToMergeIntoAPopulatedLog. The seqs would collide with
// somebody else's events, and silently dropping either side is the failure mode
// a disaster-recovery tool must not have.
func TestRestoreRefusesToMergeIntoAPopulatedLog(t *testing.T) {
	f := newFixture(t, 0)
	ace, _ := f.player("ace")
	f.ship(ace, 5)
	f.run()

	// A different server, with its own history at the same seq numbers.
	other := newFixture(t, 0)
	otherAce, _ := other.player("someone-else")
	other.ship(otherAce, 5)

	_, err := Restore(t.Context(), other.events, f.store, testutil.DiscardLogger())
	if err == nil {
		t.Fatal("restoring into a populated log succeeded")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error = %v, want a conflict", err)
	}
}

// TestRestoreVerifiesChunksBeforeTrustingThem: a corrupted or truncated chunk
// must stop the restore, not be replayed in part. This runs at exactly the
// moment nobody is in a position to notice the difference.
func TestRestoreVerifiesChunksBeforeTrustingThem(t *testing.T) {
	tamper := map[string]func(t *testing.T, root, sub string){
		"corrupted chunk body": func(t *testing.T, root, sub string) {
			path := filepath.Join(root, filepath.FromSlash(ChunkKey(sub, 1, 5)))
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			body[len(body)/2] ^= 0xff
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"deleted chunk": func(t *testing.T, root, sub string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(ChunkKey(sub, 1, 5)))); err != nil {
				t.Fatal(err)
			}
		},
		"missing manifest": func(t *testing.T, root, sub string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(ManifestKey(sub)))); err != nil {
				t.Fatal(err)
			}
		},
		"unlisted extra chunk": func(t *testing.T, root, sub string) {
			path := filepath.Join(root, filepath.FromSlash(ChunkKey(sub, 900, 901)))
			if err := os.WriteFile(path, []byte("not a chunk"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"manifest count inflated": func(t *testing.T, root, sub string) {
			path := filepath.Join(root, filepath.FromSlash(ManifestKey(sub)))
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(string(body), `"events": 5`, `"events": 6`, 2)
			if edited == string(body) {
				t.Fatalf("the manifest did not contain the counts this test edits:\n%s", body)
			}
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, break_ := range tamper {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 0)
			ace, sub := f.player("ace")
			f.ship(ace, 5)
			f.run()

			break_(t, f.store.Root(), sub)

			target := testutil.EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
			if _, err := Restore(t.Context(), target, f.store, testutil.DiscardLogger()); err == nil {
				t.Error("the restore accepted a damaged archive")
			}
		})
	}
}

// TestRestoreOfAnEmptyArchiveSaysSo rather than reporting a successful restore
// of nothing, which is the single most dangerous thing this verb could print.
func TestRestoreOfAnEmptyArchiveSaysSo(t *testing.T) {
	st, err := NewFSStore(filepath.Join(t.TempDir(), "archive"))
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
	if _, err := Restore(t.Context(), target, st, testutil.DiscardLogger()); err == nil {
		t.Error("restoring an empty archive reported success")
	}
}

// TestRestoreNeedsAReadableStore documents the Getter seam: a write-only store
// can be archived to but not restored from, and says which.
func TestRestoreNeedsAReadableStore(t *testing.T) {
	target := testutil.EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
	_, err := Restore(t.Context(), target, writeOnlyStore{}, testutil.DiscardLogger())
	if err == nil || !strings.Contains(err.Error(), "cannot read objects back") {
		t.Errorf("error = %v", err)
	}
}

type writeOnlyStore struct{}

func (writeOnlyStore) Put(context.Context, string, io.Reader) error   { return nil }
func (writeOnlyStore) List(context.Context, string) ([]string, error) { return nil, nil }
func (writeOnlyStore) Delete(context.Context, string) error           { return nil }

// TestManifestCarriesWhatARestoreNeeds: the archive holds the raw log (D8), and
// the manifest holds precisely the three player columns that cannot be derived
// from it.
func TestManifestCarriesWhatARestoreNeeds(t *testing.T) {
	f := newFixture(t, 0)
	ace, sub := f.player("ace")
	f.ship(ace, 3)
	f.run()

	m := f.manifest(sub)
	uk, err := keys.ParseUserKey(m.Sub)
	if err != nil {
		t.Fatalf("manifest sub does not decode: %v", err)
	}
	orig, err := f.events.PlayerByID(t.Context(), ace)
	if err != nil {
		t.Fatal(err)
	}
	if uk != orig.UserKey || m.PlayerID != orig.ID || m.IdP != orig.IdP || m.CreatedAt != orig.CreatedAt {
		t.Errorf("manifest player = {%d %s %d}, row = {%d %s %d}",
			m.PlayerID, m.IdP, m.CreatedAt, orig.ID, orig.IdP, orig.CreatedAt)
	}

	target := testutil.EventsAt(t, filepath.Join(t.TempDir(), "events.db"))
	if _, err := Restore(t.Context(), target, f.store, testutil.DiscardLogger()); err != nil {
		t.Fatal(err)
	}
	got, err := target.PlayerByUserKey(t.Context(), orig.UserKey)
	if err != nil {
		t.Fatalf("the restored player is not there: %v", err)
	}
	if got.ID != orig.ID || got.IdP != orig.IdP || got.CreatedAt != orig.CreatedAt {
		t.Errorf("restored player = %+v, want %+v", got, orig)
	}
	// Handles and credentials are identity state and are not archived (D8).
	handles, err := target.HandlesForPlayer(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 0 {
		t.Errorf("a restore brought back %v; only the event log is archived", handles)
	}
}
