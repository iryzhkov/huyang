package handlers

import (
	"context"
	"strings"
	"testing"
)

func TestProviderPlanLocatorMissOffersExactHandleRecovery(t *testing.T) {
	h, workspaceID, _ := newRefactorFixture(t, &refactorProvider{})
	result := h.Execute(context.Background(), "locator_recovery", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "missing_alias",
		"operations": []any{map[string]any{
			"op_id": "rename", "kind": "rename_symbol", "content": "NewName",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "MissingAlias"}},
		}},
	})
	summary, _ := result["summary"].(string)
	for _, want := range []string{"resolved to 0 declarations", "mode=literal", "include_handles=true", "target.handle"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing %q: %#v", want, result)
		}
	}
}
