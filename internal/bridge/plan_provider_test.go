package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestOfficialClientPreparesExclusiveUnsavedProviderBuffersAndDiscards(t *testing.T) {
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root := t.TempDir()
	stateDir := t.TempDir()
	file := filepath.Join(root, "note.txt")
	original := []byte("alpha beta gamma\n")
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{file},
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
		"include_ranges": true,
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "prepare-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": "DELTA",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID := plan["plan_id"].(string)
	planRevision := plan["plan_revision"].(float64)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "prepare", "action": "prepare",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if prepared["outcome"] != "provisional" || prepared["transaction"].(map[string]any)["state"] != "PROVISIONAL" {
		t.Fatalf("silent text provider must remain provisional: %#v", prepared)
	}
	if current, err := os.ReadFile(file); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("prepare changed canonical disk: %q, %v", current, err)
	}
	unblocked := callModern(t, session, "debug_session", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "non-owner-provider-call", "action": "start",
	})
	if unblocked["outcome"] == "conflict" && unblocked["code"] == "workspace_busy" {
		t.Fatalf("canonical provider was blocked by isolated staging: %#v", unblocked)
	}
	sandboxStager := direct.sandboxStagers[stagerKey{workspace: workspaceIDValue(workspaceID), planID: planID}]
	sandboxFile := filepath.Join(sandboxStager.sandbox.Tree, "note.txt")
	staged, err := os.ReadFile(sandboxFile)
	if err != nil || !bytes.Equal(staged, []byte("alpha DELTA gamma\n")) {
		t.Fatalf("sandbox does not contain prepared bytes: %q, %v", staged, err)
	}
	discarded := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "discard", "action": "discard",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if discarded["outcome"] != "ok" || discarded["transaction"].(map[string]any)["state"] != "ROLLED_BACK" {
		t.Fatalf("discard = %#v", discarded)
	}
	if _, err := os.Stat(sandboxStager.sandbox.Root); !os.IsNotExist(err) {
		t.Fatalf("discard did not remove owned sandbox: %v", err)
	}
}

func TestOfficialClientAppliesJournaledPlanAndResyncsProvider(t *testing.T) {
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root, stateDir := t.TempDir(), t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "huyang"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "huyang", "config.toml"), []byte("[trust]\nroots = [\""+root+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte("version = 1\n[[check]]\ncommand = [\"go\", \"version\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("alpha beta gamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{file},
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
		"include_ranges": true,
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": "DELTA",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID, planRevision := plan["plan_id"].(string), plan["plan_revision"].(float64)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply-prepare", "action": "prepare",
		"plan_id": planID, "plan_revision": planRevision,
	})
	preparedPlan := prepared["data"].(map[string]any)["plan"].(map[string]any)
	preparedRevision := preparedPlan["preparation"].(map[string]any)["prepared_revision"].(string)
	applied := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply", "action": "apply",
		"plan_id": planID, "plan_revision": planRevision, "prepared_revision": preparedRevision,
		"accept_provisional": true,
	})
	if applied["outcome"] != "provisional" || applied["transaction"].(map[string]any)["state"] != "COMMITTED" {
		t.Fatalf("apply = %#v", applied)
	}
	if current, err := os.ReadFile(file); err != nil || !bytes.Equal(current, []byte("alpha DELTA gamma\n")) {
		t.Fatalf("canonical apply = %q, %v", current, err)
	}
	sandboxStager := direct.sandboxStagers[stagerKey{workspace: workspaceIDValue(workspaceID), planID: planID}]
	if _, err := os.Stat(sandboxStager.sandbox.Root); !os.IsNotExist(err) {
		t.Fatalf("commit did not remove owned sandbox: %v", err)
	}
}

func workspaceIDValue(value string) workspacecore.ID {
	return workspacecore.ID(value)
}

func firstProviderLine(value any) string {
	result, _ := value.(map[string]any)
	raw, _ := result["lines"].([]any)
	if len(raw) == 0 {
		if strings, ok := result["lines"].([]string); ok && len(strings) > 0 {
			return strings[0]
		}
		return ""
	}
	line, _ := raw[0].(string)
	return line
}

func TestProviderBackedPrepareReceiptIsNotReplayedAfterRestart(t *testing.T) {
	if !providerBackedReplay(map[string]any{"transaction": map[string]any{"state": "READY"}}) {
		t.Fatal("READY receipt would replay after its provider buffers vanished")
	}
	if providerBackedReplay(map[string]any{"transaction": map[string]any{"state": "PREVIEWED"}}) {
		t.Fatal("durable preview receipt was incorrectly invalidated")
	}
}

func TestOfficialClientPreparesMissingFileWithoutRevisionPlaceholder(t *testing.T) {
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()

	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))

	root, stateDir := t.TempDir(), t.TempDir()
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()

	opened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "project", "root": root,
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "create-missing-plan", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "create-readme", "kind": "create_file",
			"path": "README.md", "content": "# Created\n",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID, planRevision := plan["plan_id"].(string), plan["plan_revision"].(float64)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "prepare-missing-plan", "action": "prepare",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if prepared["outcome"] != "provisional" || prepared["transaction"].(map[string]any)["state"] != "PROVISIONAL" {
		t.Fatalf("prepare missing file = %#v", prepared)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("prepare wrote missing file to canonical disk: %v", err)
	}
	stager := direct.sandboxStagers[stagerKey{workspace: workspaceIDValue(workspaceID), planID: planID}]
	request, _, ok := stager.PreparedRequest()
	if !ok || len(request.Files) != 1 {
		t.Fatalf("prepared request = %#v, available=%t", request, ok)
	}
	file := request.Files[0]
	if file.Path != "README.md" || file.BeforeExists || len(file.Before) != 0 {
		t.Fatalf("missing-file preimage path=%q exists=%t bytes=%q", file.Path, file.BeforeExists, file.Before)
	}
	staged, err := os.ReadFile(filepath.Join(stager.sandbox.Tree, "README.md"))
	if err != nil || !bytes.Equal(staged, []byte("# Created\n")) {
		t.Fatalf("sandbox missing staged create: %q, %v", staged, err)
	}
}
