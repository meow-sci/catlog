package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// get is the read half of the two admin verbs this file exercises.
func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()
	res, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()

	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("response is not JSON (%d): %v", res.StatusCode, err)
	}
	return res, decoded
}

// TestAdminShadowbanRoutes drives the whole review workflow over HTTP, which is
// the only way catlogctl ever reaches it: withhold, list, read, restore.
//
// No projector is registered on this server, so the responses carry no rebuild
// handle — which is itself worth asserting, because the routes must work on a
// server that only does ingest and moderation.
func TestAdminShadowbanRoutes(t *testing.T) {
	_, srv, events, deny := newModerationServer(t)

	res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "griefer"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}
	jkt, _ := body["jkt"].(string)

	handle, err := events.HandleByLC(t.Context(), "griefer")
	if err != nil {
		t.Fatalf("resolve the handle: %v", err)
	}
	playerID := handle.PlayerID

	// Two events in the live log before the ban.
	evs := []store.Event{
		{ID: testutil.ULID(t), SessionID: testutil.ULID(t), Type: "vehicle.rud", Ver: 1, WallTime: 1,
			Payload: json.RawMessage(`{"cause":"ground_impact"}`)},
		{ID: testutil.ULID(t), SessionID: testutil.ULID(t), Type: "vehicle.staging", Ver: 1, WallTime: 2,
			Payload: json.RawMessage(`{"stage_index":3}`)},
	}
	if _, _, err := events.InsertEvents(t.Context(), nil, playerID, evs); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	// --- shadowban ---
	res, body = post(t, srv, "/admin/shadowban", ShadowbanRequest{Handle: "griefer", Reason: "harassment"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("shadowban = %d: %v", res.StatusCode, body)
	}
	sb, _ := body["shadowban"].(map[string]any)
	if sb == nil {
		t.Fatalf("shadowban response has no result: %v", body)
	}
	if got := sb["withheld"].(float64); got != 2 {
		t.Errorf("withheld = %v, want 2", got)
	}
	sub, _ := sb["sub"].(string)

	// The credential must keep working, and the account must not reach the
	// deny-list. That is the entire difference from a ban: their client goes on
	// shipping, and everything it ships is withheld.
	if deny.HasSub(sub) || deny.HasJKT(jkt) {
		t.Error("a shadow ban reached the deny-list; it must not — the client keeps working")
	}
	cred, err := events.CredentialByJKT(t.Context(), jkt)
	if err != nil {
		t.Fatalf("read the credential: %v", err)
	}
	if cred.Revoked() {
		t.Error("a shadow ban revoked the credential; it must not")
	}
	// Nor may it retire the handle: retirement is permanent (D9) and this is a
	// reversible action.
	if retired, _ := events.HandleRetired(t.Context(), "griefer"); retired {
		t.Error("a shadow ban retired the handle permanently")
	}

	// --- the roster ---
	res, body = get(t, srv, "/admin/shadowban")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("shadowban list = %d: %v", res.StatusCode, body)
	}
	if got := body["total_events"].(float64); got != 2 {
		t.Errorf("roster total_events = %v, want 2", got)
	}
	rows, _ := body["shadowbans"].([]any)
	if len(rows) != 1 {
		t.Fatalf("roster has %d rows, want 1: %v", len(rows), body)
	}
	row := rows[0].(map[string]any)
	if row["reason"] != "harassment" {
		t.Errorf("roster reason = %v, want harassment", row["reason"])
	}
	if handles, _ := row["handles"].([]any); len(handles) != 1 || handles[0] != "griefer" {
		t.Errorf("roster handles = %v, want [griefer]", row["handles"])
	}

	// --- the content the review reads ---
	res, body = get(t, srv, "/admin/shadowban/events?handle=griefer&limit=10")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("shadowban events = %d: %v", res.StatusCode, body)
	}
	got, _ := body["events"].([]any)
	if len(got) != 2 {
		t.Fatalf("review page has %d events, want 2: %v", len(got), body)
	}
	// Newest first, payloads intact and unredacted — the point of the endpoint.
	first := got[0].(map[string]any)
	payload, _ := json.Marshal(first["payload"])
	if string(payload) != `{"stage_index":3}` {
		t.Errorf("review payload = %s, want the original", payload)
	}

	// --- restore ---
	res, body = post(t, srv, "/admin/unshadowban", ShadowbanRequest{Handle: "griefer"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unshadowban = %d: %v", res.StatusCode, body)
	}
	un, _ := body["unshadowban"].(map[string]any)
	if un == nil || un["restored"].(float64) != 2 {
		t.Fatalf("unshadowban restored %v, want 2", body)
	}
	if n, err := events.CountEvents(t.Context(), playerID); err != nil || n != 2 {
		t.Errorf("live event count after the restore = %d (err %v), want 2", n, err)
	}
	if n, err := events.CountWithheldEvents(t.Context(), playerID); err != nil || n != 0 {
		t.Errorf("withheld count after the restore = %d (err %v), want 0", n, err)
	}
}

// TestAdminShadowbanDeleteIsIrreversibleAndKeepsTheBan pins the end of a
// review: the events go, the ban does not, and a later lift restores nothing.
func TestAdminShadowbanDeleteIsIrreversibleAndKeepsTheBan(t *testing.T) {
	_, srv, events, _ := newModerationServer(t)

	if res, body := post(t, srv, "/admin/issue", IssueRequest{Handle: "reviewed"}); res.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d: %v", res.StatusCode, body)
	}
	handle, err := events.HandleByLC(t.Context(), "reviewed")
	if err != nil {
		t.Fatalf("resolve the handle: %v", err)
	}
	evs := []store.Event{{
		ID: testutil.ULID(t), SessionID: testutil.ULID(t), Type: "vehicle.rud", Ver: 1, WallTime: 1,
		Payload: json.RawMessage(`{"cause":"ground_impact"}`),
	}}
	if _, _, err := events.InsertEvents(t.Context(), nil, handle.PlayerID, evs); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	if res, body := post(t, srv, "/admin/shadowban", ShadowbanRequest{Handle: "reviewed"}); res.StatusCode != http.StatusOK {
		t.Fatalf("shadowban = %d: %v", res.StatusCode, body)
	}

	res, body := post(t, srv, "/admin/shadowban/delete", ShadowbanRequest{Handle: "reviewed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d: %v", res.StatusCode, body)
	}
	del, _ := body["deleted"].(map[string]any)
	if del == nil || del["deleted"].(float64) != 1 {
		t.Fatalf("delete reported %v, want 1 event", body)
	}

	// Still shadowbanned, so anything shipped next is withheld too.
	on, err := events.Shadowbanned(t.Context(), nil, handle.PlayerID)
	if err != nil || !on {
		t.Fatalf("the delete lifted the ban (err %v)", err)
	}

	// And the lift now gives nothing back, honestly rather than by failing.
	res, body = post(t, srv, "/admin/unshadowban", ShadowbanRequest{Handle: "reviewed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unshadowban = %d: %v", res.StatusCode, body)
	}
	un, _ := body["unshadowban"].(map[string]any)
	if un == nil || un["restored"].(float64) != 0 {
		t.Errorf("unshadowban after a delete restored %v, want 0", body)
	}
}

// TestAdminShadowbanNeedsATarget: "shadowban whichever account matched
// something" is not an operation an operator should be able to reach.
func TestAdminShadowbanNeedsATarget(t *testing.T) {
	_, srv, _, _ := newModerationServer(t)

	res, body := post(t, srv, "/admin/shadowban", ShadowbanRequest{})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("shadowban with no target = %d, want 400: %v", res.StatusCode, body)
	}
	res, body = post(t, srv, "/admin/shadowban", ShadowbanRequest{Handle: "nobody_here"})
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("shadowban of an unknown handle = %d, want 404: %v", res.StatusCode, body)
	}
}
