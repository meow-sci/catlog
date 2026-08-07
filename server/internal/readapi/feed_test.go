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

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

// feed writes rows straight into `feed`, oldest first.
func (f *fixture) feed(summaries ...string) {
	f.t.Helper()
	for i, summary := range summaries {
		if _, err := f.proj.InsertFeed(f.t.Context(), nil, store.FeedRow{
			At: int64(1_800_000_000_000 + i), Handle: "whiskers", Type: "vehicle.rud", Summary: summary,
		}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func TestFeedSnapshotIsNewestFirstAndClamps(t *testing.T) {
	f := newFixture(t)
	f.feed("one", "two", "three")

	got := decode[readapi.FeedResponse](t, f.get("/v1/feed"))
	if got.Limit != readapi.DefaultFeedLimit {
		t.Errorf("limit = %d, want %d", got.Limit, readapi.DefaultFeedLimit)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("%d rows, want 3", len(got.Rows))
	}
	if got.Rows[0].Summary != "three" {
		t.Errorf("first row = %q, want the newest (%q)", got.Rows[0].Summary, "three")
	}
	if got.Rows[0].ID <= got.Rows[2].ID {
		t.Errorf("rows are not newest-first: ids %d … %d", got.Rows[0].ID, got.Rows[2].ID)
	}

	// Clamped, not rejected — same reasoning as the leaderboard limit (§4.8).
	if got := decode[readapi.FeedResponse](t, f.get("/v1/feed?limit=99999")); got.Limit != readapi.MaxFeedLimit {
		t.Errorf("limit=99999 → %d, want %d", got.Limit, readapi.MaxFeedLimit)
	}
	if got := decode[readapi.FeedResponse](t, f.get("/v1/feed?limit=2")); len(got.Rows) != 2 {
		t.Errorf("limit=2 returned %d rows", len(got.Rows))
	}
}

// TestFeedSnapshotIsAnArrayWhenEmpty matters to the SPA: `rows: null` would make
// every consumer of this endpoint need a null check the type does not admit.
func TestFeedSnapshotIsAnArrayWhenEmpty(t *testing.T) {
	f := newFixture(t)
	if body := strings.TrimSpace(f.get("/v1/feed").Body.String()); !strings.Contains(body, `"rows":[]`) {
		t.Errorf("empty feed body = %s, want rows to be []", body)
	}
}

// stubFeed is a broadcaster whose channel the test writes to directly.
type stubFeed struct{ ch chan []store.FeedRow }

func (s stubFeed) Subscribe() (<-chan []store.FeedRow, func()) { return s.ch, func() {} }

// TestFeedStreamEmitsJSONEvents pins the wire format the SPA's EventSource
// parses: `event: feed` with a JSON `store.FeedRow` payload — not HTML, which is
// the whole reason this route exists alongside the datastar one.
func TestFeedStreamEmitsJSONEvents(t *testing.T) {
	events := testutil.MemEvents(t)
	dir := directory.New(events)
	if err := dir.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	ch := make(chan []store.FeedRow, 1)
	srv, err := readapi.New(readapi.Deps{
		Projections: live{testutil.MemProjections(t)},
		Events:      events,
		Directory:   dir,
		Feed:        stubFeed{ch},
		Log:         testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	// The handler runs until the request context is cancelled, so it is driven
	// on a goroutine against a real socket rather than a recorder.
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/feed/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if got, want := res.Header.Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached stream never updates", got)
	}

	ch <- []store.FeedRow{{ID: 7, At: 1_800_000_000_000, Handle: "whiskers", Type: "vehicle.rud", Summary: "boom"}}

	var data string
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if data == "" {
		t.Fatalf("no data frame arrived (scanner err %v)", scanner.Err())
	}
	var row store.FeedRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		t.Fatalf("frame %q is not a JSON feed row: %v", data, err)
	}
	if row.ID != 7 || row.Summary != "boom" || row.Handle != "whiskers" {
		t.Errorf("row = %+v, want the one published", row)
	}
}

// TestFeedStreamIsNotMountedWithoutABroadcaster keeps the optional dependency
// honest: no broadcaster means no route, rather than a route that 500s.
func TestFeedStreamIsNotMountedWithoutABroadcaster(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/feed/stream", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /v1/feed/stream without a broadcaster = %d, want 404", rec.Code)
	}
}
