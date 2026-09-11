package bridge

import (
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// A symbol locator that no parser or provider can resolve answers
// unavailable with a literal-search fallback instead of returning the whole
// file as if it were the symbol.
func TestReadSymbolLocatorWithoutParserIsUnavailableNotWholeFile(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"main.rb": "class Widget\n  def call\n    :ok\n  end\nend\n",
	})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID,
		"target": map[string]any{"symbol_locator": map[string]any{
			"path": filepath.Join(root, "main.rb"), "name_path": "Widget#call",
		}},
		"view": "source",
	})
	if result["outcome"] != "unavailable" || result["code"] != "semantic_provider_unavailable" {
		t.Fatalf("text-only symbol read made an unsupported semantic claim: %#v", result)
	}
	data := result["data"].(map[string]any)
	if _, leaked := data["content"]; leaked {
		t.Fatalf("unresolved symbol locator silently returned file content: %#v", result)
	}
	next := result["next"].([]any)
	if len(next) == 0 || next[0].(map[string]any)["tool"] != "search" || next[0].(map[string]any)["query"] != "Widget#call" {
		t.Fatalf("symbol fallback is not actionable: %#v", result)
	}
}
