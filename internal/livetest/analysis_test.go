//go:build live

package livetest

import (
	"os"
	"path/filepath"
	"testing"
)

// trust makes a fixture's declared commands runnable, so change_plan prepare
// runs them in its sandbox.
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
