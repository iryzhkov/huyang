package mcpapi

import (
	"strings"
	"testing"
)

func TestCompactPlanPreservesAcceptanceEvidence(t *testing.T) {
	plan := map[string]any{
		"plan_id": "plan_a", "plan_revision": 2, "state": "PROVISIONAL",
		"invariants":  []any{map[string]any{"status": "unknown", "required": true}},
		"operations":  strings.Repeat("large-operation", 10000),
		"preparation": map[string]any{"prepared_revision": "prep_a", "verification": []any{map[string]any{"status": "failed"}}, "missing_coverage": []string{"alias"}, "tool_delta": strings.Repeat("delta", 10000)},
	}
	envelope := map[string]any{"outcome": "provisional", "code": "invariant_not_proven", "data": map[string]any{"plan": plan}, "next": []any{map[string]any{"action": "discard"}}}
	result := CompactReceipt("change_plan", map[string]any{"response_mode": "compact"}, envelope)
	receipt := result["data"].(map[string]any)
	got := receipt["plan"].(map[string]any)
	prep := got["preparation"].(map[string]any)
	if got["operations"] != nil || prep["tool_delta"] != nil {
		t.Fatal("large details retained")
	}
	if got["invariants"] == nil || prep["verification"] == nil || prep["missing_coverage"] == nil || result["code"] != "invariant_not_proven" || result["next"] == nil {
		t.Fatal("lost acceptance evidence")
	}
	if plan["operations"] == nil {
		t.Fatal("changed durable record")
	}
}
