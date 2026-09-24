package mcpapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnambiguousAliasesAreRewrittenWithAWarning(t *testing.T) {
	cases := []struct {
		tool    string
		sent    map[string]any
		want    map[string]any
		warning string
	}{
		{"read", map[string]any{"path": "go.mod", "start_line": 2.0},
			map[string]any{"target": map[string]any{"path": "go.mod"}, "start_line": 2.0}, "target.path"},
		{"read", map[string]any{"symbol_locator": map[string]any{"path": "a.go", "name_path": "A"}},
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "a.go", "name_path": "A"}}}, "target.symbol_locator"},
		{"search", map[string]any{"query": "q", "path": "internal/"},
			map[string]any{"query": "q", "paths": []any{"internal/"}}, "read as paths"},
		{"search", map[string]any{"query": "q", "path_prefix": []any{"a/", "b/"}},
			map[string]any{"query": "q", "paths": []any{"a/", "b/"}}, "path_prefix"},
		{"search", map[string]any{"query": "q", "max_results": 5.0},
			map[string]any{"query": "q", "limit": 5.0}, "read as limit"},
		{"revision_diff", map[string]any{"from_revision": "wsrev_1", "to_revision": "current"},
			map[string]any{"from_revision": "wsrev_1", "to_revision_or_current": "current"}, "to_revision_or_current"},
	}
	for _, test := range cases {
		warnings := ApplyArgumentAliases(test.tool, test.sent)
		if !reflect.DeepEqual(test.sent, test.want) {
			t.Errorf("%s: rewritten = %#v, want %#v", test.tool, test.sent, test.want)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], test.warning) {
			t.Errorf("%s: warnings = %q, want one naming %s", test.tool, warnings, test.warning)
		}
		if err := ValidateToolArguments(toolSchema(t, test.tool), test.sent); err != nil {
			t.Errorf("%s: the rewritten arguments are refused: %v", test.tool, err)
		}
	}
}

// A rewrite that could overwrite what the caller spelled correctly, or pick
// between two meanings, is not made; validation refuses it instead.
func TestAmbiguousAliasesAreLeftForValidation(t *testing.T) {
	cases := []struct {
		tool string
		sent map[string]any
	}{
		{"read", map[string]any{"path": "a", "target": map[string]any{"path": "b"}}},
		{"read", map[string]any{"path": "a", "targets": []any{map[string]any{"path": "b"}}}},
		{"read", map[string]any{"path": "a", "symbol_locator": map[string]any{"path": "b", "name_path": "B"}}},
		{"search", map[string]any{"query": "q", "path": "a", "paths": []any{"b"}}},
		{"search", map[string]any{"query": "q", "path": "a", "path_prefix": "b"}},
		{"search", map[string]any{"query": "q", "path": []any{}}},
		{"search", map[string]any{"query": "q", "max_results": 5.0, "limit": 7.0}},
		{"navigate", map[string]any{"relation": "definition", "path": "a"}},
	}
	for _, test := range cases {
		before := CloneEnvelope(test.sent)
		if warnings := ApplyArgumentAliases(test.tool, test.sent); len(warnings) != 0 || !reflect.DeepEqual(before, test.sent) {
			t.Errorf("%s %v was rewritten to %v with %q", test.tool, before, test.sent, warnings)
		}
	}
}
