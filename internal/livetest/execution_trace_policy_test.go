//go:build live

package livetest

import (
	"path/filepath"
	"strings"
	"testing"
)

func traceLiveSetup(t *testing.T) (sessionHandle, string, string) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "trace_go"))
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	return session, root, workspaceIdentity(t, opened)
}
func TestExecutionTraceValuesAndStaleMapping(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "values-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "conditions", "capture_values": true, "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": 5}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	if strings.Contains(renderJSON(t, inspected), "trace-secret-value") {
		t.Fatal("trace exposed a redacted value")
	}
	trace := data(inspected)["trace"].(map[string]any)
	redacted := false
	for _, raw := range trace["events"].([]any) {
		values, _ := raw.(map[string]any)["values"].([]any)
		for _, value := range values {
			item := value.(map[string]any)
			if item["name"] == "password" && item["redacted"] == true {
				redacted = true
			}
		}
	}
	if !redacted {
		t.Fatalf("no structured redacted local: %v", trace)
	}
	edited := call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "n * 2", "new": "n * 3"}})
	if data(edited)["canonical_changed"] != true {
		t.Fatalf("edit while stopped failed: %v", edited)
	}
	continued := call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": "values-continue", "action": "continue", "wait_ms": 10000})
	if data(continued)["trace_error"] != nil {
		t.Fatalf("%v", continued)
	}
	inspected = call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	if !strings.Contains(renderJSON(t, inspected), "source_changed_since_launch") {
		t.Fatal("source edit after build not marked stale")
	}
	stopped := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "values-stop", "action": "stop"})
	if data(stopped)["trace_error"] != nil {
		t.Fatalf("%v", stopped)
	}
}
func TestExecutionTracePreservesExistingBreakpointsAndDisabledBehavior(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	target := map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}
	placed := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": w, "idempotency_key": "existing", "action": "set", "target": target, "line_offset": 2})
	if outcome(placed) != "ok" {
		t.Fatalf("%v", placed)
	}
	refused := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "conflict", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "trace_policy": map[string]any{"mode": "path", "targets": []any{map[string]any{"target": target, "line_offset": 2}}}})
	if outcome(refused) != "failed" {
		t.Fatalf("overlap not refused: %v", refused)
	}
	listed := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": w, "idempotency_key": "list", "action": "list"})
	if data(listed)["debug"].(map[string]any)["count"] != float64(1) {
		t.Fatal("existing breakpoint removed")
	}
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "plain-start", "action": "start", "file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000})
	if data(started)["trace_id"] != nil || data(started)["debug"].(map[string]any)["state"] != "stopped" {
		t.Fatalf("disabled tracing changed debugger: %v", started)
	}
	call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "plain-stop", "action": "stop"})
}
