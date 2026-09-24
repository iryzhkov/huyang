package mcpapi

import (
	"errors"
	"strings"
	"testing"
)

func toolSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, descriptor := range Tools {
		if descriptor.Name == name {
			return descriptor.InputSchema
		}
	}
	t.Fatalf("the catalog has no %s tool", name)
	return nil
}

func refusal(t *testing.T, tool string, arguments map[string]any) *ArgumentError {
	t.Helper()
	err := ValidateToolArguments(toolSchema(t, tool), arguments)
	var argument *ArgumentError
	if !errors.As(err, &argument) {
		t.Fatalf("%s %v: refusal = %v, want an ArgumentError", tool, arguments, err)
	}
	return argument
}

// A rejected property is pointed at the sibling the caller meant before any
// property of the same name a level down: search's path means paths, and
// sending the caller to refine.path turned one wrong call into two.
func TestAnUnknownPropertyIsPointedAtTheSiblingItMeant(t *testing.T) {
	cases := []struct {
		tool, key, want string
	}{
		{"search", "path", "paths"},
		{"search", "path_prefix", "paths"},
		{"search", "max_results", "limit"},
		{"search", "symbol", "query"},
		{"search", "context_line", "context_lines"},
		{"revision_diff", "to_revision", "to_revision_or_current"},
		{"read", "symbol", "target.symbol_locator"},
	}
	valid := map[string]map[string]any{
		"search":        {"query": "q"},
		"read":          {},
		"revision_diff": {"from_revision": "wsrev_1", "to_revision_or_current": "current"},
	}
	for _, test := range cases {
		arguments := map[string]any{"workspace_id": "ws_test", test.key: "x"}
		for key, value := range valid[test.tool] {
			arguments[key] = value
		}
		got := refusal(t, test.tool, arguments)
		if got.Suggestion != test.want || !strings.Contains(got.Error(), test.want) {
			t.Errorf("%s %s: suggestion %q in %q, want %q", test.tool, test.key, got.Suggestion, got.Error(), test.want)
		}
		if got.Path != test.key {
			t.Errorf("%s %s: argument = %q", test.tool, test.key, got.Path)
		}
	}
	if got := refusal(t, "search", map[string]any{"query": "q", "path": "x"}); strings.Contains(got.Error(), "refine.path") {
		t.Fatalf("search path is still sent to refine.path: %v", got)
	}
}

// A target that matches none of its shapes is told which one it came
// closest to and what is wrong with it there.
func TestAOneOfRefusalNamesTheClosestShape(t *testing.T) {
	got := refusal(t, "read", map[string]any{
		"workspace_id": "ws_test", "target": map[string]any{"symbol_locator": map[string]any{"path": "main.go"}},
	})
	if !strings.Contains(got.Error(), "closest is {symbol_locator: {name_path, path}}") || !strings.Contains(got.Error(), `missing required property "name_path"`) {
		t.Fatalf("closest-shape refusal = %v", got)
	}
	if got.Path != "target.symbol_locator.name_path" {
		t.Fatalf("argument = %q", got.Path)
	}

	got = refusal(t, "read", map[string]any{"workspace_id": "ws_test", "target": map[string]any{"file": "main.go"}})
	for _, shape := range []string{"{handle}", "{path}", "{symbol_locator: {name_path, path}}"} {
		if !strings.Contains(got.Error(), shape) {
			t.Fatalf("a target like no shape does not list %s: %v", shape, got)
		}
	}
	if strings.Contains(got.Error(), "closest") {
		t.Fatalf("a target that shares nothing with any shape was given a closest one: %v", got)
	}
}

func TestDescribeShapeMarksOptionalPropertiesAndAlternatives(t *testing.T) {
	shape := DescribeShape(toolSchema(t, "evidence_get"))
	if !strings.HasPrefix(shape, "{evidence_id, ") || !strings.Contains(shape, "cursor?") {
		t.Fatalf("evidence_get shape = %s", shape)
	}
	if got := DescribeShape(targetSchema()); got != "{handle} | {file_range: {after_sha256, anchor_bytes, before_sha256, byte_end, byte_start, expected_sha256, path, revision_id}} | {symbol_locator: {name_path, path}}" {
		t.Fatalf("target shape = %s", got)
	}
}
