// Package ids mints and parses ULIDs, converting between the 26-char string
// form and the 16-byte storage form (§5.2).
//
// ULIDs are catlog's only identifier type: event ids, flight ids, session ids,
// stream ids and license jtis (D19). The DDL (§5.4) stores them as 16-byte
// BLOBs, so every helper here is about moving between the wire form (26-char
// Crockford base32, what the mod sends) and the storage form.
//
// Most ULIDs are minted client-side by the mod; the server mints only its own
// (license jti, dev-path identifiers), which is what [New] is for.
package ids

import (
	"crypto/rand"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// ID is a ULID. It is an alias — not a wrapper — so callers can hand an ID to
// any oklog/ulid function and vice versa without conversion (D4's "no shims"
// spirit applied to the identifier type).
type ID = ulid.ULID

// Size is the byte width of a ULID in its storage form (§5.4: `BLOB` columns).
const Size = 16

// StringLen is the character width of a ULID in its wire form (§4.1).
const StringLen = ulid.EncodedSize // 26

// Zero is the all-zero ULID. It is never a valid identifier; nullable columns
// (`event.flight_id`) use SQL NULL rather than this value.
var Zero ID

// entropy is the process-wide monotonic entropy source. Monotonic entropy makes
// ULIDs minted within the same millisecond sort in creation order, which keeps
// server-minted identifiers totally ordered. It is mutex-guarded, so New is
// safe for concurrent use.
var entropy = &ulid.LockedMonotonicReader{
	MonotonicReader: ulid.Monotonic(rand.Reader, 0),
}

// New mints a ULID at the current wall time.
func New() (ID, error) { return NewAt(time.Now()) }

// NewAt mints a ULID with t's millisecond timestamp.
func NewAt(t time.Time) (ID, error) {
	id, err := ulid.New(ulid.Timestamp(t), entropy)
	if err != nil {
		return Zero, fmt.Errorf("mint ulid: %w", err)
	}
	return id, nil
}

// MustNew mints a ULID and panics on failure. Only for tests and process
// startup, where a failing CSPRNG is unrecoverable anyway.
func MustNew() ID {
	id, err := New()
	if err != nil {
		panic(err)
	}
	return id
}

// Parse decodes the 26-char wire form. It is strict: length, alphabet and
// timestamp overflow are all rejected, so a malformed `id` in an ingest
// envelope fails here rather than silently truncating (§4.1 validation).
func Parse(s string) (ID, error) {
	if len(s) != StringLen {
		return Zero, fmt.Errorf("parse ulid: got %d chars, want %d", len(s), StringLen)
	}
	id, err := ulid.ParseStrict(s)
	if err != nil {
		return Zero, fmt.Errorf("parse ulid %q: %w", s, err)
	}
	return id, nil
}

// MustParse decodes the wire form and panics on failure. Tests and constants only.
func MustParse(s string) ID {
	id, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

// String renders the 26-char wire form.
func String(id ID) string { return id.String() }

// Bytes returns the 16-byte storage form as a fresh slice, ready to bind to a
// BLOB parameter. The copy matters: database/sql may retain the slice past the
// call, and id is a value the caller owns.
func Bytes(id ID) []byte {
	b := make([]byte, Size)
	copy(b, id[:])
	return b
}

// NullBytes is Bytes for nullable columns: it returns SQL NULL for [Zero],
// which is how `event.flight_id` encodes "no flight" (§4.1: flight is null for
// session and roster events).
func NullBytes(id ID) driver.Value {
	if id == Zero {
		return nil
	}
	return Bytes(id)
}

// FromBytes decodes the 16-byte storage form read back out of a BLOB column.
func FromBytes(b []byte) (ID, error) {
	if len(b) != Size {
		return Zero, fmt.Errorf("ulid from bytes: got %d bytes, want %d", len(b), Size)
	}
	var id ID
	copy(id[:], b)
	return id, nil
}

// FromNullBytes decodes a nullable BLOB column: a NULL (nil slice) yields [Zero].
func FromNullBytes(b []byte) (ID, error) {
	if b == nil {
		return Zero, nil
	}
	return FromBytes(b)
}

// Time returns the ULID's embedded millisecond timestamp.
func Time(id ID) time.Time { return ulid.Time(id.Time()) }
