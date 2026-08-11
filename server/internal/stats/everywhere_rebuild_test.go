package stats_test

import (
	"reflect"
	"testing"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func everywhereHistory(flagLate bool) []input {
	root := catalogueBody(solHash, "root", "Root", 0, nil)
	rootPayload := root.payload.(stats.SystemBody)
	rootPayload.Class, rootPayload.Kind = "FutureRootClass", "star"
	root.payload = rootPayload

	planet := catalogueBody(solHash, "new-world", "New World", 1, strptr("root"))
	planetPayload := planet.payload.(stats.SystemBody)
	planetPayload.Class, planetPayload.Kind = "NeverSeenPlanetClass", "planet"
	planet.payload = planetPayload

	flight := flightN(70)
	history := []input{
		discovered(solHash, "FutureSystem", "Future System", 2, true),
		root,
		planet,
		{flight: flight, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "root"}},
		{flight: flight, typ: "vehicle.soi", payload: stats.VehicleSOI{FromBody: "root", ToBody: "new-world"}},
	}
	if flagLate {
		history = append(history, input{flight: flight, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "teleport"}})
	}
	return history
}

func everywhereAwardRows(t *testing.T, p *store.Projections) []string {
	t.Helper()
	rows, err := p.Reader().QueryContext(t.Context(), `
		SELECT career, badge, system, earned_seq
		FROM badge_award
		WHERE badge IN (?, ?)
		ORDER BY career, badge`, stats.BadgeBeenToEveryPlanet, stats.BadgeBeenToEverything)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var career, badge, system string
		var seq int64
		if err := rows.Scan(&career, &badge, &system, &seq); err != nil {
			t.Fatal(err)
		}
		out = append(out, career+"/"+badge+"/"+system)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func projectEverywhereHistory(t *testing.T, history []input, refined bool) (*store.Projections, []string) {
	t.Helper()
	p := testutil.MemProjections(t)
	apply(t, p, history, 0, refined)
	return p, everywhereAwardRows(t, p)
}

func TestEverywhereHonestHistoryMatchesRefinedRebuildAndUsesEmittedKind(t *testing.T) {
	history := everywhereHistory(false)
	incremental, want := projectEverywhereHistory(t, history, false)
	_, rebuilt := projectEverywhereHistory(t, history, true)
	if !reflect.DeepEqual(rebuilt, want) {
		t.Fatalf("refined everywhere awards = %v, incremental %v", rebuilt, want)
	}
	if len(want) != 4 {
		t.Fatalf("everywhere awards = %v, want both scopes for both badges", want)
	}

	// The concrete classes and forest position remain opaque. The subset uses
	// exactly the normalized kind emitted beside them; a root still belongs to
	// Nothing Left, and the unknown concrete planet belongs to Every World.
	rows, err := incremental.Reader().QueryContext(t.Context(), `
		SELECT body, class, kind, coalesce(parent, '')
		FROM system_body WHERE hash = ? ORDER BY body`, solHash)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var body, class, kind, parent string
		if err := rows.Scan(&body, &class, &kind, &parent); err != nil {
			t.Fatal(err)
		}
		got = append(got, body+"/"+class+"/"+kind+"/"+parent)
	}
	wantMapping := []string{
		"new-world/NeverSeenPlanetClass/planet/root",
		"root/FutureRootClass/star/",
	}
	if !reflect.DeepEqual(got, wantMapping) {
		t.Errorf("stored class/parent/kind mapping = %v, want %v", got, wantMapping)
	}
}

func TestEverywhereLateFlagIsRemovedByRefinedRebuild(t *testing.T) {
	history := everywhereHistory(true)
	_, incremental := projectEverywhereHistory(t, history, false)
	_, rebuilt := projectEverywhereHistory(t, history, true)
	if len(incremental) != 4 {
		t.Fatalf("incremental pre-flag awards = %v", incremental)
	}
	if len(rebuilt) != 0 {
		t.Fatalf("refined late-flag awards = %v, want none", rebuilt)
	}
}
