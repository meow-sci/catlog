package clock

import (
	"errors"
	"testing"
	"time"
)

// TestDefaultIsRealTime: the clock every production catlogd runs on is
// time.Now with an extra function call, and nothing else.
func TestDefaultIsRealTime(t *testing.T) {
	c := New(false)
	if c.Controllable() {
		t.Fatal("New(false) produced a controllable clock")
	}
	if got := c.Offset(); got != 0 {
		t.Errorf("offset = %v, want 0", got)
	}
	if drift := c.Now().Sub(time.Now()).Abs(); drift > time.Second {
		t.Errorf("Now() is %v from real time", drift)
	}
}

// TestUncontrollableClockRefuses is the guard that matters: a catlogd started
// without `[server] clock_control = true` must not be movable, whatever calls
// it. This is defence in depth behind the config flag and the loopback-only
// admin mux, not instead of them.
func TestUncontrollableClockRefuses(t *testing.T) {
	c := New(false)

	if _, err := c.Advance(time.Hour); !errors.Is(err, ErrNotControllable) {
		t.Errorf("Advance on an uncontrollable clock = %v, want ErrNotControllable", err)
	}
	if _, err := c.SetTo(time.Now().Add(time.Hour)); !errors.Is(err, ErrNotControllable) {
		t.Errorf("SetTo on an uncontrollable clock = %v, want ErrNotControllable", err)
	}
	if got := c.Offset(); got != 0 {
		t.Errorf("a refused move still changed the offset to %v", got)
	}
}

// TestAdvanceAccumulates: a simulation walks the clock forward in steps, so the
// steps have to add up rather than replace one another.
func TestAdvanceAccumulates(t *testing.T) {
	c := New(true)
	fixed := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	c.real = func() time.Time { return fixed }

	for _, step := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 90 * 24 * time.Hour} {
		if _, err := c.Advance(step); err != nil {
			t.Fatalf("Advance(%v): %v", step, err)
		}
	}

	want := (1 + 7 + 90) * 24 * time.Hour
	if got := c.Offset(); got != want {
		t.Errorf("offset = %v, want %v", got, want)
	}
	if got := c.Now(); !got.Equal(fixed.Add(want)) {
		t.Errorf("Now() = %v, want %v", got, fixed.Add(want))
	}
}

// TestAdvanceGoesBackwards: filling in "last month" means moving back, and the
// clock has to allow it. What it must not do is silently clamp.
func TestAdvanceGoesBackwards(t *testing.T) {
	c := New(true)
	if _, err := c.Advance(-48 * time.Hour); err != nil {
		t.Fatalf("Advance backwards: %v", err)
	}
	if got := c.Offset(); got != -48*time.Hour {
		t.Errorf("offset = %v, want -48h", got)
	}
}

// TestSetToLandsOnTheInstant is the form a harness actually wants: "make it be
// the first of next month" rather than "add 31 days".
func TestSetToLandsOnTheInstant(t *testing.T) {
	c := New(true)
	fixed := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	c.real = func() time.Time { return fixed }

	target := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.SetTo(target)
	if err != nil {
		t.Fatalf("SetTo: %v", err)
	}
	if !got.Equal(target) {
		t.Errorf("SetTo returned %v, want %v", got, target)
	}
	if now := c.Now(); !now.Equal(target) {
		t.Errorf("Now() = %v, want %v", now, target)
	}
}

// TestOffsetIsBounded: an offset typo must fail loudly rather than push unix
// milliseconds somewhere absurd, and a refused move must not half-apply.
func TestOffsetIsBounded(t *testing.T) {
	c := New(true)
	if _, err := c.Advance(MaxOffset + 24*time.Hour); err == nil {
		t.Fatal("an offset past ±10 years was accepted")
	}
	if got := c.Offset(); got != 0 {
		t.Errorf("a refused move left the offset at %v, want 0", got)
	}

	if _, err := c.Advance(-MaxOffset - 24*time.Hour); err == nil {
		t.Fatal("an offset past -10 years was accepted")
	}
}

// TestConcurrentReadsAndMoves: ingest stamps recv_time from this on the writer
// goroutine while an admin route moves it, so the race detector has to be happy.
func TestConcurrentReadsAndMoves(t *testing.T) {
	c := New(true)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for range 500 {
			_ = c.Now()
		}
	}()
	for range 500 {
		if _, err := c.Advance(time.Millisecond); err != nil {
			t.Errorf("Advance: %v", err)
			break
		}
	}
	<-done
}
