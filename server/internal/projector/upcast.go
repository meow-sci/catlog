package projector

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// CurrentVer is the payload version every §4.2 event type is at today. The
// taxonomy shipped as "launch set, all ver: 1"; a type that later diverges gets
// an entry in currentVer below.
const CurrentVer = 1

// currentVer overrides [CurrentVer] per type. Adding a key here is half of a
// version bump, the other half being an [Upcaster] for every version between the
// old one and the new. It must equal the mod's EventTypes.Versions exactly: a
// type the mod stamps ver 2 while this map still says 1 is skipped here as a
// future version, which is silent data loss for that type until a rebuild.
var currentVer = map[string]int{
	// Both gained a non-null `flight` in ver 2 (mod: EventPipeline's tumble and
	// KIA attribution). The payload did not change — see the identity upcasters
	// in [NewUpcasters] — but what the server does with the event did: at ver 1
	// neither could name a flight, so `kitten.tumble` could not inherit its
	// flight's `tuning` flag and `kitten.kia` could not disqualify an impact
	// under the ±2 s window. The stored `ver` is what tells those rows apart.
	"kitten.tumble": 2,
	"kitten.kia":    2,
}

// Errors the version resolution can produce. Neither is fatal to the projector:
// §4.1 says an event it cannot decode is skipped and logged once, because the
// row itself is valid and a later build will fold it on the next rebuild.
var (
	// ErrFutureVersion means the event was written by a newer mod than this
	// build understands. Accepting and storing such events is deliberate
	// (§4.1); folding them is not possible.
	ErrFutureVersion = errors.New("projector: event payload version is newer than this build")
	// ErrNoUpcaster means a version bump was declared without the upcaster that
	// makes the older rows readable. A programming error, surfaced loudly.
	ErrNoUpcaster = errors.New("projector: no upcaster registered")
)

// Upcaster rewrites a payload of version v into the shape of version v+1.
//
// It receives the payload exactly as stored, so an upcaster that only adds a
// field can return its input unchanged plus the addition; unknown keys must be
// preserved, because §4.1 promises they survive.
type Upcaster func(raw json.RawMessage) (json.RawMessage, error)

type upcastKey struct {
	typ string
	ver int
}

// Upcasters is the (type, ver) → transform registry (§5.6).
//
// Stored events are immutable forever: the row that arrived in 2026 will still
// be folded in 2030, by whatever the folds look like then. This registry is the
// only sanctioned way to bridge that gap — nothing rewrites events.db.
type Upcasters struct {
	m map[upcastKey]Upcaster
}

// NewUpcasters returns the registry this build folds with.
//
// `kitten.tumble` and `kitten.kia` are at ver 2 and their transforms are the
// identity: the bump was an envelope change (those events now carry a flight),
// and a ver 1 row's payload is already exactly what the ver 2 folds read. The
// entries are still required — [Apply] refuses to fold a row it cannot bring to
// the current version — and they are what keeps the old rows scoring as they
// always did: a flightless `kitten.tumble` still counts, it simply cannot be
// excluded by a flag it never named.
func NewUpcasters() *Upcasters {
	u := &Upcasters{m: map[upcastKey]Upcaster{}}
	u.Register("kitten.tumble", 1, identityUpcast)
	u.Register("kitten.kia", 1, identityUpcast)
	return u
}

// identityUpcast is the transform for a version bump that did not touch the
// payload. It returns the stored bytes untouched, which also preserves the
// unknown keys §4.1 promises survive.
func identityUpcast(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }

// Register adds the transform from version ver to ver+1 for one event type.
func (u *Upcasters) Register(typ string, ver int, fn Upcaster) {
	if fn == nil {
		panic("projector: nil upcaster for " + typ)
	}
	u.m[upcastKey{typ, ver}] = fn
}

// Len reports how many transforms are registered — a /admin/stats number that
// makes an empty registry visible rather than assumed.
func (u *Upcasters) Len() int { return len(u.m) }

// Types lists the event types with at least one registered transform.
func (u *Upcasters) Types() []string {
	seen := map[string]struct{}{}
	for k := range u.m {
		seen[k.typ] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range maps.Keys(seen) {
		out = append(out, t)
	}
	return out
}

// Apply brings a stored payload up to the version this build folds.
//
// A payload already at the current version is returned untouched, which is the
// only path taken today.
func (u *Upcasters) Apply(typ string, ver int, raw json.RawMessage) (json.RawMessage, error) {
	want := CurrentVer
	if v, ok := currentVer[typ]; ok {
		want = v
	}
	switch {
	case ver == want:
		return raw, nil
	case ver > want:
		return nil, fmt.Errorf("%w: %s ver %d, this build folds ver %d", ErrFutureVersion, typ, ver, want)
	}

	out := raw
	for v := ver; v < want; v++ {
		fn, ok := u.m[upcastKey{typ, v}]
		if !ok {
			return nil, fmt.Errorf("%w: %s ver %d → %d", ErrNoUpcaster, typ, v, v+1)
		}
		next, err := fn(out)
		if err != nil {
			return nil, fmt.Errorf("projector: upcast %s ver %d → %d: %w", typ, v, v+1, err)
		}
		out = next
	}
	return out, nil
}
