package readapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strings"
)

// What the public read API may never publish, and how the raw-event view keeps
// that promise.
//
// # The rule
//
// Constitution §1: the handle a player claims is the only thing any public
// surface ever shows. `user_key` — `HMAC-SHA256(pepper, "<idp>:<subject>")` —
// appears nowhere in this package and never has; it is not in any response
// struct, and redaction_test.go asserts that it stays that way.
//
// # The hazard the rule does not name
//
// `user_key` is the obvious secret. The subtler one is that several §4.2
// payload fields are **derived from the mod's install id**, which is one value
// per KSA installation and therefore one value per *person* rather than per
// account:
//
//	install  session.started's payload, verbatim — a per-install ULID.
//	kid      SHA-256("catlog-kitten:" + install_id + ":" + roster_name).
//	career   SHA-256("catlog-career:" + install_id + ":" + save_key)
//	         — the §4.1 envelope key, which folds copy into the `context` of
//	         every career-time board row.
//
// catlog is built so that one person may hold two accounts (two IdP subjects,
// two handles) and there is no way to tell from the outside that they are the
// same person. But a player with two accounts ships both from the same install,
// so **publishing any of those three raw would link the two handles to one
// person** — exactly the deanonymisation the handle-only model exists to
// prevent. `install` is the strongest (identical across every session of the
// install); `career` follows it (identical whenever both accounts play the same
// save); `kid` is weakest but real (identical whenever both accounts fly a
// kitten of the same name).
//
// # What this file does about it
//
//   - `install` (and `install_id`) is **dropped**. Relabelling it would produce
//     a per-player constant that says nothing — one install per player, as far
//     as a reader can see — while still being a token people would try to read
//     meaning into. There is nothing to group by, so there is nothing to keep.
//
//   - `career` and `kid` are **relabelled per player** by [Label]. Both are
//     grouping keys a reader genuinely wants — "these records came from the
//     same save", "these EVAs were the same kitten" — and a per-player
//     relabelling keeps that inside one handle while making two handles'
//     labels unrelatable. The label has the same shape (16 lowercase Crockford
//     base32 characters) as the value it replaces, so nothing downstream has to
//     care.
//
//   - The rules are keyed **by field name at any depth**, not by event type.
//     §4.1 preserves unknown payload keys for forward compatibility, so a
//     future mod version can put a field anywhere; matching on the name means
//     `roster.snapshot.kittens[].kid` is covered without being enumerated, and
//     a new event type carrying `install` is covered before anybody notices.
//
// # What is deliberately published
//
// Everything else, because a raw-event view whose data is mostly hidden is not
// worth building. Flight and session ids are per-flight and per-save-load
// ULIDs, minted fresh, with no install in them. Body names, speeds, vehicle
// names and kitten names are gameplay — the same class of player-authored text
// as a handle, and the moderation path already covers them (§4.2).
//
// Two residuals, stated rather than hidden:
//
//   - Kitten and vehicle **names** are the same across a person's two accounts
//     if they name things the same way. That is a soft correlator no redaction
//     can remove without deleting the content the view exists to show, and it
//     is the same exposure a player accepts by picking a recognisable handle.
//   - Receive **times** correlate anything shipped at the same moment. The
//     activity feed has published per-handle timestamps since §5.6; this adds
//     no new channel.
//
// # Not published, for a smaller reason
//
// The envelope's `wall_t` is absent from [EventRow]. It is the client's own
// clock, untrusted by §4.1 and useless next to the server's receive time — and
// its offset from `recv` is a per-machine constant, which is a (weak) way to
// tell two accounts on one machine apart from two accounts on two.

// labelDomain separates this hash from every other one in catlog, the same way
// the mod's own derivations are domain-separated (docs/events.md).
const labelDomain = "catlog-public-label:"

// crockford is the lowercase Crockford base32 alphabet the mod's `career` and
// `kid` are written in. A label is drawn from it so a redacted value is the
// same shape as the one it replaces.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// LabelLen is the character width of a public label: 16, matching §4.2's `kid`
// and §4.1's `career`.
const LabelLen = 16

// Label re-derives an install-derived identifier as a per-player token.
//
// The salt is the player id, so the same `career` shipped by two accounts on
// one machine produces two unrelated labels — which is the whole property this
// exists for. It is unkeyed: linking two labels would require guessing the
// underlying value, and the only party who already knows it is the person who
// owns the install. A pepper would close that last case, and is not worth
// giving this package a dependency on the key set for.
//
// It is stable for the lifetime of a player id, so a client may cache it, group
// by it, and put it in a URL.
func Label(playerID int64, kind, value string) string {
	if value == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(labelDomain))
	h.Write([]byte(kind))
	h.Write([]byte{':'})
	_ = binary.Write(h, binary.BigEndian, playerID)
	h.Write([]byte{':'})
	h.Write([]byte(value))
	sum := h.Sum(nil)

	out := make([]byte, LabelLen)
	for i := range out {
		out[i] = crockford[sum[i]&0x1f]
	}
	return string(out)
}

// The `kind` arguments [Label] is called with. They keep one value from
// producing the same label in two roles.
const (
	kindCareer = "career"
	kindKitten = "kid"
)

// redactedKeys are the field names [Redact] acts on, at any depth. Adding a
// §4.2 field with cross-handle correlation properties means adding it here —
// and nowhere else, which is the point of matching on the name.
var redactedKeys = map[string]string{
	"install":    "", // dropped
	"install_id": "", // dropped
	"career":     kindCareer,
	"kid":        kindKitten,
}

// redactTriggers is the fast path: a blob mentioning none of these names cannot
// contain anything to redact, so the common board row — `{"body":"duna",
// "flight":"…","energy_j":…}` — is passed through as the bytes the fold wrote,
// with no decode and no re-encode.
var redactTriggers = [][]byte{[]byte(`"install`), []byte(`"career"`), []byte(`"kid"`)}

// Redact applies the rules above to a payload or `context` blob and returns it
// re-encoded. Malformed JSON — which nothing can produce, since both callers
// pass bytes a decoder already accepted — is dropped rather than passed
// through, because a blob this cannot parse is a blob it cannot promise
// anything about.
func Redact(playerID int64, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	hit := false
	for _, t := range redactTriggers {
		if bytes.Contains(raw, t) {
			hit = true
			break
		}
	}
	if !hit {
		return raw
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers keep their literal text, so a payload's own formatting survives
	// the round trip and no float is re-rendered into something else.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	out, err := json.Marshal(redactValue(playerID, v))
	if err != nil {
		return nil
	}
	return out
}

// redactValue walks decoded JSON applying [redactedKeys] by name at any depth.
func redactValue(playerID int64, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			kind, redacted := redactedKeys[strings.ToLower(k)]
			if !redacted {
				out[k] = redactValue(playerID, val)
				continue
			}
			if kind == "" {
				continue // dropped outright
			}
			s, isString := val.(string)
			if !isString || s == "" {
				// A relabelled key whose value is not a string is not something
				// this build understands, so it does not get published either.
				continue
			}
			out[k] = Label(playerID, kind, s)
		}
		return out
	case []any:
		for i := range t {
			t[i] = redactValue(playerID, t[i])
		}
		return t
	default:
		return v
	}
}
