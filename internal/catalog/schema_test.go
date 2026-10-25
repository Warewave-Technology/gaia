package catalog

import "testing"

func TestSchema(t *testing.T) {
	n := Schema(KindNetwork)
	if n == nil || n.Type != "object" {
		t.Fatalf("network = %+v", n)
	}
	by := map[string]Field{}
	for _, f := range n.Fields {
		by[f.Key] = f
	}
	if f := by["site"]; f.Type != "string" || !f.Required || f.Ref != KindSite {
		t.Errorf("site = %+v", f)
	}
	if f := by["ipam"]; f.Type != "object" || len(f.Fields) != 2 || f.Fields[1].Key != "pool" {
		t.Errorf("ipam = %+v", f)
	}
	if f := by["allowedEnvironments"]; f.Type != "list" || f.Items.Type != "string" {
		t.Errorf("allowedEnvironments = %+v", f)
	}
	p := Schema(KindProvider)
	if p.Fields[0].Key != "type" || len(p.Fields[0].Enum) != len(ProviderTypes) {
		t.Errorf("provider type = %+v", p.Fields[0])
	}
	img := Schema(KindImage)
	for _, f := range img.Fields {
		if f.Key == "templates" && (f.Items.Type != "object" || f.Items.Fields[0].Ref != KindProvider) {
			t.Errorf("templates = %+v", f.Items)
		}
	}
	for _, k := range Kinds() {
		if Schema(k) == nil {
			t.Errorf("no schema for %s", k)
		}
	}
	if Schema("Nope") != nil {
		t.Error("an unknown kind")
	}
}
