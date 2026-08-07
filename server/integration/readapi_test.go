//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/meow-sci/catlog/server/internal/readapi"
	"github.com/meow-sci/catlog/server/internal/stats"
)

// TestSeededReadAPI drives the whole WP4 surface against the real binary:
// seed the demo dataset over the admin API, then read the boards over the public
// API and check the §4.8 contract — the cache header, the paging, the profile —
// exactly as a CDN and a browser would.
func TestSeededReadAPI(t *testing.T) {
	s := startServer(t)

	var seeded struct {
		Players  []string `json:"players"`
		Events   int      `json:"events"`
		Accepted int      `json:"accepted"`
		Deduped  int      `json:"deduped"`
		FoldedTo int64    `json:"folded_to"`
	}
	s.adminJSON(t, http.MethodPost, "/admin/seed", &seeded)
	if len(seeded.Players) != 3 || seeded.Accepted == 0 || seeded.FoldedTo == 0 {
		t.Fatalf("seed response: %+v", seeded)
	}

	t.Run("leaderboards index", func(t *testing.T) {
		res, body := s.public(t, "/v1/leaderboards")
		if got := res.Header.Get("Cache-Control"); got != readapi.CacheControl {
			t.Errorf("Cache-Control = %q, want %q", got, readapi.CacheControl)
		}
		var out readapi.BoardsResponse
		mustJSON(t, body, &out)
		byStat := map[string]readapi.BoardSummary{}
		for _, b := range out.Boards {
			byStat[b.Stat] = b
		}
		// Every board with a compile-time key is listed whether or not anyone is
		// on it. There is deliberately no total to compare against: the rest of
		// the index comes from the data, and hard-coding its size is what this
		// change removed.
		for _, b := range stats.FixedBoards() {
			if _, listed := byStat[b.Stat]; !listed {
				t.Errorf("%q is missing from the index", b.Stat)
			}
		}
		if b := byStat[stats.StatBiggestLithobrakeSurvived]; b.Count != 1 || b.Unit != "m/s" {
			t.Errorf("lithobrake board = %+v", b)
		}
		// And the two data-driven families, which the demo dataset gives two
		// entrants each precisely so they are publishable.
		for _, want := range []struct {
			stat  string
			title string
			asc   bool
		}{
			{"rud_ground_impact", "RUDs — Ground Impact", false},
			{"fastest_to_luna", "Fastest to Luna", true},
			{"fastest_to_mars", "Fastest to Mars", true},
		} {
			b, listed := byStat[want.stat]
			if !listed {
				t.Errorf("%q came out of the event stream and was not listed", want.stat)
				continue
			}
			if b.Title != want.title || b.Ascending != want.asc || b.Count != 2 {
				t.Errorf("board %q = %+v, want title %q ascending=%v count=2", want.stat, b, want.title, want.asc)
			}
		}
	})

	t.Run("one board", func(t *testing.T) {
		res, body := s.public(t, "/v1/leaderboards/"+stats.StatBiggestLithobrakeSurvived)
		if got := res.Header.Get("Cache-Control"); got != readapi.CacheControl {
			t.Errorf("Cache-Control = %q", got)
		}
		var out readapi.BoardResponse
		mustJSON(t, body, &out)
		if len(out.Rows) != 1 {
			t.Fatalf("%d rows, want 1: %s", len(out.Rows), body)
		}
		row := out.Rows[0]
		if row.Handle != "demo_crasher" || row.Value != 214 || row.Rank != 1 {
			t.Errorf("row = %+v, want demo_crasher at 214 m/s, rank 1", row)
		}
		if row.Updated == 0 {
			t.Error("row has no `updated` timestamp")
		}
		var ctx struct {
			Body    string  `json:"body"`
			Flight  string  `json:"flight"`
			EnergyJ float64 `json:"energy_j"`
		}
		mustJSON(t, row.Context, &ctx)
		if ctx.Body != "duna" || ctx.Flight == "" || ctx.EnergyJ == 0 {
			t.Errorf("context = %s", row.Context)
		}
	})

	t.Run("paging and clamping", func(t *testing.T) {
		_, body := s.public(t, "/v1/leaderboards/kittens_recovered?limit=2&offset=1")
		var out readapi.BoardResponse
		mustJSON(t, body, &out)
		if out.Limit != 2 || out.Offset != 1 {
			t.Errorf("echoed limit/offset = %d/%d", out.Limit, out.Offset)
		}
		if len(out.Rows) != 2 || out.Rows[0].Rank != 2 {
			t.Errorf("rows = %+v", out.Rows)
		}

		_, body = s.public(t, "/v1/leaderboards/kittens_recovered?limit=9999")
		mustJSON(t, body, &out)
		if out.Limit != readapi.MaxLimit {
			t.Errorf("limit = %d, want it clamped to %d (§4.8)", out.Limit, readapi.MaxLimit)
		}
	})

	t.Run("profile", func(t *testing.T) {
		res, body := s.public(t, "/v1/players/demo_ace")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", res.StatusCode, body)
		}
		var out readapi.PlayerResponse
		mustJSON(t, body, &out)
		if out.Handle != "demo_ace" || out.Since == 0 || len(out.Stats) == 0 {
			t.Fatalf("profile = %+v", out)
		}
		found := map[string]float64{}
		for _, st := range out.Stats {
			found[st.Stat] = st.Value
			if st.Rank < 1 {
				t.Errorf("stat %s has rank %d", st.Stat, st.Rank)
			}
		}
		if found[stats.StatFastestOrbitalSpeed] != 9450 {
			t.Errorf("fastest_orbital_speed = %v, want 9450", found[stats.StatFastestOrbitalSpeed])
		}
		if _, scored := found[stats.StatKittenTumbles]; scored {
			t.Error("demo_ace scored on the tumble board")
		}
	})

	t.Run("not found", func(t *testing.T) {
		for _, path := range []string{"/v1/leaderboards/not_a_board", "/v1/players/nobody_here"} {
			res, _ := s.public(t, path)
			if res.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
			}
			if got := res.Header.Get("Cache-Control"); got != readapi.CacheControl {
				t.Errorf("GET %s Cache-Control = %q — a 404 is a cacheable public fact too", path, got)
			}
		}
	})

	t.Run("admin stats", func(t *testing.T) {
		var out struct {
			Events struct {
				Total   int64 `json:"total"`
				Players int64 `json:"players"`
				Handles int64 `json:"handles"`
			} `json:"events"`
			Projector struct {
				CheckpointSeq int64    `json:"checkpoint_seq"`
				LagSeq        int64    `json:"lag_seq"`
				Folds         []string `json:"folds"`
			} `json:"projector"`
			Projections struct {
				PlayerStat     int64 `json:"player_stat"`
				FlightState    int64 `json:"flight_state"`
				FlaggedFlights int64 `json:"flagged_flights"`
			} `json:"projections"`
		}
		s.adminJSON(t, http.MethodGet, "/admin/stats", &out)
		if out.Events.Total != int64(seeded.Events) || out.Events.Players != 3 || out.Events.Handles != 3 {
			t.Errorf("events = %+v", out.Events)
		}
		if out.Projector.CheckpointSeq != seeded.FoldedTo || out.Projector.LagSeq != 0 {
			t.Errorf("projector = %+v, want checkpoint %d and no lag", out.Projector, seeded.FoldedTo)
		}
		if out.Projections.PlayerStat == 0 || out.Projections.FlaggedFlights != 1 {
			t.Errorf("projections = %+v, want the one flagged demo flight", out.Projections)
		}
		if len(out.Projector.Folds) == 0 {
			t.Error("no folds reported")
		}
	})

	t.Run("rebuild keeps the boards identical", func(t *testing.T) {
		_, before := s.public(t, "/v1/leaderboards/"+stats.StatBiggestLithobrakeSurvived)

		var res struct {
			Events  int64  `json:"events"`
			LastSeq int64  `json:"last_seq"`
			Stats   int64  `json:"stats"`
			Path    string `json:"path"`
		}
		s.adminJSON(t, http.MethodPost, "/admin/projections/rebuild", &res)
		if res.Events != int64(seeded.Events) || res.LastSeq != seeded.FoldedTo || res.Stats == 0 {
			t.Fatalf("rebuild = %+v", res)
		}

		_, after := s.public(t, "/v1/leaderboards/"+stats.StatBiggestLithobrakeSurvived)
		if !bytes.Equal(before, after) {
			t.Errorf("the board changed across a rebuild of an already-correct dataset:\n before %s\n after  %s", before, after)
		}
	})

	t.Run("catlogctl", func(t *testing.T) {
		// The same three verbs through the CLI, which is how the nightly timer
		// (§11) drives them.
		for _, verb := range []string{"seed", "rebuild", "stats"} {
			cmd := exec.Command(s.catlogctl, verb, "-admin", s.adminURL)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("catlogctl %s: %v\n%s", verb, err, out)
				continue
			}
			t.Logf("catlogctl %s:\n%s", verb, out)
		}
	})
}

// public issues a GET against the public API and returns the response and body.
func (s *server) public(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Get(s.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET %s Content-Type = %q", path, ct)
	}
	return res, body
}

// adminJSON calls the loopback admin API and decodes the response.
func (s *server) adminJSON(t *testing.T, method, path string, out any) {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte(`{}`))
	}
	req, err := http.NewRequest(method, s.adminURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d: %s", method, path, res.StatusCode, raw)
	}
	if out != nil {
		mustJSON(t, raw, out)
	}
}

func mustJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
}
