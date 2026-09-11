package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParserStageParsesCommonStructuredConfig(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"config.toml":  "enabled = true\n",
		"config.yaml":  "service:\n  enabled: true\n",
		"compose.yml":  "services:\n  api:\n    image: example/api\n",
		"events.jsonl": "{\"event\":\"start\"}\n{\"event\":\"stop\"}\n",
	}
	var paths []string
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	stage := parserStage(root, "wsrev_test", paths)
	if stage.Status != VerificationPassed || !stage.Coverage.Complete {
		t.Fatalf("common config parser stage = %#v", stage)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.yaml"), []byte("key: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage = parserStage(root, "wsrev_test", []string{"broken.yaml"})
	if stage.Status != VerificationFailed || !strings.Contains(stage.Output, "broken.yaml") {
		t.Fatalf("invalid YAML was not diagnosed: %#v", stage)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.jsonl"), []byte("{\"ok\":true}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage = parserStage(root, "wsrev_test", []string{"broken.jsonl"})
	if stage.Status != VerificationFailed || !strings.Contains(stage.Output, "broken.jsonl:2") {
		t.Fatalf("invalid JSONL was not diagnosed by line: %#v", stage)
	}
}

func gitIgnoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		".gitignore": ".ruby-lsp/\n", "tracked.txt": "tracked\n", "untracked.txt": "untracked\n",
		".ruby-lsp/Gemfile.lock": "generated\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", ".gitignore", "tracked.txt"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func TestProjectInventoryHonorsGitIgnore(t *testing.T) {
	root := gitIgnoreFixture(t)
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := workspace.collectFiles()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(files, "\n")
	if strings.Contains(joined, ".ruby-lsp") || !strings.Contains(joined, "tracked.txt") || !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("git-aware inventory = %v", files)
	}
}

func TestProjectInventoryIgnoresAmbientGitEnvironment(t *testing.T) {
	root := gitIgnoreFixture(t)
	// With the ambient environment these settings would redirect Git to a
	// foreign repository and make it execute an fsmonitor hook; the native
	// inventory must run Git only through the sanitized runner.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(t.TempDir(), "missing-hook"))
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	files, coverage, err := workspace.collectFiles()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(files, "\n")
	if strings.Contains(joined, ".ruby-lsp") || !strings.Contains(joined, "tracked.txt") || !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("inventory under hostile git environment = %v (coverage %+v)", files, coverage)
	}
}

func TestLoadPipelinePolicyRejectsUnknownAffectedKey(t *testing.T) {
	root := t.TempDir()
	policy := "version = 1\n[[tests]]\nname = \"unit\"\ncommand = [\"true\"]\naffected = [\"src/**\"]\n"
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPipelinePolicy(root, filepath.Join(root, "missing-user-config.toml"))
	if err == nil || !strings.Contains(err.Error(), "use covers") {
		t.Fatalf("expected actionable unknown-key error, got %v", err)
	}
}

func TestCaptureTreeQuotaErrorIsActionable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.bin"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := captureTree(t.Context(), root, 4)
	if err == nil || !strings.Contains(err.Error(), "observed_bytes=10") || !strings.Contains(err.Error(), "resource.max_snapshot_bytes") {
		t.Fatalf("quota error = %v", err)
	}
}
