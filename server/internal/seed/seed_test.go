package seed_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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
			if x.Type == "system.discovered" {
				var discovered stats.SystemDiscovered
				if err := json.Unmarshal(x.Payload, &discovered); err != nil {
					t.Fatalf("%s system payload: %v", a[i].Handle, err)
				}
				digest, err := base64.RawURLEncoding.DecodeString(discovered.System)
				if err != nil || len(digest) != sha256.Size || len(discovered.System) != 43 ||
					strings.ContainsAny(discovered.System, "+/=") {
					t.Errorf("%s system hash %q is not canonical base64url SHA-256", a[i].Handle, discovered.System)
				}
				if j+1 >= len(a[i].Events) || a[i].Events[j+1].Type != "session.started" ||
					a[i].Events[j+1].Career != x.Career || a[i].Events[j+1].SessionID != x.SessionID {
					t.Errorf("%s system discovery is not immediately before its session.started", a[i].Handle)
				}
			}
		}
	}
}

func TestSeedProducesExpectedBoardsAndBadges(t *testing.T) {
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
		"demo_crasher/landings":                    2,

		"demo_ace/fastest_surface_speed": 2410,
		"demo_ace/fastest_orbital_speed": 9450,
		"demo_ace/peak_g_survived":       6.8,
		"demo_ace/orbits_achieved":       3,
		"demo_ace/dockings":              1,
		"demo_ace/stagings":              3,
		"demo_ace/soi_bodies":            4,
		"demo_ace/kittens_recovered":     5,
		"demo_ace/landings":              3,

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

		"demo_tumbler/kitten_tumbles":    4,
		"demo_tumbler/soi_bodies":        2,
		"demo_tumbler/kittens_recovered": 2,
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
	// Per-save pages and boards must be non-vacuous in the demo: two players
	// each have two saves, every one has a landing row, and their first/second
	// saves deliberately exercise unknown/friendly system display.
	if err := live.With(func(proj *store.Projections) error {
		landings, err := proj.CareerLeaderboard(ctx, stats.StatLandings, "", false, 100, 0)
		if err != nil {
			return err
		}
		if len(landings) != 4 {
			t.Errorf("career landings rows = %d, want 4 populated demo saves", len(landings))
		}
		seen := map[string]float64{}
		for _, row := range landings {
			seen[fmt.Sprintf("%d/%d", row.PlayerID, row.Ordinal)] = row.Value
		}
		for key, want := range map[string]float64{"1/1": 2, "1/2": 1, "3/1": 1, "3/2": 1} {
			if seen[key] != want {
				t.Errorf("career landing %s = %v, want %v", key, seen[key], want)
			}
		}
		for _, playerID := range []int64{1, 3} {
			careers, err := proj.PlayerCareers(ctx, playerID)
			if err != nil {
				return err
			}
			if len(careers) != 2 || careers[0].System != "" || careers[1].System != seed.DemoSystemHash {
				t.Errorf("player %d careers = %+v, want unknown then Sol", playerID, careers)
			}
			if playerID == 1 && (!careers[1].SystemChanged || !careers[1].Rewound) {
				t.Errorf("demo_ace second save = %+v, want system-changed and rewound provenance", careers[1])
			}
			if playerID == 3 && (careers[1].SystemChanged || careers[1].Rewound) {
				t.Errorf("demo_crasher second save = %+v, want no provenance qualifications", careers[1])
			}
		}
		system, ok, err := proj.SystemBySlugOrHash(ctx, "sol")
		if err != nil {
			return err
		}
		if !ok || system.Hash != seed.DemoSystemHash || system.Name != "Sol" || system.Slug != "sol" {
			t.Errorf("friendly demo system = %+v, %v", system, ok)
		}
		byHash, ok, err := proj.SystemBySlugOrHash(ctx, seed.DemoSystemHash)
		if err != nil {
			return err
		}
		if !ok || byHash != system {
			t.Errorf("demo system hash lookup = %+v, %v; slug lookup = %+v", byHash, ok, system)
		}
		alternate, ok, err := proj.SystemBySlugOrHash(ctx, "sol-dense")
		if err != nil {
			return err
		}
		if !ok || alternate.Name != "Sol Dense" || alternate.Slug != "sol-dense" {
			t.Errorf("alternate demo system = %+v, %v", alternate, ok)
		}

		// Do not pin the total badge catalogue: adding a fixed badge should not
		// make the demo brittle (PROJ-039). Pin the representative behaviours G4
		// needs instead: an ordinary event badge, an exploration tier, and a
		// dynamic family member held by two players so the default gate publishes
		// it. These rows came through the ordinary fold above, not a fixture-only
		// projection write.
		counts, err := proj.BadgeCounts(ctx)
		if err != nil {
			return err
		}
		for badge, want := range map[string]int64{
			stats.BadgeFirstFlight: 3,
			stats.BadgeWanderer:    2,
			"reached_luna":         2,
		} {
			if counts[badge] != want {
				t.Errorf("seeded badge %s holders = %d, want %d", badge, counts[badge], want)
			}
		}
		published := false
		for _, badge := range stats.BadgeCatalog(counts, stats.DefaultMinPlayers) {
			if badge.Key == "reached_luna" {
				published = true
				break
			}
		}
		if !published {
			t.Error("reached_luna is not published at the default two-player family gate")
		}

		holders, err := proj.BadgeHolders(ctx, "reached_luna", "", 100, 0)
		if err != nil {
			return err
		}
		if len(holders) != 2 || holders[0].PlayerID != 1 || holders[1].PlayerID != 3 ||
			holders[0].EarnedSeq >= holders[1].EarnedSeq ||
			holders[0].System != seed.DemoSystemHash || holders[1].System != seed.DemoSystemHash {
			t.Errorf("seeded reached_luna holders = %+v, want demo_ace then demo_crasher in Sol", holders)
		}

		aceCareers, err := proj.PlayerCareers(ctx, 1)
		if err != nil {
			return err
		}
		if len(aceCareers) < 2 {
			t.Errorf("demo_ace careers = %+v, want a second save", aceCareers)
		} else {
			awards, err := proj.BadgesForPlayer(ctx, 1, aceCareers[1].Career)
			if err != nil {
				return err
			}
			seen := map[string]store.BadgeRow{}
			for _, award := range awards {
				seen[award.Badge] = award
			}
			for _, badge := range []string{stats.BadgeFirstFlight, stats.BadgeWanderer, "reached_luna"} {
				if _, ok := seen[badge]; !ok {
					t.Errorf("demo_ace Save 2 did not earn representative badge %s: %+v", badge, awards)
				}
			}
			if family := seen["reached_luna"]; family.System != seed.DemoSystemHash ||
				string(family.Context) != `{"body":"luna"}` {
				t.Errorf("demo_ace Save 2 reached_luna provenance = %+v context=%s", family, family.Context)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
	incrementalBoards := boardValues(t, live)
	incrementalBadges := badgeRows(t, live)

	if _, err := p.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt := boardValues(t, live); !sameFloats(rebuilt, incrementalBoards) {
		t.Errorf("rebuilt boards differ from seeded incremental state:\n rebuilt: %v\n seeded:  %v", rebuilt, incrementalBoards)
	}
	if rebuilt := badgeRows(t, live); !slices.Equal(rebuilt, incrementalBadges) {
		t.Errorf("rebuilt badges differ from seeded incremental state:\n rebuilt: %v\n seeded:  %v", rebuilt, incrementalBadges)
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

// badgeRows snapshots every badge column in deterministic primary-key order.
// The seed's rebuild assertion must cover new projection families as they are
// added; comparing boards alone would let a badge-only divergence hide behind
// a test name that promised the whole seeded projection was canonical.
func badgeRows(t *testing.T, live *projector.Live) []string {
	t.Helper()
	var out []string
	err := live.With(func(p *store.Projections) error {
		rows, err := p.Reader().QueryContext(t.Context(),
			`SELECT player_id, career, badge, system, first_career,
			        earned_seq, earned_at, earned_sim_t, context
			 FROM badge_award ORDER BY player_id, career, badge`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				playerID, earnedSeq, earnedAt int64
				career, badge, system, first  string
				sim                           sql.NullFloat64
				context                       []byte
			)
			if err := rows.Scan(&playerID, &career, &badge, &system, &first,
				&earnedSeq, &earnedAt, &sim, &context); err != nil {
				return err
			}
			out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%d|%d|%t:%g|%s",
				playerID, career, badge, system, first, earnedSeq, earnedAt,
				sim.Valid, sim.Float64, context))
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read badges: %v", err)
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
			case "session.started", "system.discovered", "roster.snapshot":
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
