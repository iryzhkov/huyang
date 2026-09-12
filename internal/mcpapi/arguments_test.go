package mcpapi

import "testing"

// Structured arguments that arrive as JSON text are decoded; string
// arguments that merely look like JSON are left alone.
func TestNormalizeArgumentsDecodesStructuredText(t *testing.T) {
	arguments := NormalizeArguments(map[string]any{
		"targets":   `[{"path": "a.go"}]`,
		"operation": `{"kind": "replace_literal", "old": "{", "new": "}"}`,
		"query":     `{"not": "decoded"}`,
		"stages":    "not json",
		"numbered":  "true",
		"end_line":  "12",
		"old":       "42",
	})
	if arguments["numbered"] != true || arguments["end_line"] != float64(12) || arguments["old"] != "42" {
		t.Fatalf("scalar text = %#v", arguments)
	}
	if targets, ok := arguments["targets"].([]any); !ok || len(targets) != 1 {
		t.Fatalf("targets = %#v", arguments["targets"])
	}
	if operation, ok := arguments["operation"].(map[string]any); !ok || operation["old"] != "{" {
		t.Fatalf("operation = %#v", arguments["operation"])
	}
	if _, ok := arguments["query"].(string); !ok {
		t.Fatalf("query string was decoded: %#v", arguments["query"])
	}
	if _, ok := arguments["stages"].(string); !ok {
		t.Fatalf("malformed text was replaced: %#v", arguments["stages"])
	}
}

// The finalised envelope omits an empty evidence block and a persisted
// receipt flag, and keeps both when they carry information.
func TestFinalizeEnvelopeOmitsNormalCaseFields(t *testing.T) {
	quiet := FinalizeEnvelope("read", map[string]any{"evidence": map[string]any{"ids": []string{}, "truncated": false}, "idempotency_persisted": true})
	if _, present := quiet["evidence"]; present {
		t.Fatalf("empty evidence kept: %#v", quiet)
	}
	if _, present := quiet["idempotency_persisted"]; present {
		t.Fatalf("persisted flag kept: %#v", quiet)
	}
	loud := FinalizeEnvelope("edit_apply", map[string]any{"evidence": map[string]any{"ids": []string{"ev_1"}, "truncated": false}, "idempotency_persisted": false})
	if loud["evidence"] == nil || loud["idempotency_persisted"] != false {
		t.Fatalf("informative fields dropped: %#v", loud)
	}
}
