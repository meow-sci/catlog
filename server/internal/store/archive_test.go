package store_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// TestArchiveCursorStartsAtZeroAndPersists: the table is created empty, so the
// "never archived" answer has to come from a missing row rather than from a
// seeded one (§5.4, §5.10).
func TestArchiveCursorStartsAtZeroAndPersists(t *testing.T) {
	e := testutil.MemEvents(t)

	got, err := e.ArchiveCursor(t.Context(), nil)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if got != 0 {
		t.Errorf("a fresh database reports cursor %d, want 0", got)
	}

	for _, want := range []int64{7, 42, 42} { // including a no-op re-set
		if err := e.SetArchiveCursor(t.Context(), nil, want); err != nil {
			t.Fatalf("set cursor %d: %v", want, err)
		}
		got, err := e.ArchiveCursor(t.Context(), nil)
		if err != nil {
			t.Fatalf("read cursor: %v", err)
		}
		if got != want {
			t.Errorf("cursor = %d, want %d", got, want)
		}
	}

	if err := e.SetArchiveCursor(t.Context(), nil, -1); err == nil {
		t.Error("a negative cursor was accepted")
	}
}

// TestRestoreEventsPreservesSeqAndDedups is the property the whole
// disaster-recovery path rests on: an archived event goes back at its original
// seq, and a repeat restore changes nothing.
func TestRestoreEventsPreservesSeqAndDedups(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	uk := set.UserKey("dev", "ace")

	if err := e.RestorePlayer(t.Context(), nil, 9, uk, "dev", 1_600_000_000_000); err != nil {
		t.Fatalf("restore player: %v", err)
	}
	p, err := e.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("player: %v", err)
	}
	if p.ID != 9 {
		t.Errorf("restored player_id = %d, want the archived 9", p.ID)
	}

	// Seqs with a hole in them: a purge deletes rows, so a real archive is not
	// contiguous, and a restore must not renumber to close the gap.
	evs := []store.StoredEvent{
		storedEvent(t, 4, "vehicle.staging"),
		storedEvent(t, 5, "vehicle.rud"),
		storedEvent(t, 11, "flight.ended"),
	}
	inserted, deduped, err := e.RestoreEvents(t.Context(), nil, 9, evs)
	if err != nil {
		t.Fatalf("restore events: %v", err)
	}
	if inserted != 3 || deduped != 0 {
		t.Errorf("first restore = %d new, %d deduped", inserted, deduped)
	}

	back, err := e.EventsSince(t.Context(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 || back[0].Seq != 4 || back[1].Seq != 5 || back[2].Seq != 11 {
		t.Fatalf("restored seqs = %v, want 4, 5, 11", seqs(back))
	}

	inserted, deduped, err = e.RestoreEvents(t.Context(), nil, 9, evs)
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if inserted != 0 || deduped != 3 {
		t.Errorf("second restore = %d new, %d deduped; want everything deduped", inserted, deduped)
	}
}

// TestRestoreRefusesToOverwriteSomebodyElse: `INSERT OR IGNORE` cannot tell
// "already here" from "that rowid belongs to someone else", so the restore path
// asks explicitly — and this is the case where getting it wrong would silently
// drop an event during a recovery.
func TestRestoreRefusesToOverwriteSomebodyElse(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)

	mine := testutil.Player(t, e, set, "dev", "ace")
	if _, _, err := e.InsertEvents(t.Context(), nil, mine, []store.Event{storedEvent(t, 0, "vehicle.staging").Event}); err != nil {
		t.Fatal(err)
	}

	theirs := testutil.Player(t, e, set, "dev", "bee")
	_, _, err := e.RestoreEvents(t.Context(), nil, theirs, []store.StoredEvent{storedEvent(t, 1, "vehicle.rud")})
	if !errors.Is(err, store.ErrSeqConflict) {
		t.Errorf("restoring onto an occupied seq = %v, want ErrSeqConflict", err)
	}

	// And a player_id already held by a different account.
	other := set.UserKey("dev", "clawdia")
	if err := e.RestorePlayer(t.Context(), nil, mine, other, "dev", 0); !errors.Is(err, store.ErrPlayerConflict) {
		t.Errorf("restoring onto an occupied player_id = %v, want ErrPlayerConflict", err)
	}
}

func storedEvent(t *testing.T, seq int64, typ string) store.StoredEvent {
	t.Helper()
	return store.StoredEvent{
		Seq:      seq,
		RecvTime: 1_700_000_000_500,
		Event: store.Event{
			ID:        testutil.ULID(t),
			SessionID: testutil.ULID(t),
			Type:      typ,
			Ver:       1,
			WallTime:  1_700_000_000_000,
			Payload:   json.RawMessage(`{}`),
		},
	}
}

func seqs(evs []store.StoredEvent) []int64 {
	out := make([]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}
