//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestExecutionExplainGuardAndEarlyReturn(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "explain_go"))
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	w := workspaceIdentity(t, opened)
	for _, negative := range []bool{false, true} {
		key := fmt.Sprint(negative)
		args := []string{}
		if negative {
			args = append(args, "negative")
		}
		targets := []any{}
		for name, offset := range map[string]int{"guard": 5, "sink": 1} {
			targets = append(targets, map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": name}}, "line_offset": offset})
		}
		started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": key + "-start", "action": "start",
			"file": filepath.Join(root, "main.go"), "adapter": "delve", "args": args, "wait_ms": 10000,
			"trace_policy": map[string]any{"mode": "conditions", "capture_values": true, "targets": targets}})
		id, _ := data(started)["trace_id"].(string)
		if id == "" {
			t.Fatalf("%v", started)
		}
		last := started
		for i := 0; i < 4; i++ {
			if data(last)["debug"].(map[string]any)["state"] == "exited" {
				break
			}
			last = call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("%s-%d", key, i), "action": "continue", "wait_ms": 10000})
		}
		explained := call(t, session, "path_explain", map[string]any{"workspace_id": w, "mode": "combined", "trace_id": id, "from": "guard", "to": "sink", "use_provider": false})
		explanation, ok := data(explained)["explanation"].(map[string]any)
		if !ok {
			t.Fatalf("%v", explained)
		}
		expected := "observed"
		if negative {
			expected = "not_observed"
		}
		if explanation["target_status"] != expected {
			t.Fatalf("%v", explanation)
		}
		found := false
		for _, raw := range explanation["conditions"].([]any) {
			condition := raw.(map[string]any)
			if condition["expression"] == "n < 0" && condition["status"] == "inferred" && condition["value"] == negative {
				found = true
			}
		}
		if !found {
			t.Fatalf("guard snapshot not explained: %v", explanation)
		}
	}
}
