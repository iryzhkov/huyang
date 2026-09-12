package workspace

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// detectPipelinePolicy fills policy with the commands a repository's layout
// implies when it has no .huyang.toml. Detection reads the project, never
// the service's environment alone: a Makefile's test, lint and check targets
// come first; a Go module gets gofmt, go build, go vet and go test; a Python
// project gets a syntax check, ruff when the project configures it or has
// it installed, and pytest when the project declares it or has it installed,
// each run through the project's own interpreter (.venv, then uv or poetry
// when their lockfile is present, then python3); a package.json with a test
// script gets npm test. Detection never runs anything; execution still
// needs the workspace root to be trusted in the user policy, and a
// .huyang.toml replaces the detected commands entirely.
func detectPipelinePolicy(root string, policy *PipelinePolicy) {
	targets := makefileTargets(root)
	if len(targets) > 0 {
		policy.Detected = append(policy.Detected, "Makefile")
		for _, name := range []string{"lint", "check"} {
			if targets[name] {
				policy.Check = append(policy.Check, CommandPolicy{Name: "make-" + name, Command: []string{"make", name}})
			}
		}
		if targets["test"] {
			policy.Tests = append(policy.Tests, CommandPolicy{Name: "make-test", Command: []string{"make", "test"},
				Covers: []string{"**"}, Variants: []string{"default"}})
		}
	}
	makeTests, makeLint := targets["test"], targets["lint"] || targets["check"]
	if exists(root, "go.mod") {
		detectGo(policy, makeTests)
	}
	if marker := pythonMarker(root); marker != "" {
		detectPython(root, marker, policy, makeTests, makeLint)
	}
	if !makeTests && packageHasTestScript(root) {
		policy.Detected = append(policy.Detected, "package.json")
		policy.Tests = append(policy.Tests, CommandPolicy{Name: "npm-test", Command: []string{"npm", "test", "--silent"},
			Covers: []string{"**/*.js", "**/*.jsx", "**/*.ts", "**/*.tsx", "**/*.mjs", "package.json"}, Variants: []string{"default"}})
	}
	if len(policy.Detected) > 0 && len(policy.Variants) == 0 {
		policy.Variants = []VariantPolicy{{Name: "default", Files: []string{"**"}, Required: true}}
	}
}

func detectGo(policy *PipelinePolicy, makeTests bool) {
	policy.Detected = append(policy.Detected, "go.mod")
	policy.Format.Mode, policy.Format.Scope = "check", "whole_repository"
	policy.Format.Gate = CommandPolicy{Name: "gofmt", Command: []string{"sh", "-c", `test -z "$(gofmt -l .)"`}}
	policy.Check = append(policy.Check,
		CommandPolicy{Name: "go-build", Command: []string{"go", "build", "./..."}, Required: true},
		CommandPolicy{Name: "go-vet", Command: []string{"go", "vet", "./..."}, Required: true})
	if !makeTests {
		policy.Tests = append(policy.Tests, CommandPolicy{Name: "go-test", Command: []string{"go", "test", "./..."},
			Covers: []string{"**/*.go", "go.mod", "go.sum"}, Variants: []string{"default"}})
	}
}

// detectPython derives the Python commands from what the project declares.
// The interpreter and tools come from the project's own environment first,
// so a pytest or ruff pinned in a uv or poetry venv is found even when the
// service's PATH has neither.
func detectPython(root, marker string, policy *PipelinePolicy, makeTests, makeLint bool) {
	policy.Detected = append(policy.Detected, marker)
	environment := pythonEnvironment(root)
	policy.Detected = append(policy.Detected, environment.detected...)
	// A syntax check through ast.parse writes nothing, unlike compileall.
	policy.Check = append(policy.Check, CommandPolicy{Name: "python-syntax", Required: true, Command: append(environment.python(), "-B", "-c",
		"import ast, pathlib, sys\nbad = 0\nfor p in pathlib.Path('.').rglob('*.py'):\n    if '.venv' in p.parts or 'venv' in p.parts or 'node_modules' in p.parts:\n        continue\n    try:\n        ast.parse(p.read_text(), str(p))\n    except SyntaxError as e:\n        bad += 1\n        print(f'{p}:{e.lineno}: {e.msg}')\nsys.exit(1 if bad else 0)")})
	if !makeLint && (pyprojectHas(root, "[tool.ruff") || exists(root, "ruff.toml") || exists(root, ".ruff.toml") || environment.hasTool("ruff")) {
		if command := environment.tool("ruff"); command != nil {
			policy.Check = append(policy.Check, CommandPolicy{Name: "ruff", Command: append(command, "check", ".")})
		}
	}
	if makeTests {
		return
	}
	// -B keeps the interpreter from writing bytecode: the tests stage is
	// non-mutating and refuses any file the command leaves behind.
	tests := append(environment.python(), "-B", "-m", "unittest")
	if pytestDeclared(root) || environment.hasTool("pytest") {
		tests = append(environment.python(), "-B", "-m", "pytest", "-q", "-p", "no:cacheprovider")
	}
	policy.Tests = append(policy.Tests, CommandPolicy{Name: "python-test", Command: tests,
		Covers: []string{"**/*.py", "pyproject.toml", "setup.cfg", "pytest.ini"}, Variants: []string{"default"}})
}

// pythonEnv is how a project's Python tools are reached: a venv directory
// (absolute, so the sandbox copy of the tree finds the canonical venv), a
// lockfile runner (uv or poetry), or the ambient python3.
type pythonEnv struct {
	venvBin  string
	runner   []string
	detected []string
}

func pythonEnvironment(root string) pythonEnv {
	var environment pythonEnv
	for _, name := range []string{".venv", "venv", "env"} {
		if exists(root, filepath.Join(name, "bin", "python")) {
			environment.venvBin = filepath.Join(root, name, "bin")
			environment.detected = append(environment.detected, name)
			break
		}
	}
	if environment.venvBin == "" {
		switch {
		case exists(root, "uv.lock"):
			environment.runner = []string{"uv", "run", "--no-sync"}
			environment.detected = append(environment.detected, "uv.lock")
		case exists(root, "poetry.lock"):
			environment.runner = []string{"poetry", "run"}
			environment.detected = append(environment.detected, "poetry.lock")
		}
	}
	return environment
}

// python is the interpreter command prefix for this environment.
func (e pythonEnv) python() []string {
	if e.venvBin != "" {
		return []string{filepath.Join(e.venvBin, "python")}
	}
	if e.runner != nil {
		return append(append([]string(nil), e.runner...), "python")
	}
	return []string{"python3"}
}

// hasTool reports whether the tool is installed where this environment
// would run it: in the venv, or on the service's PATH otherwise.
func (e pythonEnv) hasTool(name string) bool {
	if e.venvBin != "" {
		_, err := os.Stat(filepath.Join(e.venvBin, name))
		return err == nil
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// tool is the command that runs a tool in this environment, or nil when
// the environment has no way to reach it.
func (e pythonEnv) tool(name string) []string {
	switch {
	case e.venvBin != "":
		if _, err := os.Stat(filepath.Join(e.venvBin, name)); err == nil {
			return []string{filepath.Join(e.venvBin, name)}
		}
		return nil
	case e.runner != nil:
		return append(append([]string(nil), e.runner...), name)
	default:
		if _, err := exec.LookPath(name); err == nil {
			return []string{name}
		}
		return nil
	}
}

// pytestDeclared reports whether the project configures or depends on
// pytest, which makes it the runner even when it is not installed on the
// service's PATH.
func pytestDeclared(root string) bool {
	return pyprojectHas(root, "[tool.pytest") || exists(root, "pytest.ini") ||
		fileContains(filepath.Join(root, "setup.cfg"), "[tool:pytest]") ||
		fileContains(filepath.Join(root, "tox.ini"), "[pytest]") ||
		pyprojectHas(root, "\"pytest") || pyprojectHas(root, "'pytest")
}

func pyprojectHas(root, needle string) bool {
	return fileContains(filepath.Join(root, "pyproject.toml"), needle)
}

func fileContains(path, needle string) bool {
	content, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(content), needle)
}

var makefileTarget = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]*:([^=]|$)`)

// makefileTargets lists the plain rule targets of the repository's
// Makefile (GNUmakefile, makefile and Makefile in GNU make's order).
// Pattern rules, variable assignments, comments and recipe lines are
// ignored.
func makefileTargets(root string) map[string]bool {
	var file *os.File
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if opened, err := os.Open(filepath.Join(root, name)); err == nil {
			file = opened
			break
		}
	}
	if file == nil {
		return nil
	}
	defer file.Close()
	targets := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") || !makefileTarget.MatchString(line) {
			continue
		}
		for _, name := range strings.Fields(strings.SplitN(line, ":", 2)[0]) {
			targets[name] = true
		}
	}
	return targets
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
