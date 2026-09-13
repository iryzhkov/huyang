//go:build live

package livetest

// What a change promises to code this repository does not contain.
//
// These run without a language server on purpose: the exported surface is read
// from the bytes by the adapter for the language, so the answer must be the
// same on a machine where nothing is installed.

import "testing"

// Removing an exported declaration is removing something somebody compiled
// against.
func TestRemovingAnExportedDeclarationViolatesCompatibility(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "api-remove",
		[]any{map[string]any{
			"op_id": "drop-report", "kind": "delete_symbol",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "report.go", "name_path": "Report"}},
		}},
		[]any{map[string]any{"id": "api", "kind": "api_compatible"}})

	plan, revision, invariants := preparedPlan(t, prepared)
	api := invariants["api"]
	if api == nil {
		t.Fatalf("the prepared plan carries no invariant: %#v", plan)
	}
	t.Logf("api: %v - %v", api["status"], api["detail"])
	if api["status"] != "violated" {
		t.Fatalf("removing an exported function was not an incompatible change: %#v", api)
	}
	applied := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "apply", "idempotency_key": "api-remove-apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if applied["code"] != "invariant_not_proven" {
		t.Fatalf("an incompatible change was not blocked: %v/%v %s",
			applied["outcome"], applied["code"], summary(applied))
	}
}

// Adding an implementation of an existing interface promises nothing new of
// anybody, so it passes.
func TestAddingAnImplementationKeepsCompatibility(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "api-add",
		[]any{map[string]any{
			"op_id": "add-cash", "kind": "create_file", "path": "cash.go",
			"content": "package huyangfixture\n\n// Cash is another implementation of the same interface.\ntype Cash struct{ amount int }\n\nfunc (c Cash) Balance() int { return c.amount }\n",
		}},
		[]any{map[string]any{"id": "api", "kind": "api_compatible"}})

	plan, _, invariants := preparedPlan(t, prepared)
	api := invariants["api"]
	if api == nil {
		t.Fatalf("the prepared plan carries no invariant: %#v", plan)
	}
	t.Logf("api: %v - %v", api["status"], api["detail"])
	if api["status"] != "proven" {
		t.Fatalf("adding a new type was not compatible: %#v", api)
	}
}

// A barrel says where a name comes from, and importers depend on that answer.
func TestChangingATypeScriptBarrelViolatesCompatibility(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "typescript")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "api-barrel",
		[]any{map[string]any{
			"op_id": "narrow-barrel", "kind": "replace_matches",
			"content": "export { total } from \"./ledger.js\";",
			"target":  map[string]any{"handle": barrelHandle(t, session, workspaceID)},
		}},
		[]any{map[string]any{"id": "api", "kind": "api_compatible"}})

	plan, _, invariants := preparedPlan(t, prepared)
	api := invariants["api"]
	if api == nil {
		t.Fatalf("the prepared plan carries no invariant: %#v", plan)
	}
	t.Logf("api: %v - %v", api["status"], api["detail"])
	if api["status"] != "violated" {
		t.Fatalf("dropping a name from the barrel was not an incompatible change: %#v", api)
	}
}

// barrelHandle is the search result set naming the barrel's ledger export, so
// the edit replaces exact bytes rather than a line number.
func barrelHandle(t *testing.T, session sessionHandle, workspaceID string) string {
	t.Helper()
	found := call(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "export { total, unused } from \"./ledger.js\";",
		"paths": []any{"index.ts"},
	})
	resultSet, _ := data(found)["result_set"].(map[string]any)
	handle, _ := resultSet["handle"].(string)
	if handle == "" {
		t.Fatalf("the barrel line was not found: %s", summary(found))
	}
	return handle
}

// Reachability can be refuted by one remaining reference and cannot yet be
// proved: the proof needs a static execution graph this service does not
// build, and the answer says so instead of guessing.
func TestReachabilityIsRefutedButNotProved(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "reachability",
		[]any{map[string]any{
			"op_id": "touch-unused", "kind": "insert_before",
			"content": "// touched\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
		}},
		[]any{
			map[string]any{"id": "called", "kind": "path_unreachable", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Total"}}},
			map[string]any{"id": "orphan", "kind": "path_unreachable", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Unused"}}},
		})

	_, _, invariants := preparedPlan(t, prepared)
	for id, invariant := range invariants {
		t.Logf("%s: %v - %v", id, invariant["status"], invariant["detail"])
	}
	if status := invariants["called"]["status"]; status != "violated" {
		t.Fatalf("a declaration with callers was not reported reachable: %#v", invariants["called"])
	}
	if status := invariants["orphan"]["status"]; status != "unknown" {
		t.Fatalf("unreachability was claimed without a graph to prove it: %#v", invariants["orphan"])
	}
}
