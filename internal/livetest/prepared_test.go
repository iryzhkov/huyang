//go:build live

package livetest

import (
	"maps"
	"strings"
	"testing"
)

// prepareTypeError stages a change that does not compile, which is the
// interesting case: the agent wants to know what is wrong with a proposal
// before deciding whether to apply it.
func prepareTypeError(t *testing.T, instance *live, session sessionHandle, workspaceID string) map[string]any {
	t.Helper()
	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "prepared-typeerror",
		"operations": []any{map[string]any{
			"op_id": "break", "kind": "replace_symbol",
			// Total returns int everywhere else, so this is a type error in
			// every file that calls it.
			"content": "// Total is the amount left in the ledger.\nfunc Total() string {\n\treturn \"seven\"\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	if outcome(prepared) == "failed" {
		t.Fatalf("prepare = %#v", prepared)
	}
	return data(prepared)["plan"].(map[string]any)
}

// A prepared revision is a place to read. The staged bytes are there, the
// canonical bytes are unchanged, and each reply says which revision it read.
func TestPreparedRevisionIsReadable(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	before := hashTree(t, root)
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	plan := prepareTypeError(t, instance, session, workspaceID)
	preparation, _ := plan["preparation"].(map[string]any)
	preparedRevision, _ := preparation["prepared_revision"].(string)
	if preparedRevision == "" {
		t.Fatalf("no prepared revision: %#v", plan)
	}

	canonical := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "target": ledgerTarget(),
	})
	staged := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": preparedRevision,
		"target": ledgerTarget(),
	})
	if outcome(staged) != "ok" {
		t.Fatalf("prepared read = %#v", staged)
	}
	canonicalText := data(canonical)["content"].(string)
	stagedText := data(staged)["content"].(string)
	if !strings.Contains(canonicalText, "func Total() int") {
		t.Fatalf("canonical read is not the canonical bytes: %q", canonicalText)
	}
	if !strings.Contains(stagedText, "func Total() string") {
		t.Fatalf("the prepared read did not return the staged bytes: %q", stagedText)
	}
	if data(staged)["revision_id"] != preparedRevision {
		t.Fatalf("the prepared read does not name its revision: %#v", data(staged))
	}
	// The sandbox is the service's own business.
	if strings.Contains(stagedText, instance.stateDir) {
		t.Fatal("the prepared read leaked a sandbox path")
	}
	if path := data(staged)["path"].(string); path != "ledger.go" {
		t.Fatalf("the prepared read answered with %q rather than the path the agent knows", path)
	}
	if after := hashTree(t, root); !maps.Equal(before, after) {
		t.Fatal("reading a prepared revision changed canonical bytes")
	}
}

// The question the whole stage is for: what does the language server make of
// the staged bytes, without applying them.
func TestPreparedRevisionAnswersDiagnosticsAndNavigation(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	plan := prepareTypeError(t, instance, session, workspaceID)
	preparation, _ := plan["preparation"].(map[string]any)
	preparedRevision, _ := preparation["prepared_revision"].(string)

	diagnosed := call(t, session, "diagnostics", map[string]any{
		"workspace_id": workspaceID, "revision": preparedRevision,
	})
	if outcome(diagnosed) != "ok" && outcome(diagnosed) != "partial" && outcome(diagnosed) != "unavailable" {
		t.Fatalf("prepared diagnostics = %#v", diagnosed)
	}
	if outcome(diagnosed) == "ok" && data(diagnosed)["revision"] != preparedRevision {
		t.Fatalf("prepared diagnostics do not name their revision: %#v", data(diagnosed))
	}
	rendered := renderJSON(t, diagnosed)
	if strings.Contains(rendered, instance.stateDir) || strings.Contains(rendered, "sandbox") {
		t.Fatalf("prepared diagnostics leaked a sandbox path: %s", rendered)
	}
	t.Logf("prepared diagnostics: %s", summary(diagnosed))

	navigated := call(t, session, "navigate", map[string]any{
		"workspace_id": workspaceID, "revision": preparedRevision, "relation": "references",
		"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
	})
	if outcome(navigated) == "failed" {
		t.Fatalf("prepared navigation = %#v", navigated)
	}
	if rendered := renderJSON(t, navigated); strings.Contains(rendered, instance.stateDir) {
		t.Fatalf("prepared navigation leaked a sandbox path: %s", rendered)
	}
}

// A prepared handle is not a canonical one. When the preparation is gone the
// answer is a refusal that says so, never the canonical bytes wearing the
// prepared revision's name.
func TestPreparedRevisionIsRefusedOnceItIsGone(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	plan := prepareTypeError(t, instance, session, workspaceID)
	preparation, _ := plan["preparation"].(map[string]any)
	preparedRevision, _ := preparation["prepared_revision"].(string)

	discarded := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "discard", "idempotency_key": "prepared-discard",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	if outcome(discarded) == "failed" {
		t.Fatalf("discard = %#v", discarded)
	}

	gone := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": preparedRevision,
		"target": ledgerTarget(),
	})
	if outcome(gone) == "ok" {
		t.Fatalf("a discarded revision still answered: %#v", gone)
	}
	if !strings.Contains(summary(gone), "prepared revision") {
		t.Fatalf("the refusal does not say what happened: %q", summary(gone))
	}
	// And it did not quietly answer with canonical source.
	if content, ok := data(gone)["content"].(string); ok && strings.Contains(content, "func Total") {
		t.Fatal("a gone prepared revision fell back to canonical bytes")
	}
}

// The frozen profiles do not offer the selector, so a client with a cached
// schema cannot accidentally ask a question this server would answer about
// other bytes.
func TestThePreparedSelectorIsExperimentalOnly(t *testing.T) {
	instance := start(t)
	session := instance.connect("orient")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	refusal := callRefused(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "revision": "prep_whatever",
		"target": ledgerTarget(),
	})
	if !strings.Contains(refusal.Error(), "revision") {
		t.Fatalf("the refusal does not name the argument: %v", refusal)
	}
}
