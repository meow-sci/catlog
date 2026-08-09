package projector

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestUpcastPassesThroughTheCurrentVersion(t *testing.T) {
	u := NewUpcasters()
	if u.Len() != 9 {
		t.Fatalf("the registry has %d entries, want 9 — kitten.tumble, kitten.kia and the seven wire-v2 types", u.Len())
	}
	raw := json.RawMessage(`{"dir":"entered","speed_ms":3200}`)
	got, err := u.Apply("vehicle.atmosphere", 1, raw)
	if err != nil {
		t.Fatalf("ver 1: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("payload was rewritten: %s", got)
	}
}

// TestWireV2UpcastersAreTheIdentity is the server half of the contract in
// mod/catlog.lib/Events/EventTypes.cs: seven types went to ver 2 by *adding*
// keys, so a ver 1 row has to reach the folds byte for byte — unknown keys
// included (§4.1) — and a ver 2 row has to pass straight through.
func TestWireV2UpcastersAreTheIdentity(t *testing.T) {
	u := NewUpcasters()
	for typ, raw := range map[string]json.RawMessage{
		"flight.started":    json.RawMessage(`{"vehicle_name":"A","body":"kerbin","mass_kg":412000,"future_key":7}`),
		"flight.ended":      json.RawMessage(`{"reason":"recovered","crew_count":3}`),
		"vehicle.situation": json.RawMessage(`{"from":"freefall","to":"landed","body":"mun"}`),
		"vehicle.orbit":     json.RawMessage(`{"phase":"achieved","body":"kerbin","ap_m":320000}`),
		"vehicle.rud":       json.RawMessage(`{"cause":"collision","speed_ms":90}`),
		"vehicle.impact":    json.RawMessage(`{"speed_ms":214,"survived":true}`),
		"telemetry.window":  json.RawMessage(`{"n":60,"body":"duna"}`),
	} {
		for _, ver := range []int{1, 2} {
			got, err := u.Apply(typ, ver, raw)
			if err != nil {
				t.Fatalf("%s ver %d: %v", typ, ver, err)
			}
			if string(got) != string(raw) {
				t.Errorf("%s ver %d payload was rewritten:\n got %s\nwant %s", typ, ver, got, raw)
			}
		}
		if _, err := u.Apply(typ, 3, raw); !errors.Is(err, ErrFutureVersion) {
			t.Errorf("%s ver 3 error = %v, want ErrFutureVersion", typ, err)
		}
	}

	// vehicle.landed is new in wire v2 and starts at ver 1, so it needs no
	// upcaster at all — and must not have acquired one.
	raw := json.RawMessage(`{"body":"mun","vertical_speed_ms":1.4,"survived":true}`)
	if got, err := u.Apply("vehicle.landed", 1, raw); err != nil || string(got) != string(raw) {
		t.Errorf("vehicle.landed ver 1 = (%s, %v), want the payload untouched and no error", got, err)
	}
	if _, err := u.Apply("vehicle.landed", 2, raw); !errors.Is(err, ErrFutureVersion) {
		t.Error("vehicle.landed ver 2 folded; this build knows only ver 1")
	}
}

func TestUpcastLeavesTheFlightBumpedPayloadsAlone(t *testing.T) {
	// `kitten.tumble` and `kitten.kia` went to ver 2 when they began carrying a
	// flight. Nothing in the payload moved, so a stored ver 1 row has to reach
	// the folds byte for byte — including keys this build does not know (§4.1).
	u := NewUpcasters()
	for _, typ := range []string{"kitten.tumble", "kitten.kia"} {
		raw := json.RawMessage(`{"kid":"k1","name":"Comet","future_key":7}`)
		got, err := u.Apply(typ, 1, raw)
		if err != nil {
			t.Fatalf("%s ver 1: %v", typ, err)
		}
		if string(got) != string(raw) {
			t.Errorf("%s ver 1 payload was rewritten: %s", typ, got)
		}
		if got, err = u.Apply(typ, 2, raw); err != nil || string(got) != string(raw) {
			t.Errorf("%s ver 2 = (%s, %v), want the payload untouched and no error", got, err, raw)
		}
		if _, err := u.Apply(typ, 3, raw); !errors.Is(err, ErrFutureVersion) {
			t.Errorf("%s ver 3 error = %v, want ErrFutureVersion", typ, err)
		}
	}
}

func TestUpcastRefusesAFutureVersion(t *testing.T) {
	// §4.1 accepts and stores an unknown-but-higher ver; the projector skips it
	// and logs once. This is the "skip" half.
	_, err := NewUpcasters().Apply("vehicle.impact", 3, json.RawMessage(`{}`))
	if !errors.Is(err, ErrFutureVersion) {
		t.Fatalf("ver 3 error = %v, want ErrFutureVersion", err)
	}
}

func TestUpcastChainsThroughEveryVersion(t *testing.T) {
	// The registry has nothing to do until a payload version is bumped, so the
	// chain is exercised against a synthetic type rather than a real one.
	const typ = "test.chained"
	currentVer[typ] = 3
	t.Cleanup(func() { delete(currentVer, typ) })

	u := NewUpcasters()
	u.Register(typ, 1, func(raw json.RawMessage) (json.RawMessage, error) {
		return append(raw[:len(raw)-1:len(raw)-1], []byte(`,"added_in_2":true}`)...), nil
	})
	u.Register(typ, 2, func(raw json.RawMessage) (json.RawMessage, error) {
		return append(raw[:len(raw)-1:len(raw)-1], []byte(`,"added_in_3":true}`)...), nil
	})

	got, err := u.Apply(typ, 1, json.RawMessage(`{"original":1}`))
	if err != nil {
		t.Fatalf("chain from ver 1: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("chained payload is not JSON: %s", got)
	}
	for _, key := range []string{"original", "added_in_2", "added_in_3"} {
		if _, ok := out[key]; !ok {
			t.Errorf("chained payload is missing %q: %s", key, got)
		}
	}

	// Starting halfway runs only the remaining step.
	got, err = u.Apply(typ, 2, json.RawMessage(`{"original":1}`))
	if err != nil {
		t.Fatalf("chain from ver 2: %v", err)
	}
	var half map[string]any
	if err := json.Unmarshal(got, &half); err != nil {
		t.Fatalf("chained payload is not JSON: %s", got)
	}
	if _, ran := half["added_in_2"]; ran {
		t.Errorf("chain from ver 2 re-ran the ver-1 step: %s", got)
	}
	if _, ran := half["added_in_3"]; !ran {
		t.Errorf("chain from ver 2 skipped the ver-2 step: %s", got)
	}

	if types := u.Types(); !slices.Contains(types, typ) {
		t.Errorf("Types() = %v, want it to list %q", types, typ)
	}
}

func TestUpcastReportsAMissingStep(t *testing.T) {
	const typ = "test.gap"
	currentVer[typ] = 2
	t.Cleanup(func() { delete(currentVer, typ) })

	// A version bump declared without its upcaster is a programming error, and
	// the message has to say which hop is missing.
	_, err := NewUpcasters().Apply(typ, 1, json.RawMessage(`{}`))
	if !errors.Is(err, ErrNoUpcaster) {
		t.Fatalf("error = %v, want ErrNoUpcaster", err)
	}
}
