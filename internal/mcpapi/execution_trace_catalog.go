package mcpapi

func executionTracePolicySchema() map[string]any {
	return schemaObject(map[string]any{
		"mode":           enumSchema("stops", "path", "conditions", "mutations"),
		"capture_values": map[string]any{"type": "boolean", "description": "Persist bounded locals; default false. Sensitive names are redacted."},
		"max_events":     map[string]any{"type": "integer", "minimum": 1, "maximum": 10000},
		"redact_names":   map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string", "maxLength": 128}},
		"targets":        map[string]any{"type": "array", "maxItems": 128, "items": schemaObject(map[string]any{"target": modernDebugTargetSchema(), "line_offset": map[string]any{"type": "integer", "minimum": 1}}, "target")},
	})
}
