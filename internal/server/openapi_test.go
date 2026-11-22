package server

import (
	"encoding/json"
	"testing"
)

// The text fields are deprecated for M2 scripts in the OpenAPI description (ADR-007 C11): the UI uses codes and
// params, which the VM list carries.
func TestOpenAPIDeprecatedText(t *testing.T) {
	f := newFixture(t)
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(f.s.openapi, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ schema, field string }{{"Reason", "text"}, {"VMListRow", "reason"}, {"VMStatus", "notes"},
		{"AttentionExample", "reason"}} {
		if p := doc.Components.Schemas[c.schema].Properties[c.field]; p["deprecated"] != true {
			t.Errorf("%s.%s is not deprecated: %v", c.schema, c.field, p)
		}
	}
	if p := doc.Components.Schemas["VMListRow"].Properties["params"]; p["$ref"] != "#/components/schemas/Params" {
		t.Errorf("VMListRow.params: %v", p)
	}
	if p := doc.Components.Schemas["VMListRow"].Properties["code"]; p["deprecated"] != nil {
		t.Errorf("VMListRow.code is deprecated: %v", p)
	}
}
