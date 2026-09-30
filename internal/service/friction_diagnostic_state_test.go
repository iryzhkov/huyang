package service

import (
	"reflect"
	"testing"
)

func TestFrictionDiagnosticStateSelectsOnlySafeFields(t *testing.T) {
	data := map[string]any{
		"canonical_changed": false,
		// This cannot be JSON-marshaled. Metadata extraction must not traverse
		// unrelated content, including source-bearing read/search bodies.
		"content":      make(chan int),
		"verification": map[string]any{"confidence": "unavailable", "reasons": []string{"lsp_not_configured", "/private/path", "code_shaped_secret"}},
	}
	codes, confidence, changed := frictionDiagnosticState(data)
	if !reflect.DeepEqual(codes, []string{"diagnostic_reason_unclassified", "lsp_not_configured"}) || confidence != "unavailable" || changed == nil || *changed {
		t.Fatalf("state = %v %q %v", codes, confidence, changed)
	}
	codes, confidence, changed = frictionDiagnosticState(map[string]any{"verification": map[string]any{"confidence": "secret-value", "reason_codes": []string{"push_missing_current_document_proof"}}})
	if !reflect.DeepEqual(codes, []string{"push_missing_current_document_proof"}) || confidence != "" || changed != nil {
		t.Fatalf("unsafe state = %v %q %v", codes, confidence, changed)
	}
}
