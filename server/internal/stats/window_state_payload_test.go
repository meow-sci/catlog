package stats

import (
	"encoding/json"
	"testing"
)

func TestTelemetryWindowStateDecodePreservesPresenceAndFiniteValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want *StateVec
	}{
		{name: "absent", raw: `{"body":"earth"}`},
		{name: "legitimate origin", raw: `{"body":"earth","state":{"pos":{"x":0,"y":0,"z":0},"vel":{"x":0,"y":0,"z":0}}}`, want: &StateVec{}},
		{name: "finite", raw: `{"body":"earth","state":{"pos":{"x":6557100.375,"y":-182500.25,"z":42125.5},"vel":{"x":215.75,"y":7640.5,"z":-38.125}}}`, want: &StateVec{
			Pos: Vec3{X: 6557100.375, Y: -182500.25, Z: 42125.5},
			Vel: Vec3{X: 215.75, Y: 7640.5, Z: -38.125},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodePayload("telemetry.window", json.RawMessage(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := decoded.(TelemetryWindow)
			if !ok {
				t.Fatalf("decoded type = %T, want TelemetryWindow", decoded)
			}
			switch {
			case tc.want == nil && got.State != nil:
				t.Errorf("state = %+v, want absent", got.State)
			case tc.want != nil && got.State == nil:
				t.Errorf("state is absent, want %+v", *tc.want)
			case tc.want != nil && *got.State != *tc.want:
				t.Errorf("state = %+v, want %+v", *got.State, *tc.want)
			}
		})
	}
}

func TestTelemetryWindowStateDecodeRejectsNonFiniteJSONTokens(t *testing.T) {
	for _, token := range []string{"NaN", "Infinity", "-Infinity"} {
		raw := json.RawMessage(`{"state":{"pos":{"x":` + token + `,"y":0,"z":0},"vel":{"x":0,"y":0,"z":0}}}`)
		if _, err := decodePayload("telemetry.window", raw); err == nil {
			t.Errorf("decodePayload accepted %s", token)
		}
	}
}
