//go:build live

package livetest

import (
	"fmt"
	"maps"
	"path/filepath"
	"testing"
)

func TestExecutionIntegratedPreparedProofTraceOverlayComparison(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "closed_go"))
	before := hashTree(t, root)
	w := workspaceIdentity(t, call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root}))
	graph := call(t, session, "execution_graph", map[string]any{"workspace_id": w, "use_provider": false})
	if data(graph)["snapshot"] == nil {
		t.Fatalf("%v", graph)
	}
	pathArgs := map[string]any{"workspace_id": w, "from": "source", "to": "target", "use_provider": false}
	if result := call(t, session, "path_explain", pathArgs); data(result)["status"] != "static_possible" {
		t.Fatalf("%v", result)
	}
	prepared := call(t, session, "change_plan", map[string]any{"workspace_id": w, "action": "prepare", "idempotency_key": "integrated-remove",
		"operations": []any{map[string]any{"op_id": "remove", "kind": "replace_symbol", "content": "func source() {}\n", "target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "source"}}}},
		"invariants": []any{map[string]any{"id": "unreachable", "kind": "path_unreachable", "enforcement": "required", "scope": map[string]any{"symbol": map[string]any{"path": "main.go", "name_path": "target"}}}}})
	plan, ok := data(prepared)["plan"].(map[string]any)
	if !ok {
		t.Fatalf("%v", prepared)
	}
	if plan["invariants"].([]any)[0].(map[string]any)["status"] != "proven" {
		t.Fatalf("%v", plan)
	}
	revision := plan["preparation"].(map[string]any)["prepared_revision"]
	staged := maps.Clone(pathArgs)
	staged["revision"] = revision
	if result := call(t, session, "path_explain", staged); data(result)["status"] != "statically_unreachable" {
		t.Fatalf("%v", result)
	}
	ids := []string{}
	for run := 0; run < 2; run++ {
		started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("integrated-start-%d", run), "action": "start", "file": filepath.Join(root, "main.go"),
			"adapter": "delve", "wait_ms": 10000, "trace_policy": map[string]any{"mode": "path", "targets": []any{
				map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "target"}}}}}})
		id, _ := data(started)["trace_id"].(string)
		if id == "" {
			t.Fatalf("%v", started)
		}
		ended := call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("integrated-end-%d", run), "action": "continue", "wait_ms": 10000})
		if data(ended)["debug"].(map[string]any)["state"] != "exited" {
			t.Fatalf("%v", ended)
		}
		ids = append(ids, id)
	}
	combinedArgs := maps.Clone(pathArgs)
	combinedArgs["mode"] = "combined"
	combinedArgs["trace_id"] = ids[0]
	combined := call(t, session, "path_explain", combinedArgs)
	if data(combined)["explanation"].(map[string]any)["target_status"] != "observed" {
		t.Fatalf("%v", combined)
	}
	compared := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "compare_traces", "passing_trace_id": ids[0], "failing_trace_id": ids[1]})
	if data(compared)["comparison"].(map[string]any)["common_prefix_stops"].(float64) < 1 {
		t.Fatalf("%v", compared)
	}
	semantic := call(t, session, "change_plan", map[string]any{"workspace_id": w, "action": "inspect", "idempotency_key": "integrated-semantic", "plan_id": plan["plan_id"], "view": "semantic"})
	if data(semantic)["semantic_summary"] == nil {
		t.Fatalf("%v", semantic)
	}
	if !maps.Equal(before, hashTree(t, root)) {
		t.Fatal("prepared proof or trace flow changed canonical bytes")
	}
}
