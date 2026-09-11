package service

import (
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// A provider install that reports failure yields a failed tool result with
// recovery; it is never promoted to an attached language server.
func TestLanguageServerSetupInstallFailureIsNeverReportedAttached(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"CMakeLists.txt": "project(sample)\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	result := callModern(t, session, "language_server_setup", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "install-failure",
		"action": "install", "language": "cmake", "server": "cmake-language-server",
	})
	if result["outcome"] != "failed" || result["code"] != "language_server_install_failed" {
		t.Fatalf("failed provider installation was promoted: %#v", result)
	}
	if len(result["next"].([]any)) == 0 {
		t.Fatalf("failed installation omitted recovery: %#v", result)
	}
}
