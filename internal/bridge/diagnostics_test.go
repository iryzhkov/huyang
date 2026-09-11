package bridge

import (
	"strings"
	"testing"
)

// With no evidence recorded, diagnostics answers unavailable, does not claim
// to have retrieved anything, and points at the tools that can produce
// evidence.
func TestDiagnosticsWithoutEvidenceIsUnavailableWithRecovery(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "only once\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	diagnostics := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	if diagnostics["outcome"] != "unavailable" ||
		strings.Contains(diagnostics["summary"].(string), "retrieved") || len(diagnostics["next"].([]any)) == 0 {
		t.Fatalf("unavailable diagnostics are misleading: %#v", diagnostics)
	}
}
