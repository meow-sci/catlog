package stats

import (
	"encoding/json"
	"testing"
)

func TestVehicleOrbitFinalV1ElementsJSONAndDecode(t *testing.T) {
	want := VehicleOrbit{
		Phase: "achieved", Body: "earth", ApM: 185000.5, PeM: 172400.25,
		Ecc: 0.0034, IncDeg: 28.58, SmaM: 6557100.375, LanDeg: 72.25,
		ArgpDeg: 14.75, TPe: 160.125, PeriodS: 5420.5, MassKg: 4820.75,
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"phase":"achieved","body":"earth","ap_m":185000.5,"pe_m":172400.25,"ecc":0.0034,"inc_deg":28.58,"sma_m":6557100.375,"lan_deg":72.25,"argp_deg":14.75,"t_pe":160.125,"period_s":5420.5,"mass_kg":4820.75}`
	if string(raw) != wantJSON {
		t.Fatalf("marshal = %s, want %s", raw, wantJSON)
	}

	decoded, err := decodePayload("vehicle.orbit", raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(VehicleOrbit)
	if !ok {
		t.Fatalf("decoded type = %T, want VehicleOrbit", decoded)
	}
	if got != want {
		t.Errorf("decoded = %+v, want %+v", got, want)
	}
}

func TestVehicleOrbitDecodeRejectsNonFiniteJSONTokens(t *testing.T) {
	for _, token := range []string{"NaN", "Infinity", "-Infinity"} {
		raw := json.RawMessage(`{"phase":"achieved","sma_m":` + token + `}`)
		if _, err := decodePayload("vehicle.orbit", raw); err == nil {
			t.Errorf("decodePayload accepted %s", token)
		}
	}
}
