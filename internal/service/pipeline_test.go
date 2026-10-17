package service

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
)

// trustedEmbedProject writes a project whose root the user policy trusts,
// with projectConfig as its .huyang.toml, and selects the embedded provider
// so change_plan prepare runs against a real sandbox.
func trustedEmbedProject(t *testing.T, projectConfig string, files map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	previousFactory := providerpool.DefaultFactory
	providerpool.DefaultFactory = providerpool.ConfiguredFactory{Backend: "embed"}
	t.Cleanup(func() { providerpool.DefaultFactory = previousFactory })
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root, configHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "huyang"), 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := "[trust]\nroots = [\"" + root + "\"]\n"
	if err := os.WriteFile(filepath.Join(configHome, "huyang", "config.toml"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte(projectConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// preparedStageStatuses maps each stage a preparation recorded to its status.
func preparedStageStatuses(t *testing.T, preparation map[string]any) map[string]any {
	t.Helper()
	statuses := map[string]any{}
	stages, _ := preparation["verification"].([]any)
	for _, value := range stages {
		stage, _ := value.(map[string]any)
		name, _ := stage["stage"].(string)
		statuses[name] = stage["status"]
	}
	return statuses
}

func TestOfficialClientRunsTrustedPipelineAgainstPreparedSandbox(t *testing.T) {
	projectConfig := "version = 1\n[format]\nmode = \"check\"\nscope = \"whole_repository\"\n" +
		"[format.transform]\ncommand = [\"sh\", \"-c\", \"sed -i s/DELTA/delta/ note.txt; printf tool > extra.txt\"]\n" +
		"declared_writes = [\"note.txt\", \"extra.txt\"]\n" +
		"[[check]]\ncommand = [\"sh\", \"-c\", \"grep -q delta note.txt\"]\n" +
		`[[tests]]
name = "note-unit"
command = ["sh", "-c", "test \"$(cat note.txt)\" = \"alpha delta gamma\""]
`
	root := trustedEmbedProject(t, projectConfig, map[string]string{"note.txt": "alpha beta gamma\n"})
	file := filepath.Join(root, "note.txt")
	extra := filepath.Join(root, "extra.txt")
	if err := os.WriteFile(extra, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	policy := inspected["data"].(map[string]any)["pipeline_policy"].(map[string]any)
	if policy["trusted"] != true {
		t.Fatalf("pipeline policy not visibly trusted: %#v", inspected)
	}
	searched := callModern(t, session, "search", map[string]any{"workspace_id": workspaceID, "query": "beta", "mode": "literal", "include_handles": true})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"handle": hit["handle"]}, "content": "DELTA", "indentation": "formatter",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID, planRevision := plan["plan_id"].(string), plan["plan_revision"].(float64)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-prepare", "action": "prepare",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if prepared["outcome"] != "provisional" {
		t.Fatalf("prepare = %#v", prepared)
	}
	preparedPlan := prepared["data"].(map[string]any)["plan"].(map[string]any)
	preparation := preparedPlan["preparation"].(map[string]any)
	if len(preparation["tool_delta"].([]any)) != 2 {
		t.Fatalf("tool delta = %#v", preparation)
	}
	// The trusted check and tests ran inside the prepare sandbox, against the
	// transformed bytes; prepare is the only place Huyang runs them.
	if statuses := preparedStageStatuses(t, preparation); statuses["check"] != "passed" || statuses["tests"] != "passed" {
		t.Fatalf("prepare did not gate on the trusted check and tests: %#v", preparation["verification"])
	}
	preparedRevision := preparation["prepared_revision"].(string)
	applied := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-apply", "action": "apply",
		"plan_id": planID, "plan_revision": planRevision, "prepared_revision": preparedRevision,
		"accept_provisional": true,
	})
	if applied["outcome"] != "provisional" {
		t.Fatalf("apply = %#v", applied)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, []byte("alpha delta gamma\n")) {
		t.Fatalf("canonical transformed bytes = %q, %v", got, err)
	}
	if got, err := os.ReadFile(extra); err != nil || !bytes.Equal(got, []byte("tool")) {
		t.Fatalf("canonical whole-repository tool delta = %q, %v", got, err)
	}
}

// A trusted root whose test suite fails cannot be prepared: the gate is not
// advisory, and nothing reaches the canonical tree.
func TestPrepareFailsWhenTrustedTestsFail(t *testing.T) {
	projectConfig := "version = 1\n[[tests]]\nname = \"always-fails\"\ncommand = [\"sh\", \"-c\", \"exit 7\"]\n"
	root := trustedEmbedProject(t, projectConfig, map[string]string{"note.txt": "alpha\n"})
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "failing-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "add", "kind": "create_file", "path": "added.txt", "content": "new\n",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "failing-prepare", "action": "prepare",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	if prepared["outcome"] != "failed" {
		t.Fatalf("prepare with a failing trusted test suite = %#v", prepared)
	}
	if _, err := os.Stat(filepath.Join(root, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("a failed preparation reached the canonical tree: %v", err)
	}
}
