package mcpapi

// leanSchema is a complete closed input override, not a permission bypass.
// The same handlers enforce literal match counts, missing-path creation,
// overwrite revisions and delete revision/hash guards after validation.
func leanSchema(name string) map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	positive := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1} }
	switch name {
	case "read":
		return schemaObject(map[string]any{
			"root": text(), "target": schemaObject(map[string]any{"path": text()}, "path"),
			"start_line": positive(), "end_line": positive(), "max_lines": positive(),
			"view": enumSchema("source", "outline"),
		}, "target")
	case "search":
		return schemaObject(map[string]any{
			"root": text(), "query": text(), "mode": enumSchema("literal", "regex"),
			"paths":         map[string]any{"type": "array", "items": text()},
			"context_lines": map[string]any{"type": "integer", "minimum": 0},
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})
	case "edit_apply":
		return schemaObject(map[string]any{
			"root": text(),
			"operation": schemaObject(map[string]any{
				"kind": enumSchema("replace_literal", "create_file", "delete_file"),
				"path": text(), "old": text(), "new": text(), "content": text(),
				"expected_count": positive(), "revision_id": text(), "expected_sha256": text(),
				"replace": map[string]any{"type": "boolean"},
			}, "kind", "path"),
		}, "operation")
	}
	return nil
}

var leanDescriptions = map[string]string{
	"read":       "Read a path or line window; returns revision_id. max_lines caps output; view=outline lists declarations.",
	"search":     "Find literal text (default) or regex. paths scopes hits; context_lines adds up to 20 lines. Empty query lists paths.",
	"edit_apply": "Edit one file. replace_literal requires old/new and exact expected_count (default 1). create_file needs content; overwrite needs replace and revision_id. delete_file needs revision_id from read or expected_sha256. Returns revisions and diagnostics. Pass root for repository writes.",
}
