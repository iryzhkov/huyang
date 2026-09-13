//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"testing"
)

func captureComparisonTrace(t *testing.T, session sessionHandle, root, w, key string, args []string) string {
	t.Helper()
	if args == nil {
		args = []string{}
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
			return id
		}
		last = call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("%s-%d", key, i), "action": "continue", "wait_ms": 10000})
	}
	t.Fatalf("comparison trace did not exit: %v", last)
	return ""
}
func TestExecutionComparePassingFailingRelocationAndSignature(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "explain_go"))
	w := workspaceIdentity(t, call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root}))
	call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "return", "new": "os.Exit(1)"}})
	passing := captureComparisonTrace(t, session, root, w, "passing", nil)
	failing := captureComparisonTrace(t, session, root, w, "failing", []string{"fail"})
	compare := func(a, b string) map[string]any {
		result := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "compare_traces", "passing_trace_id": a, "failing_trace_id": b})
		comparison, ok := data(result)["comparison"].(map[string]any)
		if !ok {
			t.Fatalf("%v", result)
		}
		return comparison
	}
	comparison := compare(passing, failing)
	if comparison["common_prefix_stops"].(float64) < 1 || comparison["cause_status"] != "unknown" {
		t.Fatalf("%v", comparison)
	}
	valueDifference := false
	for _, raw := range comparison["differences"].([]any) {
		difference := raw.(map[string]any)
		if difference["kind"] == "value" && difference["name"] == "n" && difference["status"] == "observed" {
			valueDifference = true
		}
	}
	if !valueDifference {
		t.Fatalf("%v", comparison)
	}
	call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "package main", "new": "package main\n\n// relocated layout"}})
	relocated := captureComparisonTrace(t, session, root, w, "relocated", nil)
	comparison = compare(passing, relocated)
	if comparison["alignment"] != "token_relocated" || comparison["common_prefix_stops"].(float64) < 2 {
		t.Fatalf("%v", comparison)
	}
	call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "guard(n int)", "new": "guard(n int, unused ...int)"}})
	incompatible := captureComparisonTrace(t, session, root, w, "signature", nil)
	comparison = compare(passing, incompatible)
	if comparison["alignment"] != "ambiguous" || comparison["common_prefix_stops"] != float64(0) {
		t.Fatalf("%v", comparison)
	}
}
