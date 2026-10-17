package mcpapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestEditIsExactFullAlias(t *testing.T) {
	full, err := CatalogJSON(ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := CatalogJSON(ProfileEdit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full, edit) || !reflect.DeepEqual(Catalog(ProfileFull), Catalog(ProfileEdit)) {
		t.Fatal("edit must preserve full's order, descriptions, schemas, annotations and discovery metadata")
	}
}

func TestLeanSchemasAreClosedOverrides(t *testing.T) {
	want := map[string][]string{
		"read":       {"root", "target", "start_line", "end_line", "max_lines", "view"},
		"search":     {"root", "query", "mode", "paths", "context_lines", "limit"},
		"edit_apply": {"root", "operation"},
	}
	if !slices.Equal(CatalogNames(ProfileLean), []string{"read", "search", "edit_apply"}) {
		t.Fatalf("lean tools = %v", CatalogNames(ProfileLean))
	}
	before, _ := CatalogJSON(ProfileFull)
	for _, tool := range Catalog(ProfileLean) {
		if tool.InputSchema["additionalProperties"] != false || tool.InputSchema["$schema"] != jsonSchemaDialect {
			t.Fatalf("%s is not a closed top-level schema", tool.Name)
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		if len(properties) != len(want[tool.Name]) {
			t.Fatalf("%s properties = %v", tool.Name, properties)
		}
		for _, name := range want[tool.Name] {
			if properties[name] == nil {
				t.Fatalf("%s missing %s", tool.Name, name)
			}
		}
	}
	after, _ := CatalogJSON(ProfileFull)
	if !bytes.Equal(before, after) {
		t.Fatal("lean changed the full catalog")
	}

	schema := leanSchema("edit_apply")
	operation := schema["properties"].(map[string]any)["operation"].(map[string]any)
	properties := operation["properties"].(map[string]any)
	for _, guard := range []string{"revision_id", "expected_sha256", "expected_count", "replace"} {
		if properties[guard] == nil {
			t.Fatalf("lean lost %s guard", guard)
		}
	}
	if operation["additionalProperties"] != false {
		t.Fatal("lean operation is open")
	}
}

func TestLeanValidationRefusesFullOnlyArguments(t *testing.T) {
	for _, tc := range []struct{ name, tool, raw string }{
		{"workspace", "read", `{"workspace_id":"ws","target":{"path":"a.txt"}}`},
		{"batch", "read", `{"targets":[{"path":"a.txt"}]}`},
		{"handle", "read", `{"target":{"handle":"rng_x"}}`},
		{"symbol", "read", `{"target":{"symbol_locator":{"path":"a.go","name_path":"A"}}}`},
		{"history", "read", `{"target":{"path":"a.txt"},"view":"history"}`},
		{"semantic", "search", `{"query":"A","mode":"references"}`},
		{"refinement", "search", `{"result_set_handle":"set_x"}`},
		{"response", "search", `{"query":"a","response_format":"text"}`},
		{"batch_edit", "edit_apply", `{"operations":[{"kind":"delete_file","path":"a.txt"}]}`},
		{"transfer", "edit_apply", `{"operation":{"kind":"move_file","path":"a.txt","to":"b.txt"}}`},
		{"force", "edit_apply", `{"operation":{"kind":"delete_file","path":"a.txt","force":true}}`},
		{"missing_path", "edit_apply", `{"operation":{"kind":"replace_literal","old":"a","new":"b"}}`},
		{"format", "edit_apply", `{"operation":{"kind":"create_file","path":"a","content":"b"},"format":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			if err := ValidateCall(tc.tool, leanSchema(tc.tool), args); err == nil {
				t.Fatalf("lean accepted %s", tc.raw)
			}
		})
	}
	for _, tc := range []struct{ tool, raw string }{
		{"read", `{"root":"/repo","target":{"path":"a.txt"},"max_lines":5}`},
		{"search", `{"root":"/repo","query":"a","mode":"regex","limit":3,"context_lines":2}`},
		{"search", `{"root":"/repo","paths":["pkg"]}`},
		{"edit_apply", `{"root":"/repo","operation":{"kind":"replace_literal","path":"a.txt","old":"a","new":"b","expected_count":1}}`},
		{"edit_apply", `{"root":"/repo","operation":{"kind":"create_file","path":"a.txt","content":"b","replace":true,"revision_id":"docrev_x"}}`},
		{"edit_apply", `{"root":"/repo","operation":{"kind":"delete_file","path":"a.txt","revision_id":"docrev_x"}}`},
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
			t.Fatal(err)
		}
		if err := ValidateCall(tc.tool, leanSchema(tc.tool), args); err != nil {
			t.Fatal(err)
		}
	}
}
