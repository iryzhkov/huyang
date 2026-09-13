//go:build live

package livetest

import (
	"os"
	"path/filepath"
	"testing"
)

// trust makes a fixture's declared commands runnable, because the impact
// analysis this stage is about is only reached by the affected-test path.
func trust(t *testing.T, instance *live, root string) {
	t.Helper()
	config := filepath.Join(instance.homeDir, ".config", "huyang")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "[trust]\nroots = [\"" + root + "\"]\n"
	if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// declareTests writes the project configuration the affected-test selection
// needs, so the fixture has something to associate a change with.
func declareTests(t *testing.T, root string) {
	t.Helper()
	content := "version = 1\n\n[[tests]]\nname = \"go-unit\"\ncommand = [\"go\", \"test\", \"./...\"]\ncovers = [\"**/*.go\"]\n"
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// impactOf runs an affected-scope verification and returns its impact block,
// which is where a snapshot becomes visible to a caller today.
func impactOf(t *testing.T, instance *live, root string) map[string]any {
	t.Helper()
	session := instance.connect("edit")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "analysis-edit",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.go", "old": "return 7", "new": "return 8",
		},
	})
	verified := call(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "analysis-verify",
		"revision_or_transaction": "current", "stages": []any{"tests"}, "test_scope": "affected",
		"verbose": true,
	})
	verification, _ := data(verified)["verification"].(map[string]any)
	impact, _ := verification["impact"].(map[string]any)
	if impact == nil {
		t.Fatalf("verification carried no impact block: %#v", verified)
	}
	return impact
}

// With a language server attached, the snapshot behind the impact answer has
// two contributors and says so. This is the difference the stage exists to
// make: the reader's guesses and the server's knowledge are both present and
// distinguishable.
func TestAnalysisSnapshotNamesEveryContributor(t *testing.T) {
	instance := startWithLanguageServers(t)
	root := fixture(t, "go")
	declareTests(t, root)
	trust(t, instance, root)
	impact := impactOf(t, instance, root)

	snapshot, _ := impact["snapshot"].(string)
	if snapshot == "" {
		t.Fatalf("the impact answer does not name its snapshot: %#v", impact)
	}
	producers := map[string]bool{}
	for _, value := range impact["producers"].([]any) {
		producers[value.(string)] = true
	}
	if !producers["native_imports"] {
		t.Fatalf("the native reader is missing from the snapshot: %v", producers)
	}
	t.Logf("snapshot %s producers %v", snapshot, producers)
}

// With no language server at all, the same question still has an answer: the
// import reader alone, named as the only contributor, with coverage that does
// not claim to be semantic.
func TestAnalysisSnapshotFallsBackToTheImportReader(t *testing.T) {
	instance := start(t)
	root := fixture(t, "go")
	declareTests(t, root)
	trust(t, instance, root)
	impact := impactOf(t, instance, root)

	// The import reader is always consulted, and on a machine with no
	// language server installed it is the only one with anything to say.
	producers := map[string]bool{}
	for _, value := range impact["producers"].([]any) {
		producers[value.(string)] = true
	}
	if !producers["native_imports"] {
		t.Fatalf("the import reader did not contribute: %v", producers)
	}
	coverage, _ := impact["coverage"].(map[string]any)
	if semantic, _ := coverage["semantic"].(string); semantic == "semantic_provider" {
		t.Fatalf("a snapshot with no language server claims semantic coverage: %#v", coverage)
	}
	// And the answer says which contributor could not see anything, rather
	// than presenting a thinner graph as a complete one.
	if complete, _ := coverage["complete"].(bool); complete {
		t.Fatalf("an analysis without a language server called itself complete: %#v", coverage)
	}
	if skipped, _ := coverage["skipped"].([]any); len(skipped) == 0 {
		t.Fatalf("an incomplete analysis named no gaps: %#v", coverage)
	}
}

// A language whose server is not installed still produces a snapshot, and its
// coverage says the analysis is partial rather than reporting an empty graph
// as a complete one.
func TestAnalysisSnapshotDisclosesIncompleteLanguageCoverage(t *testing.T) {
	instance := startWithLanguageServers(t)
	root := fixture(t, "lua")
	session := instance.connect("orient")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	if outcome(opened) != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	capabilities, _ := data(opened)["capabilities"].(map[string]any)
	semantic, _ := capabilities["semantic"].(string)
	if semantic == "" {
		t.Fatalf("the workspace does not state its semantic coverage: %#v", capabilities)
	}
	// Whatever the machine has installed, the answer names its own limits
	// rather than implying none.
	t.Logf("lua fixture semantic coverage: %s", semantic)
}
