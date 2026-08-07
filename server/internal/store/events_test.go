package store_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// newEvent builds a minimal valid envelope with a fresh ULID.
func newEvent(t *testing.T, typ string) store.Event {
	t.Helper()
	return store.Event{
		ID:        testutil.ULID(t),
		FlightID:  testutil.ULID(t),
		SessionID: testutil.ULID(t),
		Type:      typ,
		Ver:       1,
		SimTime:   sql.NullFloat64{Float64: 12345.678, Valid: true},
		WallTime:  1770000000123,
		Payload:   json.RawMessage(`{"body":"duna"}`),
	}
}

// --- players ---------------------------------------------------------------

func TestEnsurePlayerIsIdempotent(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	uk := set.UserKey("discord", "100000000000000000")

	first, err := e.EnsurePlayer(t.Context(), nil, uk, "discord", 1770000000000)
	if err != nil {
		t.Fatalf("EnsurePlayer: %v", err)
	}
	second, err := e.EnsurePlayer(t.Context(), nil, uk, "discord", 1780000000000)
	if err != nil {
		t.Fatalf("EnsurePlayer (repeat): %v", err)
	}
	if first != second {
		t.Errorf("player_id changed on repeat login: %d then %d", first, second)
	}

	p, err := e.PlayerByUserKey(t.Context(), uk)
	if err != nil {
		t.Fatalf("PlayerByUserKey: %v", err)
	}
	if p.ID != first {
		t.Errorf("player.ID = %d, want %d", p.ID, first)
	}
	if p.UserKey != uk {
		t.Error("user_key did not round-trip through the BLOB column")
	}
	if p.IdP != "discord" {
		t.Errorf("idp = %q, want discord", p.IdP)
	}
	if p.CreatedAt != 1770000000000 {
		t.Errorf("created_at = %d, want the first login's timestamp", p.CreatedAt)
	}
	if p.Banned() {
		t.Error("a fresh player is banned")
	}

	byID, err := e.PlayerByID(t.Context(), first)
	if err != nil {
		t.Fatalf("PlayerByID: %v", err)
	}
	if byID.UserKey != uk {
		t.Error("PlayerByID returned a different player")
	}
}

func TestPlayerNotFound(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)

	if _, err := e.PlayerByUserKey(t.Context(), set.UserKey("discord", "nobody")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := e.PlayerByID(t.Context(), 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetBan(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	id := testutil.Player(t, e, set, "github", "4242")

	if err := e.SetBan(t.Context(), nil, id, 1770000000000, "cheating"); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	p, err := e.PlayerByID(t.Context(), id)
	if err != nil {
		t.Fatalf("PlayerByID: %v", err)
	}
	if !p.Banned() {
		t.Fatal("player is not banned after SetBan")
	}
	if p.BanReason.String != "cheating" {
		t.Errorf("ban_reason = %q, want cheating", p.BanReason.String)
	}

	if err := e.SetBan(t.Context(), nil, id, 0, ""); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if p, err = e.PlayerByID(t.Context(), id); err != nil {
		t.Fatalf("PlayerByID: %v", err)
	}
	if p.Banned() {
		t.Error("player is still banned after unban")
	}
	if p.BanReason.Valid {
		t.Error("ban_reason survived the unban")
	}
}

// --- handles (§4.7) --------------------------------------------------------

func TestClaimHandle(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	if err := e.ClaimHandle(t.Context(), alice, "Whiskers_Prime", 1770000000000); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	h, err := e.HandleByLC(t.Context(), "whiskers_prime")
	if err != nil {
		t.Fatalf("HandleByLC: %v", err)
	}
	if h.Handle != "Whiskers_Prime" {
		t.Errorf("stored handle = %q, want the original casing", h.Handle)
	}
	if h.HandleLC != "whiskers_prime" {
		t.Errorf("handle_lc = %q, want whiskers_prime", h.HandleLC)
	}
	if h.PlayerID != alice {
		t.Errorf("handle owner = %d, want %d", h.PlayerID, alice)
	}

	// Case-insensitive uniqueness: no casing of a claimed handle is available
	// to anyone, including its owner.
	for _, variant := range []string{"whiskers_prime", "WHISKERS_PRIME", "WhIsKeRs_PrImE", "Whiskers_Prime"} {
		t.Run("collision "+variant, func(t *testing.T) {
			if err := e.ClaimHandle(t.Context(), bob, variant, 1770000001000); !errors.Is(err, store.ErrHandleTaken) {
				t.Errorf("err = %v, want ErrHandleTaken", err)
			}
			if err := e.ClaimHandle(t.Context(), alice, variant, 1770000001000); !errors.Is(err, store.ErrHandleTaken) {
				t.Errorf("owner re-claim err = %v, want ErrHandleTaken (first claim is permanent, D9)", err)
			}
		})
	}

	// A failed claim leaves the original owner untouched.
	if h, err = e.HandleByLC(t.Context(), "whiskers_prime"); err != nil {
		t.Fatalf("HandleByLC after collisions: %v", err)
	}
	if h.PlayerID != alice {
		t.Errorf("owner changed to %d after failed claims, want %d", h.PlayerID, alice)
	}

	// Different handles are fine.
	if err := e.ClaimHandle(t.Context(), bob, "mittens", 1770000002000); err != nil {
		t.Fatalf("distinct claim: %v", err)
	}
}

// TestClaimHandleRetired covers the never-recycled rule (D9): once retired, a
// handle is permanently unavailable — to its former owner and to everyone else.
func TestClaimHandleRetired(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	if err := e.ClaimHandle(t.Context(), alice, "Clawdia", 1770000000000); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if retired, err := e.HandleRetired(t.Context(), "clawdia"); err != nil || retired {
		t.Fatalf("HandleRetired before retirement = %v, %v; want false, nil", retired, err)
	}

	if err := e.RetireHandle(t.Context(), nil, "Clawdia", "banned", 1770000005000); err != nil {
		t.Fatalf("RetireHandle: %v", err)
	}

	// The live row is gone…
	if _, err := e.HandleByLC(t.Context(), "clawdia"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("HandleByLC err = %v, want ErrNotFound", err)
	}
	// …and it is on the retired list, under any casing.
	for _, variant := range []string{"clawdia", "CLAWDIA", "ClAwDiA"} {
		retired, err := e.HandleRetired(t.Context(), variant)
		if err != nil {
			t.Fatalf("HandleRetired(%q): %v", variant, err)
		}
		if !retired {
			t.Errorf("HandleRetired(%q) = false, want true", variant)
		}
		if err := e.ClaimHandle(t.Context(), bob, variant, 1770000006000); !errors.Is(err, store.ErrHandleRetired) {
			t.Errorf("claim %q by another player: err = %v, want ErrHandleRetired", variant, err)
		}
		if err := e.ClaimHandle(t.Context(), alice, variant, 1770000006000); !errors.Is(err, store.ErrHandleRetired) {
			t.Errorf("re-claim %q by the former owner: err = %v, want ErrHandleRetired", variant, err)
		}
	}

	// Retiring twice is harmless (ban then purge).
	if err := e.RetireHandle(t.Context(), nil, "clawdia", "purged", 1770000007000); err != nil {
		t.Errorf("second RetireHandle: %v", err)
	}
}

func TestHandlesForPlayer(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	for i, h := range []string{"one", "two", "three"} {
		if err := e.ClaimHandle(t.Context(), alice, h, int64(1770000000000+i)); err != nil {
			t.Fatalf("claim %s: %v", h, err)
		}
	}
	if err := e.ClaimHandle(t.Context(), bob, "bobs", 1770000009000); err != nil {
		t.Fatalf("claim bobs: %v", err)
	}

	got, err := e.HandlesForPlayer(t.Context(), alice)
	if err != nil {
		t.Fatalf("HandlesForPlayer: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("alice has %d handles, want 3", len(got))
	}
	for i, want := range []string{"one", "two", "three"} { // oldest first
		if got[i].Handle != want {
			t.Errorf("handle[%d] = %q, want %q", i, got[i].Handle, want)
		}
	}

	empty := testutil.Player(t, e, set, "github", "nohandles")
	if got, err = e.HandlesForPlayer(t.Context(), empty); err != nil {
		t.Fatalf("HandlesForPlayer (empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d handles for a player with none", len(got))
	}
}

func TestClaimHandleRejectsEmpty(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	id := testutil.Player(t, e, set, "discord", "alice")
	if err := e.ClaimHandle(t.Context(), id, "", 0); err == nil {
		t.Error("empty handle accepted, want an error")
	}
}

// --- events and the dedup index (D19) --------------------------------------

func TestInsertEventsDedup(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	batch := []store.Event{newEvent(t, "flight.started"), newEvent(t, "vehicle.rud"), newEvent(t, "flight.ended")}

	accepted, deduped, err := e.InsertEvents(t.Context(), nil, alice, batch)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if accepted != 3 || deduped != 0 {
		t.Errorf("first insert = %d accepted / %d deduped, want 3/0", accepted, deduped)
	}

	// The whole point of D19: resending is a no-op that reports honestly.
	accepted, deduped, err = e.InsertEvents(t.Context(), nil, alice, batch)
	if err != nil {
		t.Fatalf("replay InsertEvents: %v", err)
	}
	if accepted != 0 || deduped != 3 {
		t.Errorf("replay = %d accepted / %d deduped, want 0/3", accepted, deduped)
	}

	// A partial overlap: two old, one new.
	fresh := newEvent(t, "vehicle.orbit")
	accepted, deduped, err = e.InsertEvents(t.Context(), nil, alice, []store.Event{batch[0], batch[1], fresh})
	if err != nil {
		t.Fatalf("partial InsertEvents: %v", err)
	}
	if accepted != 1 || deduped != 2 {
		t.Errorf("partial = %d accepted / %d deduped, want 1/2", accepted, deduped)
	}

	if n, err := e.CountEvents(t.Context(), alice); err != nil || n != 4 {
		t.Errorf("alice event count = %d (err %v), want 4", n, err)
	}

	// The index is (player_id, event_id): the same event_id under a different
	// player is a different row. Two players legitimately minting the same ULID
	// is vanishingly unlikely, but the index must not conflate them.
	accepted, deduped, err = e.InsertEvents(t.Context(), nil, bob, batch)
	if err != nil {
		t.Fatalf("cross-player InsertEvents: %v", err)
	}
	if accepted != 3 || deduped != 0 {
		t.Errorf("cross-player = %d accepted / %d deduped, want 3/0 (dedup is per player)", accepted, deduped)
	}
	if n, err := e.CountEvents(t.Context(), 0); err != nil || n != 7 {
		t.Errorf("total event count = %d (err %v), want 7", n, err)
	}
}

func TestInsertEventsInTransactionIsAtomic(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")

	sentinel := errors.New("batch rejected late")
	err := e.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		if _, _, err := e.InsertEvents(t.Context(), tx, alice, []store.Event{newEvent(t, "vehicle.rud")}); err != nil {
			return err
		}
		if err := e.InsertBatch(t.Context(), tx, alice, "batch-1", 1, 1770000000000); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel", err)
	}
	if n, err := e.CountEvents(t.Context(), alice); err != nil || n != 0 {
		t.Errorf("events after rollback = %d (err %v), want 0", n, err)
	}
	if seen, err := e.BatchSeen(t.Context(), nil, alice, "batch-1"); err != nil || seen {
		t.Errorf("batch survived rollback: seen = %v (err %v)", seen, err)
	}
}

func TestEventRoundTrip(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")

	withFlight := newEvent(t, "vehicle.impact")
	// A session-scoped event has no flight (§4.1: flight is null for session
	// and roster events).
	noFlight := newEvent(t, "session.started")
	noFlight.FlightID = ids.Zero
	noFlight.SimTime = sql.NullFloat64{}
	noFlight.Payload = nil // must normalize to {}

	if _, _, err := e.InsertEvents(t.Context(), nil, alice, []store.Event{withFlight, noFlight}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := e.EventsSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read back %d events, want 2", len(got))
	}
	if got[0].Seq >= got[1].Seq {
		t.Errorf("seq is not ascending: %d then %d", got[0].Seq, got[1].Seq)
	}

	a := got[0]
	if a.ID != withFlight.ID {
		t.Errorf("event_id = %s, want %s", ids.String(a.ID), ids.String(withFlight.ID))
	}
	if a.FlightID != withFlight.FlightID {
		t.Errorf("flight_id = %s, want %s", ids.String(a.FlightID), ids.String(withFlight.FlightID))
	}
	if a.SessionID != withFlight.SessionID {
		t.Errorf("session_id = %s, want %s", ids.String(a.SessionID), ids.String(withFlight.SessionID))
	}
	if a.Type != "vehicle.impact" || a.Ver != 1 {
		t.Errorf("type/ver = %q/%d, want vehicle.impact/1", a.Type, a.Ver)
	}
	if !a.SimTime.Valid || a.SimTime.Float64 != 12345.678 {
		t.Errorf("sim_time = %+v, want 12345.678", a.SimTime)
	}
	if a.WallTime != 1770000000123 {
		t.Errorf("wall_time = %d", a.WallTime)
	}
	if a.RecvTime == 0 {
		t.Error("recv_time was not set by the server")
	}
	if string(a.Payload) != `{"body":"duna"}` {
		t.Errorf("payload = %s", a.Payload)
	}
	if a.PlayerID != alice {
		t.Errorf("player_id = %d, want %d", a.PlayerID, alice)
	}

	b := got[1]
	if b.FlightID != ids.Zero {
		t.Errorf("a NULL flight_id read back as %s, want the zero ULID", ids.String(b.FlightID))
	}
	if b.SimTime.Valid {
		t.Errorf("a NULL sim_time read back as valid: %+v", b.SimTime)
	}
	if string(b.Payload) != `{}` {
		t.Errorf("nil payload = %s, want {}", b.Payload)
	}
}

func TestEventsSinceAndMaxSeq(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")

	if seq, err := e.MaxSeq(t.Context()); err != nil || seq != 0 {
		t.Errorf("MaxSeq on an empty log = %d (err %v), want 0", seq, err)
	}

	var batch []store.Event
	for range 10 {
		batch = append(batch, newEvent(t, "telemetry.window"))
	}
	if _, _, err := e.InsertEvents(t.Context(), nil, alice, batch); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	maxSeq, err := e.MaxSeq(t.Context())
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	if maxSeq != 10 {
		t.Errorf("MaxSeq = %d, want 10", maxSeq)
	}

	page, err := e.EventsSince(t.Context(), 0, 4)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(page) != 4 || page[0].Seq != 1 || page[3].Seq != 4 {
		t.Fatalf("first page = %d rows, seqs %d..%d; want 4 rows, 1..4", len(page), page[0].Seq, page[len(page)-1].Seq)
	}
	page, err = e.EventsSince(t.Context(), page[3].Seq, 100)
	if err != nil {
		t.Fatalf("EventsSince (resume): %v", err)
	}
	if len(page) != 6 || page[0].Seq != 5 {
		t.Errorf("resume page = %d rows starting at %d; want 6 starting at 5", len(page), page[0].Seq)
	}
	if page, err = e.EventsSince(t.Context(), 10, 100); err != nil || len(page) != 0 {
		t.Errorf("EventsSince past the end = %d rows (err %v), want 0", len(page), err)
	}
	if page, err = e.EventsSince(t.Context(), 0, 0); err != nil || page != nil {
		t.Errorf("EventsSince with limit 0 = %v (err %v), want nil", page, err)
	}
}

func TestInsertEventsRejectsUntypedEvent(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")

	ev := newEvent(t, "")
	if _, _, err := e.InsertEvents(t.Context(), nil, alice, []store.Event{ev}); err == nil {
		t.Error("an event with no type was accepted")
	}
}

// --- batches and streams (§4.5.3 steps 11–12) ------------------------------

func TestBatchReplayShortCircuit(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	const batchID = "01J9V5M3E8Z0FAKEULID26CHR"
	if seen, err := e.BatchSeen(t.Context(), nil, alice, batchID); err != nil || seen {
		t.Fatalf("BatchSeen before insert = %v (err %v), want false", seen, err)
	}
	if err := e.InsertBatch(t.Context(), nil, alice, batchID, 42, 1770000000000); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	if seen, err := e.BatchSeen(t.Context(), nil, alice, batchID); err != nil || !seen {
		t.Errorf("BatchSeen after insert = %v (err %v), want true", seen, err)
	}
	// Batch ids are scoped per player.
	if seen, err := e.BatchSeen(t.Context(), nil, bob, batchID); err != nil || seen {
		t.Errorf("another player's batch id leaked: seen = %v (err %v)", seen, err)
	}
	// Re-inserting is harmless.
	if err := e.InsertBatch(t.Context(), nil, alice, batchID, 42, 1770000001000); err != nil {
		t.Errorf("repeat InsertBatch: %v", err)
	}
}

func TestStreamState(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	sid := testutil.ULID(t)

	if _, found, err := e.StreamState(t.Context(), nil, alice, sid); err != nil || found {
		t.Fatalf("StreamState on a new stream = found %v (err %v), want false", found, err)
	}

	s := store.StreamState{
		PlayerID: alice, SID: sid, JKT: "jkt-1",
		LastSeq: 1, LastBH: "bh-1", UpdatedAt: 1770000000000,
	}
	if err := e.UpsertStreamState(t.Context(), nil, s); err != nil {
		t.Fatalf("UpsertStreamState: %v", err)
	}
	got, found, err := e.StreamState(t.Context(), nil, alice, sid)
	if err != nil || !found {
		t.Fatalf("StreamState = found %v (err %v), want true", found, err)
	}
	if got != s {
		t.Errorf("stream state = %+v, want %+v", got, s)
	}

	// Advancing the chain.
	s.LastSeq, s.LastBH, s.UpdatedAt = 2, "bh-2", 1770000002000
	if err := e.UpsertStreamState(t.Context(), nil, s); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got, _, err = e.StreamState(t.Context(), nil, alice, sid); err != nil {
		t.Fatalf("StreamState: %v", err)
	}
	if got.LastSeq != 2 || got.LastBH != "bh-2" {
		t.Errorf("chain head = seq %d / bh %q, want 2 / bh-2", got.LastSeq, got.LastBH)
	}

	// gap is sticky: once a stream has skipped a seq the marker must survive
	// later well-behaved batches (§4.5.3 step 12, forensics only).
	s.LastSeq, s.LastBH, s.Gap = 5, "bh-5", true
	if err := e.UpsertStreamState(t.Context(), nil, s); err != nil {
		t.Fatalf("upsert with gap: %v", err)
	}
	s.LastSeq, s.LastBH, s.Gap = 6, "bh-6", false
	if err := e.UpsertStreamState(t.Context(), nil, s); err != nil {
		t.Fatalf("upsert after gap: %v", err)
	}
	if got, _, err = e.StreamState(t.Context(), nil, alice, sid); err != nil {
		t.Fatalf("StreamState: %v", err)
	}
	if !got.Gap {
		t.Error("gap marker was cleared by a later batch; it must be sticky")
	}

	// Streams are keyed per (player, sid).
	if _, found, err = e.StreamState(t.Context(), nil, alice, testutil.ULID(t)); err != nil || found {
		t.Errorf("a different sid resolved: found %v (err %v)", found, err)
	}
}

// TestStreamCensus covers the query that finally reads `stream_state.gap`: it
// was written on every commit and read by nothing, which made the §4.5.3 chain
// all cost and no benefit. Gap visibility is the one thing the chain provides,
// so it has to reach /admin/stats.
func TestStreamCensus(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	if c, err := e.StreamCensus(t.Context()); err != nil || c != (store.StreamCensus{}) {
		t.Fatalf("census on an empty server = %+v (err %v), want zeroes", c, err)
	}

	upsert := func(player int64, sid ids.ID, gap bool) {
		t.Helper()
		if err := e.UpsertStreamState(t.Context(), nil, store.StreamState{
			PlayerID: player, SID: sid, JKT: "jkt", LastSeq: 1, LastBH: "bh", Gap: gap, UpdatedAt: 1,
		}); err != nil {
			t.Fatalf("upsert stream state: %v", err)
		}
	}

	upsert(alice, testutil.ULID(t), false)
	upsert(alice, testutil.ULID(t), true)
	upsert(alice, testutil.ULID(t), true) // one player, two gapped streams
	upsert(bob, testutil.ULID(t), false)

	c, err := e.StreamCensus(t.Context())
	if err != nil {
		t.Fatalf("StreamCensus: %v", err)
	}
	want := store.StreamCensus{Total: 4, Gapped: 2, GappedPlayers: 1}
	if c != want {
		t.Errorf("census = %+v, want %+v", c, want)
	}

	// GappedPlayers is what separates "one client churning" from "everybody is
	// losing batches".
	upsert(bob, testutil.ULID(t), true)
	if c, err = e.StreamCensus(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.GappedPlayers != 2 {
		t.Errorf("gapped players = %d, want 2", c.GappedPlayers)
	}
}

// --- credentials -----------------------------------------------------------

func TestCredentials(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")

	c := store.Credential{
		JKT: "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs", PlayerID: alice,
		Handle: "whiskers_prime", LicenseJTI: "lic_01J9V5M3E8Z0FAKEULID26CHR",
		IssuedAt: 1770000000000, ExpiresAt: 1785552000000,
	}
	if err := e.InsertCredential(t.Context(), nil, c); err != nil {
		t.Fatalf("InsertCredential: %v", err)
	}

	got, err := e.CredentialByJKT(t.Context(), c.JKT)
	if err != nil {
		t.Fatalf("CredentialByJKT: %v", err)
	}
	if got.Revoked() {
		t.Error("a fresh credential is revoked")
	}
	if got.Handle != c.Handle || got.LicenseJTI != c.LicenseJTI || got.ExpiresAt != c.ExpiresAt {
		t.Errorf("credential = %+v, want %+v", got, c)
	}
	if _, err := e.CredentialByJKT(t.Context(), "unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown jkt err = %v, want ErrNotFound", err)
	}

	if list, err := e.CredentialsForPlayer(t.Context(), alice); err != nil || len(list) != 1 {
		t.Errorf("CredentialsForPlayer = %d rows (err %v), want 1", len(list), err)
	}
	if jkts, err := e.RevokedJKTs(t.Context()); err != nil || len(jkts) != 0 {
		t.Errorf("RevokedJKTs = %v (err %v), want none", jkts, err)
	}

	if err := e.RevokeCredential(t.Context(), nil, c.JKT, 1770000009000); err != nil {
		t.Fatalf("RevokeCredential: %v", err)
	}
	if got, err = e.CredentialByJKT(t.Context(), c.JKT); err != nil {
		t.Fatalf("CredentialByJKT: %v", err)
	}
	if !got.Revoked() || got.RevokedAt.Int64 != 1770000009000 {
		t.Errorf("revoked_at = %+v, want 1770000009000", got.RevokedAt)
	}
	// A second revoke keeps the first timestamp.
	if err := e.RevokeCredential(t.Context(), nil, c.JKT, 1770000099000); err != nil {
		t.Fatalf("second RevokeCredential: %v", err)
	}
	if got, _ = e.CredentialByJKT(t.Context(), c.JKT); got.RevokedAt.Int64 != 1770000009000 {
		t.Errorf("revoked_at moved to %d on re-revoke", got.RevokedAt.Int64)
	}
	if jkts, err := e.RevokedJKTs(t.Context()); err != nil || len(jkts) != 1 || jkts[0] != c.JKT {
		t.Errorf("RevokedJKTs = %v (err %v), want [%s]", jkts, err, c.JKT)
	}
}

func TestRevokeCredentialsForPlayer(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	alice := testutil.Player(t, e, set, "discord", "alice")
	bob := testutil.Player(t, e, set, "google", "bob")

	for i, owner := range []int64{alice, alice, bob} {
		if err := e.InsertCredential(t.Context(), nil, store.Credential{
			JKT: string(rune('a' + i)), PlayerID: owner, Handle: "h", LicenseJTI: "j",
			IssuedAt: int64(1770000000000 + i), ExpiresAt: 1785552000000,
		}); err != nil {
			t.Fatalf("InsertCredential: %v", err)
		}
	}

	if err := e.RevokeCredentialsForPlayer(t.Context(), nil, alice, 1770000009000); err != nil {
		t.Fatalf("RevokeCredentialsForPlayer: %v", err)
	}
	revoked, err := e.RevokedJKTs(t.Context())
	if err != nil {
		t.Fatalf("RevokedJKTs: %v", err)
	}
	if len(revoked) != 2 {
		t.Errorf("revoked %d credentials, want 2 (bob's must be untouched)", len(revoked))
	}
}

// --- tombstones ------------------------------------------------------------

func TestTombstones(t *testing.T) {
	e := testutil.MemEvents(t)
	set := testutil.Keys(t)
	uk := set.UserKey("discord", "banned-user")

	if err := e.InsertTombstone(t.Context(), nil, store.Tombstone{UserKey: uk, Reason: "banned", At: 1770000000000}); err != nil {
		t.Fatalf("InsertTombstone: %v", err)
	}
	// Repeating a purge keeps the original reason.
	if err := e.InsertTombstone(t.Context(), nil, store.Tombstone{UserKey: uk, Reason: "purged again", At: 1780000000000}); err != nil {
		t.Fatalf("repeat InsertTombstone: %v", err)
	}

	got, err := e.Tombstones(t.Context())
	if err != nil {
		t.Fatalf("Tombstones: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tombstones, want 1", len(got))
	}
	if got[0].UserKey != uk {
		t.Error("user_key did not round-trip")
	}
	if got[0].Reason != "banned" || got[0].At != 1770000000000 {
		t.Errorf("tombstone = %+v, want the original reason and time", got[0])
	}
	var zero keys.UserKey
	if got[0].UserKey == zero {
		t.Error("tombstone user_key is zero")
	}
}
