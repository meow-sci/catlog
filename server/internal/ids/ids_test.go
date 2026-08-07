package ids

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRoundTripStringAndBytes(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s := String(id)
	if len(s) != StringLen {
		t.Errorf("string form is %d chars, want %d", len(s), StringLen)
	}
	if s != strings.ToUpper(s) {
		t.Errorf("string form %q is not upper-case Crockford base32", s)
	}

	back, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if back != id {
		t.Errorf("string round trip: %s != %s", String(back), s)
	}

	b := Bytes(id)
	if len(b) != Size {
		t.Fatalf("byte form is %d bytes, want %d", len(b), Size)
	}
	fromBytes, err := FromBytes(b)
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	if fromBytes != id {
		t.Errorf("byte round trip: %s != %s", String(fromBytes), s)
	}

	// Bytes must copy: the DDL stores BLOBs and database/sql may retain the
	// slice past the call.
	b[0] ^= 0xff
	if again := Bytes(id); again[0] == b[0] {
		t.Error("Bytes returned an aliased slice; mutating it changed the ULID")
	}
}

// TestParseWireForm pins the exact 26-char envelope value the mod sends (§4.1).
func TestParseWireForm(t *testing.T) {
	const s = "01ARZ3NDEKTSV4RRFFQ69G5FAV" // canonical ULID spec example
	id, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if String(id) != s {
		t.Errorf("round trip = %q, want %q", String(id), s)
	}
	if got := len(Bytes(id)); got != 16 {
		t.Errorf("storage form is %d bytes, want 16", got)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for name, s := range map[string]string{
		"empty":               "",
		"too short":           "01ARZ3NDEKTSV4RRFFQ69G5FA",
		"too long":            "01ARZ3NDEKTSV4RRFFQ69G5FAVX",
		"lowercase-length-ok": "01arz3ndektsv4rrffq69g5fa!",
		"invalid alphabet":    "01ARZ3NDEKTSV4RRFFQ69G5FAU", // U is not in Crockford base32
		"illegal char":        "01ARZ3NDEKTSV4RRFFQ69G5FA-",
		"timestamp overflow":  "80000000000000000000000000",
		"not an id":           "hello world",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(s); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", s)
			}
		})
	}
}

func TestFromBytesRejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 1, 15, 17, 32} {
		if _, err := FromBytes(make([]byte, n)); err == nil {
			t.Errorf("FromBytes(%d bytes) succeeded, want an error", n)
		}
	}
}

// TestNullBytes covers the nullable flight_id column (§4.1: flight is null for
// session and roster events).
func TestNullBytes(t *testing.T) {
	if got := NullBytes(Zero); got != nil {
		t.Errorf("NullBytes(Zero) = %v, want nil (SQL NULL)", got)
	}
	id := MustNew()
	b, ok := NullBytes(id).([]byte)
	if !ok {
		t.Fatalf("NullBytes(id) = %T, want []byte", NullBytes(id))
	}
	if len(b) != Size {
		t.Errorf("NullBytes(id) is %d bytes, want %d", len(b), Size)
	}

	back, err := FromNullBytes(nil)
	if err != nil {
		t.Fatalf("FromNullBytes(nil): %v", err)
	}
	if back != Zero {
		t.Errorf("FromNullBytes(nil) = %s, want the zero ULID", String(back))
	}
	if back, err = FromNullBytes(b); err != nil || back != id {
		t.Errorf("FromNullBytes round trip = %s (err %v), want %s", String(back), err, String(id))
	}
}

func TestNewAtCarriesTimestamp(t *testing.T) {
	when := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	id, err := NewAt(when)
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	got := Time(id)
	if !got.Equal(when.Truncate(time.Millisecond)) {
		t.Errorf("Time = %v, want %v", got, when)
	}
}

// TestNewIsMonotonicAndUnique checks the two properties the server relies on:
// no duplicates under concurrency, and lexicographic order matching creation
// order (which is what makes ULIDs a useful sort key).
func TestNewIsMonotonicAndUnique(t *testing.T) {
	const goroutines, each = 8, 250

	var (
		mu   sync.Mutex
		seen = make(map[ID]bool, goroutines*each)
		wg   sync.WaitGroup
	)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				id, err := New()
				if err != nil {
					t.Errorf("New: %v", err)
					return
				}
				mu.Lock()
				if seen[id] {
					t.Errorf("duplicate ULID %s", String(id))
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != goroutines*each {
		t.Errorf("minted %d distinct ULIDs, want %d", len(seen), goroutines*each)
	}

	// Sequential mints within the same millisecond must still sort in order.
	prev := String(MustNew())
	for range 1000 {
		next := String(MustNew())
		if next <= prev {
			t.Fatalf("ULIDs went backwards: %s then %s", prev, next)
		}
		prev = next
	}
}

func TestMustParsePanicsOnGarbage(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustParse did not panic on garbage")
		}
	}()
	MustParse("nope")
}

func TestZeroIsNotAValidStorageValue(t *testing.T) {
	if String(Zero) != "00000000000000000000000000" {
		t.Errorf("Zero renders as %q", String(Zero))
	}
	// It still parses, so callers must treat it as a sentinel rather than
	// relying on parse failure.
	if _, err := Parse(String(Zero)); err != nil {
		t.Errorf("the zero ULID must still parse: %v", err)
	}
}
