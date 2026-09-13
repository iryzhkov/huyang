//go:build live

package livetest

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionPathsAndPreparedAbsence(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "closed_go"))
	before := hashTree(t, root)
	opened := call(t, session, "workspace_open", map[string]any{"root": root, "kind": "project"})
	workspace := workspaceIdentity(t, opened)
	args := map[string]any{"workspace_id": workspace, "from": "source", "to": "target", "use_provider": false}
	initial := call(t, session, "path_explain", args)
	if data(initial)["status"] != "static_possible" {
		t.Fatalf("%v", initial)
	}
	if len(data(initial)["paths"].([]any)) < 2 {
		t.Fatalf("missing distinct branch paths: %v", initial)
	}
	limitedArgs := maps.Clone(args)
	limitedArgs["max_paths"] = 1
	limited := call(t, session, "path_explain", limitedArgs)
	if data(limited)["status"] != "static_possible" || data(limited)["coverage"].(map[string]any)["capped"] != true {
		t.Fatalf("path cap not disclosed: %v", limited)
	}
	conditions := 0
	graph := data(initial)["snapshot"].(map[string]any)["execution"].(map[string]any)
	for _, raw := range graph["nodes"].([]any) {
		if raw.(map[string]any)["condition"] != nil {
			conditions++
		}
	}
	if conditions == 0 {
		t.Fatal("path query did not expand branch evidence")
	}
	again := call(t, session, "path_explain", args)
	if data(again)["snapshot"].(map[string]any)["id"] != data(initial)["snapshot"].(map[string]any)["id"] {
		t.Fatal("repeated path acquisition changed identity")
	}
	prepared := call(t, session, "change_plan", map[string]any{"workspace_id": workspace, "action": "prepare", "idempotency_key": "remove-call-path",
		"operations": []any{map[string]any{"op_id": "remove", "kind": "replace_symbol", "content": "func source() {}\n", "target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "source"}}}},
		"invariants": []any{map[string]any{"id": "target-unreachable", "kind": "path_unreachable", "enforcement": "required", "scope": map[string]any{"symbol": map[string]any{"path": "main.go", "name_path": "target"}}}}})
	plan, ok := data(prepared)["plan"].(map[string]any)
	if !ok {
		t.Fatalf("prepare: %v", prepared)
	}
	invariants, _ := plan["invariants"].([]any)
	if len(invariants) != 1 || invariants[0].(map[string]any)["status"] != "proven" {
		t.Fatalf("invariant not unlocked: %v", plan)
	}
	revision := plan["preparation"].(map[string]any)["prepared_revision"].(string)
	stagedArgs := maps.Clone(args)
	stagedArgs["revision"] = revision
	staged := call(t, session, "path_explain", stagedArgs)
	if data(staged)["status"] != "statically_unreachable" || outcome(staged) != "ok" {
		t.Fatalf("prepared absence: %v", staged)
	}
	if data(staged)["proof_scope"] == nil {
		t.Fatal("proof has no declared scope")
	}
	snapshot := data(staged)["snapshot"].(map[string]any)
	if snapshot["id"] == data(initial)["snapshot"].(map[string]any)["id"] {
		t.Fatal("prepared path reused canonical identity")
	}
	stagedGraph := snapshot["execution"].(map[string]any)
	handles := 0
	for _, raw := range stagedGraph["nodes"].([]any) {
		node := raw.(map[string]any)
		for _, rawFact := range node["evidence"].([]any) {
			fact := rawFact.(map[string]any)
			refs, _ := fact["source_handles"].([]any)
			for _, handle := range refs {
				read := call(t, session, "read", map[string]any{"workspace_id": workspace, "revision": revision, "target": map[string]any{"handle": handle}})
				if outcome(read) != "ok" {
					t.Fatalf("prepared handle unreadable: %v", read)
				}
				if strings.Contains(data(read)["content"].(string), instance.stateDir) {
					t.Fatal("sandbox path leaked")
				}
				handles++
			}
		}
	}
	if handles == 0 {
		t.Fatal("no prepared source handles")
	}
	if !maps.Equal(before, hashTree(t, root)) {
		t.Fatal("prepared path query changed canonical bytes")
	}
	canonical := call(t, session, "path_explain", args)
	if data(canonical)["status"] != "static_possible" {
		t.Fatal("canonical path was removed")
	}
}
func TestExecutionPathsRetainAsyncBoundaries(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "async_go"))
	result := call(t, session, "path_explain", map[string]any{"root": root, "from": "source", "to": "target", "use_provider": false})
	if data(result)["status"] != "static_possible" {
		t.Fatalf("%v", result)
	}
	graph := data(result)["snapshot"].(map[string]any)["execution"].(map[string]any)
	edgeKinds := map[string]string{}
	for _, raw := range graph["edges"].([]any) {
		edge := raw.(map[string]any)
		edgeKinds[edge["id"].(string)] = edge["kind"].(string)
	}
	found := map[string]bool{}
	for _, raw := range data(result)["paths"].([]any) {
		for _, rawStep := range raw.(map[string]any)["steps"].([]any) {
			step := rawStep.(map[string]any)
			id, _ := step["edge"].(string)
			found[edgeKinds[id]] = true
		}
	}
	if !found["spawns"] || !found["receives"] {
		t.Fatalf("missing async path boundaries: %v", found)
	}
	if data(result)["coverage"].(map[string]any)["complete"] != false {
		t.Fatal("async timing claimed complete")
	}
}
func TestExecutionPathsRefuseDynamicAbsenceAndStaleContent(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "go"))
	result := call(t, session, "path_explain", map[string]any{"root": root, "from": "leaf", "to": "main", "use_provider": false})
	if data(result)["status"] != "unknown" {
		t.Fatalf("dynamic absence overstated: %v", result)
	}
	stale := call(t, session, "path_explain", map[string]any{"root": root, "revision": "content_stale", "from": "leaf", "to": "main", "use_provider": false})
	if outcome(stale) == "ok" || data(stale)["paths"] != nil {
		t.Fatalf("stale content accepted: %v", stale)
	}
	combined := call(t, session, "path_explain", map[string]any{"root": root, "from": "leaf", "to": "main", "mode": "combined"})
	if outcome(combined) == "ok" {
		t.Fatal("missing trace overlay pretended to succeed")
	}
}
