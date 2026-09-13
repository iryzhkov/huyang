//go:build live

package livetest

import (
	"maps"
	"strings"
	"testing"
)

// The question an agent should ask before changing a declaration: who calls
// this, is it exported, is anything testing it, and what could nobody see.
// It is answered from the experimental profile, because the frozen schemas
// are a contract and this argument is new.
func TestImpactPreviewNamesCallersTestsAndUnknowns(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	before := hashTree(t, root)
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	created := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create", "idempotency_key": "impact-plan",
		"operations": []any{map[string]any{
			"op_id": "retype", "kind": "replace_symbol",
			"content": "// Total is the amount left in the ledger.\nfunc Total() int64 {\n\treturn 7\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	if outcome(created) != "ok" {
		t.Fatalf("create = %#v", created)
	}
	plan := data(created)["plan"].(map[string]any)

	previewed := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "preview", "idempotency_key": "impact-preview",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"], "view": "impact",
	})
	if verdict := outcome(previewed); verdict != "ok" && verdict != "partial" {
		t.Fatalf("impact preview = %#v", previewed)
	}
	impact, _ := data(previewed)["impact"].(map[string]any)
	if impact == nil {
		t.Fatalf("the preview carried no impact: %#v", previewed)
	}

	// Both revisions, and they are not the same thing: the callers are a fact
	// about the code as it stands, the proposal is what is being weighed.
	if impact["analysed_revision"] == "" || impact["snapshot"] == "" {
		t.Fatalf("the impact does not name what it read: %#v", impact)
	}
	targets, _ := impact["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	target := targets[0].(map[string]any)
	if target["name_path"] != "Total" || target["path"] != "ledger.go" {
		t.Fatalf("target = %#v", target)
	}

	// report.go and store.go both call Total; the language server knows it
	// and no import reader could, because they are the same package.
	callers := map[string]bool{}
	for _, value := range impact["callers"].([]any) {
		callers[value.(map[string]any)["path"].(string)] = true
	}
	if !callers["report.go"] || !callers["store.go"] {
		t.Fatalf("the callers of Total were not found: %v", callers)
	}

	// A preview reads; it never writes and never runs a project command.
	if after := hashTree(t, root); !maps.Equal(before, after) {
		t.Fatal("previewing the impact of a plan changed canonical bytes")
	}
}

// The same question with no language server: the answer is still an answer,
// and it says plainly that it rests on the import reader alone.
func TestImpactPreviewFallsBackWithLowerConfidence(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	created := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create", "idempotency_key": "fallback-plan",
		"operations": []any{map[string]any{
			"op_id": "retype", "kind": "replace_symbol", "content": "func Total() int { return 8 }\n",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	if outcome(created) != "ok" {
		t.Fatalf("create = %#v", created)
	}
	plan := data(created)["plan"].(map[string]any)
	previewed := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "preview", "idempotency_key": "fallback-impact",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"], "view": "impact",
	})
	impact, _ := data(previewed)["impact"].(map[string]any)
	if impact == nil {
		t.Fatalf("no impact in the fallback preview: %#v", previewed)
	}
	// Without a language server the same-package callers are invisible, so
	// the list is shorter or absent. What must not happen is a confident
	// claim: an import reader cannot know who calls a function.
	callers, _ := impact["callers"].([]any)
	for _, value := range callers {
		caller := value.(map[string]any)
		if caller["confidence"] == "authoritative" {
			t.Fatalf("a machine with no language server claimed authority: %#v", caller)
		}
	}
	t.Logf("fallback preview found %d caller(s)", len(callers))
	coverage, _ := impact["coverage"].(map[string]any)
	if complete, _ := coverage["complete"].(bool); complete {
		t.Fatalf("a fallback analysis called itself complete: %#v", coverage)
	}
}

// The frozen profiles do not advertise the view argument, and a client that
// has cached their schemas must not have it accepted by accident.
func TestTheImpactViewIsExperimentalOnly(t *testing.T) {
	instance := start(t)
	session := instance.connect("edit")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	refusal := callRefused(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create", "idempotency_key": "frozen-view", "view": "impact",
		"operations": []any{map[string]any{
			"op_id": "noop", "kind": "create_file", "path": "added.go", "content": "package huyangfixture\n",
		}},
	})
	if !strings.Contains(refusal.Error(), "view") {
		t.Fatalf("the refusal does not name the argument it rejected: %v", refusal)
	}
}
