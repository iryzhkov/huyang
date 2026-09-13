package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResponseFormatsAvoidDuplicateSource(t *testing.T) {
	payload := strings.Repeat("source-payload", 10000)
	envelope := map[string]any{"outcome": "ok", "summary": "Read min.txt", "data": map[string]any{"content": payload}}
	for _, format := range []string{"both", "text", "structured"} {
		result, err := renderToolResponse("read", map[string]any{"response_format": format}, envelope, false)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		count := strings.Count(string(wire), payload)
		want := 1
		if format == "both" {
			want = 2
		}
		if count != want {
			t.Fatalf("%s: source occurs %d times", format, count)
		}
	}
}

func TestCompactMutationRetainsEvidenceAndRefusals(t *testing.T) {
	envelope := map[string]any{
		"outcome": "provisional", "summary": "Edited; diagnostics unavailable", "workspace": map[string]any{"revision": "wsrev_2"},
		"evidence": map[string]any{"ids": []string{"ev_1"}}, "warnings": []string{"partial verification"},
		"next":               []any{map[string]any{"tool": "diagnostics"}},
		"data":               map[string]any{"canonical_changed": true, "revision": "wsrev_2", "verification": map[string]any{"confidence": "unavailable"}, "diffs": strings.Repeat("patch", 10000)},
		"diagnostic_updates": []any{map[string]any{"id": "diag_1"}},
	}
	result, err := renderToolResponse("edit_apply", map[string]any{"response_mode": "compact"}, envelope, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuredContent != nil {
		t.Fatal("compact mode duplicated receipt")
	}
	body := result.Content[0].(*mcp.TextContent).Text
	for _, value := range []string{"canonical_changed", "wsrev_2", "unavailable", "ev_1", "partial verification"} {
		if !strings.Contains(body, value) {
			t.Fatalf("lost %s", value)
		}
	}
	if strings.Contains(body, "patchpatch") || strings.Contains(body, "diag_1") {
		t.Fatal("verbose details in compact receipt")
	}
	if envelope["diagnostic_updates"] == nil || envelope["data"].(map[string]any)["diffs"] == nil {
		t.Fatal("mutated original evidence")
	}
	envelope["outcome"], envelope["code"] = "conflict", "stale_revision"
	result, err = renderToolResponse("edit_apply", map[string]any{"response_mode": "compact"}, envelope, true)
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "stale_revision") {
		t.Fatal("refusal lost")
	}
}
