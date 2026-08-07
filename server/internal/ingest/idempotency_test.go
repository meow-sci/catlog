package ingest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// This file is the executable form of the idempotency contract in
// docs/ingest-api.md: the key is (server-derived player, client-minted event id),
// and a client may resend anything it is unsure about.
//
// Ingest-level, not store-level, on purpose. The store's own dedup is pinned in
// internal/store (TestInsertEventsDedup, TestBatchReplayShortCircuit); what
// matters here is that a *request* cannot reach past it — that the player half
// of the key is derived from the verified credential and can never be
// influenced by anything a client puts in the body.

// eventLine renders one envelope with an explicit event id, so a test can
// control exactly which events overlap between two batches.
func (r *rig) eventLine(id ids.ID) string {
	r.t.Helper()
	return fmt.Sprintf(
		`{"id":%q,"type":"vehicle.rud","ver":1,"flight":%q,"session":%q,"sim_t":1.5,"wall_t":%d,"payload":{"cause":"ground_impact","speed_ms":62}}`,
		ids.String(id), ids.String(r.sid), ids.String(r.sid), r.now.UnixMilli())
}

// batchOf renders a batch containing exactly these event ids, in order.
func (r *rig) batchOf(eventIDs ...ids.ID) []byte {
	r.t.Helper()
	var b strings.Builder
	for _, id := range eventIDs {
		b.WriteString(r.eventLine(id))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func (r *rig) newEventIDs(n int) []ids.ID {
	r.t.Helper()
	out := make([]ids.ID, n)
	for i := range out {
		out[i] = testutil.ULID(r.t)
	}
	return out
}

func (r *rig) countEvents(playerID int64) int64 {
	r.t.Helper()
	n, err := r.events.CountEvents(r.t.Context(), playerID)
	if err != nil {
		r.t.Fatalf("count events: %v", err)
	}
	return n
}

// --- property 1: the player half of the key is server-derived ---------------

// TestIdempotencyKeyIsServerScoped is the security half of the guarantee: the
// client mints the event id, the server decides whose namespace it lands in,
// and no field a client controls can move that decision.
func TestIdempotencyKeyIsServerScoped(t *testing.T) {
	t.Run("the envelope has no identity field to forge", func(t *testing.T) {
		r := newRig(t)

		// §4.1 rejects unknown envelope keys, which is what makes "there is no
		// player field" a guarantee rather than an omission: a client cannot
		// invent one and hope a future decoder reads it.
		for _, forged := range []string{"player_id", "player", "handle", "sub", "user_key", "jkt"} {
			line := fmt.Sprintf(
				`{"id":%q,"type":"vehicle.rud","ver":1,"flight":null,"session":%q,"sim_t":1.5,"wall_t":%d,"payload":{},%q:9999}`,
				ids.String(testutil.ULID(t)), ids.String(r.sid), r.now.UnixMilli(), forged)
			res, body := r.ship([]byte(line+"\n"), func(o *shipOpts) { o.Seq = 1 })
			wantError(t, res, body, http.StatusBadRequest, authz.CodeMalformedBatch)
			if detail, _ := body["detail"].(string); !strings.Contains(detail, forged) {
				t.Errorf("detail for a forged %q key = %q; it should name the offending key", forged, detail)
			}
		}
	})

	t.Run("a payload that claims another player changes nothing", func(t *testing.T) {
		r := newRig(t)
		victim := r.otherPlayer("victim_cat")

		// Unknown *payload* keys are preserved verbatim (§4.1) — so this one is
		// stored. What must not happen is for it to affect attribution.
		line := fmt.Sprintf(
			`{"id":%q,"type":"vehicle.rud","ver":1,"flight":null,"session":%q,"sim_t":1.5,"wall_t":%d,"payload":{"cause":"collision","player_id":%d,"handle":%q}}`,
			ids.String(testutil.ULID(t)), ids.String(r.sid), r.now.UnixMilli(), victim.PlayerID, victim.Handle)
		res, body := r.ship([]byte(line + "\n"))
		wantStatus(t, res, body, http.StatusOK)

		if n := r.countEvents(victim.PlayerID); n != 0 {
			t.Errorf("the victim's namespace gained %d events from a forged payload", n)
		}
		if n := r.countEvents(r.cred.PlayerID); n != 1 {
			t.Errorf("the shipper's own namespace has %d events, want 1", n)
		}
	})

	t.Run("the same event id under two credentials is two rows", func(t *testing.T) {
		r := newRig(t)
		other := r.otherPlayer("second_cat")
		otherSID := testutil.ULID(t)

		shared := r.newEventIDs(3)
		batch := r.batchOf(shared...)

		res, body := r.ship(batch)
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(3) {
			t.Fatalf("first player: %v", body)
		}

		// Byte-identical events, byte-identical ids, a different credential.
		res, body = r.ship(batch, func(o *shipOpts) { o.As = &other; o.AsSID = otherSID; o.AsSeq = 1 })
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(3) || body["deduped"] != float64(0) {
			t.Fatalf("second player = %v, want accepted 3 — dedup is scoped per player", body)
		}

		if n := r.countEvents(r.cred.PlayerID); n != 3 {
			t.Errorf("player 1 has %d events, want 3", n)
		}
		if n := r.countEvents(other.PlayerID); n != 3 {
			t.Errorf("player 2 has %d events, want 3", n)
		}
		if n := r.countEvents(0); n != 6 {
			t.Errorf("total = %d, want 6", n)
		}
	})

	t.Run("a batch id is scoped per player too", func(t *testing.T) {
		r := newRig(t)
		other := r.otherPlayer("third_cat")
		otherSID := testutil.ULID(t)

		jti := ids.String(testutil.ULID(t))
		res, body := r.ship(r.batch(2), func(o *shipOpts) { o.JTI = jti })
		wantStatus(t, res, body, http.StatusOK)

		// The same batch id from a different credential is a first sighting, not
		// a replay: one player cannot suppress another's writes by guessing ids.
		res, body = r.ship(r.batch(2), func(o *shipOpts) {
			o.JTI = jti
			o.As = &other
			o.AsSID = otherSID
			o.AsSeq = 1
		})
		wantStatus(t, res, body, http.StatusOK)
		if body["replay"] == true {
			t.Fatalf("another player's batch id short-circuited this one: %v", body)
		}
		if body["accepted"] != float64(2) {
			t.Fatalf("body = %v, want accepted 2", body)
		}
	})
}

// --- property 2: a dumb retry of an identical request is a no-op ------------

// TestDumbRetryIsANoOp is the headline promise: resend the identical request as
// many times as you like, get the same answer, store the same rows.
func TestDumbRetryIsANoOp(t *testing.T) {
	r := newRig(t)

	eventIDs := r.newEventIDs(6)
	compressed := testutil.Brotli(t, r.batchOf(eventIDs...))
	jti := ids.String(testutil.ULID(t))

	res, body := r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti })
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(6) || body["deduped"] != float64(0) {
		t.Fatalf("first ship = %v, want accepted 6", body)
	}

	// Byte-for-byte the same request, five more times. The client is a dumb
	// retrier: it never advances its seq because it never saw a response.
	for i := range 5 {
		res, body = r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti; o.Seq = 1 })
		wantStatus(t, res, body, http.StatusOK)
		if body["replay"] != true || body["accepted"] != float64(0) || body["deduped"] != float64(6) {
			t.Fatalf("retry %d = %v, want {accepted:0, deduped:6, replay:true}", i+1, body)
		}
	}

	if n := r.countEvents(r.cred.PlayerID); n != 6 {
		t.Errorf("stored %d events after six identical requests, want 6", n)
	}

	// The stream chain did not move either: a replay is a short-circuit, so it
	// never reaches the stream_state upsert.
	state, found, err := r.events.StreamState(t.Context(), nil, r.cred.PlayerID, r.sid)
	if err != nil || !found {
		t.Fatalf("stream state: found=%v err=%v", found, err)
	}
	if state.LastSeq != 1 {
		t.Errorf("last_seq = %d after five replays, want 1", state.LastSeq)
	}
}

// --- property 3: a partial retry is safe ------------------------------------

// TestPartialRetryStoresOnlyTheNewEvents covers the overlap case: a client that
// lost track of exactly where it got to resends a window that straddles the
// boundary. The union merge (D19) stores the new tail and reports the split
// honestly rather than rejecting the batch or double-counting it.
func TestPartialRetryStoresOnlyTheNewEvents(t *testing.T) {
	r := newRig(t)

	all := r.newEventIDs(6)

	res, body := r.ship(r.batchOf(all[:4]...))
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(4) {
		t.Fatalf("first batch = %v", body)
	}

	// Events 3–6: two the server already has, two it does not.
	res, body = r.ship(r.batchOf(all[2:]...))
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(2) || body["deduped"] != float64(2) {
		t.Fatalf("overlapping batch = %v, want {accepted:2, deduped:2}", body)
	}

	if n := r.countEvents(r.cred.PlayerID); n != 6 {
		t.Errorf("stored %d events, want 6 — the overlap must merge, not duplicate", n)
	}

	// And the whole window again: everything is now a duplicate.
	res, body = r.ship(r.batchOf(all...))
	wantStatus(t, res, body, http.StatusOK)
	if body["accepted"] != float64(0) || body["deduped"] != float64(6) {
		t.Fatalf("full resend = %v, want {accepted:0, deduped:6}", body)
	}
	if n := r.countEvents(r.cred.PlayerID); n != 6 {
		t.Errorf("stored %d events, want 6", n)
	}
}

// --- property 4: a retry with a new batch id --------------------------------

// TestRetryWithANewBatchID is the case a client hits after a timeout it never
// saw the answer to. Two shapes, and they behave differently — which is the one
// rough edge in the guarantee.
func TestRetryWithANewBatchID(t *testing.T) {
	t.Run("a fresh batch id on the next seq deduplicates cleanly", func(t *testing.T) {
		r := newRig(t)
		eventIDs := r.newEventIDs(4)

		res, body := r.ship(r.batchOf(eventIDs...))
		wantStatus(t, res, body, http.StatusOK)

		// A client that *did* see the 200, advanced its chain, and resent anyway.
		res, body = r.ship(r.batchOf(eventIDs...))
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(0) || body["deduped"] != float64(4) {
			t.Fatalf("body = %v, want every event deduped", body)
		}
		if n := r.countEvents(r.cred.PlayerID); n != 4 {
			t.Errorf("stored %d events, want 4", n)
		}
	})

	// The rough edge, pinned so it cannot change silently. A client that times
	// out has NOT seen a response, so it cannot advance `seq`; if it also mints
	// a fresh batch id it misses the step-11 replay short-circuit and lands on
	// step 12, where its unchanged seq reads as a reused one. The events were
	// already safe — a duplicate would have been harmless — but the chain
	// answers 409 anyway.
	//
	// The mod does not hit this: BatchShipper mints its batch id per *body*, so
	// a resend of unchanged bytes carries the id the server already knows. This
	// test exists for third-party clients and for the contract section that
	// tells them so.
	t.Run("a fresh batch id on the same seq forks the stream", func(t *testing.T) {
		r := newRig(t)
		eventIDs := r.newEventIDs(4)
		batch := r.batchOf(eventIDs...)

		res, body := r.ship(batch)
		wantStatus(t, res, body, http.StatusOK)

		res, body = r.ship(batch, func(o *shipOpts) { o.Seq = 1 })
		wantError(t, res, body, http.StatusConflict, authz.CodeStreamFork)

		// Nothing was lost and nothing was duplicated: the documented recovery
		// is a new stream, and the dedup index absorbs the resend.
		fresh := testutil.ULID(t)
		res, body = r.ship(batch, func(o *shipOpts) { o.SID = fresh; o.Seq = 1 })
		wantStatus(t, res, body, http.StatusOK)
		if body["accepted"] != float64(0) || body["deduped"] != float64(4) {
			t.Fatalf("after the fork recovery = %v, want everything deduped", body)
		}
		if n := r.countEvents(r.cred.PlayerID); n != 4 {
			t.Errorf("stored %d events, want 4 — a fork must cost a round trip, never a row", n)
		}
	})

	// The same retry with the batch id *kept* — what the mod actually does — is
	// a clean 200 replay with no fork at all.
	t.Run("keeping the batch id turns the same retry into a replay", func(t *testing.T) {
		r := newRig(t)
		eventIDs := r.newEventIDs(4)
		compressed := testutil.Brotli(t, r.batchOf(eventIDs...))
		jti := ids.String(testutil.ULID(t))

		res, body := r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti })
		wantStatus(t, res, body, http.StatusOK)

		res, body = r.ship(nil, func(o *shipOpts) { o.Body = compressed; o.JTI = jti; o.Seq = 1 })
		wantStatus(t, res, body, http.StatusOK)
		if body["replay"] != true {
			t.Fatalf("body = %v, want a replay short-circuit rather than a fork", body)
		}
	})
}

// --- property 6: the guarantee is index-backed, not in-memory ---------------

// TestIdempotencySurvivesAStoreRestart proves the dedup lives in the database,
// not in process memory: the store is closed, reopened from the same file, and
// the same events are still duplicates.
func TestIdempotencySurvivesAStoreRestart(t *testing.T) {
	path := t.TempDir() + "/events.db"
	set := testutil.Keys(t)

	evs := []store.Event{
		{ID: testutil.ULID(t), SessionID: testutil.ULID(t), Type: "vehicle.rud", Ver: 1, WallTime: 1},
		{ID: testutil.ULID(t), SessionID: testutil.ULID(t), Type: "vehicle.impact", Ver: 1, WallTime: 2},
	}
	const batchID = "01J9V5M3E8Z0FAKEULID26CHR"

	var playerID int64
	func() {
		db, err := store.OpenEvents(t.Context(), path, testutil.Options())
		if err != nil {
			t.Fatalf("open events.db: %v", err)
		}
		defer db.Close()

		playerID = testutil.Player(t, db, set, "discord", "restart_cat")
		accepted, _, err := db.InsertEvents(t.Context(), nil, playerID, evs)
		if err != nil || accepted != 2 {
			t.Fatalf("first insert = %d accepted (err %v), want 2", accepted, err)
		}
		if err := db.InsertBatch(t.Context(), nil, playerID, batchID, 2, 1); err != nil {
			t.Fatalf("insert batch: %v", err)
		}
	}()

	// A new process, a cold cache, no in-memory set of anything.
	db, err := store.OpenEvents(t.Context(), path, testutil.Options())
	if err != nil {
		t.Fatalf("reopen events.db: %v", err)
	}
	defer db.Close()

	seen, err := db.BatchSeen(t.Context(), nil, playerID, batchID)
	if err != nil || !seen {
		t.Errorf("BatchSeen after restart = %v (err %v), want true", seen, err)
	}
	accepted, deduped, err := db.InsertEvents(t.Context(), nil, playerID, evs)
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if accepted != 0 || deduped != 2 {
		t.Errorf("replay after restart = %d accepted / %d deduped, want 0/2", accepted, deduped)
	}
	if n, err := db.CountEvents(t.Context(), playerID); err != nil || n != 2 {
		t.Errorf("stored %d events (err %v), want 2", n, err)
	}
}
