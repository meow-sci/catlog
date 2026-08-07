//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// TestIngestIdempotency drives the docs/ingest-api.md idempotency contract
// against a real catlogd over real HTTP: a dumb retrier, a partial resend, a
// second player, and a server restart in the middle.
//
// The unit suite (internal/ingest/idempotency_test.go) proves the same
// properties against the handler; this proves the built binary keeps them,
// including across a process boundary — which is the only way to show that the
// guarantee is index-backed rather than something the writer goroutine happens
// to remember.
func TestIngestIdempotency(t *testing.T) {
	// A generous burst: this suite deliberately hammers the same credential with
	// retries, which is exactly what the §4.3 bucket exists to slow down. The
	// bucket itself is covered by TestIngestRateLimit.
	s := startServer(t, "CATLOG_LIMITS_RATELIMIT_BURST=200")
	cred := s.issue("idempotent_cat")
	sh := newShipper(t, s, cred)

	sessionID := newULID(t)
	eventIDs := newULIDs(t, 6)
	firstFour := batchOf(sessionID, eventIDs[:4])
	overlap := batchOf(sessionID, eventIDs[2:])
	everything := batchOf(sessionID, eventIDs)

	firstJTI := ids.String(newULID(t))

	t.Run("the first batch is stored", func(t *testing.T) {
		res := sh.ship(firstFour, func(o *shipOpts) { o.JTI = firstJTI })
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(4) || res.Body["deduped"] != float64(0) {
			t.Fatalf("body = %v, want accepted 4", res.Body)
		}
	})

	t.Run("a dumb retrier replaying the identical request is a no-op", func(t *testing.T) {
		// Five identical requests. The client never saw a response, so it never
		// advanced its seq and never re-minted its batch id — exactly what the
		// mod's shipper does after a timeout.
		for i := range 5 {
			res := sh.ship(firstFour, func(o *shipOpts) { o.JTI = firstJTI; o.Seq = 1; o.NoAdvace = true })
			if res.Status != http.StatusOK {
				t.Fatalf("retry %d: status %d body %v", i+1, res.Status, res.Body)
			}
			if res.Body["replay"] != true || res.Body["accepted"] != float64(0) {
				t.Fatalf("retry %d body = %v, want a replay short-circuit", i+1, res.Body)
			}
		}
	})

	t.Run("a partial retry stores only the new events", func(t *testing.T) {
		res := sh.ship(overlap)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(2) || res.Body["deduped"] != float64(2) {
			t.Fatalf("body = %v, want {accepted:2, deduped:2}", res.Body)
		}
	})

	t.Run("idempotency survives a server restart", func(t *testing.T) {
		s.restart()

		// Same batch id as the very first request, against a process that has
		// never seen it in this lifetime.
		res := sh.ship(firstFour, func(o *shipOpts) { o.JTI = firstJTI; o.Seq = 1; o.NoAdvace = true })
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["replay"] != true {
			t.Fatalf("body = %v — the batch replay index did not survive the restart", res.Body)
		}

		// And the event-level dedup, under a fresh batch id on the next seq.
		res = sh.ship(everything)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(0) || res.Body["deduped"] != float64(6) {
			t.Fatalf("body = %v — the event dedup index did not survive the restart", res.Body)
		}
	})

	t.Run("a second player is a separate namespace", func(t *testing.T) {
		other := s.issue("collision_cat")
		otherShipper := newShipper(t, s, other)

		// Byte-identical events, byte-identical ids, a different credential.
		res := otherShipper.ship(everything)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %v", res.Status, res.Body)
		}
		if res.Body["accepted"] != float64(6) || res.Body["deduped"] != float64(0) {
			t.Fatalf("body = %v — dedup must be scoped to (player, event_id), not global", res.Body)
		}
	})

	t.Run("a forged identity field in the envelope is rejected", func(t *testing.T) {
		line := fmt.Sprintf(
			`{"id":%q,"type":"vehicle.rud","ver":1,"flight":null,"session":%q,"sim_t":1.5,"wall_t":%d,"payload":{},"player_id":1}`,
			ids.String(newULID(t)), ids.String(sessionID), time.Now().UnixMilli())
		res := sh.ship([]byte(line+"\n"), func(o *shipOpts) { o.NoAdvace = true })
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, body %v, want 400", res.Status, res.Body)
		}
		if res.Body["error"] != authz.CodeMalformedBatch {
			t.Errorf("error = %v, want %s", res.Body["error"], authz.CodeMalformedBatch)
		}
	})

	// Read the rows out of the file once the lock is released.
	s.stop()

	ctx := context.Background()
	db, err := store.OpenEvents(ctx, filepath.Join(s.dataDir, "events.db"), store.Options{})
	if err != nil {
		t.Fatalf("open events.db after shutdown: %v", err)
	}
	defer db.Close()

	c, err := db.CredentialByJKT(ctx, cred.jkt)
	if err != nil {
		t.Fatalf("credential row: %v", err)
	}
	if n, err := db.CountEvents(ctx, c.PlayerID); err != nil || n != 6 {
		t.Errorf("player has %d events (err %v), want 6 after eleven shipments of them", n, err)
	}
	if n, err := db.CountEvents(ctx, 0); err != nil || n != 12 {
		t.Errorf("total = %d (err %v), want 12 — six per player", n, err)
	}
}

// TestStreamGapIsVisibleInAdminStats closes the loop on the §4.5.3 step-12
// chain. A skipped seq is accepted (telemetry is loss-tolerant) and marked
// permanently — but until now nothing read the marker, so the chain's one real
// benefit was unrealised. This proves it reaches an operator.
func TestStreamGapIsVisibleInAdminStats(t *testing.T) {
	s := startServer(t, "CATLOG_LIMITS_RATELIMIT_BURST=100")
	cred := s.issue("gappy_cat")
	sh := newShipper(t, s, cred)

	type streamStats struct {
		Total         int64 `json:"total"`
		Gapped        int64 `json:"gapped"`
		GappedPlayers int64 `json:"gapped_players"`
	}
	read := func() streamStats {
		t.Helper()
		var out struct {
			Streams streamStats `json:"streams"`
		}
		s.adminJSON(t, http.MethodGet, "/admin/stats", &out)
		return out.Streams
	}

	session := newULID(t)
	if res := sh.ship(batchOf(session, newULIDs(t, 2))); res.Status != http.StatusOK {
		t.Fatalf("first batch: %d %v", res.Status, res.Body)
	}
	if got := read(); got != (streamStats{Total: 1}) {
		t.Fatalf("streams = %+v, want one clean stream", got)
	}

	// seq 4 after seq 1: batches 2 and 3 never arrived.
	if res := sh.ship(batchOf(session, newULIDs(t, 2)), func(o *shipOpts) { o.Seq = 4 }); res.Status != http.StatusOK {
		t.Fatalf("gapped batch: %d %v", res.Status, res.Body)
	}
	if got := read(); got != (streamStats{Total: 1, Gapped: 1, GappedPlayers: 1}) {
		t.Fatalf("streams = %+v, want the gap surfaced", got)
	}

	// The marker is sticky, so a well-behaved batch afterwards must not clear it.
	if res := sh.ship(batchOf(session, newULIDs(t, 1))); res.Status != http.StatusOK {
		t.Fatalf("contiguous batch: %d %v", res.Status, res.Body)
	}
	if got := read(); got.Gapped != 1 {
		t.Errorf("streams = %+v, want the gap marker to be sticky", got)
	}
}

// batchOf renders one NDJSON batch containing exactly these event ids.
func batchOf(session ids.ID, eventIDs []ids.ID) []byte {
	var b strings.Builder
	for i, id := range eventIDs {
		fmt.Fprintf(&b,
			`{"id":%q,"type":"vehicle.rud","ver":1,"flight":null,"session":%q,"sim_t":%d.5,"wall_t":%d,"payload":{"cause":"ground_impact","speed_ms":%d}}`+"\n",
			ids.String(id), ids.String(session), i, 1_770_000_000_000+int64(i), 60+i)
	}
	return []byte(b.String())
}

func newULID(t *testing.T) ids.ID {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newULIDs(t *testing.T, n int) []ids.ID {
	t.Helper()
	out := make([]ids.ID, n)
	for i := range out {
		out[i] = newULID(t)
	}
	return out
}
