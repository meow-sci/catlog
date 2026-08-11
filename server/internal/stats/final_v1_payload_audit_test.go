package stats

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestFinalV1PayloadFieldTypesAndTags is the D4 server-half audit in one place.
// Non-pointer fields are required final-v1 values; pointers preserve meaningful
// absence separately from zero.
func TestFinalV1PayloadFieldTypesAndTags(t *testing.T) {
	intType := reflect.TypeOf(int(0))
	floatType := reflect.TypeOf(float64(0))
	stringType := reflect.TypeOf("")

	assertPayloadField(t, reflect.TypeOf(VehicleRUD{}), "PartCount", intType, "part_count")
	assertPayloadField(t, reflect.TypeOf(FlightStarted{}), "EngineCount", reflect.PointerTo(intType), "engine_count")
	assertPayloadField(t, reflect.TypeOf(KittenTumble{}), "From", stringType, "from")

	orbit := reflect.TypeOf(VehicleOrbit{})
	for _, field := range []struct {
		name string
		tag  string
	}{
		{name: "SmaM", tag: "sma_m"},
		{name: "LanDeg", tag: "lan_deg"},
		{name: "ArgpDeg", tag: "argp_deg"},
		{name: "TPe", tag: "t_pe"},
		{name: "PeriodS", tag: "period_s"},
	} {
		assertPayloadField(t, orbit, field.name, floatType, field.tag)
	}

	assertPayloadField(
		t, reflect.TypeOf(TelemetryWindow{}), "State", reflect.TypeOf((*StateVec)(nil)), "state")
	assertPayloadField(t, reflect.TypeOf(StateVec{}), "Pos", reflect.TypeOf(Vec3{}), "pos")
	assertPayloadField(t, reflect.TypeOf(StateVec{}), "Vel", reflect.TypeOf(Vec3{}), "vel")
	vec := reflect.TypeOf(Vec3{})
	assertPayloadField(t, vec, "X", floatType, "x")
	assertPayloadField(t, vec, "Y", floatType, "y")
	assertPayloadField(t, vec, "Z", floatType, "z")
}

func TestFinalV1PayloadDecodeRejectsNumericOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  string
		raw  string
	}{
		{name: "rud part count", typ: "vehicle.rud", raw: `{"part_count":1e1000}`},
		{name: "flight engine count", typ: "flight.started", raw: `{"engine_count":1e1000}`},
		{name: "orbit sma", typ: "vehicle.orbit", raw: `{"sma_m":1e1000}`},
		{name: "orbit lan", typ: "vehicle.orbit", raw: `{"lan_deg":1e1000}`},
		{name: "orbit argp", typ: "vehicle.orbit", raw: `{"argp_deg":1e1000}`},
		{name: "orbit periapsis time", typ: "vehicle.orbit", raw: `{"t_pe":1e1000}`},
		{name: "orbit period", typ: "vehicle.orbit", raw: `{"period_s":1e1000}`},
		{name: "state position x", typ: "telemetry.window", raw: stateWith("pos", "x", "1e1000")},
		{name: "state position y", typ: "telemetry.window", raw: stateWith("pos", "y", "1e1000")},
		{name: "state position z", typ: "telemetry.window", raw: stateWith("pos", "z", "1e1000")},
		{name: "state velocity x", typ: "telemetry.window", raw: stateWith("vel", "x", "1e1000")},
		{name: "state velocity y", typ: "telemetry.window", raw: stateWith("vel", "y", "1e1000")},
		{name: "state velocity z", typ: "telemetry.window", raw: stateWith("vel", "z", "1e1000")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodePayload(tc.typ, json.RawMessage(tc.raw)); err == nil {
				t.Fatalf("decodePayload(%s) accepted numeric overflow: %s", tc.typ, tc.raw)
			}
		})
	}
}

func assertPayloadField(t *testing.T, owner reflect.Type, name string, wantType reflect.Type, wantTag string) {
	t.Helper()
	field, ok := owner.FieldByName(name)
	if !ok {
		t.Fatalf("%s.%s is missing", owner.Name(), name)
	}
	if field.Type != wantType {
		t.Errorf("%s.%s type = %s, want %s", owner.Name(), name, field.Type, wantType)
	}
	if got := field.Tag.Get("json"); got != wantTag {
		t.Errorf("%s.%s json tag = %q, want %q", owner.Name(), name, got, wantTag)
	}
}

func stateWith(group, component, value string) string {
	components := map[string]map[string]string{
		"pos": {"x": "0", "y": "0", "z": "0"},
		"vel": {"x": "0", "y": "0", "z": "0"},
	}
	components[group][component] = value
	return `{"state":{"pos":{"x":` + components["pos"]["x"] +
		`,"y":` + components["pos"]["y"] + `,"z":` + components["pos"]["z"] +
		`},"vel":{"x":` + components["vel"]["x"] +
		`,"y":` + components["vel"]["y"] + `,"z":` + components["vel"]["z"] + `}}}`
}
