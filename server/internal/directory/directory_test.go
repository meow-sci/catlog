package directory_test

import (
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/directory"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func TestDirectoryResolvesBothWaysAndHidesBans(t *testing.T) {
	events := testutil.MemEvents(t)
	keys := testutil.Keys(t)
	ctx := t.Context()

	whiskers := testutil.Player(t, events, keys, "dev", "whiskers")
	mittens := testutil.Player(t, events, keys, "dev", "mittens")
	if err := events.ClaimHandle(ctx, whiskers, "Whiskers_Prime", 100); err != nil {
		t.Fatal(err)
	}
	if err := events.ClaimHandle(ctx, mittens, "Mittens", 200); err != nil {
		t.Fatal(err)
	}

	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if h, ok := d.Handle(whiskers); !ok || h != "Whiskers_Prime" {
		t.Errorf("Handle(%d) = %q, %v — want the display casing", whiskers, h, ok)
	}
	// §4.7: uniqueness is case-insensitive, display casing is preserved.
	e, ok := d.Lookup("whiskers_prime")
	if !ok || e.PlayerID != whiskers || e.Handle != "Whiskers_Prime" || e.Since != 100 {
		t.Errorf("Lookup(lowercased) = %+v, %v", e, ok)
	}
	if _, ok := d.Lookup("nobody"); ok {
		t.Error("an unknown handle resolved")
	}
	if d.Len() != 2 {
		t.Errorf("Len = %d, want 2", d.Len())
	}

	// A ban removes the player from every read surface at once (§4.7): the
	// handle stops resolving and the player stops having one.
	if err := events.SetBan(ctx, nil, mittens, 12345, "cheating"); err != nil {
		t.Fatal(err)
	}
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Lookup("mittens"); ok {
		t.Error("a banned player's handle still resolves")
	}
	if _, ok := d.Handle(mittens); ok {
		t.Error("a banned player still has a handle")
	}
	if got := d.BannedIDs(); len(got) != 1 || got[0] != mittens {
		t.Errorf("BannedIDs = %v, want [%d]", got, mittens)
	}
	if d.Len() != 1 {
		t.Errorf("Len = %d after the ban, want 1", d.Len())
	}
}

func TestPrimaryHandleIsTheOldestOne(t *testing.T) {
	// §4.7 lets an account hold up to five handles. The identity a leaderboard
	// shows must not move when a second is claimed, so the oldest wins.
	events := testutil.MemEvents(t)
	keys := testutil.Keys(t)
	ctx := t.Context()

	p := testutil.Player(t, events, keys, "dev", "whiskers")
	if err := events.ClaimHandle(ctx, p, "first_claim", 100); err != nil {
		t.Fatal(err)
	}
	if err := events.ClaimHandle(ctx, p, "second_claim", 500); err != nil {
		t.Fatal(err)
	}

	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.Handle(p); h != "first_claim" {
		t.Errorf("Handle = %q, want the oldest claim", h)
	}
	// Both still resolve by name.
	for _, h := range []string{"first_claim", "second_claim"} {
		if _, ok := d.Lookup(h); !ok {
			t.Errorf("Lookup(%q) failed", h)
		}
	}
}

func TestRetiredHandleDisappearsOnReload(t *testing.T) {
	events := testutil.MemEvents(t)
	keys := testutil.Keys(t)
	ctx := t.Context()

	p := testutil.Player(t, events, keys, "dev", "whiskers")
	if err := events.ClaimHandle(ctx, p, "gone_forever", 100); err != nil {
		t.Fatal(err)
	}
	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Lookup("gone_forever"); !ok {
		t.Fatal("the handle did not resolve before retirement")
	}

	if err := events.RetireHandle(ctx, nil, "gone_forever", "deleted", 999); err != nil {
		t.Fatal(err)
	}
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Lookup("gone_forever"); ok {
		t.Error("a retired handle still resolves (D9: never recycled, never shown)")
	}
}

func TestReloadIsSafeOnAnEmptyDatabase(t *testing.T) {
	d := directory.New(testutil.MemEvents(t))
	if err := d.Reload(t.Context()); err != nil {
		t.Fatalf("reload on an empty database: %v", err)
	}
	if d.Len() != 0 || d.BannedIDs() != nil {
		t.Errorf("Len = %d, BannedIDs = %v", d.Len(), d.BannedIDs())
	}
	if _, ok := d.Handle(1); ok {
		t.Error("an unknown player resolved")
	}
}

func TestSearchIsDeterministicAndOrderedPrefixFirst(t *testing.T) {
	events := testutil.MemEvents(t)
	keys := testutil.Keys(t)
	ctx := t.Context()

	for _, handle := range []string{"Whiskers_Prime", "whiskers_two", "not_whiskers", "banned_whisker", "mittens"} {
		id := testutil.Player(t, events, keys, "dev", handle)
		if err := events.ClaimHandle(ctx, id, handle, 100); err != nil {
			t.Fatal(err)
		}
		if handle == "banned_whisker" {
			if err := events.SetBan(ctx, nil, id, 12345, "cheating"); err != nil {
				t.Fatal(err)
			}
		}
	}
	d := directory.New(events)
	if err := d.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	got, truncated := d.Search("WHISK", 10)
	want := []string{"Whiskers_Prime", "whiskers_two", "not_whiskers"}
	if !slices.Equal(got, want) || truncated {
		t.Errorf("Search = %v (truncated %v), want %v — prefix matches first, banned absent", got, truncated, want)
	}

	// The order has to be stable across calls, because a CDN caches one answer
	// per query and Go's map iteration is randomised.
	for range 5 {
		if again, _ := d.Search("whisk", 10); !slices.Equal(again, got) {
			t.Fatalf("Search gave %v then %v", got, again)
		}
	}

	if got, truncated := d.Search("whisk", 2); len(got) != 2 || !truncated {
		t.Errorf("Search(limit 2) = %v, truncated %v", got, truncated)
	}
	if got, _ := d.Search("  ", 10); got != nil {
		t.Errorf("Search(blank) = %v, want nothing", got)
	}
}
