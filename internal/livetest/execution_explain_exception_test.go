//go:build live

package livetest

import (
	"path/filepath"
	"testing"
)

func TestExecutionExplainPanicBoundaryRemainsPartial(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "explain_go"))
	w := workspaceIdentity(t, call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root}))
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "panic-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "args": []string{"negative", "panic"}, "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "conditions", "capture_values": true, "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "guard"}}, "line_offset": 2}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": "panic-continue", "action": "continue", "wait_ms": 10000})
	call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "panic-stop", "action": "stop"})
	explained := call(t, session, "path_explain", map[string]any{"workspace_id": w, "mode": "combined", "trace_id": id, "from": "guard", "to": "sink", "use_provider": false})
	explanation, ok := data(explained)["explanation"].(map[string]any)
	if !ok || explanation["target_status"] != "not_observed" || outcome(explained) != "partial" {
		t.Fatalf("%v", explained)
	}
	found := false
	for _, raw := range explanation["conditions"].([]any) {
		condition := raw.(map[string]any)
		if condition["expression"] == "n == -2" && condition["value"] == true && condition["status"] == "inferred" {
			found = true
		}
	}
	if !found || len(data(explained)["boundary_candidates"].([]any)) == 0 {
		t.Fatalf("%v", explained)
	}
}
