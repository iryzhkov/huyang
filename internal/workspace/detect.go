package workspace

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// detectPipelinePolicy fills policy with the commands a repository's layout
// implies when it has no .huyang.toml: gofmt, go build, go vet and go test
// for a Go module; compileall, ruff (when installed), pytest (when
// installed) or unittest for a Python project; npm test for a package.json
// with a test script. Detection never runs anything; execution still needs
// the workspace root to be trusted in the user policy, and a .huyang.toml
// replaces the detected commands entirely.
func detectPipelinePolicy(root string, policy *PipelinePolicy) {
	if exists(root, "go.mod") {
		policy.Detected = append(policy.Detected, "go.mod")
		policy.Format.Mode, policy.Format.Scope = "check", "whole_repository"
		policy.Format.Gate = CommandPolicy{Name: "gofmt", Command: []string{"sh", "-c", `test -z "$(gofmt -l .)"`}}
		policy.Check = append(policy.Check,
			CommandPolicy{Name: "go-build", Command: []string{"go", "build", "./..."}, Required: true},
			CommandPolicy{Name: "go-vet", Command: []string{"go", "vet", "./..."}, Required: true})
		policy.Tests = append(policy.Tests, CommandPolicy{Name: "go-test", Command: []string{"go", "test", "./..."},
			Covers: []string{"**/*.go", "go.mod", "go.sum"}, Variants: []string{"default"}})
	}
	if marker := pythonMarker(root); marker != "" {
		policy.Detected = append(policy.Detected, marker)
		// A syntax check through ast.parse writes nothing, unlike compileall.
		policy.Check = append(policy.Check, CommandPolicy{Name: "python-syntax", Required: true, Command: []string{"python3", "-B", "-c",
			"import ast, pathlib, sys\nbad = 0\nfor p in pathlib.Path('.').rglob('*.py'):\n    try:\n        ast.parse(p.read_text(), str(p))\n    except SyntaxError as e:\n        bad += 1\n        print(f'{p}:{e.lineno}: {e.msg}')\nsys.exit(1 if bad else 0)"}})
		if _, err := exec.LookPath("ruff"); err == nil {
			policy.Check = append(policy.Check, CommandPolicy{Name: "ruff", Command: []string{"ruff", "check", "."}})
		}
		// -B keeps the interpreter from writing bytecode: the tests stage is
		// non-mutating and refuses any file the command leaves behind.
		tests := []string{"python3", "-B", "-m", "unittest"}
		if _, err := exec.LookPath("pytest"); err == nil {
			tests = []string{"python3", "-B", "-m", "pytest", "-q", "-p", "no:cacheprovider"}
		}
		policy.Tests = append(policy.Tests, CommandPolicy{Name: "python-test", Command: tests,
			Covers: []string{"**/*.py", "pyproject.toml", "setup.cfg", "pytest.ini"}, Variants: []string{"default"}})
	}
	if packageHasTestScript(root) {
		policy.Detected = append(policy.Detected, "package.json")
		policy.Tests = append(policy.Tests, CommandPolicy{Name: "npm-test", Command: []string{"npm", "test", "--silent"},
			Covers: []string{"**/*.js", "**/*.jsx", "**/*.ts", "**/*.tsx", "**/*.mjs", "package.json"}, Variants: []string{"default"}})
	}
	if len(policy.Detected) > 0 && len(policy.Variants) == 0 {
		policy.Variants = []VariantPolicy{{Name: "default", Files: []string{"**"}, Required: true}}
	}
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// pythonMarker returns the file that identifies a Python project, or a
// directory of Python files when there is no project file.
func pythonMarker(root string) string {
	for _, name := range []string{"pyproject.toml", "setup.py", "setup.cfg", "pytest.ini", "tox.ini", "requirements.txt"} {
		if exists(root, name) {
			return name
		}
	}
	for _, dir := range []string{".", "tests", "src"} {
		matches, _ := filepath.Glob(filepath.Join(root, dir, "*.py"))
		if len(matches) > 0 {
			return filepath.ToSlash(filepath.Join(dir, "*.py"))
		}
	}
	return ""
}

func packageHasTestScript(root string) bool {
	content, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(content, &manifest) != nil {
		return false
	}
	test := strings.TrimSpace(manifest.Scripts["test"])
	return test != "" && !strings.Contains(test, "no test specified")
}
