package bridge

import (
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A symbol handle that spans a whole declaration is positioned on the
// identifier inside it, which is where a language server expects the cursor.
func TestProviderTargetPointsAtSymbolIdentifier(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"report.go": "package report\n\nfunc OperationalSummary() string { return \"ok\" }\n",
	})
	workspace := direct.get(workspacecore.ID(workspaceID))
	symbol, err := workspace.RegisterSymbolHandle(
		"report.go", "OperationalSummary", "function", len("package report\n\n"), len("package report\n\nfunc OperationalSummary() string { return \"ok\" }"),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := modernProviderTarget(workspace, map[string]any{"handle": string(symbol.Handle)})
	if err != nil {
		t.Fatal(err)
	}
	if target["line"] != 3 || target["col"] != 6 || target["symbol"] != "OperationalSummary" {
		t.Fatalf("semantic target does not point at identifier: %#v", target)
	}
}
