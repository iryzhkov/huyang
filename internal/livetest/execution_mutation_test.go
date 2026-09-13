//go:build live

package livetest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionMutationCapabilityGateRealDelve(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "capability-start", "action": "start", "adapter": "delve",
		"file": filepath.Join(root, "main.go"), "wait_ms": 10000, "initial_breakpoints": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": 2}}})
	if data(started)["debug"].(map[string]any)["state"] != "stopped" {
		t.Fatalf("%v", started)
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "mutation_capabilities"})
	capability := data(inspected)["debug"].(map[string]any)["mutation_capabilities"].(map[string]any)
	t.Logf("Delve mutation capabilities: %v", capability)
	if capability["available"] != true || capability["validated_capture"] != true {
		t.Fatalf("unvalidated capture enabled: %v", capability)
	}
	refused := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "mutation-refused", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "trace_policy": map[string]any{"mode": "mutations"}})
	if !strings.Contains(renderJSON(t, refused), "trace_capability_unavailable") {
		t.Fatalf("%v", refused)
	}
	call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "capability-stop", "action": "stop"})
}
