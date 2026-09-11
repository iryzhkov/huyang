package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerificationPolicyFingerprintTracksTrustChanges(t *testing.T) {
	root := t.TempDir()
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "huyang")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[trust]\nroots = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untrusted := verificationPolicyFingerprint(root)
	trustedConfig := []byte("[trust]\nroots = [\"" + root + "\"]\n")
	if err := os.WriteFile(configPath, trustedConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	trusted := verificationPolicyFingerprint(root)
	if untrusted == trusted {
		t.Fatalf("verification cache fingerprint ignored trust change: %s", trusted)
	}
}

func TestChangePlanInspectAcceptsOmittedOptionalRevision(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "create-inspect-optional",
		"action":          "create",
		"operations":      []any{map[string]any{"op_id": "new", "kind": "create_file", "path": "new.txt", "content": "new\n"}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	inspected := callModern(t, session, "change_plan", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "inspect-without-revision",
		"action":          "inspect",
		"plan_id":         plan["plan_id"],
	})
	if inspected["outcome"] != "ok" {
		t.Fatalf("inspect without optional plan_revision = %#v", inspected)
	}
}

func TestUnknownEditHandleReturnsRestartRecovery(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "edit_apply", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "expired-handle",
		"operation":       map[string]any{"kind": "replace_range", "target": map[string]any{"handle": "rng_pre_restart"}, "content": "after"},
	})
	if result["code"] != "handle_resolve_failed" {
		t.Fatalf("unknown handle result = %#v", result)
	}
	next := result["next"].([]any)
	if len(next) != 2 || !strings.Contains(next[1].(map[string]any)["action"].(string), "fresh_handle") {
		t.Fatalf("unknown handle recovery = %#v", result)
	}
}
