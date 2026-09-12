package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func detectFixture(t *testing.T, files map[string]string) PipelinePolicy {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	policy, err := LoadPipelinePolicy(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// A Go module without .huyang.toml gets gofmt, go build, go vet and go test,
// a default variant, and stays untrusted until the user policy says so.
func TestDetectGoModuleCommands(t *testing.T) {
	policy := detectFixture(t, map[string]string{"go.mod": "module x\n", "main.go": "package x\n"})
	if len(policy.Detected) != 1 || policy.Detected[0] != "go.mod" || policy.ProjectConfig != "" {
		t.Fatalf("detected = %#v config=%q", policy.Detected, policy.ProjectConfig)
	}
	if len(policy.Check) != 2 || policy.Check[0].Name != "go-build" || policy.Check[1].Name != "go-vet" {
		t.Fatalf("checks = %#v", policy.Check)
	}
	if len(policy.Tests) != 1 || policy.Tests[0].Command[0] != "go" || len(policy.Tests[0].Variants) != 1 {
		t.Fatalf("tests = %#v", policy.Tests)
	}
	if policy.Format.Gate.Name != "gofmt" || policy.Format.Scope != "whole_repository" {
		t.Fatalf("format = %#v", policy.Format)
	}
	if len(policy.Variants) != 1 || policy.Variants[0].Name != "default" || policy.Trusted {
		t.Fatalf("variants=%#v trusted=%v", policy.Variants, policy.Trusted)
	}
}

// A Python project is recognised by its project file and gets a compile
// check and a test command; a repository with no known layout detects
// nothing and keeps the empty default policy.
func TestDetectPythonAndUnknownLayouts(t *testing.T) {
	python := detectFixture(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "x.py": "x = 1\n"})
	if len(python.Detected) != 1 || python.Detected[0] != "pyproject.toml" || len(python.Tests) != 1 || python.Check[0].Name != "python-syntax" {
		t.Fatalf("python policy = %#v", python)
	}
	if python.Tests[0].Command[3] != "unittest" && python.Tests[0].Command[3] != "pytest" {
		t.Fatalf("python tests = %#v", python.Tests[0].Command)
	}
	node := detectFixture(t, map[string]string{"package.json": `{"scripts":{"test":"node --test"}}`})
	if len(node.Detected) != 1 || node.Tests[0].Name != "npm-test" {
		t.Fatalf("node policy = %#v", node)
	}
	nothing := detectFixture(t, map[string]string{"README.md": "# x\n"})
	if len(nothing.Detected) != 0 || len(nothing.Check) != 0 || len(nothing.Tests) != 0 {
		t.Fatalf("unknown layout detected commands: %#v", nothing)
	}
}

// A project that declares pytest in pyproject runs it through its own venv
// interpreter (an absolute path, so the sandbox copy finds the canonical
// venv), even when the service's PATH has no pytest; ruff is added when
// the project configures it and the venv has it.
func TestDetectPythonUsesTheProjectEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	policy := detectFixture(t, map[string]string{
		"pyproject.toml":   "[project]\nname = \"x\"\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n[tool.ruff]\nline-length = 100\n",
		"uv.lock":          "",
		".venv/bin/python": "",
		".venv/bin/ruff":   "",
		"x.py":             "x = 1\n",
	})
	if len(policy.Tests) != 1 || !strings.HasSuffix(policy.Tests[0].Command[0], "/.venv/bin/python") || policy.Tests[0].Command[3] != "pytest" {
		t.Fatalf("python tests = %#v", policy.Tests)
	}
	if len(policy.Check) != 2 || policy.Check[1].Name != "ruff" || !strings.HasSuffix(policy.Check[1].Command[0], "/.venv/bin/ruff") {
		t.Fatalf("python checks = %#v", policy.Check)
	}
	if strings.Join(policy.Detected, ",") != "pyproject.toml,.venv" {
		t.Fatalf("detected = %#v", policy.Detected)
	}
	locked := detectFixture(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n[tool.pytest.ini_options]\n", "uv.lock": ""})
	if strings.Join(locked.Tests[0].Command[:4], " ") != "uv run --no-sync python" || locked.Tests[0].Command[6] != "pytest" {
		t.Fatalf("uv-locked tests = %#v", locked.Tests[0].Command)
	}
	if strings.Join(locked.Detected, ",") != "pyproject.toml,uv.lock" {
		t.Fatalf("uv detected = %#v", locked.Detected)
	}
	bare := detectFixture(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"})
	if bare.Tests[0].Command[0] != "python3" || bare.Tests[0].Command[3] != "unittest" || len(bare.Check) != 1 {
		t.Fatalf("bare python policy = %#v", bare)
	}
}

// A Makefile's test, lint and check targets are the project's own
// commands: they take the place of the language defaults for tests and
// optional linters, while required build and syntax checks stay.
func TestDetectMakefileTargetsComeFirst(t *testing.T) {
	policy := detectFixture(t, map[string]string{
		"Makefile":       ".PHONY: test lint gate\n\ntest:\n\tuv run pytest\n\nlint:\n\tuv run ruff check .\n\ngate: lint test\n\nVAR := x\n%.o: %.c\n\tcc $<\n",
		"pyproject.toml": "[project]\nname = \"x\"\n[tool.ruff]\n",
		"uv.lock":        "",
		"package.json":   `{"scripts":{"test":"node --test"}}`,
	})
	if len(policy.Tests) != 1 || policy.Tests[0].Name != "make-test" || strings.Join(policy.Tests[0].Command, " ") != "make test" {
		t.Fatalf("tests = %#v", policy.Tests)
	}
	names := make([]string, 0, len(policy.Check))
	for _, check := range policy.Check {
		names = append(names, check.Name)
	}
	if strings.Join(names, ",") != "make-lint,python-syntax" {
		t.Fatalf("checks = %v", names)
	}
	if policy.Detected[0] != "Makefile" {
		t.Fatalf("detected = %#v", policy.Detected)
	}
	goOnly := detectFixture(t, map[string]string{"Makefile": "check:\n\tgo vet ./...\n", "go.mod": "module x\n"})
	if len(goOnly.Tests) != 1 || goOnly.Tests[0].Name != "go-test" || goOnly.Check[0].Name != "make-check" || len(goOnly.Check) != 3 {
		t.Fatalf("go with make check = %#v", goOnly)
	}
}

// A checked-in .huyang.toml wins over detection entirely.
func TestProjectPolicyOverridesDetection(t *testing.T) {
	policy := detectFixture(t, map[string]string{
		"go.mod":       "module x\n",
		".huyang.toml": "version = 1\n[[check]]\nname = \"custom\"\ncommand = [\"true\"]\n",
	})
	if len(policy.Detected) != 0 || len(policy.Check) != 1 || policy.Check[0].Name != "custom" || len(policy.Tests) != 0 {
		t.Fatalf("declared policy was not authoritative: %#v", policy)
	}
}
