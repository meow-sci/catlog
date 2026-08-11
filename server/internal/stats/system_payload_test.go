package stats

import (
	"encoding/json"
	"testing"
)

func TestSystemPayloadsDecodeWithOptionalOrbitSemantics(t *testing.T) {
	discovered, err := decodePayload("system.discovered", json.RawMessage(
		`{"system":"01kittensol","id":"Sol","name":"Sol","home":"earth","bodies":2,"complete":true}`))
	if err != nil {
		t.Fatal(err)
	}
	h := discovered.(SystemDiscovered)
	if h.System != "01kittensol" || h.Bodies != 2 || !h.Complete {
		t.Fatalf("decoded header = %+v", h)
	}

	root, err := decodePayload("system.body", json.RawMessage(
		`{"system":"01kittensol","body":"sol","name":"Sol","class":"StellarBody","kind":"star","rank":0,"radius_m":1,"mass_kg":2,"soi_m":0,"atmo_m":0,"ocean_m":0,"angvel":0,"axis":{"x":0,"y":1,"z":0},"ccf_to_cce_t0":{"x":0,"y":0,"z":0,"w":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b := root.(SystemBody)
	if b.Parent != nil || b.SmaM != nil || b.Ecc != nil || b.PeriodS != nil {
		t.Fatalf("root acquired absent optionals: %+v", b)
	}

	orbit, err := decodePayload("system.body", json.RawMessage(
		`{"system":"01kittensol","body":"earth","name":"Earth","class":"TerrestrialBody","kind":"planet","rank":1,"parent":"sol","radius_m":3,"mass_kg":4,"soi_m":5,"atmo_m":6,"ocean_m":0,"angvel":1,"axis":{"x":0,"y":1,"z":0},"ccf_to_cce_t0":{"x":0,"y":0,"z":0,"w":1},"sma_m":100,"ecc":0.1,"inc_deg":2,"lan_deg":3,"argp_deg":4,"t_pe":-5,"period_s":50}`))
	if err != nil {
		t.Fatal(err)
	}
	o := orbit.(SystemBody)
	if o.Parent == nil || *o.Parent != "sol" || o.SmaM == nil || *o.SmaM != 100 || o.PeriodS == nil || *o.PeriodS != 50 {
		t.Fatalf("decoded orbit = %+v", o)
	}
}
