package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// The migration-0003 correctness gates: payloads are zstd-compressed with a
// trained dictionary inside the store's write path and decompressed inside its
// single read seam, so every consumer sees JSON byte-identical to what was
// ingested — across encodings, config flips and restores.

// samplePayloads is one payload per shape that matters: the dominant real
// telemetry.window (a production row, dictionary-friendly), tiny objects the
// encoder cannot shrink, escapes, non-ASCII, and empty.
var samplePayloads = map[string]string{
	"telemetry.window": `{"t0_sim":9560.935867065617,"t1_sim":9565.935867065617,"n":11,"body":"earth","alt_m":{"min":0,"max":255.35389716750115,"mean":44.39081693189453,"last":255.35389716750115},"surface_speed_ms":{"min":0,"max":3.118838035134325,"mean":0.5421799698128816,"last":3.118838035134325},"orbital_speed_ms":{"min":0,"max":3.118838035134325,"mean":0.5421799698128816,"last":3.118838035134325},"accel_ms2":{"min":0,"max":13.67980231499272,"mean":6.066934478368358,"last":13.67980231499272},"peak_g":1.3944752614671478,"max_q_pa":20136.96739784773,"mass_kg_last":11572.076419545007}`,
	"vehicle.staging":  `{"stage_index":3}`,
	"flight.started":   `{"vehicle_name":"Röntgen \"IX\"","body":"earth","crew_count":2,"mass_kg":41000,"part_count":60}`,
	"kitten.tumble":    `{"kid":"k1","name":"Comet — 彗星","speed_ms":8.2,"body":"earth"}`,
	"session.started":  `{}`,
	"vehicle.rud":      `{"parts":[{"name":"tank\n?","id":1},{"name":"engine\t","id":2}],"cause":"aero"}`,
}

func insertSamples(t *testing.T, e *store.Events, playerID int64) map[string]store.Event {
	t.Helper()
	evs := make([]store.Event, 0, len(samplePayloads))
	byType := make(map[string]store.Event, len(samplePayloads))
	for typ, p := range samplePayloads {
		ev := newEvent(t, typ)
		ev.Payload = json.RawMessage(p)
		evs = append(evs, ev)
		byType[typ] = ev
	}
	if _, _, err := e.InsertEvents(t.Context(), nil, playerID, evs); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	return byType
}

// checkPayloads asserts that every read-back payload is byte-identical to what
// went in, matching rows to inserts by event id.
func checkPayloads(t *testing.T, got []store.StoredEvent, want map[string]store.Event) {
	t.Helper()
	seen := 0
	for _, se := range got {
		w, ok := want[se.Type]
		if !ok || w.ID != se.ID {
			continue
		}
		seen++
		if !bytes.Equal(se.Payload, w.Payload) {
			t.Errorf("%s: payload = %s, want %s", se.Type, se.Payload, w.Payload)
		}
	}
	if seen != len(want) {
		t.Errorf("matched %d of %d inserted events", seen, len(want))
	}
}

func TestPayloadCompressionRoundTrip(t *testing.T) {
	e := testutil.Events(t)
	set := testutil.Keys(t)
	pid := testutil.Player(t, e, set, "discord", "100000000000000001")
	want := insertSamples(t, e, pid)

	// All three read paths run through the same seam; prove each anyway.
	since, err := e.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	checkPayloads(t, since, want)

	page, err := e.PlayerEvents(t.Context(), pid, 0, 100)
	if err != nil {
		t.Fatalf("PlayerEvents: %v", err)
	}
	checkPayloads(t, page, want)

	recent, err := e.RecentEvents(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	checkPayloads(t, recent, want)

	// The dictionary-friendly payload must actually be stored compressed —
	// enc = 1 and fewer stored bytes than JSON — or this whole exercise is a
	// no-op that reads back convincingly.
	var enc, stored int
	if err := e.Reader().QueryRowContext(t.Context(),
		`SELECT enc, length(payload) FROM event WHERE type = 'telemetry.window'`).Scan(&enc, &stored); err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	if enc != 1 {
		t.Errorf("telemetry.window enc = %d, want 1", enc)
	}
	if raw := len(samplePayloads["telemetry.window"]); stored >= raw {
		t.Errorf("stored %d B >= raw %d B — not compressed", stored, raw)
	}

	// And a payload compression cannot shrink must fall back to plain text.
	var tinyEnc int
	if err := e.Reader().QueryRowContext(t.Context(),
		`SELECT enc FROM event WHERE type = 'session.started'`).Scan(&tinyEnc); err != nil {
		t.Fatalf("read tiny row: %v", err)
	}
	if tinyEnc != 0 {
		t.Errorf("empty payload enc = %d, want 0 (verbatim fallback)", tinyEnc)
	}
}

// TestMixedEncPageReadsBothEncodings is the lazy-migration gate: rows written
// before 0003 (enc = 0 JSON text) and compressed rows must read back correctly
// side by side in one page, because old rows are never rewritten.
func TestMixedEncPageReadsBothEncodings(t *testing.T) {
	e := testutil.Events(t)
	set := testutil.Keys(t)
	pid := testutil.Player(t, e, set, "discord", "100000000000000002")

	// A legacy row, exactly as a pre-0003 binary would have left it: JSON
	// text, enc defaulted — inserted without naming the column at all.
	legacy := newEvent(t, "vehicle.orbit")
	legacyPayload := `{"phase":"achieved","body":"earth","ap_m":300000,"pe_m":280000}`
	if _, err := e.Writer().ExecContext(t.Context(),
		`INSERT INTO event (event_id, player_id, session_id, type, ver, wall_time, recv_time, payload)
		 VALUES (?, ?, ?, ?, 1, 1, 1, ?)`,
		ids.Bytes(legacy.ID), pid, ids.Bytes(legacy.SessionID), legacy.Type, legacyPayload); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	want := insertSamples(t, e, pid)

	page, err := e.PlayerEvents(t.Context(), pid, 0, 100)
	if err != nil {
		t.Fatalf("PlayerEvents: %v", err)
	}
	if len(page) != len(want)+1 {
		t.Fatalf("page has %d rows, want %d", len(page), len(want)+1)
	}
	checkPayloads(t, page, want)
	for _, se := range page {
		if se.ID == legacy.ID && string(se.Payload) != legacyPayload {
			t.Errorf("legacy payload = %s, want %s", se.Payload, legacyPayload)
		}
	}
}

// TestCompressionOffWritesLegacyRows is the escape hatch gate:
// `[data] compress_payloads = false` must write plain enc = 0 rows, and a log
// written under both settings must read whole.
func TestCompressionOffWritesLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")

	opts := testutil.Options()
	opts.DisablePayloadCompression = true
	off, err := store.OpenEvents(t.Context(), path, opts)
	if err != nil {
		t.Fatalf("open with compression off: %v", err)
	}
	set := testutil.Keys(t)
	pid := testutil.Player(t, off, set, "discord", "100000000000000003")
	wantOff := insertSamples(t, off, pid)

	var n int
	if err := off.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM event WHERE enc != 0`).Scan(&n); err != nil {
		t.Fatalf("count enc rows: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rows compressed with compression off", n)
	}
	if err := off.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Flip the flag on over the same file: new rows compress, old rows stay,
	// and one page serves both.
	on, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen with compression on: %v", err)
	}
	defer on.Close()
	more := newEvent(t, "telemetry.window")
	more.Payload = json.RawMessage(samplePayloads["telemetry.window"])
	if _, _, err := on.InsertEvents(t.Context(), nil, pid, []store.Event{more}); err != nil {
		t.Fatalf("InsertEvents after flip: %v", err)
	}

	page, err := on.PlayerEvents(t.Context(), pid, 0, 100)
	if err != nil {
		t.Fatalf("PlayerEvents: %v", err)
	}
	if len(page) != len(wantOff)+1 {
		t.Fatalf("page has %d rows, want %d", len(page), len(wantOff)+1)
	}
	checkPayloads(t, page, wantOff)
	for _, se := range page {
		if se.ID == more.ID && !bytes.Equal(se.Payload, more.Payload) {
			t.Errorf("post-flip payload = %s, want %s", se.Payload, more.Payload)
		}
	}
}

// twinStores builds two file stores on the same frozen clock — one compressing,
// one not — and inserts the same events into both, returning the shared player
// key set for archive runs.
func twinStores(t *testing.T) (compressed, legacy *store.Events, evs []store.Event) {
	t.Helper()
	now := func() time.Time { return time.UnixMilli(1770000000000) }

	optsC := testutil.Options()
	optsC.Now = now
	compressed, err := store.OpenEvents(t.Context(), filepath.Join(t.TempDir(), "events.db"), optsC)
	if err != nil {
		t.Fatalf("open compressed store: %v", err)
	}
	t.Cleanup(func() { compressed.Close() })

	optsL := optsC
	optsL.DisablePayloadCompression = true
	legacy, err = store.OpenEvents(t.Context(), filepath.Join(t.TempDir(), "events.db"), optsL)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	t.Cleanup(func() { legacy.Close() })

	for typ, p := range samplePayloads {
		ev := newEvent(t, typ)
		ev.Payload = json.RawMessage(p)
		evs = append(evs, ev)
	}
	return compressed, legacy, evs
}

// TestStoredEventsIdenticalAcrossEncodings is the projector gate at the seam
// the projector reads: a fold over a compressed log and a fold over a legacy
// log see the same []StoredEvent, so every projection they build is identical
// by construction.
func TestStoredEventsIdenticalAcrossEncodings(t *testing.T) {
	compressed, legacy, evs := twinStores(t)
	set := testutil.Keys(t)
	uk := set.UserKey("discord", "100000000000000004")

	for _, e := range []*store.Events{compressed, legacy} {
		pid, err := e.EnsurePlayer(t.Context(), nil, uk, "discord", 1770000000000)
		if err != nil {
			t.Fatalf("EnsurePlayer: %v", err)
		}
		if _, _, err := e.InsertEvents(t.Context(), nil, pid, evs); err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
	}

	a, err := compressed.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince (compressed): %v", err)
	}
	b, err := legacy.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince (legacy): %v", err)
	}
	if len(a) == 0 || !reflect.DeepEqual(a, b) {
		t.Errorf("stored events differ across encodings:\ncompressed: %+v\nlegacy: %+v", a, b)
	}

	// Belt and braces: the two logs really are stored differently.
	var nc, nl int
	if err := compressed.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM event WHERE enc = 1`).Scan(&nc); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM event WHERE enc = 1`).Scan(&nl); err != nil {
		t.Fatal(err)
	}
	if nc == 0 || nl != 0 {
		t.Errorf("enc=1 rows: compressed %d (want > 0), legacy %d (want 0)", nc, nl)
	}
}

// TestArchiveChunksIdenticalAcrossEncodings is the archive determinism gate:
// the §5.10 NDJSON chunks are built from store.StoredEvent, decode happens in
// the store, so a compressed-storage log and a legacy log with the same events
// must produce byte-identical archive files.
func TestArchiveChunksIdenticalAcrossEncodings(t *testing.T) {
	compressed, legacy, evs := twinStores(t)
	set := testutil.Keys(t)
	uk := set.UserKey("discord", "100000000000000005")

	dirs := make(map[*store.Events]string)
	for e, dir := range map[*store.Events]string{compressed: t.TempDir(), legacy: t.TempDir()} {
		pid, err := e.EnsurePlayer(t.Context(), nil, uk, "discord", 1770000000000)
		if err != nil {
			t.Fatalf("EnsurePlayer: %v", err)
		}
		if _, _, err := e.InsertEvents(t.Context(), nil, pid, evs); err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
		fs, err := archive.NewFSStore(dir)
		if err != nil {
			t.Fatalf("NewFSStore: %v", err)
		}
		arch, err := archive.New(archive.Options{
			Events: e,
			Store:  fs,
			Log:    testutil.DiscardLogger(),
			Now:    func() time.Time { return time.UnixMilli(1770000000000) },
		})
		if err != nil {
			t.Fatalf("archive.New: %v", err)
		}
		if _, err := arch.Run(t.Context()); err != nil {
			t.Fatalf("archive run: %v", err)
		}
		dirs[e] = dir
	}

	filesA := archiveFiles(t, dirs[compressed])
	filesB := archiveFiles(t, dirs[legacy])
	if len(filesA) == 0 {
		t.Fatal("archive run produced no files")
	}
	if !reflect.DeepEqual(keysOf(filesA), keysOf(filesB)) {
		t.Fatalf("archive file sets differ: %v vs %v", keysOf(filesA), keysOf(filesB))
	}
	for name, a := range filesA {
		if !bytes.Equal(a, filesB[name]) {
			t.Errorf("archive file %s differs between compressed and legacy storage", name)
		}
	}
}

func archiveFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	fs, err := archive.OpenFSStore(dir)
	if err != nil {
		t.Fatalf("OpenFSStore: %v", err)
	}
	keys, err := fs.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		r, err := fs.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("read %s: %v", k, err)
		}
		out[k] = b
	}
	return out
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestRestoreCompressesPayloads: the disaster-recovery path lands the same
// storage win and the same bytes-out as live ingest.
func TestRestoreCompressesPayloads(t *testing.T) {
	source := testutil.Events(t)
	set := testutil.Keys(t)
	uk := set.UserKey("discord", "100000000000000006")
	pid := testutil.Player(t, source, set, "discord", "100000000000000006")
	want := insertSamples(t, source, pid)

	stored, err := source.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}

	target := testutil.Events(t)
	if err := target.RestorePlayer(t.Context(), nil, pid, uk, "discord", 1770000000000); err != nil {
		t.Fatalf("RestorePlayer: %v", err)
	}
	inserted, _, err := target.RestoreEvents(t.Context(), nil, pid, stored)
	if err != nil {
		t.Fatalf("RestoreEvents: %v", err)
	}
	if inserted != len(stored) {
		t.Fatalf("restored %d of %d events", inserted, len(stored))
	}

	back, err := target.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince (restored): %v", err)
	}
	checkPayloads(t, back, want)
	if !reflect.DeepEqual(stored, back) {
		t.Error("restored events differ from the source's")
	}

	var n int
	if err := target.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM event WHERE enc = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("restore stored no compressed rows")
	}
}

// TestPayloadDictPersisted: dictionary v1 is written into payload_dict at
// first open (append-only — a reopen must not touch it), and compressed rows
// survive a close/reopen, i.e. decoding does not secretly depend on process
// state.
func TestPayloadDictPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	e, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	set := testutil.Keys(t)
	pid := testutil.Player(t, e, set, "discord", "100000000000000007")
	want := insertSamples(t, e, pid)

	var created int64
	var dict []byte
	if err := e.Reader().QueryRowContext(t.Context(),
		`SELECT bytes, created_at FROM payload_dict WHERE dict_id = 1`).Scan(&dict, &created); err != nil {
		t.Fatalf("read payload_dict: %v", err)
	}
	// A trained zstd dictionary starts with magic 0xEC30A437 (little-endian).
	if len(dict) != 16384 || !bytes.Equal(dict[:4], []byte{0x37, 0xA4, 0x30, 0xEC}) {
		t.Fatalf("dictionary v1: %d bytes, magic %x", len(dict), dict[:4])
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	re, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer re.Close()

	var dict2 []byte
	var created2 int64
	var rows int
	if err := re.Reader().QueryRowContext(t.Context(),
		`SELECT bytes, created_at FROM payload_dict WHERE dict_id = 1`).Scan(&dict2, &created2); err != nil {
		t.Fatalf("read payload_dict after reopen: %v", err)
	}
	if err := re.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM payload_dict`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || !bytes.Equal(dict, dict2) || created != created2 {
		t.Errorf("payload_dict changed across reopen: rows=%d bytesEqual=%v created %d→%d",
			rows, bytes.Equal(dict, dict2), created, created2)
	}

	page, err := re.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince after reopen: %v", err)
	}
	checkPayloads(t, page, want)
}

// TestEvPlayerIndexPlans records why ev_player KEEPS its seemingly redundant
// seq column (an index entry's implicit rowid tail IS seq, so classic SQLite
// would plan (player_id) identically): tursogo only turns the §4.8 cursor's
// `seq < ?` into an index seek when seq is a named index column. Against
// ev_player(player_id) the EXPLAIN bytecode shows SeekLE on player_id alone
// followed by a per-row `Ge … Prev` filter — page N of a player's history
// would cost N pages, not one. The 4 B/row the slim index would save is not
// worth an O(history) paging scan, so the wide index stays and this test
// pins the seek.
func TestEvPlayerIndexPlans(t *testing.T) {
	e := testutil.Events(t)

	plan := func(q string, args ...any) string {
		rows, err := e.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("explain %s: %v", q, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			out = append(out, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, "; ")
	}

	// The cursor page must seek on BOTH columns — `(player_id=? AND seq<?)`
	// in the plan detail is tursogo saying SeekLT got the seq bound. If this
	// ever degrades to `(player_id=?)` alone, paging went quadratic.
	cursor := plan(`SELECT seq FROM event WHERE player_id = ? AND seq < ? ORDER BY seq DESC LIMIT 10`, 1, 100)
	if !strings.Contains(cursor, "ev_player (player_id=? AND seq<?)") {
		t.Errorf("cursor page does not seek on (player_id, seq): %s", cursor)
	}

	for name, q := range map[string]string{
		"page-first": `SELECT seq FROM event WHERE player_id = ? ORDER BY seq DESC LIMIT 10`,
		"count":      `SELECT count(*) FROM event WHERE player_id = ?`,
	} {
		if p := plan(q, 1); !strings.Contains(p, "ev_player") {
			t.Errorf("%s: plan does not use ev_player: %s", name, p)
		}
	}
}

// TestInsertEventsStillDedupes: compression must not break the D19 idempotent
// union-merge — the dedup index sees (player_id, event_id), never the payload.
func TestInsertEventsDedupesAcrossEncodings(t *testing.T) {
	e := testutil.Events(t)
	set := testutil.Keys(t)
	pid := testutil.Player(t, e, set, "discord", "100000000000000008")

	ev := newEvent(t, "telemetry.window")
	ev.Payload = json.RawMessage(samplePayloads["telemetry.window"])
	if a, d, err := e.InsertEvents(t.Context(), nil, pid, []store.Event{ev}); err != nil || a != 1 || d != 0 {
		t.Fatalf("first insert: accepted=%d deduped=%d err=%v", a, d, err)
	}
	if a, d, err := e.InsertEvents(t.Context(), nil, pid, []store.Event{ev}); err != nil || a != 0 || d != 1 {
		t.Fatalf("resend: accepted=%d deduped=%d err=%v", a, d, err)
	}

	var n int
	if err := e.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM event`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows stored, want 1", n)
	}
}
