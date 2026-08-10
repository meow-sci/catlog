package stats

import (
	"encoding/json"
	"testing"
)

func TestKittenTumbleFromDecodeIsOpenSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "absent defaults empty", raw: `{"kid":"k1","name":"Ace","speed_ms":7,"body":"duna"}`},
		{name: "known airborne", raw: `{"kid":"k1","name":"Ace","speed_ms":7,"body":"duna","from":"airborne"}`, want: "airborne"},
		{name: "future value preserved", raw: `{"kid":"k1","name":"Ace","speed_ms":7,"body":"duna","from":"swimming"}`, want: "swimming"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodePayload("kitten.tumble", json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("decodePayload: %v", err)
			}
			got, ok := decoded.(KittenTumble)
			if !ok {
				t.Fatalf("decoded type = %T, want KittenTumble", decoded)
			}
			if got.From != tc.want {
				t.Errorf("From = %q, want %q", got.From, tc.want)
			}
		})
	}
}
