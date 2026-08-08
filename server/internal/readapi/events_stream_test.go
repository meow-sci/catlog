package readapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// stubRaw is a raw broadcaster whose channel the test writes to directly.
type stubRaw struct{ ch chan []store.StoredEvent }

func (s stubRaw) Subscribe() (<-chan []store.StoredEvent, func()) { return s.ch, func() {} }

// streamFixture is the readapi fixture with a raw broadcaster wired and the
// mux served on a real socket — the stream handler runs until its request
// context cancels, so a recorder cannot drive it.
type streamFixture struct {
	*fixture
	ch chan []store.StoredEvent
	ts *httptest.Server
}

func newStreamFixture(t *testing.T, maxClients int) *streamFixture {
	t.Helper()
	ch := make(chan []store.StoredEvent, 4)
	f := newFixture(t, func(d *readapi.Deps) {
		d.RawEvents = stubRaw{ch}
		d.MaxStreamClients = maxClients
	})
	ts := httptest.NewServer(f.mux)
	t.Cleanup(ts.Close)
	return &streamFixture{fixture: f, ch: ch, ts: ts}
}

// open starts one stream request and returns the response.
func (sf *streamFixture) open(query string) *http.Response {
	sf.t.Helper()
	ctx, cancel := context.WithTimeout(sf.t.Context(), 10*time.Second)
	sf.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sf.ts.URL+"/v1/events/stream"+query, nil)
	if err != nil {
		sf.t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		sf.t.Fatal(err)
	}
	sf.t.Cleanup(func() { res.Body.Close() })
	return res
}

// stored builds a StoredEvent as the projector would publish it: the row as
// events.db holds it, redaction not yet applied.
func stored(seq, playerID int64, typ, career string, payload string) store.StoredEvent {
	var id ids.ID
	id[0], id[15] = byte(playerID), byte(seq)
	return store.StoredEvent{
		Seq: seq, PlayerID: playerID, RecvTime: 1_800_000_000_000 + seq,
		Event: store.Event{
			ID: id, SessionID: id, Career: career, Type: typ, Ver: 1,
			WallTime: 1_770_000_000_000, Payload: json.RawMessage(payload),
		},
	}
}

// readEvents scans SSE frames off the stream until want data frames arrived.
func readEvents(t *testing.T, body *bufio.Scanner, want int) []readapi.EventRow {
	t.Helper()
	var out []readapi.EventRow
	for len(out) < want && body.Scan() {
		line := body.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var row readapi.EventRow
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &row); err != nil {
			t.Fatalf("frame %q is not a JSON event row: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

// The wire format, and the privacy boundary on it: a frame is `event: raw`
// with a JSON EventRow — seq, handle, redacted payload — and the redaction is
// per row's player, so the §1 install-derived identifiers never reach a socket.
func TestEventStreamEmitsRedactedJSON(t *testing.T) {
	sf := newStreamFixture(t, 0)
	alpha := sf.player("alpha_pilot")

	res := sf.open("")
	if got, want := res.Header.Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	sf.ch <- []store.StoredEvent{stored(41, alpha, "session.started", sharedCareer,
		`{"mod_ver":"0.1.0","install":"`+sharedInstall+`","kid":"`+sharedKid+`"}`)}

	scanner := bufio.NewScanner(res.Body)
	var sawEventName, sawID bool
	var data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "event: raw":
			sawEventName = true
		case line == "id: 41":
			sawID = true
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
		if data != "" {
			break
		}
	}
	if data == "" {
		t.Fatalf("no data frame arrived (scanner err %v)", scanner.Err())
	}
	if !sawEventName || !sawID {
		t.Errorf("frame lacked event name (%v) or id (%v)", sawEventName, sawID)
	}

	var row readapi.EventRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		t.Fatalf("frame %q is not a JSON event row: %v", data, err)
	}
	if row.Seq != 41 || row.Handle != "alpha_pilot" || row.Type != "session.started" {
		t.Errorf("row = %+v", row)
	}
	for _, leak := range []string{sharedInstall, sharedCareer, sharedKid, `"install"`, `"wall_t"`, `"user_key"`} {
		if strings.Contains(data, leak) {
			t.Errorf("the stream published %q", leak)
		}
	}
	if row.Career == "" || row.Career == sharedCareer {
		t.Errorf("career = %q, want a per-player label", row.Career)
	}
}

// Rows with nobody to name and rows in flagged flights are dropped at publish,
// before a frame exists — the same rules as every paginated raw view.
func TestEventStreamDropsHandleLessAndFlaggedRows(t *testing.T) {
	sf := newStreamFixture(t, 0)
	alpha := sf.player("alpha_pilot")
	cheated := testutil.ULID(t)
	sf.flagFlight(alpha, cheated, stats.FlagTeleport)

	res := sf.open("")
	flaggedRow := stored(2, alpha, "vehicle.impact", sharedCareer, `{"speed_ms":999}`)
	flaggedRow.FlightID = cheated
	sf.ch <- []store.StoredEvent{
		stored(1, 9999, "vehicle.staging", sharedCareer, `{"stage_index":0}`), // no such player: no handle
		flaggedRow,
		stored(3, alpha, "vehicle.staging", sharedCareer, `{"stage_index":1}`),
	}

	rows := readEvents(t, bufio.NewScanner(res.Body), 1)
	if len(rows) != 1 || rows[0].Seq != 3 {
		t.Fatalf("rows = %+v, want only seq 3", rows)
	}
}

// Server-side filters: `?type=` and `?handle=` match per subscriber against
// the already-rendered frames.
func TestEventStreamFiltersPerSubscriber(t *testing.T) {
	sf := newStreamFixture(t, 0)
	alpha, beta := sf.player("alpha_pilot"), sf.player("beta_pilot")

	byType := sf.open("?type=vehicle.impact")
	byHandle := sf.open("?handle=beta_pilot")

	sf.ch <- []store.StoredEvent{
		stored(1, alpha, "vehicle.staging", sharedCareer, `{"stage_index":0}`),
		stored(2, alpha, "vehicle.impact", sharedCareer, `{"speed_ms":100}`),
		stored(3, beta, "vehicle.staging", sharedCareer, `{"stage_index":1}`),
	}
	// A second batch proves the misses above were skips, not lag.
	sf.ch <- []store.StoredEvent{
		stored(4, beta, "vehicle.impact", sharedCareer, `{"speed_ms":200}`),
	}

	gotTypes := readEvents(t, bufio.NewScanner(byType.Body), 2)
	if len(gotTypes) != 2 || gotTypes[0].Seq != 2 || gotTypes[1].Seq != 4 {
		t.Errorf("?type= rows = %+v, want seqs 2 and 4", gotTypes)
	}
	gotHandles := readEvents(t, bufio.NewScanner(byHandle.Body), 2)
	if len(gotHandles) != 2 || gotHandles[0].Seq != 3 || gotHandles[1].Seq != 4 {
		t.Errorf("?handle= rows = %+v, want seqs 3 and 4", gotHandles)
	}

	// An unknown, retired or banned handle is the usual one-answer 404.
	if res := sf.open("?handle=never_existed"); res.StatusCode != http.StatusNotFound {
		t.Errorf("?handle=never_existed = %d, want 404", res.StatusCode)
	}
}

// The subscriber cap: over it, a stream open is refused with the §4.9
// rate_limited shape rather than held open.
func TestEventStreamCapsSubscribers(t *testing.T) {
	sf := newStreamFixture(t, 1)

	first := sf.open("")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first stream = %d, want 200", first.StatusCode)
	}
	second := sf.open("")
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second stream = %d, want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After")
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(second.Body).Decode(&body); err != nil || body.Error != "rate_limited" {
		t.Errorf("429 body error = %q (err %v), want rate_limited", body.Error, err)
	}
}

// No broadcaster, no route — the same rule as the feed stream.
func TestEventStreamIsNotMountedWithoutABroadcaster(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /v1/events/stream without a broadcaster = %d, want 404", rec.Code)
	}
	// The paginated view is unconditional.
	if rec := f.get("/v1/events"); rec.Code != http.StatusOK {
		t.Errorf("GET /v1/events = %d, want 200", rec.Code)
	}
}
