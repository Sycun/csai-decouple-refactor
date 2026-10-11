package handler

import (
	"reflect"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
)

// The RoleConfig schema in openapi_components.go had drifted from the struct it documents: it
// still advertised a systemPrompt the struct never had, spelled user_prompt in camelCase, and
// was missing icon / mcps / workflow_* entirely. Nothing compared the two, so the drift stayed
// invisible until a client generated from /api/openapi.json omitted a field the API accepts.
//
// This gate derives the expected property set from the struct's own json tags. Adding a field to
// config.RoleConfig - ai_channel included - now fails until the schema documents it, which is
// the point: the schema is documentation of a real wire shape, not a separate opinion about it.
func TestOpenAPIRoleConfigMatchesStruct(t *testing.T) {
	schemas, ok := openAPIComponents()["schemas"].(map[string]interface{})
	if !ok {
		t.Fatal("openAPIComponents has no schemas map")
	}
	schema, ok := schemas["RoleConfig"].(map[string]interface{})
	if !ok {
		t.Fatal("openAPIComponents.schemas has no RoleConfig object schema")
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("RoleConfig schema has no properties map")
	}
	if len(props) == 0 {
		t.Fatal("RoleConfig schema documents no properties; the read is broken")
	}

	tt := reflect.TypeOf(config.RoleConfig{})
	documented := make(map[string]bool, len(props))
	for name := range props {
		documented[name] = true
	}
	structural := make(map[string]bool, tt.NumField())
	for i := 0; i < tt.NumField(); i++ {
		name := strings.Split(tt.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		structural[name] = true
	}
	if len(structural) == 0 {
		t.Fatal("config.RoleConfig has no json-tagged fields; the reflection is broken")
	}

	// difference is the package's existing set-difference helper; both directions matter, since
	// a stale property is as misleading as a missing one.
	if missing, extra := difference(structural, documented), difference(documented, structural); len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("OpenAPI RoleConfig properties != config.RoleConfig json tags.\n"+
			"  missing from the schema: %v\n"+
			"  in the schema but not on the struct: %v\n"+
			"The schema is what clients generate from; it has to list the wire shape the API actually serves.",
			missing, extra)
	}

	for name, prop := range props {
		m, ok := prop.(map[string]interface{})
		if !ok {
			t.Fatalf("RoleConfig.%s is not an object", name)
		}
		typ, _ := m["type"].(string)
		if typ == "" {
			t.Errorf("RoleConfig.%s has no type", name)
		}
		if desc, _ := m["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("RoleConfig.%s has no description", name)
		}
		if typ == "array" {
			if _, ok := m["items"].(map[string]interface{}); !ok {
				t.Errorf("RoleConfig.%s is an array without items", name)
			}
		}
	}
}
