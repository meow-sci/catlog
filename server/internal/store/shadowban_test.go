package store_test

import (
	"path/filepath"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// insertN stores n fresh events for a player and returns them.
func insertN(t *testing.T, e *store.Events, playerID int64, n int) []store.Event {
	t.Helper()
	evs := make([]store.Event, 0, n)
	for range n {
		evs = append(evs, newEvent(t, "vehicle.staging"))
	}
	accepted, _, err := e.InsertEvents(t.Context(), nil, playerID, evs)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if accepted != n {
		t.Fatalf("accepted %d of %d events", accepted, n)
	}
	return evs
}

// seqs reads back the seq of every live event a player owns, oldest first.
func liveSeqs(t *testing.T, e *store.Events, playerID int64) []int64 {
	t.Helper()
	page, err := e.PlayerEvents(t.Context(), playerID, 0, 1000)
	if err != nil {
		t.Fatalf("PlayerEvents: %v", err)
	}
	out := make([]int64, 0, len(page))
	for i := len(page) - 1; i >= 0; i-- {
		out = append(out, page[i].Seq)
	}
	return out
}

func shadowbanFixture(t *testing.T) (*store.Events, int64, int64) {
	t.Helper()
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	subject := testutil.Player(t, e, set, "discord", "100000000000000010")
	bystander := testutil.Player(t, e, set, "discord", "100000000000000011")
	return e, subject, bystander
}

// TestSeqIsNeverHandedOutTwice is 0004's whole reason to exist: the highest-seq
// row is deleted and the next insert must not reuse its number.
//
// A reused seq is below the projector checkpoint and the archive cursor, so the
// re-issued event would be stored, never folded onto any board and never
// archived — with no error anywhere. Nothing else in the suite would notice.
func TestSeqIsNeverHandedOutTwice(t *testing.T) {
	e, subject, bystander := shadowbanFixture(t)

	insertN(t, e, subject, 3)
	head := liveSeqs(t, e, subject)
	highest := head[len(head)-1]

	// Delete the row holding the highest seq, exactly as a purge would.
	if _, err := e.Writer().ExecContext(t.Context(), `DELETE FROM event WHERE seq = ?`, highest); err != nil {
		t.Fatalf("delete the head row: %v", err)
	}

	insertN(t, e, bystander, 1)
	got := liveSeqs(t, e, bystander)
	if len(got) != 1 {
		t.Fatalf("bystander has %d events, want 1", len(got))
	}
	if got[0] <= highest {
		t.Errorf("seq %d was handed out again after seq %d was deleted", got[0], highest)
	}
}

// TestSeqAllocatorReconcilesAtOpen pins the self-heal: a row written behind the
// allocator's back must not be able to make the next insert collide with it.
func TestSeqAllocatorReconcilesAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	set := testutil.Keys(t)

	first := testutil.EventsAt(t, path)
	subject := testutil.Player(t, first, set, "discord", "100000000000000012")
	insertN(t, first, subject, 2)
	if _, err := first.ShadowbanPlayer(t.Context(), subject, 1770000000000, "abuse"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	// Drag the allocator backwards, simulating a database whose rows arrived by
	// some other route: a restore, a manual repair, an older binary. The
	// withheld rows still own seqs 1 and 2.
	if _, err := first.Writer().ExecContext(t.Context(), `UPDATE event_seq SET next_seq = 1`); err != nil {
		t.Fatalf("rewind the allocator: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	bystander := testutil.Player(t, second, set, "discord", "100000000000000013")
	insertN(t, second, bystander, 2)
	got := liveSeqs(t, second, bystander)
	if len(got) != 2 {
		t.Fatalf("bystander has %d events, want 2 — a row collided with a withheld seq", len(got))
	}
	if got[0] <= 2 {
		t.Errorf("seq %d was allocated over a withheld row", got[0])
	}
}

// TestShadowbanWithholdsAndRestores is the round trip: the events leave the log
// at their original seq, come back at exactly the same one, and nobody else's
// rows move.
func TestShadowbanWithholdsAndRestores(t *testing.T) {
	e, subject, bystander := shadowbanFixture(t)
	insertN(t, e, subject, 4)
	insertN(t, e, bystander, 2)

	before := liveSeqs(t, e, subject)
	bystanderBefore := liveSeqs(t, e, bystander)

	moved, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000000, "harassment")
	if err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}
	if moved != 4 {
		t.Errorf("withheld %d events, want 4", moved)
	}
	if got := liveSeqs(t, e, subject); len(got) != 0 {
		t.Errorf("subject still has %d live events", len(got))
	}
	if got := liveSeqs(t, e, bystander); !equalSeqs(got, bystanderBefore) {
		t.Errorf("bystander's log moved: %v, want %v", got, bystanderBefore)
	}
	if n, err := e.CountWithheldEvents(t.Context(), subject); err != nil || n != 4 {
		t.Errorf("withheld count = %d (err %v), want 4", n, err)
	}

	// A second shadowban is a no-op rather than an error, so the verb is safe
	// to re-run — and moves nothing, because nothing of theirs is live.
	again, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000001, "harassment")
	if err != nil {
		t.Fatalf("second ShadowbanPlayer: %v", err)
	}
	if again != 0 {
		t.Errorf("second shadowban moved %d events, want 0", again)
	}

	restored, err := e.UnshadowbanPlayer(t.Context(), subject)
	if err != nil {
		t.Fatalf("UnshadowbanPlayer: %v", err)
	}
	if restored != 4 {
		t.Errorf("restored %d events, want 4", restored)
	}
	if got := liveSeqs(t, e, subject); !equalSeqs(got, before) {
		t.Errorf("restored seqs = %v, want the originals %v", got, before)
	}
	if on, err := e.Shadowbanned(t.Context(), nil, subject); err != nil || on {
		t.Errorf("player is still shadowbanned after the lift (err %v)", err)
	}
}

// TestShadowbannedIngestIsSilentlyWithheld is the point of the whole feature:
// the batch is accepted with the same counts as anyone else's and lands in the
// other table, so the client cannot tell and the log cannot see it.
func TestShadowbannedIngestIsSilentlyWithheld(t *testing.T) {
	e, subject, _ := shadowbanFixture(t)
	if _, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000000, "abuse"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}

	evs := []store.Event{newEvent(t, "vehicle.rud"), newEvent(t, "vehicle.orbit")}
	accepted, deduped, err := e.InsertEvents(t.Context(), nil, subject, evs)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if accepted != 2 || deduped != 0 {
		t.Errorf("accepted %d deduped %d, want 2 and 0 — the response must not differ", accepted, deduped)
	}
	if got := liveSeqs(t, e, subject); len(got) != 0 {
		t.Errorf("%d withheld events reached the live log", len(got))
	}
	if n, err := e.CountWithheldEvents(t.Context(), subject); err != nil || n != 2 {
		t.Errorf("withheld count = %d (err %v), want 2", n, err)
	}

	// And the resend converges exactly as it does on the live path (D19).
	accepted, deduped, err = e.InsertEvents(t.Context(), nil, subject, evs)
	if err != nil {
		t.Fatalf("InsertEvents (resend): %v", err)
	}
	if accepted != 0 || deduped != 2 {
		t.Errorf("resend: accepted %d deduped %d, want 0 and 2", accepted, deduped)
	}
}

// TestWithheldEventsAreReadableForReview covers the surface the review process
// runs on: the content is still there, newest first, with its payloads intact.
func TestWithheldEventsAreReadableForReview(t *testing.T) {
	e, subject, _ := shadowbanFixture(t)
	insertN(t, e, subject, 3)
	if _, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000000, "under review"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}

	page, err := e.WithheldEvents(t.Context(), subject, 0, 10)
	if err != nil {
		t.Fatalf("WithheldEvents: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("review page has %d rows, want 3", len(page))
	}
	for i := 1; i < len(page); i++ {
		if page[i].Seq >= page[i-1].Seq {
			t.Errorf("review page is not newest-first: %d then %d", page[i-1].Seq, page[i].Seq)
		}
	}
	if string(page[0].Payload) != `{"body":"duna"}` {
		t.Errorf("withheld payload = %s, want the original", page[0].Payload)
	}

	roster, err := e.Shadowbans(t.Context())
	if err != nil {
		t.Fatalf("Shadowbans: %v", err)
	}
	if len(roster) != 1 || roster[0].PlayerID != subject || roster[0].Events != 3 {
		t.Errorf("roster = %+v, want one row for player %d with 3 events", roster, subject)
	}
	if roster[0].Reason != "under review" {
		t.Errorf("roster reason = %q, want %q", roster[0].Reason, "under review")
	}
}

// TestDeleteWithheldEventsKeepsTheBan is the end of the review: the data goes,
// the ban does not, so anything they ship next is still withheld.
func TestDeleteWithheldEventsKeepsTheBan(t *testing.T) {
	e, subject, _ := shadowbanFixture(t)
	insertN(t, e, subject, 3)
	if _, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000000, "reviewed"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}

	deleted, err := e.DeleteWithheldEvents(t.Context(), subject)
	if err != nil {
		t.Fatalf("DeleteWithheldEvents: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted %d events, want 3", deleted)
	}
	if on, err := e.Shadowbanned(t.Context(), nil, subject); err != nil || !on {
		t.Errorf("the ban was lifted by a delete (err %v)", err)
	}

	insertN(t, e, subject, 1)
	if got := liveSeqs(t, e, subject); len(got) != 0 {
		t.Errorf("%d events reached the live log after a delete", len(got))
	}
	if n, _ := e.CountWithheldEvents(t.Context(), subject); n != 1 {
		t.Errorf("withheld count = %d, want 1", n)
	}

	// A lift now restores nothing, which is the honest outcome: the events were
	// deleted on purpose and there is nothing to give back.
	restored, err := e.UnshadowbanPlayer(t.Context(), subject)
	if err != nil {
		t.Fatalf("UnshadowbanPlayer: %v", err)
	}
	if restored != 1 {
		t.Errorf("restored %d events, want the 1 shipped after the delete", restored)
	}
}

// TestPurgeTakesWithheldEvents: an account deletion that left a copy of the log
// in the other table would be a privacy failure dressed as a feature.
func TestPurgeTakesWithheldEvents(t *testing.T) {
	e, subject, _ := shadowbanFixture(t)
	insertN(t, e, subject, 3)
	if _, err := e.ShadowbanPlayer(t.Context(), subject, 1770000000000, "abuse"); err != nil {
		t.Fatalf("ShadowbanPlayer: %v", err)
	}

	counts, err := e.PurgePlayer(t.Context(), subject)
	if err != nil {
		t.Fatalf("PurgePlayer: %v", err)
	}
	if counts.Withheld != 3 {
		t.Errorf("purge deleted %d withheld events, want 3", counts.Withheld)
	}
	if n, err := e.CountWithheldEvents(t.Context(), subject); err != nil || n != 0 {
		t.Errorf("withheld count after purge = %d (err %v), want 0", n, err)
	}
	if on, err := e.Shadowbanned(t.Context(), nil, subject); err != nil || on {
		t.Errorf("the shadowban roster row survived the purge (err %v)", err)
	}
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
