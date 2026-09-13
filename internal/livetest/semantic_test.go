//go:build live

package livetest

// The semantic view of a change, over the socket.

import (
	"strings"
	"testing"
)

// semanticSummary asks for the meaning of one plan and returns the summary.
func semanticSummary(t *testing.T, session sessionHandle, workspaceID, key string, plan map[string]any) map[string]any {
	t.Helper()
	answered := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "inspect", "view": "semantic", "idempotency_key": key,
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	if outcome(answered) != "ok" {
		t.Fatalf("semantic view = %s/%v: %s", outcome(answered), answered["code"], summary(answered))
	}
	value, _ := data(answered)["semantic_summary"].(map[string]any)
	if value == nil {
		t.Fatalf("no summary in %#v", data(answered))
	}
	t.Logf("summary: %s", summary(answered))
	return value
}

func symbolKinds(summary map[string]any) map[string]string {
	kinds := map[string]string{}
	for _, value := range anySlice(summary["symbols"]) {
		change, _ := value.(map[string]any)
		name, _ := change["name"].(string)
		kind, _ := change["kind"].(string)
		kinds[name] = kind
	}
	return kinds
}

func joined(summary map[string]any, key string) string {
	var parts []string
	for _, value := range anySlice(summary[key]) {
		parts = append(parts, strings.TrimSpace(stringOf(value)))
	}
	return strings.Join(parts, " | ")
}

func stringOf(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		detail, _ := typed["detail"].(string)
		return detail
	}
	return ""
}

// A rename the language server carried through every caller is one change in
// the summary, not a removal and an addition to pair up by eye.
func TestARenameAcrossCallersIsSummarisedAsOneChange(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "semantic-rename",
		"operations": []any{map[string]any{
			"op_id": "rename-total", "kind": "rename_symbol", "content": "Balance",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	if verdict := outcome(prepared); verdict != "ok" && verdict != "provisional" {
		t.Skipf("the language server did not carry the rename: %s", summary(prepared))
	}
	plan, _, _ := preparedPlan(t, prepared)
	summary := semanticSummary(t, session, workspaceID, "semantic-rename-view", plan)
	kinds := symbolKinds(summary)
	t.Logf("symbols: %#v", kinds)
	if kinds["Total"] != "renamed" {
		t.Fatalf("a rename was not summarised as one: %#v", kinds)
	}
	if len(anySlice(summary["files"])) < 2 {
		t.Fatalf("the callers the rename touched are missing from the index: %#v", summary["files"])
	}
}

// A function that starts returning an error is a signature change, a break for
// callers outside this repository, and - with no test touched - a coverage gap
// worth saying out loud.
func TestAChangedSignatureIsSummarisedWithItsConsequences(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "semantic-signature",
		"operations": []any{map[string]any{
			"op_id": "fallible-total", "kind": "replace_symbol",
			"content": "// Total is the amount left in the ledger.\nfunc Total() (int, error) {\n\treturn 7, nil\n}",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	plan, _, _ := preparedPlan(t, prepared)
	summary := semanticSummary(t, session, workspaceID, "semantic-signature-view", plan)
	if kinds := symbolKinds(summary); kinds["Total"] != "signature_changed" {
		t.Fatalf("symbols = %#v", kinds)
	}
	advice := joined(summary, "recommendations")
	if !strings.Contains(advice, "outside this repository") {
		t.Fatalf("a break was not named: %s", advice)
	}
	if !strings.Contains(advice, "no test file changed") {
		t.Fatalf("the coverage gap was not named: %s", advice)
	}
}

// A configuration file nobody can read is reported as exactly that, rather
// than as a file with nothing in it.
func TestAChangedSchemaIsReportedAsAKnownGap(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "semantic-schema",
		"operations": []any{map[string]any{
			"op_id": "add-config", "kind": "create_file", "path": "config.json",
			"content": "{\n  \"currency\": \"EUR\"\n}\n",
		}},
	})
	plan, _, _ := preparedPlan(t, prepared)
	summary := semanticSummary(t, session, workspaceID, "semantic-schema-view", plan)
	gaps := joined(summary, "gaps")
	if !strings.Contains(gaps, "no schema adapter") {
		t.Fatalf("gaps = %s", gaps)
	}
	if complete, _ := summary["complete"].(bool); complete {
		t.Fatal("a summary with an unread file called itself complete")
	}
	indexed := false
	for _, value := range anySlice(summary["files"]) {
		file, _ := value.(map[string]any)
		if file["path"] == "config.json" {
			indexed = true
		}
	}
	if !indexed {
		t.Fatalf("the unreadable file left the index: %#v", summary["files"])
	}
}

// The same change, asked about before and after it landed: the meaning is the
// same and the revisions it names are not.
func TestTheSummarySurvivesTheApply(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "semantic-apply",
		"operations": []any{map[string]any{
			"op_id": "add-extra", "kind": "insert_after",
			"content": "\n\n// Extra is new behaviour.\nfunc Extra() int { return 1 }",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	plan, revision, _ := preparedPlan(t, prepared)
	before := semanticSummary(t, session, workspaceID, "semantic-before-view", plan)
	if kinds := symbolKinds(before); kinds["Extra"] != "added" {
		t.Fatalf("symbols = %#v", kinds)
	}

	applied := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "apply", "idempotency_key": "semantic-apply-commit",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if verdict := outcome(applied); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("apply = %s: %s", verdict, summary(applied))
	}
	committed, _, _ := preparedPlan(t, applied)
	after := semanticSummary(t, session, workspaceID, "semantic-after-view", committed)
	if kinds := symbolKinds(after); kinds["Extra"] != "added" {
		t.Fatalf("the applied summary lost the change: %#v", kinds)
	}
	if before["to_revision"] == after["to_revision"] {
		t.Fatalf("the proposal and the applied change name the same revision: %v", after["to_revision"])
	}
	if !strings.HasPrefix(stringValue(after["to_revision"]), "wsrev_") {
		t.Fatalf("an applied change is not named by a canonical revision: %v", after["to_revision"])
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
