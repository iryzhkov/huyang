package handlers

import (
	"context"
	"strings"
	"testing"
)

func TestSearchContextBoundaryDeliveryAndWarning(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"one.txt": strings.Repeat("before\n", 30) + "needle\n" + strings.Repeat("after\n", 30)})
	for _, bound := range []int{0, 20, 21} {
		result := h.Execute(context.Background(), "context_boundary", "search", map[string]any{"workspace_id": id, "query": "needle", "context_lines": bound})
		hits := result["data"].(map[string]any)["hits"].([]map[string]any)
		if result["outcome"] != "ok" || len(hits) != 1 {
			t.Fatalf("context %d: %#v", bound, result)
		}
		body, _ := hits[0]["context"].(string)
		warnings, _ := result["warnings"].([]string)
		if bound == 0 && body != "" {
			t.Fatalf("zero context delivered %q", body)
		}
		if bound > 0 && strings.Count(body, "\n") != 41 {
			t.Fatalf("context %d delivered %d lines", bound, strings.Count(body, "\n"))
		}
		if bound <= 20 && len(warnings) != 0 {
			t.Fatalf("context %d warned %#v", bound, warnings)
		}
		if bound == 21 && (len(warnings) != 1 || !strings.Contains(warnings[0], "reduced to 20") || !strings.Contains(warnings[0], "start_line and end_line")) {
			t.Fatalf("clamp hidden: %#v", result)
		}
	}
}
