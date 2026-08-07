package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// identityFixture builds a player who holds a handle, a credential, some events,
// a batch and a stream — one of everything a purge has to delete.
func identityFixture(t *testing.T) (*store.Events, int64, keys.UserKey) {
	t.Helper()
	e := testutil.MemEvents(t)
	ctx := context.Background()

	var uk keys.UserKey
	for i := range uk {
		uk[i] = byte(i + 1)
	}
	id, err := e.EnsurePlayer(ctx, nil, uk, "discord", 1000)
	if err != nil {
		t.Fatalf("ensure player: %v", err)
	}
	if err := e.ClaimHandle(ctx, id, "Whiskers", 1000); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := e.InsertCredential(ctx, nil, store.Credential{
		JKT: "jkt-1", PlayerID: id, Handle: "Whiskers", LicenseJTI: "lic_1",
		IssuedAt: 1000, ExpiresAt: 9000,
	}); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	evID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.InsertEvents(ctx, nil, id, []store.Event{{
		ID: evID, SessionID: sid, Type: "session.started", Ver: 1,
		WallTime: 1000, Payload: json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if err := e.InsertBatch(ctx, nil, id, "batch-1", 1, 1000); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if err := e.UpsertStreamState(ctx, nil, store.StreamState{
		PlayerID: id, SID: sid, JKT: "jkt-1", LastSeq: 1, LastBH: "bh", UpdatedAt: 1000,
	}); err != nil {
		t.Fatalf("upsert stream state: %v", err)
	}
	return e, id, uk
}

// TestMarkHandleRetiredKeepsOwnership is the ban path: the handle_lc becomes
// unclaimable by anyone, but the row survives so an unban can hand it back
// (D9, §5.9).
func TestMarkHandleRetiredKeepsOwnership(t *testing.T) {
	e, playerID, _ := identityFixture(t)
	ctx := context.Background()

	if err := e.MarkHandleRetired(ctx, nil, "Whiskers", "banned", 2000); err != nil {
		t.Fatalf("MarkHandleRetired: %v", err)
	}
	retired, err := e.HandleRetired(ctx, "whiskers")
	if err != nil {
		t.Fatalf("HandleRetired: %v", err)
	}
	if !retired {
		t.Error("the handle is not retired")
	}
	// The row is still there and still theirs.
	h, err := e.HandleByLC(ctx, "WHISKERS")
	if err != nil {
		t.Fatalf("HandleByLC: %v", err)
	}
	if h.PlayerID != playerID || h.Handle != "Whiskers" {
		t.Errorf("handle row = %+v, want the original owner and casing", h)
	}
	// Repeating it is a no-op, so ban-then-purge is safe.
	if err := e.MarkHandleRetired(ctx, nil, "whiskers", "purged", 3000); err != nil {
		t.Fatalf("second MarkHandleRetired: %v", err)
	}

	// Un-retiring puts it back.
	if err := e.UnretireHandle(ctx, nil, "WhIsKeRs"); err != nil {
		t.Fatalf("UnretireHandle: %v", err)
	}
	if retired, _ := e.HandleRetired(ctx, "whiskers"); retired {
		t.Error("the handle is still retired after UnretireHandle")
	}
}

// TestUnrevokeCredentialsAt checks the timestamp selector that makes an unban an
// inverse rather than an amnesty.
func TestUnrevokeCredentialsAt(t *testing.T) {
	e, playerID, _ := identityFixture(t)
	ctx := context.Background()

	if err := e.InsertCredential(ctx, nil, store.Credential{
		JKT: "jkt-2", PlayerID: playerID, Handle: "Whiskers", LicenseJTI: "lic_2",
		IssuedAt: 1100, ExpiresAt: 9000,
	}); err != nil {
		t.Fatalf("insert second credential: %v", err)
	}

	// One revoked by the player at 1500, one by a ban at 2000.
	if err := e.RevokeCredential(ctx, nil, "jkt-1", 1500); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := e.RevokeCredentialsForPlayer(ctx, nil, playerID, 2000); err != nil {
		t.Fatalf("revoke for player: %v", err)
	}

	n, err := e.UnrevokeCredentialsAt(ctx, nil, playerID, 2000)
	if err != nil {
		t.Fatalf("UnrevokeCredentialsAt: %v", err)
	}
	if n != 1 {
		t.Errorf("un-revoked %d credentials, want 1", n)
	}
	if c, _ := e.CredentialByJKT(ctx, "jkt-1"); !c.Revoked() {
		t.Error("the self-revoked credential was resurrected")
	}
	if c, _ := e.CredentialByJKT(ctx, "jkt-2"); c.Revoked() {
		t.Error("the ban-revoked credential was not restored")
	}
}

// TestPurgePlayerDeletesEverything is §4.7's purge, counted row by row.
func TestPurgePlayerDeletesEverything(t *testing.T) {
	e, playerID, uk := identityFixture(t)
	ctx := context.Background()

	counts, err := e.PurgePlayer(ctx, playerID)
	if err != nil {
		t.Fatalf("PurgePlayer: %v", err)
	}
	want := store.PurgeCounts{Events: 1, Batches: 1, Streams: 1, Credentials: 1, Handles: 1}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}

	if n, err := e.CountEvents(ctx, playerID); err != nil || n != 0 {
		t.Errorf("events left = %d (err %v)", n, err)
	}
	if _, err := e.CredentialByJKT(ctx, "jkt-1"); err != store.ErrNotFound {
		t.Errorf("credential survived: %v", err)
	}
	if _, err := e.HandleByLC(ctx, "whiskers"); err != store.ErrNotFound {
		t.Errorf("handle survived: %v", err)
	}
	if _, err := e.PlayerByUserKey(ctx, uk); err != store.ErrNotFound {
		t.Errorf("player survived: %v", err)
	}
	if seen, err := e.BatchSeen(ctx, nil, playerID, "batch-1"); err != nil || seen {
		t.Errorf("ingest_batch survived (err %v)", err)
	}

	// Idempotent: purging an already-purged player deletes nothing and errors
	// on nothing, which is what makes a retried admin call safe.
	again, err := e.PurgePlayer(ctx, playerID)
	if err != nil {
		t.Fatalf("second PurgePlayer: %v", err)
	}
	if (again != store.PurgeCounts{}) {
		t.Errorf("second purge deleted %+v, want nothing", again)
	}
}

// TestPurgeDoesNotTouchOtherPlayers is the obvious thing that must never break.
func TestPurgeDoesNotTouchOtherPlayers(t *testing.T) {
	e, playerID, _ := identityFixture(t)
	ctx := context.Background()

	var other keys.UserKey
	for i := range other {
		other[i] = byte(0xF0 - i)
	}
	otherID, err := e.EnsurePlayer(ctx, nil, other, "github", 1000)
	if err != nil {
		t.Fatalf("ensure other player: %v", err)
	}
	if err := e.ClaimHandle(ctx, otherID, "Mittens", 1000); err != nil {
		t.Fatalf("claim: %v", err)
	}
	evID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.InsertEvents(ctx, nil, otherID, []store.Event{{
		ID: evID, SessionID: sid, Type: "session.started", Ver: 1, WallTime: 1000,
	}}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	if _, err := e.PurgePlayer(ctx, playerID); err != nil {
		t.Fatalf("PurgePlayer: %v", err)
	}
	if n, err := e.CountEvents(ctx, otherID); err != nil || n != 1 {
		t.Errorf("the other player's events = %d (err %v), want 1", n, err)
	}
	if _, err := e.HandleByLC(ctx, "mittens"); err != nil {
		t.Errorf("the other player's handle went away: %v", err)
	}
}
