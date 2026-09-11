package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOfficialClientRunsTrustedPipelineAgainstPreparedSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root, stateDir, configHome := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "huyang"), 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := "[trust]\nroots = [\"" + root + "\"]\n"
	if err := os.WriteFile(filepath.Join(configHome, "huyang", "config.toml"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	projectConfig := "version = 1\n[format]\nmode = \"check\"\nscope = \"whole_repository\"\n" +
		"[format.transform]\ncommand = [\"sh\", \"-c\", \"sed -i s/DELTA/delta/ note.txt; printf tool > extra.txt\"]\n" +
		"declared_writes = [\"note.txt\", \"extra.txt\"]\n" +
		"[[check]]\ncommand = [\"sh\", \"-c\", \"grep -q delta note.txt\"]\n" +
		`[[tests]]
name = "note-unit"
covers = ["note.txt"]
variants = ["default"]
command = ["sh", "-c", "test \"$(cat note.txt)\" = \"alpha delta gamma\""]
[[variants]]
name = "default"
files = ["**"]
required = true
`
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte(projectConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("alpha beta gamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, "extra.txt")
	if err := os.WriteFile(extra, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	policy := inspected["data"].(map[string]any)["pipeline_policy"].(map[string]any)
	if policy["trusted"] != true {
		t.Fatalf("pipeline policy not visibly trusted: %#v", inspected)
	}
	searched := callModern(t, session, "search", map[string]any{"workspace_id": workspaceID, "query": "beta", "mode": "literal"})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": "DELTA", "indentation": "formatter",
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
	preparedRevision := preparation["prepared_revision"].(string)
	verifyArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-verify",
		"revision_or_transaction": preparedRevision,
		"stages":                  []string{"format_gate", "parser", "diagnostics", "check", "tests"},
		"test_scope":              "full",
	}
	verified := callModern(t, session, "verify_run", verifyArguments)
	if verified["outcome"] != "partial" {
		t.Fatalf("verify = %#v", verified)
	}
	replayed := callModern(t, session, "verify_run", verifyArguments)
	if replayed["idempotency"] != "replayed" {
		t.Fatalf("verify replay = %#v", replayed)
	}
	verifyArguments["idempotency_key"] = "pipeline-verify-revision-cache"
	cached := callModern(t, session, "verify_run", verifyArguments)
	if cached["data"].(map[string]any)["cache"] != "revision_hit" {
		t.Fatalf("revision-keyed verification cache = %#v", cached)
	}
	affected := callModern(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-affected-verify",
		"revision_or_transaction": preparedRevision, "stages": []string{"tests"}, "test_scope": "affected",
	})
	affectedVerification := affected["data"].(map[string]any)["verification"].(map[string]any)
	if affectedVerification["full_test_gate"] != nil {
		t.Fatalf("affected verification claimed a full gate: %#v", affectedVerification)
	}
	targeted := affectedVerification["targeted_tests"].(map[string]any)
	if targeted["status"] != "affected_tests_passed" || len(targeted["executed"].([]any)) != 1 {
		t.Fatalf("targeted verification = %#v", affected)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "test-history", workspaceID+".json")); err != nil {
		t.Fatalf("revision-keyed test history: %v", err)
	}
	applied := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-apply", "action": "apply",
		"plan_id": planID, "plan_revision": planRevision, "prepared_revision": preparedRevision,
		"accept_provisional": true,
	})
	if applied["outcome"] != "provisional" {
		t.Fatalf("apply = %#v", applied)
	}
	appliedPlan := applied["data"].(map[string]any)["plan"].(map[string]any)
	canonicalRevision := appliedPlan["preparation"].(map[string]any)["canonical_revision"].(string)
	canonical := callModern(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "pipeline-canonical-verify",
		"revision_or_transaction": canonicalRevision, "stages": []string{"parser", "check", "tests"}, "test_scope": "full",
	})
	if canonical["outcome"] != "partial" {
		t.Fatalf("canonical verify = %#v", canonical)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, []byte("alpha delta gamma\n")) {
		t.Fatalf("canonical transformed bytes = %q, %v", got, err)
	}
	if got, err := os.ReadFile(extra); err != nil || !bytes.Equal(got, []byte("tool")) {
		t.Fatalf("canonical whole-repository tool delta = %q, %v", got, err)
	}
}
