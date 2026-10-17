package mcpapi

import (
	"strings"
	"testing"
)

func TestReadHoistsOptionsNestedInTarget(t *testing.T) {
	arguments := map[string]any{"target": map[string]any{"path": "a.go", "start_line": float64(3), "end_line": float64(9), "view": "source"}}
	warnings := ApplyArgumentAliases("read", arguments)
	if arguments["start_line"] != float64(3) || arguments["end_line"] != float64(9) || arguments["view"] != "source" {
		t.Fatalf("options were not hoisted: %v", arguments)
	}
	if target := arguments["target"].(map[string]any); len(target) != 1 || target["path"] != "a.go" {
		t.Fatalf("target = %v, want {path}", target)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "top-level") {
		t.Fatalf("warnings = %v", warnings)
	}
	if err := ValidateCall("read", readSchemaForTest(t), arguments); err != nil {
		t.Fatalf("hoisted call is refused: %v", err)
	}
}

func TestReadKeepsATopLevelOptionOverANestedOne(t *testing.T) {
	arguments := map[string]any{"start_line": float64(1), "target": map[string]any{"path": "a.go", "start_line": float64(5)}}
	ApplyArgumentAliases("read", arguments)
	if arguments["start_line"] != float64(1) {
		t.Fatalf("top-level start_line was overwritten: %v", arguments)
	}
	if err := ValidateCall("read", readSchemaForTest(t), arguments); err == nil {
		t.Fatal("a conflicting nested start_line should still be refused")
	}
}

func TestReadAcceptsAStringTargetAsPath(t *testing.T) {
	arguments := map[string]any{"target": "internal/a.go"}
	ApplyArgumentAliases("read", arguments)
	if target, _ := arguments["target"].(map[string]any); target["path"] != "internal/a.go" {
		t.Fatalf("target = %v", arguments["target"])
	}
}

func TestSearchClampsLimitToItsMaximum(t *testing.T) {
	arguments := map[string]any{"query": "x", "limit": float64(500)}
	warnings := ApplyArgumentAliases("search", arguments)
	if arguments["limit"] != float64(200) || len(warnings) != 1 {
		t.Fatalf("limit = %v, warnings = %v", arguments["limit"], warnings)
	}
}

func TestSearchLeavesAFractionalLimitForValidation(t *testing.T) {
	arguments := map[string]any{"query": "x", "limit": 200.5}
	if warnings := ApplyArgumentAliases("search", arguments); len(warnings) != 0 || arguments["limit"] != 200.5 {
		t.Fatalf("limit = %v, warnings = %v", arguments["limit"], warnings)
	}
}

func TestMalformedJSONHintOmitsTheCallersText(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"operation": map[string]any{"type": "object"}}}
	err := ValidateToolArguments(schema, map[string]any{"operation": "{\"secret_token\": tru"})
	if err == nil || strings.Contains(err.Error(), "secret_token") || strings.Contains(err.Error(), "tru\"") {
		t.Fatalf("hint repeats caller text: %v", err)
	}
	err = ValidateToolArguments(schema, map[string]any{"operation": "[1e1000]"})
	if err == nil || strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("a range error is not malformed JSON: %v", err)
	}
}

func TestMalformedJSONObjectSaysWhereItBroke(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"operation": map[string]any{"type": "object"}}}
	err := ValidateToolArguments(schema, map[string]any{"operation": "{\"kind\": \"replace_literal\", \"new\": \"a\nb\"}"})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") || !strings.Contains(err.Error(), "at byte") {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchReadsIncludeAsPaths(t *testing.T) {
	schema := searchSchema()
	arguments := map[string]any{"query": "x", "include": "*.go"}
	warnings := ApplyArgumentAliases("search", arguments)
	if paths, _ := arguments["paths"].([]any); len(paths) != 1 || paths[0] != "*.go" || arguments["include"] != nil {
		t.Fatalf("include was not read as paths: %v", arguments)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "include is not a search argument") {
		t.Fatalf("warnings = %v", warnings)
	}
	if err := ValidateCall("search", schema, arguments); err != nil {
		t.Fatalf("aliased call is refused: %v", err)
	}
	// Beside paths it is ambiguous, and the refusal now points at paths
	// rather than at include_handles.
	ambiguous := map[string]any{"query": "x", "include": "*.go", "paths": []any{"cmd"}}
	if warnings := ApplyArgumentAliases("search", ambiguous); len(warnings) != 0 {
		t.Fatalf("include beside paths was rewritten: %v", ambiguous)
	}
	err := ValidateCall("search", schema, ambiguous)
	if err == nil || !strings.Contains(err.Error(), "paths") || strings.Contains(err.Error(), "include_handles") {
		t.Fatalf("include beside paths refusal = %v", err)
	}
	// include together with path is two guesses at one scope.
	two := map[string]any{"query": "x", "include": "*.go", "path": "cmd"}
	if warnings := ApplyArgumentAliases("search", two); len(warnings) != 0 || two["paths"] != nil {
		t.Fatalf("two scope aliases were merged: %v", two)
	}
}

func readSchemaForTest(t *testing.T) map[string]any {
	t.Helper()
	for _, descriptor := range Tools {
		if descriptor.Name == "read" {
			return descriptor.InputSchema
		}
	}
	t.Fatal("read is not in the catalog")
	return nil
}
