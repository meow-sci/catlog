package stats

import (
	"encoding/json"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/store"
)

// Event is a stored event decoded for folding — the `DecodedEvent` of §5.5/§5.6.
//
// Seq is the projector's cursor and the leaderboard tie-break, so it is the one
// field that must never be derived from anything the client sent: it is the
// server-local rowid of events.db.
type Event struct {
	Seq       int64
	PlayerID  int64
	FlightID  ids.ID // ids.Zero for session.started and roster.snapshot
	SessionID ids.ID
	// Career is the §4.1 career id: which KSA save this session is playing.
	// SimTime is seconds since *that* career began, so it is only comparable
	// between events sharing a (PlayerID, Career). Empty for a stored event
	// written before the key existed.
	Career     string
	Type       string
	Ver        int
	SimTime    float64
	HasSimTime bool
	WallTime   int64 // client unix ms, untrusted
	RecvTime   int64 // server unix ms — what the feed timestamps with
	// Raw is the payload exactly as stored (§4.1: unknown payload keys survive).
	Raw json.RawMessage
	// Payload is the typed form, or nil for a type no fold reads.
	Payload any
}

// HasFlight reports whether the event belongs to a flight. session.started and
// roster.snapshot do not (§5.4 stores their flight_id as SQL NULL), which is why
// the flag exclusion cannot apply to them.
func (e Event) HasFlight() bool { return e.FlightID != ids.Zero }

// HasCareer reports whether the event can be placed in a career. Only the
// career-time boards care; every other fold ignores it.
func (e Event) HasCareer() bool { return e.Career != "" }

// Decode turns a stored row into a foldable event. payload is passed separately
// so the projector can hand in an upcast payload without mutating the row it
// read (stored events are immutable forever — §5.6).
func Decode(se store.StoredEvent, payload json.RawMessage) (Event, error) {
	if len(payload) == 0 {
		payload = se.Payload
	}
	typed, err := decodePayload(se.Type, payload)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Seq:        se.Seq,
		PlayerID:   se.PlayerID,
		FlightID:   se.FlightID,
		SessionID:  se.SessionID,
		Career:     se.Career,
		Type:       se.Type,
		Ver:        se.Ver,
		SimTime:    se.SimTime.Float64,
		HasSimTime: se.SimTime.Valid,
		WallTime:   se.WallTime,
		RecvTime:   se.RecvTime,
		Raw:        payload,
		Payload:    typed,
	}, nil
}
