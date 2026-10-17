package service

import (
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// Huyang installs no language servers. An install request is refused by the
// catalog before any handler runs, and the provider is never asked to
// install anything.
func TestLanguageServerSetupRefusesInstall(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"CMakeLists.txt": "project(sample)\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	result := callModern(t, session, "language_server_setup", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "install-refused",
		"action": "install", "language": "cmake",
	})
	if result["outcome"] != "failed" || result["code"] != "invalid_arguments" {
		t.Fatalf("an install request was not refused: %#v", result)
	}
	if backend.lastOperation == "install_language" {
		t.Fatalf("the provider was asked to install a language server: %#v", result)
	}
}
