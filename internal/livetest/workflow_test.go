//go:build live

package livetest

// The whole workflow, and the things that go wrong around it.
//
// Every other live test proves one seam. This file walks the sequence an agent
// actually performs - plan, look, revise, verify, apply - and then does the
// awkward things: two preparations at once, somebody else writing the file
// first, and two catalogs talking to one workspace.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planAction is one change_plan call with its own idempotency key.
func planAction(t *testing.T, session sessionHandle, workspaceID, key string, arguments map[string]any) map[string]any {
	t.Helper()
	call := map[string]any{"workspace_id": workspaceID, "idempotency_key": key}
	for name, value := range arguments {
		call[name] = value
	}
	return callTool(t, session, "change_plan", call)
}

func callTool(t *testing.T, session sessionHandle, name string, arguments map[string]any) map[string]any {
	t.Helper()
	return call(t, session, name, arguments)
}

// The sequence, end to end: a plan with invariants, its impact, its prepared
// bytes, a revision, its meaning, and then the exact revision applied.
func TestTheWholeWorkflowFromOpenToApplied(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	created := planAction(t, session, workspaceID, "workflow-create", map[string]any{
		"action": "create",
		"operations": []any{
			map[string]any{
				"op_id": "extend-ledger", "kind": "insert_after",
				"content": "\n\n// Pending is what has not settled yet.\nfunc Pending() int { return 2 }",
				"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
			},
			map[string]any{
				"op_id": "add-note", "kind": "create_file", "path": "NOTES.md",
				"content": "# Notes\n\nPending is new.\n",
			},
		},
		"invariants": []any{
			// Scoped to the source file: a plan that also writes a note has
			// nothing to say about the note's exported surface.
			map[string]any{"id": "api", "kind": "api_compatible",
				"scope": map[string]any{"paths": []any{"ledger.go"}}},
			map[string]any{"id": "still-there", "kind": "symbol_exists",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Total"}}},
		},
	})
	if outcome(created) != "ok" {
		t.Fatalf("create = %s: %s", outcome(created), summary(created))
	}
	plan, _, _ := preparedPlan(t, created)
	planID := plan["plan_id"]

	// What the change reaches, before it is made.
	impact := planAction(t, session, workspaceID, "workflow-impact", map[string]any{
		"action": "preview", "view": "impact", "plan_id": planID, "plan_revision": plan["plan_revision"],
	})
	if verdict := outcome(impact); verdict != "ok" && verdict != "partial" {
		t.Fatalf("impact = %s: %s", verdict, summary(impact))
	}
	t.Logf("impact: %s (%s)", summary(impact), outcome(impact))

	prepared := planAction(t, session, workspaceID, "workflow-prepare", map[string]any{
		"action": "prepare", "plan_id": planID, "plan_revision": plan["plan_revision"],
	})
	if verdict := outcome(prepared); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("prepare = %s/%v: %s", verdict, prepared["code"], summary(prepared))
	}
	plan, revision, invariants := preparedPlan(t, prepared)
	for id, invariant := range invariants {
		t.Logf("%s: %v - %v", id, invariant["status"], invariant["detail"])
	}

	// The staged bytes are somewhere to look, and they say so.
	read := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": revision,
		"target": map[string]any{"path": "ledger.go"},
	})
	if outcome(read) != "ok" || !strings.Contains(fieldString(data(read), "content"), "Pending") {
		t.Fatalf("prepared read = %s: %s", outcome(read), summary(read))
	}
	diagnosed := call(t, session, "diagnostics", map[string]any{
		"workspace_id": workspaceID, "revision": revision,
	})
	if outcome(diagnosed) != "ok" {
		t.Fatalf("prepared diagnostics = %s: %s", outcome(diagnosed), summary(diagnosed))
	}
	t.Logf("prepared diagnostics: %s", summary(diagnosed))
	actions := call(t, session, "code_actions", map[string]any{
		"workspace_id": workspaceID, "revision": revision,
		"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
	})
	t.Logf("code actions: %s/%v", outcome(actions), actions["code"])

	// Revise: editing a prepared plan releases its preparation and returns it
	// to an intent, then it is prepared again.
	// An operation kind that does not exist is refused by the catalog itself,
	// before the service is asked to do anything with it.
	callRefused(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "workflow-edit", "action": "edit",
		"plan_id": planID, "plan_revision": plan["plan_revision"],
		"edit": map[string]any{"mode": "add", "operations": []any{map[string]any{
			"op_id": "extend-note", "kind": "replace_literal_unsupported",
		}}},
	})
	revised := planAction(t, session, workspaceID, "workflow-edit-2", map[string]any{
		"action": "edit", "plan_id": planID, "plan_revision": plan["plan_revision"],
		"edit": map[string]any{"mode": "add", "operations": []any{map[string]any{
			"op_id": "add-changelog", "kind": "create_file", "path": "CHANGELOG.md",
			"content": "# Changelog\n\n- Pending\n",
		}}},
	})
	if outcome(revised) != "ok" {
		t.Fatalf("edit = %s/%v: %s", outcome(revised), revised["code"], summary(revised))
	}
	plan, _, _ = preparedPlan(t, revised)
	if plan["state"] != "OPEN" {
		t.Fatalf("a revised plan is %v, not OPEN", plan["state"])
	}
	// The preparation the revision replaced is gone, and reading it says so.
	gone := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": revision, "target": map[string]any{"path": "ledger.go"},
	})
	if gone["code"] != "prepared_revision_unavailable" {
		t.Fatalf("the replaced preparation still answers: %s/%v", outcome(gone), gone["code"])
	}

	prepared = planAction(t, session, workspaceID, "workflow-reprepare", map[string]any{
		"action": "prepare", "plan_id": planID, "plan_revision": plan["plan_revision"],
	})
	if verdict := outcome(prepared); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("reprepare = %s/%v: %s", verdict, prepared["code"], summary(prepared))
	}
	plan, revision, _ = preparedPlan(t, prepared)

	// What it means, then what the tests say, then the exact revision applied.
	meaning := semanticSummary(t, session, workspaceID, "workflow-semantic", plan)
	if len(anySlice(meaning["files"])) != 3 {
		t.Fatalf("the summary does not index every affected file: %#v", meaning["files"])
	}
	verified := call(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "workflow-verify",
		"stages": []any{"parser"}, "revision_or_transaction": "current",
	})
	t.Logf("verify: %s/%v %s", outcome(verified), verified["code"], summary(verified))

	applied := planAction(t, session, workspaceID, "workflow-apply", map[string]any{
		"action": "apply", "plan_id": planID, "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if verdict := outcome(applied); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("apply = %s/%v: %s", verdict, applied["code"], summary(applied))
	}
	for _, path := range []string{"NOTES.md", "CHANGELOG.md"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatalf("%s did not land: %v", path, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "ledger.go"))
	if err != nil || !strings.Contains(string(content), "func Pending()") {
		t.Fatalf("the edit did not land: %v", err)
	}
	committed, _, _ := preparedPlan(t, applied)
	after := semanticSummary(t, session, workspaceID, "workflow-semantic-after", committed)
	if kinds := symbolKinds(after); kinds["Pending"] != "added" {
		t.Fatalf("the applied summary lost the change: %#v", kinds)
	}
}

func fieldString(payload map[string]any, key string) string {
	text, _ := payload[key].(string)
	return text
}

// Two plans prepared at once are two sandboxes, each answering about its own
// bytes, and applying one leaves the other describing a workspace that has
// moved.
func TestTwoPreparedPlansCoexist(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	first := planAction(t, session, workspaceID, "coexist-one", map[string]any{
		"action": "prepare",
		"operations": []any{map[string]any{
			"op_id": "one", "kind": "create_file", "path": "one.md", "content": "one\n",
		}},
	})
	second := planAction(t, session, workspaceID, "coexist-two", map[string]any{
		"action": "prepare",
		"operations": []any{map[string]any{
			"op_id": "two", "kind": "create_file", "path": "two.md", "content": "two\n",
		}},
	})
	firstPlan, firstRevision, _ := preparedPlan(t, first)
	secondPlan, secondRevision, _ := preparedPlan(t, second)
	if firstRevision == secondRevision || firstRevision == "" || secondRevision == "" {
		t.Fatalf("two preparations share a revision: %s and %s", firstRevision, secondRevision)
	}
	// Each prepared revision holds its own file and not the other's.
	for revision, present := range map[string]string{firstRevision: "one.md", secondRevision: "two.md"} {
		read := call(t, session, "read", map[string]any{
			"workspace_id": workspaceID, "revision": revision, "target": map[string]any{"path": present},
		})
		if outcome(read) != "ok" {
			t.Fatalf("%s does not hold %s: %s", revision, present, summary(read))
		}
	}
	absent := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": firstRevision, "target": map[string]any{"path": "two.md"},
	})
	if outcome(absent) == "ok" {
		t.Fatal("one preparation can see another's staged file")
	}

	applied := planAction(t, session, workspaceID, "coexist-apply", map[string]any{
		"action": "apply", "plan_id": firstPlan["plan_id"], "plan_revision": firstPlan["plan_revision"],
		"prepared_revision": firstRevision, "accept_provisional": true,
	})
	if verdict := outcome(applied); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("apply = %s: %s", verdict, summary(applied))
	}
	// The second plan was prepared against a workspace that has moved. Whether
	// it still applies is the commit's decision; what must not happen is
	// applying it silently over the first one's work.
	secondApplied := planAction(t, session, workspaceID, "coexist-apply-two", map[string]any{
		"action": "apply", "plan_id": secondPlan["plan_id"], "plan_revision": secondPlan["plan_revision"],
		"prepared_revision": secondRevision, "accept_provisional": true,
	})
	t.Logf("second apply: %s/%v %s", outcome(secondApplied), secondApplied["code"], summary(secondApplied))
	if _, err := os.Stat(filepath.Join(root, "one.md")); err != nil {
		t.Fatalf("the applied plan's file is gone: %v", err)
	}
}

// Somebody else wrote the file between prepare and apply. The commit must
// refuse rather than overwrite what they wrote.
func TestAnExternalWriteBeforeApplyIsRefused(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := planAction(t, session, workspaceID, "external-prepare", map[string]any{
		"action": "prepare",
		"operations": []any{map[string]any{
			"op_id": "change-total", "kind": "replace_symbol",
			"content": "// Total is the amount left in the ledger.\nfunc Total() int {\n\treturn 8\n}",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	plan, revision, _ := preparedPlan(t, prepared)

	external := "package huyangfixture\n\n// Total was rewritten by somebody else.\nfunc Total() int {\n\treturn 99\n}\n"
	if err := os.WriteFile(filepath.Join(root, "ledger.go"), []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := planAction(t, session, workspaceID, "external-apply", map[string]any{
		"action": "apply", "plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if outcome(applied) == "ok" {
		t.Fatalf("a plan prepared against other bytes was applied over them: %s", summary(applied))
	}
	t.Logf("refused: %s/%v %s", outcome(applied), applied["code"], summary(applied))
	content, err := os.ReadFile(filepath.Join(root, "ledger.go"))
	if err != nil || string(content) != external {
		t.Fatalf("the external write was overwritten: %v", err)
	}
}

// A plan with no operations is refused at prepare, and the canonical
// revision does not move. Committing it wrote no byte and still answered
// canonical_changed with an empty changed-path list beside it, advancing the
// revision counter for a change that does not exist.
func TestAnEmptyPlanIsRefusedAtPrepare(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	before := workspaceRevision(call(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID}))

	created := planAction(t, session, workspaceID, "empty-create", map[string]any{
		"action": "create", "operations": []any{},
	})
	if outcome(created) != "ok" {
		t.Fatalf("create with no operations = %s: %s", outcome(created), summary(created))
	}
	plan, _ := data(created)["plan"].(map[string]any)
	prepared := planAction(t, session, workspaceID, "empty-prepare", map[string]any{
		"action": "prepare", "plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	if outcome(prepared) == "ok" || outcome(prepared) == "provisional" {
		t.Fatalf("a plan with no operations was prepared: %s", summary(prepared))
	}
	if code, _ := prepared["code"].(string); code != "plan_state_invalid" {
		t.Fatalf("empty prepare code = %q, want plan_state_invalid: %s", code, summary(prepared))
	}
	if !offers(nextActions(prepared), "edit") && !offers(nextActions(prepared), "discard") {
		t.Fatalf("the refusal leaves nothing to do next: %#v", prepared["next"])
	}
	after := call(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	if got := workspaceRevision(after); got != before {
		t.Fatalf("the canonical revision moved from %s to %s for a plan that changes nothing", before, got)
	}
}

// The frozen catalog and the experimental one describe the same workspace. An
// agent on the frozen profile cannot reach the experimental arguments, and is
// refused rather than quietly given the default behaviour.
func TestTheFrozenAndExperimentalProfilesCoexist(t *testing.T) {
	instance := start(t)
	frozen := instance.connect("edit")
	experimental := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, frozen, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	refused := callRefused(t, frozen, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "frozen-invariants", "action": "prepare",
		"operations": []any{map[string]any{"op_id": "one", "kind": "create_file", "path": "one.md", "content": "one\n"}},
		"invariants": []any{map[string]any{"id": "api", "kind": "api_compatible"}},
	})
	if !strings.Contains(refused.Error(), "invariants") {
		t.Fatalf("the frozen catalog refused for the wrong reason: %v", refused)
	}
	accepted := planAction(t, experimental, workspaceID, "experimental-invariants", map[string]any{
		"action": "prepare",
		"operations": []any{map[string]any{
			"op_id": "one", "kind": "create_file", "path": "extra.go",
			"content": "package huyangfixture\n\n// Extra is new and breaks nothing.\nfunc Extra() int { return 1 }\n",
		}},
		"invariants": []any{map[string]any{"id": "api", "kind": "api_compatible"}},
	})
	if verdict := outcome(accepted); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("the experimental catalog refused what it advertises: %s/%v %s",
			verdict, accepted["code"], summary(accepted))
	}
	// Both sessions see one workspace: the frozen one can inspect the plan the
	// experimental one prepared.
	plan, _, _ := preparedPlan(t, accepted)
	inspected := planAction(t, frozen, workspaceID, "frozen-inspect", map[string]any{
		"action": "inspect", "plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	if verdict := outcome(inspected); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("the frozen catalog cannot see the shared plan: %s/%v %s",
			verdict, inspected["code"], summary(inspected))
	}
	shared, _, invariants := preparedPlan(t, inspected)
	if shared["plan_id"] != plan["plan_id"] {
		t.Fatalf("the frozen catalog answered about another plan: %v", shared["plan_id"])
	}
	if status := invariants["api"]["status"]; status != "proven" {
		t.Fatalf("adding a file that breaks nothing was not compatible: %#v", invariants["api"])
	}
}
