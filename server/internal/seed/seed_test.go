package seed_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/ingest"
	"github.com/meow-sci/catlog/server/internal/projector"
	"github.com/meow-sci/catlog/server/internal/seed"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func TestDatasetIsDeterministic(t *testing.T) {
	a, b := seed.Dataset(), seed.Dataset()
	if len(a) != len(b) || len(a) != 3 {
		t.Fatalf("dataset has %d players, want 3", len(a))
	}
	for i := range a {
		if a[i].Handle != b[i].Handle {
			t.Fatalf("player %d: %q vs %q", i, a[i].Handle, b[i].Handle)
		}
		if len(a[i].Events) != len(b[i].Events) {
			t.Fatalf("%s: %d vs %d events", a[i].Handle, len(a[i].Events), len(b[i].Events))
		}
		seen := map[ids.ID]bool{}
		for j := range a[i].Events {
			x, y := a[i].Events[j], b[i].Events[j]
			if x.ID != y.ID {
				t.Errorf("%s event %d: id %s vs %s", a[i].Handle, j, ids.String(x.ID), ids.String(y.ID))
			}
			if string(x.Payload) != string(y.Payload) {
				t.Errorf("%s event %d: payload %s vs %s", a[i].Handle, j, x.Payload, y.Payload)
			}
			if seen[x.ID] {
				t.Errorf("%s event %d reuses id %s; the dedup index would swallow it",
					a[i].Handle, j, ids.String(x.ID))
			}
			seen[x.ID] = true
		}
	}
}

func TestSeedProducesTheExpectedBoards(t *testing.T) {
	dir := t.TempDir()
	events := testutil.EventsAt(t, filepath.Join(dir, "events.db"))
	projections := testutil.ProjectionsAt(t, filepath.Join(dir, "projections.db"))
	keys := testutil.Keys(t)
	ctx := t.Context()

	res, err := seed.Apply(ctx, events, keys, 1_700_000_000_000)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(res.Players) != 3 || res.Accepted != res.Events || res.Deduped != 0 {
		t.Fatalf("first seed: %+v", res)
	}

	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	live := projector.NewLive(projections)
	p, err := projector.New(projector.Options{
		Events: events, Live: live, Directory: d,
		StoreOptions: testutil.Options(), Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Drain(ctx); err != nil {
		t.Fatalf("fold the seeded events: %v", err)
	}

	got := boardValues(t, live)
	want := map[string]float64{
		// demo_crasher's lithobrake is §5.6's own example. The 999 m/s impact on
		// the flagged flight must be nowhere.
		"demo_crasher/biggest_lithobrake_survived": 214,
		"demo_crasher/peak_g_survived":             9.6,
		"demo_crasher/fastest_surface_speed":       782,
		"demo_crasher/rud_total":                   6,
		"demo_crasher/kittens_recovered":           1,

		"demo_ace/fastest_surface_speed": 2410,
		"demo_ace/fastest_orbital_speed": 9450,
		"demo_ace/peak_g_survived":       6.8,
		"demo_ace/orbits_achieved":       3,
		"demo_ace/dockings":              1,
		"demo_ace/stagings":              3,
		"demo_ace/soi_bodies":            4,
		"demo_ace/kittens_recovered":     5,
		"demo_ace/distance_travelled":    4_210_000,

		// demo_ace's second career is the fast one: its clock restarts at 0 and
		// the builder ticks 12.5 s per event, so orbit lands at 37.5 s into the
		// career and Mars at 75 s. Career-relative, not wall-clock — that is the
		// whole point of the boards. Published in milliseconds: `sim_t` stays
		// seconds on the wire and the projection converts (docs/DECISIONS.md,
		// WP-CLOCK).
		"demo_ace/fastest_to_orbit": 37_500,
		"demo_ace/fastest_to_luna":  50_000,
		"demo_ace/fastest_to_sol":   62_500,
		"demo_ace/fastest_to_mars":  75_000,

		"demo_tumbler/kitten_tumbles":     4,
		"demo_tumbler/soi_bodies":         2,
		"demo_tumbler/kittens_recovered":  2,
		"demo_tumbler/distance_travelled": 930_000,
	}
	for key, v := range want {
		if got[key] != v {
			t.Errorf("%s = %v, want %v", key, got[key], v)
		}
	}
	// Two players per cause on purpose: a `rud_<cause>` board is only *listed*
	// once two distinct players are on it (stats.Catalog), so a demo where only
	// demo_crasher had ever had a RUD would show none of the per-cause boards.
	for _, cause := range seed.RUDCauses {
		stat, ok := stats.RUDStat(cause)
		if !ok {
			t.Fatalf("the demo flies a cause that cannot be a stat key: %q", cause)
		}
		for _, handle := range []string{seed.HandleCrasher, seed.HandleTumbler} {
			key := handle + "/" + stat
			if got[key] != 1 {
				t.Errorf("%s = %v, want 1 — every RUD cause needs two demo entrants", key, got[key])
			}
		}
	}
	// Same for the per-body boards: demo_ace owns them, demo_crasher makes them
	// publishable.
	for _, soi := range seed.StockBodyRun {
		stat, ok := stats.FastestToStat(soi.ToBody)
		if !ok {
			t.Fatalf("the demo visits a body that cannot be a stat key: %q", soi.ToBody)
		}
		for _, handle := range []string{seed.HandleAce, seed.HandleCrasher} {
			if _, on := got[handle+"/"+stat]; !on {
				t.Errorf("%s/%s is missing — a per-body board needs two demo entrants", handle, stat)
			}
		}
		if got[seed.HandleAce+"/"+stat] >= got[seed.HandleCrasher+"/"+stat] {
			t.Errorf("%s: demo_ace %v is not faster than demo_crasher %v",
				stat, got[seed.HandleAce+"/"+stat], got[seed.HandleCrasher+"/"+stat])
		}
	}
	// The flagged flight must not have leaked into anything.
	for _, key := range []string{
		"demo_crasher/fastest_orbital_speed", "demo_crasher/orbits_achieved",
	} {
		if got[key] >= 9999 {
			t.Errorf("%s = %v: the flagged flight scored", key, got[key])
		}
	}

	// Re-seeding is a no-op (D19's derived ids meeting the dedup index).
	again, err := seed.Apply(ctx, events, keys, 1_700_000_000_001)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if again.Accepted != 0 || again.Deduped != again.Events {
		t.Errorf("second seed: %+v — want everything deduped", again)
	}
	if _, err := p.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if after := boardValues(t, live); !sameFloats(after, got) {
		t.Error("re-seeding changed the boards")
	}
}

func TestSeedIsWhatARebuildProduces(t *testing.T) {
	// The demo dataset is meant to be the canonical answer, which means the
	// incremental fold of it must already equal what a rebuild would produce —
	// the flagged flight emits its flag before it scores anything for exactly
	// this reason.
	dir := t.TempDir()
	events := testutil.EventsAt(t, filepath.Join(dir, "events.db"))
	projections := testutil.ProjectionsAt(t, filepath.Join(dir, "projections.db"))
	keys := testutil.Keys(t)
	ctx := t.Context()

	if _, err := seed.Apply(ctx, events, keys, 1_700_000_000_000); err != nil {
		t.Fatal(err)
	}
	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	live := projector.NewLive(projections)
	p, err := projector.New(projector.Options{
		Events: events, Live: live, Directory: d,
		StoreOptions: testutil.Options(), Log: testutil.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	incremental := boardValues(t, live)

	if _, err := p.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt := boardValues(t, live); !sameFloats(rebuilt, incremental) {
		t.Errorf("rebuild differs from the seeded incremental state:\n rebuilt: %v\n seeded:  %v", rebuilt, incremental)
	}
}

// boardValues reads player_stat keyed "handle/stat".
func boardValues(t *testing.T, live *projector.Live) map[string]float64 {
	t.Helper()
	byPlayer := map[int64]string{}
	for i, h := range seed.Handles() {
		byPlayer[int64(i+1)] = h
	}
	out := map[string]float64{}
	err := live.With(func(p *store.Projections) error {
		rows, err := p.Reader().QueryContext(t.Context(),
			`SELECT player_id, stat, value FROM player_stat ORDER BY player_id, stat`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id    int64
				stat  string
				value float64
			)
			if err := rows.Scan(&id, &stat, &value); err != nil {
				return err
			}
			handle := byPlayer[id]
			if handle == "" {
				handle = fmt.Sprintf("player_%d", id)
			}
			out[handle+"/"+stat] = value
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read boards: %v", err)
	}
	return out
}

func sameFloats(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestEveryDemoEventCarriesAKnownTypeAndAValidEnvelope(t *testing.T) {
	// The dataset bypasses /v1/ingest, so nothing else checks that it obeys the
	// §4.1 envelope rules. A demo event that ingest would have rejected is a
	// dataset that cannot be reproduced by a real client.
	for _, p := range seed.Dataset() {
		for i, e := range p.Events {
			where := fmt.Sprintf("%s event %d (%s)", p.Handle, i, e.Type)
			if e.ID == ids.Zero || e.SessionID == ids.Zero {
				t.Errorf("%s: zero id or session", where)
			}
			if e.Ver != 1 {
				t.Errorf("%s: ver %d, want 1", where, e.Ver)
			}
			if !e.SimTime.Valid {
				t.Errorf("%s: no sim_t", where)
			}
			if !json.Valid(e.Payload) {
				t.Errorf("%s: payload is not JSON: %s", where, e.Payload)
			}
			if !ingest.KnownType(e.Type) {
				t.Errorf("%s: type is not in the §4.2 registry, so /v1/ingest would reject the batch", where)
			}
			switch e.Type {
			case "session.started", "roster.snapshot":
				if e.FlightID != ids.Zero {
					t.Errorf("%s: §4.1 requires a null flight", where)
				}
			default:
				if e.FlightID == ids.Zero {
					t.Errorf("%s: missing flight", where)
				}
			}
		}
	}
}
