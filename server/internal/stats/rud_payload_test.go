package stats

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestVehicleRUDPartCountJSONAndDecode(t *testing.T) {
	for _, partCount := range []int{34, 0} {
		t.Run(strconv.Itoa(partCount), func(t *testing.T) {
			want := VehicleRUD{
				Cause: "ground_impact", PeakG: 41.5, SpeedMs: 220, Body: "earth",
				CrewCount: 2, PartCount: partCount,
			}
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			const prefix = `{"cause":"ground_impact","peak_g":41.5,"peak_q_pa":0,"speed_ms":220,"altitude_m":0,"body":"earth","crew_count":2,"part_count":`
			if got, wantJSON := string(raw), prefix+strconv.Itoa(partCount)+`,"lat":null,"lon":null}`; got != wantJSON {
				t.Fatalf("marshal = %s, want %s", got, wantJSON)
			}

			decoded, err := decodePayload("vehicle.rud", raw)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := decoded.(VehicleRUD)
			if !ok {
				t.Fatalf("decode type = %T, want VehicleRUD", decoded)
			}
			if got.PartCount != partCount {
				t.Errorf("part_count = %d, want %d", got.PartCount, partCount)
			}
		})
	}
}
