package stats

import (
	"encoding/json"
	"testing"
)

func TestFlightStartedEngineCountDecodeAbsentZeroAndPositive(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want *int
	}{
		{name: "absent", raw: `{"vehicle_name":"Probe","stage_count":0}`},
		{name: "zero", raw: `{"vehicle_name":"Probe","stage_count":0,"engine_count":0}`, want: intPointer(0)},
		{name: "positive", raw: `{"vehicle_name":"Rocket","stage_count":3,"engine_count":4}`, want: intPointer(4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodePayload("flight.started", json.RawMessage(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := decoded.(FlightStarted)
			if !ok {
				t.Fatalf("decode type = %T, want FlightStarted", decoded)
			}
			switch {
			case tc.want == nil && got.EngineCount != nil:
				t.Errorf("engine_count = %d, want absent", *got.EngineCount)
			case tc.want != nil && got.EngineCount == nil:
				t.Errorf("engine_count is absent, want %d", *tc.want)
			case tc.want != nil && *got.EngineCount != *tc.want:
				t.Errorf("engine_count = %d, want %d", *got.EngineCount, *tc.want)
			}
		})
	}
}

func intPointer(v int) *int { return &v }
