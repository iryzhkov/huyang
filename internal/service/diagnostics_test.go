package service

import (
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// With no evidence recorded, diagnostics answers ok with no findings and
// coverage that says it is incomplete, rather than unavailable, does not
// claim to have retrieved anything, and points at the tools that can produce
// evidence.
func TestDiagnosticsWithoutEvidenceAnswersEmptyWithIncompleteCoverage(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "only once\n"})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()

	diagnostics := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	if diagnostics["outcome"] != "ok" ||
		strings.Contains(diagnostics["summary"].(string), "retrieved") || len(diagnostics["next"].([]any)) == 0 ||
		len(diagnostics["warnings"].([]any)) == 0 {
		t.Fatalf("empty diagnostics are misleading: %#v", diagnostics)
	}
	data := diagnostics["data"].(map[string]any)
	coverage := data["coverage"].(map[string]any)
	if coverage["complete"] != false || coverage["reason"] != "no_diagnostic_evidence" {
		t.Fatalf("coverage = %#v", coverage)
	}
	if items := data["diagnostics"].(map[string]any)["new"].([]any); len(items) != 0 {
		t.Fatalf("findings = %#v", items)
	}
}
