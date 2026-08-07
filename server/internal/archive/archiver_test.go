package archive

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// fixedClock is what makes a whole run byte-deterministic: the chunk bytes never
// carried a timestamp, but `manifest.updated_at` does, and a determinism test
// that excused one field would not be one.
var fixedClock = func() time.Time { return time.UnixMilli(1_700_000_000_000) }

type fixture struct {
	t      *testing.T
	dir    string
	events *store.Events
	keys   *keys.Set
	store  *FSStore
	arch   *Archiver
}

func newFixture(t *testing.T, maxEvents int) *fixture {
	t.Helper()
	dir := t.TempDir()
	events := testutil.EventsAt(t, filepath.Join(dir, "events.db"))
	st, err := NewFSStore(filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatalf("archive store: %v", err)
	}
	arch, err := New(Options{
		Events: events, Store: st,
		Log: testutil.DiscardLogger(), Now: fixedClock, MaxEvents: maxEvents,
	})
	if err != nil {
		t.Fatalf("archiver: %v", err)
	}
	return &fixture{t: t, dir: dir, events: events, keys: testutil.Keys(t), store: st, arch: arch}
}

// player creates a player and returns its id and sub.
func (f *fixture) player(subject string) (int64, string) {
	f.t.Helper()
	uk := f.keys.UserKey("dev", subject)
	id, err := f.events.EnsurePlayer(f.t.Context(), nil, uk, "dev", 1_600_000_000_000)
	if err != nil {
		f.t.Fatalf("ensure player %s: %v", subject, err)
	}
	return id, uk.B64U()
}

// ship appends n events for a player, in the shape §4.1 describes.
func (f *fixture) ship(playerID int64, n int) {
	f.t.Helper()
	session := testutil.ULID(f.t)
	flight := testutil.ULID(f.t)
	evs := make([]store.Event, 0, n)
	for i := range n {
		evs = append(evs, store.Event{
			ID:        testutil.ULID(f.t),
			FlightID:  flight,
			SessionID: session,
			Type:      "vehicle.staging",
			Ver:       1,
			SimTime:   sql.NullFloat64{Float64: float64(i) * 1.5, Valid: true},
			WallTime:  1_700_000_000_000 + int64(i),
			Payload:   json.RawMessage(fmt.Sprintf(`{"stage_index":%d}`, i)),
		})
	}
	if _, _, err := f.events.InsertEvents(f.t.Context(), nil, playerID, evs); err != nil {
		f.t.Fatalf("insert events: %v", err)
	}
}

func (f *fixture) run() RunResult {
	f.t.Helper()
	res, err := f.arch.Run(f.t.Context())
	if err != nil {
		f.t.Fatalf("archive run: %v", err)
	}
	return res
}

// snapshot reads every object in the archive, so two runs can be compared byte
// for byte.
func (f *fixture) snapshot() map[string][]byte {
	f.t.Helper()
	keys, err := f.store.List(f.t.Context(), "")
	if err != nil {
		f.t.Fatalf("list: %v", err)
	}
	out := map[string][]byte{}
	for _, k := range keys {
		rc, err := f.store.Get(f.t.Context(), k)
		if err != nil {
			f.t.Fatalf("get %s: %v", k, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			f.t.Fatalf("read %s: %v", k, err)
		}
		out[k] = b
	}
	return out
}

func (f *fixture) manifest(sub string) *Manifest {
	f.t.Helper()
	m, err := loadManifest(f.t.Context(), f.store, sub)
	if err != nil {
		f.t.Fatalf("load manifest: %v", err)
	}
	if m == nil {
		f.t.Fatalf("no manifest for %s", subLog(sub))
	}
	return m
}

// chunkLines decompresses a chunk and returns its NDJSON lines.
func (f *fixture) chunkLines(key string) []string {
	f.t.Helper()
	rc, err := f.store.Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	raw, err := decompressChunk(rc)
	if err != nil {
		f.t.Fatalf("decompress %s: %v", key, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// TestArchiveRunIsDeterministic is §12 WP10's first test: the same events must
// produce the same archive, byte for byte.
//
// The cursor is rewound rather than the events re-created, because re-creating
// them would change `recv_time` — and then the test would be asserting that two
// different logs compress the same way, which is not the property anyone wants.
func TestArchiveRunIsDeterministic(t *testing.T) {
	f := newFixture(t, 0)
	ace, _ := f.player("ace")
	bee, _ := f.player("bee")
	f.ship(ace, 20)
	f.ship(bee, 13)

	first := f.run()
	before := f.snapshot()
	if len(before) != 4 { // two chunks, two manifests
		t.Fatalf("archive holds %d objects, want 4: %v", len(before), slices.Sorted(keysOf(before)))
	}

	// Rewind the cursor: the next run sees exactly the same events again. This
	// is also the crash-between-Put-and-cursor path (§5.10), so it doubles as
	// the retry-idempotence test.
	if err := f.events.SetArchiveCursor(t.Context(), nil, 0); err != nil {
		t.Fatal(err)
	}
	second := f.run()
	after := f.snapshot()

	if first.Events != second.Events || first.ToSeq != second.ToSeq {
		t.Errorf("runs disagree: %+v vs %+v", first, second)
	}
	if len(after) != len(before) {
		t.Fatalf("a repeat run changed the object count: %d → %d", len(before), len(after))
	}
	for key, want := range before {
		got, ok := after[key]
		if !ok {
			t.Errorf("%s disappeared on the second run", key)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not byte-identical across runs (%d vs %d bytes)", key, len(want), len(got))
		}
	}

	// And the compressor itself is deterministic, which is what makes the above
	// true rather than accidentally true.
	raw := bytes.Repeat([]byte(`{"a":1}`+"\n"), 500)
	a, err := compressChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := compressChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("compressChunk is not deterministic")
	}
}

func keysOf[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// TestManifestRecordsEveryChunk is the manifest-correctness test: chunk list,
// seq ranges, per-chunk counts, digests and totals, across two runs.
func TestManifestRecordsEveryChunk(t *testing.T) {
	f := newFixture(t, 0)
	ace, aceSub := f.player("ace")
	bee, beeSub := f.player("bee")

	f.ship(ace, 5)
	f.ship(bee, 2)
	first := f.run()

	f.ship(ace, 3)
	second := f.run()

	m := f.manifest(aceSub)
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest does not validate: %v", err)
	}
	if m.Ver != ManifestVer || m.Sub != aceSub || m.PlayerID != ace || m.IdP != "dev" {
		t.Errorf("manifest header = %+v", *m)
	}
	if m.UpdatedAt != fixedClock().UnixMilli() {
		t.Errorf("manifest updated_at = %d", m.UpdatedAt)
	}
	if len(m.Chunks) != 2 {
		t.Fatalf("manifest lists %d chunks, want 2: %+v", len(m.Chunks), m.Chunks)
	}
	if m.Chunks[0].Events != 5 || m.Chunks[1].Events != 3 || m.Events != 8 {
		t.Errorf("counts = %d + %d = %d, want 5 + 3 = 8", m.Chunks[0].Events, m.Chunks[1].Events, m.Events)
	}
	if m.FirstSeq != 1 || m.LastSeq != m.Chunks[1].LastSeq {
		t.Errorf("manifest spans %d-%d, chunks span %d-%d", m.FirstSeq, m.LastSeq, m.Chunks[0].FirstSeq, m.Chunks[1].LastSeq)
	}

	// Every listed chunk exists, is the size and digest recorded, and holds
	// exactly the events the manifest claims — which is what readChunk verifies
	// on the restore path, so running it here is the manifest assertion.
	for _, c := range m.Chunks {
		if c.Key != ChunkKey(aceSub, c.FirstSeq, c.LastSeq) {
			t.Errorf("chunk key %q does not encode its range", c.Key)
		}
		evs, err := readChunk(t.Context(), f.store, c)
		if err != nil {
			t.Errorf("chunk %s: %v", c.Key, err)
			continue
		}
		if int64(len(evs)) != c.Events {
			t.Errorf("chunk %s holds %d events, manifest says %d", c.Key, len(evs), c.Events)
		}
	}

	// The other player is untouched by the second run.
	bm := f.manifest(beeSub)
	if len(bm.Chunks) != 1 || bm.Events != 2 || bm.PlayerID != bee {
		t.Errorf("bee's manifest = %+v", *bm)
	}

	if first.Events != 7 || second.Events != 3 {
		t.Errorf("run event counts = %d, %d; want 7, 3", first.Events, second.Events)
	}
	if len(second.Players) != 1 || second.Players[0].PlayerID != ace {
		t.Errorf("second run touched %+v, want only ace", second.Players)
	}
}

// TestCursorResumesAcrossRuns: an archive pass is capped and resumable, and
// across passes every event is archived exactly once (§5.10).
func TestCursorResumesAcrossRuns(t *testing.T) {
	const perRun = 3
	f := newFixture(t, perRun)
	ace, aceSub := f.player("ace")
	f.ship(ace, 7)

	var (
		runs  []RunResult
		total int64
	)
	for range 4 {
		res := f.run()
		runs = append(runs, res)
		total += res.Events
		if res.Events == 0 {
			break
		}
	}

	if total != 7 {
		t.Errorf("archived %d events across %d runs, want 7", total, len(runs))
	}
	if runs[0].FromSeq != 0 || runs[0].ToSeq != perRun || !runs[0].Truncated {
		t.Errorf("first run = %+v; want seq 0→3 and truncated", runs[0])
	}
	if runs[1].FromSeq != perRun {
		t.Errorf("the second run started at seq %d, not where the first stopped (%d)", runs[1].FromSeq, perRun)
	}
	if last := runs[len(runs)-1]; last.Events != 0 || last.Truncated {
		t.Errorf("the final run = %+v; want an empty, untruncated pass", last)
	}

	// The cursor is what the resumption rides on.
	cursor, err := f.events.ArchiveCursor(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	head, err := f.events.MaxSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cursor != head {
		t.Errorf("cursor is at %d, the log head is %d", cursor, head)
	}

	// Union of the chunks == the whole log, each seq exactly once.
	m := f.manifest(aceSub)
	seen := map[int64]int{}
	for _, c := range m.Chunks {
		evs, err := readChunk(t.Context(), f.store, c)
		if err != nil {
			t.Fatal(err)
		}
		for _, se := range evs {
			seen[se.Seq]++
		}
	}
	if len(seen) != 7 {
		t.Errorf("archive covers %d distinct seqs, want 7", len(seen))
	}
	for seq, n := range seen {
		if n != 1 {
			t.Errorf("seq %d was archived %d times", seq, n)
		}
	}
}

// TestArchivingCopiesAndNeverDeletes is §5.10's explicit rule.
func TestArchivingCopiesAndNeverDeletes(t *testing.T) {
	f := newFixture(t, 0)
	ace, _ := f.player("ace")
	f.ship(ace, 12)

	before, err := f.events.CountEvents(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.run()
	after, err := f.events.CountEvents(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || after != 12 {
		t.Errorf("the log went from %d to %d events; archiving must copy, never delete", before, after)
	}
}

// TestNothingToArchive: the nightly timer runs this on a quiet server every day.
func TestNothingToArchive(t *testing.T) {
	f := newFixture(t, 0)
	res := f.run()
	if res.Events != 0 || res.FromSeq != 0 || res.ToSeq != 0 || len(res.Players) != 0 {
		t.Errorf("empty run = %+v", res)
	}
	if got := f.snapshot(); len(got) != 0 {
		t.Errorf("an empty run wrote %v", got)
	}

	// And again after a run that did copy something.
	ace, _ := f.player("ace")
	f.ship(ace, 2)
	f.run()
	if res := f.run(); res.Events != 0 || res.FromSeq != res.ToSeq {
		t.Errorf("second empty run = %+v", res)
	}
}

// TestChunkLineIsTheWireEnvelopePlusSeq pins the §5.10 line format: the §4.1
// envelope exactly, plus the two underscore-prefixed server fields.
func TestChunkLineIsTheWireEnvelopePlusSeq(t *testing.T) {
	f := newFixture(t, 0)
	ace, aceSub := f.player("ace")
	f.ship(ace, 1)

	// One event with no flight and no sim_t, which is what a session or roster
	// event looks like (§4.1) and the case a naive encoder gets wrong.
	if _, _, err := f.events.InsertEvents(t.Context(), nil, ace, []store.Event{{
		ID:        testutil.ULID(t),
		SessionID: testutil.ULID(t),
		Type:      "session.started",
		Ver:       1,
		WallTime:  1_700_000_000_123,
		Payload:   json.RawMessage(`{"mod_ver":"0.1.0","game_build":"2026.7.3.4826","install":"01J9V5M3E8Z0FAKEULID26CHR"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	f.run()

	m := f.manifest(aceSub)
	lines := f.chunkLines(m.Chunks[0].Key)
	if len(lines) != 2 {
		t.Fatalf("chunk holds %d lines, want 2", len(lines))
	}

	wantKeys := []string{"id", "type", "ver", "flight", "session", "career", "sim_t", "wall_t", "payload", "_seq", "_recv"}
	for i, l := range lines {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		got := slices.Sorted(keysOf(obj))
		want := slices.Sorted(slices.Values(wantKeys))
		if !slices.Equal(got, want) {
			t.Errorf("line %d keys = %v, want %v", i+1, got, want)
		}
	}

	// The staging event carries a flight and a sim_t; the session event carries
	// neither, and says so with null rather than by omission.
	var session struct {
		Flight *string  `json:"flight"`
		SimT   *float64 `json:"sim_t"`
		Seq    int64    `json:"_seq"`
		Recv   int64    `json:"_recv"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &session); err != nil {
		t.Fatal(err)
	}
	if session.Flight != nil || session.SimT != nil {
		t.Errorf("session event has flight=%v sim_t=%v, want both null", session.Flight, session.SimT)
	}
	if session.Seq != 2 || session.Recv == 0 {
		t.Errorf("session event _seq=%d _recv=%d", session.Seq, session.Recv)
	}

	// Round-trip: what was written decodes back to what was stored.
	stored, err := f.events.EventsSince(t.Context(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range lines {
		got, err := decodeLine([]byte(l))
		if err != nil {
			t.Fatalf("decode line %d: %v", i+1, err)
		}
		want := stored[i]
		if got.Seq != want.Seq || got.ID != want.ID || got.Type != want.Type ||
			got.RecvTime != want.RecvTime || got.WallTime != want.WallTime ||
			got.SessionID != want.SessionID || got.FlightID != want.FlightID ||
			got.SimTime != want.SimTime || string(got.Payload) != string(want.Payload) {
			t.Errorf("line %d round-trip: got %+v, want %+v", i+1, got, want)
		}
	}
}

// TestDeletePlayerArchiveRemovesOnlyThatPlayer is the purge seam's own test; the
// end-to-end version lives in the identity package, where the purge path is.
func TestDeletePlayerArchiveRemovesOnlyThatPlayer(t *testing.T) {
	f := newFixture(t, 0)
	ace, aceSub := f.player("ace")
	bee, beeSub := f.player("bee")
	f.ship(ace, 4)
	f.ship(bee, 4)
	f.run()

	if got := len(f.snapshot()); got != 4 {
		t.Fatalf("archive holds %d objects, want 4", got)
	}
	if err := f.arch.DeletePlayerArchive(t.Context(), aceSub); err != nil {
		t.Fatalf("delete player archive: %v", err)
	}

	keys, err := f.store.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if sub, _ := SubFromKey(k); sub == aceSub {
			t.Errorf("%s survived the purge", k)
		}
	}
	if len(keys) != 2 {
		t.Errorf("after purging one of two players the archive holds %v", keys)
	}
	if _, err := loadManifest(t.Context(), f.store, beeSub); err != nil {
		t.Errorf("the other player's manifest was damaged: %v", err)
	}

	// Purging a player with no archive at all succeeds — the common case for
	// an account that never shipped an event.
	if err := f.arch.DeletePlayerArchive(t.Context(), aceSub); err != nil {
		t.Errorf("re-purging: %v", err)
	}
}
